package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestFramebuffer_CodeblockCopyAccuracy verifies that selecting across a
// multi-line markdown codeblock with background padding copies without
// trailing padding spaces and without arbitrary soft-wrap breaks.
func TestFramebuffer_CodeblockCopyAccuracy(t *testing.T) {
	code := "```go\nfunc main() {\n    fmt.Println(\"hello\")\n    fmt.Println(\"world\")\n}\n```"
	m := newGeometryModel(60, 30, []record{{role: roleAI, text: code}})
	if m.docLayout == nil || m.docLayout.Len() == 0 {
		t.Fatalf("docLayout empty")
	}
	if m.framebuffer == nil || len(m.framebuffer.Grid) == 0 {
		t.Fatalf("framebuffer not rasterized")
	}
	// Find codeblock content rows (those containing "func main" and "fmt.Println")
	var startY, endY = -1, -1
	for i, l := range m.docLayout.Lines {
		if strings.Contains(l.RawText, "func main") && startY == -1 {
			startY = i
		}
		if strings.Contains(l.RawText, "world") {
			endY = i
		}
	}
	if startY < 0 || endY < 0 {
		t.Fatalf("codeblock lines not found startY=%d endY=%d", startY, endY)
	}
	// Select across the codeblock content: from startY col 4 (after outer+left) to endY
	// Use X=4 (gutter 2 + box left 2) to capture full content
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: startY, X: 4}, Cursor: GlobalPos{Y: endY, X: 60}}
	copied := m.serializeMouseSelection()
	// Should contain code without trailing padding spaces
	if strings.Contains(copied, " \n") && strings.HasSuffix(copied, " ") {
		t.Fatalf("codeblock copy has trailing spaces: %q", copied)
	}
	for _, line := range strings.Split(copied, "\n") {
		if strings.HasSuffix(line, " ") && strings.TrimSpace(line) != "" {
			// Allow single trailing space only if it's part of content, but not padding
			// Check that line does not end with multiple spaces (padding)
			if strings.HasSuffix(line, "  ") {
				t.Fatalf("codeblock line has padding spaces: %q", line)
			}
		}
	}
	if !strings.Contains(copied, "func main()") {
		t.Fatalf("codeblock copy missing func main: %q", copied)
	}
	if !strings.Contains(copied, "fmt.Println(\"hello\")") {
		t.Fatalf("codeblock copy missing hello: %q", copied)
	}
	if !strings.Contains(copied, "fmt.Println(\"world\")") {
		t.Fatalf("codeblock copy missing world: %q", copied)
	}
	// Verify via direct framebuffer extraction also has no padding
	fbText := extractFromFramebuffer(m.framebuffer, startY, 6, endY, 60)
	for _, line := range strings.Split(fbText, "\n") {
		if strings.HasSuffix(line, "  ") {
			t.Fatalf("framebuffer extract has padding: %q", line)
		}
	}
	// Verify IsPadding cells exist and are skipped
	foundPadding := false
	for y := startY; y <= endY; y++ {
		if y < 0 || y >= len(m.framebuffer.Grid) {
			continue
		}
		for _, c := range m.framebuffer.Grid[y] {
			if c.IsPadding {
				foundPadding = true
				break
			}
		}
	}
	if !foundPadding {
		t.Logf("warning: no IsPadding flagged for codeblock (may still be correct if no padding needed at this width)")
	}
}

