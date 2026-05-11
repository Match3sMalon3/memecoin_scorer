package proxy_test

import (
	"strings"
	"testing"

	"memecoin_scorer/internal/live"
	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/proxy"
)

func TestScoreEarlyProxyRewardsEarlyRunnerFormationDespiteAvoid(t *testing.T) {
	row := strongRow()
	row.Decision = "AVOID"
	row.Reasons = []string{"structural engine rejected"}

	got := proxy.ScoreEarlyProxy(row)
	if got.Score < got.Threshold {
		t.Fatalf("score %.2f below threshold %.2f; reasons=%v risks=%v missing=%v", got.Score, got.Threshold, got.Reasons, got.RiskFlags, got.MissingFields)
	}
	if got.Band != "RUNNER" {
		t.Fatalf("band=%q, want RUNNER", got.Band)
	}
	if !contains(got.Reasons, "strong effective buyer depth") {
		t.Fatalf("reasons=%v, want effective buyer depth driver", got.Reasons)
	}
}

func TestScoreEarlyProxySeparatesMissingEvidenceFromRisk(t *testing.T) {
	row := model.LiveSnapshot{}

	got := proxy.ScoreEarlyProxy(row)
	if !contains(got.RiskFlags, "no real flow") {
		t.Fatalf("risk_flags=%v, want observed no-flow risk", got.RiskFlags)
	}
	if contains(got.MissingFields, "buyers_last1m") || contains(got.MissingFields, "buy_sol_last_1m") {
		t.Fatalf("missing=%v, observed zero buyer/flow fields must not be missing", got.MissingFields)
	}
}

func TestScoreEarlyProxyFullFallbackIsDead(t *testing.T) {
	row := strongRow()
	row.ClusteringRowStatus = "full_fallback"

	got := proxy.ScoreEarlyProxy(row)
	if got.Score != 0 || got.Band != "DEAD" {
		t.Fatalf("score=%.2f band=%q, want DEAD for full fallback", got.Score, got.Band)
	}
	if !contains(got.RiskFlags, "full clustering fallback") {
		t.Fatalf("risk_flags=%v, want full fallback risk", got.RiskFlags)
	}
}

func TestScoreEarlyProxyReturnsWatchForPromisingFlowWithPartialFallback(t *testing.T) {
	row := model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:      5,
			BuyersLast5m:      6,
			BuySolLast1m:      1.5,
			SellSolLast1m:     0.4,
			BuyerAcceleration: 1.2,
			HolderCount:       4,
			MarketCapSOL:      12,
			Top10HolderPct:    0.50,
		},
		EffectiveBuyers1m:   3,
		EffectiveBuyers5m:   5,
		LiquidityProxySOL:   8,
		EstimatedImpactPct:  20,
		ClusteringRowStatus: "partial_fallback",
		AdversarialScore:    0.20,
		ExecutionPenalty:    0.55,
	}

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "WATCH" {
		t.Fatalf("band=%q score=%.2f reasons=%v risks=%v missing=%v, want WATCH", got.Band, got.Score, got.Reasons, got.RiskFlags, got.MissingFields)
	}
	if got.Score == 0 {
		t.Fatal("partial fallback promising flow should not be zeroed")
	}
	if !contains(got.RiskFlags, "partial clustering fallback") {
		t.Fatalf("risk_flags=%v, want partial fallback risk", got.RiskFlags)
	}
}

func TestScoreEarlyProxyReturnsDeadForNoRealFlow(t *testing.T) {
	got := proxy.ScoreEarlyProxy(model.LiveSnapshot{})
	if got.Score != 0 {
		t.Fatalf("score=%.2f, want 0 for no real flow", got.Score)
	}
	if got.Band != "DEAD" {
		t.Fatalf("band=%q, want DEAD", got.Band)
	}
	if !contains(got.RiskFlags, "no real flow") {
		t.Fatalf("risk_flags=%v, want no real flow", got.RiskFlags)
	}
}

