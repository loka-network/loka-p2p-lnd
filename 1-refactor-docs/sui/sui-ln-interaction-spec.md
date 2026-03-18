# Sui and LND Interaction Contract Interface and Data Structure Specification

> Objective: In conjunction with the Sui MoveVM implementation, provide the Move contract interface conventions required for LND to adapt to Sui, including Channel/HTLC/Event/Synchronization flows.

## 1. Sui Move Contract Data Structures (lightning module)

### 1.1 Channel Shared Object

The channel state on Sui is stored in the `Channel` object.

```move
struct Channel has key {
    id: UID,
    party_a: address,
    party_b: address,
    balance_a: u64,
    balance_b: u64,
    pubkey_a: vector<u8>, // secp256k1 compressed
    pubkey_b: vector<u8>,
    status: u8,           // 0: OPEN, 1: CLOSING, 2: CLOSED
    state_num: u64,
    to_self_delay: u64,   // epoch delay
    close_epoch: u64,
    htlcs: Table<u64, HTLC>,
    revocation_key: Option<vector<u8>>,
}

struct HTLC has store, drop {
    htlc_id: u64,
    amount: u64,
    payment_hash: vector<u8>, // sha256
    expiry: u64,              // absolute epoch
    direction: u8,            // 0: A_to_B, 1: B_to_A
    status: u8,               // 0: PENDING, 1: CLAIMED, 2: TIMEOUT
}
```

### 1.2 Move Events

The contract notifies the LND adapter by triggering Events.

```move
struct ChannelOpenEvent has copy, drop {
    channel_id: ID,
    party_a: address,
    party_b: address,
    capacity: u64,
}

struct ChannelSpendEvent has copy, drop {
    channel_id: ID,
    htlc_id: u64, // 0 indicates channel-level spend (close)
    spend_type: u8, // 0: COOP, 1: FORCE, 2: CLAIM, 3: TIMEOUT, 4: PENALIZE
}
```

## 2. Move Contract Interfaces (Entry Functions)

The LND adapter builds Transactions and calls the following Entry functions.

### 2.1 Channel Lifecycle

```move
public entry fun open_channel(
    coin: Coin<SUI>,
    pubkey_a: vector<u8>,
    pubkey_b: vector<u8>,
    party_b: address,
    to_self_delay: u64,
    ctx: &mut TxContext
);

public entry fun close_channel(
    channel: &mut Channel,
    state_num: u64,
    balance_a: u64,
    balance_b: u64,
    sig_a: vector<u8>,
    sig_b: vector<u8>,
    ctx: &mut TxContext
);

public entry fun force_close(
    channel: &mut Channel,
    state_num: u64,
    commitment_sig: vector<u8>,
    ctx: &mut TxContext
);
```

### 2.2 HTLC Operations

```move
public entry fun htlc_claim(
    channel: &mut Channel,
    htlc_id: u64,
    preimage: vector<u8>,
    ctx: &mut TxContext
);

public entry fun htlc_timeout(
    channel: &mut Channel,
    htlc_id: u64,
    ctx: &mut TxContext
);
```

### 2.3 Penalty

```move
public entry fun penalize(
    channel: &mut Channel,
    revocation_key: vector<u8>,
    ctx: &mut TxContext
);
```

## 3. LND Adaptation Layer Semantic Mapping

| Move Contract Action / Event| LND Semantics     | ChainNotifier Mapping     |
| --------------------------- | ----------------- | ------------------------- |
| `open_channel` Finalized    | Funding Confirmed | RegisterConfirmationsNtfn |
| `ChannelSpendEvent (COOP)`  | Cooperative Close | RegisterSpendNtfn         |
| `ChannelSpendEvent (FORCE)` | Force Close       | RegisterSpendNtfn         |
| `htlc_claim` Finalized      | HTLC Settled      | RegisterSpendNtfn         |
| `htlc_timeout` Finalized    | HTLC Timeout      | RegisterSpendNtfn         |
| `penalize` Finalized        | Breach            | RegisterSpendNtfn         |

## 4. State Synchronization Flow

1. **Initiate Request**: The LND adapter calls the `sui-go-sdk` to build the Move Call transaction and send it.
2. **Execution and Listening**:
   - LND subscribes to `ChannelOpenEvent` and `ChannelSpendEvent` via the Sui RPC's `subscribeEvent`.
   - `ChainNotifier` parses the `channel_id` and `htlc_id` from the Event and dispatches them to the corresponding Resolver.
3. **Balance Extraction**:
   - After confirming there are no disputes, the adapter calls the contract to extract the balance in the `Channel` object back to the wallet account.

## 5. Key Implementation Checks

- **Signature Algorithm**: The Move contract must use `sui::ecdsa_k1` to verify secp256k1 signatures.
- **Time Reference**: Use `sui::clock` or `checkpoint` as the time reference for CSV/CLTV.
- **Concurrency Control**: Sui's Shared Object model supports multi-party concurrent read and write operations, making it suitable for channel state updates.
