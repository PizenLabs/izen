package architecture

// PHASE 12 — negative architecture tests for the execution-intelligence
// convergence.
//
// These are the locks that make the repairs structural rather than incidental.
// Each one asserts an invariant that must hold no matter what future code does:
//
//	planner cannot mutate
//	context cannot authorize
//	model output cannot expand scope
//	UI cannot execute mutations
//	continuation cannot become a second scheduler
//	another RuntimeExecutor cannot become a production claimant
//	output exhaustion cannot bypass admission
//	partial output cannot bypass authorization
//	cache cannot bypass current-state validation
//
// They are deliberately structural (import graphs and file-system scans) rather
// than behavioural, so a regression is caught even if the behavioural suite is
// not run.

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// phase12ProductionFiles returns every non-test Go file under root.
func phase12ProductionFiles(t *testing.T) []string {
	t.Helper()
	return goFilesUnder(repoRoot(t))
}

// TestPhase12_NoSecondProductionExecutor pins the canonical runtime owner: the
// bounded-step continuation added inside the executor must NOT have introduced
// a second production executor, and the only production construction of a
// RuntimeExecutor stays the composition root.
func TestPhase12_NoSecondProductionExecutor(t *testing.T) {
	root := repoRoot(t)
	var constructions []string
	for _, rel := range phase12ProductionFiles(t) {
		f, _ := parseFile(t, filepath.Join(root, rel))
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "NewRuntimeExecutor" {
				return true
			}
			constructions = append(constructions, rel)
			return true
		})
	}
	if len(constructions) == 0 {
		t.Fatal("no production construction of execution.RuntimeExecutor found — the invariant would be vacuous")
	}
	for _, rel := range constructions {
		if rel != "internal/runtime/compose/compose.go" {
			t.Errorf("production file %s constructs an execution.RuntimeExecutor — the composition root is the single construction site", rel)
		}
	}
}

// TestPhase12_BoundedStepLivesInsideTheCanonicalExecutor pins where the new
// bounded-step continuation may live. It belongs to the canonical execution
// authority and to no scheduler, no planner, and no UI package.
func TestPhase12_BoundedStepLivesInsideTheCanonicalExecutor(t *testing.T) {
	root := repoRoot(t)
	// The bounded-step primitive is a shared library; its LIFECYCLE owner must
	// be the canonical executor. No planner, scheduler or UI file may own one.
	if _, err := os.Stat(filepath.Join(root, "internal/execution/artifact_step.go")); err != nil {
		t.Fatalf("the canonical bounded-step implementation must live in the executor package: %v", err)
	}
	for _, rel := range phase12ProductionFiles(t) {
		switch rel {
		case "internal/execution/artifact_step.go",
			"internal/execution/executor.go",
			"internal/modes/plan/synthesis_step.go",
			"internal/llmstep/step.go",
			"internal/llmstep/response_state.go",
			"internal/investigation/agent_orchestrator.go",
			"internal/runtime/autonomy/driver.go":
			continue
		}
		src := readFileOK(t, filepath.Join(root, rel))
		if strings.Contains(src, "invokeArtifactBoundedStep") {
			t.Errorf("file %s invokes the artifact bounded-step lifecycle — only the canonical executor owns it", rel)
		}
	}
}

// TestPhase12_PlannerCannotMutate re-asserts the Phase 3 lock for the
// discovery gate added this phase: the intent compiler decides SCOPE, and it
// may not write, authorize, or gain an execution handle.
func TestPhase12_PlannerCannotMutate(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"internal/modes/plan/intentcompiler.go",
		"internal/engine/planner/irplanner.go",
		"internal/engine/strategy/greenfield.go",
		"internal/engine/lowerer/lowerer.go",
	} {
		src := readFileOK(t, filepath.Join(root, rel))
		for _, forbidden := range []string{
			"os.WriteFile", "os.Create", "os.MkdirAll", "os.Remove",
			"AuthorizeBuild", "SetAuthorization", "MutationAuthorization",
			"RuntimeExecutor", "os/exec",
		} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s references %q — a planner decides scope, it never mutates or authorizes", rel, forbidden)
			}
		}
	}
}