func TestScoreEarlyProxyHardVetoExtremeConcentration(t *testing.T) {
	row := strongRow()
	row.Top10HolderPct = 0.96

	got := proxy.ScoreEarlyProxy(row)
	if got.Score != 0 {
		t.Fatalf("score=%.2f, want 0 for extreme concentration", got.Score)
	}
	if got.Band != "DEAD" {
		t.Fatalf("band=%q, want DEAD", got.Band)
	}
	if !contains(got.RiskFlags, "extreme top10 concentration") {
		t.Fatalf("risk_flags=%v, want extreme concentration risk", got.RiskFlags)
	}
}

func TestScoreEarlyProxyUnreliableThinProxyWithCleanFlowMovesToWatch(t *testing.T) {
	row := unreliableThinProxyRow()

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "WATCH" {
		t.Fatalf("band=%q score=%.2f reasons=%v risks=%v, want WATCH", got.Band, got.Score, got.Reasons, got.RiskFlags)
	}
	if !contains(got.Reasons, "real buyer flow despite unreliable liquidity proxy") {
		t.Fatalf("reasons=%v, want unreliable-liquidity reclassification reason", got.Reasons)
	}
	if !contains(got.RiskFlags, "observed liq proxy below 5") {
		t.Fatalf("risk_flags=%v, want observed proxy liquidity risk", got.RiskFlags)
	}
}

func TestScoreEarlyProxyUnreliableThinProxyNoFlowRemainsDead(t *testing.T) {
	row := unreliableThinProxyRow()
	row.BuyersLast1m = 0
	row.BuyersLast5m = 0
	row.BuySolLast1m = 0
	row.EffectiveBuyers1m = 0
	row.EffectiveBuyers5m = 0

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "DEAD" {
		t.Fatalf("band=%q score=%.2f, want DEAD for no flow", got.Band, got.Score)
	}
	if !contains(got.RiskFlags, "no real flow") {
		t.Fatalf("risk_flags=%v, want no real flow", got.RiskFlags)
	}
}

func TestScoreEarlyProxyUnreliableThinProxyExtremeTop10RemainsDead(t *testing.T) {
	row := unreliableThinProxyRow()
	row.Top10HolderPct = 0.95

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "DEAD" || got.Score != 0 {
		t.Fatalf("band=%q score=%.2f, want DEAD score 0 for extreme top10", got.Band, got.Score)
	}
}

func TestScoreEarlyProxyUnreliableThinProxyFullFallbackRemainsDead(t *testing.T) {
	row := unreliableThinProxyRow()
	row.ClusteringRowStatus = "full_fallback"

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "DEAD" {
		t.Fatalf("band=%q score=%.2f risks=%v, want DEAD for full fallback", got.Band, got.Score, got.RiskFlags)
	}
}

func TestScoreEarlyProxyUnreliableLiquidityHighScoreCannotBeRunner(t *testing.T) {
	row := strongRow()
	row.RealPoolDepthSOL = -1
	row.LiquidityEvidenceSource = live.LiquidityEvidenceObservedSwapsProxy
	row.LiquidityProxyReliable = false
	row.LiquidityProxySOL = 50
	row.EstimatedImpactPct = 4

	got := proxy.ScoreEarlyProxy(row)
	if got.Band == "RUNNER" {
		t.Fatalf("band=%q score=%.2f risks=%v, want not RUNNER for unverified liquidity", got.Band, got.Score, got.RiskFlags)
	}
	if got.Band != "WATCH" {
		t.Fatalf("band=%q score=%.2f, want WATCH cap for high-score unverified liquidity", got.Band, got.Score)
	}
	if !contains(got.RiskFlags, "unverified pool depth") {
		t.Fatalf("risk_flags=%v, want unverified pool depth", got.RiskFlags)
	}
	if !contains(got.Reasons, "runner-like flow, liquidity unverified") {
		t.Fatalf("reasons=%v, want unverified liquidity flow reason", got.Reasons)
	}
}

