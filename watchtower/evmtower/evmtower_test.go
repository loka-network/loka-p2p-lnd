package evmtower

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/stretchr/testify/require"
)

func testBackup(nonce uint64) *JusticeBackup {
	return &JusticeBackup{
		ChannelID:       [32]byte{0xab, 0xcd},
		Nonce:           nonce,
		BalanceA:        big.NewInt(600_000_000),
		BalanceB:        big.NewInt(400_000_000),
		HtlcsHash:       [32]byte{},
		CounterpartySig: make([]byte, 65),
	}
}

// TestBackupEncodeRoundtrip checks Encode/Decode preserve every field.
func TestBackupEncodeRoundtrip(t *testing.T) {
	t.Parallel()

	b := testBackup(7)
	b.CounterpartySig[0] = 0x1b
	b.HtlcsHash[31] = 0x9

	enc, err := b.Encode()
	require.NoError(t, err)

	got, err := DecodeJusticeBackup(enc)
	require.NoError(t, err)
	require.Equal(t, b.ChannelID, got.ChannelID)
	require.Equal(t, b.Nonce, got.Nonce)
	require.Equal(t, 0, b.BalanceA.Cmp(got.BalanceA))
	require.Equal(t, 0, b.BalanceB.Cmp(got.BalanceB))
	require.Equal(t, b.HtlcsHash, got.HtlcsHash)
	require.Equal(t, b.CounterpartySig, got.CounterpartySig)
}

// TestShouldPenalize covers the breach predicate.
func TestShouldPenalize(t *testing.T) {
	t.Parallel()

	require.False(t, shouldPenalize(nil, 5))
	require.False(t, shouldPenalize(testBackup(5), 5)) // equal — not a breach
	require.False(t, shouldPenalize(testBackup(4), 5)) // older backup
	require.True(t, shouldPenalize(testBackup(7), 5))  // newer backup = breach
}

// TestMemStoreKeepsHighestNonce checks the store never regresses.
func TestMemStoreKeepsHighestNonce(t *testing.T) {
	t.Parallel()

	s := NewMemStore()
	require.NoError(t, s.Put(testBackup(5)))
	require.NoError(t, s.Put(testBackup(3))) // stale — ignored
	require.NoError(t, s.Put(testBackup(9)))

	got, ok := s.Get([32]byte{0xab, 0xcd})
	require.True(t, ok)
	require.Equal(t, uint64(9), got.Nonce)
}

// closeLogClient is a mock EvmClient that serves one UnilateralCloseInitiated
// log and captures broadcast transactions.
type closeLogClient struct {
	contract       common.Address
	channelID      [32]byte
	broadcastNonce uint64
	challengeAhead bool // challenge window still open

	sent []*types.Transaction
}

func (c *closeLogClient) FilterLogs(_ context.Context,
	q ethereum.FilterQuery) ([]types.Log, error) {

	// Build the UnilateralCloseInitiated non-indexed data:
	// (broadcaster, nonce, balanceA, balanceB, challengeExpiry).
	expiry := big.NewInt(1) // already passed
	if c.challengeAhead {
		expiry = big.NewInt(time.Now().Add(time.Hour).Unix())
	}
	data, err := evmnotify.ChannelManagerABI.
		Events["UnilateralCloseInitiated"].Inputs.NonIndexed().Pack(
		common.HexToAddress("0x000000000000000000000000000000000000dEaD"),
		new(big.Int).SetUint64(c.broadcastNonce),
		big.NewInt(900_000_000), big.NewInt(100_000_000), expiry,
	)
	if err != nil {
		return nil, err
	}

	return []types.Log{{
		Address: c.contract,
		Topics: []common.Hash{
			evmnotify.TopicUnilateralCloseInitiated,
			common.Hash(c.channelID),
		},
		Data: data,
	}}, nil
}

func (c *closeLogClient) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(31337), nil
}
func (c *closeLogClient) PendingNonceAt(context.Context, common.Address) (
	uint64, error) {

	return 0, nil
}
func (c *closeLogClient) SuggestGasPrice(context.Context) (*big.Int, error) {
	return big.NewInt(1_000_000_000), nil
}
func (c *closeLogClient) SendTransaction(_ context.Context,
	tx *types.Transaction) error {

	c.sent = append(c.sent, tx)

	return nil
}

