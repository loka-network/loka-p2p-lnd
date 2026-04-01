#!/bin/bash
# ---------------------------------------------------------------------------
# fund_wumbo_devnet.sh
# 
# A script to repeatedly request Sui from the Devnet faucet to accumulate
# enough SUI for Wumbo (Large) Channel testing without manual clicking.
# ---------------------------------------------------------------------------

# Get address from argument or fallback to active sui client address
ADDRESS=${1:-$(sui client active-address)}
if [ -z "$ADDRESS" ]; then
    echo "❌ Error: Could not determine SUI address."
    echo "Usage: ./scripts/fund_wumbo_devnet.sh <SUI_ADDRESS>"
    exit 1
fi

# Define how many times to request (default: 5)
TIMES=${2:-5}

echo "======================================================"
echo "🌊 Starting Devnet Wumbo Faucet for: $ADDRESS"
echo "Fetching $TIMES batches of SUI..."
echo "======================================================"

for ((i=1; i<=TIMES; i++)); do
    echo "⏳ [Batch $i/$TIMES] Requesting funds..."
    
    # Use direct curl to bypass sui client constraints
    RESPONSE=$(curl --silent --location --request POST 'https://faucet.devnet.sui.io/gas' \
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
