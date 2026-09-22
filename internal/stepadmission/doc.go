// Package stepadmission implements Adaptive Step Admission (Phase 5).
//
// It is a pure proposal/admission layer that answers: "Is this candidate
// step appropriately bounded for the active model/runtime capability?"
//
// Flow:
//
//	Candidate Step + ModelCapabilityProfile + StepBudget + Scope + State
//	  → Admission Decision (ADMIT | REFINE | BLOCK)
//
// Design constraints (Phase 5):
//   - No second planner, no second scheduler, no execution authority.
//   - No authorization escalation, no capability creation.
//   - Provider-specific knowledge is isolated to the capability-resolution
//     boundary; the rest of Izen consumes ModelCapabilityProfile.
//   - Estimate, Budget, Capability, Admission, Authorization, Scheduling,
//     Execution, Observation, and Verification remain distinct concepts.
//   - Refinement is domain-neutral, evidence-backed, scope-preserving,
//     dependency-aware, and finite. It never invents targets.
//   - Mutation refinement reuses the existing mutationstrategy semantics
//     (MutationPlan / MutationStep / EstimatedMutationSize / StepBudget).
//   - Non-mutation steps (INVESTIGATE / ANALYZE / VERIFY / OBSERVE) are
//     admitted and refined on the same boundary without becoming mutations.
//
// The layer is proposal/admission logic only: it never writes files,
// executes shell commands, runs tests, invokes capabilities, creates
// grants, mutates authorization, or mutates workspace state.
package stepadmission
