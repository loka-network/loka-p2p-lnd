package input

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcec/v2/schnorr/musig2"
	"github.com/btcsuite/btcd/txscript"
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

// SignStateUpdateWire signs the EIP-712 StateUpdate digest and returns it as an
// input.Signature (a 64-byte ECDSA r,s), the form carried in the BOLT
// commitment_signed message. This is the off-chain commitment signature the EVM
// channel hook produces in place of a SegWit sighash signature; the recovery
// byte (v) the contract needs at forceClose/penalize is re-derived on-chain from
// the known signer address, so it is not transported here.
func (s *EvmSigner) SignStateUpdateWire(keyDesc keychain.KeyDescriptor,
	domain EvmDomain, su EvmStateUpdate) (Signature, error) {

	privKey, err := s.keyRing.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, err
	}

	digest := su.Digest(domain)

	return btcecdsa.Sign(privKey, digest[:]), nil
}

// RecoverEvmSigV reconstructs the 65-byte (r ‖ s ‖ v) Ethereum signature the
// ChannelManager's ECDSA.recover expects, from the 64-byte (r ‖ s) commitment
// signature LND carries on the wire (lnwire.Sig drops the recovery byte). It is
// the keystone of EVM breach handling: the off-chain commitment_signed only
// transports r,s, but forceClose / penalize must recover the signer on-chain, so
// v has to be re-derived before the retained counterparty signature can be
// submitted.
//
// EVM has no on-chain revocation-key construction; "revocation" reduces to
// retaining the counterparty's latest StateUpdate signature and proving a newer
// one exists (contract penalize, newer-nonce model). This function turns that
// retained 64-byte signature into the form the contract accepts.
//
// It tries both legal recovery ids (v ∈ {27, 28}) over digest and returns the
// 65-byte signature whose recovered key derives expected; it errors if neither
// does — i.e. rs is not expected's signature over digest.
func RecoverEvmSigV(rs []byte, digest [32]byte, expected [20]byte) ([]byte,
	error) {

	if len(rs) != 64 {
		return nil, fmt.Errorf("evm_signer: want 64-byte r||s sig, "+
			"got %d", len(rs))
	}

	// btcec RecoverCompact wants [header ‖ r ‖ s]; header = 27 + recid for an
	// uncompressed key, which is exactly Ethereum's v.
	for _, v := range []byte{27, 28} {
		compact := make([]byte, 65)
		compact[0] = v
		copy(compact[1:], rs)

		pub, _, err := btcecdsa.RecoverCompact(compact, digest[:])
		if err != nil {
			continue
		}
		if EvmAddressFromPubKey(pub) == expected {
			ethSig := make([]byte, 65)
			copy(ethSig, rs)
			ethSig[64] = v

			return ethSig, nil
		}
	}

	return nil, fmt.Errorf("evm_signer: no recovery id recovers signer "+
		"%x for the given digest", expected)
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

// SignOutputRaw satisfies input.Signer with standard SegWit-v0 sighash
// signing. The EVM commitment signature itself is an EIP-712 StateUpdate
// (signed via SignStateUpdateWire in channel.go's evmChainActive branch), but
// the legacy per-HTLC signature batch still runs over the internal commitment
// transaction: both peers build the identical tx, so the sigs self-verify
// peer-to-peer through the unchanged genHtlcSigValidationJobs path. The math
// here is therefore byte-for-byte the Bitcoin signer's (plain witness sighash,
// no extra hashing), mirroring suiwallet.SuiSigner's fallback branch.
func (s *EvmSigner) SignOutputRaw(tx *wire.MsgTx,
	signDesc *SignDescriptor) (Signature, error) {

	privKey, err := s.keyRing.DerivePrivKey(signDesc.KeyDesc)
	if err != nil {
		return nil, err
	}

	// Apply the single/double tweaks the descriptor demands, exactly as
	// the Bitcoin signers do, so tweaked keys (HTLC, revocation) derive
	// identically on both peers.
	switch {
	case signDesc.SingleTweak != nil:
		privKey = TweakPrivKey(privKey, signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		privKey = DeriveRevocationPrivKey(privKey, signDesc.DoubleTweak)
	}

	sigHash, err := txscript.CalcWitnessSigHash(
		signDesc.WitnessScript, signDesc.SigHashes, signDesc.HashType,
		tx, signDesc.InputIndex, signDesc.Output.Value,
	)
	if err != nil {
		return nil, fmt.Errorf("evm_signer: failed to calc sighash: %w",
			err)
	}

	return btcecdsa.Sign(privKey, sigHash), nil
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
