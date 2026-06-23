package evmtower

import "sync"

// BackupStore persists the latest JusticeBackup per channel for the tower. Only
// the newest state need be retained: penalize requires a nonce strictly greater
// than the broadcast one, so the highest-nonce state defends against every
// older revoked one. Phase 1 ships an in-memory store; a bbolt-backed
// implementation (restart-durable) is a drop-in behind this interface for the
// networked tower in Phase 2.
type BackupStore interface {
	// Put stores (or overwrites with) the latest backup for its channel. A
	// backup with a nonce not greater than the stored one is ignored, so
	// out-of-order delivery never regresses the tower's defense.
	Put(b *JusticeBackup) error

	// Get returns the stored backup for channelID, or (nil, false).
	Get(channelID [32]byte) (*JusticeBackup, bool)
}

// memStore is an in-memory BackupStore.
type memStore struct {
	mu      sync.RWMutex
	backups map[[32]byte]*JusticeBackup
}

// NewMemStore returns an in-memory BackupStore.
func NewMemStore() BackupStore {
	return &memStore{backups: make(map[[32]byte]*JusticeBackup)}
}

// Put implements BackupStore; it keeps only the highest-nonce backup per
// channel.
func (m *memStore) Put(b *JusticeBackup) error {
	if err := b.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if cur, ok := m.backups[b.ChannelID]; ok && b.Nonce <= cur.Nonce {
		// Not newer — keep the stronger (higher-nonce) defense.
		return nil
	}
	m.backups[b.ChannelID] = b

	return nil
}

// Get implements BackupStore.
func (m *memStore) Get(channelID [32]byte) (*JusticeBackup, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	b, ok := m.backups[channelID]

	return b, ok
}
