package chainreg

import (
	"fmt"

	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/chainntnfs/suinotify"
	"github.com/lightningnetwork/lnd/graph/db/models"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
)

// newSuiPartialChainControl constructs a PartialChainControl for the Sui
// backend.
//
// The chain notifier is initialised with a NoopSuiClient placeholder. A
// real RPC-backed client (wrapping the Sui node) will replace it
// once the connectivity layer is implemented.
//
// Design notes:
//   - ChainSource and ChainView are left nil; Sui does not have a Bitcoin-
//     style UTXO set or a compact filter chain-view.
//   - FeeEstimator uses a static placeholder (SuiEstimator) since Sui
//     currently has no dynamic gas pricing API.
//   - RoutingPolicy is populated from cfg.Sui when set, otherwise the Sui
//     defaults are used.
func newSuiPartialChainControl(cfg *Config) (
	*PartialChainControl, func(), error) {

	// Derive routing policy from the Sui chain config.  If cfg.Sui is
	// nil we fall back to the Sui default constants defined in
	// sui_params.go.
	var (
		minHTLCIn     = lnwire.MilliSatoshi(DefaultSuiMinHTLCInMSat)
		minHTLCOut    = lnwire.MilliSatoshi(DefaultSuiMinHTLCOutMSat)
		baseFee       = lnwire.MilliSatoshi(DefaultSuiBaseFeeMSat)
		feeRate       = lnwire.MilliSatoshi(DefaultSuiFeeRate)
		timeLockDelta = uint32(DefaultSuiTimeLockDelta)
	)

	if cfg.Sui != nil {
		minHTLCIn = cfg.Sui.MinHTLCIn
		minHTLCOut = cfg.Sui.MinHTLCOut
		baseFee = cfg.Sui.BaseFee
		feeRate = cfg.Sui.FeeRate
		timeLockDelta = cfg.Sui.TimeLockDelta
	}

	// Build the partial chain control with Sui-specific components.
	cc := &PartialChainControl{
		Cfg: cfg,
		RoutingPolicy: models.ForwardingPolicy{
			MinHTLCOut:    minHTLCOut,
			BaseFee:       baseFee,
			FeeRate:       feeRate,
			TimeLockDelta: timeLockDelta,
		},
		MinHtlcIn: minHTLCIn,
		// Use the static Sui fee estimator.  When Sui exposes a
		// dynamic gas API this can be swapped for a live estimator.
		FeeEstimator: chainfee.NewSuiEstimator(),
	}

	// Create the chain notifier backed by the real RPC client.
	suiClient := suinotify.NewSuiRPCClient(cfg.SuiMode.RPCAddr())
	notifier := suinotify.New(suiClient)
	cc.ChainNotifier = notifier
	cc.SuiClient = suiClient

	// Start the notifier. If startup fails we abort here so that callers
	// do not hold a partially initialised control plane.
	if err := notifier.Start(); err != nil {
		return nil, nil, fmt.Errorf("suinotify: failed to start "+
			"chain notifier: %w", err)
	}

	// The best-block tracker wraps the notifier and is used by several
	// LND subsystems to keep a cached view of the chain tip.
	cc.BestBlockTracker = chainntnfs.NewBestBlockTracker(cc.ChainNotifier)

	// A trivial health check: as long as the notifier is running the
	// control plane is considered healthy.
	cc.HealthCheck = func() error {
		if !notifier.Started() {
			return fmt.Errorf("sui chain notifier is not running")
		}
		return nil
	}

	// Initialise the fee estimator. The static estimator Start() is a
	// no-op but we call it for interface compliance.
	if err := cc.FeeEstimator.Start(); err != nil {
		_ = notifier.Stop()
		return nil, nil, fmt.Errorf("sui fee estimator: failed to "+
			"start: %w", err)
	}

	// Cleanup function stops both the notifier and the fee estimator.
	cleanup := func() {
		if err := cc.FeeEstimator.Stop(); err != nil {
			log.Errorf("Failed to stop Sui fee estimator: %v", err)
		}
		if err := notifier.Stop(); err != nil {
			log.Errorf("Failed to stop Sui chain notifier: %v", err)
		}
	}

	return cc, cleanup, nil
}
