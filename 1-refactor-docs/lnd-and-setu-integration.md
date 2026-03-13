# 计划：闪电网络适配 Sui/MoveVM — 改造重构文档

## 0. Overview

将 LND 闪电网络改造为同时支持 **Bitcoin + Sui (MoveVM) 双系统**。利用 Sui 成熟的 MoveVM 智能合约能力实现闪电网络核心逻辑，通道以 32 字节 `ObjectID` 标识。

> **背景与策略调整**：为了实现并行开发，在 Sui 通用虚拟机（MoveVM）完全集成前，先行将 LND 集成到 **Sui 网络**。原本在 Bitcoin 上的闪电网络脚本逻辑（如多签、HTLC、时间锁、违约惩罚）将直接通过 **MoveVM 合约** 实现。
>
> **适配策略**：从"在 Setu 侧新增硬编码原语"调整为**"在 Sui 侧部署 Move 闪电网络合约"**。LND 通过适配器调用 Sui 上的 Move 合约来执行通道生命周期管理（`open_channel`、`close_channel`、`force_close`、`htlc_claim`、`penalize` 等）。

改造策略：**零侵入适配器模式（Adapter Pattern）** 。不新增抽象层、不改现有接口签名，而是在接口实现层做 Sui/MoveVM 适配器 —— 适配器内部复用 Bitcoin 类型（如 `wire.OutPoint.Hash` 存储 ObjectID、`btcutil.Amount` 做单位映射、`wire.MsgTx` 承载 Move Call 序列化字节），在实现边界做语义转换。现有 Bitcoin 代码路径完全不受影响，Sui 作为新的 `ChainControl` 实现插入，通过 `lncli --chain=sui` 选择。

核心改造工作量分布：**lnd 针对 Sui/MoveVM 的后端适配器实现（35%）→ Sui 侧 Move 闪电网络合约实现（20%）→ 上层模块扩展（25%）→ 配置/启动/测试集成（20%）**。

---

## 1. 流程交互图

如下 8 张图分别覆盖了：

1. **架构总览** — 双链抽象层分层与模块关系
2. **通道生命周期对比** — Bitcoin vs Sui (MoveVM) 的流程差异一目了然
3. **开通道序列** — 详细的双方+链交互时序
4. **多跳 HTLC 支付** — 正常流转与异常超时的完整序列
5. **强制关闭与争议解决** — 含违约惩罚的完整决策流程
6. **Bitcoin Script → MoveVM 合约方法映射** — 每个 Bitcoin 合约操作如何翻译为 Move 合约调用
7. **改造阶段依赖** — 5 个 Phase 的执行顺序与依赖
8. **链上/链下数据流全景** — 完整的通道生命周期交互泳道

### 1. 适配器模式双链架构总览

```mermaid
graph TB
    subgraph "LND 应用层（不改动）"
        RPC["RPC Server<br/>lnrpc/"]
        Router["路由引擎<br/>routing/"]
        Switch["HTLC Switch<br/>htlcswitch/"]
        Invoice["发票管理<br/>invoices/"]
        Funding["资金管理器<br/>funding/"]
        ChanSM["通道状态机<br/>lnwallet/channel.go"]
        ContractCourt["合约裁决<br/>contractcourt/"]
        Graph["图构建<br/>graph/"]
        Discovery["Gossip 发现<br/>discovery/"]
    end

    subgraph "现有接口（不改签名）"
        IF_Notify["ChainNotifier<br/>chainntnfs/interface.go"]
        IF_Wallet["WalletController<br/>lnwallet/interface.go"]
        IF_Signer["Signer<br/>input/signer.go"]
        IF_IO["BlockChainIO<br/>chainntnfs/interface.go"]
        CC["ChainControl<br/>chainreg/chainregistry.go"]
    end

    subgraph "Bitcoin 后端（不改动）"
        BTC_Notify["bitcoindnotify/<br/>btcdnotify/<br/>neutrinonotify/"]
        BTC_Wallet["btcwallet/<br/>WalletController 实现"]
        BTC_Script["input/script_utils.go<br/>Bitcoin Script 构建"]
        BTC_Fee["chainfee/<br/>SatPerKWeight"]
        BTC_Chain["Bitcoin 区块链<br/>UTXO 模型"]
    end

    subgraph "Sui/MoveVM 适配器（新增）"
        SUI_Notify["suinotify/<br/>Move Event/Object 订阅<br/>（实现 ChainNotifier）"]
        SUI_Wallet["suiwallet/<br/>Move 合约调用<br/>（实现 WalletController）"]
        SUI_Program["input/sui_channel.go<br/>Move 合约调用封装"]
        SUI_Fee["chainfee/sui_estimator<br/>GasPrice→SatPerKWeight"]
        SUI_Chain["Sui 网络<br/>MoveVM 合约<br/>实现闪电网络脚本逻辑"]
        SUI_Adapt["类型映射策略<br/>OutPoint.Hash←ObjectID<br/>Amount←SuiUnit<br/>MsgTx←Move Call bytes"]
    end

    RPC --> CC
    Router --> Switch
    Switch --> ChanSM
    Funding --> CC
    ChanSM --> CC
    ContractCourt --> CC
    Graph --> CC
    Discovery --> CC

    CC --> IF_Notify & IF_Wallet & IF_Signer & IF_IO

    IF_Notify -->|"chain=bitcoin"| BTC_Notify
    IF_Wallet -->|"chain=bitcoin"| BTC_Wallet
    IF_Notify -->|"chain=sui"| SUI_Notify
    IF_Wallet -->|"chain=sui"| SUI_Wallet

    BTC_Notify --> BTC_Chain
    BTC_Wallet --> BTC_Chain
    SUI_Notify --> SUI_Chain
    SUI_Wallet --> SUI_Chain

    SUI_Adapt -.->|"内部复用"| SUI_Notify
    SUI_Adapt -.->|"内部复用"| SUI_Wallet
    IF_Wallet --- SUI_Adapt


    style IF_Notify fill:#4a9eff,color:#fff
    style IF_Wallet fill:#4a9eff,color:#fff
    style IF_Signer fill:#4a9eff,color:#fff
    style IF_IO fill:#4a9eff,color:#fff
    style CC fill:#4a9eff,color:#fff
    style SUI_Notify fill:#2ecc71,color:#fff
    style SUI_Wallet fill:#2ecc71,color:#fff
    style SUI_Program fill:#2ecc71,color:#fff
    style SUI_Fee fill:#2ecc71,color:#fff
    style SUI_Chain fill:#2ecc71,color:#fff
    style SUI_Adapt fill:#9b59b6,color:#fff
    style BTC_Chain fill:#f39c12,color:#fff

    %% 隐藏最后两条边（强制布局边）
    linkStyle 22 stroke-width:0, stroke:transparent;
```

