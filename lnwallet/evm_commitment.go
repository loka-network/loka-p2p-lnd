package lnwallet

import (
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/channeldb"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwire"
)

// This file bridges an LND commitment view into the EVM off-chain commitment
// artifact — the EIP-712 StateUpdate{channelId, nonce, balanceA, balanceB,
// htlcsHash} each peer signs per state. It deliberately holds no protocol logic
// (StateNum, UpdateLog, revocation are untouched, exactly as for the Sui
// adapter); it only translates the already-computed commitment into the form
// the ChannelManager contract verifies at forceClose / penalize.
//
// The pure EIP-712 / Merkle primitives live in input/ (evm_channel.go,
// evm_merkle.go) and are cross-checked against the contract via golden vectors;
// this file just feeds them. The channel.go hook (gated on evmChainActive)
// calls buildEvmStateUpdate to obtain the digest it signs.

// internalDecimals mirrors evmwallet.internalDecimals: LND's btcutil.Amount uses
// a fixed 1e8 internal scale (Bitcoin's 1 BTC = 1e8 sat) regardless of the
// ERC20's own decimals. The bridge cannot import lnwallet/evmwallet (that
// package implements lnwallet.WalletController and so imports lnwallet — a
// cycle), so the Decimals Scaling Factor is reproduced here. Keep in lockstep
// with evmwallet.ScaleToBase.
const internalDecimals = 8

// evmCommitmentDomain and evmCommitmentTokenDecimals are the per-sub-network
// EIP-712 domain and ERC20 token precision used to build StateUpdate digests.
// They are set once at startup (alongside SetEvmChainActive) and never mutated
// thereafter, mirroring the suiChainActive pattern.
var (
	evmCommitmentDomain        input.EvmDomain
	evmCommitmentTokenDecimals uint8
)

// SetEvmCommitmentParams records the EIP-712 domain (chainID + verifying
// ChannelManager address) and the ERC20 token decimals for the active EVM
// sub-network. Called once during configuration, before any channel state
// machine starts.
func SetEvmCommitmentParams(domain input.EvmDomain, tokenDecimals uint8) {
	evmCommitmentDomain = domain
	evmCommitmentTokenDecimals = tokenDecimals
}

// EvmCommitmentDomain returns the configured EIP-712 domain. Exposed for the
// adapter packages (signer, contractcourt) that re-derive the same digest.
func EvmCommitmentDomain() input.EvmDomain {
	return evmCommitmentDomain
}

// evmScaleToBase converts an internal btcutil.Amount (1e8 per token) to raw
// ERC20 base-units (10^tokenDecimals per token) — the units the contract's
// uint256 balances/amounts are denominated in. It mirrors
// evmwallet.ScaleToBase: exact when tokenDecimals >= internalDecimals, else
// rounds DOWN so an amount never exceeds what was authorized.
func evmScaleToBase(amt btcutil.Amount) *big.Int {
	if amt <= 0 {
		return big.NewInt(0)
	}

	raw := new(big.Int).Mul(
		big.NewInt(int64(amt)),
		pow10Int(int(evmCommitmentTokenDecimals)),
	)
	raw.Quo(raw, pow10Int(internalDecimals)) // truncates toward zero

	return raw
}

