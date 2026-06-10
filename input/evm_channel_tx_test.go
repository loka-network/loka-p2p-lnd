package input

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// TestEvmChannelOpenTxRoundTrip checks the carrier tx round-trips: the channelId
// rides in the outpoint hash and the openChannel payload survives encode/decode.
func TestEvmChannelOpenTxRoundTrip(t *testing.T) {
	t.Parallel()

	var channelID chainhash.Hash
	channelID[0], channelID[31] = 0xC0, 0x1D

	payload := EvmChannelOpenPayload{
		Salt:          "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		Counterparty:  "f39fd6e51aad88f6f4ce6ab8827279cfffb92266",
		LocalBalance:  600_000_000,
		RemoteBalance: 400_000_000,
		LocalKey:      "02aa",
		RemoteKey:     "03bb",
	}

	tx, err := BuildEvmChannelOpenTx(channelID, payload)
	if err != nil {
		t.Fatalf("BuildEvmChannelOpenTx: %v", err)
	}

	gotID, callType, raw, err := DecodeEvmCallTx(tx)
	if err != nil {
		t.Fatalf("DecodeEvmCallTx: %v", err)
	}
	if gotID != channelID {
		t.Fatalf("channelId mismatch\n got %x\nwant %x", gotID, channelID)
	}
	if callType != EvmCallChannelOpen {
		t.Fatalf("callType = %q, want %q", callType, EvmCallChannelOpen)
	}

	var got EvmChannelOpenPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got != payload {
		t.Fatalf("payload mismatch\n got %+v\nwant %+v", got, payload)
	}
}

// TestDecodeEvmCallTxEmpty guards the no-input error path.
func TestDecodeEvmCallTxEmpty(t *testing.T) {
	t.Parallel()

	if _, _, _, err := DecodeEvmCallTx(nil); err == nil {
		t.Fatal("expected error decoding a nil tx")
	}
}

// TestEvmForceCloseTxRoundTrip checks the forceClose carrier preserves the
// StateUpdate fields and signature in the contract's raw units.
func TestEvmForceCloseTxRoundTrip(t *testing.T) {
	t.Parallel()

	var channelID chainhash.Hash
	channelID[0] = 0xFC
	var htlcsHash [32]byte
	htlcsHash[0] = 0x13
	sig := make([]byte, 65)
	sig[64] = 27

	tx, err := BuildEvmForceCloseTx(
		channelID, 9, big.NewInt(600_000_000), big.NewInt(400_000_000),
		htlcsHash, sig,
	)
	if err != nil {
		t.Fatalf("BuildEvmForceCloseTx: %v", err)
	}

	gotID, callType, raw, err := DecodeEvmCallTx(tx)
	if err != nil {
		t.Fatalf("DecodeEvmCallTx: %v", err)
	}
	if gotID != channelID || callType != EvmCallForceClose {
		t.Fatalf("id/type mismatch: %x / %s", gotID, callType)
	}

	var p EvmStateClosePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Nonce != 9 || p.BalanceA != "600000000" || p.BalanceB != "400000000" {
		t.Fatalf("state fields mismatch: %+v", p)
	}
	if len(p.Sig) != 130 { // 65 bytes hex
		t.Fatalf("sig hex len = %d, want 130", len(p.Sig))
	}
}

// TestEvmClaimHtlcTxRoundTrip checks the claimHtlc carrier preserves the HTLC,
// its proof, and the preimage.
func TestEvmClaimHtlcTxRoundTrip(t *testing.T) {
	t.Parallel()

	var channelID chainhash.Hash
	channelID[0] = 0xC1
	var recip [20]byte
	recip[19] = 0xAA
	htlc := EvmHTLC{
		Index:     3,
		Amount:    big.NewInt(20_000_000),
		Hashlock:  Keccak256([]byte("p")),
		Timelock:  1_700_000_000,
		Recipient: recip,
	}
	proof := [][32]byte{Keccak256([]byte("sib1")), Keccak256([]byte("sib2"))}
	var preimage [32]byte
	preimage[0] = 0xBE

	tx, err := BuildEvmClaimHtlcTx(channelID, htlc, proof, preimage)
	if err != nil {
		t.Fatalf("BuildEvmClaimHtlcTx: %v", err)
	}

	_, callType, raw, err := DecodeEvmCallTx(tx)
	if err != nil {
		t.Fatalf("DecodeEvmCallTx: %v", err)
	}
	if callType != EvmCallClaimHtlc {
		t.Fatalf("callType = %s, want ClaimHtlc", callType)
	}

	var p EvmHtlcResolvePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.HTLC.Index != 3 || p.HTLC.Amount != "20000000" {
		t.Fatalf("htlc fields mismatch: %+v", p.HTLC)
	}
	if len(p.MerkleProof) != 2 {
		t.Fatalf("proof len = %d, want 2", len(p.MerkleProof))
	}
	if p.Preimage == "" {
		t.Fatal("claim payload must carry a preimage")
	}

	// timeoutHtlc must omit the preimage.
	txTo, err := BuildEvmTimeoutHtlcTx(channelID, htlc, proof)
	if err != nil {
		t.Fatal(err)
	}
	_, _, rawTo, _ := DecodeEvmCallTx(txTo)
	var pTo EvmHtlcResolvePayload
	if err := json.Unmarshal(rawTo, &pTo); err != nil {
		t.Fatal(err)
	}
	if pTo.Preimage != "" {
		t.Fatal("timeout payload must not carry a preimage")
	}
}
