package authorization

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/core/workflow"
)

// ── Phase 3 P3 — production source-hash verifier ────────────────────────────
//
// The historical noop placeholder is replaced by a real sha256 verifier over
// the DECLARED mutation domain: it recomputes the domain hash of the proposal
// targets against the live workspace and compares it (constant-time) with the
// snapshot hash captured at proposal time. A divergence means an out-of-band
// writer touched a target between snapshot and authorize — the freshness gate
// denies the mutation at StepDependencyFreshness.
//
// The verify scope is strictly target-scoped: only paths listed by the
// proposal are read, never the workspace at large.

// SourceHashMismatchError reports a stale mutation domain: the live hash of
// the declared targets no longer matches the declared snapshot hash.
type SourceHashMismatchError struct {
	Expected string
	Actual   string
}

func (e *SourceHashMismatchError) Error() string {
	return fmt.Sprintf("source hash mismatch: snapshot %s… vs workspace %s…",
		shortHex(e.Expected), shortHex(e.Actual))
}

// sha256SourceHashVerifier is the production SourceHashVerifier.
type sha256SourceHashVerifier struct {
	root string
}

func newSHA256SourceHashVerifier(root string) SourceHashVerifier {
	return &sha256SourceHashVerifier{root: root}
}

// VerifySourceHash recomputes the sha256 domain hash over the declared target
// files and compares it with snapshotHash. An EMPTY snapshotHash declares no
// baseline — the freshness gate is not applicable and passes (callers that do
// declare one are always verified). Any mismatch fails closed.
func (v *sha256SourceHashVerifier) VerifySourceHash(paths []string, snapshotHash string) error {
	if strings.TrimSpace(snapshotHash) == "" {
		// No declared baseline to protect: not-applicable, never a silent pass
		// for a stale hash.
		return nil
	}
	actual, err := DomainSourceHash(v.root, paths)
	if err != nil {
		return fmt.Errorf("source hash verification failed: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(actual), []byte(strings.ToLower(strings.TrimSpace(snapshotHash)))) != 1 {
		return &SourceHashMismatchError{Expected: snapshotHash, Actual: actual}
	}
	return nil
}

// DomainSourceHash computes the deterministic sha256 identity of a declared
// mutation domain: every path contributes its workspace-relative identity and
// its current content (or a canonical absent marker) in a length-prefixed,
// sorted, injection-proof encoding. Missing targets contribute deterministically
// so a deleted file changes the hash while creation-intent domains stay stable
// as long as nothing appeared.
func DomainSourceHash(root string, paths []string) (string, error) {
	dedup := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		p = filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
		if p == "" || p == "." || seen[p] {
			continue
		}
		seen[p] = true
		dedup = append(dedup, p)
	}
	sort.Strings(dedup)

	var b strings.Builder
	b.WriteString("izen-source-domain-v1")
	b.WriteString(":")
	b.WriteString(strconv.Itoa(len(dedup)))
	for _, p := range dedup {
		full := filepath.Join(root, filepath.FromSlash(p))
		b.WriteByte(0)
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.WriteString(p)
		b.WriteByte(':')
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			b.WriteString("absent")
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", p, err)
		}
		sum := sha256.Sum256(data)
		b.WriteString(hex.EncodeToString(sum[:]))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

func shortHex(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(empty)"
	}
	return s
}

type productionCheckpointChecker struct {
	checkpointDir string
}

func newProductionCheckpointChecker(root string) CheckpointChecker {
	return &productionCheckpointChecker{
		checkpointDir: filepath.Join(root, ".izen", "checkpoints"),
	}
}

func (c *productionCheckpointChecker) HasCheckpoint() bool {
	info, err := os.Stat(c.checkpointDir)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(c.checkpointDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			cpFile := filepath.Join(c.checkpointDir, e.Name(), "checkpoint.json")
			if _, err := os.Stat(cpFile); err == nil {
				return true
			}
		}
	}
	return false
}

func (c *productionCheckpointChecker) LatestCheckpoint() (workflow.CheckpointRef, error) {
	entries, err := os.ReadDir(c.checkpointDir)
	if err != nil {
		return "", err
	}
	var latest string
	for _, e := range entries {
		if e.IsDir() {
			cpFile := filepath.Join(c.checkpointDir, e.Name(), "checkpoint.json")
			if _, err := os.Stat(cpFile); err == nil {
				if e.Name() > latest {
					latest = e.Name()
				}
			}
		}
	}
	if latest == "" {
		return "", nil
	}
	return workflow.CheckpointRef(latest), nil
}

// NewProductionAuthorizationEngine wires the production AuthorizationEngine:
// the real sha256 source-hash freshness gate plus the on-disk checkpoint
// checker under .izen/checkpoints/.
func NewProductionAuthorizationEngine(root string, getState func() workflow.WorkflowState) *AuthorizationEngine {
	return NewAuthorizationEngine(
		newSHA256SourceHashVerifier(root),
		newProductionCheckpointChecker(root),
		getState,
	)
}
