package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── Unified Intent Gateway bridge (execution-driven runtime) ───────────────
//
// Every user action that reaches the execution path — bare text, $prompt,
// $hot — crosses the IntentGateway and produces an ExecuteRequest. The UI
// NEVER decides the execution path: it does not route by mode, it does not
// trigger a hidden /build, and it does not invoke a provider. It submits the
// request and renders the canonical runtime events + the returned result.
//
//	User Input → IntentGateway.Gate() → ExecuteRequest → RuntimeExecutor
//	                                                 → ExecutionProof + Events → UI

// gatedExecutionMsg carries the gateway interpretation plus the executor
// result for the update loop.
type gatedExecutionMsg struct {
	res *execution.ExecutionResult
	det execution.IntentResolution
	err error
}

var _ tea.Msg = gatedExecutionMsg{}

// runGatedLine routes one user action through the IntentGateway and submits
// the resulting ExecuteRequest to the RuntimeExecutor. It is the single entry
// point for execution-bearing input.
func (m *model) runGatedLine(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if m.gateway == nil || m.executor == nil {
		m.push(roleError, "execution runtime not wired")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── STRATEGY SELECTION IS UNCONDITIONAL AND SYNCHRONOUS ───────────
	// The gateway resolves the strategy BEFORE any execution begins; the
	// decision is recorded synchronously so $inspect is truthful even while
	// the async execution runs. Modes never select the path.
	req, det, gateErr := m.gateway.Gate(context.Background(), line)
	if gateErr != nil {
		// A gate failure is a terminal, non-execution outcome: release the
		// generic Enter shimmer so no stale loading claim survives it.
		m.stopShimmer()
		return func() tea.Msg { return gatedExecutionMsg{det: det, err: gateErr} }
	}
	m.lastExecutionStrategy = det.Profile
	m.hotfixBranding = "PROMPT"
	// Mode is a presentation label only — never an execution-path decision.
	req.Mode = m.resolver.Current().String()

	// ── CONVERSATION FLOW (UX_ENGINE #4) ─────────────────────────────
	// A direct-response request (casual greeting / simple question) is a single
	// human action: Izen → understands intent → answers. It must NOT create an
	// execution narrative, a workspace-context pipeline, or planning states.
	// The runtime still resolves and executes it (zero repository context), but
	// the human surface is the answer only — no narrative panel, no milestones,
	// no execution timeline, and no loading spinner. The in-flight marker stays
	// set so terminal cleanup releases input and a second submission is blocked
	// while the direct answer is produced; the provider call stays cancellable
	// (Ctrl+C) via the registered background cancel.
	if det.Profile.Strategy == strategy.DirectResponse {
		m.execView = nil
		m.execVisibility = presentation.VisibilityNormal
		m.executionResolving = true
		m.agentRunning = true
		m.agentLabel = ""
		dirCtx, dirCancel := context.WithCancel(context.Background())
		m.streamCancel = dirCancel
		m.registerBackgroundCancel(dirCancel)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(dirCtx, 5*time.Minute)
			defer cancel()
			res, err := m.executor.Execute(ctx, req)
			if res == nil {
				res = &execution.ExecutionResult{RequestID: req.RequestID}
			}
			return gatedExecutionMsg{res: res, det: det, err: err}
		}
	}

	// ── OPERATION LIFECYCLE BINDING (P0 #1) ───────────────────────────
	// The gated execution runs as a real foreground operation: it owns a
	// cancellable context the provider call inherits, a watchdog, per-operation
	// telemetry and the busy flags. Ctrl+C now cancels the active execution
	// immediately (operationContext → provider stream) instead of leaving a
	// detached background call running silently.
	m.beginOperation(OpHotfix)
	// beginOperation supersedes the gated resolving marker; restore it so the
	// execution-view projection keeps driving the loading dock and the terminal
	// events finalize THIS operation (no newer operation exists to clobber).
	m.executionResolving = true
	m.agentLabel = ""

	// ── EXECUTOR STREAMING ─────────────────────────────────────────────
	// Reuse the same incremental streaming mechanism as /ask: a channel +
	// reader goroutine + callback. The executor invokes the callback for each
	// content delta, first token, and completion. The UI reads from the channel
	// and renders incrementally via the existing tokenMsg/streamDoneMsg handlers.
	m.execStreamCh = make(chan tea.Msg, 1024)
	m.execStreaming = true
	m.spinnerFrame = 0
	m.startShimmer("Waiting for model...", "execution")

	req.StreamCallback = func(ev execution.StreamEvent) {
		// Capture channel locally to avoid race with cleanup
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
	}

	// ── LOADING DOCK: SPINNER + TIPS, EVENT-DERIVED TEXT ──────────────
	// The dock stays alive (spinner + tips) while its text derives EXCLUSIVELY
	// from the execution-view projection (real runtime events) and the
	// authoritative stage — no static progress template is ever claimed.
	m.startShimmer("", "execution")

	// The single execution-view projection resets at dispatch. From here on
	// the renderer derives the execution status ONLY from this projection's
	// state, which handleDomainEvent advances from the canonical runtime
	// events — the UI never invents execution truth.
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin(req.RequestID)
	// A fresh execution starts in the NORMAL human layer.
	m.execVisibility = presentation.VisibilityNormal

	// The provider call context is derived from the ACTIVE OPERATION context
	// synchronously (no goroutine race on m.activeOp) and registered so
	// Ctrl+C / stale-agent release propagate cancellation into the runtime
	// provider call immediately.
	execCtx, execCancel := context.WithTimeout(m.operationContext(), 5*time.Minute)
	m.streamCancel = execCancel
	m.registerBackgroundCancel(execCancel)

	// Reader goroutine: reads from execStreamCh and returns messages to Update loop
	readerCmd := func() tea.Msg {
		return m.readExecStream()
	}

	// Executor background task
	executorCmd := func() tea.Msg {
		res, err := m.executor.Execute(execCtx, req)
		if res == nil {
			res = &execution.ExecutionResult{RequestID: req.RequestID}
		}
		return gatedExecutionMsg{res: res, det: det, err: err}
	}

	return tea.Batch(readerCmd, executorCmd, m.smoothStreamTickCmd(), m.shimmerTickCmd())
}

