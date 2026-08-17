# Phase 0 — Findings: New Facts Verified This Session

**Status:** FROZEN — investigation complete, no production code changed.
**Date:** 2026-08-17
**Scope:** Facts verified in this session that are NOT already in `docs/architecture/AUDIT-RUNTIME-AUTHORITY.md` or `docs/design/PHASE/RUNTIME_FORENSIC_AUDIT.md`. Findings here extend/correct those audits; they do not repeat them.

---

## 1. Verification Wiring (confirms and sharpens audit P0#2)

- `execution.NewEngine` (execution.go:79-97) creates `Verifier` (`v`, line 68-75) and stores it on the Engine (`Verifier: v`, line 90) but **never attaches it to its own PatchManager** — there is no `p.SetVerifier(v)` anywhere in production. The only `SetVerifier` callers are tests:
  - `internal/execution/validation_test.go:100`
  - `internal/ui/mutationset_test.go:211` (a `failing` verifier)
  - `internal/execution/execution_phase4_test.go:143`
  - `internal/runtime/handlers/executor_integration_test.go:75,170`
  - `internal/execution/executor_test.go:268,529`
  - `internal/ui/intent_dispatch_test.go:46`, `instant_feedback_test.go:28`, `operation_regression_test.go:199`, `engine_first_test.go:46`, `gated_cancellation_test.go:67`
  - `internal/ui/execution_truth_test.go:45,333` (explicitly `SetVerifier(nil)` to test unverified apply)
- Consequence: `PatchManager.Apply`'s micro-fix gate (`patch.go:850 if pm.verifier != nil`) is unreachable in production — confirming the audit's "nil verifier" finding with source-level proof.

## 2. The legacy apply path is fully event-silent (confirms audit §7)

- `PatchManager.ApplyContext` (patch.go:972) and its apply path publish **nothing** on any bus. The canonical lifecycle events (`EventExecutionStarted` … `EventExecutionFinished`) have exactly one production emitter: the `runtimegraph` (`internal/execution/graph/graph.go:289-504`) reached only via `RuntimeExecutor` (executor.go:271 `x.bus.Publish`).
- The UI's `EventReasoningStream` publish (commands.go:2822) and `NewEngineTelemetry`/activity sinks are the only events on the real path.
- **New precision:** `EventVerificationCompleted` is emitted at graph.go:399 inside `CompleteVerification`, which the RuntimeExecutor calls at executor.go:706. On the legacy path nothing ever calls it.

## 3. Two fully independent mutation boundaries (confirmed, now with lifecycle details)

- `execution.Engine` owns a `mutationSet` (execution.go:44) replaced per terminal outcome; `BeginTransaction` (219) relinks the PatchManager. UI commit/rollback sites verified: commit at model.go:2567, update.go:2143,2289; rollback at keys.go:764, lifecycle.go:245, update.go:1245,1629,1729.
- `RuntimeExecutor` owns its **own** `PatchManager` (executor.go:237) and `MutationSet` (executor.go:640 `pm.ms = ms`), created per execution. The two boundaries never interact — a fact the audit implied but not enumerated.

## 4. Autonomy handoff gap — target confidence is computed and then discarded (confirms audit §7 with exact line)

- `autonomy.Decide` computes `targetConfidence` (engine.go:208-214) and passes it into `DecisionInput` (engine.go:225) **for the controller verdict only**. It is **never stored on `Trace`** — `Trace` carries `Intent` (with `Confidence`), `Risk`, `Route`, `ScopeSize`, `Rollback`, `Grant`. The build path (`runBuildCmd`/`runBuildFastTrack`) receives none of these fields; `dispatchAutonomyTrace` → `executeAutonomyWorkspace` → `stageAutonomyBuild` passes only `trace.Input` and `trace.Intent.Target()`.

## 5. Compose wiring additions not covered by the audit

- `a.Approver = nil` (compose.go:369): the `handlers.PatchApprover` seam is wired to nil, so `ApprovePatchHandler` (handlers.go:253) degrades to `ErrNoExecutor`/injected-approver in any non-executor context.
- IntentRouter constructed (compose.go:590) only when `provider != nil`, then **never read** — confirmed `a.IntentRouter` has no consumers.
- Plan engine gets provider directly (compose.go:528-529 `SetProvider`/`SetStreamProvider`) — a fourth place a provider can be invoked, outside the executor.
- Autonomy is wired with `WithRiskFunc` → `Execution.Risk.ClassifyFileOp` and `WithRollbackFunc` → `Execution.Checkpoints` (compose.go ~631-635), i.e. **both the autonomy runtime and the legacy engine read the same Risk/Checkpoint singletons** — the only shared state between the two execution stacks.

