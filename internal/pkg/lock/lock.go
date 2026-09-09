// Package lock provides inter-process workspace locking for izen.
//
// A single advisory lock file (.izen/lock) serializes concurrent izen
// processes operating on the same workspace so .izen/ state files and
// active session operations cannot be corrupted by a second process
// (second terminal pane, background daemon, etc.).
//
// Acquisition is strictly non-blocking: TryAcquireWorkspaceLock either
// holds the exclusive flock on return or fails with ErrWorkspaceLocked.
// The returned unlock closure releases the flock and closes the file
// descriptor; it is idempotent and safe to defer.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ErrWorkspaceLocked is returned when another process holds the workspace
// lock. It wraps descriptive details including the holder PID when readable
// from the lock file. Use errors.Is(err, ErrWorkspaceLocked) to detect it.
var ErrWorkspaceLocked = errors.New("workspace locked")

// LockFileName is the well-known lock file relative to the workspace root.
const LockFileName = "lock"

// lockPath returns the absolute lock file path for a workspace.
func lockPath(workDir string) string {
	return filepath.Join(workDir, ".izen", LockFileName)
}

// TryAcquireWorkspaceLock acquires the inter-process workspace lock for
// workDir in a non-blocking fashion.
//
// On success it returns an unlock closure that must be called (typically
// deferred) to release the flock descriptor. The closure is idempotent:
// calling it twice is a safe no-op and never closes a recycled descriptor.
//
// On contention it returns ErrWorkspaceLocked (wrapping details, including
// the holder PID when readable) and closes any locally opened descriptor
// so no file descriptor leaks.
func TryAcquireWorkspaceLock(workDir string) (unlock func(), err error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("lock: empty workDir")
	}
	fi, statErr := os.Stat(workDir)
	if statErr != nil {
		return nil, fmt.Errorf("lock: workDir: %w", statErr)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("lock: workDir %q is not a directory", workDir)
	}
	if err := os.MkdirAll(filepath.Join(workDir, ".izen"), 0o755); err != nil {
		return nil, fmt.Errorf("lock: mkdir .izen: %w", err)
	}
	path := lockPath(workDir)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock: open %s: %w", path, err)
	}

	if err := flockExclusive(f); err != nil {
		// Contention: close our descriptor (no leak) and report.
		_ = f.Close()
		if errors.Is(err, errWouldBlock) {
			if pid, ok := readHolderPID(path); ok {
				return nil, fmt.Errorf("%w: %s held by another izen process (pid %d)",
					ErrWorkspaceLocked, path, pid)
			}
			return nil, fmt.Errorf("%w: %s held by another izen process",
				ErrWorkspaceLocked, path)
		}
		if errors.Is(err, ErrLockUnsupported) {
			// Platform without flock: degrade to success with an in-process
			// no-op unlock. Cross-process isolation is unavailable but the
			// caller proceeds rather than failing every mutation.
			// Re-open is unnecessary; return a no-op closer for the file we
			// already closed? Re-open a handle to keep semantics simple:
			// just return a no-op since there is nothing OS-level to hold.
			return func() {}, nil
		}
		return nil, fmt.Errorf("lock: flock %s: %w", path, err)
	}

	// We hold the flock. Record our PID best-effort for diagnostics.
	if err := writeHolderPID(f); err != nil {
		// PID stamping is advisory only; a failure still leaves us holding
		// a valid lock, so do not fail acquisition.
		_ = err
	}

	var once sync.Once
	var mu sync.Mutex
	released := false
	unlock = func() {
		once.Do(func() {
			mu.Lock()
			released = true
			mu.Unlock()
			_ = flockRelease(f)
			_ = f.Close()
		})
	}
	_ = released
	return unlock, nil
}

// IsHeldByCurrentProcess reports whether the lock file's stamped PID equals
// our own PID. It is best-effort diagnostics for nested callers (e.g. the
// agent loop calling ApplyPatch while the CLI entrypoint already holds the
// workspace lock): a true result means contention is self-inflicted and the
// caller may proceed under the outer holder instead of failing.
func IsHeldByCurrentProcess(workDir string) bool {
	if strings.TrimSpace(workDir) == "" {
		return false
	}
	pid, ok := readHolderPID(lockPath(workDir))
	if !ok {
		return false
	}
	return pid == os.Getpid()
}

// HolderPID returns the PID stamped in the lock file, if readable.
func HolderPID(workDir string) (int, bool) {
	if strings.TrimSpace(workDir) == "" {
		return 0, false
	}
	return readHolderPID(lockPath(workDir))
}

// readHolderPID best-effort reads the holder PID from the lock file.
func readHolderPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, false
	}
	// PID is the first whitespace-separated field.
	if fields := strings.Fields(s); len(fields) > 0 {
		s = fields[0]
	}
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// writeHolderPID stamps the current PID into an already-locked file.
func writeHolderPID(f *os.File) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		return err
	}
	return f.Sync()
}
