package suiwallet

import (
	"errors"
	"fmt"
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
	"crypto/rand"
	go_ecdsa "crypto/ecdsa"
	"math/big"
	"github.com/lightningnetwork/lnd/chainntnfs/suinotify"
	"github.com/lightningnetwork/lnd/fn/v2"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"golang.org/x/crypto/blake2b"
	"crypto/sha256"
)

// ErrUnsupported is returned for Sui wallet operations that are not yet
// implemented.
var ErrUnsupported = errors.New("suiwallet: operation not implemented")

// Config holds configuration parameters for the Sui adapter wallet.
type Config struct {
	// KeyRing allows derivation of Sui addresses.
	KeyRing keychain.SecretKeyRing

	// Client provides connectivity to the Sui network.
	Client suinotify.SuiClient
}

// Wallet is an adapter that implements the lnwallet.WalletController interface
// for the Sui MoveVM network.
type Wallet struct {
	cfg Config
}

// New creates a new Sui wallet instance backed by the given configuration.
func New(cfg Config) *Wallet {
	return &Wallet{
		cfg: cfg,
	}
}

// FetchOutpointInfo reports no ownership for now.
func (w *Wallet) FetchOutpointInfo(prevOut *wire.OutPoint) (*lnwallet.Utxo, error) {
	return nil, lnwallet.ErrNotMine
}

// FetchDerivationInfo is not implemented for Sui yet.
func (w *Wallet) FetchDerivationInfo(pkScript []byte) (*psbt.Bip32Derivation, error) {
	return nil, ErrUnsupported
}

// ScriptForOutput is not implemented for Sui yet.
func (w *Wallet) ScriptForOutput(output *wire.TxOut) (waddrmgr.ManagedPubKeyAddress, []byte, []byte, error) {
	return nil, nil, nil, ErrUnsupported
}

// ConfirmedBalance returns the sum of all confirmed unspent outputs.
func (w *Wallet) ConfirmedBalance(confs int32, accountFilter string) (btcutil.Amount, error) {
	utxos, err := w.ListUnspentWitness(confs, 1000000, accountFilter)
	if err != nil {
		return 0, err
	}

	var balance btcutil.Amount
	for _, utxo := range utxos {
		balance += utxo.Value
	}

	return balance, nil
}

// SuiAddress implements the btcutil.Address interface for Sui addresses.
type SuiAddress struct {
	addr string
}

func (s *SuiAddress) String() string { return s.addr }
func (s *SuiAddress) EncodeAddress() string { return s.addr }
func (s *SuiAddress) ScriptAddress() []byte { return []byte(s.addr) }
func (s *SuiAddress) IsForNet(p *chaincfg.Params) bool { return true }

// NewAddress returns the Sui address owned by this wallet adapter.
func (w *Wallet) NewAddress(addrType lnwallet.AddressType, change bool, account string) (btcutil.Address, error) {
	// We return a virtual address since Sui addresses are not BTC-compatible.
	// We derive it dynamically so it's only generated once the wallet is unlocked.
	nodeKeyDesc, err := w.cfg.KeyRing.DeriveKey(keychain.KeyLocator{
		Family: keychain.KeyFamilyNodeKey,
		Index:  0,
	})
	if err != nil {
		return nil, err
	}
	// Sui Address derivation for Secp256k1 (flag 0x01):
	// blake2b.Sum256([]byte{0x01} + compressed_pubkey)
	pubKeyData := nodeKeyDesc.PubKey.SerializeCompressed()
	addrData := append([]byte{0x01}, pubKeyData...)
	hash := blake2b.Sum256(addrData)
	
	suiAddress := fmt.Sprintf("0x%x", hash[:])
	return &SuiAddress{addr: suiAddress}, nil
}

// LastUnusedAddress returns the Sui address.
func (w *Wallet) LastUnusedAddress(addrType lnwallet.AddressType, account string) (btcutil.Address, error) {
	return w.NewAddress(addrType, false, account)
}

// IsOurAddress reports whether the address is ours.
func (w *Wallet) IsOurAddress(a btcutil.Address) bool {
	addr, err := w.NewAddress(lnwallet.UnknownAddressType, false, "")
	if err != nil {
		return false
	}
	return a.String() == addr.String()
}

