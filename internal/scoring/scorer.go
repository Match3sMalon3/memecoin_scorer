package scoring

import (
	"memecoin_scorer/internal/config"
	"memecoin_scorer/internal/model"
)

// Score evaluates a TokenFeatures against configured thresholds and weights,
// returning a fully populated ScoreResult.
func Score(f model.TokenFeatures, cfg config.Config) model.ScoreResult {
	t := cfg.Thresholds
	w := cfg.Weights

	tradeable := isTradeable(f, t)
	clean := tradeable && isClean(f, t)

	opp := opportunityComponent(f, t)  // 0–100
	adv := adversarialComponent(f, t)  // 0–100 (higher = more adversarial risk)
	mon := monetizationComponent(f, t) // 0–100

	// Adversarial component reduces the score — invert it for weighting.
	advContrib := 100 - adv

	composite := w.Opportunity*opp + w.Adversarial*advContrib + w.Monetization*mon
	if !tradeable {
		composite = 0
	}

	return model.ScoreResult{
		IsTradeable30m:        tradeable,
		IsCleanTradeable30m:   clean,
		OpportunityScore:      clamp(composite, 0, 100),
		OpportunityComponent:  opp,
		AdversarialComponent:  adv,
		MonetizationComponent: mon,
		SniperIntensityRatio:  f.SniperIntensityRatio,
		FirstMinuteShare:      f.FirstMinuteShare,
		WinnerExitRatio:       f.WinnerExitRatio,
	}
}

// isTradeable applies hard threshold gates — all must pass.
// Uses YAML config thresholds — no hardcoded numbers.
func isTradeable(f model.TokenFeatures, t config.Thresholds) bool {
	return f.CohortBuyerCount >= t.MinCohortBuyers &&
		f.MfeMultiple30m > t.MfeThreshold &&
		f.BuySol0_35m > f.SellSol0_35m &&
		f.SellTradeCount5to35m >= t.MinSellTrades &&
		f.SellUniqueTraders5to35m >= t.MinSellUniqueTraders
}

// isClean applies stricter quality criteria on top of tradeable.
// Uses YAML config thresholds — no hardcoded numbers.
func isClean(f model.TokenFeatures, t config.Thresholds) bool {
	return f.ManipulationRiskScore <= t.MaxManipulationRiskScore &&
		f.FirstMinuteShare <= t.MaxFirstMinuteShare &&
		f.SniperIntensityRatio <= t.MaxSniperIntensityRatio &&
		f.SizeDiversityRatio >= t.MinSizeDiversityRatio &&
		f.WalletsThatExited >= t.MinWalletsThatExited &&
		f.MedianRealizedReturnPct >= t.MinMedianRealizedReturn &&
		(f.MedianRealizedReturnPct >= t.MinRealizedReturnForClean ||
			(f.WalletsGt25Pct >= t.MinWalletsGt25PctForClean &&
				f.WinnerExitRatio >= t.MinWinnerRatioForClean))
}

// opportunityComponent scores buyer depth, MFE strength, and trade diversity (0–100).
func opportunityComponent(f model.TokenFeatures, t config.Thresholds) float64 {
	// Buyer depth: saturates at 5× minimum.
	buyerScore := clamp(float64(f.CohortBuyerCount)/float64(t.MinCohortBuyers*5), 0, 1)

	// MFE strength: excess above threshold, saturates at 3× threshold.
	var mfeScore float64
	if t.MfeThreshold > 0 {
		mfeScore = clamp((f.MfeMultiple30m-t.MfeThreshold)/(t.MfeThreshold*2), 0, 1)
	}

	// Size diversity: how far above the minimum.
	divScore := clamp((f.SizeDiversityRatio-t.MinSizeDiversityRatio)/(1-t.MinSizeDiversityRatio), 0, 1)

	return ((buyerScore + mfeScore + divScore) / 3) * 100
}

// adversarialComponent scores manipulation and launch risk (0–100, higher = more risky).
func adversarialComponent(f model.TokenFeatures, t config.Thresholds) float64 {
	// Sniper intensity: threshold = 1.0 risk unit, 2× threshold saturates.
	var sniperRisk float64
	if t.MaxSniperIntensityRatio > 0 {
		sniperRisk = clamp(f.SniperIntensityRatio/(t.MaxSniperIntensityRatio*2), 0, 1)
	}

	// First-minute share: threshold = 1.0 risk unit, 2× threshold saturates.
	var fmsRisk float64
	if t.MaxFirstMinuteShare > 0 {
		fmsRisk = clamp(f.FirstMinuteShare/(t.MaxFirstMinuteShare*2), 0, 1)
	}

	// Manipulation risk score normalised to observed max of 4.
	manipRisk := clamp(float64(f.ManipulationRiskScore)/4.0, 0, 1)

	return ((sniperRisk + fmsRisk + manipRisk) / 3) * 100
}

// monetizationComponent scores exit quality and buy-flow health (0–100).
func monetizationComponent(f model.TokenFeatures, t config.Thresholds) float64 {
	// Winner exit ratio: saturates at 2× the clean threshold.
	var winnerScore float64
	if t.MinWinnerRatioForClean > 0 {
		winnerScore = clamp(f.WinnerExitRatio/(t.MinWinnerRatioForClean*2), 0, 1)
	}

	// Median realized return: saturates at 2× the clean return threshold.
	var returnScore float64
	if t.MinRealizedReturnForClean > 0 {
		returnScore = clamp(f.MedianRealizedReturnPct/(t.MinRealizedReturnForClean*2), 0, 1)
	}

	// Buy flow: >0.5 is positive, saturates at 1.0.
	buyFlowScore := clamp((f.BuyFlowPct-0.5)/0.5, 0, 1)

	return ((winnerScore + returnScore + buyFlowScore) / 3) * 100
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
