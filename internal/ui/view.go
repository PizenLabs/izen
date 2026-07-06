package ui

import (
	"fmt"
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

// View returns the full UI state.
// The scrollable chat history is rendered inside the viewport; metadata bars,
// the input line, and the status line are pinned to the bottom of the terminal.
func (m *model) View() string {
	if m.showHelpOverlay {
		return m.renderHelpOverlay()
	}

	if !m.Ready {
		return "Loading IZEN..."
	}

	var buf strings.Builder
	buf.WriteString(m.Viewport.View())
	buf.WriteString("\n")
	buf.WriteString(m.renderStaticBottomControls())
	return buf.String()
}

// renderStaticBottomControls renders the fixed bottom dashboard that must never
// scroll away: autocomplete dropdown, mode lines, input line, and telemetry.
func (m *model) renderStaticBottomControls() string {
	width := m.width
	if width < 40 {
		width = 40
	}

	mode := m.resolver.Current()
	modeColor := m.modeStyle(mode)

	var s strings.Builder

	if m.autocompleteActive && len(m.autocompleteItems) > 0 {
		s.WriteString(m.renderAutocompleteDropdown(width))
	}

	s.WriteString(modeColor.Render(strings.Repeat("─", width)) + "\n")
	s.WriteString(modeColor.Render("❯ "+mode.String()) + " ⟩ " + m.ti.View() + "\n")
	s.WriteString(modeColor.Render(strings.Repeat("─", width)) + "\n")
	s.WriteString(m.renderRuntimeStatus(width))
	s.WriteString("\n")

	return s.String()
}

// modeStyle returns the appropriate lipgloss style for a mode.
// Core engineering modes (ask, build, investigate, review) get their
// unique thematic color. Secondary/utils modes get unified subtle styling.
func (m *model) modeStyle(mode modes.Mode) lipgloss.Style {
	if isCoreEngineeringMode(mode) {
		return modeBoldFgStyles[mode]
	}
	return secondaryModeStyle
}

// ── Autocomplete Dropdown ──────────────────────────────────────────────────

// ── Command section categories for autocomplete ───────────────────────

type cmdCategory int

const (
	catPrimaryMode cmdCategory = iota
	catSystemCommand
	catModeContextual
)

type cmdSection struct {
	title string
	style lipgloss.Style
	items []string
}

func (m *model) cmdCategoryFor(item string) cmdCategory {
	for _, c := range coreModes {
		if c == item {
			return catPrimaryMode
		}
	}
	for _, c := range globalCommands {
		if c == item {
			return catSystemCommand
		}
	}
	return catModeContextual
}

func (m *model) buildCmdSections(items []string) []cmdSection {
	var primary, sys, ctx []string
	for _, it := range items {
		switch m.cmdCategoryFor(it) {
		case catPrimaryMode:
			primary = append(primary, it)
		case catSystemCommand:
			sys = append(sys, it)
		case catModeContextual:
			ctx = append(ctx, it)
		}
	}
	var sections []cmdSection
	if len(primary) > 0 {
		sections = append(sections, cmdSection{title: "PRIMARY MODES", style: accentStyle, items: primary})
	}
	if len(sys) > 0 {
		sections = append(sections, cmdSection{title: "SYSTEM COMMANDS", style: subtleStyle, items: sys})
	}
	if len(ctx) > 0 {
		sections = append(sections, cmdSection{title: "MODE CONTEXTUAL", style: mutedStyle, items: ctx})
	}
	return sections
}

// renderAutocompleteDropdown renders a compact border-box suggestion list
// positioned directly above the top parallel line. For file selections (@),
// it uses a two-column layout with filename on the left and directory on the
// right. Command selections (/) are displayed in categorized section blocks.
func (m *model) renderAutocompleteDropdown(width int) string {
	if len(m.autocompleteItems) == 0 || !m.autocompleteActive {
		return ""
	}
	var b strings.Builder

	maxShow := 8
	list := m.autocompleteItems
	if len(list) > maxShow {
		list = list[:maxShow]
	}

	// Pre-compiled styles for the dropdown
	highlightedBgStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(colorOverlay))

	// Top border with title
	title := "Context Selection"
	titleSection := "── " + title + " ──"
	topFiller := width - lipgloss.Width(titleSection) - 2
	if topFiller < 0 {
		topFiller = 0
	}
	b.WriteString(subtleStyle.Render("╭"+titleSection+strings.Repeat("─", topFiller)+"╮") + "\n")

	if m.autocompleteType == "file" {
		for i, item := range list {
			name := filepath.Base(item)
			dir := filepath.Dir(item)
			if dir == "." {
				dir = "./"
			}

			icon := "◽ "
			if i == m.autocompleteIdx {
				icon = "▶ "
			}

			// Left column: file name (high contrast)
			leftSide := textStyle.Render(icon + name)
			// Right column: parent directory (low contrast #6c7086)
			rightSide := mutedStyle.Render(dir + " ")

			paddingCount := width - lipgloss.Width(icon+name) - lipgloss.Width(dir+" ") - 4
			if paddingCount < 0 {
				paddingCount = 0
			}

			if i == m.autocompleteIdx {
				rowString := leftSide + strings.Repeat(" ", paddingCount) + rightSide
				b.WriteString("│ " + highlightedBgStyle.Render(rowString) + " │\n")
			} else {
				b.WriteString("│ " + leftSide + strings.Repeat(" ", paddingCount) + rightSide + " │\n")
			}
		}
	} else {
		sections := m.buildCmdSections(list)
		itemIdx := 0
		for _, sec := range sections {
			// Section header
			headerStr := "  " + sec.style.Render(sec.title)
			hPad := width - lipgloss.Width(headerStr) - 4
			if hPad < 0 {
				hPad = 0
			}
			b.WriteString("│ " + headerStr + strings.Repeat(" ", hPad) + " │\n")

			for _, item := range sec.items {
				display := item
				lw := lipgloss.Width(display)
				maxContent := width - 8
				if maxContent < 10 {
					maxContent = 10
				}
				if lw > maxContent {
					display = display[:maxContent-1] + "…"
					lw = lipgloss.Width(display)
				}
				pad := strings.Repeat(" ", width-lw-6)

				rowString := display + pad
				if itemIdx == m.autocompleteIdx {
					b.WriteString("│ " + highlightedBgStyle.Render("▶ "+rowString) + " │\n")
				} else {
					b.WriteString("│ " + dimmedStyle.Render("◽ "+rowString) + " │\n")
				}
				itemIdx++
			}
		}
	}

	// Bottom border
	b.WriteString(subtleStyle.Render("╰"+strings.Repeat("─", width-2)+"╯") + "\n")

	return b.String()
}

