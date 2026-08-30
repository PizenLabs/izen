package ui

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
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
		// ── UNIFIED MARKDOWN/ANSI ENGINE ──────────────────────────────
		// roleAI records (both completed history and the live streaming tail)
		// render through renderAIBlockLines — the single Catppuccin Mocha
		// engine for headers, inline code, fenced code blocks, lists and
		// numbered items. This guarantees stream completion never reverts to
		// raw markdown syntax: the trailing lines are byte-identical to the
		// streamed ones, minus the active block cursor.
		if rec.role == roleAI {
			aiLines := renderAIBlockLines(text, wrapWidth)
			for i := range aiLines {
				aiLines[i].GlobalY = globalY
				aiLines[i].RecordIdx = idx
				lines = append(lines, aiLines[i])
				globalY++
			}
			if len(aiLines) == 0 {
				lines = append(lines, DocumentLine{GlobalY: globalY, Spans: []RenderSpan{{StartCell: 0, EndCell: 2, SourceStart: 0, SourceEnd: 0, Selectable: false}}, RawText: "", RenderedStr: dimmedStyle.Render("│ "), RecordIdx: idx})
				globalY++
			}
			continue
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

// ── Unified Markdown / ANSI rendering engine ──────────────────────────────────
// renderAIBlockLines converts a markdown/text AI record into physical
// DocumentLines with Catppuccin Mocha ANSI styling. It is the SINGLE unified
// engine shared by:
//   - the completed-history path (BuildDocumentLayout roleAI branch), and
//   - the live streaming tail (syncStreamingSegment in model.go).
//
// Both paths therefore produce byte-identical RenderedStr/Spans, so stream
// completion NEVER reverts to raw markdown syntax (fences, backticks) — the
// only difference is the trailing active cursor appended while streaming.

// aiBlockRenderer is the STATEFUL markdown→DocumentLine engine. It consumes
// logical lines one at a time so a long-lived LLM stream can render
// incrementally: complete logical lines are committed exactly once and cached,
// and only the partial trailing line is re-rendered as it grows. The code-block
// and list state (inCode / lang / codeLines) persists across lines so an open
// fence or multi-line markdown construct carries its context between ticks
// without re-parsing its head. renderAIBlockLines is the one-shot wrapper.
type aiBlockRenderer struct {
	out       []DocumentLine
	inCode    bool
	lang      string
	codeLines []string
	inTable   bool
	tableRows []string
}

// renderLine consumes one LOGICAL line (no embedded "\n") and appends its
// rendered DocumentLines to out. It is the exact per-line state machine of the
// legacy renderAIBlockLines loop, extracted so streaming ticks can feed only
// the delta.
func (r *aiBlockRenderer) renderLine(rl string, wrapWidth int) {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	trimmed := strings.TrimSpace(rl)
	if strings.HasPrefix(trimmed, "```") {
		if r.inCode {
			r.flushCode(wrapWidth)
			r.inCode = false
		} else {
			r.inCode = true
			r.lang = strings.TrimPrefix(trimmed, "```")
		}
		return
	}
	if r.inCode {
		r.codeLines = append(r.codeLines, rl)
		return
	}
	// Pipe-delimited table detection: a trimmed line that starts with '|'
	// and contains another '|' is a table row (header, separator, or body).
	// Rows are buffered so the full grid (column widths) can be computed on
	// flush, exactly like fenced code blocks.
	if isTableRowLine(trimmed) {
		if !r.inTable {
			r.inTable = true
		}
		r.tableRows = append(r.tableRows, rl)
		return
	}
	if r.inTable {
		r.flushTable(wrapWidth)
	}
	if trimmed == "" {
		r.out = append(r.out, gutterDocumentLine(wrapWidth))
		return
	}
	// Word-wrap the raw logical line before styling (marker width is reserved
	// on the first wrapped line so the styled line lands exactly on the
	// boundary, never past it).
	innerW := wrapWidth - 2
	if innerW < 10 {
		innerW = 10
	}
	wrapW := innerW - markdownLinePrefixWidth(rl) - 2
	if wrapW < 10 {
		wrapW = 10
	}
	wrapped := ansi.Wordwrap(rl, wrapW, " \t")
	for _, sub := range strings.Split(wrapped, "\n") {
		rendered := renderDeterministicInlineMarkdown(sub, wrapWidth)
		r.out = append(r.out, renderedTextToDocumentLines("│ ", rendered, wrapWidth)...)
	}
}

// flushCode emits the buffered code-block lines (Chromatised, Catppuccin
// Mocha) at the closing fence or EOF.
func (r *aiBlockRenderer) flushCode(wrapWidth int) {
	if len(r.codeLines) > 0 {
		r.out = append(r.out, renderCodeBlockToLines(r.lang, r.codeLines, wrapWidth)...)
	}
	r.codeLines = nil
	r.lang = ""
}

// finish flushes any unclosed code block left at the end of the input so a
// stream that ends mid-fence still renders its buffered lines exactly as the
// completed-history path does.
func (r *aiBlockRenderer) finish(wrapWidth int) {
	if r.inCode {
		r.flushCode(wrapWidth)
	}
	if r.inTable {
		r.flushTable(wrapWidth)
	}
}

// flushTable emits the buffered table rows as a Unicode box grid at the closing
// blank line or EOF. Rows are parsed, column widths computed, and the structured
// border container (┌─┬─┐ / ├─┼─┤ / └─┴─┘) rendered with native transparent
// background preserved.
func (r *aiBlockRenderer) flushTable(wrapWidth int) {
	if len(r.tableRows) > 0 {
		r.out = append(r.out, renderMarkdownTableToLines(r.tableRows, wrapWidth)...)
	}
	r.tableRows = nil
	r.inTable = false
}

// renderAIBlockLines renders an AI record's text to physical DocumentLines.
func renderAIBlockLines(text string, wrapWidth int) []DocumentLine {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	text = ensurePreflightDelimiter(sanitizeText(text))
	r := &aiBlockRenderer{}
	for _, rl := range strings.Split(text, "\n") {
		r.renderLine(rl, wrapWidth)
	}
	r.finish(wrapWidth)
	return r.out
}

// renderedTextToDocumentLines converts a rendered (ANSI-styled, possibly
// multi-line) markdown block into DocumentLines with an outer AI gutter and
// span-level cell geometry. Header leading-separator newlines become empty
// gutter rows so headings always start on a fresh physical line.
func renderedTextToDocumentLines(gutter string, rendered string, wrapWidth int) []DocumentLine {
	if rendered == "" {
		return nil
	}
	var out []DocumentLine
	parts := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	gutterCells := runewidth.StringWidth(ansi.Strip(gutter))
	for _, p := range parts {
		if p == "" {
			out = append(out, gutterDocumentLine(wrapWidth))
			continue
		}
		raw := ansi.Strip(p)
		contentCells := runewidth.StringWidth(raw)
		spans := []RenderSpan{
			{StartCell: 0, EndCell: gutterCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
			{StartCell: gutterCells, EndCell: gutterCells + contentCells, SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true},
		}
		out = append(out, DocumentLine{
			Spans:       spans,
			RawText:     raw,
			RenderedStr: dimmedStyle.Render(gutter) + p,
		})
	}
	return out
}

// renderCodeBlockToLines renders a fenced code block as an elegant thin-line
// framed box container with native terminal background preserved. The outer AI
// gutter ("│ ") is kept, then a box is drawn:
//
//	┌─ lang ──────────────────────────────────────────┐
//	│ code line 1                                      │
//	│ code line 2                                      │
//	└─────────────────────────────────────────────────┘
//
// Border style is dimmed subtle color (Surface2 #585b70 / Overlay0 #6c7086)
// via mdCodeBorderStyle, with NO Background(...) fill so the user's native
// terminal background shows through. Syntax colours are foreground-only.
func renderCodeBlockToLines(lang string, codeLines []string, wrapWidth int) []DocumentLine {
	if len(codeLines) == 0 {
		return nil
	}
	// Shell/command snippets from response text are informational copy — they
	// render frameless (no ┌─┐ box), indented with a dimmed "$ " prompt and
	// Catppuccin Yellow command foreground. Actual Tool Execution panels never
	// pass through here (they use renderExecutionFrame), so this branch cannot
	// touch them.
	if isShellLang(lang) {
		return renderShellSnippetToLines(codeLines, wrapWidth)
	}
	outerGutter := "│ "
	outerCells := runewidth.StringWidth(ansi.Strip(outerGutter))
	boxWidth := wrapWidth - outerCells
	if boxWidth < 10 {
		boxWidth = 10
	}
	boxStyle := mdCodeBorderStyle // dimmed, no background
	// Top border: ┌─ lang ──────┐
	var topBorder string
	if lang != "" {
		label := "─ " + lang + " "
		labelCells := runewidth.StringWidth(label)
		// ┌ + label + dashes + ┐  => total boxWidth ("┌─ go ──┐")
		remaining := boxWidth - 2 - labelCells
		if remaining < 0 {
			remaining = 0
		}
		topBorder = "┌" + label + strings.Repeat("─", remaining) + "┐"
	} else {
		topBorder = "┌" + strings.Repeat("─", boxWidth-2) + "┐"
	}
	bottomBorder := "└" + strings.Repeat("─", boxWidth-2) + "┘"
	// Content width inside box: between "│ " and " │" (4 cells total).
	innerWidth := boxWidth - 4
	if innerWidth < 4 {
		innerWidth = 4
	}
	var out []DocumentLine
	// Use raw ANSI for border to guarantee ANSI in test (lipgloss may be disabled in test)
	const borderFg = "\x1b[38;2;88;91;112m" // colorDimmed #585b70
	const borderReset = "\x1b[0m"
	const gutterFg = "\x1b[38;2;88;91;112m"
	_ = boxStyle
	// Top border line (non-selectable)
	out = append(out, DocumentLine{
		Spans:       []RenderSpan{{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false}, {StartCell: outerCells, EndCell: outerCells + boxWidth, SourceStart: 0, SourceEnd: 0, Selectable: false}},
		RawText:     "",
		RenderedStr: gutterFg + outerGutter + borderReset + borderFg + topBorder + borderReset,
	})
	// Each code line, wrapped to innerWidth, with side borders
	for _, rawLine := range codeLines {
		// Preserve empty lines as empty boxed rows
		if rawLine == "" {
			emptyContent := strings.Repeat(" ", innerWidth)
			rendered := gutterFg + outerGutter + borderReset + borderFg + "│ " + borderReset + emptyContent + borderFg + " │" + borderReset
			out = append(out, DocumentLine{
				Spans:       []RenderSpan{{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false}, {StartCell: outerCells, EndCell: outerCells + 2, SourceStart: 0, SourceEnd: 0, Selectable: false}, {StartCell: outerCells + 2, EndCell: outerCells + 2 + innerWidth, SourceStart: 0, SourceEnd: 0, Selectable: true}},
				RawText:     "",
				RenderedStr: rendered,
			})
			continue
		}
		// Wrap long lines at innerWidth (cell-aware, no ANSI in raw)
		wrapped := wrapForContentWidth(rawLine, innerWidth)
		if len(wrapped) == 0 {
			wrapped = []string{rawLine}
		}
		for _, piece := range wrapped {
			// Colorize with Chroma/Catppuccin foreground only, explicitly stripping background codes
			colorized := colorizeNoBg(lang, piece)
			// Ensure no background escapes leak (defensive strip)
			if strings.Contains(colorized, "48;2;") {
				colorized = stripBackgroundANSI(colorized)
			}
			// Calculate visual width with ANSI-aware width
			colorizedWidth := ansi.StringWidth(colorized)
			_ = colorizedWidth
			pieceCells := runewidth.StringWidth(piece)
			// Use raw piece width for padding (same as colorized width)
			padding := ""
			if pieceCells < innerWidth {
				padding = strings.Repeat(" ", innerWidth-pieceCells)
			}
			rendered := gutterFg + outerGutter + borderReset + borderFg + "│ " + borderReset + colorized + padding + borderFg + " │" + borderReset
			contentCells := runewidth.StringWidth(piece)
			out = append(out, DocumentLine{
				Spans: []RenderSpan{
					{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
					{StartCell: outerCells, EndCell: outerCells + 2, SourceStart: 0, SourceEnd: 0, Selectable: false},
					{StartCell: outerCells + 2, EndCell: outerCells + 2 + contentCells, SourceStart: 0, SourceEnd: len([]rune(piece)), Selectable: true},
				},
				RawText:     piece,
				RenderedStr: rendered,
			})
		}
	}
	// Bottom border line (non-selectable)
	out = append(out, DocumentLine{
		Spans:       []RenderSpan{{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false}, {StartCell: outerCells, EndCell: outerCells + boxWidth, SourceStart: 0, SourceEnd: 0, Selectable: false}},
		RawText:     "",
		RenderedStr: "\x1b[38;2;88;91;112m" + outerGutter + "\x1b[0m" + "\x1b[38;2;88;91;112m" + bottomBorder + "\x1b[0m",
	})
	return out
}

// renderShellSnippetToLines renders a shell/command snippet from response text
// WITHOUT a framed box container. Each line is indented, prefixed with a dimmed
// "$ " prompt (Overlay0 #6c7086), and the command string itself is painted with
// Catppuccin Yellow (#f9e2af) 24-bit foreground. No background fill is applied.
// The outer AI gutter ("│ ") is kept for consistent cell geometry.
func renderShellSnippetToLines(codeLines []string, wrapWidth int) []DocumentLine {
	outerGutter := "│ "
	outerCells := runewidth.StringWidth(ansi.Strip(outerGutter))
	indent := "  "
	indentCells := runewidth.StringWidth(indent)
	prompt := "$ "
	promptCells := runewidth.StringWidth(prompt)
	prefixCells := outerCells + indentCells + promptCells
	innerWidth := wrapWidth - prefixCells
	if innerWidth < 10 {
		innerWidth = 10
	}
	const (
		gutterFg = "\x1b[38;2;88;91;112m"   // #585b70
		promptFg = "\x1b[38;2;108;112;134m" // #6c7086 dimmed prompt
		reset    = "\x1b[0m"
	)
	var out []DocumentLine
	for _, rawLine := range codeLines {
		if strings.TrimSpace(rawLine) == "" {
			rendered := gutterFg + outerGutter + reset + indent + strings.Repeat(" ", promptCells) + reset
			out = append(out, DocumentLine{
				Spans: []RenderSpan{
					{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
					{StartCell: outerCells, EndCell: outerCells + indentCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
				},
				RawText:     "",
				RenderedStr: rendered,
			})
			continue
		}
		wrapped := wrapForContentWidth(rawLine, innerWidth)
		if len(wrapped) == 0 {
			wrapped = []string{rawLine}
		}
		for wi, piece := range wrapped {
			cmd := shellColorCommand(piece)
			cmdCells := runewidth.StringWidth(piece)
			padding := ""
			if cmdCells < innerWidth {
				padding = strings.Repeat(" ", innerWidth-cmdCells)
			}
			cmdStart := outerCells + indentCells + promptCells
			spanCmd := RenderSpan{StartCell: cmdStart, EndCell: cmdStart + cmdCells, SourceStart: 0, SourceEnd: len([]rune(piece)), Selectable: true}
			if wi == 0 {
				rendered := gutterFg + outerGutter + reset + indent + promptFg + prompt + reset + cmd + padding + reset
				out = append(out, DocumentLine{
					Spans: []RenderSpan{
						{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
						{StartCell: outerCells, EndCell: cmdStart, SourceStart: 0, SourceEnd: 0, Selectable: false},
						spanCmd,
					},
					RawText:     piece,
					RenderedStr: rendered,
				})
			} else {
				contIndent := strings.Repeat(" ", indentCells+promptCells)
				rendered := gutterFg + outerGutter + reset + contIndent + cmd + padding + reset
				out = append(out, DocumentLine{
					Spans: []RenderSpan{
						{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
						{StartCell: outerCells, EndCell: cmdStart, SourceStart: 0, SourceEnd: 0, Selectable: false},
						spanCmd,
					},
					RawText:     piece,
					RenderedStr: rendered,
				})
			}
		}
	}
	return out
}

// shellColorCommand styles one shell command piece. Standard commands render in
// Catppuccin Yellow (#f9e2af); full-line and inline comments render in
// Catppuccin Green (#a6e3a1). An inline comment is the first '#' preceded by
// whitespace, so '#' inside quoted arguments or URLs is left as command text.
func shellColorCommand(piece string) string {
	const (
		cmdYellow = "\x1b[38;2;249;226;175m" // #f9e2af Catppuccin Yellow
		cmdGreen  = "\x1b[38;2;166;227;161m" // #a6e3a1 Catppuccin Green
		reset     = "\x1b[0m"
	)
	if strings.HasPrefix(strings.TrimSpace(piece), "#") {
		return cmdGreen + piece + reset
	}
	idx := -1
	for i := 0; i < len(piece); i++ {
		if piece[i] == '#' && i > 0 && (piece[i-1] == ' ' || piece[i-1] == '\t') {
			idx = i
			break
		}
	}
	if idx < 0 {
		return cmdYellow + piece + reset
	}
	return cmdYellow + piece[:idx] + reset + cmdGreen + piece[idx:] + reset
}

// isTableRowLine reports whether a trimmed line is a pipe-delimited markdown
// table row: it must start with '|' and contain at least one further '|'.
func isTableRowLine(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "|") {
		return false
	}
	return strings.Contains(strings.TrimPrefix(trimmed, "|"), "|")
}

// splitTableRowCells splits one pipe-delimited row into its cells, dropping the
// leading/trailing pipes and trimming each cell.
func splitTableRowCells(rl string) []string {
	s := strings.TrimSpace(rl)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}

// isTableSepCell reports whether a cell is a GFM header separator fragment
// (e.g. "---", ":---", "---:", ":---:").
func isTableSepCell(c string) bool {
	if c == "" {
		return false
	}
	c = strings.TrimLeft(c, ":")
	c = strings.TrimRight(c, ":")
	return c != "" && strings.Trim(c, "-") == ""
}

// renderTableCell renders a single table cell's inline markdown (code, bold)
// with background codes stripped so the native terminal background shows
// through.
func renderTableCell(cell string) string {
	if strings.TrimSpace(cell) == "" {
		return ""
	}
	rendered := applyInlineStyles(cell)
	if strings.Contains(rendered, "48;2;") {
		rendered = stripBackgroundANSI(rendered)
	}
	return rendered
}

// renderMarkdownTableToLines renders a buffered pipe-delimited markdown table
// as a structured Unicode box grid:
//
//	┌─────────┬─────────┐
//	│ Col1    │ Col2    │
//	├─────────┼─────────┤
//	│ A       │ B       │
//	└─────────┴─────────┘
//
// Column widths are the maximum visual cell width across the header and all
// body rows. Borders use #585b70 (38;2;88;91;112), header titles are bold
// Catppuccin Green (#a6e3a1), and no background fill is ever applied.
func renderMarkdownTableToLines(rows []string, wrapWidth int) []DocumentLine {
	type parsedRow struct {
		isSep bool
		cells []string
	}
	var parsed = make([]parsedRow, 0, len(rows))
	for _, rl := range rows {
		cells := splitTableRowCells(rl)
		isSep := true
		for _, c := range cells {
			if !isTableSepCell(c) {
				isSep = false
				break
			}
		}
		parsed = append(parsed, parsedRow{isSep: isSep, cells: cells})
	}

	var headerCells []string
	var bodyRows [][]string
	sepSeen := false
	hasSep := false
	for _, pr := range parsed {
		if pr.isSep {
			if !sepSeen {
				sepSeen = true
				hasSep = true
			}
			continue
		}
		if !sepSeen {
			headerCells = pr.cells
		} else {
			bodyRows = append(bodyRows, pr.cells)
		}
	}
	if !hasSep {
		headerCells = nil
		bodyRows = nil
		for _, pr := range parsed {
			if !pr.isSep {
				bodyRows = append(bodyRows, pr.cells)
			}
		}
	}

	ncols := len(headerCells)
	for _, row := range bodyRows {
		if len(row) > ncols {
			ncols = len(row)
		}
	}
	if ncols == 0 {
		return nil
	}

	colWidths := make([]int, ncols)
	renderHeader := make([]string, len(headerCells))
	for i, c := range headerCells {
		renderHeader[i] = renderTableCell(c)
		if i < ncols {
			if w := ansi.StringWidth(renderHeader[i]); w > colWidths[i] {
				colWidths[i] = w
			}
		}
	}
	renderBody := make([][]string, len(bodyRows))
	for ri, row := range bodyRows {
		renderBody[ri] = make([]string, len(row))
		for ci, c := range row {
			renderBody[ri][ci] = renderTableCell(c)
			if ci < ncols {
				if w := ansi.StringWidth(renderBody[ri][ci]); w > colWidths[ci] {
					colWidths[ci] = w
				}
			}
		}
	}
	for i := range colWidths {
		if colWidths[i] < 3 {
			colWidths[i] = 3
		}
	}

	const (
		borderFg = "\x1b[38;2;88;91;112m"     // #585b70
		headerFg = "\x1b[1;38;2;166;227;161m" // bold #a6e3a1
		gutterFg = "\x1b[38;2;88;91;112m"
		reset    = "\x1b[0m"
	)
	outerGutter := "│ "
	outerCells := runewidth.StringWidth(outerGutter)

	seg := make([]int, ncols)
	totalWidth := 1
	for i := range colWidths {
		seg[i] = colWidths[i] + 2
		totalWidth += seg[i]
	}
	totalWidth += ncols - 1
	totalWidth += 1

	horiz := func(left, mid, right string) string {
		var b strings.Builder
		b.WriteString(left)
		for i := 0; i < ncols; i++ {
			if i > 0 {
				b.WriteString(mid)
			}
			b.WriteString(strings.Repeat("─", seg[i]))
		}
		b.WriteString(right)
		return b.String()
	}
	topBorder := horiz("┌", "┬", "┐")
	sepBorder := horiz("├", "┼", "┤")
	botBorder := horiz("└", "┴", "┘")

	rowLine := func(cells []string, isHeader bool) string {
		var b strings.Builder
		b.WriteString("│")
		for i := 0; i < ncols; i++ {
			content := ""
			if i < len(cells) {
				content = cells[i]
			}
			w := ansi.StringWidth(content)
			pad := 0
			if w < colWidths[i] {
				pad = colWidths[i] - w
			}
			b.WriteString(" ")
			if isHeader {
				b.WriteString(headerFg + content + reset)
			} else {
				b.WriteString(content)
			}
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString(" │")
		}
		return b.String()
	}

	borderLine := func(border string) DocumentLine {
		return DocumentLine{
			Spans: []RenderSpan{
				{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
				{StartCell: outerCells, EndCell: outerCells + totalWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
			},
			RawText:     "",
			RenderedStr: gutterFg + outerGutter + reset + borderFg + border + reset,
		}
	}

	contentLine := func(cells []string, isHeader bool) DocumentLine {
		rendered := rowLine(cells, isHeader)
		raw := ansi.Strip(rendered)
		return DocumentLine{
			Spans: []RenderSpan{
				{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
				{StartCell: outerCells, EndCell: outerCells + totalWidth, SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true},
			},
			RawText:     raw,
			RenderedStr: gutterFg + outerGutter + reset + rendered,
		}
	}

	out := []DocumentLine{borderLine(topBorder)}
	if len(renderHeader) > 0 {
		out = append(out, contentLine(renderHeader, true))
	}
	if hasSep {
		out = append(out, borderLine(sepBorder))
	}
	for _, rc := range renderBody {
		out = append(out, contentLine(rc, false))
	}
	out = append(out, borderLine(botBorder))
	return out
}

// codeSurfaceBG is retained for backward compat but no longer used for code
// containers — background fills are forbidden (native terminal background).
const codeSurfaceBG = ""

// stripBackgroundANSI removes any background color escapes (48;2;...) from an
// ANSI string to preserve native transparent terminal backgrounds.
func stripBackgroundANSI(s string) string {
	if !strings.Contains(s, "48;2;") {
		return s
	}
	// Replace SGR sequences containing background codes.
	// We scan for \x1b[...m and filter out 48;2;r;g;b segments.
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			seq := s[i:j]
			// Filter background codes inside seq
			inner := seq[2 : len(seq)-1] // between [ and m
			parts := strings.Split(inner, ";")
			var filtered []string
			k := 0
			for k < len(parts) {
				if parts[k] == "48" && k+4 < len(parts) && parts[k+1] == "2" {
					k += 5
					continue
				}
				filtered = append(filtered, parts[k])
				k++
			}
			if len(filtered) == 0 {
				// If only background was present, emit reset instead of empty
				out.WriteString("\x1b[0m")
			} else {
				out.WriteString("\x1b[" + strings.Join(filtered, ";") + "m")
			}
			i = j
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// normalizeShellLang maps shell-like identifiers to Chroma's bash lexer name.
func normalizeShellLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "sh", "shell", "zsh", "bash", "console", "cmd", "terminal":
		return "bash"
	default:
		return lang
	}
}

// isShellLang reports whether lang is a shell/command language needing fallback.
func isShellLang(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "sh", "shell", "zsh", "bash", "console", "cmd", "terminal":
		return true
	default:
		return false
	}
}

// colorizeShellFallback is a custom tokenizer for shell/command lines when
// Chroma's lexer emits uncolored Text for command tokens (e.g. "go", "build").
// It emits 24-bit foreground codes without background fill:
//   - first token (command): #a6e3a1 (green) or #89dceb (cyan)
//   - flags/subcommands (-v, --flag, run): #cba6f7 (mauve) or #89b4fa (blue)
//   - args/paths (hello.go): #fab387 (peach) or #f9e2af (yellow)
func colorizeShellFallback(line string) string {
	const (
		colCmd  = "\x1b[38;2;166;227;161m" // #a6e3a1 green
		colFlag = "\x1b[38;2;203;166;247m" // #cba6f7 mauve
		colArg  = "\x1b[38;2;250;179;135m" // #fab387 peach
		reset   = "\x1b[0m"
	)
	var buf strings.Builder
	i := 0
	tokenIdx := 0
	for i < len(line) {
		if line[i] == ' ' || line[i] == '\t' {
			buf.WriteByte(line[i])
			i++
			continue
		}
		j := i
		if j < len(line) && (line[j] == '"' || line[j] == '\'') {
			quote := line[j]
			j++
			for j < len(line) && line[j] != quote {
				j++
			}
			if j < len(line) {
				j++ // include closing quote
			}
		} else {
			for j < len(line) && line[j] != ' ' && line[j] != '\t' {
				j++
			}
		}
		token := line[i:j]
		var sgr string
		if tokenIdx == 0 {
			sgr = colCmd
		} else if strings.HasPrefix(token, "-") {
			sgr = colFlag
		} else if strings.Contains(token, ".") || strings.Contains(token, "/") {
			sgr = colArg
		} else {
			// subcommand like "run", "build"
			sgr = colFlag
		}
		buf.WriteString(sgr)
		buf.WriteString(token)
		buf.WriteString(reset)
		tokenIdx++
		i = j
	}
	if buf.Len() == 0 {
		return line
	}
	return buf.String()
}

// colorizeNoBg returns Chroma/Catppuccin syntax-highlighted line with foreground
// colors (38;2;...) for keywords, identifiers, strings, etc., explicitly stripping
// any background (48;2;...) to keep terminal background transparent.
func colorizeNoBg(lang, line string) string {
	if strings.TrimSpace(line) == "" {
		return line
	}
	normalized := normalizeShellLang(lang)
	lexer := lexers.Get(normalized)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	it, err := lexer.Tokenise(nil, line)
	if err != nil {
		if isShellLang(lang) {
			return colorizeShellFallback(line)
		}
		return line
	}
	var buf strings.Builder
	hasColored := false
	for _, tok := range it.Tokens() {
		if tok.Value == "" {
			continue
		}
		// Detect whether Chroma produced any colored token beyond plain Text.
		// Shell lexers often emit a single Text token for unknown commands.
		if tok.Type != chroma.Text && tok.Type != 0 {
			hasColored = true
		}
		parts := strings.Split(tok.Value, "\n")
		for pi, part := range parts {
			if pi > 0 {
				buf.WriteString("\n")
			}
			if part == "" {
				continue
			}
			sgr := sgrForToken(tok.Type)
			sgr = stripBackgroundANSI(sgr)
			buf.WriteString(sgr)
			buf.WriteString(part)
			buf.WriteString("\x1b[0m")
		}
	}
	if buf.Len() == 0 {
		if isShellLang(lang) {
			return colorizeShellFallback(line)
		}
		return line
	}
	// If shell language produced only uncolored Text (e.g. "go run hello.go"
	// as a single Text token), fall back to custom tokenizer to ensure
	// command elements are colorized with 38;2; codes.
	if isShellLang(lang) && !hasColored {
		// Check if buffer contains only the default foreground color (#cdd6f4)
		// without distinct command/flag/arg colors.
		s := buf.String()
		// If s contains only one distinct color or is equivalent to plain,
		// use fallback for richer shell highlighting.
		if strings.Count(s, "\x1b[38;2;") <= 1 {
			return colorizeShellFallback(line)
		}
		// Also if lexer emitted single Text token covering whole line, fallback
		toks := it.Tokens()
		if len(toks) == 1 && strings.TrimSpace(toks[0].Value) == strings.TrimSpace(line) {
			return colorizeShellFallback(line)
		}
	}
	// Ensure no background codes leak
	res := buf.String()
	if strings.Contains(res, "48;2;") {
		res = stripBackgroundANSI(res)
	}
	return res
}

// gutterDocumentLine builds an empty AI gutter row (blank physical line).
func gutterDocumentLine(wrapWidth int) DocumentLine {
	return DocumentLine{
		Spans:       []RenderSpan{{StartCell: 0, EndCell: 2, SourceStart: 0, SourceEnd: 0, Selectable: false}},
		RawText:     "",
		RenderedStr: dimmedStyle.Render("│ "),
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
		// ── MULTI-RECORD APPEND (stream-completion) ────────────────
		// Records routinely grow by MORE than one between refreshes: stream
		// completion lands the committed assistant record AND the terminal
		// "✔ done · +N tok" telemetry record in the same turn. Append ALL
		// records after the last committed index instead of assuming exactly one
		// new record — otherwise every record after the first new one is silently
		// dropped from the layout (the completed answer disappears while only the
		// status line renders).
		if maxIdx < len(records)-1 {
			added := BuildDocumentLayout(records[maxIdx+1:], wrapWidth, uname)
			// Adjust GlobalY and RecordIdx (sub-build uses 0-relative indices).
			base := prevLen
			baseIdx := maxIdx + 1
			for i := range added.Lines {
				added.Lines[i].GlobalY = base + i
				added.Lines[i].RecordIdx = baseIdx + added.Lines[i].RecordIdx
			}
			newLines := append(append([]DocumentLine(nil), prev.Lines...), added.Lines...)
			return DocumentLayout{Lines: newLines, width: wrapWidth}
		}
		// Fallback: if line count differs drastically or records shrunk, full rebuild
		if len(records) < maxIdx+1 {
			return BuildDocumentLayout(records, wrapWidth, uname)
		}
	}
	return BuildDocumentLayout(records, wrapWidth, uname)
}
