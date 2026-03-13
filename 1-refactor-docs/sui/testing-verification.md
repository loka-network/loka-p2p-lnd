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

### 先决条件

- Go 1.25.5
- Sui CLI (已配置本地网络环境)
- 构建工具链（`make`）

### 1. 启动 Sui 本地网络

```sh
sui start --local-network
```

### 2. 部署 Move 合约

```sh
sui client publish --path <path_to_lightning_move_module>
```
记录生成的 `PackageID` 并更新 LND 配置文件。

### 3. 启动 LND（Sui 链）

```sh
./lnd-debug --configfile=<LND_CONFIG> --chain=sui --sui.active --sui.package_id=<PACKAGE_ID>
```

### 4. 建立通道与支付验证

1. 生成收款发票
```sh
./lncli-debug --chain=sui addinvoice --amt 1000
```

2. 使用发票支付
```sh
./lncli-debug --chain=sui payinvoice <PAYREQ>
```

3. 验证结果
- 检查 `lncli listinvoices` 状态为 settled。
- 在 Sui Explorer 或 CLI 中查询 `Channel` 对象状态, 验证余额扣减与版本更新。

## 常见问题与排查

- **Gas 不足**: 检查 LND 钱包账户是否有足够的 SUI 支付 Move Call 交易手续费。
- **Event 订阅失败**: 确保 Sui RPC 节点支持 WebSocket 订阅。
- **签名校验失败**: 确认 Move 合约中 `ecdsa_k1::secp256k1_verify` 使用的是压缩格式公钥。
