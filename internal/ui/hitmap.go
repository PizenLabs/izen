package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// RowLayout describes one physical terminal row inside the viewport content.
// Zero-allocation: no per-cell slices, no per-rune heap for historical logs.
// Only rows within the current viewport window are cached.
type RowLayout struct {
	RecordIdx    int32  // History record index, -1 for chrome/prefix/separator rows
	LogicalLine  int32  // Raw line index inside records[RecordIdx].text (-1 for headers/chrome/separators)
	PrefixCells  uint8  // Total cell width of all gutters/prefixes on this row (outer gutter + markdown/code prefix)
	ContentLen   uint16 // Visible cell width of printable content (excluding prefix)
	RuneStartIdx uint32 // Starting rune index in raw logical line text for this physical segment
	RuneCount    uint16 // Number of runes in this segment (for clamping)
}

// ViewportHitMap is the single source of truth for mouse hit-testing.
// It is generated atomically alongside the rendered viewport string and
// cached only for the currently visible window (YOffset .. YOffset+Height)
// to keep memory bounded for 25k+ rows.
type ViewportHitMap struct {
	YOffset int
	Rows    []RowLayout // length == viewport height (visible window)
}

// cellToRuneInString converts content cell offset to rune index within raw string s
// starting at rune offset startIdx, using runewidth for CJK/emoji/wide safety.
func cellToRuneInString(s string, startIdx int, contentCells int) int {
	if contentCells <= 0 {
		return startIdx
	}
	runes := []rune(s)
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= len(runes) {
		return len(runes)
	}
	cells := 0
	for i := startIdx; i < len(runes); i++ {
		w := runewidth.RuneWidth(runes[i])
		if cells+w > contentCells {
			return i
		}
		cells += w
		if cells >= contentCells {
			return i + 1
		}
	}
	return len(runes)
}

// stripANSI returns plain text without ANSI escapes, without modifying content.
func stripANSI(s string) string {
	return ansi.Strip(s)
}

