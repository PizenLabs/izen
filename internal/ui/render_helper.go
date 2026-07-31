package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// tabWidth is the column width substituted for a tab character during
// sanitization. Tabs render at terminal-dependent widths, so they are
// normalized to four spaces to keep column alignment deterministic.
const tabWidth = 4

// normalizeLineEndings converts CRLF and lone-CR line endings to a single LF
// so downstream newline-splitting logic never leaves a dangling \r that would
// break column alignment and indentation.
func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// expandTabs converts tab characters into spaces so indentation is preserved
// without breaking alignment on terminals with custom tab stops.
func expandTabs(s string) string {
	return strings.ReplaceAll(s, "\t", strings.Repeat(" ", tabWidth))
}

// sanitizeText is the unified pre-render sanitizer for streamed log, activity,
// and error text. It normalizes mixed line endings, converts literal \n/\t
// escape sequences (leaked by external processes) into control characters, and
// expands tabs to spaces. Every line appended to the view buffer should pass
// through this helper before rendering.
func sanitizeText(s string) string {
	s = normalizeLineEndings(s)
	s = sanitizeEscapes(s)
	return expandTabs(s)
}

// truncateANSI truncates s to at most maxW visual cells without splitting ANSI
// escape sequences or multi-byte runes. The ellipsis tail is included in the
// width budget. Never truncate by raw byte slicing — cutting an SGR sequence
// mid-way makes the terminal drop visible text.
func truncateANSI(s string, maxW int) string {
	if maxW < 1 {
		return ""
	}
	return ansi.Truncate(s, maxW, "...")
}

// chunkWord splits an unbreakable token into cell-aligned pieces of at most
// maxW visual cells each. Slicing is done at cell boundaries (ansi.Cut), so
// ANSI sequences and wide glyphs are never split.
func chunkWord(word string, maxW int) []string {
	if maxW < 1 {
		maxW = 1
	}
	var pieces []string
	for off := 0; off < ansi.StringWidth(word); {
		piece := ansi.Cut(word, off, off+maxW)
		if piece == "" {
			break
		}
		pieces = append(pieces, piece)
		off += lipgloss.Width(piece)
	}
	return pieces
}

// wrapPlainLine word-wraps a single line (no embedded newlines) to maxW visual
// cells. Line widths are measured with lipgloss.Width (cell width, ANSI-aware)
// rather than byte length, and overlong unbreakable tokens are hard-chunked at
// cell boundaries so no line can overflow the bound.
func wrapPlainLine(line string, maxW int) []string {
	if maxW < 1 {
		maxW = 1
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}

	var result []string
	var current strings.Builder
	curWidth := 0
	flush := func() {
		if curWidth > 0 {
			result = append(result, current.String())
		}
		current.Reset()
		curWidth = 0
	}

	for _, word := range words {
		wordW := lipgloss.Width(word)
		if wordW > maxW {
			// Overlong unbreakable token: flush the pending line, then emit
			// cell-aligned chunks, each on its own line.
			flush()
			result = append(result, chunkWord(word, maxW)...)
			continue
		}
		if curWidth > 0 && curWidth+1+wordW > maxW {
			flush()
		}
		if curWidth > 0 {
			current.WriteString(" ")
			curWidth++
		}
		current.WriteString(word)
		curWidth += wordW
	}
	flush()
	if len(result) == 0 {
		result = []string{line}
	}
	return result
}

// wrapText wraps a multi-line block of text to maxW visual cells per line,
// preserving explicit newlines as hard breaks (per-line wrap, never a single
// reflow pass that would collapse structured output).
func wrapText(text string, maxW int) []string {
	if text == "" {
		return []string{""}
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapPlainLine(line, maxW)...)
	}
	return out
}

// markdownLinePrefixWidth returns the number of visual cells that the inline
// markdown pass prepends to a styled line (bullet, checkbox, ordered number,
// blockquote, or H3 marker). Wrapping must reserve this width on the first
// line so the styled output lands exactly on the boundary instead of
// overflowing it — this prevents ragged right edges and orphan symbols when
// already-styled content is laid out beside a fixed-width marker.
func markdownLinePrefixWidth(line string) int {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "> "):
		// Blockquote: "┃ " gutter marker.
		return 2
	case strings.HasPrefix(trimmed, "- [ ]"), strings.HasPrefix(trimmed, "- [x]"):
		// Checkbox: status icon (1 cell) + space.
		return 2
	case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
		// Bullet: "• " marker.
		return 2
	case strings.HasPrefix(trimmed, "### "):
		// H3: "▸ " marker.
		return 2
	}
	// Ordered list: "N. " renders as styled "N." plus a space; the marker
	// grows one cell per digit.
	for i := 0; i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9'; i++ {
		if i+1 < len(trimmed) && trimmed[i+1] == '.' && i+2 < len(trimmed) && trimmed[i+2] == ' ' {
			return i + 3
		}
	}
	return 0
}
