// Phase 2 Project Understanding & Change Surface boundary lock suite
// (IZEN_PHASE_2_PROJECT_UNDERSTANDING_CHANGE_SURFACE).
//
// These tests pin the Phase 2 invariants behaviorally and structurally:
//
//	Understanding may constrain and inform planning, but it never
//	authorizes execution.
//	Change Surface describes where a change may be relevant; it does not
//	decide what may be mutated.
//
// Style follows the Phase 0/1 lock suites: AST structural sweeps over
// production sources (resistant to whitespace churn) plus black-box
// behavioral proofs against the exported understanding/changesurface API.
package architecture

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/changesurface"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/understanding"
)

// ── Phase 2 canonical homes ──────────────────────────────────────────────

// TestPhase2_CanonicalHomesExist pins the single semantic home per concept:
// Project Understanding lives in internal/understanding, Change Surface in
// internal/changesurface. No rival manager/engine/runtime types may exist.
func TestPhase2_CanonicalHomesExist(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal/understanding", "internal/changesurface"} {
		fi, err := os.Stat(filepath.Join(root, dir))
		if err != nil || !fi.IsDir() {
			t.Fatalf("canonical home %s must exist", dir)
		}
	}
	// No second orchestration layer: rival homes must not exist.
	for _, rival := range []string{
		"internal/changesurfacemanager", "internal/changesurfaceengine",
		"internal/projectunderstandingengine", "internal/projectcontextruntime",
	} {
		if _, err := os.Stat(filepath.Join(root, rival)); !os.IsNotExist(err) {
			t.Errorf("rival orchestration home %s must not exist — extend existing concepts instead", rival)
		}
	}
}

// ── PU boundary: understanding never authorizes ──────────────────────────

// forbiddenAuthorityIdents are symbols that mint, expand, or exercise
// execution authority. Neither Phase 2 package may reference them.
var phase2ForbiddenIdents = map[string]bool{
	"Authorize": true, "Grant": true, "Approve": true, "Apply": true,
	"Execute": true, "Mutate": true, "Commit": true,
	"NewRuntimeExecutor": true, "ScopeProvenance": true,
	"AllowsMutation": true, "MutationAuthorization": true,
}

// TestPhase2_UnderstandingNeverAuthorizes pins the PU authority boundary
// structurally: internal/understanding must import only the standard
// library and must never reference authority-minting symbols.
func TestPhase2_UnderstandingNeverAuthorizes(t *testing.T) {
	root := repoRoot(t)
	assertNoForbiddenImports(t, root, "internal/understanding", map[string]bool{
		"github.com/PizenLabs/izen/internal/execution":           true,
		"github.com/PizenLabs/izen/internal/core/authorization":  true,
		"github.com/PizenLabs/izen/internal/boundary/scopeguard": true,
		"github.com/PizenLabs/izen/internal/runtime/executor":    true,
		"github.com/PizenLabs/izen/internal/runtime/scopeguard":  true,
		"github.com/PizenLabs/izen/internal/patch":               true,
	})
	assertNoForbiddenIdents(t, root, "internal/understanding", phase2ForbiddenIdents)
}

// TestPhase2_ChangeSurfaceNeverAuthorizes pins the CS authority boundary
// structurally: internal/changesurface may import only stdlib plus the
// canonical understanding home, and must never reference authority or
// mutation-operation symbols.
func TestPhase2_ChangeSurfaceNeverAuthorizes(t *testing.T) {
	root := repoRoot(t)
	assertNoForbiddenImports(t, root, "internal/changesurface", map[string]bool{
		"github.com/PizenLabs/izen/internal/execution":           true,
		"github.com/PizenLabs/izen/internal/core/authorization":  true,
		"github.com/PizenLabs/izen/internal/boundary/scopeguard": true,
		"github.com/PizenLabs/izen/internal/runtime/executor":    true,
		"github.com/PizenLabs/izen/internal/runtime/scopeguard":  true,
		"github.com/PizenLabs/izen/internal/patch":               true,
		"github.com/PizenLabs/izen/internal/planner/scope":       true,
	})
	assertNoForbiddenIdents(t, root, "internal/changesurface", map[string]bool{
		"Authorize": true, "Grant": true, "Approve": true, "Apply": true,
		"Execute": true, "Mutate": true, "Commit": true,
		"Operation": true, "EstimatedMutationSize": true, "TokenBudget": true,
		"NewRuntimeExecutor": true, "ScopeProvenance": true,
		"AllowsMutation": true, "MutationAuthorization": true,
	})
}

// ── AUTH-REGRESSION-01/02: no new mutation authority ─────────────────────

