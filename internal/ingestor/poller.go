package ingestor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"memecoin_scorer/internal/model"
)

// DefaultIngressPrograms lists the AMM program accounts polled by default for
// broad live Solana memecoin discovery.  All SWAP events on these programs are
// ingested regardless of which wallets or tokens are involved.
//
// Override with INGRESS_PROGRAMS (comma-separated) to watch additional or
// different programs.
var DefaultIngressPrograms = []string{
	"6EF8rrecthR5Dkzon8Nwu78hRvfCKubJ14M5uBEwF6P",  // Pump.fun bonding curve
	"675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8", // Raydium AMM V4
}

// PollConfig configures the Helius transaction history poller.
type PollConfig struct {
	// APIKey is the Helius API key.  Required.  NewPoller returns nil when empty.
	APIKey string

	// Programs is the list of Solana program accounts to watch.
	// Defaults to DefaultIngressPrograms when empty.
	Programs []string

	// Interval between poll cycles.  Defaults to 10s.
	Interval time.Duration

	// Limit is the number of transactions requested per program per poll (1-100).
	// Defaults to 100.
	Limit int

	// HeliusAPIBase overrides the Helius API base URL.  Used in tests.
	// Defaults to "https://api.helius.xyz".
	HeliusAPIBase string
}

// IngressHealth is written by the Poller and read by the /healthz handler.
// All fields are updated atomically and safe for concurrent use.
type IngressHealth struct {
	configured  atomic.Bool
	connected   atomic.Bool
	lastEventAt atomic.Int64 // unix nano; 0 = never
	eventsTotal atomic.Int64
	lastRawTxns atomic.Int64
	lastNorm    atomic.Int64
	lastApplied atomic.Int64
	lastErrMsg  atomic.Value // always stores a string
}

// NewIngressHealth returns a zero-value IngressHealth.
func NewIngressHealth() *IngressHealth {
	h := &IngressHealth{}
	h.lastErrMsg.Store("") // prime the atomic.Value with the concrete type
	return h
}

// Snap returns a JSON-serialisable view of current ingress health.
func (h *IngressHealth) Snap() IngressHealthSnap {
	var lastEventAgoSec float64
	if nano := h.lastEventAt.Load(); nano > 0 {
		lastEventAgoSec = time.Since(time.Unix(0, nano)).Seconds()
	}
	errMsg, _ := h.lastErrMsg.Load().(string)
	return IngressHealthSnap{
		Configured:      h.configured.Load(),
		Connected:       h.connected.Load(),
		EventsTotal:     h.eventsTotal.Load(),
		LastRawFetched:  h.lastRawTxns.Load(),
		LastNormalized:  h.lastNorm.Load(),
		LastApplied:     h.lastApplied.Load(),
		LastEventAgoSec: lastEventAgoSec,
		LastError:       errMsg,
	}
}

// IngressHealthSnap is the JSON representation of IngressHealth.
type IngressHealthSnap struct {
	Configured      bool     `json:"configured"`
	Connected       bool     `json:"connected"`
	Programs        []string `json:"programs"`
	PollIntervalSec float64  `json:"poll_interval_sec"`
	EventsTotal     int64    `json:"events_total"`
	LastRawFetched  int64    `json:"last_raw_fetched"`
	LastNormalized  int64    `json:"last_normalized"`
	LastApplied     int64    `json:"last_applied"`
	LastEventAgoSec float64  `json:"last_event_ago_sec"`
	LastError       string   `json:"last_error,omitempty"`
}

// Poller polls the Helius transaction history API for recent SWAP events on
// watched AMM program accounts, normalises them via NormalizeHelius, and feeds
// them to the caller's apply function.
//
// Each program maintains an independent cursor (last-seen transaction signature)
// so that only new transactions are processed on each poll cycle.
// The state.Store's signature ring handles any residual duplicates.
type Poller struct {
	cfg     PollConfig
	health  *IngressHealth
	cursors map[string]string // program → last-seen sig (newest processed)
}

// NewPoller constructs a Poller.  Returns nil (not an error) when APIKey is
// empty — the caller must check whether the returned pointer is non-nil.
func NewPoller(cfg PollConfig, health *IngressHealth) *Poller {
	if cfg.APIKey == "" {
		return nil
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.Limit <= 0 || cfg.Limit > 100 {
		cfg.Limit = 100
	}
	if cfg.HeliusAPIBase == "" {
		cfg.HeliusAPIBase = "https://api.helius.xyz"
	}
	if len(cfg.Programs) == 0 {
		cfg.Programs = DefaultIngressPrograms
	}
	health.configured.Store(true)
	return &Poller{
		cfg:     cfg,
		health:  health,
		cursors: make(map[string]string),
	}
}

// Start launches the poll loop in a background goroutine and returns
// immediately.  The goroutine exits when ctx is cancelled.
func (p *Poller) Start(ctx context.Context, apply func(model.SwapEvent) bool) {
	go p.run(ctx, apply)
}

// Programs returns the list of program accounts being polled.
func (p *Poller) Programs() []string { return p.cfg.Programs }

// Interval returns the configured poll interval.
func (p *Poller) Interval() time.Duration { return p.cfg.Interval }

func (p *Poller) run(ctx context.Context, apply func(model.SwapEvent) bool) {
	log.Printf("ingress poller: starting — programs=[%s] interval=%s limit=%d",
		strings.Join(p.cfg.Programs, ", "), p.cfg.Interval, p.cfg.Limit)
	p.poll(apply) // poll immediately at startup; don't wait for first tick
	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("ingress poller: stopped")
			return
		case <-ticker.C:
			p.poll(apply)
		}
	}
}

