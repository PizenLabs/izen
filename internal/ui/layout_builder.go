package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/PizenLabs/izen/internal/config"
)

// BuildDocumentLayout flattens records into physical rows with word-wrapping and
// correct RenderSpan boundaries. Every physical row gets a strictly sequential
// GlobalY index. Wrapping accounts for CJK double-width runes via runewidth.
// username is the display name for @-badge (e.g. "kaka"); empty falls back to "Developer".
func BuildDocumentLayout(records []record, wrapWidth int, username ...string) DocumentLayout {
	// Support both old 2-arg calls and new 3-arg calls via variadic for backward compat.
	var uname string
	if len(username) > 0 {
		uname = username[0]
	}
	return BuildDocumentLayoutWithUsername(records, wrapWidth, uname)
}

// BuildDocumentLayoutWithUsername is the canonical builder with explicit username.
func BuildDocumentLayoutWithUsername(records []record, wrapWidth int, username string) DocumentLayout {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	var lines []DocumentLine
	globalY := 0

	// Chrome prefix rows (banner/workspace headers) are handled in model.go's
	// content assembly; here we focus on record rows. Callers that need chrome
	// can prepend via BuildDocumentLayoutWithChrome or let model add them.

	for idx, rec := range records {
		// Special handling for User to keep badge stable (visual invariance) with dynamic username.
		// Implements mandatory word wrapping: contentWidth = wrapWidth - headerWidth.
		if rec.role == roleUser {
			displayName := config.SanitizeUsername(username)
			headerPlain := "@" + displayName + "  "
			headerWidth := runewidth.StringWidth(headerPlain)
			headerRendered := dimmedStyle.Render(headerPlain)
			contentWidth := wrapWidth - headerWidth
			if contentWidth < 10 {
				contentWidth = 10
			}
			origLines := strings.Split(sanitizeText(rec.text), "\n")
			isFirstPhysical := true
			for _, origLine := range origLines {
				// Wrap each logical line strictly at contentWidth.
				// Use wrapPlainLine for strict cell-aware wrapping; empty lines produce single "".
				var wrapped []string
				if strings.TrimSpace(origLine) == "" {
					wrapped = []string{origLine}
				} else {
					// Use wrapIndentedLine with contentWidth but without extra indent handling for user;
					// for user prompts we want plain word wrap at contentWidth.
					// We use wrapPlainLine-style logic via wrapIndentedLine with no leading whitespace preservation.
					// To keep CJK correctness, delegate to wrapPlainLike helper.
					wrapped = wrapForContentWidth(origLine, contentWidth)
				}
				if len(wrapped) == 0 {
					wrapped = []string{""}
				}
				for _, wl := range wrapped {
					// Ensure wl does not exceed contentWidth (hard-chunk if needed)
					wlCells := runewidth.StringWidth(wl)
					if wlCells > contentWidth {
						// Chunk overlong token at cell boundary
						for _, piece := range chunkWord(wl, contentWidth) {
							var renderedLine string
							var rawText string
							var spans []RenderSpan
							if isFirstPhysical {
								rawText = piece
								renderedLine = headerRendered + userBgStyle.Render(piece)
								spans = []RenderSpan{
									{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
									{StartCell: headerWidth, EndCell: headerWidth + runewidth.StringWidth(rawText), SourceStart: 0, SourceEnd: len([]rune(rawText)), Selectable: true},
								}
								isFirstPhysical = false
							} else {
								rawText = piece
								padded := strings.Repeat(" ", headerWidth)
								renderedLine = padded + userBgStyle.Render(piece)
								spans = []RenderSpan{
									{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
									{StartCell: headerWidth, EndCell: headerWidth + runewidth.StringWidth(rawText), SourceStart: 0, SourceEnd: len([]rune(rawText)), Selectable: true},
								}
							}
							// Final width invariant check: clamp if still over
							if runewidth.StringWidth(ansi.Strip(renderedLine)) > wrapWidth {
								// Truncate content to fit
								allowed := contentWidth
								if len(piece) > 0 {
									rawText = string([]rune(piece)[:allowed])
									if isFirstPhysical {
										renderedLine = headerRendered + userBgStyle.Render(rawText)
									} else {
										padded := strings.Repeat(" ", headerWidth)
										renderedLine = padded + userBgStyle.Render(rawText)
									}
								}
							}
							dl := DocumentLine{GlobalY: globalY, Spans: spans, RawText: rawText, RenderedStr: renderedLine, RecordIdx: idx}
							lines = append(lines, dl)
							globalY++
						}
						continue
					}
					var renderedLine string
					rawText := wl
					var spans []RenderSpan
					if isFirstPhysical {
						renderedLine = headerRendered + userBgStyle.Render(wl)
						spans = []RenderSpan{
							{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
							{StartCell: headerWidth, EndCell: headerWidth + runewidth.StringWidth(rawText), SourceStart: 0, SourceEnd: len([]rune(rawText)), Selectable: true},
						}
						isFirstPhysical = false
					} else {
						padded := strings.Repeat(" ", headerWidth)
						renderedLine = padded + userBgStyle.Render(wl)
						spans = []RenderSpan{
							{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
							{StartCell: headerWidth, EndCell: headerWidth + runewidth.StringWidth(rawText), SourceStart: 0, SourceEnd: len([]rune(rawText)), Selectable: true},
						}
					}
					// Ensure invariant: rendered width <= wrapWidth
					if runewidth.StringWidth(ansi.Strip(renderedLine)) > wrapWidth {
						// Safety: truncate (should not happen with correct contentWidth)
						trimmed := ansi.Truncate(renderedLine, wrapWidth, "")
						renderedLine = trimmed
					}
					dl := DocumentLine{GlobalY: globalY, Spans: spans, RawText: rawText, RenderedStr: renderedLine, RecordIdx: idx}
					lines = append(lines, dl)
					globalY++
				}
			}
			// Ensure at least one line for empty user prompt
			if len(origLines) == 0 {
				rawText := ""
				renderedLine := headerRendered + userBgStyle.Render("")
				spans := []RenderSpan{
					{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
					{StartCell: headerWidth, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: true},
				}
				dl := DocumentLine{GlobalY: globalY, Spans: spans, RawText: rawText, RenderedStr: renderedLine, RecordIdx: idx}
				lines = append(lines, dl)
				globalY++
			}
			continue
		}
		text := sanitizeText(rec.text)
		// Deterministic preflight handling: if completed, collapse to single summary
		if rec.role == roleActivity && strings.Contains(text, "[preflight]") {
			if strings.Contains(text, "completed") || strings.Contains(text, "✓ preflight") {
				rawCollapsed := "✓ preflight completed"
				renderedCollapsed := dimmedStyle.Render(rawCollapsed)
				spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(rawCollapsed), SourceStart: 0, SourceEnd: len([]rune(rawCollapsed)), Selectable: true}}
				dl := DocumentLine{GlobalY: globalY, Spans: spans, RawText: rawCollapsed, RenderedStr: renderedCollapsed, RecordIdx: idx}
				lines = append(lines, dl)
				globalY++
				continue
			}
		}
		// For AI markdown responses and plain text records: effectiveWidth = wrapWidth - gutterWidth
		gutterWidth := 2 // "│ " is 2 cells
		effectiveWidth := wrapWidth - gutterWidth
		if effectiveWidth < 10 {
			effectiveWidth = 10
		}
		isAI := rec.role == roleAI
		logicalLines := strings.Split(text, "\n")
		if len(logicalLines) == 0 {
			logicalLines = []string{""}
		}
		for _, ll := range logicalLines {
			if strings.TrimSpace(ll) == "" {
				// Empty logical line: preserve as single physical empty row with gutter for AI
				if isAI {
					rendered := dimmedStyle.Render("│ ")
					// Empty content but gutter present
					spans := []RenderSpan{
						{StartCell: 0, EndCell: gutterWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
					}
					dl := DocumentLine{GlobalY: globalY, Spans: spans, RawText: "", RenderedStr: rendered, RecordIdx: idx}
					lines = append(lines, dl)
					globalY++
				} else {
					spans := []RenderSpan{{StartCell: 0, EndCell: 0, SourceStart: 0, SourceEnd: 0, Selectable: true}}
					dl := DocumentLine{GlobalY: globalY, Spans: spans, RawText: "", RenderedStr: "", RecordIdx: idx}
					lines = append(lines, dl)
					globalY++
				}
				continue
			}
			// Strict wrapping at effectiveWidth (preserve indent via wrapIndentedLine)
			wrapped := wrapForContentWidth(ll, effectiveWidth)
			if len(wrapped) == 0 {
				wrapped = []string{ll}
			}
			for _, wl := range wrapped {
				// Ensure wl cell width <= effectiveWidth (chunk if needed)
				if runewidth.StringWidth(wl) > effectiveWidth {
					for _, piece := range chunkWord(wl, effectiveWidth) {
						contentRaw := piece
						var renderedLine string
						var spans []RenderSpan
						if isAI {
							renderedLine = dimmedStyle.Render("│ ") + piece
							spans = []RenderSpan{
								{StartCell: 0, EndCell: gutterWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
								{StartCell: gutterWidth, EndCell: gutterWidth + runewidth.StringWidth(contentRaw), SourceStart: 0, SourceEnd: len([]rune(contentRaw)), Selectable: true},
							}
						} else {
							renderedLine = piece
							spans = []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(contentRaw), SourceStart: 0, SourceEnd: len([]rune(contentRaw)), Selectable: true}}
						}
						// Final invariant: ensure rendered width <= wrapWidth
						if runewidth.StringWidth(ansi.Strip(renderedLine)) > wrapWidth {
							renderedLine = ansi.Truncate(renderedLine, wrapWidth, "")
						}
						dl := DocumentLine{GlobalY: globalY, Spans: spans, RawText: contentRaw, RenderedStr: renderedLine, RecordIdx: idx}
						lines = append(lines, dl)
						globalY++
					}
					continue
				}
				contentRaw := wl
				var renderedLine string
				var spans []RenderSpan
				if isAI {
					renderedLine = dimmedStyle.Render("│ ") + wl
					spans = []RenderSpan{
						{StartCell: 0, EndCell: gutterWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
						{StartCell: gutterWidth, EndCell: gutterWidth + runewidth.StringWidth(contentRaw), SourceStart: 0, SourceEnd: len([]rune(contentRaw)), Selectable: true},
					}
				} else {
					renderedLine = wl
					spans = []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(contentRaw), SourceStart: 0, SourceEnd: len([]rune(contentRaw)), Selectable: true}}
				}
				if runewidth.StringWidth(ansi.Strip(renderedLine)) > wrapWidth {
					renderedLine = ansi.Truncate(renderedLine, wrapWidth, "")
				}
				// Also ensure RawText width invariant (without ANSI)
				if runewidth.StringWidth(contentRaw) > wrapWidth {
					// Should not happen because contentRaw <= effectiveWidth < wrapWidth
					contentRaw = string([]rune(contentRaw)[:wrapWidth])
				}
				dl := DocumentLine{GlobalY: globalY, Spans: spans, RawText: contentRaw, RenderedStr: renderedLine, RecordIdx: idx}
				lines = append(lines, dl)
				globalY++
			}
		}
		if len(logicalLines) == 0 {
			lines = append(lines, DocumentLine{
				GlobalY:     globalY,
				Spans:       []RenderSpan{{StartCell: 0, EndCell: 0, SourceStart: 0, SourceEnd: 0, Selectable: true}},
				RawText:     "",
				RenderedStr: "",
				RecordIdx:   idx,
			})
			globalY++
		}
	}

	// If records empty, still return empty layout (no chrome here)
	return DocumentLayout{
		Lines: lines,
		width: wrapWidth,
	}
}

// BuildDocumentLayoutWithChrome builds layout including chrome prefix rows.
// chromeLines are pre-rendered chrome strings (banner, workspace header) split by "\n".
func BuildDocumentLayoutWithChrome(chromeLines []string, records []record, wrapWidth int, username ...string) DocumentLayout {
	var uname string
	if len(username) > 0 {
		uname = username[0]
	}
	var lines []DocumentLine
	globalY := 0
	for _, ch := range chromeLines {
		parts := strings.Split(ch, "\n")
		for _, p := range parts {
			if p == "" {
				// Still count as a physical row for GlobalY
				lines = append(lines, DocumentLine{
					GlobalY:     globalY,
					Spans:       []RenderSpan{{StartCell: 0, EndCell: 0, SourceStart: 0, SourceEnd: 0, Selectable: false}},
					RawText:     "",
					RenderedStr: "",
					RecordIdx:   -1,
				})
				globalY++
				continue
			}
			raw := ansi.Strip(p)
			lines = append(lines, DocumentLine{
				GlobalY:     globalY,
				Spans:       []RenderSpan{{StartCell: 0, EndCell: StringCellWidth(raw), SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: false}},
				RawText:     raw,
				RenderedStr: p,
				RecordIdx:   -1,
			})
			globalY++
		}
	}
	recLayout := BuildDocumentLayout(records, wrapWidth, uname)
	for i := range recLayout.Lines {
		recLayout.Lines[i].GlobalY = globalY + i
	}
	lines = append(lines, recLayout.Lines...)
	return DocumentLayout{
		Lines: lines,
		width: wrapWidth,
	}
}

// renderRecordForLayout mirrors renderRecordForViewport but returns rendered string
// without requiring a model instance. It uses sanitizeText already applied.
func renderRecordForLayout(rec record, text string, width int, username ...string) string {
	var uname string
	if len(username) > 0 {
		uname = username[0]
	}
	// For layout building we need to simulate same wrapping as renderRecordForViewport.
	// We delegate to a lightweight version that uses the same pipeline.
	// To avoid importing model methods, we replicate logic inline.
	wrapWidth := width - 4
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	switch rec.role {
	case roleUser:
		displayName := config.SanitizeUsername(uname)
		headerPlain := "@" + displayName + "  "
		headerRendered := dimmedStyle.Render(headerPlain)
		lines := strings.Split(text, "\n")
		var out []string
		for i, l := range lines {
			if i == 0 {
				out = append(out, headerRendered+userBgStyle.Render(" "+l))
			} else {
				out = append(out, wrapIndentedLine(l, wrapWidth)...)
			}
		}
		return strings.Join(out, "\n")
	case roleAI:
		// Use deterministic pipeline that handles markdown/code blocks, then add outer gutter "│ "
		// to match renderStreamingContent's gutter for accurate cell geometry.
		pipeline := RenderDeterministicPipeline(text, width, false)
		lines := strings.Split(pipeline, "\n")
		for i, l := range lines {
			trimmed := strings.TrimSpace(l)
			if trimmed == "" {
				// Preserve empty line as gutter-only
				lines[i] = "│ "
				continue
			}
			if strings.HasPrefix(strings.TrimLeft(l, " "), "│") {
				// Already has gutter (code block lines)
				continue
			}
			lines[i] = "│ " + l
		}
		return strings.Join(lines, "\n")
	default:
		var b strings.Builder
		for _, srcLine := range strings.Split(text, "\n") {
			wrapped := wrapIndentedLine(srcLine, wrapWidth)
			for i, wl := range wrapped {
				b.WriteString(wl)
				if i < len(wrapped)-1 || srcLine != strings.Split(text, "\n")[len(strings.Split(text, "\n"))-1] {
					// Add newline between wrapped parts and logical lines
					// We'll handle outer join
					b.WriteString("\n")
				}
			}
		}
		s := b.String()
		// Trim trailing newline added by loop
		s = strings.TrimSuffix(s, "\n")
		// Re-split to ensure per-logical-line wrapping preserved correctly
		// Fallback to simple re-wrap if empty
		if s == "" {
			return text
		}
		return s
	}
}

// wrapForContentWidth wraps text strictly at maxWidth using cell-aware logic.
// It delegates to wrapIndentedLine which handles CJK double-width and chunking.
func wrapForContentWidth(text string, maxWidth int) []string {
	if maxWidth < 1 {
		maxWidth = 1
	}
	return wrapIndentedLine(text, maxWidth)
}

// splitGutterPrefix detects gutter prefix cells like "│ " and returns prefix cell count and content without prefix.
// It handles AI outer gutter (2 cells) and code block inner gutter (additional 2).
func splitGutterPrefix(stripped string) (prefixCells int, content string) {
	trimmed := stripped
	count := 0
	for {
		if strings.HasPrefix(trimmed, "│ ") {
			count += 2
			// "│ " is 3 bytes for │ plus 1 for space = 4 bytes
			trimmed = strings.TrimPrefix(trimmed, "│ ")
		} else if strings.HasPrefix(trimmed, "│") {
			w := runewidth.RuneWidth('│')
			count += w
			trimmed = strings.TrimPrefix(trimmed, "│")
			if strings.HasPrefix(trimmed, " ") {
				count++
				trimmed = strings.TrimPrefix(trimmed, " ")
			}
		} else {
			break
		}
	}
	return count, trimmed
}

// IncrementalLayoutUpdate appends or invalidates ONLY the trailing record/line
// currently streaming. Returns updated layout. Full re-flattening is done only
// when width changes or record count shrinks.
func IncrementalLayoutUpdate(prev *DocumentLayout, records []record, wrapWidth int, username ...string) DocumentLayout {
	var uname string
	if len(username) > 0 {
		uname = username[0]
	}
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	if prev.width != wrapWidth {
		return BuildDocumentLayout(records, wrapWidth, uname)
	}
	prevLen := len(prev.Lines)
	// Count expected lines for records up to len-1
	// If records length unchanged but last record's text changed (streaming), invalidate trailing lines.
	if len(records) == 0 {
		return DocumentLayout{Lines: nil, width: wrapWidth}
	}
	// Heuristic: if records length same as before's record count estimate, only rebuild last record
	// We don't store record count in layout, so we estimate: if number of lines decreased/increased by more than one record's worth, do full rebuild.
	// For simplicity, if records grew by exactly 1, incremental append
	if prevLen > 0 {
		// Try incremental append if last layout's RecordIdx max < len(records)-1
		maxIdx := -1
		for _, l := range prev.Lines {
			if l.RecordIdx > maxIdx {
				maxIdx = l.RecordIdx
			}
		}
		if maxIdx == len(records)-2 && len(records) >= 2 {
			// Previous max was second last, so new record appended
			added := BuildDocumentLayout(records[len(records)-1:], wrapWidth, uname)
			// Adjust GlobalY and RecordIdx (sub-build uses 0, need original index)
			base := prevLen
			origIdx := len(records) - 1
			for i := range added.Lines {
				added.Lines[i].GlobalY = base + i
				added.Lines[i].RecordIdx = origIdx
			}
			newLines := append(append([]DocumentLine(nil), prev.Lines...), added.Lines...)
			return DocumentLayout{Lines: newLines, width: wrapWidth}
		}
		if maxIdx == len(records)-1 {
			// Streaming: last record mutated, rebuild only its lines if text changed
			startIdx := -1
			for i, l := range prev.Lines {
				if l.RecordIdx == len(records)-1 {
					startIdx = i
					break
				}
			}
			if startIdx >= 0 {
				// Check if last record's text actually changed (streaming)
				// If not changed, no need to rebuild
				lastRecText := sanitizeText(records[len(records)-1].text)
				// Reconstruct last record's raw from docLayout lines
				var lastRaw strings.Builder
				for _, l := range prev.Lines[startIdx:] {
					if l.RecordIdx == len(records)-1 {
						if lastRaw.Len() > 0 {
							lastRaw.WriteString("\n")
						}
						lastRaw.WriteString(l.RawText)
					}
				}
				// Compare stripped lastRaw vs sanitized text's first line? For simplicity, compare via rendered
				// If lengths match and no streaming flag, skip rebuild
				if lastRaw.String() == lastRecText || lastRaw.String() == ansi.Strip(lastRecText) {
					return DocumentLayout{Lines: append([]DocumentLine(nil), prev.Lines...), width: wrapWidth}
				}
				// Build new lines for last record
				newRecLines := BuildDocumentLayout(records[len(records)-1:], wrapWidth, uname)
				// Replace trailing segment and fix RecordIdx
				kept := append([]DocumentLine(nil), prev.Lines[:startIdx]...)
				base := len(kept)
				origIdx := len(records) - 1
				for i := range newRecLines.Lines {
					newRecLines.Lines[i].GlobalY = base + i
					newRecLines.Lines[i].RecordIdx = origIdx
				}
				newLines := append(kept, newRecLines.Lines...)
				return DocumentLayout{Lines: newLines, width: wrapWidth}
			}
		}
		// Fallback: if line count differs drastically or records shrunk, full rebuild
		if len(records) < maxIdx+1 {
			return BuildDocumentLayout(records, wrapWidth, uname)
		}
	}
	return BuildDocumentLayout(records, wrapWidth, uname)
}
