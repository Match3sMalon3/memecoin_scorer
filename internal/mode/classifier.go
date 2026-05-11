package mode

import (
	"time"

	"memecoin_scorer/internal/model"
)

func Classify(s model.LiveSnapshot) model.TokenMode {
	if s.MigrationEventAt != nil {
		delta := time.Since(*s.MigrationEventAt)
		if delta < 0 {
			delta = -delta
		}
		if delta <= 10*time.Minute {
			return model.TokenModeMigration
		}
	}

	if s.BondingCurveProgressPct > 0 && s.BondingCurveProgressPct < 100 {
		return model.TokenModeBonding
	}

	if hasLaunchConfidence(s) && s.LaunchAgeSeconds != nil && *s.LaunchAgeSeconds < 900 {
		return model.TokenModeLaunch
	}

	if hasFreshDemand(s) {
		return model.TokenModeRevival
	}

	return model.TokenModeUnknown
}

func hasLaunchConfidence(s model.LiveSnapshot) bool {
	return s.LaunchConfidence == model.LaunchConfidenceExact ||
		s.LaunchConfidence == model.LaunchConfidenceInferred
}

func hasFreshDemand(s model.LiveSnapshot) bool {
	totalBuySOL5m := s.SolPerUniqueBuyer5m * float64(max(s.BuyersLast5m, s.EffectiveBuyers5m))
	return (s.BuyersLast5m >= 5 || s.EffectiveBuyers5m >= 5) &&
		(s.BuySolLast1m > 0 || totalBuySOL5m > 0)
}
