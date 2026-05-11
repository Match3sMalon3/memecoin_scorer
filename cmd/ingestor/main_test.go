package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"memecoin_scorer/internal/ingestor"
	"memecoin_scorer/internal/live"
	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/state"
)

const testMint = "MEMExxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
const testSecret = "s3cr3t"

// minimalBuyPayload is one valid Helius-style buy event for testMint.
func minimalBuyPayload(sig string) string {
	return fmt.Sprintf(`[{
		"signature": %q,
		"slot": 1,
		"timestamp": %d,
		"type": "SWAP",
		"source": "RAYDIUM",
		"feePayer": "wallet1",
		"transactionError": null,
		"events": {"swap": {
			"nativeInput":  {"account": "wallet1", "amount": "1000000000"},
			"nativeOutput": null,
			"tokenInputs":  [],
			"tokenOutputs": [{"mint": %q, "tokenAmount": 1000000}]
		}}
	}]`, sig, time.Now().Unix(), testMint)
}

// wrappedBuyPayload is the same event in {"transactions":[...]} form.
func wrappedBuyPayload(sig string) string {
	return fmt.Sprintf(`{"transactions": [{
		"signature": %q,
		"slot": 2,
		"timestamp": %d,
		"type": "SWAP",
		"source": "RAYDIUM",
		"feePayer": "wallet2",
		"transactionError": null,
		"events": {"swap": {
			"nativeInput":  {"account": "wallet2", "amount": "500000000"},
			"nativeOutput": null,
			"tokenInputs":  [],
			"tokenOutputs": [{"mint": %q, "tokenAmount": 500000}]
		}}
	}]}`, sig, time.Now().Unix(), testMint)
}

// ---- helpers ----

func newTestServer(t *testing.T, secret string) (*httptest.Server, *state.Store) {
	t.Helper()
	store := state.New()
	srv := httptest.NewServer(newServer(store, nil, secret, live.DefaultLiveConfig(), ingestor.NewIngressHealth(), nil))
	t.Cleanup(srv.Close)
	return srv, store
}

func postWebhook(t *testing.T, srv *httptest.Server, body, authHeader string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhook", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func decodeJSON(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return m
}

func TestDepthRPCURLFromEnv_DerivesHeliusURL(t *testing.T) {
	getenv := func(key string) string {
		if key == "HELIUS_API_KEY" {
			return "abc123"
		}
		return ""
	}
	got, derived := depthRPCURLFromEnv(getenv)
	if !derived {
		t.Fatal("derived=false, want true")
	}
	want := "https://mainnet.helius-rpc.com/?api-key=abc123"
	if got != want {
		t.Fatalf("rpcURL=%q, want %q", got, want)
	}
}

func TestDepthRPCURLFromEnv_NoSourcesFallsBack(t *testing.T) {
	got, derived := depthRPCURLFromEnv(func(string) string { return "" })
	if got != "" || derived {
		t.Fatalf("rpcURL=%q derived=%v, want empty false", got, derived)
	}
}

func TestMaskAPIKey(t *testing.T) {
	got := maskAPIKey("https://mainnet.helius-rpc.com/?api-key=secret&x=1")
	if strings.Contains(got, "secret") {
		t.Fatalf("masked URL leaked key: %s", got)
	}
	if !strings.Contains(got, "api-key=***") {
		t.Fatalf("masked URL=%q, want redacted api-key", got)
	}
}

// ---- /healthz ----

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeJSON(t, resp.Body)
	if m["ok"] != true {
		t.Errorf("body[ok] = %v, want true", m["ok"])
	}
}

// ---- /webhook auth ----

func TestWebhook_NoSecret_AllowsRequest(t *testing.T) {
	srv, _ := newTestServer(t, "") // no secret configured
	resp := postWebhook(t, srv, minimalBuyPayload("sigA"), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (no secret → auth not enforced)", resp.StatusCode)
	}
}

