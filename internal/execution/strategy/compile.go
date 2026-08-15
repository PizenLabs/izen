package strategy

// ── PHASE 11 — STRATEGY → GRAPH COMPILATION ────────────────────────────────
//
// Compile turns an ExecutionStrategyProfile — the deterministic decision the
// engine made BEFORE any model invocation — into the explicit ExecutionGraph
// Izen intends to execute. The model never invents graph topology: every
// strategy maps onto one fixed, bounded topology. The compiled graph is the
// minimum-sufficient execution for the strategy; it grows only through
// recorded escalation, never through a generic re-planner.

// targetOf returns the resolved path of the first target, or "".
func targetOf(p ExecutionStrategyProfile) string {
	if len(p.Targets) > 0 {
		return p.Targets[0].Resolved
	}
	return ""
}

// resolvedTargetPaths returns the deduplicated resolved target paths in
// resolution order.
func resolvedTargetPaths(p ExecutionStrategyProfile) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range p.Targets {
		if t.Resolved == "" || seen[t.Resolved] {
			continue
		}
		seen[t.Resolved] = true
		out = append(out, t.Resolved)
	}
	return out
}

// mutationTargets returns the target set the owning MutationSet records: the
// resolved targets, or a single unnamed mutation phase when the strategy has
// no resolved targets yet (multi-file planning).
func mutationTargets(p ExecutionStrategyProfile) []string {
	paths := resolvedTargetPaths(p)
	if len(paths) > 0 {
		return paths
	}
	if p.Strategy == MultiFilePlanning || p.Strategy == RepositoryInvestigation {
		return nil
	}
	if len(p.Targets) > 0 {
		return []string{p.Targets[0].Resolved}
	}
	return nil
}

// expectedInvocations is the model-invocation count the strategy justifies.
// Deterministic and clarification strategies justify zero; every reasoning
// strategy justifies exactly its mandatory reasoning call. Multi-file planning
// marks its planning call as mandatory and leaves the bounded per-target
// calls to the runtime (never hidden inside a transition).
func expectedInvocations(p ExecutionStrategyProfile) int {
	if !p.ModelRequired {
		return 0
	}
	switch p.Strategy {
	case TargetedMutation, TargetedReasoning, RepositoryInvestigation, MultiFilePlanning:
		return 1
	default:
		return 0
	}
}

// contractFor returns the compact InvocationContract string of the model node,
// or "" for deterministic nodes.
func contractFor(p ExecutionStrategyProfile, invocation int) string {
	if !p.ModelRequired || invocation <= 0 {
		return ""
	}
	return For(p, invocation).String()
}

// Compile builds the explicit ExecutionGraph for a strategy profile. It is
// deterministic: the same profile always yields the same node sequence. The
// returned graph starts in the pending state; the runtime drives it through
// the node lifecycle.
func Compile(p ExecutionStrategyProfile) *ExecutionGraph {
	g := NewExecutionGraph("", p.Strategy)
	g.ExpectedInvocations = expectedInvocations(p)
	g.ContextKinds = append([]ContextKind(nil), p.ContextKinds...)
	g.MutationTargets = mutationTargets(p)

	switch p.Strategy {
	case TargetedMutation:
		compileTargetedMutation(g, p)
	case DirectDeterministic:
		compileDeterministic(g, p)
	case HumanClarification:
		compileClarification(g, p)
	case TargetedReasoning:
		compileReasoning(g, p)
	case RepositoryInvestigation:
		compileInvestigation(g, p)
	case MultiFilePlanning:
		compilePlanning(g, p)
	default:
		// Unknown strategies compile to the most conservative minimal graph:
		// resolve, then stop at a human clarification boundary. No model node
		// is ever added for an unknown strategy.
		g.Add(NodeResolveTarget, targetOf(p), false, 0, "")
		g.Add(NodeClarify, targetOf(p), false, 0, "")
	}

	// Seed the escalation history recorded by the selector decision and the
	// context compiler. The graph carries the escalation evidence so $inspect
	// can answer "why did this execution grow" without reconstructing it.
	for _, e := range decisionEscalations(p) {
		g.RecordEscalation(e)
	}
	return g
}

