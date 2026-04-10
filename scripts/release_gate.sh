#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

rm -f ./dashboard ./ingestor
go build -o dashboard ./cmd/dashboard
go build -o ingestor ./cmd/ingestor
ls -l dashboard ingestor
gofmt -w .
go test ./...
make clean-stop
make clean-start
sleep 5
make prove-ingress
curl -s http://localhost:8080/healthz
curl -s "http://localhost:8090/api/live-snapshots?min_buyers=0&since_minutes=240&limit=20&actionable_only=0" > /tmp/live.json
cat /tmp/live.json
python3 - <<'PY'
import json, sys
rows=json.load(open('/tmp/live.json'))
viol_why=[]
viol_verdict=[]
viol_blocker=[]
viol_exec=[]
for r in rows:
    qualifies = any([
        (r.get("effective_buyers_1m") or 0) > 0,
        (r.get("effective_buyers_5m") or 0) > 0,
        (r.get("holder_count") or 0) > 0,
        r.get("clustering_row_status") == "resolved",
        (r.get("market_cap_sol") or 0) > 0,
        (r.get("last_price_sol") or 0) > 0,
    ])
    if qualifies and not r.get("why_now"):
        viol_why.append(r.get("mint"))
    if not r.get("operator_verdict"):
        viol_verdict.append(r.get("mint"))
    if not r.get("dominant_blocker"):
        viol_blocker.append(r.get("mint"))
    if not r.get("execution_url"):
        viol_exec.append(r.get("mint"))
print("TOTAL_ROWS", len(rows))
print("WHY_MISSING", len(viol_why), viol_why)
print("VERDICT_MISSING", len(viol_verdict), viol_verdict)
print("BLOCKER_MISSING", len(viol_blocker), viol_blocker)
print("EXEC_MISSING", len(viol_exec), viol_exec)
if viol_why or viol_verdict or viol_blocker or viol_exec:
    sys.exit(1)
PY
curl -s http://localhost:8090 > /tmp/dashboard.html
grep -nE "Best Current Setup|Structural Quality Filter|exec-link|verdict-label|gmgn.ai|why-now|blocker-cell|partial fallback|full fallback|resolved|compressed" /tmp/dashboard.html
python3 - <<'PY'
import json, sys
rows=json.load(open('/tmp/live.json'))
html=open('/tmp/dashboard.html').read()
checked=0
for r in rows:
    mint=r.get("mint")
    url=r.get("execution_url")
    if not mint or not url:
        continue
    if url in html:
        print("LINK_PASS", mint, url)
        checked += 1
    else:
        print("LINK_FAIL", mint, url)
        sys.exit(1)
    if checked == 5:
        break
print("CHECKED", checked)
if checked < 5:
    sys.exit(1)
PY
