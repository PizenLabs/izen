# IZEN — Phase 4 Audit: Bounded Adaptive Continuity

**Phase:** 4 — Bounded Adaptive Continuity  
**Baseline:** Generality Boundary Correction (complete) — `docs/audits/IZEN_GENERALITY_BOUNDARY_CORRECTION.md` + `docs/architecture/IZEN_PROBLEM_SOLVING_MODEL.md`  
**New runtime:** NONE (single substrate, single scheduler, single authorization)  
**New scheduler:** NONE  
**New execution authority:** NONE

---

## 1. What Changed

| Area | Change | Files |
|---|---|---|
| **Continuation contract** | Introduced minimal domain-neutral proposal layer `internal/continuation` with `ContinuationDecision` (`COMPLETE/CONTINUE/BLOCKED/FAILED/STALE/AWAITING_APPROVAL/NO_PROGRESS`), `StepProposal`, `Observation` (`MODEL_PROPOSAL/EXECUTION_RESULT/OBSERVATION/VERIFICATION/STATE_TRANSITION`), `BoundedStepState` (`PROPOSED→ADMITTED→EXECUTING→OBSERVED→VERIFIED/PARTIAL/BLOCKED/FAILED→CONTINUE/TERMINATE`), `DerivationInput`/`TaskStateView`/`StepHistoryEntry`. Pure, deterministic, side-effect-free `DeriveNextStep` — never executes, authorizes, schedules, expands scope, or switches authority. | `internal/continuation/doc.go`, `types.go`, `derive.go` |
| **Scheduler bridge** | Thin adapter `ContinuationToTaskSpec` + `DeriveContinuation` that materializes the next `TaskSpec` from a `ContinuationDecision` for the *existing* `StepScheduler`. Preserves durable `ActiveTargetScope` as envelope, carries authoritative evidence into `LatestEvidence`, reuses `EffectiveBudget`/`Schedule`/`AcceptStep`. Observable integration path for §20. | `internal/runtime/scheduler/continuation_bridge.go` |
| **Integration trace** | Real execution path test: `Task → ProblemSolvingPlan → scheduler.Schedule → RunNext (staging, length) → observation → DeriveNextStep → ContinuationToTaskSpec → scheduler.Schedule` (minimal, proof-bearing). | `internal/runtime/scheduler/continuation_integration_test.go` |
| **Partial-output isolation** | Reuses existing `executor.ProposalStagingBuffer` (`Partial` + `OUTPUT_CEILING`, `NeedsContinuation`, `RecoveryContext.LastClean*`, `PostStepEvaluation`/`ConsecutiveZeroDeltas` with `zeroDeltaHaltThreshold=2`). Continuation treats `partial` as `CONTINUE` from durable state, never as task failure or blind prompt resend. | (existing) consumed by continuation via `isPartialOutput` |
| **Stale/OCC + no-progress** | Stale checks (`HasStaleState` or `RecoveryReason` fingerprint/digest/occ drift → `STALE`) and bounded-history no-progress (`same fingerprint+outcome+evidence + 0 patches ×3` or same `StepID`×3 → `NO_PROGRESS` / `ErrRecoveryHalted`). Uses existing `StateFingerprint`, `TargetSnapshot` digest, `RecoveryContext`, `History`. | `internal/continuation/derive.go` |
| **Provider per-step bounds** | `boundedEstimate` caps `EstimatedTokens` at `ProviderCeiling` (1024-token ceiling bounds step, not task). Task `RemainingBudget` stays independent (CONT-12). | `internal/continuation/derive.go` |
| **Architecture tests** | `TestContinuationMustNotImportForbidden`, `TestContinuationMustNotContainExecutionPrimitives`, `TestCoreMustNotImportWebAdapterContinuation`, `TestNoSecondSchedulerOrExecutor`, `TestContinuationDomainNeutralArchitecture`, `TestContinuationHasNoAuthorizationFields`. Also `TestGEN*` still green; static-web leakage re-pinned. | `internal/architecture/continuation_invariants_test.go` |
| **Continuation unit tests** | CONT-01…18 covering §18 required tests (see §7). | `internal/continuation/continuation_test.go` |
| **Docs** | `docs/architecture/IZEN_BOUNDED_ADAPTIVE_CONTINUITY.md` (canonical §1–§23 spec) + this audit. | `docs/architecture/IZEN_BOUNDED_ADAPTIVE_CONTINUITY.md`, `docs/audits/IZEN_PHASE4_BOUNDED_ADAPTIVE_CONTINUITY.md` |

