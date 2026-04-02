# Loka LND Payment Application Integration & API Guide

This document serves as the architectural compass for integrating upper-layer applications, mobile clients, and AI Agents into the Loka P2P Agentic Payment network. 

Depending on your security model and target hardware, applications can interact with the network using either **Non-Custodial (Sovereign)** or **Custodial (Hosted)** methods.

---

## 1. Paradigm Overview

Before writing code, you must decide how your application handles cryptographic keys and channel liquidity:

1. **Non-Custodial**: Your application (or Agent) runs its own `lnd` node. It holds its own private keys, manages its own Sui Channels, and communicates directly with the P2P network. Perfect for autonomous AI Agents and sovereign mobile wallets.
2. **Custodial (Account-based)**: Operations scale by maintaining a single, massive LND server (the Liquidity Hub). Upper-layer applications (like a web-app wallet) do not run a node; instead, they query a centralized database that maps "virtual balances" carved out of the main Hub's liquidity. Ideal for centralized exchanges, enterprise financial accounts, and high-frequency internal micro-transactions.

---

## 2. Non-Custodial Integration Strategies

In this model, the application directly interfaces with standard `lnd` APIs.

### 2.1 Server/Cloud Environment (1-to-1 Node)
For AI Agents or backend servers with sufficient compute, you run a full `lnd` node alongside your application.

- **Agent Frameworks**: If you are instructing an AI Agent (e.g., AutoGPT) to operate the node autonomously via CLI, explicitly refer the Agent to the provided skill module: [`SKILL/loka-agentic-payment/SKILL.md`](../../SKILL/loka-agentic-payment/SKILL.md).
- **Basic CLI Flow**:
  1. Boot node: `lnd --chain=sui ...`
  2. Connect to a network Seed: `lncli connect <Pubkey>@<Host>`
  3. Create channel: `lncli openchannel --node_key=... --local_amt=...`
  4. Create invoice: `lncli addinvoice --amt=100` -> returns payment request.
  5. Pay invoice: `lncli payinvoice --pay_req=<string>`

### 2.2 Embedded / Mobile Light Node (iOS & Android)
For mobile phones, IoT devices, or lightweight edge computing, running a separate CLI binary is impractical. Instead, you compile the entire `lnd` logic into a native application SDK (Light Node).

The repository natively supports cross-compilation using `gomobile`. Since Loka's SUI adapter is built on pure Go without external CGo cryptographic dependencies, it compiles flawlessly to mobile architectures.

**Compilation Steps:**
1. Install `gomobile`:
   ```bash
   go install golang.org/x/mobile/cmd/gomobile@latest
   gomobile init
   ```
2. Build iOS and Android libraries:
   ```bash
   # Ensure Android Studio & NDK are installed for the Android build
   make mobile
   ```
3. **Output**:
   - iOS: `mobile/build/ios/Lndmobile.xcframework`
   - Android: `mobile/build/android/Lndmobile.aar`

**App Integration**: The mobile App integrates these libraries. Through the SDK bindings, the App starts an embedded `lnd` process locally, syncs light client data from SUI RPC servers directly (no full node required), and exposes the LND API natively to the mobile application's frontend.

---

## 3. LND Default API Interface

All Non-Custodial nodes expose APIs that grant full control over the lightning daemon. For a deep dive into the base protocol buffer definitions, refer to the origin files in [lnrpc/README.md](../lnrpc/README.md).

### 3.1 Crucial Payment & Channel APIs
Whether interacting via REST or gRPC, these are the fundamental API calls your application or Agent will invoke the most:

1. **`GetInfo`** (`GET /v1/getinfo`): Returns backend network status, your node's identity pubkey, and chain sync status. Always call this first to ensure the node is healthy.
2. **`WalletBalance` & `NewAddress`** (`GET /v1/balance/blockchain`): Native SUI token management to verify you have enough base gas for routing operations.
3. **`OpenChannel` & `CloseChannel`** (`POST /v1/channels`): The heavy-lifting endpoints that invoke physical on-chain transactions to deploy multi-sig Smart Contracts on the Sui blockchain.
4. **`AddInvoice`** (`POST /v1/invoices`): Generates a standardized cryptographic Lightning Payment Request for receiving funds.
5. **`SendPaymentV2`** (`POST /v2/router/send` via `Router` sub-service): **This is the underlying API for the CLI's `payinvoice` command.** (Note: There is no REST endpoint named "payinvoice"). Whether you are paying a standard invoice (by passing the `payment_request` string) or executing spontaneous invoice-less micro-payments (Keysend), this engine seamlessly handles HTLC execution, pathfinding, and mission control.
6. **`SubscribeInvoices`**: A powerful real-time streaming endpoint (primarily used over gRPC) that pushes an event to your backend the exact millisecond a payment is successfully received.

