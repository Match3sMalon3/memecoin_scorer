package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"memecoin_scorer/internal/alerts"
)

func sampleLiveRow() map[string]any {
	return map[string]any{
		"mint":                      "RENDERMINT123456789",
		"dominant_blocker":          "impossible execution • observed liq proxy below 5",
		"why_now":                   "5 eff buyers /1m • positive buy pressure",
		"execution_url":             "https://gmgn.ai/sol/token/RENDERMINT123456789",
		"solscan_url":               "https://solscan.io/token/RENDERMINT123456789",
		"early_proxy":               map[string]any{"score": 74.0, "threshold": 62.0, "band": "RUNNER", "reasons": []any{"early effective buyer depth", "positive buy pressure"}, "risk_flags": []any{"observed liq proxy below 5 SOL"}, "evidence_version": "test"},
		"token_mode":                "launch",
		"launch_confidence":         "exact",
		"launch_age_seconds":        60.0,
		"observed_age_seconds":      120.0,
		"setup":                     map[string]any{"mode": "LAUNCH_WOW", "score_tier": "HIGH", "action": "PAPER_LOG", "proxy_score": 74.0, "authenticity_score": 100.0, "velocity_score": 25.0, "reasons": []any{"early effective buyer depth", "positive buy pressure"}, "blockers": []any{}, "invalidation": []any{"buyer flow disappears"}},
		"authenticity":              map[string]any{"bundle_bot": false, "bundle_bot_confidence": "unavailable", "sniper_bot": false, "sniper_bot_confidence": "unavailable", "bump_bot": false, "bump_bot_confidence": "unavailable", "mechanical_rhythm": false, "identical_buy_sizes": false, "flags": []any{}, "score": 100.0, "severity": "none"},
		"sol_per_trade_5m":          0.5,
		"sol_per_unique_buyer_5m":   0.5,
		"signal_state":              "active",
		"confidence_score":          66.0,
		"buyers_last1m":             5.0,
		"buyers_last5m":             8.0,
		"effective_buyers_1m":       5.0,
		"effective_buyers_5m":       6.0,
		"buy_sol_last_1m":           1.2,
		"sell_sol_last_1m":          0.4,
		"estimated_impact_pct":      12.0,
		"age_seconds":               120.0,
		"top10_holder_pct":          0.22,
		"clustering_row_status":     "resolved",
		"last_price_sol":            0.000001,
		"liquidity_evidence_source": "observed_swaps_proxy",
		"liquidity_proxy_reliable":  false,
		"real_pool_depth_sol":       -1.0,
	}
}

func TestRenderIndexHTML_WowShell(t *testing.T) {
	app := &App{
		cfg:              dashConfig{liveMode: true},
		cachedLiveRows:   []map[string]any{sampleLiveRow()},
		cachedLiveRowsAt: time.Now(),
	}
	html := app.renderIndexHTML()
	for _, want := range []string{
		`ANTI-BULLSHIT RUNNER INTELLIGENCE`,
		`EDGE PROOF`,
		`proof-bar`,
		`alert-panel`,
		`/api/alerts/stream`,
		`LIVE LAUNCH_WOW SETUP`,
		`BACKTEST`,
		`GMGN`,
		`Solscan`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("markup %q missing: %s", want, html)
		}
	}
	for _, bad := range []string{`RUNNERS`, `SIGNAL ACTIVE`, `BAD TAPE DETECTED`, `STRUCTURAL QUALITY FILTER`, `POSTURE: DEFENSIVE`, `liq 0.00 < 5`} {
		if strings.Contains(html, bad) {
			t.Fatalf("forbidden markup %q present: %s", bad, html)
		}
	}
}

func TestChooseBestSetupGo_UsesRunnerVocabulary(t *testing.T) {
	watch := sampleLiveRow()
	watch["mint"] = "WATCHMINT123456789"
	watch["early_proxy"] = map[string]any{"score": 55.0, "threshold": 62.0, "band": "WATCH"}
	watch["setup"] = map[string]any{"mode": "WATCH", "action": "WATCH_5M", "proxy_score": 55.0}

	runner := sampleLiveRow()
	runner["mint"] = "RUNNERMINT123456789"
	runner["early_proxy"] = map[string]any{"score": 70.0, "threshold": 62.0, "band": "RUNNER"}
	runner["setup"] = map[string]any{"mode": "LAUNCH_WOW", "action": "PAPER_LOG", "proxy_score": 70.0}

	best := chooseBestSetupGo([]map[string]any{watch, runner})
	if got := stringFieldMap(best, "mint"); got != "RUNNERMINT123456789" {
		t.Fatalf("best mint=%q, want runner row", got)
	}
}

