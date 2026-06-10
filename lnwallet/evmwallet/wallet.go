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
}

// Wallet adapts an EVM chain to lnwallet.WalletController.
type Wallet struct {
	cfg Config
	mu  sync.Mutex
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
		Index:  0,
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

// ListAddresses is unsupported on EVM.
func (w *Wallet) ListAddresses(string, bool) (lnwallet.AccountAddressMap,
	error) {

	return nil, ErrUnsupported
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

// ListUnspentWitness returns no UTXOs; EVM uses an account/balance model.
func (w *Wallet) ListUnspentWitness(int32, int32, string) ([]*lnwallet.Utxo,
	error) {

	return nil, nil
}

// ListTransactionDetails is unsupported on EVM.
func (w *Wallet) ListTransactionDetails(int32, int32, string, uint32,
	uint32) ([]*lnwallet.TransactionDetail, uint64, uint64, error) {

	return nil, 0, 0, ErrUnsupported
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
