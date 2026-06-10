package lnwallet

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/channeldb"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
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

// TestEvmBreachEvidence exercises the full phase-3.2 breach path: the
// counterparty signs the canonical StateUpdate (as it would in commitment_signed),
// LND retains only the 64-byte wire form, and evmBreachEvidence must reconstruct
// the 65-byte signature the contract's forceClose/penalize accept — recovering to
// the remote funding address. This is the EVM analogue of breach retribution data
// (no per-commitment secret; the signed newer state is the proof).
func TestEvmBreachEvidence(t *testing.T) {
	SetEvmCommitmentParams(input.EvmDomain{ChainID: 31337}, 6)

	// Local and remote funding keys; the remote is the "counterparty" whose
	// signature the contract must recover.
	localPriv, _ := btcec.NewPrivateKey()
	remotePriv, _ := btcec.NewPrivateKey()

	var fundingHash [32]byte
	fundingHash[0] = 0xFE

	chanState := &channeldb.OpenChannel{
		IsInitiator: true,
		LocalChanCfg: channeldb.ChannelConfig{
			MultiSigKey: keychain.KeyDescriptor{
				PubKey: localPriv.PubKey(),
			},
		},
		RemoteChanCfg: channeldb.ChannelConfig{
			MultiSigKey: keychain.KeyDescriptor{
				PubKey: remotePriv.PubKey(),
			},
		},
	}
	chanState.FundingOutpoint.Hash = fundingHash
	lc := &LightningChannel{channelState: chanState}

	view := &commitment{
		height:       12,
		ourBalance:   lnwire.NewMSatFromSatoshis(700_000_000),
		theirBalance: lnwire.NewMSatFromSatoshis(300_000_000),
	}

	// The counterparty signs the canonical StateUpdate digest; LND keeps only
	// the 64-byte r||s on the wire.
	digest := lc.stateUpdateForView(view).Digest(EvmCommitmentDomain())
	rawSig := btcecdsa.Sign(remotePriv, digest[:])
	wireSig, err := lnwire.NewSigFromSignature(rawSig)
	if err != nil {
		t.Fatal(err)
	}

	ev, err := lc.evmBreachEvidence(view, wireSig)
	if err != nil {
		t.Fatalf("evmBreachEvidence: %v", err)
	}
	if len(ev.Sig) != 65 {
		t.Fatalf("want 65-byte sig, got %d", len(ev.Sig))
	}
	if ev.Nonce != 12 {
		t.Fatalf("nonce = %d, want 12", ev.Nonce)
	}
	if ev.ChannelID != fundingHash {
		t.Fatalf("channelId = %x, want %x", ev.ChannelID, fundingHash)
	}

	// The reconstructed signature must recover to the remote funding address,
	// exactly as the contract's ECDSA.recover would.
	compact := make([]byte, 65)
	compact[0] = ev.Sig[64]
	copy(compact[1:], ev.Sig[:64])
	recovered, _, err := btcecdsa.RecoverCompact(compact, digest[:])
	if err != nil {
		t.Fatalf("recover failed: %v", err)
	}
	wantAddr := input.EvmAddressFromPubKey(remotePriv.PubKey())
	if input.EvmAddressFromPubKey(recovered) != wantAddr {
		t.Fatal("breach evidence does not recover to the counterparty")
	}

	// A signature from the wrong party must be rejected.
	badSig := btcecdsa.Sign(localPriv, digest[:])
	badWire, _ := lnwire.NewSigFromSignature(badSig)
	if _, err := lc.evmBreachEvidence(view, badWire); err == nil {
		t.Fatal("expected error for a signature not from the counterparty")
	}
}

