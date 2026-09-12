package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/workflow"
	domainorch "github.com/PizenLabs/izen/internal/domain/orchestration"
	domainworkflow "github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── CASUAL CONVERSATION AUTO-UNWIND ──────────────────────────────────────
// A casual prompt (IntentConversation, e.g. "hi") submitted while the
// WorkflowStateMachine is in any non-idle phase MUST unwind to StateIdle /
// StateChat (/ask) before delivering a direct response. It MUST NEVER enter
// the orchestrator synthesis path (plan.synthesize), background tools, or
// LLM execution providers.

// stripCasualDirectives removes execution-directive prefixes so the intent
// classifier sees the human content ("$prompt hi" → "hi"). Slash commands
// are NOT stripped: they are explicit mode switches handled elsewhere.
func stripCasualDirectives(line string) string {
	trimmed := strings.TrimSpace(line)
	for _, prefix := range []string{"$prompt", "$hot", "$decide"} {
		if strings.HasPrefix(trimmed, prefix) {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			trimmed = rest
			break
		}
	}
	return trimmed
}

// isCasualConversationPrompt reports whether the input is pure conversational
// chatter (IntentConversation at ~95% confidence per the deterministic
// classifier). Slash inputs, shell bangs, and empty lines are never casual:
// they belong to the command surfaces.
func isCasualConversationPrompt(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if isSlashInput(trimmed) || strings.HasPrefix(trimmed, "!") || strings.HasPrefix(trimmed, "$inspect") {
		return false
	}
	return autonomy.IsConversation(stripCasualDirectives(trimmed))
}

// casualDirectResponse resolves the zero-pipeline direct answer for an
// unwound casual prompt. It uses the local intent interceptor first (zero
// LLM, zero tools); when history is non-empty and no canned answer matches,
// it falls back to a static greeting so the turn completes WITHOUT any
// provider call, synthesis step, or background worker.
func (m *model) casualDirectResponse(content string) string {
	if response := m.interceptLocalIntent(content); response != "" {
		return response
	}
	if m.userName != "" {
		return fmt.Sprintf("Hello %s! How can I assist you today?", m.userName)
	}
	return "Hello! How can I assist you today?"
}

// handleCasualAutoUnwind implements the CASUAL INTENT AUTO-UNWIND invariant.
// When line is casual conversation and the WorkflowStateMachine is in any
// non-idle phase, it emits EventReset, clears streaming/execution state,
// synchronizes the UI back to StateChat (/ask), and delivers the direct
// response locally. It returns true when the prompt was consumed — callers
// MUST return immediately without entering any pipeline dispatch.
//
// ZERO PIPELINE PROPAGATION: this path performs no Gateway.Gate call, no
// ScopeGuard proposal, no WorkerEngine dispatch, no plan synthesis, and no
// provider invocation.
func (m *model) handleCasualAutoUnwind(line string) bool {
	if m == nil || m.workflowSM == nil {
		return false
	}
	if m.workflowSM.State() == workflow.StateIdle {
		return false
	}
	if !isCasualConversationPrompt(line) {
		return false
	}
	content := stripCasualDirectives(line)

	// 1. Unwind the WorkflowStateMachine to StateIdle. EventReset is valid
	// from every non-idle workflow state.
	if err := m.workflowSM.SendEvent(workflow.EventReset, workflow.TransitionContext{}); err != nil {
		m.appendSystemError(fmt.Errorf("failed to auto-unwind state machine on casual prompt: %w", err))
	}
	// Keep the application-layer domain runtime reachable: a stale Build /
	// Review phase would otherwise reject the next recovery prompt with
	// "moving to a previous phase is not permitted".
	if m.workflowRT != nil {
		m.workflowRT.Reset()
	}
	// Keep the orchestrator projection consistent with the reset machine.
	// Force(PhaseAsk) maps onto StateIdle (no-op on the SM) and re-anchors
	// the orchestrator's current/history without touching the phase graph.
	if m.orch != nil {
		if err := m.orch.Force(domainorch.PhaseAsk, workflow.TransitionContext{}); err != nil {
			m.appendSystemError(fmt.Errorf("failed to auto-unwind orchestrator on casual prompt: %w", err))
		}
	}

	// 2. Clear any active execution / streaming state so no spinner, tick
	// loop, or background worker survives the unwind.
	m.cancelStaleAgentOps()
	m.resetStreamingState()
	m.clearBusyFlags()
	m.autonomousActive = false
	m.executionResolving = false
	m.autonomyHotfix = false
	m.pendingHotfixObjective = ""
	m.resolveApprovalState()

	// 3. Synchronize the UI back to StateChat (/ask). A lightweight resolver
	// set (not a full setMode) is intentional: the handoff sanitizer and
	// engine auto-triggers of setMode would re-enter the pipeline we just
	// unwound from.
	if m.resolver != nil && m.resolver.Current() != modes.ModeAsk {
		m.resolver.Set(modes.ModeAsk)
		if m.sess != nil {
			m.sess.SetMode(modes.ModeAsk)
			m.persistSession("gateway-auto-unwind")
		}
	}
	m.syncUIState()
	m.ti.Focus()

	// 4. Direct response immediately, without entering pipeline dispatch.
	if m.sess != nil {
		m.sess.AddMessage("user", content, 5)
		m.sess.AddMessage("assistant", m.casualDirectResponse(content), 5)
		m.persistSession("gateway-auto-unwind")
	}
	m.push(roleAI, m.casualDirectResponse(content))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return true
}

// isBackwardTransitionError reports whether err is a phase-transition
// rejection for moving to a previous phase (backward movement). It matches
// both the domain WorkflowRuntime sentinel and the orchestrator/domain
// transition error shapes, plus the legacy message substring.
func isBackwardTransitionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, domainworkflow.ErrInvalidTransition) {
		return true
	}
	var rte *domainworkflow.TransitionError
	if errors.As(err, &rte) {
		return true
	}
	var ote *domainorch.TransitionError
	if errors.As(err, &ote) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "moving to a previous phase") ||
		strings.Contains(msg, "no valid transition") ||
		strings.Contains(msg, "event not allowed in current state")
}

// handleBackwardTransitionError surfaces a blocked backward phase switch as
// a system warning and leaves the UI in a predictable state. It returns true
// when err was a backward-transition rejection (handled); callers MUST return
// nil immediately so prompt dispatching is never corrupted.
func (m *model) handleBackwardTransitionError(err error) bool {
	if !isBackwardTransitionError(err) {
		return false
	}
	m.appendSystemError(fmt.Errorf("phase transition blocked: use /reset or /ask to return to initial state before switching phases: %w", err))
	m.push(roleSystem, infoStyle.Render("Phase transition blocked — use /reset or /ask to return to the initial state before switching phases."))
	m.syncUIState()
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return true
}
