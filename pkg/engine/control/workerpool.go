package control

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

// Executor performs the actual work of a single execution node. It returns the
// observation the control loop folds back into the Dynamic IR. Executors are
// the Execution Plane of the adaptive control system: they execute, they never
// decide.
type Executor interface {
	Execute(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error)
}

// ExecutorFunc adapts a plain function to the Executor interface.
type ExecutorFunc func(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error)

// Execute implements Executor.
func (f ExecutorFunc) Execute(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
	if f == nil {
		return ir.ObservationPayload{}, nil
	}
	return f(ctx, node, vars)
}

// WorkItem is a single dispatch unit handed to the worker pool. It couples the
// static node definition with the variable snapshot captured at dispatch time.
type WorkItem struct {
	// Node is the static definition of the work to perform.
	Node *ir.ExecutionNode
	// Vars is the variable snapshot captured at dispatch time.
	Vars ir.Variables
}

// defaultPoolLimit bounds concurrent node execution when no explicit limit is
// given.
const defaultPoolLimit = 4

// WorkerPool executes work items with bounded concurrency. The orchestrator
// dispatches work strictly through the pool — no node ever executes outside
// it.
type WorkerPool struct {
	limit int
	exec  Executor
	now   func() time.Time
}

// NewWorkerPool returns a pool running at most limit nodes concurrently over
// exec. limit <= 0 uses the default bound.
func NewWorkerPool(limit int, exec Executor) *WorkerPool {
	if limit <= 0 {
		limit = defaultPoolLimit
	}
	return &WorkerPool{
		limit: limit,
		exec:  exec,
		now:   time.Now,
	}
}

// Submit runs the given work items with bounded concurrency and returns a
// channel of observations in completion order. The channel is closed when all
// items finish. A cancelled context marks unfinished items as failed
// observations rather than blocking. Submit is safe for concurrent use; each
// call owns its own worker goroutines.
func (p *WorkerPool) Submit(ctx context.Context, items []WorkItem) <-chan ir.ObservationPayload {
	out := make(chan ir.ObservationPayload, len(items))
	if len(items) == 0 {
		close(out)
		return out
	}
	sem := make(chan struct{}, p.limit)
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func(item WorkItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out <- p.run(ctx, item)
		}(item)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// run executes one work item, always producing a well-formed observation: a
// node id is guaranteed, panics are converted to failed observations, and
// context cancellation marks the item as failed.
func (p *WorkerPool) run(ctx context.Context, item WorkItem) (obs ir.ObservationPayload) {
	obs = ir.ObservationPayload{NodeID: item.Node.ID, Timestamp: p.now()}
	defer func() {
		if r := recover(); r != nil {
			obs.OK = false
			obs.Err = fmt.Sprintf("worker panic: %v", r)
		}
	}()
	if err := ctx.Err(); err != nil {
		obs.OK = false
		obs.Err = err.Error()
		return obs
	}
	if p.exec == nil {
		obs.OK = false
		obs.Err = "control: no executor configured"
		return obs
	}
	res, err := p.exec.Execute(ctx, item.Node, item.Vars)
	if res.NodeID == "" {
		res.NodeID = item.Node.ID
	}
	if res.Timestamp.IsZero() {
		res.Timestamp = p.now()
	}
	if err != nil {
		res.OK = false
		res.Err = err.Error()
	}
	return res
}
