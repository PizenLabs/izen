package ir

import (
	"fmt"
	"sync/atomic"
	"time"
)

// MutationAction classifies a file mutation captured by an observation.
type MutationAction string

const (
	// ActionCreated is a newly created file.
	ActionCreated MutationAction = "created"
	// ActionModified is an existing file rewritten in place.
	ActionModified MutationAction = "modified"
	// ActionDeleted is a removed file.
	ActionDeleted MutationAction = "deleted"
)

// FileMutation is a single file mutation captured by an ObservationPayload.
type FileMutation struct {
	Path    string
	Action  MutationAction
	Content string
}

// SignalKind classifies an environment signal captured by an observation.
type SignalKind string

const (
	// SignalWarning is a soft environment note (e.g. a missing optional file).
	SignalWarning SignalKind = "warning"
	// SignalGraphInvalidation is emitted when live tool output proves the
	// static graph can no longer be satisfied. The RePlanTrigger policy
	// consumes this signal to emit a RePlan directive.
	SignalGraphInvalidation SignalKind = "graph_invalidation"
)

// EnvSignal is a discrete environment observation (missing file, missing
// command, toolchain invalidation, ...) captured during execution.
type EnvSignal struct {
	Kind  SignalKind
	Name  string
	Value string
}

// ObservationPayload is the Dynamic IR record of one node execution: the live
// tool output, the file mutations the node produced and the environment
// signals it observed. It is the ONLY way facts flow from execution back into
// the control loop.
type ObservationPayload struct {
	// NodeID identifies the node that produced the observation.
	NodeID string
	// Output is the raw live tool output.
	Output string
	// FileMutations lists the file mutations observed.
	FileMutations []FileMutation
	// EnvSignals lists the environment signals observed.
	EnvSignals []EnvSignal
	// VariableMutations fold new values into the snapshot Variables.
	VariableMutations map[string]string
	// OK reports whether the node completed successfully.
	OK bool
	// Err is the machine-readable failure, when the node failed.
	Err string
	// SkipReason, when non-empty, records that the node was absorbed by a
	// decision-engine skip directive (self-healing without re-planning).
	SkipReason string
	// Timestamp records when the observation was captured.
	Timestamp time.Time
}

// Variables is the mutable variable surface of an execution snapshot. Nodes
// read variables before running and fold VariableMutations into them on
// completion.
type Variables map[string]string

// ExecutionSnapshot is the Dynamic IR: the mutable runtime state of one plan
// execution. It binds the Static IR (the Plan) to the live node states, the
// last observation of each node, per-node attempt counts and the variables
// flowing between steps.
//
// The snapshot is the ONLY input of the Decision Engine. The Decision Engine
// never mutates it — mutation is confined to the control loop's session
// transitions.
type ExecutionSnapshot struct {
	// ID uniquely identifies the execution.
	ID string
	// Plan is the static plan this execution instantiates.
	Plan *Plan

	// NodeStates maps node id to its lifecycle state.
	NodeStates map[string]NodeState
	// LastObservation maps node id to its most recent observation.
	LastObservation map[string]ObservationPayload
	// AttemptCounts maps node id to the number of times it has been executed.
	AttemptCounts map[string]int
	// Variables carries the values flowing between nodes.
	Variables Variables

	// UpdatedAt records the last mutation time.
	UpdatedAt time.Time
}

var snapIDCounter atomic.Uint64

func newSnapshotID() string {
	return fmt.Sprintf("snap-%d", snapIDCounter.Add(1))
}

// NewExecutionSnapshot instantiates a fresh Dynamic IR for a plan: every node
// starts in Pending with zero attempts.
func NewExecutionSnapshot(plan *Plan) *ExecutionSnapshot {
	snap := &ExecutionSnapshot{
		ID:              newSnapshotID(),
		Plan:            plan,
		NodeStates:      make(map[string]NodeState, plan.Graph.Len()),
		LastObservation: make(map[string]ObservationPayload, plan.Graph.Len()),
		AttemptCounts:   make(map[string]int, plan.Graph.Len()),
		Variables:       Variables{},
		UpdatedAt:       time.Now(),
	}
	for _, node := range plan.Graph.Nodes() {
		snap.NodeStates[node.ID] = StatePending
	}
	return snap
}

