package retrieval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TargetPathResolver resolves fuzzy or relative LLM-generated file paths
// against the actual workspace file tree. It prevents mutation target drift
// where the LLM emits a generic placeholder (e.g. "workspace") instead of
// the real repository path.
type TargetPathResolver struct {
	root     string
	fallback *FallbackChain
}

// NewTargetPathResolver creates a resolver rooted at workspaceRoot.
func NewTargetPathResolver(workspaceRoot string) *TargetPathResolver {
	return &TargetPathResolver{
		root:     workspaceRoot,
		fallback: NewFallbackChain(workspaceRoot),
	}
}

// Resolve resolves a raw LLM-emitted target path against the workspace.
// It returns the confirmed absolute path or an error.
//
// Resolution order:
//  1. If the raw path is a reserved keyword, return ErrReservedTarget.
//  2. If the raw path exists as-is relative to root, return it.
//  3. If the file exists on disk at the resolved absolute path, return it.
//  4. Attempt fuzzy match via glob for the base filename.
//  5. Fall back to ripgrep for content search.
//  6. If all fail, return ErrTargetNotFound.
func (r *TargetPathResolver) Resolve(ctx context.Context, rawPath string) (string, error) {
	if rawPath == "" {
		return "", fmt.Errorf("target path is empty")
	}

	clean := filepath.ToSlash(filepath.Clean(rawPath))
	if clean == "." || clean == "/" {
		return "", fmt.Errorf("target path %q resolves to root", rawPath)
	}

	// Step 1: Check if path exists as-is relative to workspace root.
	abs := filepath.Join(r.root, clean)
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		return clean, nil
	}

	// Step 2: Check the raw path itself (may have been passed with leading ./ etc.)
	if info, err := os.Stat(filepath.Join(r.root, rawPath)); err == nil && !info.IsDir() {
		rel, _ := filepath.Rel(r.root, filepath.Join(r.root, rawPath))
		return filepath.ToSlash(rel), nil
	}

	// Step 3: Attempt fuzzy match using glob with base filename.
	base := filepath.Base(clean)
	if base != "" && base != "." {
		globPattern := "**/" + base
		rs := r.fallback.Glob(globPattern)
		if rs != nil && !rs.Empty() {
			best := rs.Best()
			if best != nil && best.File != "" {
				return best.File, nil
			}
		}

		// Step 4: Try direct glob from root for files with same name.
		rs2 := r.fallback.Glob("**/*" + base + "*")
		if rs2 != nil && !rs2.Empty() {
			best := rs2.Best()
			if best != nil && best.File != "" {
				return best.File, nil
			}
		}

		// Step 5: Walk the workspace tree looking for filename matches.
		if match := r.walkAndMatch(base); match != "" {
			return match, nil
		}
	}

	// Step 6: Ripgrep the path as a text pattern in file paths.
	if r.fallback != nil {
		rgPattern := strings.ReplaceAll(clean, string(filepath.Separator), "/")
		rgPattern = strings.TrimPrefix(rgPattern, "./")
		rs := r.fallback.Ripgrep(rgPattern, "") //nolint:contextcheck
		if rs != nil && !rs.Empty() {
			best := rs.Best()
			if best != nil && best.File != "" {
				return best.File, nil
			}
		}
	}

	return "", fmt.Errorf("target path %q not found in workspace", rawPath)
}

// walkAndMatch walks the workspace directory tree looking for files whose
// base name matches the given name (case-insensitive suffix match).
func (r *TargetPathResolver) walkAndMatch(name string) string {
	lower := strings.ToLower(name)
	var match string
	_ = filepath.Walk(r.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		baseLower := strings.ToLower(info.Name())
		if baseLower == lower {
			rel, relErr := filepath.Rel(r.root, path)
			if relErr == nil {
				if match == "" || len(rel) < len(match) {
					match = filepath.ToSlash(rel)
				}
			}
		}
		return nil
	})
	return match
}
