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
	var rows []map[string]any
	if a.cfg.liveMode {
		rows = a.getCachedLiveRows(10 * time.Minute)
	}
	if a.cfg.liveMode && len(rows) == 0 {
		var err error
		rows, err = a.loadLiveRows(0, 240, 20, false)
		if err != nil {
			log.Printf("renderIndexHTML: live bootstrap unavailable: %v", err)
			rows = nil
		} else {
			a.setCachedLiveRows(rows)
		}
	}

	page := indexHTML
	best := chooseBestSetupGo(rows)
	posture := pagePosture(rows, best)
	rejects := rejectCount(rows)

	pagePostureText := "NO TRADE"
	verdictBar := "NO CANDIDATES WORTH MONITORING."
	heroPrimaryText := "VIEW [GMGN]"
	heroPrimaryClass := "btn-disabled"
	heroPrimaryHref := "#"
	heroSecondaryHref := "#"
	heroName := "NO LIVE CANDIDATE"
	heroMeta := "n/a"
	heroTrigger := "no valid execution edge"
	heroNoTradeReason := "no valid execution edge"
	heroState := "AVOID"
	heroQuality := "DEAD"

	if best != nil {
		heroName = tokenLabel(best)
		heroMeta = shortAddress(stringFieldMap(best, "mint"))
		heroTrigger = firstNonEmpty(stringFieldMap(best, "trigger_line"), "no valid execution edge")
		heroState = scanStateText(best)
		heroQuality = qualityTier(best)
		heroPrimaryHref = firstNonEmpty(stringFieldMap(best, "execution_url"), "#")
		heroSecondaryHref = heroPrimaryHref
	}

	switch posture {
	case "pristine":
		pagePostureText = "SYSTEM: NOMINAL"
		verdictBar = "HIGH CONVICTION SETUP DETECTED. EXECUTION ENABLED."
		heroPrimaryText = "EXECUTE [GMGN]"
		heroPrimaryClass = "btn-exec"
		heroNoTradeReason = ""
	case "degraded":
		pagePostureText = "POSTURE: DEFENSIVE"
		verdictBar = "BAD TAPE DETECTED. NO TRADE ZONE."
		heroPrimaryText = "VIEW [GMGN]"
		heroPrimaryClass = "btn-view"
		if best != nil {
			heroNoTradeReason = noTradeReason(best)
		}
	}

	replacements := map[string]string{
		"__PAGE_POSTURE_CLASS__":   posture,
		"__PAGE_POSTURE_TEXT__":    html.EscapeString(pagePostureText),
		"__PAGE_SYSTEM_META__":     html.EscapeString(fmt.Sprintf("%d rows • %d rejects", len(rows), rejects)),
		"__PAGE_VERDICT_BAR__":     html.EscapeString(verdictBar),
		"__HERO_NAME__":            html.EscapeString(heroName),
		"__HERO_META__":            html.EscapeString(heroMeta),
		"__HERO_STATE_CLASS__":     stateBadgeClass(heroState),
		"__HERO_STATE__":           html.EscapeString(heroState),
		"__HERO_QUALITY_CLASS__":   qualityBadgeClass(heroQuality),
		"__HERO_QUALITY__":         html.EscapeString(heroQuality),
		"__HERO_TRIGGER_LINE__":    html.EscapeString(heroTrigger),
		"__HERO_SUPERIORITY__":     html.EscapeString(superiorityLine(heroQuality)),
		"__HERO_NO_TRADE_REASON__": html.EscapeString(heroNoTradeReason),
		"__HERO_PRIMARY_HREF__":    html.EscapeString(heroPrimaryHref),
		"__HERO_PRIMARY_CLASS__":   heroPrimaryClass,
		"__HERO_PRIMARY_TEXT__":    html.EscapeString(heroPrimaryText),
		"__HERO_SECONDARY_HREF__":  html.EscapeString(heroSecondaryHref),
		"__PRIMARY_SCAN_ROWS__":    renderScanRows(rows, best, posture, false),
		"__REJECT_SCAN_ROWS__":     renderScanRows(rows, best, posture, true),
	}
	for placeholder, value := range replacements {
		page = strings.ReplaceAll(page, placeholder, value)
	}
	return page
}

func renderInitialRows(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	return renderScanRows(rows, best, pagePosture(rows, best), false)
}

func pagePosture(rows []map[string]any, best map[string]any) string {
	if len(rows) == 0 || best == nil {
		return "no-trade"
	}
	if stringFieldMap(best, "actionability_label") == "actionable now" &&
		stringFieldMap(best, "trust_label") != "insider-controlled" &&
		stringFieldMap(best, "clustering_row_status") == "resolved" &&
		!engineLayer0Reject(best) {
		return "pristine"
	}
	return "degraded"
}

func rejectCount(rows []map[string]any) int {
	total := 0
	for _, row := range rows {
		if isRejectRow(row) {
			total++
		}
	}
	return total
}

