// Package engine implements the validated 7-point Organic Success framework.
// It runs as Layer 1 in the live decision pipeline, after Layer 0 adversarial
// rejection but before the label-assignment logic in internal/live.
//
// # Gate evaluation contract
//
// Every gate either passes, fails, or is skipped (insufficient data).
// A skipped gate does not count as a failure — it contributes neither to
// GatesPassCount nor to the hard-fail count that determines MaxLabel.
// This preserves backward compatibility: snapshots from tests or scenarios
// where the 7-gate fields have not been populated behave exactly as before.
//
// # MaxLabel semantics
//
// MaxLabel is the ceiling imposed on the live label:
//   - ""      no constraint (all gates skipped — treat as data-unavailable)
//   - "BUY"   all evaluated gates passed
//   - "READY" exactly 1 hard failure
//   - "WATCH" exactly 2 hard failures
//   - "AVOID" 3+ hard failures, Layer 0 reject, or Gate 1/7 hard-AVOID condition
package engine

import (
	"fmt"
	"math"
	"sort"

	"memecoin_scorer/internal/model"
)

// Gate IDs match the spec numbering.
const (
	GateIDLiquidityMC     = 1
	GateIDSupplyConc      = 2
	GateIDBundleFunder    = 3
	GateIDVolumeMC        = 4
	GateIDOrganicWinners  = 5
	GateIDHolderGrowth    = 6
	GateIDSlippageCeiling = 7
)

// EngineConfig holds all thresholds used by the 7-gate engine.
// All defaults are named constants — change only via env/config injection.
type EngineConfig struct {
	// Gate 1 — Liquidity / MC ratio
	MinLiqMCRatioPctBUY float64 // default 5.0  — SUCCESS-eligible threshold
	AvoidLiqMCRatioPct  float64 // default 3.0  — hard AVOID below this

	// Gate 2 — Supply concentration
	MaxTop10HolderPct   float64 // default 0.15 (15%) for tokens < NewTokenMaxAgeHours
	NewTokenMaxAgeHours float64 // default 6.0

	// Gate 3 — Bundle / shared funder detection
	MaxSharedFunderRatio float64 // default 0.05 (5%)

	// Gate 4 — Volume / MC ratio
	MinVolMCRatio float64 // default 0.01
	MaxVolMCRatio float64 // default 1.0

	// Gate 5 — Organic winners
	MinOrganicWinners int // default 10

	// Gate 6 — Holder growth (30m vs 60m): implicit — 60m holders must exceed 30m

	// Gate 7 — Execution reality / slippage ceiling
	SellTestSOL        float64 // default 25.0 (≈$5 000 at $200/SOL)
	MaxSellSlippagePct float64 // default 5.0
	SlippageCapScore   int     // default 40 — ConfidenceScore ceiling when gate 7 fails

	// Layer 0 thresholds
	MaxLayer0ClusterRatio float64 // default 0.80 — near-total sybil → hard reject
	MinExecLiqSOL         float64 // default 5.0  — minimum executable liquidity
}

// DefaultEngineConfig returns conservative production defaults.
// These are unvalidated priors — retune after 200+ live signals.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		MinLiqMCRatioPctBUY:   5.0,
		AvoidLiqMCRatioPct:    3.0,
		MaxTop10HolderPct:     0.15,
		NewTokenMaxAgeHours:   6.0,
		MaxSharedFunderRatio:  0.05,
		MinVolMCRatio:         0.01,
		MaxVolMCRatio:         1.0,
		MinOrganicWinners:     10,
		SellTestSOL:           25.0,
		MaxSellSlippagePct:    5.0,
		SlippageCapScore:      40,
		MaxLayer0ClusterRatio: 0.80,
		MinExecLiqSOL:         5.0,
	}
}

