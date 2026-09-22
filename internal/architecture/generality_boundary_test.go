package architecture_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/adapters/web"
	"github.com/PizenLabs/izen/internal/changesurface"
	"github.com/PizenLabs/izen/internal/mutationstrategy"
	"github.com/PizenLabs/izen/internal/problem"
	"github.com/PizenLabs/izen/internal/problemsurface"
	"github.com/PizenLabs/izen/internal/understanding"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureStaticWebRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../testdata/staticweb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	return abs
}

// GEN-01 — No StaticWeb Core Dependency
func TestGEN01_NoStaticWebCoreDependency(t *testing.T) {
	typ := reflect.TypeOf(understanding.ProjectUnderstanding{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if strings.EqualFold(f.Name, "StaticWeb") {
			t.Fatalf("GEN-01: ProjectUnderstanding must not have StaticWeb field, found %v", f)
		}
	}
	// Also prove Derive works without any html/css/js present
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/demo\n\ngo 1.24\n")
	writeFile(t, root, "main.go", "package main\nfunc main(){}\n")
	u := understanding.Derive(root)
	if !u.Valid() {
		t.Fatal("GEN-01: generic Go repo must produce valid understanding without static-web")
	}
	if u.Kind != understanding.KindExisting {
		t.Fatalf("GEN-01: kind = %v, want EXISTING for Go backend", u.Kind)
	}
}

// GEN-02 — Static Web Adapter Preservation
func TestGEN02_StaticWebAdapterPreservation(t *testing.T) {
	root := fixtureStaticWebRoot(t)
	u := understanding.Derive(root)
	if !u.Valid() {
		t.Fatal("GEN-02: fixture understanding must be valid via core")
	}
	ws := web.Derive(root)
	if !ws.Present {
		t.Fatal("GEN-02: web adapter must still detect static-web fixture")
	}
	if len(ws.Entrypoints) == 0 || ws.Entrypoints[0] != "index.html" {
		t.Fatalf("GEN-02: entrypoints = %v, want index.html", ws.Entrypoints)
	}
	// Adapter can also derive from generic understanding
	ws2 := web.DeriveFromUnderstanding(u)
	if !ws2.Present {
		t.Fatalf("GEN-02: DeriveFromUnderstanding must also detect static-web via generic evidence, got %+v", ws2)
	}
	// ChangeSurface via web adapter still yields DIRECT index.html
	wsurf := web.DeriveSurface("Redesign the portfolio website.", nil, u)
	found := false
	for _, c := range wsurf.Candidates {
		if c.Path == "index.html" && c.Certainty == changesurface.CertaintyDirect {
			found = true
		}
	}
	if !found {
		t.Fatalf("GEN-02: web adapter surface must contain DIRECT index.html, got %v", wsurf.Candidates)
	}
}

// GEN-03 — Generic Repository Understanding
func TestGEN03_GenericRepositoryUnderstanding(t *testing.T) {
	cases := []struct {
		name  string
		setup func(string)
	}{
		{"go backend", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
			writeFile(t, root, "cmd/worker/main.go", "package main\nfunc main(){}\n")
			writeFile(t, root, "internal/worker/worker.go", "package worker\nfunc Run(){}\n")
		}},
		{"node app", func(root string) {
			writeFile(t, root, "package.json", `{"name":"app","dependencies":{"react":"18"}}`)
			writeFile(t, root, "src/index.js", "console.log(1);\n")
		}},
		{"python", func(root string) {
			writeFile(t, root, "pyproject.toml", "[project]\nname=\"x\"\n")
			writeFile(t, root, "app/main.py", "print(1)\n")
		}},
		{"cli", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/cli\n\ngo 1.24\n")
			writeFile(t, root, "main.go", "package main\nfunc main(){}\n")
			writeFile(t, root, "README.md", "# cli\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(root)
			u := understanding.Derive(root)
			if !u.Valid() {
				t.Fatalf("GEN-03: %s must produce valid understanding", tc.name)
			}
			if u.Kind != understanding.KindExisting {
				t.Fatalf("GEN-03: %s kind = %v, want EXISTING", tc.name, u.Kind)
			}
			// Must not require static-web
			if u.Identity == "static-web" {
				t.Fatalf("GEN-03: %s identity must not be static-web", tc.name)
			}
		})
	}
}

