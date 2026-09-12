// Package graph defines the Dynamic Execution Graph of the Izen Agent Runtime
// V3. An ExecutionGraph is a thread-safe DAG of OpNodes that run on the Phase A
// kernel.Engine, and it supports runtime mutation: InjectRepairOps inserts
// repair operations upon a node failure and rewires downstream dependencies
// without disturbing completed node states.
//
// The package is deliberately free of any AI, LLM or prompt dependencies.
package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	domaintask "github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/internal/op"
)

// TaskExecutor is the minimal execution contract ExecutionGraph drives.
// Both *runtime.RuntimeEngine (canonical, STEP 3A) and the legacy
// *kernel.Engine satisfy it, so callers program against the domain, not an
// adapter package.
type TaskExecutor interface {
	ExecuteTask(context.Context, domaintask.Executable) domaintask.TaskResult
}

// Errors returned by ExecutionGraph methods.
var (
	// ErrInvalidNode is returned when a nil or empty-ID node is added.
	ErrInvalidNode = errors.New("graph: invalid node")
	// ErrDuplicateNode is returned when a node ID is already present.
	ErrDuplicateNode = errors.New("graph: duplicate node id")
	// ErrUnknownNode is returned when a node ID is absent.
	ErrUnknownNode = errors.New("graph: unknown node id")
	// ErrUnknownPrecondition is returned when a node references a precondition
	// that is not part of the graph.
	ErrUnknownPrecondition = errors.New("graph: unknown precondition")
	// ErrEmptyRepairOps is returned when InjectRepairOps receives no repairs.
	ErrEmptyRepairOps = errors.New("graph: no repair operations provided")
	// ErrRepairDependsOnFailed is returned when a repair operation lists the
	// failed node as a precondition; the failed node can never complete.
	ErrRepairDependsOnFailed = errors.New("graph: repair operation depends on the failed node")
	// ErrNodeNotFailed is returned when InjectRepairOps targets a node that is
	// not in a failed or canceled state.
	ErrNodeNotFailed = errors.New("graph: node is not failed")
	// ErrGraphBlocked is returned by Execute when no node is pending yet the
	// graph is incomplete, meaning a terminal failure was never repaired.
	ErrGraphBlocked = errors.New("graph: graph blocked on an unrepaired failure")
)

// ExecutionFailure describes the node whose execution failed and its domain
// task result, so callers can InjectRepairOps and re-run the graph. The
// kernel Engine remains the execution bridge that produces the result.
type ExecutionFailure struct {
	// NodeID is the ID of the node that failed.
	NodeID string
	// Result is the terminal domain task result of the failed node.
	Result domaintask.TaskResult
}

// Error implements error.
func (f *ExecutionFailure) Error() string {
	return fmt.Sprintf("graph: node %q failed: %v", f.NodeID, f.Result.Error)
}

// Unwrap exposes the underlying node error.
func (f *ExecutionFailure) Unwrap() error { return f.Result.Error }

// ExecutionGraph is a thread-safe DAG of OpNodes executed on the Phase A
// kernel.Engine. It tracks each node's execution status and supports runtime
// mutation through InjectRepairOps.
type ExecutionGraph struct {
	mu     sync.RWMutex
	nodes  map[string]*OpNode
	states map[string]domaintask.ExecutionStatus
}

// NewExecutionGraph constructs an empty graph.
func NewExecutionGraph() *ExecutionGraph {
	return &ExecutionGraph{
		nodes:  make(map[string]*OpNode),
		states: make(map[string]domaintask.ExecutionStatus),
	}
}

// AddNode inserts a node into the graph with StatusPending. The node must have
// a non-empty ID, must not collide with an existing ID, and every precondition
// must already be part of the graph (dependencies are added first).
func (g *ExecutionGraph) AddNode(node *OpNode) error {
	if node == nil || node.ID() == "" {
		return ErrInvalidNode
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.nodes[node.ID()]; dup {
		return ErrDuplicateNode
	}
	for _, pre := range node.Requires() {
		if _, ok := g.nodes[pre]; !ok {
			return ErrUnknownPrecondition
		}
	}
	g.nodes[node.ID()] = node
	g.states[node.ID()] = domaintask.ExecStatusPending
	return nil
}

// GetNode returns the node registered under id.
func (g *ExecutionGraph) GetNode(id string) (*OpNode, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	node, ok := g.nodes[id]
	return node, ok
}

// GetPendingNodes returns the nodes that are ready to execute: not yet
// terminal and with every precondition completed. Results are sorted by ID for
// deterministic scheduling.
func (g *ExecutionGraph) GetPendingNodes() []*OpNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	pending := make([]*OpNode, 0, len(g.nodes))
	for _, node := range g.nodes {
		if g.states[node.ID()].IsTerminal() {
			continue
		}
		if g.preconditionsCompleted(node.Requires()) {
			pending = append(pending, node)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ID() < pending[j].ID() })
	return pending
}

