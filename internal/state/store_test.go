package state_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/state"
)

const testMint = "TESTMINT11111111111111111111111111111111111"

// epoch is a fixed reference time used throughout tests.
var epoch = time.Unix(1_000_000, 0)

func makeBuy(sig, wallet, mint string, t time.Time, sol float64) model.SwapEvent {
	return model.SwapEvent{
		Signature:   sig,
		BlockTime:   t,
		TokenMint:   mint,
		IsBuy:       true,
		WalletAddr:  wallet,
		SOLAmount:   sol,
		TokenAmount: 1,
	}
}

func makeSell(sig, wallet, mint string, t time.Time, sol float64) model.SwapEvent {
	return model.SwapEvent{
		Signature:   sig,
		BlockTime:   t,
		TokenMint:   mint,
		IsBuy:       false,
		WalletAddr:  wallet,
		SOLAmount:   sol,
		TokenAmount: 1,
	}
}

// ---- Deduplication ----

func TestStore_DeduplicatesEvents(t *testing.T) {
	s := state.New()

	ev := makeBuy("sig1", "wallet1", testMint, epoch, 1.0)
	s.Apply(ev)
	s.Apply(ev) // exact duplicate — must be ignored

	snap, ok := s.Snapshot(testMint)
	if !ok {
		t.Fatal("expected snapshot")
	}
	if snap.TotalBuySOL != 1.0 {
		t.Errorf("TotalBuySOL = %.4f, want 1.0 (duplicate must not double-count)", snap.TotalBuySOL)
	}
	if snap.UniqueBuyerCount != 1 {
		t.Errorf("UniqueBuyerCount = %d, want 1", snap.UniqueBuyerCount)
	}
}

func TestStore_DifferentSignatures_BothCounted(t *testing.T) {
	s := state.New()

	s.Apply(makeBuy("sig1", "wallet1", testMint, epoch, 1.0))
	s.Apply(makeBuy("sig2", "wallet1", testMint, epoch.Add(time.Second), 2.0))

	snap, _ := s.Snapshot(testMint)
	if snap.TotalBuySOL != 3.0 {
		t.Errorf("TotalBuySOL = %.4f, want 3.0 (two distinct sigs)", snap.TotalBuySOL)
	}
}

// ---- Unique buyer counting ----

func TestStore_UniqueBuyerCounting(t *testing.T) {
	s := state.New()

	s.Apply(makeBuy("sig1", "walletA", testMint, epoch, 1.0))
	s.Apply(makeBuy("sig2", "walletA", testMint, epoch.Add(time.Second), 1.0)) // same wallet
	s.Apply(makeBuy("sig3", "walletB", testMint, epoch.Add(2*time.Second), 1.0))

	snap, _ := s.Snapshot(testMint)
	if snap.UniqueBuyerCount != 2 {
		t.Errorf("UniqueBuyerCount = %d, want 2 (walletA counted once)", snap.UniqueBuyerCount)
	}
}

// ---- Sell tracking ----

func TestStore_SellTracking(t *testing.T) {
	s := state.New()

	s.Apply(makeBuy("sig1", "walletA", testMint, epoch, 2.0))
	s.Apply(makeSell("sig2", "walletA", testMint, epoch.Add(10*time.Second), 1.5))
	s.Apply(makeSell("sig3", "walletB", testMint, epoch.Add(20*time.Second), 0.5))

	snap, _ := s.Snapshot(testMint)
	if snap.TotalBuySOL != 2.0 {
		t.Errorf("TotalBuySOL = %.4f, want 2.0", snap.TotalBuySOL)
	}
	if snap.TotalSellSOL != 2.0 {
		t.Errorf("TotalSellSOL = %.4f, want 2.0", snap.TotalSellSOL)
	}
	if snap.SellTradeCount != 2 {
		t.Errorf("SellTradeCount = %d, want 2", snap.SellTradeCount)
	}
	// Sells do not add to unique buyer count
	if snap.UniqueBuyerCount != 1 {
		t.Errorf("UniqueBuyerCount = %d, want 1 (sells don't add buyers)", snap.UniqueBuyerCount)
	}
}

