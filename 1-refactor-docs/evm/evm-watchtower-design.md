# EVM Watchtower — Design (H-1 breach-remedy delegation)

> Status: **design, pre-implementation.** Audit reference: `1-refactor-docs/evm/security-audit.md` H-1.
> Date: 2026-06-22.

## 1. Problem

The EVM breach remedy is `penalize`: present a higher-nonce co-signed `StateUpdate` during the force-close challenge window, and the contract awards the full escrow to the victim. After the H-1 fix, `penalize` derives the victim from the recorded `broadcaster` and pays that address **regardless of who submits the transaction** — so a third party holding the higher-nonce co-signed state can defend an offline victim, and a leaked state can at worst return the victim their own funds.

What's missing is the *third party*: today the only thing that submits `penalize` is the victim's **own** node, online, via `contractcourt/evm_close.go:dispatchEvmBreach`. If the node is offline when the counterparty broadcasts a revoked state, nothing penalizes and the cheater finalizes the obsolete state after the window. A watchtower closes this gap.

## 2. How the other backends handle the breach (and why none is a true watchtower)

| Backend | Breach detection + remedy | Separate always-on tower? |
| ------- | ------------------------- | ------------------------- |
| **Bitcoin (upstream)** | Full watchtower subsystem (`watchtower/`): client backs up an encrypted `JusticeKit` blob keyed by the breach txid on each revocation; a separately-deployable tower (`wtserver` + `lookout`) matches breach txids on-chain, decrypts, and broadcasts a justice sweep tx. | **Yes** — but Bitcoin-only (see §3), and **not wired for Sui or EVM**. |
| **Sui** | Reuses LND's `BreachArbitrator` (`contractcourt/breach_arbitrator.go`) with `IsSui` overrides (`:425,:470,:1093,:1468`). The "Broadcasting justice tx" the Sui itest greps is the BreachArbitrator emitting a Sui `penalize`. The itest's step-10 "Watchtower" test **restarts Bob's node** and checks his BreachArbitrator re-detects + penalizes. | **No** — this is *local-node recovery after restart*, not a third party watching for an offline node. |
| **EVM (today)** | Bespoke inline `dispatchEvmBreach` in the chain watcher (no BreachArbitrator: its Bitcoin justice-tx path has no EVM analogue). Node-online-only. | **No.** |

**Conclusion:** the "Sui watchtower" is a misnomer — Sui has no separate tower; it has BreachArbitrator + restart survival. So "make EVM consistent with Sui" has two readings, and the design below offers both as staged options: (A) match Sui's *local-recovery* robustness cheaply, then (B) build the genuine separate tower that neither chain has yet.

## 3. Why the Bitcoin watchtower framework cannot be reused

The `watchtower/` subsystem is deeply UTXO-coupled — confirmed by audit of the package:

- **Blob / `JusticeKit`** (`watchtower/blob/justice_kit.go`) encodes SegWit/Taproot **witness scripts**, revocation pubkeys, and CSV delays for to-local/to-remote **outputs**. EVM has no outputs/witnesses — the remedy is one signed struct.
- **Breach identification** is a txid hint (`BreachHint = SHA256(txid)[:16]`, `derivation.go`) matched against every transaction in every block. EVM breaches are `UnilateralCloseInitiated` **events** carrying an explicit `nonce`; there is nothing to brute-match.
- **Justice** (`lookout/justice_descriptor.go`) builds a full Bitcoin sweep `wire.MsgTx` with BIP69 sorting and script-engine re-validation. EVM "justice" is a single `penalize(...)` contract call.
- **Sessions / policy** (`wtpolicy`) govern sweep-fee rates and rewards — meaningless for a deterministic on-chain `penalize`.

There is **no chain abstraction** in `watchtower/`; it is hardcoded to Bitcoin. An EVM tower must be a parallel, EVM-native subsystem that *follows the same client→tower→punish shape* with EVM-appropriate data.

## 4. EVM-native watchtower — data model

The only thing a tower needs to penalize is what `penalize` consumes. Define a self-contained backup record (everything reconstructable from `channeldb.OpenChannel.LocalCommitment`, see `lnwallet/evm_commitment.go:EvmPenalizeTx`):

```
EvmJusticeBackup {
    channelID   [32]byte   // on-chain channelId
    nonce       uint64     // StateNum of this co-signed state (must beat the broadcast one)
    balanceA    *big.Int   // raw token base-units
    balanceB    *big.Int
    htlcsHash   [32]byte
    counterpartySig [65]byte // counterparty's EIP-712 StateUpdate sig (r‖s‖v), recovers to broadcaster
}
```

Notes:
- **No encryption is required for correctness.** Unlike Bitcoin (where the blob hands the tower the keys to *spend*, so it must stay encrypted until the breach), the EVM backup only lets the holder submit `penalize`, which can only ever pay the **victim** (H-1). A leaked backup cannot steal. Encryption is therefore optional privacy hardening, not a safety requirement — a deliberate simplification over Bitcoin's txid-keyed blob.
- **Only the latest state need be retained.** `penalize` requires `correctNonce > broadcastNonce`; the latest co-signed state always has the highest nonce, so the tower keeps **one record per channel**, overwritten on each update. (Bitcoin must keep every revoked state because its justice tx is per-commitment.)
- The backup is produced by the same code path as `EvmPenalizeTx` — factor that builder to accept an `EvmJusticeBackup` rather than reading `chanState` directly, so node-self-penalize and tower-penalize share one path.

## 5. Client side (protected node)

