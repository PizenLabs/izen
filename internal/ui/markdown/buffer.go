// buffer.go — FULL HOLDBACK BUFFER for a streaming Markdown table block.
//
// # THE PROBLEM THIS SOLVES
//
// A Markdown table is the one construct whose layout is a function of its WHOLE
// body: the column widths are `max(cell width)` across the header AND every
// data row, and the box frame is derived from the resulting grid. So no prefix
// of a table has a stable layout. `| Name | Age` has three possible column
// widths and `| Name | Age |` has four, and a viewport that renders the prefix
// must therefore redraw every row the instant the closing pipe arrives.
//
// Worse, the redraw is not a redraw — it is a SCROLL. Each additional row makes
// the grid one physical line taller, so the document grows under the cursor and
// everything below it moves. At 100 tok/s that is the "table columns jump around
// and the screen flashes while the model is still writing" report.
//
// # THE CONTRACT
//
// A TableHoldback is a LATCH plus a byte buffer:
//
//   - LATCH ON. The first line that is syntactically a table row engages the
//     holdback. From that instant EVERY subsequent line of the block — complete
//     rows, the still-growing trailing line, cell fragments — is diverted into
//     the buffer and NOTHING reaches the viewport. There is no partial row, no
//     unparsed cell, and no re-laid-out grid: the single-line
//     "Constructing table view..." indicator is the only honest thing the
//     viewport can show, and it is the only thing it shows.
//
//   - LATCH OFF. The latch is released ONLY by an explicit termination signal
//     (see Termination). An intermediate chunk that merely lacks a pipe — a
//     bare word, a fragment of the next paragraph — is NOT a terminator and
//     never releases it. This is the hysteresis: without it the indicator
//     oscillates on and off once per cell boundary, which is the flicker.
//
//   - ONE-SHOT RENDER. Drain hands the ENTIRE accumulated block back at once, so
//     the caller renders the grid in a single pass and swaps it into the
//     viewport atomically. Nothing is ever rendered twice and nothing is
//     rendered from a half-populated buffer.
//
// A nil *TableHoldback is inert: every method is a no-op, so a renderer can hold
// one unconditionally and the zero value needs no constructor-only invariants.
package markdown

import "strings"

// TableRow reports whether a TRIMMED line is a pipe-delimited Markdown table
// row: it must open with '|' and carry at least one further '|'. It is the
// single definition of "this line belongs to a table body" — the holdback's
// latch trigger, its row accumulator, and the renderer's own row detector all
// resolve through this one predicate, so they can never disagree about where a
// block begins.
func TableRow(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "|") {
		return false
	}
	return strings.Contains(strings.TrimPrefix(trimmed, "|"), "|")
}

// HasUnescapedPipe reports whether s carries at least one unescaped cell
// boundary. A backslash-escaped "\|" is literal cell text, not a boundary, so
// the decision about whether a line still "belongs to a table" ignores it.
func HasUnescapedPipe(s string) bool {
	return strings.Contains(unescapePipes(s), "|")
}

// Termination names the explicit signal that ends a held-back table block, and
// the classification of each line fed to the holdback. It is a small vocabulary
// on purpose: the whole point of the latch is that exactly ONE thing releases
// it, so the release decision is an enum a test can assert on rather than a
// boolean inferred from what a chunk happened to look like.
type Termination uint8

const (
	// HoldOpen means the holdback is still latched: the line was absorbed and
	// nothing may reach the viewport.
	HoldOpen Termination = iota

	// HoldRowAccepted means the line was a complete table row and has been
	// accumulated. The latch stays ON — the grid is still being built.
	HoldRowAccepted

	// HoldClosedBlankLine is the DOUBLE LINE BREAK criterion ("\n\n"): a blank
	// line is CommonMark's block boundary, so nothing after it can be part of
	// this table.
	HoldClosedBlankLine

	// HoldClosedPipeFreeLine is the ZERO-PIPE criterion: a complete line that
	// follows a valid row and carries no cell boundary at all. Prose does not
	// extend a table.
	HoldClosedPipeFreeLine

	// HoldClosedNonRowLine is the general GFM boundary: a complete line that is
	// not a table row. A pipe-delimited table body cannot contain such a line,
	// so this is a strict superset of HoldClosedPipeFreeLine and is what keeps
	// the latch from ever stranding on a line that merely happens to contain a
	// pipe (a paragraph mentioning `a | b`).
	HoldClosedNonRowLine

	// HoldClosedStreamEnd is the STREAM COMPLETION criterion: StreamEnd, a
	// PARTIAL token-limit truncation, or Ctrl+C. Nothing more is coming, so
	// whatever arrived is structurally final.
	HoldClosedStreamEnd
)

// String returns the termination's canonical name (diagnostics and tests).
func (t Termination) String() string {
	switch t {
	case HoldRowAccepted:
		return "row-accepted"
	case HoldClosedBlankLine:
		return "blank-line"
	case HoldClosedPipeFreeLine:
		return "pipe-free-line"
	case HoldClosedNonRowLine:
		return "non-row-line"
	case HoldClosedStreamEnd:
		return "stream-end"
	default:
		return "open"
	}
}

// Closed reports whether t is a release signal. Only a Closed value drains the
// buffer; every other value means the block is still being constructed.
func (t Termination) Closed() bool {
	return t == HoldClosedBlankLine ||
		t == HoldClosedPipeFreeLine ||
		t == HoldClosedNonRowLine ||
		t == HoldClosedStreamEnd
}

// TableHoldback is the FULL HOLDBACK BUFFER for one Markdown table block. The
// zero value is an open, empty holdback and is immediately usable; a nil
// *TableHoldback is inert.
//
// It is NOT safe for concurrent use: it is confined to the UI goroutine, driven
// from the streaming renderer's per-tick projection.
type TableHoldback struct {
	active  bool
	rows    []string
	pending string
}

