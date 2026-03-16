package scoring_test

import (
	"testing"

	"memecoin_scorer/internal/config"
	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/scoring"
)

// testCfg returns the frozen v9 config (must not be modified between runs).
func testCfg() config.Config {
	return config.Config{
		Thresholds: config.Thresholds{
			MinCohortBuyers:           10,
			MfeThreshold:              1.20,
			MinSellTrades:             20,
			MinSellUniqueTraders:      5,
			MaxManipulationRiskScore:  0,
			MaxFirstMinuteShare:       0.25,
			MaxSniperIntensityRatio:   0.30,
			MinSizeDiversityRatio:     0.35,
			MinWalletsThatExited:      5,
			MinMedianRealizedReturn:   0.0,
			MinRealizedReturnForClean: 10.0,
			MinWinnerRatioForClean:    0.30,
		},
		Weights: config.Weights{
			Opportunity:  0.50,
			Adversarial:  0.30,
			Monetization: 0.20,
		},
	}
}

// cleanWinner returns a TokenFeatures that passes every gate (tradeable and clean).
func cleanWinner() model.TokenFeatures {
	return model.TokenFeatures{
		TokenRow: model.TokenRow{
			TokenMint:               "CLEAN_TOKEN",
			CohortBuyerCount:        50,
			SniperIntensityRatio:    0.10,
			FirstMinuteShare:        0.10,
			SizeDiversityRatio:      0.60,
			ManipulationRiskScore:   0,
			MfeMultiple30m:          2.50,
			MedianRealizedReturnPct: 15.0,
			WalletsThatExited:       20,
			WalletsGt25Pct:          8,
			BuySol0_35m:             100.0,
			SellSol0_35m:            50.0,
		},
		WinnerExitRatio: 8.0 / 20.0, // 0.40
		BuyFlowPct:      100.0 / 150.0,
	}
}

func TestScore_CleanWinner_AllTrue(t *testing.T) {
	res := scoring.Score(cleanWinner(), testCfg())
	if !res.IsTradeable30m {
		t.Error("expected IsTradeable30m = true")
	}
	if !res.IsCleanTradeable30m {
		t.Error("expected IsCleanTradeable30m = true")
	}
	if res.OpportunityScore < 0 || res.OpportunityScore > 100 {
		t.Errorf("OpportunityScore %.2f out of [0,100]", res.OpportunityScore)
	}
	if res.OpportunityScore == 0 {
		t.Error("clean winner should have OpportunityScore > 0")
	}
}

func TestScore_OpportunityScoreRange(t *testing.T) {
	cfg := testCfg()
	tests := []struct {
		name string
		f    model.TokenFeatures
	}{
		{"clean winner", cleanWinner()},
		{"zero score (fails gates)", func() model.TokenFeatures {
			f := cleanWinner()
			f.CohortBuyerCount = 1 // below min
			return f
		}()},
	}
	for _, tt := range tests {
		res := scoring.Score(tt.f, cfg)
		if res.OpportunityScore < 0 || res.OpportunityScore > 100 {
			t.Errorf("%s: OpportunityScore %.2f out of [0,100]", tt.name, res.OpportunityScore)
		}
	}
}

func TestScore_Gate_MinCohortBuyers(t *testing.T) {
	f := cleanWinner()
	f.CohortBuyerCount = 9 // one below minimum of 10
	res := scoring.Score(f, testCfg())
	if res.IsTradeable30m {
		t.Error("expected IsTradeable30m = false when cohort_buyer_count < min")
	}
	if res.OpportunityScore != 0 {
		t.Errorf("expected OpportunityScore = 0 for non-tradeable, got %.2f", res.OpportunityScore)
	}
}

func TestScore_Gate_ManipulationRisk(t *testing.T) {
	f := cleanWinner()
	f.ManipulationRiskScore = 1 // above max of 0
	res := scoring.Score(f, testCfg())
	if res.IsTradeable30m {
		t.Error("expected IsTradeable30m = false when manipulation_risk_score > 0")
	}
}

func TestScore_Gate_MaxFirstMinuteShare(t *testing.T) {
	f := cleanWinner()
	f.FirstMinuteShare = 0.30 // above max of 0.25
	res := scoring.Score(f, testCfg())
	if res.IsTradeable30m {
		t.Error("expected IsTradeable30m = false when first_minute_share > max")
	}
}

func TestScore_Gate_MaxSniperIntensityRatio(t *testing.T) {
	f := cleanWinner()
	f.SniperIntensityRatio = 0.31 // above max of 0.30
	res := scoring.Score(f, testCfg())
	if res.IsTradeable30m {
		t.Error("expected IsTradeable30m = false when sniper_intensity_ratio > max")
	}
}

