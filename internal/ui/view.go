package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
)

type blockType int

const (
	blockText blockType = iota
	blockPlan
	blockDiff
	blockTable
	blockEvidence
	blockRisk
	blockCommand
)

type contentBlock struct {
	kind blockType
	raw  string
}

// View renders the entire multi-pane TUI architecture layout.
func (m *model) View() string {
	if !m.vpReady {
		return "initializing…"
	}

	width := m.width
	if width < 40 {
		width = 40
	}

	var sections []string

	// 1. Top Status Bar (objective + metrics — hidden when no state)
	topBar := m.renderTopBar()
	if topBar != "" {
		sections = append(sections, topBar)
	}

	// 2. Conversation Timeline (Viewport)
	sections = append(sections, m.vp.View())

	// 3. Suggestion palette (Floats dynamically above the prompt input area)
	if m.showSuggestions && len(m.suggestions) > 0 {
		sections = append(sections, m.renderSuggestions(width))
	}

	// 4. Engineering Widgets (Active/pinned widget, e.g. active Proposal or Progress)
	activeWidget := m.renderActiveWidget(width)
	if activeWidget != "" {
		sections = append(sections, activeWidget)
	}

	// 5. Focus separator line
	sections = append(sections, m.renderFocusLine(width))

	// 6. Input Prompt box area
	sections = append(sections, m.renderPromptBox(width))

	// 7. Runtime Status
	sections = append(sections, m.renderRuntimeStatus(width))

	// 8. Footer
	sections = append(sections, m.renderFooter(width))

	return strings.Join(sections, "\n")
}

func (m *model) renderTopBar() string {
	if m.sess == nil {
		return ""
	}
	obj := m.sess.ObjectiveState
	rawIntent := ""
	scopeFiles := 0
	if obj != nil {
		rawIntent = strings.TrimSpace(obj.RawIntent)
		scopeFiles = len(obj.Scope.Files)
	}

	if rawIntent == "" && scopeFiles == 0 {
		return ""
	}

	var parts []string
	if rawIntent != "" {
		objText := truncateStr(rawIntent, 40)
		parts = append(parts, "⌖ "+objText)
	}
	if scopeFiles > 0 {
		parts = append(parts, fmt.Sprintf("🗀 %d files", scopeFiles))
	}

	content := strings.Join(parts, "  •  ")
	bar := " " + content + " "

	return lipgloss.NewStyle().
		Background(lipgloss.Color(colorSurface)).
		Foreground(lipgloss.Color(colorTopBarMetrics)).
		Padding(0, 1).
		Render(bar)
}

func truncateStr(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "…"
}

// ── Focus line ────────────────────────────────────────────────────────────

func (m *model) renderFocusLine(width int) string {
	color := animLineColor(m)
	return lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("─", width))
}

// ── Prompt box ────────────────────────────────────────────────────────────

