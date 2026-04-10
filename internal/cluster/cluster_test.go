package cluster_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"memecoin_scorer/internal/cluster"
)

var (
	testNow = time.Unix(1_700_000_000, 0)
	ctx     = context.Background()
)

// ============================================================
// helpers
// ============================================================

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func newMockHeliusServer(funders map[string]string) *httptest.Server {
	var dummy int64
	return newMockHeliusServerWithCount(funders, &dummy)
}

func newMockHeliusServerWithCount(funders map[string]string, count *int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(count, 1)
		parts := strings.Split(r.URL.Path, "/")
		var wallet string
		for i, p := range parts {
			if p == "addresses" && i+1 < len(parts) {
				wallet = parts[i+1]
				break
			}
		}
		funder, ok := funders[wallet]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		tx := fmt.Sprintf(`[{"signature":"sig1","timestamp":%d,"nativeTransfers":[{"fromUserAccount":%q,"toUserAccount":%q,"amount":1000000000}]}]`,
			time.Now().Unix(), funder, wallet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tx))
	}))
}

func newErrorHeliusServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
}

func newSlowHeliusServer(delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
}

// ============================================================
// NullResolver — unhealthy sentinel
// ============================================================

func TestNullResolver_EffectiveEqualsRaw(t *testing.T) {
	wallets := []string{"A", "B", "C"}
	res := cluster.Cluster(ctx, wallets, cluster.NullResolver{}, testNow)
	if res.EffectiveUniqueBuyerCount != 3 {
		t.Errorf("effective=%d, want 3", res.EffectiveUniqueBuyerCount)
	}
	if res.ClusteredBuyerCount != 0 {
		t.Errorf("clustered=%d, want 0", res.ClusteredBuyerCount)
	}
}

func TestNullResolver_IsNotHealthy(t *testing.T) {
	if cluster.IsResolverHealthy(cluster.NullResolver{}) {
		t.Error("NullResolver must not be healthy")
	}
}

func TestNullResolver_BackendName(t *testing.T) {
	if cluster.ResolverBackendName(cluster.NullResolver{}) != "null" {
		t.Errorf("expected backend name 'null'")
	}
}

// ============================================================
// StaticResolver — healthy, deterministic
// ============================================================

func TestStaticResolver_SameParentCollapses_5to1(t *testing.T) {
	m := map[string]string{"A": "P1", "B": "P1", "C": "P1", "D": "P1", "E": "P1"}
	r := cluster.NewStaticResolver(m)
	res := cluster.Cluster(ctx, []string{"A", "B", "C", "D", "E"}, r, testNow)
	if res.EffectiveUniqueBuyerCount != 1 {
		t.Errorf("effective=%d, want 1", res.EffectiveUniqueBuyerCount)
	}
	if res.ClusteredBuyerCount != 4 {
		t.Errorf("clustered=%d, want 4", res.ClusteredBuyerCount)
	}
	wantRatio := 0.80
	if res.FundingClusterRatio < wantRatio-0.001 || res.FundingClusterRatio > wantRatio+0.001 {
		t.Errorf("ratio=%.4f, want %.4f", res.FundingClusterRatio, wantRatio)
	}
}

func TestStaticResolver_FiveWalletsFiveParents_EffectiveStays5(t *testing.T) {
	m := map[string]string{"A": "PA", "B": "PB", "C": "PC", "D": "PD", "E": "PE"}
	r := cluster.NewStaticResolver(m)
	res := cluster.Cluster(ctx, []string{"A", "B", "C", "D", "E"}, r, testNow)
	if res.EffectiveUniqueBuyerCount != 5 {
		t.Errorf("effective=%d, want 5", res.EffectiveUniqueBuyerCount)
	}
	if res.ClusteredBuyerCount != 0 {
		t.Errorf("clustered=%d, want 0", res.ClusteredBuyerCount)
	}
}

