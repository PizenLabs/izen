// Package capabilities provides concrete adapters for the domain capability
// ports (RFC v1.0 section 2). Each adapter satisfies one interface from
// internal/domain/ports over a real external system: the OS filesystem
// (FilePort), the shell (ShellPort), the git CLI (GitPort), and a file
// patcher (PatchPort).
//
// The domain layer never imports this package: it only sees the port
// interfaces. Adapters depend on the ports, never the reverse.
package capabilities

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/domain/ports"
	"github.com/PizenLabs/izen/internal/runtime/scope"
)

// compile-time assertions that the adapters satisfy the domain ports.
var (
	_ ports.FilePort  = (*OSFile)(nil)
	_ ports.ShellPort = (*ExecShell)(nil)
	_ ports.GitPort   = (*GitCLI)(nil)
	_ ports.PatchPort = (*PatchAdapter)(nil)
)

// OSFile implements ports.FilePort over the operating system filesystem using
// the os and filepath packages. Paths are resolved against an optional root
// directory so the adapter can be confined to a workspace.
//
// Execution-time confinement: every Read, Write, Exists, and Remove
// verifies its target at USE time against the workspace-root FD handle.
// A symlink swapped in after resolution that points outside the root
// fails closed with scope.ErrWorkspaceEscape before any I/O. Internal
// symlinks resolving within the root remain permitted. Raw string-path
// operations never proceed without FD-anchored verification when a root
// is configured.
type OSFile struct {
	root string
	// scopeRoot, when bound, anchors every operation to the open root FD
	// for the adapter lifetime. Otherwise a short-lived handle is opened
	// per call so verification still runs at use time.
	scopeRoot *scope.Root
}

// NewOSFile returns a FilePort adapter rooted at root. An empty root means
// paths are used as given (relative to the process working directory).
func NewOSFile(root string) *OSFile {
	return &OSFile{root: root}
}

// NewOSFileWithRoot returns a FilePort adapter anchored at an already-open
// workspace-root FD handle. The caller retains ownership of root.
func NewOSFileWithRoot(root *scope.Root) *OSFile {
	f := &OSFile{}
	if root != nil {
		f.scopeRoot = root
		f.root = root.RootPath()
	}
	return f
}

// BindRoot anchors the adapter to an open workspace-root FD handle.
func (f *OSFile) BindRoot(root *scope.Root) {
	if f == nil {
		return
	}
	f.scopeRoot = root
	if root != nil {
		f.root = root.RootPath()
	}
}

// anchored verifies path at use time and returns the absolute path plus,
// for short-lived handles, a closer. An empty adapter root bypasses
// anchoring (legacy unconfined behavior).
func (f *OSFile) anchored(path string) (string, func(), error) {
	if f.root == "" && f.scopeRoot == nil {
		return path, nil, nil
	}
	if f.scopeRoot != nil {
		rel, err := toRel(f.root, path)
		if err != nil {
			return "", nil, err
		}
		if err := f.scopeRoot.Verify(rel); err != nil {
			return "", nil, err
		}
		return filepath.Join(f.root, rel), nil, nil
	}
	r, err := scope.Open(f.root)
	if err != nil {
		return "", nil, err
	}
	rel, rerr := toRel(f.root, path)
	if rerr != nil {
		_ = r.Close()
		return "", nil, rerr
	}
	if verr := r.Verify(rel); verr != nil {
		_ = r.Close()
		return "", nil, verr
	}
	return filepath.Join(f.root, rel), func() { _ = r.Close() }, nil
}

// toRel converts an adapter-level path to a root-relative slash path and
// enforces lexical containment. Absolute paths escaping the root fail
// with scope.ErrWorkspaceEscape.
func toRel(root, path string) (string, error) {
	candidate := path
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%w: path %q escapes workspace", scope.ErrWorkspaceEscape, path)
		}
		candidate = rel
	}
	clean := filepath.Clean(filepath.FromSlash(candidate))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path %q escapes workspace", scope.ErrWorkspaceEscape, path)
	}
	if clean == "." {
		return "", fmt.Errorf("%w: empty path", scope.ErrWorkspaceEscape)
	}
	return clean, nil
}

// anchoredDir verifies a directory path at use time. "." / empty maps to
// the root itself (always inside by construction).
func (f *OSFile) anchoredDir(dir string) (string, func(), error) {
	if f.root == "" && f.scopeRoot == nil {
		if dir == "" {
			return ".", nil, nil
		}
		return dir, nil, nil
	}
	if dir == "" || dir == "." {
		if f.scopeRoot != nil {
			return f.root, nil, nil
		}
		r, err := scope.Open(f.root)
		if err != nil {
			return "", nil, err
		}
		return f.root, func() { _ = r.Close() }, nil
	}
	return f.anchored(dir)
}

// Read returns the full content of the file at path.
func (f *OSFile) Read(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	full, closeRoot, err := f.anchored(path)
	if err != nil {
		return "", err
	}
	if closeRoot != nil {
		defer closeRoot()
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("osfile: read %s: %w", path, err)
	}
	return string(data), nil
}

// Write persists content to the file at path, creating parent directories as
// needed. The target is verified at use time against the root FD; an
// escaping symlink fails with scope.ErrWorkspaceEscape before any write.
func (f *OSFile) Write(ctx context.Context, path string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, closeRoot, err := f.anchored(path)
	if err != nil {
		return err
	}
	if closeRoot != nil {
		defer closeRoot()
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("osfile: mkdir for %s: %w", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return fmt.Errorf("osfile: write %s: %w", path, err)
	}
	return nil
}

// List returns the directory entry names directly under dir, sorted by the
// filesystem reader.
func (f *OSFile) List(ctx context.Context, dir string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, closeRoot, err := f.anchoredDir(dir)
	if err != nil {
		return nil, err
	}
	if closeRoot != nil {
		defer closeRoot()
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, fmt.Errorf("osfile: list %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// Exists reports whether the file at path exists.
func (f *OSFile) Exists(ctx context.Context, path string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	full, closeRoot, err := f.anchored(path)
	if err != nil {
		return false
	}
	if closeRoot != nil {
		defer closeRoot()
	}
	_, err = os.Stat(full)
	return err == nil
}

// Remove deletes the file at path.
func (f *OSFile) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, closeRoot, err := f.anchored(path)
	if err != nil {
		return err
	}
	if closeRoot != nil {
		defer closeRoot()
	}
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("osfile: remove %s: %w", path, err)
	}
	return nil
}
