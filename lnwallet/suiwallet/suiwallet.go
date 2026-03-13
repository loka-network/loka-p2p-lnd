package suiwallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
	base "github.com/btcsuite/btcwallet/wallet"
	"github.com/btcsuite/btcwallet/wallet/txauthor"
	"github.com/btcsuite/btcwallet/wtxmgr"
	"github.com/lightningnetwork/lnd/fn/v2"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// ErrUnsupported is returned for Sui wallet operations that are not yet
// implemented.
var ErrUnsupported = errors.New("suiwallet: operation not implemented")

// SuiClient is an interface that provides the wallet with connectivity to
// the Sui network. It is implemented by the SuiRPCClient.
type SuiClient interface {
	GetBestEpoch() (uint32, chainhash.Hash, error)

	// GetCoins returns the list of SUI coins owned by the given address.
	GetCoins(address string) ([]SuiCoin, error)

	// ExecuteMoveCall executes a Sui Move call transaction.
	ExecuteMoveCall(payload []byte, signature []byte) (chainhash.Hash, error)
}

// SuiCoin represents a Sui Coin object.
type SuiCoin struct {
	ObjectID chainhash.Hash
	Balance  uint64
}

// Config holds configuration parameters for the Sui adapter wallet.
type Config struct {
	// SuiAddress is the Sui address owned by this wallet.
	SuiAddress string

	// Client provides connectivity to the Sui network.
	Client SuiClient
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

// NewAddress is not implemented for Sui yet.
func (w *Wallet) NewAddress(addrType lnwallet.AddressType, change bool, account string) (btcutil.Address, error) {
	return nil, ErrUnsupported
}

// LastUnusedAddress is not implemented for Sui yet.
func (w *Wallet) LastUnusedAddress(addrType lnwallet.AddressType, account string) (btcutil.Address, error) {
	return nil, ErrUnsupported
}

// IsOurAddress always returns false until address management is wired in.
func (w *Wallet) IsOurAddress(a btcutil.Address) bool {
	return false
}

// AddressInfo is not implemented for Sui yet.
func (w *Wallet) AddressInfo(a btcutil.Address) (waddrmgr.ManagedAddress, error) {
	return nil, ErrUnsupported
}

// ListAccounts is not implemented for Sui yet.
func (w *Wallet) ListAccounts(name string, scope *waddrmgr.KeyScope) ([]*waddrmgr.AccountProperties, error) {
	return nil, ErrUnsupported
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
	coins, err := w.cfg.Client.GetCoins(w.cfg.SuiAddress)
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

// ListLeasedOutputs is not implemented for Sui yet.
func (w *Wallet) ListLeasedOutputs() ([]*base.ListLeasedOutputResult, error) {
	return nil, ErrUnsupported
}

// PublishTransaction decodes the wire.MsgTx envelope and executes the
// corresponding Sui Move call.
func (w *Wallet) PublishTransaction(tx *wire.MsgTx, label string) error {
	// Decode the Sui call from the MsgTx envelope.
	_, _, _, err := input.DecodeSuiCallTx(tx)
	if err != nil {
		return fmt.Errorf("suiwallet: failed to decode tx: %w", err)
	}

	// In our adapter, the signature is expected to be appended to the
	// end of the SignatureScript or handled via a separate mechanism.
	// For now, assume tx.TxIn[0].SignatureScript contains the serialized
	// call and the Signer has already been called.
	//
	// This is a simplified flow:
	// 1. input.BuildSuiCallTx creates MsgTx with JSON payload.
	// 2. suiSigner.SignOutputRaw is called, it signs the JSON payload.
	// 3. The caller (e.g. fundingManager) must put the signature somewhere.
	//
	// Let's assume for this adapter that the SignatureScript IS the payload,
	// and we need the signature from the witness or a known location.
	// To keep it compatible with LND's expectation that PublishTransaction
	// takes a "signed" tx, we'll assume the signature is in the witness.
	if len(tx.TxIn[0].Witness) == 0 {
		return fmt.Errorf("suiwallet: tx has no signature in witness")
	}
	signature := tx.TxIn[0].Witness[0]

	_, err = w.cfg.Client.ExecuteMoveCall(tx.TxIn[0].SignatureScript, signature)
	if err != nil {
		return fmt.Errorf("suiwallet: execution failed: %w", err)
	}

	return nil
}

// LabelTransaction is not implemented for Sui yet.
func (w *Wallet) LabelTransaction(hash chainhash.Hash, label string, overwrite bool) error {
	return ErrUnsupported
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
