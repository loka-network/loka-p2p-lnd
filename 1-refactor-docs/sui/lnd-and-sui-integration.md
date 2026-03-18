# Plan: Lightning Network Adaptation for Sui/MoveVM — Refactoring Document

## 0. Overview

Refactor LND Lightning Network to simultaneously support a **Bitcoin + Sui (MoveVM) dual system**. Leverage Sui's mature MoveVM smart contract capabilities to implement the core Lightning Network logic, with channels identified by a 32-byte `ObjectID`.

> **Background and Strategy Adjustment**: To enable parallel development, LND integration will first be carried out on the **Sui network** before Setu's general-purpose virtual machine (MoveVM) is fully integrated. Sui, as a mature MoveVM runtime environment, is ideal for initially validating the Move version of Lightning Network contract logic. The original Lightning Network script logic on Bitcoin (such as multisig, HTLC, timelocks, breach penalties) will be directly implemented through **MoveVM contracts**.
>
> **Goal**: By first adapting to Sui, accelerate LND's compatibility refactoring for the object-account model and MoveVM. Once Setu's MoveVM environment is ready, the relevant contract logic will be migrated to Setu.
>
> **Adaptation Strategy**: Shift from "adding hardcoded primitives on the Setu side" to **"deploying Move Lightning Network contracts on the Sui side"**. LND invokes the Move contracts on Sui through adapters to perform channel lifecycle management (`open_channel`, `close_channel`, `force_close`, `htlc_claim`, `penalize`, etc.).

Refactoring Strategy: **Zero-intrusion Adapter Pattern**. No new abstraction layers, no changes to existing interface signatures; instead, implement Sui/MoveVM adapters at the interface implementation level — the adapter internally reuses Bitcoin types (e.g., `wire.OutPoint.Hash` to store ObjectID, `btcutil.Amount` for unit mapping, `wire.MsgTx` to carry Move Call serialized bytes) and performs semantic conversion at the implementation boundaries. Existing Bitcoin code paths remain completely unaffected; Sui is inserted as a new `ChainControl` implementation, selectable via `lncli --chain=sui`.

Core refactoring workload distribution: **lnd backend adapter implementation for Sui/MoveVM (35%) → Move Lightning Network contract implementation on Sui side (20%) → Upper module extensions (25%) → Configuration/startup/testing integration (20%)**.

---

## 1. Process Interaction Diagrams

The following 8 diagrams cover:

1. **Architecture Overview** — Layer and module relationships in the dual-chain abstraction
2. **Channel Lifecycle Comparison** — Clear side-by-side difference between Bitcoin and Sui (MoveVM) processes
3. **Channel Opening Sequence** — Detailed interaction timeline between both parties and the chain
4. **Multi-hop HTLC Payment** — Full sequence for normal flow and exceptional timeout
5. **Force Close & Dispute Resolution** — Complete decision flow including breach penalty
6. **Bitcoin Script → MoveVM Contract Method Mapping** — How each Bitcoin contract operation translates to Move contract calls
7. **Refactoring Phase Dependencies** — Execution order and dependencies of 5 phases
8. **On-chain/Off-chain Data Flow Panorama** — Full channel lifecycle interaction swimlane

### 1. Adapter Pattern Dual-Chain Architecture Overview

