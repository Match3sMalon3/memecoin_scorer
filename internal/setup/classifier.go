package setup

import (
	"fmt"
	"strings"

	"memecoin_scorer/internal/model"
)

type blocker struct {
	text     string
	severity string
}

func Classify(s model.LiveSnapshot) model.SetupResult {
	blockers := synthesizeBlockers(s)
	result := model.SetupResult{
		Mode:              model.SetupAvoid,
		ProxyScore:        s.EarlyProxy.Score,
		AuthenticityScore: s.Authenticity.Score,
		VelocityScore:     velocityScore(s),
		Reasons:           []string{},
		Blockers:          blockerTexts(blockers),
		BlockerSeverity:   maxSeverity(blockers),
		Invalidation: []string{
			"authenticity severity worsens",
			"real pool depth falls below 5 SOL",
			"estimated impact reaches 50%",
			"buyer flow disappears",
		},
	}

	switch {
	case hasDeadBlocker(blockers):
		result.Mode = model.SetupDead
	case manipulatedMomentum(s):
		result.Mode = model.SetupManipulatedMomentum
		result.Reasons = append(result.Reasons, s.Authenticity.Flags...)
	case reviewCandidate(s, blockers):
		result.Mode = model.SetupReviewCandidate
		result.Reviewable = true
		result.ReviewReason = reviewReason(s, blockers)
		result.Reasons = append(result.Reasons, "high-score setup requires operator review before WOW")
	case hasAvoidBlocker(blockers):
		result.Mode = model.SetupAvoid
	case wowLaunch(s):
		result.Mode = model.SetupLaunchWOW
		result.Reasons = append(result.Reasons, "launch context verified with clean authenticity and adequate velocity")
	case wowBonding(s):
		result.Mode = model.SetupBondingWOW
		result.Reasons = append(result.Reasons, "bonding curve progress and velocity are strong")
	case wowMigration(s):
		result.Mode = model.SetupMigrationWOW
		result.Reasons = append(result.Reasons, "migration window with clean distribution")
	case wowRevival(s):
		result.Mode = model.SetupRevivalWOW
		result.Reasons = append(result.Reasons, "older token has authentic fresh demand")
	case hasWatchBlocker(blockers) || (s.EarlyProxy.Score >= 45 && s.EarlyProxy.Score < 62 && s.Authenticity.Severity != "high"):
		result.Mode = model.SetupWatch
		if len(result.Reasons) == 0 {
			result.Reasons = append(result.Reasons, "watchable but blocked from WOW")
		}
	default:
		result.Mode = model.SetupAvoid
	}

	if isWOW(result.Mode) {
		result.ScoreTier = scoreTier(s)
	}
	assignAction(&result)
	return result
}

func synthesizeBlockers(s model.LiveSnapshot) []blocker {
	var out []blocker
	add := func(text, severity string) {
		if text == "" {
			return
		}
		for _, b := range out {
			if b.text == text {
				return
			}
		}
		out = append(out, blocker{text: text, severity: severity})
	}

	if noRealFlow(s) {
		add("no real flow", "dead")
	}
	for _, b := range concentrationBlockers(s) {
		add(b.text, b.severity)
	}
	switch s.ClusteringRowStatus {
	case "full_fallback":
		add("full clustering fallback", "dead")
	case "partial_fallback":
		add("partial clustering fallback", "avoid")
	}
	for _, b := range authenticityBlockers(s) {
		add(b.text, b.severity)
	}
	if !liquidityVerified(s) {
		add("unverified liquidity", "avoid")
	}
	if s.RealPoolDepthSOL == 0 && s.LiquidityProxyReliable {
		add("verified pool depth is zero", "dead")
	} else if s.RealPoolDepthSOL >= 0 && s.RealPoolDepthSOL < 5 {
		add("verified pool depth below 5 SOL", "avoid")
	}
	if s.EstimatedImpactPct >= 50 {
		add("estimated impact >= 50%", "avoid")
	}
	if !velocityAdequate(s) {
		add("weak velocity: low SOL per trade", "watch")
	}
	if unknownCatalyst(s) {
		add("unknown catalyst", "watch")
	}
	if s.EarlyProxy.Score < 62 {
		add(fmt.Sprintf("proxy score %.0f below WOW threshold 62", s.EarlyProxy.Score), "watch")
	}
	return out
}

