// Package db provides PostgreSQL persistence for the memecoin scorer.
// All writes are non-blocking best-effort: errors are logged but never
// propagate to the live decision path so that a DB outage cannot take
// down the ingestor.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"

	"memecoin_scorer/internal/model"
)

// Store wraps a *sql.DB and exposes write methods for the live pipeline.
type Store struct {
	db *sql.DB
}

// Open connects to the database using DATABASE_URL and verifies connectivity.
// Returns (nil, nil) when DATABASE_URL is not set so the caller can treat
// DB persistence as optional.
func Open() (*Store, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, nil
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("db.Open: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db.Open ping: %w", err)
	}
	log.Printf("db: connected to postgres (%s)", maskDSN(dsn))
	return &Store{db: db}, nil
}

// Close closes the underlying connection pool.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// SQLDB exposes the underlying connection pool for packages that need to share
// the optional application database without opening a second pool.
func (s *Store) SQLDB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// UpsertToken inserts or updates the token row for mint.
// Uses ON CONFLICT DO UPDATE so it is safe to call on every snapshot cycle.
func (s *Store) UpsertToken(ctx context.Context, snap model.TokenSnapshot) {
	if s == nil {
		return
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tokens (
			mint, first_seen_at, last_event_at,
			total_buy_sol, total_sell_sol, sell_trade_count, total_event_count,
			unique_buyer_count, liquidity_pool_sol, market_cap_sol,
			last_price_sol, top10_holder_pct, volume_24h_sol,
			organic_winner_count, holders_at_30m, holders_at_60m,
			holder_count, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,NOW())
		ON CONFLICT (mint) DO UPDATE SET
			last_event_at        = EXCLUDED.last_event_at,
			total_buy_sol        = EXCLUDED.total_buy_sol,
			total_sell_sol       = EXCLUDED.total_sell_sol,
			sell_trade_count     = EXCLUDED.sell_trade_count,
			total_event_count    = EXCLUDED.total_event_count,
			unique_buyer_count   = EXCLUDED.unique_buyer_count,
			liquidity_pool_sol   = EXCLUDED.liquidity_pool_sol,
			market_cap_sol       = EXCLUDED.market_cap_sol,
			last_price_sol       = EXCLUDED.last_price_sol,
			top10_holder_pct     = EXCLUDED.top10_holder_pct,
			volume_24h_sol       = EXCLUDED.volume_24h_sol,
			organic_winner_count = EXCLUDED.organic_winner_count,
			holders_at_30m       = EXCLUDED.holders_at_30m,
			holders_at_60m       = EXCLUDED.holders_at_60m,
			holder_count         = EXCLUDED.holder_count,
			updated_at           = NOW()
	`,
		snap.Mint, snap.FirstSeenAt, snap.LastEventAt,
		snap.TotalBuySOL, snap.TotalSellSOL, snap.SellTradeCount, snap.TotalEventCount,
		snap.UniqueBuyerCount, snap.LiquidityPoolSOL, snap.MarketCapSOL,
		snap.LastPriceSOL, snap.Top10HolderPct, snap.Volume24hSOL,
		snap.OrganicWinnerCount, snap.HoldersAt30m, snap.HoldersAt60m,
		snap.HolderCount,
	)
	if err != nil {
		log.Printf("db.UpsertToken %s: %v", snap.Mint, err)
	}
}

// InsertSwapEvent persists a raw swap event.  Duplicate signatures are silently
// ignored (ON CONFLICT DO NOTHING).
func (s *Store) InsertSwapEvent(ctx context.Context, ev model.SwapEvent) {
	if s == nil {
		return
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO swap_events (signature, slot, block_time, mint, is_buy, wallet_addr,
		                         sol_amount, token_amount, program_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (signature) DO NOTHING
	`,
		ev.Signature, ev.Slot, ev.BlockTime, ev.TokenMint, ev.IsBuy,
		ev.WalletAddr, ev.SOLAmount, ev.TokenAmount, ev.ProgramID,
	)
	if err != nil {
		log.Printf("db.InsertSwapEvent %s: %v", ev.Signature, err)
	}
}

