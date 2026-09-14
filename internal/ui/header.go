package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/modes"
)

// Fixed header styles
var (
	headerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderTop(false).
				BorderLeft(false).
				BorderRight(false).
				BorderForeground(lipgloss.Color(colorSubtle))
	workflowStateStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorMauve))
	indexingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorYellow)).
			Bold(true)
	indexedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGreen)).
			Bold(true)
	// executingHeaderTitleStyle renders the execution title in deep forest
	// emerald (Catppuccin mint green) so the active state reads instantly.
	executingHeaderTitleStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color(colorGreen))
	// executingShimmerStyle renders the windowed sweep glyphs in teal
	// (Tokyo Night emerald). A single style per frame keeps repaint
	// low-overhead: no per-rune interpolation, one SGR run per render.
	executingShimmerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorTeal))
)

// ExecutingShimmerFrames is the windowed ANSI gradient sweep moving
// right-to-left across the Top Header during execution. The window is 4
// cells wide; advancing one frame per tea.Tick (80–100ms) yields a smooth
// sweep at ~10fps with a single styled span per frame (<0.5% CPU overhead —
// no per-rune color math, no layout recompute).
var ExecutingShimmerFrames = []string{
	"█▓▒░", "▓▒░ ", "▒░  ", "░   ", "   ░", "  ░▒", " ░▒▓", "░▒▓█",
}

// ExecutingTickInterval is the capped repaint cadence for the header sweep.
// 90ms sits inside the 80–100ms target band and matches the existing shimmer
// tick so all animation loops share one cadence (no extra timer, <0.5% CPU).
const ExecutingTickInterval = 90 * time.Millisecond

// RenderExecutingHeader renders the Top Header execution state as a single
// fixed line: "● <TITLE> <sweep>". Title is upper-cased and truncated to fit;
// tick selects the sweep frame (advance on every tea.Tick at 80–100ms).
// Width < 20 returns "" (never panics); narrow widths (<80) truncate the
// title first and never drop the sweep window.
func RenderExecutingHeader(title string, tick int, width int) string {
	if width < 20 {
		return ""
	}
	n := len(ExecutingShimmerFrames)
	if n == 0 {
		return ""
	}
	idx := tick % n
	if idx < 0 {
		idx += n
	}
	frame := ExecutingShimmerFrames[idx]
	t := title
	if t == "" {
		t = "EXECUTING"
	}
	// Upper-case ASCII fast path (titles are short status labels).
	upper := make([]rune, 0, len([]rune(t)))
	for _, r := range t {
		if r >= 'a' && r <= 'z' {
			r -= 32
		}
		upper = append(upper, r)
	}
	t = string(upper)
	left := executingHeaderTitleStyle.Render("● " + t)
	sweep := executingShimmerStyle.Render(frame)
	// Budget: left + space + sweep must fit; truncate title text first.
	// lipgloss.Width is cell-aware; fall back to plain truncation on narrow.
	content := left + " " + sweep
	if lipgloss.Width(content) > width {
		// Shrink the title runes to fit, keeping "● " + sweep always visible.
		reserve := lipgloss.Width("●  "+frame) + 2
		budget := width - reserve
		if budget < 1 {
			return headerBorderStyle.Width(width).Render(sweep)
		}
		rs := []rune(t)
		if len(rs) > budget {
			t = string(rs[:budget])
		}
		left = executingHeaderTitleStyle.Render("● " + t)
	}
	return headerBorderStyle.Width(width).Render(padRightOverlay(left, sweep, width))
}

// renderFixedHeader renders the anchored top bar as a single compact,
// high-density line with exactly two regions:
//
//	Left:  ● <WORKFLOW_STATE>  ● Indexed   (state + indexing status)
//	Right: Toast Overlay "[✓ <msg>]" when active, otherwise the Mode Badge
//	       ("[READ-ONLY]" / "[WRITE]" / "[EXECUTE]").
//
// The static R W X T P C B capability flags are gone — the Mode Badge is the
// sole right-side indicator. A transient toast (toast != "") owns the far-right
// boundary for its window, so the bar never grows a row for notifications. See
// internal/ui/topbar.go for the toast lifecycle.
func renderFixedHeader(runtimeCtx *runtime.RuntimeContext, wfSM *workflow.WorkflowStateMachine, mode modes.Mode, width int, indexingStatus string, toast string) string {
	if runtimeCtx == nil || wfSM == nil || width < 20 {
		return ""
	}

	ws := wfSM.State()

	var b strings.Builder

	// ── Left region: workflow state + indexing status ──────────────
	b.WriteString(workflowStateStyle.Render(Icon.Check + " " + strings.ToUpper(ws.String())))

	switch indexingStatus {
	case "indexing":
		b.WriteString("  ")
		b.WriteString(indexingStyle.Render(Icon.Index + " Indexing..."))
	case "indexed":
		b.WriteString("  ")
		b.WriteString(indexedStyle.Render(Icon.Success + " Indexed"))
	case "error":
		b.WriteString("  ")
		b.WriteString(indexingStyle.Render(Icon.Error + " Index error"))
	}

	// ── Right region: toast overlay (active) or mode badge (idle) ──
	effectiveMode := mode
	switch ws {
	case workflow.StateBuilding:
		effectiveMode = modes.ModeBuild
	case workflow.StateInvestigating:
		effectiveMode = modes.ModeInvestigate
	case workflow.StatePlanning:
		effectiveMode = modes.ModePlan
	case workflow.StateReviewing:
		effectiveMode = modes.ModeReview
	}

	right := toast
	if right == "" {
		right = renderModeBadge(effectiveMode)
	}
	if right == "" {
		return headerBorderStyle.Width(width).Render(b.String())
	}
	return headerBorderStyle.Width(width).Render(padRightOverlay(b.String(), right, width))
}
