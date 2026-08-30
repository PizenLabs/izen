package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// selPos is deprecated logical coordinate retained for backward compatibility
// with existing tests. New code must use GlobalPos (space-anchored).
type selPos struct {
	Line int
	Col  int
}

// ToGlobal converts selPos to GlobalPos (Y=Line, X=Col in cell units).
func (s selPos) ToGlobal() GlobalPos { return GlobalPos{Y: s.Line, X: s.Col} }

// FromGlobal converts GlobalPos to selPos.
func FromGlobal(g GlobalPos) selPos { return selPos{Line: g.Y, Col: g.X} }

// mouseSelection is the lightweight orthogonal selection state.
// Anchor and Cursor MUST strictly store GlobalPos (space-anchored).
type mouseSelection struct {
	Active     bool
	Dragging   bool
	Anchor     GlobalPos
	Cursor     GlobalPos
	lastY      int
	lastX      int
	TickActive bool
}

// selectionEdgeRows is the drag edge zone that triggers auto-scroll.
const selectionEdgeRows = 3

// selectionAutoScrollInterval bounds CPU: one scroll per tick, not a tight loop.
const selectionAutoScrollInterval = 80 * time.Millisecond

type selectionScrollTickMsg struct {
	Y int
	X int
}

// AutoScrollTickMsg is the timer-driven selection auto-scroll tick.
// It is an alias for backward compat with spec naming.
type AutoScrollTickMsg = selectionScrollTickMsg

func selectionScrollTickCmd(y, x int) tea.Cmd {
	return tea.Tick(selectionAutoScrollInterval, func(time.Time) tea.Msg {
		return selectionScrollTickMsg{Y: y, X: x}
	})
}

// AutoScrollTickCmd is the spec-named alias for continuous background ticking.
func AutoScrollTickCmd(y, x int) tea.Cmd {
	return selectionScrollTickCmd(y, x)
}