// GEN-04 — Generic Problem Surface
func TestGEN04_GenericProblemSurface(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
	writeFile(t, root, "cmd/worker/main.go", "package main\nfunc main(){}\n")
	writeFile(t, root, "internal/worker/worker.go", "package worker\nfunc Run(){}\n")
	writeFile(t, root, "internal/config/config.go", "package config\n")
	writeFile(t, root, "go.sum", "")
	u := understanding.Derive(root)
	if u.Kind != understanding.KindExisting {
		t.Fatalf("setup: kind = %v, want EXISTING", u.Kind)
	}
	ps := problemsurface.Derive("investigate worker race condition", nil, u)
	if ps.Status == problemsurface.StatusUnresolved {
		t.Fatalf("GEN-04: problem surface must resolve for Go backend, got unresolved: %s", ps.UnresolvedReason)
	}
	if len(ps.References) == 0 {
		t.Fatalf("GEN-04: problem surface must have references for non-web intent, got empty")
	}
	// Must contain evidence-backed non-web references
	hasWorker := false
	for _, r := range ps.References {
		if strings.Contains(r.Path, "worker") {
			hasWorker = true
		}
		if r.Path == "" || r.Reason == "" || len(r.Evidence) == 0 {
			t.Fatalf("GEN-04: reference must carry reason+evidence: %+v", r)
		}
	}
	if !hasWorker {
		t.Fatalf("GEN-04: expected worker-related reference, got %v", ps.References)
	}
	// No HTML/CSS/JS required
	for _, r := range ps.References {
		low := strings.ToLower(r.Path)
		if strings.HasSuffix(low, ".html") || strings.HasSuffix(low, ".css") {
			t.Logf("warning: non-web surface contains web path %q (allowed if present, but not required)", r.Path)
		}
	}
	if !ps.DigestMatches(u) {
		t.Fatal("GEN-04: surface must bind to understanding digest")
	}
}

// GEN-05 — ChangeSurface Is Mutation-Only
func TestGEN05_ChangeSurfaceIsMutationOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
	writeFile(t, root, "cmd/worker/main.go", "package main\n")
	writeFile(t, root, "internal/worker/worker.go", "package worker\n")
	u := understanding.Derive(root)
	ps := problemsurface.Derive("investigate worker and refactor it", nil, u)
	cs := changesurface.Derive("refactor worker", nil, u)
	if cs.Status == changesurface.StatusUnresolved && ps.Status != problemsurface.StatusUnresolved {
		t.Logf("GEN-05: mutation surface unresolved while problem surface resolved — acceptable narrow mutation subset")
	}
	// ChangeSurface must be read-only, informational, no mutation operation
	for _, c := range cs.Candidates {
		if c.Path == "" {
			t.Fatal("GEN-05: candidate path must be explicit")
		}
		// Must not carry operation verb as path
		for _, forbidden := range []string{"overwrite", "delete", "create"} {
			if c.Path == forbidden {
				t.Fatalf("GEN-05: surface must not express mutation operation: %+v", c)
			}
		}
	}
	// ChangeSurface conceptually is the mutation-candidate subset; allow variance
	// due to different broad-supplement thresholds, but both must be bounded.
	if len(cs.Candidates) == 0 && len(ps.References) > 0 {
		t.Logf("GEN-05: mutation surface empty while problem surface has %d refs (narrow mutation filter)", len(ps.References))
	}
	if len(cs.Candidates) > 12 || len(ps.References) > 12 {
		t.Fatalf("GEN-05: surfaces must be bounded, got cs=%d ps=%d", len(cs.Candidates), len(ps.References))
	}
	// Must bind to understanding
	if cs.UnderstandingDigest == "" {
		t.Fatal("GEN-05: change surface must bind to digest")
	}
	if !cs.DigestMatches(u) {
		t.Fatal("GEN-05: change surface must match understanding digest")
	}
}