func TestStore_LastPriceUpdatedFromSell(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(model.SwapEvent{
		Signature:   "buy1",
		BlockTime:   now.Add(-10 * time.Second),
		TokenMint:   testMint,
		IsBuy:       true,
		WalletAddr:  "walletA",
		SOLAmount:   1.0,
		TokenAmount: 10.0,
	})
	s.Apply(model.SwapEvent{
		Signature:   "sell1",
		BlockTime:   now.Add(-5 * time.Second),
		TokenMint:   testMint,
		IsBuy:       false,
		WalletAddr:  "walletA",
		SOLAmount:   0.5,
		TokenAmount: 2.0,
	})

	snap, _ := s.Snapshot(testMint)
	if got := snap.LastPriceSOL; got != 0.25 {
		t.Fatalf("LastPriceSOL = %.6f, want 0.25", got)
	}
	if snap.MarketCapSOL <= 0 {
		t.Fatalf("MarketCapSOL = %.6f, want > 0", snap.MarketCapSOL)
	}
}

// ---- Stale token pruning ----

func TestStore_PrunesStaleTokens(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "w1", "FRESH_MINT", now.Add(-time.Minute), 1.0))
	s.Apply(makeBuy("sig2", "w2", "STALE_MINT", now.Add(-5*time.Hour), 1.0))

	if s.Len() != 2 {
		t.Fatalf("want 2 tokens before prune, got %d", s.Len())
	}

	pruned := s.PruneStale()
	if pruned != 1 {
		t.Errorf("PruneStale() = %d, want 1", pruned)
	}
	if s.Len() != 1 {
		t.Errorf("Len() = %d after prune, want 1", s.Len())
	}
	if _, ok := s.Snapshot("STALE_MINT"); ok {
		t.Error("stale token should have been removed")
	}
	if _, ok := s.Snapshot("FRESH_MINT"); !ok {
		t.Error("fresh token should still be present")
	}
}

func TestStore_PruneStale_NothingToRemove(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "w1", testMint, now.Add(-time.Minute), 1.0))

	if n := s.PruneStale(); n != 0 {
		t.Errorf("PruneStale() = %d, want 0 (token is fresh)", n)
	}
	if s.Len() != 1 {
		t.Errorf("Len() = %d, want 1", s.Len())
	}
}

// ---- Velocity windows ----

func TestStore_BuyersInWindow(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "walletOld", testMint, now.Add(-10*time.Minute), 1.0)) // outside 5m
	s.Apply(makeBuy("sig2", "wallet2m", testMint, now.Add(-2*time.Minute), 1.0))   // inside 5m, outside 1m
	s.Apply(makeBuy("sig3", "wallet30s", testMint, now.Add(-30*time.Second), 1.0)) // inside 1m
	s.Apply(makeBuy("sig4", "wallet10s", testMint, now.Add(-10*time.Second), 1.0)) // inside 1m

	snap, ok := s.Snapshot(testMint)
	if !ok {
		t.Fatal("expected snapshot")
	}
	if snap.BuyersLast1m != 2 {
		t.Errorf("BuyersLast1m = %d, want 2", snap.BuyersLast1m)
	}
	if snap.BuyersLast5m != 3 {
		t.Errorf("BuyersLast5m = %d, want 3 (2m, 30s, 10s)", snap.BuyersLast5m)
	}
}

func TestStore_BuyersInWindow_SameWalletMultipleBuys(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	// Same wallet buys three times within last 1m
	s.Apply(makeBuy("sig1", "walletA", testMint, now.Add(-50*time.Second), 1.0))
	s.Apply(makeBuy("sig2", "walletA", testMint, now.Add(-30*time.Second), 1.0))
	s.Apply(makeBuy("sig3", "walletA", testMint, now.Add(-10*time.Second), 1.0))
	s.Apply(makeBuy("sig4", "walletB", testMint, now.Add(-20*time.Second), 1.0))

	snap, _ := s.Snapshot(testMint)
	if snap.BuyersLast1m != 2 {
		t.Errorf("BuyersLast1m = %d, want 2 (walletA counted once)", snap.BuyersLast1m)
	}
}

// ---- Buyer acceleration ----

func TestStore_BuyerAcceleration_Accelerating(t *testing.T) {
	// prior window (now-2m, now-1m]: 1 buyer
	// recent window (now-1m, now]:   2 buyers
	// acceleration = 2 / 1 = 2.0
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "walletPrior", testMint, now.Add(-90*time.Second), 1.0))
	s.Apply(makeBuy("sig2", "walletR1", testMint, now.Add(-30*time.Second), 1.0))
	s.Apply(makeBuy("sig3", "walletR2", testMint, now.Add(-20*time.Second), 1.0))

	snap, _ := s.Snapshot(testMint)
	if snap.BuyerAcceleration != 2.0 {
		t.Errorf("BuyerAcceleration = %.4f, want 2.0 (2 recent / 1 prior)", snap.BuyerAcceleration)
	}
}

