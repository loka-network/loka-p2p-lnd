package lnd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/channeldb"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/evmwallet"
	"github.com/lightningnetwork/lnd/watchtower/evmtower"
)

// newEvmLookout constructs the EVM watchtower Lookout when --evmwatchtower.active
// is set on an EVM-backend node. It returns (nil, nil) when the tower is
// disabled so the caller can no-op. See 1-refactor-docs/evm/evm-watchtower-design.md.
func newEvmLookout(cfg *Config, cc *chainreg.ChainControl) (*evmtower.Lookout,
	error) {

	if cfg.EvmWatchtower == nil || !cfg.EvmWatchtower.Active {
		return nil, nil
	}
	if cfg.EvmMode == nil || !cfg.EvmMode.Active {
		return nil, fmt.Errorf("evmwatchtower requires the EVM chain " +
			"backend (--evm.active)")
	}

	client, ok := cc.EvmClient.(evmnotify.EvmClient)
	if !ok {
		return nil, fmt.Errorf("evmwatchtower: chain control exposes "+
			"no EVM client (%T)", cc.EvmClient)
	}
	evmWallet, ok := cc.Wallet.WalletController.(*evmwallet.Wallet)
	if !ok {
		return nil, fmt.Errorf("evmwatchtower: wallet is %T, not "+
			"*evmwallet.Wallet", cc.Wallet.WalletController)
	}

	// The tower signs penalize with the node's EVM key. It is only a
	// relayer/gas payer — the contract pays the broadcaster-derived victim
	// regardless of msg.sender (H-1) — so this key never needs a stake in
	// any channel it defends.
	relayKey, _, err := evmWallet.NodeECDSAKey()
	if err != nil {
		return nil, fmt.Errorf("evmwatchtower: derive relay key: %w", err)
	}

	backupDir := cfg.EvmWatchtower.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(cfg.LndDir, "evm-justice")
	}
	store, err := evmtower.NewFileStore(backupDir)
	if err != nil {
		return nil, err
	}

	var pollInterval time.Duration
	if cfg.EvmWatchtower.PollInterval != "" {
		pollInterval, err = time.ParseDuration(
			cfg.EvmWatchtower.PollInterval,
		)
		if err != nil {
			return nil, fmt.Errorf("evmwatchtower: bad pollinterval "+
				"%q: %w", cfg.EvmWatchtower.PollInterval, err)
		}
	}

	contract := common.HexToAddress(cfg.EvmMode.ContractAddress)

	ltndLog.Infof("Starting EVM watchtower: contract=%s backupdir=%s",
		contract, backupDir)

	return evmtower.NewLookout(evmtower.Config{
		Client:   client,
		Contract: contract,
		Store:    store,
		Penalizer: &evmtower.EvmPenalizer{
			Client:   client,
			Contract: contract,
			Key:      relayKey,
		},
		PollInterval: pollInterval,
		// FromBlock 0 scans from genesis; fine on anvil/short chains.
		// Production hardening (Phase 2): bound the scan to a sliding
		// window like evmnotify.logFromBlock so a range-capped public
		// RPC doesn't reject the query.
		FromBlock: 0,
	}), nil
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
func newEvmBackupAgent(cfg *Config,
	fetchOpen func() ([]*channeldb.OpenChannel, error)) (
	*evmtower.BackupAgent, error) {

	if cfg.EvmWtClient == nil || !cfg.EvmWtClient.Active {
		return nil, nil
	}
	if cfg.EvmMode == nil || !cfg.EvmMode.Active {
		return nil, fmt.Errorf("evmwtclient requires the EVM chain " +
			"backend (--evm.active)")
	}

	backupDir := cfg.EvmWtClient.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(cfg.LndDir, "evm-justice")
	}
	store, err := evmtower.NewFileStore(backupDir)
	if err != nil {
		return nil, err
	}

	var interval time.Duration
	if cfg.EvmWtClient.Interval != "" {
		interval, err = time.ParseDuration(cfg.EvmWtClient.Interval)
		if err != nil {
			return nil, fmt.Errorf("evmwtclient: bad interval %q: %w",
				cfg.EvmWtClient.Interval, err)
		}
	}

	ltndLog.Infof("Starting EVM watchtower client: backupdir=%s", backupDir)

	source := &evmChannelSource{fetchOpen: fetchOpen}

	return evmtower.NewBackupAgent(source, store, interval), nil
}
