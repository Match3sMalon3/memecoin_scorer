package model

import "time"

// SwapEvent is a normalised Solana swap extracted from a Helius webhook payload.
// One SwapEvent represents a single SOL-side trade (buy or sell) for one token mint.
type SwapEvent struct {
	Signature   string
	Slot        uint64
	BlockTime   time.Time
	TokenMint   string
	IsBuy       bool    // true = SOL → token (buy), false = token → SOL (sell)
	WalletAddr  string  // feePayer / initiating wallet address
	SOLAmount   float64 // in SOL (lamports divided by 1e9)
	TokenAmount float64 // raw token units as reported by the DEX
	ProgramID   string  // DEX program address or Helius source name
}

// GateResult holds the pass/fail result for one of the 7 success gates.
// Gates that lack sufficient data are marked Skipped rather than failed.
type GateResult struct {
	ID        int     `json:"gate_id"`
	Name      string  `json:"gate_name"`
	Passed    bool    `json:"passed"`
	Skipped   bool    `json:"skipped"` // insufficient data — not a hard failure
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Margin    float64 `json:"margin"` // value - threshold; positive = passing headroom
	Reason    string  `json:"reason"`
}

// EngineDecision is the output of the 7-gate engine evaluation.
// It constrains the final live label produced by ClassifyAt.
type EngineDecision struct {
	Layer0Reject   bool         `json:"layer0_reject"`
	Layer0Reason   string       `json:"layer0_reason,omitempty"`
	Gates          []GateResult `json:"gates"`
	GatesPassCount int          `json:"gates_pass_count"`
	// MaxLabel is the highest live label permitted by gate results.
	// Empty string means "no engine constraint" (all gates skipped — data unavailable).
	// Possible values: "BUY" | "READY" | "WATCH" | "AVOID"
	MaxLabel string `json:"max_label"`
	// ScoreCap is set to 40 when Gate 7 (slippage ceiling) fails; 0 otherwise.
	ScoreCap int `json:"score_cap"`
}

