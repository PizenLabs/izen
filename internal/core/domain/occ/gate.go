package occ

import (
	"errors"
	"fmt"
	"sync"
)

// ErrStaleDependency is the sentinel for an OCC conflict: the caller's
// expected version does not match the current committed version, so the
// mutation must be rejected and the caller must re-sync.
var ErrStaleDependency = errors.New("occ: stale state version, conflict detected")

// OCCGate is the state mutation gate enforcing monotonic progression:
//
//	CommitAllowed(V_expected, V_current) iff V_expected == V_current
//
// It is safe for concurrent use. The gate owns the linearizable compare-and-
// increment of a single entity's version clock.
type OCCGate struct {
	mu sync.Mutex
}

// ValidateAndAdvance atomically validates that the caller's expected version
// matches the current committed version and, on success, advances the clock
// by one. On conflict it returns the current version and an error wrapping
// ErrStaleDependency; the current version is NOT advanced.
func (g *OCCGate) ValidateAndAdvance(current *StateVersion, expected StateVersion) (StateVersion, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if *current != expected {
		return *current, fmt.Errorf("%w: expected version %d, current version %d", ErrStaleDependency, expected, *current)
	}

	*current = current.Next()
	return *current, nil
}