func concentrationBlockers(s model.LiveSnapshot) []blocker {
	if s.HolderCount == 0 {
		return []blocker{{text: "holder distribution unavailable", severity: "avoid"}}
	}
	pct := s.Top10HolderPct * 100
	switch {
	case s.HolderCount < 10 && s.Top10HolderPct >= 0.95:
		return []blocker{{text: fmt.Sprintf("distribution immature: %d holders, top10 %.1f%%", s.HolderCount, pct), severity: "avoid"}}
	case s.HolderCount >= 10 && s.Top10HolderPct >= 0.95:
		return []blocker{{text: fmt.Sprintf("terminal holder concentration: top10 %.1f%%", pct), severity: "dead"}}
	case s.Top10HolderPct >= 0.90:
		return []blocker{{text: fmt.Sprintf("near-terminal holder concentration %.1f%%", pct), severity: "avoid"}}
	case s.Top10HolderPct >= 0.85:
		return []blocker{{text: fmt.Sprintf("high holder concentration %.1f%%", pct), severity: "watch"}}
	default:
		return nil
	}
}

func authenticityBlockers(s model.LiveSnapshot) []blocker {
	var out []blocker
	add := func(text string) {
		out = append(out, blocker{text: text, severity: "avoid"})
	}
	if s.Authenticity.BundleBot {
		add("bundle bot evidence")
	}
	if s.Authenticity.SniperBot {
		add("sniper bot evidence")
	}
	if s.Authenticity.BumpBot {
		add("bump bot evidence")
	}
	if s.Authenticity.MechanicalRhythm {
		add("mechanical rhythm")
	}
	if s.Authenticity.IdenticalBuySizes {
		add("identical buy sizes")
	}
	return out
}

func blockerTexts(blockers []blocker) []string {
	out := make([]string, 0, len(blockers))
	for _, b := range blockers {
		out = append(out, b.text)
	}
	return out
}

func maxSeverity(blockers []blocker) string {
	severity := "none"
	for _, b := range blockers {
		if severityRank(b.severity) > severityRank(severity) {
			severity = b.severity
		}
	}
	return severity
}

func severityRank(severity string) int {
	switch severity {
	case "dead":
		return 3
	case "avoid":
		return 2
	case "watch":
		return 1
	default:
		return 0
	}
}

func hasDeadBlocker(blockers []blocker) bool {
	for _, b := range blockers {
		if b.severity == "dead" {
			return true
		}
	}
	return false
}

func hasAvoidBlocker(blockers []blocker) bool {
	for _, b := range blockers {
		if b.severity == "avoid" {
			return true
		}
	}
	return false
}

func hasWatchBlocker(blockers []blocker) bool {
	for _, b := range blockers {
		if b.severity == "watch" {
			return true
		}
	}
	return false
}

