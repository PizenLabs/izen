// table_budgeting_test.go — END-TO-END CONTRACTS FOR TABLE COLUMN BUDGETING.
//
// The unit tests in internal/ui/markdown pin the allocation arithmetic. These
// pin the RENDERED grid, because the arithmetic being correct is not the same as
// the table being readable: a budget that is one cell short still tears the
// `│` boundary, and a table that stays in bounds while turning into a bullet
// list has not solved the problem the budgeter was written for.
package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// sampleTable is a realistic three-column table: a short label column, a
// verbose prose column, and a notes column. That shape is what prorating exists
// for — a greedy allocator hands everything to the prose column and starves the
// other two.
const sampleTable = "| Component | Responsibility | Notes |\n" +
	"|---|---|---|\n" +
	"| Parser | Converts a raw byte stream into a typed AST for downstream consumers | hot path |\n" +
	"| Renderer | Projects the AST onto the terminal with bounded width | cold |\n" +
	"| Buffer | Holds back partially received blocks until they are structurally final | hot path |\n"

func renderTableText(raw string, width int) string {
	return ansi.Strip(renderTable(raw, width))
}

// TestTableStaysFramedDownToFortyColumns is the DoD. At every width the grid
// must keep all four border glyphs on every row, and no row may exceed the
// width it was given.
//
// The frame check is the substantive one. A table that keeps its data but loses
// its `│` columns is not a table, and that is exactly what an un-budgeted
// renderer produces: cells overflow, the separators drift out of column, and the
// grid becomes a staircase.
func TestTableStaysFramedDownToFortyColumns(t *testing.T) {
	for _, w := range []int{40, 48, 60, 76, 100, 120, 200} {
		out := renderTableText(sampleTable, w)
		lines := strings.Split(out, "\n")
		if len(lines) < 4 {
			t.Fatalf("width %d: table has %d rows, want at least 4\n%s", w, len(lines), out)
		}

		// Every row is the same width and carries both vertical boundaries.
		want := ansi.StringWidth(lines[0])
		if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
			t.Errorf("width %d: missing top corners\n%s", w, out)
		}
		if !strings.HasSuffix(lines[len(lines)-1], "┘") {
			t.Errorf("width %d: missing bottom corners\n%s", w, out)
		}
		for i, l := range lines {
			got := ansi.StringWidth(l)
			if got > w {
				t.Errorf("width %d: row %d is %d cells\n%q", w, i, got, l)
			}
			if strings.HasPrefix(l, "│") && !strings.HasSuffix(l, "│") {
				t.Errorf("width %d: row %d lost its right boundary (torn frame)\n%q", w, i, l)
			}
			// Every data/rule row is padded to the same width, which is what
			// makes the columns line up.
			if got != want {
				t.Errorf("width %d: row %d is %d cells, top border is %d — columns are ragged\n%q",
					w, i, got, want, l)
			}
		}
	}
}

// tableColumn reassembles the text of one column from a rendered grid.
//
// Reading a wrapped cell back out of a grid is not the same as reading the
// whole table: a rendered row interleaves every column, so squashing the entire
// output produces "ComponentResponsibiNotes…ParserConvertsa…" and a single
// cell's sentence never appears contiguously. Slicing the column band out of
// each row and concatenating reconstructs exactly what that cell said, which is
// the thing the content assertions need to compare against.
//
// Rule rows contribute nothing and are skipped.
func tableColumn(rendered string, col int) string {
	var b strings.Builder
	for _, row := range strings.Split(rendered, "\n") {
		if !strings.HasPrefix(row, "│") {
			continue // a rule row: ─ no content
		}
		cells := strings.Split(row, "│")
		// cells[0] is empty (the row starts with the boundary) and cells[1] is
		// column 0.
		if col+1 >= len(cells) {
			continue
		}
		b.WriteString(cells[col+1])
	}
	return b.String()
}

