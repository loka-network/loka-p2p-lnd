package evmwallet

import (
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// Compile-time assertion that Wallet satisfies lnwallet.MessageSigner.
var _ lnwallet.MessageSigner = (*Wallet)(nil)

// SignMessage signs the given message with the key identified by the key
// locator, delegating to the base keyring (EVM reuses secp256k1, so node
// announcements and gossip signatures are unchanged).
func (w *Wallet) SignMessage(keyLoc keychain.KeyLocator, msg []byte,
	doubleHash bool) (*ecdsa.Signature, error) {

	return w.cfg.KeyRing.SignMessage(keyLoc, msg, doubleHash)
}