// SnapshotReader is the read-only projection of an ExecutionSnapshot. It is
// the ONLY interface the Decision Engine consumes: the Decision Engine
// verifiably cannot mutate the Dynamic IR because no mutator is exposed. The
// control loop alone transitions the snapshot.
type SnapshotReader interface {
	// StaticPlan returns the static plan bound to the execution.
	StaticPlan() *Plan
	// Node returns the static node with the given id, if any.
	Node(id string) (*ExecutionNode, bool)
	// State returns the lifecycle state of a node.
	State(nodeID string) NodeState
	// Observation returns the last observation of a node.
	Observation(nodeID string) (ObservationPayload, bool)
	// Attempts returns the execution count of a node.
	Attempts(nodeID string) int
	// Var returns a variable value.
	Var(key string) (string, bool)
	// StateMap returns a copy of the per-node states.
	StateMap() map[string]NodeState
	// ReadyNodes returns the ids of Pending nodes whose dependencies have all
	// reached SUCCESS.
	ReadyNodes() []string
	// FailedNodes returns the ids of nodes in the Failed state.
	FailedNodes() []string
	// IsComplete reports whether every node has reached SUCCESS.
	IsComplete() bool
}

// compile-time assertion that *ExecutionSnapshot satisfies SnapshotReader.
var _ SnapshotReader = (*ExecutionSnapshot)(nil)

// StaticPlan returns the static plan bound to the execution.
func (s *ExecutionSnapshot) StaticPlan() *Plan { return s.Plan }

// Node returns the static node with the given id, if any.
func (s *ExecutionSnapshot) Node(id string) (*ExecutionNode, bool) {
	if s.Plan == nil || s.Plan.Graph == nil {
		return nil, false
	}
	return s.Plan.Graph.Node(id)
}

// State returns the lifecycle state of a node.
func (s *ExecutionSnapshot) State(nodeID string) NodeState { return s.NodeStates[nodeID] }

// Observation returns the last observation of a node.
func (s *ExecutionSnapshot) Observation(nodeID string) (ObservationPayload, bool) {
	o, ok := s.LastObservation[nodeID]
	return o, ok
}

// Attempts returns the execution count of a node.
func (s *ExecutionSnapshot) Attempts(nodeID string) int { return s.AttemptCounts[nodeID] }

// Var returns a variable value.
func (s *ExecutionSnapshot) Var(key string) (string, bool) {
	v, ok := s.Variables[key]
	return v, ok
}

// StateMap returns a copy of the per-node states.
func (s *ExecutionSnapshot) StateMap() map[string]NodeState {
	out := make(map[string]NodeState, len(s.NodeStates))
	for k, v := range s.NodeStates {
		out[k] = v
	}
	return out
}

// ReadyNodes returns the ids of Pending nodes whose dependencies have all
// reached SUCCESS. It is the pure scheduling query of the Dynamic IR.
func (s *ExecutionSnapshot) ReadyNodes() []string {
	if s.Plan == nil || s.Plan.Graph == nil {
		return nil
	}
	var out []string
	for _, id := range s.Plan.Graph.IDs() {
		if s.NodeStates[id] != StatePending {
			continue
		}
		node, _ := s.Plan.Graph.Node(id)
		if node == nil {
			continue
		}
		all := true
		for _, dep := range node.DependsOn {
			if s.NodeStates[dep] != StateSuccess {
				all = false
				break
			}
		}
		if all {
			out = append(out, id)
		}
	}
	return out
}

// FailedNodes returns the ids of nodes in the Failed state, in graph order.
func (s *ExecutionSnapshot) FailedNodes() []string {
	if s.Plan == nil || s.Plan.Graph == nil {
		return nil
	}
	var out []string
	for _, id := range s.Plan.Graph.IDs() {
		if s.NodeStates[id] == StateFailed {
			out = append(out, id)
		}
	}
	return out
}

// IsComplete reports whether every node has reached SUCCESS.
func (s *ExecutionSnapshot) IsComplete() bool {
	if s.Plan == nil || s.Plan.Graph == nil {
		return false
	}
	for _, id := range s.Plan.Graph.IDs() {
		if s.NodeStates[id] != StateSuccess {
			return false
		}
	}
	return true
}

// Clone returns a deep copy of the snapshot. The Static Plan is shared (it is
// immutable); all mutable maps are copied.
func (s *ExecutionSnapshot) Clone() *ExecutionSnapshot {
	out := &ExecutionSnapshot{
		ID:              s.ID,
		Plan:            s.Plan,
		NodeStates:      make(map[string]NodeState, len(s.NodeStates)),
		LastObservation: make(map[string]ObservationPayload, len(s.LastObservation)),
		AttemptCounts:   make(map[string]int, len(s.AttemptCounts)),
		Variables:       make(Variables, len(s.Variables)),
		UpdatedAt:       s.UpdatedAt,
	}
	for k, v := range s.NodeStates {
		out.NodeStates[k] = v
	}
	for k, v := range s.LastObservation {
		v.FileMutations = append([]FileMutation(nil), v.FileMutations...)
		v.EnvSignals = append([]EnvSignal(nil), v.EnvSignals...)
		out.LastObservation[k] = v
	}
	for k, v := range s.AttemptCounts {
		out.AttemptCounts[k] = v
	}
	for k, v := range s.Variables {
		out.Variables[k] = v
	}
	return out
}
