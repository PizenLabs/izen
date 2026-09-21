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
package problem
