// table_budgeter.go — PRORATED COLUMN BUDGETING FOR MARKDOWN TABLES.
//
// # THE PROBLEM THIS SOLVES
//
// A Markdown table's layout is a function of its WHOLE body: every column is as
// wide as its widest cell across the header AND every data row. Give a renderer
// the intrinsic widths and the total, and the table is either
//
//   - TOO NARROW: cells overflow their column and the `│` separators drift out
//     of column, tearing the grid into a staircase. The reader loses the one
//     alignment cue that makes a table scannable; or
//   - TOO WIDE: the grid runs past the right edge of the pane. The terminal
//     hard-wraps, and the right border of row 1 lands on the left edge of
//     row 2. Below ~40 cells — a tmux split, a phone-width SSH session — the
//     common fallback is to abandon the grid entirely and re-emit the data as a
//     bullet list, which loses the column relationship that justified a table.
//
// Both failures share one cause: NOTHING ALLOCATES the width. The renderer has
// an unlimited budget and a hard limit and no step in between.
//
// # THE CONTRACT
//
// BudgetTable is that step. It takes the intrinsic per-column maximums and a
// viewport width, and returns an explicit allocation:
//
//	totalAvailableWidth = viewportWidth - numColumns - 3
//	colWidth[i] = max(MinColumnWidth, floor(totalAvailableWidth * rawWidth[i] / sum(rawWidth)))
//
// The allocation is PRORATED, not greedy. A greedy pass ("give column 0 what it
// wants until the budget runs out") hands the whole table to whichever column
// happens to come first and starves the rest, so a three-column table with one
// long prose column collapses the two short ones to nothing. Prorating by
// intrinsic share keeps the RELATIVE shape of the table — which column is
// verbose, which is a label — intact at every width, which is the property a
// reader actually uses to read it.
//
// The `max(MinColumnWidth, …)` floor is deliberately load-bearing and
// deliberately imperfect. A column narrower than MinColumnWidth cannot show a
// word, so a floor beats a column of single characters. But floors are not free:
// `numColumns × MinColumnWidth` can itself exceed the available width, and then
// no allocation exists that is both readable and bounded. BudgetTable does not
// hide that — Fits() returns false and the caller degrades on purpose rather
// than rendering a torn frame by accident.
//
// # WHY THE ALLOCATION IS ALSO CLAMPED
//
// The budget above is the SPECIFIED arithmetic and is exported verbatim as
// Available so a test can assert it. It is not, by itself, sufficient to keep a
// framed grid inside the viewport, because the specified budget does not
// account for the grid's own drawing chrome. A box-drawing grid spends
//
//	FrameChrome(n) = 3n + 1
//
// cells before any content at all: n+1 `│` boundaries plus one space of padding
// on each side of each column. An allocation that fills the budget exactly
// therefore overruns the viewport by 2n+1 cells. ClampToFrame removes that
// overrun from the columns with the most slack, which is the smallest possible
// loss: the columns that needed their width least give it up first.
//
// Together: the specified prorating decides the SHAPE, and the clamp makes the
// stated goal — a perfectly framed table down to a 40-column pane — actually
// true rather than aspirational.
//
// # WHAT A CELL IS BEFORE IT IS BUDGETED
//
// A budget can only be allocated against text the renderer can actually lay out,
// so a cell's bytes have to be NORMALISED first. Models reach for HTML inside
// table cells constantly — `a<br>b`, `a<br/>b`, `a<br />b` — because it is the
// only line break plain Markdown offers a table cell. Goldmark parses all three
// as a raw-HTML inline node and Glamour/renderers have no terminal equivalent to
// give it, so an un-normalised cell does one of two wrong things:
//
//   - it PRINTS the tag. `bytes<br>into` lands in the frame as literal markup,
//     which is the worst outcome: the reader sees syntax the model never meant
//     to emit, and the `|`-column budget is spent on four characters that carry
//     no information.
//   - it DROPS the tag. `bytes<br>into` becomes `bytesinto` — two words fused
//     into one token that then cannot be word-wrapped, so a cell that wrapped
//     cleanly now breaks mid-word, and the text is no longer what the model
//     said.
//
// SanitizeCellBreaks resolves both by rewriting the break into the one sequence
// a cell renderer understands: "\n". A multi-line cell then wraps within its
// prorated column like any other wrapped content, and CellIntrinsicWidth
// measures the WIDEST LINE of it — measuring the whole cell as one line would
// demand the width of content that is never drawn on any single row and would
// starve every other column.
package markdown

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Cell is a lipgloss.Style that carries an EXPLICIT word-wrap contract, so a
// call site can read `.Width(n).Wrap(true)` and mean it.
//
// WRAP AND LIPGLOSS VERSIONS: lipgloss v1 wraps unconditionally whenever
// Width > 0 and exposes no Wrap(bool) setter, so under v1 the Wrap method here
// is a declaration of intent that Width structurally guarantees. It exists so
// the wrap contract is greppable at every call site and survives verbatim into
// the lipgloss v2 migration, where wrapping becomes opt-in and this becomes the
// real API. No call site changes. (The same seam already exists for banners in
// internal/ui/components.)
type Cell struct {
	lipgloss.Style
}

