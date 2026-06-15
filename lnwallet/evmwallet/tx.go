package evmwallet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/lightningnetwork/lnd/keychain"
)

// evmCallPrefix tags a wire.MsgTx whose first input's SignatureScript carries a
// serialized EvmCall envelope. This mirrors the Sui adapter's "SUI_PAYLOAD:"
// convention: the core LND code still passes a wire.MsgTx around, but on an EVM
// node that MsgTx is just an envelope for an EVM contract call.
var evmCallPrefix = []byte("EVM_CALL:")

// gasPriceBufferPct bumps the node's suggested gas price by this percentage
// before broadcasting. SuggestGasPrice reflects the current tip; under rising
// demand a bare suggestion can leave a settlement tx (forceClose, claimHtlc,
// distributeFunds) stuck in the mempool past a deadline, which on a payment
// channel risks funds. A modest buffer trades a little gas for timely
// inclusion; on L2s the absolute cost is negligible.
const gasPriceBufferPct = 25

// EvmCall is the serialized description of an EVM transaction the wallet should
// build, sign (EIP-155) and broadcast. To is the contract/recipient address,
// Data the ABI-encoded call (empty for a plain value transfer), Value the
// native-coin value (usually zero — gas is paid separately, ERC20 value moves
// via Data).
type EvmCall struct {
	To    common.Address `json:"to"`
	Data  []byte         `json:"data"`
	Value *big.Int       `json:"value"`
}

// WrapEvmCall packs an EvmCall into a single-input wire.MsgTx envelope so it can
// travel through the chain-agnostic LND plumbing.
func WrapEvmCall(call EvmCall) (*wire.MsgTx, error) {
	blob, err := json.Marshal(call)
	if err != nil {
		return nil, err
	}

	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{
		SignatureScript: append(append([]byte{}, evmCallPrefix...), blob...),
	})

	return tx, nil
}

// unwrapEvmCall extracts the EvmCall from a wire.MsgTx envelope, or reports
// whether the tx carries one at all.
func unwrapEvmCall(tx *wire.MsgTx) (EvmCall, bool, error) {
	if tx == nil || len(tx.TxIn) == 0 {
		return EvmCall{}, false, nil
	}
	script := tx.TxIn[0].SignatureScript
	if !bytes.HasPrefix(script, evmCallPrefix) {
		return EvmCall{}, false, nil
	}

	var call EvmCall
	if err := json.Unmarshal(script[len(evmCallPrefix):], &call); err != nil {
		return EvmCall{}, true, fmt.Errorf("evmwallet: bad EVM_CALL "+
			"envelope: %w", err)
	}

	return call, true, nil
}

// nodeECDSAKey derives the node's signing key and returns it as a
// crypto/ecdsa.PrivateKey, the form go-ethereum's transaction signer needs.
func (w *Wallet) nodeECDSAKey() (*ecdsa.PrivateKey, common.Address, error) {
	keyDesc, err := w.cfg.KeyRing.DeriveKey(keychain.KeyLocator{
		Family: keychain.KeyFamilyNodeKey,
		Index:  0,
	})
	if err != nil {
		return nil, common.Address{}, err
	}
	priv, err := w.cfg.KeyRing.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, common.Address{}, err
	}

	ecdsaKey := priv.ToECDSA()
	addr := gethcrypto.PubkeyToAddress(ecdsaKey.PublicKey)

	return ecdsaKey, addr, nil
}

// broadcastCall builds a legacy EIP-155 transaction for the given EVM call,
// signs it with the node key, and broadcasts it. It returns the transaction
// hash mapped into LND's chainhash.Hash type.
func (w *Wallet) broadcastCall(ctx context.Context, call EvmCall) (
	chainhash.Hash, error) {

	ecdsaKey, _, err := w.nodeECDSAKey()
	if err != nil {
		return chainhash.Hash{}, err
	}

	return w.broadcastCallFrom(ctx, call, ecdsaKey)
}

// broadcastCallFrom is broadcastCall with an explicit signing key. Channel
// settlement calls (openChannel, forceClose, …) must originate from the
// channel's funding-key address — the contract binds participantA/B to
// openChannel's msg.sender/counterparty and StateUpdate signatures recover to
// those same addresses — so those calls are signed with the per-channel
// multisig key rather than the node key.
func (w *Wallet) broadcastCallFrom(ctx context.Context, call EvmCall,
	ecdsaKey *ecdsa.PrivateKey) (chainhash.Hash, error) {

	var zero chainhash.Hash

	from := gethcrypto.PubkeyToAddress(ecdsaKey.PublicKey)

	chainID, err := w.cfg.Client.ChainID(ctx)
	if err != nil {
		return zero, fmt.Errorf("evmwallet: chainid: %w", err)
	}

	nonce, err := w.cfg.Client.PendingNonceAt(ctx, from)
	if err != nil {
		return zero, fmt.Errorf("evmwallet: nonce: %w", err)
	}

	gasPrice, err := w.cfg.Client.SuggestGasPrice(ctx)
	if err != nil {
		return zero, fmt.Errorf("evmwallet: gas price: %w", err)
	}

	// Apply the buffer: gasPrice = gasPrice * (100 + buffer) / 100.
	gasPrice = new(big.Int).Div(
		new(big.Int).Mul(
			gasPrice, big.NewInt(100+gasPriceBufferPct),
		),
		big.NewInt(100),
	)

	value := call.Value
	if value == nil {
		value = big.NewInt(0)
	}

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &call.To,
		Value:    value,
		Gas:      w.cfg.GasLimit,
		GasPrice: gasPrice,
		Data:     call.Data,
	})

	signer := types.LatestSignerForChainID(chainID)
	signedTx, err := types.SignTx(tx, signer, ecdsaKey)
	if err != nil {
		return zero, fmt.Errorf("evmwallet: sign tx: %w", err)
	}

	if err := w.cfg.Client.SendTransaction(ctx, signedTx); err != nil {
		return zero, fmt.Errorf("evmwallet: send tx: %w", err)
	}

	return chainhash.Hash(signedTx.Hash()), nil
}
