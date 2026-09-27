package markdown

import (
	"strings"
	"sync"
	"testing"
)

// ── TableRow: the single row-grammar definition ─────────────────────────────

// TestTableRowRecognisesRowsAndRejectsProse pins the row grammar.
//
// TableRow takes a TRIMMED line (the renderer trims before every call), so the
// caller — not the predicate — owns whitespace. What the predicate owns is the
// grammar itself: a leading cell boundary plus at least one more.
func TestTableRowRecognisesRowsAndRejectsProse(t *testing.T) {
	for _, r := range []string{"| a | b |", "| a |", "||", "|---|"} {
		if !TableRow(r) {
			t.Errorf("TableRow(%q) = false, want true", r)
		}
	}
	// A lone pipe is the OPENER of a row, not a row: there is no cell boundary
	// yet, so latching on it would claim a table where none exists.
	if TableRow("|") {
		t.Error(`TableRow("|") = true; a lone pipe is an opener, not a row`)
	}
	// A leading boundary is REQUIRED. "a | b" is prose that mentions a pipe, and
	// treating it as a row would latch the holdback on ordinary text.
	for _, notRow := range []string{"", "  ", "|", "| a", "a | b", "x | y |", "text |", "```", "# a | b", "**a** | b"} {
		if TableRow(notRow) {
			t.Errorf("TableRow(%q) = true, want false", notRow)
		}
	}
}

func TestHasUnescapedPipeIgnoresEscapedPipes(t *testing.T) {
	if HasUnescapedPipe(`a \| b`) {
		t.Error("an escaped pipe was counted as a cell boundary")
	}
	if !HasUnescapedPipe("a | b") {
		t.Error("an unescaped pipe was not counted")
	}
	if HasUnescapedPipe("no boundaries here") {
		t.Error("a pipe-free line reported a cell boundary")
	}
}

// ── Latch ON ────────────────────────────────────────────────────────────────

func TestHoldbackEngagesOnTheFirstRow(t *testing.T) {
	h := NewTableHoldback()
	if h.Active() {
		t.Fatal("a fresh holdback reports itself active")
	}
	if !h.Engage() {
		t.Fatal("Engage reported no transition on an open holdback")
	}
	if !h.Active() {
		t.Fatal("the latch did not turn ON")
	}
	// Engaging again is a no-op: "the hold began here" stays observable, and
	// nothing that happens while the hold is live can look like a new hold.
	if h.Engage() {
		t.Error("a second Engage reported a transition")
	}
}

func TestFeedAccumulatesRowsAndKeepsTheLatchOn(t *testing.T) {
	h := NewTableHoldback()
	for _, row := range []string{"| Name | Age |", "|---|---|", "| Ana | 31 |"} {
		if term := h.Feed(row); term != HoldRowAccepted {
			t.Fatalf("Feed(%q) = %v, want row-accepted", row, term)
		}
	}
	// THE HYSTERESIS: after three rows the latch is still ON. A derivation from
	// "does the latest chunk look like a table" would have released here on any
	// non-pipe chunk; the latch cannot be released except by a terminator.
	if !h.Active() {
		t.Fatal("the latch released while rows were still arriving")
	}
	if h.RowCount() != 3 {
		t.Fatalf("RowCount = %d, want 3", h.RowCount())
	}
	if h.Bytes() == 0 {
		t.Error("Bytes reported nothing held back")
	}
	if got := h.Rows(); len(got) != 3 || got[0] != "| Name | Age |" {
		t.Errorf("Rows() = %q", got)
	}
}

// ── Hold(): the input that must NEVER flip the state ────────────────────────

func TestHoldNeverReleasesTheLatch(t *testing.T) {
	h := NewTableHoldback()
	h.Engage()
	// Every prefix of a still-growing row, in arrival order.
	for _, prefix := range []string{"|", "| N", "| Na", "| Name", "| Name |", "| Name | A", "| Name | Ag", "| Name | Age", "| Name | Age |"} {
		h.Hold(prefix)
		if !h.Active() {
			t.Fatalf("Hold(%q) released the latch", prefix)
		}
		if got := h.Pending(); got != prefix {
			t.Fatalf("Pending() = %q, want %q (Hold must REPLACE, not append)", got, prefix)
		}
		if h.RowCount() != 0 {
			t.Fatalf("Hold(%q) committed a row: the trailing line is not a row yet", prefix)
		}
	}
}

func TestHoldReplacesRatherThanAccumulates(t *testing.T) {
	h := NewTableHoldback()
	h.Engage()
	for _, prefix := range []string{"|", "| N", "| Na", "| Nam"} {
		h.Hold(prefix)
	}
	// The buffer must hold the line's CURRENT prefix. Accumulating prefixes
	// would feed the renderer "|N|Na|Nam…" the moment the line commits.
	if got := h.Pending(); got != "| Nam" {
		t.Fatalf("Pending() = %q, want %q", got, "| Nam")
	}
}

