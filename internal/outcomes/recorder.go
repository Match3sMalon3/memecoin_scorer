package outcomes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"

	"memecoin_scorer/internal/model"
)

const (
	HistoricalPrecision     = 0.89
	HistoricalN             = 30847
	SuccessDefinition       = "MfeMultiple30m >= 1.20"
	mfeHitThreshold         = 1.20
	defaultMFEPollInterval  = time.Minute
	defaultMFETrackDuration = 30 * time.Minute
)

type PriceFetcher func(ctx context.Context, mint string) (float64, bool)

var fetchPrice PriceFetcher = fetchJupiterPrice

func openDB() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://localhost:5432/meme_trading_system_v1?sslmode=disable"
	}
	return sql.Open("postgres", dsn)
}

func EnsureTables() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(context.Background(), `
CREATE TABLE IF NOT EXISTS outcome_signals (
    id                  SERIAL PRIMARY KEY,
    mint                TEXT NOT NULL,
    symbol              TEXT,
    signal_time         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    runner_score        FLOAT NOT NULL,
    price_at_signal_sol FLOAT,
    price_5m_sol        FLOAT,
    price_15m_sol       FLOAT,
    price_30m_sol       FLOAT,
    price_60m_sol       FLOAT,
    return_5m_pct       FLOAT,
    return_15m_pct      FLOAT,
    return_30m_pct      FLOAT,
    return_60m_pct      FLOAT,
    max_runup_pct       FLOAT,
    max_drawdown_pct    FLOAT,
    mfe_30m_multiple    FLOAT,
    max_price_0_30m_sol FLOAT,
    outcome             TEXT DEFAULT 'pending',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE outcome_signals ADD COLUMN IF NOT EXISTS mfe_30m_multiple FLOAT;
ALTER TABLE outcome_signals ADD COLUMN IF NOT EXISTS max_price_0_30m_sol FLOAT;
UPDATE outcome_signals
SET outcome = CASE
    WHEN outcome IN ('hit_1_5x','hit_2x') THEN 'hit'
    WHEN outcome IN ('died','rugged') THEN 'miss'
    ELSE outcome
END
WHERE outcome IN ('hit_1_5x','hit_2x','died','rugged');
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'outcome_signals_outcome_check'
    ) THEN
        ALTER TABLE outcome_signals
            ADD CONSTRAINT outcome_signals_outcome_check
            CHECK (outcome IN ('pending','hit','miss'));
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_outcome_signals_signal_time
    ON outcome_signals (signal_time DESC);
CREATE INDEX IF NOT EXISTS idx_outcome_signals_outcome
    ON outcome_signals (outcome);
CREATE TABLE IF NOT EXISTS deployer_history (
    deployer_address  TEXT NOT NULL,
    mint              TEXT NOT NULL,
    launched_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    outcome           TEXT DEFAULT 'pending',
    PRIMARY KEY (deployer_address, mint)
);
CREATE INDEX IF NOT EXISTS idx_deployer_history_addr
    ON deployer_history (deployer_address);
`)
	return err
}

func RecordSignal(snap model.LiveSnapshot, score float64) error {
	_ = EnsureTables()
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	price := snap.LastPriceSol
	if price == 0 {
		price = snap.LastPriceSOL
	}
	var id int64
	err = db.QueryRowContext(context.Background(), `
		INSERT INTO outcome_signals (
			mint, signal_time, runner_score, price_at_signal_sol,
			max_price_0_30m_sol, mfe_30m_multiple, outcome
		)
		VALUES ($1, NOW(), $2, NULLIF($3,0), NULLIF($3,0), CASE WHEN $3 > 0 THEN 1 ELSE NULL END, 'pending')
		RETURNING id
	`, snap.Mint, score, price).Scan(&id)
	if err == nil {
		go trackMFE30(context.Background(), id, snap.Mint, defaultMFEPollInterval, defaultMFETrackDuration, fetchPrice)
		go scheduleTelemetrySnapshots(context.Background(), snap.Mint)
	}
	return err
}

func scheduleTelemetrySnapshots(ctx context.Context, mint string) {
	for _, window := range []int{5, 15, 30, 60} {
		window := window
		go func() {
			timer := time.NewTimer(time.Duration(window) * time.Minute)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
				_ = UpdateOutcome(mint, window)
			}
		}()
	}
}

