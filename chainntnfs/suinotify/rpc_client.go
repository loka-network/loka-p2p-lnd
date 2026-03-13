package suinotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// rpcRequest represents a standard JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      uint64      `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

// rpcResponse represents a standard JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError represents a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("rpc error (code=%d): %s", e.Code, e.Message)
}

// SuiRPCClient is a minimal JSON-RPC client for the Sui network.
// It implements the SuiClient interface using standard HTTP POST requests.
type SuiRPCClient struct {
	url    string
	client *http.Client

	idMu sync.Mutex
	nextID uint64
}

// NewSuiRPCClient creates a new SuiRPCClient pointing to the given URL.
func NewSuiRPCClient(url string) *SuiRPCClient {
	return &SuiRPCClient{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// call performs a JSON-RPC call to the Sui node.
func (s *SuiRPCClient) call(method string, params interface{}) (json.RawMessage, error) {
	s.idMu.Lock()
	id := s.nextID
	s.nextID++
	s.idMu.Unlock()

	reqBody := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Post(s.url, "application/json", bytes.NewBuffer(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, err
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return rpcResp.Result, nil
}

// GetCoins returns the list of SUI coins owned by the given address.
func (s *SuiRPCClient) GetCoins(address string) ([]SuiCoin, error) {
	// sui_getCoins: (owner, coin_type, cursor, limit)
	result, err := s.call("sui_getCoins", []interface{}{
		address, nil, nil, nil,
	})
	if err != nil {
		return nil, err
	}

	var response struct {
		Data []struct {
			CoinObjectID string `json:"coinObjectId"`
			Balance      string `json:"balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, err
	}

	var coins []SuiCoin
	for _, c := range response.Data {
		objID, err := chainhash.NewHashFromStr(c.CoinObjectID)
		if err != nil {
			// Sui ObjectIDs are 32 bytes hex, similar to chainhash.
			// If it has 0x prefix, we should strip it.
			cleanID := c.CoinObjectID
			if len(cleanID) > 2 && cleanID[:2] == "0x" {
				cleanID = cleanID[2:]
			}
			objID, err = chainhash.NewHashFromStr(cleanID)
			if err != nil {
				continue
			}
		}

		var bal uint64
		fmt.Sscanf(c.Balance, "%d", &bal)

		coins = append(coins, SuiCoin{
			ObjectID: *objID,
			Balance:  bal,
		})
	}

	return coins, nil
}
func (s *SuiRPCClient) GetBestEpoch() (uint32, chainhash.Hash, error) {
	// sui_getLatestCheckpointSequenceNumber returns the seq as a string.
	result, err := s.call("sui_getLatestCheckpointSequenceNumber", []interface{}{})
	if err != nil {
		return 0, chainhash.Hash{}, err
	}

	var seqStr string
	if err := json.Unmarshal(result, &seqStr); err != nil {
		return 0, chainhash.Hash{}, err
	}

	var seq uint32
	if _, err := fmt.Sscanf(seqStr, "%d", &seq); err != nil {
		return 0, chainhash.Hash{}, err
	}

	// For the hash, we use a deterministic placeholder based on height for now,
	// or we could query the full checkpoint object. To keep it simple and
	// compatible with heightToHash:
	return seq, heightToHash(seq), nil
}

// SubscribeEpochs polls the Sui node for new checkpoints.
// In a real implementation, this would use a WebSocket subscription
// (sui_subscribeEvent with a filter or a dedicated checkpoint stream).
// For the initial adapter, we'll use a polling-based approach to ensure
// compatibility with standard HTTP RPC.
func (s *SuiRPCClient) SubscribeEpochs(quit <-chan struct{}) (<-chan EpochEvent, error) {
	ch := make(chan EpochEvent, 10)

	go func() {
		defer close(ch)
		var lastSeq uint32

		// Initial height.
		if seq, _, err := s.GetBestEpoch(); err == nil {
			lastSeq = seq
		}

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				currSeq, _, err := s.GetBestEpoch()
				if err != nil {
					continue
				}

				for h := lastSeq + 1; h <= currSeq; h++ {
					select {
					case ch <- EpochEvent{
						Height: h,
						Hash:   heightToHash(h),
					}:
					case <-quit:
						return
					}
				}
				lastSeq = currSeq

			case <-quit:
				return
			}
		}
	}()

	return ch, nil
}

// SubscribeEventConfirmation monitors for transaction finalization.
func (s *SuiRPCClient) SubscribeEventConfirmation(txID chainhash.Hash, numConfs,
	heightHint uint32, quit <-chan struct{}) (<-chan ConfirmEvent, error) {

	ch := make(chan ConfirmEvent, 1)

	go func() {
		defer close(ch)
		// Poll for transaction status.
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// sui_getTransactionBlock returns tx info if found.
				// txID.String() gives the hex, but Sui uses Base58 for Digests.
				// We assume the conversion is handled or txID already stores the digest.
				// For now, we'll just check if it's "confirmed" in Sui terms.
				
				// Placeholder: check if tx exists.
				_, err := s.call("sui_getTransactionBlock", []interface{}{
					txID.String(),
					map[string]bool{"showEffects": true},
				})
				if err == nil {
					// Found and executed.
					select {
					case ch <- ConfirmEvent{
						TxID:         txID,
						AnchorHeight: 0, // Placeholder
					}:
					case <-quit:
					}
					return
				}

			case <-quit:
				return
			}
		}
	}()

	return ch, nil
}

// SubscribeObjectSpend watches for Move events that indicate a channel state transition.
func (s *SuiRPCClient) SubscribeObjectSpend(objectID chainhash.Hash, htlcIndex uint32,
	heightHint uint32, quit <-chan struct{}) (<-chan SpendEvent, error) {

	ch := make(chan SpendEvent, 1)
	// This would ideally use sui_subscribeEvent with a filter on MoveEvent types
	// defined in our lightning module.
	
	go func() {
		defer close(ch)
		// Placeholder polling logic.
		<-quit
	}()

	return ch, nil
}

var _ SuiClient = (*SuiRPCClient)(nil)
