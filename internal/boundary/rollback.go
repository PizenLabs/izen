// Package boundary is the workspace-integrity ASSERTION surface.
//
// The rollback authority itself lives in exactly one place:
// internal/execution.RollbackAndVerify, next to the MutationSet and the OCC
// verifier it shares a digest with. This package is a thin re-export so a
// caller that only needs the integrity assertion does not have to import the
// execution authority — it is NOT a second implementation.
//
// It previously carried its own os.MkdirAll / os.WriteFile / os.Remove copy of
// the restore loop, which made "the workspace was rolled back" obtainable
// through a path that asserted nothing and was not covered by any architecture
// guard. There is now one restore implementation and one digest assertion.
package boundary

import (
	"github.com/PizenLabs/izen/internal/execution"
)

// DigestMismatchError re-exports the execution boundary digest error.
type DigestMismatchError = execution.DigestMismatchError

// AssertWorkspaceIntegrity verifies the live tree digest against baseDigest
// and emits the required telemetry trace.
func AssertWorkspaceIntegrity(root string, targets []string, baseDigest string) error {
	b := execution.NewExecutionBoundary(root, targets)
	return b.AssertWorkspaceIntegrity(baseDigest)
}

// RollbackAndVerify restores originals and verifies digest, emitting the
// canonical telemetry: [boundary] state rollback verified digest=<hash>
// match=<bool>.
//
// It delegates to the single authoritative boundary rather than reimplementing
// the restore loop, so the restore and the assertion that justifies it are one
// operation owned by one component.
func RollbackAndVerify(root string, targets []string, baseDigest string, originals map[string][]byte) error {
	return execution.RollbackAndVerify(root, targets, baseDigest, originals)
}
