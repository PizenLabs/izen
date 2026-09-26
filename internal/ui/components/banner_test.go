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

// assertFrameIntact fails when a framed box is missing its rounded border,
// which is the visual signature of a box whose content overflowed it. It is
// retained for the interactive surfaces that are deliberately still boxes
// (approval gates, the Ask card, code fences); notices use assertFrameless.
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

// assertFrameless fails when a notice draws any part of a full enclosure: a
// corner/tee glyph, or a row wrapped on both edges by a vertical border. A soft
// left accent line (`│`) is explicitly allowed — that is the notice's
// deliberate visual anchor, not a frame.
func assertFrameless(t *testing.T, name, rendered string) {
	t.Helper()
	plain := ansi.Strip(rendered)
	for _, glyph := range []string{"╭", "╮", "╰", "╯", "┌", "┐", "└", "┘", "├", "┤", "┬", "┴", "┼"} {
		if strings.Contains(plain, glyph) {
			t.Errorf("%s: notice drew box glyph %q; the muted notice system is frameless\n%q",
				name, glyph, plain)
		}
	}
	for i, line := range strings.Split(plain, "\n") {
		l := strings.TrimRight(line, " ")
		if len(l) < 2 {
			continue
		}
		left, right := rune(l[0]), rune(l[len(l)-1])
		if isVerticalEdge(left) && isVerticalEdge(right) {
			t.Errorf("%s: row %d is enclosed on both edges — not a frameless notice\n%q",
				name, i, line)
		}
	}
}

func isVerticalEdge(r rune) bool {
	switch r {
	case '│', '┃', '║', '|':
		return true
	}
	return false
}

// TestBannerNeverExceedsViewport is the core layout-bounding regression: a
// long error string must break inside the frame at every terminal width instead
// of pushing the right border off-screen and corrupting the layout.
func TestBannerNeverExceedsViewport(t *testing.T) {
	for _, vw := range viewportWidths {
		out := ErrorBanner(longProviderError, vw)
		assertBounded(t, "ErrorBanner", out, vw)
		assertFrameless(t, "ErrorBanner", out)
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

		// Drop the notice chrome: the badge line, and every accent-line/space
		// glyph the notice drew alongside the payload. The frameless body starts
		// on line 1 (line 0 is the `✖ [ERROR]` badge).
		lines := strings.Split(out, "\n")
		if len(lines) < 2 {
			t.Fatalf("viewport %d: banner has no body line\n%q", vw, out)
		}
		got := squash(stripFrame(lines[1:]))
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
		assertFrameless(t, "unbreakable", out)
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

// ── ANSI-Safe Card Bounding: the frame-size regression ───────────────────────

// TestUsableWidthIsTheSpecifiedArithmetic pins the budget as the literal formula
// it is, so the two terms cannot drift apart silently:
//
//	usableWidth(style, paneWidth) = paneWidth - style.GetHorizontalFrameSize() - 2
//
// The trailing 2 is ViewportMargin — the one-cell-per-side gutter that keeps a
// frame off the pane's last drawable column. The frame term is read off the
// style rather than assumed, because a style that grows a margin is the change
// most likely to be made later and the one least likely to be re-audited here.
//
// It is worth stating separately from the structural tests below because those
// assert RELATIVE properties (a bigger frame buys less budget) and would all
// still pass with a different constant. This one fails if the constant changes.
func TestUsableWidthIsTheSpecifiedArithmetic(t *testing.T) {
	const gutter = 2
	styles := map[string]lipgloss.Style{
		"border only":     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()),
		"border + pad 1":  lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		"border + pad 2":  lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 2),
		"border + margin": lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Margin(0, 1),
		"heavy margin":    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Margin(0, 3),
	}
	for name, st := range styles {
		for _, pane := range []int{20, 40, 60, 80, 120, 200} {
			want := pane - st.GetHorizontalFrameSize() - gutter
			if got := usableWidth(st, pane); got != want {
				t.Errorf("%s at pane %d: usableWidth = %d, want %d (= pane - frame(%d) - %d)",
					name, pane, got, want, st.GetHorizontalFrameSize(), gutter)
			}
		}
	}
	// The gutter is two cells TOTAL, one per side, so a frame's outer edge lands
	// strictly inside the pane at every width where the budget is not floored.
	if gutter != ViewportMargin {
		t.Errorf("the budget reserves %d cells per side; the arithmetic test assumes %d",
			ViewportMargin, gutter)
	}
}