func TestStore_BuyerAcceleration_Decelerating(t *testing.T) {
	// prior: 3 buyers, recent: 1 buyer → acceleration = 1/3 ≈ 0.333
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "wp1", testMint, now.Add(-110*time.Second), 1.0))
	s.Apply(makeBuy("sig2", "wp2", testMint, now.Add(-100*time.Second), 1.0))
	s.Apply(makeBuy("sig3", "wp3", testMint, now.Add(-80*time.Second), 1.0))
	s.Apply(makeBuy("sig4", "wr1", testMint, now.Add(-30*time.Second), 1.0))

	snap, _ := s.Snapshot(testMint)
	want := 1.0 / 3.0
	if snap.BuyerAcceleration != want {
		t.Errorf("BuyerAcceleration = %.6f, want %.6f (1 recent / 3 prior)", snap.BuyerAcceleration, want)
	}
}

func TestStore_BuyerAcceleration_NoPriorWindow(t *testing.T) {
	// Only recent buyers — prior window is empty → acceleration must be 0 (not infinity)
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "walletR", testMint, now.Add(-30*time.Second), 1.0))

	snap, _ := s.Snapshot(testMint)
	if snap.BuyerAcceleration != 0 {
		t.Errorf("BuyerAcceleration = %.4f, want 0 (no prior buyers)", snap.BuyerAcceleration)
	}
}

// ---- Bounded buy history ----

func TestStore_BoundedBuyHistory(t *testing.T) {
	// Events are placed 100ms apart, spanning 60 seconds total.
	// The fixed clock sits at the end so every event falls within the 1m window.
	//
	// After applying MaxBuyHistoryPerToken+100 (600) events:
	//   - history retains only the most recent 500 (indices 100..599)
	//   - UniqueBuyerCount reflects all 600 wallets (separate map, unbounded)
	//   - BuyersLast1m = 500 (the 500 retained events are all within the window)
	const overflow = 100
	const total = state.MaxBuyHistoryPerToken + overflow // 600
	step := 100 * time.Millisecond

	// now is exactly one step past the last event, keeping all events in the 1m window.
	// span = total*step = 600*100ms = 60s; window cutoff = now-60s = base.
	// Events at base are NOT strictly after cutoff so are excluded from BuyersLast1m.
	// Events after base (indices 1..599) are strictly after cutoff.
	// Only indices 100..599 remain in history after overflow eviction.
	base := epoch
	now := base.Add(time.Duration(total) * step) // base + 60s
	s := state.NewWithClock(func() time.Time { return now })

	for i := 0; i < total; i++ {
		s.Apply(makeBuy(
			fmt.Sprintf("sig%d", i),
			fmt.Sprintf("wallet%d", i),
			testMint,
			base.Add(time.Duration(i)*step),
			1.0,
		))
	}

	snap, ok := s.Snapshot(testMint)
	if !ok {
		t.Fatal("expected snapshot")
	}

	if snap.TotalBuySOL != float64(total) {
		t.Errorf("TotalBuySOL = %.2f, want %.2f", snap.TotalBuySOL, float64(total))
	}
	if snap.UniqueBuyerCount != total {
		t.Errorf("UniqueBuyerCount = %d, want %d (unbounded map tracks all wallets)", snap.UniqueBuyerCount, total)
	}
	// BuyersLast1m counts wallets in surviving history that are strictly after the cutoff.
	// Surviving history: indices 100..599 (after eviction of 0..99).
	// Cutoff = now-1m = base+60s-60s = base. Events at base+10ms..base+59.9s are all after base.
	if snap.BuyersLast1m != state.MaxBuyHistoryPerToken {
		t.Errorf("BuyersLast1m = %d, want %d (oldest %d events evicted from history)",
			snap.BuyersLast1m, state.MaxBuyHistoryPerToken, overflow)
	}
}

// ---- Out-of-order delivery ----

