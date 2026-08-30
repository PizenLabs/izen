package ui

import (
	"strings"

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
)

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
	right := toast
	if right == "" {
		right = renderModeBadge(mode)
	}
	if right == "" {
		return headerBorderStyle.Width(width).Render(b.String())
	}
	return headerBorderStyle.Width(width).Render(padRightOverlay(b.String(), right, width))
}