## 6. ScopeGuard is a real pre-build validation (new, not in audit)

- `planResultMsg` handler (update.go:657-674) runs `control.ValidateStagedPlan(scopeTasks, m.planEngine.AllowedFiles)` when the plan engine has a grounded allowed-file tree, rejecting out-of-tree targets with a `ScopeViolationError` and a dimmed activity line. This is a live deterministic guard on the real path (only when `AllowedFiles` non-empty).

## 7. Fast-track continuous-execution path (new detail)

- `planResultMsg` with `IsFastTrack` (update.go:699-706) auto-creates a checkpoint (update.go:704), sets `planApproved = true`, and continues to build in the SAME turn (update.go:779-791): `setMode(ModeBuild)` if not already in build, else `runBuildCmd("")` directly. Single-dispatch guard: never pairs `setMode` with an explicit `runBuildCmd` when the handoff auto-trigger (`buildHandoffTriggerContent`) can produce a payload.

## 8. Hotfix local short-circuit (new, 0-token path)

- `proposeHotfixPatch` (commands.go:3860-3878): for `@`-referenced targets with simple text modifications, `execution.ApplyContextAwareFuzzyReplace` produces the patch with **no provider invocation**. Provider fallback at 3998/4062 with a non-streaming retry for truncated SSE responses (responseEffectivelyEmpty guard). This is the only mutation path that can reach the proposal without an LLM call.

## 9. Provider site in the artifact gate (new precision)

- The V3 artifact gate (`m.execEng.Artifact.ValidateContent`) is invoked on the **proposeBuildPatch** path (commands.go:4900-4907, full-rewrite fallback) and the retry loop (4824, 4904) — but **not** on the fast-track streaming path (`runBuildFastTrack` → `ExecuteStream`, commands.go:2787). Confirms audit's "no artifact contract validation on fast-track" with exact sites.

## 10. UI event subscription is fully ready (new)

- program.go:312-348 subscribes to **all** canonical lifecycle events (EventExecutionStarted/StrategySelected/TargetResolved/ContextPrepared/ModelInvoked/ProviderWaiting/FirstToken/StreamDelta/UsageUpdate/ReasoningTelemetry/ProviderResponse/ArtifactProduced/MutationStarted/MutationCompleted/VerificationCompleted/ExecutionFinished/ApprovalRequired) plus autonomy events (AutonomyDecision/CapabilityGranted/LoopTransition/ContextCompiled). The projection is wired and waiting — only the emitter is missing on the real path.

## 11. Corrections / refinements to the audit

- Audit §4 lists `handlers.ClassifyIntent` at `handlers.go:402`; the live call is at `handlers.go:153` (SubmitPromptHandler.Handle). The 402 reference is the definition in the same file.
- Audit's executor line refs (executor.go:373 Execute, 599 Approve, 640 mutation set) verified identical in this session.
- Audit says "three bus implementations" — verified with distinct vocabularies (42/7/8 event types) and the single one-way telemetry→domain bridge (compose.go:509).

## 12. Remaining investigation items still open (not blocker)

- `internal/execution/verify.go` RunAll step set (default Go steps) vs `NewLanguageVerifier` step set — not diffed.
- `internal/execution/mutationset.go` evidence vocabulary vs `MutationEvidence` used by the UI (proposals.go `applyEvidence`) — the UI builds its own `MutationEvidence`; the executor's `MutationSet.Outcomes` vocabulary is separate. Not cross-checked.
- `internal/runtime/runtime.go` `Runtime.Execute` facade and its consumers outside `presentation.Bridge` (bridge.go:78) and tests.

---

## Bottom line

The baseline (PHASE_0_ARCHITECTURE_BASELINE.md) and cutover plan (PHASE_1_CUTOVER_PLAN.md) are grounded in the facts above. The single most important new precision: **verification is absent not because the verifier is missing, but because a one-line wiring (`p.SetVerifier(v)`) inside `execution.NewEngine` was never made** — the cheapest possible first cutover step (Phase 1 Step 1).