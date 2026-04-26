package live_test

// hardening_test.go covers the six hardening modules added in the final
// pre-go-live pass:
//
//   6A — hard impact veto
//   6B — effective buyer clustering (gate behaviour; cluster internals tested in cluster_test.go)
//   6C — freshness / stale signal control
//   6D — warm-up / confidence gate
//   6E — short-window sell reversal veto

import (
	"context"
	"strings"
	"testing"
	"time"

	"memecoin_scorer/internal/cluster"
	"memecoin_scorer/internal/live"
	"memecoin_scorer/internal/model"
)

// freshSnap returns a snapshot that passes all BUY gates; LastEventAt is set to
// the same moment as now so freshness always passes.
func freshSnap(now time.Time) model.TokenSnapshot {
	s := baseSnap()
	s.LastEventAt = now
	s.FirstSeenAt = now.Add(-5 * time.Minute)
	s.AgeSeconds = 300 // 5 min — above MinTokenAgeSecondsForBuy default (90s)
	return s
}

// cfgWithNow returns the default config with a healthy StaticResolver wired in.
// Without a healthy resolver ClusterRequired=true blocks BUY/READY in all tests
// that don't specifically test the degraded path.
func cfgWithNow() live.LiveConfig {
	c := live.DefaultLiveConfig()
	c.FunderResolver = cluster.NewStaticResolver(map[string]string{}) // healthy, empty → effective == raw
	return c
}

// ============================================================
// Module 6A — Hard Impact Veto
// ============================================================

// helper: impact = tradeSOL / liqProxy * 100
// tradeSOL=1.0 (default), liqProxy = TotalBuySOL + TotalSellSOL

func TestImpact_BelowThreshold_NoVeto(t *testing.T) {
	// impactPct = 1/200*100 = 0.5% < 15.0 default → no veto
	s := freshSnap(epoch)
	s.TotalBuySOL = 180
	s.TotalSellSOL = 20
	d := live.ClassifyAt(s, cfgWithNow(), epoch)
	if d.Label == live.LabelAVOID {
		t.Errorf("label = AVOID, want BUY/READY/WATCH; impact below threshold; reasons: %v", d.Reasons)
	}
}

func TestImpact_EqualThreshold_NoVeto(t *testing.T) {
	// impactPct = 1/6.667*100 = 15.0% exactly == MaxEstimatedImpactPct → NOT vetoed (strictly greater triggers)
	c := cfgWithNow()
	c.MaxEstimatedImpactPct = 15.0
	// liqProxy needed: tradeSOL/liqProxy*100 = 15.0 → liqProxy = 100/15 ≈ 6.667
	s := freshSnap(epoch)
	s.TotalBuySOL = 6.667
	s.TotalSellSOL = 0.0
	d := live.ClassifyAt(s, c, epoch)
	// At exactly the threshold we do NOT veto — only strictly above.
	// Note: exec = 6.667/20 = 0.33 which passes READY but not BUY.
	if d.Label == live.LabelAVOID {
		for _, r := range d.Reasons {
			if strings.Contains(r, "impact") && strings.Contains(r, "> max") {
				t.Errorf("impact veto must not fire at exactly the threshold; reasons: %v", d.Reasons)
				return
			}
		}
	}
}

func TestImpact_AboveThreshold_ForcesAVOID(t *testing.T) {
	// impactPct = 1/5*100 = 20% > MaxEstimatedImpactPct(15) → forced AVOID
	s := freshSnap(epoch)
	s.TotalBuySOL = 5.0
	s.TotalSellSOL = 0.0
	d := live.ClassifyAt(s, cfgWithNow(), epoch)
	if d.Label != live.LabelAVOID {
		t.Errorf("label = %q, want AVOID (impact > max); reasons: %v", d.Label, d.Reasons)
	}
	hasImpactReason := false
	for _, r := range d.Reasons {
		if strings.Contains(r, "impact") {
			hasImpactReason = true
			break
		}
	}
	if !hasImpactReason {
		t.Errorf("expected impact reason in %v", d.Reasons)
	}
}

