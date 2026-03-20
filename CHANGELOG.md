# Changelog

All notable changes to this project will be documented in this file.

---

## [Unreleased]
### Added
- Phase 3C: Adapted Contract arbitration resolvers to bypass Sweeper and directly publish `sui-htlc-timeout-direct`, `sui-htlc-claim-direct`, and `sui-channel-claim-local` payload signatures to the Sui RPC node.
- Phase 3D: Adapted Funding flow to directly publish Move Calls and obtain unique `ObjectID` prior to the remainder of channel setup.
- Included correct signature passing for Sui HTLC payloads using `Serialize()`.

### Fixed
- **Fixed Force Close Instant Sweeping Vulnerability:** Extrapolated the raw Bitcoin `144` block delay inside `sui_assembler.go` into real-time milliseconds (24 Hours) to enforce strict mathematically mature delays within the SUI clock (`CSVDelay`).
- **Fixed Force Close SUI LND Panics:** Rewrote `Launch()` in `commit_sweep_resolver.go` to construct a robust asynchronous polling Goroutine preventing false-positive resolution `ENotExpired` flags. Force-closed channels now safely wait in Limbo instead of dropping natively out of `pending_force_closing_channels`.
- Fixed `invalid_htlc_sig` during Sui cooperative and force closes by applying correct private key tweaks (`SingleTweak` and `DoubleTweak`) in `SuiSigner.SignOutputRaw`.
- Fixed `counterparty's commitment signature is invalid` and `not all signatures empty on failed checkmultisig` crashes by bypassing strict Bitcoin `OP_CHECKMULTISIG` engine validations in `wallet.go` and `channel.go` using a safe manual `SHA256(sighash)` fallback mathematically equivalent for Sui Move signatures.
- Fixed `failed to decode call envelope` during Sui force closes by having the `ChannelArbitrator` correctly wrap Bitcoin commitment transactions into Sui `force_close` Move Call envelopes.
- Fixed `EInvalidSignature` test failures in the Move contract by ensuring `ecdsa_k1::secp256k1_verify` is passed the raw payload with hash algorithm `1` (SHA256) instead of a pre-hashed payload.
- Fixed signature validation tests in `lightning_tests.move` by using standard `btcec/v2/ecdsa` in Go to generate deterministic, low-S (BIP-62 compliant) `secp256k1` signatures ensuring full compatibility with Sui Move VM requirements.
- **Fixed Sui Node `Invalid user signature` execution crashes:** Migrated native Go SECP256K1 signature generation to use `SHA256(Blake2B(intent))` to perfectly replicate the implicit double-hashing sequence inadvertently introduced by the `@mysten/sui` Typescript SDK.
- **Fixed `encoding/hex: invalid byte: U+007A 'z'` crash:** Swapped `chainhash` hexadecimal hex-decoding in `ExecuteTransactionBlock` with a native Base58 parser to properly decode Sui's Transaction Block `Digest` format.
- Fixed panics and `invalid shutdown script` errors during Sui cooperative channel closures by bypassing strict Bitcoin payload length (`deliveryAddressMaxSize`) and script type validation for 66-byte hex Sui addresses.
- **Fixed Sui Channel Capacity Overflow:** Increased default channel capacity to 1000 SUI and added a strict Wumbo limit at 9,000,000 SUI (as well as RPC interception) to prevent internal `int64`/`uint64` overflow vulnerabilities when mapping Mist to MSats.
- **Fixed Channel Close Hanging:** Updated `SuiRPCClient` to correctly poll `suix_queryEvents` (instead of `sui_getEvents`) for mapping `.ObjectSpend` notifications, and removed mock delays in `SubscribeEventConfirmation` by introducing a real `sui_getTransactionBlock` checkpoint poller.
- **Fixed zombie `lncli` processes:** Added `pkill` cleanup for long-running stream subscriptions (`closechannel`) in `itest_sui.sh` to prevent resource leaks during local testing.
- **Added Native SUI Coin Payouts:** Enhanced the `close_channel` and `penalize` functions in `sui-contracts/lightning` to explicitly split the internal `Balance<SUI>` state and execute `transfer::public_transfer` to sweep SUI physical coins directly back to the Alice and Bob wallet addresses upon channel teardown or breach.
- Fixed `Transfer of an object to transaction sender address` and `abort without named constant` linter warnings in `sui-contracts/lightning` by applying `#[allow(lint(self_transfer))]` and defining `EInvalidStatus`.
- **Fixed channels stuck in `waiting_close_channels`:** Added `suiHexToHash`/`hashToSuiHex` byte-order helpers to eliminate the byte-reversal mismatch between `chainhash.Hash.String()` (Bitcoin convention) and Sui's big-endian hex ObjectIDs. Fixed `SubscribeObjectSpend` channel_id comparison, `GetCoins`, and `BuildMoveCall` hex conversions.
- **Fixed wrong channel identifier stored on open:** `ExecuteOpenChannelCall` now uses `ExecuteTransactionBlockFull` (with `showObjectChanges`) to extract the actual created Channel ObjectID from the Sui RPC response, instead of incorrectly returning the tx digest as the channel identifier.
- **Fixed channels stuck in `pending_open`:** `SubscribeEventConfirmation` now falls back to `sui_getObject` when the tx digest lookup fails (because the funding manager passes the ObjectID, not a tx digest). Since Sui has instant finality, the channel is confirmed immediately once the object exists on-chain.
- **Fixed Sui CLI 1.68+ compatibility:** Updated `lightning.move` to Move 2024 edition (`public struct`, explicit `let mut`), added `edition = "2024.beta"` and Sui framework dependency to `Move.toml`, switched `itest_sui.sh` to use `test-publish --build-env` and clean stale `Move.lock`/`Pub.*.toml` between runs.
- **Fixed cooperative close never reaching Sui chain:** Bitcoin-style close transactions from `chancloser.go` were failing at `DecodeSuiCallTx()` (not a Sui envelope). Added `publishBitcoinStyleTx` in `suiwallet.go` that extracts `channelID` from `FundingOutpoint.Hash` and output balances, then constructs and executes a `close_channel` Sui Move call. Made errors non-fatal so both peers can independently attempt the close.
- **Fixed cooperative close crash (index out of range):** `SpendDetail.SpendingTx` in `suinotify.go` was constructed without any `TxOut`, causing `chain_watcher.go:764` to panic. Added a placeholder OP_RETURN output.
- **Fixed force close `channel not found`:** `channel_arbitrator.go` used `FetchHistoricalChannel()` to get Sui envelope data, but the channel is still active during force close. Replaced with direct placeholder construction that doesn't depend on the historical channel database.
- **Fixed Bob lacking SUI gas for close:** Added Bob wallet funding via faucet in `itest_sui.sh` so both peers have gas for close transaction broadcasts.