func TestHoldStripsCarriageReturn(t *testing.T) {
	h := NewTableHoldback()
	h.Engage()
	h.Hold("| a | b |\r")
	if got := h.Pending(); got != "| a | b |" {
		t.Fatalf("Pending() = %q, want the line without its CR", got)
	}
}

// ── Latch OFF: the explicit termination criteria ────────────────────────────

func TestBlankLineTerminatesTheHoldback(t *testing.T) {
	// The double line break ("\n\n") — CommonMark's block boundary.
	h := NewTableHoldback()
	h.Feed("| a | b |")
	term := h.Feed("")
	if term != HoldClosedBlankLine {
		t.Fatalf("Feed(\"\") = %v, want blank-line", term)
	}
	if !term.Closed() {
		t.Fatal("a blank-line closure is not a release")
	}
}

func TestPipeFreeLineAfterAValidRowTerminatesTheHoldback(t *testing.T) {
	h := NewTableHoldback()
	h.Feed("| a | b |")
	if term := h.Feed("That concludes the table."); term != HoldClosedPipeFreeLine {
		t.Fatalf("Feed(pipe-free) = %v, want pipe-free-line", term)
	}
}

func TestNonRowLineTerminatesTheHoldback(t *testing.T) {
	// A strict superset of the zero-pipe criterion. Without it, prose that
	// merely MENTIONS a pipe ("see a | b below") would hold the grid open until
	// the stream ended, stranding the indicator over a finished table.
	h := NewTableHoldback()
	h.Feed("| a | b |")
	if term := h.Feed("see a | b below"); term != HoldClosedNonRowLine {
		t.Fatalf("Feed(non-row) = %v, want non-row-line", term)
	}
}

func TestTerminatorIsNeverStoredInTheHoldback(t *testing.T) {
	h := NewTableHoldback()
	h.Feed("| a | b |")
	h.Feed("after the table")
	if h.RowCount() != 1 {
		t.Fatalf("RowCount = %d; the terminator belongs to the next block and must not be buffered", h.RowCount())
	}
}

func TestFeedOnAnOpenHoldbackIsNotATerminator(t *testing.T) {
	// A bare paragraph line is only a terminator when something was actually
	// being held. With the latch off there is nothing to terminate, and calling a
	// release signal here would fabricate the end of a block that never began.
	h := NewTableHoldback()
	if term := h.Feed("just prose"); term != HoldOpen {
		t.Fatalf("Feed on an open holdback = %v, want open", term)
	}
	if term := h.Feed(""); term != HoldOpen {
		t.Fatalf("Feed(\"\") on an open holdback = %v, want open", term)
	}
}

func TestCloseIsTheStreamCompletionCriterion(t *testing.T) {
	h := NewTableHoldback()
	h.Feed("| a | b |")
	h.Hold("| c | d") // the stream ends mid-row
	if term := h.Close(); term != HoldClosedStreamEnd {
		t.Fatalf("Close() = %v, want stream-end", term)
	}
	// The still-growing line is promoted: nothing will extend it, so it is a
	// complete line now and its bytes must not be lost.
	rows, ok := h.Drain()
	if !ok || len(rows) != 2 {
		t.Fatalf("Drain() = (%q, %v); the trailing row must be promoted at stream end", rows, ok)
	}
	if rows[1] != "| c | d" {
		t.Errorf("promoted row = %q", rows[1])
	}
}

func TestCloseOnAnOpenHoldbackIsANoOp(t *testing.T) {
	h := NewTableHoldback()
	if term := h.Close(); term != HoldOpen {
		t.Fatalf("Close() on an open holdback = %v, want open", term)
	}
	if _, ok := h.Drain(); ok {
		t.Fatal("an open holdback produced rows")
	}
}

// ── Drain: the one-shot render seam ─────────────────────────────────────────

func TestDrainReleasesTheLatchAndHandsOverTheRows(t *testing.T) {
	h := NewTableHoldback()
	for _, row := range []string{"| a | b |", "|---|---|", "| 1 | 2 |"} {
		h.Feed(row)
	}
	rows, ok := h.Drain()
	if !ok {
		t.Fatal("Drain reported failure on a populated holdback")
	}
	if len(rows) != 3 {
		t.Fatalf("Drain returned %d rows, want 3", len(rows))
	}
	if h.Active() {
		t.Error("Drain left the latch ON; a second render would duplicate the grid")
	}
	if h.RowCount() != 0 || h.Pending() != "" {
		t.Error("Drain left bytes behind in the buffer")
	}
	// Drain is the ONLY release, and it is idempotent.
	if _, ok := h.Drain(); ok {
		t.Error("a second Drain produced rows; the grid would render twice")
	}
}

