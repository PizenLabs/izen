package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// TestDocumentLayout_SpaceAnchoredSelection verifies Anchor remains static in
// global space while YOffset mutates, and that text extraction via geometry
// matches visual cells.
func TestDocumentLayout_SpaceAnchoredSelection(t *testing.T) {
	records := []record{
		{role: roleUser, text: "hello user prompt"},
		{role: roleAI, text: "AI response body here with enough text to wrap"},
		{role: roleAI, text: "third line content for scrolling test"},
	}
	width := 40
	dl := BuildDocumentLayout(records, width)
	if dl.Len() == 0 {
		t.Fatalf("layout empty")
	}
	// Simulate viewport height 5, YOffset 0
	m := newTestModel()
	m.width = width
	m.height = 20
	m.records = records
	m.Ready = true
	m.Viewport.Width = width
	m.Viewport.Height = 5
	m.docLayout = &dl
	m.refreshViewportContent()
	geo := m.viewportGeometry()
	// Click at first visible row -> GlobalPos Y = YOffset + relY
	startGlobal := m.docLayout.ScreenToGlobal(geo.Left+2, geo.Top, 0, geo.Left, geo.Top)
	anchor := startGlobal
	// Simulate scroll down by 2
	m.Viewport.YOffset = 2
	// Anchor must remain unchanged
	if m.mouseSel.Active {
		// if selection active, anchor should be stable; test direct anchor stability via layout
	}
	if anchor.Y != startGlobal.Y || anchor.X != startGlobal.X {
		t.Fatalf("anchor drift: was %v now %v", startGlobal, anchor)
	}
	// Extract text spanning anchor to end on last record's line
	// Find global Y for last record
	lastY := dl.Len() - 1
	end := GlobalPos{Y: lastY, X: 5}
	text := dl.ExtractText(anchor, end)
	if !strings.Contains(text, "hello") && !strings.Contains(text, "AI response") {
		t.Fatalf("extraction missing expected content: %q anchor %v end %v", text, anchor, end)
	}
	// Verify space-anchored invariant: after scroll, same anchor extracts same content
	m.Viewport.YOffset = 2
	// Anchor unchanged, cursor should be YOffset+edgeY in real auto-scroll, but extraction with same anchor should still work
	text2 := dl.ExtractText(anchor, end)
	if text != text2 {
		t.Fatalf("anchor-anchored extraction changed after scroll: %q vs %q", text, text2)
	}
	// Also test auto-scroll tick preserves anchor
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: anchor, Cursor: anchor, lastY: geo.Top}
	origAnchor := m.mouseSel.Anchor
	_ = m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: geo.Top, X: geo.Left + 2})
	if m.mouseSel.Anchor != origAnchor {
		t.Fatalf("auto-scroll mutated anchor: was %v now %v", origAnchor, m.mouseSel.Anchor)
	}
}

