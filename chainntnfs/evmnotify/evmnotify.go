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

	// decimals caches the escrow token's ERC20 decimals, resolved lazily
	// on the first ChannelOpened confirmation.
	decimalsMu    sync.Mutex
	decimals      uint8
	decimalsKnown bool

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
	go n.waitForConfirmation(*txid, pkScript, numConfs, heightHint,
		confEvent)

	return confEvent, nil
}

// maxLogRange bounds the block span of an eth_getLogs query. Public RPC
// endpoints reject wide ranges (e.g. publicnode caps at 50000), so every
// FilterLogs the notifier issues must stay within this window.
const maxLogRange = 45000

// evmReorgSafetyDepth is how many blocks a close/settlement log must be buried
// by before the spend watcher acts on it, mirroring the confirmation path's
// depth gate (lncfg.DefaultEvmNumConfs). It prevents dispatching a spend for a
// close that an L2 sequencer reorg could still drop (audit M-5). Kept here as a
// const rather than threaded from config to avoid an lncfg import cycle.
const evmReorgSafetyDepth = 3

// logFromBlock returns the FromBlock for a FilterLogs query that wants to start
// at heightHint but never spans more than maxLogRange blocks back from the
// current tip (the events the notifier hunts for — ChannelOpened receipts,
// settlement spends — are always recent relative to the tip, so a capped
// window finds them while satisfying the RPC's range limit).
func (n *EvmChainNotifier) logFromBlock(heightHint uint32) *big.Int {
	from := int64(heightHint)
	if floor := atomic.LoadInt64(&n.bestHeight) - maxLogRange; from < floor {
		from = floor
	}
	if from < 0 {
		from = 0
	}

	return big.NewInt(from)
}

