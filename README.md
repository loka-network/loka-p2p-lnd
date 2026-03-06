# Loka AI Agentic Payment P2P Lightning Node

[![Website](https://img.shields.io/badge/website-lokachain.org-blue.svg)](https://lokachain.org/)
[![Twitter](https://img.shields.io/badge/twitter-@lokachain-1DA1F2.svg)](https://x.com/lokachain)
[![Architecture](https://img.shields.io/badge/architecture-P2P%20Agentic-orange.svg)](https://github.com/loka-network/loka-p2p-lnd)
[![Status](https://img.shields.io/badge/status-Active-success.svg)](https://github.com/loka-network)
[![MIT licensed](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/lightningnetwork/lnd/blob/master/LICENSE)

**This repository is a fork of [lightningnetwork/lnd](https://github.com/lightningnetwork/lnd)** that preserves full Bitcoin Lightning Network functionality while integrating **Setu** (a DAG-BFT high-performance distributed ledger from the Hetu Project) via a zero-intrusion adapter pattern, delivering a unified dual-chain Lightning Network payment node.

---

## Project Overview

`lnd` (Lightning Network Daemon) is a full Go implementation of a Lightning Network node, supporting channel creation, HTLC forwarding, pathfinding, and the complete feature set. This fork adds native support for the **Setu chain** on top of that foundation:

- **Bitcoin path**: Fully preserved, connected via `btcd` / `bitcoind` / `neutrino` backends
- **Setu path**: Connected via newly added adapter modules (`setunotify/`, `setuwallet/`, etc.), selected with `--chain=setu`

Both chains share the same RPC interface, routing engine, HTLC Switch, and channel state machine — **one codebase, two chains**.

---

## What is Setu?

**Setu** is the next-generation distributed consensus network designed by the Hetu Project for high-throughput payment workloads. Key features:

| Feature                     | Description                                                                                          |
| --------------------------- | ---------------------------------------------------------------------------------------------------- |
| **DAG-BFT Consensus**       | Byzantine fault-tolerant protocol based on a directed acyclic graph; confirmation latency < 1 second |
| **VLC Hybrid Clock**        | Vector Logic Clock (VLCSnapshot) for causal ordering of distributed events                           |
| **TEE Trusted Execution**   | Secure on-chain computation backed by AWS Nitro Enclaves                                             |
| **Object-Account Model**    | Sui-style object-oriented state management; channels identified by a 32-byte `ObjectID`              |
| **Merkle State Commitment** | Binary + Sparse Merkle Trees for verifiable state                                                    |

The network consists of **Validator nodes** (verification + consensus) and **Solver nodes** (TEE execution + state transitions). Lightning channel primitives (`ChannelOpen`, `ChannelClose`, `ChannelForceClose`, `HTLCAdd`, `HTLCClaim`, `HTLCTimeout`, `ChannelPenalize`) are natively implemented in the Setu Runtime as **hardcoded EventTypes** — no general-purpose VM required.

---

## Architecture: Zero-Intrusion Adapter Pattern

```
┌─────────────────────────────────────────────────────┐
│          LND Application Layer (unchanged)           │
│  RPC Server · Routing Engine · HTLC Switch · FSM    │
└──────────────────────┬──────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────┐
│          Chain Abstraction Interfaces (never modify) │
│  ChainNotifier · WalletController · Signer          │
│  BlockChainIO  · ChainControl                       │
└────────┬──────────────────────────────┬─────────────┘
         │ chain=bitcoin                 │ chain=setu
┌────────▼───────────┐       ┌──────────▼────────────┐
│  Bitcoin Backends   │       │  Setu Adapters (new)  │
│  bitcoindnotify/   │       │  setunotify/           │
│  btcdnotify/       │       │  setuwallet/           │
│  neutrinonotify/   │       │  input/setu_channel.go │
│  lnwallet/btcwallet│       │  chainfee/setu_estimator│
└────────────────────┘       └────────────────────────┘
```

### Type Mapping Conventions

Setu adapters reuse Bitcoin/LND types internally, performing semantic translation at the boundary:

| LND Type              | Setu Semantic          | Notes                    |
| --------------------- | ---------------------- | ------------------------ |
| `wire.OutPoint.Hash`  | `ObjectID`             | Direct 32-byte mapping   |
| `wire.OutPoint.Index` | `0`                    | Setu has no UTXO index   |
| `btcutil.Amount`      | `u64`                  | Setu base unit           |
| `wire.MsgTx`          | Setu Event bytes       | Carries serialized Event |
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
- Setu chain: DAG finality replaces block confirmations (< 1 second)

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

Start a node with the Setu backend:

```sh
lnd --chain=setu --setu.rpc=<setu-node-endpoint> ...
```

---

## Key Reference Documents

| Topic                                 | File                                                                                       |
| ------------------------------------- | ------------------------------------------------------------------------------------------ |
| Setu adaptation & integration plan    | [1-refactor-docs/lnd-and-setu-integration.md](1-refactor-docs/lnd-and-setu-integration.md) |
| Setu chain architecture               | [1-refactor-docs/setu-architecture.md](1-refactor-docs/setu-architecture.md)               |
| LND refactor plan                     | [1-refactor-docs/lnd-setu-refactor-plan.md](1-refactor-docs/lnd-setu-refactor-plan.md)     |
| LND engineering architecture overview | [1-refactor-docs/lnd-architecture.md](1-refactor-docs/lnd-architecture.md)                 |
| Setu ↔ LND interaction interface spec | [1-refactor-docs/setu-ln-interaction-spec.md](1-refactor-docs/setu-ln-interaction-spec.md) |

---

## Security

This node is still in **beta**. For mainnet operation, please refer to the [Safe Operating Guide](docs/safety.md).

If you discover a security vulnerability, please issue [a GitHub issue](https://github.com/loka-network/loka-p2p-lnd/issues).

---

## Further Reading

- [Contribution Guide](https://github.com/lightningnetwork/lnd/blob/master/docs/code_contribution_guidelines.md)
- [Docker Deployment Guide](docs/DOCKER.md)
- [Installation Instructions](docs/INSTALL.md)
