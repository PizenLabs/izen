package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/ui/markdown"
)

// tailText renders the streaming tail the way the viewport does: the ANSI-stripped
// concatenation of every DocumentLine, so a test can assert on what the user
// actually sees rather than on internal bookkeeping.
func tailText(lines []DocumentLine) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, ansi.Strip(l.RenderedStr))
	}
	return strings.Join(parts, "\n")
}

// tailWidths returns the visible cell width of every tail line, so a reflow can
// be detected as a change in the line set.
func tailWidths(lines []DocumentLine) []int {
	out := make([]int, 0, len(lines))
	for _, l := range lines {
		out = append(out, ansi.StringWidth(ansi.Strip(l.RenderedStr)))
	}
	return out
}

// TestPartialTableRowIsDivertedNotRendered is the anti-flicker contract for
// tables, in its strongest form. A row whose closing pipe has not arrived must
// not reach the viewport AT ALL — not as a wrapped paragraph that re-flows into
// a box grid, and not even as plain dimmed text, because a plain-text prefix
// still reflows as the remaining cells arrive and still grows the document by a
// line per row. A leading pipe commits the line to table grammar, so it is
// diverted into the full holdback and the one-line skeleton is the only thing on
// screen.
func TestPartialTableRowIsDivertedNotRendered(t *testing.T) {
	const w = 80
	// Every prefix of this row UP TO the closing pipe is structurally
	// incomplete; the closing pipe itself makes the line final but does NOT
	// make it renderable — a grid needs the whole block.
	heldPrefixes := []string{"|", "| Name", "| Name | Age"}

	r := &aiBlockRenderer{}
	for _, p := range heldPrefixes {
		if kind, ok := markdown.Incomplete(p, false, false); !ok || kind != markdown.BlockTable {
			t.Fatalf("prefix %q classified as (%v, %v), want (table, true)", p, kind, ok)
		}
		lines := r.renderPartialTail(p, w)
		if len(lines) != 0 {
			t.Errorf("prefix %q reached the viewport:\n%s", p, tailText(lines))
		}
		if !r.tableHolding() {
			t.Fatalf("prefix %q did not latch the holdback", p)
		}
	}
	// Nothing was ever rendered, so nothing can reflow.
	if len(r.out) != 0 {
		t.Fatalf("the holdback leaked %d lines into the viewport:\n%s", len(r.out), tailText(r.out))
	}
	// Even a structurally COMPLETE row is not renderable on its own: it is
	// diverted too, and the accumulated prefix is retained verbatim for the
	// one-shot render.
	if lines := r.renderPartialTail("| Name | Age |", w); len(lines) != 0 {
		t.Fatalf("a complete but unterminated row reached the viewport:\n%s", tailText(lines))
	}
	if !r.tableHolding() {
		t.Fatal("a complete unterminated row released the holdback")
	}
	if got := r.hold.Pending(); got != "| Name | Age |" {
		t.Errorf("the holdback holds %q, want the full row", got)
	}
}

// TestPartialLineReflowIsMonotonic is the observable flicker metric. Within one
// hold-back run — a sequence of prefixes that are ALL still structurally
// incomplete — the rendered tail may only grow: the line count must never
// decrease and the text already on screen must never be rewritten. A decrease or
// a rewrite is a visible pop.
//
// The moment a prefix becomes structurally final it is PROMOTED to the full
// pipeline, and that single transition is expected to change the rendering: a
// line break or a block closure arriving is exactly what the contract allows.
func TestPartialLineReflowIsMonotonic(t *testing.T) {
	const w = 80
	sources := []string{
		"Here is a summary table of the results we collected during the run",
		"| Name | Age | City |",
		"**bold conclusion** and then a normal tail sentence",
		"## Section heading that keeps growing for a while",
		"```go",
		"func main() {",
		"~~struck phrase~~ followed by plain text",
		"see [the docs](https://example.com) for detail",
	}
	for _, src := range sources {
		r := &aiBlockRenderer{}
		prevLines, prevText := 0, ""
		held := false
		for i := 1; i <= len(src); i++ {
			prefix := src[:i]
			_, incomplete := markdown.Incomplete(prefix, r.inCode, r.inTable)
			if !incomplete && held {
				// Promotion boundary: the line just became structurally final.
				// Reset the comparison so the (expected) restyle is not read as
				// a pop.
				held, prevLines, prevText = false, 0, ""
				continue
			}
			lines := r.renderPartialTail(prefix, w)
			if incomplete {
				held = true
			}
			if len(lines) == 0 {
				continue
			}
			if len(lines) < prevLines {
				t.Errorf("source %q: line count shrank %d -> %d at prefix %q: the tail popped",
					src, prevLines, len(lines), prefix)
			}
			text := tailText(lines)
			if prevText != "" && !strings.HasPrefix(text, prevText) && !strings.HasPrefix(prevText, text) {
				t.Errorf("source %q: rendered tail diverged at prefix %q\n prev: %q\n now:  %q",
					src, prefix, prevText, text)
			}
			prevLines, prevText = len(lines), text
		}
	}
}

