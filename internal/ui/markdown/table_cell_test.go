// table_cell_test.go — A CELL IS NORMALISED BEFORE IT IS BUDGETED.
//
// # WHY THIS FILE EXISTS
//
// A Markdown table cell has no block structure of its own, so the model's only
// way to express a break inside one is an HTML tag — `<br>`, `<br/>`, `<br />`.
// Goldmark parses all three as a raw-HTML inline node, and a terminal has no
// meaning to give that node. Left alone, a cell does one of two wrong things:
//
//   - PRINTS the tag. `bytes<br>into` reaches the frame as literal markup: the
//     reader sees syntax the model never meant to emit, and four characters of
//     column budget are spent on a break that is never drawn.
//   - DROPS the tag. `bytes<br>into` becomes `bytesinto` — two words fused into
//     one token, which then cannot be word-wrapped, so a cell that used to break
//     at a space now breaks mid-word and the text is no longer what was said.
//
// Rewriting the break into "\n" resolves both, and it has to happen BEFORE the
// width scan, because a width scan that sees `<br>` charges for the tag and a
// width scan that sees the joined string charges for content that is never drawn
// on any single row. Both mistakes starve the columns beside the broken cell.
package markdown

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestSanitizeCellBreaksRewritesEverySpelling: the three spellings are all real
// model output, and the self-closing slash is OPTIONAL — a pattern that required
// it would rewrite the minority form and leave the visible leak in place.
func TestSanitizeCellBreaksRewritesEverySpelling(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"bare tag", "bytes<br>into", "bytes\ninto"},
		{"self-closing", "bytes<br/>into", "bytes\ninto"},
		{"spaced self-closing", "bytes<br />into", "bytes\ninto"},
		{"extra internal space", "bytes<br  />into", "bytes\ninto"},
		{"tab before slash", "bytes<br\t/>into", "bytes\ninto"},
		{"uppercase", "bytes<BR>into", "bytes\ninto"},
		{"mixed case", "bytes<Br/>into", "bytes\ninto"},
		{"several in one cell", "a<br>b<br />c<br/>d", "a\nb\nc\nd"},
		{"at the start", "<br>leading", "\nleading"},
		{"at the end", "trailing<br>", "trailing\n"},
		{"no break at all", "bytes into", "bytes into"},
		{"empty cell", "", ""},
	} {
		if got := SanitizeCellBreaks(c.in); got != c.want {
			t.Errorf("%s: SanitizeCellBreaks(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestSanitizeCellBreaksLeavesOtherMarkupAlone: this function replaces LINE
// BREAKS and nothing else. Silently deleting other tags would corrupt text a
// reader might need, which is strictly worse than showing it — and a pattern
// loose enough to catch `<b>` would do exactly that.
func TestSanitizeCellBreaksLeavesOtherMarkupAlone(t *testing.T) {
	for _, in := range []string{
		"<b>bold</b> and <i>italic</i>",
		"<code>inline</code>",
		"<brx>not a break",
		"<brr>not a break",
		"a < b and b > c",
		"<a href=\"x\">link</a>",
		"<br class=\"x\">",
	} {
		if got := SanitizeCellBreaks(in); got != in {
			t.Errorf("SanitizeCellBreaks(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestSanitizeCellBreaksIsIdempotent: a caller may normalise defensively at
// every layer, and normalisation that compounded — or that re-matched its own
// output — would be a different answer on the second renderer than on the first.
func TestSanitizeCellBreaksIsIdempotent(t *testing.T) {
	for _, in := range []string{"a<br>b", "a<br/>b", "<br>", "plain", "", "a\nb"} {
		once := SanitizeCellBreaks(in)
		if twice := SanitizeCellBreaks(once); twice != once {
			t.Errorf("SanitizeCellBreaks is not idempotent on %q: %q then %q", in, once, twice)
		}
	}
}

// TestIsCellBreakMatchesExactlyTheBreakForms: the AST path classifies a
// raw-HTML node with this, so a false positive turns a dropped markup node into
// a line break inside a cell, and a false negative leaves the break unhandled.
func TestIsCellBreak(t *testing.T) {
	for _, raw := range []string{"<br>", "<br/>", "<br />", "<BR>", "<Br />", "  <br>  ", "<br>"} {
		if !IsCellBreak(raw) {
			t.Errorf("IsCellBreak(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"", "<b>", "</b>", "<brx>", "br", "<p>", "<a href=\"x\">"} {
		if IsCellBreak(raw) {
			t.Errorf("IsCellBreak(%q) = true, want false", raw)
		}
	}
	// A node that carries a long payload is not a break, and the length guard is
	// what makes that a cheap question to answer.
	if IsCellBreak(strings.Repeat("<br>", 100)) {
		t.Error("IsCellBreak accepted a 400-character node")
	}
}

// TestCellIntrinsicWidthMeasuresTheWidestLine is the measurement half of the
// fix, and it is where a naive implementation quietly breaks every table it
// touches.
//
// Measuring the cell as ONE line — the width of the whole string — reports the
// SUM of its lines, so a three-line cell demands three times the room it will
// ever occupy. In a squeezed table that phantom demand is exactly what starves
// the columns next to it, and the table degrades to a stacked listing long
// before it needs to.
func TestCellIntrinsicWidthMeasuresTheWidestLine(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want int
	}{
		{"single short line", "ab", 2},
		{"single long line", "abcdef", 6},
		{"widest line wins", "a\nabcd\nab", 4},
		{"equal lines", "ab\nab", 2},
		{"empty", "", 0},
		{"blank lines do not count", "abc\n\n\n", 3},
		// The break tag is FOUR characters wide and draws nothing: the demand is
		// the widest side of the break, not the joined string.
		{"bare tag does not add width", "abcdef<br>abc", 6},
		{"self-closing tag does not add width", "abcdef<br/>abc", 6},
		// A CJK line is two cells per rune, so the widest line is measured in
		// cells and not in runes.
		{"wide runes", "日本語\nab", 6},
	} {
		if got := CellIntrinsicWidth(c.in); got != c.want {
			t.Errorf("%s: CellIntrinsicWidth(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

// TestCellIntrinsicWidthIsAnsiAware: a cell may already carry styling by the
// time it is measured, and counting escape-sequence BYTES as cells would
// over-report a styled cell's demand by its entire SGR run.
func TestCellIntrinsicWidthIsAnsiAware(t *testing.T) {
	styled := "\x1b[38;2;249;226;175mab\x1b[0m"
	if got, want := CellIntrinsicWidth(styled), 2; got != want {
		t.Errorf("CellIntrinsicWidth(styled) = %d, want %d", got, want)
	}
	if got := ansi.StringWidth(styled); got != 2 {
		t.Fatalf("the styled cell is not 2 cells wide to begin with (got %d)", got)
	}
}

// TestBudgetTableIsUnaffectedByBreakTags: two tables whose cells are
// byte-identical apart from how their breaks are spelled must receive the SAME
// allocation. Anything else means the budget is a function of the model's
// punctuation rather than of its content, and two renderers of the same table
// would lay it out two different ways.
func TestBudgetTableIsUnaffectedByBreakTags(t *testing.T) {
	tagged := BudgetTable([]int{
		CellIntrinsicWidth("Component"),
		CellIntrinsicWidth("Converts bytes<br>into a typed AST"),
		CellIntrinsicWidth("hot path"),
	}, 60)
	plain := BudgetTable([]int{
		CellIntrinsicWidth("Component"),
		CellIntrinsicWidth("Converts bytes\ninto a typed AST"),
		CellIntrinsicWidth("hot path"),
	}, 60)

	if tagged.Available != plain.Available || tagged.Chrome != plain.Chrome {
		t.Fatalf("the budget envelope differs: %+v vs %+v", tagged, plain)
	}
	for i := range tagged.Widths {
		if tagged.Widths[i] != plain.Widths[i] {
			t.Errorf("column %d allocated %d cells for a `<br>` cell and %d for a "+
				"newline cell — the budget is reading the markup", i, tagged.Widths[i], plain.Widths[i])
		}
	}

	// And the same holds against the naive alternative. Measuring the cell as one
	// string charges for the joined text AND for the four-character tag, so the
	// column is allocated a width it will never occupy — and in a squeezed table
	// that phantom demand is what starves its neighbours.
	naive := BudgetTable([]int{
		CellIntrinsicWidth("Component"),
		ansi.StringWidth("Converts bytes<br>into a typed AST"),
		CellIntrinsicWidth("hot path"),
	}, 60)
	if naive.Widths[1] <= tagged.Widths[1] {
		t.Errorf("the naive whole-cell measurement allocated only %d cells, the per-line "+
			"one %d — the assertion is meaningless if the phantom demand is not larger",
			naive.Widths[1], tagged.Widths[1])
	}
}

// TestSanitizeRawMarkdownRewritesBreaksAndCRLF pins the pre-AST contract: every
// spelling and a Windows CRLF are normalised in the raw chunk before parsing.
// The break is held as CellBreakSentinel rather than a literal newline, so the
// assertion is on the sentinel — materialising it via SanitizeCellBreaks is the
// second half and is asserted in the same test.
func TestSanitizeRawMarkdownRewritesBreaksAndCRLF(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"bare tag", "a<br>b", "a" + CellBreakSentinel + "b"},
		{"self-closing", "a<br/>b", "a" + CellBreakSentinel + "b"},
		{"spaced self-closing", "a<br />b", "a" + CellBreakSentinel + "b"},
		{"uppercase", "a<BR>b", "a" + CellBreakSentinel + "b"},
		{"crlf", "a\r\nb", "a\nb"},
		{"crlf with break", "a\r\nb<br>c", "a\nb" + CellBreakSentinel + "c"},
		{"plain", "a\nb", "a\nb"},
		{"long tag", "a<br class=\"x\">b", "a<br class=\"x\">b"},
	} {
		got := SanitizeRawMarkdown(c.in)
		if got != c.want {
			t.Errorf("%s: SanitizeRawMarkdown(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		// Materialised, the sentinel is a real newline and no CR survives.
		materialised := SanitizeCellBreaks(got)
		if strings.Contains(materialised, CellBreakSentinel) || strings.Contains(materialised, "\r") {
			t.Errorf("%s: materialised output still carries a sentinel/CR: %q", c.name, materialised)
		}
	}
}

// TestSanitizeRawMarkdownKeepsTableRowsWhole is the load-bearing constraint: a
// GFM table row ends at a newline, so a sanitizer that wrote a real newline into
// `| a | b<br>c |` would split one row into two and destroy the table. The
// sentinel is ordinary inline text, so the row survives parsing and the break is
// materialised only once the cell exists.
func TestSanitizeRawMarkdownKeepsTableRowsWhole(t *testing.T) {
	const row = "| Parser | Converts bytes<br>into a typed AST | hot path |"
	got := SanitizeRawMarkdown(row)
	if strings.Contains(got, "\n") {
		t.Fatalf("the sanitizer split a table row: %q", got)
	}
	materialised := SanitizeCellBreaks(got)
	if !strings.Contains(materialised, "Converts bytes\ninto a typed AST") {
		t.Fatalf("the break did not materialise inside the cell: %q", materialised)
	}
}

// TestSanitizeRawMarkdownPreservesFencedCode: a code sample that documents HTML
// is content, not a directive. Rewriting its literal tags would corrupt the very
// thing the author wanted to show.
func TestSanitizeRawMarkdownPreservesFencedCode(t *testing.T) {
	src := "text<br>outside\n\n```html\n<p>a<br>b</p>\n```\n\ntext<br>again\n"
	got := SanitizeRawMarkdown(src)
	if !strings.Contains(got, "<p>a<br>b</p>") {
		t.Errorf("the sanitizer rewrote a tag inside a fenced code block: %q", got)
	}
	if strings.Contains(got, "text<br>") {
		t.Errorf("the sanitizer left a tag outside the fence: %q", got)
	}
	if strings.Count(got, CellBreakSentinel) != 2 {
		t.Errorf("want two materialisable breaks outside the fence, got %d in %q",
			strings.Count(got, CellBreakSentinel), got)
	}
}

// TestSanitizeRawMarkdownIsIdempotent: the pass runs at every layer, so running
// it twice must not compound or re-match its own output.
func TestSanitizeRawMarkdownIsIdempotent(t *testing.T) {
	for _, in := range []string{"a<br>b", "a\r\nb", CellBreakSentinel, "plain", "", "```\na<br>b\n```"} {
		once := SanitizeRawMarkdown(in)
		if twice := SanitizeRawMarkdown(once); twice != once {
			t.Errorf("SanitizeRawMarkdown is not idempotent on %q: %q then %q", in, once, twice)
		}
	}
}
