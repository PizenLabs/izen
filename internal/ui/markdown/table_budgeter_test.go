// table_budgeter_test.go — CONTRACTS FOR PRORATED TABLE COLUMN BUDGETING.
//
// The budgeter exists because a table's layout is a function of its whole body:
// every column wants `max(cell width)` and nothing was allocating the finite
// budget between that demand and the pane. These tests pin the two properties
// that make the difference between a readable table and a torn one — the
// allocation is PRORATED, and the result provably FITS.
package markdown

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// splitLines and cellWidth keep the width assertions readable: a cell's width is
// its widest rendered line, measured in terminal cells.
func splitLines(s string) []string { return strings.Split(s, "\n") }

func cellWidth(s string) int { return ansi.StringWidth(s) }

// TestAvailableWidthIsTheSpecifiedArithmetic pins the budget formula itself:
//
//	viewportWidth - numColumns - TableConstantChrome
//
// It is exported precisely so this can be asserted directly. A budget buried
// inside an allocation loop is a budget nobody can check, and "the table was
// too wide" is discovered by looking at it, which is what the budget was meant
// to prevent.
func TestAvailableWidthIsTheSpecifiedArithmetic(t *testing.T) {
	for _, c := range []struct{ viewport, cols int }{
		{40, 1}, {40, 3}, {40, 6}, {76, 3}, {120, 5}, {200, 2},
	} {
		want := c.viewport - c.cols - TableConstantChrome
		if got := AvailableWidth(c.viewport, c.cols); got != want {
			t.Errorf("AvailableWidth(%d, %d) = %d, want %d",
				c.viewport, c.cols, got, want)
		}
	}
}

// TestFrameChromeMatchesTheDrawnGrid: the chrome term must describe the grid the
// renderer actually draws, or the clamp reserves the wrong number of cells and
// the frame still tears.
func TestFrameChromeMatchesTheDrawnGrid(t *testing.T) {
	for cols, want := range map[int]int{0: 0, 1: 4, 2: 7, 3: 10, 6: 19} {
		if got := FrameChrome(cols); got != want {
			t.Errorf("FrameChrome(%d) = %d, want %d", cols, got, want)
		}
	}
	// A negative column count is a degenerate parse result, not a crash.
	if got := FrameChrome(-3); got != 0 {
		t.Errorf("FrameChrome(-3) = %d, want 0", got)
	}
}

// TestBudgetTableGivesNaturalWidthWhenItFits: when the table's intrinsic demand
// already fits, the budget must not touch it. Shrinking a table that fits is a
// regression dressed as a fix — the reader loses columns for no reason.
func TestBudgetTableGivesNaturalWidthWhenItFits(t *testing.T) {
	raw := []int{5, 8, 3}
	b := BudgetTable(raw, 80)
	for i := range raw {
		if b.Widths[i] != raw[i] {
			t.Errorf("column %d: width = %d, want %d (table fits, allocation must be untouched)",
				i, b.Widths[i], raw[i])
		}
	}
	if b.Floor != 1 {
		t.Errorf("Floor = %d, want 1 (nothing is being squeezed)", b.Floor)
	}
	if !b.Fits(80) {
		t.Error("a table that fits reported Fits() == false")
	}
}

