# LND 适配 Sui/MoveVM 改造详细文档

> 目标: 在不改动 LND 现有接口签名的前提下, 以适配器模式接入 Sui, 使用 MoveVM 合约支持通道生命周期与 HTLC 流程。
> 约束: 利用 Sui 成熟的 MoveVM 智能合约能力, 在 Sui 侧部署闪电网络合约以替代 Bitcoin 脚本逻辑。

## 1. 改造原则与总体路径

- 零侵入接口层: 不新增抽象层、不改 LND 既有接口签名, 仅在实现层新增 Sui 版本。
- 类型复用: 适配层内部尽量复用 Bitcoin/LND 类型, 并在边界做语义转换。
- 双链共存: Bitcoin 逻辑保持不变, 通过 `--chain=sui` 启动 Sui 后端。
- 端到端闭环: 需要链上 Move Event/Object State 与 LND 通道状态机同步, 以及完整测试验证。

## 2. 关键接口与 Sui 适配实现清单

### 2.1 ChainNotifier (链事件通知)

LND 依赖 `chainntnfs.ChainNotifier` 以订阅确认、花费、区块等事件。

适配策略:

- 将 Sui Checkpoint/Epoch 作为 “区块” 语义来源。
- 将 Sui Transaction Digest 作为 `TxID` 语义来源。
- 将 Sui Channel Object 的版本变更或 Move Event 触发模拟为 “花费”/“确认”。

需要实现:

- `RegisterConfirmationsNtfn`: 对 Move 合约调用的确认, 通过 Transaction Finalized 触发。
- `RegisterSpendNtfn`: 对 Channel Object 的状态变更 (如 close_channel 调用) 触发。
- `RegisterBlockEpochNtfn`: 对 Sui Checkpoint/Epoch 推进事件。

### 2.2 WalletController (钱包控制器)

LND 高度依赖 `lnwallet.WalletController` 进行 UTXO 选择、交易构造与签名。

Sui 适配策略:

- 以 Sui `Coin<SUI>` + `ObjectId` 作为 UTXO 替代。
- `wire.OutPoint.Hash` 内部存放 Sui `ObjectId` (32B) 并固定 `Index = 0`。
- `btcutil.Amount` 直接映射为 Sui 最小单位 (Mist, u64)。

关键方法映射:

- `ListUnspentWitness`: 返回 Sui `Coin` -> `Utxo` 列表。
- `SendOutputs`/`CreateSimpleTx`: 构建 Move Call 交易。
- `PublishTransaction`: 提交 Sui 交易。
- `GetTransactionDetails`: 通过 Digest 查询执行状态和确认高度。

### 2.3 BlockChainIO (链读接口)

Sui 适配策略:

- `GetBestBlock`: 返回最新 Checkpoint/Epoch。
- `GetUtxo`: 查询 ObjectID 对应的 Object 状态, 并构造 TxOut 语义。
- `GetBlock`: 将 Checkpoint + Transactions 列表包装为 `wire.MsgBlock` 等效结构。

### 2.4 Signer + SecretKeyRing

Sui 支持 Secp256k1 / Ed25519 / Secp256r1。

适配策略:

- LND 内部签名默认基于 Secp256k1, 维持使用 Secp256k1。
- Sui 接受 Secp256k1 签名进行交易授权。
- 私钥派生路径复用 Sui `sui-keys` BIP32 路径。

### 2.5 Fee Estimator

Sui 使用 Gas 机制。

实现策略:

- `EstimateFeePerKW`: 返回当前 Gas Price 转换后的 SatPerKWeight。
- `RelayFeePerKW`: 返回固定最小值。

## 3. 通道生命周期映射 (MoveVM 合约)

### 3.1 ChannelOpen

LND 逻辑:

- Bitcoin: 构建 2-of-2 多签 funding tx, chain confirm

Sui 逻辑:

- 调用 `lightning::open_channel` 创建 Channel SharedObject (ObjectId = ChannelID)
- 触发 `ChannelOpenEvent`

数据流:

1. LND 调用 `WalletController.SendOutputs` -> 生成 Move Call
2. Sui 执行合约, 创建 Channel Object
3. Transaction Finalize -> LND `ChainNotifier` 触发确认

### 3.2 ChannelClose

Sui 逻辑:

- 调用 `lightning::close_channel`
- 更新 Channel Object -> 状态标记为 Closed 并销毁/归档

