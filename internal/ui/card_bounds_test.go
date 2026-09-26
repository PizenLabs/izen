package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/ui/components"
)

// ── Bounded framed surfaces ──────────────────────────────────────────────────
//
// Every framed (bordered) card in the TUI is a pure function of (payload,
// viewport width), so its width is fully determined at render time and can be
// asserted without a terminal. The invariant is the same for all of them:
//
//	no rendered line may exceed the viewport width, and the left and right
//	borders must both survive
//
// The second half is what makes the first meaningful. A card that "fits" by
// dropping its right border is not bounded, it is corrupt.

// boundedWidths spans a normal pane, a split pane, a wide terminal, and the
// narrow end of the range a terminal can be resized to.
var boundedWidths = []int{20, 40, 60, 80, 100, 120, 200}

// partialNotice is the [PARTIAL] truncation warning verbatim. It is the longest
// fixed string the UI renders (~190 cells on one unwrapped line), which makes it
// the canonical test subject for box bounding: it exceeds every realistic pane
// width, and it carries a quoted `finish_reason: "length"` payload whose
// punctuation the word-wrapper must break on.
const partialNotice = "[PARTIAL] The response hit the provider's max_tokens limit and was cut off " +
	"mid-generation (finish_reason: \"length\", EvidenceState.PARTIAL). Increase max_tokens in the " +
	"provider config to allow longer responses."

// rawProviderBody is the payload that historically blew a frame: a raw
// OpenRouter 403 JSON body behind the error banner. It is prose interleaved
// with unbreakable JSON tokens, so it exercises both wrap passes.
var rawProviderBody = `{"error":{"code":403,"message":"This model's maximum context length is 8192 tokens, ` +
	`however you requested 12411 tokens. Please reduce the length of the messages.",` +
	`"metadata":{"provider":"openrouter","model":"anthropic/claude-opus-4"}}` +
	strings.Repeat(" Supercalifragilisticexpialidocious", 20)

// unbreakablePayload is the worst case for wrapping: no whitespace, no
// separator, wider than any terminal. Without a hard-wrap pass this single token
// is what tears a frame's right border off the screen.
func unbreakablePayload() string { return strings.Repeat("A", 500) }

// isBoxRune reports whether r is a box-drawing glyph. The border style differs
// per surface (rounded, double, square) and a few frames legitimately draw no
// border at all, so the assertions check the SHAPE of the frame — closed on both
// edges, made of box glyphs — rather than one hardcoded corner pair.
func isBoxRune(r rune) bool {
	switch r {
	case '│', '┃', '║', '|': // vertical edges
		return true
	}
	// Box-drawing block U+2500..U+257F covers every horizontal, corner and tee
	// glyph, so one range test covers the whole family.
	return r >= 0x2500 && r <= 0x257F
}

