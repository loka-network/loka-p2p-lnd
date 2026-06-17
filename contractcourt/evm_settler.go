package contractcourt

import (
	"context"
	"time"

	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/channeldb"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// The EVM settler is the post-force-close worker that performs the on-chain
// actions Bitcoin's contract resolvers perform with sweeps: it claims
// incoming HTLCs once their preimage is known (claimHtlc + Merkle proof),
// reclaims expired outgoing HTLCs (timeoutHtlc), and finalises the channel
// with distributeFunds once the challenge window has passed. All actions are
// ChannelManager call carriers built by lnwallet and broadcast through the
// regular PublishTransaction path (evmwallet ABI-encodes them).

const (
	// evmSettlerPollInterval is how often the settler re-evaluates its
	// pending HTLC set and deadlines.
	evmSettlerPollInterval = 3 * time.Second

	// evmDistributeAttempts bounds the distributeFunds broadcasts. The
	// call is permissionless and idempotent-on-success (later attempts
	// revert harmlessly), but each attempt costs gas, so we stop after a
	// few tries; the counterparty's settler is attempting too.
	evmDistributeAttempts = 3

	// evmDistributeRetryDelay spaces the distributeFunds attempts far
	// enough apart for the counterparty's remaining claims to land.
	evmDistributeRetryDelay = 15 * time.Second
)

// launchEvmSettler starts the settlement worker for a unilaterally closed
// EVM channel. commit is the persisted commitment matching the broadcast
// state; challengeExpiry is the contract's challenge deadline (unix seconds,
// zero when unknown — distributeFunds is then skipped).
func (c *chainWatcher) launchEvmSettler(commit channeldb.ChannelCommitment,
	challengeExpiry uint64) {

	if c.cfg.publishTx == nil {
		log.Warnf("ChannelPoint(%v): no publishTx; EVM settler not "+
			"started", c.cfg.chanState.FundingOutpoint)

		return
	}

	// The settler runs on its own goroutine, NOT under the chainWatcher's
	// waitgroup/quit: a force-closed EVM channel has no Bitcoin resolvers,
	// so the arbitrator resolves it and stops this watcher within
	// milliseconds — long before the challenge window elapses. Its
	// lifecycle is the ChainArbitrator's (settlerQuit).
	go c.evmSettle(commit, challengeExpiry)
}

// evmSettle runs the settlement loop until every action is done or the
// watcher shuts down.
func (c *chainWatcher) evmSettle(commit channeldb.ChannelCommitment,
	challengeExpiry uint64) {

	chanPoint := c.cfg.chanState.FundingOutpoint
	channelID := [32]byte(chanPoint.Hash)

	log.Infof("ChannelPoint(%v): EVM settler started: %d HTLCs to "+
		"resolve, challenge expiry %d", chanPoint, len(commit.Htlcs),
		challengeExpiry)

	// Track the chain tip for outgoing-HTLC timeout gating (RefundTimeout
	// is an absolute EVM block height).
	epochs, err := c.cfg.notifier.RegisterBlockEpochNtfn(nil)
	if err != nil {
		log.Errorf("ChannelPoint(%v): EVM settler: epoch ntfn: %v",
			chanPoint, err)

		return
	}
	defer epochs.Cancel()

	pending := make(map[uint64]channeldb.HTLC, len(commit.Htlcs))
	for _, h := range commit.Htlcs {
		pending[h.HtlcIndex] = h
	}

	var (
		bestHeight          uint32
		distributeAttempts  int
		nextDistributeAfter time.Time
	)

	ticker := time.NewTicker(evmSettlerPollInterval)
	defer ticker.Stop()

	for {
		select {
		case epoch, ok := <-epochs.Epochs:
			if !ok {
				return
			}
			bestHeight = uint32(epoch.Height)

			continue

		case <-ticker.C:

		case <-c.cfg.settlerQuit:
			return
		}

		// If the channel is already CLOSED on-chain — our
		// distributeFunds landed, or the counterparty's settler beat us
		// to it — there's nothing left to do. Stop here rather than
		// re-broadcasting distributeFunds, which would just revert with
		// NotUnilateralClose and waste gas.
		if c.cfg.evmChannelStatus != nil {
			st, err := c.cfg.evmChannelStatus(channelID)
			if err != nil {
				log.Warnf("ChannelPoint(%v): EVM settler: "+
					"status query: %v", chanPoint, err)
			} else if st == evmnotify.ChannelStatusClosed {
				log.Infof("ChannelPoint(%v): EVM settler: "+
					"channel CLOSED on-chain, done",
					chanPoint)

				return
			}
		}

		// Resolve what's resolvable this tick.
		for idx, h := range pending {
			switch {
			// Incoming HTLC: claim as soon as the preimage shows
			// up in the witness store / invoice registry.
			case h.Incoming:
				preimage, ok := c.lookupEvmPreimage(
					lntypes.Hash(h.RHash),
				)
				if !ok {
					continue
				}

				if c.publishEvmHtlc(
					&commit, idx, &preimage,
				) {
					delete(pending, idx)
				}

			// Outgoing HTLC: reclaim once its absolute expiry
			// height has been reached.
			case bestHeight >= h.RefundTimeout:
				if c.publishEvmHtlc(&commit, idx, nil) {
					delete(pending, idx)
				}
			}
		}

		// Once nothing is pending on our side and the challenge
		// window has passed, finalise with distributeFunds.
		if len(pending) > 0 || challengeExpiry == 0 ||
			distributeAttempts >= evmDistributeAttempts {

			if len(pending) == 0 &&
				distributeAttempts >= evmDistributeAttempts {

				return
			}

			continue
		}
		now := time.Now()
		if now.Unix() < int64(challengeExpiry)+1 ||
			now.Before(nextDistributeAfter) {

			continue
		}

		tx, err := lnwallet.EvmDistributeFundsTx(c.cfg.chanState)
		if err != nil {
			log.Errorf("ChannelPoint(%v): EVM settler: build "+
				"distributeFunds: %v", chanPoint, err)

			return
		}

		distributeAttempts++
		nextDistributeAfter = now.Add(evmDistributeRetryDelay)
		log.Infof("ChannelPoint(%v): EVM settler broadcasting "+
			"distributeFunds (attempt %d/%d)", chanPoint,
			distributeAttempts, evmDistributeAttempts)
		if err := c.cfg.publishTx(tx, "evm-distribute"); err != nil {
			log.Warnf("ChannelPoint(%v): distributeFunds "+
				"broadcast: %v", chanPoint, err)
		}
	}
}

// lookupEvmPreimage looks for an HTLC preimage in the witness cache, then in
// the invoice registry (hold-invoice preimages only ever live there).
func (c *chainWatcher) lookupEvmPreimage(hash lntypes.Hash) ([32]byte, bool) {
	if c.cfg.preimageDB != nil {
		if pre, ok := c.cfg.preimageDB.LookupPreimage(hash); ok {
			return [32]byte(pre), true
		}
	}

	if c.cfg.registry != nil {
		inv, err := c.cfg.registry.LookupInvoice(
			context.Background(), hash,
		)
		if err == nil && inv.Terms.PaymentPreimage != nil {
			return [32]byte(*inv.Terms.PaymentPreimage), true
		}
	}

	return [32]byte{}, false
}

// publishEvmHtlc builds and broadcasts the claim (preimage != nil) or timeout
// carrier for one HTLC, reporting whether the broadcast was handed off.
func (c *chainWatcher) publishEvmHtlc(commit *channeldb.ChannelCommitment,
	htlcIndex uint64, preimage *[32]byte) bool {

	chanPoint := c.cfg.chanState.FundingOutpoint

	action, label := "timeoutHtlc", "evm-htlc-timeout"
	if preimage != nil {
		action, label = "claimHtlc", "evm-htlc-claim"
	}

	tx, err := lnwallet.EvmHtlcResolutionTx(
		c.cfg.chanState, commit, htlcIndex, preimage,
	)
	if err != nil {
		log.Errorf("ChannelPoint(%v): EVM settler: build %s for "+
			"htlc %d: %v", chanPoint, action, htlcIndex, err)

		// Unbuildable carriers won't improve on retry; drop the HTLC.
		return true
	}

	log.Infof("ChannelPoint(%v): EVM settler broadcasting %s for "+
		"htlc %d", chanPoint, action, htlcIndex)
	if err := c.cfg.publishTx(tx, label); err != nil {
		log.Warnf("ChannelPoint(%v): %s broadcast for htlc %d: %v — "+
			"will retry", chanPoint, action, htlcIndex, err)

		return false
	}

	return true
}
