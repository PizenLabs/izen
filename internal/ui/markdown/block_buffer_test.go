package markdown

import (
	"strings"
	"testing"
)

// TestIncompleteDetectsPartiallyArrivedBlocks is the anti-flicker contract: a
// line that is still arriving must be reported as incomplete so the renderer
// holds it back instead of interpreting markup that may not mean what it looks
// like yet. Each case is the EXACT prefix a provider emits mid-stream.
func TestIncompleteDetectsPartiallyArrivedBlocks(t *testing.T) {
	cases := []struct {
		name string
		line string
		want BlockKind
	}{
		// Table rows: the closing pipe has not arrived yet.
		{"table single pipe", "|", BlockTable},
		{"table one cell", "| Name", BlockTable},
		{"table two cells", "| Name | Age", BlockTable},
		{"table trailing space after cell", "| Name | Age ", BlockTable},
		{"table odd pipe count", "| a | b | c", BlockTable},

		// Heading markers: the required space has not arrived.
		{"h1 bare", "#", BlockHeading},
		{"h2 bare", "##", BlockHeading},
		{"h3 bare", "###", BlockHeading},
		{"h6 bare", "######", BlockHeading},
		{"indented h2 bare", "  ##", BlockHeading},

		// Fence markers: fewer than three markers have arrived and nothing on
		// the line can close them yet. With a second run present the construct
		// is an inline span instead (CommonMark gives the span precedence), so
		// it is resolved by the inline branch below.
		{"fence one tick", "`", BlockFence},
		{"fence two ticks", "``", BlockFence},
		{"fence one tick with lang", "`go", BlockFence},
		{"open code span at line start", "`co", BlockFence},
		{"fence one tilde", "~", BlockFence},
		{"fence two tildes", "~~", BlockFence},
		{"indented fence prefix", "  ~", BlockFence},
		{"indented two tildes", "   ~~", BlockFence},

		// Inline delimiters with no closer yet.
		{"open bold", "**bol", BlockInline},
		{"open bold only", "**", BlockInline},
		{"open italic", "*ital", BlockInline},
		{"open strikethrough", "see ~~stru", BlockInline},
		{"open link", "see [docs", BlockInline},
		{"open link target", "see [docs](htt", BlockInline},
		{"bold inside sentence", "this is **very", BlockInline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := Incomplete(tc.line, false, false)
			if !ok {
				t.Fatalf("Incomplete(%q) = false; the renderer would interpret a partial block and reflow", tc.line)
			}
			if kind != tc.want {
				t.Errorf("Incomplete(%q) kind = %v, want %v", tc.line, kind, tc.want)
			}
		})
	}
}

// TestIncompleteAcceptsStructurallyFinalLines is the other half: a line that is
// already final must NOT be held back, or committed content would render as
// permanently dimmed plain text.
func TestIncompleteAcceptsStructurallyFinalLines(t *testing.T) {
	lines := []string{
		"",
		"   ",
		"ordinary prose with no markup at all",
		"| Name | Age |",
		"|---|---|",
		"| a \\| b | c |",    // escaped pipe inside a cell
		"```go",              // complete fence marker
		"```",                // bare fence marker
		"~~~python",          // tilde fence
		"````",               // four-backtick fence
		"# Heading",          //
		"## Heading two",     //
		"###### Heading six", //
		"#notaheading",       // CommonMark: no space, so a paragraph
		"**bold**",
		"*italic*",
		"***both***",
		"`code`",
		"~~struck~~",
		"[docs](https://example.com)",
		"a * b * c", // two balanced single stars
		"1. list item",
		"- bullet",
		"> quote",
		"```", // in a line that is only a fence
	}
	for _, line := range lines {
		if kind, ok := Incomplete(line, false, false); ok {
			t.Errorf("Incomplete(%q) = (%v, true); a structurally final line must render immediately", line, kind)
		}
	}
}