func TestRenderWowLockedRows_UsesFinalCappedBandNotScore(t *testing.T) {
	row := sampleLiveRow()
	row["early_proxy"] = map[string]any{
		"score":      99.0,
		"threshold":  62.0,
		"band":       "WATCH",
		"risk_flags": []any{"WOW capped: unverified liquidity"},
	}
	row["setup"] = map[string]any{"mode": "WATCH", "action": "WATCH_5M", "proxy_score": 99.0, "blockers": []any{"WOW capped: unverified liquidity"}}
	html := renderWowLockedRows([]map[string]any{row}, false)
	if !strings.Contains(html, `>WATCH</span>`) {
		t.Fatalf("rendered row did not display capped WATCH band: %s", html)
	}
	if strings.Contains(html, `>LAUNCH_WOW</span>`) {
		t.Fatalf("dashboard promoted raw score to WOW: %s", html)
	}
	if !strings.Contains(html, "WOW capped: unverified liquidity") {
		t.Fatalf("capping reason missing from dashboard row: %s", html)
	}
}

func TestRenderIndexHTML_WatchRowsSurfaceInHero(t *testing.T) {
	row := sampleLiveRow()
	row["setup"] = map[string]any{
		"mode":             "WATCH",
		"action":           "WATCH_5M",
		"proxy_score":      55.0,
		"reasons":          []any{"fresh buyer flow"},
		"blockers":         []any{"unverified liquidity"},
		"blocker_severity": "watch",
	}
	app := &App{
		cfg:              dashConfig{liveMode: true},
		cachedLiveRows:   []map[string]any{row},
		cachedLiveRowsAt: time.Now(),
	}
	html := app.renderIndexHTML()
	if !strings.Contains(html, "LIVE WATCH CANDIDATE") && !strings.Contains(html, "NO WOW SETUP") {
		t.Fatalf("watch hero missing: %s", html)
	}
	if strings.Contains(strings.ToLower(html), "no live rows") {
		t.Fatalf("hero says no live rows while watch row exists: %s", html)
	}
}

func TestRenderIndexHTML_DeadCollapsedByDefault(t *testing.T) {
	row := sampleLiveRow()
	row["setup"] = map[string]any{"mode": "DEAD", "action": "NO_TRADE", "proxy_score": 0.0, "blockers": []any{"no real flow"}}
	app := &App{
		cfg:              dashConfig{liveMode: true},
		cachedLiveRows:   []map[string]any{row},
		cachedLiveRowsAt: time.Now(),
	}
	html := app.renderIndexHTML()
	if !strings.Contains(html, "Show DEAD / hidden rejected tokens (1)") {
		t.Fatalf("dead toggle missing: %s", html)
	}
	if !strings.Contains(html, `<tbody id="token-rows"><tr><td colspan="7">No active WOW, REVIEW, WATCH, MANIPULATED, or AVOID rows.</td></tr>`) {
		t.Fatalf("dead row leaked into default table: %s", html)
	}
}

func TestRenderIndexHTML_ReviewCandidateSurfacesInHero(t *testing.T) {
	row := sampleLiveRow()
	row["token_mode"] = "revival"
	row["setup"] = map[string]any{
		"mode":               "REVIEW_CANDIDATE",
		"action":             "WATCH_1M",
		"proxy_score":        88.0,
		"authenticity_score": 100.0,
		"reasons":            []any{"high-score setup requires operator review before WOW"},
		"blockers":           []any{"partial clustering fallback", "unknown catalyst"},
		"blocker_severity":   "avoid",
		"reviewable":         true,
		"review_reason":      "strong revival demand, blocked from WOW by partial clustering fallback and unknown catalyst",
	}
	row["authenticity"] = map[string]any{"score": 100.0, "severity": "none", "flags": []any{}}
	row["clustering_row_status"] = "partial_fallback"
	row["real_pool_depth_sol"] = 12.0
	row["liquidity_evidence_source"] = "raydium_wsol_vault"
	row["liquidity_proxy_reliable"] = true
	app := &App{
		cfg:              dashConfig{liveMode: true},
		cachedLiveRows:   []map[string]any{row},
		cachedLiveRowsAt: time.Now(),
	}
	html := app.renderIndexHTML()
	if !strings.Contains(html, "LIVE REVIEW CANDIDATE") {
		t.Fatalf("review candidate hero missing: %s", html)
	}
	if strings.Contains(strings.Split(html, "</section>")[0], "NO LIVE SETUP") {
		t.Fatalf("hero says no live setup while review candidate exists: %s", html)
	}
	if !strings.Contains(html, "blocked from WOW: partial clustering fallback") {
		t.Fatalf("review blocker copy missing from row: %s", html)
	}
}

