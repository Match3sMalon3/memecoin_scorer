package live_test

import (
	"testing"
	"time"

	"memecoin_scorer/internal/live"
)

func TestReleaseFields_PopulatedWhenEvidenceExists(t *testing.T) {
	now := epoch.Add(2 * time.Hour)
	s := freshSnap(now)
	s.Mint = "RELTESTMINT"
	s.LastEventAt = now
	s.BuyersLast1m = 6
	s.BuyersLast5m = 8
	s.UniqueBuyerCount = 8
	s.TotalBuySOL = 25
	s.TotalSellSOL = 5
	s.HolderCount = 8
	s.MarketCapSOL = 12
	s.LastPriceSOL = 0.000001

	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.OperatorVerdict == "" {
		t.Fatalf("operator_verdict is empty")
	}
	if d.DominantBlocker == "" {
		t.Fatalf("dominant_blocker is empty")
	}
	if d.ExecutionURL == "" {
		t.Fatalf("execution_url is empty")
	}
	if d.WhyNow == "" {
		t.Fatalf("why_now is empty despite qualifying evidence")
	}
}

func TestReleaseFields_PopulatedForResolvedNoBuyerRow(t *testing.T) {
	now := epoch.Add(3 * time.Hour)
	s := freshSnap(now)
	s.Mint = "RELTESTRESOLVED"
	s.LastEventAt = now
	s.BuyersLast1m = 0
	s.BuyersLast5m = 0
	s.UniqueBuyerCount = 0
	s.UniqueWalletsLast1m = nil
	s.UniqueWalletsLast5m = nil
	s.TotalBuySOL = 0
	s.TotalSellSOL = 6
	s.HolderCount = 0
	s.MarketCapSOL = 0
	s.LastPriceSOL = 0.000001

	d := live.ClassifyAt(s, cfgWithNow(), now)
	if d.OperatorVerdict == "" {
		t.Fatalf("operator_verdict is empty")
	}
	if d.DominantBlocker == "" {
		t.Fatalf("dominant_blocker is empty")
	}
	if d.ExecutionURL == "" {
		t.Fatalf("execution_url is empty")
	}
	if d.ClusteringRowStatus == "resolved" && d.WhyNow == "" {
		t.Fatalf("why_now is empty for resolved row")
	}
}
