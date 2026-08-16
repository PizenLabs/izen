package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
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
		return func() tea.Msg { return gatedExecutionMsg{det: det, err: gateErr} }
	}
	m.lastExecutionStrategy = det.Profile
	m.lastStrategyGraph = nil
	m.hotfixBranding = "PROMPT"
	// Mode is a presentation label only — never an execution-path decision.
	req.Mode = m.resolver.Current().String()

	// The loading shimmer activates synchronously at dispatch (t=0ms) so the
	// runtime execution is never a frozen pane.
	m.startShimmer("Resolving execution...", "execution")
	m.agentRunning = true
	m.agentLabel = "resolving execution"

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res, err := m.executor.Execute(ctx, req)
		if res == nil {
			res = &execution.ExecutionResult{RequestID: req.RequestID}
		}
		return gatedExecutionMsg{res: res, det: det, err: err}
	}
}

// handleGatedExecution projects a gated execution result into the proposal /
// result surface. It delegates to the shared executionResultMsg handler so the
// rendering truth is identical for every execution path.
func (m *model) handleGatedExecution(msg gatedExecutionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.push(roleError, "[execution] gate failed: "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil
	}
	m.clearEngineFirstMutationState()
	return m.executionResultUpdate(executionResultMsg{res: msg.res, err: msg.res.Err})
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
		m.finalizeOperation(OpOutcomeFailure, execErr)
		m.push(roleError, "[PROMPT] execution failed: "+execErr.Error())
		m.push(roleSystem, infoStyle.Render("No files were modified."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.flushPendingRecords()
	}
	res := msg.res
	if res == nil {
		return m, nil
	}

	// ── Terminal token accounting from the runtime's provider usage ──
	var tokenInput, tokenOutput int
	for _, inv := range res.ModelCalls {
		tokenInput += inv.TokenInput
		tokenOutput += inv.TokenOutput
	}

	// ── READ-ONLY TERMINAL: the runtime produced an artifact, no mutation ──
	if res.Proof != nil && res.Proof.Outcome == execution.OutcomeCompleted && res.Content != "" {
		m.finalizeOperation(OpOutcomeSuccess, nil)
		m.push(roleActivity, fmt.Sprintf("[runtime] artifact produced: %s", res.ArtifactKind))
		m.push(roleAI, res.Content)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.tokenUsageCmd(tokenInput, tokenOutput)
	}

	// ── APPROVAL TERMINAL: the runtime applied the mutation ────────
	terminal := res.Proof != nil && res.Proof.Outcome.MutationSucceeded()
	if terminal {
		m.executorPendingPatchID = ""
		m.pendingHotfixTask = nil
		m.pendingHotfixPatch = nil
		m.pendingProposals = nil
		m.resolveApprovalState()
		m.finalizeOperation(OpOutcomeSuccess, nil)
		for _, mut := range res.Mutations {
			m.logActivity("[mutation] %s: %s", mut.File, mut.Outcome.Display())
		}
		if res.Verification.Passed {
			m.push(roleSystem, infoStyle.Render("  "+Icon.Success+" Mutation applied and verified."))
		} else {
			m.push(roleSystem, infoStyle.Render("  "+Icon.Info+" Mutation applied (verification not run or not passed)."))
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.tokenUsageCmdKnown(tokenInput, tokenOutput, true)
	}

	// ── CANCELLED / REJECTED TERMINAL ──────────────────────────────
	if res.Proof != nil && res.Proof.Outcome == execution.OutcomeCancelled {
		m.executorPendingPatchID = ""
		m.pendingHotfixTask = nil
		m.pendingHotfixPatch = nil
		m.pendingProposals = nil
		m.resolveApprovalState()
		m.finalizeOperation(OpOutcomeCancelled, nil)
		m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Mutation rejected. No files were modified."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.tokenUsageCmd(tokenInput, tokenOutput)
	}

	// ── CLARIFICATION REQUIRED (no model call, no mutation) ────────
	if res.ClarificationRequired {
		m.finalizeOperation(OpOutcomeAmbiguous, nil)
		m.push(roleError, "[PROMPT] "+res.StrategyReason)
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
		m.push(roleStatus, fmt.Sprintf("[%s APPROVAL] Proposed patch to %s", m.hotfixBrandingLabel(), target))
		m.push(roleSystem, infoStyle.Render("Review the code diff below. Use Alt+A to accept, Alt+R to reject."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.tokenUsageCmd(tokenInput, tokenOutput)
	}

	m.finalizeOperation(OpOutcomeSuccess, nil)
	m.push(roleSystem, infoStyle.Render("Execution completed with no artifact."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m, m.tokenUsageCmd(tokenInput, tokenOutput)
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
	m.push(roleSystem, infoStyle.Render(" engine-first: resolving intent deterministically (no mode routing)."))
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
	m.push(roleSystem, infoStyle.Render(" engine-first: resolving hotfix intent deterministically."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m.runGatedLine("$hot " + rawInput)
}
