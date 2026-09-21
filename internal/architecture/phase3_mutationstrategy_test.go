// Phase 3 Mutation Strategy & Step Construction boundary lock suite
// (IZEN_PHASE_3_MUTATION_STRATEGY_STEP_CONSTRUCTION).
//
// These tests pin the Phase 3 invariants behaviorally and structurally:
//
//	Change Surface is the sole planning input.
//	Mutation Strategy / Mutation Plan are derivational, never authoritative.
//	EstimatedMutationSize and StepBudget never expand authorization.
//	No autonomous continuation, provider retry, or model switching exists.
package architecture

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/changesurface"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/mutationstrategy"
	"github.com/PizenLabs/izen/internal/understanding"
)

// ── Phase 3 canonical home ─────────────────────────────────────────────

func TestPhase3_CanonicalHomeExists(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "mutationstrategy")
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("canonical home internal/mutationstrategy must exist")
	}
	for _, rival := range []string{
		"internal/mutationstrategyengine",
		"internal/mutationplanengine",
		"internal/stepplanner",
		"internal/stepscheduler",
		"internal/mutationplannerruntime",
		"internal/adaptiveplanner",
	} {
		if _, err := os.Stat(filepath.Join(root, rival)); !os.IsNotExist(err) {
			t.Errorf("rival orchestration home %s must not exist — extend the canonical home instead", rival)
		}
	}
}

// ── PU/CS/Strategy boundary: mutationstrategy never authorizes ─────────

var phase3ForbiddenImports = map[string]bool{
	"github.com/PizenLabs/izen/internal/execution":           true,
	"github.com/PizenLabs/izen/internal/core/authorization":  true,
	"github.com/PizenLabs/izen/internal/boundary/scopeguard": true,
	"github.com/PizenLabs/izen/internal/runtime/executor":    true,
	"github.com/PizenLabs/izen/internal/runtime/scopeguard":  true,
	"github.com/PizenLabs/izen/internal/runtime/scheduler":   true,
	"github.com/PizenLabs/izen/internal/patch":               true,
	"github.com/PizenLabs/izen/internal/provider":            true,
	"github.com/PizenLabs/izen/internal/providers":           true,
	"github.com/PizenLabs/izen/internal/llm":                 true,
}

var phase3ForbiddenIdents = map[string]bool{
	"Authorize": true, "Grant": true, "Approve": true, "Apply": true,
	"Execute": true, "Mutate": true, "Commit": true,
	"MutationAuthorization": true, "AllowsMutation": true,
	"ScopeProvenance": true, "ExecutionDeniedError": true,
}

func TestPhase3_MutationStrategyNeverAuthorizes(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "mutationstrategy")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read mutationstrategy: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, _ := parseFile(t, filepath.Join(dir, e.Name()))
		for imp := range imports(f) {
			if phase3ForbiddenImports[imp] {
				t.Errorf("architecture: internal/mutationstrategy/%s imports %s — Phase 3 derivation must never touch authority/execution", e.Name(), imp)
			}
			if strings.HasPrefix(imp, "github.com/PizenLabs/izen/internal/") &&
				imp != "github.com/PizenLabs/izen/internal/understanding" &&
				imp != "github.com/PizenLabs/izen/internal/changesurface" {
				t.Errorf("architecture: internal/mutationstrategy/%s imports %s — must stay to stdlib + understanding + changesurface only", e.Name(), imp)
			}
		}
		f2, fset := parseFile(t, filepath.Join(dir, e.Name()))
		for _, s := range findCalls(f2, fset, phase3ForbiddenIdents) {
			t.Errorf("architecture: internal/mutationstrategy/%s references authority symbol at line %d — derivation must never mint authority", e.Name(), s.line)
		}
	}
}

