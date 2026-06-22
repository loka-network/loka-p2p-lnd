# LND ↔ EVM Lightning Integration — Security Audit

> Scope:
> - Full audit of the Solidity contract `evm-contracts/channel-manager/src/ChannelManager.sol` (and its interface `IChannelManager.sol`).
> - Key Go-side interaction paths: `input/evm_channel.go`, `input/evm_merkle.go`, `input/evm_signer.go`, `lnwallet/evm_commitment.go`, the `evmChainActive` interception points in `lnwallet/channel.go`, `contractcourt/evm_close.go` + `evm_settler.go`, `chainntnfs/evmnotify/`, and the decimals-scaling boundary in `lnwallet/evmwallet/amounts.go`.
> - Audit date: 2026-06-22.
> - Reviewer: automated code review.
>
> This document does not re-audit the upstream LND modules that are already battle-tested, nor the Bitcoin/Sui code paths. It focuses exclusively on the risk surface introduced by the EVM (`ChannelManager`) settlement adaptation.
>
> **Relationship to the Sui audit.** The EVM `ChannelManager` was written *after* `1-refactor-docs/sui/security-audit.md` and deliberately bakes in that audit's hard-won invariants from day one — the three Sui Critical findings (C-1 broadcaster-bound CSV, C-2 timeout-refunds-offerer, C-3 claim-credits-receiver-only) and H-1 (penalize-pays-victim) do **not** recur as open bugs here. The contract source even cites them by name (e.g. `ChannelManager.sol:339-341` references "the Sui audit's C-3 invariant"). This audit therefore opens **no new Critical findings**; the material risk surface is concentrated in **breach-remedy availability**, **fund-lock liveness**, and the **single-RPC trust model** that the L2 setting introduces.

---

## Summary

| Severity | Count | Notes |
| -------- | ----- | ----- |
| Critical | 0 | The Sui-audit criticals were pre-empted by design; see "Pre-empted Criticals" below |
| High     | 0 | 2 were found and **both remediated 2026-06-22** — watchtower-delegable breach remedy (H-1) + emergency escape hatch for unresolvable HTLCs (H-2) |
| Medium   | 5 | Trust-surface, replay, consent, and reconciliation concerns |
| Low / Info | 4 | Token assumptions and code hygiene |

Both **High** findings are design-level (not introduced bugs) and are exploitable only under specific conditions documented per finding. **Both have since been fixed** in `ChannelManager.sol` and validated by new Foundry tests (`test_Penalize_WatchtowerCanSubmitForVictim`, `test_DistributeFunds_EmergencyResolvesStuckHtlc`) — the suite is now 24 tests, all passing. Neither enables outright theft from an honest, online participant: the EVM signature, nonce, and Merkle-inclusion checks are byte-for-byte equivalent to the off-chain artifacts (validated by golden vectors on both the Go and Solidity sides — see "Serialization Equivalence"). The risks are about **availability of the breach remedy when a participant is offline** and **liveness of fund recovery when a co-signed state is malformed**.

---

## Pre-empted Criticals (carried over from the Sui audit, verified fixed-by-design)

| Sui finding | EVM status | Evidence |
| ----------- | ---------- | -------- |
| C-1 broadcaster-bound CSV | Pre-empted | `Channel.broadcaster` recorded in `forceClose` (`ChannelManager.sol:247`); `challengeExpiry` binds to the broadcaster; the non-broadcaster never waits |
| C-2 `htlc_timeout` refunds the offerer | Pre-empted | `timeoutHtlc` credits the **non-recipient** party (`ChannelManager.sol:372-378`) |
| C-3 `htlc_claim` credits receiver only (no double-charge) | Pre-empted | `claimHtlc` credits the recipient and never re-debits the sender (`ChannelManager.sol:339-348`) |
| H-1 `penalize` pays the victim, not an arbitrary caller | Partially — see **H-1 (EVM)** | `penalize` pays `msg.sender` but restricts it to the non-broadcaster participant; this *blocks third-party theft* but also *blocks watchtower delegation* |
| H-2 caller-identity on close paths | Pre-empted | `forceClose` asserts `NotAParticipant` (`:225`); `penalize` asserts participant && `!= broadcaster` (`:297-300`) |
| H-4 balance-sum bound | Pre-empted | `closeChannel` enforces strict `finalA + finalB == totalDeposited` (`:179`); `forceClose` enforces `balanceA + balanceB <= totalDeposited` (`:230`) — strict `==` is correct on EVM because gas is paid out-of-band in the native coin, so no fee is deducted from the channel balance (contrast the Sui `<=` relaxation) |

