// Package dag implements a small, dependency-free Directed Acyclic Graph
// engine used to resolve inter-file dependencies and order code generation and
// patching steps without cycles. It is the execution planner behind the
// pipeline's artifact application: file creation steps are topologically
// ordered so every dependency is written before its dependents, and a circular
// dependency is rejected with the explicit cycle path before any file is
// touched.
//
// The package is deliberately free of any AI, LLM or prompt dependencies.
package dag

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Node is a single unit of work in the graph. Payload carries caller data
// (e.g. the ir.Artifact being written) and is opaque to the engine.
type Node struct {
	// ID uniquely identifies the node within the graph.
	ID string
	// Payload carries caller-defined data attached to the node.
	Payload any
}

// Edge is a directed dependency edge: From must be scheduled before To.
type Edge struct {
	// From is the ID of the prerequisite node.
	From string
	// To is the ID of the dependent node.
	To string
}

// Errors returned by Graph methods.
var (
	// ErrInvalidNode is returned when a nil node or an empty node ID is added.
	ErrInvalidNode = errors.New("dag: invalid node")
	// ErrDuplicateNode is returned when a node ID is already present.
	ErrDuplicateNode = errors.New("dag: duplicate node id")
	// ErrUnknownNode is returned when an edge references an absent node ID.
	ErrUnknownNode = errors.New("dag: unknown node id")
	// ErrSelfDependency is returned when a node is declared as its own
	// dependency.
	ErrSelfDependency = errors.New("dag: node must not depend on itself")
	// ErrCyclicDependency is the sentinel unwrapped by *CyclicDependencyError.
	ErrCyclicDependency = errors.New("dag: cyclic dependency detected")
)

// CyclicDependencyError reports a dependency cycle in the graph. Cycle holds
// the explicit path of node IDs, e.g. [a b a], where the final element repeats
// the first to close the loop.
type CyclicDependencyError struct {
	// Cycle is the ordered path of node IDs forming the cycle, with the
	// first ID repeated as the last element.
	Cycle []string
}

// Error implements error. A self-loop is rendered as a single repeated node.
func (e *CyclicDependencyError) Error() string {
	if len(e.Cycle) == 0 {
		return ErrCyclicDependency.Error()
	}
	return fmt.Sprintf("dag: cyclic dependency detected: %s", strings.Join(e.Cycle, " -> "))
}

// Unwrap exposes the ErrCyclicDependency sentinel so callers can match with
// errors.Is while still accessing the explicit cycle path through the typed
// error.
func (e *CyclicDependencyError) Unwrap() error { return ErrCyclicDependency }

// Graph is a thread-safe directed graph keyed by node ID. It supports dynamic
// construction (AddNode / AddEdge), deterministic topological ordering and
// cycle detection. The graph holds no execution state: callers decide how to
// run the ordered steps.
type Graph struct {
	mu    sync.RWMutex
	nodes map[string]*Node
	out   map[string]map[string]struct{}
	in    map[string]map[string]struct{}
}

// NewGraph constructs an empty graph.
func NewGraph() *Graph {
	return &Graph{
		nodes: make(map[string]*Node),
		out:   make(map[string]map[string]struct{}),
		in:    make(map[string]map[string]struct{}),
	}
}

// AddNode inserts a node with the given ID and payload. The node ID must be
// non-empty and unique.
func (g *Graph) AddNode(n *Node) error {
	if n == nil || n.ID == "" {
		return ErrInvalidNode
	}
	return g.AddNodeWithPayload(n.ID, n.Payload)
}

// AddNodeWithPayload inserts a node with the given ID and payload.
func (g *Graph) AddNodeWithPayload(id string, payload any) error {
	if id == "" {
		return ErrInvalidNode
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.nodes[id]; dup {
		return fmt.Errorf("%w: %q", ErrDuplicateNode, id)
	}
	g.nodes[id] = &Node{ID: id, Payload: payload}
	return nil
}

// AddEdge inserts a directed edge From -> To, meaning From must be scheduled
// before To. Both endpoints must already be part of the graph and a node must
// not depend on itself. Adding an already-present edge is a no-op.
func (g *Graph) AddEdge(from, to string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.nodes[from]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownNode, from)
	}
	if _, ok := g.nodes[to]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownNode, to)
	}
	if from == to {
		return fmt.Errorf("%w: %q", ErrSelfDependency, from)
	}
	if _, dup := g.out[from][to]; dup {
		return nil
	}
	if g.out[from] == nil {
		g.out[from] = make(map[string]struct{})
	}
	if g.in[to] == nil {
		g.in[to] = make(map[string]struct{})
	}
	g.out[from][to] = struct{}{}
	g.in[to][from] = struct{}{}
	return nil
}

