package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/ui/components"
)

// ── Catppuccin Mocha Palette (Optimized Visual Hierarchy) ─────────────────────
const (
	colorText     = "#cdd6f4" // Dominant foreground text
	colorAccent   = "#a6e3a1" // High-fidelity mint green
	colorGreen    = "#a6e3a1"
	colorGreenBr  = "#b9f0b4"
	colorRed      = "#f38ba8"
	colorMaroon   = "#eba0ac"
	colorOrange   = "#fab387"
	colorYellow   = "#f9e2af"
	colorCyan     = "#89dceb"
	colorTeal     = "#94e2d5"
	colorPink     = "#f5c2e7"
	colorBlue     = "#89b4fa"
	colorMauve    = "#cba6f7"
	colorSapphire = "#74c7ec"

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

// boundBox is THE bounding entry point for every framed (bordered) TUI surface.
//
// Lipgloss applies a border AFTER the content box, so `style.Width(w)` renders
// at w+2 cells. Handing w = viewport width therefore pushes the right border two
// cells past the viewport, which wraps the terminal and corrupts the frame
// whenever the payload is long (an OpenRouter 403 JSON body, a [PARTIAL]
// truncation notice, a multi-kilobyte build error). boundBox subtracts the
// safety margin and the border, and states the word-wrap contract explicitly,
// so a long string always breaks inside the frame instead of breaking the
// frame. See internal/ui/components/banner.go for the full contract.
func boundBox(style lipgloss.Style, viewportWidth int) components.Bounded {
	return components.Bound(style, viewportWidth)
}

// boundInner returns the content width available INSIDE a framed surface drawn
// with boundBox: the outer width minus the box's own border and padding. Any
// horizontal rule or wrapped body placed inside the card must use it, otherwise
// the rule outgrows the frame and the right border is pushed off-screen.
//
// It delegates to components.InnerWidth so the budget is derived from the
// style's own border size — a half-bordered or borderless box is measured
// correctly instead of being charged a full two cells it cannot spend.
func boundInner(viewportWidth int, style lipgloss.Style) int {
	return components.InnerWidth(viewportWidth, style)
}

// boundRule draws a horizontal separator sized to the card's inner width. The
// width is clamped to a minimum so a very narrow terminal degrades to a short
// rule instead of a negative Repeat count.
func boundRule(viewportWidth int, style lipgloss.Style, indent int) string {
	n := boundInner(viewportWidth, style) - indent
	if n < 8 {
		n = 8
	}
	return strings.Repeat("─", n)
}

// boundedWarning renders the amber warning/gate banner (policy notice,
// [PARTIAL] truncation notice, oversized-repo warning) inside a frame whose
// width is derived from the live viewport.
//
// These notices are the longest free-form strings the UI ever renders — a
// [PARTIAL] notice alone is ~190 cells — so without an explicit bound the box
// sizes itself to the payload and shoves the right border far off-screen,
// wrapping the terminal and corrupting every frame around it.
func boundedWarning(text string, viewportWidth int) string {
	return boundBox(warningStyle, viewportWidth).
		Render(components.WrapBody(strings.TrimSpace(text), boundInner(viewportWidth, warningStyle)))
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
	cyanStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan))

	// Bold + color
	boldTextStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))
	boldAccentStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	boldSapphireStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorSapphire))
	boldMauveStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))

	// Startup banner border
	bannerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(colorSubtle)).
				Padding(1, 2)

	// Language badge for detected project type
	langBadgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorTeal)).
			Background(lipgloss.Color(colorSurface)).
			Padding(0, 1).
			Bold(true)

	// Catppuccin Mocha soft interrupt indicator (legacy; the double-tap Esc
	// protocol renders styleDimHint/styleWarnHint instead).
	//nolint:unused
	interruptLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMaroon)).Faint(true)

	// Double-tap Esc footer hints: subtle idle affordance vs high-contrast
	// re-confirm warning. Single-sourced here so the footer and the status
	// header speak the same interrupt language.
	styleDimHint  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen)).Faint(true)
	styleWarnHint = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Bold(true)

	// Semantic renderer diff styles
	semanticAddStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#18302b")).Foreground(lipgloss.Color("#6cd0a1"))
	semanticNormalStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))

	// System Activity Log Style — Catppuccin Mocha Dimmed.
	// Highly muted faint gray so logs flow seamlessly without
	// bloating the main chat aesthetic. [ OK ] and [ FAIL ] tags
	// within the text are colorized separately in the view layer.
	systemActivityStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorDimmed)).
				Faint(true)
)

