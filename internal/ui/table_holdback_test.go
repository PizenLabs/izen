package ui

// ── Table Holdback Latch & One-Shot Atomic Render ───────────────────────────
//
// These tests are the Definition of Done for the streaming-table flicker, driven
// the way a user actually hits it: a Markdown table streamed ONE CHARACTER AT A
// TIME through the real streaming pipeline (emitVisibleContent →
// syncStreamingSegment → refreshViewportContent), with the model's own viewport
// and lifecycle ledger in play at every tick.
//
// The claims under test, in the order the spec states them:
//
//  1. ZERO PARTIAL TEXT LEAKS. While the holdback is accumulating, nothing about
//     the table reaches the document: no raw pipes, no cell fragments, and above
//     all no box frame — a partially built grid whose column widths will all
//     change again is the flicker, not a lesser version of it.
//  2. NO STATE TOGGLING. The one-line skeleton is mounted on every single tick
//     from the opening pipe to the termination signal. Not one frame in the
//     middle of the block is unmounted, and the latch's engage/release counters
//     prove it happened exactly once each.
//  3. ATOMIC REPLACEMENT. On the terminating blank line the whole grid appears in
//     one pass and the indicator is gone in the same projection — never both,
//     never neither, never a half-built grid.
//  4. STATUS BAR MIRROR. The fixed bottom bar carries the same canonical
//     sentence for the whole holdback window and drops it on the same tick.
//  5. STREAM-COMPLETION TERMINATION. A stream that ends mid-table releases the
//     latch and still renders the whole table.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/ui/animation"
	"github.com/PizenLabs/izen/internal/ui/markdown"
	"github.com/PizenLabs/izen/internal/ui/states"
)

// tableDoD is the streamed document used by most tests here: prose, a two-column
// table, a blank line, then more prose. The prose before and after is load
// bearing — it proves the holdback is a BLOCK-scoped construct and that content
// on either side of it keeps flowing normally.
const tableDoD = "Here are the results:\n\n" +
	"| Name | Age |\n" +
	"| --- | --- |\n" +
	"| Ana | 31 |\n" +
	"| Bo | 28 |\n" +
	"\n" +
	"That is the full roster."

// newTableDoDModel builds a streaming-ready model with a live viewport, so every
// assertion reads what the user would actually see rather than internal state.
func newTableDoDModel(t *testing.T) *model {
	t.Helper()
	m := readyChatModel(newTestModel())
	m.width = 96
	m.height = 40
	m.wrapWidth = 92
	m.Viewport.Width = m.width
	m.streaming = true
	m.streamingDocStart = -1
	m.docLayout = &DocumentLayout{width: m.wrapWidth}
	return m
}

// frame is one observation of the whole presentation after a streamed prefix: the
// document the viewport renders, the tail panel, the bottom status bar, and the
// lifecycle/latch bookkeeping. Capturing all four per tick is what lets a test
// assert that no two of them ever disagreed.
type frame struct {
	content   string // ANSI-stripped document body
	tail      string // ANSI-stripped tail panel (where the skeleton row lives)
	footer    string // ANSI-stripped fixed bottom status bar
	indicator string // the mounted indicator sentence, "" when unmounted
	epoch     uint64
}

// observe captures the whole presentation for one frame: the document the
// viewport renders, the tail panel, the bottom status bar, and the
// lifecycle/latch bookkeeping. Capturing all four per tick is what lets a test
// assert that no two of them ever disagreed.
func (m *model) observe() frame {
	f := frame{
		content:   m.visibleDocument(),
		tail:      animation.Strip(strings.Join(m.renderTailPanelLines(), "\n")),
		footer:    animation.Strip(m.renderFixedFooter(m.width, nil)),
		indicator: m.skeletonStatusMirror(),
	}
	if m.lifecycle != nil {
		f.epoch = m.lifecycle.Epoch()
	}
	return f
}

// visibleDocument is the ANSI-stripped concatenation of the document lines the
// viewport is currently projecting — the assistant streaming segment plus every
// committed record.
func (m *model) visibleDocument() string {
	if m.docLayout == nil {
		return ""
	}
	var b strings.Builder
	for _, l := range m.docLayout.Lines {
		b.WriteString(ansi.Strip(l.RenderedStr))
		b.WriteByte('\n')
	}
	return b.String()
}

// tableGridRunes are the box-drawing characters a RENDERED TABLE GRID is made of.
// Their presence in the document is the positive signal that the table has been
// laid out.
//
// The vertical bar is deliberately NOT here: "│" is the document's outer gutter,
// present on every assistant line forever, so including it would make every
// frame look like a rendered table.
var tableGridRunes = "┌┐└┘├┤┬┴┼─"

