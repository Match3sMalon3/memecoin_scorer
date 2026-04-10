package engine_test

import (
	"testing"

	"memecoin_scorer/internal/engine"
	"memecoin_scorer/internal/model"
)

// defaultSnap returns a snapshot with all 7-gate fields populated to pass all gates.
// LiquidityPoolSOL=600 → Gate 7: 25/600*100=4.2% ≤ 5% ✓
// MarketCapSOL=1000    → Gate 1: 600/1000*100=60% ≥ 5% ✓
// Volume24hSOL=500     → Gate 4: 500/1000=0.5 in [0.01,1.0] ✓
// Top10HolderPct=0.10  → Gate 2: 10% ≤ 15% ✓
// OrganicWinnerCount=12→ Gate 5: 12 ≥ 10 ✓
// HoldersAt30m=50, HoldersAt60m=80 → Gate 6: 80>50 ✓
// clusterRatio=0.02    → Gate 3: 0.02 ≤ 0.05 ✓
func defaultSnap() model.TokenSnapshot {
	return model.TokenSnapshot{
		Mint:               "TESTMINT",
		AgeSeconds:         3700, // > 60m so Gate 6 uses holder snapshots
		TotalBuySOL:        400.0,
		TotalSellSOL:       200.0,
		LiquidityPoolSOL:   600.0,
		MarketCapSOL:       1000.0,
		Volume24hSOL:       500.0,
		Top10HolderPct:     0.10,
		OrganicWinnerCount: 12,
		HoldersAt30m:       50,
		HoldersAt60m:       80,
	}
}

func defaultCfg() engine.EngineConfig {
	return engine.DefaultEngineConfig()
}

// ============================================================
// Layer 0 tests
// ============================================================

func TestLayer0_SelfBundled_Rejects(t *testing.T) {
	snap := defaultSnap()
	dec := engine.EvaluateGates(snap, 0.85, defaultCfg()) // cluster=85% > 80%
	if !dec.Layer0Reject {
		t.Error("expected Layer0Reject=true for cluster_ratio=0.85")
	}
	if dec.MaxLabel != "AVOID" {
		t.Errorf("MaxLabel=%q, want AVOID", dec.MaxLabel)
	}
}

// Layer 0 rejects must return Gates as a non-nil empty slice so JSON encodes
// as "gates":[] not "gates":null, which is ambiguous to operators.
func TestLayer0_Reject_GatesIsEmptySliceNotNil(t *testing.T) {
	snap := defaultSnap()
	dec := engine.EvaluateGates(snap, 0.85, defaultCfg())
	if dec.Gates == nil {
		t.Error("Gates must be non-nil empty slice on Layer 0 reject (JSON must encode as [] not null)")
	}
	if len(dec.Gates) != 0 {
		t.Errorf("Gates length = %d, want 0 on Layer 0 reject", len(dec.Gates))
	}
}

func TestLayer0_ImpossibleExecution_GatesIsEmptySliceNotNil(t *testing.T) {
	snap := defaultSnap()
	snap.LiquidityPoolSOL = 2.0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	if dec.Gates == nil {
		t.Error("Gates must be non-nil empty slice on Layer 0 reject")
	}
}

func TestLayer0_ImpossibleExecution_Rejects(t *testing.T) {
	snap := defaultSnap()
	snap.LiquidityPoolSOL = 2.0 // below MinExecLiqSOL=5.0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	if !dec.Layer0Reject {
		t.Error("expected Layer0Reject=true for liquidity=2 SOL")
	}
}

func TestLayer0_NormalToken_Passes(t *testing.T) {
	snap := defaultSnap()
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg())
	if dec.Layer0Reject {
		t.Errorf("unexpected Layer0Reject: %s", dec.Layer0Reason)
	}
}

// ============================================================
// Gate 1: Liquidity / MC ratio
// ============================================================

func TestGate1_Passes_WhenRatioAboveThreshold(t *testing.T) {
	snap := defaultSnap() // 600/1000*100 = 60% >= 5%
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[0]
	if !g.Passed {
		t.Errorf("Gate 1 should pass: %s", g.Reason)
	}
}