// mousePosToLogical maps an absolute terminal MouseMsg coordinate (X,Y) to a
// logical record position using the single-source ViewportHitMap generated
// atomically alongside Viewport.SetContent. Retained for legacy geometry tests.
func (m *model) mousePosToLogical(msg tea.MouseMsg) selPos {
	if len(m.records) == 0 {
		return selPos{Line: 0, Col: 0}
	}
	geo := m.viewportGeometry()
	relY := msg.Y - geo.Top
	relX := msg.X - geo.Left
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
	_ = relX
	yOff := 0
	if m.Ready {
		yOff = m.docScrollOffset
	}
	if len(m.fullHitRows) == 0 {
		m.fullHitRows = buildFullHitMap(m)
	}
	var row RowLayout
	found := false
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
		return selPos{Line: len(m.records) - 1, Col: 0}
	}
	if row.RecordIdx < 0 || row.LogicalLine < 0 {
		target := yOff + relY
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
	rawLines := strings.Split(sanitizeText(m.records[idx].text), "\n")
	ll := int(row.LogicalLine)
	if ll < 0 || ll >= len(rawLines) {
		ll = 0
		if len(rawLines) > 0 {
			rawLines[0] = sanitizeText(m.records[idx].text)
		} else {
			return selPos{Line: idx, Col: 0}
		}
	}
	lineStr := rawLines[ll]
	if int(row.ContentLen) > 0 && contentX > int(row.ContentLen) {
		contentX = int(row.ContentLen)
	}
	absCol := cellToRuneInString(lineStr, int(row.RuneStartIdx), contentX)
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

// mousePosToGlobal converts absolute terminal MouseMsg to GlobalPos via
// DocumentLayout.ScreenToGlobal when layout is available. DocumentLayout is
// record-only (GlobalY 0 == first record's first physical row), so we subtract
// the viewport content prefix height to align viewport relY with document Y.
func (m *model) mousePosToGlobal(msg tea.MouseMsg) GlobalPos {
	geo := m.viewportGeometry()
	yOff := 0
	if m.Ready {
		yOff = m.docScrollOffset
	}
	if m.docLayout != nil && m.docLayout.Len() > 0 {
		prefix := m.viewportContentPrefixHeight()
		// Convert absolute to document-relative via prefix offset
		relY := msg.Y - geo.Top
		relX := msg.X - geo.Left
		if relY < 0 {
			relY = 0
		}
		if relX < 0 {
			relX = 0
		}
		if relY >= geo.Height {
			relY = geo.Height - 1
		}
		if relX >= geo.Width {
			relX = geo.Width - 1
		}
		// Document Y is viewport Y minus prefix, clamped
		docY := yOff + relY - prefix
		if docY < 0 {
			docY = 0
		}
		if docY >= m.docLayout.Len() {
			docY = m.docLayout.Len() - 1
		}
		// X is cell column relative to viewport left, preserved for geometry
		return GlobalPos{Y: docY, X: relX}
	}
	// Fallback: physical row index mapping
	if len(m.records) == 0 {
		return GlobalPos{Y: 0, X: 0}
	}
	relY := msg.Y - geo.Top
	relX := msg.X - geo.Left
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
	// Use hitmap fallback if no docLayout
	if len(m.fullHitRows) == 0 {
		m.fullHitRows = buildFullHitMap(m)
	}
	// Simple fallback: physical row index is yOff+relY
	return GlobalPos{Y: yOff + relY, X: relX}
}

// normalized returns ordered anchor/cursor so anchor <= cursor in (Y,X) tuple order.
func (s mouseSelection) normalized() (GlobalPos, GlobalPos) {
	a, c := s.Anchor, s.Cursor
	if a.Y > c.Y || (a.Y == c.Y && a.X > c.X) {
		return c, a
	}
	return a, c
}

// normalizedSel retains old selPos ordering for legacy callers.
func (s mouseSelection) normalizedSel() (selPos, selPos) {
	a, c := FromGlobal(s.Anchor), FromGlobal(s.Cursor)
	if a.Line > c.Line || (a.Line == c.Line && a.Col > c.Col) {
		return c, a
	}
	return a, c
}

// serializeMouseSelection is the pure geometry extraction path: it extracts
// visible text matching selected cell bounds across lines [startY : endY],
// stripping ANSI but preserving chrome if manually dragged. It uses
// DocumentLayout when available, falling back to legacy logical record slicing.
func (m *model) serializeMouseSelection() string {
	if !m.mouseSel.Active {
		return ""
	}
	// Prefer DocumentLayout geometry
	if m.docLayout != nil && m.docLayout.Len() > 0 {
		s, e := m.mouseSel.normalized()
		// Clamp to document bounds
		if s.Y < 0 {
			s.Y = 0
		}
		if e.Y >= m.docLayout.Len() {
			e.Y = m.docLayout.Len() - 1
		}
		text := m.docLayout.ExtractText(s, e)
		// Strip ANSI already done in ExtractText; ensure no leakage
		return ansi.Strip(text)
	}
	// Fallback: legacy logical record slicing (for tests without layout)
	sLine, eLine := m.mouseSel.normalizedSel()
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
		m.setToast("Failed to copy selection: " + werr.Error())
	} else {
		m.setToast("Copied selection to clipboard")
	}
	return nil
}

