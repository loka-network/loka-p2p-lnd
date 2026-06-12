#!/usr/bin/env bash
# itest_evm.sh — full-flow E2E test of the EVM (ChannelManager) backend.
#
# Spins up a local Anvil devnet, deploys MockERC20 + ChannelManager, boots two
# lnd --evm.active nodes, then drives and asserts the whole channel lifecycle:
#
#   1. wallet funding   — ERC20 balance visible through WalletBalance
#   2. channel open     — openChannel escrow pulled on-chain, channel active
#   3. payment          — lnevm… invoice paid over the channel (EIP-712
#                         commitments + HTLC settle)
#   3b. reverse channel — node2 opens a channel back so the path-finder has
#                         two distinct edges (self-payment prerequisite)
#   3c. self-payment    — node1 pays its own invoice node1→node2→node1 over
#                         the circular route, N rounds (mirrors the Sui
#                         itest's step 8; common L402/paywall pattern)
#   4. cooperative close— closeChannel call pays both participants; escrow
#                         drops by exactly that channel's deposit
#   5. force close      — forceClose call (challenge window) + distributeFunds
#                         after expiry
#
# After all checks pass the nodes stay up in suspended mode for manual
# poking (RPC/REST); press Enter to tear down. Set ITEST_EVM_SUSPEND=0 to
# exit immediately (CI), ITEST_EVM_SELF_PAY_ROUNDS=N to tune the loop.
#
# Requirements: go, anvil/forge/cast (Foundry), python3.
# Usage: ./scripts/itest_evm.sh
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
WORKDIR=$(mktemp -d /tmp/lnd-evm-itest.XXXXXX)

RPC_PORT=18545
RPC="http://127.0.0.1:${RPC_PORT}"
DEVKEY=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
# Short challenge window so the force-close → auto-distributeFunds path
# (driven by the in-node EVM settler, which gates on wall-clock vs the
# contract's challengeExpiry) completes quickly.
CHALLENGE_PERIOD=12
CHAIN_ID=31337

LND_BIN=${LND_BIN:-$WORKDIR/lnd}
LNCLI_BIN=${LNCLI_BIN:-$WORKDIR/lncli}

