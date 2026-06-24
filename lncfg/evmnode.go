package lncfg

import (
	"fmt"
	"net"
	"strings"
)

const (
	// DefaultEvmRPCHost is the default host:port for the EVM JSON-RPC
	// endpoint (Anvil's default).
	DefaultEvmRPCHost = "http://127.0.0.1:8545"

	// DefaultEvmNumConfs is the default number of confirmations required
	// before treating an EVM event/receipt as final. L2 finality is fast
	// but not instant, so we wait for a small depth to absorb sequencer
	// reorgs.
	DefaultEvmNumConfs = 3

	// DefaultEvmGasLimit is the default gas limit applied to ChannelManager
	// calls. ecrecover-heavy settlement calls are comparatively expensive,
	// so the ceiling is generous.
	DefaultEvmGasLimit = 500_000

	// DefaultEvmChain is the default EVM sub-network name when none is
	// given on the command line.
	DefaultEvmChain = "anvil"
)

// EvmNode holds configuration options for the LND node's connection to an
// EVM-compatible chain backend (Base, Taiko/Tempo, Arbitrum, a local Anvil
// devnet, …). It mirrors SuiNode: when Active is false every other EVM flag is
// ignored and the node runs the standard Bitcoin path.
//
//nolint:ll
type EvmNode struct {
	// Active enables the EVM chain backend. Must be set true via
	// --evm.active to run on EVM instead of Bitcoin. Defaults to false so
	// existing Bitcoin-only deployments are unaffected.
	Active bool `long:"active" description:"Enable the EVM chain backend. Must be true to use an EVM chain instead of Bitcoin."`

	// Chain is the EVM sub-network name (e.g. "base", "tempo", "anvil").
	// It selects the EvmParams entry used for key-derivation coin type and
	// the synthesized genesis hash.
	Chain string `long:"chain" description:"EVM sub-network name (base, tempo, anvil, ...)"`

	// ChainID is the EVM chain id (8453 for Base, 31337 for Anvil, ...). It
	// is bound into the EIP-712 domain so a signed StateUpdate is valid on
	// exactly one sub-network.
	ChainID uint64 `long:"chainid" description:"EVM chain id (8453 Base, 31337 Anvil, ...)"`

	// RPCHost is the JSON-RPC endpoint of the EVM node. WebSocket
	// subscriptions for event monitoring use the ws:// form of the same
	// host when available.
	RPCHost string `long:"rpchost" description:"The EVM node JSON-RPC endpoint URL (http(s):// or ws(s)://)"`

	// TokenAddress is the ERC20 asset (USDC/USDT) this sub-network settles
	// in. 20-byte hex address.
	TokenAddress string `long:"tokenaddress" description:"ERC20 token contract address escrowed by channels on this sub-network"`

	// ContractAddress is the deployed ChannelManager escrow contract
	// address. 20-byte hex address.
	ContractAddress string `long:"contractaddress" description:"Deployed ChannelManager escrow contract address"`

	// GasLimit is the gas ceiling applied to ChannelManager calls.
	GasLimit uint64 `long:"gaslimit" description:"Gas limit for ChannelManager calls (default 500000)"`

	// NumConfs is the number of confirmations required before treating an
	// EVM event/receipt as final.
	NumConfs uint32 `long:"numconfs" description:"EVM confirmations required for finality (default 3)"`

	// KeyIndex selects the KeyFamilyNodeKey index from which the node's
	// on-chain settlement account is derived (default 0). Bump it to rotate
	// the settlement address to a fresh key, independently of the Lightning
	// node identity. NOTE: a new index does NOT mitigate a leaked wallet
	// seed (all indices derive from the same seed); true key rotation
	// requires recovering from a new seed. Funds/gas must be moved to the
	// new address after changing this.
	KeyIndex uint32 `long:"keyindex" description:"KeyFamilyNodeKey index for the on-chain settlement account (default 0). Bump to rotate the settlement key independently of the node identity; does not help if the seed itself leaked."`
}

// DefaultEvmNode returns an EvmNode configuration populated with sensible
// defaults.
func DefaultEvmNode() *EvmNode {
	return &EvmNode{
		Chain:    DefaultEvmChain,
		RPCHost:  DefaultEvmRPCHost,
		GasLimit: DefaultEvmGasLimit,
		NumConfs: DefaultEvmNumConfs,
	}
}

// Validate checks that the EvmNode configuration is internally consistent.
func (e *EvmNode) Validate() error {
	if e.RPCHost == "" {
		return fmt.Errorf("evm.rpchost must not be empty")
	}

	if e.ChainID == 0 {
		return fmt.Errorf("evm.chainid must be a positive value")
	}

	if !isHexAddress(e.TokenAddress) {
		return fmt.Errorf("evm.tokenaddress must be a 20-byte hex "+
			"address, got %q", e.TokenAddress)
	}

	if !isHexAddress(e.ContractAddress) {
		return fmt.Errorf("evm.contractaddress must be a 20-byte hex "+
			"address, got %q", e.ContractAddress)
	}

	if e.NumConfs == 0 {
		return fmt.Errorf("evm.numconfs must be at least 1")
	}

	if e.GasLimit == 0 {
		return fmt.Errorf("evm.gaslimit must be a positive value")
	}

	return nil
}

// RPCAddr returns the URL for dialling the EVM node JSON-RPC endpoint,
// returning the configured value verbatim when it already carries a scheme.
func (e *EvmNode) RPCAddr() string {
	if strings.Contains(e.RPCHost, "://") {
		return e.RPCHost
	}

	// Bare host: default to http and append the conventional port if none
	// is present.
	if _, _, err := net.SplitHostPort(e.RPCHost); err != nil {
		return fmt.Sprintf("http://%s:8545", e.RPCHost)
	}

	return fmt.Sprintf("http://%s", e.RPCHost)
}

// isHexAddress reports whether s is a 0x-prefixed 20-byte (40 hex-digit)
// Ethereum address.
func isHexAddress(s string) bool {
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return false
	}
	s = s[2:]
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}

	return true
}
