package contextspec

import "errors"

// Typed Context Domain failures. These are deliberately distinct sentinels so
// the Control Plane can route them differently; they are never collapsed into
// a generic error.
var (
	// ErrStaleContextCandidate is returned when a compiler candidate was
	// produced from a conversation revision that is no longer current. The
	// candidate MUST be discarded; it must never overwrite newer human state.
	ErrStaleContextCandidate = errors.New("contextspec: stale context compilation candidate")

	// ErrStaleWorkspaceSnapshot is returned when the live workspace no longer
	// matches the snapshot an ExecutionSpec was frozen against. Callers must
	// re-hand-off; they must never silently refresh and continue.
	ErrStaleWorkspaceSnapshot = errors.New("contextspec: stale workspace snapshot")

	// ErrInvalidContext is returned when a ContextSpec/ExecutionSpec is missing
	// or structurally invalid.
	ErrInvalidContext = errors.New("contextspec: invalid context")

	// ErrUnresolvedExecutionTarget is returned when an execution hand-off
	// requires a target set but the compiled context resolved none.
	ErrUnresolvedExecutionTarget = errors.New("contextspec: unresolved execution target")

	// ErrAuthorizationFailure is reserved for the Control Plane so an
	// authorization rejection is never confused with a context/execution fault.
	ErrAuthorizationFailure = errors.New("contextspec: authorization failure")

	// ErrPolicyFailure is reserved for policy rejections observed at the
	// hand-off boundary.
	ErrPolicyFailure = errors.New("contextspec: policy failure")
)
