// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test, console} from "forge-std/Test.sol";
import {ChannelManager} from "../src/ChannelManager.sol";
import {IChannelManager} from "../src/IChannelManager.sol";

/// @notice Emits a golden vector for the FULL off-chain commitment artifact: a
/// StateUpdate whose htlcsHash is a real (non-zero) HTLC Merkle root rather than
/// bytes32(0). Eip712Vectors covers the empty-HTLC case; this proves the Go side
/// (input.HtlcsMerkleRoot -> EvmStateUpdate.Digest) agrees with the contract for
/// the combined Merkle-root -> typed-data-digest path the channel hook signs.
///
/// Domain is identical to Eip712Vectors (chainId 31337, ChannelManager deployed
/// first in the test so it lands at the same deterministic address), so the
/// domainSeparator matches the pinned goldenDomainSep in input/evm_signer_test.go.
contract StateUpdateHtlcsVectorsTest is Test {
    bytes32 private constant STATE_UPDATE_TYPEHASH = keccak256(
        "StateUpdate(bytes32 channelId,uint256 nonce,uint256 balanceA,uint256 balanceB,bytes32 htlcsHash)"
    );

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

    function test_EmitStateUpdateWithHtlcsVector() public {
        vm.chainId(31337);
        ChannelManager mgr =
            new ChannelManager(address(uint160(1)), 86_400, 0, 0);

        // Two HTLCs (even tree: root = commit(l1, l2)). Recipients are the two
        // channel parties — exactly what the channel bridge fills in per HTLC
        // direction.
        IChannelManager.HTLC memory h1 = IChannelManager.HTLC({
            index: 4,
            amount: 25_000_000,
            hashlock: keccak256("preimage-A"),
            timelock: 1_700_000_000,
            recipient: address(uint160(0xA11CE))
        });
        IChannelManager.HTLC memory h2 = IChannelManager.HTLC({
            index: 7,
            amount: 15_000_000,
            hashlock: keccak256("preimage-B"),
            timelock: 1_700_000_500,
            recipient: address(uint160(0xB0B))
        });

        // Leaves ordered by index ascending (4 then 7), commutative parent.
        bytes32 htlcsHash = _commit(_leaf(h1), _leaf(h2));

        bytes32 channelId = keccak256("loka-evm-test-channel");
        uint256 nonce = 5;
        uint256 balanceA = 600_000_000;
        uint256 balanceB = 400_000_000;

        bytes32 structHash = keccak256(
            abi.encode(
                STATE_UPDATE_TYPEHASH,
                channelId,
                nonce,
                balanceA,
                balanceB,
                htlcsHash
            )
        );
        bytes32 digest = keccak256(
            abi.encodePacked("\x19\x01", mgr.domainSeparator(), structHash)
        );

        console.logBytes32(htlcsHash);
        console.log("^ htlcsHash (2 htlcs, idx 4/7)");
        console.logBytes32(_leaf(h1));
        console.log("^ leaf htlc index 4");
        console.logBytes32(_leaf(h2));
        console.log("^ leaf htlc index 7");
        console.logBytes32(digest);
        console.log("^ StateUpdate digest (nonce=5, 600e6/400e6, htlcsHash!=0)");
    }
}