// EvaluateGates runs Layer 0 checks followed by all 7 success gates.
//
// clusterRatio is the FundingClusterRatio produced by the clustering layer
// (passed in to avoid re-computing it inside the engine).
//
// Returns a model.EngineDecision that constrains the live label assignment
// in internal/live.ClassifyAt.
func EvaluateGates(snap model.TokenSnapshot, clusterRatio float64, cfg EngineConfig) model.EngineDecision {
	// Always initialise Gates to a non-nil empty slice so JSON encodes as []
	// rather than null — even when Layer 0 rejects before gate evaluation.
	dec := model.EngineDecision{Gates: []model.GateResult{}}

	// ---- Layer 0: hard reject before any gate evaluation ----
	if reject, reason := layer0(snap, clusterRatio, cfg); reject {
		dec.Layer0Reject = true
		dec.Layer0Reason = reason
		dec.MaxLabel = "AVOID"
		// Gates stays [] — Layer 0 short-circuits gate evaluation.
		// Consumers should check layer0_reject first; an empty gates list
		// here means "not evaluated" not "all passed".
		return dec
	}

	// ---- Gates 1–7 ----
	gates := []model.GateResult{
		gate1LiquidityMC(snap, cfg),
		gate2SupplyConc(snap, cfg),
		gate3BundleFunder(clusterRatio, cfg),
		gate4VolumeMC(snap, cfg),
		gate5OrganicWinners(snap, cfg),
		gate6HolderGrowth(snap),
		gate7SlippageCeiling(snap, cfg),
	}
	dec.Gates = gates

	// Count hard failures (not skipped).
	hardFails := 0
	passCount := 0
	for _, g := range gates {
		if g.Skipped {
			continue
		}
		if g.Passed {
			passCount++
		} else {
			hardFails++
		}
	}
	dec.GatesPassCount = passCount

	// Gate 7 fail → cap confidence score and force AVOID.
	g7 := gates[GateIDSlippageCeiling-1]
	if !g7.Passed && !g7.Skipped {
		dec.ScoreCap = cfg.SlippageCapScore
		dec.MaxLabel = "AVOID"
		return dec
	}

	// Gate 1 hard-AVOID: liq/MC below the absolute floor.
	g1 := gates[GateIDLiquidityMC-1]
	if !g1.Passed && !g1.Skipped && snap.MarketCapSOL > 0 {
		ratio := snap.LiquidityPoolSOL / snap.MarketCapSOL * 100
		if ratio < cfg.AvoidLiqMCRatioPct {
			dec.MaxLabel = "AVOID"
			return dec
		}
	}

	// Gate 6 hard rule: holder stall or decline → force AVOID regardless of other gates.
	// A token that stopped attracting holders at 60m is structurally unsound.
	g6 := gates[GateIDHolderGrowth-1]
	if !g6.Passed && !g6.Skipped {
		dec.MaxLabel = "AVOID"
		return dec
	}

	// Determine MaxLabel from hard failure count.
	// If all evaluated gates skipped → no constraint → MaxLabel = ""
	totalEvaluated := 0
	for _, g := range gates {
		if !g.Skipped {
			totalEvaluated++
		}
	}
	if totalEvaluated == 0 {
		// No gate had sufficient data; impose no constraint.
		dec.MaxLabel = ""
		return dec
	}

	switch {
	case hardFails == 0:
		dec.MaxLabel = "BUY"
	case hardFails == 1:
		dec.MaxLabel = "READY"
	case hardFails == 2:
		dec.MaxLabel = "WATCH"
	default:
		dec.MaxLabel = "AVOID"
	}

	// Gate 4 hard rule: vol/MC out of range → cap MaxLabel at WATCH.
	// Anomalous volume (too thin or wash-like) makes READY or BUY unsafe.
	g4 := gates[GateIDVolumeMC-1]
	if !g4.Passed && !g4.Skipped {
		if dec.MaxLabel == "BUY" || dec.MaxLabel == "READY" {
			dec.MaxLabel = "WATCH"
		}
	}

	return dec
}

// ---- Layer 0 ----

func layer0(snap model.TokenSnapshot, clusterRatio float64, cfg EngineConfig) (bool, string) {
	// Self-bundled: overwhelming shared-funder ratio → synthetic launch.
	if clusterRatio > cfg.MaxLayer0ClusterRatio {
		return true, fmt.Sprintf("self_bundled: cluster_ratio=%.2f > %.2f", clusterRatio, cfg.MaxLayer0ClusterRatio)
	}
	// Impossible execution: pool too thin to trade at any size.
	liq := snap.LiquidityPoolSOL
	if liq > 0 && liq < cfg.MinExecLiqSOL {
		return true, fmt.Sprintf("impossible_execution: liquidity=%.2f SOL < %.2f SOL minimum", liq, cfg.MinExecLiqSOL)
	}
	return false, ""
}

