package lnwire

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// TestEvmChanIDIsChannelID confirms that the existing NewChanIDFromOutPoint
// already yields the 32-byte EVM channelId verbatim: EVM funding uses
// OutPoint.Index = 0 (no UTXO index), so the lower-2-byte XOR is the identity.
// No EVM-specific ChanID code is needed.
func TestEvmChanIDIsChannelID(t *testing.T) {
	t.Parallel()

	var channelID chainhash.Hash
	for i := range channelID {
		channelID[i] = byte(i + 1)
	}
	// Non-zero low bytes ensure we'd notice an unintended XOR.
	channelID[30], channelID[31] = 0xAB, 0xCD

	op := wire.OutPoint{Hash: channelID, Index: 0}
	cid := NewChanIDFromOutPoint(op)

	if !cid.IsChanPoint(&op) {
		t.Fatal("ChanID does not map back to the EVM outpoint")
	}
	for i := range channelID {
		if cid[i] != channelID[i] {
			t.Fatalf("ChanID byte %d = %x, want %x (index-0 XOR must "+
				"be identity)", i, cid[i], channelID[i])
		}
	}
}

// TestNewEvmShortChanID checks the direct (blockNumber, txIndex, logIndex)
// mapping and the mod-2²⁴ overflow escape hatch for L2 block heights.
func TestNewEvmShortChanID(t *testing.T) {
	t.Parallel()

	// In-range coordinates map verbatim and survive the uint64 round-trip.
	scid := NewEvmShortChanID(12_345, 7, 3)
	if scid.BlockHeight != 12_345 || scid.TxIndex != 7 ||
		scid.TxPosition != 3 {

		t.Fatalf("in-range mapping wrong: %+v", scid)
	}
	if got := NewShortChanIDFromInt(scid.ToUint64()); got != scid {
		t.Fatalf("uint64 round-trip changed scid: %+v != %+v", got, scid)
	}

	// A block height past 2²⁴ wraps (documented escape hatch) and stays
	// within the 24-bit field.
	overflow := uint64(scidMax24) + 100
	wrapped := NewEvmShortChanID(overflow, 0, 0)
	if wrapped.BlockHeight != 99 {
		t.Fatalf("overflow block height = %d, want 99 (mod 2^24)",
			wrapped.BlockHeight)
	}
	if wrapped.BlockHeight > scidMax24 {
		t.Fatalf("block height %d exceeds 24-bit field",
			wrapped.BlockHeight)
	}

	// TxIndex is masked to 24 bits.
	masked := NewEvmShortChanID(1, scidMax24+5, 0)
	if masked.TxIndex > scidMax24 {
		t.Fatalf("txIndex %d exceeds 24-bit field", masked.TxIndex)
	}
}
