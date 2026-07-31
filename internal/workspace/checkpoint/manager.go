package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/contract"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/workspace/snapshot"
)

// ErrPermissionDenied is returned when checkpoint creation is attempted without
// the CREATE_CHECKPOINT (contract.PermCheckpoint) permission level.
var ErrPermissionDenied = errors.New("checkpoint: CREATE_CHECKPOINT permission not granted")

// ErrCheckpointNotFound is returned when a Rollback/Commit references an
// unknown or already-consumed checkpoint ID.
var ErrCheckpointNotFound = errors.New("checkpoint: not found")

// ErrNoOriginalContent is returned when a rollback cannot locate the buffered
// original content for a target file (neither in memory nor on disk).
var ErrNoOriginalContent = errors.New("checkpoint: original content unavailable")

// Manager owns the lifecycle of every checkpoint for a single workspace root:
// capture, atomic rollback, and commit. All operations are thread-safe; a
// checkpoint is consumed by the first Rollback or Commit that references it.
type Manager struct {
	root string

	mu          sync.RWMutex
	checkpoints map[CheckpointID]*Checkpoint

	// bus, when injected, receives the PatchAttempted / ExecutionFailed events
	// emitted during rollback. Nil disables emission (headless/CLI fallbacks).
	bus *events.Bus
	// snapCache, when injected, is refreshed after a successful rollback so the
	// WorkspaceSnapshot cache re-aligns with disk state.
	snapCache *snapshot.SnapshotCache
	// perms gates checkpoint creation on the CREATE_CHECKPOINT permission
	// level. When nil, creation is allowed (backwards compatible).
	perms []contract.PermissionLevel
}

// NewManager returns a checkpoint manager protecting the workspace rooted at
// root. All file paths passed to CreateCheckpoint are resolved relative to it.
func NewManager(root string) *Manager {
	return &Manager{
		root:        root,
		checkpoints: make(map[CheckpointID]*Checkpoint),
	}
}

// WithEventBus injects the event bus rollback events are published to. May be
// nil to disable emission.
func (m *Manager) WithEventBus(bus *events.Bus) *Manager {
	m.bus = bus
	return m
}

// WithSnapshotCache injects the workspace snapshot cache that is refreshed
// after a rollback so cached workspace state re-aligns with disk.
func (m *Manager) WithSnapshotCache(sc *snapshot.SnapshotCache) *Manager {
	m.snapCache = sc
	return m
}

// WithPermissions installs the allowed permission levels that gate checkpoint
// creation. When nil or empty, checkpoint creation is always permitted. When
// set, contract.PermCheckpoint (CREATE_CHECKPOINT) must be present.
func (m *Manager) WithPermissions(perms []contract.PermissionLevel) *Manager {
	m.perms = perms
	return m
}

// Root returns the workspace root the manager protects.
func (m *Manager) Root() string { return m.root }

// CreateCheckpoint buffers the byte-exact original state of every target file
// before any mutation is applied. Target paths that do not exist are recorded
// for deletion upon rollback. Creation requires the CREATE_CHECKPOINT
// permission when permissions are configured. The returned checkpoint remains
// open until Rollback or Commit consumes it.
func (m *Manager) CreateCheckpoint(stage string, targetFiles []string) (*Checkpoint, error) {
	if !m.permitted() {
		return nil, ErrPermissionDenied
	}

	cp := &Checkpoint{
		ID:            NewCheckpointID(),
		CreatedAt:     time.Now(),
		Stage:         stage,
		TargetFiles:   append([]string(nil), targetFiles...),
		OriginalBlobs: make(map[string][]byte, len(targetFiles)),
		MissingFiles:  make(map[string]bool),
	}

	for _, file := range targetFiles {
		if file == "" {
			continue
		}
		absPath := filepath.Join(m.root, file)
		data, err := os.ReadFile(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				cp.MissingFiles[file] = true
				continue
			}
			return nil, fmt.Errorf("checkpoint %s: buffer %s: %w", cp.ID, file, err)
		}
		cp.OriginalBlobs[file] = data
	}

	cp.persistShadow(m)

	m.mu.Lock()
	m.checkpoints[cp.ID] = cp
	m.mu.Unlock()

	return cp, nil
}

// Rollback atomically restores every target file to its captured original
// state: modified files are rewritten with their byte-exact pre-mutation
// content and files that did not exist at capture time are deleted. After a
// successful restore the injected snapshot cache is refreshed so workspace
// state re-aligns, and the checkpoint is consumed.
//
// When an event bus is injected, a PatchAttempted event is emitted per
// restored/deleted file and an ExecutionFailed event is emitted if the restore
// itself fails.
func (m *Manager) Rollback(id CheckpointID) error {
	cp, err := m.consume(id)
	if err != nil {
		return err
	}

	var errs []error
	for _, file := range cp.TargetFiles {
		absPath := filepath.Join(m.root, file)
		if cp.MissingFiles[file] {
			if rmErr := os.Remove(absPath); rmErr != nil && !os.IsNotExist(rmErr) {
				errs = append(errs, fmt.Errorf("rollback delete %s: %w", file, rmErr))
				continue
			}
			m.emit(events.NewPatchAttempted(file, "CHECKPOINT_ROLLBACK_DELETE", 1))
			continue
		}

		blob := cp.OriginalBlobs[file]
		if blob == nil {
			blob = cp.readShadow(file)
		}
		if blob == nil {
			errs = append(errs, fmt.Errorf("%w: %s", ErrNoOriginalContent, file))
			continue
		}

		if err := writeFileAtomically(absPath, blob); err != nil {
			errs = append(errs, fmt.Errorf("rollback restore %s: %w", file, err))
			continue
		}
		m.emit(events.NewPatchAttempted(file, "CHECKPOINT_ROLLBACK_RESTORE", 1))
	}

	_ = os.RemoveAll(cp.blobDir)

	if len(errs) > 0 {
		m.emit(events.NewExecutionFailed(events.FailurePermanent, errors.Join(errs...), "checkpoint.rollback"))
		return errors.Join(errs...)
	}

	if m.snapCache != nil {
		if _, err := m.snapCache.Refresh(m.root); err != nil {
			m.emit(events.NewExecutionFailed(events.FailureTransient,
				fmt.Errorf("rollback snapshot refresh: %w", err), "checkpoint.rollback"))
			return fmt.Errorf("rollback snapshot refresh: %w", err)
		}
	}
	return nil
}

