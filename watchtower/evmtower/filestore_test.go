package evmtower

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFileStoreRoundtrip checks persist → reload, highest-nonce-wins, and
// ChannelIDs enumeration across a fresh FileStore instance (simulating the
// tower reading backups handed over on disk).
func TestFileStoreRoundtrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := NewFileStore(dir)
	require.NoError(t, err)

	require.NoError(t, s.Put(testBackup(5)))
	require.NoError(t, s.Put(testBackup(3))) // stale — ignored
	require.NoError(t, s.Put(testBackup(9)))

	// A second store over the same dir (the tower process) reads it back.
	tower, err := NewFileStore(dir)
	require.NoError(t, err)

	got, ok := tower.Get([32]byte{0xab, 0xcd})
	require.True(t, ok)
	require.Equal(t, uint64(9), got.Nonce)
	require.Equal(t, 0, testBackup(9).BalanceA.Cmp(got.BalanceA))
	require.Len(t, got.CounterpartySig, 65)

	ids, err := tower.ChannelIDs()
	require.NoError(t, err)
	require.Equal(t, [][32]byte{{0xab, 0xcd}}, ids)

	// Missing channel.
	_, ok = tower.Get([32]byte{0xff})
	require.False(t, ok)
}