PASS_COUNT=0
step()   { printf '\n\033[1;34m=== %s\033[0m\n' "$*"; }
ok()     { printf '\033[1;32m  ✓ %s\033[0m\n' "$*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail()   { printf '\033[1;31m  ✗ %s\033[0m\n' "$*"; exit 1; }

cleanup() {
    local code=$?
    pkill -f "lnddir=$WORKDIR" 2>/dev/null || true
    [ -n "${ANVIL_PID:-}" ] && kill "$ANVIL_PID" 2>/dev/null || true
    if [ $code -eq 0 ]; then
        # Let the nodes finish their shutdown writes before removing the dir.
        for _ in $(seq 1 10); do
            pgrep -f "lnddir=$WORKDIR" >/dev/null 2>&1 || break
            sleep 0.5
        done
        rm -rf "$WORKDIR"
        printf '\n\033[1;32mEVM E2E: done — nodes terminated, workdir removed\033[0m\n'
    else
        printf '\n\033[1;31mEVM E2E: FAILED — logs kept in %s\033[0m\n' "$WORKDIR"
    fi
}
trap cleanup EXIT

# wait_until <timeout_s> <description> <command...> — polls until the command
# succeeds (exit 0) or the timeout elapses.
wait_until() {
    local timeout=$1 desc=$2; shift 2
    local t=0
    until "$@" >/dev/null 2>&1; do
        t=$((t+1))
        [ "$t" -ge "$timeout" ] && fail "timeout waiting for: $desc"
        sleep 1
    done
}

json() { python3 -c "import json,sys; $1" ; }

lncli_n() {
    local n=$1; shift
    "$LNCLI_BIN" --rpcserver=127.0.0.1:1180$n --lnddir="$WORKDIR/node$n" \
        --tlscertpath="$WORKDIR/node$n/tls.cert" \
        --macaroonpath="$WORKDIR/node$n/data/chain/evm/anvil/admin.macaroon" \
        "$@"
}

erc20_bal() { cast call "$TOKEN" "balanceOf(address)(uint256)" "$1" --rpc-url "$RPC" | awk '{print $1}'; }

log_count() { # log_count <event-sig>
    cast logs --from-block 0 --address "$CM" "$1" --rpc-url "$RPC" --json \
        | json 'print(len(json.load(sys.stdin)))'
}

# ---------------------------------------------------------------------------
# The invoicesrpc/routerrpc sub-servers are needed for the in-flight HTLC
# step (addholdinvoice / settleinvoice); chainrpc/signrpc/walletrpc round out
# the standard dev RPC set.
BUILD_TAGS="invoicesrpc routerrpc chainrpc signrpc walletrpc"
step "Build lnd / lncli"
( cd "$REPO" && GOWORK=off go build -tags="$BUILD_TAGS" -o "$LND_BIN" ./cmd/lnd \
             && GOWORK=off go build -tags="$BUILD_TAGS" -o "$LNCLI_BIN" ./cmd/lncli )
ok "binaries built"

step "Start Anvil + deploy MockERC20 / ChannelManager"
anvil --port "$RPC_PORT" --block-time 1 --silent >"$WORKDIR/anvil.log" 2>&1 &
ANVIL_PID=$!
wait_until 15 "anvil rpc" cast chain-id --rpc-url "$RPC"

DEPLOY_OUT=$(cd "$REPO/evm-contracts/channel-manager" && \
    PRIVATE_KEY=$DEVKEY forge script script/DeployMockToken.s.sol \
        --rpc-url "$RPC" --broadcast 2>/dev/null)
TOKEN=$(echo "$DEPLOY_OUT" | grep -o 'Deployed MockERC20.*0x[0-9a-fA-F]*' | grep -o '0x[0-9a-fA-F]*')
DEPLOY_OUT=$(cd "$REPO/evm-contracts/channel-manager" && \
    PRIVATE_KEY=$DEVKEY TOKEN_ADDRESS=$TOKEN CHALLENGE_PERIOD=$CHALLENGE_PERIOD \
    forge script script/Deploy.s.sol --rpc-url "$RPC" --broadcast 2>/dev/null)
CM=$(echo "$DEPLOY_OUT" | grep -o 'Deployed ChannelManager to: 0x[0-9a-fA-F]*' | grep -o '0x[0-9a-fA-F]*')
[ -n "$TOKEN" ] && [ -n "$CM" ] || fail "contract deployment"
ok "token=$TOKEN manager=$CM (challenge ${CHALLENGE_PERIOD}s)"

step "Boot two lnd --evm.active nodes"
# --allow-circular-route is required for the self-payment step: the HTLC
# leaves node1 over one channel and returns over the other; without the
# flag the htlcswitch rejects the return hop (same rationale as the Sui
# itest). --protocol.no-anchors because EVM channels have no on-chain
# commitment tx to CPFP, so anchor outputs are meaningless (and the
# sweeper would otherwise try to sweep one to an EVM account address).
# REST stays on (no TLS) so the suspended nodes are usable from
# Postman/curl after the run.
for N in 1 2; do
    "$LND_BIN" --lnddir="$WORKDIR/node$N" --noseedbackup \
        --evm.active --evm.chain=anvil --evm.chainid=$CHAIN_ID \
        --evm.rpchost="$RPC" \
        --evm.tokenaddress="$TOKEN" --evm.contractaddress="$CM" \
        --listen=127.0.0.1:1190$N --rpclisten=127.0.0.1:1180$N \
        --restlisten=127.0.0.1:1280$N --no-rest-tls \
        --allow-circular-route --protocol.no-anchors --debuglevel=info \
        >"$WORKDIR/node$N.log" 2>&1 &
done
for N in 1 2; do
    wait_until 30 "node$N rpc" lncli_n "$N" getinfo
done
ok "both nodes serving rpc"

# --------------------------------------------------------------------------
step "1. Wallet funding (mint 1000 USDC + gas to both nodes)"
ADDR1=$(lncli_n 1 newaddress p2wkh | grep -o '0x[0-9a-fA-F]*')
ADDR2=$(lncli_n 2 newaddress p2wkh | grep -o '0x[0-9a-fA-F]*')
for A in "$ADDR1" "$ADDR2"; do
    cast send "$TOKEN" "mint(address,uint256)" "$A" 1000000000 \
        --rpc-url "$RPC" --private-key "$DEVKEY" >/dev/null
    cast send "$A" --value 10ether \
        --rpc-url "$RPC" --private-key "$DEVKEY" >/dev/null
done

check_wallet_bal() {
    [ "$(lncli_n 1 walletbalance | json 'print(json.load(sys.stdin)["confirmed_balance"])')" = "100000000000" ]
}
wait_until 15 "node1 wallet balance" check_wallet_bal
ok "1000 USDC visible as 100000000000 internal units"

step "2. Channel open (100 USDC)"
PK2=$(lncli_n 2 getinfo | json 'print(json.load(sys.stdin)["identity_pubkey"])')
lncli_n 1 connect "$PK2@127.0.0.1:11902" >/dev/null 2>&1 || true
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" --local_amt=10000000000)
FUNDING_TXID=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
[ -n "$FUNDING_TXID" ] || fail "openchannel returned no funding txid"

chan_active() { # chan_active <node> <count>
    [ "$(lncli_n "$1" listchannels | json 'd=json.load(sys.stdin); print(sum(1 for c in d["channels"] if c["active"]))')" = "$2" ]
}
wait_until 60 "channel active on node1" chan_active 1 1
wait_until 60 "channel active on node2" chan_active 2 1
[ "$(erc20_bal "$CM")" = "100000000" ] || fail "escrow != 100 USDC raw"
ok "channel active on both peers; escrow holds 100000000 base-units"

# pay_with_retry <payer-node> <payreq> [extra payinvoice flags…] — the
# link's outbound bandwidth can lag the channel's "active" flag briefly,
# so retry a few times.
pay_with_retry() {
    local n=$1 payreq=$2 status="" i
    shift 2
    for i in 1 2 3 4 5; do
        status=$(lncli_n "$n" payinvoice --force --timeout 30s --json "$@" \
            "$payreq" 2>/dev/null \
            | json 'print(json.load(sys.stdin)["status"])' || echo RETRY)
        [ "$status" = "SUCCEEDED" ] && return 0
        sleep 3
    done
    fail "payment status after retries: $status"
}

escrow_is() { [ "$(erc20_bal "$CM")" = "$1" ]; }

step "3. Payment (5 USDC invoice)"
PAYREQ=$(lncli_n 2 addinvoice --amt 500000000 --memo e2e \
    | json 'print(json.load(sys.stdin)["payment_request"])')
case "$PAYREQ" in lnevm*) ;; *) fail "invoice prefix not lnevm…: $PAYREQ";; esac
pay_with_retry 1 "$PAYREQ"

