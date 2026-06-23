package evmtower

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type stubSource struct {
	backups []*JusticeBackup
	err     error
}

func (s *stubSource) EvmBackups() ([]*JusticeBackup, error) {
	return s.backups, s.err
}

// TestBackupAgentSnapshot checks one snapshot writes every channel's backup to
// the store.
func TestBackupAgentSnapshot(t *testing.T) {
	t.Parallel()

	b1 := testBackup(5)
	b2 := testBackup(8)
	b2.ChannelID = [32]byte{0x11, 0x22}

	store := NewMemStore()
	agent := NewBackupAgent(
		&stubSource{backups: []*JusticeBackup{b1, b2}}, store, 0,
	)

	// Drive a snapshot directly (no goroutine) for determinism.
	agent.snapshot()

	got1, ok := store.Get(b1.ChannelID)
	require.True(t, ok)
	require.Equal(t, uint64(5), got1.Nonce)

	got2, ok := store.Get(b2.ChannelID)
	require.True(t, ok)
	require.Equal(t, uint64(8), got2.Nonce)
}

// TestBackupAgentSourceError checks a source error doesn't write anything.
func TestBackupAgentSourceError(t *testing.T) {
	t.Parallel()

	store := NewMemStore()
	agent := NewBackupAgent(
		&stubSource{err: errStub}, store, 0,
	)
	agent.snapshot()

	_, ok := store.Get([32]byte{0xab, 0xcd})
	require.False(t, ok)
}

var errStub = stubErr("boom")

type stubErr string

func (e stubErr) Error() string { return string(e) }