// countPhysicalRows returns number of physical rows in rendered string s.
func countPhysicalRows(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// buildFullHitMap constructs the complete physical row layout for the current
// viewport content (prefix + all records). It is the single source of truth
// for wrapping and prefix widths; selection MUST NOT re-derive them.
func buildFullHitMap(m *model) []RowLayout {
	var rows []RowLayout
	width := m.width
	if width < 40 {
		width = 40
	}
	// Prefix chrome rows (banner + context header + workspace header)
	prefix := m.viewportContentPrefixHeight()
	for i := 0; i < prefix; i++ {
		rows = append(rows, RowLayout{RecordIdx: -1, LogicalLine: -1, PrefixCells: 0, ContentLen: 0, RuneStartIdx: 0, RuneCount: 0})
	}
	for idx, rec := range m.records {
		recRows := hitMapRowsForRecord(rec, int32(idx), width)
		rows = append(rows, recRows...)
	}
	return rows
}

// hitMapRowsForRecord returns the physical row layouts for a single record.
// It mirrors renderRecordForViewport exactly so PrefixCells and RuneStartIdx
// cannot drift.
func hitMapRowsForRecord(rec record, recordIdx int32, width int) []RowLayout {
	text := ensurePreflightDelimiter(sanitizeText(rec.text))
	switch rec.role {
	case roleUser:
		// User header is a single chrome-like row; content is not wrapped per-line.
		// We model as one row with no outer gutter prefix (user header is rendered inline).
		lines := strings.Split(text, "\n")
		var out []RowLayout
		for li, l := range lines {
			out = append(out, RowLayout{
				RecordIdx:    recordIdx,
				LogicalLine:  int32(li),
				PrefixCells:  0,
				ContentLen:   uint16(lipgloss.Width(l)),
				RuneStartIdx: 0,
				RuneCount:    uint16(len([]rune(l))),
			})
		}
		if len(out) == 0 {
			out = append(out, RowLayout{RecordIdx: recordIdx, LogicalLine: 0, PrefixCells: 0})
		}
		return out
	case roleAI:
		return hitMapRowsForAI(text, recordIdx, width)
	default:
		// roleActivity, roleError, roleStatus, roleCode, roleSystem, etc. use wrapIndentedLine
		wrapWidth := width - 4
		if wrapWidth < 20 {
			wrapWidth = 20
		}
		var out []RowLayout
		rawLines := strings.Split(text, "\n")
		for li, srcLine := range rawLines {
			parts := wrapIndentedLine(srcLine, wrapWidth)
			// wrapIndentedLine preserves leadingWhitespace on each continuation;
			// we track RuneStartIdx as offset in the original srcLine's runes.
			// For each part, compute its rune start by accumulating.
			// Use helper that mirrors wrapIndentedLine's word splitting to get accurate offsets.
			segments := hitMapSegmentsForDefaultLine(srcLine, wrapWidth)
			for _, seg := range segments {
				_ = parts // keep sync check
				out = append(out, RowLayout{
					RecordIdx:    recordIdx,
					LogicalLine:  int32(li),
					PrefixCells:  0,
					ContentLen:   uint16(lipgloss.Width(seg.content)),
					RuneStartIdx: uint32(seg.runeStart),
					RuneCount:    uint16(seg.runeCount),
				})
			}
		}
		if len(out) == 0 {
			out = append(out, RowLayout{RecordIdx: recordIdx, LogicalLine: 0, PrefixCells: 0})
		}
		return out
	}
}

// segment describes one wrapped physical row's content slice.
type segment struct {
	content   string
	runeStart int
	runeCount int
}

// hitMapSegmentsForDefaultLine mirrors wrapIndentedLine but also returns runeStart/runeCount.
func hitMapSegmentsForDefaultLine(text string, maxWidth int) []segment {
	if maxWidth < 1 {
		maxWidth = 1
	}
	prefix := leadingWhitespace(text)
	body := strings.TrimLeft(text, " \t")
	if body == "" {
		return []segment{{content: prefix, runeStart: 0, runeCount: len([]rune(prefix))}}
	}
	indent := lipgloss.Width(prefix)
	avail := maxWidth - indent
	if avail < 1 {
		avail = 1
	}
	// Track rune offset in original body (excluding prefix)
	bodyRunes := []rune(body)
	// Map word -> rune range in body
	var segs []segment
	line := prefix
	words := strings.Fields(body)
	// Precompute word rune positions in body
	wordPos := make([]int, len(words))
	pos := 0
	for i, w := range words {
		for pos < len(bodyRunes) && bodyRunes[pos] == ' ' {
			pos++
		}
		wordPos[i] = pos
		pos += len([]rune(w))
	}
	currentLineStartPos := 0
	for wi, word := range words {
		wordW := lipgloss.Width(word)
		if wordW > avail {
			// Hard chunk
			if line != prefix {
				// flush current
				segs = append(segs, segment{content: line, runeStart: len([]rune(prefix)) + currentLineStartPos, runeCount: len([]rune(strings.TrimPrefix(line, prefix))) - func() int {
					if strings.HasPrefix(strings.TrimPrefix(line, prefix), " ") {
						return 1
					}
					return 0
				}()})
				line = prefix
			}
			// Each chunk is its own row with same prefix
			for _, piece := range chunkWord(word, avail) {
				pieceRunes := len([]rune(piece))
				segs = append(segs, segment{content: prefix + piece, runeStart: len([]rune(prefix)) + wordPos[wi], runeCount: pieceRunes})
				// For next chunk of same word, rune start advances within word
				wordPos[wi] += pieceRunes
			}
			// Next line starts after this word
			if wi+1 < len(words) {
				currentLineStartPos = wordPos[wi+1]
			}
			line = prefix
			continue
		}
		if line != prefix && lipgloss.Width(line)+1+wordW > maxWidth {
			// flush
			segs = append(segs, segment{content: line, runeStart: len([]rune(prefix)) + currentLineStartPos, runeCount: len([]rune(strings.TrimPrefix(line, prefix))) - func() int {
				if strings.HasPrefix(strings.TrimPrefix(line, prefix), " ") {
					return 1
				}
				return 0
			}()})
			line = prefix
			currentLineStartPos = wordPos[wi]
		}
		if line != prefix {
			line += " "
		} else {
			currentLineStartPos = wordPos[wi]
		}
		line += word
	}
	if line != prefix {
		segs = append(segs, segment{content: line, runeStart: len([]rune(prefix)) + currentLineStartPos, runeCount: len([]rune(strings.TrimPrefix(line, prefix))) - func() int {
			if strings.HasPrefix(strings.TrimPrefix(line, prefix), " ") {
				return 1
			}
			return 0
		}()})
	}
	if len(segs) == 0 {
		segs = []segment{{content: prefix, runeStart: 0, runeCount: len([]rune(prefix))}}
	}
	_ = bodyRunes
	return segs
}

// hitMapRowsForAI builds rows for roleAI text, mirroring RenderDeterministicPipeline
// and renderStreamingContent outer gutter composition exactly.
func hitMapRowsForAI(text string, recordIdx int32, width int) []RowLayout {
	text = ensurePreflightDelimiter(text)
	var rows []RowLayout
	// Outer gutter for AI is always "│ " (2 cells) applied in renderStreamingContent.
	const outerGutterCells = 2
	availableWidth := width - 2
	if availableWidth < 20 {
		availableWidth = 20
	}
	codeWidth := width - 6
	if codeWidth < 10 {
		codeWidth = 10
	}
	// Use same block parsing as RenderDeterministicPipeline to keep budgets identical.
	lines := strings.Split(text, "\n")
	inCodeBlock := false
	var blockLines []string
	language := ""
	for li := 0; li < len(lines); li++ {
		line := lines[li]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				// Emit header row for code block
				langLabel := language
				if langLabel == "" {
					langLabel = "code"
				}
				rows = append(rows, RowLayout{
					RecordIdx:    recordIdx,
					LogicalLine:  -1,
					PrefixCells:  outerGutterCells,
					ContentLen:   uint16(lipgloss.Width(langLabel)),
					RuneStartIdx: 0,
					RuneCount:    0,
				})
				// Emit each code line with inner gutter composition
				for codeIdx, codeLine := range blockLines {
					// Track logical line as block start + codeIdx
					ll := int32(li - len(blockLines) + codeIdx)
					if codeLine == "" {
						rows = append(rows, RowLayout{
							RecordIdx:    recordIdx,
							LogicalLine:  ll,
							PrefixCells:  outerGutterCells + 2,
							ContentLen:   0,
							RuneStartIdx: 0,
							RuneCount:    0,
						})
						continue
					}
					parts := strings.Split(ansi.Hardwrap(codeLine, codeWidth, true), "\n")
					runeOff := 0
					for _, part := range parts {
						runes := []rune(part)
						rows = append(rows, RowLayout{
							RecordIdx:    recordIdx,
							LogicalLine:  ll,
							PrefixCells:  outerGutterCells + 2,
							ContentLen:   uint16(lipgloss.Width(part)),
							RuneStartIdx: uint32(runeOff),
							RuneCount:    uint16(len(runes)),
						})
						runeOff += len(runes)
					}
				}
				inCodeBlock = false
				blockLines = nil
				language = ""
			} else {
				inCodeBlock = true
				language = strings.TrimPrefix(trimmed, "```")
				blockLines = nil
			}
			continue
		}
		if inCodeBlock {
			blockLines = append(blockLines, line)
			continue
		}
		// Not in code block
		if trimmed == "" {
			rows = append(rows, RowLayout{
				RecordIdx:    recordIdx,
				LogicalLine:  int32(li),
				PrefixCells:  outerGutterCells,
				ContentLen:   0,
				RuneStartIdx: 0,
				RuneCount:    0,
			})
			continue
		}
		// Preflight header is a distinct physical row (chrome) so response text starts on next row with RuneStartIdx 0
		if strings.Contains(line, "[preflight]") || strings.Contains(line, "snapshot ready") {
			rows = append(rows, RowLayout{
				RecordIdx:    recordIdx,
				LogicalLine:  -1,
				PrefixCells:  outerGutterCells,
				ContentLen:   uint16(lipgloss.Width(strings.TrimSpace(line))),
				RuneStartIdx: 0,
				RuneCount:    0,
			})
			continue
		}
		// Heading leading "\n" blank separator
		if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "#### ") {
			rows = append(rows, RowLayout{
				RecordIdx:    recordIdx,
				LogicalLine:  -1,
				PrefixCells:  outerGutterCells,
				ContentLen:   0,
				RuneStartIdx: 0,
				RuneCount:    0,
			})
		}
		// Strict 2-cell safety padding: availableWidth = viewport.Width - 4
		innerW := width - 4
		if innerW < 10 {
			innerW = 10
		}
		wrapW := innerW - markdownLinePrefixWidth(line) - 2
		if wrapW < 10 {
			wrapW = 10
		}
		// Determine markdown prefix stripping for rune mapping
		prefixRunes, strippedContent := splitMarkdownPrefix(line)
		wrapped := ansi.Wordwrap(line, wrapW, " \t")
		subLines := strings.Split(wrapped, "\n")
		// Track rune offset within strippedContent for continuation lines
		contentRunes := []rune(strippedContent)
		// For wrapped subLines, we need to find where each subLine's content appears in strippedContent.
		// ansi.Wordwrap splits on spaces preserving words; we can locate via progressive search.
		searchPos := 0
		for subIdx, subLine := range subLines {
			var subContent string
			var subPrefixCells int
			if subIdx == 0 {
				// First subLine includes markdown marker; extract content part after marker for hit mapping
				// subLine is something like "- hello world" or "1. Install..." or plain text
				_, subContent = splitMarkdownPrefix(subLine)
				if subContent == "" {
					// Fallback: subLine may be the markdown marker itself
					subContent = strings.TrimSpace(subLine)
				}
				subPrefixCells = outerGutterCells + markdownLinePrefixWidth(line)
			} else {
				subContent = strings.TrimSpace(subLine)
				subPrefixCells = outerGutterCells
			}
			// Find subContent in contentRunes starting at searchPos
			subRunes := []rune(subContent)
			runeStart := prefixRunes
			if len(contentRunes) > 0 && len(subRunes) > 0 {
				// Find subContent occurrence
				found := -1
				for p := searchPos; p+len(subRunes) <= len(contentRunes); p++ {
					match := true
					for k := 0; k < len(subRunes); k++ {
						if contentRunes[p+k] != subRunes[k] {
							match = false
							break
						}
					}
					if match {
						found = p
						break
					}
				}
				if found >= 0 {
					runeStart = prefixRunes + found
					searchPos = found + len(subRunes) + 1
				} else {
					runeStart = prefixRunes + searchPos
					searchPos += len(subRunes) + 1
				}
			} else if subIdx > 0 {
				runeStart = prefixRunes + searchPos
			}
			rows = append(rows, RowLayout{
				RecordIdx:    recordIdx,
				LogicalLine:  int32(li),
				PrefixCells:  uint8(subPrefixCells),
				ContentLen:   uint16(lipgloss.Width(subContent)),
				RuneStartIdx: uint32(runeStart),
				RuneCount:    uint16(len(subRunes)),
			})
			_ = prefixRunes
		}
	}
	if inCodeBlock && len(blockLines) > 0 {
		// Unclosed block — emit as code
		langLabel := language
		if langLabel == "" {
			langLabel = "code"
		}
		rows = append(rows, RowLayout{
			RecordIdx:    recordIdx,
			LogicalLine:  -1,
			PrefixCells:  outerGutterCells,
			ContentLen:   uint16(lipgloss.Width(langLabel)),
			RuneStartIdx: 0,
			RuneCount:    0,
		})
		for codeIdx, codeLine := range blockLines {
			ll := int32(len(lines) - len(blockLines) + codeIdx)
			if codeLine == "" {
				rows = append(rows, RowLayout{
					RecordIdx:    recordIdx,
					LogicalLine:  ll,
					PrefixCells:  outerGutterCells + 2,
					ContentLen:   0,
					RuneStartIdx: 0,
					RuneCount:    0,
				})
				continue
			}
			parts := strings.Split(ansi.Hardwrap(codeLine, codeWidth, true), "\n")
			runeOff := 0
			for _, part := range parts {
				runes := []rune(part)
				rows = append(rows, RowLayout{
					RecordIdx:    recordIdx,
					LogicalLine:  ll,
					PrefixCells:  outerGutterCells + 2,
					ContentLen:   uint16(lipgloss.Width(part)),
					RuneStartIdx: uint32(runeOff),
					RuneCount:    uint16(len(runes)),
				})
				runeOff += len(runes)
			}
		}
	}
	// Ensure at least one row per record
	if len(rows) == 0 {
		rows = append(rows, RowLayout{RecordIdx: recordIdx, LogicalLine: 0, PrefixCells: outerGutterCells, ContentLen: 0, RuneStartIdx: 0})
	}
	// Silence unused
	_ = availableWidth
	return rows
}

