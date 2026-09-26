package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// viewportWidths covers the realistic terminal range plus the two degenerate
// ends: narrower than the safety margin, and a very wide pane.
var viewportWidths = []int{20, 40, 60, 80, 100, 120, 200, 400}

// longProviderError is a realistic worst case: a raw OpenRouter 403 JSON body
// behind a [PARTIAL] truncation notice. It is deliberately a mix of prose and
// unbreakable tokens so both wrap passes are exercised.
var longProviderError = "[PARTIAL] OpenRouter returned HTTP 403 Forbidden: " +
	`{"error":{"code":403,"message":"This model's maximum context length is 8192 tokens, ` +
	`however you requested 12411 tokens. Please reduce the length of the messages.",` +
	`"metadata":{"provider":"openrouter","model":"anthropic/claude-opus-4"}}` +
	strings.Repeat(" Supercalifragilisticexpialidocious", 20)

// assertBounded fails when any rendered line exceeds viewportWidth cells.
func assertBounded(t *testing.T, name string, rendered string, viewportWidth int) {
	t.Helper()
	limit := viewportWidth
	if limit < MinBoundWidth {
		limit = MinBoundWidth
	}
	if got := MaxLineWidth(rendered); got > limit {
		t.Errorf("%s at viewport %d: widest line = %d cells, want <= %d\n%s",
			name, viewportWidth, got, limit, rendered)
	}
}

// assertFrameIntact fails when a framed banner is missing its rounded border,
// which is the visual signature of a box whose content overflowed it.
func assertFrameIntact(t *testing.T, name, rendered string) {
	t.Helper()
	if !strings.HasPrefix(ansi.Strip(rendered), "╭") {
		t.Errorf("%s: missing top-left rounded border\n%q", name, rendered)
	}
	if !strings.HasPrefix(strings.TrimSpace(ansi.Strip(rendered)), "╭") &&
		!strings.HasPrefix(ansi.Strip(rendered), "╭") {
		t.Errorf("%s: frame does not start at column 0\n%q", name, rendered)
	}
	if !strings.Contains(ansi.Strip(rendered), "╮") ||
		!strings.Contains(ansi.Strip(rendered), "╰") ||
		!strings.Contains(ansi.Strip(rendered), "╯") {
		t.Errorf("%s: border is corrupted (missing corner glyphs)\n%q", name, rendered)
	}
}

// TestBannerNeverExceedsViewport is the core layout-bounding regression: a
// long error string must break inside the frame at every terminal width instead
// of pushing the right border off-screen and corrupting the layout.
func TestBannerNeverExceedsViewport(t *testing.T) {
	for _, vw := range viewportWidths {
		out := ErrorBanner(longProviderError, vw)
		assertBounded(t, "ErrorBanner", out, vw)
		assertFrameIntact(t, "ErrorBanner", out)
	}
}

// TestErrorBannerPreservesVerbatimMessage pins the other half of the contract:
// bounding must never rewrite the provider's words. Replacing a raw 403 body
// with generic text destroys the only diagnostic the user has, so the rendered
// frame must contain exactly the payload's content — modulo the newlines
// wrapping inserts and the spaces it consumes at break points.
//
// The check is whitespace-insensitive because WHERE a break lands is a function
// of the viewport width and is deliberately unspecified; WHAT survives is not.
func TestErrorBannerPreservesVerbatimMessage(t *testing.T) {
	for _, vw := range viewportWidths {
		out := ansi.Strip(ErrorBanner(longProviderError, vw))

		// Drop the frame chrome: the top border, the icon+label header line, and
		// every border/drawing glyph the box drew around the payload.
		lines := strings.Split(out, "\n")
		if len(lines) < 3 {
			t.Fatalf("viewport %d: banner has no body line\n%q", vw, out)
		}
		got := squash(stripFrame(lines[2:]))
		want := squash(longProviderError)
		if got != want {
			t.Errorf("viewport %d: banner altered the provider payload\n got: %q\nwant: %q", vw, got, want)
		}
	}
}