// preconditionsCompleted reports whether every requirement is completed. A
// precondition in a terminal-but-not-completed state (failed or canceled)
// blocks the dependent until the graph is repaired.
func (g *ExecutionGraph) preconditionsCompleted(requires []string) bool {
	for _, id := range requires {
		if g.states[id] != domaintask.ExecStatusCompleted {
			return false
		}
	}
	return true
}

// MarkCompleted records a node as completed.
func (g *ExecutionGraph) MarkCompleted(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.nodes[id]; !ok {
		return ErrUnknownNode
	}
	g.states[id] = domaintask.ExecStatusCompleted
	return nil
}

// MarkFailed records a node as failed.
func (g *ExecutionGraph) MarkFailed(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.nodes[id]; !ok {
		return ErrUnknownNode
	}
	g.states[id] = domaintask.ExecStatusFailed
	return nil
}

// State returns the current execution status of a node.
func (g *ExecutionGraph) State(id string) (domaintask.ExecutionStatus, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	status, ok := g.states[id]
	return status, ok
}

// IsCompleted reports whether every node has reached a terminal status. A
// graph whose failure was repaired is completed once the repair nodes and
// their downstream dependents terminate, even when the originally failed node
// remains failed.
func (g *ExecutionGraph) IsCompleted() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, node := range g.nodes {
		if !g.states[node.ID()].IsTerminal() {
			return false
		}
	}
	return true
}

// InjectRepairOps inserts repair operations after a node failure and rewires
// the graph so every downstream node that depended on the failed node now
// waits for the repair nodes instead. Repair nodes become pending once their
// own preconditions complete, and the originally failed node's state is left
// untouched. InjectRepairOps is atomic: on error no node is added and no
// dependency is rewired.
func (g *ExecutionGraph) InjectRepairOps(failedNodeID string, repairOps []op.Operation) error {
	if len(repairOps) == 0 {
		return ErrEmptyRepairOps
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.nodes[failedNodeID]; !ok {
		return ErrUnknownNode
	}
	switch g.states[failedNodeID] {
	case domaintask.ExecStatusFailed, domaintask.ExecStatusCanceled:
	default:
		return ErrNodeNotFailed
	}

	repairIDs := make([]string, 0, len(repairOps))
	for _, repair := range repairOps {
		node, err := NewOpNode(repair)
		if err != nil {
			return err
		}
		if _, dup := g.nodes[node.ID()]; dup {
			return ErrDuplicateNode
		}
		for _, pre := range node.Requires() {
			if pre == failedNodeID {
				return ErrRepairDependsOnFailed
			}
			if _, ok := g.nodes[pre]; !ok {
				return ErrUnknownPrecondition
			}
		}
		g.nodes[node.ID()] = node
		g.states[node.ID()] = domaintask.ExecStatusPending
		repairIDs = append(repairIDs, node.ID())
	}

	for _, node := range g.nodes {
		requires := node.Requires()
		if !containsID(requires, failedNodeID) {
			continue
		}
		kept := make([]string, 0, len(requires)+len(repairIDs))
		seen := make(map[string]struct{}, len(requires)+len(repairIDs))
		for _, pre := range requires {
			if pre == failedNodeID {
				continue
			}
			if _, dup := seen[pre]; dup {
				continue
			}
			seen[pre] = struct{}{}
			kept = append(kept, pre)
		}
		for _, id := range repairIDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			kept = append(kept, id)
		}
		node.op.Preconditions = kept
	}
	return nil
}

// containsID reports whether ids contains id.
func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// Execute runs the graph to a quiescent state on a TaskExecutor (canonical:
// *runtime.RuntimeEngine). Nodes are executed in dependency order via
// GetPendingNodes; a completed node is marked completed, while a failed or
// canceled node stops the run and is returned as an *ExecutionFailure so the
// caller can InjectRepairOps and call Execute again. Execute returns nil once
// every node is terminal. The returned map holds the terminal result of every
// executed node keyed by node ID.
func (g *ExecutionGraph) Execute(ctx context.Context, engine TaskExecutor) (map[string]domaintask.TaskResult, error) {
	if engine == nil {
		return nil, errors.New("graph: nil task executor")
	}
	results := make(map[string]domaintask.TaskResult)
	for {
		pending := g.GetPendingNodes()
		if len(pending) == 0 {
			if g.IsCompleted() {
				return results, nil
			}
			return results, ErrGraphBlocked
		}
		for _, node := range pending {
			if err := ctx.Err(); err != nil {
				return results, err
			}
			result := engine.ExecuteTask(ctx, node)
			results[node.ID()] = result
			switch result.Status {
			case domaintask.ExecStatusCompleted:
				if err := g.MarkCompleted(node.ID()); err != nil {
					return results, err
				}
			case domaintask.ExecStatusFailed, domaintask.ExecStatusCanceled:
				if err := g.MarkFailed(node.ID()); err != nil {
					return results, err
				}
				return results, &ExecutionFailure{NodeID: node.ID(), Result: result}
			}
		}
	}
}