// assertBoundedFrame fails when any rendered line exceeds viewportWidth, or when
// a framed surface lost either vertical edge.
//
// framed reports whether the surface is expected to draw a border. It is false
// only for the one style in the tree that disables all four sides (the build
// summary block, which is deliberately a bare padded block rather than a card).
func assertBoundedFrame(t *testing.T, name, rendered string, viewportWidth int, framed bool) {
	t.Helper()

	// The floor: a viewport narrower than the minimum bound degrades to
	// components.MinBoundWidth rather than collapsing to nothing.
	limit := viewportWidth
	if limit < components.MinBoundWidth {
		limit = components.MinBoundWidth
	}

	lines := strings.Split(rendered, "\n")
	widest := 0
	for _, l := range lines {
		if w := components.MaxLineWidth(l); w > widest {
			widest = w
		}
	}
	if widest > limit {
		t.Errorf("%s at viewport %d: widest line is %d cells, want <= %d\n%s",
			name, viewportWidth, widest, limit, rendered)
	}

	if strings.TrimSpace(rendered) == "" {
		t.Errorf("%s at viewport %d: rendered nothing", name, viewportWidth)
		return
	}
	if !framed {
		return
	}

	// A frame is three kinds of row, and each has its own edge glyphs: the top
	// and bottom rules carry CORNERS, the content rows carry VERTICALS. So the
	// rules are checked as box glyphs and the verticals are read off the first
	// content row — which is exactly the "did the side edges survive the fit"
	// question. Deriving both from the same row is what made a correct frame
	// look broken.
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = ansi.Strip(l)
	}
	last := len(plain) - 1

	ruleEdge := func(i int, label string) {
		r := []rune(plain[i])
		if len(r) < 2 || !isBoxRune(r[0]) || !isBoxRune(r[len(r)-1]) {
			t.Errorf("%s at viewport %d: the %s is not a closed box edge: %q",
				name, viewportWidth, label, plain[i])
		}
	}
	ruleEdge(0, "top rule")
	ruleEdge(last, "bottom rule")

	// A label-only card is top rule, one content row, bottom rule.
	var leftEdge, rightEdge rune
	for i := 1; i < last; i++ {
		if strings.TrimSpace(plain[i]) == "" {
			continue
		}
		r := []rune(plain[i])
		if len(r) < 2 || !isBoxRune(r[0]) || !isBoxRune(r[len(r)-1]) {
			t.Errorf("%s at viewport %d: content row %d is not closed: %q",
				name, viewportWidth, i, plain[i])
			return
		}
		if leftEdge == 0 {
			leftEdge, rightEdge = r[0], r[len(r)-1]
		}
		if r[0] != leftEdge {
			t.Errorf("%s at viewport %d: row %d lost its left edge (want %q)\n%q",
				name, viewportWidth, i, string(leftEdge), plain[i])
		}
		if r[len(r)-1] != rightEdge {
			t.Errorf("%s at viewport %d: row %d lost its right edge (want %q)\n%q",
				name, viewportWidth, i, string(rightEdge), plain[i])
		}
	}
}

// TestPartialNoticeCardFitsTheTerminal is the [PARTIAL] DoD. The truncation
// warning is the longest fixed string the UI emits and it renders inside the
// amber safety-gate frame, so if that frame's width arithmetic is wrong this is
// the surface that visibly tears.
func TestPartialNoticeCardFitsTheTerminal(t *testing.T) {
	for _, vw := range boundedWidths {
		assertBoundedFrame(t, "[PARTIAL] notice", boundedWarning(partialNotice, vw), vw, true)
	}
}

// TestFramedSurfacesFitTheTerminal applies the same invariant to every framed
// card the conversation can produce, at every width. They are grouped into one
// test because they share one bounding discipline: a status surface that drifts
// into a different discipline from an error surface is exactly the regression
// the shared Bound seam exists to prevent.
func TestFramedSurfacesFitTheTerminal(t *testing.T) {
	ask := components.NewAskComponent(
		"Please pick the boundary you want the refactor to respect",
		true,
		[]components.AskOption{
			{ID: "api", Title: "Keep the public API surface stable", Description: rawProviderBody, Recommended: true},
			{ID: "impl", Title: "Allow internal restructuring", Description: rawProviderBody},
		},
	)

	for _, vw := range boundedWidths {
		// name, rendered, framed
		surfaces := [][3]any{
			{"[PARTIAL] notice", boundedWarning(partialNotice, vw), true},
			{"error banner", components.ErrorBanner(rawProviderBody, vw), true},
			{"status banner", components.StatusBanner("Streaming", rawProviderBody, vw), true},
			{"policy banner", components.PolicyBanner("Tool rejected", rawProviderBody, vw), true},
			{"info banner", components.InfoBanner("Clarify", rawProviderBody, vw), true},
			{"unbreakable payload", components.ErrorBanner(unbreakablePayload(), vw), true},
			{"approval box", boundBox(ApprovalBox, vw).Render("y approve · n reject · esc cancel"), true},
			{"permission box", boundBox(permissionBoxStyle, vw).Render("allow this shell command?"), true},
			{"decomposition box", boundBox(decompositionBoxStyle, vw).Render("apply this decomposition?"), true},
			{"thought log box", boundBox(thoughtLogBoxStyle, vw).Render(rawProviderBody), true},
			{"system log box", boundBox(systemLogBoxStyle, vw).Render(rawProviderBody), true},
			// buildSummaryBoxStyle disables all four border sides: it is a bare
			// padded block by design, so only its width is bounded.
			{"build summary box", boundBox(buildSummaryBoxStyle, vw).Render(rawProviderBody), false},
			{"oversized-repo notice", boundedWarning(Icon.Warning+" oversized repository — 12,400 files", vw), true},
		}
		for _, s := range surfaces {
			assertBoundedFrame(t, s[0].(string), s[1].(string), vw, s[2].(bool))
		}

		// The ask card is bounded by the same seam but is not a lipgloss border
		// on every width, so it is checked for width only.
		if got := components.MaxLineWidth(ask.Render(vw)); got > max(vw, components.MinBoundWidth) {
			t.Errorf("ask card at viewport %d: widest line is %d cells", vw, got)
		}
	}
}

