package evmtower

import (
	"sync"
	"time"
)

// ChannelSource yields the current JusticeBackup for every channel the
// protected node wants defended. The implementation lives in the daemon (it
// reads channeldb and builds backups via lnwallet); evmtower stays free of
// those dependencies.
type ChannelSource interface {
	// EvmBackups returns the latest backup for each open EVM channel.
	EvmBackups() ([]*JusticeBackup, error)
}

// BackupAgent is the client side of the EVM watchtower: it periodically snapshots
// the protected node's latest co-signed state per channel into a BackupStore.
//
// Phase 1 writes to a local FileStore (handed to the tower out of band); phase 2
// replaces the store with a networked client that ships each backup to a remote
// tower. The poll model is deliberately simple and off the hot path — a backup
// only needs to reflect the latest revoked-beating state, and the highest-nonce
// store semantics make a missed tick self-correcting on the next one.
type BackupAgent struct {
	source   ChannelSource
	store    BackupStore
	interval time.Duration

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewBackupAgent constructs a BackupAgent. A zero interval defaults to 30s.
func NewBackupAgent(source ChannelSource, store BackupStore,
	interval time.Duration) *BackupAgent {

	if interval == 0 {
		interval = 30 * time.Second
	}

	return &BackupAgent{
		source:   source,
		store:    store,
		interval: interval,
		quit:     make(chan struct{}),
	}
}

// Start launches the periodic backup loop.
func (a *BackupAgent) Start() {
	a.wg.Add(1)
	go a.run()
}

// Stop halts the backup loop.
func (a *BackupAgent) Stop() {
	close(a.quit)
	a.wg.Wait()
}

// run polls and persists backups until stopped.
func (a *BackupAgent) run() {
	defer a.wg.Done()

	// Snapshot once at startup so a node that has been offline re-arms its
	// tower immediately, then on every tick.
	a.snapshot()

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.snapshot()
		case <-a.quit:
			return
		}
	}
}

// snapshot writes the current backup for every channel to the store.
func (a *BackupAgent) snapshot() {
	backups, err := a.source.EvmBackups()
	if err != nil {
		log.Warnf("evmtower: backup snapshot: %v", err)

		return
	}

	for _, b := range backups {
		if err := a.store.Put(b); err != nil {
			log.Warnf("evmtower: store backup for %x: %v",
				b.ChannelID, err)
		}
	}
}
