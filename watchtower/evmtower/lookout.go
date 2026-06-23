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

	// FromBlock bounds the historical log scan (e.g. the contract's deploy
	// block); 0 scans from genesis where the RPC allows it.
	FromBlock uint64
}

// Lookout watches the ChannelManager for UnilateralCloseInitiated events and,
// when a force-close broadcasts a state older than the backup it holds for that
// channel, submits penalize on the (offline) victim's behalf — the EVM analogue
// of the Bitcoin lookout's breach-hint match → justice broadcast.
type Lookout struct {
	cfg Config

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

// scan does one pass over recent close events.
func (l *Lookout) scan() {
	ctx, cancel := context.WithTimeout(
		context.Background(), l.cfg.PollInterval,
	)
	defer cancel()

	q := ethereum.FilterQuery{
		Addresses: []common.Address{l.cfg.Contract},
		Topics: [][]common.Hash{
			{evmnotify.TopicUnilateralCloseInitiated},
		},
	}
	if l.cfg.FromBlock != 0 {
		q.FromBlock = new(big.Int).SetUint64(l.cfg.FromBlock)
	}

	logs, err := l.cfg.Client.FilterLogs(ctx, q)
	if err != nil {
		log.Warnf("evmtower: filter logs: %v", err)

		return
	}

	for _, lg := range logs {
		l.handleClose(ctx, lg)
	}
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
