// Package understanding is the ONE canonical semantic home for Phase 2
// Project Understanding (IZEN_PHASE_2_PROJECT_UNDERSTANDING_CHANGE_SURFACE).
//
// It answers "what kind of project is this?" from repository evidence only.
// It is a pure, deterministic, read-only derivation layer:
//
//	repository evidence → ProjectUnderstanding → (consumed by planning)
//
// Authority invariant (Phase 1 preserved): understanding NEVER authorizes
// execution. This package imports only the standard library, owns no sink,
// mints no grant, and knows nothing about authorization, mutation strategy,
// patching, budgeting, or execution. Model output enters only as an explicit
// ModelProposal hypothesis and can never silently become repository truth
// (see ConsiderProposal).
//
// Evidence is assembled from existing domain concepts without duplicating
// them: the workspace file-tree scan mirrors internal/workspace/snapshot,
// manifest/config signals mirror internal/engine/inference facts and
// internal/engine/layer1 capability detection, archetype signals mirror
// internal/discovery/recon, and language evidence mirrors
// internal/language. Those systems remain authoritative in their own
// domains; this package only consolidates their signals into one
// inspectable understanding with an explicit lifecycle boundary.
//
// DEMOTION NOTICE (Phase 8 M7 — canonical runtime convergence): this
// package is a derive-helper, NOT a production pipeline stage. The
// canonical decomposition owner is execution/planner, consulted by
// runtime/autonomy.Driver; no canonical runtime package imports this
// Derive chain (pinned by TestDeriveChainHasNoCanonicalImporters). Do
// not wire Derive into the production path without a proven ownership
// gap review.
package understanding
