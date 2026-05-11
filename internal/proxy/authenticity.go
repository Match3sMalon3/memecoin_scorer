package proxy

import (
	"math"
	"sort"
	"strings"
	"time"

	"memecoin_scorer/internal/model"
)

type BotDetectionResult struct {
	BundleBotDetected       bool
	BundleBotConfidence     string
	SniperBotDetected       bool
	SniperBotConfidence     string
	SniperBuyCount          int
	SniperUniqueWallets     int
	SniperBuySOL            float64
	SniperShareBuySOL       float64
	BumpBotDetected         bool
	BumpBotScore            float64
	BumpBotWallets          []string
	CommentBotProxyDetected bool
	BotFlags                []string
	AuthenticityLabel       string
	MechanicalityScore      float64
	EvidenceSource          string
}

type LiquidityVelocityFeatures struct {
	BuySOL5m                 float64
	BuyTradeCount5m          int
	UniqueBuyers5m           int
	SOLPerTrade5m            float64
	SOLPerBuyer5m            float64
	VSolCurrent              float64
	VSolPerTrade             float64
	VSolPerMinute            float64
	TradesTo10SOL            int
	TradesTo20SOL            int
	TradesTo40SOL            int
	GraduationDistanceSOL    float64
	VelocityLabel            string
	RawLiquidityVelocity     float64
	OrganicLiquidityVelocity float64
}

func ApplyAuthenticityEvidence(s *model.LiveSnapshot) {
	if s == nil {
		return
	}
	bot, vel, mode, subtype, why, action := AnalyzeAuthenticity(*s)
	s.BotFlags = bot.BotFlags
	s.AuthenticityLabel = bot.AuthenticityLabel
	s.MechanicalityScore = bot.MechanicalityScore
	s.BundleBotDetected = bot.BundleBotDetected
	s.BundleBotConfidence = bot.BundleBotConfidence
	s.SniperBotDetected = bot.SniperBotDetected
	s.SniperBotConfidence = bot.SniperBotConfidence
	s.SniperShareEarlyBuySOL = bot.SniperShareBuySOL
	s.BumpBotDetected = bot.BumpBotDetected
	s.BumpBotScore = bot.BumpBotScore
	s.BumpBotWallets = bot.BumpBotWallets
	s.LiquidityVelocityLabel = vel.VelocityLabel
	s.SOLPerTrade5m = vel.SOLPerTrade5m
	if s.SolPerTrade5m == 0 {
		s.SolPerTrade5m = vel.SOLPerTrade5m
	}
	s.SOLPerBuyer5m = vel.SOLPerBuyer5m
	if s.SolPerUniqueBuyer5m == 0 {
		s.SolPerUniqueBuyer5m = vel.SOLPerBuyer5m
	}
	s.VSolPerTrade = vel.VSolPerTrade
	s.VSolPerMinute = vel.VSolPerMinute
	s.RawLiquidityVelocity = vel.RawLiquidityVelocity
	s.OrganicLiquidityVelocity = vel.OrganicLiquidityVelocity
	s.SignalMode = mode
	s.RunnerSubtype = subtype
	s.WhyNotWOW = why
	s.OperatorAction = action
}

