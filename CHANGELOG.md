# Changelog

All notable changes to this project will be documented in this file.

---

## [Unreleased] — 2026-03-05

### Overview

Initial implementation of the **Setu DAG chain adapter** for LND.  
This change set introduces a zero-intrusion adapter layer that allows LND to operate over the Setu distributed ledger (DAG-BFT, object-account model) in addition to Bitcoin, without modifying any existing Bitcoin code paths or interface signatures.

Activation is controlled by a single flag: `--setunode.active`. When absent, LND behaves identically to upstream.

---

### New Files

#### Chain Notifier

| File                                       | Description                                                                                                                                                                                  |
| ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `chainntnfs/setunotify/setunotify.go`      | Full `chainntnfs.ChainNotifier` implementation for Setu. Maps Bitcoin "block" → Setu epoch/anchor, "tx" → Setu EventId, "outpoint" → Setu ObjectId.                                          |
| `chainntnfs/setunotify/setunotify_test.go` | Unit tests for `SetuChainNotifier` covering epoch dispatch, confirmations, spend notifications, and stopped-notifier error paths.                                                            |
| `chainntnfs/setunotify/noop_client.go`     | `NoopSetuClient` placeholder implementing the `SetuClient` interface. All subscriptions return closed channels on quit. Replaced with a live gRPC client once the Setu RPC SDK is available. |

#### Chain Registry

| File                                 | Description                                                                                                                                                                                       |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `chainreg/setu_params.go`            | Setu network parameters (`BitcoinNetParams`-compatible) for DevNet, TestNet, MainNet, and SimNet. Defines `CoinTypeSetu = 99999` for BIP-44 HD key derivation.                                    |
| `chainreg/setu_params_test.go`       | Tests for Setu network parameter constants.                                                                                                                                                       |
| `chainreg/setu_chaincontrol.go`      | `newSetuPartialChainControl`: assembles a `PartialChainControl` for the Setu backend. Wires `SetuChainNotifier`, `SetuEstimator`, `BestBlockTracker`, routing policy defaults, and `HealthCheck`. |
| `chainreg/setu_chaincontrol_test.go` | Tests for `newSetuPartialChainControl`: default routing policy, custom policy override, health check lifecycle, fee estimator startup.                                                            |

#### Wallet Stubs (`lnwallet/setuwallet/`)