---

### 2. 通道生命周期对比（Bitcoin vs Sui）

- 图1：Bitcoin 闪电网络通道生命周期

```mermaid
flowchart LR
    B1["选择 UTXO<br/>coin selection"] --> B2["构建 2-of-2<br/>多签资金 TX"]
    B2 --> B3["广播 TX<br/>等待 3-6 次确认<br/>⏱ 30~60 min"]
    B3 --> B4["生成 ShortChanID<br/>block:tx:output"]
    B4 --> B5["交换承诺交易<br/>wire.MsgTx 签名"]
    B5 --> B6["HTLC 更新<br/>Script 条件分支"]
    B6 --> B7{"关闭方式"}
    B7 -->|协作| B8["构建关闭 TX<br/>双方签名广播"]
    B7 -->|强制| B9["广播承诺 TX<br/>CSV 延迟等待"]
    B9 --> B10["Sweep UTXO<br/>扫回钱包"]
    B7 -->|违约| B11["构建正义 TX<br/>撤销密钥花费"]

    style B3 fill:#e74c3c,color:#fff
    style B10 fill:#e67e22,color:#fff
```

- 图2：Sui 闪电网络通道生命周期

```mermaid
flowchart LR
    S1["查询余额<br/>balance check"] --> S2["创建 Channel<br/>Object 共享对象"]
    S2 --> S3["DAG 最终确定<br/>⏱ < 1 sec"]
    S3 --> S4["ObjectID<br/>即为通道标识"]
    S4 --> S5["签名状态更新<br/>state_num 递增"]
    S5 --> S6["HTLC 更新<br/>Move 合约方法调用"]
    S6 --> S7{"关闭方式"}
    S7 -->|协作| S8["调用 close_channel()<br/>余额分配回账户"]
    S7 -->|强制| S9["调用 force_close()<br/>epoch 延迟等待"]
    S9 --> S10["调用 withdraw()<br/>余额回账户"]
    S7 -->|违约| S11["调用 penalize()<br/>撤销密钥+旧状态"]

    style S3 fill:#2ecc71,color:#fff
    style S10 fill:#2ecc71,color:#fff
```

---

### 3. 开通道序列交互图（Sui 适配）

