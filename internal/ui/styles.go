package ui

import (
	"fmt"
	"math"
	"strconv"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
)

// ── Catppuccin Mocha Palette (Optimized Visual Hierarchy) ─────────────────────
const (
	colorText    = "#cdd6f4" // Dominant foreground text
	colorAccent  = "#a6e3a1" // High-fidelity mint green
	colorGreen   = "#a6e3a1"
	colorGreenBr = "#b9f0b4"
	colorRed     = "#f38ba8"
	colorMaroon  = "#eba0ac"
	colorOrange  = "#fab387"
	colorYellow  = "#f9e2af"
	colorCyan    = "#89dceb"
	colorTeal    = "#94e2d5"
	colorPink    = "#f5c2e7"
	colorBlue    = "#89b4fa"
	colorMauve   = "#cba6f7"

	colorSurface = "#1e1e2e"
	colorOverlay = "#313244"
	colorSubtle  = "#45475a" // Clean structural borders
	colorMuted   = "#6c7086" // Secondary contextual data
	colorDimmed  = "#585b70" // Muted background data (Tokens, Stats)
	colorBase    = "#181825"
	colorCrust   = "#11111b"

	// Diff background overlays
	colorDiffAddBg  = "#1a2d1a" // Subtle dark green tint
	colorDiffDelBg  = "#2d1a1a" // Subtle dark red tint
	colorDiffAddFg  = "#a6e3a1"
	colorDiffDelFg  = "#f38ba8"
	colorDiffHunkFg = "#585b70" // Dimmed hunk metrics
	colorDiffCtxFg  = "#6c7086"

	// Line number gutter (High-Fidelity low-contrast tracking)
	colorLineNumFg = "#45475a" // Hard dim for passive line numbers
	colorLineNumHL = "#6c7086" // Active line highlight

	// Mode accent colors — per design spec
	colorModeAsk         = "#a6e3a1"
	colorModePlan        = "#fab387"
	colorModeBuild       = "#89b4fa"
	colorModeInvestigate = "#cba6f7"
	colorModeReview      = "#f9e2af"
	colorModeNeutral     = "#313244"

	colorGutterUser   = "#a6e3a1"
	colorGutterAI     = "#89b4fa"
	colorGutterError  = "#f38ba8"
	colorGutterStatus = "#585b70" // Dimmed status tracking gutter
	colorGutterSystem = "#45475a"
)

// lipglossColor is a convenience helper (init-time only, NOT for render path).
func lipglossColor(hex string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
}

// ── Color Interpolation For Mode-Line Fade ────────────────────────────────────

func hexToRGB(hex string) (r, g, b float64) {
	if len(hex) == 7 && hex[0] == '#' {
		rv, _ := strconv.ParseUint(hex[1:3], 16, 8)
		gv, _ := strconv.ParseUint(hex[3:5], 16, 8)
		bv, _ := strconv.ParseUint(hex[5:7], 16, 8)
		return float64(rv), float64(gv), float64(bv)
	}
	return 200, 200, 200
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func interpolateColor(from, to lipgloss.Color, t float64) lipgloss.Color {
	t = math.Max(0, math.Min(1, t))
	fr, fg, fb := hexToRGB(string(from))
	tr, tg, tb := hexToRGB(string(to))
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x",
		uint8(lerp(fr, tr, t)),
		uint8(lerp(fg, tg, t)),
		uint8(lerp(fb, tb, t)),
	))
}

func modeLineColor(mode modes.Mode) lipgloss.Color {
	switch mode {
	case modes.ModeAsk:
		return lipgloss.Color(colorModeAsk)
	case modes.ModePlan:
		return lipgloss.Color(colorModePlan)
	case modes.ModeBuild:
		return lipgloss.Color(colorModeBuild)
	case modes.ModeInvestigate:
		return lipgloss.Color(colorModeInvestigate)
	case modes.ModeReview:
		return lipgloss.Color(colorModeReview)
	default:
		return lipgloss.Color(colorModeNeutral)
	}
}

func modeAccentColor(m modes.Mode) lipgloss.Color { return modeLineColor(m) }

// ── Shared Text Styles (Refactored Contrast Levels) ───────────────────────────
var (
	outputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	labelBoldStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))
	infoStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	errorStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))

	// Shell execution proposal
	shellWarningStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorOrange))

	// Gutter markers
	gutterUserStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterUser))
	gutterAIStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterAI))
	gutterErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterError))
	gutterStatusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus))
	gutterSysStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterSystem))

	// Code highlight
	hlCodeBg = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Background(lipgloss.Color(colorOverlay))

	// Diff (Dynamic Layout)
	diffAddBgStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDiffAddFg)).Background(lipgloss.Color(colorDiffAddBg))
	diffDelBgStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDiffDelFg)).Background(lipgloss.Color(colorDiffDelBg))
	diffHunkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDiffHunkFg))
	diffCtxStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDiffCtxFg))
	diffLineNumSty   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorLineNumFg))
	diffLineNumHLSty = lipgloss.NewStyle().Foreground(lipgloss.Color(colorLineNumHL))
)

