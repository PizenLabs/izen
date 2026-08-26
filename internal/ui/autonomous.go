package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── PRODUCTION AUTONOMOUS DRIVER BRIDGE (Phase 6) ───────────────────────────
//
// The composition-root autonomous Driver owns the bounded loop: resolve →
// observe → decide → execute → interpret → complete/recover/abort/park. The UI
// never owns loop state — it initiates a run (Run), renders the parked human
// boundary, resumes it (ResumeApprove/ResumeReject/ResumeClarify) or aborts it
// (Abort), and projects the terminal outcome. It reaches the driver ONLY
// through the structural interface below so this package never imports
// internal/runtime/autonomy (architecture invariant: the UI is a projection,
// the driver owns the loop).
//
// The driver parks at a HumanBoundary with a PatchID (approval), Options
// (clarify) or neither (inform). The boundary's Targets are authoritative: on
// approve the UI issues a MutationAuthorization over exactly those files before
// ResumeApprove so the executor's held patch applies under the same governance
// owner as every other mutation.

// executeAutonomyViaDriver is the Phase 6 production BUILD path: an
// autonomy-decided mutation workspace is executed by the bounded autonomous
// Driver (resolve → observe → decide → execute → interpret → complete/park)
// instead of the single-shot executor submission. The driver owns target
// resolution through the IntentGateway and the approval gate through the
// RuntimeExecutor; the UI initiates and renders the parked boundary. When the
// driver is not wired (harness), the legacy single-shot executor path runs.
func (m *model) executeAutonomyViaDriver(trace autonomy.Trace) tea.Cmd {
	if m.autonomousDriver == nil {
		return m.executeAutonomyViaRuntime(trace)
	}
	prompt := trace.Input
	if m.autonomyHotfix {
		m.autonomyHotfix = false
		if objective := m.pendingHotfixObjective; objective != "" {
			m.pendingHotfixObjective = ""
			prompt = objective
		}
	}
	return m.runAutonomousDriver(prompt)
}

// autonomousDriver is the minimal production surface the TUI may drive. It is
// implemented by the runtime autonomy Driver bound at the composition root.
type autonomousDriver interface {
	Run(ctx context.Context, objective string) (*autonomy.LoopTermination, error)
	ResumeApprove(ctx context.Context) (*autonomy.LoopTermination, error)
	ResumeReject(ctx context.Context, reason string) (*autonomy.LoopTermination, error)
	ResumeClarify(ctx context.Context, target string) (*autonomy.LoopTermination, error)
	ResumeApproveProposal(ctx context.Context) (*autonomy.LoopTermination, error)
	ResumeRejectProposal(ctx context.Context, reason string) (*autonomy.LoopTermination, error)
	Abort(reason string) (*autonomy.LoopTermination, error)
	State() autonomy.RuntimeState
	Boundary() *autonomy.HumanBoundary
	Termination() *autonomy.LoopTermination
	SetStreamCallback(cb execution.StreamCallback)
	AggregatedUsage() (input, output int, known bool)
}

// autonomousRunMsg carries a driver Run/Resume/Abort outcome back into the
// Bubble Tea event loop. A nil term with a non-nil Boundary means the run
// parked at a human boundary (approval/clarify/inform).
type autonomousRunMsg struct {
	term *autonomy.LoopTermination
	err  error
}

var _ tea.Msg = autonomousRunMsg{}