// TestUncommittedLineUsesPlainDimmedStyle pins the visual treatment: a held-back
// line is dimmed and carries NO bold/italic SGR, so the eye reads it as "still
// arriving" instead of watching markup flash in and out.
func TestUncommittedLineUsesPlainDimmedStyle(t *testing.T) {
	const w = 80
	// `**bold` has an unterminated strong delimiter.
	lines := (&aiBlockRenderer{}).renderPartialTail("**bold", w)
	if len(lines) == 0 {
		t.Fatal("no lines rendered for an unterminated bold prefix")
	}
	rendered := lines[len(lines)-1].RenderedStr
	if !strings.Contains(rendered, "\x1b[") {
		t.Error("a held-back line must still be styled (dimmed)")
	}
	if strings.Contains(rendered, "\x1b[1m") {
		t.Errorf("a held-back line must not be bold yet: %q", ansi.Strip(rendered))
	}
	if !strings.Contains(ansi.Strip(rendered), "**bold") {
		t.Errorf("a held-back line must show the raw text as typed, got %q", ansi.Strip(rendered))
	}
}

// TestCommittedLineStillGetsFullStyling is the other half: the hold-back is a
// STREAMING-only concession. Once a line terminates it must render through the
// complete pipeline exactly as before, or completed responses would come back
// permanently dimmed.
func TestCommittedLineStillGetsFullStyling(t *testing.T) {
	const w = 80
	committed := renderAIBlockLines("**bold conclusion**", w)
	if len(committed) == 0 {
		t.Fatal("committed line produced no lines")
	}
	joined := tailText(committed)
	if strings.Contains(joined, "**") {
		t.Errorf("a committed **bold** span still shows its markers: %q", joined)
	}
	if !strings.Contains(joined, "bold conclusion") {
		t.Errorf("committed line lost its content: %q", joined)
	}
	if !strings.ContainsAny(committed[0].RenderedStr, "\x1b[1m") {
		t.Errorf("a committed **bold** span must render bold: %q", committed[0].RenderedStr)
	}
}

// TestStreamingAndCommittedPathsAgreeOnWidth proves the hold-back does not change
// the LINE COUNT budget: a line held back and the same line committed must
// occupy a comparable number of physical rows, so the promotion at commit is a
// style change and not a scroll jump.
func TestStreamingAndCommittedPathsAgreeOnWidth(t *testing.T) {
	const w = 80
	body := "Here is a reasonably long sentence that will certainly need to be wrapped across more than one physical line in the viewport"
	held := (&aiBlockRenderer{}).renderPartialTail(body, w)
	committed := renderAIBlockLines(body, w)
	if len(held) != len(committed) {
		t.Errorf("a structurally final partial line wrapped to %d rows but the committed path produced %d",
			len(held), len(committed))
	}
	for i := range held {
		if a, b := tailWidths(held)[i], tailWidths(committed)[i]; a != b {
			t.Errorf("row %d: held-back width %d != committed width %d", i, a, b)
		}
	}
}

// TestCodeFenceIsNotInterpretedMidStream guards the fence transition: a partial
// marker must not toggle the block state, or the renderer would open a code
// block one frame early and drop the fence line it was still typing.
func TestCodeFenceIsNotInterpretedMidStream(t *testing.T) {
	const w = 80
	r := &aiBlockRenderer{}
	for _, partial := range []string{"`", "``"} {
		lines := r.renderPartialTail(partial, w)
		if r.inCode {
			t.Errorf("partial fence %q opened a code block", partial)
		}
		if len(lines) == 0 {
			t.Errorf("partial fence %q rendered nothing", partial)
		}
	}
	// The full marker is a real fence and must open the block.
	r.renderPartialTail("```go", w)
	if !r.inCode {
		t.Error("a complete ``` marker must open a code block")
	}
}

// TestTableCommitsIntoTheBoxGridOnce is the promotion path, and the DoD clause
// for the FULL HOLDBACK: every row of the block is diverted (so the tail stays
// EMPTY — no partial grid is ever visible), and the terminating blank line
// releases the latch and renders the whole table in a single pass.
func TestTableCommitsIntoTheBoxGridOnce(t *testing.T) {
	const w = 80
	r := &aiBlockRenderer{}
	rows := []string{"| Name | Age |", "|---|---|", "| Ana | 31 |"}
	for _, row := range rows {
		r.renderLine(row, w)
		// The persistent state must have buffered the row as a table, and
		// NOTHING may have reached the viewport.
		if !r.inTable {
			t.Fatalf("row %q did not latch the holdback", row)
		}
		if len(r.out) != 0 {
			t.Fatalf("row %q leaked %d lines into the viewport before the block completed:\n%s",
				row, len(r.out), tailText(r.out))
		}
	}
	if got := r.hold.RowCount(); got != len(rows) {
		t.Fatalf("holdback rows = %d, want %d", got, len(rows))
	}

	// The blank line is the explicit termination signal.
	r.renderLine("", w)
	if r.inTable {
		t.Fatal("the blank line did not release the holdback")
	}
	joined := tailText(r.out)
	for _, want := range []string{"┌", "├", "└", "Ana", "31"} {
		if !strings.Contains(joined, want) {
			t.Errorf("committed table missing %q:\n%s", want, joined)
		}
	}
}

