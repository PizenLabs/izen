package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type LexerState int

const (
	StateText LexerState = iota
	StateInCodeBlock
	StateInTable
)

type IncrementalStreamParser struct {
	state     LexerState
	fenceLang string
	lineBuf   strings.Builder
	width     int
}

func NewIncrementalStreamParser(width int) *IncrementalStreamParser {
	return &IncrementalStreamParser{
		state: StateText,
		width: width,
	}
}

func (p *IncrementalStreamParser) Reset() {
	p.state = StateText
	p.fenceLang = ""
	p.lineBuf.Reset()
}

func (p *IncrementalStreamParser) SetWidth(w int) {
	p.width = w
}

func (p *IncrementalStreamParser) Width() int {
	return p.width
}

func (p *IncrementalStreamParser) ProcessChunk(chunk string) []string {
	p.lineBuf.WriteString(chunk)
	content := p.lineBuf.String()

	lastNewline := strings.LastIndex(content, "\n")
	if lastNewline == -1 {
		return nil
	}

	complete := content[:lastNewline]
	p.lineBuf.Reset()
	p.lineBuf.WriteString(content[lastNewline+1:])

	lines := strings.Split(complete, "\n")
	if len(lines) == 0 {
		return nil
	}

	result := make([]string, 0, len(lines)*2)
	for _, line := range lines {
		processed := p.processLine(line)
		result = append(result, strings.Split(p.wrapLine(processed), "\n")...)
	}
	return result
}

func (p *IncrementalStreamParser) Flush() []string {
	if p.lineBuf.Len() == 0 {
		return nil
	}
	line := p.lineBuf.String()
	p.lineBuf.Reset()
	processed := p.processLine(line)
	wrapped := p.wrapLine(processed)
	if wrapped == "" {
		return nil
	}
	return strings.Split(wrapped, "\n")
}

// wrapLine wraps an ANSI-styled line to the parser's configured width, preserving
// ANSI escape sequences. Text lines are word-wrapped at space boundaries; code
// lines are hard-wrapped to preserve indentation structure.
func (p *IncrementalStreamParser) wrapLine(line string) string {
	wrapAt := p.width - 2
	if wrapAt < 10 {
		wrapAt = 10
	}
	switch p.state {
	case StateInCodeBlock:
		return ansi.Hardwrap(line, wrapAt, true)
	case StateInTable:
		return line
	default:
		return ansi.Wordwrap(line, wrapAt, " ")
	}
}

func (p *IncrementalStreamParser) processLine(line string) string {
	line = strings.TrimRight(line, "\r")
	switch p.state {
	case StateInCodeBlock:
		return p.processCodeLine(line)
	case StateInTable:
		return p.processTableLine(line)
	default:
		return p.processTextLine(line)
	}
}

func (p *IncrementalStreamParser) processTextLine(line string) string {
	trimmed := strings.TrimSpace(line)

	if strings.HasPrefix(trimmed, "```") {
		lang := strings.TrimPrefix(trimmed, "```")
		p.state = StateInCodeBlock
		p.fenceLang = lang
		return mdCodeContStyle.Render(line)
	}

	if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
		p.state = StateInTable
		return p.processTableLine(line)
	}

	if strings.HasPrefix(line, "> ") {
		rest := strings.TrimPrefix(line, "> ")
		return mdAccentStyle.Render("┃") + " " + applyInlineStyles(rest)
	}

	switch {
	case strings.HasPrefix(line, "####"):
		return mdH4Style.Render(strings.TrimSpace(line[4:]))
	case strings.HasPrefix(line, "### "):
		return mdH3Style.Render(strings.TrimSpace(line[4:]))
	case strings.HasPrefix(line, "## "):
		return mdH2Style.Render(strings.TrimSpace(line[3:]))
	case strings.HasPrefix(line, "# "):
		return mdH1Style.Render(strings.TrimSpace(line[2:]))
	}

	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
		content := strings.TrimSpace(trimmed[2:])
		return mdBulletStyle.Render("• ") + applyInlineStyles(content)
	}

	if len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && trimmed[1] == '.' && trimmed[2] == ' ' {
		prefix := trimmed[:2]
		content := strings.TrimSpace(trimmed[3:])
		return mdBulletStyle.Render(prefix) + " " + applyInlineStyles(content)
	}

	if strings.HasPrefix(trimmed, "- [ ]") {
		content := strings.TrimSpace(trimmed[5:])
		return dimmedStyle.Render("○ ") + applyInlineStyles(content)
	}
	if strings.HasPrefix(trimmed, "- [x]") {
		content := strings.TrimSpace(trimmed[5:])
		return greenStyle.Render("✓ ") + applyInlineStyles(content)
	}

	return applyInlineStyles(line)
}

