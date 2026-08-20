package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
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
	Abort(reason string) (*autonomy.LoopTermination, error)
	State() autonomy.RuntimeState
	Boundary() *autonomy.HumanBoundary
	Termination() *autonomy.LoopTermination
	SetStreamCallback(cb execution.StreamCallback)
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
	m.setStage("model", m.cfg.ActiveModelName(), stageWaiting)

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
	m.beginOperation(OpAutonomous)
	m.agentLabel = ""
	m.startShimmer("", "autonomy apply")
	ctx := m.operationContext()
	return func() tea.Msg {
		term, err := m.autonomousDriver.ResumeApprove(ctx)
		return autonomousRunMsg{term: term, err: err}
	}
}

// resumeAutonomousReject rejects the parked approval boundary. The rejection
// is a terminal human decision through the executor; no files are touched.
func (m *model) resumeAutonomousReject(reason string) tea.Cmd {
	if !m.autonomousParked() {
		return nil
	}
	m.autonomousBoundary = nil
	m.beginOperation(OpAutonomous)
	m.agentLabel = ""
	m.startShimmer("", "autonomy reject")
	ctx := m.operationContext()
	return func() tea.Msg {
		term, err := m.autonomousDriver.ResumeReject(ctx, reason)
		return autonomousRunMsg{term: term, err: err}
	}
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
	m.beginOperation(OpAutonomous)
	m.agentLabel = ""
	m.startShimmer("", "autonomy")
	ctx := m.operationContext()
	return func() tea.Msg {
		term, err := m.autonomousDriver.ResumeClarify(ctx, target)
		return autonomousRunMsg{term: term, err: err}
	}
}

// abortAutonomousRun aborts a parked driver run. It is the only UI path to
// cancel a parked run; an in-flight run is cancelled via its context.
func (m *model) abortAutonomousRun(reason string) tea.Cmd {
	if !m.autonomousParked() {
		return nil
	}
	m.autonomousBoundary = nil
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
	if msg.err != nil {
		m.autonomousBoundary = nil
		m.finalizeOperation(OpOutcomeFailure, msg.err)
		m.push(roleError, "[autonomous] "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
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
	return nil
}

// authorizeAutonomousApproval issues a MutationAuthorization over the parked
// approval boundary's target files and attaches it to the executor the driver
// shares, so ResumeApprove applies the held patch under governance.
func (m *model) authorizeAutonomousApproval() error {
	if m.executor == nil || m.authEngine == nil {
		return nil
	}
	// Ensure workflow is in Building/Repairing state for authorization.
	// Autonomous execution from /model mode starts in Planning; we must transition.
	if err := m.transitionToBuilding(); err != nil {
		return fmt.Errorf("workflow transition to building failed: %w", err)
	}
	b := m.autonomousBoundary
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
