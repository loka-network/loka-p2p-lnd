package evmtower

import (
	"fmt"
	"net"
	"time"

	"github.com/lightningnetwork/lnd/brontide"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/tor"
)

// RemoteStore is a BackupStore that ships each backup to a remote tower over
// brontide instead of writing it locally. Because it satisfies BackupStore, it
// drops straight into BackupAgent: pointing the agent at a RemoteStore turns
// local snapshots into networked uploads — the phase-2 transport — with no
// change to the agent. Get is unsupported (the tower, not the client, holds the
// backups for acting on).
type RemoteStore struct {
	localKey keychain.SingleKeyECDH
	tower    *lnwire.NetAddress
	timeout  time.Duration
	dialer   tor.DialFunc
}

// NewRemoteStore returns a RemoteStore that uploads to tower, authenticating as
// localKey. A nil dialer uses net.DialTimeout; a zero timeout defaults to 10s.
func NewRemoteStore(localKey keychain.SingleKeyECDH, tower *lnwire.NetAddress,
	timeout time.Duration, dialer tor.DialFunc) *RemoteStore {

	if dialer == nil {
		dialer = func(network, addr string, t time.Duration) (net.Conn,
			error) {

			return net.DialTimeout(network, addr, t)
		}
	}
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	return &RemoteStore{
		localKey: localKey,
		tower:    tower,
		timeout:  timeout,
		dialer:   dialer,
	}
}

// Put uploads the backup to the tower and waits for its ack. A fresh connection
// per call keeps the client stateless; the tower's highest-nonce-wins store
// makes a re-sent backup harmless.
func (r *RemoteStore) Put(b *JusticeBackup) error {
	enc, err := b.Encode()
	if err != nil {
		return err
	}

	conn, err := brontide.Dial(r.localKey, r.tower, r.timeout, r.dialer)
	if err != nil {
		return fmt.Errorf("evmtower: dial tower: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.WriteMessage(enc); err != nil {
		return err
	}
	if _, err := conn.Flush(); err != nil {
		return err
	}

	ack, err := conn.ReadNextMessage()
	if err != nil {
		return fmt.Errorf("evmtower: tower ack: %w", err)
	}
	if len(ack) != 1 || ack[0] != statusOK {
		return fmt.Errorf("evmtower: tower rejected backup for %x",
			b.ChannelID)
	}

	return nil
}

// Get is unsupported on the client; the tower holds backups for acting on.
func (r *RemoteStore) Get(_ [32]byte) (*JusticeBackup, bool) {
	return nil, false
}