| File                                           | Description                                                                                                                                                          |
| ---------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lnwallet/setuwallet/setuwallet.go`            | Stub `lnwallet.WalletController`. All unimplemented methods return `ErrUnsupported`. `IsSynced()` returns `true`; `BackEnd()` returns `"setu"`.                      |
| `lnwallet/setuwallet/setuio.go`                | Stub `lnwallet.BlockChainIO`. `GetUtxo` is the semantic entry point for Setu Channel Object queries (by ObjectID via `op.Hash`); full implementation deferred.       |
| `lnwallet/setuwallet/setusigner.go`            | Stub `input.Signer` + `input.MuSig2Signer`. All eight MuSig2 methods and base signing methods return `ErrUnsupported`.                                               |
| `lnwallet/setuwallet/setukeyring.go`           | Stub `keychain.SecretKeyRing`. All seven key-derivation and signing methods return `ErrUnsupported`.                                                                 |
| `lnwallet/setuwallet/setumessagesigner.go`     | Adds `SignMessage` to `Wallet`, satisfying `lnwallet.MessageSigner` required by `chainreg.NewChainControl`.                                                          |
| `lnwallet/setuwallet/setuwallet_stubs_test.go` | Comprehensive stub tests: interface compile-time assertions, `ErrUnsupported` coverage for all stub types, `BackEnd`/`IsSynced`/`GetRecoveryInfo` behavioral checks. |

#### Fee Estimator

| File                                       | Description                                                                                                             |
| ------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------- |
| `lnwallet/chainfee/setu_estimator.go`      | `SetuEstimator` wrapping `StaticEstimator` at 12 500 sat/kW. Swappable for a dynamic Setu gas-price API when available. |
| `lnwallet/chainfee/setu_estimator_test.go` | Tests for static fee value, relay fee, and idempotent `Start`/`Stop`.                                                   |

#### Config

| File                 | Description                                                                                                                                                                                                                                                        |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `lncfg/setu.go`      | `SetuNode` config struct with CLI flags under the `setunode.*` namespace. Fields: `Active`, `RPCHost`, `TLSCertPath`, `MacaroonPath`, `SubnetID`, `ChainID`, `EpochInterval`, `NumConfs`, `CSVDelay`. Includes `DefaultSetuNode()`, `Validate()`, and `RPCAddr()`. |
| `lncfg/setu_test.go` | Tests for `DefaultSetuNode` values, `Validate` edge cases (empty host, zero interval, zero confs), and `RPCAddr` port handling.                                                                                                                                    |

#### Channel Event Builder

| File                         | Description                                                                                                                                                                                                                                                                                                                                                       |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `input/setu_channel.go`      | Setu channel event builder — the Setu equivalent of Bitcoin's `script_utils.go`. Defines `SetuEventType` enum (8 types), per-event payload structs, and `BuildSetuEventTx`/`DecodeSetuEventTx` for packing Setu events into `wire.MsgTx` wrappers (ObjectID in `OutPoint.Hash`, JSON event in `SignatureScript`). Convenience constructors for all 8 event types. |
| `input/setu_channel_test.go` | Full round-trip tests for every event type, error-path tests (nil tx, no inputs, garbled script), and per-convenience-constructor smoke tests.                                                                                                                                                                                                                    |

#### Top-level Wiring

| File                    | Description                                                                                                                                                                                                    |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `setu_chain_builder.go` | `buildSetuChainControl`: assembles the full `chainreg.ChainControl` for Setu by combining stub wallet, key ring, signer, and block chain I/O into an `lnwallet.Config`, then calls `chainreg.NewChainControl`. |

#### Debug Config

| File                  | Description                                                                                                                                                                |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `.vscode/launch.json` | Added two VS Code debug configurations: `lnd-setu (devnet)` (connects to `localhost:9000`, no TLS) and `lnd-setu (testnet)` (node/subnet/chain via environment variables). |

---

### Modified Files

#### `config.go`

- Added `SetuChainName = "setu"` constant.
- Added `Setu *lncfg.Chain` (group `"Setu"`, namespace `"setu"`) to `Config`.
- Added `SetuMode *lncfg.SetuNode` (group `"setunode"`, namespace `"setunode"`) to `Config`.
- Populated Setu defaults in `DefaultConfig()` using `chainreg.DefaultSetu*` constants.

#### `chainreg/chainregistry.go`

- Added `Setu *lncfg.Chain` and `SetuMode *lncfg.SetuNode` fields to `chainreg.Config`.
- Added early-return at the top of `NewPartialChainControl`: when `cfg.SetuMode != nil && cfg.SetuMode.Active`, the function delegates to `newSetuPartialChainControl` and returns, keeping all Bitcoin code paths untouched.

#### `config_builder.go`

- Added `Setu` and `SetuMode` fields to the `chainControlCfg` construction in `DefaultWalletImpl.BuildChainControl`.
- Added early-return before `btcwallet.New`: when `SetuMode.Active`, delegates to `buildSetuChainControl`.

---

### Architecture Notes

**Activation:** `--setunode.active` (boolean flag). Default is `false`; existing Bitcoin-only deployments are unaffected.

**Type mapping (Bitcoin wire types reused internally):**

| Bitcoin type             | Setu semantic                                   |
| ------------------------ | ----------------------------------------------- |
| `wire.OutPoint.Hash`     | Setu `ObjectID` (32 bytes)                      |
| `wire.OutPoint.Index`    | Always `0` for channel objects                  |
| `wire.MsgTx`             | Setu Event envelope (JSON in `SignatureScript`) |
| `btcutil.Amount`         | Setu minimum unit (1:1 mapping, placeholder)    |
| `chainfee.SatPerKWeight` | Setu gas price placeholder                      |

**Setu `EventType` → Bitcoin Script analogy:**

| `SetuEventType`      | Bitcoin equivalent              |
| -------------------- | ------------------------------- |
| `ChannelOpen`        | 2-of-2 multisig funding tx      |
| `ChannelClose`       | Cooperative-close tx            |
| `ChannelForceClose`  | Commitment tx broadcast         |
| `ChannelClaimLocal`  | `to_local` CSV-locked output    |
| `ChannelClaimRemote` | `to_remote` output              |
| `HTLCClaim`          | HTLC-success (hash-lock) script |
| `HTLCTimeout`        | HTLC-timeout (CLTV) script      |
| `ChannelPenalize`    | Justice (breach-remedy) tx      |

**Pending (stub → real implementation):**

- `NoopSetuClient` → live gRPC client against Setu validator node.
- `SetuBlockChainIO.GetUtxo` → query Channel Object by ObjectID.
- `SetuSigner` → ECDSA signing via Setu key material.
- `SetuKeyRing` → BIP-44 HD derivation at `m/1017'/99999'/…`.
- `buildSetuChainControl` → wire real `lnwallet.LightningWallet` once stubs are replaced.
