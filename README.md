# Loka AI Agentic Payment P2P Lightning Node

[![Website](https://img.shields.io/badge/website-lokachain.org-blue.svg)](https://lokachain.org/)
[![Twitter](https://img.shields.io/badge/twitter-@lokachain-1DA1F2.svg)](https://x.com/lokachain)
[![Architecture](https://img.shields.io/badge/architecture-P2P%20Agentic-orange.svg)](https://github.com/loka-network/loka-p2p-lnd)
[![Status](https://img.shields.io/badge/status-Active-success.svg)](https://github.com/loka-network)
[![MIT licensed](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/lightningnetwork/lnd/blob/master/LICENSE)

**Loka AI Agentic Payment P2P Lightning Node** represents a new iteration and ambitious evolution built natively on top of the established Lightning Network infrastructure. By pushing the boundaries of traditional payment channels, we are building a superior, high-throughput P2P Payment Value Network.

While preserving the full functionality and battle-tested architecture of the Bitcoin Lightning Network, we have fundamentally expanded its capabilities by integrating **Sui** (a DAG-BFT high-performance distributed ledger) and simultaneously laying the architectural groundwork for the upcoming zero-intrusion integration of **Setu** (the Hetu Project's dedicated payment consensus network).

---

## Project Overview

Built upon the robust routing engine and HTLC state machine of the traditional Lightning Network daemon (`lnd`), this project introduces a unified, multi-chain P2P payment infrastructure:

- **Bitcoin path**: Fully preserved, connected via traditional `btcd` / `bitcoind` / `neutrino` backends
- **Sui path**: Connected via newly engineered adapter modules (`suinotify/`, `suiwallet/`, etc.), selected seamlessly with `--chain=sui`
- **Setu path (Upcoming)**: Extending the adapter pattern to natively support Setu, providing a dedicated scaling layer for Lightning transactions

All supported chains share the same powerful RPC interface, pathfinding engine, HTLC Switch, and channel state machine — **one unified codebase, seamlessly routing across multiple ledger backends**.

---

## Advantages & Features: The P2P Payment Value Network

Integrating LND with Sui creates a truly scalable, high-throughput P2P payment value network that is capable of supporting global micro-transactions with optimal efficiency:

- **Instant Finality & High Throughput:** Sui's DAG-BFT consensus provides sub-second confirmation latency, ensuring that on-chain operations—like channel openings, closures, and dispute resolutions—are settled incredibly fast.
- **Battle-Tested P2P Routing:** We preserve the complete, extensively tested Lightning Network (BOLT) logic. The P2P network, routing engine, and HTLC state machine remain fully intact.
- **Smart Contract (Move) Enforced Lightning:** Channel primitives (open, close, HTLCs, penalties) are handled elegantly by native Sui Move smart contracts, avoiding the limitations of traditional Bitcoin scripting.

---

## How LND Was Modified (Implementation)

This project modifies LND using a **Zero-Intrusion Adapter Pattern**. Rather than altering the core Lightning Network state machine, we abstracted the native Bitcoin consensus, wallet, and cryptographic interactions. This allows high-performance networks like **Sui**—and soon, **Setu**—to be dynamically plugged in beneath LND's application layer.

1. **Chain Abstraction & Adapter Layer**: We implemented new backends satisfying the `ChainNotifier`, `WalletController`, and `Signer` interfaces specifically for Sui (and architecturally prepared for Setu). 
2. **Sui Adapters & RPC**: We introduced the `suinotify` package for event tracking via Sui RPC, `suiwallet` for key and address management, and `sui_estimator` for network fees.
3. **Smart Contract Primitives**: Instead of relying on Bitcoin HTLC and Commitment scripts, we route operations through `suiwallet` which constructs `BuildMoveCall` requests to execute natively deployed Move Smart Contracts (handling events like `ChannelOpen`, `ChannelClose`, `HTLCClaim`, etc.).
4. **Transparent Type Mapping**: We seamlessly map internal Bitcoin wire types to Sui constructs. For example, a 32-byte Sui `ObjectID` seamlessly maps to LND's `wire.OutPoint.Hash`, avoiding massive refactoring of LND's core logic.
5. **Cryptographic Compatibility**: Extended Go's SECP256K1 logic to sign a deterministic `SHA256(Blake2B(intent))` payload, effectively matching the official Mysten Sui TS SDK and ensuring 100% compatibility with Sui Devnet validation requirements.

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
