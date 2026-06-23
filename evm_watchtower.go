package lnd

import (
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/channeldb"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lncfg"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/evmwallet"
	"github.com/lightningnetwork/lnd/watchtower/evmtower"
)

// evmClientAllowlist parses a list of hex client identity pubkeys into the
// public keys the tower's Server allowlist is seeded with. An empty list yields
// nil → an open tower; a non-empty list restricts uploads (audit A-1).
func evmClientAllowlist(hexKeys []string) ([]*btcec.PublicKey, error) {
	if len(hexKeys) == 0 {
		return nil, nil
	}

	keys := make([]*btcec.PublicKey, 0, len(hexKeys))
	for _, h := range hexKeys {
		raw, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("evmwatchtower: bad allowed "+
				"client pubkey %q: %w", h, err)
		}
		pk, err := btcec.ParsePubKey(raw)
		if err != nil {
			return nil, fmt.Errorf("evmwatchtower: bad allowed "+
				"client pubkey %q: %w", h, err)
		}
		keys = append(keys, pk)
	}

	return keys, nil
}

// newEvmLookout constructs the EVM watchtower Lookout when --evmwatchtower.active
// is set on an EVM-backend node. It returns (nil, nil) when the tower is
// disabled so the caller can no-op. See 1-refactor-docs/evm/evm-watchtower-design.md.
func newEvmLookout(cfg *Config, cc *chainreg.ChainControl,
	nodeKey keychain.SingleKeyECDH) (*evmtower.Lookout, *evmtower.Server,
	error) {

	if cfg.EvmWatchtower == nil || !cfg.EvmWatchtower.Active {
		return nil, nil, nil
	}
	if cfg.EvmMode == nil || !cfg.EvmMode.Active {
		return nil, nil, fmt.Errorf("evmwatchtower requires the EVM " +
			"chain backend (--evm.active)")
	}

	client, ok := cc.EvmClient.(evmnotify.EvmClient)
	if !ok {
		return nil, nil, fmt.Errorf("evmwatchtower: chain control "+
			"exposes no EVM client (%T)", cc.EvmClient)
	}
	evmWallet, ok := cc.Wallet.WalletController.(*evmwallet.Wallet)
	if !ok {
		return nil, nil, fmt.Errorf("evmwatchtower: wallet is %T, not "+
			"*evmwallet.Wallet", cc.Wallet.WalletController)
	}

	// The tower signs penalize with the node's EVM key. It is only a
	// relayer/gas payer — the contract pays the broadcaster-derived victim
	// regardless of msg.sender (H-1) — so this key never needs a stake in
	// any channel it defends.
	relayKey, _, err := evmWallet.NodeECDSAKey()
	if err != nil {
		return nil, nil, fmt.Errorf("evmwatchtower: derive relay key: "+
			"%w", err)
	}

	backupDir := cfg.EvmWatchtower.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(cfg.LndDir, "evm-justice")
	}
	store, err := evmtower.NewFileStore(backupDir)
	if err != nil {
		return nil, nil, err
	}

	var pollInterval time.Duration
	if cfg.EvmWatchtower.PollInterval != "" {
		pollInterval, err = time.ParseDuration(
			cfg.EvmWatchtower.PollInterval,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("evmwatchtower: bad "+
				"pollinterval %q: %w",
				cfg.EvmWatchtower.PollInterval, err)
		}
	}

	contract := common.HexToAddress(cfg.EvmMode.ContractAddress)

	ltndLog.Infof("Starting EVM watchtower: contract=%s backupdir=%s",
		contract, backupDir)

	lookout := evmtower.NewLookout(evmtower.Config{
		Client:   client,
		Contract: contract,
		Store:    store,
		Penalizer: &evmtower.EvmPenalizer{
			Client:   client,
			Contract: contract,
			Key:      relayKey,
		},
		PollInterval: pollInterval,
		// Bounded sliding-window scan so a range-capped public RPC
		// doesn't reject the eth_getLogs query; FromBlock (e.g. the
		// deploy block) lets the tower catch closes predating startup.
		WindowSize: cfg.EvmWatchtower.ScanWindow,
		FromBlock:  cfg.EvmWatchtower.FromBlock,
	})

	// Optionally accept networked backup uploads (phase 2), persisting them
	// into the same store the lookout acts on.
	var server *evmtower.Server
	if cfg.EvmWatchtower.Listen != "" {
		allowed, err := evmClientAllowlist(
			cfg.EvmWatchtower.AllowedClients,
		)
		if err != nil {
			return nil, nil, err
		}
		server, err = evmtower.NewServer(
			nodeKey, cfg.EvmWatchtower.Listen, store, allowed,
		)
		if err != nil {
			return nil, nil, err
		}
		ltndLog.Infof("EVM watchtower listening for backups on %s "+
			"(allowed clients: %d, 0=open)",
			cfg.EvmWatchtower.Listen,
			len(cfg.EvmWatchtower.AllowedClients))
	}

	return lookout, server, nil
}