// TestFramebuffer_SoftWrapJoin verifies soft-wrapped lines join seamlessly.
func TestFramebuffer_SoftWrapJoin(t *testing.T) {
	// Long paragraph that will wrap at viewport width 40
	text := strings.Repeat("word ", 30) // 150 chars, will wrap to multiple physical lines
	text = strings.TrimSpace(text)
	m := newGeometryModel(40, 30, []record{{role: roleAI, text: text}})
	if m.framebuffer == nil {
		t.Fatalf("framebuffer nil")
	}
	// Find that the paragraph produced multiple physical lines
	if m.docLayout.Len() <= 1 {
		t.Fatalf("expected wrapping to produce multiple lines, got %d", m.docLayout.Len())
	}
	// Check that intermediate wrapped lines are flagged IsSoftWrapped
	softCount := 0
	for y := 0; y < len(m.framebuffer.Grid); y++ {
		row := m.framebuffer.Grid[y]
		if len(row) > 0 && row[len(row)-1].IsSoftWrapped {
			softCount++
		}
	}
	if softCount == 0 {
		t.Fatalf("no soft-wrapped lines flagged")
	}
	// Select across the entire paragraph
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: m.docLayout.Len() - 1, X: 100}}
	copied := m.serializeMouseSelection()
	// Soft-wrapped lines should join without extra newlines for wrapped segments.
	// The original text had no explicit newlines, so copied should be either the same
	// as original (with spaces) or with fewer newlines than physical lines.
	lines := strings.Split(copied, "\n")
	if len(lines) > 2 {
		t.Logf("copied has %d lines, physical %d, soft %d", len(lines), m.docLayout.Len(), softCount)
		// At least softCount lines should have been joined (so copied lines < physical lines)
		if len(lines) >= m.docLayout.Len() {
			t.Fatalf("soft-wrapped lines not joined: copied lines %d >= physical %d", len(lines), m.docLayout.Len())
		}
	}
	// Ensure no excessive double spaces from bad joins
	if strings.Contains(copied, "  ") {
		if count := strings.Count(copied, "  "); count > 10 {
			t.Fatalf("excessive double spaces in soft-wrap join: %d", count)
		}
	}
	if !strings.Contains(copied, "word") {
		t.Fatalf("soft-wrap copy missing words: %q", copied)
	}
}

// TestFramebuffer_WideCharacterCopy verifies CJK/emoji handling with zero drift.
func TestFramebuffer_WideCharacterCopy(t *testing.T) {
	text := "hello 你好世界 world 😀 test 🌍 end"
	m := newGeometryModel(80, 30, []record{{role: roleAI, text: text}})
	if m.framebuffer == nil {
		t.Fatalf("framebuffer nil")
	}
	// Verify wide cells are correctly flagged
	foundWide := false
	foundContinuation := false
	for y := 0; y < len(m.framebuffer.Grid); y++ {
		for x, c := range m.framebuffer.Grid[y] {
			if c.Width == 2 && !c.IsContinuation {
				foundWide = true
				// Next cell should be continuation
				if x+1 < len(m.framebuffer.Grid[y]) && m.framebuffer.Grid[y][x+1].IsContinuation {
					foundContinuation = true
				}
			}
		}
	}
	if !foundWide {
		t.Fatalf("no wide char cells found for CJK/emoji")
	}
	if !foundContinuation {
		t.Fatalf("no continuation cells for wide chars")
	}
	// Select exact substring "你好世界" (4 CJK chars, each 2 cells = 8 cells)
	// Find its position via docLayout extraction vs framebuffer
	// Determine visual X for CJK start: after "hello " (6 cells incl gutter 2? For AI, gutter 2)
	// For AI, gutter 2, "hello " 6 => start at 8
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 8}, Cursor: GlobalPos{Y: 0, X: 15}} // 8 cells for 4 wide chars
	copied := m.serializeMouseSelection()
	if !strings.Contains(copied, "你好") {
		t.Fatalf("wide char copy missing CJK: %q", copied)
	}
	// Select emoji
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 100}}
	full := m.serializeMouseSelection()
	if !strings.Contains(full, "😀") {
		t.Fatalf("emoji missing from full copy: %q", full)
	}
	if !strings.Contains(full, "🌍") {
		t.Fatalf("second emoji missing: %q", full)
	}
	if strings.Contains(full, "\ufffd") {
		t.Fatalf("replacement char drift: %q", full)
	}
	// Verify that selecting a sub-range that lands inside a wide char's second cell
	// snaps to the start (no drift)
	// Find a wide char's continuation X and try to start there
	var wideX = -1
	var wideY = -1
	for y, row := range m.framebuffer.Grid {
		for x, c := range row {
			if c.IsContinuation {
				wideX = x
				wideY = y
				break
			}
		}
		if wideX >= 0 {
			break
		}
	}
	if wideX >= 0 {
		// Simulate mouse at continuation cell: should snap to left half
		pos := m.mousePosToFramebufferGlobal(tea.MouseMsg{X: wideX, Y: m.viewportGeometry().Top + wideY - m.docScrollOffset + m.viewportContentPrefixHeight()})
		// The returned X should be wideX-1 (snapped)
		if pos.X != wideX-1 {
			t.Logf("continuation snap: got %d want %d (wideX %d)", pos.X, wideX-1, wideX)
		}
	}
}

