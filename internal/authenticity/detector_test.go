package authenticity

import (
	"fmt"
	"testing"
	"time"

	"memecoin_scorer/internal/model"
)

func TestDetectBundleBotKnownCreationBlock(t *testing.T) {
	got := Detect(model.LiveSnapshot{}, []model.BuyEvent{{Wallet: "w1", Block: 10, Timestamp: time.Now(), SolAmount: 1}}, map[string][]model.WalletEvent{}, 10)
	if !got.BundleBot || got.BundleBotConfidence != model.CoverageExact || got.Severity != "high" || got.Score > 50 {
		t.Fatalf("unexpected bundle result: %+v", got)
	}
}

func TestDetectCreationBlockUnknown(t *testing.T) {
	got := Detect(model.LiveSnapshot{}, nil, map[string][]model.WalletEvent{}, 0)
	if got.BundleBot || got.BundleBotConfidence != model.CoverageUnavailable {
		t.Fatalf("unexpected unknown creation result: %+v", got)
	}
}

func TestDetectMechanicalRhythmAndIdenticalSizes(t *testing.T) {
	now := time.Now()
	var buys []model.BuyEvent
	for i := 0; i < 12; i++ {
		amount := 1.0
		if i >= 9 {
			amount = 2.0
		}
		buys = append(buys, model.BuyEvent{
			Wallet:    fmt.Sprintf("w%d", i),
			Block:     uint64(i + 1),
			Timestamp: now.Add(time.Duration(i*10) * time.Second),
			SolAmount: amount,
		})
	}
	got := Detect(model.LiveSnapshot{}, buys, map[string][]model.WalletEvent{}, 1)
	if !got.MechanicalRhythm || !got.IdenticalBuySizes {
		t.Fatalf("mechanical/identical flags missing: %+v", got)
	}
}

func TestDetectUnavailableBotCoverageCapsScore(t *testing.T) {
	got := Detect(model.LiveSnapshot{}, nil, nil, 0)
	if got.Score != 70 {
		t.Fatalf("score=%v want 70 for unavailable bot coverage: %+v", got.Score, got)
	}
}
