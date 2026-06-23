package evmtower

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

// defaultAllowlistReload is how often the allowlist file is checked for changes.
const defaultAllowlistReload = 5 * time.Second

// parseAllowlistFile reads a client allowlist file: one hex identity pubkey per
// line, blank lines and lines starting with '#' ignored. It returns an error on
// the first malformed line, so a typo never silently drops clients (the caller
// keeps the current set instead of applying a partial list).
func parseAllowlistFile(path string) ([]*btcec.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var keys []*btcec.PublicKey
	for i, line := range strings.Split(string(data), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		raw, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		pk, err := btcec.ParsePubKey(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		keys = append(keys, pk)
	}

	return keys, nil
}

// WatchAllowlistFile makes the Server hot-reload its client allowlist from a
// file: edit the file and within reloadInterval the tower applies the change —
// no lnd restart. The file is also the source loaded at startup. A zero
// interval uses defaultAllowlistReload. Call before Start.
func (s *Server) WatchAllowlistFile(path string, reloadInterval time.Duration) {
	s.allowlistPath = path
	if reloadInterval == 0 {
		reloadInterval = defaultAllowlistReload
	}
	s.allowlistInterval = reloadInterval
}

// watchAllowlist polls the allowlist file and applies changes live.
func (s *Server) watchAllowlist() {
	defer s.wg.Done()

	s.reloadAllowlist() // apply the file at startup

	ticker := time.NewTicker(s.allowlistInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.reloadAllowlist()
		case <-s.quit:
			return
		}
	}
}

// reloadAllowlist re-reads the allowlist file when its mtime changed and applies
// it via SetAllowed. A missing file or a parse error leaves the current set
// untouched (logged), so a transient edit can't accidentally open the tower.
func (s *Server) reloadAllowlist() {
	info, err := os.Stat(s.allowlistPath)
	if err != nil {
		// File not present (yet): keep the current (flag-seeded) set.
		return
	}
	if mod := info.ModTime(); mod.Equal(s.allowlistMod) {
		return // unchanged since last load
	} else {
		// Mark seen now so a malformed file isn't re-logged every tick.
		s.allowlistMod = mod
	}

	keys, err := parseAllowlistFile(s.allowlistPath)
	if err != nil {
		log.Errorf("evmtower: allowlist file %s invalid, keeping "+
			"current set: %v", s.allowlistPath, err)

		return
	}

	s.SetAllowed(keys)
	log.Infof("evmtower: reloaded allowlist from %s (%d clients)",
		s.allowlistPath, len(keys))
}
