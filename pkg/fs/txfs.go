// Package txfs implements a Transactional File System (TxFS) over a workspace
// root. A transaction stages file writes and removes in an isolated in-memory
// map — the live workspace is never touched until Commit — then applies them
// atomically via write-to-temp-and-rename. Rollback wipes the staged changes
// and restores every original file, so a partial execution failure or a
// rejected validation leaves the workspace exactly as it was.
//
// Commit follows a two-phase protocol: phase one prepares every staged write
// as an fsynced temp file in the target directory (never touching the live
// target), phase two renames the temp files into place. A failure in either
// phase keeps the transaction active so the caller can Rollback, which
// restores the captured originals and prunes created directories.
//
// The package is deliberately free of any AI, LLM or prompt dependencies.
package txfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// action discriminates the kind of a staged operation.
type action uint8

const (
	actionWrite action = iota
	actionRemove
)

// defaultFileMode is applied to staged writes that carry a zero permission
// set, mirroring the workspace-wide file default.
const defaultFileMode fs.FileMode = 0o644

// Errors returned by TxFS methods.
var (
	// ErrNilTxFS is returned when a method is invoked on a nil receiver.
	ErrNilTxFS = errors.New("txfs: nil TxFS")
	// ErrActiveTransaction is returned by Begin when a transaction is already
	// open, preventing accidental loss of staged state.
	ErrActiveTransaction = errors.New("txfs: transaction already active")
	// ErrNoActiveTransaction is returned by staged operations, Commit and
	// Rollback when no transaction has been begun.
	ErrNoActiveTransaction = errors.New("txfs: no active transaction")
	// ErrPathEscapesRoot is returned when a path is absolute or resolves
	// outside the workspace root.
	ErrPathEscapesRoot = errors.New("txfs: path escapes workspace root")
)

// fileOrigin is the pre-transaction state of a target file, captured the first
// time the path is staged so Rollback can restore it exactly.
type fileOrigin struct {
	content []byte
	perm    fs.FileMode
	exists  bool
}

// stagedFile is one path's staged state within the transaction.
type stagedFile struct {
	action  action
	content []byte
	perm    fs.FileMode
	orig    *fileOrigin
}

// TxFS wraps a workspace root and stages file operations inside an explicit
// two-phase transaction. A TxFS is not safe to share across workspace roots;
// it is safe for concurrent use through its internal RWMutex.
type TxFS struct {
	mu     sync.RWMutex
	root   string
	active bool
	staged map[string]*stagedFile
	// createdDirs records directories created during Commit so Rollback can
	// remove them (when empty) and restore a pristine tree.
	createdDirs map[string]struct{}
}

// NewTxFS returns a TxFS bound to root. The root does not need to exist yet;
// it is created on Commit.
func NewTxFS(root string) *TxFS {
	return &TxFS{
		root:        root,
		staged:      make(map[string]*stagedFile),
		createdDirs: make(map[string]struct{}),
	}
}

// Root returns the workspace root the transaction is bound to.
func (t *TxFS) Root() string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.root
}

// Active reports whether a transaction is currently open.
func (t *TxFS) Active() bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active
}

// Begin opens a transaction over the workspace root. All staged operations
// remain in memory until Commit; nothing touches the live workspace before
// then. Begin fails when a transaction is already active.
func (t *TxFS) Begin() error {
	if t == nil {
		return ErrNilTxFS
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active {
		return ErrActiveTransaction
	}
	t.active = true
	t.staged = make(map[string]*stagedFile)
	t.createdDirs = make(map[string]struct{})
	return nil
}

// WriteFile stages a write of content to the workspace-relative path. The live
// file is not touched until Commit; the pre-existing content (when present) is
// captured so Rollback can restore it. A zero perm falls back to 0o644.
func (t *TxFS) WriteFile(path string, content []byte, perm fs.FileMode) error {
	if t == nil {
		return ErrNilTxFS
	}
	if _, err := t.safeJoin(path); err != nil {
		return err
	}
	if perm == 0 {
		perm = defaultFileMode
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return ErrNoActiveTransaction
	}
	entry, ok := t.staged[path]
	if !ok {
		entry = &stagedFile{orig: captureOrigin(t.root, path)}
		t.staged[path] = entry
	}
	entry.action = actionWrite
	entry.content = append([]byte(nil), content...)
	entry.perm = perm
	return nil
}

// RemoveFile stages the removal of the workspace-relative path. The live file
// is not touched until Commit; its content is captured so Rollback can restore
// it. Removing a path that does not exist stages a no-op removal.
func (t *TxFS) RemoveFile(path string) error {
	if t == nil {
		return ErrNilTxFS
	}
	if _, err := t.safeJoin(path); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return ErrNoActiveTransaction
	}
	entry, ok := t.staged[path]
	if !ok {
		entry = &stagedFile{orig: captureOrigin(t.root, path)}
		t.staged[path] = entry
	}
	entry.action = actionRemove
	return nil
}

