package evmwallet

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/ethereum/go-ethereum/common"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/lightningnetwork/lnd/chainntnfs/evmnotify"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

// waitMinedPollInterval is how often waitMined re-checks for a receipt.
const waitMinedPollInterval = 500 * time.Millisecond

// This file is the carrier-translation layer the input-package call carriers
// (input/evm_channel_tx.go) promise: it decodes the JSON envelope riding in
// TxIn[0].SignatureScript, applies the Decimals Scaling Factor where the
// payload is in LND-internal units (openChannel only — settlement payloads
// already carry raw base-units), ABI-encodes the ChannelManager call, and
// broadcasts it EIP-155 signed. For openChannel it first grants the contract
// the ERC20 allowance the deposit pull (safeTransferFrom) requires; the two
// transactions are nonce-ordered, so the allowance is in place when
// openChannel executes.

// executeCarrier translates and broadcasts an input.BuildEvmCallTx carrier.
// It returns the EVM transaction hash of the principal ChannelManager call.
func (w *Wallet) executeCarrier(ctx context.Context, tx *wire.MsgTx) (
	chainhash.Hash, error) {

	var zero chainhash.Hash

	channelID, callType, payload, err := input.DecodeEvmCallTx(tx)
	if err != nil {
		return zero, err
	}

	contract := common.HexToAddress(w.cfg.Params.ContractAddr)
	cid := [32]byte(channelID)

	var data []byte
	switch callType {
	case input.EvmCallChannelOpen:
		return w.executeOpenChannel(ctx, payload)

	case input.EvmCallChannelClose:
		var p input.EvmChannelClosePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return zero, fmt.Errorf("evmwallet: bad %s payload: %w",
				callType, err)
		}

		finalA, err := parseRawAmount(p.FinalBalanceA)
		if err != nil {
			return zero, err
		}
		finalB, err := parseRawAmount(p.FinalBalanceB)
		if err != nil {
			return zero, err
		}
		sigA, err := hex.DecodeString(p.SigA)
		if err != nil {
			return zero, fmt.Errorf("evmwallet: bad sigA hex: %w",
				err)
		}
		sigB, err := hex.DecodeString(p.SigB)
		if err != nil {
			return zero, fmt.Errorf("evmwallet: bad sigB hex: %w",
				err)
		}

		data, err = evmnotify.PackCloseChannel(
			cid, new(big.Int).SetUint64(p.Nonce), finalA, finalB,
			sigA, sigB,
		)
		if err != nil {
			return zero, err
		}

	case input.EvmCallForceClose, input.EvmCallPenalize:
		var p input.EvmStateClosePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return zero, fmt.Errorf("evmwallet: bad %s payload: %w",
				callType, err)
		}

		balA, err := parseRawAmount(p.BalanceA)
		if err != nil {
			return zero, err
		}
		balB, err := parseRawAmount(p.BalanceB)
		if err != nil {
			return zero, err
		}
		htlcsHash, err := parseHex32(p.HtlcsHash)
		if err != nil {
			return zero, err
		}
		sig, err := hex.DecodeString(p.Sig)
		if err != nil {
			return zero, fmt.Errorf("evmwallet: bad sig hex: %w",
				err)
		}

		nonce := new(big.Int).SetUint64(p.Nonce)
		if callType == input.EvmCallForceClose {
			data, err = evmnotify.PackForceClose(
				cid, nonce, balA, balB, htlcsHash, sig,
			)
		} else {
			data, err = evmnotify.PackPenalize(
				cid, nonce, balA, balB, htlcsHash, sig,
			)
		}
		if err != nil {
			return zero, err
		}

		// The contract only accepts forceClose/penalize from a
		// participant, i.e. the funding-key account (provisioned with
		// gas headroom at open).
		if p.LocalKey != "" {
			chanKey, _, err := w.channelECDSAKey(p.LocalKey)
			if err != nil {
				return zero, err
			}

			return w.broadcastCallFrom(
				ctx, EvmCall{To: contract, Data: data},
				chanKey,
			)
		}

	case input.EvmCallClaimHtlc, input.EvmCallTimeoutHtlc:
		var p input.EvmHtlcResolvePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return zero, fmt.Errorf("evmwallet: bad %s payload: %w",
				callType, err)
		}

		htlcArg, err := htlcDataToArg(p.HTLC)
		if err != nil {
			return zero, err
		}
		proof, err := parseProof(p.MerkleProof)
		if err != nil {
			return zero, err
		}

		if callType == input.EvmCallClaimHtlc {
			preimage, err := parseHex32(p.Preimage)
			if err != nil {
				return zero, err
			}
			data, err = evmnotify.PackClaimHtlc(
				cid, htlcArg, proof, preimage,
			)
			if err != nil {
				return zero, err
			}
		} else {
			data, err = evmnotify.PackTimeoutHtlc(
				cid, htlcArg, proof,
			)
			if err != nil {
				return zero, err
			}
		}

	case input.EvmCallDistributeFunds:
		data, err = evmnotify.PackDistributeFunds(cid)
		if err != nil {
			return zero, err
		}

	default:
		return zero, fmt.Errorf("evmwallet: unsupported carrier call "+
			"type %q", callType)
	}

	return w.broadcastCall(ctx, EvmCall{To: contract, Data: data})
}

