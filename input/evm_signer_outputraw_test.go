package input

import (
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/keychain"
)

// fixedKeyRing is a minimal keychain.SecretKeyRing returning one fixed key,
// enough to exercise EvmSigner.SignOutputRaw.
type fixedKeyRing struct {
	priv *btcec.PrivateKey
}

func (f *fixedKeyRing) DeriveNextKey(keychain.KeyFamily) (
	keychain.KeyDescriptor, error) {

	return keychain.KeyDescriptor{PubKey: f.priv.PubKey()}, nil
}

func (f *fixedKeyRing) DeriveKey(loc keychain.KeyLocator) (
	keychain.KeyDescriptor, error) {

	return keychain.KeyDescriptor{
		KeyLocator: loc,
		PubKey:     f.priv.PubKey(),
	}, nil
}

func (f *fixedKeyRing) DerivePrivKey(keychain.KeyDescriptor) (
	*btcec.PrivateKey, error) {

	return f.priv, nil
}

func (f *fixedKeyRing) ECDH(keychain.KeyDescriptor, *btcec.PublicKey) (
	[32]byte, error) {

	return [32]byte{}, nil
}

func (f *fixedKeyRing) SignMessage(keychain.KeyLocator, []byte, bool) (
	*ecdsa.Signature, error) {

	return nil, nil
}

func (f *fixedKeyRing) SignMessageCompact(keychain.KeyLocator, []byte, bool) (
	[]byte, error) {

	return nil, nil
}

func (f *fixedKeyRing) SignMessageSchnorr(keychain.KeyLocator, []byte, bool,
	[]byte, []byte) (*schnorr.Signature, error) {

	return nil, nil
}

// outputRawTestTx builds a 1-in/1-out tx and the SignDescriptor for spending a
// (synthetic) witness-script output of the given value.
func outputRawTestTx(t *testing.T, witnessScript []byte,
	value int64) (*wire.MsgTx, *SignDescriptor) {

	t.Helper()

	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Index: 0},
	})
	tx.AddTxOut(&wire.TxOut{Value: value - 100, PkScript: []byte{0x51}})

	prevOut := &wire.TxOut{Value: value, PkScript: witnessScript}
	fetcher := txscript.NewCannedPrevOutputFetcher(
		prevOut.PkScript, prevOut.Value,
	)

	desc := &SignDescriptor{
		KeyDesc: keychain.KeyDescriptor{
			KeyLocator: keychain.KeyLocator{
				Family: keychain.KeyFamilyMultiSig,
			},
		},
		WitnessScript: witnessScript,
		Output:        prevOut,
		HashType:      txscript.SigHashAll,
		SigHashes:     txscript.NewTxSigHashes(tx, fetcher),
		InputIndex:    0,
	}

	return tx, desc
}

// TestEvmSignOutputRaw checks that the EVM signer's SegWit fallback produces
// a signature that verifies against the standard witness sighash — the exact
// check genHtlcSigValidationJobs runs on the receiving peer for the legacy
// per-HTLC signature batch.
func TestEvmSignOutputRaw(t *testing.T) {
	t.Parallel()

	pkb, _ := hex.DecodeString(
		"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4" +
			"f2ff80",
	)
	priv, _ := btcec.PrivKeyFromBytes(pkb)
	signer := NewEvmSigner(&fixedKeyRing{priv: priv})

	witnessScript := []byte{0x52} // OP_2; content is opaque to the sighash
	tx, desc := outputRawTestTx(t, witnessScript, 100_000)

	sig, err := signer.SignOutputRaw(tx, desc)
	if err != nil {
		t.Fatalf("SignOutputRaw: %v", err)
	}

	sigHash, err := txscript.CalcWitnessSigHash(
		desc.WitnessScript, desc.SigHashes, desc.HashType, tx,
		desc.InputIndex, desc.Output.Value,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sig.Verify(sigHash, priv.PubKey()) {
		t.Fatal("signature does not verify against the witness sighash")
	}
}

// TestEvmSignOutputRawSingleTweak checks the SingleTweak branch: the signature
// must verify under the tweaked public key, matching how the verifying peer
// derives HTLC keys.
func TestEvmSignOutputRawSingleTweak(t *testing.T) {
	t.Parallel()

	pkb, _ := hex.DecodeString(
		"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b" +
			"78690d",
	)
	priv, _ := btcec.PrivKeyFromBytes(pkb)
	signer := NewEvmSigner(&fixedKeyRing{priv: priv})

	tweak := chainhash.HashB([]byte("per-commitment-point tweak"))
	tx, desc := outputRawTestTx(t, []byte{0x51}, 50_000)
	desc.SingleTweak = tweak

	sig, err := signer.SignOutputRaw(tx, desc)
	if err != nil {
		t.Fatalf("SignOutputRaw: %v", err)
	}

	sigHash, err := txscript.CalcWitnessSigHash(
		desc.WitnessScript, desc.SigHashes, desc.HashType, tx,
		desc.InputIndex, desc.Output.Value,
	)
	if err != nil {
		t.Fatal(err)
	}

	tweakedPub := TweakPubKeyWithTweak(priv.PubKey(), tweak)
	if !sig.Verify(sigHash, tweakedPub) {
		t.Fatal("signature does not verify under the tweaked pubkey")
	}
	if sig.Verify(sigHash, priv.PubKey()) {
		t.Fatal("signature unexpectedly verifies under the base key")
	}
}
