package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// HeliusResolver resolves wallet funding parents via the Helius enhanced-transaction API.
//
// Strategy: for each wallet, request its most recent TRANSFER transactions.  The
// sender of the oldest inbound native-SOL transfer within the lookback window is
// treated as the wallet's funder/parent.  Results are cached with configurable TTL.
//
// Conservative fallback: if the API call fails, times out, or returns no matching
// transfer, the wallet is its own root.  The resolver tracks consecutive failures
// and marks itself degraded after maxConsecFail failures so the decision engine
// can enforce CLUSTER_REQUIRED.
//
// Implements HealthyResolver.
type HeliusResolver struct {
	apiKey   string
	baseURL  string
	lookback time.Duration
	cache    *resolverCache
	sem      chan struct{} // concurrency cap
	client   *http.Client

	// failure tracking
	consecFail    int64 // atomic; resets on success
	maxConsecFail int64 // threshold before IsHealthy() → false
	timeoutFallbk int64 // atomic count of request-time deadline fallbacks
	errorFallbk   int64 // atomic count of non-timeout resolver fallbacks
	lastErrMsg    atomic.Value

	// in-flight dedup: prevents request stampede for same wallet
	inflightMu sync.Mutex
	inflight   map[string]*inflightEntry
}

type inflightEntry struct {
	wg     sync.WaitGroup
	parent string
	found  bool
}

// HeliusResolverConfig holds construction parameters.
type HeliusResolverConfig struct {
	APIKey         string
	LookbackHours  int // default 72
	CacheTTLMin    int // positive-cache TTL minutes; default 120
	MaxConcurrency int // default 8
	// BaseURL overrides the Helius API base. Only used in tests.
	BaseURL string
}

// NewHeliusResolver creates a HeliusResolver from config.
// Returns an error if APIKey is empty.
func NewHeliusResolver(cfg HeliusResolverConfig) (*HeliusResolver, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("cluster: HeliusResolver requires HELIUS_API_KEY")
	}
	if cfg.LookbackHours <= 0 {
		cfg.LookbackHours = 72
	}
	if cfg.CacheTTLMin <= 0 {
		cfg.CacheTTLMin = 120
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 8
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.helius.xyz"
	}
	sem := make(chan struct{}, cfg.MaxConcurrency)
	r := &HeliusResolver{
		apiKey:        cfg.APIKey,
		baseURL:       baseURL,
		lookback:      time.Duration(cfg.LookbackHours) * time.Hour,
		cache:         newResolverCache(time.Duration(cfg.CacheTTLMin)*time.Minute, 5*time.Minute),
		sem:           sem,
		client:        &http.Client{Timeout: 4 * time.Second},
		maxConsecFail: 3,
		inflight:      make(map[string]*inflightEntry),
	}
	r.lastErrMsg.Store("")
	return r, nil
}

// IsHealthy implements HealthyResolver.
// Returns false after maxConsecFail consecutive API failures.
func (r *HeliusResolver) IsHealthy() bool {
	return atomic.LoadInt64(&r.consecFail) < r.maxConsecFail
}

// BackendName implements HealthyResolver.
func (r *HeliusResolver) BackendName() string { return "helius" }

// Stats implements StatsResolver.
func (r *HeliusResolver) Stats() Stats {
	lastErr, _ := r.lastErrMsg.Load().(string)
	return Stats{
		ConsecutiveFailures: atomic.LoadInt64(&r.consecFail),
		TimeoutFallbacks:    atomic.LoadInt64(&r.timeoutFallbk),
		ErrorFallbacks:      atomic.LoadInt64(&r.errorFallbk),
		LastError:           lastErr,
	}
}

