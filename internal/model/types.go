package model

import "time"

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
	TotalTokens        int
	TradeableCount     int
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