- **Hook point:** the same place Bitcoin backs up — on receiving the counterparty's revocation, in `htlcswitch/link.go` (`handleRevocationMsg`, the `l.cfg.TowerClient.BackupState` call site). Gate it to EVM channels + tower-active. For EVM we back up the **new latest** co-signed state (highest nonce), not the just-revoked one.
- **API** (mirrors `wtclient.ClientManager`, trimmed):
  ```go
  type EvmTowerClient interface {
      RegisterChannel(chanID lnwire.ChannelID) error
      BackupState(chanID lnwire.ChannelID, backup EvmJusticeBackup) error
      AddTower(*lnwire.NetAddress) error
      RemoveTower(*btcec.PublicKey, net.Addr) error
  }
  ```
- Reuse `lncfg`/wiring patterns from `server.go`'s `cfg.WtClient.Active` block.

## 6. Tower side (separately deployable)

- **Persistence:** one `EvmJusticeBackup` per `(channelID)` in a bbolt bucket (reuse `wtdb` patterns; no session/blob-type complexity).
- **Lookout:** the tower runs its **own** `chainntnfs/evmnotify` against the same `ChannelManager`, subscribing to `UnilateralCloseInitiated`. On each event it reads `(broadcaster, nonce, challengeExpiry)` (already decoded by `evmnotify`), looks up the stored backup for that `channelID`, and if `storedNonce > eventNonce` → **breach** → builds `penalize` from the backup and broadcasts before `challengeExpiry`. This *replaces* Bitcoin's "scan every txid" with a direct event→nonce comparison.
- **Submission:** the tower needs gas and an EVM key to send `penalize`; the payout still goes to the victim (contract-enforced), so the tower's key is just a relayer. Reuse `evmwallet` broadcast (the `broadcastCallFrom` retry path) or a minimal `cast`-style sender.

## 7. Wire protocol (phase 2)

Minimal, over the existing **brontide** authenticated transport (as Bitcoin's `AuthDial`): `RegisterChannel`, `BackupState(EvmJusticeBackup)`, `Ack`/`Error`. No session negotiation, no policy, no reward accounting. Far smaller than `wtwire`.

## 8. Wiring & gating

- New `lncfg` group (e.g. `--evmwatchtower.active` for the tower, `--evmwtclient.active` + `--evmwtclient.towers` for the client), mirroring `WtClient`/`Watchtower`.
- Construct the client manager in `server.go` only when EVM is the backend **and** the client is active; pass it to the link like `TowerClient`. Gate the Bitcoin `TowerClient` registration to **non-EVM** channels so the two never cross.
- The tower is its own runnable (another `lnd --evmwatchtower.active`, or a standalone binary), with its own RPC endpoint + `ChannelManager`/token config.

## 9. Security properties

- **No theft surface:** a tower (or a leaked backup) can only submit `penalize`, which pays the broadcaster-derived victim — never the submitter (H-1). This is *why* encryption/sessions can be dropped relative to Bitcoin.
- **Liveness, not custody:** the tower never holds channel funds or keys that move funds; worst case a malicious/offline tower simply *fails to defend*, degrading to today's node-online-only behavior — it cannot make things worse than no tower.
- **Privacy (optional):** the backup reveals channel balances to the tower. If that matters, encrypt the backup with a key derived from `(channelID, nonce)` and have the tower decrypt only after observing the on-chain close — a lighter analogue of Bitcoin's txid-keyed scheme. Deferred unless required.
- **Single-RPC caveat (M-1) applies to the tower too:** a tower behind a lying RPC could miss the close. Same mitigation/deferral as M-1.

## 10. Proposed phasing

- **Phase 1 — core loop, single daemon, anvil-tested.** `EvmJusticeBackup` + the refactored shared `penalize` builder + a persistent per-channel backup store + an `EvmLookout` that watches `UnilateralCloseInitiated` and submits `penalize`. Drive it end-to-end on anvil: node A backs up → node A goes *offline* → B force-closes with a revoked state → the lookout (running in a separate process/keyring) penalizes → A's address receives the escrow. Proves the mechanism without a network protocol (backup handed over locally / via a file or loopback RPC).
- **Phase 2 — networked tower.** Add the brontide client/tower wire protocol (§7), `lncfg` groups, and `server.go` wiring so a real remote tower can be registered and backed up to.
- **(Alternative / cheaper interim) — match Sui:** instead of/before Phase 2, route the EVM breach through the existing `BreachArbitrator` with an `IsEvm` path (as Sui did with `IsSui`), gaining restart-survival local recovery without a separate tower. Lower effort, but *not* true offline protection — documents the tradeoff explicitly.

## 11. Decisions (resolved 2026-06-23)

1. **Both phases, in order.** Implement Phase 1 (core loop, anvil-tested) first, then Phase 2 (networked brontide tower). Each phase verified + committed independently.
2. **No encryption — plaintext backup.** Safe per §9: a leaked backup can only submit `penalize`, which pays the broadcaster-derived victim, never the submitter (H-1). Optional privacy encryption is left as future hardening, not built.
3. **Tower runs as `lnd --evmwatchtower.active`**, mirroring Bitcoin's embedded `--watchtower.active` tower (not a standalone binary).
4. **No `BreachArbitrator` `IsEvm` path.** With a real standalone tower covering the offline case and the existing inline `dispatchEvmBreach` covering the online case, the Sui-style `IsEvm` BreachArbitrator graft is redundant and will not be added.

## References
- `1-refactor-docs/evm/security-audit.md` (H-1)
- Bitcoin watchtower: `watchtower/{wtclient,wtserver,lookout,blob,wtdb,wtwire,wtpolicy}/`
- Sui breach path: `contractcourt/breach_arbitrator.go` (`IsSui` seams)
- EVM breach path: `contractcourt/evm_close.go` (`dispatchEvmBreach`), `lnwallet/evm_commitment.go` (`EvmPenalizeTx`)
- Client hook precedent: `htlcswitch/link.go` (`BackupState` call site)
