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
# Usage: ./scripts/itest_evm.sh [anvil|base-sepolia]
#
#   anvil (default)  local Anvil devnet; everything is created from scratch
#                    and torn down afterwards.
#   base-sepolia     Base's public testnet (chain id 84532). Requires
#                    PRIVATE_KEY env with Base-Sepolia ETH for gas (faucet:
#                    https://portal.cdp.coinbase.com/products/faucet). The
#                    contracts are deployed once and recorded in
#                    evm-contracts/channel-manager/deploy_state_base-sepolia
#                    .json; later runs reuse that file. EVM_RPC overrides the
#                    default public endpoint (https://sepolia.base.org).
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
WORKDIR=$(mktemp -d /tmp/lnd-evm-itest.XXXXXX)

NETWORK="${1:-anvil}"
case "$NETWORK" in
anvil)
    RPC_PORT=18545
    RPC="http://127.0.0.1:${RPC_PORT}"
    # Anvil's well-known dev account 0.
    DEVKEY=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
    # Short challenge window so the force-close → auto-distributeFunds
    # path (the in-node settler gates on wall-clock vs the contract's
    # challengeExpiry) completes quickly.
    CHALLENGE_PERIOD=12
    CHAIN_ID=31337
    # Generous-but-quick waits: 1s blocks.
    WAIT_CHAN=60 WAIT_SETTLE=90
    # Anvil has infinite money — fund each node lavishly.
    GAS_FUND=10ether
    NODE_FUND_USDC=1000
    ;;
base-sepolia)
    RPC="${EVM_RPC:-https://sepolia.base.org}"
    DEVKEY="${PRIVATE_KEY:?base-sepolia mode needs PRIVATE_KEY with funded Base-Sepolia ETH}"
    CHALLENGE_PERIOD=60
    CHAIN_ID=84532
    # 2s blocks + public-RPC latency: double the waits.
    WAIT_CHAN=180 WAIT_SETTLE=240
    # Real testnet ETH comes from a faucet (often a tiny 0.0001 ETH/claim),
    # so fund each node frugally. Base Sepolia gas is ~0.006 gwei and the
    # per-channel funding-account provisioning is ~0.00004 ETH; a node opens
    # a handful of channels across the suite, so 0.0005 ETH/node is ~3x
    # headroom. Raise via EVM_GAS_FUND if a run ever runs dry.
    GAS_FUND="${EVM_GAS_FUND:-0.0005ether}"
    # node1 opens channels totaling 200 USDC across the full suite (100+50+50)
    # and closed-channel funds don't return to its spendable wallet, so each
    # node needs ≥200; 250 gives margin. Tunable via EVM_NODE_FUND_USDC.
    NODE_FUND_USDC="${EVM_NODE_FUND_USDC:-250}"
    ;;
*)
    echo "Unknown network '$NETWORK'. Use 'anvil' or 'base-sepolia'." >&2
    exit 1
    ;;
esac
DEPLOY_STATE="$REPO/evm-contracts/channel-manager/deploy_state_${NETWORK}.json"

# The deployer/funder address — also where node gas is swept back on teardown.
DEPLOYER_ADDR=$(cast wallet address --private-key "$DEVKEY" 2>/dev/null || true)

LND_BIN=${LND_BIN:-$WORKDIR/lnd}
LNCLI_BIN=${LNCLI_BIN:-$WORKDIR/lncli}