// ── Help Overlay ───────────────────────────────────────────────────────────

// renderHelpOverlay displays IZEN's philosophy, operational rules, and
// keyboard shortcuts as a full-height overlay panel.
func (m *model) renderHelpOverlay() string {
	lines := []string{
		"",
		boldAccentStyle.Render("  ⚡ IZEN  "),
		textStyle.Render("  engineering intelligence · human in control"),
		"",
		subtleStyle.Render("  ─── Modes ───"),
		"  " + accentStyle.Render("/ask") + "         " + dimmedStyle.Render("explain, inspect, understand"),
		"  " + orangeStyle.Render("/plan") + "        " + dimmedStyle.Render("break down, structure, design"),
		"  " + blueStyle.Render("/build") + "       " + dimmedStyle.Render("implement, refactor, elevate"),
		"  " + greenStyle.Render("/investigate") + "  " + dimmedStyle.Render("debug, trace, root-cause"),
		"  " + yellowStyle.Render("/review") + "      " + dimmedStyle.Render("analyze, critique, improve"),
		"",
		subtleStyle.Render("  ─── Commands ───"),
		"  " + dimmedStyle.Render("/help  /?  /mode  /objective  /clear  /drop  /undo"),
		"  " + dimmedStyle.Render("/commit  /checkpoint  /arch  /quit"),
		"  " + dimmedStyle.Render("!<cmd>          run a shell command"),
		"  " + dimmedStyle.Render("@<path>         attach a file"),
		"",
		subtleStyle.Render("  ─── Shortcuts ───"),
		"  " + dimmedStyle.Render("Esc (×3)        quit IZEN"),
		"  " + dimmedStyle.Render("↑/↓             history navigation"),
		"  " + dimmedStyle.Render("Tab/Enter       complete autocomplete"),
		"  " + dimmedStyle.Render("?               toggle this help overlay"),
		"",
		mutedStyle.Render("  press " + boldTextStyle.Render("Esc") + " or " + boldTextStyle.Render("?") + " to close"),
		"",
	}

	return strings.Join(lines, "\n")
}

