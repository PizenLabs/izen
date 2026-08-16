package ui

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// ── Gated-execution lifecycle projection (authority migration) ──────────────
//
// These tests pin the contract that the UI's loading state is a pure projection
// of execution truth: a gated execution starts in "Resolving execution..." and
// EVERY terminal outcome — the returned result carrying an error, or the
// runtime's terminal events (execution.failed / execution.finished / cancelled)
// — MUST clear the loading state, the spinner, and the pending operation. The
// shimmer can never outlive a terminal execution event.

// TestGatedExecutionFailureLeavesResolvingState is the reported bug: the
// provider returns an error, the runtime emits execution.failed +
// execution.finished, but the UI stayed stuck on "Resolving execution...". The
// gated result must be routed through the shared terminal projection which
// finalizes the operation and clears the shimmer.
func TestGatedExecutionFailureLeavesResolvingState(t *testing.T) {
	// A mock with no responses makes every provider call fail, so the executor
	// reaches the model invocation and returns a terminal error.
	mock := &mockProvider{}
	m := gatedDispatchModel(t, mock, map[string]string{"note.txt": "foo\nbar\nbaz\n"})
	m.state = StateChat

	cmd := m.runGatedLine("$hot change bar to qux @note.txt")
	if cmd == nil {
		t.Fatal("gated execution returned nil command")
	}
	// The loading shimmer activates synchronously at dispatch.
	if !m.shimmerActive || m.shimmerText != "Resolving execution..." {
		t.Fatalf("execution shimmer not active at dispatch: active=%v text=%q", m.shimmerActive, m.shimmerText)
	}
	if !m.executionResolving {
		t.Fatal("execution in-flight marker not set at dispatch")
	}

	// Run the worker: the provider fails, the executor returns a terminal
	// result alongside the error.
	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("expected gatedExecutionMsg, got %T", msg)
	}
	if gem.err == nil {
		t.Fatal("expected the provider failure to surface as an execution error")
	}
	if gem.res == nil {
		t.Fatal("expected the execution result to accompany the error")
	}

	// Project the terminal result: the UI must leave the resolving state.
	res, _ := m.handleGatedExecution(gem)
	m2 := res.(*model)
	if m2.shimmerActive {
		t.Fatal("spinner still active after a terminal execution failure")
	}
	if m2.executionResolving {
		t.Fatal("execution in-flight marker survived a terminal execution failure")
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy flags still set after a terminal execution failure")
	}
	if m2.state == StateProcessing {
		t.Fatalf("state stuck in Processing after a terminal execution failure: %v", m2.state)
	}
}

// TestTerminalExecutionEventClearsResolvingState pins the projection-layer
// guarantee: every terminal execution event — execution.failed,
// execution.finished (success), and a cancelled execution — MUST clear the
// loading state, spinner, and pending operation, even when the result message
// has not arrived yet. The UI only projects execution truth, and a terminal
// event IS that truth.
func TestTerminalExecutionEventClearsResolvingState(t *testing.T) {
	cases := []struct {
		name string
		evs  []events.DomainEvent
	}{
		{
			name: "execution failed",
			evs: []events.DomainEvent{
				events.NewExecutionFailed(events.FailureRecoverable, errors.New("boom"), "executor.model"),
				events.NewExecutionFinished("r1", false, "failed"),
			},
		},
		{
			name: "execution finished success",
			evs: []events.DomainEvent{
				events.NewExecutionFinished("r2", true, "completed"),
			},
		},
		{
			name: "execution cancelled",
			evs: []events.DomainEvent{
				events.NewExecutionFinished("r3", false, "cancelled"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.state = StateChat
			m.startShimmer("Resolving execution...", "execution")
			m.agentRunning = true
			m.agentLabel = "resolving execution"
			m.executionResolving = true

			for _, ev := range tc.evs {
				m.handleDomainEvent(ev)
			}

			if m.shimmerActive {
				t.Fatal("terminal event left the shimmer active")
			}
			if m.executionResolving {
				t.Fatal("terminal event left the execution in-flight marker set")
			}
			if m.isWorkflowBusy() {
				t.Fatal("terminal event left busy flags set")
			}
			if m.shimmerText != "" {
				t.Fatalf("terminal event left stale shimmer text: %q", m.shimmerText)
			}
		})
	}
}

// TestTerminalEventDoesNotClearUnrelatedOperation pins that a terminal runtime
// event can never clobber an operation that superseded the resolving phase: a
// new operation clears the in-flight marker, so a late terminal event is a
// no-op for the newer operation's loading state.
func TestTerminalEventDoesNotClearUnrelatedOperation(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.startShimmer("Resolving execution...", "execution")
	m.agentRunning = true
	m.agentLabel = "resolving execution"
	m.executionResolving = true

	// A new operation supersedes the resolving phase.
	m.beginOperation(OpHotfix)
	m.startShimmer("Applying hotfix...", "execute")
	if m.executionResolving {
		t.Fatal("new operation must clear the execution in-flight marker")
	}

	// A late terminal execution event must not clear the newer operation's
	// shimmer.
	m.handleDomainEvent(events.NewExecutionFinished("r4", true, "completed"))
	if !m.shimmerActive {
		t.Fatal("late terminal event cleared an unrelated operation's shimmer")
	}
	if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
		t.Fatalf("late terminal event released an unrelated operation: %+v", m.activeOp)
	}
}
