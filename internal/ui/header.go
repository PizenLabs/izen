package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
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
	capBadgeEnabledStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorGreen)).
				Padding(0, 1)
	capBadgeDisabledStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorDimmed)).
				Padding(0, 1)
	indexingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorYellow)).
			Bold(true)
	indexedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGreen)).
			Bold(true)
)

// renderFixedHeader renders the anchored top bar: WorkflowState badge,
// indexing status Indicator, and CapabilitySet flags in a single
// compact, high-density line. This maximizes scrollable viewport space
// by eliminating redundant mode/context info.
func renderFixedHeader(runtimeCtx *runtime.RuntimeContext, wfSM *workflow.WorkflowStateMachine, width int, indexingStatus string) string {
	if runtimeCtx == nil || wfSM == nil || width < 20 {
		return ""
	}

	ws := wfSM.State()

	var b strings.Builder

	b.WriteString(workflowStateStyle.Render(Icon.Check + " " + strings.ToUpper(ws.String())))

	// Indexing status indicator
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

	if runtimeCtx.Caps != nil {
		b.WriteString("  ")
		b.WriteString(renderCapabilities(runtimeCtx.Caps))
	}

	return headerBorderStyle.Width(width).Render(b.String())
}

// renderCapabilities renders the enabled capability badges inline.
func renderCapabilities(caps *capability.CapabilitySet) string {
	type capDef struct {
		key  capability.Capability
		icon string
	}
	all := []capDef{
		{capability.CapabilityRead, "R"},
		{capability.CapabilityWrite, "W"},
		{capability.CapabilityExecute, "X"},
		{capability.CapabilityTest, "T"},
		{capability.CapabilityPatch, "P"},
		{capability.CapabilityCheckpoint, "C"},
		{capability.CapabilityRollback, "B"},
	}
	var badges []string
	for _, c := range all {
		if caps.Has(c.key) {
			badges = append(badges, capBadgeEnabledStyle.Render(c.icon))
		} else {
			badges = append(badges, capBadgeDisabledStyle.Render(c.icon))
		}
	}
	return strings.Join(badges, " ")
}
