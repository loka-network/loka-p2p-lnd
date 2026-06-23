#!/usr/bin/env bash
# itest_evm_watchtower.sh [anvil|base-sepolia] — end-to-end EVM watchtower
# breach test (H-1). Drives the full loop against a chain + deployed
# ChannelManager: a channel is force-closed with a revoked state and the tower
# penalizes on the offline victim's behalf, sweeping the whole escrow to them.
#
#   anvil (default)  starts a local anvil, deploys MockERC20 + ChannelManager
#                    fresh, mints USDC, runs with generous (1 ETH) gas funding.
#   base-sepolia     uses the deployed ChannelManager from
#                    deploy_state_base-sepolia.json (must be the H-1-fixed
#                    build — redeploy if stale), a funded PRIVATE_KEY (or
#                    /tmp/evm_itest_pk), small gas funding, and EVM_RPC
#                    (default https://sepolia.base.org).
#
# Requires: anvil/forge/cast (Foundry), go. The Go test
# (TestAnvilWatchtowerBreach) is build-tagged evmtower_itest.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
NETWORK="${1:-anvil}"
ACCT0=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80

ANVIL_PID=""
cleanup() { [ -n "$ANVIL_PID" ] && kill "$ANVIL_PID" 2>/dev/null || true; }
trap cleanup EXIT

case "$NETWORK" in
anvil)
    RPC="http://127.0.0.1:18545"
    KEY=$ACCT0
    GAS_WEI=1000000000000000000 # 1 ETH

    echo "=== starting anvil"
    anvil --port 18545 --block-time 1 --silent &
    ANVIL_PID=$!
    for _ in $(seq 1 30); do
        cast block-number --rpc-url "$RPC" >/dev/null 2>&1 && break
        sleep 0.5
    done

    echo "=== deploying MockERC20 + ChannelManager"
    ( cd "$REPO/evm-contracts/channel-manager" &&
        PRIVATE_KEY=$KEY CHALLENGE_PERIOD=60 ./deploy.sh anvil "$RPC" )
    STATE="$REPO/evm-contracts/channel-manager/deploy_state_anvil.json"
    ;;

base-sepolia)
    RPC="${EVM_RPC:-https://sepolia.base.org}"
    KEY="${PRIVATE_KEY:-$(cat /tmp/evm_itest_pk 2>/dev/null || true)}"
    [ -n "$KEY" ] || { echo "set PRIVATE_KEY (funded base-sepolia key)" >&2; exit 1; }
    # Small gas funding (~0.0003 ETH each) — base-sepolia gas is ~0.006 gwei.
    GAS_WEI=300000000000000
    STATE="$REPO/evm-contracts/channel-manager/deploy_state_base-sepolia.json"
    [ -s "$STATE" ] || { echo "missing $STATE (deploy the H-1-fixed contract first)" >&2; exit 1; }
    ;;

*)
    echo "usage: $0 [anvil|base-sepolia]" >&2
    exit 1
    ;;
esac

TOKEN=$(python3 -c "import json;print(json.load(open('$STATE'))['token'])")
CM=$(python3 -c "import json;print(json.load(open('$STATE'))['channel_manager'])")
DEPLOYER_ADDR=$(cast wallet address --private-key "$KEY")

echo "=== minting USDC to the deployer ($DEPLOYER_ADDR)"
cast send "$TOKEN" "mint(address,uint256)" "$DEPLOYER_ADDR" 200000000 \
    --private-key "$KEY" --rpc-url "$RPC" >/dev/null

echo "=== network=$NETWORK token=$TOKEN manager=$CM"
echo "=== running watchtower breach integration test"
cd "$REPO"
EVMTOWER_RPC="$RPC" \
EVMTOWER_CONTRACT="$CM" \
EVMTOWER_TOKEN="$TOKEN" \
EVMTOWER_DEPLOYER_KEY="$KEY" \
EVMTOWER_GAS_WEI="$GAS_WEI" \
    GOWORK=off GOTOOLCHAIN=auto go test -tags evmtower_itest -count=1 -v \
    -timeout 5m -run TestAnvilWatchtowerBreach ./watchtower/evmtower/
