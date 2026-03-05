// Package setunotify provides an implementation of the ChainNotifier interface
// for the Setu DAG network.
package setunotify

import "github.com/btcsuite/btcd/chaincfg/chainhash"

// NoopSetuClient is a placeholder SetuClient that produces no events and
// returns immediately for all calls. It is used as a stub until a real
// gRPC-backed Setu client is available.
//
// IMPORTANT: This client must NOT be used in production. Replace it with a
// proper implementation backed by the Setu validator node gRPC API.
type NoopSetuClient struct{}

// Compile-time assertion that NoopSetuClient implements SetuClient.
var _ SetuClient = (*NoopSetuClient)(nil)

// GetBestEpoch returns height 0 and a zero hash as there is no real chain
// connection.
func (n *NoopSetuClient) GetBestEpoch() (uint32, chainhash.Hash, error) {
	return 0, chainhash.Hash{}, nil
}

// SubscribeEpochs returns a channel that is closed when quit fires. No epoch
// events will be sent.
func (n *NoopSetuClient) SubscribeEpochs(
	quit <-chan struct{}) (<-chan EpochEvent, error) {

	ch := make(chan EpochEvent)
	go func() {
		<-quit
		close(ch)
	}()
	return ch, nil
}

// SubscribeEventConfirmation returns a channel that is closed when quit fires.
// No confirmation events will be sent.
func (n *NoopSetuClient) SubscribeEventConfirmation(
	_ chainhash.Hash, _, _ uint32,
	quit <-chan struct{}) (<-chan ConfirmEvent, error) {

	ch := make(chan ConfirmEvent)
	go func() {
		<-quit
		close(ch)
	}()
	return ch, nil
}

// SubscribeObjectSpend returns a channel that is closed when quit fires. No
// spend events will be sent.
//
// htlcIndex == 0  → watch for channel-level close (ChannelObject CLOSING/CLOSED)
// htlcIndex == N  → watch for HTLCEntry[N] status changing to Claimed/Timeout
func (n *NoopSetuClient) SubscribeObjectSpend(
	_ chainhash.Hash, _ uint32, _ uint32,
	quit <-chan struct{}) (<-chan SpendEvent, error) {

	ch := make(chan SpendEvent)
	go func() {
		<-quit
		close(ch)
	}()
	return ch, nil
}