// ── Runtime Status ────────────────────────────────────────────────────

// renderRuntimeStatus renders a single telemetry line with zero duplication.
// Format: ● <model> │ <tokens> tkn
func (m *model) renderRuntimeStatus(width int) string {
	var b strings.Builder

	// Streaming/agent spinner or idle bullet
	if m.streaming || m.agentRunning {
		idx := m.spinnerFrame % len(taskSpinnerFrames)
		b.WriteString(cyanStyle.Render(taskSpinnerFrames[idx]))
	} else {
		b.WriteString(dimmedStyle.Render("●"))
	}
	b.WriteByte(' ')

	// AI INTERRUPT ENGINE: high-visibility indicator when streaming
	if m.streaming {
		b.WriteString(redStyle.Render("● [Ctrl+D] Interrupt AI "))
	}

	// Model name
	b.WriteString(dimmedStyle.Render(m.cfg.ActiveModelName()))

	// Separator
	b.WriteString(dimmedStyle.Render(" │ "))

	// Tokens
	if m.TotalTokens > 0 {
		b.WriteString(textStyle.Render(strconv.Itoa(m.TotalTokens)))
		b.WriteString(dimmedStyle.Render(" tkn"))
	} else {
		b.WriteString(dimmedStyle.Render("0 tkn"))
	}

	// Checkpoint (truncated)
	if cp := m.latestCheckpointID(); cp != "" {
		cp = strings.TrimPrefix(cp, "cp-")
		if len(cp) > 7 {
			cp = cp[:7]
		}
		b.WriteString(dimmedStyle.Render(" │ "))
		b.WriteString(dimmedStyle.Render("cp-" + cp))
	}

	return b.String()
}

// ── Startup banner ────────────────────────────────────────────────────

var bannerModes = []struct{ name, desc string }{
	{"/ask", "explain, inspect, understand"},
	{"/plan", "break down, structure, design"},
	{"/build", "implement, refactor, elevate"},
	{"/investigate", "debug, trace, root-cause"},
	{"/review", "analyze, critique, improve"},
}

func (m *model) getGreeting() string {
	userName := m.userName
	if userName == "" {
		userName = "developer"
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
	innerW := termWidth - 6
	if innerW < 60 {
		innerW = 60
	}

	const robotW = 6
	const sep = "  "

	cleanRobotArt := []string{
		"  ██  ",
		" █  █ ",
		" ████ ",
		" █ ██ ",
		" █  █ ",
	}

	rightCol := make([]string, 0, 5+len(bannerModes))
	rightCol = append(rightCol,
		boldAccentStyle.Render(m.getGreeting()),
		boldAccentStyle.Render("IZEN"),
		textStyle.Render("engineering intelligence."),
		textStyle.Render("human in control."),
		"",
		"",
	)
	for _, mode := range bannerModes {
		nameS := boldTextStyle.Render(mode.name)
		descS := mutedStyle.Render(mode.desc)
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
			robotPart = boldAccentStyle.Render(padRight(cleanRobotArt[i], robotW))
		} else {
			robotPart = strings.Repeat(" ", robotW)
		}
		var rightPart string
		if i < len(rightCol) {
			rightPart = rightCol[i]
		}
		rows = append(rows, robotPart+sep+rightPart)
	}

	divider := subtleStyle.Render(strings.Repeat("─", innerW-2))
	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()
	metaParts := []string{
		mutedStyle.Render("v" + version),
		mutedStyle.Render(provider + " " + modelName),
	}
	if branch, err := m.gitEng.Branch(); err == nil && branch != "" {
		metaParts = append(metaParts, mutedStyle.Render("git ("+branch+")"))
	}
	metaSep := subtleStyle.Render(" • ")
	meta := strings.Join(metaParts, metaSep)

	rows = append(rows, divider, meta)
	body := strings.Join(rows, "\n")

	return bannerBorderStyle.Width(termWidth - 2).Render(body)
}

