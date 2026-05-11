package proxy

import (
	"math"
	"strings"

	"memecoin_scorer/internal/model"
)

const (
	earlyProxyThreshold       = 62.0
	earlyProxyEvidenceVersion = "early_proxy_v0.1_decision_time_rules"
)

type EarlyProxyInput = model.LiveSnapshot
type EarlyProxyResult = model.EarlyProxyScore

func ScoreEarlyProxy(s model.LiveSnapshot) model.EarlyProxyScore {
	var score float64
	var reasons []string
	var risks []string
	var missing []string

	missing = appendMissing(missing, missingCoreInputs(s)...)

	if s.EffectiveBuyers1m >= 3 {
		score += 12
		reasons = append(reasons, "early effective buyer depth")
	} else if s.EffectiveBuyers1m > 0 {
		score += 5
	}
	if s.EffectiveBuyers5m >= 8 {
		score += 14
		reasons = append(reasons, "strong effective buyer depth")
	} else if s.EffectiveBuyers5m >= 5 {
		score += 10
		reasons = append(reasons, "qualified effective buyer depth")
	} else if s.EffectiveBuyers5m >= 3 {
		score += 5
	}
	if s.BuyersLast1m >= 5 || s.BuyersLast5m >= 10 {
		score += 8
		reasons = append(reasons, "raw buyer flow")
	} else if s.BuyersLast1m > 0 || s.BuyersLast5m > 0 {
		score += 3
	}

	netBuy := s.BuySolLast1m - s.SellSolLast1m
	if s.BuySolLast1m > 0 && netBuy > 0 {
		imbalance := netBuy / math.Max(s.BuySolLast1m+s.SellSolLast1m, 0.000001)
		switch {
		case imbalance >= 0.70:
			score += 12
			reasons = append(reasons, "strong buy pressure")
		case imbalance >= 0.35:
			score += 8
			reasons = append(reasons, "positive buy pressure")
		default:
			score += 3
		}
	}
	if s.BuyerAcceleration >= 2 {
		score += 10
		reasons = append(reasons, "buyer acceleration")
	} else if s.BuyerAcceleration >= 1 {
		score += 6
		reasons = append(reasons, "positive buyer acceleration")
	}
	if s.HolderCount >= 25 {
		score += 8
		reasons = append(reasons, "holder base forming")
	} else if s.HolderCount >= 10 {
		score += 5
		reasons = append(reasons, "early holder base")
	} else if s.HolderCount > 0 {
		score += 2
	}
	liquiditySOL := liquidityForScoring(s)
	if verifiedPoolDepth(s) {
		if liquiditySOL >= 20 {
			score += 10
			reasons = append(reasons, "liquidity above minimum")
		} else if liquiditySOL >= 5 {
			score += 6
			reasons = append(reasons, "minimum liquidity present")
		}
	}
	if s.EstimatedImpactPct > 0 && s.EstimatedImpactPct <= 5 {
		score += 8
		reasons = append(reasons, "low estimated impact")
	} else if s.EstimatedImpactPct > 0 && s.EstimatedImpactPct <= 15 {
		score += 4
		reasons = append(reasons, "acceptable estimated impact")
	}
	if s.MarketCapSOL > 0 || s.MarketCapSol > 0 {
		score += 4
		reasons = append(reasons, "market cap proxy available")
	}
	switch s.ClusteringRowStatus {
	case "resolved":
		score += 8
		reasons = append(reasons, "resolved clustering")
	case "partial_fallback":
		score += 3
		reasons = append(reasons, "partial clustering evidence")
	}
	if s.FundingClusterRatio > 0 && s.FundingClusterRatio <= 0.15 {
		score += 3
		reasons = append(reasons, "low funding concentration")
	}
	if s.AdversarialScore > 0 && s.AdversarialScore <= 0.25 {
		score += 4
		reasons = append(reasons, "low adversarial score")
	}
	if s.ExecutionPenalty >= 0.5 {
		score += 6
		reasons = append(reasons, "execution quality available")
	}

	risks = append(risks, observedRiskFlags(s)...)
	auth, vel, mode, subtype, why, action := AnalyzeAuthenticity(s)
	if s.AuthenticityLabel != "" || s.BundleBotDetected || s.SniperBotDetected || s.BumpBotDetected || len(s.BotFlags) > 0 {
		auth.AuthenticityLabel = s.AuthenticityLabel
		auth.MechanicalityScore = s.MechanicalityScore
		auth.BotFlags = s.BotFlags
		auth.BundleBotDetected = s.BundleBotDetected
		auth.BundleBotConfidence = s.BundleBotConfidence
		auth.SniperBotDetected = s.SniperBotDetected
		auth.SniperShareBuySOL = s.SniperShareEarlyBuySOL
		auth.BumpBotDetected = s.BumpBotDetected
		auth.BumpBotScore = s.BumpBotScore
		auth.BumpBotWallets = s.BumpBotWallets
		vel.VelocityLabel = s.LiquidityVelocityLabel
		vel.OrganicLiquidityVelocity = s.OrganicLiquidityVelocity
		vel.RawLiquidityVelocity = s.RawLiquidityVelocity
	}
	invariantInput := s
	invariantInput.BotFlags = auth.BotFlags
	invariantInput.AuthenticityLabel = auth.AuthenticityLabel
	invariantInput.MechanicalityScore = auth.MechanicalityScore
	invariantInput.BundleBotDetected = auth.BundleBotDetected
	invariantInput.BundleBotConfidence = auth.BundleBotConfidence
	invariantInput.SniperBotDetected = auth.SniperBotDetected
	invariantInput.SniperShareEarlyBuySOL = auth.SniperShareBuySOL
	invariantInput.BumpBotDetected = auth.BumpBotDetected
	invariantInput.BumpBotScore = auth.BumpBotScore
	invariantInput.BumpBotWallets = auth.BumpBotWallets
	invariantInput.LiquidityVelocityLabel = vel.VelocityLabel
	invariantInput.RawLiquidityVelocity = vel.RawLiquidityVelocity
	invariantInput.OrganicLiquidityVelocity = vel.OrganicLiquidityVelocity
	if invariantInput.SignalMode == "" {
		invariantInput.SignalMode = mode
	}
	if invariantInput.RunnerSubtype == "" {
		invariantInput.RunnerSubtype = subtype
	}
	if invariantInput.WhyNotWOW == "" {
		invariantInput.WhyNotWOW = why
	}
	if invariantInput.OperatorAction == "" {
		invariantInput.OperatorAction = action
	}
	risks = append(risks, auth.BotFlags...)
	if auth.AuthenticityLabel == "bot_like" {
		risks = append(risks, "bot-like activity detected — no automatic entry")
		score -= 35
	}
	if auth.BundleBotDetected {
		risks = append(risks, "bundle bot detected")
		score -= 35
	}
	if auth.BumpBotDetected {
		risks = append(risks, "bump bot detected")
		score -= 45
	}
	if auth.SniperBotDetected && auth.SniperShareBuySOL >= 0.70 {
		risks = append(risks, "high sniper share")
		score -= 18
	}
	if containsAny(auth.BotFlags, "regular interval buys", "structured sell-buy cycle", "repeated identical buy sizes") {
		risks = append(risks, "mechanical interval buying detected — downgrade")
		score -= 18
	}
	if (vel.VelocityLabel == "strong" || vel.VelocityLabel == "exceptional") && auth.AuthenticityLabel == "organic" {
		score += 6
		reasons = append(reasons, "strong organic liquidity velocity")
	}
	if vel.RawLiquidityVelocity > vel.OrganicLiquidityVelocity*2 && auth.AuthenticityLabel != "organic" {
		risks = append(risks, "raw velocity bot-contaminated")
	}
	score -= riskPenalty(s)
	if len(missing) >= 6 {
		score *= 0.75
		reasons = append(reasons, "low evidence coverage")
	}
	if !verifiedPoolDepth(s) && hasRealBuyerFlow(s) {
		reasons = append(reasons, "runner-like flow, liquidity unverified")
	}

	// CALIBRATION_NOTE: velocity weighting is conservative until
	// live outcome data permits recalibration. Do not tune without
	// recalibration evidence.
	velocityBonus := 0.0
	if s.SolPerTrade5m >= 0.5 {
		velocityBonus += 10
	}
	if s.SolPerTrade5m >= 1.0 {
		velocityBonus += 10
	}
	if s.SolPerUniqueBuyer5m >= 1.0 {
		velocityBonus += 5
	}
	velocityPenalty := 0.0
	if s.SolPerTrade5m > 0 && s.SolPerTrade5m < 0.05 {
		velocityPenalty += 15
	}
	score += velocityBonus - velocityPenalty

	if hardRugVeto(s) {
		score = 0
	}
	if auth.BundleBotDetected || auth.BumpBotDetected || (auth.SniperBotDetected && auth.SniperShareBuySOL >= 0.70 && s.Top10HolderPct >= 0.85) {
		score = 0
	}

	score = clamp(score, 0, 100)
	band := bandFor(score, risks)
	if !runnerEligible(s) && band == "RUNNER" && !validUnverifiedBondingRunner(invariantInput) {
		band = "WATCH"
	}
	if band == "RUNNER" && (auth.AuthenticityLabel == "bot_like" || auth.BundleBotDetected || auth.BumpBotDetected || s.ClusteringRowStatus == "full_fallback") {
		band = "DEAD"
	}
	if band == "RUNNER" && containsAny(auth.BotFlags, "regular interval buys", "structured sell-buy cycle", "repeated identical buy sizes") {
		band = "WATCH"
	}
	if band == "RUNNER" && vel.RawLiquidityVelocity > vel.OrganicLiquidityVelocity*2 && auth.AuthenticityLabel != "organic" {
		band = "WATCH"
	}
	if band == "RUNNER" && invariantInput.SignalMode == "unknown" && !validUnverifiedBondingRunner(invariantInput) {
		band = "WATCH"
	}
	band = applyLiquidityEvidenceBandCap(s, band)
	if unreliableObservedLiquidityProxy(s) && s.LiquidityProxySOL < 5 {
		if qualifiesForUnreliableLiquidityWatch(s) {
			score = math.Max(score, 45)
			band = applyLiquidityEvidenceBandCap(s, "WATCH")
			reasons = append(reasons, "real buyer flow despite unreliable liquidity proxy")
		} else {
			score = 0
			band = "DEAD"
		}
	}

	result := model.EarlyProxyScore{
		Eligible:        band == "RUNNER" && score >= earlyProxyThreshold,
		Score:           score,
		Threshold:       earlyProxyThreshold,
		Band:            band,
		Reasons:         dedupe(reasons),
		RiskFlags:       dedupe(risks),
		MissingFields:   dedupe(missing),
		EvidenceVersion: earlyProxyEvidenceVersion,
	}
	return EnforceRunnerInvariants(invariantInput, result)
}