// TokenSnapshot is a read-only derived view of a token's live state.
// All fields are computed at snapshot time from the store's internal state.
// No mutable references are held; it is safe to pass by value.
type TokenSnapshot struct {
	Mint             string    `json:"mint"`
	FirstSeenAt      time.Time `json:"first_seen_at"`
	LastEventAt      time.Time `json:"last_event_at"`
	UniqueBuyerCount int       `json:"unique_buyer_count"`
	TotalBuySOL      float64   `json:"total_buy_sol"`
	TotalSellSOL     float64   `json:"total_sell_sol"`
	SellTradeCount   int       `json:"sell_trade_count"`
	// BuyersLast1m is the count of distinct buyer wallets in the most recent 1 minute.
	BuyersLast1m int `json:"buyers_last1m"`
	// BuyersLast5m is the count of distinct buyer wallets in the most recent 5 minutes.
	BuyersLast5m int `json:"buyers_last5m"`
	// BuyerAcceleration is BuyersLast1m divided by the buyer count in the prior 1m window
	// (the 2m-to-1m window). Returns 0 when the prior window has no buyers.
	BuyerAcceleration float64 `json:"buyer_acceleration"`
	// AgeSeconds is the elapsed seconds between FirstSeenAt and the snapshot time.
	AgeSeconds float64 `json:"age_seconds"`

	// --- Adversarial indicators (computed from buy history) ---

	// TopWalletBuyShareLast5m is the fraction of 5m buy SOL volume attributable to
	// the single largest buyer wallet. Range [0,1]. High values indicate concentration risk.
	TopWalletBuyShareLast5m float64 `json:"top_wallet_buy_share_5m"`
	// WalletDiversityRatio is unique_buyers_5m / total_buy_events_5m.
	// Low values (many buys from few wallets) suggest bot-like activity.
	// Returns 1.0 when there are no buy events in the window (no signal either way).
	WalletDiversityRatio float64 `json:"wallet_diversity_ratio"`
	// RepeatBuyerShare1m is the fraction of last-1m buyers who also appeared in the
	// prior 1m window. High values suggest wallet recycling / wash-trade patterns.
	RepeatBuyerShare1m float64 `json:"repeat_buyer_share_1m"`

	// --- Short-window buy/sell SOL volume (for sell-reversal veto) ---

	// BuySolLast1m is the total SOL spent buying in the last 1 minute.
	BuySolLast1m float64 `json:"buy_sol_last_1m"`
	// SellSolLast1m is the total SOL received from selling in the last 1 minute.
	SellSolLast1m float64 `json:"sell_sol_last_1m"`

	// --- Warm-up / confidence ---

	// TotalEventCount is the cumulative count of all swap events (buys + sells) ever
	// applied for this token. Used for warm-up gating; 0 only in tests that do not
	// populate it (in which case the event-count warm-up check is skipped).
	TotalEventCount int `json:"total_event_count"`

	// --- Clustering support (not serialised — used internally by Classify) ---

	// UniqueWalletsLast1m is the slice of distinct buyer wallet addresses active in the
	// last 1 minute. Populated by the store; omitted from JSON so it does not bloat the
	// API response. When nil, Classify falls back to raw BuyersLast1m counts.
	UniqueWalletsLast1m []string `json:"-"`
	// UniqueWalletsLast5m is the analogous slice for the 5-minute window.
	UniqueWalletsLast5m []string `json:"-"`

	// --- 7-gate fields (populated by the state store from swap event data) ---
	// Zero values signal "data not yet available" — the engine skips the relevant gate.

	// LiquidityPoolSOL is the estimated pool depth in SOL (TotalBuySOL + TotalSellSOL proxy).
	// Used by Gate 1 (Liquidity/MC) and Gate 7 (slippage ceiling). Zero = not yet computed.
	LiquidityPoolSOL float64 `json:"liquidity_pool_sol"`

	// MarketCapSOL is the estimated market cap in SOL (lastPriceSOL × totalTokenSupply).
	// Used by Gates 1 and 4. Zero = price not yet observed or supply unknown.
	MarketCapSOL float64 `json:"market_cap_sol"`

	// LastPriceSOL is the most recent observed price in SOL per token unit.
	// Derived from swap events: SOLAmount / TokenAmount. Zero = not yet observed.
	LastPriceSOL float64 `json:"last_price_sol"`
	// LastPriceReason explains why LastPriceSOL is zero.
	// Empty when a non-zero price has been derived.
	LastPriceReason string `json:"last_price_reason,omitempty"`

	// Top10HolderPct is the cumulative token share of the top-10 wallet balances [0,1].
	// Computed from per-wallet net token holdings derived from swap events.
	// Zero = not yet observed (no TokenAmount data available from DEX).
	Top10HolderPct float64 `json:"top10_holder_pct"`

	// Volume24hSOL is the total observed volume (buy + sell) within the first 24 hours.
	// Equals TotalBuySOL + TotalSellSOL when token age ≤ 24 hours.
	Volume24hSOL float64 `json:"volume_24h_sol"`

	// OrganicWinnerCount is the count of wallets that:
	// (a) bought more than 5 minutes after FirstSeenAt,
	// (b) are not the deployer wallet,
	// (c) have realised >50% profit on observed sell events.
	OrganicWinnerCount int `json:"organic_winner_count"`

	// HoldersAt30m is the distinct holder count snapshot taken when the token reached 30 minutes old.
	// Zero = token not yet 30 minutes old.
	HoldersAt30m int `json:"holders_at_30m"`

	// HoldersAt60m is the distinct holder count snapshot taken when the token reached 60 minutes old.
	// Zero = token not yet 60 minutes old.
	HoldersAt60m int `json:"holders_at_60m"`

	// HolderCount is the current count of wallets with a net positive token balance.
	HolderCount int `json:"holder_count"`
	// MarketCapReason explains why MarketCapSOL is zero.
	// Empty when a non-zero market cap has been derived.
	MarketCapReason string `json:"market_cap_reason,omitempty"`
}

