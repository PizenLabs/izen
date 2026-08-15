package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── Phase 9B — multi-file execution graph regression tests ─────────────────
//
// These tests drive the REAL multi-file $hot path: handleHotfixCmd → target
// resolution → ExecutionGraph + one MutationSet → Phase A generation (one
// provider invocation per node) → proposal → approval → Phase B apply →
// verify → COMMIT or ROLLBACK. They never bypass the real execution path.

const (
	multiAOrig  = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>A</h1>\n  <p>duplicate A</p>\n</body>\n</html>\n"
	multiAFixed = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>A</h1>\n</body>\n</html>\n"
	multiBOrig  = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>B</h1>\n  <p>duplicate B</p>\n</body>\n</html>\n"
	multiBFixed = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>B</h1>\n</body>\n</html>\n"
)

// twoNodeMock returns a provider answering exactly two node invocations.
func twoNodeMock() *mockProvider {
	return &mockProvider{responses: []*ai.Response{
		{Content: "```html\n" + multiAFixed + "```", TokenInput: 100, TokenOutput: 900},
		{Content: "```html\n" + multiBFixed + "```", TokenInput: 200, TokenOutput: 700},
	}}
}

// multiHotfixFiles writes the standard two independent fixtures.
func multiHotfixFiles(t *testing.T) {
	t.Helper()
	if err := os.WriteFile("a.html", []byte(multiAOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.html", []byte(multiBOrig), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dispatchMultiProposal dispatches a multi-file $hot and runs the Phase A
// worker to its proposal message.
func dispatchMultiProposal(t *testing.T, m *model, prompt string) multiHotfixProposalMsg {
	t.Helper()
	cmd := m.handleHotfixCmd(prompt)
	if cmd == nil {
		t.Fatalf("handleHotfixCmd returned nil for %q", prompt)
	}
	msgs := runBuildCmdsFiltered(t, cmd)
	for _, msg := range msgs {
		if mp, ok := msg.(multiHotfixProposalMsg); ok {
			return mp
		}
	}
	t.Fatalf("no multiHotfixProposalMsg produced for %q: %+v", prompt, msgs)
	return multiHotfixProposalMsg{}
}

// approveAndApplyMulti stages the proposal through the approval gate and runs
// the Phase B apply + terminal exactly as the Alt+A key path does. Returns the
// model, the committed/rolled-back boundary, and the terminal result.
func approveAndApplyMulti(t *testing.T, m *model, mp multiHotfixProposalMsg) (*model, *execution.MutationSet, buildResultMsg) {
	t.Helper()
	upd, _ := m.Update(mp)
	m = upd.(*model)
	if m.pendingHotfixGraph == nil {
		t.Fatal("prepared graph not staged for approval")
	}
	graph := m.pendingHotfixGraph
	ms := graph.MutationSet
	// Mirrors the Alt+A approval key: clear the gate, apply ALL nodes under the
	// graph's single MutationSet.
	m.pendingHotfixGraph = nil
	m.pendingProposals = nil
	m.resolveApprovalState()
	cmd := m.applyMultiHotfixGraph(graph)
	m.hotfixActive = true
	res := cmd()
	br, ok := res.(buildResultMsg)
	if !ok {
		t.Fatalf("apply returned %T, want buildResultMsg", res)
	}
	upd, _ = m.Update(res)
	m = upd.(*model)
	return m, ms, br
}

// TestMultiHotfix_TwoExplicitFilesTwoNodes is #1: two explicit @file targets
// produce exactly two graph nodes in prompt order.
func TestMultiHotfix_TwoExplicitFilesTwoNodes(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	g := mp.Graph
	if g == nil || len(g.Nodes) != 2 {
		t.Fatalf("graph nodes = %d, want 2", nodeCount(g))
	}
	got := g.Targets()
	if len(got) != 2 || got[0] != "a.html" || got[1] != "b.html" {
		t.Fatalf("targets = %v, want [a.html b.html]", got)
	}
}

func nodeCount(g *execution.ExecutionGraph) int {
	if g == nil {
		return 0
	}
	return len(g.Nodes)
}

// TestMultiHotfix_DuplicateTargetOneNode is #2: a duplicated target collapses
// into exactly ONE node — the language parser de-duplicates scopes, so a
// duplicate request is a single-file $hot, never a two-node graph.
func TestMultiHotfix_DuplicateTargetOneNode(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	cmd := m.handleHotfixCmd("remove the duplicated text from @a.html and @a.html")
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil")
	}
	msgs := runBuildCmdsFiltered(t, cmd)
	var hp hotfixProposalMsg
	multiSeen := false
	for _, msg := range msgs {
		if _, ok := msg.(multiHotfixProposalMsg); ok {
			multiSeen = true
		}
		if p, ok := msg.(hotfixProposalMsg); ok {
			hp = p
		}
	}
	if multiSeen {
		t.Fatal("duplicate target must not create a multi-file graph")
	}
	if hp.Patch == nil || hp.Patch.File != "a.html" {
		t.Fatalf("duplicate target must collapse to one single-file patch, got %+v", hp.Patch)
	}
}

// TestMultiHotfix_DeterministicNodeOrdering is #3: the node order equals the
// prompt's first-appearance order — never sorted, never map-iteration order.
func TestMultiHotfix_DeterministicNodeOrdering(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "fix @b.html and @a.html")
	got := mp.Graph.Targets()
	if len(got) != 2 || got[0] != "b.html" || got[1] != "a.html" {
		t.Fatalf("targets = %v, want [b.html a.html] (prompt order)", got)
	}
	// Determinism: resolving the same request twice yields the same graph.
	mp2 := dispatchMultiProposal(t, m, "fix @b.html and @a.html")
	g2 := mp2.Graph.Targets()
	if len(g2) != 2 || g2[0] != got[0] || g2[1] != got[1] {
		t.Fatalf("graph(b) != graph(a): %v vs %v", g2, got)
	}
}

// TestMultiHotfix_AmbiguousTargetNoProvider is #4: a target set that cannot be
// resolved stops before any provider invocation and any mutation.
func TestMultiHotfix_AmbiguousTargetNoProvider(t *testing.T) {
	mock := &mockProvider{}
	m := hotfixTruthModel(t, mock)
	cmd := m.handleHotfixCmd("fix @Server.Handle and @Client.Handle")
	if cmd != nil {
		t.Fatalf("ambiguous multi request must not dispatch, got cmd")
	}
	if mock.callCount != 0 {
		t.Fatalf("provider invoked %d times, want 0", mock.callCount)
	}
}

// TestMultiHotfix_MissingTargetDeterministicFailure is #5: a named target that
// does not exist (and is not a creation template) fails deterministically
// before any provider call.
func TestMultiHotfix_MissingTargetDeterministicFailure(t *testing.T) {
	mock := &mockProvider{}
	m := hotfixTruthModel(t, mock)
	multiHotfixFiles(t)
	cmd := m.handleHotfixCmd("fix @a.html and @missing.html")
	if cmd != nil {
		t.Fatalf("missing target must not dispatch, got cmd")
	}
	if mock.callCount != 0 {
		t.Fatalf("provider invoked %d times, want 0", mock.callCount)
	}
}

// TestMultiHotfix_TwoIndependentFilesOneMutationSet is #8: the whole graph
// executes under exactly ONE MutationSet — the same boundary the engine holds.
func TestMultiHotfix_TwoIndependentFilesOneMutationSet(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	msAtProposal := m.execEng.MutationSet()
	if mp.Graph.MutationSet == nil {
		t.Fatal("graph must own a MutationSet")
	}
	if mp.Graph.MutationSet != msAtProposal {
		t.Fatal("graph must own the SAME MutationSet the engine holds for this operation")
	}
	if mp.Graph.MutationSet.State != execution.MutationPending {
		t.Fatalf("MutationSet state = %q, want pending", mp.Graph.MutationSet.State)
	}
	// Phase A recorded no targets into the boundary (nothing applied yet).
	if len(mp.Graph.MutationSet.Targets) != 0 {
		t.Fatalf("pre-apply MutationSet must own no targets: %v", mp.Graph.MutationSet.Targets)
	}
}

// TestMultiHotfix_AllFilesSucceedOneCommit is #9: two files succeed → one
// commit, both files changed, the graph is terminal committed, and no future
// rollback can undo them.
func TestMultiHotfix_AllFilesSucceedOneCommit(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	m, ms, br := approveAndApplyMulti(t, m, mp)
	if br.exitCode != 0 {
		t.Fatalf("apply failed: %v", br.err)
	}
	if !ms.Committed() {
		t.Fatalf("MutationSet state = %q, want committed", ms.State)
	}
	if m.activeGraph != nil || m.pendingHotfixGraph != nil {
		t.Fatal("graph pointers not cleared at terminal")
	}
	if m.lastExecutionGraph == nil || m.lastExecutionGraph.State != execution.GraphCommitted {
		t.Fatalf("graph not retained as committed: %+v", m.lastExecutionGraph)
	}
	for _, path := range []string{"a.html", "b.html"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "duplicate") {
			t.Fatalf("%s was not mutated:\n%s", path, data)
		}
	}
	// A later rollback cannot undo a committed multi-file hotfix.
	if errs := m.execEng.RollbackTransaction(); len(errs) != 0 {
		t.Fatalf("rollback after commit errored: %v", errs)
	}
	data, _ := os.ReadFile("a.html")
	if strings.Contains(string(data), "duplicate") {
		t.Fatal("committed multi-file hotfix was rolled back")
	}
}

