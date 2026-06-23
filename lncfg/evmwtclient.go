package lncfg

// EvmWtClient configures the client side of the EVM watchtower: the protected
// node periodically snapshots its latest co-signed channel state as a
// JusticeBackup so a tower can penalize a revoked-state force-close while the
// node is offline. The EVM analogue of --wtclient.active.
//
//nolint:ll
type EvmWtClient struct {
	// Active enables periodic backup snapshots. Requires --evm.active.
	Active bool `long:"active" description:"Snapshot the latest co-signed EVM channel state for watchtower backup."`

	// BackupDir is where backups are written (phase-1 local handover to a
	// tower). Defaults to <lnddir>/evm-justice when empty.
	BackupDir string `long:"backupdir" description:"Directory to write per-channel EVM JusticeBackup files to."`

	// Interval is how often to snapshot, e.g. "30s". Empty uses the
	// package default.
	Interval string `long:"interval" description:"How often to snapshot channel state for backup (e.g. 30s)."`

	// Tower is the remote tower to ship backups to, as <pubkey>@<host:port>.
	// Empty keeps backups local (written to BackupDir); set it to upload
	// over brontide to a networked tower instead.
	Tower string `long:"tower" description:"Remote EVM watchtower to upload backups to, as <pubkey>@<host:port>."`
}

// DefaultEvmWtClient returns the default (disabled) EVM watchtower client config.
func DefaultEvmWtClient() *EvmWtClient {
	return &EvmWtClient{Active: false}
}
