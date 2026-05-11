package proxy_test

import (
	"fmt"
	"testing"
	"time"

	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/proxy"
)

func TestAuthenticityCleanLaunchRunner(t *testing.T) {
	row := authBaseRow()
	row.TradeHistory = variedBuys(row.FirstSeenAt, row.FirstSeenSlot, 8, 1.2)
	proxy.ApplyAuthenticityEvidence(&row)
	got := proxy.ScoreEarlyProxy(row)
	if got.Band != "RUNNER" {
		t.Fatalf("band=%q score=%.1f risks=%v auth=%s, want RUNNER", got.Band, got.Score, got.RiskFlags, row.AuthenticityLabel)
	}
	if row.AuthenticityLabel != "organic" {
		t.Fatalf("auth=%q flags=%v, want organic", row.AuthenticityLabel, row.BotFlags)
	}
}

func TestAuthenticityApproxBundleBotRejects(t *testing.T) {
	row := authBaseRow()
	row.TradeHistory = []model.TokenTradeEvent{
		trade(row, 0, 0, "creator", "buy", 1, 100),
		trade(row, 0, 0, "b1", "buy", 1, 100),
		trade(row, 0, 0, "b2", "buy", 1.1, 110),
		trade(row, 10, 3, "b3", "buy", 1.2, 120),
	}
	proxy.ApplyAuthenticityEvidence(&row)
	got := proxy.ScoreEarlyProxy(row)
	if !row.BundleBotDetected || row.BundleBotConfidence != "approximate" {
		t.Fatalf("bundle=%v confidence=%q flags=%v", row.BundleBotDetected, row.BundleBotConfidence, row.BotFlags)
	}
	if got.Band == "RUNNER" {
		t.Fatalf("band=%q risks=%v, bundle bot must not remain RUNNER", got.Band, got.RiskFlags)
	}
}

func TestAuthenticitySniperHeavyDowngrades(t *testing.T) {
	row := authBaseRow()
	row.Top10HolderPct = 0.90
	row.TradeHistory = []model.TokenTradeEvent{
		trade(row, 0, 0, "creator", "buy", 0.1, 10),
		trade(row, 1, 1, "s1", "buy", 4, 400),
		trade(row, 2, 2, "s2", "buy", 3, 300),
		trade(row, 3, 3, "s3", "buy", 3, 300),
	}
	proxy.ApplyAuthenticityEvidence(&row)
	got := proxy.ScoreEarlyProxy(row)
	if !row.SniperBotDetected || row.SniperShareEarlyBuySOL < 0.70 {
		t.Fatalf("sniper=%v share=%.2f flags=%v", row.SniperBotDetected, row.SniperShareEarlyBuySOL, row.BotFlags)
	}
	if got.Band == "RUNNER" {
		t.Fatalf("band=%q risks=%v, high sniper share + concentration must downgrade", got.Band, got.RiskFlags)
	}
}

func TestAuthenticityBumpBotAvoids(t *testing.T) {
	row := authBaseRow()
	row.TradeHistory = []model.TokenTradeEvent{
		trade(row, 0, 0, "w1", "buy", 1, 100),
		trade(row, 5, 1, "w1", "sell", 1, 100),
		trade(row, 10, 2, "w1", "buy", 1, 100),
		trade(row, 15, 3, "w1", "sell", 1, 100),
		trade(row, 20, 4, "w1", "buy", 1, 100),
		trade(row, 25, 5, "w1", "sell", 1, 100),
	}
	proxy.ApplyAuthenticityEvidence(&row)
	got := proxy.ScoreEarlyProxy(row)
	if !row.BumpBotDetected || row.BumpBotScore < 50 {
		t.Fatalf("bump=%v score=%.2f flags=%v", row.BumpBotDetected, row.BumpBotScore, row.BotFlags)
	}
	if got.Band == "RUNNER" {
		t.Fatalf("band=%q risks=%v, bump bot must not remain RUNNER", got.Band, got.RiskFlags)
	}
}

func TestAuthenticityBananaStyleMechanicalOldTokenNotRunner(t *testing.T) {
	row := authBaseRow()
	row.AgeSeconds = 3600
	row.LaunchEvidenceSource = ""
	row.TradeHistory = nil
	for i := 0; i < 8; i++ {
		row.TradeHistory = append(row.TradeHistory, trade(row, i*60, uint64(i+10), fmt.Sprintf("w%d", i%2), "buy", 0.42, 42))
	}
	proxy.ApplyAuthenticityEvidence(&row)
	got := proxy.ScoreEarlyProxy(row)
	if row.AuthenticityLabel != "mechanical" && row.AuthenticityLabel != "bot_like" {
		t.Fatalf("auth=%q flags=%v, want mechanical/bot_like", row.AuthenticityLabel, row.BotFlags)
	}
	if got.Band == "RUNNER" {
		t.Fatalf("band=%q risks=%v, BANANA-style mechanical token must not remain RUNNER", got.Band, got.RiskFlags)
	}
	if row.SignalMode == "launch_bonding" {
		t.Fatalf("mode=%q, old token must not be launch_bonding", row.SignalMode)
	}
}

