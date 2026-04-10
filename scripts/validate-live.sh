#!/usr/bin/env bash
# validate-live.sh — deterministic end-to-end validation of the live ingestor path.
#
# What it tests:
#   1. Ingestor starts and /healthz responds.
#   2. A synthetic AVOID event (thin liquidity) posts successfully.
#   3. A synthetic WATCH event (few buyers, ok liquidity) posts successfully.
#   4. A synthetic BUY-eligible event (strong velocity + good liquidity) posts.
#   5. /api/snapshots reflects all three tokens.
#   6. Decisions match expected labels (AVOID, WATCH, BUY).
#
# The ingestor is started in the background on a scratch port, used for the
# duration of the script, then killed on exit.  No running ingestor is required.
#
# Usage:
#   scripts/validate-live.sh
#
# Exit codes:
#   0  all checks passed
#   1  one or more checks failed

set -euo pipefail

PORT=18080   # scratch port — avoids conflicting with a running ingestor on 8080
BASE="http://localhost:${PORT}"
PASS=0
FAIL=0

GREEN="\033[0;32m"
RED="\033[0;31m"
RESET="\033[0m"

pass() { echo -e "${GREEN}PASS${RESET}  $1"; PASS=$((PASS+1)); }
fail() { echo -e "${RED}FAIL${RESET}  $1"; FAIL=$((FAIL+1)); }

# ---------------------------------------------------------------------------
# 1. Start ingestor on scratch port, kill on exit.
# ---------------------------------------------------------------------------
echo "Starting ingestor on :${PORT}..."
PORT=${PORT} HELIUS_WEBHOOK_SECRET= go run ./cmd/ingestor > /tmp/validate-ingestor.log 2>&1 &
INGESTOR_PID=$!
trap 'kill ${INGESTOR_PID} 2>/dev/null; echo "Ingestor stopped."' EXIT

# Wait up to 5 seconds for the ingestor to be ready.
for i in $(seq 1 10); do
  if curl -sf "${BASE}/healthz" > /dev/null 2>&1; then break; fi
  sleep 0.5
done

# ---------------------------------------------------------------------------
# 2. /healthz responds.
# ---------------------------------------------------------------------------
HEALTH=$(curl -sf "${BASE}/healthz" 2>/dev/null || echo "")
if echo "${HEALTH}" | grep -q '"ok":true'; then
  pass "/healthz returns {ok:true}"
else
  fail "/healthz not responding (ingestor failed to start — check /tmp/validate-ingestor.log)"
  exit 1
fi

NOW=$(date +%s)

post_event() {
  local mint="$1"
  local sol_lamports="$2"   # native input amount in lamports
  local wallets="$3"        # number of distinct buyer wallets to inject
  local sig_prefix="$4"

  for i in $(seq 1 "${wallets}"); do
    local sig="${sig_prefix}-w${i}-${NOW}"
    local wallet="Wallet${i}validate111111111111111111111111111"
    curl -sf -X POST "${BASE}/webhook" \
      -H "Content-Type: application/json" \
      -d "[{
        \"signature\": \"${sig}\",
        \"slot\": $((RANDOM + 1000)),
        \"timestamp\": ${NOW},
        \"type\": \"SWAP\",
        \"source\": \"RAYDIUM\",
        \"feePayer\": \"${wallet}\",
        \"transactionError\": null,
        \"events\": {\"swap\": {
          \"nativeInput\": {\"account\": \"${wallet}\", \"amount\": \"${sol_lamports}\"},
          \"nativeOutput\": null,
          \"tokenInputs\": [],
          \"tokenOutputs\": [{\"mint\": \"${mint}\", \"tokenAmount\": 1000000}]
        }}
      }]" > /dev/null
  done
}

# ---------------------------------------------------------------------------
# 3. AVOID scenario: one buyer, 0.05 SOL (50_000 lamports).
#    exec = 0.05 / (1 * 20) = 0.0025 < MinExecQualityAVOID(0.1) → AVOID
# ---------------------------------------------------------------------------
MINT_AVOID="AVOIDtestXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
post_event "${MINT_AVOID}" "50000" "1" "avoid"

# ---------------------------------------------------------------------------
# 4. WATCH scenario: 3 unique buyers, 30 SOL total (30_000_000_000 lamports).
#    exec = 30/(1*20) = 1.5 → capped at 1.0 (good).
#    buyers_1m = 3, acceleration = 0 (no prior window) → fails BUY accel gate.
#    buyers_5m = 3 < MinBuyers5mREADY(5) → fails READY.
#    unique_buyers = 3 >= MinTotalBuyersWATCH(3) → WATCH.
# ---------------------------------------------------------------------------
MINT_WATCH="WATCHtestXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
post_event "${MINT_WATCH}" "10000000000" "3" "watch"

