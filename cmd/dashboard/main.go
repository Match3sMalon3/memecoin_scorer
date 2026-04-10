package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// denylist contains token mints that are never actionable memecoin signals.
// Only infrastructure tokens and stablecoins belong here — not memecoins.
var denylist = map[string]bool{
	"So11111111111111111111111111111111111111112":  true, // wrapped SOL (wSOL)
	"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": true, // USDC
	"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB": true, // USDT (SPL)
}

type Signal struct {
	TokenMint               string  `json:"token_mint"`
	Window                  string  `json:"window"`
	PredictedTradeable      bool    `json:"predicted_tradeable"`
	PredictedCleanTradeable bool    `json:"predicted_clean_tradeable"`
	OpportunityScore        float64 `json:"opportunity_score"`
	OpportunityComponent    float64 `json:"opportunity_component"`
	AdversarialComponent    float64 `json:"adversarial_component"`
	MonetizationComponent   float64 `json:"monetization_component"`
	SniperIntensityRatio    float64 `json:"sniper_intensity_ratio"`
	FirstMinuteShare        float64 `json:"first_minute_share"`
	WinnerExitRatio         float64 `json:"winner_exit_ratio"`
	ActualTradeable         bool    `json:"actual_tradeable"`
	ActualCleanTradeable    bool    `json:"actual_clean_tradeable"`
}

// dashConfig holds the runtime configuration resolved from environment variables.
type dashConfig struct {
	liveMode        bool
	ingestorURL     string
	refreshInterval int    // seconds; live mode only
	listenPort      string // resolved by resolveDashboardPort
}

// resolveDashboardPort resolves the port the dashboard HTTP server binds to.
//
// Priority (highest to lowest):
//  1. DASHBOARD_PORT — explicit dashboard port; always wins
//  2. PORT           — legacy single-service var; only used when DASHBOARD_PORT is absent
//  3. "8090"         — hard default
//
// The Makefile always sets DASHBOARD_PORT explicitly and clears PORT when
// starting the dashboard, so PORT=8080 from the ingestor's env is never
// inherited by the dashboard process.
func resolveDashboardPort() string {
	if p := os.Getenv("DASHBOARD_PORT"); p != "" {
		return p
	}
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8090"
}

func resolveConfig() dashConfig {
	cfg := dashConfig{
		liveMode:        os.Getenv("LIVE_MODE") == "1",
		ingestorURL:     os.Getenv("INGESTOR_URL"),
		refreshInterval: 10,
		listenPort:      resolveDashboardPort(),
	}
	if cfg.ingestorURL == "" {
		cfg.ingestorURL = "http://localhost:8080"
	}
	if s := os.Getenv("REFRESH_INTERVAL_SEC"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			cfg.refreshInterval = v
		}
	}
	return cfg
}

type App struct {
	cfg              dashConfig
	byWindow         map[string][]Signal // offline mode only
	mu               sync.RWMutex
	cachedLiveRows   []map[string]any
	cachedLiveRowsAt time.Time
}

func main() {
	cfg := resolveConfig()

	app := &App{
		cfg:      cfg,
		byWindow: map[string][]Signal{},
	}

	if !cfg.liveMode {
		if err := app.reload(); err != nil {
			log.Printf("warning: loading CSVs: %v (serving empty state)", err)
		}
	}

	mode := "OFFLINE"
	if cfg.liveMode {
		mode = "LIVE (ingestor: " + cfg.ingestorURL + ", refresh: " + strconv.Itoa(cfg.refreshInterval) + "s)"
		// Non-blocking startup probe: warn immediately if the ingestor appears unreachable.
		probeIngestor(cfg.ingestorURL)
	}
	log.Printf("dashboard mode: %s", mode)

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/api/config", app.handleConfig)                  // mode metadata for JS
	mux.HandleFunc("/api/signals", app.handleSignals)                // offline mode
	mux.HandleFunc("/api/live-signals", app.handleSignals)           // offline alias
	mux.HandleFunc("/api/live-snapshots", app.handleLiveSnapshots)   // live mode proxy
	mux.HandleFunc("/api/ingestor-health", app.handleIngestorHealth) // proxy ingestor /healthz
	mux.HandleFunc("/api/refresh", app.handleRefresh)                // offline mode

	addr := ":" + cfg.listenPort
	log.Printf("dashboard listening on http://localhost:%s", cfg.listenPort)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// probeIngestor does a best-effort GET /healthz against the ingestor at startup.
func probeIngestor(baseURL string) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		log.Printf("WARNING: ingestor probe failed (%s/healthz): %v", baseURL, err)
		log.Printf("WARNING: dashboard is running in LIVE mode but ingestor appears unreachable — live table will be empty until it responds")
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("WARNING: ingestor /healthz returned HTTP %d — live signals may be unavailable", resp.StatusCode)
		return
	}
	log.Printf("ingestor reachable at %s", baseURL)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleConfig returns mode metadata consumed by the frontend JS.
func (a *App) handleConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"live_mode":        a.cfg.liveMode,
		"ingestor_url":     a.cfg.ingestorURL,
		"refresh_interval": a.cfg.refreshInterval,
	})
}

// fetchLiveSnapshots calls the ingestor's /api/snapshots endpoint.
func (a *App) fetchLiveSnapshots(minBuyers int, sinceMinutes int, limit int, actionableOnly bool) ([]byte, error) {
	ao := "0"
	if actionableOnly {
		ao = "1"
	}
	url := fmt.Sprintf("%s/api/snapshots?min_buyers=%d&since_minutes=%d&limit=%d&actionable_only=%s",
		a.cfg.ingestorURL, minBuyers, sinceMinutes, limit, ao)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("ingestor unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ingestor returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading ingestor response: %w", err)
	}
	return body, nil
}

func (a *App) loadLiveRows(minBuyers int, sinceMinutes int, limit int, actionableOnly bool) ([]map[string]any, error) {
	body, err := a.fetchLiveSnapshots(minBuyers, sinceMinutes, limit, actionableOnly)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode live rows: %w", err)
	}
	return rows, nil
}

func cloneRows(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		copied := make(map[string]any, len(row))
		for k, v := range row {
			copied[k] = v
		}
		out[i] = copied
	}
	return out
}

func (a *App) setCachedLiveRows(rows []map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cachedLiveRows = cloneRows(rows)
	a.cachedLiveRowsAt = time.Now()
}

func (a *App) getCachedLiveRows(maxAge time.Duration) []map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.cachedLiveRows) == 0 {
		return nil
	}
	if time.Since(a.cachedLiveRowsAt) > maxAge {
		return nil
	}
	return cloneRows(a.cachedLiveRows)
}

// noCacheHeaders sets response headers that prevent browsers and proxies from
// caching the response.  Call before writing any body.
func noCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

// handleIngestorHealth proxies GET /healthz from the ingestor so the browser
// can read clustering status without CORS issues.
// Returns 503 on upstream failure — never fabricates a health response.
func (a *App) handleIngestorHealth(w http.ResponseWriter, _ *http.Request) {
	noCacheHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	if !a.cfg.liveMode {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	upstream := a.cfg.ingestorURL + "/healthz"
	resp, err := client.Get(upstream)
	if err != nil {
		log.Printf("ingestor-health proxy: upstream fetch failed: %v", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"ingestor unreachable"}`))
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("ingestor-health proxy: read body failed: %v", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"read error"}`))
		return
	}
	log.Printf("ingestor-health proxy: upstream=%s status=%d body=%s", upstream, resp.StatusCode, body)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// handleLiveSnapshots proxies the ingestor /api/snapshots call.
func (a *App) handleLiveSnapshots(w http.ResponseWriter, r *http.Request) {
	noCacheHeaders(w)
	w.Header().Set("Content-Type", "application/json")

	if !a.cfg.liveMode {
		_, _ = w.Write([]byte(`[]`))
		return
	}

	q := r.URL.Query()
	minBuyers, _ := strconv.Atoi(q.Get("min_buyers"))
	if minBuyers < 0 {
		minBuyers = 0
	}
	sinceMinutes, _ := strconv.Atoi(q.Get("since_minutes"))
	if sinceMinutes <= 0 {
		sinceMinutes = 240
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	actionableOnly := q.Get("actionable_only") == "1"

	rows, err := a.loadLiveRows(minBuyers, sinceMinutes, limit, actionableOnly)
	if err != nil {
		log.Printf("live-snapshots: %v", err)
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	pricedRows := 0
	marketCapRows := 0
	whyNowRows := 0
	for _, row := range rows {
		if v, ok := row["last_price_sol"].(float64); ok && v > 0 {
			pricedRows++
		}
		if v, ok := row["market_cap_sol"].(float64); ok && v > 0 {
			marketCapRows++
		}
		if v, ok := row["why_now"].(string); ok && strings.TrimSpace(v) != "" {
			whyNowRows++
		}
	}
	log.Printf("dashboard live rows: row_count=%d priced_rows=%d market_cap_rows=%d why_now_rows=%d min_buyers=%d since_minutes=%d actionable_only=%t",
		len(rows), pricedRows, marketCapRows, whyNowRows, minBuyers, sinceMinutes, actionableOnly)
	a.setCachedLiveRows(rows)
	body, err := json.Marshal(rows)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "encode live rows failed"})
		return
	}

	_, _ = w.Write(body)
}

