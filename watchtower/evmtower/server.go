package evmtower

import (
	"fmt"
	"net"
	"sync"

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

	wg   sync.WaitGroup
	quit chan struct{}
}

// NewServer starts a brontide listener on listenAddr identified by localKey.
// shouldAccept gates which client identity keys may connect (nil accepts all).
func NewServer(localKey keychain.SingleKeyECDH, listenAddr string,
	store BackupStore,
	shouldAccept func(*btcec.PublicKey) (bool, error)) (*Server, error) {

	if shouldAccept == nil {
		shouldAccept = func(*btcec.PublicKey) (bool, error) {
			return true, nil
		}
	}
	l, err := brontide.NewListener(localKey, listenAddr, shouldAccept)
	if err != nil {
		return nil, fmt.Errorf("evmtower: listen: %w", err)
	}

	return &Server{
		listener: l,
		store:    store,
		quit:     make(chan struct{}),
	}, nil
}

// Addr returns the listener's network address (useful when listening on :0).
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Start begins accepting connections.
func (s *Server) Start() {
	s.wg.Add(1)
	go s.accept()
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
			default:
				log.Warnf("evmtower: accept: %v", err)
			}

			return
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
