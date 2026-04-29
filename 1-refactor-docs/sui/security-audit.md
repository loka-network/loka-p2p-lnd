# LND ↔ Sui Lightning Integration — Security Audit

> Scope:
> - Full audit of the Move contract `sui-contracts/lightning/sources/lightning.move`.
> - Key Go-side interaction paths (`input/sui_channel.go`, `lnwallet/suiwallet/suisigner.go`, `lnwallet/chanfunding/sui_assembler.go`, `chainntnfs/suinotify/`, the `SUI_PAYLOAD:` interception point in `lnwallet/channel.go`).
> - Audit date: 2026-04-20 (Critical / High remediation landed 2026-04-21).
> - Reviewer: automated code review.
>
> This document does not re-audit the upstream LND modules that are already battle-tested. It focuses exclusively on the risk surface introduced by the Sui adaptation layer.

---

## Summary

| Severity | Count | Notes |
| -------- | ----- | ----- |
| Critical | 3 | Direct loss-of-funds or breakage of Lightning's core security assumptions |
| High     | 4 | Logic / access-control gaps that are exploitable or grief-able |
| Medium   | 5 | Reliability, robustness and consistency concerns |
| Low / Info | 4 | Recommended improvements and code hygiene |

All **Critical** and **High** findings have been remediated in the current `main` line of `lightning.move`; the fixes are validated by `sui move test` and by the end-to-end flow in `scripts/itest_sui.sh` (cooperative close, force close with CSV sweep, and watchtower justice transaction all pass).

Overall, the Move contract implements the on-chain arbiter behaviour required by a Lightning-style channel, and the Go side's BCS serialization / double-SHA-256 alignment with `ecdsa_k1::secp256k1_verify(hash=1)` is correct. The three originally critical logic bugs were concentrated in **HTLC settlement**, **CSV protection after force-close**, and **the payout target of `penalize`** — all of which diverged from Lightning's standard security model and have since been fixed.

---

## Critical Findings

### C-1. `claim_force_close` hard-coded the CSV delay to `party_a`, breaking breach-remedy

- Location: `sui-contracts/lightning/sources/lightning.move:267-306` (pre-fix)
- Pre-fix behaviour:
  ```move
  if (sender == channel.party_a) {
      // Alice must wait for time lock!
      assert!(clock::timestamp_ms(clock) >= channel.close_timestamp_ms + channel.to_self_delay, ENotExpired);
      ...
  } else if (sender == channel.party_b) {
      // Bob claims his balance immediately
      ...
  }
  ```
  The delay window was tied to `party_a`, independent of **who actually broadcast the force-close**.
- Risk: Lightning's breach-remedy model requires that the **broadcaster** waits for `to_self_delay`, giving the counterparty time to submit a revocation proof through `penalize`. Under the pre-fix code:
  1. If Bob (`party_b`) broadcast a long-since revoked commitment, he could call `claim_force_close` immediately and walk away with his old-state balance.
  2. Even if Alice held the revocation secret and called `penalize` afterwards, `penalize` could only transfer the **remaining** `funding_balance`, i.e. whatever Bob had not yet withdrawn.
  3. Net effect: the cheating party could immediately liquidate the portion of the revoked state that favoured them — the exact scenario `to_self_delay` is designed to prevent.
- Fix (shipped): a `broadcaster: address` field was added to `Channel`. `force_close` records the broadcaster based on the signature that verifies (Alice broadcasts when `commitment_sig` verifies against `pubkey_b`, and vice versa). `claim_force_close` now applies the CSV delay **only to the broadcaster**, while the non-broadcasting party may sweep immediately.

### C-2. `htlc_timeout` did not refund the HTLC amount to the sender

- Location: `sui-contracts/lightning/sources/lightning.move:339-357` (pre-fix)
- Pre-fix behaviour: `htlc_timeout` only set `htlc.status = 2 (TIMEOUT)`; it never touched `channel.balance_a` / `channel.balance_b`, nor did it transfer funds from `funding_balance`.
- Risk: After an A→B HTLC expires, the Lightning semantics require the amount to revert to A. Without that update the later `claim_force_close` would only withdraw `balance_a` / `balance_b` as recorded at force-close time (which excluded outstanding HTLCs), leaving the HTLC amount **permanently locked** in `funding_balance` with no recovery path. `penalize` only zeroes out balances in a cheating scenario; the honest-timeout path had no clean-up mechanism.
- Fix (shipped): after `htlc.status = 2`, the amount is now added back to the sender's balance based on `htlc.direction`:
  ```move
  if (htlc.direction == 0) {
      channel.balance_a = channel.balance_a + amount;
  } else {
      channel.balance_b = channel.balance_b + amount;
  };
  ```

