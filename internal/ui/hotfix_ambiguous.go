package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes/plan"
)

// clarifyHotfixTarget resolves the ambiguity by returning focus to the build
// input so the developer can supply the missing target/change. The ambiguous
// card is dismissed; no provider call and no mutation occur.
func (m *model) clarifyHotfixTarget() (tea.Model, tea.Cmd) {
	m.pendingHotfixAmbiguous = nil
	m.hotfixCandidatesMode = false
	m.syncUIState()
	m.ti.Focus()
	m.recalcViewportHeight()
	m.push(roleSystem, infoStyle.Render(
		"  Provide the missing target/change, e.g. \"$hot Remove extra text from the Contact heading in @index.html\"."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m, nil
}

// cancelHotfixAmbiguous aborts the ambiguous hotfix cleanly, restoring the
// stashed plan and returning to interactive chat with zero mutation.
func (m *model) cancelHotfixAmbiguous() (tea.Model, tea.Cmd) {
	amb := m.pendingHotfixAmbiguous
	m.pendingHotfixAmbiguous = nil
	m.hotfixCandidatesMode = false
	m.hotfixActive = false
	// The ambiguity card is a terminal outcome of the hotfix operation; a
	// cancel releases any residual operation ownership through the single
	// finalization path.
	m.finalizeOperation(OpOutcomeCancelled, nil)
	if stashedTasks, rerr := m.restorePlan(); rerr == nil && len(stashedTasks) > 0 {
		m.sess.StageTaskList(&stashedTasks)
		_ = m.sess.Save()
	}
	m.resolveApprovalState()
	m.syncUIState()
	m.ti.Focus()
	m.recalcViewportHeight()
	m.push(roleSystem, infoStyle.Render(
		"  "+Icon.Error+" Cancelled — ambiguous hotfix aborted. No files were modified."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	if amb != nil && amb.Task != nil {
		return m, m.runtimeRejectCmd(amb.Task.Target, "ambiguous hotfix cancelled")
	}
	return m, nil
}

// submitHotfixCardCommand submits the command currently composed in the input
// buffer while the ambiguity card renders. It dismisses the card (the
// ambiguity was a terminal outcome of the previous operation) and hands off to
// the normal Enter submission pipeline, which starts a NEW operation when the
// command actually executes — human interaction never resumes the old worker.
func (m *model) submitHotfixCardCommand() (tea.Model, tea.Cmd) {
	m.pendingHotfixAmbiguous = nil
	m.hotfixCandidatesMode = false
	m.hotfixActive = false
	m.syncUIState()
	return m.submitEnter()
}

// toggleHotfixCandidates toggles the read-only candidate-inspection sub-view.
// Inspecting candidates NEVER mutates the file and NEVER selects a candidate —
// the human chooses explicitly, or the card returns to Clarify/Cancel.
//
// FOCUS CONTRACT: candidate selection ([1-9]) is an explicit modal
// interaction, so entering the sub-view blurs the text input and the number
// keys become modal. Leaving it restores focus so the next command can be
// typed immediately.
func (m *model) toggleHotfixCandidates() (tea.Model, tea.Cmd) {
	m.hotfixCandidatesMode = !m.hotfixCandidatesMode
	if m.hotfixCandidatesMode {
		m.ti.Blur()
	} else {
		m.ti.Focus()
	}
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m, nil
}

// selectHotfixCandidate promotes a HUMAN-SELECTED candidate to the explicit
// mutation target and runs the normal bounded mutation pipeline for it. The
// candidate's block is the deterministic target — it is never re-resolved and
// the first candidate is never auto-selected.
func (m *model) selectHotfixCandidate(n int) (tea.Model, tea.Cmd) {
	amb := m.pendingHotfixAmbiguous
	if amb == nil || n < 1 || n > len(amb.Candidates) {
		return m, nil
	}
	cand := amb.Candidates[n-1]
	m.pendingHotfixAmbiguous = nil
	m.hotfixCandidatesMode = false
	m.syncUIState()

	synth := &plan.Task{
		StepNum:     0,
		Status:      "idle",
		Type:        "FILE_MUTATE",
		Target:      amb.Task.Target,
		Description: "Fix the HTML structure: " + cand.Mismatch.Describe(),
	}
	// The selected candidate becomes the explicit target. It is read by the
	// proposeHotfixPatch closure (dispatched below) and cleared when the
	// terminal proposal/ambiguity message reaches the event loop.
	m.pendingHotfixCandidate = &cand
	// OPERATION LIFECYCLE: human candidate selection resumes actual execution
	// as a NEW operation (op B). The previous operation already reached its
	// terminal AMBIGUOUS outcome — no worker is resumed, a fresh one starts.
	m.beginOperation(OpHotfix)
	m.hotfixActive = true
	m.agentRunning = true
	m.agentDone = false
	m.agentLabel = "hotfix"
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	m.lastAgentActivity = time.Now()
	m.startShimmer("Applying hotfix...", "execute")
	m.push(roleStatus, fmt.Sprintf("[HOTFIX] Target set to candidate %d — generating patch for %s", n, amb.Task.Target))
	m.push(roleSystem, fmt.Sprintf("  ⚙ Invoking %s...", m.activeRouteModel()))

	return m, tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "hotfix"} },
		m.proposeHotfixPatch(synth),
		m.smoothStreamTickCmd(),
		m.shimmerTickCmd(),
		m.opWatchdogCmd(),
	)
}

// renderHotfixAmbiguousBlock renders the actionable ambiguity-resolution card.
// It is NOT a patch proposal: it NEVER renders Accept/Reject actions (there is
// no patch), and candidate inspection is strictly read-only.
func (m *model) renderHotfixAmbiguousBlock(width int) string {
	amb := m.pendingHotfixAmbiguous
	if amb == nil {
		return ""
	}
	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	var b strings.Builder
	b.WriteString(permissionTitleStyle.Render(Icon.Warning + " HOTFIX TARGET AMBIGUOUS"))
	b.WriteString("\n\n")
	if amb.Task != nil {
		b.WriteString(permissionDescStyle.Render("Request:"))
		b.WriteString(" " + permissionTargetStyle.Render(amb.Task.Description))
		b.WriteString("\n")
		b.WriteString(permissionDescStyle.Render("Target:"))
		b.WriteString(" " + permissionTargetStyle.Render(amb.Task.Target))
		b.WriteString("\n")
	}
	b.WriteString(permissionDescStyle.Render("Reason:"))
	b.WriteString(" " + infoStyle.Render(amb.Reason))
	b.WriteString("\n")

	// Candidate inspection (read-only): never mutates, never auto-selects.
	if m.hotfixCandidatesMode {
		b.WriteString("\n")
		b.WriteString(permissionTitleStyle.Render("Candidate targets (inspection only)"))
		b.WriteString("\n")
		if len(amb.Candidates) == 0 {
			b.WriteString("  " + mutedStyle.Render("No deterministic candidates were found for this file."))
			b.WriteString("\n")
		} else {
			for i, cand := range amb.Candidates {
				fmt.Fprintf(&b, "  %s %s\n",
					permissionKeyStyle.Render(fmt.Sprintf("[%d]", i+1)),
					tracerStyle.Render(cand.Mismatch.Describe()))
				lines := strings.Split(cand.Block, "\n")
				preview := cand.Block
				if len(lines) > 3 {
					preview = strings.Join(lines[:3], "\n") + "\n  …"
				}
				for _, pl := range strings.Split(preview, "\n") {
					b.WriteString(mutedStyle.Render("     " + pl))
					b.WriteString("\n")
				}
			}
		}
	}

	sep := strings.Repeat("─", boxWidth-4)
	b.WriteString(" " + sep + "\n")
	if m.hotfixCandidatesMode && len(amb.Candidates) > 0 {
		fmt.Fprintf(&b, "%s  %s  %s",
			permissionKeyStyle.Render("[1-9] Select"),
			permissionKeyStyle.Render("[⌥C] Clarify"),
			permissionKeyStyle.Render("[⌥X] Cancel"),
		)
		b.WriteString("\n")
	} else {
		keys := permissionKeyStyle.Render("[⌥C] Clarify target")
		if len(amb.Candidates) > 0 {
			keys += "  " + permissionKeyStyle.Render("[⌥I] Inspect candidates")
		}
		keys += "  " + permissionKeyStyle.Render("[⌥X] Cancel")
		b.WriteString(keys)
		b.WriteString("\n")
	}
	return permissionBoxStyle.Width(boxWidth).Render(b.String())
}