func assignAction(result *model.SetupResult) {
	switch result.Mode {
	case model.SetupLaunchWOW, model.SetupBondingWOW, model.SetupMigrationWOW, model.SetupRevivalWOW:
		result.Action = model.ActionPaperLog
	case model.SetupReviewCandidate:
		result.Action = model.ActionWatch1M
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

func reviewCandidate(s model.LiveSnapshot, blockers []blocker) bool {
	if s.EarlyProxy.Score < 75 ||
		s.Authenticity.Score < 80 ||
		!authLowOrNone(s) ||
		!liquidityEnoughForReview(s) ||
		s.EstimatedImpactPct >= 50 ||
		!hasReviewFlow(s) ||
		s.Top10HolderPct >= 0.95 {
		return false
	}
	for _, b := range blockers {
		if hardReviewBlocker(b.text) {
			return false
		}
	}
	return hasReviewableBlocker(blockers)
}

func liquidityEnoughForReview(s model.LiveSnapshot) bool {
	return s.RealPoolDepthSOL >= 5 || s.LiquidityEvidenceSource == "raydium_wsol_vault"
}

func hasReviewFlow(s model.LiveSnapshot) bool {
	return s.BuyersLast1m > 0 || s.BuyersLast5m >= 5
}

func hardReviewBlocker(text string) bool {
	switch {
	case text == "no real flow":
		return true
	case strings.Contains(text, "terminal holder concentration"):
		return true
	case strings.Contains(text, "full clustering fallback"):
		return true
	case strings.Contains(text, "verified pool depth below 5"):
		return true
	case strings.Contains(text, "estimated impact >= 50"):
		return true
	case strings.Contains(text, "bundle bot"):
		return true
	case strings.Contains(text, "bump bot"):
		return true
	case strings.Contains(text, "mechanical rhythm"):
		return true
	default:
		return false
	}
}

func hasReviewableBlocker(blockers []blocker) bool {
	for _, b := range blockers {
		switch {
		case b.text == "partial clustering fallback":
			return true
		case b.text == "unknown catalyst":
			return true
		case strings.Contains(b.text, "high holder concentration"):
			return true
		case strings.Contains(b.text, "near-terminal holder concentration"):
			return true
		}
	}
	return false
}

func reviewReason(s model.LiveSnapshot, blockers []blocker) string {
	var soft []string
	for _, b := range blockers {
		if !hardReviewBlocker(b.text) && (b.text == "partial clustering fallback" || b.text == "unknown catalyst" || strings.Contains(b.text, "holder concentration")) {
			soft = append(soft, b.text)
		}
	}
	if len(soft) == 0 {
		return "strong score, needs operator confirmation"
	}
	if s.TokenMode == model.TokenModeRevival {
		return "strong revival demand, blocked from WOW by " + strings.Join(soft, " and ")
	}
	return "strong flow, blocked from WOW by " + strings.Join(soft, " and ")
}

func wowLaunch(s model.LiveSnapshot) bool {
	return s.TokenMode == model.TokenModeLaunch &&
		hasLaunchConfidence(s) &&
		s.LaunchAgeSeconds != nil &&
		*s.LaunchAgeSeconds < 900 &&
		s.EarlyProxy.Score >= 62 &&
		s.Authenticity.Severity == "none" &&
		s.ClusteringRowStatus != "partial_fallback" &&
		distributionWOWAllowed(s) &&
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
		distributionWOWAllowed(s) &&
		liquidityReliableForWOW(s)
}

func wowMigration(s model.LiveSnapshot) bool {
	return s.TokenMode == model.TokenModeMigration &&
		s.EarlyProxy.Score >= 55 &&
		authLowOrNone(s) &&
		distributionWOWAllowed(s) &&
		liquidityReliableForWOW(s)
}

func wowRevival(s model.LiveSnapshot) bool {
	return s.TokenMode == model.TokenModeRevival &&
		s.EarlyProxy.Score >= 55 &&
		s.Authenticity.Severity == "none" &&
		hasFreshDemand(s) &&
		!unknownCatalyst(s) &&
		distributionWOWAllowed(s) &&
		liquidityReliableForWOW(s)
}

func authLowOrNone(s model.LiveSnapshot) bool {
	return s.Authenticity.Severity == "none" || s.Authenticity.Severity == "low"
}

func distributionWOWAllowed(s model.LiveSnapshot) bool {
	return s.HolderCount >= 10 && s.Top10HolderPct < 0.85
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

func liquidityVerified(s model.LiveSnapshot) bool {
	return s.RealPoolDepthSOL >= 0 && s.LiquidityProxyReliable
}

func unknownCatalyst(s model.LiveSnapshot) bool {
	switch s.TokenMode {
	case model.TokenModeLaunch, model.TokenModeBonding, model.TokenModeMigration:
		return false
	case model.TokenModeRevival:
		return s.LaunchConfidence == model.LaunchConfidenceUnknown &&
			s.MigrationEventAt == nil &&
			s.BondingCurveProgressPct <= 0
	default:
		return true
	}
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
