package authenticity

import (
	"fmt"
	"math"
	"sort"

	"memecoin_scorer/internal/model"
)

func Detect(s model.LiveSnapshot, buys []model.BuyEvent, wallets map[string][]model.WalletEvent, creationBlock uint64) model.AuthenticityResult {
	res := model.AuthenticityResult{
		BundleBotConfidence: model.CoverageExact,
		SniperBotConfidence: model.CoverageExact,
		BumpBotConfidence:   model.CoverageExact,
		Score:               100,
		Severity:            "none",
	}

	if creationBlock == 0 {
		res.BundleBotConfidence = model.CoverageUnavailable
		res.SniperBotConfidence = model.CoverageUnavailable
	} else {
		for _, b := range buys {
			if b.Wallet != "" && b.Wallet == s.CreatorWallet {
				continue
			}
			if b.Block == creationBlock {
				res.BundleBot = true
			}
			if b.Block >= creationBlock+1 && b.Block <= creationBlock+5 {
				res.SniperBot = true
			}
		}
	}

	if wallets == nil {
		res.BumpBotConfidence = model.CoverageUnavailable
	} else {
		for wallet, events := range wallets {
			if isBumpBot(events) {
				res.BumpBot = true
				res.Flags = append(res.Flags, "bump_bot: "+wallet)
			}
		}
	}

	if res.BundleBot {
		res.Flags = append(res.Flags, "bundle_bot: non-creator buy in creation block")
	}
	if res.SniperBot {
		res.Flags = append(res.Flags, "sniper_bot: non-creator buy within creation block +5")
	}
	if mechanicalRhythm(buys, &res) {
		res.MechanicalRhythm = true
	}
	if identicalBuySizes(buys, &res) {
		res.IdenticalBuySizes = true
	}
	if s.ClusteringRowStatus == "full_fallback" {
		res.Flags = append(res.Flags, "full_fallback: clustering unavailable")
	}
	if s.ClusteringRowStatus == "partial_fallback" {
		res.Flags = append(res.Flags, "partial_fallback: clustering partially unavailable")
	}
	if s.Top10HolderPct >= 0.95 {
		res.Flags = append(res.Flags, "top10_holder_pct: concentration >= 0.95")
	}

	base := 100.0
	if res.BundleBot && res.BundleBotConfidence == model.CoverageExact {
		base -= 50
	}
	if res.BumpBot && res.BumpBotConfidence == model.CoverageExact {
		base -= 40
	}
	if res.MechanicalRhythm {
		base -= 30
	}
	if res.IdenticalBuySizes {
		base -= 20
	}
	if res.SniperBot && res.SniperBotConfidence == model.CoverageExact {
		base -= 15
	}
	if res.BundleBotConfidence == model.CoverageUnavailable &&
		res.SniperBotConfidence == model.CoverageUnavailable &&
		res.BumpBotConfidence == model.CoverageUnavailable {
		if base > 70 {
			base = 70
		}
		res.Flags = append(res.Flags, "authenticity_partial: bot detection data unavailable")
	}
	if base < 0 {
		base = 0
	}
	res.Score = base
	res.Severity = severity(res)
	return res
}

func isBumpBot(events []model.WalletEvent) bool {
	if len(events) < 2 {
		return false
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })
	flips := 0
	netChange := 0.0
	for i, ev := range events {
		netChange += ev.TokenQty
		if i == 0 {
			continue
		}
		prev := events[i-1]
		if prev.IsBuy == ev.IsBuy {
			continue
		}
		a := math.Abs(prev.TokenQty)
		b := math.Abs(ev.TokenQty)
		if a <= 0 || b <= 0 {
			continue
		}
		if math.Abs(a-b)/math.Max(a, b) <= 0.01 {
			flips++
		}
	}
	return float64(flips)/(math.Abs(netChange)+1) >= 50
}

func mechanicalRhythm(buys []model.BuyEvent, res *model.AuthenticityResult) bool {
	if len(buys) < 8 {
		return false
	}
	sort.Slice(buys, func(i, j int) bool { return buys[i].Timestamp.Before(buys[j].Timestamp) })
	var gaps []float64
	for i := 1; i < len(buys); i++ {
		gap := buys[i].Timestamp.Sub(buys[i-1].Timestamp).Seconds()
		if gap >= 0 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return false
	}
	mean := avg(gaps)
	if mean == 0 {
		return false
	}
	variance := 0.0
	for _, gap := range gaps {
		variance += math.Pow(gap-mean, 2)
	}
	cv := math.Sqrt(variance/float64(len(gaps))) / mean
	if cv < 0.40 {
		res.Flags = append(res.Flags, fmt.Sprintf("mechanical_rhythm: inter-arrival CV = %.2f", cv))
		return true
	}
	return false
}

func identicalBuySizes(buys []model.BuyEvent, res *model.AuthenticityResult) bool {
	if len(buys) < 5 {
		return false
	}
	amounts := make([]float64, 0, len(buys))
	for _, b := range buys {
		if b.SolAmount > 0 {
			amounts = append(amounts, b.SolAmount)
		}
	}
	if len(amounts) < 5 {
		return false
	}
	sort.Float64s(amounts)
	median := amounts[len(amounts)/2]
	if len(amounts)%2 == 0 {
		median = (amounts[len(amounts)/2-1] + amounts[len(amounts)/2]) / 2
	}
	if median <= 0 {
		return false
	}
	near := 0
	for _, amount := range amounts {
		if math.Abs(amount-median)/median <= 0.10 {
			near++
		}
	}
	share := float64(near) / float64(len(amounts))
	if share >= 0.60 {
		res.Flags = append(res.Flags, fmt.Sprintf("identical_buy_sizes: %.0f%% within 10%% of %.4f SOL", share*100, median))
		return true
	}
	return false
}

func severity(res model.AuthenticityResult) string {
	if res.BundleBot && res.BundleBotConfidence == model.CoverageExact ||
		(res.BumpBot && res.MechanicalRhythm) {
		return "high"
	}
	if res.MechanicalRhythm || res.IdenticalBuySizes || res.SniperBot {
		return "medium"
	}
	if len(res.Flags) > 0 {
		return "low"
	}
	return "none"
}

func avg(xs []float64) float64 {
	total := 0.0
	for _, x := range xs {
		total += x
	}
	return total / float64(len(xs))
}