---

## 2. What Did Not Change

- **Runtime substrate & composition root**: `internal/runtime/substrate` remains sole `os/exec`/`os.WriteFile` owner; `internal/runtime/compose` remains sole `NewRuntimeExecutor` binder (`TestPhase4PipelineLock`, `TestRuntimeExecutorSingleCompositionBinding` green).
- **Execution authority**: `execution.RuntimeExecutor` remains the single production mutation authority (`Phase 0/1/4/5/6` lock suites green). `runtime/executor.RuntimeExecutor` stays non-production coordinator; no new executor in continuation.
- **Scheduler**: `runtime/scheduler.StepScheduler` (`Schedule`, `EffectiveBudget`, `AcceptStep`, `StepOutcome`, `RecoveryContext`, `Strategy`/`SKELETON_CREATE`/`BOUNDED_*`) unchanged except for the thin bridge file. No `AdaptiveScheduler` / `ContinuationScheduler` / `AgentScheduler` / `ProblemScheduler` introduced.
- **Authorization**: `core/authorization` / `runtime/executor` 6/8-clause guard unchanged; planning abstractions still carry no `Grant`/`Authorize`/`Approve` fields (`TestGEN09` green).
- **Understanding / ProblemSurface / ChangeSurface / MutationPlan / ProblemSolvingPlan**: contracts unchanged and still domain-neutral; no new kinds added to `ProblemSolvingPlan` (still 6 kinds), no new `OperationKind`/`StrategyKind`. Web-generic boundary re-verified (`TestArch_CoreDoesNotDependOnWebAdapter`-style `TestCoreMustNotImportWebAdapterContinuation` green; `GEN-01`…`GEN-12` green).
- **OCC, evidence, verification, checkpoint, ledger**: `runtime/durable` (`State`, `RecoveryContext`, `ledger.ndjson`), `core/domain/evidence` (`EvidenceVector`, `Verdict*`), `verification`, `checkpoint` unchanged; continuation only reads their digests/evidence.
- **Provider/model selection**: `internal/ai` / `internal/providers` / `internal/model_picker` untouched (no switching/ranking).
- **TUI**: no workspace-mutating changes to `internal/ui` beyond the existing event projection (`TestUINeverFabricatesLoopTransitions` green).
- **Existing tests/fixtures**: `testdata/staticweb` and all Phase 0–3 lock suites untouched; `go test ./...` still `ok`.

---

## 3. Authority Boundaries (Pinned)

| Boundary | Owner | Continuation's Role | Enforcement |
|---|---|---|---|
| **Mutation authority** | `execution.RuntimeExecutor` → `runtime/substrate.Substrate` (single path) | **None** — continuation never calls `os.WriteFile`/`os/exec`/`substrate.ExecuteUnit` and never holds a `Substrate` or `executor` field. | `TestContinuationMustNotImportForbidden`, `TestContinuationMustNotContainExecutionPrimitives`, `TestPhase4PipelineLock`, `TestPhase6_ShadowPathEradication` |
| **Authorization** | `core/authorization.CapabilityGuard.Evaluate` (8-clause) + `runtime/executor` fast-path and `OCCGate` | **Proposal only** — `ContinuationDecision`/`StepProposal` carry no `Grant`/`Approve`/`Capability`; `AWAITING_APPROVAL` is the only outcome when envelope would be exceeded. | `TestContinuationHasNoAuthorizationFields`, `TestGEN09`, `TestContinuationMustNotImportForbidden` |
| **Scheduling** | `runtime/scheduler.StepScheduler` (`Schedule`, `AcceptStep`, `EffectiveBudget`) | **Thin bridge only** — `ContinuationToTaskSpec` reuses `Schedule`; `DeriveNextStep` contains no `Schedule`/`AcceptStep`/`StepScheduler` reference. | `TestNoSecondSchedulerOrExecutor`, `TestGEN10`, `TestContinuationMustNotImportForbidden` |
| **Execution** | `runtime/substrate` (FILE_WRITE + SHELL), `runtime/executor` (orchestration) | **Never executes** — `DeriveNextStep` is pure; file writes only in `integrationSink` test helper, never in continuation source. | `TestContinuationMustNotContainExecutionPrimitives` |

```
LLM (bounded worker) → Continuation (derives proposal) → Authorization (permits) → Scheduler (admits) → Execution (acts) → Observation (records) → State (truth)
```

Never collapsed; each arrow has a single owner.

---