func EnforceRunnerInvariants(input EarlyProxyInput, result EarlyProxyResult) EarlyProxyResult {
	result.RiskFlags = dedupe(result.RiskFlags)
	result.Reasons = dedupe(result.Reasons)
	band := strings.ToUpper(result.Band)
	if band == "" {
		band = "DEAD"
	}

	addCap := func(msg string) {
		result.RiskFlags = dedupe(append(result.RiskFlags, msg))
	}
	capTo := func(next, msg string) {
		if bandRank(next) < bandRank(band) {
			band = next
			addCap(msg)
		}
	}
	terminal := func(next string, checks ...struct {
		fail bool
		msg  string
	}) {
		for _, c := range checks {
			if c.fail {
				capTo(next, c.msg)
			}
		}
	}

	if band == "APEX" {
		terminal("RUNNER",
			struct {
				fail bool
				msg  string
			}{!verifiedPoolDepth(input) && !validBondingLiquidityState(input), "APEX capped: unverified liquidity"},
			struct {
				fail bool
				msg  string
			}{verifiedPoolDepth(input) && input.RealPoolDepthSOL < 5, "APEX capped: pool depth below 5 SOL"},
			struct {
				fail bool
				msg  string
			}{input.AuthenticityLabel != "organic" && input.AuthenticityLabel != "mixed", "APEX capped: authenticity not organic/mixed"},
			struct {
				fail bool
				msg  string
			}{input.MechanicalityScore >= 40, "APEX capped: mechanicality >= 40"},
			struct {
				fail bool
				msg  string
			}{input.EffectiveBuyers5m < 5, "APEX capped: insufficient effective buyers"},
			struct {
				fail bool
				msg  string
			}{input.SignalMode == "" || input.SignalMode == "unknown", "APEX capped: unknown signal mode"},
		)
	}

	terminal("DEAD",
		struct {
			fail bool
			msg  string
		}{input.BundleBotDetected, "RUNNER capped: bundle bot detected"},
		struct {
			fail bool
			msg  string
		}{input.BumpBotDetected, "RUNNER capped: bump bot detected"},
		struct {
			fail bool
			msg  string
		}{input.ClusteringRowStatus == "full_fallback", "RUNNER capped: full clustering fallback"},
		struct {
			fail bool
			msg  string
		}{input.Top10HolderPct >= 0.95, "RUNNER capped: extreme top10 concentration"},
		struct {
			fail bool
			msg  string
		}{hardRugVeto(input), "RUNNER capped: hard rug veto"},
	)
	if bandRank(band) >= bandRank("RUNNER") {
		if input.AuthenticityLabel == "bot_like" {
			capTo("WATCH", "RUNNER capped: bot-like authenticity")
		}
		if (input.SignalMode == "" || input.SignalMode == "unknown") && !validUnverifiedBondingRunner(input) {
			capTo("WATCH", "RUNNER capped: unknown signal mode")
		}
		if mechanicalPattern(input.BotFlags) {
			if !(input.AuthenticityLabel == "mixed" && input.MechanicalityScore < 40) {
				capTo("WATCH", "RUNNER capped: mechanical pattern")
			}
		}
		if !verifiedPoolDepth(input) && !validBondingLiquidityState(input) {
			addCap("unverified pool depth")
			capTo("WATCH", "RUNNER capped: unverified liquidity")
		}
		if !input.LiquidityProxyReliable && !validUnverifiedBondingRunner(input) {
			capTo("WATCH", "RUNNER capped: liquidity not verified")
		}
		if input.AgeSeconds > 1800 && input.RunnerSubtype == "LAUNCH_RUNNER" {
			capTo("WATCH", "RUNNER capped: old token cannot be launch runner")
		}
	}
	if input.SignalMode == "unknown" && bandRank(band) > bandRank("WATCH") && !validUnverifiedBondingRunner(input) {
		capTo("WATCH", "RUNNER capped: unknown signal mode")
	}
	if band == "APEX" && input.RealPoolDepthSOL == -1 && !validBondingLiquidityState(input) {
		addCap("unverified pool depth")
		capTo("RUNNER", "APEX capped: unverified liquidity")
	}

	result.Band = band
	result.RiskFlags = dedupe(result.RiskFlags)
	result.Reasons = dedupe(result.Reasons)
	result.Eligible = result.Band == "RUNNER" && result.Score >= result.Threshold
	return result
}

