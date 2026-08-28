package autonomy

import "testing"

// TestScopeMachine_HappyPath verifies the canonical sequence:
// OBSERVING -> EVALUATING_SCOPE -> DECIDING -> STAGING -> EXECUTING ->
// VERIFYING -> COMPLETED.
func TestScopeMachine_HappyPath(t *testing.T) {
	m := NewScopeStateMachine()
	if m.State() != StateObserving {
		t.Fatalf("initial state = %s, want observing", m.State())
	}
	assert := func(want ScopeState, err error, step string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if m.State() != want {
			t.Fatalf("%s: state = %s, want %s", step, m.State(), want)
		}
	}
	if _, err := m.Observe("context collected"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	assert(StateEvaluatingScope, nil, "observe")
	if _, err := m.GatePassed("gate passed"); err != nil {
		t.Fatalf("gate passed: %v", err)
	}
	assert(StateDeciding, nil, "gate passed")
	if _, err := m.SendEvent(EventDecide, "decide"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	assert(StateStaging, nil, "decide")
	if _, err := m.SendEvent(EventStaged, "staged"); err != nil {
		t.Fatalf("staged: %v", err)
	}
	assert(StateExecuting, nil, "staged")
	if _, err := m.SendEvent(EventExecuted, "executed"); err != nil {
		t.Fatalf("executed: %v", err)
	}
	assert(StateVerifying, nil, "executed")
	if _, err := m.SendEvent(EventVerified, "verified"); err != nil {
		t.Fatalf("verified: %v", err)
	}
	assert(StateCompleted, nil, "verified")
	if !m.State().IsTerminal() {
		t.Fatal("completed must be terminal")
	}
}

// TestScopeMachine_ObservingCannotSkipEvaluatingScope pins the core invariant:
// OBSERVING CANNOT transition directly to DECIDING or STAGING. Every attempt is
// refused — the machine MUST pass through EVALUATING_SCOPE first.
func TestScopeMachine_ObservingCannotSkipEvaluatingScope(t *testing.T) {
	m := NewScopeStateMachine()

	if _, err := m.SendEvent(EventGatePassed, "skip"); err == nil {
		t.Fatal("OBSERVING -> DECIDING must be refused (must pass through EVALUATING_SCOPE)")
	}
	if _, err := m.SendEvent(EventDecide, "skip"); err == nil {
		t.Fatal("OBSERVING -> STAGING must be refused (must pass through EVALUATING_SCOPE)")
	}
	if m.State() != StateObserving {
		t.Fatalf("state moved to %s after illegal transitions, want observing", m.State())
	}

	// The gate is the ONLY legal exit from EVALUATING_SCOPE: an attempt to
	// stage directly from it is refused.
	if _, err := m.Observe("observe"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := m.GatePassed("ok"); err != nil {
		t.Fatalf("legal observe->evaluate->gate-passed failed: %v", err)
	}
	if _, err := m.SendEvent(EventStaged, "illegal"); err == nil {
		t.Fatal("EVALUATING_SCOPE -> STAGING must be refused (staging requires DECIDING)")
	}
}

// TestScopeMachine_FailClosedDivertsToAwaitingHuman pins the fail-closed path:
// when the ExecutionGate is closed, EVALUATING_SCOPE diverts to
// AWAITING_HUMAN_PROPOSAL — never to DECIDING or STAGING — and parks there.
func TestScopeMachine_FailClosedDivertsToAwaitingHuman(t *testing.T) {
	m := NewScopeStateMachine()
	if _, err := m.Observe("context collected"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	s, err := m.GateBarred("EVALUATING_SCOPE_BARRIER: corrupt AST")
	if err != nil {
		t.Fatalf("gate barred: %v", err)
	}
	if s != StateAwaitingHumanProposal || m.State() != StateAwaitingHumanProposal {
		t.Fatalf("state = %s, want awaiting_human_proposal", m.State())
	}
	// The parked machine must refuse any advance toward staging/deciding.
	for _, ev := range []ScopeEvent{EventDecide, EventStaged, EventGatePassed, EventExecuted} {
		if _, err := m.SendEvent(ev, "illegal from park"); err == nil {
			t.Fatalf("state %s must refuse event %s while parked", m.State(), ev)
		}
	}
}