func TestDrainHandsOverWithoutAliasing(t *testing.T) {
	h := NewTableHoldback()
	h.Feed("| a | b |")
	rows, _ := h.Drain()
	h.Feed("| c | d |")
	// rows must be the snapshot it was, not a view onto a buffer that moved on.
	if len(rows) != 1 || rows[0] != "| a | b |" {
		t.Fatalf("the drained slice was mutated by later feeds: %q", rows)
	}
}

func TestDrainOnAHoldbackWithNoRowsIsNormal(t *testing.T) {
	// Latching on a lone "|" holds nothing worth drawing. That is a normal
	// outcome, not an error, and it must emit nothing rather than an empty grid.
	h := NewTableHoldback()
	h.Engage()
	rows, ok := h.Drain()
	if ok || len(rows) != 0 {
		t.Fatalf("Drain = (%q, %v), want (nil, false)", rows, ok)
	}
	if h.Active() {
		t.Error("Drain left the latch ON")
	}
}

func TestResetDropsEverything(t *testing.T) {
	h := NewTableHoldback()
	h.Feed("| a | b |")
	h.Hold("| c")
	h.Reset()
	if h.Active() || h.RowCount() != 0 || h.Pending() != "" {
		t.Fatal("Reset left holdback state behind; a discarded turn would leak into the next")
	}
	if _, ok := h.Drain(); ok {
		t.Fatal("a reset holdback produced rows")
	}
}

// ── Nil safety ──────────────────────────────────────────────────────────────

func TestNilHoldbackIsInert(t *testing.T) {
	var h *TableHoldback
	if h.Active() {
		t.Error("a nil holdback reported itself active")
	}
	if h.Engage() {
		t.Error("a nil holdback engaged")
	}
	if h.RowCount() != 0 || h.Bytes() != 0 || h.Pending() != "" || h.Rows() != nil {
		t.Error("a nil holdback reported content")
	}
	if term := h.Feed("| a | b |"); term != HoldOpen {
		t.Errorf("nil Feed = %v, want open", term)
	}
	if term := h.Close(); term != HoldOpen {
		t.Errorf("nil Close = %v, want open", term)
	}
	if _, ok := h.Drain(); ok {
		t.Error("a nil holdback drained rows")
	}
	h.Hold("| a")
	h.Reset()
}

func TestZeroValueHoldbackIsUsable(t *testing.T) {
	var h TableHoldback
	if h.Active() {
		t.Fatal("the zero value is active")
	}
	if term := h.Feed("| a | b |"); term != HoldRowAccepted {
		t.Fatalf("zero-value Feed = %v", term)
	}
	if !h.Active() {
		t.Fatal("the zero value did not latch")
	}
}

// ── Termination vocabulary ──────────────────────────────────────────────────

func TestTerminationClosedness(t *testing.T) {
	closed := map[Termination]bool{
		HoldOpen:               false,
		HoldRowAccepted:        false,
		HoldClosedBlankLine:    true,
		HoldClosedPipeFreeLine: true,
		HoldClosedNonRowLine:   true,
		HoldClosedStreamEnd:    true,
	}
	for term, want := range closed {
		if got := term.Closed(); got != want {
			t.Errorf("%v.Closed() = %v, want %v", term, got, want)
		}
		if term.String() == "" {
			t.Errorf("Termination %d has no canonical name", term)
		}
	}
	if HoldOpen.String() != "open" {
		t.Errorf("HoldOpen.String() = %q", HoldOpen.String())
	}
}

// ── Concurrency ─────────────────────────────────────────────────────────────

// TestHoldbackIsGoroutineConfinedByContract is a guard, not a race test: the
// holdback is driven from the UI goroutine only, and the -race suite proves it
// stays that way when the read helpers are used from a test.
func TestHoldbackIsGoroutineConfinedByContract(t *testing.T) {
	h := NewTableHoldback()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = h.Active()
				_ = h.RowCount()
				_ = h.Pending()
			}
		}()
	}
	wg.Wait()
	if h.Active() {
		t.Error("concurrent reads engaged the latch")
	}
}

// TestHoldbackPreservesEveryByteItAbsorbs is the no-token-left-behind clause at
// the buffer level: the concatenation of everything the holdback accepted must
// reconstruct the block exactly.
func TestHoldbackPreservesEveryByteItAbsorbs(t *testing.T) {
	block := "| Name | Age |\n|---|---|\n| Ana | 31 |"
	var held strings.Builder
	h := NewTableHoldback()
	for _, line := range strings.Split(block, "\n") {
		if term := h.Feed(line); term.Closed() {
			t.Fatalf("row %q closed the holdback: %v", line, term)
		}
		held.WriteString(line)
		held.WriteByte('\n')
	}
	if got := strings.Join(h.Rows(), "\n"); got != block {
		t.Fatalf("reconstructed block =\n%q\nwant\n%q", got, block)
	}
}
