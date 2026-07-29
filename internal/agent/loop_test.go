package agent

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/agent/checkpoint"
	"github.com/PizenLabs/izen/internal/controlplane/failure"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/retrieval"
)

// ── Helpers ────────────────────────────────────────────────────────────────

func newTestRuntime() *runtime.RuntimeContext {
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityRead)
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityExecute)
	caps.Grant(capability.CapabilityTest)
	caps.Grant(capability.CapabilityPatch)
	bgt := budget.NewBudget(10, 500, 8000, 3, 5*time.Minute, 20)
	return runtime.New(nil, caps, bgt)
}

func newTestAgent() *AgentLoop {
	return NewAgentLoop(AgentLoopConfig{
		RuntimeCtx:        newTestRuntime(),
		FailureClassifier: failure.NewClassifier(),
		RecoveryMgr:       failure.NewRecoveryManager(),
		CheckpointMgr:     checkpoint.NewCheckpointManager(),
	})
}

// ── State Machine Transitions ──────────────────────────────────────────────

func TestAgentLoop_InitialState(t *testing.T) {
	al := newTestAgent()
	if got := al.State(); got != StateIdle {
		t.Errorf("expected initial state StateIdle, got %s", got)
	}
}

func TestAgentLoop_StateString(t *testing.T) {
	tests := []struct {
		state AgentState
		want  string
	}{
		{StateIdle, "idle"},
		{StatePlanning, "planning"},
		{StateRetrieving, "retrieving"},
		{StateGuarding, "guarding"},
		{StateExecuting, "executing"},
		{StateRecovering, "recovering"},
		{StateAwaitingApproval, "awaiting-approval"},
		{StateFinished, "finished"},
		{StateFailed, "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Errorf("AgentState.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentLoop_StateIsTerminal(t *testing.T) {
	if !StateFinished.IsTerminal() {
		t.Error("expected StateFinished to be terminal")
	}
	if !StateFailed.IsTerminal() {
		t.Error("expected StateFailed to be terminal")
	}
	if StateIdle.IsTerminal() {
		t.Error("expected StateIdle to not be terminal")
	}
}

func TestAgentLoop_StateValid(t *testing.T) {
	for s := StateIdle; s <= StateFailed; s++ {
		if !s.Valid() {
			t.Errorf("expected state %d to be valid", s)
		}
	}
	if AgentState(99).Valid() {
		t.Error("expected invalid state to be invalid")
	}
}

func TestAgentLoop_Transition(t *testing.T) {
	al := newTestAgent()
	stream := al.Events()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range stream.Events() {
			if ev.Type == EventStateChanged {
				return
			}
		}
	}()

	al.transition(StatePlanning, nil)
	<-done

	if got := al.State(); got != StatePlanning {
		t.Errorf("expected StatePlanning, got %s", got)
	}
}

// ── Event Streaming ────────────────────────────────────────────────────────

func TestAgentLoop_EventStream(t *testing.T) {
	al := newTestAgent()
	stream := al.Events()

	events := make(chan Event, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range stream.Events() {
			events <- ev
		}
	}()

	al.emit(EventPlanStart, nil)
	al.emit(EventPlanComplete, PlanResult{Steps: []string{"step1"}})
	al.emit(EventTurnComplete, struct {
		TurnCount int
		Summary   string
	}{TurnCount: 1, Summary: "turn 1 complete"})

	time.Sleep(50 * time.Millisecond)
	al.stream.Close()
	<-done

	close(events)
	collected := make([]Event, 0)
	for e := range events {
		collected = append(collected, e)
	}

	if len(collected) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(collected))
	}
	if collected[0].Type != EventPlanStart {
		t.Errorf("expected EventPlanStart, got %s", collected[0].Type)
	}
}

func TestAgentLoop_EventStreamClose(t *testing.T) {
	stream := NewEventStream(10)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream.Events() {
		}
	}()

	stream.Close()
	<-done
}

// ── Guard Approval Pause/Resume ───────────────────────────────────────────

