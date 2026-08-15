package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── Phase 11 — Execution graph UI wiring ───────────────────────────────────

// TestEngineGraphStaleClearedByPlainHot verifies a plain $hot (the direct
// executor outside the strategy layer) clears a stale non-terminal strategy
// graph so it can never be recorded into by a different operation — $inspect
// stays truthful about which operation executed.
func TestEngineGraphStaleClearedByPlainHot(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	})
	m.routePromptDirective("fix extra contents in @index.html")
	if m.lastStrategyGraph == nil {
		t.Fatal("strategy graph must be compiled at dispatch")
	}
	// Leave the graph non-terminal (proposal recorded, apply not reached) to
	// simulate a $prompt whose operation was interrupted.
	m.recordStrategyGraphProposal()
	if m.lastStrategyGraph.Terminal() {
		t.Fatal("precondition: graph must be non-terminal")
	}
	// A plain $hot (not PROMPT-branded) outside /build returns early AFTER the
	// stale-graph guard, proving the guard runs unconditionally for $hot.
	m.hotfixBranding = ""
	m.resolver.Set(modes.ModeReview)
	if cmd := m.handleHotfixCmd("fix the year in @LICENSE"); cmd != nil {
		t.Fatal("plain $hot outside /build must return nil")
	}
	if m.lastStrategyGraph != nil {
		t.Fatal("plain $hot must clear a stale non-terminal strategy graph")
	}
}

// TestEngineGraphCompiledAtDispatch verifies the explicit execution graph is
// compiled deterministically BEFORE any model invocation and driven through
// its initial deterministic nodes at $prompt dispatch.
func TestEngineGraphCompiledAtDispatch(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<html><body><p>one</p><p>two</p></body></html>",
	})

	cmd := m.routePromptDirective("fix extra contents in @index.html")
	if cmd == nil {
		t.Fatal("targeted mutation did not dispatch")
	}

	g := m.lastStrategyGraph
	if g == nil {
		t.Fatal("strategy graph was not compiled at dispatch")
	}
	if g.Strategy != strategy.TargetedMutation {
		t.Fatalf("graph strategy = %s, want targeted_mutation", g.Strategy)
	}
	// Topology: resolve → read → reason → propose → approve → mutate → verify.
	for _, want := range []string{"resolve_target", "read_target", "reason",
		"propose", "approve", "mutate", "verify"} {
		if !g.Has(strategy.NodeKind(want)) {
			t.Errorf("graph missing node kind %s", want)
		}
	}
	if g.Has(strategy.NodeGatherEvidence) {
		t.Error("single-file targeted mutation must not gather repository evidence")
	}
	// The graph is a decision record: it exists before any model call and
	// exposes the expected invocation count.
	if g.ExpectedInvocations != 1 {
		t.Errorf("expected invocations = %d, want 1", g.ExpectedInvocations)
	}
	if n := g.First(strategy.NodeReason); n == nil || !n.RequiresModel || n.Invocation != 1 || n.Contract == "" {
		t.Fatalf("reason node must carry the explicit InvocationContract: %+v", n)
	}
	// resolve_target is deterministically complete at dispatch; nothing has
	// mutated yet.
	if n := g.First(strategy.NodeResolveTarget); n.State != strategy.NodeComplete {
		t.Errorf("resolve_target state = %s, want complete", n.State)
	}
	if n := g.First(strategy.NodeMutate); n.State.Terminal() {
		t.Error("mutate node must not be terminal before any mutation")
	}
}

// TestEngineGraphClarification verifies the ambiguous-target graph stops at the
// human clarify boundary with zero model invocations.
func TestEngineGraphClarification(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"src/index.html":    "<html></html>",
		"public/index.html": "<html></html>",
	})

	cmd := m.routePromptDirective("fix the header in @index.html")
	if cmd != nil {
		t.Fatal("ambiguous target returned a cmd — must stop before execution")
	}
	g := m.lastStrategyGraph
	if g == nil {
		t.Fatal("clarification graph was not compiled")
	}
	if g.Strategy != strategy.HumanClarification {
		t.Fatalf("graph strategy = %s, want human_clarification", g.Strategy)
	}
	if g.ExpectedInvocations != 0 || g.ModelNodeCount() != 0 {
		t.Fatalf("clarification must expect 0 model invocations (expected=%d nodes=%d)",
			g.ExpectedInvocations, g.ModelNodeCount())
	}
	if !g.Has(strategy.NodeClarify) {
		t.Fatal("clarification graph must contain a clarify boundary")
	}
	if n := g.First(strategy.NodeClarify); n.State != strategy.NodeWaiting {
		t.Errorf("clarify node state = %s, want waiting (human boundary)", n.State)
	}
	if !g.AwaitingHuman() {
		t.Error("clarification graph must pause awaiting human")
	}
	if m.resolver.Current() != modes.ModeAsk {
		t.Errorf("mode = /%s, want /ask (no transition on clarification)", m.resolver.Current())
	}
}