// handleSelectionAutoScroll performs one bounded viewport step when dragging
// outside viewport bounds and re-issues continuous background ticking until mouse release.
// Invariant: Anchor remains STRICTLY UNCHANGED in global space; Cursor.Y
// dynamically updates to YOffset + viewportHeight -1 for down scroll.
// Spec: When mouseSel.Active==true and screen Y outside viewport range
// (msg.Y >= topMargin+viewportHeight or msg.Y < topMargin), trigger continuous
// AutoScrollTickMsg until tea.MouseReleaseMsg. On each tick, recalculate
// maxYOffset, increment YOffset by 1, update Cursor.
func (m *model) handleSelectionAutoScroll(msg selectionScrollTickMsg) tea.Cmd {
	if !m.mouseSel.Active || !m.mouseSel.Dragging {
		m.mouseSel.TickActive = false
		return nil
	}
	geo := m.viewportGeometry()
	delta := 0
	// Primary spec trigger: outside viewport bounds – strict increment by 1 per tick
	switch {
	case msg.Y >= geo.Top+geo.Height:
		delta = 1
	case msg.Y < geo.Top:
		delta = -1
	default:
		// Inside viewport: edge-zone velocity (legacy) for smooth acceleration
		relY := msg.Y - geo.Top
		switch {
		case relY < selectionEdgeRows:
			dist := selectionEdgeRows - relY
			switch {
			case dist <= 1:
				delta = -1
			default:
				delta = -2
			}
		case relY >= geo.Height-selectionEdgeRows:
			dist := relY - (geo.Height - selectionEdgeRows) + 1
			switch {
			case dist <= 1:
				delta = 1
			default:
				delta = 2
			}
		default:
			lastRel := m.mouseSel.lastY - geo.Top
			if lastRel < selectionEdgeRows && lastRel >= 0 {
				delta = -1
			} else if lastRel >= geo.Height-selectionEdgeRows {
				delta = 1
			}
		}
	}
	if delta == 0 {
		m.mouseSel.TickActive = false
		return nil
	}
	if m.Ready {
		// Strict maxYOffset using the cached full-document scroll height as
		// source of truth (consistent with the manual-slicing contract).
		maxYOffset := m.maxAppScroll()
		if maxYOffset == 0 && m.docLayout != nil && m.docLayout.Len() > 0 && m.Viewport.Height > 0 {
			// Fallback: derive from docLayout when no refresh has run yet.
			maxYOffset = m.docLayout.Len() - m.Viewport.Height
			if maxYOffset < 0 {
				maxYOffset = 0
			}
		}
		if maxYOffset == 0 && m.lastScrollTotal <= 0 {
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
			maxYOffset = totalPhys - geo.Height
			if maxYOffset < 0 {
				maxYOffset = 0
			}
		}
		// Enforce increment only if not at bounds (prevents over-increment and Bubble Tea re-clamp jerk)
		if delta > 0 && m.docScrollOffset >= maxYOffset {
			m.mouseSel.TickActive = false
			return nil
		}
		if delta < 0 && m.docScrollOffset <= 0 {
			m.mouseSel.TickActive = false
			return nil
		}
		newOff := m.docScrollOffset + delta
		if newOff < 0 {
			newOff = 0
		}
		if newOff > maxYOffset {
			newOff = maxYOffset
		}
		if newOff != m.docScrollOffset {
			m.docScrollOffset = newOff
			m.setScrollLocked(true)
			// Space-anchored invariant: Anchor unchanged, Cursor.Y follows viewport strictly
			// For down scroll, Cursor.Y = min(YOffset+Height-1, len-1) to reach absolute bottom smoothly
			anchor := m.mouseSel.Anchor
			var newGlobalY int
			if delta > 0 {
				// Scrolling down: bottom edge
				if m.docLayout != nil && m.docLayout.Len() > 0 {
					newGlobalY = m.docScrollOffset + m.Viewport.Height - 1
					if newGlobalY >= m.docLayout.Len() {
						newGlobalY = m.docLayout.Len() - 1
					}
				} else {
					newGlobalY = m.docScrollOffset + geo.Height - 1
				}
			} else {
				// Scrolling up: top edge
				newGlobalY = m.docScrollOffset
			}
			relX := msg.X - geo.Left
			if relX < 0 {
				relX = 0
			}
			if relX >= geo.Width {
				relX = geo.Width - 1
			}
			m.mouseSel.Cursor = GlobalPos{Y: newGlobalY, X: relX}
			m.mouseSel.Anchor = anchor
			m.refreshViewportContent()
		}
		if newOff == 0 && delta < 0 {
			m.mouseSel.TickActive = false
			return nil
		}
		if newOff == maxYOffset && delta > 0 {
			m.mouseSel.TickActive = false
			return nil
		}
	}
	m.mouseSel.TickActive = true
	return selectionScrollTickCmd(msg.Y, msg.X)
}

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
		return true
	}
	return false
}

