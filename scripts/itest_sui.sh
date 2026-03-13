#!/bin/bash
# itest_sui.sh
# End-to-end integration test for LND running on the Sui blockchain backend.

set -e

# Configuration
LND_BIN="./lnd-debug"
LNCLI_BIN="./lncli-debug"
SUI_CMD="sui"

ALICE_DIR="/tmp/lnd-sui-test/alice"
BOB_DIR="/tmp/lnd-sui-test/bob"
ALICE_PORT=10011
BOB_PORT=10012
ALICE_REST=8081
BOB_REST=8082
ALICE_RPC=10009
BOB_RPC=10010

echo "=== Sui LND Integration Test ==="

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

echo "[2/7] Starting Alice and Bob LND nodes..."

# Start Alice
$LND_BIN \
    --lnddir="$ALICE_DIR" \
    --listen="127.0.0.1:$ALICE_PORT" \
    --rpclisten="127.0.0.1:$ALICE_RPC" \
    --restlisten="127.0.0.1:$ALICE_REST" \
    --suinode.active \
    --suinode.devnet \
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
    --noseedbackup \
    > "$BOB_DIR/lnd.log" 2>&1 &
BOB_PID=$!

# Function to clean up background processes on exit
cleanup() {
    echo "Saving Bob's log to .bob_lnd.log..."
    cp "$BOB_DIR/lnd.log" .bob_lnd.log 2>/dev/null || true
    echo "Saving Alice's log to .alice_lnd.log..."
    cp "$ALICE_DIR/lnd.log" .alice_lnd.log 2>/dev/null || true
    echo "Cleaning up LND nodes..."
    kill $ALICE_PID $BOB_PID 2>/dev/null || true
    wait $ALICE_PID $BOB_PID 2>/dev/null || true
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
sui client faucet --address "$ALICE_ADDR"
sleep 5 # Wait for faucet tx

echo "Checking Alice's wallet balance..."
$ALICE_CLI walletbalance

# 4. Connecting Alice to Bob
echo "[4/7] Connecting Alice to Bob..."
BOB_PUBKEY=$($BOB_CLI getinfo | jq -r '.identity_pubkey')
echo "Bob Pubkey: $BOB_PUBKEY"

$ALICE_CLI connect "${BOB_PUBKEY}@127.0.0.1:${BOB_PORT}"
sleep 2

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
echo "[7/7] Testing Payment (Alice -> Bob)..."
INVOICE=$($BOB_CLI addinvoice --amt=1000 --memo="itest-sui-payment" | jq -r '.payment_request')
echo "Bob Invoice: $INVOICE"
$ALICE_CLI payinvoice --pay_req="$INVOICE" --force

echo "=== Sui LND Integration Test SUCCESS ==="
exit 0