func missingCoreInputs(s model.LiveSnapshot) []string {
	var out []string
	hasObservedFlow := s.BuyersLast1m > 0 || s.BuyersLast5m > 0 || s.BuySolLast1m > 0 || s.SellSolLast1m > 0

	// Zero buyer/flow values are valid observations: they mean no observed flow,
	// not missing evidence. Only structural fields that need token amount/price
	// support are marked missing when their companion evidence is absent.
	if s.BuyersLast1m > 0 && s.EffectiveBuyers1m == 0 {
		out = append(out, "effective_buyers_1m")
	}
	if s.BuyersLast5m > 0 && s.EffectiveBuyers5m == 0 {
		out = append(out, "effective_buyers_5m")
	}
	if hasObservedFlow && s.LiquidityProxySOL == 0 {
		out = append(out, "liquidity_proxy_sol")
	}
	if hasObservedFlow && s.LiquidityProxySOL == 0 && s.EstimatedImpactPct == 0 {
		out = append(out, "estimated_impact_pct")
	}
	if hasObservedFlow && s.LiquidityProxySOL == 0 && s.ExecutionPenalty == 0 {
		out = append(out, "execution_penalty")
	}
	if s.HolderCount == 0 {
		out = append(out, "holder_count")
	}
	if s.MarketCapSOL == 0 && s.MarketCapSol == 0 {
		out = append(out, "market_cap_sol")
	}
	if s.Top10HolderPct == 0 && s.HolderCount == 0 {
		out = append(out, "top10_holder_pct")
	}
	if strings.TrimSpace(s.ClusteringRowStatus) == "" {
		out = append(out, "clustering_row_status")
	}
	if strings.TrimSpace(s.ClusteringRowStatus) == "" {
		out = append(out, "funding_cluster_ratio")
	}
	return out
}

