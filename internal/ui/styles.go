package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
)

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

	colorModeAsk         = "#a6e3a1"
	colorModePlan        = "#cba6f7"
	colorModeBuild       = "#b9f0b4"
	colorModeInvestigate = "#f9e2af"
	colorModeReview      = "#f5c2e7"
	colorModeCommit      = "#89b4fa"

	colorGutterUser   = "#a6e3a1"
	colorGutterAI     = "#89b4fa"
	colorGutterError  = "#f38ba8"
	colorGutterStatus = "#89dceb"
	colorGutterSystem = "#45475a"
)

var spinnerFrames = []string{
	"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
}

var (
	outputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	labelBoldStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))

	promptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent))

	sepStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))
	hairlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	infoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	spinnerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Bold(true)
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	subtleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))

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

	logoStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreenBr))
	versionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	dotStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	bulletStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).SetString(" • ")

	modeTabActiveStyle   = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	modeTabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed)).Padding(0, 1)
	modeLabelStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))

	statusLeftStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	statusRightStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	statusSepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle)).SetString(" │ ")

	gutterUserStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterUser))
	gutterAIStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterAI))
	gutterErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterError))
	gutterStatusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus))
	gutterSysStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterSystem))

	labelUserStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterUser))
	labelAIStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterAI))
	labelErrorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterError))
	labelStatusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterStatus))

	hlKeyword = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
	hlString  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	hlComment = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	hlNumber  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve))
	hlType    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPink))
	hlCodeBg  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Background(lipgloss.Color(colorOverlay))
	hlLang    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Bold(true)

	confirmBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colorOrange)).
			Padding(0, 1)
	confirmKeyStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorOrange))
	confirmDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	confirmDimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	confirmFileStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent))
)

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

func modeAccentColor(m modes.Mode) lipgloss.Color {
	switch m {
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
	case modes.ModeCommit:
		return lipgloss.Color(colorModeCommit)
	default:
		return lipgloss.Color(colorAccent)
	}
}
