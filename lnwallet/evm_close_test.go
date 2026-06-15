package lnwallet

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightningnetwork/lnd/channeldb"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwire"
)

// evmCloseTestChannels builds the two mirror-image channel states of one
// channel: peer A is the initiator. Balances are in internal units.
func evmCloseTestChannels(t *testing.T, balA, balB btcutil.Amount,
	capacity btcutil.Amount) (*LightningChannel, *LightningChannel,
	*btcec.PrivateKey, *btcec.PrivateKey) {

	t.Helper()

	privA, _ := btcec.NewPrivateKey()
	privB, _ := btcec.NewPrivateKey()

	var fundingHash [32]byte
	fundingHash[0] = 0xC1

	mkState := func(isInitiator bool, localKey, remoteKey *btcec.PublicKey,
		localBal, remoteBal btcutil.Amount) *channeldb.OpenChannel {

		cs := &channeldb.OpenChannel{
			IsInitiator: isInitiator,
			Capacity:    capacity,
			LocalChanCfg: channeldb.ChannelConfig{
				MultiSigKey: keychain.KeyDescriptor{
					PubKey: localKey,
				},
			},
			RemoteChanCfg: channeldb.ChannelConfig{
				MultiSigKey: keychain.KeyDescriptor{
					PubKey: remoteKey,
				},
			},
			LocalCommitment: channeldb.ChannelCommitment{
				CommitHeight: 7,
				LocalBalance: lnwire.NewMSatFromSatoshis(
					localBal,
				),
				RemoteBalance: lnwire.NewMSatFromSatoshis(
					remoteBal,
				),
			},
		}
		cs.FundingOutpoint.Hash = fundingHash

		return cs
	}

	lcA := &LightningChannel{channelState: mkState(
		true, privA.PubKey(), privB.PubKey(), balA, balB,
	)}
	lcB := &LightningChannel{channelState: mkState(
		false, privB.PubKey(), privA.PubKey(), balB, balA,
	)}

	return lcA, lcB, privA, privB
}

// TestEvmCoopCloseSymmetry checks both peers derive the identical canonical
// (finalA, finalB) split — including when scaling truncates: the sub-unit
// remainder folds to A (the funder), and the sum is exactly the deposit.
func TestEvmCoopCloseSymmetry(t *testing.T) {
	SetEvmCommitmentParams(input.EvmDomain{ChainID: 31337}, 6)

	const capacity = btcutil.Amount(10_000_000_000) // 100 tokens internal

	// B's balance has a sub-base-unit tail (…6530 internal = ….30 raw
	// hundredths) that must be floored, the remainder going to A.
	const (
		balA = btcutil.Amount(9_500_003_470)
		balB = btcutil.Amount(499_996_530)
	)

	lcA, lcB, _, _ := evmCloseTestChannels(t, balA, balB, capacity)

	ccA := lcA.evmCoopClose()
	ccB := lcB.evmCoopClose()

	domain := EvmCommitmentDomain()
	if ccA.Digest(domain) != ccB.Digest(domain) {
		t.Fatalf("coop close digests diverge:\n A: %+v\n B: %+v",
			ccA, ccB)
	}

	totalRaw := evmScaleToBase(capacity)
	sum := new(big.Int).Add(ccA.FinalBalanceA, ccA.FinalBalanceB)
	if sum.Cmp(totalRaw) != 0 {
		t.Fatalf("finalA+finalB = %s, want totalDeposited %s",
			sum, totalRaw)
	}

	// B floored: 499996530 internal / 1e2 → 4999965 raw; A absorbs the
	// remainder.
	if ccA.FinalBalanceB.Cmp(big.NewInt(4_999_965)) != 0 {
		t.Fatalf("finalB = %s, want 4999965", ccA.FinalBalanceB)
	}
	if ccA.FinalBalanceA.Cmp(big.NewInt(95_000_035)) != 0 {
		t.Fatalf("finalA = %s, want 95000035", ccA.FinalBalanceA)
	}
}

// TestEvmCoopCloseCarrier runs the full cooperative-close signing path on both
// peers: each signs the canonical digest, each assembles the carrier from its
// own + the remote signature, and both carriers must encode the identical
// closeChannel call with sigA/sigB recovering to participants A/B.
func TestEvmCoopCloseCarrier(t *testing.T) {
	SetEvmCommitmentParams(input.EvmDomain{ChainID: 31337}, 6)

	lcA, lcB, privA, privB := evmCloseTestChannels(
		t, 9_500_000_000, 500_000_000, 10_000_000_000,
	)

	digest := lcA.evmCoopClose().Digest(EvmCommitmentDomain())
	sigA := btcecdsa.Sign(privA, digest[:])
	sigB := btcecdsa.Sign(privB, digest[:])

	// A assembles with (local=A, remote=B); B with (local=B, remote=A).
	carrierA, err := lcA.evmCoopCloseCarrier(sigA, sigB)
	if err != nil {
		t.Fatalf("A carrier: %v", err)
	}
	carrierB, err := lcB.evmCoopCloseCarrier(sigB, sigA)
	if err != nil {
		t.Fatalf("B carrier: %v", err)
	}

	decode := func(raw []byte) input.EvmChannelClosePayload {
		var p input.EvmChannelClosePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("payload: %v", err)
		}
		return p
	}

	_, typeA, rawA, err := input.DecodeEvmCallTx(carrierA)
	if err != nil || typeA != input.EvmCallChannelClose {
		t.Fatalf("A decode: type=%s err=%v", typeA, err)
	}
	_, _, rawB, err := input.DecodeEvmCallTx(carrierB)
	if err != nil {
		t.Fatalf("B decode: %v", err)
	}

	pA, pB := decode(rawA), decode(rawB)
	if pA != pB {
		t.Fatalf("peers assembled different closeChannel calls:\n"+
			" A: %+v\n B: %+v", pA, pB)
	}

	// sigA must recover to participant A (the initiator), sigB to B.
	checkRecovers := func(sigHex string, want *btcec.PublicKey) {
		t.Helper()
		sig, _ := hex.DecodeString(sigHex)
		if len(sig) != 65 {
			t.Fatalf("want 65-byte sig, got %d", len(sig))
		}
		compact := make([]byte, 65)
		compact[0] = sig[64]
		copy(compact[1:], sig[:64])
		rec, _, err := btcecdsa.RecoverCompact(compact, digest[:])
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if input.EvmAddressFromPubKey(rec) !=
			input.EvmAddressFromPubKey(want) {

			t.Fatal("signature recovers to the wrong participant")
		}
	}
	checkRecovers(pA.SigA, privA.PubKey())
	checkRecovers(pA.SigB, privB.PubKey())

	// A foreign signature must be rejected during assembly.
	rogue, _ := btcec.NewPrivateKey()
	bad := btcecdsa.Sign(rogue, digest[:])
	if _, err := lcA.evmCoopCloseCarrier(sigA, bad); err == nil {
		t.Fatal("expected error for non-counterparty signature")
	}
}