// fundingGasHeadroom multiplies the per-call gas budget when provisioning the
// channel account with native coin: it must cover approve + openChannel now
// and the eventual settlement calls (forceClose/claimHtlc/distributeFunds)
// later.
const fundingGasHeadroom = 10

// executeOpenChannel handles the ChannelOpen carrier. The contract binds
// participantA to openChannel's msg.sender, and every later StateUpdate
// signature must ECDSA-recover to that same address — which is the channel's
// funding (multisig) key. So the call must originate from the per-channel
// funding-key account, which starts empty. The wallet therefore provisions it
// from the node account first:
//
//  1. native-coin transfer (gas for approve/openChannel and later settlement),
//  2. ERC20 transfer of the deposit,
//  3. approve(ChannelManager, deposit) — signed by the channel key,
//  4. openChannel(salt, counterparty, deposit, remote) — channel key.
//
// Steps wait for inclusion where a later step spends the earlier one's funds,
// so the flow also works on non-automining chains.
func (w *Wallet) executeOpenChannel(ctx context.Context,
	payload json.RawMessage) (chainhash.Hash, error) {

	var zero chainhash.Hash

	var p input.EvmChannelOpenPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return zero, fmt.Errorf("evmwallet: bad ChannelOpen "+
			"payload: %w", err)
	}

	saltBytes, err := parseHex32(p.Salt)
	if err != nil {
		return zero, err
	}
	cpBytes, err := hex.DecodeString(p.Counterparty)
	if err != nil || len(cpBytes) != 20 {
		return zero, fmt.Errorf("evmwallet: bad counterparty %q",
			p.Counterparty)
	}
	counterparty := common.BytesToAddress(cpBytes)

	chanKey, chanAddr, err := w.channelECDSAKey(p.LocalKey)
	if err != nil {
		return zero, err
	}

	localRaw := ScaleToBase(
		btcutil.Amount(p.LocalBalance), w.cfg.TokenDecimals,
	)
	remoteRaw := ScaleToBase(
		btcutil.Amount(p.RemoteBalance), w.cfg.TokenDecimals,
	)

	contract := common.HexToAddress(w.cfg.Params.ContractAddr)
	token := common.HexToAddress(w.cfg.Params.TokenAddress)

	if localRaw.Sign() > 0 {
		// Provision gas: the channel account broadcasts approve +
		// openChannel now and the settlement calls at close.
		gasPrice, err := w.cfg.Client.SuggestGasPrice(ctx)
		if err != nil {
			return zero, fmt.Errorf("evmwallet: gas price: %w", err)
		}
		gasBudget := new(big.Int).Mul(
			gasPrice, new(big.Int).SetUint64(
				w.cfg.GasLimit*fundingGasHeadroom,
			),
		)
		fundHash, err := w.broadcastCall(ctx, EvmCall{
			To:    chanAddr,
			Value: gasBudget,
		})
		if err != nil {
			return zero, fmt.Errorf("evmwallet: gas funding: %w",
				err)
		}

		// Provision the deposit itself.
		transferData, err := evmnotify.PackTransfer(chanAddr, localRaw)
		if err != nil {
			return zero, err
		}
		tokenHash, err := w.broadcastCall(ctx, EvmCall{
			To:   token,
			Data: transferData,
		})
		if err != nil {
			return zero, fmt.Errorf("evmwallet: deposit "+
				"transfer: %w", err)
		}

		// The channel account spends both transfers next, so they must
		// be mined first.
		if err := w.waitMined(ctx, fundHash); err != nil {
			return zero, err
		}
		if err := w.waitMined(ctx, tokenHash); err != nil {
			return zero, err
		}

		// Allowance for the contract's safeTransferFrom pull, from the
		// channel account.
		approveData, err := evmnotify.PackApprove(contract, localRaw)
		if err != nil {
			return zero, err
		}
		_, err = w.broadcastCallFrom(ctx, EvmCall{
			To:   token,
			Data: approveData,
		}, chanKey)
		if err != nil {
			return zero, fmt.Errorf("evmwallet: approve: %w", err)
		}
	}

	// counterpartySig is empty: the LND funding flow currently opens
	// single-funded channels (remoteRaw == 0), for which the contract
	// requires no counterparty consent signature. Dual-funded opens
	// (remoteRaw > 0) would need the counterparty's EIP-712 OpenChannel
	// signature exchanged during funding negotiation (audit M-3) — not yet
	// wired, so a dual-funded open fails closed at the contract rather than
	// silently pulling the counterparty's deposit without consent.
	openData, err := evmnotify.PackOpenChannel(
		saltBytes, counterparty, localRaw, remoteRaw, nil,
	)
	if err != nil {
		return zero, err
	}

	return w.broadcastCallFrom(
		ctx, EvmCall{To: contract, Data: openData}, chanKey,
	)
}