// TestUncommittedBufferIsWiredIntoTheStreamingTail proves the model's tail
// renderer keeps an explicit UncommittedBuffer and feeds it the partial line, so
// the hold-back decision is observable in production rather than only in unit
// tests.
func TestUncommittedBufferIsWiredIntoTheStreamingTail(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamingDocStart = -1
	m.docLayout = &DocumentLayout{width: 80}

	m.currentStreamContent = "| Name | Age"
	m.syncStreamingSegment()
	if m.aiStreamUncommitted == nil {
		t.Fatal("the streaming tail must own an UncommittedBuffer")
	}
	if got := m.aiStreamUncommitted.Pending(); got != "| Name | Age" {
		t.Errorf("UncommittedBuffer.Pending() = %q, want the partial line", got)
	}
	if kind, ok := m.aiStreamUncommitted.Kind(); !ok || kind != markdown.BlockTable {
		t.Errorf("UncommittedBuffer.Kind() = (%v, %v), want (table, true)", kind, ok)
	}

	// Completing the row clears the pending classification.
	m.currentStreamContent = "| Name | Age |\nnext line"
	m.syncStreamingSegment()
	if got := m.aiStreamUncommitted.Pending(); got != "next line" {
		t.Errorf("after commit Pending() = %q, want %q", got, "next line")
	}
	if _, ok := m.aiStreamUncommitted.Kind(); ok {
		t.Error("a committed line must not be reported as incomplete")
	}
}

// TestUncommittedBufferResetsAtStreamBoundaries guards cross-turn leakage: a
// finished stream must never leave its uncommitted tail in the buffer where the
// next turn would render it.
func TestUncommittedBufferResetsAtStreamBoundaries(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamingDocStart = -1
	m.docLayout = &DocumentLayout{width: 80}
	m.currentStreamContent = "| Name | Ag"
	m.syncStreamingSegment()
	if !m.aiStreamUncommitted.HasPending() {
		t.Fatal("precondition: an uncommitted tail is buffered")
	}
	m.streaming = false
	m.syncStreamingSegment()
	if m.aiStreamUncommitted.HasPending() {
		t.Error("stream teardown must clear the UncommittedBuffer")
	}
}

// renderPartialTail renders one still-growing line through a fresh clone of r's
// block state, mirroring exactly what renderStreamingTail does per tick: a
// structurally incomplete line is held back, a final one goes through the full
// pipeline and advances the persistent state.
//
// The FULL HOLDBACK branch comes first and renders nothing at all, matching
// production: a table block that is latched diverts even a structurally
// complete trailing line, because a grid's layout is not knowable until the
// whole block has arrived. A trailing line that merely LOOKS like a table row
// latches the holdback too — a leading pipe commits the line to table grammar.
func (r *aiBlockRenderer) renderPartialTail(rl string, wrapWidth int) []DocumentLine {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	if r.tableHolding() {
		r.ensureHold().Hold(rl)
		return nil
	}
	kind, incomplete := markdown.Incomplete(rl, r.inCode, r.inTable)
	if incomplete {
		if kind == markdown.BlockTable {
			r.engageTable()
			r.ensureHold().Hold(rl)
			return nil
		}
		return renderUncommittedLine(rl, wrapWidth, kind)
	}
	probe := r.clone()
	probe.renderLine(rl, wrapWidth)
	r.adopt(probe)
	return probe.out
}

// clone copies the renderer's block state so a partial render can never advance
// the persistent state. The holdback pointer is SHARED (not deep-copied) because
// the latch is one resource: a probe that opened a hold and a parent that never
// learned about it would strand the holdback and its rows.
func (r *aiBlockRenderer) clone() *aiBlockRenderer {
	return &aiBlockRenderer{
		inCode:    r.inCode,
		lang:      r.lang,
		codeLines: append([]string(nil), r.codeLines...),
		inTable:   r.inTable,
		hold:      r.hold,
	}
}

// adopt copies the probe's advanced block state back onto r. Only the block
// state is adopted — never `out`, which is the per-tick render output.
func (r *aiBlockRenderer) adopt(p *aiBlockRenderer) {
	r.inCode, r.lang, r.codeLines = p.inCode, p.lang, p.codeLines
	r.inTable, r.hold = p.inTable, p.hold
}
