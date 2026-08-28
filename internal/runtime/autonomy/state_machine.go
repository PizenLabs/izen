package autonomy

import "fmt"

// ── Zero-Token EVALUATING_SCOPE State Machine ──────────────────────────────
//
// This is the SCOPE-EXECUTION state machine: it governs the structural
// preflight sequence between observation and staging. It is deliberately
// independent of the runtime loop's canonical state vocabulary (which lives in
// internal/autonomy) — it owns ONLY the pre-execution gate ordering and is the
// single authority that enforces the invariant:
//
//	OBSERVING -> EVALUATING_SCOPE -> (DECIDING | AWAITING_HUMAN_PROPOSAL)
//	             -> STAGING -> EXECUTING -> VERIFYING -> COMPLETED
//
// Enforced rule: OBSERVING CANNOT transition directly to DECIDING or STAGING.
// It MUST pass through EVALUATING_SCOPE. That intermediate state is where the
// zero-token local preflight (EvaluateScope / ExecutionGate) runs; when the
// gate is closed the machine diverts to AWAITING_HUMAN_PROPOSAL instead of
// ever reaching DECIDING/STAGING.

// ScopeState is the position of the scope-execution state machine.
type ScopeState string

const (
	// StateObserving collects the target context before any preflight.
	StateObserving ScopeState = "OBSERVING"
	// StateEvaluatingScope runs the ZERO-TOKEN local preflight. The ONLY
	// legal exit is either DECIDING (gate passed) or AWAITING_HUMAN_PROPOSAL
	// (gate closed / fail-closed barrier).
	StateEvaluatingScope ScopeState = "EVALUATING_SCOPE"
	// StateDeciding validates a bounded decision before any action.
	StateDeciding ScopeState = "DECIDING"
	// StateStaging builds/stages the approved plan (manifest / DAG). It is
	// reachable ONLY from DECIDING — never directly from OBSERVING or
	// EVALUATING_SCOPE.
	StateStaging ScopeState = "STAGING"
	// StateExecuting runs the staged mutation units.
	StateExecuting ScopeState = "EXECUTING"
	// StateVerifying consumes the verification outcome of the execution.
	StateVerifying ScopeState = "VERIFYING"
	// StateCompleted is the terminal success position.
	StateCompleted ScopeState = "COMPLETED"
	// StateAwaitingHumanProposal is the fail-closed park position: the
	// EVALUATING_SCOPE gate returned false, so the engine halts the transition
	// to DECIDING/STAGING and diverts here for a human proposal. Zero LLM
	// tokens have been spent.
	StateAwaitingHumanProposal ScopeState = "AWAITING_HUMAN_PROPOSAL"
)

// IsTerminal reports whether the state is terminal.
func (s ScopeState) IsTerminal() bool {
	return s == StateCompleted
}

// String returns the canonical scope-state label.
func (s ScopeState) String() string { return string(s) }

// ScopeEvent is the closed event vocabulary of the scope machine.
type ScopeEvent string

const (
	// EventObserved: the target context has been collected (OBSERVING ->
	// EVALUATING_SCOPE).
	EventObserved ScopeEvent = "observed"
	// EventGatePassed: the zero-token ExecutionGate returned true
	// (EVALUATING_SCOPE -> DECIDING).
	EventGatePassed ScopeEvent = "gate_passed"
	// EventGateBarred: the zero-token ExecutionGate returned false
	// (EVALUATING_SCOPE -> AWAITING_HUMAN_PROPOSAL).
	EventGateBarred ScopeEvent = "gate_barred"
	// EventDecide: a validated decision advances (DECIDING -> STAGING).
	EventDecide ScopeEvent = "decide"
	// EventStaged: the plan is staged (STAGING -> EXECUTING).
	EventStaged ScopeEvent = "staged"
	// EventExecuted: the execution completed (EXECUTING -> VERIFYING).
	EventExecuted ScopeEvent = "executed"
	// EventVerified: verification passed (VERIFYING -> COMPLETED).
	EventVerified ScopeEvent = "verified"
)