bal2_is() {
    [ "$(lncli_n 2 listchannels | json 'print(json.load(sys.stdin)["channels"][0]["local_balance"])')" = "$1" ]
}
wait_until 15 "node2 settled balance" bal2_is 500000000
ok "payment SUCCEEDED, node2 local balance 500000000"

step "3b. Reverse channel (node2 → node1, 50 USDC)"
PK1=$(lncli_n 1 getinfo | json 'print(json.load(sys.stdin)["identity_pubkey"])')
CHAN_OPEN=$(lncli_n 2 openchannel --node_key="$PK1" --local_amt=5000000000)
FUNDING_TXID_REV=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
[ -n "$FUNDING_TXID_REV" ] || fail "reverse openchannel returned no funding txid"
wait_until 60 "2 channels active on node1" chan_active 1 2
wait_until 60 "2 channels active on node2" chan_active 2 2
wait_until 15 "escrow holds both deposits" escrow_is 150000000
ok "reverse channel active; escrow holds 150000000 base-units"

# Self-payment: node1 pays its own invoice, the HTLC routes
# node1 → node2 (channel 1) → node1 (reverse channel) and settles against
# node1's own invoice registry. This exercises HTLC forwarding plus both
# channels' EIP-712 commitment updates in one round trip — the everyday
# pattern when an app pays a paywall backed by the same node (L402).
SELF_PAY_ROUNDS=${ITEST_EVM_SELF_PAY_ROUNDS:-3}
step "3c. Self-payment (node1 → node1 via node2) × $SELF_PAY_ROUNDS"
for ROUND in $(seq 1 "$SELF_PAY_ROUNDS"); do
    SELF_PAYREQ=$(lncli_n 1 addinvoice --amt 100000000 --memo "self-r$ROUND" \
        | json 'print(json.load(sys.stdin)["payment_request"])')
    lncli_n 1 resetmc >/dev/null 2>&1 || true
    pay_with_retry 1 "$SELF_PAYREQ" --allow_self_payment
    chan_active 1 2 || fail "round $ROUND left <2 active channels on node1"
    echo "    ✓ round $ROUND settled"
