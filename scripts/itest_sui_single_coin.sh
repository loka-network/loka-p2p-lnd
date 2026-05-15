#!/bin/bash
# itest_sui.sh
# End-to-end integration test for LND running on the Sui blockchain backend.

set -e
export ITEST_SUI_FAST_SWEEP=1

# Configuration
LND_BIN="./lnd-debug"
LNCLI_BIN="./lncli-debug"
SUI_CMD="sui"

NETWORK="${1:-devnet}"

if [ "$NETWORK" == "localnet" ]; then
    echo "=== Running in LOCALNET mode ==="
    SUI_RPC_HOST="http://127.0.0.1:9000"
    FAUCET_URL="" # Localnet uses the active-env settings for faucet natively
    sui client switch --env localnet || true

    if ! nc -z 127.0.0.1 9000; then
        echo "Local Sui node not found on port 9000. Starting it..."
        RUST_LOG="off,sui_node=info" sui start --with-faucet --force-regenesis > /tmp/sui_localnet.log 2>&1 &
        SUI_PID=$!
        echo "Waiting for local node RPC (9000) and Faucet (9123) to initialize..."
        for i in {1..30}; do
            if nc -z 127.0.0.1 9000 && nc -z 127.0.0.1 9123; then
                break
            fi
            sleep 1
        done
        sleep 2 # Extra padding
    else
        echo "Local Sui node is already running."
    fi
elif [ "$NETWORK" == "devnet" ]; then
    echo "=== Running in DEVNET mode ==="
    SUI_RPC_HOST="https://fullnode.devnet.sui.io:443"
    FAUCET_URL="https://faucet.devnet.sui.io/v2/gas"
    sui client switch --env devnet || true
else
    echo "Error: Unknown network parameter '$NETWORK'. Please use 'localnet' or 'devnet'."
    exit 1
fi

ALICE_DIR="/tmp/lnd-sui-test/alice"
BOB_DIR="/tmp/lnd-sui-test/bob"
ALICE_PORT=10011
BOB_PORT=10012
ALICE_REST=8081
BOB_REST=8082
ALICE_RPC=10009
BOB_RPC=10010

echo "=== Sui LND Integration Test ($NETWORK) ==="

# 1. Clean up from previous runs
echo "[1/8] Cleaning up previous test state..."
rm -rf "$ALICE_DIR" "$BOB_DIR"
mkdir -p "$ALICE_DIR" "$BOB_DIR"

# 2. Check prerequisites
if ! command -v "$SUI_CMD" &> /dev/null; then
    echo "Error: '$SUI_CMD' is not installed or not in PATH."
    exit 1
fi
if [ ! -f "$LND_BIN" ] || [ ! -f "$LNCLI_BIN" ]; then
    echo "Error: lnd-debug or lncli-debug not found."
    echo "Please run 'make build' first."
    exit 1
fi

echo "[2.5/8] Funding default Sui CLI address and publishing Lightning Move package..."

# Dynamic conditional compilation for Testnet Timelocks
if [ "$ITEST_SUI_FAST_SWEEP" == "1" ]; then
    echo "Applying FAST_SWEEP 15-second simulation overrides to Move VM contract..."
    # Support both GNU and BSD/macOS sed syntax for in-place backups
    sed -i.bak 's/MIN_TO_SELF_DELAY_MS: u64 = 86_400_000/MIN_TO_SELF_DELAY_MS: u64 = 15_000/g' sui-contracts/lightning/sources/lightning.move
fi

DEFAULT_ADDR=$(sui client active-address || echo "")
if [ -n "$FAUCET_URL" ] && [ -n "$DEFAULT_ADDR" ]; then
    curl --location --request POST "$FAUCET_URL" \
         --header 'Content-Type: application/json' \
         --data '{"FixedAmountRequest":{"recipient":"'"$DEFAULT_ADDR"'"}}' > /dev/null
else
    sui client faucet > /dev/null || true
fi
echo "Waiting for $NETWORK faucet funding..."
sleep 5

# Sui CLI 1.68+ uses test-publish for ephemeral deployments (integration tests).
# Delete stale Move.lock and build/ so the CLI regenerates them with correct chain-id.
MOVE_PKG="./sui-contracts/lightning"
rm -f "$MOVE_PKG/Move.lock"
rm -f "$MOVE_PKG"/Pub.*.toml
rm -f "$MOVE_PKG/Publications.toml"
rm -f Pub.*.toml Publications.toml
rm -rf "$MOVE_PKG/build"

