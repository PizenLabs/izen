package execution

import (
	"fmt"
	"strings"
)

// ── Execution Graph (Phase 9B) ─────────────────────────────────────────────
//
// ExecutionGraph is the deterministic multi-file mutation model. The Phase 9B
// invariant:
//
//	ONE USER INTENT == ONE EXECUTION GRAPH == ONE MUTATION SET ==
//	ONE TERMINAL OUTCOME
//
// The graph is a pure, deterministic structure: given the same resolved
// targets and dependency edges it always yields the same node ordering and the
// same validation result. It performs no I/O and no provider work itself — the
// runtime drives node states through the graph lifecycle while the owning
// MutationSet remains the single transaction boundary.

// NodeState is the lifecycle state of one execution node. It reuses the
// semantic vocabulary of the existing mutation lifecycle and never contradicts
// the owning MutationSet's state.
type NodeState string

// Node lifecycle states.
const (
	NodePending    NodeState = "pending"    // resolved, not yet processed
	NodeReading    NodeState = "reading"    // file read in progress
	NodeGenerating NodeState = "generating" // provider artifact generation
	NodeArtifact   NodeState = "artifact"   // a concrete artifact exists
	NodeReady      NodeState = "ready"      // artifact extracted + validated
	NodeApplying   NodeState = "applying"   // patch application in progress
	NodeVerified   NodeState = "verified"   // applied + verification passed
	NodeSkipped    NodeState = "skipped"    // no change required
	NodeFailed     NodeState = "failed"     // generation/apply/verify failed
	NodeCancelled  NodeState = "cancelled"  // cancelled by the runtime
)

// Terminal reports whether the node reached a terminal state.
func (s NodeState) Terminal() bool {
	switch s {
	case NodeVerified, NodeSkipped, NodeFailed, NodeCancelled:
		return true
	}
	return false
}

// Succeeded reports whether the node provably mutated (or cleanly no-oped)
// the filesystem.
func (s NodeState) Succeeded() bool { return s == NodeVerified || s == NodeSkipped }

// GraphState is the lifecycle state of the whole execution graph.
type GraphState string

// Graph lifecycle states.
const (
	GraphPending    GraphState = "pending"     // constructed, not started
	GraphPreparing  GraphState = "preparing"   // Phase A: resolve/read/generate
	GraphReady      GraphState = "ready"       // Phase A complete, apply authorized
	GraphApplying   GraphState = "applying"    // Phase B: mutations executing
	GraphVerifying  GraphState = "verifying"   // post-apply verification
	GraphCommitted  GraphState = "committed"   // MutationSet committed (terminal)
	GraphRolledBack GraphState = "rolled_back" // MutationSet rolled back (terminal)
	GraphFailed     GraphState = "failed"      // deterministic failure (terminal)
	GraphCancelled  GraphState = "cancelled"   // cancelled by the runtime (terminal)
)

// Terminal reports whether the graph reached a terminal state.
func (s GraphState) Terminal() bool {
	switch s {
	case GraphCommitted, GraphRolledBack, GraphFailed, GraphCancelled:
		return true
	}
	return false
}

// Edge is a dependency edge: To depends on From (From must complete before To
// runs). Edges exist only where evidence establishes the dependency.
type Edge struct {
	From   string // node ID (the prerequisite)
	To     string // node ID (the dependent)
	Reason string // why this edge exists
}

// ExecutionNode is one deterministic mutation target inside the graph.
type ExecutionNode struct {
	// ID is the stable node identity within the graph ("n1", "n2", ...).
	ID string
	// Target is the exact, preserved target identity (workspace-relative path).
	Target string
	// Role records whether the target was explicit (@file) or inferred.
	Role TargetRole
	// Dependencies are the node IDs this node depends on. The graph rejects
	// cycles and missing dependencies before execution.
	Dependencies []string
	// OriginalContent is the on-disk content captured during Phase A.
	OriginalContent string
	// Patch is the bounded mutation artifact (nil until Phase A succeeds).
	Patch *Patch
	// Evidence is the per-node semantic mutation evidence.
	Evidence MutationEvidence
	// State is the node lifecycle state.
	State NodeState
}

