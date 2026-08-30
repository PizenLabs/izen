package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/PizenLabs/izen/internal/modes"
)

// stripANSIFooter strips ANSI escapes so content assertions read clean.
func stripANSIFooter(s string) string { return stripANSITest(s) }

// TestFooterFreshLaunchState pins the FRESH LAUNCH state: before any prompt has
// been submitted the footer shows the clean startup hint — model alias, trace
// toggle and help — with NO token counters, cost metrics or zero-value
// indicators.
func TestFooterFreshLaunchState(t *testing.T) {
	m := readyChatModel(newTestModel())
	width := 80

	footer := stripANSIFooter(m.renderFixedFooter(width, nil))

	for _, want := range []string{"qwen2.5-coder:7b", "Alt+E trace", "Ctrl+H help"} {
		if !strings.Contains(footer, want) {
			t.Errorf("fresh-launch footer missing %q:\n%q", want, footer)
		}
	}

	// Zero-value indicators and idle telemetry must be fully suppressed.
	for _, forbidden := range []string{"0 tok", "tok", "/", "↓", "↑", "usage", "cost", "$", "0/", "Ctrl+C"} {
		if strings.Contains(footer, forbidden) {
			t.Errorf("fresh-launch footer leaked idle telemetry %q:\n%q", forbidden, footer)
		}
	}

	// Strict single-line layout: no chrome, no wrapped second row.
	if strings.Contains(footer, "\n") {
		t.Errorf("fresh-launch footer wrapped to multiple lines:\n%q", footer)
	}
	if lipgloss.Width(stripANSIFooter(footer)) > width {
		t.Errorf("fresh-launch footer exceeds width %d: %q", width, footer)
	}
}