```mermaid
graph TB
    subgraph "LND Application Layer (No Changes)"
        RPC["RPC Server<br/>lnrpc/"]
        Router["Routing Engine<br/>routing/"]
        Switch["HTLC Switch<br/>htlcswitch/"]
        Invoice["Invoice Management<br/>invoices/"]
        Funding["Funding Manager<br/>funding/"]
        ChanSM["Channel State Machine<br/>lnwallet/channel.go"]
        ContractCourt["Contract Court<br/>contractcourt/"]
        Graph["Graph Construction<br/>graph/"]
        Discovery["Gossip Discovery<br/>discovery/"]
    end

    subgraph "Existing Interfaces (Signatures Unchanged)"
        IF_Notify["ChainNotifier<br/>chainntnfs/interface.go"]
        IF_Wallet["WalletController<br/>lnwallet/interface.go"]
        IF_Signer["Signer<br/>input/signer.go"]
        IF_IO["BlockChainIO<br/>chainntnfs/interface.go"]
        CC["ChainControl<br/>chainreg/chainregistry.go"]
    end

    subgraph "Bitcoin Backend (No Changes)"
        BTC_Notify["bitcoindnotify/<br/>btcdnotify/<br/>neutrinonotify/"]
        BTC_Wallet["btcwallet/<br/>WalletController implementation"]
        BTC_Script["input/script_utils.go<br/>Bitcoin Script construction"]
        BTC_Fee["chainfee/<br/>SatPerKWeight"]
        BTC_Chain["Bitcoin Blockchain<br/>UTXO Model"]
    end

    subgraph "Sui/MoveVM Adapter (New)"
        SUI_Notify["suinotify/<br/>Move Event/Object subscription<br/>(implements ChainNotifier)"]
        SUI_Wallet["suiwallet/<br/>Move contract calls<br/>(implements WalletController)"]
        SUI_Program["input/sui_channel.go<br/>Move contract call encapsulation"]
        SUI_Fee["chainfee/sui_estimator<br/>GasPrice→SatPerKWeight"]
        SUI_Chain["Sui Network<br/>MoveVM Contracts<br/>Implementing Lightning script logic"]
        SUI_Adapt["Type Mapping Strategy<br/>OutPoint.Hash ← ObjectID<br/>Amount ← SuiUnit<br/>MsgTx ← Move Call bytes"]
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

    SUI_Adapt -.->|"Internally reused"| SUI_Notify
    SUI_Adapt -.->|"Internally reused"| SUI_Wallet
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

    %% Hide last two edges (forced layout edges)
    linkStyle 22 stroke-width:0, stroke:transparent;
```

---

### 2. Channel Lifecycle Comparison (Bitcoin vs Sui)

- Figure 1: Bitcoin Lightning Channel Lifecycle

```mermaid
flowchart LR
    B1["Select UTXO<br/>coin selection"] --> B2["Build 2-of-2<br/>multisig funding TX"]
    B2 --> B3["Broadcast TX<br/>Wait for 3-6 confirmations<br/>⏱ 30~60 min"]
    B3 --> B4["Generate ShortChanID<br/>block:tx:output"]
    B4 --> B5["Exchange commitment transactions<br/>wire.MsgTx signatures"]
    B5 --> B6["HTLC updates<br/>Script conditional branches"]
    B6 --> B7{"Closing Method"}
    B7 -->|Cooperative| B8["Build closing TX<br/>Both sign and broadcast"]
    B7 -->|Force| B9["Broadcast commitment TX<br/>CSV delay wait"]
    B9 --> B10["Sweep UTXO<br/>Return to wallet"]
    B7 -->|Breach| B11["Build justice TX<br/>Spend with revocation key"]

    style B3 fill:#e74c3c,color:#fff
    style B10 fill:#e67e22,color:#fff
```

- Figure 2: Sui Lightning Channel Lifecycle

```mermaid
flowchart LR
    S1["Balance check<br/>balance check"] --> S2["Create Channel<br/>Shared Object"]
    S2 --> S3["DAG finalization<br/>⏱ < 1 sec"]
    S3 --> S4["ObjectID<br/>Serves as channel identifier"]
    S4 --> S5["Sign state updates<br/>state_num increments"]
    S5 --> S6["HTLC updates<br/>Move contract method calls"]
    S6 --> S7{"Closing Method"}
    S7 -->|Cooperative| S8["Call close_channel()<br/>Allocate balance back to accounts"]
    S7 -->|Force| S9["Call force_close()<br/>Epoch delay wait"]
    S9 --> S10["Call withdraw()<br/>Balance back to accounts"]
    S7 -->|Breach| S11["Call penalize()<br/>Revocation key + old state"]

    style S3 fill:#2ecc71,color:#fff
    style S10 fill:#2ecc71,color:#fff
```

---

### 3. Channel Opening Sequence Diagram (Sui Adaptation)