// ---- Gate 1: Liquidity / Market Cap ratio ----

func gate1LiquidityMC(snap model.TokenSnapshot, cfg EngineConfig) model.GateResult {
	g := model.GateResult{ID: GateIDLiquidityMC, Name: "liquidity_mc_ratio"}

	if snap.MarketCapSOL <= 0 || snap.LiquidityPoolSOL <= 0 {
		g.Skipped = true
		g.Reason = missingMarketDataReason(snap, "liq/mc")
		return g
	}

	ratio := snap.LiquidityPoolSOL / snap.MarketCapSOL * 100
	g.Value = ratio
	g.Threshold = cfg.MinLiqMCRatioPctBUY

	if ratio >= cfg.MinLiqMCRatioPctBUY {
		g.Passed = true
		g.Margin = ratio - cfg.MinLiqMCRatioPctBUY
		g.Reason = fmt.Sprintf("liq_mc_ratio=%.2f%% >= %.1f%%", ratio, cfg.MinLiqMCRatioPctBUY)
	} else {
		g.Margin = ratio - cfg.MinLiqMCRatioPctBUY // negative
		if ratio < cfg.AvoidLiqMCRatioPct {
			g.Reason = fmt.Sprintf("liq_mc_ratio=%.2f%% < avoid_floor=%.1f%% (hard AVOID)", ratio, cfg.AvoidLiqMCRatioPct)
		} else {
			g.Reason = fmt.Sprintf("liq_mc_ratio=%.2f%% < success_threshold=%.1f%%", ratio, cfg.MinLiqMCRatioPctBUY)
		}
	}
	return g
}

// ---- Gate 2: Supply concentration ----

func gate2SupplyConc(snap model.TokenSnapshot, cfg EngineConfig) model.GateResult {
	g := model.GateResult{ID: GateIDSupplyConc, Name: "supply_concentration"}

	if snap.Top10HolderPct <= 0 {
		g.Skipped = true
		g.Reason = "holder balance data not yet available"
		return g
	}

	ageHours := snap.AgeSeconds / 3600
	if ageHours > cfg.NewTokenMaxAgeHours {
		// Gate only applies to new tokens (< 6h old).
		g.Passed = true
		g.Skipped = false
		g.Reason = fmt.Sprintf("token_age=%.1fh > %.0fh gate window — skipping", ageHours, cfg.NewTokenMaxAgeHours)
		return g
	}

	g.Value = snap.Top10HolderPct
	g.Threshold = cfg.MaxTop10HolderPct

	if snap.Top10HolderPct <= cfg.MaxTop10HolderPct {
		g.Passed = true
		g.Margin = cfg.MaxTop10HolderPct - snap.Top10HolderPct
		g.Reason = fmt.Sprintf("top10_holder_pct=%.1f%% <= %.1f%% max (token_age=%.1fh)",
			snap.Top10HolderPct*100, cfg.MaxTop10HolderPct*100, ageHours)
	} else {
		g.Margin = cfg.MaxTop10HolderPct - snap.Top10HolderPct // negative
		g.Reason = fmt.Sprintf("top10_holder_pct=%.1f%% > %.1f%% max (token_age=%.1fh)",
			snap.Top10HolderPct*100, cfg.MaxTop10HolderPct*100, ageHours)
	}
	return g
}

// ---- Gate 3: Bundle / shared funder detection ----

