// Package kernel defines the pure execution core of the Izen Agent Runtime V3:
// task lifecycle, context handling, timeout enforcement, and terminal status
// classification.
//
// The package is deliberately coupled to nothing: it carries no AI/LLM
// concepts, never touches a file system, and depends only on the standard
// library and internal/events.
package kernel

import domaintask "github.com/PizenLabs/izen/internal/domain/task"

// ── STEP 1 transitional bridge ────────────────────────────────────────────
// Canonical execution state types live in internal/domain/task. These aliases
// keep the legacy execution bridge (Engine, Runtime) compiling while
// high-level callers migrate to the domain.
type ExecutionStatus = domaintask.ExecutionStatus

const (
	StatusPending   = domaintask.ExecStatusPending
	StatusRunning   = domaintask.ExecStatusRunning
	StatusCompleted = domaintask.ExecStatusCompleted
	StatusFailed    = domaintask.ExecStatusFailed
	StatusCanceled  = domaintask.ExecStatusCanceled
)
