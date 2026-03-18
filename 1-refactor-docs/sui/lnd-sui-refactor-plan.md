# Detailed Refactoring Plan for LND Adaptation to Sui/MoveVM

> Objective: Integrate Sui using an adapter pattern without modifying LND's existing interface signatures, and use MoveVM contracts to support channel lifecycles and HTLC processes.
> Constraints: Utilize Sui's mature MoveVM smart contract capabilities to deploy Lightning Network contracts on the Sui side to replace Bitcoin script logic.

## 1. Refactoring Principles and Overall Approach

- Zero-intrusion at the interface layer: Do not add new abstraction layers and do not change existing LND interface signatures; only add the Sui version at the implementation layer.
- Type reuse: Reuse Bitcoin/LND types internally within the adapter layer as much as possible, and perform semantic conversions at the boundaries.
- Dual-chain coexistence: Keep Bitcoin logic unchanged, and start the Sui backend via `--chain=sui`.
- End-to-end closed loop: Requires synchronization between on-chain Move Event/Object State and the LND channel state machine, as well as comprehensive test validation.

## 2. Key Interfaces and Sui Adaptation Implementation List

### 2.1 ChainNotifier (Chain Event Notification)

LND relies on `chainntnfs.ChainNotifier` to subscribe to confirmations, spends, blocks, and other events.

Adaptation Strategy:

- Use Sui Checkpoint/Epoch as the "block" semantic source.
- Use Sui Transaction Digest as the `TxID` semantic source.
- Simulate the version changes of Sui Channel Objects or the triggering of Move Events as "spends"/"confirmations".

Needs to be implemented:

- `RegisterConfirmationsNtfn`: For confirmation of Move contract calls, triggered via Transaction Finalized.
- `RegisterSpendNtfn`: Triggered by state changes of the Channel Object (e.g., `close_channel` call).
- `RegisterBlockEpochNtfn`: For Sui Checkpoint/Epoch advancement events.

### 2.2 WalletController (Wallet Controller)

LND heavily relies on `lnwallet.WalletController` for UTXO selection, transaction construction, and signing.

Sui Adaptation Strategy:

- Use Sui `Coin<SUI>` + `ObjectId` as a UTXO replacement.
- `wire.OutPoint.Hash` internally stores the Sui `ObjectId` (32B) with a fixed `Index = 0`.
- `btcutil.Amount` maps directly to the Sui's smallest unit (Mist, u64).

Key method mapping:

- `ListUnspentWitness`: Returns a list of Sui `Coin` -> `Utxo`.
- `SendOutputs`/`CreateSimpleTx`: Builds Move Call transactions.
- `PublishTransaction`: Submits Sui transactions.
- `GetTransactionDetails`: Queries execution status and confirmation height via Digest.

### 2.3 BlockChainIO (Chain Read Interface)

Sui Adaptation Strategy:

- `GetBestBlock`: Returns the latest Checkpoint/Epoch.
- `GetUtxo`: Queries the Object state corresponding to an ObjectID, and constructs TxOut semantics.
- `GetBlock`: Wraps the Checkpoint + Transactions list into an equivalent `wire.MsgBlock` structure.

### 2.4 Signer + SecretKeyRing

Sui supports Secp256k1 / Ed25519 / Secp256r1.

Adaptation Strategy:

- LND internal signatures default to Secp256k1; maintain the use of Secp256k1.
- Sui accepts Secp256k1 signatures for transaction authorization.
- Private key derivation path reuses the Sui `sui-keys` BIP32 path.

### 2.5 Fee Estimator

Sui uses a Gas mechanism.

Implementation Strategy:

- `EstimateFeePerKW`: Returns SatPerKWeight converted from the current Gas Price.
- `RelayFeePerKW`: Returns a fixed minimum value.

## 3. Channel Lifecycle Mapping (MoveVM Contract)

### 3.1 ChannelOpen

LND Logic:

- Bitcoin: Builds a 2-of-2 multisig funding tx, chain confirm.

Sui Logic:

- Calls `lightning::open_channel` to create a Channel SharedObject (ObjectId = ChannelID).
- Triggers `ChannelOpenEvent`.

Data Flow:

1. LND calls `WalletController.SendOutputs` -> Generates Move Call.
2. Sui executes the contract, creating the Channel Object.
3. Transaction Finalize -> LND `ChainNotifier` triggers confirmation.

### 3.2 ChannelClose