func TestGate1_Fails_BetweenFloorAndThreshold(t *testing.T) {
	snap := defaultSnap()
	// liq/MC = 4% (between 3% floor and 5% threshold) → Gate 1 fails but not hard-AVOID.
	// Must keep LiquidityPoolSOL large enough for Gate 7: slippage = 25/liq*100 ≤ 5%
	// → liq ≥ 500 SOL. Use MC=10000, liq=400 → liq/MC=4%, slippage=25/400=6.25% > 5%
	// That would trigger Gate 7. Instead use liq=600, MC=20000 → 3%, slippage=4.2%.
	// Wait: 600/20000*100 = 3% < 3% floor → hard AVOID.
	// Use liq=600, MC=12000 → 5%, slippage=4.2% — that passes Gate 1.
	// Need liq/MC between 3% and 5% AND slippage ≤ 5%:
	//   slippage = 25/liq ≤ 0.05 → liq ≥ 500
	//   liq/MC between 3% and 5% → MC between liq/0.05 and liq/0.03
	//   With liq=600: MC between 12000 and 20000.
	//   Use MC=14000 → 600/14000*100 = 4.3%, slippage=25/600=4.2% ≤ 5% ✓
	snap.LiquidityPoolSOL = 600.0
	snap.MarketCapSOL = 14000.0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[0]
	if g.Passed {
		t.Errorf("Gate 1 should fail when liq/MC = 4.3%%: %s", g.Reason)
	}
	// MaxLabel should be READY (1 hard failure from Gate 1, not hard-AVOID)
	if dec.MaxLabel == "AVOID" {
		t.Errorf("MaxLabel=%q — should not be AVOID for liq/MC between 3%% and 5%%", dec.MaxLabel)
	}
}

func TestGate1_HardAvoid_BelowFloor(t *testing.T) {
	snap := defaultSnap()
	snap.LiquidityPoolSOL = 20.0 // 20/1000*100 = 2% < 3% → hard AVOID
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	if dec.MaxLabel != "AVOID" {
		t.Errorf("MaxLabel=%q, want AVOID when liq/MC < 3%%", dec.MaxLabel)
	}
}

func TestGate1_Skips_WhenMarketCapZero(t *testing.T) {
	snap := defaultSnap()
	snap.MarketCapSOL = 0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[0]
	if !g.Skipped {
		t.Error("Gate 1 should be skipped when MarketCapSOL=0")
	}
}

// ============================================================
// Gate 2: Supply concentration
// ============================================================

func TestGate2_Passes_WhenTop10BelowThreshold(t *testing.T) {
	snap := defaultSnap() // Top10=10% ≤ 15%, age=3700s < 6h
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[1]
	if !g.Passed {
		t.Errorf("Gate 2 should pass: %s", g.Reason)
	}
}

func TestGate2_Fails_WhenTop10ExceedsThreshold(t *testing.T) {
	snap := defaultSnap()
	snap.Top10HolderPct = 0.20 // 20% > 15%
	snap.AgeSeconds = 3700     // < 6h
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[1]
	if g.Passed || g.Skipped {
		t.Errorf("Gate 2 should fail for top10=20%% (token < 6h): %s", g.Reason)
	}
}

func TestGate2_Passes_WhenTokenOlderThan6h(t *testing.T) {
	snap := defaultSnap()
	snap.Top10HolderPct = 0.50 // would fail if < 6h
	snap.AgeSeconds = 7 * 3600 // > 6h → gate doesn't apply
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[1]
	if !g.Passed {
		t.Errorf("Gate 2 should pass for tokens > 6h old: %s", g.Reason)
	}
}

func TestGate2_Skips_WhenHolderDataZero(t *testing.T) {
	snap := defaultSnap()
	snap.Top10HolderPct = 0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[1]
	if !g.Skipped {
		t.Error("Gate 2 should skip when Top10HolderPct=0 (data unavailable)")
	}
}

// ============================================================
// Gate 3: Bundle / shared funder
// ============================================================

func TestGate3_Passes_WhenClusterRatioLow(t *testing.T) {
	snap := defaultSnap()
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg()) // 2% ≤ 5%
	g := dec.Gates[2]
	if !g.Passed {
		t.Errorf("Gate 3 should pass: %s", g.Reason)
	}
}

func TestGate3_Fails_WhenClusterRatioHigh(t *testing.T) {
	snap := defaultSnap()
	dec := engine.EvaluateGates(snap, 0.10, defaultCfg()) // 10% > 5%
	g := dec.Gates[2]
	if g.Passed {
		t.Error("Gate 3 should fail for cluster_ratio=10%")
	}
}

