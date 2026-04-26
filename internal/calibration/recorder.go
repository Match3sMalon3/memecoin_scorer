package calibration

import (
	"sort"
	"sync"
	"time"

	"memecoin_scorer/internal/model"
)

const (
	checkpoint5m  = 5 * time.Minute
	checkpoint15m = 15 * time.Minute
	checkpoint30m = 30 * time.Minute
	checkpoint35m = 35 * time.Minute
)

type FeatureSummary struct {
	UniqueBuyerCount     int     `json:"unique_buyer_count"`
	TotalEventCount      int     `json:"total_event_count"`
	BuyersLast1m         int     `json:"buyers_last1m"`
	BuyersLast5m         int     `json:"buyers_last5m"`
	BuyerAcceleration    float64 `json:"buyer_acceleration"`
	EffectiveBuyers1m    int     `json:"effective_buyers_1m"`
	EffectiveBuyers5m    int     `json:"effective_buyers_5m"`
	TotalBuySOL          float64 `json:"total_buy_sol"`
	TotalSellSOL         float64 `json:"total_sell_sol"`
	BuySolLast1m         float64 `json:"buy_sol_last_1m"`
	SellSolLast1m        float64 `json:"sell_sol_last_1m"`
	SellTradeCount       int     `json:"sell_trade_count"`
	LiquidityProxySOL    float64 `json:"liquidity_proxy_sol"`
	ExecutionPenalty     float64 `json:"execution_penalty"`
	EstimatedImpactPct   float64 `json:"estimated_impact_pct"`
	AdversarialScore     float64 `json:"adversarial_score"`
	FundingClusterRatio  float64 `json:"funding_cluster_ratio"`
	ClusterCompression1m float64 `json:"cluster_compression_ratio_1m"`
	ClusterCompression5m float64 `json:"cluster_compression_ratio_5m"`
	ClusteringRowStatus  string  `json:"clustering_row_status"`
	TopWalletBuyShare5m  float64 `json:"top_wallet_buy_share_5m"`
	WalletDiversityRatio float64 `json:"wallet_diversity_ratio"`
	RepeatBuyerShare1m   float64 `json:"repeat_buyer_share_1m"`
	Top10HolderPct       float64 `json:"top10_holder_pct"`
	HolderCount          int     `json:"holder_count"`
	Volume24hSOL         float64 `json:"volume_24h_sol"`
	LiquidityPoolSOL     float64 `json:"liquidity_pool_sol"`
	MarketCapSOL         float64 `json:"market_cap_sol"`
	LastPriceSOL         float64 `json:"last_price_sol"`
	EngineMaxLabel       string  `json:"engine_max_label"`
	EngineGatesPassCount int     `json:"engine_gates_pass_count"`
	EngineLayer0Reject   bool    `json:"engine_layer0_reject"`
	ShadowMissingCount   int     `json:"shadow_missing_count"`
	ShadowWindowComplete bool    `json:"shadow_window_complete"`
}

type Checkpoint struct {
	CapturedAt         time.Time      `json:"captured_at"`
	AgeMinutes         float64        `json:"age_minutes"`
	Posture            string         `json:"posture"`
	Decision           string         `json:"decision"`
	SignalState        string         `json:"signal_state"`
	OperatorVerdict    string         `json:"operator_verdict"`
	ConfidenceScore    float64        `json:"confidence_score"`
	PriorityLabel      string         `json:"priority_label"`
	ActionabilityLabel string         `json:"actionability_label"`
	IsActionable       bool           `json:"is_actionable"`
	QualityTier        string         `json:"quality_tier"`
	FeatureSummary     FeatureSummary `json:"feature_summary"`
}

