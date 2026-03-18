# Plan: Lightning Network Adaptation for Setu — Refactoring Document

## 0. Overview

Refactor LND Lightning Network to simultaneously support a **Bitcoin + Setu dual system**. Setu is a DAG ledger based on the object-account model, with cryptography supporting secp256k1, Ed25519, and Secp256r1, and channels identified by a 32-byte `ObjectID`.

> **⚠️ Key Fact: Setu currently lacks a general-purpose programmable virtual machine.** Setu's `setu-runtime` is a **pseudo-implementation** (pre-simplified) of a Move VM, supporting only hardcoded operations such as Transfer (full/partial), Query (balance/object queries), SubnetRegister, and UserRegister. The "custom 10-opcode interpreter (ProgramTx)" described in previous design documents **is not yet implemented**; the `RuntimeExecutor` of `setu-runtime` currently has only two execution paths: `execute_transfer()` and `execute_query()`.
>
> **Adaptation Strategy Adjustment**: Shift from "building Lightning Network contracts based on ProgramTx opcodes" to **"adding hardcoded Lightning Channel EventTypes and corresponding execution logic on the Setu side"**. This effectively implements native channel lifecycle management operations (`ChannelOpen`, `ChannelClose`, `ChannelForceClose`, `HTLCClaim`, `ChannelPenalize`, etc.) directly at the Setu Validator/Runtime layer, rather than through generic VM instruction orchestration.

Refactoring Strategy: **Zero-intrusion Adapter Pattern**. No new abstraction layers, no changes to existing interface signatures; instead, implement Setu adapters at the interface implementation level — the Setu adapter internally reuses Bitcoin types (e.g., `wire.OutPoint.Hash` to store ObjectID, `btcutil.Amount` for unit mapping, `wire.MsgTx` to carry Setu Event serialized bytes) and performs semantic conversion at the implementation boundaries. Existing Bitcoin code paths remain completely unaffected; Setu is inserted as a new `ChainControl` implementation, selectable via `lncli --chain=setu`.

Core refactoring workload distribution: **Setu backend adapter implementation in lnd (35%) → Hardcoded Lightning primitives on the Setu chain side (20%) → Upper module extensions (25%) → Configuration/startup/testing integration (20%)**.

---

## 1. Process Interaction Diagrams

The following 8 diagrams cover:

1. **Architecture Overview** — Layer and module relationships in the dual-chain abstraction
2. **Channel Lifecycle Comparison** — Clear side-by-side difference between Bitcoin and Setu processes
3. **Channel Opening Sequence** — Detailed interaction timeline between both parties and the chain
4. **Multi-hop HTLC Payment** — Full sequence for normal flow and exceptional timeout
5. **Force Close & Dispute Resolution** — Complete decision flow including breach penalty
6. **Bitcoin Script → Setu Hardcoded EventType Mapping** — How each Bitcoin contract operation translates to Setu native operations
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

    subgraph "Setu Adapter (New)"
        SETU_Notify["setunotify/<br/>Object event subscription<br/>(implements ChainNotifier)"]
        SETU_Wallet["setuwallet/<br/>Balance operations<br/>(implements WalletController)"]
        SETU_Program["input/setu_channel.go<br/>Channel operation Event construction"]
        SETU_Fee["chainfee/setu_estimator<br/>GasPrice → SatPerKWeight"]
        SETU_Chain["Setu DAG Chain<br/>Object Account Model<br/>+ Lightning hardcoded primitives"]
        SETU_Adapt["Type Mapping Strategy<br/>OutPoint.Hash ← ObjectID<br/>Amount ← SetuUnit<br/>MsgTx ← Event bytes"]
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
    IF_Notify -->|"chain=setu"| SETU_Notify
    IF_Wallet -->|"chain=setu"| SETU_Wallet

    BTC_Notify --> BTC_Chain
    BTC_Wallet --> BTC_Chain
    SETU_Notify --> SETU_Chain
    SETU_Wallet --> SETU_Chain

    SETU_Adapt -.->|"Internally reused"| SETU_Notify
    SETU_Adapt -.->|"Internally reused"| SETU_Wallet
    IF_Wallet --- SETU_Adapt


    style IF_Notify fill:#4a9eff,color:#fff
    style IF_Wallet fill:#4a9eff,color:#fff
    style IF_Signer fill:#4a9eff,color:#fff
    style IF_IO fill:#4a9eff,color:#fff
    style CC fill:#4a9eff,color:#fff
    style SETU_Notify fill:#2ecc71,color:#fff
    style SETU_Wallet fill:#2ecc71,color:#fff
    style SETU_Program fill:#2ecc71,color:#fff
    style SETU_Fee fill:#2ecc71,color:#fff
    style SETU_Chain fill:#2ecc71,color:#fff
    style SETU_Adapt fill:#9b59b6,color:#fff
    style BTC_Chain fill:#f39c12,color:#fff

    %% Hide last two edges (forced layout edges)
    linkStyle 22 stroke-width:0, stroke:transparent;