func TestGate3_AlwaysEvaluated_NeverSkipped(t *testing.T) {
	snap := model.TokenSnapshot{} // zero snap
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[2]
	if g.Skipped {
		t.Error("Gate 3 should never be skipped (clusterRatio is always known)")
	}
}

// ============================================================
// Gate 4: Volume / MC ratio
// ============================================================

func TestGate4_Passes_WhenRatioInRange(t *testing.T) {
	snap := defaultSnap() // 500/1000 = 0.5 in [0.01, 1.0]
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[3]
	if !g.Passed {
		t.Errorf("Gate 4 should pass: %s", g.Reason)
	}
}

func TestGate4_Fails_WhenRatioTooLow(t *testing.T) {
	snap := defaultSnap()
	snap.Volume24hSOL = 5.0 // 5/1000 = 0.005 < 0.01
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[3]
	if g.Passed || g.Skipped {
		t.Error("Gate 4 should fail when vol/MC < 0.01")
	}
}

func TestGate4_Fails_WhenRatioTooHigh(t *testing.T) {
	snap := defaultSnap()
	snap.Volume24hSOL = 2000.0 // 2000/1000 = 2.0 > 1.0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[3]
	if g.Passed || g.Skipped {
		t.Error("Gate 4 should fail when vol/MC > 1.0 (suspicious wash volume)")
	}
}

func TestGate4_Skips_WhenMarketCapZero(t *testing.T) {
	snap := defaultSnap()
	snap.MarketCapSOL = 0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[3]
	if !g.Skipped {
		t.Error("Gate 4 should skip when MarketCapSOL=0")
	}
}

// ============================================================
// Gate 5: Organic winners
// ============================================================

func TestGate5_Passes_WhenEnoughOrganicWinners(t *testing.T) {
	snap := defaultSnap() // OrganicWinnerCount=12 ≥ 10
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[4]
	if !g.Passed {
		t.Errorf("Gate 5 should pass: %s", g.Reason)
	}
}

func TestGate5_Fails_WhenTooFewOrganicWinners(t *testing.T) {
	snap := defaultSnap()
	snap.OrganicWinnerCount = 3 // < 10
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[4]
	if g.Passed || g.Skipped {
		t.Error("Gate 5 should fail when organic_winners < 10")
	}
}

func TestGate5_Skips_WhenTokenNot60mOld(t *testing.T) {
	snap := defaultSnap()
	snap.HoldersAt60m = 0 // not yet 60m old
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[4]
	if !g.Skipped {
		t.Error("Gate 5 should skip when HoldersAt60m=0 (token < 60m old)")
	}
}

// ============================================================
// Gate 6: Holder growth / stall test
// ============================================================

func TestGate6_Passes_WhenHolderCountGrows(t *testing.T) {
	snap := defaultSnap() // HoldersAt60m=80 > HoldersAt30m=50
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[5]
	if !g.Passed {
		t.Errorf("Gate 6 should pass: %s", g.Reason)
	}
}

func TestGate6_Fails_WhenHolderCountFlat(t *testing.T) {
	snap := defaultSnap()
	snap.HoldersAt60m = 50 // same as HoldersAt30m → stall
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[5]
	if g.Passed || g.Skipped {
		t.Error("Gate 6 should fail when 60m holder count equals 30m")
	}
}

func TestGate6_Fails_WhenHolderCountDeclines(t *testing.T) {
	snap := defaultSnap()
	snap.HoldersAt60m = 40 // less than 30m → decline
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[5]
	if g.Passed || g.Skipped {
		t.Error("Gate 6 should fail when holder count declines")
	}
}

func TestGate6_Skips_WhenSnapshotsNotCaptured(t *testing.T) {
	snap := defaultSnap()
	snap.HoldersAt30m = 0
	snap.HoldersAt60m = 0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[5]
	if !g.Skipped {
		t.Error("Gate 6 should skip when holder snapshots not captured")
	}
}

// ============================================================
// Gate 7: Slippage ceiling
// ============================================================

