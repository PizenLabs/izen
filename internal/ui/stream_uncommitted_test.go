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
// hold-back run — a sequence of prefixes that are ALL rendered by the SAME
// mechanism — the rendered tail may only grow: the line count must never
// decrease and the text already on screen must never be rewritten. A decrease or
// a rewrite is a visible pop.
//
// A hold-back run ends at either of the two deliberate transitions:
//
//   - PROMOTION: the prefix becomes structurally final and goes down the full
//     pipeline (a line break, a block closure).
//   - INLINE BALANCE: the prefix acquires a transient AST parse copy, so the
//     phrase starts rendering styled instead of held. This is the same kind of
//     one-time transition — the raw marker stops being displayed because it is no
//     longer what the line is — and it is what the balance exists to cause.
//
// Both are single edges, not ongoing churn. Everything AFTER an edge must be
// monotonic, which is the property that says streaming does not flicker.
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
		"a `code span` closes here",
		"*an italic phrase* then plain",
	}
	for _, src := range sources {
		r := &aiBlockRenderer{}
		prevLines, prevText := 0, ""
		held := false
		balanced := false
		for i := 1; i <= len(src); i++ {
			prefix := src[:i]
			kind, incomplete := markdown.Incomplete(prefix, r.inCode, r.inTable)
			nowBalanced := incomplete && kind == markdown.BlockInline &&
				frameTailLine(prefix, r.inCode, r.inTable) != prefix
			if nowBalanced != balanced || (!incomplete && held) {
				// A deliberate one-time transition. Reset the comparison so the
				// expected change of rendering is not read as a pop.
				held, balanced, prevLines, prevText = false, nowBalanced, 0, ""
				if !incomplete {
					continue
				}
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

// TestInlineDelimiterRendersStyledFromTheFirstFrame is the no-raw-marker
// contract. `**bold` has an unterminated strong delimiter, and the frame's
// TRANSIENT AST BALANCE closes it for this frame's parse only — so the phrase is
// bold on the FIRST tick, with no `**` ever reaching the screen and no
// restyle-to-bold frame later.
//
// This is the case the inline hold-back could never serve. The hold-back exists
// to stop the viewport REFLOWING, and an inline delimiter cannot reflow anything:
// it changes the style of the words after it, never the number of physical rows
// or the width of any of them. Holding it back therefore bought no stability and
// cost the entire point of streaming.
func TestInlineDelimiterRendersStyledFromTheFirstFrame(t *testing.T) {
	const w = 80
	for _, tc := range []struct {
		prefix string
		want   string
		absent string
	}{
		{"**bold", "bold", "**"},
		{"*italic", "italic", "*i"},
		{"a `code", "code", "`"},
	} {
		lines := (&aiBlockRenderer{}).renderPartialTail(tc.prefix, w)
		if len(lines) == 0 {
			t.Fatalf("%q rendered nothing", tc.prefix)
		}
		rendered := lines[len(lines)-1].RenderedStr
		if got := ansi.Strip(rendered); strings.Contains(got, tc.absent) {
			t.Errorf("%q leaked its raw marker: %q", tc.prefix, got)
		}
		if !strings.Contains(ansi.Strip(rendered), tc.want) {
			t.Errorf("%q lost its content: %q", tc.prefix, ansi.Strip(rendered))
		}
		if !strings.Contains(rendered, "\x1b[") {
			t.Errorf("%q rendered unstyled: %q", tc.prefix, rendered)
		}
	}
}

// TestInlineBalanceNeverChangesTheCommittedBytes is the buffer-safety contract.
// The frame renders a balanced COPY, so every authoritative consumer of the line
// — the UncommittedBuffer, the tail memo key, and ultimately the committed record
// — must still see the exact bytes the provider sent. A committed transcript
// containing a synthetic `**` would be a content bug, not a rendering one.
func TestInlineBalanceNeverChangesTheCommittedBytes(t *testing.T) {
	const raw = "**The migration is complete and verified"
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamingDocStart = -1
	m.docLayout = &DocumentLayout{width: 80}
	m.currentStreamContent = raw

	m.syncStreamingSegment()
	if got := m.aiStreamUncommitted.Pending(); got != raw {
		t.Errorf("UncommittedBuffer.Pending() = %q, want the raw bytes %q", got, raw)
	}
	if m.aiStreamTailContent != raw {
		t.Errorf("tail memo key = %q, want the raw bytes %q", m.aiStreamTailContent, raw)
	}

	// The frame shows styled text...
	tail := m.docLayout.Lines[m.streamingDocStart:]
	if len(tail) == 0 {
		t.Fatal("the streaming tail rendered no rows")
	}
	shown := ansi.Strip(tail[len(tail)-1].RenderedStr)
	if strings.Contains(shown, "**") {
		t.Errorf("the streaming frame shows a raw marker: %q", shown)
	}
	if !strings.Contains(shown, "The migration is complete and verified") {
		t.Errorf("the streaming frame lost content: %q", shown)
	}

	// ...and the line that COMMITS carries the provider's own bytes, with no
	// synthetic closer baked into it. An unbalanced `**` is what the model
	// actually wrote, so the committed transcript must show exactly that.
	committed := renderAIBlockLines(raw, 80)
	if len(committed) == 0 {
		t.Fatal("the committed render produced no lines")
	}
	if got := ansi.Strip(committed[0].RenderedStr); !strings.Contains(got, raw) {
		t.Errorf("the committed line is not the raw bytes: got %q, want %q", got, raw)
	}
}

// TestBlockGrammarIsStillHeldBack is the counterpart: the balance applies to
// INLINE delimiters only. A line that is block-incomplete must keep the
// hold-back treatment, because those are grammar that changes what the line IS.
func TestBlockGrammarIsStillHeldBack(t *testing.T) {
	const w = 80
	// An arriving fence: a transient closer on a fence marker would hand the
	// rest of the line to a code block that does not exist yet.
	for _, partial := range []string{"`", "``"} {
		lines := (&aiBlockRenderer{}).renderPartialTail(partial, w)
		if len(lines) == 0 {
			t.Errorf("partial fence %q rendered nothing", partial)
		}
		if got := ansi.Strip(lines[len(lines)-1].RenderedStr); !strings.Contains(got, "`") {
			t.Errorf("partial fence %q lost its marker (it must be held, not closed): %q", partial, got)
		}
	}
	// An unclosed link is block-incomplete for the holdback and is NOT closed
	// transiently: a `)` would fabricate a destination the model never wrote.
	lines := (&aiBlockRenderer{}).renderPartialTail("see [the docs](https://exa", w)
	if len(lines) == 0 {
		t.Fatal("an unclosed link rendered nothing")
	}
	if got := ansi.Strip(lines[len(lines)-1].RenderedStr); strings.Contains(got, "https://exa)") {
		t.Errorf("an unclosed link was given a synthetic closer: %q", got)
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
//
// Like production it also feeds the frame's TRANSIENT PARSE COPY to the markdown
// pass (frameTailLine), so an inline delimiter with no closer yet is styled from
// the first frame instead of flashing its raw marker.
func (r *aiBlockRenderer) renderPartialTail(rl string, wrapWidth int) []DocumentLine {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	probe := r.clone()
	probe.renderPartialFramed(rl, frameTailLine(rl, r.inCode, r.inTable), wrapWidth)
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