done
# Circular payments are value-neutral, so both escrows must be untouched.
escrow_is 150000000 || fail "escrow changed after self-payments"
ok "$SELF_PAY_ROUNDS self-payment rounds settled; channels and escrow intact"

step "4. Cooperative close (channel 1)"
lncli_n 1 closechannel "$FUNDING_TXID" >/dev/null 2>&1 &
CLOSE_PID=$!

# Only channel 1's 100-USDC deposit leaves the escrow; the reverse channel
# stays open (and stays available in suspended mode).
wait_until 60 "channel-1 escrow paid out" escrow_is 50000000
[ "$(log_count 'ChannelClosed(bytes32,uint256,uint256)')" = "1" ] \
    || fail "no ChannelClosed event"
wait_until 60 "channel 1 gone from node1" chan_active 1 1
kill $CLOSE_PID 2>/dev/null || true
ok "closeChannel paid out channel 1's escrow; ChannelClosed emitted"

step "5. Force close (open third channel, pay, then --force)"
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" --local_amt=5000000000)
FUNDING_TXID2=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
wait_until 60 "third channel active on node1" chan_active 1 2
wait_until 60 "third channel active on node2" chan_active 2 2

PAYREQ=$(lncli_n 2 addinvoice --amt 700000000 --memo e2e-fc \
    | json 'print(json.load(sys.stdin)["payment_request"])')
pay_with_retry 1 "$PAYREQ"

lncli_n 1 closechannel --force "$FUNDING_TXID2" >/dev/null 2>&1 &
FORCE_PID=$!

unilateral_seen() {
    [ "$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)')" = "1" ]
}
wait_until 60 "forceClose on-chain" unilateral_seen
kill $FORCE_PID 2>/dev/null || true
ok "forceClose landed; channel in challenge window"

# No manual distributeFunds: node1's in-node EVM settler broadcasts it on
# its own once the challenge window elapses (no HTLCs are pending, the
# payment settled before the force close). We just wait for the on-chain
# effect. Only the reverse channel's deposit should remain escrowed.
wait_until 60 "settler auto-broadcasts distributeFunds" escrow_is 50000000
[ "$(log_count 'FundsDistributed(bytes32,uint256,uint256)')" = "1" ] \
    || fail "no FundsDistributed event"
ok "EVM settler auto-distributed the force-closed escrow"

step "6. In-flight HTLC force close (hold invoice → on-chain claimHtlc)"
# Open a fresh node1 → node2 channel, route a HELD payment so an HTLC is
# locked in-flight on both commitments, then force-close while it is
# pending. The settlers must resolve the HTLC on-chain: node2 reveals the
# preimage (settleinvoice) and its settler calls claimHtlc against the
# committed htlcsHash, after which distributeFunds can finalise.
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" --local_amt=5000000000)
FUNDING_TXID3=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
wait_until 60 "in-flight test channel active on node1" chan_active 1 2
wait_until 60 "in-flight test channel active on node2" chan_active 2 2