func TestGate7_Passes_WhenSlippageLow(t *testing.T) {
	snap := defaultSnap() // 25/600*100 = 4.2% ≤ 5%
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[6]
	if !g.Passed {
		t.Errorf("Gate 7 should pass: %s", g.Reason)
	}
	if dec.ScoreCap != 0 {
		t.Errorf("ScoreCap should be 0 when gate 7 passes, got %d", dec.ScoreCap)
	}
}

func TestGate7_Fails_WhenSlippageHigh_ForcesAVOID(t *testing.T) {
	snap := defaultSnap()
	snap.LiquidityPoolSOL = 100.0 // 25/100*100 = 25% > 5%
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[6]
	if g.Passed || g.Skipped {
		t.Error("Gate 7 should fail when slippage > 5%")
	}
	if dec.MaxLabel != "AVOID" {
		t.Errorf("MaxLabel=%q, want AVOID when gate 7 fails", dec.MaxLabel)
	}
	if dec.ScoreCap != 40 {
		t.Errorf("ScoreCap=%d, want 40 when gate 7 fails", dec.ScoreCap)
	}
}

func TestGate7_Skips_WhenNoLiquidity(t *testing.T) {
	snap := defaultSnap()
	snap.LiquidityPoolSOL = 0
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	g := dec.Gates[6]
	if !g.Skipped {
		t.Error("Gate 7 should skip when LiquidityPoolSOL=0")
	}
}

// ============================================================
// MaxLabel inference from gate failures
// ============================================================

func TestMaxLabel_AllPass_GivesBUY(t *testing.T) {
	snap := defaultSnap()
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg())
	if dec.MaxLabel != "BUY" {
		t.Errorf("MaxLabel=%q, want BUY when all gates pass", dec.MaxLabel)
	}
}

func TestMaxLabel_OneHardFail_GivesREADY(t *testing.T) {
	snap := defaultSnap()
	snap.OrganicWinnerCount = 3 // gate 5 fails; all others pass
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg())
	if dec.MaxLabel != "READY" {
		t.Errorf("MaxLabel=%q, want READY for 1 hard failure", dec.MaxLabel)
	}
}

func TestMaxLabel_TwoHardFails_GivesWATCH(t *testing.T) {
	snap := defaultSnap()
	snap.OrganicWinnerCount = 3 // gate 5 fails
	snap.Top10HolderPct = 0.30  // gate 2 fails (age=3700s < 6h, 30% > 15%)
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg())
	if dec.MaxLabel != "WATCH" {
		t.Errorf("MaxLabel=%q, want WATCH for 2 hard failures (gate5+gate2)", dec.MaxLabel)
	}
}

func TestMaxLabel_ThreeHardFails_GivesAVOID(t *testing.T) {
	// Use gate2 + gate3 + gate5 (all "normal" gates — no special hard rules).
	// Gate 2: top10=30% > 15%, age < 6h
	// Gate 3: clusterRatio=0.10 > 0.05
	// Gate 5: organicWinners=3 < 10
	// Gate 6 must still pass to avoid its special AVOID rule: keep HoldersAt60m > HoldersAt30m.
	snap := defaultSnap()
	snap.OrganicWinnerCount = 3 // gate 5 fails
	snap.Top10HolderPct = 0.30  // gate 2 fails (age < 6h, 30% > 15%)
	// clusterRatio=0.10 makes gate 3 fail (10% > 5%)
	dec := engine.EvaluateGates(snap, 0.10, defaultCfg())
	if dec.MaxLabel != "AVOID" {
		t.Errorf("MaxLabel=%q, want AVOID for 3 hard failures (gate2+gate3+gate5)", dec.MaxLabel)
	}
}

func TestMaxLabel_AllSkipped_NoConstraint(t *testing.T) {
	// A completely zero snapshot — all gate fields unset.
	snap := model.TokenSnapshot{
		AgeSeconds: 300, // < 60m so holder gates skip
		// All 7-gate fields zero → all gates skip except gate 3
	}
	// gate3 uses clusterRatio=0 → passes
	dec := engine.EvaluateGates(snap, 0.0, defaultCfg())
	// Gate 3 always evaluates; with ratio=0 it passes.
	// Gates 1,2,4,5,6,7 skip.
	// hardFails = 0 (gate 3 passes) → MaxLabel = "BUY"
	// But only 1 evaluated gate (gate 3), so it's minimal constraint.
	if dec.MaxLabel == "" {
		// Acceptable: no constraint when all data is absent
	} else if dec.MaxLabel != "BUY" {
		t.Errorf("MaxLabel=%q for near-zero snap (gate3 passes), want BUY or empty", dec.MaxLabel)
	}
}

