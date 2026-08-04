// Package engine is the state-driven orchestration runtime of Izen v1. It
// owns the Receive -> Analyze -> Plan -> EvaluatePolicy -> Execute -> Validate
// -> Done/Recover state machine and is responsible strictly for orchestration
// and state transitions: every step is delegated to the analyzer, planner,
// policy, registry and metrics packages, and the engine itself performs no
// execution.
package engine

import (
	"errors"
	"sync"
	"time"
)

// State identifies one stage of a runtime run.
type State string

const (
	// StateIdle is the initial state before a run starts.
	StateIdle State = "idle"
	// StateReceived is reached once the request has been received.
	StateReceived State = "received"
	// StateAnalyzed is reached once workspace analysis completed.
	StateAnalyzed State = "analyzed"
	// StatePlanned is reached once the execution plan was derived.
	StatePlanned State = "planned"
	// StatePolicyOK is reached once the declarative policy was evaluated.
	StatePolicyOK State = "policy_evaluated"
	// StateExecuting is reached while the selected strategy runs.
	StateExecuting State = "executing"
	// StateValidating is reached while the validation pipeline runs.
	StateValidating State = "validating"
	// StateRecovering is reached when a step failed and recovery/rollback
	// is being attempted.
	StateRecovering State = "recovering"
	// StateDone is the terminal success state.
	StateDone State = "done"
	// StateRecovered is the terminal state after a successful recovery
	// (rollback).
	StateRecovered State = "recovered"
	// StateFailed is the terminal failure state.
	StateFailed State = "failed"
)

// transitionTable declares the allowed deterministic transitions. Edges to
// StateRecovering are taken when Execute or Validate fails; StateRecovered is
// reached only after a successful recovery, StateFailed after a failed
// recovery, a policy denial or an unrecoverable step error.
var transitionTable = map[State][]State{
	StateIdle:       {StateReceived},
	StateReceived:   {StateAnalyzed},
	StateAnalyzed:   {StatePlanned},
	StatePlanned:    {StatePolicyOK},
	StatePolicyOK:   {StateExecuting, StateFailed},
	StateExecuting:  {StateValidating, StateRecovering},
	StateValidating: {StateDone, StateRecovering},
	StateRecovering: {StateRecovered, StateFailed},
	StateDone:       {},
	StateRecovered:  {},
	StateFailed:     {},
}

// ErrInvalidTransition is returned when a transition is not allowed by the
// transition table.
var ErrInvalidTransition = errors.New("engine: invalid state transition")

// Transition records one state change of a run.
type Transition struct {
	From State
	To   State
	At   time.Time
}

// StateMachine is a deterministic, concurrency-safe state machine driven by
// the explicit transition table.
type StateMachine struct {
	mu      sync.Mutex
	current State
	history []Transition
	clock   func() time.Time
}

// NewStateMachine returns a state machine starting at StateIdle.
func NewStateMachine() *StateMachine {
	return &StateMachine{current: StateIdle, clock: time.Now}
}

// Current returns the current state.
func (sm *StateMachine) Current() State {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current
}

// Can reports whether the transition to next is allowed from the current
// state.
func (sm *StateMachine) Can(next State) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, allowed := range transitionTable[sm.current] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Transition moves the machine to next, recording the transition. It returns
// ErrInvalidTransition when the transition is not allowed.
func (sm *StateMachine) Transition(next State) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, allowed := range transitionTable[sm.current] {
		if allowed == next {
			sm.history = append(sm.history, Transition{From: sm.current, To: next, At: sm.clock()})
			sm.current = next
			return nil
		}
	}
	return ErrInvalidTransition
}

// History returns a snapshot of the recorded transitions in order.
func (sm *StateMachine) History() []Transition {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return append([]Transition(nil), sm.history...)
}

// Reset returns the machine to StateIdle with an empty history.
func (sm *StateMachine) Reset() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.current = StateIdle
	sm.history = sm.history[:0]
}
