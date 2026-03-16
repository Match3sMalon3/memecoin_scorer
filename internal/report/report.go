package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"memecoin_scorer/internal/model"
)

// WriteCSV writes per-token backtest results to w as CSV.
func WriteCSV(w io.Writer, results []model.BacktestResult) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"token_mint",
		"predicted_tradeable",
		"predicted_clean_tradeable",
		"opportunity_score",
		"opportunity_component",
		"adversarial_component",
		"monetization_component",
		"sniper_intensity_ratio",
		"first_minute_share",
		"winner_exit_ratio",
		"actual_tradeable",
		"actual_clean_tradeable",
	}); err != nil {
		return err
	}

	for _, r := range results {
		row := []string{
			r.TokenMint,
			boolStr(r.Score.IsTradeable30m),
			boolStr(r.Score.IsCleanTradeable30m),
			fmtF(r.Score.OpportunityScore),
			fmtF(r.Score.OpportunityComponent),
			fmtF(r.Score.AdversarialComponent),
			fmtF(r.Score.MonetizationComponent),
			fmtF(r.Score.SniperIntensityRatio),
			fmtF(r.Score.FirstMinuteShare),
			fmtF(r.Score.WinnerExitRatio),
			boolStr(r.Features.IsTradeable30m),
			boolStr(r.Features.IsCleanTradeable30m),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteJSON writes the Summary as indented JSON to w.
func WriteJSON(w io.Writer, s model.Summary) error {
	payload := map[string]any{
		"total_tokens":          s.TotalTokens,
		"tradeable_count":       s.TradeableCount,
		"clean_tradeable_count": s.CleanTradeableCount,
		"base_rate":             round4(s.BaseRate),
		"precision_tradeable":   round4(s.PrecisionTradeable),
		"precision_clean":       round4(s.PrecisionCleanTradeable),
		"uplift_tradeable":      round4(s.UpliftTradeable),
		"uplift_clean":          round4(s.UpliftCleanTradeable),
		"median_return": map[string]any{
			"tradeable":     round4(s.MedianReturnTradeable),
			"non_tradeable": round4(s.MedianReturnNonTradeable),
			"clean":         round4(s.MedianReturnCleanTradeable),
		},
		"avg_first_minute_share": map[string]any{
			"tradeable":     round4(s.AvgFirstMinuteShareTradeable),
			"non_tradeable": round4(s.AvgFirstMinuteShareNonTradeable),
		},
		"avg_sniper_intensity": map[string]any{
			"tradeable":     round4(s.AvgSniperIntensityTradeable),
			"non_tradeable": round4(s.AvgSniperIntensityNonTradeable),
		},
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("writing JSON: %w", err)
	}
	return nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func fmtF(v float64) string {
	return strconv.FormatFloat(v, 'f', 4, 64)
}

func round4(v float64) float64 {
	// JSON encoder handles precision; keep as float for marshalling.
	return v
}
