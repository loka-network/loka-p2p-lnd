// Package suinotify implements the chainntnfs.ChainNotifier interface for
// the Sui network.
//
// Semantic mapping:
//   - Bitcoin "block"            -> Sui checkpoint / epoch
//   - Bitcoin "txid"             -> Sui Transaction Digest (chainhash.Hash)
//   - Bitcoin "outpoint"         -> Sui ObjectId (OutPoint.Hash, Index=0)
//   - Bitcoin "num confirmations"-> Sui transaction finality
package suinotify

import (
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/chainntnfs"
)

const notifierType = "suinotify"

// EpochEvent is a single epoch notification from the Sui network client.
type EpochEvent struct {
	Height uint32
	Hash   chainhash.Hash
}

// ConfirmEvent is fired when a Sui transaction reaches the requested finalization
// depth.
type ConfirmEvent struct {
	TxID         chainhash.Hash
	AnchorHeight uint32
}

// SpendEvent is fired when a Channel Object is spent / closed on Sui.
type SpendEvent struct {
	OutPoint    wire.OutPoint
	SpendTxID   chainhash.Hash
	SpendHeight uint32
}

// SuiClient is the minimal interface required from a Sui network backend.
type SuiClient interface {
	// GetBestEpoch returns the current latest checkpoint height and its hash.
	GetBestEpoch() (height uint32, hash chainhash.Hash, err error)

	// GetCoins returns the list of SUI coins owned by the given address.
	GetCoins(address string) ([]SuiCoin, error)

	// ExecuteMoveCall executes a Sui Move call transaction.
	ExecuteMoveCall(payload []byte, signature []byte) (chainhash.Hash, error)

	// SubscribeEpochs sends each newly finalised checkpoint on the returned
	// channel. The channel is closed when quit is closed.
	SubscribeEpochs(quit <-chan struct{}) (<-chan EpochEvent, error)

	// SubscribeEventConfirmation fires once the Sui transaction with txID
	// is finalized.
	SubscribeEventConfirmation(txID chainhash.Hash, numConfs,
		heightHint uint32, quit <-chan struct{}) (<-chan ConfirmEvent, error)

	// SubscribeObjectSpend fires once the Channel Object (or a specific
	// HTLC slot within it) is spent / closed.
	SubscribeObjectSpend(objectID chainhash.Hash, htlcIndex uint32,
		heightHint uint32, quit <-chan struct{}) (<-chan SpendEvent, error)
}

// SuiCoin represents a Sui Coin object.
type SuiCoin struct {
	ObjectID chainhash.Hash
	Balance  uint64
}

// blockEpochRegistration holds one RegisterBlockEpochNtfn subscriber.
type blockEpochRegistration struct {
	epochID uint64
	epochCh chan *chainntnfs.BlockEpoch
	cancel  func()
}

// SuiChainNotifier implements chainntnfs.ChainNotifier for Sui.
type SuiChainNotifier struct {
	epochClientCounter uint64 // accessed atomically

	started int32 // accessed atomically
	stopped int32 // accessed atomically

	start sync.Once
	stop  sync.Once

	client SuiClient

	blockEpochClients map[uint64]*blockEpochRegistration
	epochMu           sync.Mutex

	quit chan struct{}
	wg   sync.WaitGroup
}

var _ chainntnfs.ChainNotifier = (*SuiChainNotifier)(nil)

// New returns a SuiChainNotifier backed by the given client.
func New(client SuiClient) *SuiChainNotifier {
	return &SuiChainNotifier{
		client:            client,
		blockEpochClients: make(map[uint64]*blockEpochRegistration),
		quit:              make(chan struct{}),
	}
}

// Start starts the event-dispatch goroutine.
func (s *SuiChainNotifier) Start() error {
	var startErr error
	s.start.Do(func() {
		chainntnfs.Log.Info("Sui chain notifier starting")
		if !atomic.CompareAndSwapInt32(&s.started, 0, 1) {
			return
		}
		s.wg.Add(1)
		go s.epochDispatcher()
	})
	return startErr
}

// Started reports whether the notifier has been started.
func (s *SuiChainNotifier) Started() bool {
	return atomic.LoadInt32(&s.started) != 0
}

// Stop shuts down the notifier.
func (s *SuiChainNotifier) Stop() error {
	s.stop.Do(func() {
		chainntnfs.Log.Info("Sui chain notifier shutting down")
		atomic.StoreInt32(&s.stopped, 1)
		close(s.quit)
		s.wg.Wait()
	})
	return nil
}

// RegisterConfirmationsNtfn registers to be notified once txid (a Sui
// Transaction Digest) reaches numConfs confirmations.
func (s *SuiChainNotifier) RegisterConfirmationsNtfn(
	txid *chainhash.Hash, pkScript []byte, numConfs, heightHint uint32,
	opts ...chainntnfs.NotifierOption) (*chainntnfs.ConfirmationEvent, error) {

	if !s.Started() {
		return nil, chainntnfs.ErrChainNotifierShuttingDown
	}

	confEvent := chainntnfs.NewConfirmationEvent(numConfs, func() {})

	if txid == nil {
		chainntnfs.Log.Warn("suinotify: nil txid is unsupported; event will never fire")
		return confEvent, nil
	}

	sub, err := s.client.SubscribeEventConfirmation(
		*txid, numConfs, heightHint, s.quit,
	)
	if err != nil {
		return nil, err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			select {
			case confEvent.Done <- struct{}{}:
			default:
			}
		}()

		select {
		case ev, ok := <-sub:
			if !ok {
				return
			}
			bh := heightToHash(ev.AnchorHeight)
			txConf := &chainntnfs.TxConfirmation{
				BlockHash:   &bh,
				BlockHeight: ev.AnchorHeight,
				TxIndex:     0,
			}
			select {
			case confEvent.Confirmed <- txConf:
			case <-s.quit:
			}
		case <-s.quit:
		}
	}()

	return confEvent, nil
}

