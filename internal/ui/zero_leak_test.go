// zero_leak_test.go — THE FRAME NEVER EXCEEDS THE TERMINAL.
//
// # WHY THIS FILE EXISTS
//
// A Bubble Tea View() that returns more rows than the terminal has does not get
// truncated by the terminal. It SCROLLS it. Scrolling pushes the previous
// frame's last rows into scrollback, and scrollback is never redrawn: the
// orphan is frozen there for the rest of the session.
//
// The observable symptom was a growing stack of `ask )` prompt bars and
// telemetry rows above the conversation, one more per overflowing frame, worst
// during long streaming. The cause was arithmetic, not drawing:
//
//   - The viewport height was derived from a hand-maintained estimate of how
//     many rows the chrome took (`3 + autocompleteHeight` for the prompt, a
//     per-branch line count for the proposal dock). Every one of those numbers
//     was wrong the moment a region grew — a dropdown opened, a dock gained a
//     diff line — and nothing noticed, because the arithmetic still balanced.
//   - The composed frame was then handed to the terminal with no bound at all.
//
// Two tests, matching the two halves of the fix:
//
//	TestViewportFrameNeverExceedsTerminal — the BUDGET. The viewport is sized
//	  from the measured regions, so the frame fits by construction and the
//	  prompt bar and footer always survive.
//	TestClipFrameIsTheUnconditionalBackstop — the CLIP. Whatever the budget
//	  got wrong, ClipFrame bounds the result anyway.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// frameScenario is one UI state rendered at one terminal size. The set is
// chosen to cover the states that make the chrome TALL, because a frame only
// leaks when the fixed regions exceed the screen — a tall frame in a tall
// terminal proves nothing.
type frameScenario struct {
	name   string
	width  int
	height int
	setup  func(*model)
}

func frameScenarios() []frameScenario {
	return []frameScenario{
		{"idle", 100, 40, nil},
		{"idle narrow pane", 40, 24, nil},
		{"streaming", 100, 40, func(m *model) { m.streaming = true; m.state = StateProcessing }},
		{"processing with spinner", 80, 24, func(m *model) { m.state = StateProcessing }},
		{"awaiting approval, no dock", 80, 24, func(m *model) {
			m.state = StateAwaitingApproval
			m.pendingProposals = nil
		}},
		{"awaiting approval, collapsed dock", 80, 24, func(m *model) {
			m.state = StateAwaitingApproval
			m.pendingProposals = []SemanticProposal{{ID: "p", Target: SemanticTarget{QualifiedName: "a.go"}, Diff: "x"}}
		}},
		{"awaiting approval, huge expanded diff", 80, 24, func(m *model) {
			m.state = StateAwaitingApproval
			m.pendingProposals = []SemanticProposal{{
				ID: "p", Target: SemanticTarget{QualifiedName: "huge.go"},
				Diff: longDiff(400), Expanded: true,
			}}
		}},
		{"autocomplete open", 80, 24, func(m *model) {
			m.autocompleteActive = true
			m.autocompleteType = "command"
			for i := 0; i < 8; i++ {
				m.autocompleteItems = append(m.autocompleteItems,
					Suggestion{Label: fmt.Sprintf("/command%d", i), Detail: "do the thing"})
			}
		}},
		{"vi mode", 80, 24, func(m *model) { m.inViMode = true }},
		{"agent running", 80, 24, func(m *model) { m.agentRunning = true; m.state = StateProcessing }},
		// Terminals shorter than the chrome. These are the cases that leaked:
		// the viewport cannot shrink below 1 row, so without a clip the frame
		// is the full chrome height no matter how small the terminal is.
		{"terminal 12 rows", 40, 12, nil},
		{"terminal 8 rows", 40, 8, nil},
		{"terminal 6 rows", 40, 6, nil},
		{"terminal 4 rows", 40, 4, nil},
		{"terminal 3 rows", 40, 3, nil},
		{"terminal 1 row", 40, 1, nil},
		{"terminal 5 rows, wide", 200, 5, nil},
		{"terminal 10 rows with dock", 80, 10, func(m *model) {
			m.state = StateAwaitingApproval
			m.pendingProposals = []SemanticProposal{{
				ID: "p", Target: SemanticTarget{QualifiedName: "a.go"},
				Diff: longDiff(200), Expanded: true,
			}}
		}},
	}
}

func longDiff(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "-old line %d with content that is fairly long and realistic\n", i)
		fmt.Fprintf(&b, "+new line %d with content that is fairly long and realistic\n", i)
	}
	return b.String()
}

