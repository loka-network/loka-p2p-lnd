# EVM and LND Interaction Contract Interface and Data Structure Specification

> Objective: In conjunction with the `ChannelManager.sol` Solidity implementation, define the on-chain data structures, the **off-chain EIP-712 commitment schema**, the event/entry-function interface, and the synchronization flow that the LND EVM adapter (`evmwallet`, `evmnotify`, `evm_signer`) must conform to.
>
> This is the EVM counterpart of [`../sui/sui-ln-interaction-spec.md`](../sui/sui-ln-interaction-spec.md). Where Sui uses a shared `Channel` object and Move entry functions, EVM uses a single escrow contract with a `mapping(bytes32 => Channel)` and Solidity external functions. The off-chain protocol semantics (`StateNum`, HTLC add/settle/fail, revocation window) are identical and live unchanged in `lnwallet/channel.go`; only the *signed artifact* and the *settlement calls* differ.

---

## 1. Solidity Contract Data Structures (`ChannelManager`)

Channel state lives in contract storage, keyed by the 32-byte `channelId`.

```solidity
// On-chain channel record (storage)
struct Channel {
    address participantA;
    address participantB;
    uint256 totalDeposited;   // escrowed token base-units (sum of both deposits)
    uint8   status;           // 0 OPEN, 1 CLOSING_COOP, 2 CLOSING_UNILATERAL, 3 CLOSED
    uint256 nonce;            // highest settled StateUpdate.nonce (== LND StateNum)
    uint256 challengeExpiry;  // block.timestamp deadline; 0 unless CLOSING_UNILATERAL
    address broadcaster;      // who initiated the unilateral close (CSV is bound to THIS party)
    bytes32 htlcsHash;        // Merkle root of the active HTLC set committed at force-close
}

// An HTLC as presented to claimHtlc / timeoutHtlc, proven against htlcsHash
struct HTLC {
    uint256 index;            // == LND UpdateLog index, sort key for the Merkle tree
    uint256 amount;           // token base-units
    bytes32 hashlock;         // sha256(preimage) — note SHA-256, matching BOLT, not keccak
    uint32  timelock;         // absolute block.timestamp deadline (replaces CLTV)
    address recipient;        // party credited on successful claim
}
```

`channelId = keccak256(abi.encodePacked(participantA, participantB, salt))`. The `salt` lets the same pair open multiple channels; it is chosen by the initiator and mirrors Bitcoin's funding-outpoint uniqueness.

> **Note — `broadcaster`-bound CSV.** The `broadcaster` field exists for the same reason the Sui audit added it (see [`security-audit.md`](security-audit.md) C-1, mirroring [`../sui/security-audit.md`](../sui/security-audit.md) C-1): the challenge delay must apply to *whoever force-closed*, never to a hard-coded party, or a cheater can sweep a revoked state immediately.

### 1.1 Events

The contract notifies the LND `evmnotify` adapter exclusively through logs. Every event is `indexed` on `channelId` so the adapter can filter by channel with a single topic.

```solidity
event ChannelOpened(bytes32 indexed channelId, address participantA, address participantB, uint256 balanceA, uint256 balanceB);
event ChannelClosed(bytes32 indexed channelId, uint256 finalBalanceA, uint256 finalBalanceB);
event UnilateralCloseInitiated(bytes32 indexed channelId, address broadcaster, uint256 nonce, uint256 balanceA, uint256 balanceB, uint256 challengeExpiry);
event ChannelPunished(bytes32 indexed channelId, address victim, uint256 rewardAmount);
event HTLCClaimed(bytes32 indexed channelId, uint256 indexed htlcIndex, bytes32 preimage);
event HTLCTimeout(bytes32 indexed channelId, uint256 indexed htlcIndex);
```

Unlike the Sui `ChannelSpendEvent` (which the audit flagged in M-2 for carrying too few fields), these events emit balances and the resolved `broadcaster`/`victim` up front, so `evmnotify` never needs a follow-up `eth_call` to reconstruct who-owes-what when dispatching to a resolver.

