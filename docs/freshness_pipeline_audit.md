# Freshness Pipeline Audit

Date: 2026-04-26

## Freshness Calculation Chain

1. Helius transactions are normalized in `internal/ingestor/normalize.go`.
   - `SwapEvent.BlockTime` is `time.Unix(tx.Timestamp, 0).UTC()`.
   - This is the event timestamp from Helius/the chain payload, not the local polling time.

2. The in-memory store applies swap events in `internal/state/store.go`.
   - On first event for a mint, `FirstSeenAt` and `LastEventAt` are initialized from `SwapEvent.BlockTime`.
   - On later events, `FirstSeenAt` is the minimum observed block time and `LastEventAt` is the maximum observed block time.
   - `AgeSeconds` is `snapshot_time - FirstSeenAt`.

3. `/api/snapshots` in `cmd/ingestor/main.go` asks the store for `RecentTokens(since_minutes)`.
   - `RecentTokens` keeps tokens whose `LastEventAt` is after `now - since_minutes`.
   - It sorts by `LastEventAt` descending before classification.

4. `live.ClassifyAt` computes `signal_state` in `internal/live/decision.go`.
   - Freshness age is `now - snap.LastEventAt`.
   - It does not use `FirstSeenAt` or `AgeSeconds` for freshness.
   - BUY/READY are fresh/actionable inside `MaxSignalAgeMinBuyReady` (default 5 minutes).
   - WATCH is fresh inside 5 minutes, stale inside `MaxSignalAgeMinWatch` (default 15 minutes), and expired after that.
   - AVOID is always marked `signal_state=expired` and `is_actionable=false`.

5. `warming_up` is separate from freshness.
   - It uses `AgeSeconds` and `TotalEventCount`.
   - It can block BUY, but it is not the source of `signal_state=expired`.

6. `priority_label` is assigned after classification.
   - Expired rows are no longer eligible for `best_on_tape`.
   - Expired rows may still be returned in the API/table for review.

## Health Evidence

Captured after `make clean-start` and `sleep 12`:

- `ingress.connected`: `true`
- `ingress.events_total`: `54`
- `ingress.last_raw_fetched`: `120`
- `ingress.last_normalized`: `13`
- `ingress.last_applied`: `13`
- `ingress.last_event_ago_sec`: `0.102646`
- `clustering.status`: `healthy`
- `clustering.healthy`: `true`
- `ingress.poll_interval_sec`: `10`

This proves the poller was connected and applying events recently.

## Live Snapshot Evidence

The API returned 20 rows:

- `FRESH_ROWS`: `0`
- `EXPIRED_ROWS`: `20`
- Hero: `NO LIVE CANDIDATE`

Top rows had fresh event timestamps but AVOID decisions:

- `ukHH6c7m...`: `last_event_at=2026-04-26T19:16:38Z`, `age_seconds=47.5`, `decision=AVOID`, `signal_state=expired`, reasons included too few buyers.
- `J1krRu...`: `last_event_at=2026-04-26T19:16:38Z`, `age_seconds=19.5`, `decision=AVOID`, `signal_state=expired`, reason was `exec_penalty=0.00 < avoid_floor=0.10`.
- `WENWEN...`: `last_event_at=2026-04-26T19:16:38Z`, `age_seconds=92.5`, `decision=AVOID`, `signal_state=expired`, reason was weak execution.
- `7GCihg...`: `last_event_at=2026-04-26T19:16:37Z`, `age_seconds=30.5`, `decision=AVOID`, `signal_state=expired`, reason was impact above max.
- `HfMbPy...`: `last_event_at=2026-04-26T19:16:37Z`, `age_seconds=83.5`, `decision=AVOID`, `signal_state=expired`, reason was weak execution.

## Root Cause Classification

The observed condition does not match A-G as a freshness pipeline failure:

- A. No fresh upstream events are arriving: disproven. `events_total=54`, `last_applied=13`, `last_event_ago_sec=0.102646`.
- B. Events are arriving, but timestamps are stale: disproven. Top rows had `last_event_at` within seconds of the audit.
- C. Events are arriving, but store retention is returning old rows: disproven for the sampled top rows. `RecentTokens` returned newest `LastEventAt` first.
- D. `signal_state` is incorrectly using `first_seen_at`: disproven by code and tests. It uses `last_event_at`.
- E. Freshness window is shorter than ingestion cadence: disproven. Defaults are 5m/15m vs 10s poll interval.
- F. Dashboard/API is reading stale snapshots: disproven. Health and API payload were current; dashboard showed no candidate after the hero staleness fix.
- G. Clock/timezone mismatch: not supported. Event timestamps were UTC and current.

Actual cause: all sampled rows were classified as `AVOID`, and the current decision logic intentionally maps every AVOID row to `signal_state=expired`. In this codebase, `signal_state=expired` is not purely timestamp freshness; it also means "not actionable because the label is AVOID."

## Smallest Safe Fix

No trading or ranking behavior was changed. The proven issue is not stale timestamp math or stale API data.

Added tests only:

- A row with recent `LastEventAt` is not expired even if `FirstSeenAt` is old.
- A row with old `LastEventAt` is expired.
- Default freshness windows are longer than the default poll cadence.
- `RecentTokens` ordering already prefers newer `LastEventAt`.
- Dashboard hero remains `NO LIVE CANDIDATE` if all rows are expired.

If the operator needs to distinguish "timestamp-expired" from "fresh but rejected", the smallest future schema fix would be adding a separate freshness-age field or reason while preserving `is_actionable=false` for AVOID. That is a display/explainability improvement, not a ranking fix.

## Is `NO LIVE CANDIDATE` Truthful?

Yes. Given the current semantics, `NO LIVE CANDIDATE` is truthful: live ingress is active, but the sampled rows are all AVOID due to execution, impact, or buyer-depth gates.

It is not caused by missing live ingress, stale retention, first-seen/last-event confusion, freshness window/cadence mismatch, dashboard fallback promotion, or timezone drift.
