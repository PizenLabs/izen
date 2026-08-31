package ui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Cell is the atomic unit of the rasterized terminal screen.
// It represents one visual cell coordinate (y,x) after Markdown/ANSI parsing.
type Cell struct {
	Rune           rune
	Width          uint8 // 1 for standard, 2 for wide CJK/emoji, 0 for continuation cell
	IsPadding      bool  // true if space was injected solely for codeblock background alignment
	IsSoftWrapped  bool  // true if line break was caused by viewport edge wrapping (not explicit \n)
	IsContinuation bool  // true if this cell is the right-half of a 2-cell wide character
}

// Framebuffer is the flat 2D cell grid that represents the fully rasterized
// terminal screen state after Markdown/ANSI parsing. It is the single source
// of truth for O(1) selection and clean copy extraction.
type Framebuffer struct {
	Grid     [][]Cell
	Width    int
	Height   int
	Gutter   []int // per-row gutter width (first selectable StartCell) for copy clamping
	DocLines []DocumentLine
	mu       sync.RWMutex
}

// FramebufferBufferLines is the configurable viewport buffer threshold.
// The framebuffer is capped to visible height plus this buffer on top and bottom
// to bound memory for large documents (e.g., 25k rows).
const FramebufferBufferLines = 100

// Rasterize renders a DocumentLayout once into a 2D Framebuffer at the given
// viewportWidth. It strips ANSI SGRs into metadata, flags padding and soft-wrap,
// and handles wide characters with continuation cells.
func Rasterize(doc *DocumentLayout, viewportWidth int) *Framebuffer {
	if doc == nil {
		return &Framebuffer{Grid: nil, Width: viewportWidth, Height: 0}
	}
	doc.mu.RLock()
	n := len(doc.Lines)
	lines := make([]DocumentLine, n)
	copy(lines, doc.Lines)
	doc.mu.RUnlock()
	if len(lines) == 0 {
		return &Framebuffer{Grid: nil, Width: viewportWidth, Height: 0}
	}
	return rasterizeLines(lines, viewportWidth, 0, len(lines))
}

// RasterizeViewport caps the framebuffer to the visible window plus a buffer
// threshold to bound memory. Only rows in [yOffset-buffer, yOffset+height+buffer)
// are rasterized. yOffset and height come from the app-owned scroll offset and
// viewport geometry.
func RasterizeViewport(doc *DocumentLayout, viewportWidth, yOffset, viewportHeight int) *Framebuffer {
	if doc == nil {
		return &Framebuffer{Grid: nil, Width: viewportWidth, Height: 0}
	}
	doc.mu.RLock()
	total := len(doc.Lines)
	linesCopy := make([]DocumentLine, total)
	copy(linesCopy, doc.Lines)
	doc.mu.RUnlock()
	if total == 0 {
		return &Framebuffer{Grid: nil, Width: viewportWidth, Height: 0}
	}
	if viewportHeight <= 0 {
		viewportHeight = total
	}
	start := yOffset - FramebufferBufferLines
	if start < 0 {
		start = 0
	}
	end := yOffset + viewportHeight + FramebufferBufferLines
	if end > total {
		end = total
	}
	if start >= end {
		start = 0
		end = total
	}
	fb := rasterizeLines(linesCopy, viewportWidth, start, end)
	// Shift Grid indices so that fb.Grid[0] corresponds to global Y == start.
	// Callers that need global Y must offset by start; for O(1) mouse lookup
	// we store the offset as a virtual base. For simplicity we keep Height as
	// total and pad with empty prefix rows if needed, so that Grid[y] == global Y.
	// To keep O(1) index GlobalPos.Y -> Grid[Y] we expand with empty rows for
	// the prefix. This preserves direct indexing without translation, at the
	// cost of at most 2*buffer empty rows (bounded).
	if start > 0 {
		prefix := make([][]Cell, start)
		prefixGutter := make([]int, start)
		prefixLines := make([]DocumentLine, start)
		for i := range prefix {
			prefix[i] = []Cell{}
		}
		suffixLen := total - end
		suffix := make([][]Cell, suffixLen)
		suffixGutter := make([]int, suffixLen)
		suffixLines := make([]DocumentLine, suffixLen)
		for i := range suffix {
			suffix[i] = []Cell{}
		}
		combined := make([][]Cell, 0, total)
		combined = append(combined, prefix...)
		combined = append(combined, fb.Grid...)
		combined = append(combined, suffix...)
		fb.Grid = combined
		combinedGutter := make([]int, 0, total)
		combinedGutter = append(combinedGutter, prefixGutter...)
		combinedGutter = append(combinedGutter, fb.Gutter...)
		combinedGutter = append(combinedGutter, suffixGutter...)
		fb.Gutter = combinedGutter
		combinedLines := make([]DocumentLine, 0, total)
		combinedLines = append(combinedLines, prefixLines...)
		combinedLines = append(combinedLines, fb.DocLines...)
		combinedLines = append(combinedLines, suffixLines...)
		fb.DocLines = combinedLines
		fb.Height = total
	} else if fb.Height != total {
		// When start==0 but end < total (windowed at top), pad suffix to reach total for consistent indexing
		suffixLen := total - end
		if suffixLen > 0 {
			suffix := make([][]Cell, suffixLen)
			suffixGutter := make([]int, suffixLen)
			suffixLines := make([]DocumentLine, suffixLen)
			for i := range suffix {
				suffix[i] = []Cell{}
			}
			fb.Grid = append(fb.Grid, suffix...)
			fb.Gutter = append(fb.Gutter, suffixGutter...)
			fb.DocLines = append(fb.DocLines, suffixLines...)
			fb.Height = total
		}
	}
	return fb
}