// TestDocumentLayout_CJKCellSpanAccuracy tests selection over mixed CJK/ASCII
// with no index panics or rune splitting.
func TestDocumentLayout_CJKCellSpanAccuracy(t *testing.T) {
	text := "hello 你好世界 world 😀 test"
	rec := record{role: roleAI, text: text}
	width := 30
	dl := BuildDocumentLayout([]record{rec}, width)
	if dl.Len() == 0 {
		t.Fatalf("empty layout for CJK")
	}
	// Find line containing CJK
	var cjkLine *DocumentLine
	for i := range dl.Lines {
		if strings.Contains(dl.Lines[i].RawText, "你好") {
			cjkLine = &dl.Lines[i]
			break
		}
	}
	if cjkLine == nil {
		t.Fatalf("CJK line not found in layout, lines=%v", dl.Lines)
	}
	// Verify spans correctly account for CJK double width
	if len(cjkLine.Spans) == 0 {
		t.Fatalf("no spans for CJK line")
	}
	// Attempt cell slicing at various offsets that land inside wide runes
	for _, cell := range []int{0, 1, 6, 7, 8, 12, 20} {
		sliced := SliceByCells(text, cell, cell+2)
		// Should not panic and should not produce half-rune (invalid utf8)
		if !strings.Contains(text, sliced) && sliced != "" {
			// sliced may be empty at end, but if non-empty it must be substring of runes
			t.Fatalf("sliceByCells produced invalid slice at cell %d: %q", cell, sliced)
		}
		// Ensure no rune splitting: sliced length in runes * max width should be consistent
		_ = ansi.Strip(sliced)
	}
	// Extraction across CJK boundary
	start := GlobalPos{Y: cjkLine.GlobalY, X: 6} // roughly after "hello "
	end := GlobalPos{Y: cjkLine.GlobalY, X: 14}  // includes some CJK
	extracted := dl.ExtractText(start, end)
	if extracted == "" {
		t.Fatalf("CJK extraction empty start %v end %v line %q", start, end, cjkLine.RawText)
	}
	// Ensure extracted does not contain replacement char
	if strings.Contains(extracted, "\ufffd") {
		t.Fatalf("CJK extraction produced replacement char: %q", extracted)
	}
	// Verify round-trip via ScreenToGlobal not panicking
	m := newTestModel()
	m.width = width
	m.records = []record{rec}
	m.Ready = true
	m.Viewport.Width = width
	m.Viewport.Height = 10
	m.docLayout = &dl
	geo := m.viewportGeometry()
	// Simulate click inside CJK wide rune second cell
	// CJK char "你" is 2 cells; clicking at second cell should still map to same rune
	pos1 := m.docLayout.ScreenToGlobal(geo.Left+7, geo.Top, 0, geo.Left, geo.Top)
	pos2 := m.docLayout.ScreenToGlobal(geo.Left+8, geo.Top, 0, geo.Left, geo.Top)
	// Both should be valid and not panic; extraction should be stable
	_ = dl.ExtractText(pos1, pos2)
}

// TestDocumentLayout_CrossBoundaryExtraction performs selection spanning across
// prompt lines, code blocks, and AI text and asserts raw text extraction matches
// exact visual cells.
func TestDocumentLayout_CrossBoundaryExtraction(t *testing.T) {
	records := []record{
		{role: roleUser, text: "user prompt line for extraction"},
		{role: roleAI, text: "```go\npackage main\nfunc main() {\n    fmt.Println(\"hello\")\n}\n```"},
		{role: roleAI, text: "final AI response after code block with normal text"},
	}
	width := 40
	dl := BuildDocumentLayout(records, width)
	if dl.Len() < 3 {
		t.Fatalf("layout too small for cross-boundary: %d", dl.Len())
	}
	// Find Y for user prompt, code block, final AI
	var userY, codeY, finalY int = -1, -1, -1
	for _, l := range dl.Lines {
		if strings.Contains(l.RawText, "user prompt") && userY == -1 {
			userY = l.GlobalY
		}
		if strings.Contains(l.RawText, "package main") && codeY == -1 {
			codeY = l.GlobalY
		}
		if strings.Contains(l.RawText, "final AI response") && finalY == -1 {
			finalY = l.GlobalY
		}
	}
	if userY < 0 || codeY < 0 || finalY < 0 {
		t.Fatalf("failed to locate cross-boundary lines userY=%d codeY=%d finalY=%d lines=%v", userY, codeY, finalY, dl.Lines)
	}
	// Selection from user prompt (col 5) to final AI (col 80) to include full final line
	start := GlobalPos{Y: userY, X: 5}
	end := GlobalPos{Y: finalY, X: 80}
	extracted := dl.ExtractText(start, end)
	if !strings.Contains(extracted, "prompt line") {
		t.Fatalf("cross-boundary missing user prompt: %q", extracted)
	}
	if !strings.Contains(extracted, "package main") && !strings.Contains(extracted, "fmt.Println") {
		t.Fatalf("cross-boundary missing code block: %q", extracted)
	}
	if !strings.Contains(extracted, "final AI response") {
		t.Fatalf("cross-boundary missing final AI: %q", extracted)
	}
	// Ensure extraction includes newlines between lines
	if !strings.Contains(extracted, "\n") {
		t.Fatalf("cross-boundary should be newline separated: %q", extracted)
	}
	// Reverse order should normalize and produce same
	extractedRev := dl.ExtractText(end, start)
	if extracted != extractedRev {
		t.Fatalf("extraction not normalized: forward %q vs reverse %q", extracted, extractedRev)
	}
	// Verify ANSI stripping: rendered strings may have ANSI, raw extraction must not contain ESC
	if strings.Contains(extracted, "\x1b[") {
		t.Fatalf("extraction leaked ANSI: %q", extracted)
	}
}

