# ChannelManager — Loka Lightning EVM Settlement Contract

The on-chain settlement surface for the [Loka fork of LND](../../README.md) when
it runs against an **EVM-compatible chain** (Base, Taiko/Tempo, Arbitrum, …) and
an **ERC20 asset** (USDC, USDT). One `ChannelManager` is deployed per
`(chain, asset)` sub-network and holds every channel's escrow in a
`mapping(bytes32 => Channel)` keyed by `channelId`.

It mirrors the Sui `lightning` Move module: the off-chain Lightning protocol
(`StateNum`, HTLC add/settle/fail, revocation window) lives **unchanged** in
LND's `lnwallet/channel.go`; only the *signed artifact* (an EIP-712
`StateUpdate`) and the *settlement calls* differ.

- Interface & data model — [`src/IChannelManager.sol`](src/IChannelManager.sol)
- Design / spec — [`../../1-refactor-docs/evm/`](../../1-refactor-docs/evm/)
  (`lnd-evm-refactor-plan.md` §3–§4, `evm-ln-interaction-spec.md` §1–§5)

## Layout

```
channel-manager/
├── foundry.toml              # solc 0.8.28, cancun, optimizer runs=200
├── src/
│   ├── IChannelManager.sol   # locked external interface (the LND adapter targets this)
│   └── ChannelManager.sol    # escrow implementation
├── script/
│   └── Deploy.s.sol          # CREATE2 deterministic deployment
├── test/
│   ├── ChannelManager.t.sol  # Forge suite (open/close/force-close/penalty/HTLC)
│   └── mocks/MockERC20.sol   # mintable test token
└── lib/                      # dependencies — git-ignored, installed locally (see Setup)
```

## Prerequisites

[Foundry](https://book.getfoundry.sh/getting-started/installation) (the repo was
built with `forge 1.4.3`):

```sh
curl -L https://foundry.paradigm.xyz | bash
foundryup
```

## Setup

`lib/` is **git-ignored** — dependencies are kept local and not pushed to the
remote. After a fresh checkout, reinstall them:

```sh
cd evm-contracts/channel-manager
forge install OpenZeppelin/openzeppelin-contracts@v5.1.0
forge install foundry-rs/forge-std
```

The remappings in `foundry.toml` expect them at `lib/openzeppelin-contracts/`
and `lib/forge-std/`:

```toml
remappings = [
    "@openzeppelin/=lib/openzeppelin-contracts/",
    "forge-std/=lib/forge-std/src/",
]
```

> `forge install` may try to register the libs as git submodules of the parent
> LND repo. Since we do **not** want them committed, add `--no-git` (or just
> leave them ignored — the `.gitignore` already excludes `lib/`).

## Develop

```sh
forge build          # compile src + script + test
forge fmt            # format Solidity sources
forge build --sizes  # report deployed bytecode sizes
forge inspect ChannelManager abi   # dump the ABI (feed into the Go evm adapter)
```

> Running from inside the LND repo: prefix with `GOWORK=off` if a stray Go
> workspace interferes with shell tooling — it does not affect `forge` itself.

## Test

```sh
forge test                 # run the full suite
forge test -vvv            # verbose traces on failure
forge test --match-test test_Penalize_SweepsToVictim   # one test
forge test --match-contract ChannelManagerTest
forge test --gas-report    # per-function gas
forge coverage             # line/branch coverage
```

The suite covers the five scenarios from
[`testing-verification.md`](../../1-refactor-docs/evm/testing-verification.md)
§1.2: open, cooperative close, unilateral close + challenge window, breach
penalty, and HTLC claim/timeout.

## Deploy

[`script/Deploy.s.sol`](script/Deploy.s.sol) deploys deterministically via
`CREATE2` (refactor-plan §4). Configure it with environment variables:

