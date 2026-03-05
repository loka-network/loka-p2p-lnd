package chainreg

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/keychain"
)

const (
	// CoinTypeSetu is the BIP-44 coin type assigned to the Setu network.
	// This value (99999) mirrors the DERIVATION_PATH_COIN_TYPE constant
	// defined in the Setu setu-keys crate (crates/setu-keys/src/key_derive.rs).
	CoinTypeSetu uint32 = 99999

	// CoinTypeSetuTestnet is the BIP-44 coin type for all Setu test
	// networks (devnet / simnet). It reuses the standard BIP-44 testnet
	// coin type for consistency with the rest of LND.
	CoinTypeSetuTestnet uint32 = keychain.CoinTypeTestnet
)

// setuDevnetGenesisHashHex is the SHA-256 pre-image of the devnet chain_id
// string "setu-devnet", zero-padded to 32 bytes and hex-encoded. In a
// production deployment this would be a canonical hash committed to in the
// on-chain genesis anchor.
const setuDevnetGenesisHashHex = "736574752d6465766e6574000000000000000000000000000000000000000000"

// setuDevnetGenesisHash is the parsed genesis hash for the Setu devnet.
var setuDevnetGenesisHash = mustDecodeHash(setuDevnetGenesisHashHex)

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

// SetuNetParams couples the network-level parameters of a Setu deployment
// with the RPC port of the validator node and the BIP-44 coin type used for
// key derivation.
//
// Setu does not use Bitcoin's chaincfg.Params; instead these lightweight
// parameters capture only what LND requires to disambiguate networks and
// derive keys.
type SetuNetParams struct {
	// Name is the human-readable network identifier (e.g. "mainnet",
	// "testnet", "devnet", "simnet").
	Name string

	// ChainID is the Setu chain identifier string as found in genesis.json.
	ChainID string

	// SubnetID identifies the specific Setu subnet within the chain.
	SubnetID string

	// GenesisHash is a 32-byte identifier for the network derived from
	// (or committed to) the genesis anchor.  It is used wherever LND
	// needs a chain-level hash to distinguish networks.
	GenesisHash chainhash.Hash

	// DefaultRPCPort is the default gRPC port of the Setu validator node
	// for this network.
	DefaultRPCPort string

	// CoinType is the BIP-44 coin type used for HD key derivation on this
	// network.
	CoinType uint32
}

// SetuDevNetParams contains parameters for connecting to a local Setu devnet
// (equivalent to Bitcoin's regtest / simnet).
var SetuDevNetParams = SetuNetParams{
	Name:           "devnet",
	ChainID:        "setu-devnet",
	SubnetID:       "ROOT",
	GenesisHash:    setuDevnetGenesisHash,
	DefaultRPCPort: "9000",
	CoinType:       CoinTypeSetuTestnet,
}

// SetuTestNetParams contains parameters for the Setu public testnet.
// Values will be updated when an official testnet is launched.
var SetuTestNetParams = SetuNetParams{
	Name:           "testnet",
	ChainID:        "setu-testnet",
	SubnetID:       "ROOT",
	GenesisHash:    mustDecodeHash("736574752d746573746e65740000000000000000000000000000000000000000"),
	DefaultRPCPort: "19000",
	CoinType:       CoinTypeSetuTestnet,
}

// SetuMainNetParams contains parameters for the Setu mainnet.
// Values will be updated when mainnet is deployed.
var SetuMainNetParams = SetuNetParams{
	Name:           "mainnet",
	ChainID:        "setu-mainnet",
	SubnetID:       "ROOT",
	GenesisHash:    mustDecodeHash("736574752d6d61696e6e65740000000000000000000000000000000000000000"),
	DefaultRPCPort: "9000",
	CoinType:       CoinTypeSetu,
}

// SetuSimNetParams contains parameters suitable for a local simulation/unit-
// test environment where the Setu validator is in-process or mocked.
var SetuSimNetParams = SetuNetParams{
	Name:           "simnet",
	ChainID:        "setu-simnet",
	SubnetID:       "ROOT",
	GenesisHash:    mustDecodeHash("736574752d73696d6e6574000000000000000000000000000000000000000000"),
	DefaultRPCPort: "19100",
	CoinType:       CoinTypeSetuTestnet,
}

// DefaultSetuMinHTLCInMSat is the smallest HTLC value accepted on Setu
// channels, expressed in milli-satoshis for compatibility with LND's
// lnwire.MilliSatoshi type.  In practice this maps to 1 Setu minimum unit.
const DefaultSetuMinHTLCInMSat = DefaultBitcoinMinHTLCInMSat

// DefaultSetuMinHTLCOutMSat is the minimum outgoing HTLC size on Setu
// channels.
const DefaultSetuMinHTLCOutMSat = DefaultBitcoinMinHTLCOutMSat

// DefaultSetuBaseFeeMSat is the default forwarding base fee for Setu
// channels, expressed in millisatoshis.
const DefaultSetuBaseFeeMSat = DefaultBitcoinBaseFeeMSat

// DefaultSetuFeeRate is the default proportional forwarding fee for Setu
// channels.
const DefaultSetuFeeRate = DefaultBitcoinFeeRate

// DefaultSetuTimeLockDelta is the default CLTV delta subtracted from
// forwarded HTLCs on Setu.  Because Setu uses VLC logical time rather than
// Bitcoin block heights, the interpretation of this value is in VLC ticks.
const DefaultSetuTimeLockDelta = DefaultBitcoinTimeLockDelta

// DefaultSetuStaticFeePerKW is a placeholder fee rate used by the Setu static
// fee estimator.  Since Setu currently has no dynamic gas mechanism the value
// is kept low; a chain-level gas API can replace this in future.
const DefaultSetuStaticFeePerKW = DefaultBitcoinStaticFeePerKW

// DefaultSetuStaticMinRelayFeeRate is the minimum relay fee rate for the Setu
// static estimator.
const DefaultSetuStaticMinRelayFeeRate = DefaultBitcoinStaticMinRelayFeeRate