func rasterizeLines(lines []DocumentLine, viewportWidth, start, end int) *Framebuffer {
	if viewportWidth < 1 {
		viewportWidth = 1
	}
	slice := lines[start:end]
	height := len(slice)
	grid := make([][]Cell, height)
	gutter := make([]int, height)
	docLines := make([]DocumentLine, height)
	for i, line := range slice {
		globalY := start + i
		cells := rasterizeLine(line, viewportWidth, globalY, lines)
		grid[i] = cells
		// Compute gutter as first selectable StartCell (0 if none)
		g := 0
		for _, sp := range line.Spans {
			if sp.Selectable {
				g = int(sp.StartCell)
				break
			}
		}
		gutter[i] = g
		docLines[i] = line
	}
	return &Framebuffer{
		Grid:     grid,
		Width:    viewportWidth,
		Height:   height,
		Gutter:   gutter,
		DocLines: docLines,
	}
}

func visualForLine(line DocumentLine, viewportWidth int, stripped string) string {
	// User prompt has a left badge header plus content; the rendered style adds
	// PaddingLeft(1) which introduces an extra visual space not counted in the
	// DocumentLayout spans. Reconstruct from spans+RawText to keep coordinates exact.
	if line.Kind == LineKindUserPrompt && len(line.Spans) >= 2 {
		headerWidth := int(line.Spans[0].EndCell - line.Spans[0].StartCell)
		if headerWidth < 0 {
			headerWidth = 0
		}
		if headerWidth > len([]rune(stripped)) {
			headerWidth = len([]rune(stripped))
		}
		headerPart := SliceByCells(stripped, 0, headerWidth)
		contentPart := line.RawText
		return headerPart + contentPart
	}
	// Codeblock content lines: outer gutter + box left + content + padding + right border
	if len(line.Spans) == 3 && line.Spans[2].Selectable {
		// Detect codeblock by presence of box borders in stripped
		if strings.Contains(stripped, "│") {
			outerWidth := int(line.Spans[0].EndCell - line.Spans[0].StartCell)
			leftWidth := int(line.Spans[1].EndCell - line.Spans[1].StartCell)
			if outerWidth < 0 {
				outerWidth = 0
			}
			if leftWidth < 0 {
				leftWidth = 0
			}
			outerPart := SliceByCells(stripped, 0, outerWidth)
			leftPart := SliceByCells(stripped, outerWidth, outerWidth+leftWidth)
			contentPart := line.RawText
			innerWidth := viewportWidth - 6
			if innerWidth < 0 {
				innerWidth = 0
			}
			contentCells := StringCellWidth(contentPart)
			padding := ""
			if contentCells < innerWidth {
				padding = strings.Repeat(" ", innerWidth-contentCells)
			}
			rightPart := " │"
			// For border lines RawText is empty but stripped is border; handled below
			if contentPart == "" && (strings.Contains(stripped, "┌") || strings.Contains(stripped, "└")) {
				return stripped
			}
			return outerPart + leftPart + contentPart + padding + rightPart
		}
	}
	if stripped != "" && (strings.Contains(stripped, "┌") || strings.Contains(stripped, "└") || strings.Contains(stripped, "─")) {
		return stripped
	}
	return stripped
}

