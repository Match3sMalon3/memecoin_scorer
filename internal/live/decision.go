// Package live provides live-signal decision classification for the ingestor.
// It is intentionally separate from the offline batch scorer so the two can
// evolve independently.  All functions are pure and stateless.
package live

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"memecoin_scorer/internal/cluster"
	"memecoin_scorer/internal/engine"
	"memecoin_scorer/internal/features"
	"memecoin_scorer/internal/model"
)

// Decision labels.
const (
	LabelBUY   = "BUY"
	LabelREADY = "READY"
	LabelWATCH = "WATCH"
	LabelAVOID = "AVOID"

	clusterWindowTimeout = 750 * time.Millisecond
)

// Signal state labels (freshness).
const (
	StateFresh   = "fresh"
	StateStale   = "stale"
	StateExpired = "expired"
)

// Clustering status strings exposed in Decision.ClusteringStatus.
const (
	ClusteringHealthy  = "healthy"
	ClusteringDegraded = "degraded"
	ClusteringResolved = "resolved"
	ClusteringPartial  = "partial_fallback"
	ClusteringFallback = "full_fallback"
)

// LiveConfig holds all thresholds used by Classify.
// All values are unvalidated priors — retune after 100+ live signals.
type LiveConfig struct {
	// Execution / liquidity
	TradeSizeSOL        float64 // intended position size in SOL (default 1.0)
	LiquidityMultiplier float64 // pool depth must be >= TradeSizeSOL * LiquidityMultiplier (default 20)

	// BUY gates — all must pass
	MinBuyers1mBUY     int     // minimum effective buyers in last 1m (default 3)
	MinAccelerationBUY float64 // minimum buyer_acceleration (default 1.0)
	StrongVelocity1m   int     // bypass acceleration check when effective_buyers_1m >= this (default 8)
	MinExecQualityBUY  float64 // minimum execution_penalty (default 0.5)

	// READY gates — all must pass; BUY has priority
	MinBuyers5mREADY    int     // minimum effective buyers in last 5m (default 5)
	MinExecQualityREADY float64 // minimum execution_penalty (default 0.3)

	// WATCH gate
	MinTotalBuyersWATCH int // minimum unique_buyer_count (default 3)

	// AVOID trigger: execution_penalty below this → AVOID regardless of velocity
	MinExecQualityAVOID float64 // (default 0.1)

	// Adversarial gates — BUY/READY blocked when adversarial_score exceeds threshold.
	MaxAdversarialBUY   float64 // (default 0.60) — unvalidated prior
	MaxAdversarialREADY float64 // (default 0.75) — unvalidated prior

	// --- Module 6A: Hard impact veto ---
	MaxEstimatedImpactPct float64 // (default 15.0)

	// --- Module 6B: Effective buyer clustering ---
	// FunderResolver maps wallets to their funding parents.
	// Must implement cluster.HealthyResolver for BUY/READY to be allowed when
	// ClusterRequired=true.  NullResolver is NOT healthy — it is the "no backend" sentinel.
	FunderResolver cluster.FunderResolver

	// ClusterRequired gates BUY and READY when clustering is not healthy.
	// Default true.  Set to false only when operating without any resolver backend
	// and you explicitly accept that effective == raw.
	ClusterRequired bool

	// --- Module 6C: Freshness / stale signal control ---
	MaxSignalAgeMinBuyReady float64 // (default 5.0)
	MaxSignalAgeMinWatch    float64 // (default 15.0)

	// --- Module 6D: Warm-up / confidence gate ---
	MinTokenAgeSecondsForBuy      float64 // (default 90)
	MinEffBuyers1mForConfidentBuy int     // (default 3)
	MinTotalEventsForConf         int     // (default 3)

	// --- 7-gate engine ---
	// EngineConfig holds thresholds for the 7 Organic Success gates.
	// Defaults are set by DefaultLiveConfig via engine.DefaultEngineConfig().
	EngineConfig engine.EngineConfig
}

// DefaultLiveConfig returns a conservative set of defaults.
// FunderResolver defaults to NullResolver — the ingestor must replace it with
// StaticResolver or HeliusResolver before going live (see liveConfigFromEnv).
// ClusterRequired=true means BUY/READY are blocked until a healthy resolver is wired in.
func DefaultLiveConfig() LiveConfig {
	return LiveConfig{
		TradeSizeSOL:        1.0,
		LiquidityMultiplier: 20.0,

		MinBuyers1mBUY:     3,
		MinAccelerationBUY: 1.0,
		StrongVelocity1m:   8,
		MinExecQualityBUY:  0.5,

		MinBuyers5mREADY:    5,
		MinExecQualityREADY: 0.3,

		MinTotalBuyersWATCH: 3,

		MinExecQualityAVOID: 0.1,

		MaxAdversarialBUY:   0.60,
		MaxAdversarialREADY: 0.75,

		// 6A
		MaxEstimatedImpactPct: 15.0,

		// 6B — NullResolver + ClusterRequired=true means BUY/READY disabled until
		// liveConfigFromEnv() wires in a real backend.
		FunderResolver:  cluster.NullResolver{},
		ClusterRequired: true,

		// 6C
		MaxSignalAgeMinBuyReady: 5.0,
		MaxSignalAgeMinWatch:    15.0,

		// 6D
		MinTokenAgeSecondsForBuy:      90,
		MinEffBuyers1mForConfidentBuy: 3,
		MinTotalEventsForConf:         3,

		// 7-gate engine
		EngineConfig: engine.DefaultEngineConfig(),
	}
}