```

---

### 2. Channel Lifecycle Comparison (Bitcoin vs Setu)

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

- Figure 2: Setu Lightning Channel Lifecycle

```mermaid
flowchart LR
    S1["Balance check<br/>balance check"] --> S2["Create Channel<br/>Shared Object"]
    S2 --> S3["DAG finalization<br/>⏱ < 1 sec"]
    S3 --> S4["ObjectID<br/>Serves as channel identifier"]
    S4 --> S5["Sign state updates<br/>state_num increments"]
    S5 --> S6["HTLC updates<br/>Hardcoded Channel logic"]
    S6 --> S7{"Closing Method"}
    S7 -->|Cooperative| S8["Call close()<br/>Allocate balance back to accounts"]
    S7 -->|Force| S9["Call force_close()<br/>Epoch delay wait"]
    S9 --> S10["Call withdraw()<br/>Balance back to accounts"]
    S7 -->|Breach| S11["Call penalize()<br/>Revocation key + old state"]

    style S3 fill:#2ecc71,color:#fff
    style S10 fill:#2ecc71,color:#fff
```

---

### 3. Channel Opening Sequence Diagram (Setu Adaptation)

```mermaid
sequenceDiagram
    participant Alice as Alice (Initiator)
    participant Bob as Bob (Recipient)
    participant Setu as Setu DAG Chain
    participant LND_A as Alice LND
    participant LND_B as Bob LND

    Note over Alice, Bob: ═══ Channel Opening Protocol ═══

    Alice->>LND_A: openchannel(bob_pubkey, amount)
    LND_A->>LND_A: Check balance ≥ amount + gas
    LND_A->>LND_B: MsgOpenChannel<br/>{chain_hash, amount, push_amt,<br/>channel_flags, funding_key}

    LND_B->>LND_B: Validate parameters<br/>Check balance (if push)
    LND_B->>LND_A: MsgAcceptChannel<br/>{min_depth=1, funding_key,<br/>revocation_basepoint, ...}

    Note over LND_A, Setu: ═══ Setu On-Chain Operations ═══

    LND_A->>LND_A: Build ChannelOpen Event:<br/>Create Channel Object<br/>{local_key, remote_key,<br/>local_balance, remote_balance,<br/>state_num=0, to_self_delay}

    LND_A->>Setu: Submit ChannelOpen Event<br/>(create shared object)
    Setu-->>Setu: DAG consensus execution<br/>Create Channel Object
    Setu-->>LND_A: Finalization notification<br/>ObjectID = 0xABC...

    LND_A->>LND_B: MsgFundingCreated<br/>{object_id, initial_commitment_sig}

    LND_B->>LND_B: Verify Object exists on-chain<br/>Verify signature
    LND_B->>LND_A: MsgFundingSigned<br/>{commitment_sig}

    Note over LND_A, Setu: ═══ Wait for finalization (very fast) ═══

    Setu-->>LND_A: Object finalized ✓
    Setu-->>LND_B: Object finalized ✓

    LND_A->>LND_B: MsgChannelReady<br/>{channel_id = ObjectID}
    LND_B->>LND_A: MsgChannelReady<br/>{channel_id = ObjectID}

    Note over Alice, Bob: ✅ Channel ready, payments can be sent<br/>ShortChanID = ObjectID

    rect rgb(200, 235, 200)
        Note over Alice, Setu: Compared to Bitcoin: This process shortens from ~60min to ~2sec
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
    participant Setu as Setu Chain<br/>(only on dispute)

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

    Note over A, Setu: ═══ Exception Case: HTLC Timeout ═══

    rect
        alt B does not reveal preimage and expiry reached
            N1->>Setu: Call htlc_timeout()<br/>Hardcoded logic: current_epoch ≥ expiry
            Setu-->>N1: HTLC amount returned to N1
            N1->>A: update_fail_htlc
        end
    end