func gate3BundleFunder(clusterRatio float64, cfg EngineConfig) model.GateResult {
	g := model.GateResult{
		ID:        GateIDBundleFunder,
		Name:      "bundle_shared_funder",
		Value:     clusterRatio,
		Threshold: cfg.MaxSharedFunderRatio,
	}

	// clusterRatio is always available (0 when clustering is null/degraded).
	// When clustering is degraded, clusterRatio = 0 → gate passes trivially.
	// The clustering-required gate in live/decision.go handles the degraded case separately.
	g.Margin = cfg.MaxSharedFunderRatio - clusterRatio

	if clusterRatio <= cfg.MaxSharedFunderRatio {
		g.Passed = true
		g.Reason = fmt.Sprintf("shared_funder_ratio=%.3f <= %.3f max", clusterRatio, cfg.MaxSharedFunderRatio)
	} else {
		g.Reason = fmt.Sprintf("shared_funder_ratio=%.3f > %.3f max", clusterRatio, cfg.MaxSharedFunderRatio)
	}
	return g
}

// ---- Gate 4: Volume / MC ratio ----

func gate4VolumeMC(snap model.TokenSnapshot, cfg EngineConfig) model.GateResult {
	g := model.GateResult{ID: GateIDVolumeMC, Name: "volume_mc_ratio"}

	if snap.MarketCapSOL <= 0 || snap.Volume24hSOL <= 0 {
		g.Skipped = true
		g.Reason = missingMarketDataReason(snap, "vol/mc")
		return g
	}

	ratio := snap.Volume24hSOL / snap.MarketCapSOL
	g.Value = ratio
	// Threshold shown as min; range check [min, max]
	g.Threshold = cfg.MinVolMCRatio

	inRange := ratio >= cfg.MinVolMCRatio && ratio <= cfg.MaxVolMCRatio
	if inRange {
		g.Passed = true
		// Margin as distance from nearest boundary
		g.Margin = math.Min(ratio-cfg.MinVolMCRatio, cfg.MaxVolMCRatio-ratio)
		g.Reason = fmt.Sprintf("vol_mc_ratio=%.4f in [%.2f, %.2f]", ratio, cfg.MinVolMCRatio, cfg.MaxVolMCRatio)
	} else {
		if ratio < cfg.MinVolMCRatio {
			g.Margin = ratio - cfg.MinVolMCRatio // negative
			g.Reason = fmt.Sprintf("vol_mc_ratio=%.4f < min=%.2f (too thin)", ratio, cfg.MinVolMCRatio)
		} else {
			g.Margin = cfg.MaxVolMCRatio - ratio // negative
			g.Reason = fmt.Sprintf("vol_mc_ratio=%.4f > max=%.2f (suspicious volume)", ratio, cfg.MaxVolMCRatio)
		}
	}
	return g
}

func missingMarketDataReason(snap model.TokenSnapshot, gate string) string {
	switch {
	case snap.MarketCapSOL <= 0 && snap.MarketCapReason != "":
		return fmt.Sprintf("%s skipped: %s", gate, snap.MarketCapReason)
	case snap.MarketCapSOL <= 0 && snap.LastPriceReason != "":
		return fmt.Sprintf("%s skipped: %s", gate, snap.LastPriceReason)
	case gate == "liq/mc" && snap.LiquidityPoolSOL <= 0:
		return "liq/mc skipped: liquidity not yet observed"
	case gate == "vol/mc" && snap.Volume24hSOL <= 0:
		return "vol/mc skipped: volume not yet observed"
	default:
		return fmt.Sprintf("%s skipped: market cap or activity not yet observed", gate)
	}
}

// ---- Gate 5: Organic winners ----

func gate5OrganicWinners(snap model.TokenSnapshot, cfg EngineConfig) model.GateResult {
	g := model.GateResult{
		ID:        GateIDOrganicWinners,
		Name:      "organic_winners",
		Threshold: float64(cfg.MinOrganicWinners),
	}

	// Skip if the token hasn't been live long enough for organic winners to form.
	// We need at least enough time for buys after minute 5 AND subsequent sells.
	if snap.HoldersAt60m == 0 {
		g.Skipped = true
		g.Reason = "token not yet 60m old — organic winner data accumulating"
		return g
	}

	g.Value = float64(snap.OrganicWinnerCount)
	g.Margin = float64(snap.OrganicWinnerCount - cfg.MinOrganicWinners)

	if snap.OrganicWinnerCount >= cfg.MinOrganicWinners {
		g.Passed = true
		g.Reason = fmt.Sprintf("organic_winners=%d >= %d required", snap.OrganicWinnerCount, cfg.MinOrganicWinners)
	} else {
		g.Reason = fmt.Sprintf("organic_winners=%d < %d required (bought>5m, not deployer, >50%% profit)",
			snap.OrganicWinnerCount, cfg.MinOrganicWinners)
	}
	return g
}