func TestAuthenticityOrganicRevivalCanBeRunnerSubtype(t *testing.T) {
	row := authBaseRow()
	row.AgeSeconds = 3600
	row.LaunchEvidenceSource = ""
	row.TradeHistory = variedBuys(row.FirstSeenAt.Add(55*time.Minute), row.FirstSeenSlot+1000, 8, 1.1)
	proxy.ApplyAuthenticityEvidence(&row)
	if row.RunnerSubtype != "AMM_MOMENTUM_RUNNER" && row.RunnerSubtype != "REVIVAL_RUNNER" {
		t.Fatalf("subtype=%q mode=%q", row.RunnerSubtype, row.SignalMode)
	}
	if row.AuthenticityLabel != "organic" {
		t.Fatalf("auth=%q flags=%v, want organic", row.AuthenticityLabel, row.BotFlags)
	}
}

func TestAuthenticityRawVelocityBotContaminatedNoRunner(t *testing.T) {
	row := authBaseRow()
	row.TradeHistory = []model.TokenTradeEvent{
		trade(row, 0, 0, "bot", "buy", 5, 500),
		trade(row, 5, 1, "bot", "sell", 5, 500),
		trade(row, 10, 2, "bot", "buy", 5, 500),
		trade(row, 15, 3, "bot", "sell", 5, 500),
		trade(row, 20, 4, "bot", "buy", 5, 500),
		trade(row, 25, 5, "bot", "sell", 5, 500),
	}
	proxy.ApplyAuthenticityEvidence(&row)
	got := proxy.ScoreEarlyProxy(row)
	if row.RawLiquidityVelocity <= row.OrganicLiquidityVelocity {
		t.Fatalf("raw=%.2f organic=%.2f, want contaminated raw velocity", row.RawLiquidityVelocity, row.OrganicLiquidityVelocity)
	}
	if got.Band == "RUNNER" {
		t.Fatalf("band=%q risks=%v, contaminated velocity must not uplift to RUNNER", got.Band, got.RiskFlags)
	}
}

func TestAuthenticityHighOrganicVelocityUpliftAllowed(t *testing.T) {
	row := authBaseRow()
	row.TradeHistory = variedBuys(row.FirstSeenAt, row.FirstSeenSlot, 6, 2.2)
	proxy.ApplyAuthenticityEvidence(&row)
	got := proxy.ScoreEarlyProxy(row)
	if row.LiquidityVelocityLabel != "strong" && row.LiquidityVelocityLabel != "exceptional" {
		t.Fatalf("velocity=%q organic=%.2f", row.LiquidityVelocityLabel, row.OrganicLiquidityVelocity)
	}
	if !contains(got.Reasons, "strong organic liquidity velocity") {
		t.Fatalf("reasons=%v, want velocity reason", got.Reasons)
	}
}

func TestAuthenticityFullFallbackNoRunner(t *testing.T) {
	row := authBaseRow()
	row.ClusteringRowStatus = "full_fallback"
	row.TradeHistory = variedBuys(row.FirstSeenAt, row.FirstSeenSlot, 8, 1.2)
	proxy.ApplyAuthenticityEvidence(&row)
	got := proxy.ScoreEarlyProxy(row)
	if got.Band == "RUNNER" {
		t.Fatalf("band=%q risks=%v, full fallback must not remain RUNNER", got.Band, got.RiskFlags)
	}
}

func authBaseRow() model.LiveSnapshot {
	now := time.Unix(1_777_600_000, 0).UTC()
	row := model.LiveSnapshot{
		TokenSnapshot: model.TokenSnapshot{
			Mint:                    "AUTHMINT",
			FirstSeenAt:             now,
			LastEventAt:             now.Add(4 * time.Minute),
			FirstSeenSlot:           1000,
			CreatorWallet:           "creator",
			BuyersLast1m:            4,
			BuyersLast5m:            8,
			BuySolLast1m:            4,
			HolderCount:             28,
			MarketCapSOL:            35,
			Top10HolderPct:          0.35,
			TotalEventCount:         8,
			RealPoolDepthSOL:        25,
			LiquidityPoolSOL:        25,
			LaunchEvidenceSource:    "pump_fun_bonding_curve",
			LiquidityEvidenceSource: "raydium_pc_vault",
			LiquidityProxyReliable:  true,
			AgeSeconds:              240,
		},
		EffectiveBuyers1m:       4,
		EffectiveBuyers5m:       8,
		LiquidityProxySOL:       25,
		EstimatedImpactPct:      4,
		ClusteringRowStatus:     "resolved",
		ExecutionPenalty:        0.8,
		AdversarialScore:        0.1,
		LiquidityEvidenceSource: "raydium_pc_vault",
		LiquidityProxyReliable:  true,
	}
	return row
}

func variedBuys(start time.Time, slot uint64, n int, sol float64) []model.TokenTradeEvent {
	out := make([]model.TokenTradeEvent, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, model.TokenTradeEvent{
			Slot:        slot + uint64(10+i*3),
			BlockTime:   start.Add(time.Duration([]int{0, 11, 37, 83, 151, 242, 359, 491, 650, 830}[i%10]) * time.Second),
			Wallet:      fmt.Sprintf("organic%d", i),
			Side:        "buy",
			SOLAmount:   sol + float64(i)*0.17,
			TokenAmount: 100 + float64(i*13),
		})
	}
	return out
}

func trade(row model.LiveSnapshot, sec int, slotOffset uint64, wallet, side string, sol, token float64) model.TokenTradeEvent {
	return model.TokenTradeEvent{
		Slot:        row.FirstSeenSlot + slotOffset,
		BlockTime:   row.FirstSeenAt.Add(time.Duration(sec) * time.Second),
		Wallet:      wallet,
		Side:        side,
		SOLAmount:   sol,
		TokenAmount: token,
		IsCreator:   wallet == row.CreatorWallet,
	}
}
