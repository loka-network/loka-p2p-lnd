// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Script, console} from "forge-std/Script.sol";
import {ChannelManager} from "../src/ChannelManager.sol";

/// @title DeployChannelManager
/// @notice Deterministic deployment of `ChannelManager` via CREATE2
/// (refactor-plan §4). Foundry routes `new C{salt: ...}()` through the
/// canonical CREATE2 factory, so the deployed address is
/// `keccak256(0xff ++ factory ++ salt ++ keccak256(initcode))`.
///
/// The address is identical across chains only when the initcode matches —
/// i.e. same bytecode AND same constructor args. Because `token` differs per
/// (chain, asset) sub-network, addresses match across chains only for the same
/// token+challengePeriod pair. Set `SALT` and constructor env vars identically
/// where you want matching addresses.
///
/// Env vars:
///   - PRIVATE_KEY       deployer key (uint256)
///   - TOKEN_ADDRESS     ERC20 asset escrowed by this sub-network
///   - CHALLENGE_PERIOD  force-close challenge window, seconds (default 86400)
///   - SALT              CREATE2 salt (default 1337)
///
/// Usage:
///   forge script script/Deploy.s.sol --rpc-url $BASE_RPC --broadcast
contract DeployChannelManager is Script {
    function run() external returns (ChannelManager manager) {
        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        address tokenAddress = vm.envAddress("TOKEN_ADDRESS");
        uint256 challengePeriod =
            vm.envOr("CHALLENGE_PERIOD", uint256(86_400));
        bytes32 salt = bytes32(vm.envOr("SALT", uint256(1337)));

        vm.startBroadcast(deployerPrivateKey);
        manager = new ChannelManager{salt: salt}(tokenAddress, challengePeriod);
        vm.stopBroadcast();

        console.log("Deployed ChannelManager to:", address(manager));
        console.log("  token:", tokenAddress);
        console.log("  challengePeriod:", challengePeriod);
    }
}
