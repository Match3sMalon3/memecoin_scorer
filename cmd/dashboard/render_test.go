package main

import (
	"strings"
	"testing"
	"time"
)

func sampleLiveRow() map[string]any {
	return map[string]any{
		"mint":                  "RENDERMINT123456789",
		"decision":              "AVOID",
		"operator_verdict":      "structurally broken",
		"dominant_blocker":      "impossible execution • thin liquidity",
		"why_not_higher":        "impossible execution • thin liquidity",
		"why_now":               "5 eff buyers /1m • 5 eff buyers /5m • clean clustering",
		"execution_url":         "https://gmgn.ai/sol/token/RENDERMINT123456789",
		"solscan_url":           "https://solscan.io/token/RENDERMINT123456789",
		"early_proxy":           map[string]any{"score": 74.0, "threshold": 62.0, "band": "CANDIDATE", "reasons": []any{"early effective buyer depth", "positive buy pressure"}, "risk_flags": []any{"very thin liquidity"}, "missing_fields": []any{"market_cap_sol"}, "evidence_version": "test"},
		"signal_state":          "expired",
		"confidence_score":      66.0,
		"buyers_last1m":         5.0,
		"effective_buyers_1m":   5.0,
		"buy_sol_last_1m":       1.2,
		"sell_sol_last_1m":      0.4,
		"buyer_acceleration":    1.0,
		"execution_penalty":     0.4,
		"adversarial_score":     0.2,
		"estimated_impact_pct":  12.0,
		"age_seconds":           120.0,
		"funding_cluster_ratio": 0.0,
		"clustering_row_status": "resolved",
		"clustering_timeouts":   0.0,
		"clustering_fallbacks":  0.0,
		"market_cap_sol":        10.0,
		"last_price_sol":        0.000001,
		"market_cap_reason":     "",
		"is_actionable":         false,
		"engine":                map[string]any{"layer0_reject": true, "layer0_reason": "impossible_execution: liquidity=1.00 SOL < 5.00 SOL minimum", "gates_pass_count": 0.0, "gates": []any{}},
		"holder_count":          5.0,
	}
}

func TestRenderInitialRows_VisibleOperatorCells(t *testing.T) {
	html := renderInitialRows([]map[string]any{sampleLiveRow()})
	if !strings.Contains(html, `<td class="verdict-label"><strong>structurally broken</strong></td>`) {
		t.Fatalf("visible verdict cell missing: %s", html)
	}
	if !strings.Contains(html, `<td class="blocker-cell">impossible execution • thin liquidity</td>`) {
		t.Fatalf("visible blocker cell missing: %s", html)
	}
	if !strings.Contains(html, `<td class="why-now-cell">5 eff buyers /1m • 5 eff buyers /5m • clean clustering</td>`) {
		t.Fatalf("visible why-now cell missing: %s", html)
	}
	if !strings.Contains(html, `<a href="https://gmgn.ai/sol/token/RENDERMINT123456789" class="gmgn-link exec-link" target="_blank" rel="noopener noreferrer">EXECUTE [GMGN]</a>`) {
		t.Fatalf("visible GMGN link missing: %s", html)
	}
}

func TestRenderIndexHTML_ServerRendersPostureHeroScan(t *testing.T) {
	app := &App{
		cfg:              dashConfig{liveMode: true},
		cachedLiveRows:   []map[string]any{sampleLiveRow()},
		cachedLiveRowsAt: time.Now(),
	}
	html := app.renderIndexHTML()
	for _, want := range []string{
		`id="heroCard"`,
		`id="heroName"`,
		`id="heroPrimaryAction"`,
		`id="heroSecondaryAction"`,
		`id="primaryScanBody"`,
		`id="rejectsPanel"`,
		`NO LIVE RUNNER CANDIDATE`,
		`proxy 74 CANDIDATE`,
		`<th>actions</th>`,
		`VIEW [GMGN]`,
		`https://gmgn.ai/sol/token/RENDERMINT123456789`,
		`https://solscan.io/token/RENDERMINT123456789`,
		`SOLSCAN ↗`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("posture scan markup %q missing: %s", want, html)
		}
	}
	if strings.Contains(html, `DEX unavailable`) {
		t.Fatalf("old Dex fallback should not render: %s", html)
	}
	if strings.Contains(html, `trigger = flow`) {
		t.Fatalf("helper trigger copy should not render: %s", html)
	}
}

