// Package markdown implements the streaming-safe block buffer that keeps
// partially-received Markdown from reflowing the viewport while a model is
// still writing it.
//
// # THE PROBLEM
//
// An LLM stream delivers text a few bytes at a time, so the renderer sees a
// sequence of PREFIXES of the final document. Feeding every prefix through the
// full Markdown pipeline makes the layout thrash: `| Name | Age` is not yet a
// table row (no closing pipe), so it renders as a wrapped paragraph; the frame
// after the closing `|` arrives it is a table and re-flows into a full box grid.
// The same happens for a half-arrived `**bold**`, an unterminated “ `code` “,
// and a `##` that has not yet grown its trailing space. At 100+ tok/s these
// reflows happen dozens of times per second, which is the "markdown blocks
// flicker / table columns jump around while streaming" report.
//
// # THE CONTRACT
//
// A Buffer splits the stream into two populations:
//
//   - COMMITTED: complete logical lines (everything up to the last "\n"). These
//     are structurally final, so the full block pipeline (fences, tables, lists,
//     inline styles) may run and the result is stable forever.
//   - UNCOMMITTED: the partial trailing line, which is still growing. It is
//     held in the buffer and rendered as PLAIN DIMMED TEXT, with no block or
//     inline interpretation, until a line break or a block closure arrives.
//
// Rendering the uncommitted line without interpretation is the whole point: a
// plain text run has exactly one possible layout, so nothing can reflow when
// the remaining bytes arrive. The line is promoted to the styled pipeline once,
// at commit — a single deliberate transition instead of a continuous flicker.
package markdown

import "strings"

// BlockKind classifies the construct an uncommitted line is part of. It exists
// so callers can log/assert on WHY a line is being held back, and so the
// renderer can pick a fitting dimmed treatment.
type BlockKind int

const (
	// BlockText is ordinary prose with no in-flight block syntax.
	BlockText BlockKind = iota
	// BlockTable is a pipe-delimited table row whose closing pipe has not
	// arrived yet.
	BlockTable
	// BlockFence is a code fence marker (``` or ~~~) that has not been fully
	// received yet.
	BlockFence
	// BlockHeading is an ATX heading marker that has not yet grown its
	// trailing space.
	BlockHeading
	// BlockInline is a line with an unterminated inline delimiter (`**`, `*`,
	// `~~`, a backtick code span, or an unclosed `[link](`).
	BlockInline
)

// String returns the block kind's canonical name (used in diagnostics/tests).
func (k BlockKind) String() string {
	switch k {
	case BlockTable:
		return "table"
	case BlockFence:
		return "fence"
	case BlockHeading:
		return "heading"
	case BlockInline:
		return "inline"
	default:
		return "text"
	}
}

// options tunes detection. The zero value is the production configuration.
type options struct {
	inCode  bool
	inTable bool
}

// Incomplete reports whether line is an in-flight block construct whose
// interpretation MUST be deferred until a line break or block closure.
//
// inCode / inTable report the surrounding block state the line was captured in:
// a partial line inside an OPEN fenced block is already handled by the block
// buffer, so it is not additionally reported as incomplete.
//
// The detection is intentionally conservative in the direction of "hold back":
// a false positive costs one frame of unstyled text, a false negative costs a
// visible reflow. Every branch therefore triggers on the ABSENCE of its
// terminator rather than on the presence of its opener.
func Incomplete(line string, inCode, inTable bool) (BlockKind, bool) {
	// Inside an already-open block the block buffer owns the line; the
	// renderer is not interpreting it as a new construct.
	if inCode || inTable {
		return BlockText, false
	}

	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return BlockText, false
	}

	// ── Fence marker: ``` or ~~~ ──────────────────────────────────────
	// A fence line is only complete once the run of markers is at least three
	// long. Anything shorter is a prefix of a fence that has not finished
	// arriving. A run of one or two BACKTICKS is not a fence at all though —
	// CommonMark gives a code span precedence — so it falls through to the
	// inline check below.
	if kind, ok := fenceIncomplete(trimmed); ok {
		return kind, true
	}
	if isFenceLine(trimmed) {
		// A complete fence marker. Its backticks must not be re-examined as an
		// unterminated code span.
		return BlockText, false
	}

	// ── Table row: the closing pipe has not arrived ───────────────────
	// A leading pipe commits the line to table grammar, so the row is only
	// structurally final once an UNESCAPED trailing pipe closes it — and a
	// lone "|" is just the opener, still waiting for its partner.
	if strings.HasPrefix(trimmed, "|") {
		unescaped := unescapePipes(trimmed)
		if strings.Count(unescaped, "|") < 2 || !strings.HasSuffix(unescaped, "|") {
			return BlockTable, true
		}
		return BlockText, false
	}

	// ── Heading marker: the trailing space has not arrived ────────────
	if kind, ok := headingIncomplete(trimmed); ok {
		return kind, true
	}

	// ── Inline delimiter: no closer yet ───────────────────────────────
	if kind, ok := inlineIncomplete(trimmed); ok {
		return kind, true
	}

	return BlockText, false
}

