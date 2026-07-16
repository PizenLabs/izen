package context

import "sync"

// TaskStatus is the atomic lifecycle state of a single plan task within the
// shared transaction context. It is the bridge between the /build execution
// layer and the /plan visual checklist layer.
type TaskStatus int

const (
	// TaskPending is the default state before execution begins.
	TaskPending TaskStatus = iota
	// TaskExecuting is set while /build is actively mutating the file.
	TaskExecuting
	// TaskCompleted is set once /build commits the patch successfully.
	TaskCompleted
	// TaskFailed is set when the patch transaction aborts.
	TaskFailed
)

func (s TaskStatus) String() string {
	switch s {
	case TaskPending:
		return "PENDING"
	case TaskExecuting:
		return "EXECUTING"
	case TaskCompleted:
		return "COMPLETED"
	case TaskFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// TaskLedger is the atomic task state map that synchronises /plan's checklist
// with /build's execution. It is safe for concurrent use by the build loop and
// the plan renderer.
type TaskLedger struct {
	mu     sync.RWMutex
	states map[int]TaskStatus
}

// NewTaskLedger creates an empty ledger.
func NewTaskLedger() *TaskLedger {
	return &TaskLedger{states: make(map[int]TaskStatus)}
}

func (l *TaskLedger) MarkPending(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.states[id] = TaskPending
}

func (l *TaskLedger) MarkExecuting(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.states[id] = TaskExecuting
}

// MarkCompleted records a successful /build commit for task id.
func (l *TaskLedger) MarkCompleted(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.states[id] = TaskCompleted
}

func (l *TaskLedger) MarkFailed(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.states[id] = TaskFailed
}

// Status returns the current state for id, defaulting to TaskPending.
func (l *TaskLedger) Status(id int) TaskStatus {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if s, ok := l.states[id]; ok {
		return s
	}
	return TaskPending
}

// IsCompleted reports whether task id has been committed by /build. It is the
// method the /plan renderer uses, kept minimal so plan can consume the ledger
// through a small interface without importing this package.
func (l *TaskLedger) IsCompleted(id int) bool {
	return l.Status(id) == TaskCompleted
}

// All returns a snapshot of every tracked task state.
func (l *TaskLedger) All() map[int]TaskStatus {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[int]TaskStatus, len(l.states))
	for k, v := range l.states {
		out[k] = v
	}
	return out
}