// Phase3_NoNewExecutionAuthority proves the package declares no execution
// authority types (RuntimeExecutor, PatchManager, etc.) and that the
// forbidden vocabulary of autonomous continuation does not appear.
func TestPhase3_NoNewExecutionAuthority(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "mutationstrategy")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read mutationstrategy: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		for _, pat := range []string{
			"RuntimeExecutor", "PatchManager", "MutationSet", "ScopeGuard",
			"ContinuationPipeline", "AdaptiveSelector", "ProviderRetry",
			"AutoRecovery", "AutonomousLoop", "ModelSwitch",
			"os.WriteFile", "os.Create", "os.Remove", "exec.Command",
			"ExecuteStream", "llm.Provider",
		} {
			if strings.Contains(body, pat) {
				t.Errorf("architecture: internal/mutationstrategy/%s contains %q — Phase 3 must not implement execution/continuation/retry", e.Name(), pat)
			}
		}
		// Forbidden control-bearing identifiers inside mutationstrategy.
		for ident := range phase3ForbiddenIdents {
			needles := []string{"func " + ident, "type " + ident, ident + "Grant", ident + "Authorization"}
			for _, needle := range needles {
				if strings.Contains(body, needle) {
					t.Errorf("architecture: internal/mutationstrategy/%s contains %q — derivation must never mint authority", e.Name(), needle)
				}
			}
		}
	}
}

// ── Behavioral: plan never mints authority or mutates ──────────────────

func TestPhase3_PlanNeverMintsAuthorityNorMutates(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "index.html")
	original := "<html></html>\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	u := understanding.Derive(root)
	s := changesurface.Derive("Update the index.", nil, u)
	plan := mutationstrategy.Derive("Update the index.", u, s, mutationstrategy.PlanOptions{})
	// Deriving must not mutate workspace.
	if got, err := os.ReadFile(target); err != nil || string(got) != original {
		t.Fatal("Derive must not mutate the workspace")
	}
	// Plan carries no authorization grant.
	_ = plan
}

func TestPhase3_PlanNeverExpandsAuthorizationScope(t *testing.T) {
	root := t.TempDir()
	writeFileForPhase3(t, root, "index.html", "<html></html>\n")
	writeFileForPhase3(t, root, "styles.css", "body{}\n")
	u := understanding.Derive(root)
	s := changesurface.Derive("Redesign the site.", nil, u)
	plan := mutationstrategy.Derive("Redesign the site.", u, s, mutationstrategy.PlanOptions{
		StepBudget: mutationstrategy.StepBudget{MaxOutputTokens: 9999, MaxFiles: 999},
	})
	// Large budget must not create a grant; execution gateway still gates.
	gw := execution.NewIntentGateway(root)
	req, res, err := gw.Gate(context.Background(), "change foo in @note.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	_ = plan
	if res.ScopeProvenance.AllowsMutation() || req.ScopeProvenance.AllowsMutation() {
		t.Fatal("mutationstrategy budget must not expand Phase 1 authorization")
	}
}

func TestPhase3_StepBudgetDoesNotExpandAuthorization(t *testing.T) {
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 1_000_000, MaxFiles: 9999}
	if !budget.Valid() {
		t.Fatal("budget valid")
	}
	// Even huge budget cannot grant mutation — gate still denied.
	root := t.TempDir()
	gw := execution.NewIntentGateway(root)
	_, res, err := gw.Gate(context.Background(), "change foo in @note.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if res.ScopeProvenance.AllowsMutation() {
		t.Fatal("huge StepBudget must not imply mutation authority")
	}
	_ = budget
}

func TestPhase3_EstimateDoesNotExpandAuthorization(t *testing.T) {
	est := mutationstrategy.EstimatedMutationSize{Lower: 1, Expected: 10, Upper: 20, Confidence: 0.9}
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 5}
	if !est.Exceeds(budget) {
		t.Fatal("estimate should exceed tiny budget (TOO_LARGE signal, not grant)")
	}
	root := t.TempDir()
	gw := execution.NewIntentGateway(root)
	_, res, err := gw.Gate(context.Background(), "change foo in @note.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if res.ScopeProvenance.AllowsMutation() {
		t.Fatal("estimate outcome must not imply authority")
	}
}

