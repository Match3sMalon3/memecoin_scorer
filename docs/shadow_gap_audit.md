# Shadow Gap Audit

## Section A — Missing Validated Inputs

These validated scorer inputs are currently missing in live shadow mode because `internal/state/store.go` does not populate their `Has*` flags in `ShadowFeatureInputs`, so `internal/shadow/map_live_to_token_features.go` reports them in `missing_fields`.

| Input | Why missing | Future window? | Wallet PnL/exits? | Dune-only definition? |
|---|---|---:|---:|---:|
| `cohort_buyer_count` | Live state tracks all unique buyers and short rolling buyer windows, but not the exact Dune cohort membership used by the validated export. | No, likely early/cohort-window based. | No. | Yes, exact cohort definition is unavailable. |
| `mfe_multiple_30m` | Live state has `last_price_sol` only, not a retained entry price plus max price path through 30m. | Yes, requires the 30m price path. | No. | Partly; the MFE window/entry convention comes from Dune export definitions. |
| `manipulation_risk_score` | Live has adversarial heuristics, holder concentration, and clustering, but not the Dune integer manipulation score. | Possibly no, depending on Dune definition. | No. | Yes, exact scoring rule is unavailable. |
| `first_minute_share` | Live can compute recent buy windows, but the store does not currently retain a fixed first-minute volume share feature for shadow output. | No, available after minute 1 if defined as first-minute buy share over a known denominator. | No. | Partly; denominator and Dune exact calculation are unavailable. |
| `sniper_intensity_ratio` | Live does not identify snipers or compute the Dune sniper ratio. | Possibly no, depending on Dune definition. | No. | Yes, sniper definition is unavailable. |
| `size_diversity_ratio` | Live has wallet diversity by count over the rolling 5m window, but not Dune's trade-size diversity calculation. | Possibly no, if based on early trade sizes. | No. | Yes, exact diversity definition is unavailable. |
| `winner_exit_ratio` | Live can derive it only when `wallets_that_exited > 0`; if no exit exists, the current mapper marks it missing rather than fabricating a zero. | Yes, needs sell/exits by the 35m outcome window. | Yes. | No for the ratio itself, but parity depends on Dune exit definitions. |

## Section B — Partially Derivable Inputs

These inputs have some support in live state, but the current bridge either only maps them after the 35m checkpoint or the approximation is not proven Dune-equivalent.

| Input | Approximation quality | Current live support | Still missing |
|---|---|---|---|
| `buy_sol_0_35m` | High if bounded history did not truncate. | `buyHistory` with timestamps and SOL amounts; `buySolBetween` over `[first_seen, first_seen+35m]`. | Full-history persistence beyond `MaxBuyHistoryPerToken`; Dune parity around launch time and included venues. |
| `sell_sol_0_35m` | High if bounded history did not truncate. | `sellHistory` with timestamps and SOL amounts; `sellSolBetween` over `[first_seen, first_seen+35m]`. | Full-history persistence beyond `MaxSellHistoryPerToken`; Dune parity around launch time and included venues. |
| `sell_trade_count_5to35m` | High if bounded history did not truncate. | `sellTradeCountBetween` over `[first_seen+5m, first_seen+35m]`. | Full sell history and exact Dune window boundary behavior. |
| `sell_unique_traders_5to35m` | High if bounded history did not truncate. | `sellUniqueTradersBetween` over `[first_seen+5m, first_seen+35m]`. | Full sell history, wallet identity parity, Dune venue coverage. |
| `wallets_that_exited` | Medium. | `walletsThatExitedBy` counts wallets with sells by minute 35. | Dune's exact "exited" definition may require position closure or cohort membership, not merely any sell. |
| `wallets_gt_25pct` | Medium. | `walletsOverReturnPctBy` compares observed sell SOL to observed buy SOL by wallet. | Cost basis parity, partial exits, remaining inventory valuation, fees/slippage, and Dune cohort filtering. |
| `median_realized_return` | Medium. | `medianRealizedReturnPctBy` computes median realized return from observed wallet buy/sell SOL. | Same issues as `wallets_gt_25pct`; also excludes wallets without realized sells. |
| `winner_exit_ratio` | Medium when exits exist. | Derived from `wallets_gt_25pct / wallets_that_exited`. | Missing when there are no exits; Dune-equivalent exit and winner definitions. |
| `first_minute_share` | Low to medium. | `buyHistory`, `TotalBuySOL`, `BuySolLast1m`, `buyersInWindow` patterns. | Fixed first-minute snapshot and exact denominator. |
| `cohort_buyer_count` | Low to medium. | `uniqueBuyers`, `buyersInWindow`, `UniqueBuyerCount`, `BuyersLast1m`, `BuyersLast5m`. | Exact Dune cohort window and cohort membership rules. |
| `size_diversity_ratio` | Low. | `TokenAmount`, SOL amounts, wallet diversity ratio, top-wallet share. | Exact Dune formula for trade-size diversity. |
| `manipulation_risk_score` | Low. | `TopWalletBuyShareLast5m`, `WalletDiversityRatio`, `RepeatBuyerShare1m`, `Top10HolderPct`, clustering fields. | Exact Dune integer risk score mapping. |
| `sniper_intensity_ratio` | Low. | Early buy timestamps and first-minute wallet activity can be observed. | Sniper labeling rules and exact Dune ratio. |
| `mfe_multiple_30m` | Low until price path retention exists. | `LastPriceSOL`, event SOL/token prices on applied swaps. | Entry price, max price through 30m, retained price path, and Dune entry convention. |

