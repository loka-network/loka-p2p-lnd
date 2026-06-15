package evmwallet

import (
	"context"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/stretchr/testify/require"
)

// capturingClient is an evmnotify.EvmClient mock recording every broadcast
// transaction.
type capturingClient struct {
	sent []*types.Transaction
}

func (c *capturingClient) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(31337), nil
}

func (c *capturingClient) BlockNumber(context.Context) (uint64, error) {
	return 1, nil
}

func (c *capturingClient) HeaderByNumber(context.Context, *big.Int) (
	*types.Header, error) {

	return &types.Header{Number: big.NewInt(1)}, nil
}

func (c *capturingClient) CallContract(context.Context, ethereum.CallMsg,
	*big.Int) ([]byte, error) {

	return nil, nil
}

func (c *capturingClient) PendingNonceAt(context.Context, common.Address) (
	uint64, error) {

	return uint64(len(c.sent)), nil
}

func (c *capturingClient) BalanceAt(context.Context, common.Address,
	*big.Int) (*big.Int, error) {

	return big.NewInt(1e18), nil
}

func (c *capturingClient) SuggestGasPrice(context.Context) (*big.Int, error) {
	return big.NewInt(1_000_000_000), nil
}

func (c *capturingClient) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return big.NewInt(1_000_000_000), nil
}

func (c *capturingClient) SendTransaction(_ context.Context,
	tx *types.Transaction) error {

	c.sent = append(c.sent, tx)

	return nil
}

func (c *capturingClient) TransactionReceipt(context.Context, common.Hash) (
	*types.Receipt, error) {

	// Every broadcast is "instantly mined" so waitMined returns at once.
	return &types.Receipt{BlockNumber: big.NewInt(1)}, nil
}

func (c *capturingClient) FilterLogs(context.Context, ethereum.FilterQuery) (
	[]types.Log, error) {

	return nil, nil
}

func (c *capturingClient) SubscribeFilterLogs(context.Context,
	ethereum.FilterQuery, chan<- types.Log) (ethereum.Subscription, error) {

	return nil, nil
}

func (c *capturingClient) Close() {}

// testKeyRing is a minimal SecretKeyRing over one fixed key.
type testKeyRing struct {
	priv *btcec.PrivateKey
}

func (k *testKeyRing) DeriveNextKey(keychain.KeyFamily) (
	keychain.KeyDescriptor, error) {

	return keychain.KeyDescriptor{PubKey: k.priv.PubKey()}, nil
}

func (k *testKeyRing) DeriveKey(loc keychain.KeyLocator) (
	keychain.KeyDescriptor, error) {

	return keychain.KeyDescriptor{
		KeyLocator: loc,
		PubKey:     k.priv.PubKey(),
	}, nil
}

func (k *testKeyRing) DerivePrivKey(keychain.KeyDescriptor) (
	*btcec.PrivateKey, error) {

	return k.priv, nil
}

func (k *testKeyRing) ECDH(keychain.KeyDescriptor, *btcec.PublicKey) (
	[32]byte, error) {

	return [32]byte{}, nil
}

func (k *testKeyRing) SignMessage(keychain.KeyLocator, []byte, bool) (
	*btcecdsa.Signature, error) {

	return nil, nil
}

func (k *testKeyRing) SignMessageCompact(keychain.KeyLocator, []byte, bool) (
	[]byte, error) {

	return nil, nil
}

func (k *testKeyRing) SignMessageSchnorr(keychain.KeyLocator, []byte, bool,
	[]byte, []byte) (*schnorr.Signature, error) {

	return nil, nil
}

const (
	testContractAddr = "0x4686A400982FB766092147506f421D28AfDa0e65"
	testTokenAddr    = "0x5FbDB2315678afecb367f032d93F642f64180aa3"
)