// ScopeTransition records one observable step of the scope machine.
type ScopeTransition struct {
	From   ScopeState
	To     ScopeState
	Event  ScopeEvent
	Reason string
}

// String renders the transition compactly.
func (t ScopeTransition) String() string {
	return fmt.Sprintf("%s -> %s (%s)", t.From, t.To, t.Event)
}

// ScopeStateMachine is the fail-closed scope-execution state machine. It owns
// position, enforces the OBSERVING -> EVALUATING_SCOPE ordering, and refuses
// any illegal transition. It is NOT thread-safe (single-lane control flow).
type ScopeStateMachine struct {
	state   ScopeState
	history []ScopeTransition
}

// NewScopeStateMachine returns a machine positioned at OBSERVING.
func NewScopeStateMachine() *ScopeStateMachine {
	return &ScopeStateMachine{state: StateObserving}
}

// State returns the current position.
func (m *ScopeStateMachine) State() ScopeState {
	if m == nil {
		return StateObserving
	}
	return m.state
}

// History returns the observed transitions, oldest first.
func (m *ScopeStateMachine) History() []ScopeTransition {
	if m == nil {
		return nil
	}
	out := make([]ScopeTransition, len(m.history))
	copy(out, m.history)
	return out
}

// SendEvent advances the machine when the transition is legal. It returns the
// destination state and an error for any illegal transition (including the
// invariant violation OBSERVING -> DECIDING/STAGING).
func (m *ScopeStateMachine) SendEvent(event ScopeEvent, reason string) (ScopeState, error) {
	if m == nil {
		return StateObserving, fmt.Errorf("scope: nil state machine")
	}
	next, err := m.lookup(m.state, event)
	if err != nil {
		return m.state, err
	}
	m.push(event, next, reason)
	return next, nil
}

// Observe advances OBSERVING -> EVALUATING_SCOPE. It is the ONLY legal entry
// into the preflight gate.
func (m *ScopeStateMachine) Observe(reason string) (ScopeState, error) {
	return m.SendEvent(EventObserved, reason)
}

// GatePassed advances EVALUATING_SCOPE -> DECIDING when the zero-token
// ExecutionGate returned true.
func (m *ScopeStateMachine) GatePassed(reason string) (ScopeState, error) {
	return m.SendEvent(EventGatePassed, reason)
}

// GateBarred advances EVALUATING_SCOPE -> AWAITING_HUMAN_PROPOSAL when the
// zero-token ExecutionGate returned false (fail-closed). The machine parks
// here; it never proceeds to DECIDING or STAGING.
func (m *ScopeStateMachine) GateBarred(reason string) (ScopeState, error) {
	return m.SendEvent(EventGateBarred, reason)
}

// lookup resolves the destination for a legal event from the given state.
// The core invariant is enforced here: DECIDING and STAGING are reachable only
// via EVALUATING_SCOPE (gate passed) and DECIDING respectively.
func (m *ScopeStateMachine) lookup(from ScopeState, event ScopeEvent) (ScopeState, error) {
	switch from {
	case StateObserving:
		if event == EventObserved {
			return StateEvaluatingScope, nil
		}
	case StateEvaluatingScope:
		switch event {
		case EventGatePassed:
			return StateDeciding, nil
		case EventGateBarred:
			return StateAwaitingHumanProposal, nil
		}
	case StateDeciding:
		if event == EventDecide {
			return StateStaging, nil
		}
	case StateStaging:
		if event == EventStaged {
			return StateExecuting, nil
		}
	case StateExecuting:
		if event == EventExecuted {
			return StateVerifying, nil
		}
	case StateVerifying:
		if event == EventVerified {
			return StateCompleted, nil
		}
	case StateAwaitingHumanProposal:
		// Parked: a fresh run must re-enter through OBSERVING. No self-event.
	}
	// Fall through: illegal transition.
	return from, fmt.Errorf("scope: invalid transition from %s via %s", from, event)
}

// push records one transition and moves the machine.
func (m *ScopeStateMachine) push(event ScopeEvent, to ScopeState, reason string) {
	from := m.state
	m.history = append(m.history, ScopeTransition{From: from, To: to, Event: event, Reason: reason})
	m.state = to
}