func AnalyzeAuthenticity(s model.LiveSnapshot) (BotDetectionResult, LiquidityVelocityFeatures, string, string, string, string) {
	events := append([]model.TokenTradeEvent(nil), s.TradeHistory...)
	sort.Slice(events, func(i, j int) bool {
		if events[i].Slot != events[j].Slot {
			return events[i].Slot < events[j].Slot
		}
		return events[i].BlockTime.Before(events[j].BlockTime)
	})
	bot := BotDetectionResult{
		BundleBotConfidence: "unavailable",
		SniperBotConfidence: "unavailable",
		AuthenticityLabel:   "organic",
		EvidenceSource:      "trade_history",
	}
	mode := signalMode(s)
	subtype := runnerSubtype(mode)
	if len(events) == 0 {
		vel := liquidityVelocity(s, events, bot)
		return bot, vel, mode, subtype, "insufficient trade history for authenticity checks", "WAIT"
	}

	firstSlot := s.FirstSeenSlot
	if firstSlot == 0 {
		firstSlot = events[0].Slot
	}
	creator := s.CreatorWallet

	bot.BundleBotConfidence = "approximate"
	sameSlotBuyers := map[string]struct{}{}
	for _, ev := range events {
		if ev.Side == "buy" && ev.Slot == firstSlot && ev.Wallet != "" && ev.Wallet != creator {
			sameSlotBuyers[ev.Wallet] = struct{}{}
		}
	}
	if len(sameSlotBuyers) >= 2 {
		bot.BundleBotDetected = true
		bot.BotFlags = append(bot.BotFlags, "approximate_bundle_bot:first_seen_slot_proxy")
	}

	earlySOL := 0.0
	sniperWallets := map[string]struct{}{}
	for _, ev := range events {
		if ev.Side != "buy" || ev.Slot > firstSlot+5 {
			continue
		}
		earlySOL += ev.SOLAmount
		if ev.Wallet != creator {
			bot.SniperBuyCount++
			bot.SniperBuySOL += ev.SOLAmount
			sniperWallets[ev.Wallet] = struct{}{}
		}
	}
	if bot.SniperBuyCount > 0 {
		bot.SniperBotDetected = true
		bot.SniperBotConfidence = "approximate"
		bot.SniperUniqueWallets = len(sniperWallets)
		if earlySOL > 0 {
			bot.SniperShareBuySOL = bot.SniperBuySOL / earlySOL
		}
		if bot.SniperShareBuySOL >= 0.70 {
			bot.BotFlags = append(bot.BotFlags, "high sniper share")
		} else {
			bot.BotFlags = append(bot.BotFlags, "sniper buys near first_seen_slot")
		}
	}

	bumpDetect(events, &bot)
	mechanicalDetect(events, s, &bot)
	bot.BotFlags = dedupe(bot.BotFlags)

	switch {
	case bot.MechanicalityScore >= 70 || bot.BumpBotDetected:
		bot.AuthenticityLabel = "bot_like"
	case bot.MechanicalityScore >= 40:
		bot.AuthenticityLabel = "mechanical"
	case len(bot.BotFlags) > 0:
		bot.AuthenticityLabel = "mixed"
	default:
		bot.AuthenticityLabel = "organic"
	}
	vel := liquidityVelocity(s, events, bot)
	why := whyNotWOW(bot, vel, mode, s)
	action := operatorAction(bot, vel, mode, s)
	return bot, vel, mode, subtype, why, action
}

func bumpDetect(events []model.TokenTradeEvent, bot *BotDetectionResult) {
	byWallet := map[string][]model.TokenTradeEvent{}
	for _, ev := range events {
		if ev.Wallet != "" {
			byWallet[ev.Wallet] = append(byWallet[ev.Wallet], ev)
		}
	}
	for wallet, evs := range byWallet {
		sort.Slice(evs, func(i, j int) bool { return evs[i].BlockTime.Before(evs[j].BlockTime) })
		flips := 0
		buyTok, sellTok := 0.0, 0.0
		maxQ := 0.0
		for i, ev := range evs {
			if ev.Side == "buy" {
				buyTok += ev.TokenAmount
			} else {
				sellTok += ev.TokenAmount
			}
			maxQ = math.Max(maxQ, ev.TokenAmount)
			if i == 0 || ev.Side == evs[i-1].Side {
				continue
			}
			q1, q2 := ev.TokenAmount, evs[i-1].TokenAmount
			if nearSame(q1, q2, 0.01) || (q1 == 0 || q2 == 0) && nearSame(ev.SOLAmount, evs[i-1].SOLAmount, 0.03) {
				flips++
			}
		}
		net := math.Abs(buyTok - sellTok)
		score := float64(flips) / (net + 1e-9)
		if maxQ > 0 && flips >= 3 && net <= maxQ*0.05 {
			score = math.Max(score, 50)
		}
		if score >= 50 || (flips >= 3 && net <= maxQ*0.05) {
			bot.BumpBotDetected = true
			bot.BumpBotWallets = append(bot.BumpBotWallets, wallet)
			bot.BumpBotScore = math.Max(bot.BumpBotScore, score)
		}
	}
	if bot.BumpBotDetected {
		bot.BotFlags = append(bot.BotFlags, "bump bot buy/sell flips")
	}
}