// TestEngineGraphDeterministic verifies the zero-model deterministic graph.
func TestEngineGraphDeterministic(t *testing.T) {
	m, _ := engineFirstHarness(t, nil)

	m.routePromptDirective("create a .gitignore file")
	g := m.lastStrategyGraph
	if g == nil {
		t.Fatal("deterministic graph was not compiled")
	}
	if g.Strategy != strategy.DirectDeterministic {
		t.Fatalf("graph strategy = %s, want direct_deterministic", g.Strategy)
	}
	if g.ExpectedInvocations != 0 || g.ModelNodeCount() != 0 {
		t.Fatalf("deterministic graph must expect 0 model invocations")
	}
	if g.Has(strategy.NodeReason) || g.Has(strategy.NodeApprove) {
		t.Fatal("deterministic graph must not contain reasoning or approval nodes")
	}
	// resolve + read are deterministically complete; mutate + verify wait for
	// the apply terminal.
	if n := g.First(strategy.NodeResolveTarget); n.State != strategy.NodeComplete {
		t.Errorf("resolve_target = %s, want complete", n.State)
	}
	if n := g.First(strategy.NodeReadTarget); n.State != strategy.NodeComplete {
		t.Errorf("read_target = %s, want complete", n.State)
	}
}

// TestEngineGraphTerminalRecording verifies the graph is driven from the real
// runtime boundaries: proposal (reason+propose), apply (mutate+verify), and
// the terminal reconcile to complete.
func TestEngineGraphTerminalRecording(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<p>one</p><p>two</p>",
	})
	m.routePromptDirective("remove the duplicate paragraph in @index.html")
	g := m.lastStrategyGraph

	// The bounded model produced its artifact: reason + propose complete.
	m.recordStrategyGraphProposal()
	if n := g.First(strategy.NodeReason); n.State != strategy.NodeComplete {
		t.Errorf("reason state = %s, want complete", n.State)
	}
	if n := g.First(strategy.NodePropose); n.State != strategy.NodeComplete {
		t.Errorf("propose state = %s, want complete", n.State)
	}
	// The human approved and the MutationSet committed: approve + mutate +
	// verify complete, graph terminal complete.
	m.recordStrategyGraphMutation(true)
	if !g.Terminal() {
		t.Fatalf("graph not terminal after successful apply (state=%s)", g.State)
	}
	if g.State != strategy.GraphComplete {
		t.Fatalf("graph state = %s, want complete", g.State)
	}
	for _, n := range g.Nodes {
		if n.State != strategy.NodeComplete {
			t.Errorf("node %s (%s) state = %s, want complete", n.ID, n.Kind, n.State)
		}
	}
	if errs := strategy.CheckInvariants(m.lastExecutionStrategy, g); len(errs) > 0 {
		t.Fatalf("invariants violated after full execution: %v", errs)
	}
}

// TestEngineGraphModelFailure verifies a generation failure marks the reason
// node failed and the graph reaches the terminal failed state.
func TestEngineGraphModelFailure(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	})
	m.routePromptDirective("fix @index.html")
	g := m.lastStrategyGraph

	m.recordStrategyGraphModelFailed(errors.New("artifact rejected"))
	if g.State != strategy.GraphFailed {
		t.Fatalf("graph state = %s, want failed", g.State)
	}
	if n := g.First(strategy.NodeReason); n.State != strategy.NodeFailed {
		t.Errorf("reason state = %s, want failed", n.State)
	}
}

// TestEngineGraphMutationFailureRollback verifies a failed apply marks the
// mutate node failed (MutationSet rolled back) and the graph reaches failed.
func TestEngineGraphMutationFailureRollback(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	})
	m.routePromptDirective("fix @index.html")
	g := m.lastStrategyGraph
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
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	})
	m.routePromptDirective("fix @index.html")
	g := m.lastStrategyGraph

	m.cancelStrategyGraph("user interrupted")
	if g.State != strategy.GraphCancelled {
		t.Fatalf("graph state = %s, want cancelled", g.State)
	}
	// A terminal graph never re-enters: later terminal records are no-ops.
	m.recordStrategyGraphMutation(true)
	if g.State != strategy.GraphCancelled {
		t.Fatalf("graph state after late record = %s, want cancelled (immutable)", g.State)
	}
}

// TestEngineGraphInspectRenders verifies $inspect renders the compiled graph
// with execution facts only — node sequence, states, expected invocations and
// escalation history.
func TestEngineGraphInspectRenders(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	})
	m.routePromptDirective("remove the footer from @missing.html")
	out := renderStrategyGraph(m.lastStrategyGraph)

	for _, want := range []string{"execution-graph", "strategy=human_clarification",
		"state=awaiting_human", "resolve_target", "clarify", "human", "expected-invocations=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderStrategyGraph missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "mutate") {
		t.Error("clarification graph must not render a mutate node")
	}
	// The escalation evidence is part of the record.
	if !strings.Contains(out, "human_clarification") {
		t.Error("renderStrategyGraph must expose the escalation record")
	}
}

// TestEngineGraphExecutionProofCarriesStrategy verifies the ExecutionProof
// records the engine-first strategy decision (section 19).
func TestEngineGraphExecutionProofCarriesStrategy(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	})
	m.routePromptDirective("fix extra contents in @index.html")
	m.lastExecutionSnapshot = execution.TelemetrySnapshot{OpID: "test-op"}

	m.recordHotfixProposalProof("index.html", true, true, 100, 200)
	if m.lastExecutionProof.Strategy != "targeted_mutation" {
		t.Fatalf("proof strategy = %q, want targeted_mutation", m.lastExecutionProof.Strategy)
	}
	if m.lastExecutionProof.ProviderInvocations != 0 {
		t.Errorf("proof invocations = %d, want 0 (snapshot has no provider stage)", m.lastExecutionProof.ProviderInvocations)
	}
}

// TestEngineGraphNoStrategyGraphForDirectExecutor verifies the recording
// helpers are strict no-ops when no strategy graph is active (e.g. $hot and
// explicit /build run outside the strategy layer and keep their existing
// $inspect surfaces).
func TestEngineGraphNoStrategyGraphForDirectExecutor(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	})
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
