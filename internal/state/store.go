// Package state provides a thread-safe in-memory store of per-token swap activity.
// It is designed for live ingestion: events are applied as they arrive, and
// read-only TokenSnapshot values are derived on demand.
//
// # Memory bounds
//
//   - Buy history per token is capped at MaxBuyHistoryPerToken entries (oldest evicted).
//   - Sell history per token is capped at MaxSellHistoryPerToken entries (oldest evicted).
//   - The unique-buyer map per token is unbounded; it records all wallets ever seen.
//   - Signature deduplication uses a bounded circular ring of size SigCacheCap.
//   - Tokens inactive for longer than StaleDuration are removed by PruneStale.
//
// # What is idempotent
//
// An event with a non-empty Signature is idempotent within the deduplication
// horizon: while the signature remains in the ring (i.e. within the last
// SigCacheCap events globally), a duplicate is silently dropped and Apply
// returns false.  After eviction from the ring the same signature is
// re-processed exactly as a new event.
//
// Events with an empty Signature bypass deduplication entirely and are always
// applied.  This means replaying empty-sig events double-counts them.  Callers
// that need idempotency must supply a non-empty Signature.
//
// # What is NOT idempotent across restart
//
// The store is entirely in-memory.  On process restart the ring is empty, so
// all replayed events (even those with non-empty signatures) are re-processed.
// The store is a live rolling view, not a replay log.
//
// # Ordering guarantees
//
// Events are stored in arrival order, NOT in block-time order.  All window
// metrics (BuyersLast1m, BuyersLast5m, adversarial indicators) perform full
// scans over the bounded buy history and do not assume any ordering.  Out-of-
// order delivery from Helius — e.g. batch retries arriving newest-first — is
// handled correctly.
//
// FirstSeenAt tracks the minimum block time observed across all events for a
// token so that AgeSeconds remains accurate even under out-of-order delivery.
// LastEventAt tracks the maximum block time.
//
// # What is approximate
//
// TotalBuySOL, TotalSellSOL, UniqueBuyerCount, and SellTradeCount are exact:
// they accumulate from every applied event.
//
// Window metrics (BuyersLast1m, BuyersLast5m, and all adversarial indicators)
// are computed from the bounded buy history only.  When a token has more than
// MaxBuyHistoryPerToken buy events, the oldest entries are evicted and those
// wallets are no longer visible to window metrics — even though they remain
// counted in UniqueBuyerCount and TotalBuySOL.  Window metrics therefore
// under-count on extremely high-activity tokens.
package state

import (
	"sort"
	"sync"
	"time"

	"memecoin_scorer/internal/engine"
	"memecoin_scorer/internal/model"
)

// Configurable limits — defined as package-level constants so they appear in one place.
const (
	// MaxBuyHistoryPerToken is the maximum number of timed buy records kept per token.
	// When exceeded, the oldest entry is evicted.
	MaxBuyHistoryPerToken = 500

	// MaxSellHistoryPerToken is the maximum number of timed sell records kept per token.
	// Only the last-1m window is needed for the sell-reversal veto, so 200 entries is
	// more than sufficient under normal trading cadences.
	MaxSellHistoryPerToken = 200

	// StaleDuration is the minimum idle time before a token is eligible for pruning.
	StaleDuration = 4 * time.Hour

	// SigCacheCap is the maximum number of signatures held in the deduplication ring.
	// Idempotency guarantee: duplicate retries are silently dropped while the
	// signature remains in the ring (i.e. within the last SigCacheCap events).
	// After eviction, the same signature is re-processed.
	// On process restart the ring is empty, so all replayed events are re-processed.
	// This is intentional: the store is an in-memory live view, not a replay log.
	SigCacheCap = 10_000
)

// Store is a thread-safe in-memory store of per-token swap activity.
type Store struct {
	mu     sync.RWMutex
	tokens map[string]*tokenState
	sigs   *sigRing
	now    func() time.Time
}

// New returns a production Store with default constants.
func New() *Store {
	return newStore(SigCacheCap, time.Now)
}

// NewWithClock returns a Store using a custom clock function.
// Intended for deterministic tests.
func NewWithClock(clock func() time.Time) *Store {
	return newStore(SigCacheCap, clock)
}

func newStore(sigCap int, clock func() time.Time) *Store {
	return &Store{
		tokens: make(map[string]*tokenState),
		sigs:   newSigRing(sigCap),
		now:    clock,
	}
}