// TestMultiHotfix_SecondFileFailureRollsBackEverything is #10: node 1 applies
// successfully, node 2 fails at apply → the WHOLE MutationSet rolls back and
// file 1 returns to its pre-operation state.
func TestMultiHotfix_SecondFileFailureRollsBackEverything(t *testing.T) {
	big := strings.Repeat("<!-- filler line -->\n", 6000) // > 50KB
	mock := &mockProvider{responses: []*ai.Response{
		{Content: "```html\n" + multiAFixed + "```", TokenInput: 100, TokenOutput: 900},
		// Node 2: a well-formed but wrong-hunk diff against the large file —
		// extracts fine (Phase A passes), fails at apply (Phase B).
		{Content: "--- a/big.html\n+++ b/big.html\n@@ -99999,1 +99999,1 @@\n-nope\n+replacement\n", TokenInput: 50, TokenOutput: 30},
	}}
	m := hotfixTruthModel(t, mock)
	if err := os.WriteFile("a.html", []byte(multiAOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("big.html", []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	mp := dispatchMultiProposal(t, m, "fix @a.html and @big.html")
	m, ms, br := approveAndApplyMulti(t, m, mp)
	if br.exitCode == 0 {
		t.Fatal("precondition: second file apply unexpectedly succeeded")
	}
	if !ms.RolledBack() {
		t.Fatalf("MutationSet state = %q, want rolled_back", ms.State)
	}
	data, _ := os.ReadFile("a.html")
	if string(data) != multiAOrig {
		t.Fatalf("first file was not rolled back after second failure:\n%s", data)
	}
	bigData, _ := os.ReadFile("big.html")
	if string(bigData) != big {
		t.Fatal("failed node mutated big.html")
	}
	if m.lastExecutionGraph == nil || m.lastExecutionGraph.State != execution.GraphFailed {
		t.Fatalf("graph state = %+v, want failed", m.lastExecutionGraph)
	}
}

// TestMultiHotfix_CancellationBeforeApplyNoMutation is #11: cancelling the
// operation before Phase B runs leaves every file untouched.
func TestMultiHotfix_CancellationBeforeApplyNoMutation(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	upd, _ := m.Update(mp)
	m = upd.(*model)
	graph := m.pendingHotfixGraph
	m.pendingHotfixGraph = nil
	cmd := m.applyMultiHotfixGraph(graph)
	// Cancel the apply operation before the worker runs.
	m.activeOp.Cancel()
	m.hotfixActive = true
	res := cmd()
	br, _ := res.(buildResultMsg)
	m.Update(res)
	if br.exitCode == 0 {
		t.Fatal("precondition: cancelled apply unexpectedly succeeded")
	}
	want := map[string]string{"a.html": multiAOrig, "b.html": multiBOrig}
	for path, orig := range want {
		data, _ := os.ReadFile(path)
		if string(data) != orig {
			t.Fatalf("%s was mutated despite cancellation:\n%s", path, data)
		}
	}
	if graph.State != execution.GraphCancelled {
		t.Fatalf("graph state = %q, want cancelled", graph.State)
	}
}

// TestMultiHotfix_CancellationAfterApplyRollsBack is #12/#13: a mutation that
// was applied and then cancelled rolls back exactly the owned MutationSet —
// the already-written first file is restored and the second is untouched.
func TestMultiHotfix_CancellationAfterApplyRollsBack(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	graph := mp.Graph
	ms := graph.MutationSet
	if ms == nil {
		t.Fatal("graph must own a MutationSet")
	}
	if err := m.authorizeBuildExecution([]string{"a.html", "b.html"}, true); err != nil {
		t.Fatal(err)
	}
	// Node 1 applied (real PatchManager write into the graph's MutationSet).
	if err := m.execEng.Patches.Apply(graph.Nodes[0].Patch); err != nil {
		t.Fatalf("node1 apply failed: %v", err)
	}
	data, _ := os.ReadFile("a.html")
	if strings.Contains(string(data), "duplicate") {
		t.Fatal("precondition: node1 did not mutate")
	}
	// The cancelled terminal invokes RollbackTransaction on the owned set.
	if errs := m.execEng.RollbackTransaction(); len(errs) != 0 {
		t.Fatalf("rollback errors: %v", errs)
	}
	data, _ = os.ReadFile("a.html")
	if string(data) != multiAOrig {
		t.Fatalf("cancelled node1 was not rolled back:\n%s", data)
	}
	bData, _ := os.ReadFile("b.html")
	if string(bData) != multiBOrig {
		t.Fatal("untouched node2 was mutated")
	}
	if !ms.RolledBack() {
		t.Fatalf("MutationSet state = %q, want rolled_back", ms.State)
	}
}

// TestMultiHotfix_NoOrphanWorkers is #14: after the full terminal no worker
// remains live and the operation is released.
func TestMultiHotfix_NoOrphanWorkers(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	m, _, br := approveAndApplyMulti(t, m, mp)
	if br.exitCode != 0 {
		t.Fatalf("apply failed: %v", br.err)
	}
	if m.activeOp != nil {
		t.Fatalf("operation not released after terminal: %+v", m.activeOp)
	}
	snap := m.lastExecutionSnapshot
	if len(snap.LiveWorkers) != 0 {
		t.Fatalf("live workers after terminal: %v", snap.LiveWorkers)
	}
}

// TestMultiHotfix_OneInvocationPerNode is #15: each graph node gets exactly one
// provider invocation (two nodes → two invocations, in node order).
func TestMultiHotfix_OneInvocationPerNode(t *testing.T) {
	mock := twoNodeMock()
	m := hotfixTruthModel(t, mock)
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	if mock.callCount != 2 {
		t.Fatalf("provider invoked %d times, want exactly 2 (one per node)", mock.callCount)
	}
	// The proposal terminal finalizes the generation operation; its telemetry
	// snapshot carries the authoritative invocation count.
	upd, _ := m.Update(mp)
	m = upd.(*model)
	if snap := m.lastExecutionSnapshot; snap.Invocations != 2 {
		t.Fatalf("telemetry invocations = %d, want 2", snap.Invocations)
	}
	if mp.Graph == nil || mp.Graph.MutationSet == nil {
		t.Fatal("graph missing after dispatch")
	}
}

// TestMultiHotfix_ProviderUsageAggregates is #16: the aggregate input/output
// is the SUM of the per-node provider-reported usage — never duplicated.
func TestMultiHotfix_ProviderUsageAggregates(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	if mp.TokenInput != 300 {
		t.Fatalf("aggregate input = %d, want 300 (100+200)", mp.TokenInput)
	}
	if mp.TokenOutput != 1600 {
		t.Fatalf("aggregate output = %d, want 1600 (900+700)", mp.TokenOutput)
	}
	upd, cmd := m.Update(mp)
	m2 := upd.(*model)
	// Drain the dispatched TokenUsageMsg back into the model so the footer
	// accumulates the aggregate provider usage.
	for _, drained := range drainCmds(t, cmd) {
		r2, _ := m2.Update(drained)
		m2 = r2.(*model)
	}
	if m2.InputTokens != 300 {
		t.Errorf("footer InputTokens = %d, want 300", m2.InputTokens)
	}
	if m2.OutputTokens != 1600 {
		t.Errorf("footer OutputTokens = %d, want 1600", m2.OutputTokens)
	}
}

// TestMultiHotfix_NoDuplicatedContext is #17: each node's provider request
// carries ONLY its own file content — node A's request never contains node B's
// content.
func TestMultiHotfix_NoDuplicatedContext(t *testing.T) {
	mock := twoNodeMock()
	m := hotfixTruthModel(t, mock)
	multiHotfixFiles(t)
	dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	if len(mock.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(mock.requests))
	}
	if !strings.Contains(mock.requests[0].Messages[0].Content, "duplicate A") {
		t.Fatalf("node A request missing its own content:\n%s", mock.requests[0].Messages[0].Content)
	}
	if strings.Contains(mock.requests[0].Messages[0].Content, "duplicate B") {
		t.Fatal("node A request leaked node B's file content (context duplication)")
	}
	if !strings.Contains(mock.requests[1].Messages[0].Content, "duplicate B") {
		t.Fatalf("node B request missing its own content:\n%s", mock.requests[1].Messages[0].Content)
	}
	if strings.Contains(mock.requests[1].Messages[0].Content, "duplicate A") {
		t.Fatal("node B request leaked node A's file content (context duplication)")
	}
}

// TestMultiHotfix_PerNodeEvidence is #18/#19/#20: every node carries real
// artifact / apply / verify evidence.
func TestMultiHotfix_PerNodeEvidence(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	for _, n := range mp.Graph.Nodes {
		if !n.Evidence.ArtifactPresent || !n.Evidence.DiffPresent {
			t.Fatalf("node %s artifact evidence missing: %+v", n.Target, n.Evidence)
		}
	}
	m, _, br := approveAndApplyMulti(t, m, mp)
	if br.exitCode != 0 {
		t.Fatalf("apply failed: %v", br.err)
	}
	graph := m.lastExecutionGraph
	for _, n := range graph.Nodes {
		if !n.Evidence.ApplyExecuted || !n.Evidence.FilesystemChanged {
			t.Fatalf("node %s apply evidence missing: %+v", n.Target, n.Evidence)
		}
		if !n.Evidence.VerificationRun || !n.Evidence.VerificationPassed {
			t.Fatalf("node %s verify evidence missing: %+v", n.Target, n.Evidence)
		}
		if !n.Evidence.Outcome.MutationSucceeded() {
			t.Fatalf("node %s outcome = %q, want succeeded", n.Target, n.Evidence.Outcome)
		}
	}
}

// TestMultiHotfix_ExecutionProofExposesAllNodes is #21: the ExecutionProof
// carries every node with its real evidence, and the MutationSet terminal
// state.
func TestMultiHotfix_ExecutionProofExposesAllNodes(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	m, _, br := approveAndApplyMulti(t, m, mp)
	if br.exitCode != 0 {
		t.Fatalf("apply failed: %v", br.err)
	}
	p := m.lastExecutionProof
	if len(p.Targets) != 2 || p.Targets[0] != "a.html" || p.Targets[1] != "b.html" {
		t.Fatalf("proof targets = %v, want [a.html b.html]", p.Targets)
	}
	if len(p.Nodes) != 2 {
		t.Fatalf("proof nodes = %d, want 2", len(p.Nodes))
	}
	for _, n := range p.Nodes {
		if !n.ApplyExecuted || !n.FilesystemChanged || !n.VerificationPassed {
			t.Fatalf("proof node %s missing apply/verify evidence: %+v", n.Target, n)
		}
	}
	if p.MutationSetState != string(execution.MutationCommitted) {
		t.Fatalf("proof MutationSetState = %q, want committed", p.MutationSetState)
	}
	if p.RolledBack {
		t.Fatal("committed proof must not report rollback")
	}
}

// TestMultiHotfix_InspectExposesAggregateGraph is #22: $inspect renders the
// aggregate graph with the single MutationSet terminal state and every node.
func TestMultiHotfix_InspectExposesAggregateGraph(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	m, _, br := approveAndApplyMulti(t, m, mp)
	if br.exitCode != 0 {
		t.Fatalf("apply failed: %v", br.err)
	}
	rendered := renderExecutionGraph(m.lastExecutionGraph)
	for _, want := range []string{"state=committed", "mutation-set=committed", "a.html", "b.html"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("inspect graph missing %q:\n%s", want, rendered)
		}
	}
	// The $inspect directive routes without error.
	cmd := m.runInspectCmd("")
	if cmd == nil {
		t.Fatal("runInspectCmd returned nil")
	}
	res := cmd()
	if res != nil {
		t.Fatalf("inspect produced a stray message: %T", res)
	}
}

// TestMultiHotfix_ApplyAllOneMutationSet is #23: Apply All executes under ONE
// MutationSet and commits exactly once.
func TestMultiHotfix_ApplyAllOneMutationSet(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	_, ms, br := approveAndApplyMulti(t, m, mp)
	if br.exitCode != 0 {
		t.Fatalf("apply failed: %v", br.err)
	}
	if !ms.Committed() {
		t.Fatalf("Apply All MutationSet = %q, want committed", ms.State)
	}
	if len(ms.Transaction.Snapshots) != 0 {
		t.Fatalf("committed MutationSet still staged: %d", len(ms.Transaction.Snapshots))
	}
}

// TestMultiHotfix_NoSuccessLogBeforeApply is #24: no successful mutation
// result is logged before the apply executes.
func TestMultiHotfix_NoSuccessLogBeforeApply(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	upd, _ := m.Update(mp)
	m = upd.(*model)
	if last := m.logStore.LastResult(); last != nil {
		t.Fatalf("successful mutation logged before apply: %+v", last)
	}
}

// TestMultiHotfix_RetryStaysInsideSameNode is #25/#26: a truncated response
// retries INSIDE the same node (same graph, same MutationSet) and never
// advances to the next node or creates a second boundary.
func TestMultiHotfix_RetryStaysInsideSameNode(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{
		{Content: "", TokenInput: 10, TokenOutput: 1}, // node A: truncated → retry
		{Content: "```html\n" + multiAFixed + "```", TokenInput: 100, TokenOutput: 900},
		{Content: "```html\n" + multiBFixed + "```", TokenInput: 200, TokenOutput: 700},
	}}
	m := hotfixTruthModel(t, mock)
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	msAtProposal := m.execEng.MutationSet()
	if mock.callCount != 3 {
		t.Fatalf("provider invoked %d times, want 3 (node A + retry + node B)", mock.callCount)
	}
	// The retry stayed inside node A: the first two requests target a.html.
	if !strings.Contains(mock.requests[0].Messages[0].Content, "a.html") || !strings.Contains(mock.requests[1].Messages[0].Content, "a.html") {
		t.Fatalf("retry left node A: %s / %s", mock.requests[0].Messages[0].Content, mock.requests[1].Messages[0].Content)
	}
	if !strings.Contains(mock.requests[2].Messages[0].Content, "b.html") {
		t.Fatalf("third request should target node B: %s", mock.requests[2].Messages[0].Content)
	}
	// #26: the retry created NO second MutationSet.
	if mp.Graph.MutationSet != msAtProposal || mp.Graph.MutationSet != m.execEng.MutationSet() {
		t.Fatal("retry must not create another MutationSet")
	}
	if len(mp.Graph.Nodes) != 2 {
		t.Fatalf("graph nodes = %d, want 2 (retry stays in one node)", len(mp.Graph.Nodes))
	}
}