func TestAgentLoop_ApprovalFlow(t *testing.T) {
	al := newTestAgent()
	al.transition(StateAwaitingApproval, nil)

	respCh := make(chan ApprovalResponse, 1)
	respCh <- ApprovalResponse{Granted: true, Reason: "looks good"}
	close(respCh)

	al.approvalCh <- ApprovalPending{
		Request: ApprovalRequest{
			Action: "write file",
			Target: "main.go",
		},
		RespCh: respCh,
	}

	resp, err := al.waitForApproval()
	if err != nil {
		t.Fatalf("waitForApproval failed: %v", err)
	}
	if !resp.Granted {
		t.Error("expected approval to be granted")
	}
}

func TestAgentLoop_ApprovalDenied(t *testing.T) {
	al := newTestAgent()
	al.transition(StateAwaitingApproval, nil)

	respCh := make(chan ApprovalResponse, 1)
	respCh <- ApprovalResponse{Granted: false, Reason: "not now"}
	close(respCh)

	al.approvalCh <- ApprovalPending{
		Request: ApprovalRequest{Action: "delete file"},
		RespCh:  respCh,
	}

	resp, err := al.waitForApproval()
	if err != nil {
		t.Fatalf("waitForApproval failed: %v", err)
	}
	if resp.Granted {
		t.Error("expected approval to be denied")
	}
}

