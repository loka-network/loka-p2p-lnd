// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test} from "forge-std/Test.sol";
import {ChannelManager} from "../src/ChannelManager.sol";
import {IChannelManager} from "../src/IChannelManager.sol";
import {MockERC20} from "./mocks/MockERC20.sol";

/// @title ChannelManagerTest
/// @notice Foundry suite for the EVM settlement contract, covering the five
/// scenarios in testing-verification.md §1.2: open, cooperative close,
/// unilateral close + challenge window, breach penalty, and HTLC claim/timeout.
contract ChannelManagerTest is Test {
    // EIP-712 type hashes — must match ChannelManager exactly.
    bytes32 private constant STATE_UPDATE_TYPEHASH = keccak256(
        "StateUpdate(bytes32 channelId,uint256 nonce,uint256 balanceA,uint256 balanceB,bytes32 htlcsHash)"
    );
    bytes32 private constant COOP_CLOSE_TYPEHASH = keccak256(
        "CooperativeClose(bytes32 channelId,uint256 finalBalanceA,uint256 finalBalanceB)"
    );

    uint256 private constant CHALLENGE_PERIOD = 1 days;
    uint256 private constant FUND_A = 600e6; // 600 USDC (6 decimals)
    uint256 private constant FUND_B = 400e6; // 400 USDC

    ChannelManager private mgr;
    MockERC20 private token;

    uint256 private alicePk = 0xA11CE;
    uint256 private bobPk = 0xB0B;
    address private alice;
    address private bob;

    bytes32 private salt = bytes32(uint256(7));

    function setUp() public {
        alice = vm.addr(alicePk);
        bob = vm.addr(bobPk);

        token = new MockERC20("Mock USD Coin", "USDC", 6);
        mgr = new ChannelManager(address(token), CHALLENGE_PERIOD);

        token.mint(alice, 1_000e6);
        token.mint(bob, 1_000e6);
    }

    // ---------------------------------------------------------------------
    // Helpers
    // ---------------------------------------------------------------------

    /// @dev Open a dual-funded Alice/Bob channel and return its id.
    function _openChannel() internal returns (bytes32 channelId) {
        vm.prank(alice);
        token.approve(address(mgr), FUND_A);
        vm.prank(bob);
        token.approve(address(mgr), FUND_B);

        vm.prank(alice);
        channelId = mgr.openChannel(salt, bob, FUND_A, FUND_B);
    }

    function _stateDigest(
        bytes32 channelId,
        uint256 nonce,
        uint256 balanceA,
        uint256 balanceB,
        bytes32 htlcsHash
    ) internal view returns (bytes32) {
        bytes32 structHash = keccak256(
            abi.encode(
                STATE_UPDATE_TYPEHASH,
                channelId,
                nonce,
                balanceA,
                balanceB,
                htlcsHash
            )
        );
        return _typed(structHash);
    }

    function _coopDigest(
        bytes32 channelId,
        uint256 finalBalanceA,
        uint256 finalBalanceB
    ) internal view returns (bytes32) {
        bytes32 structHash = keccak256(
            abi.encode(
                COOP_CLOSE_TYPEHASH, channelId, finalBalanceA, finalBalanceB
            )
        );
        return _typed(structHash);
    }

    function _typed(bytes32 structHash) internal view returns (bytes32) {
        return keccak256(
            abi.encodePacked("\x19\x01", mgr.domainSeparator(), structHash)
        );
    }

    function _sign(uint256 pk, bytes32 digest)
        internal
        pure
        returns (bytes memory)
    {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(pk, digest);
        return abi.encodePacked(r, s, v);
    }

    // ---------------------------------------------------------------------
    // Open
    // ---------------------------------------------------------------------

    function test_OpenChannel_EscrowsAndEmits() public {
        bytes32 expectedId = mgr.computeChannelId(alice, bob, salt);

        vm.prank(alice);
        token.approve(address(mgr), FUND_A);
        vm.prank(bob);
        token.approve(address(mgr), FUND_B);

        vm.expectEmit(true, true, true, true);
        emit IChannelManager.ChannelOpened(
            expectedId, alice, bob, FUND_A, FUND_B
        );

        vm.prank(alice);
        bytes32 channelId = mgr.openChannel(salt, bob, FUND_A, FUND_B);

        assertEq(channelId, expectedId);
        assertEq(token.balanceOf(address(mgr)), FUND_A + FUND_B);
        assertEq(token.balanceOf(alice), 1_000e6 - FUND_A);
        assertEq(token.balanceOf(bob), 1_000e6 - FUND_B);

        (,, uint256 totalDeposited, IChannelManager.Status status,,,,,,,) =
            mgr.channels(channelId);
        assertEq(totalDeposited, FUND_A + FUND_B);
        assertEq(uint8(status), uint8(IChannelManager.Status.OPEN));
    }

    function test_OpenChannel_RevertsOnDuplicate() public {
        _openChannel();
        vm.prank(alice);
        token.approve(address(mgr), FUND_A);
        vm.prank(bob);
        token.approve(address(mgr), FUND_B);
        vm.prank(alice);
        vm.expectRevert(ChannelManager.ChannelAlreadyExists.selector);
        mgr.openChannel(salt, bob, FUND_A, FUND_B);
    }

    function test_OpenChannel_RevertsOnSelfCounterparty() public {
        vm.prank(alice);
        vm.expectRevert(ChannelManager.InvalidParticipants.selector);
        mgr.openChannel(salt, alice, FUND_A, 0);
    }

    function test_OpenChannel_SingleFunded() public {
        vm.prank(alice);
        token.approve(address(mgr), FUND_A);
        vm.prank(alice);
        bytes32 channelId = mgr.openChannel(salt, bob, FUND_A, 0);
        assertEq(token.balanceOf(address(mgr)), FUND_A);
        (,, uint256 totalDeposited,,,,,,,,) = mgr.channels(channelId);
        assertEq(totalDeposited, FUND_A);
    }

    // ---------------------------------------------------------------------
    // Cooperative close
    // ---------------------------------------------------------------------

    function test_CooperativeClose_DistributesFunds() public {
        bytes32 channelId = _openChannel();

        uint256 finalA = 700e6; // Alice gained 100 off-chain
        uint256 finalB = 300e6;
        bytes32 digest = _coopDigest(channelId, finalA, finalB);

        vm.expectEmit(true, true, true, true);
        emit IChannelManager.ChannelClosed(channelId, finalA, finalB);

        mgr.closeChannel(
            channelId,
            finalA,
            finalB,
            _sign(alicePk, digest),
            _sign(bobPk, digest)
        );

        assertEq(token.balanceOf(alice), 1_000e6 - FUND_A + finalA);
        assertEq(token.balanceOf(bob), 1_000e6 - FUND_B + finalB);
        assertEq(token.balanceOf(address(mgr)), 0);
        (,,, IChannelManager.Status status,,,,,,,) = mgr.channels(channelId);
        assertEq(uint8(status), uint8(IChannelManager.Status.CLOSED));
    }

    function test_CooperativeClose_RevertsOnBadSignature() public {
        bytes32 channelId = _openChannel();
        uint256 finalA = 700e6;
        uint256 finalB = 300e6;
        bytes32 digest = _coopDigest(channelId, finalA, finalB);

        // Bob's slot signed by a stranger.
        bytes memory badSig = _sign(0xDEAD, digest);
        vm.expectRevert(ChannelManager.InvalidSignature.selector);
        mgr.closeChannel(
            channelId, finalA, finalB, _sign(alicePk, digest), badSig
        );
    }

    function test_CooperativeClose_RevertsOnNonConservingBalances() public {
        bytes32 channelId = _openChannel();
        uint256 finalA = 700e6;
        uint256 finalB = 301e6; // sums to more than the deposit
        bytes32 digest = _coopDigest(channelId, finalA, finalB);
        vm.expectRevert(ChannelManager.BalanceConservationViolated.selector);
        mgr.closeChannel(
            channelId,
            finalA,
            finalB,
            _sign(alicePk, digest),
            _sign(bobPk, digest)
        );
    }

    // ---------------------------------------------------------------------
    // Unilateral (force) close + challenge window
    // ---------------------------------------------------------------------

    function test_ForceClose_OpensChallengeWindow() public {
        bytes32 channelId = _openChannel();

        uint256 nonce = 5;
        uint256 balA = 550e6;
        uint256 balB = 450e6;
        // Alice broadcasts a state Bob co-signed.
        bytes memory bobSig =
            _sign(bobPk, _stateDigest(channelId, nonce, balA, balB, bytes32(0)));

        vm.prank(alice);
        mgr.forceClose(channelId, nonce, balA, balB, bytes32(0), bobSig);

        (
            ,
            ,
            ,
            IChannelManager.Status status,
            uint256 storedNonce,
            uint256 challengeExpiry,
            address broadcaster,
            ,
            ,
            ,
        ) = mgr.channels(channelId);
        assertEq(
            uint8(status), uint8(IChannelManager.Status.CLOSING_UNILATERAL)
        );
        assertEq(storedNonce, nonce);
        assertEq(broadcaster, alice);
        assertEq(challengeExpiry, block.timestamp + CHALLENGE_PERIOD);
    }

    function test_ForceClose_RevertsWhenNotCounterpartySig() public {
        bytes32 channelId = _openChannel();
        uint256 nonce = 5;
        uint256 balA = 550e6;
        uint256 balB = 450e6;
        // Alice signs her own state — must be the COUNTERPARTY's signature.
        bytes memory aliceSig = _sign(
            alicePk, _stateDigest(channelId, nonce, balA, balB, bytes32(0))
        );
        vm.prank(alice);
        vm.expectRevert(ChannelManager.InvalidSignature.selector);
        mgr.forceClose(channelId, nonce, balA, balB, bytes32(0), aliceSig);
    }

    function test_DistributeFunds_RevertsBeforeWindowCloses() public {
        bytes32 channelId = _openChannel();
        uint256 balA = 550e6;
        uint256 balB = 450e6;
        bytes memory bobSig =
            _sign(bobPk, _stateDigest(channelId, 5, balA, balB, bytes32(0)));
        vm.prank(alice);
        mgr.forceClose(channelId, 5, balA, balB, bytes32(0), bobSig);

        vm.expectRevert(ChannelManager.ChallengeWindowOpen.selector);
        mgr.distributeFunds(channelId);
    }

    function test_DistributeFunds_PaysOutAfterWindow() public {
        bytes32 channelId = _openChannel();
        uint256 balA = 550e6;
        uint256 balB = 450e6;
        bytes memory bobSig =
            _sign(bobPk, _stateDigest(channelId, 5, balA, balB, bytes32(0)));
        vm.prank(alice);
        mgr.forceClose(channelId, 5, balA, balB, bytes32(0), bobSig);

        vm.warp(block.timestamp + CHALLENGE_PERIOD + 1);

        vm.expectEmit(true, true, true, true);
        emit IChannelManager.FundsDistributed(channelId, balA, balB);
        mgr.distributeFunds(channelId);

        assertEq(token.balanceOf(alice), 1_000e6 - FUND_A + balA);
        assertEq(token.balanceOf(bob), 1_000e6 - FUND_B + balB);
        assertEq(token.balanceOf(address(mgr)), 0);
    }

    // ---------------------------------------------------------------------
    // Breach penalty
    // ---------------------------------------------------------------------

    function test_Penalize_SweepsToVictim() public {
        bytes32 channelId = _openChannel();

        // Alice cheats: force-closes with an old, revoked state (nonce 3).
        uint256 staleNonce = 3;
        uint256 staleA = 900e6; // Alice grabs most of the funds
        uint256 staleB = 100e6;
        bytes memory bobSigStale = _sign(
            bobPk,
            _stateDigest(channelId, staleNonce, staleA, staleB, bytes32(0))
        );
        vm.prank(alice);
        mgr.forceClose(
            channelId, staleNonce, staleA, staleB, bytes32(0), bobSigStale
        );

        // Bob holds a later state (nonce 7) that Alice signed — proof of cheat.
        uint256 goodNonce = 7;
        uint256 goodA = 500e6;
        uint256 goodB = 500e6;
        bytes memory aliceSigGood = _sign(
            alicePk,
            _stateDigest(channelId, goodNonce, goodA, goodB, bytes32(0))
        );

        vm.expectEmit(true, true, true, true);
        emit IChannelManager.ChannelPunished(channelId, bob, FUND_A + FUND_B);

        vm.prank(bob);
        mgr.penalize(
            channelId, goodNonce, goodA, goodB, bytes32(0), aliceSigGood
        );

        // Bob sweeps the entire deposit.
        assertEq(token.balanceOf(bob), 1_000e6 - FUND_B + FUND_A + FUND_B);
        assertEq(token.balanceOf(address(mgr)), 0);
        (,,, IChannelManager.Status status,,,,,,,) = mgr.channels(channelId);
        assertEq(uint8(status), uint8(IChannelManager.Status.CLOSED));
    }

    function test_Penalize_RevertsOnStaleNonce() public {
        bytes32 channelId = _openChannel();
        uint256 nonce = 5;
        bytes memory bobSig = _sign(
            bobPk, _stateDigest(channelId, nonce, 550e6, 450e6, bytes32(0))
        );
        vm.prank(alice);
        mgr.forceClose(channelId, nonce, 550e6, 450e6, bytes32(0), bobSig);

        // A "correct" state at the same nonce is not proof of a breach.
        bytes memory aliceSig = _sign(
            alicePk, _stateDigest(channelId, nonce, 500e6, 500e6, bytes32(0))
        );
        vm.prank(bob);
        vm.expectRevert(ChannelManager.StaleNonce.selector);
        mgr.penalize(channelId, nonce, 500e6, 500e6, bytes32(0), aliceSig);
    }

    function test_Penalize_RevertsAfterWindow() public {
        bytes32 channelId = _openChannel();
        bytes memory bobSig =
            _sign(bobPk, _stateDigest(channelId, 3, 900e6, 100e6, bytes32(0)));
        vm.prank(alice);
        mgr.forceClose(channelId, 3, 900e6, 100e6, bytes32(0), bobSig);

        vm.warp(block.timestamp + CHALLENGE_PERIOD + 1);

        bytes memory aliceSig =
            _sign(alicePk, _stateDigest(channelId, 7, 500e6, 500e6, bytes32(0)));
        vm.prank(bob);
        vm.expectRevert(ChannelManager.ChallengeWindowClosed.selector);
        mgr.penalize(channelId, 7, 500e6, 500e6, bytes32(0), aliceSig);
    }

    // ---------------------------------------------------------------------
    // HTLC resolution
    // ---------------------------------------------------------------------

    /// @dev Build a single-HTLC committed set. With one leaf the Merkle root is
    /// the leaf itself and the inclusion proof is empty.
    function _singleHtlc(
        uint256 amount,
        bytes32 preimage,
        uint32 timelock,
        address recipient
    )
        internal
        pure
        returns (IChannelManager.HTLC memory htlc, bytes32 root)
    {
        htlc = IChannelManager.HTLC({
            index: 1,
            amount: amount,
            hashlock: sha256(abi.encodePacked(preimage)),
            timelock: timelock,
            recipient: recipient
        });
        root = keccak256(abi.encode(htlc));
    }

    function test_ClaimHtlc_CreditsReceiverWithPreimage() public {
        bytes32 channelId = _openChannel();

        bytes32 preimage = keccak256("secret");
        uint256 htlcAmt = 50e6;
        (IChannelManager.HTLC memory htlc, bytes32 root) =
            _singleHtlc(htlcAmt, preimage, uint32(block.timestamp + 100), bob);

        // Force-close balances are net of the in-flight HTLC: A+B = total-htlc.
        uint256 balA = 550e6;
        uint256 balB = FUND_A + FUND_B - htlcAmt - balA; // 400
        bytes memory bobSig =
            _sign(bobPk, _stateDigest(channelId, 5, balA, balB, root));
        vm.prank(alice);
        mgr.forceClose(channelId, 5, balA, balB, root, bobSig);

        bytes32[] memory proof = new bytes32[](0);
        vm.expectEmit(true, true, true, true);
        emit IChannelManager.HTLCClaimed(channelId, htlc.index, preimage);
        mgr.claimHtlc(channelId, htlc, proof, preimage);

        assertTrue(mgr.htlcResolved(channelId, htlc.index));

        // After the window, Bob's payout includes the claimed HTLC amount.
        vm.warp(block.timestamp + CHALLENGE_PERIOD + 1);
        mgr.distributeFunds(channelId);
        assertEq(token.balanceOf(bob), 1_000e6 - FUND_B + balB + htlcAmt);
        assertEq(token.balanceOf(alice), 1_000e6 - FUND_A + balA);
    }

    function test_ClaimHtlc_RevertsOnWrongPreimage() public {
        bytes32 channelId = _openChannel();
        bytes32 preimage = keccak256("secret");
        uint256 htlcAmt = 50e6;
        (IChannelManager.HTLC memory htlc, bytes32 root) =
            _singleHtlc(htlcAmt, preimage, uint32(block.timestamp + 100), bob);
        uint256 balA = 550e6;
        uint256 balB = FUND_A + FUND_B - htlcAmt - balA;
        bytes memory bobSig =
            _sign(bobPk, _stateDigest(channelId, 5, balA, balB, root));
        vm.prank(alice);
        mgr.forceClose(channelId, 5, balA, balB, root, bobSig);

        bytes32[] memory proof = new bytes32[](0);
        vm.expectRevert(ChannelManager.InvalidPreimage.selector);
        mgr.claimHtlc(channelId, htlc, proof, keccak256("wrong"));
    }

    function test_TimeoutHtlc_RefundsOffererAfterTimelock() public {
        bytes32 channelId = _openChannel();

        bytes32 preimage = keccak256("secret");
        uint256 htlcAmt = 50e6;
        uint32 timelock = uint32(block.timestamp + 100);
        // Alice offers the HTLC to Bob (Bob is recipient); on timeout it reverts
        // to Alice, the offerer.
        (IChannelManager.HTLC memory htlc, bytes32 root) =
            _singleHtlc(htlcAmt, preimage, timelock, bob);
        uint256 balA = 550e6;
        uint256 balB = FUND_A + FUND_B - htlcAmt - balA;
        bytes memory bobSig =
            _sign(bobPk, _stateDigest(channelId, 5, balA, balB, root));
        vm.prank(alice);
        mgr.forceClose(channelId, 5, balA, balB, root, bobSig);

        bytes32[] memory proof = new bytes32[](0);

        // Too early.
        vm.expectRevert(ChannelManager.TimelockNotExpired.selector);
        mgr.timeoutHtlc(channelId, htlc, proof);

        vm.warp(uint256(timelock) + 1);
        vm.expectEmit(true, true, true, true);
        emit IChannelManager.HTLCTimeout(channelId, htlc.index);
        mgr.timeoutHtlc(channelId, htlc, proof);

        // Settle after the challenge window; the HTLC value went back to Alice.
        vm.warp(block.timestamp + CHALLENGE_PERIOD + 1);
        mgr.distributeFunds(channelId);
        assertEq(token.balanceOf(alice), 1_000e6 - FUND_A + balA + htlcAmt);
        assertEq(token.balanceOf(bob), 1_000e6 - FUND_B + balB);
    }

    function test_DistributeFunds_RevertsWithUnresolvedHtlc() public {
        bytes32 channelId = _openChannel();
        bytes32 preimage = keccak256("secret");
        uint256 htlcAmt = 50e6;
        (, bytes32 root) =
            _singleHtlc(htlcAmt, preimage, uint32(block.timestamp + 100), bob);
        uint256 balA = 550e6;
        uint256 balB = FUND_A + FUND_B - htlcAmt - balA;
        bytes memory bobSig =
            _sign(bobPk, _stateDigest(channelId, 5, balA, balB, root));
        vm.prank(alice);
        mgr.forceClose(channelId, 5, balA, balB, root, bobSig);

        vm.warp(block.timestamp + CHALLENGE_PERIOD + 1);
        vm.expectRevert(ChannelManager.HtlcsUnresolved.selector);
        mgr.distributeFunds(channelId);
    }
}