// TestFooterExecutingStateLiveBar pins the EXECUTING state: the dynamic live
// execution bar with spinner, live token count, token rate and interrupt hint.
func TestFooterExecutingStateLiveBar(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.spinnerFrame = 1
	m.streamStartTime = time.Now().Add(-10 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m.setStageMetrics(0, 0, 128) // authoritative provider-reported count

	width := 100
	footer := stripANSIFooter(m.renderFixedFooter(width, nil))

	for _, want := range []string{"Generating...", "↓128 tok", "tok/s", "Ctrl+C interrupt", "⠙"} {
		if !strings.Contains(footer, want) {
			t.Errorf("executing footer missing %q:\n%q", want, footer)
		}
	}
	// The executing bar never renders idle hints.
	if strings.Contains(footer, "Alt+E trace") || strings.Contains(footer, "Ctrl+H help") {
		t.Errorf("executing footer leaked idle hint:\n%q", footer)
	}
	// The authoritatively derived rate ≈ 128 tok / 10s = 12.8 tok/s.
	if !strings.Contains(footer, "12.8") {
		t.Errorf("executing footer missing live token rate (want ~12.8):\n%q", footer)
	}
	if strings.Contains(footer, "\n") {
		t.Errorf("executing footer wrapped to multiple lines:\n%q", footer)
	}
}

// TestFooterActiveIdleState pins the ACTIVE SESSION IDLE state: after prompts
// have run the footer shows persistent refined telemetry anchored on the model
// name (<Model> · ↓in + ↑out tok (pct%) · <Cost> · Alt+E trace) WITHOUT any
// stale execution controls ('Ctrl+C interrupt', '⏸') or a mode badge.
func TestFooterActiveIdleState(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.sessionHasRunPrompts = true
	m.InputTokens = 2860
	m.OutputTokens = 2048
	m.TotalTokens = m.InputTokens + m.OutputTokens
	m.AccumulatedCost = 0.0123

	width := 120
	footer := stripANSIFooter(m.renderFixedFooter(width, nil))

	// The line is anchored on the active model name (NO mode badge prefix).
	if !strings.HasPrefix(strings.TrimSpace(footer), "qwen2.5-coder:7b") {
		t.Errorf("active-idle footer must start with the model name, got prefix:\n%q", footer)
	}
	for _, want := range []string{
		"qwen2.5-coder:7b",  // model alias
		"↓2.9k + ↑2.0k tok", // in + out usage split
		"(", "%)",           // context percentage
		"$0.0123", // accumulated cost
		"Alt+E trace",
	} {
		if !strings.Contains(footer, want) {
			t.Errorf("active-idle footer missing %q:\n%q", want, footer)
		}
	}

	// The Mode Badge belongs EXCLUSIVELY to the Top Bar — never the footer.
	for _, badge := range []string{"[WRITE]", "[READ-ONLY]", "[EXECUTE]"} {
		if strings.Contains(footer, badge) {
			t.Errorf("active-idle footer leaked mode badge %q:\n%q", badge, footer)
		}
	}
	// CRITICAL: no stale execution controls survive once execution finishes.
	for _, stale := range []string{"Ctrl+C", "⏸", "Generating...", "tok/s"} {
		if strings.Contains(footer, stale) {
			t.Errorf("active-idle footer retained stale execution control %q:\n%q", stale, footer)
		}
	}
	if strings.Contains(footer, "\n") {
		t.Errorf("active-idle footer wrapped to multiple lines:\n%q", footer)
	}
	if lipgloss.Width(stripANSIFooter(footer)) > width {
		t.Errorf("active-idle footer exceeds width %d: %q", width, footer)
	}
}

// TestFooterActiveIdleNoModeBadge pins that the Mode Badge never appears in the
// footer regardless of mode — the Top Bar owns it.
func TestFooterActiveIdleNoModeBadge(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.sessionHasRunPrompts = true
	m.InputTokens = 100
	m.OutputTokens = 50
	m.TotalTokens = 150
	m.resolver.Set(modes.ModeAsk)

	footer := stripANSIFooter(m.renderFixedFooter(120, nil))
	if strings.Contains(footer, "[READ-ONLY]") {
		t.Errorf("ask-mode footer must NOT contain the mode badge:\n%q", footer)
	}
	if !strings.HasPrefix(strings.TrimSpace(footer), "qwen2.5-coder:7b") {
		t.Errorf("ask-mode footer must start directly with the model name:\n%q", footer)
	}
}

// TestFooterCompletedStateRevertsToIdle pins the terminal transition: the
// instant execution ends, the footer reverts to the minimal idle layout and
// NEVER retains 'Ctrl+C' / '⏸' execution icons.
func TestFooterCompletedStateRevertsToIdle(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.streamStartTime = time.Now().Add(-10 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m.setStageMetrics(0, 0, 256)

	executing := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(executing, "Generating...") {
		t.Fatalf("precondition: executing bar not rendered:\n%q", executing)
	}

	// ── Terminal transition: operation completes ──
	m.state = StateChat
	m.streaming = false
	m.finishStage(OpOutcomeSuccess)

	idle := stripANSIFooter(m.renderFixedFooter(100, nil))
	for _, stale := range []string{"Generating...", "tok/s", "Ctrl+C", "⏸"} {
		if strings.Contains(idle, stale) {
			t.Errorf("completed footer retained execution control %q:\n%q", stale, idle)
		}
	}
	// Fresh-launch (no prompts run yet): clean startup hint returns.
	if !strings.Contains(idle, "Alt+E trace") {
		t.Errorf("completed footer missing fresh-launch hint:\n%q", idle)
	}
	if strings.Contains(idle, "\n") {
		t.Errorf("completed footer wrapped:\n%q", idle)
	}
}

// TestFooterNeverRendersToast pins the toast migration contract: toasts belong
// to the Top Bar transient overlay — the footer is a pure lifecycle bar and
// never shows a toast message.
func TestFooterNeverRendersToast(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.setToast("Copied selection to clipboard")

	footer := stripANSIFooter(m.renderFixedFooter(80, nil))
	if strings.Contains(footer, "Copied selection") {
		t.Errorf("footer must never render a toast (toasts belong to the Top Bar):\n%q", footer)
	}
}

// TestFooterIdleChipsRightAligned pins that capability chips survive the
// layout rework: they right-align onto the single active-idle footer line
// instead of stealing a second row.
func TestFooterIdleChipsRightAligned(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.sessionHasRunPrompts = true
	m.InputTokens = 100
	m.OutputTokens = 50
	m.TotalTokens = 150
	width := 120

	actions := planApprovalActions().Actions
	footer := m.renderFixedFooter(width, actions)
	stripped := stripANSIFooter(footer)

	if !strings.Contains(stripped, "Approve Plan") {
		t.Errorf("active-idle footer missing capability chip:\n%q", stripped)
	}
	// Base idle hint must survive alongside the chip.
	if !strings.Contains(stripped, "Alt+E trace") {
		t.Errorf("active-idle footer lost trace hint with chips:\n%q", stripped)
	}
	if strings.Contains(stripped, "\n") {
		t.Errorf("chips wrapped the footer to a second row:\n%q", stripped)
	}
	if lipgloss.Width(stripped) > width {
		t.Errorf("footer with chips exceeds width %d: %q", width, stripped)
	}
}

// TestFooterToastColors pins the toast overlay palette: teal #89dceb frame and
// mint green #a6e3a1 confirmation glyph.
func TestFooterToastColors(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	toast := renderToast("Copied to clipboard")
	if !strings.Contains(toast, "38;2;137;220;235m") { // #89dceb teal
		t.Errorf("toast missing teal frame color:\n%q", toast)
	}
	if !strings.Contains(toast, "38;2;166;227;161m") { // #a6e3a1 green
		t.Errorf("toast missing mint-green check color:\n%q", toast)
	}
	if !strings.Contains(toast, "[") || !strings.Contains(toast, "✓") || !strings.Contains(toast, "]") {
		t.Errorf("toast missing [✓ ...] frame:\n%q", toast)
	}
}