```mermaid
sequenceDiagram
    participant Alice as Alice (Initiator)
    participant Bob as Bob (Recipient)
    participant Sui as Sui Network
    participant LND_A as Alice LND
    participant LND_B as Bob LND

    Note over Alice, Bob: ═══ Channel Opening Protocol ═══

    Alice->>LND_A: openchannel(bob_pubkey, amount)
    LND_A->>LND_A: Check balance ≥ amount + gas
    LND_A->>LND_B: MsgOpenChannel<br/>{chain_hash, amount, push_amt,<br/>channel_flags, funding_key}

    LND_B->>LND_B: Validate parameters<br/>Check balance (if push)
    LND_B->>LND_A: MsgAcceptChannel<br/>{min_depth=1, funding_key,<br/>revocation_basepoint, ...}

    Note over LND_A, Sui: ═══ Sui On-Chain Operations ═══

    LND_A->>LND_A: Build open_channel transaction:<br/>Create Channel Object<br/>{local_key, remote_key,<br/>local_balance, remote_balance,<br/>state_num=0, to_self_delay}

    LND_A->>Sui: Submit open_channel transaction<br/>(create shared object)
    Sui-->>Sui: MoveVM execution<br/>Create Channel Object
    Sui-->>LND_A: Finalization notification<br/>ObjectID = 0xABC...

    LND_A->>LND_B: MsgFundingCreated<br/>{object_id, initial_commitment_sig}

    LND_B->>LND_B: Verify Object exists on-chain<br/>Verify signature
    LND_B->>LND_A: MsgFundingSigned<br/>{commitment_sig}

    Note over LND_A, Sui: ═══ Wait for finalization (very fast) ═══

    Sui-->>LND_A: Object finalized ✓
    Sui-->>LND_B: Object finalized ✓

    LND_A->>LND_B: MsgChannelReady<br/>{channel_id = ObjectID}
    LND_B->>LND_A: MsgChannelReady<br/>{channel_id = ObjectID}

    Note over Alice, Bob: ✅ Channel ready, payments can be sent<br/>ShortChanID = ObjectID

    rect rgb(200, 235, 200)
        Note over Alice, Sui: Compared to Bitcoin: This process shortens from ~60min to ~2sec
    end
```

---

### 4. Multi-hop HTLC Payment Sequence Diagram

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
    participant A as Alice<br/>(Payer)
    participant N1 as Node1<br/>(Relay)
    participant B as Bob<br/>(Payee)
    participant Sui as Sui Chain<br/>(only on dispute)

    Note over A, B: ═══ Invoice Creation ═══
    B->>B: Generate preimage R<br/>payment_hash H = SHA256(R)
    B->>A: Invoice (lnst1...)<br/>{H, amount, expiry_epoch}

    Note over A, B: ═══ Onion Route Construction ═══
    A->>A: Path find: A → N1 → B<br/>Build onion packet (Sphinx)

    Note over A, B: ═══ HTLC Forwarding Chain ═══

    rect
        A->>N1: update_add_htlc<br/>{H, amt=1010, expiry=epoch+200}
        A->>N1: commitment_signed
        N1->>A: revoke_and_ack
        N1->>A: commitment_signed
        A->>N1: revoke_and_ack
        Note over A, N1: A↔N1 channel state update<br/>(off-chain signature exchange, no on-chain operations)
    end

    rect
        N1->>B: update_add_htlc<br/>{H, amt=1000, expiry=epoch+100}
        N1->>B: commitment_signed
        B->>N1: revoke_and_ack
        B->>N1: commitment_signed
        N1->>B: revoke_and_ack
        Note over N1, B: N1↔B channel state update<br/>(off-chain signature exchange, no on-chain operations)
    end

    Note over A, B: ═══ Preimage Revelation (Reverse) ═══

    rect
        B->>N1: update_fulfill_htlc<br/>{preimage=R}
        B->>N1: commitment_signed
        N1->>B: revoke_and_ack
        Note over N1, B: N1 obtains R, deducts HTLC from B
    end

    rect
        N1->>A: update_fulfill_htlc<br/>{preimage=R}
        N1->>A: commitment_signed
        A->>N1: revoke_and_ack
        Note over A, N1: A confirms payment completion
    end

    Note over A, B: ✅ Payment completed<br/>Fully off-chain, zero gas consumption

    Note over A, Sui: ═══ Exception Case: HTLC Timeout ═══

    rect
        alt B does not reveal preimage and expiry reached
            N1->>Sui: Call htlc_timeout() contract method<br/>Contract logic: current_epoch ≥ expiry
            Sui-->>N1: HTLC amount returned to N1
            N1->>A: update_fail_htlc
        end
    end