// AddressInfo is not implemented for Sui yet.
func (w *Wallet) AddressInfo(a btcutil.Address) (waddrmgr.ManagedAddress, error) {
	return nil, ErrUnsupported
}

// ListAccounts returns a single dummy default account for Sui wallet.
func (w *Wallet) ListAccounts(name string, scope *waddrmgr.KeyScope) ([]*waddrmgr.AccountProperties, error) {
	return []*waddrmgr.AccountProperties{
		{
			AccountName: lnwallet.DefaultAccountName,
		},
	}, nil
}

// RequiredReserve returns zero.
func (w *Wallet) RequiredReserve(numAnchorChans uint32) btcutil.Amount {
	return 0
}

// ListAddresses is not implemented for Sui yet.
func (w *Wallet) ListAddresses(account string, showCustom bool) (lnwallet.AccountAddressMap, error) {
	return nil, ErrUnsupported
}

// ImportAccount is not implemented for Sui yet.
func (w *Wallet) ImportAccount(name string, accountPubKey *hdkeychain.ExtendedKey, masterKeyFingerprint uint32, addrType *waddrmgr.AddressType, dryRun bool) (*waddrmgr.AccountProperties, []btcutil.Address, []btcutil.Address, error) {
	return nil, nil, nil, ErrUnsupported
}

// ImportPublicKey is not implemented for Sui yet.
func (w *Wallet) ImportPublicKey(pubKey *btcec.PublicKey, addrType waddrmgr.AddressType) error {
	return ErrUnsupported
}

// ImportTaprootScript is not implemented for Sui yet.
func (w *Wallet) ImportTaprootScript(scope waddrmgr.KeyScope, tapscript *waddrmgr.Tapscript) (waddrmgr.ManagedAddress, error) {
	return nil, ErrUnsupported
}

// SendOutputs is not implemented for Sui yet.
func (w *Wallet) SendOutputs(inputs fn.Set[wire.OutPoint], outputs []*wire.TxOut, feeRate chainfee.SatPerKWeight, minConfs int32, label string, strategy base.CoinSelectionStrategy) (*wire.MsgTx, error) {
	return nil, ErrUnsupported
}

// CreateSimpleTx is not implemented for Sui yet.
func (w *Wallet) CreateSimpleTx(inputs fn.Set[wire.OutPoint], outputs []*wire.TxOut, feeRate chainfee.SatPerKWeight, minConfs int32, strategy base.CoinSelectionStrategy, dryRun bool) (*txauthor.AuthoredTx, error) {
	return nil, ErrUnsupported
}

// GetTransactionDetails is not implemented for Sui yet.
func (w *Wallet) GetTransactionDetails(txHash *chainhash.Hash) (*lnwallet.TransactionDetail, error) {
	return nil, ErrUnsupported
}

// ListUnspentWitness returns all unspent outputs (SUI coins) that are
// available for spending.
func (w *Wallet) ListUnspentWitness(minConfs, maxConfs int32, accountFilter string) ([]*lnwallet.Utxo, error) {
	addr, err := w.NewAddress(lnwallet.UnknownAddressType, false, "")
	if err != nil {
		return nil, err
	}
	coins, err := w.cfg.Client.GetCoins(addr.String())
	if err != nil {
		return nil, err
	}

	var utxos []*lnwallet.Utxo
	for _, c := range coins {
		utxos = append(utxos, &lnwallet.Utxo{
			AddressType: lnwallet.UnknownAddressType,
			Value:       btcutil.Amount(c.Balance),
			OutPoint: wire.OutPoint{
				Hash:  c.ObjectID,
				Index: 0,
			},
			// Placeholder script.
			PkScript: []byte{0x51},
		})
	}

	return utxos, nil
}

// ListTransactionDetails is not implemented for Sui yet.
func (w *Wallet) ListTransactionDetails(startHeight, endHeight int32, accountFilter string, indexOffset uint32, maxTransactions uint32) ([]*lnwallet.TransactionDetail, uint64, uint64, error) {
	return nil, 0, 0, ErrUnsupported
}

