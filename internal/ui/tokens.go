package ui

// ── Design Tokens ─────────────────────────────────────────────────────────────
//
// Single source of truth for colors, text priorities, spacing rhythm, icons,
// and borders. Every renderer consumes these tokens instead of hardcoding
// literals so the visual language stays consistent across all modes
// (/ask, /plan, /build, /investigate, /review).
//
// Palette values are Catppuccin Mocha hex strings defined in styles.go and are
// referenced here by their existing constants to avoid divergence.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Color holds the semantic palette. Each field communicates *meaning*, never
// pure decoration:
//   - Success : completed actions, added code, success states
//   - Warning : pending approval, validation, warnings
//   - Danger  : failures, removed code, rejected actions
//   - Info    : section titles, workspace labels, informational elements
//   - Muted   : metadata, timestamps, token usage, checkpoint IDs
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

// Text holds reusable text styles ordered by visual priority.
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

// Spacing defines the vertical rhythm (in blank lines) used to separate
// ideas rather than simply occupy space. Whitespace performs most of the
// structural work; these constants keep that rhythm uniform.
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

// Border holds reusable border/rule tokens.
var Border = struct {
	Subtle lipgloss.Style
}{
	Subtle: subtleStyle,
}

// Icon holds quiet, monochrome Unicode/Nerd-Font glyphs (no emoji). Icons
// help users scan content faster and stay visually consistent across modes.
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
	// Interrupt is the stop-sign glyph used by the interrupt hint next to the
	// runtime status spinner while a stream or shell command is active.
	Interrupt string
	// Index is the lightning-bolt glyph used by the header indexing badge.
	Index string
	// ── Tool activity stream icons (Claude Code / OpenCode style) ──
	// Nerd-Font cod glyphs used to prefix live tool steps so the tree reads
	// as a transparent execution log: grep/read/diff/exec each carry a
	// dedicated glyph (Icon.Diff is repurposed from the unused "⇄" to the
	// cod-diff glyph). The exec icon is the animated snowflake ✻ while a
	// shell command is running (see SpinnerSnowflakeFrames).
	Grep string
	Read string
	Exec string
}{
	Command:   "❯",
	File:      "▦",
	Diff:      "\U000F03EB", // nf-cod-diff — patch/diff tool step
	Task:      "✓",
	Warning:   "▲",
	Review:    "◎",
	Execute:   "▶",
	Evidence:  "◉",
	Action:    "❖",
	Success:   "✔",
	Error:     "✘",
	Info:      "ℹ",
	Plan:      "▤",
	Edit:      "✎",
	Table:     "⊞",
	Summary:   "»",
	Risk:      "◆",
	Context:   "⊚",
	Chevron:   "▸",
	Bullet:    "•",
	Check:     "●",
	Cross:     "✗",
	Pending:   "◌",
	Spark:     "✦",
	ShellExec: "▶",
	Config:    "⚙",
	SrcPatch:  "⚙",
	Done:      "✔",
	Blueprint: "\U000F0313",
	Timeline:  "\U000F0316",
	EnvDeps:   "\U000F03D7",
	CodeMod:   "\U000F061E",
	Verify:    "\U000F0668",
	Interrupt: "⏹",
	Index:     "⚡",
	Grep:      "\U000F0349", // nf-cod-search — grep tool step
	Read:      "\U000F0219", // nf-cod-file — read tool step
	Exec:      "✻",          // snowflake — shell exec step (animated while running)
}

// SpinnerSnowflakeFrames is the canonical animated snowflake sequence
// (✻ ❅ ❆ ✦) shared by the inline loading spinner, the exec tree node, and the
// loading dock. Every spinner in the UI must cycle these frames so the glyph
// set stays identical across all modes.
var SpinnerSnowflakeFrames = []string{"✻", "❅", "❆", "✦"}

// SpinnerSnowflake returns the resting snowflake glyph (the first frame of
// SpinnerSnowflakeFrames). Used whenever a static (non-animated) loading or
// exec indicator is needed.
func SpinnerSnowflake() string {
	return SpinnerSnowflakeFrames[0]
}

// IconGrep returns the Nerd-Font cod-search glyph used to prefix grep tool
// steps in the activity tree.
func IconGrep() string { return Icon.Grep }

// IconRead returns the Nerd-Font cod-file glyph used to prefix read tool
// steps in the activity tree.
func IconRead() string { return Icon.Read }

// IconExec returns the snowflake glyph used to prefix shell-exec tool steps
// in the activity tree (animated while the command is running).
func IconExec() string { return Icon.Exec }

// rule returns a full-width horizontal separator rendered in the given style.
// Used for region boundaries that must reflow deterministically on resize.
func rule(width int, style lipgloss.Style) string {
	if width < 1 {
		width = 1
	}
	return style.Render(strings.Repeat("─", width))
}

// vspace returns n blank lines as a string, used to apply the Spacing rhythm.
func vspace(n int) string {
	if n < 0 {
		n = 0
	}
	return strings.Repeat("\n", n)
}
