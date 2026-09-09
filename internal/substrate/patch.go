// Package substrate implements the Phase 3 patch staging and evidence
// pipeline under .izen/.
package substrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/audit"
	"github.com/PizenLabs/izen/internal/checkpoint"
	"github.com/PizenLabs/izen/internal/pkg/atomicio"
	"github.com/PizenLabs/izen/internal/pkg/lock"
)

// ErrPatchTransactionFailed wraps any mid-transaction patch write failure
// after automatic rollback. Use errors.Is(err, ErrPatchTransactionFailed)
// to detect it; the original cause is wrapped and available via errors.Unwrap.
var ErrPatchTransactionFailed = errors.New("substrate: patch transaction failed")

// atomicWriteFile is the file-write hook used by ApplyPatch. It defaults to
// atomicio.WriteFileAtomic and is overrideable in tests to inject mid-patch
// failures and prove rollback atomicity.
var atomicWriteFile = atomicio.WriteFileAtomic

// PatchFile is one file mutation inside a candidate patch.
type PatchFile struct {
	Path    string  `json:"path"`
	Content *string `json:"content,omitempty"`
	Delete  bool    `json:"delete,omitempty"`
}

// Patch is the staged candidate patch document.
type Patch struct {
	RunID string      `json:"run_id,omitempty"`
	Step  int         `json:"step,omitempty"`
	Files []PatchFile `json:"files"`
}

// patchesDir returns the .izen/patches root for a workspace.
func patchesDir(workDir string) string {
	return filepath.Join(workDir, ".izen", "patches")
}

// StagePatch saves a candidate patch to
// .izen/patches/run-<run_id>-patch-<step>.json atomically and records a
// patch_staged audit event (best-effort: staging succeeds even if the audit
// write fails).
func StagePatch(workDir, runID string, step int, patch Patch) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("substrate: empty workDir")
	}
	safeRun := sanitizeRunID(runID)
	if safeRun == "" {
		return "", fmt.Errorf("substrate: empty run id")
	}
	if step < 0 {
		return "", fmt.Errorf("substrate: negative step %d", step)
	}
	patch.RunID = safeRun
	patch.Step = step
	if patch.Files == nil {
		patch.Files = []PatchFile{}
	}
	data, err := json.MarshalIndent(patch, "", "  ")
	if err != nil {
		return "", fmt.Errorf("substrate: marshal patch: %w", err)
	}
	name := fmt.Sprintf("run-%s-patch-%d.json", safeRun, step)
	dst := filepath.Join(patchesDir(workDir), name)
	if err := atomicio.WriteFileAtomic(dst, data, 0o644); err != nil {
		return "", fmt.Errorf("substrate: stage patch: %w", err)
	}
	// Best-effort audit trail.
	alog := audit.NewLogger(workDir)
	_ = alog.LogEvent("", audit.EventPatchStaged, map[string]any{
		"run_id": safeRun, "step": step, "path": dst, "files": len(patch.Files),
	})
	return dst, nil
}