```

---

### 5. Force Close and Dispute Resolution Flowchart

```mermaid
flowchart TB
    Start["Unilateral close triggered<br/>(Peer offline / unresponsive)"]

    Start --> Publish["Submit latest commitment state to Sui<br/>Call Channel Object's<br/>force_close(state_num, sig)"]

    Publish --> SuiExec["Sui MoveVM execution<br/>Contract ForceClose logic"]
    SuiExec --> Validate{"Verify signature +<br/>state_num ≥ on-chain record?"}

    Validate -->|"Failed"| Reject["Transaction rejected<br/>State unchanged"]
    Validate -->|"Success"| Freeze["Channel Object<br/>enters CLOSING state<br/>Record close_epoch"]

    Freeze --> Parallel["Parallel Processing"]

    Parallel --> LocalBalance["Local Balance Processing"]
    Parallel --> RemoteBalance["Remote Balance Processing"]
    Parallel --> HTLCs["Active HTLC Processing"]

    subgraph "Local Balance (to_self_delay protected)"
        LocalBalance --> WaitCSV{"Wait relative delay<br/>current_epoch ≥<br/>close_epoch + to_self_delay?"}
        WaitCSV -->|"Not yet"| WaitCSV
        WaitCSV -->|"Expired"| ClaimLocal["Call claim_local_balance()<br/>Balance transferred to local account"]
    end

    subgraph "Remote Balance"
        RemoteBalance --> ClaimRemote["Remote can call<br/>claim_remote_balance() immediately<br/>(no delay)"]
    end

    subgraph "HTLC Resolution"
        HTLCs --> HTLCType{"HTLC Direction?"}
        HTLCType -->|"Outgoing HTLC"| TimeoutPath["Wait for CLTV expiry<br/>current_epoch ≥ htlc.expiry"]
        TimeoutPath --> ClaimTimeout["Call htlc_timeout()<br/>Contract verification"]

        HTLCType -->|"Incoming HTLC"| PreimagePath["Hold preimage?"]
        PreimagePath -->|"Yes"| ClaimSuccess["Call htlc_claim(preimage)<br/>Contract verifies SHA256"]
        PreimagePath -->|"No"| WaitExpiry["Wait for timeout then<br/>remote can retrieve"]
    end

    subgraph "Breach Detection"
        Freeze --> Monitor["ChainWatcher monitors<br/>Object state changes"]
        Monitor --> OldState{"Submitted state_num<br/>< locally known latest?"}
        OldState -->|"Yes = Breach!"| Penalize["Call penalize()<br/>Submit revocation proof"]
        Penalize --> SeizeAll["All balances<br/>awarded to honest party"]
        OldState -->|"No"| NoBreach["Normal flow"]
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

### 6. Bitcoin Script → Sui MoveVM Contract Method Mapping

> **Note**: The original script logic on Bitcoin is mapped to specific functions in the Sui Move module. The LND adapter triggers on-chain state changes by calling these contract methods.

```mermaid
graph TB
    subgraph "Bitcoin Script (Existing)"
        BS1["OP_2 &lt;key1&gt; &lt;key2&gt; OP_2<br/>OP_CHECKMULTISIG"]
        BS2["OP_HASH160 &lt;hash&gt; OP_EQUAL<br/>OP_CHECKSIG"]
        BS3["&lt;delay&gt; OP_CSV<br/>OP_DROP OP_CHECKSIG"]
        BS4["&lt;expiry&gt; OP_CLTV<br/>OP_DROP OP_CHECKSIG"]
        BS5["OP_IF<br/>  revocation_path<br/>OP_ELSE<br/>  normal_path<br/>OP_ENDIF"]
    end

    subgraph "Sui MoveVM Contract Methods (New)"
        SP1["lightning::open_channel()<br/>Verify both signatures + lock balances<br/>Create SharedObject"]
        SP2["lightning::htlc_claim()<br/>Verify preimage + signature<br/>Transfer HTLC balance"]
        SP3["lightning::claim_local_balance()<br/>Check relative timelock<br/>current_epoch >= close_epoch + delay"]
        SP4["lightning::htlc_timeout()<br/>Check absolute timelock<br/>current_epoch >= htlc.expiry"]
        SP5["lightning::penalize()<br/>Verify revocation_key<br/>+ compare state_num"]
    end

    BS1 -->|"2-of-2 multisig"| SP1
    BS2 -->|"Hashlock HTLC"| SP2
    BS3 -->|"Relative timelock CSV"| SP3
    BS4 -->|"Absolute timelock CLTV"| SP4
    BS5 -->|"Conditional branch/revocation"| SP5

    subgraph "Sui ChannelObject (Move Object)"
        CO["ChannelObject State Fields<br/>─────────────<br/>id: UID<br/>local_balance: u64<br/>remote_balance: u64<br/>local_pubkey: vector&lt;u8&gt;<br/>remote_pubkey: vector&lt;u8&gt;<br/>revocation_key: Option&lt;vector&lt;u8&gt;&gt;<br/>state_num: u64<br/>to_self_delay: u64<br/>status: OPEN|CLOSING|CLOSED<br/>close_epoch: u64<br/>htlcs: Table&lt;u64, HTLCEntry&gt;"]
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

### 7. Module Refactoring Priority and Dependencies

```mermaid
graph LR
    subgraph "Phase 1: Configuration & Foundation"
        P1A["config.go add<br/>SuiChainName"]
        P1B["chainreg/sui_params<br/>Network parameters"]
        P1C["keychain/ extension<br/>Ed25519 + secp256k1"]
        P1D["lncfg/sui.go<br/>Sui node configuration"]
    end

    subgraph "Phase 2: Chain Backend Adapters"
        P2A["chainntnfs/suinotify/<br/>Event subscription adapter"]
        P2B["lnwallet/suiwallet/<br/>Wallet adapter<br/>(reuses Bitcoin types)"]
        P2C["input/sui_channel.go<br/>Move contract call encapsulation"]
        P2D["chainfee/sui_estimator<br/>Gas→SatPerKWeight"]
    end

    subgraph "Phase 3: Core Extensions"
        P3A["lnwallet/channel.go<br/>Extract CommitmentBuilder"]
        P3B["funding/manager.go<br/>SuiAssembler branch"]
        P3C["channeldb/<br/>Serialization compatibility"]
        P3D["lnwire/<br/>ChannelID = ObjectID"]
    end

    subgraph "Phase 4: Upper Layer Extensions"
        P4A["contractcourt/<br/>Sui resolver branch"]
        P4B["sweep/<br/>Simplified to balance withdrawal"]
        P4C["graph/ + discovery/<br/>SMT channel verification"]
        P4D["rpcserver.go<br/>Chain type dispatching"]
    end

    subgraph "Phase 5: Integration"
        P5A["config_builder.go<br/>BuildChainControl branch"]
        P5B["server.go<br/>Startup process integration"]
        P5C["zpay32/<br/>lnst invoice encoding"]
        P5D["itest/<br/>Integration tests"]
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

