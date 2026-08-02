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

	"github.com/PizenLabs/izen/internal/domain/ports"
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
type OSFile struct {
	root string
}

// NewOSFile returns a FilePort adapter rooted at root. An empty root means
// paths are used as given (relative to the process working directory).
func NewOSFile(root string) *OSFile {
	return &OSFile{root: root}
}

// resolve joins path under the adapter root and cleans it.
func (f *OSFile) resolve(path string) string {
	if f.root == "" {
		return path
	}
	return filepath.Join(f.root, path)
}

// Read returns the full content of the file at path.
func (f *OSFile) Read(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(f.resolve(path))
	if err != nil {
		return "", fmt.Errorf("osfile: read %s: %w", path, err)
	}
	return string(data), nil
}

// Write persists content to the file at path, creating parent directories as
// needed.
func (f *OSFile) Write(ctx context.Context, path string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full := f.resolve(path)
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
	entries, err := os.ReadDir(f.resolve(dir))
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
	_, err := os.Stat(f.resolve(path))
	return err == nil
}