// Decision is the output of Classify for a single token snapshot.
type Decision struct {
	Label              string   `json:"label"`
	Reasons            []string `json:"reasons"`
	ExecutionPenalty   float64  `json:"execution_penalty"`
	LiquidityProxySOL  float64  `json:"liquidity_proxy_sol"`
	AdversarialScore   float64  `json:"adversarial_score"`
	TradeSizeSOL       float64  `json:"trade_size_sol"`
	EstimatedImpactPct float64  `json:"estimated_impact_pct"`

	// --- Module 6B: Effective buyer clustering ---
	EffectiveBuyers1m   int     `json:"effective_buyers_1m"`
	EffectiveBuyers5m   int     `json:"effective_buyers_5m"`
	ClusteredBuyerCount int     `json:"clustered_buyer_count"`
	FundingClusterRatio float64 `json:"funding_cluster_ratio"`
	// ClusteringStatus is "healthy" when the resolver is operational, "degraded" otherwise.
	ClusteringStatus string `json:"clustering_status"`
	// ClusteringBackend is the resolver backend name: "helius", "static", or "null".
	ClusteringBackend string `json:"clustering_backend"`
	// ClusterCompressionRatio1m = (raw_buyers_1m - effective_buyers_1m) / raw_buyers_1m.
	// Zero when no clustering occurred.
	ClusterCompressionRatio1m float64 `json:"cluster_compression_ratio_1m"`
	ClusterCompressionRatio5m float64 `json:"cluster_compression_ratio_5m"`
	ClusteringRowStatus       string  `json:"clustering_row_status"`
	ClusteringTimeouts        int     `json:"clustering_timeouts"`
	ClusteringFallbacks       int     `json:"clustering_fallbacks"`

	// --- Module 6C: Freshness ---
	SignalState  string `json:"signal_state"`
	IsActionable bool   `json:"is_actionable"`

	// --- Module 6D: Warm-up / confidence ---
	ConfidenceScore float64 `json:"confidence_score"`
	WarmingUp       bool    `json:"warming_up"`

	// --- Module 6G: Positive rationale ---
	WhyNow          string `json:"why_now"`
	WhyNotHigher    string `json:"why_not_higher"`
	DominantBlocker string `json:"dominant_blocker"`
	OperatorVerdict string `json:"operator_verdict"`
	ExecutionURL    string `json:"execution_url"`

	PriorityLabel             string `json:"priority_label"`
	ActionabilityLabel        string `json:"actionability_label"`
	HistoricalAnalogueSummary string `json:"historical_analogue_summary"`
	HistoricalOutcomeBand     string `json:"historical_outcome_band"`
	HistoricalTimeToOutcome   string `json:"historical_time_to_outcome"`
	UpgradeTriggers           string `json:"upgrade_triggers"`
	InvalidateTriggers        string `json:"invalidate_triggers"`

	// --- 7-gate engine result ---
	Engine model.EngineDecision `json:"engine"`
}

// Classify assigns a decision label to a live TokenSnapshot using time.Now().
// Kept for backward compatibility with existing callers and tests.
func Classify(snap model.TokenSnapshot, cfg LiveConfig) Decision {
	return ClassifyAt(snap, cfg, time.Now())
}