func TestScoreEarlyProxyUnreliableLiquidityStrongFlowCappedToWatchOnHighImpact(t *testing.T) {
	row := strongRow()
	row.RealPoolDepthSOL = -1
	row.LiquidityEvidenceSource = live.LiquidityEvidenceObservedSwapsProxy
	row.LiquidityProxyReliable = false
	row.LiquidityProxySOL = 20
	row.EstimatedImpactPct = 55

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "WATCH" {
		t.Fatalf("band=%q score=%.2f risks=%v, want WATCH cap for unverified liquidity with high impact", got.Band, got.Score, got.RiskFlags)
	}
	if contains(got.Reasons, "minimum liquidity present") || contains(got.Reasons, "liquidity above minimum") {
		t.Fatalf("reasons=%v, must not claim verified liquidity when source is unreliable", got.Reasons)
	}
}

func TestScoreEarlyProxyReliableRealDepthCanBeRunner(t *testing.T) {
	row := reliableStrongRow()

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "RUNNER" {
		t.Fatalf("band=%q score=%.2f reasons=%v risks=%v, want RUNNER with verified depth", got.Band, got.Score, got.Reasons, got.RiskFlags)
	}
}

func TestScoreEarlyProxyReliableDepthExtremeTop10PreventsRunner(t *testing.T) {
	row := reliableStrongRow()
	row.Top10HolderPct = 0.95

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "DEAD" {
		t.Fatalf("band=%q score=%.2f, want DEAD for extreme top10 even with verified depth", got.Band, got.Score)
	}
}

func TestScoreEarlyProxyReliableDepthFullFallbackPreventsRunner(t *testing.T) {
	row := reliableStrongRow()
	row.ClusteringRowStatus = "full_fallback"

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "DEAD" {
		t.Fatalf("band=%q score=%.2f, want DEAD for full fallback even with verified depth", got.Band, got.Score)
	}
}

func TestRunnerInvariantHighScoreUnverifiedLiquidityCannotBeRunnerWithoutBonding(t *testing.T) {
	row := strongRow()
	row.RealPoolDepthSOL = -1
	row.LiquidityEvidenceSource = live.LiquidityEvidenceObservedSwapsProxy
	row.LiquidityProxyReliable = false
	row.LaunchEvidenceSource = ""
	row.LiquidityVelocityLabel = "exceptional"
	row.OrganicLiquidityVelocity = 2

	got := proxy.ScoreEarlyProxy(row)
	if got.Band == "RUNNER" || got.Band == "APEX" {
		t.Fatalf("band=%q score=%.2f risks=%v, unverified non-bonding liquidity must cap", got.Band, got.Score, got.RiskFlags)
	}
	if !contains(got.RiskFlags, "unverified liquidity") {
		t.Fatalf("risk_flags=%v, want unverified liquidity invariant reason", got.RiskFlags)
	}
}

func TestRunnerInvariantHighScoreUnverifiedLiquidityCannotBeApex(t *testing.T) {
	row := strongRow()
	row.RealPoolDepthSOL = -1
	row.LiquidityEvidenceSource = live.LiquidityEvidenceObservedSwapsProxy
	row.LiquidityProxyReliable = false
	row.LaunchEvidenceSource = ""
	row.SignalMode = "unknown"
	result := model.EarlyProxyScore{Score: 95, Threshold: 62, Band: "APEX"}

	got := proxy.EnforceRunnerInvariants(row, result)
	if got.Band == "APEX" {
		t.Fatalf("band=%q risks=%v, unverified liquidity cannot remain APEX", got.Band, got.RiskFlags)
	}
	if !contains(got.RiskFlags, "APEX capped: unverified liquidity") {
		t.Fatalf("risk_flags=%v, want APEX cap reason", got.RiskFlags)
	}
}

