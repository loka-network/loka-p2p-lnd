# Detailed Refactoring Plan for LND Adaptation to Setu

> Objective: Integrate Setu using an adapter pattern without modifying LND's existing interface signatures, and support Setu's channel lifecycle and HTLC processes.
> Constraints: Setu currently does not have a general-purpose VM, requiring hardcoded Lightning primitives to be added on the Setu Runtime/Validator side.

## 1. Refactoring Principles and Overall Approach

- Zero-intrusion at the interface layer: Do not add new abstraction layers and do not change existing LND interface signatures; only add the Setu version at the implementation layer.
- Type reuse: Reuse Bitcoin/LND types internally within the adapter layer as much as possible, and perform semantic conversions at the boundaries.
- Dual-chain coexistence: Keep Bitcoin logic unchanged, and start the Setu backend via `--chain=setu`.
- End-to-end closed loop: Requires synchronization between on-chain Event/State and the LND channel state machine, as well as comprehensive test validation.

## 2. Key Interfaces and Setu Adaptation Implementation List

### 2.1 ChainNotifier (Chain Event Notification)

LND relies on `chainntnfs.ChainNotifier` to subscribe to confirmations, spends, blocks, and other events.

Adaptation Strategy:

- Use Setu Event/Anchor as the "block" semantic source.
- Use Setu EventId as the `TxID` semantic source.
- Simulate the version changes or state changes of Setu Channel Objects as "spends"/"confirmations".

Needs to be implemented:

- `RegisterConfirmationsNtfn`: For confirmation of Setu EventTypes, triggered via Anchor Finalized.
- `RegisterSpendNtfn`: Triggered by state changes of the Channel Object, such as force closes or penalties.
- `RegisterBlockEpochNtfn`: For Anchor event broadcasts (Anchor = BlockEpoch).

### 2.2 WalletController (Wallet Controller)

LND heavily relies on `lnwallet.WalletController` for UTXO selection, transaction construction, and signing.

Setu Adaptation Strategy:

- Use Setu `Coin` + `ObjectId` as a UTXO replacement.
- `wire.OutPoint.Hash` internally stores the Setu `ObjectId` (32B) with a fixed `Index = 0`.
- `btcutil.Amount` maps directly to Setu's smallest unit (u64).

Key method mapping:

- `ListUnspentWitness`: Returns a list of Setu `Coin` -> `Utxo`.
- `SendOutputs`/`CreateSimpleTx`: Builds Setu Events (Transfer/ChannelXXX).
- `PublishTransaction`: Submits a Setu Event.
- `GetTransactionDetails`: Queries execution status and confirmation height (Anchor depth) via EventId.

### 2.3 BlockChainIO (Chain Read Interface)

Setu Adaptation Strategy:

