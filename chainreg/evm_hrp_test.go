package chainreg

import (
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/zpay32"
)

// TestEvmBech32HRP checks the per-sub-network invoice HRP is deterministic,
// distinct across (chainID, token) pairs, and a well-formed bech32 HRP. Because
// zpay32 forms the invoice prefix as "ln" + Bech32HRPSegwit and rejects a
// mismatching prefix on decode, distinctness here is what makes invoices
// non-cross-decodable between sub-networks (integration doc §6.1.1).
func TestEvmBech32HRP(t *testing.T) {
	t.Parallel()

	base := ResolveEvmParams("base", 8453, "0xtokenA", "0xcontract")
	sameChainOtherToken := ResolveEvmParams(
		"base", 8453, "0xtokenB", "0xcontract",
	)
	otherChainSameToken := ResolveEvmParams(
		"other", 10, "0xtokenA", "0xcontract",
	)

	hrpBase := base.Bech32HRP()
	hrpToken := sameChainOtherToken.Bech32HRP()
	hrpChain := otherChainSameToken.Bech32HRP()

	// Deterministic: recomputing yields the same value.
	if again := base.Bech32HRP(); again != hrpBase {
		t.Fatalf("HRP not deterministic: %q != %q", again, hrpBase)
	}

	// Distinct per token and per chain.
	if hrpBase == hrpToken {
		t.Fatalf("different tokens share HRP %q", hrpBase)
	}
	if hrpBase == hrpChain {
		t.Fatalf("different chains share HRP %q", hrpBase)
	}

	// Well-formed: "evm" prefix, lowercase, reasonable length for a bech32
	// HRP (the full invoice prefix is "ln"+HRP).
	for _, hrp := range []string{hrpBase, hrpToken, hrpChain} {
		if !strings.HasPrefix(hrp, "evm") {
			t.Fatalf("HRP %q missing evm prefix", hrp)
		}
		if hrp != strings.ToLower(hrp) {
			t.Fatalf("HRP %q must be lowercase", hrp)
		}
		if len(hrp) != len("evm")+8 {
			t.Fatalf("HRP %q wrong length %d", hrp, len(hrp))
		}
	}
}

// TestEvmNetParams checks that the ActiveNetParams stand-in for an EVM
// sub-network carries the synthesized GenesisHash, the per-sub-network
// invoice HRP and the EVM coin type — and that building it does not mutate
// the shared global regtest params it is copied from.
func TestEvmNetParams(t *testing.T) {
	t.Parallel()

	p := ResolveEvmParams("base", 8453, "0xtokenA", "0xcontract")
	net := EvmNetParams(p)

	if *net.GenesisHash != p.GenesisHash {
		t.Fatalf("GenesisHash = %v, want synthesized %v",
			net.GenesisHash, p.GenesisHash)
	}
	if net.Bech32HRPSegwit != p.Bech32HRP() {
		t.Fatalf("Bech32HRPSegwit = %q, want %q",
			net.Bech32HRPSegwit, p.Bech32HRP())
	}
	if net.CoinType != p.CoinType || net.HDCoinType != p.CoinType {
		t.Fatalf("coin types = (%d, %d), want %d",
			net.CoinType, net.HDCoinType, p.CoinType)
	}

	// The global regtest placeholder must be untouched.
	global := BitcoinRegTestNetParams
	if net.Params == global.Params {
		t.Fatal("EvmNetParams aliases the global regtest params")
	}
	if *global.GenesisHash == p.GenesisHash {
		t.Fatal("global regtest GenesisHash was mutated")
	}
	if strings.HasPrefix(global.Bech32HRPSegwit, "evm") {
		t.Fatal("global regtest Bech32HRPSegwit was mutated")
	}
}

// TestEvmInvoiceSegregation pins the §6.1.1 invoice claim end-to-end: a
// zpay32 invoice encoded under one (chainID, token) sub-network decodes
// there, and fails to decode under a different token on the same chain —
// purely via the per-sub-network HRP, with no zpay32 change.
func TestEvmInvoiceSegregation(t *testing.T) {
	t.Parallel()

	netUSDC := EvmNetParams(
		ResolveEvmParams("base", 8453, "0xtokenA", "0xc"),
	)
	netUSDT := EvmNetParams(
		ResolveEvmParams("base", 8453, "0xtokenB", "0xc"),
	)

	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("priv key: %v", err)
	}
	signer := zpay32.MessageSigner{
		SignCompact: func(msg []byte) ([]byte, error) {
			hash := chainhash.HashB(msg)
			return ecdsa.SignCompact(privKey, hash, true), nil
		},
	}

	var payHash [32]byte
	payHash[0] = 0x42
	invoice, err := zpay32.NewInvoice(
		netUSDC.Params, payHash, time.Unix(1700000000, 0),
		zpay32.Description("evm sub-network test"),
	)
	if err != nil {
		t.Fatalf("new invoice: %v", err)
	}

	encoded, err := invoice.Encode(signer)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(encoded, "ln"+netUSDC.Bech32HRPSegwit) {
		t.Fatalf("invoice %q missing prefix ln%s", encoded,
			netUSDC.Bech32HRPSegwit)
	}

	// Same sub-network decodes.
	if _, err := zpay32.Decode(encoded, netUSDC.Params); err != nil {
		t.Fatalf("same-sub-network decode failed: %v", err)
	}

	// A different token on the same chain must reject the invoice.
	if _, err := zpay32.Decode(encoded, netUSDT.Params); err == nil {
		t.Fatal("cross-sub-network decode unexpectedly succeeded")
	}
}