// LeaseOutput is not implemented for Sui yet.
func (w *Wallet) LeaseOutput(id wtxmgr.LockID, op wire.OutPoint, duration time.Duration) (time.Time, error) {
	return time.Now(), ErrUnsupported
}

// ReleaseOutput is not implemented for Sui yet.
func (w *Wallet) ReleaseOutput(id wtxmgr.LockID, op wire.OutPoint) error {
	return ErrUnsupported
}

// ListLeasedOutputs returns an empty array for Sui wallet.
func (w *Wallet) ListLeasedOutputs() ([]*base.ListLeasedOutputResult, error) {
	return nil, nil
}

	// LabelTransaction is not implemented for Sui yet.
func (w *Wallet) LabelTransaction(hash chainhash.Hash, label string, overwrite bool) error {
	return ErrUnsupported
}

// ExecuteOpenChannelCall executes a channel open Move Call payload and returns the resulting Channel ObjectID.
func (w *Wallet) ExecuteOpenChannelCall(tx *wire.MsgTx) (chainhash.Hash, error) {
	// Decode the Sui call from the MsgTx envelope.
	_, _, _, err := input.DecodeSuiCallTx(tx)
	if err != nil {
		return chainhash.Hash{}, fmt.Errorf("suiwallet: failed to decode tx: %w", err)
	}

	addr, err := w.NewAddress(lnwallet.UnknownAddressType, false, "")
	if err != nil {
		return chainhash.Hash{}, fmt.Errorf("failed to get sender address: %w", err)
	}

	channelID := &tx.TxIn[0].PreviousOutPoint.Hash
	txBytes, err := w.cfg.Client.BuildMoveCall(addr.String(), channelID, tx.TxIn[0].SignatureScript)
	if err != nil {
		return chainhash.Hash{}, fmt.Errorf("suiwallet: BuildMoveCall failed: %w", err)
	}

	suiSig, err := w.signSuiTransaction(txBytes)
	if err != nil {
		return chainhash.Hash{}, fmt.Errorf("suiwallet: failed to sign Sui transaction: %w", err)
	}

	// Execute via RPC client and extract the created Channel ObjectID.
	_, createdObjects, err := w.cfg.Client.ExecuteTransactionBlockFull(txBytes, suiSig)
	if err != nil {
		return chainhash.Hash{}, fmt.Errorf("suiwallet: execution failed: %w", err)
	}

	// The first created object should be our Channel.
	if len(createdObjects) > 0 {
		fmt.Printf("[SUI] Channel ObjectID created: %x\n", createdObjects[0][:])
		return createdObjects[0], nil
	}

	return chainhash.Hash{}, fmt.Errorf("suiwallet: no Channel object created in transaction")
}

// PublishTransaction decodes the wire.MsgTx envelope and executes the
// corresponding Sui Move call.
func (w *Wallet) PublishTransaction(tx *wire.MsgTx, label string) error {
	// Try to decode a Sui call envelope from the MsgTx.
	embeddedObjID, callType, _, err := input.DecodeSuiCallTx(tx)
	if err != nil {
		// DecodeSuiCallTx failed — this is a Bitcoin-style tx
		// (e.g. cooperative close from chancloser). Handle it
		// by building the corresponding Sui Move call.
		return w.publishBitcoinStyleTx(tx, label)
	}

	// Check if this is a channel open that has an embedded ObjectId
	// (meaning it was already executed by SuiAssembler).
	// If it is, do nothing to prevent double-execution.
	if callType == input.SuiCallChannelOpen {
		var zeroHash chainhash.Hash
		if embeddedObjID != zeroHash {
			// Already executed and ObjectID assigned.
			return nil
		}
	}

	// Intercept premature timelock sweeps to prevent burning SUI Gas on 0x5 failures.
	// LND's physical mock ticks aggressively when `blocks_til_maturity` is large, 
	// driving the Sweeper to submit transactions constantly.
	if callType == input.SuiCallChannelClaimLocal {
		channelID := &tx.TxIn[0].PreviousOutPoint.Hash
		closeTs, delay, err := w.cfg.Client.GetChannelStatus(channelID)
		if err == nil && closeTs > 0 {
			target := closeTs + delay
			now := uint64(time.Now().UnixMilli())
			if now < target {
				fmt.Printf("[suiwallet] Intercepted premature claim_force_close for channel %x. "+
					"Physical SUI Time (%d) < Target (%d). Silently dropping to save Gas.\n", 
					channelID[:4], now, target)
				// Returning nil pretends to LND that it hit the Mempool, 
				// avoiding LND panic loops while awaiting the physical maturity phase.
				return nil
			}
		} else if err != nil {
			fmt.Printf("[suiwallet] Warning: GetChannelStatus failed: %v\n", err)
		}
	}

	return w.executeSuiEnvelopeTx(tx)
}

