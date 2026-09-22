// Package changesurface is the ONE canonical semantic home for Phase 2
// Change Surface (IZEN_PHASE_2_PROJECT_UNDERSTANDING_CHANGE_SURFACE).
//
// It answers "given this user intent and the current Project Understanding,
// which existing repository areas are plausibly relevant?" It MUST NOT
// answer "which files are authorized to mutate?" (authorization) or
// "exactly what patch should be produced?" (mutation strategy).
//
// Authority invariant (Phase 1 preserved): a Change Surface NEVER
// authorizes a write and NEVER produces mutation operations. This package
// imports only the standard library plus internal/understanding, owns no
// sink, mints no grant, and declares no Operation/Mutation/Patch/Approve
// symbols. It is informational input to the existing planner/proposal
// layers, which still cross the existing authorization boundary before
// the existing execution authority.
//
// Boundary separation preserved here:
//
//	Target Resolution  — what concrete target did the user refer to?
//	                     (explicit @file; resolved by execution targets)
//	Change Surface     — what repository area is structurally relevant?
//	                     (this package; candidates + evidence + certainty)
//	Authorization Scope — what may Izen actually mutate?
//	                     (Phase 1 grants; never derived here)
//	Mutation Guard      — can this mutation cross the safety boundary?
//	                     (OCC/ScopeGuard; never derived here)
package changesurface
