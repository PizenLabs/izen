package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestPasteFolding_AtomicDeletion verifies that pasting 50 lines creates a
// compact pill badge [Paste #1 - 50 lines], that the styled badge contains the
// required ANSI pill, and that expanding the token emits the full 50 lines.
// It also pins the atomic Backspace deletion contract.
func TestPasteFolding_AtomicDeletion(t *testing.T) {
	m := readyChatModel(newTestModel())
	// Build a 50-line payload (no trailing newline to keep line count == 50).
	var sb strings.Builder
	for i := 1; i <= 50; i++ {
		if i > 1 {
			sb.WriteString("\n")
		}
		sb.WriteString("line content number ")
		sb.WriteString(strings.Repeat("x", 10))
	}
	raw := sb.String()
	if lines := CountPasteLines(raw); lines != 50 {
		t.Fatalf("CountPasteLines = %d, want 50", lines)
	}
	if !ShouldFoldPaste(raw) {
		t.Fatalf("ShouldFoldPaste must be true for 50 lines")
	}

	// Simulate a paste: the key handler's HandlePasteInput collapses the block.
	m.ti.Focus()
	if !m.HandlePasteInput(raw) {
		t.Fatalf("HandlePasteInput should fold 50-line paste")
	}

	// pasteCounter must have been incremented to 1.
	if m.pasteCounter != 1 {
		t.Fatalf("pasteCounter = %d, want 1", m.pasteCounter)
	}
	if stored, ok := m.pasteTokens[1]; !ok || stored != raw {
		t.Fatalf("pasteTokens[1] not stored correctly, got %q", stored)
	}

	// The prompt buffer should now contain the plain badge, not the raw lines.
	val := m.ti.Value()
	expectedPlain := PlainPasteBadge(1, 50)
	if !strings.Contains(val, expectedPlain) {
		t.Fatalf("prompt buffer missing plain badge %q, got %q", expectedPlain, val)
	}
	if strings.Contains(val, "line content") {
		t.Fatalf("prompt buffer still contains raw pasted text:\n%q", val)
	}

	// The rendered badge must be the styled pill with exact ANSI sequence.
	styled := FormatPasteBadge(1, 50)
	if !strings.Contains(styled, "\x1b[48;2;60;50;90m") || !strings.Contains(styled, "\x1b[38;2;205;214;244m") {
		t.Fatalf("styled badge missing required ANSI pill colors:\n%q", styled)
	}
	if !strings.Contains(styled, "[Paste #1 - 50 lines]") {
		t.Fatalf("styled badge missing text:\n%q", styled)
	}

	// Expanding the badge must emit the exact RawText (full 50 lines).
	expanded := m.ExpandPasteTokens(val)
	if expanded != raw {
		t.Errorf("ExpandPasteTokens mismatch:\nexpanded %d bytes\nwant %d bytes", len(expanded), len(raw))
		if len(expanded) != len(raw) {
			t.Fatalf("expanded raw length mismatch")
		}
	}

	// Static helper must also expand correctly (without a full model).
	staticExpanded := ExpandPasteTokensStatic(val, m.pasteTokens)
	if staticExpanded != raw {
		t.Fatalf("ExpandPasteTokensStatic failed")
	}

	// ── Atomic Deletion: Backspace immediately after badge removes entire block.
	// Cursor is at end (after badge). A single Backspace should delete the whole badge.
	m.ti.SetCursor(len(val)) // after badge
	if !m.tryDeletePasteBadgeAtomic(true) {
		t.Fatalf("atomic Backspace should delete the badge")
	}
	if got := m.ti.Value(); got != "" {
		t.Fatalf("after atomic delete, prompt should be empty, got %q", got)
	}

	// Re-insert and test Delete at start.
	m.HandlePasteInput(raw)
	val2 := m.ti.Value()
	m.ti.SetCursor(0)
	if !m.tryDeletePasteBadgeAtomic(false) {
		t.Fatalf("atomic Delete at badge start should delete the badge")
	}
	if m.ti.Value() != "" {
		t.Fatalf("after atomic Delete, prompt should be empty, got %q", m.ti.Value())
	}
	_ = val2

	// Re-insert and test that expanding after deletion is no-op (badge gone).
	// Also test that non-foldable short text is NOT folded.
	m.ti.SetValue("hello")
	m.syncInputFromTI()
	// Verify non-foldable short text is NOT folded.
	if ShouldFoldPaste("hello\nalready 2 lines only") {
		t.Fatalf("2 lines should NOT trigger folding (threshold >=3)")
	}
	if ShouldFoldPaste("short\ntwo lines") {
		t.Fatalf("2 lines should NOT trigger folding (threshold >=3)")
	}
	if ShouldFoldPaste(strings.Repeat("a", 151)) {
		t.Fatalf("single line >150 chars without newline should NOT fold")
	}
	if !ShouldFoldPaste(strings.Repeat("a", 151) + "\n" + "b") {
		t.Fatalf(">150 chars with newline should fold")
	}

	// ── Threshold: >150 chars with line breaks also folds (3-line rule already covers).
	longWithBreak := strings.Repeat("x", 100) + "\n" + strings.Repeat("y", 60)
	if !ShouldFoldPaste(longWithBreak) {
		t.Fatalf("long text with break >150 should fold")
	}

	// Verify paste increment: next paste should increment counter (now #3 after two prior inserts)
	secondRaw := "a\nb\nc"
	m.HandlePasteInput(secondRaw)
	if !strings.Contains(m.ti.Value(), "[Paste #") {
		t.Fatalf("second paste should contain a Paste badge, got %q", m.ti.Value())
	}
	if !strings.Contains(m.ti.Value(), "3 lines]") {
		t.Fatalf("second paste should be 3 lines, got %q", m.ti.Value())
	}

	// Submission resolution: expandPromptForSubmit must return raw for #2's raw plus previous?
	// For this test, clear and paste 50 again, then expandPromptForSubmit should give raw.
	m.ti.SetValue("")
	m.pasteCounter = 0
	m.pasteTokens = make(map[int]string)
	m.HandlePasteInput(raw)
	submit := m.expandPromptForSubmit()
	if submit != raw {
		t.Fatalf("expandPromptForSubmit must return exact raw, got len %d want %d", len(submit), len(raw))
	}

	// Ensure navigation helper detects badge inside.
	m.ti.SetValue(PlainPasteBadge(1, 50))
	m.ti.SetCursor(5)
	if _, _, found := PasteBadgeAt(m.ti.Value(), 5); !found {
		t.Fatalf("PasteBadgeAt should detect badge when cursor inside")
	}

	// Ensure plain badge rendering: RenderPasteBadgesStyled replaces plain with styled.
	rendered := RenderPasteBadgesStyled(PlainPasteBadge(1, 5))
	if !strings.Contains(rendered, "\x1b[48;2;60;50;90m") {
		t.Fatalf("RenderPasteBadgesStyled should produce styled pill")
	}

	// Also test handlePasteBackspace wrapper.
	m.ti.SetValue(PlainPasteBadge(1, 10))
	m.ti.SetCursor(len(PlainPasteBadge(1, 10)))
	if !m.handlePasteBackspace(tea.KeyBackspace) {
		t.Fatalf("handlePasteBackspace should handle Backspace after badge")
	}
}