// Width constrains the cell to i cells of content and keeps the Cell contract.
// It shadows the promoted lipgloss method so the `.Width(n).Wrap(true)` chain
// type-checks.
func (c Cell) Width(i int) Cell { return Cell{Style: c.Style.Width(i)} }

// Wrap states that the cell's content must be word-wrapped to its width. Under
// lipgloss v1 this is guaranteed by the non-zero Width above.
func (c Cell) Wrap(bool) Cell { return c }

// Table budgeting constants. They are exported because a test (and a caller
// choosing a fallback) needs to know the floor and the chrome without
// re-deriving them.
const (
	// MinColumnWidth is the narrowest a column may be prorated to. Below this a
	// cell cannot hold a word, so a column that would land here is floored here
	// instead: a narrow column is strictly more readable than a column of
	// one-character lines.
	MinColumnWidth = 10

	// TableConstantChrome is the fixed, column-count-independent chrome a
	// table's frame spends beyond its per-column chrome: the two boundary
	// cells and the gutter between the frame edge and the first/last column.
	TableConstantChrome = 3
)

// FrameChrome returns the number of cells a box-drawing grid of numCols columns
// spends on separators and cell padding, independent of any content width:
// one `│` per column boundary (numCols+1) plus one space on each side of each
// column (2×numCols).
func FrameChrome(numCols int) int {
	if numCols <= 0 {
		return 0
	}
	return 3*numCols + 1
}

// AvailableWidth is the content budget for a numCols-column table inside a
// viewport of viewportWidth cells:
//
//	viewportWidth - numColumns - TableConstantChrome
//
// It is exported so the specified arithmetic is directly assertable rather than
// buried inside an allocation, and so a caller can ask "how much room is there
// for text at all?" before deciding between a grid and a stacked fallback.
func AvailableWidth(viewportWidth, numCols int) int {
	return viewportWidth - numCols - TableConstantChrome
}

// TableBudget is the resolved column allocation for one table. It is a value:
// every field is populated by BudgetTable, and none of it depends on renderer
// state, so a budget can be computed once and reused (a table re-rendered on
// every streaming tick allocates from the same numbers every time).
type TableBudget struct {
	// Raw is the intrinsic width each column asked for: the widest cell it
	// contains. It is retained so a caller can report how much a column was
	// squeezed, and so a test can check the proration ratio.
	Raw []int

	// Widths is the allocated render width per column, after prorating and
	// clamping. It is always the same length as Raw.
	Widths []int

	// Available is the content budget Widths was prorated against
	// (see AvailableWidth).
	Available int

	// Chrome is the frame overhead the allocation must leave room for
	// (see FrameChrome).
	Chrome int

	// Floor is the per-column minimum this allocation may not cross. It is
	// MinColumnWidth when the table is being squeezed and 1 when it is at its
	// natural width (nothing is being squeezed, so a genuinely tiny column is
	// allowed to stay tiny). Carrying it on the value is what lets ClampToFrame
	// run without re-deriving the mode the allocation was made under.
	Floor int
}

// BudgetTable allocates column widths for a table whose columns have intrinsic
// widths raw, to be drawn inside viewportWidth cells.
//
// raw is not modified and the returned budget owns its slices, so a caller may
// hand the same raw widths on every streaming tick without aliasing surprises.
//
// The zero-width and empty cases return an empty (but non-nil, safe to range)
// allocation rather than a panic: a table with no columns is a degenerate parse
// result, and a renderer must survive it, not crash on it.
func BudgetTable(raw []int, viewportWidth int) TableBudget {
	if len(raw) == 0 {
		return TableBudget{
			Widths:    []int{},
			Available: AvailableWidth(viewportWidth, 0),
			Chrome:    0,
			Floor:     1,
		}
	}

	// sumRaw is the total intrinsic demand. Every column's share is measured
	// against it, so the allocation is scale-invariant: doubling every cell
	// changes no individual allocation's RATIO, only its absolute size.
	sumRaw := sumInts(raw)
	available := AvailableWidth(viewportWidth, len(raw))

	// The floor is a property of the MODE, not of a column: a table that fits
	// keeps its natural widths (including a genuinely two-cell column), while a
	// squeezed one is floored so that no cell is too narrow to hold a word.
	squeezed := sumRaw > available
	floor := 1
	if squeezed {
		floor = MinColumnWidth
	}

	b := TableBudget{
		Raw:       append([]int(nil), raw...),
		Widths:    make([]int, len(raw)),
		Available: available,
		Chrome:    FrameChrome(len(raw)),
		Floor:     floor,
	}

	for i, w := range raw {
		if !squeezed {
			// The table fits at its natural width: hand every column exactly
			// what it asked for. A column measured at 0 still gets 1 so the
			// grid keeps a visible boundary instead of collapsing to nothing.
			b.Widths[i] = max(w, 1)
			continue
		}
		share := 0
		if sumRaw > 0 && w > 0 {
			share = available * w / sumRaw
		}
		b.Widths[i] = max(share, MinColumnWidth)
	}

	b.ClampToFrame(viewportWidth)
	return b
}