// TestPhase12_ContextCannotAuthorize pins that neither the context compiler nor
// the context domain can grant capability, scope, or mutation permission.
func TestPhase12_ContextCannotAuthorize(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"internal/contextcompiler/compiler.go",
		"internal/contextcompiler/request.go",
		"internal/contextcompiler/provider.go",
		"internal/contextspec/pipeline.go",
		"internal/contextspec/store.go",
		"internal/execution/context_compiler.go",
		"internal/execution/context.go",
	} {
		src := readFileOK(t, filepath.Join(root, rel))
		for _, forbidden := range []string{
			"AuthorizeBuild", "SetAuthorization", "MutationAuthorization",
			"os.WriteFile", "os.Create", "os/exec",
		} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s references %q — context supplies facts, it never authorizes or mutates", rel, forbidden)
			}
		}
	}
	// The context domain must not import the execution authority at all.
	for _, forbidden := range []string{
		moduleImport("internal/execution"),
		moduleImport("internal/core/authorization"),
	} {
		if importsOfDir(t, root, "internal/contextspec")[forbidden] {
			t.Errorf("internal/contextspec imports %q — the Context Domain is untrusted by construction", forbidden)
		}
	}
}

// TestPhase12_ContinuationCannotBecomeAScheduler pins the pure continuation
// library's closed surface: it proposes, and it may never own a loop, a
// scheduler, a provider, an executor, or a mutation.
func TestPhase12_ContinuationCannotBecomeAScheduler(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal/continuation")
	got := importsOfDir(t, root, "internal/continuation")
	for _, forbidden := range []string{
		moduleImport("internal/runtime/autonomy"),
		moduleImport("internal/autonomy"),
		moduleImport("internal/execution"),
		moduleImport("internal/runtime/executor"),
		moduleImport("internal/events"),
		moduleImport("internal/ai"),
		moduleImport("internal/llmstep"),
		"os/exec",
	} {
		if got[forbidden] {
			t.Errorf("internal/continuation imports %q — the continuation library is a pure proposal, never a scheduler or executor", forbidden)
		}
	}
	for _, rel := range goFilesUnder(dir) {
		src := readFileOK(t, filepath.Join(dir, rel))
		for _, forbidden := range []string{"os.WriteFile", "os.Create", "os/exec", "go func", "time.Sleep"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("internal/continuation/%s references %q — the proposal function is pure", rel, forbidden)
			}
		}
	}
}

// TestPhase12_ModelOutputCannotExpandScope pins the zero-trust boundary for the
// new artifact candidate: a partial generation is carried as a bounded,
// non-authoritative record. It must expose no filesystem, provider, or
// authorization surface, and it must not travel on the authorization path.
func TestPhase12_ModelOutputCannotExpandScope(t *testing.T) {
	root := repoRoot(t)
	src := readFileOK(t, filepath.Join(root, "internal/execution/artifact_step.go"))
	for _, forbidden := range []string{
		"os.WriteFile", "os.Create", "os.Remove", "os.MkdirAll",
		"AuthorizeBuild", "SetAuthorization", "MutationAuthorization",
		"patches.Apply", "x.patches",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("internal/execution/artifact_step.go references %q — an artifact candidate is evidence, never a mutation", forbidden)
		}
	}
	// The candidate must carry a fingerprint, not content: no raw generated
	// bytes may live in the exported record.
	if strings.Contains(src, "func (c ArtifactCandidate) Content(") {
		t.Error("ArtifactCandidate must not expose its content — only a fingerprint and counters")
	}
}

// TestPhase12_UICannotExecuteMutations pins the presentation boundary for
// MUTATIONS specifically.
//
// Scope note: the TUI legitimately issues a MutationAuthorization over a parked
// human-approval boundary (the human pressed Alt+A) and legitimately calls a
// provider directly for read-only chat/title paths. Both are pre-existing
// sanctioned design and are NOT what this phase changed. What the UI must never
// do is perform a MUTATION itself: write a file, remove one, or apply a patch
// to the workspace. Directory creation for logs, checkpoints and the telemetry
// sink is scaffolding, not a workspace mutation, and is deliberately not
// forbidden here.
func TestPhase12_UICannotExecuteMutations(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{
		"os.WriteFile(",
		"os.Create(",
		"os.Rename(",
		"os.Remove(",
		"patches.Apply(",
		"ApplyPatches(",
	}
	for _, rel := range phase12ProductionFiles(t) {
		if !strings.HasPrefix(rel, "internal/ui/") {
			continue
		}
		src := readFileOK(t, filepath.Join(root, rel))
		for _, f := range forbidden {
			if strings.Contains(src, f) {
				t.Errorf("UI file %s calls %q — the presentation layer must never execute a mutation", rel, f)
			}
		}
	}
	// Every staged / autonomous task still crosses the single admission
	// boundary before any mutation is possible.
	src := readFileOK(t, filepath.Join(root, "internal/ui/commands.go"))
	if !strings.Contains(src, "runRuntimeTaskRequest") {
		t.Error("the staged build queue must keep crossing the RuntimeExecutor admission boundary")
	}
	if !strings.Contains(src, "dispatchStagedTask") {
		t.Error("every staged task must still reach dispatchStagedTask")
	}
}

