package setup

import (
	"testing"

	"memecoin_scorer/internal/model"
)

func TestClassifySetupPhase1Actions(t *testing.T) {
	tests := []struct {
		name string
		row  model.LiveSnapshot
		mode model.SetupMode
		act  model.OperatorAction
	}{
		{"launch wow", launchSetupRow(model.LaunchConfidenceExact), model.SetupLaunchWOW, model.ActionPaperLog},
		{"manipulated", manipulatedRow(), model.SetupManipulatedMomentum, model.ActionExitAvoid},
		{"revival wow", setupRow(model.TokenModeRevival, 60, "none"), model.SetupRevivalWOW, model.ActionPaperLog},
		{"no flow dead", model.LiveSnapshot{}, model.SetupDead, model.ActionNoTrade},
		{"thin avoid", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{BuyersLast1m: 1, BuyersLast5m: 1, RealPoolDepthSOL: 2}, LiquidityProxyReliable: true}, model.SetupAvoid, model.ActionNoTrade},
		{"watch", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{BuyersLast1m: 1, BuyersLast5m: 1, RealPoolDepthSOL: 10}, LiquidityProxyReliable: true, EarlyProxy: model.EarlyProxyScore{Score: 50}, Authenticity: model.AuthenticityResult{Severity: "none"}}, model.SetupWatch, model.ActionWatch5M},
	}
	for _, tt := range tests {
		got := Classify(tt.row)
		if got.Mode != tt.mode || got.Action != tt.act {
			t.Fatalf("%s: got %+v want mode=%s action=%s", tt.name, got, tt.mode, tt.act)
		}
		if got.Action == model.ActionEnterSmall || got.Action == model.ActionEnterAllowed {
			t.Fatalf("%s produced forbidden Phase 1 action %s", tt.name, got.Action)
		}
	}
}

func TestLaunchPartialFallbackNotWOW(t *testing.T) {
	row := launchSetupRow(model.LaunchConfidenceExact)
	row.ClusteringRowStatus = "partial_fallback"
	if got := Classify(row); got.Mode == model.SetupLaunchWOW {
		t.Fatalf("partial fallback classified as launch wow: %+v", got)
	}
}

func TestLaunchWOWRequiresLaunchConfidence(t *testing.T) {
	row := launchSetupRow(model.LaunchConfidenceUnknown)
	row.LaunchAgeSeconds = nil
	if got := Classify(row); got.Mode == model.SetupLaunchWOW {
		t.Fatalf("unknown launch confidence classified as launch wow: %+v", got)
	}
}

func TestLaunchWOWAllowsInferredLaunchConfidence(t *testing.T) {
	row := launchSetupRow(model.LaunchConfidenceInferred)
	if got := Classify(row); got.Mode != model.SetupLaunchWOW {
		t.Fatalf("got %+v want launch wow for inferred launch confidence", got)
	}
}

func TestOldObservedTokenCannotBecomeLaunchWOW(t *testing.T) {
	row := setupRow(model.TokenModeRevival, 70, "none")
	row.ObservedAgeSeconds = 60
	row.LaunchConfidence = model.LaunchConfidenceUnknown
	if got := Classify(row); got.Mode == model.SetupLaunchWOW {
		t.Fatalf("old/unknown launch token classified as launch wow: %+v", got)
	}
}

func setupRow(tokenMode model.TokenMode, score float64, severity string) model.LiveSnapshot {
	return model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:     5,
			BuyersLast5m:     6,
			RealPoolDepthSOL: 10,
		},
		LiquidityProxyReliable: true,
		TokenMode:              tokenMode,
		EarlyProxy:             model.EarlyProxyScore{Score: score},
		Authenticity:           model.AuthenticityResult{Severity: severity, Score: 100},
		SolPerTrade5m:          0.2,
		SolPerUniqueBuyer5m:    0.2,
		ClusteringRowStatus:    "resolved",
	}
}

func launchSetupRow(confidence model.LaunchConfidence) model.LiveSnapshot {
	row := setupRow(model.TokenModeLaunch, 70, "none")
	row.LaunchConfidence = confidence
	row.LaunchAgeSeconds = floatPtr(60)
	return row
}

func manipulatedRow() model.LiveSnapshot {
	row := setupRow(model.TokenModeLaunch, 70, "high")
	row.Authenticity.Flags = []string{"bundle_bot"}
	row.SolPerTrade5m = 1.0
	return row
}

func floatPtr(v float64) *float64 {
	return &v
}
