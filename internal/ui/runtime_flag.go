package ui

import "os"

// runtimeExecutorEnabled reports whether the RuntimeExecutor cutover is
// enabled via the IZEN_RUNTIME_EXECUTOR environment variable.
//
//	disabled (default) → legacy mode-engine execution (rollback path)
//	enabled  (IZEN_RUNTIME_EXECUTOR=1) → RuntimeExecutor execution authority
//
// The flag is a migration mechanism only. It is NOT a permanent execution
// mode: once the cutover is validated the legacy path is removed and the flag
// disappears. Every migration step that alters production routing MUST be
// gated on this flag so a regression can flip the whole cutover back.
func runtimeExecutorEnabled() bool {
	return os.Getenv("IZEN_RUNTIME_EXECUTOR") == "1"
}
