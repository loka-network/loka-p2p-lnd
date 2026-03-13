package suiwallet

import (
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// SuiBlockChainIO is a stub implementation of the lnwallet.BlockChainIO
// interface for the Sui DAG backend.
//
// Mapping semantics:
//   - GetBestBlock  -> query the Sui validator for the latest epoch height
//     and anchor hash.
//   - GetUtxo       -> query whether a Channel Object (ObjectID stored in
//     OutPoint.Hash) still exists in the Sui state tree.
//   - GetBlockHash  -> return the anchor hash for the given epoch height.
//   - GetBlock      -> wrap the Sui epoch data in a wire.MsgBlock shell.
//
// All methods currently return ErrUnsupported. They will be replaced with real
// gRPC calls once the Sui SDK connectivity layer is implemented.
type SuiBlockChainIO struct{}

// Compile-time assertion that SuiBlockChainIO satisfies the interface.
var _ lnwallet.BlockChainIO = (*SuiBlockChainIO)(nil)

// GetBestBlock returns the current Sui epoch height and anchor hash.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SuiBlockChainIO) GetBestBlock() (*chainhash.Hash, int32, error) {
	return nil, 0, ErrUnsupported
}

// GetUtxo queries whether the Channel Object identified by op.Hash (a Sui
// ObjectID) still exists in the Sui state tree.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SuiBlockChainIO) GetUtxo(
	op *wire.OutPoint, pkScript []byte, heightHint uint32,
	cancel <-chan struct{}) (*wire.TxOut, error) {

	return nil, ErrUnsupported
}

// GetBlockHash returns the anchor hash for the given Sui epoch height.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SuiBlockChainIO) GetBlockHash(blockHeight int64) (*chainhash.Hash, error) {
	return nil, ErrUnsupported
}

// GetBlock wraps the Sui epoch data into a wire.MsgBlock shell so that
// existing LND subsystems that rely on block data can continue to operate.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SuiBlockChainIO) GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	return nil, ErrUnsupported
}

// GetBlockHeader returns the block header for the given block hash.
//
// NOTE: Stub — returns ErrUnsupported until the gRPC backend is wired in.
func (s *SuiBlockChainIO) GetBlockHeader(
	blockHash *chainhash.Hash) (*wire.BlockHeader, error) {

	return nil, ErrUnsupported
}