PASS_COUNT=0
step()   { printf '\n\033[1;34m=== %s\033[0m\n' "$*"; }
ok()     { printf '\033[1;32m  ✓ %s\033[0m\n' "$*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail()   { printf '\033[1;31m  ✗ %s\033[0m\n' "$*"; exit 1; }

cleanup() {
    local code=$?

    # Reclaim leftover gas from the throwaway node wallets before killing them
    # (base-sepolia only — anvil has infinite money). On the EVM backend
    # `sendcoins --sweepall` sweeps the node account's native ETH, so each run
    # returns its unspent gas headroom to the funder instead of permanently
    # stranding it in a discarded wallet. Best-effort: nodes must still be up.
    if [ "$NETWORK" = "base-sepolia" ] && [ -n "${DEPLOYER_ADDR:-}" ]; then
        for n in 1 2; do
            if out=$(lncli_n "$n" sendcoins --sweepall --force \
                --addr "$DEPLOYER_ADDR" 2>&1); then
                echo "  swept node$n gas -> deployer"
            fi
        done
    fi

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
        --macaroonpath="$WORKDIR/node$n/data/chain/evm/$NETWORK/admin.macaroon" \
        "$@"
}

# rcall <cast-args…> — a read-only `cast` call that retries transient
# public-RPC errors (TLS handshake EOF, rate-limit drops). Prints the raw
# output on success.
rcall() {
    local i out
    for i in 1 2 3 4 5 6; do
        if out=$(cast "$@" --rpc-url "$RPC" 2>/dev/null) && [ -n "$out" ]; then
            printf '%s' "$out"
            return 0
        fi
        sleep 2
    done
    return 1
}

erc20_bal() { rcall call "$TOKEN" "balanceOf(address)(uint256)" "$1" | awk '{print $1}'; }

# dsend <cast-send-args…> — a deployer-key `cast send` that retries the
# transient nonce races a load-balanced public RPC produces when txs are fired
# back-to-back (its pending-nonce view lags across backends, yielding
# "nonce too low/high"). cast waits for the receipt, so a rejected send did
# not land and is safe to re-send with a freshly-fetched nonce.
dsend() {
    local i out
    for i in 1 2 3 4 5 6; do
        if out=$(cast send "$@" --rpc-url "$RPC" \
            --private-key "$DEVKEY" 2>&1); then
            return 0
        fi
        case "$out" in
        *"nonce too low"*|*"nonce too high"*|\
        *"replacement transaction underpriced"*|\
        *"tls handshake"*|*"client error (Connect)"*|\
        *"connection reset"*|*"connection refused"*|*"timed out"*)
            # nonce races + pre-send connection failures (a TLS-handshake
            # error means the signed tx never left, so re-sending is safe).
            sleep 4
            ;;
        *)
            echo "dsend failed: $out" >&2
            return 1
            ;;
        esac
    done
    echo "dsend gave up after retries: $out" >&2
    return 1
}