func TestImpact_DefaultConfig_Is15(t *testing.T) {
	c := live.DefaultLiveConfig()
	if c.MaxEstimatedImpactPct != 15.0 {
		t.Errorf("MaxEstimatedImpactPct default = %.1f, want 15.0", c.MaxEstimatedImpactPct)
	}
}

// ============================================================
// Module 6B — Effective buyer clustering in gates
// ============================================================

// fixedResolverFor returns a FunderResolver that maps a set of wallets to one parent.
func fixedResolverFor(wallets ...string) cluster.FunderResolver {
	return testResolver(wallets)
}

type testResolver []string

func (r testResolver) ResolveParent(_ context.Context, wallet string, _ time.Time) (string, bool, error) {
	for _, w := range r {
		if w == wallet {
			return "PARENT", true, nil
		}
	}
	return wallet, false, nil
}

func TestClustering_NoParentData_EffectiveEqualsRaw(t *testing.T) {
	// NullResolver: effective == raw; gates behave identically to pre-clustering.
	s := freshSnap(epoch)
	s.BuyersLast1m = 5
	s.UniqueWalletsLast1m = []string{"A", "B", "C", "D", "E"}
	d := live.ClassifyAt(s, cfgWithNow(), epoch)
	if d.EffectiveBuyers1m != 5 {
		t.Errorf("effective_buyers_1m=%d, want 5 (NullResolver)", d.EffectiveBuyers1m)
	}
	if d.ClusteredBuyerCount != 0 {
		t.Errorf("clustered=%d, want 0", d.ClusteredBuyerCount)
	}
}

func TestClustering_SameParentCollapses_GateSees_EffectiveCount(t *testing.T) {
	// 3 wallets → 2 share a parent → EffectiveBuyers1m = 2, below MinBuyers1mBUY(3) → BUY blocked.
	c := cfgWithNow()
	c.FunderResolver = fixedResolverFor("A", "B") // A and B → PARENT
	s := freshSnap(epoch)
	s.BuyersLast1m = 3
	s.UniqueWalletsLast1m = []string{"A", "B", "C"} // A+B → 1 root + C → 2 effective
	d := live.ClassifyAt(s, c, epoch)
	if d.EffectiveBuyers1m != 2 {
		t.Errorf("effective_buyers_1m=%d, want 2", d.EffectiveBuyers1m)
	}
	// effective(2) < MinBuyers1mBUY(3) → should not be BUY
	if d.Label == live.LabelBUY {
		t.Errorf("BUY must be blocked when effective_buyers_1m=%d < MinBuyers1mBUY(3)", d.EffectiveBuyers1m)
	}
}

func TestClustering_RawPreserved_EffectiveChanges(t *testing.T) {
	// BuyersLast1m (raw) stays 4; effective is 3 after collapsing two wallets.
	c := cfgWithNow()
	c.FunderResolver = fixedResolverFor("W1", "W2")
	s := freshSnap(epoch)
	s.BuyersLast1m = 4
	s.UniqueWalletsLast1m = []string{"W1", "W2", "W3", "W4"}
	d := live.ClassifyAt(s, c, epoch)
	if s.BuyersLast1m != 4 {
		t.Errorf("raw BuyersLast1m must stay 4, got %d", s.BuyersLast1m)
	}
	if d.EffectiveBuyers1m != 3 {
		t.Errorf("effective_buyers_1m=%d, want 3", d.EffectiveBuyers1m)
	}
	if d.ClusteredBuyerCount != 1 {
		t.Errorf("clustered=%d, want 1", d.ClusteredBuyerCount)
	}
}

func TestClustering_DecisionUsesEffective_NotRaw(t *testing.T) {
	// Raw buyers=4 would pass MinBuyers1mBUY(3), but effective=1 should not.
	c := cfgWithNow()
	c.FunderResolver = fixedResolverFor("A", "B", "C", "D") // all → PARENT → 1 effective
	s := freshSnap(epoch)
	s.BuyersLast1m = 4
	s.UniqueWalletsLast1m = []string{"A", "B", "C", "D"}
	d := live.ClassifyAt(s, c, epoch)
	if d.EffectiveBuyers1m != 1 {
		t.Errorf("effective=%d, want 1", d.EffectiveBuyers1m)
	}
	if d.Label == live.LabelBUY {
		t.Errorf("BUY must be blocked when effective_buyers_1m=1 < MinBuyers1mBUY(3)")
	}
}

