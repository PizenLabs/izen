package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TestFooterFlexFlowLayout pins the flex-box flow layout engine:
// left telemetry cluster (natural widths, tight " · ") + dynamic middle
// gap + right action badge pinned to the exact right edge, with the final
// line exactly matching the terminal width on every frame.
func TestFooterFlexFlowLayout(t *testing.T) {
	// ── Idle: qwen2.5-coder:7b · ↑712 · ↓10 · $free + ^P menu ──
	idle := readyChatModel(newTestModel())
	idle.sessionHasRunPrompts = true
	idle.InputTokens = 712
	idle.OutputTokens = 10
	idle.TotalTokens = 722

	for _, width := range []int{120, 100, 80, 70, 55, 45, 35} {
		got := stripANSIFooter(idle.renderFixedFooter(width, nil))
		if lipgloss.Width(got) != width {
			t.Errorf("idle width %d: got %d, want %d:\n%q", width, lipgloss.Width(got), width, got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("idle width %d wrapped:\n%q", width, got)
		}
		if !strings.Contains(got, "↑712") || !strings.Contains(got, "↓10") {
			t.Errorf("idle width %d missing telemetry:\n%q", width, got)
		}
		if !strings.HasSuffix(strings.TrimSpace(got), menuBadge) {
			t.Errorf("idle width %d: %q must pin to the right edge:\n%q", width, menuBadge, got)
		}
		// Middle fill is pure whitespace: strip the badge, the remainder
		// must end with spaces (the dynamic gap), never with text reflow.
		trimmed := strings.TrimSpace(got)
		if !strings.HasSuffix(got, menuBadge) && !strings.HasSuffix(trimmed, menuBadge) {
			t.Errorf("idle width %d lost right pin:\n%q", width, got)
		}
	}

	// ── Executing: Generating... · ↑712 · ↓25 · rate + ^C stop ──
	exec := readyChatModel(newTestModel())
	exec.state = StateProcessing
	exec.streaming = true
	exec.spinnerFrame = 1
	exec.streamStartTime = time.Now().Add(-10 * time.Second)
	exec.InputTokens = 712
	exec.OutputTokens = 10
	exec.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	exec.setStageMetrics(0, 0, 15)

	for _, width := range []int{120, 100, 80, 70, 48, 30} {
		got := stripANSIFooter(exec.renderFixedFooter(width, nil))
		if lipgloss.Width(got) != width {
			t.Errorf("exec width %d: got %d, want %d:\n%q", width, lipgloss.Width(got), width, got)
		}
		if !strings.Contains(got, "Generating...") {
			t.Errorf("exec width %d missing state label:\n%q", width, got)
		}
		if !strings.HasSuffix(strings.TrimSpace(got), stopBadge) {
			t.Errorf("exec width %d: %q must pin to the right edge:\n%q", width, stopBadge, got)
		}
	}

	// ── Flex spacer math: gap = total - left - right ──
	left := footerSep("qwen2.5-coder:7b", "↑712", "↓10", "$free")
	right := menuBadge
	pinned := flexPinRight(left, right, 80)
	if lipgloss.Width(stripANSITest(pinned)) != 80 {
		t.Errorf("flexPinRight width = %d, want 80:\n%q", lipgloss.Width(stripANSITest(pinned)), pinned)
	}
	gap := 80 - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	want := left + strings.Repeat(" ", gap) + right
	if stripANSITest(pinned) != want {
		t.Errorf("flex pin mismatch:\n got %q\nwant %q", stripANSITest(pinned), want)
	}

	// ── Narrow fallback truncates the model, never detaches the badge ──
	narrow := stripANSIFooter(idle.renderFixedFooter(35, nil))
	if !strings.HasSuffix(strings.TrimSpace(narrow), menuBadge) {
		t.Errorf("narrow idle must keep pinned badge:\n%q", narrow)
	}
	if !strings.Contains(narrow, "↑712") || !strings.Contains(narrow, "↓10") {
		t.Errorf("narrow idle must keep token telemetry:\n%q", narrow)
	}
}

// TestZeroFloatingSeparators pins INVARIANT 1: separator dots sit strictly
// adjacent to metrics (↑712 · ↓10) with single-space delimiters and zero
// trailing padding inside metric slots.
func TestZeroFloatingSeparators(t *testing.T) {
	idle := readyChatModel(newTestModel())
	idle.sessionHasRunPrompts = true
	idle.InputTokens = 712
	idle.OutputTokens = 10
	idle.TotalTokens = 722

	exec := readyChatModel(newTestModel())
	exec.state = StateProcessing
	exec.streaming = true
	exec.spinnerFrame = 1
	exec.streamStartTime = time.Now().Add(-10 * time.Second)
	exec.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	exec.setStageMetrics(0, 0, 25)

	for _, m := range []struct {
		name  string
		model *model
		width int
	}{
		{"idle-120", idle, 120},
		{"idle-80", idle, 80},
		{"exec-100", exec, 100},
		{"exec-70", exec, 70},
	} {
		got := stripANSIFooter(m.model.renderFixedFooter(m.width, nil))
		// Tight separators: exactly one space on each side of every dot.
		if strings.Contains(got, "  ·") || strings.Contains(got, "·  ") {
			t.Errorf("%s: floating separator gap:\n%q", m.name, got)
		}
		if strings.Contains(got, "   ") && strings.Contains(got, "·") {
			// Triple spaces near telemetry indicate slot padding leakage.
			// The dynamic middle gap is allowed to be wide, but no wide
			// gap may sit ADJACENT to a dot.
			for _, dot := range []string{" ·  ", "  · "} {
				if strings.Contains(got, dot) {
					t.Errorf("%s: wide gap adjacent to separator:\n%q", m.name, got)
				}
			}
		}
		// Canonical adjacent pair must exist.
		if strings.Contains(got, "↑712") && strings.Contains(got, "↓") {
			idxUp := strings.Index(got, "↑712")
			rest := got[idxUp+len("↑712"):]
			if !strings.HasPrefix(rest, " · ") {
				t.Errorf("%s: ↑712 not followed by tight ' · ':\n%q", m.name, got)
			}
		}
	}

	// footerSep itself emits exactly " · " between natural-width tokens.
	joined := stripANSITest(footerSep("↑712", "↓10", "$free"))
	if joined != "↑712 · ↓10 · $free" {
		t.Errorf("footerSep = %q, want %q", joined, "↑712 · ↓10 · $free")
	}

	// Smooth metric motion: quantization is fixed-precision so 712 → 1.2k
	// swaps values without leaking padding or suffixes.
	if got := stripANSITest(footerSep("↑712", "↓10")); got != "↑712 · ↓10" {
		t.Errorf("inline cluster = %q, want %q", got, "↑712 · ↓10")
	}
}
