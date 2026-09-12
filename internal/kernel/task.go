package kernel

import domaintask "github.com/PizenLabs/izen/internal/domain/task"

// ── STEP 1 transitional bridge ────────────────────────────────────────────
// Canonical task DTOs live in internal/domain/task. Aliases keep the Engine
// bridge compiling while graph/planner callers migrate to the domain.
type TaskResult = domaintask.TaskResult
type Executable = domaintask.Executable
