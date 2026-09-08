package ask

import (
	"github.com/charmbracelet/lipgloss"
)

// Catppuccin Mocha palette shared with the rest of the Izen TUI.
const (
	colorText     = "#cdd6f4"
	colorSubtext  = "#a6adc8"
	colorMuted    = "#6c7086"
	colorOverlay  = "#313244"
	colorMantle   = "#1e1e2e"
	colorMauve    = "#cba6f7"
	colorBlue     = "#89b4fa"
	colorGreen    = "#a6e3a1"
	colorYellow   = "#f9e2af"
	colorOrange   = "#fab387"
	colorPeach    = "#fab387"
	colorSurface0 = "#313244"
	colorSurface1 = "#45475a"
)

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(colorMantle)).
			Background(lipgloss.Color(colorMauve)).
			Padding(0, 1)

	tabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(colorMauve))

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorSubtext))

	questionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorText)).
			Width(78).
			MaxWidth(78)

	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorMuted))

	optionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorText))

	descStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorMuted))

	optionFocusedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorMantle)).
				Background(lipgloss.Color(colorBlue)).
				Padding(0, 1)

	descFocusedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorBlue))

	inputLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(colorYellow))

	inputPlaceholderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorMuted)).
				Italic(true)

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGreen))
)
