//go:build !unix

package session

import (
	"os"
)

// errWouldBlock is unused on non-unix platforms; kept for symmetry with the
// unix build so lock.go compiles identically.
var errWouldBlock = ErrLockUnsupported

// flockExclusive degrades to a no-op on platforms without flock support. The
// in-process mutex tier remains authoritative; the OS tier is reported as
// unsupported once by the acquire loop.
func flockExclusive(f *os.File) error { return ErrLockUnsupported }

// flockRelease is a no-op on platforms without flock support.
func flockRelease(f *os.File) error { return nil }
