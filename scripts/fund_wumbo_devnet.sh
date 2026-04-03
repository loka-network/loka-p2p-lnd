#!/bin/bash
# ---------------------------------------------------------------------------
# fund_wumbo_devnet.sh
# 
# A script to repeatedly request Sui from the Devnet faucet to accumulate
# enough SUI for Wumbo (Large) Channel testing without manual clicking.
# ---------------------------------------------------------------------------

# Check if sui CLI is installed
if ! command -v sui &> /dev/null; then
    echo "❌ Error: 'sui' command is not found. Please install the Sui CLI first."
    echo ""
    echo "🛠️  Installation commands:"
    echo "  - macOS/Linux (Homebrew):   brew install sui"
    echo "  - Rust/Cargo:               cargo install --locked --git https://github.com/MystenLabs/sui.git --branch testnet sui"
    echo ""
    exit 1
fi

# Ensure devnet and testnet are configured
if ! sui client envs | grep -q "devnet" ; then
    echo "⚙️ Configuring devnet environment..."
    sui client new-env --alias devnet --rpc https://fullnode.devnet.sui.io:443 2>/dev/null || true
fi

if ! sui client envs | grep -q "testnet" ; then
    echo "⚙️ Configuring testnet environment..."
    sui client new-env --alias testnet --rpc https://fullnode.testnet.sui.io:443 2>/dev/null || true
fi

# Flexible argument parsing to optionally take network first
if [ "$1" == "devnet" ] || [ "$1" == "testnet" ]; then
    ENV=$1
    INPUT_ADDRESS=$2
    TIMES=${3:-5}
    echo "🔄 Switching Sui client environment to: $ENV"
    sui client switch --env "$ENV" 2>/dev/null || true
else
    ENV=$(sui client active-env 2>/dev/null || echo "devnet")
    INPUT_ADDRESS=$1
    TIMES=${2:-5}
fi

# Get address from argument or fallback to active sui client address
ADDRESS=${INPUT_ADDRESS:-$(sui client active-address)}
ADDRESS=$(echo "$ADDRESS" | tr -d '\r\n[:space:]') # Strip control characters to fix JSON parsing

if [ -z "$ADDRESS" ]; then
    echo "❌ Error: Could not determine SUI address."
    echo "Usage: ./scripts/fund_wumbo_devnet.sh [devnet|testnet] <SUI_ADDRESS> [TIMES]"
    exit 1
fi

echo "======================================================"
echo "🌊 Starting ($ENV) Wumbo Faucet for: $ADDRESS"
echo "Fetching $TIMES batches of SUI..."
echo "======================================================"

for ((i=1; i<=TIMES; i++)); do
    echo "⏳ [Batch $i/$TIMES] Requesting funds via native CLI..."
    
    # Use native CLI to automatically handle api-versioning/V2 paths
    if sui client faucet --address "$ADDRESS"; then
        echo "✅ Success! Sent to address."
    else
        echo "⚠️ Rate Limited or error occurred. (Waiting longer...)"
        sleep 20
    fi

    # Crucial sleep to bypass strict anti-spam rate limiting on Sui nodes
    if [ $i -lt $TIMES ]; then
        echo "Zzz... sleeping 15 seconds to avoid API ban..."
        sleep 15
    fi
done

echo ""
echo "🎉 Wumbo Funding sequence complete!"
echo "Check your new balance with: sui client gas"
