# Research-Backed Authenticity Layer

## Purpose

The live runner terminal must not treat every burst of fresh buying as organic runner formation. BANANA-style migrated or old-token pumps can show visible buy flow while being driven by interval bots, bump cycles, sniper concentration, or recycled wallet groups. This layer adds decision-time authenticity evidence before a row can remain a RUNNER.

## Luo Bot Detectors Implemented

### Bundle Bot

Exact bundle detection requires the token creation slot and non-creator buys in that same slot. The current live event stream does not provide a verified creation slot for every token, so the live detector uses `first_seen_slot` as `first_seen_slot_proxy`.

Approximate bundle detection flags multiple non-creator wallets buying in the first observed slot. The confidence is reported as `approximate`, and the evidence source is visible through `bot_flags`.

Policy: approximate bundle activity is a severe risk. Exact bundle evidence, when creation slot support is added, should be a hard launch-bonding veto.

### Sniper Bot

Exact sniper detection requires the launch slot and non-creator buys within the first five blocks after launch. Until verified creation slots are available, the live detector uses buys within five slots of `first_seen_slot`.

The detector tracks sniper buy count, unique wallets, sniper SOL, and sniper share of early buy SOL. High sniper share downgrades runner eligibility, and sniper share combined with holder concentration or fallback clustering blocks RUNNER.

### Bump Bot

Bump detection groups events by wallet, sorts each wallet's activity by time and slot, and counts repeated buy/sell flips with near-identical token quantities. The near-same threshold is 1 percent of token quantity. If token quantity is unavailable, the detector uses a SOL amount fallback with looser tolerance and keeps that as fallback evidence.

Policy: bump bot activity hard downgrades a row. A RUNNER becomes non-runner, and the operator action becomes AVOID.

## Mechanical Pattern Detection

The authenticity layer computes:

- buy interarrival mean/std/CV
- repeated buy size share
- round-clock aligned buys
- same-wallet reentry and alternating buy/sell cycles
- top wallet flow share
- mechanicality score

Flags include regular interval buys, repeated identical buy sizes, round-clock aligned buys, structured sell-buy cycle, and concentrated wallet flow.

Labels:

- `organic`: no strong manipulation pattern
- `mixed`: weak or isolated suspicious evidence
- `mechanical`: interval, size, or cycle structure is material
- `bot_like`: severe mechanicality or bump activity

BANANA-style mechanical interval pumps are downgraded because old/migrated tokens with repeated buy cadence or structured cycles are not launch WOW candidates.

## Liquidity Velocity

Liquidity velocity is added for early bonding and launch-like rows without blindly increasing score.

Computed fields:

- `sol_per_trade_5m`
- `sol_per_buyer_5m`
- `vsol_per_trade`
- `vsol_per_minute`
- `raw_liquidity_velocity`
- `organic_liquidity_velocity`
- `liquidity_velocity_label`

Raw velocity counts all observed buys. Organic velocity excludes detected bump wallets and bot-like traffic. Only organic velocity can uplift a score. Strong raw velocity with weak organic velocity is treated as bot-contaminated flow and caps runner eligibility.

## Signal Mode Split

Rows are classified into:

- `launch_bonding`: young launch-like or bonding-curve evidence
- `revival_existing_token`: old token with fresh flow but no launch evidence
- `migrated_amm_momentum`: old token with verified AMM/pool liquidity evidence
- `unknown`: insufficient verified context

Old tokens may still be tradable as revival or AMM momentum setups, but they must not be labeled as launch WOW. The dashboard exposes `signal_mode`, `runner_subtype`, `authenticity_label`, `bot_flags`, `why_not_wow`, and `operator_action`.

## Operator Rules

- RUNNER requires authenticity label not `bot_like`.
- RUNNER requires no bundle bot and no bump bot.
- RUNNER requires clustering not `full_fallback`.
- RUNNER is downgraded when mechanical interval buying is detected.
- Launch RUNNER requires clean or defensible organic velocity.
- Strong raw velocity with bot flags does not uplift.
- Full fallback clustering blocks actionable RUNNER even with strong flow.

The anti-manipulation layer is evidence-bearing, not cosmetic: API rows carry the exact flags and the dashboard shows why a row was downgraded or vetoed.
