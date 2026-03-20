#!/bin/bash
# itest_sui.sh
# End-to-end integration test for LND running on the Sui blockchain backend.

set -e

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
    FAUCET_URL="https://faucet.devnet.sui.io"
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
echo "[1/7] Cleaning up previous test state..."
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

echo "[2.5/7] Funding default Sui CLI address and publishing Lightning Move package..."
if [ -n "$FAUCET_URL" ]; then
    sui client faucet --url "$FAUCET_URL" > /dev/null || true
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

echo "PUBLISH_JSON: $PUBLISH_JSON"
PACKAGE_ID=$(echo "$PUBLISH_JSON" | grep -v 'Note' | grep -v 'INCLUDING' | grep -v 'BUILDING' | grep -v 'Skipping' | jq -r '.objectChanges[] | select(.type == "published") | .packageId')

if [ -z "$PACKAGE_ID" ] || [ "$PACKAGE_ID" == "null" ]; then
    echo "Error: Failed to publish Sui package or extract Package ID. Is your Devnet environment configured correctly?"
    echo "$PUBLISH_JSON"
    exit 1
fi
echo "Published Lightning Package ID: $PACKAGE_ID"

echo "[2.8/7] Starting Alice and Bob LND nodes..."

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
    --noseedbackup \
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
    --noseedbackup \
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
echo "[3/7] Generating address and requesting Sui Faucet for Alice..."
ALICE_ADDR=$($ALICE_CLI newaddress p2wkh | jq -r '.address')
echo "Alice Address: $ALICE_ADDR"

# Assuming local faucet is running. If interacting with public testnet, we'd use 'sui client faucet'
if [ -n "$FAUCET_URL" ]; then
    sui client faucet --url "$FAUCET_URL" --address "$ALICE_ADDR" || true
    sleep 5
    # Call faucet a second time so Alice has TWO coins (one for funding, one for gas)
    sui client faucet --url "$FAUCET_URL" --address "$ALICE_ADDR" || true
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
echo "[4/7] Connecting Alice to Bob..."
BOB_PUBKEY=$($BOB_CLI getinfo | jq -r '.identity_pubkey')
echo "Bob Pubkey: $BOB_PUBKEY"

# Fund Bob with SUI so he has gas for close transactions
BOB_ADDR=$($BOB_CLI newaddress p2wkh | jq -r '.address')
echo "Bob Address: $BOB_ADDR"
if [ -n "$FAUCET_URL" ]; then
    sui client faucet --url "$FAUCET_URL" --address "$BOB_ADDR" || true
else
    sui client faucet --address "$BOB_ADDR" || true
fi
sleep 5

$ALICE_CLI connect "${BOB_PUBKEY}@127.0.0.1:${BOB_PORT}"
sleep 5

# Wait for Alice's nodes to fully propagate coins
echo "Double checking gas object propagation..."
sleep 10

# 5. Opening Channel
echo "[5/7] Alice opening channel to Bob..."
$ALICE_CLI openchannel --node_key=$BOB_PUBKEY --local_amt=10000000

echo "Waiting for channel to open..."
sleep 10

# 6. Verification
echo "[6/7] Verifying Channel..."
$ALICE_CLI pendingchannels
$ALICE_CLI listchannels

# 7. Payment Test
echo "[7/9] Testing Payment (Alice -> Bob)..."
INVOICE=$($BOB_CLI addinvoice --amt=200000 --memo="itest-sui-payment" | jq -r '.payment_request')
echo "Bob Invoice: $INVOICE"
$ALICE_CLI payinvoice --pay_req="$INVOICE" --force

echo "[8/9] Testing Reverse Payment (Bob -> Alice)..."
INVOICE2=$($ALICE_CLI addinvoice --amt=500 --memo="reverse-sui-payment" | jq -r '.payment_request')
echo "Alice Invoice: $INVOICE2"
$BOB_CLI payinvoice --pay_req="$INVOICE2" --force

echo "Alice Channel Balance:"
$ALICE_CLI channelbalance
echo "Bob Channel Balance:"
$BOB_CLI channelbalance

# 8. Cooperative Channel Closure
echo "[9/9] Testing Cooperative and Force Channel Closures..."
echo "Closing first channel cooperatively..."
CHAN_POINT=$($ALICE_CLI listchannels | jq -r '.channels[0].channel_point')
TXID=$(echo $CHAN_POINT | cut -d':' -f1)
OUT_INDEX=$(echo $CHAN_POINT | cut -d':' -f2)

# Start cooperative close stream in background
$ALICE_CLI closechannel $TXID $OUT_INDEX > /tmp/coop_close.log &
echo "Waiting 10s for cooperative close to settle on chain..."
sleep 10

echo "Funding Alice for the second channel (Force Close test)..."
if [ -n "$FAUCET_URL" ]; then
    sui client faucet --url "$FAUCET_URL" --address "$ALICE_ADDR" || true
else
    sui client faucet --address "$ALICE_ADDR" || true
fi
sleep 5

echo "Alice opening second channel to Bob..."
$ALICE_CLI openchannel --node_key=$BOB_PUBKEY --local_amt=5000000
sleep 10
CHAN_POINT2=$($ALICE_CLI listchannels | jq -r '.channels[0].channel_point')
if [ "$CHAN_POINT2" == "null" ] || [ -z "$CHAN_POINT2" ]; then
    echo "Warning: Second channel failed to open or sync. Delaying."
    sleep 10
    CHAN_POINT2=$($ALICE_CLI listchannels | jq -r '.channels[0].channel_point')
fi
TXID2=$(echo "$CHAN_POINT2" | cut -d':' -f1)
OUT_INDEX2=$(echo "$CHAN_POINT2" | cut -d':' -f2)

echo "Alice force closing second channel..."
$ALICE_CLI closechannel --force $TXID2 $OUT_INDEX2 > /tmp/force_close.log &
echo "Waiting 10s for force close to register..."
sleep 10

echo "Checking final node states:"
$ALICE_CLI pendingchannels
$ALICE_CLI listchannels
$ALICE_CLI pendingchannels

echo "=== Sui LND Integration Test SUCCESS ==="
exit 0
