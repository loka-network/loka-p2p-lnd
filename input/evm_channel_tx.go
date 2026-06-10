package input

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// This file wraps an EVM ChannelManager call into a wire.MsgTx, mirroring the
// Sui call-carrier in sui_channel.go. The adapter boundary keeps speaking in
// wire.MsgTx (the lnwallet/funding plumbing is untouched); the EVM-specific
// call envelope rides in TxIn[0].SignatureScript and the channelId in
// PreviousOutPoint.Hash (wire.OutPoint.Hash ↔ channelId, per the type-mapping
// convention). evmwallet decodes the envelope, applies the Decimals Scaling
// Factor, ABI-encodes the actual call, and broadcasts it.
//
// Amounts in these payloads are LND-internal btcutil.Amount base-units
// (1 token = 1e8); the raw-ERC20 scaling lives only in evmwallet, never here.

// EvmCallType identifies which ChannelManager method a carrier tx represents.
type EvmCallType string

const (
	// EvmCallChannelOpen is an openChannel(salt, counterparty, localAmt,
	// remoteAmt) call.
	EvmCallChannelOpen EvmCallType = "ChannelOpen"

	// EvmCallChannelClose is a cooperative closeChannel call.
	EvmCallChannelClose EvmCallType = "ChannelClose"

	// EvmCallForceClose is a unilateral forceClose call.
	EvmCallForceClose EvmCallType = "ForceClose"

	// EvmCallClaimHtlc is a claimHtlc(preimage + Merkle proof) call.
	EvmCallClaimHtlc EvmCallType = "ClaimHtlc"

	// EvmCallTimeoutHtlc is a timeoutHtlc call after the timelock.
	EvmCallTimeoutHtlc EvmCallType = "TimeoutHtlc"

	// EvmCallPenalize is a penalize(newer signed state) call.
	EvmCallPenalize EvmCallType = "Penalize"

	// EvmCallDistributeFunds is the distributeFunds call that pays out after
	// the challenge window closes and all HTLCs are resolved.
	EvmCallDistributeFunds EvmCallType = "DistributeFunds"
)

// EvmChannelOpenPayload carries the parameters of an openChannel call. Salt and
// Counterparty are hex (no 0x prefix); the channelId the contract derives is
// keccak256(participantA, participantB, salt) with A = the funder broadcasting
// the call (see EvmChannelID).
type EvmChannelOpenPayload struct {
	// Salt is the 32-byte channel salt, hex-encoded.
	Salt string `json:"salt"`

	// Counterparty is the 20-byte counterparty EVM address, hex-encoded.
	Counterparty string `json:"counterparty"`

	// LocalBalance / RemoteBalance are the two deposits in LND-internal
	// base-units (evmwallet scales to raw token units before the call).
	LocalBalance  uint64 `json:"local_balance"`
	RemoteBalance uint64 `json:"remote_balance"`

	// LocalKey / RemoteKey are the compressed funding pubkeys, hex-encoded,
	// retained so the responder can reconstruct the same channelId.
	LocalKey  string `json:"local_key"`
	RemoteKey string `json:"remote_key"`
}

// evmCall is the JSON envelope spliced into TxIn[0].SignatureScript.
type evmCall struct {
	Type    EvmCallType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// BuildEvmCallTx wraps a serialised EVM ChannelManager call into a wire.MsgTx
// carrier keyed by channelId.
func BuildEvmCallTx(channelID chainhash.Hash, callType EvmCallType,
	payload interface{}) (*wire.MsgTx, error) {

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("evm_channel_tx: marshal %s payload: %w",
			callType, err)
	}

	envelopeBytes, err := json.Marshal(evmCall{
		Type:    callType,
		Payload: payloadBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("evm_channel_tx: marshal call "+
			"envelope: %w", err)
	}

	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  channelID,
			Index: 0,
		},
		SignatureScript: envelopeBytes,
		Sequence:        wire.MaxTxInSequenceNum,
	})
	tx.AddTxOut(&wire.TxOut{
		Value:    0,
		PkScript: []byte{0x51}, // OP_1 placeholder
	})

	return tx, nil
}