```mermaid
sequenceDiagram
    participant Alice as Alice (发起方)
    participant Bob as Bob (接收方)
    participant Sui as Sui 网络
    participant LND_A as Alice LND
    participant LND_B as Bob LND

    Note over Alice, Bob: ═══ 通道开设协议 ═══

    Alice->>LND_A: openchannel(bob_pubkey, amount)
    LND_A->>LND_A: 检查余额 ≥ amount + gas
    LND_A->>LND_B: MsgOpenChannel<br/>{chain_hash, amount, push_amt,<br/>channel_flags, funding_key}

    LND_B->>LND_B: 验证参数<br/>检查余额(若有 push)
    LND_B->>LND_A: MsgAcceptChannel<br/>{min_depth=1, funding_key,<br/>revocation_basepoint, ...}

    Note over LND_A, Sui: ═══ Sui 链上操作 ═══

    LND_A->>LND_A: 构建 open_channel 交易:<br/>创建 Channel Object<br/>{local_key, remote_key,<br/>local_balance, remote_balance,<br/>state_num=0, to_self_delay}

    LND_A->>Sui: 提交 open_channel 交易<br/>(创建共享对象)
    Sui-->>Sui: MoveVM 执行<br/>创建 Channel Object
    Sui-->>LND_A: 最终确定通知<br/>ObjectID = 0xABC...

    LND_A->>LND_B: MsgFundingCreated<br/>{object_id, initial_commitment_sig}

    LND_B->>LND_B: 验证 Object 存在于链上<br/>验证签名
    LND_B->>LND_A: MsgFundingSigned<br/>{commitment_sig}

    Note over LND_A, Sui: ═══ 等待最终确定（极快） ═══

    Sui-->>LND_A: Object 最终确定 ✓
    Sui-->>LND_B: Object 最终确定 ✓

    LND_A->>LND_B: MsgChannelReady<br/>{channel_id = ObjectID}
    LND_B->>LND_A: MsgChannelReady<br/>{channel_id = ObjectID}

    Note over Alice, Bob: ✅ 通道就绪，可发送支付<br/>ShortChanID = ObjectID

    rect rgb(200, 235, 200)
        Note over Alice, Sui: 对比 Bitcoin: 此流程从 ~60min 缩短到 ~2sec
    end
```

---

### 4. 多跳 HTLC 支付序列图

```mermaid
%%{init: {
  'theme': 'base',
  'themeVariables': {
    'actorBorder': '#888888',
    'actorBkg': '#333333',
    'actorTextColor': '#ffffff',
    'noteBkgColor': '#444444',
    'noteBorderColor': '#666666',
    'noteTextColor': '#eeeeee',
    'signalColor': '#aaaaaa',
    'signalTextColor': '#dddddd',
    'labelTextColor': '#cccccc',
    'loopTextColor': '#ffffff',
    'activationBkgColor': '#3a3a3a',
    'fillType0': '#2a2a2a'
  }
}}%%

sequenceDiagram
    participant A as Alice<br/>(付款方)
    participant N1 as Node1<br/>(中继)
    participant B as Bob<br/>(收款方)
    participant Sui as Sui 链<br/>(仅争议时)

    Note over A, B: ═══ 发票创建 ═══
    B->>B: 生成 preimage R<br/>payment_hash H = SHA256(R)
    B->>A: Invoice (lnst1...)<br/>{H, amount, expiry_epoch}

    Note over A, B: ═══ 洋葱路由构建 ═══
    A->>A: 路径查找: A → N1 → B<br/>构建洋葱包 (Sphinx)

    Note over A, B: ═══ HTLC 转发链 ═══

    rect
        A->>N1: update_add_htlc<br/>{H, amt=1010, expiry=epoch+200}
        A->>N1: commitment_signed
        N1->>A: revoke_and_ack
        N1->>A: commitment_signed
        A->>N1: revoke_and_ack
        Note over A, N1: A↔N1 通道状态更新<br/>(链下签名交换，无链上操作)
    end

    rect
        N1->>B: update_add_htlc<br/>{H, amt=1000, expiry=epoch+100}
        N1->>B: commitment_signed
        B->>N1: revoke_and_ack
        B->>N1: commitment_signed
        N1->>B: revoke_and_ack
        Note over N1, B: N1↔B 通道状态更新<br/>(链下签名交换，无链上操作)
    end

    Note over A, B: ═══ 原像揭示（反向） ═══

    rect
        B->>N1: update_fulfill_htlc<br/>{preimage=R}
        B->>N1: commitment_signed
        N1->>B: revoke_and_ack
        Note over N1, B: N1 获得 R，扣减 B 的 HTLC
    end

    rect
        N1->>A: update_fulfill_htlc<br/>{preimage=R}
        N1->>A: commitment_signed
        A->>N1: revoke_and_ack
        Note over A, N1: A 确认支付完成
    end

    Note over A, B: ✅ 支付完成<br/>全程链下，零 gas 消耗

    Note over A, Sui: ═══ 异常情况：HTLC 超时 ═══

    rect
        alt B 不揭示原像且 expiry 到达
            N1->>Sui: 调用 htlc_timeout() 合约方法<br/>合约逻辑: current_epoch ≥ expiry
            Sui-->>N1: HTLC 金额返还 N1
            N1->>A: update_fail_htlc
        end
    end
```

---

### 5. 强制关闭与争议解决流程图

