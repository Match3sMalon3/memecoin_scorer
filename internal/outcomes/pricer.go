package outcomes

import (
	"context"
	"time"
)

type PricePoint struct {
	PriceSol float64
	DepthSol float64
	Source   string // raydium_reserves | observed_trade | unavailable
	Reliable bool   // true only when read directly from vault reserves
}

// Pricer abstracts the price source for outcome measurement.
// Implementations should:
//   - At: try Raydium vault reserves first; fall back to last observed
//     swap price only if reserves are unavailable. Mark Reliable accordingly.
//   - MaxBetween: walk the observed swap stream for (from, to] and return the
//     highest price seen. Returns 0 if no swaps fell in the window.
type Pricer interface {
	At(ctx context.Context, mint string, t time.Time) (PricePoint, error)
	MaxBetween(ctx context.Context, mint string, from, to time.Time) (float64, error)
}
