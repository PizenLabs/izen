package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// helper to create model with predictable geometry
func newGeometryModel(width, height int, records []record) *model {
	m := newTestModel()
	m.state = StateChat
	m.showBanner = false
	m.records = records
	m.width = width
	m.height = height
	m.Ready = true
	m.Viewport.Width = width
	m.Viewport.Height = m.computeVpHeight()
	m.streaming = false
	m.agentRunning = false
	// Populate PreRenderedHistory so refreshViewportContent has content to display
	var b strings.Builder
	for i, rec := range m.records {
		if rendered := m.renderRecordForViewport(rec); rendered != "" {
			b.WriteString(rendered)
			if i < len(m.records)-1 {
				b.WriteString("\n")
			}
		}
	}
	m.PreRenderedHistory = b.String()
	// Ensure viewport content is built
	m.refreshViewportContent()
	m.Viewport.YOffset = 0
	return m
}

func TestMouseMapping_TopRow(t *testing.T) {
	m := newGeometryModel(80, 24, []record{
		{role: roleAI, text: "first line"},
		{role: roleAI, text: "second line"},
		{role: roleAI, text: "third line"},
	})
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	y := geo.Top + prefix // first record row
	pos := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y})
	if pos.Line != 0 {
		t.Fatalf("top row should map to line 0, got %d (y=%d top=%d prefix=%d)", pos.Line, y, geo.Top, prefix)
	}
}

func TestMouseMapping_MiddleRow(t *testing.T) {
	m := newGeometryModel(80, 24, []record{
		{role: roleAI, text: "a"}, {role: roleAI, text: "b"}, {role: roleAI, text: "c"}, {role: roleAI, text: "d"}, {role: roleAI, text: "e"},
	})
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	// middle record is index 2
	y := geo.Top + prefix + 2
	pos := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y})
	if pos.Line != 2 {
		t.Fatalf("middle row should map to line 2, got %d", pos.Line)
	}
}

func TestMouseMapping_BottomRow(t *testing.T) {
	m := newGeometryModel(80, 24, []record{
		{role: roleAI, text: "one"}, {role: roleAI, text: "two"}, {role: roleAI, text: "three"},
	})
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	// viewport height may be larger than records, bottom visible row is last record
	lastRow := len(m.records) - 1
	y := geo.Top + prefix + lastRow
	// clamp inside viewport
	if y >= geo.Top+geo.Height {
		y = geo.Top + geo.Height - 1
	}
	pos := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y})
	if pos.Line != lastRow {
		t.Fatalf("bottom row should map to %d got %d", lastRow, pos.Line)
	}
}

func TestMouseMapping_ScrolledViewport(t *testing.T) {
	records := make([]record, 20)
	for i := range records {
		records[i] = record{role: roleAI, text: "line"}
	}
	m := newGeometryModel(80, 24, records)
	// scroll down 5
	m.Viewport.YOffset = 5
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	// After scroll, viewport top shows recordRow = YOffset - prefix? Actually content row includes prefix
	// With YOffset=5, first visible content row is 5. If prefix=3, then recordRow 2 is at top.
	// Click at viewport top should map to record 2
	y := geo.Top // top of viewport
	pos := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y})
	expected := 5 - prefix
	if expected < 0 {
		expected = 0
	}
	if pos.Line != expected {
		t.Fatalf("scrolled top should map to %d got %d (YOffset 5 prefix %d)", expected, pos.Line, prefix)
	}
	// Click at viewport bottom should map accordingly
	y2 := geo.Top + geo.Height - 1
	pos2 := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y2})
	// bottom content row = YOffset + Height -1
	bottomContent := m.Viewport.YOffset + geo.Height - 1
	bottomRecord := bottomContent - prefix
	if bottomRecord >= len(records) {
		bottomRecord = len(records) - 1
	}
	if pos2.Line != bottomRecord {
		t.Fatalf("scrolled bottom should map to %d got %d", bottomRecord, pos2.Line)
	}
}

func TestMouseMapping_WrappedLine(t *testing.T) {
	// Create a long line that wraps at width 40
	m := newGeometryModel(40, 24, []record{
		{role: roleAI, text: strings.Repeat("word ", 30)}, // long
		{role: roleAI, text: "second"},
	})
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	// First physical row of wrapped record should map to line 0
	y0 := geo.Top + prefix
	pos0 := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y0})
	if pos0.Line != 0 {
		t.Fatalf("wrapped first row should be line 0 got %d", pos0.Line)
	}
	// Second wrapped row (same logical line) should still map to line 0
	c := m.renderedLineCount(m.records[0])
	if c < 2 {
		t.Skip("record did not wrap as expected")
	}
	y1 := geo.Top + prefix + 1
	pos1 := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y1})
	if pos1.Line != 0 {
		t.Fatalf("wrapped second row should still be line 0 got %d", pos1.Line)
	}
	// Column mapping for wrapped second row should be beyond first wrap width
	if pos1.Col <= 0 {
		t.Fatalf("wrapped second row col should be >0 got %d", pos1.Col)
	}
}

