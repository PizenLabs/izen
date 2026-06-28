package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
)

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

	// 1. Viewport (Manages scrollable conversation history + live stream buffer)
	sections = append(sections, m.vp.View())

	// 2. Suggestion palette (Floats dynamically above the prompt input area)
	if m.showSuggestions && len(m.suggestions) > 0 {
		sections = append(sections, m.renderSuggestions(width))
	}

	// 3. Focus line separator rule
	sections = append(sections, m.renderFocusLine(width))

	// 4. Input Prompt box area
	sections = append(sections, m.renderPromptBox(width))

	// 5. Responsive adaptive status bar
	sections = append(sections, m.renderStatusBar(width))

	return strings.Join(sections, "\n")
}

// ── Focus line ────────────────────────────────────────────────────────────────

func (m *model) renderFocusLine(width int) string {
	color := animLineColor(m)
	return lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("─", width))
}

// ── Prompt box ────────────────────────────────────────────────────────────────

func (m *model) renderPromptBox(width int) string {
	mode := m.resolver.Current()
	modeColor := modeLineColor(mode)
	prefixStyle := lipgloss.NewStyle().Bold(true).Foreground(modeColor)
	prefix := prefixStyle.Render(mode.String() + ">")

	var inner string
	if m.agentRunning {
		sp := spinnerStyle.Render(spinnerFrames[m.spinnerFrame%len(spinnerFrames)])
		label := lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Render(m.agentLabel + "…")
		inner = prefix + " " + sp + "  " + label
	} else if m.streaming && m.responseBuffer.Len() == 0 {
		sp := spinnerStyle.Render(spinnerFrames[m.spinnerFrame%len(spinnerFrames)])
		inner = prefix + " " + sp + "  " + infoStyle.Render("thinking…")
	} else {
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

// ── Status bar ────────────────────────────────────────────────────────────────

func (m *model) renderStatusBar(width int) string {
	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)
	branch, _ := m.gitEng.Branch()

	// 1. Build Left Side (Context Path tracking)
	var left strings.Builder
	left.WriteString(statusLeftStyle.Render(shortWd))

	// Only display git branch details if we have enough horizontal layout width
	if branch != "" && width >= 90 {
		left.WriteString(dimStyle.Render(" (" + branch + ")"))
	}

	// 2. Resolve Core Token Metrics
	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()
	total := m.tokenInput + m.tokenOutput
	maxCtx := 32768
	pct := float64(total) / float64(maxCtx) * 100

	var tokStr string
	if total >= 1000 {
		tokStr = fmt.Sprintf("%.1fk/%dk", float64(total)/1000, maxCtx/1000)
	} else {
		tokStr = fmt.Sprintf("%d/%dk", total, maxCtx/1000)
	}
	if width >= 100 {
		tokStr = fmt.Sprintf("%s (%.0f%%)", tokStr, pct)
	}

	// Calculate usage costs
	var costStr string
	if provider == "ollama" {
		costStr = "$0.00"
	} else {
		c := float64(m.tokenInput)*(3.0/1_000_000) + float64(m.tokenOutput)*(15.0/1_000_000)
		costStr = fmt.Sprintf("$%.2f", c)
	}

	// 3. Build Right Side dynamically based on available terminal width
	var rightParts []string
	sep := statusSepStyle.String()

	if width < 75 {
		// Ultra-compact layout: Optimal when sharing screen with split Neovim panes
		rightParts = []string{
			statusRightStyle.Render(modelName),
			statusRightStyle.Render(tokStr),
		}
	} else if width < 105 {
		// Balanced layout for standard medium windows
		rightParts = []string{
			statusRightStyle.Render(provider + " " + modelName),
			statusRightStyle.Render(tokStr),
			statusRightStyle.Render(costStr),
		}
	} else {
		// Full layout mode for wide windows
		safeStr := dimStyle.Render("safe")
		if !m.resolver.Current().ReadOnly() {
			safeStr = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOrange)).Render("write")
		}
		cleanStr := dimStyle.Render("clean")
		if m.awaitingConfirmation {
			cleanStr = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Render("pending")
		}

		rightParts = []string{
			statusRightStyle.Render(provider + " " + modelName),
			statusRightStyle.Render(tokStr),
			statusRightStyle.Render(costStr),
			safeStr,
			cleanStr,
		}
	}

	right := strings.Join(rightParts, sep)

	// 4. Compute dynamic spacer filling to align contents properly
	leftW := lipgloss.Width(left.String())
	rightW := lipgloss.Width(right)
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}

	return left.String() + strings.Repeat(" ", gap) + right
}

// ── Startup banner ────────────────────────────────────────────────────────────

var bannerModes = []struct{ name, desc string }{
	{"/ask", "explain, inspect, understand"},
	{"/plan", "break down, structure, design"},
	{"/build", "implement, refactor, elevate"},
	{"/investigate", "debug, trace, root-cause"},
	{"/review", "analyze, critique, improve"},
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

	rightCol := []string{
		acS.Render("IZEN"),
		txS.Render("engineering intelligence."),
		txS.Render("human in control."),
		"",
	}
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
	sw := lipgloss.Width(s)
	if sw >= n {
		return s
	}
	return s + strings.Repeat(" ", n-sw)
}

// ── Record renderer (for viewport content) ────────────────────────────────────