// hasTableGrid reports whether a rendered frame contains a laid-out table.
func hasTableGrid(s string) bool {
	return strings.ContainsAny(s, tableGridRunes)
}

// leaksTableText reports whether ANY trace of the table block reached the
// viewport: a raw pipe row, a cell fragment, a box frame, or one of the cell
// values. This is the anti-leak assertion, and it is deliberately broad — a
// partial row is a leak whether it was styled or not.
func leaksTableText(s string) bool {
	if hasTableGrid(s) {
		return true
	}
	for _, needle := range []string{"|", "Name", "Age", "Ana", "Bo ", "---"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// streamOneCharAtATime feeds src to the model one character at a time and calls
// visit after every character. It returns the number of ticks.
func streamOneCharAtATime(t *testing.T, m *model, src string, visit func(tick int, prefix string, f frame)) int {
	t.Helper()
	for i := 1; i <= len(src); i++ {
		prefix := src[:i]
		m.emitVisibleContent(prefix[len(prefix)-1:])
		m.refreshViewportContent()
		visit(i, prefix, m.observe())
	}
	return len(src)
}

// ── DoD 1 + 2: no leaks, no toggling, across the whole block ────────────────

// TestStreamingTableLeaksNothingAndNeverToggles is the headline DoD. Every
// single character of the stream is a separate frame, and for the entire window
// in which the table block is open the document must contain no trace of it and
// the one-line indicator must be mounted on every frame.
func TestStreamingTableLeaksNothingAndNeverToggles(t *testing.T) {
	m := newTableDoDModel(t)

	// The document as it stands before the table begins: the prose only.
	const preamble = "Here are the results:\n\n"
	streamOneCharAtATime(t, m, preamble, func(_ int, _ string, f frame) {
		if f.indicator != "" {
			t.Fatalf("an indicator mounted before any table byte arrived: %q", f.indicator)
		}
	})

	// The table block, one character at a time.
	const block = "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n| Bo | 28 |\n"
	blockStart := len(preamble)
	offenders := 0
	streamOneCharAtATime(t, m, block, func(tick int, _ string, f frame) {
		prefix := preamble + block[:tick]
		held := f.indicator == tableIndicator
		if !held {
			// The block is not open yet: only the very first pipes of the header
			// are allowed to precede the mount.
			if strings.Count(prefix, "\n") < 2 {
				return
			}
			t.Errorf("tick %d: the indicator unmounted mid-table (content=%q tail=%q)",
				tick, f.content, f.tail)
			offenders++
			return
		}
		if leaksTableText(f.content) {
			t.Errorf("tick %d: table text leaked into the document:\n%s", tick, f.content)
			offenders++
		}
	})

	// The block must actually have been held for a meaningful number of ticks —
	// otherwise the assertions above proved nothing.
	if got := m.aiStreamRenderer.hold.RowCount(); got != 4 {
		t.Errorf("holdback rows = %d, want 4 — the block was never really held", got)
	}
	_ = blockStart
	if offenders > 0 {
		t.Fatalf("%d mid-table violations", offenders)
	}
}

// tableIndicator is the canonical sentence the holdback mounts. It is read from
// the state machine rather than hard-coded so a rewording of the clause is a
// deliberate, visible change to this test rather than a silent divergence.
var tableIndicator = states.StateTablePending.Describe("")

// TestStreamingTableMountsTheShimmerSkeletonContinuously is the visual half of
// the DoD: the row is the single-line shimmer skeleton, on every frame of the
// holdback, and it is exactly one physical row.
func TestStreamingTableMountsTheShimmerSkeletonContinuously(t *testing.T) {
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, "Here are the results:\n\n", func(int, string, frame) {})

	base := len(m.renderTailPanelLines())
	mounted := 0
	streamOneCharAtATime(t, m, "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n", func(tick int, _ string, f frame) {
		if f.indicator != tableIndicator {
			return
		}
		mounted++
		rows := len(m.renderTailPanelLines())
		if rows != base+1 {
			t.Errorf("tick %d: the indicator occupied %d rows, want exactly 1", tick, rows-base)
		}
		line := m.skeletonRenderLine()
		if strings.ContainsAny(line, "\n\r") {
			t.Errorf("tick %d: the indicator wrapped: %q", tick, line)
		}
		if !strings.Contains(line, "\x1b[38;2;") {
			t.Errorf("tick %d: the indicator is not shimmering: %q", tick, line)
		}
		if animation.VisibleWidth(line) > m.wrapWidth {
			t.Errorf("tick %d: the indicator exceeded the width budget: %d cells", tick, animation.VisibleWidth(line))
		}
	})
	if mounted < 10 {
		t.Fatalf("the indicator was mounted on only %d ticks; the test proved too little", mounted)
	}
}

// TestTableHoldbackIsByteStableAcrossTicks is the "ZERO screen flashing" DoD
// stated as a byte comparison. While the holdback is accumulating, every single
// frame must be BYTE-IDENTICAL to the one before it: same document, same tail,
// same footer. A single differing byte is a redraw, and a redraw is the flicker.
func TestTableHoldbackIsByteStableAcrossTicks(t *testing.T) {
	m := newTableDoDModel(t)
	m.sessionHasRunPrompts = true
	streamOneCharAtATime(t, m, "Here are the results:\n\n", func(int, string, frame) {})

	var prev frame
	stable := true
	streamOneCharAtATime(t, m, "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n| Bo | 28 |\n",
		func(tick int, _ string, f frame) {
			if tick == 1 {
				prev = f
				return
			}
			if f.content != prev.content {
				t.Errorf("tick %d: the document changed while the holdback was accumulating:\n before:\n%s\n after:\n%s",
					tick, prev.content, f.content)
				stable = false
			}
			if f.tail != prev.tail {
				t.Errorf("tick %d: the tail changed while the holdback was accumulating:\n before:\n%s\n after:\n%s",
					tick, prev.tail, f.tail)
				stable = false
			}
			// The shimmer GLYPH advances by design, so the footer and the
			// indicator sentence are what must be stable, not their raw bytes.
			if f.footer != prev.footer && f.indicator != "" {
				t.Errorf("tick %d: the status bar changed mid-holdback:\n before: %q\n after:  %q",
					tick, prev.footer, f.footer)
				stable = false
			}
			prev = f
		})
	if !stable {
		t.Fatal("the holdback window was not visually stable")
	}
}

// TestTableLatchEngagesOnceAndReleasesOnce is the hysteresis DoD stated as
// bookkeeping rather than pixels: over the entire block the latch transitions
// exactly ON→OFF. Any second engage or any early release is oscillation, and the
// counters make it unmissable.
func TestTableLatchEngagesOnceAndReleasesOnce(t *testing.T) {
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, "Here are the results:\n\n", func(int, string, frame) {})

	engagedBefore, releasedBefore := m.ensureTableLatch().Counts()
	streamOneCharAtATime(t, m, "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n| Bo | 28 |\n",
		func(tick int, _ string, f frame) {
			if f.indicator != tableIndicator {
				t.Errorf("tick %d: the indicator is not mounted", tick)
			}
		})
	engagedDuring, releasedDuring := m.ensureTableLatch().Counts()
	if engagedDuring-engagedBefore != 1 {
		t.Errorf("the latch engaged %d times across one table block, want 1",
			engagedDuring-engagedBefore)
	}
	if releasedDuring-releasedBefore != 0 {
		t.Errorf("the latch released %d times BEFORE the block terminated", releasedDuring-releasedBefore)
	}

	// The blank line is the double-line-break termination criterion.
	m.emitVisibleContent("\n")
	m.refreshViewportContent()
	engagedAfter, releasedAfter := m.ensureTableLatch().Counts()
	if releasedAfter-releasedDuring != 1 {
		t.Errorf("the terminating blank line released the latch %d times, want 1",
			releasedAfter-releasedDuring)
	}
	if engagedAfter-engagedDuring != 0 {
		t.Errorf("the latch engaged again after the block terminated")
	}
}