// LiveSnapshot extends TokenSnapshot with a live decision classification.
// It is produced by cmd/ingestor when responding to /api/snapshots and consumed
// by cmd/dashboard in live mode.
type LiveSnapshot struct {
	TokenSnapshot
	// Decision label: BUY | READY | WATCH | AVOID
	Decision string `json:"decision"`
	// Reasons is a short list of strings explaining the label.
	Reasons []string `json:"reasons"`
	// ExecutionPenalty is the [0,1] execution quality score computed from the
	// liquidity proxy and intended trade size.
	ExecutionPenalty float64 `json:"execution_penalty"`
	// LiquidityProxySOL is TotalBuySOL + TotalSellSOL, used as a rough depth proxy.
	// This is NOT true AMM pool depth — it is cumulative observed volume and
	// understates real depth, especially on newly-launched tokens.
	LiquidityProxySOL float64 `json:"liquidity_proxy_sol"`
	// AdversarialScore is a [0,1] suspicion score combining concentration,
	// wallet diversity, and repeat-buyer signals. 0=clean, 1=maximally suspicious.
	AdversarialScore float64 `json:"adversarial_score"`
	// TradeSizeSOL is the intended position size assumed when computing execution_penalty.
	TradeSizeSOL float64 `json:"trade_size_sol"`
	// EstimatedImpactPct is TradeSizeSOL / LiquidityProxySOL * 100.
	// Expresses the trade as a percentage of observed pool activity.
	// Returns 0 when LiquidityProxySOL is zero. >15% triggers the hard impact veto.
	EstimatedImpactPct float64 `json:"estimated_impact_pct"`

	// --- Module 6B: Effective buyer clustering ---

	// EffectiveBuyers1m is the count of distinct funding-cluster roots in the last 1m.
	// When ClusterRequired=true and resolver is healthy, gates use this, not raw.
	EffectiveBuyers1m int `json:"effective_buyers_1m"`
	// EffectiveBuyers5m is the analogous count for the 5-minute window.
	EffectiveBuyers5m int `json:"effective_buyers_5m"`
	// ClusteredBuyerCount is the number of 1m wallets collapsed into shared clusters.
	ClusteredBuyerCount int `json:"clustered_buyer_count"`
	// FundingClusterRatio is ClusteredBuyerCount / BuyersLast1m. Zero when no clustering.
	FundingClusterRatio float64 `json:"funding_cluster_ratio"`
	// ClusterCompressionRatio1m = (raw_buyers_1m - effective_buyers_1m) / raw_buyers_1m.
	ClusterCompressionRatio1m float64 `json:"cluster_compression_ratio_1m"`
	// ClusterCompressionRatio5m is the same for the 5m window.
	ClusterCompressionRatio5m float64 `json:"cluster_compression_ratio_5m"`
	// ClusteringStatus is "healthy" or "degraded".
	ClusteringStatus string `json:"clustering_status"`
	// ClusteringBackend is "helius", "static", or "null".
	ClusteringBackend string `json:"clustering_backend"`
	// ClusteringRowStatus describes the per-row clustering outcome:
	// resolved | partial_fallback | full_fallback.
	ClusteringRowStatus string `json:"clustering_row_status"`
	// ClusteringTimeouts is the number of wallets in this row whose funder lookup
	// hit the request-time deadline and fell back to raw wallet roots.
	ClusteringTimeouts int `json:"clustering_timeouts"`
	// ClusteringFallbacks is the number of wallets in this row that fell back to
	// raw wallet roots because clustering resolution failed or timed out.
	ClusteringFallbacks int `json:"clustering_fallbacks"`

	// --- Module 6C: Freshness / stale signal control ---

	// SignalState is one of: "fresh" | "stale" | "expired".
	// fresh   — within the actionable age window for this label
	// stale   — WATCH signal past BUY/READY window but inside WATCH window
	// expired — beyond the label's age limit, or AVOID
	SignalState string `json:"signal_state"`
	// IsActionable is true when the signal is fresh enough to act on.
	// Dashboard default view shows only rows where is_actionable=true.
	IsActionable bool `json:"is_actionable"`

	// --- Module 6D: Warm-up / confidence ---

	// ConfidenceScore is a 0–100 composite score reflecting how much trust to place
	// in this signal. Lower when: token is young, few events, high adversarial score,
	// high impact, or high funding-cluster ratio.
	ConfidenceScore float64 `json:"confidence_score"`
	// WarmingUp is true when the token is too new or has too little activity to
	// support a confident BUY decision.
	WarmingUp bool `json:"warming_up"`

	// --- Module 6G: Positive rationale ---

	// WhyNow is a concise human-readable explanation of the positive signal.
	WhyNow string `json:"why_now"`
	// WhyNotHigher explains what is limiting the score or decision level.
	WhyNotHigher string `json:"why_not_higher"`
	// DominantBlocker is the single most important reason this row is not stronger.
	DominantBlocker string `json:"dominant_blocker"`
	// OperatorVerdict is a compact human verdict for the row's structural quality.
	OperatorVerdict string `json:"operator_verdict"`
	// ExecutionURL is a direct click-through helper for operator execution context.
	ExecutionURL string `json:"execution_url"`
	// QualityTier is the compact posture tier used by the operator hero/table.
	QualityTier string `json:"quality_tier"`
	// TriggerLine is the compact scan trigger line used by the operator hero/table.
	TriggerLine string `json:"trigger_line"`
	// NoTradeReason is the compact blocker line used when execution is not pristine.
	NoTradeReason string `json:"no_trade_reason"`

	PriorityLabel             string `json:"priority_label"`
	ActionabilityLabel        string `json:"actionability_label"`
	HistoricalAnalogueSummary string `json:"historical_analogue_summary"`
	HistoricalOutcomeBand     string `json:"historical_outcome_band"`
	HistoricalTimeToOutcome   string `json:"historical_time_to_outcome"`
	UpgradeTriggers           string `json:"upgrade_triggers"`
	InvalidateTriggers        string `json:"invalidate_triggers"`
	OperatorFocus             string `json:"operator_focus"`
	RelativeSetupLabel        string `json:"relative_setup_label"`
	TrustLabel                string `json:"trust_label"`
	TrustReason               string `json:"trust_reason"`
	AsymmetryLabel            string `json:"asymmetry_label"`
	AsymmetryReason           string `json:"asymmetry_reason"`

	// Compatibility fields used by the runtime audit builders.
	LastPriceSol float64 `json:"-"`
	MarketCapSol float64 `json:"-"`
	Layer0Reject bool    `json:"-"`

	// --- 7-gate engine result ---

	// Engine contains the per-gate pass/fail results and Layer 0 outcome.
	// Provides machine-readable explainability for every BUY/READY signal.
	Engine EngineDecision `json:"engine"`
}