```mermaid
flowchart TB
    Start["单方面关闭触发<br/>(对端掉线/不响应)"]

    Start --> Publish["提交最新承诺状态到 Sui<br/>调用 Channel Object 的<br/>force_close(state_num, sig)"]

    Publish --> SuiExec["Sui MoveVM 执行<br/>合约 ForceClose 逻辑"]
    SuiExec --> Validate{"验证签名 +<br/>state_num ≥ 链上记录?"}

    Validate -->|"失败"| Reject["交易失败<br/>状态不变"]
    Validate -->|"成功"| Freeze["Channel Object<br/>进入 CLOSING 状态<br/>记录 close_epoch"]

    Freeze --> Parallel["并行处理"]

    Parallel --> LocalBalance["本地余额处理"]
    Parallel --> RemoteBalance["对端余额处理"]
    Parallel --> HTLCs["活跃 HTLC 处理"]

    subgraph "本地余额（to_self_delay 保护）"
        LocalBalance --> WaitCSV{"等待相对延迟<br/>current_epoch ≥<br/>close_epoch + to_self_delay?"}
        WaitCSV -->|"未到期"| WaitCSV
        WaitCSV -->|"到期"| ClaimLocal["调用 claim_local_balance()<br/>余额转入本地账户"]
    end

    subgraph "对端余额"
        RemoteBalance --> ClaimRemote["对端可立即调用<br/>claim_remote_balance()<br/>（无延迟）"]
    end

    subgraph "HTLC 解析"
        HTLCs --> HTLCType{"HTLC 方向?"}
        HTLCType -->|"出站 HTLC"| TimeoutPath["等待 CLTV 到期<br/>current_epoch ≥ htlc.expiry"]
        TimeoutPath --> ClaimTimeout["调用 htlc_timeout()<br/>合约验证"]

        HTLCType -->|"入站 HTLC"| PreimagePath["持有原像?"]
        PreimagePath -->|"是"| ClaimSuccess["调用 htlc_claim(preimage)<br/>合约验证 SHA256"]
        PreimagePath -->|"否"| WaitExpiry["等待超时后<br/>对端取回"]
    end

    subgraph "违约检测 (Breach)"
        Freeze --> Monitor["ChainWatcher 监控<br/>Object 状态变更"]
        Monitor --> OldState{"提交的 state_num<br/>< 本地已知最新?"}
        OldState -->|"是 = 违约!"| Penalize["调用 penalize()<br/>提交撤销证明"]
        Penalize --> SeizeAll["全部余额<br/>判归诚实方"]
        OldState -->|"否"| NoBreach["正常流程"]
    end

    style Freeze fill:#e74c3c,color:#fff
    style ClaimLocal fill:#2ecc71,color:#fff
    style ClaimRemote fill:#2ecc71,color:#fff
    style ClaimSuccess fill:#2ecc71,color:#fff
    style ClaimTimeout fill:#f39c12,color:#fff
    style SeizeAll fill:#9b59b6,color:#fff
    style Reject fill:#95a5a6,color:#fff
```

---

### 6. Bitcoin Script → Sui MoveVM 合约方法映射

> **说明**：原本在 Bitcoin 上的脚本逻辑被映射为 Sui Move 模块中的具体函数。LND 适配器通过调用这些合约方法来触发链上状态变更。

```mermaid
graph TB
    subgraph "Bitcoin Script（现有）"
        BS1["OP_2 &lt;key1&gt; &lt;key2&gt; OP_2<br/>OP_CHECKMULTISIG"]
        BS2["OP_HASH160 &lt;hash&gt; OP_EQUAL<br/>OP_CHECKSIG"]
        BS3["&lt;delay&gt; OP_CSV<br/>OP_DROP OP_CHECKSIG"]
        BS4["&lt;expiry&gt; OP_CLTV<br/>OP_DROP OP_CHECKSIG"]
        BS5["OP_IF<br/>  revocation_path<br/>OP_ELSE<br/>  normal_path<br/>OP_ENDIF"]
    end

    subgraph "Sui MoveVM 合约方法（新增）"
        SP1["lightning::open_channel()<br/>验证双方签名 + 锁定余额<br/>创建 SharedObject"]
        SP2["lightning::htlc_claim()<br/>验证 preimage + 签名<br/>转移 HTLC 余额"]
        SP3["lightning::claim_local_balance()<br/>检查相对时间锁<br/>current_epoch >= close_epoch + delay"]
        SP4["lightning::htlc_timeout()<br/>检查绝对时间锁<br/>current_epoch >= htlc.expiry"]
        SP5["lightning::penalize()<br/>验证 revocation_key<br/>+ state_num 比较"]
    end

    BS1 -->|"2-of-2 多签"| SP1
    BS2 -->|"哈希锁 HTLC"| SP2
    BS3 -->|"相对时间锁 CSV"| SP3
    BS4 -->|"绝对时间锁 CLTV"| SP4
    BS5 -->|"条件分支/撤销"| SP5

    subgraph "Sui ChannelObject (Move Object)"
        CO["ChannelObject 状态字段<br/>─────────────<br/>id: UID<br/>local_balance: u64<br/>remote_balance: u64<br/>local_pubkey: vector&lt;u8&gt;<br/>remote_pubkey: vector&lt;u8&gt;<br/>revocation_key: Option&lt;vector&lt;u8&gt;&gt;<br/>state_num: u64<br/>to_self_delay: u64<br/>status: OPEN|CLOSING|CLOSED<br/>close_epoch: u64<br/>htlcs: Table&lt;u64, HTLCEntry&gt;"]
    end

    SP1 --> CO
    SP2 --> CO
    SP3 --> CO
    SP4 --> CO
    SP5 --> CO

    style BS1 fill:#f39c12,color:#fff
    style BS2 fill:#f39c12,color:#fff
    style BS3 fill:#f39c12,color:#fff
    style BS4 fill:#f39c12,color:#fff
    style BS5 fill:#f39c12,color:#fff
    style SP1 fill:#2ecc71,color:#fff
    style SP2 fill:#2ecc71,color:#fff
    style SP3 fill:#2ecc71,color:#fff
    style SP4 fill:#2ecc71,color:#fff
    style SP5 fill:#2ecc71,color:#fff
    style CO fill:#3498db,color:#fff
```