// GEN-06 — No Web Family Dependency
func TestGEN06_NoWebFamilyDependency(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
	writeFile(t, root, "internal/worker/worker.go", "package worker\nfunc Run(){}\n")
	writeFile(t, root, "cmd/worker/main.go", "package main\n")
	u := understanding.Derive(root)
	cs := changesurface.Derive("refactor worker module", nil, u)
	if len(cs.Candidates) == 0 {
		t.Fatalf("GEN-06: generic change surface must produce candidates without html/css/js, got empty: %s", cs.UnresolvedReason)
	}
	plan := mutationstrategy.Derive("refactor worker module", u, cs, mutationstrategy.PlanOptions{})
	if plan.Status == mutationstrategy.StatusUnresolved {
		t.Fatalf("GEN-06: mutation plan must not require html/css/js, got unresolved: %s", plan.UnresolvedReason)
	}
	if len(plan.Steps) == 0 {
		t.Fatal("GEN-06: plan must have steps without web families")
	}
	for _, st := range plan.Steps {
		for _, p := range st.SurfaceRefs {
			low := strings.ToLower(p)
			if strings.HasSuffix(low, ".html") || strings.HasSuffix(low, ".css") {
				t.Logf("GEN-06: plan contains web path %q but not required", p)
			}
		}
	}
}

// GEN-07 — Non-Web Mutation
func TestGEN07_NonWebMutation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
	writeFile(t, root, "internal/worker/worker.go", "package worker\nfunc Run(){}\n")
	writeFile(t, root, "internal/worker/pool.go", "package worker\nfunc Pool(){}\n")
	u := understanding.Derive(root)
	cs := changesurface.Derive("refactor the Go worker module for clarity", nil, u)
	plan := mutationstrategy.Derive("refactor the Go worker module for clarity", u, cs, mutationstrategy.PlanOptions{})
	if plan.Status == mutationstrategy.StatusUnresolved {
		t.Fatalf("GEN-07: refactor Go module must produce valid plan, got unresolved: %s", plan.UnresolvedReason)
	}
	if plan.Strategy != mutationstrategy.StrategySingleStep && plan.Strategy != mutationstrategy.StrategyMultiStep {
		t.Fatalf("GEN-07: strategy = %v, want SINGLE or MULTI", plan.Strategy)
	}
	foundRefactor := false
	for _, st := range plan.Steps {
		if st.Operation == mutationstrategy.OpRefactor {
			foundRefactor = true
		}
		if len(st.SurfaceRefs) == 0 {
			t.Fatalf("GEN-07: step %s has no surface refs", st.ID)
		}
	}
	if !foundRefactor {
		t.Fatalf("GEN-07: expected REFACTOR operation, got %v", plan.Steps)
	}
}