func TestStore_OutOfOrderDelivery_VelocityCorrect(t *testing.T) {
	// Events arrive newest-first (out of slot order), as can happen during
	// Helius batch retries or multi-program route deliveries.
	// Both events fall within the 1m window; both wallets must be counted.
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	// Newer event arrives first (t = -10s), older arrives second (t = -50s).
	// After Apply both are inside the 1m window → BuyersLast1m must be 2.
	s.Apply(makeBuy("sig_newer", "walletNewer", testMint, now.Add(-10*time.Second), 1.0))
	s.Apply(makeBuy("sig_older", "walletOlder", testMint, now.Add(-50*time.Second), 1.0))

	snap, _ := s.Snapshot(testMint)
	if snap.BuyersLast1m != 2 {
		t.Errorf("BuyersLast1m = %d, want 2 (out-of-order delivery must not under-count)",
			snap.BuyersLast1m)
	}
}

func TestStore_OutOfOrderDelivery_OneBuyOutsideWindow(t *testing.T) {
	// Newer event (-10s) is inside the 1m window; older event (-90s) is outside.
	// Even though the older event was applied second, it must not be counted in BuyersLast1m.
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig_newer", "walletNewer", testMint, now.Add(-10*time.Second), 1.0))
	s.Apply(makeBuy("sig_older", "walletOlder", testMint, now.Add(-90*time.Second), 1.0))

	snap, _ := s.Snapshot(testMint)
	if snap.BuyersLast1m != 1 {
		t.Errorf("BuyersLast1m = %d, want 1 (walletOlder at -90s is outside 1m window)",
			snap.BuyersLast1m)
	}
	if snap.BuyersLast5m != 2 {
		t.Errorf("BuyersLast5m = %d, want 2 (both events are inside 5m window)", snap.BuyersLast5m)
	}
}

// ---- Signature cache bounded ----

func TestStore_SignatureCacheBounded(t *testing.T) {
	// Apply one event (EARLY_SIG), then overflow the sig cache with SigCacheCap new sigs,
	// then re-apply EARLY_SIG. Because EARLY_SIG was evicted from the ring, it should
	// be re-processed (not deduplicated), increasing TotalBuySOL by the second SOL amount.
	s := state.New()

	s.Apply(makeBuy("EARLY_SIG", "walletE", testMint, epoch, 5.0))

	for i := 0; i < state.SigCacheCap; i++ {
		s.Apply(makeBuy(
			fmt.Sprintf("fill%d", i),
			fmt.Sprintf("walletF%d", i),
			testMint,
			epoch.Add(time.Duration(i+1)*time.Millisecond),
			1.0,
		))
	}

	// EARLY_SIG is now evicted — applying it again must re-process it.
	s.Apply(makeBuy("EARLY_SIG", "walletE2", testMint, epoch.Add(time.Hour), 7.0))

	snap, ok := s.Snapshot(testMint)
	if !ok {
		t.Fatal("expected snapshot")
	}

	// Expected: 5.0 (first EARLY) + SigCacheCap*1.0 (fill) + 7.0 (second EARLY)
	want := 5.0 + float64(state.SigCacheCap)*1.0 + 7.0
	if snap.TotalBuySOL != want {
		t.Errorf("TotalBuySOL = %.4f, want %.4f (EARLY_SIG re-processed after eviction)",
			snap.TotalBuySOL, want)
	}
}

// ---- RecentTokens ----

func TestStore_RecentTokens(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "w1", "ACTIVE1", now.Add(-5*time.Minute), 1.0))
	s.Apply(makeBuy("sig2", "w2", "ACTIVE2", now.Add(-10*time.Minute), 1.0))
	s.Apply(makeBuy("sig3", "w3", "OLD_MINT", now.Add(-2*time.Hour), 1.0))

	recent := s.RecentTokens(30 * time.Minute)
	if len(recent) != 2 {
		t.Fatalf("RecentTokens(30m) = %d tokens, want 2", len(recent))
	}
	// Most recently active first
	if recent[0].Mint != "ACTIVE1" {
		t.Errorf("first = %q, want ACTIVE1 (most recent)", recent[0].Mint)
	}
	if recent[1].Mint != "ACTIVE2" {
		t.Errorf("second = %q, want ACTIVE2", recent[1].Mint)
	}
}

func TestStore_RecentTokens_Empty(t *testing.T) {
	s := state.New()
	recent := s.RecentTokens(30 * time.Minute)
	if len(recent) != 0 {
		t.Errorf("RecentTokens on empty store = %d, want 0", len(recent))
	}
}

