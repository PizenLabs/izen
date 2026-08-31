//go:build unix

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// errWouldBlock is the canonical non-blocking contention sentinel on unix.
var errWouldBlock = errors.New("flock: lock held by another process")

// flockExclusive attempts a NON-BLOCKING exclusive flock. It returns
// errWouldBlock when the lock is held by another open file description (the
// cross-process contention signal the acquire loop retries on).
func flockExclusive(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return nil
	}
	if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
		return errWouldBlock
	}
	return err
}

// flockRelease releases a previously acquired exclusive flock.
func flockRelease(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		return err
	}
	return nil
}
