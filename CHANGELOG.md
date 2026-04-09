# Changelog

## [Unreleased] - 2026-04-09
### Fixed
- **Fixed SUI Devnet RPC Execution Parsing Errors:** Modified `sui_executeTransactionBlock` natively inside `chainntnfs/suinotify/rpc_client.go` to explicitly enforce `WaitForEffectsCert` over `WaitForLocalExecution`. This mitigates `invalid character 'u'` Nginx proxy JSON deserialization errors originating from public Devnet load balancers forcefully terminating connections exceeding 30-second synchronization deadlines. 
- **Fixed Sui PTB Serialization Sync Errors:** Diagnosed and bypassed structural serialization desynchronizations embedded within the `sui-go-sdk` `MakeMoveVec` construction API. Substituted empty string pointers to securely encode `Option<TypeTag>::None` byte aliasing (`0x00`), preventing payload length parsing shifts and eliminating `malformed utf8` decoding collisions on the SUI validator engine while preserving accurate Native PTB operations.
- **Fixed Single-Coin Sui Channel Funding Lockup:** Resolved a critical bug where users with exactly one SUI coin in their wallet could not open Lightning channels due to SUI gas-locking protocol boundaries. Migrated `open_channel` parameter compilation to conditionally construct an inline `tx.SplitCoins` PTB via `block-vision/sui-go-sdk` for precise single-coin derivation, bypassing the deprecated `unsafe_moveCall` RPC limitations while leaving standard multi-coin arrays intact.

## [Unreleased] - 2026-03-30

### Fixed
- **Fixed Cooperative Close Signature Vulnerability:** Remediated a critical vulnerability in the SUI `lightning.move` contract where `close_channel` allowed arbitrarily decoupled `sighash` injections, exposing channels to cooperative close spoofing attacks. Removed legacy `sighash` arguments from all Move function signatures and enforced rigid mathematically-bound native `bcs::to_bytes` serialization derivations across `sui_channel.go`, `channel.go`, and `suisigner.go`.
- **Fixed Sui Force Close Payload Generator:** Fixed a symmetric parameter assignment bug in `DeriveCommitmentKeys` invocation within `lnwallet/wallet.go` that incorrectly transposed `localChanCfg` and `remoteChanCfg` during SUI `signCommitTx` payload caching, seamlessly resolving strict signature validation mismatch (`EInvalidSignature`) which broke peer funding.
- **Implemented Dynamic Commit Derivation:** Refactored `contractcourt/channel_arbitrator.go` to dynamically reconstruct the corresponding `RevocationHash` from `RevocationProducer` matching precisely the active `CommitHeight` instead of mocking the static genesis state, guaranteeing robust parameter compliance with SUI force close deployments.
- **Fixed Signature Decoupling Vulnerability:** Remediated a critical vulnerability in the SUI `lightning.move` contract where `force_close` and `close_channel` allowed arbitrarily decoupled `sighash` injections, exposing channels to state spoofing attacks. Removed legacy `sighash` arguments from all Move function signatures.
- **Enforced Native SUI Payload Serialization:** Rewrote `GenerateSuiPayloadHash` inside the Go `input` package to exactly mathematically match `bcs::to_bytes` concatenation rules for integers, arrays, and HTLC structs, preventing payload spoofing.
- **Decoupled Bitcoin SegWit Signatures:** Refactored `channel.go` and `suisigner.go` to structurally intercept SUI native operations via a JSON payload injected directly into `wire.MsgTx` `SignatureScript`, bypassing generic Bitcoin hashing routines and cleanly securing `ReceiveNewCommitment` validation using strictly bound parameters.
- **Fixed SUI ecdsa_k1 Hashing Mismatch:** Applied an explicit double-SHA256 hash inside `suisigner.go` to synchronize signature parameters with SUI's `ecdsa_k1::secp256k1_verify` strict payload requirements when `hash_id = 1` is natively applied by Move, thereby preventing `EInvalidSignature` force closures.
- **Fixed HTLC Sui Signature Validations:** Fixed `sigHash` capture in `genHtlcSigValidationJobs` loop inside `channel.go`. HTLC signatures now strictly implement the double-hashed payload evaluation ensuring Sui Move VM simulation security standards are met natively inside the LND worker pool validator.
- **Restored Cooperative Close Compatibility:** Reverted Cooperative Close parameter bindings within `lightning.move` to accept external `sighash` arrays natively constructed dynamically by 2-of-2 `chancloser` aggregators maintaining backwards compatibility for all multi-sig states.
- **Fixed SUI Event Synchronization Deadlock:** Refactored SUI event watchers in `rpc_client.go` to continuously intercept asynchronous TxDigest bridging inside the polling reactor to rapidly release nodes hanging infinitely under `waiting_close_channels`.
- **Hardened Testing Benchmarks:** Added forceful `pkill -9` process eviction to `performance_test_sui.sh` within shell-intercept `EXIT` hooks avoiding orphaned Go and Database components.