// TestPhase2_NoNewMutationAuthority proves behaviorally that neither new
// package can produce a workspace mutation or an approvable artifact:
// the only types they expose are descriptive (understanding, candidates,
// evidence), and deriving both over a real workspace leaves the tree
// untouched with zero approval surface involved.
func TestPhase2_NoNewMutationAuthority(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "index.html")
	original := "<html></html>\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "styles.css"), []byte("body{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := understanding.Derive(root)
	s := changesurface.Derive("Redesign the portfolio website.", nil, u)
	if len(s.Candidates) == 0 {
		t.Fatalf("surface must resolve over the fixture-like workspace, got status %v reason %q", s.Status, s.UnresolvedReason)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != original {
		t.Fatal("deriving understanding + surface must not mutate the workspace")
	}
}

// ── Target Resolution ≠ Change Surface (CS-04 behavioral) ────────────────

// TestPhase2_TargetResolutionStaysDistinct proves the execution target
// layer is untouched by Phase 2: a non-existent non-template target is
// still a deterministic failure at the target layer (never silently
// expanded), while the surface layer independently refuses to invent it.
func TestPhase2_TargetResolutionStaysDistinct(t *testing.T) {
	if _, err := execution.ResolveTargetSet([]string{"dashboard.tsx"}, func(string) bool { return false }); err == nil {
		t.Fatal("execution target resolution must still fail closed on unevidenced targets")
	}
	u := understanding.Derive(t.TempDir()) // empty → GREENFIELD
	s := changesurface.Derive("Update the dashboard.", []string{"@dashboard.tsx"}, u)
	for _, c := range s.Candidates {
		if c.Path == "dashboard.tsx" {
			t.Fatalf("change surface invented an unevidenced target: %+v", c)
		}
	}
}

// ── AUTH-REGRESSION-03/04: Phase 1 boundary intact ───────────────────────

// TestPhase2_Phase1BoundaryIntact re-asserts the Phase 1 gateway contract
// from the Phase 2 suite: a bare mutation goal still compiles to
// read-only ScopeNone, so adding understanding/surface created no second
// authority path.
func TestPhase2_Phase1BoundaryIntact(t *testing.T) {
	gw := execution.NewIntentGateway(t.TempDir())
	req, res, err := gw.Gate(context.Background(), "change bar to qux in @note.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if res.ScopeProvenance.AllowsMutation() || req.ScopeProvenance.AllowsMutation() {
		t.Fatal("Phase 1 boundary regressed: bare goal minted mutation authority")
	}
}

// ── structural helpers ───────────────────────────────────────────────────

func assertNoForbiddenImports(t *testing.T, root, pkg string, forbidden map[string]bool) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(pkg))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", pkg, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, _ := parseFile(t, filepath.Join(dir, e.Name()))
		for imp := range imports(f) {
			if forbidden[imp] {
				t.Errorf("architecture: %s/%s imports %s — Phase 2 derivation must never touch authority/execution", pkg, e.Name(), imp)
			}
			if strings.HasPrefix(imp, "github.com/PizenLabs/izen/internal/") &&
				imp != "github.com/PizenLabs/izen/internal/understanding" {
				// understanding must be stdlib-only; changesurface may add
				// only the understanding home.
				if pkg == "internal/understanding" {
					t.Errorf("architecture: %s/%s imports %s — understanding must stay dependency-free (stdlib only)", pkg, e.Name(), imp)
				} else if imp != "github.com/PizenLabs/izen/internal/understanding" {
					t.Errorf("architecture: %s/%s imports %s — change surface may only reuse the understanding home", pkg, e.Name(), imp)
				}
			}
		}
	}
}

func assertNoForbiddenIdents(t *testing.T, root, pkg string, forbidden map[string]bool) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(pkg))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", pkg, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, fset := parseFile(t, filepath.Join(dir, e.Name()))
		for _, s := range findCalls(f, fset, forbidden) {
			t.Errorf("architecture: %s/%s references authority symbol at line %d — derivation must never mint authority", pkg, e.Name(), s.line)
		}
		// Type declarations carrying authority semantics are also banned
		// (checked textually below against the operation vocabulary).
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		for ident := range forbidden {
			// Match `type X Ident` / `Ident ` declarations conservatively:
			// any `type ...Operation...` or const block naming an operation
			// vocabulary word is a Mutation Strategy leak.
			for _, pat := range []string{"type " + ident, ident + " Operation", "MutationStrategy", "EstimatedMutationSize", "StepBudget", "ContinuationPipeline"} {
				if strings.Contains(body, pat) {
					t.Errorf("architecture: %s/%s contains %q — Phase 2 must not implement mutation strategy/budget/continuation", pkg, e.Name(), pat)
				}
			}
		}
	}
}
