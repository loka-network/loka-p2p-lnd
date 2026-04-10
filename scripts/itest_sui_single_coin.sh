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
    --protocol.wumbo-channels \
    --protocol.no-anchors \
    --noseedbackup \
    --maxpendingchannels=10 \
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
echo "[4/7] Connecting Alice to Bob..."
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

echo "[5/7] Bob opening channel to Alice (Native Single-Coin PTB Test)..."
# Bob natively holds exactly 1 coin drop transferred dynamically from the root script CLI.
BOB_TOTAL_BAL=$($BOB_CLI walletbalance | jq -r '.confirmed_balance')
# Force local_amt to 100 SUI (100,000,000,000 MIST) so it intrinsically fits completely 
# inside a single Faucet UTXO object (200 SUI) without demanding Knapsack merges!
BOB_LOCAL_AMT=5000000000
$BOB_CLI openchannel --node_key=$ALICE_PUBKEY --local_amt=$BOB_LOCAL_AMT --push_amt=100000000
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

# 6. Verification
echo "[6/7] Verifying Channel..."
$ALICE_CLI pendingchannels
$ALICE_CLI listchannels

# 7. Payment Test
echo "[7/7] Testing Lightning Routing (Bob -> Alice)..."
INVOICE=$($ALICE_CLI addinvoice --amt=1000 --memo="single-coin-test" | jq -r '.payment_request')
echo "Alice Invoice: $INVOICE"
$BOB_CLI payinvoice --pay_req="$INVOICE" --force

echo "Bob Channel Balance post-payment:"
$BOB_CLI channelbalance

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