// InsertSignal writes a scored snapshot to the signals table and the
// per-gate results to signal_gate_results.  Returns the new signal ID
// (0 on error).
func (s *Store) InsertSignal(ctx context.Context, scored model.ScoredSnapshot) int64 {
	if s == nil {
		return 0
	}

	// Ensure the token row exists first.
	s.UpsertToken(ctx, scored.TokenSnapshot)

	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO signals (
			mint, emitted_at, label, signal_state, is_actionable,
			confidence_score, warming_up,
			trade_size_sol, liquidity_proxy_sol, estimated_impact_pct,
			execution_penalty, adversarial_score,
			effective_buyers_1m, effective_buyers_5m, funding_cluster_ratio,
			why_now, why_not_higher, reasons,
			engine_layer0_reject, engine_layer0_reason, engine_max_label,
			engine_gates_pass_count, engine_score_cap
		) VALUES (
			$1, NOW(), $2, $3, $4,
			$5, $6,
			$7, $8, $9,
			$10, $11,
			$12, $13, $14,
			$15, $16, $17,
			$18, $19, $20,
			$21, $22
		) RETURNING id
	`,
		scored.Mint, scored.Decision, scored.SignalState, scored.IsActionable,
		scored.ConfidenceScore, scored.WarmingUp,
		scored.TradeSizeSOL, scored.LiquidityProxySOL, scored.EstimatedImpactPct,
		scored.ExecutionPenalty, scored.AdversarialScore,
		scored.EffectiveBuyers1m, scored.EffectiveBuyers5m, scored.FundingClusterRatio,
		scored.WhyNow, scored.WhyNotHigher, stringSlice(scored.Reasons),
		scored.Engine.Layer0Reject, scored.Engine.Layer0Reason, scored.Engine.MaxLabel,
		scored.Engine.GatesPassCount, scored.Engine.ScoreCap,
	).Scan(&id)
	if err != nil {
		log.Printf("db.InsertSignal %s: %v", scored.Mint, err)
		return 0
	}

	// Persist per-gate results for explainability.
	for _, g := range scored.Engine.Gates {
		_, gerr := s.db.ExecContext(ctx, `
			INSERT INTO signal_gate_results
				(signal_id, gate_id, gate_name, passed, skipped, value, threshold, margin, reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, id, g.ID, g.Name, g.Passed, g.Skipped, g.Value, g.Threshold, g.Margin, g.Reason)
		if gerr != nil {
			log.Printf("db.InsertSignal gate %d: %v", g.ID, gerr)
		}
	}
	return id
}

// ApplyMigrations runs schema.sql against the connected database.
// Idempotent: all DDL uses IF NOT EXISTS.
func (s *Store) ApplyMigrations(ctx context.Context, schemaSQL string) error {
	if s == nil {
		return fmt.Errorf("db: not connected (DATABASE_URL not set)")
	}
	_, err := s.db.ExecContext(ctx, schemaSQL)
	return err
}

// Ping verifies DB connectivity.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("db: not connected")
	}
	return s.db.PingContext(ctx)
}

// ---- helpers ----

func maskDSN(dsn string) string {
	// Redact password portion if present.
	for i, c := range dsn {
		if c == '@' {
			// Find start of user:pass
			for j := i - 1; j >= 0; j-- {
				if dsn[j] == ':' {
					return dsn[:j+1] + "***" + dsn[i:]
				}
			}
			break
		}
	}
	return dsn
}

// stringSlice wraps []string for pq array parameter.
type stringSlice []string

func (ss stringSlice) Value() (interface{}, error) {
	// pq can handle []string directly via pq.Array; we use that approach.
	return pqArray([]string(ss)), nil
}

// pqArray converts a string slice to a form postgres understands.
// We use github.com/lib/pq's Array helper via a local wrapper to avoid
// importing pq in the caller layer.
func pqArray(ss []string) interface{} {
	// lib/pq registers a type for []string as pq.Array —
	// but we want to avoid the extra import in callers.
	// We encode as a literal Postgres array string instead.
	if len(ss) == 0 {
		return "{}"
	}
	out := "{"
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += `"` + escapeStr(s) + `"`
	}
	out += "}"
	return out
}

func escapeStr(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			result = append(result, '\\')
		}
		result = append(result, s[i])
	}
	return string(result)
}
