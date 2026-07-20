package ui

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Catppuccin Mocha ANSI true-color escape sequences (subdued) ─────────
var (
	ansiReset    = "\x1b[0m"
	ansiText     = "\x1b[38;2;205;214;244m" // #cdd6f4 Foreground
	ansiKeyword  = "\x1b[38;2;203;166;247m" // #cba6f7 Muted lavender
	ansiString   = "\x1b[38;2;166;227;161m" // #a6e3a1 Soft green
	ansiComment  = "\x1b[38;2;108;112;134m" // #6c7086 Muted gray
	ansiNumber   = "\x1b[38;2;250;179;135m" // #fab387 Soft amber
	ansiFunction = "\x1b[38;2;137;180;250m" // #89b4fa Muted blue
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
		return ansiText
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
		// H4: dimmed — supporting info, metadata-like
		return mdH4Style.Render(strings.TrimSpace(line[5:]))

	case strings.HasPrefix(line, "### "):
		// H3: blue — section subheadings
		return "\n" + mdH3Style.Render("▸ "+strings.TrimSpace(line[4:]))

	case strings.HasPrefix(line, "## "):
		// H2: bold text — major section heading
		return "\n" + mdH2Style.Render(strings.TrimSpace(line[3:]))

	case strings.HasPrefix(line, "# "):
		// H1: bold accent green — document title level
		return "\n" + mdH1Style.Render(strings.TrimSpace(line[2:]))
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
		return dimmedStyle.Render(Icon.Pending+" ") + applyInlineStyles(content)
	}
	if strings.HasPrefix(trimmed, "- [x]") {
		content := strings.TrimSpace(trimmed[5:])
		return greenStyle.Render(Icon.Done+" ") + applyInlineStyles(content)
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

	codeWidth := width - 6
	if codeWidth < 10 {
		codeWidth = 10
	}

	// Language header line with monochrome icon
	langLabel := language
	if langLabel == "" {
		langLabel = "code"
	}
	headerPad := width - lipgloss.Width("  "+langLabel) - 2
	if headerPad < 0 {
		headerPad = 0
	}
	builder.WriteString(dimmedStyle.Render("│ ") + dimmedStyle.Render(langLabel))
	builder.WriteString("\n")

	rawCode := strings.Join(lines, "\n")

	// Resolve Chroma lexer — fallback to Fallback if language is unknown/unset
	lexer := lexers.Get(language)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	iterator, err := lexer.Tokenise(nil, rawCode)
	if err != nil {
		// Fallback: plain rendering with left-anchor gutter
		for i, line := range lines {
			if i > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(dimmedStyle.Render("│ "))
			wrapped := ansi.Hardwrap(line, codeWidth, true)
			parts := strings.Split(wrapped, "\n")
			for j, part := range parts {
				if j > 0 {
					builder.WriteString("\n" + dimmedStyle.Render("│ "))
				}
				builder.WriteString(mdCodeContStyle.Render(part))
			}
		}
		return builder.String()
	}

	tokens := iterator.Tokens()

	// Single-pass token-to-line-engine with left-anchor gutter on every line
	currentLineLen := 0
	firstOnLine := true

	for _, token := range tokens {
		ansiStart := tokenTypeColor(token.Type)
		text := token.Value

		// Chunk token values on literal newlines
		fragments := strings.Split(text, "\n")
		for fi, frag := range fragments {
			if fi > 0 {
				builder.WriteByte('\n')
				currentLineLen = 0
				firstOnLine = true
			}
			if frag == "" {
				continue
			}

			// Emit gutter anchor at the start of each new line
			if firstOnLine {
				builder.WriteString(dimmedStyle.Render("│ "))
				firstOnLine = false
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
					builder.WriteString(dimmedStyle.Render("│ "))
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

	_ = headerPad
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

	gutter := gutterAIStyle.Render("│") + " "

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

				// Detect operational status icons for structured task lines
				switch {
				case strings.Contains(item, "SHELL_EXEC"):
					prefixChar = Icon.ShellExec + " "
					prefixStyle = orangeStyle
				case strings.Contains(item, "FILE_MUTATE"), strings.Contains(item, "DIFF_PATCH"), strings.Contains(item, "ATOMIC_REPLACE"):
					prefixChar = Icon.SrcPatch + " "
					prefixStyle = blueStyle
				case strings.Contains(item, "GIT_ACTION"):
					prefixChar = Icon.ShellExec + " "
					prefixStyle = orangeStyle
				default:
					switch {
					case strings.HasPrefix(item, "- [x]") || strings.HasPrefix(item, "[x]") || strings.HasPrefix(item, "✓"):
						prefixChar = Icon.Done + " "
						prefixStyle = greenStyle
						text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [x]"), "[x]"), "✓"))
					case strings.HasPrefix(item, "- [/]") || strings.HasPrefix(item, "[/]") || strings.HasPrefix(item, "●"):
						prefixChar = Icon.ShellExec + " "
						prefixStyle = orangeStyle
						text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [/]"), "[/]"), "●"))
					case strings.HasPrefix(item, "- [ ]") || strings.HasPrefix(item, "[ ]") || strings.HasPrefix(item, "○"):
						prefixChar = Icon.Pending + " "
						prefixStyle = dimmedStyle
						text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [ ]"), "[ ]"), "○"))
					case strings.HasPrefix(item, "✗"):
						prefixChar = Icon.Cross + " "
						prefixStyle = redStyle
						text = strings.TrimSpace(strings.TrimPrefix(item, "✗"))
					default:
						prefixChar = Icon.Pending + " "
						prefixStyle = textStyle
						text = item
					}
				}

				stripTypePrefix := func(s string) string {
					for _, p := range []string{"SHELL_EXEC:", "FILE_MUTATE:", "GIT_ACTION:", "DIFF_PATCH:", "ATOMIC_REPLACE:"} {
						if idx := strings.Index(s, p); idx >= 0 {
							return strings.TrimSpace(s[idx+len(p):])
						}
					}
					return s
				}
				if prefixChar == Icon.ShellExec+" " || prefixChar == Icon.SrcPatch+" " {
					text = stripTypePrefix(item)
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
			// Phase 4: Always render plan blocks through the widget layout engine.
			// The LLM returns raw task data; the UI applies the │ borders, operational
			// status icons, and color blocks deterministically via renderWidget.
			rendered = renderWidget("Plan", strings.Join(contentLines, "\n"), availableWidth, colorModePlan)

		case blockDiff:
			file, symbol, linesRange, cleanDiff := parseDiffMetadata(block.raw)
			dr := &DiffRenderer{Width: availableWidth, Language: langFromPath(file)}
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
				fullContent = strings.Join(details, "\n") + "\n\n" + diffRendered
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
			cmdLines := strings.Split(cmdText, "\n")
			for _, cl := range cmdLines {
				cl = strings.TrimRight(cl, " \r")
				if cl == "" {
					container.WriteString("\n")
					continue
				}
				// Indented with a semantic shell prompt marker (orange =
				// execution) so commands stand out from surrounding prose.
				container.WriteString("  ")
				container.WriteString(orangeStyle.Render("$"))
				container.WriteString(" " + textStyle.Render(cl) + "\n")
			}
			container.WriteString("\n")
			container.WriteString("  " + boldTextStyle.Render(Icon.Action+" Run") + " " + dimmedStyle.Render("[Alt+A]") +
				"   " + boldTextStyle.Render(Icon.Action+" Skip") + " " + dimmedStyle.Render("[Alt+R]") + "\n")

			rendered = renderWidget("Command", container.String(), availableWidth, colorModePlan)

		default:
			// INTERCEPT: JSON plan widget — ONLY for /plan and /build modes where
			// the model is instructed to output structured JSON. For /ask, /review,
			// and /investigate this MUST be skipped entirely to prevent accidental
			// JSON parsing of plain Markdown content (which can trigger terminal-
			// corrupting stderr log spam from ParseJSONPlan's token-limit guard).
			if m.resolver.Current() == modes.ModePlan || m.resolver.Current() == modes.ModeBuild {
				if jsonResult := plan.ParseJSONPlan(block.raw); jsonResult != nil && jsonResult.Valid && jsonResult.Plan != nil {
					if m.resolver.Current() == modes.ModeBuild {
						rendered = renderWidget("Execution Error",
							textStyle.Render("Model returned a /plan JSON contract instead of a code patch. "+
								"The plan phase is complete — re-run the task or refine the instruction to force patch output."),
							availableWidth, colorModeReview)
						break
					}
					rendered = renderJSONPlanWidget(jsonResult.Plan, m.planStatusSource(), availableWidth)
					break
				}
			}

			// Phase 4: In /plan mode, force ALL content through the plan widget
			// layout engine (│ borders, ◉ markers, color blocks) so the LLM's
			// raw text is always wrapped in the deterministic UI frame.
			if m.resolver.Current() == modes.ModePlan {
				lines := strings.Split(block.raw, "\n")
				var cleanLines []string
				for _, l := range lines {
					t := strings.TrimSpace(l)
					if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "---") {
						continue
					}
					cleanLines = append(cleanLines, t)
				}
				if len(cleanLines) > 0 {
					rendered = renderWidget("Plan", strings.Join(cleanLines, "\n"), availableWidth, colorModePlan)
				}
				break
			}

			// UNIFIED PATH: deterministic pipeline — identical for streaming and history.
			// Replaces the goldmark-based MarkdownRenderer to eliminate layout flicker.
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

	return strings.Join(renderedBlocks, vspace(Spacing.Section))
}

// planStatusSource exposes the live /plan task ledger as a plan.TaskStatusSource
// for the checklist renderer. It returns a genuine nil interface (not a typed
// nil *TaskLedger) when no ledger is attached, so callers can safely nil-check
// without risking a nil-pointer panic inside IsCompleted.
func (m *model) planStatusSource() plan.TaskStatusSource {
	if m.buildLedger == nil {
		return nil
	}
	return m.buildLedger
}

// planTrackIcon maps a Task to its track classification (ENV_DEPS, CODE_MOD, VERIFY)
// and returns the icon+track label for the enriched plan display.
// SHELL_EXEC dependency commands → 📦 [ENV_DEPS]
// FILE_MUTATE/DIFF_PATCH/ATOMIC_REPLACE → 📝 [CODE_MOD]
// Verification commands (go test, go build, etc.) → 🧪 [VERIFY]
func planTrackIcon(t plan.Task) (string, string) {
	icon, label := enrichedTrack(t.Type, t.Target)
	return icon, label
}

// enrichedTrack maps a strategy+target pair to the enriched track classification.
// Used by both planTrackIcon (Task) and renderJSONPlanWidget (AtomicTask).
func enrichedTrack(strategy, target string) (string, string) {
	upper := strings.ToUpper(strategy)
	lower := strings.ToLower(strings.TrimSpace(target))
	switch upper {
	case "SHELL_EXEC", "GIT_ACTION":
		if strings.HasPrefix(lower, "go test") || strings.HasPrefix(lower, "go build") ||
			strings.HasPrefix(lower, "npm test") || strings.HasPrefix(lower, "cargo test") ||
			strings.HasPrefix(lower, "pytest") || strings.Contains(lower, "verify") ||
			strings.Contains(lower, "./...") {
			return Icon.Verify, "VERIFY"
		}
		return Icon.EnvDeps, "ENV_DEPS"
	case "FILE_MUTATE", "DIFF_PATCH", "ATOMIC_REPLACE":
		return Icon.CodeMod, "CODE_MOD"
	default:
		return Icon.EnvDeps, "ENV_DEPS"
	}
}

// renderJSONPlanWidget renders a validated PlanOutput as a clean TUI widget.
// Used when the LLM returns a valid JSON plan contract instead of markdown.
// When src is non-nil each task's checkbox reflects its ledger state: tasks
// committed by /build (keyed on AtomicTask.TaskID) render as checked [✓] with
// strike-through text; pending tasks keep the open [ ] state.
func renderJSONPlanWidget(planOutput *plan.PlanOutput, src plan.TaskStatusSource, width int) string {
	if planOutput == nil {
		return ""
	}

	contentWidth := width - 4
	if contentWidth < 20 {
		contentWidth = 20
	}

	var b strings.Builder

	// ── Strategic Architectural Blueprint ──────────────────────────────
	b.WriteString(boldSapphireStyle.Render(Icon.Blueprint + " STRATEGIC ARCHITECTURAL BLUEPRINT"))
	b.WriteString("\n")

	overview := planOutput.StrategicOverview
	if overview.RootCoreFactor != "" {
		b.WriteString(textStyle.Render(overview.RootCoreFactor))
		b.WriteString("\n")
	}
	if overview.ImpactDomain != "" {
		fmt.Fprintf(&b, "  %s %s\n",
			dimmedStyle.Render(Icon.Chevron+" Impact Domain:"),
			textStyle.Render(overview.ImpactDomain),
		)
	}
	if overview.RiskEvaluation != "" {
		riskStyle := dimmedStyle
		riskText := overview.RiskEvaluation
		riskLower := strings.ToLower(riskText)
		if strings.Contains(riskLower, "critical") || strings.Contains(riskLower, "high") {
			riskStyle = redStyle
		}
		fmt.Fprintf(&b, "  %s %s\n",
			dimmedStyle.Render(Icon.Chevron+" Risk Evaluation:"),
			riskStyle.Render(riskText),
		)
	}
	if overview.VerificationVector != "" {
		fmt.Fprintf(&b, "  %s %s\n",
			dimmedStyle.Render(Icon.Chevron+" Verification Vector:"),
			textStyle.Render(overview.VerificationVector),
		)
	}

	b.WriteString("\n")

	// ── Staged Execution Timeline ──────────────────────────────────────
	b.WriteString(boldMauveStyle.Render(Icon.Timeline + " STAGED EXECUTION TIMELINE"))
	b.WriteString("\n")

	// Count committed tasks so the header reflects live ledger progress.
	completed := 0
	for _, task := range planOutput.AtomicTasks {
		if src != nil && src.IsCompleted(task.TaskID) {
			completed++
		}
	}
	if completed > 0 {
		fmt.Fprintf(&b, "%s\n\n", boldTextStyle.Render(
			fmt.Sprintf("(%d/%d tasks completed)", completed, len(planOutput.AtomicTasks))))
	} else {
		b.WriteString("\n")
	}

	strikeDimStyle := dimmedStyle.Strikethrough(true)

	for _, task := range planOutput.AtomicTasks {
		done := src != nil && src.IsCompleted(task.TaskID)

		var trackIcon, trackLabel string
		if done {
			trackIcon = Icon.Done
			trackLabel = "DONE"
		} else {
			trackIcon, trackLabel = enrichedTrack(task.Strategy, task.File)
		}

		iconStyle := greenStyle
		if !done {
			switch trackLabel {
			case "ENV_DEPS":
				iconStyle = orangeStyle
			case "CODE_MOD":
				iconStyle = blueStyle
			case "VERIFY":
				iconStyle = greenStyle
			default:
				iconStyle = dimmedStyle
			}
		}

		descStyle := dimmedStyle
		if done {
			descStyle = strikeDimStyle
		}

		tagStyle := dimmedStyle
		if !done {
			switch trackLabel {
			case "ENV_DEPS":
				tagStyle = orangeStyle
			case "CODE_MOD":
				tagStyle = blueStyle
			case "VERIFY":
				tagStyle = greenStyle
			}
		}

		fmt.Fprintf(&b, "%s %s %s\n",
			iconStyle.Render(trackIcon),
			tagStyle.Render("["+trackLabel+"]"),
			textStyle.Render(task.File),
		)

		// Rationale line
		rationale := task.Rationale
		if rationale == "" {
			rationale = task.Description
		}
		if rationale != "" {
			descW := contentWidth - 6
			if descW < 10 {
				descW = 10
			}
			ratLines := wrapStreamText(rationale, descW)
			for _, rl := range ratLines {
				fmt.Fprintf(&b, "  %s %s\n", dimmedStyle.Render(Icon.Chevron+" Rationale:"), descStyle.Render(rl))
			}
		}

		// Solution line
		if task.Solution != "" {
			descW := contentWidth - 6
			if descW < 10 {
				descW = 10
			}
			solLines := wrapStreamText(task.Solution, descW)
			for _, sl := range solLines {
				fmt.Fprintf(&b, "  %s %s\n", dimmedStyle.Render(Icon.Chevron+" Expected Solution:"), descStyle.Render(sl))
			}
		}

		b.WriteString("\n")
	}

	return renderWidget("Plan", strings.TrimSuffix(b.String(), "\n"), width, colorModePlan)
}