// IsStagedDelete reports whether the staged entry for path is a deletion.
func (t *TxFS) IsStagedDelete(path string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if entry, ok := t.staged[path]; ok {
		return entry.action == actionRemove
	}
	return false
}

// StagedWriteContent returns the staged write content for path, if it is a
// pending write. The boolean reports whether a staged write exists.
func (t *TxFS) StagedWriteContent(path string) ([]byte, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if entry, ok := t.staged[path]; ok && entry.action == actionWrite {
		return append([]byte(nil), entry.content...), true
	}
	return nil, false
}

// Clear discards all staged operations without restoring files. It is the
// staging-buffer reset used when the Substrate authority takes ownership of
// the staged proposal — the TxFS never commits directly to disk.
func (t *TxFS) Clear() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = false
	t.staged = make(map[string]*stagedFile)
	t.createdDirs = make(map[string]struct{})
}

// ReadFile returns the staged content of a pending write, or the live content
// when the path is not staged.
func (t *TxFS) ReadFile(path string) ([]byte, error) {
	if t == nil {
		return nil, ErrNilTxFS
	}
	target, err := t.safeJoin(path)
	if err != nil {
		return nil, err
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if entry, ok := t.staged[path]; ok && entry.action == actionWrite {
		return append([]byte(nil), entry.content...), nil
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("txfs: read %q: %w", path, err)
	}
	return data, nil
}

// StagedCount returns the number of paths staged in the open transaction.
func (t *TxFS) StagedCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.staged)
}

// StagedPaths returns the staged paths, sorted for determinism.
func (t *TxFS) StagedPaths() []string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	paths := make([]string, 0, len(t.staged))
	for path := range t.staged {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// Commit applies every staged operation to the live workspace atomically.
//
// Phase one writes each staged write to an fsynced temp file in the target's
// directory and creates missing parent directories. Phase two renames the temp
// files into place and removes staged deletions. On any failure the
// transaction stays active and the caller must Rollback, which restores the
// captured originals — a partial phase-two commit is fully undone.
func (t *TxFS) Commit() error {
	if t == nil {
		return ErrNilTxFS
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return ErrNoActiveTransaction
	}

	type preparedWrite struct {
		target string
		tmp    string
	}
	writes := make([]preparedWrite, 0, len(t.staged))
	removals := make([]string, 0, len(t.staged))

	for _, path := range sortedStagedPaths(t.staged) {
		entry := t.staged[path]
		target, err := t.safeJoin(path)
		if err != nil {
			return err
		}
		switch entry.action {
		case actionWrite:
			if err := t.ensureDirs(filepath.Dir(target)); err != nil {
				return err
			}
			tmp, err := prepareTemp(target, entry.content, entry.perm)
			if err != nil {
				return err
			}
			writes = append(writes, preparedWrite{target: target, tmp: tmp})
		case actionRemove:
			removals = append(removals, target)
		}
	}

	// Phase two: atomic renames. A failure here leaves the transaction active
	// so Rollback restores the files already renamed.
	for _, w := range writes {
		if err := os.Rename(w.tmp, w.target); err != nil {
			return fmt.Errorf("txfs: commit write %q: %w", w.target, err)
		}
	}
	for _, target := range removals {
		if err := os.Remove(target); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("txfs: commit remove %q: %w", target, err)
			}
		}
	}
	t.fsyncDirs()
	t.reset()
	return nil
}

// Rollback discards every staged operation and restores the workspace to its
// pre-transaction state. Original files captured at staging time are written
// back byte-for-byte, files created by the transaction are removed, and
// directories created during Commit are pruned when empty. Rollback ends the
// transaction.
func (t *TxFS) Rollback() error {
	if t == nil {
		return ErrNilTxFS
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return ErrNoActiveTransaction
	}
	for _, path := range sortedStagedPaths(t.staged) {
		entry := t.staged[path]
		target, err := t.safeJoin(path)
		if err != nil {
			continue
		}
		if entry.orig != nil && entry.orig.exists {
			if err := restoreFile(target, entry.orig); err != nil {
				return fmt.Errorf("txfs: rollback restore %q: %w", path, err)
			}
			continue
		}
		// The path was not a regular file before the transaction. Remove only
		// what the transaction created; directories (which are never created
		// at a staged target) are preserved and pruned separately.
		info, err := os.Stat(target)
		if err != nil {
			// ENOENT, ENOTDIR or another stat failure means nothing the
			// transaction created is present; rollback stays best-effort.
			continue
		}
		if info.IsDir() {
			continue
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("txfs: rollback remove %q: %w", path, err)
		}
	}
	removeEmptyDirs(t.createdDirs)
	t.reset()
	return nil
}

