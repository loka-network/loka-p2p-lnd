package lncfg

import (
	"fmt"
	"net"
)

const (
	// DefaultSuiRPCHost is the default host for the Sui RPC endpoint.
	DefaultSuiRPCHost = "localhost"

	// DefaultSuiRPCPort is the default RPC port exposed by the Sui
	// node.
	DefaultSuiRPCPort = 9000

	// DefaultSuiEpochInterval is the default expected interval between
	// consecutive Sui epochs, expressed in milliseconds.
	DefaultSuiEpochInterval = 500

	// DefaultSuiNumConfs is the default number of confirmations
	// required before treating a Channel Object as confirmed.
	DefaultSuiNumConfs = 1

	// DefaultSuiCSVDelay is the default relative time delay (in epochs)
	// applied to to_local outputs when force-closing a channel.
	DefaultSuiCSVDelay = 144
)

// SuiNode holds configuration options for the LND node's connection to the
// Sui network backend.
//
//nolint:ll
type SuiNode struct {
	// Active enables the Sui chain backend.  Must be set to true via
	// --suinode.active to activate Sui; all other Sui flags are ignored
	// when this is false.  Defaults to false so that existing Bitcoin-only
	// deployments are not affected.
	Active bool `long:"active" description:"Enable the Sui chain backend. Must be true to use Sui instead of Bitcoin."`

	// MainNet specifies that the node should connect to the Sui mainnet.
	MainNet bool `long:"mainnet" description:"Use the Sui mainnet"`

	// TestNet specifies that the node should connect to the Sui public
	// testnet.
	TestNet bool `long:"testnet" description:"Use the Sui testnet"`

	// DevNet specifies that the node should connect to a local Sui devnet
	// (equivalent to Bitcoin's regtest). This is the default when no
	// network flag is given.
	DevNet bool `long:"devnet" description:"Use a local Sui devnet (default if no network flag is given)"`

	// SimNet specifies that the node should connect to the Sui simulation
	// network, suitable for in-process or unit-test environments.
	SimNet bool `long:"simnet" description:"Use the Sui simulation network"`

	// RPCHost is the host (and optional port) of the Sui node's
	// RPC endpoint.  Example: "localhost:9000" or "sui.example.com:9000".
	RPCHost string `long:"rpchost" description:"The host:port of the Sui node's RPC endpoint"`

	// PackageID is the Sui package ID where the lightning Move module is
	// deployed.
	PackageID string `long:"packageid" description:"The Sui package ID where the lightning move module is deployed"`

	// TLSCertPath is the path to the TLS certificate used to authenticate
	// the connection to the Sui node. Leave empty for insecure
	// connections (development only).
	TLSCertPath string `long:"tlscertpath" description:"Path to the TLS certificate for the Sui node. Leave empty for insecure plaintext connections (dev only)."`

	// MacaroonPath is an optional path to a macaroon file for
	// authenticating RPC calls to the Sui node.
	MacaroonPath string `long:"macaroonpath" description:"Path to the macaroon file for authenticating Sui RPC calls (optional)"`

	// EpochInterval is the expected time in milliseconds between Sui
	// epochs. This is used to calibrate timeout heuristics.
	EpochInterval int `long:"epochinterval" description:"Expected milliseconds between Sui epochs (used for timeout heuristics)"`

	// NumConfs is the number of Sui confirmations required before
	// treating an event as confirmed.
	NumConfs uint32 `long:"numconfs" description:"Number of Sui confirmations required for confirmation (default: 1)"`

	// CSVDelay is the default relative time delay in epochs applied to
	// to_local outputs during a force-close. Analogous to OP_CSV in
	// Bitcoin.
	CSVDelay uint16 `long:"csvdelay" description:"Default to_local epoch delay for force-close outputs (analogous to Bitcoin CSV delay)"`
}

// DefaultSuiNode returns a SuiNode configuration populated with sensible
// defaults.
func DefaultSuiNode() *SuiNode {
	return &SuiNode{
		RPCHost:       fmt.Sprintf("%s:%d", DefaultSuiRPCHost, DefaultSuiRPCPort),
		EpochInterval: DefaultSuiEpochInterval,
		NumConfs:      DefaultSuiNumConfs,
		CSVDelay:      DefaultSuiCSVDelay,
	}
}

// Validate checks that the SuiNode configuration is internally consistent.
func (s *SuiNode) Validate() error {
	nets := 0
	if s.MainNet {
		nets++
	}
	if s.TestNet {
		nets++
	}
	if s.DevNet {
		nets++
	}
	if s.SimNet {
		nets++
	}
	if nets > 1 {
		return fmt.Errorf("only one of --suinode.mainnet, " +
			"--suinode.testnet, --suinode.devnet or " +
			"--suinode.simnet may be specified")
	}

	if s.RPCHost == "" {
		return fmt.Errorf("suinode.rpchost must not be empty")
	}

	host, _, err := net.SplitHostPort(s.RPCHost)
	if err != nil {
		// Treat bare hostname (no port) as valid; callers are expected
		// to append the default port when dialling.
		host = s.RPCHost
	}

	if host == "" {
		return fmt.Errorf("suinode.rpchost has an empty host component")
	}

	if s.EpochInterval <= 0 {
		return fmt.Errorf("suinode.epochinterval must be a positive value")
	}

	if s.NumConfs == 0 {
		return fmt.Errorf("suinode.numconfs must be at least 1")
	}

	return nil
}

// RPCAddr returns the network address (host:port) for dialling the Sui
// node RPC endpoint.  If RPCHost already contains a port the value is
// returned verbatim; otherwise the default port is appended.
func (s *SuiNode) RPCAddr() string {
	_, _, err := net.SplitHostPort(s.RPCHost)
	if err != nil {
		// No port present – append default.
		return fmt.Sprintf("%s:%d", s.RPCHost, DefaultSuiRPCPort)
	}

	return s.RPCHost
}
