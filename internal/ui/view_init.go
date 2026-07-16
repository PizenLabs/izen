package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	initAccentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	initDimmedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	initMutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	initTextStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	initCyanStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorCyan))
	initGreenStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreen))
	initRedStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	initSubStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))
)

func (m *model) renderInitView() string {
	width := m.width
	if width < 60 {
		width = 60
	}

	var b strings.Builder

	// ── Branded ASCII Banner ──
	b.WriteString(m.renderInitBanner(width))
	b.WriteString("\n")

	// ── Stage-specific content ──
	switch m.initStage {
	case initGitCheck:
		b.WriteString(m.renderInitGitCheck(width))
	case initConfirm:
		b.WriteString(m.renderInitConfirm(width))
	case initIdentity:
		b.WriteString(m.renderInitIdentity(width))
	case initProviderSelect:
		b.WriteString(m.renderInitProviderSelect(width))
	}

	b.WriteString("\n")

	// ── Bottom help bar ──
	b.WriteString(initSubStyle.Render(strings.Repeat("─", width)) + "\n")
	b.WriteString(m.renderInitHelp(width))

	return b.String()
}

func (m *model) renderInitBanner(width int) string {
	cleanRobotArt := []string{
		"  ██  ",
		" █  █ ",
		" ████ ",
		" █ ██ ",
		" █  █ ",
	}

	const robotW = 6
	const sep = "  "

	rightCol := []string{
		initAccentStyle.Render(m.getGreeting()),
		initTextStyle.Render("engineering intelligence."),
		initTextStyle.Render("human in control."),
	}

	var rows []string
	totalRows := len(cleanRobotArt)
	if len(rightCol) > totalRows {
		totalRows = len(rightCol)
	}
	for i := 0; i < totalRows; i++ {
		var robotPart string
		if i < len(cleanRobotArt) {
			robotPart = initAccentStyle.Render(padRight(cleanRobotArt[i], robotW))
		} else {
			robotPart = strings.Repeat(" ", robotW)
		}
		var rightPart string
		if i < len(rightCol) {
			rightPart = rightCol[i]
		}
		rows = append(rows, robotPart+sep+rightPart)
	}

	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *model) renderInitGitCheck(width int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(initSubStyle.Render(strings.Repeat("─", width)) + "\n")
	b.WriteString("\n")
	b.WriteString(initTextStyle.Render("Workspace setup"))
	b.WriteString("\n")
	b.WriteString(initDimmedStyle.Render("No Git repository detected. IZEN can initialize one for you."))
	b.WriteString("\n\n")

	if m.initGitInitErr != "" {
		b.WriteString(initTextStyle.Render("  " + initRedStyle.Render("!") + " " + m.initGitInitErr))
		b.WriteString("\n\n")
	}

	choice := "  " + initGreenStyle.Render("●") + initTextStyle.Render(" Initialize git") + "    " + initDimmedStyle.Render("○ Skip")
	b.WriteString(choice)
	b.WriteString("\n")
	b.WriteString(initDimmedStyle.Render("  (press ") + initCyanStyle.Render("Y") + initDimmedStyle.Render(" to init with 'main' branch, ") + initCyanStyle.Render("N") + initDimmedStyle.Render(" to skip)"))

	return b.String()
}

func (m *model) renderInitConfirm(width int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(initSubStyle.Render(strings.Repeat("─", width)) + "\n")
	b.WriteString("\n")
	b.WriteString(initTextStyle.Render("Welcome to IZEN — engineering intelligence at your terminal."))
	b.WriteString("\n")
	b.WriteString(initDimmedStyle.Render("This quick setup configures your workspace identity and AI provider."))
	b.WriteString("\n\n")

	choice := "  " + initGreenStyle.Render("●") + initTextStyle.Render(" Yes") + "    " + initDimmedStyle.Render("○ No")
	b.WriteString(choice)
	b.WriteString("\n")
	b.WriteString(initDimmedStyle.Render("  (press ") + initCyanStyle.Render("Y") + initDimmedStyle.Render(" to begin, ") + initCyanStyle.Render("N") + initDimmedStyle.Render(" to skip)"))

	return b.String()
}

func (m *model) renderInitIdentity(width int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(initSubStyle.Render(strings.Repeat("─", width)) + "\n")
	b.WriteString("\n")
	b.WriteString(initTextStyle.Render("Your identity"))
	b.WriteString("\n")
	b.WriteString(initDimmedStyle.Render("Set your workspace username. Press ") + initCyanStyle.Render("Enter") + initDimmedStyle.Render(" to confirm."))
	b.WriteString("\n\n")

	label := initMutedStyle.Render("@ ")
	val := m.initIdentityInput.View()
	b.WriteString(label + val)
	b.WriteString("\n")

	return b.String()
}

func (m *model) renderInitProviderSelect(width int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(initSubStyle.Render(strings.Repeat("─", width)) + "\n")
	b.WriteString("\n")
	b.WriteString(initTextStyle.Render("AI provider selection"))
	b.WriteString("\n")
	b.WriteString(initDimmedStyle.Render("Choose your default AI provider. Type to filter the list."))
	b.WriteString("\n\n")

	if m.initProviderFilter != "" {
		b.WriteString(initMutedStyle.Render("  filter: ") + initTextStyle.Render(m.initProviderFilter))
		b.WriteString("\n\n")
	}

	items := m.filteredProviders()
	activeProvider := m.getActiveProviderName()
	for i, item := range items {
		glyph := "○"
		style := initDimmedStyle
		if i == m.initProviderIdx {
			glyph = "●"
			style = initGreenStyle
		}
		status := ""
		if item == activeProvider {
			status = initMutedStyle.Render(" (active)")
		}
		line := fmt.Sprintf("  %s %s%s", style.Render(glyph), initTextStyle.Render(item), status)
		b.WriteString(line)
		b.WriteString("\n")
	}

	return b.String()
}

func (m *model) renderInitHelp(width int) string {
	var hint string
	switch m.initStage {
	case initGitCheck:
		hint = "Y: init git • N: skip"
	case initConfirm:
		hint = "Y: confirm • N: skip"
	case initIdentity:
		hint = "Enter: confirm • Esc: skip"
	case initProviderSelect:
		hint = "↑/↓ to select • Enter: confirm • Type: to search"
	}
	return initMutedStyle.Render(hint)
}
