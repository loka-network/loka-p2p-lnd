package input

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// hexToBytes32 decodes a 0x-prefixed 32-byte hex string for test vectors.
func hexToBytes32(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	if len(b) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(b))
	}
	var out [32]byte
	copy(out[:], b)

	return out
}

// Golden vectors emitted by the Solidity suite
// (evm-contracts/channel-manager/test/Eip712Vectors.t.sol). They pin the Go
// EIP-712 implementation to the deployed ChannelManager byte-for-byte.
const (
	goldenChainID    = 31337
	goldenContract   = "5615dEB798BB3E4dFa0139dFa1b3D433Cc23b72f"
	goldenDomainSep  = "f9539b7625e92735545c67c4e960feba8c96f55f2eadb421aa810332e49afed3"
	goldenChannelID  = "cf71ef495a3b75cecb486559f73ef932fad04e7ddcc3ddadd2c144e4c5ecce25"
	goldenStateDig   = "261e14131a6d9c28cc84579fe345605ff2f942b95fdcbae3bfb793f30e7513c9"
	goldenCoopDigest = "d671c8ec9682eb8e1d097009119d0b2418bd15e9e3a857fd3e51c1b34140994e"
)

func goldenDomain(t *testing.T) EvmDomain {
	t.Helper()
	cb, err := hex.DecodeString(goldenContract)
	if err != nil {
		t.Fatal(err)
	}
	var addr [20]byte
	copy(addr[:], cb)

	return EvmDomain{ChainID: goldenChainID, VerifyingContract: addr}
}

func TestEvmDomainSeparator(t *testing.T) {
	t.Parallel()
	got := goldenDomain(t).Separator()
	want := hexToBytes32(t, goldenDomainSep)
	if got != want {
		t.Fatalf("domainSeparator mismatch\n got %x\nwant %x", got, want)
	}
}

func TestEvmStateUpdateDigest(t *testing.T) {
	t.Parallel()
	su := EvmStateUpdate{
		ChannelID: hexToBytes32(t, goldenChannelID),
		Nonce:     5,
		BalanceA:  big.NewInt(600_000_000),
		BalanceB:  big.NewInt(400_000_000),
		HtlcsHash: [32]byte{},
	}
	got := su.Digest(goldenDomain(t))
	want := hexToBytes32(t, goldenStateDig)
	if got != want {
		t.Fatalf("StateUpdate digest mismatch\n got %x\nwant %x",
			got, want)
	}
}

func TestEvmCooperativeCloseDigest(t *testing.T) {
	t.Parallel()
	cc := EvmCooperativeClose{
		ChannelID:     hexToBytes32(t, goldenChannelID),
		Nonce:         5,
		FinalBalanceA: big.NewInt(600_000_000),
		FinalBalanceB: big.NewInt(400_000_000),
	}
	got := cc.Digest(goldenDomain(t))
	want := hexToBytes32(t, goldenCoopDigest)
	if got != want {
		t.Fatalf("CooperativeClose digest mismatch\n got %x\nwant %x",
			got, want)
	}
}

// Golden vector emitted by StateUpdateHtlcsVectors.t.sol — the combined path a
// channel commitment signs: a 2-HTLC Merkle root folded into a StateUpdate
// digest (htlcsHash != 0). Same domain as the other vectors above.
const (
	goldenHtlcsHash     = "13b0b913ea0a2a4af8babab4b39089aeb4438448075bf3e91427a312749fd2e3"
	goldenHtlcLeaf4     = "a821e59aab4b114ff66e39447e5518df32a3df9598309a9e313019f71f65b6cc"
	goldenHtlcLeaf7     = "8cf75b368bb1c3d8f80ed67cfc31fa81d0eab692186e97a65eaa851dd5841b93"
	goldenStateDigHtlcs = "07579810fc7c56ed1ff2fd32885bb2f5c4a76c5dac91ee242d684cc173112dcb"
)