// TestIncompleteDefersToSurroundingBlockState proves a line inside an already
// open block is not double-reported: the block buffer owns it and the renderer
// is not opening a new construct.
func TestIncompleteDefersToSurroundingBlockState(t *testing.T) {
	if _, ok := Incomplete("| Name | Age", true, false); ok {
		t.Error("a partial line inside an open code fence must not be reported as a new table row")
	}
	if _, ok := Incomplete("**bol", false, true); ok {
		t.Error("a partial line inside an open table must not be reported as inline markup")
	}
	if _, ok := Incomplete("**bol", true, true); ok {
		t.Error("a partial line inside an open code fence must not be reported as inline markup")
	}
}

// TestBufferSeparatesCommittedFromUncommitted is the core buffer contract: bytes
// up to the last newline are committed, the rest is the uncommitted tail, and
// nothing is duplicated or lost across the split.
func TestBufferSeparatesCommittedFromUncommitted(t *testing.T) {
	b := NewBuffer(80)
	b.Push("line one\nline two\npar")

	if got := b.Pending(); got != "par" {
		t.Errorf("Pending() = %q, want %q", got, "par")
	}
	if !b.HasPending() {
		t.Error("HasPending() = false with an uncommitted tail buffered")
	}
	committed := b.Commit()
	want := []string{"line one", "line two"}
	if len(committed) != len(want) {
		t.Fatalf("Commit() = %v, want %v", committed, want)
	}
	for i := range want {
		if committed[i] != want[i] {
			t.Errorf("Commit()[%d] = %q, want %q", i, committed[i], want[i])
		}
	}
	if got := b.Pending(); got != "par" {
		t.Errorf("Commit() consumed the uncommitted tail: Pending() = %q", got)
	}
	if again := b.Commit(); again != nil {
		t.Errorf("Commit() with no newline buffered = %v, want nil", again)
	}
}

// TestBufferAccumulatesChunkByChunk simulates a real token stream: many small
// pushes must reassemble into exactly the source text with the commit split
// happening only at line boundaries.
func TestBufferAccumulatesChunkByChunk(t *testing.T) {
	const source = "alpha\nbeta **two**\n| a | b |\ngamma"
	prefixes := make([]string, 0, len(source))
	for i := 1; i <= len(source); i++ {
		prefixes = append(prefixes, source[:i])
	}

	b := NewBuffer(80)
	for i, p := range prefixes {
		b.Reset()
		b.Push(p)
		committed := b.Commit()
		// Commit consumes the newline that terminated each line, so the
		// reassembly re-inserts one separator per committed line.
		rebuilt := b.Pending()
		if len(committed) > 0 {
			rebuilt = strings.Join(committed, "\n") + "\n" + rebuilt
		}
		if rebuilt != p {
			t.Fatalf("prefix %d (%q): reassembled %q", i, p, rebuilt)
		}
		if i == len(prefixes)-1 {
			break
		}
	}
	// Replay the final prefix to inspect the commit/pending split.
	b.Reset()
	b.Push(source)
	committed := b.Commit()
	// Every line except the last is structurally final and must be committed.
	if len(committed) != 3 {
		t.Fatalf("committed %d lines, want 3: %v", len(committed), committed)
	}
	last, ok := b.Flush()
	if !ok || last != "gamma" {
		t.Errorf("Flush() = (%q, %v), want (\"gamma\", true)", last, ok)
	}
}

// TestBufferCommitIsIdempotentPerFrame guards the no-duplicate-emission rule: a
// frame that carries no new bytes must commit nothing, because the renderer
// treats every committed line as a one-shot append.
func TestBufferCommitIsIdempotentPerFrame(t *testing.T) {
	b := NewBuffer(80)
	b.Push("stable line\n")
	first := b.Commit()
	second := b.Commit()
	if len(first) != 1 {
		t.Fatalf("first Commit() = %v, want one line", first)
	}
	if second != nil {
		t.Errorf("second Commit() = %v, want nil (a frame with no new bytes must emit nothing)", second)
	}
}