// ResolveParent implements FunderResolver.
// Returns the funding parent of wallet, or (wallet, false, nil) when unknown.
func (r *HeliusResolver) ResolveParent(ctx context.Context, wallet string, _ time.Time) (string, bool, error) {
	// 1. Cache lookup.
	if parent, found, ok := r.cache.get(wallet); ok {
		return parent, found, nil
	}

	// 2. In-flight dedup: if another goroutine is already fetching this wallet, wait.
	r.inflightMu.Lock()
	if e, ok := r.inflight[wallet]; ok {
		r.inflightMu.Unlock()
		e.wg.Wait()
		if e.found {
			return e.parent, true, nil
		}
		return wallet, false, nil
	}
	e := &inflightEntry{}
	e.wg.Add(1)
	r.inflight[wallet] = e
	r.inflightMu.Unlock()

	defer func() {
		e.wg.Done()
		r.inflightMu.Lock()
		delete(r.inflight, wallet)
		r.inflightMu.Unlock()
	}()

	// 3. Acquire concurrency slot.
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return wallet, false, nil
	}

	// 4. Fetch from Helius.
	parent, found, err := r.fetchFunder(ctx, wallet)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			atomic.AddInt64(&r.timeoutFallbk, 1)
			return wallet, false, context.DeadlineExceeded
		}
		atomic.AddInt64(&r.errorFallbk, 1)
		atomic.AddInt64(&r.consecFail, 1)
		r.lastErrMsg.Store(err.Error())
		return wallet, false, err
	}
	atomic.StoreInt64(&r.consecFail, 0) // reset on success
	r.lastErrMsg.Store("")

	if found {
		r.cache.setPositive(wallet, parent)
		e.parent = parent
		e.found = true
		return parent, true, nil
	}
	r.cache.setNegative(wallet)
	return wallet, false, nil
}

// fetchFunder calls the Helius enhanced-transaction API and returns the earliest
// inbound SOL funder for wallet within the lookback window.
//
// API: GET /v0/addresses/{wallet}/transactions?api-key={key}&type=TRANSFER&limit=100
//
// The API returns transactions newest-first.  We iterate all returned transactions
// and find the one with the smallest timestamp whose nativeTransfers includes an
// inbound SOL transfer to wallet.  That sender becomes the parent.
func (r *HeliusResolver) fetchFunder(ctx context.Context, wallet string) (parent string, found bool, err error) {
	endpoint := fmt.Sprintf("%s/v0/addresses/%s/transactions",
		r.baseURL, url.PathEscape(wallet))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, err
	}
	q := req.URL.Query()
	q.Set("api-key", r.apiKey)
	q.Set("type", "TRANSFER")
	q.Set("limit", "100")
	req.URL.RawQuery = q.Encode()

	resp, err := r.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("helius request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", false, fmt.Errorf("helius HTTP %d: %s", resp.StatusCode, body)
	}

	var txns []heliusTx
	if err := json.NewDecoder(resp.Body).Decode(&txns); err != nil {
		return "", false, fmt.Errorf("helius decode: %w", err)
	}

	cutoff := time.Now().Add(-r.lookback).Unix()

	// Find oldest inbound SOL transfer within the lookback window.
	// API is newest-first, so iterate all and track minimum timestamp.
	var bestFunder string
	var bestTS int64 = -1

	for _, tx := range txns {
		if int64(tx.Timestamp) < cutoff {
			continue
		}
		for _, nt := range tx.NativeTransfers {
			if nt.ToUserAccount == wallet && nt.Amount > 0 {
				// Inbound SOL transfer to our wallet.
				ts := int64(tx.Timestamp)
				if bestTS == -1 || ts < bestTS {
					bestTS = ts
					bestFunder = nt.FromUserAccount
				}
			}
		}
	}

	if bestFunder != "" && bestFunder != wallet {
		return bestFunder, true, nil
	}
	return "", false, nil
}

// heliusTx is the minimal subset of a Helius enhanced transaction we need.
type heliusTx struct {
	Signature       string           `json:"signature"`
	Timestamp       int64            `json:"timestamp"`
	NativeTransfers []nativeTransfer `json:"nativeTransfers"`
}

type nativeTransfer struct {
	FromUserAccount string  `json:"fromUserAccount"`
	ToUserAccount   string  `json:"toUserAccount"`
	Amount          float64 `json:"amount"` // lamports
}
