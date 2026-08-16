package strategy

import (
	"strings"
	"testing"
)

// ── Phase 11 — Strategy → Graph compilation and golden flows ───────────────

// kinds returns the ordered node-kind sequence of the graph.
func kinds(g *ExecutionGraph) []NodeKind {
	if g == nil {
		return nil
	}
	out := make([]NodeKind, len(g.Nodes))
	for i, n := range g.Nodes {
		out[i] = n.Kind
	}
	return out
}

func sameKinds(a, b []NodeKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FLOW 1 — Simple prompt mutation: one bounded model call, no repository scan,
// no gather_evidence, zero unnecessary investigation.
func TestCompileFlow1SimplePromptMutation(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<html><body><p>one</p><p>two</p></body></html>"})
	p := Select("fix extra content in @index.html", d)
	g := Compile(p)

	if p.Strategy != TargetedMutation {
		t.Fatalf("strategy = %s, want targeted_mutation", p.Strategy)
	}
	want := []NodeKind{NodeResolveTarget, NodeReadTarget, NodeReason, NodePropose, NodeApprove, NodeMutate, NodeVerify}
	if !sameKinds(kinds(g), want) {
		t.Fatalf("graph = %v, want %v", kinds(g), want)
	}
	if g.Has(NodeGatherEvidence) {
		t.Fatal("simple single-file mutation must not gather repository evidence")
	}
	if got := g.ModelNodeCount(); got != 1 {
		t.Fatalf("model nodes = %d, want 1", got)
	}
	if got := g.ExpectedInvocations; got != 1 {
		t.Fatalf("expected invocations = %d, want 1", got)
	}
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("invariants violated: %v", errs)
	}
}

// FLOW 2 — Deterministic create: zero model calls.
func TestCompileFlow2DeterministicCreate(t *testing.T) {
	d := deps(t, nil)
	p := Select("create a .gitignore file", d)
	g := Compile(p)

	if p.Strategy != DirectDeterministic {
		t.Fatalf("strategy = %s, want direct_deterministic", p.Strategy)
	}
	want := []NodeKind{NodeResolveTarget, NodeReadTarget, NodeMutate, NodeVerify}
	if !sameKinds(kinds(g), want) {
		t.Fatalf("graph = %v, want %v", kinds(g), want)
	}
	if g.ModelNodeCount() != 0 || g.ExpectedInvocations != 0 {
		t.Fatalf("deterministic graph must expect 0 model invocations (nodes=%d expected=%d)",
			g.ModelNodeCount(), g.ExpectedInvocations)
	}
	if g.Has(NodeApprove) || g.Has(NodeReason) || g.Has(NodePropose) {
		t.Fatal("deterministic graph must not contain reasoning or approval nodes")
	}
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("invariants violated: %v", errs)
	}
}

// FLOW 3 — Ambiguous target: zero model calls + human clarification.
func TestCompileFlow3AmbiguousTarget(t *testing.T) {
	d := deps(t, map[string]string{
		"src/index.html":    "<html></html>",
		"public/index.html": "<html></html>",
	})
	p := Select("fix the header in @index.html", d)
	g := Compile(p)

	if p.Strategy != HumanClarification {
		t.Fatalf("strategy = %s, want human_clarification", p.Strategy)
	}
	want := []NodeKind{NodeResolveTarget, NodeClarify}
	if !sameKinds(kinds(g), want) {
		t.Fatalf("graph = %v, want %v", kinds(g), want)
	}
	if g.ModelNodeCount() != 0 || g.ExpectedInvocations != 0 {
		t.Fatalf("ambiguity must stop before any model invocation")
	}
	if !g.Has(NodeClarify) {
		t.Fatal("ambiguous graph must stop at a clarify boundary")
	}
	// Driving the graph: resolve completes, clarify waits → awaiting_human.
	g.Start()
	g.Complete("n1", "ambiguous: multiple candidates")
	g.Wait("n2", "human must disambiguate")
	if g.State != GraphAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", g.State)
	}
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("invariants violated: %v", errs)
	}
}

// FLOW 4 — Complex repository task: escalation is justified — gather_evidence,
// propose and approval appear.
func TestCompileFlow4ComplexRepositoryTask(t *testing.T) {
	d := deps(t, map[string]string{"auth.go": "package auth", "svc.go": "package svc"})
	p := Select("refactor the authentication flow across affected services", d)
	g := Compile(p)

	if p.Strategy != MultiFilePlanning && p.Strategy != RepositoryInvestigation {
		t.Fatalf("strategy = %s, want multi_file_planning/repository_investigation", p.Strategy)
	}
	if !g.Has(NodeGatherEvidence) {
		t.Fatal("complex repository task must gather structural evidence")
	}
	if !g.Has(NodeReason) || !g.Has(NodePropose) {
		t.Fatal("complex task must reason and propose")
	}
	if g.ExpectedInvocations != 1 {
		t.Fatalf("expected invocations = %d, want 1 (mandatory planning call)", g.ExpectedInvocations)
	}
	// Escalation was recorded: the strategy expansion is never silent.
	if g.EscalationCount() == 0 {
		t.Fatal("complex repository task must record its escalation evidence")
	}
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("invariants violated: %v", errs)
	}
}