// squashText removes every whitespace character, making a wrapped cell directly
// comparable to its source text. Whitespace is dropped entirely rather than
// collapsed because a cell is broken at arbitrary points — a word boundary or a
// hard cell boundary — and only its character sequence is invariant. In a
// 10-cell column the word "structurally" does not fit and is split as
// "structural" / "ly"; that is the same two-pass discipline the banners use, and
// the correct trade, because a broken word is still readable while a dropped one
// is a lie about what the model said.
func squashText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestTableWrapsCellsInsteadOfOverflowing is the behaviour the budgeter buys: a
// cell too long for its column WRAPS inside the grid and the row grows, rather
// than pushing the border past the pane.
func TestTableWrapsCellsInsteadOfOverflowing(t *testing.T) {
	out := renderTableText(sampleTable, 40)
	prose := squashText(tableColumn(out, 1))
	for _, want := range []string{
		"Converts a raw byte stream into a typed AST for downstream consumers",
		"Projects the AST onto the terminal with bounded width",
		"Holds back partially received blocks until they are structurally final",
	} {
		if !strings.Contains(prose, squashText(want)) {
			t.Errorf("wrapped cell lost content: %q\n%s", want, out)
		}
	}
	// Wrapping means more rows than the source has lines.
	if rows := len(strings.Split(out, "\n")); rows < 8 {
		t.Errorf("expected the verbose column to wrap onto extra rows, got %d\n%s", rows, out)
	}
}

// TestTableFallsBackOnlyWhenItCannotBeFramed: the stacked listing is the honest
// degradation, but using it while a grid would have fit is a regression — the
// old renderer fell back below 60 cells, which is most split-panes.
func TestTableFallsBackOnlyWhenItCannotBeFramed(t *testing.T) {
	// Three columns at 40 cells IS framable, so it must be a grid.
	out := renderTableText(sampleTable, 40)
	if !strings.Contains(out, "┌") {
		t.Errorf("a 3-column table at 40 cells fell back to a listing instead of a grid:\n%s", out)
	}
	if strings.Contains(out, "• Component:") {
		t.Errorf("stacked fallback used where a grid fits:\n%s", out)
	}

	// Ten columns at 40 cells is NOT framable at any readable width, so the
	// listing is correct — and it must still be bounded.
	var wide strings.Builder
	wide.WriteString("| a | b | c | d | e | f | g | h | i | j |\n|---|---|---|---|---|---|---|---|---|---|\n")
	wide.WriteString("| 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 |\n")
	fallback := renderTableText(wide.String(), 40)
	if strings.Contains(fallback, "┌") {
		t.Errorf("a 10-column table at 40 cells was drawn as a grid; it cannot fit\n%s", fallback)
	}
	if !strings.Contains(fallback, "a:") {
		t.Errorf("stacked fallback lost the column relationship (no header labels)\n%s", fallback)
	}
	for i, l := range strings.Split(fallback, "\n") {
		if got := ansi.StringWidth(l); got > 40 {
			t.Errorf("fallback row %d is %d cells, want <= 40\n%q", i, got, l)
		}
	}
}

// TestTablePreservesAllData pins the content contract. Budgeting allocates
// width; it must never allocate away information.
func TestTablePreservesAllData(t *testing.T) {
	for _, w := range []int{40, 60, 120} {
		out := renderTableText(sampleTable, w)
		columns := []string{
			tableColumn(out, 0),
			tableColumn(out, 1),
			tableColumn(out, 2),
		}
		for col, want := range [][]string{
			{"Component", "Parser", "Renderer", "Buffer"},
			{
				"Responsibility",
				"Converts a raw byte stream into a typed AST for downstream consumers",
				"Projects the AST onto the terminal with bounded width",
				"Holds back partially received blocks until they are structurally final",
			},
			{"Notes", "hot path", "cold"},
		} {
			for _, cell := range want {
				if !strings.Contains(squashText(columns[col]), squashText(cell)) {
					t.Errorf("width %d: column %d lost %q\n%s", w, col, cell, out)
				}
			}
		}
	}
}