// renderRecordsWithMouseSelection renders all chat records with mouse drag
// selection highlighting applied inline, purely visual. For DocumentLayout path
// it highlights visible cell ranges per GlobalPos without modifying line counts.
func (m *model) renderRecordsWithMouseSelection() string {
	if len(m.records) == 0 {
		return ""
	}
	// If DocumentLayout available, highlight via cell ranges
	if m.docLayout != nil && m.docLayout.Len() > 0 {
		return m.renderDocumentWithSelection()
	}
	sStart, sEnd := m.mouseSel.normalizedSel()
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

// MapCellToByteIndex scans an ANSI-encoded line and returns a mapping from
// visual display cell -> byte offset in the raw string. ANSI escapes (\x1b[...m)
// are zero-width and skipped, CJK double-width runes are counted via
// runewidth.RuneWidth, ensuring pixel-perfect visual column to byte mapping.
func MapCellToByteIndex(s string, targetCell int) int {
	if targetCell <= 0 {
		return 0
	}
	cells := 0
	i := 0
	for i < len(s) && cells < targetCell {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		// Decode rune
		var r int
		var size int
		if s[i] < 0x80 {
			r = int(s[i])
			size = 1
		} else {
			// Use utf8 decode
			ru, sz := decodeRune(s[i:])
			r = int(ru)
			size = sz
		}
		w := runewidth.RuneWidth(rune(r))
		if w < 0 {
			w = 0
		}
		if cells+w > targetCell {
			return i
		}
		cells += w
		i += size
	}
	return i
}

func decodeRune(s string) (rune, int) {
	// Inline utf8 decode without importing unicode/utf8 for speed
	if len(s) == 0 {
		return 0, 0
	}
	b0 := s[0]
	if b0 < 0x80 {
		return rune(b0), 1
	}
	if len(s) >= 2 && b0&0xE0 == 0xC0 {
		return rune(b0&0x1F)<<6 | rune(s[1]&0x3F), 2
	}
	if len(s) >= 3 && b0&0xF0 == 0xE0 {
		return rune(b0&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), 3
	}
	if len(s) >= 4 && b0&0xF8 == 0xF0 {
		return rune(b0&0x07)<<18 | rune(s[1]&0x3F)<<12 | rune(s[2]&0x3F)<<6 | rune(s[3]&0x3F), 4
	}
	return rune(b0), 1
}

// lastANSI returns the last SGR ANSI sequence in s, or "" if none.
func lastANSI(s string) string {
	idx := strings.LastIndex(s, "\x1b[")
	if idx < 0 {
		return ""
	}
	end := strings.Index(s[idx:], "m")
	if end < 0 {
		return ""
	}
	return s[idx : idx+end+1]
}

// injectHighlightByCells highlights visual cells [startCell, endCell] inclusive
// in an ANSI-encoded line by slicing at exact byte offsets derived from
// MapCellToByteIndex and wrapping the slice with background highlight codes.
// Internal \x1b[0m resets inside the highlighted slice are patched to re-apply
// the selection background so highlight persists across color switches.
//
// Boundary semantics: startCell inclusive, endCell inclusive (last selected
// visual cell, 0-indexed). The exclusive byte offset is therefore at
// endCell+1 cells. If sel.EndColumn were already exclusive (boundary after
// selection), callers must pass endCell-1 or use exclusive variant; this
// function treats C_end as inclusive to avoid off-by-one trailing bleed.
func injectHighlightByCells(s string, startCell, endCell int, bg string) string {
	if startCell < 0 {
		startCell = 0
	}
	if endCell < startCell {
		return s
	}
	// Clamp to visual content width to prevent bleeding into adjacent cells
	// (e.g. selecting "the following" must stop at 'g' without including
	// the following space or 'c' of "commands:").
	plainWidth := StringCellWidth(ansi.Strip(s))
	if plainWidth == 0 {
		return s
	}
	if startCell >= plainWidth {
		return s
	}
	if endCell >= plainWidth {
		endCell = plainWidth - 1
	}
	if endCell < startCell {
		return s
	}
	startByte := MapCellToByteIndex(s, startCell)
	// endCell inclusive => exclusive byte at endCell+1 cells
	endByte := MapCellToByteIndex(s, endCell+1)
	if endByte > len(s) {
		endByte = len(s)
	}
	if startByte >= len(s) || startByte >= endByte {
		return s
	}
	before := s[:startByte]
	middle := s[startByte:endByte]
	after := s[endByte:]
	const reset = "\x1b[0m"
	restore := lastANSI(s[:startByte])
	// Preserve highlight across internal resets: re-apply bg after each \x1b[0m inside middle
	if strings.Contains(middle, "\x1b[0m") {
		middle = strings.ReplaceAll(middle, "\x1b[0m", "\x1b[0m"+bg)
	}
	return before + bg + middle + reset + restore + after
}

// renderDocumentWithSelection highlights DocumentLayout lines intersecting [Anchor,Cursor]
// using strict Cell-to-Byte index mapping for ANSI-styled text. It parses ANSI escapes
// character-by-character, tracks visual cells with runewidth, and slices at exact byte
// offsets to guarantee 100% pixel-perfect highlighting.
func (m *model) renderDocumentWithSelection() string {
	if m.docLayout == nil || m.docLayout.Len() == 0 {
		return ""
	}
	s, e := m.mouseSel.normalized()
	var b strings.Builder
	const selBg = "\x1b[48;2;42;34;64m"
	for idx, line := range m.docLayout.Lines {
		rendered := line.RenderedStr
		if rendered == "" {
			rendered = line.RawText
		}
		if idx >= s.Y && idx <= e.Y {
			// Determine gutter and content width for clamping
			gutter := 0
			for _, sp := range line.Spans {
				if sp.Selectable {
					gutter = sp.StartCell
					break
				}
			}
			if gutter == 0 && len(line.Spans) == 0 {
				stripped := ansi.Strip(rendered)
				if strings.HasPrefix(stripped, "│ ") {
					gutter = 2
					if strings.HasPrefix(stripped, "│ │ ") {
						gutter = 4
					}
				}
			}
			contentCells := 0
			raw := ansi.Strip(line.RawText)
			if raw == "" && line.RenderedStr != "" {
				raw = ansi.Strip(line.RenderedStr)
			}
			if raw != "" {
				contentCells = StringCellWidth(raw)
			} else {
				contentCells = StringCellWidth(ansi.Strip(rendered)) - gutter
				if contentCells < 0 {
					contentCells = 0
				}
			}
			renderedCells := StringCellWidth(ansi.Strip(rendered))
			var startCell, endCell int
			switch {
			case s.Y == e.Y:
				startCell = s.X
				endCell = e.X
			case idx == s.Y:
				startCell = s.X
				endCell = renderedCells - 1
				// Clamp end to content end
				if gutter+contentCells-1 < endCell {
					endCell = gutter + contentCells - 1
				}
			case idx == e.Y:
				startCell = gutter
				endCell = e.X
			default:
				startCell = gutter
				endCell = gutter + contentCells - 1
			}
			// Clamp to valid content range
			if startCell < gutter {
				startCell = gutter
			}
			if endCell < gutter {
				endCell = gutter
			}
			if contentCells > 0 {
				maxCell := gutter + contentCells - 1
				if startCell > maxCell {
					startCell = maxCell
				}
				if endCell > maxCell {
					endCell = maxCell
				}
			}
			if startCell <= endCell && renderedCells > 0 {
				rendered = injectHighlightByCells(rendered, startCell, endCell, selBg)
			}
		}
		b.WriteString(rendered)
		if idx < len(m.docLayout.Lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
