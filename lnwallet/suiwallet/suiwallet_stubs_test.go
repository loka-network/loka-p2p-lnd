// Package suiwallet provides a stub WalletController, BlockChainIO, Signer,
// SecretKeyRing and MessageSigner implementation for the Sui DAG backend.
// This file tests that all stub implementations compile, satisfy their
// interfaces, and return the expected ErrUnsupported sentinel for every
// unimplemented method.
package suiwallet

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// Wallet (WalletController + MessageSigner)
// ----------------------------------------------------------------------------

// TestWalletBackEnd verifies BackEnd returns the canonical "sui" string.
func TestWalletBackEnd(t *testing.T) {
	t.Parallel()
	w := New()
	require.Equal(t, "sui", w.BackEnd())
}

// TestWalletIsSynced verifies that the Sui stub reports itself as always
// synced (the wallet is virtual and tracks no real chain state).
func TestWalletIsSynced(t *testing.T) {
	t.Parallel()
	w := New()
	synced, _, err := w.IsSynced()
	require.NoError(t, err)
	require.True(t, synced)
}

// TestWalletStartStop verifies that Start and Stop are no-ops that return nil.
func TestWalletStartStop(t *testing.T) {
	t.Parallel()
	w := New()
	require.NoError(t, w.Start())
	require.NoError(t, w.Stop())
}

// TestWalletGetRecoveryInfo verifies the stub recovery response values.
func TestWalletGetRecoveryInfo(t *testing.T) {
	t.Parallel()
	w := New()
	inRecovery, progress, err := w.GetRecoveryInfo()
	require.NoError(t, err)
	require.False(t, inRecovery)
	require.Equal(t, float64(1.0), progress)
}

// TestWalletUnsupportedMethods verifies that the remaining WalletController
// methods return ErrUnsupported.
func TestWalletUnsupportedMethods(t *testing.T) {
	t.Parallel()
	w := New()

	// ListUnspentWitness
	_, err := w.ListUnspentWitness(0, 0, "")
	require.ErrorIs(t, err, ErrUnsupported)

	// ListTransactionDetails
	_, _, _, err = w.ListTransactionDetails(0, 0, "", 0, 0)
	require.ErrorIs(t, err, ErrUnsupported)

	// PublishTransaction
	require.ErrorIs(t, w.PublishTransaction(&wire.MsgTx{}, ""), ErrUnsupported)

	// SubscribeTransactions
	_, err = w.SubscribeTransactions()
	require.ErrorIs(t, err, ErrUnsupported)
}

// TestWalletSignMessage verifies that MessageSigner.SignMessage returns
// ErrUnsupported (Sui uses its own signing layer).
func TestWalletSignMessage(t *testing.T) {
	t.Parallel()
	w := New()
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
	io := &SuiBlockChainIO{}

	_, _, err := io.GetBestBlock()
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = io.GetUtxo(nil, nil, 0, nil)
	require.ErrorIs(t, err, ErrUnsupported)

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
	s := &SuiSigner{}

	_, err := s.SignOutputRaw(nil, nil)
	require.ErrorIs(t, err, ErrUnsupported)

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
	kr := &SuiKeyRing{}

	_, err := kr.DeriveNextKey(keychain.KeyFamily(0))
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = kr.DeriveKey(keychain.KeyLocator{})
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = kr.ECDH(keychain.KeyDescriptor{}, &btcec.PublicKey{})
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = kr.SignMessage(keychain.KeyLocator{}, nil, false)
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = kr.SignMessageCompact(keychain.KeyLocator{}, nil, false)
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = kr.SignMessageSchnorr(
		keychain.KeyLocator{}, nil, false, nil, nil,
	)
	require.ErrorIs(t, err, ErrUnsupported)

	_, err = kr.DerivePrivKey(keychain.KeyDescriptor{})
	require.ErrorIs(t, err, ErrUnsupported)
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
	w := New()
	require.NotNil(t, w)

	// Spot-check a few well-known properties.
	require.Equal(t, "sui", w.BackEnd())
	bal, err := w.ConfirmedBalance(0, "")
	require.ErrorIs(t, err, ErrUnsupported)
	require.Zero(t, bal)
}
