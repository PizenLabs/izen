// Package architecture enforces the structural invariants of the Izen codebase
// with AST/dependency-level tests. These are the "architecture linter" of the
// repository: they prevent the engine boundaries from drifting by asserting on
// the actual import graph and on the composition-root service bindings.
//
// The checks are read-only — they never mutate the repository or the Go build
// cache — and run in milliseconds, so they belong in the fast test suite.
package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the module root (the directory containing go.mod). The test
// binary runs from internal/architecture, so the root is two levels up.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("architecture: getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("architecture: could not locate module root (no go.mod found)")
		}
		dir = parent
	}
}

// goFilesUnder returns the relative paths of all non-test .go files under root.
func goFilesUnder(root string) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out
}

// parseFile parses a .go file into an AST.
func parseFile(t *testing.T, path string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("architecture: parse %s: %v", path, err)
	}
	return f, fset
}

// imports returns the set of import paths declared by a parsed file.
func imports(f *ast.File) map[string]bool {
	set := make(map[string]bool)
	for _, imp := range f.Imports {
		if imp.Path != nil {
			set[strings.Trim(imp.Path.Value, `"`)] = true
		}
	}
	return set
}

// importsOfDir returns the union of all import paths across the non-test .go
// files in the given (module-relative) directory.
func importsOfDir(t *testing.T, root, relDir string) map[string]bool {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(relDir))
	set := make(map[string]bool)
	for _, rel := range goFilesUnder(dir) {
		f, _ := parseFile(t, filepath.Join(dir, rel))
		for p := range imports(f) {
			set[p] = true
		}
	}
	return set
}

// assertForbidden reports a failure for every forbidden import path found in
// the importer package's import set.
func assertForbidden(t *testing.T, importer string, got map[string]bool, forbidden ...string) {
	t.Helper()
	for _, f := range forbidden {
		if got[f] {
			t.Errorf("architecture: %s MUST NOT import %q (import drift)", importer, f)
		}
	}
}

const modulePrefix = "github.com/PizenLabs/izen/"

func moduleImport(rel string) string {
	return modulePrefix + rel
}

// ── Forbidden import rules ──────────────────────────────────────────────────

// TestLeaMustNotImportUIOrPrompt asserts the Phase 3 Lea structural engine
// stays decoupled from the presentation layer (internal/ui) and the prompt
// layer (internal/prompt). Lea is a pure graph/index engine: importing the TUI
// or prompt templates would create a cycle-prone, presentation-coupling drift.
func TestLeaMustNotImportUIOrPrompt(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/lea")
	assertForbidden(t, "internal/lea", got, moduleImport("internal/ui"), moduleImport("internal/prompt"))
}

// TestPlannerMustNotImportUI asserts the Context Planner (internal/planner)
// never reaches into the presentation layer. The planner orchestrates context
// sources and budgets; it is consumed BY the UI, never the reverse.
func TestPlannerMustNotImportUI(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/planner")
	assertForbidden(t, "internal/planner", got, moduleImport("internal/ui"))
}

// TestPromptMustNotImportExecution asserts the prompt layer (internal/prompt)
// stays above the execution layer (internal/execution). Prompts are declarative
// template definitions; importing the runner would invert the dependency axis
// and couple prompt assembly to command execution.
func TestPromptMustNotImportExecution(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/prompt")
	assertForbidden(t, "internal/prompt", got, moduleImport("internal/execution"))
}

// ── Service binding invariants ──────────────────────────────────────────────

// leaEngineCall is a discovered `lea.NewEngine(...)` call site.
type leaEngineCall struct {
	file     string
	funcName string
	line     int
}

// findLeaEngineCalls scans a parsed file for selector expressions of the form
// `lea.NewEngine(...)` and records their enclosing function. It iterates the
// top-level FuncDecl bodies directly so every call site is attributed to the
// function that binds the engine.
func findLeaEngineCalls(f *ast.File, fset *token.FileSet) []leaEngineCall {
	var calls []leaEngineCall
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok || x.Name != "lea" || sel.Sel.Name != "NewEngine" {
				return true
			}
			pos := fset.Position(call.Pos())
			calls = append(calls, leaEngineCall{
				file:     pos.Filename,
				funcName: fn.Name.Name,
				line:     pos.Line,
			})
			return true
		})
	}
	return calls
}

// TestLeaEngineSingleCompositionBinding asserts the service-binding invariant:
// the workspace bootstrap/DI binds exactly ONE canonical `lea.Engine` per
// workspace context, and that binding is confined to the composition root
// (internal/ui/program.go). Creating additional engines elsewhere (mode
// engines, adapters, retrieval) would fragment the structural index and break
// the single-source-of-truth contract the UI, planner, and /arch analysis
// depend on.
//
// The one allowed exception is the standalone `izen debug` diagnostic entry
// (cmd/izen/main.go): it spins up a temporary engine purely for on-demand
// inspection and is not part of any workspace context's dependency-injection
// graph. It is whitelisted here explicitly so the invariant remains
// machine-enforced for every other binding site.
func TestLeaEngineSingleCompositionBinding(t *testing.T) {
	root := repoRoot(t)

	// Allowed engine-binding sites. The composition root binds the canonical
	// engine; the diagnostic tool spins up a throwaway inspection instance.
	const compositionRoot = "internal/ui/program.go"
	const diagnosticEntry = "cmd/izen/main.go"
	allowed := map[string]bool{
		compositionRoot: true,
		diagnosticEntry: true,
	}

	var all []leaEngineCall
	all = make([]leaEngineCall, 0, 4)
	for _, rel := range goFilesUnder(root) {
		f, fset := parseFile(t, filepath.Join(root, rel))
		all = append(all, findLeaEngineCalls(f, fset)...)
	}

	if len(all) == 0 {
		t.Fatal("architecture: no lea.NewEngine production call sites found; invariant is unenforced")
	}

	for _, c := range all {
		rel := filepath.ToSlash(strings.TrimPrefix(c.file, root+string(filepath.Separator)))
		if !allowed[rel] {
			t.Errorf("architecture: lea.NewEngine must be bound only in the composition root %s (and the %s diagnostic tool), found at %s:%d (func %s)",
				compositionRoot, diagnosticEntry, rel, c.line, c.funcName)
		}
	}

	// Exactly one canonical engine instance per bootstrap path: every
	// engine-binding function creates exactly one instance, and the composition
	// root holds exactly one such function.
	for _, c := range all {
		if countOf(all, c.funcName) != 1 {
			t.Errorf("architecture: engine-binding function %s creates %d lea.Engine instances, want exactly 1",
				c.funcName, countOf(all, c.funcName))
		}
	}

	var bindingFns []string
	for _, c := range all {
		if !contains(bindingFns, c.funcName) {
			bindingFns = append(bindingFns, c.funcName)
		}
	}
	var bootstrapFns []string
	for _, c := range all {
		rel := filepath.ToSlash(strings.TrimPrefix(c.file, root+string(filepath.Separator)))
		if rel == compositionRoot && !contains(bootstrapFns, c.funcName) {
			bootstrapFns = append(bootstrapFns, c.funcName)
		}
	}
	if len(bootstrapFns) != 1 {
		t.Errorf("architecture: expected exactly one engine-binding function in %s, got %d: %v",
			compositionRoot, len(bootstrapFns), bootstrapFns)
	}
}

func countOf(calls []leaEngineCall, funcName string) int {
	n := 0
	for _, c := range calls {
		if c.funcName == funcName {
			n++
		}
	}
	return n
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
