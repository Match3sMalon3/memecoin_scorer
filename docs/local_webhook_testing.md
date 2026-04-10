# Local Webhook Testing

This document covers everything needed to run the live ingestion pipeline locally, send real (or synthetic) Helius webhook events through it, and verify the live dashboard.

---

## Happy path — 5 commands

Open **four terminal tabs** and run one command in each:

```bash
# Tab 1 — ingestor
source .env && make run-ingestor

# Tab 2 — dashboard (live mode)
source .env && make run-dashboard-live

# Tab 3 — tunnel (skip if testing with synthetic events only)
cloudflared tunnel --url http://localhost:8080

# Tab 4 — smoke test / verification
make smoke-webhook        # send one synthetic buy event
make show-snapshots       # verify it was stored and classified
```

Open http://localhost:8090 in a browser to see the live dashboard.

---

## Environment setup

Copy `.env.example` to `.env` and fill in values:

```bash
cp .env.example .env
# then edit .env
```

`.env` is git-ignored and must never be committed.

Check active configuration at any time:

```bash
make env-check
```

---

## Terminal layout

| Tab | Process | Port | Notes |
|-----|---------|------|-------|
| 1 | `cmd/ingestor` | 8080 | Receives Helius webhook events |
| 2 | `cmd/dashboard` | 8090 | Renders live signal table |
| 3 | `cloudflared` | — | Exposes :8080 as a public HTTPS URL |
| 4 | curl / make | — | Testing, healthchecks, diagnostics |

---

## Starting the ingestor

```bash
source .env
make run-ingestor
```

Expected log line:
```
ingestor listening on http://localhost:8080
live config: trade_size=1.00 SOL, liq_multiplier=20, exec_gates=[BUY>=0.50 READY>=0.30 AVOID<0.10], adv_gates=[BUY<=0.60 READY<=0.75]
```

The ingestor accepts:
- `POST /webhook` — Helius enhanced-transaction payload (bare array or wrapped object)
- `GET /api/snapshots` — scored live token state
- `GET /healthz` — liveness probe

---

## Starting the dashboard in live mode

```bash
source .env
make run-dashboard-live
# or equivalently:
LIVE_MODE=1 make run-dashboard
```

Expected log line:
```
dashboard mode: LIVE (ingestor: http://localhost:8080, refresh: 10s)
dashboard listening on http://localhost:8090
```

Open http://localhost:8090. The mode badge should show **LIVE** (green).

The table columns are:
`decision | token | buyers_1m | buyers_5m | accel | exec | adv | liq_proxy | impact% | buy_sol | sell_sol | age_min | reason`

---

## Starting cloudflared (for real Helius events)

Skip this section if you are only testing with synthetic events (`make smoke-webhook`).

```bash
cloudflared tunnel --url http://localhost:8080
```

cloudflared will print a line like:
```
https://abc-xyz-789.trycloudflare.com
```

Copy that URL, set it in your shell and `.env`:
```bash
export PUBLIC_BASE_URL=https://abc-xyz-789.trycloudflare.com
# also add to .env:
# PUBLIC_BASE_URL=https://abc-xyz-789.trycloudflare.com
```

**trycloudflare URLs change on every cloudflared restart.** Update Helius any time this happens.

---

## Registering / updating the Helius webhook

Set these env vars in `.env` (not committed):
```
HELIUS_API_KEY=your-api-key-from-helius-dashboard
HELIUS_WEBHOOK_SECRET=your-chosen-secret   # any string, keep it secret
PUBLIC_BASE_URL=https://abc-xyz.trycloudflare.com
```

**Create** a new webhook:
```bash
source .env
scripts/set-helius-webhook.sh
```

Save the `webhookID` returned and add it to `.env`:
```
HELIUS_WEBHOOK_ID=returned-id-here
```

**Update** after a cloudflared restart (new URL):
```bash
export PUBLIC_BASE_URL=https://new-url.trycloudflare.com
scripts/set-helius-webhook.sh   # reads HELIUS_WEBHOOK_ID from .env
```

---

## Testing /webhook with a synthetic event