---

### 7. 模块改造优先级与依赖关系

```mermaid
graph LR
    subgraph "Phase 1: 配置与基础"
        P1A["config.go 新增<br/>SuiChainName"]
        P1B["chainreg/sui_params<br/>网络参数"]
        P1C["keychain/ 扩展<br/>Ed25519 + secp256k1"]
        P1D["lncfg/sui.go<br/>Sui 节点配置"]
    end

    subgraph "Phase 2: 链后端适配器"
        P2A["chainntnfs/suinotify/<br/>事件订阅适配器"]
        P2B["lnwallet/suiwallet/<br/>钱包适配器<br/>(复用 Bitcoin 类型)"]
        P2C["input/sui_channel.go<br/>Move 合约调用封装"]
        P2D["chainfee/sui_estimator<br/>Gas→SatPerKWeight"]
    end

    subgraph "Phase 3: 核心扩展"
        P3A["lnwallet/channel.go<br/>CommitmentBuilder 提取"]
        P3B["funding/manager.go<br/>SuiAssembler 分支"]
        P3C["channeldb/<br/>序列化兼容"]
        P3D["lnwire/<br/>ChannelID = ObjectID"]
    end

    subgraph "Phase 4: 上层扩展"
        P4A["contractcourt/<br/>Sui resolver 分支"]
        P4B["sweep/<br/>简化为余额提取"]
        P4C["graph/ + discovery/<br/>SMT 通道验证"]
        P4D["rpcserver.go<br/>链类型调度"]
    end

    subgraph "Phase 5: 集成"
        P5A["config_builder.go<br/>BuildChainControl 分支"]
        P5B["server.go<br/>启动流程集成"]
        P5C["zpay32/<br/>lnst 发票编码"]
        P5D["itest/<br/>集成测试"]
    end

    P1A --> P2A & P2B
    P1B --> P2A & P2B
    P1C --> P2B & P2C
    P1D --> P2A & P2B

    P2A --> P3A & P3B
    P2B --> P3A & P3B
    P2C --> P3A
    P2D --> P3B

    P3A --> P4A & P4B
    P3B --> P4C
    P3C --> P3A & P3B
    P3D --> P3B & P4C

    P4A & P4B & P4C & P4D --> P5A
    P5A --> P5B --> P5D
    P4D --> P5C

    style P1A fill:#e74c3c,color:#fff
    style P1B fill:#e74c3c,color:#fff
    style P1C fill:#e74c3c,color:#fff
    style P2A fill:#e67e22,color:#fff
    style P2B fill:#e67e22,color:#fff
    style P2C fill:#e67e22,color:#fff
    style P2D fill:#e67e22,color:#fff
    style P3A fill:#f1c40f,color:#333
    style P3B fill:#f1c40f,color:#333
    style P3C fill:#f1c40f,color:#333
    style P3D fill:#f1c40f,color:#333
    style P4A fill:#2ecc71,color:#fff
    style P4B fill:#2ecc71,color:#fff
    style P4C fill:#2ecc71,color:#fff
    style P4D fill:#2ecc71,color:#fff
    style P5A fill:#3498db,color:#fff
    style P5B fill:#3498db,color:#fff
    style P5C fill:#3498db,color:#fff
    style P5D fill:#3498db,color:#fff
```

---

### 8. 数据流：链上 vs 链下交互全景

