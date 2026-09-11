package durable

import (
	"encoding/json"
	"fmt"
	"syscall"

	"github.com/PizenLabs/izen/internal/pkg/atomicio"
)

// pidAlive reports whether pid names a currently running process.
// A nil/own pid is alive; pid <= 1 (init) outside our own is treated as
// alive to stay conservative. ESRCH => dead; EPERM => alive (no signal
// permission but the process exists).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == syscall.Getpid() {
		return true
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if err == syscall.ESRCH {
		return false
	}
	// EPERM or any other error: the pid exists (or we cannot prove it
	// does not), so treat it as alive and let lease expiry decide.
	return true
}

// writeLockMetaAtomic persists lock metadata via temp-file + rename so a
// SIGKILL mid-write never leaves a half-written lock file behind.
func writeLockMetaAtomic(path string, m LockMetadata) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("durable: marshal lock: %w", err)
	}
	data = append(data, '\n')
	if err := atomicio.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("durable: write lock: %w", err)
	}
	return nil
}
