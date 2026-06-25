package evmwallet

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/require"
)

// TestListTransactionDetailsJournal checks the in-memory broadcast journal:
// an empty journal yields no details, and a recorded tx is reported with its
// on-chain block height and confirmation depth (derived from the client tip).
func TestListTransactionDetailsJournal(t *testing.T) {
	t.Parallel()

	c := &capturingClient{} // BlockNumber 1, receipts mined at block 1
	w := &Wallet{cfg: Config{Client: c}}

	// Empty journal → no history, no error.
	details, _, _, err := w.ListTransactionDetails(0, 0, "", 0, 0)
	require.NoError(t, err)
	require.Empty(t, details)

	var h chainhash.Hash
	h[0], h[1] = 0xab, 0xcd
	w.recordTx(h)

	details, tip, _, err := w.ListTransactionDetails(0, 0, "", 0, 0)
	require.NoError(t, err)
	require.Len(t, details, 1)
	require.Equal(t, h, details[0].Hash)
	require.Equal(t, int32(1), details[0].BlockHeight)
	require.Equal(t, int32(1), details[0].NumConfirmations)
	require.Equal(t, "evm-node-tx", details[0].Label)
	require.Equal(t, uint64(1), tip)

	// A startHeight above the tx's block filters it out.
	details, _, _, err = w.ListTransactionDetails(2, 0, "", 0, 0)
	require.NoError(t, err)
	require.Empty(t, details)
}

// TestRecordTxBounded checks the journal is capped at maxTxJournal (newest kept).
func TestRecordTxBounded(t *testing.T) {
	t.Parallel()

	w := &Wallet{}
	for i := 0; i < maxTxJournal+50; i++ {
		var h chainhash.Hash
		h[0] = byte(i)
		h[1] = byte(i >> 8)
		w.recordTx(h)
	}
	require.Len(t, w.txJournal, maxTxJournal)
}
