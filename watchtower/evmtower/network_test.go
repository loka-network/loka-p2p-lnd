package evmtower

import (
	"net"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/stretchr/testify/require"
)

// TestNetworkedBackup drives the phase-2 transport over a real brontide
// loopback: a client RemoteStore uploads a backup to a tower Server, which
// persists it into the store the Lookout would act on.
func TestNetworkedBackup(t *testing.T) {
	t.Parallel()

	towerPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	towerKey := &keychain.PrivKeyECDH{PrivKey: towerPriv}

	store := NewMemStore()
	srv, err := NewServer(towerKey, "127.0.0.1:0", store, nil)
	require.NoError(t, err)
	srv.Start()
	defer srv.Stop()

	clientPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	clientKey := &keychain.PrivKeyECDH{PrivKey: clientPriv}

	tower := &lnwire.NetAddress{
		IdentityKey: towerPriv.PubKey(),
		Address:     srv.Addr().(*net.TCPAddr),
	}
	remote := NewRemoteStore(clientKey, tower, 5*time.Second, nil)

	// Upload a backup; the tower should persist it.
	require.NoError(t, remote.Put(testBackup(7)))

	require.Eventually(t, func() bool {
		b, ok := store.Get([32]byte{0xab, 0xcd})

		return ok && b.Nonce == 7
	}, 3*time.Second, 20*time.Millisecond)

	// A second, higher-nonce upload supersedes; a stale one is ignored by
	// the tower's highest-nonce-wins store.
	require.NoError(t, remote.Put(testBackup(11)))
	require.NoError(t, remote.Put(testBackup(9)))

	require.Eventually(t, func() bool {
		b, ok := store.Get([32]byte{0xab, 0xcd})

		return ok && b.Nonce == 11
	}, 3*time.Second, 20*time.Millisecond)
}

// TestBackupAgentOverNetwork wires the existing BackupAgent to a RemoteStore,
// proving the client agent ships snapshots to a remote tower unchanged.
func TestBackupAgentOverNetwork(t *testing.T) {
	t.Parallel()

	towerPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	towerKey := &keychain.PrivKeyECDH{PrivKey: towerPriv}

	store := NewMemStore()
	srv, err := NewServer(towerKey, "127.0.0.1:0", store, nil)
	require.NoError(t, err)
	srv.Start()
	defer srv.Stop()

	clientPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	clientKey := &keychain.PrivKeyECDH{PrivKey: clientPriv}
	tower := &lnwire.NetAddress{
		IdentityKey: towerPriv.PubKey(),
		Address:     srv.Addr().(*net.TCPAddr),
	}

	remote := NewRemoteStore(clientKey, tower, 5*time.Second, nil)
	agent := NewBackupAgent(
		&stubSource{backups: []*JusticeBackup{testBackup(4)}},
		remote, 0,
	)
	agent.snapshot() // one snapshot ships over the wire

	require.Eventually(t, func() bool {
		b, ok := store.Get([32]byte{0xab, 0xcd})

		return ok && b.Nonce == 4
	}, 3*time.Second, 20*time.Millisecond)
}