// Apply records one SwapEvent. Returns true if the event was stored, false if it
// was a duplicate and silently dropped. Events with an empty Signature are always
// stored (no dedup attempted) and return true.
func (s *Store) Apply(ev model.SwapEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ev.Signature != "" {
		if s.sigs.seen(ev.Signature) {
			return false
		}
		s.sigs.add(ev.Signature)
	}

	st, ok := s.tokens[ev.TokenMint]
	if !ok {
		st = &tokenState{
			Mint:             ev.TokenMint,
			FirstSeenAt:      ev.BlockTime,
			LastEventAt:      ev.BlockTime,
			uniqueBuyers:     make(map[string]struct{}),
			walletNetTokens:  make(map[string]float64),
			walletBuySOL:     make(map[string]float64),
			walletSellSOL:    make(map[string]float64),
			walletFirstBuyAt: make(map[string]time.Time),
			realPoolDepthSOL: -1, // sentinel: use observed proxy until real depth arrives
		}
		s.tokens[ev.TokenMint] = st
	}
	// FirstSeenAt tracks the minimum observed block time across all events.
	// This ensures AgeSeconds is accurate even when events arrive out of order.
	if ev.BlockTime.Before(st.FirstSeenAt) {
		st.FirstSeenAt = ev.BlockTime
	}
	if ev.BlockTime.After(st.LastEventAt) {
		st.LastEventAt = ev.BlockTime
	}

	st.totalEventCount++

	if ev.IsBuy {
		st.buySOL += ev.SOLAmount
		st.uniqueBuyers[ev.WalletAddr] = struct{}{}

		// Track net token balance for holder concentration (Gate 2).
		if ev.TokenAmount > 0 {
			st.walletNetTokens[ev.WalletAddr] += ev.TokenAmount
		}
		// Track buy cost basis for organic winner detection (Gate 5).
		st.walletBuySOL[ev.WalletAddr] += ev.SOLAmount
		if _, seen := st.walletFirstBuyAt[ev.WalletAddr]; !seen {
			st.walletFirstBuyAt[ev.WalletAddr] = ev.BlockTime
		}
		buy := timestampedBuy{At: ev.BlockTime, Wallet: ev.WalletAddr, SOL: ev.SOLAmount, TokenAmount: ev.TokenAmount}
		if len(st.buyHistory) < MaxBuyHistoryPerToken {
			st.buyHistory = append(st.buyHistory, buy)
		} else {
			// Evict the oldest entry (index 0) by shifting the slice left.
			copy(st.buyHistory, st.buyHistory[1:])
			st.buyHistory[len(st.buyHistory)-1] = buy
		}
	} else {
		st.sellSOL += ev.SOLAmount
		st.sellTrades++

		// Track net token balance and sell proceeds for Gate 2/5.
		if ev.TokenAmount > 0 {
			st.walletNetTokens[ev.WalletAddr] -= ev.TokenAmount
		}
		st.walletSellSOL[ev.WalletAddr] += ev.SOLAmount

		sell := timestampedSell{At: ev.BlockTime, Wallet: ev.WalletAddr, SOL: ev.SOLAmount, TokenAmount: ev.TokenAmount}
		if len(st.sellHistory) < MaxSellHistoryPerToken {
			st.sellHistory = append(st.sellHistory, sell)
		} else {
			copy(st.sellHistory, st.sellHistory[1:])
			st.sellHistory[len(st.sellHistory)-1] = sell
		}
	}

	// Derive last-observed price for MarketCapSOL estimation (Gates 1, 4).
	// Both buys and sells carry usable SOL/token price information.
	if ev.TokenAmount > 0 && ev.SOLAmount > 0 {
		st.lastPriceSOL = ev.SOLAmount / ev.TokenAmount
	}

	// Capture holder snapshots at 30m and 60m marks.
	ageMin := s.now().Sub(st.FirstSeenAt).Minutes()
	if !st.snapped30m && ageMin >= 30 {
		st.holdersAt30m = engine.CountHolders(st.walletNetTokens)
		st.snapped30m = true
	}
	if !st.snapped60m && ageMin >= 60 {
		st.holdersAt60m = engine.CountHolders(st.walletNetTokens)
		st.snapped60m = true
		// Recompute organic winner count now that 60m has elapsed.
		st.organicWinnerCount = countOrganicWinners(st)
	}

	return true
}

