// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test, console} from "forge-std/Test.sol";
import {ChannelManager} from "../src/ChannelManager.sol";

/// @notice Emits a golden vector for the EIP-712 OpenChannel consent digest the
/// counterparty signs on a dual-funded open (audit M-3). The Go side
/// (input.EvmOpenChannel.Digest) must reproduce this byte-for-byte so the
/// signature it produces is accepted by ChannelManager.openChannel.
///
/// Domain is identical to Eip712Vectors (chainId 31337, ChannelManager deployed
/// first so it lands at the same deterministic address), so the domainSeparator
/// matches the pinned goldenDomainSep in input/evm_signer_test.go.
contract OpenChannelVectorsTest is Test {
    bytes32 private constant OPEN_CHANNEL_TYPEHASH = keccak256(
        "OpenChannel(bytes32 salt,address participantA,address participantB,uint256 localFundingAmount,uint256 remoteFundingAmount)"
    );

    function test_EmitOpenChannelVector() public {
        vm.chainId(31337);
        ChannelManager mgr =
            new ChannelManager(address(uint160(1)), 86_400, 0, 0);

        // Fixed inputs (same parties as the HTLC vector for familiarity).
        bytes32 salt = keccak256("loka-evm-open-salt");
        address participantA = address(uint160(0xA11CE));
        address participantB = address(uint160(0xB0B));
        uint256 localFundingAmount = 600_000_000; // 600 USDC
        uint256 remoteFundingAmount = 400_000_000; // 400 USDC

        bytes32 structHash = keccak256(
            abi.encode(
                OPEN_CHANNEL_TYPEHASH,
                salt,
                participantA,
                participantB,
                localFundingAmount,
                remoteFundingAmount
            )
        );
        bytes32 digest = keccak256(
            abi.encodePacked("\x19\x01", mgr.domainSeparator(), structHash)
        );

        console.logBytes32(salt);
        console.log("^ salt = keccak256('loka-evm-open-salt')");
        console.logBytes32(digest);
        console.log("^ OpenChannel digest (A=0xA11CE,B=0xB0B,600e6/400e6)");
    }
}
