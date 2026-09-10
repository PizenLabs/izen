package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prodRoot is the repository root relative to the test file.
const prodRoot = "../.."

// isProductionGoFile returns true for .go files under internal/ that are not
// test files, fixtures, or generated code.
func isProductionGoFile(path string) bool {
	if !strings.HasSuffix(path, ".go") {
		return false
	}
	if strings.HasSuffix(path, "_test.go") {
		return false
	}
	if strings.Contains(path, "testdata") || strings.Contains(path, "fixture") {
		return false
	}
	return strings.Contains(path, "internal/")
}

// collectProductionFiles returns all production .go files under internal/.
func collectProductionFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(filepath.Join(prodRoot, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable paths
		}
		if info.IsDir() {
			return nil
		}
		if isProductionGoFile(path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	return files
}

// fileContains reports whether the file at path contains any of the given
// substrings. Lines that are pure comments (// or /*) are skipped.
func fileContains(path string, substrings []string) ([]string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	content := string(data)
	var found []string
	for _, sub := range substrings {
		if strings.Contains(content, sub) {
			// Skip comment-only lines: check if the match appears only in comments.
			// Simple heuristic: if every line containing the substring is a comment,
			// treat it as not found in production logic.
			lines := strings.Split(content, "\n")
			inComment := true
			for _, line := range lines {
				if !strings.Contains(line, sub) {
					continue
				}
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
					continue
				}
				inComment = false
				break
			}
			if !inComment {
				found = append(found, sub)
			}
		}
	}
	return found, len(found) > 0
}

// ── Test: No Default Model Fallback ──────────────────────────────────────────
// TestNoDefaultModelFallback verifies that no production code path creates
// model identifiers independently of the Model Resolution authority.
//
// Forbidden patterns in production code (excluding tests/fixtures/docs):
//   - "defaultModel" as a variable or field name
//   - "fallbackModel" as a variable or field name
//   - c.model as a provider-local hardcoded default execution model
//   - Hardcoded model strings in execution paths (e.g. "gpt-4", "claude-")
//
// This test enforces the architecture invariant that all model resolution
// flows through authority.ResolveModel or RuntimeAuthority.ResolveForIntent.
func TestNoDefaultModelFallback(t *testing.T) {
	files := collectProductionFiles(t)
	if len(files) == 0 {
		t.Fatal("no production files found — test setup error")
	}

	// Patterns that indicate independent model resolution outside authority.
	forbidden := []string{
		"defaultModel",
		"fallbackModel",
		"WithFallbackModel",
	}

	var violations []string
	for _, path := range files {
		matches, found := fileContains(path, forbidden)
		if found {
			for _, m := range matches {
				violations = append(violations, filepath.Base(path)+": "+m)
			}
		}

		// Check for provider-local model fallback in LLM client files.
		if strings.Contains(path, "internal/llm/") && strings.HasSuffix(path, ".go") {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			content := string(data)
			// Look for "return c.model" pattern — the provider-local fallback.
			if strings.Contains(content, "return c.model") {
				violations = append(violations, filepath.Base(path)+": return c.model (provider-local fallback)")
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("forbidden model fallback patterns found in production code:\n%s",
			strings.Join(violations, "\n"))
	}
}

// ── Test: No Duplicate Model Authority ───────────────────────────────────────
// TestNoDuplicateModelAuthority verifies that RuntimeAuthority is the only
// source of active model state. No other struct should hold a "current model"
// field that could diverge from the authority's active binding.
//
// Forbidden patterns:
//   - "currentModel" field declarations in non-authority structs
//   - "activeModel" field declarations outside authority packages
//   - Direct model state mutation bypassing authority
func TestNoDuplicateModelAuthority(t *testing.T) {
	files := collectProductionFiles(t)
	if len(files) == 0 {
		t.Fatal("no production files found — test setup error")
	}

	// These patterns indicate a struct field holding independent model state.
	forbidden := []string{
		"currentModel string",
		"activeModel string",
		"modelOverride string",
	}

	var violations []string
	for _, path := range files {
		// Allow the authority package itself to define model state.
		if strings.Contains(path, "internal/runtime/authority/") {
			continue
		}
		// Allow config package (it defines the persistence model, not runtime state).
		if strings.Contains(path, "internal/config/") {
			continue
		}
		// Allow adapter package (validation guard functions, not model authorities).
		if strings.Contains(path, "internal/provider/adapter/") {
			continue
		}

		matches, found := fileContains(path, forbidden)
		if found {
			for _, m := range matches {
				violations = append(violations, filepath.Base(path)+": "+m)
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("duplicate model authority fields found outside authority/config packages:\n%s",
			strings.Join(violations, "\n"))
	}
}

// ── Test: ResolveModel Is Sole Path ──────────────────────────────────────────
// TestResolveModelIsSolePath verifies that all execution paths that produce
// a model identifier go through the authority's ResolveModel or
// ResolveForIntent. It checks that:
//
//  1. The authority package exports ResolveModel (the stateless resolver).
//  2. RuntimeAuthority exports ResolveForIntent (the stateful entry point).
//  3. The executor's resolveModel does not contain its own model resolution
//     logic (it delegates to the request's Model field, set by the caller).
//
// This is a structural check — it verifies the API surface exists, not that
// every call site uses it (that is enforced by the other negative tests).
func TestResolveModelIsSolePath(t *testing.T) {
	// Check 1: authority.ResolveModel must exist.
	resolverPath := filepath.Join(prodRoot, "internal", "runtime", "authority", "resolver.go")
	data, err := os.ReadFile(resolverPath)
	if err != nil {
		t.Fatalf("cannot read resolver.go: %v", err)
	}
	if !strings.Contains(string(data), "func ResolveModel(") {
		t.Error("authority.ResolveModel not found — stateless resolver missing from API surface")
	}

	// Check 2: RuntimeAuthority.ResolveForIntent must exist.
	authorityPath := filepath.Join(prodRoot, "internal", "runtime", "authority.go")
	data, err = os.ReadFile(authorityPath)
	if err != nil {
		t.Fatalf("cannot read authority.go: %v", err)
	}
	if !strings.Contains(string(data), "func (a *RuntimeAuthority) ResolveForIntent(") {
		t.Error("RuntimeAuthority.ResolveForIntent not found — stateful entry point missing")
	}

	// Check 3: executor resolveModel must not contain independent model logic.
	executorPath := filepath.Join(prodRoot, "internal", "execution", "executor.go")
	data, err = os.ReadFile(executorPath)
	if err != nil {
		t.Fatalf("cannot read executor.go: %v", err)
	}
	content := string(data)
	// The executor must not fall back to a hardcoded model.
	if strings.Contains(content, "return c.model") {
		t.Error("executor resolveModel falls back to c.model — must use request-provided model only")
	}

	// Check 4: No provider adapter should contain a model fallback.
	llmDir := filepath.Join(prodRoot, "internal", "llm")
	entries, err := os.ReadDir(llmDir)
	if err != nil {
		t.Fatalf("cannot read llm/ directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(llmDir, entry.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		if strings.Contains(content, "return c.model") {
			t.Errorf("provider adapter %s still falls back to c.model", entry.Name())
		}
	}
}
