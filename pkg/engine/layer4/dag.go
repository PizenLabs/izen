package layer4

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Sentinel errors returned by the validation DAG engine.
var (
	// ErrCycleDetected is returned when the DAG contains a dependency cycle.
	ErrCycleDetected = errors.New("layer4: validation DAG contains a cycle")
	// ErrDuplicateNode is returned when a node id is added twice.
	ErrDuplicateNode = errors.New("layer4: duplicate validation node")
	// ErrUnknownDependency is returned when a node depends on an unknown id.
	ErrUnknownDependency = errors.New("layer4: unknown validation dependency")
	// ErrEmptyDAG is returned when Execute is called on a DAG with no nodes.
	ErrEmptyDAG = errors.New("layer4: empty validation DAG")
	// ErrShortCircuited is returned when a run stops early because a stage
	// failed and its dependents were cancelled.
	ErrShortCircuited = errors.New("layer4: validation short-circuited by stage failure")
)

// defaultConcurrency bounds the DAG worker pool when no explicit limit is set.
const defaultConcurrency = 8

// Status is the lifecycle status of a DAG node during a run.
type Status string

const (
	// StatusPending is a node that has not started.
	StatusPending Status = "pending"
	// StatusRunning is a node currently executing.
	StatusRunning Status = "running"
	// StatusPassed is a node whose validation succeeded.
	StatusPassed Status = "passed"
	// StatusFailed is a node whose validation failed.
	StatusFailed Status = "failed"
	// StatusSkipped is a node cancelled by early short-circuiting: a
	// dependency failed, so the node never ran.
	StatusSkipped Status = "skipped"
)

// String returns the machine-readable status label.
func (s Status) String() string { return string(s) }

// Node is a single validation step in the DAG. Nodes are immutable after they
// are added to a DAG.
type Node struct {
	// ID uniquely identifies the node within the DAG.
	ID string
	// Stage is the validation stage the node performs.
	Stage Stage
	// Validator performs the actual check.
	Validator Validator
	// DependsOn lists the node IDs that must pass before this node runs.
	DependsOn []string
}

// DAG is a Directed Acyclic Graph of validation steps. It is constructed once
// with AddNode and then executed concurrently with early short-circuiting. A
// node only starts once every dependency has passed; a failing node prevents
// every transitive descendant from starting (StatusSkipped).
type DAG struct {
	nodes map[string]*Node
	order []string
}

// New returns an empty validation DAG.
func New() *DAG {
	return &DAG{nodes: make(map[string]*Node)}
}

// AddNode registers a validation node. The id must be unique and non-empty,
// and the node must carry a validator. Dependencies are validated at topo
// sort time so forward references are permitted. AddNode is not safe for
// concurrent use; construct the DAG before executing it.
func (d *DAG) AddNode(id string, stage Stage, v Validator, deps ...string) error {
	if id == "" {
		return ErrDuplicateNode
	}
	if _, ok := d.nodes[id]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicateNode, id)
	}
	if v == nil {
		return fmt.Errorf("%w: node %q", ErrNoValidator, id)
	}
	d.nodes[id] = &Node{
		ID:        id,
		Stage:     stage,
		Validator: v,
		DependsOn: append([]string(nil), deps...),
	}
	d.order = append(d.order, id)
	return nil
}

// Len returns the number of nodes in the DAG.
func (d *DAG) Len() int { return len(d.nodes) }

// Node returns the node with the given id, if any.
func (d *DAG) Node(id string) (*Node, bool) {
	n, ok := d.nodes[id]
	return n, ok
}

// IDs returns the node ids in insertion order.
func (d *DAG) IDs() []string {
	return append([]string(nil), d.order...)
}