func TestChooseBestSetupGo_AllFormingDeadRowsReturnNil(t *testing.T) {
	forming := sampleLiveRow()
	forming["mint"] = "FORMINGMINT123456789"
	forming["signal_state"] = "forming"
	forming["early_proxy"] = map[string]any{"score": 0.0, "threshold": 62.0, "band": "DEAD"}

	if best := chooseBestSetupGo([]map[string]any{forming}); best != nil {
		t.Fatalf("best=%v, want nil for all forming DEAD rows", best)
	}
}

func TestChooseBestSetupGo_IgnoresExpiredRows(t *testing.T) {
	expired := sampleLiveRow()
	expired["priority_label"] = "best_on_tape"
	expired["confidence_score"] = 100.0

	forming := sampleLiveRow()
	forming["mint"] = "FORMINGMINT123456789"
	forming["signal_state"] = "forming"
	forming["priority_label"] = "monitor_for_upgrade"
	forming["confidence_score"] = 50.0
	forming["early_proxy"] = map[string]any{"score": 40.0, "threshold": 62.0, "band": "WATCH"}

	best := chooseBestSetupGo([]map[string]any{expired, forming})
	if best == nil {
		t.Fatalf("best=nil, want forming row")
	}
	if got := stringFieldMap(best, "mint"); got != "FORMINGMINT123456789" {
		t.Fatalf("best mint=%q, want forming row", got)
	}
}

func TestChooseBestSetupGo_PrefersFormingHigherEarlyProxyOverCoolingHighConfidence(t *testing.T) {
	cooling := sampleLiveRow()
	cooling["mint"] = "COOLINGMINT123456789"
	cooling["signal_state"] = "cooling"
	cooling["confidence_score"] = 99.0
	cooling["early_proxy"] = map[string]any{"score": 95.0, "threshold": 62.0, "band": "APEX"}
	cooling["last_event_at"] = "2026-04-26T10:00:00Z"

	freshLow := sampleLiveRow()
	freshLow["mint"] = "FRESHLOW123456789"
	freshLow["signal_state"] = "forming"
	freshLow["confidence_score"] = 80.0
	freshLow["early_proxy"] = map[string]any{"score": 50.0, "threshold": 62.0, "band": "WATCH"}
	freshLow["last_event_at"] = "2026-04-26T10:01:00Z"

	freshHigh := sampleLiveRow()
	freshHigh["mint"] = "FRESHHIGH123456789"
	freshHigh["signal_state"] = "forming"
	freshHigh["confidence_score"] = 40.0
	freshHigh["early_proxy"] = map[string]any{"score": 74.0, "threshold": 62.0, "band": "CANDIDATE"}
	freshHigh["last_event_at"] = "2026-04-26T10:00:30Z"

	best := chooseBestSetupGo([]map[string]any{cooling, freshLow, freshHigh})
	if best == nil {
		t.Fatal("best=nil, want forming high proxy row")
	}
	if got := stringFieldMap(best, "mint"); got != "FRESHHIGH123456789" {
		t.Fatalf("best mint=%q, want forming row with higher early proxy score", got)
	}
}

func TestChooseBestSetupGo_CandidateBeatsWatch(t *testing.T) {
	watch := sampleLiveRow()
	watch["mint"] = "WATCHMINT123456789"
	watch["signal_state"] = "forming"
	watch["early_proxy"] = map[string]any{"score": 55.0, "threshold": 62.0, "band": "WATCH"}
	watch["last_event_at"] = "2026-04-26T10:01:00Z"

	candidate := sampleLiveRow()
	candidate["mint"] = "CANDIDATEMINT123456789"
	candidate["signal_state"] = "forming"
	candidate["early_proxy"] = map[string]any{"score": 70.0, "threshold": 62.0, "band": "CANDIDATE"}
	candidate["last_event_at"] = "2026-04-26T10:00:00Z"

	best := chooseBestSetupGo([]map[string]any{watch, candidate})
	if best == nil {
		t.Fatal("best=nil, want candidate row")
	}
	if got := stringFieldMap(best, "mint"); got != "CANDIDATEMINT123456789" {
		t.Fatalf("best mint=%q, want CANDIDATEMINT123456789", got)
	}
}

func TestChooseBestSetupGo_ExpiredCandidateCannotBecomeHero(t *testing.T) {
	expired := sampleLiveRow()
	expired["mint"] = "EXPIREDCANDIDATE123456789"
	expired["signal_state"] = "expired"
	expired["early_proxy"] = map[string]any{"score": 90.0, "threshold": 62.0, "band": "CANDIDATE"}

	if best := chooseBestSetupGo([]map[string]any{expired}); best != nil {
		t.Fatalf("best=%v, want nil for expired CANDIDATE", best)
	}
}

