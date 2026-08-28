package execution

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// ── P0 Execution Core — Boundary Integrity & Rollback ────────────────────────
//
// The execution core guarantees atomicity: any unrecoverable artifact failure,
// protocol rejection, or exhausted retry budget triggers an immediate DAG
// abortion and instant workspace rollback. Partial writes are forbidden. After
// rollback, the live workspace digest is cryptographically asserted against
// base_tree_digest; any mismatch halts with a DigestMismatchError.
//
// This file owns MutationBoundary, the rollback helper, and the telemetry
// trace required by the invariant: [boundary] state rollback verified
// digest=<hash> match=<bool>.

// ExecutionBoundary is the production MutationBoundary. It binds a workspace
// root and its target geometry to the OCC verifier so digest assertions are
// always measured against the same target set that was baselined.
type ExecutionBoundary struct {
	root    string
	targets []string
	occ     *OCCVerifier
}

// NewExecutionBoundary constructs a boundary scoped to root+targets. Targets
// are deduplicated via OCC normalization; an empty set hashes deterministically
// as the empty workspace.
func NewExecutionBoundary(root string, targets []string) *ExecutionBoundary {
	return &ExecutionBoundary{
		root:    root,
		targets: targets,
		occ:     NewOCCVerifier(root),
	}
}

// AssertWorkspaceIntegrity recomputes the live tree digest over the bound
// targets and compares it to baseDigest. It ALWAYS emits the telemetry trace
// [boundary] state rollback verified digest=<hash> match=<bool> and returns a
// *DigestMismatchError when they diverge.
func (b *ExecutionBoundary) AssertWorkspaceIntegrity(baseDigest string) error {
	if b == nil || b.occ == nil {
		return fmt.Errorf("boundary: not configured")
	}
	live := b.occ.TreeDigest(b.targets)
	match := live == baseDigest
	log.Printf("[boundary] state rollback verified digest=%s match=%v", live, match)
	if !match {
		return &DigestMismatchError{Expected: baseDigest, Actual: live, Targets: b.targets}
	}
	return nil
}

// RollbackAndVerify restores the exact originals map (target → bytes) onto
// disk, then cryptographically asserts the live digest equals baseDigest.
// It is the Fail-Closed DAG abort helper: restore first, verify second, halt
// on mismatch. Every invocation emits the [boundary] telemetry line.
func RollbackAndVerify(root string, targets []string, baseDigest string, originals map[string][]byte) error {
	// Restore every original in deterministic order.
	for _, t := range sortedTargets(originals) {
		full := filepath.Join(root, filepath.FromSlash(t))
		data := originals[t]
		if data == nil {
			// Creation intent that was applied: remove the file that was created.
			_ = os.Remove(full)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("boundary: rollback mkdir %s: %w", t, err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return fmt.Errorf("boundary: rollback write %s: %w", t, err)
		}
	}
	// Remove any target that did not exist at baseline but now exists and
	// was not in originals (exhaustive cleanup): they contribute to the digest.
	// We only need to ensure originals restoration is sufficient; the digest
	// check will surface any stray files.

	b := NewExecutionBoundary(root, targets)
	return b.AssertWorkspaceIntegrity(baseDigest)
}

// sortedTargets returns map keys in deterministic order.
func sortedTargets(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Deterministic lexical order.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// Ensure ExecutionBoundary implements MutationBoundary.
var _ MutationBoundary = (*ExecutionBoundary)(nil)