// ClassifyAt assigns a decision label with a controlled clock.
//
// Evaluation order:
//  1. Hard AVOID: exec < MinExecQualityAVOID
//  2. Hard AVOID: impact > MaxEstimatedImpactPct (6A)
//  3. Clustering health gate: if ClusterRequired and resolver not healthy → clamp to WATCH/AVOID (6B)
//  4. BUY  (all gates must pass; includes warm-up veto + sell-reversal veto)
//  5. READY
//  6. WATCH
//  7. AVOID
func ClassifyAt(snap model.TokenSnapshot, cfg LiveConfig, now time.Time) Decision {
	liqProxy := snap.TotalBuySOL + snap.TotalSellSOL
	execPenalty := features.ExecutionPenalty(cfg.TradeSizeSOL, liqProxy, cfg.LiquidityMultiplier)
	advScore := adversarialScore(snap)
	impactPct := estimatedImpact(cfg.TradeSizeSOL, liqProxy)

	// Clustering health
	clusteringOK := cluster.IsResolverHealthy(cfg.FunderResolver)
	clusteringStatus := ClusteringHealthy
	if !clusteringOK {
		clusteringStatus = ClusteringDegraded
	}
	clusteringBackend := cluster.ResolverBackendName(cfg.FunderResolver)

	// Cluster buyers
	clust1m := clusterWindow(snap.UniqueWalletsLast1m, snap.BuyersLast1m, cfg, now)
	clust5m := clusterWindow(snap.UniqueWalletsLast5m, snap.BuyersLast5m, cfg, now)
	effBuyers1m := clust1m.EffectiveUniqueBuyerCount
	effBuyers5m := clust5m.EffectiveUniqueBuyerCount

	comp1m := compressionRatio(snap.BuyersLast1m, effBuyers1m)
	comp5m := compressionRatio(snap.BuyersLast5m, effBuyers5m)

	// Warm-up check
	warmingUp, warmReason := isWarmingUp(snap, cfg)

	// 7-gate engine evaluation.
	engDec := engine.EvaluateGates(snap, clust1m.FundingClusterRatio, cfg.EngineConfig)

	base := Decision{
		LiquidityProxySOL:         liqProxy,
		ExecutionPenalty:          execPenalty,
		AdversarialScore:          advScore,
		TradeSizeSOL:              cfg.TradeSizeSOL,
		EstimatedImpactPct:        impactPct,
		EffectiveBuyers1m:         effBuyers1m,
		EffectiveBuyers5m:         effBuyers5m,
		ClusteredBuyerCount:       clust1m.ClusteredBuyerCount,
		FundingClusterRatio:       clust1m.FundingClusterRatio,
		ClusteringStatus:          clusteringStatus,
		ClusteringBackend:         clusteringBackend,
		ClusterCompressionRatio1m: comp1m,
		ClusterCompressionRatio5m: comp5m,
		ClusteringRowStatus:       clusteringRowStatus(clust1m, snap.BuyersLast1m),
		ClusteringTimeouts:        clust1m.ResolverTimeoutCount,
		ClusteringFallbacks:       clust1m.ResolverFallbackCount,
		WarmingUp:                 warmingUp,
		Engine:                    engDec,
	}

	// Hard AVOID: execution quality too low
	if execPenalty < cfg.MinExecQualityAVOID {
		base.Label = LabelAVOID
		base.Reasons = []string{
			fmt.Sprintf("exec_penalty=%.2f < avoid_floor=%.2f (liq_proxy=%.2f SOL, trade=%.2f SOL, impact=%.1f%%)",
				execPenalty, cfg.MinExecQualityAVOID, liqProxy, cfg.TradeSizeSOL, impactPct),
		}
		return finalize(base, snap, cfg, now)
	}

	// Module 6A: hard impact veto
	if impactPct > cfg.MaxEstimatedImpactPct {
		base.Label = LabelAVOID
		base.Reasons = []string{
			fmt.Sprintf("impact=%.1f%% > max=%.1f%%", impactPct, cfg.MaxEstimatedImpactPct),
		}
		return finalize(base, snap, cfg, now)
	}

	// Module 6B: clustering health gate — if required and degraded, clamp to WATCH or AVOID
	if cfg.ClusterRequired && !clusteringOK {
		// Allow WATCH if the token has enough unique buyers, else AVOID.
		watchReasons, watchOK := checkWATCH(snap, cfg)
		if watchOK {
			base.Label = LabelWATCH
			base.Reasons = append(
				[]string{"clustering degraded: BUY/READY disabled (set HELIUS_API_KEY or FUNDER_MAP_PATH, or set CLUSTER_REQUIRED=0)"},
				watchReasons...,
			)
		} else {
			base.Label = LabelAVOID
			base.Reasons = append(
				[]string{"clustering degraded: BUY/READY disabled"},
				failReasons("not WATCH", watchReasons)...,
			)
		}
		return finalize(base, snap, cfg, now)
	}

	// Layer 0 hard reject from engine.
	if engDec.Layer0Reject {
		base.Label = LabelAVOID
		base.Reasons = []string{"layer0_reject: " + engDec.Layer0Reason}
		return finalize(base, snap, cfg, now)
	}

	// BUY: all gates must pass
	buyReasons, buyOK := checkBUY(snap, cfg, execPenalty, advScore, effBuyers1m, warmingUp, warmReason)
	if buyOK {
		base.Label = LabelBUY
		base.Reasons = buyReasons
	} else {
		// READY: promising but not BUY-grade
		readyReasons, readyOK := checkREADY(snap, cfg, execPenalty, advScore, effBuyers5m)
		if readyOK {
			base.Label = LabelREADY
			base.Reasons = append(readyReasons, failReasons("not BUY", buyReasons)...)
		} else {
			// WATCH: early / weak but some activity
			watchReasons, watchOK := checkWATCH(snap, cfg)
			if watchOK {
				base.Label = LabelWATCH
				base.Reasons = append(watchReasons, failReasons("not READY", readyReasons)...)
			} else {
				// AVOID: nothing passed
				var avoidReasons []string
				avoidReasons = append(avoidReasons, failReasons("not WATCH", watchReasons)...)
				avoidReasons = append(avoidReasons, failReasons("not READY", readyReasons)...)
				base.Label = LabelAVOID
				base.Reasons = avoidReasons
			}
		}
	}

	// Apply engine MaxLabel ceiling — never upgrade the label above what the 7 gates allow.
	if engDec.MaxLabel != "" {
		base.Label = applyLabelCeiling(base.Label, engDec.MaxLabel, &base.Reasons)
	}

	return finalize(base, snap, cfg, now)
}

// applyLabelCeiling downgrades label to ceil if label is higher than ceil.
// Appends a reason if a downgrade occurs.  Label order: BUY > READY > WATCH > AVOID.
func applyLabelCeiling(label, ceil string, reasons *[]string) string {
	rank := map[string]int{LabelBUY: 3, LabelREADY: 2, LabelWATCH: 1, LabelAVOID: 0}
	if rank[label] > rank[ceil] {
		*reasons = append(*reasons, fmt.Sprintf("engine_ceiling: %s→%s (7-gate max_label=%s)", label, ceil, ceil))
		return ceil
	}
	return label
}

// compressionRatio returns (raw-eff)/raw, or 0 when raw is 0.
func compressionRatio(raw, eff int) float64 {
	if raw == 0 {
		return 0
	}
	r := float64(raw-eff) / float64(raw)
	if r < 0 {
		return 0
	}
	return r
}

// finalize populates freshness, confidence, and rationale fields.
func finalize(d Decision, snap model.TokenSnapshot, cfg LiveConfig, now time.Time) Decision {
	d = applyFreshness(d, snap, cfg, now)
	d.ConfidenceScore = computeConfidence(snap, cfg, d.EffectiveBuyers1m, d.AdversarialScore, d.EstimatedImpactPct, d.FundingClusterRatio)
	// Gate 7 failure caps ConfidenceScore at engine ScoreCap (default 40).
	if d.Engine.ScoreCap > 0 && d.ConfidenceScore > float64(d.Engine.ScoreCap) {
		d.ConfidenceScore = float64(d.Engine.ScoreCap)
	}
	liveRow := &model.LiveSnapshot{
		TokenSnapshot:             snap,
		EffectiveBuyers1m:         d.EffectiveBuyers1m,
		EffectiveBuyers5m:         d.EffectiveBuyers5m,
		LiquidityProxySOL:         d.LiquidityProxySOL,
		ClusteringRowStatus:       d.ClusteringRowStatus,
		ClusterCompressionRatio5m: d.ClusterCompressionRatio5m,
		AdversarialScore:          d.AdversarialScore,
		EstimatedImpactPct:        d.EstimatedImpactPct,
		Decision:                  d.Label,
		LastPriceSol:              snap.LastPriceSOL,
		MarketCapSol:              snap.MarketCapSOL,
		Layer0Reject:              d.Engine.Layer0Reject,
		Engine:                    d.Engine,
	}
	d.WhyNow = BuildWhyNow(liveRow)
	d.WhyNotHigher = BuildWhyNotHigher(liveRow)
	d.DominantBlocker = BuildDominantBlocker(liveRow)
	d.OperatorVerdict = BuildOperatorVerdict(liveRow)
	liveRow.OperatorVerdict = d.OperatorVerdict
	d.ExecutionURL = BuildExecutionURL(snap.Mint)
	d.ActionabilityLabel = BuildActionabilityLabel(liveRow)
	liveRow.ActionabilityLabel = d.ActionabilityLabel
	d.HistoricalAnalogueSummary = BuildHistoricalAnalogueSummary(liveRow)
	d.HistoricalOutcomeBand = BuildHistoricalOutcomeBand(liveRow)
	d.HistoricalTimeToOutcome = BuildHistoricalTimeToOutcome(liveRow)
	d.UpgradeTriggers = BuildUpgradeTriggers(liveRow)
	d.InvalidateTriggers = BuildInvalidateTriggers(liveRow)
	return d
}