func TestStaticResolver_MixedParents(t *testing.T) {
	// A+B+C → P1, D+E → P2, F independent → 3 effective
	m := map[string]string{"A": "P1", "B": "P1", "C": "P1", "D": "P2", "E": "P2"}
	r := cluster.NewStaticResolver(m)
	res := cluster.Cluster(ctx, []string{"A", "B", "C", "D", "E", "F"}, r, testNow)
	if res.EffectiveUniqueBuyerCount != 3 {
		t.Errorf("effective=%d, want 3 (P1, P2, F)", res.EffectiveUniqueBuyerCount)
	}
	if res.ClusteredBuyerCount != 3 {
		t.Errorf("clustered=%d, want 3", res.ClusteredBuyerCount)
	}
}

func TestStaticResolver_IsHealthy(t *testing.T) {
	r := cluster.NewStaticResolver(map[string]string{})
	if !cluster.IsResolverHealthy(r) {
		t.Error("StaticResolver must be healthy")
	}
}

func TestStaticResolver_BackendName(t *testing.T) {
	r := cluster.NewStaticResolver(map[string]string{})
	if cluster.ResolverBackendName(r) != "static" {
		t.Errorf("expected backend name 'static'")
	}
}

func TestStaticResolver_EmptyWallets(t *testing.T) {
	r := cluster.NewStaticResolver(map[string]string{"A": "P1"})
	res := cluster.Cluster(ctx, nil, r, testNow)
	if res.EffectiveUniqueBuyerCount != 0 {
		t.Errorf("effective=%d, want 0 for empty input", res.EffectiveUniqueBuyerCount)
	}
}

// ============================================================
// LoadStaticResolver — JSON file
// ============================================================

func TestLoadStaticResolver_JSONFile(t *testing.T) {
	f := t.TempDir() + "/funder_map.json"
	content := `{"WalletA":"ParentX","WalletB":"ParentX","WalletC":"ParentY"}`
	if err := writeFile(f, content); err != nil {
		t.Fatal(err)
	}
	r, err := cluster.LoadStaticResolver(f)
	if err != nil {
		t.Fatalf("LoadStaticResolver: %v", err)
	}
	if r.Len() != 3 {
		t.Errorf("Len=%d, want 3", r.Len())
	}
	res := cluster.Cluster(ctx, []string{"WalletA", "WalletB", "WalletC"}, r, testNow)
	if res.EffectiveUniqueBuyerCount != 2 { // ParentX, ParentY
		t.Errorf("effective=%d, want 2", res.EffectiveUniqueBuyerCount)
	}
}

func TestLoadStaticResolver_MissingFile(t *testing.T) {
	_, err := cluster.LoadStaticResolver("/nonexistent/path/funder_map.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadStaticResolver_InvalidJSON(t *testing.T) {
	f := t.TempDir() + "/bad.json"
	if err := writeFile(f, `not json`); err != nil {
		t.Fatal(err)
	}
	_, err := cluster.LoadStaticResolver(f)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ============================================================
// HeliusResolver — cache hit, negative cache, concurrency safety
// ============================================================

func TestHeliusResolver_IsHealthy_Initially(t *testing.T) {
	srv := newMockHeliusServer(map[string]string{})
	defer srv.Close()

	r, err := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewHeliusResolver: %v", err)
	}
	if !r.IsHealthy() {
		t.Error("HeliusResolver must be healthy initially")
	}
	if r.BackendName() != "helius" {
		t.Errorf("BackendName=%q, want helius", r.BackendName())
	}
}

func TestHeliusResolver_ReturnsParent(t *testing.T) {
	srv := newMockHeliusServer(map[string]string{"WalletA": "ParentX"})
	defer srv.Close()

	r, _ := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	parent, found, err := r.ResolveParent(ctx, "WalletA", testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("expected found=true")
	}
	if parent != "ParentX" {
		t.Errorf("parent=%q, want ParentX", parent)
	}
}

func TestHeliusResolver_CacheHit_NoExtraHTTP(t *testing.T) {
	callCount := int64(0)
	srv := newMockHeliusServerWithCount(map[string]string{"WalletA": "ParentX"}, &callCount)
	defer srv.Close()

	r, _ := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{
		APIKey:      "test-key",
		BaseURL:     srv.URL,
		CacheTTLMin: 120,
	})
	r.ResolveParent(ctx, "WalletA", testNow)
	first := atomic.LoadInt64(&callCount)
	r.ResolveParent(ctx, "WalletA", testNow)
	second := atomic.LoadInt64(&callCount)
	if second != first {
		t.Errorf("cache miss on second call: HTTP count went from %d to %d", first, second)
	}
}

