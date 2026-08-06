// Package file implements the file Resource adapter. A FileResource wraps one
// file inside a workspace tree and exposes deterministic, non-destructive
// snapshot/restore of its raw byte content and permission bits.
package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/pkg/resource"
)

// defaultMode is applied to a newly created file when no mode is configured.
const defaultMode fs.FileMode = 0o644

// FileResource wraps a single relative path inside a workspace root. It is the
// concrete resource.Resource implementation for file targets.
type FileResource struct {
	workspaceRoot string
	relPath       string
	mode          fs.FileMode
}

// Compile-time assertion that FileResource satisfies resource.Resource.
var _ resource.Resource = (*FileResource)(nil)

// NewFileResource returns a FileResource targeting relPath inside
// workspaceRoot. The path must be relative and resolve within the workspace
// root; a zero mode falls back to 0o644.
func NewFileResource(workspaceRoot, relPath string, mode fs.FileMode) (*FileResource, error) {
	if workspaceRoot == "" || relPath == "" {
		return nil, errors.New("file: workspace root and relative path are required")
	}
	if filepath.IsAbs(relPath) {
		return nil, fmt.Errorf("file: relative path must be relative, got %q", relPath)
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("file: resolve workspace root: %w", err)
	}
	abs, err := filepath.Abs(filepath.Join(root, relPath))
	if err != nil {
		return nil, fmt.Errorf("file: resolve target: %w", err)
	}
	if !within(root, abs) {
		return nil, fmt.Errorf("file: target %q escapes workspace root %q", relPath, root)
	}
	if mode == 0 {
		mode = defaultMode
	}
	return &FileResource{workspaceRoot: root, relPath: relPath, mode: mode}, nil
}

// within reports whether target is root itself or a descendant of root.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// ID returns the canonical absolute path of the wrapped file.
func (f *FileResource) ID() string {
	return filepath.Join(f.workspaceRoot, f.relPath)
}

// Kind returns resource.KindFile.
func (f *FileResource) Kind() resource.ResourceKind { return resource.KindFile }

// targetPath returns the absolute path the resource operates on.
func (f *FileResource) targetPath() string {
	return filepath.Join(f.workspaceRoot, f.relPath)
}

// ValidateState checks that the target file exists and can be both read and
// written.
func (f *FileResource) ValidateState(ctx context.Context) error {
	_ = ctx
	path := f.targetPath()
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("file: target %q does not exist", path)
		}
		return fmt.Errorf("file: stat target %q: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("file: target %q is a directory, expected a file", path)
	}
	if err := checkAccess(path); err != nil {
		return err
	}
	return nil
}

// checkAccess verifies the file at path is openable for both reading and
// writing without mutating its content.
func checkAccess(path string) error {
	rf, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("file: target %q is not readable: %w", path, err)
	}
	_ = rf.Close()
	wf, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("file: target %q is not writable: %w", path, err)
	}
	_ = wf.Close()
	return nil
}

// fileSnapshot is the concrete Snapshot implementation for FileResource.
type fileSnapshot struct {
	hash string
	data fileSnapshotData
}

// fileSnapshotData is the typed payload of a FileResource snapshot.
type fileSnapshotData struct {
	// Path is the absolute path captured at snapshot time.
	Path string
	// Content is the raw byte content of the file.
	Content []byte
	// Mode is the permission bits of the file at snapshot time.
	Mode fs.FileMode
}

// Hash returns the SHA-256 of the captured content.
func (s *fileSnapshot) Hash() string { return s.hash }

// Data returns the typed snapshot payload.
func (s *fileSnapshot) Data() any { return s.data }

// Snapshot captures the current byte content and permission bits of the target
// file together with a SHA-256 hash over that content. The capture is
// read-only and does not mutate the file.
func (f *FileResource) Snapshot(ctx context.Context) (resource.Snapshot, error) {
	if err := f.ValidateState(ctx); err != nil {
		return nil, err
	}
	path := f.targetPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("file: read snapshot of %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("file: stat snapshot of %q: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return &fileSnapshot{
		hash: hex.EncodeToString(sum[:]),
		data: fileSnapshotData{
			Path:    f.ID(),
			Content: data,
			Mode:    info.Mode().Perm(),
		},
	}, nil
}

// Restore rewrites the target file to the captured snapshot content and
// permission bits. Only snapshots produced by a FileResource are accepted; the
// target is recreated if it was deleted since the snapshot was taken.
func (f *FileResource) Restore(ctx context.Context, s resource.Snapshot) error {
	_ = ctx
	if s == nil {
		return errors.New("file: restore requires a non-nil snapshot")
	}
	snap, ok := s.(*fileSnapshot)
	if !ok {
		return fmt.Errorf("file: incompatible snapshot type %T", s)
	}
	path := f.targetPath()
	if err := os.WriteFile(path, snap.data.Content, snap.data.Mode); err != nil {
		return fmt.Errorf("file: restore content of %q: %w", path, err)
	}
	if err := os.Chmod(path, snap.data.Mode); err != nil {
		return fmt.Errorf("file: restore mode of %q: %w", path, err)
	}
	return nil
}

// Read returns the raw byte content of the target file.
func (f *FileResource) Read() ([]byte, error) {
	data, err := os.ReadFile(f.targetPath())
	if err != nil {
		return nil, fmt.Errorf("file: read %q: %w", f.targetPath(), err)
	}
	return data, nil
}

// Write replaces the raw byte content of the target file. A newly created file
// receives the resource's configured permissions.
func (f *FileResource) Write(data []byte) error {
	if err := os.WriteFile(f.targetPath(), data, f.mode); err != nil {
		return fmt.Errorf("file: write %q: %w", f.targetPath(), err)
	}
	return nil
}
