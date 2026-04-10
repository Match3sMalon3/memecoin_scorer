#!/usr/bin/env bash
# set-helius-webhook.sh — register or update the Helius webhook URL.
#
# Prerequisites:
#   - PUBLIC_BASE_URL is set (the cloudflared public HTTPS URL)
#   - HELIUS_API_KEY is set (your Helius dashboard API key)
#   - HELIUS_ACCOUNT_ADDRESSES is set (comma-separated Solana addresses to watch)
#   - HELIUS_WEBHOOK_ID is set if updating an existing webhook
#
# Environment loading:
#   If a .env file exists in the repo root, it is sourced before variable
#   validation.  Variables already exported in the calling shell take
#   PRECEDENCE over .env values — .env only fills in what is missing.
#   This matches the expected operator flow: export PUBLIC_BASE_URL in your
#   shell, then run the script; .env supplies HELIUS_API_KEY silently.
#
# Usage via make (recommended):
#   export PUBLIC_BASE_URL=https://abc.trycloudflare.com
#   make set-helius-webhook
#
# Usage direct:
#   export PUBLIC_BASE_URL=https://abc.trycloudflare.com
#   scripts/set-helius-webhook.sh
#
# Usage (update existing):
#   export PUBLIC_BASE_URL=https://new-url.trycloudflare.com
#   export HELIUS_WEBHOOK_ID=existing-id
#   make set-helius-webhook
set -euo pipefail

# ---- Locate repo root (script may be called from any working directory) ----
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"

# ---- Auto-load .env, letting already-exported vars win ----
# We parse .env manually instead of using 'source' so that:
#   1. We never execute arbitrary shell code embedded in .env
#   2. Already-exported variables are NOT overwritten by .env values
if [[ -f "${ENV_FILE}" ]]; then
  while IFS= read -r line || [[ -n "$line" ]]; do
    # Skip blank lines and comments
    [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
    # Expect KEY=VALUE; skip malformed lines
    [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue
    var_name="${BASH_REMATCH[1]}"
    var_value="${BASH_REMATCH[2]}"
    # Only set if not already in the environment
    if [[ -z "${!var_name+set}" ]]; then
      export "${var_name}=${var_value}"
    fi
  done < "${ENV_FILE}"
fi

PUBLIC_BASE_URL="${PUBLIC_BASE_URL:-}"
API_KEY="${HELIUS_API_KEY:-}"
WEBHOOK_ID="${HELIUS_WEBHOOK_ID:-}"
SECRET="${HELIUS_WEBHOOK_SECRET:-}"
ACCOUNT_ADDRESSES="${HELIUS_ACCOUNT_ADDRESSES:-}"

# ---- Validate required variables ----

if [[ -z "${PUBLIC_BASE_URL}" ]]; then
  echo "ERROR: PUBLIC_BASE_URL is not set."
  echo "Start cloudflared first:  cloudflared tunnel --url http://localhost:8080"
  echo "Then set:  export PUBLIC_BASE_URL=https://<assigned>.trycloudflare.com"
  exit 1
fi

if [[ -z "${API_KEY}" ]]; then
  echo "ERROR: HELIUS_API_KEY is not set."
  echo "Run: make setup-live-env"
  exit 1
fi

if [[ -z "${ACCOUNT_ADDRESSES}" ]]; then
  echo "ERROR: HELIUS_ACCOUNT_ADDRESSES is not set."
  echo "At least one address is required for webhook registration."
  echo "Set it in .env or export it in your shell:"
  echo "  HELIUS_ACCOUNT_ADDRESSES=<address1>,<address2>,..."
  exit 1
fi

# ---- Build the JSON address array from comma-separated input ----
# Input:  "AddrA,AddrB, AddrC"
# Output: ["AddrA","AddrB","AddrC"]
ADDRESSES_JSON="["
first=1
IFS=',' read -ra addr_arr <<< "${ACCOUNT_ADDRESSES}"
for addr in "${addr_arr[@]}"; do
  # Trim surrounding whitespace
  addr="${addr#"${addr%%[![:space:]]*}"}"
  addr="${addr%"${addr##*[![:space:]]}"}"
  [[ -z "$addr" ]] && continue
  if [[ "$first" -eq 1 ]]; then
    ADDRESSES_JSON+="\"${addr}\""
    first=0
  else
    ADDRESSES_JSON+=",\"${addr}\""
  fi
done
ADDRESSES_JSON+="]"

# Guard: must have at least one address after parsing
if [[ "${ADDRESSES_JSON}" == "[]" ]]; then
  echo "ERROR: HELIUS_ACCOUNT_ADDRESSES parsed to an empty list."
  echo "Value was: ${ACCOUNT_ADDRESSES}"
  echo "Provide at least one valid Solana address."
  exit 1
fi

WEBHOOK_URL="${PUBLIC_BASE_URL%/}/webhook"
echo "Webhook URL:       ${WEBHOOK_URL}"
echo "Account addresses: ${ADDRESSES_JSON}"

if [[ -z "${WEBHOOK_ID}" ]]; then
  echo "Creating new Helius webhook..."
  curl -sS -X POST "https://api.helius.xyz/v0/webhooks?api-key=${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "{
      \"webhookURL\": \"${WEBHOOK_URL}\",
      \"transactionTypes\": [\"SWAP\"],
      \"accountAddresses\": ${ADDRESSES_JSON},
      \"webhookType\": \"enhanced\",
      \"authHeader\": \"Bearer ${SECRET}\"
    }" | python3 -m json.tool
  echo ""
  echo "Save the returned webhookID as HELIUS_WEBHOOK_ID in your .env"
else
  echo "Updating Helius webhook ${WEBHOOK_ID}..."
  curl -sS -X PUT "https://api.helius.xyz/v0/webhooks/${WEBHOOK_ID}?api-key=${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "{
      \"webhookURL\": \"${WEBHOOK_URL}\",
      \"transactionTypes\": [\"SWAP\"],
      \"accountAddresses\": ${ADDRESSES_JSON},
      \"webhookType\": \"enhanced\",
      \"authHeader\": \"Bearer ${SECRET}\"
    }" | python3 -m json.tool
fi