func TestRunnerInvariantUnverifiedBondingCanRemainRunner(t *testing.T) {
	row := strongRow()
	row.RealPoolDepthSOL = -1
	row.LiquidityEvidenceSource = live.LiquidityEvidenceObservedSwapsProxy
	row.LiquidityProxyReliable = false
	row.LaunchEvidenceSource = "pump_fun_bonding_curve"
	row.LiquidityVelocityLabel = "strong"
	row.OrganicLiquidityVelocity = 1.5
	row.BotFlags = nil
	row.AuthenticityLabel = "organic"

	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "RUNNER" {
		t.Fatalf("band=%q score=%.2f risks=%v, valid bonding state may remain RUNNER", got.Band, got.Score, got.RiskFlags)
	}
}

func TestRunnerInvariantBotLikeCannotBeRunner(t *testing.T) {
	row := reliableStrongRow()
	row.AuthenticityLabel = "bot_like"
	row.MechanicalityScore = 80

	got := proxy.ScoreEarlyProxy(row)
	if got.Band == "RUNNER" || got.Band == "APEX" {
		t.Fatalf("band=%q risks=%v, bot_like cannot be RUNNER/APEX", got.Band, got.RiskFlags)
	}
}

func TestRunnerInvariantBumpBundleCannotBeRunner(t *testing.T) {
	for name, mutate := range map[string]func(*model.LiveSnapshot){
		"bump":   func(row *model.LiveSnapshot) { row.BumpBotDetected = true },
		"bundle": func(row *model.LiveSnapshot) { row.BundleBotDetected = true },
	} {
		t.Run(name, func(t *testing.T) {
			row := reliableStrongRow()
			mutate(&row)
			got := proxy.ScoreEarlyProxy(row)
			if got.Band == "RUNNER" || got.Band == "APEX" {
				t.Fatalf("band=%q risks=%v, %s cannot be RUNNER/APEX", got.Band, got.RiskFlags, name)
			}
		})
	}
}

func TestRunnerInvariantOldTokenCannotBeLaunchRunner(t *testing.T) {
	row := reliableStrongRow()
	row.AgeSeconds = 3600
	row.LaunchEvidenceSource = ""
	row.SignalMode = "unknown"
	row.RunnerSubtype = "LAUNCH_RUNNER"

	got := proxy.ScoreEarlyProxy(row)
	if got.Band == "RUNNER" || got.Band == "APEX" {
		t.Fatalf("band=%q risks=%v, old unknown token cannot be launch RUNNER", got.Band, got.RiskFlags)
	}
}

func TestScoreEarlyProxyAppliesEvidenceCoverageDiscount(t *testing.T) {
	row := model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:      5,
			BuyersLast5m:      8,
			BuySolLast1m:      2,
			SellSolLast1m:     0.5,
			BuyerAcceleration: 1.2,
		},
		EffectiveBuyers1m: 3,
		EffectiveBuyers5m: 5,
	}

	got := proxy.ScoreEarlyProxy(row)
	if len(got.MissingFields) < 6 {
		t.Fatalf("missing_fields=%v, want at least 6", got.MissingFields)
	}
	if !contains(got.Reasons, "low evidence coverage") {
		t.Fatalf("reasons=%v, want low evidence coverage", got.Reasons)
	}
}

func TestScoreEarlyProxyObservedZeroBuyerFlowIsNotMissing(t *testing.T) {
	got := proxy.ScoreEarlyProxy(model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:  0,
			BuyersLast5m:  0,
			BuySolLast1m:  0,
			SellSolLast1m: 0,
		},
		EffectiveBuyers1m:   0,
		EffectiveBuyers5m:   0,
		ClusteringRowStatus: "resolved",
	})

	for _, field := range []string{"buyers_last1m", "buyers_last5m", "effective_buyers_1m", "effective_buyers_5m", "buy_sol_last_1m", "sell_sol_last_1m"} {
		if contains(got.MissingFields, field) {
			t.Fatalf("%s reported missing in %v; observed zero flow is valid evidence", field, got.MissingFields)
		}
	}
	if !contains(got.RiskFlags, "no real flow") {
		t.Fatalf("risk_flags=%v, want no real flow", got.RiskFlags)
	}
}

