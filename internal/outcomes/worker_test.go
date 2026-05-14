package outcomes

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

type fakePricer struct {
	point PricePoint
	max   float64
	err   error
}

func (p fakePricer) At(context.Context, string, time.Time) (PricePoint, error) {
	return p.point, p.err
}

func (p fakePricer) MaxBetween(context.Context, string, time.Time, time.Time) (float64, error) {
	return p.max, nil
}

func TestWorkerProcessesFiveMinuteCheckpoint(t *testing.T) {
	db := openTestDB(t)
	id := insertOutcomeTestSnapshot(t, db, 6*time.Minute, 1.0)

	worker := NewWorker(db, fakePricer{
		point: PricePoint{PriceSol: 1.25, DepthSol: 100, Source: "observed_trade", Reliable: false},
		max:   1.25,
	}, 1)
	if err := worker.processDue(context.Background()); err != nil {
		t.Fatalf("processDue: %v", err)
	}
	assertCheckpointCount(t, db, id, 1)
	assertCheckpointStatus(t, db, id, 300, "measured")
}

func TestWorkerProcessesAllDueCheckpoints(t *testing.T) {
	db := openTestDB(t)
	id := insertOutcomeTestSnapshot(t, db, 35*time.Minute, 1.0)

	worker := NewWorker(db, fakePricer{
		point: PricePoint{PriceSol: 2.1, DepthSol: 100, Source: "observed_trade", Reliable: false},
		max:   2.1,
	}, 1)
	if err := worker.processDue(context.Background()); err != nil {
		t.Fatalf("processDue: %v", err)
	}
	assertCheckpointCount(t, db, id, 3)
	for _, cp := range []int{300, 900, 1800} {
		assertCheckpointStatus(t, db, id, cp, "measured")
	}
}

func TestWorkerDoesNotDuplicateCheckpointRows(t *testing.T) {
	db := openTestDB(t)
	id := insertOutcomeTestSnapshot(t, db, 35*time.Minute, 1.0)

	worker := NewWorker(db, fakePricer{
		point: PricePoint{PriceSol: 1.1, DepthSol: 100, Source: "observed_trade", Reliable: false},
		max:   1.1,
	}, 1)
	if err := worker.processDue(context.Background()); err != nil {
		t.Fatalf("first processDue: %v", err)
	}
	if err := worker.processDue(context.Background()); err != nil {
		t.Fatalf("second processDue: %v", err)
	}
	assertCheckpointCount(t, db, id, 3)
}

func TestWorkerMissingPriceInsertsUnavailable(t *testing.T) {
	db := openTestDB(t)
	id := insertOutcomeTestSnapshot(t, db, 6*time.Minute, 0)

	worker := NewWorker(db, fakePricer{
		point: PricePoint{PriceSol: 1.2, DepthSol: 100, Source: "observed_trade", Reliable: false},
		max:   1.2,
	}, 1)
	if err := worker.processDue(context.Background()); err != nil {
		t.Fatalf("processDue: %v", err)
	}
	assertCheckpointStatus(t, db, id, 300, "unavailable")
}

func TestWorkerRunNilSafe(t *testing.T) {
	done := make(chan struct{})
	go func() {
		var worker *Worker
		worker.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nil worker Run blocked")
	}
}

func insertOutcomeTestSnapshot(t *testing.T, db *sql.DB, age time.Duration, priceAtSignal float64) int64 {
	t.Helper()
	mint := fmt.Sprintf("TEST_WORKER_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM signal_snapshots WHERE mint = $1`, mint)
	})
	var id int64
	err := db.QueryRow(`
		INSERT INTO signal_snapshots (
		  mint, signaled_at, bucket_5m, classification_version,
		  setup_mode, blocker_severity, action, blockers, blocker_signature, proxy_score,
		  age_at_signal_s, holder_count, top10_pct, clustering_mode, catalyst,
		  real_depth_sol, liq_source, sol_per_trade_5m,
		  price_sol_at_signal, price_source, price_reliable, pool_depth_at_signal,
		  raw_snapshot
		) VALUES (
		  $1, now() - $2::interval, date_trunc('minute', now()), $3,
		  'AVOID', 'avoid', 'NO_TRADE', '[]'::jsonb, '', 50,
		  120, 10, 0.5, 'resolved', 'revival',
		  -1, 'observed_swaps_proxy', 0.1,
		  $4, 'observed_trade', $5, -1,
		  '{}'::jsonb
		)
		RETURNING id
	`, mint, fmt.Sprintf("%f seconds", age.Seconds()), ClassificationVersion, priceAtSignal, priceAtSignal > 0).Scan(&id)
	if err != nil {
		t.Fatalf("insert signal snapshot: %v", err)
	}
	return id
}

func assertCheckpointCount(t *testing.T, db *sql.DB, signalID int64, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM signal_outcomes WHERE signal_id = $1`, signalID).Scan(&got); err != nil {
		t.Fatalf("count checkpoints: %v", err)
	}
	if got != want {
		t.Fatalf("checkpoint count=%d, want %d", got, want)
	}
}

func assertCheckpointStatus(t *testing.T, db *sql.DB, signalID int64, checkpoint int, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`
		SELECT measurement_status
		FROM signal_outcomes
		WHERE signal_id = $1 AND checkpoint_s = $2
	`, signalID, checkpoint).Scan(&got); err != nil {
		t.Fatalf("checkpoint status: %v", err)
	}
	if got != want {
		t.Fatalf("checkpoint %d status=%q, want %q", checkpoint, got, want)
	}
}
