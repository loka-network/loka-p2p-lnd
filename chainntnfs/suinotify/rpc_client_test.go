package suinotify

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil/base58"
	"github.com/stretchr/testify/require"
)

// fakeSuiServer is a minimal JSON-RPC stand-in for a real Sui fullnode. It
// records every call and lets each test rewrite handler behaviour on the fly
// so we can simulate the canonical "indexer lag" window that triggered the
// original SCID-divergence bug.
type fakeSuiServer struct {
	*httptest.Server

	// pollCount counts how many times sui_getTransactionBlock has been
	// invoked. Tests use this to switch from "checkpoint not yet known"
	// responses to "checkpoint resolved" responses partway through.
	pollCount atomic.Int32

	// pollsBeforeCheckpoint controls how many sui_getTransactionBlock calls
	// will return checkpoint="" (the indexer-lag window). After this many
	// calls the server starts returning canonicalCheckpoint.
	pollsBeforeCheckpoint int32

	// canonicalCheckpoint is the value the server eventually returns once
	// the indexer "catches up".
	canonicalCheckpoint uint32
}

func newFakeSuiServer(t *testing.T, pollsBeforeCheckpoint int32,
	canonicalCheckpoint uint32) *fakeSuiServer {

	f := &fakeSuiServer{
		pollsBeforeCheckpoint: pollsBeforeCheckpoint,
		canonicalCheckpoint:   canonicalCheckpoint,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int           `json:"id"`
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		switch req.Method {
		case "sui_getTransactionBlock":
			n := f.pollCount.Add(1)
			// Build the response. Effects.status.status="success" so the
			// adapter treats the tx as executed; the only knob we vary is
			// whether the canonical checkpoint field is populated yet.
			var checkpointField string
			if n > f.pollsBeforeCheckpoint {
				checkpointField = fmt.Sprintf(`"checkpoint":"%d",`,
					f.canonicalCheckpoint)
			}
			body := fmt.Sprintf(`{
				"id": %d,
				"result": {
					%s
					"effects": {
						"status": {"status": "success"}
					}
				}
			}`, req.ID, checkpointField)
			_, _ = w.Write([]byte(body))

		case "sui_getCheckpoint", "sui_getLatestCheckpointSequenceNumber":
			// GetBestEpoch fallback should never be used by the fix. Return
			// a "trap value" so any accidental reliance would fail an
			// equality check downstream.
			body := fmt.Sprintf(`{"id": %d, "result": "9999999"}`, req.ID)
			_, _ = w.Write([]byte(body))

		default:
			body := fmt.Sprintf(`{"id": %d, "result": null}`, req.ID)
			_, _ = w.Write([]byte(body))
		}
	})

	f.Server = httptest.NewServer(mux)
	return f
}

// txDigestPair returns a deterministic (lnd-side hash, sui-side base58 digest)
// pair so the test can drive the txDigestMap that SubscribeEventConfirmation
// looks up against the Sui RPC.
func txDigestPair() (lndHash, suiHash chainhash.Hash, suiB58 string) {
	// 32-byte arbitrary fixed digest — anything works; only relevance is
	// that lnd and Sui sides remain consistent across the round-trip.
	raw, _ := base64.StdEncoding.DecodeString(
		"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=",
	)
	copy(lndHash[:], raw)
	suiHash = lndHash
	suiB58 = base58.Encode(suiHash[:])
	return
}