// ============================================================
// Module 6C — Freshness / stale signal control
// ============================================================

func TestFreshness_BUY_Fresh_WithinWindow(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.LastEventAt = now.Add(-2 * time.Minute) // 2m ago < MaxSignalAgeMinBuyReady(5)
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label != live.LabelBUY {
		t.Fatalf("expected BUY label, got %q; reasons: %v", d.Label, d.Reasons)
	}
	if d.SignalState != live.StateFresh {
		t.Errorf("signal_state=%q, want fresh", d.SignalState)
	}
	if !d.IsActionable {
		t.Errorf("is_actionable must be true for fresh BUY")
	}
}

func TestFreshness_BUY_Expired_BeyondWindow(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.LastEventAt = now.Add(-6 * time.Minute) // 6m ago > MaxSignalAgeMinBuyReady(5)
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label != live.LabelBUY {
		t.Fatalf("expected BUY label, got %q", d.Label)
	}
	if d.SignalState != live.StateExpired {
		t.Errorf("signal_state=%q, want expired", d.SignalState)
	}
	if d.IsActionable {
		t.Errorf("is_actionable must be false for expired BUY")
	}
}

func TestFreshness_READY_Expired_BeyondWindow(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	// Make it READY, not BUY: low 1m buyers, enough 5m buyers.
	s.BuyersLast1m = 1
	s.BuyerAcceleration = 0.5
	s.BuyersLast5m = 8
	s.LastEventAt = now.Add(-7 * time.Minute) // beyond 5m window
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label != live.LabelREADY {
		t.Fatalf("expected READY, got %q; reasons: %v", d.Label, d.Reasons)
	}
	if d.SignalState != live.StateExpired {
		t.Errorf("signal_state=%q, want expired", d.SignalState)
	}
	if d.IsActionable {
		t.Errorf("is_actionable must be false for expired READY")
	}
}

func TestFreshness_WATCH_Stale_BetweenBuyReadyAndWatchWindow(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := model.TokenSnapshot{
		Mint:             "WATCHMINT",
		FirstSeenAt:      now.Add(-20 * time.Minute),
		LastEventAt:      now.Add(-8 * time.Minute), // 8m ago: > 5m (BUY/READY window) but < 15m (WATCH window)
		UniqueBuyerCount: 10,
		TotalBuySOL:      50,
		TotalSellSOL:     10,
		BuyersLast1m:     0,
		BuyersLast5m:     0,
		AgeSeconds:       1200,
	}
	c := cfgWithNow()
	c.MaxEstimatedImpactPct = 20.0 // enough room
	d := live.ClassifyAt(s, c, now)
	if d.Label != live.LabelWATCH {
		t.Fatalf("expected WATCH, got %q; reasons: %v", d.Label, d.Reasons)
	}
	if d.SignalState != live.StateStale {
		t.Errorf("signal_state=%q, want stale (8m > buyready window 5m, < watch window 15m)", d.SignalState)
	}
	if !d.IsActionable {
		t.Errorf("is_actionable must be true: still within WATCH window")
	}
}

func TestFreshness_WATCH_Expired_BeyondWatchWindow(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := model.TokenSnapshot{
		Mint:             "OLDWATCH",
		FirstSeenAt:      now.Add(-40 * time.Minute),
		LastEventAt:      now.Add(-20 * time.Minute), // 20m ago > MaxSignalAgeMinWatch(15)
		UniqueBuyerCount: 5,
		TotalBuySOL:      50,
		TotalSellSOL:     5,
		AgeSeconds:       2400,
	}
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label != live.LabelWATCH {
		t.Fatalf("expected WATCH, got %q; reasons: %v", d.Label, d.Reasons)
	}
	if d.SignalState != live.StateExpired {
		t.Errorf("signal_state=%q, want expired", d.SignalState)
	}
	if d.IsActionable {
		t.Errorf("is_actionable must be false for expired WATCH")
	}
}

