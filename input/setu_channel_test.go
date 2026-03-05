package input

import (
	"encoding/json"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/require"
)

// testObjectID returns a deterministic channel ObjectID for use in tests.
func testObjectID(t *testing.T) chainhash.Hash {
	t.Helper()
	h, err := chainhash.NewHashFromStr(
		"aabbccddeeff00112233445566778899aabbccddeeff001122334455667788aa",
	)
	require.NoError(t, err)
	return *h
}

// TestSetuEventTypeString verifies that every defined SetuEventType has a
// non-generic string representation.
func TestSetuEventTypeString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		et   SetuEventType
		want string
	}{
		{SetuEventChannelOpen, "ChannelOpen"},
		{SetuEventChannelClose, "ChannelClose"},
		{SetuEventChannelForceClose, "ChannelForceClose"},
		{SetuEventChannelClaimLocal, "ChannelClaimLocal"},
		{SetuEventChannelClaimRemote, "ChannelClaimRemote"},
		{SetuEventHTLCClaim, "HTLCClaim"},
		{SetuEventHTLCTimeout, "HTLCTimeout"},
		{SetuEventChannelPenalize, "ChannelPenalize"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.et.String())
		})
	}
}

// TestSetuEventTypeStringUnknown verifies that an unknown SetuEventType falls
// back to the numeric form.
func TestSetuEventTypeStringUnknown(t *testing.T) {
	t.Parallel()

	unknown := SetuEventType(255)
	require.Contains(t, unknown.String(), "255")
}

// TestBuildDecodeRoundTrip verifies that BuildSetuEventTx and DecodeSetuEventTx
// form a lossless round-trip for every defined event type.
func TestBuildDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	objectID := testObjectID(t)

	cases := []struct {
		name      string
		eventType SetuEventType
		payload   interface{}
	}{
		{
			name:      "ChannelOpen",
			eventType: SetuEventChannelOpen,
			payload: ChannelOpenPayload{
				LocalKey:      "02aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff00000000111111112",
				RemoteKey:     "03bbbbbbbbccccccccddddddddeeeeeeeeffffffff000000001111111122222222",
				LocalBalance:  500_000,
				RemoteBalance: 500_000,
				CSVDelay:      144,
			},
		},
		{
			name:      "ChannelClose",
			eventType: SetuEventChannelClose,
			payload: ChannelClosePayload{
				StateNum:      42,
				LocalBalance:  600_000,
				RemoteBalance: 400_000,
				LocalSig:      []byte{0xaa, 0xbb},
				RemoteSig:     []byte{0xcc, 0xdd},
			},
		},
		{
			name:      "ChannelForceClose",
			eventType: SetuEventChannelForceClose,
			payload: ChannelForceClosePayload{
				StateNum:      10,
				CommitmentSig: []byte{0xde, 0xad, 0xbe, 0xef},
			},
		},
		{
			name:      "ChannelClaimLocal",
			eventType: SetuEventChannelClaimLocal,
			payload: ChannelClaimLocalPayload{
				Sig: []byte{0x01, 0x02, 0x03},
			},
		},
		{
			name:      "ChannelClaimRemote",
			eventType: SetuEventChannelClaimRemote,
			payload: ChannelClaimRemotePayload{
				Sig: []byte{0x04, 0x05, 0x06},
			},
		},
		{
			name:      "HTLCClaim",
			eventType: SetuEventHTLCClaim,
			payload: HTLCClaimPayload{
				HtlcID:      3,
				PaymentHash: [32]byte{0x11},
				Preimage:    [32]byte{0x22},
				Sig:         []byte{0x33, 0x44},
			},
		},
		{
			name:      "HTLCTimeout",
			eventType: SetuEventHTLCTimeout,
			payload: HTLCTimeoutPayload{
				HtlcID:      5,
				PaymentHash: [32]byte{0x55},
				Sig:         []byte{0x66, 0x77},
			},
		},
		{
			name:      "ChannelPenalize",
			eventType: SetuEventChannelPenalize,
			payload: ChannelPenalizePayload{
				RevocationKey:  []byte{0xaa, 0xbb, 0xcc},
				BreachStateNum: 7,
				Sig:            []byte{0xdd, 0xee},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tx, err := BuildSetuEventTx(objectID, tc.eventType, tc.payload)
			require.NoError(t, err)
			require.NotNil(t, tx)

			// Basic wire.MsgTx shape.
			require.Len(t, tx.TxIn, 1)
			require.Len(t, tx.TxOut, 1)
			require.Equal(t, objectID, tx.TxIn[0].PreviousOutPoint.Hash)
			require.EqualValues(t, 0, tx.TxIn[0].PreviousOutPoint.Index)

			// Decode round-trip.
			gotID, gotType, gotRaw, err := DecodeSetuEventTx(tx)
			require.NoError(t, err)
			require.Equal(t, objectID, gotID)
			require.Equal(t, tc.eventType, gotType)

			// Raw payload should re-marshal to the same JSON.
			wantRaw, err := json.Marshal(tc.payload)
			require.NoError(t, err)
			require.JSONEq(t, string(wantRaw), string(gotRaw))
		})
	}
}

