package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ui/status"
	"github.com/charmbracelet/lipgloss"
)

// TestMinimalistTokenFormatting pins INVARIANT 1: telemetry formats
// exclusively with glyph prefixes ↑<count> / ↓<count> and zero "in"/"out"
// text suffixes.
func TestMinimalistTokenFormatting(t *testing.T) {
	// Status formatters.
	for _, got := range []string{
		status.FormatUsageValues(631, 95),
		status.FormatUsageContext(631, 95, 726, 128000),
	} {
		if !strings.Contains(got, "↑631") || !strings.Contains(got, "↓95") {
			t.Errorf("minimalist glyph missing in %q", got)
		}
		if strings.Contains(got, " in") || strings.Contains(got, " out") {
			t.Errorf("leaked in/out suffix in %q", got)
		}
	}
	// Flex-flow natural widths: zero trailing padding inside metric slots.
	inSlot := statusArrowIn(status.FormatTokens(631))
	outSlot := statusArrowOut(status.FormatTokens(95))
	if inSlot != "↑631" {
		t.Errorf("input slot must be natural width without padding, got %q", inSlot)
	}
	if outSlot != "↓95" {
		t.Errorf("output slot must be natural width without padding, got %q", outSlot)
	}
	if lipgloss.Width(inSlot) != 4 || lipgloss.Width(outSlot) != 3 {
		t.Errorf("natural slot widths wrong: in=%q out=%q", inSlot, outSlot)
	}
	if strings.HasSuffix(inSlot, " ") || strings.HasSuffix(outSlot, " ") {
		t.Errorf("trailing space padding forbidden: in=%q out=%q", inSlot, outSlot)
	}

	// Footer surfaces (both states) carry zero suffixes.
	m := readyChatModel(newTestModel())
	m.sessionHasRunPrompts = true
	m.InputTokens = 631
	m.OutputTokens = 95
	m.TotalTokens = 726
	idle := stripANSIFooter(m.renderFixedFooter(120, nil))
	if strings.Contains(idle, "↑631 in") || strings.Contains(idle, "↓95 out") {
		t.Errorf("idle footer leaked suffix:\n%q", idle)
	}
	m2 := readyChatModel(newTestModel())
	m2.state = StateProcessing
	m2.streaming = true
	m2.spinnerFrame = 1
	m2.streamStartTime = time.Now().Add(-10 * time.Second)
	m2.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m2.setStageMetrics(0, 0, 95)
	exec := stripANSIFooter(m2.renderFixedFooter(100, nil))
	if strings.Contains(exec, " in") && strings.Contains(exec, "↑") {
		// Allow "Generating..." prose but never "↑N in" / "↓N out".
		if strings.Contains(exec, "↑0 in") || strings.Contains(exec, "↓95 out") || strings.Contains(exec, "↓128 out") {
			t.Errorf("executing footer leaked suffix:\n%q", exec)
		}
	}
}