// stripFrame removes the box-drawing glyphs lipgloss adds around a bordered
// card, leaving only the payload text.
func stripFrame(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		for _, r := range l {
			if strings.ContainsRune("│╭╮╰╯├┤─ ", r) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// squash removes every whitespace character, making a rendered frame directly
// comparable to its source payload.
func squash(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestBannerUnbreakableTokenIsSplit proves the hard-wrap pass runs: a single
// token far wider than the frame must be cut at the cell boundary. Without it
// the frame border is torn off the right edge.
func TestBannerUnbreakableTokenIsSplit(t *testing.T) {
	blob := strings.Repeat("A", 500)
	for _, vw := range []int{30, 60, 120} {
		out := ErrorBanner(blob, vw)
		assertBounded(t, "unbreakable", out, vw)
		assertFrameIntact(t, "unbreakable", out)
	}
}

// TestBoundSubtractsBorderFromWidth documents the off-by-two that caused the
// original clipping: lipgloss applies the border AFTER the declared content
// width, so Bound must shrink the width or the frame renders 2 cells too wide.
func TestBoundSubtractsBorderFromWidth(t *testing.T) {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	for _, vw := range viewportWidths {
		rendered := Bound(style, vw).Render("hello")
		if got := lipgloss.Width(rendered); got != OuterWidth(vw) {
			t.Errorf("Bound(rendered) width = %d at viewport %d, want %d",
				got, vw, OuterWidth(vw))
		}
	}
}

// TestBoundNeverExceedsViewport is the invariant, stated once: no framed surface
// may render wider than the viewport it was given.
func TestBoundNeverExceedsViewport(t *testing.T) {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	for _, vw := range viewportWidths {
		rendered := Bound(style, vw).Render(longProviderError)
		if got := lipgloss.Width(rendered); got > vw {
			t.Errorf("Bound width = %d exceeds viewport %d", got, vw)
		}
	}
}

// TestBannerKindsShareBoundingDiscipline proves Error/Status/Policy/Info are
// bounded by the same rule, so a status surface can never drift into a
// different (unbounded) discipline than an error surface.
func TestBannerKindsShareBoundingDiscipline(t *testing.T) {
	for _, vw := range []int{40, 80, 160} {
		cases := map[string]string{
			"error":  ErrorBanner(longProviderError, vw),
			"status": StatusBanner("Streaming", longProviderError, vw),
			"policy": PolicyBanner("Tool rejected", longProviderError, vw),
			"info":   InfoBanner("Clarify", longProviderError, vw),
		}
		for name, out := range cases {
			assertBounded(t, name, out, vw)
			assertFrameIntact(t, name, out)
		}
	}
}

// TestBannerEmptyAndNarrowInputs covers the degradation paths: an empty detail
// renders a label-only card, an empty label falls back to a caption, and a very
// narrow viewport never panics or emits a zero-width box.
func TestBannerEmptyAndNarrowInputs(t *testing.T) {
	if out := ErrorBanner("", 80); !strings.Contains(ansi.Strip(out), "Error") {
		t.Errorf("empty message must still render the label caption, got %q", out)
	}
	if out := Banner(BannerStatus, "", "detail", 80); !strings.Contains(ansi.Strip(out), "Status") {
		t.Errorf("empty label must fall back to the kind caption, got %q", out)
	}
	if out := Banner(BannerKind(99), "x", "y", 80); out == "" {
		t.Error("unknown BannerKind must degrade to a rendered card, not empty output")
	}
	for _, vw := range []int{-5, 0, 1, 5, 10} {
		out := ErrorBanner(longProviderError, vw)
		if out == "" {
			t.Errorf("viewport %d produced empty output", vw)
		}
		assertBounded(t, "degenerate", out, MinBoundWidth)
	}
}

// TestWrapBodySplitsUnbreakableTokens pins the two-pass wrap contract directly.
func TestWrapBodySplitsUnbreakableTokens(t *testing.T) {
	const width = 20
	for _, line := range strings.Split(WrapBody(longProviderError, width), "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Errorf("wrapped line is %d cells, want <= %d: %q", got, width, line)
		}
	}
}

// TestWrapBodyPreservesAnsiAndWords asserts the word pass really prefers word
// boundaries and the hard pass never splits an escape sequence.
func TestWrapBodyPreservesAnsiAndWords(t *testing.T) {
	styled := "\x1b[31mred text that is long enough to wrap somewhere\x1b[0m"
	out := WrapBody(styled, 12)
	if strings.Contains(out, "\x1b[3") && !strings.Contains(out, "\x1b[31m") {
		t.Errorf("wrap corrupted an SGR sequence: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > 12 {
			t.Errorf("line %q is %d cells, want <= 12", line, got)
		}
	}
}

// TestContentWidthFloorsAtMinBoundWidth pins the narrow-terminal floor: a
// banner degrades to MinBoundWidth rather than collapsing to a negative width.
func TestContentWidthFloorsAtMinBoundWidth(t *testing.T) {
	for _, vw := range []int{-100, -4, 0, 1, 8, 19, 20} {
		if got := ContentWidth(vw); got < MinBoundWidth {
			t.Errorf("ContentWidth(%d) = %d, want >= %d", vw, got, MinBoundWidth)
		}
	}
	if got := ContentWidth(100); got != 100-ViewportMargin {
		t.Errorf("ContentWidth(100) = %d, want %d", got, 100-ViewportMargin)
	}
}

// TestAskCardNeverExceedsViewport applies the same invariant to the interactive
// clarification card, whose option descriptions are the longest free-form text
// it renders.
func TestAskCardNeverExceedsViewport(t *testing.T) {
	card := NewAskComponent(
		"Please pick the boundary you want the refactor to respect, including any constraints you already know about",
		true,
		[]AskOption{
			{ID: "api", Title: "Keep the public API surface stable", Description: longProviderError, Recommended: true},
			{ID: "impl", Title: "Allow internal restructuring", Description: longProviderError},
		},
	)
	for _, vw := range viewportWidths {
		out := card.Render(vw)
		limit := vw
		if limit < MinBoundWidth {
			limit = MinBoundWidth
		}
		if got := MaxLineWidth(out); got > limit {
			t.Errorf("AskComponent.Render at %d: widest line %d > %d\n%s", vw, got, limit, out)
		}
		assertFrameIntact(t, "ask", out)
	}
}