// pow10Int returns 10^n as a big.Int.
func pow10Int(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// buildEvmHTLCs gathers the active HTLCs of a commitment view into the contract
// HTLC form, assigning each its channel-absolute recipient: an outgoing HTLC
// (local→remote, from this node's PoV) is claimed by the remote party, an
// incoming HTLC (remote→local) by the local party. Both peers therefore derive
// an identical recipient for the same HTLC (their Local key is the other's
// Remote), so the htlcsHash agrees. The slice is returned in UpdateLog order;
// HtlcsMerkleRoot re-sorts by index, so ordering here is not significant.
//
// Timelock carries the LND absolute CLTV expiry verbatim. The block-height ↔
// block.timestamp mapping the contract's timeoutHtlc ultimately compares
// against is applied at the resolver boundary (phase 3.4), not here — both
// peers must merely commit to the same value, which they do.
func buildEvmHTLCs(view *commitment, ourAddr,
	theirAddr [20]byte) []input.EvmHTLC {

	total := len(view.outgoingHTLCs) + len(view.incomingHTLCs)
	if total == 0 {
		return nil
	}

	htlcs := make([]input.EvmHTLC, 0, total)
	add := func(pd paymentDescriptor, recipient [20]byte) {
		var hashlock [32]byte
		copy(hashlock[:], pd.RHash[:])

		htlcs = append(htlcs, input.EvmHTLC{
			Index:     pd.HtlcIndex,
			Amount:    evmScaleToBase(pd.Amount.ToSatoshis()),
			Hashlock:  hashlock,
			Timelock:  pd.Timeout,
			Recipient: recipient,
		})
	}

	for _, pd := range view.outgoingHTLCs {
		add(pd, theirAddr)
	}
	for _, pd := range view.incomingHTLCs {
		add(pd, ourAddr)
	}

	return htlcs
}

// evmBalanceSplit computes the channel-absolute (balanceA, balanceB) pair the
// contract accounts in. The contract requires the broadcast state to satisfy
// balanceA + balanceB + Σhtlc == totalDeposited exactly (any shortfall is
// treated as unresolved HTLC funds and strands the channel in
// distributeFunds), but LND's internal balances are net of the commit fee and
// anchor amounts — artifacts that do not exist on EVM — and down-scaling can
// truncate sub-base-unit msat tails. So only B's balance and the HTLC sum are
// scaled (floored); A — the funder, who fronted fee and anchors — absorbs the
// remainder. Both peers compute the identical pair because B's balance and
// the HTLC set are channel-absolute.
func evmBalanceSplit(capacity, bBalance btcutil.Amount,
	htlcs []input.EvmHTLC) (*big.Int, *big.Int) {

	totalRaw := evmScaleToBase(capacity)
	balanceB := evmScaleToBase(bBalance)

	balanceA := new(big.Int).Sub(totalRaw, balanceB)
	for _, h := range htlcs {
		balanceA.Sub(balanceA, h.Amount)
	}
	if balanceA.Sign() < 0 {
		// Cannot happen for a real channel (capacity covers balances
		// and HTLCs); clamp defensively rather than emit a negative.
		balanceA.SetInt64(0)
	}

	return balanceA, balanceB
}

// buildEvmStateUpdate translates a commitment view into the EIP-712 StateUpdate
// the peers sign for that state. Balances are mapped to the channel-absolute
// (A, B) convention — A is always the funder/initiator — so both peers compute
// the same digest regardless of perspective; nonce is the LND commitment height
// (StateNum); htlcsHash is the Merkle root over the active HTLC set (zero when
// empty). channelID is the on-chain channelId (the funding outpoint hash).
func buildEvmStateUpdate(view *commitment, channelID [32]byte,
	capacity btcutil.Amount, isInitiator bool,
	ourAddr, theirAddr [20]byte) input.EvmStateUpdate {

	htlcs := buildEvmHTLCs(view, ourAddr, theirAddr)

	// B = the non-funder. From the initiator's PoV that is the remote
	// balance, from the non-initiator's its own.
	bBalance := view.theirBalance.ToSatoshis()
	if !isInitiator {
		bBalance = view.ourBalance.ToSatoshis()
	}

	balanceA, balanceB := evmBalanceSplit(capacity, bBalance, htlcs)

	return input.EvmStateUpdate{
		ChannelID: channelID,
		Nonce:     view.height,
		BalanceA:  balanceA,
		BalanceB:  balanceB,
		HtlcsHash: input.HtlcsMerkleRoot(htlcs),
	}
}

// evmChannelID returns the on-chain channelId for this channel: the 32-byte
// funding outpoint hash (wire.OutPoint.Hash ↔ ChannelManager channelId, per the
// adapter type-mapping convention).
func evmChannelID(chanState *channeldb.OpenChannel) [32]byte {
	return [32]byte(chanState.FundingOutpoint.Hash)
}

// evmPartyAddrs derives the two parties' 20-byte EVM addresses from their
// funding multisig pubkeys (local = this node's PoV). Both peers see the same
// pair because each one's local key is the other's remote key.
func evmPartyAddrs(chanState *channeldb.OpenChannel) (our, their [20]byte) {
	our = input.EvmAddressFromPubKey(chanState.LocalChanCfg.MultiSigKey.PubKey)
	their = input.EvmAddressFromPubKey(
		chanState.RemoteChanCfg.MultiSigKey.PubKey,
	)

	return our, their
}

// stateUpdateForView builds the canonical EIP-712 StateUpdate for a commitment
// view. Because the artifact is keyless and channel-absolute, both peers derive
// an identical StateUpdate for the same nonce regardless of which party's
// commitment the view represents — that single shared state is what both sign.
func (lc *LightningChannel) stateUpdateForView(
	view *commitment) input.EvmStateUpdate {

	our, their := evmPartyAddrs(lc.channelState)

	return buildEvmStateUpdate(
		view, evmChannelID(lc.channelState), lc.channelState.Capacity,
		lc.channelState.IsInitiator, our, their,
	)
}

// signEvmCommitment signs the EIP-712 StateUpdate for the given commitment view
// with the funding multisig key, returning the wire signature carried in
// commitment_signed. It is the EVM replacement for SegWit sighash signing, gated
// by evmChainActive in SignNextCommitment.
func (lc *LightningChannel) signEvmCommitment(
	view *commitment) (lnwire.Sig, error) {

	evmSigner, ok := lc.Signer.(*input.EvmSigner)
	if !ok {
		return lnwire.Sig{}, fmt.Errorf("evm chain active but signer "+
			"is %T, not *input.EvmSigner", lc.Signer)
	}

	su := lc.stateUpdateForView(view)
	rawSig, err := evmSigner.SignStateUpdateWire(
		lc.channelState.LocalChanCfg.MultiSigKey, evmCommitmentDomain,
		su,
	)
	if err != nil {
		return lnwire.Sig{}, err
	}

	return lnwire.NewSigFromSignature(rawSig)
}

// EvmBreachEvidence is the calldata a forceClose or penalize submits: the
// canonical StateUpdate fields plus the counterparty's 65-byte (r ‖ s ‖ v)
// signature over that state's EIP-712 digest. The signature is reconstructed
// from the 64-byte form LND retained in commitment_signed — EVM has no on-chain
// revocation key, so the breach remedy is simply proving the counterparty signed
// this state (forceClose) or a newer one (penalize, newer-nonce model).
type EvmBreachEvidence struct {
	ChannelID [32]byte
	Nonce     uint64
	BalanceA  *big.Int
	BalanceB  *big.Int
	HtlcsHash [32]byte

	// Sig is the 65-byte counterparty signature the contract's ECDSA.recover
	// resolves to the remote funding address.
	Sig []byte
}

// evmBreachEvidence assembles the on-chain evidence for a commitment view from
// the counterparty's retained wire signature over that state. It rebuilds the
// canonical StateUpdate, recovers the signature's v against the remote funding
// address, and returns the tuple forceClose / penalize consume. It errors if the
// retained signature does not recover to the counterparty (i.e. it is not their
// signature over this state).
func (lc *LightningChannel) evmBreachEvidence(view *commitment,
	counterpartySig lnwire.Sig) (EvmBreachEvidence, error) {

	su := lc.stateUpdateForView(view)
	digest := su.Digest(evmCommitmentDomain)

	_, theirAddr := evmPartyAddrs(lc.channelState)
	sig65, err := input.RecoverEvmSigV(
		counterpartySig.RawBytes(), digest, theirAddr,
	)
	if err != nil {
		return EvmBreachEvidence{}, err
	}

	return EvmBreachEvidence{
		ChannelID: su.ChannelID,
		Nonce:     su.Nonce,
		BalanceA:  su.BalanceA,
		BalanceB:  su.BalanceB,
		HtlcsHash: su.HtlcsHash,
		Sig:       sig65,
	}, nil
}

// evmForceCloseTx builds the forceClose carrier tx that broadcasts the given
// state, co-signed by the counterparty. It is the EVM action behind the
// commitSweepResolver (spec §2.8). The carrier is decoded and ABI-encoded by
// evmwallet; the on-chain challenge window then replaces the Bitcoin CSV.
func (lc *LightningChannel) evmForceCloseTx(view *commitment,
	counterpartySig lnwire.Sig) (*wire.MsgTx, error) {

	ev, err := lc.evmBreachEvidence(view, counterpartySig)
	if err != nil {
		return nil, err
	}

	return input.BuildEvmForceCloseTx(
		chainhash.Hash(ev.ChannelID), ev.Nonce, ev.BalanceA,
		ev.BalanceB, ev.HtlcsHash, ev.Sig,
		lc.channelState.LocalChanCfg.MultiSigKey.PubKey.
			SerializeCompressed(),
	)
}

// evmPenalizeTx builds the penalize carrier tx submitting a strictly-newer
// signed state, the EVM action behind the BreachArbitrator (spec §2.8). The
// view must be a higher-nonce state than the one the cheater broadcast.
func (lc *LightningChannel) evmPenalizeTx(view *commitment,
	counterpartySig lnwire.Sig) (*wire.MsgTx, error) {

	ev, err := lc.evmBreachEvidence(view, counterpartySig)
	if err != nil {
		return nil, err
	}

	return input.BuildEvmPenalizeTx(
		chainhash.Hash(ev.ChannelID), ev.Nonce, ev.BalanceA,
		ev.BalanceB, ev.HtlcsHash, ev.Sig,
		lc.channelState.LocalChanCfg.MultiSigKey.PubKey.
			SerializeCompressed(),
	)
}

// evmHtlcResolution builds a claimHtlc (preimage non-nil, the htlcSuccessResolver
// action) or timeoutHtlc (preimage nil, the htlcTimeoutResolver action) carrier
// for the HTLC at htlcIndex within the view. The HTLC and its Merkle proof are
// reconstructed from the same committed set that produced the state's htlcsHash,
// so the contract's _verifyHtlcInclusion accepts them.
func (lc *LightningChannel) evmHtlcResolution(view *commitment,
	htlcIndex uint64, preimage *[32]byte) (*wire.MsgTx, error) {

	our, their := evmPartyAddrs(lc.channelState)
	htlcs := buildEvmHTLCs(view, our, their)

	proof, ok := input.HtlcMerkleProof(htlcs, htlcIndex)
	if !ok {
		return nil, fmt.Errorf("evm: no HTLC with index %d in the "+
			"committed set", htlcIndex)
	}

	var target input.EvmHTLC
	for _, h := range htlcs {
		if h.Index == htlcIndex {
			target = h

			break
		}
	}

	channelID := chainhash.Hash(evmChannelID(lc.channelState))
	if preimage != nil {
		return input.BuildEvmClaimHtlcTx(
			channelID, target, proof, *preimage,
		)
	}

	return input.BuildEvmTimeoutHtlcTx(channelID, target, proof)
}

// evmDistributeFundsTx builds the distributeFunds carrier that finalises a
// unilateral close after the challenge window — the simplified EVM sweep (the
// contract pushes funds directly, so there is no second-level sweep tx).
func (lc *LightningChannel) evmDistributeFundsTx() (*wire.MsgTx, error) {
	return input.BuildEvmDistributeFundsTx(
		chainhash.Hash(evmChannelID(lc.channelState)),
	)
}

// evmCoopCloseFinalBalances computes the canonical cooperative-close split in
// raw token base-units. The contract requires finalA + finalB ==
// totalDeposited exactly, and gas is paid out-of-band, so the negotiated
// closing fee plays no role: B (the non-funder) receives its settled
// commitment balance floored to base-units, and A (the funder) receives the
// remainder — which folds the commit fee, anchor amounts and any sub-unit
// dust back to the party that funded them. Both peers derive the same pair
// because B's balance is channel-absolute.
func evmCoopCloseFinalBalances(
	chanState *channeldb.OpenChannel) (*big.Int, *big.Int) {

	commit := chanState.LocalCommitment

	// B's settled balance from this node's PoV: the remote balance if we
	// are the initiator (A), our own balance otherwise.
	bBalance := commit.RemoteBalance.ToSatoshis()
	if !chanState.IsInitiator {
		bBalance = commit.LocalBalance.ToSatoshis()
	}

	totalRaw := evmScaleToBase(chanState.Capacity)
	finalB := evmScaleToBase(bBalance)
	finalA := new(big.Int).Sub(totalRaw, finalB)

	return finalA, finalB
}

// evmCoopClose builds the canonical EIP-712 CooperativeClose artifact both
// peers sign for this channel's current settled state.
func (lc *LightningChannel) evmCoopClose() input.EvmCooperativeClose {
	finalA, finalB := evmCoopCloseFinalBalances(lc.channelState)

	return input.EvmCooperativeClose{
		ChannelID:     evmChannelID(lc.channelState),
		FinalBalanceA: finalA,
		FinalBalanceB: finalB,
	}
}

// signEvmCoopClose signs the EIP-712 CooperativeClose digest with the funding
// multisig key, returning the wire signature carried in closing_signed. It is
// the EVM replacement for signing the SegWit closing transaction, gated by
// evmChainActive in CreateCloseProposal.
func (lc *LightningChannel) signEvmCoopClose() (input.Signature, error) {
	evmSigner, ok := lc.Signer.(*input.EvmSigner)
	if !ok {
		return nil, fmt.Errorf("evm chain active but signer is %T, "+
			"not *input.EvmSigner", lc.Signer)
	}

	return evmSigner.SignCooperativeCloseWire(
		lc.channelState.LocalChanCfg.MultiSigKey,
		evmCommitmentDomain, lc.evmCoopClose(),
	)
}

// evmSigToRS64 converts an input.Signature (DER-serialising ECDSA) into the
// fixed 64-byte r ‖ s form RecoverEvmSigV consumes.
func evmSigToRS64(sig input.Signature) ([]byte, error) {
	wireSig, err := lnwire.NewSigFromSignature(sig)
	if err != nil {
		return nil, err
	}

	return wireSig.RawBytes(), nil
}

// evmCoopCloseCarrier verifies both parties' CooperativeClose signatures
// against the canonical digest, restores their recovery bytes, and assembles
// the closeChannel carrier tx. It is the EVM replacement for the SegWit
// witness assembly in CompleteCooperativeClose.
func (lc *LightningChannel) evmCoopCloseCarrier(localSig,
	remoteSig input.Signature) (*wire.MsgTx, error) {

	cc := lc.evmCoopClose()
	digest := cc.Digest(evmCommitmentDomain)
	ourAddr, theirAddr := evmPartyAddrs(lc.channelState)

	localRS, err := evmSigToRS64(localSig)
	if err != nil {
		return nil, err
	}
	remoteRS, err := evmSigToRS64(remoteSig)
	if err != nil {
		return nil, err
	}

	ourSig65, err := input.RecoverEvmSigV(localRS, digest, ourAddr)
	if err != nil {
		return nil, fmt.Errorf("evm coop close: local sig: %w", err)
	}
	theirSig65, err := input.RecoverEvmSigV(remoteRS, digest, theirAddr)
	if err != nil {
		return nil, fmt.Errorf("evm coop close: remote sig: %w", err)
	}

	// Map our/their onto the channel-absolute A/B the contract checks.
	sigA, sigB := ourSig65, theirSig65
	if !lc.channelState.IsInitiator {
		sigA, sigB = theirSig65, ourSig65
	}

	return input.BuildEvmChannelCloseTx(
		chainhash.Hash(cc.ChannelID), cc.FinalBalanceA,
		cc.FinalBalanceB, sigA, sigB,
	)
}

// evmStateUpdateFromDiskCommit rebuilds the canonical StateUpdate for a
// persisted commitment, mirroring buildEvmStateUpdate over the channeldb form
// (used at force-close time, when no in-memory commitment view is at hand).
func evmStateUpdateFromDiskCommit(chanState *channeldb.OpenChannel,
	commit *channeldb.ChannelCommitment) input.EvmStateUpdate {

	ourAddr, theirAddr := evmPartyAddrs(chanState)

	var htlcs []input.EvmHTLC
	for _, h := range commit.Htlcs {
		recipient := theirAddr
		if h.Incoming {
			recipient = ourAddr
		}

		var hashlock [32]byte
		copy(hashlock[:], h.RHash[:])

		htlcs = append(htlcs, input.EvmHTLC{
			Index:     h.HtlcIndex,
			Amount:    evmScaleToBase(h.Amt.ToSatoshis()),
			Hashlock:  hashlock,
			Timelock:  h.RefundTimeout,
			Recipient: recipient,
		})
	}

	// B = the non-funder, same channel-absolute convention (and the same
	// remainder-to-A rule) as buildEvmStateUpdate.
	bBalance := commit.RemoteBalance.ToSatoshis()
	if !chanState.IsInitiator {
		bBalance = commit.LocalBalance.ToSatoshis()
	}

	balanceA, balanceB := evmBalanceSplit(
		chanState.Capacity, bBalance, htlcs,
	)

	return input.EvmStateUpdate{
		ChannelID: evmChannelID(chanState),
		Nonce:     commit.CommitHeight,
		BalanceA:  balanceA,
		BalanceB:  balanceB,
		HtlcsHash: input.HtlcsMerkleRoot(htlcs),
	}
}

// evmLocalForceCloseCarrier assembles the forceClose carrier for this node's
// latest persisted local commitment: the canonical StateUpdate plus the
// counterparty's retained commitment signature with its recovery byte
// restored. It replaces the signed Bitcoin commitment tx as the broadcast
// artifact of a unilateral close, gated by evmChainActive in ForceClose.
func (lc *LightningChannel) evmLocalForceCloseCarrier() (*wire.MsgTx, error) {
	commit := lc.channelState.LocalCommitment
	su := evmStateUpdateFromDiskCommit(lc.channelState, &commit)
	digest := su.Digest(evmCommitmentDomain)

	// The persisted CommitSig is the remote's DER signature over our
	// commitment state; restore the fixed 64-byte form, then its v.
	derSig, err := ecdsa.ParseDERSignature(commit.CommitSig)
	if err != nil {
		return nil, fmt.Errorf("evm force close: parse retained "+
			"commit sig: %w", err)
	}
	remoteRS, err := evmSigToRS64(derSig)
	if err != nil {
		return nil, err
	}

	_, theirAddr := evmPartyAddrs(lc.channelState)
	sig65, err := input.RecoverEvmSigV(remoteRS, digest, theirAddr)
	if err != nil {
		return nil, fmt.Errorf("evm force close: retained sig does "+
			"not recover to counterparty: %w", err)
	}

	return input.BuildEvmForceCloseTx(
		chainhash.Hash(su.ChannelID), su.Nonce, su.BalanceA,
		su.BalanceB, su.HtlcsHash, sig65,
		lc.channelState.LocalChanCfg.MultiSigKey.PubKey.
			SerializeCompressed(),
	)
}

// verifyEvmCommitment checks a remote commitment signature against the EIP-712
// StateUpdate for the given view, using the remote funding multisig pubkey. It
// is the EVM replacement for SegWit sighash verification, gated by
// evmChainActive in ReceiveNewCommitment.
func (lc *LightningChannel) verifyEvmCommitment(view *commitment,
	cSig input.Signature) bool {

	su := lc.stateUpdateForView(view)
	digest := su.Digest(evmCommitmentDomain)

	return cSig.Verify(
		digest[:], lc.channelState.RemoteChanCfg.MultiSigKey.PubKey,
	)
}