### 8. Data Flow: On-chain vs Off-chain Interaction Panorama

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
    box #2a2a2a Off-chain (Lightning Protocol)
        participant Alice
        participant Bob
    end
    box #3a3a3a On-chain (Sui DAG)
        participant SuiChain as Sui Chain
        participant ChanObj as Channel Object
    end

    Note over Alice, ChanObj: ══════ Phase 1: Channel Establishment ══════

    Alice->>SuiChain: Move Call: open_channel<br/>{keys, balances, delay}
    SuiChain->>ChanObj: Create Shared Object<br/>state_num=0, status=OPEN
    SuiChain-->>Alice: ObjectID confirmation
    SuiChain-->>Bob: ObjectID confirmation

    Note over Alice, ChanObj: ══════ Phase 2: Off-chain Payments (Core Loop) ══════

    loop Each payment/forwarding
        Alice->>Bob: update_add_htlc {hash, amt, expiry}
        Alice->>Bob: commitment_signed {sig_for_state_N+1}
        Bob->>Alice: revoke_and_ack {revocation_key_N}
        Bob->>Alice: commitment_signed {sig_for_state_N+1}
        Alice->>Bob: revoke_and_ack {revocation_key_N}
        Note over Alice, Bob: Both locally update state<br/>state_num++ (no on-chain interaction!)
    end

    Note over Alice, Bob: 💡 During normal operation<br/>Zero on-chain transactions, zero Gas

    Note over Alice, ChanObj: ══════ Phase 3a: Cooperative Close ══════

    Alice->>Bob: shutdown
    Bob->>Alice: shutdown
    Alice->>Bob: closing_signed {final_balances}
    Bob->>Alice: closing_signed {final_balances}

    Alice->>SuiChain: Move Call: close_channel<br/>{both_sigs, final_balances}
    SuiChain->>ChanObj: Verify both signatures<br/>Allocate balances to respective accounts
    SuiChain->>ChanObj: Destroy Object → status=CLOSED

    Note over Alice, ChanObj: ══════ Phase 3b: Force Close (Alternative Path) ══════

    rect #4a4a4a
        Alice->>SuiChain: Move Call: force_close<br/>{state_num, commitment_sig}
        SuiChain->>ChanObj: status → CLOSING<br/>close_epoch = current_epoch

        Note over Bob, ChanObj: Bob has to_self_delay epochs<br/>Detects if it's an old state

        alt Bob detects old state (Breach!)
            Bob->>SuiChain: Move Call: penalize<br/>{revocation_key, proof}
            SuiChain->>ChanObj: All balances awarded to Bob
        else State is correct, after delay expires
            Alice->>SuiChain: Move Call: claim_local_balance<br/>{epoch ≥ close_epoch + delay}
            Bob->>SuiChain: Move Call: claim_remote_balance
            SuiChain->>ChanObj: Balances transferred to respective accounts
        end
    end
