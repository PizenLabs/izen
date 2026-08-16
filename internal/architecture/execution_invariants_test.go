// Phase 5 architectural enforcement: these tests prevent regression of the
// execution-authority boundaries with AST/dependency-level assertions.
//
// The Runtime is the source of truth. The UI is a projection. Every execution
// crosses the IntentGateway, drives a runtime-owned ExecutionGraph, and the
// graph — and only the graph — generates the canonical lifecycle events.
package architecture

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lifecycleConstructors are the canonical runtime lifecycle event constructors.
// They may ONLY be invoked by the runtime-owned execution graph — the single
// generator of lifecycle events.
var lifecycleConstructors = []string{
	"NewExecutionStarted",
	"NewStrategySelected",
	"NewTargetResolved",
	"NewContextPrepared",
	"NewModelInvoked",
	"NewProviderResponse",
	"NewArtifactProduced",
	"NewApprovalRequired",
	"NewMutationStarted",
	"NewMutationCompleted",
	"NewVerificationCompleted",
	"NewExecutionFinished",
}

// callSite is one discovered function-call site.
type callSite struct {
	file string
	line int
}

// renderExpr renders an identifier/selector chain expression to its dotted text
// (e.g. "m.gateway", "execution.NewRuntimeExecutor").
func renderExpr(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return renderExpr(x.X) + "." + x.Sel.Name
	default:
		return ""
	}
}

// findCalls scans a parsed file for direct calls to the named functions, either
// package-level (`NewExecutionStarted(...)`) or selector (`execution.Foo(...)`).
func findCalls(f *ast.File, fset *token.FileSet, names map[string]bool) []callSite {
	var sites []callSite
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var fn string
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			fn = fun.Name
		case *ast.SelectorExpr:
			fn = fun.Sel.Name
		default:
			return true
		}
		if !names[fn] {
			return true
		}
		pos := fset.Position(call.Pos())
		sites = append(sites, callSite{file: pos.Filename, line: pos.Line})
		return true
	})
	return sites
}

// findSelectorCalls scans a parsed file for `recv.Method(...)` calls where the
// receiver renders to recv (e.g. "m.gateway", "m.executor") and the method
// matches.
func findSelectorCalls(f *ast.File, fset *token.FileSet, recv, method string) []callSite {
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
		if sel.Sel.Name != method || renderExpr(sel.X) != recv {
			return true
		}
		pos := fset.Position(call.Pos())
		sites = append(sites, callSite{file: pos.Filename, line: pos.Line})
		return true
	})
	return sites
}

// TestLifecycleEventsGeneratedOnlyFromGraph pins rule 5: every canonical
// lifecycle event is generated ONLY by graph transitions. The constructors may
// be invoked nowhere except internal/execution/graph/graph.go (the definitions
// live in events.go and are not call sites).
func TestLifecycleEventsGeneratedOnlyFromGraph(t *testing.T) {
	root := repoRoot(t)
	names := make(map[string]bool, len(lifecycleConstructors))
	for _, n := range lifecycleConstructors {
		names[n] = true
	}
	const graphEmitter = "internal/execution/graph/graph.go"

	var violations []callSite
	for _, rel := range goFilesUnder(root) {
		if rel == graphEmitter {
			continue
		}
		f, fset := parseFile(t, filepath.Join(root, rel))
		violations = append(violations, findCalls(f, fset, names)...)
	}
	if len(violations) > 0 {
		for _, v := range violations {
			rel := filepath.ToSlash(strings.TrimPrefix(v.file, root+string(filepath.Separator)))
			t.Errorf("architecture: lifecycle event constructed outside the execution graph at %s:%d — events must be generated from graph transitions", rel, v.line)
		}
	}

	// The graph must actually emit every lifecycle event type (the invariant is
	// enforced, not vacuous).
	raw, err := os.ReadFile(filepath.Join(root, graphEmitter))
	if err != nil {
		t.Fatalf("architecture: read %s: %v", graphEmitter, err)
	}
	for _, n := range lifecycleConstructors {
		if !strings.Contains(string(raw), n) {
			t.Errorf("architecture: %s does not emit %s — lifecycle event not generated from the graph", graphEmitter, n)
		}
	}
}

// TestRuntimeExecutorSingleCompositionBinding pins rule 7 (no duplicate
// execution paths): the RuntimeExecutor authority is constructed exactly once
// in production, in the composition root.
func TestRuntimeExecutorSingleCompositionBinding(t *testing.T) {
	root := repoRoot(t)
	const compositionRoot = "internal/runtime/compose/compose.go"
	names := map[string]bool{"NewRuntimeExecutor": true}

	files := goFilesUnder(root)
	sites := make([]callSite, 0, len(files))
	for _, rel := range files {
		f, fset := parseFile(t, filepath.Join(root, rel))
		sites = append(sites, findCalls(f, fset, names)...)
	}
	if len(sites) == 0 {
		t.Fatal("architecture: no NewRuntimeExecutor call sites found; invariant is unenforced")
	}
	for _, s := range sites {
		rel := filepath.ToSlash(strings.TrimPrefix(s.file, root+string(filepath.Separator)))
		if rel != compositionRoot {
			t.Errorf("architecture: NewRuntimeExecutor must be bound only in %s, found at %s:%d", compositionRoot, rel, s.line)
		}
	}
}

