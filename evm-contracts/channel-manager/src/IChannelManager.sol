// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/// @title IChannelManager
/// @notice Interface for the Loka Lightning `ChannelManager` escrow contract.
///
/// This is the EVM settlement surface for the Loka fork of LND. It mirrors the
/// Sui `lightning` Move module: where Sui uses a shared `Channel` object and
/// Move entry functions, EVM uses a single escrow contract holding a
/// `mapping(bytes32 => Channel)` keyed by `channelId`.
///
/// The off-chain protocol semantics (StateNum, HTLC add/settle/fail, revocation
/// window) live unchanged in LND's `lnwallet/channel.go`; only the *signed
/// artifact* (an EIP-712 `StateUpdate`) and the *settlement calls* below differ.
///
/// References:
///   - 1-refactor-docs/evm/lnd-evm-refactor-plan.md §3 (interface)
///   - 1-refactor-docs/evm/evm-ln-interaction-spec.md §1-§2 (data + EIP-712)
interface IChannelManager {
    /// @notice Lifecycle status of a channel.
    enum Status {
        NONEXISTENT, // 0: never opened (zero value)
        OPEN, // 1: funded and operating off-chain
        CLOSING_COOP, // 2: transient; closeChannel paid out (terminal in practice)
        CLOSING_UNILATERAL, // 3: forceClose initiated, challenge window open
        CLOSED // 4: funds dispersed
    }

    /// @notice An HTLC as presented to claimHtlc / timeoutHtlc, proven against
    /// the `htlcsHash` Merkle root committed at force-close.
    struct HTLC {
        uint256 index; // == LND UpdateLog index; Merkle sort key
        uint256 amount; // token base-units
        bytes32 hashlock; // sha256(preimage) — SHA-256 (BOLT), NOT keccak256
        uint32 timelock; // absolute block.timestamp deadline (replaces CLTV)
        address recipient; // party credited on successful claim
    }

    // ---------------------------------------------------------------------
    // Events — every event is `indexed` on channelId so the LND `evmnotify`
    // adapter can filter by channel with a single topic. Balances and the
    // resolved broadcaster/victim are emitted up front so the adapter never
    // needs a follow-up eth_call to dispatch to a resolver.
    // ---------------------------------------------------------------------

    /// @notice Emitted when a channel is opened and both deposits are escrowed.
    event ChannelOpened(
        bytes32 indexed channelId,
        address participantA,
        address participantB,
        uint256 balanceA,
        uint256 balanceB
    );

    /// @notice Emitted on a cooperative close (both signatures supplied).
    event ChannelClosed(
        bytes32 indexed channelId, uint256 finalBalanceA, uint256 finalBalanceB
    );

    /// @notice Emitted when a unilateral (force) close is initiated.
    event UnilateralCloseInitiated(
        bytes32 indexed channelId,
        address broadcaster,
        uint256 nonce,
        uint256 balanceA,
        uint256 balanceB,
        uint256 challengeExpiry
    );

    /// @notice Emitted when a breach penalty is executed against a cheater.
    event ChannelPunished(
        bytes32 indexed channelId, address victim, uint256 rewardAmount
    );

    /// @notice Emitted when an HTLC is claimed with its preimage. The preimage
    /// is exactly what the upstream `htlcSuccessResolver` needs to settle the
    /// incoming HTLC (the on-chain leg of cross-chain atomic-swap routing).
    event HTLCClaimed(
        bytes32 indexed channelId, uint256 indexed htlcIndex, bytes32 preimage
    );

    /// @notice Emitted when an HTLC is timed out after its timelock.
    event HTLCTimeout(bytes32 indexed channelId, uint256 indexed htlcIndex);

    /// @notice Emitted when a CLOSING_UNILATERAL channel is finally settled
    /// after the challenge window elapses.
    event FundsDistributed(
        bytes32 indexed channelId, uint256 finalBalanceA, uint256 finalBalanceB
    );

    // ---------------------------------------------------------------------
    // Settlement surface — see evm-ln-interaction-spec.md §2.4 for the LND
    // resolver that drives each call.
    // ---------------------------------------------------------------------

    /// @notice Open a channel by escrowing ERC20 tokens from both participants.
    /// The caller funds `localFundingAmount`; the counterparty must have
    /// approved `remoteFundingAmount` (zero for single-funded channels). When
    /// `remoteFundingAmount > 0`, `counterpartySig` must be the counterparty's
    /// EIP-712 OpenChannel signature consenting to the dual-funded open (audit
    /// M-3); it is ignored for single-funded opens.
    function openChannel(
        bytes32 salt,
        address counterparty,
        uint256 localFundingAmount,
        uint256 remoteFundingAmount,
        bytes calldata counterpartySig
    ) external returns (bytes32 channelId);

    /// @notice Cooperatively close a channel; both EIP-712 signatures required.
    /// `nonce` is the channel state number the split is agreed at; it must not
    /// be below any previously recorded nonce (audit M-2 replay guard).
    function closeChannel(
        bytes32 channelId,
        uint256 nonce,
        uint256 finalBalanceA,
        uint256 finalBalanceB,
        bytes calldata sigA,
        bytes calldata sigB
    ) external;

    /// @notice Force close unilaterally with a single co-signed state, opening
    /// the challenge window bound to the broadcaster.
    function forceClose(
        bytes32 channelId,
        uint256 nonce,
        uint256 balanceA,
        uint256 balanceB,
        bytes32 htlcsHash,
        bytes calldata sig
    ) external;

    /// @notice Claim an HTLC with its preimage during the challenge window,
    /// proving inclusion in the committed `htlcsHash`.
    function claimHtlc(
        bytes32 channelId,
        HTLC calldata htlc,
        bytes32[] calldata merkleProof,
        bytes32 preimage
    ) external;

    /// @notice Time out an HTLC after its timelock, proving inclusion in the
    /// committed `htlcsHash`.
    function timeoutHtlc(
        bytes32 channelId, HTLC calldata htlc, bytes32[] calldata merkleProof
    ) external;

    /// @notice Submit a higher-nonce co-signed state to penalize a counterparty
    /// who force-closed with a revoked (lower-nonce) state.
    function penalize(
        bytes32 channelId,
        uint256 correctNonce,
        uint256 balanceA,
        uint256 balanceB,
        bytes32 htlcsHash,
        bytes calldata correctSig
    ) external;

    /// @notice Disperse escrowed funds after the challenge window elapses.
    function distributeFunds(bytes32 channelId) external;
}
