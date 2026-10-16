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
	"strings"
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
// runtime lifecycle as the TUI, through the shared CLI seam. Its historical
// private control-plane proposal lifecycle (orchestrator.RunCycle +
// ProposalProviderCLI + ProposalValidator + ApprovalGate + FileExecutor) must
// no longer be on the command path. The lock is an AST call-edge assertion: it
// survives whitespace churn and fails if a competing lifecycle is silently
// reintroduced.
func TestExecutionOwnership_CLIOrchestrateDrivesAuthoritativeDriver(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "cmd", "izen", "orchestrate.go"))
	fn := findFuncDecl(f, "runOrchestrateCommand")
	if fn == nil {
		t.Fatal("architecture: runOrchestrateCommand must exist")
	}
	callees := calleeNamesInNode(fn)
	// The command crosses the shared execution seam (runExecRequestAs), which
	// itself drives runAuthoritativeCommand. Both edges are asserted so the lock
	// cannot be satisfied by a seam that stopped reaching the runtime.
	if callees["runExecRequestAs"] == 0 && callees["runExecRequest"] == 0 {
		t.Error("architecture: izen orchestrate must cross the shared authoritative execution seam (runExecRequestAs)")
	}
	assertAuthoritativeExecSeam(t, root)
	// The command must NOT construct or run its own proposal lifecycle or its
	// own app.Pipeline.
	for _, forbidden := range []string{"RunCycle", "NewOrchestrator", "NewProposalProvider", "NewTerminalBridge", "NewPipeline"} {
		if callees[forbidden] != 0 {
			t.Errorf("architecture: izen orchestrate still calls %s — a competing proposal lifecycle", forbidden)
		}
	}
}

// TestExecutionOwnership_CLIRunDrivesAuthoritativeDriver pins the Phase 3
// migration: `izen run` no longer owns the internal/app.Pipeline execution
// lifecycle. It parses its arguments and hands the objective to the SAME
// shared authoritative runtime lifecycle as every migrated surface. The AST
// lock fails if the retired pipeline loop silently returns to the command path.
func TestExecutionOwnership_CLIRunDrivesAuthoritativeDriver(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "cmd", "izen", "runtime.go"))
	fn := findFuncDecl(f, "runRuntimeCommand")
	if fn == nil {
		t.Fatal("architecture: runRuntimeCommand must exist")
	}
	callees := calleeNamesInNode(fn)
	if callees["runExecRequest"] == 0 && callees["runExecRequestAs"] == 0 {
		t.Error("architecture: izen run must cross the shared authoritative execution seam (runExecRequest)")
	}
	assertAuthoritativeExecSeam(t, root)
	// The retired owners: the app.Pipeline loop and the control-plane proposal
	// lifecycle must not be reachable from the command.
	for _, forbidden := range []string{"NewPipeline", "RunCycle", "NewOrchestrator", "NewProposalProvider", "NewTerminalBridge"} {
		if callees[forbidden] != 0 {
			t.Errorf("architecture: izen run still calls %s — a competing execution lifecycle", forbidden)
		}
	}
}

// assertAuthoritativeExecSeam pins the shared execution wrapper to the one
// authoritative lifecycle: runExecRequestAs must drive runAuthoritativeCommand
// and own no competing loop.
func assertAuthoritativeExecSeam(t *testing.T, root string) {
	t.Helper()
	f, _ := parseFile(t, filepath.Join(root, "cmd", "izen", "authoritative.go"))
	fn := findFuncDecl(f, "runExecRequestAs")
	if fn == nil {
		t.Fatal("architecture: runExecRequestAs must exist in cmd/izen/authoritative.go")
	}
	callees := calleeNamesInNode(fn)
	if callees["runAuthoritativeCommand"] == 0 {
		t.Error("architecture: the shared execution seam must drive runAuthoritativeCommand")
	}
	for _, forbidden := range []string{"NewPipeline", "RunCycle", "NewOrchestrator", "NewProposalProvider", "NewTerminalBridge", "NewDriver"} {
		if callees[forbidden] != 0 {
			t.Errorf("architecture: the shared execution seam constructs %s — a competing runtime/loop", forbidden)
		}
	}
}

// TestExecutionOwnership_AuthoritativeCommandWiresRuntimeAndDriver pins the
// shared CLI seam itself: it must cross the production composition root
// (compose.Wire) and drive the authoritative driver through the structural port
// (cli.RunAuthoritative), and it must not construct the retired alternative
// lifecycles. This is what makes the convergence single-sourced rather than
// duplicated per command.
func TestExecutionOwnership_AuthoritativeCommandWiresRuntimeAndDriver(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "cmd", "izen", "authoritative.go"))
	fn := findFuncDecl(f, "runAuthoritativeCommand")
	if fn == nil {
		t.Fatal("architecture: runAuthoritativeCommand must exist in cmd/izen/authoritative.go")
	}
	callees := calleeNamesInNode(fn)
	if callees["Wire"] == 0 {
		t.Error("architecture: runAuthoritativeCommand must wire the production composition root (compose.Wire)")
	}
	if callees["RunAuthoritative"] == 0 {
		t.Error("architecture: runAuthoritativeCommand must drive the authoritative runtime (cli.RunAuthoritative)")
	}
	for _, forbidden := range []string{"NewPipeline", "RunCycle", "NewOrchestrator", "NewProposalProvider", "NewTerminalBridge", "NewDriver"} {
		if callees[forbidden] != 0 {
			t.Errorf("architecture: runAuthoritativeCommand constructs %s — a competing runtime/loop (the authoritative Driver is built by compose.Wire)", forbidden)
		}
	}
}

// TestExecutionOwnership_CLIProductionFilesDoNotImportApp pins that no
// production cmd/izen file imports internal/app. That package owns the retired
// app.Pipeline; a production import is the strongest signal that the competing
// lifecycle is being wired back into the CLI. (Test files may still import it to
// exercise the retained pipeline library directly.)
func TestExecutionOwnership_CLIProductionFilesDoNotImportApp(t *testing.T) {
	root := repoRoot(t)
	const appImport = "github.com/PizenLabs/izen/internal/app"
	for _, rel := range goFilesUnder(root) {
		if !strings.HasPrefix(rel, "cmd/izen/") {
			continue
		}
		f, _ := parseFile(t, filepath.Join(root, rel))
		if imports(f)[appImport] {
			t.Errorf("architecture: production file %s imports internal/app — the retired app.Pipeline lifecycle must not return to the CLI command path (Phase 3)", rel)
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