// TestUICannotCallProviderOnExecutionPath pins rule 1 on the gated execution
// path: gateway.go routes executions through the RuntimeExecutor and must never
// invoke the provider directly. It must submit via gateway.Gate →
// executor.Execute.
func TestUICannotCallProviderOnExecutionPath(t *testing.T) {
	root := repoRoot(t)
	const gatedPath = "internal/ui/gateway.go"
	f, fset := parseFile(t, filepath.Join(root, gatedPath))

	for _, pair := range [][2]string{
		{"m.provider", "Execute"},
		{"m.provider", "ExecuteStream"},
		{"provider", "Execute"},
		{"provider", "ExecuteStream"},
	} {
		if sites := findSelectorCalls(f, fset, pair[0], pair[1]); len(sites) > 0 {
			t.Errorf("architecture: %s must not call %s.%s — provider invocation belongs to the RuntimeExecutor (found at line %d)",
				gatedPath, pair[0], pair[1], sites[0].line)
		}
	}

	// The gated path MUST cross the gateway and the executor.
	gate := findSelectorCalls(f, fset, "m.gateway", "Gate")
	exec := findSelectorCalls(f, fset, "m.executor", "Execute")
	if len(gate) == 0 {
		t.Error("architecture: gateway.go must submit through m.gateway.Gate (every user action crosses the IntentGateway)")
	}
	if len(exec) == 0 {
		t.Error("architecture: gateway.go must submit through m.executor.Execute")
	}
}

// TestUICannotMutateWorkspaceOnExecutionPath pins rule 2: the gated execution
// path must never mutate the workspace itself — no PatchManager, no MutationSet
// ownership, no direct filesystem writes. Mutation is owned by the executor's
// approval boundary.
func TestUICannotMutateWorkspaceOnExecutionPath(t *testing.T) {
	root := repoRoot(t)
	const gatedPath = "internal/ui/gateway.go"
	f, fset := parseFile(t, filepath.Join(root, gatedPath))

	for _, pair := range [][2]string{
		{"m.execEng", "ApplyContext"},
		{"m.execEng", "Apply"},
		{"pm", "ApplyContext"},
		{"pm", "Apply"},
	} {
		if sites := findSelectorCalls(f, fset, pair[0], pair[1]); len(sites) > 0 {
			t.Errorf("architecture: %s must not call %s.%s — mutation belongs to the RuntimeExecutor approval boundary (line %d)",
				gatedPath, pair[0], pair[1], sites[0].line)
		}
	}
	for _, n := range []string{"NewPatchManager", "NewMutationSet", "NewTxFS"} {
		names := map[string]bool{n: true}
		if sites := findCalls(f, fset, names); len(sites) > 0 {
			t.Errorf("architecture: %s must not construct %s — mutation ownership is runtime-owned (line %d)", gatedPath, n, sites[0].line)
		}
	}
}

// TestEveryUserActionCrossesIntentGateway pins rule 6 statically: the gated UI
// path must gate BEFORE it executes — strategy selection is unconditional and
// precedes any executor submission.
func TestEveryUserActionCrossesIntentGateway(t *testing.T) {
	root := repoRoot(t)
	const gatedPath = "internal/ui/gateway.go"
	f, fset := parseFile(t, filepath.Join(root, gatedPath))

	gate := findSelectorCalls(f, fset, "m.gateway", "Gate")
	exec := findSelectorCalls(f, fset, "m.executor", "Execute")
	if len(gate) == 0 || len(exec) == 0 {
		t.Fatalf("architecture: %s must gate then execute (gate=%d exec=%d)", gatedPath, len(gate), len(exec))
	}
	if gate[0].line > exec[0].line {
		t.Errorf("architecture: %s executes before gating (Gate at line %d after Execute at line %d) — the gateway must resolve the strategy first",
			gatedPath, gate[0].line, exec[0].line)
	}
}

// TestNoDuplicateHotfixExecutionPath pins rule 7 for $hot: the message-content
// handler must not dispatch $hot to the legacy provider-path builder. Every
// $hot execution crosses the unified gateway (dispatchDirectives →
// runHotExecution → runGatedLine).
func TestNoDuplicateHotfixExecutionPath(t *testing.T) {
	root := repoRoot(t)
	const messageHandler = "internal/ui/commands.go"
	raw, err := os.ReadFile(filepath.Join(root, messageHandler))
	if err != nil {
		t.Fatalf("architecture: read %s: %v", messageHandler, err)
	}
	src := string(raw)
	// The legacy fast-track dispatched "$hot" to runBuildCmd — a second,
	// UI-owned execution path. It must not exist.
	if strings.Contains(src, "runBuildCmd(hotContent)") {
		t.Error("architecture: commands.go must not dispatch $hot to runBuildCmd — every $hot execution crosses the unified IntentGateway (rule 7)")
	}
	if strings.Contains(src, "HasPrefix(strings.TrimSpace(content), \"$hot\")") {
		t.Error("architecture: commands.go must not fast-track $hot through a legacy provider path (rule 7)")
	}
}