func mechanicalDetect(events []model.TokenTradeEvent, s model.LiveSnapshot, bot *BotDetectionResult) {
	var buys []model.TokenTradeEvent
	flowByWallet := map[string]float64{}
	totalFlow := 0.0
	cycles := 0
	for i, ev := range events {
		totalFlow += ev.SOLAmount
		flowByWallet[ev.Wallet] += ev.SOLAmount
		if ev.Side == "buy" {
			buys = append(buys, ev)
		}
		if i > 0 && ev.Side != events[i-1].Side && ev.Wallet == events[i-1].Wallet {
			cycles++
		}
	}
	if len(buys) >= 5 {
		var intervals []float64
		roundAligned := 0
		for i := 1; i < len(buys); i++ {
			d := buys[i].BlockTime.Sub(buys[i-1].BlockTime).Seconds()
			if d > 0 {
				intervals = append(intervals, d)
				if nearMultiple(d, 30, 3) || nearMultiple(d, 60, 4) {
					roundAligned++
				}
			}
		}
		if len(intervals) >= 4 && coefficientVariation(intervals) < 0.40 {
			bot.BotFlags = append(bot.BotFlags, "regular interval buys")
			bot.MechanicalityScore += 30
		}
		if len(intervals) > 0 && float64(roundAligned)/float64(len(intervals)) >= 0.50 {
			bot.BotFlags = append(bot.BotFlags, "round-clock aligned buys")
			bot.MechanicalityScore += 20
		}
		if repeatedSizeShare(buys) >= 0.60 {
			bot.BotFlags = append(bot.BotFlags, "repeated identical buy sizes")
			bot.MechanicalityScore += 25
		}
	}
	if cycles >= 3 {
		bot.BotFlags = append(bot.BotFlags, "structured sell-buy cycle")
		bot.MechanicalityScore += 30
	}
	topShare := 0.0
	for _, v := range flowByWallet {
		if totalFlow > 0 {
			topShare = math.Max(topShare, v/totalFlow)
		}
	}
	if topShare >= 0.55 && len(events) >= 6 {
		bot.BotFlags = append(bot.BotFlags, "concentrated wallet flow")
		bot.MechanicalityScore += 20
	}
	if s.FundingClusterRatio >= 0.5 {
		bot.MechanicalityScore += 15
	}
	if bot.MechanicalityScore > 100 {
		bot.MechanicalityScore = 100
	}
}

func liquidityVelocity(s model.LiveSnapshot, events []model.TokenTradeEvent, bot BotDetectionResult) LiquidityVelocityFeatures {
	ref := s.LastEventAt
	if ref.IsZero() {
		ref = time.Now()
	}
	cutoff := ref.Add(-5 * time.Minute)
	botWallet := map[string]struct{}{}
	for _, w := range bot.BumpBotWallets {
		botWallet[w] = struct{}{}
	}
	buySOL, organicSOL := 0.0, 0.0
	trades, organicTrades := 0, 0
	buyers := map[string]struct{}{}
	for _, ev := range events {
		if ev.Side != "buy" || ev.BlockTime.Before(cutoff) {
			continue
		}
		buySOL += ev.SOLAmount
		trades++
		buyers[ev.Wallet] = struct{}{}
		if _, bad := botWallet[ev.Wallet]; !bad && bot.AuthenticityLabel != "bot_like" {
			organicSOL += ev.SOLAmount
			organicTrades++
		}
	}
	vsol := liquidityForScoring(s)
	ageMin := math.Max(s.AgeSeconds/60, 0.1)
	out := LiquidityVelocityFeatures{
		BuySOL5m:                 buySOL,
		BuyTradeCount5m:          trades,
		UniqueBuyers5m:           len(buyers),
		SOLPerTrade5m:            buySOL / math.Max(float64(trades), 1),
		SOLPerBuyer5m:            buySOL / math.Max(float64(len(buyers)), 1),
		VSolCurrent:              vsol,
		VSolPerTrade:             vsol / math.Max(float64(s.TotalEventCount), 1),
		VSolPerMinute:            vsol / ageMin,
		RawLiquidityVelocity:     buySOL / math.Max(float64(trades), 1),
		OrganicLiquidityVelocity: organicSOL / math.Max(float64(organicTrades), 1),
	}
	switch {
	case out.OrganicLiquidityVelocity >= 3 && organicTrades >= 3:
		out.VelocityLabel = "exceptional"
	case out.OrganicLiquidityVelocity >= 1 && organicTrades >= 3:
		out.VelocityLabel = "strong"
	case out.SOLPerTrade5m >= 0.2:
		out.VelocityLabel = "normal"
	default:
		out.VelocityLabel = "weak"
	}
	return out
}

