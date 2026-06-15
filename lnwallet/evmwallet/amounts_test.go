package evmwallet

import (
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
)

func TestScaleRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      *big.Int
		decimals uint8
		want     btcutil.Amount
	}{
		// USDC: 6 decimals. 1 USDC (1e6 base) -> 1e8 internal.
		{"usdc 1 token", big.NewInt(1_000_000), 6, 100_000_000},
		{"usdc 600 tokens", big.NewInt(600_000_000), 6, 60_000_000_000},
		// 8-decimal token maps 1:1 with internal sats.
		{"8-dec identity", big.NewInt(123_456_789), 8, 123_456_789},
		{"zero", big.NewInt(0), 6, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScaleToInternal(tc.raw, tc.decimals)
			if got != tc.want {
				t.Fatalf("ScaleToInternal = %d, want %d",
					got, tc.want)
			}

			// Round-trip back to base-units must be lossless for
			// decimals >= internalDecimals or exact multiples.
			back := ScaleToBase(got, tc.decimals)
			if back.Cmp(tc.raw) != 0 {
				t.Fatalf("ScaleToBase round-trip = %s, want %s",
					back, tc.raw)
			}
		})
	}
}

func TestScaleDustFloor(t *testing.T) {
	t.Parallel()

	// With 6 decimals the internal scale (1e8) is FINER than base-units
	// (1e6): 1 base-unit == 100 internal units. So internal amounts not
	// divisible by 100 cannot be represented on-chain and round DOWN — the
	// dust floor lives in the internal->base direction.
	if got := ScaleToBase(99, 6); got.Sign() != 0 {
		t.Fatalf("sub-floor internal amount should scale to 0 base, "+
			"got %s", got)
	}
	if got := ScaleToBase(150, 6); got.Int64() != 1 {
		t.Fatalf("150 internal -> 1 base-unit (dust dropped), got %s",
			got)
	}
}

func TestEvmCallEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	call := EvmCall{
		Data:  []byte{0xde, 0xad, 0xbe, 0xef},
		Value: big.NewInt(0),
	}
	tx, err := WrapEvmCall(call)
	if err != nil {
		t.Fatal(err)
	}

	got, ok, err := unwrapEvmCall(tx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("envelope not recognized")
	}
	if string(got.Data) != string(call.Data) {
		t.Fatalf("data mismatch: %x vs %x", got.Data, call.Data)
	}

	// A non-envelope tx must not be misread as an EVM call.
	plain := wire.NewMsgTx(2)
	plain.AddTxIn(&wire.TxIn{SignatureScript: []byte("not-an-evm-call")})
	if _, ok, _ := unwrapEvmCall(plain); ok {
		t.Fatal("plain tx misidentified as EVM call")
	}
}

// TestScale18Decimals covers the ≥18-decimal path (DAI/WETH-style assets),
// where base-units (1e18/token) are FINER than the internal scale (1e8), so
// the dust floor lives in the base->internal (incoming/balance) direction.
// USDC/USDT (6-dec) only exercise the opposite direction, so this is the
// previously-untested case from the integration doc §5.
func TestScale18Decimals(t *testing.T) {
	t.Parallel()

	oneToken := pow10(18) // 1e18 wei == 1 token at 18 decimals.

	// 1 token <-> 1e8 internal, exact both ways.
	if got := ScaleToInternal(oneToken, 18); got != 100_000_000 {
		t.Fatalf("ScaleToInternal(1e18,18) = %d, want 1e8", got)
	}
	if got := ScaleToBase(100_000_000, 18); got.Cmp(oneToken) != 0 {
		t.Fatalf("ScaleToBase(1e8,18) = %s, want 1e18", got)
	}

	// internal->base is EXACT for 18 decimals (1 internal == 1e10 wei,
	// pure multiply), so no value is lost sending out.
	if got := ScaleToBase(1, 18); got.Cmp(big.NewInt(1e10)) != 0 {
		t.Fatalf("ScaleToBase(1,18) = %s, want 1e10 wei", got)
	}

	// base->internal rounds DOWN: a sub-1e10-wei tail is unrepresentable
	// internally and must be dropped (left with the contract), never
	// credited — the node can't create value it can't settle.
	dusty := new(big.Int).Add(oneToken, big.NewInt(9_999_999_999)) // +<1e10
	if got := ScaleToInternal(dusty, 18); got != 100_000_000 {
		t.Fatalf("ScaleToInternal(1e18+dust,18) = %d, want 1e8 "+
			"(dust dropped)", got)
	}

	// A raw balance smaller than one internal unit (1e10 wei) reads as 0
	// internal — unspendable dust, not a rounding-up to 1.
	if got := ScaleToInternal(big.NewInt(9_999_999_999), 18); got != 0 {
		t.Fatalf("sub-internal-unit raw should be 0 internal, got %d",
			got)
	}

	// Value-conservation round trip: scaling a dusty raw amount in then
	// back out must never EXCEED the original (value is only ever dropped,
	// never created).
	back := ScaleToBase(ScaleToInternal(dusty, 18), 18)
	if back.Cmp(dusty) > 0 {
		t.Fatalf("round-trip created value: %s > %s", back, dusty)
	}
}