// countOrganicWinners counts wallets that:
//  1. Made their first buy more than 5 minutes after the token's FirstSeenAt.
//  2. Are not the deployer (approximation: not the first buyer seen).
//  3. Have realised > 50% profit: sellSOL / buySOL > 1.5.
func countOrganicWinners(st *tokenState) int {
	// Approximate deployer: any wallet whose first buy was in the first 30 seconds.
	deployerWindow := st.FirstSeenAt.Add(30 * time.Second)
	minute5 := st.FirstSeenAt.Add(5 * time.Minute)

	count := 0
	for wallet, firstBuy := range st.walletFirstBuyAt {
		// Must have bought after minute 5.
		if !firstBuy.After(minute5) {
			continue
		}
		// Must not appear to be deployer-linked (first buy within first 30s).
		if firstBuy.Before(deployerWindow) || firstBuy.Equal(deployerWindow) {
			continue
		}
		// Must have realised > 50% profit.
		bought := st.walletBuySOL[wallet]
		sold := st.walletSellSOL[wallet]
		if bought <= 0 {
			continue
		}
		returnPct := (sold - bought) / bought
		if returnPct > 0.50 {
			count++
		}
	}
	return count
}

// Snapshot returns a read-only derived view of a single token's current state.
// Returns false if the mint is not known to the store.
func (s *Store) Snapshot(mint string) (model.TokenSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.tokens[mint]
	if !ok {
		return model.TokenSnapshot{}, false
	}
	return deriveSnapshot(st, s.now()), true
}

// RecentTokens returns snapshots for all tokens with at least one event in the
// last window, sorted by LastEventAt descending (most recently active first).
func (s *Store) RecentTokens(window time.Duration) []model.TokenSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cutoff := s.now().Add(-window)
	out := make([]model.TokenSnapshot, 0)
	for _, st := range s.tokens {
		if st.LastEventAt.After(cutoff) {
			out = append(out, deriveSnapshot(st, s.now()))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastEventAt.After(out[j].LastEventAt)
	})
	return out
}

// PruneStale removes tokens whose LastEventAt is older than StaleDuration.
// Returns the number of tokens removed. Call this periodically (e.g. every 5 minutes).
func (s *Store) PruneStale() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.now().Add(-StaleDuration)
	n := 0
	for mint, st := range s.tokens {
		if st.LastEventAt.Before(cutoff) {
			delete(s.tokens, mint)
			n++
		}
	}
	return n
}

// UpdateDepth sets the real on-chain pool depth for mint.
// depthSOL must be >= 0; source should be rpc.LiquiditySourcePCVault.
// No-op when mint is not known to the store or depthSOL < 0.
// Safe for concurrent use; typically called from a goroutine spawned after Apply.
func (s *Store) UpdateDepth(mint string, depthSOL float64, source string) {
	if depthSOL < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.tokens[mint]
	if !ok {
		return
	}
	st.realPoolDepthSOL = depthSOL
	st.realDepthSource = source
}

// Len returns the number of tokens currently tracked.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tokens)
}

// Reset clears all token state and resets the signature deduplication ring.
// This is a destructive local-admin action; never call from production code paths.
// Only exposed when ENABLE_LOCAL_ADMIN=1.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = make(map[string]*tokenState)
	s.sigs = newSigRing(SigCacheCap)
}

// --- internal token state ---

type tokenState struct {
	Mint            string
	FirstSeenAt     time.Time
	LastEventAt     time.Time
	buyHistory      []timestampedBuy  // bounded at MaxBuyHistoryPerToken; arrival order
	sellHistory     []timestampedSell // bounded at MaxSellHistoryPerToken; arrival order
	buySOL          float64
	sellSOL         float64
	sellTrades      int
	totalEventCount int
	uniqueBuyers    map[string]struct{} // all wallets ever seen; unbounded

	// Per-wallet net token balance, derived from swap TokenAmount fields.
	// Used to compute Top10HolderPct and HolderCount.
	walletNetTokens map[string]float64

	// Holder snapshots captured at the 30m and 60m marks.
	holdersAt30m int
	holdersAt60m int
	snapped30m   bool
	snapped60m   bool

	// OrganicWinnerCount: wallets that bought after minute 5, are not the
	// deployer, and have realised > 50% profit on observed sell events.
	organicWinnerCount int

	// Per-wallet buy cost basis (SOL spent) for organic winner tracking.
	walletBuySOL map[string]float64
	// Per-wallet sell proceeds (SOL received).
	walletSellSOL map[string]float64
	// Per-wallet first buy time, to check "bought after minute 5".
	walletFirstBuyAt map[string]time.Time

	// lastPriceSOL is the most recently observed price (SOLAmount/TokenAmount).
	// Updated on every buy event where both fields are > 0.
	lastPriceSOL float64

	// realPoolDepthSOL is the on-chain WSOL reserve depth from a Raydium pc_vault query.
	// -1 = not yet available; >= 0 = verified depth. Updated by UpdateDepth.
	realPoolDepthSOL float64
	// realDepthSource is the evidence source label when realPoolDepthSOL >= 0.
	realDepthSource string
}

