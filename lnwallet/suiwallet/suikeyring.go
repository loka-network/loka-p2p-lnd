package suiwallet

import (
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/keychain"
)

// SuiKeyRing is an adapter that implements the keychain.SecretKeyRing interface
// for the Sui network. It wraps a base Bitcoin keyring and overrides the
// coin type to Sui's BIP-44 value (784).
type SuiKeyRing struct {
	keychain.SecretKeyRing
}

// Compile-time assertion that SuiKeyRing satisfies keychain.SecretKeyRing.
var _ keychain.SecretKeyRing = (*SuiKeyRing)(nil)

const (
	// CoinTypeSui is the BIP-44 coin type for Sui.
	CoinTypeSui uint32 = 784
)

// --- keychain.KeyRing methods ---

// DeriveNextKey derives the next external key in the given family for the
// Sui coin type.
func (k *SuiKeyRing) DeriveNextKey(
	family keychain.KeyFamily) (keychain.KeyDescriptor, error) {

	return k.SecretKeyRing.DeriveNextKey(family)
}

// DeriveKey derives an arbitrary key identified by the passed KeyLocator
// for the Sui coin type.
func (k *SuiKeyRing) DeriveKey(
	locator keychain.KeyLocator) (keychain.KeyDescriptor, error) {

	return k.SecretKeyRing.DeriveKey(locator)
}

// --- keychain.SecretKeyRing methods ---

// DerivePrivKey derives the private key corresponding to the given descriptor
// using the Sui coin type.
func (k *SuiKeyRing) DerivePrivKey(
	desc keychain.KeyDescriptor) (*btcec.PrivateKey, error) {

	return k.SecretKeyRing.DerivePrivKey(desc)
}
