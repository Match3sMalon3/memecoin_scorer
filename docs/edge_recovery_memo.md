# Edge Recovery Memo

## Historically Validated Edge Logic

The historically validated edge appears to be the Dune v9/frozen token-level scorer, not the current live posture monitor. It classifies whether an early memecoin became tradeable around the 30 to 35 minute outcome window, then scores only tokens that pass the tradeable gate.

The core tradeable gate is implemented in `internal/scoring/scorer.go` as `isTradeable`. A token is predicted tradeable only if all of these pass:

- `cohort_buyer_count >= min_cohort_buyers`
- `mfe_multiple_30m > mfe_threshold`
- `buy_sol_0_35m > sell_sol_0_35m`
- `sell_trade_count_5to35m >= min_sell_trades`
- `sell_unique_traders_5to35m >= min_sell_unique_traders`

The frozen thresholds live in `config/scoring_config.yaml` and the scorer tests repeat them as the "frozen v9 config":

- `min_cohort_buyers: 10`
- `mfe_threshold: 1.20`
- `min_sell_trades: 20`
- `min_sell_unique_traders: 5`
- `max_manipulation_risk_score: 0`
- `max_first_minute_share: 0.25`
- `max_sniper_intensity_ratio: 0.30`
- `min_size_diversity_ratio: 0.35`
- `min_wallets_that_exited: 5`
- `min_median_realized_return: 0.0`
- `min_realized_return_for_clean: 10.0`
- `min_winner_ratio_for_clean: 0.30`
- `min_wallets_gt_25pct_for_clean: 3`
- weights: opportunity `0.50`, adversarial `0.30`, monetization `0.20`

Clean tradeable is stricter than tradeable. `isClean` requires the tradeable gate plus clean launch and monetization features: manipulation risk at zero, first-minute share and sniper intensity below caps, size diversity above floor, at least five wallets exited, non-negative median realized return, and either median realized return at least 10% or at least three wallets over 25% profit with a winner-exit ratio at least 30%.

The score is not the gate itself. `Score` computes:

- opportunity component: buyer depth, MFE strength, size diversity
- adversarial component: sniper intensity, first-minute share, manipulation risk
- monetization component: winner-exit ratio, median realized return, buy-flow share
- composite: `0.50 * opportunity + 0.30 * (100 - adversarial) + 0.20 * monetization`
- if tradeable is false, composite score is forced to zero

The predicted targets are `is_tradeable_30m` and `is_clean_tradeable_30m` from Dune token-level CSVs. The Dune outcome validation CSVs also carry stronger 35m and 60m outcome labels (`outcome_label_strong_35m`, `outcome_label_strong_60m`) with MFE/MAE and realized return fields, but those fields are not wired into the Go scorer parser.

Evidence in repo:

- `internal/model/types.go`: `TokenRow`, `TokenFeatures`, `ScoreResult`, and `Summary` define the Dune columns, derived features, scorer output, precision/uplift reporting.
- `internal/features/features.go`: parses Dune CSV exports and derives `WinnerExitRatio` and `BuyFlowPct`.
- `internal/scoring/scorer.go`: implements the tradeable/clean gates and composite score.
- `config/scoring_config.yaml`: stores the frozen scorer thresholds and weights.
- `internal/scoring/scorer_test.go`: verifies each hard gate and clean gate.
- `internal/backtest/backtest.go`: runs the scorer across CSVs and computes precision, base rate, uplift, and return splits.
- `internal/backtest/backtest_test.go`: verifies scorer behavior on clean winners, zero exits, negative returns, and high-MFE/bad-monetization fixtures.
- `cmd/validate/main.go`: explicitly says it loads a Dune export and verifies Go scoring output against Dune output.
- `cmd/scorer/main.go` and `internal/report/report.go`: produce per-token predictions and summary JSON/CSV.
- `testdata/dune_token_level_7d_deduped.csv` and `testdata/dune_token_level_14d_deduped.csv`: large Dune token-level fixtures with the scorer inputs and labels.
- `testdata/dune_export_7d.csv`: Dune summary shows 21,914 tokens, 748 tradeable 30m tokens (3.41%), 531 tradeable 15m tokens (2.42%), 3 clean tradeable tokens (0.01%), median realized return 21.0 for tradeable vs 1.3 for non-tradeable, and median clean return 42.2.
- `testdata/dune_export_14d.csv`: Dune summary shows 31,302 tokens, 1,673 tradeable 30m tokens (5.34%), 1,200 tradeable 15m tokens (3.83%), 7 clean tradeable tokens (0.02%), median realized return 21.2 for tradeable vs 1.8 for non-tradeable, and median clean return 52.1.

