package features

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"memecoin_scorer/internal/model"
)

// ParseCSV reads the Dune CSV at path, parses each row into a TokenRow,
// and enriches it into a TokenFeatures with derived fields.
func ParseCSV(path string) ([]model.TokenFeatures, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening CSV: %w", err)
	}
	defer f.Close()
	return parseReader(f)
}

// ParseReader parses a Dune CSV from an io.Reader. Exported for testing.
func ParseReader(r io.Reader) ([]model.TokenFeatures, error) {
	return parseReader(r)
}

func parseReader(r io.Reader) ([]model.TokenFeatures, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading CSV header: %w", err)
	}

	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.TrimSpace(h)] = i
	}

	// Fail fast if any required column is absent.
	// is_tradeable_30m and is_clean_tradeable_30m are intentionally optional
	// so the scorer can run on unlabeled live exports.
	requiredCols := []string{
		"token_mint",
		"launch_time",
		"cohort_buyer_count",
		"buyers_min0_1",
		"buyers_min1_5",
		"sniper_intensity_ratio",
		"first_minute_share",
		"size_diversity_ratio",
		"manipulation_risk_score",
		"mfe_multiple_15m",
		"mfe_multiple_30m",
		"median_realized_return_pct",
		"wallets_that_exited",
		"wallets_gt_25pct",
		"buy_sol_0_35m",
		"sell_sol_0_35m",
		"sell_trade_count_5to35m",
		"sell_unique_traders_5to35m",
	}
	for _, col := range requiredCols {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("missing required CSV column %q", col)
		}
	}

	var out []model.TokenFeatures
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading CSV row: %w", err)
		}
		row, err := recordToRow(rec, idx)
		if err != nil {
			return nil, err
		}
		out = append(out, enrich(row))
	}
	return out, nil
}

// enrich computes derived fields from the raw row.
func enrich(row model.TokenRow) model.TokenFeatures {
	tf := model.TokenFeatures{TokenRow: row}

	if row.WalletsThatExited > 0 {
		tf.WinnerExitRatio = float64(row.WalletsGt25Pct) / float64(row.WalletsThatExited)
	}

	total := row.BuySol0_35m + row.SellSol0_35m
	if total > 0 {
		tf.BuyFlowPct = row.BuySol0_35m / total
	} else {
		tf.BuyFlowPct = 0.5
	}

	return tf
}

// recordToRow maps a CSV record to a TokenRow using the header index.
func recordToRow(rec []string, idx map[string]int) (model.TokenRow, error) {
	get := func(name string) string {
		i, ok := idx[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	parseInt := func(name string) (int, error) {
		s := get(name)
		if s == "" {
			return 0, nil
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("field %s: %w", name, err)
		}
		return v, nil
	}

	parseFloat := func(name string) (float64, error) {
		s := get(name)
		if s == "" {
			return 0, nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("field %s: %w", name, err)
		}
		return v, nil
	}

	parseBool := func(name string) bool {
		s := strings.ToLower(get(name))
		return s == "true" || s == "1" || s == "yes"
	}

	parseTime := func(name string) time.Time {
		s := get(name)
		if s == "" {
			return time.Time{}
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t
			}
		}
		return time.Time{}
	}

	cohortBuyers, err := parseInt("cohort_buyer_count")
	if err != nil {
		return model.TokenRow{}, err
	}
	buyersMin0_1, err := parseInt("buyers_min0_1")
	if err != nil {
		return model.TokenRow{}, err
	}
	buyersMin1_5, err := parseInt("buyers_min1_5")
	if err != nil {
		return model.TokenRow{}, err
	}
	manipRisk, err := parseInt("manipulation_risk_score")
	if err != nil {
		return model.TokenRow{}, err
	}
	walletsExited, err := parseInt("wallets_that_exited")
	if err != nil {
		return model.TokenRow{}, err
	}
	walletsGt25, err := parseInt("wallets_gt_25pct")
	if err != nil {
		return model.TokenRow{}, err
	}
	sniperRatio, err := parseFloat("sniper_intensity_ratio")
	if err != nil {
		return model.TokenRow{}, err
	}
	firstMin, err := parseFloat("first_minute_share")
	if err != nil {
		return model.TokenRow{}, err
	}
	sizeDiversity, err := parseFloat("size_diversity_ratio")
	if err != nil {
		return model.TokenRow{}, err
	}
	mfe15m, err := parseFloat("mfe_multiple_15m")
	if err != nil {
		return model.TokenRow{}, err
	}
	mfe30m, err := parseFloat("mfe_multiple_30m")
	if err != nil {
		return model.TokenRow{}, err
	}
	medianReturn, err := parseFloat("median_realized_return_pct")
	if err != nil {
		return model.TokenRow{}, err
	}
	buySol, err := parseFloat("buy_sol_0_35m")
	if err != nil {
		return model.TokenRow{}, err
	}
	sellSol, err := parseFloat("sell_sol_0_35m")
	if err != nil {
		return model.TokenRow{}, err
	}
	sellTradeCount, err := parseInt("sell_trade_count_5to35m")
	if err != nil {
		return model.TokenRow{}, err
	}
	sellUniqueTraders, err := parseInt("sell_unique_traders_5to35m")
	if err != nil {
		return model.TokenRow{}, err
	}

	return model.TokenRow{
		TokenMint:               get("token_mint"),
		LaunchTime:              parseTime("launch_time"),
		CohortBuyerCount:        cohortBuyers,
		BuyersMin0_1:            buyersMin0_1,
		BuyersMin1_5:            buyersMin1_5,
		SniperIntensityRatio:    sniperRatio,
		FirstMinuteShare:        firstMin,
		SizeDiversityRatio:      sizeDiversity,
		ManipulationRiskScore:   manipRisk,
		MfeMultiple15m:          mfe15m,
		MfeMultiple30m:          mfe30m,
		MedianRealizedReturnPct: medianReturn,
		WalletsThatExited:       walletsExited,
		WalletsGt25Pct:          walletsGt25,
		BuySol0_35m:             buySol,
		SellSol0_35m:            sellSol,
		SellTradeCount5to35m:    sellTradeCount,
		SellUniqueTraders5to35m: sellUniqueTraders,
		IsTradeable30m:          parseBool("is_tradeable_30m"),
		IsCleanTradeable30m:     parseBool("is_clean_tradeable_30m"),
	}, nil
}
