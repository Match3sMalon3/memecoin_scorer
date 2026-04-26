# Operator Runbook — Memecoin Live Signal System

## Quick start

```bash
# Step 1 — bootstrap the Helius API key (silent input, never echoed)
make setup-live-env

# Step 2 — build + start ingestor (8080) and dashboard (8090) as background daemons
make clean-start

# Step 3 — prove ingress is polling live events from Helius
make prove-ingress

# Step 4 — wait for events and check snapshots (polls Helius every 10s by default)
make prove-events

# Check services are alive
make status

# Open the dashboard
open http://localhost:8090

# Stream ingestor logs (shows polling activity)
make tail-ingestor
```

No webhook registration. No cloudflared tunnel. No address list.
`HELIUS_API_KEY` is the only required external configuration.

---

## Clustering backend (required for strict live mode)

BUY and READY signals require a healthy clustering backend. The system supports two backends:

| Backend | How to enable | Notes |
|---|---|---|
| `helius` | Set `HELIUS_API_KEY` in `.env` | Production-grade; resolves funder parents via Helius API |
| `static` | Set `FUNDER_MAP_PATH` in `.env` | Deterministic; for smoke tests and offline validation only |
| `null` | Neither key set | **Unhealthy** — BUY/READY are blocked when `CLUSTER_REQUIRED=1` |

### Setting up the Helius API key

```bash
make setup-live-env   # prompts for key with hidden input — never echoed to terminal
```

The key is written to `.env` (git-ignored). `make prove-go-live` verifies the key is
present, non-placeholder, and that the running ingestor confirms `backend=helius, healthy=true`.

### Verifying strict live mode

```bash
make prove-go-live
```

This pre-flight check will:
1. Confirm `.env` exists and `HELIUS_API_KEY` is set to a real key
2. Start services via `make clean-start`
3. Assert `GET /healthz` returns `clustering.backend=helius`, `clustering.healthy=true`, `clustering.status=healthy`

Fails hard with `FAIL:` prefix if any condition is unmet.

---

## Services

| Service   | Port | Binary            | Log                   |
|-----------|------|-------------------|-----------------------|
| Ingestor  | 8080 | `.pids/ingestor_bin` | `logs/ingestor.log` |
| Dashboard | 8090 | `.pids/dashboard_bin`| `logs/dashboard.log`|

The ingestor polls the Helius transaction history API for all SWAP events on known AMM
programs (Pump.fun bonding curve + Raydium AMM V4) and maintains an in-memory token state.
The dashboard polls `/api/snapshots` every 10 seconds and renders the signal table.

---

## Lifecycle commands

| Command               | What it does                                           |
|-----------------------|--------------------------------------------------------|
| `make clean-start`    | Stop any previous instances, build, and start both services |
| `make clean-stop`     | Kill ingestor + dashboard + cloudflared (if running)   |
| `make status`         | Report listening ports, PID files, health check        |
| `make tail-ingestor`  | `tail -f logs/ingestor.log`                            |
| `make tail-dashboard` | `tail -f logs/dashboard.log`                           |
| `make reset-state`    | Clear the live in-memory store (see Caution below)     |

---

## Environment variables

All variables have safe defaults. Set them in `.env` or export before calling `make`.

### Ingestor (`cmd/ingestor`)

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `HELIUS_WEBHOOK_SECRET` | *(unset)* | Bearer token for webhook auth; auth disabled when unset |
| `TRADE_SIZE_SOL` | `1.0` | Intended position size used for execution quality + impact |
| `LIQUIDITY_MULTIPLIER` | `20.0` | Pool depth conservatism |
| `MIN_EXEC_QUALITY_BUY` | `0.5` | Min execution quality for BUY |
| `MIN_EXEC_QUALITY_READY` | `0.3` | Min execution quality for READY |
| `MIN_EXEC_QUALITY_AVOID` | `0.1` | Below this → hard AVOID |
| `MAX_ADVERSARIAL_BUY` | `0.60` | Adversarial score ceiling for BUY |
| `MAX_ADVERSARIAL_READY` | `0.75` | Adversarial score ceiling for READY |
| `MAX_ESTIMATED_IMPACT_PCT` | `15.0` | Impact ceiling — above this forces AVOID |
| `MAX_SIGNAL_AGE_MINUTES_BUYREADY` | `5` | BUY/READY expire after this many minutes of inactivity |
| `MAX_SIGNAL_AGE_MINUTES_WATCH` | `15` | WATCH expires after this many minutes of inactivity |
| `MIN_TOKEN_AGE_SECONDS_FOR_BUY` | `90` | Token must be at least this old before BUY is allowed |
| `MIN_EFFECTIVE_BUYERS_1M_FOR_CONFIDENT_BUY` | `3` | Effective 1m buyers needed for full confidence |
| `MIN_TOTAL_EVENTS_FOR_CONFIDENCE` | `3` | Total events before event-count warm-up gate fires |
| `ENABLE_LOCAL_ADMIN` | `0` | Set to `1` to enable `/admin/reset-state` (localhost only) |