What is uncertain or missing:

- The original Dune SQL is not present, only CSV exports and Go replication logic.
- The exact Dune formula for `is_tradeable_30m` and `is_clean_tradeable_30m` is inferred from column names, config, tests, and Go gates.
- `min_opportunity_score_to_trade: 62.0` exists in config and `internal/config/config.go`, but `internal/scoring/scorer.go` does not use it to set `IsTradeable30m`.
- The Go scorer depends on outcome-window fields such as MFE, sell counts, exited wallets, and realized return, so it is not directly executable at token birth without either waiting for maturity or introducing unvalidated proxies.
- Recall is not computed in `model.Summary`; precision, base rate, uplift, and median returns are.
- The Dune outcome validation CSVs use 5m-to-35m and 5m-to-60m field names that the current parser does not accept.

## Current Live Terminal Behavior vs Validated Edge

The live terminal currently ranks on live posture and execution health, not the Dune validated scorer.

Backend live classification is produced by `internal/live/decision.go`. `ClassifyAt` ranks each token into BUY/READY/WATCH/AVOID using:

- execution quality from `features.ExecutionPenalty`
- estimated impact and hard impact veto at 15%
- effective buyer counts after funder clustering
- buyer acceleration and strong 1m velocity
- buy-vs-sell pressure
- live adversarial score from top-wallet concentration, wallet diversity, and repeat buyers
- clustering health, freshness, and warm-up gates
- the 7-gate `internal/engine` ceiling

The current best row is selected in `AssignPriorityLabels` via `bestPriorityMint` and `priorityLess`, which compares confidence score, clustering row status, adversarial score, effective buyers over 5m, and estimated impact. The dashboard then uses `rankedWowRows`, `wowRankScore`, and `bestSetupScoreGo` in `cmd/dashboard/main.go`, adding large bonuses for `best_on_tape`, `monitor_for_upgrade`, pristine posture, actionability, decision label, resolved clustering, zero funding-cluster ratio, low impact, execution quality, and market cap.

Current filters not obviously part of the validated Dune thesis:

- Posture modes (`pristine`, `defensive`, `no-trade`)
- `QualityTier`, `TriggerLine`, `NoTradeReason`
- 7-gate organic success engine: liquidity/MC, top-10 holder concentration, shared funder ratio, volume/MC, organic winners, holder growth, slippage ceiling
- Layer 0 hard rejects for self-bundling and minimum executable liquidity
- Cluster backend health requirement for BUY/READY
- Effective-buyer clustering from funder parents
- Freshness expiration and token warm-up windows
- Execution penalty based on cumulative observed live volume as a liquidity proxy
- Dashboard hero/table ranking by confidence and posture rather than `OpportunityScore` or Dune tradeable labels

Where drift occurred:

- `cmd/ingestor/main.go` builds live API rows from `live.ClassifyAt`; it does not call `scoring.Score`.
- `internal/live/decision.go` explicitly says it is separate from the offline batch scorer, and its defaults are documented as unvalidated priors.
- `internal/engine/scorer.go` names itself a validated 7-point framework, but `DefaultEngineConfig` also says the defaults are unvalidated priors.
- `cmd/dashboard/main.go` still contains offline snapshot fields and table code for `predicted_tradeable` and `opportunity_score`, but the live operator view now consumes `LiveSnapshot` posture fields and ranks on `wowRankScore`.
- The live store derives buyer velocity, liquidity proxy, market cap proxy, holder concentration, and organic winners from in-memory swap state; it does not derive the Dune `TokenFeatures` contract needed by `scoring.Score`.

## Minimal Reconnection Plan

Reuse existing scorer logic first:

- `config/scoring_config.yaml`
- `internal/features.ParseReader` / `ParseCSV`
- `internal/scoring.Score`
- `internal/backtest.Run`
- `internal/report.WriteCSV` / `WriteJSON`

Live data fields needed to drive the validated contract:

- Already close or derivable from live state: token mint, launch/first seen time, unique/cohort buyers, buyers in minute 0-1, buyers in minutes 1-5, buy SOL, sell SOL, sell trade count, sell unique traders, first-minute share, size diversity proxy, buy-flow share.
- Not currently equivalent or only partially represented: sniper intensity ratio, manipulation risk score, MFE multiple at 15m/30m, median realized return, wallets that exited, wallets over 25% profit, Dune clean labels, Dune strong 35m/60m outcome labels.
- Requires richer state/history: price series at entry and outcome windows, wallet-level realized PnL, per-wallet exit tracking, cohort membership, and Dune-equivalent sniper/manipulation definitions.