log_count() { # log_count <event-sig>
    # FROM_BLOCK comes from the deploy state: public RPCs reject unbounded
    # from-block-0 ranges, and pre-deployment blocks can't hold our events.
    rcall logs --from-block "${FROM_BLOCK:-0}" --address "$CM" "$1" --json \
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

if [ "$NETWORK" = "anvil" ]; then
    step "Start Anvil ($NETWORK)"
    anvil --port "$RPC_PORT" --block-time 1 --silent >"$WORKDIR/anvil.log" 2>&1 &
    ANVIL_PID=$!
    wait_until 15 "anvil rpc" cast chain-id --rpc-url "$RPC"
    # A fresh devnet never has a valid prior deployment.
    rm -f "$DEPLOY_STATE"
else
    step "Using public network $NETWORK ($RPC)"
    wait_until 15 "rpc reachable" cast chain-id --rpc-url "$RPC"
fi

step "Deploy (or reuse) MockERC20 / ChannelManager"
# deploy.sh records the deployment in deploy_state_<network>.json — the
# canonical lookup for contract addresses (the EVM analogue of
# sui-contracts/lightning/deploy_state_*.json). On public networks an
# existing state file is reused: CREATE2 with the same salt+initcode can't
# be redeployed anyway, and indexers expect the address to stay put.
# EVM_TOKEN lets a run escrow a PRE-EXISTING ERC20 (e.g. a USDC already
# deployed on a public testnet) instead of deploying a fresh MockERC20 — the
# token must be mintable by the deployer key (the funding step mints to the
# nodes). deploy.sh deploys only the ChannelManager when given a token.
if [ ! -s "$DEPLOY_STATE" ]; then
    PRIVATE_KEY=$DEVKEY CHALLENGE_PERIOD=$CHALLENGE_PERIOD \
        "$REPO/evm-contracts/channel-manager/deploy.sh" "$NETWORK" "$RPC" \
        ${EVM_TOKEN:+"$EVM_TOKEN"} \
        >/dev/null || fail "contract deployment"
fi
TOKEN=$(json 'print(json.load(sys.stdin)["token"])' <"$DEPLOY_STATE")
CM=$(json 'print(json.load(sys.stdin)["channel_manager"])' <"$DEPLOY_STATE")
CHALLENGE_PERIOD=$(json 'print(json.load(sys.stdin)["challenge_period"])' <"$DEPLOY_STATE")
FROM_BLOCK=$(json 'print(json.load(sys.stdin)["deploy_block"])' <"$DEPLOY_STATE")
[ -n "$TOKEN" ] && [ -n "$CM" ] || fail "bad deploy state file: $DEPLOY_STATE"

# Query the token's decimals so base-unit amounts (mint, on-chain escrow
# assertions) are computed for whatever ERC20 is escrowed — 6 for USDC/USDT,
# 18 for DAI/WETH-style tokens. Channel amounts and balances elsewhere are in
# LND-internal units (1e8/token) and are decimal-independent.
TOKEN_DEC=$(rcall call "$TOKEN" "decimals()(uint8)" | awk '{print $1}')
[ -n "$TOKEN_DEC" ] || fail "unable to read token decimals"

# usdc_base <whole-token-amount> -> that amount in raw base-units
# (amount * 10^TOKEN_DEC), big-int safe.
usdc_base() {
    python3 -c "import sys; print(int(sys.argv[1]) * 10**$TOKEN_DEC)" "$1"
}

# Escrow baseline: the ChannelManager may be REUSED across runs (public-net
# deploy_state is persistent), so its token balance can already hold deposits
# from earlier runs' channels. Assert escrow CHANGES relative to this baseline
# rather than against an absolute value. On a fresh deploy this is 0.
ESCROW0=$(erc20_bal "$CM")
[ -n "$ESCROW0" ] || fail "unable to read baseline escrow balance"

# escrow_is <whole-usdc-delta> — true when the manager's escrow equals the
# baseline plus `delta` whole tokens.
escrow_is() {
    [ "$(erc20_bal "$CM")" = \
        "$(python3 -c "print($ESCROW0 + $(usdc_base "$1"))")" ]
}

ok "token=$TOKEN (${TOKEN_DEC} dec) manager=$CM (escrow baseline $ESCROW0, challenge ${CHALLENGE_PERIOD}s, state: ${DEPLOY_STATE#$REPO/})"

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
        --evm.active --evm.chain="$NETWORK" --evm.chainid=$CHAIN_ID \
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
step "1. Wallet funding (1000 USDC + gas to both nodes)"
ADDR1=$(lncli_n 1 newaddress p2wkh | grep -o '0x[0-9a-fA-F]*')
ADDR2=$(lncli_n 2 newaddress p2wkh | grep -o '0x[0-9a-fA-F]*')
NODE_FUND=$(usdc_base "$NODE_FUND_USDC")

# A freshly-deployed MockERC20 (no EVM_TOKEN) is owner-mintable by our
# deployer, so mint straight to the nodes. A pre-existing EVM_TOKEN may
# restrict mint to its owner (e.g. OpenZeppelin Ownable), so instead
# TRANSFER from the deployer, which must already hold the token (send it
# there beforehand: the deployer address is printed at startup).
if [ -n "${EVM_TOKEN:-}" ]; then
    DEPLOYER_ADDR=$(cast wallet address --private-key "$DEVKEY")
    have=$(erc20_bal "$DEPLOYER_ADDR")
    need=$(python3 -c "print(2 * $NODE_FUND)")
    if python3 -c "import sys; sys.exit(0 if int('$have') >= int('$need') else 1)"; then
        FUND_METHOD=transfer
    else
        fail "deployer $DEPLOYER_ADDR holds $have of $TOKEN, needs $need; \
mint is owner-only on this token — send it some USDC first"
    fi
else
    FUND_METHOD=mint
fi

for A in "$ADDR1" "$ADDR2"; do
    if [ "$FUND_METHOD" = "transfer" ]; then
        dsend "$TOKEN" "transfer(address,uint256)" "$A" "$NODE_FUND" >/dev/null
    else
        dsend "$TOKEN" "mint(address,uint256)" "$A" "$NODE_FUND" >/dev/null
    fi
    dsend "$A" --value "$GAS_FUND" >/dev/null
done

# node1's balance, in LND-internal units (1e8/token), must equal what we
# funded — independent of the token's on-chain decimals.
WANT_INTERNAL=$(python3 -c "print($NODE_FUND_USDC * 100000000)")
check_wallet_bal() {
    [ "$(lncli_n 1 walletbalance | json 'print(json.load(sys.stdin)["confirmed_balance"])')" = "$WANT_INTERNAL" ]
}
wait_until 15 "node1 wallet balance" check_wallet_bal
ok "$NODE_FUND_USDC USDC visible as $WANT_INTERNAL internal units"

step "2. Channel open (100 USDC)"
PK2=$(lncli_n 2 getinfo | json 'print(json.load(sys.stdin)["identity_pubkey"])')
lncli_n 1 connect "$PK2@127.0.0.1:11902" >/dev/null 2>&1 || true
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" --local_amt=10000000000)
FUNDING_TXID=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
[ -n "$FUNDING_TXID" ] || fail "openchannel returned no funding txid"

chan_active() { # chan_active <node> <count>
    [ "$(lncli_n "$1" listchannels | json 'd=json.load(sys.stdin); print(sum(1 for c in d["channels"] if c["active"]))')" = "$2" ]
}
wait_until $WAIT_CHAN "channel active on node1" chan_active 1 1
wait_until $WAIT_CHAN "channel active on node2" chan_active 2 1
escrow_is 100 || fail "escrow delta != 100 USDC"
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

# htlc_pending_on_1 is true once node1 has at least one in-flight HTLC across
# its channels.
htlc_pending_on_1() {
    [ "$(lncli_n 1 listchannels \
        | json 'd=json.load(sys.stdin); print(sum(len(c["pending_htlcs"]) for c in d["channels"]))')" -ge 1 ]
}

# pay_hold_until_inflight <invoice> <pid-var-name> — launch a (blocking)
# hold-invoice payment from node1 in the background and wait until its HTLC is
# locked in-flight. A freshly-opened channel's link bandwidth can lag its
# "active" flag, so the first payinvoice may fail with insufficient_balance
# before any HTLC is created; retry launching until one sticks. The surviving
# background PID is stored in the named variable (so it can be killed later).
pay_hold_until_inflight() {
    local invoice=$1 pidvar=$2 i t pid
    for i in 1 2 3 4 5 6; do
        lncli_n 1 payinvoice --force --timeout 120s "$invoice" \
            >/dev/null 2>&1 &
        pid=$!
        t=0
        while [ "$t" -lt 12 ]; do
            if htlc_pending_on_1; then
                printf -v "$pidvar" '%s' "$pid"
                return 0
            fi
            # If the payment process already exited, it failed without
            # locking an HTLC — break out and relaunch.
            kill -0 "$pid" 2>/dev/null || break
            t=$((t + 1))
            sleep 1
        done
        kill "$pid" 2>/dev/null || true
        sleep 2
    done
    fail "hold-invoice HTLC never went in-flight after retries"
}

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
wait_until $WAIT_CHAN "2 channels active on node1" chan_active 1 2
wait_until $WAIT_CHAN "2 channels active on node2" chan_active 2 2
wait_until 15 "escrow holds both deposits" escrow_is 150
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
escrow_is 150 || fail "escrow changed after self-payments"
ok "$SELF_PAY_ROUNDS self-payment rounds settled; channels and escrow intact"

step "4. Cooperative close (channel 1)"
lncli_n 1 closechannel "$FUNDING_TXID" >/dev/null 2>&1 &
CLOSE_PID=$!

# Only channel 1's 100-USDC deposit leaves the escrow; the reverse channel
# stays open (and stays available in suspended mode).
wait_until $WAIT_SETTLE "channel-1 escrow paid out" escrow_is 50
[ "$(log_count 'ChannelClosed(bytes32,uint256,uint256)')" = "1" ] \
    || fail "no ChannelClosed event"
wait_until $WAIT_SETTLE "channel 1 gone from node1" chan_active 1 1
kill $CLOSE_PID 2>/dev/null || true
ok "closeChannel paid out channel 1's escrow; ChannelClosed emitted"

step "5. Force close (open third channel, pay, then --force)"
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" --local_amt=5000000000)
FUNDING_TXID2=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
wait_until $WAIT_CHAN "third channel active on node1" chan_active 1 2
wait_until $WAIT_CHAN "third channel active on node2" chan_active 2 2

PAYREQ=$(lncli_n 2 addinvoice --amt 700000000 --memo e2e-fc \
    | json 'print(json.load(sys.stdin)["payment_request"])')
pay_with_retry 1 "$PAYREQ"

lncli_n 1 closechannel --force "$FUNDING_TXID2" >/dev/null 2>&1 &
FORCE_PID=$!

unilateral_seen() {
    [ "$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)')" = "1" ]
}
wait_until $WAIT_SETTLE "forceClose on-chain" unilateral_seen
kill $FORCE_PID 2>/dev/null || true
ok "forceClose landed; channel in challenge window"

# No manual distributeFunds: node1's in-node EVM settler broadcasts it on
# its own once the challenge window elapses (no HTLCs are pending, the
# payment settled before the force close). We just wait for the on-chain
# effect. Only the reverse channel's deposit should remain escrowed.
wait_until $WAIT_SETTLE "settler auto-broadcasts distributeFunds" escrow_is 50
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
wait_until $WAIT_CHAN "in-flight test channel active on node1" chan_active 1 2
wait_until $WAIT_CHAN "in-flight test channel active on node2" chan_active 2 2

# Random preimage and its SHA-256 payment hash for the hold invoice.
PREIMAGE=$(python3 -c "import os; print(os.urandom(32).hex())")
PAYHASH=$(python3 -c "import hashlib,sys; print(hashlib.sha256(bytes.fromhex(sys.argv[1])).hexdigest())" "$PREIMAGE")
HOLD_PR=$(lncli_n 2 addholdinvoice "$PAYHASH" --amt 300000000 \
    | json 'print(json.load(sys.stdin)["payment_request"])')
[ -n "$HOLD_PR" ] || fail "addholdinvoice returned no payment_request"

# Pay in the background — a hold invoice never settles on its own, so this
# call blocks with the HTLC locked in-flight until we cancel/settle. Retry
# the launch until the HTLC actually sticks (fresh-channel link lag).
pay_hold_until_inflight "$HOLD_PR" HOLD_PAY_PID
ok "held HTLC is in-flight (channel has a pending HTLC)"

FC_BEFORE=$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)')
lncli_n 1 closechannel --force "$FUNDING_TXID3" >/dev/null 2>&1 &
FORCE_PID2=$!
fc_incremented() {
    [ "$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)')" -gt "$FC_BEFORE" ]
}
wait_until $WAIT_SETTLE "forceClose with pending HTLC on-chain" fc_incremented
kill $FORCE_PID2 2>/dev/null || true
ok "forceClose landed with an in-flight HTLC committed"