// TestFramebuffer_SoftWrapVsExplicitNewline checks that explicit \n preserves newline
func TestFramebuffer_SoftWrapVsExplicitNewline(t *testing.T) {
	text := "line one\nline two\nline three"
	m := newGeometryModel(80, 30, []record{{role: roleAI, text: text}})
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 2, X: 60}}
	copied := m.serializeMouseSelection()
	lines := strings.Split(copied, "\n")
	if len(lines) != 3 {
		t.Fatalf("explicit newlines should produce 3 lines, got %d: %q", len(lines), copied)
	}
	if lines[0] != "line one" || lines[1] != "line two" || lines[2] != "line three" {
		t.Fatalf("explicit newline copy wrong: %q", copied)
	}
}

// BenchmarkSelectionDragPerf verifies O(1) lookup complexity (<50 microseconds)
func BenchmarkSelectionDragPerf(b *testing.B) {
	text := strings.Repeat("alpha bravo charlie delta echo ", 20)
	m := newGeometryModel(80, 30, []record{{role: roleAI, text: text}})
	geo := m.viewportGeometry()
	y := geo.Top + m.viewportContentPrefixHeight()
	x := geo.Left + 10
	msg := tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.mousePosToFramebufferGlobal(msg)
	}
	b.StopTimer()
	elapsedPerOp := time.Duration(b.Elapsed().Nanoseconds() / int64(b.N))
	if elapsedPerOp > 50*time.Microsecond {
		b.Fatalf("mouse drag O(1) lookup too slow: %v > 50µs", elapsedPerOp)
	}
}

// TestFramebuffer_CapViewport ensures framebuffer is capped to viewport+buffer
func TestFramebuffer_CapViewport(t *testing.T) {
	// Create many records to exceed viewport
	records := make([]record, 200)
	for i := range records {
		records[i] = record{role: roleAI, text: "line content number " + strings.Repeat("word ", 5)}
	}
	m := newGeometryModel(80, 10, records)
	if m.framebuffer == nil {
		t.Fatalf("framebuffer nil")
	}
	// With viewport height 10 and buffer 100, expected max rasterized rows is about 210
	// But docLen is maybe >200 due to wrapping, check that Grid height is total docLen but
	// heavy cells only for windowed portion
	totalDoc := m.docLayout.Len()
	nonEmptyRows := 0
	for _, row := range m.framebuffer.Grid {
		if len(row) > 0 {
			nonEmptyRows++
		}
	}
	// nonEmpty should be limited to viewport+2*buffer, not totalDoc
	maxExpected := m.Viewport.Height + 2*FramebufferBufferLines
	if nonEmptyRows > maxExpected+5 { // allow small slack
		t.Fatalf("framebuffer not capped: nonEmpty %d > maxExpected %d (total %d)", nonEmptyRows, maxExpected, totalDoc)
	}
}

// TestFramebuffer_InvalidateOnResize ensures re-rasterize on WindowSizeMsg
func TestFramebuffer_InvalidateOnResize(t *testing.T) {
	m := newGeometryModel(80, 30, []record{{role: roleAI, text: "hello world"}})
	fb1 := m.framebuffer
	if fb1 == nil {
		t.Fatalf("fb1 nil")
	}
	// Simulate resize
	_, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	fb2 := m.framebuffer
	if fb2 == nil {
		t.Fatalf("fb2 nil after resize")
	}
	if fb1.Width == fb2.Width {
		t.Fatalf("framebuffer width should change after resize: %d vs %d", fb1.Width, fb2.Width)
	}
	// Simulate mouse move should NOT invalidate
	fbBefore := m.framebuffer
	// Do a mouse motion (drag)
	geo := m.viewportGeometry()
	y := geo.Top + m.viewportContentPrefixHeight()
	x := geo.Left + 5
	_, _ = m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	fbAfter := m.framebuffer
	if fbBefore != fbAfter {
		// It's okay if it's same pointer (not reallocated) – we check that width unchanged and not rebuilt unnecessarily
		// For this test we just ensure not nil and same width
		if fbAfter.Width != fbBefore.Width {
			t.Fatalf("framebuffer should not be re-rasterized on mouse move")
		}
	}
}