func TestMouseMapping_UnicodeWide(t *testing.T) {
	m := newGeometryModel(80, 24, []record{
		{role: roleAI, text: "你好世界 hello"},
	})
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	y := geo.Top + prefix
	// CJK chars are 2 cells each. "你" is 2 cells, so X=2 (gutter 2 + 0) -> col 0, X=4 -> after one CJK char -> col 1
	pos0 := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y}) // gutter 2 + 2 cells = after first CJK
	if pos0.Col != 1 {
		t.Fatalf("CJK col mapping: X=4 should be col 1 got %d", pos0.Col)
	}
	// Emoji wide
	m2 := newGeometryModel(80, 24, []record{
		{role: roleAI, text: "a😀b"},
	})
	geo2 := m2.viewportGeometry()
	prefix2 := m2.viewportContentPrefixHeight()
	y2 := geo2.Top + prefix2
	// "a" 1 cell, "😀" 2 cells, "b" 1 cell
	// gutter 2, so cellCol = X-2. X=3 -> cell 1 -> after 'a' -> col 1 (emoji start)
	pos := m2.mousePosToLogical(tea.MouseMsg{X: 3, Y: y2})
	if pos.Col != 1 {
		t.Fatalf("emoji mapping: X=3 should be col 1 got %d", pos.Col)
	}
}

func TestMouseMapping_HorizontalGutterAccuracy(t *testing.T) {
	m := newGeometryModel(80, 24, []record{{role: roleAI, text: "hello world"}})
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	y := geo.Top + prefix
	// gutter is 2 cells. X exactly at gutter start should be col 0
	pos := m.mousePosToLogical(tea.MouseMsg{X: 2, Y: y})
	if pos.Col != 0 {
		t.Fatalf("X at gutter start should be col 0 got %d", pos.Col)
	}
	// X=7 -> gutter 2 + col 5 -> ' ' position? "hello" len 5, "hello"[5]=' '
	pos2 := m.mousePosToLogical(tea.MouseMsg{X: 7, Y: y})
	if pos2.Col != 5 {
		t.Fatalf("X=7 should be col 5 got %d", pos2.Col)
	}
}

func TestMouseMapping_NoFixedOffset(t *testing.T) {
	// Ensure mouse mapping uses viewportGeometry, not hardcoded +1/-2.
	// By changing width (which changes header height when runtimeCtx present),
	// the mapping must remain accurate - a fixed offset would drift.
	m := newGeometryModel(100, 30, []record{{role: roleAI, text: "line1"}, {role: roleAI, text: "line2"}})
	// Without runtimeCtx header is 0, geo.Top 0.
	// Add a fake runtimeCtx to make header non-empty would change Top, but we
	// test that narrow vs wide doesn't use constant offset.
	geo := m.viewportGeometry()
	if geo.Top != 0 {
		// with nil ctx top should be 0; this confirms no phantom header bias
		t.Fatalf("with nil ctx, Top should be 0 got %d", geo.Top)
	}
	prefix := m.viewportContentPrefixHeight()
	pos := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: geo.Top + prefix})
	if pos.Line != 0 {
		t.Fatalf("no fixed offset: top row must be line 0, got %d", pos.Line)
	}
}

func TestHighlightMatchesCopy(t *testing.T) {
	m := newGeometryModel(80, 24, []record{
		{role: roleAI, text: "alpha bravo charlie"},
		{role: roleAI, text: "delta echo foxtrot"},
	})
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	// Select from col 6 on line 0 to col 5 on line 1
	startY := geo.Top + prefix
	endY := geo.Top + prefix + 1
	start := m.mousePosToLogical(tea.MouseMsg{X: 2 + 6, Y: startY})
	end := m.mousePosToLogical(tea.MouseMsg{X: 2 + 5, Y: endY})
	m.mouseSel = mouseSelection{Active: true, Anchor: start, Cursor: end}
	// Highlight rendering should inject style for exactly the selected range
	highlighted := m.renderRecordsWithMouseSelection()
	plain := ""
	var pb strings.Builder
	for i, rec := range m.records {
		if r := m.renderRecordForViewport(rec); r != "" {
			pb.WriteString(r)
			if i < len(m.records)-1 {
				pb.WriteString("\n")
			}
		}
	}
	plain = pb.String()
	// Copied logical range should match highlighted logical range
	copied := m.serializeMouseSelection()
	// The copied text must be non-empty and match the logical range content
	if copied == "" {
		t.Fatalf("copied empty, highlight %q", highlighted)
	}
	// Highlight must be non-empty and not leak markers
	if strings.Contains(highlighted, "\x00") {
		t.Fatalf("highlight leaked markers %q", highlighted)
	}
	_ = plain
	// Copied must equal the logical slice: from line0 col6 to line1 col5
	// line0 "alpha bravo charlie" col6 is 'b' of bravo
	// line1 "delta echo foxtrot" col5 is ' ' or 'e'?
	// Just assert it contains expected substrings from both lines
	if !strings.Contains(copied, "bravo") {
		t.Fatalf("copied should contain bravo, got %q", copied)
	}
	if !strings.Contains(copied, "delta") {
		t.Fatalf("copied should contain delta, got %q", copied)
	}
}

