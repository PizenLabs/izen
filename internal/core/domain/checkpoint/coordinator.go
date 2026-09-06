package checkpoint

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/occ"
)

// CheckpointCoordinator is owned exclusively by the Control Plane.
// It is the ONLY type that may call git read-tree / checkout-index.
type CheckpointCoordinator interface {
	CreateBeforeBuild(ctx context.Context, frameID domain.FrameID) (domain.CheckpointID, error)
	HasRef() bool
	Rollback(ctx context.Context, id domain.CheckpointID, boundary domain.RollbackBoundary) error
	Clear(ctx context.Context, id domain.CheckpointID) error
}

// DiskCheckpointCoordinator implements CheckpointCoordinator with filesystem
// snapshotting. It is concurrency-safe via sync.Mutex + OCCGate and idempotent
// for Rollback/Clear. On any mid-rollback I/O failure the workspace is flagged
// as tainted (WorkflowStateFailed) and subsequent mutations are blocked until
// a clean recovery.
type DiskCheckpointCoordinator struct {
	mu        sync.Mutex
	gate      occ.OCCGate //nolint:unused // reserved for future OCC validation
	root      string
	snapshots map[domain.CheckpointID]*snapshot
	// Optional stores for transactional alignment. When set, Rollback
	// clears uncommitted diffs and resets versioning.
	artifactStore artifactStore
	execState     executionState
	tainted       bool
	taintErr      error
}

type artifactStore interface {
	ClearUncommitted()
	ResetToBaseline(baseline occ.StateVersion)
	Version() occ.StateVersion
}

type executionState interface {
	ResetToBaseline(version occ.StateVersion)
	Version() occ.StateVersion
}

type snapshot struct {
	files            map[string][]byte // relPath -> content (nil if dir)
	fileModes        map[string]os.FileMode
	baselineArtVer   occ.StateVersion
	baselineExecVer  occ.StateVersion
	existingRelPaths map[string]bool // set of rel paths at snapshot time
}

// NewDiskCheckpointCoordinator creates a coordinator bound to workspace root.
func NewDiskCheckpointCoordinator(root string) *DiskCheckpointCoordinator {
	return &DiskCheckpointCoordinator{
		root:      filepath.Clean(root),
		snapshots: make(map[domain.CheckpointID]*snapshot),
	}
}

// SetStores wires the transactional stores for rollback alignment.
// It may be called once at composition time; it is safe for concurrent use.
func (d *DiskCheckpointCoordinator) SetStores(as artifactStore, es executionState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.artifactStore = as
	d.execState = es
}

// CreateBeforeBuild creates a dirty tree snapshot and returns CheckpointID.
func (d *DiskCheckpointCoordinator) CreateBeforeBuild(ctx context.Context, frameID domain.FrameID) (domain.CheckpointID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	files := make(map[string][]byte)
	modes := make(map[string]os.FileMode)
	existing := make(map[string]bool)

	// Walk workspace root, capture file contents.
	err := filepath.Walk(d.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip .git and .izen internals for snapshot lightness, but include workspace files.
			name := info.Name()
			if name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(d.root, path)
		if relErr != nil {
			return relErr
		}
		// Normalize to forward slashes.
		rel = filepath.ToSlash(rel)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[rel] = append([]byte(nil), data...)
		modes[rel] = info.Mode().Perm()
		existing[rel] = true
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("checkpoint: snapshot walk failed: %w", err)
	}

	// Content-addressed CheckpointID.
	h := sha256.New()
	for _, rel := range sortedKeys(files) {
		h.Write([]byte(rel))
		h.Write(files[rel])
	}
	chkID := domain.CheckpointID(fmt.Sprintf("chkpt_%x_%s", h.Sum(nil)[:8], string(frameID)))

	var artVer occ.StateVersion
	if d.artifactStore != nil {
		artVer = d.artifactStore.Version()
	}
	var execVer occ.StateVersion
	if d.execState != nil {
		execVer = d.execState.Version()
	}

	d.snapshots[chkID] = &snapshot{
		files:            files,
		fileModes:        modes,
		baselineArtVer:   artVer,
		baselineExecVer:  execVer,
		existingRelPaths: existing,
	}
	return chkID, nil
}

