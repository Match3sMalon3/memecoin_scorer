package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"memecoin_scorer/internal/authenticity"
	"memecoin_scorer/internal/calibration"
	"memecoin_scorer/internal/catalyst"
	"memecoin_scorer/internal/cluster"
	"memecoin_scorer/internal/db"
	"memecoin_scorer/internal/ingestor"
	"memecoin_scorer/internal/live"
	"memecoin_scorer/internal/mode"
	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/outcomes"
	"memecoin_scorer/internal/proxy"
	"memecoin_scorer/internal/rpc"
	"memecoin_scorer/internal/setup"
	"memecoin_scorer/internal/shadow"
	"memecoin_scorer/internal/state"

	_ "github.com/lib/pq"
)

const maxBodyBytes = 4 << 20
const maxSnapshotLimit = 500

type signalSnapshotRecorder interface {
	RecordSignalSnapshot(context.Context, model.LiveSnapshot) (int64, bool, error)
}

type snapshotQuery struct {
	minBuyers      int
	sinceMinutes   int
	limit          int
	actionableOnly bool
}

type snapshotBuildResult struct {
	rows          []model.ScoredSnapshot
	snapshotCount int
	pricedRows    int
	marketCapRows int
	duration      time.Duration
}

type snapshotCache struct {
	mu         sync.RWMutex
	rows       []model.ScoredSnapshot
	updatedAt  time.Time
	refreshing atomic.Bool
}

// resolverFromEnv selects and constructs the active FunderResolver.
//
// Priority order:
//  1. HELIUS_API_KEY set → HeliusResolver (real live backend)
//  2. FUNDER_MAP_PATH set → StaticResolver (deterministic from JSON file)
//  3. Neither set → NullResolver (unhealthy sentinel; BUY/READY blocked when CLUSTER_REQUIRED=1)
//
// Env vars consumed:
//
//	HELIUS_API_KEY               Helius API key for live wallet-parent resolution
//	CLUSTER_LOOKBACK_HOURS       lookback window for Helius (default 72)
//	CLUSTER_CACHE_TTL_MINUTES    positive-cache TTL for Helius (default 120)
//	CLUSTER_MAX_CONCURRENCY      max concurrent Helius calls (default 8)
//	FUNDER_MAP_PATH              path to JSON funder-map file (wallet→parent)
func resolverFromEnv() cluster.FunderResolver {
	apiKey := os.Getenv("HELIUS_API_KEY")
	if apiKey != "" {
		lookback := envIntDefault("CLUSTER_LOOKBACK_HOURS", 72)
		cacheTTL := envIntDefault("CLUSTER_CACHE_TTL_MINUTES", 120)
		concurrency := envIntDefault("CLUSTER_MAX_CONCURRENCY", 8)

		r, err := cluster.NewHeliusResolver(cluster.HeliusResolverConfig{
			APIKey:         apiKey,
			LookbackHours:  lookback,
			CacheTTLMin:    cacheTTL,
			MaxConcurrency: concurrency,
		})
		if err != nil {
			log.Printf("WARNING: HeliusResolver init failed (%v) — falling through to FUNDER_MAP_PATH", err)
		} else {
			log.Printf("clustering backend: helius (lookback=%dh, cache_ttl=%dm, concurrency=%d)",
				lookback, cacheTTL, concurrency)
			return r
		}
	}

	mapPath := os.Getenv("FUNDER_MAP_PATH")
	if mapPath != "" {
		r, err := cluster.LoadStaticResolver(mapPath)
		if err != nil {
			log.Printf("WARNING: StaticResolver load failed (%v) — falling back to NullResolver", err)
		} else {
			log.Printf("clustering backend: static (path=%s, entries=%d)", mapPath, r.Len())
			return r
		}
	}

	log.Printf("WARNING: no clustering backend configured (HELIUS_API_KEY and FUNDER_MAP_PATH both unset)")
	log.Printf("WARNING: CLUSTER_REQUIRED=1 means BUY/READY will be disabled until a backend is set")
	return cluster.NullResolver{}
}

// pollerFromEnv constructs the ingress Poller when HELIUS_API_KEY is set.
// Returns nil when not configured — the main ingress path degrades gracefully.
//
// Env vars:
//
//	HELIUS_API_KEY              Required for polling to start.
//	INGRESS_POLL_INTERVAL_SEC   Seconds between poll cycles (default 10).
//	INGRESS_PROGRAMS            Comma-separated Solana program accounts to watch.
//	                            Default: Pump.fun bonding curve + Raydium AMM V4.
func pollerFromEnv(health *ingestor.IngressHealth) *ingestor.Poller {
	apiKey := os.Getenv("HELIUS_API_KEY")
	if apiKey == "" {
		log.Printf("ingress poller: HELIUS_API_KEY not set — polling disabled")
		return nil
	}

	intervalSec := envIntDefault("INGRESS_POLL_INTERVAL_SEC", 10)
	interval := time.Duration(intervalSec) * time.Second

	var programs []string
	if raw := strings.TrimSpace(os.Getenv("INGRESS_PROGRAMS")); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				programs = append(programs, p)
			}
		}
	}
	// programs == nil → NewPoller uses DefaultIngressPrograms

	return ingestor.NewPoller(ingestor.PollConfig{
		APIKey:   apiKey,
		Programs: programs,
		Interval: interval,
	}, health)
}

