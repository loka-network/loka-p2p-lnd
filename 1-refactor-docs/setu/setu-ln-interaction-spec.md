# Setu and LND Interaction Data Structure and Interface Specification

> Objective: In conjunction with the existing Setu implementation, provide the data structures and interface conventions required for LND to adapt to Setu, including Event/State/Channel/HTLC/Synchronization flows.

## 1. Setu Core Data Structures (From Existing Code)

### 1.1 Event Structure

On-chain changes in Setu are expressed as Events and are ultimately Finalized by Anchors.

Existing definition source:

- `types/src/event.rs`

Key fields:

- `Event { id, event_type, parent_ids, subnet_id, payload, vlc_snapshot, creator, status, execution_result, timestamp }`
- `EventType` currently includes: Genesis, System, Transfer, ValidatorRegister/Unregister, SolverRegister/Unregister, SubnetRegister, UserRegister, PowerConsume, TaskSubmit
- `EventStatus`: Pending → InWorkQueue → Executed → Confirmed → Finalized → Failed

Suggested extensions:

- Add Lightning-related `EventType`s:
  - `ChannelOpen`, `ChannelClose`, `ChannelForceClose`
  - `HTLCAdd`, `HTLCClaim`, `HTLCTimeout`
  - `ChannelPenalize`

### 1.2 Object / Coin

Source:

- `types/src/object.rs`
- `types/src/coin.rs`

Key points:

- `ObjectId` / `Address`: 32 bytes
- `Object<T>`: Metadata + data
- `Coin` = `Object<CoinData>`
- `CoinState` is the on-chain persistent format (BCS format)

### 1.3 VLC (Vector Logical Clock)

Source:

- `crates/setu-vlc/src/lib.rs`

Fields:

- `VLCSnapshot { vector_clock, logical_time, physical_time }`

Usage:

- LND can use `logical_time` as a reference for Anchor/confirmation ordering

### 1.4 Runtime Executor

Source:

- `crates/setu-runtime/src/executor.rs`

Current execution capabilities:

- Only supports `Transfer` and `Query`
- Storage reads and writes `Object<CoinData>` via `StateStore`

Needs to be added:

- Channel and HTLC execution logic (see below)

## 2. Setu Interaction Actions Required by LND

LND's expectations for the chain layer come from several core interfaces:

- `ChainNotifier` (Confirmations/Spends/Blocks)
- `WalletController` (Transaction construction and sending)
- `BlockChainIO` (Chain querying)

Action abstractions that need to be implemented on the Setu side:

### 2.1 State Synchronization

LND needs:

- Channel state (Open/Closing/Closed)
- HTLC state (Pending/Claimed/Timeout)

Setu side suggestions:

- Use a Channel SharedObject to store the channel state
- Each Event execution updates the Channel Object version and digest

Synchronization method:

- When an Anchor is Finalized, LND gets the latest state of the Channel Object
- Event-level callbacks are pushed by Setu RPC (subscription or polling)

### 2.2 Channel Open/Close/ForceClose

The required Event Payload structures are as follows (suggested):

```
ChannelOpenPayload {
  channel_id: ObjectId,
  party_a: Address,
  party_b: Address,
  capacity: u64,
  funding_tx: Vec<u8>,
  commitment_a: Vec<u8>,
  commitment_b: Vec<u8>,
  open_time: u64,
  subnet_id: Option<SubnetId>
}

ChannelClosePayload {
  channel_id: ObjectId,
  close_type: "cooperative" | "local" | "remote" | "breach",
  final_balance_a: u64,
  final_balance_b: u64,
  close_time: u64
}

ChannelForceClosePayload {
  channel_id: ObjectId,
  initiator: Address,
  commitment_tx: Vec<u8>,
  close_time: u64
}
```

Execution results:

- Update the status fields of the Channel Object
- Generate `ExecutionResult.state_changes`

### 2.3 HTLC Add / Claim / Timeout

```
HTLCAddPayload {
  channel_id: ObjectId,
  htlc_id: u64,
  amount: u64,
  hash_lock: [u8; 32],
  expiry_height: u64,
  sender: Address,
  receiver: Address
}

HTLCClaimPayload {
  channel_id: ObjectId,
  htlc_id: u64,
  preimage: [u8; 32],
  claimer: Address,
  claim_time: u64
}

HTLCTimeoutPayload {
  channel_id: ObjectId,
  htlc_id: u64,
  timeout_height: u64,
  timeout_time: u64
}
```

### 2.4 Penalize

```
ChannelPenalizePayload {
  channel_id: ObjectId,
  offender: Address,
  penalty_amount: u64,
  evidence_tx: Vec<u8>,
  time: u64
}
```

## 3. Recommended Channel Object Structure

```
ChannelObjectData {
  channel_id: ObjectId,
  party_a: Address,
  party_b: Address,
  balance_a: u64,
  balance_b: u64,
  status: "Opening" | "Open" | "Closing" | "Closed",
  htlcs: Vec<HTLCEntry>,
  latest_commitment_hash: [u8; 32],
  version: u64,
  last_update_time: u64
}

HTLCEntry {
  htlc_id: u64,
  amount: u64,
  hash_lock: [u8; 32],
  expiry_height: u64,
  status: "Pending" | "Claimed" | "Timeout",
  sender: Address,
  receiver: Address
}
```

## 4. Setu RPC Interaction Extension Suggestions

The existing RPC (`setu-rpc/src/messages.rs`) does not yet provide an Event submission/subscription API.
Suggested extensions:

- `SubmitEventRequest { event: Event }`
- `GetEventRequest { event_id }`
- `GetObjectRequest { object_id }`
- `SubscribeEventStream { filter }`

The LND adapter needs to:

- Submit Channel/HTLC Events
- Poll or subscribe to EventStatus
- Query the Channel Object state

## 5. Event -> LND Semantic Mapping

| Setu Event                  | LND Semantics     | ChainNotifier Mapping     |
| --------------------------- | ----------------- | ------------------------- |
| ChannelOpen Finalized       | Funding Confirmed | RegisterConfirmationsNtfn |
| ChannelClose Finalized      | Cooperative Close | RegisterSpendNtfn         |
| ChannelForceClose Finalized | Force Close       | RegisterSpendNtfn         |
| HTLCClaim Finalized         | HTLC Settled      | RegisterSpendNtfn         |
| HTLCTimeout Finalized       | HTLC Timeout      | RegisterSpendNtfn         |
| ChannelPenalize Finalized   | Breach            | RegisterSpendNtfn         |

## 6. State Synchronization Flow

1. LND submits the Event
2. Setu executes -> EventStatus: Executed
3. Anchor Finalized -> EventStatus: Finalized
4. LND fetches the EventStatus + Channel Object state via RPC
5. ChainNotifier triggers the corresponding callback

## 7. Key Implementation Checks

- Event `subnet_id` routing must be correct (ROOT/App Subnet)
- The Channel Object must use SharedObject
- State changes must be written in a Merkle-compatible format

## 8. Testing Suggestions (Setu Side)

- Unit tests for ChannelOpen/Close/ForceClose executors
- Unit tests for HTLCAdd/Claim/Timeout state machines
- Integration tests for Event/Anchor Finalize flow and LND notifications