// TestEvmStateUpdateWithHtlcsDigest proves the full off-chain commitment
// artifact agrees with the contract: building the HTLC set in Go, computing the
// htlcsHash via HtlcsMerkleRoot, and folding it into EvmStateUpdate.Digest
// reproduces the digest the ChannelManager computes byte-for-byte.
func TestEvmStateUpdateWithHtlcsDigest(t *testing.T) {
	t.Parallel()

	// address(uint160(0xA11CE)) and address(uint160(0xB0B)) — right-aligned.
	var recipA, recipB [20]byte
	recipA[17], recipA[18], recipA[19] = 0x0A, 0x11, 0xCE
	recipB[18], recipB[19] = 0x0B, 0x0B

	htlcs := []EvmHTLC{
		{
			Index:     4,
			Amount:    big.NewInt(25_000_000),
			Hashlock:  Keccak256([]byte("preimage-A")),
			Timelock:  1_700_000_000,
			Recipient: recipA,
		},
		{
			Index:     7,
			Amount:    big.NewInt(15_000_000),
			Hashlock:  Keccak256([]byte("preimage-B")),
			Timelock:  1_700_000_500,
			Recipient: recipB,
		},
	}

	// Leaves must match the contract's keccak256(abi.encode(HTLC)).
	if got := htlcs[0].Leaf(); got != hexToBytes32(t, goldenHtlcLeaf4) {
		t.Fatalf("htlc leaf 4 mismatch\n got %x\nwant %s", got, goldenHtlcLeaf4)
	}
	if got := htlcs[1].Leaf(); got != hexToBytes32(t, goldenHtlcLeaf7) {
		t.Fatalf("htlc leaf 7 mismatch\n got %x\nwant %s", got, goldenHtlcLeaf7)
	}

	htlcsHash := HtlcsMerkleRoot(htlcs)
	if htlcsHash != hexToBytes32(t, goldenHtlcsHash) {
		t.Fatalf("htlcsHash mismatch\n got %x\nwant %s", htlcsHash,
			goldenHtlcsHash)
	}

	su := EvmStateUpdate{
		ChannelID: hexToBytes32(t, goldenChannelID),
		Nonce:     5,
		BalanceA:  big.NewInt(600_000_000),
		BalanceB:  big.NewInt(400_000_000),
		HtlcsHash: htlcsHash,
	}
	got := su.Digest(goldenDomain(t))
	if got != hexToBytes32(t, goldenStateDigHtlcs) {
		t.Fatalf("StateUpdate(htlcs) digest mismatch\n got %x\nwant %s",
			got, goldenStateDigHtlcs)
	}
}

// TestEvmChannelID checks the channelId derivation matches the contract's
// keccak256(abi.encodePacked(A, B, salt)). Vector generated alongside the
// Solidity computeChannelId.
func TestEvmChannelID(t *testing.T) {
	t.Parallel()
	var a, b [20]byte
	a[19] = 0xaa
	b[19] = 0xbb
	var salt [32]byte
	salt[31] = 0x07

	// keccak256(abi.encodePacked(0x..aa, 0x..bb, 0x..07)).
	got := EvmChannelID(a, b, salt)

	// Recompute independently via Keccak256 over the packed bytes to guard
	// against accidental padding regressions.
	packed := append(append(append([]byte{}, a[:]...), b[:]...), salt[:]...)
	want := Keccak256(packed)
	if got != want {
		t.Fatalf("channelId mismatch\n got %x\nwant %x", got, want)
	}
	if len(packed) != 72 {
		t.Fatalf("packed encoding must be 72 bytes, got %d", len(packed))
	}
}

// Golden vector emitted by SigRecoveryVectors.t.sol: Anvil account 0's signature
// over goldenStateDig, as the canonical (r, s) the contract's ECDSA.recover
// accepts. anvilAcct0Addr is that key's EVM address (== TestEvmAddressFromPubKey).
const (
	goldenSigR     = "3427a7c5a654686fd5d583e5aae99933b019732c8283d3de3e4e87c60b1af8e6"
	goldenSigS     = "1e8edaf5dcc1a4d512bc2a280da16ee8deca041ee7244829efb07115e818b138"
	anvilAcct0Addr = "f39fd6e51aad88f6f4ce6ab8827279cfffb92266"
)