// BuildEvmChannelOpenTx creates the carrier tx for an openChannel call.
func BuildEvmChannelOpenTx(channelID chainhash.Hash,
	payload EvmChannelOpenPayload) (*wire.MsgTx, error) {

	return BuildEvmCallTx(channelID, EvmCallChannelOpen, payload)
}

// --------------------------------------------------------------------------
// Settlement-call payloads (resolver surface, spec §2.4)
// --------------------------------------------------------------------------
//
// Unlike openChannel (whose amounts are LND-internal and scaled by evmwallet),
// these payloads carry values already in the contract's units: balances and
// HTLC amounts are raw token base-units (decimal strings, since uint256 exceeds
// int64), because they must match byte-for-byte what was committed in the
// htlcsHash / StateUpdate digest. evmwallet ABI-encodes them verbatim.

// EvmStateClosePayload carries a forceClose or penalize call: a StateUpdate
// (nonce/balances/htlcsHash) plus the 65-byte signature recovering to the
// counterparty (forceClose) or the cheating broadcaster (penalize).
type EvmStateClosePayload struct {
	// Nonce is the StateUpdate nonce: the broadcast state for forceClose, the
	// strictly-higher correctNonce for penalize.
	Nonce uint64 `json:"nonce"`

	// BalanceA / BalanceB are raw token base-units (decimal string).
	BalanceA string `json:"balance_a"`
	BalanceB string `json:"balance_b"`

	// HtlcsHash is the Merkle root committed in the state, hex-encoded.
	HtlcsHash string `json:"htlcs_hash"`

	// Sig is the 65-byte (r ‖ s ‖ v) signature, hex-encoded.
	Sig string `json:"sig"`
}

// EvmHTLCData is the contract HTLC struct in carrier form, presented to
// claimHtlc / timeoutHtlc and proven against the committed htlcsHash. The fields
// (and thus the Merkle leaf) must reproduce exactly what HtlcsMerkleRoot hashed.
type EvmHTLCData struct {
	Index     uint64 `json:"index"`
	Amount    string `json:"amount"` // raw base-units, decimal
	Hashlock  string `json:"hashlock"`
	Timelock  uint32 `json:"timelock"`
	Recipient string `json:"recipient"`
}

// EvmHtlcResolvePayload carries a claimHtlc (preimage set) or timeoutHtlc
// (preimage empty) call: the HTLC plus its Merkle proof against htlcsHash.
type EvmHtlcResolvePayload struct {
	HTLC        EvmHTLCData `json:"htlc"`
	MerkleProof []string    `json:"merkle_proof"`

	// Preimage is the SHA-256 preimage for claimHtlc, hex-encoded; empty for
	// timeoutHtlc.
	Preimage string `json:"preimage,omitempty"`
}

// htlcToData serialises an EvmHTLC into the carrier form.
func htlcToData(h EvmHTLC) EvmHTLCData {
	amt := h.Amount
	if amt == nil {
		amt = big.NewInt(0)
	}

	return EvmHTLCData{
		Index:     h.Index,
		Amount:    amt.String(),
		Hashlock:  hex.EncodeToString(h.Hashlock[:]),
		Timelock:  h.Timelock,
		Recipient: hex.EncodeToString(h.Recipient[:]),
	}
}

// proofToHex hex-encodes a Merkle proof for the carrier payload.
func proofToHex(proof [][32]byte) []string {
	out := make([]string, len(proof))
	for i, p := range proof {
		out[i] = hex.EncodeToString(p[:])
	}

	return out
}

