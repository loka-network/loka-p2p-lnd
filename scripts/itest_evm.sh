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
#   3d. push_amt open   — single-funded open that credits the counterparty an
#                         initial off-chain balance; escrow still holds the
#                         full deposit (conservation), then coop-closed
#   4. cooperative close— closeChannel call pays both participants; escrow
#                         drops by exactly that channel's deposit
#   5. force close      — forceClose call (challenge window) + distributeFunds
#                         after expiry
#   8. crash recovery   — force close, kill node1 mid-window, restart; the
#                         settler must resume and distributeFunds (anvil only)
#
# The ChannelManager is deployed with a deposit-scaled challenge window
# (floor→cap); a dedicated step asserts challengeWindowFor() on-chain.
#
# After all checks pass the nodes stay up in suspended mode for manual
# poking (RPC/REST); press Enter to tear down. Set ITEST_EVM_SUSPEND=0 to
# exit immediately (CI), ITEST_EVM_SELF_PAY_ROUNDS=N to tune the loop.
#
# Requirements: go, anvil/forge/cast (Foundry), python3.
# Usage: ./scripts/itest_evm.sh [anvil|localnet|base-sepolia]
#
#   anvil (default)  local Anvil devnet; everything is created from scratch
#                    and torn down afterwards. `localnet` is an alias.
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
# `localnet` is an alias for `anvil` — the local EVM devnet IS anvil; the alias
# matches the Sui itest's naming (./scripts/itest_sui.sh localnet).
[ "$NETWORK" = "localnet" ] && NETWORK=anvil
# Asset label for the (chain, asset) data dir: data/chain/evm/<network>/<asset>/.
# Passed as --evm.tokensymbol so the dir segment is deterministic ("mock").
ASSET="mock"
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
    echo "Unknown network '$NETWORK'. Use 'anvil' (alias 'localnet') or 'base-sepolia'." >&2
    exit 1
    ;;
esac
DEPLOY_STATE="$REPO/evm-contracts/channel-manager/deploy_state_${NETWORK}.json"

# Deposit-scaled challenge window (same for all networks): the per-channel
# force-close window scales linearly from floor=CHALLENGE_PERIOD to
# cap=CHALLENGE_PERIOD+SCALE_SPAN, reaching the cap at FULL_SCALE_DEPOSIT raw
# token base-units. FULL_SCALE is set far above any itest channel (1M USDC @ 6
# dec) so every channel here gets a window ≈ floor and the suite's timing is
# unchanged; a dedicated step asserts the curve at 0 / mid / full / above.
SCALE_SPAN=1000
FULL_SCALE_DEPOSIT=1000000000000
MAX_CHALLENGE_PERIOD=$((CHALLENGE_PERIOD + SCALE_SPAN))

# The deployer/funder address — also where node gas is swept back on teardown.
DEPLOYER_ADDR=$(cast wallet address --private-key "$DEVKEY" 2>/dev/null || true)

# eth_getLogs window size for log_count. sepolia.base.org caps a single query
# at 2000 blocks; 1900 leaves margin. Tunable for RPCs with other limits.
LOG_RANGE="${EVM_LOG_RANGE:-1900}"

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
    # Track wall-clock, not iteration count: a predicate that calls rcall can
    # itself burn 12s on retries against a flaky public RPC, so counting "one
    # second per loop" would stretch a 240s budget into ~50 minutes. SECONDS
    # is a bash builtin counting real seconds since the shell started.
    local deadline=$((SECONDS + timeout))
    until "$@" >/dev/null 2>&1; do
        [ "$SECONDS" -ge "$deadline" ] && fail "timeout waiting for: $desc"
        sleep 1
    done
}

json() { python3 -c "import json,sys; $1" ; }

