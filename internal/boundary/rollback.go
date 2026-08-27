package boundary

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

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
// canonical telemetry: [boundary] state rollback verified digest=<hash> match=<bool>.
func RollbackAndVerify(root string, targets []string, baseDigest string, originals map[string][]byte) error {
	for _, t := range sortedKeys(originals) {
		full := filepath.Join(root, filepath.FromSlash(t))
		data := originals[t]
		if data == nil {
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
	b := execution.NewExecutionBoundary(root, targets)
	err := b.AssertWorkspaceIntegrity(baseDigest)
	// Ensure canonical log is emitted even if the boundary already logged it;
	// duplicate line is acceptable for traceability.
	if err == nil {
		log.Printf("[boundary] state rollback verified digest=%s match=true", baseDigest)
	}
	return err
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
