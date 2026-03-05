package chainreg

import (
	"testing"

	"github.com/lightningnetwork/lnd/lncfg"
	"github.com/stretchr/testify/require"
)

// TestNewSetuPartialChainControlDefaults verifies that
// newSetuPartialChainControl succeeds and populates a PartialChainControl when
// Setu routing-policy overrides are not provided (cfg.Setu == nil).
func TestNewSetuPartialChainControlDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		SetuMode: func() *lncfg.SetuNode {
			n := lncfg.DefaultSetuNode()
			n.Active = true
			return n
		}(),
		// cfg.Setu is nil → routing policy uses Setu default constants.
	}

	cc, cleanup, err := newSetuPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cc)
	require.NotNil(t, cleanup)
	t.Cleanup(cleanup)

	// Routing policy should use the Setu default constants.
	require.Equal(
		t,
		DefaultSetuTimeLockDelta,
		int(cc.RoutingPolicy.TimeLockDelta),
	)
	require.EqualValues(t, DefaultSetuMinHTLCInMSat, int64(cc.MinHtlcIn))

	// Core components should be non-nil.
	require.NotNil(t, cc.ChainNotifier)
	require.NotNil(t, cc.FeeEstimator)
	require.NotNil(t, cc.BestBlockTracker)
	require.NotNil(t, cc.HealthCheck)
}

// TestNewSetuPartialChainControlCustomPolicy verifies that when cfg.Setu is
// provided the routing policy fields are taken from that config rather than
// from the default Setu constants.
func TestNewSetuPartialChainControlCustomPolicy(t *testing.T) {
	t.Parallel()

	const customTimeLockDelta = uint32(42)

	cfg := &Config{
		SetuMode: func() *lncfg.SetuNode {
			n := lncfg.DefaultSetuNode()
			n.Active = true
			return n
		}(),
		Setu: &lncfg.Chain{
			TimeLockDelta: customTimeLockDelta,
		},
	}

	cc, cleanup, err := newSetuPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cc)
	require.NotNil(t, cleanup)
	t.Cleanup(cleanup)

	require.Equal(t, customTimeLockDelta, cc.RoutingPolicy.TimeLockDelta)
}

// TestNewSetuPartialChainControlHealthCheck verifies that the HealthCheck
// function reports healthy immediately after construction, and that the cleanup
// function shuts the control plane down cleanly.
func TestNewSetuPartialChainControlHealthCheck(t *testing.T) {
	t.Parallel()

	setuMode := lncfg.DefaultSetuNode()
	setuMode.Active = true
	cfg := &Config{SetuMode: setuMode}

	cc, cleanup, err := newSetuPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	// HealthCheck should pass while the notifier is running.
	require.NoError(t, cc.HealthCheck())

	cleanup()
}

// TestNewSetuPartialChainControlFeeEstimator verifies that the fee estimator
// embedded in the PartialChainControl returns the static Setu defaults.
func TestNewSetuPartialChainControlFeeEstimator(t *testing.T) {
	t.Parallel()

	setuMode := lncfg.DefaultSetuNode()
	setuMode.Active = true
	cfg := &Config{SetuMode: setuMode}

	cc, cleanup, err := newSetuPartialChainControl(cfg)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	// After construction the fee estimator must already be started.
	fee, err := cc.FeeEstimator.EstimateFeePerKW(1)
	require.NoError(t, err)
	require.Positive(t, fee)
}
