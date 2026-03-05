package setuwallet

import (
	"errors"
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
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// ErrUnsupported is returned for Setu wallet operations that are not yet
// implemented. The Setu adapter currently acts as a stub until the
// blockchain-facing pieces are wired in.
var ErrUnsupported = errors.New("setuwallet: operation not implemented")

// Wallet is a stub WalletController implementation for the Setu backend.
// It satisfies the interface to allow incremental wiring without touching
// Bitcoin paths. All operations currently return ErrUnsupported unless
// otherwise noted.
type Wallet struct{}

// New creates a new stub Setu wallet instance.
func New() *Wallet {
	return &Wallet{}
}

// FetchOutpointInfo reports no ownership for now.
func (w *Wallet) FetchOutpointInfo(prevOut *wire.OutPoint) (*lnwallet.Utxo, error) {
	return nil, lnwallet.ErrNotMine
}

// FetchDerivationInfo is not implemented for Setu yet.
func (w *Wallet) FetchDerivationInfo(pkScript []byte) (*psbt.Bip32Derivation, error) {
	return nil, ErrUnsupported
}

// ScriptForOutput is not implemented for Setu yet.
func (w *Wallet) ScriptForOutput(output *wire.TxOut) (waddrmgr.ManagedPubKeyAddress, []byte, []byte, error) {
	return nil, nil, nil, ErrUnsupported
}

// ConfirmedBalance is not implemented for Setu yet.
func (w *Wallet) ConfirmedBalance(confs int32, accountFilter string) (btcutil.Amount, error) {
	return 0, ErrUnsupported
}

// NewAddress is not implemented for Setu yet.
func (w *Wallet) NewAddress(addrType lnwallet.AddressType, change bool, account string) (btcutil.Address, error) {
	return nil, ErrUnsupported
}

// LastUnusedAddress is not implemented for Setu yet.
func (w *Wallet) LastUnusedAddress(addrType lnwallet.AddressType, account string) (btcutil.Address, error) {
	return nil, ErrUnsupported
}

// IsOurAddress always returns false until address management is wired in.
func (w *Wallet) IsOurAddress(a btcutil.Address) bool {
	return false
}

// AddressInfo is not implemented for Setu yet.
func (w *Wallet) AddressInfo(a btcutil.Address) (waddrmgr.ManagedAddress, error) {
	return nil, ErrUnsupported
}

// ListAccounts is not implemented for Setu yet.
func (w *Wallet) ListAccounts(name string, scope *waddrmgr.KeyScope) ([]*waddrmgr.AccountProperties, error) {
	return nil, ErrUnsupported
}

// RequiredReserve returns zero until Setu fee bumping is defined.
func (w *Wallet) RequiredReserve(numAnchorChans uint32) btcutil.Amount {
	return 0
}

// ListAddresses is not implemented for Setu yet.
func (w *Wallet) ListAddresses(account string, showCustom bool) (lnwallet.AccountAddressMap, error) {
	return nil, ErrUnsupported
}

// ImportAccount is not implemented for Setu yet.
func (w *Wallet) ImportAccount(name string, accountPubKey *hdkeychain.ExtendedKey, masterKeyFingerprint uint32, addrType *waddrmgr.AddressType, dryRun bool) (*waddrmgr.AccountProperties, []btcutil.Address, []btcutil.Address, error) {
	return nil, nil, nil, ErrUnsupported
}

// ImportPublicKey is not implemented for Setu yet.
func (w *Wallet) ImportPublicKey(pubKey *btcec.PublicKey, addrType waddrmgr.AddressType) error {
	return ErrUnsupported
}

// ImportTaprootScript is not implemented for Setu yet.
func (w *Wallet) ImportTaprootScript(scope waddrmgr.KeyScope, tapscript *waddrmgr.Tapscript) (waddrmgr.ManagedAddress, error) {
	return nil, ErrUnsupported
}

// SendOutputs is not implemented for Setu yet.
func (w *Wallet) SendOutputs(inputs fn.Set[wire.OutPoint], outputs []*wire.TxOut, feeRate chainfee.SatPerKWeight, minConfs int32, label string, strategy base.CoinSelectionStrategy) (*wire.MsgTx, error) {
	return nil, ErrUnsupported
}

// CreateSimpleTx is not implemented for Setu yet.
func (w *Wallet) CreateSimpleTx(inputs fn.Set[wire.OutPoint], outputs []*wire.TxOut, feeRate chainfee.SatPerKWeight, minConfs int32, strategy base.CoinSelectionStrategy, dryRun bool) (*txauthor.AuthoredTx, error) {
	return nil, ErrUnsupported
}

// GetTransactionDetails is not implemented for Setu yet.
func (w *Wallet) GetTransactionDetails(txHash *chainhash.Hash) (*lnwallet.TransactionDetail, error) {
	return nil, ErrUnsupported
}

// ListUnspentWitness is not implemented for Setu yet.
func (w *Wallet) ListUnspentWitness(minConfs, maxConfs int32, accountFilter string) ([]*lnwallet.Utxo, error) {
	return nil, ErrUnsupported
}

// ListTransactionDetails is not implemented for Setu yet.
func (w *Wallet) ListTransactionDetails(startHeight, endHeight int32, accountFilter string, indexOffset uint32, maxTransactions uint32) ([]*lnwallet.TransactionDetail, uint64, uint64, error) {
	return nil, 0, 0, ErrUnsupported
}

// LeaseOutput is not implemented for Setu yet.
func (w *Wallet) LeaseOutput(id wtxmgr.LockID, op wire.OutPoint, duration time.Duration) (time.Time, error) {
	return time.Now(), ErrUnsupported
}

// ReleaseOutput is not implemented for Setu yet.
func (w *Wallet) ReleaseOutput(id wtxmgr.LockID, op wire.OutPoint) error {
	return ErrUnsupported
}

// ListLeasedOutputs is not implemented for Setu yet.
func (w *Wallet) ListLeasedOutputs() ([]*base.ListLeasedOutputResult, error) {
	return nil, ErrUnsupported
}

// PublishTransaction is not implemented for Setu yet.
func (w *Wallet) PublishTransaction(tx *wire.MsgTx, label string) error {
	return ErrUnsupported
}

// LabelTransaction is not implemented for Setu yet.
func (w *Wallet) LabelTransaction(hash chainhash.Hash, label string, overwrite bool) error {
	return ErrUnsupported
}

// FetchTx is not implemented for Setu yet.
func (w *Wallet) FetchTx(hash chainhash.Hash) (*wire.MsgTx, error) {
	return nil, ErrUnsupported
}

// RemoveDescendants is not implemented for Setu yet.
func (w *Wallet) RemoveDescendants(tx *wire.MsgTx) error {
	return ErrUnsupported
}

// FundPsbt is not implemented for Setu yet.
func (w *Wallet) FundPsbt(packet *psbt.Packet, minConfs int32, feeRate chainfee.SatPerKWeight, account string, changeScope *waddrmgr.KeyScope, strategy base.CoinSelectionStrategy, allowUtxo func(wtxmgr.Credit) bool) (int32, error) {
	return 0, ErrUnsupported
}

// SignPsbt is not implemented for Setu yet.
func (w *Wallet) SignPsbt(packet *psbt.Packet) ([]uint32, error) {
	return nil, ErrUnsupported
}

// FinalizePsbt is not implemented for Setu yet.
func (w *Wallet) FinalizePsbt(packet *psbt.Packet, account string) error {
	return ErrUnsupported
}

// DecorateInputs is not implemented for Setu yet.
func (w *Wallet) DecorateInputs(packet *psbt.Packet, failOnUnknown bool) error {
	return ErrUnsupported
}

// SubscribeTransactions is not implemented for Setu yet.
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
	return "setu"
}

// CheckMempoolAcceptance is not implemented for Setu yet.
func (w *Wallet) CheckMempoolAcceptance(tx *wire.MsgTx) error {
	return ErrUnsupported
}