// TestTableIndicatorIsNotRemountedMidBlock is the "no row blink" DoD. A release
// followed by a re-mount would drop the row for one frame and restart the
// shimmer glyph from frame zero; the lifecycle epoch counts mounts and releases,
// so a stable row count is a constant epoch.
func TestTableIndicatorIsNotRemountedMidBlock(t *testing.T) {
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, "Here are the results:\n\n", func(int, string, frame) {})
	streamOneCharAtATime(t, m, "|", func(int, string, frame) {})
	epoch := m.lifecycle.Epoch()
	streamOneCharAtATime(t, m, " Name | Age |\n| --- | --- |\n| Ana | 31 |\n| Bo | 28 |\n",
		func(tick int, _ string, f frame) {
			if m.lifecycle.Epoch() != epoch {
				t.Fatalf("tick %d: the indicator was remounted (epoch %d → %d); the row would blink",
					tick, epoch, m.lifecycle.Epoch())
			}
		})
}

// ── DoD 3: one-shot atomic replacement ──────────────────────────────────────

// TestTerminatedTableRendersAtomically is the swap DoD. On the terminating
// blank line the document gains the COMPLETE grid — every row, every column — and
// the indicator is gone in the very same projection. There is no frame in which
// the indicator coexists with the table, and none in which a partial grid is
// drawn.
func TestTerminatedTableRendersAtomically(t *testing.T) {
	m := newTableDoDModel(t)
	// The block, INCLUDING the newline that commits its last row. A table is
	// terminated by a blank line, so a second newline is still owed.
	streamOneCharAtATime(t, m, "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n| Bo | 28 |\n",
		func(int, string, frame) {})

	// Mid-block: the document is still empty of table content.
	if leaksTableText(m.visibleDocument()) {
		t.Fatalf("table content leaked before termination:\n%s", m.visibleDocument())
	}
	if m.skeletonStatusMirror() != tableIndicator {
		t.Fatalf("the indicator is not mounted before termination: %q", m.skeletonStatusMirror())
	}

	// The terminating blank line — the double line break "\n\n".
	m.emitVisibleContent("\n")
	m.refreshViewportContent()

	doc := m.visibleDocument()
	if m.skeletonStatusMirror() != "" {
		t.Errorf("the indicator survived the block's termination: %q", m.skeletonStatusMirror())
	}
	if !hasTableGrid(doc) {
		t.Fatalf("the completed table is not in the document:\n%s", doc)
	}
	// The whole grid, not a prefix of it.
	for _, want := range []string{"Name", "Age", "Ana", "31", "Bo", "28"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the atomic render is missing %q:\n%s", want, doc)
		}
	}
	// The grid is complete: one top border, one header separator, one bottom.
	if got := strings.Count(doc, "┌"); got != 1 {
		t.Errorf("the rendered table has %d top borders, want 1 (a re-laid-out grid):\n%s", got, doc)
	}
	if got := strings.Count(doc, "├"); got != 1 {
		t.Errorf("the rendered table has %d header separators, want 1:\n%s", got, doc)
	}
	if got := strings.Count(doc, "└"); got != 1 {
		t.Errorf("the rendered table has %d bottom borders, want 1:\n%s", got, doc)
	}
}

