package ui

import (
	"github.com/PizenLabs/izen/internal/policy"
	"github.com/PizenLabs/izen/internal/ui/plan"
)

// PermissionPromptMsg triggers the interactive security-permission modal and
// blocks the requesting agent goroutine until the human resolves it.
//
// The sender creates a buffered RespCh (capacity 1), dispatches this message
// onto the Bubble Tea event loop (e.g. program.Send), then blocks on
// `<-RespCh`. The UI resolves exactly once via PermissionResolvedMsg and
// always delivers a PermissionResponse — denial included — so the agent
// goroutine can never hang on an abandoned channel.
type PermissionPromptMsg struct {
	Req    policy.PermissionRequest
	RespCh chan policy.PermissionResponse
}

// PermissionResolvedMsg delivers the human's explicit decision back to the
// Agent Executor loop. The Update handler forwards Resp into the request's
// RespCh (non-blocking) and clears the modal state.
type PermissionResolvedMsg struct {
	Resp policy.PermissionResponse
}

// ShowDiffMsg triggers the full-screen unified diff viewer modal. DiffText
// is a standard unified diff (e.g. `git diff` output); Title labels the view.
type ShowDiffMsg struct {
	DiffText string
	Title    string
}

// ToggleDiffCollapseMsg toggles context folding for one hunk.
// FileIdx/HunkIdx < 0 toggles the hunk under the viewport cursor;
// FileIdx < 0 && HunkIdx < 0 toggles all hunks globally.
type ToggleDiffCollapseMsg struct {
	FileIdx int
	HunkIdx int
}

// ── Agent Execution Plan & Live Tool Cards ─────────────────────────────
// PlanUpdateMsg replaces or updates the current execution plan state.
type PlanUpdateMsg struct {
	Plan plan.ExecutionPlan
}

// PlanStepMsg updates a single plan step in place.
type PlanStepMsg struct {
	ID      string
	Status  plan.TaskStatus
	Elapsed int64 // nanoseconds; <0 leaves elapsed unchanged
	Error   string
}

// ToolStartMsg spawns a new active Tool Output Card.
type ToolStartMsg struct {
	ID       string
	ToolName string
	Command  string
}

// ToolChunkMsg appends stdout/stderr stream data to a card.
type ToolChunkMsg struct {
	ID    string
	Chunk []byte
}

// ToolEndMsg marks process completion and schedules auto-collapse.
type ToolEndMsg struct {
	ID       string
	ExitCode int
	Err      error
	// ElapsedNs carries the process duration in nanoseconds (0 = auto).
	ElapsedNs int64
}

// toolCollapseMsg fires after the auto-collapse delay for completed cards.
type toolCollapseMsg struct {
	ID string
}

// ToolToggleMsg toggles expansion of a historical output log.
type ToolToggleMsg struct {
	ID string // empty = most recent card
}