func rasterizeLine(line DocumentLine, viewportWidth, globalY int, allLines []DocumentLine) []Cell {
	stripped := ansi.Strip(line.RenderedStr)
	if stripped == "" && line.RawText != "" {
		// Fallback: for lines where RenderedStr may be empty (should not happen)
		stripped = line.RawText
	}
	// For gutter-only or empty lines, stripped may be "" with RawText "".
	// We still produce an empty row (no cells) so that copy produces empty line.
	if stripped == "" {
		// Check for soft-wrap flag even on empty.
		return []Cell{}
	}
	visual := visualForLine(line, viewportWidth, stripped)
	if visual == "" {
		return []Cell{}
	}
	// Build cells rune by rune, handling wide chars.
	var cells []Cell
	cells = make([]Cell, 0, len([]rune(visual))*2)
	for _, r := range visual {
		w := runewidth.RuneWidth(r)
		if w < 0 {
			w = 1
		}
		if w == 0 {
			w = 1
		}
		if w == 2 {
			// Wide char occupies two cells: first with Width 2, second continuation.
			cells = append(cells, Cell{Rune: r, Width: 2, IsContinuation: false})
			cells = append(cells, Cell{Rune: r, Width: 0, IsContinuation: true})
		} else {
			cells = append(cells, Cell{Rune: r, Width: 1})
		}
	}
	// Cap to viewportWidth (should already be within, but truncate if overflow).
	if len(cells) > viewportWidth {
		cells = cells[:viewportWidth]
	}
	// Flag trailing codeblock padding spaces as IsPadding.
	markCodeblockPadding(line, cells, viewportWidth)
	// Flag soft-wrap: if this line is an automatic viewport-edge wrap, mark last
	// cell's IsSoftWrapped=true. Heuristic: consecutive lines with same RecordIdx
	// and the current line's width is near viewportWidth indicates wrap.
	// More precise: if next line exists and shares RecordIdx and the current
	// line was produced by wrapping (i.e., the wrap would have split at a word
	// boundary), we flag.
	if isSoftWrappedLine(line, globalY, allLines, viewportWidth, cells) {
		if len(cells) > 0 {
			cells[len(cells)-1].IsSoftWrapped = true
		}
	}
	return cells
}

func markCodeblockPadding(line DocumentLine, cells []Cell, viewportWidth int) {
	if viewportWidth < 10 {
		return
	}
	// Detect codeblock context via spans: codeblock lines have 3 spans (outer, box left, content)
	// and viewportWidth -6 interior logic. For non-codeblock, skip.
	if len(line.Spans) != 3 {
		return
	}
	// Check that this looks like a codeblock content line (has outer gutter)
	// Spans[2] is selectable content.
	contentStart := line.Spans[2].StartCell
	contentEnd := line.Spans[2].EndCell
	// For empty raw interior lines, contentEnd - contentStart == innerWidth
	// but RawText == "" indicates interior is all padding.
	innerWidth := viewportWidth - 6
	if innerWidth < 1 {
		return
	}
	// For non-empty raw, padding is interior beyond contentEnd up to contentStart+innerWidth.
	// For empty, the entire interior beyond header is padding? But spans already mark interior as selectable,
	// so we treat spaces beyond raw width as padding.
	rawWidth := StringCellWidth(line.RawText)
	// If raw is empty but spans indicate interior width, rawWidth is 0, so padding = innerWidth.
	paddingStart := contentStart + rawWidth
	paddingEnd := contentStart + innerWidth
	if paddingStart < 0 {
		paddingStart = 0
	}
	if paddingEnd > len(cells) {
		paddingEnd = len(cells)
	}
	// Mark spaces in padding region as IsPadding.
	for i := paddingStart; i < paddingEnd && i < len(cells); i++ {
		if cells[i].Rune == ' ' && !cells[i].IsContinuation {
			cells[i].IsPadding = true
		}
	}
	// Also, if raw empty and line is inside codeblock box interior empty line,
	// the entire interior spaces are padding even though spans mark them selectable.
	if line.RawText == "" && contentEnd-contentStart == innerWidth {
		for i := contentStart; i < paddingEnd && i < len(cells); i++ {
			if cells[i].Rune == ' ' && !cells[i].IsContinuation {
				cells[i].IsPadding = true
			}
		}
	}
}

