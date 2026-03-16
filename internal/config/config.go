package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Thresholds holds hard-gate and filter values for the scorer.
type Thresholds struct {
	MinCohortBuyers           int     `yaml:"min_cohort_buyers"`
	MfeThreshold              float64 `yaml:"mfe_threshold"`
	MinSellTrades             int     `yaml:"min_sell_trades"`
	MinSellUniqueTraders      int     `yaml:"min_sell_unique_traders"`
	MaxManipulationRiskScore  int     `yaml:"max_manipulation_risk_score"`
	MaxFirstMinuteShare       float64 `yaml:"max_first_minute_share"`
	MaxSniperIntensityRatio   float64 `yaml:"max_sniper_intensity_ratio"`
	MinSizeDiversityRatio     float64 `yaml:"min_size_diversity_ratio"`
	MinWalletsThatExited      int     `yaml:"min_wallets_that_exited"`
	MinMedianRealizedReturn   float64 `yaml:"min_median_realized_return"`
	MinRealizedReturnForClean float64 `yaml:"min_realized_return_for_clean"`
	MinWinnerRatioForClean    float64 `yaml:"min_winner_ratio_for_clean"`
}

// Weights controls how the three scoring components are combined.
// All values are unvalidated priors — retune after 200 live signals.
type Weights struct {
	Opportunity  float64 `yaml:"opportunity"`  // unvalidated prior — retune after 200 live signals
	Adversarial  float64 `yaml:"adversarial"`  // unvalidated prior — retune after 200 live signals
	Monetization float64 `yaml:"monetization"` // unvalidated prior — retune after 200 live signals
}

// Config is the top-level configuration loaded from scoring_config.yaml.
type Config struct {
	Thresholds Thresholds `yaml:"thresholds"`
	Weights    Weights    `yaml:"weights"`
}

// Load reads Config from the given YAML file path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config: %w", err)
	}
	return cfg, nil
}