# ---------------------------------------------------------------------------
# 5. BUY scenario: 10 unique buyers, 200 SOL total (200_000_000_000 lamports).
#    exec = 200/(1*20) = 10 → capped at 1.0 (good).
#    buyers_1m = 10 >= StrongVelocity1m(8) → bypasses accel gate.
#    buy_sol(200) > sell_sol(0) → net buy pressure ok.
#    adversarial: 10 unique wallets, all different → diversity=1, concentration low → clean.
#    → BUY.
# ---------------------------------------------------------------------------
MINT_BUY="BUYtesttXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
post_event "${MINT_BUY}" "20000000000" "10" "buy"

# Give the store a moment to process.
sleep 0.2

# ---------------------------------------------------------------------------
# 6. /api/snapshots returns all three tokens.
# ---------------------------------------------------------------------------
SNAPS=$(curl -sf "${BASE}/api/snapshots?min_buyers=1&since_minutes=5&limit=50" 2>/dev/null || echo "[]")
SNAP_COUNT=$(echo "${SNAPS}" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")

if [ "${SNAP_COUNT}" -ge 3 ]; then
  pass "/api/snapshots returned ${SNAP_COUNT} token(s) (expected >= 3)"
else
  fail "/api/snapshots returned ${SNAP_COUNT} token(s), expected >= 3"
fi

# ---------------------------------------------------------------------------
# 7. Decision labels match expectations.
# ---------------------------------------------------------------------------
get_decision() {
  local mint="$1"
  echo "${SNAPS}" | python3 -c "
import sys, json
snaps = json.load(sys.stdin)
for s in snaps:
    if s.get('mint') == '${mint}':
        print(s.get('decision', 'MISSING'))
        sys.exit(0)
print('NOT_FOUND')
" 2>/dev/null || echo "ERROR"
}

DEC_AVOID=$(get_decision "${MINT_AVOID}")
DEC_WATCH=$(get_decision "${MINT_WATCH}")
DEC_BUY=$(get_decision "${MINT_BUY}")

if [ "${DEC_AVOID}" = "AVOID" ]; then
  pass "AVOID scenario → decision=${DEC_AVOID}"
else
  fail "AVOID scenario → decision=${DEC_AVOID}, expected AVOID"
fi

if [ "${DEC_WATCH}" = "WATCH" ]; then
  pass "WATCH scenario → decision=${DEC_WATCH}"
else
  fail "WATCH scenario → decision=${DEC_WATCH}, expected WATCH"
fi

if [ "${DEC_BUY}" = "BUY" ]; then
  pass "BUY scenario → decision=${DEC_BUY}"
else
  fail "BUY scenario → decision=${DEC_BUY}, expected BUY"
fi

# ---------------------------------------------------------------------------
# 8. Execution and adversarial fields are present and in range.
# ---------------------------------------------------------------------------
python3 - <<'PYEOF'
import sys, json, os

snaps_raw = open('/dev/stdin').read() if False else None
import subprocess, shlex
snaps = json.loads(subprocess.check_output(
    shlex.split(f"curl -sf http://localhost:{os.environ.get('PORT','18080')}/api/snapshots?min_buyers=1&since_minutes=5&limit=50"),
    stderr=subprocess.DEVNULL
))

failures = []
for s in snaps:
    mint = s.get('mint', '?')
    ep = s.get('execution_penalty')
    adv = s.get('adversarial_score')
    ts = s.get('trade_size_sol')
    imp = s.get('estimated_impact_pct')

    if ep is None or not (0 <= ep <= 1):
        failures.append(f"{mint}: execution_penalty={ep} out of [0,1]")
    if adv is None or not (0 <= adv <= 1):
        failures.append(f"{mint}: adversarial_score={adv} out of [0,1]")
    if ts is None or ts <= 0:
        failures.append(f"{mint}: trade_size_sol={ts} not positive")
    if imp is None or not (0 <= imp <= 100):
        failures.append(f"{mint}: estimated_impact_pct={imp} out of [0,100]")

if failures:
    for f in failures:
        print(f"FAIL  field range: {f}")
    sys.exit(1)
else:
    print(f"PASS  all marketability fields in range across {len(snaps)} snapshots")
    sys.exit(0)
PYEOF
FIELD_CHECK=$?
if [ $FIELD_CHECK -eq 0 ]; then
  PASS=$((PASS+1))
else
  FAIL=$((FAIL+1))
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "-------------------------------------------"
echo "  validate-live: ${PASS} passed, ${FAIL} failed"
echo "-------------------------------------------"

if [ $FAIL -gt 0 ]; then
  echo "Ingestor log: /tmp/validate-ingestor.log"
  exit 1
fi
exit 0