// TestMultiHotfix_DuplicateDispatchNoSecondGraph is #27: a duplicate dispatch
// supersedes the previous operation — only one graph is ever active.
func TestMultiHotfix_DuplicateDispatchNoSecondGraph(t *testing.T) {
	mock := twoNodeMock()
	m := hotfixTruthModel(t, mock)
	multiHotfixFiles(t)
	// First dispatch: the worker is NOT run, so no provider work.
	cmd1 := m.handleHotfixCmd("remove the duplicated text from @a.html and @b.html")
	if cmd1 == nil {
		t.Fatal("first dispatch returned nil")
	}
	g1 := m.activeGraph
	// Second dispatch supersedes the first operation.
	cmd2 := m.handleHotfixCmd("remove the duplicated text from @a.html and @b.html")
	if cmd2 == nil {
		t.Fatal("second dispatch returned nil")
	}
	g2 := m.activeGraph
	if g1 == nil || g2 == nil {
		t.Fatal("graph missing after dispatch")
	}
	if g1 == g2 {
		t.Fatal("duplicate dispatch must supersede with a new graph")
	}
	// Only the SECOND graph is active; running it yields one proposal.
	msgs := runBuildCmdsFiltered(t, cmd2)
	var mp *multiHotfixProposalMsg
	for _, msg := range msgs {
		if p, ok := msg.(multiHotfixProposalMsg); ok {
			mp = &p
		}
	}
	if mp == nil {
		t.Fatal("no proposal from the active graph")
	}
	if mock.callCount != 2 {
		t.Fatalf("provider invoked %d times, want 2 (only active graph)", mock.callCount)
	}
	if m.activeGraph != g2 {
		t.Fatal("active graph is not the latest dispatch")
	}
}

