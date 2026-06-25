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

// countingStore wraps a BackupStore and counts Put calls.
type countingStore struct {
	BackupStore
	puts int
}

func (c *countingStore) Put(b *JusticeBackup) error {
	c.puts++

	return c.BackupStore.Put(b)
}

// TestBackupAgentSendOnChange checks the agent only uploads when a channel's
// backup nonce advances — an unchanged backup on a later tick is a no-op.
func TestBackupAgentSendOnChange(t *testing.T) {
	t.Parallel()

	src := &stubSource{backups: []*JusticeBackup{testBackup(5)}}
	cs := &countingStore{BackupStore: NewMemStore()}
	agent := NewBackupAgent(src, cs, 0)

	agent.snapshot() // nonce 5: first push
	agent.snapshot() // nonce 5 again: skipped
	require.Equal(t, 1, cs.puts, "an unchanged backup must not be re-sent")

	src.backups = []*JusticeBackup{testBackup(6)} // advanced
	agent.snapshot()
	require.Equal(t, 2, cs.puts, "an advanced backup is sent")
}

var errStub = stubErr("boom")

type stubErr string

func (e stubErr) Error() string { return string(e) }