// TestBudgetTableProratesWhenSqueezed pins the proration formula:
//
//	max(MinColumnWidth, floor(totalAvailableWidth * rawWidth[i] / sum(rawWidth)))
//
// The property that matters is RATIO preservation: a column that asked for
// twice as much as another must receive roughly twice as much. A greedy
// allocation ("give column 0 what it wants until the budget is gone") also
// produces widths that sum to the budget, and it fails exactly this test — which
// is why the assertion is about ratios and not about the total.
//
// Every column here clears MinColumnWidth, because the floor is a separate
// (and order-destroying) mechanism with its own test below.
func TestBudgetTableProratesWhenSqueezed(t *testing.T) {
	// A clean 3:1 intrinsic ratio between two columns, with both shares well
	// clear of the floor. The floor has its own test; mixing the two mechanisms
	// here would make a failure ambiguous between them.
	raw := []int{60, 20}
	const viewport = 60
	b := BudgetTable(raw, viewport)

	sumRaw := sumInts(raw)
	if sumRaw <= b.Available {
		t.Fatalf("test setup: demand %d must exceed budget %d", sumRaw, b.Available)
	}
	for i := range b.Widths {
		share := float64(b.Available) * float64(raw[i]) / float64(sumRaw)
		if b.Widths[i] < MinColumnWidth {
			t.Errorf("column %d: width %d is below MinColumnWidth %d — the floor should not bind here",
				i, b.Widths[i], MinColumnWidth)
		}
		// The clamp removes a few cells from the slackiest column afterwards,
		// so the upper bound carries a cell of slack rather than being exact.
		if float64(b.Widths[i]) > share+1 {
			t.Errorf("column %d: width %d exceeds its prorated share %.1f (raw %d of %d)",
				i, b.Widths[i], share, raw[i], sumRaw)
		}
	}
	// The defining property: a column that asked for three times as much still
	// gets about three times as much. A greedy allocation hands column 0
	// everything and drops column 1 to its floor, and this assertion is what
	// distinguishes the two.
	ratio := float64(b.Widths[0]) / float64(b.Widths[1])
	if ratio < 2.5 || ratio > 3.5 {
		t.Errorf("allocated ratio = %.2f, want ~3.00 (raw ratio 3.00, got widths %v)", ratio, b.Widths)
	}
	if !b.Fits(viewport) {
		t.Errorf("prorated allocation %v does not fit %d (outer %d)", b.Widths, viewport, b.OuterWidth())
	}
}

// TestBudgetTableFloorFlattensColumnRatios documents the one place the floor
// destroys proportionality, because it is a real and deliberate trade-off rather
// than an accident. When a column's prorated share falls below MinColumnWidth
// it is raised to the floor, so two columns with very different intrinsic
// widths can end up EQUAL. The alternative — honouring the ratio — produces a
// column of one-character lines, which is worse than two equal readable
// columns. A reader would rather have two equal columns than one legible one.
func TestBudgetTableFloorFlattensColumnRatios(t *testing.T) {
	// One enormous column against several tiny ones: the tiny ones' shares are
	// far below the floor and all get raised to it.
	raw := []int{400, 6, 5, 4, 3}
	b := BudgetTable(raw, 80)
	if b.Widths[1] != b.Widths[2] || b.Widths[2] != b.Widths[3] {
		t.Errorf("narrow columns %v did not converge on the floor; ratio preservation "+
			"cannot hold once the floor binds", b.Widths[1:])
	}
	if b.Widths[0] <= b.Widths[1] {
		t.Errorf("the verbose column (%d) must still be the widest, got %v", raw[0], b.Widths)
	}
}

// TestBudgetTableFloorsNarrowColumns: a column that lands below MinColumnWidth
// is floored, because a column of single characters cannot hold a word. The
// floor is why an allocation can legitimately exceed the budget, and the caller
// has to be able to detect that rather than discover it as a torn frame.
func TestBudgetTableFloorsNarrowColumns(t *testing.T) {
	// One enormous column and four tiny ones: the tiny ones would each receive
	// well under MinColumnWidth of the shared budget.
	raw := []int{400, 2, 2, 2, 2}
	b := BudgetTable(raw, 80)
	for i := 1; i < len(raw); i++ {
		if b.Widths[i] < MinColumnWidth {
			t.Errorf("column %d: width %d is below the %d-cell floor", i, b.Widths[i], MinColumnWidth)
		}
	}
	if b.Floor != MinColumnWidth {
		t.Errorf("Floor = %d, want %d (a squeezed table floors at MinColumnWidth)", b.Floor, MinColumnWidth)
	}
}

