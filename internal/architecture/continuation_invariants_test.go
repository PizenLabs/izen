package architecture

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContinuationMustNotImportExecutionAuthority pins §2.1, §17: continuation is
// a pure proposal layer and must not import execution, authorization, scheduler,
// provider, or TUI implementations.
func TestContinuationMustNotImportForbidden(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/continuation")
	forbidden := []string{
		moduleImport("internal/runtime/executor"),
		moduleImport("internal/runtime/scheduler"),
		moduleImport("internal/execution"),
		moduleImport("internal/execution/scheduler"),
		moduleImport("internal/core/domain/authorization"),
		moduleImport("internal/runtime/authorization"),
		moduleImport("internal/domain/capability"),
		moduleImport("internal/domain/capability/policy"),
		moduleImport("internal/ui"),
		moduleImport("internal/tui"),
		moduleImport("internal/provider"),
		moduleImport("internal/providers"),
		moduleImport("internal/ai"),
		moduleImport("internal/adapters/web"),
	}
	for _, f := range forbidden {
		if got[f] {
			t.Errorf("architecture: internal/continuation MUST NOT import %q (continuation is proposal-only, no execution/auth/scheduler/TUI/provider)", f)
		}
	}
}

// TestContinuationMustNotImportExecutionDirectly checks file-level raw imports
// for os/exec and direct workspace mutation (os.WriteFile) inside continuation.
func TestContinuationMustNotContainExecutionPrimitives(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal/continuation")
	files := goFilesUnder(dir)
	for _, rel := range files {
		f, _ := parseFile(t, filepath.Join(dir, rel))
		for p := range imports(f) {
			if p == "os/exec" {
				t.Errorf("architecture: internal/continuation/%s must not import os/exec", rel)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "os" && sel.Sel.Name == "WriteFile" {
				t.Errorf("architecture: internal/continuation/%s must not call os.WriteFile — no workspace mutation", rel)
			}
			return true
		})
	}
}

// TestCoreMustNotImportWebAdapter re-pins generality boundary after Phase 4.
func TestCoreMustNotImportWebAdapterContinuation(t *testing.T) {
	root := repoRoot(t)
	corePkgs := []string{
		"internal/understanding",
		"internal/problemsurface",
		"internal/changesurface",
		"internal/mutationstrategy",
		"internal/problem",
		"internal/continuation",
	}
	for _, pkg := range corePkgs {
		got := importsOfDir(t, root, pkg)
		if got[moduleImport("internal/adapters/web")] {
			t.Errorf("architecture: %s MUST NOT import adapters/web (core stays domain-neutral)", pkg)
		}
	}
	// Also check source does not reference StaticWeb in code lines
	for _, pkg := range corePkgs {
		dir := filepath.Join(root, filepath.FromSlash(pkg))
		for _, rel := range goFilesUnder(dir) {
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			data, _ := os.ReadFile(filepath.Join(dir, rel))
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				trim := strings.TrimSpace(line)
				if strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "/*") || strings.HasPrefix(trim, "*") {
					continue
				}
				if strings.Contains(line, "StaticWeb") {
					t.Errorf("architecture: %s/%s must not reference StaticWeb in code: %q", pkg, rel, line)
				}
			}
		}
	}
}

// TestNoSecondSchedulerOrExecutor pins §2.1, §2.2: there is exactly one
// StepScheduler (internal/runtime/scheduler) and one execution authority
// (internal/execution.RuntimeExecutor / internal/runtime/substrate).
func TestNoSecondSchedulerOrExecutor(t *testing.T) {
	root := repoRoot(t)
	forbiddenSchedulers := []string{
		"AdaptiveScheduler",
		"ContinuationScheduler",
		"AgentScheduler",
		"ProblemScheduler",
		"MutationScheduler",
		"ReasoningScheduler",
	}
	forbiddenExecutors := []string{
		"ContinuationExecutor",
		"AdaptiveExecutor",
	}
	for _, rel := range goFilesUnder(root) {
		if strings.Contains(rel, "_test.go") {
			continue
		}
		// Only inspect non-vendor, non-test go files
		if strings.Contains(rel, "internal/continuation/") {
			f, _ := parseFile(t, filepath.Join(root, rel))
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				if gd.Tok.String() != "type" {
					continue
				}
				_ = gd
			}
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		content := string(data)
		for _, name := range forbiddenSchedulers {
			if strings.Contains(content, "type "+name) {
				t.Errorf("architecture: second scheduler %q forbidden, found in %s", name, rel)
			}
		}
		for _, name := range forbiddenExecutors {
			if strings.Contains(content, "type "+name) && strings.Contains(rel, "internal/continuation") {
				t.Errorf("architecture: second executor %q forbidden in continuation, found in %s", name, rel)
			}
		}
	}
	// Positive: canonical scheduler and executor must still exist
	if _, err := os.Stat(filepath.Join(root, "internal/runtime/scheduler/scheduler.go")); err != nil {
		t.Error("architecture: canonical StepScheduler missing at internal/runtime/scheduler/scheduler.go")
	}
	if _, err := os.Stat(filepath.Join(root, "internal/execution/executor.go")); err != nil {
		t.Error("architecture: canonical RuntimeExecutor missing at internal/execution/executor.go")
	}
}

// TestContinuationDomainNeutral ensures no domain-specific continuation types.
func TestContinuationDomainNeutralArchitecture(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal/continuation")
	forbidden := []string{"ReactContinuation", "BackendContinuation", "GoContinuation", "StaticWebContinuation", "DatabaseContinuation", "WebContinuation"}
	for _, rel := range goFilesUnder(dir) {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(dir, rel))
		for _, name := range forbidden {
			if strings.Contains(string(data), name) {
				t.Errorf("architecture: domain-specific continuation %q forbidden, found in %s", name, rel)
			}
		}
	}
}

// TestContinuationDoesNotExpandAuthorizationScope proves continuation has no
// capability/scope grant fields via AST field inspection.
func TestContinuationHasNoAuthorizationFields(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal/continuation/types.go"))
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if gd.Tok.String() != "type" {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if ts.Name.Name != "ContinuationDecision" && ts.Name.Name != "StepProposal" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					low := strings.ToLower(name.Name)
					if strings.Contains(low, "grant") || strings.Contains(low, "authoriz") || strings.Contains(low, "capability") {
						t.Errorf("architecture: %s field %q must not be authorization/capability-bearing", ts.Name.Name, name.Name)
					}
				}
			}
		}
	}
}
