package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/core/workflow"
	domainworkflow "github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/modes"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
)

// driveIntoPlanning moves the core state machine into StatePlanning, as /plan
// does before a synthesis attempt starts.
func driveIntoPlanning(t *testing.T, m *model) {
	t.Helper()
	if err := m.workflowSM.SendEvent(workflow.EventPlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("plan transition: %v", err)
	}
	if m.workflowSM.State() != workflow.StatePlanning {
		t.Fatalf("core SM state = %v, want StatePlanning", m.workflowSM.State())
	}
}

// driveIntoReviewing moves the core state machine Idle → Planning → Building
// → Reviewing along the canonical path.
func driveIntoReviewing(t *testing.T, m *model) {
	t.Helper()
	driveIntoPlanning(t, m)
	if err := m.workflowSM.SendEvent(workflow.EventBuild, workflow.TransitionContext{HasPlan: true, HasCapabilities: true}); err != nil {
		t.Fatalf("build transition: %v", err)
	}
	if err := m.workflowSM.SendEvent(workflow.EventReview, workflow.TransitionContext{}); err != nil {
		t.Fatalf("review transition: %v", err)
	}
	if m.workflowSM.State() != workflow.StateReviewing {
		t.Fatalf("core SM state = %v, want StateReviewing", m.workflowSM.State())
	}
}

// TestGateway_CasualPromptAutoUnwindsFromPlanning is the regression guard for
// the casual-conversation auto-unwind invariant: submitting "hi" (classified
// as IntentConversation at 95% confidence) while the WorkflowStateMachine is
// in StatePlanning MUST reset the machine to StateIdle, restore StateChat,
// and deliver a direct response WITHOUT invoking any orchestrator/plan
// synthesis driver, background tool, or LLM provider.
func TestGateway_CasualPromptAutoUnwindsFromPlanning(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModePlan)
	driveIntoPlanning(t, m)

	// Simulate in-flight execution state a real /plan synthesis would hold.
	m.streaming = true
	m.agentRunning = true
	m.executionResolving = true
	m.autonomousActive = true
	m.planPending = true

	cmd := m.handleInput("hi")
	if cmd != nil {
		t.Fatalf("casual unwind must return nil cmd (no pipeline dispatch), got %v", cmd)
	}

	if m.workflowSM.State() != workflow.StateIdle {
		t.Errorf("workflowSM.State() = %v, want StateIdle (unwound)", m.workflowSM.State())
	}
	if m.state != StateChat {
		t.Errorf("UI state = %v, want StateChat", m.state)
	}
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Errorf("resolver mode = /%s, want /ask (unwound)", got)
	}
	if m.streaming || m.agentRunning || m.executionResolving || m.autonomousActive || m.planPending {
		t.Error("execution/streaming flags not cleared after casual unwind")
	}
	// Zero pipeline propagation: no plan synthesis may be staged or pending.
	if m.planPending {
		t.Error("planPending must be false — plan.synthesize must never trigger on casual input")
	}
	text := recordsText(m)
	if !strings.Contains(strings.ToLower(text), "hello") && !strings.Contains(strings.ToLower(text), "how can i") && !strings.Contains(text, "IZEN") {
		t.Errorf("expected a direct conversational response in records, got:\n%s", text)
	}
	if strings.Contains(text, "Synthesizing") || strings.Contains(text, "structured execution plan") {
		t.Errorf("casual prompt leaked into the plan synthesis pipeline:\n%s", text)
	}
}

// TestGateway_CasualPromptAutoUnwindsFromReview covers the same invariant
// from the Review phase: "hello" must unwind StateReviewing → StateIdle.
func TestGateway_CasualPromptAutoUnwindsFromReview(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeReview)
	driveIntoReviewing(t, m)

	if cmd := m.handleInput("hello"); cmd != nil {
		t.Fatalf("casual unwind must return nil cmd (no pipeline dispatch), got %v", cmd)
	}
	if m.workflowSM.State() != workflow.StateIdle {
		t.Errorf("workflowSM.State() = %v, want StateIdle (unwound)", m.workflowSM.State())
	}
	if m.state != StateChat {
		t.Errorf("UI state = %v, want StateChat", m.state)
	}
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Errorf("resolver mode = /%s, want /ask (unwound)", got)
	}
}

