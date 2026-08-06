package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/router"
)

// handleRouterResult projects the Hybrid Intent Gateway classification of a
// free-form prompt onto the UI.
//
//   - On error (e.g. no classifier configured) the prompt falls through to the
//     normal chat dispatch in the current mode.
//   - On ConfirmationRequirement (confidence below the policy threshold) the UI
//     freezes in StateAwaitingApproval and renders an interactive mode-selection
//     prompt instead of auto-switching on a blind guess.
//   - Otherwise the classified intent is mapped onto the canonical execution
//     phase: when it differs from the current mode, the orchestrator drives the
//     workflow transition; the prompt is then dispatched to that phase.
//
// This runs on the UI goroutine, so all model mutation is safe.
func (m *model) handleRouterResult(msg routerResultMsg) (tea.Model, tea.Cmd) {
	line := msg.line
	if msg.err != nil || m.intentRouter == nil {
		if msg.err != nil {
			m.push(roleStatus, "intent routing unavailable: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
		}
		return m, m.handleMessageContent(line)
	}

	res := msg.result

	// ── AMBIGUOUS: ask the user which phase to enter ──────────────────
	if res.ConfirmationRequirement {
		options := []modes.Mode{
			modes.ModeAsk,
			modes.ModeInvestigate,
			modes.ModePlan,
			modes.ModeBuild,
			modes.ModeReview,
		}
		idx := 0
		if target, ok := modeForIntent(res.Intent); ok {
			for i, o := range options {
				if o == target {
					idx = i
					break
				}
			}
		}
		m.pendingRouteConfirm = true
		m.pendingRouteInput = line
		m.pendingRouteResult = res
		m.pendingRouteOptions = options
		m.pendingRouteIdx = idx
		m.enterApprovalState()
		m.awaitingConfirmation = true
		m.ti.Blur()
		m.recalcViewportHeight()
		m.push(roleSystem, fmt.Sprintf("Ambiguous request (confidence %.0f%%) — choose a mode below, or press Esc to ask instead.", res.Confidence*100))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil
	}

	// ── CONFIDENT: project the intent onto the execution phase ───────
	target, ok := modeForIntent(res.Intent)
	if ok && target != m.resolver.Current() {
		m.modeChangeAuthorized = true
		m.currentResult = nil
		m.setMode(target)
		m.push(roleStatus, fmt.Sprintf("Intent classified as /%s (%.0f%%, %s)", target, res.Confidence*100, res.Explanation))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
	}
	return m, m.handleMessageContent(line)
}

// modeForIntent maps a router intent onto the canonical UI mode.
func modeForIntent(intent router.Intent) (modes.Mode, bool) {
	switch intent {
	case router.IntentAsk:
		return modes.ModeAsk, true
	case router.IntentInvestigate:
		return modes.ModeInvestigate, true
	case router.IntentPlan:
		return modes.ModePlan, true
	case router.IntentBuild:
		return modes.ModeBuild, true
	case router.IntentReview:
		return modes.ModeReview, true
	default:
		return modes.ModeAsk, false
	}
}

// confirmRouteSelection finalizes the interactive mode-selection prompt by
// dispatching the pending input to the chosen mode (via the orchestrator) and
// clearing the pending confirmation state.
func (m *model) confirmRouteSelection(mode modes.Mode) tea.Cmd {
	line := m.pendingRouteInput
	m.pendingRouteConfirm = false
	m.pendingRouteInput = ""
	m.pendingRouteResult = router.ClassificationResult{}
	m.pendingRouteOptions = nil
	m.pendingRouteIdx = 0
	m.resolveApprovalState()
	m.awaitingConfirmation = false
	m.recalcViewportHeight()
	m.ti.Focus()
	m.refreshViewportContent()
	m.Viewport.GotoBottom()

	if mode != m.resolver.Current() {
		m.modeChangeAuthorized = true
		m.currentResult = nil
		m.setMode(mode)
		m.push(roleStatus, fmt.Sprintf("Proceeding as /%s", mode))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
	}
	return m.handleMessageContent(line)
}

// cancelRouteSelection abandons the mode-selection prompt and treats the input
// as a plain /ask query in the current mode.
func (m *model) cancelRouteSelection() tea.Cmd {
	line := m.pendingRouteInput
	m.pendingRouteConfirm = false
	m.pendingRouteInput = ""
	m.pendingRouteResult = router.ClassificationResult{}
	m.pendingRouteOptions = nil
	m.pendingRouteIdx = 0
	m.resolveApprovalState()
	m.awaitingConfirmation = false
	m.recalcViewportHeight()
	m.ti.Focus()
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m.handleMessageContent(line)
}

// renderRouteConfirmPrompt renders the interactive mode-selection dock shown
// when the Hybrid Intent Gateway cannot commit to a single execution phase.
func (m *model) renderRouteConfirmPrompt(width int) string {
	var b strings.Builder

	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}
	title := permissionTitleStyle.Render("◈ CLARIFY INTENT")
	desc := permissionDescStyle.Render(
		fmt.Sprintf("Confidence %.0f%% — which phase should this prompt enter?", m.pendingRouteResult.Confidence*100))

	b.WriteString(title + "\n")
	b.WriteString(desc + "\n\n")

	for i, opt := range m.pendingRouteOptions {
		marker := "  "
		num := " "
		if i == m.pendingRouteIdx {
			marker = Icon.Chevron + " "
			num = fmt.Sprintf("%d", i+1)
		}
		label := opt.String()
		line := fmt.Sprintf("%s[%s] /%s", marker, num, label)
		if i == m.pendingRouteIdx {
			line = permissionKeyStyle.Render(line)
		} else {
			line = permissionDescStyle.Render(line)
		}
		b.WriteString("  " + line + "\n")
	}

	b.WriteString("\n  " + dimmedStyle.Render(strings.Repeat("─", boxWidth-8)) + "\n")
	keys := fmt.Sprintf("%s  %s  %s",
		permissionKeyStyle.Render("1-5 Select"),
		permissionKeyStyle.Render("←/→ Navigate"),
		permissionKeyStyle.Render("Enter Confirm / Esc Ask"),
	)
	b.WriteString("  " + keys + "\n")

	return permissionBoxStyle.Render(b.String())
}