// Node returns the node registered under id.
func (g *Graph) Node(id string) (*Node, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	node, ok := g.nodes[id]
	return node, ok
}

// Has reports whether a node with the given id is present.
func (g *Graph) Has(id string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.nodes[id]
	return ok
}

// NodeCount returns the number of nodes in the graph.
func (g *Graph) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// EdgeCount returns the number of directed edges in the graph.
func (g *Graph) EdgeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	count := 0
	for _, succ := range g.out {
		count += len(succ)
	}
	return count
}

// Nodes returns every node in the graph, sorted by ID for determinism.
func (g *Graph) Nodes() []*Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Edges returns every directed edge in the graph, sorted by From then To for
// determinism.
func (g *Graph) Edges() []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Edge, 0, g.edgeCountLocked())
	for from, succ := range g.out {
		for to := range succ {
			out = append(out, Edge{From: from, To: to})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// edgeCountLocked returns the number of edges; caller must hold the read lock.
func (g *Graph) edgeCountLocked() int {
	count := 0
	for _, succ := range g.out {
		count += len(succ)
	}
	return count
}

// TopoSort returns the node IDs in dependency order: every node appears after
// all of its dependencies. The order is deterministic — nodes with no remaining
// dependency are emitted in ascending ID order. When the graph contains a
// cycle, TopoSort returns a *CyclicDependencyError carrying the explicit cycle
// path.
func (g *Graph) TopoSort() ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.nodes) == 0 {
		return []string{}, nil
	}

	// Kahn's algorithm. The frontier of zero-in-degree nodes is kept sorted
	// so the emitted order is deterministic.
	indeg := make(map[string]int, len(g.nodes))
	for id := range g.nodes {
		indeg[id] = len(g.in[id])
	}
	frontier := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		if indeg[id] == 0 {
			frontier = append(frontier, id)
		}
	}
	sort.Strings(frontier)

	order := make([]string, 0, len(g.nodes))
	for len(frontier) > 0 {
		id := frontier[0]
		frontier = frontier[1:]
		order = append(order, id)
		succ := sortedKeys(g.out[id])
		for _, s := range succ {
			indeg[s]--
			if indeg[s] == 0 {
				frontier = append(frontier, s)
			}
		}
		sort.Strings(frontier)
	}

	if len(order) != len(g.nodes) {
		return nil, &CyclicDependencyError{Cycle: g.findCycleLocked()}
	}
	return order, nil
}

// TopoSortNodes returns the graph nodes in dependency order, mirroring
// TopoSort with the node payloads attached. A cyclic graph yields the same
// *CyclicDependencyError.
func (g *Graph) TopoSortNodes() ([]*Node, error) {
	ids, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*Node, 0, len(ids))
	for _, id := range ids {
		if n, ok := g.nodes[id]; ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// HasCycle reports whether the graph contains any directed cycle.
func (g *Graph) HasCycle() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.findCycleLocked()) > 0
}

// FindCycle returns an explicit cycle path [a b a] when the graph is cyclic,
// or nil when it is acyclic. The path always closes the loop by repeating its
// first element last.
func (g *Graph) FindCycle() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.findCycleLocked()
}

// findCycleLocked runs an iterative DFS with a recursion path stack and
// returns the first cycle found as an ordered path. The caller must hold the
// read lock.
func (g *Graph) findCycleLocked() []string {
	const (
		white uint8 = iota
		gray
		black
	)
	color := make(map[string]uint8, len(g.nodes))
	for _, id := range sortedNodeIDs(g.nodes) {
		if color[id] != white {
			continue
		}
		type frame struct {
			node string
			succ []string
			idx  int
		}
		path := []string{id}
		frames := []frame{{node: id, succ: sortedKeys(g.out[id])}}
		color[id] = gray
		for len(frames) > 0 {
			f := &frames[len(frames)-1]
			if f.idx < len(f.succ) {
				nxt := f.succ[f.idx]
				f.idx++
				switch color[nxt] {
				case white:
					color[nxt] = gray
					path = append(path, nxt)
					frames = append(frames, frame{node: nxt, succ: sortedKeys(g.out[nxt])})
				case gray:
					// Back edge to a node on the current path: cycle found.
					start := indexOf(path, nxt)
					cycle := append([]string(nil), path[start:]...)
					return append(cycle, nxt)
				}
				continue
			}
			color[f.node] = black
			path = path[:len(path)-1]
			frames = frames[:len(frames)-1]
		}
	}
	return nil
}

// sortedNodeIDs returns the IDs of the given node map in ascending order.
func sortedNodeIDs(m map[string]*Node) []string {
	if len(m) == 0 {
		return nil
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// indexOf returns the first index of target in values, or -1 when absent.
func indexOf(values []string, target string) int {
	for i, v := range values {
		if v == target {
			return i
		}
	}
	return -1
}
