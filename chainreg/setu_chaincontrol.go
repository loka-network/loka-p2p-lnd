package chainreg

import (
	"fmt"

	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/chainntnfs/setunotify"
	"github.com/lightningnetwork/lnd/graph/db/models"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
)

// newSetuPartialChainControl constructs a PartialChainControl for the Setu
// DAG backend.
//
// The chain notifier is initialised with a NoopSetuClient placeholder. A
// real gRPC-backed client (wrapping the setu-validator node) will replace it
// once the connectivity layer is implemented.
//
// Design notes:
//   - ChainSource and ChainView are left nil; Setu does not have a Bitcoin-
//     style UTXO set or a compact filter chain-view.
//   - FeeEstimator uses a static placeholder (SetuEstimator) since Setu
//     currently has no dynamic gas pricing API.
//   - RoutingPolicy is populated from cfg.Setu when set, otherwise the Setu
//     defaults are used.
func newSetuPartialChainControl(cfg *Config) (
	*PartialChainControl, func(), error) {

	// Derive routing policy from the Setu chain config.  If cfg.Setu is
	// nil we fall back to the Setu default constants defined in
	// setu_params.go.
	var (
		minHTLCIn     = lnwire.MilliSatoshi(DefaultSetuMinHTLCInMSat)
		minHTLCOut    = lnwire.MilliSatoshi(DefaultSetuMinHTLCOutMSat)
		baseFee       = lnwire.MilliSatoshi(DefaultSetuBaseFeeMSat)
		feeRate       = lnwire.MilliSatoshi(DefaultSetuFeeRate)
		timeLockDelta = uint32(DefaultSetuTimeLockDelta)
	)

	if cfg.Setu != nil {
		minHTLCIn = cfg.Setu.MinHTLCIn
		minHTLCOut = cfg.Setu.MinHTLCOut
		baseFee = cfg.Setu.BaseFee
		feeRate = cfg.Setu.FeeRate
		timeLockDelta = cfg.Setu.TimeLockDelta
	}

	// Build the partial chain control with Setu-specific components.
	cc := &PartialChainControl{
		Cfg: cfg,
		RoutingPolicy: models.ForwardingPolicy{
			MinHTLCOut:    minHTLCOut,
			BaseFee:       baseFee,
			FeeRate:       feeRate,
			TimeLockDelta: timeLockDelta,
		},
		MinHtlcIn: minHTLCIn,
		// Use the static Setu fee estimator.  When Setu exposes a
		// dynamic gas API this can be swapped for a live estimator.
		FeeEstimator: chainfee.NewSetuEstimator(),
	}

	// Create the chain notifier backed by the no-op client stub.
	notifier := setunotify.New(&setunotify.NoopSetuClient{})
	cc.ChainNotifier = notifier

	// Start the notifier. If startup fails we abort here so that callers
	// do not hold a partially initialised control plane.
	if err := notifier.Start(); err != nil {
		return nil, nil, fmt.Errorf("setunotify: failed to start "+
			"chain notifier: %w", err)
	}

	// The best-block tracker wraps the notifier and is used by several
	// LND subsystems to keep a cached view of the chain tip.
	cc.BestBlockTracker = chainntnfs.NewBestBlockTracker(cc.ChainNotifier)

	// A trivial health check: as long as the notifier is running the
	// control plane is considered healthy.
	cc.HealthCheck = func() error {
		if !notifier.Started() {
			return fmt.Errorf("setu chain notifier is not running")
		}
		return nil
	}

	// Initialise the fee estimator. The static estimator Start() is a
	// no-op but we call it for interface compliance.
	if err := cc.FeeEstimator.Start(); err != nil {
		_ = notifier.Stop()
		return nil, nil, fmt.Errorf("setu fee estimator: failed to "+
			"start: %w", err)
	}

	// Cleanup function stops both the notifier and the fee estimator.
	cleanup := func() {
		if err := cc.FeeEstimator.Stop(); err != nil {
			log.Errorf("Failed to stop Setu fee estimator: %v", err)
		}
		if err := notifier.Stop(); err != nil {
			log.Errorf("Failed to stop Setu chain notifier: %v", err)
		}
	}

	return cc, cleanup, nil
}
