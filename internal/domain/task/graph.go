package task

import (
	"fmt"
	"sort"
	"sync"
)

// Node is a single unit of work in the execution graph.
type Node struct {
	// ID uniquely identifies the node within the graph.
	ID string
	// Description is a human-readable summary of the work.
	Description string
	// Dependencies lists the ids of nodes that must complete first.
	Dependencies []string
}

// Graph is a thread-safe directed acyclic graph of work nodes keyed by id.
// Edges point from a node to the nodes it depends on.
type Graph struct {
	mu    sync.RWMutex
	tasks map[string]Node
	order []string
}

// New builds an empty task graph.
func New() *Graph {
	return &Graph{tasks: make(map[string]Node)}
}

// Add inserts a node, rejecting empty ids, duplicate ids, and self
// dependencies. Forward references to not-yet-added dependencies are allowed
// and are validated by Validate.
func (g *Graph) Add(t Node) error {
	if t.ID == "" {
		return ErrEmptyTaskID
	}
	if g.Has(t.ID) {
		return fmt.Errorf("%w: %s", ErrDuplicateTask, t.ID)
	}
	for _, dep := range t.Dependencies {
		if dep == t.ID {
			return fmt.Errorf("%w: %s", ErrSelfDependency, t.ID)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tasks[t.ID] = t
	g.order = append(g.order, t.ID)
	return nil
}

// Get returns the node with the given id.
func (g *Graph) Get(id string) (Node, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	t, ok := g.tasks[id]
	return t, ok
}

// Has reports whether a node with the given id exists.
func (g *Graph) Has(id string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.tasks[id]
	return ok
}

// Len returns the number of nodes in the graph.
func (g *Graph) Len() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.tasks)
}

// Tasks returns a snapshot of all nodes in insertion order.
func (g *Graph) Tasks() []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Node, 0, len(g.order))
	for _, id := range g.order {
		out = append(out, g.tasks[id])
	}
	return out
}

// Dependencies returns a copy of the dependency ids for a task.
func (g *Graph) Dependencies(id string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	t, ok := g.tasks[id]
	if !ok {
		return nil
	}
	out := make([]string, len(t.Dependencies))
	copy(out, t.Dependencies)
	return out
}

// Dependents returns the ids of tasks that directly depend on id, sorted for
// deterministic output.
func (g *Graph) Dependents(id string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []string
	for _, t := range g.tasks {
		for _, dep := range t.Dependencies {
			if dep == id {
				out = append(out, t.ID)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// Ready returns the ids of tasks whose dependencies are all present in
// completed, in insertion order. Tasks already completed are excluded.
func (g *Graph) Ready(completed map[string]bool) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []string
	for _, id := range g.order {
		if completed[id] {
			continue
		}
		if g.depsDoneLocked(id, completed) {
			out = append(out, id)
		}
	}
	return out
}

// depsDoneLocked reports whether every dependency of id is marked done.
func (g *Graph) depsDoneLocked(id string, done map[string]bool) bool {
	for _, dep := range g.tasks[id].Dependencies {
		if !done[dep] {
			return false
		}
	}
	return true
}

// Validate checks graph integrity: every dependency must reference an existing
// task and the graph must be acyclic.
func (g *Graph) Validate() error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.tasks) == 0 {
		return nil
	}
	for _, id := range g.order {
		for _, dep := range g.tasks[id].Dependencies {
			if _, ok := g.tasks[dep]; !ok {
				return fmt.Errorf("%w: %s (depends on %s)", ErrMissingDependency, id, dep)
			}
		}
	}
	if _, err := topoLocked(g.tasks, g.order); err != nil {
		return err
	}
	return nil
}

// TopologicalOrder returns task ids ordered so that every task appears after
// all of its dependencies. It returns an error when the graph is cyclic.
func (g *Graph) TopologicalOrder() ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return topoLocked(g.tasks, g.order)
}

// topoLocked runs Kahn's algorithm over the graph under an already-held read
// lock. It returns ErrCycleDetected when the graph is cyclic. Iteration follows
// insertion order so the result is deterministic.
func topoLocked(tasks map[string]Node, order []string) ([]string, error) {
	indeg := make(map[string]int, len(tasks))
	for _, id := range order {
		indeg[id] = len(tasks[id].Dependencies)
	}
	queue := make([]string, 0, len(tasks))
	for _, id := range order {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	var out []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		out = append(out, id)
		for _, dependent := range dependentsOf(tasks, order, id) {
			indeg[dependent]--
			if indeg[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if len(out) != len(tasks) {
		return nil, ErrCycleDetected
	}
	return out, nil
}

// dependentsOf returns the ids of tasks that directly depend on id, iterated in
// insertion order.
func dependentsOf(tasks map[string]Node, order []string, id string) []string {
	var out []string
	for _, tID := range order {
		for _, dep := range tasks[tID].Dependencies {
			if dep == id {
				out = append(out, tID)
				break
			}
		}
	}
	return out
}
