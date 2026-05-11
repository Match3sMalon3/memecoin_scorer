# WOW v2 — Anti-Bullshit Runner Intelligence

## Question
Is this a genuine asymmetric trade setup, what type is it, and
is it clean enough to act on?

## Key invariant
A token may have high score and still not be WOW.
Score suggests. Authenticity filters. Liquidity verifies.
Mode contextualizes. Setup classification decides.
Operator action remains conservative.

## Setup modes (top-level state)
- LAUNCH_WOW           — fresh launch, clean structure
- BONDING_WOW          — bonding-curve velocity
- MIGRATION_WOW        — migration window clean
- REVIVAL_WOW          — older token, authentic fresh demand
- MANIPULATED_MOMENTUM — moving but manufactured
- WATCH                — promising, below threshold
- AVOID                — failed authenticity or execution
- DEAD                 — no flow or terminal

## Token modes (classified by age and context)
- launch    : age < 15 minutes
- bonding   : Pump.fun token, bonding curve active, not migrated
- migration : within ±10 minutes of Raydium migration
- revival   : age >= 15 minutes AND has fresh demand
- unknown   : default fallback

## Authenticity (mandatory filter)
A token cannot be classified WOW if it shows:
- Bundle bot evidence (where coverage allows verification)
- Sniper bot evidence (where coverage allows verification)
- Bump bot evidence
- Full clustering fallback
- Partial fallback (for launch_mode only)
- Top10 holder concentration >= 0.95
- Mechanical candle rhythm (inter-arrival CV < 0.40)
- Identical or near-identical buy sizes
  (>= 60% of buys within 10% of median SOL amount)

If authenticity flags trigger BUT velocity and demand are strong,
classify as MANIPULATED_MOMENTUM instead of AVOID.

Coverage honesty:
- bundle_bot_confidence:  exact | approximate | unavailable
- sniper_bot_confidence:  exact | approximate | unavailable
- bump_bot_confidence:    exact | approximate | unavailable
A missing creation block does NOT silently mean "clean".

## Liquidity contract (locked)
- real_pool_depth_sol >= 0   → verified
- real_pool_depth_sol == -1  → unknown, observed proxy only
A token cannot be classified WOW with unreliable liquidity.

## Velocity features (mandatory scoring inputs)
- sol_per_trade_5m
- sol_per_unique_buyer_5m
- bonding_curve_progress_pct (when applicable)
- bonding_velocity_sol_per_min (when applicable)

WOW rewards fast committed capital, not transaction count.

## Operator actions (Phase 1 — locked)
  LAUNCH_WOW / BONDING_WOW / MIGRATION_WOW / REVIVAL_WOW → PAPER_LOG
  MANIPULATED_MOMENTUM                                   → EXIT_AVOID
  WATCH                                                  → WATCH_5M
  AVOID / DEAD                                           → NO_TRADE

No precision-based override in Phase 1. ENTER_SMALL and
ENTER_ALLOWED do not appear as default actions.

## Internal score tier (within a valid mode only)
APEX / HIGH / LOW. Never a standalone state.

## Forbidden as standalone row states
APEX, CANDIDATE, SIGNAL, forming, defensive, posture, bad tape,
structural quality filter, copy trading, kol, alpha.

## Out of scope for Phase 1
Outcome schema migration, alert broker, SSE, precision override,
Telegram, auto-trading, deployer veto if devprint absent.

## Acceptance test (Phase 1)
Every live row has: setup.mode, setup.action, authenticity result
(with coverage fields), velocity (sol_per_trade_5m), token_mode.
Dashboard row state is setup.mode, never EarlyProxy.Band.