func TestScore_Gate_MinSizeDiversityRatio(t *testing.T) {
	f := cleanWinner()
	f.SizeDiversityRatio = 0.34 // below min of 0.35
	res := scoring.Score(f, testCfg())
	if res.IsTradeable30m {
		t.Error("expected IsTradeable30m = false when size_diversity_ratio < min")
	}
}

func TestScore_Gate_MinWalletsThatExited(t *testing.T) {
	f := cleanWinner()
	f.WalletsThatExited = 4 // below min of 5
	f.WinnerExitRatio = 0   // recalculate
	res := scoring.Score(f, testCfg())
	if res.IsTradeable30m {
		t.Error("expected IsTradeable30m = false when wallets_that_exited < min")
	}
}

func TestScore_Gate_MinMedianRealizedReturn(t *testing.T) {
	f := cleanWinner()
	f.MedianRealizedReturnPct = -0.1 // below min of 0.0
	res := scoring.Score(f, testCfg())
	if res.IsTradeable30m {
		t.Error("expected IsTradeable30m = false when median_realized_return_pct < 0")
	}
}

func TestScore_Gate_MfeThreshold(t *testing.T) {
	f := cleanWinner()
	f.MfeMultiple30m = 1.19 // below threshold of 1.20
	res := scoring.Score(f, testCfg())
	if res.IsTradeable30m {
		t.Error("expected IsTradeable30m = false when mfe_multiple_30m < threshold")
	}
}

func TestScore_CleanGate_MedianReturn(t *testing.T) {
	f := cleanWinner()
	f.MedianRealizedReturnPct = 9.9 // below min_realized_return_for_clean (10.0) but >= 0
	res := scoring.Score(f, testCfg())
	if !res.IsTradeable30m {
		t.Error("expected IsTradeable30m = true")
	}
	if res.IsCleanTradeable30m {
		t.Error("expected IsCleanTradeable30m = false when median_return < min_for_clean")
	}
}

func TestScore_CleanGate_WinnerRatio(t *testing.T) {
	f := cleanWinner()
	// Set winner ratio below 0.30: 1/10 = 0.10
	f.WalletsThatExited = 10
	f.WalletsGt25Pct = 1
	f.WinnerExitRatio = 1.0 / 10.0
	res := scoring.Score(f, testCfg())
	if !res.IsTradeable30m {
		t.Error("expected IsTradeable30m = true")
	}
	if res.IsCleanTradeable30m {
		t.Error("expected IsCleanTradeable30m = false when winner_exit_ratio < min")
	}
}

func TestScore_ComponentsPopulated(t *testing.T) {
	res := scoring.Score(cleanWinner(), testCfg())
	if res.OpportunityComponent < 0 || res.OpportunityComponent > 100 {
		t.Errorf("OpportunityComponent %.2f out of [0,100]", res.OpportunityComponent)
	}
	if res.AdversarialComponent < 0 || res.AdversarialComponent > 100 {
		t.Errorf("AdversarialComponent %.2f out of [0,100]", res.AdversarialComponent)
	}
	if res.MonetizationComponent < 0 || res.MonetizationComponent > 100 {
		t.Errorf("MonetizationComponent %.2f out of [0,100]", res.MonetizationComponent)
	}
}

func TestScore_FeaturePassthrough(t *testing.T) {
	f := cleanWinner()
	f.SniperIntensityRatio = 0.12
	f.FirstMinuteShare = 0.08
	f.WinnerExitRatio = 0.45
	res := scoring.Score(f, testCfg())
	if res.SniperIntensityRatio != 0.12 {
		t.Errorf("SniperIntensityRatio passthrough: got %.4f, want 0.12", res.SniperIntensityRatio)
	}
	if res.FirstMinuteShare != 0.08 {
		t.Errorf("FirstMinuteShare passthrough: got %.4f, want 0.08", res.FirstMinuteShare)
	}
	if res.WinnerExitRatio != 0.45 {
		t.Errorf("WinnerExitRatio passthrough: got %.4f, want 0.45", res.WinnerExitRatio)
	}
}

func TestScore_NonTradeable_ZeroScore(t *testing.T) {
	f := cleanWinner()
	f.ManipulationRiskScore = 1 // hard fail
	res := scoring.Score(f, testCfg())
	if res.OpportunityScore != 0 {
		t.Errorf("non-tradeable token must have OpportunityScore = 0, got %.2f", res.OpportunityScore)
	}
}
