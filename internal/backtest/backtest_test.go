package backtest_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"memecoin_scorer/internal/backtest"
	"memecoin_scorer/internal/config"
)

// testCfg returns the frozen v9 scoring config — must not be altered.
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

// testdata returns the absolute path to the top-level testdata directory,
// regardless of which package the test runs from.
func testdata(name string) string {
	_, file, _, _ := runtime.Caller(0)
	// file = .../internal/backtest/backtest_test.go → go up two levels
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(root, "testdata", name)
}

func TestBacktest_CleanWinner(t *testing.T) {
	_, summary, err := backtest.Run(testdata("clean_winner.csv"), testCfg())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if summary.TotalTokens != 2 {
		t.Errorf("TotalTokens = %d, want 2", summary.TotalTokens)
	}
	if summary.TradeableCount != 2 {
		t.Errorf("TradeableCount = %d, want 2 (all pass gates)", summary.TradeableCount)
	}
	if summary.CleanTradeableCount != 2 {
		t.Errorf("CleanTradeableCount = %d, want 2 (all pass clean gates)", summary.CleanTradeableCount)
	}
	// BaseRate: both tokens labelled tradeable in CSV → 1.0
	if summary.BaseRate != 1.0 {
		t.Errorf("BaseRate = %.4f, want 1.0", summary.BaseRate)
	}
	// PrecisionTradeable: all predicted tradeable are actually tradeable → 1.0
	if summary.PrecisionTradeable != 1.0 {
		t.Errorf("PrecisionTradeable = %.4f, want 1.0", summary.PrecisionTradeable)
	}
	// MedianReturnTradeable should be positive (clean winners have return 15 and 22.5)
	if summary.MedianReturnTradeable <= 0 {
		t.Errorf("MedianReturnTradeable = %.4f, want > 0", summary.MedianReturnTradeable)
	}
	// AvgSniperIntensityTradeable should be between 0 and max threshold
	if summary.AvgSniperIntensityTradeable < 0 || summary.AvgSniperIntensityTradeable > 0.30 {
		t.Errorf("AvgSniperIntensityTradeable = %.4f out of expected range", summary.AvgSniperIntensityTradeable)
	}
}

func TestBacktest_ZeroExits(t *testing.T) {
	_, summary, err := backtest.Run(testdata("zero_exits.csv"), testCfg())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if summary.TotalTokens != 2 {
		t.Errorf("TotalTokens = %d, want 2", summary.TotalTokens)
	}
	// Zero exits fails min_wallets_that_exited gate → no tradeable tokens.
	if summary.TradeableCount != 0 {
		t.Errorf("TradeableCount = %d, want 0 (wallets_that_exited = 0 fails gate)", summary.TradeableCount)
	}
	if summary.CleanTradeableCount != 0 {
		t.Errorf("CleanTradeableCount = %d, want 0", summary.CleanTradeableCount)
	}
	// All tokens are non-tradeable → MedianReturnNonTradeable should be set.
	if summary.MedianReturnTradeable != 0 {
		t.Errorf("MedianReturnTradeable = %.4f, want 0 (no tradeable)", summary.MedianReturnTradeable)
	}
}

func TestBacktest_NegativeReturn(t *testing.T) {
	_, summary, err := backtest.Run(testdata("negative_return.csv"), testCfg())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if summary.TradeableCount != 0 {
		t.Errorf("TradeableCount = %d, want 0 (negative median_realized_return fails gate)", summary.TradeableCount)
	}
	if summary.CleanTradeableCount != 0 {
		t.Errorf("CleanTradeableCount = %d, want 0", summary.CleanTradeableCount)
	}
	// Non-tradeable returns should be negative.
	if summary.MedianReturnNonTradeable >= 0 {
		t.Errorf("MedianReturnNonTradeable = %.4f, want < 0 for negative-return fixture", summary.MedianReturnNonTradeable)
	}
}

func TestBacktest_HighMfeBadMonetization(t *testing.T) {
	_, summary, err := backtest.Run(testdata("high_mfe_bad_monetization.csv"), testCfg())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if summary.TotalTokens != 2 {
		t.Errorf("TotalTokens = %d, want 2", summary.TotalTokens)
	}
	// MFE > 1.20 and other base gates pass → tradeable.
	if summary.TradeableCount != 2 {
		t.Errorf("TradeableCount = %d, want 2 (MFE passes, other base gates pass)", summary.TradeableCount)
	}
	// wallets_gt_25pct = 0 and median_return < 10 → not clean.
	if summary.CleanTradeableCount != 0 {
		t.Errorf("CleanTradeableCount = %d, want 0 (winner_ratio=0, return<10)", summary.CleanTradeableCount)
	}
	// Uplift should be defined (BaseRate > 0 because actual labels are true/false in CSV).
	// PrecisionCleanTradeable should be 0 (no predicted clean tokens).
	if summary.PrecisionCleanTradeable != 0 {
		t.Errorf("PrecisionCleanTradeable = %.4f, want 0 (nothing predicted clean)", summary.PrecisionCleanTradeable)
	}
}

func TestBacktest_Malformed_ReturnsError(t *testing.T) {
	// malformed.csv contains "N/A" in a numeric field — must return a parse error,
	// not silently produce a corrupt Summary.
	_, _, err := backtest.Run(testdata("malformed.csv"), testCfg())
	if err == nil {
		t.Error("expected error for malformed CSV with non-numeric value, got nil")
	}
}

func TestBacktest_Summary_BaseRateAndUplift(t *testing.T) {
	// Clean winner CSV has BaseRate = 1.0 (both tokens labelled tradeable),
	// PrecisionTradeable = 1.0, so UpliftTradeable = 1.0 / 1.0 = 1.0.
	_, summary, err := backtest.Run(testdata("clean_winner.csv"), testCfg())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.UpliftTradeable != 1.0 {
		t.Errorf("UpliftTradeable = %.4f, want 1.0 (precision/base_rate = 1.0/1.0)", summary.UpliftTradeable)
	}
}