// TestMultiHotfix_CtrlCCancelsAllGraphWorkers is #28: Ctrl+C propagates into
// the Phase A provider work, no node completes, and no file mutates.
func TestMultiHotfix_CtrlCCancelsAllGraphWorkers(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	m := hotfixTruthModel(t, &mockProvider{})
	m.provider = bp
	multiHotfixFiles(t)
	cmd := m.handleHotfixCmd("remove the duplicated text from @a.html and @b.html")
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil")
	}
	graph := m.activeGraph
	if graph == nil {
		t.Fatal("no active graph")
	}
	// Run the Phase A worker in the background so the provider blocks.
	ch := runBuildCmdsFilteredBackground(cmd)
	// Wait for the provider to actually block, then cancel (Ctrl+C).
	select {
	case <-bp.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider never blocked")
	}
	m.cancelActiveOperation("ctrl-c")
	select {
	case <-bp.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was never cancelled")
	}
	// The worker must terminate with a cancelled proposal.
	var mp *multiHotfixProposalMsg
	deadline := time.After(5 * time.Second)
	for mp == nil {
		select {
		case msg, ok := <-ch:
			if !ok {
				t.Fatal("worker channel closed without a proposal")
			}
			if p, ok := msg.(multiHotfixProposalMsg); ok {
				mp = &p
			}
		case <-deadline:
			t.Fatal("worker did not terminate after cancellation")
		}
	}
	if mp.Err == nil {
		t.Fatal("cancelled generation must carry an error")
	}
	want := map[string]string{"a.html": multiAOrig, "b.html": multiBOrig}
	for path, orig := range want {
		data, _ := os.ReadFile(path)
		if string(data) != orig {
			t.Fatalf("%s was mutated despite cancellation:\n%s", path, data)
		}
	}
	if graph.State != execution.GraphCancelled {
		t.Fatalf("graph state = %q, want cancelled", graph.State)
	}
}

