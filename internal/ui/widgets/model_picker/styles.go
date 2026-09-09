package model_picker

import (
	"github.com/charmbracelet/lipgloss"
)

// Compact Lipgloss styles for the contextual command surface. Catppuccin
// Mocha palette: mauve borders, blue headers, per-provider badges, Surface0
// row selection. All styles are package-private except where tests need
// them; keep zero I/O.
var (
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f5a623")).Bold(true)
	defaultBadge  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	planBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
	thinkBadge    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	visionBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89dceb"))
	otherBadge    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f5a623"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
	okStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
	syncOkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
	syncBusyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	syncWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))

	// dividerStyle is the subtle horizontal rule under the header.
	dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#45475a"))

	// selectedRowStyle is the active-row highlight: Surface0 background
	// (#313244) with bold white text and the ">" cursor.
	selectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#ffffff")).
				Background(lipgloss.Color("#313244"))

	// Per-provider badges (Catppuccin Mocha accents).
	openRouterBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fab387"))
	anthropicBadge  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	openAIBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	ollamaBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89dceb"))
	geminiBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
	deepseekBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#94e2d5"))
	providerBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#6c7086"))
)