```

---

### 5. Force Close and Dispute Resolution Flowchart

```mermaid
flowchart TB
    Start["Unilateral close triggered<br/>(Peer offline / unresponsive)"]

    Start --> Publish["Submit latest commitment state to Setu<br/>Call Channel Object's<br/>force_close(state_num, sig)"]

    Publish --> SetuExec["Setu DAG execution<br/>Hardcoded ForceClose logic"]
    SetuExec --> Validate{"Verify signature +<br/>state_num ≥ on-chain record?"}

    Validate -->|"Failed"| Reject["Transaction rejected<br/>State unchanged"]
    Validate -->|"Success"| Freeze["Channel Object<br/>enters CLOSING state<br/>Record close_epoch"]

    Freeze --> Parallel["Parallel Processing"]

    Parallel --> LocalBalance["Local Balance Processing"]
    Parallel --> RemoteBalance["Remote Balance Processing"]
    Parallel --> HTLCs["Active HTLC Processing"]

    subgraph "Local Balance (to_self_delay protected)"
        LocalBalance --> WaitCSV{"Wait relative delay<br/>current_epoch ≥<br/>close_epoch + to_self_delay?"}
        WaitCSV -->|"Not yet"| WaitCSV
        WaitCSV -->|"Expired"| ClaimLocal["Call claim_local()<br/>Balance transferred to local account"]
    end

    subgraph "Remote Balance"
        RemoteBalance --> ClaimRemote["Remote can call<br/>claim_remote() immediately<br/>(no delay)"]
    end

    subgraph "HTLC Resolution"
        HTLCs --> HTLCType{"HTLC Direction?"}
        HTLCType -->|"Outgoing HTLC"| TimeoutPath["Wait for CLTV expiry<br/>current_epoch ≥ htlc.expiry"]
        TimeoutPath --> ClaimTimeout["Call htlc_timeout()<br/>Funds returned"]

        HTLCType -->|"Incoming HTLC"| PreimagePath["Hold preimage?"]
        PreimagePath -->|"Yes"| ClaimSuccess["Call htlc_success(preimage)<br/>Hardcoded SHA256 verification"]
        PreimagePath -->|"No"| WaitExpiry["Wait for timeout then<br/>remote can retrieve"]
    end

    subgraph "Breach Detection"
        Freeze --> Monitor["ChainWatcher monitors<br/>Object state changes"]
        Monitor --> OldState{"Submitted state_num<br/>< locally known latest?"}
        OldState -->|"Yes = Breach!"| Penalize["Call penalize()<br/>Submit revocation key +<br/>proof of old state"]
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

### 6. Bitcoin Script → Setu Hardcoded Lightning Primitive Mapping

> **Note**: Since Setu currently has no general-purpose VM (`setu-runtime` is only a simplified precursor to Move VM), contract logic cannot be implemented via opcode orchestration. Instead, new hardcoded Lightning Channel operation types (new `EventType` + corresponding execution functions) are added in Setu's `RuntimeExecutor` and executed directly by the Validator.

```mermaid
graph TB
    subgraph "Bitcoin Script (Existing)"
        BS1["OP_2 &lt;key1&gt; &lt;key2&gt; OP_2<br/>OP_CHECKMULTISIG"]
        BS2["OP_HASH160 &lt;hash&gt; OP_EQUAL<br/>OP_CHECKSIG"]
        BS3["&lt;delay&gt; OP_CSV<br/>OP_DROP OP_CHECKSIG"]
        BS4["&lt;expiry&gt; OP_CLTV<br/>OP_DROP OP_CHECKSIG"]
        BS5["OP_IF<br/>  revocation_path<br/>OP_ELSE<br/>  normal_path<br/>OP_ENDIF"]
    end

    subgraph "Setu Hardcoded EventType (New)"
        SP1["EventType::ChannelOpen<br/>execute_channel_open()<br/>Verify both signatures + lock balances<br/>Create SharedObject"]
        SP2["EventType::HTLCClaim<br/>execute_htlc_claim()<br/>SHA256(preimage)==hash<br/>Verify signature + transfer balance"]
        SP3["EventType::ChannelClaim<br/>execute_channel_claim()<br/>Check current_vlc ≥<br/>close_vlc + to_self_delay"]
        SP4["EventType::HTLCTimeout<br/>execute_htlc_timeout()<br/>Check current_vlc ≥<br/>htlc.expiry_vlc"]
        SP5["EventType::ChannelPenalize<br/>execute_penalize()<br/>Verify revocation_key<br/>+ compare state_num"]
    end

    BS1 -->|"2-of-2 multisig"| SP1
    BS2 -->|"Hashlock HTLC"| SP2
    BS3 -->|"Relative timelock CSV"| SP3
    BS4 -->|"Absolute timelock CLTV"| SP4
    BS5 -->|"Conditional branch/revocation"| SP5

    subgraph "Setu ChannelObject (SharedObject)"
        CO["ChannelObject State Fields<br/>─────────────<br/>object_id: ObjectId [32]byte<br/>local_balance: u64<br/>remote_balance: u64<br/>local_pubkey: PublicKey<br/>remote_pubkey: PublicKey<br/>revocation_key: PublicKey<br/>state_num: u64<br/>to_self_delay: u64<br/>status: OPEN|CLOSING|CLOSED<br/>close_vlc: u64<br/>htlcs: Vec&lt;HTLCEntry&gt;"]
    end

    subgraph "Setu RuntimeExecutor Extension"
        RE["executor.rs new methods<br/>─────────────<br/>execute_channel_open()<br/>execute_channel_close()<br/>execute_channel_force_close()<br/>execute_channel_claim()<br/>execute_htlc_claim()<br/>execute_htlc_timeout()<br/>execute_channel_penalize()"]
    end

    SP1 --> CO
    SP2 --> CO
    SP3 --> CO
    SP4 --> CO
    SP5 --> CO
    RE --> CO

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
    style RE fill:#9b59b6,color:#fff
```