// TestUsableWidthAccountsForTheWholeFrame is the core regression. usableWidth
// used to subtract only the BORDER from the content budget, so it returned the
// same number for every style:
//
//	usableWidth(border)            == 74   at viewport 80
//	usableWidth(border+pad)        == 74
//	usableWidth(border+pad+margin) == 74
//
// Lipgloss, however, adds the border AND the margins OUTSIDE the declared
// width, so a card carrying a margin rendered ViewportMargin-minus-the-margin
// cells too wide — the one-cell right-border clip on [PARTIAL] warning cards.
// The budget has to be reduced by the style's whole horizontal frame.
func TestUsableWidthAccountsForTheWholeFrame(t *testing.T) {
	const vw = 80
	// Ordered by increasing frame size, so the sweep below is a monotone check
	// rather than a set comparison.
	styles := []struct {
		name  string
		style lipgloss.Style
	}{
		{"no frame", lipgloss.NewStyle()},
		{"padding only", lipgloss.NewStyle().Padding(0, 1)},
		{"border only", lipgloss.NewStyle().Border(lipgloss.RoundedBorder())},
		{"border + pad 1", lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)},
		{"border + pad 2", lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 2)},
		{"border + margin", lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Margin(0, 1)},
		{"heavy margin", lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Margin(0, 3)},
	}

	first := true
	prevFrame, prevBudget := 0, 0
	for _, c := range styles {
		frame := c.style.GetHorizontalFrameSize()
		want := ContentWidth(vw) - frame
		got := usableWidth(c.style, vw)

		if got != want {
			t.Errorf("%s (frame=%d): usableWidth(%d) = %d, want %d",
				c.name, frame, vw, got, want)
		}
		// The load-bearing property, and the one the bug violated: a style with
		// a STRICTLY bigger frame gets a STRICTLY smaller budget. (Equal frame
		// sizes must yield equal budgets — a border and one cell of padding
		// happen to measure the same.) The old code returned one constant for
		// every style, so this is the assertion it fails.
		if !first && frame > prevFrame && got >= prevBudget {
			t.Errorf("%s: frame %d bought budget %d, but the smaller frame %d already got %d — "+
				"a larger frame must never buy more width",
				c.name, frame, got, prevFrame, prevBudget)
		}
		prevFrame, prevBudget, first = frame, got, false
	}
}

// TestBoundNeverOverflowsAnyFrame is the DoD stated as an invariant, in the
// form the specification asks for: for every frame shape and every viewport,
// the rendered banner's printable width is strictly less than the viewport.
//
// The "<" is deliberate. lipgloss's own Bound test asserts an exact outer width
// for a border-only style, but a card that merely FITS still consumes the last
// cell of the pane, and a pane that has no last spare cell (a vertical
// scrollbar, a tmux pane divider) turns "exactly full" into a torn frame. The
// safety gutter is what buys that cell back.
func TestBoundNeverOverflowsAnyFrame(t *testing.T) {
	styles := map[string]lipgloss.Style{
		"border only":     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()),
		"border + pad 1":  lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		"border + pad 2":  lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 2),
		"border + margin": lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Margin(0, 1),
		"heavy margin":    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Margin(0, 3),
	}
	for name, st := range styles {
		for _, vw := range viewportWidths {
			// Below the documented readability floor Bound deliberately stops
			// shrinking and lets the frame scroll horizontally. Asserting
			// "fits" there would be asserting the opposite of the design: a
			// usable card is strictly better than a card of three cells.
			if usableWidth(st, vw) < MinBoundWidth-BorderCells {
				continue
			}
			rendered := Bound(st, vw).Render(longProviderError)
			if got := MaxLineWidth(rendered); got >= vw {
				t.Errorf("%s at viewport %d: printable width %d, want < %d\n%s",
					name, vw, got, vw, rendered)
			}
			// Per-line, not just overall: one over-wide line is what tears a
			// border, and an average would hide it.
			for i, line := range strings.Split(ansi.Strip(rendered), "\n") {
				if got := ansi.StringWidth(line); got >= vw {
					t.Errorf("%s at viewport %d: line %d is %d cells, want < %d\n%q",
						name, vw, i, got, vw, line)
				}
			}
		}
	}
}