// TestMultiHotfix_DropRollsBackGraph is #29: /drop rolls back the owned
// MutationSet and discards the graph.
func TestMultiHotfix_DropRollsBackGraph(t *testing.T) {
	m := hotfixTruthModel(t, twoNodeMock())
	multiHotfixFiles(t)
	mp := dispatchMultiProposal(t, m, "remove the duplicated text from @a.html and @b.html")
	graph := mp.Graph
	if err := m.authorizeBuildExecution([]string{"a.html", "b.html"}, true); err != nil {
		t.Fatal(err)
	}
	if err := m.execEng.Patches.Apply(graph.Nodes[0].Patch); err != nil {
		t.Fatalf("node1 apply failed: %v", err)
	}
	// /drop: discard what I am about to do — roll back the graph's MutationSet.
	m.discardPendingAction()
	data, _ := os.ReadFile("a.html")
	if string(data) != multiAOrig {
		t.Fatalf("/drop did not roll back the applied node:\n%s", data)
	}
	if m.activeGraph != nil || m.pendingHotfixGraph != nil {
		t.Fatal("/drop must discard the execution graph")
	}
	if graph.MutationSet == nil || !graph.MutationSet.Terminal() {
		t.Fatalf("/drop must leave the MutationSet terminal: %+v", graph.MutationSet)
	}
}