# Random preimage and its SHA-256 payment hash for the hold invoice.
PREIMAGE=$(python3 -c "import os; print(os.urandom(32).hex())")
PAYHASH=$(python3 -c "import hashlib,sys; print(hashlib.sha256(bytes.fromhex(sys.argv[1])).hexdigest())" "$PREIMAGE")
HOLD_PR=$(lncli_n 2 addholdinvoice "$PAYHASH" --amt 300000000 \
    | json 'print(json.load(sys.stdin)["payment_request"])')
[ -n "$HOLD_PR" ] || fail "addholdinvoice returned no payment_request"

# Pay in the background — a hold invoice never settles on its own, so this
# call blocks with the HTLC locked in-flight until we cancel/settle.
lncli_n 1 payinvoice --force --timeout 120s "$HOLD_PR" >/dev/null 2>&1 &
HOLD_PAY_PID=$!

htlc_pending_on_1() {
    [ "$(lncli_n 1 listchannels \
        | json 'd=json.load(sys.stdin); print(sum(len(c["pending_htlcs"]) for c in d["channels"]))')" -ge 1 ]
}
wait_until 60 "HTLC locked in-flight on node1" htlc_pending_on_1
ok "held HTLC is in-flight (channel has a pending HTLC)"

FC_BEFORE=$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)')
lncli_n 1 closechannel --force "$FUNDING_TXID3" >/dev/null 2>&1 &
FORCE_PID2=$!
fc_incremented() {
    [ "$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)')" -gt "$FC_BEFORE" ]
}
wait_until 60 "forceClose with pending HTLC on-chain" fc_incremented
kill $FORCE_PID2 2>/dev/null || true
ok "forceClose landed with an in-flight HTLC committed"

# Reveal the preimage at node2; its settler now claims the HTLC on-chain.
lncli_n 2 settleinvoice "$PREIMAGE" >/dev/null 2>&1 || true
kill $HOLD_PAY_PID 2>/dev/null || true
htlc_claimed() {
    [ "$(log_count 'HTLCClaimed(bytes32,uint256,bytes32)')" -ge 1 ]
}
wait_until 90 "settler broadcasts claimHtlc on-chain" htlc_claimed
ok "in-flight HTLC resolved on-chain via claimHtlc (Merkle proof verified)"

# With the only HTLC resolved, a settler finalises the channel.
fd_two() { [ "$(log_count 'FundsDistributed(bytes32,uint256,uint256)')" -ge 2 ]; }
wait_until 60 "distributeFunds after HTLC resolution" fd_two
ok "channel finalised after in-flight HTLC resolution"

# ---------------------------------------------------------------------------
# Suspended mode: keep both nodes (and Anvil) up for manual RPC/REST poking,
# mirroring the Sui itest. The reverse channel is still active. Press Enter
# (or close stdin / set ITEST_EVM_SUSPEND=0) to tear everything down.
printf '\n\033[1;32mEVM E2E: ALL %d CHECKS PASSED\033[0m\n' "$PASS_COUNT"
if [ "${ITEST_EVM_SUSPEND:-1}" = "1" ]; then
    echo "=================================================================================="
    echo "✅ Test workflow completed! Nodes are now in [Suspended Mode], waiting for"
    echo "   external RPC / REST requests. The node2→node1 channel is still active."
    echo ""
    echo " -> Anvil RPC:        $RPC  (token=$TOKEN manager=$CM)"
    echo " -> node1 gRPC:       127.0.0.1:11801   REST: http://127.0.0.1:12801"
    echo "    macaroon:         $WORKDIR/node1/data/chain/evm/anvil/admin.macaroon"
    echo " -> node2 gRPC:       127.0.0.1:11802   REST: http://127.0.0.1:12802"
    echo "    macaroon:         $WORKDIR/node2/data/chain/evm/anvil/admin.macaroon"
    echo ""
    echo "    lncli example:"
    echo "    lncli --rpcserver=127.0.0.1:11801 --lnddir=$WORKDIR/node1 \\"
    echo "          --macaroonpath=$WORKDIR/node1/data/chain/evm/anvil/admin.macaroon getinfo"
    echo ""
    echo "Once you are done testing, press [Enter] to terminate nodes and exit..."
    echo "=================================================================================="
    read -r _ || true
fi