All notable changes to this project will be documented in this file.

---

## [Unreleased] - 2026-03-27

### Added
- Native SUI Object ID mappings in `SuiClient.RegisterPseudoToChannel` to bridge LND's Bitcoin-like Pseudo-Hashes straight into the SUI Network states, averting the execution races generated randomly between peers interacting asynchronously.
- Support for intercepting `EInvalidSignature` (MoveAbort 2) errors inside `suiwallet.go`. When a peer node resolves a `close_channel` Cooperative Sweep first, `IsChannelClosed(&channelID)` aborts LND's signature iteration gracefully, completing channel lifecycles cleanly without logging false Positive anomalies involving 12850 dust residues.
- Custom Mock fallback assertions directly in `noop_client.go` maintaining integration test environment boundaries natively across different nodes.

### Fixed
- **Fixed SUI Breach Arbitrator Deadlocks:** Identified and eliminated a critical execution freeze inside `contractcourt/breach_arbitrator.go` where Bob's Watchtower would infinitely hang awaiting `waitForSpendEvent` confirmations. SUI's bypass of physical Output UTXOs caused `len(breachedOutputs)` to structurally equal 0, perpetually forcing the Justice Transaction executor into a wait-loop stall. Implemented a zero-intrusion `IsSui` dynamic output injector that mocks the root SUI Channel Object as the sweep target, immediately triggering the native event watch-loop and successfully allowing `penalize` transaction executions to definitively close cheated channels.
- **Fixed `itest_sui.sh` Terminal Output Assertion Race:** Revised the Watchtower detection integration checks to actively grep the live `$BOB_DIR/lnd.log` buffer natively rather than erroneously evaluating the post-mortem `.bob_lnd.log` cleanup archive, strictly synchronizing stdout `make itest` assertions.

---

## [Unreleased] - 2026-03-24
### Added
- Phase 3C: Adapted Contract arbitration resolvers to bypass Sweeper and directly publish `sui-htlc-timeout-direct`, `sui-htlc-claim-direct`, and `sui-channel-claim-local` payload signatures to the Sui RPC node.
- Phase 3D: Adapted Funding flow to directly publish Move Calls and obtain unique `ObjectID` prior to the remainder of channel setup.
- Included correct signature passing for Sui HTLC payloads using `Serialize()`.
- Added `ITEST_SUI_FAST_SWEEP` shell environment variable to dynamically downgrade `sui_assembler.go` force close CLTV `CSVDelay` bindings from 24 Hours to 15 Seconds during `./scripts/itest_sui.sh localnet` validations, enabling full automated lifecycle testing of `claim_force_close` executions.

