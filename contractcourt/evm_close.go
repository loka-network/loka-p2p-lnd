package contractcourt

import (
	"encoding/binary"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/fn/v2"
	"github.com/lightningnetwork/lnd/input"
)

// EVM close events reach the chain watcher as synthetic spend transactions
// built by evmnotify.buildSpendDetail. The ChannelManager event that "spent"
// the channel rides in an OP_RETURN-style marker in TxOut[0]:
//
//	[0x6a 'E' 'V' 'M' topic0[0] <event-specific payload>]
//
// topic0[0] is the first byte of the event's topic-0 hash (the six close
// topics have distinct first bytes). For UnilateralCloseInitiated the payload
// is the 20-byte broadcaster address followed by the big-endian uint64
// StateUpdate nonce, which is what lets the watcher tell a local from a
// remote force close and dispatch with the right state number.

// decodeEvmSpendMarker extracts the event topic byte and payload from a
// synthetic EVM spend transaction, reporting whether the marker is present.
func decodeEvmSpendMarker(tx *wire.MsgTx) (byte, []byte, bool) {
	if tx == nil || len(tx.TxOut) == 0 {
		return 0, nil, false
	}

	pk := tx.TxOut[0].PkScript
	if len(pk) < 5 || pk[0] != 0x6a ||
		pk[1] != 'E' || pk[2] != 'V' || pk[3] != 'M' {

		return 0, nil, false
	}

	return pk[4], pk[5:], true
}

// handleEvmSpend dispatches a detected ChannelManager close event to the
// matching close path, replacing the Bitcoin state-hint/txid classification
// that cannot apply to synthetic spends.
func (c *chainWatcher) handleEvmSpend(commitSpend *chainntnfs.SpendDetail,
	topicByte byte, payload []byte, chainSet *chainSet) error {

	chanPoint := c.cfg.chanState.FundingOutpoint

	switch topicByte {
	// Cooperative close: the contract already paid both parties, nothing
	// is left to resolve.
	case evmnotify.TopicChannelClosed[0]:
		log.Infof("Cooperative close of ChannelPoint(%v) detected "+
			"via EVM ChannelClosed event", chanPoint)

		return c.dispatchCooperativeClose(commitSpend)

	// Unilateral close: the marker carries the broadcaster address and
	// the broadcast StateUpdate nonce.
	case evmnotify.TopicUnilateralCloseInitiated[0]:
		if len(payload) < 28 {
			return fmt.Errorf("evm unilateral close marker too "+
				"short: %d bytes", len(payload))
		}

		var broadcaster [20]byte
		copy(broadcaster[:], payload[:20])
		nonce := binary.BigEndian.Uint64(payload[20:28])

		localAddr := input.EvmAddressFromPubKey(
			c.cfg.chanState.LocalChanCfg.MultiSigKey.PubKey,
		)
		if broadcaster == localAddr {
			log.Infof("Local unilateral close of "+
				"ChannelPoint(%v) detected via EVM event "+
				"(nonce=%d)", chanPoint, nonce)

			chainSet.commitSet.ConfCommitKey = fn.Some(
				LocalHtlcSet,
			)

			return c.dispatchLocalForceClose(
				commitSpend, nonce, chainSet.commitSet,
			)
		}

		log.Infof("Remote unilateral close of ChannelPoint(%v) "+
			"detected via EVM event (nonce=%d, broadcaster=%x)",
			chanPoint, nonce, broadcaster)

		chainSet.commitSet.ConfCommitKey = fn.Some(RemoteHtlcSet)

		return c.dispatchRemoteForceClose(
			commitSpend, chainSet.remoteCommit, chainSet.commitSet,
			c.cfg.chanState.RemoteCurrentRevocation,
		)

	// HTLC claims/timeouts and the final distribution do not close the
	// channel by themselves; the channel was already dispatched on the
	// preceding ChannelClosed/UnilateralCloseInitiated event.
	default:
		log.Warnf("ChannelPoint(%v): unhandled EVM close event "+
			"topic byte %#x — ignoring", chanPoint, topicByte)

		return nil
	}
}
