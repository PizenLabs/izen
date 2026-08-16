package ui

import (
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── EXECUTION GRAPH (retained UI projection) ────────────────────────────────
//
// The execution graph for the engine-first strategy layer is compiled and
// driven by the RUNTIME now: the RuntimeExecutor records real graph boundaries
// in its ExecutionProof (internal/execution). The UI-side record functions
// below remain ONLY for the legacy hotfix/build paths that still run outside
// the strategy layer; the migrated gated-execution path renders the runtime's
// proof instead.

// recordStrategyGraphProposal marks the reasoning and proposal nodes complete
// at the mutation-proposal terminal: the bounded model produced its artifact
// and the deterministic extraction validated it. It also closes the read node
// — by the time a proposal exists, the target was read.
func (m *model) recordStrategyGraphProposal() {
	g := m.lastStrategyGraph
	if g == nil || g.Terminal() {
		return
	}
	if n := g.First(strategy.NodeReadTarget); n != nil && !n.State.Terminal() {
		g.Complete(n.ID, "target content read")
	}
	if n := g.First(strategy.NodeReason); n != nil {
		g.Complete(n.ID, "bounded model artifact produced")
	}
	if n := g.First(strategy.NodePropose); n != nil {
		g.Complete(n.ID, "artifact extracted and validated")
	}
}

// recordStrategyGraphModelFailed marks the reasoning node failed at the
// generation terminal: the model did not produce a usable artifact under the
// InvocationContract.
func (m *model) recordStrategyGraphModelFailed(err error) {
	g := m.lastStrategyGraph
	if g == nil || g.Terminal() {
		return
	}
	reason := "model artifact rejected by the InvocationContract"
	if err != nil {
		reason = err.Error()
	}
	if n := g.First(strategy.NodeReason); n != nil {
		g.Fail(n.ID, reason)
		return
	}
	g.Cancel(reason)
}

// cancelStrategyGraph drives the compiled graph to the terminal cancelled
// state. It is a no-op on an already-terminal graph — a committed mutation can
// never be cancelled.
func (m *model) cancelStrategyGraph(reason string) {
	g := m.lastStrategyGraph
	if g == nil || g.Terminal() {
		return
	}
	g.Cancel(reason)
}

// recordStrategyGraphMutation records the apply terminal: committed only when
// every mutation applied and the deterministic verification passed. On failure
// the owning MutationSet was rolled back and the graph reaches the terminal
// failed state.
func (m *model) recordStrategyGraphMutation(success bool) {
	g := m.lastStrategyGraph
	if g == nil || g.Terminal() {
		return
	}
	if success {
		// The human approved the proposal: close the approval boundary.
		if n := g.First(strategy.NodeApprove); n != nil {
			g.Complete(n.ID, "human approved the proposal")
		}
		for _, n := range g.All(strategy.NodeMutate) {
			g.Complete(n.ID, "mutation applied and recorded in the MutationSet")
		}
		if n := g.First(strategy.NodeVerify); n != nil {
			g.Complete(n.ID, "deterministic verification passed")
		}
		return
	}
	if n := g.First(strategy.NodeMutate); n != nil {
		g.Fail(n.ID, "mutation failed; MutationSet rolled back")
		return
	}
	if n := g.First(strategy.NodeVerify); n != nil {
		g.Fail(n.ID, "verification failed; MutationSet rolled back")
		return
	}
	g.Cancel("mutation cancelled")
}
