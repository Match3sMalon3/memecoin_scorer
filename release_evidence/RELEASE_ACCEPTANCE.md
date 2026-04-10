# Release Acceptance

Binary timestamps
- `-rwxr-xr-x@ 1 ddff  staff   9324658 Apr 10 08:56 dashboard`
- `-rwxr-xr-x@ 1 ddff  staff  10570978 Apr 10 08:56 ingestor`

Passing commands
```text
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
python3 audit for why_now / why_not_higher / dominant_blocker / operator_verdict / execution_url
curl -s http://localhost:8090 > /tmp/dashboard.html
grep runtime HTML for Best Current Setup / verdict-label / blocker-cell / why-now / gmgn.ai / resolved / partial fallback / full fallback / compressed
python3 link audit for 5 exact GMGN URLs in rendered HTML
```

Backend-owned field proof
```text
TOTAL_ROWS 11
WHY_MISSING 0 []
VERDICT_MISSING 0 []
BLOCKER_MISSING 0 []
EXEC_MISSING 0 []
```

Rendered HTML proof
```text
<h3>Best Current Setup</h3>
<td class="verdict-label"><strong>structurally broken</strong></td>
<td class="why-now-cell">2 eff buyers /1m • 2 eff buyers /5m • clean clustering</td>
<td class="blocker-cell">impossible execution • thin liquidity</td>
<a href="https://gmgn.ai/sol/token/5G1M83kQjPBwSidYiLDT3WcWzxJRD4t9BG6SZbnHkyfK" class="gmgn-link exec-link" target="_blank" rel="noopener noreferrer">EXECUTE [GMGN]</a>
```

Field ownership proven in served JSON
- `why_now`
- `why_not_higher`
- `dominant_blocker`
- `operator_verdict`
- `execution_url`