// executionFirstTarget returns the first resolved mutation target, or "".
func executionFirstTarget(targets []string) string {
	if len(targets) == 0 {
		return ""
	}
	return targets[0]
}

// humanArtifactActivity renders the human activity line for a produced
// artifact, converting the runtime artifact kind into human language
// (UX_ENGINE: "artifact produced" → "Prepared result"). A direct-response /
// explanation artifact is the conversation answer itself — it is rendered
// without an internal activity line.
func humanArtifactActivity(kind, target string) string {
	switch presentation.ClassifyArtifact(kind) {
	case presentation.ArtifactPlan:
		return "Prepared a plan"
	case presentation.ArtifactDiff:
		return "Prepared a proposed change"
	case presentation.ArtifactInspection:
		return "Prepared findings"
	case presentation.ArtifactVerification:
		return "Verified changes"
	case presentation.ArtifactError:
		return "Prepared an error report"
	default:
		if target != "" {
			return "Generated response for " + target
		}
		return "Generated response"
	}
}

// pushArtifact routes a terminal artifact through the semantic ArtifactRenderer
// so structured artifacts (plans, diffs, verification, errors) are NEVER pushed
// as raw JSON/text. Response-type artifacts render as human content lines.
func (m *model) pushArtifact(kind, target, content string) {
	if kind == "" {
		m.push(roleAI, content)
		return
	}
	lines := presentation.RenderArtifact(kind, target, content)
	if len(lines) == 0 {
		m.push(roleAI, content)
		return
	}
	m.push(roleAI, strings.Join(lines, "\n"))
}