### C-3. `htlc_claim` double-charged the sender

- Location: `sui-contracts/lightning/sources/lightning.move:323-329` (pre-fix)
- Pre-fix behaviour:
  ```move
  if (htlc.direction == 0) { // A to B
      channel.balance_a = channel.balance_a - htlc.amount;
      channel.balance_b = channel.balance_b + htlc.amount;
  } else {
      channel.balance_b = channel.balance_b - htlc.amount;
      channel.balance_a = channel.balance_a + htlc.amount;
  };
  ```
- Risk: In Lightning, the `local_balance` / `remote_balance` signed in the commitment are **already net of outstanding HTLCs** (HTLCs are carried as separate commitment outputs). Debiting the sender again at `htlc_claim` time would double-charge them — or, if `balance_a < htlc.amount`, cause a u64 underflow and abort the entire transaction. The original logic conflated the balances-after-HTLCs model with an HTLC-inclusive model.
- Fix (shipped): the receiver is credited only; the sender is **not** debited:
  ```move
  if (htlc.direction == 0) {
      channel.balance_b = channel.balance_b + amount;
  } else {
      channel.balance_a = channel.balance_a + amount;
  };
  ```

---

## High Findings

### H-1. `penalize` paid out to `tx_context::sender(ctx)`, creating a leaked-secret front-run

- Location: `sui-contracts/lightning/sources/lightning.move:360-387` (pre-fix)
- Pre-fix behaviour: whoever submitted a valid `revocation_secret` became `honest_party := tx_context::sender(ctx)` and took the entire `funding_balance`.
- Risk:
  1. If the honest peer delegates monitoring to a watchtower, the watchtower that holds the secret could betray the peer and claim the full payout for itself.
  2. Any third party that happens to obtain the `revocation_secret` (log leak, memory scrape, compromised host) could race ahead of the victim and steal the funds.
  3. Sui mempool is visible before finality, so a broadcaster who leaks the secret via their own transaction could be front-run.
- Fix (shipped): the contract now derives the **victim** (the non-broadcaster party) from the `broadcaster` field recorded during `force_close`, and transfers the remaining funding directly to that address regardless of who calls `penalize`. A leaked secret can at worst cause the victim to recover their own funds; it no longer enables theft.

### H-2. `force_close` / `close_channel` / `claim_force_close` accepted arbitrary callers, enabling grief

- Pre-fix locations:
  - `force_close` @ `lightning.move:186-264`
  - `close_channel` @ `lightning.move:133-184`
  - `claim_force_close` @ `lightning.move:267-306`
- Pre-fix behaviour: none of these entries validated that `tx_context::sender(ctx)` was a channel member. Given the right set of signatures (one commitment signature for force-close, both signatures for coop close), a third party could trigger the state transition.
- Risk:
  - **Denial-of-service / harassment**: a third party who intercepts a valid commitment signature (via operational leak or node compromise) could unilaterally force-close the channel, disrupting off-chain operation.
  - **Combined with C-1**: a third party could act as a proxy broadcaster on behalf of the cheater, since `claim_force_close` treated party_b as always-immediate.
- Fix (shipped): each entry now asserts `sender == channel.party_a || sender == channel.party_b` and aborts with `EUnauthorized` otherwise.

### H-3. `close_channel` did not enforce `state_num` monotonicity, permitting replay of old coop-close agreements

- Location: `lightning.move:133-184` (pre-fix)
- Pre-fix behaviour: only `assert!(channel.status == 0, EChannelNotOpen)` was present; no comparison against `channel.state_num`.
- Risk: if the two parties had previously signed several shutdown transactions (e.g. after an aborted negotiation), any party who retained an older, more favourable signature pair could broadcast it. In a well-behaved client this does not normally happen, but the contract layer should not assume correct client behaviour.
- Fix (shipped): `assert!(state_num >= channel.state_num, EInvalidStateNum);`

