#!/usr/bin/env bash
# smoke-webhook.sh — post one synthetic buy event to the local ingestor.
#
# Usage:
#   scripts/smoke-webhook.sh
#   INGESTOR_PORT=8080 scripts/smoke-webhook.sh
#
# If HELIUS_WEBHOOK_SECRET is set in the environment (or .env), the correct
# Authorization header is added automatically.
set -euo pipefail

PORT="${INGESTOR_PORT:-${PORT:-8080}}"
BASE="http://localhost:${PORT}"
SECRET="${HELIUS_WEBHOOK_SECRET:-}"

NOW_TS=$(date +%s)
SIG="smoke-$(date +%s%N)"

PAYLOAD=$(cat <<EOF
[{
  "signature": "${SIG}",
  "slot": 999,
  "timestamp": ${NOW_TS},
  "type": "SWAP",
  "source": "RAYDIUM",
  "feePayer": "SmokeWallet111111111111111111111111111111111",
  "transactionError": null,
  "events": {
    "swap": {
      "nativeInput":  {"account": "SmokeWallet111111111111111111111111111111111", "amount": "2000000000"},
      "nativeOutput": null,
      "tokenInputs":  [],
      "tokenOutputs": [{"mint": "SMOKEtokenXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", "tokenAmount": 1000000}]
    }
  }
}]
EOF
)

ARGS=(-s -S -X POST "${BASE}/webhook" \
  -H "Content-Type: application/json" \
  -d "${PAYLOAD}")

if [[ -n "${SECRET}" ]]; then
  ARGS+=(-H "Authorization: Bearer ${SECRET}")
  echo "Auth: Bearer *** (HELIUS_WEBHOOK_SECRET is set)"
else
  echo "Auth: none (HELIUS_WEBHOOK_SECRET not set)"
fi

echo "POST ${BASE}/webhook"
echo ""
curl "${ARGS[@]}" | python3 -m json.tool
echo ""
echo "Check snapshots:  make show-snapshots"
