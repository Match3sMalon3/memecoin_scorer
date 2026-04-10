# Version 2 Go-Live Checklist

This document is the authoritative reference for starting, validating, and operating the V2 live signal system.

---

## 1. What the system does right now

**V2 is a live signal classifier, not an execution engine.**

It ingests Helius webhook swap events → maintains an in-memory rolling state per token → classifies each token into BUY / READY / WATCH / AVOID using velocity, execution quality, and adversarial heuristics → exposes those signals via `/api/snapshots` → renders them in the live dashboard.

No orders are placed. No Postgres. No deployer profiling. No sybil graphing.

---

## 2. Pre-flight checks

Run these before starting any live session:

```bash
# Confirm everything builds and all tests pass.
make build
make test

# Confirm race detector is clean.
make test-race

# Check active environment variables.
make env-check
```

Ensure `.env` exists and contains at minimum:
```
PORT=8080
LIVE_MODE=0               # dashboard default; override per-process
INGESTOR_URL=http://localhost:8080
```

See `.env.example` for the full list.

---

## 3. Start the system

Open **three terminal tabs**:

```bash
# Tab 1 — ingestor (receives Helius events)
source .env && make run-ingestor

# Tab 2 — dashboard in live mode
source .env && make run-dashboard-live

# Tab 3 — verification
make healthcheck
```

**Expected log lines:**

Ingestor (Tab 1):
```
live config: trade_size=1.00 SOL, liq_multiplier=20, exec_gates=[BUY>=0.50 READY>=0.30 AVOID<0.10], adv_gates=[BUY<=0.60 READY<=0.75]
ingestor listening on http://localhost:8080
```

Dashboard (Tab 2):
```
ingestor reachable at http://localhost:8080
dashboard mode: LIVE (ingestor: http://localhost:8080, refresh: 10s)
dashboard listening on http://localhost:8090
```

If the dashboard logs `WARNING: ingestor appears unreachable`, start the ingestor first and reload.

Open http://localhost:8090 — mode badge must show **LIVE** (green).

---

## 4. Validate end-to-end (single command)

```bash
make validate-live
```

This script:
1. Starts a scratch ingestor on port 18080 (separate from any running instance).
2. Posts three controlled synthetic SWAP events designed to produce AVOID, WATCH, and BUY decisions.
3. Calls `/api/snapshots` and asserts the correct decision for each token.
4. Asserts all marketability fields (`execution_penalty`, `adversarial_score`, `trade_size_sol`, `estimated_impact_pct`) are in range.
5. Kills the scratch ingestor and reports PASS/FAIL counts.

**Expected output (all passing):**
```
PASS  /healthz returns {ok:true}
PASS  /api/snapshots returned 3 token(s) (expected >= 3)
PASS  AVOID scenario → decision=AVOID
PASS  WATCH scenario → decision=WATCH
PASS  BUY scenario → decision=BUY
PASS  all marketability fields in range across 3 snapshots

-------------------------------------------
  validate-live: 6 passed, 0 failed
-------------------------------------------
```

---

## 5. What success looks like in production

After Helius begins delivering real events:

| Signal | What it means | Action |
|--------|--------------|--------|
| **BUY** | Velocity strong, execution viable, adversarial clean | Candidate for manual review / position |
| **READY** | Promising but not BUY-grade (weaker velocity or exec) | Monitor; may upgrade in next poll |
| **WATCH** | Early / thin; some real activity | Keep watching; no action |
| **AVOID** | Execution too weak, adversarial too high, or no activity | Do not act |

Check the **reason** column for the specific gate that blocked or passed each label.

---

## 6. What each decision input is — and is not

### `execution_penalty` [0,1]
- **Is:** `liquidity_proxy_sol / (trade_size_sol × liquidity_multiplier)`, capped at 1.0.
- **Is not:** actual AMM slippage. No RPC depth query is made.
- **Proxy limitation:** `liquidity_proxy_sol` = cumulative `TotalBuySOL + TotalSellSOL` since first seen. On a new token with 500 SOL of real pool depth but only 10 SOL of observed swaps, the proxy is 10 SOL and exec will be low — it will improve as more events arrive.

### `estimated_impact_pct`
- **Is:** `trade_size_sol / liquidity_proxy_sol × 100`, capped at 100%.
- **Is not:** actual price impact. Uses the same proxy as above.
- **Interpretation:** <5% is comfortable; >20% is a red flag given current data.

### `adversarial_score` [0,1]
- **Is:** a weighted combination of three live heuristics computed from the in-memory buy history:
  - Concentration: single wallet's share of 5m buy volume.
  - Diversity: unique wallets / total buy events in 5m.
  - Repeat buyers: fraction of 1m buyers recycled from prior 1m.
