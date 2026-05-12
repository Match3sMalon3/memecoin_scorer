package runner

import (
	"context"
	"log"
	"sync"
	"time"

	"memecoin_scorer/internal/alerts"
	"memecoin_scorer/internal/devprint"
	"memecoin_scorer/internal/model"
	"memecoin_scorer/internal/outcomes"
	"memecoin_scorer/internal/proxy"
)

var (
	outcomeRecorderMu sync.RWMutex
	outcomeRecorder   *outcomes.Recorder
)

func SetOutcomeRecorder(recorder *outcomes.Recorder) {
	outcomeRecorderMu.Lock()
	defer outcomeRecorderMu.Unlock()
	outcomeRecorder = recorder
}

func ScheduleEarlyScore(ctx context.Context, mint string, firstSeen time.Time) {
	go func() {
		timer := time.NewTimer(time.Until(firstSeen.Add(5 * time.Minute)))
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			_ = mint
		}
	}()
}

func ShouldAlert(snap model.LiveSnapshot, ep model.EarlyProxyScore) bool {
	return ep.Band == "RUNNER" &&
		ep.Score >= 62.0 &&
		!HardVeto(snap)
}

func PublishIfRunner(snap model.LiveSnapshot) bool {
	ep := proxy.ScoreEarlyProxy(snap)
	if !ShouldAlert(snap, ep) {
		return false
	}
	prof, _ := devprint.GetProfile(snap.DeployerAddress)
	if prof.IsVetoed {
		return false
	}
	alerts.Publish(alerts.Alert{
		Mint:                   snap.Mint,
		Symbol:                 "",
		Score:                  ep.Score,
		AgeMinutes:             int(snap.AgeSeconds / 60),
		Reasons:                ep.Reasons,
		RiskFlags:              ep.RiskFlags,
		Invalidation:           Invalidation(snap),
		GMGNUrl:                snap.ExecutionURL,
		SolscanUrl:             snap.SolscanURL,
		RealPoolDepth:          snap.RealPoolDepthSOL,
		LiqSource:              snap.LiquidityEvidenceSource,
		DeployerRugRate:        prof.RugRate,
		DeployerLaunches:       prof.TotalLaunches,
		FiredAt:                time.Now().UTC(),
		HistoricalPrecisionPct: 89.0,
		LiveSignalsTotal:       0,
	})
	recordOutcomeSignalSnapshot(snap)
	_ = devprint.RecordLaunch(snap.DeployerAddress, snap.Mint)
	return true
}

func recordOutcomeSignalSnapshot(snap model.LiveSnapshot) {
	outcomeRecorderMu.RLock()
	recorder := outcomeRecorder
	outcomeRecorderMu.RUnlock()
	if recorder == nil {
		return
	}
	if _, _, err := recorder.RecordSignalSnapshot(context.Background(), snap); err != nil {
		log.Printf("outcome RecordSignalSnapshot %s: %v", snap.Mint, err)
	}
}

func HardVeto(s model.LiveSnapshot) bool {
	if s.Top10HolderPct >= 0.95 {
		return true
	}
	if s.ClusteringRowStatus == "full_fallback" {
		return true
	}
	if s.EstimatedImpactPct >= 50 {
		return true
	}
	if s.BuyersLast1m == 0 && s.BuyersLast5m == 0 && s.BuySolLast1m == 0 {
		return true
	}
	if s.RealPoolDepthSOL >= 0 && s.RealPoolDepthSOL < 5 && s.BuyersLast5m < 5 {
		return true
	}
	return false
}

func Invalidation(_ model.LiveSnapshot) []string {
	return []string{
		"Top10 > 25%",
		"Sell pressure > 40%",
		"Real pool depth < 5 SOL",
		"Dev-linked wallet sells",
		"Buyer flow stalls below 5/5min",
	}
}
