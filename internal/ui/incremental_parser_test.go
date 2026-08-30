package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestInlineMarkdown_NumberedListBoldParsed pins the numbered-list inline
// markdown contract: the content after "N." is ALWAYS routed through the inline
// pipeline, `**Text:**` renders "Text:" as a single bold unit, and ZERO raw `**`
// residue survives. Both the unified deterministic engine (layout_builder
// aiBlockRenderer path) and the streaming incremental parser must agree.
func TestInlineMarkdown_NumberedListBoldParsed(t *testing.T) {
	input := "1. **Simplicity and Ease of Use:**"

	unified := renderDeterministicInlineMarkdown(input, 80)
	assertNumberedBold(t, "unified", unified)

	parser := NewIncrementalStreamParser(80)
	streamed := parser.ProcessChunk(input + "\n")
	if len(streamed) == 0 {
		t.Fatalf("streaming parser emitted no lines for %q", input)
	}
	assertNumberedBold(t, "streaming", strings.Join(streamed, "\n"))

	// Multi-digit + indented items must route through the same pipeline so
	// their markers never leak raw "N." text.
	multi := renderDeterministicInlineMarkdown("  10. **Verified:** scope", 80)
	stripped := ansi.Strip(multi)
	if !strings.HasPrefix(strings.TrimSpace(stripped), "10.") {
		t.Errorf("multi-digit marker not preserved: %q", stripped)
	}
	if !strings.Contains(multi, "Verified:") {
		t.Errorf("multi-digit bold-colon content missing: %q", stripped)
	}
	if strings.Contains(multi, "**") {
		t.Errorf("multi-digit bold leaked ** residue: %q", stripped)
	}
}

func assertNumberedBold(t *testing.T, label, rendered string) {
	t.Helper()
	if !strings.Contains(rendered, "\x1b[1m") {
		t.Errorf("%s: rendered output lacks bold escape \\x1b[1m:\n%q", label, rendered)
	}
	// The colon must be inside the bold segment — "Simplicity and Ease of Use:"
	// survives as a contiguous substring, never split by an ANSI reset.
	if !strings.Contains(rendered, "Simplicity and Ease of Use:") {
		t.Errorf("%s: bold text (with colon) split or lost:\n%q", label, rendered)
	}
	if strings.Contains(rendered, "**") {
		t.Errorf("%s: raw ** markdown residue leaked:\n%q", label, rendered)
	}
	if strings.Contains(ansi.Strip(rendered), "**") {
		t.Errorf("%s: raw ** residue in stripped output:\n%q", label, ansi.Strip(rendered))
	}
}
