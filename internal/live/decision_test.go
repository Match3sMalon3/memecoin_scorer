package live_test

import (
	"strings"
	"testing"
	"time"

	"memecoin_scorer/internal/cluster"
	"memecoin_scorer/internal/live"
	"memecoin_scorer/internal/model"
)

var epoch = time.Unix(1_000_000, 0)

// cfg returns the default config for tests with a healthy StaticResolver wired in.
// Without a healthy resolver, ClusterRequired=true blocks BUY/READY in all tests.
func cfg() live.LiveConfig {
	c := live.DefaultLiveConfig()
	c.FunderResolver = cluster.NewStaticResolver(map[string]string{}) // healthy, empty → effective == raw
	return c
}

// baseSnap returns a snapshot that passes all BUY gates.
func baseSnap() model.TokenSnapshot {
	return model.TokenSnapshot{
		Mint:              "TESTMINT",
		FirstSeenAt:       epoch,
		LastEventAt:       epoch.Add(5 * time.Minute),
		UniqueBuyerCount:  20,
		TotalBuySOL:       100.0, // liqProxy = 120 → exec = 120/(1*20) = 1.0
		TotalSellSOL:      20.0,
		SellTradeCount:    5,
		BuyersLast1m:      5,   // >= MinBuyers1mBUY(3)
		BuyersLast5m:      15,  // >= MinBuyers5mREADY(5)
		BuyerAcceleration: 2.0, // >= MinAccelerationBUY(1.0)
		AgeSeconds:        300,
	}
}

// ---- BUY ----

func TestClassify_BUY_AllGatesPass(t *testing.T) {
	d := live.Classify(baseSnap(), cfg())
	if d.Label != live.LabelBUY {
		t.Errorf("label = %q, want BUY; reasons: %v", d.Label, d.Reasons)
	}
	if d.ExecutionPenalty <= 0 || d.ExecutionPenalty > 1 {
		t.Errorf("ExecutionPenalty = %.4f out of (0,1]", d.ExecutionPenalty)
	}
	if d.LiquidityProxySOL != 120.0 {
		t.Errorf("LiquidityProxySOL = %.2f, want 120.0", d.LiquidityProxySOL)
	}
}

func TestClassify_BUY_BlockedByWeakExecution(t *testing.T) {
	s := baseSnap()
	// liqProxy = 1 + 0 = 1 → exec = 1/(1*20) = 0.05 < MinExecQualityBUY(0.5)
	s.TotalBuySOL = 1.0
	s.TotalSellSOL = 0.0
	d := live.Classify(s, cfg())
	if d.Label == live.LabelBUY {
		t.Error("BUY must be blocked when execution_penalty < 0.5")
	}
	hasExecReason := false
	for _, r := range d.Reasons {
		if strings.Contains(r, "exec") {
			hasExecReason = true
			break
		}
	}
	if !hasExecReason {
		t.Errorf("expected exec reason in %v", d.Reasons)
	}
}

func TestClassify_BUY_BlockedByLowVelocity(t *testing.T) {
	s := baseSnap()
	s.BuyersLast1m = 1        // below MinBuyers1mBUY=3
	s.BuyerAcceleration = 0.5 // below MinAccelerationBUY=1.0
	d := live.Classify(s, cfg())
	if d.Label == live.LabelBUY {
		t.Error("BUY must be blocked when buyers_1m < 3 and accel < 1.0")
	}
}

func TestClassify_BUY_StrongVelocityBypassesAccel(t *testing.T) {
	s := baseSnap()
	s.BuyersLast1m = 10       // >= StrongVelocity1m=8, bypasses accel check
	s.BuyerAcceleration = 0.1 // below threshold, but bypassed
	d := live.Classify(s, cfg())
	if d.Label != live.LabelBUY {
		t.Errorf("label = %q, want BUY (strong velocity bypasses accel); reasons: %v", d.Label, d.Reasons)
	}
}

func TestClassify_BUY_BlockedBySellPressure(t *testing.T) {
	s := baseSnap()
	s.TotalBuySOL = 50.0
	s.TotalSellSOL = 60.0 // sell > buy
	d := live.Classify(s, cfg())
	if d.Label == live.LabelBUY {
		t.Error("BUY must be blocked when sell_sol > buy_sol")
	}
	hasSellReason := false
	for _, r := range d.Reasons {
		if strings.Contains(r, "sell_sol") || strings.Contains(r, "buy_sol") {
			hasSellReason = true
			break
		}
	}
	if !hasSellReason {
		t.Errorf("expected sell pressure reason in %v", d.Reasons)
	}
}

