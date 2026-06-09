// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Script, console} from "forge-std/Script.sol";
import {MockERC20} from "../test/mocks/MockERC20.sol";

/// @title DeployMockToken
/// @notice Deploy a mintable mock USDC for LOCAL testing only — gives you a
/// concrete `TOKEN_ADDRESS` to feed into `Deploy.s.sol`. The token has a public
/// `mint`, so any account can top itself up afterwards. DO NOT deploy on a real
/// network; on Base/Arbitrum/etc. use the canonical USDC address instead.
///
/// Env vars:
///   - PRIVATE_KEY   deployer key (uint256)
///   - TOKEN_NAME    ERC20 name     (default "Mock USD Coin")
///   - TOKEN_SYMBOL  ERC20 symbol   (default "USDC")
///   - TOKEN_DECIMALS decimals      (default 6, matching USDC)
///   - MINT_AMOUNT   minted to the deployer, base-units (default 1_000_000e6)
///   - MINT_TO       optional second recipient minted the same amount
///
/// Usage (local Anvil):
///   anvil &
///   export PRIVATE_KEY=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
///   forge script script/DeployMockToken.s.sol --rpc-url http://127.0.0.1:8545 --broadcast
contract DeployMockToken is Script {
    function run() external returns (MockERC20 mock) {
        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        address deployer = vm.addr(deployerPrivateKey);

        string memory name = vm.envOr("TOKEN_NAME", string("Mock USD Coin"));
        string memory symbol = vm.envOr("TOKEN_SYMBOL", string("USDC"));
        uint8 decimals = uint8(vm.envOr("TOKEN_DECIMALS", uint256(6)));
        uint256 mintAmount =
            vm.envOr("MINT_AMOUNT", uint256(1_000_000) * (10 ** decimals));
        address mintTo = vm.envOr("MINT_TO", address(0));

        vm.startBroadcast(deployerPrivateKey);

        mock = new MockERC20(name, symbol, decimals);
        mock.mint(deployer, mintAmount);
        if (mintTo != address(0) && mintTo != deployer) {
            mock.mint(mintTo, mintAmount);
        }

        vm.stopBroadcast();

        console.log("Deployed MockERC20 (%s) to:", symbol, address(mock));
        console.log("  decimals:", decimals);
        console.log("  minted to deployer:", deployer, mintAmount);
        if (mintTo != address(0) && mintTo != deployer) {
            console.log("  minted to:", mintTo, mintAmount);
        }
        console.log("Set this as TOKEN_ADDRESS for Deploy.s.sol.");
    }
}