// ── Permission Box Styles (Build Approval Gate) ────────────────────────────────
var (
	// permissionBoxStyle wraps the entire permission-required dialog in a
	// distinctive red/orange bordered box so the user instantly recognises
	// an interactive security checkpoint.
	//
	// NO hardcoded Width: the width is always derived from the live viewport
	// through boundBox, so the frame can never outgrow the terminal.
	permissionBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(lipgloss.Color(colorOrange)).
				Padding(0, 1)
	permissionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorYellow))
	permissionTargetStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorText)).
				PaddingLeft(2)
	permissionKeyStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorMauve))
	permissionDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorMuted))

	// ── Decomposition Proposal Card (DECOMPOSITION_PROPOSAL gate) ──────────
	// The staged ExecutionDAG proposal renders as a yellow-framed interactive
	// decision box so it is instantly distinguishable from the orange
	// permission/approval gates while still reading as "human decision here".
	// Width is supplied per-frame by boundBox (never hardcoded).
	decompositionBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(lipgloss.Color(colorYellow)).
				Padding(0, 1)
	decompositionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorYellow))
	decompositionKeyStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorMauve))
)

// ── Hotkey Highlight Styles (Keyboard-Only Execution) ─────────────────────────
var (
	hotkeyStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))
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

	// ── Build Mutation Summary Box ──────────────────────────────
	// Clean styled box with border highlights for BUILD MUTATION SUMMARY
	// and other system summary output rendered through the TUI style layer.
	buildSummaryBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color(colorBlue)).
				BorderTop(false).
				BorderBottom(false).
				BorderLeft(false).
				BorderRight(false).
				Padding(0, 1)
	buildSummaryTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorBlue)).
				PaddingLeft(1)

	// ── Status Badge Styles ─────────────────────────────────────
	// Rendered inline within system summary text to replace raw
	// Markdown bold/asterisk syntax with clean colored TUI badges.
	badgeOKStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreen)).Background(lipgloss.Color(colorSurface))
	badgeFailStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed)).Background(lipgloss.Color(colorSurface))

	// ── Self-Healing Badge Styles ───────────────────────────────
	// Distinct visual indicators for the build self-healing loop:
	// a warm retry badge for each regeneration attempt and a red
	// exhausted badge when retries run out.
	badgeRetryStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorYellow)).Background(lipgloss.Color(colorSurface))
	badgeExhaustedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed)).Background(lipgloss.Color(colorSurface))

	// ── Tool Policy Rejection Badge ──────────────────────────────
	// Muted warm badge for forbidden tool-call notices (e.g. /ask rejecting a
	// shell command). Rendered inline as a status badge so the rejection reads
	// as a clean policy notice rather than raw unformatted error text.
	toolRejectBadgeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMaroon)).Background(lipgloss.Color(colorSurface))

	// ── Thought Log Box Style ───────────────────────────────────
	// Bordered container for expanded LLM reasoning content.
	thoughtLogBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(colorMauve)).
				Padding(0, 1)
	thoughtLogTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorMauve))

	// ── System Log Box Style ────────────────────────────────────
	// Bordered container for raw system execution log output.
	systemLogBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color(colorDimmed)).
				Padding(0, 1)
	systemLogTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorDimmed))
)