// splitMarkdownPrefix returns (prefixRuneCount, strippedContent) mirroring renderDeterministicInlineMarkdown's prefix logic.
func splitMarkdownPrefix(line string) (int, string) {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "> "):
		return 2, strings.TrimPrefix(strings.TrimSpace(line), "> ")
	case strings.HasPrefix(trimmed, "- [ ]"):
		return 5, strings.TrimSpace(trimmed[5:])
	case strings.HasPrefix(trimmed, "- [x]"):
		return 5, strings.TrimSpace(trimmed[5:])
	case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
		return 2, strings.TrimSpace(trimmed[2:])
	case strings.HasPrefix(trimmed, "### "):
		return 4, strings.TrimSpace(trimmed[4:])
	case strings.HasPrefix(trimmed, "## "):
		return 3, strings.TrimSpace(trimmed[3:])
	case strings.HasPrefix(trimmed, "# "):
		return 2, strings.TrimSpace(trimmed[2:])
	case strings.HasPrefix(trimmed, "#### "):
		return 5, strings.TrimSpace(trimmed[5:])
	}
	// Ordered list "N. "
	for i := 0; i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9'; i++ {
		if i+1 < len(trimmed) && trimmed[i+1] == '.' && i+2 < len(trimmed) && trimmed[i+2] == ' ' {
			return i + 3, strings.TrimSpace(trimmed[i+3:])
		}
	}
	// Separator "---"
	if trimmed == "---" || trimmed == "***" || trimmed == "___" {
		return len([]rune(trimmed)), ""
	}
	return 0, strings.TrimSpace(line)
}
