package setup

import (
	"strings"

	"memecoin_scorer/internal/model"
)

func Classify(s model.LiveSnapshot) model.SetupResult {
	result := model.SetupResult{
		Mode:              model.SetupAvoid,
		ProxyScore:        s.EarlyProxy.Score,
		AuthenticityScore: s.Authenticity.Score,
		VelocityScore:     velocityScore(s),
		Reasons:           []string{},
		Blockers:          []string{},
		Invalidation: []string{
			"authenticity severity worsens",
			"real pool depth falls below 5 SOL",
			"estimated impact reaches 50%",
			"buyer flow disappears",
		},
	}

	switch {
	case noRealFlow(s):
		result.Mode = model.SetupDead
		result.Blockers = append(result.Blockers, "no real flow")
	case s.Top10HolderPct >= 0.95:
		result.Mode = model.SetupDead
		result.Blockers = append(result.Blockers, "top10 holder concentration >= 0.95")
	case s.ClusteringRowStatus == "full_fallback":
		result.Mode = model.SetupDead
		result.Blockers = append(result.Blockers, "full_fallback clustering")
	case s.RealPoolDepthSOL == 0 && s.LiquidityProxyReliable:
		result.Mode = model.SetupDead
		result.Blockers = append(result.Blockers, "verified pool depth is zero")
	case s.RealPoolDepthSOL >= 0 && s.RealPoolDepthSOL < 5:
		result.Mode = model.SetupAvoid
		result.Blockers = append(result.Blockers, "real pool depth below 5 SOL")
	case s.EstimatedImpactPct >= 50:
		result.Mode = model.SetupAvoid
		result.Blockers = append(result.Blockers, "estimated impact >= 50%")
	case manipulatedMomentum(s):
		result.Mode = model.SetupManipulatedMomentum
		result.Reasons = append(result.Reasons, s.Authenticity.Flags...)
	case wowLaunch(s):
		result.Mode = model.SetupLaunchWOW
		result.Reasons = append(result.Reasons, "fresh launch with clean authenticity and adequate velocity")
	case wowBonding(s):
		result.Mode = model.SetupBondingWOW
		result.Reasons = append(result.Reasons, "bonding curve progress and velocity are strong")
	case wowMigration(s):
		result.Mode = model.SetupMigrationWOW
		result.Reasons = append(result.Reasons, "migration window with clean distribution")
	case wowRevival(s):
		result.Mode = model.SetupRevivalWOW
		result.Reasons = append(result.Reasons, "older token has authentic fresh demand")
	case s.EarlyProxy.Score >= 45 && s.EarlyProxy.Score < 62 && s.Authenticity.Severity != "high":
		result.Mode = model.SetupWatch
		result.Reasons = append(result.Reasons, "proxy score in watch range")
	default:
		result.Mode = model.SetupAvoid
		result.Blockers = append(result.Blockers, "setup requirements not met")
	}

	if isWOW(result.Mode) {
		result.ScoreTier = scoreTier(s)
	}
	assignAction(&result)
	return result
}

func assignAction(result *model.SetupResult) {
	switch result.Mode {
	case model.SetupLaunchWOW, model.SetupBondingWOW, model.SetupMigrationWOW, model.SetupRevivalWOW:
		result.Action = model.ActionPaperLog
	case model.SetupManipulatedMomentum:
		result.Action = model.ActionExitAvoid
	case model.SetupWatch:
		result.Action = model.ActionWatch5M
	case model.SetupAvoid, model.SetupDead:
		result.Action = model.ActionNoTrade
	default:
		result.Action = model.ActionNoTrade
	}
}

func noRealFlow(s model.LiveSnapshot) bool {
	return s.BuyersLast1m == 0 && s.BuyersLast5m == 0
}

func manipulatedMomentum(s model.LiveSnapshot) bool {
	return (s.Authenticity.Severity == "high" || s.Authenticity.Severity == "medium") &&
		s.EarlyProxy.Score >= 50 &&
		velocityBonus(s) > 0
}

func wowLaunch(s model.LiveSnapshot) bool {
	return s.TokenMode == model.TokenModeLaunch &&
		hasLaunchConfidence(s) &&
		s.LaunchAgeSeconds != nil &&
		*s.LaunchAgeSeconds < 900 &&
		s.EarlyProxy.Score >= 62 &&
		s.Authenticity.Severity == "none" &&
		s.ClusteringRowStatus != "partial_fallback" &&
		velocityAdequate(s) &&
		liquidityReliableForWOW(s)
}

func hasLaunchConfidence(s model.LiveSnapshot) bool {
	return s.LaunchConfidence == model.LaunchConfidenceExact ||
		s.LaunchConfidence == model.LaunchConfidenceInferred
}

func wowBonding(s model.LiveSnapshot) bool {
	return s.TokenMode == model.TokenModeBonding &&
		s.BondingCurveProgressPct > 30 &&
		s.BondingVelocitySolPerMin >= 0.1 &&
		authLowOrNone(s) &&
		liquidityReliableForWOW(s)
}

func wowMigration(s model.LiveSnapshot) bool {
	return s.TokenMode == model.TokenModeMigration &&
		s.EarlyProxy.Score >= 55 &&
		authLowOrNone(s) &&
		liquidityReliableForWOW(s)
}

func wowRevival(s model.LiveSnapshot) bool {
	return s.TokenMode == model.TokenModeRevival &&
		s.EarlyProxy.Score >= 55 &&
		s.Authenticity.Severity == "none" &&
		hasFreshDemand(s) &&
		liquidityReliableForWOW(s)
}

func authLowOrNone(s model.LiveSnapshot) bool {
	return s.Authenticity.Severity == "none" || s.Authenticity.Severity == "low"
}

func velocityAdequate(s model.LiveSnapshot) bool {
	return s.SolPerTrade5m >= 0.05 || s.SolPerUniqueBuyer5m >= 0.1 || s.BuySolLast1m >= 0.1
}

func hasFreshDemand(s model.LiveSnapshot) bool {
	totalBuySOL5m := s.SolPerUniqueBuyer5m * float64(max(s.BuyersLast5m, s.EffectiveBuyers5m))
	return (s.BuyersLast5m >= 5 || s.EffectiveBuyers5m >= 5) &&
		(s.BuySolLast1m > 0 || totalBuySOL5m > 0)
}

func liquidityReliableForWOW(s model.LiveSnapshot) bool {
	return s.RealPoolDepthSOL >= 5 && s.LiquidityProxyReliable
}

func velocityBonus(s model.LiveSnapshot) float64 {
	bonus := 0.0
	if s.SolPerTrade5m >= 0.5 {
		bonus += 10
	}
	if s.SolPerTrade5m >= 1.0 {
		bonus += 10
	}
	if s.SolPerUniqueBuyer5m >= 1.0 {
		bonus += 5
	}
	return bonus
}

func velocityScore(s model.LiveSnapshot) float64 {
	score := velocityBonus(s) * 4
	if score > 100 {
		return 100
	}
	return score
}

func scoreTier(s model.LiveSnapshot) string {
	if s.EarlyProxy.Score >= 80 && s.Authenticity.Score >= 80 {
		return "APEX"
	}
	if s.EarlyProxy.Score >= 62 && s.Authenticity.Score >= 60 {
		return "HIGH"
	}
	return "LOW"
}

func isWOW(mode model.SetupMode) bool {
	return strings.HasSuffix(string(mode), "_WOW")
}