---

## High Findings

### H-1 (EVM). `penalize` cannot be delegated to a watchtower — breach remedy requires the victim to be online — **FIXED (shipped 2026-06-22)**

- Location: `ChannelManager.sol:265-312`; Go side `contractcourt/evm_close.go` (`dispatchEvmBreach`), `lnwallet/evm_commitment.go` (`EvmPenalizeTx`).
- Behaviour: `penalize` pays the reward to `msg.sender` and requires `msg.sender` to be a participant **and** not the broadcaster:
  ```solidity
  if (msg.sender != ch.participantA && msg.sender != ch.participantB) revert NotAParticipant();
  if (msg.sender == ch.broadcaster) revert NotAParticipant();
  address victim = msg.sender;
  ...
  token.safeTransfer(victim, reward);
  ```
  A watchtower is, by definition, **not** a channel participant, so any penalize it submits reverts with `NotAParticipant`. The Go side confirms there is **no watchtower integration for the EVM breach path** (`watchtower/` has zero EVM references); `dispatchEvmBreach` only fires when *this* node observes the revoked-nonce close on-chain while online and holding `LocalCommitment` (which carries the counterparty's signature on the higher-nonce state).
- Risk: Lightning's security model assumes a victim may be offline during the challenge window and relies on a watchtower to submit the justice transaction on their behalf. Under the current design:
  1. If the cheated node is offline when the counterparty broadcasts a revoked state, **no one can penalize** — the challenge window elapses and the cheater finalises the obsolete (favourable) state via `distributeFunds`.
  2. This is a strict regression from the Sui contract, whose `penalize` derives the victim from the recorded `broadcaster` field and pays that address **regardless of caller**, so a watchtower (or any altruistic relayer) can submit the proof and the victim still receives the funds (the Sui itest exercises exactly this watchtower scenario).
- Recommendation (must-fix before mainnet if watchtowers are in the threat model): mirror the Sui H-1 fix. Derive `victim` from the `broadcaster` field (`victim = broadcaster == participantA ? participantB : participantA`), pay `victim` rather than `msg.sender`, and drop the caller restriction so any party — including a watchtower holding the higher-nonce co-signed `StateUpdate` — can submit `penalize`. A leaked higher-nonce state then at worst lets the victim recover their own funds; it never enables theft.
- **Fix (shipped, `ChannelManager.sol:265-312`):** `penalize` now derives `victim = ch.broadcaster == ch.participantA ? ch.participantB : ch.participantA`, pays that fixed address regardless of `msg.sender`, and no longer restricts the caller. The proof requirement is unchanged (`correctNonce > ch.nonce` and `correctSig` must recover to `ch.broadcaster`). `test_Penalize_WatchtowerCanSubmitForVictim` proves a non-participant relayer can submit the proof and the offline victim still receives the full deposit. The node's own auto-penalize path (`contractcourt/evm_close.go` `dispatchEvmBreach`) is unaffected — it sends as the victim participant, which still works. **Remaining follow-up (not blocking):** a watchtower *client/server* hand-off format for the EVM `StateUpdate` + counterparty signature is not yet implemented on the Go side; the contract now *permits* delegation, but wiring an actual tower is a separate feature.

### H-2 (EVM). An unresolvable HTLC permanently locks the entire channel — no escape hatch — **FIXED (shipped 2026-06-22)**

- Location: `ChannelManager.sol:389-412` (`distributeFunds`), `319-382` (`claimHtlc`/`timeoutHtlc`).
- Behaviour: `distributeFunds` refuses to settle until **every** committed HTLC is resolved:
  ```solidity
  if (ch.htlcPool != 0) revert HtlcsUnresolved();
  ```
  `htlcPool` is decremented only by `claimHtlc` / `timeoutHtlc`, both of which `revert RecipientNotParticipant` if the HTLC's `recipient` is neither participant, and require either the SHA-256 preimage (claim) or `block.timestamp >= htlc.timelock` (timeout). There is **no admin function, no timeout-based forced distribution, and no per-HTLC default-resolution path.** The contract never checks on-chain that the sum of the committed HTLC amounts equals `htlcPool` — that invariant is trusted entirely to the off-chain `htlcsHash` construction.
- Risk: if a `forceClose` commits an `htlcsHash` whose leaf set (a) contains an HTLC with a non-participant `recipient`, or (b) has amounts that do not sum to `totalDeposited - balanceA - balanceB`, then `htlcPool` can never reach zero, `distributeFunds` reverts forever, and **the entire channel escrow is locked permanently with no recovery path.** Because `forceClose` requires the *counterparty's* signature on the state (including `htlcsHash`), this is not remotely exploitable against an honest client — it requires an honest party to have co-signed a malformed state, i.e. it is a client-correctness footgun rather than a direct attack. The contract test `test_DistributeFunds_RevertsWithUnresolvedHtlc` confirms the brick condition is reachable.
- Recommendation: add a final escape hatch — e.g. after `challengeExpiry + gracePeriod`, allow `distributeFunds` to sweep any residual `htlcPool` to a deterministic default. Independently, document the off-chain invariant `sum(htlc.amount) == totalDeposited - balanceA - balanceB` as a checked assertion in the Go `htlcsHash` builder so a buggy client fails closed before signing.
- **Fix (shipped, `ChannelManager.sol:389-432`):** `distributeFunds` gains an escape hatch. The normal path is unchanged (reverts `HtlcsUnresolved` while `htlcPool != 0`), but once `block.timestamp >= challengeExpiry + EMERGENCY_RESOLUTION_DELAY` it finalizes anyway, splitting the unattributable residual evenly between the two participants. `EMERGENCY_RESOLUTION_DELAY = 30 days` is fixed far longer than any realistic HTLC timelock (BOLT `max_cltv_expiry` ~2 weeks), so a *legitimate* HTLC is always resolved correctly (claim/timeout pays its rightful owner 100%) long before the backstop can fire — and because the even split is always weakly worse for an HTLC's rightful owner than resolving it, the hatch adds no griefing incentive. The invariant `payoutA + payoutB + htlcPool == totalDeposited` guarantees the full deposit is paid out with nothing stranded. `test_DistributeFunds_EmergencyResolvesStuckHtlc` proves a channel with a non-participant-recipient HTLC (otherwise permanently locked) finalizes after the grace and splits the residual 50/50. The off-chain sum-invariant assertion (M-4) remains recommended as defence-in-depth.

---

## Medium Findings

### M-1. Single-RPC trust surface (deferred to post-mainnet hardening)

- Location: `chainntnfs/evmnotify/` (single `EvmClient` injected at construction; `ethclient.Dial(rpcURL)`).
- Observation: the node derives all chain state — event logs (`FilterLogs` by `channelId` topic), confirmation depth, and the native-gas balance — from a **single configured RPC endpoint**. There is no N-of-M quorum. A malicious or compromised RPC could suppress a `UnilateralCloseInitiated` event past the challenge window (defeating the breach remedy — compounding **H-1**), forge a `ChannelClosed`/`FundsDistributed` event, or rate-limit critical reads at an adversarial moment.
- Risk profile: against reputable hosted endpoints (Base/Optimism public RPC, Alchemy, etc.) TLS-interception risk is low, but on an L2 the sequencer/RPC is already a meaningful trust assumption, and missed close notifications during a challenge window are a direct breach-remedy failure mode.
- Recommendation (post-mainnet): N-of-M RPC quorum in `evmnotify` — treat a log/confirmation as real only when ≥ threshold independent endpoints agree on the same `(blockHash, txHash, logIndex)`. Config via `lncfg` (`Evm.PrimaryRPC`, `Evm.SecondaryRPCs`, `Evm.QuorumThreshold`). **Status:** acceptable for testnet / early mainnet behind one reputable endpoint plus a backup; re-prioritise once the node carries non-trivial capital.

### M-2. `CooperativeClose` commits no nonce — older co-signed splits can be replayed while `OPEN`

- Location: `ChannelManager.sol:43-45` (`COOP_CLOSE_TYPEHASH`), `170-208` (`closeChannel`).
- Observation: the cooperative-close digest binds only `(channelId, finalBalanceA, finalBalanceB)` — no `nonce` / `state_num`. `closeChannel` checks `status == OPEN` and the two signatures, but does not compare against `ch.nonce`. If the parties co-sign several distinct splits during an aborted close negotiation, a party holding an older, more-favourable-to-them co-signed pair can broadcast it first.
- Risk: this is the EVM analogue of the Sui H-3 finding, and it is structurally *more* exposed here because there is no nonce field to compare at all. Severity is bounded: it requires both signatures, balances must equal `totalDeposited` (a redistribution between the two parties only, never an over-draw), and a well-behaved client never co-signs conflicting final splits. But "the contract layer should not assume correct client behaviour."
- Recommendation: add a `nonce` to `CooperativeClose` and enforce `nonce >= ch.nonce`, **or** document and enforce on the Go side that a client co-signs exactly one final split per channel.

### M-3. `openChannel` pulls the counterparty's deposit with no per-channel consent

- Location: `ChannelManager.sol:117-163`.
- Observation: `participantA` (the caller) names `counterparty` and `remoteFundingAmount`, and the contract pulls the counterparty's deposit via `safeTransferFrom` against a standing allowance. The counterparty signs nothing at open. For single-funded channels (`remoteFundingAmount == 0`) this is harmless. For dual-funded channels, anyone the counterparty has approved the contract for can lock those approved tokens into a channel whose `channelId`, salt, and split the initiator chose unilaterally.
- Risk: not theft — the counterparty's funds are escrowed at their nominal value and recoverable via cooperative close (their own signature) or by waiting out a force-close — but it is a consent / griefing gap: an initiator can strand a counterparty's approved balance in a channel the counterparty never agreed to.
- Recommendation: for dual-funded opens, require a counterparty EIP-712 `OpenChannel` signature, or document single-funded-only operation with just-in-time (exact-amount) approvals on the Go side.

### M-4. HTLC-pool ↔ balance reconciliation is trusted entirely off-chain

- Location: `ChannelManager.sol:251` (`htlcPool = totalDeposited - balanceA - balanceB`), `349`/`379` (`htlcPool -= htlc.amount`).
- Observation: `htlcPool` is derived from the signed balances, but the contract never verifies that the committed HTLC leaves actually sum to it. A claim/timeout whose `htlc.amount` exceeds the running `htlcPool` underflows and reverts (Solidity 0.8 checked arithmetic), and a set that sums to less than `htlcPool` can never drive it to zero. Both outcomes feed directly into **H-2** (permanent lock).
- Recommendation: as in H-2, assert the sum invariant in the Go `htlcsHash` builder before signing; consider committing the HTLC count/total in the signed `StateUpdate` so the contract can sanity-check it.

### M-5. `evmnotify` lacks explicit reorg buffer and event dedup

- Location: `chainntnfs/evmnotify/evmnotify.go` (poll → `FilterLogs` over `[logFromBlock, tip]`; confirmation by receipt depth).
- Observation: confirmation uses receipt depth but applies no extra buffer for L2 reorg / sequencer-reorg risk, and each poll re-scans its window with no persisted idempotency key, so a reconnect can re-dispatch an event. LND's higher layers absorb most duplicates, but the EVM path has not been stress-tested under forced reconnects, and `evm_close.go` takes the first matching log without de-duplicating.
- Recommendation: persist `(blockHash, txHash, logIndex)` as the idempotency key in kvdb; add a small reorg-depth buffer (configurable) before treating a close as final.

---

## Low / Info

### L-1. Fee-on-transfer / rebasing tokens are silently unsupported
- `openChannel` records `totalDeposited = localFundingAmount + remoteFundingAmount` (nominal), but `safeTransferFrom` of a fee-on-transfer token escrows less, leaving the contract under-collateralised and the final `safeTransfer` able to over-draw against other channels' escrow. The single-asset-per-deployment model (`token` is `immutable`) makes this a deploy-time assumption. Recommendation: document "standard, non-fee, non-rebasing ERC-20 only" (USDC-class) and, if defence-in-depth is wanted, measure balance deltas around the transfers.

### L-2. `CLOSED` channel storage is never cleared; `(A,B,salt)` is single-use
- A terminal channel keeps `status == CLOSED`, so reopening the same `(participantA, participantB, salt)` reverts `ChannelAlreadyExists`. This is correct for `htlcResolved` collision-safety but means the Go side must rotate `salt` per channel and pays for permanent storage growth. Info — confirm the adapter's salt derivation never repeats.

### L-3. `claimHtlc` has no challenge-window upper bound (by design — documented for clarity)
- Unlike `penalize`, `claimHtlc` is gated only on `status == CLOSING_UNILATERAL`, not `block.timestamp < challengeExpiry`. This is intentional and necessary: HTLCs whose timelock falls after the challenge window must still be resolvable before `distributeFunds`. No change needed; noted so reviewers don't mistake it for a missing check.

### L-4. Synthetic-spend payload parsing trusts event-data length
- `evmnotify` tunnels EVM events through LND's Bitcoin `SpendDetail` plumbing as synthetic `wire.MsgTx` payloads; `evm_close.go` length-checks (`len(payload) < 28`) before decoding broadcaster/nonce/challengeExpiry. The checks are present but tightly coupled to the contract's event ABI layout — add a regression test that fails if the `UnilateralCloseInitiated` field offsets change.

---

## Contract ↔ Go Serialization Equivalence

The Go signer and the Solidity verifier are locked to each other by **golden test vectors on both sides** — a stronger guarantee than the Sui integration's by-inspection equivalence.

| Artifact | Solidity | Go | Cross-check |
| -------- | -------- | -- | ----------- |
| EIP-712 domain | `EIP712("LokaChannelManager","1")` + chainId + verifyingContract | `input/evm_channel.go` `EvmDomain.Separator()` — same name/version, `encodeUint256(chainId)`, `encodeAddress(verifyingContract)` | ✓ golden digest |
| `StateUpdate` digest | `_stateUpdateDigest` over `(channelId,nonce,balanceA,balanceB,htlcsHash)` | `EvmStateUpdate.Digest(domain)` | ✓ `evm_signer_test.go` golden `261e1413…` |
| `CooperativeClose` digest | `COOP_CLOSE_TYPEHASH` over `(channelId,finalBalanceA,finalBalanceB)` | `EvmCooperativeClose.Digest` | ✓ golden `6d537a6d…` |
| HTLC Merkle leaf | `keccak256(abi.encode(htlc))` over `{index,amount,hashlock,timelock,recipient}` | `EvmHTLC.Leaf()` — same 5 fields as 32-byte ABI words, timelock widened to uint256 | ✓ golden leaf vectors |
| HTLC Merkle root/proof | OZ `MerkleProof.verify`, commutative pair hashing | `input/evm_merkle.go` — leaves sorted by `index`, `commutativeHash` (lexicographic `keccak`), odd-node promotion | ✓ golden `13b0b913…` + `HtlcMerkleVectors.t.sol` |
| Signature recovery | OZ `ECDSA.recover` (rejects high-s, `v ∈ {27,28}`) | `RecoverEvmSigV` tries `v ∈ {27,28}`, returns the one recovering the expected address | ✓ `SigRecoveryVectors.t.sol` |

Hashlock is **SHA-256** (`sha256(abi.encodePacked(preimage))`, the BOLT payment hash), **not** keccak256 — verified identical on both sides (`ChannelManager.sol:332`, `IChannelManager.sol:34`). Signing uses the **per-channel funding multisig key**, not the node identity key (`evm_commitment.go` `signEvmCommitment`), and the signer logs no sensitive material (contrast the Sui L-3 `fmt.Printf` finding — not present here).

---

## Remediation Priority

| # | Priority | Action |
| - | -------- | ------ |
| 1 | Must-fix before mainnet (if watchtowers in scope) | **H-1** — pay the broadcaster-derived victim regardless of caller — **shipped**; EVM watchtower client/server hand-off remains a follow-up feature |
| 2 | Must-fix before mainnet | **H-2** — emergency escape hatch for unresolvable HTLCs — **shipped**; off-chain sum-invariant assertion (M-4) still recommended |
| 3 | Before mainnet | **M-2** (coop-close nonce), **M-4** (pool reconciliation) |
| 4 | Post-release hardening | **M-1** (RPC quorum), **M-5** (reorg/dedup), **M-3** (dual-fund consent) |
| 5 | Hygiene | **L-1**…**L-4** |

---

## Regression Test Status

On-chain behaviour is covered by **24 Foundry tests** (`evm-contracts/channel-manager/test/`): 20 lifecycle/assertion tests in `ChannelManager.t.sol` (including the two H-1/H-2 remediation tests) plus 4 cross-language vector tests (`Eip712Vectors`, `HtlcMerkleVectors`, `SigRecoveryVectors`, `StateUpdateHtlcsVectors`) that pin the Solidity side to the same golden vectors the Go side asserts. End-to-end coverage comes from `scripts/itest_evm.sh` — **19/19 checks on Anvil** and **15/15 on Base Sepolia** (the public-testnet run skips the Anvil-only `anvil_mine` timeout step).

| # | Invariant | Where tested | Status |
| - | --------- | ------------ | ------ |
| 1 | Broadcaster-bound challenge window (Sui C-1) | `test_ForceClose_OpensChallengeWindow` + itest force-close | ✅ |
| 2 | `timeoutHtlc` refunds the offerer (Sui C-2) | `test_TimeoutHtlc_RefundsOffererAfterTimelock` + itest step 7 (Anvil) | ✅ |
| 3 | `claimHtlc` credits receiver only (Sui C-3) | `test_ClaimHtlc_CreditsReceiverWithPreimage` + itest step 6 | ✅ |
| 4 | Third-party close rejected (Sui H-2) | `test_ForceClose_RevertsWhenNotCounterpartySig`, `test_OpenChannel_RevertsOnSelfCounterparty` | ✅ |
| 5 | `penalize` sweeps to the victim (Sui H-1) | `test_Penalize_SweepsToVictim`, `_RevertsOnStaleNonce`, `_RevertsAfterWindow` | ✅ |
| 5b | **H-1 fix:** watchtower/relayer can penalize for an offline victim | `test_Penalize_WatchtowerCanSubmitForVictim` | ✅ |
| 6 | Balance conservation on close | `test_CooperativeClose_RevertsOnNonConservingBalances` | ✅ |
| 7 | `distributeFunds` blocks before window / with unresolved HTLC | `test_DistributeFunds_RevertsBeforeWindowCloses`, `_RevertsWithUnresolvedHtlc` | ✅ |
| 7b | **H-2 fix:** emergency hatch finalizes a channel with an unresolvable HTLC | `test_DistributeFunds_EmergencyResolvesStuckHtlc` | ✅ |
| 8 | EIP-712 / Merkle / sig-recovery cross-language equivalence | 4 vector tests + Go `evm_signer_test.go` golden vectors | ✅ |

## References

- Contract source: `evm-contracts/channel-manager/src/ChannelManager.sol`, `IChannelManager.sol`
- Go serializer / signer: `input/evm_channel.go`, `input/evm_merkle.go`, `input/evm_signer.go`, `lnwallet/evm_commitment.go`
- Settlement driver: `contractcourt/evm_close.go`, `contractcourt/evm_settler.go`
- Chain notifier: `chainntnfs/evmnotify/`
- Refactor docs: `1-refactor-docs/evm/lnd-evm-refactor-plan.md`, `1-refactor-docs/evm/evm-ln-interaction-spec.md`
- Companion audit: `1-refactor-docs/sui/security-audit.md`
- BOLT-03 Commitment Transactions (Lightning specification baseline)