# Reveal the preimage at node2; its settler now claims the HTLC on-chain.
lncli_n 2 settleinvoice "$PREIMAGE" >/dev/null 2>&1 || true
kill $HOLD_PAY_PID 2>/dev/null || true
htlc_claimed() {
    [ "$(log_count 'HTLCClaimed(bytes32,uint256,bytes32)')" -ge 1 ]
}
wait_until $WAIT_SETTLE "settler broadcasts claimHtlc on-chain" htlc_claimed
ok "in-flight HTLC resolved on-chain via claimHtlc (Merkle proof verified)"

# With the only HTLC resolved, a settler finalises the channel.
fd_two() { [ "$(log_count 'FundsDistributed(bytes32,uint256,uint256)')" -ge 2 ]; }
wait_until $WAIT_SETTLE "distributeFunds after HTLC resolution" fd_two
ok "channel finalised after in-flight HTLC resolution"

# Step 7 fast-forwards the chain with anvil_mine to cross the HTLC's CLTV
# deadline quickly, so it only runs on the local Anvil devnet.
if [ "$NETWORK" = "anvil" ]; then
step "7. In-flight HTLC force close → on-chain timeoutHtlc (no preimage)"
# Same setup as step 6 but the hold invoice is NEVER settled: node2 never
# learns the preimage, so node1's settler must reclaim its outgoing HTLC via
# timeoutHtlc once the CLTV deadline passes. This exercises the block-height
# → block.timestamp timelock conversion: the contract gates timeoutHtlc on
# block.timestamp >= htlc.timelock, and an unconverted height would let the
# timeout fire immediately (or never), so getting an actual on-chain
# HTLCTimeout proves the deadline is a real future timestamp.
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" --local_amt=5000000000)
FUNDING_TXID4=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
wait_until $WAIT_CHAN "timeout test channel active on node1" chan_active 1 2
wait_until $WAIT_CHAN "timeout test channel active on node2" chan_active 2 2