type timestampedBuy struct {
	At          time.Time
	Wallet      string
	SOL         float64
	TokenAmount float64
}

type timestampedSell struct {
	At          time.Time
	Wallet      string
	SOL         float64
	TokenAmount float64
}

// deriveSnapshot computes all TokenSnapshot fields from current tokenState.
// Must be called with at least a read lock held on the parent Store.
func deriveSnapshot(st *tokenState, now time.Time) model.TokenSnapshot {
	age := now.Sub(st.FirstSeenAt).Seconds()
	if age < 0 {
		age = 0
	}

	// Prefer verified on-chain depth; fall back to observed swap-flow proxy.
	var liq float64
	var liqSource string
	var liqReliable bool
	if st.realPoolDepthSOL >= 0 {
		liq = st.realPoolDepthSOL
		liqSource = st.realDepthSource
		liqReliable = true
	} else {
		liq = st.buySOL + st.sellSOL
		liqSource = "observed_swaps_proxy"
		liqReliable = false
	}

	marketCap, marketCapReason := derivedMarketCap(st)
	lastPriceReason := ""
	if st.lastPriceSOL <= 0 {
		lastPriceReason = "no priced swap observed yet"
	}

	return model.TokenSnapshot{
		Mint:              st.Mint,
		FirstSeenAt:       st.FirstSeenAt,
		LastEventAt:       st.LastEventAt,
		UniqueBuyerCount:  len(st.uniqueBuyers),
		TotalBuySOL:       st.buySOL,
		TotalSellSOL:      st.sellSOL,
		SellTradeCount:    st.sellTrades,
		TotalEventCount:   st.totalEventCount,
		BuyersLast1m:      buyersInWindow(st.buyHistory, now, time.Minute),
		BuyersLast5m:      buyersInWindow(st.buyHistory, now, 5*time.Minute),
		BuyerAcceleration: buyerAcceleration(st.buyHistory, now),
		AgeSeconds:        age,
		// Adversarial indicators
		TopWalletBuyShareLast5m: topWalletBuyShare(st.buyHistory, now, 5*time.Minute),
		WalletDiversityRatio:    walletDiversityRatio(st.buyHistory, now, 5*time.Minute),
		RepeatBuyerShare1m:      repeatBuyerShare(st.buyHistory, now),
		// Short-window SOL volume
		BuySolLast1m:  buySolInWindow(st.buyHistory, now, time.Minute),
		SellSolLast1m: sellSolInWindow(st.sellHistory, now, time.Minute),
		// Wallet lists for effective-buyer clustering (not serialised to JSON)
		UniqueWalletsLast1m: walletsInWindow(st.buyHistory, now, time.Minute),
		UniqueWalletsLast5m: walletsInWindow(st.buyHistory, now, 5*time.Minute),
		// 7-gate fields — RealPoolDepthSOL carries the raw sentinel (-1 when unavailable).
		LiquidityPoolSOL:        liq,
		RealPoolDepthSOL:        st.realPoolDepthSOL,
		LiquidityEvidenceSource: liqSource,
		LiquidityProxyReliable:  liqReliable,
		Volume24hSOL:       volume24h(st, now),
		Top10HolderPct:     engine.ComputeTop10HolderPct(st.walletNetTokens),
		HolderCount:        engine.CountHolders(st.walletNetTokens),
		OrganicWinnerCount: st.organicWinnerCount,
		HoldersAt30m:       st.holdersAt30m,
		HoldersAt60m:       st.holdersAt60m,
		// MarketCapSOL: derived from last observed price × total tokens held.
		// lastPriceSOL = SOLAmount/TokenAmount from the most recent buy with both fields > 0.
		// totalTokensHeld = sum of positive walletNetTokens values (lower-bound observable supply).
		// This proxy understates true MC (excludes tokens in pools or not yet traded), but
		// it is non-zero after the first buy and makes Gates 1 and 4 evaluable without an RPC.
		LastPriceSOL:    st.lastPriceSOL,
		LastPriceReason: lastPriceReason,
		MarketCapSOL:    marketCap,
		MarketCapReason: marketCapReason,
		ShadowFeatures:  shadowFeatureInputs(st, now),
	}
}

