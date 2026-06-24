# Bootstrapping the Loka EVM-Lightning Network

Bootstrapping a brand new, isolated Loka EVM-Lightning network graph is like lighting the first fires in a dark forest. Without any pre-existing interconnected routing nodes, we must establish a highly available, discoverable **Seed Node (Hub)** architecture.

This guide details how to plan, bootstrap, and scale a completely new Agentic P2P payment network on top of an EVM chain (Base, an OP-stack L2, or a local Anvil devnet) where settlement is anchored by the `ChannelManager` escrow contract.

> **What differs from the Sui bootstrap.** The topology, DNS, and Gossip mechanics are identical to `1-refactor-docs/sui/sui-lightning-network-bootstrap.md` and are reproduced here for completeness. The EVM-specific concerns are: (1) **two balances per node** — native gas (ETH) *and* the ERC-20 channel asset (USDC) — which both must be funded; (2) a **deployed `ChannelManager` contract** per (chain, asset) sub-network that every node must point at; (3) an **ERC-20 approval** before opening; and (4) an optional **watchtower** to defend offline nodes against revoked-state force-closes. Sections 0, 2.5, 4, and 7 below cover these.

---

## 0. Per-Sub-Network Prerequisite: Deploy the `ChannelManager`

A Loka EVM sub-network is defined by a `(chain, ERC-20 asset)` pair and a single `ChannelManager` deployment that escrows that asset. **Every node on the sub-network must be launched against the same contract and token address** — they are the on-chain settlement root, the analogue of the Sui package id.

Deploy once (or reuse an existing deployment), recording the result in `evm-contracts/channel-manager/deploy_state_<network>.json`:

```bash
# Deploys MockERC20 (omit on mainnet; pass a real token instead) + ChannelManager,
# and writes deploy_state_<network>.json {token, channel_manager, challenge_period, deploy_block}.
PRIVATE_KEY=0x<deployer-key> CHALLENGE_PERIOD=86400 \
  evm-contracts/channel-manager/deploy.sh base-sepolia https://sepolia.base.org
```

