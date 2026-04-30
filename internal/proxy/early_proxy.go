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
	score -= riskPenalty(s)
	if len(missing) >= 6 {
		score *= 0.75
		reasons = append(reasons, "low evidence coverage")
	}
	if !verifiedPoolDepth(s) && hasRealBuyerFlow(s) {
		reasons = append(reasons, "runner-like flow, liquidity unverified")
	}

	if hardRugVeto(s) {
		score = 0
	}

	score = clamp(score, 0, 100)
	band := bandFor(score, risks)
	if !apexEligible(s) && band == "APEX" {
		band = "CANDIDATE"
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

	return model.EarlyProxyScore{
		Eligible:        score >= earlyProxyThreshold,
		Score:           score,
		Threshold:       earlyProxyThreshold,
		Band:            band,
		Reasons:         dedupe(reasons),
		RiskFlags:       dedupe(risks),
		MissingFields:   dedupe(missing),
		EvidenceVersion: earlyProxyEvidenceVersion,
	}
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

func apexEligible(s model.LiveSnapshot) bool {
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
	if s.EstimatedImpactPct >= 90 && bandRank(band) > bandRank("WATCH") {
		return "WATCH"
	}
	if s.EstimatedImpactPct >= 50 && bandRank(band) > bandRank("WATCH") {
		return "WATCH"
	}
	if bandRank(band) > bandRank("CANDIDATE") {
		return "CANDIDATE"
	}
	return band
}

func bandRank(band string) int {
	switch band {
	case "APEX":
		return 4
	case "CANDIDATE":
		return 3
	case "WATCH":
		return 2
	case "DEAD":
		return 1
	default:
		return 0
	}
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
		return "APEX"
	}
	if score >= earlyProxyThreshold {
		return "CANDIDATE"
	}
	if score >= 45 {
		return "WATCH"
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
