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
	// The modal interior deliberately carries NO solid background fill so
	// the terminal-native background/transparency shows through; only the
	// mauve rounded outer-frame border (#cba6f7) and Surface1 structural
	// tones (#45475a) are applied.
	dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#45475a"))

	// selectedRowStyle is the active-row highlight: Surface0 background
	// (#313244) with Text (#cdd6f4) and the ">" cursor in Mauve (#cba6f7).
	selectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#cdd6f4")).
				Background(lipgloss.Color("#313244"))

	// normalRowStyle is the unselected row: default background, Text.
	normalRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#cdd6f4"))

	// cursorStyle renders the ">" cursor in Mauve (#cba6f7).
	cursorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#cba6f7")).
			Background(lipgloss.Color("#313244"))

	// metaStyle renders muted metadata (context, pricing) in Subtext0.
	metaStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6adc8"))

	// selectedMetaStyle renders muted metadata on the selected row:
	// Subtext0 text on Surface0 background.
	selectedMetaStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#a6adc8")).
				Background(lipgloss.Color("#313244"))

	// inactiveProviderStyle renders unselected provider pills in Surface2.
	inactiveProviderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70"))

	// Per-provider badges (Catppuccin Mocha accents).
	openRouterBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fab387"))
	anthropicBadge  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	openAIBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	ollamaBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89dceb"))
	geminiBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
	deepseekBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#94e2d5"))
	providerBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#6c7086"))
)