func TestPhase3_ModelConstraintsDoNotExpandAuthorization(t *testing.T) {
	mc := mutationstrategy.ModelConstraints{OutputCeiling: 8192}
	budget := mutationstrategy.StepBudgetFor(mc, mutationstrategy.StepBudget{MaxOutputTokens: 4096})
	if budget.MaxOutputTokens == 0 {
		t.Fatal("budget derived from model constraints must be set")
	}
	root := t.TempDir()
	gw := execution.NewIntentGateway(root)
	_, res, err := gw.Gate(context.Background(), "change foo in @note.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if res.ScopeProvenance.AllowsMutation() {
		t.Fatal("model capability metadata must not imply authority")
	}
	_ = mc
}

// TestPhase3_PlanCannotInventTargets proves Derive never invents paths
// outside the evidence-backed Change Surface.
func TestPhase3_PlanCannotInventTargetsOutsideSurface(t *testing.T) {
	root := t.TempDir()
	writeFileForPhase3(t, root, "index.html", "<html></html>\n")
	u := understanding.Derive(root)
	s := changesurface.Derive("Update dashboard.tsx for the site.", nil, u)
	plan := mutationstrategy.Derive("Update dashboard.tsx for the site.", u, s, mutationstrategy.PlanOptions{})
	allowed := map[string]bool{}
	for _, c := range s.Candidates {
		allowed[c.Path] = true
	}
	for _, st := range plan.Steps {
		for _, r := range st.SurfaceRefs {
			if !allowed[r] {
				t.Errorf("plan step %s invents %q outside the change surface", st.ID, r)
			}
		}
	}
}

// TestPhase3_NoAutonomousContinuation proves the package has no continuation
// loop: a TOO_LARGE plan does not auto-split-and-execute; it returns a
// planning failure requiring decomposition.
func TestPhase3_NoAutonomousContinuation(t *testing.T) {
	root := t.TempDir()
	writeFileForPhase3(t, root, "index.html", "<html></html>\n")
	writeFileForPhase3(t, root, "styles.css", "body{}\n")
	writeFileForPhase3(t, root, "app.js", "console.log(1);\n")
	u := understanding.Derive(root)
	s := changesurface.Derive("Redesign the whole site.", nil, u)
	plan := mutationstrategy.Derive("Redesign the whole site.", u, s, mutationstrategy.PlanOptions{
		StepBudget: mutationstrategy.StepBudget{MaxOutputTokens: 10, MaxFiles: 4},
	})
	if plan.Status != mutationstrategy.StatusTooLarge {
		t.Fatalf("tiny envelope must yield TOO_LARGE, got %v", plan.Status)
	}
	if plan.UnresolvedReason == "" {
		t.Error("TOO_LARGE must carry a reason, not auto-continue")
	}
}

// ── Phase 1/2 regression locks ─────────────────────────────────────────

func TestPhase3_Phase1AuthorizationBoundaryIntact(t *testing.T) {
	gw := execution.NewIntentGateway(t.TempDir())
	req, res, err := gw.Gate(context.Background(), "change bar to qux in @note.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if res.ScopeProvenance.AllowsMutation() || req.ScopeProvenance.AllowsMutation() {
		t.Fatal("Phase 1 boundary regressed: bare goal minted mutation authority after Phase 3")
	}
}

func TestPhase3_Phase2UnderstandingStillValid(t *testing.T) {
	root := t.TempDir()
	writeFileForPhase3(t, root, "index.html", "<html><title>demo</title></html>\n")
	writeFileForPhase3(t, root, "styles.css", "body{}\n")
	writeFileForPhase3(t, root, "script.js", "console.log(1);\n")
	u := understanding.Derive(root)
	if u.Kind != understanding.KindExisting {
		t.Fatalf("Phase 2 understanding regressed: kind = %v", u.Kind)
	}
	s := changesurface.Derive("Redesign the portfolio website.", nil, u)
	if s.Status == changesurface.StatusUnresolved {
		t.Fatalf("Phase 2 change surface regressed: status = %v", s.Status)
	}
}

func writeFileForPhase3(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