// TestBudgetTableFitsDownToFortyColumns is the DoD: a table stays framed and
// bounded at a 40-cell split-pane. It is swept over the realistic column counts
// and asserted through the SAME accessor the renderer consults, so the test
// cannot pass while the grid overflows.
//
// The maximum column count is itself part of the contract, so it is asserted
// rather than assumed: at 40 cells, MinColumnWidth(10) × n + FrameChrome(3n+1)
// fits up to n = 3. Past that the budgeter reports Fits() == false and the
// renderer switches to the stacked listing — see
// TestFitsReportsHonestFailure for why that boundary is reported rather than
// papered over.
func TestBudgetTableFitsDownToFortyColumns(t *testing.T) {
	cases := []struct {
		name     string
		raw      []int
		maxCols  int // the widest viewport this shape is guaranteed to frame
		viewport int
	}{
		{"two columns", []int{12, 40}, 2, 40},
		{"three columns", []int{9, 44, 10}, 3, 40},
		{"three equal columns", []int{20, 20, 20}, 3, 40},
		{"five columns", []int{8, 30, 12, 9, 25}, 5, 80},
		{"six columns", []int{6, 6, 6, 6, 6, 6}, 6, 80},
		{"ragged", []int{3, 60, 7, 15, 4, 22}, 6, 80},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, vw := range []int{40, 48, 60, 80, 120, 200} {
				if vw < c.viewport {
					continue // below the shape's guaranteed minimum: see below
				}
				b := BudgetTable(c.raw, vw)
				if !b.Fits(vw) {
					t.Errorf("viewport %d: allocation %v needs %d cells (content %d + chrome %d); "+
						"the renderer would have to tear the frame",
						vw, b.Widths, b.OuterWidth(), b.Total(), b.Chrome)
				}
				if b.OuterWidth() > vw && b.Floor == 1 {
					t.Errorf("viewport %d: OuterWidth %d exceeds the viewport but Fits() reported true",
						vw, b.OuterWidth())
				}
			}
			// The stated guarantee, checked directly: this shape IS framed at
			// its guaranteed minimum width.
			b := BudgetTable(c.raw, c.viewport)
			if !b.Fits(c.viewport) {
				t.Errorf("at its guaranteed minimum %d cells, %d columns do not fit (outer %d)",
					c.viewport, c.maxCols, b.OuterWidth())
			}
		})
	}
}

// TestMaxFramedColumnsAtFortyCells states the boundary as a number. Three
// columns is the widest grid a 40-cell split-pane can frame at a readable
// column width; a fourth genuinely cannot be, and knowing the exact limit is
// what lets a caller choose a fallback deliberately.
func TestMaxFramedColumnsAtFortyCells(t *testing.T) {
	const viewport = 40
	lastFitting := 0
	for cols := 1; cols <= 8; cols++ {
		raw := make([]int, cols)
		for i := range raw {
			raw[i] = 30 // every column wants more than its share
		}
		if BudgetTable(raw, viewport).Fits(viewport) {
			lastFitting = cols
		}
	}
	if lastFitting != 3 {
		t.Errorf("at %d cells the widest framable grid is %d columns, want 3", viewport, lastFitting)
	}
}

// TestFitsReportsHonestFailure: when the per-column floors alone exceed the
// budget, NO allocation can be both readable and bounded. Fits must say so, so
// the caller degrades on purpose instead of drawing a broken frame.
func TestFitsReportsHonestFailure(t *testing.T) {
	// Ten columns at 40 cells: 10 × MinColumnWidth + chrome cannot fit.
	raw := make([]int, 10)
	for i := range raw {
		raw[i] = 30
	}
	b := BudgetTable(raw, 40)
	if b.Fits(40) {
		t.Errorf("ten columns at 40 cells reported Fits() == true (Widths %v, outer %d)",
			b.Widths, b.OuterWidth())
	}
	if b.Floor != MinColumnWidth {
		t.Errorf("Floor = %d, want %d (a squeezed table floors at MinColumnWidth)", b.Floor, MinColumnWidth)
	}
	// The allocation is still returned and still bounded per column — the
	// caller is expected to choose a different SHAPE, not to receive garbage.
	for i, w := range b.Widths {
		if w < MinColumnWidth {
			t.Errorf("column %d: width %d fell below the floor even in the unfittable case", i, w)
		}
	}
}

