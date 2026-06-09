// Package evmnotify implements chainntnfs.ChainNotifier for EVM-compatible
// chains. Where the Sui notifier consumes push subscriptions, the EVM notifier
// polls the JSON-RPC node — receipt depth drives confirmations, contract event
// logs (filtered by channelId topic) drive spends, and header polling drives
// block epochs. Polling works on HTTP-only endpoints and is resilient to the WS
// subscription drops L2 sequencers are prone to (testing-verification §3).
package evmnotify

import (
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/lightningnetwork/lnd/chainntnfs"
)

// DefaultPollInterval is how often the notifier polls the node for new blocks,
// receipts and logs.
const DefaultPollInterval = 3 * time.Second

// maxSpendTxOutputs pads the synthetic spend transaction so LND's
// Bitcoin-centric watchers (arbitrator, sweeper) can index any commitment
// output without a slice out-of-range panic. 483 local + 483 remote HTLCs + 2
// balances + 2 anchors = 970 max legal outputs; 1000 is a safe ceiling.
const maxSpendTxOutputs = 1000

// closeTopics are the contract event topic-0 hashes that signify a channel
// outpoint has been "spent" (closed, force-closed, settled or punished).
var closeTopics = map[common.Hash]struct{}{
	TopicChannelClosed:            {},
	TopicUnilateralCloseInitiated: {},
	TopicHTLCClaimed:              {},
	TopicHTLCTimeout:              {},
	TopicChannelPunished:          {},
	TopicFundsDistributed:         {},
}

// blockEpochRegistration holds one RegisterBlockEpochNtfn subscriber.
type blockEpochRegistration struct {
	epochCh chan *chainntnfs.BlockEpoch
	cancel  func()
}

// EvmChainNotifier implements chainntnfs.ChainNotifier for EVM chains.
type EvmChainNotifier struct {
	epochClientCounter uint64 // atomic
	started            int32  // atomic
	stopped            int32  // atomic
	bestHeight         int64  // atomic

	start sync.Once
	stop  sync.Once

	client       EvmClient
	contractAddr common.Address
	pollInterval time.Duration

	blockEpochClients map[uint64]*blockEpochRegistration
	epochMu           sync.Mutex

	quit chan struct{}
	wg   sync.WaitGroup
}

var _ chainntnfs.ChainNotifier = (*EvmChainNotifier)(nil)

// New returns an EvmChainNotifier watching the given ChannelManager contract.
func New(client EvmClient, contractAddr common.Address) *EvmChainNotifier {
	return &EvmChainNotifier{
		client:            client,
		contractAddr:      contractAddr,
		pollInterval:      DefaultPollInterval,
		blockEpochClients: make(map[uint64]*blockEpochRegistration),
		quit:              make(chan struct{}),
	}
}

// Start launches the block-poller goroutine.
func (n *EvmChainNotifier) Start() error {
	n.start.Do(func() {
		chainntnfs.Log.Info("EVM chain notifier starting")
		if !atomic.CompareAndSwapInt32(&n.started, 0, 1) {
			return
		}
		n.wg.Add(1)
		go n.blockPoller()
	})

	return nil
}

// Started reports whether the notifier has been started.
func (n *EvmChainNotifier) Started() bool {
	return atomic.LoadInt32(&n.started) != 0
}

// Stop shuts down the notifier.
func (n *EvmChainNotifier) Stop() error {
	n.stop.Do(func() {
		chainntnfs.Log.Info("EVM chain notifier shutting down")
		atomic.StoreInt32(&n.stopped, 1)
		close(n.quit)
		n.wg.Wait()

		n.epochMu.Lock()
		for _, reg := range n.blockEpochClients {
			close(reg.epochCh)
		}
		n.epochMu.Unlock()
	})

	return nil
}

// blockPoller polls the node tip and fans out a BlockEpoch to every registered
// client on each new block.
func (n *EvmChainNotifier) blockPoller() {
	defer n.wg.Done()

	ticker := time.NewTicker(n.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			n.pollTip()
		case <-n.quit:
			return
		}
	}
}

