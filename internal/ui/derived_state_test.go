package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/core/workflow"
	domainworkflow "github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/presentation"
	"github.com/PizenLabs/izen/pkg/tui/components/shimmer"
)

// TestEnterApprovalStateDerivesFromWorkflowGate asserts the approval
// presentation state is derived from the canonical WorkflowStateMachine
// pending-approval signal (the single source of truth), not an independent UI
// flag.
func TestEnterApprovalStateDerivesFromWorkflowGate(t *testing.T) {
	m := newTestModel()
	m.state = StateChat

	m.enterApprovalState()

	if !m.workflowSM.PendingApproval() {
		t.Error("workflowSM.PendingApproval() = false after enterApprovalState")
	}
	if m.state != StateAwaitingApproval {
		t.Errorf("state = %v, want StateAwaitingApproval (derived)", m.state)
	}
}

// TestResolveApprovalStateClearsWorkflowGate asserts approve/reject/cancel all
// funnel through the canonical gate and re-derive the presentation state.
func TestResolveApprovalStateClearsWorkflowGate(t *testing.T) {
	m := newTestModel()
	m.enterApprovalState()
	if m.state != StateAwaitingApproval {
		t.Fatalf("precondition: state = %v, want StateAwaitingApproval", m.state)
	}

	m.resolveApprovalState()

	if m.workflowSM.PendingApproval() {
		t.Error("workflowSM.PendingApproval() = true after resolveApprovalState")
	}
	if m.state != StateChat {
		t.Errorf("state = %v, want StateChat (idle, no in-flight operation)", m.state)
	}
}

// TestPhaseChangedEventDerivesProcessing asserts a workflow phase-change event
// projects onto StateProcessing while an operation is in flight and falls back
// to StateChat when resting.
func TestPhaseChangedEventDerivesProcessing(t *testing.T) {
	m := newTestModel()
	m.streaming = true
	m.agentRunning = true

	m.handleDomainEvent(events.NewPhaseChanged("ask", "plan"))

	if m.state != StateProcessing {
		t.Errorf("state = %v, want StateProcessing while in-flight", m.state)
	}

	// Operation completes: resting in a mode phase must not gate the input.
	m.streaming = false
	m.agentRunning = false
	m.syncUIState()
	if m.state != StateChat {
		t.Errorf("state = %v, want StateChat when no operation is in flight", m.state)
	}
}

// TestApprovalRequestedEventDerivesState asserts a Tier 4 EventApprovalRequested
// on the shared bus drives the workflow pending-approval gate and the derived
// AwaitingApproval presentation state.
func TestApprovalRequestedEventDerivesState(t *testing.T) {
	m := newTestModel()
	m.state = StateChat

	m.handleDomainEvent(events.NewApprovalRequested("fix.go", "approval required", ""))

	if !m.workflowSM.PendingApproval() {
		t.Error("workflowSM.PendingApproval() = false after approval-requested event")
	}
	if m.state != StateAwaitingApproval {
		t.Errorf("state = %v, want StateAwaitingApproval (derived from event)", m.state)
	}
}

// TestWorkflowSMApprovalGateTransitions guards the canonical gate lifecycle:
// pending -> resolved -> pending round-trips and stays consistent with the
// presentation derivation. The SM is owned by the UI goroutine (like the rest
// of the WorkflowStateMachine), so access is single-threaded here.
func TestWorkflowSMApprovalGateTransitions(t *testing.T) {
	sm := workflow.NewWorkflowStateMachine()
	if sm.PendingApproval() {
		t.Fatal("fresh state machine should not be awaiting approval")
	}
	for i := 0; i < 100; i++ {
		sm.MarkApprovalPending()
		if !sm.PendingApproval() {
			t.Fatalf("iteration %d: pending flag not set", i)
		}
		if got := presentation.DeriveUIState(sm.State().String(), sm.PendingApproval(), false); got != StateAwaitingApproval {
			t.Fatalf("iteration %d: derived state = %v, want StateAwaitingApproval", i, got)
		}
		sm.MarkApprovalResolved()
		if sm.PendingApproval() {
			t.Fatalf("iteration %d: pending flag not cleared", i)
		}
	}
}

