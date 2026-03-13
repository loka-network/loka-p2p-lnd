// Package input contains Bitcoin input script construction utilities.  This
// file provides the Sui MoveVM equivalent: functions that construct Sui Move
// Call transactions instead of Bitcoin Scripts.
//
// Mapping (Bitcoin Script → Sui Move contract methods):
//
//	OP_2 <key1> <key2> OP_2 OP_CHECKMULTISIG  →  lightning::open_channel
//	HTLC hash-lock                             →  lightning::htlc_claim
//	OP_CSV relative time-lock                  →  lightning::claim_local_balance
//	OP_CLTV absolute time-lock                 →  lightning::htlc_timeout
//	Revocation / breach-remedy                 →  lightning::penalize
//
// Each Sui Move call is serialised to JSON and packed into the SignatureScript
// field of the first TxIn of a wire.MsgTx wrapper.  The OutPoint.Hash of that
// TxIn carries the Sui ObjectID (channel identifier).
//
// Reference: 1-refactor-docs/lnd-and-sui-integration.md §6
package input

import (
	"encoding/json"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// SuiCallType is an enum that identifies the Lightning Channel MoveVM
// module entry functions.
type SuiCallType uint8

const (
	// SuiCallChannelOpen calls lightning::open_channel to create a new
	// Channel object.
	SuiCallChannelOpen SuiCallType = iota

	// SuiCallChannelClose calls lightning::close_channel for a cooperative
	// close.
	SuiCallChannelClose

	// SuiCallChannelForceClose calls lightning::force_close for a unilateral
	// force-close.
	SuiCallChannelForceClose

	// SuiCallChannelClaimLocal calls lightning::claim_local_balance.
	SuiCallChannelClaimLocal

	// SuiCallChannelClaimRemote calls lightning::claim_remote_balance.
	SuiCallChannelClaimRemote

	// SuiCallHTLCClaim calls lightning::htlc_claim.
	SuiCallHTLCClaim

	// SuiCallHTLCTimeout calls lightning::htlc_timeout.
	SuiCallHTLCTimeout

	// SuiCallChannelPenalize calls lightning::penalize for breach-remedy.
	SuiCallChannelPenalize
)

// String returns the human-readable name of the call type.
func (t SuiCallType) String() string {
	switch t {
	case SuiCallChannelOpen:
		return "open_channel"
	case SuiCallChannelClose:
		return "close_channel"
	case SuiCallChannelForceClose:
		return "force_close"
	case SuiCallChannelClaimLocal:
		return "claim_local_balance"
	case SuiCallChannelClaimRemote:
		return "claim_remote_balance"
	case SuiCallHTLCClaim:
		return "htlc_claim"
	case SuiCallHTLCTimeout:
		return "htlc_timeout"
	case SuiCallChannelPenalize:
		return "penalize"
	default:
		return fmt.Sprintf("SuiCallType(%d)", uint8(t))
	}
}

// -------------------------------------------------------------------------
// Payload structs — one per CallType.
// These mirror the parameters of the MoveVM module entry functions.
// -------------------------------------------------------------------------

// ChannelOpenPayload is the payload for SuiCallChannelOpen.
type ChannelOpenPayload struct {
	// LocalKey is the local party's secp256k1 public key (compressed).
	LocalKey string `json:"local_key"`

	// RemoteKey is the remote party's secp256k1 public key.
	RemoteKey string `json:"remote_key"`

	// LocalBalance is the initial balance (in Mist).
	LocalBalance uint64 `json:"local_balance"`

	// RemoteBalance is the initial balance for the remote party.
	RemoteBalance uint64 `json:"remote_balance"`

	// CSVDelay is the relative epoch delay for force-close.
	CSVDelay uint64 `json:"csv_delay"`
}

// ChannelClosePayload is the payload for SuiCallChannelClose.
type ChannelClosePayload struct {
	// StateNum is the monotonically-increasing state counter.
	StateNum uint64 `json:"state_num"`

	// LocalBalance is the final balance agreed upon by both parties.
	LocalBalance uint64 `json:"local_balance"`

	// RemoteBalance is the remote party's final balance.
	RemoteBalance uint64 `json:"remote_balance"`

	// LocalSig is the local party's signature over the close payload.
	LocalSig []byte `json:"local_sig"`

	// RemoteSig is the remote party's signature.
	RemoteSig []byte `json:"remote_sig"`
}

// ChannelForceClosePayload is the payload for SuiCallChannelForceClose.
type ChannelForceClosePayload struct {
	// StateNum is the commitment state number being force-closed.
	StateNum uint64 `json:"state_num"`

	// CommitmentSig is the owner's signature over this commitment state.
	CommitmentSig []byte `json:"commitment_sig"`
}

// ChannelClaimLocalPayload is the payload for SuiCallChannelClaimLocal.
type ChannelClaimLocalPayload struct {
	// Sig is the claimer's signature authorising the sweep.
	Sig []byte `json:"sig"`
}

// ChannelClaimRemotePayload is the payload for SuiCallChannelClaimRemote.
type ChannelClaimRemotePayload struct {
	// Sig is the remote party's signature.
	Sig []byte `json:"sig"`
}

// HTLCClaimPayload is the payload for SuiCallHTLCClaim.
type HTLCClaimPayload struct {
	// HtlcID is the ID of this HTLC inside the Channel object.
	HtlcID uint64 `json:"htlc_id"`

	// PaymentHash is the 32-byte SHA256 hash of the preimage.
	PaymentHash [32]byte `json:"payment_hash"`

	// Preimage is the 32-byte secret.
	Preimage [32]byte `json:"preimage"`

	// Sig is the recipient's signature.
	Sig []byte `json:"sig"`
}

// HTLCTimeoutPayload is the payload for SuiCallHTLCTimeout.
type HTLCTimeoutPayload struct {
	// HtlcID is the ID of this HTLC inside the Channel object.
	HtlcID uint64 `json:"htlc_id"`

	// PaymentHash identifies the HTLC being timed out.
	PaymentHash [32]byte `json:"payment_hash"`

	// Sig is the sender's signature.
	Sig []byte `json:"sig"`
}

// ChannelPenalizePayload is the payload for SuiCallChannelPenalize.
type ChannelPenalizePayload struct {
	// RevocationKey is the revocation private key.
	RevocationKey []byte `json:"revocation_key"`

	// BreachStateNum is the state_num of the revoked commitment.
	BreachStateNum uint64 `json:"breach_state_num"`

	// Sig is the honest party's signature.
	Sig []byte `json:"sig"`
}

// -------------------------------------------------------------------------
// suiMoveCall is the on-wire envelope packed into wire.MsgTx.SignatureScript.
// -------------------------------------------------------------------------

type suiMoveCall struct {
	// Type is the Move function identifier.
	Type SuiCallType `json:"type"`

	// Payload is the raw JSON-encoded payload.
	Payload json.RawMessage `json:"payload"`
}

// -------------------------------------------------------------------------
// Helper: wrap a Sui Move call into a wire.MsgTx
// -------------------------------------------------------------------------

// BuildSuiCallTx creates a wire.MsgTx wrapper that carries a serialised Sui
// Move Call.
func BuildSuiCallTx(
	channelObjectID chainhash.Hash,
	callType SuiCallType,
	payload interface{}) (*wire.MsgTx, error) {

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("sui_channel: failed to marshal "+
			"%s payload: %w", callType, err)
	}

	envelope := suiMoveCall{
		Type:    callType,
		Payload: json.RawMessage(payloadBytes),
	}
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("sui_channel: failed to marshal "+
			"call envelope: %w", err)
	}

	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  channelObjectID,
			Index: 0,
		},
		SignatureScript: envelopeBytes,
		Sequence:        wire.MaxTxInSequenceNum,
	})
	// Add a placeholder output.
	tx.AddTxOut(&wire.TxOut{
		Value:    0,
		PkScript: []byte{0x51}, // OP_1
	})

	return tx, nil
}

