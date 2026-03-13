package chainreg

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/keychain"
)

const (
	// CoinTypeSui is the BIP-44 coin type assigned to the Sui network.
	// This value (784) is the standard BIP-44 coin type for Sui.
	CoinTypeSui uint32 = 784

	// CoinTypeSuiTestnet is the BIP-44 coin type for all Sui test
	// networks (devnet / simnet). It reuses the standard BIP-44 testnet
	// coin type for consistency with the rest of LND.
	CoinTypeSuiTestnet uint32 = keychain.CoinTypeTestnet
)

// suiDevnetGenesisHashHex is the SHA-256 pre-image of the devnet chain_id
// string "sui-devnet", zero-padded to 32 bytes and hex-encoded.
const suiDevnetGenesisHashHex = "7375692d6465766e657400000000000000000000000000000000000000000000"

// suiDevnetGenesisHash is the parsed genesis hash for the Sui devnet.
var suiDevnetGenesisHash = mustDecodeHash(suiDevnetGenesisHashHex)

// mustDecodeHash panics if the hex string cannot be decoded into a
// chainhash.Hash.  It is intended only for package-level variable
// initialisation.
func mustDecodeHash(s string) chainhash.Hash {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("chainreg: invalid genesis hash hex: " + err.Error())
	}

	if len(b) != chainhash.HashSize {
		panic(fmt.Sprintf(
			"chainreg: invalid genesis hash length: got %d, want %d",
			len(b), chainhash.HashSize,
		))
	}

	var h chainhash.Hash
	copy(h[:], b)

	return h
}

// SuiNetParams couples the network-level parameters of a Sui deployment
// with the RPC port of the node and the BIP-44 coin type used for
// key derivation.
//
// Sui does not use Bitcoin's chaincfg.Params; instead these lightweight
// parameters capture only what LND requires to disambiguate networks and
// derive keys.
type SuiNetParams struct {
	// Name is the human-readable network identifier (e.g. "mainnet",
	// "testnet", "devnet", "simnet").
	Name string

	// ChainID is the Sui chain identifier string.
	ChainID string

	// GenesisHash is a 32-byte identifier for the network. It is used 
	// wherever LND needs a chain-level hash to distinguish networks.
	GenesisHash chainhash.Hash

	// DefaultRPCPort is the default RPC port of the Sui node
	// for this network.
	DefaultRPCPort string

	// CoinType is the BIP-44 coin type used for HD key derivation on this
	// network.
	CoinType uint32
}

// SuiDevNetParams contains parameters for connecting to a local Sui devnet
// (equivalent to Bitcoin's regtest / simnet).
var SuiDevNetParams = SuiNetParams{
	Name:           "devnet",
	ChainID:        "sui-devnet",
	GenesisHash:    suiDevnetGenesisHash,
	DefaultRPCPort: "9000",
	CoinType:       CoinTypeSuiTestnet,
}

// SuiTestNetParams contains parameters for the Sui public testnet.
var SuiTestNetParams = SuiNetParams{
	Name:           "testnet",
	ChainID:        "sui-testnet",
	GenesisHash:    mustDecodeHash("7375692d746573746e6574000000000000000000000000000000000000000000"),
	DefaultRPCPort: "9000",
	CoinType:       CoinTypeSuiTestnet,
}

// SuiMainNetParams contains parameters for the Sui mainnet.
var SuiMainNetParams = SuiNetParams{
	Name:           "mainnet",
	ChainID:        "sui-mainnet",
	GenesisHash:    mustDecodeHash("7375692d6d61696e6e6574000000000000000000000000000000000000000000"),
	DefaultRPCPort: "9000",
	CoinType:       CoinTypeSui,
}

// SuiSimNetParams contains parameters suitable for a local simulation/unit-
// test environment.
var SuiSimNetParams = SuiNetParams{
	Name:           "simnet",
	ChainID:        "sui-simnet",
	GenesisHash:    mustDecodeHash("7375692d73696d6e657400000000000000000000000000000000000000000000"),
	DefaultRPCPort: "9000",
	CoinType:       CoinTypeSuiTestnet,
}

// DefaultSuiMinHTLCInMSat is the smallest HTLC value accepted on Sui
// channels, expressed in milli-satoshis for compatibility with LND's
// lnwire.MilliSatoshi type.
const DefaultSuiMinHTLCInMSat = DefaultBitcoinMinHTLCInMSat

// DefaultSuiMinHTLCOutMSat is the minimum outgoing HTLC size on Sui
// channels.
const DefaultSuiMinHTLCOutMSat = DefaultBitcoinMinHTLCOutMSat

// DefaultSuiBaseFeeMSat is the default forwarding base fee for Sui
// channels, expressed in millisatoshis.
const DefaultSuiBaseFeeMSat = DefaultBitcoinBaseFeeMSat

// DefaultSuiFeeRate is the default proportional forwarding fee for Sui
// channels.
const DefaultSuiFeeRate = DefaultBitcoinFeeRate

// DefaultSuiTimeLockDelta is the default CLTV delta subtracted from
// forwarded HTLCs on Sui.
const DefaultSuiTimeLockDelta = DefaultBitcoinTimeLockDelta

// DefaultSuiStaticFeePerKW is a placeholder fee rate used by the Sui static
// fee estimator.
const DefaultSuiStaticFeePerKW = DefaultBitcoinStaticFeePerKW

// DefaultSuiStaticMinRelayFeeRate is the minimum relay fee rate for the Sui
// static estimator.
const DefaultSuiStaticMinRelayFeeRate = DefaultBitcoinStaticMinRelayFeeRate