// TestSubscribeEventConfirmation_WaitsForCanonicalCheckpoint locks down the
// fix: when the Sui RPC reports `effects.status.status=="success"` but the
// canonical `checkpoint` field is still empty (indexer-lag window), the
// confirmation goroutine MUST NOT fabricate a height from GetBestEpoch —
// doing so causes independently deployed nodes to derive divergent SCIDs
// for the same funding tx (each side's SCID embeds its own observer-local
// chain tip), permanently breaking gossip announcement proof exchange.
//
// Expected behaviour: keep polling until the fullnode populates the
// canonical checkpoint, then emit a ConfirmEvent whose AnchorHeight is
// EXACTLY that canonical value (identical across observers).
func TestSubscribeEventConfirmation_WaitsForCanonicalCheckpoint(t *testing.T) {
	t.Parallel()

	const (
		// Number of polls during which the server pretends the
		// canonical checkpoint is still being indexed.
		indexerLagPolls int32 = 3

		// The canonical checkpoint number every observer must agree on.
		canonicalCheckpoint uint32 = 42

		// Trap value the test server returns from GetBestEpoch-style
		// queries; we assert the AnchorHeight is NOT this.
		bestEpochTrap uint32 = 9999999
	)

	srv := newFakeSuiServer(t, indexerLagPolls, canonicalCheckpoint)
	defer srv.Close()

	client := NewSuiRPCClient(srv.URL, "0x0")

	lndHash, suiHash, _ := txDigestPair()
	client.RegisterTxDigest(lndHash, suiHash)

	quit := make(chan struct{})
	defer close(quit)

	ch, err := client.SubscribeEventConfirmation(lndHash, 1, 0, quit)
	require.NoError(t, err)

	select {
	case ev := <-ch:
		// CRITICAL ASSERTIONS:
		//
		// 1. AnchorHeight must be the canonical checkpoint from the
		//    server's response, never the GetBestEpoch trap value.
		require.Equal(t, canonicalCheckpoint, ev.AnchorHeight,
			"AnchorHeight must come from the canonical Sui "+
				"checkpoint field, not from a local-tip "+
				"fallback. Anything else causes SCID "+
				"divergence between independently deployed "+
				"nodes (see rpc_client.go comment).")

		require.NotEqual(t, bestEpochTrap, ev.AnchorHeight,
			"AnchorHeight must NOT be derived from "+
				"GetBestEpoch — that is the broken fallback "+
				"this test is locking out.")

		// 2. The server must have been polled MORE times than the
		//    indexer-lag window. If the goroutine had emitted on the
		//    first "checkpoint empty" response (the pre-fix
		//    behaviour), pollCount would still be 1 here.
		require.Greater(t, srv.pollCount.Load(),
			indexerLagPolls,
			"SubscribeEventConfirmation must keep polling "+
				"through the indexer-lag window, not emit a "+
				"fabricated AnchorHeight on the first "+
				"empty-checkpoint response.")

	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for ConfirmEvent after %d polls",
			srv.pollCount.Load())
	}
}

// fakeSuiServerWithObjectFallback simulates the realistic Loka deployment
// pattern: the hash we register for confirmation is a Channel ObjectID, not
// a Sui transaction digest. `sui_getTransactionBlock(objId)` always errors
// ("Could not find the referenced transaction"); `sui_getObject(objId)`
// succeeds and returns `previousTransaction`. The fix must use that
// previous-tx digest to fetch the canonical checkpoint — NOT
// GetBestEpoch, which races between observers.
type fakeSuiServerWithObjectFallback struct {
	*httptest.Server

	objCalls atomic.Int32
	txCalls  atomic.Int32

	// What the canonical funding tx → canonical checkpoint mapping is.
	prevTxDigest        string
	canonicalCheckpoint uint32
}

func newFakeSuiServerObjectFallback(t *testing.T, prevTxDigest string,
	canonicalCheckpoint uint32) *fakeSuiServerWithObjectFallback {

	f := &fakeSuiServerWithObjectFallback{
		prevTxDigest:        prevTxDigest,
		canonicalCheckpoint: canonicalCheckpoint,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int           `json:"id"`
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		switch req.Method {
		case "sui_getTransactionBlock":
			n := f.txCalls.Add(1)
			// Inspect the digest. If it matches our canonical previous-tx,
			// return the canonical checkpoint. Otherwise return the
			// "not a tx digest" error Sui actually emits for ObjectIDs.
			digest, _ := req.Params[0].(string)
			if digest == f.prevTxDigest {
				body := fmt.Sprintf(`{
					"id": %d,
					"result": {
						"checkpoint": "%d",
						"effects": {"status": {"status": "success"}}
					}
				}`, req.ID, f.canonicalCheckpoint)
				_, _ = w.Write([]byte(body))
				return
			}
			// Mirror the real Sui RPC error wording so the suinotify
			// code's "treat as object instead" path triggers.
			body := fmt.Sprintf(`{
				"id": %d,
				"error": {
					"code": -32602,
					"message": "Could not find the referenced transaction"
				}
			}`, req.ID)
			_, _ = w.Write([]byte(body))
			_ = n

		case "sui_getObject":
			f.objCalls.Add(1)
			body := fmt.Sprintf(`{
				"id": %d,
				"result": {
					"data": {
						"objectId": "0xdeadbeef",
						"previousTransaction": "%s"
					}
				}
			}`, req.ID, f.prevTxDigest)
			_, _ = w.Write([]byte(body))

		case "sui_getCheckpoint", "sui_getLatestCheckpointSequenceNumber":
			// Trap value — should not be used.
			body := fmt.Sprintf(`{"id": %d, "result": "9999999"}`, req.ID)
			_, _ = w.Write([]byte(body))

		default:
			body := fmt.Sprintf(`{"id": %d, "result": null}`, req.ID)
			_, _ = w.Write([]byte(body))
		}
	})

	f.Server = httptest.NewServer(mux)
	return f
}