func signalMode(s model.LiveSnapshot) string {
	if hasLaunchEvidence(s) || validBondingLiquidityState(s) {
		return "launch_bonding"
	}
	if s.AgeSeconds > 1800 && s.LiquidityProxyReliable {
		return "migrated_amm_momentum"
	}
	if s.AgeSeconds > 1800 {
		return "revival_existing_token"
	}
	return "unknown"
}

func hasLaunchEvidence(s model.LiveSnapshot) bool {
	return s.LaunchSlot > 0 ||
		s.LaunchDetectedAt != nil ||
		(strings.TrimSpace(s.LaunchEvidenceSource) != "" && strings.TrimSpace(s.LaunchEvidenceSource) != "unknown")
}

func runnerSubtype(mode string) string {
	switch mode {
	case "launch_bonding":
		return "LAUNCH_RUNNER"
	case "revival_existing_token":
		return "REVIVAL_RUNNER"
	case "migrated_amm_momentum":
		return "AMM_MOMENTUM_RUNNER"
	default:
		return "WATCH"
	}
}

func whyNotWOW(bot BotDetectionResult, vel LiquidityVelocityFeatures, mode string, s model.LiveSnapshot) string {
	if bot.BumpBotDetected {
		return "bump bot flips detected"
	}
	if bot.BundleBotDetected {
		return "bundle bot pattern detected"
	}
	if bot.AuthenticityLabel == "bot_like" {
		return "bot-like activity detected — no automatic entry"
	}
	if containsAny(bot.BotFlags, "regular interval buys", "structured sell-buy cycle", "repeated identical buy sizes") {
		return "mechanical interval buying detected — downgrade"
	}
	if mode != "launch_bonding" && s.AgeSeconds > 1800 {
		return "REVIVAL signal — not launch WOW"
	}
	if vel.VelocityLabel == "weak" {
		return "organic liquidity velocity weak"
	}
	return ""
}

func operatorAction(bot BotDetectionResult, vel LiquidityVelocityFeatures, mode string, s model.LiveSnapshot) string {
	if bot.BumpBotDetected || bot.BundleBotDetected || bot.AuthenticityLabel == "bot_like" {
		return "AVOID"
	}
	if containsAny(bot.BotFlags, "regular interval buys", "repeated identical buy sizes", "structured sell-buy cycle") {
		return "WATCH_AUTHENTICITY"
	}
	if mode == "unknown" || vel.VelocityLabel == "weak" {
		return "WATCH"
	}
	return "WATCH_RUNNER"
}

func nearSame(a, b, pct float64) bool {
	m := math.Max(math.Abs(a), math.Abs(b))
	if m <= 0 {
		return false
	}
	return math.Abs(a-b)/m <= pct
}

func nearMultiple(v, target, tol float64) bool {
	if target <= 0 {
		return false
	}
	m := math.Round(v/target) * target
	return math.Abs(v-m) <= tol
}

func coefficientVariation(values []float64) float64 {
	if len(values) == 0 {
		return 999
	}
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	if mean <= 0 {
		return 999
	}
	variance := 0.0
	for _, v := range values {
		d := v - mean
		variance += d * d
	}
	return math.Sqrt(variance/float64(len(values))) / mean
}

func repeatedSizeShare(buys []model.TokenTradeEvent) float64 {
	best := 0
	for i := range buys {
		count := 0
		for j := range buys {
			if nearSame(buys[i].SOLAmount, buys[j].SOLAmount, 0.10) {
				count++
			}
		}
		if count > best {
			best = count
		}
	}
	return float64(best) / math.Max(float64(len(buys)), 1)
}

func containsAny(values []string, needles ...string) bool {
	for _, v := range values {
		for _, n := range needles {
			if strings.Contains(v, n) {
				return true
			}
		}
	}
	return false
}