// FLOW 5 — Targeted reasoning: resolve → read → reason, never mutate.
func TestCompileFlow5TargetedReasoning(t *testing.T) {
	d := deps(t, map[string]string{"auth.go": "package auth"})
	p := Select("explain the login flow in @auth.go", d)
	g := Compile(p)

	if p.Strategy != TargetedReasoning {
		t.Fatalf("strategy = %s, want targeted_reasoning", p.Strategy)
	}
	want := []NodeKind{NodeResolveTarget, NodeReadTarget, NodeReason}
	if !sameKinds(kinds(g), want) {
		t.Fatalf("graph = %v, want %v", kinds(g), want)
	}
	if g.Has(NodeMutate) || g.Has(NodeApprove) || g.Has(NodeVerify) {
		t.Fatal("read-only reasoning graph must never reach mutation nodes")
	}
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("invariants violated: %v", errs)
	}
}

// Golden-flow coverage: every compiled graph renders a truthful inspectable
// record and validates.
func TestCompileAllStrategiesValidateAndRender(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>", "main.go": "package main"})
	inputs := []struct {
		raw string
	}{
		{"fix extra content in @index.html"},
		{"create a .gitignore file"},
		{"remove the footer from @missing.html"},
		{"explain the login flow in @index.html"},
		{"why is the build failing"},
		{"add a rate limiter to the API"},
	}
	for _, in := range inputs {
		p := Select(in.raw, d)
		g := Compile(p)
		if err := g.Validate(); err != nil {
			t.Fatalf("Compile(%q) [%s] invalid: %v", in.raw, p.Strategy, err)
		}
		if out := g.String(); !strings.Contains(out, "execution-graph") {
			t.Fatalf("Compile(%q) renders no inspectable record: %q", in.raw, out)
		}
		if errs := CheckInvariants(p, g); len(errs) > 0 {
			t.Fatalf("Compile(%q) [%s] invariants: %v", in.raw, p.Strategy, errs)
		}
	}
}

// Deterministic compilation: the same profile always yields the same node
// sequence (never map-iteration order).
func TestCompileDeterministicAcrossRuns(t *testing.T) {
	d := deps(t, map[string]string{"a.html": "a", "b.css": "b", "c.js": "c"})
	raw := "restyle the page using @a.html and @b.css and @c.js"
	first := kinds(Compile(Select(raw, d)))
	for i := 0; i < 10; i++ {
		if got := kinds(Compile(Select(raw, d))); !sameKinds(got, first) {
			t.Fatalf("non-deterministic compilation: %v != %v", got, first)
		}
	}
}

// Multi-file mutation: one mutate node per target, ordered by resolution.
func TestCompileMultiFileMutateNodes(t *testing.T) {
	d := deps(t, map[string]string{"a.html": "a", "b.css": "b"})
	p := Select("restyle the page using @a.html and @b.css", d)
	g := Compile(p)

	if p.Strategy != TargetedMutation {
		t.Fatalf("strategy = %s, want targeted_mutation", p.Strategy)
	}
	mutates := g.All(NodeMutate)
	if len(mutates) != 2 {
		t.Fatalf("mutate nodes = %d, want 2", len(mutates))
	}
	if mutates[0].Target != "a.html" || mutates[1].Target != "b.css" {
		t.Fatalf("mutate targets = [%s %s], want [a.html b.css]", mutates[0].Target, mutates[1].Target)
	}
	// One mutation graph → one MutationSet target set (deduplicated).
	if got := g.Targets(); len(got) != 2 || got[0] != "a.html" || got[1] != "b.css" {
		t.Fatalf("MutationSet targets = %v, want [a.html b.css]", got)
	}
	// Exactly one model call for the whole multi-file change (the bounded
	// artifact generation), never one per file.
	if g.ModelNodeCount() != 1 {
		t.Fatalf("model nodes = %d, want 1", g.ModelNodeCount())
	}
}

// Escalation records carry the full evidence account (section 7).
func TestCompileEscalationRecordsCarryEvidence(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	p := Select("remove the footer from @missing.html", d)
	g := Compile(p)

	if p.Strategy != HumanClarification {
		t.Fatalf("strategy = %s, want human_clarification", p.Strategy)
	}
	if g.EscalationCount() != 1 {
		t.Fatalf("escalations = %d, want 1", g.EscalationCount())
	}
	e := g.Escalations[0]
	if e.From == "" || e.To == "" || e.Reason == "" || e.Evidence == "" {
		t.Fatalf("escalation missing required fields: %+v", e)
	}
	if e.To != "human_clarification" {
		t.Fatalf("escalation To = %q, want human_clarification", e.To)
	}
}

// Repository investigation: the clarify-or-propose branch is explicit.
func TestCompileInvestigationClarifyOrPropose(t *testing.T) {
	d := deps(t, map[string]string{"main.go": "package main"})
	p := Select("why is the build failing", d)
	g := Compile(p)

	if p.Strategy != RepositoryInvestigation {
		t.Fatalf("strategy = %s, want repository_investigation", p.Strategy)
	}
	if !g.Has(NodePropose) {
		t.Fatal("investigation graph must propose when evidence is sufficient")
	}
	if !g.Has(NodeClarify) {
		t.Fatal("investigation graph must clarify when evidence is insufficient")
	}
}
