package strategy

// ── CANONICAL STRATEGY → MUTATION PROJECTION ────────────────────────────────
//
// A strategy answers "what KIND of execution is this". Whether that execution is
// allowed to WRITE the workspace is a different question, and until this file
// existed the runtime answered it in two places:
//
//	syncCanonicalIntent:  {TargetedMutation, DirectDeterministic, MultiFilePlanning}
//	                      → needs a mutation contract
//	intentClassForStrategy: MultiFilePlanning → PLAN (read-only), and the other
//	                      two → MUTATE
//
// Two lists, one strategy, two answers: for MultiFilePlanning the canonical
// intent said MUTATION while admission said PLAN. A split brain between a
// lifecycle's compiled context policy and the gate that guards its dispatch is
// exactly the defect the intent authority exists to prevent, one layer earlier.
//
// So the answer lives here, once, next to the taxonomy it describes. Both call
// sites project from it; neither keeps its own list.
//
// ── WHY PROPOSAL IS NOT READ-ONLY ───────────────────────────────────────────
//
// MultiFilePlanning is the only strategy that legitimately expands into
// investigate/plan/build, and its dispatched turn is a proposal. It is NOT a
// read-only objective, and the architecture already says so in two places:
//
//   - bindGatewayContract gives it StructuredCompletion, the proposal contract;
//   - operationForStrategy treats it as an attempted FILE_MUTATE so the
//     direct-completion ceiling rejects it rather than executing it.
//
// One value carries both facts. PROPOSAL dispatches no mutation on its own
// (IsApplied is false) while still requiring a mutation contract for the
// lifecycle it may grow into (RequiresMutationContract is true). Collapsing the
// two — calling it read-only, or calling it applied — is what produced the
// contradiction.

// MutationSemantics is the canonical answer to "what may this strategy do to the
// workspace". It is a semantic projection of ExecutionStrategy, never a second
// source of strategy truth.
type MutationSemantics string

const (
	// MutationSemanticsReadOnly: the execution writes nothing and asks for no
	// mutation authority.
	MutationSemanticsReadOnly MutationSemantics = "read_only"
	// MutationSemanticsProposal: the execution may propose a workspace change
	// and requires the mutation contract for the lifecycle, but the dispatched
	// turn does not itself apply one.
	MutationSemanticsProposal MutationSemantics = "proposal"
	// MutationSemanticsApplied: the execution writes the workspace under its own
	// authority.
	MutationSemanticsApplied MutationSemantics = "applied"
)

// String returns the canonical mutation-semantics label.
func (m MutationSemantics) String() string { return string(m) }

// IsApplied reports whether the dispatched turn itself writes the workspace.
func (m MutationSemantics) IsApplied() bool { return m == MutationSemanticsApplied }

// RequiresMutationContract reports whether a lifecycle running this strategy must
// compile its context under the modification contract. Every non-read-only
// strategy does; a read-only one has nothing to elevate.
func (m MutationSemantics) RequiresMutationContract() bool { return m != MutationSemanticsReadOnly }

// MutationSemanticsOf projects an execution strategy onto its canonical mutation
// semantics. It is total: an unknown strategy is READ-ONLY, so adding a strategy
// to the taxonomy without deciding its mutation semantics can never widen
// authority by omission.
func MutationSemanticsOf(s ExecutionStrategy) MutationSemantics {
	switch s {
	case TargetedMutation, DirectDeterministic:
		return MutationSemanticsApplied
	case MultiFilePlanning:
		return MutationSemanticsProposal
	case TargetedReasoning, RepositoryInvestigation, DirectResponse, HumanClarification:
		return MutationSemanticsReadOnly
	default:
		return MutationSemanticsReadOnly
	}
}
