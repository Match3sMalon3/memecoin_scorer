package catalyst

import "memecoin_scorer/internal/model"

func Detect(s model.LiveSnapshot) model.CatalystResult {
	result := model.CatalystResult{
		Status:     "unknown",
		Source:     "unknown",
		Reasons:    []string{"no catalyst evidence available"},
		Confidence: 0,
	}

	if s.TokenMode == model.TokenModeMigration {
		result.Status = "weak"
		result.Source = "metadata"
		result.Reasons = []string{"migration context can create a live attention pathway"}
		result.Confidence = 0.45
		return result
	}

	if s.TokenMode == model.TokenModeBonding &&
		s.IsPumpFun &&
		s.BondingCurveProgressPct > 0 &&
		(s.BuyersLast5m >= 5 || s.EffectiveBuyers5m >= 5) &&
		(s.BuySolLast1m > 0 || s.SolPerTrade5m > 0) {
		result.Status = "weak"
		result.Source = "metadata"
		result.Reasons = []string{"Pump.fun bonding context has strong organic flow"}
		result.Confidence = 0.5
		return result
	}

	if s.DeployerAddress != "" && s.OrganicWinnerCount >= 2 {
		result.Status = "weak"
		result.Source = "deployer_history"
		result.Reasons = []string{"deployer has prior observed organic winners"}
		result.Confidence = 0.45
		return result
	}

	return result
}
