package lncfg

// EvmWatchtower holds the configuration for running an EVM watchtower
// (1-refactor-docs/evm/evm-watchtower-design.md). When Active, an EVM-backend
// node also runs an evmtower.Lookout that watches its ChannelManager for
// revoked-state force-closes and submits penalize on behalf of the channels it
// holds backups for — the EVM analogue of `--watchtower.active`.
//
//nolint:ll
type EvmWatchtower struct {
	// Active turns the EVM watchtower on. Requires the EVM chain backend
	// (--evm.active); ignored otherwise.
	Active bool `long:"active" description:"Run an EVM watchtower that penalizes revoked-state force-closes for backed-up channels."`

	// BackupDir is the directory the tower reads JusticeBackup files from.
	// In phase 1 backups are handed over locally (a file per channel);
	// phase 2 adds a networked client→tower protocol. Defaults to
	// <lnddir>/evm-justice when empty.
	BackupDir string `long:"backupdir" description:"Directory of per-channel EVM JusticeBackup files the tower acts on."`

	// PollInterval is how often the tower scans for close events, e.g.
	// "5s". Empty uses the package default.
	PollInterval string `long:"pollinterval" description:"How often to scan the chain for force-close events (e.g. 5s)."`
}

// DefaultEvmWatchtower returns the default (disabled) EVM watchtower config.
func DefaultEvmWatchtower() *EvmWatchtower {
	return &EvmWatchtower{
		Active: false,
	}
}