PUBLISH_JSON=$(sui client test-publish --build-env "$NETWORK" --json --gas-budget 100000000 "$MOVE_PKG" 2>/dev/null || echo "")

# echo "PUBLISH_JSON: $PUBLISH_JSON"
PACKAGE_ID=$(echo "$PUBLISH_JSON" | sed -n '/^{/,$p' | jq -r '.objectChanges[] | select(.type == "published") | .packageId')

# Revert the Move VM simulation override to keep git clean
if [ "$ITEST_SUI_FAST_SWEEP" == "1" ]; then
    mv sui-contracts/lightning/sources/lightning.move.bak sui-contracts/lightning/sources/lightning.move 2>/dev/null || true
fi

if [ -z "$PACKAGE_ID" ] || [ "$PACKAGE_ID" == "null" ]; then
    echo "Error: Failed to publish Sui package or extract Package ID. Is your Devnet environment configured correctly?"
    echo "$PUBLISH_JSON"
    exit 1
fi
echo "Published Lightning Package ID: $PACKAGE_ID"

echo "[2.8/8] Starting Alice and Bob LND nodes..."

# Optional: extra hostnames to bake into the auto-generated lnd tls.cert
# as Subject Alternative Names. The default cert only includes the host's
# hostname + "localhost", which is enough for tests on the same box but
# breaks when someone runs prism (or any other lnd client) inside a
# container and connects via "host.docker.internal" — the gRPC handshake
# fails with a "certificate is valid for localhost, not host.docker.internal"
# error. Two ways to opt in:
#
#   DOCKER=1 ./itest_sui_single_coin.sh
#       Convenience flag: appends "host.docker.internal" so containers
#       reaching the host via Docker's special hostname work out of the box.
#
#   TLS_EXTRA_DOMAINS=host.docker.internal,prism.example.com ./itest_sui_single_coin.sh
#       Comma-separated list. Wins over DOCKER=1 when both are set.
TLS_EXTRA_DOMAINS="${TLS_EXTRA_DOMAINS:-}"
if [ "${DOCKER:-0}" = "1" ] && [ -z "$TLS_EXTRA_DOMAINS" ]; then
    TLS_EXTRA_DOMAINS="host.docker.internal"
fi
TLS_EXTRA_ARGS=()
if [ -n "$TLS_EXTRA_DOMAINS" ]; then
    IFS=',' read -ra _domains <<<"$TLS_EXTRA_DOMAINS"
    for d in "${_domains[@]}"; do
        TLS_EXTRA_ARGS+=("--tlsextradomain=$d")
    done
    echo "  → adding TLS extra domains to alice/bob certs: ${_domains[*]}"
fi

# --allow-circular-route is required for self-payments to settle on a
# minimal alice ↔ bob topology (the HTLC has to come back through the
# same channel it left). Without this flag, htlcswitch.go:1161 rejects
# the return hop with "OutgoingFailureCircularRoute → TemporaryChannelFailure"
# and the pathfinder bails out with FAILURE_REASON_NO_ROUTE.
#
# Self-payment is exercised by step [8/8] below and is also a common
# real-world pattern when an LNbits-backed app pays a paywall on the
# same lnd it's funded from (e.g. agents-pay-service + Prism share Alice).

# Start Alice
$LND_BIN \
    --lnddir="$ALICE_DIR" \
    --listen="127.0.0.1:$ALICE_PORT" \
    --rpclisten="127.0.0.1:$ALICE_RPC" \
    --restlisten="127.0.0.1:$ALICE_REST" \
    --suinode.active \
    --suinode.devnet \
    --suinode.rpchost="$SUI_RPC_HOST" \
    --suinode.packageid="$PACKAGE_ID" \
    --protocol.wumbo-channels \
    --protocol.no-anchors \
    --noseedbackup \
    --maxpendingchannels=10 \
    --allow-circular-route \
    "${TLS_EXTRA_ARGS[@]}" \
    > "$ALICE_DIR/lnd.log" 2>&1 &
ALICE_PID=$!

