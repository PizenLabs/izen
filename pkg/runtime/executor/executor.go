package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PizenLabs/izen/pkg/runtime/harness"
)

// defaultCommitMode is applied to committed files that carry a zero
// permission set, mirroring the workspace-wide file default.
const defaultCommitMode fs.FileMode = 0o644

// RuntimeExecutor applies approved proposals transactionally over a captured
// FileBackup snapshot. It owns zero-orphan temp-file cleanup and automatic
// rollback: any commit failure restores the pre-mutation state.
type RuntimeExecutor struct{}

// NewExecutor returns a ready-to-use RuntimeExecutor.
func NewExecutor() *RuntimeExecutor {
	return &RuntimeExecutor{}
}

// PrepareSnapshot captures the pre-mutation state of targetPath. A missing
// file yields a snapshot with Exists = false so rollback can safely remove
// whatever the commit creates.
func (e *RuntimeExecutor) PrepareSnapshot(targetPath string) (*FileBackup, error) {
	if e == nil {
		return nil, errors.New("executor: nil RuntimeExecutor")
	}
	if targetPath == "" {
		return nil, errors.New("executor: snapshot requires a target path")
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &FileBackup{Path: targetPath, Exists: false}, nil
		}
		return nil, fmt.Errorf("executor: snapshot read %q: %w", targetPath, err)
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
func (e *RuntimeExecutor) Commit(proposal ProposedMutation, backup *FileBackup) error {
	if e == nil {
		return errors.New("executor: nil RuntimeExecutor")
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

	mode := fs.FileMode(backup.FileMode)
	if mode == 0 {
		mode = defaultCommitMode
	}

	if err := e.atomicWrite(targetPath, final, mode); err != nil {
		return e.failWithRollback(backup, err)
	}
	return nil
}

// CommitMutation is the Sole-Authority commit path of the closed execution
// loop (Model Output -> RMAH -> Gate -> RuntimeExecutor). It materializes an
// approved harness.CandidateArtifact against the Observation-phase memory
// snapshot bytes — never a disk read — and writes the result atomically.
//
// The memory snapshot is the only base: RMAH, the gate pipeline, and this
// method consume the same []byte captured once per cycle at the Observation
// phase (zero disk-read redundancy).
func (e *RuntimeExecutor) CommitMutation(ctx context.Context, candidate harness.CandidateArtifact, memorySnapshot []byte) error {
	if e == nil {
		return errors.New("executor: nil RuntimeExecutor")
	}
	if candidate.TargetFile == "" {
		return errors.New("executor: commit requires a candidate target file")
	}

	final, err := materializeCandidate(candidate, memorySnapshot)
	if err != nil {
		return fmt.Errorf("executor: materialize candidate: %w", err)
	}
	return e.atomicWrite(candidate.TargetFile, final, defaultCommitMode)
}

// atomicWrite writes content to targetPath atomically: content is written to a
// same-directory temp file (.tmp.izen.*), fsynced, and renamed over targetPath.
// On any failure the temp file is removed (zero-orphan guard). It never reads
// the target, so it may be driven purely from an in-memory snapshot.
func (e *RuntimeExecutor) atomicWrite(targetPath, content string, mode fs.FileMode) error {
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
func (e *RuntimeExecutor) Rollback(backup *FileBackup) error {
	if e == nil {
		return errors.New("executor: nil RuntimeExecutor")
	}
	if backup == nil {
		return errors.New("executor: rollback requires a backup snapshot")
	}
	if backup.Path == "" {
		return errors.New("executor: rollback requires a target path")
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
func (e *RuntimeExecutor) failWithRollback(backup *FileBackup, cause error) error {
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
