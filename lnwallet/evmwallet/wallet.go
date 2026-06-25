// Package evmwallet implements lnwallet.WalletController for EVM-compatible
// chains. Like the Sui adapter (lnwallet/suiwallet), it reuses LND's existing
// key derivation (secp256k1, BtcWalletKeyRing) and only swaps the on-chain
// settlement layer: balances come from an ERC20 contract, "transactions" are
// ChannelManager calls signed EIP-155 and broadcast over JSON-RPC, and the many
// Bitcoin-specific UTXO/PSBT methods are unsupported stubs.
package evmwallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
	base "github.com/btcsuite/btcwallet/wallet"
	"github.com/btcsuite/btcwallet/wallet/txauthor"
	"github.com/btcsuite/btcwallet/wtxmgr"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/fn/v2"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// ErrUnsupported is returned for Bitcoin-specific wallet operations that have no
// EVM analogue (UTXO scans, PSBTs, output leasing, …).
var ErrUnsupported = errors.New("evmwallet: operation not supported on EVM")

// rpcTimeout bounds individual JSON-RPC calls.
const rpcTimeout = 30 * time.Second

// Config holds the dependencies of the EVM adapter wallet.
type Config struct {
	// KeyRing derives the node's secp256k1 keys (coin type 60 / testnet).
	KeyRing keychain.SecretKeyRing

	// Client is the EVM JSON-RPC client.
	Client evmnotify.EvmClient

	// Params identifies the sub-network (chain id, token, contract, coin
	// type, genesis hash).
	Params chainreg.EvmParams

	// TokenDecimals is the ERC20 asset's decimals, used by the Decimals
	// Scaling Factor.
	TokenDecimals uint8

	// GasLimit is the gas ceiling applied to ChannelManager calls.
	GasLimit uint64

	// NodeKeyIndex is the KeyFamilyNodeKey index from which the node's
	// on-chain settlement account is derived (default 0). It is exposed via
	// --evm.keyindex so an operator can rotate the settlement address to a
	// fresh key independently of the Lightning node identity (which stays at
	// index 0). NOTE: this does not help if the wallet SEED leaks — every
	// index derives from the same seed; true rotation requires a new seed.
	NodeKeyIndex uint32
}

// maxTxJournal bounds the in-memory broadcast journal (newest kept).
const maxTxJournal = 1024

// journalTx records one transaction this node broadcast, so ListTransactionDetails
// can report the node's own on-chain activity. EVM has no wallet-local tx index
// (a full history needs an external indexer), so this is a best-effort,
// process-lifetime journal of what this node itself sent.
type journalTx struct {
	hash chainhash.Hash
	when time.Time
}

// Wallet adapts an EVM chain to lnwallet.WalletController.
type Wallet struct {
	cfg Config
	mu  sync.Mutex

	// txMu guards txJournal, the bounded in-memory record of the node's own
	// broadcast transactions surfaced by ListTransactionDetails.
	txMu      sync.Mutex
	txJournal []journalTx
}

// recordTx appends a broadcast transaction to the bounded in-memory journal.
func (w *Wallet) recordTx(hash chainhash.Hash) {
	w.txMu.Lock()
	defer w.txMu.Unlock()

	w.txJournal = append(w.txJournal, journalTx{hash: hash, when: time.Now()})
	if len(w.txJournal) > maxTxJournal {
		w.txJournal = w.txJournal[len(w.txJournal)-maxTxJournal:]
	}
}

// Compile-time assertion that Wallet satisfies the interface.
var _ lnwallet.WalletController = (*Wallet)(nil)

// New creates a new EVM wallet backed by the given configuration.
func New(cfg Config) *Wallet {
	return &Wallet{cfg: cfg}
}

// nodeAddress derives the node's 20-byte EVM account address.
func (w *Wallet) nodeAddress() (common.Address, error) {
	keyDesc, err := w.cfg.KeyRing.DeriveKey(keychain.KeyLocator{
		Family: keychain.KeyFamilyNodeKey,
		Index:  w.cfg.NodeKeyIndex,
	})
	if err != nil {
		return common.Address{}, err
	}
	addr := input.EvmAddressFromPubKey(keyDesc.PubKey)

	return common.BytesToAddress(addr[:]), nil
}