// waitForConfirmation polls until the registered hash is buried under
// numConfs blocks, then fires the confirmation. The hash is matched two ways,
// because LND registers two kinds of identifiers through the unchanged
// ChainNotifier interface:
//
//   - an actual EVM transaction hash (settlement calls): matched by receipt;
//   - the 32-byte channelId (funding: the funding "txid" IS the channelId,
//     per the OutPoint.Hash ↔ channelId mapping): matched by the
//     ChannelOpened log carrying that channelId as its indexed topic.
func (n *EvmChainNotifier) waitForConfirmation(txHash chainhash.Hash,
	pkScript []byte, numConfs, heightHint uint32,
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
			target, found := n.locateConfTarget(
				ctx, ethHash, heightHint,
			)
			cancel()
			if !found {
				continue
			}

			tip := atomic.LoadInt64(&n.bestHeight)
			confs := tip - target.height + 1
			if confs < int64(numConfs) {
				continue
			}

			bh := chainhash.Hash(target.blockHash)

			// The synthetic 0-in/1-out tx satisfies lnwallet's
			// ValidateChannel capacity check: value carries the
			// channel capacity in internal units when the target
			// was a ChannelOpened event (zero otherwise).
			fakeTx := wire.NewMsgTx(2)
			fakeTx.AddTxOut(&wire.TxOut{
				Value:    target.value,
				PkScript: pkScript,
			})

			// LND's ShortChannelID packs the confirmation height
			// into 24 bits (max ~16.7M). EVM L2 block heights blow
			// past that (Base Sepolia is already >40M), so the
			// raw height overflows SCID/backup serialization
			// ("block height should fit in 3 bytes"). Reduce it
			// mod 2^24 — the documented escape hatch (integration
			// doc §6.1.3, mirroring lnwire.NewEvmShortChanID).
			// The confirmation DEPTH was already satisfied above
			// from the real height, and both peers observe the
			// same block, so their SCIDs still agree.
			scidHeight := uint32(target.height % (1 << 24))

			select {
			case confEvent.Confirmed <- &chainntnfs.TxConfirmation{
				BlockHash:   &bh,
				BlockHeight: scidHeight,
				TxIndex:     target.txIndex,
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

// confTarget is the resolved on-chain location of a confirmation
// registration, plus the channel capacity in LND-internal units when the
// registration matched a ChannelOpened event.
type confTarget struct {
	height    int64
	txIndex   uint32
	blockHash common.Hash
	value     int64
}

// locateConfTarget resolves the registered hash to on-chain coordinates,
// first as a transaction hash (receipt lookup), then as a channelId (a
// ChannelOpened log with that indexed topic).
func (n *EvmChainNotifier) locateConfTarget(ctx context.Context,
	hash common.Hash, heightHint uint32) (confTarget, bool) {

	receipt, err := n.client.TransactionReceipt(ctx, hash)
	if err == nil && receipt != nil {
		return confTarget{
			height:    receipt.BlockNumber.Int64(),
			txIndex:   uint32(receipt.TransactionIndex),
			blockHash: receipt.BlockHash,
		}, true
	}

	logs, err := n.client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: n.logFromBlock(heightHint),
		Addresses: []common.Address{n.contractAddr},
		Topics: [][]common.Hash{
			{TopicChannelOpened},
			{hash},
		},
	})
	if err != nil || len(logs) == 0 {
		return confTarget{}, false
	}

	l := logs[0]
	target := confTarget{
		height:    int64(l.BlockNumber),
		txIndex:   uint32(l.TxIndex),
		blockHash: l.BlockHash,
	}

	// Recover the channel capacity from the event's deposits so the
	// synthetic confirmation tx passes lnwallet's capacity validation.
	balA, balB, err := UnpackChannelOpened(l.Data)
	if err != nil {
		chainntnfs.Log.Errorf("evmnotify: bad ChannelOpened data: %v",
			err)

		return target, true
	}
	decimals, err := n.tokenDecimals(ctx)
	if err != nil {
		chainntnfs.Log.Errorf("evmnotify: token decimals: %v", err)

		return target, true
	}
	target.value = scaleRawToInternal(
		new(big.Int).Add(balA, balB), decimals,
	)

	return target, true
}

// tokenDecimals lazily resolves and caches the escrow token's decimals via
// ChannelManager.token() → ERC20.decimals().
func (n *EvmChainNotifier) tokenDecimals(ctx context.Context) (uint8, error) {
	n.decimalsMu.Lock()
	defer n.decimalsMu.Unlock()

	if n.decimalsKnown {
		return n.decimals, nil
	}

	tokenCall, err := PackToken()
	if err != nil {
		return 0, err
	}
	out, err := n.client.CallContract(ctx, ethereum.CallMsg{
		To:   &n.contractAddr,
		Data: tokenCall,
	}, nil)
	if err != nil {
		return 0, err
	}
	tokenAddr, err := UnpackToken(out)
	if err != nil {
		return 0, err
	}

	decCall, err := PackDecimals()
	if err != nil {
		return 0, err
	}
	out, err = n.client.CallContract(ctx, ethereum.CallMsg{
		To:   &tokenAddr,
		Data: decCall,
	}, nil)
	if err != nil {
		return 0, err
	}
	dec, err := UnpackDecimals(out)
	if err != nil {
		return 0, err
	}

	n.decimals = dec
	n.decimalsKnown = true

	return dec, nil
}

// internalDecimals mirrors the Decimals Scaling Factor's internal scale
// (1 token = 1e8 internal units, evmwallet/amounts.go).
const internalDecimals = 8

// scaleRawToInternal converts raw token base-units into LND-internal units.
// It mirrors evmwallet.ScaleToInternal (which cannot be imported here —
// evmwallet depends on this package).
func scaleRawToInternal(raw *big.Int, tokenDecimals uint8) int64 {
	scaled := new(big.Int).Mul(
		raw, new(big.Int).Exp(
			big.NewInt(10), big.NewInt(internalDecimals), nil,
		),
	)
	scaled.Quo(scaled, new(big.Int).Exp(
		big.NewInt(10), big.NewInt(int64(tokenDecimals)), nil,
	))

	return scaled.Int64()
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

	ticker := time.NewTicker(n.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(
				context.Background(), n.pollInterval,
			)
			// Re-evaluate the window each tick so it stays within the
			// RPC's range cap as the tip advances.
			logs, err := n.client.FilterLogs(ctx, ethereum.FilterQuery{
				FromBlock: n.logFromBlock(heightHint),
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

			// Reorg safety: wait until the close log is buried by
			// evmReorgSafetyDepth blocks before acting on it, so an
			// L2 sequencer reorg that drops the close before then is
			// never dispatched as a spend (audit M-5). Re-poll until
			// the depth is reached.
			tip := atomic.LoadInt64(&n.bestHeight)
			if tip-int64(match.BlockNumber)+1 < evmReorgSafetyDepth {
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

	// payload: [0x6a OP_RETURN]['E','V','M'][topic0[0]] [+ event extras].
	payload := []byte{0x6a, 'E', 'V', 'M', l.Topics[0][0]}
	switch {
	case l.Topics[0] == TopicHTLCClaimed && len(l.Data) >= 32:
		payload = append(payload, l.Data[:32]...)

	// UnilateralCloseInitiated(channelId idx, broadcaster, nonce,
	// balanceA, balanceB, challengeExpiry): embed the broadcaster address,
	// the uint64 tail of the nonce and of the challenge deadline so the
	// chain watcher can tell a local from a remote force close, at which
	// state, and when the settler may call distributeFunds.
	case l.Topics[0] == TopicUnilateralCloseInitiated && len(l.Data) >= 160:
		payload = append(payload, l.Data[12:32]...)   // broadcaster
		payload = append(payload, l.Data[56:64]...)   // nonce (BE tail)
		payload = append(payload, l.Data[152:160]...) // challengeExpiry
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