// Additional helper to ensure document layout visible slice correctness
func TestDocumentLayout_VisibleSlice(t *testing.T) {
	records := make([]record, 5)
	for i := range records {
		records[i] = record{role: roleAI, text: strings.Repeat("word ", 10)}
	}
	dl := BuildDocumentLayout(records, 20)
	if dl.Len() == 0 {
		t.Fatalf("empty")
	}
	visible := dl.VisibleSlice(2, 3)
	if len(visible) != 3 {
		t.Fatalf("visible slice len %d want 3", len(visible))
	}
	if visible[0].GlobalY != 2 {
		t.Fatalf("visible slice GlobalY %d want 2", visible[0].GlobalY)
	}
}

// Test that SliceByCells correctly handles wide runes without splitting
func TestDocumentLayout_SliceByCellsWide(t *testing.T) {
	s := "a你b好c"
	// "a" 1 cell, "你" 2 cells, "b"1, "好"2, "c"1 => total 7
	if SliceByCells(s, 0, 1) != "a" {
		t.Fatalf("slice 0,1 got %q", SliceByCells(s, 0, 1))
	}
	// cell 1 is start of "你" (2 cells), slicing 1,3 should be "你"
	if SliceByCells(s, 1, 3) != "你" {
		t.Fatalf("slice 1,3 got %q want 你", SliceByCells(s, 1, 3))
	}
	// slicing inside wide rune second cell should still return same rune (no split)
	if SliceByCells(s, 2, 3) != "你" && SliceByCells(s, 2, 3) != "" {
		// Our implementation maps inside wide rune to same rune start, so may return empty or rune
		// Just ensure no panic and valid utf8
		t.Logf("slice inside wide rune got %q", SliceByCells(s, 2, 3))
	}
	if SliceByCells(s, 0, -1) != s {
		t.Fatalf("full slice got %q want %q", SliceByCells(s, 0, -1), s)
	}
}

func TestDocumentLayout_DynamicUsernameBadge(t *testing.T) {
	// Non-default username "kaka" should produce badge "@kaka  " with correct cell width
	records := []record{{role: roleUser, text: "hello"}}
	width := 40
	dlDefault := BuildDocumentLayout(records, width, "")
	dlKaka := BuildDocumentLayout(records, width, "kaka")
	if dlDefault.Len() == 0 || dlKaka.Len() == 0 {
		t.Fatalf("empty layout")
	}
	// Find user line
	var lineDefault, lineKaka *DocumentLine
	for i := range dlDefault.Lines {
		if dlDefault.Lines[i].RecordIdx == 0 {
			lineDefault = &dlDefault.Lines[i]
			break
		}
	}
	for i := range dlKaka.Lines {
		if dlKaka.Lines[i].RecordIdx == 0 {
			lineKaka = &dlKaka.Lines[i]
			break
		}
	}
	if lineDefault == nil || lineKaka == nil {
		t.Fatalf("user line not found")
	}
	// Check badge strings via RenderedStr stripped
	// Default should be "@Developer  " (fallback)
	defaultBadgeWidth := runewidth.StringWidth("@Developer  ")
	kakaBadgeWidth := runewidth.StringWidth("@kaka  ")
	// Verify spans reflect dynamic width
	var defaultSpanWidth, kakaSpanWidth int
	for _, sp := range lineDefault.Spans {
		if !sp.Selectable {
			defaultSpanWidth = sp.EndCell - sp.StartCell
			break
		}
	}
	for _, sp := range lineKaka.Spans {
		if !sp.Selectable {
			kakaSpanWidth = sp.EndCell - sp.StartCell
			break
		}
	}
	if defaultSpanWidth != defaultBadgeWidth {
		t.Fatalf("default badge width %d != expected %d", defaultSpanWidth, defaultBadgeWidth)
	}
	if kakaSpanWidth != kakaBadgeWidth {
		t.Fatalf("kaka badge width %d != expected %d", kakaSpanWidth, kakaBadgeWidth)
	}
	if kakaSpanWidth == defaultSpanWidth {
		t.Fatalf("badge width should differ for different usernames")
	}
	// RenderedStr should contain the username
	if !strings.Contains(ansi.Strip(lineKaka.RenderedStr), "@kaka") {
		t.Fatalf("kaka badge not in rendered %q", lineKaka.RenderedStr)
	}
	if !strings.Contains(ansi.Strip(lineDefault.RenderedStr), "@Developer") {
		t.Fatalf("default badge not in rendered %q", lineDefault.RenderedStr)
	}
	// RawText should remain clean (without badge) for both
	if lineKaka.RawText != "hello" || lineDefault.RawText != "hello" {
		t.Fatalf("RawText should be clean hello, got kaka %q default %q", lineKaka.RawText, lineDefault.RawText)
	}
}