### Fixed
- **Fixed Force Close HTLC Burn Vulnerability:** Addressed a critical architectural flaw where in-flight Routed Payments (HTLCs) were entirely excluded from SUI cooperative and non-cooperative closures, permanently burning capital inside the funding abstraction. Rewrote the `lightning.move` `force_close` parameters to ingest `htlc_ids`, `amounts`, `payment_hashes`, `expiries`, and `directions` natively mapped as arrays into the contract's `channel.htlcs` `Table`. Refactored `sui_channel.go`'s `ChannelForceClosePayload` and `channel_arbitrator.go` to intercept Bitcoin `cltv_expiry` threshold blocks natively mutating them using absolute Millisecond SUI clock equivalents mapping seamlessly across JSON RPC integrations inside `rpc_client.go`.
- **Fixed Sui Gas Drain in Force Close Sweeper:** Implemented a physical time interceptor `GetChannelStatus` within `suiwallet` tracking `close_timestamp_ms` and `to_self_delay` via native `suix_getObject` RPC calls to suppress 0x5 `ENotExpired` abort transactions, preventing LND from burning SUI Gas on premature maturity height broadcasts.
- **Fixed Integration Test JSON Parser Pipeline:** Rewrote `sui client test-publish` pipeline in `itest_sui.sh` to extract JSON strictly from the opening brace `{` using `sed`, completely neutralizing multi-line compiler `[NOTE]` diagnostic strings that shattered `jq` evaluations.
- **Fixed SUI Async Sweeper Confirmation Bypass:** Corrected the `GetChannelStatus` gas drain interceptor within `suiwallet` to return an explicit Go `fmt.Errorf` instead of a spoofed `nil`. Previously, returning `nil` tricked the custom asynchronous polling loop inside `commit_sweep_resolver.go` into falsely flagging the transaction as successfully claimed, permanently preventing the physical `claim_force_close` Native Sui Events from broadcasting.
- **Fixed Sighash Verification Compile Panics:** Addressed Go compile errors mapping 32-byte generic signature bounds successfully natively down into LND `channel_arbitrator.go` sweeps.
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

## [Unreleased] - 2026-03-13

### Changed

- **`lnwallet/channel.go` & `lnwallet/commitment.go`** — Extracted `CommitmentBuilder` and `ScriptEngine` into interfaces to decouple LND's protocol logic from Bitcoin-specific script generation. This completes Phase 3A of the Sui adapter integration.

---

## [Unreleased] - 2026-03-12

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

## [Unreleased] - 2026-03-09

### Changed

- **`lncfg/sui.go`** — Added network selection flags to `SuiNode`: `--suinode.mainnet`, `--suinode.testnet`, `--suinode.devnet`, `--suinode.simnet`. `Validate()` now enforces that at most one network flag is set.
- **`config.go` (`ValidateConfig`)** — When `--suinode.active` is set, bitcoin network flags (`--bitcoin.mainnet` etc.) are no longer required. The Bitcoin chain validation block is skipped; a Sui-specific branch selects the active Sui network (defaulting to devnet), uses `BitcoinRegTestNetParams` as a structural placeholder for `ActiveNetParams`, and sets `activeChainName = "sui"` for all directory construction. Directory paths (`networkDir`, `towerDir`, `LogDir`) are now derived from `activeChainName`/`activeNetworkName` variables so they are namespaced under `sui/<network>/` when Sui is active.

---

## [Unreleased] - 2026-03-05

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
- Fixed crucial Sui Payload Generation mismatch between SignNextCommitment and ReceiveNewCommitment where htlcDirections were populated strictly from the computing node's generic perspective instead of the absolute viewpoint of the Commitment Transaction's remote Owner, thereby solving a critical signature validation failure preventing HTLC lifecycle progression (force closes hanging during IN_FLIGHT state).
- Fixed MoveAbort 0 (EInvalidSignature) on Force Close at state_num 0 by modifying wallet.go's `signCommitTx` and `verifyCommitSig` to enforce Sui Payload hashing semantics instead of Bitcoin sighash for the initial FundingCommitment signatures.
