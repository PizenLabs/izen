package strategy

import "fmt"

// ── PHASE 11 — EFFICIENCY INVARIANTS ───────────────────────────────────────
//
// The engine-first contract is auditable: a strategy profile plus its compiled
// graph must satisfy the minimum-sufficient-execution invariants, or the
// combination is a bug. CheckInvariants validates the pairs in tests and at
// diagnostic time. It returns the violated invariant descriptions — an empty
// result means every invariant held.

// CheckInvariants validates the section-23 efficiency invariants of a
// (profile, compiled graph) pair. The graph must be the graph produced by
// Compile(p) for the invariants to be meaningful. It is pure and side-effect
// free.
func CheckInvariants(p ExecutionStrategyProfile, g *ExecutionGraph) []string {
	var violations []string

	if g == nil {
		return []string{"compiled graph is nil"}
	}

	// One strategy → one graph: the graph must be the compiled graph of the
	// profile that selected the strategy.
	if g.Strategy != p.Strategy {
		violations = append(violations,
			fmt.Sprintf("graph strategy %s != profile strategy %s", g.Strategy, p.Strategy))
	}

	// Deterministic task → no model.
	if !p.ModelRequired && g.ModelNodeCount() != 0 {
		violations = append(violations, "deterministic task compiled a model node")
	}
	if !p.ModelRequired && g.ExpectedInvocations != 0 {
		violations = append(violations, "deterministic task expects model invocations")
	}

	// Ambiguous / unresolved target → no model.
	if p.Strategy == HumanClarification {
		if g.ModelNodeCount() != 0 {
			violations = append(violations, "human-clarification strategy compiled a model node")
		}
		if !g.Has(NodeClarify) {
			violations = append(violations, "human-clarification graph lacks a clarify node")
		}
	}

	// Simple task → no investigation / no repository scan.
	if (p.Strategy == TargetedMutation || p.Strategy == TargetedReasoning || p.Strategy == DirectDeterministic) &&
		g.Has(NodeGatherEvidence) {
		violations = append(violations, "targeted/simple strategy compiled a gather_evidence node (repository scan)")
	}

	// One user mutation → one deduplicated MutationSet target set.
	if seen := dupes(g.MutationTargets); len(seen) > 0 {
		violations = append(violations, fmt.Sprintf("mutation target set contains duplicates: %v", seen))
	}

	// Model invocation → explicit InvocationContract on the node.
	for _, n := range g.Nodes {
		if n.RequiresModel && n.Invocation <= 0 {
			violations = append(violations,
				fmt.Sprintf("model node %s (%s) has no invocation ordinal", n.ID, n.Kind))
		}
		if n.RequiresModel && n.Contract == "" {
			violations = append(violations,
				fmt.Sprintf("model node %s (%s) has no InvocationContract", n.ID, n.Kind))
		}
	}

	// Expected invocations must match the model-node count for exact-count
	// strategies (bounded strategies report their mandatory call).
	if g.ModelNodeCount() != g.ExpectedInvocations {
		violations = append(violations,
			fmt.Sprintf("model node count %d != expected invocations %d", g.ModelNodeCount(), g.ExpectedInvocations))
	}

	// Execution → inspectable proof: the graph must render.
	if g.String() == "" {
		violations = append(violations, "graph renders no inspectable record")
	}

	return violations
}

func dupes(s []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range s {
		if seen[v] {
			out = append(out, v)
			continue
		}
		seen[v] = true
	}
	return out
}