// ---- READY ----

func TestClassify_READY_GoodVolumeWeakVelocity(t *testing.T) {
	s := baseSnap()
	s.BuyersLast1m = 1        // fails BUY gate (< 3)
	s.BuyerAcceleration = 0.5 // fails BUY gate (< 1.0)
	s.BuyersLast5m = 8        // passes READY gate (>= 5)
	d := live.Classify(s, cfg())
	if d.Label != live.LabelREADY {
		t.Errorf("label = %q, want READY; reasons: %v", d.Label, d.Reasons)
	}
}

func TestClassify_READY_BlockedByWeakExecution(t *testing.T) {
	s := baseSnap()
	s.BuyersLast1m = 1        // fails BUY
	s.BuyerAcceleration = 0.5 // fails BUY
	// liqProxy = 1+0 = 1 → exec = 1/20 = 0.05 < MinExecQualityREADY(0.3)
	s.TotalBuySOL = 1.0
	s.TotalSellSOL = 0.0
	d := live.Classify(s, cfg())
	if d.Label == live.LabelREADY {
		t.Error("READY must be blocked when execution_penalty < 0.3")
	}
}

func TestClassify_READY_ExecJustAboveREADYFloor(t *testing.T) {
	// exec floor for READY is 0.3; set liqProxy so exec = 0.31
	// liqProxy = tradeSOL * multiplier * 0.31 = 1 * 20 * 0.31 = 6.2
	// impactPct = 1/6.2*100 ≈ 16.1% — above the default 15% impact ceiling,
	// so we raise MaxEstimatedImpactPct to 20 to isolate the exec-floor behaviour.
	s := baseSnap()
	s.BuyersLast1m = 1        // fails BUY
	s.BuyerAcceleration = 0.5 // fails BUY
	s.BuyersLast5m = 6        // passes READY
	s.TotalBuySOL = 6.2
	s.TotalSellSOL = 0.0
	c := cfg()
	c.MaxEstimatedImpactPct = 20.0 // raise ceiling to isolate exec-floor test
	d := live.Classify(s, c)
	if d.Label != live.LabelREADY {
		t.Errorf("label = %q, want READY (exec just above READY floor); reasons: %v", d.Label, d.Reasons)
	}
}

// ---- WATCH ----

func TestClassify_WATCH_SomeActivityNoVelocity(t *testing.T) {
	s := baseSnap()
	s.BuyersLast1m = 0        // fails BUY
	s.BuyerAcceleration = 0.0 // fails BUY
	s.BuyersLast5m = 2        // fails READY (< 5)
	s.UniqueBuyerCount = 5    // passes WATCH (>= 3)
	d := live.Classify(s, cfg())
	if d.Label != live.LabelWATCH {
		t.Errorf("label = %q, want WATCH; reasons: %v", d.Label, d.Reasons)
	}
}

func TestClassify_WATCH_BlockedByTooFewBuyers(t *testing.T) {
	s := baseSnap()
	s.BuyersLast1m = 0
	s.BuyerAcceleration = 0.0
	s.BuyersLast5m = 1
	s.UniqueBuyerCount = 2 // fails WATCH (< 3)
	d := live.Classify(s, cfg())
	if d.Label == live.LabelWATCH {
		t.Error("WATCH must be blocked when unique_buyers < 3")
	}
}

// ---- AVOID ----

func TestClassify_AVOID_ZeroLiquidity(t *testing.T) {
	s := baseSnap()
	s.TotalBuySOL = 0.0
	s.TotalSellSOL = 0.0
	d := live.Classify(s, cfg())
	if d.Label != live.LabelAVOID {
		t.Errorf("label = %q, want AVOID (zero liquidity)", d.Label)
	}
	if d.ExecutionPenalty != 0.0 {
		t.Errorf("ExecutionPenalty = %.4f, want 0.0", d.ExecutionPenalty)
	}
}

func TestClassify_AVOID_VeryThinLiquidity(t *testing.T) {
	// liqProxy = 0.1 → exec = 0.1/20 = 0.005 < MinExecQualityAVOID(0.1)
	s := baseSnap()
	s.TotalBuySOL = 0.1
	s.TotalSellSOL = 0.0
	d := live.Classify(s, cfg())
	if d.Label != live.LabelAVOID {
		t.Errorf("label = %q, want AVOID (very thin liquidity); exec=%.4f", d.Label, d.ExecutionPenalty)
	}
}

func TestClassify_AVOID_NoActivity(t *testing.T) {
	s := model.TokenSnapshot{
		Mint:       "DEAD",
		AgeSeconds: 3600,
	}
	d := live.Classify(s, cfg())
	if d.Label != live.LabelAVOID {
		t.Errorf("label = %q, want AVOID (no activity)", d.Label)
	}
}