// newWorkspaceModel returns a model that renders the REAL workspace chrome —
// header, viewport, prompt bar, footer — rather than the first-run onboarding
// overlay.
//
// The distinction matters for everything in this file. newTestModel leaves
// workspaceRoot empty, so isProjectInitialized() is false and BuildWorkspace
// returns a full-screen Overlay: that string happens to be inside the terminal
// bounds, so a bounds assertion against it passes for the wrong reason, and a
// "the prompt bar is present" assertion is really asserting something about the
// onboarding screen. Backing the model with a real .izen directory is what puts
// the actual composition under test.
func newWorkspaceModel(t *testing.T) *model {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".izen"), 0o755); err != nil {
		t.Fatalf("mkdir .izen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".izen", "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m := newTestModel()
	m.workspaceRoot = dir
	m.initStage = initComplete
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.Ready = true
	m.showBanner = false
	m.Viewport.Width = m.width
	m.refreshViewportContent()
	return m
}

// frameOf builds the model, applies the scenario, and returns the composed frame
// plus the terminal size it must fit in.
func frameOf(t *testing.T, s frameScenario) (frame string, w, h int) {
	t.Helper()
	m := newWorkspaceModel(t)
	m.width, m.height = s.width, s.height
	if s.setup != nil {
		s.setup(m)
	}
	m.ti.SetValue("a prompt that is long enough to fill the input row comfortably")
	m.refreshViewportContent()
	return m.View(), s.width, s.height
}

// TestViewportFrameNeverExceedsTerminal is the DoD. For every state and every
// terminal size, the frame handed to the terminal is at most height rows by
// width cells.
//
// Both dimensions are checked, and per line rather than by average, because a
// single over-wide row is what wraps the terminal and what pushes the right
// border of a banner off-screen.
func TestViewportFrameNeverExceedsTerminal(t *testing.T) {
	for _, s := range frameScenarios() {
		t.Run(s.name, func(t *testing.T) {
			frame, w, h := frameOf(t, s)

			if got := lipgloss.Height(frame); got > h {
				t.Errorf("frame is %d rows, terminal is %d — the terminal will SCROLL and "+
					"leave the prompt bar in scrollback\n%s", got, h, frame)
			}
			for i, line := range strings.Split(frame, "\n") {
				if got := lipgloss.Width(line); got > w {
					t.Errorf("row %d is %d cells, terminal is %d\n%q", i, got, w, ansi.Strip(line))
				}
			}
		})
	}
}

// TestPromptAndFooterSurviveEveryFrame is the half of the DoD that a pure clip
// would fail. A frame clipped from the bottom loses its prompt bar and its
// footer — the two surfaces the user is typing into — and a TUI that has lost
// them is broken even though every line is in bounds.
//
// So the budget must absorb the shortfall instead: whenever the terminal has
// room for the prompt and the footer, both must be present in the frame.
func TestPromptAndFooterSurviveEveryFrame(t *testing.T) {
	for _, s := range frameScenarios() {
		// The chrome cannot be smaller than itself. Below this, dropping the
		// prompt is the only honest option and the frame is clipped to nothing.
		if s.height < 4 {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			m := newWorkspaceModel(t)
			m.width, m.height = s.width, s.height
			if s.setup != nil {
				s.setup(m)
			}
			m.ti.SetValue("hello")
			m.refreshViewportContent()
			frame := ansi.Strip(m.View())

			// Vi mode replaces the command prompt with a status line, so the
			// marker is state-dependent. Asserting the wrong one would make this
			// test pass for the wrong reason in vi mode and fail in every other.
			promptMarker := Icon.Command
			if m.inViMode {
				promptMarker = m.viModeLabel()
			}
			if !strings.Contains(frame, promptMarker) {
				t.Errorf("prompt bar missing %q from the frame (mode=%s, vi=%v)\n%s",
					promptMarker, m.resolver.Current(), m.inViMode, frame)
			}
			// The lifecycle footer renders a horizontal rule; its presence is
			// what distinguishes "anchored footer" from "clipped away".
			if !strings.Contains(frame, "─") {
				t.Errorf("footer rule missing from the frame\n%s", frame)
			}
		})
	}
}

