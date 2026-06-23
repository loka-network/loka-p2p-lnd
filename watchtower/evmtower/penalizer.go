package evmtower

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
)

const (
	// penalizeGasLimit is the gas ceiling for a penalize call (one
	// ecrecover + a token transfer).
	penalizeGasLimit = 500_000

	// gasPriceBufferPct bumps the suggested gas price so the justice tx
	// lands promptly within the challenge window.
	gasPriceBufferPct = 25
)

// EvmPenalizer submits penalize transactions signed by a tower-owned relayer
// key. The relayer is not a channel participant — the contract pays the
// broadcaster-derived victim regardless of msg.sender (H-1), so the key needs
// only native gas, never a stake in the channel.
type EvmPenalizer struct {
	// Client is the EVM RPC client used to broadcast.
	Client evmnotify.EvmClient

	// Contract is the ChannelManager address.
	Contract common.Address

	// Key is the relayer key that signs and pays gas for penalize.
	Key *ecdsa.PrivateKey
}

// Penalize builds and broadcasts a penalize call carrying the backup's
// higher-nonce co-signed state.
func (p *EvmPenalizer) Penalize(ctx context.Context, b *JusticeBackup) error {
	if err := b.Validate(); err != nil {
		return err
	}

	data, err := evmnotify.PackPenalize(
		b.ChannelID, new(big.Int).SetUint64(b.Nonce),
		b.BalanceA, b.BalanceB, b.HtlcsHash, b.CounterpartySig,
	)
	if err != nil {
		return fmt.Errorf("evmtower: pack penalize: %w", err)
	}

	chainID, err := p.Client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("evmtower: chainid: %w", err)
	}

	from := gethcrypto.PubkeyToAddress(p.Key.PublicKey)
	nonce, err := p.Client.PendingNonceAt(ctx, from)
	if err != nil {
		return fmt.Errorf("evmtower: nonce: %w", err)
	}

	gasPrice, err := p.Client.SuggestGasPrice(ctx)
	if err != nil {
		return fmt.Errorf("evmtower: gas price: %w", err)
	}
	gasPrice = new(big.Int).Div(
		new(big.Int).Mul(gasPrice, big.NewInt(100+gasPriceBufferPct)),
		big.NewInt(100),
	)

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &p.Contract,
		Value:    big.NewInt(0),
		Gas:      penalizeGasLimit,
		GasPrice: gasPrice,
		Data:     data,
	})

	signed, err := types.SignTx(
		tx, types.LatestSignerForChainID(chainID), p.Key,
	)
	if err != nil {
		return fmt.Errorf("evmtower: sign penalize: %w", err)
	}

	return p.Client.SendTransaction(ctx, signed)
}
