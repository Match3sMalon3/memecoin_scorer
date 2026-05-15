package setup

import (
	"strings"
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
		{"thin avoid", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{BuyersLast1m: 1, BuyersLast5m: 1, RealPoolDepthSOL: 2, HolderCount: 20, Top10HolderPct: 0.2, BuySolLast1m: 0.1}, LiquidityProxyReliable: true}, model.SetupAvoid, model.ActionNoTrade},
		{"watch", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{BuyersLast1m: 1, BuyersLast5m: 1, RealPoolDepthSOL: 10, HolderCount: 20, Top10HolderPct: 0.2, BuySolLast1m: 0.1}, LiquidityProxyReliable: true, EarlyProxy: model.EarlyProxyScore{Score: 50}, Authenticity: model.AuthenticityResult{Severity: "none"}}, model.SetupWatch, model.ActionWatch5M},
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

func TestHolderDistributionImmatureIsNotTerminal(t *testing.T) {
	row := setupRow(model.TokenModeLaunch, 70, "none")
	row.HolderCount = 1
	row.Top10HolderPct = 1.0

	got := Classify(row)
	if !containsBlocker(got.Blockers, "distribution immature") {
		t.Fatalf("got blockers %v want distribution immature", got.Blockers)
	}
	if containsBlocker(got.Blockers, "terminal holder concentration") {
		t.Fatalf("immature distribution reported as terminal: %v", got.Blockers)
	}
	if isWOW(got.Mode) {
		t.Fatalf("immature distribution produced WOW: %+v", got)
	}
}

func TestTerminalHolderConcentration(t *testing.T) {
	row := setupRow(model.TokenModeLaunch, 70, "none")
	row.HolderCount = 20
	row.Top10HolderPct = 0.96

	got := Classify(row)
	if !containsBlocker(got.Blockers, "terminal holder concentration") {
		t.Fatalf("got blockers %v want terminal concentration", got.Blockers)
	}
	if got.BlockerSeverity != "dead" || got.Mode != model.SetupDead {
		t.Fatalf("got mode=%s severity=%s want DEAD/dead: %+v", got.Mode, got.BlockerSeverity, got)
	}
}

func TestNearTerminalHolderConcentration(t *testing.T) {
	row := setupRow(model.TokenModeLaunch, 70, "none")
	row.HolderCount = 20
	row.Top10HolderPct = 0.92

	got := Classify(row)
	if !containsBlocker(got.Blockers, "near-terminal holder concentration") {
		t.Fatalf("got blockers %v want near-terminal concentration", got.Blockers)
	}
	if got.BlockerSeverity != "avoid" || isWOW(got.Mode) {
		t.Fatalf("got mode=%s severity=%s want non-WOW/avoid: %+v", got.Mode, got.BlockerSeverity, got)
	}
}

func TestHighHolderConcentration(t *testing.T) {
	row := setupRow(model.TokenModeLaunch, 70, "none")
	row.HolderCount = 20
	row.Top10HolderPct = 0.87

	got := Classify(row)
	if !containsBlocker(got.Blockers, "high holder concentration") {
		t.Fatalf("got blockers %v want high concentration", got.Blockers)
	}
	if got.BlockerSeverity != "watch" || isWOW(got.Mode) {
		t.Fatalf("got mode=%s severity=%s want non-WOW/watch: %+v", got.Mode, got.BlockerSeverity, got)
	}
}

func TestHighScoreRevivalPreciseBlockers(t *testing.T) {
	row := setupRow(model.TokenModeRevival, 83, "none")
	row.BuyersLast5m = 9
	row.EffectiveBuyers5m = 9
	row.SolPerTrade5m = 1.67
	row.SolPerUniqueBuyer5m = 1.67
	row.Top10HolderPct = 0.949
	row.ClusteringRowStatus = "partial_fallback"

	got := Classify(row)
	if isWOW(got.Mode) {
		t.Fatalf("high-score blocked revival produced WOW: %+v", got)
	}
	if !containsBlocker(got.Blockers, "near-terminal holder concentration") {
		t.Fatalf("got blockers %v want near-terminal concentration", got.Blockers)
	}
	if !containsBlocker(got.Blockers, "partial clustering fallback") {
		t.Fatalf("got blockers %v want partial clustering fallback", got.Blockers)
	}
	assertNoGenericBlockers(t, got)
}

