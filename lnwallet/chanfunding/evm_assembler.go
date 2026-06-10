package chanfunding

import (
	"encoding/hex"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

// EvmAssembler is an Assembler that provisions ChannelManager escrow funding
// instead of selecting on-chain UTXOs. It mirrors SuiAssembler: no coins are
// locked here; the funding "transaction" is a carrier tx wrapping the
// openChannel call, which evmwallet later ABI-encodes (preceded by the ERC20
// approve) and broadcasts.
type EvmAssembler struct{}

// NewEvmAssembler creates a new EvmAssembler.
func NewEvmAssembler() *EvmAssembler {
	return &EvmAssembler{}
}

// ProvisionChannel begins EVM channel funding by returning an intent carrying
// the requested deposits. Keys (and hence the channelId) are bound later via
// BindKeys, once the remote funding pubkey is known.
func (e *EvmAssembler) ProvisionChannel(req *Request) (Intent, error) {
	return &EvmIntent{
		EvmAssembler: e,
		localAmt:     req.LocalAmt,
		remoteAmt:    req.RemoteAmt,
	}, nil
}

// EvmIntent implements Intent for EVM ChannelManager funding.
type EvmIntent struct {
	*EvmAssembler

	localAmt  btcutil.Amount
	remoteAmt btcutil.Amount

	localKey  *keychain.KeyDescriptor
	remoteKey *keychain.KeyDescriptor

	// channelID is keccak256(participantA, participantB, salt) with A = the
	// funder (this node when it broadcasts openChannel). It is computed in
	// BindKeys and may be overwritten by SetChannelID with the authoritative
	// value from the ChannelOpened event.
	channelID chainhash.Hash

	// salt is keccak256(localKey, remoteKey) — deterministic and unique per
	// channel because the funding multisig key is freshly derived per channel.
	salt [32]byte
}

// BindKeys records both funding pubkeys and derives the provisional channelId.
// Both peers compute the same salt and channelId from the same key pair.
func (e *EvmIntent) BindKeys(localKey *keychain.KeyDescriptor,
	remoteKey *btcec.PublicKey) {

	e.localKey = localKey
	e.remoteKey = &keychain.KeyDescriptor{PubKey: remoteKey}

	localAddr := input.EvmAddressFromPubKey(localKey.PubKey)
	remoteAddr := input.EvmAddressFromPubKey(remoteKey)

	// salt = keccak256(localPub ‖ remotePub).
	e.salt = input.Keccak256(
		localKey.PubKey.SerializeCompressed(),
		remoteKey.SerializeCompressed(),
	)

	// A = funder = this node (the openChannel broadcaster); B = counterparty.
	e.channelID = chainhash.Hash(
		input.EvmChannelID(localAddr, remoteAddr, e.salt),
	)
}

// SetChannelID overrides the channelId with the authoritative value observed in
// the ChannelOpened event (analogue of SuiIntent.SetObjectID). Use this on the
// responder side, where the funder's exact salt/A-B ordering is taken from the
// event rather than recomputed.
func (e *EvmIntent) SetChannelID(id chainhash.Hash) {
	e.channelID = id
}

// FundingOutput returns a P2WSH 2-of-2 multisig output matching the funding
// keys. The EVM escrow itself holds the funds on-chain; this output exists only
// so LND's internal commitment machinery (which still constructs and validates
// SegWit artifacts) stays consistent — mirroring SuiIntent.FundingOutput.
func (e *EvmIntent) FundingOutput() ([]byte, *wire.TxOut, error) {
	capacity := int64(e.localAmt + e.remoteAmt)

	if e.localKey != nil && e.remoteKey != nil {
		witnessScript, err := input.GenMultiSigScript(
			e.localKey.PubKey.SerializeCompressed(),
			e.remoteKey.PubKey.SerializeCompressed(),
		)
		if err != nil {
			return nil, nil, err
		}

		pkScript, err := input.WitnessScriptHash(witnessScript)
		if err != nil {
			return nil, nil, err
		}

		return witnessScript, &wire.TxOut{
			Value:    capacity,
			PkScript: pkScript,
		}, nil
	}

	return nil, &wire.TxOut{
		Value:    capacity,
		PkScript: []byte{0x51}, // OP_1
	}, nil
}

// ChanPoint returns the channel outpoint: channelId as the hash, index 0 (EVM
// has no UTXO index).
func (e *EvmIntent) ChanPoint() (*wire.OutPoint, error) {
	return &wire.OutPoint{
		Hash:  e.channelID,
		Index: 0,
	}, nil
}

// LocalFundingAmt is the amount this node deposits.
func (e *EvmIntent) LocalFundingAmt() btcutil.Amount {
	return e.localAmt
}

// RemoteFundingAmt is the amount the counterparty deposits.
func (e *EvmIntent) RemoteFundingAmt() btcutil.Amount {
	return e.remoteAmt
}

// Inputs returns the funding inputs; EVM funding selects no UTXOs.
func (e *EvmIntent) Inputs() []wire.OutPoint {
	return nil
}

// Outputs returns the funding outputs.
func (e *EvmIntent) Outputs() []*wire.TxOut {
	_, out, _ := e.FundingOutput()

	return []*wire.TxOut{out}
}

// CompileFunds returns the carrier tx wrapping the openChannel call. evmwallet
// decodes it, scales the amounts to raw token base-units, ABI-encodes
// openChannel (after the prerequisite ERC20 approve), and broadcasts it.
func (e *EvmIntent) CompileFunds() (*wire.MsgTx, error) {
	var localKeyHex, remoteKeyHex, counterpartyHex string
	if e.localKey != nil && e.remoteKey != nil {
		localKeyHex = hex.EncodeToString(
			e.localKey.PubKey.SerializeCompressed(),
		)
		remoteKeyHex = hex.EncodeToString(
			e.remoteKey.PubKey.SerializeCompressed(),
		)
		remoteAddr := input.EvmAddressFromPubKey(e.remoteKey.PubKey)
		counterpartyHex = hex.EncodeToString(remoteAddr[:])
	}

	payload := input.EvmChannelOpenPayload{
		Salt:          hex.EncodeToString(e.salt[:]),
		Counterparty:  counterpartyHex,
		LocalBalance:  uint64(e.localAmt),
		RemoteBalance: uint64(e.remoteAmt),
		LocalKey:      localKeyHex,
		RemoteKey:     remoteKeyHex,
	}

	return input.BuildEvmChannelOpenTx(e.channelID, payload)
}

// Cancel releases any resources; EVM funding holds none until broadcast.
func (e *EvmIntent) Cancel() {}

// FundingTxAvailable signals that the assembler provides the channel's
// funding transaction (the openChannel carrier) through its intent, so the
// funding manager keeps the standard broadcast-after-funding_signed flow
// instead of setting NoFundingTxBit. The carrier is published via
// PublishTransaction → evmwallet.executeCarrier.
//
// NOTE: This method is a part of the FundingTxAssembler interface.
func (e *EvmAssembler) FundingTxAvailable() {}

var (
	_ Assembler          = (*EvmAssembler)(nil)
	_ FundingTxAssembler = (*EvmAssembler)(nil)
	_ Intent             = (*EvmIntent)(nil)
)