// TestMultiHotfix_ClearPreservesSessionContract is #30: /clear clears the
// visible presentation but preserves the active session contract — staged
// plan tasks and the staged graph survive (discarding pending mutation state
// is /drop's job, never /clear's).
func TestMultiHotfix_ClearPreservesSessionContract(t *testing.T) {
	m := clearTestModel()
	taskCount := len(m.sess.CurrentTasks)
	// Stage a prepared graph the way the multi proposal terminal does.
	g := execution.NewExecutionGraph("op-clear", []execution.Target{{Path: "a.html", Role: execution.TargetExplicit}}, nil)
	g.Transition(execution.GraphReady)
	m.activeGraph = g
	m.pendingHotfixGraph = g
	m.pendingProposals = []SemanticProposal{{ID: "hotfix-n1", Target: SemanticTarget{QualifiedName: "a.html"}}}
	// /clear = clear what I SEE. The session contract survives.
	m.resetTransientInteraction()
	if len(m.sess.CurrentTasks) != taskCount {
		t.Fatalf("/clear destroyed staged session tasks: %d -> %d", taskCount, len(m.sess.CurrentTasks))
	}
	if m.sess.Objective == "" {
		t.Fatal("/clear destroyed the session objective")
	}
	if m.pendingHotfixGraph == nil {
		t.Fatal("/clear must preserve the staged graph (that is /drop's job)")
	}
	if m.activeGraph == nil {
		t.Fatal("/clear must not clear the in-flight graph")
	}
}