// ExecutionGraph is one user mutation's deterministic execution model.
type ExecutionGraph struct {
	// ID is the owning operation ID.
	ID string
	// Nodes are in stable, first-appearance order. Ordering is never derived
	// from map iteration.
	Nodes []*ExecutionNode
	// Edges are the explicit dependency edges.
	Edges []Edge
	// State is the graph lifecycle state.
	State GraphState
	// MutationSet is THE single transaction boundary for this graph. Every
	// node records into it; the graph commits or rolls it back as a whole.
	MutationSet *MutationSet
}

// NewExecutionGraph constructs a graph from a deterministic target list (in
// resolution order) and the single owning MutationSet. Duplicate targets must
// already have been collapsed by the target resolver; Validate re-checks.
func NewExecutionGraph(id string, targets []Target, ms *MutationSet) *ExecutionGraph {
	g := &ExecutionGraph{
		ID:          id,
		State:       GraphPending,
		MutationSet: ms,
	}
	for i, t := range targets {
		g.Nodes = append(g.Nodes, &ExecutionNode{
			ID:     fmt.Sprintf("n%d", i+1),
			Target: t.Path,
			Role:   t.Role,
			State:  NodePending,
		})
	}
	return g
}

// Node returns the node with the given ID, or nil.
func (g *ExecutionGraph) Node(id string) *ExecutionNode {
	if g == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// NodeForTarget returns the node mutating path, or nil.
func (g *ExecutionGraph) NodeForTarget(path string) *ExecutionNode {
	if g == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if n.Target == path {
			return n
		}
	}
	return nil
}

// Targets returns the node targets in stable order.
func (g *ExecutionGraph) Targets() []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		out = append(out, n.Target)
	}
	return out
}

// Transition advances the graph lifecycle state. A terminal graph is never
// re-entered.
func (g *ExecutionGraph) Transition(s GraphState) {
	if g == nil || g.State.Terminal() {
		return
	}
	g.State = s
}

// Terminal reports whether the graph reached a terminal state.
func (g *ExecutionGraph) Terminal() bool { return g != nil && g.State.Terminal() }

// AddDependency adds an explicit edge To → From (To depends on From). It
// rejects self-dependencies and cycles deterministically.
func (g *ExecutionGraph) AddDependency(from, to, reason string) error {
	if g == nil {
		return fmt.Errorf("graph: nil graph")
	}
	if g.Node(from) == nil {
		return fmt.Errorf("graph: dependency from unknown node %q", from)
	}
	if g.Node(to) == nil {
		return fmt.Errorf("graph: dependency to unknown node %q", to)
	}
	if from == to {
		return fmt.Errorf("graph: self dependency on %q", from)
	}
	// Reject duplicate edges.
	for _, e := range g.Edges {
		if e.From == from && e.To == to {
			return fmt.Errorf("graph: duplicate dependency %s -> %s", from, to)
		}
	}
	// Topological cycle check: adding To -> From must not create a cycle.
	g.Edges = append(g.Edges, Edge{From: from, To: to, Reason: reason})
	for _, n := range g.Nodes {
		n.Dependencies = nil
	}
	for _, e := range g.Edges {
		if n := g.Node(e.To); n != nil {
			n.Dependencies = append(n.Dependencies, e.From)
		}
	}
	if _, ok := g.cycle(); ok {
		g.Edges = g.Edges[:len(g.Edges)-1]
		for _, n := range g.Nodes {
			n.Dependencies = nil
		}
		for _, e := range g.Edges {
			if n := g.Node(e.To); n != nil {
				n.Dependencies = append(n.Dependencies, e.From)
			}
		}
		return fmt.Errorf("graph: dependency %s -> %s creates a cycle", from, to)
	}
	return nil
}

