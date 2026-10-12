package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/classifier"
	intentdomain "github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
	proposaltui "github.com/PizenLabs/izen/internal/ui/tui"
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
		// The driver bridge falls back to the single-shot runtime path even
		// when nothing is wired: provenance binding must happen BEFORE the
		// runtime seam so an unauthorized harness fallback still surfaces the
		// original "execution runtime not wired" admission error.
		if m.autonomy != nil && m.autonomy.Authority(autonomy.RequiredCapabilities(autonomy.IntentModification)) {
			m.bindScopeProvenance(intentdomain.ScopeDynamic)
		}
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
	// ResumeWithProposal routes a human-selected DecisionSurface recovery
	// intent (rescope_bounded_patch / retry_with_explicit_budget / repair_first
	// / inspect / cancel) back to the runtime. The intent is a plain string so
	// this projection never imports the runtime autonomy package.
	ResumeWithProposal(ctx context.Context, intent string) (*autonomy.LoopTermination, error)
	// ResumeAdaptiveProposal routes the human's accepted decisions for an
	// adaptive proposal back to the runtime. The decisions are the transport
	// type owned by the loop contract package, so this projection never imports
	// the runtime autonomy package.
	ResumeAdaptiveProposal(ctx context.Context, fingerprint string, decisions []autonomy.ProposalDecision) (*autonomy.LoopTermination, error)
	Abort(reason string) (*autonomy.LoopTermination, error)
	State() autonomy.RuntimeState
	Boundary() *autonomy.HumanBoundary
	Termination() *autonomy.LoopTermination
	SetStreamCallback(cb execution.StreamCallback)
	AggregatedUsage() (input, output int, known bool)
	// SetScope hands the runtime the human directive that authorized this run
	// ("$prompt" / "$hot"). The driver derives the behavioral completion gate's
	// capability vector from it, so without it a `$prompt` run executes under
	// read-only authority and that gate can never observe the workspace it is
	// being asked to prove. The UI binds the directive per input, so it cannot
	// be supplied once at composition time.
	SetScope(scope string)
	// RunID is the runtime's stable identity for the current execution run. It
	// is what a human is told when asked "which run is parked?" — a parked
	// boundary is only actionable if the operator can name it.
	RunID() string
}

// autonomousRunMsg carries a driver Run/Resume/Abort outcome back into the
// Bubble Tea event loop. A nil term with a non-nil Boundary means the run
// parked at a human boundary (approval/clarify/inform).
type autonomousRunMsg struct {
	term *autonomy.LoopTermination
	err  error
}

var _ tea.Msg = autonomousRunMsg{}