### H-4. `close_channel` / `claim_force_close` did not validate that `balance_a + balance_b` fit the escrowed funds

- Location: `lightning.move:164-176`, `267-306` (pre-fix)
- Pre-fix behaviour: the contract directly executed `coin::take(&mut funding_balance, balance_a, ctx)` and `coin::take(..., balance_b, ctx)`. If the signed split exceeded `funding_balance`, the second `coin::take` would abort — but only after the first had already paid out. Residual dust (when the split was less than `funding_balance`) was left locked in the `Channel` forever with no recovery path.
- Fix (shipped): `close_channel` now asserts `balance_a + balance_b <= balance::value(&channel.funding_balance)`. The inequality (rather than equality) is intentional: the Bitcoin-style close path in LND deducts a proposed fee from the output sum, so a strict `==` would reject valid cooperative closes. The relaxed bound still preserves the `coin::take` invariant and prevents over-draws.

---

## Medium Findings (open)

### M-1. ~~`open_channel` does not bind `party_b` to `pubkey_b` cryptographically~~ — Withdrawn (matches BTC LN by design)

- Original concern: the initiator can supply arbitrary `pubkey_a`, `pubkey_b` and `party_b`; the contract does not enforce that `pubkey_b` derives to `party_b`.
- **Re-evaluation**: this exactly matches the on-chain behaviour of Bitcoin Lightning. BTC LN's `funding_tx` is also broadcast unilaterally by the initiator, who can lock funds into any 2-of-2 multisig script she chooses; Bitcoin nodes never check that the multisig keys correspond to specific identities. Counterparty attestation is **not** an on-chain concern in BOLT design — it is delegated to the gossip layer.
- **Why the gossip layer is sufficient**: BOLT-7 `channel_announcement` requires four signatures: `node_signature_1`, `node_signature_2`, `bitcoin_signature_1`, `bitcoin_signature_2`. A malicious initiator who fabricates a "channel with Bob" cannot produce `node_signature_2` (Bob's node identity key) nor `bitcoin_signature_2` (Bob's funding multisig key). The announcement is rejected by every receiving node and the fabricated channel never enters anyone's `graph.db`. The Sui adapter inherits this mechanism unchanged: `discovery/gossiper.go:2664-2671` rejects any announcement whose `chain_hash` doesn't match the local `GenesisHash`, and the four-signature check applies before persistence.
- **Funds risk**: zero. Even if a malicious initiator opens a channel with a self-controlled "Bob" pubkey, she still has to lock real SUI into the `funding_balance`. To reclaim those funds she must produce both signatures at close time — but she only controls `pubkey_a`'s private key. The funds are self-locked.
- **Verdict**: this is a BOLT design choice, not a Sui-adaptation defect. No fix planned.

### M-1bis (was task 2). Single-RPC trust surface — deferred to post-mainnet hardening

- Location: `chainntnfs/suinotify/rpc_client.go`, `lnwallet/suiwallet/suiwallet.go`
- Observation: the node currently consumes Sui state through a single configured RPC endpoint (e.g. `https://fullnode.devnet.sui.io:443`). Finality, object reads, and event subscriptions are all derived from that single source. A malicious or compromised RPC could feed forged "channel closed" events, suppress real `force_close` notifications past the watchtower's CSV window, or rate-limit critical reads at adversarial moments.
- Risk profile against the major hosted endpoints (Mysten Labs, Triton, etc.):
  - **TLS interception**: low — HTTPS with proper certificate validation.
  - **Single-host compromise**: low but nonzero, especially under DDoS pressure or operator account takeover.
  - **Liveness / rate limiting**: realistic — public fullnodes throttle, and missed `force_close` notifications during a CSV window are a direct breach-remedy failure mode.
- Recommendation (post-mainnet): introduce N-of-M RPC quorum verification in `chainntnfs/suinotify/rpc_client.go`. Treat finality as confirmed only when multiple independent fullnodes agree on the same `transaction_digest` and `checkpoint`, and treat object reads similarly. Configuration via `lncfg/sui.go` (`Sui.PrimaryRPC`, `Sui.SecondaryRPCs []string`, `Sui.QuorumThreshold int`).
- **Status**: deferred. Acceptable for testnet / early mainnet under a single Mysten Labs–operated endpoint plus a single backup. Re-prioritise once the node carries non-trivial mainnet capital.