// depthClientFromEnv constructs a DepthClient from SOLANA_RPC_URL or HELIUS_API_KEY.
// Returns nil when neither source is available — all callers treat nil as "depth unavailable".
//
// Env vars:
//
//	SOLANA_RPC_URL   Solana JSON-RPC endpoint (e.g. https://mainnet.helius-rpc.com/?api-key=…)
//	HELIUS_API_KEY   Fallback source for Helius mainnet RPC when SOLANA_RPC_URL is absent
func depthClientFromEnv() *rpc.DepthClient {
	rpcURL, derived := depthRPCURLFromEnv(os.Getenv)
	if rpcURL == "" {
		log.Printf("depth client: no RPC URL available, real pool depth disabled, using observed_swaps_proxy")
		return nil
	}
	c := rpc.NewClient(rpcURL, 3*time.Second)
	if derived {
		log.Printf("depth client: using Helius mainnet RPC (SOLANA_RPC_URL not set, derived from HELIUS_API_KEY)")
	}
	log.Printf("depth client: real pool depth ENABLED via %s", maskAPIKey(rpcURL))
	return rpc.NewDepthClient(c)
}

func depthRPCURLFromEnv(getenv func(string) string) (string, bool) {
	rpcURL := strings.TrimSpace(getenv("SOLANA_RPC_URL"))
	if rpcURL != "" {
		return rpcURL, false
	}
	apiKey := strings.TrimSpace(getenv("HELIUS_API_KEY"))
	if apiKey == "" {
		return "", false
	}
	return "https://mainnet.helius-rpc.com/?api-key=" + apiKey, true
}

func maskAPIKey(raw string) string {
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "api-key="); i >= 0 {
		start := i + len("api-key=")
		end := len(raw)
		if amp := strings.Index(raw[start:], "&"); amp >= 0 {
			end = start + amp
		}
		return raw[:start] + "***" + raw[end:]
	}
	return raw
}

func openOutcomeRecorderDB() (*sql.DB, error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		dsn = "postgres://localhost:5432/meme_trading_system_v1?sslmode=disable"
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// makeApplyFn returns a function that applies a swap event to the store and,
// when a DepthClient is configured and the event carries a pool account address,
// asynchronously fetches real pool depth and updates the store via UpdateDepth.
//
// The goroutine is bounded by a 2-second context; failures are silent (real depth
// remains -1 and the observed_swaps_proxy fallback continues to apply).
func makeApplyFn(store *state.Store, dc *rpc.DepthClient) func(model.SwapEvent) bool {
	return func(ev model.SwapEvent) bool {
		applied := store.Apply(ev)
		if applied && dc != nil && ev.PoolAccountAddr != "" {
			go func(mint, poolAddr string) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result := dc.FetchDepth(ctx, poolAddr)
				if result.SOL >= 0 {
					store.UpdateDepth(mint, result.SOL, result.Source)
					log.Printf("depth client: real pool depth applied mint=%s pool=%s depth=%.4f source=%s", shortLogMint(mint), shortLogMint(poolAddr), result.SOL, result.Source)
				} else {
					log.Printf("depth client: verified WSOL vault depth unavailable mint=%s pool=%s fallback=observed_swaps_proxy", shortLogMint(mint), shortLogMint(poolAddr))
				}
			}(ev.TokenMint, ev.PoolAccountAddr)
		}
		return applied
	}
}

func shortLogMint(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 8 {
		return v
	}
	return v[:8] + "…"
}

