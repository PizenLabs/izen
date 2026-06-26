package ui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
)

// renderStartupBanner handles the localized identity block aligned clean-left
// with a consistent structural indentation to mimic high-end dev tools.
func (m *model) renderStartupBanner(width int) string {
	// 1. Color Palette Setup (Using Catppuccin Mocha Accents)
	accentColor := lipgloss.Color("#A6E3A1") // Emerald Green
	dimColor := lipgloss.Color("#6C7086")    // Slate Gray
	textColor := lipgloss.Color("#CDD6F4")   // Crisp Text

	// 2. Base Container Styles (Clean left alignment with explicit indentation padding)
	containerStyle := lipgloss.NewStyle().MarginLeft(4).MarginTop(1).MarginBottom(1)
	logoStyle := lipgloss.NewStyle().Foreground(accentColor)
	metaStyle := lipgloss.NewStyle().MarginLeft(3).Foreground(textColor)

	// 3. Left Column: Compact Geometric Icon Box
	logoBlock := logoStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		"┌──┐",
		"│▒▒│ IZEN",
		"└──┘",
	))

	// 4. Gather unique operational metrics to avoid top-bar information redundancy
	username := os.Getenv("USER")
	if username == "" {
		username = "developer"
	}

	// Determine backend execution sandbox state
	sandboxStatus := "isolated-local"
	if m.cfg.ActiveProviderName() != "ollama" {
		sandboxStatus = "cloud-verified"
	}

	// 5. Right Column: High-density Profile & Diagnostics (Non-redundant data)
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

	// 6. Combine structurally using top-edge anchoring
	banner := lipgloss.JoinHorizontal(lipgloss.Top, logoBlock, metaBlock)

	// 7. Render inside the left-aligned margin box wrapper
	return containerStyle.Render(banner)
}