// pollTip queries the latest header and, if the height advanced, dispatches it.
func (n *EvmChainNotifier) pollTip() {
	ctx, cancel := context.WithTimeout(context.Background(), n.pollInterval)
	defer cancel()

	hdr, err := n.client.HeaderByNumber(ctx, nil)
	if err != nil {
		chainntnfs.Log.Warnf("evmnotify: header poll failed: %v", err)
		return
	}

	height := hdr.Number.Int64()
	if height <= atomic.LoadInt64(&n.bestHeight) {
		return
	}
	atomic.StoreInt64(&n.bestHeight, height)

	hash := chainhash.Hash(hdr.Hash())
	epoch := &chainntnfs.BlockEpoch{
		Hash:   &hash,
		Height: int32(height),
	}

	n.epochMu.Lock()
	for _, reg := range n.blockEpochClients {
		select {
		case reg.epochCh <- epoch:
		case <-n.quit:
		default:
		}
	}
	n.epochMu.Unlock()
}

// RegisterConfirmationsNtfn registers to be notified once txid (an EVM tx hash)
// reaches numConfs confirmations, judged by receipt depth.
func (n *EvmChainNotifier) RegisterConfirmationsNtfn(txid *chainhash.Hash,
	pkScript []byte, numConfs, heightHint uint32,
	_ ...chainntnfs.NotifierOption) (*chainntnfs.ConfirmationEvent, error) {

	if !n.Started() {
		return nil, chainntnfs.ErrChainNotifierShuttingDown
	}

	confEvent := chainntnfs.NewConfirmationEvent(numConfs, func() {})
	if txid == nil {
		chainntnfs.Log.Warn("evmnotify: nil txid; conf event will " +
			"never fire")

		return confEvent, nil
	}

	n.wg.Add(1)
	go n.waitForConfirmation(*txid, pkScript, numConfs, confEvent)

	return confEvent, nil
}

// waitForConfirmation polls the receipt for txHash until it is buried under
// numConfs blocks, then fires the confirmation.
func (n *EvmChainNotifier) waitForConfirmation(txHash chainhash.Hash,
	pkScript []byte, numConfs uint32,
	confEvent *chainntnfs.ConfirmationEvent) {

	defer n.wg.Done()
	defer func() {
		select {
		case confEvent.Done <- struct{}{}:
		default:
		}
	}()

	ticker := time.NewTicker(n.pollInterval)
	defer ticker.Stop()

	ethHash := common.Hash(txHash)
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(
				context.Background(), n.pollInterval,
			)
			receipt, err := n.client.TransactionReceipt(ctx, ethHash)
			cancel()
			if err != nil || receipt == nil {
				continue
			}

			tip := atomic.LoadInt64(&n.bestHeight)
			confs := tip - receipt.BlockNumber.Int64() + 1
			if confs < int64(numConfs) {
				continue
			}

			bh := chainhash.Hash(receipt.BlockHash)
			fakeTx := wire.NewMsgTx(2)
			fakeTx.AddTxOut(&wire.TxOut{PkScript: pkScript})
			select {
			case confEvent.Confirmed <- &chainntnfs.TxConfirmation{
				BlockHash:   &bh,
				BlockHeight: uint32(receipt.BlockNumber.Int64()),
				TxIndex:     uint32(receipt.TransactionIndex),
				Tx:          fakeTx,
			}:
			case <-n.quit:
			}

			return
		case <-n.quit:
			return
		}
	}
}

// RegisterSpendNtfn registers to be notified once the channel identified by
// outpoint.Hash (the 32-byte channelId) is closed/settled on-chain, detected by
// a ChannelManager settlement event log.
func (n *EvmChainNotifier) RegisterSpendNtfn(outpoint *wire.OutPoint,
	pkScript []byte, heightHint uint32) (*chainntnfs.SpendEvent, error) {

	if !n.Started() {
		return nil, chainntnfs.ErrChainNotifierShuttingDown
	}

	spendEvent := chainntnfs.NewSpendEvent(func() {})
	if outpoint == nil {
		chainntnfs.Log.Warn("evmnotify: nil outpoint; spend event " +
			"will never fire")

		return spendEvent, nil
	}

	n.wg.Add(1)
	go n.waitForSpend(*outpoint, heightHint, spendEvent)

	return spendEvent, nil
}

