package ui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) renderStartupBanner(width int) string {
	accentColor := lipgloss.Color("#A6E3A1")
	dimColor := lipgloss.Color("#6C7086")
	textColor := lipgloss.Color("#CDD6F4")

	containerStyle := lipgloss.NewStyle().MarginLeft(4).MarginTop(1).MarginBottom(1)
	logoStyle := lipgloss.NewStyle().Foreground(accentColor)
	metaStyle := lipgloss.NewStyle().MarginLeft(3).Foreground(textColor)

	logoBlock := logoStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		"┌──┐",
		"│▒▒│ IZEN",
		"└──┘",
	))

	username := os.Getenv("USER")
	if username == "" {
		username = "developer"
	}

	sandboxStatus := "isolated-local"
	if m.cfg.ActiveProviderName() != "ollama" {
		sandboxStatus = "cloud-verified"
	}

	profileLine := fmt.Sprintf("%s  %s",
		lipgloss.NewStyle().Bold(true).Foreground(accentColor).Render(username+"@izen"),
		lipgloss.NewStyle().Foreground(dimColor).Render("• interactive developer container"),
	)
	engineLine := fmt.Sprintf("%s  %s",
		lipgloss.NewStyle().Foreground(accentColor).Render("├─ runtime:"),
		lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf("%s sandbox mode active", sandboxStatus)),
	)
	engineTarget := fmt.Sprintf("%s  %s",
		lipgloss.NewStyle().Foreground(accentColor).Render("└─ ast-graph:"),
		lipgloss.NewStyle().Foreground(dimColor).Render("tree-sitter optimization initialized"),
	)

	metaBlock := metaStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		profileLine,
		engineLine,
		engineTarget,
	))

	banner := lipgloss.JoinHorizontal(lipgloss.Top, logoBlock, metaBlock)

	return containerStyle.Render(banner)
}
