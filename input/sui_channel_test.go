package input

import (
	"encoding/json"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/require"
)

// TestSuiCallTypeString verifies that every defined SuiCallType has a
// correct string representation mapping to its Move function name.
func TestSuiCallTypeString(t *testing.T) {
	tests := []struct {
		ct   SuiCallType
		name string
	}{
		{SuiCallChannelOpen, "open_channel"},
		{SuiCallChannelClose, "close_channel"},
		{SuiCallChannelForceClose, "force_close"},
		{SuiCallChannelClaimLocal, "claim_local_balance"},
		{SuiCallChannelClaimRemote, "claim_remote_balance"},
		{SuiCallHTLCClaim, "htlc_claim"},
		{SuiCallHTLCTimeout, "htlc_timeout"},
		{SuiCallChannelPenalize, "penalize"},
	}

	for _, tc := range tests {
		require.Equal(t, tc.name, tc.ct.String())
	}
}

// TestSuiCallTypeStringUnknown verifies that an unknown SuiCallType falls
// back to a numeric format.
func TestSuiCallTypeStringUnknown(t *testing.T) {
	unknown := SuiCallType(255)
	require.Equal(t, "SuiCallType(255)", unknown.String())
}

// TestBuildDecodeRoundTrip verifies that BuildSuiCallTx and DecodeSuiCallTx
// correctly preserve the ObjectID, call type, and JSON payload.
func TestBuildDecodeRoundTrip(t *testing.T) {
	var objectID chainhash.Hash
	objectID[0] = 0xca
	objectID[1] = 0xfe

	tests := []struct {
		name     string
		callType SuiCallType
		payload  interface{}
	}{
		{
			name:     "ChannelOpen",
			callType: SuiCallChannelOpen,
			payload: ChannelOpenPayload{
				LocalKey:      "02deadbeef",
				RemoteKey:     "03cafebabe",
				LocalBalance:  1000,
				RemoteBalance: 2000,
				CSVDelay:      144,
			},
		},
		{
			name:     "ChannelClose",
			callType: SuiCallChannelClose,
			payload: ChannelClosePayload{
				StateNum:      42,
				LocalBalance:  500,
				RemoteBalance: 2500,
				LocalSig:      []byte{0x01, 0x02},
				RemoteSig:     []byte{0x03, 0x04},
			},
		},
		{
			name:     "ChannelForceClose",
			callType: SuiCallChannelForceClose,
			payload: ChannelForceClosePayload{
				StateNum:      10,
				CommitmentSig: []byte{0xaa, 0xbb},
			},
		},
		{
			name:     "ChannelClaimLocal",
			callType: SuiCallChannelClaimLocal,
			payload: ChannelClaimLocalPayload{
				Sig: []byte{0xcc, 0xdd},
			},
		},
		{
			name:     "ChannelClaimRemote",
			callType: SuiCallChannelClaimRemote,
			payload: ChannelClaimRemotePayload{
				Sig: []byte{0xee, 0xff},
			},
		},
		{
			name:     "HTLCClaim",
			callType: SuiCallHTLCClaim,
			payload: HTLCClaimPayload{
				HtlcID:      3,
				PaymentHash: [32]byte{0x11},
				Preimage:    [32]byte{0x22},
				Sig:         []byte{0x33},
			},
		},
		{
			name:     "HTLCTimeout",
			callType: SuiCallHTLCTimeout,
			payload: HTLCTimeoutPayload{
				HtlcID:      5,
				PaymentHash: [32]byte{0x44},
				Sig:         []byte{0x55},
			},
		},
		{
			name:     "ChannelPenalize",
			callType: SuiCallChannelPenalize,
			payload: ChannelPenalizePayload{
				RevocationKey:  []byte{0x66},
				BreachStateNum: 7,
				Sig:            []byte{0x77},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := BuildSuiCallTx(objectID, tc.callType, tc.payload)
			require.NoError(t, err)

			// Basic structure checks
			require.Len(t, tx.TxIn, 1)
			require.Equal(t, objectID, tx.TxIn[0].PreviousOutPoint.Hash)
			require.EqualValues(t, 0, tx.TxIn[0].PreviousOutPoint.Index)

			gotID, gotType, gotRaw, err := DecodeSuiCallTx(tx)
			require.NoError(t, err)
			require.Equal(t, objectID, gotID)
			require.Equal(t, tc.callType, gotType)

			// Round-trip the payload through JSON to verify content
			wantRaw, _ := json.Marshal(tc.payload)
			require.JSONEq(t, string(wantRaw), string(gotRaw))
		})
	}
}

