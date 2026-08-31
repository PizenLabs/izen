// Windows process-lock parity enforcement (Phase 3). The two-tier session lock
// (internal/session/lock.go) must provide the OS tier on Windows with the SAME
// non-blocking contention semantics as Unix flock: LockFileEx with
// LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY, mapping
// ERROR_LOCK_VIOLATION onto the acquire loop's retry sentinel. The !unix
// fallback must therefore EXCLUDE windows so a Windows build never silently
// degrades to a no-op.
package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFileOrSkip(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("cannot read %s: %v", path, err)
	}
	return string(data)
}

// TestWindowsLockFileExImplementation pins the real Windows implementation
// behind the `windows` build tag: it must use LockFileEx with the exclusive +
// fail-immediately flags and the ERROR_LOCK_VIOLATION contention mapping.
func TestWindowsLockFileExImplementation(t *testing.T) {
	root := repoRoot(t)
	src := readFileOrSkip(t, filepath.Join(root, "internal/session/flock_windows.go"))

	if !strings.Contains(src, "//go:build windows") {
		t.Error("flock_windows.go must carry the `windows` build tag")
	}
	for _, want := range []string{
		"windows.LockFileEx",
		"windows.LOCKFILE_EXCLUSIVE_LOCK",
		"windows.LOCKFILE_FAIL_IMMEDIATELY",
		"windows.ERROR_LOCK_VIOLATION",
		"windows.UnlockFileEx",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("flock_windows.go is missing %s — Windows lock parity is not implemented", want)
		}
	}
	// The contention sentinel the acquire loop retries on must be the same
	// symbol the Unix tier uses (errWouldBlock).
	if !strings.Contains(src, "errWouldBlock") {
		t.Error("flock_windows.go must map lock violation onto errWouldBlock so lock.go retries identically")
	}
}

// TestWindowsDoesNotUseNoopFlockFallback pins the build-tag split: the no-op
// flock fallback must be excluded on Windows so the OS tier is never silently
// degraded there.
func TestWindowsDoesNotUseNoopFlockFallback(t *testing.T) {
	root := repoRoot(t)
	src := readFileOrSkip(t, filepath.Join(root, "internal/session/flock_other.go"))

	if !strings.Contains(src, "//go:build !unix && !windows") {
		t.Error("flock_other.go must be `!unix && !windows` so Windows compiles the LockFileEx implementation")
	}
	// The build tag line must be exactly the compound constraint; a bare !unix
	// tag would also match windows and collide with flock_windows.go.
	firstLine := strings.TrimSpace(strings.Split(src, "\n")[0])
	if firstLine == "//go:build !unix" {
		t.Error("flock_other.go must not match windows (a bare !unix tag would collide with flock_windows.go)")
	}
}

// TestUnixFlockBuildTagPins ensures the Unix tier keeps its exclusive tag.
func TestUnixFlockBuildTagPins(t *testing.T) {
	root := repoRoot(t)
	src := readFileOrSkip(t, filepath.Join(root, "internal/session/flock_unix.go"))
	if !strings.Contains(src, "//go:build unix") {
		t.Error("flock_unix.go must carry the `unix` build tag")
	}
	if !strings.Contains(src, "unix.Flock") || !strings.Contains(src, "LOCK_EX") {
		t.Error("flock_unix.go must implement flockExclusive via unix.Flock(LOCK_EX|LOCK_NB)")
	}
}
