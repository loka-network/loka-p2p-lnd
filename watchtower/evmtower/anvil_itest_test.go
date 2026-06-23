//go:build evmtower_itest

// This integration test runs the full EVM watchtower loop against a live anvil
// chain and a real deployed ChannelManager, proving the H-1 breach remedy
// end to end: a channel is force-closed with a revoked (lower-nonce) state and
// the tower — holding the latest co-signed state — penalizes on the offline
// victim's behalf, sweeping the whole escrow to the victim.
//
// It is build-tagged so normal `go test`/CI (which has no anvil) skips it. The
// wrapper scripts/itest_evm_watchtower.sh starts anvil, deploys the contract,
// and runs it with:
//
//	go test -tags evmtower_itest -run TestAnvilWatchtowerBreach \
//	    ./watchtower/evmtower/
//
// Env: EVMTOWER_RPC, EVMTOWER_CONTRACT, EVMTOWER_TOKEN, EVMTOWER_DEPLOYER_KEY.
package evmtower

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"os"
	"testing"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/input"
	"github.com/stretchr/testify/require"
)

func TestAnvilWatchtowerBreach(t *testing.T) {
	rpc := os.Getenv("EVMTOWER_RPC")
	contractHex := os.Getenv("EVMTOWER_CONTRACT")
	tokenHex := os.Getenv("EVMTOWER_TOKEN")
	deployerHex := os.Getenv("EVMTOWER_DEPLOYER_KEY")
	if rpc == "" || contractHex == "" || tokenHex == "" || deployerHex == "" {
		t.Skip("set EVMTOWER_{RPC,CONTRACT,TOKEN,DEPLOYER_KEY}")
	}

	ctx := context.Background()
	client, err := evmnotify.DialEvmClient(rpc)
	require.NoError(t, err)
	defer client.Close()

	contract := common.HexToAddress(contractHex)
	token := common.HexToAddress(tokenHex)
	deployer := mustKey(t, deployerHex)
	chainID, err := client.ChainID(ctx)
	require.NoError(t, err)

	// Parties: A is the cheater/broadcaster, B the offline victim; tower is
	// the relayer that submits penalize. Fresh keys each run.
	aKey, _ := gethcrypto.GenerateKey()
	bKey, _ := gethcrypto.GenerateKey()
	towerKey, _ := gethcrypto.GenerateKey()
	aAddr := gethcrypto.PubkeyToAddress(aKey.PublicKey)
	bAddr := gethcrypto.PubkeyToAddress(bKey.PublicKey)
	towerAddr := gethcrypto.PubkeyToAddress(towerKey.PublicKey)

	// Gas for A (open/forceClose) and the tower (penalize). B stays offline.
	// Default 1 ETH (anvil); a public testnet sets EVMTOWER_GAS_WEI small so
	// the modestly-funded deployer can afford it.
	gasFund := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	if v := os.Getenv("EVMTOWER_GAS_WEI"); v != "" {
		g, ok := new(big.Int).SetString(v, 10)
		require.True(t, ok, "bad EVMTOWER_GAS_WEI")
		gasFund = g
	}
	sendValue(ctx, t, client, deployer, aAddr, gasFund)
	sendValue(ctx, t, client, deployer, towerAddr, gasFund)

	// Fund A with 100 USDC (6-dec) from the deployer's minted supply.
	const deposit = uint64(100_000_000) // 100 USDC
	transferData, err := evmnotify.PackTransfer(
		aAddr, new(big.Int).SetUint64(deposit),
	)
	require.NoError(t, err)
	sendCall(ctx, t, client, deployer, token, transferData)

	// A approves the ChannelManager, then opens a single-funded channel to B
	// (remoteFundingAmount 0 → no counterparty consent sig needed).
	approveData, err := evmnotify.PackApprove(
		contract, new(big.Int).SetUint64(deposit),
	)
	require.NoError(t, err)
	sendCall(ctx, t, client, aKey, token, approveData)

	var salt [32]byte
	salt[31] = 0x7
	openData, err := evmnotify.PackOpenChannel(
		salt, bAddr, new(big.Int).SetUint64(deposit), big.NewInt(0), nil,
	)
	require.NoError(t, err)
	sendCall(ctx, t, client, aKey, contract, openData)

	// channelId = keccak256(participantA, participantB, salt).
	channelID := [32]byte(gethcrypto.Keccak256(
		aAddr.Bytes(), bAddr.Bytes(), salt[:],
	))

	domain := input.EvmDomain{
		ChainID:           chainID.Uint64(),
		VerifyingContract: [20]byte(contract),
	}

	// Revoked state (nonce 1, favors A): B co-signed it; A will force-close
	// with it. Latest state (nonce 2, favors B): A co-signed it; the tower
	// holds it as the breach proof.
	revoked := input.EvmStateUpdate{
		ChannelID: channelID, Nonce: 1,
		BalanceA: big.NewInt(90_000_000), BalanceB: big.NewInt(10_000_000),
	}
	latest := input.EvmStateUpdate{
		ChannelID: channelID, Nonce: 2,
		BalanceA: big.NewInt(10_000_000), BalanceB: big.NewInt(90_000_000),
	}
	sigBRevoked := signSU(t, bKey, domain, revoked) // counterparty sig
	sigALatest := signSU(t, aKey, domain, latest)   // broadcaster's later sig

	// A cheats: force-close with the revoked state (B's signature).
	fcData, err := evmnotify.PackForceClose(
		channelID, big.NewInt(1), revoked.BalanceA, revoked.BalanceB,
		[32]byte{}, sigBRevoked,
	)
	require.NoError(t, err)
	sendCall(ctx, t, client, aKey, contract, fcData)

	// The tower holds the latest co-signed state and runs its lookout.
	store := NewMemStore()
	require.NoError(t, store.Put(&JusticeBackup{
		ChannelID: channelID, Nonce: 2,
		BalanceA: latest.BalanceA, BalanceB: latest.BalanceB,
		CounterpartySig: sigALatest,
	}))
	lo := NewLookout(Config{
		Client:   client,
		Contract: contract,
		Store:    store,
		Penalizer: &EvmPenalizer{
			Client: client, Contract: contract, Key: towerKey,
		},
		// Poll quickly; the close may land in the very tip block, caught
		// on the next pass. Run the real loop, not a single scan, so this
		// works against a live chain too.
		PollInterval: 2 * time.Second,
	})
	lo.Start()
	defer lo.Stop()

	// The victim B (offline throughout) must receive the entire deposit
	// before the challenge window closes (60s on the test deployment).
	require.Eventually(t, func() bool {
		bal := balanceOf(ctx, t, client, token, bAddr)

		return bal.Uint64() == deposit
	}, 55*time.Second, 2*time.Second,
		"victim should be swept the full escrow by the tower's penalize")
}

