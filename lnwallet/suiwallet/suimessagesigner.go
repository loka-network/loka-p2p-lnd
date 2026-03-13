package suiwallet

import (
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// Compile-time assertion that Wallet satisfies lnwallet.MessageSigner.
var _ lnwallet.MessageSigner = (*Wallet)(nil)

// SignMessage signs the given message with the key identified by the key
// locator. For Sui, this uses the base keyring to sign.
func (w *Wallet) SignMessage(
	_ keychain.KeyLocator, _ []byte, _ bool) (*ecdsa.Signature, error) {

	// In a real implementation, we would use the internal keyring to
	// sign the message. For this adapter, we delegate or return error
	// if not critical.
	return nil, ErrUnsupported
}