func shadowFeatureInputs(st *tokenState, now time.Time) model.ShadowFeatureInputs {
	if now.Before(st.FirstSeenAt.Add(35 * time.Minute)) {
		return model.ShadowFeatureInputs{}
	}
	if len(st.buyHistory) >= MaxBuyHistoryPerToken || len(st.sellHistory) >= MaxSellHistoryPerToken {
		return model.ShadowFeatureInputs{}
	}

	start := st.FirstSeenAt
	min5 := start.Add(5 * time.Minute)
	min35 := start.Add(35 * time.Minute)

	obs := model.ShadowFeatureInputs{
		BuySol0_35m:                buySolBetween(st.buyHistory, start, min35),
		HasBuySol0_35m:             true,
		SellSol0_35m:               sellSolBetween(st.sellHistory, start, min35),
		HasSellSol0_35m:            true,
		SellTradeCount5to35m:       sellTradeCountBetween(st.sellHistory, min5, min35),
		HasSellTradeCount5to35m:    true,
		SellUniqueTraders5to35m:    sellUniqueTradersBetween(st.sellHistory, min5, min35),
		HasSellUniqueTraders5to35m: true,
		WalletsThatExited:          walletsThatExitedBy(st.sellHistory, min35),
		HasWalletsThatExited:       true,
		WalletsGt25Pct:             walletsOverReturnPctBy(st.buyHistory, st.sellHistory, min35, 25),
		HasWalletsGt25Pct:          true,
		MedianRealizedReturnPct:    medianRealizedReturnPctBy(st.buyHistory, st.sellHistory, min35),
		HasMedianRealizedReturnPct: true,
	}
	if obs.WalletsThatExited > 0 {
		obs.WinnerExitRatio = float64(obs.WalletsGt25Pct) / float64(obs.WalletsThatExited)
		obs.HasWinnerExitRatio = true
	}
	return obs
}

func buySolBetween(history []timestampedBuy, start, end time.Time) float64 {
	total := 0.0
	for i := range history {
		if inWindowInclusive(history[i].At, start, end) {
			total += history[i].SOL
		}
	}
	return total
}

func sellSolBetween(history []timestampedSell, start, end time.Time) float64 {
	total := 0.0
	for i := range history {
		if inWindowInclusive(history[i].At, start, end) {
			total += history[i].SOL
		}
	}
	return total
}

func sellTradeCountBetween(history []timestampedSell, start, end time.Time) int {
	count := 0
	for i := range history {
		if inWindowInclusive(history[i].At, start, end) {
			count++
		}
	}
	return count
}

func sellUniqueTradersBetween(history []timestampedSell, start, end time.Time) int {
	seen := make(map[string]struct{})
	for i := range history {
		if inWindowInclusive(history[i].At, start, end) {
			seen[history[i].Wallet] = struct{}{}
		}
	}
	return len(seen)
}

func walletsThatExitedBy(history []timestampedSell, end time.Time) int {
	seen := make(map[string]struct{})
	for i := range history {
		if !history[i].At.After(end) {
			seen[history[i].Wallet] = struct{}{}
		}
	}
	return len(seen)
}

func walletsOverReturnPctBy(buys []timestampedBuy, sells []timestampedSell, end time.Time, threshold float64) int {
	returns := walletReturnPctBy(buys, sells, end)
	count := 0
	for _, pct := range returns {
		if pct >= threshold {
			count++
		}
	}
	return count
}

func medianRealizedReturnPctBy(buys []timestampedBuy, sells []timestampedSell, end time.Time) float64 {
	returns := make([]float64, 0, len(sells))
	for _, pct := range walletReturnPctBy(buys, sells, end) {
		returns = append(returns, pct)
	}
	if len(returns) == 0 {
		return 0
	}
	sort.Float64s(returns)
	n := len(returns)
	if n%2 == 0 {
		return (returns[n/2-1] + returns[n/2]) / 2
	}
	return returns[n/2]
}

