package suiwallet

import (
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// Compile-time assertion that Wallet satisfies lnwallet.MessageSigner.
var _ lnwallet.MessageSigner = (*Wallet)(nil)

// SignMessage signs the given message with the key identified by the key
// locator.
//
// NOTE: Stub — returns ErrUnsupported until the key management layer is
// implemented.
func (w *Wallet) SignMessage(
	_ keychain.KeyLocator, _ []byte, _ bool) (*ecdsa.Signature, error) {

	return nil, ErrUnsupported
}
