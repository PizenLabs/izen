package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// selPos is a logical content coordinate - record identity + column within
// that record's plain text. It is independent from terminal wrapping.
type selPos struct {
	Line int // index into m.records
	Col  int // rune offset within records[Line].text
}

// mouseSelection is the lightweight orthogonal selection state.
// It is NOT execution state and never mutates conversation.
type mouseSelection struct {
	Active   bool
	Dragging bool
	Anchor   selPos
	Cursor   selPos
	// last mouse absolute coordinates for auto-scroll direction
	lastY int
	lastX int
	// TickActive reports whether the single auto-scroll loop is running.
	TickActive bool
}

// selectionEdgeRows is the drag edge zone that triggers auto-scroll.
const selectionEdgeRows = 3

// selectionAutoScrollInterval bounds CPU: one scroll per tick, not a tight loop.
const selectionAutoScrollInterval = 80 * time.Millisecond

// selectionScrollTickMsg drives bounded viewport auto-scroll while dragging
// near an edge. It carries the last mouse Y/X so the viewport can follow.
type selectionScrollTickMsg struct {
	Y int
	X int
}

func selectionScrollTickCmd(y, x int) tea.Cmd {
	return tea.Tick(selectionAutoScrollInterval, func(time.Time) tea.Msg {
		return selectionScrollTickMsg{Y: y, X: x}
	})
}

// mousePosToLogical maps an absolute terminal MouseMsg coordinate (X,Y) to a
// logical record position using the single-source ViewportHitMap generated
// atomically alongside Viewport.SetContent. It is O(1) into the visible window
// (Rows[relY]) and correctly accounts for per-row PrefixCells, wrapping
// RuneStartIdx, and grapheme cell widths. No wrapping or gutter is re-derived.
func (m *model) mousePosToLogical(msg tea.MouseMsg) selPos {
	if len(m.records) == 0 {
		return selPos{Line: 0, Col: 0}
	}
	geo := m.viewportGeometry()
	// Relative Multi-Pane Geometry: convert absolute terminal coordinates
	// into viewport-local coordinates. In split panes (tmux/Ghostty),
	// msg.X/Y are absolute; viewportGeometry.Left/Top are pane offsets.
	relY := msg.Y - geo.Top
	relX := msg.X - geo.Left
	// Clamp bounds strictly: reject/clamp without panic. Outside viewport
	// horizontally or vertically we clamp to nearest valid edge.
	if relY < 0 {
		relY = 0
	}
	if relY >= geo.Height {
		relY = geo.Height - 1
	}
	if relX < 0 {
		relX = 0
	}
	if relX >= geo.Width {
		relX = geo.Width - 1
	}
	_ = relX // preserved for geometry completeness; X mapping uses geo.Left below
	yOff := 0
	if m.Ready {
		yOff = m.Viewport.YOffset
	}
	// Ensure hitmap is populated (tests may call before first refresh).
	if len(m.fullHitRows) == 0 {
		m.fullHitRows = buildFullHitMap(m)
		total := countPhysicalRows(m.Viewport.View())
		_ = total
	}
	var row RowLayout
	found := false
	// Fast path: windowed hitmap Rows[relY] when YOffset matches.
	if len(m.viewportHitMap.Rows) > 0 && m.viewportHitMap.YOffset == yOff && relY < len(m.viewportHitMap.Rows) {
		row = m.viewportHitMap.Rows[relY]
		found = true
	} else if len(m.fullHitRows) > 0 {
		target := yOff + relY
		if target < 0 {
			target = 0
		}
		if target >= len(m.fullHitRows) {
			target = len(m.fullHitRows) - 1
		}
		if target >= 0 && target < len(m.fullHitRows) {
			row = m.fullHitRows[target]
			found = true
		}
	}
	if !found {
		// Fallback: clamp to last record start when hitmap unavailable (should not happen).
		return selPos{Line: len(m.records) - 1, Col: 0}
	}
	// Chrome rows (prefix / headers / blank separators with no logical line) clamp to nearest record.
	if row.RecordIdx < 0 || row.LogicalLine < 0 {
		// Scan outward for nearest record row in fullHitRows.
		target := yOff + relY
		// Search backward then forward for a real record.
		for d := 1; d < len(m.fullHitRows); d++ {
			for _, candIdx := range []int{target - d, target + d} {
				if candIdx >= 0 && candIdx < len(m.fullHitRows) {
					c := m.fullHitRows[candIdx]
					if c.RecordIdx >= 0 && c.LogicalLine >= 0 {
						row = c
						found = true
						break
					}
				}
			}
			if found {
				break
			}
		}
		if !found {
			// No record rows at all — clamp to first
			return selPos{Line: 0, Col: 0}
		}
	}
	idx := int(row.RecordIdx)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.records) {
		idx = len(m.records) - 1
	}
	// Horizontal: content cell offset after stripping per-row PrefixCells.
	// Use the already-clamped relative X so split-pane offsets cannot drift.
	cellX := relX
	if cellX < 0 {
		cellX = 0
	}
	if cellX >= geo.Width {
		cellX = geo.Width - 1
	}
	prefixCells := int(row.PrefixCells)
	contentX := cellX - prefixCells
	if contentX < 0 {
		contentX = 0
	}
	// Map contentX (cells) to rune offset within this row's logical line segment,
	// using runewidth so CJK (2 cells) and emoji (2 cells) map correctly.
	// The hitmap's RuneStartIdx is the absolute rune index in the logical line
	// where this physical segment begins.
	rawLines := strings.Split(sanitizeText(m.records[idx].text), "\n")
	ll := int(row.LogicalLine)
	if ll < 0 || ll >= len(rawLines) {
		// Fallback to whole record text (single logical line)
		ll = 0
		if len(rawLines) > 0 {
			rawLines[0] = sanitizeText(m.records[idx].text)
		} else {
			return selPos{Line: idx, Col: 0}
		}
	}
	lineStr := rawLines[ll]
	// Clamp contentX to this row's ContentLen so clicks beyond line end map to segment end.
	if int(row.ContentLen) > 0 && contentX > int(row.ContentLen) {
		contentX = int(row.ContentLen)
	}
	absCol := cellToRuneInString(lineStr, int(row.RuneStartIdx), contentX)
	// Clamp to line bounds (selPos is inclusive; last index is len-1 for non-empty)
	lineRunes := []rune(lineStr)
	if len(lineRunes) == 0 {
		absCol = 0
	} else if absCol >= len(lineRunes) {
		absCol = len(lineRunes) - 1
		if absCol < 0 {
			absCol = 0
		}
	}
	if absCol < 0 {
		absCol = 0
	}
	return selPos{Line: idx, Col: absCol}
}

