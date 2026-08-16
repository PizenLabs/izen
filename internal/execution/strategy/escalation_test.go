package strategy

import (
	"strings"
	"testing"
)

// ── Phase 11 — Escalation records and efficiency invariants ───────────────

func TestEscalationRecorder(t *testing.T) {
	r := NewRecorder()
	r.Record("targeted_mutation", "human_clarification", "ambiguous target", "", "the human is the authority")
	r.Record("sufficient_context", "expanded_context", "model revealed a dependency", "dependency_evidence", "target ripples to a coupled file")

	if r.Count() != 2 {
		t.Fatalf("count = %d, want 2", r.Count())
	}
	recs := r.Records()
	if recs[0].To != "human_clarification" || recs[1].To != "expanded_context" {
		t.Fatalf("records out of order: %+v", recs)
	}
	if recs[0].At.IsZero() {
		t.Fatal("escalation must carry its timestamp")
	}
}

func TestEscalationRecordRenders(t *testing.T) {
	e := EscalationRecord{
		From:              "initial",
		To:                "human_clarification",
		Evidence:          "target is ambiguous",
		AdditionalContext: "",
		Reason:            "the human must disambiguate",
	}
	out := e.String()
	for _, want := range []string{"initial", "human_clarification", "target is ambiguous", "disambiguate"} {
		if !strings.Contains(out, want) {
			t.Errorf("escalation render missing %q in %q", want, out)
		}
	}
}

func TestEscalationsForHumanClarification(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	p := Select("remove the footer from @missing.html", d)
	recs := EscalationsFor(p, ContextEnvelope{})
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	if recs[0].To != "human_clarification" {
		t.Fatalf("To = %q, want human_clarification", recs[0].To)
	}
}

func TestEscalationsForContextExpansion(t *testing.T) {
	d := deps(t, map[string]string{"a.css": "body{}", "index.html": "<p>hi</p>"})
	p := Select("fix @index.html", d)
	env := NewCompiler(d).Compile(p)
	esc := NewEscalator(env)
	esc.Expand("model reasoning revealed a dependency", ContextItem{
		Kind: ContextDependencyEvidence, Owner: "engine", Source: SourceFileGraph,
		Relevance: "target depends on a.css", Authority: "structural graph",
		ReasonForInclusion: "the mutation ripples to a coupled file",
	})
	recs := EscalationsFor(p, esc.Envelope())
	found := false
	for _, r := range recs {
		if r.To == "expanded_context" {
			found = true
			if r.Reason == "" {
				t.Fatal("context expansion escalation must record why the previous level was insufficient")
			}
		}
	}
	if !found {
		t.Fatal("expanded context envelope must produce an escalation record")
	}
}

// ── Efficiency invariants (section 23) ────────────────────────────────────

func TestInvariantsSimpleTaskNoInvestigation(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	p := Select("fix the extra paragraph in @index.html", d)
	g := Compile(p)
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("simple task violated invariants: %v", errs)
	}
}

func TestInvariantsDeterministicNoModel(t *testing.T) {
	d := deps(t, nil)
	p := Select("create a .gitignore file", d)
	g := Compile(p)
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("deterministic task violated invariants: %v", errs)
	}
}

func TestInvariantsAmbiguousNoModel(t *testing.T) {
	d := deps(t, map[string]string{
		"src/index.html":    "a",
		"public/index.html": "b",
	})
	p := Select("fix the header in @index.html", d)
	g := Compile(p)
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("ambiguous task violated invariants: %v", errs)
	}
}

func TestInvariantsResolvedSingleFileNoRepoScan(t *testing.T) {
	d := deps(t, map[string]string{"auth.go": "package auth"})
	p := Select("fix the login flow in @auth.go", d)
	g := Compile(p)
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("resolved single-file task violated invariants: %v", errs)
	}
}

func TestInvariantsModelNodeCarriesContract(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	p := Select("fix @index.html", d)
	g := Compile(p)
	n := g.First(NodeReason)
	if n == nil || !n.RequiresModel {
		t.Fatal("reason node missing")
	}
	if n.Invocation != 1 || n.Contract == "" {
		t.Fatalf("model node missing contract: invocation=%d contract=%q", n.Invocation, n.Contract)
	}
	if !strings.Contains(n.Contract, "invocation#1") {
		t.Fatalf("model node contract must be the explicit InvocationContract: %q", n.Contract)
	}
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("invariants violated: %v", errs)
	}
}

func TestInvariantsMultiFileOneMutationSet(t *testing.T) {
	d := deps(t, map[string]string{"a.html": "a", "b.css": "b"})
	p := Select("restyle using @a.html and @b.css", d)
	g := Compile(p)
	// Independent mutations are represented as independent mutate nodes under
	// one graph / one MutationSet target set.
	if got := g.MutationNodeCount(); got != 2 {
		t.Fatalf("mutate nodes = %d, want 2", got)
	}
	if len(g.Targets()) != 2 {
		t.Fatalf("mutation target count = %d, want 2 (one set, no duplicates)", len(g.Targets()))
	}
	if errs := CheckInvariants(p, g); len(errs) > 0 {
		t.Fatalf("multi-file task violated invariants: %v", errs)
	}
}