// ConfirmedBalance returns the node's ERC20 balance scaled into LND's internal
// btcutil.Amount via the Decimals Scaling Factor.
func (w *Wallet) ConfirmedBalance(_ int32, _ string) (btcutil.Amount, error) {
	addr, err := w.nodeAddress()
	if err != nil {
		return 0, err
	}

	data, err := evmnotify.PackBalanceOf(addr)
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	token := common.HexToAddress(w.cfg.Params.TokenAddress)
	out, err := w.cfg.Client.CallContract(ctx, ethereum.CallMsg{
		To:   &token,
		Data: data,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("evmwallet: balanceOf call: %w", err)
	}

	raw, err := evmnotify.UnpackBalanceOf(out)
	if err != nil {
		return 0, err
	}

	return ScaleToInternal(raw, w.cfg.TokenDecimals), nil
}

// NativeGasBalance returns the node account's native-coin (ETH) balance in
// wei. Channel operations (openChannel, settlement calls) pay gas from this
// balance, separately from the ERC20 channel asset, so operators need it to
// know the node can still act on-chain.
func (w *Wallet) NativeGasBalance() (*big.Int, error) {
	addr, err := w.nodeAddress()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	return w.cfg.Client.BalanceAt(ctx, addr, nil)
}

// SendTokens transfers `amount` raw token base-units (10^tokenDecimals per
// token — e.g. 1_000_000 = 1 USDC at 6 decimals) of the sub-network's ERC20
// asset from the node account to `recipient` (a 0x-prefixed EVM address),
// returning the broadcast transaction hash. It is the on-chain transfer
// primitive behind the SendCoins RPC, the EVM analogue of suiwallet.SendSui.
func (w *Wallet) SendTokens(recipient string, amount uint64) (chainhash.Hash,
	error) {

	var zero chainhash.Hash

	if !common.IsHexAddress(recipient) {
		return zero, fmt.Errorf("evmwallet: %q is not a valid EVM "+
			"address", recipient)
	}

	data, err := evmnotify.PackTransfer(
		common.HexToAddress(recipient),
		new(big.Int).SetUint64(amount),
	)
	if err != nil {
		return zero, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	token := common.HexToAddress(w.cfg.Params.TokenAddress)

	return w.broadcastCall(ctx, EvmCall{To: token, Data: data})
}

// nativeTransferGas is the intrinsic gas of a plain value transfer (no calldata).
const nativeTransferGas = 21000

// SweepNative sends the node account's entire native-coin (ETH) balance, less a
// gas reserve, to `recipient` (a 0x-prefixed EVM address), returning the
// broadcast transaction hash. It is the native-coin counterpart to SendTokens
// (which moves the ERC20 channel asset): operators use it to reclaim a node's
// leftover gas balance — e.g. an itest sweeping throwaway nodes back to the
// funder on teardown so each run doesn't permanently strand its gas.
func (w *Wallet) SweepNative(recipient string) (chainhash.Hash, error) {
	var zero chainhash.Hash

	if !common.IsHexAddress(recipient) {
		return zero, fmt.Errorf("evmwallet: %q is not a valid EVM "+
			"address", recipient)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	from, err := w.nodeAddress()
	if err != nil {
		return zero, err
	}

	balance, err := w.cfg.Client.BalanceAt(ctx, from, nil)
	if err != nil {
		return zero, fmt.Errorf("evmwallet: balance: %w", err)
	}

	gasPrice, err := w.cfg.Client.SuggestGasPrice(ctx)
	if err != nil {
		return zero, fmt.Errorf("evmwallet: gas price: %w", err)
	}

	// Reserve enough for the transfer's gas across broadcastCallFrom's worst
	// case: the 25% buffer plus up to broadcastAttempts of 15% bumps. A 4x
	// multiple over nativeTransferGas * gasPrice covers that comfortably and
	// is still a negligible absolute amount at L2 gas prices.
	reserve := new(big.Int).Mul(
		big.NewInt(nativeTransferGas*4), gasPrice,
	)
	value := new(big.Int).Sub(balance, reserve)
	if value.Sign() <= 0 {
		return zero, fmt.Errorf("evmwallet: balance %s wei too low to "+
			"sweep after gas reserve %s wei", balance, reserve)
	}

	return w.broadcastCall(ctx, EvmCall{
		To:    common.HexToAddress(recipient),
		Value: value,
		Gas:   nativeTransferGas,
	})
}

// EvmAddress is a btcutil.Address wrapper for a 20-byte EVM address so the
// chain-agnostic LND plumbing can carry it.
type EvmAddress struct {
	addr common.Address
}

// String returns the 0x-prefixed hex address.
func (e *EvmAddress) String() string { return e.addr.Hex() }

// EncodeAddress returns the 0x-prefixed hex address.
func (e *EvmAddress) EncodeAddress() string { return e.addr.Hex() }

// ScriptAddress returns the raw 20 address bytes.
func (e *EvmAddress) ScriptAddress() []byte { return e.addr.Bytes() }

// IsForNet reports whether the address is valid for the network (always true;
// EVM addresses are network-agnostic).
func (e *EvmAddress) IsForNet(*chaincfg.Params) bool { return true }

// NewAddress returns the node's EVM address. EVM has a single account address
// per key, so change/account are ignored.
func (w *Wallet) NewAddress(_ lnwallet.AddressType, _ bool, _ string) (
	btcutil.Address, error) {

	addr, err := w.nodeAddress()
	if err != nil {
		return nil, err
	}

	return &EvmAddress{addr: addr}, nil
}

// LastUnusedAddress returns the node's EVM address.
func (w *Wallet) LastUnusedAddress(t lnwallet.AddressType, account string) (
	btcutil.Address, error) {

	return w.NewAddress(t, false, account)
}

// IsOurAddress reports whether the address is the node's EVM address.
func (w *Wallet) IsOurAddress(a btcutil.Address) bool {
	ours, err := w.nodeAddress()
	if err != nil {
		return false
	}

	return common.HexToAddress(a.String()) == ours
}

// SendOutputs broadcasts an EVM call carried in the wire.MsgTx-shaped output
// set. The first output's PkScript is expected to carry an EvmCall envelope; on
// success the returned wire.MsgTx echoes the broadcast call with its EVM tx
// hash recorded as the txid.
func (w *Wallet) SendOutputs(_ fn.Set[wire.OutPoint], outputs []*wire.TxOut,
	_ chainfee.SatPerKWeight, _ int32, _ string,
	_ base.CoinSelectionStrategy) (*wire.MsgTx, error) {

	if len(outputs) == 0 {
		return nil, fmt.Errorf("evmwallet: SendOutputs requires an " +
			"EvmCall output")
	}

	envelope := wire.NewMsgTx(2)
	envelope.AddTxIn(&wire.TxIn{SignatureScript: outputs[0].PkScript})

	if _, err := w.ExecuteOpenChannelCall(envelope); err != nil {
		return nil, err
	}

	return envelope, nil
}

// ExecuteOpenChannelCall builds, signs and broadcasts the EVM call carried in
// the wire.MsgTx envelope, returning the resulting EVM transaction hash. Used by
// the funding assembler for openChannel and by the resolvers for settlement
// calls. Two envelope forms are accepted: the raw "EVM_CALL:" {to,data,value}
// form, and the input.BuildEvmCallTx ChannelManager carrier, which is
// translated (decimals scaling, allowance, ABI encoding) by executeCarrier.
func (w *Wallet) ExecuteOpenChannelCall(tx *wire.MsgTx) (chainhash.Hash,
	error) {

	call, ok, err := unwrapEvmCall(tx)
	if err != nil {
		return chainhash.Hash{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	if !ok {
		return w.executeCarrier(ctx, tx)
	}

	return w.broadcastCall(ctx, call)
}

// PublishTransaction broadcasts an EVM call carried in the wire.MsgTx envelope.
func (w *Wallet) PublishTransaction(tx *wire.MsgTx, _ string) error {
	_, err := w.ExecuteOpenChannelCall(tx)

	return err
}

// IsSynced reports whether the node has a current chain tip and the timestamp
// of the best block. EVM nodes are considered synced once they answer a
// block-header query.
func (w *Wallet) IsSynced() (bool, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	hdr, err := w.cfg.Client.HeaderByNumber(ctx, nil)
	if err != nil {
		return false, 0, err
	}

	return true, int64(hdr.Time), nil
}

// RequiredReserve returns the on-chain reserve LND should keep. EVM channels
// have no anchor outputs, so no reserve is required.
func (w *Wallet) RequiredReserve(_ uint32) btcutil.Amount { return 0 }

// Start is a no-op; the JSON-RPC client is dialled by the chain builder.
func (w *Wallet) Start() error { return nil }

// Stop closes the EVM client connection.
func (w *Wallet) Stop() error {
	w.cfg.Client.Close()

	return nil
}

// BackEnd returns the backend identifier.
func (w *Wallet) BackEnd() string { return "evm" }

// GetRecoveryInfo reports no recovery scan is in progress (EVM has no UTXO
// rescan).
func (w *Wallet) GetRecoveryInfo() (bool, float64, error) {
	return false, 1, nil
}

// --- Bitcoin-specific methods with no EVM analogue: unsupported stubs. ---

// FetchOutpointInfo is unsupported on EVM.
func (w *Wallet) FetchOutpointInfo(*wire.OutPoint) (*lnwallet.Utxo, error) {
	return nil, lnwallet.ErrNotMine
}

// FetchDerivationInfo is unsupported on EVM.
func (w *Wallet) FetchDerivationInfo([]byte) (*psbt.Bip32Derivation, error) {
	return nil, ErrUnsupported
}

// ScriptForOutput is unsupported on EVM.
func (w *Wallet) ScriptForOutput(*wire.TxOut) (waddrmgr.ManagedPubKeyAddress,
	[]byte, []byte, error) {

	return nil, nil, nil, ErrUnsupported
}

// AddressInfo is unsupported on EVM.
func (w *Wallet) AddressInfo(btcutil.Address) (waddrmgr.ManagedAddress, error) {
	return nil, ErrUnsupported
}

// ListAccounts reports the single account an EVM chain has: the node address
// under the default name. Exposing it (rather than erroring) lets the
// WalletBalance RPC walk its usual account loop and land in ConfirmedBalance,
// which reads the ERC20 balance.
func (w *Wallet) ListAccounts(name string, _ *waddrmgr.KeyScope) (
	[]*waddrmgr.AccountProperties, error) {

	if name != "" && name != lnwallet.DefaultAccountName {
		return nil, nil
	}

	return []*waddrmgr.AccountProperties{{
		AccountName: lnwallet.DefaultAccountName,
		KeyScope:    waddrmgr.KeyScopeBIP0084,
	}}, nil
}

// ListAddresses reports the node's single EVM account address with its
// confirmed balance. EVM is an account model (one address, no HD tree), so the
// result always has exactly one entry under a single account.
//
// The account is reported under the "imported" account name on purpose: the
// walletkit marshaler derefs AccountPubKey.ChildIndex for any other account
// name (the EVM keyring exposes no account-level xpub, so that would panic),
// but skips the xpub block for the imported account — which is the right
// semantic anyway for a single externally-derived key with no derivation tree.
func (w *Wallet) ListAddresses(string, bool) (lnwallet.AccountAddressMap,
	error) {

	addr, err := w.nodeAddress()
	if err != nil {
		return nil, err
	}
	balance, err := w.ConfirmedBalance(0, "")
	if err != nil {
		return nil, err
	}

	account := &waddrmgr.AccountProperties{
		AccountName: waddrmgr.ImportedAddrAccountName,
		KeyScope:    waddrmgr.KeyScopeBIP0084,
	}

	return lnwallet.AccountAddressMap{
		account: {{
			Address: addr.Hex(),
			Balance: balance,
		}},
	}, nil
}

// ImportAccount is unsupported on EVM.
func (w *Wallet) ImportAccount(string, *hdkeychain.ExtendedKey, uint32,
	*waddrmgr.AddressType, bool) (*waddrmgr.AccountProperties,
	[]btcutil.Address, []btcutil.Address, error) {

	return nil, nil, nil, ErrUnsupported
}

// ImportPublicKey is unsupported on EVM.
func (w *Wallet) ImportPublicKey(*btcec.PublicKey, waddrmgr.AddressType) error {
	return ErrUnsupported
}

// ImportTaprootScript is unsupported on EVM.
func (w *Wallet) ImportTaprootScript(waddrmgr.KeyScope,
	*waddrmgr.Tapscript) (waddrmgr.ManagedAddress, error) {

	return nil, ErrUnsupported
}

// CreateSimpleTx is unsupported on EVM.
func (w *Wallet) CreateSimpleTx(fn.Set[wire.OutPoint], []*wire.TxOut,
	chainfee.SatPerKWeight, int32, base.CoinSelectionStrategy, bool) (
	*txauthor.AuthoredTx, error) {

	return nil, ErrUnsupported
}

// GetTransactionDetails is unsupported on EVM.
func (w *Wallet) GetTransactionDetails(*chainhash.Hash) (
	*lnwallet.TransactionDetail, error) {

	return nil, ErrUnsupported
}

// ListUnspentWitness reports the node's spendable ERC20 balance as a single
// synthetic UTXO. EVM is an account model — there are no discrete unspent
// outputs the way Bitcoin has (or the way Sui's coin objects map one-to-one
// to UTXOs) — so rather than an empty set we surface the whole confirmed
// balance as one entry, keeping `listunspent` informative and consistent with
// the Sui adapter's object→UTXO mapping. This is purely informational: EVM
// funding provisions via ERC20 transfer (chanfunding.EvmAssembler), never via
// UTXO coin selection, so this set is not consumed for input selection.
// minConfs/maxConfs are ignored — the on-chain balance is always confirmed.
func (w *Wallet) ListUnspentWitness(_, _ int32, _ string) ([]*lnwallet.Utxo,
	error) {

	balance, err := w.ConfirmedBalance(0, "")
	if err != nil {
		return nil, err
	}
	if balance == 0 {
		return nil, nil
	}

	addr, err := w.nodeAddress()
	if err != nil {
		return nil, err
	}

	// Synthetic outpoint: the node's 20-byte account address in the low
	// bytes of the hash, index 0 — stable and identifiable, not a real
	// txid. The PkScript is a well-formed P2WPKH (version 0, 20-byte
	// program = the account address) so lnrpc.MarshalUtxos /
	// txscript.ExtractPkScriptAddrs don't drop it.
	var hash chainhash.Hash
	copy(hash[:20], addr.Bytes())

	return []*lnwallet.Utxo{{
		AddressType:   lnwallet.WitnessPubKey,
		Value:         balance,
		Confirmations: 1,
		PkScript:      append([]byte{0x00, 0x14}, addr.Bytes()...),
		OutPoint:      wire.OutPoint{Hash: hash, Index: 0},
	}}, nil
}

// ListTransactionDetails reports the transactions this node has broadcast this
// process lifetime, enriched with their on-chain confirmation depth. EVM has no
// wallet-local transaction index, so a full account history (including txs sent
// by other tools or before this process started) needs an external indexer
// (eth_getLogs / explorer) — out of scope for the adapter. This best-effort
// session journal is still useful for `listchaintxns` and polling UIs; entries
// not yet mined are reported with zero confirmations. startHeight/endHeight
// filter by confirmation block; pagination args are ignored (the journal is
// bounded and small).
func (w *Wallet) ListTransactionDetails(startHeight, endHeight int32, _ string,
	_, _ uint32) ([]*lnwallet.TransactionDetail, uint64, uint64, error) {

	w.txMu.Lock()
	journal := make([]journalTx, len(w.txJournal))
	copy(journal, w.txJournal)
	w.txMu.Unlock()

	if len(journal) == 0 {
		return nil, 0, 0, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	tip, err := w.cfg.Client.BlockNumber(ctx)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("evmwallet: block number: %w", err)
	}

	details := make([]*lnwallet.TransactionDetail, 0, len(journal))
	for _, jtx := range journal {
		detail := &lnwallet.TransactionDetail{
			Hash:      jtx.hash,
			Timestamp: jtx.when.Unix(),
			Label:     "evm-node-tx",
		}

		// A receipt gives the mined block; absence means still pending.
		rcpt, err := w.cfg.Client.TransactionReceipt(
			ctx, common.Hash(jtx.hash),
		)
		if err == nil && rcpt != nil && rcpt.BlockNumber != nil {
			bn := rcpt.BlockNumber.Uint64()
			detail.BlockHeight = int32(bn)
			if tip >= bn {
				detail.NumConfirmations = int32(tip-bn) + 1
			}
		}

		// Height filter (0 bounds mean "unbounded"); pending txs
		// (BlockHeight 0) are always included.
		if startHeight > 0 && detail.BlockHeight != 0 &&
			detail.BlockHeight < startHeight {

			continue
		}
		if endHeight > 0 && detail.BlockHeight > endHeight {
			continue
		}

		details = append(details, detail)
	}

	return details, tip, 0, nil
}

// LeaseOutput is unsupported on EVM.
func (w *Wallet) LeaseOutput(wtxmgr.LockID, wire.OutPoint, time.Duration) (
	time.Time, error) {

	return time.Time{}, ErrUnsupported
}

// ReleaseOutput is unsupported on EVM.
func (w *Wallet) ReleaseOutput(wtxmgr.LockID, wire.OutPoint) error {
	return ErrUnsupported
}

// ListLeasedOutputs reports no leases; EVM has no UTXOs to lock for funding
// reservations.
func (w *Wallet) ListLeasedOutputs() ([]*base.ListLeasedOutputResult, error) {
	return nil, nil
}

// LabelTransaction is unsupported on EVM.
func (w *Wallet) LabelTransaction(chainhash.Hash, string, bool) error {
	return ErrUnsupported
}

// FetchTx is unsupported on EVM.
func (w *Wallet) FetchTx(chainhash.Hash) (*wire.MsgTx, error) {
	return nil, ErrUnsupported
}

// RemoveDescendants is a no-op on EVM (no mempool descendant tracking).
func (w *Wallet) RemoveDescendants(*wire.MsgTx) error { return nil }

// FundPsbt is unsupported on EVM.
func (w *Wallet) FundPsbt(*psbt.Packet, int32, chainfee.SatPerKWeight, string,
	*waddrmgr.KeyScope, base.CoinSelectionStrategy,
	func(wtxmgr.Credit) bool) (int32, error) {

	return 0, ErrUnsupported
}

// SignPsbt is unsupported on EVM.
func (w *Wallet) SignPsbt(*psbt.Packet) ([]uint32, error) {
	return nil, ErrUnsupported
}

// FinalizePsbt is unsupported on EVM.
func (w *Wallet) FinalizePsbt(*psbt.Packet, string) error {
	return ErrUnsupported
}

// DecorateInputs is unsupported on EVM.
func (w *Wallet) DecorateInputs(*psbt.Packet, bool) error {
	return ErrUnsupported
}

// SubscribeTransactions is unsupported on EVM.
func (w *Wallet) SubscribeTransactions() (lnwallet.TransactionSubscription,
	error) {

	return nil, ErrUnsupported
}

// CheckMempoolAcceptance is a no-op on EVM.
func (w *Wallet) CheckMempoolAcceptance(*wire.MsgTx) error { return nil }