// TestTableProratesRatherThanStarves is the property that separates a prorated
// budget from a greedy one, observed on the rendered grid: the short label
// column keeps a usable width even though the prose column wants far more.
//
// A greedy allocator gives the prose column everything and leaves the label
// column at one or two cells, which is not a narrower table — it is a different,
// worse table.
func TestTableProratesRatherThanStarves(t *testing.T) {
	out := renderTableText(sampleTable, 60)
	lines := strings.Split(out, "\n")

	// Column boundaries are the `│` positions on the top border.
	var cols []int
	for i, r := range lines[0] {
		if r == '┬' {
			cols = append(cols, i)
		}
	}
	if len(cols) != 2 {
		t.Fatalf("expected 3 columns, found %d boundaries in %q", len(cols)+1, lines[0])
	}
	widths := []int{cols[0] + 1, cols[1] - cols[0] - 1, len(lines[0]) - 1 - cols[1]}
	for i, cw := range widths {
		if cw < 8 {
			t.Errorf("column %d is only %d cells wide at viewport 60 — the budget starved it:\n%s",
				i, cw, out)
		}
	}
	// And the prose column is still recognisably the prose column.
	if widths[1] <= widths[0] {
		t.Errorf("prose column (%d) is not wider than the label column (%d): proration collapsed\n%s",
			widths[1], widths[0], out)
	}
}

// TestTableIsBoundedInsideItsWidget: in the real UI a table is drawn inside a
// widget that prefixes every row with a "│ " gutter, so the table's own budget
// must be the WIDGET's budget minus that gutter. Getting this wrong is how a
// table looked correct in isolation and overflowed the pane in the document.
func TestTableIsBoundedInsideItsWidget(t *testing.T) {
	for _, w := range []int{40, 60, 80, 120} {
		m := newTestModel()
		m.width = w
		out := ansi.Strip(m.renderStreamingContent(sampleTable, w))
		widest := 0
		for _, l := range strings.Split(out, "\n") {
			if x := ansi.StringWidth(l); x > widest {
				widest = x
			}
		}
		// renderStreamingContent budgets to width-4 for safety.
		if limit := w - 4; widest > limit {
			t.Errorf("width %d: widget content is %d cells, want <= %d\n%s", w, widest, limit, out)
		}
	}
}

// TestTableHandlesDegenerateInput: a renderer must survive malformed model
// output, because a panic takes the whole session with it and truncated tables
// are normal while a response streams.
func TestTableHandlesDegenerateInput(t *testing.T) {
	for _, raw := range []string{
		"",
		"|",
		"||",
		"| a |",
		"| a | b |",
		"|---|---|",
		"| only header |",
		"not a table at all",
		"| a | b |\n|---|---|\n| 1 |\n| 2 | 3 | 4 |\n",
		"| " + strings.Repeat("x", 500) + " |\n|---|\n",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on degenerate table %q: %v", raw, r)
				}
			}()
			out := renderTableText(raw, 40)
			for i, l := range strings.Split(out, "\n") {
				if got := ansi.StringWidth(l); got > 40 {
					t.Errorf("degenerate input %q produced row %d of %d cells", raw, i, got)
				}
			}
		}()
	}
}

// breakTable is a table whose cells use the HTML line break that is the only
// break GFM allows inside a cell. All three spellings appear, because models emit
// all three and a fix that handles only the self-closing form leaves the visible
// leak in place for the other two.
const breakTable = "| Component | Responsibility | Notes |\n" +
	"|---|---|---|\n" +
	"| Parser | Converts bytes<br>into a typed AST | hot path |\n" +
	"| Buffer | Holds back blocks<br/>until they are final | hot<br />path |\n"