// Unused EvmClient methods.
func (c *closeLogClient) BlockNumber(context.Context) (uint64, error) {
	return 1, nil
}
func (c *closeLogClient) HeaderByNumber(context.Context, *big.Int) (
	*types.Header, error) {

	return &types.Header{Number: big.NewInt(1)}, nil
}
func (c *closeLogClient) CallContract(context.Context, ethereum.CallMsg,
	*big.Int) ([]byte, error) {

	return nil, nil
}
func (c *closeLogClient) BalanceAt(context.Context, common.Address,
	*big.Int) (*big.Int, error) {

	return big.NewInt(1e18), nil
}
func (c *closeLogClient) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}
func (c *closeLogClient) TransactionReceipt(context.Context, common.Hash) (
	*types.Receipt, error) {

	return &types.Receipt{BlockNumber: big.NewInt(1)}, nil
}
func (c *closeLogClient) SubscribeFilterLogs(context.Context,
	ethereum.FilterQuery, chan<- types.Log) (ethereum.Subscription, error) {

	return nil, nil
}
func (c *closeLogClient) Close() {}

// TestLookoutPenalizesStaleClose drives the full core loop: a backed-up channel
// is force-closed with a revoked (lower) nonce, and the lookout submits a
// penalize carrying the backup's higher-nonce state.
func TestLookoutPenalizesStaleClose(t *testing.T) {
	t.Parallel()

	chanID := [32]byte{0xab, 0xcd}
	contract := common.HexToAddress(
		"0x5BB60C287435B420BE926c34dA54f670B165Fd12",
	)
	key, err := gethcrypto.GenerateKey()
	require.NoError(t, err)

	client := &closeLogClient{
		contract:       contract,
		channelID:      chanID,
		broadcastNonce: 4, // cheater broadcasts an old state
		challengeAhead: true,
	}

	store := NewMemStore()
	require.NoError(t, store.Put(testBackup(9))) // we hold a newer state

	lo := NewLookout(Config{
		Client:    client,
		Contract:  contract,
		Store:     store,
		Penalizer: &EvmPenalizer{Client: client, Contract: contract, Key: key},
	})

	// Drive one scan synchronously.
	lo.scan()

	require.Len(t, client.sent, 1, "expected one penalize tx")
	tx := client.sent[0]
	require.NotNil(t, tx.To())
	require.Equal(t, contract, *tx.To())

	// The calldata must be a penalize call for our higher-nonce backup.
	want, err := evmnotify.PackPenalize(
		chanID, big.NewInt(9), big.NewInt(600_000_000),
		big.NewInt(400_000_000), [32]byte{}, make([]byte, 65),
	)
	require.NoError(t, err)
	require.Equal(t, want, tx.Data())

	// A second scan must not double-submit (channel marked done).
	lo.scan()
	require.Len(t, client.sent, 1)
}

// TestLookoutSkipsWhenWindowClosed checks we don't waste a tx after expiry.
func TestLookoutSkipsWhenWindowClosed(t *testing.T) {
	t.Parallel()

	chanID := [32]byte{0xab, 0xcd}
	contract := common.HexToAddress(
		"0x5BB60C287435B420BE926c34dA54f670B165Fd12",
	)
	key, _ := gethcrypto.GenerateKey()
	client := &closeLogClient{
		contract: contract, channelID: chanID,
		broadcastNonce: 4, challengeAhead: false, // window already closed
	}
	store := NewMemStore()
	require.NoError(t, store.Put(testBackup(9)))

	lo := NewLookout(Config{
		Client: client, Contract: contract, Store: store,
		Penalizer: &EvmPenalizer{Client: client, Contract: contract, Key: key},
	})
	lo.scan()
	require.Empty(t, client.sent, "must not penalize after the window closes")
}

// TestLookoutNoBreachOnFreshClose checks an honest (latest-nonce) close is not
// penalized.
func TestLookoutNoBreachOnFreshClose(t *testing.T) {
	t.Parallel()

	chanID := [32]byte{0xab, 0xcd}
	contract := common.HexToAddress(
		"0x5BB60C287435B420BE926c34dA54f670B165Fd12",
	)
	key, _ := gethcrypto.GenerateKey()
	client := &closeLogClient{
		contract: contract, channelID: chanID,
		broadcastNonce: 9, challengeAhead: true, // closes at the latest state
	}
	store := NewMemStore()
	require.NoError(t, store.Put(testBackup(9)))

	lo := NewLookout(Config{
		Client: client, Contract: contract, Store: store,
		Penalizer: &EvmPenalizer{Client: client, Contract: contract, Key: key},
	})
	lo.scan()
	require.Empty(t, client.sent, "honest close must not be penalized")
}
