#!/bin/bash
# itest_sui_sendcoins_verify.sh
#
# Companion to itest_sui_single_coin.sh: verifies that the lnrpc SendCoins
# RPC, when running on a sui-settling lnd, accepts a 0x-prefixed sui address
# and submits a sui_pay transaction on chain. Run this AFTER the main itest
# has reached its "Suspended Mode" prompt — at that point Alice and Bob are
# alive at their respective rpc/macaroon paths and have funded SUI coins.
#
# Usage:
#   ./scripts/itest_sui_sendcoins_verify.sh
#
# Override SEND_AMT_MIST (default 50000000 = 0.05 SUI) if you want a
# different amount.

set -e

LNCLI_BIN="${LNCLI_BIN:-./lncli-debug}"

ALICE_DIR="${ALICE_DIR:-/tmp/lnd-sui-test/alice}"
BOB_DIR="${BOB_DIR:-/tmp/lnd-sui-test/bob}"
ALICE_RPC="${ALICE_RPC:-10009}"
BOB_RPC="${BOB_RPC:-10010}"

SEND_AMT_MIST="${SEND_AMT_MIST:-50000000}"  # 0.05 SUI

ALICE_CLI="$LNCLI_BIN --lnddir=$ALICE_DIR --rpcserver=localhost:$ALICE_RPC --macaroonpath=$ALICE_DIR/data/chain/sui/devnet/admin.macaroon"
BOB_CLI="$LNCLI_BIN --lnddir=$BOB_DIR --rpcserver=localhost:$BOB_RPC --macaroonpath=$BOB_DIR/data/chain/sui/devnet/admin.macaroon"

if [ ! -f "$LNCLI_BIN" ]; then
    echo "Error: $LNCLI_BIN not found. Run 'make build' first."
    exit 1
fi
if ! $ALICE_CLI getinfo &>/dev/null; then
    echo "Error: Alice ($ALICE_RPC) is not reachable. Did you run itest_sui_single_coin.sh first?"
    exit 1
fi
if ! $BOB_CLI getinfo &>/dev/null; then
    echo "Error: Bob ($BOB_RPC) is not reachable. Did you run itest_sui_single_coin.sh first?"
    exit 1
fi

echo "=== SendCoins-to-sui-address verification ==="

# Confirm both nodes report chain=sui via GetInfo. This is the same flag
# downstream apps (robosats/lnbits) use to decide whether to route to sui.
ALICE_CHAIN=$($ALICE_CLI getinfo | jq -r '.chains[0].chain')
BOB_CHAIN=$($BOB_CLI getinfo | jq -r '.chains[0].chain')
echo "Alice chain: $ALICE_CHAIN   Bob chain: $BOB_CHAIN"
if [ "$ALICE_CHAIN" != "sui" ] || [ "$BOB_CHAIN" != "sui" ]; then
    echo "❌ Expected both nodes to report chain=sui, got Alice=$ALICE_CHAIN Bob=$BOB_CHAIN"
    exit 1
fi

BOB_ADDR=$($BOB_CLI newaddress p2wkh | jq -r '.address')
echo "Bob sui address: $BOB_ADDR"

BOB_BAL_BEFORE=$($BOB_CLI walletbalance | jq -r '.confirmed_balance')
echo "Bob balance before: $BOB_BAL_BEFORE MIST"

echo "Alice → Bob sendcoins (amt=$SEND_AMT_MIST MIST)..."
SENDCOINS_JSON=$($ALICE_CLI sendcoins --addr "$BOB_ADDR" --amt "$SEND_AMT_MIST")
echo "$SENDCOINS_JSON"
TXID=$(echo "$SENDCOINS_JSON" | jq -r '.txid')
if [ -z "$TXID" ] || [ "$TXID" = "null" ]; then
    echo "❌ sendcoins returned no txid"
    exit 1
fi
echo "Sui tx digest: $TXID"

echo "Waiting 15s for sui finality + GetCoins refresh on Bob..."
sleep 15

BOB_BAL_AFTER=$($BOB_CLI walletbalance | jq -r '.confirmed_balance')
echo "Bob balance after: $BOB_BAL_AFTER MIST"

DELTA=$((BOB_BAL_AFTER - BOB_BAL_BEFORE))
echo "Delta: $DELTA MIST"
if [ "$DELTA" -lt "$SEND_AMT_MIST" ]; then
    echo "❌ Bob's balance did not increase by at least $SEND_AMT_MIST MIST"
    exit 1
fi

echo "✓ sui sendcoins verified — Bob received $DELTA MIST"