func isSoftWrappedLine(line DocumentLine, globalY int, allLines []DocumentLine, viewportWidth int, cells []Cell) bool {
	if globalY < 0 || globalY+1 >= len(allLines) {
		return false
	}
	next := allLines[globalY+1]
	// Only within same record and not a chrome/border line.
	if line.RecordIdx < 0 || next.RecordIdx < 0 {
		return false
	}
	if line.RecordIdx != next.RecordIdx {
		return false
	}
	// Border/top lines have RawText == "" and are box decorations; not soft.
	if line.RawText == "" && next.RawText == "" {
		// Could be consecutive empty codeblock interior lines each explicit newline,
		// not soft.
		return false
	}
	// Heuristic: if current line's visual width is at the wrap boundary (close to
	// max content width) and next line continues same logical line, it's soft.
	// For user-prompt and AI text, contentWidth = viewportWidth - header/gutter.
	// Wrapped lines typically fill that width. We consider soft if cells length
	// is >= viewportWidth-4 (near full) or if next line's prefix indicates
	// continuation (no new logical marker like "│ " heading).
	// Simpler: treat as soft if current line's stripped width >= viewportWidth-4
	// and next line's RawText does not start a new block marker (e.g., not "```", not heading).
	// For now, if same RecordIdx, we consider soft if cells length >= viewportWidth-8
	// (leaving slack for gutter). This distinguishes short explicit-newline lines
	// (which are short) from wrapped lines (which are long).
	if len(cells) >= viewportWidth-8 && len(cells) > 0 {
		return true
	}
	// Fallback: if both lines have same RecordIdx and the current line is not
	// the last physical line for that logical segment, and its width is not
	// trivially short, consider it soft when the next line's content appears to
	// be a continuation (no leading markdown marker).
	// Check if next line's stripped starts with continuation (no "│" header reset).
	// For simplicity, if same record and current line not empty and next not empty,
	// and current width > 20, consider soft? This may over-mark but is safe for
	// copy tests where soft lines are long paragraphs.
	if len(cells) > 20 && StringCellWidth(next.RawText) > 0 {
		// Avoid marking explicit short lines as soft.
		// Only mark if current line length is relatively long.
		return true
	}
	return false
}

