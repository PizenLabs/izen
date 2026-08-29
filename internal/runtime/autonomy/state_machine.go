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
	// StateAborted is the terminal cancellation position reached by a
	// ProposalCancel or by the anti-loop guard (the same proposal strategy
	// failed twice without altering workspace state). ABORTED is terminal:
	// zero further execution, zero further spend.
	StateAborted ScopeState = "ABORTED"
)

// IsTerminal reports whether the state is terminal.
func (s ScopeState) IsTerminal() bool {
	return s == StateCompleted || s == StateAborted
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

	// ── Anti-loop protection metadata ──────────────────────────────
	// proposalStrategy is the last ProposalIntent selected by the human.
	// proposalFails counts how many times the SAME strategy failed without
	// altering workspace state. When proposalFails reaches the guard threshold
	// (2) the machine forces ABORTED instead of re-offering the same strategy.
	proposalStrategy ProposalIntent
	proposalFails    int
}

// proposalAntiLoopLimit is the maximum number of times the SAME proposal
// strategy may fail without altering workspace state before the guard forces
// ABORTED. Selecting a DIFFERENT strategy resets the counter.
const proposalAntiLoopLimit = 2

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

// ProposalSelected records the human's selected proposal strategy and, unless
// it is a cancel, advances AWAITING_HUMAN_PROPOSAL -> DECIDING so the engine
// constructs the authorized DAG. A ProposalCancel transitions to the terminal
// ABORTED state with zero spend.
//
// Anti-loop protection: the machine tracks how many times the SAME strategy
// has been selected-and-failed without altering workspace state. Selecting a
// DIFFERENT strategy resets the failure counter. When the same strategy is
// re-selected after the guard threshold of failures, the machine forces
// ABORTED instead of looping.
func (m *ScopeStateMachine) ProposalSelected(intent ProposalIntent, fail bool) (ScopeState, error) {
	if m == nil {
		return StateAborted, fmt.Errorf("scope: nil state machine")
	}
	if intent.IsCancel() {
		return m.abort("proposal cancelled: " + string(intent))
	}
	if !intent.Valid() {
		return m.state, fmt.Errorf("scope: invalid proposal intent %q", intent)
	}
	if m.state != StateAwaitingHumanProposal {
		return m.state, fmt.Errorf("scope: proposal requires parked awaiting_human (state=%s)", m.state)
	}
	// Reset the failure counter when the human selects a DIFFERENT strategy.
	if intent != m.proposalStrategy {
		m.proposalStrategy = intent
		m.proposalFails = 0
	}
	if fail {
		m.proposalFails++
	}
	// Anti-loop guard: the SAME strategy has failed enough times without
	// altering state — force ABORTED rather than re-offering it.
	if m.proposalFails >= proposalAntiLoopLimit {
		return m.abort(fmt.Sprintf("proposal anti-loop guard: %s failed %d times without altering state",
			intent, m.proposalFails))
	}
	return m.SendEvent(EventDecide, "proposal selected: "+string(intent))
}

// Repark re-enters the AWAITING_HUMAN_PROPOSAL gate after an execution cycle
// completed without altering workspace state and the re-evaluation barred the
// gate again. It models the post-execution scope re-check so the anti-loop
// guard can observe repeated failures of the SAME strategy. It is refused from
// the terminal states.
func (m *ScopeStateMachine) Repark(reason string) (ScopeState, error) {
	if m == nil {
		return StateObserving, fmt.Errorf("scope: nil state machine")
	}
	if m.state == StateCompleted || m.state == StateAborted {
		return m.state, fmt.Errorf("scope: cannot repark from terminal state %s", m.state)
	}
	if m.state == StateAwaitingHumanProposal {
		return m.state, nil // already parked
	}
	m.push(EventGateBarred, StateAwaitingHumanProposal, reason)
	return StateAwaitingHumanProposal, nil
}

// abort force-terminates the machine at the terminal ABORTED state.
func (m *ScopeStateMachine) abort(reason string) (ScopeState, error) {
	m.push(EventGateBarred, StateAborted, reason)
	return StateAborted, nil
}

// ProposalStrategy returns the last proposal intent the human selected.
func (m *ScopeStateMachine) ProposalStrategy() ProposalIntent {
	if m == nil {
		return ""
	}
	return m.proposalStrategy
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
		// A proposal selection re-enters the decision lane: the human chose a
		// strategy, so the machine advances to DECIDING to construct the
		// authorized DAG. A cancel parks at ABORTED via ProposalSelected, never
		// through a plain event.
		if event == EventDecide {
			return StateDeciding, nil
		}
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