// ---- Fields always populated ----

func TestClassify_AlwaysPopulatesFields(t *testing.T) {
	snaps := []model.TokenSnapshot{
		baseSnap(),
		{Mint: "EMPTY"},
	}
	for _, s := range snaps {
		d := live.Classify(s, cfg())
		if d.Label == "" {
			t.Errorf("Label is empty for snap %q", s.Mint)
		}
		if d.LiquidityProxySOL < 0 {
			t.Errorf("LiquidityProxySOL < 0 for snap %q", s.Mint)
		}
		if d.ExecutionPenalty < 0 || d.ExecutionPenalty > 1 {
			t.Errorf("ExecutionPenalty %.4f out of [0,1] for snap %q", d.ExecutionPenalty, s.Mint)
		}
		// Marketability fields must always be present.
		if d.TradeSizeSOL <= 0 {
			t.Errorf("TradeSizeSOL = %.4f, want > 0 for snap %q", d.TradeSizeSOL, s.Mint)
		}
		if d.EstimatedImpactPct < 0 || d.EstimatedImpactPct > 100 {
			t.Errorf("EstimatedImpactPct = %.4f out of [0,100] for snap %q", d.EstimatedImpactPct, s.Mint)
		}
	}
}

// ---- Config: stricter exec threshold prevents BUY ----

func TestClassify_StrictExecConfig_BlocksBUY(t *testing.T) {
	s := baseSnap()
	// liqProxy = 120; exec = 120/(1*20) = 1.0 — passes default
	// Now require exec >= 1.5 (impossible, capped at 1.0) → BUY blocked
	strictCfg := cfg()
	strictCfg.MinExecQualityBUY = 1.5 // artificially impossible
	d := live.Classify(s, strictCfg)
	if d.Label == live.LabelBUY {
		t.Error("BUY must be blocked when MinExecQualityBUY > 1.0 (exec can never exceed 1.0)")
	}
}

// ---- Marketability / execution reality ----

func TestClassify_EstimatedImpact_ZeroLiquidity(t *testing.T) {
	// Zero liquidity → impact = 0 (undefined, not a real zero)
	s := model.TokenSnapshot{Mint: "NOLIQ"}
	d := live.Classify(s, cfg())
	if d.EstimatedImpactPct != 0.0 {
		t.Errorf("EstimatedImpactPct = %.4f, want 0.0 when no liquidity", d.EstimatedImpactPct)
	}
}

func TestClassify_EstimatedImpact_KnownValues(t *testing.T) {
	// liqProxy = 100+20 = 120, tradeSOL = 1.0 → impact = 1/120*100 ≈ 0.833%
	s := baseSnap() // TotalBuySOL=100, TotalSellSOL=20 → liqProxy=120
	d := live.Classify(s, cfg())
	wantApprox := 1.0 / 120.0 * 100
	if d.EstimatedImpactPct < wantApprox-0.01 || d.EstimatedImpactPct > wantApprox+0.01 {
		t.Errorf("EstimatedImpactPct = %.4f, want ≈ %.4f", d.EstimatedImpactPct, wantApprox)
	}
}

func TestClassify_EstimatedImpact_CappedAt100(t *testing.T) {
	// tradeSOL >> liqProxy → impact capped at 100
	s := baseSnap()
	bigCfg := cfg()
	bigCfg.TradeSizeSOL = 10_000 // trade vastly exceeds proxy
	d := live.Classify(s, bigCfg)
	if d.EstimatedImpactPct > 100 {
		t.Errorf("EstimatedImpactPct = %.4f, must be capped at 100", d.EstimatedImpactPct)
	}
}

func TestClassify_TradeSizeSOL_ReflectsConfig(t *testing.T) {
	s := baseSnap()
	customCfg := cfg()
	customCfg.TradeSizeSOL = 3.5
	d := live.Classify(s, customCfg)
	if d.TradeSizeSOL != 3.5 {
		t.Errorf("TradeSizeSOL = %.4f, want 3.5 (must reflect config)", d.TradeSizeSOL)
	}
}

