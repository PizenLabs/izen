//go:build windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errWouldBlock is the canonical non-blocking contention sentinel on Windows:
// LockFileEx with LOCKFILE_FAIL_IMMEDIATELY reports ERROR_LOCK_VIOLATION when
// the region is held by another open file handle.
var errWouldBlock = errors.New("flock: lock held by another process")

// flockExclusive attempts a NON-BLOCKING exclusive byte-range lock on the whole
// file via LockFileEx. It maps the Windows lock-violation error onto
// errWouldBlock so the acquire loop in lock.go retries with the same
// timeout/backoff semantics as the Unix flock tier. The lock covers a single
// byte at offset 0; the exclusive lock is released by UnlockFileEx on the same
// handle (see flockRelease), or implicitly when the handle closes.
func flockExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errWouldBlock
	}
	return err
}

// flockRelease releases a previously acquired exclusive byte-range lock.
func flockRelease(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