func renderHotkeyPromptWithToggle(width int) string {
	action := boldTextStyle.Render
	hk := dimmedStyle.Render

	text := "› " + action("Accept") + " " + hk("Alt+A / Enter") + "  " +
		action("Allow All") + " " + hk("Alt+L") + "  " +
		action("Reject") + " " + hk("Alt+R / Esc") + "  " +
		action("Toggle") + " " + hk("Alt+P") + "  " +
		action("Scroll") + " " + hk("↑/↓")

	if lipgloss.Width(text) > width {
		text = "› " + action("Accept") + " " + hk("Alt+A") + "  " +
			action("Allow") + " " + hk("Alt+L") + "  " +
			action("Reject") + " " + hk("Alt+R") + "  " +
			action("Toggle") + " " + hk("Alt+P") + "  " +
			action("Scroll") + " " + hk("↑/↓")
		if lipgloss.Width(text) > width {
			text = "› " + action("Acc/Rej") + " " + hk("Alt+A/R") + "  " +
				action("All") + " " + hk("Alt+L")
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

// isCoreEngineeringMode returns true for /ask, /plan, /build, /investigate, /review.
func isCoreEngineeringMode(m modes.Mode) bool {
	return m == modes.ModeAsk || m == modes.ModePlan || m == modes.ModeBuild ||
		m == modes.ModeInvestigate || m == modes.ModeReview
}

// ── Header / Footer / Approval / Distinction Styles ───────────────────────
var (
	// Header
	HeaderWorkflowStateStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color(colorMauve))
	HeaderArtifactIDStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorCyan))
	HeaderLifecycleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorYellow))
	HeaderCapEnabled = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorGreen)).
				Padding(0, 1)
	HeaderCapDisabled = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorDimmed)).
				Padding(0, 1)
	HeaderLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorMuted))
	HeaderBorder = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderTop(false).
			BorderLeft(false).
			BorderRight(false).
			BorderForeground(lipgloss.Color(colorSubtle))

	// Footer
	FooterBudgetLabel = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorMuted))
	FooterBudgetValue = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorText))
	FooterBudgetExhausted = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorRed))
	FooterNotification = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorOrange))
	FooterBorder = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderBottom(false).
			BorderLeft(false).
			BorderRight(false).
			BorderForeground(lipgloss.Color(colorSubtle))

	// Approval prompt styles
	ApprovalBox     = lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(lipgloss.Color(colorOrange)).Padding(0, 1)
	ApprovalTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorYellow))
	ApprovalLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	ApprovalValue   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	ApprovalFile    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan))
	ApprovalCap     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen)).Bold(true)
	ApprovalBudget  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve))
	ApprovalKey     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))
	ApprovalWarning = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	ApprovalInfo    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))

	// Distinction styles
	DistinctionConfirmed  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen)).Bold(true)
	DistinctionHypothesis = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Italic(true)
	DistinctionUnknown    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed)).Bold(true)
	DistinctionErrorClass = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMaroon)).Bold(true)
)

// ── Differential Stream Styling (Thinking vs Content) ───────────────────────
// The streaming content renderer applies these styles per typed block: reasoning
// (KindThinking) is dimmed/faint/italic and subordinate; content (KindContent)
// is bright/crisp as it arrives. They are never applied to the same text.
var (
	// streamThinkingStyle is applied EXCLUSIVELY to KindThinking blocks in the
	// streaming content renderer. Faint + Italic keeps reasoning visually
	// subordinate to the answer; the muted gray foreground guarantees the dim
	// look even on terminals that ignore the Faint SGR attribute (where faint
	// alone would render at full brightness).
	streamThinkingStyle = lipgloss.NewStyle().
				Faint(true).
				Italic(true).
				Foreground(lipgloss.Color(colorMuted))

	// streamThinkingGutter anchors inline thinking lines with a low-contrast
	// gutter so they stay visually aligned with the bright content blocks that
	// follow.
	streamThinkingGutter = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))

	// brightStyle is applied to KindContent blocks as they arrive, making the
	// actual answer/actions read crisp and clear against the dimmed reasoning.
	// It mirrors the standard bright text style (colorText).
	brightStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))

	// streamCursorStyle is the smooth block cursor appended to the active
	// trailing line while an assistant response streams. Accent Blue matches
	// the AI gutter (#89b4fa) so the insertion point reads as part of the
	// answer surface, never as a separate cursor widget.
	streamCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorBlue))

	// streamUncommittedStyle is applied to a structurally-INCOMPLETE streaming
	// line — one whose Markdown block is still arriving (a table row with no
	// closing pipe, a half-received fence, an unterminated `**bold**` span). It
	// is deliberately flat: one colour, no bold, no inline interpretation. A
	// flat line has exactly one possible layout, which is what makes the hold
	// back flicker-free; the line is promoted to the full styled pipeline once,
	// at commit. Muted (not faint) so the dim still reads on terminals that
	// ignore the Faint SGR attribute.
	streamUncommittedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
)