// TestViewportHeightIsDerivedFromMeasuredRegions pins the budget itself: the
// reserved viewport height plus every measured region must account for the whole
// screen — exactly, with nothing left over.
//
// The previous implementation reserved `3 + autocompleteHeight` for the prompt
// and a hand-written per-branch constant for the dock. Both were guesses; this
// asserts the composition rather than a number, so it keeps holding as the
// chrome grows. There is no rounding reserve any more: the budget is exact, and
// a budget that comes up short is not conservative, it is a frame whose prompt
// bar floats above the pane's bottom edge.
func TestViewportHeightIsDerivedFromMeasuredRegions(t *testing.T) {
	for _, s := range frameScenarios() {
		t.Run(s.name, func(t *testing.T) {
			m := newWorkspaceModel(t)
			m.width, m.height = s.width, s.height
			if s.setup != nil {
				s.setup(m)
			}
			m.ti.SetValue("hello")
			m.refreshViewportContent()
			ws := m.assembleScreen(nil)

			// The composition is asserted on the Workspace the renderer will
			// actually join, not on a re-derivation of it: a re-render is a second
			// opinion, and a second opinion is how the reserved height and the
			// drawn height come to disagree.
			header := regionHeight(ws.Header)
			dock := regionHeight(ws.ProposalDock)
			input := regionHeight(ws.Input)
			footer := regionHeight(ws.Footer)

			// When the chrome alone exceeds the screen no viewport height can
			// make the frame fit — a 3-row prompt plus a 1-row footer cannot live
			// in a 3-row terminal. Those cases are covered by
			// TestViewportFrameNeverExceedsTerminal via the clip; asserting the
			// budget can absorb them would be asserting the impossible.
			if header+dock+input+footer >= s.height {
				if ws.ViewportRows < 1 {
					t.Errorf("viewport rows = %d, want >= 1 even when the chrome cannot fit",
						ws.ViewportRows)
				}
				return
			}

			// The four fixed regions and the viewport must fill the screen
			// exactly. An equality, not an inequality: the leftovers are the rows
			// the prompt bar and the footer float above the pane's bottom edge.
			total := header + ws.ViewportRows + dock + input + footer
			if total != s.height {
				t.Errorf("composition accounts for %d of %d rows: header=%d viewport=%d "+
					"dock=%d input=%d footer=%d",
					total, s.height, header, ws.ViewportRows, dock, input, footer)
			}
			// The viewport surface the compositor is handed must be the same
			// number of rows the budget reserved for it. These are two different
			// measurements — one is what the renderer produced, the other what the
			// layout promised — and the compositor's whole job is to reconcile them,
			// so a disagreement here is the bug the reconciliation exists to hide.
			if drawn := regionHeight(ws.Viewport); drawn != ws.ViewportRows {
				t.Errorf("the viewport surface is %d rows but the budget reserved %d",
					drawn, ws.ViewportRows)
			}
		})
	}
}

// TestViewportHeightFillsThePaneExactly is the DoD for the budget, stated on the
// arithmetic itself rather than on a rendered frame: whatever the chrome, the
// four fixed regions and the viewport must sum to the pane's height.
//
// Both directions are load-bearing, and they are the two halves of one bug. A sum
// UNDER the pane height is the "floating `ask )` prompt" — the prompt bar and
// the lifecycle footer are drawn above the bottom edge with stale terminal
// content visible beneath them. A sum OVER it is the scroll — rows are pushed
// into scrollback, which is never redrawn, so the prompt bar is orphaned there.
// A reserve cannot prevent either; it only picks how far off the frame lands.
func TestViewportHeightFillsThePaneExactly(t *testing.T) {
	for screenH := 8; screenH <= 80; screenH++ {
		for _, c := range []struct{ h, p, i, f int }{
			{1, 0, 3, 1}, {2, 0, 3, 1}, {0, 0, 3, 1}, {0, 8, 3, 1}, {0, 0, 12, 1}, {0, 0, 3, 4},
		} {
			// Only assert exactness where the fixed chrome actually fits: when it
			// does not, the compositor drops regions from the top, which is a
			// different (and separately tested) behaviour.
			if c.h+c.p+c.i+c.f >= screenH {
				continue
			}
			got := ViewportHeight(screenH, c.h, c.p, c.i, c.f)
			if sum := c.h + got + c.p + c.i + c.f; sum != screenH {
				t.Errorf("screen=%d chrome=(header %d, dock %d, prompt %d, footer %d): "+
					"viewport = %d, want the composition to total exactly %d (got %d)",
					screenH, c.h, c.p, c.i, c.f, got, screenH, sum)
			}
		}
	}
}

