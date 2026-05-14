package outcomes

import (
	"context"
	"database/sql"
	"log"
	"time"
)

type Worker struct {
	db           *sql.DB
	pricer       Pricer
	tick         time.Duration
	minTrade     float64 // SOL — typical trade size used for is_tradeable
	maxImpactPct float64 // is_tradeable requires est. impact below this
	rugDepthSol  float64 // pool depth below this counts as rugged
}

func NewWorker(db *sql.DB, p Pricer, minTradeSol float64) *Worker {
	return &Worker{
		db:           db,
		pricer:       p,
		tick:         30 * time.Second,
		minTrade:     minTradeSol,
		maxImpactPct: 5.0,
		rugDepthSol:  0.1,
	}
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.db == nil {
		return
	}
	if err := w.processDue(ctx); err != nil {
		log.Printf("outcome worker: %v", err)
	}
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.processDue(ctx); err != nil {
				log.Printf("outcome worker: %v", err)
			}
		}
	}
}

func (w *Worker) processDue(ctx context.Context) error {
	if w == nil || w.db == nil {
		return nil
	}
	const q = `
		SELECT s.id, s.mint, s.signaled_at,
		       COALESCE(s.price_sol_at_signal, 0), s.price_reliable, c.cp
		FROM signal_snapshots s
		CROSS JOIN (VALUES (300),(900),(1800)) AS c(cp)
		LEFT JOIN signal_outcomes o ON o.signal_id = s.id AND o.checkpoint_s = c.cp
		WHERE o.signal_id IS NULL
		  AND s.signaled_at + (c.cp || ' seconds')::interval <= now()
		  AND s.signaled_at > now() - interval '24 hours'
		LIMIT 500
	`
	rows, err := w.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var mint string
		var signaledAt time.Time
		var pAtSig float64
		var anchorReliable bool
		var cp int
		if err := rows.Scan(&id, &mint, &signaledAt, &pAtSig, &anchorReliable, &cp); err != nil {
			return err
		}
		w.measure(ctx, id, mint, signaledAt, pAtSig, anchorReliable, cp)
	}
	return rows.Err()
}

func (w *Worker) measure(ctx context.Context, id int64, mint string, signaledAt time.Time,
	priceAtSignal float64, anchorReliable bool, cp int) {
	if w == nil || w.db == nil {
		return
	}
	if priceAtSignal <= 0 {
		w.insertUnavailable(ctx, id, cp, "missing signal price")
		return
	}
	if w.pricer == nil {
		w.insertUnavailable(ctx, id, cp, "price source unavailable")
		return
	}

	cpAt := signaledAt.Add(time.Duration(cp) * time.Second)
	pp, err := w.pricer.At(ctx, mint, cpAt)
	if err != nil {
		w.insertUnavailable(ctx, id, cp, err.Error())
		return
	}
	if pp.PriceSol <= 0 {
		w.insertUnavailable(ctx, id, cp, "price unavailable")
		return
	}
	maxP, _ := w.pricer.MaxBetween(ctx, mint, signaledAt, cpAt)
	if maxP <= 0 {
		maxP = pp.PriceSol
	}

	// Returns are decimal: 0.20 = +20% = 1.2x ; 1.00 = +100% = 2x.
	var retVS, maxRet *float64
	if priceAtSignal > 0 {
		r := pp.PriceSol/priceAtSignal - 1
		retVS = &r
		if maxP > 0 {
			m := maxP/priceAtSignal - 1
			maxRet = &m
		}
	}

	impactPct := 100.0
	if pp.DepthSol > 0 {
		impactPct = w.minTrade / pp.DepthSol * 100
	}
	isTradeable := pp.Reliable && pp.DepthSol > 0 && impactPct < w.maxImpactPct
	isRugged := pp.DepthSol >= 0 && pp.DepthSol < w.rugDepthSol
	is1_2xClean := isTradeable && maxRet != nil && *maxRet >= 0.20
	is2xClean := isTradeable && maxRet != nil && *maxRet >= 1.00

	const ins = `
		INSERT INTO signal_outcomes
		  (signal_id, checkpoint_s, measured_at, price_sol, price_source, price_reliable,
		   pool_depth_sol, return_vs_signal, max_return_so_far,
		   is_tradeable, is_1_2x_clean, is_2x_clean, is_rugged, measurement_status)
		VALUES ($1,$2,now(),$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'measured')
		ON CONFLICT DO NOTHING
	`
	if _, err := w.db.ExecContext(ctx, ins,
		id, cp, pp.PriceSol, pp.Source, pp.Reliable, pp.DepthSol,
		retVS, maxRet, isTradeable, is1_2xClean, is2xClean, isRugged); err != nil {
		log.Printf("outcome insert signal=%d cp=%d: %v", id, cp, err)
	}
	_ = anchorReliable
}

func (w *Worker) insertUnavailable(ctx context.Context, id int64, cp int, note string) {
	if w == nil || w.db == nil {
		return
	}
	const q = `
		INSERT INTO signal_outcomes (signal_id, checkpoint_s, measured_at, measurement_status, notes)
		VALUES ($1, $2, now(), 'unavailable', $3)
		ON CONFLICT DO NOTHING
	`
	_, _ = w.db.ExecContext(ctx, q, id, cp, note)
}
