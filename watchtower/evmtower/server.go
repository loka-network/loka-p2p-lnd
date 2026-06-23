package evmtower

import (
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/brontide"
	"github.com/lightningnetwork/lnd/keychain"
)

// Wire status bytes the tower returns after a backup upload.
const (
	statusOK  byte = 0x00
	statusErr byte = 0x01
)

// Server is the tower side of the networked watchtower protocol (phase 2): it
// accepts brontide (Noise_XK, authenticated + encrypted) connections from
// protected nodes and persists the JusticeBackup each uploads into the tower's
// BackupStore, which the Lookout then acts on. The wire protocol is minimal —
// one framed message per backup (JusticeBackup.Encode()), one status byte in
// reply — because a backup carries no spend authority (penalize always pays the
// victim, H-1), so there is no session/blob-encryption machinery as in the
// Bitcoin wtserver.
type Server struct {
	listener *brontide.Listener
	store    BackupStore

	// allowed is the live set of client identity pubkeys (compressed bytes
	// as map keys) permitted to upload. An empty set means "open" (accept
	// any client). It is mutable at runtime — SetAllowed/Allow/Disallow take
	// effect on the next handshake without restarting the listener, because
	// shouldAccept reads it live under allowMu (audit A-1; runtime reload).
	allowMu sync.RWMutex
	allowed map[string]struct{}

	// allowlistPath, if set via WatchAllowlistFile, is a file the tower
	// hot-reloads the allowlist from; allowlistInterval is the poll period
	// and allowlistMod the last-applied file mtime.
	allowlistPath     string
	allowlistInterval time.Duration
	allowlistMod      time.Time

	wg   sync.WaitGroup
	quit chan struct{}
}

// NewServer starts a brontide listener on listenAddr identified by localKey.
// allowed is the initial client identity-pubkey allowlist; empty/nil means
// accept any client (open tower). The set can be changed at runtime via
// SetAllowed/Allow/Disallow.
func NewServer(localKey keychain.SingleKeyECDH, listenAddr string,
	store BackupStore, allowed []*btcec.PublicKey) (*Server, error) {

	s := &Server{
		store:   store,
		allowed: make(map[string]struct{}),
		quit:    make(chan struct{}),
	}
	s.SetAllowed(allowed)

	// shouldAccept reads the live allowlist on every handshake, so runtime
	// changes apply without recreating the listener.
	l, err := brontide.NewListener(localKey, listenAddr, s.shouldAccept)
	if err != nil {
		return nil, fmt.Errorf("evmtower: listen: %w", err)
	}
	s.listener = l

	return s, nil
}

// shouldAccept is the brontide accept gate: open when the allowlist is empty,
// else membership. Read live so runtime mutations take effect immediately.
func (s *Server) shouldAccept(pub *btcec.PublicKey) (bool, error) {
	s.allowMu.RLock()
	defer s.allowMu.RUnlock()

	if len(s.allowed) == 0 {
		return true, nil
	}
	_, ok := s.allowed[string(pub.SerializeCompressed())]

	return ok, nil
}

// SetAllowed replaces the allowlist. An empty list reopens the tower to all
// clients (logged, since that is a notable posture change).
func (s *Server) SetAllowed(pubs []*btcec.PublicKey) {
	s.allowMu.Lock()
	defer s.allowMu.Unlock()

	s.allowed = make(map[string]struct{}, len(pubs))
	for _, p := range pubs {
		s.allowed[string(p.SerializeCompressed())] = struct{}{}
	}
	if len(s.allowed) == 0 {
		log.Warnf("evmtower: allowlist empty — accepting ALL clients")
	}
}

// Allow adds one client to the allowlist (switching from open to restricted if
// the list was empty).
func (s *Server) Allow(pub *btcec.PublicKey) {
	s.allowMu.Lock()
	defer s.allowMu.Unlock()
	s.allowed[string(pub.SerializeCompressed())] = struct{}{}
}

// Disallow removes one client. If it was the last entry the tower reopens to
// all (empty == open); that transition is logged.
func (s *Server) Disallow(pub *btcec.PublicKey) {
	s.allowMu.Lock()
	defer s.allowMu.Unlock()
	delete(s.allowed, string(pub.SerializeCompressed()))
	if len(s.allowed) == 0 {
		log.Warnf("evmtower: allowlist now empty — accepting ALL clients")
	}
}

// Allowed returns the current allowlist as compressed-pubkey hex strings.
func (s *Server) Allowed() []string {
	s.allowMu.RLock()
	defer s.allowMu.RUnlock()

	out := make([]string, 0, len(s.allowed))
	for k := range s.allowed {
		out = append(out, hex.EncodeToString([]byte(k)))
	}

	return out
}

// Addr returns the listener's network address (useful when listening on :0).
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Start begins accepting connections (and hot-reloading the allowlist file if
// one was configured via WatchAllowlistFile).
func (s *Server) Start() {
	s.wg.Add(1)
	go s.accept()

	if s.allowlistPath != "" {
		s.wg.Add(1)
		go s.watchAllowlist()
	}
}

// Stop closes the listener and waits for handlers to drain.
func (s *Server) Stop() {
	close(s.quit)
	_ = s.listener.Close()
	s.wg.Wait()
}

func (s *Server) accept() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return
			default:
				// A per-connection handshake failure (e.g. a
				// rejected/non-allowlisted client) is surfaced
				// here as an error; log it and keep accepting
				// rather than killing the listener.
				log.Warnf("evmtower: accept: %v", err)

				continue
			}
		}

		s.wg.Add(1)
		go s.handle(conn)
	}
}

// handle reads framed backup uploads from one client connection until it closes.
func (s *Server) handle(conn net.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()

	bconn, ok := conn.(*brontide.Conn)
	if !ok {
		return
	}

	for {
		msg, err := bconn.ReadNextMessage()
		if err != nil {
			return
		}

		status := statusOK
		b, err := DecodeJusticeBackup(msg)
		if err != nil {
			log.Warnf("evmtower: bad backup from %s: %v",
				conn.RemoteAddr(), err)
			status = statusErr
		} else if err := s.store.Put(b); err != nil {
			log.Warnf("evmtower: store backup from %s: %v",
				conn.RemoteAddr(), err)
			status = statusErr
		} else {
			log.Debugf("evmtower: stored backup ChannelID(%x) "+
				"nonce %d from %s", b.ChannelID, b.Nonce,
				conn.RemoteAddr())
		}

		if err := bconn.WriteMessage([]byte{status}); err != nil {
			return
		}
		if _, err := bconn.Flush(); err != nil {
			return
		}
	}
}
