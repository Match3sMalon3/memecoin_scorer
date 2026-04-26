package proxy_test

import (
	"strings"
	"testing"

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
	if got.Band != "APEX" && got.Band != "CANDIDATE" {
		t.Fatalf("band=%q, want APEX or CANDIDATE", got.Band)
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

func TestScoreEarlyProxyDoesNotZeroForStructuralRiskUnlessHardVeto(t *testing.T) {
	row := strongRow()
	row.ClusteringRowStatus = "full_fallback"

	got := proxy.ScoreEarlyProxy(row)
	if got.Score == 0 {
		t.Fatalf("score was zeroed for non-hard structural risk: %+v", got)
	}
	if !contains(got.RiskFlags, "full clustering fallback") {
		t.Fatalf("risk_flags=%v, want full fallback risk", got.RiskFlags)
	}
}

func TestScoreEarlyProxyReturnsWatchForPromisingFlowWithPartialFallback(t *testing.T) {
	row := model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:      3,
			BuyersLast5m:      5,
			BuySolLast1m:      1.5,
			SellSolLast1m:     0.4,
			BuyerAcceleration: 1.2,
			HolderCount:       4,
			MarketCapSOL:      12,
			Top10HolderPct:    0.50,
		},
		EffectiveBuyers1m:   1,
		EffectiveBuyers5m:   3,
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

func strongRow() model.LiveSnapshot {
	return model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:      6,
			BuyersLast5m:      14,
			BuySolLast1m:      4,
			SellSolLast1m:     0.7,
			BuyerAcceleration: 2.4,
			HolderCount:       28,
			MarketCapSOL:      35,
			Top10HolderPct:    0.42,
		},
		Decision:            "AVOID",
		EffectiveBuyers1m:   5,
		EffectiveBuyers5m:   11,
		LiquidityProxySOL:   24,
		EstimatedImpactPct:  4.2,
		ClusteringRowStatus: "resolved",
		FundingClusterRatio: 0.08,
		AdversarialScore:    0.18,
		ExecutionPenalty:    0.8,
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