// --- helpers ---------------------------------------------------------------

func mustKey(t *testing.T, hexKey string) *ecdsa.PrivateKey {
	t.Helper()
	if len(hexKey) >= 2 && hexKey[:2] == "0x" {
		hexKey = hexKey[2:]
	}
	k, err := gethcrypto.HexToECDSA(hexKey)
	require.NoError(t, err)

	return k
}

func signSU(t *testing.T, key *ecdsa.PrivateKey, d input.EvmDomain,
	su input.EvmStateUpdate) []byte {

	t.Helper()
	digest := su.Digest(d)
	sig, err := gethcrypto.Sign(digest[:], key)
	require.NoError(t, err)
	sig[64] += 27 // OZ ECDSA.recover expects v ∈ {27,28}

	return sig
}

func sendCall(ctx context.Context, t *testing.T, client evmnotify.EvmClient,
	key *ecdsa.PrivateKey, to common.Address, data []byte) {

	t.Helper()
	sendRaw(ctx, t, client, key, to, data, big.NewInt(0), 1_500_000)
}

func sendValue(ctx context.Context, t *testing.T, client evmnotify.EvmClient,
	key *ecdsa.PrivateKey, to common.Address, value *big.Int) {

	t.Helper()
	sendRaw(ctx, t, client, key, to, nil, value, 21_000)
}

func sendRaw(ctx context.Context, t *testing.T, client evmnotify.EvmClient,
	key *ecdsa.PrivateKey, to common.Address, data []byte, value *big.Int,
	gas uint64) {

	t.Helper()
	chainID, err := client.ChainID(ctx)
	require.NoError(t, err)
	from := gethcrypto.PubkeyToAddress(key.PublicKey)
	nonce, err := client.PendingNonceAt(ctx, from)
	require.NoError(t, err)
	gp, err := client.SuggestGasPrice(ctx)
	require.NoError(t, err)

	tx := types.NewTx(&types.LegacyTx{
		Nonce: nonce, To: &to, Value: value, Gas: gas, GasPrice: gp,
		Data: data,
	})
	signed, err := types.SignTx(
		tx, types.LatestSignerForChainID(chainID), key,
	)
	require.NoError(t, err)
	require.NoError(t, client.SendTransaction(ctx, signed))

	// Wait for the receipt and require success.
	require.Eventually(t, func() bool {
		r, err := client.TransactionReceipt(ctx, signed.Hash())
		if err != nil || r == nil {
			return false
		}
		require.Equal(t, uint64(1), r.Status, "tx reverted: %s",
			signed.Hash())

		return true
	}, 20*time.Second, 250*time.Millisecond)
}

func balanceOf(ctx context.Context, t *testing.T, client evmnotify.EvmClient,
	token, account common.Address) *big.Int {

	t.Helper()
	data, err := evmnotify.PackBalanceOf(account)
	require.NoError(t, err)
	out, err := client.CallContract(
		ctx, ethereum.CallMsg{To: &token, Data: data}, nil,
	)
	require.NoError(t, err)
	bal, err := evmnotify.UnpackBalanceOf(out)
	require.NoError(t, err)

	return bal
}