func renderScanRows(rows []map[string]any, best map[string]any, posture string, rejects bool) string {
	var b strings.Builder
	for _, s := range rows {
		if isRejectRow(s) != rejects {
			continue
		}
		mint := stringFieldMap(s, "mint")
		state := scanStateText(s)
		rowClass := scanRowClass(s, best, state, rejects)
		blocker := blockerText(s, state)
		actionText := "VIEW [GMGN]"
		actionClass := "view"
		if posture == "pristine" && state == "READY" {
			actionText = "EXECUTE [GMGN]"
			actionClass = ""
		}
		executionURL := firstNonEmpty(stringFieldMap(s, "execution_url"), "#")
		if executionURL == "#" {
			actionClass = "muted"
		}
		fmt.Fprintf(&b,
			"<tr class=\"scan-row %s\"><td><span class=\"badge %s\">%s</span></td><td>%d</td><td><div class=\"token-main\">%s</div><div class=\"token-sub\">%s</div></td><td><div class=\"blocker %s\">%s</div></td><td><div class=\"trigger\">%s</div></td><td><a class=\"action-link %s\" href=\"%s\" target=\"_blank\" rel=\"noopener noreferrer\">%s</a></td></tr>",
			html.EscapeString(rowClass),
			stateBadgeClass(state), html.EscapeString(state),
			int(floatFieldMap(s, "confidence_score")+0.5),
			html.EscapeString(tokenLabel(s)),
			html.EscapeString(tokenSubLabel(mint)),
			blockerClass(s, state, blocker), html.EscapeString(blocker),
			html.EscapeString(firstNonEmpty(stringFieldMap(s, "trigger_line"), "no valid execution edge")),
			actionClass, html.EscapeString(executionURL), html.EscapeString(actionText),
		)
	}
	return b.String()
}

func isRejectRow(row map[string]any) bool {
	return stringFieldMap(row, "priority_label") == "deprioritize"
}

func engineLayer0Reject(row map[string]any) bool {
	eng, ok := row["engine"].(map[string]any)
	if !ok {
		return false
	}
	return boolFieldMap(eng, "layer0_reject")
}

func tokenLabel(row map[string]any) string {
	mint := stringFieldMap(row, "mint")
	label := firstNonEmpty(
		stringFieldMap(row, "token_symbol"),
		stringFieldMap(row, "symbol"),
		stringFieldMap(row, "token_name"),
		stringFieldMap(row, "name"),
	)
	if label == "" || label == mint {
		return shortAddress(mint)
	}
	return label
}

func tokenSubLabel(mint string) string {
	if strings.TrimSpace(mint) == "" {
		return "CA n/a"
	}
	return "CA " + shortAddress(mint)
}

func shortAddress(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:6] + "..." + value[len(value)-4:]
}

func scanStateText(row map[string]any) string {
	actionability := strings.ToLower(strings.TrimSpace(stringFieldMap(row, "actionability_label")))
	decision := stringFieldMap(row, "decision")
	if actionability == "actionable now" || decision == "BUY" || decision == "READY" {
		return "READY"
	}
	if strings.Contains(actionability, "observe") ||
		strings.Contains(actionability, "monitor") ||
		decision == "WATCH" ||
		stringFieldMap(row, "priority_label") == "monitor_for_upgrade" {
		return "WATCH"
	}
	return "AVOID"
}

func stateBadgeClass(state string) string {
	switch state {
	case "READY":
		return "ready"
	case "WATCH":
		return "watch"
	default:
		return "avoid"
	}
}

func qualityTier(row map[string]any) string {
	return firstNonEmpty(stringFieldMap(row, "quality_tier"), "DEAD")
}

func qualityBadgeClass(tier string) string {
	switch tier {
	case "APEX":
		return "apex"
	case "NEAR":
		return "near"
	case "TRAP":
		return "trap"
	default:
		return "dead"
	}
}

func scanRowClass(row map[string]any, best map[string]any, state string, reject bool) string {
	if reject {
		return "reject"
	}
	if best != nil && stringFieldMap(row, "mint") == stringFieldMap(best, "mint") {
		return "best"
	}
	if state == "WATCH" || stringFieldMap(row, "priority_label") == "monitor_for_upgrade" {
		return "watch"
	}
	return ""
}

func blockerText(row map[string]any, state string) string {
	if state == "READY" &&
		stringFieldMap(row, "trust_label") != "insider-controlled" &&
		stringFieldMap(row, "clustering_row_status") == "resolved" &&
		!engineLayer0Reject(row) {
		return "none"
	}
	return noTradeReason(row)
}

func noTradeReason(row map[string]any) string {
	return firstNonEmpty(
		stringFieldMap(row, "no_trade_reason"),
		stringFieldMap(row, "dominant_blocker"),
		stringFieldMap(row, "why_not_higher"),
		"no valid execution edge",
	)
}

func blockerClass(row map[string]any, state string, blocker string) string {
	if strings.TrimSpace(blocker) == "" || blocker == "none" {
		return ""
	}
	text := strings.ToLower(blocker + " " + stringFieldMap(row, "trust_label"))
	if strings.Contains(text, "liquidity") ||
		strings.Contains(text, "impact") ||
		strings.Contains(text, "fallback") ||
		strings.Contains(text, "concentration") ||
		strings.Contains(text, "insider") ||
		strings.Contains(text, "execution") ||
		strings.Contains(text, "reject") ||
		strings.Contains(text, "compromised") {
		return "red"
	}
	if state == "WATCH" {
		return "amber"
	}
	return ""
}

func superiorityLine(tier string) string {
	switch tier {
	case "APEX":
		return "Apex candidate: real demand meeting executable structure."
	case "NEAR":
		return "Best available but still short of valid execution."
	case "TRAP":
		return "Likely distribution trap. Do not chase."
	default:
		return "No material edge versus the rest of the tape."
	}
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

func bestPriorityText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return html.EscapeString(stringFieldMap(best, "priority_label"))
}

func bestVerdictText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return html.EscapeString(firstNonEmpty(stringFieldMap(best, "operator_verdict"), ""))
}

