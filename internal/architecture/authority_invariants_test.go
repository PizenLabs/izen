// Phase 0 execution-authority remediation guards.
//
// These tests pin the P0 invariant: the RuntimeExecutor is the SOLE
// workspace-mutation authority. UI modules and caller-side entry points act
// strictly as intent producers and evidence projections — they must never own
// apply machinery, execution-engine transaction lifecycles, or legacy
// fallback paths that could mutate outside the runtime boundary.
package architecture

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uiProductionFiles lists every non-test Go file under internal/ui.
func uiProductionFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "internal", "ui"))
	if err != nil {
		t.Fatalf("architecture: read internal/ui: %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(root, "internal", "ui", name))
	}
	return files
}

// findFuncDecl locates a top-level function declaration by name.
func findFuncDecl(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// forbidLegacyExecInFunc fails when fn's body invokes a legacy execution
// handler directly, touches the UI-owned execution engine / PatchManager,
// invokes a provider, or writes files.
func forbidLegacyExecInFunc(t *testing.T, f *ast.File, fset *token.FileSet, fn *ast.FuncDecl, label string) {
	t.Helper()
	if fn == nil {
		return
	}
	bannedDirect := map[string]bool{
		"streamCmd":         true,
		"proposeBuildPatch": true,
		"runBuildFastTrack": true,
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pos := fset.Position(call.Pos())
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if bannedDirect[fun.Name] {
				t.Errorf("architecture: %s must not invoke legacy execution handler %s (line %d)", label, fun.Name, pos.Line)
			}
		case *ast.SelectorExpr:
			recv := renderExpr(fun.X)
			switch {
			case recv == "m.execEng" || recv == "m.provider" || strings.HasSuffix(recv, ".Patches"):
				t.Errorf("architecture: %s must not access %s.%s — execution belongs to the RuntimeExecutor (line %d)",
					label, recv, fun.Sel.Name, pos.Line)
			case fun.Sel.Name == "Execute" || fun.Sel.Name == "ExecuteStream":
				t.Errorf("architecture: %s must not invoke a provider (%s.%s, line %d)", label, recv, fun.Sel.Name, pos.Line)
			case fun.Sel.Name == "WriteFile":
				t.Errorf("architecture: %s must not write files (%s.WriteFile, line %d)", label, recv, pos.Line)
			}
		}
		return true
	})
}

// TestUIOwnsNoExecutionEngineTransactions pins P0 requirement 3 across the
// whole UI layer: no module under internal/ui may open, commit, or roll back
// an execution-engine transaction, and none may wire or invoke the UI-owned
// PatchManager (SetLedger/SetContextID/Apply*). Transaction and mutation
// authority live exclusively behind the RuntimeExecutor approval boundary.
func TestUIOwnsNoExecutionEngineTransactions(t *testing.T) {
	root := repoRoot(t)
	for _, path := range uiProductionFiles(t, root) {
		f, fset := parseFile(t, path)
		base := filepath.Base(path)
		for _, method := range []string{"BeginTransaction", "CommitTransaction", "RollbackTransaction"} {
			if sites := findSelectorCalls(f, fset, "m.execEng", method); len(sites) > 0 {
				t.Errorf("architecture: %s must not call m.execEng.%s — the transaction lifecycle is owned by the RuntimeExecutor boundary (line %d)",
					base, method, sites[0].line)
			}
		}
		for _, method := range []string{"SetLedger", "SetContextID", "Apply", "ApplyContext"} {
			if sites := findSelectorCalls(f, fset, "m.execEng.Patches", method); len(sites) > 0 {
				t.Errorf("architecture: %s must not access the UI-owned PatchManager via m.execEng.Patches.%s — mutation belongs to the RuntimeExecutor (line %d)",
					base, method, sites[0].line)
			}
		}
	}
}

