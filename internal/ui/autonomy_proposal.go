package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// ── AUTONOMY PROPOSAL ────────────────────────────────────────────────────
//
// The proposal is the ONLY user-facing decision surface for a DecisionAskUser
// verdict. It replaces the /grant command: the human never types a grant
// command — they select Execute on the proposal, and the runtime issues the
// capability grant internally, re-runs the decision and continues execution.
//
//   - Execute → internal grant → revalidate decision → execute
//   - Inspect  → expand the full decision/evidence detail (read-only)
//   - Cancel   → abandon the objective; no grant, no execution

// proposalActions is the ordered action list the proposal navigates.
var proposalActions = []autonomy.ProposalAction{
	autonomy.ActionExecute,
	autonomy.ActionInspect,
	autonomy.ActionCancel,
}

// requestAutonomyProposal stages the ask_user decision surface: the runtime
// presents the intent/workspace/risk/capability facts and the planned actions,
// and waits for one explicit human decision. It never prints "Approve with
// /grant" — granting is an internal authorization operation.
func (m *model) requestAutonomyProposal(trace autonomy.Trace) tea.Cmd {
	m.pendingAutonomyProposal = trace.Proposal()
	m.autonomyProposalSelect = 0
	m.autonomyProposalInspect = false
	m.enterApprovalState()

	var b strings.Builder
	b.WriteString(boldSapphireStyle.Render(Icon.Blueprint+" AUTONOMY PROPOSAL") + "\n")
	fmt.Fprintf(&b, "  intent      : %s\n", trace.Intent.Intent)
	if len(trace.Intent.Targets) > 0 {
		fmt.Fprintf(&b, "  targets     : %s\n", strings.Join(trace.Intent.Targets, ", "))
	}
	fmt.Fprintf(&b, "  workspace   : %s\n", trace.Route.Workspace)
	b.WriteString("\n  " + infoStyle.Render("Select Execute to authorize, Inspect to review details, or Cancel. Esc cancels.") + "\n")
	m.push(roleStatus, b.String())
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

// executeAutonomyProposal authorizes the pending proposal: it issues the
// session-bound capability grant internally, consumes the proposal, re-runs
// the autonomy decision on the SAME input (no command parser, no re-submitted
// prompt), and continues execution inside the now-granted boundary.
//
// Two resolution shapes exist:
//
//   - Capability authorization (Missing non-empty): the grant is issued, the
//     decision is re-run, and execution continues (auto_continue). If the
//     re-run surfaces a further gate (risk/scope confirmation), the new
//     proposal is rendered.
//   - Confirmation gate (Missing empty): the human's Execute IS the
//     acknowledgement (risk / target / scope). The controller already
//     authorized the capability boundary; the runtime executes the decided
//     workspace directly — it never re-enters the same confirmation gate.
func (m *model) executeAutonomyProposal() tea.Cmd {
	prop := m.pendingAutonomyProposal
	if prop == nil {
		return nil
	}
	m.pendingAutonomyProposal = nil
	m.autonomyProposalInspect = false
	m.resolveApprovalState()

	// ── Capability authorization: grant internally, revalidate, continue ──
	if len(prop.Missing) > 0 {
		g := m.autonomy.GrantDefault(prop.Missing...)
		m.push(roleStatus, fmt.Sprintf(
			"%s Capability granted: %s\n  scope: %s\n%s",
			greenStyle.Render("✓"), strings.Join(capNames(g.Capabilities), " + "), g.Scope,
			mutedStyle.Render("The runtime may now inspect, plan, patch and verify inside this boundary without asking again."),
		))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()

		// Re-run/revalidate the decision on the original objective. The grant
		// now covers the required capabilities, so the controller returns
		// auto_continue and execution proceeds without another approval.
		trace := m.autonomy.Decide(prop.Input)
		if trace.Decision.Decision == autonomy.DecisionAskUser {
			// A further policy gate (risk/scope/target confirmation) remains.
			return m.requestAutonomyProposal(trace)
		}
		return m.dispatchAutonomyTrace(trace)
	}

	// ── Confirmation gate: Execute is the acknowledgement ─────────────
	// Risk acknowledgement, target confirmation, and scope confirmation have
	// no capability to grant. The human's Execute resolves the gate; the
	// decided workspace executes directly.
	m.push(roleStatus, fmt.Sprintf(
		"%s Acknowledged — proceeding as %s",
		greenStyle.Render("✓"), prop.Workspace,
	))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m.executeAutonomyWorkspace(traceFromProposal(prop))
}

// traceFromProposal reconstructs the decision trace from a consumed proposal
// so the decided workspace can execute directly after a confirmation gate. It
// is a pure projection of the proposal facts — it re-classifies nothing.
func traceFromProposal(prop *autonomy.Proposal) autonomy.Trace {
	res := autonomy.IntentResult{
		Intent:   prop.Intent,
		Required: prop.Required,
	}
	if prop.Target != "" {
		res.Targets = []string{prop.Target}
	}
	return autonomy.Trace{
		Input:     prop.Input,
		Intent:    res,
		Route:     autonomy.WorkspaceRoute{Workspace: prop.Workspace, Covers: true},
		Risk:      prop.Risk,
		ScopeSize: prop.AffectedScope,
		Rollback:  prop.Rollback,
	}
}

// cancelAutonomyProposal abandons the pending objective: no grant is issued,
// no execution begins. The decision trace remains observable.
func (m *model) cancelAutonomyProposal() tea.Cmd {
	if m.pendingAutonomyProposal == nil {
		return nil
	}
	prop := m.pendingAutonomyProposal
	m.pendingAutonomyProposal = nil
	m.autonomyProposalInspect = false
	m.resolveApprovalState()
	m.push(roleSystem, infoStyle.Render("[autonomy] proposal cancelled — no capability granted, no execution started."))
	if prop.Intent.RequiresMutation() {
		m.push(roleSystem, mutedStyle.Render("  Objective abandoned: "+truncateDisplay(prop.Input, 90)))
	}
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

// toggleAutonomyProposalInspect toggles the read-only detail view of the
// pending proposal. Inspecting never grants or executes anything.
func (m *model) toggleAutonomyProposalInspect() {
	if m.pendingAutonomyProposal == nil {
		return
	}
	m.autonomyProposalInspect = !m.autonomyProposalInspect
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
}

// navigateAutonomyProposal moves the action highlight. delta is -1 (up) or +1
// (down); the selection wraps within the action list.
func (m *model) navigateAutonomyProposal(delta int) {
	if m.pendingAutonomyProposal == nil {
		return
	}
	m.autonomyProposalSelect = (m.autonomyProposalSelect + delta + len(proposalActions)) % len(proposalActions)
	m.refreshViewportContent()
}

// activateAutonomyProposal runs the currently highlighted action.
func (m *model) activateAutonomyProposal() tea.Cmd {
	if m.pendingAutonomyProposal == nil {
		return nil
	}
	if m.autonomyProposalSelect < 0 || m.autonomyProposalSelect >= len(proposalActions) {
		m.autonomyProposalSelect = 0
	}
	switch proposalActions[m.autonomyProposalSelect] {
	case autonomy.ActionExecute:
		return m.executeAutonomyProposal()
	case autonomy.ActionInspect:
		m.toggleAutonomyProposalInspect()
		return nil
	default:
		return m.cancelAutonomyProposal()
	}
}

// clearAutonomyProposal drops any pending proposal and its transient
// navigation state without granting or executing anything. It is the cleanup
// seam shared by /clear, /drop, mode transitions and failure unwinds so a
// stale authorization gate can never block a later interaction.
func (m *model) clearAutonomyProposal() {
	m.pendingAutonomyProposal = nil
	m.autonomyProposalSelect = 0
	m.autonomyProposalInspect = false
	m.autonomyHotfix = false
	m.pendingHotfixObjective = ""
}

// renderAutonomyProposalBlock renders the ask_user decision surface. It is the
// ONLY user-facing authorization gate — there is no /grant command anywhere in
// the surface.
func (m *model) renderAutonomyProposalBlock(width int) string {
	prop := m.pendingAutonomyProposal
	if prop == nil {
		return ""
	}
	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	var b strings.Builder
	b.WriteString(permissionTitleStyle.Render(Icon.Warning + " AUTONOMY PROPOSAL"))
	b.WriteString("\n\n")

	b.WriteString(permissionDescStyle.Render("Intent:"))
	b.WriteString(" " + permissionTargetStyle.Render(prop.Intent.String()))
	b.WriteString("\n")
	b.WriteString(permissionDescStyle.Render("Workspace:"))
	b.WriteString(" " + permissionTargetStyle.Render(prop.Workspace.String()))
	b.WriteString("\n")
	if prop.Target != "" {
		b.WriteString(permissionDescStyle.Render("Target:"))
		b.WriteString(" " + permissionTargetStyle.Render(prop.Target))
		b.WriteString("\n")
	}
	riskStyle := tracerStyle
	switch prop.Risk {
	case autonomy.RiskHigh, autonomy.RiskCritical:
		riskStyle = redStyle
	case autonomy.RiskMedium:
		riskStyle = infoStyle
	}
	b.WriteString(permissionDescStyle.Render("Risk:"))
	b.WriteString(" " + riskStyle.Render(prop.Risk.String()))
	b.WriteString("\n")
	b.WriteString(permissionDescStyle.Render("Capabilities:"))
	b.WriteString(" " + permissionTargetStyle.Render(prop.CapabilityLabel()))
	b.WriteString("\n")
	rollback := greenStyle.Render("available")
	if !prop.Rollback {
		rollback = redStyle.Render("unavailable")
	}
	b.WriteString(permissionDescStyle.Render("Rollback:"))
	b.WriteString(" " + rollback)
	b.WriteString("\n")
	if prop.AffectedScope > 0 {
		b.WriteString(permissionDescStyle.Render("Affected scope:"))
		b.WriteString(" " + permissionTargetStyle.Render(fmt.Sprintf("%d file(s)", prop.AffectedScope)))
		b.WriteString("\n")
	}
	b.WriteString(permissionDescStyle.Render("Reason:"))
	b.WriteString(" " + infoStyle.Render(prop.Reason))
	b.WriteString("\n")

	// Planned high-level actions.
	if len(prop.Actions) > 0 {
		b.WriteString("\n")
		b.WriteString(permissionTitleStyle.Render("Planned actions"))
		b.WriteString("\n")
		for _, a := range prop.Actions {
			b.WriteString("  " + Icon.Chevron + " " + mutedStyle.Render(a))
			b.WriteString("\n")
		}
	}

	// Inspect detail view: the full decision facts, read-only.
	if m.autonomyProposalInspect {
		b.WriteString("\n")
		b.WriteString(permissionTitleStyle.Render("Decision detail"))
		b.WriteString("\n")
		fmt.Fprintf(&b, "  objective   : %s\n", prop.Input)
		req := prop.Required.String()
		if req == "" {
			req = "none"
		}
		fmt.Fprintf(&b, "  required    : %s\n", req)
		missing := prop.Missing.String()
		if missing == "" {
			missing = "none"
		}
		fmt.Fprintf(&b, "  missing     : %s\n", missing)
		fmt.Fprintf(&b, "  scope       : %s\n", prop.Scope)
		b.WriteString("  " + mutedStyle.Render("Inspect is read-only — it grants nothing and executes nothing."))
		b.WriteString("\n")
	}

	sep := strings.Repeat("─", boxWidth-4)
	b.WriteString(" " + sep + "\n")

	// Action menu (↑/↓ + Enter, Esc cancels).
	for i, action := range proposalActions {
		label := actionLabel(action)
		if i == m.autonomyProposalSelect {
			b.WriteString("  " + permissionKeyStyle.Render("[▶]") + " " + boldTextStyle.Render(label))
		} else {
			b.WriteString("    " + mutedStyle.Render(label))
		}
		b.WriteString("\n")
	}
	b.WriteString(" " + mutedStyle.Render("↑/↓ navigate · Enter execute · Esc cancel") + "\n")

	return permissionBoxStyle.Width(boxWidth).Render(b.String())
}

// actionLabel renders a human label for a proposal action.
func actionLabel(a autonomy.ProposalAction) string {
	switch a {
	case autonomy.ActionExecute:
		return "Execute — authorize and run"
	case autonomy.ActionInspect:
		return "Inspect — review decision detail"
	default:
		return "Cancel — abandon objective"
	}
}

// truncateDisplay bounds a string to n runes for compact status lines.
func truncateDisplay(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