// driveIntoBuildPhase moves both workflow runtimes into the build phase, as
// /build does before an execution attempt starts.
func driveIntoBuildPhase(t *testing.T, m *model) {
	t.Helper()
	if err := m.workflowSM.SendEvent(workflow.EventPlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("plan transition: %v", err)
	}
	if err := m.workflowSM.SendEvent(workflow.EventBuild, workflow.TransitionContext{HasPlan: true, HasCapabilities: true}); err != nil {
		t.Fatalf("build transition: %v", err)
	}
	if m.workflowSM.State() != workflow.StateBuilding {
		t.Fatalf("core SM state = %v, want StateBuilding", m.workflowSM.State())
	}
	if err := m.workflowRT.Transition(domainworkflow.PhaseBuild); err != nil {
		t.Fatalf("domain build transition: %v", err)
	}
	if m.workflowRT.Phase() != domainworkflow.PhaseBuild {
		t.Fatalf("domain phase = %v, want build", m.workflowRT.Phase())
	}
}

// TestBuildFailedMsgUnwindsWorkflowToChat is the regression guard for the
// workflow phase lock: when the fast-track build stream fails (HTTP 400/500,
// network error), the state machine must NOT stay trapped in the build phase.
// The core SM returns to StateIdle, the domain runtime returns to PhaseAsk and
// the presentation state re-derives to interactive StateChat.
func TestBuildFailedMsgUnwindsWorkflowToChat(t *testing.T) {
	m := newTestModel()
	m.shimmerAnim = shimmer.New("")
	m.streaming = true
	m.agentRunning = true
	m.state = StateProcessing
	driveIntoBuildPhase(t, m)

	newModel, _ := m.Update(buildFailedMsg{Err: errors.New("openrouter: status 400")})
	m2 := newModel.(*model)

	if m2.workflowSM.State() != workflow.StateIdle {
		t.Errorf("core SM state = %v, want StateIdle (unwound)", m2.workflowSM.State())
	}
	if m2.workflowRT.Phase() != domainworkflow.PhaseAsk {
		t.Errorf("domain phase = %v, want ask (unwound)", m2.workflowRT.Phase())
	}
	if m2.state != StateChat {
		t.Errorf("UI state = %v, want StateChat (interactive)", m2.state)
	}
	if m2.streaming || m2.agentRunning {
		t.Error("transient processing flags not cleared after build failure")
	}
	if !m2.ti.Focused() {
		t.Error("textinput should be focused after build failure recovery")
	}
}

// TestBuildFailedMsgAllowsSubsequentPrompt asserts the recovery is actionable:
// after a build stream failure the next user prompt can route forward again.
// The domain runtime accepts a fresh build transition from Ask and the core SM
// accepts a fresh /plan transition from Idle — no "transition from build to
// ask" rejection remains.
func TestBuildFailedMsgAllowsSubsequentPrompt(t *testing.T) {
	m := newTestModel()
	m.shimmerAnim = shimmer.New("")
	driveIntoBuildPhase(t, m)

	newModel, _ := m.Update(buildFailedMsg{Err: errors.New("stream failed: EOF")})
	m2 := newModel.(*model)

	// submit_prompt routing: ask -> build is a legal forward transition again.
	if err := m2.workflowRT.Transition(domainworkflow.PhaseBuild); err != nil {
		t.Fatalf("post-failure domain transition should succeed, got: %v", err)
	}
	// /plan mode entry from the unwound Idle state is legal.
	if err := m2.workflowSM.SendEvent(workflow.EventPlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("post-failure plan transition should succeed, got: %v", err)
	}
}

// TestCtrlOTogglesOutputTraceForNonReasoningModel is the regression guard for
// the disabled Ctrl+O thinking viewport on SLMs: a model that emits no formal
// reasoning channel (e.g. Gemma) leaves the ThinkingBuffer empty, so Ctrl+O
// must fall back to toggling the raw output-trace viewport instead of doing
// nothing.
func TestCtrlOTogglesOutputTraceForNonReasoningModel(t *testing.T) {
	m := newTestModel()
	// Non-reasoning model: no thinking block, but a raw streamed trace exists.
	m.thinkingBuffer = NewThinkingBuffer()
	m.traceBuffer.Reset()
	m.traceBuffer.WriteString("<html>\n<body>hello</body>\n</html>\n")

	if !m.toggleThoughtBlock() {
		t.Fatal("toggleThoughtBlock should handle the output-trace fallback")
	}
	if !m.traceExpanded {
		t.Error("traceExpanded should be true after Ctrl+O with no thinking content")
	}
	if !m.toggleThoughtBlock() {
		t.Fatal("toggleThoughtBlock should collapse on the second press")
	}
	if m.traceExpanded {
		t.Error("traceExpanded should be false after the second Ctrl+O")
	}
}