### 3.2 Protocol Comparison: REST vs. gRPC

LND inherently implements a Dual-Endpoint architecture. You can choose the protocol that best fits your language stack:

#### REST API (Port 8080 by default)
- **Mechanism**: Facilitated by `grpc-gateway`. It acts as a reverse proxy, translating standard JSON HTTP requests into gRPC protobuf sequences.
- **Pros**: Zero compilation required. Instantly testable via Postman, Python `requests`, or Browser `fetch()`. Excellent for rapid prototyping.
- **Cons**: Slightly higher serialization overhead due to JSON parsing.
- **Reference**: View `lnrpc/lightning.swagger.json` for full endpoint structures.

### Native gRPC API (Port 10009 by default)
- **Mechanism**: Pure HTTP/2 binary communication using Protocol Buffers.
- **Pros**: Maximum machine-level performance. Supports **Bi-directional Streaming** (e.g., subscribing to real-time invoice payments without polling).
- **Cons**: Requires compiling `.proto` files into language-specific stubs.

#### How to Compile & Use gRPC Clients (e.g., Node.js)
To connect a Node.js backend directly to LND's gRPC port, you do not need to manually compile files if you use dynamic loading.

Alternatively, to generate static stubs from the source [`lnrpc`](../lnrpc) directory:
1. Define the proto path and load using `@grpc/grpc-js` and `@grpc/proto-loader` in JavaScript.
2. Read the `admin.macaroon` and pass it into the request Metadata.
3. Read the `tls.cert` and pass it into the channel credentials.

*Example Node.js Snippet (Dynamic Loading):*
```javascript
const grpc = require('@grpc/grpc-js');
const protoLoader = require('@grpc/proto-loader');
const fs = require('fs');

const packageDefinition = protoLoader.loadSync('lnrpc/lightning.proto', {
  keepCase: true, longs: String, enums: String, defaults: true, oneofs: true
});
const lnrpc = grpc.loadPackageDefinition(packageDefinition).lnrpc;
// Construct metadata containing Macaroon and connect via grpcs..
```
For explicit static compilation across Python, Go, or Rust, refer to the official [LND gRPC documentation](https://lightning.engineering/api-docs/api/lnd/).

### 3.3 Authentication: Macaroons & TLS

Due to its strict zero-trust security model, LND requires cryptographic authentication for *all* API endpoints (both REST and gRPC).

1. **TLS Certificate**: LND utilizes self-signed certificates. You must explicitly disable SSL verification in API testers (like Postman) or trust the local cert `~/.lnd-agent/tls.cert` in your backend code.
2. **Macaroon (The API Token)**: Every request MUST include a Macaroon. However, `admin.macaroon` is a raw binary file. To pass it in an HTTP Header for REST APIs, you must first convert it into a continuous hexadecimal string.

**Extracting the Hex String via CLI:**
Run the following Unix command. The specific `xxd` parameters (`-ps -u -c 1000`) forcefully prevent newline breaks and ensure you get a pure, continuous uppercase string:
```bash
xxd -ps -u -c 1000 ~/.lnd-agent/data/chain/sui/devnet/admin.macaroon
```

**Passing it to the API:**
Inject the resulting hex string into your HTTP Request Header for REST calls:
> `Grpc-Metadata-macaroon: 0201036C... (Your Hex String)`

---

## 4. Custodial Integration (Hosted Architecture)

If your architecture involves a massive corporate platform hosting thousands of micro-agents (or human users) who do NOT need to run their own node, you should adopt a Custodial architecture.

Instead of writing complex internal virtual ledger systems yourself, connect your mega-node to reputable Open Source accounting layers.

### [LNBits](https://docs.lnbits.org/)
LNBits is a powerful, python-based, free Open-Source project that transforms your single LND node into a highly modular "Wallet as a Service" (WaaS).
- **How it works**: It connects to your LND node via gRPC/REST. It then exposes its own API to your end-users. When User A pays User B inside the LNBits ecosystem, it updates a local SQLite/PostgreSQL database instantly—no LND channel is touched (0 fees). When User A pays an external invoice, LNBits securely prompts your LND node to execute the transfer.
- **API Reference**: LNBits provides extensive REST APIs to generate virtually infinite wallets and invoices programmatically. See the [LNBits API Documentation](https://demo.lnbits.com/docs).

### LNDHub
A minimalist alternative created by BlueWallet.
- **How it works**: Strictly acts as an accounting proxy layer wrapping your LND node via Redis and Node.js.
- **API Reference**: Best suited for simple mobile custodial wallets. See the [LNDHub Repository & Spec](https://github.com/BlueWallet/LndHub).