func TestChooseBestSetupGo_DeadSetupCannotBecomeHero(t *testing.T) {
	dead := sampleLiveRow()
	dead["setup"] = map[string]any{"mode": "DEAD", "action": "NO_TRADE", "proxy_score": 0.0}
	if best := chooseBestSetupGo([]map[string]any{dead}); best != nil {
		t.Fatalf("best=%v, want nil for dead setup row", best)
	}
}

func TestAlertsStreamHeaders(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/api/alerts/stream", nil)
	ctx, cancel := context.WithTimeout(req.Context(), 20*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleAlertsStream(rr, req)
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content-type=%q, want text/event-stream", got)
	}
	if !strings.Contains(rr.Body.String(), ": ping") {
		t.Fatalf("heartbeat missing: %s", rr.Body.String())
	}
}

func TestAlertBrokerPublishes(t *testing.T) {
	ch := alerts.Subscribe()
	defer alerts.Unsubscribe(ch)
	alerts.Publish(alerts.Alert{Mint: "mint", Score: 70})
	select {
	case got := <-ch:
		if got.Mint != "mint" {
			t.Fatalf("mint=%q", got.Mint)
		}
	case <-time.After(time.Second):
		t.Fatal("alert not delivered")
	}
}

func TestLiveSnapshotsDefaultLimitIs200(t *testing.T) {
	var gotLimit string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/snapshots" {
			http.NotFound(w, r)
			return
		}
		gotLimit = r.URL.Query().Get("limit")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"mint": "m"}})
	}))
	defer upstream.Close()

	app := &App{cfg: dashConfig{liveMode: true, ingestorURL: upstream.URL}}
	req := httptest.NewRequest(http.MethodGet, "/api/live-snapshots", nil)
	rr := httptest.NewRecorder()
	app.handleLiveSnapshots(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotLimit != "200" {
		t.Fatalf("upstream limit=%q, want 200", gotLimit)
	}
}

func TestMarketContextCountsReviewCandidates(t *testing.T) {
	row := sampleLiveRow()
	row["setup"] = map[string]any{"mode": "REVIEW_CANDIDATE", "action": "WATCH_1M", "proxy_score": 88.0}
	app := &App{
		cfg:              dashConfig{liveMode: true},
		cachedLiveRows:   []map[string]any{row},
		cachedLiveRowsAt: time.Now(),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/market-context", nil)
	rr := httptest.NewRecorder()
	app.handleMarketContext(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := int(body["review_count"].(float64)); got != 1 {
		t.Fatalf("review_count=%d, want 1; body=%v", got, body)
	}
	if got := body["market_posture"].(string); got != "REVIEW ACTIVE" {
		t.Fatalf("market_posture=%q, want REVIEW ACTIVE", got)
	}
}

func TestMarketContextUsesFullStoreCountFromIngestor(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/market-context":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens_seen_today": 344})
		case "/api/snapshots":
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			rows := make([]map[string]any, 0, limit)
			for i := 0; i < limit; i++ {
				rows = append(rows, map[string]any{
					"mint":        "m",
					"early_proxy": map[string]any{"band": "DEAD", "score": 0},
				})
			}
			_ = json.NewEncoder(w).Encode(rows)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	app := &App{cfg: dashConfig{liveMode: true, ingestorURL: upstream.URL}}
	req := httptest.NewRequest(http.MethodGet, "/api/market-context", nil)
	rr := httptest.NewRecorder()
	app.handleMarketContext(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := int(body["tokens_seen_today"].(float64)); got != 344 {
		t.Fatalf("tokens_seen_today=%d, want full upstream count 344", got)
	}
}