// TestTableGridIsNeverRedrawn is the "one pass" DoD. Once the grid exists, the
// remaining rows of the table must not cause it to be re-laid-out: the border
// character census has to stay identical, because a redraw is how a table makes
// the document jump under the cursor.
func TestTableGridIsNeverRedrawn(t *testing.T) {
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n", func(int, string, frame) {})
	m.emitVisibleContent("\n")
	m.refreshViewportContent()
	settled := borderCensus(m.visibleDocument())
	if settled["┌"] == 0 {
		t.Fatalf("precondition: no grid was rendered:\n%s", m.visibleDocument())
	}
	// More rows, arriving after the grid would already have been drawn... which
	// cannot happen for a correct terminator, so drive the post-block tail and
	// assert the grid is untouched.
	streamOneCharAtATime(t, m, "That is the full roster.\n\nAnd a closing remark.", func(int, string, frame) {})
	if got := borderCensus(m.visibleDocument()); !sameCensus(got, settled) {
		t.Errorf("the table grid was redrawn after it settled:\n before %v\n after  %v", settled, got)
	}
}

// borderCensus counts every box-drawing rune in a rendered document, which is a
// fingerprint of a table's exact geometry.
func borderCensus(s string) map[string]int {
	out := map[string]int{}
	for _, r := range s {
		if strings.ContainsRune(tableGridRunes, r) {
			out[string(r)]++
		}
	}
	return out
}

func sameCensus(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestNoFrameEverShowsBothTheIndicatorAndTheTable is the atomicity clause stated
// as an exhaustive scan rather than a spot check: over every tick of the stream,
// the indicator and the rendered grid are never both on screen.
func TestNoFrameEverShowsBothTheIndicatorAndTheTable(t *testing.T) {
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, tableDoD, func(tick int, _ string, f frame) {
		indicatorMounted := f.indicator != ""
		gridDrawn := hasTableGrid(f.content)
		if indicatorMounted && gridDrawn {
			t.Fatalf("tick %d: the indicator and a rendered table are both on screen:\n%s\n--- tail ---\n%s",
				tick, f.content, f.tail)
		}
	})
	// And the end state is the table with no indicator.
	f := m.observe()
	if f.indicator != "" {
		t.Errorf("the indicator is still mounted after the stream")
	}
	if !hasTableGrid(f.content) {
		t.Errorf("the table is not on screen after the stream:\n%s", f.content)
	}
}

// ── DoD 4: the status bar carries NO transient indicator ─────────────────────

// transientIndicators is every transient lifecycle sentence the UI can mount.
// The status bar must never render any of them, at any width, in any session
// state: the indicator is claimed EXCLUSIVELY inside the viewport, on the
// streaming cursor line.
var transientIndicators = []string{
	"[struct] Constructing table view...",
	"[code] Formatting code block...",
	"[mutation] Staging edit",
	"[exec] Preparing tool execution...",
	"[index] Mapping workspace context...",
}

