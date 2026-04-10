package backtest

import (
	"sort"

	"memecoin_scorer/internal/config"
	"memecoin_scorer/internal/features"
	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/scoring"
)

// Run loads the CSV at csvPath, scores each token, and computes the full Summary.
func Run(csvPath string, cfg config.Config) ([]model.BacktestResult, model.Summary, error) {
	rows, err := features.ParseCSV(csvPath)
	if err != nil {
		return nil, model.Summary{}, err
	}

	results := make([]model.BacktestResult, 0, len(rows))
	for _, tf := range rows {
		results = append(results, model.BacktestResult{
			TokenMint: tf.TokenMint,
			Features:  tf,
			Score:     scoring.Score(tf, cfg),
		})
	}

	summary := computeSummary(results)
	return results, summary, nil
}

func computeSummary(results []model.BacktestResult) model.Summary {
	if len(results) == 0 {
		return model.Summary{}
	}

	s := model.Summary{TotalTokens: len(results)}

	// Partition results by predicted label.
	var (
		tradeableReturns    []float64
		nonTradeableReturns []float64
		cleanReturns        []float64

		tradeableFMS       float64
		nonTradeableFMS    float64
		tradeableSniper    float64
		nonTradeableSniper float64

		tradeableFMSCount    int
		nonTradeableFMSCount int
	)

	// Precision counters: predicted-positive that are actually positive.
	var (
		predictedTradeable    int
		truePositiveTradeable int
		predictedClean        int
		truePositiveClean     int
		actualTradeableCount  int
	)

	for _, r := range results {
		if r.Features.IsTradeable30m {
			actualTradeableCount++
		}

		if r.Score.IsTradeable30m {
			s.TradeableCount++
			predictedTradeable++
			if r.Features.IsTradeable30m {
				truePositiveTradeable++
			}
			tradeableReturns = append(tradeableReturns, r.Features.MedianRealizedReturnPct)
			tradeableFMS += r.Features.FirstMinuteShare
			tradeableSniper += r.Features.SniperIntensityRatio
			tradeableFMSCount++
		} else {
			nonTradeableReturns = append(nonTradeableReturns, r.Features.MedianRealizedReturnPct)
			nonTradeableFMS += r.Features.FirstMinuteShare
			nonTradeableSniper += r.Features.SniperIntensityRatio
			nonTradeableFMSCount++
		}

		if r.Score.IsCleanTradeable30m {
			s.CleanTradeableCount++
			predictedClean++
			if r.Features.IsCleanTradeable30m {
				truePositiveClean++
			}
			cleanReturns = append(cleanReturns, r.Features.MedianRealizedReturnPct)
		}
	}

	// Base rate: fraction of dataset tokens actually labelled tradeable.
	s.BaseRate = safeDivide(float64(actualTradeableCount), float64(s.TotalTokens))

	// Precision.
	s.PrecisionTradeable = safeDivide(float64(truePositiveTradeable), float64(predictedTradeable))
	s.PrecisionCleanTradeable = safeDivide(float64(truePositiveClean), float64(predictedClean))

	// Uplift vs base rate.
	s.UpliftTradeable = safeDivide(s.PrecisionTradeable, s.BaseRate)
	cleanBaseRate := safeDivide(float64(countActualClean(results)), float64(s.TotalTokens))
	s.UpliftCleanTradeable = safeDivide(s.PrecisionCleanTradeable, cleanBaseRate)

	// Median returns.
	s.MedianReturnTradeable = median(tradeableReturns)
	s.MedianReturnNonTradeable = median(nonTradeableReturns)
	s.MedianReturnCleanTradeable = median(cleanReturns)

	// Average adversarial feature values.
	s.AvgFirstMinuteShareTradeable = safeDivide(tradeableFMS, float64(tradeableFMSCount))
	s.AvgFirstMinuteShareNonTradeable = safeDivide(nonTradeableFMS, float64(nonTradeableFMSCount))
	s.AvgSniperIntensityTradeable = safeDivide(tradeableSniper, float64(tradeableFMSCount))
	s.AvgSniperIntensityNonTradeable = safeDivide(nonTradeableSniper, float64(nonTradeableFMSCount))

	return s
}

func countActualClean(results []model.BacktestResult) int {
	n := 0
	for _, r := range results {
		if r.Features.IsCleanTradeable30m {
			n++
		}
	}
	return n
}

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

func safeDivide(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