// leadingMarkerRun returns the marker character and run length of a leading run
// of identical marker runes ('`' or '~'), ignoring up to three leading spaces
// (CommonMark's fence indentation allowance). ok is false when the line does
// not start with a fence-capable marker.
func leadingMarkerRun(trimmed string) (marker byte, run int, ok bool) {
	s := trimmed
	if n := len(s) - len(strings.TrimLeft(s, " ")); n <= 3 {
		s = s[n:]
	} else {
		return 0, 0, false
	}
	if s == "" {
		return 0, 0, false
	}
	marker = s[0]
	if marker != '`' && marker != '~' {
		return 0, 0, false
	}
	for run < len(s) && s[run] == marker {
		run++
	}
	return marker, run, true
}

// isFenceLine reports whether trimmed is a complete code-fence marker: a run of
// three or more backticks/tildes at the start of the line.
func isFenceLine(trimmed string) bool {
	_, run, ok := leadingMarkerRun(trimmed)
	return ok && run >= 3
}

// fenceIncomplete reports whether trimmed is a partially-received fence marker.
//
// A run of fewer than three markers at the start of a line is ambiguous: it may
// be a `~~~`/` ``` ` fence still being typed, or an inline span
// (“ `code` “, `~~struck~~`). The two are told apart by whether a SECOND run
// of the same marker closes it later in the line:
//
//   - no second run  → nothing on this line can close it, so it is a fence
//     whose third marker has not arrived;
//   - a second run   → it is an inline span, and the inline branch decides
//     whether its closer has landed.
//
// This matters because the fence marker is the construct most likely to be
// caught mid-stream: a fence arrives as "`", then "“", then "```go", one token
// at a time, and interpreting the first two as a paragraph is what makes the
// code block pop in one frame late.
func fenceIncomplete(trimmed string) (BlockKind, bool) {
	marker, run, ok := leadingMarkerRun(trimmed)
	if !ok || run >= 3 {
		return BlockText, false
	}
	if strings.Count(trimmed, string(marker)) == run {
		return BlockFence, true
	}
	return BlockText, false
}

// headingIncomplete reports whether trimmed is an ATX heading marker that has
// not yet grown its required space. "#" alone, "##" alone and "### /" are all
// prefixes of a heading; treating them as paragraphs is what causes the heading
// to pop in one frame late.
func headingIncomplete(trimmed string) (BlockKind, bool) {
	if trimmed[0] != '#' {
		return BlockText, false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level > 6 {
		return BlockText, false
	}
	rest := trimmed[level:]
	// "#text" (no space) is NOT a heading in CommonMark — it is a paragraph.
	// A heading is only structurally final once its space has arrived, so a
	// bare run of hashes is the only incomplete form here.
	if rest == "" {
		return BlockHeading, true
	}
	return BlockText, false
}

// inlineIncomplete reports whether trimmed carries an inline delimiter with no
// closer yet. Any of these would otherwise be rendered with partial (or literal)
// markup mid-stream and then re-rendered styled, which is the "the bold text
// flickers while it arrives" report.
//
// Only delimiters that are still OPEN at end-of-line are reported; a balanced
// pair is structurally final regardless of the surrounding text.
func inlineIncomplete(trimmed string) (BlockKind, bool) {
	if unclosedBacktickRun(trimmed) {
		return BlockInline, true
	}
	if unclosedDelim(trimmed, "***") || unclosedDelim(trimmed, "**") {
		return BlockInline, true
	}
	if unclosedSingleStar(trimmed) {
		return BlockInline, true
	}
	if unclosedDelim(trimmed, "~~") {
		return BlockInline, true
	}
	if unclosedLink(trimmed) {
		return BlockInline, true
	}
	return BlockText, false
}

// unclosedBacktickRun reports an odd number of backtick code-span delimiters,
// which means a span is still open. Fence markers have already been resolved by
// the caller, so only inline spans are counted here.
func unclosedBacktickRun(s string) bool {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			n++
		}
	}
	return n%2 == 1
}

// unclosedDelim reports an odd occurrence count of a multi-character
// delimiter, i.e. an opener whose closer has not arrived.
func unclosedDelim(s, delim string) bool {
	return strings.Count(s, delim)%2 == 1
}

