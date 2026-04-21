# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository identity

This is the **Loka AI Agentic Payment P2P Lightning Node** — a fork of [lightningnetwork/lnd](https://github.com/lightningnetwork/lnd) (a full Lightning Network node in Go) that is being adapted to support **Sui** (DAG-BFT, object-account model, no general VM) alongside Bitcoin, and to later add **Setu** (Hetu Project's payment consensus layer).

Adaptation plan and deeper architecture live in [1-refactor-docs/](1-refactor-docs/) — particularly [lnd-architecture.md](1-refactor-docs/lnd-architecture.md), [sui/lnd-and-sui-integration.md](1-refactor-docs/sui/lnd-and-sui-integration.md), and [sui/sui-ln-interaction-spec.md](1-refactor-docs/sui/sui-ln-interaction-spec.md). Also read [AGENTS.md](AGENTS.md) for the same contract repeated to other assistants.

## Zero-intrusion adapter architecture

The core rule: **Bitcoin code paths and the Lightning application layer are untouched**. New chains plug in as alternative `ChainControl` implementations.

Top-to-bottom layering:

- **RPC / app layer** — [lnrpc/](lnrpc/), [server.go](server.go), [rpcserver.go](rpcserver.go). Do not change exported signatures.
- **Core subsystems** — [lnwallet/](lnwallet/), [contractcourt/](contractcourt/), [htlcswitch/](htlcswitch/), [routing/](routing/), [funding/](funding/), [channeldb/](channeldb/).
- **Chain abstraction interfaces** (NEVER modify signatures):
  - [chainntnfs/interface.go](chainntnfs/interface.go) — `ChainNotifier`
  - [lnwallet/interface.go](lnwallet/interface.go) — `WalletController`
  - [input/signer.go](input/signer.go) — `Signer`
  - [chainreg/chainregistry.go](chainreg/chainregistry.go) — `ChainControl`
- **Bitcoin backends** — `chainntnfs/bitcoindnotify/`, `btcdnotify/`, `neutrinonotify/`, `lnwallet/btcwallet/`. Do not modify.
- **Sui adapters (new)** — `suinotify/`, `suiwallet/`, `input/sui_channel.go`, `chainfee/sui_estimator`. Selected via `lncli --chain=sui`.

Sui type-mapping convention at the adapter boundary:

| LND type              | Sui semantic          | Notes                  |
| --------------------- | --------------------- | ---------------------- |
| `wire.OutPoint.Hash`  | `ObjectID`            | direct 32-byte mapping |
| `wire.OutPoint.Index` | `0`                   | Sui has no UTXO index  |
| `btcutil.Amount`      | `u64` base unit       |                        |
| `wire.MsgTx`          | Sui Event bytes       | serialized Event       |
| `chainhash.Hash`      | `EventId` / `AnchorId`| 32 bytes               |

**Sui is not a general-purpose VM in this context**: only hardcoded EventTypes (`ChannelOpen`, `ChannelClose`, `HTLCClaim`, …) exist in `sui-runtime`. Do not design Sui-side logic as if arbitrary Move were callable.

## Build, lint, test

Required Go: **1.25.5** (`GO_VERSION` in [Makefile](Makefile)). No vendoring; use `GOWORK=off` for tool invocations.

```sh
make build            # lnd-debug, lncli-debug in project dir
make install          # into $GOPATH/bin
make unit             # unit tests (requires btcd binary, auto-installed)
make unit-module      # unit tests for submodules (actor/, fn/, tools/)
make unit-race        # race detector
make itest            # integration tests; requires Docker for postgres backend
make lint             # golangci-lint via Docker
make fmt              # gofmt + gosimports
make rpc              # regenerate proto/gRPC/gateway files
make tidy-module      # go mod tidy across all modules
```

Makefile variable flags (see [make/testing_flags.mk](make/testing_flags.mk)) — combine with the targets above:

- `pkg=<import-path-suffix>` — scope unit tests to one package
- `case=<TestName>` — filter unit tests by name (use with `pkg=`)
- `icase=<TestName>` — filter integration tests by name
- `backend=btcd|bitcoind|neutrino` — chain backend for `itest` (default btcd)
- `dbbackend=bbolt|etcd|postgres|sqlite` — itest DB backend
- `nativesql=1` — with `dbbackend=postgres|sqlite`, use the native SQL path
- `tags=<buildtag>` — extra build tags appended to `DEV_TAGS`
- `timeout=<dur>` — override the 180m test timeout
- `log=debug` / `verbose=1` / `nocache=1` / `short=1`

Example — run a single itest case against bitcoind on postgres:

```sh
make itest icase=ChannelForceClose backend=bitcoind dbbackend=postgres
```

Example — run one unit test in one package:

```sh
make unit pkg=htlcswitch case=TestSwitchForward
```

## Project-specific conventions

- **Sub-modules with their own `go.mod`**: [actor/](actor/), [fn/](fn/), [tools/](tools/). `go test ./...` must run **inside** each directory; `make unit-module` iterates them via [scripts/unit_test_modules.sh](scripts/unit_test_modules.sh).
- **Functional utilities** in `fn/` (`Option[T]`, `Result[T]`, `Either[L,R]`) — prefer these over raw pointer-nil patterns in new code.
- **Actor concurrency model** in `actor/` — use for new concurrent components.
- **Protocol state machines** — build via `protofsm/`.
- **SQL schema** — managed through `sqlc` per [sqlc.yaml](sqlc.yaml). Do not hand-edit generated `*.sql.go`.
- **gRPC / protobuf** — generated `*pb.go`, `*pb.gw.go`, `*.pb.json.go` are checked in; regenerate with `make rpc`.
- **Logging** — each package registers its own subsystem in a `log.go` with `btclog`; wire it through [build/log.go](build/log.go).
- **Config** — [config.go](config.go) + [lncfg/](lncfg/) for validation; [config_builder.go](config_builder.go) wires `ImplementationCfg` / `ChainControlBuilder`.
- **HD key derivation** — `m/1017'/coinType'/keyFamily'/0/index`; see [keychain/](keychain/).
- **Build tags** — `DEV_TAGS` (debug), integration tag + backend tag (itest), `RELEASE_TAGS` (release). RPC tags (`autopilotrpc`, `chainrpc`, …) expand via `with-rpc=1`.
- **Changelog** — update [docs/release-notes/](docs/release-notes/) for user-visible changes (entries merged into CHANGELOG on release).
- **Godoc required** on all exported symbols.

## Startup / wiring entry points

- [lnd.go](lnd.go) — Main lifecycle / daemon wiring.
- [server.go](server.go) — Peer, gossip, routing server composition.
- [config_builder.go](config_builder.go) — How `ChainControl` is selected and constructed per chain.
- [rpcserver.go](rpcserver.go) — gRPC surface; proto defs in [lnrpc/](lnrpc/).
- Channel state machine — [lnwallet/channel.go](lnwallet/channel.go).
- HTLC forwarding — [htlcswitch/switch.go](htlcswitch/switch.go).

When adding a Sui (or Setu) feature, the edit path is almost always: new adapter package → register in `config_builder.go` → exercise through the existing interfaces. If you find yourself editing `server.go` or the core subsystems to accommodate a chain, stop and reconsider — that is the anti-pattern this fork explicitly forbids.