func walletReturnPctBy(buys []timestampedBuy, sells []timestampedSell, end time.Time) map[string]float64 {
	buySOL := make(map[string]float64)
	sellSOL := make(map[string]float64)
	for i := range buys {
		if !buys[i].At.After(end) {
			buySOL[buys[i].Wallet] += buys[i].SOL
		}
	}
	for i := range sells {
		if !sells[i].At.After(end) {
			sellSOL[sells[i].Wallet] += sells[i].SOL
		}
	}
	returns := make(map[string]float64)
	for wallet, sold := range sellSOL {
		bought := buySOL[wallet]
		if bought <= 0 {
			continue
		}
		returns[wallet] = ((sold - bought) / bought) * 100
	}
	return returns
}

func inWindowInclusive(at, start, end time.Time) bool {
	return !at.Before(start) && !at.After(end)
}

// derivedMarketCap estimates market cap as lastPriceSOL × observable token supply.
// Observable supply = sum of positive walletNetTokens balances (tokens still held).
// This is a lower-bound proxy: it excludes tokens sitting in AMM pool reserves,
// but it is non-zero after the first buy event with token amount data, making
// Gates 1 (liq/MC) and 4 (vol/MC) evaluable without an external RPC call.
func derivedMarketCap(st *tokenState) (float64, string) {
	if st.lastPriceSOL <= 0 {
		return 0, "market cap unavailable: no priced swap observed yet"
	}
	totalHeld := 0.0
	for _, v := range st.walletNetTokens {
		if v > 0 {
			totalHeld += v
		}
	}
	if totalHeld <= 0 {
		return 0, "market cap unavailable: no positive token holder balance observed"
	}
	return st.lastPriceSOL * totalHeld, ""
}

// volume24h returns total volume (buy+sell) for the first 24h of the token's life.
// When the token is < 24h old, it equals total observed volume.
func volume24h(st *tokenState, now time.Time) float64 {
	cutoff24h := st.FirstSeenAt.Add(24 * time.Hour)
	if now.Before(cutoff24h) {
		// Token is < 24h old — all observed volume is within the window.
		return st.buySOL + st.sellSOL
	}
	// Token is > 24h old — sum only events within the first 24h.
	total := 0.0
	for _, b := range st.buyHistory {
		if b.At.Before(cutoff24h) {
			total += b.SOL
		}
	}
	for _, s := range st.sellHistory {
		if s.At.Before(cutoff24h) {
			total += s.SOL
		}
	}
	return total
}

// buyersInWindow counts distinct buyer wallets with events strictly after now-window.
// Scans the full buyHistory without early termination so that out-of-order delivery
// (e.g. Helius batch retries arriving newest-first) is handled correctly.
// History is capped at MaxBuyHistoryPerToken (500) so the full scan is O(500) worst case.
func buyersInWindow(history []timestampedBuy, now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	seen := make(map[string]struct{})
	for i := range history {
		if history[i].At.After(cutoff) {
			seen[history[i].Wallet] = struct{}{}
		}
	}
	return len(seen)
}