// --- Module 6C: freshness ---

func applyFreshness(d Decision, snap model.TokenSnapshot, cfg LiveConfig, now time.Time) Decision {
	signalAgeMin := now.Sub(snap.LastEventAt).Minutes()
	if signalAgeMin < 0 {
		signalAgeMin = 0
	}

	switch d.Label {
	case LabelBUY, LabelREADY:
		if signalAgeMin <= cfg.MaxSignalAgeMinBuyReady {
			d.SignalState = StateFresh
			d.IsActionable = true
		} else {
			d.SignalState = StateExpired
			d.IsActionable = false
		}
	case LabelWATCH:
		if signalAgeMin <= cfg.MaxSignalAgeMinWatch {
			if signalAgeMin <= cfg.MaxSignalAgeMinBuyReady {
				d.SignalState = StateFresh
			} else {
				d.SignalState = StateStale
			}
			d.IsActionable = true
		} else {
			d.SignalState = StateExpired
			d.IsActionable = false
		}
	default: // AVOID
		d.SignalState = StateExpired
		d.IsActionable = false
	}
	return d
}

// --- Module 6D: warm-up / confidence ---

func isWarmingUp(snap model.TokenSnapshot, cfg LiveConfig) (bool, string) {
	if snap.AgeSeconds < cfg.MinTokenAgeSecondsForBuy {
		return true, fmt.Sprintf("warming_up: age=%.0fs < min=%.0fs",
			snap.AgeSeconds, cfg.MinTokenAgeSecondsForBuy)
	}
	if snap.TotalEventCount > 0 && snap.TotalEventCount < cfg.MinTotalEventsForConf {
		return true, fmt.Sprintf("warming_up: total_events=%d < min=%d",
			snap.TotalEventCount, cfg.MinTotalEventsForConf)
	}
	return false, ""
}

func computeConfidence(
	snap model.TokenSnapshot,
	cfg LiveConfig,
	effBuyers1m int,
	advScore float64,
	impactPct float64,
	clusterRatio float64,
) float64 {
	score := 100.0

	if cfg.MinTokenAgeSecondsForBuy > 0 && snap.AgeSeconds < cfg.MinTokenAgeSecondsForBuy {
		frac := snap.AgeSeconds / cfg.MinTokenAgeSecondsForBuy
		score -= (1 - frac) * 25
	}
	if snap.TotalEventCount > 0 && cfg.MinTotalEventsForConf > 0 && snap.TotalEventCount < cfg.MinTotalEventsForConf {
		frac := float64(snap.TotalEventCount) / float64(cfg.MinTotalEventsForConf)
		score -= (1 - frac) * 20
	}
	if cfg.MinEffBuyers1mForConfidentBuy > 0 && effBuyers1m < cfg.MinEffBuyers1mForConfidentBuy {
		frac := float64(effBuyers1m) / float64(cfg.MinEffBuyers1mForConfidentBuy)
		score -= (1 - frac) * 20
	}
	score -= advScore * 20
	if impactPct > 5 {
		score -= math.Min(15, (impactPct-5)/2)
	}
	score -= clusterRatio * 15

	return math.Max(0, math.Min(100, score))
}

// --- Module 6G: rationale builder ---

func BuildExecutionURL(mint string) string {
	if strings.TrimSpace(mint) == "" {
		return ""
	}
	return "https://gmgn.ai/sol/token/" + mint
}

func BuildQualityTier(s *model.LiveSnapshot) string {
	if s.PriorityLabel == "best_on_tape" && s.ActionabilityLabel == "actionable now" {
		return "APEX"
	}
	if s.PriorityLabel == "best_on_tape" {
		return "NEAR"
	}
	if s.ActionabilityLabel == "observe closely" {
		return "NEAR"
	}
	if s.TrustLabel == "insider-controlled" {
		return "TRAP"
	}
	return "DEAD"
}

func BuildTriggerLine(s *model.LiveSnapshot) string {
	var fragments []string
	if s.EffectiveBuyers1m > 0 {
		fragments = append(fragments, fmt.Sprintf("%d eff/1m", s.EffectiveBuyers1m))
	} else {
		fragments = append(fragments, "no real flow")
	}

	if s.ClusteringRowStatus == "resolved" {
		fragments = append(fragments, "cluster clean")
	} else if s.ClusteringRowStatus == "partial_fallback" {
		fragments = append(fragments, "cluster partial")
	} else {
		fragments = append(fragments, "cluster fallback")
	}

	if s.Engine.Layer0Reject && s.LiquidityProxySOL > 0 {
		fragments = append(fragments, fmt.Sprintf("liq %.2f SOL", s.LiquidityProxySOL))
	} else if s.EstimatedImpactPct > 0 {
		fragments = append(fragments, fmt.Sprintf("impact %.1f%%", s.EstimatedImpactPct))
	} else if s.LiquidityProxySOL > 0 {
		fragments = append(fragments, fmt.Sprintf("liq %.2f SOL", s.LiquidityProxySOL))
	} else {
		fragments = append(fragments, "thin liq")
	}

	return strings.Join(fragments, " • ")
}