// TestHandleBuildRunIsPureIntentFactory pins P0 requirement 1: handleBuildRun
// and its dispatch seam are pure intent producers. They must not invoke the
// provider, touch the execution engine or its PatchManager, write files, or
// fall back to legacy handlers. The dispatch edge must cross exactly one of
// the admitted seams — runRuntimeTaskRequest (RuntimeExecutor) or the
// interactive shell gate — and the amendment entry point must route through
// the same factory.
func TestHandleBuildRunIsPureIntentFactory(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, filepath.Join(root, "internal", "ui", "commands.go"))

	hbr := findFuncDecl(f, "handleBuildRun")
	if hbr == nil {
		t.Fatal("architecture: handleBuildRun must exist as the /build intent factory")
	}
	forbidLegacyExecInFunc(t, f, fset, hbr, "handleBuildRun")

	dispatch := findFuncDecl(f, "dispatchStagedTask")
	if dispatch == nil {
		t.Fatal("architecture: dispatchStagedTask must exist as the single admission-boundary seam")
	}
	forbidLegacyExecInFunc(t, f, fset, dispatch, "dispatchStagedTask")

	begin := findFuncDecl(f, "beginStagedTask")
	if begin == nil {
		t.Fatal("architecture: beginStagedTask must exist as the task-selection/bookkeeping seam")
	}
	forbidLegacyExecInFunc(t, f, fset, begin, "beginStagedTask")

	foundExecutor, foundShellGate := false, false
	ast.Inspect(dispatch, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			switch sel.Sel.Name {
			case "runRuntimeTaskRequest":
				foundExecutor = true
			case "runStagedShellGate":
				foundShellGate = true
			}
		}
		return true
	})
	if !foundExecutor {
		t.Error("architecture: dispatchStagedTask must submit FILE_MUTATE/GIT_ACTION work through runRuntimeTaskRequest (RuntimeExecutor admission)")
	}
	if !foundShellGate {
		t.Error("architecture: dispatchStagedTask must route SHELL_EXEC work through runStagedShellGate (interactive admission gate)")
	}

	amend := findFuncDecl(f, "amendBuildTask")
	if amend == nil {
		t.Fatal("architecture: amendBuildTask must exist")
	}
	forbidLegacyExecInFunc(t, f, fset, amend, "amendBuildTask")
	routesThroughFactory := false
	ast.Inspect(amend, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "handleBuildRun" {
			routesThroughFactory = true
		}
		return true
	})
	if !routesThroughFactory {
		t.Error("architecture: amendBuildTask must re-execute the amended task through handleBuildRun (the admitted intent factory), never a direct mutation path")
	}
}

// TestRuntimeCutoverHasNoLegacyFallbackPath pins P0 requirement 2: the
// cutover dispatch seam never degrades out of the runtime. runtime_cutover.go
// must not reference handleBuildRun (no wholesale mixed-plan fallback), must
// not invoke providers or legacy builders, and must not touch the UI-owned
// execution engine, its PatchManager, or any transaction lifecycle.
func TestRuntimeCutoverHasNoLegacyFallbackPath(t *testing.T) {
	root := repoRoot(t)
	const cutover = "internal/ui/runtime_cutover.go"
	f, fset := parseFile(t, filepath.Join(root, cutover))

	forbidden := map[string]bool{
		"handleBuildRun":      true,
		"streamCmd":           true,
		"proposeBuildPatch":   true,
		"runBuildFastTrack":   true,
		"BeginTransaction":    true,
		"CommitTransaction":   true,
		"RollbackTransaction": true,
	}
	if sites := findCalls(f, fset, forbidden); len(sites) > 0 {
		t.Errorf("architecture: %s must not fall back to legacy execution machinery (found %q at line %d)",
			cutover, firstForbiddenName(f, forbidden), sites[0].line)
	}

	for _, pair := range [][2]string{
		{"m.execEng", "Apply"},
		{"m.execEng", "ApplyContext"},
		{"m.execEng.Patches", "SetLedger"},
		{"m.execEng.Patches", "SetContextID"},
		{"m.provider", "Execute"},
		{"m.provider", "ExecuteStream"},
	} {
		if sites := findSelectorCalls(f, fset, pair[0], pair[1]); len(sites) > 0 {
			t.Errorf("architecture: %s must not call %s.%s — every plan and amendment crosses the RuntimeExecutor admission boundary (line %d)",
				cutover, pair[0], pair[1], sites[0].line)
		}
	}
}

// firstForbiddenName reports which forbidden symbol matched (diagnostic aid).
func firstForbiddenName(f *ast.File, forbidden map[string]bool) string {
	name := "unknown symbol"
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if forbidden[fun.Name] {
				name = fun.Name
				return false
			}
		case *ast.SelectorExpr:
			if forbidden[fun.Sel.Name] {
				name = fun.Sel.Name
				return false
			}
		}
		return true
	})
	return name
}