func observedRiskFlags(s model.LiveSnapshot) []string {
	var risks []string
	if s.ClusteringRowStatus == "full_fallback" {
		risks = append(risks, "full clustering fallback")
	}
	if s.ClusteringRowStatus == "partial_fallback" {
		risks = append(risks, "partial clustering fallback")
	}
	if s.Top10HolderPct >= 0.95 {
		risks = append(risks, "extreme top10 concentration")
	} else if s.Top10HolderPct >= 0.85 {
		risks = append(risks, "high top10 concentration")
	}
	if !verifiedPoolDepth(s) {
		risks = append(risks, "unverified pool depth")
		risks = append(risks, "unverified liquidity")
	}
	liquiditySOL := liquidityForScoring(s)
	if liquiditySOL < 5 {
		if verifiedPoolDepth(s) {
			risks = append(risks, "pool depth below 5 SOL")
		} else if unreliableObservedLiquidityProxy(s) {
			risks = append(risks, "observed liq proxy below 5 SOL")
		} else if liquiditySOL > 0 {
			risks = append(risks, "very thin liquidity")
		}
	}
	if s.EstimatedImpactPct > 25 {
		risks = append(risks, "high estimated impact")
	} else if s.EstimatedImpactPct > 15 {
		risks = append(risks, "elevated estimated impact")
	}
	if s.BuyersLast1m == 0 && s.BuyersLast5m == 0 && s.BuySolLast1m == 0 {
		risks = append(risks, "no real flow")
	}
	if s.AdversarialScore >= 0.75 {
		risks = append(risks, "high adversarial score")
	}
	if s.Engine.Layer0Reject {
		risks = append(risks, "layer0 reject: "+s.Engine.Layer0Reason)
	}
	return risks
}