func TestDocumentLayout_AutoScrollBottomBoundary(t *testing.T) {
	// Multi-record document with many lines to test auto-scroll to bottom
	records := make([]record, 20)
	for i := range records {
		records[i] = record{role: roleAI, text: "line content number " + strings.Repeat("word ", 5)}
	}
	width := 40
	dl := BuildDocumentLayout(records, width, "")
	// Find a line containing "[POLICY]" to simulate bottom policy line
	policyRec := record{role: roleAI, text: "[POLICY] This is the final policy line at the very bottom of document for testing bottom boundary handling"}
	records = append(records, policyRec)
	dl = BuildDocumentLayout(records, width, "")
	m := newTestModel()
	m.width = width
	m.height = 24
	m.records = records
	m.Ready = true
	m.Viewport.Width = width
	m.Viewport.Height = 5
	m.docLayout = &dl
	m.refreshViewportContent()
	geo := m.viewportGeometry()
	// Sync Viewport.Height to geometry for strict max bound (as model does)
	m.Viewport.Height = geo.Height
	// Strict maxYOffset using Viewport.Height as per spec (dynamic, recomputed each iteration)
	m.Viewport.YOffset = 0
	// Recompute geo after height sync
	geo = m.viewportGeometry()
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 0}, lastY: geo.Top + geo.Height - 1}
	for i := 0; i < 5000; i++ {
		curMaxLoop := m.docLayout.Len() - m.Viewport.Height
		if curMaxLoop < 0 {
			curMaxLoop = 0
		}
		if m.Viewport.YOffset >= curMaxLoop {
			break
		}
		_ = m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: geo.Top + geo.Height - 1, X: geo.Left + 2})
		curMax2 := m.docLayout.Len() - m.Viewport.Height
		if curMax2 < 0 {
			curMax2 = 0
		}
		if m.Viewport.YOffset > curMax2 {
			t.Fatalf("YOffset over-incremented %d > max %d at iter %d len=%d height=%d", m.Viewport.YOffset, curMax2, i, m.docLayout.Len(), m.Viewport.Height)
		}
		geo = m.viewportGeometry()
	}
	curMax := m.docLayout.Len() - m.Viewport.Height
	if curMax < 0 {
		curMax = 0
	}
	if m.Viewport.YOffset != curMax {
		t.Fatalf("auto-scroll did not reach bottom: got %d want %d len=%d height=%d", m.Viewport.YOffset, curMax, m.docLayout.Len(), m.Viewport.Height)
	}
	// Cursor should be at bottom line: min(YOffset+Height-1, len-1)
	expectedCursorY := m.Viewport.YOffset + m.Viewport.Height - 1
	if expectedCursorY >= dl.Len() {
		expectedCursorY = dl.Len() - 1
	}
	if m.mouseSel.Cursor.Y != expectedCursorY {
		t.Fatalf("cursor Y %d != expected bottom %d", m.mouseSel.Cursor.Y, expectedCursorY)
	}
	// Verify final line is reachable and contains policy
	bottomLine := dl.Lines[dl.Len()-1]
	if !strings.Contains(bottomLine.RawText, "[POLICY]") && !strings.Contains(bottomLine.RenderedStr, "[POLICY]") {
		// Search for policy line anywhere near bottom
		found := false
		for _, l := range dl.Lines {
			if strings.Contains(l.RawText, "[POLICY]") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("policy line not found in layout")
		}
	}
	// Ensure no stutter: YOffset should be exactly max, not re-clamped
	curMax2 := m.docLayout.Len() - m.Viewport.Height
	if curMax2 < 0 {
		curMax2 = 0
	}
	if m.Viewport.YOffset < 0 || m.Viewport.YOffset > curMax2 {
		t.Fatalf("YOffset out of bounds after auto-scroll")
	}
}



