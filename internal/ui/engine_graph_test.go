package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── Execution graph — runtime-owned ─────────────────────────────────────────
//
// The execution graph is compiled and driven by the RUNTIME now: the
// RuntimeExecutor records real graph boundaries in its ExecutionProof. The UI
// renders the runtime proof (via gatedExecutionMsg) instead of maintaining its
// own strategy graph. The retained record* helpers below still apply ONLY to
// legacy hotfix/build paths that run outside the strategy layer.

// TestEngineGraphRuntimeProofCarriesExecutionGraph verifies the RuntimeExecutor
// proof carries the real execution graph (strategy, resolve_target, model
// invocation, artifact, approval) and that the UI maintains NO strategy graph
// of its own on the migrated path.
func TestEngineGraphRuntimeProofCarriesExecutionGraph(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>one</p><p>two</p>",
	}, &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>one</p>\n=======\n<p>fixed</p>\n>>>>>>>",
	}}})

	gem := runGate(m, "fix extra contents in @index.html")
	if gem.res == nil {
		t.Fatal("expected a gate result")
	}
	proof := gem.res.Proof
	if proof == nil {
		t.Fatal("expected a runtime ExecutionProof")
	}
	if proof.Strategy != "targeted_mutation" {
		t.Fatalf("proof strategy = %q, want targeted_mutation", proof.Strategy)
	}
	if len(proof.Graph) == 0 {
		t.Fatal("proof graph must record real runtime boundaries")
	}
	// The graph must record strategy selection + artifact production at minimum.
	haveStrategy := false
	haveArtifact := false
	for _, step := range proof.Graph {
		if step.Stage == "strategy_selected" {
			haveStrategy = true
		}
		if step.Stage == "artifact_produced" {
			haveArtifact = true
		}
	}
	if !haveStrategy || !haveArtifact {
		t.Fatalf("proof graph missing strategy_selected/artifact_produced: %+v", proof.Graph)
	}

	// The UI owns NO strategy graph on the migrated path — the runtime proof is
	// the single source.
	if m.lastStrategyGraph != nil {
		t.Fatal("UI must not maintain its own strategy graph on the migrated path")
	}
	// Modes are presentation contexts only: no phase transition.
	if m.resolver.Current() != modes.ModeAsk {
		t.Fatalf("mode switched to /%s — modes must not decide the execution path", m.resolver.Current())
	}
}

// TestEngineGraphRetainedHelpersNoopWithoutGraph verifies the retained legacy
// record helpers are strict no-ops when no UI strategy graph is active.
func TestEngineGraphRetainedHelpersNoopWithoutGraph(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	}, &mockProvider{responses: []*ai.Response{{Content: "x"}}})
	if m.lastStrategyGraph != nil {
		t.Fatal("fresh model must have no strategy graph")
	}
	m.recordStrategyGraphProposal()
	m.recordStrategyGraphMutation(true)
	m.recordStrategyGraphModelFailed(errors.New("x"))
	m.cancelStrategyGraph("x")
	if m.lastStrategyGraph != nil {
		t.Fatal("recording without a strategy graph must be a strict no-op")
	}
}

// completeResolveTarget completes the deterministic resolve_target node on a
// manually compiled legacy graph (mirrors what the removed recordStrategyGraph
// did at dispatch).
func completeResolveTarget(g *strategy.ExecutionGraph) {
	if n := g.First(strategy.NodeResolveTarget); n != nil {
		g.Complete(n.ID, "target resolved deterministically")
	}
}

// TestEngineGraphTerminalRecording verifies the retained legacy helpers drive a
// compiled graph from real boundaries (proposal → reason+propose; apply →
// mutate+verify; terminal reconcile to complete).
func TestEngineGraphTerminalRecording(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>one</p><p>two</p>",
	}, &mockProvider{responses: []*ai.Response{{Content: "x"}}})
	gem := runGate(m, "fix extra contents in @index.html")
	if gem.res == nil {
		t.Fatal("expected a gate result")
	}
	profile := m.lastExecutionStrategy
	if profile.Strategy != strategy.TargetedMutation {
		t.Fatalf("strategy = %s, want targeted_mutation", profile.Strategy)
	}
	m.lastStrategyGraph = strategy.Compile(profile)
	g := m.lastStrategyGraph
	completeResolveTarget(g)

	m.recordStrategyGraphProposal()
	if n := g.First(strategy.NodeReason); n.State != strategy.NodeComplete {
		t.Errorf("reason state = %s, want complete", n.State)
	}
	m.recordStrategyGraphMutation(true)
	if !g.Terminal() {
		t.Fatalf("graph not terminal after successful apply (state=%s)", g.State)
	}
	if g.State != strategy.GraphComplete {
		t.Fatalf("graph state = %s, want complete", g.State)
	}
	if errs := strategy.CheckInvariants(profile, g); len(errs) > 0 {
		t.Fatalf("invariants violated after full execution: %v", errs)
	}
}