func bestVerdictLineText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "verdict: " + html.EscapeString(firstNonEmpty(stringFieldMap(best, "operator_verdict"), ""))
}

func bestBlockerText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "Blocker: " + html.EscapeString(firstNonEmpty(stringFieldMap(best, "dominant_blocker"), stringFieldMap(best, "why_not_higher"), ""))
}

func bestBlockerLineText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "blocker: " + html.EscapeString(firstNonEmpty(stringFieldMap(best, "dominant_blocker"), stringFieldMap(best, "why_not_higher"), ""))
}

func bestWhyNowText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "why now: " + html.EscapeString(stringFieldMap(best, "why_now"))
}

func bestActionabilityText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return html.EscapeString(stringFieldMap(best, "actionability_label"))
}

func bestAnalogueText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "analogue: " + html.EscapeString(stringFieldMap(best, "historical_analogue_summary"))
}

func bestOutcomeText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "outcome: " + html.EscapeString(stringFieldMap(best, "historical_outcome_band"))
}

func bestTimingText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "timing: " + html.EscapeString(stringFieldMap(best, "historical_time_to_outcome"))
}

func bestUpgradeText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "upgrade if: " + html.EscapeString(joinListFieldMap(best, "upgrade_triggers"))
}

func bestInvalidateText(rows []map[string]any) string {
	best := chooseBestSetupGo(rows)
	if best == nil {
		return ""
	}
	return "invalidate if: " + html.EscapeString(joinListFieldMap(best, "invalidate_triggers"))
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
	for _, row := range rows {
		priority := stringFieldMap(row, "priority_label")
		if priority == "best_on_tape" || priority == "priority: best_on_tape" {
			return row
		}
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

func rowClassForPriorityGo(priority string) string {
	switch priority {
	case "best_on_tape", "priority: best_on_tape":
		return "row-best"
	case "monitor_for_upgrade", "priority: monitor_for_upgrade":
		return "row-upgrade"
	default:
		return "row-risk"
	}
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

func joinListFieldMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, " • ")
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, " • ")
	default:
		return ""
	}
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
<title>Structural Quality Filter</title>
<style>
:root{
  --bg:#050505;
  --panel:#0a0a0a;
  --panel-2:#0d0d0d;
  --border:#1a1a1a;
  --text:#ececec;
  --muted:#727272;
  --green:#00ff66;
  --green-bg:#07170d;
  --amber:#ffcc00;
  --amber-bg:#1a1500;
  --red:#ff4d4d;
  --red-bg:#1a0707;
  --blue:#66b3ff;
  --blue-bg:#08121c;
  --mono:"JetBrains Mono","SFMono-Regular","Menlo","Consolas",monospace;
}
html,body{
  background:var(--bg);
  color:var(--text);
  margin:0;
  padding:0;
  font-family:var(--mono);
  font-size:11px;
}
.operator-shell{
  width:100%;
  min-height:100vh;
  background:var(--bg);
}
.topbar{
  display:flex;
  align-items:center;
  justify-content:space-between;
  gap:12px;
  padding:10px 16px;
  border-bottom:1px solid var(--border);
  background:#070707;
}
.topbar-left{
  display:flex;
  align-items:center;
  gap:10px;
  flex-wrap:wrap;
}
.eyebrow{
  color:var(--muted);
  font-size:10px;
  text-transform:uppercase;
  letter-spacing:.08em;
}
.health-pill{
  display:inline-flex;
  align-items:center;
  gap:6px;
  padding:3px 8px;
  font-size:9px;
  font-weight:800;
  border-radius:3px;
  border:1px solid var(--border);
  text-transform:uppercase;
}
.health-pill.pristine{
  background:var(--green-bg);
  color:var(--green);
  border-color:rgba(0,255,102,.35);
}
.health-pill.degraded{
  background:var(--amber-bg);
  color:var(--amber);
  border-color:rgba(255,204,0,.35);
}
.health-pill.no-trade{
  background:var(--red-bg);
  color:var(--red);
  border-color:rgba(255,77,77,.35);
}
.topbar-meta{
  color:var(--muted);
  font-size:10px;
}
.verdict-bar{
  padding:12px 16px;
  font-size:14px;
  font-weight:900;
  letter-spacing:-.02em;
  border-bottom:1px solid var(--border);
}
.verdict-bar.pristine{
  background:#07170d;
  color:var(--green);
  border-bottom:2px solid var(--green);
}
.verdict-bar.degraded{
  background:#1a1500;
  color:var(--amber);
  border-bottom:2px solid var(--amber);
}
.verdict-bar.no-trade{
  background:#1a0707;
  color:var(--red);
  border-bottom:2px solid var(--red);
}
.hero{
  display:grid;
  grid-template-columns:240px 1fr 220px;
  gap:24px;
  align-items:center;
  padding:18px 16px;
  border-bottom:1px solid var(--border);
  background:linear-gradient(180deg,#0a0a0a,#080808);
}
.hero.degraded{
  background:linear-gradient(180deg,#141000,#0b0900);
}
.hero.no-trade{
  background:linear-gradient(180deg,#120707,#090505);
}
.hero-left{
  min-width:0;
}
.hero-name{
  margin:0;
  font-size:24px;
  line-height:1;
  font-weight:900;
  color:#fff;
}
.hero-meta{
  margin-top:6px;
  color:var(--muted);
  font-size:10px;
}
.badge-row{
  display:flex;
  flex-wrap:wrap;
  gap:8px;
  margin-top:10px;
}
.badge{
  display:inline-flex;
  align-items:center;
  padding:3px 7px;
  border-radius:3px;
  font-size:9px;
  font-weight:800;
  border:1px solid var(--border);
  text-transform:uppercase;
}
.badge.ready{
  color:var(--green);
  background:var(--green-bg);
  border-color:rgba(0,255,102,.35);
}
.badge.watch{
  color:var(--amber);
  background:var(--amber-bg);
  border-color:rgba(255,204,0,.35);
}
.badge.avoid{
  color:var(--red);
  background:var(--red-bg);
  border-color:rgba(255,77,77,.35);
}
.badge.apex{
  color:var(--green);
  background:var(--green-bg);
}
.badge.near{
  color:var(--amber);
  background:var(--amber-bg);
}
.badge.trap{
  color:var(--red);
  background:var(--red-bg);
}
.badge.dead{
  color:#a0a0a0;
  background:#111;
}
.hero-middle{
  min-width:0;
  border-left:2px solid var(--green);
  padding-left:16px;
}
.hero.degraded .hero-middle{
  border-left-color:var(--amber);
}
.hero.no-trade .hero-middle{
  border-left-color:var(--red);
}
.hero-trigger{
  font-size:14px;
  font-weight:700;
  color:#fff;
  line-height:1.35;
}
.hero-sub{
  margin-top:7px;
  font-size:10px;
  color:var(--muted);
}
.hero-superiority{
  margin-top:7px;
  font-size:10px;
  color:var(--green);
  font-style:italic;
}
.hero.degraded .hero-superiority{
  color:var(--amber);
}
.hero.no-trade .hero-superiority{
  color:var(--red);
}
.hero-reason{
  margin-top:8px;
  font-size:10px;
  color:var(--red);
  font-weight:700;
}
.hero-right{
  display:flex;
  flex-direction:column;
  gap:8px;
}
.btn{
  display:block;
  width:100%;
  box-sizing:border-box;
  text-align:center;
  padding:12px 10px;
  border-radius:3px;
  text-decoration:none;
  font-weight:900;
  font-size:12px;
  border:1px solid transparent;
  cursor:pointer;
}
.btn-exec{
  background:var(--green);
  color:#000;
}
.btn-view{
  background:var(--amber);
  color:#000;
}
.btn-disabled{
  background:#111;
  color:#555;
  border-color:var(--border);
  pointer-events:none;
}
.btn-sec{
  background:transparent;
  color:var(--muted);
  border-color:var(--border);
  font-size:10px;
  padding:7px 8px;
}
.table-wrap{
  padding:0 0 16px 0;
}
.scan-table{
  width:100%;
  border-collapse:collapse;
}
.scan-table thead th{
  text-align:left;
  padding:10px 12px;
  color:var(--muted);
  font-size:9px;
  text-transform:uppercase;
  letter-spacing:.08em;
  border-bottom:1px solid var(--border);
  background:#070707;
  position:sticky;
  top:0;
}
.scan-table tbody td{
  padding:12px;
  border-bottom:1px solid var(--border);
  vertical-align:middle;
}
.scan-row:hover{
  background:#0b0b0b;
}
.scan-row.best{
  background:rgba(0,255,102,.04);
  box-shadow:inset 3px 0 0 var(--green);
}
.scan-row.watch{
  background:rgba(255,204,0,.04);
  box-shadow:inset 3px 0 0 var(--amber);
}
.scan-row.reject{
  opacity:.72;
}
.token-main{
  font-size:12px;
  font-weight:800;
  color:#fff;
}
.token-sub{
  margin-top:4px;
  font-size:10px;
  color:var(--muted);
}
.blocker{
  font-size:11px;
  font-weight:800;
  color:#fff;
}
.blocker.red{
  color:var(--red);
}
.blocker.amber{
  color:var(--amber);
}
.trigger{
  font-size:11px;
  color:#d7d7d7;
  white-space:nowrap;
}
.action-link{
  color:var(--green);
  text-decoration:none;
  font-weight:800;
  font-size:11px;
}
.action-link.view{
  color:var(--amber);
}
.action-link.muted{
  color:var(--muted);
}
.secondary-panel{
  margin-top:8px;
  border-top:1px solid var(--border);
}
.secondary-panel details{
  border-top:1px solid var(--border);
}
.secondary-panel summary{
  list-style:none;
  cursor:pointer;
  padding:12px 16px;
  color:var(--muted);
  font-size:10px;
  text-transform:uppercase;
  letter-spacing:.08em;
  background:#070707;
}
.secondary-panel summary::-webkit-details-marker{
  display:none;
}
@media (max-width: 980px){
  .hero{
    grid-template-columns:1fr;
  }
  .hero-right{
    width:100%;
  }
  .trigger{
    white-space:normal;
  }
}
</style>
</head>
<body>
<div class="operator-shell">
  <div class="topbar">
    <div class="topbar-left">
      <div class="eyebrow">Structural Quality Filter</div>
      <div id="pagePostureHeader" class="health-pill __PAGE_POSTURE_CLASS__">__PAGE_POSTURE_TEXT__</div>
      <div class="topbar-meta" id="pageSystemMeta">__PAGE_SYSTEM_META__</div>
    </div>
  </div>

  <div id="pageVerdictBar" class="verdict-bar __PAGE_POSTURE_CLASS__">__PAGE_VERDICT_BAR__</div>

  <section id="heroCard" class="hero __PAGE_POSTURE_CLASS__">
    <div class="hero-left">
      <h1 id="heroName" class="hero-name">__HERO_NAME__</h1>
      <div id="heroMeta" class="hero-meta">CA: __HERO_META__</div>
      <div class="badge-row">
        <div id="heroStateBadge" class="badge __HERO_STATE_CLASS__">__HERO_STATE__</div>
        <div id="heroQualityBadge" class="badge __HERO_QUALITY_CLASS__">__HERO_QUALITY__</div>
      </div>
    </div>

    <div class="hero-middle">
      <div id="heroTriggerLine" class="hero-trigger">__HERO_TRIGGER_LINE__</div>
      <div id="heroSub" class="hero-sub">trigger = flow • cluster/trust • execution</div>
      <div id="heroSuperiority" class="hero-superiority">__HERO_SUPERIORITY__</div>
      <div id="heroNoTradeReason" class="hero-reason">__HERO_NO_TRADE_REASON__</div>
    </div>

    <div class="hero-right">
      <a id="heroPrimaryAction" href="__HERO_PRIMARY_HREF__" class="btn __HERO_PRIMARY_CLASS__" target="_blank" rel="noopener noreferrer">__HERO_PRIMARY_TEXT__</a>
      <a id="heroSecondaryAction" href="__HERO_SECONDARY_HREF__" class="btn btn-sec" target="_blank" rel="noopener noreferrer">DEXSCREENER ↗</a>
    </div>
  </section>

  <div class="table-wrap">
    <table class="scan-table">
      <thead>
        <tr>
          <th>state</th>
          <th>conf</th>
          <th>token</th>
          <th>blocker</th>
          <th>trigger</th>
          <th>action</th>
        </tr>
      </thead>
      <tbody id="primaryScanBody">
        __PRIMARY_SCAN_ROWS__
      </tbody>
    </table>

    <div class="secondary-panel">
      <details id="rejectsPanel">
        <summary>Show rejects</summary>
        <table class="scan-table">
          <thead>
            <tr>
              <th>state</th>
              <th>conf</th>
              <th>token</th>
              <th>blocker</th>
              <th>trigger</th>
              <th>action</th>
            </tr>
          </thead>
          <tbody id="rejectScanBody">
            __REJECT_SCAN_ROWS__
          </tbody>
        </table>
      </details>
    </div>
  </div>
</div>
</body>
</html>`

const legacyIndexHTML = `<!doctype html>
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
.row-best{box-shadow:inset 5px 0 0 #7ef0b2;background:rgba(18,74,48,.30)}
.row-upgrade{box-shadow:inset 5px 0 0 #ffd76a;background:rgba(70,54,12,.22)}
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
		<div class="best-line actionability" id="bestSetupActionability">actionability: __INITIAL_BEST_ACTIONABILITY__</div>
		<div class="best-line priority" id="bestSetupPriority">priority: __INITIAL_BEST_PRIORITY__</div>
		<div class="best-line verdict-line" id="bestSetupVerdictLine">verdict: __INITIAL_BEST_VERDICT__</div>
		<div class="best-line blocker-line" id="bestSetupBlockerLine">blocker: __INITIAL_BEST_BLOCKER__</div>
		<div class="best-line trust-line" id="bestSetupTrust">trust: __INITIAL_BEST_TRUST__</div>
		<div class="best-line trust-reason-line" id="bestSetupTrustReason">trust reason: __INITIAL_BEST_TRUST_REASON__</div>
		<div class="best-line asymmetry-label" id="bestSetupAsymmetryLabel">asymmetry: __INITIAL_BEST_ASYMMETRY_LABEL__</div>
		<div class="best-line asymmetry-reason" id="bestSetupAsymmetryReason">asymmetry reason: __INITIAL_BEST_ASYMMETRY_REASON__</div>
		<div class="best-line focus-line" id="bestSetupFocus">focus: __INITIAL_BEST_FOCUS__</div>
		<div class="best-line relative-line" id="bestSetupRelative">relative setup: __INITIAL_BEST_RELATIVE__</div>
		<div class="best-line why-now-line" id="bestSetupWhyNow">why now: __INITIAL_BEST_WHY_NOW__</div>
		<div class="best-line analogue" id="bestSetupAnalogue">analogue: __INITIAL_BEST_ANALOGUE__</div>
		<div class="best-line outcome" id="bestSetupOutcome">outcome: __INITIAL_BEST_OUTCOME__</div>
		<div class="best-line timing" id="bestSetupTiming">timing: __INITIAL_BEST_TIMING__</div>
		<div class="best-line upgrade" id="bestSetupUpgrade">upgrade if: __INITIAL_BEST_UPGRADE__</div>
		<div class="best-line invalidate" id="bestSetupInvalidate">invalidate if: __INITIAL_BEST_INVALIDATE__</div>
		<div class="best-meta" id="bestSetupMeta">__INITIAL_BEST_META__</div>
		<div class="verdict" id="bestSetupVerdict">__INITIAL_BEST_VERDICT__</div>
		<div class="blocker" id="bestSetupBlocker">Blocker: __INITIAL_BEST_BLOCKER__</div>
		<div class="evidence" id="bestSetupEvidence">__INITIAL_BEST_EVIDENCE__</div>
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
				<th>priority</th>
				<th>actionability</th>
				<th>trust</th>
				<th>trust reason</th>
				<th>asymmetry</th>
				<th>asymmetry reason</th>
				<th>verdict</th>
				<th title="dominant disqualifier / missing structure">disqualifier</th>
				<th title="why this is worth a glance now">why now</th>
				<th>execution</th>
				<th title="raw buyers last 1m / effective after clustering">buyer quality</th>
				<th title="clustering trust, compression, and row-level fallback">clustering</th>
				<th title="Gate 1 — liquidity / market-cap ratio %; &lt;3% = avoid, 3-5% = watch floor, &gt;5% = eligible">liq/mc</th>
				<th title="Gate 4 — volume / market-cap ratio %; &lt;2% = low conviction, &gt;5% = healthy">vol/mc</th>
				<th>focus</th>
				<th>relative setup</th>
				<th>analogue</th>
				<th>outcome</th>
				<th>timing</th>
				<th>upgrade if</th>
				<th>invalidate if</th>
				<th>conf</th>
				<th>state</th>
				<th title="buy SOL / sell SOL in last 1m">buy/sell 1m</th>
				<th title="buyer acceleration ratio">accel</th>
				<th title="execution quality [0-1]">exec</th>
				<th title="adversarial score [0-1]">adv</th>
				<th title="estimated price impact %">impact%</th>
				<th>age</th>
				<th title="7-gate engine: pass count / 7; L0 = layer-0 hard reject; ceiling label shown when engine caps the decision">gates</th>
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
let lastGoodRowsHTML = "";
let lastGoodBestHTML = "";
let lastGoodDisplayByMint = Object.create(null);
const lastGoodDisplayFields = [
	"liqmc-cell",
	"volmc-cell",
	"why-now-cell",
	"blocker-cell",
	"actionability-cell",
	"priority-cell",
	"trust-cell",
	"trust-reason-cell",
	"asymmetry-label-cell",
	"asymmetry-reason-cell",
	"focus-cell",
	"relative-setup-cell",
];

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
	captureLastGoodFromDOM();
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
		keepLastGoodLive("Live refresh stale: " + e.message);
		return;
	}
	if (!Array.isArray(snapshots)) {
		keepLastGoodLive("Live refresh stale: invalid snapshot payload");
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
		keepLastGoodLive("Live refresh stale: empty row response");
		return;
	}

	const freshCount = snapshots.filter(x => x.is_actionable).length;
	const buyCount = snapshots.filter(x => x.decision === "BUY" && x.is_actionable).length;
	const readyCount = snapshots.filter(x => x.decision === "READY" && x.is_actionable).length;
	const cleanCount = snapshots.filter(x => visuallyClean(x)).length;
	document.getElementById("rowsCard").textContent = freshCount + "/" + snapshots.length;
	document.getElementById("buyers1mCard").textContent = "B:" + buyCount + " R:" + readyCount;
	document.getElementById("accelCard").textContent = cleanCount + "/" + snapshots.length;

	const tbody = document.getElementById("tbody");
	const renderedRows = [];

	try {
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
			const priority = stableDisplayValue(mint, "priority-cell", s.priority_label);
			const operatorFocus = stableDisplayValue(mint, "focus-cell", s.operator_focus);
			const relativeSetup = stableDisplayValue(mint, "relative-setup-cell", s.relative_setup_label);
			const trustLabel = s.trust_label;
			const trustReason = s.trust_reason;
			const trustText = stableDisplayValue(mint, "trust-cell", trustLabel ? "trust: " + trustLabel : "");
			const trustReasonText = stableDisplayValue(mint, "trust-reason-cell", trustReason);
			const asymmetryLabel = stableDisplayValue(mint, "asymmetry-label-cell", s.asymmetry_label);
			const asymmetryReason = stableDisplayValue(mint, "asymmetry-reason-cell", s.asymmetry_reason);
			const actionability = stableDisplayValue(mint, "actionability-cell", s.actionability_label);
			const analogue = s.historical_analogue_summary;
			const outcome = s.historical_outcome_band;
			const timing = s.historical_time_to_outcome;
		const upgrade = listText(s.upgrade_triggers);
		const invalidate = listText(s.invalidate_triggers);
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
			const liqMcPct = stableDisplayValue(mint, "liqmc-cell", (g1.value != null && !g1.skipped) ? Number(g1.value).toFixed(1) + "%" : compactMissingStructure(s.market_cap_reason || g1.reason || "market cap unavailable"));
			const volMcPct = stableDisplayValue(mint, "volmc-cell", (g4.value != null && !g4.skipped) ? Number(g4.value).toFixed(1) + "%" : compactMissingStructure(s.market_cap_reason || g4.reason || "market cap unavailable"));

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
		tr.className = rowClassForPriority(priority);

			tr.innerHTML =
				"<td><span class='badge " + decClass + "'>" + dec + "</span></td>" +
				"<td class=\"priority-cell\">" + esc(priority) + "</td>" +
				"<td class=\"actionability-cell\">" + esc(actionability) + "</td>" +
				"<td class=\"trust-cell\">" + esc(trustText) + "</td>" +
				"<td class=\"trust-reason-cell\">" + esc(trustReasonText) + "</td>" +
				"<td class=\"asymmetry-label-cell\">" + esc(asymmetryLabel) + "</td>" +
				"<td class=\"asymmetry-reason-cell\">" + esc(asymmetryReason) + "</td>" +
				"<td class=\"verdict-label\"><strong>" + esc(verdict) + "</strong></td>" +
				"<td class=\"blocker-cell\">" + esc(stableDisplayValue(mint, "blocker-cell", s.dominant_blocker || s.why_not_higher)) + "</td>" +
				"<td class=\"why-now-cell\">" + esc(stableDisplayValue(mint, "why-now-cell", s.why_now)) + "</td>" +
				"<td class=\"exec-cell\"><div class='token-cell'><div class='token-meta'><div class='token-actions'><a class='token-link mono' href='" + tokenHref + "' target='_blank' rel='noopener noreferrer'>" + mint.slice(0, 8) + "…</a><a class='gmgn-link exec-link' href='" + tokenHref + "' target='_blank' rel='noopener noreferrer'>EXECUTE [GMGN]</a></div><span class='token-sub'>" + qualityBadge + "</span></div></div></td>" +
				"<td style='" + buyersStyle + "' title='raw/effective buyers after clustering'><div class='metric-stack'><span>" + rawBuyers + " raw / " + effBuyers + " eff</span><span class='metric-sub'>" + buyerQualityLabel(s) + "</span></div></td>" +
				"<td title='cluster row status: " + escAttr(rowClusterStatus) + "; compression=" + clusterCompression + "'><div class='metric-stack'><span>" + clusterTrust + clusterFallbackBadge + "</span><span class='metric-sub'>" + clusterCompression + "</span></div></td>" +
				"<td class=\"liqmc-cell\" style='" + g1Style + "' title='" + g1Title + "'>" + liqMcPct + "</td>" +
				"<td class=\"volmc-cell\" style='" + g4Style + "' title='" + g4Title + "'>" + volMcPct + "</td>" +
				"<td class=\"focus-cell\">" + esc(operatorFocus) + "</td>" +
				"<td class=\"relative-setup-cell\">" + esc(relativeSetup) + "</td>" +
				"<td class=\"analogue-cell\">analogue: " + esc(analogue) + "</td>" +
				"<td class=\"outcome-cell\">outcome: " + esc(outcome) + "</td>" +
				"<td class=\"timing-cell\">timing: " + esc(timing) + "</td>" +
				"<td class=\"upgrade-cell\">upgrade if: " + esc(upgrade) + "</td>" +
				"<td class=\"invalidate-cell\">invalidate if: " + esc(invalidate) + "</td>" +
				"<td style='" + confStyle + ";font-weight:700'>" + conf + "</td>" +
				"<td><span class='badge " + stateClass + "'>" + state + "</span></td>" +
				"<td style='" + flowStyle + "'>" + buySol1m + "/" + sellSol1m + "</td>" +
				"<td style='" + accelStyle + "'>" + accel + "</td>" +
				"<td style='" + execStyle + "'>" + exec + "</td>" +
				"<td style='" + advStyle + "'>" + adv + "</td>" +
				"<td style='" + impactStyle + "'>" + impactPct + "%</td>" +
				"<td class='dim'>" + ageMin + "m</td>" +
				"<td>" + gatesCell + "</td>";
		renderedRows.push(tr.outerHTML);
	}
	} catch(e) {
		keepLastGoodLive("Live refresh stale: render failed: " + e.message);
		return;
	}
	if (renderedRows.length === 0) {
		keepLastGoodLive("Live refresh stale: no renderable rows");
		return;
	}
	tbody.innerHTML = renderedRows.join("");
	captureDisplayCacheFromRows(tbody.querySelectorAll("tr.live-row"));
	updateBestSetup(chooseBestSetup(snapshots), snapshots);
	lastGoodRowsHTML = tbody.innerHTML;
	const bestPanel = document.getElementById("best-setup-panel");
	if (bestPanel) lastGoodBestHTML = bestPanel.innerHTML;
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

function captureLastGoodFromDOM() {
	const tbody = document.getElementById("tbody");
	const bestPanel = document.getElementById("best-setup-panel");
	if (tbody) {
		captureDisplayCacheFromRows(tbody.querySelectorAll("tr.live-row"));
	}
	if (tbody && !lastGoodRowsHTML && tbody.querySelector(".live-row")) {
		lastGoodRowsHTML = tbody.innerHTML;
	}
	if (bestPanel && !lastGoodBestHTML && bestPanel.textContent.includes("Best Current Setup")) {
		lastGoodBestHTML = bestPanel.innerHTML;
	}
}

function captureDisplayCacheFromRows(rows) {
	for (const row of rows || []) {
		const mint = mintFromRenderedRow(row);
		if (!mint) continue;
		for (const field of lastGoodDisplayFields) {
			const cell = row.querySelector("." + field);
			if (cell) rememberDisplayField(mint, field, cell.textContent);
		}
	}
}

function mintFromRenderedRow(row) {
	const link = row && row.querySelector ? row.querySelector("a.exec-link[href*='gmgn.ai/sol/token/']") : null;
	if (!link) return "";
	const href = link.getAttribute("href") || "";
	const marker = "/token/";
	const idx = href.indexOf(marker);
	return idx >= 0 ? href.slice(idx + marker.length).trim() : "";
}

function rememberDisplayField(mint, field, value) {
	if (!mint || !field) return;
	const text = String(value == null ? "" : value).trim();
	if (!text) return;
	if (!lastGoodDisplayByMint[mint]) lastGoodDisplayByMint[mint] = Object.create(null);
	lastGoodDisplayByMint[mint][field] = text;
}

function stableDisplayValue(mint, field, value) {
	const text = String(value == null ? "" : value).trim();
	if (text) {
		rememberDisplayField(mint, field, text);
		return text;
	}
	const prior = mint && lastGoodDisplayByMint[mint] ? lastGoodDisplayByMint[mint][field] : "";
	return prior || "";
}

function keepLastGoodLive(msg) {
	const tbody = document.getElementById("tbody");
	const bestPanel = document.getElementById("best-setup-panel");
	if (tbody && lastGoodRowsHTML) {
		tbody.innerHTML = lastGoodRowsHTML;
	}
	if (bestPanel && lastGoodBestHTML) {
		bestPanel.innerHTML = lastGoodBestHTML;
	}
	const note = document.getElementById("tableNote");
	if (note) {
		note.textContent = msg + "; keeping last good snapshot on screen.";
	}
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

function listText(values) {
	if (typeof values === "string") return values;
	if (!Array.isArray(values)) return "";
	return values.filter(Boolean).join(" • ");
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

function rowClassForPriority(priority) {
	if (priority === "best_on_tape" || priority === "priority: best_on_tape") return "row-best";
	if (priority === "monitor_for_upgrade" || priority === "priority: monitor_for_upgrade") return "row-upgrade";
	return "row-risk";
}

function chooseBestSetup(rows) {
	if (!rows || rows.length === 0) return null;
	const backendBest = rows.find(x => x.priority_label === "best_on_tape" || x.priority_label === "priority: best_on_tape");
	if (backendBest) return backendBest;
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
	const actionabilityEl = document.getElementById("bestSetupActionability");
	const priorityEl = document.getElementById("bestSetupPriority");
	const focusEl = document.getElementById("bestSetupFocus");
	const relativeEl = document.getElementById("bestSetupRelative");
	const trustEl = document.getElementById("bestSetupTrust");
	const trustReasonEl = document.getElementById("bestSetupTrustReason");
	const asymmetryLabelEl = document.getElementById("bestSetupAsymmetryLabel");
	const asymmetryReasonEl = document.getElementById("bestSetupAsymmetryReason");
	const verdictLineEl = document.getElementById("bestSetupVerdictLine");
	const blockerLineEl = document.getElementById("bestSetupBlockerLine");
	const whyNowLineEl = document.getElementById("bestSetupWhyNow");
	const analogueEl = document.getElementById("bestSetupAnalogue");
	const outcomeEl = document.getElementById("bestSetupOutcome");
	const timingEl = document.getElementById("bestSetupTiming");
	const upgradeEl = document.getElementById("bestSetupUpgrade");
	const invalidateEl = document.getElementById("bestSetupInvalidate");
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
		if (headline) headline.textContent = "No high-conviction setup right now";
		if (verdictEl) verdictEl.textContent = "";
		if (blockerEl) blockerEl.textContent = "";
		if (evidenceEl) evidenceEl.textContent = "Waiting for enough live structure to judge.";
		actionabilityEl.textContent = "";
		priorityEl.textContent = "";
		focusEl.textContent = "";
		relativeEl.textContent = "";
		trustEl.textContent = "";
		trustReasonEl.textContent = "";
		asymmetryLabelEl.textContent = "";
		asymmetryReasonEl.textContent = "";
		verdictLineEl.textContent = "";
		blockerLineEl.textContent = "";
		whyNowLineEl.textContent = "";
		analogueEl.textContent = "";
		outcomeEl.textContent = "";
		timingEl.textContent = "";
		upgradeEl.textContent = "";
		invalidateEl.textContent = "";
		return;
	}

	const tier = visualTier(best.operator_verdict, best);
	const whyNow = best.why_now;
	const whyNot = best.dominant_blocker || best.why_not_higher;
	const verdict = best.operator_verdict;
	const actionability = best.actionability_label;
	const priority = best.priority_label;
	const operatorFocus = best.operator_focus;
	const relativeSetup = best.relative_setup_label;
	const trustLabel = best.trust_label;
	const trustReason = best.trust_reason;
	const asymmetryLabel = best.asymmetry_label;
	const asymmetryReason = best.asymmetry_reason;
	const analogue = best.historical_analogue_summary;
	const outcome = best.historical_outcome_band;
	const timing = best.historical_time_to_outcome;
	const upgrade = listText(best.upgrade_triggers);
	const invalidate = listText(best.invalidate_triggers);
	const shortMint = (best.mint || "").slice(0, 8) + "…";

	if (tier === "clean" && best.is_actionable) {
		if (headline) headline.textContent = shortMint + " is the cleanest live setup";
	} else if (best.decision === "WATCH" || best.decision === "READY" || best.decision === "BUY") {
		if (headline) headline.textContent = shortMint + " is the best available, not a free pass";
	} else {
		if (headline) headline.textContent = "Best available, but still low-conviction";
	}
	if (verdictEl) verdictEl.textContent = verdict;
	if (blockerEl) blockerEl.textContent = "Blocker: " + whyNot;
	if (evidenceEl) evidenceEl.textContent = whyNow;
	actionabilityEl.textContent = "actionability: " + actionability;
	priorityEl.textContent = "priority: " + priority;
	verdictLineEl.textContent = "verdict: " + verdict;
	blockerLineEl.textContent = "blocker: " + whyNot;
	trustEl.textContent = "trust: " + trustLabel;
	trustReasonEl.textContent = "trust reason: " + trustReason;
	asymmetryLabelEl.textContent = "asymmetry: " + asymmetryLabel;
	asymmetryReasonEl.textContent = "asymmetry reason: " + asymmetryReason;
	focusEl.textContent = "focus: " + operatorFocus;
	relativeEl.textContent = "relative setup: " + relativeSetup;
	whyNowLineEl.textContent = "why now: " + whyNow;
	analogueEl.textContent = "analogue: " + analogue;
	outcomeEl.textContent = "outcome: " + outcome;
	timingEl.textContent = "timing: " + timing;
	upgradeEl.textContent = "upgrade if: " + upgrade;
	invalidateEl.textContent = "invalidate if: " + invalidate;

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