func TestChooseBestSetupGo_AllExpiredReturnsNil(t *testing.T) {
	expired := sampleLiveRow()
	expired["priority_label"] = "best_on_tape"

	if best := chooseBestSetupGo([]map[string]any{expired}); best != nil {
		t.Fatalf("best=%v, want nil when every row is expired", best)
	}
}

func TestChooseBestSetupGo_CoolingCanWinWhenNoFormingOrActiveNonDead(t *testing.T) {
	cooling := sampleLiveRow()
	cooling["mint"] = "COOLINGMINT123456789"
	cooling["signal_state"] = "cooling"
	cooling["early_proxy"] = map[string]any{"score": 74.0, "threshold": 62.0, "band": "CANDIDATE"}

	best := chooseBestSetupGo([]map[string]any{cooling})
	if best == nil {
		t.Fatal("best=nil, want cooling fallback row")
	}
	if got := stringFieldMap(best, "mint"); got != "COOLINGMINT123456789" {
		t.Fatalf("best mint=%q, want cooling fallback row", got)
	}
}

func TestRenderIndexHTML_AllFormingDeadRowsShowNoRunnerCandidate(t *testing.T) {
	row := sampleLiveRow()
	row["signal_state"] = "forming"
	row["early_proxy"] = map[string]any{"score": 0.0, "threshold": 62.0, "band": "DEAD", "risk_flags": []any{"very thin liquidity"}}
	app := &App{
		cfg:              dashConfig{liveMode: true},
		cachedLiveRows:   []map[string]any{row},
		cachedLiveRowsAt: time.Now(),
	}

	html := app.renderIndexHTML()
	for _, want := range []string{
		`NO LIVE RUNNER CANDIDATE`,
		`forming tokens observed, no runner footprint yet`,
		`all forming rows are currently DEAD by early proxy`,
		`proxy 0 DEAD`,
		`https://gmgn.ai/sol/token/RENDERMINT123456789`,
		`https://solscan.io/token/RENDERMINT123456789`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("all-DEAD markup %q missing: %s", want, html)
		}
	}
}

func TestChooseBestSetupGo_FormingWatchCanBecomeHero(t *testing.T) {
	watch := sampleLiveRow()
	watch["mint"] = "WATCHMINT123456789"
	watch["signal_state"] = "forming"
	watch["early_proxy"] = map[string]any{"score": 50.0, "threshold": 62.0, "band": "WATCH"}

	best := chooseBestSetupGo([]map[string]any{watch})
	if best == nil {
		t.Fatal("best=nil, want forming WATCH row")
	}
	if got := stringFieldMap(best, "mint"); got != "WATCHMINT123456789" {
		t.Fatalf("best mint=%q, want WATCHMINT123456789", got)
	}
}

func TestRenderIndexHTML_FormingNonActionableHeroCopy(t *testing.T) {
	row := sampleLiveRow()
	row["signal_state"] = "forming"
	row["is_actionable"] = false
	row["early_proxy"] = map[string]any{"score": 74.0, "threshold": 62.0, "band": "CANDIDATE", "reasons": []any{"early effective buyer depth"}}
	app := &App{
		cfg:              dashConfig{liveMode: true},
		cachedLiveRows:   []map[string]any{row},
		cachedLiveRowsAt: time.Now(),
	}

	html := app.renderIndexHTML()
	for _, want := range []string{
		`FORMATION WATCH — NOT EXECUTION`,
		`<span class="badge forming">FORMING</span>`,
		`VIEW [GMGN]`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("forming hero markup %q missing: %s", want, html)
		}
	}
}

func TestIndexHTML_NoJSDerivationForBackendOwnedFields(t *testing.T) {
	forbidden := []string{
		"s.execution_url ||",
		"s.why_now ||",
		"s.operator_verdict ||",
		"best.operator_verdict ||",
		"best.why_now ||",
		"https://gmgn.ai/sol/token/\" + encodeURIComponent(mint)",
	}
	for _, pattern := range forbidden {
		if strings.Contains(indexHTML, pattern) || strings.Contains(wowIndexHTML, pattern) {
			t.Fatalf("forbidden JS derivation present: %s", pattern)
		}
	}
}
