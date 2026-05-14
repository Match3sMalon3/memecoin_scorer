package outcomes

import (
	"context"
	"database/sql"
	"time"
)

type SQLSwapStore struct {
	db *sql.DB
}

func NewSQLSwapStore(db *sql.DB) *SQLSwapStore {
	return &SQLSwapStore{db: db}
}

func (s *SQLSwapStore) LastPriceBefore(ctx context.Context, mint string, t time.Time) (float64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, nil
	}
	var price float64
	err := s.db.QueryRowContext(ctx, `
		SELECT sol_amount / token_amount
		FROM swap_events
		WHERE mint = $1
		  AND block_time <= $2
		  AND sol_amount > 0
		  AND token_amount > 0
		ORDER BY block_time DESC
		LIMIT 1
	`, mint, t).Scan(&price)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return price, price > 0, nil
}

func (s *SQLSwapStore) MaxPriceBetween(ctx context.Context, mint string, from, to time.Time) (float64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var price sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(sol_amount / token_amount)
		FROM swap_events
		WHERE mint = $1
		  AND block_time > $2
		  AND block_time <= $3
		  AND sol_amount > 0
		  AND token_amount > 0
	`, mint, from, to).Scan(&price)
	if err != nil {
		return 0, err
	}
	if !price.Valid {
		return 0, nil
	}
	return price.Float64, nil
}