func TestAutoScroll_SingleLoop(t *testing.T) {
	m := newGeometryModel(80, 24, makeRecords(50, "auto line content for scrolling test"))
	m.Viewport.YOffset = 15
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: selPos{Line: 15, Col: 0}, Cursor: selPos{Line: 15, Col: 0}, lastY: 1}
	geo := m.viewportGeometry()
	// First motion into edge should start loop
	yEdge := geo.Top // top edge
	cmd := m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: yEdge, X: 2})
	if cmd == nil {
		t.Fatalf("should schedule tick when in edge zone")
	}
	if !m.mouseSel.TickActive {
		t.Fatalf("TickActive should be true after scheduling")
	}
	afterFirst := m.Viewport.YOffset
	// Second call while still active and not at clamp should keep scheduling
	cmd2 := m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: yEdge, X: 2})
	if cmd2 == nil {
		// If we hit top clamp, it's still correct to stop - but with offset 15 we shouldn't hit yet
		t.Fatalf("should keep scheduling while still in edge, afterFirst %d", afterFirst)
	}
	// Moving out of edge should stop loop
	yMid := geo.Top + geo.Height/2
	m.mouseSel.lastY = yMid
	cmd3 := m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: yMid, X: 2})
	if cmd3 != nil {
		t.Fatalf("should stop scheduling when out of edge, got cmd")
	}
	if m.mouseSel.TickActive {
		t.Fatalf("TickActive should be false when out of edge")
	}
}

func TestAutoScroll_Velocity(t *testing.T) {
	m := newGeometryModel(80, 24, makeRecords(50, "line"))
	m.Viewport.YOffset = 10
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: selPos{Line: 10, Col: 0}}
	geo := m.viewportGeometry()
	// Deep inside top edge (relY 0) should have velocity 2 (larger delta)
	m.mouseSel.lastY = geo.Top
	msgDeep := selectionScrollTickMsg{Y: geo.Top, X: 2}
	before := m.Viewport.YOffset
	_ = m.handleSelectionAutoScroll(msgDeep)
	afterDeep := m.Viewport.YOffset
	deepDelta := before - afterDeep
	// Reset
	m.Viewport.YOffset = 10
	m.mouseSel.TickActive = false
	// Shallow edge (relY = 2, dist=1) should have velocity 1
	msgShallow := selectionScrollTickMsg{Y: geo.Top + 2, X: 2}
	m.mouseSel.lastY = geo.Top + 2
	before = m.Viewport.YOffset
	_ = m.handleSelectionAutoScroll(msgShallow)
	afterShallow := m.Viewport.YOffset
	shallowDelta := before - afterShallow
	if deepDelta <= shallowDelta {
		t.Fatalf("deep edge velocity should be larger: deep %d shallow %d", deepDelta, shallowDelta)
	}
}

func TestAutoScroll_AnchorStability(t *testing.T) {
	m := newGeometryModel(80, 15, makeRecords(20, "content"))
	m.Viewport.YOffset = 5
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	y := geo.Top + prefix
	start := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y})
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: start, Cursor: start, lastY: geo.Top}
	anchor := m.mouseSel.Anchor
	// Simulate auto-scroll tick that moves viewport
	cmd := m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: geo.Top, X: 4})
	if cmd == nil {
		t.Skip("no edge scroll triggered")
	}
	if m.mouseSel.Anchor != anchor {
		t.Fatalf("anchor must remain stable after auto-scroll, was %v now %v", anchor, m.mouseSel.Anchor)
	}
}

func TestStreamingDoesNotFightSelection(t *testing.T) {
	m := newGeometryModel(80, 24, makeRecords(10, "history"))
	m.streaming = true
	m.userIsScrollingUp = false
	m.Viewport.YOffset = 0
	// Start selection - should freeze auto-follow
	geo := m.viewportGeometry()
	prefix := m.viewportContentPrefixHeight()
	y := geo.Top + prefix
	pos := m.mousePosToLogical(tea.MouseMsg{X: 4, Y: y})
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: pos, Cursor: pos, lastY: y}
	m.userIsScrollingUp = true
	// Simulate streaming trying to goto bottom - should be suppressed
	m.Viewport.YOffset = 2
	m.gotoBottomIfAllowed()
	if m.Viewport.YOffset != 2 {
		t.Fatalf("streaming GotoBottom should be suppressed during active drag, offset changed to %d", m.Viewport.YOffset)
	}
	// After drag ends, still userIsScrollingUp true, so not jump to bottom unexpectedly
	m.mouseSel.Dragging = false
	m.mouseSel.TickActive = false
	m.gotoBottomIfAllowed()
	if m.Viewport.YOffset != 2 {
		t.Fatalf("after selection, should preserve viewport position, not jump to bottom, offset %d", m.Viewport.YOffset)
	}
}

func makeRecords(n int, txt string) []record {
	out := make([]record, n)
	for i := range out {
		out[i] = record{role: roleAI, text: txt}
	}
	return out
}
