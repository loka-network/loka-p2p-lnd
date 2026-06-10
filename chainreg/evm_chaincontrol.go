package chainreg

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/graph/db/models"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"github.com/lightningnetwork/lnd/lnwire"
)

// newEvmPartialChainControl constructs a PartialChainControl for the EVM
// backend, mirroring newSuiPartialChainControl.
//
// Design notes:
//   - The chain notifier polls the JSON-RPC node (receipt depth for
//     confirmations, ChannelManager event logs for spends, header polling for
//     block epochs), so it works on HTTP-only endpoints and tolerates L2
//     sequencer WS drops.
//   - ChainSource and ChainView are left nil; EVM has no Bitcoin-style UTXO
//     set or compact-filter chain view.
//   - FeeEstimator is the live EvmEstimator backed by the node's gas-price
//     suggestions.
//   - RoutingPolicy is populated from cfg.Evm when set, otherwise the EVM
//     defaults (which mirror Bitcoin's) are used.
func newEvmPartialChainControl(cfg *Config) (
	*PartialChainControl, func(), error) {

	// Derive the routing policy from the EVM chain config, falling back to
	// the defaults in evm_params.go.
	var (
		minHTLCIn     = lnwire.MilliSatoshi(DefaultEvmMinHTLCInMSat)
		minHTLCOut    = lnwire.MilliSatoshi(DefaultEvmMinHTLCOutMSat)
		baseFee       = lnwire.MilliSatoshi(DefaultEvmBaseFeeMSat)
		feeRate       = lnwire.MilliSatoshi(DefaultEvmFeeRate)
		timeLockDelta = uint32(DefaultEvmTimeLockDelta)
	)

	if cfg.Evm != nil {
		minHTLCIn = cfg.Evm.MinHTLCIn
		minHTLCOut = cfg.Evm.MinHTLCOut
		baseFee = cfg.Evm.BaseFee
		feeRate = cfg.Evm.FeeRate
		timeLockDelta = cfg.Evm.TimeLockDelta
	}

	// Dial the JSON-RPC node. The same client instance is shared by the
	// notifier, the fee estimator and (via PartialChainControl.EvmClient)
	// the wallet/chain-IO adapters built later in the chain builder.
	evmClient, err := evmnotify.DialEvmClient(cfg.EvmMode.RPCHost)
	if err != nil {
		return nil, nil, fmt.Errorf("evmnotify: failed to dial EVM "+
			"node %s: %w", cfg.EvmMode.RPCHost, err)
	}

	cc := &PartialChainControl{
		Cfg: cfg,
		RoutingPolicy: models.ForwardingPolicy{
			MinHTLCOut:    minHTLCOut,
			BaseFee:       baseFee,
			FeeRate:       feeRate,
			TimeLockDelta: timeLockDelta,
		},
		MinHtlcIn:    minHTLCIn,
		FeeEstimator: chainfee.NewEvmEstimator(evmClient),
	}

	// Create the polling chain notifier watching the deployed
	// ChannelManager contract.
	contractAddr := common.HexToAddress(cfg.EvmMode.ContractAddress)
	notifier := evmnotify.New(evmClient, contractAddr)
	cc.ChainNotifier = notifier
	cc.EvmClient = evmClient

	// Start the notifier. If startup fails we abort here so that callers
	// do not hold a partially initialised control plane.
	if err := notifier.Start(); err != nil {
		evmClient.Close()
		return nil, nil, fmt.Errorf("evmnotify: failed to start "+
			"chain notifier: %w", err)
	}

	// The best-block tracker wraps the notifier and is used by several
	// LND subsystems to keep a cached view of the chain tip.
	cc.BestBlockTracker = chainntnfs.NewBestBlockTracker(cc.ChainNotifier)

	// A trivial health check: as long as the notifier is running the
	// control plane is considered healthy.
	cc.HealthCheck = func() error {
		if !notifier.Started() {
			return fmt.Errorf("evm chain notifier is not running")
		}
		return nil
	}

	// Initialise the fee estimator (primes the gas-price cache).
	if err := cc.FeeEstimator.Start(); err != nil {
		_ = notifier.Stop()
		evmClient.Close()
		return nil, nil, fmt.Errorf("evm fee estimator: failed to "+
			"start: %w", err)
	}

	// Cleanup stops the estimator and the notifier, then closes the shared
	// RPC client (go-ethereum's Close is safe against the wallet adapter
	// having already closed it).
	cleanup := func() {
		if err := cc.FeeEstimator.Stop(); err != nil {
			log.Errorf("Failed to stop EVM fee estimator: %v", err)
		}
		if err := notifier.Stop(); err != nil {
			log.Errorf("Failed to stop EVM chain notifier: %v", err)
		}
		evmClient.Close()
	}

	return cc, cleanup, nil
}
