package ui

// ── Design Tokens ─────────────────────────────────────────────────────────────
//
// Design tokens define the shared visual language for the entire UI.
//
// They provide a single source of truth for colors, typography, spacing,
// borders, and icons so every renderer projects the same visual identity
// across all interaction modes (/ask, /plan, /build, /investigate, /review).
//
// Color values reference the Catppuccin Mocha palette defined in styles.go to
// ensure consistency and eliminate duplicated styling decisions.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Color defines the semantic color palette.
//
// Colors communicate meaning rather than appearance:
//
//   - Success: completed operations and positive outcomes
//   - Warning: attention, validation, or pending approval
//   - Danger: failures, rejected actions, and destructive changes
//   - Info: informational content and section headers
//   - Muted: secondary metadata and supporting information
var Color = struct {
	Success string
	Warning string
	Danger  string
	Info    string
	Accent  string
	Muted   string
	Dimmed  string
	Text    string
	Subtle  string
	Surface string
	Overlay string
	Pending string
	Reject  string
}{
	Success: colorGreen,
	Warning: colorYellow,
	Danger:  colorRed,
	Info:    colorBlue,
	Accent:  colorAccent,
	Muted:   colorMuted,
	Dimmed:  colorDimmed,
	Text:    colorText,
	Subtle:  colorSubtle,
	Surface: colorSurface,
	Overlay: colorOverlay,
	Pending: colorOrange,
	Reject:  colorMaroon,
}

// Text defines reusable text styles ordered by visual priority.
var Text = struct {
	Primary   lipgloss.Style
	Secondary lipgloss.Style
	Muted     lipgloss.Style
	Faint     lipgloss.Style
}{
	Primary:   textStyle,
	Secondary: infoStyle,
	Muted:     mutedStyle,
	Faint:     dimmedStyle,
}

// Spacing defines the vertical rhythm shared across the UI.
//
// Whitespace separates ideas and establishes hierarchy more effectively than
// decorative elements, making spacing a first-class design token.
var Spacing = struct {
	Small   int
	Medium  int
	Large   int
	Section int
}{
	Small:   1,
	Medium:  1,
	Large:   2,
	Section: 2,
}

// Border defines reusable border and separator styles.
var Border = struct {
	Subtle lipgloss.Style
}{
	Subtle: subtleStyle,
}

// Icon defines the canonical glyph set shared across the UI.
//
// Icons communicate semantics rather than literal objects.
//
//   - Circle   : state (success, pending, review)
//   - Triangle : warning or risk
//   - Diamond  : action or metadata
//   - Square   : artifacts
//   - Arrow    : execution and navigation
//
// Unicode glyphs form the primary visual language. Nerd Font icons are
// reserved for developer tooling (grep, read, diff, blueprint, verification)
// to maintain a clean, professional terminal experience.
var Icon = struct {
	Command   string
	File      string
	Diff      string
	Task      string
	Warning   string
	Review    string
	Execute   string
	Evidence  string
	Action    string
	Success   string
	Error     string
	Info      string
	Plan      string
	Edit      string
	Table     string
	Summary   string
	Risk      string
	Context   string
	Chevron   string
	Bullet    string
	Check     string
	Cross     string
	Pending   string
	Spark     string
	ShellExec string
	Config    string
	SrcPatch  string
	Done      string
	Blueprint string
	Timeline  string
	EnvDeps   string
	CodeMod   string
	Verify    string

	// Interrupt indicates that the current operation can be cancelled.
	Interrupt string

	// Index identifies workspace indexing and repository analysis.
	Index string

	// Tool activity stream icons.
	//
	// These glyphs prefix live execution steps in the activity tree. Developer
	// tooling uses Nerd Font icons, while execution uses the shared animated
	// snowflake defined by SpinnerSnowflakeFrames.
	Grep string
	Read string
	Exec string
}{
	Command: "❯",

	// Artifacts
	File:    "□",
	Table:   "⊞",
	Context: "◎",

	// Flow
	Chevron:   "▸",
	Execute:   "▶",
	ShellExec: "▶",

	// Planning
	Plan:      "◫",
	Blueprint: "\U000F0313",
	Timeline:  "\U000F0316",

	// Actions
	Action:  "◆",
	Edit:    "◇",
	CodeMod: "\U000F061E",

	// States
	Task:    "●",
	Success: "●",
	Check:   "●",
	Done:    "●",
	Pending: "○",

	Error: "✕",
	Cross: "✕",

	// Inspection
	Review:   "◉",
	Evidence: "◉",

	// Alerts
	Warning: "▲",
	Risk:    "▲",

	// Misc
	Info:    "•",
	Bullet:  "•",
	Summary: "»",
	Spark:   "✦",

	Config:   "◈",
	SrcPatch: "\U000F03EB",
	Verify:   "\U000F0668",
	EnvDeps:  "\U000F03D7",

	Interrupt: "⏸",
	Index:     "◈",

	// Tooling
	Grep: "\U000F0349", // nf-cod-search
	Read: "\U000F0219", // nf-cod-file
	Diff: "\U000F03EB", // nf-cod-diff
	Exec: "✻",          // animated via SpinnerSnowflakeFrames
}

// SpinnerSnowflakeFrames defines the canonical loading animation.
//
// Every animated loading indicator should reuse these frames to preserve a
// consistent motion language across the interface.
var SpinnerSnowflakeFrames = []string{"✻", "❅", "❆", "✦"}

// SpinnerSnowflake returns the default resting frame of the shared loading
// animation.
func SpinnerSnowflake() string {
	return SpinnerSnowflakeFrames[0]
}

// IconGrep returns the glyph used for search operations.
func IconGrep() string { return Icon.Grep }

// IconRead returns the glyph used for file read operations.
func IconRead() string { return Icon.Read }

// IconExec returns the glyph used for shell execution.
func IconExec() string { return Icon.Exec }

// IconCheck returns the glyph representing a completed state.
func IconCheck() string { return Icon.Check }

// IconError returns the glyph representing a failed state.
func IconError() string { return Icon.Error }

// IconPending returns the glyph representing a pending state.
func IconPending() string { return Icon.Pending }

// rule renders a full-width horizontal separator.
func rule(width int, style lipgloss.Style) string {
	if width < 1 {
		width = 1
	}
	return style.Render(strings.Repeat("─", width))
}

// vspace returns n blank lines used to apply the shared spacing rhythm.
func vspace(n int) string {
	if n < 0 {
		n = 0
	}
	return strings.Repeat("\n", n)
}
