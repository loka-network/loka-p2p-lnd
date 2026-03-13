package chanfunding

import (
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

// SuiAssembler is an implementation of the Assembler interface that constructs
// Sui Move call "transactions" for channel funding.
type SuiAssembler struct {
}

// NewSuiAssembler creates a new SuiAssembler instance.
func NewSuiAssembler() *SuiAssembler {
	return &SuiAssembler{}
}

// ProvisionChannel handles the initial stage of channel funding. For Sui, we
// simply generate a temporary ObjectID that will represent the channel.
func (s *SuiAssembler) ProvisionChannel(req *Request) (Intent, error) {
	// We generate a deterministic ObjectID based on a temporary seed.
	// We'll use a zero hash for now or random.
	var tempID chainhash.Hash

	return &SuiIntent{
		SuiAssembler: s,
		localAmt:     req.LocalAmt,
		remoteAmt:    req.RemoteAmt,
		objectID:     tempID,
	}, nil
}

// SuiIntent implements the Intent interface for Sui channel funding.
type SuiIntent struct {
	*SuiAssembler
	localAmt  btcutil.Amount
	remoteAmt btcutil.Amount
	objectID  chainhash.Hash

	localKey  *keychain.KeyDescriptor
	remoteKey *keychain.KeyDescriptor
}

// BindKeys is called by the wallet when the remote peer's public key is known.
// We store both keys here so they can be included in the open_channel payload.
func (s *SuiIntent) BindKeys(localKey *keychain.KeyDescriptor, remoteKey *btcec.PublicKey) {
	s.localKey = localKey
	s.remoteKey = &keychain.KeyDescriptor{PubKey: remoteKey}
}

// FundingOutput returns the witness script, and the output that creates the
// funding output.
func (s *SuiIntent) FundingOutput() ([]byte, *wire.TxOut, error) {
	capacity := int64(s.localAmt + s.remoteAmt)

	// Since LND adapter expects to generate and verify valid Bitcoin signatures
	// internally, we must return a valid P2WSH multi-sig script matching the keys.
	if s.localKey != nil && s.remoteKey != nil {
		witnessScript, err := input.GenMultiSigScript(
			s.localKey.PubKey.SerializeCompressed(),
			s.remoteKey.PubKey.SerializeCompressed(),
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

	// Fallback if keys are not bound yet, though they should be.
	return nil, &wire.TxOut{
		Value:    capacity,
		PkScript: []byte{0x51}, // OP_INITIAL_SUI_STATE or OP_TRUE
	}, nil
}

// SetObjectID sets the generated ObjectID on the intent before final compilation.
func (s *SuiIntent) SetObjectID(id chainhash.Hash) {
	s.objectID = id
}

// ChanPoint returns the final outpoint that will create the channel.
// On Sui this is the ObjectID of the created Channel object.
func (s *SuiIntent) ChanPoint() (*wire.OutPoint, error) {
	return &wire.OutPoint{
		Hash:  s.objectID,
		Index: 0,
	}, nil
}

// RemoteFundingAmt is the amount the remote party put into the channel.
func (s *SuiIntent) RemoteFundingAmt() btcutil.Amount {
	return s.remoteAmt
}

// LocalFundingAmt is the amount we put into the channel.
func (s *SuiIntent) LocalFundingAmt() btcutil.Amount {
	return s.localAmt
}

// Inputs returns all inputs to the final funding transaction.
func (s *SuiIntent) Inputs() []wire.OutPoint {
	return nil
}

// Outputs returns all outputs of the final funding transaction.
func (s *SuiIntent) Outputs() []*wire.TxOut {
	_, out, _ := s.FundingOutput()
	return []*wire.TxOut{out}
}

// CompileFunds returns the final "funding transaction". For Sui, this is a
// wire.MsgTx that carries the serialized lightning::open_channel Move call.
func (s *SuiIntent) CompileFunds() (*wire.MsgTx, error) {
	// Build the open_channel payload.
	payload := input.ChannelOpenPayload{
		LocalBalance:  uint64(s.localAmt),
		RemoteBalance: uint64(s.remoteAmt),
		CSVDelay:      144,
	}

	return input.BuildChannelOpenTx(s.objectID, payload)
}

// Cancel cleans up any resources.
func (s *SuiIntent) Cancel() {}

var _ Assembler = (*SuiAssembler)(nil)
var _ Intent = (*SuiIntent)(nil)
