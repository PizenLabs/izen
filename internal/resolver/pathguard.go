package resolver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sentinel errors for PathGuard violations.
var (
	ErrPathTraversal     = errors.New("resolver: path traversal rejected")
	ErrOutsideWorkspace  = errors.New("resolver: path outside workspace")
	ErrInvalidPath       = errors.New("resolver: invalid path")
	ErrWorkspaceNotFound = errors.New("resolver: workspace root not found")
)

// PathGuard enforces lexical normalization, symlink policy, and workspace containment.
type PathGuard struct {
	root string // absolute, cleaned, symlink-evaluated workspace root
}

// NewPathGuard creates a guard bound to workspaceRoot.
// workspaceRoot is resolved to absolute and evaluated via EvalSymlinks.
func NewPathGuard(workspaceRoot string) (*PathGuard, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, fmt.Errorf("%w: empty workspace root", ErrWorkspaceNotFound)
	}
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolver: abs: %w", err)
	}
	abs = filepath.Clean(abs)
	// Ensure root exists.
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrWorkspaceNotFound, abs, err) //nolint:errorlint
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: not a directory: %s", ErrWorkspaceNotFound, abs)
	}
	// Evaluate symlinks on root itself.
	evaluated, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolver: eval root: %w", err)
	}
	evaluated = filepath.Clean(evaluated)
	return &PathGuard{root: evaluated}, nil
}

// Root returns the evaluated workspace root.
func (g *PathGuard) Root() string { return g.root }

// SanitizePath normalizes lexical paths, evaluates symlinks of nearest existing ancestor
// for non-existent paths, and asserts workspace containment boundary via filepath.Rel.
// Rejects relative escapes (..) and absolute paths outside root.
func (g *PathGuard) SanitizePath(rawPath string) (string, error) {
	if g == nil || g.root == "" {
		return "", fmt.Errorf("%w: nil guard", ErrInvalidPath)
	}
	if strings.TrimSpace(rawPath) == "" {
		return "", fmt.Errorf("%w: empty path", ErrPathTraversal)
	}
	cleaned := filepath.Clean(rawPath)

	// Reject paths that after cleaning are exactly ".." or start with "../"
	// This catches lexical escapes before join, but containment check is authoritative.
	// We still perform join+rel check for absolute escapes.

	var candidate string
	if filepath.IsAbs(cleaned) {
		candidate = cleaned
	} else {
		candidate = filepath.Join(g.root, cleaned)
	}
	candidate = filepath.Clean(candidate)

	resolved, err := g.resolveWithSymlinkPolicy(candidate)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(g.root, resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOutsideWorkspace, err) //nolint:errorlint
	}
	// filepath.Rel returns "." for same dir, "a/b" for inside, "../..." for outside
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes workspace %q", ErrOutsideWorkspace, rawPath, g.root)
	}
	// Additional lexical check: if original rawPath after clean contains ".." traversing above join,
	// Rel already catches it, but we also explicitly reject any absolute candidate that is not under root.
	return resolved, nil
}

// resolveWithSymlinkPolicy evaluates symlinks of nearest existing ancestor for non-existent paths.
// If candidate exists, EvalSymlinks on full path. Otherwise walk up to find existing ancestor,
// eval it, then re-append remainder suffix cleanly.
func (g *PathGuard) resolveWithSymlinkPolicy(candidate string) (string, error) {
	// Fast path: exists
	if _, err := os.Lstat(candidate); err == nil {
		ev, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", fmt.Errorf("%w: eval symlink %s: %v", ErrInvalidPath, candidate, err) //nolint:errorlint
		}
		return filepath.Clean(ev), nil
	}
	// Find nearest existing ancestor. Walk up.
	// We need to find the longest prefix that exists.
	curr := candidate
	for {
		parent := filepath.Dir(curr)
		// Stop at filesystem root or when curr == parent
		if curr == parent {
			// Reached "/" with no existing ancestor — use root evaluation fallback
			break
		}
		if _, err := os.Lstat(curr); err == nil {
			// Found existing ancestor
			ev, err := filepath.EvalSymlinks(curr)
			if err != nil {
				return "", fmt.Errorf("%w: eval symlink ancestor %s: %v", ErrInvalidPath, curr, err) //nolint:errorlint
			}
			ev = filepath.Clean(ev)
			// Remainder is suffix from curr to candidate
			suffix, err := filepath.Rel(curr, candidate)
			if err != nil {
				return "", fmt.Errorf("%w: rel suffix: %v", ErrInvalidPath, err) //nolint:errorlint
			}
			if suffix == "." {
				return ev, nil
			}
			joined := filepath.Join(ev, suffix)
			return filepath.Clean(joined), nil
		}
		// If curr exists but is not found, move up
		// Check if parent is shorter than root? We keep walking until we hit an existing dir.
		// If candidate's ancestor chain includes g.root and g.root exists, we'll eventually hit it.
		if curr == g.root {
			// root exists (checked at NewPathGuard), so this branch shouldn't happen if curr==root not exists.
			// But if candidate is under root and root is existing, we will find it.
			break
		}
		curr = parent
		// Avoid infinite loop: if we traversed up beyond root and not found, try root directly
		// If parent length < len(root) and not prefix, we still continue to "/"
		if len(curr) < 2 && curr == "/" {
			if _, err := os.Lstat(curr); err == nil {
				ev, err := filepath.EvalSymlinks(curr)
				if err != nil {
					return "", fmt.Errorf("%w: eval symlink /: %v", ErrInvalidPath, err) //nolint:errorlint
				}
				suffix, _ := filepath.Rel(curr, candidate)
				if suffix == "." {
					return filepath.Clean(ev), nil
				}
				return filepath.Clean(filepath.Join(ev, suffix)), nil
			}
			break
		}
	}
	// Fallback: no existing ancestor found (should not happen since root exists), just return cleaned candidate
	// But still need to eval symlink on root prefix if candidate is under root
	// Try to eval root and re-join rel
	if strings.HasPrefix(candidate, g.root) {
		rel, err := filepath.Rel(g.root, candidate)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			if rel == "." {
				return g.root, nil
			}
			return filepath.Clean(filepath.Join(g.root, rel)), nil
		}
	}
	return filepath.Clean(candidate), nil
}
