package chanfunding

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
)

// TestEvmAssemblerProvision exercises the EVM funding intent end to end: the
// provisioned amounts, the deterministic channelId derivation (A = funder), and
// the openChannel carrier tx that decodes back to the same channelId and
// counterparty.
func TestEvmAssemblerProvision(t *testing.T) {
	t.Parallel()

	localPriv, _ := btcec.NewPrivateKey()
	remotePriv, _ := btcec.NewPrivateKey()

	const (
		localAmt  = btcutil.Amount(600_000_000)
		remoteAmt = btcutil.Amount(400_000_000)
	)

	asm := NewEvmAssembler()
	intent, err := asm.ProvisionChannel(&Request{
		LocalAmt:  localAmt,
		RemoteAmt: remoteAmt,
	})
	if err != nil {
		t.Fatalf("ProvisionChannel: %v", err)
	}

	if intent.LocalFundingAmt() != localAmt {
		t.Fatalf("local amt = %d, want %d", intent.LocalFundingAmt(),
			localAmt)
	}
	if intent.RemoteFundingAmt() != remoteAmt {
		t.Fatalf("remote amt = %d, want %d", intent.RemoteFundingAmt(),
			remoteAmt)
	}

	evmIntent := intent.(*EvmIntent)
	evmIntent.BindKeys(
		&keychain.KeyDescriptor{PubKey: localPriv.PubKey()},
		remotePriv.PubKey(),
	)

	// ChanPoint hash must equal keccak256(A, B, salt) with A = funder.
	localAddr := input.EvmAddressFromPubKey(localPriv.PubKey())
	remoteAddr := input.EvmAddressFromPubKey(remotePriv.PubKey())
	salt := input.Keccak256(
		localPriv.PubKey().SerializeCompressed(),
		remotePriv.PubKey().SerializeCompressed(),
	)
	wantID := chainhash.Hash(input.EvmChannelID(localAddr, remoteAddr, salt))

	cp, err := evmIntent.ChanPoint()
	if err != nil {
		t.Fatalf("ChanPoint: %v", err)
	}
	if cp.Hash != wantID {
		t.Fatalf("channelId mismatch\n got %x\nwant %x", cp.Hash, wantID)
	}
	if cp.Index != 0 {
		t.Fatalf("index = %d, want 0", cp.Index)
	}

	// CompileFunds must decode back to the same channelId and counterparty.
	tx, err := evmIntent.CompileFunds()
	if err != nil {
		t.Fatalf("CompileFunds: %v", err)
	}
	gotID, callType, raw, err := input.DecodeEvmCallTx(tx)
	if err != nil {
		t.Fatalf("DecodeEvmCallTx: %v", err)
	}
	if gotID != wantID {
		t.Fatalf("carrier channelId mismatch\n got %x\nwant %x",
			gotID, wantID)
	}
	if callType != input.EvmCallChannelOpen {
		t.Fatalf("callType = %q, want ChannelOpen", callType)
	}

	var payload input.EvmChannelOpenPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.LocalBalance != uint64(localAmt) {
		t.Fatalf("payload local = %d, want %d", payload.LocalBalance,
			localAmt)
	}
	if payload.Counterparty != hex.EncodeToString(remoteAddr[:]) {
		t.Fatalf("counterparty = %s, want %x", payload.Counterparty,
			remoteAddr)
	}
}

// TestEvmIntentSetChannelID checks the event-authoritative channelId override.
func TestEvmIntentSetChannelID(t *testing.T) {
	t.Parallel()

	intent := &EvmIntent{}
	var fromEvent chainhash.Hash
	fromEvent[0] = 0xEE
	intent.SetChannelID(fromEvent)

	cp, err := intent.ChanPoint()
	if err != nil {
		t.Fatalf("ChanPoint: %v", err)
	}
	if cp.Hash != fromEvent {
		t.Fatalf("channelId = %x, want %x", cp.Hash, fromEvent)
	}
}