func (p *Poller) poll(apply func(model.SwapEvent) bool) {
	var cycleRaw, cycleNorm, cycleApplied int
	for _, prog := range p.cfg.Programs {
		events, newCursor, rawFetched, normalized, err := p.fetchSince(prog, p.cursors[prog])
		if err != nil {
			p.health.connected.Store(false)
			p.health.lastErrMsg.Store(err.Error())
			log.Printf("ingress poller: %s…: %v", prog[:8], err)
			continue
		}
		p.health.connected.Store(true)
		p.health.lastErrMsg.Store("")
		if newCursor != "" {
			p.cursors[prog] = newCursor
		}
		cycleRaw += rawFetched
		cycleNorm += normalized
		applied := 0
		for _, ev := range events {
			if apply(ev) {
				applied++
				p.health.eventsTotal.Add(1)
				p.health.lastEventAt.Store(time.Now().UnixNano())
			}
		}
		cycleApplied += applied
		log.Printf("ingress poller: %s… raw_fetched=%d normalized=%d applied=%d",
			prog[:8], rawFetched, normalized, applied)
	}
	p.health.lastRawTxns.Store(int64(cycleRaw))
	p.health.lastNorm.Store(int64(cycleNorm))
	p.health.lastApplied.Store(int64(cycleApplied))
	log.Printf("ingress cycle: raw_fetched=%d normalized=%d applied=%d",
		cycleRaw, cycleNorm, cycleApplied)
}

// fetchSince fetches recent SWAP transactions for prog from the Helius API,
// returning only those with signatures newer than cursor.
//
// Response order from Helius is newest-first.  The function:
//  1. Requests the latest `limit` transactions.
//  2. Extracts all raw signatures (for cursor tracking, independent of normalisation).
//  3. Collects signatures that appear before cursor in the list (= newer than cursor).
//  4. Calls NormalizeHelius and keeps only events whose signatures are in the new set.
//  5. Returns events in oldest-first order, the new cursor, and any error.
//
// When cursor is "" (first poll), all returned transactions are considered new.
// When cursor is not found in the response (gap larger than limit), all returned
// transactions are treated as new; the store's signature ring handles duplicates.
func (p *Poller) fetchSince(prog, cursor string) ([]model.SwapEvent, string, int, int, error) {
	url := fmt.Sprintf("%s/v0/addresses/%s/transactions?api-key=%s&limit=%d&type=SWAP",
		p.cfg.HeliusAPIBase, prog, p.cfg.APIKey, p.cfg.Limit)

	resp, err := http.Get(url) //nolint:gosec // URL is built from trusted config
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", 0, 0, fmt.Errorf("helius status %d: %s", resp.StatusCode, truncateStr(string(body), 300))
	}

	// 1. Extract raw signatures for cursor tracking (newest-first order preserved).
	rawSigs := parseRawSigs(body)
	rawFetched := len(rawSigs)

	// 2. Determine the new cursor (newest sig in this response).
	newCursor := ""
	if len(rawSigs) > 0 {
		newCursor = rawSigs[0]
	}

	// 3. Build the set of signatures that are newer than cursor.
	//    If cursor == "" (first poll) or cursor not found, all sigs are new.
	newSigSet := make(map[string]struct{}, len(rawSigs))
	for _, sig := range rawSigs {
		if cursor != "" && sig == cursor {
			break // reached last-seen sig; all further sigs are already processed
		}
		newSigSet[sig] = struct{}{}
	}
	// No cursor means everything is new; newSigSet already holds all sigs.

	// 4. Normalise and filter.
	all, err := NormalizeHelius(body)
	if err != nil {
		return nil, "", rawFetched, 0, fmt.Errorf("normalize: %w", err)
	}

	var relevant []model.SwapEvent
	for _, ev := range all {
		if _, ok := newSigSet[ev.Signature]; ok {
			relevant = append(relevant, ev)
		}
	}

	// 5. Reverse to oldest-first so the store sees them in chronological order.
	for i, j := 0, len(relevant)-1; i < j; i, j = i+1, j-1 {
		relevant[i], relevant[j] = relevant[j], relevant[i]
	}
	return relevant, newCursor, rawFetched, len(relevant), nil
}

// parseRawSigs extracts transaction signatures from a Helius API response body.
// Handles both the bare-array form and the wrapped {"transactions":[...]} form.
// Returns signatures in the original API order (newest-first from Helius).
type rawSigRow struct {
	Sig string `json:"signature"`
}

func parseRawSigs(body []byte) []string {
	var rows []rawSigRow
	if json.Unmarshal(body, &rows) != nil {
		var wrapped struct {
			Transactions []rawSigRow `json:"transactions"`
		}
		if json.Unmarshal(body, &wrapped) == nil {
			rows = wrapped.Transactions
		}
	}
	sigs := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Sig != "" {
			sigs = append(sigs, r.Sig)
		}
	}
	return sigs
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