// TestTableCellsBreakCleanlyWithoutLeakingMarkup is the end-to-end form of the
// cell normalisation contract, on BOTH renderers.
//
// The leak is the obvious half and the corruption is the subtle one. Printing
// `<br>` is ugly; DROPPING it is worse, because `bytes<br>into` becomes
// `bytesinto` — one unbreakable token that splits mid-word at a narrow width and
// is no longer what the model said. Both come from the same missing newline, so
// asserting only "no `<br>` in the output" would pass on a renderer that
// deleted the tag. The content assertions are what make this a real test: the
// break must appear, and both halves of the sentence must survive.
func TestTableCellsBreakCleanlyWithoutLeakingMarkup(t *testing.T) {
	for _, w := range []int{40, 50, 60, 80, 120} {
		for name, out := range map[string]string{
			"stream": renderTableText(breakTable, w),
			"ast":    renderASTText(breakTable, w),
		} {
			if strings.Contains(strings.ToLower(out), "<br") {
				t.Errorf("%s table at %d leaked a raw break tag:\n%s", name, w, out)
			}
			// Every half of every broken cell survives, whitespace-insensitively.
			// The sentences below are the ones that FUSE into a single
			// unbreakable token when the break is dropped rather than honoured.
			// They are read out of their own COLUMN: a rendered row interleaves
			// every column, so squashing the whole table would find "Converts"
			// and "bytes" separated by two other cells' worth of text.
			prose := squashText(tableColumn(out, 1))
			notes := squashText(tableColumn(out, 2))
			for _, want := range []string{
				"Converts bytes",
				"into a typed AST",
				"Holds back blocks",
				"until they are final",
			} {
				if !strings.Contains(prose, squashText(want)) {
					t.Errorf("%s table at %d lost %q from the prose column — a dropped "+
						"break fuses words into one unbreakable token\n%s", name, w, want, out)
				}
			}
			// The notes cell carries two breaks, so both of its halves have to be
			// there: "hot<br />path" dropped is "hotpath", one word.
			if !strings.Contains(notes, "hot") || !strings.Contains(notes, "path") {
				t.Errorf("%s table at %d lost half of the notes cell\n%s", name, w, out)
			}
			// And the break is a REAL break: it grows its row. The minimum for
			// this table is 8 rows — top rule, header, header separator, two
			// data rows of two lines each (the broken cells), bottom rule — so
			// anything shorter means a break was dropped and the cells fitted on
			// one line each, which is exactly the corruption under test.
			if rows := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); rows < 8 {
				t.Errorf("%s table at %d: the broken cells did not grow their rows (%d)\n%s",
					name, w, rows, out)
			}
		}
	}
}

// TestBrokenCellsDoNotStarveTheirNeighbours is the budget half. A cell's
// INTRINSIC demand is the width of its widest LINE, so introducing a break in one
// column must not change that column's share of a prorated budget by more than
// the break itself implies — and must certainly not make the table degrade to a
// stacked listing that it would have framed happily without the break.
func TestBrokenCellsDoNotStarveTheirNeighbours(t *testing.T) {
	// Same table, one cell broken in two and the other whole.
	whole := "| Component | Responsibility | Notes |\n|---|---|---|\n" +
		"| Parser | Converts bytes into a typed AST for downstream consumers | hot path |\n" +
		"| Buffer | Holds back partially received blocks until they are final | cold |\n"
	broken := "| Component | Responsibility | Notes |\n|---|---|---|\n" +
		"| Parser | Converts bytes<br>into a typed AST for downstream consumers | hot path |\n" +
		"| Buffer | Holds back partially received blocks until they are final | cold |\n"

	boundaryColumns := func(rendered string) int {
		lines := strings.Split(rendered, "\n")
		n := 0
		for _, r := range lines[0] {
			if r == '┬' {
				n++
			}
		}
		return n
	}

	for _, w := range []int{40, 60, 80, 120} {
		for name, src := range map[string]string{"whole": whole, "broken": broken} {
			out := renderTableText(src, w)
			if cols := boundaryColumns(out); cols != 2 {
				t.Errorf("%s table at %d: expected 3 columns, found %d boundaries\n%s",
					name, w, cols, out)
			}
		}
	}
}

