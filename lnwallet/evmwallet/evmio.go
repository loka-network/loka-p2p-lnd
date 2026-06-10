package evmwallet

import (
	"context"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// EvmBlockChainIO adapts an EVM JSON-RPC node to lnwallet.BlockChainIO,
// mirroring suiwallet.SuiBlockChainIO. Only GetBestBlock has a faithful EVM
// analogue; the block-fetch methods are stubs because no LND subsystem on the
// EVM path consumes raw Bitcoin blocks (the notifier delivers events and
// epochs directly).
type EvmBlockChainIO struct {
	client evmnotify.EvmClient
}

// NewEvmBlockChainIO creates a new EvmBlockChainIO instance.
func NewEvmBlockChainIO(client evmnotify.EvmClient) *EvmBlockChainIO {
	return &EvmBlockChainIO{client: client}
}

// Compile-time assertion that EvmBlockChainIO satisfies the interface.
var _ lnwallet.BlockChainIO = (*EvmBlockChainIO)(nil)

// GetBestBlock returns the latest EVM block height and its hash.
func (e *EvmBlockChainIO) GetBestBlock() (*chainhash.Hash, int32, error) {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	hdr, err := e.client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, 0, err
	}

	var hash chainhash.Hash
	copy(hash[:], hdr.Hash().Bytes())

	return &hash, int32(hdr.Number.Int64()), nil
}

// GetUtxo reports the channel identified by op.Hash (the 32-byte EVM
// channelId) as unspent. It is used by graph builder liveness checks; actual
// close detection flows through the notifier's contract-event spend watcher,
// so a permissive placeholder is sufficient (same approach as Sui).
func (e *EvmBlockChainIO) GetUtxo(op *wire.OutPoint, pkScript []byte,
	heightHint uint32, cancel <-chan struct{}) (*wire.TxOut, error) {

	return &wire.TxOut{
		Value:    1000, // Placeholder
		PkScript: pkScript,
	}, nil
}

// GetBlockHash is unsupported on EVM; no LND subsystem on the EVM path
// fetches blocks by height.
func (e *EvmBlockChainIO) GetBlockHash(int64) (*chainhash.Hash, error) {
	return nil, ErrUnsupported
}

// GetBlock is unsupported on EVM; events are delivered by the notifier, not
// extracted from raw blocks.
func (e *EvmBlockChainIO) GetBlock(*chainhash.Hash) (*wire.MsgBlock, error) {
	return nil, ErrUnsupported
}

// GetBlockHeader is unsupported on EVM.
func (e *EvmBlockChainIO) GetBlockHeader(*chainhash.Hash) (*wire.BlockHeader,
	error) {

	return nil, ErrUnsupported
}