- `GetBestBlock`: Returns the latest Anchor (id, depth).
- `GetUtxo`: Queries whether an ObjectId exists, and constructs TxOut semantics.
- `GetBlock`: Wraps an Anchor + Event list into an equivalent `wire.MsgTx` structure (only used for LND's internal logic).

### 2.4 Signer + SecretKeyRing

Setu supports Secp256k1 / Ed25519 / Secp256r1.

Adaptation Strategy:

- LND internal signatures default to Secp256k1; maintain the use of Secp256k1.
- The Setu side can accept Secp256k1 as the channel signature format.
- Private key derivation path reuses the Setu `setu-keys` BIP32 path (coin type 99999).

### 2.5 Fee Estimator

Setu currently has no Gas mechanism. It uses fixed rates or simulates them through configuration.

Implementation Strategy:

- `EstimateFeePerKW`: Returns a fixed value (configurable) or an estimate based on historical Event size.
- `RelayFeePerKW`: Returns a fixed minimum value.

## 3. Channel Lifecycle Mapping

### 3.1 ChannelOpen

LND Logic:

- Bitcoin: Builds a 2-of-2 multisig funding tx, chain confirm.

Setu Logic:

- Creates a Channel SharedObject (ObjectId = ChannelID).
- EventType: `ChannelOpen` (new) + payload.

Data Flow:

1. LND calls `WalletController.SendOutputs` -> Generates a ChannelOpen Event.
2. Setu Validator/Runtime executes ChannelOpen, creating/updating the Channel Object.
3. Anchor Finalize -> LND `ChainNotifier` triggers confirmation.

### 3.2 ChannelClose

Setu Logic:

- EventType: `ChannelClose`.
- Updates Channel Object -> Closed.

Trigger:

- LND actively closes (cooperative close).
- LND passively monitors on-chain close events.

### 3.3 ChannelForceClose

Setu Logic:

- EventType: `ChannelForceClose`.
- Updates Channel Object -> Closing + Timeout status.

LND Logic:

- `contractcourt.ChannelArbitrator` triggers.
- Listens to Setu Events for subsequent HTLC Claims.

### 3.4 HTLCClaim / Timeout

Setu Logic:

- EventType: `HTLCClaim` / `HTLCTimeout`.
- Modifies the HTLC state in the Channel Object.

LND Logic:

- Modules like `htlcswitch` update state based on `ChainNotifier` callbacks.

### 3.5 Penalize

Setu Logic:

- EventType: `ChannelPenalize`.
- Deducts the breaching party's balance + updates Channel state.

## 4. Setu and LND Type Mapping

| LND Type              | Setu Internal Semantics | Description            |
| --------------------- | ----------------------- | ---------------------- |
| `wire.OutPoint.Hash`  | `ObjectId`              | 32-byte direct mapping |
| `wire.OutPoint.Index` | 0                       | Setu has no UTXO index |
| `btcutil.Amount`      | `u64`                   | Setu smallest unit     |
| `wire.MsgTx`          | Setu Event bytes        | Carries Event serialization |
| `chainhash.Hash`      | EventId/AnchorId        | 32B or hex variant     |

## 5. LND Adaptation Module Breakdown

### 5.1 Configuration Extension

Target Files:

- `config.go`
- `lncfg/` new `setu.go`
- `chainreg/setu_params.go`
- `chainreg/chainregistry.go`

Key points for refactoring:

- Add `SetuChainName = "setu"`.
- Add `Setu *lncfg.Chain` configuration.
- Add Setu network parameters (genesis, chain_id, subnet_id).

### 5.2 ChainControl Assembly

Target Files:

- `chainreg/chainregistry.go`

Key points for refactoring:

- `NewPartialChainControl` add `case "setu"`.
- Initialize Setu ChainNotifier / ChainSource / FeeEstimator.
- Construct Setu WalletController + KeyRing + Signer.

### 5.3 Wallet and Signer Adaptation

Target Files:

- `lnwallet/` new `setu_wallet.go`
- `input/` new `setu_signer.go`

Key points for refactoring:

- Implement Setu WalletController logic.
- Submit Events via Setu RPC.
- Wrap Setu KeyStore to adapt `SecretKeyRing`.

### 5.4 Channel & HTLC State Synchronization

Target Files:

- `contractcourt/`, `htlcswitch/`, `chanbackup/`

Key points for refactoring:

- Listen to Setu Events -> trigger local channel state changes.
- `ChainNotifier` provides channel-level Event callbacks.

## 6. Necessary Setu Side Refactoring Points (for LND Integration)

> Only the minimum increment depended on by LND is listed. For detailed data structures, see the Setu interaction document.

- `EventType` adds Lightning primitives:
  - `ChannelOpen`, `ChannelClose`, `ChannelForceClose`
  - `HTLCAdd`, `HTLCClaim`, `HTLCTimeout`
  - `ChannelPenalize`
- `RuntimeExecutor` adds execution logic.
- `StateStore` adds Channel Object persistence.
- `setu-rpc` adds Event query/subscription interfaces.

## 7. Testing and Verification Process

### 7.1 Unit Testing (Go)

- Setu WalletController Unit Tests:
  - Coin selection
  - SendOutputs -> Event payload
  - PublishTransaction -> RPC submit
- Setu ChainNotifier Unit Tests:
  - Anchor Finalized -> Confirmed callback
  - Channel Object state changes -> Spend callback

### 7.2 Unit Testing (Rust)

- Setu Runtime Executors:
  - ChannelOpen/Close/ForceClose/HTLCClaim/Timeout/penalize execution logic
  - StateChange generation and serialization
- Setu State Storage:
  - Channel Object access and version updates

### 7.3 Integration Testing

- 2 Node LND + Setu Validator
- Test Items:
  - Open channel -> Confirm
  - HTLC multi-hop payment
  - ForceClose + HTLC Timeout
  - Penalize flow

### 7.4 Regression Testing

- Bitcoin mode regression tests (ensure no impact):
  - Run existing LND itests (partial)
  - Verify `chain=bitcoin` path is unaffected

## 8. Risks and Key Assumptions

- Setu Runtime must support atomic updates of Channel Objects.
- Event confirmation semantics must align with LND's expected `numConfs`.
- Setu has no mempool semantics, so reasonable simulation is needed in `ChainNotifier`.