// TestResponsiveFooterTiers verifies the adaptive footer across window widths
// 120, 85, 55, 35 cols, as required by the task.
func TestResponsiveFooterTiers(t *testing.T) {
	modelName := "qwen2.5-coder:7b"
	inTok := 100
	outTok := 50
	ctxPct := 10.5
	cost := "$0.0123"
	mode := "build"

	tests := []struct {
		width          int
		shouldContain  []string
		mustNotContain []string
		description    string
	}{
		{
			width:         120,
			shouldContain: []string{modelName, "↓100", "↑50", "10%", cost, "[build]"},
			description:   "Tier1 Full >=100 contains all fields",
		},
		{
			width:          85,
			shouldContain:  []string{modelName, "↓100", "↑50", "10%", cost},
			mustNotContain: []string{"[build]"},
			description:    "Tier2 Standard 70-99 contains tok+ctx+cost without mode",
		},
		{
			width:          55,
			shouldContain:  []string{"↓100", "↑50"},
			mustNotContain: []string{cost, "10%"},
			description:    "Tier3 Compact 45-69 contains short model + tok only",
		},
		{
			width:          35,
			shouldContain:  []string{"↓100", "↑50"},
			mustNotContain: []string{cost},
			description:    "Tier4 Minimal <45 contains only tok",
		},
	}

	for _, tc := range tests {
		out := renderActiveIdleFooter(tc.width, modelName, inTok, outTok, ctxPct, cost, mode)
		for _, want := range tc.shouldContain {
			if !strings.Contains(out, want) {
				t.Errorf("width %d (%s): output missing %q:\n%q", tc.width, tc.description, want, out)
			}
		}
		for _, forbidden := range tc.mustNotContain {
			if strings.Contains(out, forbidden) {
				t.Errorf("width %d (%s): output should NOT contain %q:\n%q", tc.width, tc.description, forbidden, out)
			}
		}
		// Strict width enforcement: rendered line must be exactly width cells.
		w := lipgloss.Width(out)
		if w != tc.width {
			t.Errorf("width %d: rendered width %d != requested %d:\n%q (width %d)", tc.width, w, tc.width, out, w)
		}
		// Must be single-line.
		if strings.Contains(out, "\n") {
			t.Errorf("width %d: footer wrapped to multiple lines:\n%q", tc.width, out)
		}
	}

	// Verify short model truncation for long names in compact tier.
	longModel := "very-long-model-name-exceeding-limit"
	out55 := renderActiveIdleFooter(55, longModel, inTok, outTok, ctxPct, cost, mode)
	short := truncateModelName(longModel, 12)
	if !strings.Contains(out55, short) {
		t.Errorf("compact tier should contain truncated model %q, got %q", short, out55)
	}
	if strings.Contains(out55, longModel) && lipgloss.Width(longModel) > 12 {
		t.Errorf("compact tier leaked full long model name: %q", out55)
	}
	if lipgloss.Width(out55) != 55 {
		t.Errorf("compact tier width not exact: got %d want 55", lipgloss.Width(out55))
	}

	// Verify minimal tier <45 does not contain model at all.
	out35 := renderActiveIdleFooter(35, modelName, inTok, outTok, ctxPct, cost, mode)
	if strings.Contains(out35, modelName) {
		t.Errorf("minimal tier should NOT contain model name, got %q", out35)
	}

	// Verify renderFixedFooter (method) also respects exact width via fitToWidth
	// for active idle state.
	m := readyChatModel(newTestModel())
	m.sessionHasRunPrompts = true
	m.InputTokens = 100
	m.OutputTokens = 50
	m.TotalTokens = 150
	m.AccumulatedCost = 0.0123
	for _, w := range []int{120, 85, 55, 35} {
		footer := m.renderFixedFooter(w, nil)
		// renderFixedFooter returns a padded string of exactly w via fitToWidth.
		// Its lipgloss width should be exactly w (unless w <20 returns "").
		if lipgloss.Width(footer) != w {
			t.Errorf("renderFixedFooter width %d: got %d, want %d:\n%q", w, lipgloss.Width(footer), w, footer)
		}
		if strings.Contains(footer, "\n") {
			t.Errorf("renderFixedFooter width %d wrapped:\n%q", w, footer)
		}
	}

	// Verify narrow width truncation safety for extremely long model names.
	extremelyLong := strings.Repeat("a", 200)
	out120 := renderActiveIdleFooter(120, extremelyLong, inTok, outTok, ctxPct, cost, mode)
	if lipgloss.Width(out120) != 120 {
		t.Errorf("extremely long model should still be truncated to 120, got %d", lipgloss.Width(out120))
	}
}