// TestMultiHotfix_EndToEnd is the mandatory full-path test: $hot → target
// resolution → graph → one MutationSet → proposal → approval → apply →
// verify → commit.
func TestMultiHotfix_EndToEnd(t *testing.T) {
	mock := twoNodeMock()
	m := hotfixTruthModel(t, mock)
	multiHotfixFiles(t)
	// 1. $hot dispatch → target resolution → graph → one MutationSet.
	cmd := m.handleHotfixCmd("remove the duplicated text from @a.html and @b.html")
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil")
	}
	if m.activeGraph == nil || len(m.activeGraph.Nodes) != 2 {
		t.Fatalf("no two-node graph at dispatch: %+v", m.activeGraph)
	}
	ms := m.activeGraph.MutationSet
	if ms == nil || ms != m.execEng.MutationSet() {
		t.Fatal("graph must own the single engine MutationSet")
	}
	// 2. Phase A generation.
	msgs := runBuildCmdsFiltered(t, cmd)
	var mp multiHotfixProposalMsg
	for _, msg := range msgs {
		if p, ok := msg.(multiHotfixProposalMsg); ok {
			mp = p
		}
	}
	if mp.Err != nil {
		t.Fatalf("Phase A failed: %v", mp.Err)
	}
	if mock.callCount != 2 {
		t.Fatalf("provider invocations = %d, want 2", mock.callCount)
	}
	if len(mp.Proposals) != 2 {
		t.Fatalf("proposals = %d, want 2", len(mp.Proposals))
	}
	// 3. Proposal → approval.
	m, _, br := approveAndApplyMulti(t, m, mp)
	// 4. Apply → verify → commit.
	if br.exitCode != 0 {
		t.Fatalf("apply failed: %v", br.err)
	}
	if !ms.Committed() {
		t.Fatalf("MutationSet = %q, want committed", ms.State)
	}
	if m.lastExecutionGraph == nil || m.lastExecutionGraph.State != execution.GraphCommitted {
		t.Fatalf("graph not committed: %+v", m.lastExecutionGraph)
	}
	for _, n := range m.lastExecutionGraph.Nodes {
		if !n.Evidence.Verify() || !n.Evidence.ApplyExecutedChanged() {
			t.Fatalf("node %s evidence incomplete: %+v", n.Target, n.Evidence)
		}
	}
	for _, path := range []string{"a.html", "b.html"} {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "duplicate") {
			t.Fatalf("%s not mutated", path)
		}
	}
	if m.activeOp != nil {
		t.Fatal("operation not released at terminal")
	}
}