// TestStatusBarNeverCarriesATransientIndicator is the anti-redundancy DoD. For
// the entire holdback window the fixed bottom bar stays free of the canonical
// indicator sentence, at every width, in all three bar states (executing,
// fresh launch, idle). The claim lives in the viewport only.
//
// The failure this pins is a user seeing the same sentence twice — once on the
// streaming cursor line and once on the bar — because two surfaces reporting one
// moving fact is how a UI ends up showing "constructing" and "done" at the same
// time.
func TestStatusBarNeverCarriesATransientIndicator(t *testing.T) {
	for _, sessionRun := range []bool{true, false} {
		m := newTableDoDModel(t)
		m.sessionHasRunPrompts = sessionRun
		streamOneCharAtATime(t, m, "Here are the results:\n\n", func(int, string, frame) {})

		mounted := 0
		streamOneCharAtATime(t, m, "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n| Bo | 28 |\n",
			func(tick int, _ string, f frame) {
				if f.indicator != tableIndicator {
					t.Errorf("sessionRun=%v tick %d: the indicator is not mounted", sessionRun, tick)
					return
				}
				mounted++
				// The viewport is the ONLY place the sentence may appear.
				if !strings.Contains(f.tail, "Constructing table view") {
					t.Errorf("sessionRun=%v tick %d: the viewport stopped showing the indicator: %q",
						sessionRun, tick, f.tail)
				}
				for _, width := range []int{120, 96, 70, 46, 30, 20} {
					bar := animation.Strip(m.renderFixedFooter(width, nil))
					for _, banned := range transientIndicators {
						if strings.Contains(bar, banned) {
							t.Errorf("sessionRun=%v tick %d width %d: the bar duplicated %q: %q",
								sessionRun, tick, width, banned, bar)
						}
					}
				}
			})
		if mounted == 0 {
			t.Fatalf("sessionRun=%v: the test proved too little — no tick was mounted", sessionRun)
		}

		m.emitVisibleContent("\n")
		m.refreshViewportContent()
		f := m.observe()
		if f.indicator != "" {
			t.Errorf("sessionRun=%v: the indicator survived termination: %q", sessionRun, f.indicator)
		}
		// The bar is a single line no matter what it carries.
		if strings.ContainsAny(f.footer, "\n\r") {
			t.Errorf("sessionRun=%v: the bar wrapped: %q", sessionRun, f.footer)
		}
	}
}

// TestStatusBarIsByteStableAcrossTheHoldback: because the bar carries no
// transient state, it cannot be perturbed by the holdback. Every tick of the
// block must leave the bar byte-identical — the only thing allowed to animate
// is the viewport row. A bar that changed mid-holdback would mean it had picked
// up state it is not supposed to own.
func TestStatusBarIsByteStableAcrossTheHoldback(t *testing.T) {
	m := newTableDoDModel(t)
	m.sessionHasRunPrompts = true
	streamOneCharAtATime(t, m, "Here are the results:\n\n", func(int, string, frame) {})

	var prev string
	ticks := 0
	streamOneCharAtATime(t, m, "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n| Bo | 28 |\n",
		func(tick int, _ string, f frame) {
			if tick == 1 {
				prev = f.footer
				return
			}
			if f.footer != prev {
				t.Errorf("tick %d: the bar changed mid-holdback:\n before: %q\n after:  %q",
					tick, prev, f.footer)
			}
			prev = f.footer
			ticks++
		})
	if ticks == 0 {
		t.Fatal("the test proved too little — no ticks were compared")
	}
}

// TestStatusBarIsByteIdenticalOutsideTheHoldback is the complementary half: with
// nothing held back the bar is exactly the core telemetry it always was — no
// stray indicator, and a bar that is precisely the declared width.
func TestStatusBarIsByteIdenticalOutsideTheHoldback(t *testing.T) {
	m := newTableDoDModel(t)
	m.sessionHasRunPrompts = true
	for _, width := range []int{120, 96, 70, 46, 30} {
		m.width = width
		m.aiStreamRenderer = &aiBlockRenderer{}
		m.ensureTableLatch().Release()
		bar := animation.Strip(m.renderFixedFooter(width, nil))
		for _, banned := range transientIndicators {
			if strings.Contains(bar, banned) {
				t.Errorf("width %d: the bar rendered %q with nothing held back: %q", width, banned, bar)
			}
		}
		if animation.VisibleWidth(bar) != width {
			t.Errorf("width %d: the bar is %d cells, want exactly %d", width, animation.VisibleWidth(bar), width)
		}
	}
}