# Start Bob
$LND_BIN \
    --lnddir="$BOB_DIR" \
    --listen="127.0.0.1:$BOB_PORT" \
    --rpclisten="127.0.0.1:$BOB_RPC" \
    --restlisten="127.0.0.1:$BOB_REST" \
    --suinode.active \
    --suinode.devnet \
    --suinode.rpchost="$SUI_RPC_HOST" \
    --suinode.packageid="$PACKAGE_ID" \
    --protocol.wumbo-channels \
    --protocol.no-anchors \
    --noseedbackup \
    --maxpendingchannels=10 \
    --allow-circular-route \
    "${TLS_EXTRA_ARGS[@]}" \
    > "$BOB_DIR/lnd.log" 2>&1 &
BOB_PID=$!

# Function to clean up background processes on exit
cleanup() {
    echo "Saving Bob's log to .bob_lnd.log..."
    cp "$BOB_DIR/lnd.log" .bob_lnd.log 2>/dev/null || true
    echo "Saving Alice's log to .alice_lnd.log..."
    cp "$ALICE_DIR/lnd.log" .alice_lnd.log 2>/dev/null || true
    echo "sleep 600
Cleaning up LND nodes..."
    kill $ALICE_PID $BOB_PID 2>/dev/null || true
    wait $ALICE_PID $BOB_PID 2>/dev/null || true
    
    echo "Cleaning up dangling lncli stream clients..."
    pkill -f "lncli-debug.*closechannel" || true

    if [ -n "$SUI_PID" ]; then
        echo "Stopping background local Sui node (PID: $SUI_PID)..."
        kill $SUI_PID 2>/dev/null || true
        wait $SUI_PID 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Macros for lncli commands
ALICE_CLI="$LNCLI_BIN --lnddir=$ALICE_DIR --rpcserver=localhost:$ALICE_RPC --macaroonpath=$ALICE_DIR/data/chain/sui/devnet/admin.macaroon"
BOB_CLI="$LNCLI_BIN --lnddir=$BOB_DIR --rpcserver=localhost:$BOB_RPC --macaroonpath=$BOB_DIR/data/chain/sui/devnet/admin.macaroon"

echo "Waiting for Alice nodes to initialize (this may take up to 30s)..."
for i in {1..30}; do
    if $ALICE_CLI getinfo &>/dev/null; then
        break
    fi
    sleep 1
done

echo "Waiting for Bob nodes to initialize..."
for i in {1..30}; do
    if $BOB_CLI getinfo &>/dev/null; then
        break
    fi
    sleep 1
done

# 3. Requesting coins for Alice on Sui Devnet
echo "[3/8] Generating address and requesting Sui Faucet for Alice..."
ALICE_ADDR=$($ALICE_CLI newaddress p2wkh | jq -r '.address')
ALICE_PUBKEY=$($ALICE_CLI getinfo | jq -r '.identity_pubkey')
echo "Alice Pubkey: $ALICE_PUBKEY"
echo "Alice Address: $ALICE_ADDR"

# Assuming local faucet is running. If interacting with public testnet, we'd use curl for proxy compat
if [ -n "$FAUCET_URL" ]; then
    curl --location --request POST "$FAUCET_URL" \
         --header 'Content-Type: application/json' \
         --data '{"FixedAmountRequest":{"recipient":"'"$ALICE_ADDR"'"}}' > /dev/null
    sleep 5
    # Call faucet a second time so Alice has TWO coins (one for funding, one for gas)
    curl --location --request POST "$FAUCET_URL" \
         --header 'Content-Type: application/json' \
         --data '{"FixedAmountRequest":{"recipient":"'"$ALICE_ADDR"'"}}' > /dev/null
else
    sui client faucet --address "$ALICE_ADDR" || true
    sleep 5
    sui client faucet --address "$ALICE_ADDR" || true
fi
echo "Waiting for $NETWORK faucet funding to propagate across all RPC nodes..."
sleep 15 # Wait for faucet tx

echo "Checking Alice's wallet balance..."
$ALICE_CLI walletbalance

# 4. Connecting Alice to Bob
echo "[4/8] Connecting Alice to Bob..."
BOB_PUBKEY=$($BOB_CLI getinfo | jq -r '.identity_pubkey')
echo "Bob Pubkey: $BOB_PUBKEY"

# Fund Bob with SUI so he has gas for close transactions
BOB_ADDR=$($BOB_CLI newaddress p2wkh | jq -r '.address')
echo "Bob Address: $BOB_ADDR"

# Extract an extra coin object from the local Sui default wallet and transfer it EXACTLY once natively to Bob
EXTRA_COIN=$(sui client gas --json | jq -r '.[1].gasCoinId')
echo "Transferring single object gas coin $EXTRA_COIN to Bob natively..."
sui client transfer-sui --to "$BOB_ADDR" --sui-coin-object-id "$EXTRA_COIN" --gas-budget 50000000 > /dev/null
sleep 15

$ALICE_CLI connect "${BOB_PUBKEY}@127.0.0.1:${BOB_PORT}"
sleep 5

# Wait for Alice's nodes to fully propagate coins
echo "Double checking gas object propagation..."
sleep 10

echo "Checking Bob's UTXO count to verify Single-Coin PTB test prerequisite..."
BOB_UTXO_COUNT=$($BOB_CLI listunspent | jq '.utxos | length')
echo "Bob UTXO Count: $BOB_UTXO_COUNT (Should be exactly 1)"

echo "[5/8] Bob opening channel to Alice (Native Single-Coin PTB Test)..."
# Bob natively holds exactly 1 coin drop transferred dynamically from the root script CLI.
BOB_TOTAL_BAL=$($BOB_CLI walletbalance | jq -r '.confirmed_balance')
# Bob receives a single ~30,000 SUI coin via `sui client transfer-sui` from the
# localnet default wallet, so the Single-Coin PTB constraint is satisfied for
# any open-channel amount below that. We use 9 SUI to leave generous headroom
# for repeated self-payments (step 8) without bumping channel reserves.
# push_amt=1 SUI gives Alice meaningful inbound on this leg so the path-finder
# has a usable bob→alice edge when self-payments cycle multiple times.
BOB_LOCAL_AMT=9000000000
$BOB_CLI openchannel --node_key=$ALICE_PUBKEY --local_amt=$BOB_LOCAL_AMT --push_amt=1000000000
	echo "Waiting for Bob's channel to become active on $NETWORK..."
	for i in {1..30}; do
	    ACTIVE_C=$($BOB_CLI listchannels | jq -r '.channels | length')
	    if [ "$ACTIVE_C" == "1" ]; then
	        echo "Channel is fully operational!"; sleep 3
	        break
	    fi
	    echo "Polling for 1 active channel... Current: $ACTIVE_C"
	    sleep 2
	done

# 5b. Open a SECOND channel (Alice → Bob).
#
# Self-payment requires the path-finder to construct alice → bob → alice. With
# only ONE channel, lnd's getOutgoingBalance reports "insufficient_balance"
# even though raw capacity exists, because the same channel is counted on
# both hops. Two channels give the path-finder two distinct edges to choose
# from (one outgoing, one returning) and the cycle resolves cleanly.
#
# We push a small amount to Bob so both sides have liquidity for whichever
# direction the path-finder picks at runtime.
echo "[5b/8] Alice opening a second channel back to Bob (for self-payment routing)..."
# Alice was funded with two 10-SUI faucet UTXOs (FixedAmountRequest twice
# above), so a 9-SUI open + 1-SUI push intrinsically fits inside one single
# faucet UTXO and leaves enough outbound liquidity for many self-pay cycles.
ALICE_LOCAL_AMT_2=9000000000
$ALICE_CLI openchannel --node_key=$BOB_PUBKEY --local_amt=$ALICE_LOCAL_AMT_2 --push_amt=1000000000
for i in {1..30}; do
    ACTIVE_C=$($ALICE_CLI listchannels | jq -r '.channels | length')
    if [ "$ACTIVE_C" -ge "2" ]; then
        echo "Both channels are operational ($ACTIVE_C active)!"; sleep 3
        break
    fi
    echo "Polling for 2 active channels... Current: $ACTIVE_C"
    sleep 2
done

# 6. Verification
echo "[6/8] Verifying Channel..."
$ALICE_CLI pendingchannels
$ALICE_CLI listchannels

# 6b. SCID consistency assertion.
#
# Regression guard for the suinotify "canonical-checkpoint vs GetBestEpoch
# fallback" race (see chainntnfs/suinotify/rpc_client.go around the
# "MUST NOT fall back to GetBestEpoch" comment).
#
# Both nodes must agree on the SCID of every shared channel. If they don't,
# each side derives a different `short_channel_id` (the SCID embeds
# `block_height << 40`, so a 1-checkpoint observation gap shifts the SCID
# by exactly 2^40 = 1099511627776), and the gossip announcement proof
# exchange stays stuck at "1/2 received, waiting for other half" forever,
# permanently breaking pathfinding and self-payments.
#
# We compare both lists sorted by channel_point so a single mismatch raises
# a clear, actionable error instead of letting downstream steps fail with
# generic FAILURE_REASON_NO_ROUTE on self-payments.
echo "[6b/8] Asserting Alice and Bob agree on all shared SCIDs..."
ALICE_SCIDS=$($ALICE_CLI listchannels \
    | jq -r '.channels | sort_by(.channel_point) | .[] | "\(.channel_point)\t\(.scid)"')
BOB_SCIDS=$($BOB_CLI listchannels \
    | jq -r '.channels | sort_by(.channel_point) | .[] | "\(.channel_point)\t\(.scid)"')

if [ "$ALICE_SCIDS" != "$BOB_SCIDS" ]; then
    echo "❌ SCID DIVERGENCE detected — this is the suinotify checkpoint race."
    echo "   See rpc_client.go: when sui_getTransactionBlock returns"
    echo "   effects.success but an empty canonical 'checkpoint' field,"
    echo "   the adapter must NOT fall back to GetBestEpoch (observer-"
    echo "   local chain tip)."
    echo ""
    echo "   Alice's view (channel_point  scid):"
    echo "$ALICE_SCIDS" | sed 's/^/     /'
    echo ""
    echo "   Bob's view (channel_point  scid):"
    echo "$BOB_SCIDS" | sed 's/^/     /'
    echo ""
    echo "   Diff:"
    diff <(echo "$ALICE_SCIDS") <(echo "$BOB_SCIDS") | sed 's/^/     /'
    exit 1
fi
echo "✓ SCIDs agree on both sides:"
echo "$ALICE_SCIDS" | sed 's/^/    /'

# 7. Payment Test (cross-wallet: Bob -> Alice)
echo "[7/8] Testing Lightning Routing (Bob -> Alice)..."
INVOICE=$($ALICE_CLI addinvoice --amt=1000 --memo="single-coin-test" | jq -r '.payment_request')
echo "Alice Invoice: $INVOICE"
$BOB_CLI payinvoice --pay_req="$INVOICE" --force

echo "Bob Channel Balance post-payment:"
$BOB_CLI channelbalance

# 8. Self-Payment Test (Alice -> Alice via Bob, requires --allow-circular-route)
#
# Why this matters: when an LNbits-backed app like agents-pay-service is
# co-deployed with a paywall (e.g. Prism) on the same lnd, every L402
# payment looks like a self-payment to lnd. Without --allow-circular-route
# the htlcswitch rejects the return hop. With it, the HTLC routes
# alice -> bob -> alice through the same channel and settles cleanly.
#
# Self-payment over circular route is now ON by default after the
# Move HTLCKey composite-key + channel-absolute direction fix landed
# (table-key collision and direction-encoding mismatch were the root
# causes of the intermittent commit-sig race). Set ITEST_SUI_SELF_PAY=0
# to opt out (e.g. when you want channels intact for downstream paycli /
# L402 / agents-pay-service flow tests that follow).
SELF_PAY=${ITEST_SUI_SELF_PAY:-1}
SELF_PAY_SKIPPED=0
if [ "$SELF_PAY" != "1" ]; then
    echo "[8/8] Self-Payment test SKIPPED (set ITEST_SUI_SELF_PAY=1 to enable)."
    echo "      Channels intact: ready for paycli / L402 / agents-pay-service flow tests."
    SELF_PAY_SKIPPED=1
fi
if [ "$SELF_PAY_SKIPPED" = "1" ]; then
    echo "=== Sui LND Integration Test SUCCESS (steps 1-7) ==="
    echo "=================================================================================="
    echo "✅ Test workflow completed! Nodes are now in [Suspended Mode], waiting for external RPC / REST requests."
    echo ""
    echo " -> Alice REST Address: https://127.0.0.1:$ALICE_REST"
    echo " -> Bob REST Address:   https://127.0.0.1:$BOB_REST"
    echo ""
    echo "Once you are done testing, press [Enter] in this terminal to terminate nodes and exit..."
    echo "=================================================================================="
    read -p ""
    exit 0
fi

# Repeated self-payment loop: previously the test only fired once. After the
# HTLCKey + direction-encoding fix landed (2026-05-14) a single round passed,
# but lokapay-driven follow-up payments still hit `status=pending` then
# `FAILURE_REASON_INSUFFICIENT_BALANCE` on the second try — meaning the
# regression only surfaces from round 2 onward. We now exercise N rounds
# inside the itest so the script (not lokapay) is the first thing to catch it.
SELF_PAY_ROUNDS=${ITEST_SUI_SELF_PAY_ROUNDS:-5}
SELF_PAY_AMT=2000
echo "[8/8] Testing Self-Payment (Alice -> Alice via Bob) x $SELF_PAY_ROUNDS rounds..."

# Snapshot pre-existing fail signatures so we only flag *new* hits introduced
# by the self-pay loop. The breach-arbiter startup line counts as an existing
# match and would otherwise produce a false positive.
ALICE_PREEXISTING_FAIL=$(grep -c -E "invalid_commit_sig|Attempting to force close|breach.*detected" \
    "$ALICE_DIR/lnd.log" 2>/dev/null || echo 0)
BOB_PREEXISTING_FAIL=$(grep -c -E "invalid_commit_sig|Attempting to force close|breach.*detected" \
    "$BOB_DIR/lnd.log" 2>/dev/null || echo 0)

for ROUND in $(seq 1 "$SELF_PAY_ROUNDS"); do
    echo ""
    echo "--- self-pay round $ROUND/$SELF_PAY_ROUNDS ---"

    SELF_INVOICE_JSON=$($ALICE_CLI addinvoice --amt=$SELF_PAY_AMT --memo="self-payment-itest-r$ROUND")
    SELF_INVOICE=$(echo "$SELF_INVOICE_JSON" | jq -r '.payment_request')
    SELF_RHASH=$(echo "$SELF_INVOICE_JSON" | jq -r '.r_hash')
    echo "Alice Self-Invoice r$ROUND (rhash $SELF_RHASH)"

    # Reset mission control between rounds so a transient fail on one round
    # does not poison the path-finder for the next. Mirrors what `lokapay`
    # would do if it were retrying.
    $ALICE_CLI resetmc > /dev/null 2>&1 || true

    SELF_RAW=$(timeout 35 $ALICE_CLI sendpayment \
        --pay_req="$SELF_INVOICE" \
        --allow_self_payment \
        --force \
        --timeout 30s \
        --json 2>/dev/null || true)

    SELF_LAST=$(echo "$SELF_RAW" | jq -s 'last' 2>/dev/null || echo '{}')
    SELF_STATUS=$(echo "$SELF_LAST" | jq -r '.status // "PARSE_ERROR"')
    SELF_PREIMAGE=$(echo "$SELF_LAST" | jq -r '.payment_preimage // ""')
    SELF_FAIL_REASON=$(echo "$SELF_LAST" | jq -r '.failure_reason // ""')

    if [ "$SELF_STATUS" = "PARSE_ERROR" ] && \
       grep -q "Adding preimage=.*for $SELF_RHASH" "$ALICE_DIR/lnd.log" 2>/dev/null; then
        echo "(parsed lncli output failed, but alice witness cache confirms preimage — treating as success)"
        SELF_STATUS="SUCCEEDED"
        SELF_PREIMAGE=$(grep "Adding preimage=.*for $SELF_RHASH" "$ALICE_DIR/lnd.log" \
                        | tail -1 | sed -nE 's/.*preimage=([0-9a-f]+) .*/\1/p')
    fi

    echo "Round $ROUND status:        $SELF_STATUS"
    echo "Round $ROUND preimage:      $SELF_PREIMAGE"
    [ -n "$SELF_FAIL_REASON" ] && echo "Round $ROUND failure_reason: $SELF_FAIL_REASON"

    if [ "$SELF_STATUS" != "SUCCEEDED" ]; then
        echo ""
        echo "❌ Self-payment round $ROUND FAILED — expected SUCCEEDED, got '$SELF_STATUS' (failure_reason='$SELF_FAIL_REASON')"
        echo "   Last lncli object: $SELF_LAST"
        echo ""
        echo "   Channels at time of failure:"
        $ALICE_CLI listchannels | jq '.channels[] | {chan: .chan_id, active, local_balance, remote_balance, num_updates, pending_htlcs: (.pending_htlcs | length)}' 2>/dev/null || true
        exit 1
    fi

    if [ -z "$SELF_PREIMAGE" ] || [ "$SELF_PREIMAGE" = "null" ] || \
       [ "$SELF_PREIMAGE" = "0000000000000000000000000000000000000000000000000000000000000000" ]; then
        echo "❌ Round $ROUND status was SUCCEEDED but preimage is empty/zero — fail"
        exit 1
    fi

    # Per-round commit-sig / force-close guard. We compare the current count
    # against the snapshot taken before the loop — any new hit means *this*
    # round triggered a regression and we abort immediately so the operator
    # can inspect a fresh log.
    ALICE_FAIL_NOW=$(grep -c -E "invalid_commit_sig|Attempting to force close|breach.*detected" \
        "$ALICE_DIR/lnd.log" 2>/dev/null || echo 0)
    BOB_FAIL_NOW=$(grep -c -E "invalid_commit_sig|Attempting to force close|breach.*detected" \
        "$BOB_DIR/lnd.log" 2>/dev/null || echo 0)
    if [ "$ALICE_FAIL_NOW" -gt "$ALICE_PREEXISTING_FAIL" ] || \
       [ "$BOB_FAIL_NOW" -gt "$BOB_PREEXISTING_FAIL" ]; then
        echo ""
        echo "❌ Round $ROUND introduced new commit-sig / force-close events:"
        echo "   alice: $ALICE_PREEXISTING_FAIL -> $ALICE_FAIL_NOW"
        echo "   bob:   $BOB_PREEXISTING_FAIL -> $BOB_FAIL_NOW"
        grep -E "invalid_commit_sig|Attempting to force close|breach.*detected" \
            "$ALICE_DIR/lnd.log" "$BOB_DIR/lnd.log" 2>/dev/null | tail -10
        exit 1
    fi

    # Verify both channels are still active after settlement before next round.
    ACTIVE_NOW=$($ALICE_CLI listchannels | jq -r '[.channels[] | select(.active == true)] | length' 2>/dev/null || echo 0)
    if [ "$ACTIVE_NOW" -lt 2 ]; then
        echo "❌ Round $ROUND left fewer than 2 active channels (now: $ACTIVE_NOW) — abort"
        $ALICE_CLI listchannels | jq '.channels[] | {chan: .chan_id, active, pending_htlcs: (.pending_htlcs | length)}' 2>/dev/null || true
        exit 1
    fi

    echo "✓ round $ROUND OK (active channels: $ACTIVE_NOW)"
done

echo ""
echo "✓ All $SELF_PAY_ROUNDS self-payment rounds SUCCEEDED with no force-close events"

echo "=== Sui LND Integration Test SUCCESS ==="

echo "=================================================================================="
echo "✅ Test workflow completed! Nodes are now in [Suspended Mode], waiting for external RPC / REST requests."
echo "You can import lnrpc/lightning.swagger.json into Postman,"
echo "and interact with the local nodes below (remember to disable SSL certificate verification in Postman):"
echo ""
echo " -> Alice REST Address: https://127.0.0.1:$ALICE_REST"
echo " -> Alice Macaroon (Hex):"
echo "    \$(xxd -ps -u -c 1000 \$ALICE_DIR/data/chain/sui/devnet/admin.macaroon)"
echo ""
echo " -> Bob REST Address:   https://127.0.0.1:$BOB_REST"
echo " -> Bob Macaroon (Hex):"
echo "    \$(xxd -ps -u -c 1000 \$BOB_DIR/data/chain/sui/devnet/admin.macaroon)"
echo ""
echo "Once you are done testing, press [Enter] in this terminal to terminate nodes and exit..."
echo "=================================================================================="
read -p ""

exit 0
