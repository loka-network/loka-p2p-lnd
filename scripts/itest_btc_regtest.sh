#!/bin/bash
# itest_btc_regtest.sh
# Fast, fully-local Bitcoin Lightning test network for the standard (BTC) lnd
# backend — the BTC counterpart to itest_sui_single_coin.sh.
#
# Uses bitcoind in **regtest**: a private chain that starts empty, needs ZERO
# sync / ZERO download, and mines blocks instantly on demand. Nothing connects
# to testnet/mainnet. Coins are spendable after mining 101 blocks (coinbase
# maturity) — all instant.
#
# Ports are intentionally OFFSET from itest_sui_single_coin.sh so a btc-lnd and
# a sui-lnd network can run side by side (e.g. to test the multi-asset
# agents-pay-service aggregator against both at once).
#
# Usage:
#   ./scripts/itest_btc_regtest.sh            # run the flow, then keep nodes up
#   KEEP_ALIVE=0 ./scripts/itest_btc_regtest.sh   # tear everything down at the end
#   SELF_PAY=1   ./scripts/itest_btc_regtest.sh   # also exercise an Alice->Alice self-payment
#
# Prereqs: bitcoind + bitcoin-cli in PATH (macOS: `brew install bitcoin`),
#          and lnd-debug/lncli-debug built (`make build`).

set -e

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
LND_BIN="./lnd-debug"
LNCLI_BIN="./lncli-debug"

KEEP_ALIVE="${KEEP_ALIVE:-1}"   # 1 = leave nodes running at the end (Ctrl-C to stop)
SELF_PAY="${SELF_PAY:-0}"       # 1 = also test an Alice->Alice circular self-payment

BASE_DIR="/tmp/lnd-btc-test"
BTCD_DIR="$BASE_DIR/bitcoind"
ALICE_DIR="$BASE_DIR/alice"
BOB_DIR="$BASE_DIR/bob"

# lnd ports (offset from the SUI script's 100xx/80xx so both can coexist)
ALICE_PORT=11011; ALICE_RPC=11009; ALICE_REST=9081
BOB_PORT=11012;   BOB_RPC=11010;   BOB_REST=9082

# bitcoind regtest endpoints
BTC_RPC_PORT=18443
BTC_RPC_USER=lnd
BTC_RPC_PASS=lnd
ZMQ_BLOCK=28332
ZMQ_TX=28333

# Macaroon path for the BTC/regtest chain (note: bitcoin/regtest, not sui/devnet)
MAC_PATH="data/chain/bitcoin/regtest/admin.macaroon"

echo "=== Bitcoin (regtest) LND Integration Test ==="

# ---------------------------------------------------------------------------
# 1. Prerequisites
# ---------------------------------------------------------------------------
echo "[1/9] Checking prerequisites..."
if ! command -v bitcoind &>/dev/null || ! command -v bitcoin-cli &>/dev/null; then
    echo "Error: bitcoind / bitcoin-cli not found in PATH."
    echo "  macOS:  brew install bitcoin"
    echo "  (regtest needs no blockchain download — it's a local private chain.)"
    exit 1
fi
if [ ! -f "$LND_BIN" ] || [ ! -f "$LNCLI_BIN" ]; then
    echo "Error: lnd-debug / lncli-debug not found. Run 'make build' first."
    exit 1
fi
for tool in jq nc; do
    command -v "$tool" &>/dev/null || { echo "Error: '$tool' not found."; exit 1; }
done

# ---------------------------------------------------------------------------
# 2. Clean slate
# ---------------------------------------------------------------------------
echo "[2/9] Cleaning previous test state..."
# (cleanup() defined below also runs on exit; here we wipe dirs for a fresh chain)
pkill -f "bitcoind -regtest -datadir=$BTCD_DIR" 2>/dev/null || true
sleep 1
rm -rf "$BASE_DIR"
mkdir -p "$BTCD_DIR" "$ALICE_DIR" "$BOB_DIR"

BTC_CLI="bitcoin-cli -regtest -datadir=$BTCD_DIR -rpcport=$BTC_RPC_PORT -rpcuser=$BTC_RPC_USER -rpcpassword=$BTC_RPC_PASS"