| Var                | Meaning                                   | Default      |
| ------------------ | ----------------------------------------- | ------------ |
| `PRIVATE_KEY`      | deployer key (uint256)                    | — (required) |
| `TOKEN_ADDRESS`    | ERC20 asset escrowed by this sub-network  | — (required) |
| `CHALLENGE_PERIOD` | force-close challenge window, seconds     | `86400`      |
| `SALT`             | CREATE2 salt                              | `1337`       |

### Local (Anvil)

```sh
anvil &                                    # local node on :8545
export RPC=http://127.0.0.1:8545
export PRIVATE_KEY=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80  # anvil acct 0

# 1. Deploy a mintable mock USDC (local testing only) and grab its address.
forge script script/DeployMockToken.s.sol --rpc-url "$RPC" --broadcast
export TOKEN_ADDRESS=<address printed above>

# 2. Deploy the ChannelManager against that token.
forge script script/Deploy.s.sol --rpc-url "$RPC" --broadcast
```

`DeployMockToken.s.sol` mints `1,000,000 USDC` to the deployer; pass
`MINT_TO=0x...` to also fund a counterparty, or top up any account later since
`mint` is public:

```sh
cast send "$TOKEN_ADDRESS" "mint(address,uint256)" <addr> 1000000000 \
    --rpc-url "$RPC" --private-key "$PRIVATE_KEY"
```

> The mock is for **local testing only**. On a real chain set `TOKEN_ADDRESS` to
> the canonical USDC/USDT address instead and skip step 1.

### A real chain

```sh
export PRIVATE_KEY=0x...           # keep out of shell history; prefer a keystore
export TOKEN_ADDRESS=0x...         # USDC on the target chain
forge script script/Deploy.s.sol \
    --rpc-url "$BASE_RPC" \
    --broadcast \
    --verify --etherscan-api-key "$ETHERSCAN_API_KEY"
```

> **Deterministic addresses.** CREATE2 yields the same address across chains
> only when the *initcode* matches — i.e. same bytecode **and** same constructor
> args (`token`, `challengePeriod`). Because `token` differs per chain, addresses
> match across chains only for an identical `token`+`challengePeriod` pair. Set
> `SALT` and the constructor env vars identically wherever you want a matching
> address.

After deploy, wire the printed address into the LND node config:

```sh
./lnd-debug --chain=evm --evm.active \
    --evm.chainid=8453 --evm.rpchost="$BASE_RPC" \
    --evm.tokenaddress="$TOKEN_ADDRESS" \
    --evm.contractaddress="$MANAGER_ADDRESS"
```

## Upgrade

`ChannelManager` is **non-upgradeable by design** — no proxy, no admin. The
`token` and `challengePeriod` are `immutable`, and channel funds are only ever
movable by the participants' own signatures. This is deliberate: an upgradeable
escrow would let an admin key rewrite settlement logic over locked user funds,
defeating the trust-minimization the Lightning model depends on.

"Upgrading" therefore means **deploy a new version and migrate**, not patch in
place:

1. **Bump the version.** Change the EIP-712 domain version so signatures cannot
   cross contract versions:
   ```solidity
   EIP712("LokaChannelManager", "2")
   ```
2. **Deploy a new instance** with a fresh `SALT` (CREATE2 reverts on an
   already-used salt+initcode):
   ```sh
   SALT=$(cast keccak "loka.channel-manager.v2") \
   forge script script/Deploy.s.sol --rpc-url "$RPC" --broadcast --verify
   ```
3. **Drain the old contract.** No new channels should open against it. Existing
   channels settle out normally — `closeChannel` / `forceClose` →
   `distributeFunds` — until the old contract holds zero balance.
4. **Point LND at the new address** (`--evm.contractaddress=…`) once it is live;
   open all new channels there.

Because each `(chain, asset)` runs an independent sub-network daemon, migrations
are per-sub-network and can be rolled out one at a time. Verify the old contract
is fully drained before retiring its config:

```sh
cast call "$TOKEN_ADDRESS" "balanceOf(address)(uint256)" "$OLD_MANAGER" --rpc-url "$RPC"
```
