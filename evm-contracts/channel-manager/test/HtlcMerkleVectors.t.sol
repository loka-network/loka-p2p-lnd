// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test, console} from "forge-std/Test.sol";
import {MerkleProof} from "@openzeppelin/contracts/utils/cryptography/MerkleProof.sol";
import {IChannelManager} from "../src/ChannelManager.sol";

/// @notice Builds the HTLC Merkle tree the same way the Go side does (leaves
/// ordered by index, commutative pair hashing, odd tail promoted), asserts the
/// contract's verifier (OZ MerkleProof.verify) accepts the resulting proof, and
/// emits the root/leaf/proof as golden vectors for input/evm_merkle_test.go.
///
/// Three HTLCs (odd count) exercise the promote path; the proof targets the
/// middle leaf (index 2).
contract HtlcMerkleVectorsTest is Test {
    function _leaf(IChannelManager.HTLC memory h)
        internal
        pure
        returns (bytes32)
    {
        return keccak256(abi.encode(h));
    }

    function _commit(bytes32 a, bytes32 b) internal pure returns (bytes32) {
        return a <= b
            ? keccak256(abi.encodePacked(a, b))
            : keccak256(abi.encodePacked(b, a));
    }

    function test_EmitHtlcMerkleVectors() public view {
        IChannelManager.HTLC memory h1 = IChannelManager.HTLC({
            index: 1,
            amount: 10_000_000,
            hashlock: keccak256("p1"),
            timelock: 1000,
            recipient: address(uint160(0xAA))
        });
        IChannelManager.HTLC memory h2 = IChannelManager.HTLC({
            index: 2,
            amount: 20_000_000,
            hashlock: keccak256("p2"),
            timelock: 2000,
            recipient: address(uint160(0xBB))
        });
        IChannelManager.HTLC memory h3 = IChannelManager.HTLC({
            index: 3,
            amount: 30_000_000,
            hashlock: keccak256("p3"),
            timelock: 3000,
            recipient: address(uint160(0xAA))
        });

        bytes32 l1 = _leaf(h1);
        bytes32 l2 = _leaf(h2);
        bytes32 l3 = _leaf(h3);

        // level0 [l1,l2,l3] -> level1 [commit(l1,l2), l3] -> root.
        bytes32 p12 = _commit(l1, l2);
        bytes32 root = _commit(p12, l3);

        // Proof for the middle leaf l2: [l1, l3].
        bytes32[] memory proof = new bytes32[](2);
        proof[0] = l1;
        proof[1] = l3;

        // The on-chain verifier must accept it.
        require(
            MerkleProof.verify(proof, root, l2),
            "OZ MerkleProof.verify rejected the proof"
        );

        console.logBytes32(root);
        console.log("^ root (3 htlcs, idx 1/2/3)");
        console.logBytes32(l2);
        console.log("^ leaf for htlc index 2");
        console.logBytes32(proof[0]);
        console.log("^ proof[0] (= leaf 1)");
        console.logBytes32(proof[1]);
        console.log("^ proof[1] (= leaf 3)");
    }
}
