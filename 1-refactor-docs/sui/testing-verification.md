# 测试与验证文档

本文档定义 Sui/MoveVM 适配版 LND 的测试与验证策略，覆盖单元测试、合约测试与端到端测试。

## 目标与范围

- 验证 Sui 适配不会破坏既有 LND 行为。
- 验证 Move 合约在关键路径（通道建立、HTLC、支付结算、关闭通道）上的逻辑正确性。
- 覆盖 Go 适配层单测、Move 合约单测与 LND+Sui 端到端集成测试。

## 单元测试

### 1. Go 适配层测试

- 保持与上游 LND 一致的单测结构与命名。
- 重点覆盖：`suinotify/`, `suiwallet/`, `input/sui_channel.go`, `chainfee/sui_estimator`。
- 验证类型映射：`wire.OutPoint.Hash` 与 Sui `ObjectID` 的 1:1 映射。

运行方式:
```sh
make unit tags=sui
```

### 2. Move 合约测试

- 验证合约方法的权限控制、签名校验与状态转换。
- 重点覆盖：`open_channel` 的资金锁定, `htlc_claim` 的原像验证, `penalize` 的撤销逻辑。

运行方式 (在合约目录下):
```sh
sui move test
```

## 端到端测试（Sui + LND + 支付流程）

我们提供了一个自动化的集成测试脚本 `scripts/itest_sui.sh`，用于在本地模拟完整的双节点（Alice & Bob）支付流程。

### 先决条件

- Go 1.25.5
- Sui CLI (已配置本地网络环境，用于请求测试网水龙头)
- `jq` (用于解析 JSON 输出)
- 确保已编译 LND 二进制文件（运行 `make build` 生成 `lnd-debug` 和 `lncli-debug`）

### 运行自动化脚本（推荐）

您可以直接运行提供的集成测试脚本，完成端到端的通道建立与支付验证：

```sh
./scripts/itest_sui.sh
```

**该脚本的自动化流程包括：**
1. **清理环境**：清空上一轮的测试数据目录 `/tmp/lnd-sui-test/`。
2. **启动节点**：分别启动 Alice 和 Bob 两个 LND 节点，并附带 `--suinode.active` 和 `--suinode.devnet` 参数。
3. **资金准备**：为 Alice 生成一个新的 Sui 地址，并调用 `sui client faucet` 获取 Devnet 测试币。
4. **P2P 连接**：Alice 连接到 Bob 的闪电网络节点。
5. **建立通道**：Alice 向 Bob 发起 `openchannel` 请求，在 Sui 链上注册 Channel Object。
6. **执行支付**：Bob 创建一张收款发票，Alice 通过刚刚建立的通道完成支付 (`payinvoice`)。

### 分步手动验证 (参考)
如果您希望手动分步执行，可以参考 `scripts/itest_sui.sh` 中的命令流，依次启动节点并使用 `./lncli-debug --lnddir=...` 交互式命令进行验证。

## 常见问题与排查

- **Gas 不足**: 检查 LND 钱包账户是否有足够的 SUI 支付 Move Call 交易手续费。
- **Event 订阅失败**: 确保 Sui RPC 节点支持 WebSocket 订阅。
- **签名校验失败**: 确认 Move 合约中 `ecdsa_k1::secp256k1_verify` 使用的是压缩格式公钥。