// decisionEscalations derives the escalation records that require no
// workspace evidence (strategy selection outcomes).
func decisionEscalations(p ExecutionStrategyProfile) []EscalationRecord {
	if p.Escalation {
		return EscalationsFor(p, ContextEnvelope{})
	}
	switch p.Strategy {
	case RepositoryInvestigation, MultiFilePlanning:
		return EscalationsFor(p, ContextEnvelope{})
	default:
		return nil
	}
}

// compileTargetedMutation compiles the bounded single/multi-file mutation
// graph. Exactly one model invocation (reason) precedes the deterministic
// proposal, the human approval boundary, one mutate node per target and the
// verification gate.
func compileTargetedMutation(g *ExecutionGraph, p ExecutionStrategyProfile) {
	g.Add(NodeResolveTarget, targetOf(p), false, 0, "")
	g.Add(NodeReadTarget, targetOf(p), false, 0, "")
	g.Add(NodeReason, targetOf(p), true, 1, contractFor(p, 1))
	g.Add(NodePropose, targetOf(p), false, 0, "")
	g.Add(NodeApprove, targetOf(p), false, 0, "")
	targets := resolvedTargetPaths(p)
	if len(targets) == 0 {
		g.Add(NodeMutate, "", false, 0, "")
	} else {
		for _, t := range targets {
			g.Add(NodeMutate, t, false, 0, "")
		}
	}
	g.Add(NodeVerify, targetOf(p), false, 0, "")
}

// compileDeterministic compiles the zero-model deterministic graph: resolve,
// read, mutate, verify. No reason/propose/approve nodes exist because no
// reasoning and no model artifact are required.
func compileDeterministic(g *ExecutionGraph, p ExecutionStrategyProfile) {
	target := targetOf(p)
	g.Add(NodeResolveTarget, target, false, 0, "")
	g.Add(NodeReadTarget, target, false, 0, "")
	g.Add(NodeMutate, target, false, 0, "")
	g.Add(NodeVerify, target, false, 0, "")
}

// compileClarification compiles the stop-before-execution graph: resolve the
// target deterministically, then stop at the human clarification boundary.
func compileClarification(g *ExecutionGraph, p ExecutionStrategyProfile) {
	g.Add(NodeResolveTarget, targetOf(p), false, 0, "")
	g.Add(NodeClarify, targetOf(p), false, 0, "")
}

// compileReasoning compiles the read-only understanding graph: resolve, read,
// reason. It never reaches approve/mutate/verify.
func compileReasoning(g *ExecutionGraph, p ExecutionStrategyProfile) {
	g.Add(NodeResolveTarget, targetOf(p), false, 0, "")
	g.Add(NodeReadTarget, targetOf(p), false, 0, "")
	g.Add(NodeReason, targetOf(p), true, 1, contractFor(p, 1))
}

// compileInvestigation compiles the repository-investigation graph: resolve,
// gather evidence, reason, then either propose (evidence sufficient) or
// clarify (evidence insufficient). Both are sanctioned terminal paths of the
// same node — the runtime completes exactly one.
func compileInvestigation(g *ExecutionGraph, p ExecutionStrategyProfile) {
	g.Add(NodeResolveTarget, targetOf(p), false, 0, "")
	g.Add(NodeGatherEvidence, targetOf(p), false, 0, "")
	g.Add(NodeReason, targetOf(p), true, 1, contractFor(p, 1))
	g.Add(NodePropose, targetOf(p), false, 0, "")
	g.Add(NodeClarify, targetOf(p), false, 0, "")
}

// compilePlanning compiles the multi-file planning graph: resolve, gather
// evidence, reason (the plan synthesis call), propose, human approval, one
// mutate node per plan target (or one mutation phase when targets are not yet
// resolved), and the verification gate.
func compilePlanning(g *ExecutionGraph, p ExecutionStrategyProfile) {
	g.Add(NodeResolveTarget, targetOf(p), false, 0, "")
	g.Add(NodeGatherEvidence, targetOf(p), false, 0, "")
	g.Add(NodeReason, targetOf(p), true, 1, contractFor(p, 1))
	g.Add(NodePropose, targetOf(p), false, 0, "")
	g.Add(NodeApprove, targetOf(p), false, 0, "")
	targets := resolvedTargetPaths(p)
	if len(targets) == 0 {
		g.Add(NodeMutate, "", false, 0, "")
	} else {
		for _, t := range targets {
			g.Add(NodeMutate, t, false, 0, "")
		}
	}
	g.Add(NodeVerify, targetOf(p), false, 0, "")
}
