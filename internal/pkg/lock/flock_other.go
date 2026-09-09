//go:build !unix && !windows

package lock

import (
	"os"
)

// errWouldBlock is unused on platforms without flock support.
var errWouldBlock = ErrLockUnsupported

// flockExclusive degrades to unsupported on platforms without file locking.
func flockExclusive(_ *os.File) error { return ErrLockUnsupported }

// flockRelease is a no-op on platforms without file locking.
func flockRelease(_ *os.File) error { return nil }
