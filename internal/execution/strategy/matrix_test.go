package strategy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Phase 11 — Critical test matrix (engine-level items) ──────────────────

// E — Target resolved through an exact path: no unnecessary fuzzy resolution.
func TestMatrixExactPathNoFuzzyResolution(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>", "INDEX.html": "<p>other</p>"})
	p := Select("fix the typo in @index.html", d)
	if len(p.Targets) == 0 {
		t.Fatal("no target resolved")
	}
	tgt := p.Targets[0]
	if tgt.Status != TargetExplicit {
		t.Fatalf("target status = %s, want explicit (exact match)", tgt.Status)
	}
	if strings.Contains(tgt.Reason, "case-insensitive") {
		t.Fatalf("exact path must not fall back to fuzzy resolution: %q", tgt.Reason)
	}
	if !strings.Contains(tgt.Reason, "exact") {
		t.Fatalf("exact-path target must record its exact-match evidence: %q", tgt.Reason)
	}
}

// caseSensitiveWorkspace wraps a fake workspace with a case-sensitive Exists
// so the bounded fuzzy path can be exercised deterministically on any host
// filesystem.
type caseSensitiveWorkspace struct {
	*fakeWorkspace
}

func (c caseSensitiveWorkspace) Exists(path string) bool {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(filepath.Join(c.root, dir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() == base {
			return !e.IsDir()
		}
	}
	return false
}

// F — Target resolved through bounded fuzzy resolution: recorded evidence.
func TestMatrixFuzzyResolutionRecordedEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "DocsFile.md"), []byte("# docs"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Deps{Root: root, Workspace: caseSensitiveWorkspace{&fakeWorkspace{root: root}}}
	p := Select("fix the typo in @docsfile.md", d)
	if len(p.Targets) == 0 {
		t.Fatal("no target resolved")
	}
	tgt := p.Targets[0]
	if tgt.Status != TargetResolved {
		t.Fatalf("target status = %s, want resolved (fuzzy)", tgt.Status)
	}
	if !strings.Contains(tgt.Reason, "case-insensitive") || !strings.Contains(tgt.Reason, "unique") {
		t.Fatalf("fuzzy resolution must record its evidence: %q", tgt.Reason)
	}
	// The fuzzy lookup is bounded: the compile profile never demands a
	// repository-wide scan as context.
	if p.HasContext(ContextRepositoryConstraints) {
		t.Fatal("fuzzy-resolved single-file task must not demand repository constraints")
	}
}

// I — Simple task: exactly one bounded invocation, never a duplicate.
func TestMatrixSimpleTaskSingleInvocation(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	p := Select("fix @index.html", d)
	g := Compile(p)
	if g.ModelNodeCount() != 1 {
		t.Fatalf("model nodes = %d, want exactly 1", g.ModelNodeCount())
	}
	if g.ExpectedInvocations != 1 {
		t.Fatalf("expected invocations = %d, want 1", g.ExpectedInvocations)
	}
	// Exactly one model node carries the single invocation ordinal.
	n := g.First(NodeReason)
	if n == nil || n.Invocation != 1 {
		t.Fatalf("reason node invocation = %+v, want #1", n)
	}
}

// L — Multi-file dependent mutations preserve deterministic dependency order.
func TestMatrixDependentMutationsPreserveOrder(t *testing.T) {
	d := deps(t, map[string]string{"a.html": "a", "b.css": "b", "c.js": "c"})
	p := Select("restyle the page using @a.html and @b.css and @c.js", d)
	g := Compile(p)

	mutates := g.All(NodeMutate)
	if len(mutates) != 3 {
		t.Fatalf("mutate nodes = %d, want 3", len(mutates))
	}
	// Compile order == resolution order: a.html → b.css → c.js. Dependent
	// mutations are represented as ordered nodes; independent ones may run
	// concurrently only when the runtime proves independence (the compile
	// order is never derived from map iteration).
	wantOrder := []string{"a.html", "b.css", "c.js"}
	for i, want := range wantOrder {
		if mutates[i].Target != want {
			t.Fatalf("mutate node %d target = %s, want %s (order preserved)", i, mutates[i].Target, want)
		}
	}
	// One user mutation maps to one deduplicated MutationSet target set.
	if got := g.Targets(); len(got) != 3 {
		t.Fatalf("mutation targets = %v, want 3 deduplicated targets", got)
	}
}

// Q — Model invalid artifact: the InvocationContract names the deterministic
// failure and fallback behavior before the call.
func TestMatrixInvalidArtifactHasFallback(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	p := Select("fix @index.html", d)
	ic := For(p, 1)

	if ic.InvalidOutput == "" {
		t.Fatal("contract must define invalid output")
	}
	if !strings.Contains(ic.InvalidOutput, "full-file rewrite") {
		t.Fatalf("invalid-output = %q, want the bounded-artifact rejection", ic.InvalidOutput)
	}
	if ic.DeterministicFallback == "" {
		t.Fatal("contract must define the deterministic fallback")
	}
	// The engine's failure behavior is pre-declared: the model can never
	// silently decide what happens on its own failure.
	if !strings.Contains(ic.DeterministicFallback, "fuzzy patch repair") {
		t.Fatalf("fallback = %q, want the engine-side patch repair", ic.DeterministicFallback)
	}
}

// R — Human clarification: no provider invocation is ever justified.
func TestMatrixClarificationNoInvocation(t *testing.T) {
	d := deps(t, map[string]string{
		"src/index.html":    "a",
		"public/index.html": "b",
	})
	p := Select("fix the header in @index.html", d)
	if p.Strategy != HumanClarification {
		t.Fatalf("strategy = %s, want human_clarification", p.Strategy)
	}
	if ic := For(p, 1); ic.Number != 0 {
		t.Fatalf("invocation contract = %d, want 0 (no provider call)", ic.Number)
	}
	g := Compile(p)
	if g.ModelNodeCount() != 0 {
		t.Fatalf("clarification graph compiled %d model nodes, want 0", g.ModelNodeCount())
	}
}

// Consolidated efficiency-invariant matrix: every strategy profile + compiled
// graph pair must satisfy the section-23 invariants.
func TestMatrixEfficiencyInvariantsAllStrategies(t *testing.T) {
	d := deps(t, map[string]string{
		"index.html":     "<p>hi</p>",
		"src/index.html": "<p>src</p>",
		"main.go":        "package main",
		"README.md":      "# r",
	})
	raws := []string{
		"fix extra content in @index.html",     // targeted mutation
		"create a .gitignore file",             // deterministic
		"remove the footer from @missing.html", // unresolved → clarify
		"fix the header in @index.html",        // ambiguous → clarify
		"fix the typo in @readme.md",           // fuzzy resolved
		"explain the login flow in @main.go",   // targeted reasoning
		"why is the build failing",             // repository investigation
		"add a rate limiter to the API",        // multi-file planning
		"restyle the page using @index.html",   // multi-target single strategy
	}
	for _, raw := range raws {
		p := Select(raw, d)
		g := Compile(p)
		if errs := CheckInvariants(p, g); len(errs) > 0 {
			t.Errorf("CheckInvariants(%q) [%s]: %v", raw, p.Strategy, errs)
		}
	}
}