func (a *App) reload() error {
	seven, err := loadSignals("results_7d_scored.csv", "7d")
	if err != nil {
		log.Printf("warning: 7d CSV: %v", err)
		seven = []Signal{}
	}
	fourteen, err := loadSignals("results_14d_scored.csv", "14d")
	if err != nil {
		log.Printf("warning: 14d CSV: %v", err)
		fourteen = []Signal{}
	}

	a.byWindow["7d"] = seven
	a.byWindow["14d"] = fourteen

	if len(seven) == 0 && len(fourteen) == 0 {
		return fmt.Errorf("both CSV files missing or empty")
	}
	return nil
}

func loadSignals(path, window string) ([]Signal, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(rows) < 2 {
		return []Signal{}, nil
	}

	header := rows[0]
	idx := map[string]int{}
	for i, h := range header {
		idx[h] = i
	}

	get := func(row []string, key string) string {
		i, ok := idx[key]
		if !ok || i >= len(row) {
			return ""
		}
		return row[i]
	}

	out := make([]Signal, 0, len(rows)-1)
	for _, row := range rows[1:] {
		mint := get(row, "token_mint")
		if denylist[mint] {
			continue
		}
		s := Signal{
			TokenMint:               mint,
			Window:                  window,
			PredictedTradeable:      parseBool(get(row, "predicted_tradeable")),
			PredictedCleanTradeable: parseBool(get(row, "predicted_clean_tradeable")),
			OpportunityScore:        parseFloat(get(row, "opportunity_score")),
			OpportunityComponent:    parseFloat(get(row, "opportunity_component")),
			AdversarialComponent:    parseFloat(get(row, "adversarial_component")),
			MonetizationComponent:   parseFloat(get(row, "monetization_component")),
			SniperIntensityRatio:    parseFloat(get(row, "sniper_intensity_ratio")),
			FirstMinuteShare:        parseFloat(get(row, "first_minute_share")),
			WinnerExitRatio:         parseFloat(get(row, "winner_exit_ratio")),
			ActualTradeable:         parseBool(get(row, "actual_tradeable")),
			ActualCleanTradeable:    parseBool(get(row, "actual_clean_tradeable")),
		}
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].OpportunityScore == out[j].OpportunityScore {
			return out[i].TokenMint < out[j].TokenMint
		}
		return out[i].OpportunityScore > out[j].OpportunityScore
	})

	return out, nil
}

func parseBool(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "true" || v == "1" || v == "yes"
}

func parseFloat(v string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return f
}

func (a *App) handleSignals(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "7d"
	}
	signals, ok := a.byWindow[window]
	if !ok {
		http.Error(w, "invalid window, use 7d or 14d", http.StatusBadRequest)
		return
	}

	tradeableOnly := r.URL.Query().Get("tradeable_only") == "1"
	cleanOnly := r.URL.Query().Get("clean_only") == "1"

	filtered := make([]Signal, 0, len(signals))
	var tradeableCount, cleanCount int
	for _, s := range signals {
		if s.PredictedTradeable {
			tradeableCount++
		}
		if s.PredictedCleanTradeable {
			cleanCount++
		}

		if tradeableOnly && !s.PredictedTradeable {
			continue
		}
		if cleanOnly && !s.PredictedCleanTradeable {
			continue
		}
		filtered = append(filtered, s)
	}

	resp := map[string]any{
		"window":          window,
		"total_rows":      len(signals),
		"tradeable_count": tradeableCount,
		"clean_count":     cleanCount,
		"returned_count":  len(filtered),
		"signals":         filtered,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *App) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if err := a.reload(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true,
	})
}

func (a *App) handleIndex(w http.ResponseWriter, _ *http.Request) {
	noCacheHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(a.renderIndexHTML()))
}

func (a *App) renderIndexHTML() string {
	page := indexHTML
	bestHeadlineValue := "No high-conviction setup right now"
	bestVerdictValue := ""
	bestBlockerValue := ""
	bestEvidenceValue := "Waiting for enough live structure to judge."
	bestMetaValue := ""
	marketCopyValue := "Anyone can see motion. This terminal tries to distinguish organic emergence from compressed, fallback-affected, or structurally poisoned flow."
	marketMetaValue := ""
	initialBodyValue := ""
	if !a.cfg.liveMode {
		page = strings.ReplaceAll(page, "__INITIAL_BEST_HEADLINE__", html.EscapeString(bestHeadlineValue))
		page = strings.ReplaceAll(page, "__INITIAL_BEST_VERDICT__", html.EscapeString(bestVerdictValue))
		page = strings.ReplaceAll(page, "__INITIAL_BEST_BLOCKER__", html.EscapeString(bestBlockerValue))
		page = strings.ReplaceAll(page, "__INITIAL_BEST_EVIDENCE__", bestEvidenceValue)
		page = strings.ReplaceAll(page, "__INITIAL_BEST_META__", bestMetaValue)
		page = strings.ReplaceAll(page, "__INITIAL_MARKET_COPY__", html.EscapeString(marketCopyValue))
		page = strings.ReplaceAll(page, "__INITIAL_MARKET_META__", marketMetaValue)
		page = strings.ReplaceAll(page, "__INITIAL_TBODY__", initialBodyValue)
		return page
	}

	rows := a.getCachedLiveRows(10 * time.Minute)
	if len(rows) == 0 {
		var err error
		rows, err = a.loadLiveRows(0, 240, 20, false)
		if err != nil {
			log.Printf("renderIndexHTML: live bootstrap unavailable: %v", err)
			page = strings.ReplaceAll(page, "__INITIAL_BEST_HEADLINE__", html.EscapeString(bestHeadlineValue))
			page = strings.ReplaceAll(page, "__INITIAL_BEST_VERDICT__", html.EscapeString(bestVerdictValue))
			page = strings.ReplaceAll(page, "__INITIAL_BEST_BLOCKER__", html.EscapeString(bestBlockerValue))
			page = strings.ReplaceAll(page, "__INITIAL_BEST_EVIDENCE__", bestEvidenceValue)
			page = strings.ReplaceAll(page, "__INITIAL_BEST_META__", bestMetaValue)
			page = strings.ReplaceAll(page, "__INITIAL_MARKET_COPY__", html.EscapeString(marketCopyValue))
			page = strings.ReplaceAll(page, "__INITIAL_MARKET_META__", marketMetaValue)
			page = strings.ReplaceAll(page, "__INITIAL_TBODY__", initialBodyValue)
			return page
		}
		a.setCachedLiveRows(rows)
	}
	bestHeadlineValue = bestHeadline(rows)
	bestVerdictValue = bestVerdictText(rows)
	bestBlockerValue = bestBlockerText(rows)
	bestEvidenceValue = bestEvidenceText(rows)
	bestMetaValue = bestMetaHTML(rows)
	marketCopyValue = marketCopy(rows)
	marketMetaValue = marketMetaHTML(rows)
	initialBodyValue = renderInitialRows(rows)
	page = strings.ReplaceAll(page, "__INITIAL_BEST_HEADLINE__", html.EscapeString(bestHeadlineValue))
	page = strings.ReplaceAll(page, "__INITIAL_BEST_VERDICT__", html.EscapeString(bestVerdictValue))
	page = strings.ReplaceAll(page, "__INITIAL_BEST_BLOCKER__", html.EscapeString(bestBlockerValue))
	page = strings.ReplaceAll(page, "__INITIAL_BEST_EVIDENCE__", bestEvidenceValue)
	page = strings.ReplaceAll(page, "__INITIAL_BEST_META__", bestMetaValue)
	page = strings.ReplaceAll(page, "__INITIAL_MARKET_COPY__", html.EscapeString(marketCopyValue))
	page = strings.ReplaceAll(page, "__INITIAL_MARKET_META__", marketMetaValue)
	page = strings.ReplaceAll(page, "__INITIAL_TBODY__", initialBodyValue)
	return page
}