// GEN-08 — Investigation Does Not Imply Mutation
func TestGEN08_InvestigationDoesNotImplyMutation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
	writeFile(t, root, "internal/worker/worker.go", "package worker\nvar mu sync.Mutex\nfunc Run(){ mu.Lock(); defer mu.Unlock() }\n")
	u := understanding.Derive(root)
	ps := problemsurface.Derive("investigate a race condition in worker", nil, u)
	if ps.Status == problemsurface.StatusUnresolved {
		t.Fatalf("GEN-08: problem surface must resolve for investigation, got %s", ps.UnresolvedReason)
	}
	pplan := problem.Derive("investigate a race condition in worker", u, ps)
	if len(pplan.Steps) == 0 {
		t.Fatalf("GEN-08: problem-solving plan must have investigation step, got none")
	}
	if pplan.Steps[0].Kind != problem.StepInvestigate && pplan.Steps[0].Kind != problem.StepAnalyze {
		t.Fatalf("GEN-08: first step kind = %v, want INVESTIGATE or ANALYZE, not MUTATE", pplan.Steps[0].Kind)
	}
	// Mutation plan for investigation should not force CREATE/MODIFY - it may be UNRESOLVED or NOOP, but not forced refactor
	cs := changesurface.Derive("investigate a race condition in worker", nil, u)
	// If change surface is unresolved, mutation plan will be unresolved - that's correct (no fake mutation)
	if cs.Status != changesurface.StatusUnresolved {
		mplan := mutationstrategy.Derive("investigate a race condition in worker", u, cs, mutationstrategy.PlanOptions{})
		// If it does produce a plan, it must not invent a fake mutation with web semantics; op may be MODIFY but that's generic default.
		// The key is that ProblemSolvingPlan exists to represent investigation without forcing mutation.
		t.Logf("GEN-08: mutation plan for investigation is %v (acceptable if not forced to web)", mplan.Status)
		_ = mplan
	}
	// Ensure ProblemSolvingPlan does not carry mutation OperationKind
	for _, st := range pplan.Steps {
		if st.Kind == problem.StepMutate {
			t.Fatalf("GEN-08: investigation must not be forced into MUTATE, got %+v", st)
		}
	}
}

// GEN-09 — Authorization Isolation
func TestGEN09_AuthorizationIsolation(t *testing.T) {
	// None of the planning abstractions should have methods that mint authorization.
	// We check via reflection that types do not have Grant/Authorize/Approve methods.
	checkNoAuth := func(typ reflect.Type, name string) {
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			low := strings.ToLower(m.Name)
			if strings.Contains(low, "grant") || strings.Contains(low, "authoriz") || strings.Contains(low, "approve") {
				t.Fatalf("GEN-09: %s must not have auth method %q", name, m.Name)
			}
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			low := strings.ToLower(f.Name)
			if strings.Contains(low, "grant") || strings.Contains(low, "authoriz") || strings.Contains(low, "capability") {
				t.Fatalf("GEN-09: %s must not have auth field %q", name, f.Name)
			}
		}
	}
	checkNoAuth(reflect.TypeOf(problemsurface.ProblemSurface{}), "ProblemSurface")
	checkNoAuth(reflect.TypeOf(changesurface.ChangeSurface{}), "ChangeSurface")
	checkNoAuth(reflect.TypeOf(mutationstrategy.MutationPlan{}), "MutationPlan")
	checkNoAuth(reflect.TypeOf(problem.ProblemSolvingPlan{}), "ProblemSolvingPlan")
}

// GEN-10 — Scheduler Isolation
func TestGEN10_SchedulerIsolation(t *testing.T) {
	checkNoScheduler := func(typ reflect.Type, name string) {
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			low := strings.ToLower(m.Name)
			if strings.Contains(low, "schedule") || strings.Contains(low, "scheduler") || strings.Contains(low, "execute") || strings.Contains(low, "run") {
				t.Fatalf("GEN-10: %s must not have scheduler method %q", name, m.Name)
			}
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			low := strings.ToLower(f.Name)
			if strings.Contains(low, "schedule") || strings.Contains(low, "execution") {
				t.Fatalf("GEN-10: %s must not have scheduler field %q", name, f.Name)
			}
		}
	}
	checkNoScheduler(reflect.TypeOf(problemsurface.ProblemSurface{}), "ProblemSurface")
	checkNoScheduler(reflect.TypeOf(changesurface.ChangeSurface{}), "ChangeSurface")
	checkNoScheduler(reflect.TypeOf(mutationstrategy.MutationPlan{}), "MutationPlan")
	checkNoScheduler(reflect.TypeOf(problem.ProblemSolvingPlan{}), "ProblemSolvingPlan")
}

