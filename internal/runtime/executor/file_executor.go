package executor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/scope"
)

// ErrWorkspaceEscape is the execution-boundary sentinel for file targets
// resolving outside the workspace root FD handle. It aliases
// scope.ErrWorkspaceEscape; match with errors.Is.
var ErrWorkspaceEscape = scope.ErrWorkspaceEscape

// defaultCommitMode is applied to committed files that carry a zero
// permission set, mirroring the workspace-wide file default.
const defaultCommitMode fs.FileMode = 0o644

// FileExecutor applies approved proposals transactionally over a captured
// FileBackup snapshot. It owns zero-orphan temp-file cleanup and automatic
// rollback: any commit failure restores the pre-mutation state.
//
// Execution-time confinement: when bound to a workspace-root FD handle
// (WithScopeRoot), every Commit, PrepareSnapshot, and Rollback verifies
// its target at USE time against the open descriptor. A symlink swapped
// in between resolution and commit that points outside the root fails
// closed with ErrWorkspaceEscape and mutates nothing outside the
// boundary. Internal symlinks resolving within the root remain
// permitted.
type FileExecutor struct {
	scopeRoot *scope.Root
}

// NewExecutor returns a ready-to-use FileExecutor.
func NewExecutor() *FileExecutor {
	return &FileExecutor{}
}

// WithScopeRoot anchors the executor to an open workspace-root FD
// handle. The caller retains ownership (Close); the executor never
// closes a handle it did not open.
func (e *FileExecutor) WithScopeRoot(r *scope.Root) *FileExecutor {
	if e == nil {
		return e
	}
	e.scopeRoot = r
	return e
}

// verifyUse enforces use-time confinement for an absolute or
// root-relative target path. Unbound executors (nil scopeRoot) skip
// verification for backward compatibility; every execution-time caller
// must bind a root.
func (e *FileExecutor) verifyUse(targetPath string) error {
	if e == nil || e.scopeRoot == nil {
		return nil
	}
	root := e.scopeRoot.RootPath()
	rel := targetPath
	if filepath.IsAbs(targetPath) {
		r, err := filepath.Rel(root, targetPath)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: target %q escapes workspace", scope.ErrWorkspaceEscape, targetPath)
		}
		rel = r
	}
	return e.scopeRoot.Verify(rel)
}

// PrepareSnapshot captures the pre-mutation state of targetPath. A missing
// file yields a snapshot with Exists = false so rollback can safely remove
// whatever the commit creates.
func (e *FileExecutor) PrepareSnapshot(targetPath string) (*FileBackup, error) {
	if e == nil {
		return nil, errors.New("executor: nil FileExecutor")
	}
	if targetPath == "" {
		return nil, errors.New("executor: snapshot requires a target path")
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Verify the (not-yet-created) target's ancestry at use time
			// so a swapped parent symlink cannot redirect creation.
			if verr := e.verifyUse(targetPath); verr != nil {
				return nil, verr
			}
			return &FileBackup{Path: targetPath, Exists: false}, nil
		}
		return nil, fmt.Errorf("executor: snapshot read %q: %w", targetPath, err)
	}
	// Use-time confinement: the snapshot read must have come from inside
	// the boundary; a swapped symlink fails closed here.
	if verr := e.verifyUse(targetPath); verr != nil {
		return nil, verr
	}

	mode := uint32(defaultCommitMode)
	if info, statErr := os.Stat(targetPath); statErr == nil {
		mode = uint32(info.Mode().Perm())
	}

	return &FileBackup{
		Path:     targetPath,
		Exists:   true,
		Content:  append([]byte(nil), data...),
		FileMode: mode,
	}, nil
}

