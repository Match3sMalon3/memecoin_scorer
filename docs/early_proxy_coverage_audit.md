# Early Proxy Coverage Audit

Date: 2026-04-26

## Live Missing Fields Seen

After restarting the stack with the coverage fix, `/api/live-snapshots?limit=5` returned five rows with `early_proxy` present on every row.

Missing fields seen in the sampled rows:

- `holder_count`: 1 row
- `market_cap_sol`: 1 row
- `top10_holder_pct`: 1 row

Fields no longer incorrectly reported missing when observed as valid zero values:

- `sell_sol_last_1m`
- `buyer_acceleration`
- `funding_cluster_ratio`
- `adversarial_score`
- raw/effective buyer fields when the observed value is zero
- buy/sell flow fields when the observed value is zero

## Root Cause Classification

Primary root cause: C. `ScoreEarlyProxy` incorrectly treated valid zero values as missing.

Secondary finding: E. The zero proxy scores in the sampled rows were truthful after coverage was fixed, because rows hit explicit hard-veto conditions:

- `top10_holder_pct` at or near 1.0, which is an extreme concentration veto.
- Layer 0 impossible execution, usually from liquidity below the 5 SOL minimum.
- One row had no real buyer flow.

Not root causes:

- A. Actual absent live data for every field: false. Buyer flow, liquidity, market cap, impact, clustering, confidence, and risk evidence were present in most rows.
- B. JSON/model naming mismatch: not found. Payload names map correctly.
- D. Proxy called before classification fields are populated: not found. `ScoreEarlyProxy` runs after `live.ClassifyAt` fields are copied and row enrichment is applied.
- F. Store/snapshot not preserving required live evidence: not supported by the sampled rows.

## Is Score 0 Truthful?

For the sampled rows, score 0 is truthful because each zero score was caused by an explicit hard-veto condition, not by missing evidence coverage.

The previous `ROWS_WITH_MISSING_PROXY_FIELDS 20` result was partly a coverage bug: valid observed zeros were being mixed into `MissingFields`, making low score ambiguous. That ambiguity is fixed.

## Smallest Safe Fix

Implemented only missing-field semantics:

- Zero buyer and flow values are treated as observed evidence, not missing evidence.
- Zero sell pressure is valid evidence.
- Zero funding cluster ratio is valid when clustering is resolved or otherwise present.
- Zero adversarial score is valid low-risk evidence.
- Derived structural fields remain missing when unavailable: holder count, market cap proxy, top10 concentration, clustering status, effective buyers when raw buyers exist but effective values are absent, and execution/impact/liquidity when observed flow exists but derived fields are absent.

No proxy weights, thresholds, endpoint paths, or UI layout were changed.

## Evidence Available Now

The live rows currently provide usable early proxy evidence:

- raw and effective buyer counts
- buy/sell pressure
- liquidity proxy
- market cap proxy for most sampled rows
- estimated impact
- holder/top10 concentration for most sampled rows
- clustering row status
- adversarial score
- confidence score
- execution/layer0 risk flags

These fields now contribute to score or risk flags without being mislabeled as missing.

## Evidence Still Unavailable

Some rows still legitimately lack:

- `holder_count`
- `market_cap_sol`
- `top10_holder_pct`

These remain in `MissingFields` because they depend on token amount/holder/price evidence being available in live state. They should not be fabricated.
