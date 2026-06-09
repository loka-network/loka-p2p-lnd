package evmnotify

import (
	"context"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// mockClient is a minimal EvmClient for notifier tests.
type mockClient struct {
	mu      sync.Mutex
	height  int64
	logs    []types.Log
	receipt *types.Receipt
}

func (m *mockClient) setHeight(h int64) {
	m.mu.Lock()
	m.height = h
	m.mu.Unlock()
}

func (m *mockClient) HeaderByNumber(_ context.Context, _ *big.Int) (
	*types.Header, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	return &types.Header{Number: big.NewInt(m.height)}, nil
}

func (m *mockClient) FilterLogs(_ context.Context, _ ethereum.FilterQuery) (
	[]types.Log, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.logs, nil
}

func (m *mockClient) TransactionReceipt(_ context.Context, _ common.Hash) (
	*types.Receipt, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.receipt, nil
}

func (m *mockClient) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(31337), nil
}
func (m *mockClient) BlockNumber(context.Context) (uint64, error) {
	return 0, nil
}
func (m *mockClient) CallContract(context.Context, ethereum.CallMsg,
	*big.Int) ([]byte, error) {
	return nil, nil
}
func (m *mockClient) PendingNonceAt(context.Context, common.Address) (uint64,
	error) {
	return 0, nil
}
func (m *mockClient) SuggestGasPrice(context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}
func (m *mockClient) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}
func (m *mockClient) SendTransaction(context.Context, *types.Transaction) error {
	return nil
}
func (m *mockClient) SubscribeFilterLogs(context.Context, ethereum.FilterQuery,
	chan<- types.Log) (ethereum.Subscription, error) {
	return nil, nil
}
func (m *mockClient) Close() {}

func newTestNotifier(c EvmClient) *EvmChainNotifier {
	n := New(c, common.HexToAddress("0x1"))
	n.pollInterval = 10 * time.Millisecond

	return n
}

func TestBlockEpochDispatch(t *testing.T) {
	t.Parallel()

	mc := &mockClient{height: 100}
	n := newTestNotifier(mc)
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Stop() }()

	ev, err := n.RegisterBlockEpochNtfn(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Cancel()

	mc.setHeight(101)

	select {
	case epoch := <-ev.Epochs:
		if epoch.Height != 101 {
			t.Fatalf("epoch height = %d, want 101", epoch.Height)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for block epoch")
	}
}

func TestSpendDetectionViaLog(t *testing.T) {
	t.Parallel()

	var channelID chainhash.Hash
	channelID[0] = 0xab

	// A ChannelClosed log referencing channelId in topics[1].
	mc := &mockClient{
		height: 50,
		logs: []types.Log{{
			Topics: []common.Hash{
				TopicChannelClosed, common.Hash(channelID),
			},
			TxHash:      common.Hash{0xcd},
			BlockNumber: 42,
		}},
	}
	n := newTestNotifier(mc)
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Stop() }()

	outpoint := &wire.OutPoint{Hash: channelID, Index: 0}
	ev, err := n.RegisterSpendNtfn(outpoint, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case detail := <-ev.Spend:
		if detail.SpendingHeight != 42 {
			t.Fatalf("spend height = %d, want 42",
				detail.SpendingHeight)
		}
		if *detail.SpentOutPoint != *outpoint {
			t.Fatal("spent outpoint mismatch")
		}
		// Defensive padding present.
		if len(detail.SpendingTx.TxOut) != maxSpendTxOutputs {
			t.Fatalf("spend tx outputs = %d, want %d",
				len(detail.SpendingTx.TxOut), maxSpendTxOutputs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for spend")
	}
}

func TestPreimageInSpendPayload(t *testing.T) {
	t.Parallel()

	var channelID chainhash.Hash
	channelID[0] = 0x11
	preimage := make([]byte, 32)
	preimage[31] = 0x99

	mc := &mockClient{
		height: 10,
		logs: []types.Log{{
			Topics: []common.Hash{
				TopicHTLCClaimed, common.Hash(channelID),
			},
			Data:        preimage,
			TxHash:      common.Hash{0xee},
			BlockNumber: 7,
		}},
	}
	n := newTestNotifier(mc)
	_ = n.Start()
	defer func() { _ = n.Stop() }()

	ev, _ := n.RegisterSpendNtfn(
		&wire.OutPoint{Hash: channelID}, nil, 0,
	)

	select {
	case detail := <-ev.Spend:
		// payload = 0x6a 'E' 'V' 'M' topic0[0] + preimage(32).
		payload := detail.SpendingTx.TxOut[0].PkScript
		if len(payload) != 5+32 {
			t.Fatalf("payload len = %d, want %d", len(payload), 5+32)
		}
		got := payload[5:]
		for i := range preimage {
			if got[i] != preimage[i] {
				t.Fatalf("preimage mismatch at %d", i)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HTLC claim spend")
	}
}
