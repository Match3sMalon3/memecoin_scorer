package outcomes

import "testing"

func TestOutcomeFromMFEHitWhenMinute12SpikesThenFades(t *testing.T) {
	outcome, multiple, maxPrice := OutcomeFromMFE(1.0, []float64{
		1.02, 1.08, 1.21, 1.03, 0.80,
	})
	if outcome != "hit" {
		t.Fatalf("outcome=%q multiple=%.2f max=%.2f, want hit", outcome, multiple, maxPrice)
	}
	if multiple != 1.21 {
		t.Fatalf("multiple=%.2f, want 1.21", multiple)
	}
	if maxPrice != 1.21 {
		t.Fatalf("maxPrice=%.2f, want 1.21", maxPrice)
	}
}

func TestOutcomeFromMFEMissWhenPeakBelowDuneThreshold(t *testing.T) {
	outcome, multiple, maxPrice := OutcomeFromMFE(1.0, []float64{
		1.03, 1.10, 0.95, 0.80,
	})
	if outcome != "miss" {
		t.Fatalf("outcome=%q multiple=%.2f max=%.2f, want miss", outcome, multiple, maxPrice)
	}
	if multiple != 1.10 {
		t.Fatalf("multiple=%.2f, want 1.10", multiple)
	}
	if maxPrice != 1.10 {
		t.Fatalf("maxPrice=%.2f, want 1.10", maxPrice)
	}
}