func BuildNoTradeReason(s *model.LiveSnapshot) string {
	if s.Engine.Layer0Reject && s.LiquidityProxySOL > 0 && s.LiquidityProxySOL < 5 {
		return fmt.Sprintf("liquidity %.2f SOL < 5 SOL minimum", s.LiquidityProxySOL)
	}
	if s.EstimatedImpactPct > 15 {
		return fmt.Sprintf("impact %.1f%% > 15%% max", s.EstimatedImpactPct)
	}
	if s.ClusteringRowStatus == "partial_fallback" {
		return "clustering partial fallback unresolved"
	}
	if s.ClusteringRowStatus == "full_fallback" {
		return "clustering full fallback unresolved"
	}
	if s.Top10HolderPct >= 0.85 {
		return fmt.Sprintf("top10 concentration %.1f%% > 85%% risk line", s.Top10HolderPct*100)
	}
	if s.EffectiveBuyers5m > 0 && s.EffectiveBuyers5m < 5 {
		return fmt.Sprintf("effective buyers /5m %d < 5 minimum", s.EffectiveBuyers5m)
	}
	if strings.TrimSpace(s.DominantBlocker) != "" {
		return s.DominantBlocker
	}
	return "no valid execution edge"
}

func BuildWhyNow(s *model.LiveSnapshot) string {
	var r []string
	if s.EffectiveBuyers1m > 0 {
		r = append(r, fmt.Sprintf("%d eff buyers /1m", s.EffectiveBuyers1m))
	}
	if s.EffectiveBuyers5m > 0 {
		r = append(r, fmt.Sprintf("%d eff buyers /5m", s.EffectiveBuyers5m))
	}
	if s.ClusteringRowStatus == "resolved" {
		r = append(r, "clean clustering")
	}
	if s.HolderCount > 0 {
		r = append(r, fmt.Sprintf("%d holders", s.HolderCount))
	}
	if s.LastPriceSol > 0 && s.MarketCapSol > 0 {
		r = append(r, "priced structure")
	}
	if s.AdversarialScore > 0 && s.AdversarialScore <= 0.20 {
		r = append(r, "low adversarial")
	}
	if s.EstimatedImpactPct > 0 && s.EstimatedImpactPct <= 15 {
		r = append(r, "low impact")
	}
	if len(r) > 3 {
		r = r[:3]
	}
	if len(r) == 0 {
		return ""
	}
	return strings.Join(r, " • ")
}

func BuildDominantBlocker(s *model.LiveSnapshot) string {
	layer0Reject := false
	layer0Reason := ""
	if s.Engine.Layer0Reject {
		layer0Reject = true
		layer0Reason = strings.ToLower(s.Engine.Layer0Reason)
	}
	if layer0Reject && strings.Contains(layer0Reason, "impossible_execution") {
		return "impossible execution • thin liquidity"
	}
	if s.ClusteringRowStatus == "full_fallback" {
		return "full fallback • thin liquidity"
	}
	if s.ClusteringRowStatus == "partial_fallback" {
		return "partial fallback • thin liquidity"
	}
	if s.MarketCapSol == 0 {
		return "no holder proxy • slippage ceiling"
	}
	if s.Top10HolderPct >= 0.85 {
		return "concentration • suspicious vol/mc"
	}
	if s.EstimatedImpactPct > 15 {
		return "thin liquidity • slippage ceiling"
	}
	if s.EffectiveBuyers5m < 5 {
		return "too few effective buyers"
	}
	return "structural weakness"
}

func BuildWhyNotHigher(s *model.LiveSnapshot) string {
	return BuildDominantBlocker(s)
}

func BuildOperatorVerdict(s *model.LiveSnapshot) string {
	if s.Engine.Layer0Reject {
		return "structurally broken"
	}
	if s.MarketCapSol == 0 {
		return "clean-ish but incomplete"
	}
	if s.ClusteringRowStatus != "resolved" {
		return "watchable, not trustworthy"
	}
	if s.Top10HolderPct >= 0.85 {
		return "sizeable but concentrated"
	}
	if s.HolderCount > 0 && s.EffectiveBuyers5m > 5 {
		return "clean-ish but immature"
	}
	return "low-confidence setup"
}

func BuildHistoricalAnalogueSummary(s *model.LiveSnapshot) string {
	if s.Engine.Layer0Reject || s.LiquidityProxySOL < 5 {
		return "Historically resembles failed early-interest names: attention appears before executable liquidity."
	}
	if s.MarketCapSol == 0 {
		return "Historically resembles incomplete structure: flow appears, but holder-backed market-cap formation is not yet visible."
	}
	if s.ClusteringRowStatus != "resolved" {
		return "Historically resembles compromised flow: participation exists, but clustering trust is not yet clean."
	}
	if s.Top10HolderPct >= 0.85 {
		return "Historically resembles concentrated launches: attention can persist, but durability is impaired by holder structure."
	}
	return "Historically resembles monitorable early structure: weak edge now, but upgradeable if execution and buyer quality improve."
}

func BuildHistoricalOutcomeBand(s *model.LiveSnapshot) string {
	if s.Engine.Layer0Reject || s.LiquidityProxySOL < 5 {
		return "usually stalls unless liquidity improves sharply"
	}
	if s.MarketCapSol == 0 {
		return "often noisy until holder-backed structure appears"
	}
	if s.ClusteringRowStatus != "resolved" {
		return "often fades unless clustering resolves cleanly"
	}
	if s.Top10HolderPct >= 0.85 {
		return "often squeezes briefly, then weakens unless concentration normalizes"
	}
	return "can improve if execution quality and buyer quality continue strengthening"
}