// TestEngineGraphMutationFailureRollback verifies a failed apply marks the
// mutate node failed and the graph reaches failed.
func TestEngineGraphMutationFailureRollback(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	}, &mockProvider{responses: []*ai.Response{{Content: "x"}}})
	_ = runGate(m, "fix extra contents in @index.html")
	m.lastStrategyGraph = strategy.Compile(m.lastExecutionStrategy)
	g := m.lastStrategyGraph
	completeResolveTarget(g)
	m.recordStrategyGraphProposal()
	m.recordStrategyGraphMutation(false)
	if g.State != strategy.GraphFailed {
		t.Fatalf("graph state = %s, want failed (rollback)", g.State)
	}
	if n := g.First(strategy.NodeMutate); n.State != strategy.NodeFailed {
		t.Errorf("mutate state = %s, want failed", n.State)
	}
}

// TestEngineGraphCancellation verifies cancellation drives the graph to the
// terminal cancelled state and a later record is a no-op (immutable terminal).
func TestEngineGraphCancellation(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	}, &mockProvider{responses: []*ai.Response{{Content: "x"}}})
	_ = runGate(m, "fix extra contents in @index.html")
	m.lastStrategyGraph = strategy.Compile(m.lastExecutionStrategy)
	g := m.lastStrategyGraph
	completeResolveTarget(g)

	m.cancelStrategyGraph("user interrupted")
	if g.State != strategy.GraphCancelled {
		t.Fatalf("graph state = %s, want cancelled", g.State)
	}
	m.recordStrategyGraphMutation(true)
	if g.State != strategy.GraphCancelled {
		t.Fatalf("graph state after late record = %s, want cancelled (immutable)", g.State)
	}
}

// TestEngineGraphInspectRenders verifies $inspect renders a compiled graph with
// execution facts only.
func TestEngineGraphInspectRenders(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	}, &mockProvider{responses: []*ai.Response{{Content: "x"}}})
	profile := strategy.ExecutionStrategyProfile{
		Intent:        "remove the footer from @missing.html",
		Strategy:      strategy.TargetedMutation,
		ModelRequired: true,
		ContextKinds:  []strategy.ContextKind{strategy.ContextUserIntent},
	}
	m.lastStrategyGraph = strategy.Compile(profile)
	m.recordStrategyGraphProposal()
	out := renderStrategyGraph(m.lastStrategyGraph)

	for _, want := range []string{"execution-graph", "resolve_target", "expected-invocations"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderStrategyGraph missing %q in:\n%s", want, out)
		}
	}
}

// TestEngineGraphExecutionProofCarriesStrategy verifies the runtime proof
// records the strategy decision (section 19).
func TestEngineGraphExecutionProofCarriesStrategy(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	}, &mockProvider{responses: []*ai.Response{{Content: "x"}}})
	gem := runGate(m, "fix extra contents in @index.html")
	if gem.res == nil || gem.res.Proof == nil {
		t.Fatal("expected a runtime proof")
	}
	if gem.res.Proof.Strategy != "targeted_mutation" {
		t.Fatalf("proof strategy = %q, want targeted_mutation", gem.res.Proof.Strategy)
	}
	if len(gem.res.Proof.ModelInvocations) != 1 {
		t.Fatalf("proof model invocations = %d, want 1", len(gem.res.Proof.ModelInvocations))
	}
}

// TestEngineGraphProofOutcomeCarriesEvidence verifies the approval-gate proof
// carries no fabricated mutation — the outcome stays pending_approval (a valid
// held artifact, never a committed mutation) until Approve.
func TestEngineGraphProofOutcomeCarriesEvidence(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"note.txt": "foo\nbar\nbaz\n",
	}, &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>",
	}}})
	gem := runGate(m, "change bar to qux in @note.txt")
	if gem.res == nil || gem.res.Proof == nil {
		t.Fatal("expected a runtime proof")
	}
	if gem.res.Proof.Outcome != execution.OutcomePendingApproval {
		t.Fatalf("pre-approval proof outcome = %q, want pending_approval (a valid held artifact, no committed mutation)", gem.res.Proof.Outcome)
	}
	if len(gem.res.Proof.Mutations) != 0 {
		t.Fatalf("pre-approval proof mutations = %d, want 0", len(gem.res.Proof.Mutations))
	}
}