- `CHALLENGE_PERIOD` is the force-close challenge window **in seconds**, fixed at deploy time. The default is `86400` (24 h) — the correct production value. The integration tests override it to 12 s / 60 s purely so the suite finishes in minutes; **never deploy a short challenge period to mainnet** (it shrinks the breach-remedy window — see `security-audit.md` H-1/M-1).
- **Optional deposit-scaling** (mirrors Bitcoin's value-scaled CSV delay): set `MAX_CHALLENGE_PERIOD` (the cap) and `FULL_SCALE_DEPOSIT` (the deposit at which the window reaches the cap, in token base units) to make the per-channel window scale linearly between `CHALLENGE_PERIOD` (the floor) and the cap. Both default to `0`, which disables scaling → a fixed `CHALLENGE_PERIOD` for every channel. The contract exposes `challengeWindowFor(deposit)` so operators can preview the window a given deposit will get, and `forceClose` stamps `challengeExpiry = block.timestamp + challengeWindowFor(totalDeposited)`. Rationale: a larger escrow is worth more to steal, so it deserves a longer window for the victim/watchtower to react.
- On a public testnet the deployer key must hold native gas. The deployer address is **not** privileged by the contract afterwards — `ChannelManager` is permissionless, so seed operators and agents need not be the deployer.

Distribute `{token, channel_manager}` to every operator; they become `--evm.tokenaddress` and `--evm.contractaddress`.

---

## 1. Network Topology: The Hub & Spoke Model

In a newly launched network, all AI Agents default to being isolated "islands". To solve this, we adopt a classic hybrid Star/Mesh topology:

1. **Seed Nodes (Core Hubs / Backbone)**: high-throughput routing nodes operated by the core team or institutional liquidity providers. They have static public IPs and domain names and are directly connected to each other (Full Mesh) to form an indestructible routing backbone.
2. **Agent Nodes (Edge Spokes)**: thousands of AI agents, mobile clients, and terminal devices. They need no static IP. On boot they connect to any public Seed Node and instantly merge into the global routing graph.

> **Best Practice**: deploy **3 to 5 Seed Nodes** globally (e.g. US East, Europe, Asia) for high availability without fragmenting initial liquidity.

---

## 2. Bootstrapping the First Seed Nodes

The golden rule for a seed node: **it must explicitly declare its public IP and bind external ports so the outside world can discover it.**

### Example Startup Configuration

Assuming the first server is in US East (IP `198.51.100.1`), settling USDC on Base Sepolia:

```bash
nohup lnd --evm.active \
    --evm.chain=base-sepolia \
    --evm.chainid=84532 \
    --evm.rpchost=https://sepolia.base.org \
    --evm.tokenaddress=0x<USDC_or_MockERC20_address> \
    --evm.contractaddress=0x<ChannelManager_address> \
    --evm.numconfs=3 \
    --listen=0.0.0.0:9735 \
    --rpclisten=127.0.0.1:10009 \
    --restlisten=127.0.0.1:8081 \
    --externalip=198.51.100.1:9735 \
    --alias="Loka-Seed-EastUS" \
    --color="#1DA1F2" \
    --protocol.wumbo-channels \
    --protocol.no-anchors \
    --lnddir=~/.lnd-seed \
    > ~/.lnd-seed/lnd.log 2>&1 &
```

**Key Parameters:**
- `--evm.active`: switches the node off the Bitcoin path and onto the EVM `ChainControl` (zero-intrusion adapter). When false, every other `--evm.*` flag is ignored.
- `--evm.chain` / `--evm.chainid`: the sub-network name and chain id. The chain id is **bound into the EIP-712 domain**, so a signed `StateUpdate` is valid on exactly one chain — this is the cross-chain replay defence.
- `--evm.tokenaddress` / `--evm.contractaddress`: the ERC-20 asset and the `ChannelManager` from §0. **All peers must share the same pair** or they cannot transact.
- `--evm.rpchost`: the JSON-RPC endpoint. The node and watchtower now **bound every `eth_getLogs` query to a sliding window**, so a range-capped public endpoint (e.g. `sepolia.base.org`'s 2000-block cap) works fine — the full itest passes against it. The one remaining hard requirement: the endpoint must serve the **genesis block (block 0)**, which the node reads once at startup to anchor HTLC timelocks. Aggressively pruned endpoints (e.g. some `publicnode` hosts) reject that read and the node won't start. See `security-audit.md` M-1 on the single-RPC trust surface; production seeds should still front several endpoints.
- `--evm.numconfs` (default 3): confirmations before an event/receipt is treated as final, absorbing L2 sequencer reorgs.
- `--listen=0.0.0.0:9735` / `--externalip=<Public_IP>`: bind all interfaces and encode the public IP into Gossip broadcasts. Without `--externalip`, peers hear the seed exists but cannot route TCP to it.
- `--protocol.wumbo-channels`: **critical.** With the Loka scaling factor `1 token = 1e8` internal units, the default Lightning channel cap (~16.7M base units ≈ 0.167 USDC) is uselessly small. Wumbo removes the cap so seeds can hold large USDC liquidity pools.
- `--protocol.no-anchors`: **recommended.** Disables Bitcoin CPFP anchor dust outputs, which are meaningless on an EVM chain (gas is paid out-of-band in the native coin) and otherwise spam the Sweeper subsystem with errors against `evmwallet` addresses.

### 2.5 Initializing the Node Wallet — and its **two** balances

On a fresh install LND halts waiting for a wallet password until you create the wallet.

1. **Create the wallet** (note the EVM macaroon path):
   ```bash
   lncli --lnddir=~/.lnd-seed --rpcserver=127.0.0.1:10009 \
     --macaroonpath=~/.lnd-seed/data/chain/evm/base-sepolia/admin.macaroon create
   ```
   Follow the prompts; back up the 24-word seed. On reboot the node boots locked — run `lncli unlock`.

2. **Generate an EVM address**:
   ```bash
   lncli --lnddir=~/.lnd-seed \
     --macaroonpath=~/.lnd-seed/data/chain/evm/base-sepolia/admin.macaroon newaddress p2wkh
   ```
   The command is `p2wkh` (Bitcoin standard) but the Loka Zero-Intrusion Adapter translates the result into a valid **EVM address (`0x…`)**.

3. **Fund _both_ balances** — this is the EVM-specific gotcha. A Loka EVM node holds two independent balances:
   - **Native gas (ETH)** — pays for every `ChannelManager` call (`openChannel`, `forceClose`, `distributeFunds`, …). Without it the node can observe but never act on-chain; a node that runs out of gas during a challenge window **cannot defend itself**. Check it via `lncli getinfo` (the EVM build surfaces native-gas balance at startup) or on-chain. On a public testnet, top it up from a faucet — the repo ships a loop-claim helper:
     ```bash
     CDP_COOKIE='<browser-cookie>' \
       scripts/evm_faucet_base_sepolia.sh 0x<YOUR_EVM_ADDRESS> 0.05
     ```
     > ⚠️ **Never use a well-known/public private key** (e.g. Anvil's account-0 `0xac09…ff80`) on a public testnet — those addresses are occupied by EIP-7702 sweeper bots that drain any incoming ETH instantly. Generate a fresh key per node.
   - **ERC-20 channel asset (USDC)** — the value actually routed. Acquire it like any token (mint on a Mock deployment, bridge/buy on mainnet). Channel capacity is denominated in this asset; balance is visible via `lncli walletbalance`.

---

## 3. DNS Configuration and Node Discovery

While power users can connect with raw `Pubkey@IP:Port` strings, that is brittle for dynamic scaling. Configure subdomains for the seeds.

### 1. Direct A-Record Mapping
Map subdomains to seed public IPs (Cloudflare, Route53, …):

- `lnd-seed-eu.loka.cash`  ->  `A` -> `84.46.253.204`
- `lnd-seed-us.loka.cash`  ->  `A` -> `161.97.184.38`

This makes the Lightning URI elegant for edge agents:
> `0276bf6dc8…@lnd-seed-eu.loka.cash:9735`

### 2. DNS Round-Robin (Load Balancing)
Create a generic subdomain (`seeds.loka.network`) with multiple A-Records pointing at different seeds. An agent resolving it at startup gets a random healthy seed, giving basic load balancing for free.

---

## 4. Building the Backbone & Liquidity (Large vs. Small Channels), and the Approval Step

Starting 3 independent seeds doesn't automatically route payments between them. As the network architect, you must **manually mesh the backbone nodes and fund large liquidity pools between them.**

### Channel Sizes & the Wumbo Requirement
- **Small Channels (Agent Channels)**: opened by edge agents to a seed; sized for that agent's micro-transactions.
- **Large Channels (Wumbo / Backbone)**: inter-seed channels **must** be large. `--protocol.wumbo-channels` removes the default cap so big `--local_amt` values (denominated in the ERC-20 asset's base units) pass.

### The EVM approval step
`ChannelManager.openChannel` pulls the deposit via ERC-20 `transferFrom`, so the funding key **must approve the ChannelManager** for at least the deposit before opening. LND issues this approval automatically in the open-channel carrier flow.

**Single-funded is the operational path** (the initiator funds the whole channel; the counterparty deposits nothing). The M-3 fix made `openChannel` require the counterparty's EIP-712 `OpenChannel` **consent signature** whenever `remoteFundingAmount > 0` — so a stale allowance can't be swept into a channel the counterparty never agreed to. The Go funding flow does not yet produce that consent signature, so **dual-funded opens currently fail closed at the contract** (single-funded is unaffected); dual-funding is a future enhancement.

> **Security note (see `security-audit.md` M-3):** still approve **exact, just-in-time amounts**, never an unbounded allowance.

LND issues the approval as part of the open-channel carrier flow, but operators driving raw `cast` should approve explicitly:
```bash
cast send 0x<TOKEN> "approve(address,uint256)" 0x<ChannelManager> <amount> \
  --rpc-url <RPC> --private-key 0x<funding-key>
```

### Executing the Backbone Mesh
On `seed1`, connect to `seed2`/`seed3` and open **large** channels (capacity in USDC base units; with 6-decimal USDC, `50000000000` = 50,000 USDC):

```bash
lncli connect <Seed2_Pubkey>@seed-eu.loka.network:9735
lncli openchannel --node_key=<Seed2_Pubkey> --local_amt=50000000000

lncli connect <Seed3_Pubkey>@seed-asia.loka.network:9735
lncli openchannel --node_key=<Seed3_Pubkey> --local_amt=50000000000
```

Once the `ChannelOpened` event confirms on-chain, a high-capacity Iron-Triangle backbone is formed.

---

## 5. Integrating Edge Agents

With the backbone deployed, isolated edge agents join the network.

A third-party AI agent boots its `lnd` (with the same `--evm.*` sub-network flags) and runs a persistent connect:
```bash
lncli connect <Seed1_Pubkey>@seed-us.loka.network:9735
```
Or hardcodes it in `lnd.conf`:
```ini
[Application Options]
addpeer=<Seed1_Pubkey>@seed-us.loka.network:9735
```
On boot the agent syncs the global channel graph via Gossip and can instantly route to any other connected agent worldwide. (Agents still need native gas + an ERC-20 approval before opening their own channel — §2.5, §4.)

---

## 6. Scaling: How to Add a New Seed Node

When the network scales (e.g. 100,000 concurrent agents), Gossip bandwidth on 3 nodes may strain. Scale horizontally without downtime:

1. **Deploy the server**: boot `seed4` with its own `--externalip`, unique pubkey, and the **same** `--evm.contractaddress` / `--evm.tokenaddress` / `--evm.chainid`.
2. **Update DNS**: add the IP to a regional subdomain or the round-robin pool.
3. **Mesh into backbone**: run `lncli connect` + `lncli openchannel` (after approval) to open large channels with existing seeds.
4. **Liquidity propagation**: via Gossip, within ~10 minutes the routing tables of all edge agents update to include the new hub.

---

## 7. Watchtower: defending offline nodes

The EVM breach remedy is `penalize` — present a higher-nonce co-signed `StateUpdate` during the challenge window and the contract sweeps the whole escrow to the victim. The H-1 fix made `penalize` pay the **broadcaster-derived victim regardless of who submits the transaction**, so a third party can defend a victim that is offline. A watchtower is that third party; without one, a node that is offline when a counterparty broadcasts a revoked state cannot punish it.

It has two roles, each just an `lnd` flag (mirroring Bitcoin's `--watchtower.active`/`--wtclient.active`):

**Tower** — a separate, always-on `lnd` (different machine from the protected node, so it stays up when that node is down). It runs its own chain watcher and submits `penalize` on a client's behalf:
```bash
lnd --evm.active --evm.chain=base-sepolia ... \
    --evmwatchtower.active \
    --evmwatchtower.listen=0.0.0.0:9912 \
    --evmwatchtower.fromblock=<ChannelManager deploy_block> \
    --evmwatchtower.allowlistfile=~/.lnd/evm-allowlist.txt
```
- `--evmwatchtower.listen`: accepts brontide (encrypted+authenticated) backup uploads.
- `--evmwatchtower.fromblock`: where the chain scan starts — set it to the contract's `deploy_block` (from `deploy_state`) so the tower catches closes that predate its startup. Unset starts one window back from the tip.
- `--evmwatchtower.scanwindow` (default 1800): blocks per `eth_getLogs` query; keep it under the RPC's range cap.
- **DoS allowlist** — `--evmwatchtower.allowedclient=<hex pubkey>` (repeatable, static) **or** `--evmwatchtower.allowlistfile=<path>` (one hex client pubkey per line, `#` comments). The file is **hot-reloaded**: edit it to add/remove clients and the tower applies the change within seconds — **no restart**. An empty list means accept any client (open/altruistic tower). The tower signs `penalize` with its own key (a gas-only relayer — the contract pays the victim, so a leaked backup can never steal).

**Client** — the protected node periodically snapshots its latest co-signed channel state and ships it to a tower:
```bash
lnd --evm.active ... \
    --evmwtclient.active \
    --evmwtclient.tower=<tower_pubkey>@<tower_host>:9912 \
    --evmwtclient.interval=30s
```
- `--evmwtclient.tower`: the remote tower as `<pubkey>@<host:port>` (the tower node's `lncli getinfo` identity pubkey). Omit it and set `--evmwtclient.backupdir` to write backups to a local directory instead (phase-1 local handover).

The backup carries no spend authority (it only lets the holder call `penalize`, which always pays the victim), so it is shipped plaintext. Validated end to end on Anvil and Base Sepolia (`scripts/itest_evm_watchtower.sh`). Design: `1-refactor-docs/evm/evm-watchtower-design.md`.

---

## 8. Operational Checklist (EVM-specific)

- [ ] `ChannelManager` + token addresses are identical across every node on the sub-network.
- [ ] Challenge period is the production value (≥ 24 h); short test values never reach mainnet.
- [ ] Each node holds **native gas** *and* **ERC-20 asset** — gas is monitored and auto-/manually topped up so a node can always act within a challenge window.
- [ ] RPC endpoint serves genesis + wide `eth_getLogs`; production seeds front multiple endpoints (mitigates the single-RPC trust surface, `security-audit.md` M-1).
- [ ] No public/well-known private keys on any reachable network.
- [ ] Approvals are exact and just-in-time, never unbounded.
- [ ] **Breach defense:** either keep nodes highly available, **or** run a watchtower (§7). The H-1 fix + EVM watchtower let an offline node be defended by a third party; nodes that may go offline with funds at stake (edge agents) should back up to a tower (`--evmwtclient.tower=…`). Towers that accept the public should set an allowlist (`--evmwatchtower.allowlistfile`).

---

## References

- Contract + deploy: `evm-contracts/channel-manager/src/ChannelManager.sol`, `evm-contracts/channel-manager/deploy.sh`
- Config flags: `lncfg/evmnode.go` (node), `lncfg/evmwatchtower.go` + `lncfg/evmwtclient.go` (watchtower)
- E2E reference: `scripts/itest_evm.sh` (Anvil 19/19, Base Sepolia 15/15) and `scripts/itest_evm_watchtower.sh` (Anvil + Base Sepolia breach loop)
- Faucet helper: `scripts/evm_faucet_base_sepolia.sh`
- Security: `1-refactor-docs/evm/security-audit.md` (H-1/H-2 + M-2…M-5 remediated)
- Watchtower design: `1-refactor-docs/evm/evm-watchtower-design.md`
- Companion (Sui): `1-refactor-docs/sui/sui-lightning-network-bootstrap.md`
