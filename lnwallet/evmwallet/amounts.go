package evmwallet

import (
	"math/big"

	"github.com/btcsuite/btcd/btcutil"
)

// internalDecimals is the number of decimal places LND's internal
// btcutil.Amount represents per unit, matching Bitcoin's 1 BTC = 1e8 sat. The
// Decimals Scaling Factor maps an ERC20's base-units to this internal scale so
// channel capacities and HTLC amounts flow through the unchanged Lightning
// accounting (integration doc §5).
const internalDecimals = 8

// pow10 returns 10^n as a big.Int.
func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// ScaleToInternal converts a raw ERC20 amount (base-units, 10^tokenDecimals per
// token) into LND's internal btcutil.Amount (1e8 per token).
//
// Rounding is asymmetric and deliberate: this is used for balances and incoming
// values, so it rounds DOWN — the node never credits itself fractional dust it
// cannot settle on-chain. The dropped remainder is below the representable
// internal precision (the dust floor) and is documented as unspendable.
func ScaleToInternal(raw *big.Int, tokenDecimals uint8) btcutil.Amount {
	if raw == nil || raw.Sign() <= 0 {
		return 0
	}

	scaled := new(big.Int).Mul(raw, pow10(internalDecimals))
	scaled.Quo(scaled, pow10(int(tokenDecimals))) // truncates toward zero

	return btcutil.Amount(scaled.Int64())
}

// ScaleToBase converts an internal btcutil.Amount back to raw ERC20 base-units.
// It is exact when tokenDecimals >= internalDecimals; when the token has fewer
// decimals than the internal scale the conversion rounds DOWN, so an outgoing
// amount never exceeds what the sender authorized.
func ScaleToBase(amt btcutil.Amount, tokenDecimals uint8) *big.Int {
	if amt <= 0 {
		return big.NewInt(0)
	}

	raw := new(big.Int).Mul(big.NewInt(int64(amt)), pow10(int(tokenDecimals)))
	raw.Quo(raw, pow10(internalDecimals)) // truncates toward zero

	return raw
}