// ApplyPatch validates a staged patch, creates a pre-patch-tx checkpoint
// BEFORE touching the workspace, applies the mutations transactionally, and
// logs the result to .izen/audit/mutations.log.
//
// Transactional guarantee: every target write goes through
// atomicio.WriteFileAtomic and the mutated paths are tracked. On any write
// error, permission denial, or partial failure the workspace is rolled back
// to the pre-patch-tx checkpoint, incomplete artifacts are cleaned up, and
// the returned error wraps ErrPatchTransactionFailed with the original cause.
//
// Malformed JSON fails before any checkpoint is taken and leaves the
// workspace untouched. Semantic validation failures happen after the
// pre-patch-tx checkpoint is captured, so the workspace is still untouched
// and the checkpoint is preserved for recovery (these are validation
// errors, not transaction failures, and do not trigger rollback).
func ApplyPatch(workDir string, patchPath string) error {
	if strings.TrimSpace(workDir) == "" {
		return fmt.Errorf("substrate: empty workDir")
	}
	data, err := os.ReadFile(patchPath)
	if err != nil {
		return fmt.Errorf("substrate: read patch: %w", err)
	}
	var patch Patch
	if err := json.Unmarshal(data, &patch); err != nil {
		return fmt.Errorf("substrate: malformed patch %q: %w", patchPath, err)
	}
	if patch.Files == nil {
		return fmt.Errorf("substrate: malformed patch %q: missing files", patchPath)
	}

	// Inter-process workspace lock: serialize against concurrent izen
	// processes. A self-held lock (outer CLI/agent holder in this process)
	// is reused instead of failing.
	var unlock func()
	if u, lerr := lock.TryAcquireWorkspaceLock(workDir); lerr != nil {
		if errors.Is(lerr, lock.ErrWorkspaceLocked) && lock.IsHeldByCurrentProcess(workDir) {
			unlock = func() {}
		} else {
			return fmt.Errorf("substrate: workspace lock: %w", lerr)
		}
	} else {
		unlock = u
	}
	defer unlock()

	cpID, err := checkpoint.CreateCheckpoint(workDir, "pre-patch-tx")
	if err != nil {
		return fmt.Errorf("substrate: pre-patch checkpoint: %w", err)
	}

	// Semantic validation AFTER the checkpoint so the checkpoint is
	// preserved on validation failure while the workspace stays untouched.
	for i := range patch.Files {
		if err := validatePatchFile(workDir, patch.Files[i]); err != nil {
			return fmt.Errorf("substrate: invalid patch entry %d: %w (pre-patch checkpoint %s preserved)", i, err, cpID)
		}
	}

	alog := audit.NewLogger(workDir)
	var mutated []string
	fail := func(op, target string, cause error) error {
		rbErr := checkpoint.Rollback(workDir, cpID)
		cleanupIncompleteArtifacts(workDir, mutated)
		_ = alog.LogEvent("", audit.EventPatchApplied, map[string]any{
			"patch": patchPath, "checkpoint": cpID, "status": "rolled_back",
		})
		if rbErr != nil {
			return fmt.Errorf("%w: substrate: %s %s: %w (rollback %s failed: %s)",
				ErrPatchTransactionFailed, op, target, cause, cpID, rbErr.Error())
		}
		return fmt.Errorf("%w: substrate: %s %s: %w (rolled back to %s)",
			ErrPatchTransactionFailed, op, target, cause, cpID)
	}

	for _, f := range patch.Files {
		dst := filepath.Join(workDir, filepath.FromSlash(f.Path))
		if f.Delete {
			if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				return fail("delete", f.Path, err)
			}
			mutated = append(mutated, f.Path)
			_ = alog.LogMutation(audit.MutationEntry{
				File: f.Path, Action: "delete", PatchID: cpID,
			})
			continue
		}
		content := ""
		if f.Content != nil {
			content = *f.Content
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fail("mkdir for", f.Path, err)
		}
		if err := atomicWriteFile(dst, []byte(content), 0o644); err != nil {
			return fail("write", f.Path, err)
		}
		mutated = append(mutated, f.Path)
		_ = alog.LogMutation(audit.MutationEntry{
			File: f.Path, Action: "write", PatchID: cpID, Content: truncate(content, 4096),
		})
	}
	_ = alog.LogEvent("", audit.EventPatchApplied, map[string]any{
		"patch": patchPath, "checkpoint": cpID, "files": len(patch.Files),
	})
	return nil
}

// cleanupIncompleteArtifacts best-effort removes atomic-write temp leftovers
// (<target>.tmp.*) under the mutated files' directories after a rollback.
func cleanupIncompleteArtifacts(workDir string, mutated []string) {
	seen := make(map[string]struct{})
	for _, rel := range mutated {
		dir := filepath.Dir(filepath.Join(workDir, filepath.FromSlash(rel)))
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.Contains(e.Name(), ".tmp.") {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// validatePatchFile enforces workspace containment and schema integrity.
func validatePatchFile(workDir string, f PatchFile) error {
	if strings.TrimSpace(f.Path) == "" {
		return fmt.Errorf("empty path")
	}
	if filepath.IsAbs(f.Path) {
		return fmt.Errorf("absolute path %q forbidden", f.Path)
	}
	clean := filepath.Clean(filepath.FromSlash(f.Path))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("path %q escapes workspace", f.Path)
	}
	slash := filepath.ToSlash(clean)
	if slash == ".izen" || strings.HasPrefix(slash, ".izen/") {
		return fmt.Errorf("path %q inside .izen is forbidden", f.Path)
	}
	if slash == ".git" || strings.HasPrefix(slash, ".git/") {
		return fmt.Errorf("path %q inside .git is forbidden", f.Path)
	}
	if !f.Delete && f.Content == nil {
		return fmt.Errorf("path %q: missing content", f.Path)
	}
	// Resolve symlinks/lexical containment against the workspace root.
	abs, err := filepath.Abs(filepath.Join(workDir, clean))
	if err != nil {
		return fmt.Errorf("resolve %q: %w", f.Path, err)
	}
	rootAbs, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("resolve workDir: %w", err)
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
		return fmt.Errorf("path %q escapes workspace", f.Path)
	}
	return nil
}

// sanitizeRunID keeps alphanumerics, dash and underscore; everything else
// becomes an underscore.
func sanitizeRunID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
