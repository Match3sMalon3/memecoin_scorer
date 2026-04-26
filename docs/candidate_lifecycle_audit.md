# Candidate Lifecycle Audit

Date: 2026-04-26

## Signal State Assignment

`signal_state` is assigned in `internal/live/decision.go` by `applyFreshness`, which runs during `ClassifyAt` after the BUY/READY/WATCH/AVOID label and engine gate results have been computed.

Before this change, lifecycle and trade actionability were conflated:

- BUY/READY could be `fresh` inside `MaxSignalAgeMinBuyReady` and `expired` after that.
- WATCH could be `fresh`, `stale`, or `expired` based on `LastEventAt`.
- AVOID was always marked `expired`, regardless of token age or recent activity.

This meant a 20-second-old token with weak execution or weak observed liquidity-proxy evidence was operationally dead in the UI even though it was still inside the earliest proxy observation window.

## Current States

The lifecycle states are now:

- `forming`: token age is less than 5 minutes and the row is not a terminal hard-rug/self-bundling reject.
- `active`: token age is at least 5 minutes and `LastEventAt` is inside the active freshness window.
- `cooling`: token is older than 5 minutes and quiet, but still inside the monitoring window.
- `expired`: token is beyond the monitoring window, or has a terminal hard-rug/self-bundling rejection.

Layer0 impossible execution no longer makes a token lifecycle-expired before 5 minutes. It still blocks actionability and appears as structural risk.

## Time Fields

`internal/state/store.go` maintains both first and last event timestamps:

- `FirstSeenAt` is the minimum observed block time for the token.
- `LastEventAt` is the maximum observed block time.
- `AgeSeconds` is derived from wall clock `now - FirstSeenAt`; it is token age, not silence age.
- Silence/freshness is derived from wall clock `now - LastEventAt`.

`RecentTokens` keeps rows with recent `LastEventAt` inside the requested window and sorts newest `LastEventAt` first. `PruneStale` removes rows only after store retention expiry; it is separate from lifecycle classification.

## Root Cause

The sampled rows expired at roughly 18-92 seconds because the old lifecycle rule marked every AVOID row as `expired`. The rows were not timestamp-expired: they had recent `LastEventAt` values and young `AgeSeconds`. They were trade-rejected by structural and execution gates, then lifecycle-expired because AVOID was treated as terminal.

That conflicts with the 5-minute early proxy objective. A runner-formation proxy needs a watch lifecycle so evidence can accumulate before the system declares the token operationally dead.

## Separation of Concerns

The current model separates these questions:

- `signal_state`: whether the token is still worth monitoring in the early lifecycle.
- `is_actionable` / `actionability_label`: whether it can be traded now.
- `early_proxy`: whether the row resembles early runner formation from decision-time evidence.
- `early_proxy.risk_flags` and engine fields: what can kill the setup.

This preserves risk warnings without using structural rejection as the primary lifecycle clock.

## Hero Eligibility

Dashboard hero selection now treats `forming` and `active` rows as primary hero candidates. `cooling` rows can become hero only when no forming or active rows exist. `expired` rows cannot become hero.

Within each lifecycle group, rows are ranked by:

1. `early_proxy.score`
2. `early_proxy.band`
3. `last_event_at`
4. `confidence_score`
5. mint tie-break

DEAD proxy rows are never selected as hero. They stay visible in the table for monitoring and review.

## Smallest Safe Fix

The smallest safe fix was to replace the old `fresh`/`stale`/`expired` lifecycle with `forming`/`active`/`cooling`/`expired`, and to update dashboard hero eligibility to use lifecycle plus early proxy score.

No endpoint paths, early proxy weights, trading thresholds, or UI layout were changed.

## Real-Time Usefulness

`NO LIVE RUNNER CANDIDATE` is truthful when no forming/active/cooling row has a non-DEAD early proxy band. A young AVOID row should now remain visible as `forming` and can be monitored, but it is not promoted to an execution signal unless early proxy, actionability, and risk evidence support that separately.

## Hero Semantics Hardening

Lifecycle state means monitoring eligibility, not alpha eligibility. A `forming` token is young enough to keep observing, but it is not automatically a runner candidate.

Runner-candidate eligibility is controlled by `early_proxy.band`:

- `APEX`, `CANDIDATE`, and `WATCH` can become hero when lifecycle is eligible.
- `DEAD` cannot become hero, even when the row is still `forming`.
- `expired` rows can never become hero.

When all forming/active rows are `DEAD`, the hero panel shows `NO LIVE RUNNER CANDIDATE` while the table still shows the rows, their `DEAD` proxy band, risk flags, and external links.
