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
		`BEST NOW`,
		`DEX N/A`,
		`<th>actions</th>`,
		`VIEW [GMGN]`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("posture scan markup %q missing: %s", want, html)
		}
	}
	if strings.Contains(html, `trigger = flow`) {
		t.Fatalf("helper trigger copy should not render: %s", html)
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
