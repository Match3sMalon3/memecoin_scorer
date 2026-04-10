package features_test

import (
	"math"
	"testing"

	"memecoin_scorer/internal/features"
)

func TestExecutionPenalty_FullScore_LargeLiquidity(t *testing.T) {
	// liquiditySOL >> tradeSOL * multiplier → penalty = 1.0
	got := features.ExecutionPenalty(1.0, 100.0, 20.0)
	if got != 1.0 {
		t.Errorf("got %.6f, want 1.0 (large liquidity should return full score)", got)
	}
}

func TestExecutionPenalty_FullScore_ExactBoundary(t *testing.T) {
	// liquiditySOL == tradeSOL * multiplier exactly → penalty = 1.0
	got := features.ExecutionPenalty(1.0, 20.0, 20.0)
	if got != 1.0 {
		t.Errorf("got %.6f, want 1.0 (boundary case)", got)
	}
}

func TestExecutionPenalty_PartialScore(t *testing.T) {
	// tradeSOL=1, liq=10, mult=20 → 10/(1*20) = 0.5
	got := features.ExecutionPenalty(1.0, 10.0, 20.0)
	want := 0.5
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %.6f, want %.6f", got, want)
	}
}

func TestExecutionPenalty_ZeroLiquidity(t *testing.T) {
	// No observed liquidity → cannot execute → 0.0
	got := features.ExecutionPenalty(1.0, 0.0, 20.0)
	if got != 0.0 {
		t.Errorf("got %.6f, want 0.0 (zero liquidity)", got)
	}
}

func TestExecutionPenalty_NegativeLiquidity(t *testing.T) {
	got := features.ExecutionPenalty(1.0, -5.0, 20.0)
	if got != 0.0 {
		t.Errorf("got %.6f, want 0.0 (negative treated as zero liquidity)", got)
	}
}

func TestExecutionPenalty_ZeroTradeSize(t *testing.T) {
	// No trade intended → no penalty
	got := features.ExecutionPenalty(0.0, 10.0, 20.0)
	if got != 1.0 {
		t.Errorf("got %.6f, want 1.0 (zero trade size)", got)
	}
}

func TestExecutionPenalty_ZeroMultiplier(t *testing.T) {
	// Degenerate multiplier → no penalty (avoids divide-by-zero)
	got := features.ExecutionPenalty(1.0, 10.0, 0.0)
	if got != 1.0 {
		t.Errorf("got %.6f, want 1.0 (zero multiplier)", got)
	}
}

func TestExecutionPenalty_Monotone_IncreasesWithLiquidity(t *testing.T) {
	// ExecutionPenalty must increase monotonically as liquiditySOL increases.
	trade := 1.0
	mult := 20.0
	prev := features.ExecutionPenalty(trade, 0.0, mult)
	for liq := 1.0; liq <= 50.0; liq += 1.0 {
		cur := features.ExecutionPenalty(trade, liq, mult)
		if cur < prev {
			t.Errorf("penalty decreased at liq=%.1f: prev=%.6f, cur=%.6f (not monotone)", liq, prev, cur)
		}
		prev = cur
	}
}

func TestExecutionPenalty_Monotone_DecreasesWithTradeSize(t *testing.T) {
	// ExecutionPenalty must decrease monotonically as tradeSOL increases (harder to execute large).
	liq := 20.0
	mult := 20.0
	prev := features.ExecutionPenalty(0.5, liq, mult)
	for trade := 1.0; trade <= 10.0; trade += 0.5 {
		cur := features.ExecutionPenalty(trade, liq, mult)
		if cur > prev {
			t.Errorf("penalty increased at trade=%.1f: prev=%.6f, cur=%.6f (not monotone)", trade, prev, cur)
		}
		prev = cur
	}
}

func TestExecutionPenalty_CappedAt1(t *testing.T) {
	// Even with enormous liquidity, penalty must never exceed 1.0.
	got := features.ExecutionPenalty(1.0, 1_000_000.0, 20.0)
	if got > 1.0 {
		t.Errorf("got %.6f, want <= 1.0 (must be capped)", got)
	}
}

func TestExecutionPenalty_LargerMultiplier_LowerScore(t *testing.T) {
	// Stricter multiplier (larger value) must produce equal or lower penalty.
	liq := 10.0
	trade := 1.0
	score10 := features.ExecutionPenalty(trade, liq, 10.0)
	score20 := features.ExecutionPenalty(trade, liq, 20.0)
	if score20 > score10 {
		t.Errorf("mult=20 score %.6f > mult=10 score %.6f (stricter should be equal or lower)", score20, score10)
	}
}
