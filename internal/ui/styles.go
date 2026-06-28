package ui

import (
	"fmt"
	"math"
	"strconv"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
)

// ── Catppuccin Mocha palette ──────────────────────────────────────────────────
const (
	colorText    = "#cdd6f4"
	colorAccent  = "#a6e3a1"
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
	colorSubtle  = "#45475a"
	colorMuted   = "#6c7086"
	colorDimmed  = "#585b70"
	colorBase    = "#181825"
	colorCrust   = "#11111b"

	// Diff background overlays
	colorDiffAddBg  = "#1a2d1a" // dark green tint
	colorDiffDelBg  = "#2d1a1a" // dark red tint
	colorDiffAddFg  = "#a6e3a1" // mint green
	colorDiffDelFg  = "#f38ba8" // coral red
	colorDiffHunkFg = "#6c7086" // muted
	colorDiffCtxFg  = "#7f849c" // dim context lines

	// Line number gutter
	colorLineNumFg = "#45475a"
	colorLineNumHL = "#6c7086"

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
	colorGutterStatus = "#89dceb"
	colorGutterSystem = "#45475a"
)

// lipglossColor is a convenience helper.
func lipglossColor(hex string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
}

// ── Color interpolation for mode-line fade ────────────────────────────────────

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

// ── Shared text styles ────────────────────────────────────────────────────────
var (
	outputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	labelBoldStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))
	promptStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	sepStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))
	hairlineStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	infoStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	spinnerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Bold(true)
	errorStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	subtleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))

	// Agent output styles
	investigationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	evidenceStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan))
	hypothesisStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorTeal))
	reviewStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPink))
	scoreStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))

	riskCriticalStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	riskHighStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOrange))
	riskMediumStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	riskLowStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen))
	riskInfoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorBlue))

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

	// Chrome
	logoStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreenBr))
	versionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	dotStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	bulletStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).SetString(" • ")

	modeTabActiveStyle   = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	modeTabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed)).Padding(0, 1)

	statusLeftStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	statusRightStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	statusSepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle)).SetString(" │ ")

	// Gutter markers
	gutterUserStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterUser))
	gutterAIStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterAI))
	gutterErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterError))
	gutterStatusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus))
	gutterSysStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterSystem))

	labelUserStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterUser))
	labelAIStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterAI))
	labelErrorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterError))
	labelStatusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterStatus))

	// Code highlight
	hlKeyword = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
	hlString  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	hlComment = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	hlNumber  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve))
	hlType    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPink))
	hlCodeBg  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Background(lipgloss.Color(colorOverlay))
	hlLang    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Bold(true)

	// Diff (line-numbered style)
	diffAddBgStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDiffAddFg)).Background(lipgloss.Color(colorDiffAddBg))
	diffDelBgStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDiffDelFg)).Background(lipgloss.Color(colorDiffDelBg))
	diffHunkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDiffHunkFg))
	diffCtxStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDiffCtxFg))
	diffLineNumSty   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorLineNumFg))
	diffLineNumHLSty = lipgloss.NewStyle().Foreground(lipgloss.Color(colorLineNumHL))

	// Confirmation dialog
	confirmBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colorOrange)).
			Padding(0, 1)
	confirmKeyStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorOrange))
	confirmDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	confirmDimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	confirmFileStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent))
)

// ── Gutter / label helpers ────────────────────────────────────────────────────

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

func labelFor(r role) string {
	switch r {
	case roleUser:
		return labelUserStyle.Render("you")
	case roleAI:
		return labelAIStyle.Render("izen")
	case roleError:
		return labelErrorStyle.Render("error")
	case roleStatus:
		return labelStatusStyle.Render("done")
	default:
		return ""
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