// ============================================================
// Gate 4 hard rule: vol/MC out of range → cap MaxLabel at WATCH
// ============================================================

func TestGate4_Fail_CapsAtWATCH_WhenOtherGatesPass(t *testing.T) {
	snap := defaultSnap()
	snap.Volume24hSOL = 5.0 // 5/1000 = 0.005 < 0.01 → gate 4 fails, all others pass
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg())
	if dec.MaxLabel != "WATCH" {
		t.Errorf("MaxLabel=%q, want WATCH when only gate 4 fails", dec.MaxLabel)
	}
}

func TestGate4_Fail_DoesNotUpgradeExistingAVOID(t *testing.T) {
	snap := defaultSnap()
	snap.Volume24hSOL = 5.0     // gate 4 fails
	snap.Top10HolderPct = 0.30  // gate 2 fails
	snap.OrganicWinnerCount = 3 // gate 5 fails — 3 hard fails → AVOID
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg())
	if dec.MaxLabel != "AVOID" {
		t.Errorf("MaxLabel=%q, want AVOID when 3+ gates fail (gate4 cap does not loosen AVOID)", dec.MaxLabel)
	}
}

// ============================================================
// Gate 6 hard rule: stall/decline → force AVOID regardless of other gates
// ============================================================

func TestGate6_Fail_ForcesAVOID_WhenOtherGatesPass(t *testing.T) {
	snap := defaultSnap()
	snap.HoldersAt60m = 50 // equal to HoldersAt30m=50 → stall → gate 6 fails
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg())
	if dec.MaxLabel != "AVOID" {
		t.Errorf("MaxLabel=%q, want AVOID when gate 6 fails (holder stall), even if all others pass", dec.MaxLabel)
	}
}

func TestGate6_Fail_ForcesAVOID_WhenOnlyGate5AlsoFails(t *testing.T) {
	snap := defaultSnap()
	snap.HoldersAt60m = 40      // gate 6 fails (decline)
	snap.OrganicWinnerCount = 3 // gate 5 fails
	dec := engine.EvaluateGates(snap, 0.02, defaultCfg())
	if dec.MaxLabel != "AVOID" {
		t.Errorf("MaxLabel=%q, want AVOID (gate6 hard rule regardless of fail count)", dec.MaxLabel)
	}
}

// ============================================================
// ComputeTop10HolderPct helper
// ============================================================

func TestComputeTop10HolderPct_Concentrated(t *testing.T) {
	balances := map[string]float64{
		"A": 500, "B": 100, "C": 100, "D": 50, "E": 50,
		"F": 50, "G": 50, "H": 30, "I": 30, "J": 30, "K": 10,
	}
	pct := engine.ComputeTop10HolderPct(balances)
	// Top 10: A=500, B=100, C=100, D=50, E=50, F=50, G=50, H=30, I=30, J=30 → sum=990
	// Total: 990+10=1000
	// Top10Pct = 990/1000 = 0.99
	if pct < 0.98 || pct > 1.0 {
		t.Errorf("top10_pct=%.4f, want ~0.990", pct)
	}
}

func TestComputeTop10HolderPct_Distributed(t *testing.T) {
	balances := make(map[string]float64)
	for i := 0; i < 100; i++ {
		balances[string(rune('A'+i))] = 10.0
	}
	pct := engine.ComputeTop10HolderPct(balances)
	// 10 equal-weighted holders out of 100: top10 = 100/1000 = 0.10
	if pct < 0.09 || pct > 0.11 {
		t.Errorf("top10_pct=%.4f, want 0.10 for 100 equal holders", pct)
	}
}

func TestComputeTop10HolderPct_Empty(t *testing.T) {
	if pct := engine.ComputeTop10HolderPct(nil); pct != 0 {
		t.Errorf("expected 0 for nil map, got %.4f", pct)
	}
}

func TestCountHolders(t *testing.T) {
	balances := map[string]float64{"A": 10, "B": 0, "C": -5, "D": 100}
	n := engine.CountHolders(balances)
	if n != 2 { // A and D have positive balances
		t.Errorf("CountHolders=%d, want 2", n)
	}
}
