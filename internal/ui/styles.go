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

	// Top Bar
	colorTopBarMetrics = "#a6adc8" // Subtext0
)

// lipglossColor is a convenience helper.
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

func animLineColor(m *model) lipgloss.Color {
	if !m.lineAnimating {
		return modeLineColor(m.resolver.Current())
	}
	neutral := lipgloss.Color(colorModeNeutral)
	target := modeLineColor(m.lineAnimTargetMode)
	t := m.lineAnimProgress
	if t < 0.5 {
		return interpolateColor(modeLineColor(m.resolver.Current()), neutral, t*2)
	}
	return interpolateColor(neutral, target, (t-0.5)*2)
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
	promptStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	infoStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	errorStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))

	// Suggestion palette
	paletteBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colorSubtle)).
			Padding(0, 1)
	paletteSectionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMuted))
	paletteSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	paletteItemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	paletteCoreItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	paletteHintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	palettePathStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	paletteSelectedPath  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent))

	// Accepted green dot — single-line collapsed summary
	acceptedDotStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen)).Render("●")
	acceptedLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen))

	// Shell execution proposal
	shellWarningStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorOrange))
	shellBorderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	shellCmdStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Background(lipgloss.Color(colorOverlay))

	// Gutter markers
	gutterUserStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterUser))
	gutterAIStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterAI))
	gutterErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterError))
	gutterStatusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus))
	gutterSysStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterSystem))

	labelUserStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterUser))

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

func cmdCategory(cmd string) string {
	for _, c := range coreModes {
		if c == cmd {
			return "core"
		}
	}
	for _, c := range globalCommands {
		if c == cmd {
			return "global"
		}
	}
	return "utility"
}
