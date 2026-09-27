// bottom_anchor_test.go — THE PROMPT BAR AND FOOTER LIVE ON THE LAST ROWS.
//
// # WHY THIS FILE EXISTS
//
// The pane is a fixed-size surface with one flexible region in it. Three bands
// are fixed — the header, the proposal dock, and the bottom stack (the prompt bar
// plus the lifecycle footer) — and exactly one flexes: the conversation viewport.
// The frame therefore has an exact solution, and the two ways to miss it are both
// visible defects rather than cosmetic drift:
//
//   - A frame that comes up SHORT draws the `ask )` prompt bar and the footer
//     ABOVE the pane's bottom edge. Bubble Tea writes from the top and does not
//     pad, so whatever the terminal last painted stays visible underneath them.
//     This is the "floating prompt" report, and it is trivially easy to
//     reintroduce: a rounding reserve in the height budget produces it on every
//     single frame, and a reserve looks like prudence.
//   - A frame that comes up LONG is not truncated, it SCROLLS. The rows past the
//     bottom edge land in scrollback, which is never redrawn, so the prompt bar
//     is orphaned there for the rest of the session.
//
// Both trace back to one cause: the frame was never REQUIRED to be exactly tall.
// These tests state that requirement, in both directions, on rendered frames —
// the arithmetic test alone would pass while a compositor ignored it.
package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// frameRows splits a frame into its rendered rows, dropping the trailing empty
// row a frame ending in "\n" would otherwise contribute. Counted this way, a
// frame's row count is the number of rows a terminal actually paints.
func frameRows(frame string) []string {
	return strings.Split(strings.TrimRight(frame, "\n"), "\n")
}

// setPane resizes the model through the real WindowSizeMsg path, so the pane
// fields the layout budget reads are populated exactly as a live terminal would
// populate them.
func setPane(t *testing.T, m *model, w, h int) *model {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	out, ok := updated.(*model)
	if !ok {
		t.Fatalf("WindowSizeMsg returned %T, want *model", updated)
	}
	return out
}

// TestFrameIsExactlyPaneHeight is the primary DoD. For every state and every
// pane shape the renderer produces, the frame handed to the terminal is EXACTLY
// the pane's height: not one row short (the floating prompt) and not one row
// long (the scroll that orphans the prompt bar).
//
// The scenarios deliberately include the states that make the chrome tall and
// the panes that make it cramped, because a frame only miscounts when the
// arithmetic is under pressure.
func TestFrameIsExactlyPaneHeight(t *testing.T) {
	for _, s := range frameScenarios() {
		t.Run(s.name, func(t *testing.T) {
			m := newWorkspaceModel(t)
			m = setPane(t, m, s.width, s.height)
			if s.setup != nil {
				s.setup(m)
			}
			m.ti.SetValue("a prompt that fills the input row")
			m.refreshViewportContent()

			frame := m.View()
			if got := lipgloss.Height(frame); got != s.height {
				t.Errorf("frame is %d rows, pane is %d — a short frame floats the prompt "+
					"bar above the bottom edge, a tall one scrolls and orphans it\n%s",
					got, s.height, frame)
			}
		})
	}
}