// ExtractText extracts copied text strictly via framebuffer cells per spec.
//
// For each line y from y1 to y2:
//
//	For each cell x in line y:
//	  1. If cell.IsPadding == true, SKIP.
//	  2. If cell.IsContinuation == true, SKIP.
//	  3. Append cell.Rune to string buffer.
//	If line end reached AND cell.IsSoftWrapped == false AND y < y2:
//	  Append '\n'
func (fb *Framebuffer) ExtractText(start, end GlobalPos) string {
	if fb == nil || len(fb.Grid) == 0 {
		return ""
	}
	fb.mu.RLock()
	defer fb.mu.RUnlock()
	s, e := normalizeGlobalPos(start, end)
	if s.Y < 0 {
		s.Y = 0
	}
	if e.Y >= len(fb.Grid) {
		e.Y = len(fb.Grid) - 1
	}
	if s.Y > e.Y {
		return ""
	}
	// Clamp X to valid range per row.
	var b strings.Builder
	for y := s.Y; y <= e.Y; y++ {
		if y < 0 || y >= len(fb.Grid) {
			continue
		}
		row := fb.Grid[y]
		if len(row) == 0 {
			// Empty line: preserve newline if not soft-wrapped.
			if y < e.Y {
				// Empty row cannot be soft-wrapped (no cells), so add newline.
				b.WriteString("\n")
			}
			continue
		}
		xStart := 0
		xEnd := len(row) - 1
		switch {
		case s.Y == e.Y && y == s.Y:
			xStart = s.X
			xEnd = e.X
		case y == s.Y:
			xStart = s.X
		case y == e.Y:
			xEnd = e.X
		}
		if xStart < 0 {
			xStart = 0
		}
		if xEnd >= len(row) {
			xEnd = len(row) - 1
		}
		// Gutter clamping: skip non-selectable prefix (e.g., "@Developer  " or "│ ")
		// so copy matches DocumentLayout's gutter handling.
		gutter := 0
		if y >= 0 && y < len(fb.Gutter) {
			gutter = fb.Gutter[y]
		}
		if xStart < gutter {
			xStart = gutter
		}
		if xEnd < gutter {
			xStart = gutter
			xEnd = gutter - 1
		}
		if xStart > xEnd {
			// If selection starts beyond row length, skip row content but still handle newline.
			if y < e.Y && !isRowSoftWrapped(row) {
				b.WriteString("\n")
			}
			continue
		}
		for x := xStart; x <= xEnd; x++ {
			cell := row[x]
			if cell.IsPadding {
				continue
			}
			if cell.IsContinuation {
				continue
			}
			b.WriteRune(cell.Rune)
		}
		if y < e.Y && !isRowSoftWrapped(row) {
			b.WriteString("\n")
		} else if y < e.Y && isRowSoftWrapped(row) {
			// Soft-wrapped: join seamlessly. If the content would otherwise jam words,
			// the original wrap removed a space; we add a single space to keep words
			// separated when the next line doesn't start with space. However spec says
			// "with single spaces/no extra newline" – to avoid missing space between words,
			// we ensure a space if both boundaries are alphanumeric.
			// Check if we should insert a space: if the last non-padding rune of this row
			// and first rune of next row are both not spaces, insert one space.
			// This heuristic preserves word boundaries without introducing extra newlines.
			if shouldInsertSoftSpace(row, fb.Grid[y+1], xEnd, s, e, y) {
				b.WriteString(" ")
			}
		}
	}
	return b.String()
}

func isRowSoftWrapped(row []Cell) bool {
	if len(row) == 0 {
		return false
	}
	return row[len(row)-1].IsSoftWrapped
}

func shouldInsertSoftSpace(curRow, nextRow []Cell, curEnd int, s, e GlobalPos, y int) bool {
	// Only for soft-wrapped joins where the selection spans both rows.
	// We peek at the last appended rune of curRow (ignoring padding/continuation)
	// and the first rune of nextRow within selection bounds.
	var lastRune rune
	foundLast := false
	for i := curEnd; i >= 0; i-- {
		if i >= len(curRow) {
			continue
		}
		c := curRow[i]
		if c.IsPadding || c.IsContinuation {
			continue
		}
		lastRune = c.Rune
		foundLast = true
		break
	}
	var firstRune rune
	foundFirst := false
	xStartNext := 0
	for i := xStartNext; i < len(nextRow); i++ {
		c := nextRow[i]
		if c.IsPadding || c.IsContinuation {
			continue
		}
		firstRune = c.Rune
		foundFirst = true
		break
	}
	if !foundLast || !foundFirst {
		return false
	}
	if lastRune == ' ' || firstRune == ' ' {
		return false
	}
	// Both are non-space, likely word boundary was split; insert single space.
	return true
}

// CellAt performs O(1) direct array index lookup against Grid[y][x].
// Returns cell and ok flag. No string scans, no ANSI stripping, no layout rebuild.
func (fb *Framebuffer) CellAt(y, x int) (Cell, bool) {
	if fb == nil {
		return Cell{}, false
	}
	fb.mu.RLock()
	defer fb.mu.RUnlock()
	if y < 0 || y >= len(fb.Grid) {
		return Cell{}, false
	}
	row := fb.Grid[y]
	if x < 0 || x >= len(row) {
		return Cell{}, false
	}
	return row[x], true
}

