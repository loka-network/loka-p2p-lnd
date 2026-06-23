package evmtower

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/stretchr/testify/require"
)

func hexPub(k *btcec.PrivateKey) string {
	return hex.EncodeToString(k.PubKey().SerializeCompressed())
}

// TestAllowlistFileHotReload checks the tower loads the allowlist file at
// startup and applies edits live (no restart), parsing one hex pubkey per line.
func TestAllowlistFileHotReload(t *testing.T) {
	t.Parallel()

	c1, _ := btcec.NewPrivateKey()
	c2, _ := btcec.NewPrivateKey()

	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.txt")
	// Start with only c1 allowed.
	require.NoError(t, os.WriteFile(path,
		[]byte("# clients\n"+hexPub(c1)+"\n"), 0600))

	towerPriv, _ := btcec.NewPrivateKey()
	srv, err := NewServer(
		&keychain.PrivKeyECDH{PrivKey: towerPriv}, "127.0.0.1:0",
		NewMemStore(), nil,
	)
	require.NoError(t, err)
	srv.WatchAllowlistFile(path, 200*time.Millisecond)
	srv.Start()
	defer srv.Stop()

	// After startup the file is applied: c1 allowed, c2 not.
	require.Eventually(t, func() bool {
		ok1, _ := srv.shouldAccept(c1.PubKey())
		ok2, _ := srv.shouldAccept(c2.PubKey())

		return ok1 && !ok2
	}, 2*time.Second, 50*time.Millisecond)

	// Edit the file to add c2 → must take effect live, no restart.
	require.NoError(t, os.WriteFile(path,
		[]byte(hexPub(c1)+"\n"+hexPub(c2)+"\n"), 0600))
	require.Eventually(t, func() bool {
		ok2, _ := srv.shouldAccept(c2.PubKey())

		return ok2
	}, 2*time.Second, 50*time.Millisecond,
		"editing the allowlist file must add the client without restart")

	// A malformed edit is rejected; the previous (valid) set is kept.
	require.NoError(t, os.WriteFile(path, []byte("not-a-pubkey\n"), 0600))
	time.Sleep(500 * time.Millisecond)
	ok1, _ := srv.shouldAccept(c1.PubKey())
	require.True(t, ok1, "a malformed file edit must not drop the live set")
}
