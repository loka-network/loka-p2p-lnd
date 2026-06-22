// Package input — this file provides the EVM/Solidity equivalent of the Sui
// MoveVM channel helpers: the off-chain EIP-712 commitment schema that both
// peers sign every time the channel state advances, plus the on-chain channelId
// and EVM-address derivations the adapter needs.
//
// It mirrors the ChannelManager contract's EIP-712 schema exactly (see
// evm-contracts/channel-manager/src/ChannelManager.sol and
// 1-refactor-docs/evm/evm-ln-interaction-spec.md §2). The signed digest a peer
// produces here is what may later be presented to forceClose / penalize, so the
// bytes must agree with the contract byte-for-byte. evm_signer_test.go locks
// this down against golden vectors emitted by the Solidity test suite.
package input

import (
	"math/big"

	"golang.org/x/crypto/sha3"
)

// Keccak256 returns the Keccak-256 (Ethereum, not SHA3-256) hash of the
// concatenation of the inputs.
func Keccak256(data ...[]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, d := range data {
		h.Write(d)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))

	return out
}

// EIP-712 type strings — must match ChannelManager exactly.
const (
	eip712DomainType = "EIP712Domain(string name,string version," +
		"uint256 chainId,address verifyingContract)"

	stateUpdateType = "StateUpdate(bytes32 channelId,uint256 nonce," +
		"uint256 balanceA,uint256 balanceB,bytes32 htlcsHash)"

	coopCloseType = "CooperativeClose(bytes32 channelId,uint256 nonce," +
		"uint256 finalBalanceA,uint256 finalBalanceB)"

	// EvmDomainName / EvmDomainVersion are the EIP-712 domain identifiers
	// the ChannelManager is deployed with.
	EvmDomainName    = "LokaChannelManager"
	EvmDomainVersion = "1"
)

// Pre-computed EIP-712 type hashes.
var (
	eip712DomainTypeHash = Keccak256([]byte(eip712DomainType))
	stateUpdateTypeHash  = Keccak256([]byte(stateUpdateType))
	coopCloseTypeHash    = Keccak256([]byte(coopCloseType))
)

// EvmDomain is the EIP-712 domain separator input. chainID and
// verifyingContract are bound in on purpose so a StateUpdate signed for one
// sub-network cannot be replayed on another (spec §2.1).
type EvmDomain struct {
	// ChainID is the EVM chain id of the sub-network.
	ChainID uint64

	// VerifyingContract is the 20-byte deployed ChannelManager address.
	VerifyingContract [20]byte
}

// Separator computes the EIP-712 domainSeparator:
//
//	keccak256(abi.encode(DOMAIN_TYPEHASH, keccak256(name),
//	    keccak256(version), chainId, verifyingContract))
func (d EvmDomain) Separator() [32]byte {
	nameHash := Keccak256([]byte(EvmDomainName))
	versionHash := Keccak256([]byte(EvmDomainVersion))

	var buf []byte
	buf = append(buf, eip712DomainTypeHash[:]...)
	buf = append(buf, nameHash[:]...)
	buf = append(buf, versionHash[:]...)
	buf = append(buf, encodeUint256(new(big.Int).SetUint64(d.ChainID))...)
	buf = append(buf, encodeAddress(d.VerifyingContract)...)

	return Keccak256(buf)
}

// EvmStateUpdate is the per-commitment artifact each peer signs — the EVM
// analogue of a BOLT-03 commitment transaction. Balances are net of outstanding
// HTLCs (the BOLT commitment model).
type EvmStateUpdate struct {
	// ChannelID is the 32-byte on-chain channel identifier.
	ChannelID [32]byte

	// Nonce equals the LND StateNum (monotonic).
	Nonce uint64

	// BalanceA / BalanceB are the to_local / to_remote balances in token
	// base-units, net of HTLCs.
	BalanceA *big.Int
	BalanceB *big.Int

	// HtlcsHash is the Merkle root over the active HTLC set (zero when
	// empty).
	HtlcsHash [32]byte
}

// hashStruct computes keccak256(abi.encode(STATE_UPDATE_TYPEHASH, fields...)).
func (s EvmStateUpdate) hashStruct() [32]byte {
	var buf []byte
	buf = append(buf, stateUpdateTypeHash[:]...)
	buf = append(buf, s.ChannelID[:]...)
	buf = append(buf, encodeUint256(new(big.Int).SetUint64(s.Nonce))...)
	buf = append(buf, encodeUint256(s.BalanceA)...)
	buf = append(buf, encodeUint256(s.BalanceB)...)
	buf = append(buf, s.HtlcsHash[:]...)

	return Keccak256(buf)
}