// ---- Snapshot for unknown mint ----

func TestStore_Snapshot_UnknownMint(t *testing.T) {
	s := state.New()
	_, ok := s.Snapshot("UNKNOWN_MINT")
	if ok {
		t.Error("Snapshot of unknown mint should return false")
	}
}

// ---- Age calculation ----

func TestStore_AgeSeconds(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "w1", testMint, now.Add(-90*time.Second), 1.0))

	snap, _ := s.Snapshot(testMint)
	if snap.AgeSeconds != 90 {
		t.Errorf("AgeSeconds = %.2f, want 90", snap.AgeSeconds)
	}
}

// ---- Adversarial indicators ----

func TestStore_TopWalletBuyShare_SingleWallet(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// One wallet buys all volume → share = 1.0
	s.Apply(makeBuy("s1", "w1", testMint, now.Add(-1*time.Minute), 10.0))
	s.Apply(makeBuy("s2", "w1", testMint, now.Add(-2*time.Minute), 10.0))
	snap, _ := s.Snapshot(testMint)
	if snap.TopWalletBuyShareLast5m != 1.0 {
		t.Errorf("TopWalletBuyShareLast5m = %.4f, want 1.0", snap.TopWalletBuyShareLast5m)
	}
}

func TestStore_TopWalletBuyShare_EqualWallets(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// Two wallets with equal SOL → top share = 0.5
	s.Apply(makeBuy("s1", "w1", testMint, now.Add(-1*time.Minute), 5.0))
	s.Apply(makeBuy("s2", "w2", testMint, now.Add(-2*time.Minute), 5.0))
	snap, _ := s.Snapshot(testMint)
	if snap.TopWalletBuyShareLast5m != 0.5 {
		t.Errorf("TopWalletBuyShareLast5m = %.4f, want 0.5", snap.TopWalletBuyShareLast5m)
	}
}

func TestStore_TopWalletBuyShare_OutsideWindow(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// Buy outside 5m window → no activity in window → share = 0
	s.Apply(makeBuy("s1", "w1", testMint, now.Add(-6*time.Minute), 100.0))
	snap, _ := s.Snapshot(testMint)
	if snap.TopWalletBuyShareLast5m != 0.0 {
		t.Errorf("TopWalletBuyShareLast5m = %.4f, want 0.0 (all buys outside window)", snap.TopWalletBuyShareLast5m)
	}
}

func TestStore_WalletDiversityRatio_AllUnique(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// 3 buys from 3 different wallets → ratio = 1.0
	s.Apply(makeBuy("s1", "w1", testMint, now.Add(-1*time.Minute), 1.0))
	s.Apply(makeBuy("s2", "w2", testMint, now.Add(-2*time.Minute), 1.0))
	s.Apply(makeBuy("s3", "w3", testMint, now.Add(-3*time.Minute), 1.0))
	snap, _ := s.Snapshot(testMint)
	if snap.WalletDiversityRatio != 1.0 {
		t.Errorf("WalletDiversityRatio = %.4f, want 1.0", snap.WalletDiversityRatio)
	}
}

func TestStore_WalletDiversityRatio_LowDiversity(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// 1 wallet makes 4 buys → ratio = 1/4 = 0.25
	for i := 0; i < 4; i++ {
		s.Apply(makeBuy(fmt.Sprintf("s%d", i), "w1", testMint, now.Add(-time.Duration(i+1)*time.Minute/2), 1.0))
	}
	snap, _ := s.Snapshot(testMint)
	want := 0.25
	if snap.WalletDiversityRatio != want {
		t.Errorf("WalletDiversityRatio = %.4f, want %.4f", snap.WalletDiversityRatio, want)
	}
}

func TestStore_WalletDiversityRatio_NoActivity(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// No buys → ratio = 1.0 (clean default)
	s.Apply(makeBuy("s1", "w1", testMint, now.Add(-6*time.Minute), 1.0)) // outside window
	snap, _ := s.Snapshot(testMint)
	if snap.WalletDiversityRatio != 1.0 {
		t.Errorf("WalletDiversityRatio = %.4f, want 1.0 (no activity in window)", snap.WalletDiversityRatio)
	}
}