// TestBannerPrintableWidthIsUnderViewport applies the same invariant to the
// public constructors with the [PARTIAL] payload that triggered the original
// report, so the regression is pinned against the real input rather than a
// synthetic one.
func TestBannerPrintableWidthIsUnderViewport(t *testing.T) {
	partial := "[PARTIAL] Response truncated: the model's output exceeded the " +
		"maximum context length and the remainder was dropped. " +
		`{"error":{"code":400,"message":"context_length_exceeded"}}` +
		strings.Repeat(" Supercalifragilisticexpialidocious", 12)

	for _, vw := range []int{40, 60, 80, 100, 120, 160, 200} {
		for name, out := range map[string]string{
			"ErrorBanner":  ErrorBanner(partial, vw),
			"StatusBanner": StatusBanner("Streaming", partial, vw),
			"PolicyBanner": PolicyBanner("Rejected", partial, vw),
			"InfoBanner":   InfoBanner("Note", partial, vw),
		} {
			if got := MaxLineWidth(out); got >= vw {
				t.Errorf("%s at viewport %d: printable width %d, want < %d", name, vw, got, vw)
			}
		}
	}
}

// TestFrameSizeIsNotHardcodedToTwo is the narrow form of the regression: a
// hardcoded `return ContentWidth(vw) - 2` is the exact defect, and it is
// invisible for the one style the original tests used. Each case below has a
// DIFFERENT frame size and must therefore get a different budget.
func TestFrameSizeIsNotHardcodedToTwo(t *testing.T) {
	budgets := map[int]bool{}
	for _, cells := range []int{0, 1, 2, 3, 4, 6, 8} {
		st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, cells)
		budgets[usableWidth(st, 100)] = true
	}
	if len(budgets) < 4 {
		t.Errorf("usableWidth returned %d distinct budgets for 7 frame sizes; "+
			"it is collapsing them to a constant", len(budgets))
	}
}

// TestBannerWithMarginStaysBounded is the end-to-end form: a real card built on
// a margin-carrying style, at a 40-cell split-pane, must not tear.
//
// The corner check is margin-tolerant on purpose. A card with Margin(0,1)
// starts at column 1 by design, so the shared assertFrameIntact — which
// requires the frame at column 0 — does not apply; what must hold is that all
// four corners are present and no line exceeds the pane.
func TestBannerWithMarginStaysBounded(t *testing.T) {
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#fab387")).
		Padding(0, 1).
		Margin(0, 1)
	for _, vw := range []int{40, 60, 80, 120} {
		inner := max(InnerWidth(vw, frame), 1)
		body := lipgloss.NewStyle().Width(inner).Render(WrapBody(longProviderError, inner))
		rendered := Bound(frame, vw).Render(body)

		if got := MaxLineWidth(rendered); got >= vw {
			t.Errorf("margin-carrying card at viewport %d: printable width %d, want < %d\n%s",
				vw, got, vw, rendered)
		}
		plain := ansi.Strip(rendered)
		for _, corner := range []string{"╭", "╮", "╰", "╯"} {
			if !strings.Contains(plain, corner) {
				t.Errorf("margin-carrying card at viewport %d: missing corner %q\n%s", vw, corner, plain)
			}
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
			assertFrameless(t, name, out)
		}
	}
}