// TestReasoningPanelFrameFitsTheTerminal pins the hand-rolled frame in
// thinking.go. It is drawn with string concatenation rather than a lipgloss
// border, so nothing in the box model protects it: the header rule's filler has
// to account for the elapsed badge explicitly, and it did not, pushing the
// "┐" corner past the right edge on every expanded panel.
func TestReasoningPanelFrameFitsTheTerminal(t *testing.T) {
	for _, vw := range boundedWidths {
		tp := NewThinkingPanel()
		tp.Append(strings.Repeat("an unbreakable reasoning token ", 40))
		tp.SetExpanded(true)

		// Elapsed is real elapsed time, so its rendered width varies; the bound
		// must hold for whatever it happens to be.
		panel := tp.Render(vw, "⠋")
		if strings.TrimSpace(panel) == "" {
			t.Fatalf("viewport %d: the expanded panel rendered nothing", vw)
		}
		// The panel floors its own width at 40, so the budget is the larger of
		// the two.
		budget := vw
		if budget < 40 {
			budget = 40
		}
		for i, line := range strings.Split(panel, "\n") {
			plain := ansi.Strip(line)
			if got := ansi.StringWidth(plain); got > budget {
				t.Errorf("viewport %d: row %d is %d cells, want <= %d\n%q", vw, i, got, budget, plain)
			}
		}
		// The panel is a hand-rolled frame, so its rows are checked directly:
		// the header rule ends on a "┐" corner, the body rows on "│", and the
		// footer rule on "└…┘". Every one of them must be exactly `budget` cells,
		// which is the assertion that the "┐" corner stayed on the frame.
		rows := strings.Split(ansi.Strip(panel), "\n")
		for i, row := range rows {
			if strings.TrimSpace(row) == "" {
				continue
			}
			switch {
			case i == 0: // header rule: ┌─ … ┐
				if !strings.HasPrefix(row, "┌") || !strings.HasSuffix(row, "┐") {
					t.Errorf("viewport %d: the header rule is not a closed box edge: %q", vw, row)
				}
			case i == len(rows)-1: // footer rule: └──…──┘
				if !strings.HasPrefix(row, "└") || !strings.HasSuffix(row, "┘") {
					t.Errorf("viewport %d: the footer rule is not a closed box edge: %q", vw, row)
				}
			default: // body row: │ … │
				if !strings.HasPrefix(row, "│") || !strings.HasSuffix(row, "│") {
					t.Errorf("viewport %d: body row %d lost a vertical edge: %q", vw, i, row)
				}
			}
		}
		// The whole point of the off-by-N: the header is the ONLY row that
		// carries the elapsed badge, so it is the only row that can drift. It
		// must be exactly as wide as the body.
		if got, want := ansi.StringWidth(rows[0]), ansi.StringWidth(rows[1]); got != want {
			t.Errorf("viewport %d: the header rule is %d cells, the body is %d — the ┐ corner is off the frame",
				vw, got, want)
		}
	}
	_ = time.Second
}