// TestBottomStackOccupiesTheLastRows is the anchoring itself, asserted by
// POSITION rather than by presence. A frame can contain a prompt bar and still
// float it in the middle of the pane; only its row index distinguishes an
// anchored surface from a merely-present one.
//
// The lifecycle footer is a single line, so it must be the final row of the
// frame — with nothing drawn after it. The prompt bar is the last row of the
// input region, immediately above the footer, whatever the viewport contains.
func TestBottomStackOccupiesTheLastRows(t *testing.T) {
	for _, s := range frameScenarios() {
		if s.height < 4 {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			m := newWorkspaceModel(t)
			m = setPane(t, m, s.width, s.height)
			if s.setup != nil {
				s.setup(m)
			}
			m.ti.SetValue("a prompt that fills the input row")
			m.refreshViewportContent()

			rows := frameRows(ansi.Strip(m.View()))
			if len(rows) != s.height {
				t.Fatalf("frame has %d rows, pane is %d", len(rows), s.height)
			}

			// The LAST row is the lifecycle footer. It is a padded, non-blank
			// single line, so "the last row is blank" is the signature of a frame
			// that under-fills the pane.
			if strings.TrimSpace(rows[len(rows)-1]) == "" {
				t.Errorf("the last row is blank: the bottom stack is floating above the "+
					"pane's bottom edge\n%s", strings.Join(rows, "\n"))
			}

			// The prompt bar is the last NON-BLANK row above the footer, and it
			// must be no more than a couple of rows up (the prompt region is a
			// rule, the prompt line, and a closing rule). A prompt bar parked
			// halfway up the frame is the same defect at a different magnitude.
			promptRow := -1
			for i := len(rows) - 2; i >= 0 && i >= len(rows)-6; i-- {
				if strings.Contains(rows[i], Icon.Command) {
					promptRow = i
					break
				}
			}
			if promptRow >= 0 {
				if gap := len(rows) - 1 - promptRow; gap > 2 {
					t.Errorf("the prompt bar is %d rows above the bottom edge — it is "+
						"floating, not anchored\n%s", gap, strings.Join(rows, "\n"))
				}
			}
		})
	}
}

// TestViewportRendersExactlyItsBudgetedRows is the elastic-region half of the
// contract, asserted where it can actually fail: a SHORT conversation.
//
// The viewport's job is to occupy the rows the budget gave it whether or not
// there is content to fill them. A viewport that renders only as many rows as it
// has content does not look wrong on screen — it looks fine until the content is
// short, and then it silently lifts the prompt bar and the footer off the pane's
// bottom edge, which is the whole bug.
func TestViewportRendersExactlyItsBudgetedRows(t *testing.T) {
	for _, hw := range [][2]int{{80, 24}, {120, 40}, {56, 30}, {40, 20}, {60, 50}, {200, 60}} {
		w, h := hw[0], hw[1]
		t.Run(itoa(w)+"x"+itoa(h), func(t *testing.T) {
			// Content lengths from empty to far taller than the pane: the frame
			// height must be identical for every one of them.
			for _, recs := range []int{0, 1, 3, 12, 200} {
				m := newWorkspaceModel(t)
				m = setPane(t, m, w, h)
				m.showBanner = false
				for i := 0; i < recs; i++ {
					m.records = append(m.records, record{
						role: roleAI,
						text: "A line of streamed prose that occupies exactly one row of " +
							"the conversation viewport and says something.",
					})
				}
				m.ti.SetValue("anchor check")
				m.refreshViewportContent()

				if got := lipgloss.Height(m.View()); got != h {
					t.Errorf("%d records: frame is %d rows, pane is %d", recs, got, h)
				}
			}
		})
	}
}

// TestComposedRegionsAreMeasuredAndDrawnIdentically is the seam the exact
// budget depends on.
//
// A region ending in "\n" is drawn one row TALLER by lipgloss.JoinVertical than
// the budget accounts for, because the join counts the empty row after a
// trailing newline as a real one. A composition built from such regions is
// therefore taller than everything measured, and it overflows the pane by exactly
// the amount it can least afford to be wrong by. normalizeRegion is the single
// place that disagreement is removed; this pins it, because a regression here is
// invisible in the row count and visible only as a scroll.
func TestComposedRegionsAreMeasuredAndDrawnIdentically(t *testing.T) {
	for _, c := range []struct{ name, region string }{
		{"no trailing newline", "a\nb"},
		{"one trailing newline", "a\nb\n"},
		{"two trailing newlines", "a\nb\n\n"},
		{"single row", "a"},
		{"only a newline", "\n"},
		{"blank row in the middle", "a\n\nb"},
	} {
		normalized := normalizeRegion(c.region)
		if normalized == "" {
			// A region that normalizes away is DROPPED from the composite, so
			// it is never joined and never draws a row. lipgloss.Height("") is 1
			// by definition, which is precisely why the drop has to be explicit.
			continue
		}
		if got := lipgloss.Height(normalized); got != regionHeight(normalized) {
			t.Errorf("%s: after normalizeRegion the measured height is %d but the drawn "+
				"height is %d", c.name, regionHeight(normalized), got)
		}
		// The join is what actually draws, so the invariant is stated against it.
		joined := lipgloss.JoinVertical(lipgloss.Left, "x", normalized)
		if got := lipgloss.Height(joined) - 1; got != regionHeight(normalized) {
			t.Errorf("%s: joined the region contributes %d rows, measured %d", c.name, got, regionHeight(normalized))
		}
	}
	// A region that normalizes to nothing is the one case where the two measures
	// legitimately differ, and it is safe precisely because it is never drawn.
	if normalizeRegion("") != "" {
		t.Error("normalizeRegion must preserve the empty string")
	}
	if normalizeRegion("\n") != "" {
		t.Error(`normalizeRegion("\n") must be empty so the region is dropped rather than joined`)
	}
}