# ---------------------------------------------------------------------------
# Cleanup trap
# ---------------------------------------------------------------------------
cleanup() {
    echo ""
    echo "Cleaning up..."
    cp "$ALICE_DIR/lnd.log" .alice_btc_lnd.log 2>/dev/null || true
    cp "$BOB_DIR/lnd.log" .bob_btc_lnd.log 2>/dev/null || true
    kill "$ALICE_PID" "$BOB_PID" 2>/dev/null || true
    wait "$ALICE_PID" "$BOB_PID" 2>/dev/null || true
    $BTC_CLI stop 2>/dev/null || true
    [ -n "$BTC_PID" ] && wait "$BTC_PID" 2>/dev/null || true
    echo "Done."
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# 3. Start bitcoind (regtest)
# ---------------------------------------------------------------------------
echo "[3/9] Starting bitcoind (regtest)..."
bitcoind -regtest \
    -datadir="$BTCD_DIR" \
    -rpcbind=127.0.0.1 -rpcallowip=127.0.0.1 \
    -rpcport=$BTC_RPC_PORT \
    -rpcuser=$BTC_RPC_USER -rpcpassword=$BTC_RPC_PASS \
    -zmqpubrawblock=tcp://127.0.0.1:$ZMQ_BLOCK \
    -zmqpubrawtx=tcp://127.0.0.1:$ZMQ_TX \
    -txindex=1 -fallbackfee=0.0002 -server=1 \
    > "$BTCD_DIR/bitcoind.log" 2>&1 &
BTC_PID=$!

echo "Waiting for bitcoind RPC..."
for i in {1..30}; do
    if $BTC_CLI getblockchaininfo &>/dev/null; then break; fi
    sleep 1
done
$BTC_CLI getblockchaininfo &>/dev/null || { echo "bitcoind failed to start. See $BTCD_DIR/bitcoind.log"; exit 1; }

# ---------------------------------------------------------------------------
# 4. Wallet + mine 101 blocks (coinbase maturity -> spendable coins, instant)
# ---------------------------------------------------------------------------
echo "[4/9] Creating miner wallet and mining 101 blocks..."
$BTC_CLI createwallet miner &>/dev/null || $BTC_CLI loadwallet miner &>/dev/null || true
MINER_ADDR=$($BTC_CLI getnewaddress)
$BTC_CLI generatetoaddress 101 "$MINER_ADDR" >/dev/null
echo "  miner spendable balance: $($BTC_CLI getbalance) BTC"

# ---------------------------------------------------------------------------
# 5. Start Alice & Bob lnd (bitcoind backend)
# ---------------------------------------------------------------------------
echo "[5/9] Starting Alice and Bob lnd nodes (bitcoind regtest backend)..."

# Optional TLS extra domains (e.g. host.docker.internal for a prism container),
# same convention as the SUI script.
TLS_EXTRA_DOMAINS="${TLS_EXTRA_DOMAINS:-}"
if [ "${DOCKER:-0}" = "1" ] && [ -z "$TLS_EXTRA_DOMAINS" ]; then
    TLS_EXTRA_DOMAINS="host.docker.internal"
fi
TLS_EXTRA_ARGS=()
if [ -n "$TLS_EXTRA_DOMAINS" ]; then
    IFS=',' read -ra _domains <<<"$TLS_EXTRA_DOMAINS"
    for d in "${_domains[@]}"; do TLS_EXTRA_ARGS+=("--tlsextradomain=$d"); done
fi

start_lnd() {
    local dir=$1 p2p=$2 rpc=$3 rest=$4
    $LND_BIN \
        --lnddir="$dir" \
        --listen="127.0.0.1:$p2p" \
        --rpclisten="127.0.0.1:$rpc" \
        --restlisten="127.0.0.1:$rest" \
        --no-rest-tls \
        --bitcoin.active --bitcoin.regtest --bitcoin.node=bitcoind \
        --bitcoind.rpchost="127.0.0.1:$BTC_RPC_PORT" \
        --bitcoind.rpcuser="$BTC_RPC_USER" \
        --bitcoind.rpcpass="$BTC_RPC_PASS" \
        --bitcoind.zmqpubrawblock="tcp://127.0.0.1:$ZMQ_BLOCK" \
        --bitcoind.zmqpubrawtx="tcp://127.0.0.1:$ZMQ_TX" \
        --noseedbackup \
        --maxpendingchannels=10 \
        --allow-circular-route \
        --protocol.wumbo-channels \
        "${TLS_EXTRA_ARGS[@]}" \
        > "$dir/lnd.log" 2>&1 &
    echo $!
}

ALICE_PID=$(start_lnd "$ALICE_DIR" "$ALICE_PORT" "$ALICE_RPC" "$ALICE_REST")
BOB_PID=$(start_lnd "$BOB_DIR" "$BOB_PORT" "$BOB_RPC" "$BOB_REST")

ALICE_CLI="$LNCLI_BIN --lnddir=$ALICE_DIR --network=regtest --rpcserver=localhost:$ALICE_RPC --macaroonpath=$ALICE_DIR/$MAC_PATH"
BOB_CLI="$LNCLI_BIN --lnddir=$BOB_DIR --network=regtest --rpcserver=localhost:$BOB_RPC --macaroonpath=$BOB_DIR/$MAC_PATH"

echo "Waiting for Alice & Bob RPC to come up..."
for cli in "$ALICE_CLI" "$BOB_CLI"; do
    for i in {1..30}; do
        if $cli getinfo &>/dev/null; then break; fi
        sleep 1
    done
done
$ALICE_CLI getinfo &>/dev/null || { echo "Alice lnd failed. See $ALICE_DIR/lnd.log"; exit 1; }
$BOB_CLI getinfo &>/dev/null || { echo "Bob lnd failed. See $BOB_DIR/lnd.log"; exit 1; }

# ---------------------------------------------------------------------------
# 6. Fund Alice on-chain (send from miner wallet, confirm with 6 blocks)
# ---------------------------------------------------------------------------
echo "[6/9] Funding Alice on-chain..."
ALICE_ADDR=$($ALICE_CLI newaddress p2wkh | jq -r .address)
$BTC_CLI sendtoaddress "$ALICE_ADDR" 1.0 >/dev/null
$BTC_CLI generatetoaddress 6 "$MINER_ADDR" >/dev/null
echo "Waiting for Alice to see confirmed funds..."
for i in {1..30}; do
    bal=$($ALICE_CLI walletbalance | jq -r .confirmed_balance)
    [ "$bal" != "0" ] && break
    sleep 1
done
echo "  Alice confirmed on-chain balance: $($ALICE_CLI walletbalance | jq -r .confirmed_balance) sats"

# ---------------------------------------------------------------------------
# 7. Connect + open channel Alice -> Bob (confirm with 6 blocks)
# ---------------------------------------------------------------------------
echo "[7/9] Connecting peers and opening channel..."
BOB_PUBKEY=$($BOB_CLI getinfo | jq -r .identity_pubkey)
$ALICE_CLI connect "$BOB_PUBKEY@127.0.0.1:$BOB_PORT" &>/dev/null || true
$ALICE_CLI openchannel --node_key="$BOB_PUBKEY" --local_amt=5000000 >/dev/null
$BTC_CLI generatetoaddress 6 "$MINER_ADDR" >/dev/null
echo "Waiting for channel to become active..."
for i in {1..30}; do
    active=$($ALICE_CLI listchannels | jq -r '[.channels[] | select(.active==true)] | length')
    [ "$active" -ge 1 ] && break
    sleep 1
done
[ "$active" -ge 1 ] || { echo "Channel did not activate. See $ALICE_DIR/lnd.log"; exit 1; }
echo "  active channels: $active"

# A freshly opened channel isn't immediately usable for pathfinding: lnd must
# finish syncing the channel into its graph. Paying before that yields
# FAILURE_REASON_NO_ROUTE. Wait for synced_to_graph on both nodes.
echo "Waiting for graph sync on both nodes..."
for cli in "$ALICE_CLI" "$BOB_CLI"; do
    for i in {1..30}; do
        [ "$($cli getinfo | jq -r .synced_to_graph)" = "true" ] && break
        sleep 1
    done
done

# A just-active channel has a brief window where the pathfinder's bandwidth
# hint for the local link still reads 0 (the link isn't registered in the
# switch's routing view yet), so the first pay can fail with
# FAILURE_REASON_INSUFFICIENT_BALANCE even though listchannels shows funds.
# Poll for a usable local balance, then retry the payment with a FRESH invoice
# each attempt (a permanently-failed payment hash can't be re-paid).
echo "Waiting for usable channel local balance..."
for i in {1..30}; do
    lb=$($ALICE_CLI listchannels | jq -r '[.channels[] | select(.active==true)] | .[0].local_balance // 0')
    [ "${lb:-0}" -ge 20000 ] && break
    sleep 1
done
echo "  Alice channel local_balance: ${lb:-0} sats"
sleep 2  # let the switch finish registering link bandwidth for pathfinding

# helper: pay a fresh Bob invoice of $1 sats; retries up to 3 times
pay_bob() {
    local amt=$1 out=""
    for attempt in 1 2 3; do
        local inv
        inv=$($BOB_CLI addinvoice --amt="$amt" | jq -r .payment_request)
        if out=$($ALICE_CLI payinvoice --force --timeout=60s "$inv" 2>&1); then
            return 0
        fi
        echo "  payment attempt $attempt failed; retrying in 3s..."
        sleep 3
    done
    echo "Payment FAILED after retries. lncli output:"
    echo "$out"
    echo "--- tail of Alice lnd.log ---"
    tail -n 30 "$ALICE_DIR/lnd.log" || true
    return 1
}

# ---------------------------------------------------------------------------
# 8. Pay an invoice Alice -> Bob
# ---------------------------------------------------------------------------
echo "[8/9] Paying a Lightning invoice Alice -> Bob..."
pay_bob 10000 || exit 1
echo "  ✓ payment settled. Bob channel balance:"
$BOB_CLI channelbalance | jq '{local_balance: .local_balance.sat, remote_balance: .remote_balance.sat}'

if [ "$SELF_PAY" = "1" ]; then
    # A circular self-payment Alice->Bob->Alice needs the pathfinder to find
    # TWO distinct edges (one outgoing, one returning). With a SINGLE channel
    # lnd reports insufficient_balance / no_route because the same channel is
    # counted on both hops. So we open a SECOND Alice->Bob channel with a
    # --push_amt, giving Bob local balance (well above its channel reserve) to
    # forward the return hop. This mirrors itest_sui_single_coin.sh step 5b.
    echo "  [self-pay] opening a 2nd channel (with push) for circular routing..."
    $ALICE_CLI openchannel --node_key="$BOB_PUBKEY" --local_amt=5000000 \
        --push_amt=1000000 >/dev/null
    $BTC_CLI generatetoaddress 6 "$MINER_ADDR" >/dev/null
    for i in {1..30}; do
        nch=$($ALICE_CLI listchannels | jq -r '[.channels[] | select(.active==true)] | length')
        [ "${nch:-0}" -ge 2 ] && break
        sleep 1
    done
    echo "  [self-pay] active channels: ${nch:-0}"
    # Both nodes must re-sync the graph so the new edge is usable for routing.
    for cli in "$ALICE_CLI" "$BOB_CLI"; do
        for i in {1..30}; do
            [ "$($cli getinfo | jq -r .synced_to_graph)" = "true" ] && break
            sleep 1
        done
    done
    sleep 2

    echo "  [self-pay] Alice -> Alice circular payment (needs --allow-circular-route)..."
    ok=0
    for attempt in 1 2 3; do
        SINV=$($ALICE_CLI addinvoice --amt=1000 | jq -r .payment_request)
        if SPOUT=$($ALICE_CLI payinvoice --force --allow_self_payment --timeout=60s "$SINV" 2>&1); then
            ok=1; break
        fi
        echo "  self-pay attempt $attempt failed; retrying in 3s..."
        sleep 3
    done
    if [ "$ok" != "1" ]; then
        echo "Self-payment FAILED. lncli output:"; echo "$SPOUT"
        tail -n 30 "$ALICE_DIR/lnd.log" || true
        exit 1
    fi
    echo "  ✓ self-payment settled."
fi

echo "[9/9] ✅ BTC regtest Lightning flow completed successfully."

# ---------------------------------------------------------------------------
# Connection details for agents-pay-service (multi-asset btc backend)
# ---------------------------------------------------------------------------
cat <<EOF

────────────────────────────────────────────────────────────────────────
Alice lnd (use this as the BTC backend in agents-pay-service):
  gRPC:      127.0.0.1:$ALICE_RPC
  REST:      127.0.0.1:$ALICE_REST   (no-rest-tls)
  TLS cert:  $ALICE_DIR/tls.cert
  macaroon:  $ALICE_DIR/$MAC_PATH

Add this BTC entry to your agents-pay-service .env (single-line value also
works; python-dotenv allows a multi-line value wrapped in single quotes):

LNBITS_FUNDING_SOURCES='[
  {
    "asset": "btc",
    "wallet_class": "LndWallet",
    "endpoint": "127.0.0.1",
    "port": $ALICE_RPC,
    "cert": "$ALICE_DIR/tls.cert",
    "admin_macaroon": "$ALICE_DIR/$MAC_PATH",
    "allow_self_payment": true
  }
]'

To run the multi-asset aggregator, add a second object with "asset": "sui"
pointing at your sui-lnd node (from itest_sui_single_coin.sh).

Notes:
  - When LNBITS_FUNDING_SOURCES is set, the legacy LNBITS_BACKEND_WALLET_CLASS
    and flat LND_GRPC_* settings are ignored.
  - It is a DB-managed (Editable) setting: with LNBITS_ADMIN_UI=true it is read
    from .env only on the first boot (empty settings table); afterwards edit it
    via Admin UI → Funding, or clear the settings table to re-read .env.

Mine more blocks anytime:  $BTC_CLI generatetoaddress 6 $MINER_ADDR
────────────────────────────────────────────────────────────────────────
EOF

if [ "$KEEP_ALIVE" = "1" ]; then
    echo "Nodes are running. Press Ctrl-C to stop and clean up."
    while true; do sleep 3600; done
fi