// TestGateway_NonCasualPromptDoesNotUnwind guards the complement: a genuine
// task prompt must NOT trigger the auto-unwind path.
func TestGateway_NonCasualPromptDoesNotUnwind(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModePlan)
	driveIntoPlanning(t, m)

	if isCasualConversationPrompt("remove redundant content from @index.html") {
		t.Fatal("mutation request classified as casual conversation")
	}
	if m.handleCasualAutoUnwind("remove redundant content from @index.html") {
		t.Fatal("non-casual prompt must not be consumed by the auto-unwind guard")
	}
	if m.workflowSM.State() != workflow.StatePlanning {
		t.Errorf("workflowSM.State() = %v, want StatePlanning (untouched)", m.workflowSM.State())
	}
}

// TestGateway_GatedLineCasualUnwindsWithoutGateway proves the runGatedLine
// admission guard fires before Gate()/ScopeGuard/WorkerEngine — even with no
// gateway/executor wired (nil-safe), "hi" unwinds instead of reporting
// "execution runtime not wired" or escalating to repository forensics.
func TestGateway_GatedLineCasualUnwindsWithoutGateway(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeBuild)
	driveIntoPlanning(t, m)
	// Push the machine one step further toward an advanced phase.
	if err := m.workflowSM.SendEvent(workflow.EventBuild, workflow.TransitionContext{HasPlan: true, HasCapabilities: true}); err != nil {
		t.Fatalf("build transition: %v", err)
	}
	m.gateway = nil
	m.executor = nil

	if cmd := m.runGatedLine("hi"); cmd != nil {
		t.Fatalf("casual unwind must return nil cmd, got %v", cmd)
	}
	if m.workflowSM.State() != workflow.StateIdle {
		t.Errorf("workflowSM.State() = %v, want StateIdle (unwound)", m.workflowSM.State())
	}
	if m.state != StateChat {
		t.Errorf("UI state = %v, want StateChat", m.state)
	}
	if strings.Contains(recordsText(m), "execution runtime not wired") {
		t.Error("casual prompt must unwind before the gateway-wiring check")
	}
}

// TestGateway_BackwardTransitionHandledGracefully asserts phase transition
// rejections (e.g. Review → Ask backward move) are caught at command
// admission, surfaced as system warnings, and leave the UI predictable.
func TestGateway_BackwardTransitionHandledGracefully(t *testing.T) {
	m := readyChatModel(newTestModel())

	// Build a real backward-transition rejection from the canonical rule.
	rt := domainworkflow.NewWorkflowRuntime()
	if err := rt.Transition(domainworkflow.PhasePlan); err != nil {
		t.Fatalf("setup plan transition: %v", err)
	}
	if err := rt.Transition(domainworkflow.PhaseBuild); err != nil {
		t.Fatalf("setup build transition: %v", err)
	}
	if err := rt.Transition(domainworkflow.PhaseReview); err != nil {
		t.Fatalf("setup review transition: %v", err)
	}
	backErr := rt.Transition(domainworkflow.PhaseAsk)
	if backErr == nil {
		t.Fatal("expected a backward-transition rejection (Review → Ask), got nil")
	}
	if !isBackwardTransitionError(backErr) {
		t.Fatalf("isBackwardTransitionError(%v) = false, want true", backErr)
	}

	// Route it through the runtime-result admission seam (switch_mode path).
	newModel, _ := m.Update(runtimeResultMsg{typ: appruntime.CommandSwitchMode, err: backErr})
	m2 := newModel.(*model)

	text := recordsText(m2)
	if !strings.Contains(text, "phase transition blocked") && !strings.Contains(text, "/reset") {
		t.Errorf("expected a graceful phase-blocked warning in records, got:\n%s", text)
	}
	if m2.state != StateChat {
		t.Errorf("UI state = %v, want StateChat (predictable after rejection)", m2.state)
	}
}
