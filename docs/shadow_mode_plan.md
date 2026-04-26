# Shadow Mode Plan

Shadow mode keeps the current live terminal intact while adding an inspectable bridge to the existing Dune-validated scorer. The live system can continue to rank on posture, clustering, execution quality, and freshness, while every live row also carries a `shadow` object that says whether the mature offline scorer can run yet.

This is necessary because the repo contains two different systems:

- `internal/scoring/scorer.go` is the validated offline/Dune scorer.
- `internal/live/decision.go` and `internal/engine/scorer.go` are live posture and structural filters.

The validated scorer depends on outcome-window fields that are only meaningful after a token has aged into the 30m/35m window. Running it on fresh tokens would fabricate certainty. Shadow mode therefore blocks scoring until the feature window is complete and all required validated inputs are present.

Validated fields still unavailable or not yet proven equivalent live:

- `cohort_buyer_count`
- `mfe_multiple_30m`
- `manipulation_risk_score`
- `first_minute_share`
- `sniper_intensity_ratio`
- `size_diversity_ratio`

Fields the live store can begin to observe after maturity, subject to bounded-history coverage:

- `buy_sol_0_35m`
- `sell_sol_0_35m`
- `sell_trade_count_5to35m`
- `sell_unique_traders_5to35m`
- `wallets_that_exited`
- `wallets_gt_25pct`
- `median_realized_return`
- `winner_exit_ratio`

The current API exposes this as `shadow` on each live row. Incomplete rows return `eligible_for_shadow_score=false`, `feature_window_complete`, `missing_fields`, and notes. Complete rows call the existing scorer and return `validated_tradeable_30m`, `validated_clean_30m`, `opportunity_score`, and `compared_at`.

What must be built next for parity:

1. Recover or reconstruct exact Dune definitions for the unavailable fields.
2. Extend the live store to persist enough full-window event history to compute those fields without bounded-history truncation.
3. Add exact MFE and entry-price reconstruction for the validated window.
4. Compare shadow outputs against current live posture decisions and realized outcomes before changing ranking.
5. Only promote shadow score into live ranking after parity evidence is collected.