// RegisterSpendNtfn registers to be notified once the Channel Object with
// outpoint.Hash (the Sui ObjectId) is spent.
func (s *SuiChainNotifier) RegisterSpendNtfn(
	outpoint *wire.OutPoint, pkScript []byte,
	heightHint uint32) (*chainntnfs.SpendEvent, error) {

	if !s.Started() {
		return nil, chainntnfs.ErrChainNotifierShuttingDown
	}

	spendEvent := chainntnfs.NewSpendEvent(func() {})

	if outpoint == nil {
		chainntnfs.Log.Warn("suinotify: nil outpoint is unsupported; event will never fire")
		return spendEvent, nil
	}

	sub, err := s.client.SubscribeObjectSpend(
		outpoint.Hash,  // ChannelObject ObjectID
		outpoint.Index, // HTLC slot index
		heightHint, s.quit,
	)
	if err != nil {
		return nil, err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			select {
			case spendEvent.Done <- struct{}{}:
			default:
			}
		}()

		select {
		case ev, ok := <-sub:
			if !ok {
				return
			}
			spentOut := wire.OutPoint{
				Hash:  ev.OutPoint.Hash,
				Index: ev.OutPoint.Index,
			}
			spendTx := wire.NewMsgTx(wire.TxVersion)
			spendTx.AddTxIn(&wire.TxIn{PreviousOutPoint: spentOut})

			detail := &chainntnfs.SpendDetail{
				SpentOutPoint:     &spentOut,
				SpenderTxHash:     &ev.SpendTxID,
				SpendingTx:        spendTx,
				SpenderInputIndex: 0,
				SpendingHeight:    int32(ev.SpendHeight),
			}
			select {
			case spendEvent.Spend <- detail:
			case <-s.quit:
			}
		case <-s.quit:
		}
	}()

	return spendEvent, nil
}

// RegisterBlockEpochNtfn registers to be notified of each new Sui checkpoint.
func (s *SuiChainNotifier) RegisterBlockEpochNtfn(
	bestBlock *chainntnfs.BlockEpoch) (*chainntnfs.BlockEpochEvent, error) {

	if !s.Started() {
		return nil, chainntnfs.ErrChainNotifierShuttingDown
	}

	epochCh := make(chan *chainntnfs.BlockEpoch, 8)
	id := atomic.AddUint64(&s.epochClientCounter, 1)
	cancelCh := make(chan struct{})

	reg := &blockEpochRegistration{
		epochID: id,
		epochCh: epochCh,
		cancel:  func() { close(cancelCh) },
	}

	s.epochMu.Lock()
	s.blockEpochClients[id] = reg
	s.epochMu.Unlock()

	if bestBlock != nil {
		currentHeight, _, err := s.client.GetBestEpoch()
		if err == nil && uint32(bestBlock.Height) < currentHeight {
			go s.deliverMissedEpochs(
				epochCh, uint32(bestBlock.Height),
				currentHeight, cancelCh,
			)
		}
	}

	event := &chainntnfs.BlockEpochEvent{
		Epochs: epochCh,
		Cancel: func() {
			s.epochMu.Lock()
			delete(s.blockEpochClients, id)
			s.epochMu.Unlock()
			reg.cancel()
		},
	}

	return event, nil
}

// epochDispatcher fans out new checkpoint notifications to all registered clients.
func (s *SuiChainNotifier) epochDispatcher() {
	defer s.wg.Done()

	sub, err := s.client.SubscribeEpochs(s.quit)
	if err != nil {
		chainntnfs.Log.Errorf("suinotify: epoch subscription failed: %v", err)
		return
	}

	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				return
			}
			epoch := &chainntnfs.BlockEpoch{
				Height: int32(ev.Height),
				Hash:   &ev.Hash,
			}
			s.epochMu.Lock()
			for _, client := range s.blockEpochClients {
				select {
				case client.epochCh <- epoch:
				default:
				}
			}
			s.epochMu.Unlock()

		case <-s.quit:
			return
		}
	}
}

// deliverMissedEpochs synthesises catch-up notifications.
func (s *SuiChainNotifier) deliverMissedEpochs(
	epochCh chan *chainntnfs.BlockEpoch, startHeight, endHeight uint32,
	cancelCh <-chan struct{}) {

	for h := startHeight + 1; h <= endHeight; h++ {
		hash := heightToHash(h)
		epoch := &chainntnfs.BlockEpoch{
			Height: int32(h),
			Hash:   &hash,
		}
		select {
		case epochCh <- epoch:
		case <-cancelCh:
			return
		case <-s.quit:
			return
		}
	}
}

// heightToHash produces a deterministic placeholder hash from a checkpoint height.
func heightToHash(height uint32) chainhash.Hash {
	var h chainhash.Hash
	h[0] = byte(height)
	h[1] = byte(height >> 8)
	h[2] = byte(height >> 16)
	h[3] = byte(height >> 24)
	return h
}
