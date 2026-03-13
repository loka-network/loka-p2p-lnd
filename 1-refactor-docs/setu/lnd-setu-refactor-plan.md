# LND 适配 Setu 改造详细文档

> 目标: 在不改动 LND 现有接口签名的前提下, 以适配器模式接入 Setu, 支持 Setu 通道生命周期与 HTLC 流程。
> 约束: Setu 当前无通用 VM, 需要在 Setu Runtime/Validator 侧新增硬编码 Lightning 原语。

## 1. 改造原则与总体路径

- 零侵入接口层: 不新增抽象层、不改 LND 既有接口签名, 仅在实现层新增 Setu 版本。
- 类型复用: 适配层内部尽量复用 Bitcoin/LND 类型, 并在边界做语义转换。
- 双链共存: Bitcoin 逻辑保持不变, 通过 `--chain=setu` 启动 Setu 后端。
- 端到端闭环: 需要链上 Event/State 与 LND 通道状态机同步, 以及完整测试验证。

## 2. 关键接口与 Setu 适配实现清单

### 2.1 ChainNotifier (链事件通知)

LND 依赖 `chainntnfs.ChainNotifier` 以订阅确认、花费、区块等事件。

适配策略:

- 将 Setu Event/Anchor 作为 “区块” 语义来源。
- 将 Setu EventId 作为 `TxID` 语义来源。
- 将 Setu Channel Object 的版本变更或状态变更模拟为 “花费”/“确认”。

需要实现:

- `RegisterConfirmationsNtfn`: 对 Setu EventType 的确认, 通过 Anchor Finalized 触发。
- `RegisterSpendNtfn`: 对 Channel Object 的强制关闭/处罚等状态变更触发。
- `RegisterBlockEpochNtfn`: 对 Anchor 事件广播 (Anchor = BlockEpoch)。

### 2.2 WalletController (钱包控制器)

LND 高度依赖 `lnwallet.WalletController` 进行 UTXO 选择、交易构造与签名。

Setu 适配策略:

- 以 Setu `Coin` + `ObjectId` 作为 UTXO 替代。
- `wire.OutPoint.Hash` 内部存放 Setu `ObjectId` (32B) 并固定 `Index = 0`。
- `btcutil.Amount` 直接映射为 Setu 最小单位 (u64)。

关键方法映射:

- `ListUnspentWitness`: 返回 Setu `Coin` -> `Utxo` 列表。
- `SendOutputs`/`CreateSimpleTx`: 构建 Setu Event (Transfer/ChannelXXX)。
- `PublishTransaction`: 提交 Setu Event。
- `GetTransactionDetails`: 通过 EventId 查询执行状态和确认高度 (Anchor depth)。

### 2.3 BlockChainIO (链读接口)

Setu 适配策略:

- `GetBestBlock`: 返回最新 Anchor (id, depth)。
- `GetUtxo`: 查询 ObjectId 是否存在, 并构造 TxOut 语义。
- `GetBlock`: 将 Anchor + Event 列表包装为 `wire.MsgTx` 等效结构 (只用于 LND 内部逻辑)。

### 2.4 Signer + SecretKeyRing

Setu 支持 Secp256k1 / Ed25519 / Secp256r1。

适配策略:

- LND 内部签名默认基于 Secp256k1, 维持使用 Secp256k1。
- Setu 端可接受 Secp256k1 作为通道签名格式。
- 私钥派生路径复用 Setu `setu-keys` BIP32 路径 (coin type 99999)。

### 2.5 Fee Estimator

Setu 目前无 Gas 机制, 使用固定费率或通过配置模拟。

实现策略:

- `EstimateFeePerKW`: 返回固定值 (configurable) 或基于历史 Event size 估算。
- `RelayFeePerKW`: 返回固定最小值。

## 3. 通道生命周期映射

### 3.1 ChannelOpen

LND 逻辑:

- Bitcoin: 构建 2-of-2 多签 funding tx, chain confirm

Setu 逻辑:

- 创建 Channel SharedObject (ObjectId = ChannelID)
- EventType: `ChannelOpen` (新增) + payload

数据流:

1. LND 调用 `WalletController.SendOutputs` -> 生成 ChannelOpen Event
2. Setu Validator/Runtime 执行 ChannelOpen, 创建/更新 Channel Object
3. Anchor Finalize -> LND `ChainNotifier` 触发确认

### 3.2 ChannelClose

Setu 逻辑:

- EventType: `ChannelClose`
- 更新 Channel Object -> Closed

触发:

- LND 主动关闭 (cooperative close)
- LND 被动监控链上 close event

