package chainfee

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewSuiEstimatorImplementsEstimator verifies that SuiEstimator satisfies
// the Estimator interface.
func TestNewSuiEstimatorImplementsEstimator(t *testing.T) {
	t.Parallel()

	var _ Estimator = NewSuiEstimator()
}

// TestSuiEstimatorFeePerKW checks that the returned sat/kw fee matches the
// expected static value.
func TestSuiEstimatorFeePerKW(t *testing.T) {
	t.Parallel()

	est := NewSuiEstimator()
	require.NoError(t, est.Start())
	t.Cleanup(func() { require.NoError(t, est.Stop()) })

	targets := []uint32{1, 2, 6, 12, 100, 1000}
	for _, target := range targets {
		fee, err := est.EstimateFeePerKW(target)
		require.NoError(t, err, "target=%d", target)
		require.Equal(t, suiStaticFeePerKW, fee,
			"unexpected fee for target=%d", target)
	}
}

// TestSuiEstimatorRelayFeePerKW checks that the relay fee matches the Sui
// static relay constant.
func TestSuiEstimatorRelayFeePerKW(t *testing.T) {
	t.Parallel()

	est := NewSuiEstimator()
	require.NoError(t, est.Start())
	t.Cleanup(func() { require.NoError(t, est.Stop()) })

	require.Equal(t, suiStaticRelayFeePerKW, est.RelayFeePerKW())
}

// TestSuiEstimatorStartStop verifies that Start and Stop are idempotent and
// return no errors.
func TestSuiEstimatorStartStop(t *testing.T) {
	t.Parallel()

	est := NewSuiEstimator()
	require.NoError(t, est.Start())
	require.NoError(t, est.Stop())

	// A second Start/Stop cycle should also succeed.
	require.NoError(t, est.Start())
	require.NoError(t, est.Stop())
}