lncli_n() {
    local n=$1; shift
    "$LNCLI_BIN" --rpcserver=127.0.0.1:1180$n --lnddir="$WORKDIR/node$n" \
        --tlscertpath="$WORKDIR/node$n/tls.cert" \
        --macaroonpath="$WORKDIR/node$n/data/chain/evm/$NETWORK/$ASSET/admin.macaroon" \
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

log_count() { # log_count <event-sig> — prints the event count, or fails
    # (return 1, no stdout) on a transient RPC error.
    #
    # The from-block is a recent sliding window, NOT deploy_block: public RPCs
    # cap eth_getLogs spans (sepolia.base.org rejects anything over 2000 blocks
    # with "query exceeds max block range"), and from deploy_block the gap is
    # tens of thousands of blocks. Every event this suite asserts on was just
    # emitted, so a recent window finds it — and the assertions are all
    # baseline-relative (log_gt), comparing counts seconds apart within the
    # same window, so the slide is immaterial. Clamp to FROM_BLOCK (deploy) for
    # short-lived chains (anvil) whose tip is below the window size.
    local out latest from
    latest=$(rcall block-number) || return 1
    from=$((latest - LOG_RANGE))
    [ "$from" -lt "${FROM_BLOCK:-0}" ] && from=${FROM_BLOCK:-0}
    out=$(rcall logs --from-block "$from" --address "$CM" "$1" --json) \
        || return 1
    # rcall already retries until non-empty, but guard anyway so a flaky
    # public RPC never feeds empty input to json.load (which would crash
    # with a traceback instead of cleanly failing the poll).
    [ -n "$out" ] || return 1
    printf '%s' "$out" | json 'print(len(json.load(sys.stdin)))' 2>/dev/null \
        || return 1
}

# log_is <event-sig> <count> — predicate form for wait_until; tolerates the
# transient RPC failures log_count surfaces (a failed query just retries).
log_is() { [ "$(log_count "$1")" = "$2" ]; }

# log_gt <event-sig> <baseline> — true once the event count strictly exceeds
# <baseline>. This is the reuse-safe form: the ChannelManager is shared across
# runs, so its event log accumulates; assertions must check that THIS run's
# action added an event (count > baseline captured just before it), never an
# absolute total. Tolerates the transient empty result log_count can surface.
log_gt() {
    local n
    n=$(log_count "$1") || return 1
    [ -n "$n" ] && [ "$n" -gt "$2" ]
}

# ---------------------------------------------------------------------------
# The invoicesrpc/routerrpc sub-servers are needed for the in-flight HTLC
# step (addholdinvoice / settleinvoice); chainrpc/signrpc/walletrpc round out
# the standard dev RPC set.
BUILD_TAGS="invoicesrpc routerrpc chainrpc signrpc walletrpc"
step "Build lnd / lncli"
# Pin GOTOOLCHAIN=auto so the build uses the version go.mod requires (1.25.5)
# regardless of the caller's active Go: lnd's deps pull crypto/sha3 from the
# standard library, which only exists in Go ≥1.24, so building with an older
# gvm-selected toolchain (e.g. 1.23.x) fails with "crypto/sha3 is not in std".
( cd "$REPO" && GOWORK=off GOTOOLCHAIN=auto \
        go build -tags="$BUILD_TAGS" -o "$LND_BIN" ./cmd/lnd \
    && GOWORK=off GOTOOLCHAIN=auto \
        go build -tags="$BUILD_TAGS" -o "$LNCLI_BIN" ./cmd/lncli )
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
# Redeploy when there is no state, OR the recorded ChannelManager predates
# deposit-scaling (no/zero full_scale_deposit) — the scaling test needs the
# 4-arg contract (challengeWindowFor + the cap/full-scale getters). The new
# constructor changes the CREATE2 address, so this records a fresh contract.
RECORDED_FS=0
if [ -s "$DEPLOY_STATE" ]; then
    RECORDED_FS=$(json 'print(json.load(sys.stdin).get("full_scale_deposit",0))' <"$DEPLOY_STATE")
fi
if [ ! -s "$DEPLOY_STATE" ] || [ "$RECORDED_FS" = "0" ] || [ -z "$RECORDED_FS" ]; then
    PRIVATE_KEY=$DEVKEY CHALLENGE_PERIOD=$CHALLENGE_PERIOD \
        MAX_CHALLENGE_PERIOD=$MAX_CHALLENGE_PERIOD \
        FULL_SCALE_DEPOSIT=$FULL_SCALE_DEPOSIT \
        "$REPO/evm-contracts/channel-manager/deploy.sh" "$NETWORK" "$RPC" \
        ${EVM_TOKEN:+"$EVM_TOKEN"} \
        >/dev/null || fail "contract deployment"
fi
TOKEN=$(json 'print(json.load(sys.stdin)["token"])' <"$DEPLOY_STATE")
CM=$(json 'print(json.load(sys.stdin)["channel_manager"])' <"$DEPLOY_STATE")
CHALLENGE_PERIOD=$(json 'print(json.load(sys.stdin)["challenge_period"])' <"$DEPLOY_STATE")
MAX_CHALLENGE_PERIOD=$(json 'print(json.load(sys.stdin)["max_challenge_period"])' <"$DEPLOY_STATE")
FULL_SCALE_DEPOSIT=$(json 'print(json.load(sys.stdin)["full_scale_deposit"])' <"$DEPLOY_STATE")
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

ok "token=$TOKEN (${TOKEN_DEC} dec) manager=$CM (escrow baseline $ESCROW0, challenge floor ${CHALLENGE_PERIOD}s / cap ${MAX_CHALLENGE_PERIOD}s @ ${FULL_SCALE_DEPOSIT}, state: ${DEPLOY_STATE#$REPO/})"

step "Deposit-scaled challenge window (challengeWindowFor view)"
# Assert the on-chain curve directly: floor at 0, cap at full-scale, the exact
# linear midpoint, and a clamp above full-scale. Pure view calls — no waiting.
cwf() { rcall call "$CM" "challengeWindowFor(uint256)(uint256)" "$1" | awk '{print $1}'; }
MID_DEP=$(python3 -c "print($FULL_SCALE_DEPOSIT // 2)")
MID_WIN=$(python3 -c "print($CHALLENGE_PERIOD + ($MAX_CHALLENGE_PERIOD - $CHALLENGE_PERIOD) * ($FULL_SCALE_DEPOSIT // 2) // $FULL_SCALE_DEPOSIT)")
ABOVE_DEP=$(python3 -c "print($FULL_SCALE_DEPOSIT * 2)")
[ "$(cwf 0)" = "$CHALLENGE_PERIOD" ] \
    || fail "challengeWindowFor(0)=$(cwf 0) != floor $CHALLENGE_PERIOD"
[ "$(cwf "$FULL_SCALE_DEPOSIT")" = "$MAX_CHALLENGE_PERIOD" ] \
    || fail "challengeWindowFor(fullScale) != cap $MAX_CHALLENGE_PERIOD"
[ "$(cwf "$MID_DEP")" = "$MID_WIN" ] \
    || fail "challengeWindowFor(mid)=$(cwf "$MID_DEP") != expected $MID_WIN"
[ "$(cwf "$ABOVE_DEP")" = "$MAX_CHALLENGE_PERIOD" ] \
    || fail "challengeWindowFor(>fullScale) != cap (clamp)"
ok "challengeWindowFor: ${CHALLENGE_PERIOD}s @0, ${MID_WIN}s @half, ${MAX_CHALLENGE_PERIOD}s @full + clamp"

step "Boot two lnd --evm.active nodes"
# --allow-circular-route is required for the self-payment step: the HTLC
# leaves node1 over one channel and returns over the other; without the
# flag the htlcswitch rejects the return hop (same rationale as the Sui
# itest). --protocol.no-anchors because EVM channels have no on-chain
# commitment tx to CPFP, so anchor outputs are meaningless (and the
# sweeper would otherwise try to sweep one to an EVM account address).
# REST stays on (no TLS) so the suspended nodes are usable from
# Postman/curl after the run.
# boot_node <N> launches lnd node N (idempotent: also used to restart a node on
# the same lnddir, e.g. the crash-recovery step). --noseedbackup auto-creates
# and, on a subsequent boot over an existing wallet, auto-unlocks it.
boot_node() {
    local N=$1
    "$LND_BIN" --lnddir="$WORKDIR/node$N" --noseedbackup \
        --evm.active --evm.chain="$NETWORK" --evm.chainid=$CHAIN_ID \
        --evm.rpchost="$RPC" \
        --evm.tokenaddress="$TOKEN" --evm.contractaddress="$CM" \
        --evm.tokensymbol="$ASSET" \
        --listen=127.0.0.1:1190$N --rpclisten=127.0.0.1:1180$N \
        --restlisten=127.0.0.1:1280$N --no-rest-tls \
        --allow-circular-route --protocol.no-anchors --debuglevel=info \
        >>"$WORKDIR/node$N.log" 2>&1 &
}
for N in 1 2; do
    boot_node "$N"
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

step "3d. Channel open with push_amt (off-chain initial balance to peer)"
# push_amt credits the counterparty an initial balance from a SINGLE-funded
# open: the escrow still holds only node1's full deposit, but the opening
# commitment splits it node1 / node2. Proves the EVM commitment bridge carries
# a non-zero counterparty balance from block one and conservation still holds.
PUSH_LOCAL=1000000000 # 10 USDC channel (LND-internal 1e8/token units)
PUSH_AMT=300000000    #  3 USDC pushed to node2
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" \
    --local_amt=$PUSH_LOCAL --push_amt=$PUSH_AMT)
PUSH_TXID=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
[ -n "$PUSH_TXID" ] || fail "push openchannel returned no funding txid"
wait_until $WAIT_CHAN "push channel active on node1" chan_active 1 3
wait_until $WAIT_CHAN "push channel active on node2" chan_active 2 3
# Escrow rose by the FULL 10-USDC deposit — push is off-chain only.
wait_until 15 "escrow holds the full push deposit" escrow_is 160

# node2's local balance on the push channel must equal the pushed amount.
push_local() { # push_local <node> -> local_balance on the push channel
    lncli_n "$1" listchannels | json 'import sys; d=json.load(sys.stdin); ch=[c for c in d["channels"] if c["channel_point"].startswith("'"$PUSH_TXID"':")]; print(ch[0]["local_balance"] if ch else "none")'
}
push_bal2_is() { [ "$(push_local 2)" = "$1" ]; }
wait_until 15 "node2 credited the pushed balance" push_bal2_is "$PUSH_AMT"
ok "push_amt: node2 holds $PUSH_AMT at open while escrow held the full $PUSH_LOCAL"

# Coop-close the push channel so escrow returns to its pre-push value before
# the force-close steps (which assert exact escrow deltas).
lncli_n 1 closechannel "$PUSH_TXID" >/dev/null 2>&1 &
wait_until $WAIT_SETTLE "push-channel escrow released" escrow_is 150
wait_until $WAIT_CHAN "back to 2 active channels on node1" chan_active 1 2
ok "push channel coop-closed; escrow back to baseline+150"

step "4. Cooperative close (channel 1)"
CC_BEFORE=$(log_count 'ChannelClosed(bytes32,uint256,uint256)' || echo 0)
CC_BEFORE=${CC_BEFORE:-0}
lncli_n 1 closechannel "$FUNDING_TXID" >/dev/null 2>&1 &
CLOSE_PID=$!

# Only channel 1's 100-USDC deposit leaves the escrow; the reverse channel
# stays open (and stays available in suspended mode).
wait_until $WAIT_SETTLE "channel-1 escrow paid out" escrow_is 50
wait_until $WAIT_SETTLE "ChannelClosed event emitted" \
    log_gt 'ChannelClosed(bytes32,uint256,uint256)' "$CC_BEFORE" \
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

UC_BEFORE=$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)' || echo 0)
UC_BEFORE=${UC_BEFORE:-0}
FD_BEFORE=$(log_count 'FundsDistributed(bytes32,uint256,uint256)' || echo 0)
FD_BEFORE=${FD_BEFORE:-0}
lncli_n 1 closechannel --force "$FUNDING_TXID2" >/dev/null 2>&1 &
FORCE_PID=$!

