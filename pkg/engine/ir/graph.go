// Package ir implements the Intermediate Representation of the adaptive
// control system. IR is split into two halves:
//
//   - Static IR — the immutable definition of a Plan and its ExecutionGraph.
//     A plan describes the work to be done and how the steps depend on each
//     other. It is constructed once and never mutates.
//
//   - Dynamic IR — the mutable runtime ExecutionSnapshot: per-node lifecycle
//     states, the last observation of each node, per-node attempt counts and
//     the variables flowing between steps.
//
// The two halves are strict. The Static IR never observes runtime state, and
// the Dynamic IR never decides anything. All control (retry, re-plan, skip,
// human approval) is owned by the Decision Engine, which reads ONLY the
// Dynamic IR.
package ir

import "fmt"

// NodeKind discriminates the type of work an execution node performs. It is a
// static property of the node: kinds that are deterministic environment probes
// are never retried, while generative, shell and verify steps may be.
type NodeKind string

const (
	// KindEnvProbe detects workspace facts (knowledge, capabilities). It is
	// deterministic and not retryable.
	KindEnvProbe NodeKind = "env_probe"
	// KindFileCheck asserts a file exists or is valid. Deterministic and not
	// retryable.
	KindFileCheck NodeKind = "file_check"
	// KindContext assembles execution context. Deterministic and not retryable.
	KindContext NodeKind = "context"
	// KindLLM performs a generative step. Retryable.
	KindLLM NodeKind = "llm"
	// KindShell runs a shell command. Retryable (transient environment errors).
	KindShell NodeKind = "shell"
	// KindVerify runs verification (build/test/lint). Retryable (flakes).
	KindVerify NodeKind = "verify"
)

// Retryable reports whether a failed node of this kind may benefit from being
// retried. Deterministic environment probes and context assembly re-observe
// the same stable failure; retrying them is waste.
func (k NodeKind) Retryable() bool {
	switch k {
	case KindEnvProbe, KindFileCheck, KindContext:
		return false
	default:
		return true
	}
}

// String returns the machine-readable kind label.
func (k NodeKind) String() string { return string(k) }

// ExecutionNode is a single immutable step of an ExecutionGraph. All fields
// are fixed at construction time; the graph must be treated as read-only after
// it is built.
type ExecutionNode struct {
	// ID uniquely identifies the node within the graph.
	ID string
	// Kind classifies the work the node performs.
	Kind NodeKind
	// Critical marks a failure of this node as non-skippable: a failed
	// critical node cannot be absorbed by the Decision Engine and forces a
	// RePlan or Abort once retries are exhausted. Non-critical failures are
	// the self-healing surface — they are skipped so the graph proceeds
	// without re-planning.
	Critical bool
	// RequiresApproval marks the node as a destructive / out-of-bounds action
	// that must be authorized by a human before it runs.
	RequiresApproval bool
	// Description is the human-readable purpose of the node.
	Description string
	// DependsOn lists the node IDs that must reach SUCCESS before this node
	// may run.
	DependsOn []string
}

// ExecutionGraph is the immutable definition of execution order. Nodes are
// added once at construction and the graph is read-only afterwards; every
// accessor returns copies so concurrent readers never observe mutation.
type ExecutionGraph struct {
	nodes map[string]*ExecutionNode
	order []string
}

// NewGraph returns an empty execution graph.
func NewGraph() *ExecutionGraph {
	return &ExecutionGraph{nodes: make(map[string]*ExecutionNode)}
}

// AddNode registers a single immutable node. The id must be unique and
// non-empty. Dependencies are validated lazily at TopoOrder time so forward
// references are permitted. AddNode is not safe for concurrent use; construct
// the graph before executing it.
func (g *ExecutionGraph) AddNode(id string, kind NodeKind, critical bool, description string, deps ...string) error {
	if id == "" {
		return fmt.Errorf("ir: node id must not be empty")
	}
	if _, ok := g.nodes[id]; ok {
		return fmt.Errorf("ir: duplicate node %q", id)
	}
	g.nodes[id] = &ExecutionNode{
		ID:          id,
		Kind:        kind,
		Critical:    critical,
		Description: description,
		DependsOn:   append([]string(nil), deps...),
	}
	g.order = append(g.order, id)
	return nil
}