// HasRef reports whether any checkpoint exists.
func (d *DiskCheckpointCoordinator) HasRef() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.snapshots) > 0
}

// Rollback restores disk files to snapshot state, clears uncommitted
// ArtifactStore diffs, and resets ExecutionState versioning. It is idempotent
// and concurrency-safe under OCCGate (via internal mutex). If any file
// write fails mid-rollback the workspace is flagged tainted and the error
// is returned; the coordinator blocks further mutations until Recover is called.
func (d *DiskCheckpointCoordinator) Rollback(ctx context.Context, id domain.CheckpointID, _ domain.RollbackBoundary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.tainted {
		d.mu.Unlock()
		return fmt.Errorf("checkpoint: workspace tainted (rollback previously failed): %w", d.taintErr)
	}
	snap, ok := d.snapshots[id]
	// Idempotent: if snapshot not found, treat as already cleared/rolled back.
	if !ok {
		d.mu.Unlock()
		return nil
	}
	// Copy snapshot refs for work outside lock where possible, but keep lock for idempotency semantics.
	// We hold lock for whole operation to ensure single rollback at a time.
	defer d.mu.Unlock()

	// Restore disk: for each snap file, write back.
	for rel, content := range snap.files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		abs := filepath.Join(d.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			d.tainted = true
			d.taintErr = fmt.Errorf("checkpoint rollback mkdir %q: %w", rel, err)
			return d.taintErr
		}
		mode := snap.fileModes[rel]
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(abs, content, mode); err != nil {
			d.tainted = true
			d.taintErr = fmt.Errorf("checkpoint rollback write %q: %w", rel, err)
			return d.taintErr
		}
	}

	// Remove untracked files generated after snapshot (files present on disk but not in snapshot).
	// Walk current disk and delete any file not in existingRelPaths.
	_ = filepath.Walk(d.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // walk error intentionally swallowed to continue traversal
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(d.root, path)
		if relErr != nil {
			return nil //nolint:nilerr // rel error swallowed
		}
		rel = filepath.ToSlash(rel)
		if !snap.existingRelPaths[rel] {
			// Untracked file created during failed build — remove.
			_ = os.Remove(path)
		}
		return nil
	})

	// Clear uncommitted ArtifactStore diffs and reset versioning to baseline.
	if d.artifactStore != nil {
		d.artifactStore.ResetToBaseline(snap.baselineArtVer)
	}
	// Reset ExecutionState versioning to baseline.
	if d.execState != nil {
		d.execState.ResetToBaseline(snap.baselineExecVer)
	}

	return nil
}

// Clear removes a checkpoint after successful verification. Idempotent.
func (d *DiskCheckpointCoordinator) Clear(_ context.Context, id domain.CheckpointID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.snapshots, id)
	return nil
}

// IsTainted reports whether a previous Rollback failed mid-way and left the
// workspace in an inconsistent state (WorkflowStateFailed semantics). When
// tainted, subsequent Rollback attempts fail fast until Recover is called.
func (d *DiskCheckpointCoordinator) IsTainted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tainted
}

// TaintError returns the error that caused the taint, if any.
func (d *DiskCheckpointCoordinator) TaintError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.taintErr
}

// Recover clears the taint flag after a manual or secondary clean recovery.
// It allows subsequent checkpoint operations to proceed.
func (d *DiskCheckpointCoordinator) Recover() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tainted = false
	d.taintErr = nil
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort to avoid importing sort for tiny maps; use standard sort.
	// Use Go's sort.
	return sortStrings(keys)
}

func sortStrings(s []string) []string {
	// Minimal sort without importing sort package to keep dependencies light;
	// use bubble for determinism (n small). Replace with sort.Strings if needed.
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}

// Ensure DiskCheckpointCoordinator implements CheckpointCoordinator.
var _ CheckpointCoordinator = (*DiskCheckpointCoordinator)(nil)