```mermaid
%%{init: {
  'theme': 'base',
  'themeVariables': {
    'actorBorder': '#888888',
    'actorBkg': '#333333',
    'actorTextColor': '#ffffff',
    'noteBkgColor': '#444444',
    'noteBorderColor': '#666666',
    'noteTextColor': '#eeeeee',
    'signalColor': '#aaaaaa',
    'signalTextColor': '#dddddd',
    'labelTextColor': '#cccccc',
    'loopTextColor': '#ffffff',
    'activationBkgColor': '#3a3a3a',
    'fillType0': '#2a2a2a'
  }
}}%%

sequenceDiagram
    box #2a2a2a 链下（Lightning Protocol）
        participant Alice
        participant Bob
    end
    box #3a3a3a 链上（Sui DAG）
        participant SuiChain as Sui Chain
        participant ChanObj as Channel Object
    end

    Note over Alice, ChanObj: ══════ Phase 1: 通道建立 ══════

    Alice->>SuiChain: Move Call: open_channel<br/>{keys, balances, delay}
    SuiChain->>ChanObj: 创建 Shared Object<br/>state_num=0, status=OPEN
    SuiChain-->>Alice: ObjectID 确认
    SuiChain-->>Bob: ObjectID 确认

    Note over Alice, ChanObj: ══════ Phase 2: 链下支付（核心循环） ══════

    loop 每次支付/转发
        Alice->>Bob: update_add_htlc {hash, amt, expiry}
        Alice->>Bob: commitment_signed {sig_for_state_N+1}
        Bob->>Alice: revoke_and_ack {revocation_key_N}
        Bob->>Alice: commitment_signed {sig_for_state_N+1}
        Alice->>Bob: revoke_and_ack {revocation_key_N}
        Note over Alice, Bob: 双方本地更新状态<br/>state_num++ (无链上交互!)
    end

    Note over Alice, Bob: 💡 正常运行期间<br/>零链上交易，零 Gas

    Note over Alice, ChanObj: ══════ Phase 3a: 协作关闭 ══════

    Alice->>Bob: shutdown
    Bob->>Alice: shutdown
    Alice->>Bob: closing_signed {final_balances}
    Bob->>Alice: closing_signed {final_balances}

    Alice->>SuiChain: Move Call: close_channel<br/>{both_sigs, final_balances}
    SuiChain->>ChanObj: 验证双签名<br/>分配余额到各自账户
    SuiChain->>ChanObj: 销毁 Object → status=CLOSED

    Note over Alice, ChanObj: ══════ Phase 3b: 强制关闭（替代路径） ══════

    rect #4a4a4a
        Alice->>SuiChain: Move Call: force_close<br/>{state_num, commitment_sig}
        SuiChain->>ChanObj: status → CLOSING<br/>close_epoch = current_epoch

        Note over Bob, ChanObj: Bob 有 to_self_delay 个 epoch<br/>检测是否为旧状态

        alt Bob 发现是旧状态（违约!）
            Bob->>SuiChain: Move Call: penalize<br/>{revocation_key, proof}
            SuiChain->>ChanObj: 全部余额判归 Bob
        else 状态正确，延迟到期后
            Alice->>SuiChain: Move Call: claim_local_balance<br/>{epoch ≥ close_epoch + delay}
            Bob->>SuiChain: Move Call: claim_remote_balance
            SuiChain->>ChanObj: 余额分别转入各自账户
        end
    end
```

---

## 2. 改造步骤

**1. 配置扩展 + Sui 网络参数（零侵入）**

不新增 `chaintype/` 抽象层。LND 已有 `--chain` 和 `--network` 双维参数（`lncli --chain=bitcoin --network=mainnet`），天然支持多链扩展。改造步骤：

| 文件                              | 修改内容                                                        |
| --------------------------------- | --------------------------------------------------------------- |
| `config.go`                       | 新增 `SuiChainName = "sui"` 常量 + `Sui *lncfg.Chain` 配置项 |
| `lncfg/sui.go`（新建）           | Sui 节点配置结构体（RPC 地址、PackageID、epoch 间隔等）         |
| `chainreg/sui_params.go`（新建） | `SuiNetParams`（网络 ID、创世哈希、默认端口等）                |
| `chainreg/chainregistry.go`       | `switch` 分支新增 `"sui"` case                                 |

**核心设计原则 — 适配器边界类型映射**：

Sui 适配器 in 实现 LND 现有接口时，内部复用 Bitcoin 类型做语义映射，不改接口签名：

| Bitcoin 类型                 | Sui 适配器内部用法                         | 说明         |
| ---------------------------- | ------------------------------------------- | ------------ |
| `wire.OutPoint{Hash, Index}` | `Hash` ← ObjectID (32B), `Index` = 0        | 通道标识     |
| `btcutil.Amount`             | 直接存储 Sui 最小单位（int64 Mist）             | 金额映射     |
| `wire.MsgTx`                 | `Payload` 字段承载 Move Call 序列化字节    | 交易包装     |
| `chainfee.SatPerKWeight`     | 内部做 GasPrice → SatPerKWeight 换算        | 费率映射     |
| `chainhash.Hash`             | 直接存储 Sui Transaction Digest / ObjectID           | 32B 通用     |
| `lnwire.ShortChannelID`      | 8 字节存 ObjectID 截断 + TLV 扩展存完整 32B | 路由协议兼容 |