// TestViewportHeightNeverExceedsAvailableSpace: a viewport taller than the space
// left for it is the one input that overflows the frame no matter what the clip
// does, so the budget has to bound it in both directions.
//
// The bound is stated against the space the prompt and footer leave, not
// against the whole screen, because when the chrome ALONE exceeds the screen no
// viewport height can make the frame fit — a 12-row autocomplete dropdown in a
// 5-row terminal is drawn at 12 rows whatever the viewport is. That case is real
// and is handled by composeBottomAnchoredFrame's top-down trim, not by
// pretending the budget can absorb it.
func TestViewportHeightNeverExceedsAvailableSpace(t *testing.T) {
	for screenH := 1; screenH <= 80; screenH++ {
		for _, c := range []struct{ h, p, i, f int }{
			{2, 0, 3, 1}, {0, 0, 3, 1}, {0, 8, 3, 1}, {0, 0, 12, 1}, {0, 0, 3, 4},
		} {
			got := ViewportHeight(screenH, c.h, c.p, c.i, c.f)
			if got < 1 {
				t.Errorf("screen=%d chrome=(header %d, dock %d, prompt %d, footer %d): "+
					"viewport = %d, want >= 1", screenH, c.h, c.p, c.i, c.f, got)
			}
			ceiling := max(1, screenH-c.i-c.f)
			if got > ceiling {
				t.Errorf("screen=%d chrome=(header %d, dock %d, prompt %d, footer %d): "+
					"viewport = %d, want <= %d (the space the prompt and footer leave)",
					screenH, c.h, c.p, c.i, c.f, got, ceiling)
			}
		}
	}
}

// TestClipFrameIsTheUnconditionalBackstop tests the second half in isolation, on
// strings no budget could have accounted for. ClipFrame is what makes the leak
// structurally impossible rather than merely unlikely: it does not consult any
// part of the frame.
func TestClipFrameIsTheUnconditionalBackstop(t *testing.T) {
	bounds := ScreenBounds{Width: 40, Height: 10}

	t.Run("clips an over-tall frame", func(t *testing.T) {
		frame := strings.Repeat("a row of the frame\n", 100)
		got := ClipFrame(frame, bounds)
		if h := lipgloss.Height(got); h > bounds.Height {
			t.Errorf("clipped frame is %d rows, want <= %d", h, bounds.Height)
		}
	})
	t.Run("clips an over-wide frame", func(t *testing.T) {
		frame := strings.Repeat(strings.Repeat("x", 200)+"\n", 5)
		got := ClipFrame(frame, bounds)
		for i, l := range strings.Split(got, "\n") {
			if w := lipgloss.Width(l); w > bounds.Width {
				t.Errorf("row %d is %d cells, want <= %d", i, w, bounds.Width)
			}
		}
	})
	t.Run("leaves a fitting frame byte-identical", func(t *testing.T) {
		// The clip runs on every frame, so the common path must be free of
		// side effects: no re-padding, no re-wrapping, no dropped trailing row.
		frame := "one\ntwo\nthree"
		if got := ClipFrame(frame, bounds); got != frame {
			t.Errorf("ClipFrame modified a frame that already fit:\n got %q\nwant %q", got, frame)
		}
	})
	t.Run("preserves the empty frame", func(t *testing.T) {
		if got := ClipFrame("", bounds); got != "" {
			t.Errorf("ClipFrame(\"\") = %q, want empty", got)
		}
	})
	t.Run("does not split an escape sequence", func(t *testing.T) {
		// A truncated SGR run would leak colour into whatever the user runs
		// next, so the clip must cut on row boundaries only.
		frame := strings.Repeat("\x1b[31mred row\x1b[0m\n", 50)
		got := ansi.Strip(ClipFrame(frame, bounds))
		if strings.Contains(got, "\x1b") {
			t.Error("clipping left a partial escape sequence in the output")
		}
	})
}

// TestRegionHeightCountsRowsNotCharacters: the budget is computed in rows, and
// the two degenerate inputs both over-report. lipgloss.Height("") is 1 (an
// empty region is joined away and occupies nothing) and lipgloss.Height on a
// trailing "\n" counts a row that is never drawn. Either error reserves space
// nobody uses, which shows up as a hole between the header and the viewport —
// and as a mouse-mapping offset, since clicks are resolved through the same
// rectangle.
func TestRegionHeightCountsRowsNotCharacters(t *testing.T) {
	cases := []struct {
		name   string
		region string
		want   int
	}{
		{"empty", "", 0},
		{"one row", "a", 1},
		{"two rows", "a\nb", 2},
		{"trailing newline", "a\nb\n", 2},
		{"single row with newline", "a\n", 1},
		{"blank row between", "a\n\nb", 3},
		{"only a newline", "\n", 1},
	}
	for _, c := range cases {
		if got := regionHeight(c.region); got != c.want {
			t.Errorf("%s: regionHeight(%q) = %d, want %d", c.name, c.region, got, c.want)
		}
	}
}