## 4. Scheduler Ownership

- **Owner:** `internal/runtime/scheduler` (`StepScheduler` struct, `Schedule(TaskSpec) []ExecutionStep`, `EffectiveBudget`, `AcceptStep`, `RunNext` staging, `Strategy` resolution, `StateFingerprint`/`RecoveryContext`).
- **Continuation's relationship:** read-only consumer. The bridge `ContinuationToTaskSpec(base TaskSpec, decision ContinuationDecision) (TaskSpec, bool)` preserves the durable envelope (`ActiveTargetScope`/`AllowedScope`), carries authoritative observations into `LatestEvidence`, and forwards `EstimatedTokens` into the next `TaskSpec` for the *same* `Schedule` call. No second scheduler type or method exists; `Schedule` remains the decomposition authority and `AcceptStep` remains the invariant-2 guard (executor must not alter step scope).
- **Evidence:** `internal/runtime/scheduler/scheduler.go` is unchanged except `continuation_bridge.go` addition; `go vet`/`golangci-lint` clean; `TestNoSecondSchedulerOrExecutor` forbids `type AdaptiveScheduler` etc.; `TestScheduler_AcceptStepFreezesEveryField` green.

---

## 5. Execution Ownership

- **Owner:** `internal/execution.RuntimeExecutor` (single production authority bound once in `internal/runtime/compose`) + `internal/runtime/substrate.Substrate` (sole `os/exec` + `os.WriteFile` holder). `internal/runtime/executor.RuntimeExecutor` remains the unreachable coordinator (Case C), `internal/runtime/scopeguard` the cursor idempotency helper (Case B) — pinned by `TestPhase1_SingleProductionExecutionAuthority`.
- **Continuation's relationship:** none. `internal/continuation` imports no execution package; source `derive.go` contains no `ExecuteUnit`, `WriteFile`, `exec.Command`, `PatchManager`, or `MutationSet` reference. The integration test drives execution via `scheduler.RunNext` (which itself drives `executor.ProposalStagingBuffer` + `CommitGate`), not via continuation.
- **Evidence:** `TestContinuationMustNotImportForbidden` + `TestContinuationMustNotContainExecutionPrimitives` green; `TestAutonomousLoopNeverImportsExecutionAuthority` + `TestRuntimeAutonomyPackageHasSingleBoundary` green.

---

## 6. Continuation Ownership

- **Owner:** `internal/continuation` — **pure proposal layer**.
- **Responsibility:** derive at most one bounded `StepProposal` from `Observed Truth + TaskState + PreviousOutcome + Plan + Observations + Evidence + Scope + Budgets` (spec §3, §6). Owns `ContinuationAction`/`ObservationKind`/`BoundedStepState`/`DerivationInput`/`TaskStateView`/`Transition`. Owns freshness (`STALE` on digest/OCC drift) and no-progress (`NO_PROGRESS` on 3× repeated fingerprint/outcome/evidence with zero patches) terminal semantics. Owns the `MODEL_PROPOSAL` vs `VERIFICATION`/`OBSERVATION`/`EXECUTION_RESULT` epistemic distinction so a model claim never becomes a state transition.
- **Non-responsibility:** MUST NOT execute, write, authorize, schedule, create grants, expand scope, invent targets, switch provider, or choose models. All mutation still re-enters authorization + `StepScheduler` + `execution`.
- **Domain neutrality:** reasons over `problem state / evidence / outcome / scope / dependencies / verification / freshness / progress` — no `ReactContinuation` etc. (`TestContinuationDomainNeutralArchitecture` green; `CONT-18` green).
- **Dependency posture:** bottom-most after observation per `Adapters → … → Observation → Continuation` — core never imports adapter; continuation never imports execution/authorization/scheduler/TUI/provider (enforced).

---

## 7. State / Evidence Ownership

