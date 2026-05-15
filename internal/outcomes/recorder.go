package outcomes

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"memecoin_scorer/internal/model"
)

// ClassificationVersion is the contract that the analysis layer treats as a
// hard cohort boundary. Bump this string whenever you change WOW / blocker
// logic so old and new classifier results are not mixed in v_outcomes_summary.
const ClassificationVersion = "wow_v2_phase1_failure_reason_precision"

type Snapshot struct {
	Mint              string
	SignaledAt        time.Time
	SetupMode         string
	BlockerSeverity   string
	Action            string
	Blockers          []string
	ProxyScore        float64
	AgeAtSignalS      int
	HolderCount       int
	Top10Pct          float64
	ClusteringMode    string
	Catalyst          string
	RealDepthSol      float64
	LiqSource         string
	SolPerTrade5m     float64
	PriceSolAtSignal  float64
	PriceSource       string // raydium_reserves | observed_trade | unavailable
	PriceReliable     bool
	PoolDepthAtSignal float64
	Raw               any // full live-snapshot row; sanitized before storage
}

type Recorder struct{ db *sql.DB }

func NewRecorder(db *sql.DB) *Recorder { return &Recorder{db: db} }

// RecordSignalSnapshot converts a live classifier row into the durable outcome
// snapshot shape and inserts it through the deduped recorder path.
func (r *Recorder) RecordSignalSnapshot(ctx context.Context, snapshot model.LiveSnapshot) (id int64, inserted bool, err error) {
	if r == nil || r.db == nil {
		return 0, false, nil
	}
	proxyScore := snapshot.Setup.ProxyScore
	if proxyScore == 0 {
		proxyScore = snapshot.EarlyProxy.Score
	}
	priceSource := "unavailable"
	priceReliable := false
	if snapshot.LastPriceSOL > 0 {
		priceSource = "observed_trade"
		priceReliable = true
	}
	return r.Record(ctx, Snapshot{
		Mint:              snapshot.Mint,
		SignaledAt:        time.Now().UTC(),
		SetupMode:         string(snapshot.Setup.Mode),
		BlockerSeverity:   snapshot.Setup.BlockerSeverity,
		Action:            string(snapshot.Setup.Action),
		Blockers:          snapshot.Setup.Blockers,
		ProxyScore:        proxyScore,
		AgeAtSignalS:      int(snapshot.AgeSeconds),
		HolderCount:       snapshot.HolderCount,
		Top10Pct:          snapshot.Top10HolderPct,
		ClusteringMode:    snapshot.ClusteringRowStatus,
		Catalyst:          snapshot.Catalyst.Status,
		RealDepthSol:      snapshot.RealPoolDepthSOL,
		LiqSource:         snapshot.LiquidityEvidenceSource,
		SolPerTrade5m:     snapshot.SolPerTrade5m,
		PriceSolAtSignal:  snapshot.LastPriceSOL,
		PriceSource:       priceSource,
		PriceReliable:     priceReliable,
		PoolDepthAtSignal: snapshot.RealPoolDepthSOL,
		Raw:               snapshot,
	})
}

// BlockerSignature is the canonical, dedup-friendly key for a blocker set.
func BlockerSignature(blockers []string) string {
	cp := append([]string(nil), blockers...)
	sort.Strings(cp)
	return strings.Join(cp, "|")
}

// rawAllowedFields is the whitelist of classifier-relevant fields kept in
// raw_snapshot. Anything not listed is dropped to keep snapshot rows small.
// Add fields here when the classifier starts consuming them. Event-history
// arrays (trades, swaps, holder changes) intentionally stay off.
var rawAllowedFields = map[string]struct{}{
	"mint":                         {},
	"setup":                        {},
	"real_pool_depth_sol":          {},
	"liquidity_evidence_source":    {},
	"sol_per_trade_5m":             {},
	"holder_count":                 {},
	"top10_pct":                    {},
	"clustering_mode":              {},
	"clustering_backend":           {},
	"catalyst":                     {},
	"proxy_score":                  {},
	"age_seconds":                  {},
	"pool_address":                 {},
	"creator":                      {},
	"deployer_score":               {},
	"bundle_fingerprint":           {},
	"first_minute_share":           {},
	"sniper_intensity_ratio":       {},
	"launch_confidence":            {},
	"launch_evidence_source":       {},
	"launch_slot":                  {},
	"launch_time":                  {},
	"is_pump_fun":                  {},
	"bonding_curve_progress_pct":   {},
	"bonding_velocity_sol_per_min": {},
	"trades_to_reach_current_vsol": {},
	"graduation_proximity_pct":     {},
	"review_checklist":             {},
}

func sanitizeRaw(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	out := make(map[string]any, len(rawAllowedFields))
	for k := range rawAllowedFields {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Record inserts a snapshot. Returns (id, inserted, err). inserted=false means
// an equivalent row already exists for this (mint, mode, blocker_signature,
// 5-min bucket, version) and was deduped.
func (r *Recorder) Record(ctx context.Context, s Snapshot) (int64, bool, error) {
	if r == nil || r.db == nil {
		return 0, false, nil
	}
	bucket := s.SignaledAt.UTC().Truncate(5 * time.Minute)
	sig := BlockerSignature(s.Blockers)
	blockersJSON, _ := json.Marshal(s.Blockers)
	rawJSON, _ := json.Marshal(sanitizeRaw(s.Raw))

	const q = `
		INSERT INTO signal_snapshots (
		  mint, signaled_at, bucket_5m, classification_version,
		  setup_mode, blocker_severity, action, blockers, blocker_signature, proxy_score,
		  age_at_signal_s, holder_count, top10_pct, clustering_mode, catalyst,
		  real_depth_sol, liq_source, sol_per_trade_5m,
		  price_sol_at_signal, price_source, price_reliable, pool_depth_at_signal,
		  raw_snapshot
		) VALUES (
		  $1,$2,$3,$4, $5,$6,$7,$8,$9,$10,
		  $11,$12,$13,$14,$15, $16,$17,$18,
		  $19,$20,$21,$22, $23
		)
		ON CONFLICT (mint, setup_mode, blocker_signature, bucket_5m, classification_version)
		DO NOTHING
		RETURNING id
	`
	var id int64
	err := r.db.QueryRowContext(ctx, q,
		s.Mint, s.SignaledAt, bucket, ClassificationVersion,
		s.SetupMode, s.BlockerSeverity, s.Action, blockersJSON, sig, s.ProxyScore,
		s.AgeAtSignalS, s.HolderCount, s.Top10Pct, s.ClusteringMode, s.Catalyst,
		s.RealDepthSol, s.LiqSource, s.SolPerTrade5m,
		s.PriceSolAtSignal, s.PriceSource, s.PriceReliable, s.PoolDepthAtSignal,
		rawJSON,
	).Scan(&id)

	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