func riskPenalty(s model.LiveSnapshot) float64 {
	var penalty float64
	if s.ClusteringRowStatus == "full_fallback" {
		penalty += 10
	}
	if s.Top10HolderPct >= 0.85 {
		penalty += 10
	}
	liquiditySOL := liquidityForScoring(s)
	if liquiditySOL > 0 && liquiditySOL < 5 && verifiedPoolDepth(s) {
		penalty += 12
	}
	if s.EstimatedImpactPct > 25 {
		penalty += 12
	} else if s.EstimatedImpactPct > 15 {
		penalty += 6
	}
	if s.AdversarialScore >= 0.75 {
		penalty += 8
	}
	return penalty
}

func hardRugVeto(s model.LiveSnapshot) bool {
	if s.Top10HolderPct >= 0.95 {
		return true
	}
	if s.ClusteringRowStatus == "full_fallback" {
		return true
	}
	if s.BuyersLast1m == 0 && s.BuyersLast5m == 0 && s.BuySolLast1m == 0 {
		return true
	}
	if highImpactWithoutCompensatingEvidence(s) {
		return true
	}
	if s.Engine.Layer0Reject {
		reason := strings.ToLower(s.Engine.Layer0Reason)
		if strings.Contains(reason, "self-bundl") || strings.Contains(reason, "hard rug") {
			return true
		}
		if strings.Contains(reason, "impossible_execution") {
			return !unreliableObservedLiquidityProxy(s)
		}
	}
	return false
}

func liquidityForScoring(s model.LiveSnapshot) float64 {
	if s.RealPoolDepthSOL >= 0 {
		return s.RealPoolDepthSOL
	}
	return s.LiquidityProxySOL
}

func verifiedPoolDepth(s model.LiveSnapshot) bool {
	return s.LiquidityProxyReliable &&
		strings.EqualFold(s.LiquidityEvidenceSource, "raydium_pc_vault") &&
		s.RealPoolDepthSOL >= 0
}

func runnerEligible(s model.LiveSnapshot) bool {
	return verifiedPoolDepth(s) &&
		s.RealPoolDepthSOL >= 5 &&
		s.EstimatedImpactPct <= 15 &&
		s.Top10HolderPct < 0.95 &&
		s.ClusteringRowStatus != "full_fallback" &&
		s.EffectiveBuyers5m >= 5
}

