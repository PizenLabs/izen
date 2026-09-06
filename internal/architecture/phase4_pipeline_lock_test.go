package architecture

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestPhase4PipelineLock enforces the Phase 4 single mutation authority pipe:
//
//	Intent/Command → RuntimeExecutor → CapabilityGuard.Evaluate() → Substrate
//	                                     (Allowed) → Substrate.Execute() → ports
//	                                     (Denied)  → ErrCapabilityDenied / ErrBudgetExceeded
//
// Structural guarantees:
//   - Substrate (internal/runtime/substrate) is the ONLY runtime pipeline
//     component that imports concrete OS capability ports (os/exec, os.WriteFile).
//   - RuntimeExecutor invokes CapabilityGuard.Evaluate before Substrate.ExecuteUnit.
//   - Resource Budget metering exists via BudgetTracker.
//   - Substrate exposes ExecuteUnit with ResourceBudget enforcement.
func TestPhase4PipelineLock(t *testing.T) {
	root := repoRoot(t)

	// ── 1. Substrate isolation: substrate must import os/exec and perform file writes ──
	subDir := filepath.Join(root, "internal", "runtime", "substrate")
	subFiles := goFilesUnder(subDir)
	if len(subFiles) == 0 {
		t.Fatal("architecture: no Go files under internal/runtime/substrate")
	}
	hasExecImport, hasWrite := false, false
	for _, rel := range subFiles {
		f, _ := parseFile(t, filepath.Join(subDir, rel))
		for p := range imports(f) {
			if p == "os/exec" {
				hasExecImport = true
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "os" && sel.Sel.Name == "WriteFile" {
				hasWrite = true
			}
			return true
		})
	}
	if !hasExecImport {
		t.Error("architecture: internal/runtime/substrate must import os/exec — it is the sole side-effect authority")
	}
	if !hasWrite {
		t.Error("architecture: internal/runtime/substrate must contain os.WriteFile — it owns filesystem mutations")
	}

	// ── 2. Executor must NOT directly import os/exec or os.WriteFile ──
	execDir := filepath.Join(root, "internal", "runtime", "executor")
	execFiles := goFilesUnder(execDir)
	if len(execFiles) == 0 {
		t.Fatal("architecture: no Go files under internal/runtime/executor")
	}
	for _, rel := range execFiles {
		f, fset := parseFile(t, filepath.Join(execDir, rel))
		for p := range imports(f) {
			if p == "os/exec" {
				t.Errorf("architecture: internal/runtime/executor/%s must not import os/exec — mutations belong to Substrate (line %d)", rel, fset.Position(f.Package).Line)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "os" && sel.Sel.Name == "WriteFile" {
				t.Errorf("architecture: internal/runtime/executor/%s must not call os.WriteFile — mutations belong to Substrate", rel)
			}
			return true
		})
	}

	// ── 3. RuntimeExecutor must call guard.Evaluate before substrate.ExecuteUnit ──
	execFile := filepath.Join(root, "internal", "runtime", "executor", "executor.go")
	f, _ := parseFile(t, execFile)
	decl := findFuncDecl(f, "Execute")
	if decl == nil {
		t.Fatal("architecture: RuntimeExecutor.Execute must exist in internal/runtime/executor/executor.go")
	}
	var evalPos, execPos token.Pos
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "Evaluate" {
			if evalPos == token.NoPos {
				evalPos = call.Pos()
			}
		}
		if sel.Sel.Name == "ExecuteUnit" {
			if execPos == token.NoPos {
				execPos = call.Pos()
			}
		}
		return true
	})
	if evalPos == token.NoPos {
		t.Fatal("architecture: RuntimeExecutor.Execute must invoke guard.Evaluate (CapabilityGuard.Evaluate)")
	}
	if execPos == token.NoPos {
		t.Fatal("architecture: RuntimeExecutor.Execute must delegate to substrate.ExecuteUnit")
	}
	if evalPos > execPos {
		t.Fatal("architecture: RuntimeExecutor must invoke CapabilityGuard.Evaluate BEFORE substrate.ExecuteUnit — the 6-clause formula binds every mutation")
	}

	// ── 4. BudgetTracker must exist in executor/budget.go ──
	budgetFile := filepath.Join(root, "internal", "runtime", "executor", "budget.go")
	bf, _ := parseFile(t, budgetFile)
	foundTracker := false
	for _, decl := range bf.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == "BudgetTracker" {
				foundTracker = true
			}
		}
	}
	if !foundTracker {
		t.Fatal("architecture: BudgetTracker must be defined in internal/runtime/executor/budget.go")
	}

	// ── 5. Substrate must expose ExecuteUnit with ResourceBudget enforcement ──
	substrateFile := filepath.Join(root, "internal", "runtime", "substrate", "substrate.go")
	sf, _ := parseFile(t, substrateFile)
	foundSubstrate := false
	foundExecuteUnit := false
	for _, decl := range sf.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == "Substrate" {
						if _, ok := ts.Type.(*ast.StructType); ok {
							foundSubstrate = true
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Name.Name == "ExecuteUnit" {
				foundExecuteUnit = true
				// Verify first param is context.Context and second is domain.ExecutionUnit
				if d.Type.Params != nil && len(d.Type.Params.List) >= 2 {
					hasCtx, hasUnit := false, false
					for _, p := range d.Type.Params.List {
						tstr := typeStringForPhase4(p.Type)
						if strings.Contains(tstr, "Context") {
							hasCtx = true
						}
						if strings.Contains(tstr, "ExecutionUnit") {
							hasUnit = true
						}
					}
					if !hasCtx || !hasUnit {
						t.Errorf("architecture: Substrate.ExecuteUnit must accept (context.Context, domain.ExecutionUnit)")
					}
				}
			}
		}
	}
	if !foundSubstrate {
		t.Fatal("architecture: Substrate struct must be defined in internal/runtime/substrate/substrate.go")
	}
	if !foundExecuteUnit {
		t.Fatal("architecture: Substrate.ExecuteUnit(ctx, unit domain.ExecutionUnit) must be defined")
	}

	// ── 6. Guard must be CapabilityGuard interface ──
	execTypes, _ := parseFile(t, execFile)
	foundGuardField := false
	for _, decl := range execTypes.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "RuntimeExecutor" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, field := range st.Fields.List {
				tstr := typeStringForPhase4(field.Type)
				if strings.Contains(tstr, "CapabilityGuard") {
					foundGuardField = true
				}
			}
		}
	}
	if !foundGuardField {
		t.Error("architecture: RuntimeExecutor must embed authorization.CapabilityGuard")
	}
}

func typeStringForPhase4(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return typeStringForPhase4(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return typeStringForPhase4(v.X)
	case *ast.ArrayType:
		return typeStringForPhase4(v.Elt)
	case *ast.MapType:
		return typeStringForPhase4(v.Key) + ":" + typeStringForPhase4(v.Value)
	default:
		return ""
	}
}