type Record struct {
	Mint        string    `json:"mint"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	SnapshotAt5m  *Checkpoint `json:"snapshot_at_5m,omitempty"`
	SnapshotAt15m *Checkpoint `json:"snapshot_at_15m,omitempty"`
	SnapshotAt30m *Checkpoint `json:"snapshot_at_30m,omitempty"`

	ShadowEligibleForScore      bool     `json:"shadow_eligible_for_score"`
	ShadowFeatureWindowComplete bool     `json:"shadow_feature_window_complete"`
	ShadowValidatedTradeable30m *bool    `json:"shadow_validated_tradeable_30m,omitempty"`
	ShadowValidatedClean30m     *bool    `json:"shadow_validated_clean_30m,omitempty"`
	ShadowOpportunityScore      *float64 `json:"shadow_opportunity_score,omitempty"`
	ShadowComparedAt            int64    `json:"shadow_compared_at,omitempty"`
	ShadowMissingFields         []string `json:"shadow_missing_fields,omitempty"`
	ShadowNotes                 []string `json:"shadow_notes,omitempty"`
}

type Recorder struct {
	mu      sync.Mutex
	records map[string]*Record
}

func NewRecorder() *Recorder {
	return &Recorder{records: make(map[string]*Record)}
}

func (r *Recorder) ObserveRows(rows []model.LiveSnapshot, now time.Time) {
	if r == nil {
		return
	}
	for _, row := range rows {
		r.Observe(row, now)
	}
}

func (r *Recorder) Observe(row model.LiveSnapshot, now time.Time) {
	if r == nil || row.Mint == "" {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	rec := r.records[row.Mint]
	if rec == nil {
		rec = &Record{Mint: row.Mint, FirstSeenAt: row.FirstSeenAt}
		r.records[row.Mint] = rec
	}
	if rec.FirstSeenAt.IsZero() && !row.FirstSeenAt.IsZero() {
		rec.FirstSeenAt = row.FirstSeenAt
	}
	rec.UpdatedAt = now

	cp := buildCheckpoint(row, now)
	switch checkpointSlot(row, now) {
	case "5m":
		if rec.SnapshotAt5m == nil {
			rec.SnapshotAt5m = cp
		}
	case "15m":
		if rec.SnapshotAt15m == nil {
			rec.SnapshotAt15m = cp
		}
	case "30m":
		if rec.SnapshotAt30m == nil {
			rec.SnapshotAt30m = cp
		}
	}

	rec.attachShadow(row.Shadow)
}

func (r *Recorder) Samples(limit int) []Record {
	if r == nil {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		if rec.emittable() {
			out = append(out, rec.clone())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func checkpointSlot(row model.LiveSnapshot, now time.Time) string {
	age := time.Duration(row.AgeSeconds * float64(time.Second))
	if !row.FirstSeenAt.IsZero() {
		age = now.Sub(row.FirstSeenAt)
	}
	switch {
	case age >= checkpoint5m && age < checkpoint15m:
		return "5m"
	case age >= checkpoint15m && age < checkpoint30m:
		return "15m"
	case age >= checkpoint30m && age < checkpoint35m:
		return "30m"
	default:
		return ""
	}
}

func buildCheckpoint(row model.LiveSnapshot, now time.Time) *Checkpoint {
	ageMin := row.AgeSeconds / 60
	if !row.FirstSeenAt.IsZero() {
		ageMin = now.Sub(row.FirstSeenAt).Minutes()
	}
	return &Checkpoint{
		CapturedAt:         now,
		AgeMinutes:         ageMin,
		Posture:            row.QualityTier,
		Decision:           row.Decision,
		SignalState:        row.SignalState,
		OperatorVerdict:    row.OperatorVerdict,
		ConfidenceScore:    row.ConfidenceScore,
		PriorityLabel:      row.PriorityLabel,
		ActionabilityLabel: row.ActionabilityLabel,
		IsActionable:       row.IsActionable,
		QualityTier:        row.QualityTier,
		FeatureSummary: FeatureSummary{
			UniqueBuyerCount:     row.UniqueBuyerCount,
			TotalEventCount:      row.TotalEventCount,
			BuyersLast1m:         row.BuyersLast1m,
			BuyersLast5m:         row.BuyersLast5m,
			BuyerAcceleration:    row.BuyerAcceleration,
			EffectiveBuyers1m:    row.EffectiveBuyers1m,
			EffectiveBuyers5m:    row.EffectiveBuyers5m,
			TotalBuySOL:          row.TotalBuySOL,
			TotalSellSOL:         row.TotalSellSOL,
			BuySolLast1m:         row.BuySolLast1m,
			SellSolLast1m:        row.SellSolLast1m,
			SellTradeCount:       row.SellTradeCount,
			LiquidityProxySOL:    row.LiquidityProxySOL,
			ExecutionPenalty:     row.ExecutionPenalty,
			EstimatedImpactPct:   row.EstimatedImpactPct,
			AdversarialScore:     row.AdversarialScore,
			FundingClusterRatio:  row.FundingClusterRatio,
			ClusterCompression1m: row.ClusterCompressionRatio1m,
			ClusterCompression5m: row.ClusterCompressionRatio5m,
			ClusteringRowStatus:  row.ClusteringRowStatus,
			TopWalletBuyShare5m:  row.TopWalletBuyShareLast5m,
			WalletDiversityRatio: row.WalletDiversityRatio,
			RepeatBuyerShare1m:   row.RepeatBuyerShare1m,
			Top10HolderPct:       row.Top10HolderPct,
			HolderCount:          row.HolderCount,
			Volume24hSOL:         row.Volume24hSOL,
			LiquidityPoolSOL:     row.LiquidityPoolSOL,
			MarketCapSOL:         row.MarketCapSOL,
			LastPriceSOL:         row.LastPriceSOL,
			EngineMaxLabel:       row.Engine.MaxLabel,
			EngineGatesPassCount: row.Engine.GatesPassCount,
			EngineLayer0Reject:   row.Engine.Layer0Reject,
			ShadowMissingCount:   len(row.Shadow.MissingFields),
			ShadowWindowComplete: row.Shadow.FeatureWindowComplete,
		},
	}
}

func (r *Record) attachShadow(shadow model.ShadowScoreResult) {
	if !shadowHasOutcome(shadow) && r.hasShadowOutcome() {
		return
	}
	r.ShadowEligibleForScore = shadow.EligibleForShadowScore
	r.ShadowFeatureWindowComplete = shadow.FeatureWindowComplete
	r.ShadowComparedAt = shadow.ComparedAt
	r.ShadowValidatedTradeable30m = cloneBool(shadow.ValidatedTradeable30m)
	r.ShadowValidatedClean30m = cloneBool(shadow.ValidatedClean30m)
	r.ShadowOpportunityScore = cloneFloat(shadow.OpportunityScore)
	r.ShadowMissingFields = append([]string(nil), shadow.MissingFields...)
	r.ShadowNotes = append([]string(nil), shadow.Notes...)
}

func (r Record) emittable() bool {
	hasCheckpoint := r.SnapshotAt5m != nil || r.SnapshotAt15m != nil || r.SnapshotAt30m != nil
	return hasCheckpoint && r.hasShadowOutcome()
}

func (r Record) hasShadowOutcome() bool {
	return r.ShadowEligibleForScore &&
		(r.ShadowValidatedTradeable30m != nil || r.ShadowValidatedClean30m != nil || r.ShadowOpportunityScore != nil)
}

func shadowHasOutcome(shadow model.ShadowScoreResult) bool {
	return shadow.EligibleForShadowScore &&
		(shadow.ValidatedTradeable30m != nil || shadow.ValidatedClean30m != nil || shadow.OpportunityScore != nil)
}

func (r Record) clone() Record {
	r.SnapshotAt5m = cloneCheckpoint(r.SnapshotAt5m)
	r.SnapshotAt15m = cloneCheckpoint(r.SnapshotAt15m)
	r.SnapshotAt30m = cloneCheckpoint(r.SnapshotAt30m)
	r.ShadowValidatedTradeable30m = cloneBool(r.ShadowValidatedTradeable30m)
	r.ShadowValidatedClean30m = cloneBool(r.ShadowValidatedClean30m)
	r.ShadowOpportunityScore = cloneFloat(r.ShadowOpportunityScore)
	r.ShadowMissingFields = append([]string(nil), r.ShadowMissingFields...)
	r.ShadowNotes = append([]string(nil), r.ShadowNotes...)
	return r
}

func cloneCheckpoint(cp *Checkpoint) *Checkpoint {
	if cp == nil {
		return nil
	}
	next := *cp
	return &next
}

func cloneBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	next := *v
	return &next
}

func cloneFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	next := *v
	return &next
}