func UpdateOutcome(mint string, windowMinutes int) error {
	_ = EnsureTables()
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	column := map[int]string{5: "price_5m_sol", 15: "price_15m_sol", 30: "price_30m_sol", 60: "price_60m_sol"}[windowMinutes]
	ret := map[int]string{5: "return_5m_pct", 15: "return_15m_pct", 30: "return_30m_pct", 60: "return_60m_pct"}[windowMinutes]
	if column == "" {
		return nil
	}
	price, ok := fetchPrice(context.Background(), mint)
	if !ok || price <= 0 {
		_, err = db.ExecContext(context.Background(), "UPDATE outcome_signals SET "+column+" = price_at_signal_sol, "+ret+" = 0 WHERE mint = $1 AND outcome = 'pending'", mint)
	} else {
		_, err = db.ExecContext(context.Background(), "UPDATE outcome_signals SET "+column+" = $2, "+ret+" = CASE WHEN price_at_signal_sol > 0 THEN (($2 / price_at_signal_sol) - 1) * 100 ELSE NULL END WHERE mint = $1 AND outcome = 'pending'", mint, price)
	}
	if err != nil {
		return err
	}
	if windowMinutes == 30 {
		_, err = db.ExecContext(context.Background(), `
			UPDATE outcome_signals
			SET outcome = CASE
				WHEN COALESCE(mfe_30m_multiple, 0) >= $2 THEN 'hit'
				ELSE 'miss'
			END
			WHERE mint = $1 AND outcome = 'pending'
		`, mint, mfeHitThreshold)
	}
	return err
}

func trackMFE30(ctx context.Context, signalID int64, mint string, interval, duration time.Duration, fetcher PriceFetcher) {
	if interval <= 0 || duration <= 0 || fetcher == nil {
		return
	}
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if price, ok := fetcher(ctx, mint); ok && price > 0 {
				_ = recordObservedPrice(ctx, signalID, price)
			}
		case <-deadline.C:
			_ = finalizeSignalOutcome(ctx, signalID)
			return
		}
	}
}

func recordObservedPrice(ctx context.Context, signalID int64, price float64) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `
		UPDATE outcome_signals
		SET max_price_0_30m_sol = GREATEST(COALESCE(max_price_0_30m_sol, 0), $2),
		    mfe_30m_multiple = CASE
		        WHEN price_at_signal_sol > 0
		        THEN GREATEST(COALESCE(max_price_0_30m_sol, 0), $2) / price_at_signal_sol
		        ELSE NULL
		    END
		WHERE id = $1 AND outcome = 'pending'
	`, signalID, price)
	return err
}

func finalizeSignalOutcome(ctx context.Context, signalID int64) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `
		UPDATE outcome_signals
		SET outcome = CASE
			WHEN COALESCE(mfe_30m_multiple, 0) >= $2 THEN 'hit'
			ELSE 'miss'
		END
		WHERE id = $1 AND outcome = 'pending'
	`, signalID, mfeHitThreshold)
	return err
}

func OutcomeFromMFE(priceAtSignal float64, observedPrices []float64) (string, float64, float64) {
	maxPrice := 0.0
	for _, price := range observedPrices {
		if price > maxPrice {
			maxPrice = price
		}
	}
	if priceAtSignal <= 0 || maxPrice <= 0 {
		return "miss", 0, maxPrice
	}
	multiple := maxPrice / priceAtSignal
	if multiple >= mfeHitThreshold {
		return "hit", multiple, maxPrice
	}
	return "miss", multiple, maxPrice
}

func fetchJupiterPrice(ctx context.Context, mint string) (float64, bool) {
	if mint == "" {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://price.jup.ag/v6/price?ids="+mint, nil)
	if err != nil {
		return 0, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, false
	}
	var body struct {
		Data map[string]map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, false
	}
	item := body.Data[mint]
	if item == nil {
		return 0, false
	}
	switch v := item["price"].(type) {
	case float64:
		return v, v > 0
	case string:
		var price float64
		if _, err := fmt.Sscanf(v, "%f", &price); err == nil && price > 0 {
			return price, true
		}
	}
	return 0, false
}

func TotalSignals() (int, error) {
	_ = EnsureTables()
	db, err := openDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM outcome_signals`).Scan(&n)
	return n, err
}

func LivePrecision() (float64, int, int, error) {
	_ = EnsureTables()
	db, err := openDB()
	if err != nil {
		return 0, 0, 0, err
	}
	defer db.Close()
	var total, hits int
	err = db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FILTER (WHERE outcome <> 'pending'),
		       COUNT(*) FILTER (WHERE outcome = 'hit')
		FROM outcome_signals
	`).Scan(&total, &hits)
	if err != nil || total == 0 {
		return 0, total, hits, err
	}
	return float64(hits) / float64(total), total, hits, nil
}

func SignalsFiredToday() (int, error) {
	_ = EnsureTables()
	db, err := openDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM outcome_signals WHERE signal_time::date = CURRENT_DATE`).Scan(&n)
	return n, err
}

func BestScoreToday() (float64, error) {
	_ = EnsureTables()
	db, err := openDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v sql.NullFloat64
	err = db.QueryRowContext(context.Background(), `SELECT MAX(runner_score) FROM outcome_signals WHERE signal_time::date = CURRENT_DATE`).Scan(&v)
	if !v.Valid {
		return 0, err
	}
	return v.Float64, err
}

func TrackingSince() string { return time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC).Format("2006-01-02") }
