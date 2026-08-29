package autonomy

// MutationStrategy is the execution contract strategy that governs
// preflight estimation and AST gating. It is mutated by
// Driver.ResumeWithProposal to create a NEW concrete contract before
// the next loop iteration, preventing the infinite-loop where the
// evaluator would re-compute the same 3× estimate and re-park at the
// DecisionSurface.
type MutationStrategy int

const (
	StrategyFullRewrite MutationStrategy = iota
	StrategyBoundedPatch
	StrategySyntaxRepair
	StrategyInspectOnly
)

// String returns the canonical label for the strategy.
func (s MutationStrategy) String() string {
	switch s {
	case StrategyFullRewrite:
		return "full_rewrite"
	case StrategyBoundedPatch:
		return "bounded_patch"
	case StrategySyntaxRepair:
		return "syntax_repair"
	case StrategyInspectOnly:
		return "inspect_only"
	default:
		return "unknown"
	}
}
