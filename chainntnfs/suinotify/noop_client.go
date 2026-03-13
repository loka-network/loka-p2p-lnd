// Package suinotify provides an implementation of the ChainNotifier interface
// for the Sui network.
package suinotify

import "github.com/btcsuite/btcd/chaincfg/chainhash"

// NoopSuiClient is a placeholder SuiClient that produces no events and
// returns immediately for all calls. It is used as a stub until a real
// RPC-backed Sui client is available.
//
// IMPORTANT: This client must NOT be used in production.
type NoopSuiClient struct{}

// Compile-time assertion that NoopSuiClient implements SuiClient.
var _ SuiClient = (*NoopSuiClient)(nil)

// GetBestEpoch returns height 0 and a zero hash.
func (n *NoopSuiClient) GetBestEpoch() (uint32, chainhash.Hash, error) {
	return 0, chainhash.Hash{}, nil
}

// SubscribeEpochs returns a channel that is closed when quit fires.
func (n *NoopSuiClient) SubscribeEpochs(
	quit <-chan struct{}) (<-chan EpochEvent, error) {

	ch := make(chan EpochEvent)
	go func() {
		<-quit
		close(ch)
	}()
	return ch, nil
}

// SubscribeEventConfirmation returns a channel that is closed when quit fires.
func (n *NoopSuiClient) SubscribeEventConfirmation(
	_ chainhash.Hash, _, _ uint32,
	quit <-chan struct{}) (<-chan ConfirmEvent, error) {

	ch := make(chan ConfirmEvent)
	go func() {
		<-quit
		close(ch)
	}()
	return ch, nil
}

// SubscribeObjectSpend returns a channel that is closed when quit fires.
func (n *NoopSuiClient) SubscribeObjectSpend(
	_ chainhash.Hash, _ uint32, _ uint32,
	quit <-chan struct{}) (<-chan SpendEvent, error) {

	ch := make(chan SpendEvent)
	go func() {
		<-quit
		close(ch)
	}()
	return ch, nil
}