func TestClassify_ExecFailReason_ContainsLiqAndImpact(t *testing.T) {
	// Force exec failure at BUY gate; reason must mention liq_proxy and impact.
	s := baseSnap()
	s.TotalBuySOL = 1.0 // liqProxy=1+0=1 → exec=1/(1*20)=0.05 < MinExecQualityBUY(0.5)
	s.TotalSellSOL = 0.0
	d := live.Classify(s, cfg())
	if d.Label == live.LabelBUY {
		t.Fatal("expected BUY to be blocked")
	}
	foundExecReason := false
	for _, r := range d.Reasons {
		if strings.Contains(r, "liq_proxy") && strings.Contains(r, "impact") {
			foundExecReason = true
			break
		}
	}
	if !foundExecReason {
		t.Errorf("exec failure reason must contain liq_proxy and impact; got: %v", d.Reasons)
	}
}

func TestClassify_AvoidReason_ContainsExecDetails(t *testing.T) {
	// Hard AVOID (exec < MinExecQualityAVOID=0.1); reason must be informative.
	s := model.TokenSnapshot{
		Mint:             "THINMINT",
		TotalBuySOL:      0.05,
		UniqueBuyerCount: 5,
	}
	d := live.Classify(s, cfg())
	if d.Label != live.LabelAVOID {
		t.Fatalf("expected AVOID, got %q", d.Label)
	}
	if len(d.Reasons) == 0 {
		t.Fatal("AVOID decision must have at least one reason")
	}
	if !strings.Contains(d.Reasons[0], "exec_penalty") {
		t.Errorf("AVOID reason must mention exec_penalty; got: %q", d.Reasons[0])
	}
	if !strings.Contains(d.Reasons[0], "liq_proxy") {
		t.Errorf("AVOID reason must mention liq_proxy; got: %q", d.Reasons[0])
	}
}

// ---- Adversarial score ----

// advSnap returns a baseSnap extended with adversarial indicator fields.
func advSnap(topShare, diversity, repeat float64) model.TokenSnapshot {
	s := baseSnap()
	s.TopWalletBuyShareLast5m = topShare
	s.WalletDiversityRatio = diversity
	s.RepeatBuyerShare1m = repeat
	return s
}

func TestClassify_AdversarialScore_AlwaysInRange(t *testing.T) {
	cases := []model.TokenSnapshot{
		baseSnap(),
		advSnap(0, 1, 0), // perfectly clean
		advSnap(1, 0, 1), // maximally suspicious
		advSnap(0.5, 0.5, 0.5),
		{Mint: "EMPTY"},
	}
	for _, s := range cases {
		d := live.Classify(s, cfg())
		if d.AdversarialScore < 0 || d.AdversarialScore > 1 {
			t.Errorf("AdversarialScore=%.4f out of [0,1] for mint=%q", d.AdversarialScore, s.Mint)
		}
	}
}

func TestClassify_BUY_BlockedByHighAdversarial(t *testing.T) {
	// Set all three adversarial signals to max → score = 0.45+0.30+0.25 = 1.0 > MaxAdversarialBUY(0.60)
	s := advSnap(1.0, 0.0, 1.0)
	d := live.Classify(s, cfg())
	if d.Label == live.LabelBUY {
		t.Error("BUY must be blocked when adversarial_score > MaxAdversarialBUY")
	}
	hasAdvReason := false
	for _, r := range d.Reasons {
		if strings.Contains(r, "adversarial") {
			hasAdvReason = true
			break
		}
	}
	if !hasAdvReason {
		t.Errorf("expected adversarial reason in %v", d.Reasons)
	}
}

func TestClassify_READY_BlockedByHighAdversarial(t *testing.T) {
	// Fail BUY velocity gates but pass READY; then push adversarial above READY threshold.
	s := advSnap(1.0, 0.0, 1.0)
	s.BuyersLast1m = 1        // fails BUY velocity
	s.BuyerAcceleration = 0.5 // fails BUY accel
	s.BuyersLast5m = 8        // would pass READY buyers gate

	strictCfg := cfg()
	strictCfg.MaxAdversarialREADY = 0.5 // lower threshold so score=1.0 fails it
	d := live.Classify(s, strictCfg)
	if d.Label == live.LabelREADY {
		t.Error("READY must be blocked when adversarial_score > MaxAdversarialREADY")
	}
}

func TestClassify_BUY_CleanAdversarialPassesThrough(t *testing.T) {
	// All adversarial signals near zero → BUY should still pass (baseSnap already has 0 fields).
	s := advSnap(0.0, 1.0, 0.0) // score = 0 → clean
	d := live.Classify(s, cfg())
	if d.Label != live.LabelBUY {
		t.Errorf("label = %q, want BUY (zero adversarial score); reasons: %v", d.Label, d.Reasons)
	}
	if d.AdversarialScore != 0.0 {
		t.Errorf("AdversarialScore = %.4f, want 0.0", d.AdversarialScore)
	}
}