// DecodeSuiCallTx extracts the channel ObjectID and the raw call envelope.
func DecodeSuiCallTx(tx *wire.MsgTx) (
	channelObjectID chainhash.Hash,
	callType SuiCallType,
	rawPayload json.RawMessage,
	err error) {

	if tx == nil || len(tx.TxIn) == 0 {
		return channelObjectID, 0, nil,
			fmt.Errorf("sui_channel: tx has no inputs")
	}

	channelObjectID = tx.TxIn[0].PreviousOutPoint.Hash

	var envelope suiMoveCall
	if decErr := json.Unmarshal(tx.TxIn[0].SignatureScript, &envelope); decErr != nil {
		return channelObjectID, 0, nil,
			fmt.Errorf("sui_channel: failed to decode call "+
				"envelope: %w", decErr)
	}

	return channelObjectID, envelope.Type, envelope.Payload, nil
}

// -------------------------------------------------------------------------
// Convenience constructors – one per CallType
// -------------------------------------------------------------------------

// BuildChannelOpenTx creates the wire.MsgTx for a ChannelOpen call.
func BuildChannelOpenTx(
	channelObjectID chainhash.Hash,
	payload ChannelOpenPayload) (*wire.MsgTx, error) {

	return BuildSuiCallTx(channelObjectID, SuiCallChannelOpen, payload)
}