func newCarrierTestWallet(t *testing.T) (*Wallet, *capturingClient) {
	t.Helper()

	pkb, _ := hex.DecodeString(
		"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4" +
			"f2ff80",
	)
	priv, _ := btcec.PrivKeyFromBytes(pkb)
	client := &capturingClient{}

	w := New(Config{
		KeyRing: &testKeyRing{priv: priv},
		Client:  client,
		Params: chainreg.ResolveEvmParams(
			"anvil", 31337, testTokenAddr, testContractAddr,
		),
		TokenDecimals: 6, // USDC
		GasLimit:      500_000,
	})

	return w, client
}

// TestExecuteCarrierOpenChannel checks the ChannelOpen carrier translation:
// an ERC20 approve to the ChannelManager (scaled deposit) followed by the
// openChannel call with the same scaled amounts and the carrier's salt and
// counterparty.
func TestExecuteCarrierOpenChannel(t *testing.T) {
	t.Parallel()

	w, client := newCarrierTestWallet(t)

	var channelID chainhash.Hash
	channelID[0] = 0xAA
	salt := "11" + "22" + "0000000000000000000000000000000000000000" +
		"00000000000000000000"
	counterparty := "9965507d1a55bcc2695c58ba16fb37d819b0a4dc"

	// The test keyring returns one fixed key, so the "channel account" is
	// the node account here.
	chanAddr := common.HexToAddress(
		"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
	)

	// 5 tokens in LND-internal units (1e8/token) → 5e6 raw (6 decimals).
	carrier, err := input.BuildEvmChannelOpenTx(
		channelID, input.EvmChannelOpenPayload{
			Salt:         salt,
			Counterparty: counterparty,
			LocalBalance: 500_000_000,
			LocalKey: hex.EncodeToString(
				w.cfg.KeyRing.(*testKeyRing).priv.PubKey().
					SerializeCompressed(),
			),
		},
	)
	require.NoError(t, err)

	_, err = w.ExecuteOpenChannelCall(carrier)
	require.NoError(t, err)
	require.Len(t, client.sent, 4,
		"want gas-fund + deposit transfer + approve + openChannel")

	// First tx: native-coin gas provisioning of the channel account.
	gasTx := client.sent[0]
	require.Equal(t, chanAddr, *gasTx.To())
	require.Empty(t, gasTx.Data())
	require.Positive(t, gasTx.Value().Sign())

	// Second tx: transfer(chanAddr, 5e6) of the deposit on the token.
	depositTx := client.sent[1]
	require.Equal(t, common.HexToAddress(testTokenAddr), *depositTx.To())
	depositArgs, err := evmnotify.ERC20ABI.Methods["transfer"].Inputs.
		Unpack(depositTx.Data()[4:])
	require.NoError(t, err)
	require.Equal(t, chanAddr, depositArgs[0].(common.Address))
	require.Zero(t, big.NewInt(5_000_000).Cmp(depositArgs[1].(*big.Int)))

	// Third tx: approve(contract, 5e6) on the token.
	approveTx := client.sent[2]
	require.Equal(t, common.HexToAddress(testTokenAddr), *approveTx.To())
	approveArgs, err := evmnotify.ERC20ABI.Methods["approve"].Inputs.
		Unpack(approveTx.Data()[4:])
	require.NoError(t, err)
	require.Equal(t,
		common.HexToAddress(testContractAddr),
		approveArgs[0].(common.Address),
	)
	require.Zero(t, big.NewInt(5_000_000).Cmp(approveArgs[1].(*big.Int)))

	// Fourth tx: openChannel(salt, counterparty, 5e6, 0) on the contract.
	openTx := client.sent[3]
	require.Equal(t, common.HexToAddress(testContractAddr), *openTx.To())
	openArgs, err := evmnotify.ChannelManagerABI.Methods["openChannel"].
		Inputs.Unpack(openTx.Data()[4:])
	require.NoError(t, err)

	saltArg := openArgs[0].([32]byte)
	require.Equal(t, salt, hex.EncodeToString(saltArg[:]))
	require.Equal(t,
		common.HexToAddress("0x"+counterparty),
		openArgs[1].(common.Address),
	)
	require.Zero(t, big.NewInt(5_000_000).Cmp(openArgs[2].(*big.Int)))
	require.Zero(t, big.NewInt(0).Cmp(openArgs[3].(*big.Int)))
}

