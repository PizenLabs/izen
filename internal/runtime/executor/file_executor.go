package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/kernelbridge"
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
// FileBackup snapshot. It owns automatic rollback: any commit failure restores
// the pre-mutation state.
//
// What it owns, and keeps owning, is the transaction rather than the bytes:
// which target a proposal names, what that target should hold, whether the
// result is redundant, and what must be undone when anything fails. The final
// filesystem effect of a commit and of a rollback is placed by the Runtime
// Kernel — see kernelcommit.go for the full crossing and for the two rules
// that govern it.
//
// Zero-orphan cleanup is no longer this file's job for the same reason. It
// never belonged here: the temp file that could be orphaned was always the
// kernel capability's staging file, and the kernel deletes it on every path
// that does not rename it into place.
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

	// workspace is the root every mutation of this executor is authorized
	// against. It is the root the kernel grant is formed over, so an executor
	// without one has nothing to ask about and refuses to place bytes rather
	// than guessing a root.
	workspace string
}

// NewExecutor returns a FileExecutor with no workspace bound.
//
// The result can snapshot and validate, but any mutation fails with
// ErrUnboundWorkspace. Bind it with WithWorkspace (or WithScopeRoot) at the
// composition root, where the workspace root is already known.
func NewExecutor() *FileExecutor {
	return &FileExecutor{}
}

// WithWorkspace declares the root every mutation of this executor is
// authorized against. It is the root the kernel grant is formed over, so it
// must be the workspace the caller actually intends to change.
func (e *FileExecutor) WithWorkspace(root string) *FileExecutor {
	if e == nil {
		return e
	}
	e.workspace = root
	return e
}

// WithScopeRoot anchors the executor to an open workspace-root FD
// handle. The caller retains ownership (Close); the executor never
// closes a handle it did not open.
//
// It also declares the workspace, because the FD handle already knows the
// root and a grant needs one. Binding both from a single call is what keeps
// them from disagreeing: a handle at one root and a grant at another would
// confine the check and the effect to different workspaces, which is a
// guarantee that reads as if it held and does not.
func (e *FileExecutor) WithScopeRoot(r *scope.Root) *FileExecutor {
	if e == nil {
		return e
	}
	e.scopeRoot = r
	if r != nil {
		e.workspace = r.RootPath()
	}
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
			return fmt.Errorf("%w: target %q escapes workspace", ErrWorkspaceEscape, targetPath)
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
// places the result through the Runtime Kernel. On any failure Commit
// automatically invokes Rollback and returns the cause; a rollback failure is
// appended to the returned error.
//
// The context is threaded to the one place that can honour it — the kernel
// execution — so a withdrawn execution stops the kernel rather than
// abandoning a goroutine that is still placing bytes.
func (e *FileExecutor) Commit(ctx context.Context, proposal ProposedMutation, backup *FileBackup) error {
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
		return e.failWithRollback(ctx, backup, err)
	}

	// Execution-time confinement gate: verify at USE time, immediately
	// before the write, so a TOCTOU symlink swap fails closed
	// with ErrWorkspaceEscape and nothing is written outside.
	if verr := e.verifyUse(targetPath); verr != nil {
		return verr
	}
	// An invalid existing file may be the very thing this mutation repairs.
	// Symbol hygiene is meaningful only when its baseline can be indexed;
	// artifact/syntax validation of the result remains a separate gate.
	if baseline, indexErr := NewSymbolBaseline(map[string]string{targetPath: base}); indexErr == nil {
		if redundant := baseline.Check(targetPath, final); redundant != nil {
			return redundant
		}
	}

	applied, target, err := e.commitThroughKernel(ctx, targetPath, final)
	if err != nil {
		return e.failWithKernelWrite(ctx, backup, applied, target, err)
	}
	return nil
}

// Rollback restores the pre-mutation state captured by the backup snapshot:
// existing files are rewritten byte-for-byte with their original permissions,
// and files that did not exist before are removed. Both directions are placed
// through the Runtime Kernel, so a restore that cannot be proven is reported
// rather than assumed. Rolling back a pristine snapshot is idempotent and
// safe.
//
// The permissions FileBackup carries are no longer applied by an explicit
// chmod, and the guarantee is not lost: the kernel's write capability
// preserves the destination's existing permission bits across the atomic
// replace and defaults to 0o644 for a destination that does not exist. Those
// are exactly the two cases the local mode arithmetic covered, because the
// commit that created the divergence started from the same snapshot.
func (e *FileExecutor) Rollback(ctx context.Context, backup *FileBackup) error {
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
		if err := e.restoreThroughKernel(ctx, backup.Path, string(backup.Content)); err != nil {
			return fmt.Errorf("executor: rollback restore %q: %w", backup.Path, err)
		}
		return nil
	}

	if err := e.removeThroughKernel(ctx, backup.Path); err != nil {
		return fmt.Errorf("executor: rollback remove %q: %w", backup.Path, err)
	}
	return nil
}

// failWithRollback rolls back backup and returns cause. A rollback failure is
// appended to the returned error so the caller learns the workspace was not
// fully restored.
func (e *FileExecutor) failWithRollback(ctx context.Context, backup *FileBackup, cause error) error {
	if rbErr := e.Rollback(ctx, backup); rbErr != nil {
		return fmt.Errorf("%w (rollback failed: %w)", cause, rbErr)
	}
	return cause
}

// failWithKernelWrite is failWithRollback for a commit whose kernel execution
// did not prove the write, annotated with whether bytes actually reached disk.
//
// The annotation is the point. "The kernel could not prove it" covers two
// workspace states that a caller must be able to tell apart: a refused write
// that changed nothing, and a write that landed and then failed its
// independent re-read. Both are rolled back — that is what the transaction
// is for — but only the second is an executed mutation, and reporting a
// rollback that undid nothing as a rollback of real bytes teaches a reader
// that this evidence is noise.
func (e *FileExecutor) failWithKernelWrite(ctx context.Context, backup *FileBackup, applied kernelbridge.Applied, target string, cause error) error {
	executed := applied.Wrote(target)
	rolled := e.failWithRollback(ctx, backup, cause)
	if !executed {
		return rolled
	}
	return fmt.Errorf("%w (kernel evidence records %q as written before the failure; rollback ran)", rolled, target)
}
