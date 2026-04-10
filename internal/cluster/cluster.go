// Package cluster provides effective-buyer clustering for live signal scoring.
// It maps raw wallet addresses to their funding-parent clusters so that multiple
// wallets controlled by one actor count as one effective buyer.
//
// # Resolver types
//
//   - NullResolver     — every wallet is its own root; effective == raw; IsHealthy=false
//   - StaticResolver   — loaded from a JSON funder-map file; IsHealthy=true
//   - HeliusResolver   — real live backend using HELIUS_API_KEY; IsHealthy reflects API reachability
//
// # Clustering health and CLUSTER_REQUIRED
//
// The HealthyResolver extension interface exposes IsHealthy() and BackendName().
// When CLUSTER_REQUIRED=1 (default), the decision engine checks clustering health
// before allowing BUY or READY decisions.  If the active resolver is not healthy
// (i.e. NullResolver or a HeliusResolver whose API is unreachable), BUY and READY
// are clamped to WATCH/AVOID with an explicit reason.
package cluster

import (
	"context"
	"errors"
	"sync"
	"time"
)

// FunderResolver maps a wallet address to its funding parent (if known).
// Implementations must be safe for concurrent use.
type FunderResolver interface {
	// ResolveParent returns the canonical cluster root for wallet.
	// If no parent is known, return ("", false, nil) — the wallet is its own root.
	// Errors are treated as "unknown parent"; the wallet becomes its own root.
	ResolveParent(ctx context.Context, wallet string, asOf time.Time) (parent string, ok bool, err error)
}

// HealthyResolver is a FunderResolver that also reports operational health.
// The decision engine uses this to enforce CLUSTER_REQUIRED.
//
// Implementations:
//   - StaticResolver  — always healthy once loaded; BackendName="static"
//   - HeliusResolver  — healthy while Helius API responds; BackendName="helius"
//
// NullResolver does NOT implement HealthyResolver (intentionally — it is the
// "no backend configured" sentinel).  The decision engine treats any resolver
// that does not implement HealthyResolver as unhealthy.
type HealthyResolver interface {
	FunderResolver
	// IsHealthy returns true when the resolver is operational and capable of
	// returning meaningful parent data.
	IsHealthy() bool
	// BackendName returns a short human-readable backend identifier.
	BackendName() string
}

// NullResolver is the zero-value sentinel: every wallet is its own cluster root.
// It does NOT implement HealthyResolver; the decision engine treats it as unhealthy.
// Used only as a fallback when no backend is configured — never intentionally in live mode.
type NullResolver struct{}

func (NullResolver) ResolveParent(_ context.Context, wallet string, _ time.Time) (string, bool, error) {
	return wallet, false, nil
}

// Result is the output of Cluster for a set of buyer wallets.
type Result struct {
	// EffectiveUniqueBuyerCount is the number of distinct funding-cluster roots.
	EffectiveUniqueBuyerCount int
	// ClusteredBuyerCount is len(wallets) − EffectiveUniqueBuyerCount.
	ClusteredBuyerCount int
	// FundingClusterRatio is ClusteredBuyerCount / len(wallets).
	FundingClusterRatio float64
	// ResolverTimeoutCount is the number of wallets whose lookup timed out.
	ResolverTimeoutCount int
	// ResolverFallbackCount is the number of wallets that fell back to raw roots
	// because the resolver errored or timed out.
	ResolverFallbackCount int
}

// Stats captures resolver health and fallback observability.
type Stats struct {
	ConsecutiveFailures int64  `json:"consecutive_failures"`
	TimeoutFallbacks    int64  `json:"timeout_fallbacks"`
	ErrorFallbacks      int64  `json:"error_fallbacks"`
	LastError           string `json:"last_error,omitempty"`
}

// StatsResolver exposes runtime stats for observability.
type StatsResolver interface {
	Stats() Stats
}

// Cluster groups wallets by their funding parent and returns cluster statistics.
// Each wallet produces exactly one ResolveParent call (O(n) total).
// Resolver errors are treated as unknown parent (wallet becomes its own root).
func Cluster(ctx context.Context, wallets []string, resolver FunderResolver, asOf time.Time) Result {
	if len(wallets) == 0 {
		return Result{}
	}
	roots := make(map[string]struct{}, len(wallets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	timeouts := 0
	fallbacks := 0

	for _, wallet := range wallets {
		w := wallet
		wg.Add(1)
		go func() {
			defer wg.Done()

			if ctx.Err() != nil {
				mu.Lock()
				roots[w] = struct{}{}
				mu.Unlock()
				return
			}

			parent, ok, err := resolver.ResolveParent(ctx, w, asOf)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fallbacks++
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					timeouts++
				}
			}
			if err != nil || !ok || parent == "" {
				roots[w] = struct{}{}
				return
			}
			roots[parent] = struct{}{}
		}()
	}
	wg.Wait()
	eff := len(roots)
	collapsed := len(wallets) - eff
	if collapsed < 0 {
		collapsed = 0
	}
	return Result{
		EffectiveUniqueBuyerCount: eff,
		ClusteredBuyerCount:       collapsed,
		FundingClusterRatio:       float64(collapsed) / float64(len(wallets)),
		ResolverTimeoutCount:      timeouts,
		ResolverFallbackCount:     fallbacks,
	}
}

// IsResolverHealthy returns true when resolver implements HealthyResolver and reports healthy.
// Returns false for NullResolver and any resolver that does not implement HealthyResolver.
// Used by the decision engine to enforce CLUSTER_REQUIRED.
func IsResolverHealthy(r FunderResolver) bool {
	if hr, ok := r.(HealthyResolver); ok {
		return hr.IsHealthy()
	}
	return false // NullResolver and unknown types → unhealthy
}

// ResolverBackendName returns the backend name from resolver if it implements HealthyResolver,
// otherwise returns "null".
func ResolverBackendName(r FunderResolver) string {
	if hr, ok := r.(HealthyResolver); ok {
		return hr.BackendName()
	}
	return "null"
}

// ResolverStats returns runtime stats when the resolver exposes them.
func ResolverStats(r FunderResolver) Stats {
	if sr, ok := r.(StatsResolver); ok {
		return sr.Stats()
	}
	return Stats{}
}
