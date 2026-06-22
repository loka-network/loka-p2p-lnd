// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test, console} from "forge-std/Test.sol";
import {ChannelManager} from "../src/ChannelManager.sol";

/// @notice Emits cross-language golden vectors for the EIP-712 schema so the Go
/// `evm_signer` can assert byte-for-byte agreement with the deployed contract.
/// Run with: `forge test --match-contract Eip712Vectors -vv` and copy the logged
/// hex into input/evm_signer_test.go.
///
/// All inputs are FIXED here so the digest is reproducible:
///   chainId           = 31337
///   verifyingContract = the deployed ChannelManager address (logged below)
///   token             = 0x00..01, challengePeriod = 86400
contract Eip712VectorsTest is Test {
    bytes32 private constant STATE_UPDATE_TYPEHASH = keccak256(
        "StateUpdate(bytes32 channelId,uint256 nonce,uint256 balanceA,uint256 balanceB,bytes32 htlcsHash)"
    );
    bytes32 private constant COOP_CLOSE_TYPEHASH = keccak256(
        "CooperativeClose(bytes32 channelId,uint256 nonce,uint256 finalBalanceA,uint256 finalBalanceB)"
    );

    function test_EmitVectors() public {
        vm.chainId(31337);
        ChannelManager mgr =
            new ChannelManager(address(uint160(1)), 86_400);

        // Fixed StateUpdate inputs.
        bytes32 channelId = keccak256("loka-evm-test-channel");
        uint256 nonce = 5;
        uint256 balanceA = 600_000_000; // 600 USDC
        uint256 balanceB = 400_000_000; // 400 USDC
        bytes32 htlcsHash = bytes32(0);

        bytes32 suStructHash = keccak256(
            abi.encode(
                STATE_UPDATE_TYPEHASH,
                channelId,
                nonce,
                balanceA,
                balanceB,
                htlcsHash
            )
        );
        bytes32 domSep = mgr.domainSeparator();
        bytes32 suDigest = keccak256(
            abi.encodePacked("\x19\x01", domSep, suStructHash)
        );

        bytes32 ccStructHash = keccak256(
            abi.encode(
                COOP_CLOSE_TYPEHASH, channelId, nonce, balanceA, balanceB
            )
        );
        bytes32 ccDigest = keccak256(
            abi.encodePacked("\x19\x01", domSep, ccStructHash)
        );

        console.log("chainId: 31337");
        console.log("verifyingContract:", address(mgr));
        console.logBytes32(mgr.domainSeparator());
        console.log("^ domainSeparator");
        console.logBytes32(channelId);
        console.log("^ channelId = keccak256('loka-evm-test-channel')");
        console.logBytes32(suDigest);
        console.log("^ StateUpdate digest (nonce=5, 600e6/400e6, htlcs=0)");
        console.logBytes32(ccDigest);
        console.log("^ CooperativeClose digest (nonce=5, 600e6/400e6)");
    }
}