// TestBufferKindTracksPendingConstruct wires detection to the buffer: the buffer
// must classify its own uncommitted tail so a caller never has to re-derive it.
func TestBufferKindTracksPendingConstruct(t *testing.T) {
	cases := []struct {
		push string
		want BlockKind
		ok   bool
	}{
		{"| Name | Age", BlockTable, true},
		{"**bol", BlockInline, true},
		{"```go", BlockText, false},
		{"complete prose line", BlockText, false},
		{"", BlockText, false},
	}
	for _, tc := range cases {
		b := NewBuffer(80)
		b.Reset()
		b.Push(tc.push)
		kind, ok := b.Kind()
		if ok != tc.ok || kind != tc.want {
			t.Errorf("Kind() after Push(%q) = (%v, %v), want (%v, %v)",
				tc.push, kind, ok, tc.want, tc.ok)
		}
	}
}

// TestBufferFlushPromotesTheFinalLine covers the stream-end path: whatever
// arrived is structurally final because nothing more is coming, so it must be
// handed to the full pipeline rather than left dimmed forever.
func TestBufferFlushPromotesTheFinalLine(t *testing.T) {
	b := NewBuffer(80)
	b.Push("**unterminated bold")
	if _, ok := b.Flush(); !ok {
		t.Fatal("Flush() must return the unterminated tail: it is final at stream end")
	}
	if b.HasPending() {
		t.Error("Flush() must clear the buffer")
	}
	if _, ok := b.Flush(); ok {
		t.Error("Flush() on an empty buffer must report false")
	}
}

// TestBufferNilSafe keeps harnesses that never construct a buffer panic-free.
func TestBufferNilSafe(t *testing.T) {
	var b *Buffer
	b.Push("x")
	b.SetWidth(40)
	b.SetBlockState(true, true)
	b.Reset()
	if b.Pending() != "" || b.HasPending() || b.Commit() != nil {
		t.Error("nil Buffer must degrade to a no-op")
	}
	if _, ok := b.Flush(); ok {
		t.Error("nil Buffer.Flush must report false")
	}
	if _, ok := b.Kind(); ok {
		t.Error("nil Buffer.Kind must report complete")
	}
	if b.Width() != 0 {
		t.Errorf("nil Buffer.Width() = %d, want 0", b.Width())
	}
}

// TestBufferSetWidthIgnoresDegenerateInput guards the resize path.
func TestBufferSetWidthIgnoresDegenerateInput(t *testing.T) {
	b := NewBuffer(0)
	if b.Width() != 20 {
		t.Errorf("NewBuffer(0).Width() = %d, want the 20-cell floor", b.Width())
	}
	b.SetWidth(-5)
	if b.Width() != 20 {
		t.Errorf("SetWidth(-5) must be ignored, got %d", b.Width())
	}
	b.SetWidth(120)
	if b.Width() != 120 {
		t.Errorf("SetWidth(120) = %d, want 120", b.Width())
	}
}

// TestBlockKindString pins the diagnostic names.
func TestBlockKindString(t *testing.T) {
	for kind, want := range map[BlockKind]string{
		BlockText:    "text",
		BlockTable:   "table",
		BlockFence:   "fence",
		BlockHeading: "heading",
		BlockInline:  "inline",
	} {
		if got := kind.String(); got != want {
			t.Errorf("BlockKind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}

// TestUnescapePipesMasksEscapedCells guards the pipe parity check: a literal
// "|" inside a cell must not be read as a cell boundary.
func TestUnescapePipesMasksEscapedCells(t *testing.T) {
	if got := unescapePipes(`a \| b | c`); strings.Contains(got, `\`) {
		t.Errorf("unescapePipes left a backslash behind: %q", got)
	}
	if got := strings.Count(unescapePipes(`a \| b | c`), "|"); got != 1 {
		t.Errorf("unescapePipes(%q) has %d pipes, want 1", `a \| b | c`, got)
	}
}
