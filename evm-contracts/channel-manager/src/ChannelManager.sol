// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {IChannelManager} from "./IChannelManager.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";
import {MerkleProof} from "@openzeppelin/contracts/utils/cryptography/MerkleProof.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

/// @title ChannelManager
/// @notice EVM settlement surface for the Loka fork of LND. A single escrow
/// contract — one deployment per (chain, ERC20 asset) sub-network — holds a
/// `mapping(bytes32 => Channel)` keyed by `channelId`. It mirrors the Sui
/// `lightning` Move module: the off-chain protocol semantics (`StateNum`, HTLC
/// add/settle/fail, revocation window) live unchanged in LND's
/// `lnwallet/channel.go`; only the signed artifact (an EIP-712 `StateUpdate`)
/// and the settlement calls below differ.
///
/// References:
///   - 1-refactor-docs/evm/lnd-evm-refactor-plan.md §3 (interface) / §4 (deploy)
///   - 1-refactor-docs/evm/evm-ln-interaction-spec.md §1-§5 (data, EIP-712,
///     key implementation checks)
contract ChannelManager is IChannelManager, EIP712, ReentrancyGuard {
    using SafeERC20 for IERC20;

    // ---------------------------------------------------------------------
    // EIP-712 type hashes — evm-ln-interaction-spec.md §2.2.
    // ---------------------------------------------------------------------

    /// @dev The per-commitment artifact both peers exchange and that may later
    /// be presented to `forceClose`/`penalize`. Balances are net of outstanding
    /// HTLCs (the BOLT commitment model).
    bytes32 private constant STATE_UPDATE_TYPEHASH = keccak256(
        "StateUpdate(bytes32 channelId,uint256 nonce,uint256 balanceA,uint256 balanceB,bytes32 htlcsHash)"
    );

    /// @dev Cooperative close commits to the agreed final split AND the channel
    /// state number (`nonce`). `channelId` + the EIP-712 domain bind it to one
    /// channel on one sub-network; the `nonce` binds it to one off-chain state,
    /// so an older co-signed split from an aborted negotiation cannot be
    /// replayed in place of the latest agreed one (audit M-2).
    bytes32 private constant COOP_CLOSE_TYPEHASH = keccak256(
        "CooperativeClose(bytes32 channelId,uint256 nonce,uint256 finalBalanceA,uint256 finalBalanceB)"
    );

    /// @dev Authorizes a dual-funded open: the counterparty signs this to
    /// consent to its `remoteFundingAmount` being pulled into a channel the
    /// initiator parameterizes. Required only when remoteFundingAmount > 0, so
    /// single-funded opens (the counterparty has nothing at stake) need no
    /// signature (audit M-3).
    bytes32 private constant OPEN_CHANNEL_TYPEHASH = keccak256(
        "OpenChannel(bytes32 salt,address participantA,address participantB,uint256 localFundingAmount,uint256 remoteFundingAmount)"
    );

    // ---------------------------------------------------------------------
    // Storage — Channel record per evm-ln-interaction-spec.md §1, extended
    // with the running payout/HTLC tallies the settlement flow accumulates.
    // ---------------------------------------------------------------------

    struct Channel {
        address participantA; // the funder / initiator (msg.sender of openChannel)
        address participantB; // the counterparty
        uint256 totalDeposited; // sum of both escrowed deposits, base-units
        Status status;
        uint256 nonce; // highest settled StateUpdate.nonce (== LND StateNum)
        uint256 challengeExpiry; // block.timestamp deadline; 0 unless CLOSING_UNILATERAL
        address broadcaster; // who force-closed; the challenge window binds to THIS party
        bytes32 htlcsHash; // Merkle root of the active HTLC set committed at force-close
        uint256 payoutA; // accruing final payout to A (to_local + claimed/timed-out HTLCs)
        uint256 payoutB; // accruing final payout to B
        uint256 htlcPool; // value still locked in unresolved HTLCs
    }

    /// @notice The single ERC20 asset this sub-network settles in. Immutable so
    /// the decimals-scaling assumption (spec §5) is fixed at deploy time.
    IERC20 public immutable token;

    /// @notice Seconds the unilateral-close challenge window stays open. Uses
    /// `block.timestamp`, not block height, because L2 block intervals vary.
    uint256 public immutable challengePeriod;

    /// @notice Emergency grace beyond the challenge window after which a
    /// channel carrying an HTLC that can never be resolved — a malformed but
    /// mutually co-signed set: a `recipient` that is neither participant, or
    /// amounts that don't reconcile to `htlcPool` — can still be finalized,
    /// splitting the unattributable residual evenly between the participants
    /// instead of locking the whole deposit forever (see security-audit.md
    /// H-2). Fixed far longer than any realistic HTLC timelock (BOLT
    /// `max_cltv_expiry` is ~2 weeks) so a legitimate HTLC is always resolved
    /// correctly — claim/timeout pays its rightful owner 100% — long before
    /// this backstop can fire. Because the split is always weakly worse for an
    /// HTLC's rightful owner than resolving it, it adds no griefing incentive;
    /// it is purely a liveness backstop for malformed state.
    uint256 public constant EMERGENCY_RESOLUTION_DELAY = 30 days;

    /// @notice channelId => channel record.
    mapping(bytes32 => Channel) public channels;

    /// @notice channelId => htlcIndex => resolved, preventing double claim/timeout.
    mapping(bytes32 => mapping(uint256 => bool)) public htlcResolved;

    // ---------------------------------------------------------------------
    // Errors
    // ---------------------------------------------------------------------

    error ChannelAlreadyExists();
    error ChannelNotOpen();
    error NotUnilateralClose();
    error InvalidParticipants();
    error ZeroDeposit();
    error BalanceConservationViolated();
    error NotAParticipant();
    error InvalidSignature();
    error StaleNonce();
    error ChallengeWindowClosed();
    error ChallengeWindowOpen();
    error HtlcAlreadyResolved();
    error InvalidPreimage();
    error InvalidMerkleProof();
    error TimelockNotExpired();
    error RecipientNotParticipant();
    error HtlcsUnresolved();

    /// @param token_ the ERC20 asset escrowed by every channel in this contract.
    /// @param challengePeriod_ seconds the force-close challenge window stays open.
    constructor(address token_, uint256 challengePeriod_)
        EIP712("LokaChannelManager", "1")
    {
        if (token_ == address(0)) revert InvalidParticipants();
        token = IERC20(token_);
        challengePeriod = challengePeriod_;
    }

    // ---------------------------------------------------------------------
    // Open
    // ---------------------------------------------------------------------

    /// @inheritdoc IChannelManager
    function openChannel(
        bytes32 salt,
        address counterparty,
        uint256 localFundingAmount,
        uint256 remoteFundingAmount,
        bytes calldata counterpartySig
    ) external nonReentrant returns (bytes32 channelId) {
        if (counterparty == address(0) || counterparty == msg.sender) {
            revert InvalidParticipants();
        }
        if (localFundingAmount == 0 && remoteFundingAmount == 0) {
            revert ZeroDeposit();
        }

        // channelId = keccak256(participantA, participantB, salt). The salt lets
        // the same pair open multiple channels (spec §1).
        channelId =
            keccak256(abi.encodePacked(msg.sender, counterparty, salt));

        Channel storage ch = channels[channelId];
        if (ch.status != Status.NONEXISTENT) revert ChannelAlreadyExists();

        // Dual-funded: the counterparty must explicitly consent to its deposit
        // being pulled, so a stale ERC20 allowance can't be swept into a
        // channel it never agreed to (audit M-3). Single-funded opens
        // (remoteFundingAmount == 0) put nothing of the counterparty's at
        // stake, so no signature is required.
        if (remoteFundingAmount > 0) {
            bytes32 openDigest = _hashTypedDataV4(
                keccak256(
                    abi.encode(
                        OPEN_CHANNEL_TYPEHASH,
                        salt,
                        msg.sender,
                        counterparty,
                        localFundingAmount,
                        remoteFundingAmount
                    )
                )
            );
            if (ECDSA.recover(openDigest, counterpartySig) != counterparty) {
                revert InvalidSignature();
            }
        }

        ch.participantA = msg.sender;
        ch.participantB = counterparty;
        ch.totalDeposited = localFundingAmount + remoteFundingAmount;
        ch.status = Status.OPEN;

        // Pull the funder's deposit, then the counterparty's (zero for a
        // single-funded channel). The counterparty must have approved first.
        if (localFundingAmount > 0) {
            token.safeTransferFrom(
                msg.sender, address(this), localFundingAmount
            );
        }
        if (remoteFundingAmount > 0) {
            token.safeTransferFrom(
                counterparty, address(this), remoteFundingAmount
            );
        }

        emit ChannelOpened(
            channelId,
            msg.sender,
            counterparty,
            localFundingAmount,
            remoteFundingAmount
        );
    }

    // ---------------------------------------------------------------------
    // Cooperative close
    // ---------------------------------------------------------------------

    /// @inheritdoc IChannelManager
    function closeChannel(
        bytes32 channelId,
        uint256 nonce,
        uint256 finalBalanceA,
        uint256 finalBalanceB,
        bytes calldata sigA,
        bytes calldata sigB
    ) external nonReentrant {
        Channel storage ch = channels[channelId];
        if (ch.status != Status.OPEN) revert ChannelNotOpen();
        if (finalBalanceA + finalBalanceB != ch.totalDeposited) {
            revert BalanceConservationViolated();
        }
        // The close is bound to a state number: a stale-state co-signed split
        // (nonce below the latest the parties advanced to) is rejected. Both
        // signatures cover the nonce, so neither party can downgrade it.
        if (nonce < ch.nonce) revert StaleNonce();

        bytes32 digest = _hashTypedDataV4(
            keccak256(
                abi.encode(
                    COOP_CLOSE_TYPEHASH,
                    channelId,
                    nonce,
                    finalBalanceA,
                    finalBalanceB
                )
            )
        );
        if (ECDSA.recover(digest, sigA) != ch.participantA) {
            revert InvalidSignature();
        }
        if (ECDSA.recover(digest, sigB) != ch.participantB) {
            revert InvalidSignature();
        }

        ch.nonce = nonce;
        ch.status = Status.CLOSED;

        address a = ch.participantA;
        address b = ch.participantB;
        if (finalBalanceA > 0) token.safeTransfer(a, finalBalanceA);
        if (finalBalanceB > 0) token.safeTransfer(b, finalBalanceB);

        emit ChannelClosed(channelId, finalBalanceA, finalBalanceB);
    }

    // ---------------------------------------------------------------------
    // Unilateral (force) close
    // ---------------------------------------------------------------------

    /// @inheritdoc IChannelManager
    function forceClose(
        bytes32 channelId,
        uint256 nonce,
        uint256 balanceA,
        uint256 balanceB,
        bytes32 htlcsHash,
        bytes calldata sig
    ) external {
        Channel storage ch = channels[channelId];
        if (ch.status != Status.OPEN) revert ChannelNotOpen();
        if (msg.sender != ch.participantA && msg.sender != ch.participantB) {
            revert NotAParticipant();
        }
        // to_local + to_remote may not exceed the deposit; the remainder is the
        // value still committed to active HTLCs.
        if (balanceA + balanceB > ch.totalDeposited) {
            revert BalanceConservationViolated();
        }

        // The broadcaster presents a state co-signed by the OTHER party — that
        // is what proves this state was mutually agreed.
        address counterparty =
            msg.sender == ch.participantA ? ch.participantB : ch.participantA;
        bytes32 digest = _stateUpdateDigest(
            channelId, nonce, balanceA, balanceB, htlcsHash
        );
        if (ECDSA.recover(digest, sig) != counterparty) {
            revert InvalidSignature();
        }

        ch.status = Status.CLOSING_UNILATERAL;
        ch.nonce = nonce;
        ch.broadcaster = msg.sender;
        ch.htlcsHash = htlcsHash;
        ch.payoutA = balanceA;
        ch.payoutB = balanceB;
        ch.htlcPool = ch.totalDeposited - balanceA - balanceB;
        ch.challengeExpiry = block.timestamp + challengePeriod;

        emit UnilateralCloseInitiated(
            channelId,
            msg.sender,
            nonce,
            balanceA,
            balanceB,
            ch.challengeExpiry
        );
    }

    /// @inheritdoc IChannelManager
    function penalize(
        bytes32 channelId,
        uint256 correctNonce,
        uint256 balanceA,
        uint256 balanceB,
        bytes32 htlcsHash,
        bytes calldata correctSig
    ) external nonReentrant {
        Channel storage ch = channels[channelId];
        if (ch.status != Status.CLOSING_UNILATERAL) {
            revert NotUnilateralClose();
        }
        if (block.timestamp >= ch.challengeExpiry) {
            revert ChallengeWindowClosed();
        }
        // A strictly-higher nonce proves the broadcast state was revoked.
        if (correctNonce <= ch.nonce) revert StaleNonce();

        // The cheater (broadcaster) must have signed this later state — that is
        // the proof they force-closed with an obsolete one.
        bytes32 digest = _stateUpdateDigest(
            channelId, correctNonce, balanceA, balanceB, htlcsHash
        );
        if (ECDSA.recover(digest, correctSig) != ch.broadcaster) {
            revert InvalidSignature();
        }

        // Justice: the entire deposit goes to the VICTIM — the non-broadcasting
        // party, derived from the recorded `broadcaster` — NOT to msg.sender.
        // Paying a fixed, proof-derived address (rather than the caller) lets a
        // watchtower or any altruistic relayer holding the higher-nonce
        // co-signed state submit the proof on the victim's behalf while the
        // victim is offline. A leaked higher-nonce state can then at worst
        // return the victim their own funds; it can never enable theft, so the
        // caller is intentionally unconstrained. No HTLC funds were pushed out
        // yet (claim/timeout only accrue into the payout tallies), so
        // totalDeposited is fully intact.
        address victim = ch.broadcaster == ch.participantA
            ? ch.participantB
            : ch.participantA;

        uint256 reward = ch.totalDeposited;
        ch.status = Status.CLOSED;
        ch.htlcPool = 0;
        ch.payoutA = 0;
        ch.payoutB = 0;

        token.safeTransfer(victim, reward);

        emit ChannelPunished(channelId, victim, reward);
    }

    // ---------------------------------------------------------------------
    // HTLC resolution (during the challenge window)
    // ---------------------------------------------------------------------

    /// @inheritdoc IChannelManager
    function claimHtlc(
        bytes32 channelId,
        HTLC calldata htlc,
        bytes32[] calldata merkleProof,
        bytes32 preimage
    ) external {
        Channel storage ch = channels[channelId];
        if (ch.status != Status.CLOSING_UNILATERAL) {
            revert NotUnilateralClose();
        }
        if (htlcResolved[channelId][htlc.index]) revert HtlcAlreadyResolved();

        // Hashlock is SHA-256 (BOLT payment hash), NOT keccak256 — see spec §5.
        if (sha256(abi.encodePacked(preimage)) != htlc.hashlock) {
            revert InvalidPreimage();
        }
        _verifyHtlcInclusion(ch.htlcsHash, htlc, merkleProof);

        htlcResolved[channelId][htlc.index] = true;

        // Credit the receiver only; never re-debit the sender — the balances
        // committed at force-close are already net of HTLCs (spec §2.2, the
        // Sui audit's C-3 invariant).
        if (htlc.recipient == ch.participantA) {
            ch.payoutA += htlc.amount;
        } else if (htlc.recipient == ch.participantB) {
            ch.payoutB += htlc.amount;
        } else {
            revert RecipientNotParticipant();
        }
        ch.htlcPool -= htlc.amount;

        emit HTLCClaimed(channelId, htlc.index, preimage);
    }

    /// @inheritdoc IChannelManager
    function timeoutHtlc(
        bytes32 channelId,
        HTLC calldata htlc,
        bytes32[] calldata merkleProof
    ) external {
        Channel storage ch = channels[channelId];
        if (ch.status != Status.CLOSING_UNILATERAL) {
            revert NotUnilateralClose();
        }
        if (htlcResolved[channelId][htlc.index]) revert HtlcAlreadyResolved();
        if (block.timestamp < htlc.timelock) revert TimelockNotExpired();
        _verifyHtlcInclusion(ch.htlcsHash, htlc, merkleProof);

        htlcResolved[channelId][htlc.index] = true;

        // On timeout the value reverts to the offerer — the participant that is
        // not the HTLC recipient.
        if (htlc.recipient == ch.participantA) {
            ch.payoutB += htlc.amount;
        } else if (htlc.recipient == ch.participantB) {
            ch.payoutA += htlc.amount;
        } else {
            revert RecipientNotParticipant();
        }
        ch.htlcPool -= htlc.amount;

        emit HTLCTimeout(channelId, htlc.index);
    }

    // ---------------------------------------------------------------------
    // Finalize
    // ---------------------------------------------------------------------

    /// @inheritdoc IChannelManager
    function distributeFunds(bytes32 channelId) external nonReentrant {
        Channel storage ch = channels[channelId];
        if (ch.status != Status.CLOSING_UNILATERAL) {
            revert NotUnilateralClose();
        }
        if (block.timestamp < ch.challengeExpiry) {
            revert ChallengeWindowOpen();
        }

        uint256 finalA = ch.payoutA;
        uint256 finalB = ch.payoutB;

        // Every HTLC must be claimed or timed out first; otherwise its value
        // has no determined owner. Both parties are incentivized to resolve
        // theirs (receiver claims, offerer times out after the timelock).
        if (ch.htlcPool != 0) {
            // Escape hatch: an HTLC that can never be resolved (malformed but
            // co-signed — see EMERGENCY_RESOLUTION_DELAY) would otherwise lock
            // the entire deposit forever. Once the emergency grace has elapsed,
            // finalize anyway, splitting the unattributable residual evenly.
            // payoutA + payoutB + htlcPool is invariant == totalDeposited, so
            // the full deposit is still paid out and nothing is stranded.
            if (block.timestamp < ch.challengeExpiry + EMERGENCY_RESOLUTION_DELAY)
            {
                revert HtlcsUnresolved();
            }
            uint256 residual = ch.htlcPool;
            uint256 half = residual / 2;
            finalA += half;
            finalB += residual - half;
            ch.htlcPool = 0;
        }

        ch.status = Status.CLOSED;

        address a = ch.participantA;
        address b = ch.participantB;
        if (finalA > 0) token.safeTransfer(a, finalA);
        if (finalB > 0) token.safeTransfer(b, finalB);

        emit FundsDistributed(channelId, finalA, finalB);
    }

    // ---------------------------------------------------------------------
    // Views & internals
    // ---------------------------------------------------------------------

    /// @notice Compute the channelId for a participant pair and salt, matching
    /// the on-chain derivation. Convenience for the LND adapter and tests.
    function computeChannelId(
        address participantA,
        address participantB,
        bytes32 salt
    ) external pure returns (bytes32) {
        return keccak256(abi.encodePacked(participantA, participantB, salt));
    }

    /// @notice The EIP-712 domain separator, exposed so off-chain signers (the
    /// LND `evm_signer`) can reproduce the exact digest.
    function domainSeparator() external view returns (bytes32) {
        return _domainSeparatorV4();
    }

    /// @dev EIP-712 digest of a `StateUpdate` — the shared helper for
    /// `forceClose` and `penalize`.
    function _stateUpdateDigest(
        bytes32 channelId,
        uint256 nonce,
        uint256 balanceA,
        uint256 balanceB,
        bytes32 htlcsHash
    ) internal view returns (bytes32) {
        return _hashTypedDataV4(
            keccak256(
                abi.encode(
                    STATE_UPDATE_TYPEHASH,
                    channelId,
                    nonce,
                    balanceA,
                    balanceB,
                    htlcsHash
                )
            )
        );
    }

    /// @dev Verify an HTLC's inclusion in the committed `htlcsHash` Merkle root.
    /// Leaf = keccak256(abi.encode(htlc)); leaves are ordered by `index` on the
    /// Go side (spec §2.3). OpenZeppelin's commutative pair hashing is used.
    function _verifyHtlcInclusion(
        bytes32 root,
        HTLC calldata htlc,
        bytes32[] calldata merkleProof
    ) internal pure {
        bytes32 leaf = keccak256(abi.encode(htlc));
        if (!MerkleProof.verify(merkleProof, root, leaf)) {
            revert InvalidMerkleProof();
        }
    }
}
