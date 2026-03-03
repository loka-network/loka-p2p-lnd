# Setu 与 LND 交互数据结构与接口说明

> 目标: 结合 Setu 现有实现, 给出 LND 适配 Setu 所需的数据结构与接口约定, 包含 Event/State/Channel/HTLC/同步流程。

## 1. Setu 核心数据结构 (来自现有代码)

### 1.1 Event 结构

Setu 的链上变化以 Event 表达, 并最终被 Anchor Finalize。

现有定义来源:

- `types/src/event.rs`

关键字段:

- `Event { id, event_type, parent_ids, subnet_id, payload, vlc_snapshot, creator, status, execution_result, timestamp }`
- `EventType` 目前包含: Genesis, System, Transfer, ValidatorRegister/Unregister, SolverRegister/Unregister, SubnetRegister, UserRegister, PowerConsume, TaskSubmit
- `EventStatus`: Pending → InWorkQueue → Executed → Confirmed → Finalized → Failed

建议扩展:

- 新增 Lightning 相关 `EventType`:
  - `ChannelOpen`, `ChannelClose`, `ChannelForceClose`
  - `HTLCAdd`, `HTLCClaim`, `HTLCTimeout`
  - `ChannelPenalize`

### 1.2 Object / Coin

来源:

- `types/src/object.rs`
- `types/src/coin.rs`

关键点:

- `ObjectId` / `Address`: 32 字节
- `Object<T>`: 元数据 + data
- `Coin` = `Object<CoinData>`
- `CoinState` 为链上持久化格式 (BCS 形式)

### 1.3 VLC (向量逻辑时钟)

来源:

- `crates/setu-vlc/src/lib.rs`

字段:

- `VLCSnapshot { vector_clock, logical_time, physical_time }`

用途:

- LND 可将 `logical_time` 作为 Anchor/确认顺序参考

### 1.4 Runtime Executor

来源:

- `crates/setu-runtime/src/executor.rs`

当前执行能力:

- 仅支持 `Transfer` 与 `Query`
- Storage 通过 `StateStore` 读写 `Object<CoinData>`

需要新增:

- Channel 与 HTLC 的执行逻辑 (见下文)

## 2. LND 需要的 Setu 交互动作

LND 对链层的期望来自几个核心接口:

- `ChainNotifier` (确认/花费/区块)
- `WalletController` (交易构造与发送)
- `BlockChainIO` (链查询)

在 Setu 侧需要实现的动作抽象:

### 2.1 State 同步

LND 需要:

- 通道状态 (Open/Closing/Closed)
- HTLC 状态 (Pending/Claimed/Timeout)

Setu 侧建议:

- 使用 Channel SharedObject 存储通道状态
- 每次 Event 执行更新 Channel Object 版本与 digest

同步方式:

- Anchor Finalized 时, LND 获取 Channel Object 最新状态
- Event 级别回调由 Setu RPC 推送 (订阅或轮询)

### 2.2 Channel Open/Close/ForceClose

需要的 Event Payload 结构如下 (建议):

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

执行结果:

- 更新 Channel Object 状态字段
- 生成 `ExecutionResult.state_changes`

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

## 3. Channel Object 建议结构

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

## 4. Setu RPC 交互扩展建议

现有 RPC (setu-rpc/src/messages.rs) 尚未提供 Event 提交/订阅 API。
建议扩展:

- `SubmitEventRequest { event: Event }`
- `GetEventRequest { event_id }`
- `GetObjectRequest { object_id }`
- `SubscribeEventStream { filter }`

LND 适配器需要:

- 提交 Channel/HTLC Event
- 轮询或订阅 EventStatus
- 查询 Channel Object 状态

## 5. Event -> LND 语义映射

| Setu 事件                   | LND 语义          | ChainNotifier 映射        |
| --------------------------- | ----------------- | ------------------------- |
| ChannelOpen Finalized       | Funding Confirmed | RegisterConfirmationsNtfn |
| ChannelClose Finalized      | Cooperative Close | RegisterSpendNtfn         |
| ChannelForceClose Finalized | Force Close       | RegisterSpendNtfn         |
| HTLCClaim Finalized         | HTLC Settled      | RegisterSpendNtfn         |
| HTLCTimeout Finalized       | HTLC Timeout      | RegisterSpendNtfn         |
| ChannelPenalize Finalized   | Breach            | RegisterSpendNtfn         |

## 6. 状态同步流程

1. LND 提交 Event
2. Setu 执行 -> EventStatus: Executed
3. Anchor Finalized -> EventStatus: Finalized
4. LND 通过 RPC 获取 EventStatus + Channel Object 状态
5. ChainNotifier 触发对应回调

## 7. 关键实现点检查

- Event `subnet_id` 路由必须正确 (ROOT/App Subnet)
- Channel Object 需使用 SharedObject
- 状态变更必须写入 Merkle 兼容格式

## 8. 测试建议 (Setu 侧)

- ChannelOpen/Close/ForceClose 执行器单测
- HTLCAdd/Claim/Timeout 状态机单测
- Event/Anchor Finalize 流程与 LND 通知集成测试
