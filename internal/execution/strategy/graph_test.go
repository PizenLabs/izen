package strategy

import (
	"strings"
	"testing"
)

// ── Phase 11 — ExecutionGraph structure and lifecycle ─────────────────────

func TestGraphCompilesDeterministicOrdering(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<html><body>hi</body></html>"})
	p := Select("fix the extra paragraph in @index.html", d)

	g := Compile(p)
	if err := g.Validate(); err != nil {
		t.Fatalf("compiled graph invalid: %v", err)
	}
	if g.Strategy != TargetedMutation {
		t.Fatalf("strategy = %s, want targeted_mutation", g.Strategy)
	}
	// Golden topology: resolve_target → read_target → reason → propose →
	// approve → mutate → verify. The model node must be exactly one.
	wantKinds := []NodeKind{NodeResolveTarget, NodeReadTarget, NodeReason,
		NodePropose, NodeApprove, NodeMutate, NodeVerify}
	if len(g.Nodes) != len(wantKinds) {
		t.Fatalf("node count = %d, want %d: %s", len(g.Nodes), len(wantKinds), g.String())
	}
	for i, want := range wantKinds {
		if g.Nodes[i].Kind != want {
			t.Fatalf("node %d kind = %s, want %s", i+1, g.Nodes[i].Kind, want)
		}
	}
	if got := g.ModelNodeCount(); got != 1 {
		t.Fatalf("model nodes = %d, want exactly 1", got)
	}
	if got := g.ExpectedInvocations; got != 1 {
		t.Fatalf("expected invocations = %d, want 1", got)
	}
	// The mutate node maps to the resolved MutationSet target.
	if len(g.MutationTargets) != 1 || g.MutationTargets[0] != "index.html" {
		t.Fatalf("mutation targets = %v, want [index.html]", g.MutationTargets)
	}
	if n := g.First(NodeMutate); n.Target != "index.html" {
		t.Fatalf("mutate node target = %q, want index.html", n.Target)
	}
}

func TestGraphTerminalAware(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	g := Compile(Select("fix @index.html", d))
	if g.Terminal() {
		t.Fatal("fresh graph must not be terminal")
	}
	if g.State != GraphPending {
		t.Fatalf("fresh state = %s, want pending", g.State)
	}
	g.Start()
	if g.State != GraphRunning {
		t.Fatalf("state after Start = %s, want running", g.State)
	}
	// Drive every node to complete; the graph must reconcile to complete.
	for _, n := range g.Nodes {
		g.Complete(n.ID, "evidence")
	}
	if !g.Terminal() {
		t.Fatal("all-complete graph must be terminal")
	}
	if g.State != GraphComplete {
		t.Fatalf("state = %s, want complete", g.State)
	}
	if !g.AllTerminal() {
		t.Fatal("all nodes must be terminal")
	}
}

func TestGraphCancellationAware(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	g := Compile(Select("fix @index.html", d))
	g.Start()
	// Cancel mid-execution: every pending node must cancel, the graph must be
	// terminal cancelled, and later Begin/Complete calls must be no-ops.
	g.Cancel("user interrupted")
	if g.State != GraphCancelled {
		t.Fatalf("state = %s, want cancelled", g.State)
	}
	if !g.Terminal() {
		t.Fatal("cancelled graph must be terminal")
	}
	for _, n := range g.Nodes {
		if n.State != NodeCancelled {
			t.Fatalf("node %s state = %s, want cancelled", n.ID, n.State)
		}
	}
	// A terminal graph never re-enters: Complete after cancel is a no-op.
	g.Complete("n1", "late evidence")
	if g.State != GraphCancelled {
		t.Fatalf("state after late Complete = %s, want cancelled (immutable terminal)", g.State)
	}
}

func TestGraphFailureStopsExecution(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	g := Compile(Select("fix @index.html", d))
	g.Start()
	// The approval node is rejected by the human: the graph fails and no
	// mutate node may complete afterwards.
	g.Wait("n5", "approval requested")
	if g.State != GraphAwaitingHuman {
		t.Fatalf("state after Wait = %s, want awaiting_human", g.State)
	}
	// Human rejects → the boundary fails. The mutate node must remain
	// non-terminal and the graph must be terminal failed.
	g.Fail("n5", "human rejected the proposal")
	if g.State != GraphFailed {
		t.Fatalf("state after reject = %s, want failed", g.State)
	}
	if n := g.First(NodeMutate); n.State.Terminal() {
		t.Fatalf("mutate node reached terminal state %s after approval rejection — zero mutation required", n.State)
	}
	if err := CheckInvariants(Select("fix @index.html", d), g); err != nil {
		t.Fatalf("invariants after rejection: %v", err)
	}
}