func (m *model) renderPromptBox(width int) string {
	mode := m.resolver.Current()
	modeColor := modeLineColor(mode)
	prefixStyle := lipgloss.NewStyle().Bold(true).Foreground(modeColor)

	var prefix string
	if width < 50 {
		short := "?"
		switch mode {
		case modes.ModeAsk:
			short = "?"
		case modes.ModePlan:
			short = "p"
		case modes.ModeBuild:
			short = "b"
		case modes.ModeInvestigate:
			short = "i"
		case modes.ModeReview:
			short = "r"
		}
		prefix = prefixStyle.Render(short + " ❯")
	} else {
		prefix = prefixStyle.Render(mode.String() + " ❯")
	}

	var inner string
	switch {
	case m.agentRunning:
		sp := m.renderFlowingSpinner()
		label := lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Render(m.agentLabel + "…")
		inner = prefix + " " + sp + "  " + label
	case m.streaming && m.responseBuffer.Len() == 0:
		sp := m.renderFlowingSpinner()
		inner = prefix + " " + sp + "  " + infoStyle.Render("thinking…")
	default:
		// Use native m.ti.View() to delegate terminal hardware cursor coordination.
		m.ti.Cursor.Style = lipgloss.NewStyle().Foreground(modeColor)
		inner = prefix + " " + m.ti.View()
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(modeColor).
		Padding(0, 1).
		Width(width - 2).
		Render(inner)
}

// ── Active Widget Area ────────────────────────────────────────────────────

func (m *model) renderActiveWidget(width int) string {
	if m.state == StateAwaitingApproval && len(m.pendingProposals) > 0 {
		// Map domain proposal to presentation ViewModel
		vm := ToMutationCardViewModelFromProposal(m.pendingProposals[0])

		if len(m.pendingProposals) > 1 {
			vm.Purpose = fmt.Sprintf("Apply proposed code changes for %s and %d other files", vm.Target.Name, len(m.pendingProposals)-1)
		} else {
			vm.Purpose = fmt.Sprintf("Apply proposed code changes for %s", vm.Target.Name)
		}

		// Delegate rendering entirely to the stateless component
		mr := &EnhancedMutationRenderer{Width: width}
		return mr.Render(vm)
	}

	if m.agentRunning {
		var inner strings.Builder
		fmt.Fprintf(&inner, "● Running task: %s…\n", m.agentLabel)
		inner.WriteString("Execution in progress.")
		return renderWidget("Progress", inner.String(), width, colorYellow)
	}

	return ""
}

// ── Runtime Status ────────────────────────────────────────────────────────

func (m *model) renderRuntimeStatus(width int) string {
	provider := m.cfg.ActiveProviderName()
	isCloud := provider != "ollama" && provider != "local"

	var parts []string

	// 1. Task (highest priority)
	taskStr := "task: idle"
	if m.agentRunning {
		taskStr = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Render("task: " + m.agentLabel)
	} else if m.streaming {
		taskStr = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Render("task: streaming")
	}
	parts = append(parts, taskStr)

	// 2. Sandbox — bound to the current mode's write capability
	mode := m.resolver.Current()
	sandboxStr := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus)).Render("sandbox: read")
	if mode.CanWrite() {
		sandboxStr = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOrange)).Render("sandbox: write")
	}
	parts = append(parts, sandboxStr)

	// Scale dynamic components based on screen width
	if width >= 50 {
		// 3. Model
		modelName := m.cfg.ActiveModelName()
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Render("model: "+modelName))
	}

	if width >= 75 {
		// 4. Tokens & Cost (Omit cost and % limit details for local models)
		total := m.tokenInput + m.tokenOutput
		var tokStr string
		if isCloud {
			maxCtx := 32768
			pct := float64(total) / float64(maxCtx) * 100
			c := float64(m.tokenInput)*(3.0/1_000_000) + float64(m.tokenOutput)*(15.0/1_000_000)
			costStr := fmt.Sprintf("$%.2f", c)
			tokStr = fmt.Sprintf("tokens: %d (%.0f%%) │ cost: %s", total, pct, costStr)
		} else {
			tokStr = fmt.Sprintf("tokens: %d", total)
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).Render(tokStr))
	}

	if width >= 100 {
		// 5. Checkpoint
		checkpointStr := "checkpoint: none"
		if len(m.sess.Checkpoints) > 0 {
			lastCP := m.sess.Checkpoints[len(m.sess.Checkpoints)-1]
			if len(lastCP) > 8 {
				checkpointStr = fmt.Sprintf("checkpoint: %s", lastCP[:8])
			} else {
				checkpointStr = fmt.Sprintf("checkpoint: %s", lastCP)
			}
		}
		parts = append(parts, checkpointStr)
	}
	if m.uiNotice != "" && width >= 60 {
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Render("notice: "+m.uiNotice))
	}

	sep := lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle)).Render("  │  ")
	joined := strings.Join(parts, sep)

	// Safety fallback check to prevent line wrap under extremely tight terminal splitting
	if lipgloss.Width(joined) > width && len(parts) > 2 {
		parts = parts[:2]
		joined = strings.Join(parts, sep)
	}

	return joined
}

