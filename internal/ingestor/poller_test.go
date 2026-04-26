package ingestor_test

import (
	"testing"
	"time"

	"memecoin_scorer/internal/ingestor"
	"memecoin_scorer/internal/live"
)

func TestDefaultFreshnessWindowsAreNotShorterThanPollCadence(t *testing.T) {
	p := ingestor.NewPoller(ingestor.PollConfig{APIKey: "test-key"}, ingestor.NewIngressHealth())
	if p == nil {
		t.Fatal("NewPoller returned nil")
	}

	pollInterval := p.Interval()
	cfg := live.DefaultLiveConfig()

	if got := time.Duration(cfg.MaxSignalAgeMinBuyReady * float64(time.Minute)); got < pollInterval {
		t.Fatalf("BUY/READY freshness window %s is shorter than poll cadence %s", got, pollInterval)
	}
	if got := time.Duration(cfg.MaxSignalAgeMinWatch * float64(time.Minute)); got < pollInterval {
		t.Fatalf("WATCH freshness window %s is shorter than poll cadence %s", got, pollInterval)
	}
}