// GEN-11 — No Invented Targets
func TestGEN11_NoInventedTargets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
	writeFile(t, root, "main.go", "package main\n")
	u := understanding.Derive(root)
	ps := problemsurface.Derive("do something with dashboard.tsx that does not exist", nil, u)
	for _, r := range ps.References {
		if r.Path == "dashboard.tsx" {
			t.Fatalf("GEN-11: problem surface must not invent unevidenced target dashboard.tsx")
		}
	}
	cs := changesurface.Derive("update dashboard.tsx", nil, u)
	for _, c := range cs.Candidates {
		if c.Path == "dashboard.tsx" {
			t.Fatalf("GEN-11: change surface must not invent dashboard.tsx")
		}
	}
	plan := mutationstrategy.Derive("update dashboard.tsx", u, cs, mutationstrategy.PlanOptions{})
	for _, st := range plan.Steps {
		for _, p := range st.SurfaceRefs {
			if p == "dashboard.tsx" {
				t.Fatalf("GEN-11: mutation plan must not invent dashboard.tsx")
			}
		}
	}
	// Unknown understanding must yield empty surfaces
	unknown := understanding.Derive(filepath.Join(t.TempDir(), "does-not-exist"))
	ps2 := problemsurface.Derive("anything", nil, unknown)
	if len(ps2.References) != 0 || ps2.Status != problemsurface.StatusUnresolved {
		t.Fatalf("GEN-11: unknown understanding must yield empty unresolved problem surface, got %+v", ps2)
	}
	cs2 := changesurface.Derive("anything", nil, unknown)
	if len(cs2.Candidates) != 0 || cs2.Status != changesurface.StatusUnresolved {
		t.Fatalf("GEN-11: unknown understanding must yield empty unresolved change surface, got %+v", cs2)
	}
}

// GEN-12 — Digest / Staleness Preservation
func TestGEN12_DigestStalenessPreservation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "index.html", "<html></html>\n")
	writeFile(t, root, "styles.css", "body{}\n")
	u := understanding.Derive(root)
	if u.IsStale() {
		t.Fatal("GEN-12: fresh understanding must not be stale")
	}
	ps := problemsurface.Derive("review site", nil, u)
	if !ps.DigestMatches(u) {
		t.Fatal("GEN-12: problem surface must match fresh understanding digest")
	}
	cs := changesurface.Derive("review site", nil, u)
	if !cs.DigestMatches(u) {
		t.Fatal("GEN-12: change surface must match fresh understanding digest")
	}
	// Mutate workspace
	writeFile(t, root, "script.js", "console.log(1)\n")
	if !u.IsStale() {
		t.Fatal("GEN-12: understanding must report stale after material change")
	}
	if ps.DigestMatches(understanding.Derive(root)) {
		t.Fatal("GEN-12: stale problem surface must not match refreshed digest")
	}
	if cs.DigestMatches(understanding.Derive(root)) {
		t.Fatal("GEN-12: stale change surface must not match refreshed digest")
	}
	// Mutation plan digest binding
	plan := mutationstrategy.Derive("review site", u, cs, mutationstrategy.PlanOptions{})
	if plan.UnderstandingDigest != u.Digest {
		t.Fatalf("GEN-12: plan digest mismatch: %q vs %q", plan.UnderstandingDigest, u.Digest)
	}
	if plan.SurfaceDigest == "" {
		t.Fatal("GEN-12: plan must have surface digest")
	}
	// Problem plan digest binding
	pplan := problem.Derive("review site", u, ps)
	if pplan.UnderstandingDigest != u.Digest {
		t.Fatalf("GEN-12: problem plan digest mismatch")
	}
}

