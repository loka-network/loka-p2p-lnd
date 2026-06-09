package input

import (
	"math/big"
	"testing"
)

// htlc builds an EvmHTLC matching the Solidity HtlcMerkleVectors test inputs.
func htlc(index uint64, amount int64, preimageTag string, timelock uint32,
	recipientLastByte byte) EvmHTLC {

	var recip [20]byte
	recip[19] = recipientLastByte

	return EvmHTLC{
		Index:     index,
		Amount:    big.NewInt(amount),
		Hashlock:  Keccak256([]byte(preimageTag)),
		Timelock:  timelock,
		Recipient: recip,
	}
}

// merkleTestHTLCs reproduces the three HTLCs from HtlcMerkleVectors.t.sol.
func merkleTestHTLCs() []EvmHTLC {
	return []EvmHTLC{
		htlc(1, 10_000_000, "p1", 1000, 0xAA),
		htlc(2, 20_000_000, "p2", 2000, 0xBB),
		htlc(3, 30_000_000, "p3", 3000, 0xAA),
	}
}

// Golden vectors emitted by HtlcMerkleVectors.t.sol (proof verified on-chain by
// OZ MerkleProof.verify).
const (
	goldenMerkleRoot  = "73754267b833344c17a4eb22240a2aea424fe93e8d47e5e69f270505b6d4c1ae"
	goldenLeaf2       = "87c91b41a3df676369962d1116918a38aef65bf5f7546059f751f1968ad0ee63"
	goldenProof0Leaf1 = "bdd55643dbc84a87078376663dfd679a9e8f28ff8c8eddd603b335525d981da5"
	goldenProof1Leaf3 = "831b2b768d7a3050ee742cdee56526437f3158aeb6ce18772ec00d01aa884e10"
)

func TestHtlcMerkleRootMatchesContract(t *testing.T) {
	t.Parallel()

	got := HtlcsMerkleRoot(merkleTestHTLCs())
	want := hexToBytes32(t, goldenMerkleRoot)
	if got != want {
		t.Fatalf("htlcsHash root mismatch\n got %x\nwant %x", got, want)
	}
}

func TestHtlcLeafMatchesContract(t *testing.T) {
	t.Parallel()

	leaf := htlc(2, 20_000_000, "p2", 2000, 0xBB).Leaf()
	want := hexToBytes32(t, goldenLeaf2)
	if leaf != want {
		t.Fatalf("leaf mismatch\n got %x\nwant %x", leaf, want)
	}
}

func TestHtlcMerkleProofMatchesContract(t *testing.T) {
	t.Parallel()

	htlcs := merkleTestHTLCs()
	proof, ok := HtlcMerkleProof(htlcs, 2)
	if !ok {
		t.Fatal("proof not found for index 2")
	}
	if len(proof) != 2 {
		t.Fatalf("proof length = %d, want 2", len(proof))
	}
	if proof[0] != hexToBytes32(t, goldenProof0Leaf1) {
		t.Fatalf("proof[0] mismatch: %x", proof[0])
	}
	if proof[1] != hexToBytes32(t, goldenProof1Leaf3) {
		t.Fatalf("proof[1] mismatch: %x", proof[1])
	}

	// Self-consistency: the proof must fold back into the root, mirroring
	// the contract's MerkleProof.verify.
	leaf := htlc(2, 20_000_000, "p2", 2000, 0xBB).Leaf()
	root := HtlcsMerkleRoot(htlcs)
	if !VerifyHtlcMerkleProof(proof, root, leaf) {
		t.Fatal("Go proof does not verify against Go root")
	}
}

func TestHtlcMerkleEmptyAndSingle(t *testing.T) {
	t.Parallel()

	// Empty set -> zero root, matching the contract.
	if got := HtlcsMerkleRoot(nil); got != ([32]byte{}) {
		t.Fatalf("empty root = %x, want zero", got)
	}

	// Single HTLC -> root == leaf, empty proof (contract verifies with an
	// empty proof in that case).
	single := []EvmHTLC{htlc(7, 5_000_000, "solo", 500, 0xAA)}
	root := HtlcsMerkleRoot(single)
	if root != single[0].Leaf() {
		t.Fatal("single-HTLC root must equal its leaf")
	}
	proof, ok := HtlcMerkleProof(single, 7)
	if !ok || len(proof) != 0 {
		t.Fatalf("single-HTLC proof must be empty, got %d (ok=%v)",
			len(proof), ok)
	}
}