// TestDecodeSetuEventTxNilTx verifies that DecodeSetuEventTx returns an error
// when given a nil transaction.
func TestDecodeSetuEventTxNilTx(t *testing.T) {
	t.Parallel()

	_, _, _, err := DecodeSetuEventTx(nil)
	require.Error(t, err)
}

// TestDecodeSetuEventTxNoInputs verifies that DecodeSetuEventTx returns an
// error when the transaction has no inputs.
func TestDecodeSetuEventTxNoInputs(t *testing.T) {
	t.Parallel()

	tx, err := BuildSetuEventTx(
		testObjectID(t),
		SetuEventChannelOpen,
		ChannelOpenPayload{},
	)
	require.NoError(t, err)
	tx.TxIn = nil

	_, _, _, err = DecodeSetuEventTx(tx)
	require.Error(t, err)
}

// TestDecodeSetuEventTxGarbledScript verifies that DecodeSetuEventTx returns
// an error when the signature script is not valid JSON.
func TestDecodeSetuEventTxGarbledScript(t *testing.T) {
	t.Parallel()

	tx, err := BuildSetuEventTx(
		testObjectID(t),
		SetuEventChannelOpen,
		ChannelOpenPayload{},
	)
	require.NoError(t, err)
	tx.TxIn[0].SignatureScript = []byte("not-valid-json")

	_, _, _, err = DecodeSetuEventTx(tx)
	require.Error(t, err)
}

// TestConvenienceConstructors verifies each per-EventType constructor helper
// against the generic BuildSetuEventTx result.
func TestConvenienceConstructors(t *testing.T) {
	t.Parallel()

	objectID := testObjectID(t)

	t.Run("BuildChannelOpenTx", func(t *testing.T) {
		t.Parallel()
		p := ChannelOpenPayload{LocalKey: "abc", CSVDelay: 144}
		tx, err := BuildChannelOpenTx(objectID, p)
		require.NoError(t, err)
		_, gotType, _, err := DecodeSetuEventTx(tx)
		require.NoError(t, err)
		require.Equal(t, SetuEventChannelOpen, gotType)
	})

	t.Run("BuildChannelCloseTx", func(t *testing.T) {
		t.Parallel()
		p := ChannelClosePayload{StateNum: 1}
		tx, err := BuildChannelCloseTx(objectID, p)
		require.NoError(t, err)
		_, gotType, _, err := DecodeSetuEventTx(tx)
		require.NoError(t, err)
		require.Equal(t, SetuEventChannelClose, gotType)
	})

	t.Run("BuildChannelForceCloseTx", func(t *testing.T) {
		t.Parallel()
		p := ChannelForceClosePayload{StateNum: 2}
		tx, err := BuildChannelForceCloseTx(objectID, p)
		require.NoError(t, err)
		_, gotType, _, err := DecodeSetuEventTx(tx)
		require.NoError(t, err)
		require.Equal(t, SetuEventChannelForceClose, gotType)
	})

	t.Run("BuildChannelClaimLocalTx", func(t *testing.T) {
		t.Parallel()
		p := ChannelClaimLocalPayload{Sig: []byte{0x01}}
		tx, err := BuildChannelClaimLocalTx(objectID, p)
		require.NoError(t, err)
		_, gotType, _, err := DecodeSetuEventTx(tx)
		require.NoError(t, err)
		require.Equal(t, SetuEventChannelClaimLocal, gotType)
	})

	t.Run("BuildChannelClaimRemoteTx", func(t *testing.T) {
		t.Parallel()
		p := ChannelClaimRemotePayload{Sig: []byte{0x02}}
		tx, err := BuildChannelClaimRemoteTx(objectID, p)
		require.NoError(t, err)
		_, gotType, _, err := DecodeSetuEventTx(tx)
		require.NoError(t, err)
		require.Equal(t, SetuEventChannelClaimRemote, gotType)
	})

	t.Run("BuildHTLCClaimTx", func(t *testing.T) {
		t.Parallel()
		p := HTLCClaimPayload{HtlcID: 1, PaymentHash: [32]byte{1}, Preimage: [32]byte{2}}
		tx, err := BuildHTLCClaimTx(objectID, p)
		require.NoError(t, err)
		_, gotType, _, err := DecodeSetuEventTx(tx)
		require.NoError(t, err)
		require.Equal(t, SetuEventHTLCClaim, gotType)
	})

	t.Run("BuildHTLCTimeoutTx", func(t *testing.T) {
		t.Parallel()
		p := HTLCTimeoutPayload{HtlcID: 2, PaymentHash: [32]byte{3}}
		tx, err := BuildHTLCTimeoutTx(objectID, p)
		require.NoError(t, err)
		_, gotType, _, err := DecodeSetuEventTx(tx)
		require.NoError(t, err)
		require.Equal(t, SetuEventHTLCTimeout, gotType)
	})

	t.Run("BuildChannelPenalizeTx", func(t *testing.T) {
		t.Parallel()
		p := ChannelPenalizePayload{BreachStateNum: 5, RevocationKey: []byte{0xff}}
		tx, err := BuildChannelPenalizeTx(objectID, p)
		require.NoError(t, err)
		_, gotType, _, err := DecodeSetuEventTx(tx)
		require.NoError(t, err)
		require.Equal(t, SetuEventChannelPenalize, gotType)
	})
}