// Total is the sum of the allocated column widths — the cells the table's
// CONTENT occupies, excluding frame chrome.
func (b TableBudget) Total() int {
	n := 0
	for _, w := range b.Widths {
		n += w
	}
	return n
}

// OuterWidth is the width a grid drawn to this budget actually occupies:
// its content plus the frame chrome. It is the number that must not exceed the
// viewport, and it is why the clamp exists.
func (b TableBudget) OuterWidth() int {
	return b.Total() + b.Chrome
}

// Fits reports whether a grid drawn to this budget fits inside viewportWidth
// cells. False means the per-column floors alone exceed the budget — a table
// with more columns than a 40-cell pane can show — and the caller should fall
// back to a stacked key/value rendering rather than draw a frame that has to be
// torn.
func (b TableBudget) Fits(viewportWidth int) bool {
	return b.OuterWidth() <= viewportWidth
}

// ClampToFrame shrinks the allocation until OuterWidth fits viewportWidth,
// taking cells from the column with the most slack above its floor first.
//
// Shrinking the most-slack column first is what makes the clamp minimally
// destructive. A column allocated far more than it needs has, by construction,
// lost nothing that was load-bearing; the column that needed every cell it got
// is protected until nothing else can give. A greedy "shrink the first column"
// rule would instead sacrifice whichever column the author happened to write
// first, which is a worse outcome for the same number of cells.
//
// It reports whether the allocation now fits. A false return means the floors
// themselves are wider than the viewport (see Fits) and no allocation can
// satisfy both constraints; the Widths are left at their floors, which is the
// least-bad bounded state, and the caller is expected to degrade deliberately.
func (b *TableBudget) ClampToFrame(viewportWidth int) bool {
	if b == nil || len(b.Widths) == 0 {
		return true
	}
	// floors are the per-column minimums the allocation is not permitted to
	// cross: MinColumnWidth when the table is being squeezed, 1 when it is at
	// its natural width (nothing is being squeezed, so a tiny column may still
	// be tiny).
	floor := b.Floor
	if floor < 1 {
		floor = 1
	}

	over := b.OuterWidth() - viewportWidth
	if over <= 0 {
		return true
	}
	for over > 0 {
		best := -1
		bestSlack := 0
		for i, w := range b.Widths {
			slack := w - floor
			if slack <= 0 {
				continue
			}
			if best == -1 || slack > bestSlack {
				best, bestSlack = i, slack
			}
		}
		if best == -1 {
			// Every column is already at its floor. Nothing further can be
			// given up without violating the readability minimum.
			return false
		}
		take := min(over, bestSlack)
		b.Widths[best] -= take
		over -= take
	}
	return true
}

func sumInts(v []int) int {
	n := 0
	for _, x := range v {
		if x > 0 {
			n += x
		}
	}
	return n
}

// ── Cell normalisation ────────────────────────────────────────────────────────
//
// A table cell is the one Markdown construct whose content has to be rewritten
// before it can be laid out: unlike a paragraph, a cell has no block structure
// of its own, so the model's only way to express a break inside one is an HTML
// tag — and an HTML tag has no meaning in a terminal.

// cellBreakRe matches a raw-HTML line-break tag in every form models emit:
// `<br>`, `<br/>`, `<br />`, and any capitalisation or internal spacing of
// those. The slash is OPTIONAL, which is the load-bearing part: `<br>` is by
// far the most common spelling, and a pattern that required the self-closing
// slash would rewrite the majority of real cells and leave the visible leak
// untouched. It is deliberately narrow in the other direction — `<b>`, `<i>`,
// and `<code>` are inline emphasis, and a cell that mangled those would corrupt
// text rather than fix it, so they are not matched here.
var cellBreakRe = regexp.MustCompile(`(?i)<br[ \t]*/?[ \t]*>`)

// IsCellBreak reports whether a raw-HTML inline node is a line break and should
// therefore become a newline inside a cell.
//
// It exists so the AST path can classify a node WITHOUT rewriting the whole
// document: the node carries the tag's raw bytes, and a single anchored match is
// the cheapest possible answer to "is this a break?".
func IsCellBreak(raw string) bool {
	t := strings.TrimSpace(raw)
	if len(t) > 8 {
		return false
	}
	return cellBreakRe.MatchString(t)
}

