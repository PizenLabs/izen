// Package continuation implements the bounded adaptive continuity layer (Phase 4).
//
// It is a pure proposal/derivation mechanism sitting above the existing
// StepScheduler and RuntimeExecutor. It does not execute, authorize, schedule,
// create grants, expand scope, or mutate the workspace. Every mutation still
// re-enters the existing authorization boundary and is admitted by the existing
// StepScheduler before reaching the single execution authority.
//
// Design principles (Phase 4):
//   - LLM output is epistemically non-authoritative; only execution evidence,
//     observation events, workspace state, state fingerprints, recovery context,
//     and verified artifacts drive decisions.
//   - Provider output ceiling bounds the current step, not the whole task.
//   - Durable state comes from task/step state, digests, OCC, recovery context,
//     and evidence — never from transcript replay.
//   - Stale/OCC-drift and no-progress states terminate safely via deterministic
//     terminal actions (STALE, NO_PROGRESS) without hidden loop counters.
package continuation