// executeSuiEnvelopeTx builds and executes a Sui Move call from a properly
// encoded Sui envelope transaction.
func (w *Wallet) executeSuiEnvelopeTx(tx *wire.MsgTx) error {
	addr, err := w.NewAddress(lnwallet.UnknownAddressType, false, "")
	if err != nil {
		return fmt.Errorf("failed to get sender address: %w", err)
	}

	channelID := &tx.TxIn[0].PreviousOutPoint.Hash
	txBytes, err := w.cfg.Client.BuildMoveCall(addr.String(), channelID, tx.TxIn[0].SignatureScript)
	if err != nil {
		return fmt.Errorf("suiwallet: BuildMoveCall failed: %w", err)
	}

	suiSig, err := w.signSuiTransaction(txBytes)
	if err != nil {
		return fmt.Errorf("suiwallet: failed to sign Sui transaction: %w", err)
	}

	_, err = w.cfg.Client.ExecuteTransactionBlock(txBytes, suiSig)
	if err != nil {
		return fmt.Errorf("suiwallet: execution failed: %w", err)
	}

	return nil
}

// publishBitcoinStyleTx handles a standard Bitcoin wire.MsgTx that was not
// encoded as a Sui envelope. This occurs for cooperative closes where the
// chancloser builds a Bitcoin-style closing tx. We extract the channel
// ObjectID from the funding outpoint and the final balances from the outputs,
// then construct and execute the corresponding close_channel Sui Move call.
func (w *Wallet) publishBitcoinStyleTx(tx *wire.MsgTx, label string) error {
	if len(tx.TxIn) == 0 {
		fmt.Println("[suiwallet] ignoring non-Sui tx with no inputs")
		return nil
	}

	channelID := tx.TxIn[0].PreviousOutPoint.Hash
	var zeroHash chainhash.Hash
	if channelID == zeroHash {
		fmt.Println("[suiwallet] ignoring non-Sui tx with zero channelID")
		return nil
	}

	// Extract balances from the Bitcoin close tx outputs.
	// In LND's cooperative close, the outputs contain the final
	// distribution. Output ordering follows BIP-69, so we use
	// output[0] as balance_a (channel opener) and output[1] as
	// balance_b. For this prototype the Move contract does not
	// verify signatures, so the ordering is best-effort.
	var balanceA, balanceB uint64
	if len(tx.TxOut) >= 1 {
		balanceA = uint64(tx.TxOut[0].Value)
	}
	if len(tx.TxOut) >= 2 {
		balanceB = uint64(tx.TxOut[1].Value)
	}

	fmt.Printf("[suiwallet] detected Bitcoin-style close tx for channel %x, "+
		"building Sui close_channel call (balanceA=%d, balanceB=%d)\n",
		channelID[:8], balanceA, balanceB)

	// Build the close_channel Sui Move call envelope.
	payload := input.ChannelClosePayload{
		StateNum:      0,
		LocalBalance:  balanceA,
		RemoteBalance: balanceB,
		LocalSig:      []byte{0},
		RemoteSig:     []byte{0},
	}

	suiTx, err := input.BuildChannelCloseTx(channelID, payload)
	if err != nil {
		fmt.Printf("[suiwallet] failed to build coop close envelope: %v\n", err)
		return nil
	}

	// Execute via the normal Sui envelope path.
	// Errors are logged but not returned — in a cooperative close, both
	// sides attempt to broadcast. Only one needs to succeed; the other
	// may lack gas or encounter transient failures.
	if err := w.executeSuiEnvelopeTx(suiTx); err != nil {
		fmt.Printf("[suiwallet] coop close execution failed (non-fatal): %v\n", err)
	}
	return nil
}