// CellBreakSentinel is the private-use rune the pre-AST sanitization pass writes
// in place of an HTML line-break tag.
//
// It exists because a REAL newline cannot be written into a raw Markdown chunk
// before parsing: GFM ends a table row at a newline, so `a<br>b` rewritten to
// `a\nb` splits one cell into two rows and corrupts the table it was meant to
// fix. The sentinel is ordinary inline text, so goldmark still sees a single
// table row; the post-AST layer then materialises it back into a newline once
// the cell boundaries are known (see SanitizeCellBreaks).
const CellBreakSentinel = "\uE000"

// SanitizeCellBreaks rewrites the raw-HTML break tags inside a table cell into
// real newlines, so the cell wraps inside its prorated column instead of
// printing markup or fusing two words into one unbreakable token.
//
// It also materialises the pre-AST CellBreakSentinel left by
// SanitizeRawMarkdown. The rewrite is idempotent: running it over an
// already-clean cell is a no-op, so a caller may normalise defensively at every
// layer without compounding the result. Non-break tags are left verbatim — this
// function replaces line breaks and nothing else, because silently deleting
// markup a reader might need is a worse failure than showing it.
func SanitizeCellBreaks(cell string) string {
	if strings.Contains(cell, CellBreakSentinel) {
		cell = strings.ReplaceAll(cell, CellBreakSentinel, "\n")
	}
	if !strings.ContainsRune(cell, '<') {
		return cell
	}
	return cellBreakRe.ReplaceAllString(cell, "\n")
}

// SanitizeRawMarkdown normalises HTML line breaks and CRLF endings across a raw
// Markdown chunk BEFORE it reaches the AST parser.
//
// This is the pre-AST half of the cell-normalisation contract. The post-AST
// walk (see renderInlineContent) can only repair a `<br>` that goldmark chose to
// surface as a RawHTML inline node; a `<br>` that arrives buried in a table
// cell, a list item, or a streaming fragment that never got a second pass leaks
// through as literal markup. Rewriting the raw chunk is the only place that
// catches every spelling — `<br>`, `<br/>`, `<br />`, any capitalisation —
// before a parser can reinterpret it. CRLF is folded to LF in the same pass so
// a Windows-authored chunk does not leave a stray `\r` in a rendered cell.
//
// The break is rewritten to CellBreakSentinel rather than a literal newline, so
// a GFM table row is not split apart by the sanitizer itself; the sentinel is
// turned into a newline after parsing, when the cell that contains it is known.
//
// FENCED CODE IS PRESERVED. A code sample that documents HTML is content, not a
// directive, so lines inside a ``` / ~~~ fence are copied through verbatim
// rather than having their literal tags rewritten. The rewrite is otherwise
// idempotent and safe to run at every layer.
func SanitizeRawMarkdown(src string) string {
	if !strings.ContainsRune(src, '<') && !strings.ContainsRune(src, '\r') {
		return src
	}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")

	var b strings.Builder
	b.Grow(len(src))
	inFence := false
	fence := ""
	for _, line := range strings.SplitAfter(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if marker, ok := markdownFenceMarker(trimmed); ok {
			switch {
			case !inFence:
				inFence, fence = true, marker
			case strings.HasPrefix(trimmed, fence):
				b.WriteString(line)
				inFence, fence = false, ""
				continue
			}
		}
		if inFence {
			b.WriteString(line)
			continue
		}
		b.WriteString(cellBreakRe.ReplaceAllString(line, CellBreakSentinel))
	}
	return b.String()
}

// markdownFenceMarker reports whether a trimmed line opens or closes a fenced
// code block, returning the fence run (three or more backticks or tildes).
func markdownFenceMarker(trimmed string) (string, bool) {
	if len(trimmed) < 3 {
		return "", false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return "", false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return "", false
	}
	return trimmed[:n], true
}

// CellIntrinsicWidth is the width a cell DEMANDS of its column: the widest of
// its lines, measured in terminal cells and ANSI-aware.
//
// It is the per-cell half of the intrinsic-width scan that produces the `raw`
// vector BudgetTable prorates, and it exists so both table renderers measure the
// same thing. Measuring the cell as a single line — `ansi.StringWidth` over the
// whole string — counts every line's width and reports the SUM, so a three-line
// cell demands three times the room it will ever occupy. In a squeezed table
// that phantom demand is what starves the columns next to it.
func CellIntrinsicWidth(cell string) int {
	widest := 0
	for _, line := range strings.Split(SanitizeCellBreaks(cell), "\n") {
		if w := ansi.StringWidth(line); w > widest {
			widest = w
		}
	}
	return widest
}
