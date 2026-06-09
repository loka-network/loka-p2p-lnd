// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test, console} from "forge-std/Test.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";

/// @notice Signs a fixed StateUpdate digest with a known key and emits the
/// canonical (v, r, s) plus the signer address, so the Go RecoverEvmSigV can be
/// pinned to a signature the contract's ECDSA.recover accepts. This proves the Go
/// side re-derives the SAME recovery byte v that forceClose / penalize need,
/// from the 64-byte (r ‖ s) it retains off-chain — the keystone of EVM breach
/// handling (the newer-nonce penalty model, no on-chain revocation key).
///
/// The digest is the pinned StateUpdate digest from Eip712Vectors
/// (nonce=5, 600e6/400e6, htlcsHash=0); the key is Anvil account 0, so the
/// emitted address matches input/evm_signer_test.go's TestEvmAddressFromPubKey.
contract SigRecoveryVectorsTest is Test {
    function test_EmitSigRecoveryVector() public view {
        uint256 pk =
            0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80;
        bytes32 digest =
            0x261e14131a6d9c28cc84579fe345605ff2f942b95fdcbae3bfb793f30e7513c9;

        (uint8 v, bytes32 r, bytes32 s) = vm.sign(pk, digest);

        // The contract recovers the signer from exactly this (digest, sig).
        bytes memory sig = abi.encodePacked(r, s, v);
        require(ECDSA.recover(digest, sig) == vm.addr(pk), "recover mismatch");

        console.logBytes32(digest);
        console.log("^ digest (= goldenStateDig)");
        console.logBytes32(r);
        console.log("^ r");
        console.logBytes32(s);
        console.log("^ s");
        console.log("signer:", vm.addr(pk));
    }
}