// liveConfigFromEnv builds a LiveConfig from environment variables.
//
// Clustering:
//
//	HELIUS_API_KEY                              → HeliusResolver (live backend)
//	FUNDER_MAP_PATH                             → StaticResolver (deterministic)
//	CLUSTER_REQUIRED                            default 1; set 0 to disable hard gate
//	CLUSTER_LOOKBACK_HOURS                      default 72
//	CLUSTER_CACHE_TTL_MINUTES                   default 120
//	CLUSTER_MAX_CONCURRENCY                     default 8
//
// Execution / liquidity:
//
//	TRADE_SIZE_SOL                              default 1.0
//	LIQUIDITY_MULTIPLIER                        default 20.0
//
// Gate thresholds (see DefaultLiveConfig for all):
//
//	MIN_EXEC_QUALITY_BUY / READY / AVOID
//	MAX_ADVERSARIAL_BUY / READY
//	MAX_ESTIMATED_IMPACT_PCT                    default 15.0
//	MAX_SIGNAL_AGE_MINUTES_BUYREADY             default 5
//	MAX_SIGNAL_AGE_MINUTES_WATCH                default 15
//	MIN_TOKEN_AGE_SECONDS_FOR_BUY               default 90
//	MIN_EFFECTIVE_BUYERS_1M_FOR_CONFIDENT_BUY   default 3
//	MIN_TOTAL_EVENTS_FOR_CONFIDENCE             default 3
func liveConfigFromEnv() live.LiveConfig {
	cfg := live.DefaultLiveConfig()

	// Wire the real resolver — replaces NullResolver from DefaultLiveConfig.
	cfg.FunderResolver = resolverFromEnv()

	// CLUSTER_REQUIRED: default 1 (true); set to "0" to disable.
	if os.Getenv("CLUSTER_REQUIRED") == "0" {
		cfg.ClusterRequired = false
		log.Printf("clustering: CLUSTER_REQUIRED=0 — BUY/READY allowed even when clustering is degraded")
	}

	setPositiveFloat := func(dest *float64, key string) {
		if s := os.Getenv(key); s != "" {
			if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
				*dest = v
			}
		}
	}
	setUnitFloat := func(dest *float64, key string) {
		if s := os.Getenv(key); s != "" {
			if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 && v <= 1 {
				*dest = v
			}
		}
	}
	setPositiveInt := func(dest *int, key string) {
		if s := os.Getenv(key); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v > 0 {
				*dest = v
			}
		}
	}
	setNonNegFloat := func(dest *float64, key string) {
		if s := os.Getenv(key); s != "" {
			if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 {
				*dest = v
			}
		}
	}

	setPositiveFloat(&cfg.TradeSizeSOL, "TRADE_SIZE_SOL")
	setPositiveFloat(&cfg.LiquidityMultiplier, "LIQUIDITY_MULTIPLIER")
	setUnitFloat(&cfg.MinExecQualityBUY, "MIN_EXEC_QUALITY_BUY")
	setUnitFloat(&cfg.MinExecQualityREADY, "MIN_EXEC_QUALITY_READY")
	setUnitFloat(&cfg.MinExecQualityAVOID, "MIN_EXEC_QUALITY_AVOID")
	setUnitFloat(&cfg.MaxAdversarialBUY, "MAX_ADVERSARIAL_BUY")
	setUnitFloat(&cfg.MaxAdversarialREADY, "MAX_ADVERSARIAL_READY")
	setPositiveFloat(&cfg.MaxEstimatedImpactPct, "MAX_ESTIMATED_IMPACT_PCT")
	setPositiveFloat(&cfg.MaxSignalAgeMinBuyReady, "MAX_SIGNAL_AGE_MINUTES_BUYREADY")
	setPositiveFloat(&cfg.MaxSignalAgeMinWatch, "MAX_SIGNAL_AGE_MINUTES_WATCH")
	setNonNegFloat(&cfg.MinTokenAgeSecondsForBuy, "MIN_TOKEN_AGE_SECONDS_FOR_BUY")
	setPositiveInt(&cfg.MinEffBuyers1mForConfidentBuy, "MIN_EFFECTIVE_BUYERS_1M_FOR_CONFIDENT_BUY")
	setPositiveInt(&cfg.MinTotalEventsForConf, "MIN_TOTAL_EVENTS_FOR_CONFIDENCE")

	return cfg
}

// resolveIngestorPort resolves the port the ingestor HTTP server binds to.
//
// Priority (highest to lowest):
//  1. INGESTOR_PORT — explicit ingestor port; always wins
//  2. PORT          — legacy single-service var; only used when INGESTOR_PORT is absent
//  3. "8080"        — hard default
func resolveIngestorPort() string {
	if p := os.Getenv("INGESTOR_PORT"); p != "" {
		return p
	}
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}