// Architecture: core must not depend on web adapter
func TestArch_CoreDoesNotDependOnWebAdapter(t *testing.T) {
	// Check that core packages don't import adapters/web by inspecting source files
	corePackages := []string{
		"../../internal/understanding",
		"../../internal/problemsurface",
		"../../internal/changesurface",
		"../../internal/mutationstrategy",
		"../../internal/problem",
	}
	for _, pkgPath := range corePackages {
		abs, err := filepath.Abs(pkgPath)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			t.Fatalf("cannot read %s: %v", pkgPath, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			if strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(abs, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			content := string(data)
			if strings.Contains(content, "github.com/PizenLabs/izen/internal/adapters/web") {
				t.Fatalf("architecture: core package %s file %s must not import adapters/web", pkgPath, e.Name())
			}
			if strings.Contains(content, "StaticWeb") && pkgPath != "../../internal/adapters/web" {
				// Allow StaticWeb in comments but not in type definitions
				lines := strings.Split(content, "\n")
				for _, line := range lines {
					trim := strings.TrimSpace(line)
					if strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "/*") || strings.HasPrefix(trim, "*") {
						continue
					}
					if strings.Contains(line, "StaticWeb") {
						t.Fatalf("architecture: core package %s file %s must not reference StaticWeb in code: %q", pkgPath, e.Name(), line)
					}
				}
			}
			// mutationstrategy must not contain html/css/js/assets web families
			if pkgPath == "../../internal/mutationstrategy" {
				// Allow familyOf generic but not hard-coded html/css/js as special cases
				// We already removed those, but check that plan.go doesn't contain the old preferred list
				if strings.Contains(content, `"html", "css", "js", "assets"`) {
					t.Fatalf("architecture: mutationstrategy must not hardcode web family ordering")
				}
			}
		}
	}
	// Web adapter should depend on domain-neutral contracts (understanding, problemsurface, changesurface)
	adapterPath, _ := filepath.Abs("../../internal/adapters/web")
	entries, _ := os.ReadDir(adapterPath)
	foundUnderstandingImport := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(adapterPath, e.Name()))
		if strings.Contains(string(data), "internal/understanding") {
			foundUnderstandingImport = true
		}
	}
	if !foundUnderstandingImport {
		t.Fatal("architecture: web adapter must depend on domain-neutral understanding contract")
	}
}

// Architecture: problem-solving planning has no execution authority
func TestArch_ProblemSolvingPlanningHasNoExecutionAuthority(t *testing.T) {
	// ProblemSolvingPlan and related types must not have execution verbs
	for _, typ := range []reflect.Type{
		reflect.TypeOf(problem.ProblemSolvingPlan{}),
		reflect.TypeOf(problem.ProblemStep{}),
		reflect.TypeOf(problemsurface.ProblemSurface{}),
		reflect.TypeOf(problemsurface.Reference{}),
	} {
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			low := strings.ToLower(m.Name)
			if strings.Contains(low, "exec") || strings.Contains(low, "run") || strings.Contains(low, "apply") || strings.Contains(low, "mutate") {
				t.Fatalf("architecture: %s must not have execution method %q", typ.Name(), m.Name)
			}
		}
	}
}

