#!/usr/bin/env bash
# deploy.sh — deploy the ChannelManager (and a MockERC20 when no real token is
# given) and record the result in deploy_state_<network>.json, the EVM
# analogue of sui-contracts/lightning/deploy_state_*.json. The state file is
# the canonical place for other applications / node operators to look up the
# contract addresses of a sub-network, instead of fishing them out of forge's
# broadcast logs.
#
# Usage:
#   PRIVATE_KEY=0x… ./deploy.sh <network> <rpc-url> [token-address]
#
#   <network>       label baked into the state-file name (anvil, base-sepolia,
#                   base, …)
#   <rpc-url>       JSON-RPC endpoint to deploy through
#   [token-address] existing ERC20 to escrow (e.g. canonical USDC). When
#                   omitted a MockERC20 with a public mint is deployed —
#                   local/testnet only.
#
# Env:
#   PRIVATE_KEY         deployer key (required, needs gas on the target chain)
#   CHALLENGE_PERIOD    force-close challenge window in seconds (default 86400).
#                       The floor when deposit-scaling is enabled.
#   MAX_CHALLENGE_PERIOD  cap of the scaled window in seconds (default 0).
#   FULL_SCALE_DEPOSIT  deposit (token base units) at which the window reaches
#                       the cap (default 0). 0 in either of the two vars above
#                       disables scaling → fixed CHALLENGE_PERIOD per channel.
#
# Mainnet recommended preset (USDC, 6 decimals) — deposit-scaled window:
#   floor 1 day → cap 7 days, hitting the cap at 100,000 USDC. Small channels
#   get ~1 day, a 50k channel ~3.6 days, a 100k+ channel the full 7 days. Bump
#   the cap to 1209600 (14 days) for a very-high-value deployment.
#       PRIVATE_KEY=0x<key> \
#         CHALLENGE_PERIOD=86400 \
#         MAX_CHALLENGE_PERIOD=604800 \
#         FULL_SCALE_DEPOSIT=100000000000 \
#         ./deploy.sh base <rpc-url> <usdc-token-address>
# The defaults below keep scaling OFF (fixed CHALLENGE_PERIOD) so testnet /
# itest deploys are unaffected unless the two vars are passed explicitly.
set -euo pipefail

NETWORK=${1:?usage: PRIVATE_KEY=0x… ./deploy.sh <network> <rpc-url> [token-address]}
RPC=${2:?missing rpc-url}
TOKEN=${3:-}
: "${PRIVATE_KEY:?set PRIVATE_KEY to the deployer key}"
CHALLENGE_PERIOD=${CHALLENGE_PERIOD:-86400}
MAX_CHALLENGE_PERIOD=${MAX_CHALLENGE_PERIOD:-0}
FULL_SCALE_DEPOSIT=${FULL_SCALE_DEPOSIT:-0}

cd "$(dirname "$0")"

if [ -z "$TOKEN" ]; then
    OUT=$(PRIVATE_KEY=$PRIVATE_KEY forge script script/DeployMockToken.s.sol \
        --rpc-url "$RPC" --broadcast 2>/dev/null)
    TOKEN=$(echo "$OUT" | grep -o 'Deployed MockERC20.*0x[0-9a-fA-F]*' \
        | grep -o '0x[0-9a-fA-F]*')
    [ -n "$TOKEN" ] || { echo "MockERC20 deployment failed" >&2; exit 1; }
fi

OUT=$(PRIVATE_KEY=$PRIVATE_KEY TOKEN_ADDRESS=$TOKEN \
    CHALLENGE_PERIOD=$CHALLENGE_PERIOD \
    MAX_CHALLENGE_PERIOD=$MAX_CHALLENGE_PERIOD \
    FULL_SCALE_DEPOSIT=$FULL_SCALE_DEPOSIT forge script script/Deploy.s.sol \
    --rpc-url "$RPC" --broadcast 2>/dev/null)
CM=$(echo "$OUT" | grep -o 'Deployed ChannelManager to: 0x[0-9a-fA-F]*' \
    | grep -o '0x[0-9a-fA-F]*')
[ -n "$CM" ] || { echo "ChannelManager deployment failed (already deployed \
with the same CREATE2 salt+args? see deploy_state_${NETWORK}.json)" >&2; exit 1; }

CHAIN_ID=$(cast chain-id --rpc-url "$RPC")
BLOCK=$(cast block-number --rpc-url "$RPC")
DEPLOYER=$(cast wallet address --private-key "$PRIVATE_KEY")

STATE_FILE="deploy_state_${NETWORK}.json"
cat > "$STATE_FILE" <<JSON
{
  "network": "${NETWORK}",
  "chain_id": ${CHAIN_ID},
  "channel_manager": "${CM}",
  "token": "${TOKEN}",
  "challenge_period": ${CHALLENGE_PERIOD},
  "max_challenge_period": ${MAX_CHALLENGE_PERIOD},
  "full_scale_deposit": ${FULL_SCALE_DEPOSIT},
  "deployer": "${DEPLOYER}",
  "deploy_block": ${BLOCK}
}
JSON

echo "Deployment recorded in evm-contracts/channel-manager/${STATE_FILE}:"
cat "$STATE_FILE"
