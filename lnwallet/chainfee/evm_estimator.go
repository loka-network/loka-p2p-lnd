package chainfee

import (
	"context"
	"math/big"
	"time"

	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
)

// evmStaticFeePerKW is the placeholder per-kw fee LND's internal accounting
// uses on EVM. Unlike Bitcoin, EVM channel commitments never hit the chain —
// only ChannelManager settlement calls do, and their gas is paid out-of-band by
// the broadcaster (refactor-plan §2.8, "gas paid directly; no anchor outputs").
// So this value only feeds dust/fee bookkeeping and is kept stable.
const evmStaticFeePerKW = SatPerKWeight(12500)

// evmStaticRelayFeePerKW mirrors the chain-wide minimum relay fee.
const evmStaticRelayFeePerKW = FeePerKwFloor

// gasPriceTimeout bounds the live gas-price RPC.
const gasPriceTimeout = 10 * time.Second

// EvmEstimator implements Estimator for EVM chains. The Estimator surface
// returns static values (see evmStaticFeePerKW); the genuinely dynamic part —
// the wei gas price used to actually broadcast settlement calls — is exposed
// separately via GasPrice / GasTipCap for the evmwallet adapter.
type EvmEstimator struct {
	*StaticEstimator

	client evmnotify.EvmClient
}

// A compile-time check to ensure EvmEstimator implements Estimator.
var _ Estimator = (*EvmEstimator)(nil)

// NewEvmEstimator constructs an EvmEstimator. The client may be nil, in which
// case only the static Estimator surface is available (GasPrice/GasTipCap then
// return an error).
func NewEvmEstimator(client evmnotify.EvmClient) *EvmEstimator {
	return &EvmEstimator{
		StaticEstimator: NewStaticEstimator(
			evmStaticFeePerKW, evmStaticRelayFeePerKW,
		),
		client: client,
	}
}

// GasPrice returns the node's currently suggested legacy gas price in wei
// (eth_gasPrice). Used by evmwallet to price ChannelManager settlement calls.
func (e *EvmEstimator) GasPrice() (*big.Int, error) {
	ctx, cancel := context.WithTimeout(
		context.Background(), gasPriceTimeout,
	)
	defer cancel()

	return e.client.SuggestGasPrice(ctx)
}

// GasTipCap returns the node's suggested EIP-1559 priority fee in wei
// (eth_maxPriorityFeePerGas) for chains that support dynamic-fee transactions.
func (e *EvmEstimator) GasTipCap() (*big.Int, error) {
	ctx, cancel := context.WithTimeout(
		context.Background(), gasPriceTimeout,
	)
	defer cancel()

	return e.client.SuggestGasTipCap(ctx)
}
