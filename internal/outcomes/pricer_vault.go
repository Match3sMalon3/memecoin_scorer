package outcomes

import (
	"context"
	"fmt"
	"time"
)

// VaultReader is satisfied by your existing Raydium WSOL vault reader — the
// same code path that produces liquidity_evidence_source=raydium_wsol_vault
// in /api/live-snapshots. Implement this interface against your reader.
type VaultReader interface {
	// ReadAt returns SOL-denominated price and pool depth (SOL) for the given
	// mint at approximately time t. If reserves cannot be read, return ok=false.
	ReadAt(ctx context.Context, mint string, t time.Time) (priceSol, depthSol float64, ok bool, err error)
}

// SwapStore is satisfied by whatever holds your observed swap stream.
type SwapStore interface {
	// LastPriceBefore returns the SOL-denominated price of the most recent
	// observed swap at or before t. Returns ok=false if no swaps exist.
	LastPriceBefore(ctx context.Context, mint string, t time.Time) (priceSol float64, ok bool, err error)
	// MaxPriceBetween returns the highest observed price in (from, to].
	MaxPriceBetween(ctx context.Context, mint string, from, to time.Time) (priceSol float64, err error)
}

type VaultPricer struct {
	Vault VaultReader
	Swaps SwapStore
}

func NewVaultPricer(v VaultReader, s SwapStore) *VaultPricer {
	return &VaultPricer{Vault: v, Swaps: s}
}

func (p *VaultPricer) At(ctx context.Context, mint string, t time.Time) (PricePoint, error) {
	if p.Vault != nil {
		price, depth, ok, err := p.Vault.ReadAt(ctx, mint, t)
		if err == nil && ok && price > 0 {
			return PricePoint{
				PriceSol: price,
				DepthSol: depth,
				Source:   "raydium_reserves",
				Reliable: true,
			}, nil
		}
	}
	if p.Swaps != nil {
		price, ok, err := p.Swaps.LastPriceBefore(ctx, mint, t)
		if err == nil && ok && price > 0 {
			return PricePoint{
				PriceSol: price,
				DepthSol: -1,
				Source:   "observed_trade",
				Reliable: false,
			}, nil
		}
	}
	return PricePoint{Source: "unavailable", Reliable: false},
		fmt.Errorf("no price for %s at %s", mint, t.Format(time.RFC3339))
}

func (p *VaultPricer) MaxBetween(ctx context.Context, mint string, from, to time.Time) (float64, error) {
	if p.Swaps == nil {
		return 0, nil
	}
	return p.Swaps.MaxPriceBetween(ctx, mint, from, to)
}