// Validate runs the deterministic structural validation the graph must pass
// before any mutation. It is idempotent and side-effect free.
func (g *ExecutionGraph) Validate() error {
	if g == nil {
		return fmt.Errorf("graph: nil graph")
	}
	if len(g.Nodes) == 0 {
		return fmt.Errorf("graph: no execution nodes")
	}
	// 1. Same target in multiple nodes is a conflict — never a silent
	//    overwrite. The target resolver collapses duplicates, so reaching this
	//    error means a node was injected without going through resolution.
	seen := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		if n == nil || n.Target == "" {
			return fmt.Errorf("graph: node %q has no target", nodeID(n))
		}
		if prev, ok := seen[n.Target]; ok {
			return fmt.Errorf("graph: duplicate target %q in nodes %s and %s", n.Target, prev, n.ID)
		}
		seen[n.Target] = n.ID
	}
	// 2. Missing dependency references.
	for _, n := range g.Nodes {
		for _, dep := range n.Dependencies {
			if g.Node(dep) == nil {
				return fmt.Errorf("graph: node %s depends on unknown node %q", n.ID, dep)
			}
		}
	}
	// 3. Dependency cycles.
	if cyc, ok := g.cycle(); ok {
		return fmt.Errorf("graph: dependency cycle detected: %s", strings.Join(cyc, " -> "))
	}
	return nil
}

// HasAllArtifacts reports whether every node produced a concrete mutation
// artifact. It is the Phase A → Phase B gate.
func (g *ExecutionGraph) HasAllArtifacts() bool {
	if g == nil || len(g.Nodes) == 0 {
		return false
	}
	for _, n := range g.Nodes {
		if n == nil || n.Patch == nil {
			return false
		}
	}
	return true
}

// AllNodesTerminal reports whether every node reached a terminal state. The
// MutationSet may only be committed when this holds.
func (g *ExecutionGraph) AllNodesTerminal() bool {
	if g == nil {
		return false
	}
	for _, n := range g.Nodes {
		if n == nil || !n.State.Terminal() {
			return false
		}
	}
	return true
}

// AnyNodeFailed reports whether any node failed. Used to derive the aggregate
// terminal outcome.
func (g *ExecutionGraph) AnyNodeFailed() bool {
	if g == nil {
		return false
	}
	for _, n := range g.Nodes {
		if n != nil && n.State == NodeFailed {
			return true
		}
	}
	return false
}

// FirstFailedNode returns the ID of the first failed node, or "".
func (g *ExecutionGraph) FirstFailedNode() string {
	if g == nil {
		return ""
	}
	for _, n := range g.Nodes {
		if n != nil && n.State == NodeFailed {
			return n.Target
		}
	}
	return ""
}

// cycle performs a DFS topological scan and returns the first detected cycle
// as a node-ID path, or ("", false) when the graph is acyclic. A node is the
// dependent of an edge when it lists the edge.From in Dependencies.
func (g *ExecutionGraph) cycle() ([]string, bool) {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(g.Nodes))
	for _, n := range g.Nodes {
		color[n.ID] = white
	}
	var stack []string
	var visit func(id string) ([]string, bool)
	visit = func(id string) ([]string, bool) {
		color[id] = grey
		stack = append(stack, id)
		for _, dep := range g.Node(id).Dependencies {
			switch color[dep] {
			case grey:
				// Found a cycle: return the segment back to dep.
				idx := -1
				for i, x := range stack {
					if x == dep {
						idx = i
						break
					}
				}
				if idx >= 0 {
					cyc := append([]string(nil), stack[idx:]...)
					return cyc, true
				}
			case white:
				if c, ok := visit(dep); ok {
					return c, true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil, false
	}
	for _, n := range g.Nodes {
		if color[n.ID] == white {
			if c, ok := visit(n.ID); ok {
				return c, true
			}
		}
	}
	return nil, false
}

func nodeID(n *ExecutionNode) string {
	if n == nil {
		return "<nil>"
	}
	return n.ID
}