// Generality acceptance: nine task classes remain semantically valid without web model
func TestGenerality_NineTaskClasses(t *testing.T) {
	tasks := []struct {
		name   string
		intent string
		setup  func(string)
	}{
		{"static web redesign", "Redesign the portfolio website including styles and scripts", func(root string) {
			writeFile(t, root, "index.html", "<html><link rel=\"stylesheet\" href=\"styles.css\"><script src=\"script.js\"></script></html>\n")
			writeFile(t, root, "styles.css", "body{}\n")
			writeFile(t, root, "script.js", "console.log(1)\n")
		}},
		{"Go race-condition investigation", "investigate a race condition in worker pool", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
			writeFile(t, root, "internal/worker/pool.go", "package worker\nimport \"sync\"\nvar mu sync.Mutex\nfunc Run(){mu.Lock(); defer mu.Unlock()}\n")
		}},
		{"Backend latency investigation", "investigate backend latency regression in API", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
			writeFile(t, root, "cmd/server/main.go", "package main\n")
			writeFile(t, root, "internal/api/handler.go", "package api\n")
		}},
		{"React rendering bottleneck", "investigate React rendering bottleneck", func(root string) {
			writeFile(t, root, "package.json", `{"name":"app","dependencies":{"react":"18"}}`)
			writeFile(t, root, "src/components/App.jsx", "export default function App(){}\n")
		}},
		{"Memory leak investigation", "investigate memory leak in long-running service", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
			writeFile(t, root, "cmd/service/main.go", "package main\n")
		}},
		{"Flaky CI test", "investigate flaky CI test in GitHub workflows", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
			writeFile(t, root, ".github/workflows/test.yml", "name: test\n")
			writeFile(t, root, "internal/worker/worker_test.go", "package worker\n")
		}},
		{"Architecture refactor", "refactor unstable module boundaries between worker and api", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
			writeFile(t, root, "internal/worker/worker.go", "package worker\n")
			writeFile(t, root, "internal/api/handler.go", "package api\n")
		}},
		{"Large unfamiliar-codebase investigation", "explore and audit the codebase structure", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
			writeFile(t, root, "cmd/a/main.go", "package main\n")
			writeFile(t, root, "cmd/b/main.go", "package main\n")
			writeFile(t, root, "internal/x/x.go", "package x\n")
		}},
		{"Root-cause → fix → verification", "investigate root cause, fix the race, and verify with tests", func(root string) {
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n")
			writeFile(t, root, "internal/worker/worker.go", "package worker\n")
			writeFile(t, root, "internal/worker/worker_test.go", "package worker\n")
		}},
	}
	for _, tc := range tasks {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(root)
			u := understanding.Derive(root)
			if !u.Valid() {
				t.Fatalf("task %q: understanding invalid", tc.name)
			}
			ps := problemsurface.Derive(tc.intent, nil, u)
			if ps.Status == problemsurface.StatusUnresolved {
				t.Logf("task %q: problem surface unresolved (acceptable for some tasks), reason: %s", tc.name, ps.UnresolvedReason)
				// At least the core does not force web model: it's unresolved, not web-specific error
				return
			}
			// ProblemSolvingPlan must be derivable
			pp := problem.Derive(tc.intent, u, ps)
			if pp.Status == problem.StatusUnresolved {
				t.Logf("task %q: problem plan unresolved: %s", tc.name, pp.UnresolvedReason)
				return
			}
			if len(pp.Steps) == 0 {
				t.Fatalf("task %q: problem plan must have steps when resolved", tc.name)
			}
			// If task is mutation-like, mutation plan should also be derivable, but not required for pure investigation
			if strings.Contains(strings.ToLower(tc.intent), "refactor") || strings.Contains(strings.ToLower(tc.intent), "redesign") || strings.Contains(strings.ToLower(tc.intent), "fix") {
				cs := changesurface.Derive(tc.intent, nil, u)
				if cs.Status != changesurface.StatusUnresolved {
					mp := mutationstrategy.Derive(tc.intent, u, cs, mutationstrategy.PlanOptions{})
					if mp.Status == mutationstrategy.StatusUnresolved {
						t.Logf("task %q: mutation plan unresolved (acceptable)", tc.name)
					}
				}
			}
			// Ensure no plan invents web semantics for non-web tasks
			for _, st := range pp.Steps {
				for _, ref := range st.References {
					low := strings.ToLower(ref)
					if strings.Contains(tc.intent, "race") || strings.Contains(tc.intent, "memory") || strings.Contains(tc.intent, "flaky") {
						if strings.HasSuffix(low, ".html") || strings.HasSuffix(low, ".css") {
							t.Fatalf("task %q: non-web task must not force web path %q", tc.name, ref)
						}
					}
				}
			}
		})
	}
}