// TestBannerEmptyAndNarrowInputs covers the degradation paths: an empty detail
// renders a label-only card, an empty label falls back to a caption, and a very
// narrow viewport never panics or emits a zero-width box.
func TestBannerEmptyAndNarrowInputs(t *testing.T) {
	if out := ErrorBanner("", 80); !strings.Contains(ansi.Strip(out), "ERROR") {
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

// ── Frameless Muted Notice System ─────────────────────────────────────────────

// TestRenderMinimalNoticeIsFramelessAndBadged pins the two signature properties
// of the notice system: a compact coloured prefix badge, and no enclosure at
// all — no corners, no right border, no bottom rule.
func TestRenderMinimalNoticeIsFramelessAndBadged(t *testing.T) {
	for _, vw := range viewportWidths {
		out := RenderMinimalNotice("[PARTIAL]", longProviderError, NoticeWarning, vw)
		plain := ansi.Strip(out)
		if !strings.Contains(plain, "▲ [PARTIAL]") {
			t.Errorf("viewport %d: badge missing from notice\n%q", vw, plain)
		}
		if !strings.Contains(plain, "│") {
			t.Errorf("viewport %d: notice has no accent line to anchor its body\n%q", vw, plain)
		}
		assertFrameless(t, "notice", out)
		assertBounded(t, "notice", out, vw)
	}
}

// TestRenderMinimalNoticeDerivesBadge: an empty prefix promotes the message's
// own leading `[TAG]` into the badge and strips it from the body, so a
// `[PARTIAL] …` message never repeats its marker.
func TestRenderMinimalNoticeDerivesBadge(t *testing.T) {
	out := ansi.Strip(RenderMinimalNotice("", "[PARTIAL] response truncated by length", NoticeWarning, 120))
	if !strings.Contains(out, "▲ [PARTIAL]") {
		t.Errorf("derived badge missing:\n%s", out)
	}
	if strings.Count(out, "[PARTIAL]") != 1 {
		t.Errorf("the tag was repeated instead of promoted:\n%s", out)
	}
	// A decorator marker like `[!]` is left in the body; the level caption wins.
	out = ansi.Strip(RenderMinimalNotice("", "[!] WARNING: big repo", NoticeWarning, 120))
	if !strings.Contains(out, "▲ [WARNING]") {
		t.Errorf("decorator marker should fall back to the level caption:\n%s", out)
	}
	if !strings.Contains(out, "[!]") {
		t.Errorf("decorator marker should stay in the body:\n%s", out)
	}
}

// TestRenderMinimalNoticeWrapsDownToFortyColumns is the DoD on the notice
// itself: at the narrowest realistic pane every line fits, with no clipped word.
func TestRenderMinimalNoticeWrapsDownToFortyColumns(t *testing.T) {
	payload := longProviderError + " " + strings.Repeat("Supercalifragilisticexpialidocious", 30)
	for _, vw := range []int{40, 50, 56, 60, 80, 120} {
		out := RenderMinimalNotice("[ERROR]", payload, NoticeError, vw)
		if got := MaxLineWidth(out); got > vw {
			t.Errorf("viewport %d: notice width %d exceeds the pane\n%s", vw, got, out)
		}
	}
}

// TestRenderMinimalNoticeEmptyBodyIsBadgeOnly: an empty body must not render a
// dangling accent line, and an unknown level must degrade rather than panic.
func TestRenderMinimalNoticeEmptyBodyIsBadgeOnly(t *testing.T) {
	out := ansi.Strip(RenderMinimalNotice("[OK]", "", NoticeSuccess, 80))
	if strings.Contains(out, "│") {
		t.Errorf("empty body drew an accent line: %q", out)
	}
	if !strings.Contains(out, "[OK]") {
		t.Errorf("badge missing: %q", out)
	}
	if got := RenderMinimalNotice("x", "y", NoticeLevel(99), 80); got == "" {
		t.Error("unknown level rendered nothing")
	}
}