func TestFreshness_BoundaryExact_BuyReady(t *testing.T) {
	// Exactly at the threshold: not expired.
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.LastEventAt = now.Add(-5 * time.Minute) // exactly MaxSignalAgeMinBuyReady(5) — fresh
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.SignalState == live.StateExpired {
		t.Errorf("signal_state=%q at exactly the threshold, want fresh", d.SignalState)
	}
}

func TestFreshness_DefaultEnv(t *testing.T) {
	c := live.DefaultLiveConfig()
	if c.MaxSignalAgeMinBuyReady != 5.0 {
		t.Errorf("MaxSignalAgeMinBuyReady=%v, want 5.0", c.MaxSignalAgeMinBuyReady)
	}
	if c.MaxSignalAgeMinWatch != 15.0 {
		t.Errorf("MaxSignalAgeMinWatch=%v, want 15.0", c.MaxSignalAgeMinWatch)
	}
}

func TestPriorityLabels_ExpiredRowCannotRemainHeroWhenFresherRowsExist(t *testing.T) {
	old := priorityRow("OLD", live.StateExpired, epoch.Add(-20*time.Minute))
	old.ConfidenceScore = 100

	fresh := priorityRow("FRESH", live.StateFresh, epoch.Add(-1*time.Minute))
	fresh.ConfidenceScore = 80

	rows := []model.LiveSnapshot{old, fresh}
	live.AssignPriorityLabels(rows)

	if rows[0].PriorityLabel == "best_on_tape" {
		t.Fatalf("expired row was selected as hero: %+v", rows[0])
	}
	if rows[1].PriorityLabel != "best_on_tape" {
		t.Fatalf("fresh row priority=%q, want best_on_tape", rows[1].PriorityLabel)
	}
}

func TestPriorityLabels_NewerLastEventWinsAmongSimilarlyRankedRows(t *testing.T) {
	older := priorityRow("OLDER", live.StateFresh, epoch.Add(-2*time.Minute))
	newer := priorityRow("NEWER", live.StateFresh, epoch.Add(-30*time.Second))

	rows := []model.LiveSnapshot{older, newer}
	live.AssignPriorityLabels(rows)

	if rows[0].PriorityLabel == "best_on_tape" {
		t.Fatalf("older equal-ranked row was selected as hero")
	}
	if rows[1].PriorityLabel != "best_on_tape" {
		t.Fatalf("newer equal-ranked row priority=%q, want best_on_tape", rows[1].PriorityLabel)
	}
}

func TestPriorityLabels_ExpiredRowsRemainVisibleButNotHero(t *testing.T) {
	expired := priorityRow("EXPIRED", live.StateExpired, epoch.Add(-20*time.Minute))
	fresh := priorityRow("FRESH", live.StateFresh, epoch.Add(-1*time.Minute))

	rows := []model.LiveSnapshot{expired, fresh}
	live.AssignPriorityLabels(rows)

	if len(rows) != 2 {
		t.Fatalf("rows length=%d, want expired row retained in table payload", len(rows))
	}
	if rows[0].Mint != "EXPIRED" {
		t.Fatalf("expired row removed or reordered unexpectedly: %+v", rows)
	}
	if rows[0].PriorityLabel == "best_on_tape" {
		t.Fatalf("expired row remained hero")
	}
	if rows[1].PriorityLabel != "best_on_tape" {
		t.Fatalf("fresh row priority=%q, want best_on_tape", rows[1].PriorityLabel)
	}
}

