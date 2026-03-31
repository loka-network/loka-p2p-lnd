# Loka AI Agentic Payment P2P Lightning Node

[![Website](https://img.shields.io/badge/website-lokachain.org-blue.svg)](https://lokachain.org/)
[![Twitter](https://img.shields.io/badge/twitter-@lokachain-1DA1F2.svg)](https://x.com/lokachain)
[![Architecture](https://img.shields.io/badge/architecture-P2P%20Agentic-orange.svg)](https://github.com/loka-network/loka-p2p-lnd)
[![Status](https://img.shields.io/badge/status-Active-success.svg)](https://github.com/loka-network)
[![MIT licensed](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/lightningnetwork/lnd/blob/master/LICENSE)

## Vision & Positioning

The next frontier of value transfer is not human-to-human — it is **agent-to-agent**.

As autonomous AI agents begin to coordinate, negotiate, and transact on behalf of individuals and institutions, the underlying payment infrastructure must evolve to match. **Loka AI Agentic Payment P2P Lightning Node** is built for this moment: a high-throughput, multi-chain P2P payment value network designed natively for agentic economies, built on top of the **Bitcoin battle-tested Lightning Network**.

We preserve everything that makes Lightning powerful. We extend it for everything that comes next.

Loka AI Agentic Payment P2P Lightning Node is infrastructure for the **intent economy** — where value flows not just between wallets, but between agents, protocols, and semantic trust networks. It is the payment primitive for a world where AI agents transact autonomously, verifiably, and at scale.

One routing engine. Multiple chains. Infinite agents.

---

## Project Overview

Loka's Lightning Node is not a fork — it is an architectural evolution. Built upon the proven routing engine and HTLC state machine of `lnd`, this project introduces a unified, multi-chain P2P payment infrastructure capable of supporting AI agent micropayments, cross-chain settlement, and programmable trust at global scale.

**Three supported chains share one unified codebase:**

| Path | Backend | Status |
|------|---------|--------|
| **Bitcoin** | `btcd` / `bitcoind` / `neutrino` | ✅ Live |
| **Sui** | Custom adapter modules (`suinotify`, `suiwallet`, `sui_estimator`) | ✅ Live |
| **Setu** | Hetu Project's dedicated payment consensus layer | 🔜 Upcoming |

All chains share the same RPC interface, pathfinding engine, HTLC Switch, and channel state machine. One codebase. Multiple ledger backends. Seamless routing.

---

## Why This Architecture

### The Agentic Payment Problem

Traditional payment rails were designed for humans operating at human speed: seconds, minutes, hours. Autonomous AI agents operate at machine speed — thousands of micro-intents per second, each requiring programmable, verifiable, instant settlement. No existing infrastructure was built for this.

Loka's P2P Lightning Node solves this by combining:
- **Lightning's routing maturity** — battle-tested HTLC logic, BOLT-compliant P2P networking, and proven channel state machines
- **Sui's execution performance** — DAG-BFT consensus delivering sub-second finality, Move smart contract enforceability, and high-throughput on-chain operations
- **Setu's protocol alignment** — a dedicated payment consensus layer designed specifically for the Hetu ecosystem's trust and intent infrastructure

### Core Advantages

**Instant Finality & High Throughput**
Sui's DAG-BFT consensus provides sub-second confirmation latency. Channel openings, closures, and dispute resolutions settle in near-real-time — enabling agentic payment loops that would be impossible on traditional Bitcoin L1.

**Battle-Tested P2P Routing**
The complete Lightning Network BOLT logic is fully preserved. The P2P network topology, onion-routed pathfinding engine, and HTLC state machine remain intact and unmodified — maintaining full compatibility with the broader Lightning ecosystem.

**Move-Enforced Channel Primitives**
Channel operations (open, close, HTLC claim, penalty enforcement) are handled by natively deployed Sui Move smart contracts. This eliminates the scripting limitations of Bitcoin while retaining Lightning's security model — enforceability without trusted intermediaries.

**Programmable for Agents**
The unified RPC interface exposes the full channel and routing layer to AI agents via structured APIs. Agents can open channels, route payments, and settle value autonomously — without human intervention in the payment loop.

---

## Technical Implementation

Loka's implementation follows a **Zero-Intrusion Adapter Pattern**: rather than modifying `lnd`'s core Lightning state machine, we abstract the consensus, wallet, and cryptographic layers beneath it. High-performance chains plug in as backends; the Lightning application layer remains untouched.

### 1. Chain Abstraction & Adapter Layer
New backends implement `lnd`'s `ChainNotifier`, `WalletController`, and `Signer` interfaces for Sui — and are architecturally pre-structured for Setu integration. The adapter boundary is clean: the Lightning layer never needs to know which chain is running beneath it.

### 2. Sui Adapter Modules
- **`suinotify`** — Event tracking and block notification via Sui RPC
- **`suiwallet`** — Key management, address derivation, and transaction construction
- **`sui_estimator`** — Dynamic fee estimation against Sui network conditions

### 3. Move Smart Contract Primitives
All channel lifecycle events — `ChannelOpen`, `ChannelClose`, `HTLCClaim`, penalty enforcement — are routed through `suiwallet`, which constructs `BuildMoveCall` requests to on-chain Move contracts. No Bitcoin HTLC scripts. Full programmability.

### 4. Transparent Type Mapping
Internal Bitcoin wire types map seamlessly to Sui constructs. A 32-byte Sui `ObjectID` maps directly to `wire.OutPoint.Hash` — enabling Sui integration without refactoring LND's core wire protocol or forcing type-system conflicts across the codebase.

### 5. Cryptographic Compatibility
Extended Go's SECP256K1 signing logic to produce a deterministic `SHA256(Blake2B(intent))` payload — matching the Mysten Sui TypeScript SDK specification precisely and ensuring 100% compatibility with Sui Devnet validation requirements.

---

## Architecture: Zero-Intrusion Adapter Pattern

```text
┌─────────────────────────────────────────────────────────────────┐
│              LND Application Layer (unchanged)                  │
│      RPC Server · Routing Engine · HTLC Switch · FSM            │
└──────────────────────────────┬──────────────────────────────────┘
                               │
┌──────────────────────────────▼──────────────────────────────────┐
│          Chain Abstraction Interfaces (never modify)            │
│  ChainNotifier · WalletController · Signer · BlockChainIO       │
└────────┬─────────────────────┬───────────────────────┬──────────┘
         │ chain=bitcoin       │ chain=sui             │ chain=setu
┌────────▼───────────┐  ┌──────▼──────────────┐  ┌─────▼──────────┐
│  Bitcoin Backends  │  │  Sui Adapters (new) │  │ Setu Adapters  │
│  bitcoindnotify/   │  │  suinotify/         │  │ (Upcoming)     │
│  btcdnotify/       │  │  suiwallet/         │  │                │
│  neutrinonotify/   │  │  input/sui_channel  │  │                │
│  lnwallet/btcwallet│  │  chainfee/sui       │  │                │
└────────────────────┘  └─────────────────────┘  └────────────────┘
```

### Type Mapping Conventions

Sui adapters reuse Bitcoin/LND types internally, performing semantic translation at the boundary:

| LND Type              | Sui Semantic          | Notes                    |
| --------------------- | ---------------------- | ------------------------ |
| `wire.OutPoint.Hash`  | `ObjectID`             | Direct 32-byte mapping   |
| `wire.OutPoint.Index` | `0`                    | Sui has no UTXO index   |
| `btcutil.Amount`      | `u64`                  | Sui base unit           |
| `wire.MsgTx`          | Sui Event bytes       | Carries serialized Event |
| `chainhash.Hash`      | `EventId` / `AnchorId` | 32 bytes                 |

---

## Core Features
- Channel open and close (cooperative / force-close / breach penalty)
- Full channel state machine management
- Multi-hop HTLC payment forwarding (including timeout and claim)
- Gossip network topology discovery and maintenance
- Dijkstra pathfinding + Mission Control
- Invoice management (BOLT-11)
- Automated channel management (`autopilot`)
- Watchtower (offline penalty broadcasting)
- Sui chain: DAG finality replaces block confirmations (< 1 second)

---

## Lightning Network Specification Compliance
`lnd` fully implements the following BOLT specifications:

- [x] BOLT 1: Base Protocol
- [x] BOLT 2: Peer Protocol for Channel Management
- [x] BOLT 3: Bitcoin Transaction and Script Formats
- [x] BOLT 4: Onion Routing Protocol
- [x] BOLT 5: Recommendations for On-chain Transaction Handling
- [x] BOLT 7: P2P Node and Channel Discovery
- [x] BOLT 8: Encrypted and Authenticated Transport
- [x] BOLT 9: Assigned Feature Flags
- [x] BOLT 10: DNS Bootstrap and Assisted Node Location
- [x] BOLT 11: Invoice Protocol for Lightning Payments

---

## Setu Integration (Upcoming)

Setu — the Hetu Project's dedicated payment consensus network — extends this architecture further. Where Sui provides high-performance general execution, Setu is purpose-built for intent-carrying payment flows: each transaction can carry semantic metadata, trust attestations, and IFC-compatible value signals natively at the protocol layer.

The adapter pattern is already in place. Setu integration requires no changes to the Lightning core — only a new backend satisfying the same three interfaces. This is the architectural bet: the Lightning routing layer becomes a universal payment router, and the consensus layer becomes swappable.

---



## Build & Run

```sh
# Build debug binaries (lnd-debug, lncli-debug)
make build

# Install to $GOPATH/bin
make install

# Unit tests (requires btcd binary)
make unit

# Unit tests for all submodules (actor/, fn/, etc.)
make unit-module

# Integration tests (builds binaries first; postgres backend requires Docker)
make itest

# Lint (runs golangci-lint via Docker)
make lint
```

> Required Go version: **1.25.5+** (see `GO_VERSION` in Makefile)

Start a node with the Sui backend:

```sh
lnd --chain=sui --sui.rpc=<sui-node-endpoint> ...
```

---

## Key Reference Documents

| Topic                                 | File                                                                                       |
| ------------------------------------- | ------------------------------------------------------------------------------------------ |
| Sui adaptation & integration plan    | [1-refactor-docs/sui/lnd-and-sui-integration.md](1-refactor-docs/sui/lnd-and-sui-integration.md) |
| Sui chain architecture               | [1-refactor-docs/sui/sui-architecture.md](1-refactor-docs/sui/sui-architecture.md)               |
| LND refactor plan                     | [1-refactor-docs/sui/lnd-sui-refactor-plan.md](1-refactor-docs/sui/lnd-sui-refactor-plan.md)     |
| LND engineering architecture overview | [1-refactor-docs/lnd-architecture.md](1-refactor-docs/lnd-architecture.md)                 |
| Sui ↔ LND interaction interface spec | [1-refactor-docs/sui/sui-ln-interaction-spec.md](1-refactor-docs/sui/sui-ln-interaction-spec.md) |

---

## Security

This node is still in **beta**. For mainnet operation, please refer to the [Safe Operating Guide](docs/safety.md).

If you discover a security vulnerability, please issue [a GitHub issue](https://github.com/loka-network/loka-p2p-lnd/issues).

---

## Further Reading

- [Contribution Guide](https://github.com/lightningnetwork/lnd/blob/master/docs/code_contribution_guidelines.md)
- [Docker Deployment Guide](docs/DOCKER.md)
- [Installation Instructions](docs/INSTALL.md)