// NewTableHoldback returns an open (latch OFF), empty holdback. Engagement is a
// separate, explicit step so a caller can never claim a holdback it never fed.
func NewTableHoldback() *TableHoldback {
	return &TableHoldback{}
}

// Active reports whether the latch is ON — i.e. whether a table block is
// currently being held back from the viewport.
func (h *TableHoldback) Active() bool {
	return h != nil && h.active
}

// Engage turns the latch ON without feeding a line. It reports whether the
// transition happened, so an already-latched holdback is distinguishable from a
// freshly latched one.
func (h *TableHoldback) Engage() bool {
	if h == nil || h.active {
		return false
	}
	h.active = true
	return true
}

// Feed appends ONE COMPLETE logical line (no embedded "\n") to the held-back
// block and reports what that line meant for the latch.
//
// A table row is absorbed and the latch STAYS ON: the grid is not renderable
// until every row has arrived, and admitting an early one is precisely the
// reflow this buffer exists to prevent. Any other line is a TERMINATOR and is
// NOT stored — it belongs to the following block and is rendered by the caller
// through the normal pipeline, so releasing the latch can never swallow or
// duplicate a line.
//
// A line fed to a holdback whose latch is already OFF is a no-op reported as
// HoldOpen: a bare paragraph line is not a terminator, because nothing was
// being held.
func (h *TableHoldback) Feed(line string) Termination {
	if h == nil {
		return HoldOpen
	}
	trimmed := strings.TrimSpace(line)
	if TableRow(trimmed) {
		h.active = true
		h.rows = append(h.rows, line)
		return HoldRowAccepted
	}
	if !h.active {
		return HoldOpen
	}
	switch {
	case trimmed == "":
		return HoldClosedBlankLine
	case !HasUnescapedPipe(trimmed):
		return HoldClosedPipeFreeLine
	default:
		return HoldClosedNonRowLine
	}
}

// Hold diverts the STILL-GROWING trailing line verbatim.
//
// It replaces rather than appends because the streaming contract re-supplies
// the whole partial line on every tick: the buffer must hold the line's current
// prefix, not a concatenation of every prefix it ever had. Hold NEVER releases
// the latch on its own — a partial line is by definition not yet a complete
// line, so the closing pipe (or the blank line, or the stream end) has not
// arrived and there is nothing to terminate on. This is the single most
// important property of the holdback: the one input that arrives dozens of
// times per second can never be the one that flips the state.
func (h *TableHoldback) Hold(partial string) {
	if h == nil {
		return
	}
	h.active = true
	h.pending = strings.TrimRight(partial, "\r")
}

// Pending returns the still-growing trailing line exactly as it was last
// supplied, or "" when the stream sits on a line boundary.
func (h *TableHoldback) Pending() string {
	if h == nil {
		return ""
	}
	return h.pending
}

// Rows returns a copy of the complete table rows accumulated so far, in arrival
// order. The copy is what makes the buffer safe to hand to a renderer while the
// holdback keeps accumulating.
func (h *TableHoldback) Rows() []string {
	if h == nil || len(h.rows) == 0 {
		return nil
	}
	return append([]string(nil), h.rows...)
}

// RowCount returns how many complete rows are held back. The indicator never
// claims a row count (the grid's final height is not knowable yet), but tests
// and diagnostics read it to prove no token was dropped or duplicated.
func (h *TableHoldback) RowCount() int {
	if h == nil {
		return 0
	}
	return len(h.rows)
}

// Bytes returns the total size of the accumulated raw table text, terminator
// line excluded. It is the honest measure of "how much has been held back".
func (h *TableHoldback) Bytes() int {
	if h == nil {
		return 0
	}
	n := len(h.pending)
	for _, r := range h.rows {
		n += len(r) + 1
	}
	return n
}

// Close is the STREAM COMPLETION termination signal: the stream ended (StreamEnd,
// a PARTIAL token-limit truncation, or Ctrl+C) while a table block was still
// latched. Whatever arrived is structurally final because nothing more is
// coming, so the latch is released and the accumulated rows are handed back.
//
// It reports the termination it applied, so a caller can distinguish "the stream
// ended with a table held back" (HoldClosedStreamEnd) from "nothing was held"
// (HoldOpen).
func (h *TableHoldback) Close() Termination {
	if h == nil || !h.active {
		return HoldOpen
	}
	// A still-growing trailing line is a complete line now that nothing will
	// extend it: promote it so the last row is not lost.
	if p := h.pending; strings.TrimSpace(p) != "" && TableRow(strings.TrimSpace(p)) {
		h.rows = append(h.rows, p)
	}
	return HoldClosedStreamEnd
}

// Drain atomically takes the accumulated rows and RELEASES the latch. It is the
// one-shot render seam: the caller hands the returned rows to the Markdown AST
// renderer in a single pass, so the grid appears complete or not at all.
//
// Drain reports whether any rows were produced; an empty drain is a normal
// outcome (a holdback that latched on a lone "|" holds nothing worth drawing)
// and is not an error.
func (h *TableHoldback) Drain() ([]string, bool) {
	if h == nil || !h.active {
		return nil, false
	}
	rows := h.rows
	// Hand the slice over and re-point: no copy, and no aliasing either, so a
	// large table is drained in O(1).
	h.rows = nil
	h.pending = ""
	h.active = false
	if len(rows) == 0 {
		return nil, false
	}
	return rows, true
}

// Reset clears the buffer and releases the latch. It is the stream-boundary
// teardown, so a held-back table can never leak into the next turn.
func (h *TableHoldback) Reset() {
	if h == nil {
		return
	}
	h.active = false
	h.rows = nil
	h.pending = ""
}
