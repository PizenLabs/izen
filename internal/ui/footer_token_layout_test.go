package ui

import (
	"strings"
	"testing"
	"time"
)

// TestFooterTokenLayout pins the acceptance contract for the footer token
// layout:
//   - the executing bar carries explicit ↑input / ↓output slots, a live cost,
//     tok/s rate, a truncated [model] badge and the drop-proof ^C stop badge
//   - the in/out slots are FIXED-WIDTH: growing counts (1 → 9.9k) never shift
//     the metric column positions
//   - priority-drop: at narrowing widths the model badge drops first, then the
//     rate, then token telemetry — while ^C stop survives at every usable width
//   - the idle bar renders ↑in in · ↓out out (pct%) anchored on the model
func TestFooterTokenLayout(t *testing.T) {
	// ── Executing bar: full telemetry with explicit in/out arrows ──
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.spinnerFrame = 1
	m.streamStartTime = time.Now().Add(-10 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m.setStageMetrics(0, 0, 128)

	wide := stripANSIFooter(m.renderFixedFooter(100, nil))
	for _, want := range []string{"Generating...", "↑0 in", "↓128 out", "tok/s", "^C stop", "⠙"} {
		if !strings.Contains(wide, want) {
			t.Errorf("executing footer missing %q:\n%q", want, wide)
		}
	}

	// ── Fixed-width in/out slots: count growth must not shift columns ──
	m2 := readyChatModel(newTestModel())
	m2.state = StateProcessing
	m2.streaming = true
	m2.spinnerFrame = 1
	m2.streamStartTime = time.Now().Add(-10 * time.Second)
	m2.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m2.setStageMetrics(0, 0, 9200)

	small := stripANSIFooter(m.renderFixedFooter(100, nil))
	large := stripANSIFooter(m2.renderFixedFooter(100, nil))
	smallGap := strings.Index(small, "↓128 out") - strings.Index(small, "↑0 in")
	largeGap := strings.Index(large, "↓9.2k out") - strings.Index(large, "↑0 in")
	if smallGap != largeGap {
		t.Errorf("fixed-width slots violated: in→out column gap changed %d → %d\nsmall: %q\nlarge: %q",
			smallGap, largeGap, small, large)
	}

	// ── Priority drop: ^C stop survives every usable width ──
	for _, w := range []int{70, 48, 30} {
		narrow := stripANSIFooter(m.renderFixedFooter(w, nil))
		if !strings.Contains(narrow, "^C stop") {
			t.Errorf("width %d: ^C stop badge dropped:\n%q", w, narrow)
		}
		if !strings.HasSuffix(strings.TrimSpace(narrow), "^C stop") {
			t.Errorf("width %d: ^C stop must anchor the right edge:\n%q", w, narrow)
		}
	}
	// At 30 cols the secondary telemetry is gone but the spinner+label+stop
	// anchor survives; the model badge is dropped before the rate before the
	// tokens.
	ultra := stripANSIFooter(m.renderFixedFooter(30, nil))
	if strings.Contains(ultra, "tok/s") {
		t.Errorf("width 30 should have dropped the rate segment:\n%q", ultra)
	}
	if !strings.Contains(ultra, "Generating...") || !strings.Contains(ultra, "^C stop") {
		t.Errorf("width 30 must keep the spinner label + stop anchor:\n%q", ultra)
	}

	// ── Idle bar: ↑in in · ↓out out (pct%) on the model anchor ──
	i := readyChatModel(newTestModel())
	i.sessionHasRunPrompts = true
	i.InputTokens = 2300
	i.OutputTokens = 1500
	i.TotalTokens = 3800
	idle := stripANSIFooter(i.renderFixedFooter(120, nil))
	for _, want := range []string{"qwen2.5-coder:7b", "↑2.3k in · ↓1.5k out", "%)"} {
		if !strings.Contains(idle, want) {
			t.Errorf("idle footer missing %q:\n%q", want, idle)
		}
	}
}
