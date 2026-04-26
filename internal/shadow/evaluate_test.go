package shadow

import (
	"encoding/json"
	"testing"
	"time"

	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/scoring"
)

func TestEvaluateShadowScore_IncompleteCoverageDoesNotScore(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snap := model.TokenSnapshot{
		Mint:        "INCOMPLETE",
		FirstSeenAt: now.Add(-40 * time.Minute),
		ShadowFeatures: model.ShadowFeatureInputs{
			BuySol0_35m:          10,
			HasBuySol0_35m:       true,
			SellSol0_35m:         4,
			HasSellSol0_35m:      true,
			WalletsThatExited:    1,
			HasWalletsThatExited: true,
		},
	}

	result := EvaluateShadowScore(&snap, now)
	if result.EligibleForShadowScore {
		t.Fatal("incomplete shadow inputs must not be eligible for scoring")
	}
	if !result.FeatureWindowComplete {
		t.Fatal("expected mature feature window")
	}
	for _, want := range []string{"mfe_multiple_30m", "cohort_buyer_count", "sniper_intensity_ratio"} {
		if !hasString(result.MissingFields, want) {
			t.Fatalf("missing_fields = %v, want %q", result.MissingFields, want)
		}
	}
	if result.OpportunityScore != nil || result.ValidatedTradeable30m != nil || result.ValidatedClean30m != nil {
		t.Fatalf("incomplete result must not include scorer outputs: %+v", result)
	}
}

func TestEvaluateShadowScore_CompleteCoverageScores(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snap := completeShadowSnap(now)

	result := EvaluateShadowScore(&snap, now)
	if !result.EligibleForShadowScore {
		t.Fatalf("complete shadow inputs should be eligible: %+v", result)
	}
	if result.ValidatedTradeable30m == nil || !*result.ValidatedTradeable30m {
		t.Fatalf("validated_tradeable_30m = %v, want true", result.ValidatedTradeable30m)
	}
	if result.ValidatedClean30m == nil || !*result.ValidatedClean30m {
		t.Fatalf("validated_clean_30m = %v, want true", result.ValidatedClean30m)
	}
	if result.OpportunityScore == nil || *result.OpportunityScore <= 0 {
		t.Fatalf("opportunity_score = %v, want positive", result.OpportunityScore)
	}

	tf, coverage := BuildShadowTokenFeatures(&snap, now)
	if len(coverage.MissingFields) != 0 {
		t.Fatalf("coverage missing fields: %v", coverage.MissingFields)
	}
	cfg, err := loadScoringConfig()
	if err != nil {
		t.Fatalf("loadScoringConfig: %v", err)
	}
	expected := scoring.Score(tf, cfg)
	if *result.OpportunityScore != expected.OpportunityScore {
		t.Fatalf("opportunity_score = %.6f, want %.6f", *result.OpportunityScore, expected.OpportunityScore)
	}
}

func TestLiveSnapshotJSONContainsShadow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snap := completeShadowSnap(now)
	result := EvaluateShadowScore(&snap, now)
	payload, err := json.Marshal(model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{Mint: "JSON"}, Shadow: result})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	shadowObj, ok := decoded["shadow"].(map[string]any)
	if !ok {
		t.Fatalf("shadow object missing from JSON: %s", string(payload))
	}
	if _, ok := shadowObj["eligible_for_shadow_score"]; !ok {
		t.Fatalf("eligible_for_shadow_score missing from shadow JSON: %v", shadowObj)
	}
}

func completeShadowSnap(now time.Time) model.TokenSnapshot {
	return model.TokenSnapshot{
		Mint:        "COMPLETE",
		FirstSeenAt: now.Add(-40 * time.Minute),
		ShadowFeatures: model.ShadowFeatureInputs{
			CohortBuyerCount:           50,
			HasCohortBuyerCount:        true,
			MfeMultiple30m:             2.5,
			HasMfeMultiple30m:          true,
			BuySol0_35m:                100,
			HasBuySol0_35m:             true,
			SellSol0_35m:               50,
			HasSellSol0_35m:            true,
			SellTradeCount5to35m:       25,
			HasSellTradeCount5to35m:    true,
			SellUniqueTraders5to35m:    10,
			HasSellUniqueTraders5to35m: true,
			ManipulationRiskScore:      0,
			HasManipulationRiskScore:   true,
			FirstMinuteShare:           0.10,
			HasFirstMinuteShare:        true,
			SniperIntensityRatio:       0.10,
			HasSniperIntensityRatio:    true,
			SizeDiversityRatio:         0.60,
			HasSizeDiversityRatio:      true,
			WalletsThatExited:          20,
			HasWalletsThatExited:       true,
			MedianRealizedReturnPct:    15,
			HasMedianRealizedReturnPct: true,
			WalletsGt25Pct:             8,
			HasWalletsGt25Pct:          true,
			WinnerExitRatio:            0.40,
			HasWinnerExitRatio:         true,
		},
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