// TestRecoverEvmSigV pins the v-recovery to a contract-accepted signature: given
// only the 64-byte (r ‖ s) LND retains on the wire plus the signer's address,
// RecoverEvmSigV must reproduce the 65-byte (r ‖ s ‖ v) that the ChannelManager's
// ECDSA.recover resolves to that signer — the keystone of EVM breach handling.
func TestRecoverEvmSigV(t *testing.T) {
	t.Parallel()

	digest := hexToBytes32(t, goldenStateDig)
	r := hexToBytes32(t, goldenSigR)
	s := hexToBytes32(t, goldenSigS)
	rs := append(append([]byte{}, r[:]...), s[:]...)

	addrBytes, err := hex.DecodeString(anvilAcct0Addr)
	if err != nil {
		t.Fatal(err)
	}
	var expected [20]byte
	copy(expected[:], addrBytes)

	sig, err := RecoverEvmSigV(rs, digest, expected)
	if err != nil {
		t.Fatalf("RecoverEvmSigV: %v", err)
	}
	if len(sig) != 65 {
		t.Fatalf("want 65-byte sig, got %d", len(sig))
	}
	if !bytes.Equal(sig[:64], rs) {
		t.Fatal("recovered sig must preserve the original r||s")
	}
	if v := sig[64]; v != 27 && v != 28 {
		t.Fatalf("v must be 27 or 28, got %d", v)
	}

	// Independently confirm the reconstructed signature recovers to the
	// signer, exactly as the contract would.
	compact := make([]byte, 65)
	compact[0] = sig[64]
	copy(compact[1:], rs)
	recovered, _, err := btcecdsa.RecoverCompact(compact, digest[:])
	if err != nil {
		t.Fatalf("recover failed: %v", err)
	}
	if EvmAddressFromPubKey(recovered) != expected {
		t.Fatal("reconstructed sig does not recover to expected signer")
	}

	// A wrong expected signer must be rejected, not silently mis-attributed.
	var wrong [20]byte
	wrong[0] = 0x01
	if _, err := RecoverEvmSigV(rs, digest, wrong); err == nil {
		t.Fatal("expected error recovering to a non-signer address")
	}
}

// TestRecoverEvmSigVRoundTrip guards the byte layout end-to-end: a freshly
// produced 65-byte signature, stripped to its wire 64-byte r||s, must recover to
// the identical 65 bytes.
func TestRecoverEvmSigVRoundTrip(t *testing.T) {
	t.Parallel()

	const pkHex = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	pkb, _ := hex.DecodeString(pkHex)
	priv, _ := btcec.PrivKeyFromBytes(pkb)
	addr := EvmAddressFromPubKey(priv.PubKey())

	digest := Keccak256([]byte("breach evidence digest"))
	full, err := signDigestWithKey(priv, digest)
	if err != nil {
		t.Fatal(err)
	}

	got, err := RecoverEvmSigV(full[:64], digest, addr)
	if err != nil {
		t.Fatalf("RecoverEvmSigV: %v", err)
	}
	if !bytes.Equal(got, full) {
		t.Fatalf("round-trip mismatch\n got %x\nwant %x", got, full)
	}
}

// TestEvmAddressFromPubKey checks the EVM address derivation against the
// well-known Anvil account 0 keypair.
func TestEvmAddressFromPubKey(t *testing.T) {
	t.Parallel()
	// Anvil account 0.
	const pkHex = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	const wantAddr = "f39fd6e51aad88f6f4ce6ab8827279cfffb92266"

	pkb, err := hex.DecodeString(pkHex)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := btcec.PrivKeyFromBytes(pkb)
	got := EvmAddressFromPubKey(priv.PubKey())

	want, _ := hex.DecodeString(wantAddr)
	if !bytes.Equal(got[:], want) {
		t.Fatalf("address mismatch\n got %x\nwant %x", got, want)
	}
}

// TestEvmSignDigestRecovers verifies the 65-byte signature reformatting yields
// a signature that recovers to the signer's public key, with v ∈ {27,28} — the
// exact shape OpenZeppelin's ECDSA.recover accepts.
func TestEvmSignDigestRecovers(t *testing.T) {
	t.Parallel()
	const pkHex = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	pkb, _ := hex.DecodeString(pkHex)
	priv, _ := btcec.PrivKeyFromBytes(pkb)

	digest := Keccak256([]byte("some eip712 digest"))
	sig, err := signDigestWithKey(priv, digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 65 {
		t.Fatalf("want 65-byte sig, got %d", len(sig))
	}
	if v := sig[64]; v != 27 && v != 28 {
		t.Fatalf("v must be 27 or 28, got %d", v)
	}

	// Reconstruct btcec compact layout [v ‖ r ‖ s] and recover.
	compact := make([]byte, 65)
	compact[0] = sig[64]
	copy(compact[1:], sig[0:64])
	recovered, _, err := btcecdsa.RecoverCompact(compact, digest[:])
	if err != nil {
		t.Fatalf("recover failed: %v", err)
	}
	if !recovered.IsEqual(priv.PubKey()) {
		t.Fatal("recovered pubkey does not match signer")
	}
}
