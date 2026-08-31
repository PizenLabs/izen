package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// roleHeader maps the internal record role to the canonical copy header.
// The headers are stable, human-readable, and presentation-independent.
// They deliberately match the spec's example vocabulary (USER, ASSISTANT, TOOL,
// ERROR, EXECUTION) while preserving every role that exists in the model.
func roleHeader(r role) string {
	switch r {
	case roleUser:
		return "USER"
	case roleAI:
		return "ASSISTANT"
	case roleError:
		return "ERROR"
	case roleSystem:
		return "TOOL"
	case roleStatus:
		return "EXECUTION"
	case roleActivity:
		return "ACTIVITY"
	case roleCode:
		return "CODE"
	default:
		return "SYSTEM"
	}
}

// SerializeTranscript renders the canonical conversation transcript as
// deterministic plain text for clipboard copy.
//
// Guarantees:
//   - No ANSI escape sequences (stripped via ansi.Strip).
//   - No dependence on viewport position, terminal width, wrapping, or styling.
//   - No spinner frames or transient UI artifacts (records never contain them).
//   - Code blocks and multiline tool output are preserved verbatim, line-by-line.
//   - Each logical record is rendered exactly once with a stable header boundary.
//   - Deterministic: same records always produce the same string.
func SerializeTranscript(records []record) string {
	if len(records) == 0 {
		return ""
	}
	var b strings.Builder
	first := true
	for _, rec := range records {
		header := roleHeader(rec.role)
		// Strip ANSI and normalize line endings.
		content := ansi.Strip(rec.text)
		content = strings.ReplaceAll(content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		// Trim a single trailing newline so the separator logic is deterministic.
		// Internal blank lines and indentation (code blocks) are preserved.
		content = strings.TrimRight(content, "\n")
		if strings.TrimSpace(content) == "" {
			continue
		}
		if !first {
			b.WriteString("\n\n")
		}
		first = false
		b.WriteString(header)
		b.WriteString("\n")
		b.WriteString(content)
	}
	return b.String()
}

// normalizeCopiedText guarantees clipboard output is plain text without ANSI.
func normalizeCopiedText(s string) string {
	return ansi.Strip(s)
}

// extractFromFramebuffer implements the spec's clean clipboard extraction
// pseudocode strictly by iterating through bounded cells in Framebuffer.Grid:
//
//	For each line y from y1 to y2:
//	  For each cell x in line y:
//	    1. If cell.IsPadding == true, SKIP.
//	    2. If cell.IsContinuation == true, SKIP.
//	    3. Append cell.Rune to string buffer.
//	  If line end reached AND cell.IsSoftWrapped == false AND y < y2:
//	    Append '\n' to string buffer.
//
// Soft-wrapped lines join seamlessly (single spaces/no extra newline) while
// explicit Markdown newlines preserve '\n'.
func extractFromFramebuffer(fb *Framebuffer, y1, x1, y2, x2 int) string {
	if fb == nil || len(fb.Grid) == 0 {
		return ""
	}
	// Normalize ordering so (y1,x1) <= (y2,x2) in tuple order.
	if y1 > y2 || (y1 == y2 && x1 > x2) {
		y1, y2 = y2, y1
		x1, x2 = x2, x1
	}
	if y1 < 0 {
		y1 = 0
	}
	if y2 >= len(fb.Grid) {
		y2 = len(fb.Grid) - 1
	}
	if y1 > y2 {
		return ""
	}
	var b strings.Builder
	for y := y1; y <= y2; y++ {
		if y < 0 || y >= len(fb.Grid) {
			continue
		}
		row := fb.Grid[y]
		if len(row) == 0 {
			if y < y2 {
				// Empty row is treated as hard break (no IsSoftWrapped).
				b.WriteString("\n")
			}
			continue
		}
		xStart := 0
		xEnd := len(row) - 1
		switch {
		case y1 == y2 && y == y1:
			xStart = x1
			xEnd = x2
		case y == y1:
			xStart = x1
		case y == y2:
			xEnd = x2
		}
		if xStart < 0 {
			xStart = 0
		}
		if xEnd >= len(row) {
			xEnd = len(row) - 1
		}
		// Gutter clamp: skip non-selectable prefix
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
			// Selection starts beyond row length: handle soft-wrap newline.
			if y < y2 && len(row) > 0 && !row[len(row)-1].IsSoftWrapped {
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
		if y < y2 {
			isSoft := false
			if len(row) > 0 {
				isSoft = row[len(row)-1].IsSoftWrapped
			}
			if !isSoft {
				b.WriteString("\n")
			} else if shouldInsertSoftSpaceForCopy(row, fb.Grid[y+1], xEnd) {
				// Soft-wrapped: join seamlessly with single space to preserve word boundary
				b.WriteString(" ")
			}
		}
	}
	return b.String()
}

func shouldInsertSoftSpaceForCopy(curRow, nextRow []Cell, curEnd int) bool {
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
	for i := 0; i < len(nextRow); i++ {
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
	return true
}

// extractSelectionViaFramebuffer is the model-bound wrapper that extracts the
// current mouse selection via the framebuffer grid (O(1) per cell, no string scans).
func (m *model) extractSelectionViaFramebuffer() string {
	if m.framebuffer == nil || len(m.framebuffer.Grid) == 0 || !m.mouseSel.Active {
		return ""
	}
	s, e := m.mouseSel.normalized()
	return extractFromFramebuffer(m.framebuffer, s.Y, s.X, e.Y, e.X)
}

// handleCopy executes the /copy command: serializes the complete canonical
// transcript and writes it to the system clipboard.
//
// It is safe to execute repeatedly, never mutates conversation state, and
// surfaces a transient footer notice on success or failure without inserting a
// permanent record (so the notice is never recursively copied).
func (m *model) handleCopy() {
	serialized := normalizeCopiedText(SerializeTranscript(m.records))
	if strings.TrimSpace(serialized) == "" {
		m.setToast("Nothing to copy — conversation is empty")
		return
	}
	var err error
	if m.clipboard != nil {
		err = m.clipboard.WriteAll(serialized)
	} else {
		err = clipboardWriteAll(serialized)
	}
	if err != nil {
		m.setToast("Failed to copy conversation: clipboard unavailable")
		return
	}
	m.setToast("Copied conversation to clipboard")
}