// BuildEvmForceCloseTx creates the carrier tx for a forceClose call (broadcast
// the latest agreed state, co-signed by the counterparty).
func BuildEvmForceCloseTx(channelID chainhash.Hash, nonce uint64,
	balanceA, balanceB *big.Int, htlcsHash [32]byte,
	sig []byte) (*wire.MsgTx, error) {

	return BuildEvmCallTx(channelID, EvmCallForceClose, EvmStateClosePayload{
		Nonce:     nonce,
		BalanceA:  bigOrZero(balanceA).String(),
		BalanceB:  bigOrZero(balanceB).String(),
		HtlcsHash: hex.EncodeToString(htlcsHash[:]),
		Sig:       hex.EncodeToString(sig),
	})
}

// BuildEvmPenalizeTx creates the carrier tx for a penalize call (submit a
// strictly-higher signed state, proving the broadcast one was revoked).
func BuildEvmPenalizeTx(channelID chainhash.Hash, correctNonce uint64,
	balanceA, balanceB *big.Int, htlcsHash [32]byte,
	correctSig []byte) (*wire.MsgTx, error) {

	return BuildEvmCallTx(channelID, EvmCallPenalize, EvmStateClosePayload{
		Nonce:     correctNonce,
		BalanceA:  bigOrZero(balanceA).String(),
		BalanceB:  bigOrZero(balanceB).String(),
		HtlcsHash: hex.EncodeToString(htlcsHash[:]),
		Sig:       hex.EncodeToString(correctSig),
	})
}

// BuildEvmClaimHtlcTx creates the carrier tx for a claimHtlc call: the HTLC, its
// Merkle proof against htlcsHash, and the SHA-256 preimage.
func BuildEvmClaimHtlcTx(channelID chainhash.Hash, htlc EvmHTLC,
	proof [][32]byte, preimage [32]byte) (*wire.MsgTx, error) {

	return BuildEvmCallTx(channelID, EvmCallClaimHtlc, EvmHtlcResolvePayload{
		HTLC:        htlcToData(htlc),
		MerkleProof: proofToHex(proof),
		Preimage:    hex.EncodeToString(preimage[:]),
	})
}

// BuildEvmTimeoutHtlcTx creates the carrier tx for a timeoutHtlc call: the HTLC
// and its Merkle proof, presented after the timelock expires.
func BuildEvmTimeoutHtlcTx(channelID chainhash.Hash, htlc EvmHTLC,
	proof [][32]byte) (*wire.MsgTx, error) {

	return BuildEvmCallTx(channelID, EvmCallTimeoutHtlc, EvmHtlcResolvePayload{
		HTLC:        htlcToData(htlc),
		MerkleProof: proofToHex(proof),
	})
}

// BuildEvmDistributeFundsTx creates the carrier tx for the distributeFunds call
// that pays out after the challenge window closes and all HTLCs are resolved.
func BuildEvmDistributeFundsTx(channelID chainhash.Hash) (*wire.MsgTx, error) {
	return BuildEvmCallTx(channelID, EvmCallDistributeFunds, struct{}{})
}

// bigOrZero returns v, or zero if v is nil.
func bigOrZero(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}

	return v
}

// DecodeEvmCallTx extracts the channelId, call type, and raw payload from a
// carrier tx produced by BuildEvmCallTx.
func DecodeEvmCallTx(tx *wire.MsgTx) (channelID chainhash.Hash,
	callType EvmCallType, payload json.RawMessage, err error) {

	if tx == nil || len(tx.TxIn) == 0 {
		return channelID, "", nil,
			fmt.Errorf("evm_channel_tx: tx has no inputs")
	}

	channelID = tx.TxIn[0].PreviousOutPoint.Hash

	var envelope evmCall
	if decErr := json.Unmarshal(
		tx.TxIn[0].SignatureScript, &envelope,
	); decErr != nil {
		return channelID, "", nil, fmt.Errorf("evm_channel_tx: decode "+
			"call envelope: %w", decErr)
	}

	return channelID, envelope.Type, envelope.Payload, nil
}
