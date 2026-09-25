// Phase 11.x Context Boundary architectural guards.
//
// These tests pin the three-domain separation:
//
//	Conversation (unbounded) → Context (compiled, UNTRUSTED) → Execution (frozen)
//
// They prove structurally that the Context Domain can never reach execution
// authority, that an ExecutionSpec cannot self-authorize, and that the UI's
// explicit execution hand-off crosses the context boundary BEFORE the canonical
// runtime executor is driven.
package architecture

import (
	"go/ast"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/contextspec"
)

// contextDomainForbiddenImports are the execution/authority packages the
// Context Domain must never import. If any appears, compiled context could be a
// parallel execution authority.
var contextDomainForbiddenImports = []string{
	"github.com/PizenLabs/izen/internal/execution",
	"github.com/PizenLabs/izen/internal/engine",
	"github.com/PizenLabs/izen/internal/autonomy",
	"github.com/PizenLabs/izen/internal/runtime/autonomy",
	"github.com/PizenLabs/izen/internal/runtime/orchestrator",
	"github.com/PizenLabs/izen/internal/patch",
	"os/exec",
}

// TestG_ContextSpecCannotExecute pins that the Context Domain owns no execution
// authority: no execution imports and no method that applies, writes, shells,
// authorizes or reaches the RuntimeExecutor.
func TestG_ContextSpecCannotExecute(t *testing.T) {
	root := repoRoot(t)
	dir := root + "/internal/contextspec"
	for _, rel := range []string{"types.go", "compiler.go", "store.go", "pipeline.go", "audit.go", "errors.go"} {
		f, _ := parseFile(t, dir+"/"+rel)
		for _, forbidden := range contextDomainForbiddenImports {
			if imports(f)[forbidden] {
				t.Errorf("architecture: internal/contextspec/%s imports %q — compiled context must never own execution authority", rel, forbidden)
			}
		}
	}

	forbiddenMethods := map[string]bool{
		"Execute": true, "Apply": true, "Approve": true, "Reject": true,
		"Mutate": true, "Write": true, "Patch": true, "Shell": true,
		"Authorize": true,
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(contextspec.Pipeline{}), reflect.TypeOf(contextspec.Store{})} {
		for i := 0; i < typ.NumMethod(); i++ {
			name := typ.Method(i).Name
			if forbiddenMethods[name] {
				t.Errorf("architecture: contextspec.%s.%s must not exist — the Context Domain cannot execute", typ.Name(), name)
			}
		}
	}
}

// TestG2_ContextSpecExposesNoExecutionSymbols pins the absence of a mutation /
// execution surface on the compiled context types themselves.
func TestG2_ContextSpecExposesNoExecutionSymbols(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(contextspec.ContextSpec{}), reflect.TypeOf(contextspec.ExecutionSpec{})} {
		forbiddenFields := map[string]bool{"AuthorizedBy": true, "Capabilities": true, "Executor": true, "Patch": true}
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			if forbiddenFields[name] {
				t.Errorf("architecture: %s.%s must not exist — context/execution specs carry no authority", typ.Name(), name)
			}
		}
	}
}

// TestH_ExecutionSpecCannotSelfAuthorize pins that the frozen execution
// contract carries no authorization field and that the Context Domain exposes
// no authorize/grant capability. Authorization stays with the existing
// admission/AuthorizationEngine path.
func TestH_ExecutionSpecCannotSelfAuthorize(t *testing.T) {
	typ := reflect.TypeOf(contextspec.ExecutionSpec{})
	if _, ok := typ.FieldByName("AuthorizedBy"); ok {
		t.Fatal("architecture: ExecutionSpec.AuthorizedBy must not exist — execution intent is not authorization")
	}
	if _, ok := typ.FieldByName("Grant"); ok {
		t.Fatal("architecture: ExecutionSpec must not carry an authorization grant")
	}

	// The Pipeline must expose no authorization/execution entry point.
	forbidden := map[string]bool{"Authorize": true, "Grant": true, "Execute": true, "Approve": true}
	pt := reflect.TypeOf(&contextspec.Pipeline{})
	for i := 0; i < pt.NumMethod(); i++ {
		if forbidden[pt.Method(i).Name] {
			t.Fatalf("architecture: contextspec.Pipeline.%s must not exist — authorization is independent", pt.Method(i).Name)
		}
	}
}

// TestJ_HandoffCrossesContextBeforeCanonicalExecutor pins reachability: the UI
// explicit execution workspace crosses the context boundary (fresh ContextSpec
// + frozen ExecutionSpec) BEFORE it drives the canonical autonomous driver. The
// hand-off is not dead code.
func TestJ_HandoffCrossesContextBeforeCanonicalExecutor(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, root+"/internal/ui/autonomy_route.go")
	fn := findFuncDecl(f, "executeAutonomyWorkspace")
	if fn == nil {
		t.Fatal("architecture: model.executeAutonomyWorkspace must exist")
	}

	handoffLine := -1
	driverLine := -1
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "handoffExecutionContext":
			if handoffLine == -1 {
				handoffLine = fset.Position(call.Pos()).Line
			}
		case "executeAutonomyViaDriver":
			if driverLine == -1 {
				driverLine = fset.Position(call.Pos()).Line
			}
		}
		return true
	})
	if handoffLine == -1 {
		t.Fatal("architecture: executeAutonomyWorkspace must cross the context boundary (handoffExecutionContext) before execution")
	}
	if driverLine == -1 {
		t.Fatal("architecture: executeAutonomyWorkspace must still drive the canonical autonomous executor")
	}
	if handoffLine > driverLine {
		t.Errorf("architecture: context hand-off (line %d) must precede the canonical executor (line %d)", handoffLine, driverLine)
	}
}

// TestJ2_CompositionRootWiresContextPipeline pins that the Context Domain
// Control Plane is constructed exactly once, at the composition root, over the
// existing OCC snapshot mechanism.
func TestJ2_CompositionRootWiresContextPipeline(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(root + "/internal/runtime/compose/compose.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, "contextspec.NewPipeline(") {
		t.Error("architecture: the composition root must construct the Context Pipeline")
	}
	if !strings.Contains(src, "contextpipeline.NewOCCSnapshotPort(") {
		t.Error("architecture: the Context Pipeline must reuse the canonical OCC workspace snapshot mechanism")
	}
	if !strings.Contains(src, "ContextSpecPipeline") {
		t.Error("architecture: the Application must expose the wired Context Pipeline")
	}
}
