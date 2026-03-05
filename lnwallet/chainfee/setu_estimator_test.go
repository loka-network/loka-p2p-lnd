package chainfee

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewSetuEstimatorImplementsEstimator verifies that SetuEstimator satisfies
// the Estimator interface at compile time (the var _ assertion in the source
// file is the primary check; this test documents the expectation explicitly).
func TestNewSetuEstimatorImplementsEstimator(t *testing.T) {
	t.Parallel()

	var _ Estimator = NewSetuEstimator()
}

// TestSetuEstimatorFeePerKW checks that the returned sat/kw fee matches the
// expected static value for all ConfirmTarget inputs.
func TestSetuEstimatorFeePerKW(t *testing.T) {
	t.Parallel()

	est := NewSetuEstimator()
	require.NoError(t, est.Start())
	t.Cleanup(func() { require.NoError(t, est.Stop()) })

	targets := []uint32{1, 2, 6, 12, 100, 1000}
	for _, target := range targets {
		fee, err := est.EstimateFeePerKW(target)
		require.NoError(t, err, "target=%d", target)
		require.Equal(t, setuStaticFeePerKW, fee,
			"unexpected fee for target=%d", target)
	}
}

// TestSetuEstimatorRelayFeePerKW checks that the relay fee matches the Setu
// static relay constant.
func TestSetuEstimatorRelayFeePerKW(t *testing.T) {
	t.Parallel()

	est := NewSetuEstimator()
	require.NoError(t, est.Start())
	t.Cleanup(func() { require.NoError(t, est.Stop()) })

	require.Equal(t, setuStaticRelayFeePerKW, est.RelayFeePerKW())
}

// TestSetuEstimatorStartStop verifies that Start and Stop are idempotent and
// return no errors.
func TestSetuEstimatorStartStop(t *testing.T) {
	t.Parallel()

	est := NewSetuEstimator()
	require.NoError(t, est.Start())
	require.NoError(t, est.Stop())

	// A second Start/Stop cycle should also succeed.
	require.NoError(t, est.Start())
	require.NoError(t, est.Stop())
}