// ── DoD 5: stream-completion termination ────────────────────────────────────

// TestStreamEndMidTableReleasesTheLatch is the Ctrl+C / PARTIAL-truncation
// criterion. A stream that stops in the middle of a table must not leave the
// indicator claiming work that has stopped.
func TestStreamEndMidTableReleasesTheLatch(t *testing.T) {
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, "| Name | Age |\n| --- | --- |\n| Ana |", func(int, string, frame) {})
	if m.skeletonStatusMirror() != tableIndicator {
		t.Fatalf("precondition: the holdback is not active")
	}

	// The stream ends mid-row.
	m.streaming = false
	m.refreshViewportContent()

	if m.ensureTableLatch().On() {
		t.Error("the latch survived stream completion")
	}
	if m.skeletonStatusMirror() != "" {
		t.Errorf("the indicator survived stream completion: %q", m.skeletonStatusMirror())
	}
	if animation.VisibleWidth(animation.Strip(m.renderFixedFooter(m.width, nil))) > m.width {
		t.Error("the status bar exceeded its budget after stream completion")
	}
}

// TestTruncatedTableStillRendersFromTheRecord is the other half of stream-end
// termination. The live tail is discarded when a stream ends (the record path
// owns the final layout), so a table cut off by a PARTIAL truncation must still
// be laid out in full from the committed record — the holdback delays the render,
// it never cancels it.
func TestTruncatedTableStillRendersFromTheRecord(t *testing.T) {
	m := newTableDoDModel(t)
	const src = "| Name | Age |\n| --- | --- |\n| Ana | 31"
	streamOneCharAtATime(t, m, src, func(int, string, frame) {})
	m.streaming = false

	// The terminal path pushes the whole streamed content as an AI record.
	m.push(roleAI, src)
	m.refreshViewportContent()

	doc := m.visibleDocument()
	if !hasTableGrid(doc) {
		t.Fatalf("a table truncated by stream end was not laid out from the record:\n%s", doc)
	}
	for _, want := range []string{"Name", "Age", "Ana", "31"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the record render is missing %q:\n%s", want, doc)
		}
	}
	if m.skeletonStatusMirror() != "" {
		t.Errorf("an indicator is mounted over a finished table: %q", m.skeletonStatusMirror())
	}
}

// TestCompletedRecordPathLaysOutAnUnterminatedTable covers the OTHER renderer
// that shares the holdback: renderAIBlockLines, used for committed history. A
// table at the very end of a record has no terminating blank line, so it is
// released by finish() (the EOF equivalent of the stream-completion criterion)
// rather than by a terminator.
func TestCompletedRecordPathLaysOutAnUnterminatedTable(t *testing.T) {
	for _, src := range []string{
		"| Name | Age |\n| --- | --- |\n| Ana | 31 |",
		"| Name | Age |\n| --- | --- |\n| Ana | 31 |\n",
		"| Name | Age |\n| --- | --- |\n| Ana | 31 |\n\ntrailing prose",
		"leading prose\n\n| a | b |\n| 1 | 2 |",
	} {
		lines := renderAIBlockLines(src, 80)
		joined := tailText(lines)
		if !hasTableGrid(joined) {
			t.Errorf("record %q rendered no table:\n%s", src, joined)
			continue
		}
		if strings.Contains(joined, "|") {
			t.Errorf("record %q leaked a raw pipe into the layout:\n%s", src, joined)
		}
	}
}

// TestCompletedRecordPathMatchesTheStreamingLayout is the byte-parity clause.
// The streaming tail and the committed record must lay a table out IDENTICALLY,
// or a table would visibly re-flow at the moment the stream ends.
func TestCompletedRecordPathMatchesTheStreamingLayout(t *testing.T) {
	const src = "| Name | Age |\n| --- | --- |\n| Ana | 31 |\n| Bo | 28 |\n\nThat is the roster."
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, src, func(int, string, frame) {})

	// Compare the GRID REGION, not the whole document. The two paths differ
	// elsewhere for reasons that predate the holdback (the streaming tail keeps
	// a trailing gutter row for an empty partial line that the record path has
	// no equivalent of), and a table that re-flowed at the moment the stream
	// ended is exactly what this feature must prevent — so the grid itself has
	// to be byte-identical.
	streamed := gridRegion(t, m.visibleDocument(), "streaming")
	committed := gridRegion(t, tailText(renderAIBlockLines(ensurePreflightDelimiter(sanitizeText(src)), m.wrapWidth)), "committed")
	if streamed != committed {
		t.Errorf("the streaming grid and the committed grid differ:\nstreamed:\n%s\ncommitted:\n%s",
			streamed, committed)
	}
}

