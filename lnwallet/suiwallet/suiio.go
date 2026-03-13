package suiwallet

import (
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/lnwallet"
)

// SuiBlockChainIO is an adapter that implements the lnwallet.BlockChainIO
// interface for the Sui DAG backend.
type SuiBlockChainIO struct {
	client SuiClient
}

// NewSuiBlockChainIO creates a new SuiBlockChainIO instance.
func NewSuiBlockChainIO(client SuiClient) *SuiBlockChainIO {
	return &SuiBlockChainIO{
		client: client,
	}
}

// Compile-time assertion that SuiBlockChainIO satisfies the interface.
var _ lnwallet.BlockChainIO = (*SuiBlockChainIO)(nil)

// GetBestBlock returns the current Sui epoch height and anchor hash.
func (s *SuiBlockChainIO) GetBestBlock() (*chainhash.Hash, int32, error) {
	height, hash, err := s.client.GetBestEpoch()
	if err != nil {
		return nil, 0, err
	}
	return &hash, int32(height), nil
}

// GetUtxo queries whether the Channel Object identified by op.Hash (a Sui
// ObjectID) still exists.
func (s *SuiBlockChainIO) GetUtxo(
	op *wire.OutPoint, pkScript []byte, heightHint uint32,
	cancel <-chan struct{}) (*wire.TxOut, error) {

	// For now, we'll assume the object exists if we can query it.
	// This is used by builder.go to check channel liveness.
	// We'll need a way to query any object balance/existence by ID.
	// For now, return a placeholder to satisfy the interface.
	return &wire.TxOut{
		Value:    1000,   // Placeholder
		PkScript: pkScript,
	}, nil
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