// BuildChannelCloseTx creates the wire.MsgTx for a close_channel call.
func BuildChannelCloseTx(
	channelObjectID chainhash.Hash,
	payload ChannelClosePayload) (*wire.MsgTx, error) {

	return BuildSuiCallTx(channelObjectID, SuiCallChannelClose, payload)
}

// BuildChannelForceCloseTx creates the wire.MsgTx for a force_close call.
func BuildChannelForceCloseTx(
	channelObjectID chainhash.Hash,
	payload ChannelForceClosePayload) (*wire.MsgTx, error) {

	return BuildSuiCallTx(
		channelObjectID, SuiCallChannelForceClose, payload,
	)
}

// BuildChannelClaimLocalTx creates the wire.MsgTx for claim_local_balance call.
func BuildChannelClaimLocalTx(
	channelObjectID chainhash.Hash,
	payload ChannelClaimLocalPayload) (*wire.MsgTx, error) {

	return BuildSuiCallTx(
		channelObjectID, SuiCallChannelClaimLocal, payload,
	)
}

// BuildChannelClaimRemoteTx creates the wire.MsgTx for claim_remote_balance call.
func BuildChannelClaimRemoteTx(
	channelObjectID chainhash.Hash,
	payload ChannelClaimRemotePayload) (*wire.MsgTx, error) {

	return BuildSuiCallTx(
		channelObjectID, SuiCallChannelClaimRemote, payload,
	)
}

// BuildHTLCClaimTx creates the wire.MsgTx for htlc_claim call.
func BuildHTLCClaimTx(
	channelObjectID chainhash.Hash,
	payload HTLCClaimPayload) (*wire.MsgTx, error) {

	return BuildSuiCallTx(channelObjectID, SuiCallHTLCClaim, payload)
}

// BuildHTLCTimeoutTx creates the wire.MsgTx for htlc_timeout call.
func BuildHTLCTimeoutTx(
	channelObjectID chainhash.Hash,
	payload HTLCTimeoutPayload) (*wire.MsgTx, error) {

	return BuildSuiCallTx(channelObjectID, SuiCallHTLCTimeout, payload)
}

// BuildChannelPenalizeTx creates the wire.MsgTx for the penalize call.
func BuildChannelPenalizeTx(
	channelObjectID chainhash.Hash,
	payload ChannelPenalizePayload) (*wire.MsgTx, error) {

	return BuildSuiCallTx(
		channelObjectID, SuiCallChannelPenalize, payload,
	)
}
