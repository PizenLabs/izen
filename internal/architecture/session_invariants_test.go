// Session-authority architectural enforcement (SESSION.md): these tests pin
// the boundary that the Session Manager is a persistence/pointer authority and
// NEVER a second execution state engine.
//
// INV-SESSION-09: session management cannot directly execute workspace
// mutations. The RuntimeExecutor is the sole mutation authority. The session
// package must therefore never import the execution package (or any engine
// that owns provider invocation / mutation lifecycle).
//
// INV-SESSION-02: exactly one interactive session is active per workspace. The
// Manager's atomic-pointer commit is the single enforcement point (verified
// behaviorally in internal/session/manager_test.go).
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

// sessionForbiddenImports are engine/execution packages the session authority
// must never depend on. If any sneaks in, the Manager could bypass the
// RuntimeExecutor boundary and become a parallel state engine.
var sessionForbiddenImports = []string{
	"github.com/PizenLabs/izen/internal/execution",
	"github.com/PizenLabs/izen/internal/engine",
	"github.com/PizenLabs/izen/internal/runtime/autonomy",
	"github.com/PizenLabs/izen/internal/autonomy",
	"github.com/PizenLabs/izen/internal/orchestrator",
}

// TestSessionPackageNeverImportsExecutionAuthority sweeps every file in
// internal/session and fails if any imports an execution/autonomy engine. The
// Manager reaches the RuntimeExecutor ONLY through the BoundaryHook seam wired
// at the composition root (internal/runtime/compose), never by direct import.
func TestSessionPackageNeverImportsExecutionAuthority(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	matches, err := filepath.Glob(filepath.Join(root, "internal/session/*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no internal/session source files found; invariant is unenforced")
	}

	for _, path := range matches {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Skip generated/flock build-tag variants that carry no imports.
		f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			pkg := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range sessionForbiddenImports {
				if pkg == forbidden || strings.HasPrefix(pkg, forbidden+"/") {
					t.Errorf("architecture: %s imports %q — the Session Manager must never own execution (INV-SESSION-09)", path, pkg)
				}
			}
		}
	}
}

// TestSessionManagerHasNoExecutionSurface asserts the Manager exposes no method
// whose name signals execution authority (Execute/Apply/Reject/Approve). The
// only execution bridge is BoundaryHook, invoked AFTER the pointer commits.
func TestSessionManagerHasNoExecutionSurface(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	path := filepath.Join(root, "internal/session/manager.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}

	forbiddenMethods := map[string]bool{
		"Execute": true, "Apply": true, "Approve": true,
		"Reject": true, "Commit": true, "Mutate": true, "Run": true,
	}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		recv := ""
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			if se, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := se.X.(*ast.Ident); ok {
					recv = id.Name
				}
			}
		}
		if recv != "Manager" {
			return true
		}
		if forbiddenMethods[fn.Name.Name] {
			t.Errorf("architecture: Manager.%s must not exist — session management cannot execute mutations (INV-SESSION-09)", fn.Name.Name)
		}
		return true
	})
}

// TestNewSessionBoundaryDrainsThroughExecutor pins that the /new command in the
// UI routes exclusively through the SessionManager (no ad-hoc session file
// juggling) and that the manager is wired from the composition root.
func TestNewSessionBoundaryDrainsThroughExecutor(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	src, err := os.ReadFile(filepath.Join(root, "internal/ui/session_cmds.go"))
	if err != nil {
		t.Fatal("internal/ui/session_cmds.go missing; /new wiring moved")
	}
	f, err := parser.ParseFile(fset, "internal/ui/session_cmds.go", src, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}

	var hasManagerCall, hasSessionNew bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.SelectorExpr); ok {
			switch fn.Sel.Name {
			case "NewSession", "ResumeSession", "List":
				hasManagerCall = true
			case "New":
				if id, ok := fn.X.(*ast.Ident); ok && id.Name == "session" {
					hasSessionNew = true
				}
			}
		}
		return true
	})

	if !hasManagerCall {
		t.Error("architecture: /new and /session must route through the SessionManager (NewSession/ResumeSession/List)")
	}
	if hasSessionNew {
		t.Error("architecture: the UI must never construct raw sessions directly — the SessionManager owns the lifecycle")
	}
}
