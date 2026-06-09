package chainreg

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/keychain"
)

const (
	// CoinTypeEvm is the BIP-44 coin type assigned to Ethereum/EVM
	// mainnets (60). EVM reuses secp256k1, so LND's existing
	// BtcWalletKeyRing is reused unchanged and only the coin type differs.
	CoinTypeEvm uint32 = 60

	// CoinTypeEvmTestnet is the BIP-44 coin type for EVM test sub-networks
	// (local Anvil, public testnets). It reuses LND's testnet coin type,
	// mirroring chainreg/sui_params.go.
	CoinTypeEvmTestnet uint32 = keychain.CoinTypeTestnet
)

// EvmParams defines the parameter configuration for a single EVM sub-network,
// i.e. a (chain, ERC20 asset) pair. Each such pair runs as an independent LND
// sub-network daemon.
//
// Like SuiNetParams, EVM does not use Bitcoin's chaincfg.Params; these
// lightweight parameters capture only what LND needs to disambiguate networks
// and derive keys.
type EvmParams struct {
	// Name is the human-readable sub-network identifier (e.g. "base",
	// "tempo", "anvil").
	Name string

	// ChainID is the EVM chain id. Bound into the EIP-712 domain so a
	// signed StateUpdate is valid on exactly one sub-network.
	ChainID uint64

	// TokenAddress is the default ERC20 asset address for this sub-network
	// (may be overridden by --evm.tokenaddress).
	TokenAddress string

	// ContractAddr is the default deployed ChannelManager address (may be
	// overridden by --evm.contractaddress). CREATE2 yields a stable address
	// across chains only for identical initcode (see refactor-plan §4).
	ContractAddr string

	// CoinType is the BIP-44 coin type used for HD key derivation.
	CoinType uint32

	// GenesisHash is a 32-byte identifier for the sub-network, synthesized
	// from (ChainID, TokenAddress) so distinct assets on the same chain are
	// distinguishable wherever LND needs a chain-level hash.
	GenesisHash chainhash.Hash
}

// EvmBaseParams contains parameters for the Base mainnet USDC sub-network.
var EvmBaseParams = NewEvmParams(
	"base", 8453,
	"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", // USDC on Base
	"", CoinTypeEvm,
)

// EvmAnvilParams contains parameters for a local Anvil devnet. The token and
// contract addresses are deployment-specific and supplied on the command line.
var EvmAnvilParams = NewEvmParams(
	"anvil", 31337, "", "", CoinTypeEvmTestnet,
)

// NewEvmParams constructs an EvmParams, deriving the synthesized GenesisHash
// from (chainID, tokenAddress). The token may be empty for params whose asset
// is supplied at runtime; the hash is recomputed by ResolveEvmParams once the
// real token is known.
func NewEvmParams(name string, chainID uint64, token, contract string,
	coinType uint32) EvmParams {

	return EvmParams{
		Name:         name,
		ChainID:      chainID,
		TokenAddress: token,
		ContractAddr: contract,
		CoinType:     coinType,
		GenesisHash:  SynthesizeEvmGenesisHash(chainID, token),
	}
}

// SynthesizeEvmGenesisHash deterministically derives a 32-byte chain-level
// identifier from the EVM chain id and ERC20 token address. Distinct assets on
// the same chain therefore yield distinct hashes, so LND treats each (chain,
// asset) pair as its own network. The pre-image is the canonical, lowercased
// string "evm:<chainid>:<token>".
func SynthesizeEvmGenesisHash(chainID uint64, token string) chainhash.Hash {
	preimage := fmt.Sprintf("evm:%d:%s", chainID, strings.ToLower(token))
	return chainhash.Hash(sha256.Sum256([]byte(preimage)))
}

// evmParamsByName indexes the built-in sub-network presets.
var evmParamsByName = map[string]EvmParams{
	EvmBaseParams.Name:  EvmBaseParams,
	EvmAnvilParams.Name: EvmAnvilParams,
}

// ResolveEvmParams returns the EvmParams for the given sub-network name,
// overlaying the runtime chain id, token and contract addresses supplied on the
// command line and recomputing the synthesized GenesisHash. An unknown name
// yields a fresh testnet-coin-type params entry so arbitrary chains still work.
func ResolveEvmParams(name string, chainID uint64,
	token, contract string) EvmParams {

	p, ok := evmParamsByName[strings.ToLower(name)]
	if !ok {
		p = EvmParams{Name: name, CoinType: CoinTypeEvmTestnet}
	}

	if chainID != 0 {
		p.ChainID = chainID
	}
	if token != "" {
		p.TokenAddress = token
	}
	if contract != "" {
		p.ContractAddr = contract
	}
	p.GenesisHash = SynthesizeEvmGenesisHash(p.ChainID, p.TokenAddress)

	return p
}

// The EVM channel forwarding defaults reuse the Bitcoin defaults, matching the
// Sui adapter's approach.
const (
	// DefaultEvmMinHTLCInMSat is the smallest incoming HTLC accepted on EVM
	// channels.
	DefaultEvmMinHTLCInMSat = DefaultBitcoinMinHTLCInMSat

	// DefaultEvmMinHTLCOutMSat is the minimum outgoing HTLC size on EVM
	// channels.
	DefaultEvmMinHTLCOutMSat = DefaultBitcoinMinHTLCOutMSat

	// DefaultEvmBaseFeeMSat is the default forwarding base fee.
	DefaultEvmBaseFeeMSat = DefaultBitcoinBaseFeeMSat

	// DefaultEvmFeeRate is the default proportional forwarding fee.
	DefaultEvmFeeRate = DefaultBitcoinFeeRate

	// DefaultEvmTimeLockDelta is the default CLTV delta for forwarded HTLCs.
	DefaultEvmTimeLockDelta = DefaultBitcoinTimeLockDelta
)