// TestDecodeSuiCallTxNilTx verifies that DecodeSuiCallTx returns an error for
// a nil transaction.
func TestDecodeSuiCallTxNilTx(t *testing.T) {
	_, _, _, err := DecodeSuiCallTx(nil)
	require.Error(t, err)
}

// TestDecodeSuiCallTxNoInputs verifies that DecodeSuiCallTx returns an
// error for a transaction with no inputs.
func TestDecodeSuiCallTxNoInputs(t *testing.T) {
	tx, err := BuildSuiCallTx(
		chainhash.Hash{},
		SuiCallChannelOpen,
		ChannelOpenPayload{},
	)
	require.NoError(t, err)
	tx.TxIn = nil

	_, _, _, err = DecodeSuiCallTx(tx)
	require.Error(t, err)
}

// TestDecodeSuiCallTxGarbledScript verifies that DecodeSuiCallTx returns
// an error if the SignatureScript is not valid JSON.
func TestDecodeSuiCallTxGarbledScript(t *testing.T) {
	tx, err := BuildSuiCallTx(
		chainhash.Hash{},
		SuiCallChannelOpen,
		ChannelOpenPayload{},
	)
	require.NoError(t, err)
	tx.TxIn[0].SignatureScript = []byte("not-json")

	_, _, _, err = DecodeSuiCallTx(tx)
	require.Error(t, err)
}

// TestConvenienceConstructors verifies that each named constructor dispatches
// correctly.
func TestConvenienceConstructors(t *testing.T) {
	objectID := chainhash.Hash{0xaa}

	t.Run("BuildChannelOpenTx", func(t *testing.T) {
		tx, err := BuildChannelOpenTx(objectID, ChannelOpenPayload{})
		require.NoError(t, err)
		_, gotType, _, err := DecodeSuiCallTx(tx)
		require.NoError(t, err)
		require.Equal(t, SuiCallChannelOpen, gotType)
	})

	t.Run("BuildChannelCloseTx", func(t *testing.T) {
		tx, err := BuildChannelCloseTx(objectID, ChannelClosePayload{})
		require.NoError(t, err)
		_, gotType, _, err := DecodeSuiCallTx(tx)
		require.NoError(t, err)
		require.Equal(t, SuiCallChannelClose, gotType)
	})

	t.Run("BuildChannelForceCloseTx", func(t *testing.T) {
		tx, err := BuildChannelForceCloseTx(objectID, ChannelForceClosePayload{})
		require.NoError(t, err)
		_, gotType, _, err := DecodeSuiCallTx(tx)
		require.NoError(t, err)
		require.Equal(t, SuiCallChannelForceClose, gotType)
	})

	t.Run("BuildChannelClaimLocalTx", func(t *testing.T) {
		tx, err := BuildChannelClaimLocalTx(objectID, ChannelClaimLocalPayload{})
		require.NoError(t, err)
		_, gotType, _, err := DecodeSuiCallTx(tx)
		require.NoError(t, err)
		require.Equal(t, SuiCallChannelClaimLocal, gotType)
	})

	t.Run("BuildChannelClaimRemoteTx", func(t *testing.T) {
		tx, err := BuildChannelClaimRemoteTx(objectID, ChannelClaimRemotePayload{})
		require.NoError(t, err)
		_, gotType, _, err := DecodeSuiCallTx(tx)
		require.NoError(t, err)
		require.Equal(t, SuiCallChannelClaimRemote, gotType)
	})

	t.Run("BuildHTLCClaimTx", func(t *testing.T) {
		tx, err := BuildHTLCClaimTx(objectID, HTLCClaimPayload{})
		require.NoError(t, err)
		_, gotType, _, err := DecodeSuiCallTx(tx)
		require.NoError(t, err)
		require.Equal(t, SuiCallHTLCClaim, gotType)
	})

	t.Run("BuildHTLCTimeoutTx", func(t *testing.T) {
		tx, err := BuildHTLCTimeoutTx(objectID, HTLCTimeoutPayload{})
		require.NoError(t, err)
		_, gotType, _, err := DecodeSuiCallTx(tx)
		require.NoError(t, err)
		require.Equal(t, SuiCallHTLCTimeout, gotType)
	})

	t.Run("BuildChannelPenalizeTx", func(t *testing.T) {
		tx, err := BuildChannelPenalizeTx(objectID, ChannelPenalizePayload{})
		require.NoError(t, err)
		_, gotType, _, err := DecodeSuiCallTx(tx)
		require.NoError(t, err)
		require.Equal(t, SuiCallChannelPenalize, gotType)
	})
}
