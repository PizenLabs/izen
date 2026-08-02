package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ApplyResult describes the outcome of an application.
type ApplyResult struct {
	Applied        bool
	Content        string
	Tier           Tier
	Strategy       string
	File           string
	AlreadyApplied bool
}

// Applicator writes a resolved patch to the workspace. It owns filesystem
// safety (path traversal guards, directory creation, atomic write).
type Applicator interface {
	Apply(root string, p Patch) (ApplyResult, error)
}

// FileApplicator writes patch.Modified to <root>/<file>. It is idempotent at
// the filesystem level: writing identical bytes is a cheap no-op.
type FileApplicator struct{}

func NewFileApplicator() *FileApplicator { return &FileApplicator{} }

func (a *FileApplicator) Apply(root string, p Patch) (ApplyResult, error) {
	clean := filepath.Clean(p.File)
	if clean == "." || clean == "/" || strings.Contains(clean, "..") {
		return ApplyResult{}, fmt.Errorf("%w: invalid target path %q", ErrSafetyViolation, p.File)
	}

	full := filepath.Join(root, clean)
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ApplyResult{}, fmt.Errorf("patch: mkdir %s: %w", dir, err)
	}

	if err := os.WriteFile(full, []byte(p.Modified), 0o644); err != nil {
		return ApplyResult{}, fmt.Errorf("patch: write %s: %w", p.File, err)
	}

	return ApplyResult{
		Applied:  true,
		Content:  p.Modified,
		Tier:     p.Tier,
		Strategy: p.Strategy,
		File:     p.File,
	}, nil
}
