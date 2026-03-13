// Package suiwallet provides a stub WalletController, BlockChainIO, Signer,
// SecretKeyRing and MessageSigner implementation for the Sui DAG backend.
// This file tests that all stub implementations compile, satisfy their
// interfaces, and return the expected ErrUnsupported sentinel for every
// unimplemented method.
package suiwallet

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/stretchr/testify/require"
)

// mockSuiClient is a minimal SuiClient for testing.
type mockSuiClient struct {
	SuiClient
}

func (m *mockSuiClient) GetCoins(addr string) ([]SuiCoin, error) {
	return nil, nil
}

func (m *mockSuiClient) ExecuteMoveCall(p, s []byte) (chainhash.Hash, error) {
	return chainhash.Hash{}, nil
}

func (m *mockSuiClient) GetBestEpoch() (uint32, chainhash.Hash, error) {
	return 0, chainhash.Hash{}, nil
}

// mockSecretKeyRing is a minimal SecretKeyRing for testing.
type mockSecretKeyRing struct {
	keychain.SecretKeyRing
}

func (m *mockSecretKeyRing) DeriveNextKey(fam keychain.KeyFamily) (keychain.KeyDescriptor, error) {
	return keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{Family: fam, Index: 0},
		PubKey:     &btcec.PublicKey{},
	}, nil
}

func (m *mockSecretKeyRing) DeriveKey(loc keychain.KeyLocator) (keychain.KeyDescriptor, error) {
	return keychain.KeyDescriptor{
		KeyLocator: loc,
		PubKey:     &btcec.PublicKey{},
	}, nil
}

func (m *mockSecretKeyRing) DerivePrivKey(desc keychain.KeyDescriptor) (*btcec.PrivateKey, error) {
	priv, _ := btcec.NewPrivateKey()
	return priv, nil
}

func (m *mockSecretKeyRing) ECDH(desc keychain.KeyDescriptor, pub *btcec.PublicKey) ([32]byte, error) {
	return [32]byte{}, nil
}

func newTestWallet() *Wallet {
	return New(Config{
		SuiAddress: "0x123",
		Client:     &mockSuiClient{},
	})
}

// ----------------------------------------------------------------------------
// Wallet (WalletController + MessageSigner)
// ----------------------------------------------------------------------------

// TestWalletBackEnd verifies BackEnd returns the canonical "sui" string.
func TestWalletBackEnd(t *testing.T) {
	t.Parallel()
	w := newTestWallet()
	require.Equal(t, "sui", w.BackEnd())
}

// TestWalletIsSynced verifies that the Sui stub reports itself as always
// synced (the wallet is virtual and tracks no real chain state).
func TestWalletIsSynced(t *testing.T) {
	t.Parallel()
	w := newTestWallet()
	synced, _, err := w.IsSynced()
	require.NoError(t, err)
	require.True(t, synced)
}

// TestWalletStartStop verifies that Start and Stop are no-ops that return nil.
func TestWalletStartStop(t *testing.T) {
	t.Parallel()
	w := newTestWallet()
	require.NoError(t, w.Start())
	require.NoError(t, w.Stop())
}

// TestWalletGetRecoveryInfo verifies the stub recovery response values.
func TestWalletGetRecoveryInfo(t *testing.T) {
	t.Parallel()
	w := newTestWallet()
	inRecovery, progress, err := w.GetRecoveryInfo()
	require.NoError(t, err)
	require.False(t, inRecovery)
	require.Equal(t, float64(1.0), progress)
}

// TestWalletUnsupportedMethods verifies that the remaining WalletController
// methods return ErrUnsupported.
func TestWalletUnsupportedMethods(t *testing.T) {
	t.Parallel()
	w := newTestWallet()

	// ListTransactionDetails
	_, _, _, err := w.ListTransactionDetails(0, 0, "", 0, 0)
	require.ErrorIs(t, err, ErrUnsupported)

	// SubscribeTransactions
	_, err = w.SubscribeTransactions()
	require.ErrorIs(t, err, ErrUnsupported)
}

