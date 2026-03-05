package lncfg

import (
	"fmt"
	"net"
)

const (
	// DefaultSetuRPCHost is the default host for the Setu RPC endpoint.
	DefaultSetuRPCHost = "localhost"

	// DefaultSetuRPCPort is the default gRPC port exposed by the Setu
	// validator node.
	DefaultSetuRPCPort = 9000

	// DefaultSetuEpochInterval is the default expected interval between
	// consecutive Setu epochs, expressed in milliseconds. Setu DAG
	// finality is sub-second in normal conditions.
	DefaultSetuEpochInterval = 500

	// DefaultSetuNumConfs is the default number of confirmations (i.e.
	// Setu anchor finalizations) required before treating a Channel Object
	// as confirmed. DAG finality is effectively immediate, so 1 is
	// sufficient.
	DefaultSetuNumConfs = 1

	// DefaultSetuCSVDelay is the default relative time delay (in VLC
	// ticks) applied to to_local outputs when force-closing a channel.
	// This gives the remote party a window to broadcast a breach remedy.
	DefaultSetuCSVDelay = 144
)

// SetuNode holds configuration options for the LND node's connection to the
// Setu DAG network backend.
//
//nolint:ll
type SetuNode struct {
	// Active enables the Setu DAG chain backend.  Must be set to true via
	// --setunode.active to activate Setu; all other Setu flags are ignored
	// when this is false.  Defaults to false so that existing Bitcoin-only
	// deployments are not affected.
	Active bool `long:"active" description:"Enable the Setu DAG chain backend. Must be true to use Setu instead of Bitcoin."`

	// RPCHost is the host (and optional port) of the Setu validator node's
	// gRPC endpoint.  Example: "localhost:9000" or "setu.example.com:9000".
	RPCHost string `long:"rpchost" description:"The host:port of the Setu validator node's gRPC endpoint"`

	// TLSCertPath is the path to the TLS certificate used to authenticate
	// the connection to the Setu validator node. Leave empty for insecure
	// connections (development only).
	TLSCertPath string `long:"tlscertpath" description:"Path to the TLS certificate for the Setu validator node. Leave empty for insecure plaintext connections (dev only)."`

	// MacaroonPath is an optional path to a macaroon file for
	// authenticating RPC calls to the Setu validator node.
	MacaroonPath string `long:"macaroonpath" description:"Path to the macaroon file for authenticating Setu RPC calls (optional)"`

	// SubnetID is the Setu subnet identifier this node should operate on.
	// Corresponds to the subnet_id field in the Setu genesis configuration.
	SubnetID string `long:"subnetid" description:"Setu subnet ID to connect to"`

	// ChainID is the Setu chain (network) identifier.  Corresponds to the
	// chain_id field in the genesis configuration.
	ChainID string `long:"chainid" description:"Setu chain ID (network identifier)"`

	// EpochInterval is the expected time in milliseconds between Setu
	// epochs. This is used to calibrate timeout heuristics alongside the
	// VLC logical clock.
	EpochInterval int `long:"epochinterval" description:"Expected milliseconds between Setu epochs (used for timeout heuristics)"`

	// NumConfs is the number of Setu anchor finalizations required before
	// treating an event as confirmed. Because DAG finality is near-instant,
	// this defaults to 1.
	NumConfs uint32 `long:"numconfs" description:"Number of Setu anchor finalizations required for confirmation (default: 1)"`

	// CSVDelay is the default relative time delay in VLC ticks applied to
	// to_local outputs during a force-close. Analogous to OP_CSV in
	// Bitcoin.
	CSVDelay uint16 `long:"csvdelay" description:"Default to_local VLC tick delay for force-close outputs (analogous to Bitcoin CSV delay)"`
}

// DefaultSetuNode returns a SetuNode configuration populated with sensible
// defaults.
func DefaultSetuNode() *SetuNode {
	return &SetuNode{
		RPCHost:       fmt.Sprintf("%s:%d", DefaultSetuRPCHost, DefaultSetuRPCPort),
		EpochInterval: DefaultSetuEpochInterval,
		NumConfs:      DefaultSetuNumConfs,
		CSVDelay:      DefaultSetuCSVDelay,
	}
}

// Validate checks that the SetuNode configuration is internally consistent.
func (s *SetuNode) Validate() error {
	if s.RPCHost == "" {
		return fmt.Errorf("setu.rpchost must not be empty")
	}

	host, _, err := net.SplitHostPort(s.RPCHost)
	if err != nil {
		// Treat bare hostname (no port) as valid; callers are expected
		// to append the default port when dialling.
		host = s.RPCHost
	}

	if host == "" {
		return fmt.Errorf("setu.rpchost has an empty host component")
	}

	if s.EpochInterval <= 0 {
		return fmt.Errorf("setu.epochinterval must be a positive value")
	}

	if s.NumConfs == 0 {
		return fmt.Errorf("setu.numconfs must be at least 1")
	}

	return nil
}

// RPCAddr returns the network address (host:port) for dialling the Setu
// validator gRPC endpoint.  If RPCHost already contains a port the value is
// returned verbatim; otherwise the default port is appended.
func (s *SetuNode) RPCAddr() string {
	_, _, err := net.SplitHostPort(s.RPCHost)
	if err != nil {
		// No port present – append default.
		return fmt.Sprintf("%s:%d", s.RPCHost, DefaultSetuRPCPort)
	}

	return s.RPCHost
}