func main() {
	secret := os.Getenv("HELIUS_WEBHOOK_SECRET")
	liveCfg := liveConfigFromEnv()
	store := state.New()
	calibrationRecorder := calibration.NewRecorder()

	// Optional DB persistence — gracefully disabled when DATABASE_URL is unset.
	dbStore, dbErr := db.Open()
	if dbErr != nil {
		log.Printf("WARNING: DB unavailable (%v) — running without persistence", dbErr)
	} else if dbStore != nil {
		log.Printf("db: persistence enabled")
	}
	var outcomeRecorder signalSnapshotRecorder
	var outcomeDB *sql.DB
	var ownedOutcomeDB *sql.DB
	if dbStore != nil {
		outcomeDB = dbStore.SQLDB()
		outcomeRecorder = outcomes.NewRecorder(outcomeDB)
	} else if recorderDB, err := openOutcomeRecorderDB(); err != nil {
		log.Printf("WARNING: outcome recorder DB unavailable (%v) — signal_snapshots recording disabled", err)
	} else if recorderDB != nil {
		outcomeDB = recorderDB
		ownedOutcomeDB = recorderDB
		defer ownedOutcomeDB.Close()
		outcomeRecorder = outcomes.NewRecorder(outcomeDB)
		log.Printf("outcome recorder: persistence enabled")
	}

	clusterBackend := cluster.ResolverBackendName(liveCfg.FunderResolver)
	clusterHealthy := cluster.IsResolverHealthy(liveCfg.FunderResolver)
	log.Printf("live config: trade_size=%.2f SOL, impact_max=%.1f%%, cluster_backend=%s, cluster_healthy=%v, cluster_required=%v",
		liveCfg.TradeSizeSOL, liveCfg.MaxEstimatedImpactPct,
		clusterBackend, clusterHealthy, liveCfg.ClusterRequired)

	// Ingress: poll-based broad-market discovery.
	ingressHealth := ingestor.NewIngressHealth()
	poller := pollerFromEnv(ingressHealth)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	depthClient := depthClientFromEnv()
	applyFn := makeApplyFn(store, depthClient)

	if poller != nil {
		poller.Start(ctx, applyFn)
	}
	if outcomeDB != nil {
		pricer := outcomes.NewVaultPricer(nil, outcomes.NewSQLSwapStore(outcomeDB))
		worker := outcomes.NewWorker(outcomeDB, pricer, liveCfg.TradeSizeSOL)
		go worker.Run(ctx)
		log.Printf("outcome worker: started")
	}

	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			n := store.PruneStale()
			if n > 0 {
				log.Printf("pruned %d stale tokens", n)
			}
		}
	}()

	srv := newServerWithCalibration(store, dbStore, calibrationRecorder, secret, liveCfg, ingressHealth, poller, depthClient, outcomeRecorder)

	addr := ":" + resolveIngestorPort()
	log.Printf("ingestor listening on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, srv))
}

// newServer is the test-compatible constructor. It always passes nil DepthClient
// (no real depth RPC in tests). Signature is intentionally stable.
func newServer(
	store *state.Store,
	dbStore *db.Store,
	secret string,
	liveCfg live.LiveConfig,
	ingressHealth *ingestor.IngressHealth,
	poller *ingestor.Poller,
) http.Handler {
	return newServerWithCalibration(store, dbStore, calibration.NewRecorder(), secret, liveCfg, ingressHealth, poller, nil, nil)
}

func newServerWithCalibration(
	store *state.Store,
	dbStore *db.Store,
	calibrationRecorder *calibration.Recorder,
	secret string,
	liveCfg live.LiveConfig,
	ingressHealth *ingestor.IngressHealth,
	poller *ingestor.Poller,
	depthClient *rpc.DepthClient,
	outcomeRecorder signalSnapshotRecorder,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", makeHealthzHandler(liveCfg, ingressHealth, poller))
	mux.HandleFunc("/webhook", makeWebhookHandler(store, dbStore, secret, depthClient))
	mux.HandleFunc("/api/snapshots", makeSnapshotsHandler(store, dbStore, calibrationRecorder, liveCfg, outcomeRecorder))
	mux.HandleFunc("/api/market-context", makeMarketContextHandler(store, liveCfg))
	mux.HandleFunc("/api/calibration-samples", makeCalibrationSamplesHandler(calibrationRecorder))

	if os.Getenv("ENABLE_LOCAL_ADMIN") == "1" {
		mux.HandleFunc("/admin/reset-state", makeResetHandler(store))
		log.Printf("WARNING: local admin endpoints enabled (ENABLE_LOCAL_ADMIN=1)")
	}

	return mux
}

// makeHealthzHandler returns a handler for GET /healthz.
//
// Response includes two subsections:
//
//	"clustering" — funder resolution backend status (required for BUY/READY)
//	"ingress"    — pull-based poller status (HELIUS_API_KEY required to activate)
func makeHealthzHandler(
	liveCfg live.LiveConfig,
	ingressHealth *ingestor.IngressHealth,
	poller *ingestor.Poller,
) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		clusterHealthy := cluster.IsResolverHealthy(liveCfg.FunderResolver)
		clusterBackend := cluster.ResolverBackendName(liveCfg.FunderResolver)
		clusterStats := cluster.ResolverStats(liveCfg.FunderResolver)
		clusterStatus := "healthy"
		if !clusterHealthy {
			clusterStatus = "degraded"
		}

		snap := ingressHealth.Snap()
		if poller != nil {
			snap.Programs = poller.Programs()
			snap.PollIntervalSec = poller.Interval().Seconds()
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"clustering": map[string]any{
				"enabled":              liveCfg.ClusterRequired,
				"required":             liveCfg.ClusterRequired,
				"backend":              clusterBackend,
				"healthy":              clusterHealthy,
				"status":               clusterStatus,
				"buy_ready_allowed":    !liveCfg.ClusterRequired || clusterHealthy,
				"consecutive_failures": clusterStats.ConsecutiveFailures,
				"timeout_fallbacks":    clusterStats.TimeoutFallbacks,
				"error_fallbacks":      clusterStats.ErrorFallbacks,
				"last_error":           clusterStats.LastError,
			},
			"ingress": snap,
		})
	}
}