func priorityRow(mint, signalState string, lastEventAt time.Time) model.LiveSnapshot {
	return model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			Mint:         mint,
			FirstSeenAt:  epoch.Add(-30 * time.Minute),
			LastEventAt:  lastEventAt,
			MarketCapSOL: 10,
		},
		Decision:              live.LabelWATCH,
		SignalState:           signalState,
		IsActionable:          signalState != live.StateExpired,
		ConfidenceScore:       75,
		ClusteringRowStatus:   live.ClusteringResolved,
		AdversarialScore:      0.10,
		EffectiveBuyers5m:     5,
		EstimatedImpactPct:    4,
		LiquidityProxySOL:     25,
		ExecutionPenalty:      1,
		FundingClusterRatio:   0,
		MarketCapSol:          10,
		Layer0Reject:          false,
		ClusteredBuyerCount:   0,
		EffectiveBuyers1m:     3,
		ClusteringStatus:      live.ClusteringHealthy,
		ClusteringBackend:     "static",
		ActionabilityLabel:    "observe closely",
		OperatorVerdict:       "watchable",
		QualityTier:           "NEAR",
		TriggerLine:           "5 eff/5m",
		NoTradeReason:         "",
		WhyNow:                "buyer flow",
		WhyNotHigher:          "",
		DominantBlocker:       "",
		PriorityLabel:         "",
		RelativeSetupLabel:    "",
		TrustLabel:            "organic",
		TrustReason:           "no major trust impairment detected",
		AsymmetryLabel:        "closest to upgrade",
		AsymmetryReason:       "this needs the fewest changes to become interesting",
		OperatorFocus:         "",
		ExecutionURL:          "",
		HistoricalOutcomeBand: "",
	}
}

// ============================================================
// Module 6D — Warm-up / Confidence gate
// ============================================================

func TestWarmup_BUY_BlockedWhenTooYoung(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.AgeSeconds = 30 // 30s < MinTokenAgeSecondsForBuy(90)
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label == live.LabelBUY {
		t.Errorf("BUY must be blocked when warming_up=true (age=30s < 90s)")
	}
	if !d.WarmingUp {
		t.Errorf("warming_up must be true when age < MinTokenAgeSecondsForBuy")
	}
	// Reason must mention warming_up
	hasWarmReason := false
	for _, r := range d.Reasons {
		if strings.Contains(r, "warming_up") {
			hasWarmReason = true
			break
		}
	}
	if !hasWarmReason {
		t.Errorf("expected warming_up reason in %v", d.Reasons)
	}
}

func TestWarmup_BUY_BlockedWhenTooFewEvents(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.AgeSeconds = 300    // old enough
	s.TotalEventCount = 2 // 2 < MinTotalEventsForConf(3) — triggers event-count gate
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label == live.LabelBUY {
		t.Errorf("BUY must be blocked when total_events=%d < MinTotalEventsForConf(3)", s.TotalEventCount)
	}
	if !d.WarmingUp {
		t.Errorf("warming_up must be true when total_events < min")
	}
}

func TestWarmup_READY_AllowedWhileWarmingUp(t *testing.T) {
	// Warm-up only blocks BUY; READY may still be issued if its gates pass.
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.AgeSeconds = 30  // too young for BUY
	s.BuyersLast1m = 1 // fails BUY velocity gate too
	s.BuyerAcceleration = 0.5
	s.BuyersLast5m = 8 // passes READY
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label == live.LabelBUY {
		t.Errorf("BUY must be blocked when warming_up; got BUY")
	}
	// READY should be allowed (warm-up does not block READY).
	if d.Label != live.LabelREADY {
		t.Errorf("label=%q, want READY (warm-up does not block READY); reasons: %v", d.Label, d.Reasons)
	}
}

func TestWarmup_ZeroTotalEventCount_DoesNotTriggerEventGate(t *testing.T) {
	// TotalEventCount=0 means the field is unpopulated (pre-hardening test snapshot).
	// The event-count warm-up gate must NOT fire in this case.
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.TotalEventCount = 0 // zero = skipped
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.WarmingUp {
		t.Errorf("warming_up must be false when TotalEventCount=0 (field not populated)")
	}
	if d.Label != live.LabelBUY {
		t.Errorf("label=%q, want BUY (no warm-up gate should fire); reasons: %v", d.Label, d.Reasons)
	}
}

func TestConfidence_ClampedTo0_100(t *testing.T) {
	cases := []model.TokenSnapshot{
		freshSnap(epoch),
		baseSnap(),
		{Mint: "EMPTY"},
	}
	for _, s := range cases {
		d := live.ClassifyAt(s, cfgWithNow(), epoch)
		if d.ConfidenceScore < 0 || d.ConfidenceScore > 100 {
			t.Errorf("confidence=%.2f out of [0,100] for mint=%q", d.ConfidenceScore, s.Mint)
		}
	}
}