func (m *model) printRecord(rec record) string {
	gutter := gutterFor(rec.role)
	content := rec.text

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
			if len(word) > maxW {
				if currentLine.Len() > 0 {
					chunks = append(chunks, currentLine.String())
					currentLine.Reset()
				}
				runes := []rune(word)
				for i := 0; i < len(runes); i += maxW {
					end := i + maxW
					if end > len(runes) {
						end = len(runes)
					}
					chunks = append(chunks, string(runes[i:end]))
				}
				continue
			}

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

	if rec.role == roleAI && (strings.Contains(content, "\n+") || strings.Contains(content, "\n-")) {
		return gutter + m.renderAdvancedDiff(content)
	}

	availableWidth := m.width - 2
	if availableWidth < 20 {
		availableWidth = 20
	}

	wrappedLines := wrapStringToWidth(content, availableWidth)

	switch rec.role {
	case roleUser, roleAI:
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

// Internal professional engine for rendering clean code diff blocks with precise line metrics.
func (m *model) renderAdvancedDiff(diffContent string) string {
	lines := strings.Split(diffContent, "\n")
	var renderedLines []string

	styleDeletion := lipgloss.NewStyle().Background(lipgloss.Color("#3a1e24")).Foreground(lipgloss.Color("#f1707a"))
	styleAddition := lipgloss.NewStyle().Background(lipgloss.Color("#18302b")).Foreground(lipgloss.Color("#6cd0a1"))
	styleNormalText := lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))

	contentWidth := m.width - 14
	if contentWidth < 30 {
		contentWidth = 30
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
			if len(word) > maxW {
				if currentLine.Len() > 0 {
					chunks = append(chunks, currentLine.String())
					currentLine.Reset()
				}
				runes := []rune(word)
				for i := 0; i < len(runes); i += maxW {
					end := i + maxW
					if end > len(runes) {
						end = len(runes)
					}
					chunks = append(chunks, string(runes[i:end]))
				}
				continue
			}

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

	leftLineNum := 1
	rightLineNum := 1

	for _, line := range lines {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}

		if strings.HasPrefix(line, "@@") {
			wrappedLines := wrapStringToWidth(line, contentWidth)
			for _, wl := range wrappedLines {
				gutterStr := diffHunkStyle.Render("  ---  --- │ ")
				textStr := diffHunkStyle.Render(wl)
				renderedLines = append(renderedLines, gutterStr+textStr)
			}
			continue
		}

		if strings.HasPrefix(line, "-") {
			cleanLine := strings.TrimPrefix(line, "-")
			wrappedLines := wrapStringToWidth(cleanLine, contentWidth)

			for i, wl := range wrappedLines {
				var gutterStr string
				if i == 0 {
					gutterStr = diffLineNumSty.Render(fmt.Sprintf("  %3d      │ - ", leftLineNum))
					leftLineNum++
				} else {
					gutterStr = diffLineNumSty.Render("           │   ")
				}
				textStr := styleDeletion.Width(contentWidth).Render(wl)
				renderedLines = append(renderedLines, gutterStr+textStr)
			}
		} else if strings.HasPrefix(line, "+") {
			cleanLine := strings.TrimPrefix(line, "+")
			wrappedLines := wrapStringToWidth(cleanLine, contentWidth)

			for i, wl := range wrappedLines {
				var gutterStr string
				if i == 0 {
					gutterStr = diffLineNumHLSty.Render(fmt.Sprintf("       %3d │ + ", rightLineNum))
					rightLineNum++
				} else {
					gutterStr = diffLineNumHLSty.Render("           │   ")
				}
				textStr := styleAddition.Width(contentWidth).Render(wl)
				renderedLines = append(renderedLines, gutterStr+textStr)
			}
		} else {
			wrappedLines := wrapStringToWidth(line, contentWidth)

			for i, wl := range wrappedLines {
				var gutterStr string
				if i == 0 {
					gutterStr = diffLineNumSty.Render(fmt.Sprintf("  %3d  %3d │   ", leftLineNum, rightLineNum))
					leftLineNum++
					rightLineNum++
				} else {
					gutterStr = diffLineNumSty.Render("           │   ")
				}
				textStr := styleNormalText.Width(contentWidth).Render(wl)
				renderedLines = append(renderedLines, gutterStr+textStr)
			}
		}
	}
	return strings.Join(renderedLines, "\n")
}

// ── Confirmation dialog ───────────────────────────────────────────────────────

func (m *model) renderConfirmation(width int) string {
	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString(confirmDimStyle.Render("  proposed file changes:"))
	for _, p := range m.pendingProposals {
		inner.WriteString("\n  " + confirmFileStyle.Render("    "+p.File))
	}
	inner.WriteString("\n")
	inner.WriteString(confirmKeyStyle.Render("  [1] Accept") + confirmDescStyle.Render("  apply this batch"))
	inner.WriteString("\n")
	inner.WriteString(confirmKeyStyle.Render("  [2] Allow All") + confirmDescStyle.Render("  trust agent"))
	inner.WriteString("\n")
	inner.WriteString(confirmKeyStyle.Render("  [3] Reject") + confirmDescStyle.Render("  cancel all"))
	inner.WriteString("\n")
	boxWidth := 52
	if width < boxWidth+4 {
		boxWidth = width - 4
	}
	return confirmBoxStyle.Width(boxWidth).Render(inner.String())
}

// renderModeBar builds the interactive suggestions palette component view.
func (m *model) renderModeBar(_ int) string {
	var b strings.Builder
	current := "/" + m.resolver.Current().String()
	for i, mname := range coreModes {
		if i > 0 {
			b.WriteString(hairlineStyle.Render("  "))
		}
		if mname == current {
			mode, _ := modes.Parse(mname[1:])
			b.WriteString(modeTabActiveStyle.Foreground(modeAccentColor(mode)).Render(mname))
		} else {
			b.WriteString(modeTabInactiveStyle.Render(mname))
		}
	}
	return b.String()
}