func applyLiquidityEvidenceBandCap(s model.LiveSnapshot, band string) string {
	if s.LiquidityProxyReliable && s.RealPoolDepthSOL >= 0 {
		return band
	}
	if validUnverifiedBondingRunner(s) {
		return band
	}
	if s.EstimatedImpactPct >= 90 && bandRank(band) > bandRank("WATCH") {
		return "WATCH"
	}
	if s.EstimatedImpactPct >= 50 && bandRank(band) > bandRank("WATCH") {
		return "WATCH"
	}
	if bandRank(band) > bandRank("WATCH") {
		return "WATCH"
	}
	return band
}

func qualifiesForUnreliableLiquidityWatch(s model.LiveSnapshot) bool {
	if !unreliableObservedLiquidityProxy(s) || s.LiquidityProxySOL >= 5 {
		return false
	}
	if !hasRealBuyerFlow(s) {
		return false
	}
	if s.Top10HolderPct >= 0.95 {
		return false
	}
	if s.ClusteringRowStatus == "full_fallback" {
		return false
	}
	if terminalRugSignal(s) {
		return false
	}
	if highImpactWithoutCompensatingEvidence(s) {
		return false
	}
	return true
}

func unreliableObservedLiquidityProxy(s model.LiveSnapshot) bool {
	return strings.EqualFold(s.LiquidityEvidenceSource, "observed_swaps_proxy") && !s.LiquidityProxyReliable
}

func hasRealBuyerFlow(s model.LiveSnapshot) bool {
	return s.BuyersLast1m > 0 || s.BuyersLast5m > 1
}

func terminalRugSignal(s model.LiveSnapshot) bool {
	if !s.Engine.Layer0Reject {
		return false
	}
	reason := strings.ToLower(s.Engine.Layer0Reason)
	return strings.Contains(reason, "self-bundl") || strings.Contains(reason, "hard rug")
}

func highImpactWithoutCompensatingEvidence(s model.LiveSnapshot) bool {
	if s.EstimatedImpactPct < 50 {
		return false
	}
	return s.EffectiveBuyers5m < 5 && s.BuyersLast5m < 5 && s.BuySolLast1m <= 0
}

func bandFor(score float64, risks []string) string {
	if score >= 75 && !hasSevereRisk(risks) {
		return "RUNNER"
	}
	if score >= earlyProxyThreshold {
		return "RUNNER"
	}
	if score >= 45 {
		return "WATCH"
	}
	if score >= 1 {
		return "REJECT"
	}
	return "DEAD"
}

func hasSevereRisk(risks []string) bool {
	for _, r := range risks {
		if strings.Contains(r, "extreme top10") ||
			strings.Contains(r, "no real flow") ||
			strings.Contains(r, "impossible_execution") {
			return true
		}
	}
	return false
}

func bandRank(band string) int {
	switch strings.ToUpper(band) {
	case "APEX":
		return 5
	case "RUNNER":
		return 4
	case "WATCH":
		return 3
	case "REJECT":
		return 2
	case "DEAD":
		return 1
	default:
		return 0
	}
}

func validBondingLiquidityState(s model.LiveSnapshot) bool {
	src := strings.ToLower(strings.TrimSpace(s.LaunchEvidenceSource))
	if src == "" {
		src = strings.ToLower(strings.TrimSpace(s.LiquidityEvidenceSource))
	}
	if !(strings.Contains(src, "bonding") || strings.Contains(src, "pump")) {
		return false
	}
	return liquidityForScoring(s) >= 5 || s.LiquidityPoolSOL >= 5 || s.LiquidityProxySOL >= 5
}

func validUnverifiedBondingRunner(s model.LiveSnapshot) bool {
	return validBondingLiquidityState(s) &&
		s.MechanicalityScore < 40 &&
		len(s.BotFlags) == 0 &&
		(s.LiquidityVelocityLabel == "strong" || s.LiquidityVelocityLabel == "exceptional" || s.OrganicLiquidityVelocity >= 1)
}

func mechanicalPattern(flags []string) bool {
	return containsAny(flags,
		"regular interval buys",
		"repeated identical buy sizes",
		"round-clock aligned buys",
		"structured sell-buy cycle",
		"bump bot",
	)
}

func appendMissing(dst []string, values ...string) []string {
	return append(dst, values...)
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
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