// gridRegion extracts the box-grid block (first top border through the matching
// bottom border) from a rendered document.
func gridRegion(t *testing.T, doc, label string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	start, end := -1, -1
	for i, l := range lines {
		if start < 0 && strings.Contains(l, "┌") {
			start = i
		}
		if start >= 0 && strings.Contains(l, "└") {
			end = i
			break
		}
	}
	if start < 0 || end < start {
		t.Fatalf("%s document has no box grid:\n%s", label, doc)
	}
	return strings.Join(lines[start:end+1], "\n")
}

// TestCtrlCMidTableClearsTheHoldback is the interrupt criterion on the real
// teardown path.
func TestCtrlCMidTableClearsTheHoldback(t *testing.T) {
	m := newTableDoDModel(t)
	m.state = StateProcessing
	m.currentPrompt = "summarise the roster"
	streamOneCharAtATime(t, m, "| Name | Age |\n| Ana |", func(int, string, frame) {})
	if !m.ensureTableLatch().On() {
		t.Fatal("precondition: the holdback is not latched")
	}

	_, _ = m.handleEmergencyInterrupt("ctrl-c")

	if m.ensureTableLatch().On() {
		t.Error("the latch survived Ctrl+C")
	}
	if m.skeletonActive() {
		t.Error("the indicator survived Ctrl+C")
	}
	if strings.Contains(animation.Strip(skeletonTailText(m)), "Constructing table view") {
		t.Error("an indicator fragment survived Ctrl+C in the tail")
	}
}

// TestClearMidTableReleasesTheHoldback: /clear dismisses the response the
// holdback belongs to, so the hold must go with it.
func TestClearMidTableReleasesTheHoldback(t *testing.T) {
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, "| Name | Age |\n| Ana |", func(int, string, frame) {})
	if !m.ensureTableLatch().On() {
		t.Fatal("precondition: the holdback is not latched")
	}
	m.clearPresentation()
	if m.ensureTableLatch().On() {
		t.Error("the latch survived /clear")
	}
	if m.skeletonActive() {
		t.Error("the indicator survived /clear")
	}
}

// TestHoldbackIsScopedToOneBlock: after the table terminates, ordinary prose
// streams through completely untouched — no lingering hold, no stray indicator,
// and no swallowing of the text that follows.
func TestHoldbackIsScopedToOneBlock(t *testing.T) {
	m := newTableDoDModel(t)
	// The table block, terminated by the blank line (the "\n\n" criterion).
	const block = "| a | b |\n| 1 | 2 |\n\n"
	const tail = "The roster is final.\n"
	streamOneCharAtATime(t, m, block, func(int, string, frame) {})
	streamOneCharAtATime(t, m, tail, func(tick int, _ string, f frame) {
		if f.indicator != "" {
			t.Errorf("tick %d: the indicator is still mounted after the block: %q", tick, f.indicator)
		}
	})
	doc := m.visibleDocument()
	if !strings.Contains(doc, "The roster is final.") {
		t.Fatalf("the prose after the table was swallowed:\n%s", doc)
	}
	if !hasTableGrid(doc) {
		t.Fatalf("the table is missing:\n%s", doc)
	}
	if m.ensureTableLatch().On() {
		t.Error("the latch is still on after the block terminated")
	}
}

// TestTwoTablesInOneStreamEachGetTheirOwnHoldback: the holdback is a BLOCK
// construct. Two tables separated by a blank line produce two independent holds,
// each rendered atomically, with no state carried between them.
func TestTwoTablesInOneStreamEachGetTheirOwnHoldback(t *testing.T) {
	m := newTableDoDModel(t)
	src := "| a | b |\n| 1 | 2 |\n\n| c | d |\n| 3 | 4 |\n\ndone\n"
	holds := 0
	wasHeld := false
	// grids counts laid-out tables. While a holdback is open the count must be
	// CONSTANT: the block being held may not draw a grid, and a grid already on
	// screen may not be re-laid-out. Any change is a flicker.
	grids, prevGrids := 0, 0
	streamOneCharAtATime(t, m, src, func(tick int, _ string, f frame) {
		held := f.indicator == tableIndicator
		if held && !wasHeld {
			holds++
		}
		wasHeld = held
		grids = strings.Count(f.content, "┌")
		if held {
			if grids != prevGrids {
				t.Errorf("tick %d: %d tables on screen while a holdback is open (was %d); the held block drew a grid",
					tick, grids, prevGrids)
			}
		} else if grids < prevGrids {
			t.Errorf("tick %d: a rendered table disappeared (%d → %d)", tick, prevGrids, grids)
		}
		prevGrids = grids
	})
	if holds != 2 {
		t.Errorf("observed %d holdback windows, want 2 (one per table block)", holds)
	}
	doc := m.visibleDocument()
	for _, want := range []string{"1", "2", "3", "4", "done"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the stream is missing %q:\n%s", want, doc)
		}
	}
	if got := strings.Count(doc, "┌"); got != 2 {
		t.Errorf("the document has %d rendered tables, want 2:\n%s", got, doc)
	}
}

