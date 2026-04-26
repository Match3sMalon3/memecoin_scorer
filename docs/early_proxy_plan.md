# Early Proxy Plan

Date: 2026-04-26

This is not the validated final model.

It is a deterministic bridge from the historical Dune edge to live decision-time evidence. The Dune scorer remains the teacher: it showed that rare runners can exist even when broad tape is poor, so live ranking should start with early runner formation signals and show structural risk separately.

The early proxy avoids future leakage. It does not use matured MFE, realized returns, wallets-that-exited, clean/tradeable labels, or post-window sell counts unless those fields are timestamp-valid for the decision point.

Current structural gates remain risk annotations. They can add `risk_flags`, and only explicit hard-rug vetoes can zero the score: no real flow, impossible-execution/self-bundling Layer 0 reject, or top10 concentration at or above 0.95.

The deterministic additive weights are priors, not learned coefficients. They are transparent starting weights for buyer depth, effective buyer growth, buy/sell imbalance, acceleration, holder evidence, liquidity, impact, clustering, concentration, adversarial risk, and execution quality.

The next step is to compare `early_proxy` scores against matured Shadow outcomes. Calibration should measure which score bands lead to validated 30m tradeable/clean outcomes and which missing-field patterns make the score unreliable.

Interaction effects may matter and must be calibrated later. For example, high buyer count with high sniper concentration is not equivalent to high buyer count with low sniper concentration. The current bridge records those risks separately so calibration can learn whether they should become nonlinear penalties later.