wait_until $WAIT_SETTLE "forceClose on-chain" \
    log_gt 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)' "$UC_BEFORE"
kill $FORCE_PID 2>/dev/null || true
ok "forceClose landed; channel in challenge window"

# No manual distributeFunds: node1's in-node EVM settler broadcasts it on
# its own once the challenge window elapses (no HTLCs are pending, the
# payment settled before the force close). We just wait for the on-chain
# effect. Only the reverse channel's deposit should remain escrowed.
wait_until $WAIT_SETTLE "settler auto-broadcasts distributeFunds" escrow_is 50
wait_until $WAIT_SETTLE "FundsDistributed event emitted" \
    log_gt 'FundsDistributed(bytes32,uint256,uint256)' "$FD_BEFORE" \
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

FC_BEFORE=$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)' || echo 0)
FC_BEFORE=${FC_BEFORE:-0}
HC_BEFORE=$(log_count 'HTLCClaimed(bytes32,uint256,bytes32)' || echo 0)
HC_BEFORE=${HC_BEFORE:-0}
FD_BEFORE=$(log_count 'FundsDistributed(bytes32,uint256,uint256)' || echo 0)
FD_BEFORE=${FD_BEFORE:-0}
lncli_n 1 closechannel --force "$FUNDING_TXID3" >/dev/null 2>&1 &
FORCE_PID2=$!
fc_incremented() {
    log_gt 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)' "$FC_BEFORE"
}
wait_until $WAIT_SETTLE "forceClose with pending HTLC on-chain" fc_incremented
kill $FORCE_PID2 2>/dev/null || true
ok "forceClose landed with an in-flight HTLC committed"