---

### 7. Module Refactoring Priority and Dependencies

```mermaid
graph LR
    subgraph "Phase 1: Configuration & Foundation"
        P1A["config.go add<br/>SetuChainName"]
        P1B["chainreg/setu_params<br/>Network parameters"]
        P1C["keychain/ extension<br/>Ed25519 + secp256k1"]
        P1D["lncfg/setu.go<br/>Setu node configuration"]
    end

    subgraph "Phase 2: Chain Backend Adapters"
        P2A["chainntnfs/setunotify/<br/>Event subscription adapter"]
        P2B["lnwallet/setuwallet/<br/>Wallet adapter<br/>(reuses Bitcoin types)"]
        P2C["input/setu_channel.go<br/>Channel Event construction<br/>+ Setu side hardcoded primitives"]
        P2D["chainfee/setu_estimator<br/>Gas→SatPerKWeight"]
    end

    subgraph "Phase 3: Core Extensions"
        P3A["lnwallet/channel.go<br/>Extract CommitmentBuilder"]
        P3B["funding/manager.go<br/>SetuAssembler branch"]
        P3C["channeldb/<br/>Serialization compatibility"]
        P3D["lnwire/<br/>ChannelID = ObjectID"]
    end

    subgraph "Phase 4: Upper Layer Extensions"
        P4A["contractcourt/<br/>Setu resolver branch"]
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
    box #3a3a3a On-chain (Setu DAG)
        participant SetuChain as Setu Chain
        participant ChanObj as Channel Object
    end

    Note over Alice, ChanObj: ══════ Phase 1: Channel Establishment ══════

    Alice->>SetuChain: Event: ChannelOpen<br/>{keys, balances, delay}
    SetuChain->>ChanObj: Create Shared Object<br/>state_num=0, status=OPEN
    SetuChain-->>Alice: ObjectID confirmation
    SetuChain-->>Bob: ObjectID confirmation

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

    Alice->>SetuChain: Event: CooperativeClose<br/>{both_sigs, final_balances}
    SetuChain->>ChanObj: Verify both signatures<br/>Allocate balances to respective accounts
    SetuChain->>ChanObj: Destroy Object → status=CLOSED

    Note over Alice, ChanObj: ══════ Phase 3b: Force Close (Alternative Path) ══════

    rect #4a4a4a
        Alice->>SetuChain: Event: ForceClose<br/>{state_num, commitment_sig}
        SetuChain->>ChanObj: status → CLOSING<br/>close_vlc = current_vlc

        Note over Bob, ChanObj: Bob has to_self_delay VLC ticks<br/>Detects if it's an old state

        alt Bob detects old state (Breach!)
            Bob->>SetuChain: Event: Penalize<br/>{revocation_key, proof}
            SetuChain->>ChanObj: All balances awarded to Bob
        else State is correct, after delay expires
            Alice->>SetuChain: Event: ClaimLocal<br/>{vlc ≥ close_vlc + delay}
            Bob->>SetuChain: Event: ClaimRemote
            SetuChain->>ChanObj: Balances transferred to respective accounts
        end
    end
```

---

## 2. Refactoring Steps

**1. Configuration Extension + Setu Network Parameters (Zero Intrusion)**

No new `chaintype/` abstraction layer. LND already has the `--chain` and `--network` dual parameters (`lncli --chain=bitcoin --network=mainnet`), naturally supporting multi-chain extension. Modification steps:

| File                              | Modification Content                                                                        |
| --------------------------------- | ------------------------------------------------------------------------------------------- |
| `config.go`                       | Add `SetuChainName = "setu"` constant + `Setu *lncfg.Chain` config item                     |
| `lncfg/setu.go` (new)             | Setu node configuration struct (RPC address, SDK path, epoch interval, etc.)                |
| `chainreg/setu_params.go` (new)   | `SetuNetParams` (network ID, genesis hash, default ports, etc.)                             |
| `chainreg/chainregistry.go`       | Add `"setu"` case in `switch` branch                                                        |

**Core Design Principle — Type Mapping at Adapter Boundary**:

When Setu adapters implement existing LND interfaces, they internally reuse Bitcoin types for semantic mapping without changing interface signatures:

| Bitcoin Type                  | Internal Usage in Setu Adapter                    | Description                |
| ----------------------------- | ------------------------------------------------- | -------------------------- |
| `wire.OutPoint{Hash, Index}`  | `Hash` ← ObjectID (32B), `Index` = 0              | Channel identifier         |
| `btcutil.Amount`              | Directly store Setu minimum unit (int64)          | Amount mapping             |
| `wire.MsgTx`                  | `Payload` field carries Setu Event serialized bytes | Transaction wrapper        |
| `chainfee.SatPerKWeight`      | Internally convert GasPrice → SatPerKWeight        | Fee rate mapping           |
| `chainhash.Hash`              | Directly store Setu TxDigest / ObjectID            | 32B universal              |
| `lnwire.ShortChannelID`       | Store truncated ObjectID in 8 bytes + TLV extension for full 32B | Routing protocol compatibility |

**2. Chain Backend Interfaces — Unchanged, Only New Setu Implementation**

**Do not modify** existing interface signatures. LND's core chain backend interface signatures remain as-is; the Setu adapter acts as a new implementation, performing semantic conversion internally:

- **`ChainNotifier`** — Adapter interprets `txid` in `RegisterConfirmationsNtfn(txid *chainhash.Hash, ...)` as ObjectID, subscribes to object finalization events
- **`BlockChainIO`** — Adapter interprets `GetUtxo(outpoint *wire.OutPoint, ...)` as querying Channel Object state
- **`Signer`** — Adapter interprets `tx` in `SignOutputRaw(tx *wire.MsgTx, ...)` as the serialization carrier for Setu Events, and signs the content with Setu signature scheme
- **`WalletController`** — The most modified adapter; internally performs semantic conversion from UTXO → balance (returns a "virtual UTXO" in `ListUnspentWitness`)

**3. Extend `ChainControl` + `config_builder.go`**

Modify `ChainControl` struct in `chainreg/chainregistry.go`:

- Add `ChainName string` field (`"bitcoin"` or `"setu"`)
- Add `"setu"` branch in `BuildChainControl` function in `config_builder.go` to create Setu adapter instances and inject into `ChainControl`
- Create `chainreg/setu_params.go` defining `SetuNetParams` (network ID, genesis hash, default ports, epoch interval)

**4. Implement Setu Chain Notification Backend `chainntnfs/setunotify/`**

Implement the `ChainNotifier` interface, core mapping:

| Bitcoin Concept                              | Setu Implementation                                                      |
| --------------------------------------------- | ------------------------------------------------------------------------ |
| `RegisterConfirmationsNtfn(txid, numConfs)`   | Subscribe to object finalization events (DAG finality usually 1 confirmation) |
| `RegisterSpendNtfn(outpoint)`                  | Subscribe to Channel Object state changes (balance changes/object destruction) |
| `RegisterBlockEpochNtfn()`                     | Subscribe to Setu epoch advancement events                               |
| Reorg detection                               | Greatly simplified (DAG has no classic reorgs)                           |
| `GetBlock()` / `GetBlockHash()`                | Query epoch information / DAG round data                                 |

**5. Implement Setu Wallet `lnwallet/setuwallet/`**

Implement the adapted `WalletController` interface:

| Bitcoin Operation                           | Setu Operation                                                           |
| -------------------------------------------- | ------------------------------------------------------------------------ |
| `ListUnspentWitness()` — list UTXOs          | `GetBalance()` — query account balance                                   |
| `LeaseOutput(OutPoint)` — lock UTXO          | `ReserveBalance(amount)` — reserve balance                               |
| `SendOutputs([]*wire.TxOut)` — build TX      | `Transfer(to, amount)` — call transfer                                   |
| `FundPsbt()` / `SignPsbt()`                  | `BuildChannelEvent()` / `SignChannelEvent()` — build Setu Channel Event  |
| Coin selection (`selectInputs`)              | Not needed (deduct directly from balance)                                |
| Change address generation                     | Not needed                                                               |

Key management: reuse the `KeyFamily` system from [derivation.go] (../../../keychain/derivation.go), add Setu coinType, key derivation supporting both secp256k1 and Ed25519 dual paths.

**6. Setu On-Chain Channel Logic — Based on Hardcoded EventType + RuntimeExecutor Extension**

> ⚠️ Setu currently has no VM/opcodes; `setu-runtime` only supports Transfer/Query/SubnetRegister/UserRegister.
> New hardcoded Lightning Channel execution logic must be added in Rust's `RuntimeExecutor`, rather than through an interpreter.

**6a. Add EventType (Rust side `types/src/event.rs`)**:

```rust
// Add to EventType enum
ChannelOpen,        // Create ChannelObject (SharedObject)
ChannelClose,       // Cooperative close, release balances
ChannelForceClose,  // Unilateral force close, initiate timelock
ChannelClaimLocal,  // Claim to_local output (after relative timelock)
ChannelClaimRemote, // Claim to_remote output
HTLCClaim,          // Preimage unlock of HTLC
HTLCTimeout,        // Timeout recovery of HTLC
ChannelPenalize,    // Revocation penalty (when old state is broadcast)
```