func BuildHistoricalTimeToOutcome(s *model.LiveSnapshot) string {
	if s.Engine.Layer0Reject || s.LiquidityProxySOL < 5 {
		return "usually decided quickly"
	}
	if s.MarketCapSol == 0 {
		return "usually unresolved until holder structure appears"
	}
	if s.ClusteringRowStatus != "resolved" {
		return "usually decided within the next buyer-quality cycle"
	}
	return "usually decided over the next few buyer-quality refreshes"
}

func BuildUpgradeTriggers(s *model.LiveSnapshot) string {
	var out []string

	if s.LiquidityProxySOL < 5 {
		out = append(out, "liquidity must clear 5 SOL minimum")
	}
	if s.EstimatedImpactPct > 15 || s.EstimatedImpactPct == 0 {
		out = append(out, "impact must compress to 15% or lower")
	}
	if s.EffectiveBuyers5m < 5 {
		out = append(out, "effective buyers /5m must reach 5+")
	}
	if s.MarketCapSol == 0 {
		out = append(out, "holder-backed market-cap structure must appear")
	}
	if s.ClusteringRowStatus != "resolved" {
		out = append(out, "clustering must resolve without fallback")
	}
	if s.Top10HolderPct >= 0.85 {
		out = append(out, "top-holder concentration must normalize")
	}

	if len(out) == 0 {
		out = append(out, "sustain buyer quality and execution quality")
	}
	return strings.Join(out, " • ")
}

func BuildInvalidateTriggers(s *model.LiveSnapshot) string {
	var out []string

	if s.Engine.Layer0Reject || s.LiquidityProxySOL < 5 {
		out = append(out, "continued impossible execution keeps this non-viable")
	}
	if s.ClusteringRowStatus == "full_fallback" {
		out = append(out, "persistent full fallback invalidates buyer trust")
	}
	if s.MarketCapSol == 0 {
		out = append(out, "continued lack of holder proxy keeps structure incomplete")
	}
	if s.Top10HolderPct >= 0.85 {
		out = append(out, "further concentration invalidates durability")
	}
	if s.EstimatedImpactPct > 25 {
		out = append(out, "impact expansion above 25% invalidates execution quality")
	}
	if s.EffectiveBuyers5m < 3 {
		out = append(out, "buyer quality staying below 3 effective buyers kills the setup")
	}

	if len(out) == 0 {
		out = append(out, "loss of buyer quality invalidates the setup")
	}
	return strings.Join(out, " • ")
}

func BuildActionabilityLabel(s *model.LiveSnapshot) string {
	if s.Decision == "BUY" || s.Decision == "READY" {
		return "actionable now"
	}
	if s.EffectiveBuyers5m >= 5 && s.LiquidityProxySOL >= 5 && s.EstimatedImpactPct <= 15 {
		return "observe closely"
	}
	if s.MarketCapSol == 0 || s.Engine.Layer0Reject {
		return "not actionable"
	}
	return "reject quickly"
}

func AssignPriorityLabels(rows []model.LiveSnapshot) {
	bestMint := bestPriorityMint(rows)
	for i := range rows {
		rows[i].Layer0Reject = rows[i].Engine.Layer0Reject
		rows[i].ExecutionURL = BuildExecutionURL(rows[i].Mint)
		rows[i].WhyNow = BuildWhyNow(&rows[i])
		rows[i].WhyNotHigher = BuildWhyNotHigher(&rows[i])
		rows[i].DominantBlocker = BuildDominantBlocker(&rows[i])
		rows[i].OperatorVerdict = BuildOperatorVerdict(&rows[i])
		rows[i].ActionabilityLabel = BuildActionabilityLabel(&rows[i])
		rows[i].HistoricalAnalogueSummary = BuildHistoricalAnalogueSummary(&rows[i])
		rows[i].HistoricalOutcomeBand = BuildHistoricalOutcomeBand(&rows[i])
		rows[i].HistoricalTimeToOutcome = BuildHistoricalTimeToOutcome(&rows[i])
		rows[i].UpgradeTriggers = BuildUpgradeTriggers(&rows[i])
		rows[i].InvalidateTriggers = BuildInvalidateTriggers(&rows[i])
		rows[i].PriorityLabel = BuildPriorityLabel(&rows[i], bestMint)
		rows[i].OperatorFocus = BuildOperatorFocus(&rows[i])
		rows[i].RelativeSetupLabel = BuildRelativeSetupLabel(&rows[i])
		rows[i].TrustLabel = BuildTrustLabel(&rows[i])
		rows[i].TrustReason = BuildTrustReason(&rows[i])
		rows[i].AsymmetryLabel = BuildAsymmetryLabel(&rows[i])
		rows[i].AsymmetryReason = BuildAsymmetryReason(&rows[i])
		rows[i].QualityTier = BuildQualityTier(&rows[i])
		rows[i].TriggerLine = BuildTriggerLine(&rows[i])
		rows[i].NoTradeReason = BuildNoTradeReason(&rows[i])
	}
}

func BuildPriorityLabel(s *model.LiveSnapshot, bestMint string) string {
	if s.Mint == bestMint {
		return "best_on_tape"
	}
	if s.EffectiveBuyers5m >= 3 || s.ClusteringRowStatus != "resolved" || s.MarketCapSol == 0 {
		return "monitor_for_upgrade"
	}
	return "deprioritize"
}

func BuildOperatorFocus(s *model.LiveSnapshot) string {
	if s.PriorityLabel == "best_on_tape" {
		return "best available now"
	}
	if s.PriorityLabel == "monitor_for_upgrade" {
		return "monitor for upgrade"
	}
	return "reject quickly"
}

