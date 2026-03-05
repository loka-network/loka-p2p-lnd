package chainfee

// SetuEstimator provides a static fee estimator for the Setu backend.
// It reuses the existing StaticEstimator with Setu-specific defaults.
type SetuEstimator struct {
	*StaticEstimator
}

// setuStaticFeePerKW is a placeholder fee rate used until Setu exposes a
// dynamic fee API. The value mirrors the Bitcoin default (50 sat/vbyte)
// expressed in sat/kw.
const setuStaticFeePerKW = SatPerKWeight(12500)

// setuStaticRelayFeePerKW mirrors the chain-wide minimum relay fee.
const setuStaticRelayFeePerKW = FeePerKwFloor

// NewSetuEstimator constructs an Estimator that always returns the static
// Setu defaults. The values are static to align with the current Setu
// backend capabilities.
func NewSetuEstimator() *SetuEstimator {
	return &SetuEstimator{
		StaticEstimator: NewStaticEstimator(
			setuStaticFeePerKW,
			setuStaticRelayFeePerKW,
		),
	}
}

// A compile-time check to ensure SetuEstimator implements the Estimator
// interface.
var _ Estimator = (*SetuEstimator)(nil)
