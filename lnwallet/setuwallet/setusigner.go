package setuwallet

import (
	"crypto/sha256"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcec/v2/schnorr/musig2"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

// SetuSigner is a stub implementation of the input.Signer interface for the
// Setu DAG backend.
//
// Setu channels use secp256k1 ECDSA signatures (same curve as Bitcoin), so
// the signing interface can be re-used almost unchanged.  All methods here
// return ErrUnsupported until the real key-management layer is wired in.
type SetuSigner struct{}

// Compile-time assertion that SetuSigner satisfies the Signer interface.
var _ input.Signer = (*SetuSigner)(nil)

// SignOutputRaw generates a signature for the passed transaction according to
// the data within the passed SignDescriptor.
//
// NOTE: For Setu, tx.Payload carries the serialised Setu Event bytes.
// Stub — returns ErrUnsupported.
func (s *SetuSigner) SignOutputRaw(
	_ *wire.MsgTx,
	_ *input.SignDescriptor) (input.Signature, error) {

	return nil, ErrUnsupported
}

// ComputeInputScript generates a complete InputScript for the passed
// transaction.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SetuSigner) ComputeInputScript(
	_ *wire.MsgTx,
	_ *input.SignDescriptor) (*input.Script, error) {

	return nil, ErrUnsupported
}

// --- MuSig2Signer interface methods (all stubs) ---

// MuSig2CreateSession creates a new MuSig2 signing session.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SetuSigner) MuSig2CreateSession(
	_ input.MuSig2Version,
	_ keychain.KeyLocator,
	_ []*btcec.PublicKey,
	_ *input.MuSig2Tweaks,
	_ [][musig2.PubNonceSize]byte,
	_ *musig2.Nonces) (*input.MuSig2SessionInfo, error) {

	return nil, ErrUnsupported
}

// MuSig2RegisterNonces registers public nonces for a MuSig2 session.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SetuSigner) MuSig2RegisterNonces(
	_ input.MuSig2SessionID,
	_ [][musig2.PubNonceSize]byte) (bool, error) {

	return false, ErrUnsupported
}

// MuSig2RegisterCombinedNonce registers a pre-aggregated combined nonce.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SetuSigner) MuSig2RegisterCombinedNonce(
	_ input.MuSig2SessionID,
	_ [musig2.PubNonceSize]byte) error {

	return ErrUnsupported
}

// MuSig2GetCombinedNonce retrieves the combined nonce for a session.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SetuSigner) MuSig2GetCombinedNonce(
	_ input.MuSig2SessionID) ([musig2.PubNonceSize]byte, error) {

	return [musig2.PubNonceSize]byte{}, ErrUnsupported
}

// MuSig2Sign creates a partial signature.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SetuSigner) MuSig2Sign(
	_ input.MuSig2SessionID,
	_ [sha256.Size]byte,
	_ bool) (*musig2.PartialSignature, error) {

	return nil, ErrUnsupported
}

// MuSig2CombineSig combines partial signatures.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SetuSigner) MuSig2CombineSig(
	_ input.MuSig2SessionID,
	_ []*musig2.PartialSignature) (*schnorr.Signature, bool, error) {

	return nil, false, ErrUnsupported
}

// MuSig2Cleanup removes a MuSig2 session from memory.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SetuSigner) MuSig2Cleanup(_ input.MuSig2SessionID) error {
	return ErrUnsupported
}