func TestScoreEarlyProxyPopulatedEvidenceDoesNotReportMissing(t *testing.T) {
	got := proxy.ScoreEarlyProxy(strongRow())
	if len(got.MissingFields) != 0 {
		t.Fatalf("missing_fields=%v, want none for populated evidence fixture", got.MissingFields)
	}
	if got.Score == 0 {
		t.Fatalf("score=0, want nonzero when evidence supports proxy")
	}
}

func TestScoreEarlyProxyAppliesVelocityBonus(t *testing.T) {
	base := model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:     2,
			BuyersLast5m:     4,
			BuySolLast1m:     0.5,
			SellSolLast1m:    0.1,
			HolderCount:      12,
			MarketCapSOL:     20,
			Top10HolderPct:   0.3,
			RealPoolDepthSOL: 12,
		},
		EffectiveBuyers1m:       2,
		EffectiveBuyers5m:       4,
		LiquidityProxySOL:       12,
		LiquidityEvidenceSource: "raydium_pc_vault",
		LiquidityProxyReliable:  true,
		EstimatedImpactPct:      8,
		ClusteringRowStatus:     "resolved",
	}
	withVelocity := base
	withVelocity.SolPerTrade5m = 1.0
	withVelocity.SolPerUniqueBuyer5m = 1.0

	baseScore := proxy.ScoreEarlyProxy(base).Score
	velocityScore := proxy.ScoreEarlyProxy(withVelocity).Score
	if velocityScore <= baseScore {
		t.Fatalf("velocity score %.1f <= base score %.1f", velocityScore, baseScore)
	}
}

func strongRow() model.LiveSnapshot {
	return model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:         6,
			BuyersLast5m:         14,
			BuySolLast1m:         4,
			SellSolLast1m:        0.7,
			BuyerAcceleration:    2.4,
			HolderCount:          28,
			MarketCapSOL:         35,
			Top10HolderPct:       0.42,
			RealPoolDepthSOL:     24,
			LaunchEvidenceSource: "pump_fun_bonding_curve",
		},
		Decision:                "AVOID",
		EffectiveBuyers1m:       5,
		EffectiveBuyers5m:       11,
		LiquidityProxySOL:       24,
		LiquidityEvidenceSource: "raydium_pc_vault",
		LiquidityProxyReliable:  true,
		EstimatedImpactPct:      4.2,
		ClusteringRowStatus:     "resolved",
		FundingClusterRatio:     0.08,
		AdversarialScore:        0.18,
		ExecutionPenalty:        0.8,
	}
}

func reliableStrongRow() model.LiveSnapshot {
	row := strongRow()
	row.RealPoolDepthSOL = 30
	row.LiquidityEvidenceSource = "raydium_pc_vault"
	row.LiquidityProxyReliable = true
	row.LiquidityProxySOL = 30
	row.EstimatedImpactPct = 4
	return row
}

func unreliableThinProxyRow() model.LiveSnapshot {
	return model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:      2,
			BuyersLast5m:      4,
			BuySolLast1m:      0.8,
			SellSolLast1m:     0.1,
			BuyerAcceleration: 1.1,
			HolderCount:       12,
			MarketCapSOL:      20,
			Top10HolderPct:    0.40,
		},
		Decision:                "AVOID",
		EffectiveBuyers1m:       2,
		EffectiveBuyers5m:       4,
		LiquidityProxySOL:       1.5,
		LiquidityEvidenceSource: live.LiquidityEvidenceObservedSwapsProxy,
		LiquidityProxyReliable:  false,
		EstimatedImpactPct:      40,
		ClusteringRowStatus:     "resolved",
		FundingClusterRatio:     0.05,
		AdversarialScore:        0.10,
		ExecutionPenalty:        0.08,
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}
