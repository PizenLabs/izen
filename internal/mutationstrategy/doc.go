// Package mutationstrategy is the ONE canonical semantic home for Phase 3
// Mutation Strategy & Step Construction
// (IZEN_PHASE_3_MUTATION_STRATEGY_STEP_CONSTRUCTION).
//
// It answers "given this intent, the current Project Understanding, and the
// derived Change Surface, what kind of mutation should be proposed, how should
// the task be decomposed, and how large is each bounded step?"
//
// Authority invariant (Phase 1 + Phase 2 preserved): a MutationPlan NEVER
// authorizes a write, NEVER produces an Authorization Grant, and NEVER
// executes a mutation. It is a pure derivational proposal that is still
// subject to the existing human authorization and execution boundaries:
//
//	Intent
//	  → ProjectUnderstanding (repository evidence)
//	    → ChangeSurface (plausibly relevant areas, evidence-backed)
//	      → MutationStrategy / MutationPlan (bounded proposal, this package)
//	        → Existing Authorization (human grant)
//	          → Existing Execution (runtime executor)
//
// Boundary separation preserved here:
//
//	MutationStrategy ≠ MutationPlan ≠ StepBudget ≠ Authorization ≠ Execution
//
// This package imports only the standard library plus
// internal/understanding and internal/changesurface (its input boundary).
// It owns no sink, mints no grant, declares no Approve/Apply/Mutate/Commit
// authority symbols, never touches the filesystem beyond the read-only
// understanding digest it was given, never invokes a model, and never
// schedules autonomous continuation or provider retry. It is informational
// input to the existing planner/proposal layers, which still cross the
// existing authorization boundary before the existing execution authority.
//
// Design preference: pure derivation — Derive(intent, understanding,
// changeSurface, options) deterministically returns a MutationPlan. No
// filesystem mutation, no shell execution, no model invocation.
package mutationstrategy
