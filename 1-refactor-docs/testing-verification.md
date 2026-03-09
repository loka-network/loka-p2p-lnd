# 测试与验证文档

本文档定义 Setu 适配版 LND 的测试与验证策略，覆盖单元测试与端到端测试（从启动 Setu、LND 到完成一次完整支付流程）。

## 目标与范围

- 验证 Setu 适配不会破坏既有 LND 行为。
- 验证 Setu 新增链路在关键路径（通道建立、HTLC、支付结算、关闭通道）上的正确性。
- 覆盖单元测试与端到端测试，形成可重复执行的流程。

## 单元测试

### 基础要求

- 保持与上游 LND 一致的单测结构与命名。
- 新增/修改的 Setu 适配逻辑必须有单元测试覆盖。
- 所有新导出的符号保持已有 GoDoc 约定。

### 运行方式

```sh
# 根模块单元测试（需要 btcd 二进制）
make unit

# 所有子模块单元测试（actor/, fn/, tools/）
make unit-module
```

### 覆盖建议

- 适配层：`setunotify/`, `setuwallet/`, `input/setu_channel.go`, `chainfee/setu_estimator`。
- 关键接口契约：`chainntnfs/interface.go`, `lnwallet/interface.go`, `input/signer.go`, `chainreg/chainregistry.go`。
- 类型映射：`wire.OutPoint.Hash` 与 Setu `ObjectID` 的映射，以及 `wire.MsgTx` 与 Setu Event 序列化的映射。

### 推荐用例模板

- 正常路径：创建、查询、释放等操作返回预期结果。
- 错误路径：输入非法参数、链路不可达、事件类型不支持等场景。
- 边界条件：空值、最大/最小值、重复请求等。

## 端到端测试（Setu + LND + 支付流程）

> 说明：以下步骤以通用命令占位符描述，请根据 Setu 运行环境与二进制名称替换。

### 先决条件

- Go 1.25.5
- Setu 运行环境与二进制
- 构建工具链（`make`）

### 构建 LND

```sh
make build
```

### 启动 Setu

- 启动本地 Setu 节点/网络（示例）：

```sh
<SETU_BIN> start --config <SETU_CONFIG>
```

- 等待节点进入可用状态，确认 RPC/HTTP 端口可用。

### 启动 LND（Setu 链）

```sh
./lnd-debug --configfile=<LND_CONFIG> --chain=setu
```

关键配置建议：

- 指定 Setu 适配器所需的 RPC/HTTP 地址与认证。
- 配置数据库与日志目录。

### 初始化钱包

- 创建钱包并解锁：

```sh
./lncli-debug --chain=setu create
./lncli-debug --chain=setu unlock
```

### 建立通道（如需要）

- 连接对端并打开通道（示例占位）：

```sh
./lncli-debug --chain=setu connect <PUBKEY>@<HOST>
./lncli-debug --chain=setu openchannel --node_key <PUBKEY> --local_amt <AMOUNT>
```

- 等待通道进入可用状态：

```sh
./lncli-debug --chain=setu listchannels
```

### 发起支付流程

1. 生成收款发票

```sh
./lncli-debug --chain=setu addinvoice --amt <AMOUNT>
```

2. 使用发票支付

```sh
./lncli-debug --chain=setu payinvoice <PAYREQ>
```

3. 验证支付成功

```sh
./lncli-debug --chain=setu listinvoices
./lncli-debug --chain=setu listpayments
```

### 关闭通道（可选）

```sh
./lncli-debug --chain=setu closechannel --funding_txid <TXID> --output_index <INDEX>
```

### 结果验收

- 发票状态为 settled。
- 支付记录为 succeeded。
- 通道状态正确更新（active/closing/closed）。
- Setu 事件链上可追踪对应的 `ChannelOpen`、`HTLCClaim`、`ChannelClose` 事件。

## 常见问题与排查

- RPC 连接失败：检查 Setu RPC/HTTP 地址与认证配置。
- 通道长时间 pending：检查 Setu 节点共识状态与事件落账。
- 支付失败：查看 LND 与 Setu 日志，确认 HTLC 事件与签名流程。

## 变更记录

- 创建本文档：补充单元测试与端到端测试流程。