## Section C — Earliest Feasible Scoring Checkpoint

The earliest realistic checkpoint for running the validated scorer without fabrication is minute 35.

Minute 5 is too early because the validated scorer requires `mfe_multiple_30m`, `buy_sol_0_35m`, `sell_sol_0_35m`, `sell_trade_count_5to35m`, `sell_unique_traders_5to35m`, and monetization/exit fields.

Minute 15 is still too early because the tradeable gate uses `mfe_multiple_30m` and 35m buy/sell and sell-trader windows.

Minute 30 is still not sufficient because the validated Go scorer uses 0-35m and 5-35m fields, not only a 30m price window.

Minute 35 is the first feasible checkpoint if full event history and all Dune-equivalent definitions are available. The current bridge correctly uses a 35 minute feature window, but most rows remain incomplete because Dune-only and price-path features are not yet populated.

Later than minute 35 may be necessary in production if ingress lag, bounded history truncation, or delayed sell/price observations make the 35m feature set incomplete at exactly minute 35.

## Section D — Candidate Proxy Feature Set

These currently available live fields could be candidate early proxies for missing validated inputs. They should not score or rank anything until calibrated against shadow outcomes.

- Proxy candidates for `cohort_buyer_count`: `unique_buyer_count`, `buyers_last1m`, `buyers_last5m`, `effective_buyers_1m`, `effective_buyers_5m`.
- Proxy candidates for `mfe_multiple_30m`: retained event prices derived from `SOLAmount / TokenAmount`, `last_price_sol`, future max observed price path once persisted.
- Proxy candidates for `manipulation_risk_score`: `adversarial_score`, `top_wallet_buy_share_5m`, `wallet_diversity_ratio`, `repeat_buyer_share_1m`, `top10_holder_pct`, `funding_cluster_ratio`, `cluster_compression_ratio_1m`, `cluster_compression_ratio_5m`.
- Proxy candidates for `first_minute_share`: first-minute buy SOL from `buyHistory`, total early buy SOL, `buy_sol_last_1m` only for tokens still inside minute one.
- Proxy candidates for `sniper_intensity_ratio`: first-minute buyer counts, first-minute wallet concentration, clustering compression, funding cluster ratio.
- Proxy candidates for `size_diversity_ratio`: distribution of buy SOL sizes in `buyHistory`, `top_wallet_buy_share_5m`, `wallet_diversity_ratio`.
- Proxy candidates for monetization/exit fields: `sell_trade_count`, `total_sell_sol`, `sell_sol_last_1m`, `TotalBuySOL`, `TotalSellSOL`, wallet-level buy/sell SOL maps in state.
- Proxy candidates for execution sanity, not a Dune scorer input: `liquidity_proxy_sol`, `estimated_impact_pct`, `execution_penalty`, `liquidity_pool_sol`.

## Recommendation: Shadow Calibration First Or Early Proxy First

Recommendation: shadow calibration first, because the current audit shows the validated scorer cannot run on fresh live rows without missing Dune-defined and outcome-window inputs, while the repo already exposes conservative shadow evidence that can compare matured live tokens against the validated scorer before any early proxy model is introduced.
