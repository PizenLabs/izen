package task

import (
	"fmt"
	"sync"
)

// CursorStatus describes the lifecycle of a single node within the cursor.
type CursorStatus string

const (
	// CursorStatusPending marks a node that has not started.
	CursorStatusPending CursorStatus = "pending"
	// CursorStatusActive marks a node currently being executed.
	CursorStatusActive CursorStatus = "active"
	// CursorStatusCompleted marks a node that finished successfully.
	CursorStatusCompleted CursorStatus = "completed"
	// CursorStatusBlocked marks a pending node whose dependencies are unmet.
	CursorStatusBlocked CursorStatus = "blocked"
	// CursorStatusFailed marks a node that errored during execution.
	CursorStatusFailed CursorStatus = "failed"
)

// Progress is a snapshot of the cursor's aggregate counts.
type Progress struct {
	// Total is the number of tasks in the graph.
	Total int
	// Completed counts successfully finished tasks.
	Completed int
	// Active counts tasks currently running.
	Active int
	// Pending counts tasks not yet started.
	Pending int
	// Blocked counts pending tasks with unmet dependencies.
	Blocked int
	// Failed counts tasks that errored.
	Failed int
}

// Cursor tracks execution progress through a task graph. It records which
// tasks are completed, active, and pending independently from the workflow
// phase, and is safe for concurrent use.
type Cursor struct {
	mu             sync.RWMutex
	deps           map[string][]string
	order          []string
	statuses       map[string]CursorStatus
	completedOrder []string
	activeOrder    []string
	failedOrder    []string
}

// NewCursor builds a cursor over a snapshot of the given graph. Mutations to
// the graph after construction do not affect the cursor.
func NewCursor(g *Graph) *Cursor {
	deps := make(map[string][]string, g.Len())
	order := make([]string, 0, g.Len())
	for _, t := range g.Tasks() {
		d := make([]string, len(t.Dependencies))
		copy(d, t.Dependencies)
		deps[t.ID] = d
		order = append(order, t.ID)
	}
	return &Cursor{
		deps:     deps,
		order:    order,
		statuses: make(map[string]CursorStatus, len(order)),
	}
}

// Status returns the current status of a task. Tasks absent from the cursor's
// snapshot report CursorStatusPending and false.
func (c *Cursor) Status(id string) (CursorStatus, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.deps[id]; !ok {
		return CursorStatusPending, false
	}
	if st, ok := c.statuses[id]; ok {
		return st, true
	}
	return CursorStatusPending, true
}

// Active returns the ids of tasks currently running, in activation order.
func (c *Cursor) Active() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.activeOrder))
	copy(out, c.activeOrder)
	return out
}

// Completed returns the ids of completed tasks in completion order.
func (c *Cursor) Completed() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.completedOrder))
	copy(out, c.completedOrder)
	return out
}

// Failed returns the ids of failed tasks in failure order.
func (c *Cursor) Failed() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.failedOrder))
	copy(out, c.failedOrder)
	return out
}

// Pending returns the ids of tasks that have not started, in insertion order.
func (c *Cursor) Pending() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pendingLocked()
}

// Blocked returns the ids of pending tasks whose dependencies are not all
// completed, in insertion order.
func (c *Cursor) Blocked() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for _, id := range c.pendingLocked() {
		if !c.depsDoneLocked(id) {
			out = append(out, id)
		}
	}
	return out
}

// pendingLocked lists pending ids in insertion order. Active, completed, and
// failed tasks are excluded.
func (c *Cursor) pendingLocked() []string {
	out := make([]string, 0, len(c.order))
	for _, id := range c.order {
		st := c.statuses[id]
		if st == CursorStatusCompleted || st == CursorStatusFailed || st == CursorStatusActive {
			continue
		}
		out = append(out, id)
	}
	return out
}

// depsDoneLocked reports whether every dependency of id is completed.
func (c *Cursor) depsDoneLocked(id string) bool {
	for _, dep := range c.deps[id] {
		if c.statuses[dep] != CursorStatusCompleted {
			return false
		}
	}
	return true
}

// Advance activates the next ready task and returns its id. Ready tasks are
// picked in insertion order. It returns ErrCursorComplete when every task is
// finished and ErrNoReadyTask when remaining tasks are blocked.
func (c *Cursor) Advance() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range c.order {
		st, ok := c.statuses[id]
		if ok && st != CursorStatusPending {
			continue
		}
		if c.depsDoneLocked(id) {
			c.statuses[id] = CursorStatusActive
			c.activeOrder = append(c.activeOrder, id)
			return id, nil
		}
	}
	if len(c.completedOrder)+len(c.failedOrder) == len(c.order) {
		return "", ErrCursorComplete
	}
	return "", ErrNoReadyTask
}

// Complete marks a task as finished. The task must exist and must not already
// be completed, and every dependency must be completed first. It returns
// ErrTaskAlreadyCompleted for a double completion.
func (c *Cursor) Complete(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.deps[id]; !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, id)
	}
	if c.statuses[id] == CursorStatusCompleted {
		return fmt.Errorf("%w: %s", ErrTaskAlreadyCompleted, id)
	}
	if !c.depsDoneLocked(id) {
		return fmt.Errorf("%w: %s", ErrDependenciesPending, id)
	}
	c.statuses[id] = CursorStatusCompleted
	c.completedOrder = append(c.completedOrder, id)
	c.removeActiveLocked(id)
	return nil
}

// Fail marks a task as failed and detaches it from the active set. Completing
// a failed task's dependents remains blocked because the failure is not a
// completed dependency.
func (c *Cursor) Fail(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.deps[id]; !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, id)
	}
	if c.statuses[id] == CursorStatusCompleted || c.statuses[id] == CursorStatusFailed {
		return nil
	}
	c.statuses[id] = CursorStatusFailed
	c.failedOrder = append(c.failedOrder, id)
	c.removeActiveLocked(id)
	return nil
}

// removeActiveLocked drops id from the active set.
func (c *Cursor) removeActiveLocked(id string) {
	for i, aid := range c.activeOrder {
		if aid == id {
			c.activeOrder = append(c.activeOrder[:i], c.activeOrder[i+1:]...)
			return
		}
	}
}

// Progress returns an aggregate snapshot of the cursor state.
func (c *Cursor) Progress() Progress {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p := Progress{Total: len(c.order)}
	for _, id := range c.order {
		switch c.statuses[id] {
		case CursorStatusCompleted:
			p.Completed++
		case CursorStatusActive:
			p.Active++
		case CursorStatusFailed:
			p.Failed++
		case CursorStatusBlocked:
			p.Blocked++
		case CursorStatusPending, "":
			if c.depsDoneLocked(id) {
				p.Pending++
			} else {
				p.Blocked++
			}
		}
	}
	return p
}

// IsComplete reports whether every task has finished (completed or failed).
func (c *Cursor) IsComplete() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.completedOrder)+len(c.failedOrder) == len(c.order)
}

// Reset clears all task statuses, returning the cursor to its initial state.
func (c *Cursor) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses = make(map[string]CursorStatus, len(c.order))
	c.completedOrder = nil
	c.activeOrder = nil
	c.failedOrder = nil
}
