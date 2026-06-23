// Package evmtower implements an EVM-native watchtower for the Loka LND fork.
//
// It closes the H-1 audit gap (see 1-refactor-docs/evm/security-audit.md and
// 1-refactor-docs/evm/evm-watchtower-design.md): the EVM breach remedy is to
// submit a higher-nonce co-signed StateUpdate to the ChannelManager's
// penalize(), which — after the H-1 fix — pays the broadcaster-derived victim
// regardless of who submits the transaction. A protected node can therefore
// hand the latest co-signed state to a third-party tower, which submits
// penalize on its behalf while the node is offline.
//
// Unlike the Bitcoin watchtower (watchtower/{wtclient,wtserver,lookout,...}),
// which is built around UTXO justice transactions, txid breach hints, and
// encrypted per-commitment blobs, the EVM tower is deliberately minimal:
//   - one plaintext JusticeBackup per channel (the latest state — highest
//     nonce wins), because penalize needs only a state strictly newer than the
//     broadcast one;
//   - plaintext is safe (a leaked backup can only submit penalize, which can
//     only ever pay the victim — never the submitter);
//   - breach detection is a direct UnilateralCloseInitiated event → nonce
//     comparison, not a scan of every transaction.
package evmtower

import (
	"encoding/binary"
	"fmt"
	"math/big"
)

// JusticeBackup is everything a tower needs to call penalize for one channel:
// the channel-absolute state fields plus the counterparty's signature over the
// EIP-712 StateUpdate digest. It carries no key material that could move funds
// (penalize always pays the victim), so it is stored and transmitted in
// plaintext.
type JusticeBackup struct {
	// ChannelID is the on-chain ChannelManager channelId.
	ChannelID [32]byte

	// Nonce is this state's StateNum. penalize requires it to be strictly
	// greater than the nonce the cheater broadcast.
	Nonce uint64

	// BalanceA / BalanceB are the channel-absolute balances in raw token
	// base-units (part of the signed digest; not used for payout — penalize
	// sweeps the whole escrow to the victim).
	BalanceA *big.Int
	BalanceB *big.Int

	// HtlcsHash is the Merkle root of the active HTLC set in this state.
	HtlcsHash [32]byte

	// CounterpartySig is the counterparty's 65-byte (r‖s‖v) signature over
	// the EIP-712 StateUpdate digest; it must recover to the broadcaster for
	// penalize to accept the proof.
	CounterpartySig []byte
}

// Validate checks the backup is well-formed enough to attempt a penalize.
func (b *JusticeBackup) Validate() error {
	if b.BalanceA == nil || b.BalanceB == nil {
		return fmt.Errorf("evmtower: backup has nil balance")
	}
	if len(b.CounterpartySig) != 65 {
		return fmt.Errorf("evmtower: backup sig is %d bytes, want 65",
			len(b.CounterpartySig))
	}

	return nil
}

// Encode serializes the backup for persistence. Layout (big-endian):
// channelID[32] | nonce[8] | lenA[2] | balanceA | lenB[2] | balanceB |
// htlcsHash[32] | sig[65]. Balances are length-prefixed big.Int bytes so any
// uint256 fits.
func (b *JusticeBackup) Encode() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}

	aBytes := b.BalanceA.Bytes()
	bBytes := b.BalanceB.Bytes()
	if len(aBytes) > 32 || len(bBytes) > 32 {
		return nil, fmt.Errorf("evmtower: balance exceeds uint256")
	}

	out := make([]byte, 0, 32+8+2+len(aBytes)+2+len(bBytes)+32+65)
	out = append(out, b.ChannelID[:]...)
	out = binary.BigEndian.AppendUint64(out, b.Nonce)
	out = binary.BigEndian.AppendUint16(out, uint16(len(aBytes)))
	out = append(out, aBytes...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(bBytes)))
	out = append(out, bBytes...)
	out = append(out, b.HtlcsHash[:]...)
	out = append(out, b.CounterpartySig...)

	return out, nil
}

// DecodeJusticeBackup is the inverse of Encode.
func DecodeJusticeBackup(data []byte) (*JusticeBackup, error) {
	// Minimum: 32 + 8 + 2 + 0 + 2 + 0 + 32 + 65.
	if len(data) < 141 {
		return nil, fmt.Errorf("evmtower: backup too short: %d", len(data))
	}

	var b JusticeBackup
	off := 0
	copy(b.ChannelID[:], data[off:off+32])
	off += 32
	b.Nonce = binary.BigEndian.Uint64(data[off : off+8])
	off += 8

	lenA := int(binary.BigEndian.Uint16(data[off : off+2]))
	off += 2
	if off+lenA+2 > len(data) {
		return nil, fmt.Errorf("evmtower: truncated balanceA")
	}
	b.BalanceA = new(big.Int).SetBytes(data[off : off+lenA])
	off += lenA

	lenB := int(binary.BigEndian.Uint16(data[off : off+2]))
	off += 2
	if off+lenB+32+65 > len(data) {
		return nil, fmt.Errorf("evmtower: truncated balanceB/tail")
	}
	b.BalanceB = new(big.Int).SetBytes(data[off : off+lenB])
	off += lenB

	copy(b.HtlcsHash[:], data[off:off+32])
	off += 32
	b.CounterpartySig = append([]byte(nil), data[off:off+65]...)

	return &b, b.Validate()
}