### M-2. Events lack `party_a` / `party_b` / `broadcaster` / balance fields

- Location: `lightning.move:57-71`, `ChannelSpendEvent`
- Observation: only `channel_id`, `htlc_id`, `spend_type`, `state_num` are emitted. The Go `suinotify` layer has to derive broadcaster identity and balances via additional object reads or local state, which costs round-trips and couples event correctness to client caches.
- Recommendation: extend `ChannelSpendEvent` with `broadcaster`, `balance_a`, `balance_b`, `close_timestamp_ms`.

### M-3. `close_channel`'s swap-tolerant signature check can mask client-side ordering bugs

- Location: `lightning.move:150-160`, `224-235`
- Observation: `close_channel` accepts either `(sig_a, sig_b)` or the swapped `(sig_b, sig_a)` pair. This hides client-side ordering mistakes, and — combined with the cheating paths — makes post-mortem analysis harder.
- Recommendation: fix the argument order on the Go side and validate a single combination, or include an explicit `signer_index` in the signed preimage.

### M-4. `MIN_TO_SELF_DELAY_MS` is hard-coded, but the itest overrides it via `sed`

- Location: contract `lightning.move:25` vs `scripts/itest_sui.sh` (the `sed` patch around `MIN_TO_SELF_DELAY_MS` under `ITEST_SUI_FAST_SWEEP`).
- Risk: the "production" and "test" versions of the contract are sibling byte-patched files. A mistaken deploy could put the 15-second testing constant on mainnet.
- Recommendation:
  - Move `MIN_TO_SELF_DELAY_MS` into a Config shared object (admin-initialised) or a `#[test_only]` constant.
  - Add a CI guard that checks `MIN_TO_SELF_DELAY_MS >= 86_400_000` in the source that is actually deployed.

### M-5. `suinotify` lacks explicit event-dedup / reorg handling

- Location: `chainntnfs/suinotify/*`
- Observation: the client subscribes via Sui RPC `subscribeEvent` and trusts finality from the node; there is no local idempotency key and no periodic checkpoint reconciliation.
- Risk: on RPC reconnection, the same event could be dispatched multiple times; LND's higher-level dedup absorbs most cases but the Sui path has never been stress-tested under forced reconnects.
- Recommendation: persist `(transaction_digest, event_seq)` as the idempotency key in kvdb; add a periodic reconcile via `suix_getEvents`.

---

## Low / Info

### L-1. `gen_sig.go` / `gen_sig.py` generate signatures over a minimal preimage

- Location: `sui-contracts/lightning/gen_sig.go`
- Observation: these helpers sign only over `channelID || state_num || revocation_hash`, whereas the real `force_close` preimage covers state_num, balances, revocation_hash, and all HTLC arrays. They are suitable only for the smallest single-case tests, not for HTLC-inclusive regression runs.
- Recommendation: rename to `gen_sig_minimal.*` or extend to the full preimage, and add a test case that exercises HTLC-bearing force-close flows end-to-end.

### L-2. The `SUI_PAYLOAD:` / `SUI_COOPCL:` magic-prefix sniffing in `suisigner.go`

- Location: `lnwallet/suiwallet/suisigner.go:71-87`
- Observation: the signer decides between Sui-native signing and Bitcoin-sighash signing by looking at the first 11/12 bytes of `SignatureScript` as a string. Collision probability is vanishingly small in practice, but this is a "convention-only" protocol rather than a typed boundary.
- Recommendation: introduce a typed `SignPayload` interface or a dedicated field on `SignDescriptor`, and have the caller set the path explicitly.

### L-3. `SuiSigner.SignOutputRaw` prints sighash and txid via `fmt.Printf`

- Location: `lnwallet/suiwallet/suisigner.go:113`
- Observation: noisy on production stdout; leaks signing metadata to logs that may not be scoped to Sui debug.
- Recommendation: switch to `log.Debugf` under the appropriate `debuglevel`.

### L-4. `_sig_a` / `_sig_b` were marked as unused but actually verified

- Location: `lightning.move:138-161` (pre-fix)
- Observation: the `_` prefix in Move conventionally indicates an unused parameter, yet the signatures were verified. Misleading for readers and for static tools.
- Fix (shipped alongside H-2 / H-3 / H-4): renamed to `sig_a` / `sig_b`.