func TestSetupBlockersNeverUseGenericPhrases(t *testing.T) {
	rows := []model.LiveSnapshot{
		model.LiveSnapshot{},
		setupRow(model.TokenModeUnknown, 30, "none"),
		setupRow(model.TokenModeRevival, 83, "none"),
	}
	for _, row := range rows {
		assertNoGenericBlockers(t, Classify(row))
	}
}

func TestReviewCandidateHighScoreRevivalSoftBlockers(t *testing.T) {
	row := reviewCandidateRow()

	got := Classify(row)
	if got.Mode != model.SetupReviewCandidate {
		t.Fatalf("mode=%s want REVIEW_CANDIDATE: %+v", got.Mode, got)
	}
	if got.Action != model.ActionWatch1M && got.Action != model.ActionPaperLog {
		t.Fatalf("action=%s want WATCH_1M or PAPER_LOG", got.Action)
	}
	if !got.Reviewable || got.ReviewReason == "" {
		t.Fatalf("review fields missing: %+v", got)
	}
}

func TestReviewCandidateRejectsFullFallback(t *testing.T) {
	row := reviewCandidateRow()
	row.ClusteringRowStatus = "full_fallback"
	if got := Classify(row); got.Mode == model.SetupReviewCandidate {
		t.Fatalf("full fallback became review candidate: %+v", got)
	}
}

func TestReviewCandidateRejectsTerminalTop10(t *testing.T) {
	row := reviewCandidateRow()
	row.Top10HolderPct = 0.95
	if got := Classify(row); got.Mode == model.SetupReviewCandidate {
		t.Fatalf("terminal top10 became review candidate: %+v", got)
	}
}

func TestReviewCandidateRejectsHighImpact(t *testing.T) {
	row := reviewCandidateRow()
	row.EstimatedImpactPct = 50
	if got := Classify(row); got.Mode == model.SetupReviewCandidate {
		t.Fatalf("high impact became review candidate: %+v", got)
	}
}

func TestReviewCandidateRejectsMediumAuthenticity(t *testing.T) {
	row := reviewCandidateRow()
	row.Authenticity.Severity = "medium"
	if got := Classify(row); got.Mode == model.SetupReviewCandidate {
		t.Fatalf("medium authenticity became review candidate: %+v", got)
	}
}

func setupRow(tokenMode model.TokenMode, score float64, severity string) model.LiveSnapshot {
	row := model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			BuyersLast1m:     5,
			BuyersLast5m:     6,
			RealPoolDepthSOL: 10,
			HolderCount:      20,
			Top10HolderPct:   0.2,
			BuySolLast1m:     0.5,
			LaunchConfidence: model.LaunchConfidenceExact,
		},
		LiquidityProxyReliable: true,
		TokenMode:              tokenMode,
		EarlyProxy:             model.EarlyProxyScore{Score: score},
		Authenticity:           model.AuthenticityResult{Severity: severity, Score: 100},
		SolPerTrade5m:          0.2,
		SolPerUniqueBuyer5m:    0.2,
		ClusteringRowStatus:    "resolved",
	}
	return row
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

func reviewCandidateRow() model.LiveSnapshot {
	row := setupRow(model.TokenModeRevival, 88, "none")
	row.Authenticity.Score = 100
	row.LaunchConfidence = model.LaunchConfidenceUnknown
	row.ClusteringRowStatus = "partial_fallback"
	row.RealPoolDepthSOL = 10
	row.LiquidityEvidenceSource = "raydium_wsol_vault"
	row.EstimatedImpactPct = 10
	row.BuyersLast1m = 1
	row.BuyersLast5m = 6
	row.EffectiveBuyers5m = 6
	row.SolPerTrade5m = 0.114
	return row
}

func floatPtr(v float64) *float64 {
	return &v
}

func containsBlocker(blockers []string, want string) bool {
	for _, blocker := range blockers {
		if strings.Contains(blocker, want) {
			return true
		}
	}
	return false
}

func assertNoGenericBlockers(t *testing.T, result model.SetupResult) {
	t.Helper()
	for _, blocker := range result.Blockers {
		if blocker == "setup requirements not met" || blocker == "below threshold and no momentum" {
			t.Fatalf("generic blocker leaked in %+v", result)
		}
	}
}