// TestUnsqueezedTableKeepsTinyColumns: when a table's demand already fits, the
// floor is NOT applied. A genuinely two-cell column in a table that fits must
// stay two cells — raising it to ten would be the budgeter inventing width the
// table never used, and would make a small table overflow a pane it fits in.
func TestUnsqueezedTableKeepsTinyColumns(t *testing.T) {
	b := BudgetTable([]int{2, 3, 4}, 80)
	if b.Floor != 1 {
		t.Errorf("Floor = %d, want 1 for a table that fits", b.Floor)
	}
	if got, want := b.Widths, []int{2, 3, 4}; got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("Widths = %v, want %v (an unsqueezed table keeps its natural widths)", got, want)
	}
}

// TestBudgetTableHandlesDegenerateInput: a renderer must survive a degenerate
// parse result, because crashing on malformed model output is strictly worse
// than rendering it plainly.
func TestBudgetTableHandlesDegenerateInput(t *testing.T) {
	t.Run("no columns", func(t *testing.T) {
		b := BudgetTable(nil, 80)
		if len(b.Widths) != 0 {
			t.Errorf("Widths = %v, want empty", b.Widths)
		}
		if b.Total() != 0 || b.OuterWidth() != 0 {
			t.Errorf("Total/OuterWidth = %d/%d, want 0/0", b.Total(), b.OuterWidth())
		}
		if !b.Fits(80) {
			t.Error("an empty table must trivially fit")
		}
	})
	t.Run("zero and negative measurements", func(t *testing.T) {
		b := BudgetTable([]int{0, -5, 12}, 40)
		for i, w := range b.Widths {
			if w < 1 {
				t.Errorf("column %d: width %d, want >= 1", i, w)
			}
		}
	})
	t.Run("absurdly narrow viewport", func(t *testing.T) {
		b := BudgetTable([]int{40, 40}, 4)
		if !b.Fits(4) && b.Floor == 1 {
			t.Errorf("a 4-cell viewport produced an unshrinkable allocation %v", b.Widths)
		}
	})
}

// TestBudgetTableDoesNotAliasInput: the budgeter runs on every streaming tick
// with the caller's measurement slice. Aliasing it would mean a later mutation
// of the caller's slice silently rewrites a budget that was already handed to a
// renderer.
func TestBudgetTableDoesNotAliasInput(t *testing.T) {
	raw := []int{10, 20, 30}
	b := BudgetTable(raw, 60)
	raw[0] = 999
	if b.Raw[0] != 10 {
		t.Errorf("Raw aliased the caller's slice: Raw[0] = %d after mutating the input to 999", b.Raw[0])
	}
}

// TestClampTakesFromTheSlackiestColumn pins the clamp's ordering rule. Shrinking
// "the first column" would sacrifice whichever column the author happened to
// write first; shrinking the column with the most slack above its floor
// sacrifices the one that needed its width least. Same number of cells removed,
// materially different table.
func TestClampTakesFromTheSlackiestColumn(t *testing.T) {
	b := TableBudget{
		Widths: []int{30, 12, 20},
		Raw:    []int{30, 12, 20},
		Floor:  10,
		Chrome: FrameChrome(3),
	}
	// Reduce by 25: more than the slackiest column alone can give (20), so the
	// reduction must spill to the next-slackiest — and must never reach the
	// tightest column while either of the others still has room.
	target := b.OuterWidth() - 25
	if !b.ClampToFrame(target) {
		t.Fatalf("clamp reported failure; widths %v", b.Widths)
	}
	if got := b.OuterWidth(); got != target {
		t.Errorf("OuterWidth = %d, want exactly %d (the clamp must land on the bound, not short of it)", got, target)
	}
	// Column 0 had 20 cells of slack and gives up all of them; column 2 had 10
	// and gives up the remaining 5; column 1 had 2 and is never touched, because
	// it needed its width most.
	if b.Widths[0] != 10 {
		t.Errorf("slackiest column = %d, want 10 (gave up its full 20-cell slack first)", b.Widths[0])
	}
	if b.Widths[2] != 15 {
		t.Errorf("next-slackiest column = %d, want 15 (absorbed the remaining 5)", b.Widths[2])
	}
	if b.Widths[1] != 12 {
		t.Errorf("tightest column = %d, want 12 (untouched: least slack of the three)", b.Widths[1])
	}
}

