package execution

import (
	"strings"
	"testing"
)

// ── ExecutionGraph + target model contract (Phase 9B) ─────────────────────

func statExists(path string) bool { return strings.HasPrefix(path, "exists-") }

// statAll reports every path as existing (for graph tests that do not care
// about existence).
func statAll(string) bool { return true }

func TestResolveTargetSet_DeterministicOrderAndDedup(t *testing.T) {
	// Duplicate paths collapse into one target, first appearance wins.
	res, err := ResolveTargetSet([]string{"exists-a.html", "exists-b.html", "exists-a.html"}, statExists)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Ambiguous {
		t.Fatal("unexpected ambiguity")
	}
	if len(res.Targets) != 2 {
		t.Fatalf("targets = %d, want 2 (dedup)", len(res.Targets))
	}
	if res.Targets[0].Path != "exists-a.html" || res.Targets[1].Path != "exists-b.html" {
		t.Fatalf("ordering not preserved: %+v", res.Targets)
	}
	if res.Targets[0].Role != TargetExplicit {
		t.Fatalf("role = %q, want explicit", res.Targets[0].Role)
	}
	if !res.Targets[0].Exists {
		t.Fatal("exists-a.html should be reported as existing")
	}
}

func TestResolveTargetSet_MissingTargetFailsDeterministically(t *testing.T) {
	_, err := ResolveTargetSet([]string{"exists-a.html", "missing.html"}, statExists)
	if err == nil || !strings.Contains(err.Error(), "target does not exist: missing.html") {
		t.Fatalf("missing target = %v, want deterministic failure", err)
	}
}

func TestResolveTargetSet_TemplateTargetAllowsCreation(t *testing.T) {
	res, err := ResolveTargetSet([]string{"LICENSE", "README.md"}, statExists)
	if err != nil {
		t.Fatalf("template targets must be creatable: %v", err)
	}
	if len(res.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(res.Targets))
	}
	if res.Targets[0].Exists {
		t.Fatal("LICENSE should be reported as missing")
	}
}

func TestResolveTargetSet_EmptyIsAmbiguous(t *testing.T) {
	res, err := ResolveTargetSet(nil, statExists)
	if err != nil {
		t.Fatalf("empty set should not error: %v", err)
	}
	if !res.Ambiguous {
		t.Fatal("empty target set must be ambiguous")
	}
}

func TestExecutionGraph_StableOrderingAndTargets(t *testing.T) {
	ms := NewMutationSet()
	res, _ := ResolveTargetSet([]string{"a.html", "b.html", "c.html"}, statAll)
	g := NewExecutionGraph("op-1", res.Targets, ms)
	if got := g.Targets(); len(got) != 3 || got[0] != "a.html" || got[1] != "b.html" || got[2] != "c.html" {
		t.Fatalf("targets = %v, want stable [a.html b.html c.html]", got)
	}
	if g.Nodes[0].ID != "n1" || g.Nodes[2].ID != "n3" {
		t.Fatalf("node IDs not stable: %s %s", g.Nodes[0].ID, g.Nodes[2].ID)
	}
	if g.MutationSet != ms {
		t.Fatal("graph must own the exact MutationSet")
	}
	if g.State != GraphPending {
		t.Fatalf("graph state = %q, want pending", g.State)
	}
}

func TestExecutionGraph_ValidateRejectsDuplicateTarget(t *testing.T) {
	ms := NewMutationSet()
	g := NewExecutionGraph("op", []Target{{Path: "a.html", Role: TargetExplicit}}, ms)
	g.Nodes = append(g.Nodes, &ExecutionNode{ID: "n2", Target: "a.html"})
	err := g.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate target") {
		t.Fatalf("duplicate target = %v, want deterministic conflict failure", err)
	}
}

func TestExecutionGraph_AddDependencyRejectsMissingAndSelf(t *testing.T) {
	ms := NewMutationSet()
	res, _ := ResolveTargetSet([]string{"a.html", "b.html"}, statAll)
	g := NewExecutionGraph("op", res.Targets, ms)
	if err := g.AddDependency("n1", "n99", "test"); err == nil {
		t.Fatal("dependency to unknown node must fail")
	}
	if err := g.AddDependency("n1", "n1", "self"); err == nil {
		t.Fatal("self dependency must fail")
	}
}

func TestExecutionGraph_DependencyCycleFailsDeterministically(t *testing.T) {
	ms := NewMutationSet()
	res, _ := ResolveTargetSet([]string{"a.html", "b.html"}, statAll)
	g := NewExecutionGraph("op", res.Targets, ms)
	if err := g.AddDependency("n1", "n2", "B depends on A"); err != nil {
		t.Fatalf("a -> b edge should be legal: %v", err)
	}
	if err := g.AddDependency("n2", "n1", "A depends on B"); err == nil {
		t.Fatal("a <-> b cycle must fail deterministically")
	} else if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v, want cycle description", err)
	}
	// The rejected edge must not be retained.
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d, want 1 (rejected edge rolled back)", len(g.Edges))
	}
}

func TestExecutionGraph_MissingDependencyFails(t *testing.T) {
	ms := NewMutationSet()
	res, _ := ResolveTargetSet([]string{"a.html", "b.html"}, statAll)
	g := NewExecutionGraph("op", res.Targets, ms)
	g.Nodes[1].Dependencies = []string{"n99"}
	if err := g.Validate(); err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("missing dependency = %v, want deterministic failure", err)
	}
}

func TestExecutionGraph_HasAllArtifactsGate(t *testing.T) {
	ms := NewMutationSet()
	res, _ := ResolveTargetSet([]string{"a.html", "b.html"}, statAll)
	g := NewExecutionGraph("op", res.Targets, ms)
	if g.HasAllArtifacts() {
		t.Fatal("graph without artifacts must not pass the Phase A gate")
	}
	g.Nodes[0].Patch = &Patch{File: "a.html", Modified: "x"}
	if g.HasAllArtifacts() {
		t.Fatal("partially-prepared graph must not pass the Phase A gate")
	}
	g.Nodes[1].Patch = &Patch{File: "b.html", Modified: "y"}
	if !g.HasAllArtifacts() {
		t.Fatal("fully-prepared graph must pass the Phase A gate")
	}
}

func TestExecutionGraph_TerminalInvariants(t *testing.T) {
	ms := NewMutationSet()
	res, _ := ResolveTargetSet([]string{"a.html", "b.html"}, statAll)
	g := NewExecutionGraph("op", res.Targets, ms)
	g.Transition(GraphPreparing)
	g.Transition(GraphReady)
	if g.State != GraphReady {
		t.Fatalf("state = %q, want ready", g.State)
	}
	if g.Terminal() {
		t.Fatal("ready must not be terminal")
	}
	// A terminal graph is never re-entered.
	g.Transition(GraphCommitted)
	if !g.Terminal() {
		t.Fatal("committed must be terminal")
	}
	g.Transition(GraphApplying)
	if g.State != GraphCommitted {
		t.Fatal("terminal graph must refuse transitions")
	}
	// Node terminality never contradicts the committed set.
	if g.AllNodesTerminal() {
		t.Fatal("nodes were never terminalized")
	}
	for _, n := range g.Nodes {
		n.State = NodeVerified
	}
	if !g.AllNodesTerminal() {
		t.Fatal("all verified nodes must be terminal")
	}
}