// reset clears the transaction state. Caller must hold the write lock.
func (t *TxFS) reset() {
	t.active = false
	t.staged = make(map[string]*stagedFile)
	t.createdDirs = make(map[string]struct{})
}

// safeJoin resolves a workspace-relative path under the root, rejecting
// absolute paths and any traversal that escapes the root.
func (t *TxFS) safeJoin(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("txfs: empty path")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %q", ErrPathEscapesRoot, path)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscapesRoot, path)
	}
	return filepath.Join(t.root, clean), nil
}

// captureOrigin snapshots the live target file, if it exists.
func captureOrigin(root, path string) *fileOrigin {
	target := filepath.Join(root, filepath.Clean(path))
	data, err := os.ReadFile(target)
	if err != nil {
		return &fileOrigin{exists: false}
	}
	perm := defaultFileMode
	if info, err := os.Stat(target); err == nil {
		perm = info.Mode().Perm()
	}
	return &fileOrigin{content: data, perm: perm, exists: true}
}

// restoreFile rewrites target to the captured origin, recreating the file when
// it was removed or never created, and reapplies the permission bits.
func restoreFile(target string, orig *fileOrigin) error {
	if err := os.WriteFile(target, orig.content, orig.perm); err != nil {
		return err
	}
	return os.Chmod(target, orig.perm)
}

// ensureDirs creates every missing directory along dir — including the
// workspace root itself — recording each one it created so Rollback can prune
// them. It fails when a path component exists but is not a directory.
func (t *TxFS) ensureDirs(dir string) error {
	rel, err := filepath.Rel(t.root, dir)
	if err != nil {
		return err
	}
	if err := t.ensureDir(filepath.Clean(t.root)); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := filepath.Clean(t.root)
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		if comp == "" || comp == "." {
			continue
		}
		cur = filepath.Join(cur, comp)
		if err := t.ensureDir(cur); err != nil {
			return err
		}
	}
	return nil
}

// ensureDir creates dir when missing, recording it for rollback pruning, and
// rejects an existing non-directory at the path.
func (t *TxFS) ensureDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("txfs: %q exists and is not a directory", dir)
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("txfs: create directory %q: %w", dir, err)
		}
		t.createdDirs[dir] = struct{}{}
		return nil
	default:
		return fmt.Errorf("txfs: stat %q: %w", dir, err)
	}
}

// prepareTemp writes content to a same-directory temp file with the target
// permission bits, fsyncs it, and returns the temp path ready for rename.
func prepareTemp(target string, content []byte, perm fs.FileMode) (string, error) {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, "."+filepath.Base(target)+".txfs-*")
	if err != nil {
		return "", fmt.Errorf("txfs: create temp for %q: %w", target, err)
	}
	tmp := f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if _, err := f.Write(content); err != nil {
		cleanup()
		return "", fmt.Errorf("txfs: write temp %q: %w", tmp, err)
	}
	if err := f.Chmod(perm); err != nil {
		cleanup()
		return "", fmt.Errorf("txfs: chmod temp %q: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("txfs: sync temp %q: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("txfs: close temp %q: %w", tmp, err)
	}
	return tmp, nil
}

// fsyncDirs best-effort flushes the parent directories of committed writes to
// disk so a crash cannot leave renames unwitnessed. Failures are intentionally
// ignored: durability is best-effort, correctness is guaranteed by rollback.
func (t *TxFS) fsyncDirs() {
	seen := make(map[string]struct{})
	for dir := range t.createdDirs {
		t.fsyncDir(dir, seen)
	}
}

// fsyncDir opens dir and fsyncs it once per unique path.
func (t *TxFS) fsyncDir(dir string, seen map[string]struct{}) {
	if _, ok := seen[dir]; ok {
		return
	}
	seen[dir] = struct{}{}
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}

// removeEmptyDirs removes the recorded created directories that are now empty,
// deepest first, so Rollback leaves no scaffolding behind.
func removeEmptyDirs(dirs map[string]struct{}) {
	paths := make([]string, 0, len(dirs))
	for dir := range dirs {
		paths = append(paths, dir)
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, dir := range paths {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Best effort: a directory that gained unrelated content is kept.
			continue
		}
	}
}

// sortedStagedPaths returns the staged map keys in ascending order.
func sortedStagedPaths(staged map[string]*stagedFile) []string {
	paths := make([]string, 0, len(staged))
	for path := range staged {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
