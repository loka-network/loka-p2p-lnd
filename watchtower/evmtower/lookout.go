package evmtower

import (
	"context"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
)

// Penalizer submits a penalize call carrying the backup's higher-nonce
// co-signed state. The contract pays the broadcaster-derived victim regardless
// of who sends the transaction, so the implementation may sign with any funded
// relayer key.
type Penalizer interface {
	Penalize(ctx context.Context, b *JusticeBackup) error
}

// Config configures a Lookout.
type Config struct {
	// Client is the tower's own EVM RPC client.
	Client evmnotify.EvmClient

	// Contract is the ChannelManager address to watch.
	Contract common.Address

	// Store holds the latest JusticeBackup per protected channel.
	Store BackupStore

	// Penalizer submits the penalize transaction on a detected breach.
	Penalizer Penalizer

	// PollInterval is how often the chain is scanned for close events.
	PollInterval time.Duration

	// FromBlock is where the scan starts (e.g. the contract's deploy block).
	// 0 starts a recent window back from the tip rather than trawling from
	// genesis on a long chain — set it to the deploy block to catch closes
	// that occurred before the tower started.
	FromBlock uint64

	// WindowSize bounds the block span of each eth_getLogs query so a
	// range-capped public RPC (e.g. sepolia.base.org caps at 2000) doesn't
	// reject it. The scan advances a cursor forward in WindowSize chunks,
	// catching up any backlog over successive polls. 0 uses defaultScanWindow.
	WindowSize uint64

	// ReorgDepth holds the most recent blocks back from the scan: a close is
	// only acted on once it is this many blocks deep, so one that gets
	// reorged away never triggers a wasted (reverting) penalize. 0 uses
	// defaultReorgDepth. The added latency is a few blocks — negligible
	// against any real challenge window (≥ 24h in production).
	ReorgDepth uint64
}

// defaultScanWindow is the per-query block span when Config.WindowSize is 0.
// Kept well under common public-RPC eth_getLogs caps (sepolia.base.org: 2000).
const defaultScanWindow = 1800

// defaultReorgDepth is the block hold-back when Config.ReorgDepth is 0,
// matching the node's evmReorgSafetyDepth-class buffer for L2 reorgs.
const defaultReorgDepth = 2

// Lookout watches the ChannelManager for UnilateralCloseInitiated events and,
// when a force-close broadcasts a state older than the backup it holds for that
// channel, submits penalize on the (offline) victim's behalf — the EVM analogue
// of the Bitcoin lookout's breach-hint match → justice broadcast.
type Lookout struct {
	cfg Config

	// cursor is the next block to scan from; advanced forward in
	// WindowSize chunks. cursorSet guards lazy init on the first scan.
	cursor    uint64
	cursorSet bool

	doneMu sync.Mutex
	done   map[[32]byte]bool // channels already penalized this run

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewLookout constructs a Lookout.
func NewLookout(cfg Config) *Lookout {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Second
	}

	return &Lookout{
		cfg:  cfg,
		done: make(map[[32]byte]bool),
		quit: make(chan struct{}),
	}
}

// Start launches the watch loop.
func (l *Lookout) Start() {
	l.wg.Add(1)
	go l.watch()
}

// Stop halts the watch loop.
func (l *Lookout) Stop() {
	close(l.quit)
	l.wg.Wait()
}

// shouldPenalize is the pure breach predicate: we hold a backup for the channel
// whose nonce strictly exceeds the broadcast one (i.e. the broadcast state was
// revoked). Factored out for unit testing.
func shouldPenalize(backup *JusticeBackup, broadcastNonce uint64) bool {
	return backup != nil && backup.Nonce > broadcastNonce
}

// watch polls for UnilateralCloseInitiated logs and dispatches penalize.
func (l *Lookout) watch() {
	defer l.wg.Done()

	ticker := time.NewTicker(l.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.scan()
		case <-l.quit:
			return
		}
	}
}