func TestStore_RepeatBuyerShare_NoOverlap(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// w1 only in last 1m, w2 only in prior 1m → repeat share = 0
	s.Apply(makeBuy("s1", "w1", testMint, now.Add(-30*time.Second), 1.0))
	s.Apply(makeBuy("s2", "w2", testMint, now.Add(-90*time.Second), 1.0))
	snap, _ := s.Snapshot(testMint)
	if snap.RepeatBuyerShare1m != 0.0 {
		t.Errorf("RepeatBuyerShare1m = %.4f, want 0.0", snap.RepeatBuyerShare1m)
	}
}

func TestStore_RepeatBuyerShare_FullOverlap(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// w1 appears in both windows → repeat share = 1.0
	s.Apply(makeBuy("s1", "w1", testMint, now.Add(-30*time.Second), 1.0))
	s.Apply(makeBuy("s2", "w1", testMint, now.Add(-90*time.Second), 1.0))
	snap, _ := s.Snapshot(testMint)
	if snap.RepeatBuyerShare1m != 1.0 {
		t.Errorf("RepeatBuyerShare1m = %.4f, want 1.0", snap.RepeatBuyerShare1m)
	}
}

func TestStore_RepeatBuyerShare_EmptyRecent(t *testing.T) {
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })
	// Only activity in prior window → no recent buyers → share = 0
	s.Apply(makeBuy("s1", "w1", testMint, now.Add(-90*time.Second), 1.0))
	snap, _ := s.Snapshot(testMint)
	if snap.RepeatBuyerShare1m != 0.0 {
		t.Errorf("RepeatBuyerShare1m = %.4f, want 0.0 (no recent buyers)", snap.RepeatBuyerShare1m)
	}
}

// ---- Apply return values ----

func TestStore_Apply_ReturnsTrueOnNewEvent(t *testing.T) {
	s := state.New()
	stored := s.Apply(makeBuy("sig1", "w1", testMint, epoch, 1.0))
	if !stored {
		t.Error("Apply should return true for a new event")
	}
}

func TestStore_Apply_ReturnsFalseOnDuplicate(t *testing.T) {
	s := state.New()
	ev := makeBuy("sig1", "w1", testMint, epoch, 1.0)
	s.Apply(ev)
	stored := s.Apply(ev)
	if stored {
		t.Error("Apply should return false for a duplicate signature")
	}
}

func TestStore_Apply_EmptySigAlwaysStored(t *testing.T) {
	// Empty signature bypasses deduplication — both events must be applied.
	s := state.New()
	ev := makeBuy("", "w1", testMint, epoch, 1.0)
	r1 := s.Apply(ev)
	r2 := s.Apply(ev)
	if !r1 || !r2 {
		t.Errorf("Apply(empty sig) = %v, %v; both must return true (no dedup)", r1, r2)
	}
	snap, _ := s.Snapshot(testMint)
	if snap.TotalBuySOL != 2.0 {
		t.Errorf("TotalBuySOL = %.4f, want 2.0 (empty-sig events are not deduplicated)", snap.TotalBuySOL)
	}
}

// ---- FirstSeenAt under out-of-order delivery ----

func TestStore_FirstSeenAt_UpdatedOnOlderEvent(t *testing.T) {
	// First event arrives at t=-30s; a later-delivered event has block time t=-90s.
	// FirstSeenAt must reflect the oldest block time (-90s), not arrival order.
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "w1", testMint, now.Add(-30*time.Second), 1.0))
	s.Apply(makeBuy("sig2", "w2", testMint, now.Add(-90*time.Second), 1.0)) // older block time arrives second

	snap, _ := s.Snapshot(testMint)
	wantAge := 90.0
	if snap.AgeSeconds != wantAge {
		t.Errorf("AgeSeconds = %.2f, want %.2f (FirstSeenAt must track minimum block time)", snap.AgeSeconds, wantAge)
	}
}

func TestStore_FirstSeenAt_NotUpdatedByNewerEvent(t *testing.T) {
	// Events arrive in order; FirstSeenAt stays at the first block time.
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	s.Apply(makeBuy("sig1", "w1", testMint, now.Add(-90*time.Second), 1.0))
	s.Apply(makeBuy("sig2", "w2", testMint, now.Add(-30*time.Second), 1.0)) // newer block time

	snap, _ := s.Snapshot(testMint)
	wantAge := 90.0
	if snap.AgeSeconds != wantAge {
		t.Errorf("AgeSeconds = %.2f, want %.2f (newer event must not reduce age)", snap.AgeSeconds, wantAge)
	}
}