func BuildRelativeSetupLabel(s *model.LiveSnapshot) string {
	if s.PriorityLabel == "best_on_tape" && s.ActionabilityLabel == "not actionable" {
		return "least bad on tape"
	}
	if s.PriorityLabel == "best_on_tape" {
		return "current lead setup"
	}
	if s.PriorityLabel == "monitor_for_upgrade" && s.ClusteringRowStatus == "resolved" && s.EffectiveBuyers5m >= 3 {
		return "cleaner than peers"
	}
	if s.PriorityLabel == "monitor_for_upgrade" && s.ClusteringRowStatus != "resolved" {
		return "flow exists but trust is compromised"
	}
	if s.MarketCapSol == 0 {
		return "structure incomplete"
	}
	if s.Engine.Layer0Reject {
		return "execution-failed reject"
	}
	return "low-value reject"
}

func BuildTrustLabel(s *model.LiveSnapshot) string {
	if s.Top10HolderPct >= 0.85 {
		return "insider-controlled"
	}
	if s.ClusteringRowStatus != "resolved" {
		return "compromised"
	}
	if s.FundingClusterRatio >= 0.30 {
		return "compromised"
	}
	if s.ClusterCompressionRatio1m > 0 || s.ClusterCompressionRatio5m > 0 {
		return "compromised"
	}
	return "organic"
}

func BuildTrustReason(s *model.LiveSnapshot) string {
	if s.Top10HolderPct >= 0.85 {
		return fmt.Sprintf("top10 concentration %.1f%%", s.Top10HolderPct*100)
	}
	if s.ClusteringRowStatus == "full_fallback" {
		return "full fallback clustering trust degradation"
	}
	if s.ClusteringRowStatus == "partial_fallback" {
		return "partial fallback clustering trust degradation"
	}
	if s.FundingClusterRatio >= 0.30 {
		return fmt.Sprintf("funding cluster ratio %.2f", s.FundingClusterRatio)
	}
	if s.ClusterCompressionRatio1m > 0 || s.ClusterCompressionRatio5m > 0 {
		return fmt.Sprintf("buyer compression 1m %.1f%% / 5m %.1f%%", s.ClusterCompressionRatio1m*100, s.ClusterCompressionRatio5m*100)
	}
	return "no major trust impairment detected"
}

func BuildAsymmetryLabel(s *model.LiveSnapshot) string {
	if s.PriorityLabel == "best_on_tape" && s.ActionabilityLabel == "not actionable" {
		return "best among weak tape"
	}
	if s.PriorityLabel == "best_on_tape" && s.ActionabilityLabel == "observe closely" {
		return "closest to upgrade"
	}
	if s.ActionabilityLabel == "actionable now" {
		return "live opportunity"
	}
	if s.TrustLabel == "insider-controlled" {
		return "likely distribution trap"
	}
	if s.TrustLabel == "compromised" {
		return "trust-degraded flow"
	}
	return "low asymmetry"
}

func BuildAsymmetryReason(s *model.LiveSnapshot) string {
	if s.PriorityLabel == "best_on_tape" && s.ActionabilityLabel == "not actionable" {
		return "this is the cleanest visible setup, but execution or trust still fails"
	}
	if s.PriorityLabel == "best_on_tape" && s.ActionabilityLabel == "observe closely" {
		return "this needs the fewest changes to become interesting"
	}
	if s.ActionabilityLabel == "actionable now" {
		return "this clears trust and execution thresholds better than peers"
	}
	if s.TrustLabel == "insider-controlled" {
		return "concentration makes this likely exit liquidity"
	}
	if s.TrustLabel == "compromised" {
		return "fallback or compression reduces trust in the apparent flow"
	}
	return "no material asymmetric edge versus peers"
}

func bestPriorityMint(rows []model.LiveSnapshot) string {
	if len(rows) == 0 {
		return ""
	}
	best := 0
	for i := 1; i < len(rows); i++ {
		if priorityLess(rows[best], rows[i]) {
			best = i
		}
	}
	return rows[best].Mint
}

func priorityLess(a model.LiveSnapshot, b model.LiveSnapshot) bool {
	if a.ConfidenceScore != b.ConfidenceScore {
		return a.ConfidenceScore < b.ConfidenceScore
	}
	if clusteringPriorityRank(a.ClusteringRowStatus) != clusteringPriorityRank(b.ClusteringRowStatus) {
		return clusteringPriorityRank(a.ClusteringRowStatus) < clusteringPriorityRank(b.ClusteringRowStatus)
	}
	if a.AdversarialScore != b.AdversarialScore {
		return a.AdversarialScore > b.AdversarialScore
	}
	if a.EffectiveBuyers5m != b.EffectiveBuyers5m {
		return a.EffectiveBuyers5m < b.EffectiveBuyers5m
	}
	return impactForPriority(a.EstimatedImpactPct) > impactForPriority(b.EstimatedImpactPct)
}

func clusteringPriorityRank(status string) int {
	switch status {
	case "resolved":
		return 3
	case "partial_fallback":
		return 2
	case "full_fallback":
		return 1
	default:
		return 0
	}
}

func impactForPriority(impact float64) float64 {
	if impact <= 0 {
		return math.MaxFloat64
	}
	return impact
}

func gateFailed(dec model.EngineDecision, gateID int) bool {
	for _, g := range dec.Gates {
		if g.ID == gateID {
			return !g.Passed && !g.Skipped
		}
	}
	return false
}