// scan does one pass over the next chunk of unscanned blocks. It advances a
// cursor forward in WindowSize-bounded chunks so each eth_getLogs query stays
// under the RPC's range cap, and a backlog (e.g. after downtime, or starting
// from a deploy block far behind the tip) is caught up over successive polls.
func (l *Lookout) scan() {
	ctx, cancel := context.WithTimeout(
		context.Background(), l.cfg.PollInterval,
	)
	defer cancel()

	tip, err := l.cfg.Client.BlockNumber(ctx)
	if err != nil {
		log.Warnf("evmtower: block number: %v", err)

		return
	}

	window := l.cfg.WindowSize
	if window == 0 {
		window = defaultScanWindow
	}

	// Lazily anchor the cursor on the first scan. A configured FromBlock
	// (e.g. the deploy block) is honoured verbatim and chunked forward, so
	// closes since deployment are caught even far behind the tip. With no
	// FromBlock set, start one window back from the tip rather than trawling
	// a long chain from genesis.
	if !l.cursorSet {
		l.cursor = l.cfg.FromBlock
		if l.cfg.FromBlock == 0 && tip > window {
			l.cursor = tip - window
		}
		l.cursorSet = true
	}

	// Hold the most recent ReorgDepth blocks back: only scan up to a
	// reorg-safe tip, so the cursor re-scans the held-back range once it
	// settles instead of acting on a close that might be reorged away.
	depth := l.cfg.ReorgDepth
	if depth == 0 {
		depth = defaultReorgDepth
	}
	var safeTip uint64
	if tip > depth {
		safeTip = tip - depth
	}

	from := l.cursor
	if from > safeTip {
		// Nothing reorg-safe to scan yet.
		return
	}
	// Span [from, to] is at most `window` blocks (inclusive), staying under
	// the RPC's range cap, and never past the reorg-safe tip.
	to := from + window - 1
	if to > safeTip {
		to = safeTip
	}

	logs, err := l.cfg.Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(from),
		ToBlock:   new(big.Int).SetUint64(to),
		Addresses: []common.Address{l.cfg.Contract},
		Topics: [][]common.Hash{
			{evmnotify.TopicUnilateralCloseInitiated},
		},
	})
	if err != nil {
		// Leave the cursor put so the same range is retried next poll.
		log.Warnf("evmtower: filter logs [%d,%d]: %v", from, to, err)

		return
	}

	for _, lg := range logs {
		l.handleClose(ctx, lg)
	}

	// Advance past the scanned range only on success.
	l.cursor = to + 1
}

// handleClose evaluates one UnilateralCloseInitiated log and penalizes if it is
// a breach against a backup we hold.
func (l *Lookout) handleClose(ctx context.Context, lg types.Log) {
	if len(lg.Topics) < 2 {
		return
	}
	channelID := [32]byte(lg.Topics[1])

	l.doneMu.Lock()
	already := l.done[channelID]
	l.doneMu.Unlock()
	if already {
		return
	}

	_, broadcastNonce, challengeExpiry, err := evmnotify.UnpackUnilateralClose(
		lg.Data,
	)
	if err != nil {
		log.Warnf("evmtower: decode close: %v", err)

		return
	}

	backup, ok := l.cfg.Store.Get(channelID)
	if !ok || !shouldPenalize(backup, broadcastNonce) {
		return
	}

	// The penalize must land before the challenge window closes; if it has
	// already passed there is nothing the tower can do.
	if challengeExpiry != 0 && uint64(time.Now().Unix()) >= challengeExpiry {
		log.Warnf("evmtower: ChannelID(%x): breach (broadcast nonce %d "+
			"< backup %d) but challenge window already closed",
			channelID, broadcastNonce, backup.Nonce)

		return
	}

	log.Infof("evmtower: ChannelID(%x): breach detected (broadcast nonce "+
		"%d < backup %d); submitting penalize", channelID,
		broadcastNonce, backup.Nonce)

	if err := l.cfg.Penalizer.Penalize(ctx, backup); err != nil {
		log.Errorf("evmtower: ChannelID(%x): penalize failed: %v",
			channelID, err)

		return
	}

	l.doneMu.Lock()
	l.done[channelID] = true
	l.doneMu.Unlock()
}
