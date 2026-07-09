package ui

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// ── Catppuccin Mocha ANSI true-color escape sequences ────────────────────
var (
	ansiReset    = "\x1b[0m"
	ansiText     = "\x1b[38;2;205;214;244m" // #cdd6f4 Foreground
	ansiKeyword  = "\x1b[38;2;203;166;247m" // #cba6f7 Mauve
	ansiString   = "\x1b[38;2;166;227;161m" // #a6e3a1 Green
	ansiComment  = "\x1b[38;2;108;112;134m" // #6c7086 Overlay 0
	ansiNumber   = "\x1b[38;2;250;179;135m" // #fab387 Peach
	ansiFunction = "\x1b[38;2;137;180;250m" // #89b4fa Blue
)

// tokenTypeColor maps a Chroma token type to its ANSI true-color sequence
// using the Catppuccin Mocha palette. Handles partial/incomplete tokens
// safely — unknown types default to the foreground text color.
func tokenTypeColor(t chroma.TokenType) string {
	switch {
	case t >= chroma.Keyword && t <= chroma.KeywordType:
		return ansiKeyword
	case t >= chroma.NameFunction && t <= chroma.NameFunctionMagic:
		return ansiFunction
	case t >= chroma.String && t <= chroma.StringSymbol:
		return ansiString
	case t >= chroma.Comment && t <= chroma.CommentPreprocFile:
		return ansiComment
	case t >= chroma.LiteralNumber && t <= chroma.LiteralNumberOct:
		return ansiNumber
	case t == chroma.GenericDeleted:
		return ansiKeyword
	case t == chroma.GenericInserted || t == chroma.GenericEmph:
		return ansiString
	case t == chroma.GenericHeading || t == chroma.GenericStrong:
		return ansiFunction
	default:
		return ansiText
	}
}

// RenderDeterministicPipeline handles complete and partial/streaming blocks identically.
// It uses strings.Split to guarantee a finite loop iteration count, preventing any
// possibility of a deadlock. Lines are processed with a state machine for code fences;
// text lines pass through inline markdown styling using the same style constants as history.
func RenderDeterministicPipeline(rawInput string, width int, isStreaming bool) string {
	if rawInput == "" {
		return ""
	}

	var result strings.Builder

	// Split purely by newline to guarantee a finite loop slice size
	lines := strings.Split(rawInput, "\n")

	inCodeBlock := false
	var currentBlockLines []string
	var language string

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inCodeBlock {
				result.WriteString(renderCodeBlock(language, currentBlockLines, width) + "\n")
				inCodeBlock = false
				currentBlockLines = nil
			} else {
				inCodeBlock = true
				language = strings.TrimPrefix(line, "```")
			}
			continue
		}

		if inCodeBlock {
			currentBlockLines = append(currentBlockLines, line)
		} else {
			// Handle empty lines cleanly
			if strings.TrimSpace(line) == "" {
				result.WriteString("\n")
				continue
			}

			// WRAP FIX: word-wrap long lines to fit the terminal viewport before applying styles.
			// Using a safety margin of -4 so text never kisses the raw edge of the terminal frame.
			wrappedLine := lipgloss.NewStyle().Width(width - 4).Render(line)

			subLines := strings.Split(wrappedLine, "\n")
			for _, subLine := range subLines {
				result.WriteString(renderDeterministicInlineMarkdown(subLine, width) + "\n")
			}
		}
	}

	// FAIL-SAFE EXTRACTION: If stream cuts off inside an open block, render partial content
	if inCodeBlock && len(currentBlockLines) > 0 {
		result.WriteString(renderCodeBlock(language, currentBlockLines, width))
	}

	return strings.TrimSuffix(result.String(), "\n")
}