// runAutonomousDriver starts a fresh bounded driver run for the objective
// under a foreground operation. Duplicate-start protection mirrors the
// driver's own single-lane guard: only one run may be active or parked, so a
// second start can never clobber a parked boundary.
func (m *model) runAutonomousDriver(objective string) tea.Cmd {
	if m.autonomousDriver == nil {
		return nil
	}
	if m.autonomousActive || m.autonomousParked() {
		m.push(roleError, "[autonomous] a run is already active or parked — resume or abort it first")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	m.autonomousActive = true
	m.autonomousBoundary = nil
	m.autonomousSelect = 0
	m.autonomousObjective = objective
	m.beginOperation(OpAutonomous)
	m.agentLabel = ""
	m.startShimmer("", "autonomy")

	// Set up executor streaming (same mechanism as $prompt/$hot gateway path)
	m.execStreamCh = make(chan tea.Msg, 1024)
	m.execStreaming = true
	m.spinnerFrame = 0
	m.startShimmer("Waiting for model...", "autonomy")

	m.autonomousDriver.SetStreamCallback(func(ev execution.StreamEvent) {
		ch := m.execStreamCh
		if ch == nil {
			return
		}
		switch ev.Kind {
		case "first_token":
			ch <- tokenMsg("")
		case "content_delta":
			if ev.Content != "" {
				ch <- tokenMsg(ev.Content)
			}
		case "reasoning_delta":
			// Sub-task reasoning tokens: forwarded to the main UI loop so the
			// Ctrl+O thought drawer updates in real time during DAG_EXECUTING.
			if ev.Content != "" {
				ch <- ReasoningChunkMsg{Chunk: ev.Content}
			}
		case "done":
			ch <- streamDoneMsg{
				content:        ev.Content,
				tokenInput:     ev.Usage.PromptTokens,
				tokenOutput:    ev.Usage.CompletionTokens,
				usageEstimated: ev.Usage.Estimated,
				truncated:      strings.EqualFold(strings.TrimSpace(ev.FinishReason), "length"),
			}
		case "error":
			if ev.Err != nil {
				ch <- streamErrMsg{err: ev.Err}
			}
		}
	})

	readerCmd := func() tea.Msg {
		return m.readExecStream()
	}

	ctx := m.operationContext()
	return tea.Batch(
		readerCmd,
		func() tea.Msg {
			term, err := m.autonomousDriver.Run(ctx, objective)
			return autonomousRunMsg{term: term, err: err}
		},
		m.smoothStreamTickCmd(),
		m.shimmerTickCmd(),
	)
}

// beginAutonomousResume marks an in-flight RESUME operation (approve/reject/
// clarify/proposal) and arms the animation layer for it. autonomousActive is
// set so the shimmer safety-net never kills the loading line while the driver
// goroutine runs — resume paths previously left it false, which let every
// tick loop die and froze the spinner for the whole DAG_EXECUTING phase.
func (m *model) beginAutonomousResume(phase string) {
	m.autonomousActive = true
	m.beginOperation(OpAutonomous)
	m.agentLabel = ""
	m.startShimmer("", phase)
}

// autonomousResumeCmds batches the driver-resume command with the spinner/
// shimmer tick loops. The driver runs in its own non-blocking tea.Cmd
// goroutine; WITHOUT the batched ticks no message ever re-enters the update
// loop until the terminal msg lands, so spin.Tick frames starve and the
// spinner visibly freezes mid-execution.
func (m *model) autonomousResumeCmds(run tea.Cmd) tea.Cmd {
	return tea.Batch(run, m.smoothStreamTickCmd(), m.shimmerTickCmd())
}

// resumeAutonomousApprove approves the parked approval boundary. It first
// issues a MutationAuthorization over the boundary's target files through the
// production AuthorizationEngine (the same governance owner every other
// mutation uses) and attaches it to the executor the driver shares, THEN
// resumes the driver so the held patch applies under authorization.
func (m *model) resumeAutonomousApprove() tea.Cmd {
	if !m.autonomousParked() {
		return nil
	}
	if err := m.authorizeAutonomousApproval(); err != nil {
		m.push(roleError, "[autonomous] authorization: "+err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	m.autonomousBoundary = nil
	m.beginAutonomousResume("autonomy apply")
	ctx := m.operationContext()
	return m.autonomousResumeCmds(func() tea.Msg {
		term, err := m.autonomousDriver.ResumeApprove(ctx)
		return autonomousRunMsg{term: term, err: err}
	})
}

// resumeAutonomousReject rejects the parked approval boundary. The rejection
// is a terminal human decision through the executor; no files are touched.
func (m *model) resumeAutonomousReject(reason string) tea.Cmd {
	if !m.autonomousParked() {
		return nil
	}
	m.autonomousBoundary = nil
	m.beginAutonomousResume("autonomy reject")
	ctx := m.operationContext()
	return m.autonomousResumeCmds(func() tea.Msg {
		term, err := m.autonomousDriver.ResumeReject(ctx, reason)
		return autonomousRunMsg{term: term, err: err}
	})
}

// resumeAutonomousClarify resumes a parked clarify boundary with the selected
// candidate target. Selection is an explicit human act; no candidate is ever
// auto-picked.
func (m *model) resumeAutonomousClarify() tea.Cmd {
	b := m.autonomousBoundary
	if b == nil || b.Action != autonomy.HumanBoundaryClarify || len(b.Options) == 0 {
		return nil
	}
	if m.autonomousSelect < 0 || m.autonomousSelect >= len(b.Options) {
		m.autonomousSelect = 0
	}
	target := b.Options[m.autonomousSelect]
	m.autonomousBoundary = nil
	m.beginAutonomousResume("autonomy")
	ctx := m.operationContext()
	return m.autonomousResumeCmds(func() tea.Msg {
		term, err := m.autonomousDriver.ResumeClarify(ctx, target)
		return autonomousRunMsg{term: term, err: err}
	})
}

// resumeAutonomousProposalApprove resolves a parked DECOMPOSITION_PROPOSAL
// boundary by authorizing the WHOLE plan: the driver runs every approved
// sub-task as one atomic transaction. The authorization issued here covers
// exactly the plan's target files, mirroring the plain approval gate.
func (m *model) resumeAutonomousProposalApprove() tea.Cmd {
	b := m.autonomousBoundary
	if b == nil || b.Action != autonomy.HumanBoundaryDecomposition || b.Proposal == nil {
		return nil
	}
	if err := m.authorizeAutonomousApproval(); err != nil {
		m.push(roleError, "[autonomous] proposal authorization: "+err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	m.autonomousBoundary = nil
	m.beginAutonomousResume("autonomy dag")
	ctx := m.operationContext()
	// DAG_EXECUTING runs every sub-task inside this non-blocking tea.Cmd
	// goroutine; the batched spin.Tick loops keep the event loop rendering
	// (spinner frames + shimmer sweep) for the whole transaction.
	return m.autonomousResumeCmds(func() tea.Msg {
		term, err := m.autonomousDriver.ResumeApproveProposal(ctx)
		return autonomousRunMsg{term: term, err: err}
	})
}

// resumeAutonomousProposalReject resolves a parked DECOMPOSITION_PROPOSAL by
// rejecting the whole plan. Nothing was executed; the rejection is a terminal
// human decision.
func (m *model) resumeAutonomousProposalReject(reason string) tea.Cmd {
	b := m.autonomousBoundary
	if b == nil || b.Action != autonomy.HumanBoundaryDecomposition {
		return nil
	}
	// A rejected proposal authorizes nothing: drop any plan authorization the
	// run carried so the workflow guard stays honest for future runs.
	m.orch.ClearAuthorizedPlan()
	m.autonomousBoundary = nil
	m.beginAutonomousResume("autonomy cancel")
	ctx := m.operationContext()
	return m.autonomousResumeCmds(func() tea.Msg {
		term, err := m.autonomousDriver.ResumeRejectProposal(ctx, reason)
		return autonomousRunMsg{term: term, err: err}
	})
}

// abortAutonomousRun aborts a parked driver run. It is the only UI path to
// cancel a parked run; an in-flight run is cancelled via its context.
func (m *model) abortAutonomousRun(reason string) tea.Cmd {
	if !m.autonomousParked() {
		return nil
	}
	m.autonomousBoundary = nil
	// The abort command is in flight: mark the run active so the emergency-
	// interrupt path never finalizes the operation concurrently with the
	// driver's own terminal message.
	m.autonomousActive = true
	m.beginOperation(OpAutonomous)
	return func() tea.Msg {
		term, err := m.autonomousDriver.Abort(reason)
		return autonomousRunMsg{term: term, err: err}
	}
}

// autonomousParked reports whether the model holds a parked driver boundary.
func (m *model) autonomousParked() bool {
	return m.autonomousBoundary != nil && m.autonomousDriver != nil
}

// handleAutonomousRun processes the terminal/parked outcome of a driver
// Run/Resume/Abort. It releases the operation, renders the boundary card or the
// terminal outcome, and keeps the driver's loop state as the single truth.
func (m *model) handleAutonomousRun(msg autonomousRunMsg) tea.Cmd {
	m.autonomousActive = false
	// ── STREAMING TERMINALIZATION (spinner contract) ────────────────
	// The autonomous streaming channel is terminalized here idempotently: every
	// terminal/parked outcome clears the streaming state, stops the shimmer and
	// marks the stage done so no spinner can survive the execution lifecycle.
	m.execStreamCh = nil
	if m.execStreaming {
		m.execStreaming = false
		m.stopShimmer()
		m.setStage("model", m.getActiveModelName(), stageDone)
	}
	// Fetch aggregated authoritative usage from the driver (one count per logical invocation).
	var aggIn, aggOut int
	var aggKnown bool
	if m.autonomousDriver != nil {
		aggIn, aggOut, aggKnown = m.autonomousDriver.AggregatedUsage()
	}
	usageCmd := func() tea.Cmd {
		if aggKnown {
			return m.tokenUsageCmdKnown(aggIn, aggOut, true)
		}
		return nil
	}

	if msg.err != nil {
		m.autonomousBoundary = nil
		m.finalizeOperation(OpOutcomeFailure, msg.err)
		m.push(roleError, "[autonomous] "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		cmd := usageCmd()
		if cmd != nil {
			return cmd
		}
		return nil
	}

	// A parked run: the driver holds a human boundary. Render it.
	if msg.term == nil {
		b := m.autonomousDriver.Boundary()
		m.autonomousBoundary = b
		m.autonomousSelect = 0
		if b != nil {
			switch b.Action {
			case autonomy.HumanBoundaryClarify:
				m.finalizeOperation(OpOutcomeAmbiguous, nil)
				m.renderAutonomousClarifyBoundary(b)
			case autonomy.HumanBoundaryApproval:
				m.finalizeOperation(OpOutcomeAmbiguous, nil)
				m.renderAutonomousApprovalBoundary(b)
			case autonomy.HumanBoundaryDecomposition:
				// A staged DECOMPOSITION_PROPOSAL (PLAN_STAGED) is a live
				// human decision: render the interactive proposal card, not
				// an inform/pause notice.
				m.finalizeOperation(OpOutcomeAmbiguous, nil)
				m.renderAutonomousDecompositionBoundary(b)
			default:
				m.finalizeOperation(OpOutcomeFailure, nil)
				m.renderAutonomousInformBoundary(b)
			}
			m.enterApprovalState()
		} else {
			m.finalizeOperation(OpOutcomeFailure, nil)
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		cmd := usageCmd()
		if cmd != nil {
			// Even while parked, the provider usage of completed attempts is authoritative and must reach the footer.
			return cmd
		}
		return nil
	}

	// A terminal outcome.
	m.autonomousBoundary = nil
	if msg.term.State == autonomy.RuntimeCompleted {
		m.finalizeOperation(OpOutcomeSuccess, nil)
		m.push(roleSystem, infoStyle.Render("[autonomous] "+greenStyle.Render("completed")+" — "+msg.term.Reason))
	} else {
		m.finalizeOperation(OpOutcomeFailure, nil)
		m.push(roleError, "[autonomous] aborted — "+msg.term.Reason)
		m.push(roleSystem, infoStyle.Render("Interrupted."))
	}
	// Restore interactive input for terminal autonomous runs.
	m.ti.Focus()
	m.recalcViewportHeight()
	m.state = StateChat
	m.resolveApprovalState()
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	cmd := usageCmd()
	if cmd != nil {
		return cmd
	}
	return nil
}

// authorizeAutonomousApproval issues a MutationAuthorization over the parked
// approval boundary's target files and attaches it to the executor the driver
// shares, so ResumeApprove applies the held patch under governance.
func (m *model) authorizeAutonomousApproval() error {
	if m.executor == nil || m.authEngine == nil {
		return nil
	}
	// ── STAGED DAG HANDSHAKE (planning → building guard) ────────────────
	// An approved DECOMPOSITION_PROPOSAL IS an authorized plan: the staged
	// ExecutionDAG lives inside the Autonomy Loop context (StagedPlan), so it
	// must be registered with the orchestrator BEFORE the workflow transition
	// is requested — otherwise the guard rejects planning → building with
	// "no authorized plan or micro-plan" even though the human just approved
	// every sub-task.
	b := m.autonomousBoundary
	if m.orch != nil && b != nil && b.Action == autonomy.HumanBoundaryDecomposition && b.Proposal != nil {
		if err := m.orch.BindAuthorizedMicroPlan(context.Background(), b.Proposal); err != nil {
			return fmt.Errorf("micro-plan binding failed: %w", err)
		}
	}
	// Ensure workflow is in Building/Repairing state for authorization.
	// Autonomous execution from /model mode starts in Planning; we must transition.
	if err := m.transitionToBuilding(); err != nil {
		return fmt.Errorf("workflow transition to building failed: %w", err)
	}
	var targets []string
	if b != nil {
		targets = b.Targets
	}
	auth, err := m.authEngine.AuthorizeBuild(
		targets,
		m.caps,
		m.mutationBudget,
		m.microBudget,
		false,
		true, // human-approved: the developer pressed Alt+A on the boundary
	)
	if err != nil {
		return err
	}
	m.executor.SetAuthorization(auth)
	return nil
}

// navigateAutonomousBoundary moves the clarify-candidate highlight. delta is
// -1 (up) or +1 (down); the selection wraps within the candidate list.
func (m *model) navigateAutonomousBoundary(delta int) {
	b := m.autonomousBoundary
	if b == nil || b.Action != autonomy.HumanBoundaryClarify || len(b.Options) == 0 {
		return
	}
	m.autonomousSelect = (m.autonomousSelect + delta + len(b.Options)) % len(b.Options)
	m.refreshViewportContent()
}

// clearAutonomousRun drops the parked-boundary state without touching the
// driver (used by /clear and interrupt paths after the run is aborted).
func (m *model) clearAutonomousRun() {
	m.autonomousBoundary = nil
	m.autonomousSelect = 0
	m.autonomousActive = false
	m.autonomousObjective = ""
}

// renderAutonomousApprovalBoundary renders the parked approval gate status.
func (m *model) renderAutonomousApprovalBoundary(b *autonomy.HumanBoundary) {
	var targets string
	if len(b.Targets) > 0 {
		targets = strings.Join(b.Targets, ", ")
	}
	m.push(roleStatus, fmt.Sprintf(
		"%s AUTONOMY APPROVAL — mutation awaiting authorization: %s",
		boldSapphireStyle.Render(Icon.Blueprint), targets))
	m.push(roleSystem, mutedStyle.Render("  "+b.Reason))
	m.push(roleSystem, infoStyle.Render("  Alt+A / Enter approve · Alt+R / Esc reject · Ctrl+C aborts"))
}

// renderAutonomousClarifyBoundary renders the parked target-ambiguity status.
func (m *model) renderAutonomousClarifyBoundary(b *autonomy.HumanBoundary) {
	m.push(roleStatus, "[autonomy] target is ambiguous — select the file to act on (↑/↓ + Enter, Esc cancels)")
	m.push(roleSystem, mutedStyle.Render("  "+b.Reason))
	m.push(roleSystem, infoStyle.Render("  ↑/↓ navigate · Enter select · Ctrl+C aborts"))
}

// renderAutonomousInformBoundary renders a non-resumable informational park
// (recovery exhaustion). A fresh run may start; no resume decision exists.
func (m *model) renderAutonomousInformBoundary(b *autonomy.HumanBoundary) {
	m.push(roleError, "[autonomous] run paused — "+b.Reason)
	m.push(roleSystem, mutedStyle.Render("  No further automatic execution. Start a fresh run (Ctrl+C to dismiss)."))
}

// renderAutonomousDecompositionBoundary renders the parked DECOMPOSITION_
// PROPOSAL (PLAN_STAGED) status lines: the staged plan, its strategy and its
// sub-task breakdown, plus the explicit keybindings.
func (m *model) renderAutonomousDecompositionBoundary(b *autonomy.HumanBoundary) {
	dag := b.Proposal
	if dag == nil {
		m.renderAutonomousInformBoundary(b)
		return
	}
	m.push(roleStatus, fmt.Sprintf(
		"%s DECOMPOSITION PROPOSAL — %d staged sub-task(s) on %s",
		boldSapphireStyle.Render(Icon.Blueprint), len(dag.SubTasks), dag.Target))
	m.push(roleSystem, mutedStyle.Render("  "+b.Reason))
	m.push(roleSystem, infoStyle.Render("  Enter authorizes & runs the whole DAG · Esc cancels the plan"))
}

// renderDecompositionProposalBlock renders the staged ExecutionDAG as a
// framed interactive proposal box: the splitting strategy kind, every sub-task
// with its line-range window, and the navigation keys.
func renderDecompositionProposalBlock(dag *planner.ExecutionDAG, width int) string {
	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	var sb strings.Builder
	sb.WriteString(decompositionTitleStyle.Render(Icon.Blueprint + " DECOMPOSITION PROPOSAL"))
	sb.WriteString("\n\n")
	sb.WriteString(permissionDescStyle.Render("Strategy:"))
	sb.WriteString(" " + permissionTargetStyle.Render(string(dag.Kind)))
	sb.WriteString("\n")
	sb.WriteString(permissionDescStyle.Render("Target:"))
	sb.WriteString(" " + permissionTargetStyle.Render(dag.Target))
	sb.WriteString("\n")
	sb.WriteString(permissionDescStyle.Render(fmt.Sprintf("Sub-tasks (%d):", len(dag.SubTasks))))
	sb.WriteString("\n")
	for _, st := range dag.SubTasks {
		fmt.Fprintf(&sb, "  %s %s %s — %s (~%d tok)\n",
			decompositionKeyStyle.Render(st.ID),
			Icon.Chevron,
			boldTextStyle.Render(st.Region.String()),
			mutedStyle.Render(truncateDisplay(st.Description, 48)),
			st.EstimatedTokens)
	}
	total := dag.TotalEstimatedTokens()
	fmt.Fprintf(&sb, "%s ~%d tok total · budget ≤%d tok/sub-task\n",
		permissionDescStyle.Render("Budget:"), total, dag.Budget())

	sep := strings.Repeat("─", boxWidth-4)
	sb.WriteString(" " + sep + "\n")
	sb.WriteString(" " + fmt.Sprintf("%s Authorize & Run DAG   %s Cancel",
		decompositionKeyStyle.Render("[Enter]"), decompositionKeyStyle.Render("[Esc]")) + "\n")

	return decompositionBoxStyle.Width(boxWidth).Render(sb.String())
}

// renderAutonomousBoundaryBlock renders the parked driver boundary as an
// interactive card. It is the ONLY human decision surface for a parked run.
func (m *model) renderAutonomousBoundaryBlock(width int) string {
	b := m.autonomousBoundary
	if b == nil {
		return ""
	}
	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	var sb strings.Builder
	switch b.Action {
	case autonomy.HumanBoundaryApproval:
		sb.WriteString(permissionTitleStyle.Render(Icon.Warning + " AUTONOMY APPROVAL"))
		sb.WriteString("\n\n")
		sb.WriteString(permissionDescStyle.Render("Mutation:"))
		sb.WriteString(" " + permissionTargetStyle.Render(m.autonomousObjective))
		sb.WriteString("\n")
		if len(b.Targets) > 0 {
			sb.WriteString(permissionDescStyle.Render("Targets:"))
			sb.WriteString(" " + permissionTargetStyle.Render(strings.Join(b.Targets, ", ")))
			sb.WriteString("\n")
		}
		sb.WriteString(permissionDescStyle.Render("Reason:"))
		sb.WriteString(" " + infoStyle.Render(b.Reason))
		sb.WriteString("\n")
	case autonomy.HumanBoundaryClarify:
		sb.WriteString(permissionTitleStyle.Render(Icon.Warning + " AUTONOMY TARGET SELECTION"))
		sb.WriteString("\n\n")
		sb.WriteString(permissionDescStyle.Render("Which target should I act on?"))
		sb.WriteString("\n")
		for i, opt := range b.Options {
			if i == m.autonomousSelect {
				sb.WriteString("  " + permissionKeyStyle.Render("[▶]") + " " + boldTextStyle.Render(opt))
			} else {
				sb.WriteString("    " + mutedStyle.Render(opt))
			}
			sb.WriteString("\n")
		}
	case autonomy.HumanBoundaryInform:
		sb.WriteString(permissionTitleStyle.Render(Icon.Warning + " AUTONOMY PAUSED"))
		sb.WriteString("\n\n")
		sb.WriteString(permissionDescStyle.Render("Reason:"))
		sb.WriteString(" " + infoStyle.Render(b.Reason))
		sb.WriteString("\n")
		sb.WriteString(" " + mutedStyle.Render("No further automatic execution. Start a fresh run (Ctrl+C to dismiss).") + "\n")
		return permissionBoxStyle.Width(boxWidth).Render(sb.String())
	case autonomy.HumanBoundaryDecomposition:
		// The staged DECOMPOSITION_PROPOSAL (PLAN_STAGED) decision card.
		if b.Proposal != nil {
			return renderDecompositionProposalBlock(b.Proposal, width)
		}
		sb.WriteString(permissionTitleStyle.Render(Icon.Warning + " DECOMPOSITION PROPOSAL"))
		sb.WriteString("\n\n")
		sb.WriteString(permissionDescStyle.Render("Reason:"))
		sb.WriteString(" " + infoStyle.Render(b.Reason))
		sb.WriteString("\n")
		sep := strings.Repeat("─", boxWidth-4)
		sb.WriteString(" " + sep + "\n")
		sb.WriteString(" " + fmt.Sprintf("%s Authorize & Run DAG   %s Cancel",
			decompositionKeyStyle.Render("[Enter]"), decompositionKeyStyle.Render("[Esc]")) + "\n")
		return permissionBoxStyle.Width(boxWidth).Render(sb.String())
	default:
		return ""
	}

	sep := strings.Repeat("─", boxWidth-4)
	sb.WriteString(" " + sep + "\n")

	if b.Action == autonomy.HumanBoundaryApproval {
		sb.WriteString(" " + mutedStyle.Render("Alt+A / Enter approve · Alt+R / Esc reject · Ctrl+C abort") + "\n")
	} else {
		sb.WriteString(" " + mutedStyle.Render("↑/↓ navigate · Enter select · Esc cancel · Ctrl+C abort") + "\n")
	}

	return permissionBoxStyle.Width(boxWidth).Render(sb.String())
}