func padRight(s string, n int) string {
	sw := len(s)
	if sw >= n {
		return s
	}
	return s + strings.Repeat(" ", n-sw)
}

// ── Record renderer (for viewport content) ────────────────────

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
		for i, line := range wrappedLines {
			styledLines[i] = userBgStyle.Render(" " + line)
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
		for i, line := range wrappedLines {
			styledLines[i] = gutter + dimmedStyle.Render(line)
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

// ── AST Trace Renderer ────────────────────────────────────────────

// ── Widget Box & Semantic Renderers ───────────────────────────────────

func renderWidget(title string, content string, width int, accentHex string) string {
	if width < 10 {
		width = 10
	}
	innerWidth := width // We are using the full width (no vertical borders)

	var b strings.Builder

	borderLine := strings.Repeat("─", innerWidth)

	// Top horizontal line
	b.WriteString(borderLine)
	b.WriteByte('\n')

	// Title line
	titleLine := title
	titleLen := lipgloss.Width(titleLine)
	if titleLen < innerWidth {
		titleLine += strings.Repeat(" ", innerWidth-titleLen)
	} else {
		titleLine = titleLine[:innerWidth]
	}
	b.WriteString(widgetTitleStyle.Render(titleLine))
	b.WriteByte('\n')

	// Separator horizontal line
	b.WriteString(borderLine)
	b.WriteByte('\n')

	// Content lines
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if len(line) > 0 {
			visualLen := lipgloss.Width(line)
			if visualLen < innerWidth {
				line += strings.Repeat(" ", innerWidth-visualLen)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		} else {
			b.WriteByte('\n')
		}
	}

	// Bottom horizontal line
	b.WriteString(borderLine)
	b.WriteByte('\n')

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

				// Wrap plan item text to inner width leaving room for list bullet
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

			// Shell Execution Proposal container
			var container strings.Builder

			mode := m.resolver.Current()
			if mode.CanShell() {
				container.WriteString(shellWarningStyle.Render("> System: Shell Execution Required <"))
			} else {
				container.WriteString(shellWarningStyle.Render("> System: Shell Execution Blocked by Mode <"))
			}
			container.WriteString("\n")

			cmdLines := strings.Split(cmdText, "\n")
			for _, cl := range cmdLines {
				container.WriteString("  ")
				container.WriteString(textStyle.Render("$ " + cl))
				container.WriteString("\n")
			}
			container.WriteString("\n")
			if mode.CanShell() {
				container.WriteString(mutedStyle.Render("[A] Run  [R] Skip"))
			} else {
				container.WriteString(dimmedStyle.Render("[System] Tool 'shell' rejected. Read-Only environment. No action available."))
			}
			container.WriteString("\n")

			rendered = renderWidget("Command", container.String(), availableWidth, colorDimmed)

		default:
			// UNIFIED PATH: deterministic pipeline — identical for streaming and history.
			// Replaces the goldmark-based MarkdownRenderer to eliminate layout flicker.
			blockRendered := RenderDeterministicPipeline(block.raw, availableWidth, false)
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
