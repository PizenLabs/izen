package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

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

// renderCodeBlock renders a fenced code block with optional language label and
// diff highlighting. Uses the exact same styles as the historical code path.
// Code lines are hard-wrapped to (width - 4) to prevent terminal overflow while
// preserving indentation structure.
func renderCodeBlock(language string, lines []string, width int) string {
	if len(lines) == 0 {
		return ""
	}

	var builder strings.Builder

	// Code block content width — same safety margin as prose text
	codeWidth := width - 4
	if codeWidth < 10 {
		codeWidth = 10
	}

	if language != "" {
		builder.WriteString(mdMutedStyle.Render(language))
		builder.WriteString("\n")
	}

	isDiff := strings.HasPrefix(language, "diff")

	for i, line := range lines {
		// Hard-wrap long code lines at character level to preserve structure
		wrappedLine := ansi.Hardwrap(line, codeWidth, true)
		wrappedParts := strings.Split(wrappedLine, "\n")

		for j, part := range wrappedParts {
			if i > 0 || j > 0 {
				builder.WriteString("\n")
			}

			if isDiff {
				trimmed := strings.TrimSpace(part)
				switch {
				case strings.HasPrefix(part, "+") && !strings.HasPrefix(part, "+++"):
					builder.WriteString(diffAddBgStyle.Render(part))
				case strings.HasPrefix(part, "-") && !strings.HasPrefix(part, "---"):
					builder.WriteString(diffDelBgStyle.Render(part))
				case strings.HasPrefix(trimmed, "@@"):
					builder.WriteString(diffHunkStyle.Render(part))
				default:
					builder.WriteString(mdCodeContStyle.Render(part))
				}
			} else {
				builder.WriteString(mdCodeContStyle.Render(part))
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
			container.WriteString(mutedStyle.Render("[A] Run  [R] Skip"))
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