// waitForSpend polls contract logs for a settlement event referencing the
// channelId, then fires the spend notification.
func (n *EvmChainNotifier) waitForSpend(outpoint wire.OutPoint,
	heightHint uint32, spendEvent *chainntnfs.SpendEvent) {

	defer n.wg.Done()
	defer func() {
		select {
		case spendEvent.Done <- struct{}{}:
		default:
		}
	}()

	// channelId is the indexed first event arg, i.e. topics[1].
	channelTopic := common.Hash(outpoint.Hash)
	fromBlock := big.NewInt(int64(heightHint))

	ticker := time.NewTicker(n.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(
				context.Background(), n.pollInterval,
			)
			logs, err := n.client.FilterLogs(ctx, ethereum.FilterQuery{
				FromBlock: fromBlock,
				Addresses: []common.Address{n.contractAddr},
				Topics: [][]common.Hash{
					nil, {channelTopic},
				},
			})
			cancel()
			if err != nil {
				chainntnfs.Log.Warnf("evmnotify: log filter "+
					"failed: %v", err)

				continue
			}

			match, ok := firstCloseLog(logs)
			if !ok {
				continue
			}

			detail := n.buildSpendDetail(outpoint, match)
			select {
			case spendEvent.Spend <- detail:
			case <-n.quit:
			}

			return
		case <-n.quit:
			return
		}
	}
}

// firstCloseLog returns the first log whose topic-0 is a close/settle event.
func firstCloseLog(logs []types.Log) (types.Log, bool) {
	for _, l := range logs {
		if len(l.Topics) == 0 {
			continue
		}
		if _, ok := closeTopics[l.Topics[0]]; ok {
			return l, true
		}
	}

	return types.Log{}, false
}

// buildSpendDetail synthesizes a SpendDetail from a settlement log. The spending
// transaction is padded (see maxSpendTxOutputs) and carries an OP_RETURN-style
// payload encoding the event kind; for HTLCClaimed the revealed preimage (the
// log's first data word) is included so the upstream htlcSuccessResolver can
// settle the incoming HTLC.
func (n *EvmChainNotifier) buildSpendDetail(outpoint wire.OutPoint,
	l types.Log) *chainntnfs.SpendDetail {

	spendTx := wire.NewMsgTx(2)

	// payload: [0x6a OP_RETURN]['E','V','M'][topic0[0]] [+ preimage if any].
	payload := []byte{0x6a, 'E', 'V', 'M', l.Topics[0][0]}
	if l.Topics[0] == TopicHTLCClaimed && len(l.Data) >= 32 {
		payload = append(payload, l.Data[:32]...)
	}
	spendTx.AddTxOut(&wire.TxOut{PkScript: payload})
	for i := 1; i < maxSpendTxOutputs; i++ {
		spendTx.AddTxOut(&wire.TxOut{})
	}

	spenderTxHash := chainhash.Hash(l.TxHash)

	return &chainntnfs.SpendDetail{
		SpentOutPoint:     &outpoint,
		SpenderTxHash:     &spenderTxHash,
		SpendingTx:        spendTx,
		SpenderInputIndex: 0,
		SpendingHeight:    int32(l.BlockNumber),
	}
}

// RegisterBlockEpochNtfn registers to be notified of each new EVM block.
func (n *EvmChainNotifier) RegisterBlockEpochNtfn(
	_ *chainntnfs.BlockEpoch) (*chainntnfs.BlockEpochEvent, error) {

	if !n.Started() {
		return nil, chainntnfs.ErrChainNotifierShuttingDown
	}

	epochCh := make(chan *chainntnfs.BlockEpoch, 8)
	id := atomic.AddUint64(&n.epochClientCounter, 1)

	reg := &blockEpochRegistration{epochCh: epochCh}
	n.epochMu.Lock()
	n.blockEpochClients[id] = reg
	n.epochMu.Unlock()

	return &chainntnfs.BlockEpochEvent{
		Epochs: epochCh,
		Cancel: func() {
			n.epochMu.Lock()
			delete(n.blockEpochClients, id)
			n.epochMu.Unlock()
		},
	}, nil
}