// TestScreenBoundsNeverDegenerate: terminals report 0x0 before the first
// WindowSizeMsg, and a zero bound would make every downstream computation
// degenerate — a zero MaxHeight truncates to the empty string.
func TestScreenBoundsNeverDegenerate(t *testing.T) {
	for _, c := range []struct{ w, h int }{{0, 0}, {-1, -1}, {0, 40}, {40, 0}, {1, 1}} {
		m := &model{width: c.w, height: c.h}
		b := m.Screen()
		if b.Width < 1 || b.Height < 1 {
			t.Errorf("model %dx%d: Screen() = %dx%d, want at least 1x1", c.w, c.h, b.Width, b.Height)
		}
	}
	// A nil model must not panic — Screen is on the View() path, which runs
	// before the model is fully constructed.
	var nilModel *model
	if b := nilModel.Screen(); b.Width < 1 || b.Height < 1 {
		t.Errorf("nil model: Screen() = %dx%d, want at least 1x1", b.Width, b.Height)
	}
}

// TestStreamingFramesNeverLeak is the reported symptom, exercised directly: a
// long stream rendered frame by frame, with every intermediate frame bounded.
// A leak is cumulative, so a single overflowing frame in the middle of a stream
// leaves debris even if the first and last frames are fine.
func TestStreamingFramesNeverLeak(t *testing.T) {
	for _, s := range []struct{ w, h int }{{40, 24}, {80, 30}, {100, 40}, {60, 12}} {
		t.Run(fmt.Sprintf("%dx%d", s.w, s.h), func(t *testing.T) {
			m := newWorkspaceModel(t)
			m.width, m.height = s.w, s.h
			m.streaming = true
			m.state = StateProcessing

			// A document that grows a record at a time, which is the shape of a
			// real stream: the content crosses every height between a few rows and
			// far more than the screen, and the chrome above it changes as the
			// state does.
			for tick := 1; tick <= 120; tick++ {
				m.records = append(m.records, record{
					role: roleAI,
					text: fmt.Sprintf("Turn %d: the build is failing in the parser because the "+
						"token stream is delivered a few bytes at a time and the layout thrashes "+
						"while it grows.", tick),
				})
				m.refreshViewportContent()

				frame := m.View()
				if h := lipgloss.Height(frame); h > s.h {
					t.Fatalf("tick %d: frame is %d rows, terminal is %d — this frame would "+
						"scroll and orphan the prompt bar", tick, h, s.h)
				}
				for i, line := range strings.Split(frame, "\n") {
					if w := lipgloss.Width(line); w > s.w {
						t.Fatalf("tick %d: row %d is %d cells, terminal is %d", tick, i, w, s.w)
					}
				}
			}
		})
	}
}

// TestZeroRawBytesReachStdoutDuringStreaming closes the last gap. Clipping the
// frame stops the terminal from scrolling, but only if the frame is the ONLY
// thing written to the terminal: a single stray Printf from a render path
// interleaves with Bubble Tea's own redraw sequences and corrupts the visible
// screen just as effectively, and lands in scrollback as raw text.
func TestZeroRawBytesReachStdoutDuringStreaming(t *testing.T) {
	var mu sync.Mutex
	var leaked []string

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()

	func() {
		defer func() {
			os.Stdout = orig
			_ = w.Close()
		}()
		m := newWorkspaceModel(t)
		m.width, m.height = 80, 24
		m.streaming = true
		m.state = StateProcessing
		for tick := 0; tick < 30; tick++ {
			m.records = append(m.records, record{
				role: roleAI,
				text: fmt.Sprintf("streamed line %d: a line of prose that keeps going. ", tick) +
					strings.Repeat("padding words ", 40),
			})
			m.refreshViewportContent()
			mu.Lock()
			_ = m.View()
			mu.Unlock()
		}
	}()

	captured := <-done
	_ = r.Close()

	mu.Lock()
	defer mu.Unlock()
	for _, line := range strings.Split(captured, "\n") {
		if strings.TrimSpace(line) != "" {
			leaked = append(leaked, line)
		}
	}
	if len(leaked) > 0 {
		t.Errorf("%d raw lines reached os.Stdout during streaming; every byte must go "+
			"through the renderer:\n%s", len(leaked), strings.Join(leaked, "\n"))
	}
}