func renderInitialRows(rows []map[string]any) string {
	if len(rows) == 0 {
		return "<tr><td colspan='18' style='text-align:center;color:#8ea0c3;padding:32px'>No live signals yet — waiting for webhook activity</td></tr>"
	}
	var b strings.Builder
	for _, s := range rows {
		mint := stringFieldMap(s, "mint")
		tokenHref := stringFieldMap(s, "execution_url")
		decision := stringFieldMap(s, "decision")
		verdict := stringFieldMap(s, "operator_verdict")
		whyNow := stringFieldMap(s, "why_now")
		blocker := firstNonEmpty(stringFieldMap(s, "dominant_blocker"), stringFieldMap(s, "why_not_higher"))
		state := stringFieldMap(s, "signal_state")
		if state == "" {
			state = "expired"
		}
		rowClusterStatus := stringFieldMap(s, "clustering_row_status")
		if rowClusterStatus == "" {
			rowClusterStatus = "resolved"
		}
		clusterCompression := "0% compressed"
		if pct := int(floatFieldMap(s, "funding_cluster_ratio") * 100); pct > 0 {
			clusterCompression = fmt.Sprintf("%d%% compressed", pct)
		}
		rawBuyers := int(floatFieldMap(s, "buyers_last1m"))
		effBuyers := int(floatFieldMap(s, "effective_buyers_1m"))
		decisionClass := "avoid"
		switch decision {
		case "BUY":
			decisionClass = "buy"
		case "READY":
			decisionClass = "ready"
		case "WATCH":
			decisionClass = "watch"
		}
		tier := visualTierGo(verdict, s)
		rowClass := "row-risk"
		if tier == "clean" {
			rowClass = "row-strong"
		} else if tier == "compromised" {
			rowClass = "row-watch"
		}
		qualityBadge := "<span class='badge poison'>low conviction</span>"
		if tier == "clean" {
			qualityBadge = "<span class='badge cleanflow'>clean</span>"
		} else if tier == "compromised" {
			qualityBadge = "<span class='badge partial'>compromised</span>"
		}
		clusterLabel := clusteringSurfaceLabelGo(s)
		clusterBadge := " <span class='badge cleanflow'>resolved</span>"
		if rowClusterStatus != "resolved" {
			clusterBadge = " <span class='badge partial'>" + html.EscapeString(strings.ReplaceAll(rowClusterStatus, "_", " ")) + "</span>"
		}
		liqMc := "n/a"
		volMc := "n/a"
		if eng, ok := s["engine"].(map[string]any); ok {
			if g1 := findGateGo(eng, 1); g1 != nil {
				if !boolFieldMap(g1, "skipped") {
					liqMc = fmt.Sprintf("%.1f%%", floatFieldMap(g1, "value"))
				} else {
					liqMc = compactMissingStructureGo(firstNonEmpty(stringFieldMap(s, "market_cap_reason"), stringFieldMap(g1, "reason"), "market cap unavailable"))
				}
			}
			if g4 := findGateGo(eng, 4); g4 != nil {
				if !boolFieldMap(g4, "skipped") {
					volMc = fmt.Sprintf("%.1f%%", floatFieldMap(g4, "value"))
				} else {
					volMc = compactMissingStructureGo(firstNonEmpty(stringFieldMap(s, "market_cap_reason"), stringFieldMap(g4, "reason"), "market cap unavailable"))
				}
			}
		}
		shortMint := mint
		if len(shortMint) > 8 {
			shortMint = shortMint[:8] + "…"
		}
		fmt.Fprintf(&b,
			"<tr class=\"live-row %s\"><td><span class='badge %s'>%s</span></td><td class=\"verdict-label\"><strong>%s</strong></td><td>%d</td><td><span class='badge %s'>%s</span></td><td class=\"exec-cell\"><div class='token-cell'><div class='token-meta'><div class='token-actions'><a class='token-link mono' href='%s' target='_blank' rel='noopener noreferrer'>%s</a><a href=\"%s\" class=\"gmgn-link exec-link\" target=\"_blank\">EXECUTE [GMGN]</a></div><span class='token-sub'>%s</span></div></div></td><td><div class='metric-stack'><span>%d raw / %d eff</span><span class='metric-sub'>%s</span></div></td><td><div class='metric-stack'><span>%s%s</span><span class='metric-sub'>%s</span></div></td><td>%.2f/%.2f</td><td>%.2f</td><td>%.2f</td><td>%.2f</td><td>%.1f%%</td><td>%.1fm</td><td>%s</td><td>%s</td><td>%s</td><td class=\"why-now-cell\">%s</td><td class=\"blocker-cell\">%s</td></tr>",
			rowClass,
			decisionClass, html.EscapeString(decision),
			html.EscapeString(verdict),
			int(floatFieldMap(s, "confidence_score")+0.5),
			stateClassGo(state), html.EscapeString(state),
			html.EscapeString(tokenHref), html.EscapeString(shortMint), html.EscapeString(tokenHref), qualityBadge,
			rawBuyers, effBuyers, html.EscapeString(buyerQualityLabelGo(s)),
			html.EscapeString(clusterLabel), clusterBadge, html.EscapeString(clusterCompression),
			floatFieldMap(s, "buy_sol_last_1m"), floatFieldMap(s, "sell_sol_last_1m"),
			floatFieldMap(s, "buyer_acceleration"), floatFieldMap(s, "execution_penalty"), floatFieldMap(s, "adversarial_score"),
			floatFieldMap(s, "estimated_impact_pct"),
			floatFieldMap(s, "age_seconds")/60.0,
			html.EscapeString(gatesCellGo(s)),
			html.EscapeString(liqMc), html.EscapeString(volMc),
			html.EscapeString(whyNow), html.EscapeString(blocker),
		)
	}
	return b.String()
}

func bestHeadline(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return "No high-conviction setup right now"
	}
	shortMint := stringFieldMap(best, "mint")
	if len(shortMint) > 8 {
		shortMint = shortMint[:8] + "…"
	}
	tier := visualTierGo(stringFieldMap(best, "operator_verdict"), best)
	decision := stringFieldMap(best, "decision")
	if tier == "clean" && boolFieldMap(best, "is_actionable") {
		return shortMint + " is the cleanest live setup"
	}
	if decision == "WATCH" || decision == "READY" || decision == "BUY" {
		return shortMint + " is the best available, not a free pass"
	}
	return "Best available, but still low-conviction"
}

func bestEvidenceText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return "Waiting for enough live structure to judge."
	}
	return html.EscapeString(firstNonEmpty(stringFieldMap(best, "why_now"), ""))
}

func bestMetaHTML(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	decision := stringFieldMap(best, "decision")
	verdict := firstNonEmpty(stringFieldMap(best, "operator_verdict"), "low-confidence setup")
	tier := visualTierGo(verdict, best)
	return "<span class='badge " + decisionBadgeClassGo(decision) + "'>" + html.EscapeString(decision) + "</span>" +
		"<span class='badge verdict-label " + tierBadgeClassGo(tier) + "'>" + html.EscapeString(verdict) + "</span>" +
		"<span class='badge neutral'>conf " + fmt.Sprintf("%d", int(floatFieldMap(best, "confidence_score")+0.5)) + "</span>" +
		"<span class='badge neutral'>" + html.EscapeString(clusteringSurfaceLabelGo(best)) + "</span>"
}

func bestVerdictText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return html.EscapeString(firstNonEmpty(stringFieldMap(best, "operator_verdict"), ""))
}

func bestBlockerText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "Blocker: " + html.EscapeString(firstNonEmpty(stringFieldMap(best, "dominant_blocker"), stringFieldMap(best, "why_not_higher"), ""))
}

func marketCopy(rows []map[string]any) string {
	total := len(rows)
	clean := 0
	partial := 0
	full := 0
	missingMC := 0
	for _, row := range rows {
		if visuallyCleanGo(row) {
			clean++
		}
		switch stringFieldMap(row, "clustering_row_status") {
		case "partial_fallback":
			partial++
		case "full_fallback":
			full++
		}
		if floatFieldMap(row, "market_cap_sol") <= 0 {
			missingMC++
		}
	}
	if total == 0 {
		return "No flow yet. The terminal stays quiet until there is enough structure to judge."
	}
	if clean == 0 {
		return "Current tape is active but not trustworthy yet. Most names are structurally weak, fallback-affected, or missing enough data to size with confidence."
	}
	if partial+full > clean {
		return "There is some cleaner flow, but fallback and compression still dominate the screen. Treat most motion as suspicious before it proves otherwise."
	}
	_ = missingMC
	return "A few names are structurally cleaner than the rest, but the terminal is still biased toward disqualifying weak flow over manufacturing excitement."
}

