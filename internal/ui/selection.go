package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
// logical record position using the authoritative viewport geometry and the
// physical-to-logical line model already used by vi scrolling. It does NOT
// guess layout - every offset (header, prefix, gutter) is derived from the
// same rendering geometry the viewport uses.
func (m *model) mousePosToLogical(msg tea.MouseMsg) selPos {
	if len(m.records) == 0 {
		return selPos{Line: 0, Col: 0}
	}
	geo := m.viewportGeometry()
	relY := msg.Y - geo.Top
	if relY < 0 {
		relY = 0
	}
	if relY >= geo.Height {
		relY = geo.Height - 1
	}
	physOffset := 0
	if m.Ready {
		physOffset = m.Viewport.YOffset
	}
	targetContentRow := physOffset + relY

	// The viewport's SetContent starts with a prefix (workspace header, context
	// header, banner) before the first record. Remove it so the phys mapping
	// aligns with the record array.
	prefix := m.viewportContentPrefixHeight()
	recordRow := targetContentRow - prefix
	if recordRow < 0 {
		recordRow = 0
	}

	n := len(m.records)
	phys := make([]int, n+1)
	for i := 0; i < n; i++ {
		c := m.renderedLineCount(m.records[i])
		if c == 0 {
			c = 1
		}
		phys[i+1] = phys[i] + c
	}
	// Clamp recordRow to last physical row
	totalPhys := phys[n]
	if recordRow >= totalPhys {
		recordRow = totalPhys - 1
		if recordRow < 0 {
			recordRow = 0
		}
	}
	idx := n - 1
	for i := 0; i < n; i++ {
		if recordRow < phys[i+1] {
			idx = i
			break
		}
	}
	// ── Horizontal mapping: terminal X -> logical column ──────────
	// Columns are measured in terminal cells. Strip ANSI from the gutter to
	// obtain its true cell width, then convert the remaining cell offset into
	// a rune index using per-rune cell widths (runewidth), so CJK / emoji /
	// wide runes map correctly.
	gutterWidth := lipgloss.Width(ansi.Strip(gutterFor(m.records[idx].role)))
	if gutterWidth < 0 {
		gutterWidth = 0
	}
	cellCol := msg.X - geo.Left - gutterWidth
	if cellCol < 0 {
		cellCol = 0
	}
	// For wrapped records the physical row may be the 2nd/3rd wrapped segment.
	// Its base logical offset is wrapRow * wrapWidth.
	wrapOffset := recordRow - phys[idx]
	wrapWidth := m.width - 4
	if wrapWidth < 10 {
		wrapWidth = 10
	}
	relRune := m.cellToRuneCol(idx, cellCol)
	var col int
	if wrapOffset == 0 {
		col = relRune
	} else {
		totalCells := wrapOffset*wrapWidth + cellCol
		col = m.cellToRuneCol(idx, totalCells)
	}
	lineLen := m.lineRuneLen(idx)
	if lineLen == 0 {
		col = 0
	} else if col >= lineLen {
		col = lineLen - 1
	}
	if col < 0 {
		col = 0
	}
	return selPos{Line: idx, Col: col}
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
		// Compute max offset including prefix lines, not just records.
		n := len(m.records)
		totalPhys := m.viewportContentPrefixHeight()
		for i := 0; i < n; i++ {
			c := m.renderedLineCount(m.records[i])
			if c == 0 {
				c = 1
			}
			totalPhys += c
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
