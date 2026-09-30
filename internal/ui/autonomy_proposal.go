package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// ── EXECUTION AUTHORIZATION (ask_user decision surface) ───────────────────
//
// The card is the ONLY user-facing decision surface for a DecisionAskUser
// verdict. It replaces the /grant command: the human never types a grant
// command — they select Execute, and the runtime issues the capability grant
// internally, re-runs the decision and continues execution.
//
// ── Three concepts, kept distinct ─────────────────────────────────────────
//
//	AUTONOMY DECISION     the policy verdict (continue / ask / block / answer).
//	AUTHORIZATION REQUEST the question that verdict raises. Nothing has been
//	                     proposed, planned, or written.
//	AUTHORIZATION GRANT   the capability the human releases on Execute. The UI
//	                     never mints one; the runtime does.
//
// A request for authorization is NOT an implementation proposal. The card is
// therefore named EXECUTION AUTHORIZATION, and it renders what is actually
// known at request time: the intent, the workspace, the scope, and the
// capability vector being requested — plus the explicit fact that no mutation
// has occurred. Nothing here implies a change is already drafted.
//
// ── The action set is DERIVED, never templated ────────────────────────────
//
// An action is rendered only when the object it acts on exists. Inspect is
// offered only when the runtime actually holds a candidate/diff to inspect; an
// authorization request is raised BEFORE any artifact is generated, so at that
// point there is nothing to diff and Inspect is simply absent. Offering it
// would advertise a diff that does not exist.

var (
	// actionExecuteLabel / actionCancelLabel are unconditional: the
	// authorization request exists, so releasing the grant and abandoning the
	// objective are always meaningful.
	actionExecuteLabel = "Execute"
	actionCancelLabel  = "Cancel"
	// actionInspectLabel is used only when a candidate/diff object exists.
	actionInspectLabel = "Inspect Diff"
)

// isLowRiskAutoApprovable reports whether a proposal can bypass the
// awaiting_human barrier: ScopeCapability (read+analyze+propose+mutate) granted
// for the target scope and RiskLevel <= RiskLow.
func (m *model) isLowRiskAutoApprovable(prop *autonomy.Proposal) bool {
	if prop == nil || m.autonomy == nil {
		return false
	}
	if prop.Risk > autonomy.RiskLow {
		return false
	}
	// ScopeCapability granted for "." or the engine's default scope.
	required := prop.Required
	if len(required) == 0 {
		required = autonomy.CapabilitySet{autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate}
	}
	if m.autonomy.Grants().Has(".", required) || m.autonomy.Grants().Has(m.autonomy.Scope(), required) || m.autonomy.Authority(required) {
		return true
	}
	return false
}

// authorizationCandidate reports whether the runtime currently holds a real
// mutation candidate/diff the human could inspect, and returns it.
//
// This is the ONLY source of truth for whether an Inspect action may be
// rendered. It reads the objects the runtime actually produced:
//
//   - a held patch at the approval gate (RuntimeExecutor staged a candidate and
//     compiled its diff), or
//   - a staged proposal carrying a non-empty compiled diff.
//
// A bare authorization request — the state the runtime is in when it asks the
// human for the mutate capability — has produced no artifact yet, so this
// returns false and Inspect is not offered.
func (m *model) authorizationCandidate() (string, bool) {
	if m.pendingHotfixPatch != nil && m.pendingHotfixPatch.Modified != "" {
		return m.pendingHotfixPatch.Modified, true
	}
	for _, p := range m.pendingProposals {
		if p.Diff != "" {
			return p.Diff, true
		}
	}
	return "", false
}

// authorizationActions returns the ordered action list derived from the
// CURRENT runtime state. It is the single source for both the rendered labels
// and the ↑/↓ navigation, so the two can never disagree about which actions
// exist.
func (m *model) authorizationActions() []autonomy.ProposalAction {
	actions := make([]autonomy.ProposalAction, 0, 3)
	actions = append(actions, autonomy.ActionExecute)
	if _, ok := m.authorizationCandidate(); ok {
		actions = append(actions, autonomy.ActionInspect)
	}
	actions = append(actions, autonomy.ActionCancel)
	return actions
}

// hasAuthorizationAction reports whether the derived action set contains an
// action. Key handlers consult it so a key bound to a hidden action is inert
// rather than silently performing something the UI never advertised.
func (m *model) hasAuthorizationAction(a autonomy.ProposalAction) bool {
	for _, candidate := range m.authorizationActions() {
		if candidate == a {
			return true
		}
	}
	return false
}

