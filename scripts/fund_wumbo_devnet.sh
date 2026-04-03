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

# Route to correct Faucet
if [ "$ENV" == "testnet" ]; then
    FAUCET_URL="https://faucet.testnet.sui.io/gas"
else
    FAUCET_URL="https://faucet.devnet.sui.io/gas"
fi

echo "======================================================"
echo "🌊 Starting ($ENV) Wumbo Faucet for: $ADDRESS"
echo "Fetching $TIMES batches of SUI..."
echo "======================================================"

for ((i=1; i<=TIMES; i++)); do
    echo "⏳ [Batch $i/$TIMES] Requesting funds..."
    
    # Use direct curl to bypass sui client constraints
    RESPONSE=$(curl --silent --location --request POST "$FAUCET_URL" \
    --header 'Content-Type: application/json' \
    --data-raw "{
        \"FixedAmountRequest\": {
            \"recipient\": \"$ADDRESS\"
        }
    }")

    # Check if the response contains "error":null (meaning success)
    if echo "$RESPONSE" | grep -q '"error":null'; then
        echo "✅ Success! Sent to address."
    elif echo "$RESPONSE" | grep -q 'TooManyRequests'; then
        echo "⚠️ Rate Limited! (Sui faucet is temporarily blocking your IP, waiting longer...)"
        sleep 20
    else
        echo "❌ Failed to fetch: $RESPONSE"
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