// MarkApprovalRequired flags an existing node as a destructive / out-of-bounds
// action that must be human-authorized before it runs. It is a construction
// phase call: the graph is immutable once execution begins.
func (g *ExecutionGraph) MarkApprovalRequired(id string) error {
	n, ok := g.nodes[id]
	if !ok {
		return fmt.Errorf("ir: unknown node %q", id)
	}
	n.RequiresApproval = true
	return nil
}

// Len returns the number of nodes in the graph.
func (g *ExecutionGraph) Len() int {
	if g == nil {
		return 0
	}
	return len(g.nodes)
}

// Node returns a defensive copy of the node with the given id, if any.
func (g *ExecutionGraph) Node(id string) (*ExecutionNode, bool) {
	if g == nil {
		return nil, false
	}
	n, ok := g.nodes[id]
	if !ok {
		return nil, false
	}
	return cloneNode(n), true
}

// IDs returns the node ids in insertion order.
func (g *ExecutionGraph) IDs() []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.order...)
}

// Nodes returns defensive copies of the nodes in insertion order.
func (g *ExecutionGraph) Nodes() []*ExecutionNode {
	if g == nil {
		return nil
	}
	out := make([]*ExecutionNode, 0, len(g.order))
	for _, id := range g.order {
		out = append(out, cloneNode(g.nodes[id]))
	}
	return out
}

// Select returns defensive copies of the nodes whose ids are in selection,
// preserving graph order. Unknown ids produce an error.
func (g *ExecutionGraph) Select(ids []string) ([]*ExecutionNode, error) {
	if g == nil {
		return nil, fmt.Errorf("ir: nil execution graph")
	}
	sel := make(map[string]bool, len(ids))
	for _, id := range ids {
		if _, ok := g.nodes[id]; !ok {
			return nil, fmt.Errorf("ir: unknown node %q", id)
		}
		sel[id] = true
	}
	out := make([]*ExecutionNode, 0, len(ids))
	for _, id := range g.order {
		if sel[id] {
			out = append(out, cloneNode(g.nodes[id]))
		}
	}
	return out, nil
}

// cloneNode returns a defensive copy of a node so accessors can never leak a
// live reference into the immutable graph.
func cloneNode(n *ExecutionNode) *ExecutionNode {
	c := *n
	c.DependsOn = append([]string(nil), n.DependsOn...)
	return &c
}

// TopoOrder returns the nodes in dependency order using Kahn's algorithm with
// a deterministic insertion-order tie-break. It is a pure read: the graph is
// immutable once built. An unsatisfiable dependency set (cycle or unknown
// reference) is reported as an error.
func (g *ExecutionGraph) TopoOrder() ([]*ExecutionNode, error) {
	if g == nil {
		return nil, fmt.Errorf("ir: nil execution graph")
	}
	indeg := make(map[string]int, len(g.nodes))
	succ := make(map[string][]string, len(g.nodes))
	for _, n := range g.nodes {
		indeg[n.ID] = len(n.DependsOn)
		for _, dep := range n.DependsOn {
			if _, ok := g.nodes[dep]; !ok {
				return nil, fmt.Errorf("ir: node %q depends on unknown node %q", n.ID, dep)
			}
			succ[dep] = append(succ[dep], n.ID)
		}
	}
	// Roots are seeded in graph insertion order (not map order) so the
	// topological order is deterministic across runs: independent nodes keep
	// their declaration order.
	var roots []string
	for _, id := range g.order {
		if indeg[id] == 0 {
			roots = append(roots, id)
		}
	}
	var out []*ExecutionNode
	for len(roots) > 0 {
		id := roots[0]
		roots = roots[1:]
		out = append(out, cloneNode(g.nodes[id]))
		for _, s := range succ[id] {
			indeg[s]--
			if indeg[s] == 0 {
				roots = append(roots, s)
			}
		}
	}
	if len(out) != len(g.nodes) {
		return nil, fmt.Errorf("ir: execution graph contains a cycle (%d node(s) not schedulable)", len(g.nodes)-len(out))
	}
	return out, nil
}

// Plan is the immutable definition of intent and execution order. A Plan is
// constructed once and consumed read-only by the control loop.
type Plan struct {
	// ID uniquely identifies the plan.
	ID string
	// Description summarizes what the plan accomplishes.
	Description string
	// Graph is the static execution graph.
	Graph *ExecutionGraph
}