func TestWebhook_WithSecret_CorrectToken_Allowed(t *testing.T) {
	srv, _ := newTestServer(t, testSecret)
	resp := postWebhook(t, srv, minimalBuyPayload("sigB"), "Bearer "+testSecret)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestWebhook_WithSecret_NoToken_Returns401(t *testing.T) {
	srv, _ := newTestServer(t, testSecret)
	resp := postWebhook(t, srv, minimalBuyPayload("sigC"), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWebhook_WithSecret_WrongToken_Returns401(t *testing.T) {
	srv, _ := newTestServer(t, testSecret)
	resp := postWebhook(t, srv, minimalBuyPayload("sigD"), "Bearer wrongtoken")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ---- /webhook success ----

func TestWebhook_BareArrayPayload_Applied(t *testing.T) {
	srv, store := newTestServer(t, "")
	resp := postWebhook(t, srv, minimalBuyPayload("sigE"), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeJSON(t, resp.Body)
	if m["ok"] != true {
		t.Errorf("ok = %v, want true", m["ok"])
	}
	if m["events_received"].(float64) != 1 {
		t.Errorf("events_received = %v, want 1", m["events_received"])
	}
	if m["events_applied"].(float64) != 1 {
		t.Errorf("events_applied = %v, want 1", m["events_applied"])
	}

	snap, ok := store.Snapshot(testMint)
	if !ok {
		t.Fatal("expected snapshot for testMint after webhook")
	}
	if snap.UniqueBuyerCount != 1 {
		t.Errorf("UniqueBuyerCount = %d, want 1", snap.UniqueBuyerCount)
	}
	if snap.TotalBuySOL != 1.0 {
		t.Errorf("TotalBuySOL = %.4f, want 1.0", snap.TotalBuySOL)
	}
}

func TestWebhook_WrappedObjectPayload_Applied(t *testing.T) {
	srv, store := newTestServer(t, "")
	resp := postWebhook(t, srv, wrappedBuyPayload("sigF"), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeJSON(t, resp.Body)
	if m["events_applied"].(float64) != 1 {
		t.Errorf("events_applied = %v, want 1", m["events_applied"])
	}
	if _, ok := store.Snapshot(testMint); !ok {
		t.Error("expected snapshot after wrapped-object webhook")
	}
}

// ---- /webhook duplicate idempotency ----

func TestWebhook_DuplicatePayload_IdempotentWithinCache(t *testing.T) {
	srv, store := newTestServer(t, "")

	payload := minimalBuyPayload("sigDUP")

	// First delivery
	resp1 := postWebhook(t, srv, payload, "")
	defer resp1.Body.Close()
	m1 := decodeJSON(t, resp1.Body)

	// Exact retry — same signature
	resp2 := postWebhook(t, srv, payload, "")
	defer resp2.Body.Close()
	m2 := decodeJSON(t, resp2.Body)

	// Both requests return 200 (duplicate is not an error).
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("first: status = %d, want 200", resp1.StatusCode)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("retry: status = %d, want 200", resp2.StatusCode)
	}

	// First: 1 received, 1 applied.
	if m1["events_applied"].(float64) != 1 {
		t.Errorf("first events_applied = %v, want 1", m1["events_applied"])
	}
	// Retry: 1 received (parsed ok), 0 applied (duplicate dropped by sig cache).
	if m2["events_received"].(float64) != 1 {
		t.Errorf("retry events_received = %v, want 1", m2["events_received"])
	}
	if m2["events_applied"].(float64) != 0 {
		t.Errorf("retry events_applied = %v, want 0 (duplicate)", m2["events_applied"])
	}

	// Store has exactly one event worth of state.
	snap, _ := store.Snapshot(testMint)
	if snap.TotalBuySOL != 1.0 {
		t.Errorf("TotalBuySOL = %.4f, want 1.0 (duplicate must not double-count)", snap.TotalBuySOL)
	}
}

// ---- /webhook malformed JSON ----

func TestWebhook_MalformedJSON_Returns400(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp := postWebhook(t, srv, `not json at all`, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestWebhook_EmptyBody_Returns400(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp := postWebhook(t, srv, ``, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty body", resp.StatusCode)
	}
}

func TestWebhook_EmptyArray_Returns200_ZeroApplied(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp := postWebhook(t, srv, `[]`, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeJSON(t, resp.Body)
	if m["events_received"].(float64) != 0 {
		t.Errorf("events_received = %v, want 0", m["events_received"])
	}
	if m["events_applied"].(float64) != 0 {
		t.Errorf("events_applied = %v, want 0", m["events_applied"])
	}
}

// ---- /webhook non-POST rejected ----

func TestWebhook_GetMethod_Returns405(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp, err := http.Get(srv.URL + "/webhook")
	if err != nil {
		t.Fatalf("GET /webhook: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// ---- /api/snapshots filtering ----

func applySnapshot(t *testing.T, store *state.Store, mint string, buyers int, sol float64, at time.Time) {
	t.Helper()
	for i := 0; i < buyers; i++ {
		store.Apply(model.SwapEvent{
			Signature:  fmt.Sprintf("sig-%s-%d", mint, i),
			BlockTime:  at,
			TokenMint:  mint,
			IsBuy:      true,
			WalletAddr: fmt.Sprintf("wallet-%s-%d", mint, i),
			SOLAmount:  sol / float64(buyers),
		})
	}
}

func getSnapshots(t *testing.T, srv *httptest.Server, query string) []map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/snapshots" + query)
	if err != nil {
		t.Fatalf("GET /api/snapshots: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var snaps []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&snaps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return snaps
}

func TestSnapshots_EmptyStore_ReturnsEmptyArray(t *testing.T) {
	srv, _ := newTestServer(t, "")
	snaps := getSnapshots(t, srv, "")
	if len(snaps) != 0 {
		t.Errorf("len = %d, want 0 for empty store", len(snaps))
	}
}

func TestSnapshots_ReturnsTokensWithinWindow(t *testing.T) {
	srv, store := newTestServer(t, "")
	now := time.Now()

	applySnapshot(t, store, "FRESH_MINT", 5, 2.0, now.Add(-5*time.Minute))
	applySnapshot(t, store, "OLD_MINT", 5, 1.0, now.Add(-60*time.Minute))

	snaps := getSnapshots(t, srv, "?since_minutes=30")
	if len(snaps) != 1 {
		t.Fatalf("len = %d, want 1 (only FRESH_MINT within 30m)", len(snaps))
	}
	if snaps[0]["mint"] != "FRESH_MINT" {
		t.Errorf("mint = %v, want FRESH_MINT", snaps[0]["mint"])
	}
}

func TestSnapshots_MinBuyersFilter(t *testing.T) {
	srv, store := newTestServer(t, "")
	now := time.Now()

	applySnapshot(t, store, "POPULAR", 10, 5.0, now.Add(-1*time.Minute))
	applySnapshot(t, store, "QUIET", 2, 1.0, now.Add(-1*time.Minute))

	snaps := getSnapshots(t, srv, "?min_buyers=5")
	if len(snaps) != 1 {
		t.Fatalf("len = %d, want 1 (only POPULAR has >= 5 buyers)", len(snaps))
	}
	if snaps[0]["mint"] != "POPULAR" {
		t.Errorf("mint = %v, want POPULAR", snaps[0]["mint"])
	}
}

func TestSnapshots_LimitRespected(t *testing.T) {
	srv, store := newTestServer(t, "")
	now := time.Now()

	for i := 0; i < 10; i++ {
		applySnapshot(t, store, fmt.Sprintf("MINT%d", i), 3, 1.0, now.Add(-time.Duration(i)*time.Second))
	}

	snaps := getSnapshots(t, srv, "?limit=3&min_buyers=1&since_minutes=5")
	if len(snaps) != 3 {
		t.Errorf("len = %d, want 3 (limit=3)", len(snaps))
	}
}

func TestSnapshots_NewestFirst(t *testing.T) {
	srv, store := newTestServer(t, "")
	now := time.Now()

	applySnapshot(t, store, "OLDER", 3, 1.0, now.Add(-10*time.Minute))
	applySnapshot(t, store, "NEWER", 3, 1.0, now.Add(-1*time.Minute))

	snaps := getSnapshots(t, srv, "?since_minutes=30&min_buyers=1")
	if len(snaps) < 2 {
		t.Fatalf("len = %d, want >= 2", len(snaps))
	}
	if snaps[0]["mint"] != "NEWER" {
		t.Errorf("first mint = %v, want NEWER (newest-first)", snaps[0]["mint"])
	}
}

func TestSnapshots_FieldsPresent(t *testing.T) {
	srv, store := newTestServer(t, "")
	applySnapshot(t, store, testMint, 3, 2.0, time.Now().Add(-30*time.Second))

	snaps := getSnapshots(t, srv, "?min_buyers=1&since_minutes=5")
	if len(snaps) != 1 {
		t.Fatalf("len = %d, want 1", len(snaps))
	}
	s := snaps[0]

	// TokenSnapshot fields
	for _, field := range []string{
		"mint", "first_seen_at", "last_event_at",
		"unique_buyer_count", "total_buy_sol", "total_sell_sol",
		"sell_trade_count", "buyers_last1m", "buyers_last5m",
		"buyer_acceleration", "age_seconds", "observed_age_seconds",
		"observed_first_seen_at", "launch_confidence", "launch_evidence_source",
	} {
		if _, ok := s[field]; !ok {
			t.Errorf("TokenSnapshot field %q missing from response", field)
		}
	}
	// ScoredSnapshot fields — including marketability fields
	for _, field := range []string{
		"decision", "reasons", "execution_penalty", "liquidity_proxy_sol",
		"liquidity_evidence_source", "liquidity_evidence_age_seconds", "liquidity_proxy_reliable",
		"adversarial_score", "trade_size_sol", "estimated_impact_pct",
		"early_proxy",
	} {
		if _, ok := s[field]; !ok {
			t.Errorf("ScoredSnapshot field %q missing from response", field)
		}
	}
	earlyProxy, ok := s["early_proxy"].(map[string]any)
	if !ok {
		t.Fatalf("early_proxy = %T, want object", s["early_proxy"])
	}
	for _, field := range []string{"score", "threshold", "band", "evidence_version"} {
		if _, ok := earlyProxy[field]; !ok {
			t.Errorf("early_proxy field %q missing from response", field)
		}
	}

	if s["mint"] != testMint {
		t.Errorf("mint = %v, want %q", s["mint"], testMint)
	}
	if s["unique_buyer_count"].(float64) != 3 {
		t.Errorf("unique_buyer_count = %v, want 3", s["unique_buyer_count"])
	}
	// decision must be a non-empty string
	if dec, ok := s["decision"].(string); !ok || dec == "" {
		t.Errorf("decision = %v, want non-empty string", s["decision"])
	}
	// execution_penalty must be in [0,1]
	if ep, ok := s["execution_penalty"].(float64); !ok || ep < 0 || ep > 1 {
		t.Errorf("execution_penalty = %v, want float in [0,1]", s["execution_penalty"])
	}
	if src, ok := s["liquidity_evidence_source"].(string); !ok || src != live.LiquidityEvidenceObservedSwapsProxy {
		t.Errorf("liquidity_evidence_source = %v, want %q", s["liquidity_evidence_source"], live.LiquidityEvidenceObservedSwapsProxy)
	}
	if reliable, ok := s["liquidity_proxy_reliable"].(bool); !ok || reliable {
		t.Errorf("liquidity_proxy_reliable = %v, want false for observed swaps proxy", s["liquidity_proxy_reliable"])
	}
}

// ---- Marketability fields in payload ----

func TestSnapshots_MarketabilityFields(t *testing.T) {
	srv, store := newTestServer(t, "")
	// Apply enough volume so execution penalty is non-trivial.
	applySnapshot(t, store, testMint, 5, 20.0, time.Now().Add(-30*time.Second))

	snaps := getSnapshots(t, srv, "?min_buyers=1&since_minutes=5")
	if len(snaps) != 1 {
		t.Fatalf("len = %d, want 1", len(snaps))
	}
	s := snaps[0]

	// trade_size_sol must be positive (reflects DefaultLiveConfig TradeSizeSOL=1.0).
	ts, ok := s["trade_size_sol"].(float64)
	if !ok || ts <= 0 {
		t.Errorf("trade_size_sol = %v, want positive float", s["trade_size_sol"])
	}

	// liquidity_proxy_sol must reflect total buy volume applied.
	liq, ok := s["liquidity_proxy_sol"].(float64)
	if !ok || liq <= 0 {
		t.Errorf("liquidity_proxy_sol = %v, want positive float", s["liquidity_proxy_sol"])
	}

	// estimated_impact_pct must be in [0, 100].
	imp, ok := s["estimated_impact_pct"].(float64)
	if !ok || imp < 0 || imp > 100 {
		t.Errorf("estimated_impact_pct = %v, want float in [0,100]", s["estimated_impact_pct"])
	}

	// Relationship: impact ≈ trade_size_sol / liquidity_proxy_sol * 100.
	wantImpact := ts / liq * 100
	if imp < wantImpact-0.01 || imp > wantImpact+0.01 {
		t.Errorf("estimated_impact_pct = %.4f, want ≈ %.4f (trade=%.2f/liq=%.2f*100)",
			imp, wantImpact, ts, liq)
	}
}

// ---- liveConfigFromEnv ----

func TestLiveConfigFromEnv_Defaults(t *testing.T) {
	cfg := liveConfigFromEnv()
	if cfg.TradeSizeSOL != 1.0 {
		t.Errorf("TradeSizeSOL = %.2f, want 1.0", cfg.TradeSizeSOL)
	}
	if cfg.LiquidityMultiplier != 20.0 {
		t.Errorf("LiquidityMultiplier = %.2f, want 20.0", cfg.LiquidityMultiplier)
	}
	if cfg.MinExecQualityBUY != 0.5 {
		t.Errorf("MinExecQualityBUY = %.2f, want 0.5", cfg.MinExecQualityBUY)
	}
	if cfg.MinExecQualityREADY != 0.3 {
		t.Errorf("MinExecQualityREADY = %.2f, want 0.3", cfg.MinExecQualityREADY)
	}
	if cfg.MinExecQualityAVOID != 0.1 {
		t.Errorf("MinExecQualityAVOID = %.2f, want 0.1", cfg.MinExecQualityAVOID)
	}
	if cfg.MaxAdversarialBUY != 0.60 {
		t.Errorf("MaxAdversarialBUY = %.2f, want 0.60", cfg.MaxAdversarialBUY)
	}
	if cfg.MaxAdversarialREADY != 0.75 {
		t.Errorf("MaxAdversarialREADY = %.2f, want 0.75", cfg.MaxAdversarialREADY)
	}
}

func TestLiveConfigFromEnv_TradeSize(t *testing.T) {
	t.Setenv("TRADE_SIZE_SOL", "2.5")
	cfg := liveConfigFromEnv()
	if cfg.TradeSizeSOL != 2.5 {
		t.Errorf("TradeSizeSOL = %.2f, want 2.5", cfg.TradeSizeSOL)
	}
}

func TestLiveConfigFromEnv_ExecGates(t *testing.T) {
	t.Setenv("MIN_EXEC_QUALITY_BUY", "0.7")
	t.Setenv("MIN_EXEC_QUALITY_READY", "0.4")
	t.Setenv("MIN_EXEC_QUALITY_AVOID", "0.05")
	cfg := liveConfigFromEnv()
	if cfg.MinExecQualityBUY != 0.7 {
		t.Errorf("MinExecQualityBUY = %.2f, want 0.7", cfg.MinExecQualityBUY)
	}
	if cfg.MinExecQualityREADY != 0.4 {
		t.Errorf("MinExecQualityREADY = %.2f, want 0.4", cfg.MinExecQualityREADY)
	}
	if cfg.MinExecQualityAVOID != 0.05 {
		t.Errorf("MinExecQualityAVOID = %.2f, want 0.05", cfg.MinExecQualityAVOID)
	}
}

func TestLiveConfigFromEnv_AdversarialGates(t *testing.T) {
	t.Setenv("MAX_ADVERSARIAL_BUY", "0.45")
	t.Setenv("MAX_ADVERSARIAL_READY", "0.65")
	cfg := liveConfigFromEnv()
	if cfg.MaxAdversarialBUY != 0.45 {
		t.Errorf("MaxAdversarialBUY = %.2f, want 0.45", cfg.MaxAdversarialBUY)
	}
	if cfg.MaxAdversarialREADY != 0.65 {
		t.Errorf("MaxAdversarialREADY = %.2f, want 0.65", cfg.MaxAdversarialREADY)
	}
}

func TestLiveConfigFromEnv_InvalidValues_FallbackToDefault(t *testing.T) {
	t.Setenv("TRADE_SIZE_SOL", "not-a-number")
	t.Setenv("MIN_EXEC_QUALITY_BUY", "1.5") // out of [0,1]
	t.Setenv("MAX_ADVERSARIAL_BUY", "-0.1") // negative, out of [0,1]
	cfg := liveConfigFromEnv()
	if cfg.TradeSizeSOL != 1.0 {
		t.Errorf("TradeSizeSOL = %.2f, want default 1.0 on invalid input", cfg.TradeSizeSOL)
	}
	if cfg.MinExecQualityBUY != 0.5 {
		t.Errorf("MinExecQualityBUY = %.2f, want default 0.5 on out-of-range input", cfg.MinExecQualityBUY)
	}
	if cfg.MaxAdversarialBUY != 0.60 {
		t.Errorf("MaxAdversarialBUY = %.2f, want default 0.60 on negative input", cfg.MaxAdversarialBUY)
	}
}

// ---- port resolution ----

func TestResolveIngestorPort_Default(t *testing.T) {
	t.Setenv("INGESTOR_PORT", "")
	t.Setenv("PORT", "")
	if got := resolveIngestorPort(); got != "8080" {
		t.Errorf("resolveIngestorPort() = %q, want 8080", got)
	}
}

func TestResolveIngestorPort_IngestorPortWins(t *testing.T) {
	t.Setenv("INGESTOR_PORT", "9080")
	t.Setenv("PORT", "7000") // PORT must not override INGESTOR_PORT
	if got := resolveIngestorPort(); got != "9080" {
		t.Errorf("resolveIngestorPort() = %q, want 9080 (INGESTOR_PORT wins)", got)
	}
}

func TestResolveIngestorPort_LegacyPort(t *testing.T) {
	t.Setenv("INGESTOR_PORT", "")
	t.Setenv("PORT", "7070")
	if got := resolveIngestorPort(); got != "7070" {
		t.Errorf("resolveIngestorPort() = %q, want 7070 (PORT fallback)", got)
	}
}