func TestConfidence_RisesWithHealthierConditions(t *testing.T) {
	now := epoch.Add(time.Hour)

	// Weak: young token, few events, high adversarial.
	weak := freshSnap(now)
	weak.AgeSeconds = 20 // young
	weak.TotalEventCount = 1
	weak.TopWalletBuyShareLast5m = 1.0 // max adversarial concentration

	// Strong: mature, many events, clean adversarial.
	strong := freshSnap(now)
	strong.AgeSeconds = 600
	strong.TotalEventCount = 50
	strong.TopWalletBuyShareLast5m = 0.0
	strong.WalletDiversityRatio = 1.0 // fully diverse

	c := cfgWithNow()
	weakD := live.ClassifyAt(weak, c, now)
	strongD := live.ClassifyAt(strong, c, now)

	if strongD.ConfidenceScore <= weakD.ConfidenceScore {
		t.Errorf("confidence(strong)=%.2f should be > confidence(weak)=%.2f",
			strongD.ConfidenceScore, weakD.ConfidenceScore)
	}
}

func TestWarmup_DefaultEnv(t *testing.T) {
	c := live.DefaultLiveConfig()
	if c.MinTokenAgeSecondsForBuy != 90 {
		t.Errorf("MinTokenAgeSecondsForBuy=%v, want 90", c.MinTokenAgeSecondsForBuy)
	}
	if c.MinEffBuyers1mForConfidentBuy != 3 {
		t.Errorf("MinEffBuyers1mForConfidentBuy=%v, want 3", c.MinEffBuyers1mForConfidentBuy)
	}
	if c.MinTotalEventsForConf != 3 {
		t.Errorf("MinTotalEventsForConf=%v, want 3", c.MinTotalEventsForConf)
	}
}

// ============================================================
// Module 6E — Short-window sell reversal veto
// ============================================================

func TestSellReversal_HealthyBuyDominance_NoVeto(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.BuySolLast1m = 5.0
	s.SellSolLast1m = 1.0 // buy >> sell → no reversal
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label != live.LabelBUY {
		t.Errorf("label=%q, want BUY (healthy buy dominance); reasons: %v", d.Label, d.Reasons)
	}
}

func TestSellReversal_EqualLastMin_VetosBUY(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.BuySolLast1m = 3.0
	s.SellSolLast1m = 3.0 // equal → veto
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label == live.LabelBUY {
		t.Errorf("BUY must be blocked when sell_1m >= buy_1m (equal)")
	}
	hasSellReversalReason := false
	for _, r := range d.Reasons {
		if strings.Contains(r, "sell_reversal") {
			hasSellReversalReason = true
			break
		}
	}
	if !hasSellReversalReason {
		t.Errorf("expected sell_reversal reason in %v", d.Reasons)
	}
}

func TestSellReversal_SellGreaterThanBuy_VetosBUY(t *testing.T) {
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.BuySolLast1m = 2.4
	s.SellSolLast1m = 3.2 // sell > buy → veto
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label == live.LabelBUY {
		t.Errorf("BUY must be blocked when sell_1m=%.1f > buy_1m=%.1f",
			s.SellSolLast1m, s.BuySolLast1m)
	}
}

func TestSellReversal_CumulativeFineButRecentReversed_StillVetos(t *testing.T) {
	// Cumulative: buy >> sell (good). Recent 1m: sell > buy (reversal). Veto must win.
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.TotalBuySOL = 500.0 // cumulative buy >> sell
	s.TotalSellSOL = 20.0
	s.BuySolLast1m = 0.5 // recent: sell > buy
	s.SellSolLast1m = 2.0
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label == live.LabelBUY {
		t.Errorf("BUY must be blocked: recent sell reversal despite cumulative buy dominance")
	}
}

func TestSellReversal_BothZero_NoVeto(t *testing.T) {
	// No recent 1m activity on either side — veto must NOT fire (no data).
	now := epoch.Add(time.Hour)
	s := freshSnap(now)
	s.BuySolLast1m = 0.0
	s.SellSolLast1m = 0.0
	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.Label != live.LabelBUY {
		t.Errorf("label=%q, want BUY (no recent 1m activity — veto must not fire); reasons: %v",
			d.Label, d.Reasons)
	}
}