**2. 链后端接口 — 保持不变，仅新增 Sui 实现**

**不修改**现有接口签名。LND 的核心链后端接口签名保持原样，Sui 适配器作为新实现，内部做语义转换：

- **`ChainNotifier`** — 适配器将 `RegisterConfirmationsNtfn(txid *chainhash.Hash, ...)` 中的 `txid` 解释为 Transaction Digest，订阅交易最终确定事件
- **`BlockChainIO`** — 适配器将 `GetUtxo(outpoint *wire.OutPoint, ...)` 解释为查询 Channel Object 状态
- **`Signer`** — 适配器将 `SignOutputRaw(tx *wire.MsgTx, ...)` 中的 tx 解释为 Move Call 序列化载体，对内容做 Sui 签名
- **`WalletController`** — 改动最大的适配器；在实现内部做 UTXO → 余额的语义转换（`ListUnspentWitness` 返回一个"虚拟 UTXO"）

**3. 扩展 `ChainControl` + `config_builder.go`**

修改 `chainreg/chainregistry.go` 中的 `ChainControl` 结构体：

- 新增 `ChainName string` 字段（`"bitcoin"` 或 `"sui"`）
- 在 `config_builder.go` 的 `BuildChainControl` 函数中新增 `"sui"` 分支，创建 Sui 适配器实例注入 `ChainControl`
- 创建 `chainreg/sui_params.go` 定义 `SuiNetParams`（网络 ID、创世哈希、默认端口、epoch 间隔）

**4. 实现 Sui 链通知后端 `chainntnfs/suinotify/`**

实现 `ChainNotifier` 接口，核心映射关系：

| Bitcoin 概念                                | Sui 实现                                           |
| ------------------------------------------- | --------------------------------------------------- |
| `RegisterConfirmationsNtfn(txid, numConfs)` | 订阅交易最终确定事件 |
| `RegisterSpendNtfn(outpoint)`               | 订阅 Channel Object 状态变更（通过 Move Event 触发）   |
| `RegisterBlockEpochNtfn()`                  | 订阅 Sui Checkpoint/Epoch 推进事件                            |
| 区块重组检测                                | 大幅简化（DAG 无经典重组）                          |
| `GetBlock()` / `GetBlockHash()`             | 查询 Checkpoint 信息                      |

**5. 实现 Sui 钱包 `lnwallet/suiwallet/`**

实现适配后的 `WalletController` 接口：

| Bitcoin 操作                           | Sui 操作                                                              |
| -------------------------------------- | ---------------------------------------------------------------------- |
| `ListUnspentWitness()` — 列出 UTXO     | `GetBalance()` — 查询账户余额                                          |
| `LeaseOutput(OutPoint)` — 锁 UTXO      | `ReserveBalance(amount)` — 预留余额                                    |
| `SendOutputs([]*wire.TxOut)` — 构建 TX | `MoveCall(package, module, func, args)` — 调用合约                                      |
| `FundPsbt()` / `SignPsbt()`            | `BuildMoveCall()` / `SignTransaction()` — 构建 Sui 交易 |
| 币选择（`selectInputs`）               | 不需要（直接从余额扣减）                                               |
| 找零地址生成                           | 不需要                                                                 |

密钥管理方面：复用 [derivation.go](../keychain/derivation.go) 的 `KeyFamily` 体系，新增 Sui coinType，密钥衍生支持 secp256k1 和 Ed25519 双路径。

**6. Sui 链上通道逻辑 — 基于 MoveVM 合约实现**

原本在 Bitcoin 上的脚本逻辑由 Sui 上的 Move 合约替代。

**6a. Move 模块 `lightning` 核心函数**：

```move
public entry fun open_channel(...) // 创建 Channel SharedObject
public entry fun close_channel(...) // 协作关闭
public entry fun force_close(...) // 强制关闭
public entry fun htlc_claim(...) // HTLC 成功路径
public entry fun htlc_timeout(...) // HTLC 超时路径
public entry fun penalize(...) // 违约惩罚路径
```

在 Go 侧创建 `input/sui_channel.go`，封装上述合约调用的构建函数。

**7. 通道标识体系重设计**

- 修改 [channel_id.go](../lnwire/channel_id.go) — `NewChanIDFromOutPoint` 在 Sui 链上直接使用 ObjectID 的前 32 字节
- 修改 [short_channel_id.go](../lnwire/short_channel_id.go) — Sui 模式下 `ShortChannelID` 使用 ObjectID（32 字节）
- 更新 [channel.go](../lnwallet/channel.go) — `FundingOutpoint` 字段改为 `chaintype.ChannelPoint`
- 修改 [channel_edge_info.go](../graph/db/models/channel_edge_info.go) — 重命名/扩展字段以支持 Sui 公钥格式