```

---

## 2. Refactoring Steps

**1. Configuration Extension + Sui Network Parameters (Zero Intrusion)**

No new `chaintype/` abstraction layer. LND already has the `--chain` and `--network` dual parameters (`lncli --chain=bitcoin --network=mainnet`), naturally supporting multi-chain extension. Modification steps:

| File                              | Modification Content                                                                        |
| --------------------------------- | ------------------------------------------------------------------------------------------- |
| `config.go`                       | Add `SuiChainName = "sui"` constant + `Sui *lncfg.Chain` config item                       |
| `lncfg/sui.go` (new)              | Sui node configuration struct (RPC address, PackageID, epoch interval, etc.)               |
| `chainreg/sui_params.go` (new)    | `SuiNetParams` (network ID, genesis hash, default ports, etc.)                             |
| `chainreg/chainregistry.go`       | Add `"sui"` case in `switch` branch                                                         |

**Core Design Principle — Type Mapping at Adapter Boundary**:

When Sui adapters implement existing LND interfaces, they internally reuse Bitcoin types for semantic mapping without changing interface signatures:

| Bitcoin Type                  | Internal Usage in Sui Adapter                               | Description                       |
| ----------------------------- | ----------------------------------------------------------- | --------------------------------- |
| `wire.OutPoint{Hash, Index}`  | `Hash` ← ObjectID (32B), `Index` = 0                         | Channel identifier                |
| `btcutil.Amount`              | Directly store Sui minimum unit (int64 Mist)                  | Amount mapping                    |
| `wire.MsgTx`                  | `Payload` field carries Move Call serialized bytes            | Transaction wrapper               |
| `chainfee.SatPerKWeight`      | Internally convert GasPrice → SatPerKWeight                   | Fee rate mapping                  |
| `chainhash.Hash`              | Directly store Sui Transaction Digest / ObjectID              | 32B universal                     |
| `lnwire.ShortChannelID`       | Store truncated ObjectID in 8 bytes + TLV extension for full 32B | Routing protocol compatibility |

**2. Chain Backend Interfaces — Unchanged, Only New Sui Implementation**

**Do not modify** existing interface signatures. LND's core chain backend interface signatures remain as-is; the Sui adapter acts as a new implementation, performing semantic conversion internally:

- **`ChainNotifier`** — Adapter interprets `txid` in `RegisterConfirmationsNtfn(txid *chainhash.Hash, ...)` as Transaction Digest, subscribes to transaction finalization events
- **`BlockChainIO`** — Adapter interprets `GetUtxo(outpoint *wire.OutPoint, ...)` as querying Channel Object state
- **`Signer`** — Adapter interprets `tx` in `SignOutputRaw(tx *wire.MsgTx, ...)` as the serialization carrier for Move Calls, and signs the content with Sui signature scheme
- **`WalletController`** — The most modified adapter; internally performs semantic conversion from UTXO → balance (returns a "virtual UTXO" in `ListUnspentWitness`)

**3. Extend `ChainControl` + `config_builder.go`**

Modify `ChainControl` struct in `chainreg/chainregistry.go`:

- Add `ChainName string` field (`"bitcoin"` or `"sui"`)
- Add `"sui"` branch in `BuildChainControl` function in `config_builder.go` to create Sui adapter instances and inject into `ChainControl`
- Create `chainreg/sui_params.go` defining `SuiNetParams` (network ID, genesis hash, default ports, epoch interval)

**4. Implement Sui Chain Notification Backend `chainntnfs/suinotify/`**

Implement the `ChainNotifier` interface, core mapping:

| Bitcoin Concept                              | Sui Implementation                                                       |
| --------------------------------------------- | ------------------------------------------------------------------------ |
| `RegisterConfirmationsNtfn(txid, numConfs)`   | Subscribe to transaction finalization events                             |
| `RegisterSpendNtfn(outpoint)`                  | Subscribe to Channel Object state changes (triggered via Move Events)    |
| `RegisterBlockEpochNtfn()`                     | Subscribe to Sui Checkpoint/Epoch advancement events                     |
| Reorg detection                               | Greatly simplified (DAG has no classic reorgs)                           |
| `GetBlock()` / `GetBlockHash()`                | Query Checkpoint information                                             |

**5. Implement Sui Wallet `lnwallet/suiwallet/`**

Implement the adapted `WalletController` interface:

| Bitcoin Operation                           | Sui Operation                                                                 |
| -------------------------------------------- | ---------------------------------------------------------------------------- |
| `ListUnspentWitness()` — list UTXOs          | `GetBalance()` — query account balance                                       |
| `LeaseOutput(OutPoint)` — lock UTXO          | `ReserveBalance(amount)` — reserve balance                                   |
| `SendOutputs([]*wire.TxOut)` — build TX      | `MoveCall(package, module, func, args)` — call contract                      |
| `FundPsbt()` / `SignPsbt()`                  | `BuildMoveCall()` / `SignTransaction()` — build Sui transaction              |
| Coin selection (`selectInputs`)              | Not needed (deduct directly from balance)                                    |
| Change address generation                     | Not needed                                                                   |

Key management: reuse the `KeyFamily` system from [derivation.go] (../../../keychain/derivation.go), add Sui coinType, key derivation supporting both secp256k1 and Ed25519 dual paths.

**6. Sui On-Chain Channel Logic — Implemented via MoveVM Contracts**

The original script logic on Bitcoin is replaced by Move contracts on Sui.

**6a. Move Module `lightning` Core Functions**:

```move
public entry fun open_channel(...) // Create Channel SharedObject
public entry fun close_channel(...) // Cooperative close
public entry fun force_close(...) // Force close
public entry fun htlc_claim(...) // HTLC success path
public entry fun htlc_timeout(...) // HTLC timeout path
public entry fun penalize(...) // Breach penalty path
```

Create `input/sui_channel.go` on the Go side to encapsulate construction functions for the above contract calls.

**7. Channel Identifier System Redesign**

- Modify [channel_id.go] (../../../lnwire/channel_id.go) — `NewChanIDFromOutPoint` on Sui chain directly uses the first 32 bytes of ObjectID
- Modify [short_channel_id.go] (../../../lnwire/short_channel_id.go) — In Sui mode, `ShortChannelID` uses ObjectID (32 bytes)
- Update [channel.go] (../../../lnwallet/channel.go) — Change `FundingOutpoint` field to `chaintype.ChannelPoint`
- Modify [channel_edge_info.go] (../../../graph/db/models/channel_edge_info.go) — Rename/extend fields to support Sui public key formats

**8. Channel State Machine Adaptation**

Refactoring strategy for [channel.go] (../../../lnwallet/channel.go) is to **separate protocol logic from on-chain operations**:

- Extract interface `CommitmentBuilder`: Bitcoin implementation constructs commitment transactions; Sui implementation constructs Move Call state updates
- Extract interface `ScriptEngine`: Sui implementation invokes Move contract logic
- Keep core protocol logic unchanged: state number (`StateNum`), HTLC management (`UpdateLog`), etc.

**9. Funding Manager Adaptation**

Modify [manager.go] (../../../funding/manager.go):

- `waitForFundingConfirmation` — In Sui mode, wait for transaction to be Finalized
- Funding transaction construction switches to new `chanfunding.SuiAssembler` (directly call open_channel contract)

**10. Contract Court Adaptation**

Modify all resolvers in [contractcourt] (../../../contractcourt/) to call the corresponding Move contract methods.

**11. Sweep Module Simplification**

Add Sui mode in [sweep] (../../../sweep/), simplified to call contract to withdraw balance back to personal account.

**12. Graph and Discovery Adaptation**

- Modify [builder.go] (../../../graph/builder.go) — Sui queries whether Channel Object still exists
- Modify [gossiper.go] (../../../discovery/gossiper.go) — Sui verifies Channel Object existence + both keys match

**13. Fee Rate System Adaptation**

- Add `SuiEstimator` implementing `Estimator` interface in [chainfee] (../../../chainfee/)
- Modify [rates.go] (../../../chainfee/rates.go) — Add `GasPrice` type

**14. RPC and Invoice Adaptation**

- Modify `GetInfo` in [rpcserver.go] (../../../rpcserver.go) — Return `"sui"`
- Modify [zpay32] (../../../zpay32/) — Add Sui HRP

**15. Configuration and Startup**

- Modify [config.go] (../../../config.go) — Add `Sui *lncfg.Chain`
- Modify [config_builder.go] (../../../config_builder.go) — Add Sui branch in `BuildChainControl`
- Modify [server.go] (../../../server.go) — Initialize corresponding subsystems based on chain type

---

## 3. Complete List of Required Sui Capabilities

### P0 — Core Capabilities (Lightning Network cannot run without these)

| #   | Capability                         | Detailed Requirements                                                                                     | Corresponding LND Module                                   |
| --- | ---------------------------------- | -------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| 1   | **Move Lightning Contracts**       | Implement open_channel/close_channel/force_close/htlc_claim/timeout/penalize                              | `input/sui_channel.go` + Move contracts                    |
| 2   | **Shared Object**                  | Channel Object must be operable by both parties via contracts                                             | `lnwallet/suiwallet/`                                      |
| 3   | **Hashlock**                       | Move contract must have built-in SHA256 preimage verification logic                                      | HTLC contract                                              |
| 4   | **Time Reference**                  | Contracts can read current epoch or clock for timelock comparison                                        | CSV/CLTV equivalent                                        |
| 5   | **State Version Control**           | Channel Object must have monotonically increasing `state_num` to prevent replay of old states            | Commitment state sync                                      |
| 6   | **Event Subscription API**          | Subscribe to state change events by ObjectID or PackageID                                                | `chainntnfs/suinotify/`                                    |
| 7   | **Finality Notification**           | Callback notification of finalization status after transaction submission                                 | Confirmation flow                                          |
| 8   | **Multi-signature Verification**    | Move contract must have built-in secp256k1 signature verification                                        | Transaction authorization                                  |
| 9   | **Object Query API**                | Query full object state by ObjectID                                                                      | Equivalent of `BlockChainIO`                               |
| 10  | **Atomic State Updates**            | Contract execution state changes must either all take effect or all roll back                             | Channel state consistency                                   |
| 11  | **Go SDK**                          | Support Sui transaction building, signing, submission, and event subscription                            | [keychain] (../../../keychain/) + adapter layer            |

### P1 — Important Capabilities (Affect Security and Extensibility)

| #   | Capability                        | Detailed Requirements                                                             | Corresponding LND Module                          |
| --- | --------------------------------- | -------------------------------------------------------------------------------- | ------------------------------------------------- |
| 12  | **Merkle Proof (SMT Proof)**      | Provide Binary+Sparse Merkle Tree proof of object existence/non-existence        | [discovery] (../../../discovery/) channel verification |
| 13  | **Historical State Query**        | Query historical state of Channel Object by version/epoch (for dispute arbitration) | [contractcourt] (../../../contractcourt/)         |
| 14  | **Gas Estimation API**            | Estimate gas consumption of Move contract calls                                  | `chainfee/`                                       |
| 15  | **Batch Operations**              | Atomically operate on multiple objects in a single transaction (batch HTLC settlement) | [sweep] (../../../sweep/) batch processing        |
| 16  | **Object Destruction Notification**| Generate subscribable event when Channel Object is destroyed (channel closed)    | Channel liveness check in [builder.go] (../../../graph/builder.go) |
| 17  | **Node Discovery/P2P**            | P2P connection information for Sui network nodes (for LN gossip bootstrapping)   | [chainreg] (../../../chainreg/) DNS seeds         |

### P2 — Optimization Capabilities (Enhance Performance and User Experience)

| #   | Capability                | Detailed Requirements                                                             |
| --- | ------------------------- | -------------------------------------------------------------------------------- |
| 18  | **Light Client Mode**     | Sui light client (only verify Merkle proofs, not full state)                     |
| 19  | **Watchtower Support**    | Third parties can monitor Channel Object state and automatically submit penalty transactions on breach |
| 20  | **Atomic Cross-Chain Operations** | Support atomic swaps / cross-chain HTLC between Bitcoin↔Sui (if dual-chain interoperability is needed) |

---

## 4. Verification

- **Unit Tests**: Each new Sui implementation independently tested, mocking Sui RPC
- **Contract Tests**: Use `sui move test` to verify contract logic
- **Integration Tests**: Modify [itest] (../../../itest/) framework, cover full scenarios: open channel, send payment, close, penalty, etc.
- **Commands**: `make itest tags=sui`
- **Manual Checks**: `lncli --chain=sui getinfo`

## 5. Decision Records

- **Adaptation Strategy**: Use **Adapter Pattern**. LND invokes Move contracts on Sui through adapters to replace original Bitcoin script logic.
- **Parallel Development**: Integrate Sui first to advance upper-layer logic development before Setu's general VM is ready.
- **ObjectID**: On Sui, use 32-byte ObjectID directly to identify channels.