// ---- Gate 6: Holder growth / stall test ----

func gate6HolderGrowth(snap model.TokenSnapshot) model.GateResult {
	g := model.GateResult{ID: GateIDHolderGrowth, Name: "holder_growth"}

	if snap.HoldersAt30m == 0 || snap.HoldersAt60m == 0 {
		g.Skipped = true
		g.Reason = "holder snapshots not yet captured (token < 60m old)"
		return g
	}

	growth := snap.HoldersAt60m - snap.HoldersAt30m
	g.Value = float64(snap.HoldersAt60m)
	g.Threshold = float64(snap.HoldersAt30m)
	g.Margin = float64(growth)

	if growth > 0 {
		g.Passed = true
		g.Reason = fmt.Sprintf("holders: 60m=%d > 30m=%d (growth=%+d)", snap.HoldersAt60m, snap.HoldersAt30m, growth)
	} else {
		g.Reason = fmt.Sprintf("holder_growth stalled: 60m=%d vs 30m=%d (growth=%d)", snap.HoldersAt60m, snap.HoldersAt30m, growth)
	}
	return g
}

// ---- Gate 7: Execution reality / slippage ceiling ----

func gate7SlippageCeiling(snap model.TokenSnapshot, cfg EngineConfig) model.GateResult {
	g := model.GateResult{
		ID:        GateIDSlippageCeiling,
		Name:      "slippage_ceiling",
		Threshold: cfg.MaxSellSlippagePct,
	}

	if snap.LiquidityPoolSOL <= 0 {
		g.Skipped = true
		g.Reason = "liquidity not yet observed"
		return g
	}

	slippagePct := cfg.SellTestSOL / snap.LiquidityPoolSOL * 100
	g.Value = slippagePct
	g.Margin = cfg.MaxSellSlippagePct - slippagePct

	if slippagePct <= cfg.MaxSellSlippagePct {
		g.Passed = true
		g.Reason = fmt.Sprintf("est_slippage=%.1f%% <= %.1f%% max ($%.0f test / %.0f SOL pool)",
			slippagePct, cfg.MaxSellSlippagePct, cfg.SellTestSOL*200, snap.LiquidityPoolSOL)
	} else {
		g.Reason = fmt.Sprintf("est_slippage=%.1f%% > %.1f%% max ($%.0f test / %.0f SOL pool) — score capped at %d",
			slippagePct, cfg.MaxSellSlippagePct, cfg.SellTestSOL*200, snap.LiquidityPoolSOL, cfg.SlippageCapScore)
	}
	return g
}

// ---- helpers ----

// ComputeTop10HolderPct computes the top-10 holder concentration from a wallet→netTokens map.
// Returns 0 when the map is empty or total outstanding supply is 0.
// Exported for use by the state store.
func ComputeTop10HolderPct(walletNetTokens map[string]float64) float64 {
	if len(walletNetTokens) == 0 {
		return 0
	}
	// Collect positive balances only (holders, not sellers with net-zero).
	balances := make([]float64, 0, len(walletNetTokens))
	total := 0.0
	for _, v := range walletNetTokens {
		if v > 0 {
			balances = append(balances, v)
			total += v
		}
	}
	if total == 0 || len(balances) == 0 {
		return 0
	}
	sort.Slice(balances, func(i, j int) bool { return balances[i] > balances[j] })

	top := 0.0
	for i := 0; i < len(balances) && i < 10; i++ {
		top += balances[i]
	}
	return top / total
}

// CountHolders returns the number of wallets with a strictly positive net token balance.
// Exported for use by the state store.
func CountHolders(walletNetTokens map[string]float64) int {
	n := 0
	for _, v := range walletNetTokens {
		if v > 0 {
			n++
		}
	}
	return n
}