// ---- Prune boundary ----

func TestStore_PruneStale_ExactBoundary(t *testing.T) {
	// A token whose LastEventAt == cutoff (not strictly before) must NOT be pruned.
	// PruneStale uses Before(cutoff), so equal means keep.
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	cutoff := now.Add(-state.StaleDuration)
	// Token exactly at the cutoff boundary — should NOT be pruned.
	s.Apply(makeBuy("sig1", "w1", "BOUNDARY", cutoff, 1.0))
	// Token 1ms before cutoff — SHOULD be pruned.
	s.Apply(makeBuy("sig2", "w2", "STALE", cutoff.Add(-time.Millisecond), 1.0))

	pruned := s.PruneStale()
	if pruned != 1 {
		t.Errorf("PruneStale() = %d, want 1 (only the token before the cutoff)", pruned)
	}
	if _, ok := s.Snapshot("BOUNDARY"); !ok {
		t.Error("BOUNDARY token must survive (LastEventAt == cutoff, not before)")
	}
	if _, ok := s.Snapshot("STALE"); ok {
		t.Error("STALE token must be pruned")
	}
}

// ---- sigRing: circular eviction does not corrupt map ----

func TestStore_SigRing_CircularEvictionConsistency(t *testing.T) {
	// Run 3 full cycles through the ring (3 * SigCacheCap unique sigs) and verify
	// that the ring correctly identifies recent duplicates and re-processes evicted ones.
	s := state.New()
	const cycles = 3

	for cycle := 0; cycle < cycles; cycle++ {
		for i := 0; i < state.SigCacheCap; i++ {
			sig := fmt.Sprintf("c%d-sig%d", cycle, i)
			stored := s.Apply(makeBuy(sig, fmt.Sprintf("w%d-%d", cycle, i), testMint,
				epoch.Add(time.Duration(cycle*state.SigCacheCap+i)*time.Millisecond), 1.0))
			if !stored {
				t.Fatalf("cycle %d sig %d: Apply returned false for a new signature", cycle, i)
			}
		}
	}
	snap, _ := s.Snapshot(testMint)
	want := float64(cycles * state.SigCacheCap)
	if snap.TotalBuySOL != want {
		t.Errorf("TotalBuySOL = %.0f, want %.0f after %d ring cycles", snap.TotalBuySOL, want, cycles)
	}
}

func TestStore_SigRing_DuplicateWithinCurrentCycle(t *testing.T) {
	// A signature added in the current ring cycle must be deduplicated.
	s := state.New()

	s.Apply(makeBuy("TARGET", "w1", testMint, epoch, 5.0))
	// Fill 99 more slots (ring not yet full).
	for i := 0; i < 99; i++ {
		s.Apply(makeBuy(fmt.Sprintf("other%d", i), fmt.Sprintf("w%d", i), testMint,
			epoch.Add(time.Duration(i+1)*time.Millisecond), 1.0))
	}
	// TARGET is still in the ring — must be rejected.
	stored := s.Apply(makeBuy("TARGET", "w2", testMint, epoch.Add(200*time.Millisecond), 10.0))
	if stored {
		t.Error("Apply should return false: TARGET is still in the ring")
	}
	snap, _ := s.Snapshot(testMint)
	// Expected: 5.0 (TARGET) + 99*1.0 = 104.0; the duplicate 10.0 must NOT be counted.
	if snap.TotalBuySOL != 104.0 {
		t.Errorf("TotalBuySOL = %.4f, want 104.0", snap.TotalBuySOL)
	}
}

// ---- Concurrent access safety ----

func TestStore_ConcurrentApplyAndSnapshot(t *testing.T) {
	// Hammer Apply + Snapshot from multiple goroutines to surface data races.
	// Run with -race to get full value from this test.
	s := state.New()
	const goroutines = 8
	const eventsEach = 200

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < eventsEach; i++ {
				sig := fmt.Sprintf("g%d-sig%d", gid, i)
				wallet := fmt.Sprintf("g%d-wallet%d", gid, i)
				s.Apply(makeBuy(sig, wallet, testMint,
					epoch.Add(time.Duration(gid*eventsEach+i)*time.Millisecond), 1.0))
				// Interleave reads.
				if i%10 == 0 {
					s.Snapshot(testMint)
				}
			}
		}(g)
	}
	wg.Wait()

	snap, ok := s.Snapshot(testMint)
	if !ok {
		t.Fatal("expected snapshot after concurrent writes")
	}
	// All events must have been counted (no concurrent corruption).
	want := float64(goroutines * eventsEach)
	if snap.TotalBuySOL != want {
		t.Errorf("TotalBuySOL = %.0f, want %.0f", snap.TotalBuySOL, want)
	}
}