// TestExecuteCarrierForceClose checks the ForceClose carrier translation
// round-trips the StateUpdate tuple and signature into forceClose calldata.
func TestExecuteCarrierForceClose(t *testing.T) {
	t.Parallel()

	w, client := newCarrierTestWallet(t)

	var channelID chainhash.Hash
	channelID[31] = 0x07
	var htlcsHash [32]byte
	htlcsHash[0] = 0xBE
	sig := make([]byte, 65)
	sig[64] = 27

	carrier, err := input.BuildEvmForceCloseTx(
		channelID, 9, big.NewInt(123_456), big.NewInt(654_321),
		htlcsHash, sig,
		w.cfg.KeyRing.(*testKeyRing).priv.PubKey().
			SerializeCompressed(),
	)
	require.NoError(t, err)

	_, err = w.ExecuteOpenChannelCall(carrier)
	require.NoError(t, err)
	require.Len(t, client.sent, 1)

	tx := client.sent[0]
	require.Equal(t, common.HexToAddress(testContractAddr), *tx.To())
	args, err := evmnotify.ChannelManagerABI.Methods["forceClose"].Inputs.
		Unpack(tx.Data()[4:])
	require.NoError(t, err)

	cidArg := args[0].([32]byte)
	require.Equal(t, [32]byte(channelID), cidArg)
	require.Zero(t, big.NewInt(9).Cmp(args[1].(*big.Int)))
	require.Zero(t, big.NewInt(123_456).Cmp(args[2].(*big.Int)))
	require.Zero(t, big.NewInt(654_321).Cmp(args[3].(*big.Int)))
	require.Equal(t, htlcsHash, args[4].([32]byte))
	require.Equal(t, sig, args[5].([]byte))
}

// TestExecuteCarrierClaimHtlc checks the ClaimHtlc carrier translation packs
// the HTLC tuple, proof and preimage.
func TestExecuteCarrierClaimHtlc(t *testing.T) {
	t.Parallel()

	w, client := newCarrierTestWallet(t)

	var channelID chainhash.Hash
	channelID[5] = 0x55

	htlc := input.EvmHTLC{
		Index:    3,
		Amount:   big.NewInt(777),
		Timelock: 1_900_000_000,
	}
	htlc.Hashlock[0] = 0xCC
	htlc.Recipient[19] = 0x01

	proof := [][32]byte{{0x01}, {0x02}}
	var preimage [32]byte
	preimage[31] = 0x09

	carrier, err := input.BuildEvmClaimHtlcTx(
		channelID, htlc, proof, preimage,
	)
	require.NoError(t, err)

	_, err = w.ExecuteOpenChannelCall(carrier)
	require.NoError(t, err)
	require.Len(t, client.sent, 1)

	tx := client.sent[0]
	args, err := evmnotify.ChannelManagerABI.Methods["claimHtlc"].Inputs.
		Unpack(tx.Data()[4:])
	require.NoError(t, err)

	require.Equal(t, [32]byte(channelID), args[0].([32]byte))

	// The tuple comes back as an anonymous struct; spot-check via
	// re-packing the expected arg and comparing calldata instead.
	expectedData, err := evmnotify.PackClaimHtlc(
		[32]byte(channelID), evmnotify.EvmHTLCArg{
			Index:     big.NewInt(3),
			Amount:    big.NewInt(777),
			Hashlock:  htlc.Hashlock,
			Timelock:  htlc.Timelock,
			Recipient: common.BytesToAddress(htlc.Recipient[:]),
		}, proof, preimage,
	)
	require.NoError(t, err)
	require.Equal(t, expectedData, tx.Data())
}