func compactReason(reason string) string {
	text := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(text, "holder balance"):
		return "no holder proxy"
	case strings.Contains(text, "market cap"):
		return "no market cap"
	case strings.Contains(text, "not yet observed"):
		return "incomplete structure"
	case strings.Contains(text, "token not yet"):
		return "still immature"
	default:
		return strings.TrimSpace(reason)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- Module 6B helpers ---

func clusterWindow(wallets []string, rawCount int, cfg LiveConfig, now time.Time) cluster.Result {
	if len(wallets) == 0 {
		return cluster.Result{
			EffectiveUniqueBuyerCount: rawCount,
			ClusteredBuyerCount:       0,
			FundingClusterRatio:       0,
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterWindowTimeout)
	defer cancel()
	return cluster.Cluster(ctx, wallets, cfg.FunderResolver, now)
}

func clusteringRowStatus(res cluster.Result, rawCount int) string {
	if rawCount == 0 || res.ResolverFallbackCount == 0 {
		return ClusteringResolved
	}
	if res.ResolverFallbackCount >= rawCount {
		return ClusteringFallback
	}
	return ClusteringPartial
}

func estimatedImpact(tradeSOL, liqSOL float64) float64 {
	if liqSOL <= 0 {
		return 0
	}
	pct := tradeSOL / liqSOL * 100
	if pct > 100 {
		return 100
	}
	return pct
}

func adversarialScore(snap model.TokenSnapshot) float64 {
	concentration := math.Min(1.0, snap.TopWalletBuyShareLast5m/0.5)
	diversity := 1.0 - snap.WalletDiversityRatio
	repeat := snap.RepeatBuyerShare1m
	score := 0.45*concentration + 0.30*diversity + 0.25*repeat
	return math.Max(0, math.Min(1, score))
}

func checkBUY(snap model.TokenSnapshot, cfg LiveConfig, execPenalty, advScore float64, effBuyers1m int, warmingUp bool, warmReason string) ([]string, bool) {
	var fails []string

	if warmingUp {
		fails = append(fails, warmReason)
	}
	if effBuyers1m < cfg.MinBuyers1mBUY {
		fails = append(fails, fmt.Sprintf("eff_buyers_1m=%d < %d (after parent clustering)", effBuyers1m, cfg.MinBuyers1mBUY))
	}
	accelOK := snap.BuyerAcceleration >= cfg.MinAccelerationBUY
	strongVel := effBuyers1m >= cfg.StrongVelocity1m
	if !accelOK && !strongVel {
		fails = append(fails, fmt.Sprintf("accel=%.2f < %.2f and eff_buyers_1m=%d < strong_vel=%d",
			snap.BuyerAcceleration, cfg.MinAccelerationBUY, effBuyers1m, cfg.StrongVelocity1m))
	}
	if execPenalty < cfg.MinExecQualityBUY {
		liqProxy := snap.TotalBuySOL + snap.TotalSellSOL
		impactPct := estimatedImpact(cfg.TradeSizeSOL, liqProxy)
		fails = append(fails, fmt.Sprintf("exec_penalty=%.2f < %.2f (liq_proxy=%.2f SOL, impact=%.1f%%)",
			execPenalty, cfg.MinExecQualityBUY, liqProxy, impactPct))
	}
	if snap.TotalBuySOL <= snap.TotalSellSOL {
		fails = append(fails, fmt.Sprintf("buy_sol=%.2f <= sell_sol=%.2f", snap.TotalBuySOL, snap.TotalSellSOL))
	}
	if advScore > cfg.MaxAdversarialBUY {
		fails = append(fails, fmt.Sprintf("adversarial=%.2f > max=%.2f", advScore, cfg.MaxAdversarialBUY))
	}
	if (snap.BuySolLast1m > 0 || snap.SellSolLast1m > 0) && snap.SellSolLast1m >= snap.BuySolLast1m {
		fails = append(fails, fmt.Sprintf("sell_reversal: sell_1m=%.2f >= buy_1m=%.2f",
			snap.SellSolLast1m, snap.BuySolLast1m))
	}

	if len(fails) > 0 {
		return fails, false
	}
	return []string{
		fmt.Sprintf("eff_buyers_1m=%d", effBuyers1m),
		fmt.Sprintf("accel=%.2f", snap.BuyerAcceleration),
		fmt.Sprintf("exec=%.2f", execPenalty),
		fmt.Sprintf("adv=%.2f", advScore),
	}, true
}

func checkREADY(snap model.TokenSnapshot, cfg LiveConfig, execPenalty, advScore float64, effBuyers5m int) ([]string, bool) {
	var fails []string

	if effBuyers5m < cfg.MinBuyers5mREADY {
		fails = append(fails, fmt.Sprintf("eff_buyers_5m=%d < %d", effBuyers5m, cfg.MinBuyers5mREADY))
	}
	if execPenalty < cfg.MinExecQualityREADY {
		liqProxy := snap.TotalBuySOL + snap.TotalSellSOL
		impactPct := estimatedImpact(cfg.TradeSizeSOL, liqProxy)
		fails = append(fails, fmt.Sprintf("exec_penalty=%.2f < %.2f (liq_proxy=%.2f SOL, impact=%.1f%%)",
			execPenalty, cfg.MinExecQualityREADY, liqProxy, impactPct))
	}
	if advScore > cfg.MaxAdversarialREADY {
		fails = append(fails, fmt.Sprintf("adversarial=%.2f > max=%.2f", advScore, cfg.MaxAdversarialREADY))
	}

	if len(fails) > 0 {
		return fails, false
	}
	return []string{
		fmt.Sprintf("eff_buyers_5m=%d", effBuyers5m),
		fmt.Sprintf("exec=%.2f", execPenalty),
		fmt.Sprintf("adv=%.2f", advScore),
	}, true
}

func checkWATCH(snap model.TokenSnapshot, cfg LiveConfig) ([]string, bool) {
	var fails []string
	if snap.UniqueBuyerCount < cfg.MinTotalBuyersWATCH {
		fails = append(fails, fmt.Sprintf("unique_buyers=%d < %d", snap.UniqueBuyerCount, cfg.MinTotalBuyersWATCH))
	}
	if len(fails) > 0 {
		return fails, false
	}
	return []string{fmt.Sprintf("unique_buyers=%d", snap.UniqueBuyerCount)}, true
}

func failReasons(context string, reasons []string) []string {
	if len(reasons) == 0 {
		return nil
	}
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = context + ": " + r
	}
	return out
}