- **State:** `internal/runtime/durable` owns `TaskState`/`RecoveryContext`/`LedgerEvent`/`SnapshotFile`/`FileLock` + `Reconcile`/`OCC` (`runtime/durable` + `core/domain/occ`). `StateFingerprint`/`workspace digest`/`TargetSnapshot` are the truth; continuation reads them, never invents them.
- **Evidence:** `internal/core/domain` + `internal/core/domain/evidence` own `EvidenceVector`/`EvidenceState` (`VerdictPassed/Failed/Inconclusive` + `PARTIAL`) and `EvidenceLevel` (L0→L5). `events.DomainEvent` owns the observation ledger (`EventTaskCreated`…`EventVerificationResult`). Verification (`verification`) owns `L1`→`L5` harness.
- **Continuation's evidence inputs:** `[]Observation` (kind `VERIFICATION`/`OBSERVATION`/`EXECUTION_RESULT`/`STATE_TRANSITION` are authoritative; `MODEL_PROPOSAL` is filtered out by `filterAuthoritative`). `DeriveNextStep` prioritizes verification over model claims (CONT-14) and uses `Verified` + `filterAuthoritative` as the only transition drivers. Transcript loss therefore does not destroy durable state (CONT-13) — `StateDigest` is `sha256(taskID|fingerprint|outcome|reason|verified)[:16]`, not a transcript hash, and all evidence is carried as `EvidenceDigest`/`StateFingerprint`.

---

## 8. Partial-Output Behavior (§7, §9)

- **Detection:** `executor.ProposalStagingBuffer.Finalize(finishReason="length")` or `IsPartialOutput` / `PreviousOutcome==partial` from caller.
- **Isolation invariant (reused):** staged bytes discarded, never cross `CommitGate` without `VerdictPassed`; `RecoveryContext.LastCleanByteOffset/Line/TokenOffset` and `ObservedTokens` preserved for re-admission.
- **Continuation:** `partial`/`OUTPUT_CEILING` never yields `FAILED` for the task. It yields `CONTINUE` with a bounded `NextStep` derived from durable `PlanSteps`/evidence, capped at the provider's per-step ceiling (`boundedEstimate ≤ ProviderCeiling`). The step is a different bounded proposal, not a resend of the same prompt (CONT-02, CONT-03, CONT-12). The integration test proves `length → DeriveNextStep → new TaskSpec → Schedule` without task failure.
- **Zero-delta limit (reused):** `PostStepEvaluation` with `zeroDeltaHaltThreshold=2` (max 1 bounded recovery turn, second zero-delta `OUTPUT_CEILING` with 0 patches → `ErrRecoveryHalted`, `NeedsContinuation=false`). Logged as `ConsecutiveZeroDeltas` in `RecoveryContext` (CONT-10).

---

## 9. Freshness Behavior (§11)

- **Check order:** staleness first, before any derivation.
- **Signals:** caller-supplied `HasStaleState` (workspace/file change detected via `understanding.IsStale`/`DigestMatches`/`TargetSnapshot`), or `Task.RecoveryReason` containing `fingerprint/digest/stale/occ/drift`.
- **Outcome:** `STALE` with reason `state fingerprint drift: recovery context requires re-admission` or `OCC drift: …`, no `NextStep`. Caller must take the existing recovery/re-admission path; continuation never overrides an `OCCGate.ValidateAndAdvance` failure — the gate remains the pre-write enforcer.
- **Tests:** CONT-09 (`HasStaleState` and `RecoveryReason="OCC drift: fingerprint mismatch"` → `STALE`). `TestCoreMustNotImportWebAdapterContinuation` preserves `DigestMatches`/`IsStale` still green via `GEN-12`.

---

## 10. No-Progress Behavior (§12)

- **Signal:** bounded history `Task.History []StepHistoryEntry` (`StepID`, `Outcome`, `StateFingerprint`, `EvidenceDigest`, `Patches`).
- **Rule:** `NO_PROGRESS` when last 3 entries share the same `StateFingerprint` + `Outcome` + `EvidenceDigest` and all have `Patches==0`, or the same `StepID`×3 with same outcome and `Patches==0`.
- **Effect:** terminal `NO_PROGRESS` (no `NextStep`). At the staging layer the same invariant surfaces as `ErrRecoveryHalted` after `maxConsecutiveZeroDeltas=1`. Uses existing scheduler/durable budget/history limits (`TaskRemainingBudget`/`History`), not a hidden autonomous-loop counter.
- **Tests:** CONT-10 (3× `partial` with same digest/evidence/0 patches → `NO_PROGRESS`), `TestRecovery_RepeatedCeilingBlocks` / `TestRecovery_ZeroDeltaHaltAndReset` still green.

---

## 11. Tests

### §18 Required tests (all in `internal/continuation/continuation_test.go`)