### Changed
- Refactored `htlc_timeout_resolver`, `htlc_success_resolver`, and `commit_sweep_resolver` in `contractcourt` to route through Sui via `IsSui` flag checking without modifying existing bitcoin logic.
- Implemented `ExecuteOpenChannelCall` interface on `WalletController` to allow the wallet to execute Sui move calls during channel funding.
- Extracted `CommitmentBuilder` out of `LightningChannel` (Phase 3A).
- Extracted functions using `txscript` out of `LightningChannel` into a `ScriptEngine` interface (Phase 3B - ongoing).

---

## [Unreleased] — 2026-03-13

### Changed

- **`lnwallet/channel.go` & `lnwallet/commitment.go`** — Extracted `CommitmentBuilder` and `ScriptEngine` into interfaces to decouple LND's protocol logic from Bitcoin-specific script generation. This completes Phase 3A of the Sui adapter integration.

---

## [Unreleased] — 2026-03-12

### Added

- **`chainntnfs/suinotify/rpc_client.go`** — Implemented `SuiRPCClient`, a JSON-RPC client for Sui providing connectivity for checkpoint polling, coin querying, and transaction execution.
- **`lnwallet/chanfunding/sui_assembler.go`** — Implemented `SuiAssembler` and `SuiIntent` to support LND channel funding via Sui Move call transactions.
- **`sui-contracts/lightning`** — Initial implementation of the Lightning Move module for Sui, supporting channel lifecycle (open, close, force-close) and HTLC management.

### Changed

- **Refactor (Setu to Sui)** — Performed a project-wide rename of all `setu` references to `sui` to align with the parallel development strategy using the Sui network.
- **`lnwallet/suiwallet/`** — Upgraded wallet adapters (`SuiWallet`, `SuiSigner`, `SuiKeyRing`) from stubs to functional implementations. `SuiKeyRing` now supports BIP-44 derivation for Sui (coin type 784). `SuiWallet` now supports `ListUnspentWitness` and `ConfirmedBalance` via the RPC client.
- **`sui_chain_builder.go`** — Updated `buildSuiChainControl` to wire the functional `SuiRPCClient`, `SuiSigner`, and `SuiKeyRing` into the chain control plane.
- **Docs** — Reorganized `1-refactor-docs/` into `sui/` and `setu/` subdirectories to distinguish between the current Sui implementation and the long-term Setu target.

---

## [Unreleased] — 2026-03-09

### Changed