TO_PRE=$(python3 -c "import os; print(os.urandom(32).hex())")
TO_HASH=$(python3 -c "import hashlib,sys; print(hashlib.sha256(bytes.fromhex(sys.argv[1])).hexdigest())" "$TO_PRE")
TO_PR=$(lncli_n 2 addholdinvoice "$TO_HASH" --amt 300000000 \
    | json 'print(json.load(sys.stdin)["payment_request"])')
[ -n "$TO_PR" ] || fail "addholdinvoice returned no payment_request"

pay_hold_until_inflight "$TO_PR" TO_PAY_PID
ok "held HTLC is in-flight (timeout test)"

# Capture the HTLC's CLTV expiry height so we know how far to fast-forward.
TO_CLTV=$(lncli_n 1 listchannels \
    | json 'd=json.load(sys.stdin); print(max((h["expiration_height"] for c in d["channels"] for h in c["pending_htlcs"]), default=0))')
echo "    HTLC expiry height: $TO_CLTV"

FC_BEFORE=$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)')
lncli_n 1 closechannel --force "$FUNDING_TXID4" >/dev/null 2>&1 &
TO_FORCE_PID=$!
wait_until $WAIT_SETTLE "forceClose with pending HTLC (timeout test)" fc_incremented
kill $TO_FORCE_PID 2>/dev/null || true
ok "forceClose landed with the to-be-timed-out HTLC committed"