触发:

- LND 主动调用合约进行协作关闭
- LND 监控链上 Move Event

### 3.3 ChannelForceClose

Sui 逻辑:

- 调用 `lightning::force_close`
- 更新 Channel Object -> 进入 Closing 状态并记录 `close_epoch`

LND 逻辑:

- `contractcourt.ChannelArbitrator` 触发
- 监听 Sui Move Event 进行后续 HTLC Claim

### 3.4 HTLCClaim / Timeout

Sui 逻辑:

- 调用 `lightning::htlc_claim` / `lightning::htlc_timeout`
- 修改 Channel Object 中关联的 HTLC 状态

LND 逻辑:

- `htlcswitch` 等模块根据 `ChainNotifier` (Move Event 回调) 更新状态

### 3.5 Penalize

Sui 逻辑:

- 调用 `lightning::penalize`
- 验证撤销证明, 转移全量余额

## 4. Sui 与 LND 类型映射

| LND 类型              | Sui 内部语义    | 说明                  |
| --------------------- | ---------------- | --------------------- |
| `wire.OutPoint.Hash`  | `ObjectId`       | 32 字节直接映射       |
| `wire.OutPoint.Index` | 0                | Sui 无 UTXO index    |
| `btcutil.Amount`      | `u64`            | Sui 最小单位 (Mist) |
| `wire.MsgTx`          | Move Call bytes | 用于承载 Move 调用序列化 |
| `chainhash.Hash`      | Transaction Digest | 32B 通用             |

## 5. LND 改造模块分解

### 5.1 配置扩展

目标文件:

- `config.go`
- `lncfg/` 新增 `sui.go`
- `chainreg/sui_params.go`
- `chainreg/chainregistry.go`

改造要点:

- 新增 `SuiChainName = "sui"`
- 新增 `Sui *lncfg.Chain` 配置
- 新增 Sui 网络参数 (package_id, gateway_rpc, etc.)

### 5.2 ChainControl 组装

目标文件:

- `chainreg/chainregistry.go`

改造要点:

- `NewPartialChainControl` 新增 `case "sui"`
- 初始化 Sui ChainNotifier / ChainSource / FeeEstimator
- 构造 Sui WalletController + KeyRing + Signer

### 5.3 Wallet 与 Signer 适配

目标文件:

- `lnwallet/` 新增 `sui_wallet.go`
- `input/` 新增 `sui_signer.go`

改造要点:

- 实现 Sui WalletController 逻辑 (调用 Sui Go SDK)
- 通过 Sui RPC 提交 Move Call
- 封装 Sui KeyStore 以适配 `SecretKeyRing`

### 5.4 Channel & HTLC 状态同步

目标文件:

- `contractcourt/`, `htlcswitch/`, `chanbackup/`

改造要点:

- 监听 Sui Move Event -> 触发本地通道状态变化
- `ChainNotifier` 提供合约级别 Event 回调

## 6. Sui 侧必要改造点 (Move 合约)

> 详见 Sui 闪电网络合约设计文档。

- 核心 Move 模块 `lightning`:
  - `struct Channel` (Shared Object)
  - `open_channel`, `close_channel`, `force_close`
  - `htlc_add`, `htlc_claim`, `htlc_timeout`
  - `penalize`
- Move Event 定义
- 状态存储与版本管理

## 7. 测试验证流程

### 7.1 单元测试 (Go)

- Sui WalletController 单测:
  - Mist selection
  - SendOutputs -> Move Call payload
  - PublishTransaction -> RPC submit
- Sui ChainNotifier 单测:
  - Transaction Finalized -> Confirmed 回调
  - Move Event -> Spend 回调

### 7.2 单元测试 (Move)

- Move 合约逻辑测试:
  - 签名验证
  - 时间锁逻辑
  - 余额分配

### 7.3 集成测试

- 2 节点 LND + Sui Local Network
- 测试项:
  - 开通道 -> 确认
  - HTLC 多跳支付
  - ForceClose + HTLC Timeout
  - Penalize 流程

### 7.4 回归验证

- Bitcoin 模式回归测试 (确保无影响)

## 8. 风险与关键假设

- Sui RPC 订阅的及时性。
- Move 合约中 Secp256k1 签名校验的 Gas 成本。
- ObjectID 与 OutPoint 映射的唯一性。
