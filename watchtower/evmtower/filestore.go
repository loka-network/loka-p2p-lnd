package evmtower

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileStore is a BackupStore backed by one file per channel under a directory:
// <dir>/<channelID-hex>.backup, holding the encoded JusticeBackup. It is the
// phase-1 "local handover" transport — a protected node writes its latest
// backup file and the tower reads from the same (copied) directory. Phase 2
// replaces this with a networked client→tower protocol; the BackupStore
// interface stays the same.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore returns a FileStore rooted at dir, creating it if needed.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("evmtower: mkdir %q: %w", dir, err)
	}

	return &FileStore{dir: dir}, nil
}

// path returns the backup file path for a channel.
func (f *FileStore) path(channelID [32]byte) string {
	return filepath.Join(f.dir, hex.EncodeToString(channelID[:])+".backup")
}

// Put writes the backup, keeping only the highest-nonce one per channel
// (mirrors memStore semantics so a stale re-delivery can't regress defense).
func (f *FileStore) Put(b *JusticeBackup) error {
	if err := b.Validate(); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if cur, ok := f.read(b.ChannelID); ok && b.Nonce <= cur.Nonce {
		return nil
	}

	enc, err := b.Encode()
	if err != nil {
		return err
	}
	// Write-then-rename for atomicity against a concurrent tower read.
	tmp := f.path(b.ChannelID) + ".tmp"
	if err := os.WriteFile(tmp, enc, 0600); err != nil {
		return fmt.Errorf("evmtower: write backup: %w", err)
	}

	return os.Rename(tmp, f.path(b.ChannelID))
}

// Get returns the stored backup for channelID, or (nil, false).
func (f *FileStore) Get(channelID [32]byte) (*JusticeBackup, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.read(channelID)
}

// read loads and decodes a backup file (caller holds mu).
func (f *FileStore) read(channelID [32]byte) (*JusticeBackup, bool) {
	data, err := os.ReadFile(f.path(channelID))
	if err != nil {
		return nil, false
	}
	b, err := DecodeJusticeBackup(data)
	if err != nil {
		log.Warnf("evmtower: corrupt backup %x: %v", channelID, err)

		return nil, false
	}

	return b, true
}

// ChannelIDs lists the channels with a stored backup, so the lookout can scan
// per-channel close events when filtering by topic.
func (f *FileStore) ChannelIDs() ([][32]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}

	var ids [][32]byte
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".backup" {
			continue
		}
		raw, err := hex.DecodeString(name[:len(name)-len(".backup")])
		if err != nil || len(raw) != 32 {
			continue
		}
		var id [32]byte
		copy(id[:], raw)
		ids = append(ids, id)
	}

	return ids, nil
}
