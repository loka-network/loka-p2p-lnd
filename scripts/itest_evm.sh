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
#   4. cooperative close— closeChannel call pays both participants, escrow 0
#   5. force close      — forceClose call (challenge window) + distributeFunds
#                         after expiry, escrow 0
#
# Requirements: go, anvil/forge/cast (Foundry), python3.
# Usage: ./scripts/itest_evm.sh
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
WORKDIR=$(mktemp -d /tmp/lnd-evm-itest.XXXXXX)

RPC_PORT=18545
RPC="http://127.0.0.1:${RPC_PORT}"
DEVKEY=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
CHALLENGE_PERIOD=30
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
        rm -rf "$WORKDIR"
        printf '\n\033[1;32mEVM E2E: ALL %d CHECKS PASSED\033[0m\n' "$PASS_COUNT"
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
step "Build lnd / lncli"
( cd "$REPO" && GOWORK=off go build -o "$LND_BIN" ./cmd/lnd \
             && GOWORK=off go build -o "$LNCLI_BIN" ./cmd/lncli )
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
for N in 1 2; do
    "$LND_BIN" --lnddir="$WORKDIR/node$N" --noseedbackup \
        --evm.active --evm.chain=anvil --evm.chainid=$CHAIN_ID \
        --evm.rpchost="$RPC" \
        --evm.tokenaddress="$TOKEN" --evm.contractaddress="$CM" \
        --listen=127.0.0.1:1190$N --rpclisten=127.0.0.1:1180$N \
        --norest --debuglevel=info \
        >"$WORKDIR/node$N.log" 2>&1 &
done
for N in 1 2; do
    wait_until 30 "node$N rpc" lncli_n "$N" getinfo
done
ok "both nodes serving rpc"

# --------------------------------------------------------------------------
step "1. Wallet funding (mint 1000 USDC to node1, gas to both nodes)"
ADDR1=$(lncli_n 1 newaddress p2wkh | grep -o '0x[0-9a-fA-F]*')
ADDR2=$(lncli_n 2 newaddress p2wkh | grep -o '0x[0-9a-fA-F]*')
cast send "$TOKEN" "mint(address,uint256)" "$ADDR1" 1000000000 \
    --rpc-url "$RPC" --private-key "$DEVKEY" >/dev/null
for A in "$ADDR1" "$ADDR2"; do
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

# pay_with_retry <payreq> — the link's outbound bandwidth can lag the
# channel's "active" flag briefly, so retry a few times.
pay_with_retry() {
    local payreq=$1 status="" i
    for i in 1 2 3 4 5; do
        status=$(lncli_n 1 payinvoice --force --timeout 30s --json \
            "$payreq" 2>/dev/null \
            | json 'print(json.load(sys.stdin)["status"])' || echo RETRY)
        [ "$status" = "SUCCEEDED" ] && return 0
        sleep 3
    done
    fail "payment status after retries: $status"
}

step "3. Payment (5 USDC invoice)"
PAYREQ=$(lncli_n 2 addinvoice --amt 500000000 --memo e2e \
    | json 'print(json.load(sys.stdin)["payment_request"])')
case "$PAYREQ" in lnevm*) ;; *) fail "invoice prefix not lnevm…: $PAYREQ";; esac
pay_with_retry "$PAYREQ"

bal2_is() {
    [ "$(lncli_n 2 listchannels | json 'print(json.load(sys.stdin)["channels"][0]["local_balance"])')" = "$1" ]
}
wait_until 15 "node2 settled balance" bal2_is 500000000
ok "payment SUCCEEDED, node2 local balance 500000000"

step "4. Cooperative close"
lncli_n 1 closechannel "$FUNDING_TXID" >/dev/null 2>&1 &
CLOSE_PID=$!

escrow_zero() { [ "$(erc20_bal "$CM")" = "0" ]; }
wait_until 60 "escrow paid out" escrow_zero
[ "$(log_count 'ChannelClosed(bytes32,uint256,uint256)')" = "1" ] \
    || fail "no ChannelClosed event"
wait_until 60 "channel gone from node1" chan_active 1 0
kill $CLOSE_PID 2>/dev/null || true
ok "closeChannel paid out the full escrow; ChannelClosed emitted"

step "5. Force close (open second channel, pay, then --force)"
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" --local_amt=5000000000)
FUNDING_TXID2=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
wait_until 60 "second channel active on node1" chan_active 1 1
wait_until 60 "second channel active on node2" chan_active 2 1

PAYREQ=$(lncli_n 2 addinvoice --amt 700000000 --memo e2e-fc \
    | json 'print(json.load(sys.stdin)["payment_request"])')
pay_with_retry "$PAYREQ"

lncli_n 1 closechannel --force "$FUNDING_TXID2" >/dev/null 2>&1 &
FORCE_PID=$!

unilateral_seen() {
    [ "$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)')" = "1" ]
}
wait_until 60 "forceClose on-chain" unilateral_seen
kill $FORCE_PID 2>/dev/null || true
ok "forceClose landed; channel in challenge window"

# Jump past the challenge window, then distribute. distributeFunds is
# permissionless once the window expired and no HTLCs are pending.
cast rpc evm_increaseTime $((CHALLENGE_PERIOD + 5)) --rpc-url "$RPC" >/dev/null
cast rpc evm_mine --rpc-url "$RPC" >/dev/null

CHANNEL_ID=$(cast logs --from-block 0 --address "$CM" \
    'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)' \
    --rpc-url "$RPC" --json | json 'print(json.load(sys.stdin)[0]["topics"][1])')
cast send "$CM" "distributeFunds(bytes32)" "$CHANNEL_ID" \
    --rpc-url "$RPC" --private-key "$DEVKEY" >/dev/null

wait_until 30 "escrow paid out after distributeFunds" escrow_zero
[ "$(log_count 'FundsDistributed(bytes32,uint256,uint256)')" = "1" ] \
    || fail "no FundsDistributed event"
ok "distributeFunds paid out the force-closed escrow"