Sui Logic:

- Calls `lightning::close_channel`.
- Updates Channel Object -> Status marked as Closed and destroyed/archived.

Trigger:

- LND actively calls the contract for cooperative closure.
- LND monitors on-chain Move Events.

### 3.3 ChannelForceClose

Sui Logic:

- Calls `lightning::force_close`.
- Updates Channel Object -> Enters Closing state and records `close_epoch`.

LND Logic:

- `contractcourt.ChannelArbitrator` triggers.
- Listens to Sui Move Events for subsequent HTLC Claims.

### 3.4 HTLCClaim / Timeout

Sui Logic:

- Calls `lightning::htlc_claim` / `lightning::htlc_timeout`.
- Modifies the associated HTLC state in the Channel Object.

LND Logic:

- Modules like `htlcswitch` update state based on `ChainNotifier` (Move Event callbacks).

### 3.5 Penalize

Sui Logic:

- Calls `lightning::penalize`.
- Validates the revocation proof, transfers the full balance.

## 4. Sui and LND Type Mapping

| LND Type              | Sui Internal Semantics | Description            |
| --------------------- | ---------------------- | ---------------------- |
| `wire.OutPoint.Hash`  | `ObjectId`             | 32-byte direct mapping |
| `wire.OutPoint.Index` | 0                      | Sui has no UTXO index  |
| `btcutil.Amount`      | `u64`                  | Sui smallest unit (Mist)|
| `wire.MsgTx`          | Move Call bytes        | Carries Move call serialization |
| `chainhash.Hash`      | Transaction Digest     | 32B general mechanism  |

## 5. LND Adaptation Module Breakdown

### 5.1 Configuration Extension

Target Files:

- `config.go`
- `lncfg/` new `sui.go`
- `chainreg/sui_params.go`
- `chainreg/chainregistry.go`

Key points for refactoring:

- Add `SuiChainName = "sui"`.
- Add `Sui *lncfg.Chain` configuration.
- Add Sui network parameters (package_id, gateway_rpc, etc.).

### 5.2 ChainControl Assembly

Target Files:

- `chainreg/chainregistry.go`

Key points for refactoring:

- `NewPartialChainControl` add `case "sui"`.
- Initialize Sui ChainNotifier / ChainSource / FeeEstimator.
- Construct Sui WalletController + KeyRing + Signer.

### 5.3 Wallet and Signer Adaptation

Target Files:

- `lnwallet/` new `sui_wallet.go`
- `input/` new `sui_signer.go`

Key points for refactoring:

- Implement Sui WalletController logic (call Sui Go SDK).
- Submit Move Call via Sui RPC.
- Wrap Sui KeyStore to adapt `SecretKeyRing`.

### 5.4 Channel & HTLC State Synchronization

Target Files:

- `contractcourt/`, `htlcswitch/`, `chanbackup/`

Key points for refactoring:

- Listen to Sui Move Events -> trigger local channel state changes.
- `ChainNotifier` provides contract-level Event callbacks.

## 6. Necessary Sui Side Refactoring Points (Move Contract)

> See the Sui Lightning Network Contract Design Document for details.

- Core Move Module `lightning`:
    - `struct Channel` (Shared Object)
    - `open_channel`, `close_channel`, `force_close`
    - `htlc_add`, `htlc_claim`, `htlc_timeout`
    - `penalize`
- Move Event definition
- State storage and version management

## 7. Testing and Verification Process

### 7.1 Unit Testing (Go)

- Sui WalletController Unit Tests:
    - Mist selection
    - SendOutputs -> Move Call payload
    - PublishTransaction -> RPC submit
- Sui ChainNotifier Unit Tests:
    - Transaction Finalized -> Confirmed callback
    - Move Event -> Spend callback

### 7.2 Unit Testing (Move)

- Move contract logic tests:
    - Signature validation
    - Timelock logic
    - Balance allocation

### 7.3 Integration Testing

- 2 Node LND + Sui Local Network
- Test Items:
    - Open channel -> Confirm
    - HTLC multi-hop payment
    - ForceClose + HTLC Timeout
    - Penalize flow

### 7.4 Regression Testing

- Bitcoin mode regression tests (ensure no impact).

## 8. Risks and Key Assumptions

- Timeliness of Sui RPC subscriptions.
- Gas cost of Secp256k1 signature validation in Move contracts.
- Uniqueness of mapping between ObjectID and OutPoint.