func TestStore_ConcurrentPruneAndSnapshot(t *testing.T) {
	// PruneStale (write lock) racing against RecentTokens (read lock) must not panic.
	now := epoch
	s := state.NewWithClock(func() time.Time { return now })

	// Seed some tokens.
	for i := 0; i < 20; i++ {
		s.Apply(makeBuy(fmt.Sprintf("sig%d", i), "w1",
			fmt.Sprintf("MINT%d", i), now.Add(-time.Duration(i)*time.Minute), 1.0))
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			s.PruneStale()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			s.RecentTokens(30 * time.Minute)
		}
	}()
	wg.Wait()
}

// ---- real_pool_depth_sol contract ----

// TestSnapshot_RealPoolDepthSOL_DefaultSentinel verifies that a fresh token
// snapshot always emits real_pool_depth_sol = -1 when no depth has been fetched.
func TestSnapshot_RealPoolDepthSOL_DefaultSentinel(t *testing.T) {
	s := state.New()
	s.Apply(makeBuy("sig1", "w1", testMint, epoch, 1.0))

	snap, ok := s.Snapshot(testMint)
	if !ok {
		t.Fatal("snapshot not found")
	}
	if snap.RealPoolDepthSOL != -1 {
		t.Errorf("RealPoolDepthSOL = %v, want -1 (sentinel for unavailable)", snap.RealPoolDepthSOL)
	}
	if snap.LiquidityEvidenceSource != "observed_swaps_proxy" {
		t.Errorf("LiquidityEvidenceSource = %q, want %q", snap.LiquidityEvidenceSource, "observed_swaps_proxy")
	}
	if snap.LiquidityProxyReliable {
		t.Errorf("LiquidityProxyReliable = true, want false for observed proxy")
	}
}

// TestSnapshot_RealPoolDepthSOL_UpdateDepth verifies that UpdateDepth overwrites
// the sentinel and sets evidence source to raydium_pc_vault with reliable=true.
func TestSnapshot_RealPoolDepthSOL_UpdateDepth(t *testing.T) {
	s := state.New()
	s.Apply(makeBuy("sig1", "w1", testMint, epoch, 1.0))

	s.UpdateDepth(testMint, 75.5, "raydium_pc_vault")

	snap, ok := s.Snapshot(testMint)
	if !ok {
		t.Fatal("snapshot not found")
	}
	if snap.RealPoolDepthSOL != 75.5 {
		t.Errorf("RealPoolDepthSOL = %v, want 75.5", snap.RealPoolDepthSOL)
	}
	if snap.LiquidityEvidenceSource != "raydium_pc_vault" {
		t.Errorf("LiquidityEvidenceSource = %q, want %q", snap.LiquidityEvidenceSource, "raydium_pc_vault")
	}
	if !snap.LiquidityProxyReliable {
		t.Errorf("LiquidityProxyReliable = false, want true when raydium_pc_vault depth is set")
	}
	if snap.LiquidityPoolSOL != 75.5 {
		t.Errorf("LiquidityPoolSOL = %v, want 75.5 (real depth overrides proxy)", snap.LiquidityPoolSOL)
	}
}

// TestSnapshot_RealPoolDepthSOL_JSONPresent verifies the field appears in JSON
// with value -1 (not omitted) for fallback rows.
func TestSnapshot_RealPoolDepthSOL_JSONPresent(t *testing.T) {
	s := state.New()
	s.Apply(makeBuy("sig1", "w1", testMint, epoch, 1.0))
	snap, _ := s.Snapshot(testMint)

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	v, present := m["real_pool_depth_sol"]
	if !present {
		t.Fatalf("real_pool_depth_sol missing from JSON (must not be omitempty)")
	}
	if v.(float64) != -1 {
		t.Errorf("real_pool_depth_sol JSON value = %v, want -1", v)
	}
}