func makeWebhookHandler(store *state.Store, dbStore *db.Store, secret string, depthClient *rpc.DepthClient) http.HandlerFunc {
	applyFn := makeApplyFn(store, depthClient)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+secret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if err != nil {
			http.Error(w, "error reading body", http.StatusInternalServerError)
			return
		}
		events, err := ingestor.NormalizeHelius(body)
		if err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		applied := 0
		ctx := r.Context()
		for _, ev := range events {
			if applyFn(ev) {
				applied++
				// Persist raw swap event to DB (best-effort, non-blocking).
				go dbStore.InsertSwapEvent(context.WithoutCancel(ctx), ev)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":              true,
			"events_received": len(events),
			"events_applied":  applied,
		})
	}
}

func makeSnapshotsHandler(store *state.Store, dbStore *db.Store, calibrationRecorder *calibration.Recorder, liveCfg live.LiveConfig, outcomeRecorder signalSnapshotRecorder) http.HandlerFunc {
	cache := &snapshotCache{}
	useCachedPath := cluster.ResolverBackendName(liveCfg.FunderResolver) == "helius"
	if useCachedPath {
		go cache.refreshLoop(store, dbStore, calibrationRecorder, liveCfg, outcomeRecorder)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := parseSnapshotQuery(r)
		start := time.Now()
		var res snapshotBuildResult
		if useCachedPath {
			cache.triggerRefresh(store, dbStore, calibrationRecorder, liveCfg, outcomeRecorder)
			res = cache.filtered(query, time.Now())
			log.Printf("snapshots api latency: source=cache total_ms=%d cache_age_ms=%d rows=%d",
				time.Since(start).Milliseconds(), time.Since(cache.updated()).Milliseconds(), len(res.rows))
		} else {
			res = buildSnapshotRows(store, dbStore, calibrationRecorder, liveCfg, outcomeRecorder, query, true)
			log.Printf("snapshots api latency: source=sync total_ms=%d build_ms=%d rows=%d",
				time.Since(start).Milliseconds(), res.duration.Milliseconds(), len(res.rows))
		}
		log.Printf("snapshots api: snapshot_count=%d row_count=%d priced_rows=%d market_cap_rows=%d min_buyers=%d since_minutes=%d actionable_only=%t",
			res.snapshotCount, len(res.rows), res.pricedRows, res.marketCapRows, query.minBuyers, query.sinceMinutes, query.actionableOnly)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(res.rows); err != nil {
			log.Printf("snapshots encode: %v", err)
		}
	}
}

func parseSnapshotQuery(r *http.Request) snapshotQuery {
	q := r.URL.Query()
	limit := queryInt(q.Get("limit"), 200)
	if limit > maxSnapshotLimit {
		limit = maxSnapshotLimit
	}
	if limit <= 0 {
		limit = 200
	}
	return snapshotQuery{
		minBuyers:      queryNonNegInt(q.Get("min_buyers"), 0),
		sinceMinutes:   queryInt(q.Get("since_minutes"), 30),
		limit:          limit,
		actionableOnly: q.Get("actionable_only") == "1",
	}
}

func (c *snapshotCache) refreshLoop(store *state.Store, dbStore *db.Store, calibrationRecorder *calibration.Recorder, liveCfg live.LiveConfig, outcomeRecorder signalSnapshotRecorder) {
	c.refresh(store, dbStore, calibrationRecorder, liveCfg, outcomeRecorder)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		c.refresh(store, dbStore, calibrationRecorder, liveCfg, outcomeRecorder)
	}
}

func (c *snapshotCache) triggerRefresh(store *state.Store, dbStore *db.Store, calibrationRecorder *calibration.Recorder, liveCfg live.LiveConfig, outcomeRecorder signalSnapshotRecorder) {
	if time.Since(c.updated()) < 5*time.Second {
		return
	}
	go c.refresh(store, dbStore, calibrationRecorder, liveCfg, outcomeRecorder)
}