// TestRenderOutputTraceExpands asserts the expanded trace viewport renders the
// raw streamed output (header + content) so the user can inspect exactly what
// a non-reasoning model produced.
func TestRenderOutputTraceExpands(t *testing.T) {
	m := newTestModel()
	m.traceBuffer.Reset()
	m.traceBuffer.WriteString("line1\nline2\n")
	m.traceExpanded = true

	rendered := m.renderOutputTrace(80)
	if !strings.Contains(rendered, "OUTPUT TRACE") {
		t.Errorf("trace render missing header: %q", rendered)
	}
	if !strings.Contains(rendered, "line1") || !strings.Contains(rendered, "line2") {
		t.Errorf("trace render missing content: %q", rendered)
	}
	if !strings.Contains(rendered, "Ctrl+O collapse") {
		t.Errorf("trace render missing collapse hint: %q", rendered)
	}
}

// TestFastTrackFullCoverageBypassesPerTaskExecution is the regression guard for
// the fast-track → per-task execution leak: when a fast-track batch applied
// patches for every plan target, the build loop MUST complete immediately and
// MUST NOT fall through to "executing step N: FILE_MUTATE" (Rule "Explicit
// Over Implicit"). The fast-track producer sets currentBuildTaskID to 0, so
// the mutation-result completion path must recognize full coverage and drain
// the queue instead of advancing handleBuildRun(0).
func TestFastTrackFullCoverageBypassesPerTaskExecution(t *testing.T) {
	m := newTestModel()
	driveIntoBuildPhase(t, m)
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	m.shimmerAnim = shimmer.New("")

	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "styles.css", Status: "idle"},
		{StepNum: 3, Type: "FILE_MUTATE", Target: "script.js", Status: "idle"},
	}
	m.sess.StageTaskList(&tasks)
	// Fast-track producer leaves the unified session task id at 0 (see
	// runBuildFastTrack) and records the covered plan targets.
	m.currentBuildTaskID = 0
	m.fastTrackTargets = map[string]bool{
		"index.html": true,
		"styles.css": true,
		"script.js":  true,
	}
	m.pendingProposals = nil
	m.awaitingConfirmation = false

	// Simulate the final fast-track mutation result arriving after all three
	// proposals were approved and applied.
	newModel, cmd := m.Update(mutationResultMsg{file: "script.js", status: "modified"})
	m2 := newModel.(*model)

	if cmd == nil {
		t.Fatal("expected a non-nil completion command (verification) after fast-track full coverage")
	}
	for _, task := range m2.sess.CurrentTasks {
		if task.Status != "completed" {
			t.Errorf("plan task %d (%s) status = %q, want completed — per-task execution was NOT bypassed", task.StepNum, task.Target, task.Status)
		}
	}
	if !m2.buildVerifyPending {
		t.Error("buildVerifyPending = false, want true — build must transition to completion, not per-task execution")
	}
	if len(m2.fastTrackTargets) != 0 {
		t.Error("fastTrackTargets not cleared after fast-track completion")
	}
	if m2.state != StateChat {
		t.Errorf("UI state = %v, want StateChat (interactive) after fast-track completion", m2.state)
	}
	if !m2.ti.Focused() {
		t.Error("textinput should be focused after fast-track completion")
	}
}

// TestFastTrackPartialCoverageFallsThroughToPerTask guards the complement: a
// fast-track batch covering only SOME plan targets must NOT short-circuit the
// build — remaining idle tasks still advance to per-task execution.
func TestFastTrackPartialCoverageFallsThroughToPerTask(t *testing.T) {
	m := newTestModel()
	driveIntoBuildPhase(t, m)
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	m.shimmerAnim = shimmer.New("")

	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "styles.css", Status: "idle"},
	}
	m.sess.StageTaskList(&tasks)
	m.currentBuildTaskID = 0
	// Only one of two targets was covered by the fast-track batch.
	m.fastTrackTargets = map[string]bool{"index.html": true}
	m.pendingProposals = nil
	m.awaitingConfirmation = false

	if m.fastTrackCoversAllPlanTargets() {
		t.Fatal("partial coverage must not report full coverage")
	}
}