- **Is not:** sybil detection. A coordinated group of wallets each acting independently will score clean. Graph-based coordination detection requires the graph service (not yet built).
- **Weights are unvalidated priors** (0.45/0.30/0.25) — retune after 50+ live signals.

### `buyer_acceleration`
- **Is:** `buyers_last1m / buyers_in_prior1m`. Returns 0 when no prior window (brand-new tokens).
- **Is not:** a reliable signal on tokens under 2 minutes old. The prior window is always empty.

### `liquidity_proxy_sol`
- **Is:** `TotalBuySOL + TotalSellSOL` accumulated since first event seen in this session.
- **Is not:** true AMM pool depth.
- **Resets on restart:** in-memory state. After a restart the proxy starts at 0 and rises as events arrive. Signals for the first few minutes after restart will have low exec scores even for healthy tokens.

---

## 7. Current decision gate thresholds (defaults)

| Gate | Threshold | Env var to override |
|------|-----------|---------------------|
| BUY: min buyers last 1m | ≥ 3 | — |
| BUY: min acceleration | ≥ 1.0 (or bypass if buyers_1m ≥ 8) | — |
| BUY: min exec quality | ≥ 0.5 | `MIN_EXEC_QUALITY_BUY` |
| BUY: net buy pressure | buy_sol > sell_sol | — |
| BUY: max adversarial | ≤ 0.60 | `MAX_ADVERSARIAL_BUY` |
| READY: min buyers last 5m | ≥ 5 | — |
| READY: min exec quality | ≥ 0.3 | `MIN_EXEC_QUALITY_READY` |
| READY: max adversarial | ≤ 0.75 | `MAX_ADVERSARIAL_READY` |
| WATCH: min total unique buyers | ≥ 3 | — |
| Hard AVOID: exec below floor | < 0.1 | `MIN_EXEC_QUALITY_AVOID` |

All thresholds are **unvalidated priors**. Retune after observing 100+ live signals against outcomes.

---

## 8. Known limitations before trusting larger capital

1. **No true pool depth.** `liquidity_proxy_sol` understates real AMM depth, especially on new tokens. A BUY signal with `estimated_impact_pct = 2%` against a proxy of 50 SOL might actually be 20% impact against 5 SOL of real depth. Do not size positions based on the proxy alone.

2. **State resets on restart.** The in-memory store has no persistence. After a crash or redeploy, all token state is lost. Signals return only as new events arrive. Expect a 5–15 minute warm-up before scores are reliable.

3. **Adversarial heuristics are not sybil-proof.** Coordinated wallets with distinct addresses acting independently will appear clean. The adversarial score is a heuristic, not a proof.

4. **Velocity metrics are only over the bounded history (last 500 buy events per token).** On very high-activity tokens, oldest events are evicted from the window. `BuyersLast1m` and `BuyersLast5m` may under-count on such tokens.

5. **No deployer profiling yet.** A serial rugger deploying under a fresh address gets the same score as an honest deployer. Deployer intelligence is a future module.

6. **No execution service.** BUY is a signal to review, not an order. All trading decisions remain fully manual.

7. **Gate thresholds are unvalidated priors.** The current values were chosen conservatively but have not been tuned against live signal outcomes. After 50–100 signals, review which thresholds are producing too many false BUYs or blocking good tokens.

---

## 9. Operational commands reference

```bash
make build                    # build all packages
make test                     # unit tests
make test-race                # unit tests with race detector
make validate-live            # end-to-end live path validation (no running services needed)

make run-ingestor             # start ingestor on :8080
make run-dashboard-live       # start dashboard in live mode on :8090

make healthcheck-ingestor     # probe ingestor /healthz + show recent snapshots
make healthcheck-dashboard    # probe dashboard /healthz + /api/config
make healthcheck              # both

make smoke-webhook            # post one synthetic buy event
make show-snapshots           # pretty-print /api/snapshots

make env-check                # show active environment variable values
```

---

## 10. If something is wrong

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Dashboard shows OFFLINE badge | `LIVE_MODE` not set | Use `make run-dashboard-live` |
| Dashboard log: "ingestor appears unreachable" | Ingestor not started yet | Start ingestor first |
| All tokens AVOID | Too little data; proxy too low | Wait for more events or run `make smoke-webhook` |
| `make validate-live` WATCH→BUY mismatch | Port conflict or stale process | Kill process on :18080, re-run |
| No rows in dashboard table | `since_minutes` window too short; no recent events | Check `make show-snapshots` directly |
| Webhook 401 | `HELIUS_WEBHOOK_SECRET` mismatch | Match value in `.env` between ingestor and curl/Helius config |

For deeper diagnostics see [local_webhook_testing.md](local_webhook_testing.md).