func TestGraphValidateRejectsBadCompilation(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	g := Compile(Select("fix @index.html", d))
	// A model node without an invocation ordinal is invalid.
	if n := g.First(NodeReason); n != nil {
		n.Invocation = 0
		if err := g.Validate(); err == nil {
			t.Fatal("graph with model node without invocation must fail Validate")
		}
		n.Invocation = 1
	}
	// A mutate node targeting an unrecorded target is invalid.
	if n := g.First(NodeMutate); n != nil {
		n.Target = "not-in-mutationset.go"
		if err := g.Validate(); err == nil {
			t.Fatal("mutate node outside the MutationSet target set must fail Validate")
		}
	}
}

func TestGraphInspectableRecord(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	g := Compile(Select("fix @index.html", d))
	g.Start()
	g.Complete("n1", "exact match index.html")
	out := g.String()

	for _, want := range []string{"strategy=targeted_mutation", "state=running",
		"resolve_target", "read_target", "reason", "propose", "approve",
		"mutate", "verify", "invocation#1", "model=yes", "human"} {
		if !strings.Contains(out, want) {
			t.Errorf("graph record missing %q in:\n%s", want, out)
		}
	}
	// The record must never claim an unexecuted mutation.
	if g.First(NodeMutate).State.Terminal() {
		t.Fatal("mutate node must not be terminal before execution")
	}
}

func TestGraphMutationTargetsDeduplicated(t *testing.T) {
	d := deps(t, map[string]string{"a.html": "a", "b.css": "b"})
	p := Select("restyle using @a.html and @b.css", d)
	g := Compile(p)
	// One mutate node per target, deduplicated, in resolution order.
	if got := g.MutationNodeCount(); got != 2 {
		t.Fatalf("mutate nodes = %d, want 2", got)
	}
	targets := g.Targets()
	if len(targets) != 2 || targets[0] != "a.html" || targets[1] != "b.css" {
		t.Fatalf("targets = %v, want [a.html b.css]", targets)
	}
}

// Metrics aggregates the observability signals the graph owns (section 22):
// node count, depth, model invocations, escalation count, human checkpoints,
// mutation count and verification count — without any token estimate.
func TestGraphMetrics(t *testing.T) {
	d := deps(t, map[string]string{"a.html": "a", "b.css": "b"})
	p := Select("restyle using @a.html and @b.css", d)
	g := Compile(p)

	m := g.Metrics()
	if m.Strategy != TargetedMutation {
		t.Fatalf("metric strategy = %s, want targeted_mutation", m.Strategy)
	}
	if m.NodeCount != g.NodeCount() || m.Depth != g.Depth() {
		t.Fatalf("metrics node/depth mismatch: %+v", m)
	}
	if m.ModelInvocations != 1 {
		t.Fatalf("model invocations = %d, want 1", m.ModelInvocations)
	}
	if m.ExpectedInvocations != 1 {
		t.Fatalf("expected invocations = %d, want 1", m.ExpectedInvocations)
	}
	if m.HumanCheckpoints != 1 {
		t.Fatalf("human checkpoints = %d, want 1 (approve)", m.HumanCheckpoints)
	}
	if m.MutationCount != 2 {
		t.Fatalf("mutation count = %d, want 2", m.MutationCount)
	}
	if m.VerificationCount != 1 {
		t.Fatalf("verification count = %d, want 1", m.VerificationCount)
	}
	if out := m.String(); !strings.Contains(out, "strategy=targeted_mutation") {
		t.Fatalf("metrics render missing strategy: %q", out)
	}
}

func TestGraphMetricsDeterministic(t *testing.T) {
	d := deps(t, nil)
	g := Compile(Select("create a .gitignore file", d))
	m := g.Metrics()
	if m.ModelInvocations != 0 || m.ExpectedInvocations != 0 {
		t.Fatalf("deterministic metrics must show 0 model invocations: %+v", m)
	}
	if m.HumanCheckpoints != 0 {
		t.Fatalf("deterministic metrics must show 0 human checkpoints: %+v", m)
	}
}