// evmChannelSource builds JusticeBackups from this node's open channels. It
// implements evmtower.ChannelSource. Because a node runs a single chain
// backend, when EVM is active every open channel is an EVM channel.
type evmChannelSource struct {
	fetchOpen func() ([]*channeldb.OpenChannel, error)
}

// EvmBackups implements evmtower.ChannelSource.
func (s *evmChannelSource) EvmBackups() ([]*evmtower.JusticeBackup, error) {
	chans, err := s.fetchOpen()
	if err != nil {
		return nil, err
	}

	backups := make([]*evmtower.JusticeBackup, 0, len(chans))
	for _, c := range chans {
		id, nonce, balA, balB, htlcsHash, sig, err :=
			lnwallet.EvmJusticeBackupFields(c)
		if err != nil {
			// A channel with no retained counterparty signature yet
			// (e.g. freshly funded, no state updates) can't be
			// defended; skip it until it has a co-signed state.
			ltndLog.Debugf("EVM watchtower: skip backup for %v: %v",
				c.FundingOutpoint, err)

			continue
		}
		backups = append(backups, &evmtower.JusticeBackup{
			ChannelID:       id,
			Nonce:           nonce,
			BalanceA:        balA,
			BalanceB:        balB,
			HtlcsHash:       htlcsHash,
			CounterpartySig: sig,
		})
	}

	return backups, nil
}

// newEvmBackupAgent constructs the client-side backup agent when
// --evmwtclient.active is set on an EVM-backend node, else (nil, nil).
func newEvmBackupAgent(cfg *Config, nodeKey keychain.SingleKeyECDH,
	fetchOpen func() ([]*channeldb.OpenChannel, error)) (
	*evmtower.BackupAgent, error) {

	if cfg.EvmWtClient == nil || !cfg.EvmWtClient.Active {
		return nil, nil
	}
	if cfg.EvmMode == nil || !cfg.EvmMode.Active {
		return nil, fmt.Errorf("evmwtclient requires the EVM chain " +
			"backend (--evm.active)")
	}

	var interval time.Duration
	if cfg.EvmWtClient.Interval != "" {
		var err error
		interval, err = time.ParseDuration(cfg.EvmWtClient.Interval)
		if err != nil {
			return nil, fmt.Errorf("evmwtclient: bad interval %q: %w",
				cfg.EvmWtClient.Interval, err)
		}
	}

	// Choose the backup sink: a remote tower over brontide when configured,
	// else a local file directory (phase-1 handover).
	var store evmtower.BackupStore
	if cfg.EvmWtClient.Tower != "" {
		towerAddr, err := lncfg.ParseLNAddressString(
			cfg.EvmWtClient.Tower, "9912", net.ResolveTCPAddr,
		)
		if err != nil {
			return nil, fmt.Errorf("evmwtclient: bad tower %q: %w",
				cfg.EvmWtClient.Tower, err)
		}
		store = evmtower.NewRemoteStore(nodeKey, towerAddr, 0, nil)
		ltndLog.Infof("Starting EVM watchtower client: tower=%s",
			cfg.EvmWtClient.Tower)
	} else {
		backupDir := cfg.EvmWtClient.BackupDir
		if backupDir == "" {
			backupDir = filepath.Join(cfg.LndDir, "evm-justice")
		}
		fs, err := evmtower.NewFileStore(backupDir)
		if err != nil {
			return nil, err
		}
		store = fs
		ltndLog.Infof("Starting EVM watchtower client: backupdir=%s",
			backupDir)
	}

	source := &evmChannelSource{fetchOpen: fetchOpen}

	return evmtower.NewBackupAgent(source, store, interval), nil
}
