package ir

// NodeState is the lifecycle of a single execution node. The state machine
// manages PURE MECHANICS only: the five states below and the legal transitions
// between them. It contains ZERO retry, re-plan, skip or human-approval logic —
// every such decision is owned by the Decision Engine, which reads the
// ExecutionSnapshot and returns explicit DecisionDirectives.
type NodeState string

const (
	// StatePending is a node whose dependencies are not yet satisfied.
	StatePending NodeState = "pending"
	// StateReady is a node whose dependencies are satisfied and which awaits
	// dispatch to the worker pool.
	StateReady NodeState = "ready"
	// StateRunning is a node currently executing on the worker pool.
	StateRunning NodeState = "running"
	// StateSuccess is a node that completed with an OK observation (or was
	// absorbed by a decision-engine skip directive).
	StateSuccess NodeState = "success"
	// StateFailed is a node that completed with a failure observation.
	StateFailed NodeState = "failed"
)

// transitionTable is the purely mechanical transition matrix. No transition
// encodes a policy decision; the table answers only "may this state move to
// that state".
var transitionTable = map[NodeState]map[NodeState]bool{
	StatePending: {StateReady: true},
	StateReady:   {StateRunning: true},
	StateRunning: {StateSuccess: true, StateFailed: true},
	StateFailed:  {StateReady: true, StateSuccess: true},
}

// Transition reports whether the mechanical transition from s to next is
// legal. The table encodes ONLY state mechanics:
//
//	pending → ready     dependencies satisfied
//	ready   → running   dispatched to the worker pool
//	running → success   completed with an OK observation
//	running → failed    completed with a failure observation
//	failed  → ready     mechanical reset the control loop applies when it
//	                      executes a Retry directive (the DECISION to retry
//	                      lives exclusively in the Decision Engine)
//	failed  → success   mechanical absorption the control loop applies when it
//	                      executes a skip directive (the DECISION to skip a
//	                      non-critical node lives exclusively in the Decision
//	                      Engine)
func (s NodeState) Transition(next NodeState) bool {
	m, ok := transitionTable[s]
	if !ok {
		return false
	}
	return m[next]
}

// IsTerminal reports whether the state is a terminal lifecycle state.
func (s NodeState) IsTerminal() bool {
	return s == StateSuccess || s == StateFailed
}

// String returns the machine-readable state label.
func (s NodeState) String() string { return string(s) }