| ID | Name | What is proven |
|---|---|---|
| CONT-01 | Completed step produces COMPLETE | `Derived` with all plan steps in `CompletedSteps` + `Verified` → `COMPLETE`, no NextStep |
| CONT-02 | Partial produces CONTINUE not failure | `partial`/`IsPartialOutput` → `CONTINUE` (never `FAILED`) with bounded NextStep |
| CONT-03 | Derives different next step from evidence | Evidence of remaining `b.go` after `a.go` complete → NextStep targets `b.go`, not repeat |
| CONT-04 | Cannot create authorization | No `Grant`/`Authorize`/`Approve`/`Capability` field on `ContinuationDecision`/`StepProposal`; MUTATE proposal carries no grant |
| CONT-05 | Cannot expand `$hot` | `AllowedScope=[a.go]` + plan wants `b.go` → `AWAITING_APPROVAL` (not silent expansion) |
| CONT-06 | Cannot create capabilities | No `Capability`/`Permission` field on decision; proposal never elevation |
| CONT-07 | Cannot directly execute | `DeriveNextStep` leaves marker file untouched; source contains no `os.WriteFile`/`os/exec`/`substrate.Execute` |
| CONT-08 | Cannot directly schedule | `StepProposal.ID` is not scheduler `step-%d-of-%d`; source contains no `StepScheduler`/`Schedule(` |
| CONT-09 | Stale/OCC prevents continuation | `HasStaleState` or `RecoveryReason` drift → `STALE` with no NextStep |
| CONT-10 | No-progress terminates | 3× identical `partial` with zero patches → `NO_PROGRESS` |
| CONT-11 | Multi-step completes across steps | `INVESTIGATE→ANALYZE→MUTATE→VERIFY→OBSERVE` each step `CONTINUE`, final → `COMPLETE` |
| CONT-12 | 1024-token constrained provider progresses | `ProviderCeiling=1024` + `partial` → `CONTINUE` with `EstimatedTokens ≤1024`; task `RemainingBudget>1024` preserved |
| CONT-13 | Transcript loss is harmless | Same durable `StateFingerprint`/`EvidenceDigest` with vs without model observations → identical decision, `StateDigest` from durable inputs only |
| CONT-14 | Uses observation/evidence not model claims | `MODEL_PROPOSAL("fixed")` + `VERIFICATION(failed)` → not `COMPLETE`; model-only → `VERIFY` continuation |
| CONT-15 | Mutation re-enters authorization | `MUTATE` NextStep carries no grant field (must re-enter existing guard to be authorized) |
| CONT-16 | No second scheduler | Repository contains no `type AdaptiveScheduler`/`ContinuationScheduler`/`AgentScheduler`/`ProblemScheduler` |
| CONT-17 | No second execution authority | `derive.go` has no `type RuntimeExecutor`; no continuation executor file |
| CONT-18 | Domain-neutral static-web | No `ReactContinuation`/etc. in `internal/continuation/*.go` (non-test) |

### §19 Architecture tests (`internal/architecture/continuation_invariants_test.go`)

| Test | Invariant |
|---|---|
| `TestContinuationMustNotImportForbidden` | `internal/continuation` ∤→ `runtime/executor`, `runtime/scheduler`, `execution`, `core/domain/authorization`, `ui`/`tui`, `provider`/`providers`/`ai`, `adapters/web` |
| `TestContinuationMustNotContainExecutionPrimitives` | no `os/exec` import and no `os.WriteFile` call in continuation |
| `TestCoreMustNotImportWebAdapterContinuation` | `understanding`, `problemsurface`, `changesurface`, `mutationstrategy`, `problem`, `continuation` ∤→ `adapters/web` and no `StaticWeb` code reference |
| `TestNoSecondSchedulerOrExecutor` | no `type AdaptiveScheduler`/`ContinuationScheduler`/etc.; canonical `scheduler.go` and `execution/executor.go` still present |
| `TestContinuationDomainNeutralArchitecture` | no domain-specific `*Continuation` type in continuation |
| `TestContinuationHasNoAuthorizationFields` | `ContinuationDecision`/`StepProposal` struct has no grant/authorize/capability field |

### Integration (§20)

- `TestContinuationIntegration_RealExecutionPath` in `internal/runtime/scheduler` — derives a real `ProblemSolvingPlan` (`problem.Derive`) from a Go worker repo (`understanding.Derive` + `problemsurface.Derive`), schedules it (`Schedule`), executes one bounded step via `RunNext` staging with `finish_reason=length` (→ `StepOutcomePartial`, 0 patches), projects authoritative `Observation`s, calls `DeriveContinuation` → `ActionContinue` with bounded `StepProposal`, and re-schedules via `ContinuationToTaskSpec` → `Schedule` (scope not expanded, second scheduler not created).

---

## 12. Test Results