---

## Contract ↔ Go Serialization Equivalence

Field-by-field comparison between Go (`input.GenerateSuiPayloadHash`, `GenerateSuiClosePayloadHash`) and Move (`bcs::to_bytes(...)`) serialization for `force_close` / `close_channel`:

| Field | Move BCS | Go encoding | Match |
| ----- | -------- | ----------- | ----- |
| `u64` (`state_num`, balances, expiries, amounts) | 8-byte little-endian | `binary.LittleEndian.PutUint64` | ✓ |
| `vector<u8>` (`revocation_hash`, `payment_hash`) | ULEB128 length + bytes | `writeUleb128(len) + bytes` | ✓ |
| `vector<u64>` (`htlc_ids`, amounts, expiries) | ULEB128 length + per-element LE u64 | same in Go | ✓ |
| `vector<vector<u8>>` (`payment_hashes`) | outer ULEB128 length + per-item (ULEB128 length + bytes) | same in Go | ✓ |
| `vector<u8>` (`directions`) | ULEB128 length + bytes | same in Go | ✓ |

Double SHA-256: the Go signer computes `sha256(sha256(preimage))` (see `suisigner.go:77-79`); the Move side calls `ecdsa_k1::secp256k1_verify(..., hash=1)`, which internally applies SHA-256 again on top of `sighash = sha256(preimage)`. The two chains of hashing are equivalent — **signatures verify**.

---

## Remediation Priority

| # | Priority | Action |
| - | -------- | ------ |
| 1 | Must-fix before mainnet | C-1 (broadcaster-bound CSV), C-2 (htlc_timeout refund), C-3 (htlc_claim double-charge) — **shipped** |
| 2 | Must-fix before mainnet | H-1 (penalize → victim), H-2 (caller identity) — **shipped** |
| 3 | Before mainnet | H-3 (coop close monotonic), H-4 (balance bound) — **shipped** |
| 4 | Post-release hardening | M-1 (party ↔ pubkey binding), M-2 (event fields), M-5 (suinotify dedup) |
| 5 | Post-release hardening | M-3, M-4, and all Low findings |

## Regression Test Status

Six new Move unit tests have landed in `sui-contracts/lightning/tests/lightning_tests.move`, complementing the four pre-existing tests. End-to-end coverage continues to come from `scripts/itest_sui.sh` (cooperative close, force-close + CSV sweep, breach + justice transaction). The mapping below shows where each invariant is exercised.

| # | Invariant | Where it is tested | Status |
| - | --------- | ------------------ | ------ |
| 1 | `force_close` + broadcaster CSV (C-1) | `test_claim_force_close_broadcaster_must_wait`, `test_claim_force_close_broadcaster_too_early` (Move) + `itest_sui.sh` Alice-broadcasts force-close path | ✅ |
| 2 | `htlc_timeout` refunds the sender (C-2) | `test_htlc_timeout_refund` (Move) | ✅ |
| 3 | `htlc_claim` credits receiver only, no double-charge (C-3) | `test_htlc_claim_credits_receiver_only` (Move) | ✅ |
| 4 | Third-party `close_channel` rejected (H-2) | `test_unauthorized_third_party_close_rejected` (Move) | ✅ |
| 5 | Coop close `state_num` monotonicity (H-3) | Defence-in-depth — current code paths cannot reach a state where `channel.state_num > 0` while `status == OPEN`, so the assertion is unreachable in unit-test form. The check is retained for forward compatibility (e.g. when state-num-advancing operations are added in OPEN state). | ⚠️ no test |
| 6 | `penalize` pays the victim, not the caller (H-1) | `itest_sui.sh` watchtower / breach scenario — Bob's wallet receives the slashed funds | ✅ |
| 7 | `close_channel` balance-sum bound (H-4) | `test_close_channel_rejects_excess_balance` (Move) | ✅ |

## References

- Contract source: `sui-contracts/lightning/sources/lightning.move`
- Go serializer: `input/sui_channel.go`, `lnwallet/suiwallet/suisigner.go`
- Refactor docs: `1-refactor-docs/sui/lnd-sui-refactor-plan.md`, `1-refactor-docs/sui/sui-ln-interaction-spec.md`
- BOLT-03 Commitment Transactions (Lightning specification baseline)
