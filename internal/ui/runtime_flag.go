package ui

import "os"

// runtimeExecutorEnabled reports whether the RuntimeExecutor is the production
// execution authority.
//
//	enabled (default) → RuntimeExecutor execution authority
//	disabled (IZEN_RUNTIME_EXECUTOR=0) → legacy mode-engine execution
//
// The flag is a migration mechanism only and is NOT a permanent execution
// mode. It now defaults to ENABLED: the RuntimeExecutor is the production
// execution authority. The legacy path remains reachable ONLY through the
// explicit IZEN_RUNTIME_EXECUTOR=0 override, which is itself removed in the
// Phase 3 pruning once the legacy execution authority is deleted.
func runtimeExecutorEnabled() bool {
	return os.Getenv("IZEN_RUNTIME_EXECUTOR") != "0"
}