// ── Footer ────────────────────────────────────────────────────────────────

func (m *model) renderFooter(width int) string {
	wd, _ := os.Getwd()
	project := filepath.Base(wd)
	branch, _ := m.gitEng.Branch()
	if branch == "" {
		branch = "detached"
	}

	mode := m.resolver.Current()
	safeStr := "safe"
	if mode.CanShell() || mode.CanTest() {
		safeStr = "sandboxed"
	}
	if mode.CanWrite() {
		safeStr = "write"
	}

	var left, right string

	switch {
	case width < 50:
		left = fmt.Sprintf("(%s)", branch)
		right = safeStr
	case width < 75:
		left = fmt.Sprintf("workspace: %s (%s)", project, branch)
		right = fmt.Sprintf("execution: %s", safeStr)
	default:
		provider := m.cfg.ActiveProviderName()
		modelName := m.cfg.ActiveModelName()
		left = fmt.Sprintf("workspace: %s (%s)   •   runtime: %s/%s", project, branch, provider, modelName)
		right = fmt.Sprintf("execution: %s", safeStr)
	}

	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}

	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	return footerStyle.Render(left + strings.Repeat(" ", gap) + right)
}

// ── Startup banner ────────────────────────────────────────────────────────

var bannerModes = []struct{ name, desc string }{
	{"/ask", "explain, inspect, understand"},
	{"/plan", "break down, structure, design"},
	{"/build", "implement, refactor, elevate"},
	{"/investigate", "debug, trace, root-cause"},
	{"/review", "analyze, critique, improve"},
}

func getGreeting() string {
	userName := os.Getenv("USER")
	if userName == "" {
		userName = "User"
	}
	hour := time.Now().Hour()
	switch {
	case hour >= 5 && hour < 12:
		return fmt.Sprintf("Hi %s, Good morning!", userName)
	case hour >= 12 && hour < 17:
		return fmt.Sprintf("Hi %s, Good afternoon!", userName)
	case hour >= 17 && hour < 21:
		return fmt.Sprintf("Hi %s, Good evening!", userName)
	default:
		return fmt.Sprintf("Hi %s, night owl!", userName)
	}
}

func (m *model) renderStartupBanner(termWidth int) string {
	ac := lipgloss.Color(colorGreenBr)
	dm := lipgloss.Color(colorMuted)
	tx := lipgloss.Color(colorText)
	sb := lipgloss.Color(colorSubtle)

	acS := lipgloss.NewStyle().Foreground(ac).Bold(true)
	dmS := lipgloss.NewStyle().Foreground(dm)
	txS := lipgloss.NewStyle().Foreground(tx)
	sbS := lipgloss.NewStyle().Foreground(sb)
	mnS := lipgloss.NewStyle().Bold(true).Foreground(tx)

	innerW := termWidth - 6
	if innerW < 60 {
		innerW = 60
	}

	// Grid system metric setup for geometric block alignments
	const robotW = 6
	const sep = "  "

	// Normalized clean block bounds structure to eliminate shifting metrics
	cleanRobotArt := []string{
		"  ██  ",
		" █  █ ",
		" ████ ",
		" █ ██ ",
		" █  █ ",
	}

	rightCol := make([]string, 0, 5+len(bannerModes))
	rightCol = append(rightCol,
		acS.Render(getGreeting()),
		acS.Render("IZEN"),
		txS.Render("engineering intelligence."),
		txS.Render("human in control."),
		"",
	)
	for _, mode := range bannerModes {
		nameS := mnS.Render(mode.name)
		descS := dmS.Render(mode.desc)
		padLen := max(1, 15-lipgloss.Width(nameS))
		rightCol = append(rightCol, nameS+strings.Repeat(" ", padLen)+descS)
	}

	var rows []string
	totalRows := len(cleanRobotArt)
	if len(rightCol) > totalRows {
		totalRows = len(rightCol)
	}
	for i := 0; i < totalRows; i++ {
		var robotPart string
		if i < len(cleanRobotArt) {
			robotPart = acS.Render(padRight(cleanRobotArt[i], robotW))
		} else {
			robotPart = strings.Repeat(" ", robotW)
		}
		var rightPart string
		if i < len(rightCol) {
			rightPart = rightCol[i]
		}
		rows = append(rows, robotPart+sep+rightPart)
	}

	divider := sbS.Render(strings.Repeat("─", innerW-2))
	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()
	metaParts := []string{
		dmS.Render("v" + version),
		dmS.Render(provider + " " + modelName),
	}
	if branch, err := m.gitEng.Branch(); err == nil && branch != "" {
		metaParts = append(metaParts, dmS.Render("git ("+branch+")"))
	}
	metaSep := sbS.Render(" • ")
	meta := strings.Join(metaParts, metaSep)

	rows = append(rows, divider, meta)
	body := strings.Join(rows, "\n")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorSubtle)).
		Padding(1, 2).
		Width(termWidth - 2).
		Render(body)
}

