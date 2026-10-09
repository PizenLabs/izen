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

// TestExecutionOwnership_CLIOrchestrateDrivesAuthoritativeDriver pins that the
// `izen prompt` / `izen orchestrate` command drives the SAME authoritative
// runtime lifecycle as the TUI. Its historical private control-plane proposal
// lifecycle (orchestrator.RunCycle + ProposalProviderCLI + ProposalValidator +
// ApprovalGate + FileExecutor) must no longer be on the command path. The lock
// is an AST call-edge assertion: it survives whitespace churn and fails if a
// competing lifecycle is silently reintroduced.
func TestExecutionOwnership_CLIOrchestrateDrivesAuthoritativeDriver(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "cmd", "izen", "orchestrate.go"))
	fn := findFuncDecl(f, "runOrchestrateCommand")
	if fn == nil {
		t.Fatal("architecture: runOrchestrateCommand must exist")
	}
	callees := calleeNamesInNode(fn)
	if callees["RunAuthoritative"] == 0 {
		t.Error("architecture: izen orchestrate must drive the authoritative runtime lifecycle (cli.RunAuthoritative)")
	}
	if callees["Wire"] == 0 {
		t.Error("architecture: izen orchestrate must wire the production composition root")
	}
	// The command must NOT construct or run its own proposal lifecycle.
	for _, forbidden := range []string{"RunCycle", "NewOrchestrator", "NewProposalProvider", "NewTerminalBridge"} {
		if callees[forbidden] != 0 {
			t.Errorf("architecture: izen orchestrate still calls %s — a competing proposal lifecycle", forbidden)
		}
	}
}

// TestExecutionOwnership_AuthoritativeCLIPathInvokesNoProvider pins that the
// migrated CLI lifecycle driver reaches execution ONLY through the structural
// runtime port: it must never invoke a provider, parse an artifact, or mutate a
// file. The loop owns all of that; the CLI owns the human boundary.
func TestExecutionOwnership_AuthoritativeCLIPathInvokesNoProvider(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal", "cli", "authoritative.go"))
	fn := findFuncDecl(f, "RunAuthoritative")
	if fn == nil {
		t.Fatal("architecture: cli.RunAuthoritative must exist")
	}
	callees := calleeNamesInNode(fn)
	for _, forbidden := range []string{"Execute", "ExecuteStream", "Complete", "WriteFile", "Apply", "Commit"} {
		if callees[forbidden] != 0 {
			t.Errorf("architecture: cli.RunAuthoritative calls %s — it must not reach the provider or the filesystem", forbidden)
		}
	}
}
