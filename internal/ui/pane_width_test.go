// pane_width_test.go — A FRAME IS BOUNDED BY THE PANE, NOT THE TERMINAL.
//
// # WHY THIS FILE EXISTS
//
// IZEN is routinely run in a side-by-side terminal split, and the difference
// between the two widths is the entire subject of this file:
//
//	global terminal width — how wide the terminal is
//	pane width            — how wide the region the program is drawing in is
//
// A card bounded to the global width in a narrow pane is bounded to a rectangle
// the user cannot see. Its right border lands past the pane edge, the terminal
// hard-wraps there, and the frame the user sees is a torn box with a stray `|`
// where the border should be — the [PARTIAL] truncation notice is the canonical
// case, because it is the longest free-form string the UI ever emits.
//
// The two widths are stored under separate names (m.width / m.paneWidth) and read
// through accessors (PaneWidth / PaneHeight) precisely so that a call site has to
// choose. A renderer that wants a frame bound has no way to reach the global
// width by accident, and a test can express "a 56-cell pane inside a 200-cell
// terminal" at all.
package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/ui/components"
)

// splitPaneWidths spans a very narrow split through to a wide one, all of them
// narrower than the 200-cell terminal they are placed in.
var splitPaneWidths = []int{40, 50, 56, 60, 72, 100}

// TestWindowSizeMsgRecordsThePaneExplicitly is the seam: the resize handler is
// where the two widths are separated, so a model that has been resized has an
// explicitly recorded pane rather than one that happens to alias the terminal.
//
// The aliasing is why this needs saying out loud. Before the split, `m.width`
// WAS the pane, so "use m.width" was accidentally correct and read as though it
// were a deliberate choice. Recording the pane under its own field is what turns
// it into one.
func TestWindowSizeMsgRecordsThePaneExplicitly(t *testing.T) {
	for _, hw := range [][2]int{{40, 12}, {80, 24}, {200, 60}} {
		m := newWorkspaceModel(t)
		m = setPane(t, m, hw[0], hw[1])

		if m.paneWidth != hw[0] || m.paneHeight != hw[1] {
			t.Errorf("resize to %dx%d recorded pane %dx%d",
				hw[0], hw[1], m.paneWidth, m.paneHeight)
		}
		if got := m.PaneWidth(); got != hw[0] {
			t.Errorf("PaneWidth() = %d, want %d", got, hw[0])
		}
		if got := m.PaneHeight(); got != hw[1] {
			t.Errorf("PaneHeight() = %d, want %d", got, hw[1])
		}
		// Screen() is the bounds every composition is clipped to, so it has to
		// report the pane: a frame bounded to the terminal but clipped to the
		// pane is a frame that gets truncated rather than wrapped.
		if b := m.Screen(); b.Width != hw[0] || b.Height != hw[1] {
			t.Errorf("Screen() = %dx%d, want the pane %dx%d", b.Width, b.Height, hw[0], hw[1])
		}
	}
}

// TestPaneAccessorsFallBackRatherThanDegenerate: a model built directly (in a
// test, or before the first WindowSizeMsg) has no recorded pane. The accessors
// must answer with the global width rather than zero, because a zero width makes
// every downstream bound degenerate — a negative Repeat count, a frame that
// collapses to nothing.
func TestPaneAccessorsFallBackRatherThanDegenerate(t *testing.T) {
	m := &model{width: 120, height: 40}
	if got := m.PaneWidth(); got != 120 {
		t.Errorf("PaneWidth() with no recorded pane = %d, want the global width 120", got)
	}
	if got := m.PaneHeight(); got != 40 {
		t.Errorf("PaneHeight() with no recorded pane = %d, want the global height 40", got)
	}

	// A recorded pane wins over the global width, which is the whole point.
	m.paneWidth, m.paneHeight = 56, 30
	if got := m.PaneWidth(); got != 56 {
		t.Errorf("PaneWidth() = %d, want the recorded pane 56", got)
	}

	// Degenerate and nil inputs are real states, not hypotheticals: terminals
	// report 0x0 before the first resize, and Screen() runs on a model that may
	// not be fully constructed.
	for _, c := range []*model{nil, {}, {width: 0, height: 0}, {width: -5, height: -5}} {
		if got := c.PaneWidth(); got < 1 {
			t.Errorf("PaneWidth() = %d, want at least 1", got)
		}
		if got := c.PaneHeight(); got < 1 {
			t.Errorf("PaneHeight() = %d, want at least 1", got)
		}
	}
}