// signSuiTransaction generates a native Sui signature (secp256k1) over the PTB.
func (w *Wallet) signSuiTransaction(txBytes []byte) ([]byte, error) {
	nodeKeyDesc, err := w.cfg.KeyRing.DeriveKey(keychain.KeyLocator{
		Family: keychain.KeyFamilyNodeKey,
		Index:  0,
	})
	if err != nil {
		return nil, err
	}
	privKey, err := w.cfg.KeyRing.DerivePrivKey(nodeKeyDesc)
	if err != nil {
		return nil, err
	}

	intent := append([]byte{0, 0, 0}, txBytes...)
	b2bHash := blake2b.Sum256(intent)
	hash := sha256.Sum256(b2bHash[:])

	stdPrivKey := privKey.ToECDSA()
	
	halfOrder, _ := new(big.Int).SetString("7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0", 16)
	var rVal, sVal *big.Int
	var errSign error
	for {
		rVal, sVal, errSign = go_ecdsa.Sign(rand.Reader, stdPrivKey, hash[:])
		if errSign != nil {
			return nil, errSign
		}
		if sVal.Cmp(halfOrder) <= 0 {
			break
		}
	}

	r := make([]byte, 32)
	s := make([]byte, 32)
	rVal.FillBytes(r)
	sVal.FillBytes(s)

	var suiSig []byte
	suiSig = append(suiSig, 0x01) // Flag for secp256k1
	suiSig = append(suiSig, r...)
	suiSig = append(suiSig, s...)
	suiSig = append(suiSig, nodeKeyDesc.PubKey.SerializeCompressed()...)

	return suiSig, nil
}

// FetchTx is not implemented for Sui yet.
func (w *Wallet) FetchTx(hash chainhash.Hash) (*wire.MsgTx, error) {
	return nil, ErrUnsupported
}

// RemoveDescendants is not implemented for Sui yet.
func (w *Wallet) RemoveDescendants(tx *wire.MsgTx) error {
	return ErrUnsupported
}

// FundPsbt is not implemented for Sui yet.
func (w *Wallet) FundPsbt(packet *psbt.Packet, minConfs int32, feeRate chainfee.SatPerKWeight, account string, changeScope *waddrmgr.KeyScope, strategy base.CoinSelectionStrategy, allowUtxo func(wtxmgr.Credit) bool) (int32, error) {
	return 0, ErrUnsupported
}

// SignPsbt is not implemented for Sui yet.
func (w *Wallet) SignPsbt(packet *psbt.Packet) ([]uint32, error) {
	return nil, ErrUnsupported
}

// FinalizePsbt is not implemented for Sui yet.
func (w *Wallet) FinalizePsbt(packet *psbt.Packet, account string) error {
	return ErrUnsupported
}

// DecorateInputs is not implemented for Sui yet.
func (w *Wallet) DecorateInputs(packet *psbt.Packet, failOnUnknown bool) error {
	return ErrUnsupported
}

// SubscribeTransactions is not implemented for Sui yet.
func (w *Wallet) SubscribeTransactions() (lnwallet.TransactionSubscription, error) {
	return nil, ErrUnsupported
}

// IsSynced reports synced immediately for the stub implementation.
func (w *Wallet) IsSynced() (bool, int64, error) {
	return true, time.Now().Unix(), nil
}

// GetRecoveryInfo reports complete recovery for the stub implementation.
func (w *Wallet) GetRecoveryInfo() (bool, float64, error) {
	return false, 1.0, nil
}

// Start currently performs no work.
func (w *Wallet) Start() error {
	return nil
}

// Stop currently performs no work.
func (w *Wallet) Stop() error {
	return nil
}

// BackEnd reports the backing service name.
func (w *Wallet) BackEnd() string {
	return "sui"
}

// CheckMempoolAcceptance is not implemented for Sui yet.
func (w *Wallet) CheckMempoolAcceptance(tx *wire.MsgTx) error {
	return ErrUnsupported
}