type ScoredSnapshot = LiveSnapshot

// TokenRow holds the raw CSV columns from the Dune v9 query export — no derivation.
type TokenRow struct {
	TokenMint               string    `csv:"token_mint"`
	LaunchTime              time.Time `csv:"launch_time"`
	CohortBuyerCount        int       `csv:"cohort_buyer_count"`
	BuyersMin0_1            int       `csv:"buyers_min0_1"`
	BuyersMin1_5            int       `csv:"buyers_min1_5"`
	SniperIntensityRatio    float64   `csv:"sniper_intensity_ratio"`
	FirstMinuteShare        float64   `csv:"first_minute_share"`
	SizeDiversityRatio      float64   `csv:"size_diversity_ratio"`
	ManipulationRiskScore   int       `csv:"manipulation_risk_score"`
	MfeMultiple15m          float64   `csv:"mfe_multiple_15m"`
	MfeMultiple30m          float64   `csv:"mfe_multiple_30m"`
	MedianRealizedReturnPct float64   `csv:"median_realized_return_pct"`
	WalletsThatExited       int       `csv:"wallets_that_exited"`
	WalletsGt25Pct          int       `csv:"wallets_gt_25pct"`
	BuySol0_35m             float64   `csv:"buy_sol_0_35m"`
	SellSol0_35m            float64   `csv:"sell_sol_0_35m"`
	SellTradeCount5to35m    int       `csv:"sell_trade_count_5to35m"`
	SellUniqueTraders5to35m int       `csv:"sell_unique_traders_5to35m"`
	IsTradeable30m          bool      `csv:"is_tradeable_30m"`
	IsCleanTradeable30m     bool      `csv:"is_clean_tradeable_30m"`
}

// TokenFeatures embeds the raw row and adds derived features computed at parse time.
type TokenFeatures struct {
	TokenRow

	// Derived: WalletsGt25Pct / WalletsThatExited (0 when no exits)
	WinnerExitRatio float64
	// Derived: BuySol0_35m / (BuySol0_35m + SellSol0_35m) (0.5 when both zero)
	BuyFlowPct float64
}

// ScoreResult is the output of the scorer for a single token.
type ScoreResult struct {
	IsTradeable30m      bool
	IsCleanTradeable30m bool
	// OpportunityScore is the composite 0–100 score.
	OpportunityScore float64

	// Sub-component scores (each 0–100 before weighting).
	OpportunityComponent  float64 // buyer depth + MFE strength
	AdversarialComponent  float64 // manipulation, sniper, first-minute risk (higher = riskier)
	MonetizationComponent float64 // exit quality and buy flow

	// Key features carried for reporting / diagnostics.
	SniperIntensityRatio float64
	FirstMinuteShare     float64
	WinnerExitRatio      float64
}

// BacktestResult pairs features with their score and the realised labels from the CSV.
type BacktestResult struct {
	TokenMint string
	Features  TokenFeatures
	Score     ScoreResult
}

// Summary holds aggregate metrics produced by a backtest run.
type Summary struct {
	TotalTokens         int
	TradeableCount      int
	CleanTradeableCount int

	// Precision: of predicted-positive tokens, what fraction were actually positive.
	PrecisionTradeable      float64
	PrecisionCleanTradeable float64

	// BaseRate: fraction of all tokens that are actually tradeable in the dataset.
	BaseRate float64

	// Uplift: precision / base_rate — how much better than random.
	UpliftTradeable      float64
	UpliftCleanTradeable float64

	// Return distributions split by predicted label.
	MedianReturnTradeable      float64
	MedianReturnNonTradeable   float64
	MedianReturnCleanTradeable float64

	// Average adversarial feature values split by predicted tradeable label.
	AvgFirstMinuteShareTradeable    float64
	AvgFirstMinuteShareNonTradeable float64
	AvgSniperIntensityTradeable     float64
	AvgSniperIntensityNonTradeable  float64
}