// TestFramesAreBoundedByThePaneNotTheTerminal is the DoD. A 200-cell terminal
// with a narrow pane is the split-pane case, and every framed surface the
// conversation can produce must fit the PANE.
//
// The assertion is per-row, not per-frame: one over-wide row is what wraps the
// terminal, and an average or a maximum over a multi-row frame would both hide
// it. A frame that "fits on average" and has one 180-cell row is a frame the user
// sees as debris.
func TestFramesAreBoundedByThePaneNotTheTerminal(t *testing.T) {
	const global = 200
	for _, paneW := range splitPaneWidths {
		t.Run(itoa(paneW), func(t *testing.T) {
			m := newWorkspaceModel(t)
			// A wide terminal with a narrow pane. Both fields are set: the
			// terminal is real, and the pane is where the drawing happens.
			m.width, m.height = global, 30
			m.paneWidth, m.paneHeight = paneW, 30
			m.ti.SetValue("a prompt that fills the input row")
			m.push(roleSystem, boundedWarning(partialNotice, m.PaneWidth()))
			m.refreshViewportContent()

			frame := m.View()
			for i, row := range frameRows(frame) {
				if got := lipgloss.Width(row); got > paneW {
					t.Errorf("pane %d in a %d-cell terminal: row %d is %d cells\n%q",
						paneW, global, i, got, ansi.Strip(row))
				}
			}
		})
	}
}

// TestPartialBannerFrameSurvivesInASplitPane is the specific report, on the
// specific surface: the [PARTIAL] truncation notice rendered inside a 40–60 cell
// pane.
//
// The design changed from "both borders intact" to "no borders at all". A
// notice that used to draw a box and lost its right border was corrupt; the
// frameless design removes that failure mode entirely. What must hold now is
// that the notice carries its `▲ [PARTIAL]` badge, hangs the muted body off a
// soft `│` accent line, and draws no part of an enclosure at any pane width.
func TestPartialBannerFrameSurvivesInASplitPane(t *testing.T) {
	const global = 200
	for _, paneW := range []int{40, 50, 56, 60} {
		t.Run(itoa(paneW), func(t *testing.T) {
			m := newWorkspaceModel(t)
			m.width, m.height = global, 30
			m.paneWidth, m.paneHeight = paneW, 30
			m.ti.SetValue("hello")
			m.push(roleSystem, boundedWarning(partialNotice, m.PaneWidth()))
			m.refreshViewportContent()

			plain := ansi.Strip(m.View())

			// The badge is promoted out of the message's leading `[PARTIAL]`.
			if !strings.Contains(plain, "[PARTIAL]") {
				t.Errorf("pane %d: the notice lost its [PARTIAL] badge\n%s", paneW, plain)
			}
			// No corner ever appears: the notice is frameless.
			for _, corner := range []string{"╭", "╮", "╰", "╯", "┌", "┐", "└", "┘"} {
				if strings.Contains(plain, corner) {
					t.Errorf("pane %d: the notice drew a frame corner %q\n%s", paneW, corner, plain)
				}
			}
			// The muted body is anchored by at least one soft accent line.
			accented := 0
			for _, row := range strings.Split(plain, "\n") {
				if strings.HasPrefix(strings.TrimLeft(row, " "), "│") {
					accented++
				}
			}
			if accented == 0 {
				t.Errorf("pane %d: the notice drew no accent line to anchor its body\n%s", paneW, plain)
			}
		})
	}
}

// TestTablesAndBannersUseThePaneBound covers the two other bounded renderers, so
// the pane discipline cannot hold for banners and quietly not hold for tables —
// they share a budget but not a call site, and a table bounded to the terminal in
// a narrow pane tears its grid the same way a banner tears its frame.
func TestTablesAndBannersUseThePaneBound(t *testing.T) {
	const global = 200
	for _, paneW := range splitPaneWidths {
		t.Run(itoa(paneW), func(t *testing.T) {
			m := newWorkspaceModel(t)
			m.width, m.height = global, 30
			m.paneWidth, m.paneHeight = paneW, 30

			// A banner, a card and a table, each rendered at the pane bound.
			surfaces := map[string]string{
				"warning banner": boundedWarning(partialNotice, m.PaneWidth()),
				"error banner":   components.ErrorBanner(rawProviderBody, m.PaneWidth()),
				"grid":           renderTable(sampleTable, m.PaneWidth()),
			}
			for name, out := range surfaces {
				plain := ansi.Strip(out)
				for i, row := range strings.Split(plain, "\n") {
					if got := lipgloss.Width(row); got > paneW {
						t.Errorf("pane %d: %s row %d is %d cells\n%q", paneW, name, i, got, row)
					}
				}
			}
			// The table is a grid, so its structural glyphs have to be there.
			plain := ansi.Strip(surfaces["grid"])
			if !strings.HasPrefix(plain, "┌") || !strings.HasSuffix(plain, "┘") {
				t.Errorf("pane %d: the table is not a closed grid\n%s", paneW, plain)
			}
		})
	}
}