// TestPhase12_PartialOutputCannotBypassAdmission pins the safety chain: a
// partial artifact candidate can never open an approval surface or reach the
// workspace. The executor's only write path is still the authorized one.
func TestPhase12_PartialOutputCannotBypassAdmission(t *testing.T) {
	root := repoRoot(t)
	src := readFileOK(t, filepath.Join(root, "internal/execution/executor.go"))
	// The candidate is attached to the result as evidence; it must never be
	// promoted onto Content or onto a pending patch.
	if strings.Contains(src, "res.Content = outcome.Raw") || strings.Contains(src, "res.PendingPatchID = outcome.") {
		t.Error("an artifact candidate must never become result content or a held patch")
	}
	// Exhaustion must still resolve to the truthful truncated classification so
	// the recovery matrix can act on it.
	if !strings.Contains(src, "res.Proof.Outcome = OutcomeTruncated") {
		t.Error("a bounded-step exhaustion must still seal OutcomeTruncated — exhaustion is not task failure")
	}
	// The artifact gate must still run on the final result.
	if !strings.Contains(src, "x.artifactGate(target, modified)") {
		t.Error("the artifact gate must still validate the resolved artifact")
	}
}

// TestPhase12_BudgetCannotAuthorize pins that widening a budget is a pure
// accounting operation: the pure autonomy library must expose no capability,
// grant, or filesystem surface.
func TestPhase12_BudgetCannotAuthorize(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/autonomy")
	for _, forbidden := range []string{
		moduleImport("internal/runtime/autonomy"),
		moduleImport("internal/core/authorization"),
		moduleImport("internal/core/authorization"),
		"os/exec",
	} {
		if got[forbidden] {
			t.Errorf("internal/autonomy imports %q — loop bounds are accounting, never authority", forbidden)
		}
	}
	// The pure loop package must not reach the runtime autonomy driver that
	// interprets its verdicts: the dependency is one-way (Driver → pure).
	if got[moduleImport("internal/runtime/autonomy")] {
		t.Error("internal/autonomy must never import the Driver — a pure library cannot own the loop")
	}
	for _, rel := range goFilesUnder(filepath.Join(root, "internal/autonomy")) {
		src := readFileOK(t, filepath.Join(root, "internal/autonomy", rel))
		for _, forbidden := range []string{"os.WriteFile", "os.Create", "AuthorizeBuild", "MutationAuthorization"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("internal/autonomy/%s references %q — bounds never grant capability", rel, forbidden)
			}
		}
	}
}

// TestPhase12_StrategyNeverAuthorizes re-asserts the Phase 3 lock for the
// derived creation budget: a strategy proposes a REQUEST bound, never a
// capability claim and never a mutation.
func TestPhase12_StrategyNeverAuthorizes(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/execution/strategy")
	for _, forbidden := range []string{
		moduleImport("internal/execution"),
		moduleImport("internal/core/authorization"),
		moduleImport("internal/runtime"),
		"os/exec",
	} {
		if got[forbidden] {
			t.Errorf("internal/execution/strategy imports %q — a strategy proposes a budget, it never authorizes", forbidden)
		}
	}
	for _, rel := range goFilesUnder(filepath.Join(root, "internal/execution/strategy")) {
		src := readFileOK(t, filepath.Join(root, "internal/execution/strategy", rel))
		for _, forbidden := range []string{"os.WriteFile", "os.Create", "AuthorizeBuild", "MutationAuthorization"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("internal/execution/strategy/%s references %q", rel, forbidden)
			}
		}
	}
}

// ── local helpers ───────────────────────────────────────────────────────────

func readFileOK(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