Smallest viable wiring path:

1. Add a mapper from mature live token state to `model.TokenFeatures` without changing endpoints or ranking.
2. Run `scoring.Score` in shadow mode only once enough 35m outcome-window fields exist.
3. Persist or expose shadow fields such as validated tradeable prediction, clean prediction, and opportunity score beside the current live posture fields.
4. Compare live posture decisions against shadow validated labels and realized outcomes before using the score to reorder the operator view.
5. Only after parity is proven, let live ranking prefer Dune scorer outputs over posture-only heuristics.

Can defer:

- UI redesign.
- New scoring features.
- Retuning thresholds.
- Endpoint path changes.
- Replacing Helius ingress.
- True DEX pool depth.
- Dune SQL reconstruction, unless exact label parity becomes mandatory.

Shadow mode has now been added as the first reconnection step. Each live row can expose a `shadow` result that either blocks scoring with explicit missing fields or, once mature validated inputs are complete, runs the existing `internal/scoring.Score` path without replacing live ranking.

## Code Inventory

Validated/offline scorer:

- `config/scoring_config.yaml`: scorer thresholds and weights.
- `internal/config/config.go`: `Thresholds`, `Weights`, `Config`, `Load`.
- `internal/model/types.go`: `TokenRow`, `TokenFeatures`, `ScoreResult`, `BacktestResult`, `Summary`.
- `internal/features/features.go`: `ParseCSV`, `ParseReader`, `parseReader`, `recordToRow`, `enrich`.
- `internal/scoring/scorer.go`: `Score`, `isTradeable`, `isClean`, `opportunityComponent`, `adversarialComponent`, `monetizationComponent`, `clamp`.
- `internal/backtest/backtest.go`: `Run`, `computeSummary`.
- `internal/report/report.go`: `WriteCSV`, `WriteJSON`.
- `cmd/scorer/main.go`: scorer CLI.
- `cmd/validate/main.go`: Dune validation CLI.
- `internal/scoring/scorer_test.go`: frozen v9 scorer gate tests.
- `internal/backtest/backtest_test.go`: backtest fixture tests.
- `internal/features/features_test.go`: parser and derived feature tests.
- `testdata/dune_token_level_7d_deduped.csv`, `testdata/dune_token_level_14d_deduped.csv`: Dune token-level scorer fixtures.
- `testdata/dune_export_7d.csv`, `testdata/dune_export_14d.csv`: Dune summary outputs.
- `testdata/dune_outcome_validation_7d.csv`, `testdata/dune_outcome_validation_14d.csv`: outcome validation exports.

Current live terminal:

- `internal/model/types.go`: `SwapEvent`, `TokenSnapshot`, `LiveSnapshot`, `EngineDecision`, `GateResult`.
- `internal/state/store.go`: `Store.Apply`, `RecentTokens`, `deriveSnapshot`, `derivedMarketCap`, `volume24h`.
- `internal/live/decision.go`: `DefaultLiveConfig`, `Classify`, `ClassifyAt`, `finalize`, `computeConfidence`, `BuildExecutionURL`, `BuildDexscreenerURL`, `BuildQualityTier`, `BuildTriggerLine`, `BuildNoTradeReason`, `BuildWhyNow`, `BuildDominantBlocker`, `BuildActionabilityLabel`, `AssignPriorityLabels`, `bestPriorityMint`, `priorityLess`, `adversarialScore`, `checkBUY`, `checkREADY`, `checkWATCH`.
- `internal/engine/scorer.go`: `DefaultEngineConfig`, `EvaluateGates`, `layer0`, `gate1LiquidityMC`, `gate2SupplyConc`, `gate3BundleFunder`, `gate4VolumeMC`, `gate5OrganicWinners`, `gate6HolderGrowth`, `gate7SlippageCeiling`, `ComputeTop10HolderPct`, `CountHolders`.
- `cmd/ingestor/main.go`: `makeSnapshotsHandler`, `logSignalLine`, live config construction.
- `cmd/dashboard/main.go`: `fetchLiveSnapshots`, `handleLiveSnapshots`, `rankedWowRows`, `wowRankScore`, `wowPagePosture`, `renderWowScanRows`, `chooseBestSetupGo`, `bestSetupScoreGo`.
- `docs/go_live_checklist.md`: live decision thresholds described as unvalidated priors.
- `docs/operator_runbook.md`: live limitations for liquidity proxy, clustering, in-memory store, freshness, and warm-up gates.