// RollbackAll rolls back every open checkpoint, returning a joined error when
// any individual rollback fails. It is used to atomically undo a batch of
// mutations after compilation or test verification fails.
func (m *Manager) RollbackAll() error {
	return m.forEachOpen(func(id CheckpointID) error { return m.Rollback(id) })
}

// Commit finalizes a checkpoint after successful build/test verification:
// buffered blobs are discarded from memory and the file-backed shadow copy is
// removed. The checkpoint is consumed and no longer available for rollback.
func (m *Manager) Commit(id CheckpointID) error {
	cp, err := m.consume(id)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(cp.blobDir)
	return nil
}

// CommitAll commits every open checkpoint, returning a joined error when any
// individual commit fails.
func (m *Manager) CommitAll() error {
	return m.forEachOpen(func(id CheckpointID) error { return m.Commit(id) })
}

// Get returns the open checkpoint with the given ID, or nil when it is unknown
// or already consumed.
func (m *Manager) Get(id CheckpointID) *Checkpoint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.checkpoints[id]
}

// List returns a snapshot of all open checkpoints, ordered by creation time.
func (m *Manager) List() []*Checkpoint {
	m.mu.RLock()
	cps := make([]*Checkpoint, 0, len(m.checkpoints))
	for _, cp := range m.checkpoints {
		cps = append(cps, cp)
	}
	m.mu.RUnlock()

	sort.Slice(cps, func(i, j int) bool { return cps[i].CreatedAt.Before(cps[j].CreatedAt) })
	return cps
}

// Open reports the number of open (not yet committed or rolled back)
// checkpoints.
func (m *Manager) Open() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.checkpoints)
}

// consume removes a checkpoint from the open set and returns it. It is the
// single gate that guarantees a checkpoint can be resolved at most once.
func (m *Manager) consume(id CheckpointID) (*Checkpoint, error) {
	m.mu.Lock()
	cp, ok := m.checkpoints[id]
	if ok {
		delete(m.checkpoints, id)
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
	}
	return cp, nil
}

// forEachOpen snapshots the open IDs and resolves each in order, collecting
// failures. Snapshotting outside the lock keeps concurrent lifecycle calls safe
// (an ID resolved by another goroutine is skipped by consume).
func (m *Manager) forEachOpen(fn func(CheckpointID) error) error {
	m.mu.RLock()
	ids := make([]CheckpointID, 0, len(m.checkpoints))
	for id := range m.checkpoints {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	var errs []error
	for _, id := range ids {
		if err := fn(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// permitted reports whether checkpoint creation is allowed under the installed
// permission contract.
func (m *Manager) permitted() bool {
	if m.perms == nil {
		return true
	}
	return (contract.StageContract{AllowedPerms: m.perms}).Permitted(contract.PermCheckpoint)
}

// emit publishes a domain event; a strict no-op when no bus is wired.
func (m *Manager) emit(ev events.DomainEvent) {
	if m.bus != nil {
		m.bus.Publish(ev)
	}
}

// persistShadow writes the captured blobs under
// .izen/checkpoints/workspace/<id>/ as a file-backed copy for crash safety. It
// is best-effort: the in-memory blobs remain authoritative for rollback. The
// .izen directory is excluded from workspace snapshots, so shadow copies never
// pollute the workspace tree.
func (cp *Checkpoint) persistShadow(m *Manager) {
	cp.blobDir = filepath.Join(m.root, ".izen", "checkpoints", "workspace", string(cp.ID))
	if len(cp.OriginalBlobs) == 0 {
		return
	}
	if err := os.MkdirAll(cp.blobDir, 0755); err != nil {
		return
	}
	cp.blobPaths = make(map[string]string, len(cp.OriginalBlobs))
	for file, blob := range cp.OriginalBlobs {
		blobPath := filepath.Join(cp.blobDir, blobFileName(file))
		cp.blobPaths[file] = blobPath
		_ = os.WriteFile(blobPath, blob, 0644)
	}
}

// readShadow loads the persisted shadow copy of a target file, used as a
// fallback when the in-memory buffer is unavailable.
func (cp *Checkpoint) readShadow(file string) []byte {
	if cp.blobPaths == nil {
		return nil
	}
	blobPath, ok := cp.blobPaths[file]
	if !ok {
		return nil
	}
	data, err := os.ReadFile(blobPath)
	if err != nil {
		return nil
	}
	return data
}

// blobFileName derives a stable, path-safe file name for a target path's
// shadow copy, avoiding collisions between nested paths of any depth.
func blobFileName(relPath string) string {
	sum := sha256.Sum256([]byte(relPath))
	return hex.EncodeToString(sum[:])[:16]
}

// writeFileAtomically writes data to path so that concurrent readers never
// observe a truncated or partial file: the payload is written to a temporary
// sibling and then atomically renamed over the target. This guarantees a
// concurrent CreateCheckpoint captures either the old or the new file state,
// never an in-between one.
func writeFileAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".izen-chk-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