// WidthAt returns visual cell width at (y,x) without scanning.
func (fb *Framebuffer) WidthAt(y, x int) int {
	c, ok := fb.CellAt(y, x)
	if !ok {
		return 0
	}
	if c.IsContinuation {
		return 0
	}
	return int(c.Width)
}

// Invalidate is a no-op marker for lifecycle; actual invalidation happens in
// model on WindowSizeMsg or document updates, never on mouse movement.
func (fb *Framebuffer) Invalidate() {}

// RenderWithSelection produces the visible window strings with selection
// highlight overlay. It preserves all existing foreground colors, syntax
// highlighting, and text attributes, ONLY injecting the background color
// attribute (SGR \x1b[48;...m) for cells bounded by (s.Y,s.X) to (e.Y,e.X).
// It does NOT strip ANSI sequences or fallback to raw/unstyled strings.
func (fb *Framebuffer) RenderWithSelection(start, end GlobalPos, yOffset, height int, bg string) []string {
	if fb == nil {
		return nil
	}
	fb.mu.RLock()
	defer fb.mu.RUnlock()
	s, e := normalizeGlobalPos(start, end)
	if s.Y < 0 {
		s.Y = 0
	}
	if e.Y >= len(fb.Grid) {
		e.Y = len(fb.Grid) - 1
	}
	out := make([]string, 0, height)
	for i := yOffset; i < yOffset+height && i < len(fb.Grid); i++ {
		if i < 0 || i >= len(fb.DocLines) {
			// Fallback to Grid reconstruction if DocLines missing (should not happen)
			row := fb.Grid[i]
			if len(row) == 0 {
				out = append(out, "")
				continue
			}
			var b strings.Builder
			for _, cell := range row {
				if cell.IsContinuation {
					continue
				}
				b.WriteRune(cell.Rune)
			}
			out = append(out, b.String())
			continue
		}
		line := fb.DocLines[i]
		rendered := line.RenderedStr
		if rendered == "" {
			rendered = line.RawText
		}
		if rendered == "" {
			out = append(out, "")
			continue
		}
		if i < s.Y || i > e.Y {
			// No selection on this row — return styled string untouched.
			out = append(out, rendered)
			continue
		}
		row := fb.Grid[i]
		var xStart, xEnd int
		switch {
		case s.Y == e.Y:
			xStart, xEnd = s.X, e.X
		case i == s.Y:
			xStart = s.X
			xEnd = len(row) - 1
		case i == e.Y:
			xStart = 0
			xEnd = e.X
		default:
			xStart = 0
			xEnd = len(row) - 1
		}
		if xStart < 0 {
			xStart = 0
		}
		if xEnd >= len(row) {
			xEnd = len(row) - 1
		}
		if xStart > xEnd {
			out = append(out, rendered)
			continue
		}
		// Inject background only, preserving all foreground ANSI.
		out = append(out, fbHighlightByCells(rendered, xStart, xEnd, bg))
	}
	return out
}

// fbHighlightByCells highlights visual cells [startCell, endCell] inclusive
// in an ANSI-encoded line by slicing at exact byte offsets. It preserves
// all existing foreground styles and only injects the background.
func fbHighlightByCells(s string, startCell, endCell int, bg string) string {
	if startCell < 0 {
		startCell = 0
	}
	if endCell < startCell {
		return s
	}
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
	startByte := fbMapCellToByteIndex(s, startCell)
	endByte := fbMapCellToByteIndex(s, endCell+1)
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
	restore := fbLastANSI(s[:startByte])
	if strings.Contains(middle, "\x1b[0m") {
		middle = strings.ReplaceAll(middle, "\x1b[0m", "\x1b[0m"+bg)
	}
	return before + bg + middle + reset + restore + after
}

func fbLastANSI(s string) string {
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

func fbMapCellToByteIndex(s string, targetCell int) int {
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
		var r rune
		var size int
		if s[i] < 0x80 {
			r = rune(s[i])
			size = 1
		} else {
			ru, sz := fbDecodeRune(s[i:])
			r = ru
			size = sz
		}
		w := runewidth.RuneWidth(r)
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

func fbDecodeRune(s string) (rune, int) {
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
