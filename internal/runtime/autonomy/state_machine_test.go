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
	// The parked machine must refuse any advance toward staging/executing.
	// (DECIDING is the legal proposal-selection exit, so it is not refused.)
	for _, ev := range []ScopeEvent{EventStaged, EventGatePassed, EventExecuted} {
		if _, err := m.SendEvent(ev, "illegal from park"); err == nil {
			t.Fatalf("state %s must refuse event %s while parked", m.State(), ev)
		}
	}
}

// TestScopeMachine_ProposalCancelForcesAborted pins invariant 3's cancel path:
// selecting ProposalCancel transitions AWAITING_HUMAN_PROPOSAL -> ABORTED, a
// terminal state, with zero spend.
func TestScopeMachine_ProposalCancelForcesAborted(t *testing.T) {
	m := NewScopeStateMachine()
	if _, err := m.Observe("observe"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := m.GateBarred("barrier"); err != nil {
		t.Fatalf("gate barred: %v", err)
	}
	s, err := m.ProposalSelected(ProposalCancel, false)
	if err != nil {
		t.Fatalf("proposal selected cancel: %v", err)
	}
	if s != StateAborted || m.State() != StateAborted {
		t.Fatalf("state = %s, want aborted", m.State())
	}
	if !m.State().IsTerminal() {
		t.Fatal("aborted must be terminal")
	}
}

// TestScopeMachine_ProposalSelectAdvancesToDeciding pins that a non-cancel
// valid intent advances AWAITING_HUMAN_PROPOSAL -> DECIDING so the engine can
// construct the authorized DAG.
func TestScopeMachine_ProposalSelectAdvancesToDeciding(t *testing.T) {
	m := NewScopeStateMachine()
	if _, err := m.Observe("observe"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := m.GateBarred("barrier"); err != nil {
		t.Fatalf("gate barred: %v", err)
	}
	s, err := m.ProposalSelected(ProposalRepairFirst, false)
	if err != nil {
		t.Fatalf("proposal selected: %v", err)
	}
	if s != StateDeciding || m.State() != StateDeciding {
		t.Fatalf("state = %s, want deciding", m.State())
	}
	if got := m.ProposalStrategy(); got != ProposalRepairFirst {
		t.Fatalf("proposal strategy = %q, want repair_first", got)
	}
}

// TestScopeMachine_ProposalAntiLoopGuardForcesAborted pins invariant 3: the
// SAME proposal strategy selected-and-failed twice without altering state
// forces ABORTED on the third identical selection.
func TestScopeMachine_ProposalAntiLoopGuardForcesAborted(t *testing.T) {
	m := NewScopeStateMachine()

	// Park the machine at the proposal boundary.
	if _, err := m.Observe("observe"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := m.GateBarred("barrier"); err != nil {
		t.Fatalf("gate barred: %v", err)
	}

	// First select of ProposalRepairFirst (no prior failure): advances to
	// deciding and executes.
	s, err := m.ProposalSelected(ProposalRepairFirst, false)
	if err != nil {
		t.Fatalf("first select: %v", err)
	}
	if s != StateDeciding {
		t.Fatalf("first select state = %s, want deciding", s)
	}

	// Re-park (post-execution re-evaluation barred the gate again). The prior
	// attempt failed without altering state, so the second selection carries
	// fail=true and still advances (only ONE failure so far).
	if _, err := m.Repark("re-evaluation barred"); err != nil {
		t.Fatalf("re-park: %v", err)
	}
	s, err = m.ProposalSelected(ProposalRepairFirst, true)
	if err != nil {
		t.Fatalf("second select: %v", err)
	}
	if s != StateDeciding {
		t.Fatalf("second select state = %s, want deciding", s)
	}

	// Re-park a third time: the SAME strategy has now failed TWICE without
	// altering state, so the guard forces ABORTED instead of looping.
	if _, err := m.Repark("re-evaluation barred"); err != nil {
		t.Fatalf("re-park: %v", err)
	}
	s, err = m.ProposalSelected(ProposalRepairFirst, true)
	if err != nil {
		t.Fatalf("third select: %v", err)
	}
	if s != StateAborted || m.State() != StateAborted {
		t.Fatalf("third select state = %s, want aborted (anti-loop guard)", m.State())
	}
}

// TestScopeMachine_ProposalStrategyResetClearsGuard pins that selecting a
// DIFFERENT strategy resets the anti-loop failure counter, so a fresh strategy
// can proceed.
func TestScopeMachine_ProposalStrategyResetClearsGuard(t *testing.T) {
	m := NewScopeStateMachine()
	if _, err := m.Observe("observe"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := m.GateBarred("barrier"); err != nil {
		t.Fatalf("gate barred: %v", err)
	}
	if _, err := m.ProposalSelected(ProposalRepairFirst, false); err != nil {
		t.Fatalf("select repair_first: %v", err)
	}
	if _, err := m.Repark("re-evaluation barred"); err != nil {
		t.Fatalf("re-park: %v", err)
	}
	// A different strategy resets the counter, so it succeeds.
	s, err := m.ProposalSelected(ProposalReduceScope, true)
	if err != nil {
		t.Fatalf("select reduce_scope: %v", err)
	}
	if s != StateDeciding {
		t.Fatalf("state = %s, want deciding after strategy switch", s)
	}
	if m.ProposalStrategy() != ProposalReduceScope {
		t.Fatalf("proposal strategy = %q, want reduce_scope", m.ProposalStrategy())
	}
}

// TestScopeMachine_ProposalRequiresParkedGate pins that a proposal selection
// is refused unless the machine is parked at AWAITING_HUMAN_PROPOSAL.
func TestScopeMachine_ProposalRequiresParkedGate(t *testing.T) {
	m := NewScopeStateMachine()
	if _, err := m.Observe("observe"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := m.ProposalSelected(ProposalRepairFirst, false); err == nil {
		t.Fatal("proposal selection must be refused before the gate is barred")
	}
	// Invalid intents are also refused.
	if _, err := m.GateBarred("barrier"); err != nil {
		t.Fatalf("gate barred: %v", err)
	}
	if _, err := m.ProposalSelected(ProposalIntent("bogus"), false); err == nil {
		t.Fatal("invalid proposal intent must be refused")
	}
}