// ── Pre-Compiled Render-Path Styles (Zero NewStyle in View/rebuildViewport) ─────
var (
	// Foreground-only helpers
	dimmedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))
	textStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	orangeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOrange))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent))
	redStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed))
	blueStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorBlue))

	// Bold + color
	boldTextStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))
	boldAccentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))

	// Startup banner border
	bannerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(colorSubtle)).
				Padding(1, 2)

	// Widget box
	widgetTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))

	// Catppuccin Mocha soft interrupt indicator
	interruptLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMaroon)).Faint(true)

	// Semantic renderer diff styles
	semanticAddStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#18302b")).Foreground(lipgloss.Color("#6cd0a1"))
	semanticNormalStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
)

// ── Hotkey Highlight Styles (Keyboard-Only Execution) ─────────────────────────
var (
	hotkeyStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))
	hotkeyHintStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	tracerStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	successBannerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreen))
	failureBannerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	warningBannerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorYellow))

	// Warning box style for safety gate
	warningStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colorOrange)).
			Foreground(lipgloss.Color(colorYellow)).
			Padding(0, 1)
)

func renderHotkeyPromptWithToggle(width int) string {
	hk := hotkeyStyle.Render
	hint := hotkeyHintStyle.Render
	text := hint("Press ") + hk("A") + hint(" to accept   ") +
		hk("L") + hint(" to allow all   ") +
		hk("R") + hint(" to reject   ") +
		hk("P") + hint(" to toggle   ") +
		hk("j/k") + hint(" to navigate")
	if lipgloss.Width(text) > width {
		text = hint("Press ") + hk("A") + hint(" acc  ") +
			hk("L") + hint(" all  ") +
			hk("R") + hint(" rej  ") +
			hk("P") + hint(" tog  ") +
			hk("j/k") + hint(" nav")
		if lipgloss.Width(text) > width {
			text = hint(" ") + hk("A/L/R/P") + hint(" act  ") +
				hk("j/k") + hint(" nav")
		}
	}
	return text
}

// Mode-accent style lookup (indexed by modes.Mode value).
var (
	modeBoldFgStyles = []lipgloss.Style{
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModeAsk)),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModePlan)),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModeBuild)),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModeInvestigate)),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModeReview)),
	}
	// Secondary/utils mode style — unified subtle color for non-core modes.
	secondaryModeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMuted))
)

// isCoreEngineeringMode returns true for /ask, /build, /investigate, /review.
func isCoreEngineeringMode(m modes.Mode) bool {
	return m == modes.ModeAsk || m == modes.ModeBuild ||
		m == modes.ModeInvestigate || m == modes.ModeReview
}

// Pre-compiled Markdown renderer styles (render-path — zero NewStyle).
var (
	mdEmphasisStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Italic(true)
	mdStrongStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	mdH1Style         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	mdH2Style         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	mdH3Style         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
	mdH4Style         = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	mdCodeSpanStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Background(lipgloss.Color("#1e1e2e"))
	mdLinkStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Underline(true)
	mdMutedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	mdCodeContStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	mdAccentStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa"))
	mdSepStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	mdImageMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	mdHeaderBoldCell  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	mdCellStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	mdBulletStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))

	// Callout label styles per keyword
	mdCalloutStyles = map[string]lipgloss.Style{
		"IMPORTANT": lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f38ba8")),
		"NOTE":      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa")),
		"TIP":       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1")),
		"WARNING":   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f9e2af")),
		"CAUTION":   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fab387")),
	}
)

// User message background (warm muted surface for distinct visual nesting)
var userBgStyle = lipgloss.NewStyle().Background(lipgloss.Color(colorSurface)).PaddingLeft(1)

// ── Interrupt Boundary Spinner ────────────────────────────────────────────
var ProposalSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var SpinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve))

// ── Gutter / Label Helpers ────────────────────────────────────────────────────

func gutterFor(r role) string {
	switch r {
	case roleUser:
		return gutterUserStyle.Render("▌") + " "
	case roleAI:
		return gutterAIStyle.Render("▌") + " "
	case roleError:
		return gutterErrorStyle.Render("▌") + " "
	case roleStatus:
		return gutterStatusStyle.Render("▌") + " "
	case roleSystem:
		return gutterSysStyle.Render("╎") + " "
	default:
		return "  "
	}
}
