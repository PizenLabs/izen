// Package problemsurface derives the evidence-backed problem surface
// (relevant areas for understanding/investigation/problem solving) from a
// ProjectUnderstanding. It is pure, deterministic, and read-only: it never
// authorizes, executes, schedules, or mutates.
//
// DEMOTION NOTICE (Phase 8 M7 — canonical runtime convergence): this
// package is a derive-helper, NOT a production pipeline stage. The
// canonical decomposition owner is execution/planner, consulted by
// runtime/autonomy.Driver; no canonical runtime package imports this
// Derive chain (pinned by TestDeriveChainHasNoCanonicalImporters). Do
// not wire Derive into the production path without a proven ownership
// gap review.
package problemsurface