// TestProseThatStartsWithAPipeDoesNotStrandTheHoldback is the false-positive
// guard. A pipe-leading line that is NOT a row must not leave the indicator
// mounted over a finished paragraph: the holdback closes on commit, the line
// renders as prose, and the claim goes away.
func TestProseThatStartsWithAPipeDoesNotStrandTheHoldback(t *testing.T) {
	m := newTableDoDModel(t)
	const prose = "Use the | operator to join commands.\n\nThen run it.\n"
	streamOneCharAtATime(t, m, prose, func(tick int, _ string, f frame) {
		if tick < len(prose) {
			return // mid-line, the mount is legitimate
		}
		if f.indicator != "" {
			t.Errorf("tick %d: an indicator is stranded over prose: %q", tick, f.indicator)
		}
	})
	if m.ensureTableLatch().On() {
		t.Error("the latch is stranded after pipe-leading prose committed")
	}
	doc := m.visibleDocument()
	if !strings.Contains(doc, "operator to join") {
		t.Fatalf("the prose was swallowed by the holdback:\n%s", doc)
	}
}

// TestCodeFenceSupersedesTheTableHoldback is the priority rule: an open fence is
// a strictly stronger claim than a held-back table, so it takes the row over and
// the two indicators never both claim it.
func TestCodeFenceSupersedesTheTableHoldback(t *testing.T) {
	m := newTableDoDModel(t)
	streamOneCharAtATime(t, m, "```go\nfunc main() {}\n```\n", func(tick int, _ string, f frame) {
		if f.indicator == tableIndicator {
			t.Errorf("tick %d: the table indicator is mounted inside a code fence", tick)
		}
	})
	if m.skeletonStatusMirror() != "" {
		t.Errorf("the code indicator survived its closing fence: %q", m.skeletonStatusMirror())
	}
	if m.ensureTableLatch().On() {
		t.Error("a code block latched the TABLE holdback")
	}
}

// TestHoldbackLeavesNoTokenBehind proves the buffer is lossless: the rows that
// reached the holdback are exactly the rows that came out of it, in order, with
// no row dropped and none duplicated.
func TestHoldbackLeavesNoTokenBehind(t *testing.T) {
	rows := []string{
		"| Name | Age |",
		"| --- | --- |",
		"| Ana | 31 |",
		"| Bo | 28 |",
		"| Cy | 45 |",
	}
	m := newTableDoDModel(t)
	for _, row := range rows {
		streamOneCharAtATime(t, m, row+"\n", func(int, string, frame) {})
	}
	held := m.aiStreamRenderer.hold.Rows()
	if len(held) != len(rows) {
		t.Fatalf("holdback holds %d rows, want %d", len(held), len(rows))
	}
	for i, want := range rows {
		if held[i] != want {
			t.Errorf("holdback row %d = %q, want %q", i, held[i], want)
		}
	}
	m.emitVisibleContent("\n")
	m.refreshViewportContent()
	doc := m.visibleDocument()
	for _, want := range []string{"Name", "Age", "Ana", "31", "Bo", "28", "Cy", "45"} {
		if n := strings.Count(doc, want); n == 0 {
			t.Errorf("the rendered grid is missing %q:\n%s", want, doc)
		}
	}
	// Exactly one occurrence of each cell value: no duplicated row.
	if n := strings.Count(doc, "Ana"); n != 1 {
		t.Errorf("cell %q appears %d times; a row was duplicated:\n%s", "Ana", n, doc)
	}
}

// TestHoldbackBufferExposesItsContents is the seam the renderer actually uses,
// asserted directly so a refactor cannot quietly drop the accumulation.
func TestHoldbackBufferExposesItsContents(t *testing.T) {
	h := markdown.NewTableHoldback()
	if h.Active() {
		t.Fatal("a fresh holdback is active")
	}
	h.Engage()
	h.Feed("| a | b |")
	if !h.Active() || h.RowCount() != 1 {
		t.Fatalf("holdback = active:%v rows:%d", h.Active(), h.RowCount())
	}
	rows, ok := h.Drain()
	if !ok || len(rows) != 1 {
		t.Fatalf("Drain = (%q, %v)", rows, ok)
	}
	if h.Active() {
		t.Error("Drain left the holdback active")
	}
}