**6b. Add ChannelObject Data Structure (Rust side `types/src/`)**:

```rust
pub struct ChannelData {
    pub channel_id: [u8; 32],
    pub local_key: PublicKey,       // secp256k1
    pub remote_key: PublicKey,
    pub local_balance: u64,
    pub remote_balance: u64,
    pub state_num: u64,
    pub status: ChannelStatus,      // Open | ForceClosing | Closed
    pub revocation_key: Option<PublicKey>,
    pub csv_delay: u64,             // VLC tick count
    pub force_close_vlc: Option<VectorClock>,
    pub htlcs: Vec<HTLCEntry>,
}
pub type ChannelObject = Object<ChannelData>; // SharedObject type

pub struct HTLCEntry {
    pub payment_hash: [u8; 32],
    pub amount: u64,
    pub expiry_vlc: u64,            // VLC logical time as timeout
    pub direction: HTLCDirection,   // Offered | Received
}
```

**6c. RuntimeExecutor Extension (Rust side `crates/setu-runtime/src/executor.rs`)**:

Add the following hardcoded execution functions (at the same level as existing `execute_transfer()`):

| Function                         | Functionality                                          | Corresponding Bitcoin Script         |
| -------------------------------- | ------------------------------------------------------ | ------------------------------------ |
| `execute_channel_open()`         | Create ChannelObject, verify both signatures           | funding tx 2-of-2 multisig           |
| `execute_channel_close()`        | Both sign → allocate balances → delete object          | cooperative close tx                 |
| `execute_channel_force_close()`  | Single sign → record force_close_vlc → lock csv_delay | commitment tx broadcast               |
| `execute_channel_claim_local()`  | Verify `current_vlc ≥ force_close_vlc + csv_delay`    | to_local CSV timelock                 |
| `execute_channel_claim_remote()` | Verify remote signature → release balance              | to_remote immediate output            |
| `execute_htlc_claim()`           | Verify `SHA256(preimage) == payment_hash` → release amount | HTLC success path                   |
| `execute_htlc_timeout()`         | Verify `current_vlc ≥ expiry_vlc` → refund amount      | HTLC timeout path                     |
| `execute_channel_penalize()`     | Verify revocation_key signature → confiscate all balances | breach remedy tx                     |

**6d. Timelock Mapping**:

- **Relative timelock (CSV equivalent)**: `current_vlc_tick ≥ force_close_vlc_tick + csv_delay` (VLC logical time difference)
- **Absolute timelock (CLTV equivalent)**: `current_vlc_tick ≥ expiry_vlc` (VLC logical time point)

Create `input/setu_channel.go` on the Go side to encapsulate construction functions for the above Events (similar to the 3275 lines of Bitcoin Script construction in existing [script_utils.go] (../../../input/script_utils.go)).

**7. Channel Identifier System Redesign**

- Modify [channel_id.go] (../../../lnwire/channel_id.go) — `NewChanIDFromOutPoint` on Setu chain directly uses the first 32 bytes of ObjectID, no XOR transformation needed
- Modify [short_channel_id.go] (../../../lnwire/short_channel_id.go) — In Setu mode, `ShortChannelID` uses ObjectID (32 bytes). Encoding in routing protocol messages needs to be extended to variable length or use TLV extension fields to carry full ObjectID
- Update [channel.go] (../../../lnwallet/channel.go) — Change `FundingOutpoint` field to `chaintype.ChannelPoint`; database schema must support serialization of both Bitcoin OutPoint and Setu ObjectID formats
- Modify [channel_edge_info.go] (../../../graph/db/models/channel_edge_info.go) — Rename `BitcoinKey1Bytes`/`BitcoinKey2Bytes` to `ChainKey1Bytes`/`ChainKey2Bytes`, or keep Bitcoin fields and add `SetuKey1Bytes`/`SetuKey2Bytes`

**8. Channel State Machine Adaptation**

Refactoring strategy for [channel.go] (../../../lnwallet/channel.go) (10185 lines) is to **separate protocol logic from on-chain operations**:

- Extract interface `CommitmentBuilder`: Bitcoin implementation constructs `wire.MsgTx` commitment transactions; Setu implementation constructs Channel Event (ChannelOpen/ChannelClose etc.) state updates
- Extract interface `ScriptEngine`: Bitcoin implementation uses `txscript` to verify/construct scripts; Setu implementation calls RuntimeExecutor's hardcoded Channel logic (no general VM)
- Modify [commitment.go] (../../../lnwallet/commitment.go) — Keep key derivation for `CommitmentKeyRing` generic; delegate signing/verification to `Signer` interface
- Keep core protocol logic unchanged: state number (`StateNum`), HTLC management (`UpdateLog`), revocation key exchange ([shachain] (../../../shachain/))

**9. Funding Manager Adaptation**

Modify [manager.go] (../../../funding/manager.go):