### 3.3 ChannelForceClose

Setu 逻辑:

- EventType: `ChannelForceClose`
- 更新 Channel Object -> Closing + Timeout 状态

LND 逻辑:

- `contractcourt.ChannelArbitrator` 触发
- 监听 Setu Event 进行后续 HTLC Claim

### 3.4 HTLCClaim / Timeout

Setu 逻辑:

- EventType: `HTLCClaim` / `HTLCTimeout`
- 修改 Channel Object 中 HTLC 状态

LND 逻辑:

- `htlcswitch` 等模块根据 `ChainNotifier` 回调更新状态

### 3.5 Penalize

Setu 逻辑:

- EventType: `ChannelPenalize`
- 扣减违约方余额 + 更新 Channel 状态

## 4. Setu 与 LND 类型映射

| LND 类型              | Setu 内部语义    | 说明                  |
| --------------------- | ---------------- | --------------------- |
| `wire.OutPoint.Hash`  | `ObjectId`       | 32 字节直接映射       |
| `wire.OutPoint.Index` | 0                | Setu 无 UTXO index    |
| `btcutil.Amount`      | `u64`            | Setu 最小单位         |
| `wire.MsgTx`          | Setu Event bytes | 用于承载 Event 序列化 |
| `chainhash.Hash`      | EventId/AnchorId | 32B 或 hex 变体       |

## 5. LND 改造模块分解

### 5.1 配置扩展

目标文件:

- `config.go`
- `lncfg/` 新增 `setu.go`
- `chainreg/setu_params.go`
- `chainreg/chainregistry.go`

改造要点:

- 新增 `SetuChainName = "setu"`
- 新增 `Setu *lncfg.Chain` 配置
- 新增 Setu 网络参数 (genesis, chain_id, subnet_id)

### 5.2 ChainControl 组装

目标文件:

- `chainreg/chainregistry.go`

改造要点:

- `NewPartialChainControl` 新增 `case "setu"`
- 初始化 Setu ChainNotifier / ChainSource / FeeEstimator
- 构造 Setu WalletController + KeyRing + Signer

### 5.3 Wallet 与 Signer 适配

目标文件:

- `lnwallet/` 新增 `setu_wallet.go`
- `input/` 新增 `setu_signer.go`

改造要点:

- 实现 Setu WalletController 逻辑
- 通过 Setu RPC 提交 Event
- 封装 Setu KeyStore 以适配 `SecretKeyRing`

### 5.4 Channel & HTLC 状态同步

目标文件:

- `contractcourt/`, `htlcswitch/`, `chanbackup/`

改造要点:

- 监听 Setu Event -> 触发本地通道状态变化
- `ChainNotifier` 提供通道级别 Event 回调

## 6. Setu 侧必要改造点 (对接 LND)

> 仅列出 LND 依赖的最小增量, 详细数据结构见 Setu 交互文档。

- `EventType` 新增 Lightning 原语:
  - `ChannelOpen`, `ChannelClose`, `ChannelForceClose`
  - `HTLCAdd`, `HTLCClaim`, `HTLCTimeout`
  - `ChannelPenalize`
- `RuntimeExecutor` 新增执行逻辑
- `StateStore` 新增 Channel Object 持久化
- `setu-rpc` 新增 Event 查询/订阅接口

## 7. 测试验证流程

### 7.1 单元测试 (Go)

- Setu WalletController 单测:
  - coin selection
  - SendOutputs -> Event payload
  - PublishTransaction -> RPC submit
- Setu ChainNotifier 单测:
  - Anchor Finalized -> Confirmed 回调
  - Channel Object 状态变更 -> Spend 回调

### 7.2 单元测试 (Rust)

- Setu Runtime 执行器:
  - ChannelOpen/Close/ForceClose/HTLCClaim/Timeout/penalize 执行逻辑
  - StateChange 生成与序列化
- Setu 状态存储:
  - Channel Object 存取与版本更新

### 7.3 集成测试

- 2 节点 LND + Setu Validator
- 测试项:
  - 开通道 -> 确认
  - HTLC 多跳支付
  - ForceClose + HTLC Timeout
  - Penalize 流程

### 7.4 回归验证

- Bitcoin 模式回归测试 (确保无影响):
  - 运行现有 LND itest (部分)
  - 验证 chain=bitcoin 路径不受影响

## 8. 风险与关键假设

- Setu Runtime 需支持 Channel Object 原子更新。
- Event 确认语义必须与 LND 期望的 `numConfs` 对齐。
- Setu 无 mempool 语义, 需在 ChainNotifier 做合理模拟。