// Digest returns the full EIP-712 digest the peer signs:
//
//	keccak256(0x19 ‖ 0x01 ‖ domainSeparator ‖ hashStruct(StateUpdate))
func (s EvmStateUpdate) Digest(domain EvmDomain) [32]byte {
	return eip712Digest(domain.Separator(), s.hashStruct())
}

// EvmCooperativeClose is the artifact both peers sign for a cooperative close;
// it commits to the agreed final split and the channel state number (Nonce),
// the latter binding the close to one off-chain state so an older co-signed
// split cannot be replayed in its place (audit M-2).
type EvmCooperativeClose struct {
	ChannelID     [32]byte
	Nonce         uint64
	FinalBalanceA *big.Int
	FinalBalanceB *big.Int
}

func (c EvmCooperativeClose) hashStruct() [32]byte {
	var buf []byte
	buf = append(buf, coopCloseTypeHash[:]...)
	buf = append(buf, c.ChannelID[:]...)
	buf = append(buf, encodeUint256(new(big.Int).SetUint64(c.Nonce))...)
	buf = append(buf, encodeUint256(c.FinalBalanceA)...)
	buf = append(buf, encodeUint256(c.FinalBalanceB)...)

	return Keccak256(buf)
}

// Digest returns the full EIP-712 digest the peers sign for a cooperative
// close.
func (c EvmCooperativeClose) Digest(domain EvmDomain) [32]byte {
	return eip712Digest(domain.Separator(), c.hashStruct())
}

// EvmHTLC mirrors the contract's HTLC struct, presented to claimHtlc /
// timeoutHtlc and proven against the committed htlcsHash. The Merkle leaf is
// keccak256(abi.encode(HTLC)) with fields in declaration order.
type EvmHTLC struct {
	// Index equals the LND UpdateLog index; it is the Merkle sort key.
	Index uint64

	// Amount is the HTLC value in token base-units.
	Amount *big.Int

	// Hashlock is sha256(preimage) — SHA-256 (BOLT), NOT keccak256.
	Hashlock [32]byte

	// Timelock is the absolute block.timestamp deadline.
	Timelock uint32

	// Recipient is the party credited on a successful claim.
	Recipient [20]byte
}

// Leaf returns keccak256(abi.encode(HTLC)) — the Merkle leaf the contract
// recomputes in _verifyHtlcInclusion. abi.encode pads every field to 32 bytes.
func (h EvmHTLC) Leaf() [32]byte {
	var buf []byte
	buf = append(buf, encodeUint256(new(big.Int).SetUint64(h.Index))...)
	buf = append(buf, encodeUint256(h.Amount)...)
	buf = append(buf, h.Hashlock[:]...)
	buf = append(buf, encodeUint256(new(big.Int).SetUint64(
		uint64(h.Timelock),
	))...)
	buf = append(buf, encodeAddress(h.Recipient)...)

	return Keccak256(buf)
}

// EvmChannelID derives the on-chain channelId exactly as ChannelManager does:
//
//	keccak256(abi.encodePacked(participantA, participantB, salt))
//
// encodePacked concatenates without padding, so the two 20-byte addresses and
// the 32-byte salt are laid down back-to-back (72 bytes total).
func EvmChannelID(participantA, participantB [20]byte, salt [32]byte) [32]byte {
	var buf []byte
	buf = append(buf, participantA[:]...)
	buf = append(buf, participantB[:]...)
	buf = append(buf, salt[:]...)

	return Keccak256(buf)
}

// eip712Digest applies the 0x19 0x01 prefix wrapping shared by all typed-data
// digests.
func eip712Digest(domainSeparator, structHash [32]byte) [32]byte {
	prefix := []byte{0x19, 0x01}

	return Keccak256(prefix, domainSeparator[:], structHash[:])
}

// encodeUint256 left-pads a big.Int to a 32-byte big-endian ABI word. A nil or
// negative value encodes as zero.
func encodeUint256(v *big.Int) []byte {
	out := make([]byte, 32)
	if v == nil || v.Sign() < 0 {
		return out
	}
	v.FillBytes(out)

	return out
}

// encodeAddress right-aligns a 20-byte address into a 32-byte ABI word.
func encodeAddress(addr [20]byte) []byte {
	out := make([]byte, 32)
	copy(out[12:], addr[:])

	return out
}
