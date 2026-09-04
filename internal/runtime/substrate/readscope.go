package substrate

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// FSReadScope is the filesystem-backed implementation of ReadScope.
// It provides read-only access; it contains no Write/Mutate/Commit methods.
type FSReadScope struct {
	root string
}

// NewFSReadScope creates a ReadScope bound to root.
func NewFSReadScope(root string) *FSReadScope {
	return &FSReadScope{root: filepath.Clean(root)}
}

// ReadFile reads the workspace-relative file.
func (r *FSReadScope) ReadFile(relPath string) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("substrate: nil ReadScope")
	}
	full := filepath.Join(r.root, filepath.Clean(relPath))
	return os.ReadFile(full)
}

// ReadTree lists all files under root (workspace-relative paths).
func (r *FSReadScope) ReadTree(dir string) ([]string, error) {
	if r == nil {
		return nil, fmt.Errorf("substrate: nil ReadScope")
	}
	base := filepath.Join(r.root, filepath.Clean(dir))
	var out []string
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(r.root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}

// Snapshot returns a deterministic digest of the workspace file set.
// It hashes each file's content; it does not mutate state.
func (r *FSReadScope) Snapshot() (string, error) {
	if r == nil {
		return "", fmt.Errorf("substrate: nil ReadScope")
	}
	files, err := r.ReadTree(".")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, f := range files {
		data, err := r.ReadFile(f)
		if err != nil {
			continue
		}
		h.Write([]byte(f))
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