// TopoSort returns the nodes in dependency order, cheapest prerequisites
// first, using Kahn's algorithm. A deterministic tie-break keeps the order
// stable across runs. It returns ErrCycleDetected or ErrUnknownDependency on
// an invalid graph.
func (d *DAG) TopoSort() ([]*Node, error) {
	if err := d.validateDeps(); err != nil {
		return nil, err
	}
	indeg := make(map[string]int, len(d.nodes))
	succ := make(map[string][]string, len(d.nodes))
	for _, n := range d.nodes {
		indeg[n.ID] = len(n.DependsOn)
		for _, dep := range n.DependsOn {
			succ[dep] = append(succ[dep], n.ID)
		}
	}
	var roots []string
	for id, deg := range indeg {
		if deg == 0 {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)

	var out []*Node
	for len(roots) > 0 {
		id := roots[0]
		roots = roots[1:]
		out = append(out, d.nodes[id])
		for _, s := range succ[id] {
			indeg[s]--
			if indeg[s] == 0 {
				roots = append(roots, s)
				sort.Strings(roots)
			}
		}
	}
	if len(out) != len(d.nodes) {
		return nil, fmt.Errorf("%w: %d node(s) not schedulable", ErrCycleDetected, len(d.nodes)-len(out))
	}
	return out, nil
}

// validateDeps verifies every dependency references a known node.
func (d *DAG) validateDeps() error {
	for _, n := range d.nodes {
		for _, dep := range n.DependsOn {
			if _, ok := d.nodes[dep]; !ok {
				return fmt.Errorf("%w: node %q depends on %q", ErrUnknownDependency, n.ID, dep)
			}
		}
	}
	return nil
}

// Execute runs the validation DAG against the proposed patches with the
// default worker concurrency. It returns the full run result; a failed stage
// is reported both through Result and as a wrapped sentinel error.
func (d *DAG) Execute(ctx context.Context, patches []Patch) (*Result, error) {
	return d.ExecuteWithConcurrency(ctx, patches, defaultConcurrency)
}

// ExecuteWithConcurrency runs the DAG with a bounded worker pool. Nodes whose
// dependencies have all passed execute concurrently; a failing node cancels
// the run and marks every transitive descendant as skipped. limit <= 0 uses
// the default concurrency.
func (d *DAG) ExecuteWithConcurrency(ctx context.Context, patches []Patch, limit int) (*Result, error) {
	if len(d.nodes) == 0 {
		return nil, ErrEmptyDAG
	}
	order, err := d.TopoSort()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultConcurrency
	}
	if limit > len(order) {
		limit = len(order)
	}

	started := time.Now()
	run := &dagRun{
		dag:       d,
		patches:   patches,
		remaining: make(map[string]int, len(order)),
		succ:      make(map[string][]string, len(order)),
		results:   make(map[string]NodeResult, len(order)),
	}
	for _, n := range order {
		run.remaining[n.ID] = len(n.DependsOn)
		run.results[n.ID] = NodeResult{ID: n.ID, Stage: n.Stage, Status: StatusPending}
		for _, dep := range n.DependsOn {
			run.succ[dep] = append(run.succ[dep], n.ID)
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	ready := make(chan string, len(order))
	for id, deg := range run.remaining {
		if deg == 0 {
			ready <- id
		}
	}
	run.pending = len(order)
	run.ready = ready

	for i := 0; i < limit; i++ {
		g.Go(func() error {
			for {
				select {
				case <-gctx.Done():
					return nil
				case id, ok := <-ready:
					if !ok {
						return nil
					}
					if err := run.executeNode(gctx, id); err != nil {
						return err
					}
				}
			}
		})
	}

	gErr := g.Wait()

	// Finalize: anything not passed or failed was cancelled by short-circuit.
	run.mu.Lock()
	for id, res := range run.results {
		if res.Status == StatusPending || res.Status == StatusRunning {
			res.Status = StatusSkipped
			res.Summary = "cancelled by early short-circuit"
			run.results[id] = res
		}
	}
	run.mu.Unlock()

	result := &Result{
		Order:     idsOf(order),
		Nodes:     run.resultsSnapshot(),
		StartedAt: started,
		EndedAt:   time.Now(),
	}
	if run.shortCircuit != nil {
		result.OK = false
		result.Err = run.shortCircuit
		result.Cancelled = run.cancelledNodes()
		return result, fmt.Errorf("%w: %w", ErrShortCircuited, run.shortCircuit)
	}
	if gErr != nil {
		result.OK = false
		result.Err = gErr
		return result, gErr
	}
	result.OK = true
	return result, nil
}

// dagRun owns the mutable state of one DAG execution. All state transitions
// happen under mu, except the context-bound validation calls.
type dagRun struct {
	dag       *DAG
	patches   []Patch
	remaining map[string]int
	succ      map[string][]string
	ready     chan string
	pending   int

	mu           sync.Mutex
	results      map[string]NodeResult
	shortCircuit error
}

// executeNode runs one node and schedules its successors under lock.
func (r *dagRun) executeNode(ctx context.Context, id string) error {
	r.mu.Lock()
	if ctx.Err() != nil {
		r.mu.Unlock()
		return nil
	}
	node := r.dag.nodes[id]
	now := time.Now()
	r.results[id] = NodeResult{ID: id, Stage: node.Stage, Status: StatusRunning, StartedAt: now}
	r.mu.Unlock()

	vres, verr := node.Validator.Validate(ctx, r.patches)

	r.mu.Lock()
	defer r.mu.Unlock()
	now = time.Now()

	if verr != nil {
		// A context cancellation is not a stage failure: the run was stopped
		// (by the caller or by another stage failing), so the node is
		// recorded as skipped rather than failed.
		if errors.Is(verr, context.Canceled) || errors.Is(verr, context.DeadlineExceeded) {
			prev := r.results[id]
			r.results[id] = NodeResult{
				ID: id, Stage: node.Stage, Status: StatusSkipped,
				Err: verr, StartedAt: prev.StartedAt, EndedAt: now,
				Summary: "cancelled by early short-circuit",
			}
			r.pending--
			return verr
		}
		prev := r.results[id]
		r.results[id] = NodeResult{
			ID: id, Stage: node.Stage, Status: StatusFailed,
			Err: verr, StartedAt: prev.StartedAt, EndedAt: now,
			Summary: "validator error",
		}
		r.pending--
		r.shortCircuit = fmt.Errorf("%w: %w", ErrValidationFailed, verr)
		return verr
	}
	if vres == nil || !vres.OK {
		prev := r.results[id]
		summary := "validation failed"
		if vres != nil && vres.Summary != "" {
			summary = vres.Summary
		}
		r.results[id] = NodeResult{
			ID: id, Stage: node.Stage, Status: StatusFailed,
			Result: vres, StartedAt: prev.StartedAt, EndedAt: now,
			Summary: summary,
		}
		r.pending--
		r.shortCircuit = fmt.Errorf("%w: %s", ErrValidationFailed, summary)
		return r.shortCircuit
	}

	prev := r.results[id]
	r.results[id] = NodeResult{
		ID: id, Stage: node.Stage, Status: StatusPassed,
		Result: vres, StartedAt: prev.StartedAt, EndedAt: now,
		Summary: "passed",
	}
	r.pending--
	for _, succ := range r.succ[id] {
		r.remaining[succ]--
		if r.remaining[succ] == 0 {
			r.ready <- succ
		}
	}
	if r.pending == 0 {
		close(r.ready)
	}
	return nil
}

// resultsSnapshot returns a deep copy of the per-node results.
func (r *dagRun) resultsSnapshot() map[string]NodeResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]NodeResult, len(r.results))
	for id, res := range r.results {
		out[id] = res
	}
	return out
}

