package model_picker

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
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

	// inactiveProviderStyle renders unselected provider pills in Surface2.
	//nolint:unused // retained for spec compatibility
	inactiveProviderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70"))

	// providerFilterActive is the highlighted provider pill: Yellow bold.
	providerFilterActive = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Bold(true)
	// providerFilterInactive is the muted provider pill: Subtext0.
	providerFilterInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))

	// Per-provider badges (Catppuccin Mocha accents – legacy identifiers kept for
	// backward compat; palette updated to spec's explicit Mocha mapping).
	openRouterBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7")) // Mauve per spec
	anthropicBadge  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f9e2af")) // Yellow per spec
	openAIBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#94e2d5")) // Teal per spec
	ollamaBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1")) // Green per spec
	geminiBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa")) // Blue per spec
	deepseekBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#94e2d5"))
	providerBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#6c7086"))

	// Catppuccin Mocha reasoning effort palette (spec: Header Layout Lock task).
	// default=mauve, none=subtext0, low=green, medium=yellow, high=peach,
	// xhigh=flamingo, max=red. Bold for visibility; selected adds underline.
	effortDefaultStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#cba6f7")).Bold(true)
	effortNoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	effortLowStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Bold(true)
	effortMediumStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Bold(true)
	effortHighStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#fab387")).Bold(true)
	effortXHighStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f2cdcd")).Bold(true)
	effortMaxStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Bold(true)
)

var effortStyles = map[string]lipgloss.Style{
	"default": effortDefaultStyle,
	"none":    effortNoneStyle,
	"off":     effortNoneStyle,
	"low":     effortLowStyle,
	"medium":  effortMediumStyle,
	"high":    effortHighStyle,
	"xhigh":   effortXHighStyle,
	"max":     effortMaxStyle,
	// ToggleAuto "auto" maps to medium (balanced), "on" to high.
	"auto": effortMediumStyle,
	"on":   effortHighStyle,
}

// providerBadgeStyles is the spec's explicit Catppuccin Mocha badge map used
// by renderProviderBadge for table rows.
var providerBadgeStyles = map[string]lipgloss.Style{
	"openrouter": lipgloss.NewStyle().Foreground(lipgloss.Color("#cba6f7")).Bold(true), // Mauve
	"gemini":     lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Bold(true), // Blue
	"groq":       lipgloss.NewStyle().Foreground(lipgloss.Color("#fab387")).Bold(true), // Peach
	"ollama":     lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Bold(true), // Green
	"openai":     lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5")).Bold(true), // Teal
	"anthropic":  lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Bold(true), // Yellow
	"cohere":     lipgloss.NewStyle().Foreground(lipgloss.Color("#f2cdcd")).Bold(true), // Flamingo
}

// renderProviderBadge returns a colored provider pill clipped to width.
// Style follows the Catppuccin Mocha map; unknown providers fall back to Sky (#89dceb).
func renderProviderBadge(provider string, width int) string {
	pLower := strings.ToLower(provider)
	style, ok := providerBadgeStyles[pLower]
	if !ok {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("#89dceb")).Bold(true) // Sky default
	}
	raw := fmt.Sprintf("[%s]", strings.ToUpper(provider))
	truncated := runewidth.Truncate(raw, width, "")
	padded := padRightExact(truncated, width)
	return style.Render(padded)
}
