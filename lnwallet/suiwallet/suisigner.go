package suiwallet

import (
	"crypto/sha256"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcec/v2/schnorr/musig2"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

// SuiSigner is an adapter that implements the input.Signer interface for the
// Sui network. It uses a SecretKeyRing to derive the required private keys
// for signing.
type SuiSigner struct {
	keyRing keychain.SecretKeyRing
}

// NewSuiSigner creates a new SuiSigner instance backed by the given keyring.
func NewSuiSigner(keyRing keychain.SecretKeyRing) *SuiSigner {
	return &SuiSigner{
		keyRing: keyRing,
	}
}

// Compile-time assertion that SuiSigner satisfies the Signer interface.
var _ input.Signer = (*SuiSigner)(nil)

// SignOutputRaw generates a signature for the passed transaction according to
// the data within the passed SignDescriptor.
//
// For Sui, tx is a wire.MsgTx where tx.TxIn[0].SignatureScript contains the
// serialized Move call or event to be signed.
func (s *SuiSigner) SignOutputRaw(
	tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (input.Signature, error) {

	// Derive the private key for the given key descriptor.
	privKey, err := s.keyRing.DerivePrivKey(signDesc.KeyDesc)
	if err != nil {
		return nil, err
	}

	// Sui signatures are over the SHA256 digest of the serialized data.
	// In our adapter, this data is stored in the first input's SignatureScript.
	if len(tx.TxIn) == 0 {
		return nil, fmt.Errorf("sui_signer: tx has no inputs")
	}
	digest := sha256.Sum256(tx.TxIn[0].SignatureScript)

	sig := ecdsa.Sign(privKey, digest[:])

	return sig, nil
}

// ComputeInputScript generates a complete InputScript for the passed
// transaction.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SuiSigner) ComputeInputScript(
	_ *wire.MsgTx,
	_ *input.SignDescriptor) (*input.Script, error) {

	return nil, ErrUnsupported
}

// --- MuSig2Signer interface methods (all stubs) ---

// MuSig2CreateSession creates a new MuSig2 signing session.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SuiSigner) MuSig2CreateSession(
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
func (s *SuiSigner) MuSig2RegisterNonces(
	_ input.MuSig2SessionID,
	_ [][musig2.PubNonceSize]byte) (bool, error) {

	return false, ErrUnsupported
}

// MuSig2RegisterCombinedNonce registers a pre-aggregated combined nonce.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SuiSigner) MuSig2RegisterCombinedNonce(
	_ input.MuSig2SessionID,
	_ [musig2.PubNonceSize]byte) error {

	return ErrUnsupported
}

// MuSig2GetCombinedNonce retrieves the combined nonce for a session.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SuiSigner) MuSig2GetCombinedNonce(
	_ input.MuSig2SessionID) ([musig2.PubNonceSize]byte, error) {

	return [musig2.PubNonceSize]byte{}, ErrUnsupported
}

// MuSig2Sign creates a partial signature.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SuiSigner) MuSig2Sign(
	_ input.MuSig2SessionID,
	_ [sha256.Size]byte,
	_ bool) (*musig2.PartialSignature, error) {

	return nil, ErrUnsupported
}

// MuSig2CombineSig combines partial signatures.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SuiSigner) MuSig2CombineSig(
	_ input.MuSig2SessionID,
	_ []*musig2.PartialSignature) (*schnorr.Signature, bool, error) {

	return nil, false, ErrUnsupported
}

// MuSig2Cleanup removes a MuSig2 session from memory.
//
// NOTE: Stub — returns ErrUnsupported.
func (s *SuiSigner) MuSig2Cleanup(_ input.MuSig2SessionID) error {
	return ErrUnsupported
}