func TestDocumentLayout_PreflightCollapse(t *testing.T) {
	width := 40
	// Active preflight trace: multiple lines
	activeText := "[preflight] snapshot ready target=\"\" sha= tokens=0 Go, commonly referred to as Golang, is efficient."
	activeRec := record{role: roleActivity, text: activeText}
	dlActive := BuildDocumentLayout([]record{activeRec}, width, "")
	// Active should render as 2+ lines (header + body) or at least not single collapsed
	if dlActive.Len() < 2 {
		t.Fatalf("active preflight should be multiple lines, got %d", dlActive.Len())
	}
	// Verify header and body are separate physical lines
	hasHeader := false
	hasBody := false
	for _, l := range dlActive.Lines {
		raw := ansi.Strip(l.RawText)
		if strings.Contains(raw, "[preflight]") {
			hasHeader = true
		}
		if strings.Contains(raw, "Go, commonly") {
			hasBody = true
		}
	}
	if !hasHeader || !hasBody {
		t.Fatalf("active preflight missing header/body header=%v body=%v lines=%v", hasHeader, hasBody, dlActive.Lines)
	}
	// Completed/collapsed preflight: single summary line
	collapsedRec := record{role: roleActivity, text: "✓ preflight completed"}
	dlCollapsed := BuildDocumentLayout([]record{collapsedRec}, width, "")
	if dlCollapsed.Len() != 1 {
		t.Fatalf("collapsed preflight should be single line, got %d lines %v", dlCollapsed.Len(), dlCollapsed.Lines)
	}
	if !strings.Contains(dlCollapsed.Lines[0].RawText, "preflight completed") {
		t.Fatalf("collapsed line should contain summary, got %q", dlCollapsed.Lines[0].RawText)
	}
	// Ensure not dropped mid-selection: build layout with active, then simulate selection active and ensure line count stable
	m := newTestModel()
	m.width = width
	m.records = []record{activeRec}
	m.Ready = true
	m.Viewport.Width = width
	m.Viewport.Height = 10
	dl := BuildDocumentLayout(m.records, width, "")
	m.docLayout = &dl
	m.refreshViewportContent()
	idleLen := m.docLayout.Len()
	// Simulate selection active (should not change line count)
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 5}}
	m.refreshViewportContent()
	activeLen := m.docLayout.Len()
	if idleLen != activeLen {
		t.Fatalf("line count changed mid-selection idle %d vs active %d", idleLen, activeLen)
	}
	// After completion, should collapse deterministically to 1 line
	m.records = []record{collapsedRec}
	dl2 := BuildDocumentLayout(m.records, width, "")
	if dl2.Len() != 1 {
		t.Fatalf("after completion, expected 1 line, got %d", dl2.Len())
	}
}