func (c *snapshotCache) refresh(store *state.Store, dbStore *db.Store, calibrationRecorder *calibration.Recorder, liveCfg live.LiveConfig, outcomeRecorder signalSnapshotRecorder) {
	if !c.refreshing.CompareAndSwap(false, true) {
		return
	}
	defer c.refreshing.Store(false)
	query := snapshotQuery{minBuyers: 0, sinceMinutes: 240, limit: maxSnapshotLimit}
	res := buildSnapshotRows(store, dbStore, calibrationRecorder, liveCfg, outcomeRecorder, query, true)
	c.mu.Lock()
	c.rows = append(c.rows[:0], res.rows...)
	c.updatedAt = time.Now()
	c.mu.Unlock()
	log.Printf("snapshots cache refresh: rows=%d snapshot_count=%d duration_ms=%d", len(res.rows), res.snapshotCount, res.duration.Milliseconds())
}

func (c *snapshotCache) updated() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.updatedAt
}

func (c *snapshotCache) filtered(query snapshotQuery, now time.Time) snapshotBuildResult {
	c.mu.RLock()
	rows := append([]model.ScoredSnapshot(nil), c.rows...)
	updated := c.updatedAt
	c.mu.RUnlock()
	cutoff := now.Add(-time.Duration(query.sinceMinutes) * time.Minute)
	out := make([]model.ScoredSnapshot, 0, minInt(query.limit, len(rows)))
	pricedRows := 0
	marketCapRows := 0
	for _, row := range rows {
		if row.LastEventAt.Before(cutoff) || row.UniqueBuyerCount < query.minBuyers {
			continue
		}
		if query.actionableOnly && !row.IsActionable {
			continue
		}
		if row.LastPriceSOL > 0 {
			pricedRows++
		}
		if row.MarketCapSOL > 0 {
			marketCapRows++
		}
		out = append(out, row)
		if len(out) >= query.limit {
			break
		}
	}
	_ = updated
	return snapshotBuildResult{rows: out, snapshotCount: len(rows), pricedRows: pricedRows, marketCapRows: marketCapRows}
}

func buildSnapshotRows(store *state.Store, dbStore *db.Store, calibrationRecorder *calibration.Recorder, liveCfg live.LiveConfig, outcomeRecorder signalSnapshotRecorder, query snapshotQuery, recordOutcomes bool) snapshotBuildResult {
	start := time.Now()
	now := time.Now()
	snapshots := store.RecentTokens(time.Duration(query.sinceMinutes) * time.Minute)
	out := make([]model.ScoredSnapshot, 0, minInt(query.limit, len(snapshots)))
	pricedRows := 0
	marketCapRows := 0
	for _, s := range snapshots {
		if s.UniqueBuyerCount < query.minBuyers {
			continue
		}
		scored := buildScoredSnapshot(store, liveCfg, s, now)
		if recordOutcomes {
			go recordSignalSnapshot(context.Background(), outcomeRecorder, scored)
		}
		if dbStore != nil && scored.IsActionable && (scored.Decision == "BUY" || scored.Decision == "READY") {
			go dbStore.InsertSignal(context.Background(), scored)
			logSignalLine(scored)
		}
		if query.actionableOnly && !scored.IsActionable {
			continue
		}
		if scored.LastPriceSOL > 0 {
			pricedRows++
		}
		if scored.MarketCapSOL > 0 {
			marketCapRows++
		}
		out = append(out, scored)
		if len(out) >= query.limit {
			break
		}
	}
	live.AssignPriorityLabels(out)
	if calibrationRecorder != nil {
		calibrationRecorder.ObserveRows(out, now)
	}
	return snapshotBuildResult{rows: out, snapshotCount: len(snapshots), pricedRows: pricedRows, marketCapRows: marketCapRows, duration: time.Since(start)}
}