// cancelledNodes returns the ids of nodes skipped by short-circuiting.
func (r *dagRun) cancelledNodes() []string {
	snapshot := r.resultsSnapshot()
	var out []string
	for id, res := range snapshot {
		if res.Status == StatusSkipped {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// NodeResult is the outcome of one node within a run.
type NodeResult struct {
	// ID of the node.
	ID string
	// Stage performed by the node.
	Stage Stage
	// Status of the node.
	Status Status
	// Result is the validator's structured result, when produced.
	Result *ValidationResult
	// Err is the Go error from the validator, when it failed by error.
	Err error
	// Summary is a concise outcome description.
	Summary string
	// StartedAt records when the node began executing.
	StartedAt time.Time
	// EndedAt records when the node finished.
	EndedAt time.Time
}

// Result is the immutable outcome of a DAG execution.
type Result struct {
	// OK reports whether every scheduled node passed.
	OK bool
	// Order lists the node ids in topological execution order.
	Order []string
	// Nodes maps node id to its per-node outcome.
	Nodes map[string]NodeResult
	// Cancelled lists the node ids skipped by early short-circuiting.
	Cancelled []string
	// Err is the run-level error, when the run failed.
	Err error
	// StartedAt records when the run began.
	StartedAt time.Time
	// EndedAt records when the run finished.
	EndedAt time.Time
}

// Passed returns the ids of nodes that passed.
func (r *Result) Passed() []string {
	return r.filtered(StatusPassed)
}

// Failed returns the ids of nodes that failed.
func (r *Result) Failed() []string {
	return r.filtered(StatusFailed)
}

// Skipped returns the ids of nodes cancelled by short-circuiting.
func (r *Result) Skipped() []string {
	return r.filtered(StatusSkipped)
}

func (r *Result) filtered(s Status) []string {
	var out []string
	for id, res := range r.Nodes {
		if res.Status == s {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// idsOf projects nodes to their ids in order.
func idsOf(nodes []*Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}
