package shadow

import (
	"time"

	"memecoin_scorer/internal/config"
	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/scoring"
)

func EvaluateShadowScore(s *model.TokenSnapshot, now time.Time) model.ShadowScoreResult {
	if now.IsZero() {
		now = time.Now()
	}
	tf, coverage := BuildShadowTokenFeatures(s, now)
	result := model.ShadowScoreResult{
		FeatureWindowComplete: coverage.FeatureWindowComplete,
		MissingFields:         coverage.MissingFields,
	}

	if !coverage.FeatureWindowComplete {
		result.Notes = []string{"shadow score blocked: validated 30m/35m outcome window is incomplete"}
		return result
	}
	if len(coverage.MissingFields) > 0 {
		result.Notes = []string{"shadow score blocked: required validated inputs are missing"}
		return result
	}

	cfg, err := loadScoringConfig()
	if err != nil {
		result.Notes = []string{"shadow score blocked: scoring config unavailable: " + err.Error()}
		return result
	}

	score := scoring.Score(tf, cfg)
	tradeable := score.IsTradeable30m
	clean := score.IsCleanTradeable30m
	opp := score.OpportunityScore

	result.EligibleForShadowScore = true
	result.ValidatedTradeable30m = &tradeable
	result.ValidatedClean30m = &clean
	result.OpportunityScore = &opp
	result.ComparedAt = now.Unix()
	result.Notes = []string{"shadow score complete: validated offline scorer evaluated mature live token"}
	return result
}

func loadScoringConfig() (config.Config, error) {
	var lastErr error
	for _, path := range []string{
		"config/scoring_config.yaml",
		"../config/scoring_config.yaml",
		"../../config/scoring_config.yaml",
	} {
		cfg, err := config.Load(path)
		if err == nil {
			return cfg, nil
		}
		lastErr = err
	}
	return config.Config{}, lastErr
}