func buildScoredSnapshot(store *state.Store, liveCfg live.LiveConfig, s model.TokenSnapshot, now time.Time) model.ScoredSnapshot {
	d := live.ClassifyAt(s, liveCfg, now)
	scored := model.ScoredSnapshot{
		TokenSnapshot:               s,
		Decision:                    d.Label,
		Reasons:                     d.Reasons,
		ExecutionPenalty:            d.ExecutionPenalty,
		LiquidityProxySOL:           d.LiquidityProxySOL,
		LiquidityEvidenceSource:     d.LiquidityEvidenceSource,
		LiquidityEvidenceAgeSeconds: d.LiquidityEvidenceAgeSeconds,
		LiquidityProxyReliable:      d.LiquidityProxyReliable,
		AdversarialScore:            d.AdversarialScore,
		TradeSizeSOL:                d.TradeSizeSOL,
		EstimatedImpactPct:          d.EstimatedImpactPct,
		EffectiveBuyers1m:           d.EffectiveBuyers1m,
		EffectiveBuyers5m:           d.EffectiveBuyers5m,
		ClusteredBuyerCount:         d.ClusteredBuyerCount,
		FundingClusterRatio:         d.FundingClusterRatio,
		ClusterCompressionRatio1m:   d.ClusterCompressionRatio1m,
		ClusterCompressionRatio5m:   d.ClusterCompressionRatio5m,
		ClusteringStatus:            d.ClusteringStatus,
		ClusteringBackend:           d.ClusteringBackend,
		ClusteringRowStatus:         d.ClusteringRowStatus,
		ClusteringTimeouts:          d.ClusteringTimeouts,
		ClusteringFallbacks:         d.ClusteringFallbacks,
		SignalState:                 d.SignalState,
		IsActionable:                d.IsActionable,
		ConfidenceScore:             d.ConfidenceScore,
		WarmingUp:                   d.WarmingUp,
		WhyNow:                      d.WhyNow,
		WhyNotHigher:                d.WhyNotHigher,
		DominantBlocker:             d.DominantBlocker,
		OperatorVerdict:             d.OperatorVerdict,
		ExecutionURL:                d.ExecutionURL,
		HistoricalAnalogueSummary:   d.HistoricalAnalogueSummary,
		HistoricalOutcomeBand:       d.HistoricalOutcomeBand,
		HistoricalTimeToOutcome:     d.HistoricalTimeToOutcome,
		UpgradeTriggers:             d.UpgradeTriggers,
		InvalidateTriggers:          d.InvalidateTriggers,
		ActionabilityLabel:          d.ActionabilityLabel,
		PriorityLabel:               d.PriorityLabel,
		LastPriceSol:                s.LastPriceSOL,
		MarketCapSol:                s.MarketCapSOL,
		Layer0Reject:                d.Engine.Layer0Reject,
		Shadow:                      shadow.EvaluateShadowScore(&s, now),
		Engine:                      d.Engine,
	}
	scored.ExecutionURL = live.BuildExecutionURL(scored.Mint)
	scored.WhyNow = live.BuildWhyNow(&scored)
	scored.WhyNotHigher = live.BuildWhyNotHigher(&scored)
	scored.DominantBlocker = live.BuildDominantBlocker(&scored)
	scored.OperatorVerdict = live.BuildOperatorVerdict(&scored)
	scored.ActionabilityLabel = live.BuildActionabilityLabel(&scored)
	scored.HistoricalAnalogueSummary = live.BuildHistoricalAnalogueSummary(&scored)
	scored.HistoricalOutcomeBand = live.BuildHistoricalOutcomeBand(&scored)
	scored.HistoricalTimeToOutcome = live.BuildHistoricalTimeToOutcome(&scored)
	scored.UpgradeTriggers = live.BuildUpgradeTriggers(&scored)
	scored.InvalidateTriggers = live.BuildInvalidateTriggers(&scored)
	buys5m := store.GetBuyEvents(scored.Mint, 5*time.Minute)
	totalBuySOL5m := 0.0
	uniqueBuyers5m := map[string]bool{}
	for _, buy := range buys5m {
		totalBuySOL5m += buy.SolAmount
		if buy.Wallet != "" {
			uniqueBuyers5m[buy.Wallet] = true
		}
	}
	if len(buys5m) > 0 {
		scored.SolPerTrade5m = totalBuySOL5m / float64(len(buys5m))
		scored.SOLPerTrade5m = scored.SolPerTrade5m
	}
	if len(uniqueBuyers5m) > 0 {
		scored.SolPerUniqueBuyer5m = totalBuySOL5m / float64(len(uniqueBuyers5m))
		scored.SOLPerBuyer5m = scored.SolPerUniqueBuyer5m
	}
	if scored.BondingCurveProgressPct == 0 {
		scored.BondingCurveProgressPct = -1
	}
	if scored.BondingCurveProgressPct > 0 && scored.VSolPerMinute > 0 {
		scored.BondingVelocitySolPerMin = scored.VSolPerMinute
		scored.BondingEvidenceSource = "exact"
	} else if scored.IsPumpFun && scored.ObservedAgeSeconds > 0 {
		scored.BondingVelocitySolPerMin = bondingVelocityProxy(scored.TotalBuySOL, scored.ObservedAgeSeconds)
		scored.BondingEvidenceSource = "flow_proxy"
	} else {
		scored.BondingEvidenceSource = "unknown"
	}
	if scored.BondingCurveProgressPct >= 0 {
		scored.GraduationProximityPct = 100 - scored.BondingCurveProgressPct
	}
	scored.TradesToReachCurrentVsol = len(store.GetBuyEvents(scored.Mint, 24*time.Hour))
	proxy.ApplyAuthenticityEvidence(&scored)
	scored.EarlyProxy = proxy.ScoreEarlyProxy(scored)
	scored.TokenMode = mode.Classify(scored)
	scored.Catalyst = catalyst.Detect(scored)
	scored.Authenticity = authenticity.Detect(scored, buys5m, store.GetWalletEvents(scored.Mint), store.GetCreationBlock(scored.Mint))
	scored.Setup = setup.Classify(scored)
	scored.ReviewChecklist = scored.Setup.ReviewChecklist
	return scored
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func recordSignalSnapshot(ctx context.Context, recorder signalSnapshotRecorder, row model.LiveSnapshot) {
	if recorder == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, _, err := recorder.RecordSignalSnapshot(ctx, row); err != nil {
		log.Printf("outcome snapshot record %s: %v", row.Mint, err)
	}
}

func bondingVelocityProxy(totalBuySOL float64, observedAgeSeconds float64) float64 {
	if totalBuySOL <= 0 || observedAgeSeconds <= 0 {
		return 0
	}
	minutes := observedAgeSeconds / 60
	if minutes < 1.0/60.0 {
		minutes = 1.0 / 60.0
	}
	return totalBuySOL / minutes
}

func makeMarketContextHandler(store *state.Store, liveCfg live.LiveConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		now := time.Now()
		snapshots := store.RecentTokens(24 * time.Hour)
		watched := 0
		runners := 0
		best := 0.0
		for _, s := range snapshots {
			d := live.ClassifyAt(s, liveCfg, now)
			row := model.LiveSnapshot{
				TokenSnapshot:               s,
				Decision:                    d.Label,
				ExecutionPenalty:            d.ExecutionPenalty,
				LiquidityProxySOL:           d.LiquidityProxySOL,
				EstimatedImpactPct:          d.EstimatedImpactPct,
				EffectiveBuyers1m:           d.EffectiveBuyers1m,
				EffectiveBuyers5m:           d.EffectiveBuyers5m,
				ClusteringRowStatus:         d.ClusteringRowStatus,
				FundingClusterRatio:         d.FundingClusterRatio,
				AdversarialScore:            d.AdversarialScore,
				SignalState:                 d.SignalState,
				IsActionable:                d.IsActionable,
				ConfidenceScore:             d.ConfidenceScore,
				LiquidityProxyReliable:      d.LiquidityProxyReliable,
				LiquidityEvidenceSource:     d.LiquidityEvidenceSource,
				LiquidityEvidenceAgeSeconds: d.LiquidityEvidenceAgeSeconds,
				Engine:                      d.Engine,
			}
			proxy.ApplyAuthenticityEvidence(&row)
			ep := proxy.ScoreEarlyProxy(row)
			if ep.Score > best {
				best = ep.Score
			}
			switch ep.Band {
			case "RUNNER":
				runners++
			case "WATCH":
				watched++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tokens_seen_today":    store.SnapshotCount(24 * time.Hour),
			"tokens_watched_today": watched,
			"tokens_runner_now":    runners,
			"best_score_today":     best,
		})
	}
}

