// Package input contains Bitcoin input script construction utilities.  This
// file provides the Setu DAG equivalent: functions that construct Setu Channel
// Events instead of Bitcoin Scripts.
//
// Mapping (Bitcoin Script → Setu hardcoded EventType):
//
//	OP_2 <key1> <key2> OP_2 OP_CHECKMULTISIG  →  EventType::ChannelOpen
//	HTLC hash-lock                             →  EventType::HTLCClaim
//	OP_CSV relative time-lock                  →  EventType::ChannelClaimLocal
//	OP_CLTV absolute time-lock                 →  EventType::HTLCTimeout
//	Revocation / breach-remedy                 →  EventType::ChannelPenalize
//
// Each Setu Event is serialised to JSON and packed into the SignatureScript
// field of the first TxIn of a wire.MsgTx wrapper.  The OutPoint.Hash of that
// TxIn carries the Setu ObjectID (channel identifier).
//
// Reference: 1-refactor-docs/lnd-and-setu-integration.md §6
package input

import (
	"encoding/json"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// SetuEventType is an enum that identifies the hardcoded Lightning Channel
// operation types in the Setu RuntimeExecutor.
type SetuEventType uint8

const (
	// SetuEventChannelOpen creates a new ChannelObject (SharedObject) on
	// the Setu DAG.  Analogous to the Bitcoin 2-of-2 multisig funding
	// transaction.
	SetuEventChannelOpen SetuEventType = iota

	// SetuEventChannelClose performs a cooperative close, distributing
	// balances to both parties based on the latest mutually-signed state.
	// Analogous to a Bitcoin cooperative-close transaction.
	SetuEventChannelClose

	// SetuEventChannelForceClose triggers a unilateral force-close.  The
	// ChannelObject enters CLOSING state and records the current VLC tick
	// as close_vlc.  Analogous to broadcasting a commitment transaction.
	SetuEventChannelForceClose

	// SetuEventChannelClaimLocal allows the force-closing party to claim
	// its local balance once the relative VLC delay (csv_delay) has
	// elapsed.  Analogous to the to_local CSV-locked output.
	SetuEventChannelClaimLocal

	// SetuEventChannelClaimRemote allows the non-closing party to claim
	// its balance immediately after a force-close.  Analogous to the
	// to_remote output.
	SetuEventChannelClaimRemote

	// SetuEventHTLCClaim allows a recipient to claim an HTLC by revealing
	// the payment preimage.  The Setu executor verifies SHA256(preimage) ==
	// payment_hash before releasing the locked amount.  Analogous to the
	// HTLC-success (hash-lock) script path.
	SetuEventHTLCClaim

	// SetuEventHTLCTimeout allows the sender to reclaim an HTLC after its
	// absolute VLC expiry has elapsed and the recipient has not claimed it.
	// Analogous to the HTLC-timeout (CLTV) script path.
	SetuEventHTLCTimeout

	// SetuEventChannelPenalize is the breach-remedy operation.  When a
	// party broadcasts a revoked state, the honest counterparty submits the
	// revocation key and current state proof to seize all channel funds.
	// Analogous to the Bitcoin justice transaction.
	SetuEventChannelPenalize
)

// String returns the human-readable name of the event type.
func (t SetuEventType) String() string {
	switch t {
	case SetuEventChannelOpen:
		return "ChannelOpen"
	case SetuEventChannelClose:
		return "ChannelClose"
	case SetuEventChannelForceClose:
		return "ChannelForceClose"
	case SetuEventChannelClaimLocal:
		return "ChannelClaimLocal"
	case SetuEventChannelClaimRemote:
		return "ChannelClaimRemote"
	case SetuEventHTLCClaim:
		return "HTLCClaim"
	case SetuEventHTLCTimeout:
		return "HTLCTimeout"
	case SetuEventChannelPenalize:
		return "ChannelPenalize"
	default:
		return fmt.Sprintf("SetuEventType(%d)", uint8(t))
	}
}

// -------------------------------------------------------------------------
// Payload structs — one per EventType.
// These mirror the fields consumed by the Setu RuntimeExecutor.
// -------------------------------------------------------------------------

// ChannelOpenPayload is the payload for SetuEventChannelOpen.
// It carries both parties' public keys and their initial balances.
type ChannelOpenPayload struct {
	// LocalKey is the local party's secp256k1 public key (compressed,
	// 33 bytes, hex-encoded).
	LocalKey string `json:"local_key"`

	// RemoteKey is the remote party's secp256k1 public key.
	RemoteKey string `json:"remote_key"`

	// LocalBalance is the initial balance (in Setu minimum units) for the
	// local party.
	LocalBalance uint64 `json:"local_balance"`

	// RemoteBalance is the initial balance for the remote party.
	RemoteBalance uint64 `json:"remote_balance"`

	// CSVDelay is the relative VLC tick delay applied to to_local outputs
	// on force-close.  Analogous to OP_CSV.
	CSVDelay uint64 `json:"csv_delay"`
}

// ChannelClosePayload is the payload for SetuEventChannelClose.
type ChannelClosePayload struct {
	// StateNum is the monotonically-increasing state counter.  Must match
	// or exceed the on-chain value to be accepted.
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

// ChannelForceClosePayload is the payload for SetuEventChannelForceClose.
type ChannelForceClosePayload struct {
	// StateNum is the commitment state number being force-closed.
	StateNum uint64 `json:"state_num"`

	// CommitmentSig is the owner's signature over this commitment state.
	CommitmentSig []byte `json:"commitment_sig"`
}

// ChannelClaimLocalPayload is the payload for SetuEventChannelClaimLocal.
// It is submitted after the CSV delay has elapsed.
type ChannelClaimLocalPayload struct {
	// Sig is the claimer's signature authorising the sweep of the
	// to_local output.
	Sig []byte `json:"sig"`
}

// ChannelClaimRemotePayload is the payload for SetuEventChannelClaimRemote.
// The remote party can claim immediately after a force-close.
type ChannelClaimRemotePayload struct {
	// Sig is the remote party's signature.
	Sig []byte `json:"sig"`
}

// HTLCClaimPayload is the payload for SetuEventHTLCClaim.
// The recipient reveals the payment preimage to claim the HTLC.
type HTLCClaimPayload struct {
	// HtlcID is the slot index of this HTLC inside ChannelObject.htlcs[].
	// It maps to wire.OutPoint.Index so that contractcourt can correlate
	// the on-chain spend event back to the correct resolver.
	HtlcID uint64 `json:"htlc_id"`

	// PaymentHash is the 32-byte SHA256 hash of the preimage.
	PaymentHash [32]byte `json:"payment_hash"`

	// Preimage is the 32-byte secret whose hash equals PaymentHash.
	Preimage [32]byte `json:"preimage"`

	// Sig is the recipient's signature.
	Sig []byte `json:"sig"`
}

// HTLCTimeoutPayload is the payload for SetuEventHTLCTimeout.
// The sender reclaims the HTLC after the absolute VLC expiry.
type HTLCTimeoutPayload struct {
	// HtlcID is the slot index of this HTLC inside ChannelObject.htlcs[].
	// It maps to wire.OutPoint.Index so that contractcourt can correlate
	// the on-chain spend event back to the correct resolver.
	HtlcID uint64 `json:"htlc_id"`

	// PaymentHash identifies the HTLC being timed out.
	PaymentHash [32]byte `json:"payment_hash"`

	// Sig is the sender's signature.
	Sig []byte `json:"sig"`
}

// ChannelPenalizePayload is the payload for SetuEventChannelPenalize.
// The honest party submits the revocation key to seize all funds after a
// breach (broadcasting of a revoked commitment state).
type ChannelPenalizePayload struct {
	// RevocationKey is the revocation private key derived from the
	// per-commitment secret and the base revocation point.
	RevocationKey []byte `json:"revocation_key"`

	// BreachStateNum is the state_num of the revoked commitment that was
	// broadcast.
	BreachStateNum uint64 `json:"breach_state_num"`

	// Sig is the honest party's signature over the penalise payload.
	Sig []byte `json:"sig"`
}

// -------------------------------------------------------------------------
// setuEvent is the on-wire envelope packed into wire.MsgTx.SignatureScript.
// -------------------------------------------------------------------------

type setuEvent struct {
	// Type is the event type identifier.
	Type SetuEventType `json:"type"`

	// Payload is the raw JSON-encoded payload for the specific event type.
	Payload json.RawMessage `json:"payload"`
}

// -------------------------------------------------------------------------
// Helper: wrap a Setu event into a wire.MsgTx
// -------------------------------------------------------------------------

// BuildSetuEventTx creates a wire.MsgTx wrapper that carries a serialised Setu
// Channel Event.  The wire.MsgTx is used as the opaque transport understood by
// the rest of LND (PublishTransaction, etc.) without modifying any existing
// function signatures.
//
// Layout:
//   - TxIn[0].PreviousOutPoint.Hash = channelObjectID (32-byte Setu ObjectID)
//   - TxIn[0].SignatureScript       = JSON(setuEvent{Type, Payload})
//
// The ObjectID stored in hash form maps directly to wire.OutPoint.Hash as
// specified in the zero-intrusion adapter type mapping.
func BuildSetuEventTx(
	channelObjectID chainhash.Hash,
	eventType SetuEventType,
	payload interface{}) (*wire.MsgTx, error) {

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("setu_channel: failed to marshal "+
			"%s payload: %w", eventType, err)
	}

	envelope := setuEvent{
		Type:    eventType,
		Payload: json.RawMessage(payloadBytes),
	}
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("setu_channel: failed to marshal "+
			"event envelope: %w", err)
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
	// Add a dust-value output so the transaction passes basic validity
	// checks in the LND wallet layer.
	tx.AddTxOut(&wire.TxOut{
		Value:    0,
		PkScript: []byte{0x51}, // OP_1 — placeholder; Setu ignores it
	})

	return tx, nil
}

// DecodeSetuEventTx extracts the channel ObjectID and the raw event envelope
// from a wire.MsgTx that was created by BuildSetuEventTx.
func DecodeSetuEventTx(tx *wire.MsgTx) (
	channelObjectID chainhash.Hash,
	eventType SetuEventType,
	rawPayload json.RawMessage,
	err error) {

	if tx == nil || len(tx.TxIn) == 0 {
		return channelObjectID, 0, nil,
			fmt.Errorf("setu_channel: tx has no inputs")
	}

	channelObjectID = tx.TxIn[0].PreviousOutPoint.Hash

	var envelope setuEvent
	if decErr := json.Unmarshal(tx.TxIn[0].SignatureScript, &envelope); decErr != nil {
		return channelObjectID, 0, nil,
			fmt.Errorf("setu_channel: failed to decode event "+
				"envelope: %w", decErr)
	}

	return channelObjectID, envelope.Type, envelope.Payload, nil
}

// -------------------------------------------------------------------------
// Convenience constructors – one per EventType
// -------------------------------------------------------------------------

// BuildChannelOpenTx creates the wire.MsgTx for a ChannelOpen event.
func BuildChannelOpenTx(
	channelObjectID chainhash.Hash,
	payload ChannelOpenPayload) (*wire.MsgTx, error) {

	return BuildSetuEventTx(channelObjectID, SetuEventChannelOpen, payload)
}

// BuildChannelCloseTx creates the wire.MsgTx for a cooperative ChannelClose
// event.
func BuildChannelCloseTx(
	channelObjectID chainhash.Hash,
	payload ChannelClosePayload) (*wire.MsgTx, error) {

	return BuildSetuEventTx(channelObjectID, SetuEventChannelClose, payload)
}

// BuildChannelForceCloseTx creates the wire.MsgTx for a unilateral
// ChannelForceClose event.
func BuildChannelForceCloseTx(
	channelObjectID chainhash.Hash,
	payload ChannelForceClosePayload) (*wire.MsgTx, error) {

	return BuildSetuEventTx(
		channelObjectID, SetuEventChannelForceClose, payload,
	)
}

// BuildChannelClaimLocalTx creates the wire.MsgTx for claiming the to_local
// output after the CSV delay.
func BuildChannelClaimLocalTx(
	channelObjectID chainhash.Hash,
	payload ChannelClaimLocalPayload) (*wire.MsgTx, error) {

	return BuildSetuEventTx(
		channelObjectID, SetuEventChannelClaimLocal, payload,
	)
}

// BuildChannelClaimRemoteTx creates the wire.MsgTx for claiming the to_remote
// output immediately after a force-close.
func BuildChannelClaimRemoteTx(
	channelObjectID chainhash.Hash,
	payload ChannelClaimRemotePayload) (*wire.MsgTx, error) {

	return BuildSetuEventTx(
		channelObjectID, SetuEventChannelClaimRemote, payload,
	)
}

// BuildHTLCClaimTx creates the wire.MsgTx for claiming an HTLC by revealing
// the payment preimage.
func BuildHTLCClaimTx(
	channelObjectID chainhash.Hash,
	payload HTLCClaimPayload) (*wire.MsgTx, error) {

	return BuildSetuEventTx(channelObjectID, SetuEventHTLCClaim, payload)
}

// BuildHTLCTimeoutTx creates the wire.MsgTx for timing-out an unclaimed HTLC
// after its absolute VLC expiry.
func BuildHTLCTimeoutTx(
	channelObjectID chainhash.Hash,
	payload HTLCTimeoutPayload) (*wire.MsgTx, error) {

	return BuildSetuEventTx(channelObjectID, SetuEventHTLCTimeout, payload)
}

// BuildChannelPenalizeTx creates the wire.MsgTx for the breach-remedy
// (penalise) operation.
func BuildChannelPenalizeTx(
	channelObjectID chainhash.Hash,
	payload ChannelPenalizePayload) (*wire.MsgTx, error) {

	return BuildSetuEventTx(
		channelObjectID, SetuEventChannelPenalize, payload,
	)
}