func TestHeliusResolver_NegativeCache_UnknownWallet(t *testing.T) {
	callCount := int64(0)
	srv := newMockHeliusServerWithCount(map[string]string{}, &callCount)
	defer srv.Close()

	r, _ := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	_, found1, _ := r.ResolveParent(ctx, "UnknownWallet", testNow)
	c1 := atomic.LoadInt64(&callCount)
	_, found2, _ := r.ResolveParent(ctx, "UnknownWallet", testNow)
	c2 := atomic.LoadInt64(&callCount)

	if found1 || found2 {
		t.Error("expected found=false for unknown wallet")
	}
	if c2 != c1 {
		t.Errorf("negative cache miss: HTTP calls went from %d to %d", c1, c2)
	}
}

func TestHeliusResolver_ConcurrencySafe_SameWallet(t *testing.T) {
	callCount := int64(0)
	srv := newMockHeliusServerWithCount(map[string]string{"W": "P"}, &callCount)
	defer srv.Close()

	r, _ := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{
		APIKey:         "test-key",
		BaseURL:        srv.URL,
		MaxConcurrency: 8,
	})

	var wg sync.WaitGroup
	results := make([]string, 20)
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			parent, _, _ := r.ResolveParent(ctx, "W", testNow)
			results[i] = parent
		}()
	}
	wg.Wait()

	for i, p := range results {
		if p != "P" {
			t.Errorf("results[%d]=%q, want P", i, p)
		}
	}
	n := atomic.LoadInt64(&callCount)
	if n > 5 {
		t.Errorf("expected ≤5 HTTP calls for 20 concurrent same-wallet requests, got %d", n)
	}
}

func TestHeliusResolver_DegradedAfterConsecFails(t *testing.T) {
	srv := newErrorHeliusServer()
	defer srv.Close()

	r, _ := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	// 3 consecutive failures on distinct wallets (bypasses cache)
	for i := 0; i < 3; i++ {
		r.ResolveParent(ctx, "W"+strconv.Itoa(i), testNow)
	}
	if r.IsHealthy() {
		t.Error("resolver must be degraded after 3 consecutive failures")
	}
	if cluster.IsResolverHealthy(r) {
		t.Error("IsResolverHealthy must return false for degraded HeliusResolver")
	}
}

func TestHeliusResolver_TimeoutFallback_DoesNotDegradeGlobalHealth(t *testing.T) {
	srv := newSlowHeliusServer(200 * time.Millisecond)
	defer srv.Close()

	r, _ := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})

	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()

	for i := 0; i < 3; i++ {
		_, _, _ = r.ResolveParent(timeoutCtx, "W"+strconv.Itoa(i), testNow)
	}

	if !r.IsHealthy() {
		t.Fatal("resolver should remain globally healthy after request-time deadlines")
	}
	stats := r.Stats()
	if stats.TimeoutFallbacks == 0 {
		t.Fatal("expected timeout fallbacks to be recorded")
	}
	if stats.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d, want 0 for request-time deadlines", stats.ConsecutiveFailures)
	}
}

func TestHeliusResolver_EmptyAPIKey_Error(t *testing.T) {
	_, err := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{APIKey: ""})
	if err == nil {
		t.Error("expected error when APIKey is empty")
	}
}

// ============================================================
// Decision integration — raw satisfies gate but effective does not
// ============================================================

func TestDecision_RawPassesGateButEffectiveDoesNot(t *testing.T) {
	// 4 wallets → all share P1 → effective=1, below MinBuyers1mBUY=3
	m := map[string]string{"A": "P1", "B": "P1", "C": "P1", "D": "P1"}
	r := cluster.NewStaticResolver(m)
	wallets := []string{"A", "B", "C", "D"}
	res := cluster.Cluster(ctx, wallets, r, testNow)
	if res.EffectiveUniqueBuyerCount >= 3 {
		t.Errorf("effective=%d — this would wrongly allow BUY at MinBuyers1mBUY=3", res.EffectiveUniqueBuyerCount)
	}
}