func padRight(s string, n int) string {
	sw := len(s)
	if sw >= n {
		return s
	}
	return s + strings.Repeat(" ", n-sw)
}

// ── Record renderer (for viewport content) ────────────────────────────────

func (m *model) printRecord(rec record) string {
	gutter := gutterFor(rec.role)
	content := rec.text

	if rec.role == roleAI {
		return m.renderAIResponseBlocks(content, m.width)
	}

	availableWidth := m.width - 2
	if availableWidth < 20 {
		availableWidth = 20
	}

	wrapStringToWidth := func(text string, maxW int) []string {
		if len(text) == 0 {
			return []string{""}
		}
		var chunks []string
		words := strings.Fields(text)
		if len(words) == 0 {
			runes := []rune(text)
			for i := 0; i < len(runes); i += maxW {
				end := i + maxW
				if end > len(runes) {
					end = len(runes)
				}
				chunks = append(chunks, string(runes[i:end]))
			}
			return chunks
		}

		var currentLine strings.Builder
		for _, word := range words {
			if currentLine.Len()+1+len(word) > maxW {
				chunks = append(chunks, currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			} else {
				if currentLine.Len() > 0 {
					currentLine.WriteString(" ")
				}
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			chunks = append(chunks, currentLine.String())
		}
		return chunks
	}

	wrappedLines := wrapStringToWidth(content, availableWidth)

	switch rec.role {
	case roleUser:
		styledLines := make([]string, len(wrappedLines))
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
		for i, line := range wrappedLines {
			styledLines[i] = gutter + style.Render(line)
		}
		return strings.Join(styledLines, "\n")
	case roleError:
		styledLines := make([]string, len(wrappedLines))
		for i, line := range wrappedLines {
			styledLines[i] = gutter + errorStyle.Render(line)
		}
		return strings.Join(styledLines, "\n")
	case roleStatus:
		styledLines := make([]string, len(wrappedLines))
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus))
		for i, line := range wrappedLines {
			styledLines[i] = gutter + style.Render(line)
		}
		return strings.Join(styledLines, "\n")
	default:
		styledLines := make([]string, len(wrappedLines))
		for i, line := range wrappedLines {
			styledLines[i] = gutter + outputStyle.Render(line)
		}
		return strings.Join(styledLines, "\n")
	}
}

// ── Widget Box & Semantic Renderers ───────────────────────────────────────

