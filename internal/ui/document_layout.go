package ui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// LineKind identifies the semantic category of a document line.
type LineKind int

const (
	LineKindUserPrompt LineKind = iota
	LineKindAIResponse
	LineKindEngineTrace   // Internal logs, intents, phase changes, preflight
	LineKindTraceSummary  // Single "▸ Trace: ..." bar
	LineKindSystemError   // State transition errors, execution failures
)

func (k LineKind) String() string {
	switch k {
	case LineKindUserPrompt:
		return "UserPrompt"
	case LineKindAIResponse:
		return "AIResponse"
	case LineKindEngineTrace:
		return "EngineTrace"
	case LineKindTraceSummary:
		return "TraceSummary"
	case LineKindSystemError:
		return "SystemError"
	default:
		return "Unknown"
	}
}

// GlobalPos is a physical row + visual cell coordinate in the flattened
// rendered document (0-indexed, space-anchored).
type GlobalPos struct {
	Y int // Physical row index in the full flattened rendered document (0-indexed)
	X int // Visual cell column index accounting for CJK runewidth (0-indexed)
}

// RenderSpan describes a contiguous cell range on a single physical row and its
// mapping back to the source rune offsets in the line's raw string.
type RenderSpan struct {
	StartCell   int  // Visual cell start index on physical row
	EndCell     int  // Visual cell end index on physical row
	SourceStart int  // Rune start offset in line raw string
	SourceEnd   int  // Rune end offset in line raw string
	Selectable  bool // False for gutters/prefixes, True for text content
}

// DocumentLine is one physical terminal row in global document space.
type DocumentLine struct {
	GlobalY     int          // Row index in global document space
	Kind        LineKind     // Category of line for filtering and styling
	TurnID      uint64       // Turn identifier for turn-bound operations
	Spans       []RenderSpan // Cell spans for hit-testing and extraction
	RawText     string       // Plain text string without ANSI codes
	RenderedStr string       // ANSI-styled string for display
	RecordIdx   int          // Pointer to origin record (-1 for chrome)
}

// DocumentLayout is the global flat render document. It holds every physical
// row generated after word-wrapping with strictly sequential GlobalY indices.
type DocumentLayout struct {
	mu    sync.RWMutex
	Lines []DocumentLine
	width int // wrap width used to build layout
	// traceSummaryEmitted reports whether this layout already carries the
	// single per-turn quiet-mode "▸ Trace:" summary. It persists across
	// IncrementalLayoutUpdate merges so a `▸ Trace:` line is NEVER repeated
	// sequentially within a turn. Reset at each prompt submission.
	traceSummaryEmitted bool
	renderedTurns       map[uint64]bool
}

// Clone returns a shallow copy without copying the mutex.
func (d *DocumentLayout) Clone() DocumentLayout {
	d.mu.RLock()
	defer d.mu.RUnlock()
	lines := make([]DocumentLine, len(d.Lines))
	copy(lines, d.Lines)
	var renderedTurns map[uint64]bool
	if d.renderedTurns != nil {
		renderedTurns = make(map[uint64]bool, len(d.renderedTurns))
		for k, v := range d.renderedTurns {
			renderedTurns[k] = v
		}
	}
	return DocumentLayout{
		Lines:               lines,
		width:               d.width,
		traceSummaryEmitted: d.traceSummaryEmitted,
		renderedTurns:       renderedTurns,
	}
}

// ScreenToGlobal maps viewport-relative coordinates directly to GlobalPos.
// screenX/screenY are absolute terminal coordinates, yOffset is the viewport's
// YOffset, leftMargin/topMargin are the viewport geometry offsets.
// It is thread-safe and never panics.
func (d *DocumentLayout) ScreenToGlobal(screenX, screenY, yOffset, leftMargin, topMargin int) GlobalPos {
	d.mu.RLock()
	defer d.mu.RUnlock()
	relY := screenY - topMargin
	relX := screenX - leftMargin
	if relY < 0 {
		relY = 0
	}
	if relX < 0 {
		relX = 0
	}
	globalY := yOffset + relY
	if len(d.Lines) > 0 {
		if globalY < 0 {
			globalY = 0
		}
		if globalY >= len(d.Lines) {
			globalY = len(d.Lines) - 1
		}
		if globalY < 0 {
			globalY = 0
		}
	} else if globalY < 0 {
		globalY = 0
	}
	return GlobalPos{Y: globalY, X: relX}
}

// VisibleSlice returns the visible window Lines[yOffset : yOffset+height] clamped.
func (d *DocumentLayout) VisibleSlice(yOffset, height int) []DocumentLine {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.Lines) == 0 || height <= 0 {
		return nil
	}
	if yOffset < 0 {
		yOffset = 0
	}
	if yOffset >= len(d.Lines) {
		return nil
	}
	end := yOffset + height
	if end > len(d.Lines) {
		end = len(d.Lines)
	}
	out := make([]DocumentLine, end-yOffset)
	copy(out, d.Lines[yOffset:end])
	return out
}

// Slice returns the rendered strings of the visible window
// [yOffset, yOffset+height), clamped to the document. It is the single slicing
// primitive used by the manual-slicing viewport contract: the caller passes
// exactly these lines to Viewport.SetContent and resets Viewport.YOffset to 0
// so the bubbles viewport can never double-scroll or middle-jump.
func (d *DocumentLayout) Slice(yOffset, height int) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.Lines) == 0 || height <= 0 {
		return nil
	}
	if yOffset < 0 {
		yOffset = 0
	}
	if yOffset >= len(d.Lines) {
		return nil
	}
	end := yOffset + height
	if end > len(d.Lines) {
		end = len(d.Lines)
	}
	out := make([]string, 0, end-yOffset)
	for i := yOffset; i < end; i++ {
		out = append(out, d.Lines[i].RenderedStr)
	}
	return out
}