---

## 2. Off-Chain Commitment: EIP-712 Typed-Data Schema

This is the artifact each peer signs every time the channel state advances — the EVM analogue of a BOLT-03 commitment transaction. It is exchanged inside the unchanged `commitment_signed` / `revoke_and_ack` flow and is what `input.Signer.SignOutputRaw` produces when `evmChainActive` is set (refactor-plan §2.4–2.5).

### 2.1 Domain Separator

```
EIP712Domain {
    string  name;              // "LokaChannelManager"
    string  version;           // "1"
    uint256 chainId;           // the sub-network EVM chainId (8453 for Base, …)
    address verifyingContract; // the deployed ChannelManager address
}
```

> **Replay safety (security-critical).** `chainId` **and** `verifyingContract` are part of the domain on purpose. CREATE2 deploys `ChannelManager` at the *same address* on every chain (refactor-plan §4), and `channelId` is derived only from participants + salt — so without the domain binding, a `StateUpdate` signed for Base-USDC would verify byte-for-byte on Arbitrum-USDC. Binding the domain to `chainId` makes each signature valid on exactly one sub-network. See [`security-audit.md`](security-audit.md) C-2.

### 2.2 StateUpdate Type

```
StateUpdate {
    bytes32 channelId;
    uint256 nonce;       // == LND StateNum (monotonic)
    uint256 balanceA;    // token base-units, net of outstanding HTLCs (BOLT model)
    uint256 balanceB;
    bytes32 htlcsHash;   // Merkle root over the active HTLC set, see §2.3
}
```

The signed digest follows the EIP-712 standard:

```
digest = keccak256(0x19 ‖ 0x01 ‖ domainSeparator ‖ hashStruct(StateUpdate))
```