// renderDeterministicInlineMarkdown processes a single line of text, applying
// block-level syntax (headings, blockquotes, lists, horizontal rules) and then
// inline styles (bold, italic, code, links).
func renderDeterministicInlineMarkdown(line string, width int) string {
	if line == "" {
		return ""
	}

	trimmed := strings.TrimSpace(line)

	switch {
	case strings.HasPrefix(trimmed, "> "):
		rest := strings.TrimPrefix(line, "> ")
		return mdAccentStyle.Render("┃") + " " + applyInlineStyles(rest)

	case trimmed == "---" || trimmed == "***" || trimmed == "___":
		return mdMutedStyle.Render(strings.Repeat("─", width))

	case strings.HasPrefix(line, "#### "):
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

// renderCodeBlock renders a fenced code block with Chroma syntax highlighting
// and ANSI-safe inline wrapping. The pipeline is: tokenize → newline fragment →
// rune-level wrap using visual character widths. Partial/incomplete code
// streams (e.g. mid-keyword truncation) are handled gracefully without errors.
func renderCodeBlock(language string, lines []string, width int) string {
	if len(lines) == 0 {
		return ""
	}

	var builder strings.Builder

	codeWidth := width - 4
	if codeWidth < 10 {
		codeWidth = 10
	}

	if language != "" {
		builder.WriteString(mdMutedStyle.Render(language))
		builder.WriteString("\n")
	}

	rawCode := strings.Join(lines, "\n")

	// Resolve Chroma lexer — fallback to Fallback if language is unknown/unset
	lexer := lexers.Get(language)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	iterator, err := lexer.Tokenise(nil, rawCode)
	if err != nil {
		// Fallback: plain rendering with no syntax highlighting
		for i, line := range lines {
			if i > 0 {
				builder.WriteString("\n")
			}
			wrapped := ansi.Hardwrap(line, codeWidth, true)
			parts := strings.Split(wrapped, "\n")
			for j, part := range parts {
				if j > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(mdCodeContStyle.Render(part))
			}
		}
		return builder.String()
	}

	tokens := iterator.Tokens()

	// Single-pass token-to-line-engine: iterate tokens, split on newlines,
	// and wrap using RuneWidth for visual width safety.
	currentLineLen := 0

	for _, token := range tokens {
		ansiStart := tokenTypeColor(token.Type)
		text := token.Value

		// Chunk token values on literal newlines
		fragments := strings.Split(text, "\n")
		for fi, frag := range fragments {
			if fi > 0 {
				builder.WriteByte('\n')
				currentLineLen = 0
			}
			if frag == "" {
				continue
			}

			var chunk []rune
			chunkLen := 0

			for _, rn := range frag {
				rw := runewidth.RuneWidth(rn)
				if currentLineLen+rw > codeWidth && chunkLen > 0 {
					builder.WriteString(ansiStart)
					builder.WriteString(string(chunk))
					builder.WriteString(ansiReset)
					builder.WriteByte('\n')
					currentLineLen = 0
					chunk = nil
					chunkLen = 0
				}
				chunk = append(chunk, rn)
				chunkLen += rw
				currentLineLen += rw
			}

			if chunkLen > 0 {
				builder.WriteString(ansiStart)
				builder.WriteString(string(chunk))
				builder.WriteString(ansiReset)
			}
		}
	}

	return builder.String()
}

// ── Streaming Content Renderer (now delegates to DeterministicPipeline) ─────

// renderStreamingContent renders AI content incrementally during an active
// LLM stream. It uses parseAIContent for block classification (plans, diffs,
// tables, etc.) and delegates plain text blocks to the deterministic pipeline.
//
// This guarantees zero layout shift: the exact same rendering logic is used
// whether the content is still growing or fully complete.
func (m *model) renderStreamingContent(content string, width int) string {
	blocks := parseAIContent(content)
	var renderedBlocks []string

	availableWidth := width - 2
	if availableWidth < 20 {
		availableWidth = 20
	}
	widgetInnerWidth := availableWidth - 2
	if widgetInnerWidth < 18 {
		widgetInnerWidth = 18
	}

	gutter := gutterAIStyle.Render("▌") + " "

	for _, block := range blocks {
		var rendered string
		switch block.kind {
		case blockPlan:
			planLines := strings.Split(block.raw, "\n")
			var contentLines []string
			for _, pl := range planLines {
				plTrim := strings.TrimSpace(pl)
				if plTrim == "" {
					continue
				}
				if strings.HasPrefix(strings.ToLower(plTrim), "plan") || strings.HasPrefix(plTrim, "#") {
					continue
				}
				item := plTrim
				var prefixChar string
				var prefixStyle lipgloss.Style
				var text string

				switch {
				case strings.HasPrefix(item, "- [x]") || strings.HasPrefix(item, "[x]") || strings.HasPrefix(item, "✓"):
					prefixChar = "✓ "
					prefixStyle = greenStyle
					text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [x]"), "[x]"), "✓"))
				case strings.HasPrefix(item, "- [/]") || strings.HasPrefix(item, "[/]") || strings.HasPrefix(item, "●"):
					prefixChar = "● "
					prefixStyle = orangeStyle
					text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [/]"), "[/]"), "●"))
				case strings.HasPrefix(item, "- [ ]") || strings.HasPrefix(item, "[ ]") || strings.HasPrefix(item, "○"):
					prefixChar = "○ "
					prefixStyle = dimmedStyle
					text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [ ]"), "[ ]"), "○"))
				case strings.HasPrefix(item, "✗"):
					prefixChar = "✗ "
					prefixStyle = redStyle
					text = strings.TrimSpace(strings.TrimPrefix(item, "✗"))
				default:
					prefixChar = "• "
					prefixStyle = textStyle
					text = item
				}

				wrapW := widgetInnerWidth - 2
				if wrapW < 10 {
					wrapW = 10
				}
				wrappedText := wrapStreamText(text, wrapW)

				for idx, line := range wrappedText {
					if idx == 0 {
						contentLines = append(contentLines, prefixStyle.Render(prefixChar)+line)
					} else {
						contentLines = append(contentLines, "  "+line)
					}
				}
			}
			rendered = renderWidget("Plan", strings.Join(contentLines, "\n"), availableWidth, colorModePlan)

		case blockDiff:
			file, symbol, linesRange, cleanDiff := parseDiffMetadata(block.raw)
			dr := &DiffRenderer{Width: availableWidth}
			diffRendered := dr.Render(ToDiffCardViewModel(cleanDiff))

			var details []string
			if file != "" {
				details = append(details, accentStyle.Render("File:   "+file))
			}
			if symbol != "" {
				details = append(details, blueStyle.Render("Symbol: "+symbol))
			}
			if linesRange != "" {
				details = append(details, mutedStyle.Render("Range:  "+linesRange))
			}

			var fullContent string
			if len(details) > 0 {
				fullContent = strings.Join(details, "\n") + "\n" + subtleStyle.Render(strings.Repeat("─", widgetInnerWidth)) + "\n" + diffRendered
			} else {
				fullContent = diffRendered
			}
			rendered = renderWidget("Edit", fullContent, availableWidth, colorModeBuild)

		case blockTable:
			tableContent := renderTable(block.raw, widgetInnerWidth)
			rendered = renderWidget("Table", tableContent, availableWidth, colorAccent)

		case blockEvidence:
			lines := strings.Split(block.raw, "\n")
			var wrappedLines []string
			for _, line := range lines {
				wrappedLines = append(wrappedLines, wrapStreamText(line, widgetInnerWidth)...)
			}
			rendered = renderWidget("Evidence", strings.Join(wrappedLines, "\n"), availableWidth, colorModeInvestigate)

		case blockRisk:
			lines := strings.Split(block.raw, "\n")
			var wrappedLines []string
			for _, line := range lines {
				wrappedLines = append(wrappedLines, wrapStreamText(line, widgetInnerWidth)...)
			}
			rendered = renderWidget("Risk Analysis", strings.Join(wrappedLines, "\n"), availableWidth, colorModeReview)

		case blockCommand:
			cmdText := strings.TrimSpace(block.raw)
			if strings.HasPrefix(cmdText, "```") {
				cmdLines := strings.Split(cmdText, "\n")
				if len(cmdLines) > 2 {
					cmdText = strings.Join(cmdLines[1:len(cmdLines)-1], "\n")
				}
			}

			var container strings.Builder
			container.WriteString(shellWarningStyle.Render("> System: Shell Execution Required <"))
			container.WriteString("\n")

			cmdLines := strings.Split(cmdText, "\n")
			for _, cl := range cmdLines {
				container.WriteString("  ")
				container.WriteString(textStyle.Render("$ " + cl))
				container.WriteString("\n")
			}
			container.WriteString("\n")
			container.WriteString(mutedStyle.Render("[Alt+A] Run  [Alt+R] Skip"))
			container.WriteString("\n")

			rendered = renderWidget("Command", container.String(), availableWidth, colorDimmed)

		default:
			// UNIFIED PATH: deterministic pipeline — identical for streaming and history
			blockRendered := RenderDeterministicPipeline(block.raw, availableWidth, true)
			if blockRendered != "" {
				mdLines := strings.Split(strings.TrimRight(blockRendered, "\n"), "\n")
				var styledLines []string
				for _, line := range mdLines {
					styledLines = append(styledLines, gutter+line)
				}
				rendered = strings.Join(styledLines, "\n")
			}
		}

		if rendered != "" {
			renderedBlocks = append(renderedBlocks, rendered)
		}
	}

	return strings.Join(renderedBlocks, "\n\n")
}