// TestNormalizeRegionTrimsTrailingNewlines: ALL of them, not just the last.
// regionHeight corrects for exactly one, so a region ending "\n\n" would keep
// disagreeing with the join by one row — and one row is the whole budget.
func TestNormalizeRegionTrimsTrailingNewlines(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"a", "a"},
		{"a\n", "a"},
		{"a\n\n", "a"},
		{"a\nb\n", "a\nb"},
		{"a\nb\n\n\n", "a\nb"},
		{"\n", ""},
		{"\n\n", ""},
		// A blank row BETWEEN content is content: the author's own spacing, and
		// trimming it would silently close the gap they asked for.
		{"a\n\nb", "a\n\nb"},
	} {
		if got := normalizeRegion(c.in); got != c.want {
			t.Errorf("normalizeRegion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFitRegionRowsPinsBothDirections: a pinned region is padded when it renders
// short and truncated when it renders long. The first direction is the floating
// prompt; the second is the scroll. Both are silent failures without it, which is
// why the helper does both instead of trusting its input.
//
// The measure is lipgloss.Height, because that is what the JoinVertical that
// actually draws the frame uses. Measuring the shortfall with regionHeight would
// pad a region one row short of the truth and reproduce the off-by-one this
// helper exists to eliminate.
func TestFitRegionRowsPinsBothDirections(t *testing.T) {
	for _, c := range []struct {
		name   string
		region string
		rows   int
	}{
		{"short by three", "a\nb", 5},
		{"short by one", "a\nb\nc", 4},
		{"exactly right", "a\nb\nc", 3},
		{"long by two", "a\nb\nc\nd\ne", 3},
		{"one row requested", "a\nb\nc", 1},
	} {
		if got := lipgloss.Height(fitRegionRows(c.region, c.rows)); got != c.rows {
			t.Errorf("%s: fitRegionRows(…, %d) rendered %d rows", c.name, c.rows, got)
		}
	}
	// A zero-row region contributes nothing at all — not a blank line.
	if got := fitRegionRows("a\nb", 0); got != "" {
		t.Errorf("fitRegionRows(…, 0) = %q, want empty", got)
	}
}

// TestComposeBottomAnchoredFramePinsTheBottomStack isolates the compositor from
// the model, because the property under test is arithmetic over regions and not
// anything about the TUI's state.
//
// The interesting case is the one the budget cannot solve: a fixed stack taller
// than the pane. There the only honest options are "scroll" and "give something
// up", and the choice is forced — the prompt bar and the footer are the surfaces
// the user is interacting with, so the header is what goes.
func TestComposeBottomAnchoredFramePinsTheBottomStack(t *testing.T) {
	bounds := ScreenBounds{Width: 40, Height: 10}

	t.Run("exact fit is preserved row for row", func(t *testing.T) {
		frame := composeBottomAnchoredFrame([]compositeRegion{
			{text: "header", rows: 1},
			{text: "one\ntwo\nthree", rows: 3, elastic: true},
			{text: "dock", rows: 1},
			{text: "rule\nask ) prompt\nrule", rows: 3},
			{text: "footer", rows: 1},
		}, bounds)
		rows := frameRows(frame)
		if len(rows) != 9 {
			t.Fatalf("frame is %d rows, want 9 (1+3+1+3+1):\n%s", len(rows), frame)
		}
		if !strings.HasPrefix(rows[0], "header") || !strings.HasPrefix(rows[8], "footer") {
			t.Errorf("the stack is not in order:\n%s", frame)
		}
		if !strings.Contains(rows[6], "ask ) prompt") {
			t.Errorf("the prompt bar is not in its slot:\n%s", frame)
		}
	})

	t.Run("an over-tall stack gives up the header, never the footer", func(t *testing.T) {
		// A 4-row pane whose bottom stack alone is 4 rows: the header and the
		// viewport have no room at all, and the only honest options are "scroll"
		// and "give something up". The prompt bar and the footer are the surfaces
		// the user is interacting with, so the header is what goes.
		frame := composeBottomAnchoredFrame([]compositeRegion{
			{text: "header", rows: 1},
			{text: "view", rows: 2, elastic: true},
			{text: "rule\nask ) prompt\nrule", rows: 3},
			{text: "footer", rows: 1},
		}, ScreenBounds{Width: 40, Height: 4})
		rows := frameRows(frame)
		if len(rows) > 4 {
			t.Fatalf("frame is %d rows, pane is 4 — this frame scrolls:\n%s", len(rows), frame)
		}
		if strings.Contains(strings.Join(rows, "\n"), "header") {
			t.Errorf("the header survived a frame that could not hold it:\n%s", frame)
		}
		if !strings.Contains(strings.Join(rows, "\n"), "ask ) prompt") {
			t.Errorf("the prompt bar was sacrificed instead of the header:\n%s", frame)
		}
		if !strings.HasPrefix(rows[len(rows)-1], "footer") {
			t.Errorf("the footer is not the last row:\n%s", frame)
		}
	})

	t.Run("a frame with no elastic region gives up nothing", func(t *testing.T) {
		// Every band here is bottom stack, so the trim has nothing to offer and
		// ClipFrame remains the only backstop. Silently dropping the prompt bar
		// to make a frame fit would be the compositor inventing a policy the
		// layout budget never asked for.
		frame := composeBottomAnchoredFrame([]compositeRegion{
			{text: "rule\nask ) prompt\nrule", rows: 3},
			{text: "footer", rows: 1},
		}, ScreenBounds{Width: 40, Height: 2})
		if !strings.Contains(frame, "ask ) prompt") {
			t.Errorf("the compositor dropped the prompt bar from a stack with no "+
				"elastic region:\n%s", frame)
		}
	})

	t.Run("empty regions contribute nothing", func(t *testing.T) {
		frame := composeBottomAnchoredFrame([]compositeRegion{
			{text: "", rows: 0},
			{text: "a", rows: 1, elastic: true},
			{text: "", rows: 0},
			{text: "footer", rows: 1},
		}, ScreenBounds{Width: 20, Height: 4})
		if got := len(frameRows(frame)); got != 2 {
			t.Errorf("empty regions added rows: %d rows for 2 regions\n%q", got, frame)
		}
	})

	t.Run("an over-long elastic region is truncated, not appended", func(t *testing.T) {
		frame := composeBottomAnchoredFrame([]compositeRegion{
			{text: "a\nb\nc\nd\ne\nf\ng\nh", rows: 2, elastic: true},
			{text: "footer", rows: 1},
		}, ScreenBounds{Width: 20, Height: 3})
		rows := frameRows(frame)
		if len(rows) != 3 {
			t.Errorf("frame is %d rows, want 3:\n%s", len(rows), frame)
		}
		if !strings.HasPrefix(rows[2], "footer") {
			t.Errorf("an over-long viewport pushed the footer off the bottom:\n%s", frame)
		}
	})
}
