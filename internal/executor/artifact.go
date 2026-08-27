package executor

import "github.com/PizenLabs/izen/internal/execution"

// Re-export the P0 execution core invariants so external callers can depend
// on the stable executor surface without importing the internal execution
// package directly. The interfaces are defined in execution; this package is
// the canonical import for P1 ingestion decoupling.

type BoundedPatch = execution.BoundedPatch
type ArtifactValidator = execution.ArtifactValidator
type MutationBoundary = execution.MutationBoundary
type DigestMismatchError = execution.DigestMismatchError

var (
	ErrFormatRejected  = execution.ErrFormatRejected
	ErrAmbiguousAnchor = execution.ErrAmbiguousAnchor
	ErrScopeViolation  = execution.ErrScopeViolation
)

// NewDefaultArtifactValidator constructs the production validator.
func NewDefaultArtifactValidator() execution.ArtifactValidator {
	return execution.NewDefaultArtifactValidator()
}

// NewExecutionBoundary constructs the production mutation boundary.
func NewExecutionBoundary(root string, targets []string) execution.MutationBoundary {
	return execution.NewExecutionBoundary(root, targets)
}
