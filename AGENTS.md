# Copilot Instructions

## Project Overview

This is a **fork of [lightningnetwork/lnd](https://github.com/lightningnetwork/lnd)** — a full Lightning Network node implementation in Go — being adapted to support **Setu** (a DAG-BFT distributed ledger
 with an object-account model) alongside Bitcoin, under the `loka-agentic-payment` project.

See `1-refactor-docs/` for the full adaptation plan and architecture details.

---

## Architecture

The codebase uses a **zero-intrusion adapter pattern**: Bitcoin code paths are untouched; Setu is added as a new `ChainControl` implementation.

Key layering (top → bottom):

- **RPC / App layer**: `lnrpc/`, `server.go`, `rpcserver.go` — do not change signatures
- **Core subsystems**: `lnwallet/`, `contractcourt/`, `htlcswitch/`, `routing/`, `funding/`, `channeldb/`
- **Chain abstraction interfaces** (never modify signatures):
  - `chainntnfs/interface.go` — `ChainNotifier`
  - `lnwallet/interface.go` — `WalletController`
  - `input/signer.go` — `Signer`
  - `chainreg/chainregistry.go` — `ChainControl`
- **Bitcoin backends**: `chainntnfs/bitcoindnotify/`, `btcdnotify/`, `neutrinonotify/` — do not modify
- **Setu adapters (new)**: `setunotify/`, `setuwallet/`, `input/setu_channel.go`, `chainfee/setu_estimator`

Type mapping convention for Setu adapters:

- `wire.OutPoint.Hash` ← Setu `ObjectID`
- `btcutil.Amount` ← Setu unit (mapped)
- `wire.MsgTx` ← Setu Event serialized bytes

Selected via `lncli --chain=setu`.

---

## Code Style

- Standard Go conventions; `gofmt` / `gosimports` enforced
- Functional utilities in `fn/` (own `go.mod`): `Option[T]`, `Result[T]`, `Either[L,R]` — prefer these over raw pointer-nil patterns
- Actor concurrency model in `actor/` (own `go.mod`): use for new concurrent components
- `protofsm/` — use for new protocol state machines
- All exported symbols must have godoc comments
- No vendoring; use Go modules (`GOWORK=off` for tool invocations)
- Build tags: `DEV_TAGS` for debug builds, `ITEST_TAGS` for integration, `RELEASE_TAGS` for production

---

## Build and Test

```sh
# Build debug binaries (lnd-debug, lncli-debug)
make build

# Install to $GOPATH/bin
make install

# Unit tests (requires btcd binary)
make unit

# Unit tests for all submodules (actor/, fn/, etc.)
make unit-module

# Integration tests (builds binaries first, requires Docker for postgres backend)
make itest

# Lint (runs golangci-lint via Docker)
make lint

# Run a specific itest case
make itest icase=<TestCaseName> backend=btcd  # or backend=bitcoind / postgres
```

Required Go version: **1.25.5** (see `GO_VERSION` in Makefile).

---

## Project Conventions

- **Sub-modules with own `go.mod`**: `actor/`, `fn/`, `tools/` — run `go test ./...` inside each directory independently; `make unit-module` automates this.
- **SQL schema**: managed via `sqlc` (`sqlc.yaml`); do not hand-edit generated `*.sql.go` files.
- **gRPC / protobuf**: generated files (`*pb.go`, `*pb.gw.go`, `*.pb.json.go`) are committed; regenerate with `make rpc`.
- **Logging**: use `build/log.go` subsystem registration; each package gets its own `log.go` registering with `btclog`.
- **Config**: `config.go` + `lncfg/` for validation; `config_builder.go` for `ImplementationCfg` / `ChainControlBuilder`.
- **HD key derivation**: `m/1017'/coinType'/keyFamily'/0/index` — see `keychain/`.

---

## Integration Points

- **Setu chain**: DAG-BFT, object-account model, no general VM — only hardcoded EventTypes (`ChannelOpen`, `ChannelClose`, `HTLCClaim`, etc.) in `setu-runtime`. Do **not** design around a general programmable VM.
- **Bitcoin backends**: `btcd` (full node), `bitcoind` (RPC+ZMQ), `neutrino` (light client, BIP-158).
- **Database**: `kvdb/` abstraction over bbolt / etcd / SQL; `sqldb/` for PostgreSQL and SQLite backends.
- **RPC**: gRPC primary + HTTP REST gateway via `grpc-gateway`; proto definitions in `lnrpc/`.
- **Watchtower**: `watchtower/` — offline penalty broadcasting; keep compatible when modifying `contractcourt/`.

---

## Key Reference Files

| Concern                   | File(s)                                            |
| ------------------------- | -------------------------------------------------- |
| Startup / wiring          | `lnd.go`, `server.go`, `config_builder.go`         |
| Chain interface contracts | `chainntnfs/interface.go`, `lnwallet/interface.go` |
| Channel state machine     | `lnwallet/channel.go`                              |
| HTLC forwarding           | `htlcswitch/switch.go`                             |
| Setu adaptation plan      | `1-refactor-docs/lnd-and-setu-integration.md`      |
| Setu chain architecture   | `1-refactor-docs/setu-architecture.md`             |
| LND architecture overview | `1-refactor-docs/lnd-architecture.md`              |
