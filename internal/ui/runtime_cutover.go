package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── Phase 1 cutover: autonomy → RuntimeExecutor ────────────────────────────
//
// When IZEN_RUNTIME_EXECUTOR=1 the autonomy-decided BUILD workspace routes its
// mutation through the RuntimeExecutor boundary instead of the legacy mode
// engines:
//
//	User Intent → Autonomy Decision → Execution Intent
//	  → RuntimeExecutor.Execute(req)  (owns provider, context, patch, gate)
//	  → RuntimeExecutor.Approve(id)   (owns apply, verify, commit)
//	  → ExecutionResult → executionResultUpdate (UI projection)
//
// The UI keeps: input collection, state display, autonomy proposal (capability
// gate), approval keys, and projection of the canonical runtime events. It no
// longer owns provider invocation, artifact execution, mutation authority or
// verification authority on this path.

// runRuntimeExecuteCmd submits an ExecuteRequest to the RuntimeExecutor through
// the standard operation lifecycle and routes the terminal result onto the
// shared execution-result surface (the same projection the gated path uses).
// It is the single executor dispatch used by the autonomy cutover.
func (m *model) runRuntimeExecuteCmd(req execution.ExecuteRequest) tea.Cmd {
	if m.executor == nil {
		m.push(roleError, "execution runtime not wired")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	m.beginOperation(OpHotfix)
	m.executionResolving = true
	m.agentLabel = ""
	m.startShimmer("", "execution")
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin(req.RequestID)
	m.execVisibility = presentation.VisibilityNormal
	execCtx, execCancel := context.WithTimeout(m.operationContext(), 5*time.Minute)
	m.streamCancel = execCancel
	m.registerBackgroundCancel(execCancel)
	return func() tea.Msg {
		res, err := m.executor.Execute(execCtx, req)
		if res == nil {
			res = &execution.ExecutionResult{RequestID: req.RequestID}
		}
		return gatedExecutionMsg{res: res, det: execution.IntentResolution{Raw: req.Prompt, Prompt: req.Prompt}, err: err}
	}
}

// executeAutonomyViaRuntime is the Phase 1 cutover BUILD path. It converges the
// autonomy-decided mutation workspace onto the RuntimeExecutor:
//
//  1. The IntentGateway resolves the strategy + target deterministically — the
//     single canonical target-resolution authority (Step 2). No UI-side regex
//     resolver re-interprets the target.
//  2. Ambiguity stays explicit: an unresolvable/ambiguous target surfaces the
//     existing autonomy target surface (candidate selector / not-found), never
//     the model.
//  3. The autonomy classification is preserved (Step 6): an already-classified
//     mutation intent is NEVER re-classified downstream by the strategy
//     selector. The gateway's budgets/context contract is kept; the mutation
//     path is forced so a decided mutation cannot degrade into a read-only
//     plan artifact.
//  4. The RuntimeExecutor owns provider invocation, patch creation, the
//     approval gate, apply, verification and the canonical lifecycle events.
func (m *model) executeAutonomyViaRuntime(trace autonomy.Trace) tea.Cmd {
	prompt := trace.Input
	if m.autonomyHotfix {
		m.autonomyHotfix = false
		if objective := m.pendingHotfixObjective; objective != "" {
			m.pendingHotfixObjective = ""
			prompt = objective
		}
	}
	if m.gateway == nil || m.executor == nil {
		// Runtime boundary not wired (harness): an autonomy-decided mutation
		// must never silently drop, but no legacy provider path may run either.
		// The executor is the only production mutation authority.
		m.push(roleError, "execution runtime not wired — cannot execute the decided mutation")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── CANONICAL STRATEGY + TARGET RESOLUTION (Step 2) ──────────────
	// The IntentGateway is the single target-resolution authority. The legacy
	// UI regex resolvers are never consulted on this path.
	profile := m.gateway.SelectStrategy(prompt)
	m.lastExecutionStrategy = profile

	// ── EXPLICIT AMBIGUITY (never silent, never the model as resolver) ──
	if profile.Strategy == strategy.HumanClarification {
		target, candidates := m.resolveAutonomyBuildTarget(trace.Intent.Target())
		switch {
		case len(candidates) > 1:
			// Several workspace files match — the candidate selector pauses.
			m.stageAutonomyTargetSelector(trace, candidates)
			return nil
		case len(candidates) == 0:
			// A named target that exists nowhere is a terminal diagnosis (unless
			// the objective is a creation request). Never fabricate a target.
			if trace.Intent.Target() != "" && !isAutonomyCreationRequest(trace.Input) {
				return m.reportAutonomyTargetNotFound(trace, target)
			}
		}
	}

	targets := resolvedTargetsForExecution(profile, trace.Intent.Targets)

	// ── PRESERVE THE AUTONOMY CLASSIFICATION (Step 6) ────────────────
	// An autonomy-decided mutation intent is never re-classified downstream by
	// the strategy selector. The gateway profile's budgets and context policy
	// are retained; the execution path is forced to TargetedMutation so a
	// decided mutation cannot degrade into a read-only plan artifact.
	if trace.Intent.RequiresMutation() && profile.Strategy != strategy.TargetedMutation {
		profile.Strategy = strategy.TargetedMutation
		profile.ModelRequired = true
		profile.StrategyReason = "autonomy-decided mutation on the resolved target(s)"
	}

	// ── BOUNDED EVIDENCE HANDOFF (Step 5) ────────────────────────────
	// The deterministic evidence ledger compiled by the autonomy runtime is
	// authoritative evidence for the mutation. The runtime injects it as the
	// evidence contract; the full-file context it reads remains supporting
	// context only. The model is never given redundant full-file context
	// instead of the evidence.
	var evidence string
	if len(targets) > 0 {
		evidence = m.compileAutonomyBuildEvidence(targets[0])
	}

	req := execution.ExecuteRequest{
		Mode:             modes.ModeBuild.String(),
		Prompt:           prompt,
		Targets:          targets,
		Strategy:         &profile,
		Intent:           trace.Intent.Intent.String(),
		IntentConfidence: trace.Intent.Confidence,
		TargetConfidence: trace.TargetConfidence,
		Scope:            string(trace.Route.Workspace),
		Evidence:         evidence,
	}
	return m.runRuntimeExecuteCmd(req)
}

// resolvedTargetsForExecution returns the execution target set from the
// canonical resolution, falling back to the autonomy-extracted targets. Only
// deterministically resolved targets cross into the runtime; a missing or
// ambiguous target is never fabricated.
func resolvedTargetsForExecution(profile strategy.ExecutionStrategyProfile, fallback []string) []string {
	var targets []string
	for _, t := range profile.Targets {
		if t.Resolved != "" && (t.Status == strategy.TargetExplicit || t.Status == strategy.TargetResolved) {
			targets = append(targets, t.Resolved)
		}
	}
	if len(targets) == 0 {
		targets = append(targets, fallback...)
	}
	return targets
}

// runStagedBuildViaRuntime routes the staged /build plan through the
// RuntimeExecutor admission boundary.
//
//   - Pure file plans (FILE_MUTATE/GIT_ACTION only) combine their goals into
//     one executor request over the resolved target set.
//   - Mixed plans (any SHELL_EXEC step) and plans without resolvable file
//     targets dispatch strictly PER TASK: beginStagedTask selects the next
//     pending task and dispatchStagedTask crosses it over the single
//     admission boundary — the RuntimeExecutor for file work, the
//     interactive shell gate for OS commands. Terminal-result projections
//     then advance the queue one evidence-backed task at a time. There is no
//     wholesale legacy fallback: OS command execution is not a file mutation
//     and the runtime executor does not own it, so mixed plans decompose at
//     the dispatch seam instead of degrading out of the runtime.
//
// Without a wired gateway + RuntimeExecutor the dispatch FAILS CLOSED: there
// is deliberately no caller-side execution path that could mutate outside
// the runtime boundary.
func (m *model) runStagedBuildViaRuntime() tea.Cmd {
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 {
		m.push(roleStatus, "no tasks staged — use /plan first")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	if m.gateway == nil || m.executor == nil {
		m.push(roleError, "execution runtime not wired — cannot execute the staged plan")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	var targets []string
	var goals []string
	seen := make(map[string]bool)
	mixed := false
	for _, t := range tasks {
		if t.Type != "FILE_MUTATE" && t.Type != "GIT_ACTION" {
			// Non-file tasks (e.g. SHELL_EXEC) force strict per-task
			// sequential dispatch below — never a caller-side loop.
			mixed = true
			continue
		}
		if t.Target != "" && !seen[t.Target] {
			seen[t.Target] = true
			targets = append(targets, t.Target)
		}
		if t.Description != "" {
			goals = append(goals, t.Description)
		}
	}
	if !mixed && len(targets) > 0 {
		prompt := strings.Join(goals, "\n")
		if strings.TrimSpace(prompt) == "" {
			prompt = m.lastPlanIntent
		}
		if strings.TrimSpace(prompt) == "" {
			prompt = strings.Join(targets, " ")
		}

		// Canonical strategy resolution, then pin the mutation path: a staged
		// /build plan is already an implementation intent — it is never re-routed
		// to a read-only plan artifact downstream.
		profile := m.gateway.SelectStrategy(prompt)
		profile.Strategy = strategy.TargetedMutation
		profile.ModelRequired = true
		profile.StrategyReason = "staged /build FILE_MUTATE plan routed through the runtime executor"
		m.lastExecutionStrategy = profile

		req := execution.ExecuteRequest{
			Mode:     modes.ModeBuild.String(),
			Prompt:   prompt,
			Targets:  targets,
			Strategy: &profile,
		}
		return m.runRuntimeExecuteCmd(req)
	}
	// Mixed plan or unresolvable targets: strict per-task sequential dispatch
	// across the same admission boundary as every other /build entry point.
	target := m.beginStagedTask(0)
	if target == nil {
		return nil
	}
	return m.dispatchStagedTask(target)
}

// runRuntimeTaskRequest submits a single staged task's FILE_MUTATE/GIT_ACTION
// execution through the RuntimeExecutor. It is the executor-side replacement
// for the legacy per-task proposeBuildPatch loop: the task target becomes the
// execution target set and the executor owns provider, patch, approval gate,
// apply and verification.
func (m *model) runRuntimeTaskRequest(task *plan.Task) tea.Cmd {
	if m.gateway == nil || m.executor == nil || task == nil {
		m.push(roleError, "execution runtime not wired — cannot execute the mutation task")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	prompt := task.Description
	if strings.TrimSpace(prompt) == "" {
		prompt = "implement task " + task.Target
	}
	profile := m.gateway.SelectStrategy(prompt)
	profile.Strategy = strategy.TargetedMutation
	profile.ModelRequired = true
	profile.StrategyReason = "staged FILE_MUTATE task routed through the runtime executor"
	m.lastExecutionStrategy = profile
	targets := []string{task.Target}
	req := execution.ExecuteRequest{
		Mode:     modes.ModeBuild.String(),
		Prompt:   prompt,
		Targets:  targets,
		Strategy: &profile,
	}
	return m.runRuntimeExecuteCmd(req)
}

// runRuntimePrompt submits a free-form mutation prompt in build mode through
// the RuntimeExecutor. It is the cutover equivalent of the legacy build-mode
// handleMessageContent path (legacy reclassification inside an
// autonomy-decided workspace).
func (m *model) runRuntimePrompt(content string) tea.Cmd {
	if m.gateway == nil || m.executor == nil {
		// FAIL CLOSED: without the admission gateway and the RuntimeExecutor
		// there is deliberately no caller-side execution path.
		m.push(roleError, "execution runtime not wired — cannot execute the mutation")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	profile := m.gateway.SelectStrategy(content)
	m.lastExecutionStrategy = profile
	if profile.Strategy == strategy.HumanClarification {
		m.push(roleError, "Couldn't resolve the mutation target: "+profile.StrategyReason)
		m.push(roleSystem, infoStyle.Render("No model call was made and no files were modified. Name the exact target."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	targets := resolvedTargetsForExecution(profile, nil)
	req := execution.ExecuteRequest{
		Mode:     modes.ModeBuild.String(),
		Prompt:   content,
		Targets:  targets,
		Strategy: &profile,
	}
	return m.runRuntimeExecuteCmd(req)
}
