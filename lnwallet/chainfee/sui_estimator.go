package chainfee

// SuiEstimator provides a static fee estimator for the Sui backend.
// It reuses the existing StaticEstimator with Sui-specific defaults.
type SuiEstimator struct {
	*StaticEstimator
}

// suiStaticFeePerKW is a placeholder fee rate used until Sui exposes a
// dynamic fee API.
const suiStaticFeePerKW = SatPerKWeight(12500)

// suiStaticRelayFeePerKW mirrors the chain-wide minimum relay fee.
const suiStaticRelayFeePerKW = FeePerKwFloor

// NewSuiEstimator constructs an Estimator that always returns the static
// Sui defaults.
func NewSuiEstimator() *SuiEstimator {
	return &SuiEstimator{
		StaticEstimator: NewStaticEstimator(
			suiStaticFeePerKW,
			suiStaticRelayFeePerKW,
		),
	}
}

// A compile-time check to ensure SuiEstimator implements the Estimator
// interface.
var _ Estimator = (*SuiEstimator)(nil)
