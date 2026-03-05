package setuwallet

import (
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/lightningnetwork/lnd/keychain"
)

// SetuKeyRing is a stub implementation of the keychain.SecretKeyRing interface
// for the Setu DAG backend.
//
// Key derivation on Setu follows the same BIP-44 derivation path as Bitcoin
// (m/1017'/coinType'/keyFamily'/0/index) but with coinType = 99999 as defined
// in chainreg.CoinTypeSetu.  secp256k1 keypairs are used for compatibility
// with the existing LND peer-to-peer protocol.
//
// All methods currently return ErrUnsupported. They will be replaced with real
// HD derivation once the key management layer (wrapping the Setu SDK's
// setu-keys crate) is implemented.
type SetuKeyRing struct{}

// Compile-time assertion that SetuKeyRing satisfies keychain.SecretKeyRing.
var _ keychain.SecretKeyRing = (*SetuKeyRing)(nil)

// --- keychain.KeyRing methods ---

// DeriveNextKey derives the next external key in the given family.
//
// NOTE: Stub — returns ErrUnsupported.
func (k *SetuKeyRing) DeriveNextKey(
	_ keychain.KeyFamily) (keychain.KeyDescriptor, error) {

	return keychain.KeyDescriptor{}, ErrUnsupported
}

// DeriveKey derives an arbitrary key identified by the passed KeyLocator.
//
// NOTE: Stub — returns ErrUnsupported.
func (k *SetuKeyRing) DeriveKey(
	_ keychain.KeyLocator) (keychain.KeyDescriptor, error) {

	return keychain.KeyDescriptor{}, ErrUnsupported
}

// --- keychain.ECDHRing methods ---

// ECDH performs a scalar multiplication between the target key descriptor and
// a remote public key.
//
// NOTE: Stub — returns ErrUnsupported.
func (k *SetuKeyRing) ECDH(
	_ keychain.KeyDescriptor, _ *btcec.PublicKey) ([32]byte, error) {

	return [32]byte{}, ErrUnsupported
}

// --- keychain.MessageSignerRing methods ---

// SignMessage signs the given message with the private key described by the
// key locator.
//
// NOTE: Stub — returns ErrUnsupported.
func (k *SetuKeyRing) SignMessage(
	_ keychain.KeyLocator, _ []byte, _ bool) (*ecdsa.Signature, error) {

	return nil, ErrUnsupported
}

// SignMessageCompact signs the given message and returns it in compact,
// recoverable format.
//
// NOTE: Stub — returns ErrUnsupported.
func (k *SetuKeyRing) SignMessageCompact(
	_ keychain.KeyLocator, _ []byte, _ bool) ([]byte, error) {

	return nil, ErrUnsupported
}

// SignMessageSchnorr signs the given message with a Schnorr signature.
//
// NOTE: Stub — returns ErrUnsupported.
func (k *SetuKeyRing) SignMessageSchnorr(
	_ keychain.KeyLocator, _ []byte, _ bool,
	_ []byte, _ []byte) (*schnorr.Signature, error) {

	return nil, ErrUnsupported
}

// --- keychain.SecretKeyRing methods ---

// DerivePrivKey derives the private key corresponding to the given descriptor.
//
// NOTE: Stub — returns ErrUnsupported.
func (k *SetuKeyRing) DerivePrivKey(
	_ keychain.KeyDescriptor) (*btcec.PrivateKey, error) {

	return nil, ErrUnsupported
}