// handleGatedExecution projects a gated execution result into the proposal /
// result surface. It delegates to the shared executionResultMsg handler so the
// rendering truth is identical for every execution path.
//
// A GATE failure (before any execution began) is surfaced distinctly and stays
// idle. An EXECUTION failure (the runtime returned a result alongside the
// error) is delegated to the shared terminal projection, which finalizes the
// operation and clears the loading state — a terminal execution event must
// never leave the in-flight marker behind.
func (m *model) handleGatedExecution(msg gatedExecutionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil && msg.res == nil {
		m.push(roleError, "Couldn't start: "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil
	}
	m.clearEngineFirstMutationState()
	return m.executionResultUpdate(executionResultMsg{res: msg.res, err: msg.err})
}

// executionResultUpdate projects a RuntimeExecutor result onto the proposal /
// result surface. It is the single rendering projection shared by every
// execution path (gated + direct). The runtime owns the truth; the UI renders
// it.
func (m *model) executionResultUpdate(msg executionResultMsg) (tea.Model, tea.Cmd) {
	// ── RUNTIME EXECUTOR RESULT (authority migration, Steps 2-4) ──
	// The RuntimeExecutor returned the terminal outcome of an execution
	// request. The runtime owned every provider invocation, patch creation,
	// mutation and verification; the UI projects the result and renders the
	// canonical events (MutationCompleted / VerificationCompleted /
	// ExecutionFinished) it already receives on the bus. No provider or
	// PatchManager call lives on this path anymore.
	m.clearEngineFirstMutationState()
	if msg.err != nil || (msg.res != nil && msg.res.Err != nil) {
		execErr := msg.err
		if execErr == nil {
			execErr = msg.res.Err
		}
		var usageCmd tea.Cmd
		if msg.res != nil {
			usageCmd = m.tokenUsageCmdKnown(
				msg.res.Completed.InputTokens,
				msg.res.Completed.OutputTokens,
				msg.res.Completed.Known,
			)
		}
		// Classify the terminal outcome truthfully: a cancellation or timeout is
		// distinct from a hard failure (the watchdog and the execView both
		// report it as such, never as a fabricated failure).
		outcome := classifyOpErr(execErr)
		if msg.res != nil {
			m.recordRuntimeProof(msg.res)
		}
		m.finalizeOperation(outcome, execErr)
		if outcome == OpOutcomeCancelled {
			m.push(roleSystem, infoStyle.Render("Cancelled. No files were modified."))
		} else {
			m.push(roleError, runtimeExecutionFailureMessage(msg.res, execErr))
			m.push(roleSystem, infoStyle.Render("No files were modified."))
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		if mdl, queueCmd, handled := m.projectBuildQueueFromProof(msg.res, execErr); handled {
			return mdl, tea.Batch(queueCmd, usageCmd)
		}
		return m, tea.Batch(m.flushPendingRecords(), usageCmd)
	}
	res := msg.res
	if res == nil {
		return m, nil
	}

	// ── RUNTIME EXECUTION EVIDENCE RETENTION (P1 #6) ───────────────
	// The runtime-owned proof + runtime graph are retained for $inspect — the
	// authoritative execution timeline is never reconstructed from UI state.
	m.recordRuntimeProof(res)

	// ── Terminal token accounting from the runtime's authoritative usage ──
	// The runtime computed the aggregate provider-reported usage (provider,
	// model, input/output tokens, latency, artifact) on ExecutionResult.Completed.
	// The renderer consumes that account directly and never re-sums model calls
	// — the footer numbers are always the provider's real billing.
	tokenInput := res.Completed.InputTokens
	tokenOutput := res.Completed.OutputTokens

	// ── READ-ONLY TERMINAL: the runtime produced an artifact, no mutation ──
	if res.Proof != nil && res.Proof.Outcome == execution.OutcomeCompleted && res.Content != "" {
		m.finalizeOperation(OpOutcomeSuccess, nil)
		m.push(roleActivity, humanArtifactActivity(res.ArtifactKind, executionFirstTarget(res.Targets)))
		m.pushArtifact(res.ArtifactKind, executionFirstTarget(res.Targets), res.Content)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
	}

	// ── NO-CHANGE TERMINAL: the mutation applied but nothing changed ──
	// A committed OutcomeNoChange is a successful execution that produced no
	// filesystem change. OutcomeNoOpSuccess is its bounded-patch sibling: the
	// model explicitly answered NO_CHANGES_REQUIRED, no patch was staged and
	// nothing applied. Both project distinctly — never the generic
	// "Completed — nothing produced." fallback.
	if res.Proof != nil && (res.Proof.Outcome == execution.OutcomeNoChange ||
		res.Proof.Outcome == execution.OutcomeNoOpSuccess) {
		m.executorPendingPatchID = ""
		m.executorPendingTargets = nil
		m.pendingHotfixTask = nil
		m.pendingHotfixPatch = nil
		m.pendingProposals = nil
		m.resolveApprovalState()
		m.finalizeOperation(OpOutcomeSuccess, nil)
		m.push(roleSystem, infoStyle.Render("  "+Icon.Info+" Mutation applied — no content changed."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		if mdl, queueCmd, handled := m.projectBuildQueueFromProof(res, nil); handled {
			return mdl, tea.Batch(queueCmd, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known))
		}
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
	}

	// ── REJECTED TERMINAL: the human rejected the held proposal ────────
	// A rejection is a deliberate decision on the held artifact, distinct from
	// an execution cancelled mid-run.
	if res.Proof != nil && res.Proof.Outcome == execution.OutcomeRejected {
		m.executorPendingPatchID = ""
		m.executorPendingTargets = nil
		m.pendingHotfixTask = nil
		m.pendingHotfixPatch = nil
		m.pendingProposals = nil
		m.resolveApprovalState()
		m.finalizeOperation(OpOutcomeCancelled, nil)
		m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Rejected — no files were modified."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
	}

	// ── APPROVAL TERMINAL: the runtime applied the mutation ────────
	terminal := res.Proof != nil && res.Proof.Outcome.MutationSucceeded()
	if terminal {
		m.executorPendingPatchID = ""
		m.executorPendingTargets = nil
		m.pendingHotfixTask = nil
		m.pendingHotfixPatch = nil
		m.pendingProposals = nil
		m.resolveApprovalState()
		m.finalizeOperation(OpOutcomeSuccess, nil)
		for _, mut := range res.Mutations {
			m.logActivity("Applied change to %s", mut.File)
		}
		if res.Verification.Passed {
			m.push(roleSystem, infoStyle.Render("  "+Icon.Success+" Mutation applied and verified."))
		} else {
			m.push(roleSystem, infoStyle.Render("  "+Icon.Info+" Mutation applied (verification not run or not passed)."))
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		if mdl, queueCmd, handled := m.projectBuildQueueFromProof(res, nil); handled {
			return mdl, tea.Batch(queueCmd, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known))
		}
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
	}

	// ── CANCELLED / REJECTED TERMINAL ──────────────────────────────
	if res.Proof != nil && res.Proof.Outcome == execution.OutcomeCancelled {
		m.executorPendingPatchID = ""
		m.executorPendingTargets = nil
		m.pendingHotfixTask = nil
		m.pendingHotfixPatch = nil
		m.pendingProposals = nil
		m.resolveApprovalState()
		m.finalizeOperation(OpOutcomeCancelled, nil)
		m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Cancelled. No files were modified."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
	}

	// ── TRUNCATED TERMINAL ───────────────────────────────────────────
	// The provider reported finish_reason=length. This is a terminal
	// execution fact, distinct from artifact syntax rejection, and must never
	// fall through to "Completed — nothing produced."
	if res.Proof != nil && res.Proof.Outcome == execution.OutcomeTruncated {
		m.executorPendingPatchID = ""
		m.executorPendingTargets = nil
		m.pendingHotfixTask = nil
		m.pendingHotfixPatch = nil
		m.pendingProposals = nil
		m.resolveApprovalState()
		m.finalizeOperation(OpOutcomeFailure, res.Err)
		m.push(roleError, runtimeExecutionFailureMessage(res, res.Err))
		m.push(roleSystem, infoStyle.Render("No files were modified."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		if mdl, queueCmd, handled := m.projectBuildQueueFromProof(res, res.Err); handled {
			return mdl, tea.Batch(queueCmd, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known))
		}
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
	}

	// ── RETRYABLE ARTIFACT REJECTION TERMINAL ────────────────────────
	// The artifact gate preserved a DecisionRetry verdict. The UI reports the
	// terminal boundary explicitly; recovery ownership remains with the runtime
	// driver, not a hidden UI retry.
	if res.Proof != nil && res.Proof.Outcome == execution.OutcomeArtifactRetryableRejected {
		m.executorPendingPatchID = ""
		m.executorPendingTargets = nil
		m.pendingHotfixTask = nil
		m.pendingHotfixPatch = nil
		m.pendingProposals = nil
		m.resolveApprovalState()
		m.finalizeOperation(OpOutcomeFailure, res.Err)
		m.push(roleError, runtimeExecutionFailureMessage(res, res.Err))
		m.push(roleSystem, infoStyle.Render("No files were modified."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		if mdl, queueCmd, handled := m.projectBuildQueueFromProof(res, res.Err); handled {
			return mdl, tea.Batch(queueCmd, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known))
		}
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
	}

	// ── CLARIFICATION REQUIRED (no model call, no mutation) ────────
	if res.ClarificationRequired {
		m.finalizeOperation(OpOutcomeAmbiguous, nil)
		m.push(roleError, "I need more information: "+res.StrategyReason)
		m.push(roleSystem, infoStyle.Render("  No model call was made and no files were modified. Name the exact target."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil
	}

	// ── PROPOSAL STAGED FOR APPROVAL ───────────────────────────────
	// The runtime stopped at the approval gate with a held patch. Stage it
	// in the standard proposal dock; the approval keys route the decision
	// back through RuntimeExecutor.Approve/Reject (keys.go).
	if res.PendingPatchID != "" && len(res.Targets) > 0 {
		target := res.Targets[0]
		// The execution target set is captured for the approval authorization
		// (Alt+A issues a MutationAuthorization over exactly these files).
		m.executorPendingTargets = append([]string(nil), res.Targets...)
		task := &plan.Task{
			StepNum:     0,
			Status:      "idle",
			Type:        "FILE_MUTATE",
			Target:      target,
			Description: res.StrategyReason,
		}
		m.pendingHotfixTask = task
		m.pendingHotfixPatch = &execution.Patch{
			ID:       res.PendingPatchID,
			File:     target,
			Original: res.Original,
			Modified: res.Content,
		}
		m.executorPendingPatchID = res.PendingPatchID
		m.pendingProposals = []SemanticProposal{{
			ID:   res.PendingPatchID,
			Diff: res.Diff,
			Target: SemanticTarget{
				QualifiedName: target,
				Module:        filepath.Dir(target),
				Language:      langFromPath(target),
			},
			Expanded: true,
		}}
		m.beginOperation(OpHotfix)
		m.enterApprovalState()
		m.ti.Blur()
		m.recalcViewportHeight()
		m.push(roleStatus, fmt.Sprintf("Proposed change to %s", target))
		m.push(roleSystem, infoStyle.Render("Review the code diff below. Use Alt+A to accept, Alt+R to reject."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
	}

	m.finalizeOperation(OpOutcomeSuccess, nil)
	m.push(roleSystem, infoStyle.Render("Completed — nothing produced."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, res.Completed.Known)
}

func runtimeExecutionFailureMessage(res *execution.ExecutionResult, err error) string {
	if res != nil && res.Proof != nil {
		switch res.Proof.Outcome {
		case execution.OutcomeTruncated:
			return "Execution stopped: provider output was truncated (finish_reason=length)."
		case execution.OutcomeArtifactRetryableRejected:
			return "Execution stopped: artifact validation requested a model repair, but no recovery invocation was run in this workflow."
		case execution.OutcomeArtifactRejected:
			return "Execution failed: artifact rejected."
		}
	}
	if err != nil {
		return "Execution failed: " + err.Error()
	}
	return "Execution failed."
}

// runPromptExecution routes a $prompt action through the unified gateway. It is
// a thin wrapper over runGatedLine that surfaces the directive branding.
func (m *model) runPromptExecution(rawInput string) tea.Cmd {
	m.cancelStaleAgentOps()
	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		m.push(roleError, "[Usage] $prompt <your raw architectural idea or description>")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	m.push(roleSystem, infoStyle.Render("Resolving your request..."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m.runGatedLine("$prompt " + rawInput)
}

// runHotExecution routes a $hot action through the unified gateway.
func (m *model) runHotExecution(rawInput string) tea.Cmd {
	m.cancelStaleAgentOps()
	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		m.push(roleError, "[Usage] $hot <hotfix prompt> — e.g. $hot fix syntax in @index.html")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	m.push(roleSystem, infoStyle.Render("Resolving your request..."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m.runGatedLine("$hot " + rawInput)
}

// buildQueueTaskOutcome maps a terminal RuntimeExecutor outcome onto staged
// /build task bookkeeping: "advance" (completed — move to the next task or
// hand off to verification), "halt" (failed — freeze the remaining queue),
// or "" (the approval/cancellation surface owns this outcome; no queue
// projection runs).
func buildQueueTaskOutcome(outcome execution.MutationOutcome) string {
	switch {
	case outcome.MutationSucceeded(), outcome == execution.OutcomeNoChange,
		outcome == execution.OutcomeNoOpSuccess:
		return "advance"
	case outcome == execution.OutcomeRejected,
		outcome == execution.OutcomeCancelled,
		outcome == execution.OutcomePendingApproval:
		return ""
	default:
		return "halt"
	}
}

// projectBuildQueueFromProof projects a terminal RuntimeExecutor outcome onto
// the staged /build task ledger and drives the queue forward from the
// authoritative execution evidence alone (ExecutionProof). The UI never
// decides mutation truth here: it books exactly the task status the runtime
// reported, advances to the next idle task across the same admission
// boundary (handleBuildRun → dispatchStagedTask), and hands off to the
// verification engine once the plan is drained. It returns handled=false for
// results that do not belong to a staged per-task build execution.
func (m *model) projectBuildQueueFromProof(res *execution.ExecutionResult, execErr error) (tea.Model, tea.Cmd, bool) {
	if m.hotfixActive || m.sess == nil || m.resolver == nil ||
		m.currentBuildTaskID <= 0 || len(m.sess.CurrentTasks) == 0 ||
		m.resolver.Current() != modes.ModeBuild {
		return m, nil, false
	}
	// A user cancellation is not a task failure: the queue stays exactly
	// where it is and the operator decides the next step. The cancellation
	// may surface through the terminal error alone or as a proof outcome.
	if execErr != nil && classifyOpErr(execErr) == OpOutcomeCancelled {
		return m, nil, false
	}
	if res != nil && res.Err != nil && classifyOpErr(res.Err) == OpOutcomeCancelled {
		return m, nil, false
	}
	outcome := execution.OutcomeFailed
	if res != nil && res.Proof != nil {
		outcome = res.Proof.Outcome
	}
	action := buildQueueTaskOutcome(outcome)
	if action == "" {
		return m, nil, false
	}
	tasks := m.sess.CurrentTasks
	for i := range tasks {
		if tasks[i].StepNum == m.currentBuildTaskID {
			if action == "advance" {
				tasks[i].Status = "completed"
			} else {
				tasks[i].Status = "failed"
			}
			break
		}
	}
	m.currentBuildTaskID = 0
	if action == "halt" {
		stalled := false
		for i := range tasks {
			if tasks[i].Status == "idle" {
				tasks[i].Status = "stalled"
				stalled = true
			}
		}
		m.sess.StageTaskList(&tasks)
		_ = m.sess.Save()
		if stalled {
			m.push(roleError, "[BUILD HALTED] Execution failed. Queue frozen — remaining tasks marked stalled. Use /investigate or /plan to re-generate a valid ledger.")
		}
		flush := m.flushPendingRecords()
		return m, flush, true
	}
	m.sess.StageTaskList(&tasks)
	_ = m.sess.Save()
	for _, t := range tasks {
		if t.Status == "idle" || t.Status == "processing" {
			flush := m.flushPendingRecords()
			return m, tea.Batch(flush, m.handleBuildRun(0)), true
		}
	}
	// Plan drained — run the build verification engine.
	m.buildVerifyPending = true
	m.push(roleSystem, "Verifying build...")
	flush := m.flushPendingRecords()
	return m, tea.Batch(flush, m.runTestEngine("./...")), true
}
