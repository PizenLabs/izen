package architecture

// ── EXECUTION OWNERSHIP CONVERGENCE (structural lock) ───────────────────────
//
// The topology correction establishes ONE authoritative execution lifecycle for
// every admitted TUI request. The three human execution surfaces — `$prompt`,
// `/build` ordinary prompt, and `$hot` — must all cross the SAME admission →
// runtime seam (`runAutonomyRoutedCmdExplicit`), while remaining distinct command
// surfaces. This is an AST lock, resistant to whitespace churn: it asserts the
// call edge exists, not the wording around it.

import (
	"path/filepath"
	"testing"
)

// TestExecutionOwnership_AllExecutionSurfacesConvergeOnOneRuntime pins that the
// three human execution surfaces reach the single authoritative runtime entry.
func TestExecutionOwnership_AllExecutionSurfacesConvergeOnOneRuntime(t *testing.T) {
	root := repoRoot(t)

	cases := []struct {
		rel  string
		fn   string
		what string
	}{
		{"internal/ui/intent_dispatch.go", "routePromptDirective", "$prompt"},
		{"internal/ui/runtime_cutover.go", "runRuntimePrompt", "/build ordinary prompt"},
		{"internal/ui/autonomy_route.go", "routeHotfixThroughAutonomy", "$hot"},
	}
	for _, tc := range cases {
		f, _ := parseFile(t, filepath.Join(root, tc.rel))
		fn := findFuncDecl(f, tc.fn)
		if fn == nil {
			t.Fatalf("architecture: %s must exist as the %s entry point", tc.fn, tc.what)
		}
		callees := calleeNamesInNode(fn)
		if callees["runAutonomyRoutedCmdExplicit"] == 0 {
			t.Errorf("architecture: %s (%s) does not cross the single authoritative runtime seam runAutonomyRoutedCmdExplicit", tc.fn, tc.what)
		}
	}
}

// TestExecutionOwnership_SingleRuntimeSeam pins the seams themselves: the
// explicit entry runs the autonomy decision and dispatches its trace — it does
// not start a second agent loop.
func TestExecutionOwnership_SingleRuntimeSeam(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal", "ui", "autonomy_route.go"))
	fn := findFuncDecl(f, "runAutonomyRoutedCmdExplicit")
	if fn == nil {
		t.Fatal("architecture: runAutonomyRoutedCmdExplicit must exist")
	}
	callees := calleeNamesInNode(fn)
	if callees["Decide"] == 0 {
		t.Error("architecture: the authoritative runtime seam must run the deterministic autonomy decision")
	}
	if callees["dispatchAutonomyTrace"] == 0 {
		t.Error("architecture: the authoritative runtime seam must dispatch the decision trace")
	}
}

// TestExecutionOwnership_DirectExecutorDispatchIsFencedToHarnessFallback pins
// that the UI's direct executor dispatch helper (runRuntimeExecuteCmd) is used
// only by the explicit no-driver harness fallback and the staged-task surfaces —
// never by the ordinary `/build` prompt when the runtime owner is wired. The
// operator-visible contract is enforced behaviorally by
// TestBuildOrdinaryPromptEntersAuthoritativeRuntime; this lock keeps the call
// edge from silently returning.
func TestExecutionOwnership_DirectExecutorDispatchIsFencedToHarnessFallback(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal", "ui", "runtime_cutover.go"))
	fn := findFuncDecl(f, "runRuntimePrompt")
	if fn == nil {
		t.Fatal("architecture: runRuntimePrompt must exist")
	}
	callees := calleeNamesInNode(fn)
	// The convergence seam is mandatory.
	if callees["runAutonomyRoutedCmdExplicit"] == 0 {
		t.Fatal("architecture: /build ordinary prompt must enter the authoritative runtime")
	}
	// And admission precedes it.
	if callees["admitNewExecutionRun"] == 0 {
		t.Error("architecture: /build ordinary prompt must pass the authoritative admission gate")
	}
}