func renderWidget(title string, content string, width int, accentHex string) string {
	if width < 10 {
		width = 10
	}
	innerWidth := width // We are using the full width (no vertical borders)

	var b strings.Builder

	// Styles for the widget
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(accentHex))
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))

	// Top horizontal line
	b.WriteString(borderStyle.Render(strings.Repeat("─", innerWidth)) + "\n")

	// Title line (without leading space)
	titleLine := title
	titleLen := lipgloss.Width(titleLine)
	if titleLen < innerWidth {
		titleLine += strings.Repeat(" ", innerWidth-titleLen)
	} else {
		titleLine = titleLine[:innerWidth]
	}
	b.WriteString(titleStyle.Render(titleLine) + "\n")

	// Separator horizontal line
	b.WriteString(borderStyle.Render(strings.Repeat("─", innerWidth)) + "\n")

	// Content lines
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if len(line) > 0 {
			// We don't truncate or wrap the line; we leave it as is.
			// If the line is shorter than innerWidth, we pad with spaces to the right.
			visualLen := lipgloss.Width(line)
			if visualLen < innerWidth {
				line += strings.Repeat(" ", innerWidth-visualLen)
			}
			// If the line is longer, we leave it (might exceed the width, but we are not changing wrapping behavior)
			b.WriteString(line + "\n")
		} else {
			b.WriteString("\n")
		}
	}

	// Bottom horizontal line
	b.WriteString(borderStyle.Render(strings.Repeat("─", innerWidth)) + "\n")

	return b.String()
}