# Reveal the preimage at node2; its settler now claims the HTLC on-chain.
lncli_n 2 settleinvoice "$PREIMAGE" >/dev/null 2>&1 || true
kill $HOLD_PAY_PID 2>/dev/null || true
htlc_claimed() {
    log_gt 'HTLCClaimed(bytes32,uint256,bytes32)' "$HC_BEFORE"
}
wait_until $WAIT_SETTLE "settler broadcasts claimHtlc on-chain" htlc_claimed
ok "in-flight HTLC resolved on-chain via claimHtlc (Merkle proof verified)"

# With the only HTLC resolved, a settler finalises the channel.
fd_incremented() { log_gt 'FundsDistributed(bytes32,uint256,uint256)' "$FD_BEFORE"; }
wait_until $WAIT_SETTLE "distributeFunds after HTLC resolution" fd_incremented
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

FC_BEFORE=$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)' || echo 0)
FC_BEFORE=${FC_BEFORE:-0}
FD_BEFORE=$(log_count 'FundsDistributed(bytes32,uint256,uint256)' || echo 0)
FD_BEFORE=${FD_BEFORE:-0}
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
TO_BEFORE=$(log_count 'HTLCTimeout(bytes32,uint256)' || echo 0)
TO_BEFORE=${TO_BEFORE:-0}
htlc_timed_out() {
    [ "$(log_count 'HTLCTimeout(bytes32,uint256)')" -gt "$TO_BEFORE" ]
}
wait_until $WAIT_SETTLE "settler broadcasts timeoutHtlc on-chain" htlc_timed_out
ok "in-flight HTLC reclaimed on-chain via timeoutHtlc (timelock deadline honored)"

