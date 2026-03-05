package setuwallet

import (
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// SetuBlockChainIO is a stub implementation of the lnwallet.BlockChainIO
// interface for the Setu DAG backend.
//
// Mapping semantics:
//   - GetBestBlock  -> query the Setu validator for the latest epoch height
//     and anchor hash.
//   - GetUtxo       -> query whether a Channel Object (ObjectID stored in
//     OutPoint.Hash) still exists in the Setu state tree.
//   - GetBlockHash  -> return the anchor hash for the given epoch height.
//   - GetBlock      -> wrap the Setu epoch data in a wire.MsgBlock shell.
//
// All methods currently return ErrUnsupported. They will be replaced with real
// gRPC calls once the Setu SDK connectivity layer is implemented.
type SetuBlockChainIO struct{}

// Compile-time assertion that SetuBlockChainIO satisfies the interface.
var _ lnwallet.BlockChainIO = (*SetuBlockChainIO)(nil)

// GetBestBlock returns the current Setu epoch height and anchor hash.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SetuBlockChainIO) GetBestBlock() (*chainhash.Hash, int32, error) {
	return nil, 0, ErrUnsupported
}

// GetUtxo queries whether the Channel Object identified by op.Hash (a Setu
// ObjectID) still exists in the Setu state tree.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SetuBlockChainIO) GetUtxo(
	op *wire.OutPoint, pkScript []byte, heightHint uint32,
	cancel <-chan struct{}) (*wire.TxOut, error) {

	return nil, ErrUnsupported
}

// GetBlockHash returns the anchor hash for the given Setu epoch height.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SetuBlockChainIO) GetBlockHash(blockHeight int64) (*chainhash.Hash, error) {
	return nil, ErrUnsupported
}

// GetBlock wraps the Setu epoch data into a wire.MsgBlock shell so that
// existing LND subsystems that rely on block data can continue to operate.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SetuBlockChainIO) GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	return nil, ErrUnsupported
}

// GetBlockHeader returns the block header for the given block hash.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SetuBlockChainIO) GetBlockHeader(
	blockHash *chainhash.Hash) (*wire.BlockHeader, error) {

	return nil, ErrUnsupported
}