// Pre-compiled Markdown renderer styles (render-path — zero NewStyle).
// All block/inline styles follow the Catppuccin Mocha palette.
var (
	mdEmphasisStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#cba6f7")).Italic(true)
	mdStrongStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	// Headers: bold Mauve (#cba6f7), with top/bottom line space inserted by
	// the renderer (the styles themselves are pure color + weight).
	mdH1Style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	mdH2Style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	mdH3Style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	mdH4Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	// Inline code: Pink text (#f5c2e7) on Surface0 background (#313244).
	mdCodeSpanStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f5c2e7")).Background(lipgloss.Color("#313244"))
	mdLinkStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Underline(true)
	mdMutedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	mdCodeContStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	mdAccentStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#cba6f7"))
	mdSepStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70"))
	mdImageMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	mdHeaderBoldCell  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	mdCellStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	// Lists/numbers: Peach (#fab387) markers with indented continuation text.
	mdBulletStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#fab387"))

	// Code block chrome: thin-line box with dimmed border (#585b70 Surface2)
	// and NO background fill — preserves native terminal background. Syntax
	// colours come from the chroma Catppuccin Mocha theme (green keywords
	// #a6e3a1, blue identifiers #89b4fa).
	mdCodeBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))

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

// Vi-mode styles
var (
	// viCursorStyle: inverted block cursor — mauve background with dark text
	viCursorStyle = lipgloss.NewStyle().
			Background(lipgloss.Color(colorMauve)).
			Foreground(lipgloss.Color(colorBase)).
			Bold(true)
	// viSelectionBgStyle: selection background uses a dark mauve-tinted overlay.
	// Foreground is NOT forced so the underlying styled text color shines through.
	viSelectionBgStyle = lipgloss.NewStyle().Background(lipgloss.Color("#2a2240"))
	viStatusStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve)).Bold(true)
	viCmdStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve))
	viBorderStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve))
)

// ── Interrupt Boundary Spinner ────────────────────────────────────────────
//
// ProposalSpinnerFrames is the canonical smooth braille dot cycle, sourced from
// components.DotsFrames (bubbles' spinner.MiniDot) so the glyph set can never
// drift from the framework's definition. The animation advances at 100ms (10Hz)
// on the independent spinner tick while the 30/60 FPS UI frame loop re-renders
// the same glyph, which is what makes the rotation read as smooth motion rather
// than a stuttering flicker.
//
// READ-ONLY: this aliases the shared frame slice so a framework upgrade
// propagates here. Callers index it modulo its length and must never mutate it.
var ProposalSpinnerFrames = components.DotsFrames

// spinnerGlyph renders animation frame n in the shared emerald ramp: a
// single-hue green→emerald cycle that restores perceived motion at 10Hz without
// ever reading as a state change (no amber/red leaking into a healthy
// "working" indicator).
func spinnerGlyph(frame int) string {
	if frame < 0 {
		frame = 0
	}
	// #nosec G115 -- frame is a non-negative animation index, bounded by callers.
	return components.SpinnerGlyph(uint64(frame))
}

// SpinnerStyle is the base (unramped) spinner style, kept for call sites that
// render a single static frame with no animation context.
var SpinnerStyle = components.SpinnerStyleFor(0)

// ── Gutter / Label Helpers ────────────────────────────────────────────────────

func gutterFor(r role) string {
	switch r {
	case roleUser:
		return gutterUserStyle.Render("│") + " "
	case roleAI:
		return gutterAIStyle.Render("│") + " "
	case roleError:
		return gutterErrorStyle.Render("│") + " "
	case roleStatus:
		return gutterStatusStyle.Render("│") + " "
	case roleSystem:
		return gutterSysStyle.Render("│") + " "
	case roleActivity:
		return "  " // no gutter — activity logs are visually recessive
	default:
		return "  "
	}
}