func (m *model) renderAIResponseBlocks(content string, width int) string {
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
				var prefixColor string
				var text string

				switch {
				case strings.HasPrefix(item, "- [x]") || strings.HasPrefix(item, "[x]") || strings.HasPrefix(item, "✓"):
					prefixChar = "✓ "
					prefixColor = colorGreen
					text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [x]"), "[x]"), "✓"))
				case strings.HasPrefix(item, "- [/]") || strings.HasPrefix(item, "[/]") || strings.HasPrefix(item, "●"):
					prefixChar = "● "
					prefixColor = colorOrange
					text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [/]"), "[/]"), "●"))
				case strings.HasPrefix(item, "- [ ]") || strings.HasPrefix(item, "[ ]") || strings.HasPrefix(item, "○"):
					prefixChar = "○ "
					prefixColor = colorDimmed
					text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(item, "- [ ]"), "[ ]"), "○"))
				case strings.HasPrefix(item, "✗"):
					prefixChar = "✗ "
					prefixColor = colorRed
					text = strings.TrimSpace(strings.TrimPrefix(item, "✗"))
				default:
					prefixChar = "• "
					prefixColor = colorText
					text = item
				}

				// Wrap plan item text to inner width leaving room for list bullet
				wrapW := widgetInnerWidth - 2
				if wrapW < 10 {
					wrapW = 10
				}
				wrappedText := wrapStreamText(text, wrapW)

				prefixStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(prefixColor))
				for idx, line := range wrappedText {
					if idx == 0 {
						contentLines = append(contentLines, prefixStyle.Render(prefixChar)+line)
					} else {
						contentLines = append(contentLines, "  "+line) // Indented wrap
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
				details = append(details, lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Render("File:   "+file))
			}
			if symbol != "" {
				details = append(details, lipgloss.NewStyle().Foreground(lipgloss.Color(colorBlue)).Render("Symbol: "+symbol))
			}
			if linesRange != "" {
				details = append(details, lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).Render("Range:  "+linesRange))
			}

			var fullContent string
			if len(details) > 0 {
				fullContent = strings.Join(details, "\n") + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle)).Render(strings.Repeat("─", widgetInnerWidth)) + "\n" + diffRendered
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
				lines := strings.Split(cmdText, "\n")
				if len(lines) > 2 {
					cmdText = strings.Join(lines[1:len(lines)-1], "\n")
				}
			}
			lines := strings.Split(cmdText, "\n")
			var wrappedLines []string
			for _, line := range lines {
				wrappedLines = append(wrappedLines, wrapStreamText(line, widgetInnerWidth)...)
			}
			rendered = renderWidget("Command", strings.Join(wrappedLines, "\n"), availableWidth, colorModeBuild)

		default:
			// Route plain/markdown text through the semantic MarkdownRenderer
			// per ASK_RENDERING.md pipeline: LLM response → AST → semantic UI
			mr := NewMarkdownRenderer(availableWidth)
			mdRendered := mr.Render(block.raw)
			if mdRendered != "" {
				// Prefix each line with the AI gutter indicator
				mdLines := strings.Split(strings.TrimRight(mdRendered, "\n"), "\n")
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

func parseAIContent(content string) []contentBlock {
	var blocks []contentBlock
	lines := strings.Split(content, "\n")

	var currentBlock []string
	var currentKind = blockText

	inCodeBlock := false
	codeBlockLang := ""

	flush := func() {
		if len(currentBlock) == 0 {
			return
		}
		raw := strings.Join(currentBlock, "\n")
		raw = strings.TrimSpace(raw)
		if raw != "" {
			blocks = append(blocks, contentBlock{kind: currentKind, raw: raw})
		}
		currentBlock = nil
		currentKind = blockText
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				currentBlock = append(currentBlock, line)
				inCodeBlock = false
				flush()
			} else {
				flush()
				inCodeBlock = true
				codeBlockLang = strings.TrimPrefix(trimmed, "```")
				switch {
				case strings.HasPrefix(codeBlockLang, "diff"):
					currentKind = blockDiff
				case codeBlockLang == "bash" || codeBlockLang == "sh":
					currentKind = blockCommand
				default:
					currentKind = blockText
				}
				currentBlock = append(currentBlock, line)
			}
			continue
		}

		if inCodeBlock {
			currentBlock = append(currentBlock, line)
			continue
		}

		// Outside code block
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			if currentKind != blockTable {
				flush()
				currentKind = blockTable
			}
			currentBlock = append(currentBlock, line)
			continue
		}

		isPlanLine := strings.HasPrefix(trimmed, "✓") || strings.HasPrefix(trimmed, "●") ||
			strings.HasPrefix(trimmed, "○") || strings.HasPrefix(trimmed, "✗") ||
			strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [x]") ||
			strings.HasPrefix(trimmed, "- [/]")

		if isPlanLine || (strings.HasPrefix(strings.ToLower(trimmed), "plan") && i < len(lines)-1 && (strings.HasPrefix(strings.TrimSpace(lines[i+1]), "-") || strings.HasPrefix(strings.TrimSpace(lines[i+1]), "1.") || strings.HasPrefix(strings.TrimSpace(lines[i+1]), "✓") || strings.HasPrefix(strings.TrimSpace(lines[i+1]), "●"))) {
			if currentKind != blockPlan {
				flush()
				currentKind = blockPlan
			}
			currentBlock = append(currentBlock, line)
			continue
		}

		if strings.HasPrefix(strings.ToLower(trimmed), "evidence") || strings.HasPrefix(strings.ToLower(trimmed), "source:") || strings.HasPrefix(strings.ToLower(trimmed), "confidence:") {
			if currentKind != blockEvidence {
				flush()
				currentKind = blockEvidence
			}
			currentBlock = append(currentBlock, line)
			continue
		}

		if strings.HasPrefix(strings.ToLower(trimmed), "risk") || strings.HasPrefix(strings.ToLower(trimmed), "score:") || strings.HasPrefix(strings.ToLower(trimmed), "breaking api:") {
			if currentKind != blockRisk {
				flush()
				currentKind = blockRisk
			}
			currentBlock = append(currentBlock, line)
			continue
		}

		if currentKind != blockText {
			flush()
		}

		currentBlock = append(currentBlock, line)
	}
	flush()
	return blocks
}

