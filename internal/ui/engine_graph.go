package ui

import (
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── PHASE 11 — EXECUTION GRAPH WIRING ──────────────────────────────────────
//
// The strategy layer compiles an explicit ExecutionGraph (typed nodes:
// resolve_target → read_target → reason → propose → approve → mutate → verify,
// or the bounded topology of the selected strategy) BEFORE any model
// invocation. This file drives that graph from the real runtime boundaries the
// UI reaches: dispatch (resolve/read), the mutation-proposal terminal (reason/
// propose), the apply/verification terminal (mutate/verify), and the human
// clarification boundary (clarify). Node states are recorded ONLY at real
// boundaries — never inferred from a spinner — and every record is a no-op
// when no strategy graph is active ($hot, explicit /build, … run outside the
// strategy layer and keep their existing $inspect surfaces).
//
// The graph is UI-agnostic: it is a pure engine record the UI projects. No
// presentation concern lives in the graph.

// recordStrategyGraph compiles the explicit execution graph for the strategy
// decision and records the initial deterministic nodes (target resolution and,
// where deterministic, target reading). It is called at dispatch, before any
// model invocation, so the graph exists the moment the engine decides WHAT to
// execute.
func (m *model) recordStrategyGraph(p strategy.ExecutionStrategyProfile) {
	m.lastStrategyGraph = strategy.Compile(p)
	g := m.lastStrategyGraph
	if g == nil {
		return
	}
	g.Start()

	switch p.Strategy {
	case strategy.HumanClarification:
		// Deterministic resolution was attempted; the operation stops at the
		// human boundary. No file is read, no model is called.
		if n := g.First(strategy.NodeResolveTarget); n != nil {
			g.Complete(n.ID, "target resolution attempted deterministically")
		}
		if n := g.First(strategy.NodeClarify); n != nil {
			g.Wait(n.ID, p.StrategyReason)
		}
	case strategy.TargetedReasoning:
		// The /ask chat path supplies the explicit target content through the
		// governed context planner; resolve and read are deterministic at
		// dispatch. The reason node completes at the /ask stream terminal.
		if n := g.First(strategy.NodeResolveTarget); n != nil {
			g.Complete(n.ID, "target resolved deterministically")
		}
		if n := g.First(strategy.NodeReadTarget); n != nil {
			g.Complete(n.ID, "target content supplied by the governed context planner")
		}
	case strategy.DirectDeterministic:
		// Deterministic template create: the engine already resolved and read
		// the template; mutate + verify complete at the apply terminal.
		if n := g.First(strategy.NodeResolveTarget); n != nil {
			g.Complete(n.ID, "template target resolved deterministically")
		}
		if n := g.First(strategy.NodeReadTarget); n != nil {
			g.Complete(n.ID, "template content enumerated deterministically")
		}
	case strategy.TargetedMutation, strategy.RepositoryInvestigation, strategy.MultiFilePlanning:
		// Target resolution completed deterministically by the selector. The
		// remaining nodes are driven at their real runtime boundaries.
		if n := g.First(strategy.NodeResolveTarget); n != nil {
			g.Complete(n.ID, "target resolution completed deterministically")
		}
	}
}

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