func marketMetaHTML(rows []map[string]any) string {
	total := len(rows)
	clean := 0
	partial := 0
	full := 0
	missingMC := 0
	for _, row := range rows {
		if visuallyCleanGo(row) {
			clean++
		}
		switch stringFieldMap(row, "clustering_row_status") {
		case "partial_fallback":
			partial++
		case "full_fallback":
			full++
		}
		if floatFieldMap(row, "market_cap_sol") <= 0 {
			missingMC++
		}
	}
	return "<span class='badge neutral'>" + fmt.Sprintf("%d rows tracked", total) + "</span>" +
		"<span class='badge cleanflow'>" + fmt.Sprintf("%d structurally cleaner", clean) + "</span>" +
		"<span class='badge partial'>" + fmt.Sprintf("%d fallback-affected", partial+full) + "</span>" +
		"<span class='badge poison'>" + fmt.Sprintf("%d incomplete MC", missingMC) + "</span>"
}

func chooseBestSetupGo(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return nil
	}
	best := rows[0]
	bestScore := bestSetupScoreGo(best)
	for _, row := range rows[1:] {
		score := bestSetupScoreGo(row)
		if score > bestScore {
			best = row
			bestScore = score
		}
	}
	return best
}

func bestSetupScoreGo(s map[string]any) float64 {
	score := floatFieldMap(s, "confidence_score")
	if boolFieldMap(s, "is_actionable") {
		score += 18
	}
	switch stringFieldMap(s, "decision") {
	case "BUY":
		score += 18
	case "READY":
		score += 12
	case "WATCH":
		score += 8
	}
	if stringFieldMap(s, "clustering_row_status") == "resolved" {
		score += 10
	}
	if floatFieldMap(s, "funding_cluster_ratio") == 0 {
		score += 6
	}
	if impact := floatFieldMap(s, "estimated_impact_pct"); impact > 0 && impact < 5 {
		score += 8
	}
	if exec := floatFieldMap(s, "execution_penalty"); exec >= 0.5 {
		score += 8
	}
	if floatFieldMap(s, "market_cap_sol") > 0 {
		score += 4
	}
	if floatFieldMap(s, "adversarial_score") > 0.75 {
		score -= 10
	}
	if stringFieldMap(s, "clustering_row_status") == "full_fallback" {
		score -= 12
	}
	return score
}

func visualTierGo(verdict string, s map[string]any) string {
	v := strings.ToLower(verdict)
	if strings.Contains(v, "clean-ish") || strings.Contains(v, "best of bad tape") || strings.Contains(v, "watchable") {
		return "compromised"
	}
	decision := stringFieldMap(s, "decision")
	if (decision == "BUY" || decision == "READY") && stringFieldMap(s, "clustering_row_status") == "resolved" {
		return "clean"
	}
	return "weak"
}

func visuallyCleanGo(s map[string]any) bool {
	return visualTierGo(stringFieldMap(s, "operator_verdict"), s) != "weak"
}

func decisionBadgeClassGo(decision string) string {
	switch decision {
	case "BUY":
		return "buy"
	case "READY":
		return "ready"
	case "WATCH":
		return "watch"
	default:
		return "avoid"
	}
}

func tierBadgeClassGo(tier string) string {
	switch tier {
	case "clean":
		return "cleanflow"
	case "compromised":
		return "partial"
	default:
		return "poison"
	}
}

func tierLabelGo(tier string) string {
	switch tier {
	case "clean":
		return "structurally clean"
	case "compromised":
		return "partially compromised"
	default:
		return "low confidence"
	}
}

func clusteringSurfaceLabelGo(s map[string]any) string {
	switch stringFieldMap(s, "clustering_row_status") {
	case "", "resolved":
		return "clean"
	case "partial_fallback":
		return "partial fallback"
	default:
		return "full fallback"
	}
}

func buyerQualityLabelGo(s map[string]any) string {
	raw := int(floatFieldMap(s, "buyers_last1m"))
	eff := int(floatFieldMap(s, "effective_buyers_1m"))
	if raw == 0 && eff == 0 {
		return "no real buy pressure"
	}
	if raw > 0 && eff == 0 {
		return "fully compressed"
	}
	if eff < raw {
		return "compressed after clustering"
	}
	if floatFieldMap(s, "funding_cluster_ratio") == 0 {
		return "organic buyer set"
	}
	return "mixed buyer quality"
}

func stateClassGo(state string) string {
	switch state {
	case "fresh":
		return "fresh"
	case "stale":
		return "stale"
	default:
		return "expired"
	}
}

func gatesCellGo(s map[string]any) string {
	eng, ok := s["engine"].(map[string]any)
	if !ok {
		return "–"
	}
	if boolFieldMap(eng, "layer0_reject") {
		reason := stringFieldMap(eng, "layer0_reason")
		if reason == "" {
			reason = "reject"
		}
		return "L0: " + reason
	}
	pass := int(floatFieldMap(eng, "gates_pass_count"))
	gates, ok := eng["gates"].([]any)
	total := 7
	if ok && len(gates) > 0 {
		total = len(gates)
	}
	maxLabel := stringFieldMap(eng, "max_label")
	if maxLabel != "" && maxLabel != "BUY" {
		return fmt.Sprintf("%d/%d →%s", pass, total, maxLabel)
	}
	return fmt.Sprintf("%d/%d", pass, total)
}

func findGateGo(eng map[string]any, gateID int) map[string]any {
	gates, ok := eng["gates"].([]any)
	if !ok {
		return nil
	}
	for _, item := range gates {
		gate, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if int(floatFieldMap(gate, "gate_id")) == gateID {
			return gate
		}
	}
	return nil
}

func compactMissingStructureGo(reason string) string {
	text := strings.ToLower(reason)
	switch {
	case strings.Contains(text, "holder balance"):
		return "no holder proxy"
	case strings.Contains(text, "market cap"):
		return "no market cap"
	case strings.Contains(text, "token not yet"):
		return "too young"
	case strings.Contains(text, "not yet observed"):
		return "incomplete"
	default:
		return "n/a"
	}
}

func stringFieldMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func floatFieldMap(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

func boolFieldMap(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Anti-Bullshit Live Terminal</title>
<style>
*{box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:radial-gradient(circle at top,#16223c 0%,#0b1020 38%,#070b16 100%);color:#e8ecf3;margin:0;padding:20px}
h1{margin:0 0 4px 0;font-size:28px}
.hero{display:flex;flex-wrap:wrap;justify-content:space-between;gap:16px;align-items:flex-end;margin-bottom:14px}
.hero-copy{max-width:860px}
.eyebrow{color:#89a4d8;font-size:11px;font-weight:800;letter-spacing:.14em;text-transform:uppercase;margin-bottom:8px}
.subhead{color:#c6d4ee;font-size:15px;line-height:1.5;max-width:920px}
.purpose{margin-top:10px;color:#8ea0c3;font-size:13px;max-width:920px}
.mode-badge{display:inline-block;padding:3px 10px;border-radius:999px;font-size:12px;font-weight:700;margin-bottom:12px}
.mode-live{background:#153d2b;color:#7ef0b2}
.mode-offline{background:#2a2230;color:#b9a8c9}

/* Status strip */
.status-strip{display:flex;flex-wrap:wrap;gap:16px;align-items:center;background:#0d1526;border:1px solid #1e2f4a;border-radius:10px;padding:10px 16px;margin-bottom:12px;font-size:13px}
.status-dot{display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:5px}
.dot-ok{background:#7ef0b2}
.dot-warn{background:#ffd76a}
.dot-err{background:#f87171}

.controls{display:flex;flex-wrap:wrap;gap:12px;margin-bottom:12px;align-items:center}
button,select,label{font-size:14px}
button{background:#1d4ed8;color:white;border:0;border-radius:10px;padding:8px 14px;cursor:pointer}
button.secondary{background:#1f2937}
button:disabled{opacity:.45;cursor:default}
.summary-grid{display:grid;grid-template-columns:minmax(320px,1.3fr) minmax(280px,.9fr);gap:12px;margin-bottom:12px}
.panel{background:linear-gradient(180deg,rgba(18,26,43,.96),rgba(12,19,33,.98));border:1px solid #23304d;border-radius:16px;padding:14px}
.panel-title{font-size:11px;color:#8ea0c3;text-transform:uppercase;letter-spacing:.12em;font-weight:800;margin-bottom:8px}
.panel-headline{font-size:22px;font-weight:800;line-height:1.2;margin-bottom:8px}
.panel-copy{color:#c9d6ef;font-size:14px;line-height:1.5}
.panel-copy strong{color:#ffffff}
.best-meta,.market-meta{display:flex;flex-wrap:wrap;gap:8px;margin-top:10px}
.cardrow{display:grid;grid-template-columns:repeat(4,minmax(140px,1fr));gap:10px;margin-bottom:12px}
.card{background:#121a2b;border:1px solid #23304d;border-radius:12px;padding:12px}
.card .label{font-size:11px;color:#9fb0d0;margin-bottom:4px}
.card .value{font-size:22px;font-weight:700}
.tablewrap{overflow:auto;background:#121a2b;border:1px solid #23304d;border-radius:12px}
table{width:100%;border-collapse:collapse}
th,td{padding:9px 8px;border-bottom:1px solid #1a2640;text-align:left;font-size:12px;white-space:nowrap}
th{position:sticky;top:0;background:#121a2b;z-index:1;font-size:11px;color:#9fb0d0;text-transform:uppercase;letter-spacing:.04em}
.badge{display:inline-block;padding:2px 7px;border-radius:999px;font-size:11px;font-weight:600}
.badge.buy{background:#0f3a24;color:#7ef0b2;border:1px solid #2a6644}
.badge.ready{background:#1e3a07;color:#a3e635;border:1px solid #3d6e10}
.badge.watch{background:#3a2c07;color:#ffd76a;border:1px solid #6e540e}
.badge.avoid{background:#2a1515;color:#f87171;border:1px solid #6e2a2a}
.badge.tradeable{background:#153d2b;color:#7ef0b2}
.badge.clean{background:#3a2c07;color:#ffd76a}
.badge.no{background:#2a2230;color:#b9a8c9}
.badge.fresh{background:#0d2a1a;color:#7ef0b2;font-size:10px}
.badge.stale{background:#2e2200;color:#ffd76a;font-size:10px}
.badge.expired{background:#1f1010;color:#9ca3af;font-size:10px}
.badge.clustered{background:#1a1535;color:#a78bfa;font-size:10px}
.badge.cleanflow{background:#113627;color:#83f2b4;border:1px solid #27563a}
.badge.partial{background:#31260c;color:#ffd76a;border:1px solid #61470d}
.badge.poison{background:#361515;color:#ff9b9b;border:1px solid #703030}
.badge.neutral{background:#172338;color:#9db2d8;border:1px solid #304666}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:11px}
small{color:#8ea0c3}
.dim{color:#5a7090}
.why-now{color:#d8f99a;font-size:11px;max-width:220px;overflow:hidden;text-overflow:ellipsis;font-weight:600}
.why-not{color:#fff1f1;font-size:11px;max-width:220px;overflow:hidden;text-overflow:ellipsis;font-weight:700}
.token-cell{display:flex;align-items:center;gap:8px}
.token-meta{display:flex;flex-direction:column;gap:3px}
.token-sub{font-size:10px;color:#8196bb}
.token-link{color:#dbe8ff;text-decoration:none;font-weight:700}
.token-link:hover{color:#ffffff;text-decoration:underline}
.token-actions{display:flex;gap:6px;align-items:center}
.exec-link{font-size:10px;color:#7ef0b2;text-decoration:none;border:1px solid #27563a;background:#113627;padding:2px 6px;border-radius:999px}
.exec-link:hover{text-decoration:underline}
.metric-stack{display:flex;flex-direction:column;gap:3px}
.metric-sub{font-size:10px;color:#8196bb}
.blocker-cell{background:rgba(110,42,42,.16)}
.row-strong{box-shadow:inset 4px 0 0 #7ef0b2;background:rgba(18,42,30,.22)}
.row-watch{box-shadow:inset 4px 0 0 #ffd76a;background:rgba(58,44,7,.14)}
.row-risk{box-shadow:inset 4px 0 0 #f87171}
.cluster-banner{display:none;background:#3b0d0d;border:1px solid #7f1d1d;border-radius:10px;padding:10px 16px;margin-bottom:12px;font-size:13px;font-weight:700;color:#fca5a5}
@media (max-width: 980px){
  .summary-grid{grid-template-columns:1fr}
  .cardrow{grid-template-columns:repeat(2,minmax(140px,1fr))}
}
</style>
</head>
<body>
<div class="hero">
  <div class="hero-copy">
    <div class="eyebrow">Structural Quality Filter</div>
    <h1>Anti-Bullshit Live Terminal</h1>
    <div class="subhead">Organic vs poisoned flow for Solana memecoins. The terminal is supposed to disqualify junk fast, surface rare cleaner setups, and stay honest when structure is missing or fallback-affected.</div>
    <div class="purpose">Most live flow is structurally bad; this terminal exists to isolate the rare setups that are cleaner than they look.</div>
  </div>
  <div id="modeBadge" class="mode-badge">...</div>
</div>

<!-- Clustering degraded banner (live mode only) -->
<div id="clusterBanner" class="cluster-banner">
  ⚠ CLUSTERING DEGRADED — BUY/READY DISABLED (set HELIUS_API_KEY or FUNDER_MAP_PATH, or set CLUSTER_REQUIRED=0)
</div>

<!-- Status strip (live mode only) -->
<div id="statusStrip" class="status-strip" style="display:none">
  <span id="ingestorHealth"><span class="status-dot dot-warn"></span>Connecting...</span>
  <span id="clusterStatus" class="dim">cluster: –</span>
  <span id="lastPoll" class="dim">–</span>
  <span id="storeMode" class="dim">–</span>
  <span id="freshCount" class="dim">–</span>
</div>

<!-- OFFLINE controls -->
<div id="offlineControls" class="controls" style="display:none">
	<label>Window:
		<select id="window">
			<option value="7d">7d</option>
			<option value="14d">14d</option>
		</select>
	</label>
	<label><input type="checkbox" id="tradeableOnly" checked> tradeable only</label>
	<label><input type="checkbox" id="cleanOnly"> clean only</label>
	<button id="reloadBtn">Reload view</button>
	<button id="refreshBtn" class="secondary">Refresh CSVs</button>
</div>

<!-- LIVE controls -->
<div id="liveControls" class="controls" style="display:none">
	<button id="liveReloadBtn">Refresh now</button>
	<label style="font-size:13px"><input type="checkbox" id="showAllLive" checked> show expired/stale</label>
</div>

<div class="summary-grid" id="liveSummary" style="display:none">
	<div class="panel" id="best-setup-panel">
		<h3>Best Current Setup</h3>
		<div class="panel-headline" id="bestSetupHeadline">__INITIAL_BEST_HEADLINE__</div>
		<div class="verdict" id="bestSetupVerdict">__INITIAL_BEST_VERDICT__</div>
		<div class="blocker" id="bestSetupBlocker">__INITIAL_BEST_BLOCKER__</div>
		<div class="evidence" id="bestSetupEvidence">__INITIAL_BEST_EVIDENCE__</div>
		<div class="best-meta" id="bestSetupMeta">__INITIAL_BEST_META__</div>
	</div>
	<div class="panel">
		<div class="panel-title">Why This Tool Exists</div>
		<div class="panel-copy" id="marketReadCopy">__INITIAL_MARKET_COPY__</div>
		<div class="market-meta" id="marketReadMeta">__INITIAL_MARKET_META__</div>
	</div>
</div>

<div class="cardrow">
	<div class="card"><div class="label">Mode</div><div class="value" id="modeCard">-</div></div>
	<div class="card"><div class="label">Selective view</div><div class="value" id="rowsCard">-</div></div>
	<div class="card"><div class="label">Rare top states</div><div class="value" id="buyers1mCard">-</div></div>
	<div class="card"><div class="label">Structurally clean</div><div class="value" id="accelCard">-</div></div>
</div>

<div class="tablewrap">
<table id="signalTable">
	<thead id="theadOffline" style="display:none">
		<tr>
			<th>token</th><th>score</th><th>tradeable</th><th>clean</th>
			<th>sniper</th><th>first_min</th><th>winner_exit</th>
			<th>opp</th><th>adv</th><th>mon</th>
		</tr>
	</thead>
	<thead id="theadLive" style="display:none">
		<tr>
			<th>decision</th>
			<th>verdict</th>
			<th>conf</th>
			<th>state</th>
			<th>token</th>
			<th title="raw buyers last 1m / effective after clustering">buyer quality</th>
			<th title="clustering trust, compression, and row-level fallback">clustering</th>
			<th title="buy SOL / sell SOL in last 1m">buy/sell 1m</th>
			<th title="buyer acceleration ratio">accel</th>
			<th title="execution quality [0-1]">exec</th>
			<th title="adversarial score [0-1]">adv</th>
			<th title="estimated price impact %">impact%</th>
			<th>age</th>
			<th title="7-gate engine: pass count / 7; L0 = layer-0 hard reject; ceiling label shown when engine caps the decision">gates</th>
			<th title="Gate 1 — liquidity / market-cap ratio %; &lt;3% = avoid, 3-5% = watch floor, &gt;5% = eligible">liq/mc</th>
			<th title="Gate 4 — volume / market-cap ratio %; &lt;2% = low conviction, &gt;5% = healthy">vol/mc</th>
			<th title="why this is worth a glance now">why now</th>
			<th title="dominant disqualifier / missing structure">disqualifier</th>
		</tr>
	</thead>
	<tbody id="tbody">__INITIAL_TBODY__</tbody>
</table>
</div>

<p><small id="tableNote"></small></p>

<script>
// ---- config ----
let CFG = {live_mode: false, ingestor_url: "", refresh_interval: 10};
let liveTimer = null;

async function init() {
	try {
		const r = await fetch("/api/config");
		CFG = await r.json();
	} catch(_) {}

	if (CFG.live_mode) {
		document.getElementById("modeBadge").textContent = "Mode: LIVE";
		document.getElementById("modeBadge").className = "mode-badge mode-live";
		document.getElementById("modeCard").textContent = "LIVE";
		document.getElementById("liveControls").style.display = "flex";
		document.getElementById("liveSummary").style.display = "grid";
		document.getElementById("theadLive").style.display = "";
		document.getElementById("statusStrip").style.display = "flex";
		document.getElementById("tableNote").textContent =
			"Live structural filter — auto-refresh every " + CFG.refresh_interval + "s. The terminal is allowed to say that nothing is good enough.";
		document.getElementById("liveReloadBtn").addEventListener("click", loadLive);
		document.getElementById("showAllLive").addEventListener("change", loadLive);
		startLivePolling();
		loadLive();
	} else {
		document.getElementById("modeBadge").textContent = "Mode: OFFLINE";
		document.getElementById("modeBadge").className = "mode-badge mode-offline";
		document.getElementById("modeCard").textContent = "OFFLINE";
		document.getElementById("offlineControls").style.display = "flex";
		document.getElementById("theadOffline").style.display = "";
		document.getElementById("tableNote").textContent =
			"Offline scorer outputs. Run scorer to refresh CSVs.";
		document.getElementById("reloadBtn").addEventListener("click", loadOffline);
		document.getElementById("refreshBtn").addEventListener("click", refreshCSVs);
		document.getElementById("window").addEventListener("change", loadOffline);
		document.getElementById("tradeableOnly").addEventListener("change", loadOffline);
		document.getElementById("cleanOnly").addEventListener("change", loadOffline);
		loadOffline();
	}
}

// ---- OFFLINE mode ----
async function loadOffline() {
	const windowVal = document.getElementById("window").value;
	const tradeableOnly = document.getElementById("tradeableOnly").checked ? "1" : "0";
	const cleanOnly = document.getElementById("cleanOnly").checked ? "1" : "0";

	let data;
	try {
		const res = await fetch(
			"/api/signals?window=" + encodeURIComponent(windowVal) +
			"&tradeable_only=" + tradeableOnly + "&clean_only=" + cleanOnly
		);
		if (!res.ok) throw new Error("HTTP " + res.status);
		data = await res.json();
	} catch(e) {
		setError("Error loading signals: " + e.message, 10);
		return;
	}

	document.getElementById("rowsCard").textContent = data.returned_count + "/" + data.total_rows;
	document.getElementById("buyers1mCard").textContent = "-";
	document.getElementById("accelCard").textContent = "-";

	const tbody = document.getElementById("tbody");
	tbody.innerHTML = "";

	if (!data.signals || data.signals.length === 0) {
		setEmpty("No fresh signals yet — run the scorer first", 10);
		return;
	}

	for (const s of data.signals) {
		const tr = document.createElement("tr");
		tr.innerHTML =
			"<td class='mono'>" + s.token_mint + "</td>" +
			"<td>" + Number(s.opportunity_score).toFixed(4) + "</td>" +
			td_badge(s.predicted_tradeable, "tradeable") +
			td_badge(s.predicted_clean_tradeable, "clean") +
			"<td>" + Number(s.sniper_intensity_ratio).toFixed(4) + "</td>" +
			"<td>" + Number(s.first_minute_share).toFixed(4) + "</td>" +
			"<td>" + Number(s.winner_exit_ratio).toFixed(4) + "</td>" +
			"<td>" + Number(s.opportunity_component).toFixed(4) + "</td>" +
			"<td>" + Number(s.adversarial_component).toFixed(4) + "</td>" +
			"<td>" + Number(s.monetization_component).toFixed(4) + "</td>";
		tbody.appendChild(tr);
	}
}

async function refreshCSVs() {
	await fetch("/api/refresh");
	await loadOffline();
}

// ---- LIVE mode ----
async function loadLive() {
	const showAll = document.getElementById("showAllLive") && document.getElementById("showAllLive").checked;
	const actionableOnly = showAll ? "0" : "1";

	const ts = Date.now();
	let snapshots;
	try {
		const res = await fetch(
			"/api/live-snapshots?min_buyers=0&since_minutes=240&limit=100&actionable_only=" + actionableOnly + "&ts=" + ts,
			{ cache: "no-store" }
		);
		if (!res.ok) throw new Error("HTTP " + res.status);
		snapshots = await res.json();
	} catch(e) {
		updateStatusStrip(false, null, null, null);
		setError("Live ingestor unreachable: " + e.message, 18);
		return;
	}

	// Fetch healthz for clustering status.
	// Use cache:no-store + ts param so browsers never serve a stale response.
	let healthData = null;
	try {
		const hr = await fetch("/api/ingestor-health?ts=" + ts, { cache: "no-store" });
		if (hr.ok) healthData = await hr.json();
	} catch(_) {}

	const now = new Date();
	updateStatusStrip(true, now, snapshots, healthData);

	if (!snapshots || snapshots.length === 0) {
		document.getElementById("rowsCard").textContent = "0";
		document.getElementById("buyers1mCard").textContent = "0 / 0";
		document.getElementById("accelCard").textContent = "0";
		updateBestSetup(null, snapshots || []);
		setEmpty("No live signals yet — waiting for webhook activity", 18);
		return;
	}

	const freshCount = snapshots.filter(x => x.is_actionable).length;
	const buyCount = snapshots.filter(x => x.decision === "BUY" && x.is_actionable).length;
	const readyCount = snapshots.filter(x => x.decision === "READY" && x.is_actionable).length;
	const cleanCount = snapshots.filter(x => visuallyClean(x)).length;
	document.getElementById("rowsCard").textContent = freshCount + "/" + snapshots.length;
	document.getElementById("buyers1mCard").textContent = "B:" + buyCount + " R:" + readyCount;
	document.getElementById("accelCard").textContent = cleanCount + "/" + snapshots.length;
	updateBestSetup(chooseBestSetup(snapshots), snapshots);

	const tbody = document.getElementById("tbody");
	tbody.innerHTML = "";

	for (const s of snapshots) {
		const ageMin = (s.age_seconds / 60).toFixed(1);
		const accel = Number(s.buyer_acceleration || 0).toFixed(2);
		const exec = Number(s.execution_penalty || 0).toFixed(2);
		const adv = Number(s.adversarial_score || 0).toFixed(2);
		const impactPct = Number(s.estimated_impact_pct || 0).toFixed(1);
		const conf = Math.round(s.confidence_score || 0);
		const rawBuyers = s.buyers_last1m || 0;
		const effBuyers = s.effective_buyers_1m || 0;
		const clusterPct = Math.round((s.funding_cluster_ratio || 0) * 100);
		const rowClusterStatus = s.clustering_row_status || "resolved";
		const rowClusterTimeouts = s.clustering_timeouts || 0;
		const rowClusterFallbacks = s.clustering_fallbacks || 0;
		const buySol1m = Number(s.buy_sol_last_1m || 0).toFixed(2);
		const sellSol1m = Number(s.sell_sol_last_1m || 0).toFixed(2);
		const state = s.signal_state || "expired";
		const dec = s.decision || "?";
		const mint = s.mint || "";

		// Decision badge
		const decClass = dec === "BUY" ? "buy" : dec === "READY" ? "ready" : dec === "WATCH" ? "watch" : "avoid";

		// Signal state badge
		const stateClass = state === "fresh" ? "fresh" : state === "stale" ? "stale" : "expired";

		// Confidence colour
		const confStyle = conf >= 70 ? "color:#7ef0b2" : conf >= 45 ? "color:#ffd76a" : "color:#f87171";

		// Accel colour
		const accelStyle = s.buyer_acceleration > 1 ? "color:#7ef0b2;font-weight:700" : "color:#8ea0c3";

		// Exec colour
		const execScore = s.execution_penalty || 0;
		const execStyle = execScore >= 0.5 ? "color:#7ef0b2" : execScore >= 0.3 ? "color:#ffd76a" : "color:#f87171";

		// Adv colour
		const advScore = s.adversarial_score || 0;
		const advStyle = advScore < 0.3 ? "color:#7ef0b2" : advScore < 0.6 ? "color:#ffd76a" : "color:#f87171";

		// Impact colour
		const impact = s.estimated_impact_pct || 0;
		const impactStyle = impact < 5 ? "color:#7ef0b2" : impact < 15 ? "color:#ffd76a" : "color:#f87171";

		// Raw/eff buyers — highlight when clustering reduces count
		const buyersStyle = effBuyers < rawBuyers ? "color:#ffd76a" : "";

		// 1m buy/sell — highlight reversal
		const flowStyle = (parseFloat(sellSol1m) >= parseFloat(buySol1m) && (parseFloat(buySol1m) > 0 || parseFloat(sellSol1m) > 0))
			? "color:#f87171" : "color:#8ea0c3";

		// Engine explainability
		const eng = s.engine || {};
		const l0Reject = eng.layer0_reject === true;
		const l0Reason = eng.layer0_reason || "";
		const maxLabel = eng.max_label || "";
		// gates is always [] or a real array — never null — from the server.
		const gates = Array.isArray(eng.gates) ? eng.gates : [];
		const gatePass = typeof eng.gates_pass_count === "number" ? eng.gates_pass_count : 0;
		const gateTotal = l0Reject ? 0 : (gates.length || 7);

		// Gate 1 = liq/mc ratio %, Gate 4 = vol/mc ratio %
		const g1 = gates.find(g => g.gate_id === 1) || {};
		const g4 = gates.find(g => g.gate_id === 4) || {};
		const verdict = s.operator_verdict;
		const fullWhyNot = [s.dominant_blocker, s.why_not_higher, (s.reasons || []).join(" | "), gateReasonList(eng)].filter(Boolean).join(" | ");
		const clusterTrust = clusteringSurfaceLabel(s);
		const clusterCompression = clusterPct > 0 ? clusterPct + "% compressed" : "0% compressed";
		const clusterFallbackBadge = rowClusterStatus !== "resolved"
			? " <span class='badge partial' title='row-level clustering fallback; timeouts=" + rowClusterTimeouts + ", fallbacks=" + rowClusterFallbacks + "'>" + rowClusterStatus.replace("_"," ") + "</span>"
			: " <span class='badge cleanflow'>resolved</span>";
		const qualityTier = visualTier(verdict, s);
		const qualityBadge = qualityTier === "clean"
			? "<span class='badge cleanflow'>clean</span>"
			: qualityTier === "compromised"
				? "<span class='badge partial'>compromised</span>"
				: "<span class='badge poison'>low conviction</span>";
		const liqMcPct = (g1.value != null && !g1.skipped) ? Number(g1.value).toFixed(1) + "%" : compactMissingStructure(s.market_cap_reason || g1.reason || "market cap unavailable");
		const volMcPct = (g4.value != null && !g4.skipped) ? Number(g4.value).toFixed(1) + "%" : compactMissingStructure(s.market_cap_reason || g4.reason || "market cap unavailable");

		const g1Style = g1.passed ? "color:#7ef0b2" : g1.skipped ? "color:#5a7090" : "color:#f87171";
		const g4Style = g4.passed ? "color:#7ef0b2" : g4.skipped ? "color:#5a7090" : "color:#f87171";

		// Gates cell rendering:
		//   Layer 0 reject  → "L0: <reason>" in red; gates not evaluated
		//   Normal          → "N/7" pass count + ceiling suffix
		let gatesCell;
		if (l0Reject) {
			const l0Tip = l0Reason ? escAttr(l0Reason) : "layer0 hard reject";
			gatesCell = "<span style='color:#f87171;font-size:10px;font-weight:700' title='" + l0Tip + "'>L0: " + esc(l0Reason || "reject") + "</span>";
		} else {
			const gateStyle = gatePass >= 6 ? "color:#7ef0b2" : gatePass >= 4 ? "color:#ffd76a" : "color:#f87171";
			const ceilSuffix = (maxLabel && maxLabel !== "BUY")
				? " <span class='dim' style='font-size:10px'>→" + maxLabel + "</span>" : "";
			gatesCell = "<span style='" + gateStyle + "'>" + gatePass + "/" + gateTotal + "</span>" + ceilSuffix;
		}

		// Gate 1 tooltip: gate reason
		const g1Title = g1.reason ? escAttr(g1.reason) : "Gate 1: liquidity/MC ratio";
		const g4Title = g4.reason ? escAttr(g4.reason) : "Gate 4: volume/MC ratio";
		const tokenHref = s.execution_url;

		const tr = document.createElement("tr");
		tr.setAttribute("data-actionable", s.is_actionable ? "1" : "0");
		if (!s.is_actionable) tr.style.opacity = "0.45";
		if (qualityTier === "clean") tr.className = "row-strong";
		else if (qualityTier === "compromised") tr.className = "row-watch";
		else tr.className = "row-risk";

		tr.innerHTML =
			"<td><span class='badge " + decClass + "'>" + dec + "</span></td>" +
			"<td><span class='badge verdict-label " + tierBadgeClass(qualityTier) + "' title='" + escAttr(verdict) + "'>" + esc(verdict) + "</span></td>" +
			"<td style='" + confStyle + ";font-weight:700'>" + conf + "</td>" +
			"<td><span class='badge " + stateClass + "'>" + state + "</span></td>" +
			"<td><div class='token-cell'><div class='token-meta'><div class='token-actions'><a class='token-link mono' href='" + tokenHref + "' target='_blank' rel='noopener noreferrer'>" + mint.slice(0, 8) + "…</a><a class='exec-link' href='" + tokenHref + "' target='_blank' rel='noopener noreferrer'>GMGN</a></div><span class='token-sub'>" + qualityBadge + "</span></div></div></td>" +
			"<td style='" + buyersStyle + "' title='raw/effective buyers after clustering'><div class='metric-stack'><span>" + rawBuyers + " raw / " + effBuyers + " eff</span><span class='metric-sub'>" + buyerQualityLabel(s) + "</span></div></td>" +
			"<td title='cluster row status: " + escAttr(rowClusterStatus) + "; compression=" + clusterCompression + "'><div class='metric-stack'><span>" + clusterTrust + clusterFallbackBadge + "</span><span class='metric-sub'>" + clusterCompression + "</span></div></td>" +
			"<td style='" + flowStyle + "'>" + buySol1m + "/" + sellSol1m + "</td>" +
			"<td style='" + accelStyle + "'>" + accel + "</td>" +
			"<td style='" + execStyle + "'>" + exec + "</td>" +
			"<td style='" + advStyle + "'>" + adv + "</td>" +
			"<td style='" + impactStyle + "'>" + impactPct + "%</td>" +
			"<td class='dim'>" + ageMin + "m</td>" +
			"<td>" + gatesCell + "</td>" +
			"<td style='" + g1Style + "' title='" + g1Title + "'>" + liqMcPct + "</td>" +
			"<td style='" + g4Style + "' title='" + g4Title + "'>" + volMcPct + "</td>" +
			"<td class=\"why-now-cell\">" + esc(s.why_now) + "</td>" +
			"<td class=\"blocker-cell\">" + esc(s.dominant_blocker || s.why_not_higher) + "</td>";
		tbody.appendChild(tr);
	}
}

function updateStatusStrip(ok, pollTime, snapshots, healthData) {
	const healthEl = document.getElementById("ingestorHealth");
	const clusterEl = document.getElementById("clusterStatus");
	const pollEl = document.getElementById("lastPoll");
	const modeEl = document.getElementById("storeMode");
	const freshEl = document.getElementById("freshCount");
	const banner = document.getElementById("clusterBanner");

	if (!ok) {
		healthEl.innerHTML = "<span class='status-dot dot-err'></span>Ingestor unreachable";
		return;
	}
	healthEl.innerHTML = "<span class='status-dot dot-ok'></span>Ingestor OK";

	// Clustering status from /healthz.
	// Always resolve to a definitive show/hide — never leave banner in stale state.
	{
		const c = (healthData && healthData.clustering) ? healthData.clustering : null;
		if (c) {
			const cOK = c.healthy === true;
			const backend = c.backend || "null";
			const buyAllowed = c.buy_ready_allowed === true;
			const timeoutFallbacks = c.timeout_fallbacks || 0;
			const errorFallbacks = c.error_fallbacks || 0;
			clusterEl.innerHTML = cOK
				? "<span class='status-dot dot-ok'></span>cluster: " + backend + " t/o:" + timeoutFallbacks + " err:" + errorFallbacks
				: "<span class='status-dot dot-err'></span>cluster: " + backend + " (degraded)";
			// Show banner only when we have a definitive degraded signal.
			banner.style.display = buyAllowed ? "none" : "block";
		} else {
			// Health endpoint unreachable — treat as unknown, hide banner
			// (we already reported ingestor as OK above; don't contradict that).
			clusterEl.innerHTML = "<span class='status-dot dot-warn'></span>cluster: unknown";
			banner.style.display = "none";
		}
	}

	if (pollTime) {
		pollEl.textContent = "Last poll: " + pollTime.toLocaleTimeString();
	}
	if (snapshots) {
		const fresh = (snapshots || []).filter(x => x.is_actionable).length;
		const total = (snapshots || []).length;
		const warming = fresh === 0;
		modeEl.innerHTML = warming
			? "<span style='color:#ffd76a'>Store: warming up</span>"
			: "<span style='color:#7ef0b2'>Store: " + total + " tokens tracked</span>";
		freshEl.textContent = fresh + " fresh actionable";
	}
}

function startLivePolling() {
	if (liveTimer) clearInterval(liveTimer);
	liveTimer = setInterval(loadLive, CFG.refresh_interval * 1000);
}

// ---- helpers ----
function td_badge(val, cls) {
	return val
		? "<td><span class='badge " + cls + "'>yes</span></td>"
		: "<td><span class='badge no'>no</span></td>";
}

function setEmpty(msg, cols) {
	const tbody = document.getElementById("tbody");
	tbody.innerHTML = "<tr><td colspan='" + cols +
		"' style='text-align:center;color:#8ea0c3;padding:32px'>" + msg + "</td></tr>";
}

function setError(msg, cols) {
	const tbody = document.getElementById("tbody");
	tbody.innerHTML = "<tr><td colspan='" + cols +
		"' style='text-align:center;color:#f87171;padding:32px'>" + msg + "</td></tr>";
}

function esc(s) {
	return String(s).replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;");
}
function escAttr(s) {
	return String(s).replace(/&/g,"&amp;").replace(/'/g,"&#39;").replace(/"/g,"&quot;");
}

function gateReasonList(eng) {
	const gates = Array.isArray((eng || {}).gates) ? eng.gates : [];
	return gates.filter(g => g && g.passed === false && !g.skipped && g.reason).map(g => g.reason).join(" | ");
}

function compactMissingStructure(reason) {
	const text = String(reason || "").toLowerCase();
	if (text.includes("holder balance")) return "no holder proxy";
	if (text.includes("market cap")) return "no market cap";
	if (text.includes("token not yet")) return "too young";
	if (text.includes("not yet observed")) return "incomplete";
	return "n/a";
}

function clusteringSurfaceLabel(s) {
	const status = s.clustering_row_status || "resolved";
	if (status === "resolved") return "clean";
	if (status === "partial_fallback") return "partial fallback";
	return "full fallback";
}

function buyerQualityLabel(s) {
	const raw = s.buyers_last1m || 0;
	const eff = s.effective_buyers_1m || 0;
	if (raw === 0 && eff === 0) return "no real buy pressure";
	if (raw > 0 && eff === 0) return "fully compressed";
	if (eff < raw) return "compressed after clustering";
	if ((s.funding_cluster_ratio || 0) === 0) return "organic buyer set";
	return "mixed buyer quality";
}

function visualTier(verdict, s) {
	const v = String(verdict || "").toLowerCase();
	if (v.includes("clean-ish") || v.includes("best of bad tape")) return "compromised";
	if ((s.decision === "BUY" || s.decision === "READY") && (s.clustering_row_status || "resolved") === "resolved") return "clean";
	return "weak";
}

function visuallyClean(s) {
	return visualTier(s.operator_verdict, s) !== "weak";
}

function chooseBestSetup(rows) {
	if (!rows || rows.length === 0) return null;
	const scored = [...rows].sort((a, b) => bestSetupScore(b) - bestSetupScore(a));
	return scored[0] || null;
}

function bestSetupScore(s) {
	let score = Number(s.confidence_score || 0);
	if (s.is_actionable) score += 18;
	if (s.decision === "BUY") score += 18;
	if (s.decision === "READY") score += 12;
	if (s.decision === "WATCH") score += 8;
	if ((s.clustering_row_status || "resolved") === "resolved") score += 10;
	if ((s.funding_cluster_ratio || 0) === 0) score += 6;
	if ((s.estimated_impact_pct || 999) < 5) score += 8;
	if ((s.execution_penalty || 0) >= 0.5) score += 8;
	if ((s.market_cap_sol || 0) > 0) score += 4;
	if ((s.adversarial_score || 1) > 0.75) score -= 10;
	if ((s.clustering_row_status || "resolved") === "full_fallback") score -= 12;
	return score;
}

function updateBestSetup(best, rows) {
	const headline = document.getElementById("bestSetupHeadline");
	const verdictEl = document.getElementById("bestSetupVerdict");
	const blockerEl = document.getElementById("bestSetupBlocker");
	const evidenceEl = document.getElementById("bestSetupEvidence");
	const meta = document.getElementById("bestSetupMeta");
	const marketCopy = document.getElementById("marketReadCopy");
	const marketMeta = document.getElementById("marketReadMeta");
	meta.innerHTML = "";
	marketMeta.innerHTML = "";

	const total = rows.length;
	const clean = rows.filter(x => visuallyClean(x)).length;
	const partial = rows.filter(x => (x.clustering_row_status || "resolved") === "partial_fallback").length;
	const full = rows.filter(x => (x.clustering_row_status || "resolved") === "full_fallback").length;
	const missingMC = rows.filter(x => !(x.market_cap_sol > 0)).length;
	marketMeta.innerHTML =
		"<span class='badge neutral'>" + total + " rows tracked</span>" +
		"<span class='badge cleanflow'>" + clean + " structurally cleaner</span>" +
		"<span class='badge partial'>" + (partial + full) + " fallback-affected</span>" +
		"<span class='badge poison'>" + missingMC + " incomplete MC</span>";
	if (total === 0) {
		marketCopy.textContent = "No flow yet. The terminal stays quiet until there is enough structure to judge.";
	} else if (clean === 0) {
		marketCopy.textContent = "Current tape is active but not trustworthy yet. Most names are structurally weak, fallback-affected, or missing enough data to size with confidence.";
	} else if ((partial + full) > clean) {
		marketCopy.textContent = "There is some cleaner flow, but fallback and compression still dominate the screen. Treat most motion as suspicious before it proves otherwise.";
	} else {
		marketCopy.textContent = "A few names are structurally cleaner than the rest, but the terminal is still biased toward disqualifying weak flow over manufacturing excitement.";
	}

	if (!best) {
		headline.textContent = "No high-conviction setup right now";
		verdictEl.textContent = "";
		blockerEl.textContent = "";
		evidenceEl.textContent = "Waiting for enough live structure to judge.";
		return;
	}

	const tier = visualTier(best.operator_verdict, best);
	const whyNow = best.why_now;
	const whyNot = best.dominant_blocker || best.why_not_higher;
	const verdict = best.operator_verdict;
	const shortMint = (best.mint || "").slice(0, 8) + "…";

	if (tier === "clean" && best.is_actionable) {
		headline.textContent = shortMint + " is the cleanest live setup";
	} else if (best.decision === "WATCH" || best.decision === "READY" || best.decision === "BUY") {
		headline.textContent = shortMint + " is the best available, not a free pass";
	} else {
		headline.textContent = "Best available, but still low-conviction";
	}
	verdictEl.textContent = verdict;
	blockerEl.textContent = "Blocker: " + whyNot;
	evidenceEl.textContent = whyNow;

	meta.innerHTML =
		"<span class='badge " + decisionBadgeClass(best.decision) + "'>" + (best.decision || "?") + "</span>" +
		"<span class='badge verdict-label " + tierBadgeClass(tier) + "'>" + esc(verdict) + "</span>" +
		"<span class='badge neutral'>conf " + Math.round(best.confidence_score || 0) + "</span>" +
		"<span class='badge neutral'>" + clusteringSurfaceLabel(best) + "</span>";
}

function decisionBadgeClass(decision) {
	if (decision === "BUY") return "buy";
	if (decision === "READY") return "ready";
	if (decision === "WATCH") return "watch";
	return "avoid";
}

function tierBadgeClass(tier) {
	if (tier === "clean") return "cleanflow";
	if (tier === "compromised") return "partial";
	return "poison";
}

function tierLabel(tier) {
	if (tier === "clean") return "structurally clean";
	if (tier === "compromised") return "partially compromised";
	return "low confidence";
}

init();
</script>
</body>
</html>`
