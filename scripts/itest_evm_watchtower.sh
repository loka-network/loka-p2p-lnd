#!/usr/bin/env bash
# itest_evm_watchtower.sh — end-to-end EVM watchtower breach test on anvil.
#
# Starts anvil, deploys MockERC20 + ChannelManager, mints USDC to the deployer,
# then runs the build-tagged Go integration test (TestAnvilWatchtowerBreach),
# which drives the full H-1 loop against the live chain: a channel is
# force-closed with a revoked state and the tower penalizes on the offline
# victim's behalf, sweeping the whole escrow to the victim.
#
# Requires: anvil, forge, cast (Foundry), go.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
RPC_PORT=18545
RPC="http://127.0.0.1:${RPC_PORT}"
# Anvil's well-known dev account 0 (local devnet only).
ACCT0=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80

ANVIL_PID=""
cleanup() {
    [ -n "$ANVIL_PID" ] && kill "$ANVIL_PID" 2>/dev/null || true
}
trap cleanup EXIT

echo "=== starting anvil"
anvil --port "$RPC_PORT" --block-time 1 --silent &
ANVIL_PID=$!

# Wait for anvil to accept RPC.
for _ in $(seq 1 30); do
    if cast block-number --rpc-url "$RPC" >/dev/null 2>&1; then
        break
    fi
    sleep 0.5
done

echo "=== deploying MockERC20 + ChannelManager"
( cd "$REPO/evm-contracts/channel-manager" &&
    PRIVATE_KEY=$ACCT0 CHALLENGE_PERIOD=60 ./deploy.sh anvil "$RPC" )

STATE="$REPO/evm-contracts/channel-manager/deploy_state_anvil.json"
TOKEN=$(python3 -c "import json;print(json.load(open('$STATE'))['token'])")
CM=$(python3 -c "import json;print(json.load(open('$STATE'))['channel_manager'])")
DEPLOYER_ADDR=$(cast wallet address --private-key "$ACCT0")

echo "=== minting USDC to the deployer ($DEPLOYER_ADDR)"
cast send "$TOKEN" "mint(address,uint256)" "$DEPLOYER_ADDR" 1000000000 \
    --private-key "$ACCT0" --rpc-url "$RPC" >/dev/null

echo "=== token=$TOKEN manager=$CM"
echo "=== running watchtower breach integration test"
cd "$REPO"
EVMTOWER_RPC="$RPC" \
EVMTOWER_CONTRACT="$CM" \
EVMTOWER_TOKEN="$TOKEN" \
EVMTOWER_DEPLOYER_KEY="$ACCT0" \
    GOWORK=off GOTOOLCHAIN=auto go test -tags evmtower_itest -count=1 -v \
    -run TestAnvilWatchtowerBreach ./watchtower/evmtower/