// ensureSyntheticMicroPlanForProposal registers a synthetic micro-plan for
// direct mutations that have no formal DAG (e.g. $hot single-file rewrite).
// Before a proposal transitions to awaiting_human or auto-execution the
// orchestrator must carry an authorized plan, otherwise the workflow guard
// rejects planning → building with "no authorized plan or micro-plan".
// This handshake is idempotent: when a formal DAG or ephemeral plan is
// already authorized it is a no-op; otherwise it injects a synthetic plan
// covering the proposal's target, intent and scope capabilities.
func (m *model) ensureSyntheticMicroPlanForProposal(prop *autonomy.Proposal) {
	if prop == nil || m.orch == nil {
		return
	}
	if m.orch.HasAuthorizedPlan() {
		return
	}
	// Only direct mutations that require a workspace transition need the guard.
	if prop.Workspace != autonomy.WorkspaceBuild {
		return
	}
	var targets []string
	switch {
	case prop.Target != "":
		targets = []string{prop.Target}
	case prop.Scope != "":
		targets = []string{prop.Scope}
	default:
		targets = []string{"direct-mutation"}
	}
	intent := string(prop.Intent)
	if intent == "" {
		intent = "modification"
	}
	caps := []string{}
	for _, c := range prop.Required {
		caps = append(caps, string(c))
	}
	if len(caps) == 0 {
		caps = []string{"mutate"}
	}
	scope := prop.Scope
	if scope == "" && m.autonomy != nil {
		scope = m.autonomy.Scope()
	}
	_ = m.orch.EnsureSyntheticMicroPlan(targets, intent, scope, caps)
}