func renderTable(rawTable string, width int) string {
	lines := strings.Split(rawTable, "\n")
	var grid [][]string
	var colWidths []int

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "---") && !strings.Contains(trimmed, "[a-zA-Z]") {
			clean := strings.ReplaceAll(trimmed, "|", "")
			clean = strings.ReplaceAll(clean, "-", "")
			clean = strings.ReplaceAll(clean, " ", "")
			if clean == "" {
				continue
			}
		}
		parts := strings.Split(trimmed, "|")
		var row []string
		for _, p := range parts {
			row = append(row, strings.TrimSpace(p))
		}
		if len(row) > 0 && row[0] == "" {
			row = row[1:]
		}
		if len(row) > 0 && row[len(row)-1] == "" {
			row = row[:len(row)-1]
		}
		if len(row) > 0 {
			grid = append(grid, row)
			for len(colWidths) < len(row) {
				colWidths = append(colWidths, 0)
			}
			for idx, val := range row {
				valLen := lipgloss.Width(val)
				if valLen > colWidths[idx] {
					colWidths[idx] = valLen
				}
			}
		}
	}

	if len(grid) == 0 {
		return rawTable
	}

	// Calculate sum of column widths including padding and grid lines
	totalTableW := 0
	for _, w := range colWidths {
		totalTableW += w + 3
	}
	totalTableW += 1

	// Fallback to compact key-value listing if split terminal screen is too small
	if totalTableW > width || width < 60 {
		var b strings.Builder
		headers := grid[0]
		for rowIdx := 1; rowIdx < len(grid); rowIdx++ {
			row := grid[rowIdx]
			if rowIdx > 1 {
				b.WriteString("\n" + strings.Repeat("─", width) + "\n")
			}
			for colIdx, val := range row {
				header := fmt.Sprintf("Col %d", colIdx+1)
				if colIdx < len(headers) {
					header = headers[colIdx]
				}
				line := fmt.Sprintf("• %s: %s", header, val)
				wrapped := wrapStreamText(line, width)
				b.WriteString(strings.Join(wrapped, "\n") + "\n")
			}
		}
		return strings.TrimSuffix(b.String(), "\n")
	}

	var b strings.Builder
	b.WriteString("┌")
	for idx, w := range colWidths {
		if idx > 0 {
			b.WriteString("┬")
		}
		b.WriteString(strings.Repeat("─", w+2))
	}
	b.WriteString("┐\n")

	for rowIdx, row := range grid {
		if rowIdx > 0 && rowIdx == 1 {
			b.WriteString("├")
			for idx, w := range colWidths {
				if idx > 0 {
					b.WriteString("┼")
				}
				b.WriteString(strings.Repeat("─", w+2))
			}
			b.WriteString("┤\n")
		}

		b.WriteString("│")
		for idx, w := range colWidths {
			val := ""
			if idx < len(row) {
				val = row[idx]
			}
			padded := " " + val + " "
			extra := w + 2 - lipgloss.Width(padded)
			if extra > 0 {
				padded += strings.Repeat(" ", extra)
			}
			b.WriteString(padded)
			b.WriteString("│")
		}
		b.WriteString("\n")
	}

	b.WriteString("└")
	for idx, w := range colWidths {
		if idx > 0 {
			b.WriteString("┴")
		}
		b.WriteString(strings.Repeat("─", w+2))
	}
	b.WriteString("┘")

	return b.String()
}

func parseDiffMetadata(diffBody string) (file, symbol, linesRange, cleanDiff string) {
	lines := strings.Split(diffBody, "\n")
	var diffLines []string

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			continue
		}
		if strings.HasPrefix(line, "@@") {
			parts := strings.Split(line, "@@")
			if len(parts) >= 3 {
				header := strings.TrimSpace(parts[1])
				symbol = strings.TrimSpace(parts[2])

				subparts := strings.Fields(header)
				if len(subparts) >= 2 {
					newRange := strings.TrimPrefix(subparts[1], "+")
					rangeParts := strings.Split(newRange, ",")
					if len(rangeParts) >= 2 {
						start, _ := strconv.Atoi(rangeParts[0])
						count, _ := strconv.Atoi(rangeParts[1])
						linesRange = fmt.Sprintf("Lines %d-%d", start, start+count-1)
					}
				}
			}
		}
		diffLines = append(diffLines, line)
	}
	cleanDiff = strings.Join(diffLines, "\n")
	return
}
