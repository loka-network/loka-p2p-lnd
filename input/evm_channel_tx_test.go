package input

import (
	"encoding/json"
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