// TestClampRespectsFloorsAndReportsFailure: the clamp must never cross a floor,
// and when every column is at its floor it must REPORT that rather than either
// looping forever or silently returning success.
func TestClampRespectsFloorsAndReportsFailure(t *testing.T) {
	b := TableBudget{
		Widths: []int{10, 10},
		Raw:    []int{10, 10},
		Floor:  10,
		Chrome: FrameChrome(2),
	}
	if b.ClampToFrame(1) {
		t.Error("clamp reported success with every column at its floor and an impossible target")
	}
	for i, w := range b.Widths {
		if w < 10 {
			t.Errorf("column %d: clamp crossed the floor to %d", i, w)
		}
	}
}

// TestClampIsANoOpWhenAlreadyFitted: the clamp runs on every tick, so the
// "nothing to do" path must leave the allocation byte-identical rather than
// perturbing it by a rounding cell.
func TestClampIsANoOpWhenAlreadyFitted(t *testing.T) {
	b := TableBudget{Widths: []int{20, 15}, Raw: []int{20, 15}, Floor: 1, Chrome: FrameChrome(2)}
	before := append([]int(nil), b.Widths...)
	if !b.ClampToFrame(200) {
		t.Error("clamp reported failure on an already-fitting allocation")
	}
	for i := range before {
		if b.Widths[i] != before[i] {
			t.Errorf("column %d changed from %d to %d on a no-op clamp", i, before[i], b.Widths[i])
		}
	}
}

// TestClampToleratesNilReceiver: ClampToFrame has a pointer receiver and is
// called from budget-building paths that may hold no budget at all.
func TestClampToleratesNilReceiver(t *testing.T) {
	var b *TableBudget
	if !b.ClampToFrame(80) {
		t.Error("a nil budget has nothing to clamp and must report success")
	}
	empty := &TableBudget{}
	if !empty.ClampToFrame(80) {
		t.Error("an empty budget has nothing to clamp and must report success")
	}
}

// TestCellStyleCarriesWrapIntent documents the lipgloss-v1 shim. Under v1 the
// non-zero Width wraps unconditionally and Wrap is a declaration; the test
// asserts the behaviour that matters (the content does not exceed the width)
// rather than the method's presence, so it survives the v2 migration unchanged.
func TestCellStyleCarriesWrapIntent(t *testing.T) {
	const width = 20
	long := "a very long piece of cell content that will not fit on one line at all"
	lines := splitLines(Cell{}.Width(width).Wrap(true).Render(long))
	if len(lines) < 2 {
		t.Fatalf("expected the cell to wrap onto multiple lines, got %d", len(lines))
	}
	for i, l := range lines {
		if w := cellWidth(l); w > width {
			t.Errorf("line %d is %d cells, want <= %d: %q", i, w, width, l)
		}
	}
}

// TestCellWidthFloorsAtLeastOneLine: a zero or negative width would make
// lipgloss wrap every rune, turning one cell into dozens of rows.
func TestCellWidthFloorsAtLeastOneLine(t *testing.T) {
	for _, w := range []int{0, -4} {
		out := Cell{}.Width(w).Wrap(true).Render("hi")
		if out == "" {
			t.Errorf("Width(%d) produced empty output", w)
		}
	}
}