```bash
make smoke-webhook
```

This posts one synthetic SWAP event to `http://localhost:8080/webhook`. If `HELIUS_WEBHOOK_SECRET` is set, the correct `Authorization: Bearer` header is added automatically.

Expected output:
```json
{
    "ok": true,
    "events_received": 1,
    "events_applied": 1
}
```

If `events_applied` is 0, the event was deduplicated (same signature sent twice). The script generates a timestamp-based signature so re-running always sends a new event.

---

## Verifying snapshots

```bash
make show-snapshots
# or directly:
curl -s "http://localhost:8080/api/snapshots?min_buyers=1&since_minutes=60&limit=20" | python3 -m json.tool
```

Each entry includes:
- `decision` — BUY / READY / WATCH / AVOID
- `execution_penalty` — [0,1]; low = thin liquidity relative to trade size
- `estimated_impact_pct` — your trade as % of proxy pool depth
- `adversarial_score` — [0,1]; 0=clean, 1=maximally suspicious
- `reasons` — why the decision was made

---

## Healthchecks

```bash
make healthcheck-ingestor    # checks /healthz + prints recent snapshots
make healthcheck-dashboard   # checks /healthz + prints /api/config
make healthcheck             # both
```

---

## Diagnosing common problems

### `curl: (7) Failed to connect to localhost port 8080`
The ingestor is not running. Start it: `make run-ingestor`

### `curl: (7) Failed to connect to localhost port 8090`
The dashboard is not running. Start it: `make run-dashboard-live`

### `/webhook` returns 401 Unauthorized
`HELIUS_WEBHOOK_SECRET` is set in the ingestor environment but the request is missing or has the wrong `Authorization: Bearer` header.
- If testing locally with no secret: unset `HELIUS_WEBHOOK_SECRET` in `.env` and restart the ingestor.
- If testing with `make smoke-webhook`: ensure `HELIUS_WEBHOOK_SECRET` is the same value in both the ingestor env and the shell running the script.

### Helius reports 404 on the webhook URL
The tunnel URL has changed since the webhook was registered. Re-run:
```bash
export PUBLIC_BASE_URL=https://new-url.trycloudflare.com
scripts/set-helius-webhook.sh
```

### cloudflared: `ERR_NGROK_3200 Tunnel not found`
The tunnel session has expired. Restart cloudflared to get a new URL, then update Helius.

### Dashboard shows "No live signals yet"
Either:
1. No events have arrived yet — run `make smoke-webhook` to inject a synthetic one.
2. `LIVE_MODE` is not set — confirm the mode badge says **LIVE** not **OFFLINE**.
3. Ingestor is not running — run `make healthcheck-ingestor`.
4. `INGESTOR_URL` points to the wrong address — check `make env-check`.

### Dashboard shows OFFLINE mode
`LIVE_MODE=1` is not in the environment for the dashboard process. Use `make run-dashboard-live` which sets it explicitly, or add `LIVE_MODE=1` to `.env` and `source .env` before starting.

### "invalid api key" from Helius
`HELIUS_API_KEY` in `.env` is wrong or missing. Find the correct key at https://dev.helius.xyz/dashboard/app.

### All tokens show AVOID
This is expected right after startup — the ingestor has only seen a few events so `liquidity_proxy_sol` is low and `execution_penalty` is below the BUY/READY floors. BUY and READY require meaningful observed volume. Let real events accumulate for a few minutes, or run `make smoke-webhook` several times with different amounts.

### `make smoke-webhook` fails with `python3: command not found`
Replace `python3 -m json.tool` with `cat` in the script, or install Python 3 (already present on most macOS systems via `xcode-select --install`).

---

## Stale trycloudflare URL checklist

When cloudflared restarts it assigns a new URL. You must:

1. Copy the new URL from cloudflared output.
2. `export PUBLIC_BASE_URL=https://new-url.trycloudflare.com`
3. `scripts/set-helius-webhook.sh` — updates Helius.
4. Verify: `curl -s https://new-url.trycloudflare.com/healthz`

Until step 3 is done, Helius is posting to the old URL and events are lost.
