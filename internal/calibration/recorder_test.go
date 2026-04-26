package calibration

import (
	"testing"
	"time"

	"memecoin_scorer/internal/model"
)

func TestRecorderCapturesCheckpointsWithoutFabricating(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	first := now.Add(-5 * time.Minute)
	rec := NewRecorder()

	rec.Observe(liveRow("TOKEN", first, now), now)
	samples := rec.Samples(10)
	if len(samples) != 0 {
		t.Fatalf("samples before shadow outcome = %d, want 0", len(samples))
	}

	stored := rec.records["TOKEN"]
	if stored == nil || stored.SnapshotAt5m == nil {
		t.Fatalf("5m checkpoint was not captured: %+v", stored)
	}
	if stored.SnapshotAt15m != nil || stored.SnapshotAt30m != nil {
		t.Fatalf("future checkpoints must not be fabricated: %+v", stored)
	}
	if stored.SnapshotAt5m.Decision != "WATCH" {
		t.Fatalf("checkpoint decision = %q, want WATCH", stored.SnapshotAt5m.Decision)
	}
	if stored.SnapshotAt5m.Posture != "NEAR" {
		t.Fatalf("checkpoint posture = %q, want NEAR", stored.SnapshotAt5m.Posture)
	}
	if !stored.SnapshotAt5m.IsActionable {
		t.Fatalf("checkpoint should retain actionable posture")
	}
	if stored.SnapshotAt5m.FeatureSummary.TotalEventCount != 17 {
		t.Fatalf("total_event_count = %d, want 17", stored.SnapshotAt5m.FeatureSummary.TotalEventCount)
	}
}

func TestRecorderAttachesMaturedShadowOutcome(t *testing.T) {
	first := time.Unix(1_700_000_000, 0)
	rec := NewRecorder()

	rec.Observe(liveRow("TOKEN", first, first.Add(5*time.Minute)), first.Add(5*time.Minute))
	mature := liveRow("TOKEN", first, first.Add(36*time.Minute))
	mature.Shadow = completeShadow()
	rec.Observe(mature, first.Add(36*time.Minute))

	stored := rec.records["TOKEN"]
	if stored == nil {
		t.Fatal("record missing")
	}
	if !stored.ShadowEligibleForScore {
		t.Fatalf("shadow outcome not attached: %+v", stored)
	}
	if stored.ShadowValidatedTradeable30m == nil || !*stored.ShadowValidatedTradeable30m {
		t.Fatalf("tradeable shadow = %v, want true", stored.ShadowValidatedTradeable30m)
	}
	if stored.ShadowOpportunityScore == nil || *stored.ShadowOpportunityScore != 77.5 {
		t.Fatalf("opportunity score = %v, want 77.5", stored.ShadowOpportunityScore)
	}

	incomplete := liveRow("TOKEN", first, first.Add(37*time.Minute))
	incomplete.Shadow = model.ShadowScoreResult{
		FeatureWindowComplete: true,
		MissingFields:         []string{"mfe_multiple_30m"},
	}
	rec.Observe(incomplete, first.Add(37*time.Minute))
	if stored.ShadowOpportunityScore == nil || *stored.ShadowOpportunityScore != 77.5 {
		t.Fatalf("mature shadow outcome was cleared by incomplete observation: %+v", stored)
	}
}

func TestRecorderEmitsRowsOnlyWhenUseful(t *testing.T) {
	first := time.Unix(1_700_000_000, 0)
	rec := NewRecorder()

	onlyMature := liveRow("ONLY_MATURE", first, first.Add(36*time.Minute))
	onlyMature.Shadow = completeShadow()
	rec.Observe(onlyMature, first.Add(36*time.Minute))
	if got := rec.Samples(10); len(got) != 0 {
		t.Fatalf("mature shadow without early checkpoint emitted %d rows, want 0", len(got))
	}

	rec.Observe(liveRow("READY", first, first.Add(15*time.Minute)), first.Add(15*time.Minute))
	mature := liveRow("READY", first, first.Add(36*time.Minute))
	mature.Shadow = completeShadow()
	rec.Observe(mature, first.Add(36*time.Minute))

	samples := rec.Samples(10)
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(samples))
	}
	if samples[0].Mint != "READY" {
		t.Fatalf("mint = %q, want READY", samples[0].Mint)
	}
	if samples[0].SnapshotAt15m == nil {
		t.Fatalf("15m checkpoint missing from emitted sample: %+v", samples[0])
	}
}

func liveRow(mint string, firstSeen, now time.Time) model.LiveSnapshot {
	age := now.Sub(firstSeen).Seconds()
	return model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			Mint:                    mint,
			FirstSeenAt:             firstSeen,
			LastEventAt:             now,
			AgeSeconds:              age,
			UniqueBuyerCount:        12,
			TotalEventCount:         17,
			BuyersLast1m:            4,
			BuyersLast5m:            10,
			BuyerAcceleration:       2,
			TopWalletBuyShareLast5m: 0.20,
			WalletDiversityRatio:    0.80,
			RepeatBuyerShare1m:      0.10,
			BuySolLast1m:            2.5,
			SellSolLast1m:           0.5,
			TotalBuySOL:             25,
			TotalSellSOL:            5,
			SellTradeCount:          7,
			Top10HolderPct:          0.30,
			HolderCount:             20,
			MarketCapSOL:            100,
			LastPriceSOL:            0.00001,
		},
		Decision:            "WATCH",
		SignalState:         "fresh",
		OperatorVerdict:     "watchable",
		ConfidenceScore:     64,
		PriorityLabel:       "monitor_for_upgrade",
		ActionabilityLabel:  "observe closely",
		IsActionable:        true,
		QualityTier:         "NEAR",
		EffectiveBuyers1m:   4,
		EffectiveBuyers5m:   9,
		LiquidityProxySOL:   30,
		ExecutionPenalty:    0.60,
		EstimatedImpactPct:  3.3,
		AdversarialScore:    0.25,
		FundingClusterRatio: 0.10,
		ClusteringRowStatus: "resolved",
		Engine: model.EngineDecision{
			MaxLabel:       "WATCH",
			GatesPassCount: 4,
		},
	}
}

func completeShadow() model.ShadowScoreResult {
	tradeable := true
	clean := false
	score := 77.5
	return model.ShadowScoreResult{
		EligibleForShadowScore: true,
		FeatureWindowComplete:  true,
		ValidatedTradeable30m:  &tradeable,
		ValidatedClean30m:      &clean,
		OpportunityScore:       &score,
		ComparedAt:             time.Unix(1_700_000_000, 0).Add(36 * time.Minute).Unix(),
		Notes:                  []string{"shadow score complete"},
	}
}