wait_until $WAIT_SETTLE "distributeFunds after HTLC timeout" fd_incremented
ok "channel finalised after in-flight HTLC timeout"
fi

# Step 8 kills and restarts a node, fast-forwarding the challenge window with
# anvil_mine — local Anvil only.
if [ "$NETWORK" = "anvil" ]; then
step "8. Crash recovery: force close → kill node1 mid-window → restart → distributeFunds"
# The EVM settler runs OUTSIDE contractcourt's resolver state machine, so its
# crash-recovery is the path most exposed by that design divergence: if a node
# dies after a force close is on-chain but before distributeFunds, the escrow
# must NOT be stranded — on restart the chain watcher has to re-detect the
# close and the settler must resume and finalise. This step proves exactly
# that on a fresh, HTLC-free channel.
CHAN_OPEN=$(lncli_n 1 openchannel --node_key="$PK2" --local_amt=5000000000)
FUNDING_TXID5=$(echo "$CHAN_OPEN" | json 'print(json.load(sys.stdin)["funding_txid"])')
wait_until $WAIT_CHAN "recovery test channel active on node1" chan_active 1 2
wait_until $WAIT_CHAN "recovery test channel active on node2" chan_active 2 2

# Deliberately force-close at the INITIAL (zero-payment) state: the funding
# handshake now signs the EIP-712 StateUpdate for height 0, so node1 holds a
# valid co-signed state to present even on a never-used channel. (Before the
# fix this reverted with "retained sig does not recover to counterparty".)
RC_FC_BEFORE=$(log_count 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)' || echo 0)
RC_FC_BEFORE=${RC_FC_BEFORE:-0}
RC_FD_BEFORE=$(log_count 'FundsDistributed(bytes32,uint256,uint256)' || echo 0)
RC_FD_BEFORE=${RC_FD_BEFORE:-0}
RC_ESCROW=$(erc20_bal "$CM")