func (p *IncrementalStreamParser) processCodeLine(line string) string {
	trimmed := strings.TrimSpace(line)

	if strings.HasPrefix(trimmed, "```") {
		p.state = StateText
		p.fenceLang = ""
		return dimmedStyle.Render(line)
	}

	if p.fenceLang == "diff" || p.fenceLang == "diff-bash" {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			return diffAddBgStyle.Render(line)
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			return diffDelBgStyle.Render(line)
		}
		if strings.HasPrefix(line, "@@") {
			return diffHunkStyle.Render(line)
		}
	}

	return mdCodeContStyle.Render(line)
}

func (p *IncrementalStreamParser) processTableLine(line string) string {
	trimmed := strings.TrimSpace(line)

	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		p.state = StateText
		return applyInlineStyles(line)
	}

	if strings.Contains(trimmed, "---") {
		clean := strings.ReplaceAll(trimmed, "|", "")
		clean = strings.ReplaceAll(clean, "-", "")
		clean = strings.ReplaceAll(clean, " ", "")
		if clean == "" {
			return dimmedStyle.Render(strings.Repeat("─", p.width))
		}
	}

	parts := strings.Split(trimmed, "|")
	var cells []string
	for _, part := range parts {
		cell := strings.TrimSpace(part)
		if cell != "" {
			cells = append(cells, cell)
		}
	}

	if len(cells) == 0 {
		return textStyle.Render(line)
	}

	return "│ " + strings.Join(cells, " │ ") + " │"
}

type inlineSegment struct {
	text  string
	style int
}

const (
	segPlain = iota
	segBold
	segItalic
	segCode
)

func applyInlineStyles(line string) string {
	if !strings.ContainsAny(line, "*`") {
		return textStyle.Render(line)
	}

	segments := parseInlineSegments(line)
	if len(segments) == 0 {
		return textStyle.Render(line)
	}

	var out strings.Builder
	for _, seg := range segments {
		switch seg.style {
		case segBold:
			out.WriteString(mdStrongStyle.Render(seg.text))
		case segItalic:
			out.WriteString(mdEmphasisStyle.Render(seg.text))
		case segCode:
			out.WriteString(mdCodeSpanStyle.Render(seg.text))
		default:
			out.WriteString(textStyle.Render(seg.text))
		}
	}
	return out.String()
}

func parseInlineSegments(line string) []inlineSegment {
	var segs []inlineSegment
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		if runes[i] == '`' {
			start := i + 1
			end := -1
			for j := start; j < n; j++ {
				if runes[j] == '`' {
					end = j
					break
				}
			}
			if end >= start {
				segs = append(segs, inlineSegment{text: string(runes[start:end]), style: segCode})
				i = end + 1
				continue
			}
		}

		if i+1 < n && runes[i] == '*' && runes[i+1] == '*' {
			start := i + 2
			end := -1
			for j := start; j+1 < n; j++ {
				if runes[j] == '*' && runes[j+1] == '*' {
					end = j
					break
				}
			}
			if end >= start {
				segs = append(segs, inlineSegment{text: string(runes[start:end]), style: segBold})
				i = end + 2
				continue
			}
		}

		if runes[i] == '*' {
			start := i + 1
			end := -1
			for j := start; j < n; j++ {
				if runes[j] == '*' {
					if j+1 < n && runes[j+1] == '*' {
						break
					}
					end = j
					break
				}
			}
			if end >= start {
				segs = append(segs, inlineSegment{text: string(runes[start:end]), style: segItalic})
				i = end + 1
				continue
			}
		}

		// Accumulate plain text
		start := i
		for i < n && runes[i] != '*' && runes[i] != '`' {
			i++
		}
		if i > start {
			segs = append(segs, inlineSegment{text: string(runes[start:i]), style: segPlain})
		}
	}

	return segs
}
