package evmnotify

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// EvmClient is the narrow JSON-RPC surface the EVM adapters need from an
// EVM-compatible node. It is satisfied by GethClient (a go-ethereum ethclient
// wrapper) in production and by mocks in tests, mirroring how suinotify.SuiClient
// abstracts the Sui node.
type EvmClient interface {
	// ChainID returns the EIP-155 chain id reported by the node.
	ChainID(ctx context.Context) (*big.Int, error)

	// BlockNumber returns the latest block height.
	BlockNumber(ctx context.Context) (uint64, error)

	// HeaderByNumber returns the header at the given height (nil = latest).
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header,
		error)

	// CallContract performs a read-only eth_call.
	CallContract(ctx context.Context, msg ethereum.CallMsg,
		blockNumber *big.Int) ([]byte, error)

	// PendingNonceAt returns the next nonce for the given account.
	PendingNonceAt(ctx context.Context, account common.Address) (uint64,
		error)

	// SuggestGasPrice returns the node's suggested legacy gas price.
	SuggestGasPrice(ctx context.Context) (*big.Int, error)

	// SuggestGasTipCap returns the node's suggested EIP-1559 priority fee.
	SuggestGasTipCap(ctx context.Context) (*big.Int, error)

	// SendTransaction broadcasts a signed transaction.
	SendTransaction(ctx context.Context, tx *types.Transaction) error

	// TransactionReceipt returns the receipt for a mined transaction.
	TransactionReceipt(ctx context.Context, txHash common.Hash) (
		*types.Receipt, error)

	// FilterLogs returns logs matching the query (historical scan).
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log,
		error)

	// SubscribeFilterLogs streams matching logs as they are mined.
	SubscribeFilterLogs(ctx context.Context, q ethereum.FilterQuery,
		ch chan<- types.Log) (ethereum.Subscription, error)

	// Close releases the underlying connection.
	Close()
}

// GethClient is the production EvmClient backed by go-ethereum's ethclient.
type GethClient struct {
	*ethclient.Client
}

// Compile-time assertion that GethClient satisfies EvmClient.
var _ EvmClient = (*GethClient)(nil)

// DialEvmClient connects to an EVM node over JSON-RPC (http(s):// or ws(s)://).
// Event subscriptions require a WebSocket endpoint; for HTTP-only endpoints the
// notifier falls back to header/log polling.
func DialEvmClient(rpcURL string) (*GethClient, error) {
	c, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, err
	}

	return &GethClient{Client: c}, nil
}