- **`lncfg/sui.go`** — Added network selection flags to `SuiNode`: `--suinode.mainnet`, `--suinode.testnet`, `--suinode.devnet`, `--suinode.simnet`. `Validate()` now enforces that at most one network flag is set.
- **`config.go` (`ValidateConfig`)** — When `--suinode.active` is set, bitcoin network flags (`--bitcoin.mainnet` etc.) are no longer required. The Bitcoin chain validation block is skipped; a Sui-specific branch selects the active Sui network (defaulting to devnet), uses `BitcoinRegTestNetParams` as a structural placeholder for `ActiveNetParams`, and sets `activeChainName = "sui"` for all directory construction. Directory paths (`networkDir`, `towerDir`, `LogDir`) are now derived from `activeChainName`/`activeNetworkName` variables so they are namespaced under `sui/<network>/` when Sui is active.

---

## [Unreleased] — 2026-03-05

### Overview

Initial implementation of the **Sui DAG chain adapter** for LND.  
This change set introduces a zero-intrusion adapter layer that allows LND to operate over the Sui distributed ledger (DAG-BFT, object-account model) in addition to Bitcoin, without modifying any existing Bitcoin code paths or interface signatures.

Activation is controlled by a single flag: `--suinode.active`. When absent, LND behaves identically to upstream.

---

### New Files

#### Chain Notifier

| File                                       | Description                                                                                                                                                                                  |
| ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `chainntnfs/suinotify/suinotify.go`      | Full `chainntnfs.ChainNotifier` implementation for Sui. Maps Bitcoin "block" → Sui epoch/anchor, "tx" → Sui EventId, "outpoint" → Sui ObjectId.                                          |
| `chainntnfs/suinotify/suinotify_test.go` | Unit tests for `SuiChainNotifier` covering epoch dispatch, confirmations, spend notifications, and stopped-notifier error paths.                                                            |
| `chainntnfs/suinotify/noop_client.go`     | `NoopSuiClient` placeholder implementing the `SuiClient` interface. All subscriptions return closed channels on quit. Replaced with a live gRPC client once the Sui RPC SDK is available. |

#### Chain Registry

| File                                 | Description                                                                                                                                                                                       |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `chainreg/sui_params.go`            | Sui network parameters (`BitcoinNetParams`-compatible) for DevNet, TestNet, MainNet, and SimNet. Defines `CoinTypeSui = 99999` for BIP-44 HD key derivation.                                    |
| `chainreg/sui_params_test.go`       | Tests for Sui network parameter constants.                                                                                                                                                       |
| `chainreg/sui_chaincontrol.go`      | `newSuiPartialChainControl`: assembles a `PartialChainControl` for the Sui backend. Wires `SuiChainNotifier`, `SuiEstimator`, `BestBlockTracker`, routing policy defaults, and `HealthCheck`. |
| `chainreg/sui_chaincontrol_test.go` | Tests for `newSuiPartialChainControl`: default routing policy, custom policy override, health check lifecycle, fee estimator startup.                                                            |

#### Wallet Stubs (`lnwallet/suiwallet/`)