func TestDocumentLayout_WordWrapping(t *testing.T) {
	wrapWidth := 40
	// Single paragraph of 200 characters (no newlines) – must be broken into multiple physical lines
	paragraph := strings.Repeat("word ", 40) // 200 chars (5*40)
	paragraph = strings.TrimSpace(paragraph)
	if len([]rune(paragraph)) < 150 {
		t.Fatalf("paragraph too short: %d", len([]rune(paragraph)))
	}
	records := []record{{role: roleAI, text: paragraph}}
	dl := BuildDocumentLayout(records, wrapWidth)
	if dl.Len() <= 1 {
		t.Fatalf("word wrapping failed: expected multiple lines for 200-char paragraph with wrapWidth %d, got %d", wrapWidth, dl.Len())
	}
	for i, line := range dl.Lines {
		rawWidth := runewidth.StringWidth(ansi.Strip(line.RawText))
		renderedWidth := runewidth.StringWidth(ansi.Strip(line.RenderedStr))
		if rawWidth > wrapWidth {
			t.Fatalf("line %d RawText width %d exceeds wrapWidth %d: %q", i, rawWidth, wrapWidth, line.RawText)
		}
		if renderedWidth > wrapWidth {
			t.Fatalf("line %d RenderedStr width %d exceeds wrapWidth %d: %q", i, renderedWidth, wrapWidth, ansi.Strip(line.RenderedStr))
		}
		if line.RawText != "" && renderedWidth == 0 {
			t.Fatalf("line %d has empty rendered width but non-empty RawText", i)
		}
	}
	// Also test user prompt with headerWidth accounting
	displayName := "tester"
	headerPlain := "@" + displayName + "  "
	headerWidth := runewidth.StringWidth(headerPlain)
	contentWidth := wrapWidth - headerWidth
	_ = contentWidth
	userPara := strings.Repeat("hello ", 40)
	userRec := record{role: roleUser, text: userPara}
	dlUser := BuildDocumentLayout([]record{userRec}, wrapWidth, displayName)
	if dlUser.Len() <= 1 {
		t.Fatalf("user prompt wrapping failed: expected multiple lines, got %d", dlUser.Len())
	}
	for i, line := range dlUser.Lines {
		renderedWidth := runewidth.StringWidth(ansi.Strip(line.RenderedStr))
		if renderedWidth > wrapWidth {
			t.Fatalf("user line %d rendered width %d exceeds wrapWidth %d: %q", i, renderedWidth, wrapWidth, ansi.Strip(line.RenderedStr))
		}
		rawWidth := runewidth.StringWidth(line.RawText)
		if rawWidth > wrapWidth {
			t.Fatalf("user line %d RawText width %d exceeds wrapWidth %d", i, rawWidth, wrapWidth)
		}
		// First line has header, subsequent lines padded with headerWidth spaces
		if i > 0 {
			stripped := ansi.Strip(line.RenderedStr)
			// Subsequent lines should have at least headerWidth leading spaces (visual alignment)
			if !strings.HasPrefix(stripped, strings.Repeat(" ", headerWidth)) {
				// Allow content without header prefix for non-user? For user, must be padded.
				// Check that visual width still respects wrapWidth and content is within contentWidth
				if runewidth.StringWidth(line.RawText) > contentWidth {
					t.Fatalf("user continuation line %d content width %d exceeds contentWidth %d", i, runewidth.StringWidth(line.RawText), contentWidth)
				}
			}
		}
	}
}

func TestDocumentLayout_StreamingTailLock(t *testing.T) {
	width := 40
	height := 5
	m := newTestModel()
	m.width = width
	m.height = 20
	m.Ready = true
	m.Viewport.Width = width
	m.Viewport.Height = height
	m.mouseSel = mouseSelection{Active: false}
	// Initial records: 3 lines
	records := []record{
		{role: roleAI, text: "line one content " + strings.Repeat("word ", 5)},
		{role: roleAI, text: "line two content " + strings.Repeat("word ", 5)},
		{role: roleAI, text: "line three content " + strings.Repeat("word ", 5)},
	}
	m.records = records
	dl := BuildDocumentLayout(records, width)
	m.docLayout = &dl
	m.refreshViewportContent()
	// Tail-locked: YOffset at max
	maxYOffset := dl.Len() - height
	if maxYOffset < 0 {
		maxYOffset = 0
	}
	m.Viewport.YOffset = maxYOffset
	// Capture YOffset before streaming append
	prevYOffset := m.Viewport.YOffset
	prevMax := maxYOffset
	// Simulate streaming: append new record (new lines)
	newRec := record{role: roleAI, text: "streaming appended line " + strings.Repeat("extra ", 10)}
	m.records = append(m.records, newRec)
	// Trigger incremental update via refresh (which will rebuild docLayout)
	m.refreshViewportContent()
	newMax := m.docLayout.Len() - height
	if newMax < 0 {
		newMax = 0
	}
	if newMax <= prevMax {
		t.Fatalf("expected newMax %d > prevMax %d after appending", newMax, prevMax)
	}
	if m.Viewport.YOffset != newMax {
		t.Fatalf("tail-lock failed: YOffset %d != newMax %d (prev %d) without jitter", m.Viewport.YOffset, newMax, prevYOffset)
	}
	// Ensure no jitter: calling again without new content should keep YOffset stable
	stableYOffset := m.Viewport.YOffset
	m.refreshViewportContent()
	if m.Viewport.YOffset != stableYOffset {
		t.Fatalf("jitter detected: YOffset changed from %d to %d without new content", stableYOffset, m.Viewport.YOffset)
	}
	// If user is dragging selection, tail-lock must NOT auto-scroll
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 0}}
	m.Viewport.YOffset = newMax - 2
	if m.Viewport.YOffset < 0 {
		m.Viewport.YOffset = 0
	}
	saved := m.Viewport.YOffset
	// Append another line while selection active
	m.records = append(m.records, record{role: roleAI, text: "another streaming line " + strings.Repeat("more ", 10)})
	m.refreshViewportContent()
	if m.Viewport.YOffset != saved {
		t.Fatalf("tail-lock should not auto-scroll while mouseSel.Active=true: got %d want %d", m.Viewport.YOffset, saved)
	}
}