// ExtractText extracts visible text matching selected cell bounds across lines
// [startY : endY] with pure geometry. It strips ANSI escape codes but does not
// drop chrome/log lines. What is visually selected is copied.
// start and end are normalized internally so order does not matter.
func (d *DocumentLayout) ExtractText(start, end GlobalPos) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.Lines) == 0 {
		return ""
	}
	s, e := normalizeGlobalPos(start, end)
	if s.Y < 0 {
		s.Y = 0
	}
	if e.Y >= len(d.Lines) {
		e.Y = len(d.Lines) - 1
	}
	if s.Y > e.Y {
		return ""
	}
	var b strings.Builder
	for y := s.Y; y <= e.Y; y++ {
		line := d.Lines[y]
		raw := ansi.Strip(line.RawText)
		// Also strip ANSI from RenderedStr fallback if RawText empty but RenderedStr has content
		if raw == "" && line.RenderedStr != "" {
			raw = ansi.Strip(line.RenderedStr)
		}
		switch {
		case s.Y == e.Y:
			// Single line: slice between s.X and e.X inclusive
			b.WriteString(sliceByCells(raw, s.X, e.X+1, line.Spans))
		case y == s.Y:
			// First line: from s.X to end of line
			b.WriteString(sliceByCells(raw, s.X, -1, line.Spans))
		case y == e.Y:
			// Last line: from 0 to e.X inclusive
			b.WriteString(sliceByCells(raw, 0, e.X+1, line.Spans))
		default:
			// Middle lines: full line
			b.WriteString(raw)
		}
		if y < e.Y {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// Len returns number of physical rows.
func (d *DocumentLayout) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.Lines)
}

// Width returns wrap width.
func (d *DocumentLayout) Width() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.width
}

// SetLines replaces lines (thread-safe).
func (d *DocumentLayout) SetLines(lines []DocumentLine, width int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Lines = lines
	d.width = width
}

// AppendLines appends lines incrementally (thread-safe) for streaming updates.
func (d *DocumentLayout) AppendLines(lines []DocumentLine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Reassign GlobalY sequentially
	base := len(d.Lines)
	for i := range lines {
		lines[i].GlobalY = base + i
	}
	d.Lines = append(d.Lines, lines...)
}

// normalizeGlobalPos orders two GlobalPos so s <= e in (Y,X) tuple order.
func normalizeGlobalPos(a, b GlobalPos) (GlobalPos, GlobalPos) {
	if a.Y > b.Y || (a.Y == b.Y && a.X > b.X) {
		return b, a
	}
	return a, b
}

// sliceByCells returns substring of s between cell columns [startCell, endCell)
// where endCell is exclusive and -1 means until end of line. It accounts for
// CJK double-width runes via runewidth.RuneWidth and never splits a wide rune.
func sliceByCells(s string, startCell, endCell int, spans []RenderSpan) string {
	if s == "" {
		return ""
	}
	// If spans are provided and have selectable geometry, use them to adjust for gutters.
	// For now we treat s as already content-only (gutter stripped), so spans with
	// Selectable=false at start offset the cell mapping.
	gutterCells := 0
	if len(spans) > 0 {
		// Find first selectable span's StartCell as gutter offset
		for _, sp := range spans {
			if sp.Selectable {
				gutterCells = sp.StartCell
				break
			}
		}
		// Adjust start/end to be relative to content origin if they include gutter
		// If startCell < gutterCells, clamp to gutterCells (content start)
		if startCell < gutterCells {
			startCell = gutterCells
		}
		if endCell >= 0 && endCell < gutterCells {
			return ""
		}
		// Translate to content-relative cells
		startCell -= gutterCells
		if endCell >= 0 {
			endCell -= gutterCells
		}
	}
	if startCell < 0 {
		startCell = 0
	}
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	// Map cell offset to rune index
	startRune := cellToRuneIdxRunes(runes, startCell)
	var endRune int
	if endCell < 0 {
		endRune = len(runes)
	} else {
		endRune = cellToRuneIdxRunes(runes, endCell)
	}
	if startRune < 0 {
		startRune = 0
	}
	if startRune > len(runes) {
		startRune = len(runes)
	}
	if endRune < 0 {
		endRune = 0
	}
	if endRune > len(runes) {
		endRune = len(runes)
	}
	if startRune >= endRune {
		return ""
	}
	return string(runes[startRune:endRune])
}

// cellToRuneIdxRunes converts a visual cell offset to rune index using per-rune widths.
func cellToRuneIdxRunes(runes []rune, targetCells int) int {
	if targetCells <= 0 {
		return 0
	}
	cells := 0
	for i, r := range runes {
		w := runewidth.RuneWidth(r)
		if w <= 0 {
			w = 1
		}
		if cells >= targetCells {
			return i
		}
		if cells+w > targetCells {
			// Inside a wide rune: return start of that rune (do not split)
			return i
		}
		cells += w
		if cells >= targetCells {
			return i + 1
		}
	}
	return len(runes)
}

// StringCellWidth returns visual cell width of s using runewidth.
func StringCellWidth(s string) int {
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if rw > 0 {
			w += rw
		} else {
			w++
		}
	}
	return w
}

// SliceByCells is exported cell-aware slicing for testing.
func SliceByCells(s string, startCell, endCell int) string {
	return sliceByCells(s, startCell, endCell, nil)
}
