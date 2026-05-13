package outcomes

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"memecoin_scorer/internal/model"

	_ "github.com/lib/pq"
)

func TestRecordSignalSnapshotInsertsDedupesAndAllowsMissingPrice(t *testing.T) {
	db := openTestDB(t)
	mint := fmt.Sprintf("TEST_OUTCOME_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM signal_snapshots WHERE mint = $1`, mint)
	})

	recorder := NewRecorder(db)
	snapshot := model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			Mint:                    mint,
			AgeSeconds:              123,
			HolderCount:             7,
			Top10HolderPct:          0.91,
			RealPoolDepthSOL:        -1,
			LiquidityEvidenceSource: "observed_swaps_proxy",
			LastPriceSOL:            0,
		},
		TokenMode:           model.TokenModeRevival,
		ClusteringRowStatus: "partial_fallback",
		SolPerTrade5m:       0.42,
		EarlyProxy:          model.EarlyProxyScore{Score: 83},
		Setup: model.SetupResult{
			Mode:            model.SetupAvoid,
			Action:          model.ActionNoTrade,
			BlockerSeverity: "avoid",
			Blockers:        []string{"near-terminal holder concentration 91.0%", "partial clustering fallback"},
			ProxyScore:      83,
		},
	}

	id, inserted, err := recorder.RecordSignalSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("RecordSignalSnapshot insert: %v", err)
	}
	if !inserted || id == 0 {
		t.Fatalf("inserted=%v id=%d, want inserted row", inserted, id)
	}
	_, inserted, err = recorder.RecordSignalSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("RecordSignalSnapshot dedupe: %v", err)
	}
	if inserted {
		t.Fatal("second RecordSignalSnapshot inserted duplicate in same 5m bucket")
	}

	var count int
	var priceSource string
	var priceReliable bool
	if err := db.QueryRow(`
		SELECT COUNT(*), MAX(price_source), BOOL_OR(price_reliable)
		FROM signal_snapshots
		WHERE mint = $1
	`, mint).Scan(&count, &priceSource, &priceReliable); err != nil {
		t.Fatalf("query inserted snapshot: %v", err)
	}
	if count != 1 {
		t.Fatalf("count=%d, want deduped count 1", count)
	}
	if priceSource != "unavailable" || priceReliable {
		t.Fatalf("price_source=%q reliable=%v, want unavailable/false", priceSource, priceReliable)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		dsn = "postgres://localhost:5432/meme_trading_system_v1?sslmode=disable"
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skipf("postgres unavailable: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT 1 FROM signal_snapshots LIMIT 1`); err != nil {
		_ = db.Close()
		t.Skipf("signal_snapshots unavailable: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