```
go test ./internal/continuation/... -v
  TestCONT01…TestCONT18 — all PASS

go test ./internal/architecture/... -run Continuation|NoSecond -v
  TestContinuationMustNotImportForbidden — PASS
  TestContinuationMustNotContainExecutionPrimitives — PASS
  TestCoreMustNotImportWebAdapterContinuation — PASS
  TestContinuationDomainNeutralArchitecture — PASS
  TestContinuationHasNoAuthorizationFields — PASS
  TestNoSecondSchedulerOrExecutor — PASS

go test ./internal/runtime/scheduler/... -run TestContinuationIntegration -v
  TestContinuationIntegration_RealExecutionPath — PASS

go test ./... -count=1
  all packages — PASS

go vet ./... — 0 issues
golangci-lint ./internal/continuation/... — 0 issues
golangci-lint ./internal/runtime/scheduler/... — 0 issues
golangci-lint ./internal/architecture/... — 0 issues
```

Existing lock suites (`GEN-01`…`GEN-12`, `TestPhase2_*`, `TestPhase3_*`, `TestPhase4PipelineLock`, `TestPhase6_*`, `TestLea*`, `TestUINeverFabricates*`) remain green.

---

## 13. Remaining Risks

| Risk | Severity | Mitigation |
|---|---|---|
| `TaskHistory` is caller-populated — a caller that never appends history weakens no-progress detection to the scheduler's `PostStepEvaluation` (`ConsecutiveZeroDeltas`) only. | Low | `PostStepEvaluation` remains the authoritative zero-delta gate (`threshold=2`, `max=1` recovery turn). Continuation's history check is additive; `ErrRecoveryHalted` still halts inside `RunNext`. Document that callers must append `StepHistoryEntry` per bounded step. |
| `AllowedScope` is advisory inside continuation — the hard envelope is still enforced pre-write by `execution.RuntimeExecutor` + `substrate` + `scope.Root.Verify` and `authorization.Scope` clause. Continuation's `AWAITING_APPROVAL` is a proposal signal, not the final security boundary. | Low | Keep `AWAITING_APPROVAL` as the bridge signal; do not let orchestration treat it as implicit grant. |
| `Observation.Verified` is a boolean derived from `EvidenceVector`/`VerificationReport` — a verifier-absent workspace can produce `UNVERIFIED` that continuation must not promote to `COMPLETE`. | Low | `isTaskComplete` requires `Verified==true`; `VERIFIED` observation must be present or caller must pass `Verified=false` so continuation does not prematurely complete. Existing evidence-level semantics (`L0`→`L5`) remain the source of truth. |
| `DeriveNextStep` does not yet wire `internal/runtime/durable.TaskStore` events (`EventExecutionCommitted`/`EventVerificationResult`) as first-class observations — current integration passes typed `Observation`s manually. | Low | Next phase can project `LedgerEvent` → `Observation` without changing the contract; types already carry `KindStateTransition`. |
| Static-web domain reasoning still lives only in `adapters/web` — any new domain (perf, React, Go AST) that wants richer evidence must add its own evidence provider, not a new continuation type. | None | Enforced by `TestContinuationDomainNeutralArchitecture`; documented in `IZEN_BOUNDED_ADAPTIVE_CONTINUITY.md` §15. |

---

## 14. Completion Gate (§23)

All 23 gates are satisfied:

```
[x] Large task can progress through multiple bounded steps
[x] One model output is not treated as the entire task
[x] Partial model output can continue from durable state
[x] Continuation cannot authorize
[x] Continuation cannot execute
[x] Continuation cannot schedule
[x] Continuation cannot expand scope
[x] Existing StepScheduler remains authoritative
[x] Existing execution authority remains authoritative
[x] Authorization semantics remain unchanged
[x] $prompt semantics remain unchanged
[x] $hot semantics remain unchanged
[x] OCC/state freshness remains enforced
[x] No-progress continuation terminates safely
[x] Model transcript is not durable truth
[x] Observation/evidence drives state progression
[x] ProblemSolvingPlan remains domain-neutral
[x] MutationPlan remains mutation-specific
[x] No static-web leakage returns to core
[x] No second runtime is introduced
[x] No second scheduler is introduced
[x] No second execution authority is introduced
[x] go test ./... passes
[x] go vet ./... passes
[x] golangci-lint passes
```

---

*Bound the step, not the task. The runtime now sustains bounded, evidence-driven problem solving without a second authority, a second scheduler, or transcript-driven state.*

