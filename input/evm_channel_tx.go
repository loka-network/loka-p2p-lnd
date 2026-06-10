package input

import (
	"encoding/json"
	"fmt"

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