# Force close, then kill node1 the instant the close is on-chain — well before
# its settler's challenge-window timer (CHALLENGE_PERIOD s) would distribute.
lncli_n 1 closechannel --force "$FUNDING_TXID5" >/dev/null 2>&1 &
RC_FORCE_PID=$!
rc_fc_inc() { log_gt 'UnilateralCloseInitiated(bytes32,address,uint256,uint256,uint256,uint256)' "$RC_FC_BEFORE"; }
wait_until $WAIT_SETTLE "forceClose on-chain (recovery test)" rc_fc_inc
kill $RC_FORCE_PID 2>/dev/null || true

pkill -f "lnddir=$WORKDIR/node1" 2>/dev/null || true
node1_down() { ! pgrep -f "lnddir=$WORKDIR/node1" >/dev/null 2>&1; }
wait_until 15 "node1 process gone" node1_down
ok "node1 killed after force close, before its settler distributed"

# Elapse the whole challenge window WHILE node1 is down (mine blocks so
# block.timestamp advances on the 1s devnet cadence), then confirm nothing
# distributed the escrow in node1's absence.
cast rpc anvil_mine "$(printf '0x%x' $((CHALLENGE_PERIOD + 20)))" 0x1 \
    --rpc-url "$RPC" >/dev/null 2>&1
sleep 2
[ "$(erc20_bal "$CM")" = "$RC_ESCROW" ] \
    || fail "escrow moved while node1 was down (precondition broken)"
[ "$(log_count 'FundsDistributed(bytes32,uint256,uint256)')" = "$RC_FD_BEFORE" ] \
    || fail "distributeFunds fired while node1 was down"
ok "challenge window elapsed with node1 offline; escrow still held on-chain"

# Restart node1 on the SAME lnddir. The watcher must re-detect the close from
# the chain and the settler must resume → distributeFunds.
boot_node 1
wait_until 30 "node1 back online" lncli_n 1 getinfo
ok "node1 restarted on its existing state"

rc_fd_inc() { log_gt 'FundsDistributed(bytes32,uint256,uint256)' "$RC_FD_BEFORE"; }
wait_until $WAIT_SETTLE "settler resumes after restart → distributeFunds" rc_fd_inc
ok "EVM settler RESUMED after restart and finalised the escrow (no funds stranded)"
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
    echo "    macaroon:         $WORKDIR/node1/data/chain/evm/$NETWORK/$ASSET/admin.macaroon"
    echo " -> node2 gRPC:       127.0.0.1:11802   REST: http://127.0.0.1:12802"
    echo "    macaroon:         $WORKDIR/node2/data/chain/evm/$NETWORK/$ASSET/admin.macaroon"
    echo ""
    echo "    lncli example:"
    echo "    lncli --rpcserver=127.0.0.1:11801 --lnddir=$WORKDIR/node1 \\"
    echo "          --macaroonpath=$WORKDIR/node1/data/chain/evm/$NETWORK/$ASSET/admin.macaroon getinfo"
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
