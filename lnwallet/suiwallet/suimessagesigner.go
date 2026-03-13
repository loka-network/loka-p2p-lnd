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
	keyLoc keychain.KeyLocator, msg []byte, doubleHash bool) (*ecdsa.Signature, error) {

	return w.cfg.KeyRing.SignMessage(keyLoc, msg, doubleHash)
}
