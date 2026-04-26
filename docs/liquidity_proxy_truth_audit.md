# Liquidity Proxy Truth Audit

Date: 2026-04-27

## Field Trace

`liquidity_proxy_sol` is not direct AMM reserve depth. It is currently the cumulative observed SOL swap-flow proxy:

- `internal/state/store.go` accumulates `buySOL` and `sellSOL` from observed swap events.
- `deriveSnapshot` sets `TotalBuySOL`, `TotalSellSOL`, and `LiquidityPoolSOL` from those observed totals.
- `internal/live/decision.go` sets `liqProxy := snap.TotalBuySOL + snap.TotalSellSOL`.
- The API exposes that value as `liquidity_proxy_sol`.

Because this is observed swap flow, it can be zero or very low even when a real pool has reserve liquidity that the store has not measured.

## Layer0 Dependency

The 7-gate engine currently evaluates Layer0 impossible execution from `snap.LiquidityPoolSOL`. That field is also populated from observed buy/sell SOL flow in the current store, so Layer0 impossible execution can inherit the same under-observation problem.

The user-facing reason now names this correctly as `observed_liq_proxy`, not verified reserve depth.

## Added Evidence Metadata

Every live API row now includes:

- `liquidity_evidence_source`: currently `observed_swaps_proxy`
- `liquidity_evidence_age_seconds`: seconds since the latest event backing the proxy
- `liquidity_proxy_reliable`: currently `false` for the observed swap-flow proxy

These fields make it explicit that the current liquidity value is an unreliable early proxy, not verified executable reserve depth.

## Corrected Risk Semantics

The early proxy scorer does not automatically promote a row just because liquidity evidence is unreliable.

A DEAD row may move to WATCH only when all of these are true:

- the source is `observed_swaps_proxy`
- `liquidity_proxy_reliable=false`
- `liquidity_proxy_sol < 5`
- real buyer flow exists: `buyers_last1m > 0` or `buyers_last5m > 1`
- `top10_holder_pct < 0.95`
- clustering is not `full_fallback`
- no terminal rug/self-bundling signal is present
- high estimated impact is not unsupported by compensating flow evidence

Rows remain DEAD when any hard risk is present:

- no real flow
- extreme top10 concentration
- full fallback clustering
- terminal hard rug or self-bundling
- high impact with no compensating evidence

## Copy Correction

User-facing blocker copy now says `observed liq proxy X < 5` for observed swap-flow liquidity evidence.

The dashboard and API reasons must not describe this proxy as verified reserve depth. Rows can still show that the observed proxy is weak, but that is different from proving the pool has no reserves.

## Root Cause

The root cause is semantic overloading: observed swap-flow volume was treated as liquidity depth. That made early rows look structurally dead due to "liquidity" even though the system only knew that observed swap-flow evidence was thin.

The fix preserves danger flags while separating unreliable proxy evidence from confirmed liquidity failure.

## Manual Comparison Helper

For a quick operator comparison after restart:

```bash
curl -s "http://localhost:8090/api/live-snapshots?limit=20" > /tmp/live.json
python3 scripts/liquidity_proxy_compare.py /tmp/live.json
```

The helper lists mint, proxy value, evidence source, reliability, early proxy band, and risk flags so rows promoted from DEAD to WATCH can be reviewed manually.
