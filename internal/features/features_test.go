package features_test

import (
	"strings"
	"testing"

	"memecoin_scorer/internal/features"
)

const csvHeader = "token_mint,launch_time,cohort_buyer_count,buyers_min0_1,buyers_min1_5," +
	"sniper_intensity_ratio,first_minute_share,size_diversity_ratio,manipulation_risk_score," +
	"mfe_multiple_15m,mfe_multiple_30m,median_realized_return_pct,wallets_that_exited," +
	"wallets_gt_25pct,buy_sol_0_35m,sell_sol_0_35m,is_tradeable_30m,is_clean_tradeable_30m\n"

func csvRow(fields ...string) string {
	return strings.Join(fields, ",") + "\n"
}

func TestWinnerExitRatio_Normal(t *testing.T) {
	// 8 wallets exited, 4 are gt25pct → ratio = 0.5
	csv := csvHeader + csvRow(
		"TOKENA", "2024-01-01T00:00:00Z", "20", "10", "10",
		"0.10", "0.10", "0.60", "0",
		"1.50", "2.00", "5.0",
		"8", "4", "50.0", "30.0", "false", "false",
	)
	rows, err := features.ParseReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	got := rows[0].WinnerExitRatio
	want := 4.0 / 8.0
	if got != want {
		t.Errorf("WinnerExitRatio = %.4f, want %.4f", got, want)
	}
}

func TestWinnerExitRatio_ZeroExits(t *testing.T) {
	// wallets_that_exited = 0 → WinnerExitRatio must be 0 (no division)
	csv := csvHeader + csvRow(
		"TOKENB", "2024-01-01T00:00:00Z", "20", "10", "10",
		"0.10", "0.10", "0.60", "0",
		"1.50", "2.00", "5.0",
		"0", "0", "50.0", "30.0", "false", "false",
	)
	rows, err := features.ParseReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	if rows[0].WinnerExitRatio != 0 {
		t.Errorf("WinnerExitRatio = %.4f with zero exits, want 0", rows[0].WinnerExitRatio)
	}
}

func TestBuyFlowPct_Normal(t *testing.T) {
	// buy=80, sell=20 → BuyFlowPct = 0.80
	csv := csvHeader + csvRow(
		"TOKENC", "2024-01-01T00:00:00Z", "20", "10", "10",
		"0.10", "0.10", "0.60", "0",
		"1.50", "2.00", "5.0",
		"10", "3", "80.0", "20.0", "false", "false",
	)
	rows, err := features.ParseReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	got := rows[0].BuyFlowPct
	want := 80.0 / (80.0 + 20.0)
	if got != want {
		t.Errorf("BuyFlowPct = %.4f, want %.4f", got, want)
	}
}

func TestBuyFlowPct_ZeroVolume(t *testing.T) {
	// buy=0, sell=0 → BuyFlowPct defaults to 0.5
	csv := csvHeader + csvRow(
		"TOKEND", "2024-01-01T00:00:00Z", "20", "10", "10",
		"0.10", "0.10", "0.60", "0",
		"1.50", "2.00", "5.0",
		"10", "3", "0.0", "0.0", "false", "false",
	)
	rows, err := features.ParseReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	if rows[0].BuyFlowPct != 0.5 {
		t.Errorf("BuyFlowPct = %.4f with zero volume, want 0.5", rows[0].BuyFlowPct)
	}
}

func TestSniperIntensityRatio_ReadFromCSV(t *testing.T) {
	csv := csvHeader + csvRow(
		"TOKENE", "2024-01-01T00:00:00Z", "20", "10", "10",
		"0.27", "0.10", "0.60", "0",
		"1.50", "2.00", "5.0",
		"10", "3", "50.0", "30.0", "false", "false",
	)
	rows, err := features.ParseReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	if rows[0].SniperIntensityRatio != 0.27 {
		t.Errorf("SniperIntensityRatio = %.4f, want 0.27", rows[0].SniperIntensityRatio)
	}
}

func TestFirstMinuteShare_ReadFromCSV(t *testing.T) {
	csv := csvHeader + csvRow(
		"TOKENF", "2024-01-01T00:00:00Z", "20", "10", "10",
		"0.10", "0.19", "0.60", "0",
		"1.50", "2.00", "5.0",
		"10", "3", "50.0", "30.0", "false", "false",
	)
	rows, err := features.ParseReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	if rows[0].FirstMinuteShare != 0.19 {
		t.Errorf("FirstMinuteShare = %.4f, want 0.19", rows[0].FirstMinuteShare)
	}
}

func TestEmptyNumericFields_DefaultToZero(t *testing.T) {
	// Empty numeric fields should parse as 0, not error.
	csv := csvHeader + "TOKENG,2024-01-01T00:00:00Z,,,,,,,,,,,,,,,,\n"
	rows, err := features.ParseReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("empty fields should not error, got: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].CohortBuyerCount != 0 {
		t.Errorf("CohortBuyerCount = %d, want 0 for empty field", rows[0].CohortBuyerCount)
	}
}

func TestMissingColumns_DefaultToZero(t *testing.T) {
	// Short rows (fewer columns than the header) should parse with 0 defaults.
	csv := csvHeader + "TOKENH,2024-01-01T00:00:00Z,10,5\n"
	rows, err := features.ParseReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("short row should not error, got: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].CohortBuyerCount != 10 {
		t.Errorf("CohortBuyerCount = %d, want 10", rows[0].CohortBuyerCount)
	}
	if rows[0].SniperIntensityRatio != 0 {
		t.Errorf("SniperIntensityRatio = %.4f, want 0 for missing column", rows[0].SniperIntensityRatio)
	}
}
