#!/usr/bin/env bash
#
# evm_faucet_base_sepolia.sh — top up a Base Sepolia address from the Coinbase
# CDP faucet by looping claims until a target ETH balance is reached.
#
# The CDP faucet endpoint authenticates with the portal session, so this
# script needs the auth material copied from a live browser request
# (DevTools -> the faucet POST -> Copy as cURL). Provide it via env:
#
#   CDP_COOKIE   the full value of the request's `cookie:` header
#   CDP_AUTH     the `authorization:` header value, if the request has one
#                (optional — some sessions authenticate by cookie alone)
#
# Usage:
#   CDP_COOKIE='...' scripts/evm_faucet_base_sepolia.sh [recipient] [target_eth]
#
# Defaults: recipient = the deployer in deploy_state_base-sepolia.json,
# target = 0.05 ETH. The CDP faucet drips ~0.0001 ETH/claim and rate-limits,
# so reaching a large target can take many spaced attempts; the script stops
# early once the target is met and backs off on 429/rate-limit responses.
set -euo pipefail

RPC="${EVM_RPC:-https://base-sepolia-rpc.publicnode.com}"
PROJECT_ID="c53a32be-962e-491a-9161-3ac673adbf93"
URL="https://cloud-api.coinbase.com/platform/projects/${PROJECT_ID}/v2/evm/faucet"

RECIPIENT="${1:-0x62663b3804a535f55DDc85a0ebfC6427DbeD7883}"
TARGET_ETH="${2:-0.05}"
MAX_CLAIMS="${MAX_CLAIMS:-200}"
CLAIM_GAP="${CLAIM_GAP:-3}"   # seconds between claims

if [ -z "${CDP_COOKIE:-}" ] && [ -z "${CDP_AUTH:-}" ]; then
    echo "error: set CDP_COOKIE (and/or CDP_AUTH) from a live browser request" >&2
    echo "       DevTools -> faucet POST -> Copy as cURL -> grab cookie/auth" >&2
    exit 1
fi

# Target balance in wei.
target_wei=$(python3 -c "print(int(float('$TARGET_ETH') * 10**18))")

bal_wei() { cast balance --rpc-url "$RPC" "$RECIPIENT" 2>/dev/null || echo 0; }

claim() {
    local args=(-sS -o /tmp/cdp_faucet_resp.json -w '%{http_code}'
        -X POST "$URL"
        -H 'accept: application/json'
        -H 'content-type: application/json'
        -H 'cdp-entity-id: entity_3bb20094-2d24-55dd-a5b4-76e854855286'
        -H 'cdp-organization-id: b8a40fb2-ba67-4d33-9c90-bf9e081f70e2'
        -H "cdp-project-id: ${PROJECT_ID}"
        -H 'origin: https://portal.cdp.coinbase.com'
        -H 'referer: https://portal.cdp.coinbase.com/'
        --data "{\"network\":\"base-sepolia\",\"address\":\"$RECIPIENT\",\"token\":\"eth\"}")
    [ -n "${CDP_COOKIE:-}" ] && args+=(-H "cookie: ${CDP_COOKIE}")
    [ -n "${CDP_AUTH:-}" ] && args+=(-H "authorization: ${CDP_AUTH}")
    curl "${args[@]}"
}

echo "recipient : $RECIPIENT"
echo "target    : $TARGET_ETH ETH"
echo "start bal : $(cast balance --ether --rpc-url "$RPC" "$RECIPIENT" 2>/dev/null || echo '?') ETH"
echo

for i in $(seq 1 "$MAX_CLAIMS"); do
    cur=$(bal_wei)
    if python3 -c "import sys; sys.exit(0 if int('$cur') >= $target_wei else 1)"; then
        echo "target reached: $(cast balance --ether --rpc-url "$RPC" "$RECIPIENT") ETH"
        exit 0
    fi

    code=$(claim || echo 000)
    resp=$(cat /tmp/cdp_faucet_resp.json 2>/dev/null || true)
    case "$code" in
    2*)
        tx=$(printf '%s' "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('transactionHash',''))" 2>/dev/null || true)
        echo "claim $i: ok ${tx:+tx=$tx}"
        ;;
    401|403)
        echo "claim $i: auth rejected (HTTP $code) — cookie/token expired, re-copy from browser" >&2
        echo "$resp" >&2
        exit 1
        ;;
    429)
        echo "claim $i: rate-limited (429), backing off 30s" >&2
        sleep 30
        continue
        ;;
    *)
        echo "claim $i: HTTP $code — $resp" >&2
        ;;
    esac
    sleep "$CLAIM_GAP"
done

echo "stopped after $MAX_CLAIMS claims; balance now $(cast balance --ether --rpc-url "$RPC" "$RECIPIENT") ETH" >&2
exit 1