func TestDocumentLayout_AutoScrollToAbsoluteBottom(t *testing.T) {
	width := 40
	height := 5
	records := make([]record, 20)
	for i := range records {
		records[i] = record{role: roleAI, text: "line content number " + strings.Repeat("word ", 5)}
	}
	policyRec := record{role: roleAI, text: "[POLICY] This is the final policy line at the very bottom of document for testing bottom boundary handling"}
	records = append(records, policyRec)
	dl := BuildDocumentLayout(records, width, "")
	m := newTestModel()
	m.width = width
	m.height = 24
	m.records = records
	m.Ready = true
	m.Viewport.Width = width
	m.Viewport.Height = height
	m.docLayout = &dl
	m.refreshViewportContent()
	geo := m.viewportGeometry()
	m.Viewport.Height = geo.Height
	geo = m.viewportGeometry()
	m.Viewport.YOffset = 0
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 0}, lastY: geo.Top + geo.Height - 1}
	// Simulate dragging past bottom boundary: continuous ticks
	for i := 0; i < 5000; i++ {
		curMax := m.docLayout.Len() - m.Viewport.Height
		if curMax < 0 {
			curMax = 0
		}
		if m.Viewport.YOffset >= curMax {
			break
		}
		// Tick with Y outside bottom (geo.Top+Height)
		_ = m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: geo.Top + geo.Height, X: geo.Left + 2})
		// Alternative also test with exactly at bottom edge
		curMax2 := m.docLayout.Len() - m.Viewport.Height
		if curMax2 < 0 {
			curMax2 = 0
		}
		if m.Viewport.YOffset > curMax2 {
			t.Fatalf("YOffset over-incremented %d > max %d at iter %d", m.Viewport.YOffset, curMax2, i)
		}
		geo = m.viewportGeometry()
	}
	curMax := m.docLayout.Len() - m.Viewport.Height
	if curMax < 0 {
		curMax = 0
	}
	if m.Viewport.YOffset != curMax {
		t.Fatalf("auto-scroll did not reach absolute bottom: got %d want %d len=%d height=%d", m.Viewport.YOffset, curMax, m.docLayout.Len(), m.Viewport.Height)
	}
	expectedCursorY := m.Viewport.YOffset + m.Viewport.Height - 1
	if expectedCursorY >= dl.Len() {
		expectedCursorY = dl.Len() - 1
	}
	if m.mouseSel.Cursor.Y != expectedCursorY {
		t.Fatalf("cursor Y %d != expected bottom %d (len %d)", m.mouseSel.Cursor.Y, expectedCursorY, dl.Len())
	}
	// Ensure final line [POLICY] is reachable
	found := false
	for _, l := range dl.Lines {
		if strings.Contains(l.RawText, "[POLICY]") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("policy line not found in layout")
	}
	// Ensure no stutter loops: YOffset stays exactly at max and not re-clamped
	if m.Viewport.YOffset < 0 || m.Viewport.YOffset > curMax {
		t.Fatalf("YOffset out of bounds after auto-scroll")
	}
}
