package lnwallet

import (
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwire"
)

// TestBuildEvmStateUpdateSymmetry asserts the core correctness property of the
// EVM commitment bridge: the StateUpdate is keyless and channel-absolute, so the
// two peers — who see mirror-image commitment views (each one's "our" balance is
// the other's "their", outgoing HTLCs become incoming) and opposite IsInitiator
// flags — must derive the byte-for-byte identical digest. That single shared
// state per nonce is what both sign; if this diverged, every commitment would be
// rejected as invalid_commit_sig.
func TestBuildEvmStateUpdateSymmetry(t *testing.T) {

	// 6-decimal token exercises the down-scaling path (and must cancel out
	// identically on both sides).
	SetEvmCommitmentParams(input.EvmDomain{ChainID: 31337}, 6)

	var addrA, addrB [20]byte
	addrA[19] = 0xA1
	addrB[19] = 0xB2

	var channelID [32]byte
	channelID[0] = 0xCC

	const (
		balAInternal = btcutil.Amount(600_000_000)
		balBInternal = btcutil.Amount(400_000_000)
		htlcAmt      = btcutil.Amount(25_000_000)
	)
	var rHash PaymentHash
	rHash[0], rHash[31] = 0xDE, 0xAD

	htlc := paymentDescriptor{
		HtlcIndex: 4,
		Amount:    lnwire.NewMSatFromSatoshis(htlcAmt),
		RHash:     rHash,
		Timeout:   1_700_000_000,
	}

	// Initiator (A) view: our=A, their=B, the HTLC is A→B (outgoing).
	viewA := &commitment{
		height:        9,
		ourBalance:    lnwire.NewMSatFromSatoshis(balAInternal),
		theirBalance:  lnwire.NewMSatFromSatoshis(balBInternal),
		outgoingHTLCs: []paymentDescriptor{htlc},
	}

	// Non-initiator (B) view: mirror — our=B, their=A, the same HTLC is now
	// incoming (A→B from B's PoV).
	viewB := &commitment{
		height:        9,
		ourBalance:    lnwire.NewMSatFromSatoshis(balBInternal),
		theirBalance:  lnwire.NewMSatFromSatoshis(balAInternal),
		incomingHTLCs: []paymentDescriptor{htlc},
	}

	suA := buildEvmStateUpdate(viewA, channelID, true, addrA, addrB)
	suB := buildEvmStateUpdate(viewB, channelID, false, addrB, addrA)

	domain := EvmCommitmentDomain()
	if suA.Digest(domain) != suB.Digest(domain) {
		t.Fatalf("perspective digests diverge:\n A: %+v\n B: %+v", suA, suB)
	}

	// Sanity: A is the funder, so balanceA must be A's balance regardless of
	// which view computed it.
	wantA := evmScaleToBase(balAInternal)
	if suA.BalanceA.Cmp(wantA) != 0 || suB.BalanceA.Cmp(wantA) != 0 {
		t.Fatalf("balanceA mismatch: A=%s B=%s want=%s",
			suA.BalanceA, suB.BalanceA, wantA)
	}

	// htlcsHash must be the non-zero Merkle root of the single shared HTLC.
	if suA.HtlcsHash == ([32]byte{}) {
		t.Fatal("expected non-zero htlcsHash for a 1-HTLC state")
	}
}

// TestEvmScaleToBase pins the local Decimals Scaling Factor (kept in lockstep
// with evmwallet.ScaleToBase) for representative token precisions.
func TestEvmScaleToBase(t *testing.T) {

	cases := []struct {
		name     string
		decimals uint8
		amt      btcutil.Amount
		want     *big.Int
	}{
		// 1 token internal (1e8) -> 1e6 base units at 6 decimals.
		{"usdc-6dp", 6, 100_000_000, big.NewInt(1_000_000)},
		// 8 decimals is identity with the internal scale.
		{"identity-8dp", 8, 123_456_789, big.NewInt(123_456_789)},
		// 18 decimals scales up by 1e10.
		{"eth-18dp", 18, 1, new(big.Int).Mul(big.NewInt(1), pow10Int(10))},
		{"zero", 6, 0, big.NewInt(0)},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			SetEvmCommitmentParams(input.EvmDomain{}, tc.decimals)
			if got := evmScaleToBase(tc.amt); got.Cmp(tc.want) != 0 {
				t.Fatalf("scale(%d, %ddp) = %s, want %s",
					tc.amt, tc.decimals, got, tc.want)
			}
		})
	}
}
