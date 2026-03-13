package chainreg

import (
	"testing"

	"github.com/lightningnetwork/lnd/lncfg"
	"github.com/stretchr/testify/require"
)

// TestNewSuiPartialChainControlDefaults verifies that
// newSuiPartialChainControl succeeds and populates a PartialChainControl when
// Sui routing-policy overrides are not provided (cfg.Sui == nil).
func TestNewSuiPartialChainControlDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		SuiMode: func() *lncfg.SuiNode {
			n := lncfg.DefaultSuiNode()
			n.Active = true
			return n
		}(),
		// cfg.Sui is nil → routing policy uses Sui default constants.
	}

	cc, cleanup, err := newSuiPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cc)
	require.NotNil(t, cleanup)
	t.Cleanup(cleanup)

	// Routing policy should use the Sui default constants.
	require.Equal(
		t,
		DefaultSuiTimeLockDelta,
		int(cc.RoutingPolicy.TimeLockDelta),
	)
	require.EqualValues(t, DefaultSuiMinHTLCInMSat, int64(cc.MinHtlcIn))

	// Core components should be non-nil.
	require.NotNil(t, cc.ChainNotifier)
	require.NotNil(t, cc.FeeEstimator)
	require.NotNil(t, cc.BestBlockTracker)
	require.NotNil(t, cc.HealthCheck)
}

// TestNewSuiPartialChainControlCustomPolicy verifies that when cfg.Sui is
// provided the routing policy fields are taken from that config rather than
// from the default Sui constants.
func TestNewSuiPartialChainControlCustomPolicy(t *testing.T) {
	t.Parallel()

	const customTimeLockDelta = uint32(42)

	cfg := &Config{
		SuiMode: func() *lncfg.SuiNode {
			n := lncfg.DefaultSuiNode()
			n.Active = true
			return n
		}(),
		Sui: &lncfg.Chain{
			TimeLockDelta: customTimeLockDelta,
		},
	}

	cc, cleanup, err := newSuiPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cc)
	require.NotNil(t, cleanup)
	t.Cleanup(cleanup)

	require.Equal(t, customTimeLockDelta, cc.RoutingPolicy.TimeLockDelta)
}

// TestNewSuiPartialChainControlHealthCheck verifies that the HealthCheck
// function reports healthy immediately after construction.
func TestNewSuiPartialChainControlHealthCheck(t *testing.T) {
	t.Parallel()

	suiMode := lncfg.DefaultSuiNode()
	suiMode.Active = true
	cfg := &Config{SuiMode: suiMode}

	cc, cleanup, err := newSuiPartialChainControl(cfg)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	// HealthCheck should pass while the notifier is running.
	require.NoError(t, cc.HealthCheck())

	cleanup()
}

// TestNewSuiPartialChainControlFeeEstimator verifies that the fee estimator
// returns the static Sui defaults.
func TestNewSuiPartialChainControlFeeEstimator(t *testing.T) {
	t.Parallel()

	suiMode := lncfg.DefaultSuiNode()
	suiMode.Active = true
	cfg := &Config{SuiMode: suiMode}

	cc, cleanup, err := newSuiPartialChainControl(cfg)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	// After construction the fee estimator must already be started.
	fee, err := cc.FeeEstimator.EstimateFeePerKW(1)
	require.NoError(t, err)
	require.Positive(t, fee)
}