### Dashboard (`cmd/dashboard`)

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8090` | HTTP listen port |
| `LIVE_MODE` | `0` | Set to `1` for live mode; `0` for offline CSV mode |
| `INGESTOR_URL` | `http://localhost:8080` | Ingestor base URL |
| `REFRESH_INTERVAL_SEC` | `10` | Auto-refresh interval in live mode |

---

## Dashboard columns (live mode)

| Column | Description |
|---|---|
| decision | BUY / READY / WATCH / AVOID |
| conf | Confidence score 0–100 |
| state | `fresh` / `stale` / `expired` |
| token | First 8 chars of mint address |
| raw/eff 1m | Raw buyers last 1m / effective after clustering |
| clust% | Funding cluster ratio — % of 1m wallets sharing a funder parent |
| buy/sell 1m | SOL volume bought and sold in last 1m; red when sell ≥ buy |
| accel | Buyer acceleration (last-1m / prior-1m ratio) |
| exec | Execution quality [0,1] |
| adv | Adversarial score [0,1] |
| impact% | Estimated price impact of the default trade size |
| age | Token age in minutes |
| gates | `N/7` engine gate pass count; `L0` badge = layer-0 hard reject; `→LABEL` = engine ceiling |
| liq/mc | Gate 1 — liquidity / market-cap ratio %; green >5%, yellow 3-5%, red <3% |
| vol/mc | Gate 4 — volume / market-cap ratio %; green >5%, yellow 2-5%, red <2% |
| why now | Positive narrative: what makes this interesting |
| why not higher | Limiting factors holding back the score |

### Signal states

- **fresh** — within the actionable age window; show this to the operator
- **stale** — WATCH signal past BUY/READY window but still within WATCH window
- **expired** — beyond the label's age limit; hidden by default, visible via "show expired/stale"

### Default view

The dashboard shows **only actionable (fresh) signals** by default.
Check "show expired/stale" to reveal older rows — useful for retrospective review.

---

## Ingress architecture

The primary ingress path is **pull-based polling** — no webhook, no cloudflared tunnel,
no address list required.

The ingestor polls the Helius transaction history API every 10 seconds (configurable) for all
SWAP events on the default AMM programs:

| Program | Address |
|---|---|
| Pump.fun bonding curve | `6EF8rrecthR5Dkzon8Nwu78hRvfCKubJ14M5uBEwF6P` |
| Raydium AMM V4 | `675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8` |

All SWAP events on these programs are ingested. No wallet list. No per-token configuration.

**Required config (already set by `make setup-live-env`):**

```
HELIUS_API_KEY=<your key>   # used for both ingress polling and clustering
```

**Optional tuning:**

```
INGRESS_POLL_INTERVAL_SEC=10   # seconds between polls (default 10)
INGRESS_PROGRAMS=<p1>,<p2>     # override program list (default: Pump.fun + Raydium)
```

### Proving ingress is live

After `make clean-start`:

```bash
# Check ingress is configured and connected
make prove-ingress

# Wait for events and check snapshots
make prove-events

# Stream the ingestor log to see polling activity
make tail-ingestor
```

`prove-ingress` output when healthy:
```
=== prove-ingress: checking ingress status ===
  configured : True
  connected  : True
  events     : 47
  programs   : 6EF8rrecthR5Dkzon8Nwu78hRvfCKubJ14M5uBEwF6P, 675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8
PASS: ingress configured=true connected=true
```

### `/healthz` ingress section

`GET http://localhost:8080/healthz` includes an `"ingress"` object:

```json
{
  "ingress": {
    "configured": true,
    "connected": true,
    "programs": ["6EF8rre...", "675kPX9..."],
    "poll_interval_sec": 10,
    "events_total": 47,
    "last_event_ago_sec": 3.1
  }
}
```

- `configured` — `true` when `HELIUS_API_KEY` is set (poller started)
- `connected` — `true` after at least one successful Helius API call
- `events_total` — cumulative new SwapEvents applied since startup
- `last_event_ago_sec` — seconds since the last event was applied (0 = never)

### Optional: Helius push webhook (advanced only)

The `/webhook` endpoint accepts Helius enhanced-transaction push events and feeds them into
the same state store as the poller. This is useful if you want sub-second latency and have
a publicly reachable server (cloudflared tunnel or deployed host).

The push webhook is entirely optional. The poller provides adequate latency (~10s) for
discovery purposes and does not require a public endpoint.

To register a push webhook:

```bash
# 1. Start cloudflared tunnel
cloudflared tunnel --url http://localhost:8080
# Note the assigned *.trycloudflare.com URL

# 2. Export tunnel URL (session-only — do NOT add to .env)
export PUBLIC_BASE_URL='https://xyz-abc-123.trycloudflare.com'

# 3. Set account addresses in .env (required by Helius — use program or wallet addresses)
# HELIUS_ACCOUNT_ADDRESSES=6EF8rrecthR5Dkzon8Nwu78hRvfCKubJ14M5uBEwF6P,...

# 4. Register or update the webhook
make set-helius-webhook
```

Check webhook variables are in scope before step 4:

```bash
make webhook-env-check
```

---

## Hard gates (cannot be averaged away)

| Gate | Trigger |
|---|---|
| Exec floor | `exec_penalty < 0.1` → AVOID regardless of other signals |
| Impact ceiling | `estimated_impact_pct > MAX_ESTIMATED_IMPACT_PCT` → AVOID |
| Warm-up | `age < MIN_TOKEN_AGE_SECONDS_FOR_BUY` → blocks BUY |
| Event count | `total_events > 0 && total_events < MIN_TOTAL_EVENTS_FOR_CONFIDENCE` → blocks BUY |
| Sell reversal | `sell_sol_1m >= buy_sol_1m` (with any 1m activity) → blocks BUY |

---

## Admin reset (local development only)

**Never enable in production or on a publicly-accessible host.**

1. Start the ingestor with `ENABLE_LOCAL_ADMIN=1`:
   ```bash
   ENABLE_LOCAL_ADMIN=1 make run-ingestor
   ```

2. Clear the in-memory store:
   ```bash
   make reset-state
   # or directly:
   curl -X POST "http://localhost:8080/admin/reset-state?confirm=RESET_LIVE_STATE"
   ```

3. Requirements:
   - Must be a POST request
   - Must originate from 127.0.0.1 or ::1 (localhost)
   - Must include `confirm=RESET_LIVE_STATE` query param
   - `ENABLE_LOCAL_ADMIN=1` must be set on the running ingestor process

---

## Cloudflare tunnel (optional)

To expose the ingestor to Helius webhooks from the internet:

```bash
cloudflared tunnel --url http://localhost:8080 >> logs/cloudflared.log 2>&1 &
```

`clean-stop` will kill the cloudflared process if one is running.
`clean-start` does **not** start cloudflared automatically — configure it separately.

---

## Troubleshooting

**Dashboard shows "No live signals yet"**
- Check `make status` — is the ingestor listening?
- Check `make prove-ingress` — is the poller configured and connected?
- Check `make tail-ingestor` — are poll cycles appearing in the log?
- Newly started ingestors need 10-30s for the first poll cycle to complete.
- Inject a synthetic event to test the pipeline: `make smoke-webhook`

**All signals show AVOID**
- Check impact ceiling: `MAX_ESTIMATED_IMPACT_PCT` (default 15%). Very new low-volume tokens may trip this.
- Check exec floor: `MIN_EXEC_QUALITY_AVOID` (default 0.1). Extremely weak observed liquidity proxy evidence will be gated.

**BUY signals not appearing**
- Token may be in warm-up (`warming_up=true`). Wait for `age > MIN_TOKEN_AGE_SECONDS_FOR_BUY`.
- Check sell reversal: `sell_sol_1m >= buy_sol_1m` blocks BUY.
- Check adversarial score: above `MAX_ADVERSARIAL_BUY` blocks BUY.

**Signal appears as "expired"**
- Last event was more than `MAX_SIGNAL_AGE_MINUTES_BUYREADY` (5m) ago for BUY/READY.
- Token may have gone quiet. Check "show expired/stale" to confirm it was ever actionable.

**Want to re-baseline after a bad run**
- `make reset-state` (requires `ENABLE_LOCAL_ADMIN=1` on the running ingestor)
- Or `make clean-start` to restart from scratch

---

## Known limitations

1. **Liquidity proxy is cumulative volume, not true AMM reserve depth.** `liq_proxy_sol = TotalBuySOL + TotalSellSOL` is observed swap-flow evidence. Replace with a real Solana RPC reserve-depth query for production accuracy.

2. **FunderResolver is NullResolver by default.** Effective buyer clustering requires an external funder-parent database. Without it, `effective_buyers == raw_buyers` and the clustering ratio is always 0.

3. **Store is in-memory only.** On ingestor restart, all token state is lost. The poller resumes from scratch (no cursor persistence); a small number of events within the first poll window may be re-applied (signature deduplication in the store prevents double-counting).

4. **Signal state is computed at classification time.** Rows that were BUY at T=0 but are expired at T=6m will appear as AVOID/expired on the next poll — the label itself does not change retroactively.

5. **Warm-up gates use unvalidated priors.** `MIN_TOKEN_AGE_SECONDS_FOR_BUY` (90s) and `MIN_TOTAL_EVENTS_FOR_CONFIDENCE` (3) are conservative starting points. Retune after 200+ live signals.