# DO NOT settle the invoice. Fast-forward the chain past the CLTV expiry (and
# the challenge window) at 1s/block so block.timestamp tracks height — the
# same 1s cadence EvmBlockTimeSecs(31337) assumes — then the settler's
# timeoutHtlc clears the contract's block.timestamp >= timelock check.
NOW_H=$(cast block-number --rpc-url "$RPC")
ADVANCE=$(( TO_CLTV - NOW_H + CHALLENGE_PERIOD + 20 ))
[ "$ADVANCE" -lt 30 ] && ADVANCE=30
echo "    mining $ADVANCE blocks (1s apart) to cross CLTV $TO_CLTV + challenge window"
cast rpc anvil_mine "$(printf '0x%x' "$ADVANCE")" 0x1 --rpc-url "$RPC" >/dev/null 2>&1

kill $TO_PAY_PID 2>/dev/null || true
TO_BEFORE=$(log_count 'HTLCTimeout(bytes32,uint256)')
htlc_timed_out() {
    [ "$(log_count 'HTLCTimeout(bytes32,uint256)')" -gt "$TO_BEFORE" ]
}
wait_until $WAIT_SETTLE "settler broadcasts timeoutHtlc on-chain" htlc_timed_out
ok "in-flight HTLC reclaimed on-chain via timeoutHtlc (timelock deadline honored)"

fd_three() { [ "$(log_count 'FundsDistributed(bytes32,uint256,uint256)')" -ge 3 ]; }
wait_until $WAIT_SETTLE "distributeFunds after HTLC timeout" fd_three
ok "channel finalised after in-flight HTLC timeout"
fi

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
    echo " -> $NETWORK RPC:     $RPC  (token=$TOKEN manager=$CM)"
    echo " -> node1 gRPC:       127.0.0.1:11801   REST: http://127.0.0.1:12801"
    echo "    macaroon:         $WORKDIR/node1/data/chain/evm/$NETWORK/admin.macaroon"
    echo " -> node2 gRPC:       127.0.0.1:11802   REST: http://127.0.0.1:12802"
    echo "    macaroon:         $WORKDIR/node2/data/chain/evm/$NETWORK/admin.macaroon"
    echo ""
    echo "    lncli example:"
    echo "    lncli --rpcserver=127.0.0.1:11801 --lnddir=$WORKDIR/node1 \\"
    echo "          --macaroonpath=$WORKDIR/node1/data/chain/evm/$NETWORK/admin.macaroon getinfo"
    echo ""
    echo "=================================================================================="
    if [ -t 0 ]; then
        echo "Once you are done testing, press [Enter] to terminate nodes and exit..."
        read -r _ || true
    else
        # Non-interactive (e.g. launched in the background): there's no
        # terminal to read from, so block indefinitely instead — exiting
        # here would trip the cleanup trap and kill the nodes. Stop the
        # process (or `pkill -f lnddir=$WORKDIR`) to tear down.
        echo "Non-interactive run: nodes will stay up until this process is"
        echo "killed (pkill -f \"lnddir=$WORKDIR\")."
        while true; do sleep 3600; done
    fi
fi