// TestSessionMetricAccumulation pins INVARIANT 2: session totals grow
// monotonically (Base + Live during streaming, committed on turn complete).
func TestSessionMetricAccumulation(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.sessionHasRunPrompts = true
	// Prior turns baseline.
	m.InputTokens = 631
	m.OutputTokens = 95
	m.TotalTokens = 726

	// During streaming: session = baseline + live turn.
	m.state = StateProcessing
	m.streaming = true
	m.streamBaseInputTokens = 100
	m.streamLiveTokens = 50
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m.setStageMetrics(0, 0, 50)

	sess := m.snapshotSessionMetrics()
	if sess.BaseInputTokens != 631 || sess.BaseOutputTokens != 95 {
		t.Fatalf("base = %d/%d, want 631/95", sess.BaseInputTokens, sess.BaseOutputTokens)
	}
	if got := sess.TotalInput(); got != 731 {
		t.Errorf("TotalInput = %d, want 631+100=731", got)
	}
	if got := sess.TotalOutput(); got < 145 {
		t.Errorf("TotalOutput = %d, want >= 95+50=145", got)
	}
	if got := m.sessionDisplayInput(); got != 731 {
		t.Errorf("sessionDisplayInput = %d, want 731", got)
	}
	if got := m.sessionDisplayOutput(); got < 145 {
		t.Errorf("sessionDisplayOutput = %d, want >= 145", got)
	}

	// On turn complete: commit preserves monotonic growth.
	m.commitSessionTurn(100, 50)
	if m.InputTokens != 731 || m.OutputTokens != 145 {
		t.Fatalf("after commit = %d/%d, want 731/145", m.InputTokens, m.OutputTokens)
	}
	// Second turn accumulates further, never resets.
	m.streamBaseInputTokens = 20
	m.streamLiveTokens = 10
	m.setStageMetrics(0, 0, 10)
	sess2 := m.snapshotSessionMetrics()
	if sess2.TotalInput() != 751 {
		t.Errorf("second-turn TotalInput = %d, want 751", sess2.TotalInput())
	}
	m.commitSessionTurn(20, 10)
	if m.InputTokens != 751 || m.OutputTokens != 155 {
		t.Fatalf("after second commit = %d/%d, want 751/155", m.InputTokens, m.OutputTokens)
	}
}

// TestFooterSlotGeometryLocking pins the flex-flow quantization invariant:
// token formatters produce fixed-precision strings with natural widths and
// zero trailing padding, and both footer states render exact-width lines
// with the action badge pinned right.
func TestFooterSlotGeometryLocking(t *testing.T) {
	// Quantization: fixed-precision, natural width, no trailing padding.
	if got := status.FormatTokens(712); got != "712" {
		t.Errorf("FormatTokens(712) = %q, want %q", got, "712")
	}
	if got := status.FormatTokens(12400); got != "12k" {
		t.Errorf("FormatTokens(12400) = %q, want %q", got, "12k")
	}
	if got := status.FormatTokens(1200); got != "1.2k" {
		t.Errorf("FormatTokens(1200) = %q, want %q", got, "1.2k")
	}
	for _, s := range []string{
		statusArrowIn(status.FormatTokens(12400)),
		statusArrowOut(status.FormatTokens(1800)),
	} {
		if strings.HasSuffix(s, " ") {
			t.Errorf("trailing padding forbidden in %q", s)
		}
	}

	// Both states render exact-width flex lines with a pinned right badge.
	idle := readyChatModel(newTestModel())
	idle.sessionHasRunPrompts = true
	idle.InputTokens = 631
	idle.OutputTokens = 95
	idle.TotalTokens = 726
	idleStr := stripANSIFooter(idle.renderFixedFooter(120, nil))

	exec := readyChatModel(newTestModel())
	exec.state = StateProcessing
	exec.streaming = true
	exec.spinnerFrame = 1
	exec.streamStartTime = time.Now().Add(-10 * time.Second)
	exec.InputTokens = 631
	exec.OutputTokens = 95
	exec.streamBaseInputTokens = 0
	exec.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	exec.setStageMetrics(0, 0, 10)
	execStr := stripANSIFooter(exec.renderFixedFooter(120, nil))

	if lipgloss.Width(idleStr) != 120 {
		t.Errorf("idle flex line width = %d, want 120:\n%q", lipgloss.Width(idleStr), idleStr)
	}
	if lipgloss.Width(execStr) != 120 {
		t.Errorf("exec flex line width = %d, want 120:\n%q", lipgloss.Width(execStr), execStr)
	}
	if !strings.HasSuffix(strings.TrimSpace(idleStr), menuBadge) {
		t.Errorf("idle right badge must pin ^P menu:\n%q", idleStr)
	}
	if !strings.HasSuffix(strings.TrimSpace(execStr), stopBadge) {
		t.Errorf("exec right badge must pin ^C stop:\n%q", execStr)
	}
	// Zero floating dots: tight single-space separators only.
	for _, s := range []string{idleStr, execStr} {
		if strings.Contains(s, "  ·") || strings.Contains(s, "·  ") {
			t.Errorf("floating separator gap in:\n%q", s)
		}
	}
}