// unclosedSingleStar reports a single `*` emphasis span that is still open.
// Multi-star runs (`**bold**`, `***x***`) are handled by unclosedDelim, so this
// counts only runs of exactly one star and reports an odd number of them.
func unclosedSingleStar(s string) bool {
	runes := []rune(s)
	singles := 0
	for i := 0; i < len(runes); {
		if runes[i] != '*' {
			i++
			continue
		}
		run := 0
		for i+run < len(runes) && runes[i+run] == '*' {
			run++
		}
		if run == 1 {
			singles++
		}
		i += run
	}
	return singles%2 == 1
}

// unclosedLink reports a `[label](` prefix whose `)` or label closer has not
// arrived.
func unclosedLink(s string) bool {
	open := strings.Index(s, "[")
	if open < 0 {
		return false
	}
	rest := s[open:]
	if strings.Contains(rest, "](") {
		return !strings.Contains(rest, ")")
	}
	// A "[" with no closing "]" is still accumulating its label.
	return !strings.Contains(rest, "]")
}

// unescapePipes masks backslash-escaped pipes so a literal "\|" inside a cell
// is not counted as a cell boundary.
func unescapePipes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			if c != '|' {
				b.WriteByte(c)
			}
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Buffer holds the uncommitted tail of a Markdown stream and owns the
// commit/hold decision. It is the explicit "UncommittedBuffer" of the streaming
// contract: everything not yet terminated by a line break lives here, is never
// interpreted, and is promoted to the full pipeline exactly once.
//
// The zero value is not usable; construct with NewBuffer. A nil *Buffer is
// safe — every method degrades to a no-op — so renderers can hold one
// unconditionally.
type Buffer struct {
	pending string
	width   int
	opts    options
}

// NewBuffer returns a Buffer that pre-wraps committed content to width cells.
// A non-positive width is normalised to 20.
func NewBuffer(width int) *Buffer {
	if width <= 0 {
		width = 20
	}
	return &Buffer{width: width}
}

// SetWidth updates the committed-line wrap width (on terminal resize).
func (b *Buffer) SetWidth(w int) {
	if b == nil || w <= 0 {
		return
	}
	b.width = w
}

// Width returns the committed-line wrap width.
func (b *Buffer) Width() int {
	if b == nil {
		return 0
	}
	return b.width
}

// SetBlockState records the block state surrounding the uncommitted line, so
// detection does not re-report a line the block buffer already owns.
func (b *Buffer) SetBlockState(inCode, inTable bool) {
	if b == nil {
		return
	}
	b.opts.inCode, b.opts.inTable = inCode, inTable
}

// Push appends a stream chunk to the uncommitted tail. Callers feed raw token
// text; the buffer performs no interpretation.
func (b *Buffer) Push(chunk string) {
	if b == nil || chunk == "" {
		return
	}
	b.pending += chunk
}

// Pending returns the uncommitted trailing line (the bytes after the last
// "\n"). It is "" when the stream sits exactly on a line boundary.
func (b *Buffer) Pending() string {
	if b == nil {
		return ""
	}
	if i := strings.LastIndexByte(b.pending, '\n'); i >= 0 {
		return b.pending[i+1:]
	}
	return b.pending
}

// HasPending reports whether an uncommitted line is buffered.
func (b *Buffer) HasPending() bool {
	return b.Pending() != ""
}

// Kind reports the block construct the uncommitted line belongs to and whether
// it must be held back from the styled pipeline.
func (b *Buffer) Kind() (BlockKind, bool) {
	if b == nil {
		return BlockText, false
	}
	return Incomplete(b.Pending(), b.opts.inCode, b.opts.inTable)
}

// Commit returns the bytes of every complete logical line received so far and
// clears them, leaving only the still-growing partial line in the buffer.
//
// The returned slice is exactly the COMMITTED population: its last element is
// always terminated, so feeding it to the full Markdown pipeline is safe.
func (b *Buffer) Commit() []string {
	if b == nil {
		return nil
	}
	i := strings.LastIndexByte(b.pending, '\n')
	if i < 0 {
		return nil
	}
	complete := b.pending[:i+1]
	b.pending = b.pending[i+1:]
	if complete == "" {
		return nil
	}
	lines := strings.Split(complete, "\n")
	// The final element is the empty string after the trailing newline.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) == 0 {
		return nil
	}
	return lines
}

// Flush returns the uncommitted tail as a final line and clears the buffer. It
// is the stream-end path: whatever arrived is structurally final because
// nothing more is coming, so it must be rendered by the full pipeline.
func (b *Buffer) Flush() (string, bool) {
	if b == nil || b.pending == "" {
		return "", false
	}
	line := b.pending
	b.pending = ""
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return "", false
	}
	return line, true
}

// Reset clears the uncommitted tail and block state.
func (b *Buffer) Reset() {
	if b == nil {
		return
	}
	b.pending = ""
	b.opts = options{}
}
