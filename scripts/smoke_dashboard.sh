#!/usr/bin/env bash
set -euo pipefail

ROOT="${1:-/Users/ddff/Downloads/memecoin_scorer}"
cd "$ROOT"

echo "== build =="
go build ./...

echo "== tests =="
go test ./...

echo "== restart =="
make clean-stop
make clean-start

echo "== wait =="
sleep 8

echo "== health checks =="
curl -fsS --max-time 5 http://localhost:8080/healthz >/tmp/ingestor_health.txt
curl -fsS --max-time 5 http://localhost:8090/healthz >/tmp/dashboard_health.txt

echo "== dashboard root =="
curl -fsS --max-time 5 http://localhost:8090/ >/tmp/dashboard.html
grep -q "ANTI-BULLSHIT RUNNER INTELLIGENCE" /tmp/dashboard.html

echo "== market context =="
curl -fsS --max-time 5 http://localhost:8090/api/market-context >/tmp/ctx.json
python3 - <<'PY'
import json
ctx=json.load(open('/tmp/ctx.json'))
required=["tokens_seen_today","market_posture","wow_count","watch_count","review_count","dead_count"]
missing=[k for k in required if k not in ctx]
assert not missing, f"missing market-context keys: {missing}"
print("MARKET_CONTEXT_OK", ctx)
PY

echo "== live snapshots =="
curl -fsS --max-time 10 "http://localhost:8090/api/live-snapshots?limit=5" >/tmp/live.json
python3 - <<'PY'
import json, os
raw=open('/tmp/live.json').read()
assert raw.strip(), "empty live snapshots response"
rows=json.loads(raw)
assert isinstance(rows, list), "live snapshots response is not a list"
print("LIVE_SNAPSHOTS_OK", len(rows))
PY

echo
echo "PLATFORM_SMOKE_PASS"