// TestEvmLocalForceCloseCarrier checks the unilateral-close broadcast
// artifact: the carrier embeds the disk state's StateUpdate and the retained
// counterparty signature (persisted in DER) with its recovery byte restored.
func TestEvmLocalForceCloseCarrier(t *testing.T) {
	SetEvmCommitmentParams(input.EvmDomain{ChainID: 31337}, 6)

	lcA, _, _, privB := evmCloseTestChannels(
		t, 9_500_000_000, 500_000_000, 10_000_000_000,
	)

	// The remote (B) signed our latest local commitment state; LND
	// persisted the DER form in LocalCommitment.CommitSig.
	commit := lcA.channelState.LocalCommitment
	su := evmStateUpdateFromDiskCommit(lcA.channelState, &commit)
	digest := su.Digest(EvmCommitmentDomain())
	lcA.channelState.LocalCommitment.CommitSig =
		btcecdsa.Sign(privB, digest[:]).Serialize()

	carrier, err := lcA.evmLocalForceCloseCarrier()
	if err != nil {
		t.Fatalf("force close carrier: %v", err)
	}

	chanID, callType, raw, err := input.DecodeEvmCallTx(carrier)
	if err != nil || callType != input.EvmCallForceClose {
		t.Fatalf("decode: type=%s err=%v", callType, err)
	}
	if [32]byte(chanID) != su.ChannelID {
		t.Fatalf("channelId mismatch")
	}

	var p input.EvmStateClosePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Nonce != commit.CommitHeight {
		t.Fatalf("nonce = %d, want %d", p.Nonce, commit.CommitHeight)
	}
	if p.BalanceA != su.BalanceA.String() ||
		p.BalanceB != su.BalanceB.String() {

		t.Fatalf("balances mismatch: %+v vs %+v", p, su)
	}
	if p.LocalKey == "" {
		t.Fatal("carrier missing LocalKey for participant broadcast")
	}

	sig, _ := hex.DecodeString(p.Sig)
	if len(sig) != 65 {
		t.Fatalf("want 65-byte sig, got %d", len(sig))
	}
	compact := make([]byte, 65)
	compact[0] = sig[64]
	copy(compact[1:], sig[:64])
	rec, _, err := btcecdsa.RecoverCompact(compact, digest[:])
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if input.EvmAddressFromPubKey(rec) !=
		input.EvmAddressFromPubKey(privB.PubKey()) {

		t.Fatal("retained sig does not recover to the counterparty")
	}
}

// TestEvmHtlcTimelockConversion checks that an LND CLTV-expiry block height is
// translated into the unix-second block.timestamp deadline the ChannelManager
// compares against — deterministically, so both peers commit the same value —
// and that an unset block time is an identity passthrough.
func TestEvmHtlcTimelockConversion(t *testing.T) {
	// Not parallel: mutates the package-global timelock params.
	defer SetEvmTimelockParams(0, 0)

	const (
		genesisTs = uint64(1_700_000_000)
		blockTime = uint64(2)
		cltvHt    = uint32(40_000)
	)

	// Passthrough when unset (the unit-test default).
	SetEvmTimelockParams(0, 0)
	if got := evmHtlcTimelock(cltvHt); got != cltvHt {
		t.Fatalf("passthrough: got %d, want %d", got, cltvHt)
	}

	// Configured: genesisTs + height*blockTime.
	SetEvmTimelockParams(genesisTs, blockTime)
	want := uint32(genesisTs + uint64(cltvHt)*blockTime)
	if got := evmHtlcTimelock(cltvHt); got != want {
		t.Fatalf("converted: got %d, want %d", got, want)
	}

	// Deterministic: recomputing yields the same value (the property that
	// keeps the htlcsHash identical across peers).
	if again := evmHtlcTimelock(cltvHt); again != want {
		t.Fatalf("not deterministic: %d != %d", again, want)
	}

	// The deadline must be a plausible future unix timestamp, not a raw
	// block height (the bug this fixes: a height compared against
	// block.timestamp reads as already-expired).
	if want <= uint32(genesisTs) {
		t.Fatalf("deadline %d not after genesis %d", want, genesisTs)
	}
}
