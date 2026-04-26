# Shadow Calibration Plan

Shadow calibration logs evidence for tokens that have both early live checkpoints and a mature validated shadow score. It does not change ranking, add a proxy model, or alter existing endpoint paths.

## What Is Being Logged

The in-memory calibration recorder captures one record per token. A record includes:

- token mint and first seen time
- checkpoint snapshots at 5m, 15m, and 30m when the token is actually observed in those windows
- live posture, decision, signal state, operator verdict, confidence, priority, actionability, and quality tier at each checkpoint
- compact live feature summaries: buyer counts, event count, effective buyers, buy/sell flow, execution quality, estimated impact, adversarial score, clustering status/compression, holder concentration, engine status, liquidity, market cap, and price
- mature shadow outputs when available: `validated_tradeable_30m`, `validated_clean_30m`, `opportunity_score`, and `compared_at`

Rows are emitted from `/api/calibration-samples?limit=100` only after they have at least one real early checkpoint and a complete mature shadow score. This avoids fabricating missing checkpoints or publishing rows that cannot yet compare early state with the validated scorer.

## Why This Exists

The current live terminal and the Dune-validated scorer are separate systems. The live terminal ranks posture and execution quality in real time. The validated scorer requires mature 30m/35m fields such as MFE, sell counts, realized returns, exits, and clean-tradeability features.

Calibration evidence lets us compare what the live system believed at minute 5, 15, or 30 against what the validated scorer says after maturity. That is the necessary bridge before any early proxy should be considered.

## How It Tests Whether An Early Proxy Is Justified

A proxy is only justified if early checkpoint features repeatedly line up with mature validated outcomes. The calibration rows give a compact dataset for checking questions like:

- Did high early effective buyer counts later become validated tradeable tokens?
- Did high early adversarial score or clustering compression predict non-clean outcomes?
- Did early execution quality and impact separate viable tokens from bad tape?
- Did live priority/actionability labels agree or disagree with the validated scorer after maturity?

If enough rows accumulate and the same early signals consistently separate validated winners from non-winners, then an early proxy can be designed against evidence. If the calibration rows are sparse, noisy, or mostly shadow-incomplete, the next step should be better feature capture rather than a new model.