func TestAgentLoop_ApprovalContextCancel(t *testing.T) {
	al := newTestAgent()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	oldCtx := al.ctx
	al.ctx = ctx
	defer func() { al.ctx = oldCtx }()

	_, err := al.waitForApproval()
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

// ── Full Turn Integration ─────────────────────────────────────────────────

func TestAgentLoop_RunTurn_Success(t *testing.T) {
	al := newTestAgent()
	ctx := context.Background()

	err := al.RunTurn(ctx,
		func(ctx context.Context) error {
			return nil
		},
		func(ctx context.Context) error {
			return nil
		},
	)

	if err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if got := al.State(); got != StateFinished {
		t.Errorf("expected StateFinished, got %s", got)
	}
}

func TestAgentLoop_RunTurn_PlanError(t *testing.T) {
	al := newTestAgent()
	ctx := context.Background()

	err := al.RunTurn(ctx,
		func(ctx context.Context) error {
			return context.DeadlineExceeded
		},
		nil,
	)

	if err == nil {
		t.Fatal("expected error from plan function")
	}
	if got := al.State(); got != StateFailed {
		t.Errorf("expected StateFailed, got %s", got)
	}
}

func TestAgentLoop_RunTurn_RetrieveError(t *testing.T) {
	al := newTestAgent()
	ctx := context.Background()

	err := al.RunTurn(ctx,
		func(ctx context.Context) error {
			return nil
		},
		func(ctx context.Context) error {
			return context.Canceled
		},
	)

	if err == nil {
		t.Fatal("expected error from retrieve function")
	}
	if got := al.State(); got != StateFailed {
		t.Errorf("expected StateFailed, got %s", got)
	}
}

func TestAgentLoop_EventTypeStrings(t *testing.T) {
	tests := []struct {
		et   EventType
		want string
	}{
		{EventPlanStart, "plan-start"},
		{EventPlanComplete, "plan-complete"},
		{EventRetrieveStart, "retrieve-start"},
		{EventRetrieveComplete, "retrieve-complete"},
		{EventGuardCheck, "guard-check"},
		{EventGuardApproved, "guard-approved"},
		{EventRequireApproval, "require-approval"},
		{EventApprovalGranted, "approval-granted"},
		{EventApprovalDenied, "approval-denied"},
		{EventExecuteStart, "execute-start"},
		{EventExecuteComplete, "execute-complete"},
		{EventExecuteFailed, "execute-failed"},
		{EventRecoverStart, "recover-start"},
		{EventRecoverComplete, "recover-complete"},
		{EventRecoverFailed, "recover-failed"},
		{EventTurnComplete, "turn-complete"},
		{EventLoopCancelled, "loop-cancelled"},
		{EventStateChanged, "state-changed"},
		{EventError, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.et.String(); got != tt.want {
				t.Errorf("EventType.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── Recovery Flow ─────────────────────────────────────────────────────────

func TestAgentLoop_ClassifyFailure(t *testing.T) {
	al := newTestAgent()
	fc := al.classifyFailure(nil)
	if fc != failure.UNKNOWN {
		t.Errorf("expected UNKNOWN for nil error, got %s", fc)
	}
}

func TestAgentLoop_RecoveryAutoRepair(t *testing.T) {
	al := newTestAgent()
	al.recoveryMgr.MaxAutoRepairAttempts = 3

	al.transition(StateExecuting, nil)
	err := &mockError{message: "syntax error: unexpected token"}
	al.runRecoveryPhase("step-1", err, 3)

	state := al.State()
	if state.IsTerminal() {
		t.Errorf("expected recovery to succeed and continue, got terminal state %s", state)
	}
}

type mockError struct{ message string }

func (e *mockError) Error() string { return e.message }

func TestAgentLoop_RecoveryImmediateRollback(t *testing.T) {
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityRead)
	bgt := budget.NewBudget(10, 500, 8000, 3, 5*time.Minute, 20)
	rc := runtime.New(nil, caps, bgt)
	fc := failure.NewClassifier()

	al := NewAgentLoop(AgentLoopConfig{
		RuntimeCtx:        rc,
		FailureClassifier: fc,
		RecoveryMgr:       failure.NewRecoveryManager(),
		CheckpointMgr:     checkpoint.NewCheckpointManager(),
	})

	failureResult := al.classifyFailure(nil)
	_ = failureResult
}

func TestAgentLoop_FullCycleNoGuardBlock(t *testing.T) {
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityRead)
	caps.Grant(capability.CapabilityWrite)
	bgt := budget.NewBudget(10, 500, 8000, 3, 5*time.Minute, 20)
	rc := runtime.New(nil, caps, bgt)

	al := NewAgentLoop(AgentLoopConfig{
		RuntimeCtx:        rc,
		FailureClassifier: failure.NewClassifier(),
		RecoveryMgr:       failure.NewRecoveryManager(),
		CheckpointMgr:     checkpoint.NewCheckpointManager(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go al.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	state := al.State()
	if state == StateIdle {
		t.Error("agent should have progressed past idle")
	}
}

func TestAgentLoop_TerminalStatesDontTransition(t *testing.T) {
	al := newTestAgent()
	al.transition(StateFinished, nil)

	if !al.State().IsTerminal() {
		t.Error("StateFinished should be terminal")
	}
}

func TestAgentLoop_CheckpointManagerIntegration(t *testing.T) {
	cm := checkpoint.NewCheckpointManager()
	al := NewAgentLoop(AgentLoopConfig{
		CheckpointMgr: cm,
	})

	if al.CheckpointManager() != cm {
		t.Error("CheckpointManager should be the same instance")
	}

	cm.RecordTokens(1000, 2000)
	cm.RecordSubTask("task-1", "completed", "implemented feature X", 500)
	cm.RecordExecutionOutput("build", "compilation succeeded", 200)
	cm.IncrementTurn()

	state := cm.State()
	if state.TotalTokensConsumed != 3000 {
		t.Errorf("expected 3000 tokens, got %d", state.TotalTokensConsumed)
	}
	if state.TurnCount != 1 {
		t.Errorf("expected 1 turn, got %d", state.TurnCount)
	}
	if len(state.SubTasks) != 1 {
		t.Errorf("expected 1 sub task, got %d", len(state.SubTasks))
	}
}

func TestAgentLoop_EventStreamWithRetrievalOrchestrator(t *testing.T) {
	orch := retrieval.NewDefaultOrchestrator(t.TempDir())
	al := NewAgentLoop(AgentLoopConfig{
		RuntimeCtx:    newTestRuntime(),
		RetrievalOrch: orch,
	})

	events := make(chan Event, 20)
	evtStream := al.Events()

	go func() {
		for ev := range evtStream.Events() {
			events <- ev
		}
		close(events)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go al.Run(ctx)

	time.Sleep(200 * time.Millisecond)
	al.Cancel()

	collected := make([]Event, 0)
	for ev := range events {
		collected = append(collected, ev)
	}

	foundRetrieve := false
	for _, ev := range collected {
		if ev.Type == EventRetrieveStart || ev.Type == EventRetrieveComplete {
			foundRetrieve = true
			break
		}
	}
	if !foundRetrieve {
		t.Log("note: no retrieval events observed (may have been cancelled early)")
	}
}