// TestSubscribeEventConfirmation_ObjectIDFallbackUsesCanonicalCheckpoint
// locks down the FUNDEE side path. Only the channel funder caches the
// real Sui tx digest in txDigestMap; the fundee sees only the Channel
// ObjectID. The notifier must therefore:
//
//  1. Detect that getTransactionBlock(objId) errors (it's not a tx).
//  2. Call getObject(objId) with showPreviousTransaction=true to learn
//     the canonical funding-tx digest.
//  3. Call getTransactionBlock(prevTx) to get the canonical checkpoint
//     of that tx — a value EVERY observer in the network agrees on.
//
// Before this fix the fundee fell back to GetBestEpoch() which is
// observer-local, producing different SCIDs on the two sides and
// permanently breaking gossip announcement proof matching.
func TestSubscribeEventConfirmation_ObjectIDFallbackUsesCanonicalCheckpoint(t *testing.T) {
	t.Parallel()

	const (
		// Canonical Sui checkpoint where the funding tx was committed.
		// Both observers must derive THIS exact value.
		canonicalCheckpoint uint32 = 100
		bestEpochTrap       uint32 = 9999999
	)

	// Arbitrary Sui-flavoured base58-ish previous-tx digest.
	const prevTxDigest = "DshyQV626X1U3EGBHaVxXLZq7VrzgNF2uEvbWrqLpdYh"

	srv := newFakeSuiServerObjectFallback(t, prevTxDigest, canonicalCheckpoint)
	defer srv.Close()

	client := NewSuiRPCClient(srv.URL, "0x0")

	// Crucially: do NOT call RegisterTxDigest. That simulates the fundee
	// side where the LND-side hash is the channel ObjectID, not a tx
	// digest the funder cached.
	lndHash, _, _ := txDigestPair()

	quit := make(chan struct{})
	defer close(quit)

	ch, err := client.SubscribeEventConfirmation(lndHash, 1, 0, quit)
	require.NoError(t, err)

	select {
	case ev := <-ch:
		// 1. AnchorHeight must be the canonical Sui checkpoint, not the
		//    GetBestEpoch trap value.
		require.Equal(t, canonicalCheckpoint, ev.AnchorHeight,
			"fundee-side AnchorHeight must come from "+
				"sui_getTransactionBlock(previousTransaction)."+
				"checkpoint — the canonical, observer-"+
				"independent value. Anything else causes "+
				"SCID divergence between the two channel "+
				"halves.")

		require.NotEqual(t, bestEpochTrap, ev.AnchorHeight,
			"fundee MUST NOT fall back to GetBestEpoch — that "+
				"is the broken path this test locks out.")

		// 2. We must have queried Sui for both the object AND its
		//    canonical previous-tx checkpoint. Pre-fix code only
		//    queried getObject and immediately bailed to GetBestEpoch.
		require.Positive(t, srv.objCalls.Load(),
			"must call sui_getObject")
		require.Positive(t, srv.txCalls.Load(),
			"must call sui_getTransactionBlock(previousTransaction) "+
				"to recover the canonical checkpoint")

	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for ConfirmEvent "+
			"(objCalls=%d txCalls=%d)",
			srv.objCalls.Load(), srv.txCalls.Load())
	}
}

// TestSubscribeEventConfirmation_ImmediateCheckpointEmitsRightAway verifies
// the happy path: when the very first poll already has the canonical
// checkpoint populated, the goroutine emits immediately with that height
// (no extra polling, no waiting). Guards against an over-eager fix that
// would always wait several ticks.
func TestSubscribeEventConfirmation_ImmediateCheckpointEmitsRightAway(t *testing.T) {
	t.Parallel()

	const canonicalCheckpoint uint32 = 7

	// pollsBeforeCheckpoint=0 means the canonical checkpoint is present
	// from the very first response.
	srv := newFakeSuiServer(t, 0, canonicalCheckpoint)
	defer srv.Close()

	client := NewSuiRPCClient(srv.URL, "0x0")

	lndHash, suiHash, _ := txDigestPair()
	client.RegisterTxDigest(lndHash, suiHash)

	quit := make(chan struct{})
	defer close(quit)

	ch, err := client.SubscribeEventConfirmation(lndHash, 1, 0, quit)
	require.NoError(t, err)

	select {
	case ev := <-ch:
		require.Equal(t, canonicalCheckpoint, ev.AnchorHeight)
		// Should have been resolved within just a handful of polls
		// (ticker fires every second, so allow generous slack on
		// slow CI runners but assert "not stuck looping for ever").
		require.LessOrEqual(t, srv.pollCount.Load(), int32(5),
			"happy path should not need many polls")

	case <-time.After(10 * time.Second):
		t.Fatalf("timed out on happy path")
	}
}
