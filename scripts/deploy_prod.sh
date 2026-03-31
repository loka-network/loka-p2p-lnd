#!/bin/bash
# scripts/deploy_prod.sh
# Production & testnet deploy script for Sui Lightning Contracts.
# Maintains UpgradeCap object to support contract upgrades.

set -e

NETWORK="${1:-testnet}"
MOVE_PKG="./sui-contracts/lightning"
DEPLOY_STATE_FILE="$MOVE_PKG/deploy_state_${NETWORK}.json"

echo "=== Sui Contract Production Deployer ==="
echo "Target Network: $NETWORK"

# Switch to the target environment
sui client switch --env "$NETWORK" || echo "Note: make sure you have the env $NETWORK configured in your sui client."

if [ -f "$DEPLOY_STATE_FILE" ]; then
    UPGRADE_CAP=$(jq -r '.upgrade_cap' "$DEPLOY_STATE_FILE")
    OLD_PACKAGE_ID=$(jq -r '.package_id' "$DEPLOY_STATE_FILE")
    
    echo "Found existing deployment state."
    echo "Previous Package ID: $OLD_PACKAGE_ID"
    echo "UpgradeCap ID:       $UPGRADE_CAP"
    echo "Executing upgrade..."
    
    # Execute the upgrade command
    UPGRADE_JSON=$(sui client upgrade --upgrade-capability "$UPGRADE_CAP" --gas-budget 100000000 --json "$MOVE_PKG")
    
    # Extract the newly published package ID
    NEW_PACKAGE_ID=$(echo "$UPGRADE_JSON" | jq -r '.objectChanges[]? | select(.type == "published") | .packageId')
    
    if [ -z "$NEW_PACKAGE_ID" ] || [ "$NEW_PACKAGE_ID" == "null" ]; then
        echo "Error: Upgrade failed or no new package ID returned."
        echo "Details:"
        echo "$UPGRADE_JSON"
        exit 1
    fi
    
    echo "✅ Successfully upgraded to New Package ID: $NEW_PACKAGE_ID"
    
    # Update the deployment state with the new package ID (UpgradeCap remains the same)
    cat <<EOF > "$DEPLOY_STATE_FILE"
{
  "package_id": "$NEW_PACKAGE_ID",
  "upgrade_cap": "$UPGRADE_CAP"
}
EOF

else
    echo "No existing deployment found. Initiating first-time publish..."
    
    # Publish the package
    PUBLISH_JSON=$(sui client publish --gas-budget 100000000 --json "$MOVE_PKG")
    
    # Extract Package ID
    PACKAGE_ID=$(echo "$PUBLISH_JSON" | jq -r '.objectChanges[]? | select(.type == "published") | .packageId')
    
    # Extract UpgradeCap object ID automatically given to the publisher
    UPGRADE_CAP=$(echo "$PUBLISH_JSON" | jq -r '.objectChanges[]? | select(.type == "created" and (.objectType | contains("::package::UpgradeCap"))) | .objectId')
    
    if [ -z "$PACKAGE_ID" ] || [ "$PACKAGE_ID" == "null" ]; then
        echo "Error: Publish failed or no package ID returned."
        echo "Details:"
        echo "$PUBLISH_JSON"
        exit 1
    fi
    
    if [ -z "$UPGRADE_CAP" ] || [ "$UPGRADE_CAP" == "null" ]; then
        echo "Warning: Could not capture UpgradeCap ID! Upgrades may not be possible."
    fi
    
    echo "✅ Successfully published! "
    echo "   Package ID:  $PACKAGE_ID"
    echo "   UpgradeCap:  $UPGRADE_CAP"
    
    # Save the deployment state
    cat <<EOF > "$DEPLOY_STATE_FILE"
{
  "package_id": "$PACKAGE_ID",
  "upgrade_cap": "$UPGRADE_CAP"
}
EOF
fi

echo "Done."