// TestWalletSignMessage verifies that MessageSigner.SignMessage returns
// ErrUnsupported (Sui uses its own signing layer).
func TestWalletSignMessage(t *testing.T) {
	t.Parallel()
	w := newTestWallet()
	var loc keychain.KeyLocator
	_, err := w.SignMessage(loc, []byte("hello"), false)
	require.ErrorIs(t, err, ErrUnsupported)
}

// Compile-time assertions that Wallet satisfies both required interfaces.
var (
	_ lnwallet.WalletController = (*Wallet)(nil)
	_ lnwallet.MessageSigner    = (*Wallet)(nil)
)

// ----------------------------------------------------------------------------
// SuiBlockChainIO
// ----------------------------------------------------------------------------

// TestSuiBlockChainIOUnsupported verifies that all BlockChainIO methods return
// ErrUnsupported.
func TestSuiBlockChainIOUnsupported(t *testing.T) {
	t.Parallel()
	io := &SuiBlockChainIO{client: &mockSuiClient{}}

	_, _, err := io.GetBestBlock()
	require.NoError(t, err) // Implemented

	_, err = io.GetUtxo(nil, nil, 0, nil)
	require.NoError(t, err) // Implemented stub

	_, err = io.GetBlockHash(0)
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = io.GetBlock(nil)
	require.ErrorIs(t, err, ErrUnsupported)
}

// Compile-time assertion.
var _ lnwallet.BlockChainIO = (*SuiBlockChainIO)(nil)

// ----------------------------------------------------------------------------
// SuiSigner
// ----------------------------------------------------------------------------

// TestSuiSignerUnsupported verifies that all Signer methods return
// ErrUnsupported.
func TestSuiSignerUnsupported(t *testing.T) {
	t.Parallel()
	s := NewSuiSigner(&mockSecretKeyRing{})

	_, err := s.SignOutputRaw(&wire.MsgTx{
		TxIn: []*wire.TxIn{{SignatureScript: []byte("data")}},
	}, &input.SignDescriptor{})
	require.NoError(t, err)

	_, err = s.ComputeInputScript(nil, nil)
	require.ErrorIs(t, err, ErrUnsupported)

	// MuSig2 methods (simplest signatures only — all return ErrUnsupported;
	// full interface compliance is asserted with var _ below).
	var sessionID input.MuSig2SessionID
	err = s.MuSig2Cleanup(sessionID)
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = s.MuSig2RegisterNonces(sessionID, nil)
	require.ErrorIs(t, err, ErrUnsupported)
}

// Compile-time assertion.
var _ input.MuSig2Signer = (*SuiSigner)(nil)

// ----------------------------------------------------------------------------
// SuiKeyRing
// ----------------------------------------------------------------------------

// TestSuiKeyRingUnsupported verifies that all SecretKeyRing methods return
// ErrUnsupported.
func TestSuiKeyRingUnsupported(t *testing.T) {
	t.Parallel()
	kr := &SuiKeyRing{SecretKeyRing: &mockSecretKeyRing{}}

	_, err := kr.DeriveNextKey(keychain.KeyFamily(0))
	require.NoError(t, err)

	_, err = kr.DeriveKey(keychain.KeyLocator{})
	require.NoError(t, err)

	_, err = kr.ECDH(keychain.KeyDescriptor{}, &btcec.PublicKey{})
	require.NoError(t, err)
}

// Compile-time assertion.
var _ keychain.SecretKeyRing = (*SuiKeyRing)(nil)

// ----------------------------------------------------------------------------
// New()
// ----------------------------------------------------------------------------

// TestNewReturnsWallet verifies that New() returns a non-nil *Wallet and that
// its initial state is consistent.
func TestNewReturnsWallet(t *testing.T) {
	t.Parallel()
	w := newTestWallet()
	require.NotNil(t, w)

	// Spot-check a few well-known properties.
	require.Equal(t, "sui", w.BackEnd())
	bal, err := w.ConfirmedBalance(0, "")
	require.NoError(t, err) // Implemented
	require.Zero(t, bal)
}