// walletsInWindow returns the slice of distinct buyer wallet addresses active in
// the given window. Used for effective-buyer clustering in Classify.
func walletsInWindow(history []timestampedBuy, now time.Time, window time.Duration) []string {
	cutoff := now.Add(-window)
	seen := make(map[string]struct{})
	for i := range history {
		if history[i].At.After(cutoff) {
			seen[history[i].Wallet] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	return out
}

// buySolInWindow returns the total SOL spent on buys within the window.
func buySolInWindow(history []timestampedBuy, now time.Time, window time.Duration) float64 {
	cutoff := now.Add(-window)
	total := 0.0
	for i := range history {
		if history[i].At.After(cutoff) {
			total += history[i].SOL
		}
	}
	return total
}

// sellSolInWindow returns the total SOL received from sells within the window.
func sellSolInWindow(history []timestampedSell, now time.Time, window time.Duration) float64 {
	cutoff := now.Add(-window)
	total := 0.0
	for i := range history {
		if history[i].At.After(cutoff) {
			total += history[i].SOL
		}
	}
	return total
}

// buyerAcceleration returns the ratio of buyers in the most recent 1m window to
// buyers in the prior 1m window (the 2m-to-1m interval).
//
// Windows:
//
//	recent: (now-1m, now]
//	prior:  (now-2m, now-1m]
//
// Returns 0 when the prior window has no buyers, preventing false positives on
// brand-new tokens where there is no baseline to compare against.
func buyerAcceleration(history []timestampedBuy, now time.Time) float64 {
	recent := buyersInWindow(history, now, time.Minute)

	outer := now.Add(-2 * time.Minute) // (now-2m, …
	inner := now.Add(-time.Minute)     // …, now-1m]

	prior := make(map[string]struct{})
	for _, b := range history {
		if b.At.After(outer) && !b.At.After(inner) {
			prior[b.Wallet] = struct{}{}
		}
	}
	if len(prior) == 0 {
		return 0
	}
	return float64(recent) / float64(len(prior))
}

// topWalletBuyShare returns the fraction of total buy SOL in the window that belongs
// to the single highest-volume buyer wallet. Returns 0 when there are no buys in window.
func topWalletBuyShare(history []timestampedBuy, now time.Time, window time.Duration) float64 {
	cutoff := now.Add(-window)
	walletSOL := make(map[string]float64)
	total := 0.0
	for i := range history {
		if history[i].At.After(cutoff) {
			walletSOL[history[i].Wallet] += history[i].SOL
			total += history[i].SOL
		}
	}
	if total == 0 {
		return 0
	}
	var max float64
	for _, v := range walletSOL {
		if v > max {
			max = v
		}
	}
	return max / total
}

// walletDiversityRatio returns unique_buyer_wallets / total_buy_events within the window.
// Returns 1.0 when there are no buy events (no signal either way — assume clean).
func walletDiversityRatio(history []timestampedBuy, now time.Time, window time.Duration) float64 {
	cutoff := now.Add(-window)
	seen := make(map[string]struct{})
	total := 0
	for i := range history {
		if history[i].At.After(cutoff) {
			seen[history[i].Wallet] = struct{}{}
			total++
		}
	}
	if total == 0 {
		return 1.0
	}
	return float64(len(seen)) / float64(total)
}

// repeatBuyerShare returns the fraction of last-1m unique buyers who also appeared
// in the prior 1m window (2m-to-1m). Returns 0 when the last 1m has no buyers.
func repeatBuyerShare(history []timestampedBuy, now time.Time) float64 {
	recent1m := now.Add(-time.Minute)
	prior2m := now.Add(-2 * time.Minute)

	recentSet := make(map[string]struct{})
	priorSet := make(map[string]struct{})
	for i := range history {
		b := &history[i]
		if b.At.After(recent1m) {
			recentSet[b.Wallet] = struct{}{}
		} else if b.At.After(prior2m) {
			priorSet[b.Wallet] = struct{}{}
		}
	}
	if len(recentSet) == 0 {
		return 0
	}
	overlap := 0
	for w := range recentSet {
		if _, ok := priorSet[w]; ok {
			overlap++
		}
	}
	return float64(overlap) / float64(len(recentSet))
}

// --- sigRing: bounded circular deduplication cache ---
//
// sigRing is a fixed-size circular ring backed by a pre-allocated []string.
// Insertions and evictions are O(1) with zero allocations after construction.
//
// The previous implementation used slice-front eviction (r.buf = r.buf[1:]),
// which causes the backing array to lose capacity on every overflow, triggering
// periodic O(n) reallocations by append.  The circular design avoids this.
//
// Capacity must be > 0; the caller (newStore) always passes SigCacheCap=10_000.

type sigRing struct {
	m   map[string]struct{}
	buf []string // fixed-size circular buffer; len == cap always after init
	pos int      // index of the next write slot (also the oldest slot when full)
	n   int      // number of entries currently held (0..cap)
	cap int
}

func newSigRing(cap int) *sigRing {
	return &sigRing{
		m:   make(map[string]struct{}, cap),
		buf: make([]string, cap), // pre-allocated; never grows
		cap: cap,
	}
}

func (r *sigRing) seen(sig string) bool {
	_, ok := r.m[sig]
	return ok
}

// add inserts sig into the ring.  If the ring is full, the oldest entry is
// evicted from the map before the new entry occupies its slot.  O(1).
func (r *sigRing) add(sig string) {
	if r.n == r.cap {
		// Ring is full: buf[pos] holds the oldest entry — evict it.
		delete(r.m, r.buf[r.pos])
	} else {
		r.n++
	}
	r.buf[r.pos] = sig
	r.m[sig] = struct{}{}
	r.pos = (r.pos + 1) % r.cap
}
