// Package control implements the reconcile control loop of the adaptive
// control system.
//
// The orchestrator is a MINIMAL, pure reconciliation loop:
//
//		Observe → Decide → Execute
//
//	 1. Observe — read the current Dynamic IR (ExecutionSnapshot) from the
//	    session.
//	 2. Decide — the isolated Decision Engine reads the snapshot and returns an
//	    explicit DecisionDirective.
//	 3. Execute — dispatch the directive's work strictly through the WorkerPool,
//	    applying the mechanical state transitions on the session.
//
// The orchestrator owns zero policy: retry, re-plan, skip and human-approval
// decisions are delegated entirely to the Decision Engine. State transitions
// are pure mechanics. Facts flow onto the event bus; directives never do.
package control

import (
	"fmt"
	"sync"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

// Observer exposes the current Dynamic IR. Session implements it.
type Observer interface {
	// Observe returns a deep copy of the current ExecutionSnapshot.
	Observe() *ir.ExecutionSnapshot
}

// Session is the Dynamic IR container the control loop reconciles against. It
// exposes Observe for reading and the mechanical transition methods the loop
// applies to execute directives. A session never decides anything: every
// retry, re-plan, skip and approval decision belongs to the Decision Engine.
type Session interface {
	Observer
	// Plan returns the static plan bound to the session.
	Plan() *ir.Plan
	// MarkRunning transitions nodes Ready → Running (dispatch mechanics).
	MarkRunning(nodeIDs []string) error
	// Apply folds a node observation into the Dynamic IR (Running → Success or
	// Failed, attempt bump, variable mutation).
	Apply(obs ir.ObservationPayload) error
	// ResetForRetry transitions a failed node Failed → Ready (the mechanical
	// effect of a Retry directive).
	ResetForRetry(nodeID string) error
	// Skip absorbs a failed non-critical node as Success (the mechanical
	// effect of a skip directive).
	Skip(nodeID, reason string) error
}

// Session is the concrete Dynamic IR holder.
type session struct {
	plan *ir.Plan
	snap *ir.ExecutionSnapshot

	mu  sync.Mutex
	now func() time.Time
}

// NewSession instantiates a session over an immutable plan.
func NewSession(plan *ir.Plan) Session {
	if plan == nil || plan.Graph == nil {
		panic("control: session requires a non-nil plan with a graph")
	}
	return &session{
		plan: plan,
		snap: ir.NewExecutionSnapshot(plan),
		now:  time.Now,
	}
}

// Observe returns a deep copy of the current Dynamic IR.
func (s *session) Observe() *ir.ExecutionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap.Clone()
}

// Plan returns the static plan bound to the session.
func (s *session) Plan() *ir.Plan { return s.plan }

// MarkRunning transitions the given nodes to Running (the mechanics of dispatch
// to the worker pool). A Pending node whose dependencies are satisfied moves
// Pending → Ready → Running; a node already Ready moves Ready → Running. The
// DECISION that the node is dispatchable lives in the Decision Engine — this
// method only performs the mechanical state moves.
func (s *session) MarkRunning(nodeIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range nodeIDs {
		cur := s.snap.NodeStates[id]
		if cur == ir.StatePending {
			if !cur.Transition(ir.StateReady) {
				return fmt.Errorf("control: illegal transition %s → ready for node %q", cur, id)
			}
			s.snap.NodeStates[id] = ir.StateReady
			cur = ir.StateReady
		}
		if !cur.Transition(ir.StateRunning) {
			return fmt.Errorf("control: illegal transition %s → running for node %q", cur, id)
		}
		s.snap.NodeStates[id] = ir.StateRunning
	}
	s.touchLocked()
	return nil
}

// Apply records a node observation: Running → Success/Failed, increments the
// attempt count and folds VariableMutations into the snapshot.
func (s *session) Apply(obs ir.ObservationPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snap.NodeStates[obs.NodeID]
	if cur != ir.StateRunning {
		return fmt.Errorf("control: apply requires a running node, got %s for node %q", cur, obs.NodeID)
	}
	next := ir.StateSuccess
	if !obs.OK {
		next = ir.StateFailed
	}
	s.snap.NodeStates[obs.NodeID] = next
	s.snap.LastObservation[obs.NodeID] = obs
	s.snap.AttemptCounts[obs.NodeID]++
	for k, v := range obs.VariableMutations {
		s.snap.Variables[k] = v
	}
	s.touchLocked()
	return nil
}

// ResetForRetry transitions a failed node back to Ready (the mechanical effect
// of a Retry directive). Only a failed node may be reset.
func (s *session) ResetForRetry(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snap.NodeStates[nodeID]
	if cur != ir.StateFailed {
		return fmt.Errorf("control: reset for retry requires a failed node, got %s for node %q", cur, nodeID)
	}
	s.snap.NodeStates[nodeID] = ir.StateReady
	s.touchLocked()
	return nil
}

// Skip absorbs a failed non-critical node as Success (the mechanical effect of
// a skip directive — the DECISION to skip lives in the Decision Engine). The
// last observation is annotated with the skip reason so the fact that the node
// was bypassed is never lost. Only a failed node may be skipped.
func (s *session) Skip(nodeID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snap.NodeStates[nodeID]
	if cur != ir.StateFailed {
		return fmt.Errorf("control: skip requires a failed node, got %s for node %q", cur, nodeID)
	}
	s.snap.NodeStates[nodeID] = ir.StateSuccess
	if obs, ok := s.snap.LastObservation[nodeID]; ok {
		obs.SkipReason = reason
		s.snap.LastObservation[nodeID] = obs
	}
	s.touchLocked()
	return nil
}

func (s *session) touchLocked() { s.snap.UpdatedAt = s.now() }