// requestAutonomyProposal stages the ask_user decision surface: the runtime
// presents the intent/workspace/risk/capability facts and the planned actions,
// and waits for one explicit human decision. It never prints "Approve with
// /grant" — granting is an internal authorization operation.
//
// Low-risk auto-approval: if ScopeCapability (read+analyze+propose+mutate) is
// already granted for the target scope and RiskLevel <= RiskLow, the proposal
// bypasses the awaiting_human modal, streams a DiffPreviewEvent to the Activity
// Log, and transitions directly to executing.
//
// Synthetic micro-plan handshake: before the proposal transitions to
// awaiting_human OR auto-execution, the orchestrator is checked for an active
// plan. When no formal DAG exists (direct $hot / single-file rewrite) a
// SyntheticMicroPlan is registered so the workflow guard permits the
// planning → building transition.
func (m *model) requestAutonomyProposal(trace autonomy.Trace) tea.Cmd {
	prop := trace.Proposal()
	// ── Synthetic micro-plan handshake (planning → building guard) ────
	m.ensureSyntheticMicroPlanForProposal(prop)
	// ── Low-risk auto-approval: bypass awaiting_human ───────────────────
	if m.isLowRiskAutoApprovable(prop) {
		// PHASE 16: this used to stream a hardcoded checklist of planned steps
		// ("✓ inspect target", "✓ apply mutation") the moment the low-risk
		// auto-approval fired. Every one of those checkmarks was a lie: the
		// runtime had approved and not yet executed, and rendering an
		// intended action in the same visual language as a completed one is how
		// a user ends up believing their files were touched when they were not.
		//
		// What replaces it is a PERMISSION DECLARATION — a static statement of
		// the boundary that was released. It is not progress, it has no
		// execution glyphs, and it appears at the moment of the grant. From
		// here on, step lines come from runtime events and nowhere else.
		m.logActivity("[authorized] auto-approved low-risk mutation for %s — capability boundary released, execution not yet started",
			prop.Target)
		m.logActivity("  %s", renderAuthorizedPermission())
		m.pendingAutonomyProposal = prop
		m.autonomyProposalSelect = 0
		// Bypass modal: transition directly to executing.
		return m.executeAutonomyProposal()
	}
	m.pendingAutonomyProposal = prop
	m.autonomyProposalSelect = 0
	m.autonomyProposalInspect = false
	m.enterApprovalState()

	var b strings.Builder
	b.WriteString(boldSapphireStyle.Render(Icon.Blueprint+" EXECUTION AUTHORIZATION") + "\n")
	fmt.Fprintf(&b, "  intent      : %s\n", trace.Intent.Intent)
	if len(trace.Intent.Targets) > 0 {
		fmt.Fprintf(&b, "  targets     : %s\n", strings.Join(trace.Intent.Targets, ", "))
	}
	fmt.Fprintf(&b, "  workspace   : %s\n", trace.Route.Workspace)
	// The instruction names only the actions that exist RIGHT NOW. When no
	// candidate has been generated there is no diff to inspect, so Inspect is
	// not offered and not mentioned.
	instruction := "Select Execute to authorize the capabilities above, or Esc to cancel. No mutation has occurred."
	if m.hasAuthorizationAction(autonomy.ActionInspect) {
		instruction = "Select Execute to authorize, Inspect the candidate diff, or Esc to cancel."
	}
	b.WriteString("\n  " + infoStyle.Render(instruction) + "\n")
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
	// Ensure the synthetic handshake is present before any execution path
	// leaves the proposal gate — the guard is evaluated on the subsequent
	// planning → building transition inside dispatchAutonomyTrace /
	// executeAutonomyWorkspace.
	m.ensureSyntheticMicroPlanForProposal(prop)
	m.pendingAutonomyProposal = nil
	m.autonomyProposalInspect = false
	m.resolveApprovalState()

	// ── Capability authorization: grant internally, revalidate, continue ──
	if len(prop.Missing) > 0 {
		g := m.autonomy.GrantDefault(prop.Missing...)
		m.push(roleStatus, fmt.Sprintf(
			"%s Capability granted: %s\n  scope: %s\n  %s\n%s",
			greenStyle.Render("✓"), strings.Join(capNames(g.Capabilities), " + "), g.Scope,
			renderAuthorizedPermission(),
			mutedStyle.Render("These are the operations the runtime may now perform inside this boundary. No operation has been performed yet."),
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
// (down); the selection wraps within the DERIVED action list, so navigation can
// never land on an action the card does not render.
func (m *model) navigateAutonomyProposal(delta int) {
	if m.pendingAutonomyProposal == nil {
		return
	}
	actions := m.authorizationActions()
	if len(actions) == 0 {
		return
	}
	// Re-anchor the selection when the derived set shrank (a candidate was
	// consumed), so a stale index cannot activate the wrong action.
	if m.autonomyProposalSelect < 0 || m.autonomyProposalSelect >= len(actions) {
		m.autonomyProposalSelect = 0
	}
	m.autonomyProposalSelect = (m.autonomyProposalSelect + delta + len(actions)) % len(actions)
	m.refreshViewportContent()
}

// activateAutonomyProposal runs the currently highlighted action of the DERIVED
// action set.
func (m *model) activateAutonomyProposal() tea.Cmd {
	if m.pendingAutonomyProposal == nil {
		return nil
	}
	actions := m.authorizationActions()
	if m.autonomyProposalSelect < 0 || m.autonomyProposalSelect >= len(actions) {
		m.autonomyProposalSelect = 0
	}
	switch actions[m.autonomyProposalSelect] {
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
	m.clearAutonomyTargetSelector()
	// PHASE 16: the step ledger is cleared HERE, on the same unwind seam as the
	// pending proposal. Leaving it alive would let a previous run's completed
	// steps render as this run's progress — the exact failure the hardcoded
	// checklist caused, reintroduced through a different mechanism.
	m.executionStepLedger().Reset()
}

// renderAutonomyProposalBlock renders the compact authorization card
// positioned directly above the input prompt region. It is the ONLY user-facing
// authorization gate — there is no /grant command anywhere in the surface.
//
// Everything on the card is a FACT observed at authorization-request time:
//
//	Intent / Workspace / Scope — the classified objective and its boundary.
//	Capabilities               — the capability vector being requested.
//	No mutation has occurred.   — the explicit absence of any change.
//
// The action line is derived from runtime state (authorizationActions), so an
// action is shown only when the object it acts on exists. With no candidate
// generated yet, the card offers exactly Execute and Cancel.
func (m *model) renderAutonomyProposalBlock(width int) string {
	prop := m.pendingAutonomyProposal
	if prop == nil {
		return ""
	}
	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	target := prop.Target
	if target == "" {
		if prop.Scope != "" {
			target = prop.Scope
		} else {
			target = prop.Intent.String()
		}
	}

	riskStr := strings.ToUpper(prop.Risk.String())
	if riskStr == "" {
		riskStr = "LOW"
	}
	var riskStyled string
	switch prop.Risk {
	case autonomy.RiskHigh, autonomy.RiskCritical:
		riskStyled = redStyle.Render(riskStr)
	case autonomy.RiskMedium:
		riskStyled = infoStyle.Render(riskStr)
	default:
		riskStyled = greenStyle.Render(riskStr)
	}

	scopeStr := "1 file"
	if prop.AffectedScope > 0 {
		scopeStr = fmt.Sprintf("%d file(s)", prop.AffectedScope)
	}

	rollbackStr := "OK"
	if !prop.Rollback {
		rollbackStr = "NO"
	}

	var b strings.Builder
	// Line 1: the decision facts, risk and rollback availability.
	fmt.Fprintf(&b, "Target: %s │ Risk: %s │ Scope: %s │ %s/%s (Rollback: %s)\n",
		permissionTargetStyle.Render(target),
		riskStyled,
		permissionTargetStyle.Render(scopeStr),
		strings.ToLower(prop.Intent.String()),
		strings.ToLower(prop.Workspace.String()),
		rollbackStr,
	)

	// Line 2: the capability vector being requested. This is the substance of
	// the authorization request — it is what the human is actually releasing.
	// It replaces the old "Plan: read -> propose -> mutate -> verify" line,
	// which described an execution chain that had not been planned or begun.
	requested := prop.Required.String()
	if requested == "" {
		requested = prop.CapabilityLabel()
	}
	fmt.Fprintf(&b, "Capabilities: %s\n", mutedStyle.Render(requested))

	// Line 3: the explicit absence of any change. An authorization request is
	// raised before anything is generated; saying so keeps the card from
	// reading as though a change already exists.
	b.WriteString("No mutation has occurred.\n")

	// Line 4: the DERIVED action set.
	b.WriteString(m.renderAuthorizationActionLine())

	// Line 5: runtime-attested execution steps. This block exists ONLY to the
	// extent a runtime StepStarted/StepCompleted event was consumed. When
	// nothing has been attested it renders nothing — not a planned list, not an
	// empty section, not a hint at what is coming. The permission declaration
	// above is what describes intent; this is what describes fact.
	if steps := m.executionStepLedger().renderExecutionSteps(); steps != "" {
		b.WriteString(steps)
	}

	// Inspect expansion: reachable only when a candidate/diff exists (the
	// action that reveals it is state-derived), so this line cannot appear
	// without something real to show.
	if m.autonomyProposalInspect {
		if diff, ok := m.authorizationCandidate(); ok {
			b.WriteString("\n" + permissionDescStyle.Render("Candidate diff:"))
			b.WriteString("\n" + mutedStyle.Render(truncateDisplay(diff, boxWidth*2)))
		}
		b.WriteString("\n" + permissionDescStyle.Render("Decision detail:") + " " + mutedStyle.Render(fmt.Sprintf("objective=%s requested=%s missing=%s scope=%s",
			truncateDisplay(prop.Input, 40), prop.Required.String(), prop.Missing.String(), prop.Scope)))
	}

	headerTitle := fmt.Sprintf(" %s EXECUTION AUTHORIZATION ", Icon.Warning)
	return renderBoxWithTitle(headerTitle, b.String(), boxWidth)
}

// renderAuthorizationActionLine renders the key hints for the DERIVED action
// set. Every rendered hint is bound to a key that the handler will honour, and
// every honoured action is rendered here — the two are the same list.
func (m *model) renderAuthorizationActionLine() string {
	labels := map[autonomy.ProposalAction]string{
		autonomy.ActionExecute: "[Enter] " + actionExecuteLabel,
		autonomy.ActionInspect: "[I] " + actionInspectLabel,
		autonomy.ActionCancel:  "[Esc] " + actionCancelLabel,
	}
	keys := map[autonomy.ProposalAction]string{
		autonomy.ActionExecute: "[Enter]",
		autonomy.ActionInspect: "[I]",
		autonomy.ActionCancel:  "[Esc]",
	}
	// The derived set is read ONCE per render, so every hint on this line is
	// built from a single snapshot of the runtime state — the line can never be
	// assembled from two different moments.
	actions := m.authorizationActions()
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		parts = append(parts, permissionKeyStyle.Render(keys[a])+" "+boldTextStyle.Render(labels[a]))
	}
	return "Action: " + strings.Join(parts, "   ")
}

func renderBoxWithTitle(title, content string, boxWidth int) string {
	const (
		borderFg = "\x1b[38;2;88;91;112m" // #585b70
		reset    = "\x1b[0m"
	)
	var b strings.Builder
	titleCells := lipgloss.Width(title)
	dashLen := boxWidth - 2 - titleCells - 1
	if dashLen < 0 {
		dashLen = 0
	}
	// ┌─ [TITLE] ───┐
	b.WriteString(borderFg + "┌─" + reset + title + borderFg + strings.Repeat("─", dashLen) + "┐" + reset + "\n")

	lines := strings.Split(content, "\n")
	for _, l := range lines {
		lCells := lipgloss.Width(l)
		pad := boxWidth - 4 - lCells
		if pad < 0 {
			pad = 0
		}
		b.WriteString(borderFg + "│ " + reset + l + strings.Repeat(" ", pad) + borderFg + " │" + reset + "\n")
	}

	b.WriteString(borderFg + "└" + strings.Repeat("─", boxWidth-2) + "┘" + reset)
	return b.String()
}

// truncateDisplay bounds a string to n runes for compact status lines.
func truncateDisplay(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
