package features

// ExecutionPenalty returns a [0, 1] multiplier representing how well a standard
// trade size can be absorbed by observed pool liquidity.
//
//   - Returns 1.0 (no penalty) when liquiditySOL is large relative to tradeSOL.
//   - Drops linearly toward 0 as liquiditySOL shrinks relative to tradeSOL.
//   - Returns 0.0 when liquiditySOL <= 0 (no observed liquidity — cannot execute).
//   - Returns 1.0 when tradeSOL <= 0 or multiplier <= 0 (degenerate inputs — no penalty).
//
// Formula: min(1.0, liquiditySOL / (tradeSOL * multiplier))
//
// multiplier controls how conservative the check is:
//
//	multiplier = 20 → position must be < 5 % of pool for full score (1.0).
//	multiplier = 10 → position must be < 10 % of pool for full score.
//
// liquiditySOL is a proxy derived from observed cumulative volume
// (TotalBuySOL + TotalSellSOL). It under-estimates true AMM depth on new tokens
// and should be replaced with a real depth query when available.
func ExecutionPenalty(tradeSOL, liquiditySOL, multiplier float64) float64 {
	if tradeSOL <= 0 || multiplier <= 0 {
		return 1.0
	}
	if liquiditySOL <= 0 {
		return 0.0
	}
	ratio := liquiditySOL / (tradeSOL * multiplier)
	if ratio >= 1.0 {
		return 1.0
	}
	return ratio
}