// channelECDSAKey derives the per-channel funding key from the carrier's
// compressed funding pubkey via the keyring's in-order scan, returning it in
// go-ethereum form along with its EVM account address.
func (w *Wallet) channelECDSAKey(localKeyHex string) (*ecdsa.PrivateKey,
	common.Address, error) {

	pubBytes, err := hex.DecodeString(localKeyHex)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("evmwallet: bad "+
			"local key hex: %w", err)
	}
	pub, err := btcec.ParsePubKey(pubBytes)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("evmwallet: bad "+
			"local key: %w", err)
	}

	priv, err := w.cfg.KeyRing.DerivePrivKey(keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: keychain.KeyFamilyMultiSig,
		},
		PubKey: pub,
	})
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("evmwallet: derive "+
			"channel key: %w", err)
	}

	ecdsaKey := priv.ToECDSA()

	return ecdsaKey, gethcrypto.PubkeyToAddress(ecdsaKey.PublicKey), nil
}

// waitMined polls until the transaction has a receipt or the context expires.
func (w *Wallet) waitMined(ctx context.Context, hash chainhash.Hash) error {
	ticker := time.NewTicker(waitMinedPollInterval)
	defer ticker.Stop()

	ethHash := common.Hash(hash)
	for {
		receipt, err := w.cfg.Client.TransactionReceipt(ctx, ethHash)
		if err == nil && receipt != nil {
			return nil
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("evmwallet: tx %x not mined: %w",
				hash, ctx.Err())
		}
	}
}

// htlcDataToArg converts the carrier HTLC form into the ABI tuple.
func htlcDataToArg(d input.EvmHTLCData) (evmnotify.EvmHTLCArg, error) {
	amount, err := parseRawAmount(d.Amount)
	if err != nil {
		return evmnotify.EvmHTLCArg{}, err
	}
	hashlock, err := parseHex32(d.Hashlock)
	if err != nil {
		return evmnotify.EvmHTLCArg{}, err
	}
	recipBytes, err := hex.DecodeString(d.Recipient)
	if err != nil || len(recipBytes) != 20 {
		return evmnotify.EvmHTLCArg{}, fmt.Errorf("evmwallet: bad "+
			"htlc recipient %q", d.Recipient)
	}

	return evmnotify.EvmHTLCArg{
		Index:     new(big.Int).SetUint64(d.Index),
		Amount:    amount,
		Hashlock:  hashlock,
		Timelock:  d.Timelock,
		Recipient: common.BytesToAddress(recipBytes),
	}, nil
}

// parseRawAmount parses a raw base-unit decimal string into a big.Int.
func parseRawAmount(s string) (*big.Int, error) {
	if s == "" {
		return big.NewInt(0), nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.Sign() < 0 {
		return nil, fmt.Errorf("evmwallet: bad raw amount %q", s)
	}

	return v, nil
}

// parseHex32 decodes a hex string into a 32-byte array.
func parseHex32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("evmwallet: bad 32-byte hex %q", s)
	}
	copy(out[:], b)

	return out, nil
}

// parseProof decodes a hex-encoded Merkle proof.
func parseProof(proof []string) ([][32]byte, error) {
	out := make([][32]byte, len(proof))
	for i, p := range proof {
		h, err := parseHex32(p)
		if err != nil {
			return nil, err
		}
		out[i] = h
	}

	return out, nil
}