Each peer signs `digest` with its secp256k1 funding key, producing a 65-byte `(r, s, v)` signature. The contract recovers the signer with `ECDSA.recover(digest, sig)` and checks it equals `participantA` / `participantB`. Because `balanceA`/`balanceB` are already net of outstanding HTLCs (matching Lightning's commitment model), `claimHtlc` must **credit the receiver only and never re-debit the sender** — the same invariant whose violation was the Sui audit's C-3.

### 2.3 `htlcsHash` Derivation

`htlcsHash` is a Merkle root, not a flat hash, so an individual HTLC can be proven on-chain without submitting the whole set:

- Leaf: `keccak256(abi.encode(HTLC{index, amount, hashlock, timelock, recipient}))`.
- Leaves are ordered by `index` ascending (the LND `UpdateLog` index), making the tree deterministic and independent of map iteration order.
- Empty set ⇒ `htlcsHash = bytes32(0)`.

`claimHtlc` / `timeoutHtlc` submit the `HTLC` plus a Merkle proof; the contract verifies inclusion against the `htlcsHash` recorded at force-close. The Go side computes the identical root inside `lnwallet/channel.go` when assembling the commitment (refactor-plan §2.5).

### 2.4 Entry-Function Interface (settlement surface)

The full `IChannelManager` interface is specified in [`lnd-evm-refactor-plan.md`](lnd-evm-refactor-plan.md) §3. Summarized by the resolver that drives each call:

| Call                              | Signatures required        | Driven by (LND)                       |
| --------------------------------- | -------------------------- | ------------------------------------- |
| `openChannel(salt, cp, aA, aB)`   | none (funder broadcasts)   | `chanfunding.EvmAssembler`            |
| `closeChannel(id, bA, bB, sA, sB)`| both peers (EIP-712)       | cooperative-close path                |
| `forceClose(id, nonce, …, sig)`   | one peer (EIP-712)         | `commitSweepResolver`                 |
| `claimHtlc(id, idx, …, preimage)` | preimage + Merkle proof    | `htlcSuccessResolver`                 |
| `timeoutHtlc(id, idx, …)`         | Merkle proof, after `timelock` | `htlcTimeoutResolver`             |
| `penalize(id, …, correctSig)`     | a higher-nonce signed state| `BreachArbitrator`                    |

---

## 3. LND Adaptation Layer Semantic Mapping

| Contract action / event           | LND semantics      | `ChainNotifier` mapping     |
| --------------------------------- | ------------------ | --------------------------- |
| `ChannelOpened` (≥ numconfs)      | Funding confirmed  | `RegisterConfirmationsNtfn` |
| `ChannelClosed`                   | Cooperative close  | `RegisterSpendNtfn`         |
| `UnilateralCloseInitiated`        | Force close        | `RegisterSpendNtfn`         |
| `HTLCClaimed`                     | HTLC settled (preimage learned) | `RegisterSpendNtfn` |
| `HTLCTimeout`                     | HTLC timeout       | `RegisterSpendNtfn`         |
| `ChannelPunished`                 | Breach remedy done | `RegisterSpendNtfn`         |
| new block / finalized header      | Block epoch        | `RegisterBlockEpochNtfn`    |

`evmnotify` extracts the preimage directly from the `HTLCClaimed.preimage` field, which is exactly the value the upstream `htlcSuccessResolver` needs to settle the incoming HTLC — this is the on-chain leg of the cross-chain atomic-swap routing in the integration doc §6.2.

---

## 4. State Synchronization Flow

1. **Initiate** — the adapter (`evmwallet.SendOutputs`) ABI-encodes the target `ChannelManager` call, signs it EIP-155 via `evm_signer`, and submits it over JSON-RPC (`eth_sendRawTransaction`).
2. **Confirm & listen** — `evmnotify` subscribes via WebSocket (`eth_subscribe("logs", {address: contract, topics: [..., channelId]})`). On each matching log it parses `channelId` / `htlcIndex` and dispatches to the corresponding resolver. Confirmation depth is counted against `evm.numconfs` to absorb L2 sequencer reorgs (see [`security-audit.md`](security-audit.md) H-2).
3. **Extract** — once a close/claim is confirmed with no pending dispute, the contract has already transferred ERC20 funds to the participant addresses (push payment inside the settlement call); no separate withdrawal step is required, unlike Sui's `Channel`-object balance extraction.

---

## 5. Key Implementation Checks

- **Signature algorithm** — secp256k1 throughout. On-chain calls are EIP-155; off-chain commitments are EIP-712 typed data. The contract verifies the latter with OpenZeppelin `ECDSA.recover` and **must reject `v` malleability / the upper-half-`s` form** (use `ECDSA.tryRecover` and revert on `RecoverError`).
- **Time reference** — the challenge window and HTLC `timelock` use `block.timestamp` (seconds), not block height, because L2 block intervals vary. The Go side converts LND's block-based CLTV deltas to a timestamp deadline at commitment time.
- **Hashlock is SHA-256** — `hashlock = sha256(preimage)` to stay compatible with BOLT payment hashes across sub-networks; do **not** substitute keccak256, or cross-chain HTLCs against a Bitcoin/Sui leg break.
- **ERC20 safety** — use `SafeERC20` for `transferFrom`/`transfer`: USDT returns no boolean and a naive `require(token.transfer(...))` reverts against it. Assume non-rebasing, non-fee-on-transfer tokens (USDC/USDT qualify); document the assumption and reject others (see [`security-audit.md`](security-audit.md) H-1).
- **Reentrancy** — `closeChannel`/`claimHtlc`/`penalize` move funds; guard with checks-effects-interactions plus `nonReentrant`, since the token contract is external code.
- **Decimals alignment** — the `uint256` amounts on this boundary are raw token base-units; the Decimals Scaling Factor (integration doc §5) is applied *only* inside `evmwallet`, never in the contract, so the contract stays asset-decimal-agnostic.
