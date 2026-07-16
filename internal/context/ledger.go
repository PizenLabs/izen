package context

import "sync"

// ── Sliding-Window Task Queue (Isolation State Machine) ──────────────────────
//
// The TaskLedger implements a deterministic Sliding Window architecture with a
// static frame capacity constraint (WindowSize = 1). Even if the Ledger state
// holds dozens of unresolved operations, the renderer MUST isolate and inject
// only the first pending task node combined with its corresponding context
// block.
//
// RULE: The sliding window is rigidly locked on the active index. It is
// forbidden to shift or inflate the prompt context window with future tasks
// until the current active task transitions definitively to a Success state.
// ─────────────────────────────────────────────────────────────────────────────

// DefaultWindowSize is the maximum number of tasks exposed in a single render.
// Set to 1 to enforce strict single-task focus for local LLM context limits.
const DefaultWindowSize = 1

// TaskStatus is the atomic lifecycle state of a single plan task within the
// shared transaction context. It is the bridge between the /build execution
// layer and the /plan visual checklist layer.
type TaskStatus int

const (
	// TaskPending is the default state before execution begins.
	TaskPending TaskStatus = iota
	// TaskExecuting is set while /build is actively mutating the file.
	TaskExecuting
	// TaskCompleted is set once /build commits the patch successfully AND
	// the deterministic verification gate (go fmt, go vet) passes.
	TaskCompleted
	// TaskFailed is set when the patch transaction aborts or the verification
	// gate rejects the mutation.
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

// IsTerminal returns true for completed or failed states.
func (s TaskStatus) IsTerminal() bool {
	return s == TaskCompleted || s == TaskFailed
}

// TaskLedger is the atomic task state map that synchronises /plan's checklist
// with /build's execution. It implements a sliding window of size WindowSize —
// only the first non-terminal task is exposed to the renderer.
type TaskLedger struct {
	mu         sync.RWMutex
	states     map[int]TaskStatus
	orderedIDs []int
	windowSize int
}

// NewTaskLedger creates an empty ledger with DefaultWindowSize.
func NewTaskLedger() *TaskLedger {
	return &TaskLedger{
		states:     make(map[int]TaskStatus),
		windowSize: DefaultWindowSize,
	}
}

// NewWindowedTaskLedger creates a ledger with a custom window size.
// The window size must be >= 1.
func NewWindowedTaskLedger(windowSize int) *TaskLedger {
	if windowSize < 1 {
		windowSize = DefaultWindowSize
	}
	return &TaskLedger{
		states:     make(map[int]TaskStatus),
		windowSize: windowSize,
	}
}

// SetWindowSize adjusts the sliding window after construction.
func (l *TaskLedger) SetWindowSize(n int) {
	if n < 1 {
		n = DefaultWindowSize
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.windowSize = n
}

func (l *TaskLedger) MarkPending(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.states[id]; !exists {
		l.orderedIDs = append(l.orderedIDs, id)
	}
	l.states[id] = TaskPending
}

func (l *TaskLedger) MarkExecuting(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.states[id]; !exists {
		l.orderedIDs = append(l.orderedIDs, id)
	}
	l.states[id] = TaskExecuting
}

// MarkCompleted records a successful /build commit for task id.
// Only transitions to Completed when the verification gate passes.
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

// IsCompleted reports whether task id has been committed by /build.
func (l *TaskLedger) IsCompleted(id int) bool {
	return l.Status(id) == TaskCompleted
}

// IsFailed reports whether task id has been marked as failed.
func (l *TaskLedger) IsFailed(id int) bool {
	return l.Status(id) == TaskFailed
}

// IsTerminal reports whether task id is in a terminal state.
func (l *TaskLedger) IsTerminal(id int) bool {
	return l.Status(id).IsTerminal()
}

// ── Sliding Window Accessors ─────────────────────────────────────────────────

// FirstPending returns the task ID of the first non-terminal task in insertion
// order, or 0 if all tasks are terminal or the ledger is empty.
// This is the primary sliding-window accessor — the renderer MUST call this
// to determine which task to include in the prompt context.
func (l *TaskLedger) FirstPending() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, id := range l.orderedIDs {
		s, ok := l.states[id]
		if !ok || s.IsTerminal() {
			continue
		}
		return id
	}
	return 0
}

// ActiveWindow returns the slice of task IDs within the sliding window
// starting from the first non-terminal task. The size is capped at windowSize.
func (l *TaskLedger) ActiveWindow() []int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	start := -1
	count := 0
	for _, id := range l.orderedIDs {
		s, ok := l.states[id]
		if !ok || s.IsTerminal() {
			continue
		}
		if start < 0 {
			start = id
		}
		count++
		if count >= l.windowSize {
			break
		}
	}

	if start < 0 {
		return nil
	}

	window := make([]int, 0, l.windowSize)
	for _, id := range l.orderedIDs {
		if id < start {
			continue
		}
		s, ok := l.states[id]
		if !ok || s.IsTerminal() {
			continue
		}
		window = append(window, id)
		if len(window) >= l.windowSize {
			break
		}
	}
	return window
}

// WindowSize returns the current window size.
func (l *TaskLedger) WindowSize() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.windowSize
}

// TotalPending returns the count of non-terminal tasks across the full ledger
// (not windowed). This is used for progress reporting outside the prompt.
func (l *TaskLedger) TotalPending() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	count := 0
	for _, s := range l.states {
		if !s.IsTerminal() {
			count++
		}
	}
	return count
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
