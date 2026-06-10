package chainreg

import (
	"testing"

	"github.com/lightningnetwork/lnd/lncfg"
	"github.com/stretchr/testify/require"
)

// activeEvmNode returns a default EvmNode config with the backend enabled.
// DialEvmClient over HTTP is lazy, so construction needs no live node.
func activeEvmNode() *lncfg.EvmNode {
	n := lncfg.DefaultEvmNode()
	n.Active = true

	return n
}

// TestNewEvmPartialChainControlDefaults verifies that
// newEvmPartialChainControl succeeds and populates a PartialChainControl when
// EVM routing-policy overrides are not provided (cfg.Evm == nil).
func TestNewEvmPartialChainControlDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		EvmMode: activeEvmNode(),
		// cfg.Evm is nil → routing policy uses EVM default constants.
	}

	cc, cleanup, err := newEvmPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cc)
	require.NotNil(t, cleanup)
	t.Cleanup(cleanup)

	// Routing policy should use the EVM default constants.
	require.Equal(
		t,
		DefaultEvmTimeLockDelta,
		int(cc.RoutingPolicy.TimeLockDelta),
	)
	require.EqualValues(t, DefaultEvmMinHTLCInMSat, int64(cc.MinHtlcIn))

	// Core components should be non-nil.
	require.NotNil(t, cc.ChainNotifier)
	require.NotNil(t, cc.FeeEstimator)
	require.NotNil(t, cc.BestBlockTracker)
	require.NotNil(t, cc.HealthCheck)
	require.NotNil(t, cc.EvmClient)
}

// TestNewEvmPartialChainControlCustomPolicy verifies that when cfg.Evm is
// provided the routing policy fields are taken from that config rather than
// from the default EVM constants.
func TestNewEvmPartialChainControlCustomPolicy(t *testing.T) {
	t.Parallel()

	const customTimeLockDelta = uint32(42)

	cfg := &Config{
		EvmMode: activeEvmNode(),
		Evm: &lncfg.Chain{
			TimeLockDelta: customTimeLockDelta,
		},
	}

	cc, cleanup, err := newEvmPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cc)
	require.NotNil(t, cleanup)
	t.Cleanup(cleanup)

	require.Equal(t, customTimeLockDelta, cc.RoutingPolicy.TimeLockDelta)
}

// TestNewEvmPartialChainControlHealthCheck verifies that the HealthCheck
// function reports healthy immediately after construction.
func TestNewEvmPartialChainControlHealthCheck(t *testing.T) {
	t.Parallel()

	cfg := &Config{EvmMode: activeEvmNode()}

	cc, cleanup, err := newEvmPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	// HealthCheck should pass while the notifier is running.
	require.NoError(t, cc.HealthCheck())

	cleanup()
}

// TestNewEvmPartialChainControlFeeEstimator verifies that the fee estimator
// returns the static EVM defaults for LND's internal accounting surface.
func TestNewEvmPartialChainControlFeeEstimator(t *testing.T) {
	t.Parallel()

	cfg := &Config{EvmMode: activeEvmNode()}

	cc, cleanup, err := newEvmPartialChainControl(cfg)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	// After construction the fee estimator must already be started.
	fee, err := cc.FeeEstimator.EstimateFeePerKW(1)
	require.NoError(t, err)
	require.Positive(t, fee)
}