// TestEvmHtlcResolution checks the resolver bridge: the claimHtlc carrier it
// produces embeds an HTLC whose leaf + Merkle proof verify against the very
// htlcsHash committed in that state's StateUpdate. If they didn't, the contract's
// _verifyHtlcInclusion would reject the claim. Three HTLCs exercise a non-trivial
// proof.
func TestEvmHtlcResolution(t *testing.T) {
	SetEvmCommitmentParams(input.EvmDomain{ChainID: 31337}, 6)

	localPriv, _ := btcec.NewPrivateKey()
	remotePriv, _ := btcec.NewPrivateKey()

	var fundingHash [32]byte
	fundingHash[0] = 0xFE
	chanState := &channeldb.OpenChannel{
		IsInitiator: true,
		LocalChanCfg: channeldb.ChannelConfig{
			MultiSigKey: keychain.KeyDescriptor{PubKey: localPriv.PubKey()},
		},
		RemoteChanCfg: channeldb.ChannelConfig{
			MultiSigKey: keychain.KeyDescriptor{PubKey: remotePriv.PubKey()},
		},
	}
	chanState.FundingOutpoint.Hash = fundingHash
	lc := &LightningChannel{channelState: chanState}

	mkHtlc := func(idx uint64, amt btcutil.Amount, b byte) paymentDescriptor {
		var rh PaymentHash
		rh[0] = b
		return paymentDescriptor{
			HtlcIndex: idx,
			Amount:    lnwire.NewMSatFromSatoshis(amt),
			RHash:     rh,
			Timeout:   1_700_000_000,
		}
	}
	view := &commitment{
		height:       7,
		ourBalance:   lnwire.NewMSatFromSatoshis(500_000_000),
		theirBalance: lnwire.NewMSatFromSatoshis(300_000_000),
		outgoingHTLCs: []paymentDescriptor{
			mkHtlc(1, 10_000_000, 0x01),
			mkHtlc(2, 20_000_000, 0x02),
		},
		incomingHTLCs: []paymentDescriptor{
			mkHtlc(3, 30_000_000, 0x03),
		},
	}

	// The htlcsHash this state commits to.
	committed := lc.stateUpdateForView(view).HtlcsHash

	var preimage [32]byte
	preimage[0] = 0xBE
	tx, err := lc.evmHtlcResolution(view, 2, &preimage)
	if err != nil {
		t.Fatalf("evmHtlcResolution: %v", err)
	}

	gotID, callType, raw, err := input.DecodeEvmCallTx(tx)
	if err != nil {
		t.Fatalf("DecodeEvmCallTx: %v", err)
	}
	if gotID != chainhash.Hash(fundingHash) {
		t.Fatalf("channelId mismatch: %x", gotID)
	}
	if callType != input.EvmCallClaimHtlc {
		t.Fatalf("callType = %s, want ClaimHtlc", callType)
	}

	var p input.EvmHtlcResolvePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}

	// Rebuild the HTLC leaf from the payload and verify the proof against the
	// committed root — exactly what the contract does on-chain.
	amt, _ := new(big.Int).SetString(p.HTLC.Amount, 10)
	hashlock := mustHex32(t, p.HTLC.Hashlock)
	var recip [20]byte
	copy(recip[:], mustHexBytes(t, p.HTLC.Recipient))
	leaf := input.EvmHTLC{
		Index:     p.HTLC.Index,
		Amount:    amt,
		Hashlock:  hashlock,
		Timelock:  p.HTLC.Timelock,
		Recipient: recip,
	}.Leaf()

	proof := make([][32]byte, len(p.MerkleProof))
	for i, h := range p.MerkleProof {
		proof[i] = mustHex32(t, h)
	}
	if !input.VerifyHtlcMerkleProof(proof, committed, leaf) {
		t.Fatal("claim HTLC leaf+proof do not verify against the " +
			"committed htlcsHash")
	}

	// A non-existent index must error.
	if _, err := lc.evmHtlcResolution(view, 99, &preimage); err == nil {
		t.Fatal("expected error for an unknown HTLC index")
	}
}

func mustHex32(t *testing.T, s string) [32]byte {
	t.Helper()
	b := mustHexBytes(t, s)
	if len(b) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(b))
	}
	var out [32]byte
	copy(out[:], b)

	return out
}

func mustHexBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}

	return b
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