// Commit materializes the proposal against the snapshot's base content and
// writes the result atomically: content is written to a same-directory temp
// file (.tmp.izen.*), fsynced, and renamed over targetPath. On any failure
// Commit automatically invokes Rollback and returns the cause; a rollback
// failure is appended to the returned error.
func (e *FileExecutor) Commit(proposal ProposedMutation, backup *FileBackup) error {
	if e == nil {
		return errors.New("executor: nil FileExecutor")
	}
	if proposal.TargetRef == nil {
		return errors.New("executor: commit requires a non-nil target reference")
	}
	if backup == nil {
		return errors.New("executor: commit requires a backup snapshot")
	}

	targetPath := backup.Path
	if targetPath == "" {
		targetPath = proposal.TargetRef.Canonical
	}
	if targetPath == "" {
		return errors.New("executor: commit requires a target path")
	}

	base := ""
	if backup.Exists {
		base = string(backup.Content)
	}

	final, err := materializeContent(proposal, base)
	if err != nil {
		return e.failWithRollback(backup, err)
	}

	// Execution-time confinement gate: verify at USE time, immediately
	// before the atomic write, so a TOCTOU symlink swap fails closed
	// with ErrWorkspaceEscape and nothing is written outside.
	if verr := e.verifyUse(targetPath); verr != nil {
		return verr
	}
	// An invalid existing file may be the very thing this mutation repairs.
	// Symbol hygiene is meaningful only when its baseline can be indexed;
	// artifact/syntax validation of the result remains a separate gate.
	if baseline, indexErr := NewSymbolBaseline(map[string]string{targetPath: base}); indexErr == nil {
		if redundant := baseline.Check(targetPath, final); redundant != nil { return redundant }
	}

	mode := fs.FileMode(backup.FileMode)
	if mode == 0 {
		mode = defaultCommitMode
	}

	if err := e.atomicWrite(targetPath, final, mode); err != nil {
		return e.failWithRollback(backup, err)
	}
	return nil
}

// atomicWrite writes content to targetPath atomically: content is written to a
// same-directory temp file (.tmp.izen.*), fsynced, and renamed over targetPath.
// On any failure the temp file is removed (zero-orphan guard). It never reads
// the target, so it may be driven purely from an in-memory snapshot.
func (e *FileExecutor) atomicWrite(targetPath, content string, mode fs.FileMode) error {
	if targetPath == "" {
		return errors.New("executor: atomic write requires a target path")
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("executor: create directory for %q: %w", targetPath, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp.izen.*")
	if err != nil {
		return fmt.Errorf("executor: create temp for %q: %w", targetPath, err)
	}
	tmpName := tmp.Name()

	// Zero-orphan guard: unless the temp was atomically renamed into place,
	// it is removed when the write returns.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write([]byte(content)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("executor: write temp for %q: %w", targetPath, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("executor: chmod temp for %q: %w", targetPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("executor: sync temp for %q: %w", targetPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("executor: close temp for %q: %w", targetPath, err)
	}

	// Second use-time gate immediately before the rename: closes the
	// temp-write→rename window against a swap planted during the write.
	if verr := e.verifyUse(targetPath); verr != nil {
		return verr
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("executor: rename temp to %q: %w", targetPath, err)
	}
	committed = true

	fsyncDir(dir)
	return nil
}

// Rollback restores the pre-mutation state captured by the backup snapshot:
// existing files are rewritten byte-for-byte with their original permissions,
// and files that did not exist before are removed. Rolling back a pristine
// snapshot is idempotent and safe.
func (e *FileExecutor) Rollback(backup *FileBackup) error {
	if e == nil {
		return errors.New("executor: nil FileExecutor")
	}
	if backup == nil {
		return errors.New("executor: rollback requires a backup snapshot")
	}
	if backup.Path == "" {
		return errors.New("executor: rollback requires a target path")
	}

	// Confinement holds for rollback too: restoring state must never
	// write outside the boundary if the path was swapped mid-flight.
	if verr := e.verifyUse(backup.Path); verr != nil {
		return verr
	}

	if backup.Exists {
		mode := fs.FileMode(backup.FileMode)
		if mode == 0 {
			mode = defaultCommitMode
		}
		if err := os.WriteFile(backup.Path, backup.Content, mode); err != nil {
			return fmt.Errorf("executor: rollback restore %q: %w", backup.Path, err)
		}
		if err := os.Chmod(backup.Path, mode); err != nil {
			return fmt.Errorf("executor: rollback chmod %q: %w", backup.Path, err)
		}
		return nil
	}

	if err := os.Remove(backup.Path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("executor: rollback remove %q: %w", backup.Path, err)
	}
	return nil
}

// failWithRollback rolls back backup and returns cause. A rollback failure is
// appended to the returned error so the caller learns the workspace was not
// fully restored.
func (e *FileExecutor) failWithRollback(backup *FileBackup, cause error) error {
	if rbErr := e.Rollback(backup); rbErr != nil {
		return fmt.Errorf("%w (rollback failed: %w)", cause, rbErr)
	}
	return cause
}

// fsyncDir best-effort flushes dir to disk so a completed rename is durable.
// Failures are intentionally ignored: correctness is guaranteed by rollback,
// durability is best-effort.
func fsyncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
