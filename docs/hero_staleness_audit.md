# Hero Staleness Audit

Date: 2026-04-26

## A. Exact hero selection chain

1. Live ingress writes `model.SwapEvent` values into `state.Store.Apply`.
   - `FirstSeenAt` is the minimum observed event block time for a token.
   - `LastEventAt` is the maximum observed event block time for a token.
   - `AgeSeconds` is derived as `now - FirstSeenAt`.

2. `cmd/ingestor` serves `/api/snapshots`.
   - It calls `store.RecentTokens(time.Duration(since_minutes) * time.Minute)`.
   - `RecentTokens` returns only tokens whose `LastEventAt` is after `now - window`, sorted newest `LastEventAt` first.
   - The store also has `PruneStale`, but it only removes tokens older than the fixed four-hour `StaleDuration` and only when the five-minute pruning ticker runs.

3. Each recent token is classified through `live.ClassifyAt`.
   - `applyFreshness` sets `SignalState` from `LastEventAt`, not `AgeSeconds`.
   - BUY/READY are actionable only when event age is at most `MaxSignalAgeMinBuyReady` (default 5 minutes).
   - WATCH is fresh through the BUY/READY window, stale through `MaxSignalAgeMinWatch` (default 15 minutes), and expired after that.
   - AVOID is always expired.

4. The ingestor calls `live.AssignPriorityLabels(out)`.
   - Before the fix, `bestPriorityMint` selected the best row by:
     1. higher `ConfidenceScore`
     2. better `ClusteringRowStatus` rank (`resolved`, `partial_fallback`, `full_fallback`, unknown)
     3. lower `AdversarialScore`
     4. higher `EffectiveBuyers5m`
     5. lower non-zero `EstimatedImpactPct`
     6. implicit retention of the earlier row on complete ties
   - It did not consider `SignalState`, `IsActionable`, or `LastEventAt`.

5. `cmd/dashboard` proxies `/api/live-snapshots` to the ingestor.
   - The server-rendered hero in the WOW dashboard calls `rankedWowRows`.
   - `rankedWowRows` gives `best_on_tape` a large bonus, so the backend's selected `PriorityLabel` dominates hero selection.
   - The browser-side `chooseBestSetup` also returns the first row with `priority_label == best_on_tape`.

## B. Stale-retention findings

- The store can retain old rows by design. `PruneStale` removes only rows older than four hours, while a dashboard query commonly asks for `since_minutes=240`.
- Expiry is not eviction. A row can be marked `signal_state=expired` and still be returned by `/api/snapshots` if it is inside the requested `since_minutes` window.
- Before the fix, expiry was enforced in classification fields but not in backend best-row selection.
- `AgeSeconds` is token age from first seen time. Freshness uses signal age from `LastEventAt`. Those concepts are consistent in code but serve different gates.

## C. Root cause

The repeated hero was a bug in best-row selection plus a dashboard fallback drift, not primarily stale store retention or missing ingress.

The root cause was that `bestPriorityMint` could select an expired row as `best_on_tape` when that row still had stronger historical ranking inputs, especially `ConfidenceScore`. The dashboard then trusted `best_on_tape`, so the same mint could remain hero until it left the requested recent-token window or was pruned.

The live audit after the backend fix also showed all sampled rows were expired. In that case the backend correctly emitted no `best_on_tape`, but the dashboard fallback still rendered the top expired row as hero. That was dashboard selection drift and was fixed by making the dashboard hero chooser ignore expired rows and show `NO LIVE CANDIDATE` when all rows are expired.

There was also a deterministic retention effect: complete comparator ties kept the earlier row. Because `RecentTokens` is sorted by newest `LastEventAt`, that was usually benign for equal rows, but `LastEventAt` was not an explicit tie-break and freshness was not protected against stronger stale rows.

## D. Smallest safe fix

Implemented:

- Exclude `signal_state=expired` rows from `best_on_tape` selection.
- For otherwise equally ranked eligible rows, prefer `fresh` over `stale`.
- For otherwise equal rows, prefer the newer `LastEventAt`.
- Use mint as a final deterministic tie-break.
- Align dashboard fallback hero selection with the same expiry rule.

This leaves endpoint paths and visible UI layout unchanged. Expired rows can still appear in the API/table payload; they just cannot be the backend hero while a non-expired candidate exists.

## E. Real-time operator usefulness

After this fix, the live system is more useful for real-time operator selection because the hero cannot be anchored by an expired row with stale historical strength.

Remaining caveat: real-time usefulness still depends on live ingress health. If the poller/webhook is not receiving fresh swaps, the API can only show old rows inside the requested window, and the dashboard should be read as "no fresh candidate" rather than a current execution signal.