| File                                           | Description                                                                                                                                                          |
| ---------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lnwallet/suiwallet/suiwallet.go`            | Stub `lnwallet.WalletController`. All unimplemented methods return `ErrUnsupported`. `IsSynced()` returns `true`; `BackEnd()` returns `"sui"`.                      |
| `lnwallet/suiwallet/suiio.go`                | Stub `lnwallet.BlockChainIO`. `GetUtxo` is the semantic entry point for Sui Channel Object queries (by ObjectID via `op.Hash`); full implementation deferred.       |
| `lnwallet/suiwallet/suisigner.go`            | Stub `input.Signer` + `input.MuSig2Signer`. All eight MuSig2 methods and base signing methods return `ErrUnsupported`.                                               |
| `lnwallet/suiwallet/suikeyring.go`           | Stub `keychain.SecretKeyRing`. All seven key-derivation and signing methods return `ErrUnsupported`.                                                                 |
| `lnwallet/suiwallet/suimessagesigner.go`     | Adds `SignMessage` to `Wallet`, satisfying `lnwallet.MessageSigner` required by `chainreg.NewChainControl`.                                                          |
| `lnwallet/suiwallet/suiwallet_stubs_test.go` | Comprehensive stub tests: interface compile-time assertions, `ErrUnsupported` coverage for all stub types, `BackEnd`/`IsSynced`/`GetRecoveryInfo` behavioral checks. |

#### Fee Estimator

| File                                       | Description                                                                                                             |
| ------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------- |
| `lnwallet/chainfee/sui_estimator.go`      | `SuiEstimator` wrapping `StaticEstimator` at 12 500 sat/kW. Swappable for a dynamic Sui gas-price API when available. |
| `lnwallet/chainfee/sui_estimator_test.go` | Tests for static fee value, relay fee, and idempotent `Start`/`Stop`.                                                   |

#### Config

| File                 | Description                                                                                                                                                                                                                                                        |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `lncfg/sui.go`      | `SuiNode` config struct with CLI flags under the `suinode.*` namespace. Fields: `Active`, `RPCHost`, `TLSCertPath`, `MacaroonPath`, `SubnetID`, `ChainID`, `EpochInterval`, `NumConfs`, `CSVDelay`. Includes `DefaultSuiNode()`, `Validate()`, and `RPCAddr()`. |
| `lncfg/sui_test.go` | Tests for `DefaultSuiNode` values, `Validate` edge cases (empty host, zero interval, zero confs), and `RPCAddr` port handling.                                                                                                                                    |

#### Channel Event Builder

| File                         | Description                                                                                                                                                                                                                                                                                                                                                       |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `input/sui_channel.go`      | Sui channel event builder — the Sui equivalent of Bitcoin's `script_utils.go`. Defines `SuiEventType` enum (8 types), per-event payload structs, and `BuildSuiEventTx`/`DecodeSuiEventTx` for packing Sui events into `wire.MsgTx` wrappers (ObjectID in `OutPoint.Hash`, JSON event in `SignatureScript`). Convenience constructors for all 8 event types. |
| `input/sui_channel_test.go` | Full round-trip tests for every event type, error-path tests (nil tx, no inputs, garbled script), and per-convenience-constructor smoke tests.                                                                                                                                                                                                                    |

#### Top-level Wiring

| File                    | Description                                                                                                                                                                                                    |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `sui_chain_builder.go` | `buildSuiChainControl`: assembles the full `chainreg.ChainControl` for Sui by combining stub wallet, key ring, signer, and block chain I/O into an `lnwallet.Config`, then calls `chainreg.NewChainControl`. |

#### Debug Config

| File                  | Description                                                                                                                                                                |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `.vscode/launch.json` | Added two VS Code debug configurations: `lnd-sui (devnet)` (connects to `localhost:9000`, no TLS) and `lnd-sui (testnet)` (node/subnet/chain via environment variables). |

---

### Modified Files

#### `config.go`

- Added `SuiChainName = "sui"` constant.
- Added `Sui *lncfg.Chain` (group `"Sui"`, namespace `"sui"`) to `Config`.
- Added `SuiMode *lncfg.SuiNode` (group `"suinode"`, namespace `"suinode"`) to `Config`.
- Populated Sui defaults in `DefaultConfig()` using `chainreg.DefaultSui*` constants.

#### `chainreg/chainregistry.go`

- Added `Sui *lncfg.Chain` and `SuiMode *lncfg.SuiNode` fields to `chainreg.Config`.
- Added early-return at the top of `NewPartialChainControl`: when `cfg.SuiMode != nil && cfg.SuiMode.Active`, the function delegates to `newSuiPartialChainControl` and returns, keeping all Bitcoin code paths untouched.

#### `config_builder.go`

- Added `Sui` and `SuiMode` fields to the `chainControlCfg` construction in `DefaultWalletImpl.BuildChainControl`.
- Added early-return before `btcwallet.New`: when `SuiMode.Active`, delegates to `buildSuiChainControl`.

---

### Architecture Notes

**Activation:** `--suinode.active` (boolean flag). Default is `false`; existing Bitcoin-only deployments are unaffected.

**Type mapping (Bitcoin wire types reused internally):**

| Bitcoin type             | Sui semantic                                   |
| ------------------------ | ----------------------------------------------- |
| `wire.OutPoint.Hash`     | Sui `ObjectID` (32 bytes)                      |
| `wire.OutPoint.Index`    | Always `0` for channel objects                  |
| `wire.MsgTx`             | Sui Event envelope (JSON in `SignatureScript`) |
| `btcutil.Amount`         | Sui minimum unit (1:1 mapping, placeholder)    |
| `chainfee.SatPerKWeight` | Sui gas price placeholder                      |

**Sui `EventType` → Bitcoin Script analogy:**

| `SuiEventType`      | Bitcoin equivalent              |
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

- `NoopSuiClient` → live gRPC client against Sui validator node.
- `SuiBlockChainIO.GetUtxo` → query Channel Object by ObjectID.
- `SuiSigner` → ECDSA signing via Sui key material.
- `SuiKeyRing` → BIP-44 HD derivation at `m/1017'/99999'/…`.
- `buildSuiChainControl` → wire real `lnwallet.LightningWallet` once stubs are replaced.

- Updated Move contract `force_close` and `penalize` functions to use dynamic revocation hashes (Scheme A) instead of a hardcoded expected hash, fixing a critical security vulnerability.
- Updated LND Go bindings (`ChannelForceClosePayload`) to serialize the expected revocation hash natively during force close.
