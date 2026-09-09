package lock

import (
	"errors"
)

// ErrLockUnsupported is returned by the platform flock hook when the OS
// does not provide file locking.
var ErrLockUnsupported = errors.New("lock: OS file locking unsupported on this platform")