// TestASTTableDrawsAClosedFrame: the AST renderer shares the streaming
// renderer's column budget, so the same markdown must come out looking like the
// same table whichever path renders it. A grid with no top rule and no bottom
// rule is not a narrower grid — it is a corrupt one, and it is what the header
// separator used to be mistaken for.
func TestASTTableDrawsAClosedFrame(t *testing.T) {
	for _, w := range []int{40, 60, 80, 120} {
		// renderAST separates blocks with a newline, so the table arrives with a
		// trailing blank row. It is not part of the frame.
		out := renderASTText(sampleTable, w)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
			t.Errorf("ast table at %d has no top rule:\n%s", w, out)
		}
		if !strings.HasPrefix(lines[len(lines)-1], "└") || !strings.HasSuffix(lines[len(lines)-1], "┘") {
			t.Errorf("ast table at %d has no bottom rule:\n%s", w, out)
		}
		// Every row is the same width, or the columns are ragged.
		want := ansi.StringWidth(lines[0])
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != want {
				t.Errorf("ast table at %d: row %d is %d cells, top border is %d\n%q",
					w, i, got, want, l)
			}
			if strings.HasPrefix(l, "│") && !strings.HasSuffix(l, "│") {
				t.Errorf("ast table at %d: row %d lost its right boundary\n%q", w, i, l)
			}
		}
	}
}

// TestASTTableMatchesStreamTable: two renderers, one budget. They are separate
// code paths over separate input formats, and the DoD says a table must be
// bounded in both — so the invariant is asserted on both rather than trusted to
// hold in one.
func TestASTTableMatchesStreamTable(t *testing.T) {
	for _, w := range []int{40, 60, 80, 120} {
		for name, out := range map[string]string{
			"stream": renderTableText(sampleTable, w),
			"ast":    renderASTText(sampleTable, w),
		} {
			widest := 0
			for _, l := range strings.Split(out, "\n") {
				if x := ansi.StringWidth(l); x > widest {
					widest = x
				}
			}
			if widest > w {
				t.Errorf("%s table at %d: widest row %d cells\n%s", name, w, widest, out)
			}
		}
	}
}

// TestPreASTSanitizationNeverLeaksThroughTheAST verifies the pre-AST pass at the
// surface that consumes it: a raw chunk carrying every `<br>` spelling, a CRLF,
// a prose break, a blockquote break, and a table cell break must render with no
// literal markup and no private-use sentinel anywhere, on the AST pipeline.
func TestPreASTSanitizationNeverLeaksThroughTheAST(t *testing.T) {
	src := "Intro line<br>second line.\r\n\r\n" +
		"> quoted a<br/>quoted b\r\n\r\n" +
		"| Component | Responsibility |\n" +
		"|---|---|\n" +
		"| Parser | Converts bytes<br />into a typed AST |\n"

	for _, w := range []int{40, 60, 80, 120} {
		out := renderASTText(src, w)
		if strings.Contains(strings.ToLower(out), "<br") {
			t.Errorf("width %d: a raw break tag leaked through the AST renderer:\n%s", w, out)
		}
		if strings.Contains(out, "\uE000") {
			t.Errorf("width %d: the CellBreakSentinel leaked into rendered output:\n%s", w, out)
		}
		if strings.Contains(out, "\r") {
			t.Errorf("width %d: a carriage return survived rendering:\n%s", w, out)
		}
		// The broken content survives as separate lines/words.
		for _, want := range []string{"Intro line", "second line.", "quoted a", "quoted b", "Converts bytes", "into a typed AST"} {
			if !strings.Contains(squashText(out), squashText(want)) {
				t.Errorf("width %d: %q was lost after sanitization:\n%s", w, want, out)
			}
		}
	}
}
