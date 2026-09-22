// Package problem provides the domain-neutral ProblemSolvingPlan contract.
//
// It represents task-level bounded reasoning structure — what bounded work
// should conceptually happen — rather than execution. It is pure,
// deterministic, proposal-level, and read-only. It never authorizes,
// executes, retries, continues, or switches models.
//
// The plan vocabulary is intentionally minimal: INVESTIGATE, ANALYZE,
// EXPERIMENT, MUTATE, VERIFY, OBSERVE. Only kinds with a concrete
// semantic need, clear producer, and clear consumer are implemented.
//
// Boundary:
//
//	ProblemSolvingPlan = what bounded work should conceptually happen
//	TaskSpec / StepScheduler = how authorized execution steps are scheduled durably
//
// Therefore ProblemSolvingPlan → proposal translation → existing TaskSpec
// → existing StepScheduler (translation not implemented in this phase).
//
// DEMOTION NOTICE (Phase 8 M7 — canonical runtime convergence): this
// package is a derive-helper, NOT a production pipeline stage. The
// canonical decomposition owner is execution/planner and the canonical
// orchestration owner is runtime/autonomy.Driver (the StepScheduler
// referenced above is demoted to internal/architecture_experiment/
// scheduler and has zero production importers). No canonical runtime
// package imports this Derive chain (pinned by
// TestDeriveChainHasNoCanonicalImporters). Do not wire Derive into the
// production path without a proven ownership gap review.
package problem
