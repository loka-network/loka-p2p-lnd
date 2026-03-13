# Sui 与 LND 交互合约接口与数据结构说明

> 目标: 结合 Sui MoveVM 实现, 给出 LND 适配 Sui 所需的 Move 合约接口约定, 包含 Channel/HTLC/Event/同步流程。

## 1. Sui Move 合约数据结构 (lightning 模块)

### 1.1 Channel 共享对象 (Shared Object)

Sui 上的通道状态存储在 `Channel` 对象中。

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
    to_self_delay: u64,   // epoch 延迟
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

合约通过触发 Event 通知 LND 适配器。

```move
struct ChannelOpenEvent has copy, drop {
    channel_id: ID,
    party_a: address,
    party_b: address,
    capacity: u64,
}

struct ChannelSpendEvent has copy, drop {
    channel_id: ID,
    htlc_id: u64, // 0 表示通道级别 spend (close)
    spend_type: u8, // 0: COOP, 1: FORCE, 2: CLAIM, 3: TIMEOUT, 4: PENALIZE
}
```

## 2. Move 合约接口 (Entry Functions)

LND 适配器通过构建 Transaction 并调用以下 Entry 函数。

### 2.1 Channel 生命周期

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

### 2.2 HTLC 操作

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

### 2.3 惩罚

```move
public entry fun penalize(
    channel: &mut Channel,
    revocation_key: vector<u8>,
    ctx: &mut TxContext
);
```

## 3. LND 适配层语义映射

| Move 合约动作 / 事件        | LND 语义          | ChainNotifier 映射        |
| --------------------------- | ----------------- | ------------------------- |
| `open_channel` Finalized    | Funding Confirmed | RegisterConfirmationsNtfn |
| `ChannelSpendEvent (COOP)`  | Cooperative Close | RegisterSpendNtfn         |
| `ChannelSpendEvent (FORCE)` | Force Close       | RegisterSpendNtfn         |
| `htlc_claim` Finalized      | HTLC Settled      | RegisterSpendNtfn         |
| `htlc_timeout` Finalized    | HTLC Timeout      | RegisterSpendNtfn         |
| `penalize` Finalized        | Breach            | RegisterSpendNtfn         |

## 4. 状态同步流程

1. **发起请求**: LND 适配器调用 `sui-go-sdk` 构建 Move Call 交易并发送。
2. **执行与监听**:
   - LND 通过 Sui RPC 的 `subscribeEvent` 订阅 `ChannelOpenEvent` 和 `ChannelSpendEvent`。
   - `ChainNotifier` 解析 Event 中的 `channel_id` 和 `htlc_id` 并分发给对应的 Resolver。
3. **余额提取**:
   - 确认无争议后, 适配器调用合约将 `Channel` 对象中的余额提取回钱包账户。

## 5. 关键实现点检查

- **签名算法**: Move 合约内需使用 `sui::ecdsa_k1` 验证 secp256k1 签名。
- **时间参考**: 使用 `sui::clock` 或 `checkpoint` 作为 CSV/CLTV 的时间参考。
- **并发控制**: Sui 的 Shared Object 模型支持多方并发读写,适合通道状态更新。
