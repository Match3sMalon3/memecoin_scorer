package shadow

import (
	"time"

	"memecoin_scorer/internal/model"
)

const shadowOutcomeWindow = 35 * time.Minute

func BuildShadowTokenFeatures(s *model.TokenSnapshot, now time.Time) (model.TokenFeatures, model.ShadowFeatureCoverage) {
	coverage := model.ShadowFeatureCoverage{}
	if s == nil {
		coverage.MissingFields = append(requiredShadowFields(), "token_snapshot")
		return model.TokenFeatures{}, coverage
	}

	if now.IsZero() {
		now = time.Now()
	}
	coverage.FeatureWindowComplete = shadowFeatureWindowComplete(s, now)

	obs := s.ShadowFeatures
	row := model.TokenRow{
		TokenMint:  s.Mint,
		LaunchTime: s.FirstSeenAt,
	}
	tf := model.TokenFeatures{TokenRow: row}
	missing := make([]string, 0, len(requiredShadowFields()))

	if obs.HasCohortBuyerCount {
		tf.CohortBuyerCount = obs.CohortBuyerCount
	} else {
		missing = append(missing, "cohort_buyer_count")
	}
	if obs.HasMfeMultiple30m {
		tf.MfeMultiple30m = obs.MfeMultiple30m
	} else {
		missing = append(missing, "mfe_multiple_30m")
	}
	if obs.HasBuySol0_35m {
		tf.BuySol0_35m = obs.BuySol0_35m
	} else {
		missing = append(missing, "buy_sol_0_35m")
	}
	if obs.HasSellSol0_35m {
		tf.SellSol0_35m = obs.SellSol0_35m
	} else {
		missing = append(missing, "sell_sol_0_35m")
	}
	if obs.HasSellTradeCount5to35m {
		tf.SellTradeCount5to35m = obs.SellTradeCount5to35m
	} else {
		missing = append(missing, "sell_trade_count_5to35m")
	}
	if obs.HasSellUniqueTraders5to35m {
		tf.SellUniqueTraders5to35m = obs.SellUniqueTraders5to35m
	} else {
		missing = append(missing, "sell_unique_traders_5to35m")
	}
	if obs.HasManipulationRiskScore {
		tf.ManipulationRiskScore = obs.ManipulationRiskScore
	} else {
		missing = append(missing, "manipulation_risk_score")
	}
	if obs.HasFirstMinuteShare {
		tf.FirstMinuteShare = obs.FirstMinuteShare
	} else {
		missing = append(missing, "first_minute_share")
	}
	if obs.HasSniperIntensityRatio {
		tf.SniperIntensityRatio = obs.SniperIntensityRatio
	} else {
		missing = append(missing, "sniper_intensity_ratio")
	}
	if obs.HasSizeDiversityRatio {
		tf.SizeDiversityRatio = obs.SizeDiversityRatio
	} else {
		missing = append(missing, "size_diversity_ratio")
	}
	if obs.HasWalletsThatExited {
		tf.WalletsThatExited = obs.WalletsThatExited
	} else {
		missing = append(missing, "wallets_that_exited")
	}
	if obs.HasMedianRealizedReturnPct {
		tf.MedianRealizedReturnPct = obs.MedianRealizedReturnPct
	} else {
		missing = append(missing, "median_realized_return")
	}
	if obs.HasWalletsGt25Pct {
		tf.WalletsGt25Pct = obs.WalletsGt25Pct
	} else {
		missing = append(missing, "wallets_gt_25pct")
	}
	if obs.HasWinnerExitRatio {
		tf.WinnerExitRatio = obs.WinnerExitRatio
	} else if obs.HasWalletsThatExited && obs.HasWalletsGt25Pct && obs.WalletsThatExited > 0 {
		tf.WinnerExitRatio = float64(obs.WalletsGt25Pct) / float64(obs.WalletsThatExited)
	} else {
		missing = append(missing, "winner_exit_ratio")
	}

	total := tf.BuySol0_35m + tf.SellSol0_35m
	if total > 0 {
		tf.BuyFlowPct = tf.BuySol0_35m / total
	} else {
		tf.BuyFlowPct = 0.5
	}

	coverage.MissingFields = missing
	return tf, coverage
}

func shadowFeatureWindowComplete(s *model.TokenSnapshot, now time.Time) bool {
	if !s.FirstSeenAt.IsZero() {
		return !now.Before(s.FirstSeenAt.Add(shadowOutcomeWindow))
	}
	return s.AgeSeconds >= shadowOutcomeWindow.Seconds()
}

func requiredShadowFields() []string {
	return []string{
		"cohort_buyer_count",
		"mfe_multiple_30m",
		"buy_sol_0_35m",
		"sell_sol_0_35m",
		"sell_trade_count_5to35m",
		"sell_unique_traders_5to35m",
		"manipulation_risk_score",
		"first_minute_share",
		"sniper_intensity_ratio",
		"size_diversity_ratio",
		"wallets_that_exited",
		"median_realized_return",
		"wallets_gt_25pct",
		"winner_exit_ratio",
	}
}
