package suinotify

import (
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSuiClient is a test-only implementation of SuiClient.
type mockSuiClient struct {
	bestHeight uint32
	bestHash   chainhash.Hash

	// epochCh is the channel that the mock client sends epochs on.
	epochCh chan EpochEvent

	// confirmCh maps txID -> channel to fire on confirmation.
	confirmCh map[chainhash.Hash]chan ConfirmEvent

	// spendCh maps (objectID, htlcIndex) -> channel to fire on spend.
	spendCh map[wire.OutPoint]chan SpendEvent
}

func newMockSuiClient() *mockSuiClient {
	return &mockSuiClient{
		epochCh:   make(chan EpochEvent, 16),
		confirmCh: make(map[chainhash.Hash]chan ConfirmEvent),
		spendCh:   make(map[wire.OutPoint]chan SpendEvent),
	}
}

func (m *mockSuiClient) GetBestEpoch() (uint32, chainhash.Hash, error) {
	return m.bestHeight, m.bestHash, nil
}

func (m *mockSuiClient) SubscribeEpochs(
	quit <-chan struct{}) (<-chan EpochEvent, error) {

	out := make(chan EpochEvent, 16)
	go func() {
		for {
			select {
			case ev := <-m.epochCh:
				select {
				case out <- ev:
				case <-quit:
					return
				}
			case <-quit:
				return
			}
		}
	}()
	return out, nil
}

func (m *mockSuiClient) SubscribeEventConfirmation(
	txID chainhash.Hash, numConfs, heightHint uint32,
	quit <-chan struct{}) (<-chan ConfirmEvent, error) {

	ch := make(chan ConfirmEvent, 1)
	m.confirmCh[txID] = ch
	return ch, nil
}

func (m *mockSuiClient) SubscribeObjectSpend(
	objectID chainhash.Hash, htlcIndex uint32, heightHint uint32,
	quit <-chan struct{}) (<-chan SpendEvent, error) {

	ch := make(chan SpendEvent, 1)
	m.spendCh[wire.OutPoint{Hash: objectID, Index: htlcIndex}] = ch
	return ch, nil
}

// sendEpoch fires a mock checkpoint event.
func (m *mockSuiClient) sendEpoch(height uint32) {
	hash := heightToHash(height)
	m.epochCh <- EpochEvent{Height: height, Hash: hash}
}

// confirmEvent fires the confirmation for txID.
func (m *mockSuiClient) confirmEvent(txID chainhash.Hash, height uint32) {
	if ch, ok := m.confirmCh[txID]; ok {
		ch <- ConfirmEvent{TxID: txID, AnchorHeight: height}
	}
}

// spendObject fires the spend for (objectID, htlcIndex).
func (m *mockSuiClient) spendObject(
	objectID, spendTxID chainhash.Hash, htlcIndex, height uint32) {

	op := wire.OutPoint{Hash: objectID, Index: htlcIndex}
	if ch, ok := m.spendCh[op]; ok {
		ch <- SpendEvent{
			OutPoint:    op,
			SpendTxID:   spendTxID,
			SpendHeight: height,
		}
	}
}

// TestSuiChainNotifier_BlockEpochs verifies that checkpoint subscriptions receive
// notifications correctly.
func TestSuiChainNotifier_BlockEpochs(t *testing.T) {
	client := newMockSuiClient()
	notifier := New(client)

	require.NoError(t, notifier.Start())
	defer func() { require.NoError(t, notifier.Stop()) }()

	event, err := notifier.RegisterBlockEpochNtfn(nil)
	require.NoError(t, err)
	defer event.Cancel()

	const numEpochs = 5
	for i := uint32(1); i <= numEpochs; i++ {
		client.sendEpoch(i)
	}

	for i := int32(1); i <= numEpochs; i++ {
		select {
		case epoch := <-event.Epochs:
			assert.Equal(t, i, epoch.Height)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for checkpoint %d", i)
		}
	}
}

// TestSuiChainNotifier_Confirmations verifies that confirmation subscriptions
// fire when the mock client sends a ConfirmEvent.
func TestSuiChainNotifier_Confirmations(t *testing.T) {
	client := newMockSuiClient()
	notifier := New(client)

	require.NoError(t, notifier.Start())
	defer func() { require.NoError(t, notifier.Stop()) }()

	var txID chainhash.Hash
	txID[0] = 0xde
	txID[1] = 0xad

	event, err := notifier.RegisterConfirmationsNtfn(
		&txID, nil, 1, 0,
	)
	require.NoError(t, err)

	const confirmHeight uint32 = 42
	go client.confirmEvent(txID, confirmHeight)

	select {
	case conf := <-event.Confirmed:
		assert.Equal(t, confirmHeight, conf.BlockHeight)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for confirmation")
	}
}

// TestSuiChainNotifier_Spend verifies that a channel-level spend (htlcIndex=0)
// fires.
func TestSuiChainNotifier_Spend(t *testing.T) {
	client := newMockSuiClient()
	notifier := New(client)

	require.NoError(t, notifier.Start())
	defer func() { require.NoError(t, notifier.Stop()) }()

	var objectID chainhash.Hash
	objectID[0] = 0xca
	objectID[1] = 0xfe

	outpoint := &wire.OutPoint{Hash: objectID, Index: 0}

	event, err := notifier.RegisterSpendNtfn(outpoint, nil, 0)
	require.NoError(t, err)

	var spendTxID chainhash.Hash
	spendTxID[0] = 0xbb

	const spendHeight uint32 = 100
	go client.spendObject(objectID, spendTxID, 0, spendHeight)

	select {
	case detail := <-event.Spend:
		assert.Equal(t, &spendTxID, detail.SpenderTxHash)
		assert.Equal(t, int32(spendHeight), detail.SpendingHeight)
		assert.Equal(t, outpoint, detail.SpentOutPoint)
		assert.EqualValues(t, 0, detail.SpentOutPoint.Index)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for spend notification")
	}
}

// TestSuiChainNotifier_SpendHTLCIndex verifies that when LND registers with
// OutPoint.Index = N (an HTLC slot), the notifier subscribes to exactly that
// slot.
func TestSuiChainNotifier_SpendHTLCIndex(t *testing.T) {
	client := newMockSuiClient()
	notifier := New(client)

	require.NoError(t, notifier.Start())
	defer func() { require.NoError(t, notifier.Stop()) }()

	var objectID chainhash.Hash
	objectID[0] = 0xde
	objectID[1] = 0xad

	const htlcSlot uint32 = 3
	outpoint := &wire.OutPoint{Hash: objectID, Index: htlcSlot}

	event, err := notifier.RegisterSpendNtfn(outpoint, nil, 0)
	require.NoError(t, err)

	var spendTxID chainhash.Hash
	spendTxID[0] = 0xcc

	const spendHeight uint32 = 200
	go client.spendObject(objectID, spendTxID, htlcSlot, spendHeight)

	select {
	case detail := <-event.Spend:
		assert.Equal(t, &spendTxID, detail.SpenderTxHash)
		assert.Equal(t, int32(spendHeight), detail.SpendingHeight)
		assert.Equal(t, outpoint, detail.SpentOutPoint)
		assert.EqualValues(t, htlcSlot, detail.SpentOutPoint.Index)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HTLC spend notification")
	}
}

// TestSuiChainNotifier_NilTxID verifies that a nil txid returns a valid event.
func TestSuiChainNotifier_NilTxID(t *testing.T) {
	client := newMockSuiClient()
	notifier := New(client)

	require.NoError(t, notifier.Start())
	defer func() { require.NoError(t, notifier.Stop()) }()

	event, err := notifier.RegisterConfirmationsNtfn(nil, nil, 1, 0)
	require.NoError(t, err)
	assert.NotNil(t, event)
}

// TestSuiChainNotifier_NilOutpoint ensures a nil outpoint doesn't panic.
func TestSuiChainNotifier_NilOutpoint(t *testing.T) {
	client := newMockSuiClient()
	notifier := New(client)

	require.NoError(t, notifier.Start())
	defer func() { require.NoError(t, notifier.Stop()) }()

	event, err := notifier.RegisterSpendNtfn(nil, nil, 0)
	require.NoError(t, err)
	assert.NotNil(t, event)
}

// TestSuiChainNotifier_MissedEpochs ensures catch-up notifications are
// delivered.
func TestSuiChainNotifier_MissedEpochs(t *testing.T) {
	client := newMockSuiClient()
	client.bestHeight = 10

	notifier := New(client)

	require.NoError(t, notifier.Start())
	defer func() { require.NoError(t, notifier.Stop()) }()

	bestBlock := &chainntnfs.BlockEpoch{Height: 5}
	event, err := notifier.RegisterBlockEpochNtfn(bestBlock)
	require.NoError(t, err)
	defer event.Cancel()

	for i := int32(6); i <= 10; i++ {
		select {
		case epoch := <-event.Epochs:
			assert.Equal(t, i, epoch.Height)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for missed checkpoint %d", i)
		}
	}
}
