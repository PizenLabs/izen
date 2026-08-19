// Phase 4 autonomous-runtime enforcement: these tests pin the loop contract —
// the autonomous loop is a CONSUMER of the RuntimeExecutor, never a second
// execution authority. It must not reach the provider, the PatchManager, the
// filesystem, or the execution engine directly. The loop contract lives in
// internal/autonomy (runtime_loop.go) which must stay execution-free: it
// imports only events/language and drives execution through the Executor port
// bound at the composition root.
package architecture

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutonomousLoopNeverImportsExecutionAuthority asserts the loop contract
// package (internal/autonomy) never imports the execution engine, the
// providers, or the patch manager. Those are the surfaces that would give the
// loop direct execution authority. The ONLY path to execution is the Executor
// port (LoopRequest/Executor in autonomy/runtime_loop.go), which the
// composition root binds to the RuntimeExecutor.
func TestAutonomousLoopNeverImportsExecutionAuthority(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/autonomy")
	forbidden := []string{
		moduleImport("internal/execution"),
		moduleImport("internal/providers"),
		moduleImport("internal/patch"),
		moduleImport("internal/ai"),
	}
	for _, f := range forbidden {
		if got[f] {
			t.Errorf("architecture: internal/autonomy MUST NOT import %q (loop must consume RuntimeExecutor via the Executor port)", f)
		}
	}
}

// TestAutonomousLoopContractExists asserts the Phase 4 loop contract symbols
// exist (anti-vacuous guard for TestAutonomousLoopNeverImportsExecutionAuthority).
// The contract — RuntimeState, LoopBounds, LoopDecision, RecoverFailure,
// RuntimeLoop, Executor — is the deliverable; the negative import test is only
// meaningful while the contract is present.
func TestAutonomousLoopContractExists(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, filepath.Join(root, "internal/autonomy/runtime_loop.go"))

	declared := map[string]bool{}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			declared[fn.Name.Name] = true
		}
		if gd, ok := decl.(*ast.GenDecl); ok {
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					declared[ts.Name.Name] = true
				}
			}
		}
	}
	// Track identifiers used in the file too (types referenced by methods).
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		used[id.Name] = true
		_ = fset.Position(id.Pos())
		return true
	})

	required := []string{
		"RuntimeState", "RuntimeLoop", "LoopBounds", "LoopDecision",
		"LoopAction", "RecoverFailure", "Executor", "LoopRequest",
		"HumanBoundary", "LoopTermination", "Observation",
	}
	for _, sym := range required {
		if !declared[sym] && !used[sym] {
			t.Errorf("architecture: autonomous loop contract symbol %q must exist in internal/autonomy/runtime_loop.go", sym)
		}
	}
}

// TestAutonomousLoopNoDirectFilesystemMutation asserts the loop contract file
// performs no direct filesystem mutation (os.WriteFile/os.Create/os.Rename/os.
// Remove). The loop must reach the filesystem ONLY through the RuntimeExecutor.
func TestAutonomousLoopNoDirectFilesystemMutation(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal/autonomy/runtime_loop.go"))

	var writes []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != "os" {
			return true
		}
		switch sel.Sel.Name {
		case "WriteFile", "Create", "Rename", "Remove", "RemoveAll", "MkdirAll":
			writes = append(writes, "os."+sel.Sel.Name)
		}
		return true
	})
	if len(writes) > 0 {
		t.Errorf("architecture: autonomous loop MUST NOT mutate the filesystem directly, found %v", strings.Join(writes, ", "))
	}
}

// TestAutonomousLoopNoProviderInvocation asserts the loop contract file never
// invokes a provider (ai.Provider, providers.*). Provider invocation is the
// RuntimeExecutor's authority alone.
func TestAutonomousLoopNoProviderInvocation(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, filepath.Join(root, "internal/autonomy/runtime_loop.go"))

	var sites []callSite
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if x.Name == "ai" || strings.HasPrefix(x.Name, "Provider") {
			pos := fset.Position(call.Pos())
			sites = append(sites, callSite{file: pos.Filename, line: pos.Line})
		}
		return true
	})
	if len(sites) > 0 {
		t.Errorf("architecture: autonomous loop MUST NOT invoke a provider, found %d site(s)", len(sites))
	}
}

// TestRuntimeAutonomyPackageHasSingleBoundary asserts the Phase 5 binding
// package (internal/runtime/autonomy) is the ONLY composition-boundary surface
// between the loop contract and the execution authority, and that it stays a
// boundary: it may reach the RuntimeExecutor (internal/execution) but MUST NOT
// reach the providers, the patch manager, or the ai provider API directly. The
// adapter/driver drive execution exclusively through the RuntimeExecutor.
func TestRuntimeAutonomyPackageHasSingleBoundary(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/runtime/autonomy")
	forbidden := []string{
		moduleImport("internal/providers"),
		moduleImport("internal/patch"),
		moduleImport("internal/ai"),
	}
	for _, f := range forbidden {
		if got[f] {
			t.Errorf("architecture: internal/runtime/autonomy MUST NOT import %q (single execution boundary is the RuntimeExecutor)", f)
		}
	}
}

// TestUINeverFabricatesLoopTransitions asserts the UI is a pure projection of
// loop.transition events. Phase 4 briefly let the UI synthesize preview loop
// transitions (publishAutonomyLoop); that surface is removed. The UI must only
// subscribe to the event, never publish it, and never drive the bounded loop
// (internal/runtime/autonomy) itself.
func TestUINeverFabricatesLoopTransitions(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/ui")
	if got[moduleImport("internal/runtime/autonomy")] {
		t.Errorf("architecture: internal/ui MUST NOT import internal/runtime/autonomy (UI is a projection, the driver owns the loop)")
	}
}

// TestAutonomousDriverContractExists is an anti-vacuous guard: the negative
// boundary assertions above are only meaningful while the Phase 5 contract
// (ExecutorAdapter + Driver) is present.
func TestAutonomousDriverContractExists(t *testing.T) {
	root := repoRoot(t)
	// Assert the contract symbols exist by parsing both files together.
	required := []string{"ExecutorAdapter", "NewExecutorAdapter", "Driver", "NewDriver", "Resolved"}
	declared := map[string]bool{}
	for _, rel := range []string{"adapter.go", "driver.go"} {
		f, _ := parseFile(t, filepath.Join(root, "internal/runtime/autonomy", rel))
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range gd.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						declared[ts.Name.Name] = true
					}
				}
			}
		}
	}
	used := map[string]bool{}
	for _, rel := range []string{"adapter.go", "driver.go"} {
		f, _ := parseFile(t, filepath.Join(root, "internal/runtime/autonomy", rel))
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				used[id.Name] = true
			}
			return true
		})
	}
	for _, sym := range required {
		if !declared[sym] && !used[sym] {
			t.Errorf("architecture: Phase 5 contract symbol %q must exist in internal/runtime/autonomy", sym)
		}
	}
}
