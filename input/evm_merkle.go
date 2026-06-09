package input

import (
	"bytes"
	"sort"
)

// This file builds the HTLC Merkle tree committed in a StateUpdate's htlcsHash
// and proven on-chain by ChannelManager.claimHtlc / timeoutHtlc via
// OpenZeppelin's MerkleProof.verify.
//
// To agree with OZ MerkleProof, internal nodes use COMMUTATIVE hashing: each
// parent is keccak256 of its two children sorted ascending, so sibling order is
// irrelevant. Leaves are keccak256(abi.encode(HTLC)) (EvmHTLC.Leaf), ordered by
// HTLC index ascending (spec §2.3) — that ordering fixes the tree SHAPE (which
// leaves pair together), which is all that must match between Go and Solidity.
// An odd node at any level is promoted unchanged to the next level.
//
// Empty HTLC set ⇒ htlcsHash = bytes32(0), matching the contract.

// commutativeHash returns keccak256 of a and b sorted ascending — OZ's
// _hashPair, so node hashing is order-independent.
func commutativeHash(a, b [32]byte) [32]byte {
	if bytes.Compare(a[:], b[:]) <= 0 {
		return Keccak256(a[:], b[:])
	}

	return Keccak256(b[:], a[:])
}

// sortedLeaves returns the HTLC Merkle leaves ordered by HTLC index ascending.
func sortedLeaves(htlcs []EvmHTLC) [][32]byte {
	ordered := make([]EvmHTLC, len(htlcs))
	copy(ordered, htlcs)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Index < ordered[j].Index
	})

	leaves := make([][32]byte, len(ordered))
	for i, h := range ordered {
		leaves[i] = h.Leaf()
	}

	return leaves
}

// HtlcsMerkleRoot computes the htlcsHash committed in a StateUpdate. The empty
// set yields the zero hash, exactly as the contract treats it.
func HtlcsMerkleRoot(htlcs []EvmHTLC) [32]byte {
	leaves := sortedLeaves(htlcs)
	if len(leaves) == 0 {
		return [32]byte{}
	}

	level := leaves
	for len(level) > 1 {
		level = nextLevel(level)
	}

	return level[0]
}

// nextLevel collapses one tree level into its parents, promoting an odd tail.
func nextLevel(level [][32]byte) [][32]byte {
	next := make([][32]byte, 0, (len(level)+1)/2)
	for i := 0; i < len(level); i += 2 {
		if i+1 == len(level) {
			next = append(next, level[i]) // promote odd tail
			continue
		}
		next = append(next, commutativeHash(level[i], level[i+1]))
	}

	return next
}

// HtlcMerkleProof returns the Merkle proof for the HTLC with the given index,
// suitable for ChannelManager.claimHtlc / timeoutHtlc. The bool is false if no
// HTLC with that index is present. A single-HTLC tree yields an empty proof
// (root == leaf), matching the contract.
func HtlcMerkleProof(htlcs []EvmHTLC, index uint64) ([][32]byte, bool) {
	ordered := make([]EvmHTLC, len(htlcs))
	copy(ordered, htlcs)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Index < ordered[j].Index
	})

	pos := -1
	for i, h := range ordered {
		if h.Index == index {
			pos = i
			break
		}
	}
	if pos < 0 {
		return nil, false
	}

	leaves := make([][32]byte, len(ordered))
	for i, h := range ordered {
		leaves[i] = h.Leaf()
	}

	var proof [][32]byte
	level := leaves
	idx := pos
	for len(level) > 1 {
		// Append the sibling of idx, if it has one (an odd-tail node is
		// promoted with no sibling at this level).
		if idx%2 == 0 {
			if idx+1 < len(level) {
				proof = append(proof, level[idx+1])
			}
		} else {
			proof = append(proof, level[idx-1])
		}

		level = nextLevel(level)
		idx /= 2
	}

	return proof, true
}

// VerifyHtlcMerkleProof reproduces OZ MerkleProof.verify in Go: it folds the
// proof into the leaf using commutative hashing and checks it equals root. Used
// in tests to confirm self-consistency with the on-chain verifier.
func VerifyHtlcMerkleProof(proof [][32]byte, root, leaf [32]byte) bool {
	computed := leaf
	for _, p := range proof {
		computed = commutativeHash(computed, p)
	}

	return computed == root
}
