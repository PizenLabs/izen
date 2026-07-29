package guard

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
)

// Sentinel errors for scope guard violations.
var (
	ErrScopeViolation = errors.New("scope violation: patch touches file outside declared plan scope")
	ErrBudgetExceeded = errors.New("scope guard: patch exceeds mutation budget limits")
	ErrReservedTarget = errors.New("scope violation: target path is a reserved system keyword")
)

// reservedTargets is the blacklist of system keywords and placeholders that
// MUST NEVER be accepted as file mutation targets. These leak when the LLM
// substitutes a generic workspace context identifier for a real file path.
var reservedTargets = map[string]bool{
	"workspace": true,
	"root":      true,
	"cwd":       true,
	".":         true,
	"/":         true,
	"[n/a]":     true,
	"[resolve]": true,
}

// ScopeDeclaration defines the allowed mutation boundaries for a plan. It is
// derived from a PlanArtifact and contains the exact set of files and symbols
// the plan is authorised to mutate.
type ScopeDeclaration struct {
	AllowedFiles   []string // glob patterns or exact file paths
	AllowedSymbols []string // fully-qualified symbol names (e.g. "pkg/orders.ComputeTotal")
}

// ScopeGuard enforces file and symbol boundaries during patch validation.
type ScopeGuard struct {
	scope  *ScopeDeclaration
	budget *budget.MutationBudget
}

// NewScopeGuard creates a scope guard from a declaration and an optional budget.
// When budget is nil, diff line limits are not enforced.
func NewScopeGuard(scope *ScopeDeclaration, b *budget.MutationBudget) *ScopeGuard {
	return &ScopeGuard{
		scope:  scope,
		budget: b,
	}
}

var knownExts = []string{".go", ".js", ".ts", ".py", ".html", ".css", ".json", ".yaml", ".yml", ".md", ".rs", ".rb", ".java", ".kt", ".swift"}

// stripExt removes known file extensions from a path segment, returning the
// base name. Used to detect reserved keywords in filenames like workspace.go.
func stripExt(seg string) string {
	lower := strings.ToLower(seg)
	for _, ext := range knownExts {
		if strings.HasSuffix(lower, ext) {
			return seg[:len(seg)-len(ext)]
		}
	}
	return seg
}

// ValidateMutationTarget checks whether a single target file path is a valid
// mutation target. It returns:
//   - ErrReservedTarget if the path matches a reserved system keyword (checked
//     against each path segment both with and without known file extensions)
//   - ErrScopeViolation if the path is not in the plannedTargets list
//   - nil if all checks pass
//
// When plannedTargets is nil or empty, only reserved keyword checking is performed.
func (sg *ScopeGuard) ValidateMutationTarget(targetPath string, plannedTargets []string) error {
	clean := filepath.ToSlash(filepath.Clean(targetPath))

	// Reject root paths directly.
	if clean == "." || clean == "/" {
		return fmt.Errorf("%w: %q is a root path reference", ErrReservedTarget, targetPath)
	}

	// Check the full cleaned path.
	if reservedTargets[strings.ToLower(clean)] {
		return fmt.Errorf("%w: %q is a reserved system keyword", ErrReservedTarget, targetPath)
	}

	// Check each path segment against the reserved keywords.
	segments := strings.Split(clean, "/")
	for _, seg := range segments {
		trimmed := strings.TrimSpace(strings.ToLower(seg))
		if trimmed == "" {
			continue
		}
		if reservedTargets[trimmed] {
			return fmt.Errorf("%w: %q contains reserved segment %q", ErrReservedTarget, targetPath, seg)
		}
		// Also check without known file extension.
		if reservedTargets[stripExt(trimmed)] {
			return fmt.Errorf("%w: %q base name matches reserved keyword", ErrReservedTarget, targetPath)
		}
	}

	if len(plannedTargets) > 0 {
		allowed := false
		for _, planned := range plannedTargets {
			if planned == clean || planned == targetPath {
				allowed = true
				break
			}
			if matchPath(clean, planned) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%w: %q not in planned targets %v", ErrScopeViolation, targetPath, plannedTargets)
		}
	}

	return nil
}

// ValidatePatch checks that a patch artifact respects the declared scope and
// mutation budget. It returns:
//   - ErrScopeViolation if any patched file falls outside the allowed scope
//   - ErrBudgetExceeded if the patch diff exceeds the remaining budgeted lines
//   - nil if all checks pass
func (sg *ScopeGuard) ValidatePatch(patch *artifact.PatchArtifact) error {
	if sg.scope == nil {
		return errors.New("scope guard: no scope declaration set")
	}
	if patch == nil {
		return errors.New("scope guard: nil patch artifact")
	}

	files := extractFilesFromPatch(patch)
	if len(files) == 0 {
		return errors.New("scope guard: no files found in patch content")
	}

	for _, f := range files {
		allowed := false
		for _, pattern := range sg.scope.AllowedFiles {
			if matchPath(f, pattern) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%w: %q not in declared scope %v",
				ErrScopeViolation, f, sg.scope.AllowedFiles)
		}
	}

	if sg.budget != nil && sg.budget.MaxDiffLines > 0 {
		lineCount := countDiffLines(patch.PatchContent)
		remaining := sg.budget.RemainingDiffLines()
		if lineCount > remaining {
			return fmt.Errorf("%w: patch has %d diff lines, remaining budget is %d lines",
				ErrBudgetExceeded, lineCount, remaining)
		}
	}

	return nil
}

// extractFilesFromPatch returns the set of file paths touched by the patch.
// It parses both the Changes list and the PatchContent diff headers.
func extractFilesFromPatch(patch *artifact.PatchArtifact) []string {
	seen := make(map[string]bool)
	var files []string

	for _, change := range patch.Changes {
		path := filepath.ToSlash(filepath.Clean(change))
		if path != "." && path != "/" && !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}

	if patch.PatchContent != "" {
		for _, line := range strings.Split(patch.PatchContent, "\n") {
			// Unified diff: +++ b/path/to/file   or   --- a/path/to/file
			if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
				raw := strings.TrimSpace(line[4:])
				// Strip leading b/ or a/ prefix from git-style diffs
				raw = strings.TrimPrefix(raw, "a/")
				raw = strings.TrimPrefix(raw, "b/")
				path := filepath.ToSlash(filepath.Clean(raw))
				if path != "." && path != "/" && !seen[path] {
					seen[path] = true
					files = append(files, path)
				}
			}
		}
	}

	return files
}

// countDiffLines counts the number of changed lines (+/-) in a unified diff,
// excluding hunk headers (@@ ...), file headers (---/+++), and context lines.
func countDiffLines(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+', '-':
			// Exclude --- and +++ file header lines
			if !strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "+++ ") {
				count++
			}
		}
	}
	return count
}

// matchPath checks whether a file path matches a pattern. The pattern may be
// an exact path or a glob. It uses filepath.Match for glob semantics.
func matchPath(path, pattern string) bool {
	cleanPath := filepath.ToSlash(filepath.Clean(path))
	cleanPattern := filepath.ToSlash(filepath.Clean(pattern))

	if cleanPath == cleanPattern {
		return true
	}

	if matched, err := filepath.Match(cleanPattern, cleanPath); err == nil && matched {
		return true
	}

	if strings.HasSuffix(cleanPattern, "/...") {
		prefix := strings.TrimSuffix(cleanPattern, "/...")
		if cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+"/") {
			return true
		}
	}

	return strings.Contains(cleanPath, cleanPattern)
}
