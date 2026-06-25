package evmtower

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/stretchr/testify/require"
)

// rangeClient is a mock EvmClient that serves the close log only when the
// queried [FromBlock,ToBlock] range covers logBlock, and reports a fixed tip —
// so a test can prove the lookout chunks forward and never queries more than
// WindowSize blocks at once. It records the widest range it was asked for.
type rangeClient struct {
	tip       uint64
	logBlock  uint64
	channelID [32]byte
	maxSpan   uint64
	sent      []*types.Transaction
}

func (c *rangeClient) BlockNumber(context.Context) (uint64, error) {
	return c.tip, nil
}

func (c *rangeClient) FilterLogs(_ context.Context, q ethereum.FilterQuery) (
	[]types.Log, error) {

	from := q.FromBlock.Uint64()
	to := q.ToBlock.Uint64()
	if span := to - from + 1; span > c.maxSpan {
		c.maxSpan = span
	}
	if c.logBlock < from || c.logBlock > to {
		return nil, nil
	}

	data, err := evmnotify.ChannelManagerABI.
		Events["UnilateralCloseInitiated"].Inputs.NonIndexed().Pack(
		common.HexToAddress("0x00000000000000000000000000000000000000dE"),
		big.NewInt(4), // broadcast nonce (revoked)
		big.NewInt(900_000_000), big.NewInt(100_000_000),
		big.NewInt(1<<62), // challenge window far in the future
	)
	if err != nil {
		return nil, err
	}

	return []types.Log{{
		BlockNumber: c.logBlock,
		Topics: []common.Hash{
			evmnotify.TopicUnilateralCloseInitiated,
			common.Hash(c.channelID),
		},
		Data: data,
	}}, nil
}

func (c *rangeClient) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(31337), nil
}
func (c *rangeClient) PendingNonceAt(context.Context, common.Address) (uint64,
	error) {
	return 0, nil
}
func (c *rangeClient) SuggestGasPrice(context.Context) (*big.Int, error) {
	return big.NewInt(1_000_000_000), nil
}
func (c *rangeClient) SendTransaction(_ context.Context,
	tx *types.Transaction) error {
	c.sent = append(c.sent, tx)
	return nil
}
func (c *rangeClient) HeaderByNumber(context.Context, *big.Int) (*types.Header,
	error) {
	return &types.Header{Number: big.NewInt(int64(c.tip))}, nil
}
func (c *rangeClient) CallContract(context.Context, ethereum.CallMsg,
	*big.Int) ([]byte, error) {
	return nil, nil
}
func (c *rangeClient) BalanceAt(context.Context, common.Address, *big.Int) (
	*big.Int, error) {
	return big.NewInt(1e18), nil
}
func (c *rangeClient) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}
func (c *rangeClient) TransactionReceipt(context.Context, common.Hash) (
	*types.Receipt, error) {
	return &types.Receipt{BlockNumber: big.NewInt(1)}, nil
}
func (c *rangeClient) SubscribeFilterLogs(context.Context, ethereum.FilterQuery,
	chan<- types.Log) (ethereum.Subscription, error) {
	return nil, nil
}
func (c *rangeClient) Close() {}

// TestLookoutChunkedScanCatchesUp proves the lookout walks forward from a
// configured FromBlock in WindowSize chunks (each query bounded), eventually
// scanning the chunk containing a close far behind the tip, and never exceeds
// the window — the public-RPC range-cap fix.
func TestLookoutChunkedScanCatchesUp(t *testing.T) {
	t.Parallel()

	chanID := [32]byte{0xab, 0xcd}
	key, _ := gethcrypto.GenerateKey()
	const window = 1000
	client := &rangeClient{tip: 5000, logBlock: 3500, channelID: chanID}

	store := NewMemStore()
	require.NoError(t, store.Put(testBackup(9))) // newer than broadcast nonce 4

	lo := NewLookout(Config{
		Client:     client,
		Store:      store,
		Penalizer:  &EvmPenalizer{Client: client, Key: key},
		FromBlock:  1000, // honoured verbatim, chunked forward
		WindowSize: window,
	})

	// Chunks: [1000,1999] [2000,2999] [3000,3999] ← log at 3500 here.
	for i := 0; i < 3; i++ {
		lo.scan()
	}

	require.Len(t, client.sent, 1, "penalize after reaching the log's chunk")
	require.LessOrEqual(t, client.maxSpan, uint64(window),
		"no query may exceed the window")
}

// TestLookoutWindowFloorsToTip checks that with no FromBlock the scan starts a
// single window back from the tip (not from genesis on a long chain).
func TestLookoutWindowFloorsToTip(t *testing.T) {
	t.Parallel()

	key, _ := gethcrypto.GenerateKey()
	const window = 1000
	// Log is older than tip-window, so a from-tip start must miss it.
	client := &rangeClient{
		tip: 100000, logBlock: 50, channelID: [32]byte{0xab, 0xcd},
	}
	store := NewMemStore()
	require.NoError(t, store.Put(testBackup(9)))

	lo := NewLookout(Config{
		Client:     client,
		Store:      store,
		Penalizer:  &EvmPenalizer{Client: client, Key: key},
		WindowSize: window, // FromBlock 0 → start at tip-window (99000)
	})
	lo.scan()

	require.Empty(t, client.sent,
		"a close older than the from-tip window is not scanned")
	require.LessOrEqual(t, client.maxSpan, uint64(window))
}

// TestLookoutReorgBufferHoldsRecentClose proves a close within ReorgDepth of the
// tip is held back (not acted on) until it is deep enough to be reorg-safe.
func TestLookoutReorgBufferHoldsRecentClose(t *testing.T) {
	t.Parallel()

	chanID := [32]byte{0xab, 0xcd}
	key, _ := gethcrypto.GenerateKey()
	// Close at 4999; with ReorgDepth=2 and tip 5000 the safe tip is 4998.
	client := &rangeClient{tip: 5000, logBlock: 4999, channelID: chanID}
	store := NewMemStore()
	require.NoError(t, store.Put(testBackup(9)))

	lo := NewLookout(Config{
		Client:     client,
		Store:      store,
		Penalizer:  &EvmPenalizer{Client: client, Key: key},
		FromBlock:  4990,
		WindowSize: 1000,
		ReorgDepth: 2,
	})

	lo.scan()
	require.Empty(t, client.sent,
		"a close within ReorgDepth of the tip must be held back")

	// Two more blocks mined: the close is now 2 deep → reorg-safe.
	client.tip = 5001
	lo.scan()
	require.Len(t, client.sent, 1,
		"the close is acted on once it is reorg-safe")
}