func makeCalibrationSamplesHandler(calibrationRecorder *calibration.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := queryInt(r.URL.Query().Get("limit"), 100)
		if limit > maxSnapshotLimit {
			limit = maxSnapshotLimit
		}
		samples := []calibration.Record{}
		if calibrationRecorder != nil {
			samples = calibrationRecorder.Samples(limit)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(samples); err != nil {
			log.Printf("calibration samples encode: %v", err)
		}
	}
}

func makeResetHandler(store *state.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if host != "127.0.0.1" && host != "::1" {
			http.Error(w, "forbidden: localhost only", http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("confirm") != "RESET_LIVE_STATE" {
			http.Error(w, "missing confirm token", http.StatusBadRequest)
			return
		}
		store.Reset()
		log.Printf("ADMIN: live store cleared via /admin/reset-state from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": "live store cleared"})
	}
}

func queryInt(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

func queryNonNegInt(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return def
	}
	return v
}

// logSignalLine emits a single-line operator console entry for every
// actionable BUY/READY signal:
//
//	[MINT] | SCORE | DECISION | WHY_NOW | liq=N SOL eff_buyers_1m=N cluster=X%
func logSignalLine(s model.ScoredSnapshot) {
	clusterPct := s.FundingClusterRatio * 100
	log.Printf("[%s] | score=%.0f | %s | %s | liq=%.0fSOL eff1m=%d cluster=%.0f%% gates=%d/%d",
		s.Mint,
		s.ConfidenceScore,
		s.Decision,
		s.WhyNow,
		s.LiquidityProxySOL,
		s.EffectiveBuyers1m,
		clusterPct,
		s.Engine.GatesPassCount,
		len(s.Engine.Gates),
	)
}

func envIntDefault(key string, def int) int {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return v
		}
	}
	return def
}