- `waitForFundingConfirmation` — In Setu mode, wait for DAG finalization (1 confirmation), greatly shortening timeout
- Funding transaction construction switches from `chanfunding.WalletAssembler` (UTXO selection) to new `chanfunding.SetuAssembler` (directly create Channel Object + lock balance)
- `ShortChannelID` generation logic: Bitcoin waits for confirmation in a block then encodes position; Setu uses ObjectID after object creation finalization

**10. Contract Court Adaptation**

Modify all resolvers in [contractcourt] (../../../contractcourt/):

- `commitSweepResolver` — Setu: call `claim_local_balance` entry on Channel Object
- `htlcTimeoutResolver` — Setu: call `timeout_claim` entry on HTLC (wait for VLC logical time expiry)
- `htlcSuccessResolver` — Setu: call `preimage_claim` entry on HTLC
- `breachArbitrator` — Setu: call `penalize` entry on Channel Object (submit revocation key + proof of old state)
- `anchorResolver` — Setu: **not needed** (DAG doesn't require fee bump mechanisms)
- Modify [channel_arbitrator.go] (../../../contractcourt/channel_arbitrator.go) to detect object state changes instead of UTXO spends

**11. Sweep Module Simplification**

Add Setu mode in [sweep] (../../../sweep/):

- Remove Bitcoin-specific transaction construction (`wire.NewMsgTx`), weight estimation, RBF/CPFP logic
- "Sweeping" on Setu simplifies to: call `withdraw` function on Channel Object to transfer balance back to personal account
- `FeeRate` changes from `SatPerKWeight` to `chaintype.FeeRate` (Setu: gas price)
- Batch aggregation optimizations are less useful on Setu (cost per call is lower than Bitcoin TX)

**12. Graph and Discovery Adaptation**

- Modify [builder.go] (../../../graph/builder.go) — Channel liveness check: Bitcoin checks UTXO set; Setu queries whether Channel Object still exists in state tree (SMT query)
- Modify [gossiper.go] (../../../discovery/gossiper.go) — Channel verification: Bitcoin verifies on-chain 2-of-2 multisig script; Setu verifies Channel Object existence + both keys match + SMT Merkle proof
- Add Setu verification logic in `chanvalidate/`

**13. Fee Rate System Adaptation**

- Add `SetuEstimator` implementing `Estimator` interface in [chainfee] (../../../chainfee/)
- Bitcoin: `EstimateFeePerKW(numBlocks)` → Setu: `EstimateGasPrice(priority)`
- Modify [rates.go] (../../../chainfee/rates.go) — Add `GasPrice` type and conversion methods
- Remove dust limit checks in Setu mode (account model has no dust concept)

**14. RPC and Invoice Adaptation**

- Modify `GetInfo` in [rpcserver.go] (../../../rpcserver.go) — Return `"bitcoin"` or `"setu"` based on `ChainType`
- Wallet RPCs (`SendCoins`, `NewAddress`, `ListUnspent`) need to dispatch based on chain type
- Modify [zpay32] (../../../zpay32/) — Add Setu HRP (e.g., `lnst` mainnet, `lnsts` testnet)
- Keep amount unit as minimum integer in proto definitions, interpreted by client

**15. Configuration and Startup**

- Modify [config.go] (../../../config.go) — Add `Setu *lncfg.Chain`, `SetuMode *lncfg.SetuNode`
- Add lncfg/setu.go — Setu node configuration (RPC address, SDK path, etc.)
- Modify [config_builder.go] (../../../config_builder.go) — Add Setu branch in `BuildChainControl`
- Modify [server.go] (../../../server.go) — Initialize corresponding subsystems based on chain type

---

## 3. Complete List of Required Setu Capabilities

### P0 — Core Capabilities (Lightning Network cannot run without these)

| #   | Capability                         | Detailed Requirements                                                                                   | Corresponding LND Module                               |
| --- | ---------------------------------- | -------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| 1   | **Hardcoded Channel Logic**        | RuntimeExecutor must add execution functions for: ChannelOpen/Close/ForceClose, HTLCClaim/Timeout, Penalize, etc. | `input/setu_channel.go` + Rust side `executor.rs`      |
| 2   | **Shared Object**                  | Channel Object must be operable by both parties; state updates require both signatures                    | `lnwallet/setuwallet/`                                 |
| 3   | **Hashlock**                       | `execute_htlc_claim()` must have built-in SHA256 preimage verification logic                              | HTLC contract                                          |
| 4   | **VLC Logical Time Query**         | Execution functions can read current VLC tick for timelock comparison                                    | CSV/CLTV equivalent                                    |
| 5   | **Object Version/Sequence Number** | Channel Object must have monotonically increasing `state_num` to prevent replay of old states            | Commitment transaction sequence number                 |
| 6   | **Event Subscription API**         | Subscribe to state change events (creation, update, destruction) by ObjectID; epoch advancement events   | `chainntnfs/setunotify/`                               |
| 7   | **Finality Notification**          | Callback notification of finalization status after transaction submission                                 | Confirmation flow in [manager.go] (../../../funding/manager.go) |
| 8   | **Multi-signature Verification**   | Channel execution functions must have built-in 2-of-2 signature verification (secp256k1 ECDSA or Ed25519) | Funding output 2-of-2                                   |
| 9   | **Object Query API**               | Query full object state (balances, keys, HTLC list, etc.) by ObjectID                                    | Equivalent of `BlockChainIO`                            |
| 10  | **Atomic State Updates**           | Contract execution state changes must either all take effect or all roll back                             | Channel state consistency                               |
| 11  | **Key Management SDK**             | Go SDK supports secp256k1 and Ed25519 key pair generation, HD derivation, signing, verification          | [keychain] (../../../keychain/)                        |
| 12  | **Transaction Construction & Broadcast SDK** | Go SDK supports building Channel Events, signing, submitting to Setu network                        | `lnwallet/setuwallet/`                                 |

### P1 — Important Capabilities (Affect Security and Extensibility)

| #   | Capability                        | Detailed Requirements                                                             | Corresponding LND Module                          |
| --- | --------------------------------- | -------------------------------------------------------------------------------- | ------------------------------------------------- |
| 13  | **Merkle Proof (SMT Proof)**      | Provide Binary+Sparse Merkle Tree proof of object existence/non-existence        | [discovery] (../../../discovery/) channel verification |
| 14  | **Historical State Query**        | Query historical state of Channel Object by epoch (for dispute arbitration)      | [contractcourt] (../../../contractcourt/)         |
| 15  | **Gas Estimation API**            | Estimate gas consumption of Channel Event execution                              | `chainfee/`                                       |
| 16  | **Batch Operations**              | Atomically operate on multiple objects in a single transaction (batch HTLC settlement) | [sweep] (../../../sweep/) batch processing        |
| 17  | **Object Destruction Notification**| Generate subscribable event when Channel Object is destroyed (channel closed)    | Channel liveness check in [builder.go] (../../../graph/builder.go) |
| 18  | **Node Discovery/P2P**            | P2P connection information for Setu network nodes (for LN gossip bootstrapping)  | [chainreg] (../../../chainreg/) DNS seeds         |

### P2 — Optimization Capabilities (Enhance Performance and User Experience)

| #   | Capability                | Detailed Requirements                                                             |
| --- | ------------------------- | -------------------------------------------------------------------------------- |
| 19  | **Light Client Mode**     | Setu light client similar to Neutrino (only verify Merkle proofs, not full state) |
| 20  | **Watchtower Support**    | Third parties can monitor Channel Object state and automatically submit penalty transactions on breach |
| 21  | **Atomic Cross-Chain Operations** | Support atomic swaps / cross-chain HTLC between Bitcoin↔Setu (if dual-chain interoperability is needed) |

---

## 4. Verification

- **Unit Tests**: Each new Setu implementation (`setunotify/`, `setuwallet/`, `setu_channel.go`) independently tested, mocking the Setu SDK
- **Integration Tests**: Modify [itest] (../../../itest/) framework, add Setu devnet backend, cover core scenarios:
  - Open channel → send payment → multi-hop forward → cooperative close
  - Unilateral close → HTLC timeout/success resolution
  - Breach detection → penalty transaction
  - Dual-chain mode: Bitcoin and Setu channels coexist
- **Commands**: `make itest backend=setu` or `go test -tags setu [lnd](http://_vscodecontentref_/118)`
- **Manual Checks**: `lncli --chain=setu getinfo`, `lncli --chain=setu openchannel`

## 5. Decision Records

- **Adaptation Strategy**: Use **Adapter Pattern** rather than adding a new `chaintype/` abstraction layer. Do not change existing interface signatures; Setu adapters reuse Bitcoin types for semantic mapping at the implementation boundaries (`OutPoint.Hash` ← ObjectID, `Amount` ← Setu minimum unit, `MsgTx` ← Setu Event serialized bytes), zero intrusion into existing Bitcoin code paths
- **Cryptography**: Dual support for secp256k1 + Ed25519 (same as Sui); keychain must be extended for dual-path derivation
- **Dual-Chain Support**: Keep Bitcoin; dispatch via `ChainControl` + `--chain=setu` to support Setu simultaneously
- **Contract Language**: Setu currently lacks a general VM; use hardcoded EventType + RuntimeExecutor extension to implement Lightning Channel logic (ChannelOpen/Close/ForceClose/HTLCClaim/Timeout/Penalize); can migrate to Move VM in the future
- **Channel ID**: On Setu, use 32-byte ObjectID directly to identify channels; in routing protocol messages, carry full 32-byte via TLV extension