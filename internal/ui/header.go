package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
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
	artifactIDStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorCyan))
	lifecycleStateStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorYellow))
	capBadgeEnabledStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorGreen)).
				Padding(0, 1)
	capBadgeDisabledStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorDimmed)).
				Padding(0, 1)
	modeTagStyle = lipgloss.NewStyle().
			Bold(true).
			Padding(0, 1).
			Background(lipgloss.Color(colorOverlay))
)

// renderFixedHeader renders the anchored top bar: WorkflowState, target
// context info, and active CapabilitySet flags. The header reads directly
// from RuntimeContext, WorkflowStateMachine, and Session — it never stores
// or caches its own copy of these values.
func renderFixedHeader(runtimeCtx *runtime.RuntimeContext, wfSM *workflow.WorkflowStateMachine, sess *session.Session, width int, mode modes.Mode) string {
	if runtimeCtx == nil || wfSM == nil || width < 20 {
		return ""
	}

	ws := wfSM.State()
	modeColor := modeAccentColor(mode)

	var b strings.Builder

	// ── Row 1: WorkflowState + Mode ────────────────────────────────────
	b.WriteString(workflowStateStyle.Render("● " + strings.ToUpper(ws.String())))
	b.WriteString("  ")
	modeTag := modeTagStyle.Foreground(modeColor)
	b.WriteString(modeTag.Render(mode.String()))

	// Target/Context info from session
	if sess != nil && sess.ContextLedger != nil {
		if sess.ContextLedger.TargetFile != "" {
			b.WriteString("  ")
			b.WriteString(artifactIDStyle.Render(sess.ContextLedger.TargetFile))
		}
		lifecycleStr := activeLifecycleDisplay(sess)
		if lifecycleStr != "" {
			b.WriteString(" ")
			b.WriteString(lifecycleStateStyle.Render(fmt.Sprintf("[%s]", lifecycleStr)))
		}
	}

	b.WriteByte('\n')

	// ── Row 2: CapabilitySet flags ────────────────────────────────────
	if runtimeCtx.Caps != nil {
		b.WriteString(renderCapabilities(runtimeCtx.Caps))
	}

	return headerBorderStyle.Width(width).Render(strings.TrimSuffix(b.String(), "\n"))
}

// activeLifecycleDisplay derives a display string from the session state.
func activeLifecycleDisplay(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	if sess.ContextLedger != nil && len(sess.ContextLedger.TaskStatus) > 0 {
		for _, status := range sess.ContextLedger.TaskStatus {
			if status == "failed" {
				return "FAILED"
			}
		}
		return "AUTHORIZED"
	}
	if sess.ContextLedger != nil && sess.ContextLedger.Diagnostics != "" {
		return "INVESTIGATING"
	}
	return ""
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