// driveAutonomy executes one driver operation and routes its outcome into the
// Bubble Tea event loop through the terminal-event contract (see terminal.go):
// an unrecoverable error becomes a strongly-typed TerminalExecutionMsg, while
// every other outcome (parked boundary, terminal loop state, or a recoverable
// DecisionSurface selection rejection) stays an autonomousRunMsg. It is the
// single emission point for the autonomous path, so Esc/Ctrl+C and provider
// failures both converge on ONE truthful termination path.
func (m *model) driveAutonomy(run func() (*autonomy.LoopTermination, error)) tea.Msg {
	term, err := run()
	if err != nil && !isRecoverableAutonomyErr(err) {
		return newTerminalExecutionMsg("autonomy", err)
	}
	return autonomousRunMsg{term: term, err: err}
}

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
	// ── CONCURRENCY MUTEX: prevent autonomous loop while executor is active ──
	if m.executionResolving || m.agentRunning || m.streaming || m.pipelineRunning || m.shellRunning {
		m.push(roleError, "[autonomous] execution rejected: standard executor is currently running; wait for completion or cancel before starting autonomous loop")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	m.autonomousActive = true
	m.autonomousBoundary = nil
	m.autonomousSelect = 0
	m.autonomousObjective = objective
	// The authorizing directive must reach the runtime, not just the
	// presentation layer. The driver derives the behavioral completion gate's
	// capability vector from it, so a run that received an empty scope would
	// silently execute under read-only authority and could never prove a result.
	// The recorded command SURFACE wins ("$prompt" / "$build" / "$hot") so the
	// run's command axis is truthful and `/build` is not relabelled as `$prompt`.
	surface := m.executionSurface
	if surface == "" {
		surface = m.scopeProvenanceDirective()
	}
	m.autonomousDriver.SetScope(surface)
	m.beginOperation(OpAutonomous)
	m.agentLabel = ""
	m.startShimmer("", "autonomy")

	// Set up executor streaming (same mechanism as $prompt/$hot gateway path).
	// newEventChannel floors the depth at objengine.MinEventBuffer so the
	// autonomy worker never blocks on a slow render frame.
	m.execStreamCh = newEventChannel(EventChannelBuffer)
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
			return m.driveAutonomy(func() (*autonomy.LoopTermination, error) {
				return m.autonomousDriver.Run(ctx, objective)
			})
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
		// ── Guard rejection: dismiss modal immediately, abort session ────
		// The workflow guard rejected the planning → building transition
		// (e.g. "no authorized plan or micro-plan"). The awaiting_human
		// modal must close on the FIRST failure — not stack duplicate error
		// lines on every subsequent Alt+A / Enter. Transition to Failed/
		// Aborted and clear the parked boundary so the next keypress is a
		// no-op (duplicate ApproveMsg prevention).
		if strings.Contains(err.Error(), "guard rejected") {
			m.autonomousBoundary = nil
			m.resolveApprovalState()
			m.clearAutonomousRun()
			m.autonomousActive = false
			if m.orch != nil {
				if ferr := m.orch.Fail(classifier.FailureUnknownClass); ferr != nil {
					m.appendSystemError(fmt.Errorf("orchestrator fail transition rejected: %w", ferr))
					m.logActivity("[autonomous] orch.Fail rejected: %v", ferr)
				}
			} else if m.workflowSM != nil {
				if ferr := m.workflowSM.SendEvent(workflow.EventFailureIdentified, workflow.TransitionContext{FailureClass: classifier.FailureUnknownClass}); ferr != nil {
					m.appendSystemError(fmt.Errorf("workflow state machine rejected failure event: %w", ferr))
					m.logActivity("[autonomous] workflow SendEvent rejected: %v", ferr)
					m.resetStreamingState()
				}
			}
			m.push(roleError, "[autonomous] guard rejected transition — run aborted")
			m.push(roleSystem, infoStyle.Render("Interrupted."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		// The refusal has already been reported and the gate closed by
		// convergeAutonomousApproval; a second line here would only stack noise
		// on a boundary that no longer exists.
		if m.autonomousBoundary != nil {
			m.push(roleError, "[autonomous] authorization: "+err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
		}
		return nil
	}
	m.autonomousBoundary = nil
	m.beginAutonomousResume("autonomy apply")
	ctx := m.operationContext()
	return m.autonomousResumeCmds(func() tea.Msg {
		return m.driveAutonomy(func() (*autonomy.LoopTermination, error) {
			return m.autonomousDriver.ResumeApprove(ctx)
		})
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
		return m.driveAutonomy(func() (*autonomy.LoopTermination, error) {
			return m.autonomousDriver.ResumeReject(ctx, reason)
		})
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
		return m.driveAutonomy(func() (*autonomy.LoopTermination, error) {
			return m.autonomousDriver.ResumeClarify(ctx, target)
		})
	})
}

// resumeAutonomousProposal resumes a parked ZERO-TOKEN DecisionSurface with the
// selected recovery intent (rescope_bounded_patch / retry_with_explicit_budget
// / repair_first / inspect / cancel). The intent is a pure string routed to
// Driver.ResumeWithProposal; the runtime creates a NEW execution contract for a
// recovery and re-runs preflight. Zero files are touched by this call itself.
func (m *model) resumeAutonomousProposal(intent string) tea.Cmd {
	if !m.autonomousParked() {
		return nil
	}
	m.autonomousBoundary = nil
	m.proposalTUI = nil
	m.beginAutonomousResume("autonomy recovery")
	ctx := m.operationContext()
	return m.autonomousResumeCmds(func() tea.Msg {
		return m.driveAutonomy(func() (*autonomy.LoopTermination, error) {
			return m.autonomousDriver.ResumeWithProposal(ctx, intent)
		})
	})
}

// renderAutonomousAdaptiveProposalBoundary announces the parked adaptive
// proposal and initializes the answer state. The interactive card is rendered
// by renderAutonomousBoundaryBlock; this records the status lines.
func (m *model) renderAutonomousAdaptiveProposalBoundary(b *autonomy.HumanBoundary) {
	if b == nil {
		return
	}
	if m.adaptiveAnswers == nil {
		m.adaptiveAnswers = map[string][]string{}
	}
	m.adaptiveEditing = ""
	m.adaptiveDraft = ""
	m.adaptiveFocus = 0
	m.push(roleStatus, fmt.Sprintf(
		"%s ADAPTIVE PROPOSAL — revision %d",
		boldSapphireStyle.Render(Icon.Blueprint), b.ProposalRevision))
	if strings.TrimSpace(b.ProposalSummary) != "" {
		m.push(roleSystem, infoStyle.Render("  Recommended: "+b.ProposalSummary))
	}
	m.push(roleSystem, mutedStyle.Render("  "+b.Reason))
	if adaptiveFreeFormAvailable(b) {
		m.push(roleSystem, infoStyle.Render("  Select options with 1–9 · i free-form answer · Enter submit · Esc cancel"))
	} else {
		m.push(roleSystem, infoStyle.Render("  Select options with 1–9 · Enter submit · Esc cancel"))
	}
}

// resumeAutonomousAdaptiveProposal submits the human's accepted decisions to
// the runtime. The UI owns no proposal state: it collects the selected option
// ids and the free-form text and routes them through
// Driver.ResumeAdaptiveProposal, which validates them against the parked
// revision, records them durably, and compiles them into the existing execution
// contract.
func (m *model) resumeAutonomousAdaptiveProposal() tea.Cmd {
	b := m.autonomousBoundary
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		return nil
	}
	var decisions []autonomy.ProposalDecision
	for _, q := range b.ProposalQuestions {
		// A free-form answer is submitted through the SAME authoritative
		// surface as an option answer. A question that carries both sends the
		// free-form text, which is what the human actually typed.
		if free := strings.TrimSpace(m.adaptiveFreeFormFor(q.ID)); free != "" {
			decisions = append(decisions, autonomy.ProposalDecision{
				QuestionID: q.ID,
				FreeForm:   free,
			})
			continue
		}
		sel := m.adaptiveAnswers[q.ID]
		if len(sel) == 0 {
			continue
		}
		if q.Kind == autonomy.HumanProposalQuestionMulti {
			decisions = append(decisions, autonomy.ProposalDecision{
				QuestionID: q.ID,
				OptionIDs:  append([]string(nil), sel...),
			})
			continue
		}
		decisions = append(decisions, autonomy.ProposalDecision{
			QuestionID: q.ID,
			OptionID:   sel[0],
		})
	}
	// Input validation happens HERE, before the runtime is invoked: a
	// submission that answers nothing is not a decision, and routing it would
	// ask the authoritative path to reject a message the UI already knows is
	// empty while telling the human nothing.
	if len(decisions) == 0 {
		m.push(roleError, "[autonomous] no decision was made — answer at least one question before submitting")
		m.refreshViewportContent()
		m.followTail()
		return nil
	}
	m.autonomousBoundary = nil
	m.adaptiveAnswers = nil
	m.adaptiveFreeform = nil
	m.adaptiveEditing = ""
	m.adaptiveDraft = ""
	m.adaptiveFocus = 0
	m.beginAutonomousResume("autonomy proposal")
	ctx := m.operationContext()
	return m.autonomousResumeCmds(func() tea.Msg {
		return m.driveAutonomy(func() (*autonomy.LoopTermination, error) {
			return m.autonomousDriver.ResumeAdaptiveProposal(ctx, b.ProposalFingerprint, decisions)
		})
	})
}

// ── free-form answer entry ──────────────────────────────────────────────────

// adaptiveQuestionAt resolves the focused question of a parked adaptive proposal
// boundary, or nil when there is nothing to focus.
func (m *model) adaptiveQuestionAt() *autonomy.HumanProposalQuestion {
	b := m.autonomousBoundary
	if b == nil || len(b.ProposalQuestions) == 0 {
		return nil
	}
	idx := m.adaptiveFocus
	if idx < 0 || idx >= len(b.ProposalQuestions) {
		idx = 0
	}
	return &b.ProposalQuestions[idx]
}

// adaptiveFocusQuestion moves the keyboard cursor between the questions that
// accept an answer, so the free-form editor always targets a question the human
// can see.
func (m *model) adaptiveFocusQuestion(delta int) {
	b := m.autonomousBoundary
	if b == nil || len(b.ProposalQuestions) == 0 {
		return
	}
	m.adaptiveFocus += delta
	if m.adaptiveFocus < 0 {
		m.adaptiveFocus = 0
	}
	if max := len(b.ProposalQuestions) - 1; m.adaptiveFocus > max {
		m.adaptiveFocus = max
	}
}

// startAdaptiveFreeForm opens the free-form editor for the focused question.
// A question that neither declares free-form nor allows it is REFUSED rather
// than silently accepting text the runtime would reject on submit.
func (m *model) startAdaptiveFreeForm() bool {
	q := m.adaptiveQuestionAt()
	if q == nil || (!q.AllowFreeForm && q.Kind != autonomy.HumanProposalQuestionFreeForm) {
		return false
	}
	m.adaptiveEditing = q.ID
	m.adaptiveDraft = m.adaptiveFreeFormFor(q.ID)
	return true
}

// commitAdaptiveFreeForm adopts the draft as the human's answer for the focused
// question and leaves edit mode. It is still only presentation state: the
// runtime re-validates it against the parked question on submit.
func (m *model) commitAdaptiveFreeForm() bool {
	if m.adaptiveEditing == "" {
		return false
	}
	id := m.adaptiveEditing
	if text := strings.TrimSpace(m.adaptiveDraft); text != "" {
		if m.adaptiveFreeform == nil {
			m.adaptiveFreeform = map[string]string{}
		}
		m.adaptiveFreeform[id] = text
		// A free-form answer replaces any option selection for the same
		// question: they are two spellings of one decision.
		delete(m.adaptiveAnswers, id)
	}
	m.adaptiveEditing = ""
	m.adaptiveDraft = ""
	return true
}

// cancelAdaptiveFreeForm discards the draft. A half-typed answer that was never
// committed must not reach the runtime.
func (m *model) cancelAdaptiveFreeForm() {
	m.adaptiveEditing = ""
	m.adaptiveDraft = ""
}

// adaptiveFreeFormFor returns the committed free-form answer for a question.
func (m *model) adaptiveFreeFormFor(questionID string) string {
	if m.adaptiveFreeform == nil {
		return ""
	}
	return m.adaptiveFreeform[questionID]
}

// adaptiveFreeFormAvailable reports whether the parked proposal has a question
// that accepts free-form input, so the rendered hint only promises a key that
// actually does something.
func adaptiveFreeFormAvailable(b *autonomy.HumanBoundary) bool {
	if b == nil {
		return false
	}
	for _, q := range b.ProposalQuestions {
		if q.AllowFreeForm || q.Kind == autonomy.HumanProposalQuestionFreeForm {
			return true
		}
	}
	return false
}

// appendAdaptiveDraft appends printable text to the free-form draft and reports
// whether the key was consumed. Explicit typed characters are appended one at a
// time; a pasted multi-rune block is appended in order. Control sequences and
// mouse fragments are rejected so they can never reach a draft that becomes a
// human decision.
func appendAdaptiveDraft(m *model, msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		if len(msg.Runes) == 0 {
			return false
		}
	case tea.KeyTab:
		if len(msg.Runes) == 0 {
			msg.Runes = []rune{'\t'}
		}
	default:
		if msg.Type != tea.KeyRunes || len(msg.Runes) == 0 {
			return false
		}
	}
	for _, r := range msg.Runes {
		if r < 32 && r != '\t' {
			return false
		}
	}
	if isControlSequence(msg.Runes) || IsSGRMouseFragmentRunes(msg.Runes) {
		return false
	}
	// Bound the draft: it travels to the model as part of a decision, and an
	// unbounded paste is not a human decision, it is an injection vector.
	const maxDraft = 4000
	if len([]rune(m.adaptiveDraft))+len(msg.Runes) > maxDraft {
		m.push(roleError, "[autonomous] free-form answer is too long (4000 characters)")
		return true
	}
	m.adaptiveDraft += string(msg.Runes)
	return true
}

// adaptiveOptionRef maps one flattened option index to its question and option.
type adaptiveOptionRef struct {
	QuestionID string
	OptionID   string
	Multi      bool
}

// adaptiveOptions flattens the proposal's selectable options into a stable
// index order shared by the renderer and the key handler.
func adaptiveOptions(b *autonomy.HumanBoundary) []adaptiveOptionRef {
	if b == nil {
		return nil
	}
	var out []adaptiveOptionRef
	for _, q := range b.ProposalQuestions {
		for _, o := range q.Options {
			out = append(out, adaptiveOptionRef{
				QuestionID: q.ID,
				OptionID:   o.ID,
				Multi:      q.Kind == autonomy.HumanProposalQuestionMulti,
			})
		}
	}
	return out
}

// selectAdaptiveOption applies a flattened option index to the answer state.
// Single-choice replaces; multi-select toggles.
func (m *model) selectAdaptiveOption(index int) {
	refs := adaptiveOptions(m.autonomousBoundary)
	if index < 0 || index >= len(refs) {
		return
	}
	if m.adaptiveAnswers == nil {
		m.adaptiveAnswers = map[string][]string{}
	}
	ref := refs[index]
	cur := m.adaptiveAnswers[ref.QuestionID]
	if !ref.Multi {
		m.adaptiveAnswers[ref.QuestionID] = []string{ref.OptionID}
		return
	}
	// Toggle.
	for i, id := range cur {
		if id == ref.OptionID {
			m.adaptiveAnswers[ref.QuestionID] = append(cur[:i], cur[i+1:]...)
			return
		}
	}
	m.adaptiveAnswers[ref.QuestionID] = append(cur, ref.OptionID)
}

// adaptiveCard is the presentation-only view of the adaptive proposal the human
// is answering: the selected options, the committed free-form answers, which
// question the keyboard cursor is on, and the uncommitted draft being typed.
type adaptiveCard struct {
	answers  map[string][]string
	freeform map[string]string
	focus    int
	editing  string
	draft    string
}

// renderAdaptiveProposalCard renders the adaptive proposal card: objective,
// recommended strategy, material trade-offs/risks, and the questions with their
// selectable options and any free-form answer the human has typed. It is a pure
// function of the boundary and the view, and it draws nothing the runtime
// consults.
func renderAdaptiveProposalCard(b *autonomy.HumanBoundary, view adaptiveCard, width int) string {
	var sb strings.Builder
	sb.WriteString(permissionTitleStyle.Render(Icon.Blueprint + " ADAPTIVE PROPOSAL"))
	sb.WriteString("\n\n")
	if strings.TrimSpace(b.ProposalObjective) != "" {
		sb.WriteString(permissionDescStyle.Render("Objective:"))
		sb.WriteString(" " + infoStyle.Render(b.ProposalObjective) + "\n")
	}
	sb.WriteString(permissionDescStyle.Render("Revision:"))
	fmt.Fprintf(&sb, " %d\n", b.ProposalRevision)

	for _, s := range b.ProposalStrategies {
		if !s.Recommended {
			continue
		}
		sb.WriteString("\n")
		sb.WriteString(permissionDescStyle.Render("Recommended:"))
		sb.WriteString(" " + boldTextStyle.Render(s.Title) + "\n")
		if strings.TrimSpace(s.Summary) != "" {
			sb.WriteString("  " + mutedStyle.Render(s.Summary) + "\n")
		}
		for _, tr := range s.Tradeoffs {
			sb.WriteString("  " + mutedStyle.Render("trade-off: "+tr.Description+" (+"+tr.Benefit+" / -"+tr.Cost+")") + "\n")
		}
		for _, r := range s.Risks {
			sb.WriteString("  " + mutedStyle.Render("risk ["+r.Severity+"]: "+r.Description) + "\n")
		}
	}

	index := 0
	for qi, q := range b.ProposalQuestions {
		sb.WriteString("\n")
		marker := ""
		if q.Required {
			marker = " *"
		}
		focus := ""
		if qi == view.focus && len(b.ProposalQuestions) > 1 {
			focus = " " + mutedStyle.Render("◀")
		}
		sb.WriteString(permissionDescStyle.Render(q.Prompt) + marker + focus + "\n")
		for _, o := range q.Options {
			index++
			selected := false
			for _, id := range view.answers[q.ID] {
				if id == o.ID {
					selected = true
				}
			}
			var prefix string
			label := mutedStyle.Render(o.Label)
			if selected {
				prefix = "  " + permissionKeyStyle.Render("[x]") + " "
				label = boldTextStyle.Render(o.Label)
			} else {
				prefix = fmt.Sprintf("  %s ", mutedStyle.Render("["+strconv.Itoa(index)+"]"))
			}
			rec := ""
			if o.ID == q.Recommended {
				rec = " " + mutedStyle.Render("(recommended)")
			}
			sb.WriteString(prefix + label + rec + "\n")
		}
		if free := strings.TrimSpace(view.freeform[q.ID]); free != "" {
			sb.WriteString("  " + permissionKeyStyle.Render("[free-form]") + " " + boldTextStyle.Render(free) + "\n")
		}
	}

	sb.WriteString("\n")
	sb.WriteString(" " + boundRule(width, permissionBoxStyle, 2) + "\n")
	hint := "1–9 select/toggle · Enter submit · Esc cancel"
	if len(b.ProposalQuestions) > 1 {
		hint = "↑/↓ question · " + hint
	}
	if adaptiveFreeFormAvailable(b) {
		hint += " · i free-form answer"
	}
	sb.WriteString(" " + mutedStyle.Render(hint) + "\n")
	if view.editing != "" {
		sb.WriteString(" " + infoStyle.Render("free-form answer for "+view.editing+":") + "\n")
		sb.WriteString("  " + boldTextStyle.Render(view.draft) + "\n")
		sb.WriteString(" " + mutedStyle.Render("Enter accept this text · Esc discard the draft") + "\n")
	}
	return boundBox(permissionBoxStyle, width).Render(sb.String())
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
		if strings.Contains(err.Error(), "guard rejected") {
			m.autonomousBoundary = nil
			m.resolveApprovalState()
			m.clearAutonomousRun()
			m.autonomousActive = false
			if m.orch != nil {
				if ferr := m.orch.Fail(classifier.FailureUnknownClass); ferr != nil {
					m.appendSystemError(fmt.Errorf("orchestrator fail transition rejected: %w", ferr))
					m.logActivity("[autonomous] orch.Fail rejected: %v", ferr)
				}
			} else if m.workflowSM != nil {
				if ferr := m.workflowSM.SendEvent(workflow.EventFailureIdentified, workflow.TransitionContext{FailureClass: classifier.FailureUnknownClass}); ferr != nil {
					m.appendSystemError(fmt.Errorf("workflow state machine rejected failure event: %w", ferr))
					m.logActivity("[autonomous] workflow SendEvent rejected: %v", ferr)
					m.resetStreamingState()
				}
			}
			m.push(roleError, "[autonomous] guard rejected transition — run aborted")
			m.push(roleSystem, infoStyle.Render("Interrupted."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		// convergeAutonomousAuthorization already stated the refusal and closed
		// the gate; a duplicate line here would only stack noise.
		if m.autonomousBoundary != nil {
			m.push(roleError, "[autonomous] proposal authorization: "+err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
		}
		return nil
	}
	m.autonomousBoundary = nil
	m.beginAutonomousResume("autonomy dag")
	ctx := m.operationContext()
	// DAG_EXECUTING runs every sub-task inside this non-blocking tea.Cmd
	// goroutine; the batched spin.Tick loops keep the event loop rendering
	// (spinner frames + shimmer sweep) for the whole transaction.
	return m.autonomousResumeCmds(func() tea.Msg {
		return m.driveAutonomy(func() (*autonomy.LoopTermination, error) {
			return m.autonomousDriver.ResumeApproveProposal(ctx)
		})
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
		return m.driveAutonomy(func() (*autonomy.LoopTermination, error) {
			return m.autonomousDriver.ResumeRejectProposal(ctx, reason)
		})
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

// stopAutonomousDriver schedules a driver abort for an active autonomous run
// without finalizing the operation: the driver's terminal autonomousRunMsg
// remains the canonical cleanup path (Phase 2 state-drift fix). It returns
// the abort command when a driver is attached, or nil when there is nothing
// to stop. It never hand-sets presentation state.
func (m *model) stopAutonomousDriver(reason string) tea.Cmd {
	if m == nil || m.autonomousDriver == nil {
		return nil
	}
	driver := m.autonomousDriver
	return func() tea.Msg {
		term, err := driver.Abort(reason + " interrupt")
		return autonomousRunMsg{term: term, err: err}
	}
}

// autonomousUsageDeltaCmd returns the command that folds this boundary's NEW
// provider usage into the session baseline.
//
// The driver's AggregatedUsage is CUMULATIVE across a run's parks and resumes
// (it is reset only when a fresh Run starts). The UI commits usage into the
// session baseline, so committing the raw cumulative total at every boundary —
// the park AND the terminal — adds the same provider calls to the baseline more
// than once. That is the ~2× discrepancy between the footer and the provider's
// real billing.
//
// This commits only the delta since the last commit for the SAME run and
// advances the per-run baseline. A boundary that reports nothing new returns
// nil; a run whose aggregate is UNKNOWN commits nothing (a missing measurement
// must never become a zero). The baseline advances only for the usage actually
// dispatched, so a command that is never executed cannot consume the delta.
func (m *model) autonomousUsageDeltaCmd(aggIn, aggOut int, known bool) tea.Cmd {
	if m == nil || !known {
		return nil
	}
	runID := ""
	if m.autonomousDriver != nil {
		runID = m.autonomousDriver.RunID()
	}
	if runID != m.usageAggRun {
		// A new run's aggregate starts from zero, so the previous run's
		// committed baseline must not be subtracted from it.
		m.usageAggRun = runID
		m.usageAggIn, m.usageAggOut = 0, 0
	}
	deltaIn := aggIn - m.usageAggIn
	deltaOut := aggOut - m.usageAggOut
	if deltaIn < 0 {
		deltaIn = 0
	}
	if deltaOut < 0 {
		deltaOut = 0
	}
	if deltaIn == 0 && deltaOut == 0 {
		return nil
	}
	m.usageAggIn, m.usageAggOut = aggIn, aggOut
	return m.tokenUsageCmdKnown(deltaIn, deltaOut, true)
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
		return m.autonomousUsageDeltaCmd(aggIn, aggOut, aggKnown)
	}

	if msg.err != nil {
		// TASK 2: Preserve DecisionSurface on invalid proposal intent — do NOT drop to empty prompt bar.
		// The driver republished the surface; the UI must re-render it and stay responsive.
		if strings.Contains(msg.err.Error(), "invalid proposal intent") {
			m.autonomousActive = false
			// Restore the parked boundary from the driver if we cleared it.
			if m.autonomousBoundary == nil && m.autonomousDriver != nil {
				if b := m.autonomousDriver.Boundary(); b != nil {
					m.autonomousBoundary = b
				}
			}
			// If we still have a proposal boundary, re-render the surface without finalizing.
			if b := m.autonomousBoundary; b != nil && b.Action == autonomy.HumanBoundaryProposal {
				// Re-create proposal TUI if it was cleared by resumeAutonomousProposal.
				if m.proposalTUI == nil {
					m.proposalTUI = proposaltui.NewProposalModel(proposalSurfaceFromBoundary(b))
					m.proposalTUI.Reset()
				}
				m.push(roleSystem, infoStyle.Render("⚠ Invalid option selected, please choose again"))
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return nil
			}
			// Fallback: treat as warning, not hard failure — keep awaiting_human active.
			m.push(roleSystem, infoStyle.Render("⚠ Invalid option selected, please choose again"))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			// Re-publish driver boundary if available.
			if m.autonomousDriver != nil {
				if b := m.autonomousDriver.Boundary(); b != nil {
					m.autonomousBoundary = b
					m.renderAutonomousProposalBoundary(b)
					m.refreshViewportContent()
					m.Viewport.GotoBottom()
				}
			}
			return nil
		}
		m.autonomousBoundary = nil
		m.finalizeOperation(OpOutcomeFailure, msg.err)
		// A terminal driver failure releases the workflow phase through the ONE
		// sanctioned unwind, so the BUILDING header cannot survive the loop. The
		// unwind is idempotent against an already-terminal machine.
		m.unwindTerminalExecution()
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
			case autonomy.HumanBoundaryProposal:
				// A ZERO-TOKEN DecisionSurface park is a LIVE human decision:
				// render the interactive recovery surface (bounded patch /
				// explicit budget / inspect / cancel), NEVER a static pause.
				// The typed options cross on the boundary itself — the runtime
				// never relies on log strings for a human decision.
				m.finalizeOperation(OpOutcomeAmbiguous, nil)
				m.renderAutonomousProposalBoundary(b)
			case autonomy.HumanBoundaryAdaptiveProposal:
				// An adaptive proposal park is a LIVE human decision: render the
				// interpreted objective, the recommended strategy, its material
				// trade-offs, and the questions that affect execution.
				m.finalizeOperation(OpOutcomeAmbiguous, nil)
				m.renderAutonomousAdaptiveProposalBoundary(b)
			default:
				m.finalizeOperation(OpOutcomeFailure, nil)
				m.renderAutonomousInformBoundary(b)
			}
			// Only a boundary that actually asks a human for a DECISION arms the
			// pending-approval workflow override. An informational park has no
			// resume decision, so freezing the workflow as "awaiting
			// authorization" would report a pause as a permission request.
			if b.Resumable {
				m.enterApprovalState()
			} else {
				m.resolveApprovalState()
			}
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
	switch msg.term.State {
	case autonomy.RuntimeCompleted:
		m.finalizeOperation(OpOutcomeSuccess, nil)
		// Terminal for this run. The unwind is what stops the lifecycle reading
		// `building` after the change has already landed: a PROVEN completion is
		// not still executing, and reporting it as such leaves the next prompt
		// judged against a run that no longer exists.
		m.unwindTerminalExecution()
		m.push(roleSystem, infoStyle.Render("[autonomous] "+greenStyle.Render("completed")+" — "+msg.term.Reason))
	case autonomy.RuntimeUnsubstantiated:
		// ── PHASE 14: THE OBJECTIVE WAS NOT PROVEN ─────────────────────
		// Neither success nor failure. The run was legal, the workspace was not
		// corrupted, and nothing was proven. Reporting it as "aborted" would
		// claim a failure that did not happen and invite a retry that would
		// hit the same wall; reporting it as "completed" would be the false
		// completion this phase exists to prevent.
		m.finalizeOperation(OpOutcomeAmbiguous, nil)
		// Terminal for this run (see RuntimeUnsubstantiated: a terminal position).
		// The unwind releases the phase and the human boundary together.
		m.unwindTerminalExecution()
		m.push(roleSystem, infoStyle.Render("[autonomous] "+orangeStyle.Render("objective was not proven")+" — "+msg.term.Reason))
		m.push(roleSystem, infoStyle.Render(ObjectiveUnprovenMessage("")))
	default:
		m.finalizeOperation(OpOutcomeFailure, nil)
		// The run reached a TERMINAL state. The unwind is the abort transition: it
		// releases the live execution phase AND the human boundary the terminated
		// run was parked at. Leaving `awaiting_authorization` behind would report a
		// finished run as waiting for a decision and block the next execution for an
		// answer nobody can now give.
		m.unwindTerminalExecution()
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
//
// The token is bound to the boundary's MutationCandidate identity AND to the
// content digest the human's MUTATION REVIEW was rendered from. Authorization is
// therefore a statement about ONE concrete change: a token opened for one
// candidate can never apply another, a candidate replaced in place can never be
// applied under an older review, and a boundary whose candidate the execution
// authority no longer holds is refused here instead of at the mutation boundary.
//
// CONVERGENCE. Every refusal is terminal for this boundary. The parked approval
// state is dropped so the same impossible authorization can never be requested
// twice: human approval is the final authorization gate, not a retry loop around
// a proposal the runtime already knows it cannot authorize.
func (m *model) authorizeAutonomousApproval() error {
	if m.executor == nil || m.authEngine == nil {
		return nil
	}
	b := m.autonomousBoundary
	// ── CANDIDATE FRESHNESS (lineage, re-checked at the release seam) ───
	// The runtime's admission gate already refused an approval boundary whose
	// candidate was not held at park time; this is the same read taken again at
	// the moment of release, because a human answer takes unbounded time.
	if b != nil && b.Action == autonomy.HumanBoundaryApproval && !m.executor.CandidateHeld(b.PatchID) {
		m.convergeAutonomousAuthorization("mutation candidate " + b.PatchID +
			" is no longer held by the execution authority — the computation that produced it failed, " +
			"was superseded or was cancelled. No files were modified.")
		return fmt.Errorf("candidate %s is no longer executable", b.PatchID)
	}
	// ── CANDIDATE CONTENT FRESHNESS ────────────────────────────────────
	// Identity alone answers "is this the same computation?". Content answers
	// "is this the same CHANGE?". A candidate whose bytes were replaced in place
	// under a stable identity would otherwise be applied under a review a human
	// never saw, so the digest the review was rendered from is re-read here and
	// carried on the token.
	digest := ""
	if b != nil && b.Action == autonomy.HumanBoundaryApproval && (b.CandidateDigest != "" || b.CandidateID != "") {
		preview, ok := m.executor.CandidatePreview(b.PatchID)
		if !ok {
			m.convergeAutonomousAuthorization("mutation candidate " + b.PatchID +
				" could not be re-read at authorization time. No files were modified; a new mutation review is required.")
			return fmt.Errorf("candidate %s is no longer reviewable", b.PatchID)
		}
		// ── IDENTITY RE-CHECK ─────────────────────────────────────────
		// The boundary names the candidate a human reviewed. If the executor is
		// holding a different one under that handle, the authorization would be
		// issued for a computation the human never saw.
		if b.CandidateID != "" && preview.CandidateID != b.CandidateID {
			m.convergeAutonomousAuthorization("authorization invalidated: candidate " + b.PatchID +
				" is no longer the candidate that was reviewed (reviewed " + b.CandidateID +
				", held " + preview.CandidateID + "). No files were modified; a new mutation review is required.")
			return fmt.Errorf("candidate %s is not the reviewed candidate", b.PatchID)
		}
		// ── CONTENT RE-CHECK ──────────────────────────────────────────
		// Same identity, different bytes: the change the human judged is not the
		// change the runtime would apply. Both digests are shown so the difference
		// is inspectable rather than merely refused.
		if b.CandidateDigest != "" {
			if current := preview.Digest(); current != b.CandidateDigest {
				m.convergeAutonomousAuthorization("authorization invalidated: candidate " + b.PatchID +
					" changed since it was reviewed (reviewed " + shortCandidateDigest(b.CandidateDigest) +
					", now " + shortCandidateDigest(current) + "). No files were modified; a new mutation review is required.")
				return fmt.Errorf("candidate %s changed since review", b.PatchID)
			}
			digest = b.CandidateDigest
		}
	}
	// ── STAGED DAG HANDSHAKE (planning → building guard) ────────────────
	// An approved DECOMPOSITION_PROPOSAL IS an authorized plan: the staged
	// ExecutionDAG lives inside the Autonomy Loop context (StagedPlan), so it
	// must be registered with the orchestrator BEFORE the workflow transition
	// is requested — otherwise the guard rejects planning → building with
	// "no authorized plan or micro-plan" even though the human just approved
	// every sub-task.
	if m.orch != nil && b != nil && b.Action == autonomy.HumanBoundaryDecomposition && b.Proposal != nil {
		if err := m.orch.BindAuthorizedMicroPlan(context.Background(), b.Proposal); err != nil {
			return fmt.Errorf("micro-plan binding failed: %w", err)
		}
	}
	// ── SYNTHETIC MICRO-PLAN HANDSHAKE (direct $hot / single-file) ─────
	// A direct mutation proposal has no formal DAG plan. Before transitioning
	// to building, ensure the orchestrator carries an authorized plan so the
	// guard permits planning → building. This is idempotent: when a formal
	// plan or ephemeral plan is already authorized it is a no-op.
	if m.orch != nil && !m.orch.HasAuthorizedPlan() {
		switch {
		case b != nil && len(b.Targets) > 0:
			intent := "modification"
			if m.autonomousObjective != "" && m.autonomy != nil {
				intent = string(m.autonomy.Classify(m.autonomousObjective).Intent)
				if intent == "" {
					intent = "modification"
				}
			}
			scope := ""
			if m.autonomy != nil {
				scope = m.autonomy.Scope()
			}
			caps := []string{"read", "analyze", "propose", "mutate", "verify"}
			_ = m.orch.EnsureSyntheticMicroPlan(b.Targets, intent, scope, caps)
		case m.autonomousObjective != "":
			// Fallback: objective without resolved targets (clarify not yet done).
			_ = m.orch.EnsureSyntheticMicroPlan([]string{"direct-mutation"}, "modification", "", []string{"mutate"})
		default:
			_ = m.orch.InjectEphemeralPlan("$hot:direct")
		}
	}
	// Ensure workflow is in Building/Repairing state for authorization.
	// Autonomous execution from /model mode starts in Planning; we must transition.
	if err := m.transitionToBuilding(); err != nil {
		return fmt.Errorf("workflow transition to building failed: %w", err)
	}
	var targets []string
	candidateID := ""
	if b != nil {
		targets = b.Targets
		// The authorization is bound to the candidate IDENTITY THE HUMAN REVIEWED.
		// CandidateID is that identity; PatchID is the handle the executor applies
		// through. They coincide for a healthy boundary, and when they do NOT the
		// divergence is exactly the stale-authorization case — so binding the
		// reviewed identity is what makes the mismatch detectable at the mutation
		// boundary instead of silently writing the wrong bytes.
		candidateID = b.CandidateID
		if candidateID == "" {
			candidateID = b.PatchID
		}
	}
	auth, err := m.authEngine.AuthorizeBuildCandidateContent(
		targets,
		m.caps,
		m.mutationBudget,
		m.microBudget,
		false,
		true, // human-approved: the developer pressed Alt+A on the boundary
		candidateID,
		digest,
	)
	if err != nil {
		m.convergeAutonomousAuthorization("authorization refused: " + err.Error() +
			". No files were modified; start a fresh run after resolving the refusal.")
		return err
	}
	m.executor.SetAuthorization(auth)
	return nil
}

// shortCandidateDigest renders a candidate fingerprint compactly for a
// human-readable refusal. Two digests must be tellable apart; a full sha256 would
// dominate the message.
func shortCandidateDigest(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12]
}

// convergeAutonomousAuthorization is the terminal outcome of a refused
// authorization: the parked approval state is released so the operator is not
// invited to press Approve again for a mutation the runtime has already refused,
// and the truthful reason is stated.
func (m *model) convergeAutonomousAuthorization(reason string) {
	m.autonomousBoundary = nil
	m.autonomousActive = false
	m.resolveApprovalState()
	m.finalizeOperation(OpOutcomeFailure, nil)
	m.unwindTerminalExecution()
	m.push(roleError, "[autonomous] "+reason)
	m.push(roleSystem, infoStyle.Render("  The approval gate is closed. Start a fresh run (Ctrl+C to dismiss)."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
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

// renderAutonomousApprovalBoundary renders the parked boundary as a MUTATION
// REVIEW: the concrete held change a human is being asked to authorize, with the
// runtime's own evidence checklist.
//
// The title is the semantic correction the previous UI needed. "AUTONOMY
// APPROVAL" over a target list asked for a permission without showing what was
// being permitted; the runtime is not asking for permission in the abstract, it
// is asking the human to authorize THIS change to THIS target. Nothing here
// asserts that anything has been applied — the mutation row is present and
// unsatisfied, because it has not.
func (m *model) renderAutonomousApprovalBoundary(b *autonomy.HumanBoundary) {
	var sb strings.Builder
	sb.WriteString(permissionTitleStyle.Render(Icon.Warning + " MUTATION REVIEW"))
	sb.WriteString("\n")
	writeMutationReviewBody(&sb, b, m.autonomousObjective, 0)
	m.push(roleStatus, sb.String())
	m.push(roleSystem, infoStyle.Render("  Alt+A apply mutation · Alt+R reject · Ctrl+C aborts"))
	m.push(roleSystem, infoStyle.Render("  Enter/Esc mirror those only while nothing is typed — a command you are typing is never read as an authorization"))
}

// writeMutationReviewBody renders the shared body of the mutation review: the
// concrete candidate plus the runtime's evidence checklist. Every fact comes
// from the boundary — the runtime's own read of the held candidate — so the
// review and the apply cannot disagree.
//
// indent spaces every line, which lets the same body render both in the log
// (indent 0) and inside the framed boundary modal.
func writeMutationReviewBody(sb *strings.Builder, b *autonomy.HumanBoundary, objective string, indent int) {
	if b == nil {
		return
	}
	pad := strings.Repeat(" ", indent)
	row := func(label, value string) {
		if value == "" {
			return
		}
		sb.WriteString(pad + permissionDescStyle.Render(label) + " " + permissionTargetStyle.Render(value) + "\n")
	}
	targets := b.CandidateTargets
	if len(targets) == 0 {
		targets = b.Targets
	}
	row("Target:", strings.Join(targets, ", "))
	row("Operation:", b.CandidateOperation)
	if b.CandidateOperationEvidence != "" {
		row("Why:", b.CandidateOperationEvidence)
	}
	row("Objective:", objective)
	if b.CandidateContractID != "" {
		row("Contract:", b.CandidateContractID)
	}
	row("Reason:", b.Reason)

	if len(b.CandidateEvidence) > 0 {
		sb.WriteString(pad + permissionDescStyle.Render("Evidence") + "\n")
		for _, e := range b.CandidateEvidence {
			mark := Icon.Error
			if e.Satisfied {
				mark = Icon.Success
			}
			sb.WriteString(pad + "  " + mark + " " + e.Label)
			if e.Detail != "" {
				sb.WriteString(" — " + mutedStyle.Render(e.Detail))
			}
			sb.WriteString("\n")
		}
	}

	if b.CandidateDiff != "" {
		sb.WriteString(pad + permissionDescStyle.Render("Proposed change") + "\n")
		for _, line := range mutationReviewDiffLines(b.CandidateDiff, mutationReviewMaxDiffLines) {
			sb.WriteString(pad + "  " + diffLineStyle(line) + "\n")
		}
	}
	// The one sentence that must never be omitted at this boundary: the
	// authorization has not happened, so nothing has happened.
	sb.WriteString(pad + "  " + orangeStyle.Render("Mutation has NOT occurred.") + "\n")
}

// mutationReviewMaxDiffLines bounds how much of the compiled diff the review
// card inlines. A full diff belongs behind the explicit [D] view; the card shows
// enough for the human to recognise the change and states plainly that the
// remainder exists.
const mutationReviewMaxDiffLines = 12

// mutationReviewDiffLines returns the first n non-empty diff lines plus an
// explicit truncation marker naming how much was elided. It never silently drops
// the rest: a review that hides the tail of a diff is still a partial review.
func mutationReviewDiffLines(diff string, n int) []string {
	all := make([]string, 0, n+1)
	elided := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(all) < n {
			all = append(all, line)
			continue
		}
		elided++
	}
	if elided > 0 {
		all = append(all, fmt.Sprintf("… %d more diff line(s) — press [D] for the full diff", elided))
	}
	return all
}

// diffLineStyle colourises one diff line by its kind so a mutation review reads
// as a change rather than as a wall of text.
func diffLineStyle(line string) string {
	switch {
	case strings.HasPrefix(line, "+"):
		return greenStyle.Render(line)
	case strings.HasPrefix(line, "-"):
		return redStyle.Render(line)
	default:
		return mutedStyle.Render(line)
	}
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

// renderAutonomousProposalBoundary renders the parked ZERO-TOKEN DecisionSurface
// as an interactive recovery menu. The typed options cross on the boundary
// (HumanBoundary.ProposalOptions) — never from log strings. The pure
// presentation ProposalModel owns navigation; a selection routes one intent to
// Driver.ResumeWithProposal.
func (m *model) renderAutonomousProposalBoundary(b *autonomy.HumanBoundary) {
	if len(b.ProposalOptions) == 0 {
		// Defense-in-depth deadlock guard: a parked DecisionSurface boundary
		// with no selectable options is a deadlock by construction. Fall back
		// to the inform card rather than stranding the run invisibly.
		m.renderAutonomousInformBoundary(b)
		return
	}
	m.proposalTUI = proposaltui.NewProposalModel(proposalSurfaceFromBoundary(b))
	m.proposalTUI.Reset()
	m.push(roleStatus, fmt.Sprintf(
		"%s PREFLIGHT RECOVERY — %s requires a decision",
		boldSapphireStyle.Render(Icon.Blueprint), b.Targets))
	m.push(roleSystem, mutedStyle.Render("  "+b.Reason))
	m.push(roleSystem, infoStyle.Render("  ↑/↓ navigate · Enter select · Esc cancel"))
}

// proposalSurfaceFromBoundary projects the typed recovery options + surface
// facts the runtime boundary carries onto the pure-presentation DecisionSurface
// the modal renders. It is a lossless scalar projection — no runtime import, no
// log parsing.
func proposalSurfaceFromBoundary(b *autonomy.HumanBoundary) proposaltui.DecisionSurface {
	s := proposaltui.DecisionSurface{Options: make([]proposaltui.ProposalOption, 0, len(b.ProposalOptions))}
	if len(b.Targets) > 0 {
		s.Target = b.Targets[0]
	}
	if b.Proposal != nil {
		s.Target = b.Proposal.Target
	}
	s.ASTStatus = b.SurfaceASTStatus
	s.FailureCategory = b.SurfaceFailureCategory
	s.EstimatedTokens = b.SurfaceEstimatedTokens
	s.CurrentBudget = b.SurfaceCurrentBudget
	s.Reason = b.Reason
	for _, opt := range b.ProposalOptions {
		s.Options = append(s.Options, proposaltui.ProposalOption{
			ID:          opt.ID,
			Label:       opt.Label,
			Description: opt.Description,
			Intent:      proposaltui.ProposalIntent(opt.Intent),
		})
	}
	return s
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
//
// Every interior element (rules, key hints, target paths) is sized from the
// live viewport through boundWidth/boundInner, and the frame itself is drawn
// with boundBox, so a long target path or a long sub-task description re-flows
// inside the card instead of shoving the right border off-screen.
func renderDecompositionProposalBlock(dag *planner.ExecutionDAG, width int) string {
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
			mutedStyle.Render(truncateDisplay(st.Description, boundInner(width, decompositionBoxStyle)-24)),
			st.EstimatedTokens)
	}
	total := dag.TotalEstimatedTokens()
	fmt.Fprintf(&sb, "%s ~%d tok total · budget ≤%d tok/sub-task\n",
		permissionDescStyle.Render("Budget:"), total, dag.Budget())
	sb.WriteString(" " + boundRule(width, decompositionBoxStyle, 2) + "\n")
	sb.WriteString(" " + fmt.Sprintf("%s Authorize & Run DAG   %s Cancel",
		decompositionKeyStyle.Render("[Enter]"), decompositionKeyStyle.Render("[Esc]")) + "\n")

	return boundBox(decompositionBoxStyle, width).Render(sb.String())
}

// renderAutonomousBoundaryBlock renders the parked driver boundary as an
// interactive card. It is the ONLY human decision surface for a parked run.
func (m *model) renderAutonomousBoundaryBlock(width int) string {
	b := m.autonomousBoundary
	if b == nil {
		return ""
	}

	var sb strings.Builder
	switch b.Action {
	case autonomy.HumanBoundaryApproval:
		// MUTATION REVIEW: the human is authorizing a concrete change, so the
		// card shows the concrete change. Nothing is asserted as applied.
		sb.WriteString(permissionTitleStyle.Render(Icon.Warning + " MUTATION REVIEW"))
		sb.WriteString("\n\n")
		writeMutationReviewBody(&sb, b, m.autonomousObjective, 1)
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
		return boundBox(permissionBoxStyle, width).Render(sb.String())
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
		sb.WriteString(" " + boundRule(width, permissionBoxStyle, 2) + "\n")
		sb.WriteString(" " + fmt.Sprintf("%s Authorize & Run DAG   %s Cancel",
			decompositionKeyStyle.Render("[Enter]"), decompositionKeyStyle.Render("[Esc]")) + "\n")
		return boundBox(permissionBoxStyle, width).Render(sb.String())
	case autonomy.HumanBoundaryProposal:
		// The ZERO-TOKEN DecisionSurface recovery menu. It is a LIVE human
		// decision surface — the interactive selection model owns rendering so
		// the options are always selectable (never a static pause).
		if m.proposalTUI != nil {
			return m.proposalTUI.Render(width)
		}
		sb.WriteString(permissionTitleStyle.Render(Icon.Warning + " PREFLIGHT RECOVERY"))
		sb.WriteString("\n\n")
		sb.WriteString(permissionDescStyle.Render("Reason:"))
		sb.WriteString(" " + infoStyle.Render(b.Reason))
		sb.WriteString("\n")
		sb.WriteString(" " + boundRule(width, permissionBoxStyle, 2) + "\n")
		sb.WriteString(" " + mutedStyle.Render("↑/↓ navigate · Enter select · Esc cancel") + "\n")
		return boundBox(permissionBoxStyle, width).Render(sb.String())
	case autonomy.HumanBoundaryAdaptiveProposal:
		// The adaptive proposal: the interpreted objective, the recommended
		// strategy, its material trade-offs, and only the questions that
		// materially affect execution. Decisions route to the runtime; the UI
		// owns no proposal state.
		return renderAdaptiveProposalCard(b, adaptiveCard{
			answers:  m.adaptiveAnswers,
			freeform: m.adaptiveFreeform,
			focus:    m.adaptiveFocus,
			editing:  m.adaptiveEditing,
			draft:    m.adaptiveDraft,
		}, width)
	default:
		return ""
	}

	sb.WriteString(" " + boundRule(width, permissionBoxStyle, 2) + "\n")

	if b.Action == autonomy.HumanBoundaryApproval {
		// The verb is "apply", not "approve": approving is a permission, applying
		// is the mutation this authorization permits — and it has not happened.
		sb.WriteString(" " + mutedStyle.Render("Alt+A apply mutation · Alt+R reject · Ctrl+C abort · Enter/Esc mirror these while the input is empty") + "\n")
	} else {
		sb.WriteString(" " + mutedStyle.Render("↑/↓ navigate · Enter select · Esc cancel · Ctrl+C abort") + "\n")
	}

	return boundBox(permissionBoxStyle, width).Render(sb.String())
}
