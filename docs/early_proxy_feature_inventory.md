# Early Proxy Feature Inventory

Date: 2026-04-26

The Dune scorer is the teacher. The live early proxy must use only fields observable at decision time and must keep structural risk separate from alpha resemblance.

## A. Available Live Now

These fields are already present on `TokenSnapshot` / `LiveSnapshot` / store-derived state and can be known inside the first 0-5 minutes, assuming events arrive:

- `age_seconds`: token age from `first_seen_at` to snapshot time.
- `buyers_last1m`, `buyers_last5m`: raw unique buyer counts in rolling windows.
- `effective_buyers_1m`, `effective_buyers_5m`: funding-cluster-adjusted buyers.
- `holder_count`: current positive-balance holder count when token amounts are available.
- `buy_sol_last_1m`, `sell_sol_last_1m`: short-window buy/sell pressure.
- `buyer_acceleration`: 1m buyer count over the prior 1m buyer count.
- `liquidity_proxy_sol`: observed buy + sell SOL volume proxy.
- `market_cap_sol`: current market-cap proxy from last observed price and observable supply.
- `estimated_impact_pct`: trade-size impact estimate.
- `top10_holder_pct`: top-10 concentration from observed balances.
- `clustering_row_status`: resolved / partial_fallback / full_fallback.
- `funding_cluster_ratio`: share of 1m buyers compressed by funder clustering.
- `adversarial_score`: live concentration/diversity/repeat-buyer suspicion.
- `engine`: existing structural gate results, including `layer0_reject`.
- `execution_penalty`: current execution-quality proxy.

These are decision-time inputs. Some can be missing or defaulted when live state lacks token amounts, priced swaps, resolved clustering, or enough events. The early proxy must expose those gaps in `missing_fields`.

## B. Available Historically From Dune/Testdata

Inspected files include:

- `testdata/dune_token_level_7d_deduped.csv`
- `testdata/dune_token_level_14d_deduped.csv`
- `testdata/dune_token_level_7d_normalized.csv`
- `testdata/dune_token_level_14d_normalized.csv`
- `testdata/dune_outcome_validation_7d.csv`
- `testdata/dune_outcome_validation_14d.csv`
- fixture CSVs such as `clean_winner.csv`, `high_mfe_bad_monetization.csv`, `zero_exits.csv`, and `negative_return.csv`

Decision-time or plausibly early-window historical features:

- `launch_time`
- `cohort_buyer_count`
- `buyers_min0_1`
- `buyers_min1_5`
- `sniper_intensity_ratio`
- `first_minute_share`
- `size_diversity_ratio`
- `manipulation_risk_score`
- `buy_sol_0_5m`
- `sell_sol_0_5m`
- `unique_traders_0_5m`
- `vwap_entry_price_sol`
- `median_entry_price_sol`
- `cohort_total_sol_in`

Possibly usable only when timestamp-valid for the intended decision point:

- `buy_sol_0_35m`
- `sell_sol_0_35m`
- `buy_sol_5_35m`
- `sell_sol_5_35m`
- `buy_sol_5_60m`
- `sell_sol_5_60m`
- `sell_trade_count_5to35m`
- `sell_unique_traders_5to35m`
- `sell_trade_count_5to60m`
- `sell_unique_traders_5to60m`

The current early proxy does not import these Dune columns directly. It maps their decision-time intent onto live analogues: buyer depth, early flow, buy/sell imbalance, acceleration, concentration, liquidity, impact, and clustering trust.

## C. Forbidden Future/Outcome Fields

These must not be used as early proxy inputs:

- `mfe_multiple_30m`
- `mfe_multiple_15m` when scoring before that horizon
- `mfe_multiple_5_35m`
- `mfe_multiple_5_60m`
- `mae_multiple_5_35m`
- `mae_multiple_5_60m`
- realized return fields
- `median_realized_return_pct`
- `median_realized_return_pct_5to35m`
- `median_realized_return_pct_5to60m`
- `total_cohort_pnl_sol_5to35m`
- `total_cohort_pnl_sol_5to60m`
- `wallets_that_exited`
- `wallets_that_exited_5to35m`
- `wallets_that_exited_5to60m`
- `wallets_gt_25pct`
- `wallets_gt_25pct_5to35m`
- `wallets_gt_25pct_5to60m`
- sell counts after the decision window unless timestamp-valid for that decision point
- `is_clean_tradeable_30m`
- `is_tradeable_30m`
- `outcome_label_strong_35m`
- `outcome_label_strong_60m`

Shadow can still evaluate mature windows separately. The early proxy must not fabricate those unavailable outcomes.
