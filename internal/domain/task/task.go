// Package task implements the canonical Task model of the Izen control plane.
//
// The package answers exactly one question: "What work needs to be performed?"
// A Task is a pure value describing a single unit of work (a file mutation, a
// shell execution, a git action, or a verification) with a type, a lifecycle
// status, a target and a rationale. The package contains NO execution logic,
// patch application, or policy evaluation — that is the job of the execution
// and policy layers that consume task.Task.
//
// This is the single canonical representation of tasks across the Microkernel
// (pkg/engine/plan) and Generative LLM planners (internal/modes/plan): both
// pipelines emit task.Task / task.Plan values, and every execution layer
// consumes them. It depends only on the standard library and has zero imports
// of internal/modes, internal/ui, or pkg/engine/plan.
package task

// TaskType discriminates the kind of work a Task performs. It replaces the
// legacy free-text type labels ("FILE_MUTATE", "SHELL_EXEC", "GIT_ACTION")
// that previously flowed between the planners and the execution engine.
type TaskType string

const (
	// TaskFileMutate is a whole-file mutation (create / delete / rewrite).
	TaskFileMutate TaskType = "FILE_MUTATE"
	// TaskFileEdit is a targeted in-place edit of a file.
	TaskFileEdit TaskType = "FILE_EDIT"
	// TaskShellExec runs a shell command (e.g. go mod tidy, go test ./...).
	TaskShellExec TaskType = "SHELL_EXEC"
	// TaskGitAction performs a git operation (commit, branch, ...).
	TaskGitAction TaskType = "GIT_ACTION"
	// TaskVerify runs a deterministic verification of the plan's outcome.
	TaskVerify TaskType = "VERIFY"
)

// String returns the machine-readable type label.
func (t TaskType) String() string { return string(t) }

// Valid reports whether the type is one of the defined task types.
func (t TaskType) Valid() bool {
	switch t {
	case TaskFileMutate, TaskFileEdit, TaskShellExec, TaskGitAction, TaskVerify:
		return true
	default:
		return false
	}
}

// TaskStatus is the atomic lifecycle state of a single task.
type TaskStatus string

const (
	// StatusIdle is the default state before execution begins.
	StatusIdle TaskStatus = "idle"
	// StatusProcessing is set while the task is actively being executed.
	StatusProcessing TaskStatus = "processing"
	// StatusDone is set once the task finished successfully.
	StatusDone TaskStatus = "done"
	// StatusFailed is set when the task errored during execution.
	StatusFailed TaskStatus = "failed"
	// StatusStalled is set when the task cannot advance (blocked or suspended).
	StatusStalled TaskStatus = "stalled"
)

// String returns the machine-readable status label.
func (s TaskStatus) String() string { return string(s) }

// IsTerminal reports whether the status is a terminal state (done or failed).
func (s TaskStatus) IsTerminal() bool {
	return s == StatusDone || s == StatusFailed
}

// Task is the canonical, strongly-typed representation of a single unit of
// work. It is a value: planners emit it, the execution engine consumes it, and
// projections render it. It carries no execution behaviour.
type Task struct {
	ID          string     `json:"id,omitempty"`
	StepNum     int        `json:"step_num"`
	Type        TaskType   `json:"type"`
	Status      TaskStatus `json:"status"`
	Target      string     `json:"target"`
	Description string     `json:"description"`
	Rationale   string     `json:"rationale,omitempty"`
	Solution    string     `json:"solution,omitempty"`
	IsHardcoded bool       `json:"is_hardcoded,omitempty"`
	IsFastTrack bool       `json:"is_fast_track,omitempty"`
	// Evidence carries deterministic structural findings compiled by the
	// autonomy runtime BEFORE the model is asked to interpret or propose (e.g.
	// a Context Evidence Ledger for the mutation target). The model reasons
	// over this ledger; it never discovers structural facts on its own.
	Evidence string `json:"evidence,omitempty"`
}

// Done reports whether the task has reached the completed state. It is the
// canonical completion predicate used in place of a legacy boolean field.
func (t Task) Done() bool { return t.Status == StatusDone }

// IsTerminal reports whether the task has reached a terminal state.
func (t Task) IsTerminal() bool { return t.Status.IsTerminal() }

// Plan is the canonical representation of an ordered set of tasks produced by
// a planner (microkernel or LLM).
type Plan struct {
	Tasks       []Task `json:"tasks"`
	IsFastTrack bool   `json:"is_fast_track,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// NewPlan builds a Plan from tasks, marking whether it was produced on the
// fast-track (zero-LLM) path.
func NewPlan(tasks []Task, isFastTrack bool, summary string) *Plan {
	return &Plan{
		Tasks:       append([]Task(nil), tasks...),
		IsFastTrack: isFastTrack,
		Summary:     summary,
	}
}