// cellToRuneCol converts a visual cell offset within a record's plain text
// into a rune index, using per-rune widths so wide glyphs map correctly.
func (m *model) cellToRuneCol(lineIdx, targetCells int) int {
	if targetCells <= 0 {
		return 0
	}
	if lineIdx < 0 || lineIdx >= len(m.records) {
		return 0
	}
	text := ansi.Strip(m.records[lineIdx].text)
	runes := []rune(text)
	cells := 0
	for i, r := range runes {
		if cells >= targetCells {
			return i
		}
		cells += runewidth.RuneWidth(r)
	}
	return len(runes)
}

// normalizedSelection returns ordered anchor/cursor so anchor <= cursor in
// (Line,Col) tuple order.
func (s mouseSelection) normalized() (selPos, selPos) {
	a, c := s.Anchor, s.Cursor
	if a.Line > c.Line || (a.Line == c.Line && a.Col > c.Col) {
		return c, a
	}
	return a, c
}

// serializeMouseSelection resolves the selected logical range against plain text
// records, stripping ANSI and preserving multiline/code blocks with no viewport
// border/spinner leakage.
func (m *model) serializeMouseSelection() string {
	if !m.mouseSel.Active {
		return ""
	}
	sLine, eLine := m.mouseSel.normalized()
	if sLine.Line < 0 {
		sLine.Line = 0
	}
	if eLine.Line >= len(m.records) {
		eLine.Line = len(m.records) - 1
	}
	if sLine.Line > eLine.Line {
		return ""
	}
	var b strings.Builder
	if sLine.Line == eLine.Line {
		runes := []rune(m.records[sLine.Line].text)
		end := eLine.Col + 1
		if end > len(runes) {
			end = len(runes)
		}
		start := sLine.Col
		if start < 0 {
			start = 0
		}
		if start < end {
			b.WriteString(string(runes[start:end]))
		}
		return b.String()
	}
	for i := sLine.Line; i <= eLine.Line && i < len(m.records); i++ {
		runes := []rune(m.records[i].text)
		switch i {
		case sLine.Line:
			if sLine.Col < len(runes) {
				b.WriteString(string(runes[sLine.Col:]))
			}
		case eLine.Line:
			end := eLine.Col + 1
			if end > len(runes) {
				end = len(runes)
			}
			if end > 0 {
				b.WriteString(string(runes[:end]))
			}
		default:
			b.WriteString(m.records[i].text)
		}
		if i < eLine.Line {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// copyMouseSelection serializes the selection to the clipboard abstraction
// without requiring Ctrl+C, then clears the visual selection.
func (m *model) copyMouseSelection() tea.Cmd {
	text := m.serializeMouseSelection()
	if strings.TrimSpace(text) == "" {
		m.mouseSel = mouseSelection{}
		m.frozenFullHitRows = nil
		m.frozenViewportStr = ""
		m.frozenRecords = nil
		m.refreshViewportContent()
		return nil
	}
	var werr error
	if m.clipboard != nil {
		werr = m.clipboard.WriteAll(text)
	} else {
		werr = clipboardWriteAll(text)
	}
	m.mouseSel = mouseSelection{}
	m.frozenFullHitRows = nil
	m.frozenViewportStr = ""
	m.frozenRecords = nil
	m.refreshViewportContent()
	if werr != nil {
		m.uiNotice = "Failed to copy selection: " + werr.Error()
	} else {
		m.uiNotice = "Copied selection to clipboard"
	}
	return nil
}

// handleSelectionAutoScroll performs one bounded viewport step when dragging
// near an edge and re-issues the tick only if still in the edge zone. There
// is at most one active loop (TickActive) and scroll delta uses velocity
// derived from distance into the edge zone, so motion is smooth rather than
// fixed jumps repeating at pause intervals.
func (m *model) handleSelectionAutoScroll(msg selectionScrollTickMsg) tea.Cmd {
	if !m.mouseSel.Active || !m.mouseSel.Dragging {
		m.mouseSel.TickActive = false
		return nil
	}
	geo := m.viewportGeometry()
	relY := msg.Y - geo.Top
	// Derive velocity from distance into edge zone.
	delta := 0
	if relY < selectionEdgeRows { //nolint:gocritic
		dist := selectionEdgeRows - relY // 1..3
		if dist <= 1 {
			delta = -1
		} else {
			delta = -2
		}
	} else if relY >= geo.Height-selectionEdgeRows {
		dist := relY - (geo.Height - selectionEdgeRows) + 1 // 1..3+
		if dist <= 1 {
			delta = 1
		} else {
			delta = 2
		}
	} else {
		// Not in edge zone - check stored lastY as fallback (absolute)
		lastRel := m.mouseSel.lastY - geo.Top
		if lastRel < selectionEdgeRows && lastRel >= 0 {
			delta = -1
		} else if lastRel >= geo.Height-selectionEdgeRows {
			delta = 1
		}
	}
	if delta == 0 {
		m.mouseSel.TickActive = false
		return nil
	}
	if m.Ready {
		// Compute max offset from hitmap (single source) when available.
		totalPhys := 0
		if len(m.fullHitRows) > 0 {
			totalPhys = len(m.fullHitRows)
		} else {
			n := len(m.records)
			totalPhys = m.viewportContentPrefixHeight()
			for i := 0; i < n; i++ {
				c := m.renderedLineCount(m.records[i])
				if c == 0 {
					c = 1
				}
				totalPhys += c
			}
		}
		maxOff := totalPhys - geo.Height
		if maxOff < 0 {
			maxOff = 0
		}
		newOff := m.Viewport.YOffset + delta
		if newOff < 0 {
			newOff = 0
		}
		if newOff > maxOff {
			newOff = maxOff
		}
		if newOff != m.Viewport.YOffset {
			m.Viewport.SetYOffset(newOff)
			m.userIsScrollingUp = true
			// Extend selection so cursor follows mouse as viewport moves.
			m.mouseSel.Cursor = m.mousePosToLogical(tea.MouseMsg{X: msg.X, Y: msg.Y})
			m.refreshViewportContent()
		}
		// If we hit the clamp edge and cannot move further, stop the loop.
		if newOff == 0 && delta < 0 {
			m.mouseSel.TickActive = false
			return nil
		}
		if newOff == maxOff && delta > 0 {
			m.mouseSel.TickActive = false
			return nil
		}
	}
	m.mouseSel.TickActive = true
	return selectionScrollTickCmd(msg.Y, msg.X)
}

// isModalForMouse reports whether mouse selection/scroll should be suppressed
// because a genuinely modal interaction owns input (approval, quit confirm, etc).
func (m *model) isModalForMouse() bool {
	if m.pendingQuitConfirm {
		return true
	}
	if m.state == StateAwaitingApproval || m.state == StateHotfixAmbiguous {
		return true
	}
	if m.showModelPicker {
		return true
	}
	if m.pendingBuildApproval || m.pendingBuildTask != nil {
		// approval dock is interactive - don't let drag interfere
		return true
	}
	return false
}

// renderRecordsWithMouseSelection renders all chat records with mouse drag
// selection highlighting applied inline, purely visual. It reuses the same
// injectStyleRange mechanism as vi visual mode so ANSI stays intact.
func (m *model) renderRecordsWithMouseSelection() string {
	if len(m.records) == 0 {
		return ""
	}
	sStart, sEnd := m.mouseSel.normalized()
	var b strings.Builder
	for i, rec := range m.records {
		rendered := m.renderRecordForViewport(rec)
		if rendered == "" {
			continue
		}
		if i >= sStart.Line && i <= sEnd.Line {
			lineLen := m.lineRuneLen(i)
			if lineLen > 0 {
				sCol, eCol := 0, lineLen-1
				if i == sStart.Line {
					sCol = clampCol(sStart.Col, lineLen)
				}
				if i == sEnd.Line {
					eCol = clampCol(sEnd.Col, lineLen)
				}
				if eCol < sCol {
					eCol = sCol
				}
				rendered = injectStyleRange(rendered, sCol, eCol, viSelectionBgStyle)
			}
		}
		b.WriteString(rendered)
		if i < len(m.records)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
