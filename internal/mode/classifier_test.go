package mode

import (
	"testing"
	"time"

	"memecoin_scorer/internal/model"
)

func TestClassifyModes(t *testing.T) {
	now := time.Now()
	migration := now.Add(-5 * time.Minute)
	tests := []struct {
		name string
		row  model.LiveSnapshot
		want model.TokenMode
	}{
		{"observed recent unknown", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{FirstSeenAt: now.Add(-60 * time.Second), AgeSeconds: 60, LaunchConfidence: model.LaunchConfidenceUnknown}}, model.TokenModeUnknown},
		{"exact launch", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{LaunchConfidence: model.LaunchConfidenceExact, LaunchAgeSeconds: floatPtr(60)}}, model.TokenModeLaunch},
		{"inferred launch", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{LaunchConfidence: model.LaunchConfidenceInferred, LaunchAgeSeconds: floatPtr(60)}}, model.TokenModeLaunch},
		{"revival", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{FirstSeenAt: now.Add(-1800 * time.Second), AgeSeconds: 1800, BuyersLast5m: 5, BuySolLast1m: 0.1, LaunchConfidence: model.LaunchConfidenceUnknown}}, model.TokenModeRevival},
		{"unknown", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{FirstSeenAt: now.Add(-1800 * time.Second), AgeSeconds: 1800}}, model.TokenModeUnknown},
		{"bonding", model.LiveSnapshot{BondingCurveProgressPct: 45}, model.TokenModeBonding},
		{"migration", model.LiveSnapshot{TokenSnapshot: model.TokenSnapshot{MigrationEventAt: &migration}}, model.TokenModeMigration},
	}
	for _, tt := range tests {
		if got := Classify(tt.row); got != tt.want {
			t.Fatalf("%s: got %s want %s", tt.name, got, tt.want)
		}
	}
}

func TestUnknownLaunchConfidenceWithFreshDemandIsNotLaunch(t *testing.T) {
	got := Classify(model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			AgeSeconds:       60,
			BuyersLast5m:     5,
			BuySolLast1m:     0.1,
			LaunchConfidence: model.LaunchConfidenceUnknown,
		},
	})
	if got == model.TokenModeLaunch {
		t.Fatal("unknown launch confidence became launch mode")
	}
	if got != model.TokenModeRevival {
		t.Fatalf("got %s want revival for fresh demand with unknown launch confidence", got)
	}
}

func floatPtr(v float64) *float64 {
	return &v
}