**8. 通道状态机适配**

[channel.go](../lnwallet/channel.go) 的改造策略是**分离协议逻辑与链上操作**：

- 提取接口 `CommitmentBuilder`：Bitcoin 实现构建 承诺交易，Sui 实现构建 Move Call 状态更新
- 提取接口 `ScriptEngine`：Sui 实现调用 Move 合约逻辑
- 保留状态编号（`StateNum`）、HTLC 管理（`UpdateLog`）等核心来协议逻辑不变

**9. 资金管理器适配**

修改 [manager.go](../funding/manager.go)：

- `waitForFundingConfirmation` — Sui 模式下等待交易 Finalized
- 资金交易构建切换到新的 `chanfunding.SuiAssembler`（直接调用 open_channel 合约）

**10. 合约裁决适配**

修改 [contractcourt](../contractcourt/) 所有 resolver 以调用对应的 Move 合约方法。

**11. Sweep 模块简化**

在 [sweep](../sweep/) 中新增 Sui 模式，简化为调用合约将余额提取回个人账户。

**12. 图与发现适配**

- 修改 [builder.go](../graph/builder.go) — Sui 查询 Channel Object 是否仍存在
- 修改 [gossiper.go](../discovery/gossiper.go) — Sui 验证 Channel Object 存在 + 双方 key 匹配

**13. 费率体系适配**

- 在 [chainfee](../chainfee/) 中新增 `SuiEstimator`
- 修改 [rates.go](../chainfee/rates.go) — 新增 `GasPrice` 类型

**14. RPC 与发票适配**

- 修改 [rpcserver.go](../rpcserver.go) 中的 `GetInfo` — 返回 `"sui"`
- 修改 [zpay32](../zpay32/) — 新增 Sui HRP

**15. 配置与启动**

- 修改 [config.go](../config.go) — 新增 `Sui *lncfg.Chain`
- 修改 [config_builder.go](../config_builder.go) — `BuildChainControl` 新增 Sui 分支
- 修改 [server.go](../server.go) — 根据链类型初始化对应子系统

---

## 3. Sui 必须支持的完整能力清单

### P0 — 核心能力（无此能力则无法运行闪电网络）

| #   | 能力                         | 详细需求                                                                                     | 对应 LND 模块                                   |
| --- | ---------------------------- | -------------------------------------------------------------------------------------------- | ----------------------------------------------- |
| 1   | **Move 闪电网络合约**      | 实现 open_channel/close_channel/force_close/htlc_claim/timeout/penalize | `input/sui_channel.go` + Move 合约 |
| 2   | **共享对象 (Shared Object)** | Channel Object 需双方都能通过合约操作                                        | `lnwallet/suiwallet/`                          |
| 3   | **哈希锁 (Hashlock)**        | Move 合约内置 SHA256 原像验证逻辑                                              | HTLC 合约                                       |
| 4   | **时间参考**         | 合约能读取当前 epoch 或 clock，用于时间锁比较判断                                              | CSV/CLTV 等价                                   |
| 5   | **状态版本控制**          | Channel Object 需有单调递增的 `state_num`，防止旧状态重放                                    | 承诺状态同步                                    |
| 6   | **事件订阅 API**             | 按 ObjectID 或 PackageID 订阅状态变更事件                             | `chainntnfs/suinotify/`                        |
| 7   | **最终性通知**               | 交易提交后能回调通知最终确定状态                                                             | 确认流程    |
| 8   | **多签名验证**               | Move 合约内置 secp256k1 签名验证                           | 交易授权                                 |
| 9   | **对象查询 API**             | 按 ObjectID 查询完整对象状态                                      | `BlockChainIO` 等价                             |
| 10  | **原子性状态更新**           | 合约执行的状态变更要么全部生效、要么全部回滚                                                 | 通道状态一致性                                  |
| 11  | **Go SDK**             | 支持 Sui 交易构建、签名、提交与 Event 订阅                             | [keychain](../keychain/) + 适配层                        |

---

## 4. 验证

- **单元测试**: 每个新增的 Sui 实现独立测试，mock Sui RPC
- **合约测试**: 使用 `sui move test` 验证合约逻辑
- **集成测试**: 修改 [itest](../itest/) 框架，覆盖开通道、支付、关闭、惩罚等全流程
- **命令**: `make itest tags=sui`
- **手动检查**: `lncli --chain=sui getinfo`

## 5. 决策记录

- **适配策略**: 采用**适配器模式（Adapter Pattern）**。LND 通过适配器调用 Sui 上的 Move 合约来替代原本的 Bitcoin 脚本逻辑。
- **并行开发**: 在 Setu 通用 VM 就绪前，先行集成 Sui 以推进上层逻辑开发。
- **ObjectID**: Sui 上使用 32 字节 ObjectID 直接标识通道。
