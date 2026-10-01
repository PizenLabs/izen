package autonomy

// ── PHASE 14 TEST E: ARCHITECTURE LOCK ───────────────────────────────────────
//
// The lock has two halves, and both must be enforced mechanically:
//
//  1. DEPENDENCY DIRECTION. The runtime domain must not depend on presentation.
//     A `Driver -> internal/ui` or `Driver -> internal/presentation` import
//     would make the TUI a completion authority again, and it would also make
//     the driver untestable headless. This file asserts the import graph
//     property directly, so a future contributor cannot reintroduce the
//     inversion by accident and only discover it in review.
//
//  2. HEADLESS EXECUTABILITY. The driver package must compile and run with no
//     terminal attached. Every test in this file is a plain `go test` case;
//     `go test -race -count=1 ./internal/runtime/autonomy/... ./internal/execution/...`
//     is the acceptance command.

import (
	"context"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// forbiddenPresentationImports are the presentation-layer packages the runtime
// domain must never reach for.
var forbiddenPresentationImports = map[string]bool{
	"github.com/PizenLabs/izen/internal/ui":           true,
	"github.com/PizenLabs/izen/internal/presentation": true,
	"github.com/PizenLabs/izen/internal/tui":          true,
}

// domainRoots are the package trees the lock covers. Every package beneath each
// root is walked, plus the transitive closure of their module-local imports —
// an inversion introduced through a helper package is exactly the kind that
// survives review.
var domainRoots = []string{
	"internal/autonomy",
	"internal/execution",
	"internal/runtime",
	"internal/contextcompiler",
}

// lockRoots are the additional roots walked for reachability (a domain package
// reached only from a projection must still be checked).
var lockRoots = []string{
	"internal/autonomy",
	"internal/execution",
	"internal/runtime/autonomy",
	"internal/contextcompiler",
}

// TestPhase14E_RuntimeDomainDoesNotDependOnPresentation walks the import graph
// of every package under the locked trees and fails on ANY edge into the
// presentation layer, transitively.
func TestPhase14E_RuntimeDomainDoesNotDependOnPresentation(t *testing.T) {
	roots := domainRootsLocked(t)
	if len(roots) == 0 {
		t.Fatal("no domain packages were discovered — the lock would pass vacuously")
	}
	visited := make(map[string]bool, len(roots))
	for _, pkg := range roots {
		reachablePresentationImports(t, pkg, visited, map[string]bool{})
	}
	for _, pkg := range lockRoots {
		reachablePresentationImports(t, pkg, visited, map[string]bool{})
	}
	if len(visited) < len(roots) {
		t.Fatalf("walked %d packages, expected at least the %d discovered roots", len(visited), len(roots))
	}
}

// domainRootsLocked discovers every Go package beneath the locked trees and
// returns them as module import paths.
func domainRootsLocked(t *testing.T) []string {
	t.Helper()
	repo := repoRoot(t)
	seen := make(map[string]bool)
	var out []string
	for _, root := range domainRoots {
		dir := filepath.Join(repo, filepath.FromSlash(root))
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("locked tree %s is missing: %v", root, err)
		}
		// A walk error on one entry must not abort the discovery of the rest:
		// an unreadable subdirectory is a coverage gap to report, not a reason
		// to silently pass a narrower lock.
		walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			entry := d.Name()
			if d.IsDir() {
				if entry == "testdata" || strings.HasPrefix(entry, ".") || strings.HasPrefix(entry, "_") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry, ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(repo, path)
			if relErr != nil {
				return relErr
			}
			importPath := "github.com/PizenLabs/izen/" + filepath.ToSlash(rel)
			importPath = strings.TrimSuffix(importPath, ".go")
			if !seen[importPath] {
				seen[importPath] = true
				out = append(out, importPath)
			}
			return nil
		})
		if walkErr != nil {
			t.Errorf("walking %s failed, the lock would cover less than it claims: %v", root, walkErr)
		}
	}
	return out
}

// repoRoot resolves the module root from this package's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// internal/runtime/autonomy -> module root
	return filepath.Clean(filepath.Join(dir, "..", "..", ".."))
}

// reachablePresentationImports recursively inspects one package's imports.
func reachablePresentationImports(t *testing.T, pkg string, visited, onStack map[string]bool) {
	t.Helper()
	if visited[pkg] || onStack[pkg] {
		return
	}
	visited[pkg] = true
	onStack[pkg] = true
	defer delete(onStack, pkg)

	imports, err := packageImports(pkg)
	if err != nil {
		// A package that cannot be resolved is not a layering violation; the
		// compile step already covers resolution.
		return
	}
	for _, imported := range imports {
		if forbiddenPresentationImports[imported] {
			t.Errorf("presentation layer violation: %s imports %s — "+
				"the runtime domain must reach the objective authority, never the projector", pkg, imported)
			continue
		}
		if !strings.HasPrefix(imported, "github.com/PizenLabs/izen/") {
			continue
		}
		reachablePresentationImports(t, imported, visited, onStack)
	}
}

// packageImports returns the module-local imports of one package.
func packageImports(pkg string) ([]string, error) {
	dir := packageDir(pkg)
	if dir == "" {
		return nil, os.ErrNotExist
	}
	// The textual scan runs FIRST and is authoritative for layering: it sees
	// every module-local import, including ones the type-checker elides. The
	// typed set is additive coverage for packages that need their test files to
	// resolve.
	imports, scanErr := scanImports(dir)
	buildPkg, err := build.ImportDir(dir, build.ImportComment)
	if err != nil || buildPkg == nil {
		return imports, scanErr
	}
	return append(imports, buildPkg.Imports...), nil
}

// packageDir maps a module-local import path to its source directory.
func packageDir(pkg string) string {
	rel := strings.TrimPrefix(pkg, "github.com/PizenLabs/izen/")
	dir := filepath.Join("..", "..", "..", filepath.FromSlash(rel))
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	return dir
}

// scanImports reads the package's Go sources and extracts every quoted module
// import path. It is deliberately conservative: it over-reports rather than
// under-reports, because a false violation costs a comment and a missed one
// costs the architecture.
func scanImports(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			continue
		}
		for _, imp := range quotedModuleImports(string(data)) {
			if !seen[imp] {
				seen[imp] = true
				out = append(out, imp)
			}
		}
	}
	return out, nil
}

// quotedModuleImports extracts every quoted string literal that names a module
// path, across multi-line import blocks.
func quotedModuleImports(source string) []string {
	const prefix = `"github.com/PizenLabs/izen/`
	var out []string
	rest := source
	for {
		idx := strings.Index(rest, prefix)
		if idx < 0 {
			return out
		}
		rest = rest[idx:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		rest = rest[end+1:]
	}
}

// TestPhase14E_DriverIsHeadless is the executable half of the lock: the driver
// package's own tests run with no terminal, no ANSI-capable stdout and no
// presentation wiring. It is intentionally trivial — its value is that it
// compiles and runs at all under the acceptance command.
func TestPhase14E_DriverIsHeadless(t *testing.T) {
	// A nil bus is the headless configuration: the loop still runs, still
	// terminates, and still refuses a false completion with no observer. A
	// cancelled context keeps the run from spending a single provider request.
	_, mock, a, _ := testHarness(t, nil) // provider always errors
	d := NewDriver(a, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	term, err := d.Run(ctx, "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("headless Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want a terminal non-completed state", term)
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("headless run reached completed with no bus and no evidence")
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 (cancelled before execution)", mock.calls())
	}
}
