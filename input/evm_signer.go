package input

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcec/v2/schnorr/musig2"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/keychain"
	"golang.org/x/crypto/sha3"
)

// ErrEvmUnsupported is returned by the EVM signer for Bitcoin-only Signer
// methods (script construction, MuSig2) that have no EVM analogue.
var ErrEvmUnsupported = errors.New("evm_signer: operation not supported on EVM")

// EvmSigner adapts LND's SecretKeyRing to the two secp256k1 signing surfaces an
// EVM sub-network needs:
//
//  1. EIP-712 typed-data signatures over the off-chain StateUpdate /
//     CooperativeClose commitments (the analogue of a BOLT commitment
//     signature). Produced by SignStateUpdate / SignCooperativeClose.
//  2. Raw recoverable secp256k1 signatures over a 32-byte digest, the building
//     block for both the above and for EIP-155 transaction signing performed by
//     the evmwallet adapter.
//
// EVM reuses secp256k1, so the existing keychain.BtcWalletKeyRing is reused
// unchanged; only the BIP-44 coin type differs (60 / testnet). It also
// satisfies input.Signer so it can slot into the shared signing plumbing; the
// Bitcoin-script methods return ErrEvmUnsupported.
type EvmSigner struct {
	keyRing keychain.SecretKeyRing
}

// NewEvmSigner creates a new EvmSigner backed by the given keyring.
func NewEvmSigner(keyRing keychain.SecretKeyRing) *EvmSigner {
	return &EvmSigner{keyRing: keyRing}
}

// Compile-time assertion that EvmSigner satisfies the Signer interface.
var _ Signer = (*EvmSigner)(nil)

// EvmAddressFromPubKey derives the 20-byte EVM account address for a secp256k1
// public key: keccak256(uncompressedPubKey[1:])[12:] (the low 20 bytes of the
// hash of the 64-byte X‖Y, dropping the 0x04 prefix).
func EvmAddressFromPubKey(pub *btcec.PublicKey) [20]byte {
	uncompressed := pub.SerializeUncompressed() // 65 bytes: 0x04 ‖ X ‖ Y
	h := sha3.NewLegacyKeccak256()
	h.Write(uncompressed[1:])
	sum := h.Sum(nil)

	var addr [20]byte
	copy(addr[:], sum[12:])

	return addr
}

// SignDigest produces a 65-byte Ethereum-style recoverable signature
// (r ‖ s ‖ v, v ∈ {27,28}) over the given 32-byte digest using the key derived
// from keyDesc. btcec yields low-s canonical signatures, so the result is
// accepted by OpenZeppelin's ECDSA.recover (which rejects upper-half s and
// malleable v — spec §5).
func (s *EvmSigner) SignDigest(keyDesc keychain.KeyDescriptor,
	digest [32]byte) ([]byte, error) {

	privKey, err := s.keyRing.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, err
	}

	return signDigestWithKey(privKey, digest)
}

// SignStateUpdate signs an EIP-712 StateUpdate for the given EVM domain,
// returning the 65-byte signature presented to forceClose / penalize.
func (s *EvmSigner) SignStateUpdate(keyDesc keychain.KeyDescriptor,
	domain EvmDomain, su EvmStateUpdate) ([]byte, error) {

	return s.SignDigest(keyDesc, su.Digest(domain))
}

// SignCooperativeClose signs an EIP-712 CooperativeClose for the given EVM
// domain, returning the 65-byte signature presented to closeChannel.
func (s *EvmSigner) SignCooperativeClose(keyDesc keychain.KeyDescriptor,
	domain EvmDomain, cc EvmCooperativeClose) ([]byte, error) {

	return s.SignDigest(keyDesc, cc.Digest(domain))
}

// signDigestWithKey is the shared core: it produces a btcec recoverable compact
// signature and reformats it from btcec's [v ‖ r ‖ s] layout to Ethereum's
// [r ‖ s ‖ v].
func signDigestWithKey(privKey *btcec.PrivateKey,
	digest [32]byte) ([]byte, error) {

	// isCompressedKey=false so the recovery header byte is 27+recid (no +4
	// compressed offset), which is exactly the v ∈ {27,28} Ethereum wants.
	compact := btcecdsa.SignCompact(privKey, digest[:], false)
	if len(compact) != 65 {
		return nil, fmt.Errorf("evm_signer: unexpected compact sig "+
			"length %d", len(compact))
	}

	v := compact[0]
	ethSig := make([]byte, 65)
	copy(ethSig[0:32], compact[1:33])   // r
	copy(ethSig[32:64], compact[33:65]) // s
	ethSig[64] = v

	return ethSig, nil
}

// SignOutputRaw satisfies input.Signer. On EVM there is no SegWit sighash; the
// commitment artifact is an EIP-712 StateUpdate signed via SignStateUpdate.
// Callers in the EVM commitment path use SignStateUpdate directly, so this
// generic entry point is unsupported.
func (s *EvmSigner) SignOutputRaw(_ *wire.MsgTx,
	_ *SignDescriptor) (Signature, error) {

	return nil, ErrEvmUnsupported
}

// ComputeInputScript satisfies input.Signer; EVM has no Bitcoin input scripts.
func (s *EvmSigner) ComputeInputScript(_ *wire.MsgTx,
	_ *SignDescriptor) (*Script, error) {

	return nil, ErrEvmUnsupported
}

// --- MuSig2Signer interface methods (all unsupported on EVM) ---

// MuSig2CreateSession is unsupported on EVM.
func (s *EvmSigner) MuSig2CreateSession(_ MuSig2Version, _ keychain.KeyLocator,
	_ []*btcec.PublicKey, _ *MuSig2Tweaks, _ [][musig2.PubNonceSize]byte,
	_ *musig2.Nonces) (*MuSig2SessionInfo, error) {

	return nil, ErrEvmUnsupported
}

// MuSig2RegisterNonces is unsupported on EVM.
func (s *EvmSigner) MuSig2RegisterNonces(_ MuSig2SessionID,
	_ [][musig2.PubNonceSize]byte) (bool, error) {

	return false, ErrEvmUnsupported
}

// MuSig2RegisterCombinedNonce is unsupported on EVM.
func (s *EvmSigner) MuSig2RegisterCombinedNonce(_ MuSig2SessionID,
	_ [musig2.PubNonceSize]byte) error {

	return ErrEvmUnsupported
}

// MuSig2GetCombinedNonce is unsupported on EVM.
func (s *EvmSigner) MuSig2GetCombinedNonce(
	_ MuSig2SessionID) ([musig2.PubNonceSize]byte, error) {

	return [musig2.PubNonceSize]byte{}, ErrEvmUnsupported
}

// MuSig2Sign is unsupported on EVM.
func (s *EvmSigner) MuSig2Sign(_ MuSig2SessionID, _ [32]byte,
	_ bool) (*musig2.PartialSignature, error) {

	return nil, ErrEvmUnsupported
}

// MuSig2CombineSig is unsupported on EVM.
func (s *EvmSigner) MuSig2CombineSig(_ MuSig2SessionID,
	_ []*musig2.PartialSignature) (*schnorr.Signature, bool, error) {

	return nil, false, ErrEvmUnsupported
}

// MuSig2Cleanup is unsupported on EVM.
func (s *EvmSigner) MuSig2Cleanup(_ MuSig2SessionID) error {
	return ErrEvmUnsupported
}
