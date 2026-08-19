# Phase 5 — Autonomous Runtime Integration Report

Phase 5 makes the Phase 4 autonomous runtime loop contract **real**. It binds
`internal/autonomy/runtime_loop.go` to the canonical `RuntimeExecutor` through a
composition-boundary adapter+driver package (`internal/runtime/autonomy`),
proving a small real bounded loop — `Observe → Decide → Execute → Verify →
Interpret → Complete/Recover/AwaitHuman/Abort` — over canonical `ExecutionResult`
truth. It removes the fabricated UI preview loop-transition emission, wires the
driver at the composition root, and pins every boundary with architecture
guards. It is **not** a new execution authority: the driver is a consumer of the
`RuntimeExecutor`, exactly one execution engine exists, and approval/clarify
resumption is authoritative.

## 1. STEP 1 — Current-state audit (as-audited)

### 1.1 Verified Phase 4 after-state

Phase 4 delivered the loop contract but deliberately did **not** wire it. The
audit verified the following real surfaces that Phase 5 must connect to:

| Surface | Location | Verified fact |
|---|---|---|
| `RuntimeExecutor.Execute` | `internal/execution/executor.go:418` | `Execute(ctx, ExecuteRequest) (*ExecutionResult, error)`; canonical lifecycle events; sole provider-invocation surface |
| `selectStrategy` | `executor.go:910` | explicit target + nil `Strategy` → `TargetedMutation` profile |
| `invokeMutation` | `executor.go:984` | reads target content itself; zero-value targeted profile works |
| `finalizeResult` | `executor.go:1433` | maps `MutationOutcome` → `ExecutionResult`/proof |
| `OutcomePatchGenerationFailed` | `internal/execution/mutation.go` | string value is **`"patch_failed"`** (NOT Phase 4's `patch_generation_failed`) |
| `ExecutionStrategyProfile` | `internal/execution/strategy/strategy.go:296` | `Policy()` at :370 normalizes zero → `ContextPolicyNone` |
| `strategy.Target` | `strategy/strategy.go` | `Raw/Resolved/Status/Exists` |
| `IntentGateway.SelectStrategy` | `internal/execution/intent.go` | returns the unconditionally selected profile value |
| `VerificationReport` | `internal/execution/verify.go` | `Results []VerificationResult` + `Passed bool` (no Stage) |
| `events.NewLoopTransition` | `internal/events/events.go:882` | `LoopTransitionPayload{From,To,Event,Reason}` |
| `bus.Publish(ev)` | `internal/events/bus.go:210` | non-blocking; per-subscription dispatch goroutines |
| `autonomy.Engine.PublishTransitions` | `internal/autonomy/engine.go:349` | decision-time trace publisher (unchanged, legitimate) |
| `publishAutonomyLoop` | `internal/ui/autonomy_route.go` | **fabricated** preview `loop.transition` emission — to be removed |

### 1.2 Authority audit (unchanged from Phase 3/4)

| Authority | Owner | Proof |
|---|---|---|
| Intent classification | `autonomy.Classify` + `strategy.Select` | `compose.go`, negative tests |
| Target resolution | `strategy.Select` | `selector.go` |
| Provider invocation | RuntimeExecutor only | architecture negative tests |
| Filesystem mutation | RuntimeExecutor approval boundary only | architecture negative tests |
| Approval | `executor.Approve` + `AuthorizationEngine` | `runtime_executor.go` |
| Verification | `Verifier.RunAll` inside runtime apply | `SetVerifier` |
| Lifecycle events | runtime graph only | `TestLifecycleEventsGeneratedOnlyFromGraph` |
| Loop control flow | **NEW: `internal/runtime/autonomy.Driver`** (Phase 5) | bounded, consumer-only |
| Budget | `budget.MutationBudget` | `core/budget/budget.go` |

### 1.3 Vocabulary mismatch discovered (root cause of a driver-test failure)

The canonical executor failure outcome for a failed model invocation is
`OutcomePatchGenerationFailed` with the **string value `"patch_failed"`**
(`internal/execution/mutation.go`), whereas Phase 4's autonomy
`OutcomePatchGenFailed` has the string value `"patch_generation_failed"`. A
lossless adapter mapping therefore surfaced an unrecognized outcome in the
decider (`unrecognized outcome: patch_failed`). Phase 5 adds the canonical
string to the autonomy vocabulary:

```
OutcomePatchFailed ExecutionOutcome = "patch_failed"   // canonical string, mirrors execution.MutationOutcome
```

and classifies it as `FailureRecoverable`, while keeping Phase 4's
`OutcomePatchGenFailed = "patch_generation_failed"` for backward compatibility
with the recovery matrix. **Every other** canonical `MutationOutcome` string
already mirrored 1:1 onto the extended autonomy vocabulary.

### 1.4 Loop-history fidelity bug discovered

`RuntimeLoop.applyDecision` set `l.state` **before** `push`, so every recorded
transition reported the new state as both `from` and `to`
(`executing -> executing`). The state machine was correct; only the history /
`loop.transition` projection was wrong. Phase 5 fixes `applyDecision` to push
first (capturing the true from-state) and then mutate, so projections show
`deciding -> executing` etc. — this is essential now that the driver publishes
transitions from the history.

## 2. STEP 2 — The composition-boundary package (`internal/runtime/autonomy`)

### 2.1 Why a new package

`internal/autonomy` MUST stay execution-free (architecture-invariant test: no
`internal/execution`, `internal/providers`, `internal/patch`, `internal/ai`
imports). The adapter and driver therefore live in `internal/runtime/autonomy` —
the **only** package that may import both the loop contract and the execution
authority. It is bound at the composition root (`internal/runtime/compose`).

### 2.2 `ExecutorAdapter` (the Executor-port implementation)

```
Resolved{ Prompt, Profile, Targets, Options, Ambiguous }
```

- `NewExecutorAdapter(root, gateway, executor)` — takes the workspace root, the
  `IntentGateway` (target resolution authority) and the `RuntimeExecutor`.
- `Resolve(prompt) Resolved` — calls `gateway.SelectStrategy`. A
  `HumanClarification` profile produces `Ambiguous=true` with the strategy's
  raw options copied; **targets are never leaked** on the ambiguous path
  (verified by test).
- `Execute(ctx, LoopRequest) (Observation, error)` — selects the profile; when
  explicit targets meet a `HumanClarification` profile, the request is handed to
  the executor's own explicit-target path with `Strategy=nil` (executor decides,
  `selectStrategy` → `TargetedMutation`). Maps the canonical `ExecutionResult`
  to the loop's `Observation` via `observe`, casting the intent losslessly.
- `Approve` / `Reject` — forward through `executor.Approve/Reject`; returns the
  resulting canonical outcome string.

`observe(req, res)` maps `ExecutionResult` → `Observation` with a lossless
canonical outcome cast (`autonomy.ExecutionOutcome(res.Proof.Outcome)`) and
`Intent: autonomy.Intent(req.Intent)`.

### 2.3 `Driver` (the real bounded loop)

```
Driver{ adapter, bus, bounds, decide, repair, loop, prompt, resolved, req, obs, ... }
```

- `NewDriver(adapter, bus, opts...)` with `Option`s: `WithLoopBounds`,
  `WithDecider`, `WithRepair`.
- `Run(ctx, objective)` — creates a **fresh** `RuntimeLoop` per run (re-entry is
  always a new bounded run), `Start`, `Resolve` (parks at AwaitingHuman if
  ambiguous), feeds a context observation, then `observeAndRun`.
- `ResumeApprove` / `ResumeReject` / `ResumeClarify` — human-boundary
  resumption. Approve/reject interpret the **same** pending execution (no
  re-execute); clarify re-resolves and re-executes (no mutation happened).
- Accessors: `State`, `Boundary`, `Termination`, `History`, `LastObservation`.
- `observeAndRun` drives `Observe → decide → Step/Execute/Consume →
  publish` until the loop is terminal or parks at AwaitingHuman.
- `decideDefault` maps an observation onto the closed decision vocabulary:

| Observation | Decision |
|---|---|
| context observation (no outcome) | Continue (execute) |
| changed / created / nochange / completed | Complete |
| pending_approval | AskHuman (+ PatchID) |
| ClarificationRequired | AskHuman |
| cancelled / rejected / artifact_rejected | Abort (permanent) |
| failures (incl. patch_failed) | RecoverFailure (bounded) |
| unrecognized outcome | Abort |

- `defaultRepair` appends the failed outcome to the evidence ledger before a
  bounded re-execution.
- `publish` emits `events.NewLoopTransition` for each new history entry since the
  last publish — the driver is the single owner of runtime-loop transition
  publication.
- Cancellation mid-flight → clean permanent abort, never an auto-retry.

### 2.4 Loop-counter ownership

The driver feeds the decider the authoritative counters through the bounded
observation: `d.obs.AttemptNum = d.loop.Attempts()` and
`d.obs.RecoveryCycle = d.loop.RecoveryCycles()` before deciding. The loop
remains the counter owner; the decider never mutates them.

## 3. STEP 3 — Autonomy contract extension (`internal/autonomy/runtime_loop.go`)

- **`ExecutionOutcome` vocabulary extended** to mirror the canonical mutation
  outcomes 1:1: `changed`, `created`, `nochange`, `artifact_rejected`,
  `patch_failed` (`OutcomePatchFailed`, the canonical string), `apply_failed`,
  `verify_failed`, `skipped`, `pending_approval`, `rejected`, `completed` — while
  keeping Phase 4's `no_artifact`, `cancelled`, `failed`, `artifact_produced`,
  `patch_generation_failed`.
- **`ClassifyOutcome` updated** — `OutcomePatchFailed` classifies as
  `FailureRecoverable` alongside `failed`/`patch_generation_failed`/
  `apply_failed`/`verify_failed`.
- **`Observation`** gains `PatchID` (the pending patch an approval refers to)
  and `ClarificationRequired bool`.
- **`LoopRequest`** gains `RequestID`.
- **`LoopDecision`** gains `PatchID` and `Options []string`; `applyDecision`
  copies them into the `HumanBoundary`.
- **`HumanBoundary`** gains `PatchID`.
- **`Step` bounds-gating refined** — only execution-bound decisions
  (Continue/Retry/Repair) are gated on `violation()` before and after apply; a
  `Complete` at the attempt bound is honored; `RecoverFailure` with an exhausted
  recovery bound parks at AwaitingHuman instead of aborting.
- **`Attempts()` / `RecoveryCycles()` accessors** added.
- **`applyDecision` push-order fix** — transitions record the true from-state
  (see §1.4).

## 4. STEP 4 — Wiring at the composition root

`internal/runtime/compose/compose.go`:

```go
a.Autonomous = runtimeAutonomy.NewDriver(
    runtimeAutonomy.NewExecutorAdapter(root, a.Gateway, a.Executor),
    a.Bus,
)
```

`Application` gains `Autonomous *runtimeAutonomy.Driver`. The driver is
constructed but **not** auto-run — a run is a fresh bounded loop per objective,
started explicitly. This is deliberately minimal: no existing production path
changes its behavior; the driver is available to the runtime layer.

## 5. STEP 5 — UI preview de-fabrication

`internal/ui/autonomy_route.go`:
- **Removed** the `m.publishAutonomyLoop(trace)` call.
- **Removed** the `publishAutonomyLoop` method (the UI no longer fabricates
  `loop.transition` events).
- **Kept** `NewAutonomyLoopPreview` string rendering as an explicitly-labeled
  decision-time preview (`renderAutonomyDecision`, `loop : ...` line).

The UI remains a pure projection: it subscribes to `EventLoopTransition`
(`program.go:347`) and renders canonical transitions; it never publishes them.

## 6. STEP 6 — Architecture guards (Phase 5)

`internal/architecture/autonomous_runtime_invariants_test.go`:

| Guard | Asserts |
|---|---|
| `TestRuntimeAutonomyPackageHasSingleBoundary` | `internal/runtime/autonomy` must NOT import `internal/providers`, `internal/patch`, `internal/ai` — the single execution boundary is the RuntimeExecutor |
| `TestUINeverFabricatesLoopTransitions` | `internal/ui` must NOT import `internal/runtime/autonomy` (UI is a projection; the driver owns the loop) |
| `TestAutonomousDriverContractExists` | anti-vacuous: `ExecutorAdapter`, `NewExecutorAdapter`, `Driver`, `NewDriver`, `Resolved` all present |
| Phase 4 guards (retained) | `internal/autonomy` remains execution-free, no direct filesystem mutation, no provider invocation, contract symbols exist |

## 7. STEP 7 — Test coverage

### 7.1 `internal/runtime/autonomy/helpers_test.go`

- `mockProvider` — attempt-counting provider (increments on every call, so
  "provider was reached" is provable even for failing executions).
- `blockingProvider` — blocks until cancelled (mid-flight cancellation).
- `eventCollector` — subscribes to `EventLoopTransition`, waits/asserts
  transitions, `hasTransition(from,to)`.
- `testExecutor` — real `RuntimeExecutor` over `execution.NewVerifier` +
  `SetCustomSteps([{noop, "true"}])` + `SetAuthorization`; temp workspace with a
  real `note.txt` target.

### 7.2 `adapter_test.go` (all pass)

`TestAdapter_ResolveReadOnly`, `TestAdapter_ResolveClarification`,
`TestAdapter_ExecuteReadOnly`, `TestAdapter_ExecuteMutationParksAtApproval`,
`TestAdapter_ClarificationNoTarget`, `TestAdapter_ApproveMapsChanged`,
`TestAdapter_RejectMapsRejected`, `TestAdapter_ExplicitTargetExecutesAfterClarification`.

### 7.3 `driver_test.go` (all pass)

| Test | Proves |
|---|---|
| `TestDriver_ReadOnlyCompletes` | real read-only run completes with provider calls |
| `TestDriver_MutationApprovalCycle` | mutation parks at AwaitingHuman pending_approval with PatchID; approve resumes and applies |
| `TestDriver_MutationReject` | reject aborts cleanly; the mutation is not applied |
| `TestDriver_ClarificationResume` | ambiguous objective parks; clarify re-resolves and executes |
| `TestDriver_RecoveryExhaustionParks` | a bounded failure loop parks at AwaitingHuman — never aborts |
| `TestDriver_BudgetBlocksNextExecution` | the next execution is blocked by bounds before any provider call |
| `TestDriver_CancellationBeforeExecution` | cancellation before execute → clean permanent abort, zero calls |
| `TestDriver_CancellationDuringExecution` | cancellation mid-flight → clean permanent abort, never an auto-retry |
| `TestDriver_PublishesLoopTransitions` | driver is the single publisher; canonical `observing→deciding`, `deciding→executing` transitions exist (no self-transitions) |
| `TestDriver_TerminalLoopRejectsReentry` | a completed loop is frozen; a second `Run` is a fresh bounded run |

### 7.4 Semantic regressions fixed

- `mockProvider` counting was corrected to count **attempts** (the original
  increment-on-success masked that failing executions were reaching the
  provider).
- The vocabulary mismatch (§1.3) and the history-fidelity bug (§1.4) were both
  caught by these tests.

## 8. Definition of Done

- [x] STEP 1 audit complete (this report §1) — executor path, strategy
      selection, verification report, bus, loop history, UI preview.
- [x] STEP 2 composition-boundary package created (`internal/runtime/autonomy`:
      `ExecutorAdapter` + `Driver`) over the real `RuntimeExecutor`.
- [x] STEP 3 autonomy contract extended (outcome vocabulary, PatchID,
      clarification flag, accessors, bounds-gating fix, push-order fix).
- [x] STEP 4 wiring at the composition root (`compose.go` → `Application.Autonomous`).
- [x] STEP 5 UI preview de-fabrication (`publishAutonomyLoop` removed).
- [x] STEP 6 architecture guards extended (single-boundary, UI-is-projection,
      contract-exists).
- [x] STEP 7 tests: adapter (8), driver (10), plus race runs — all pass.
- [x] STEP 8 validation: `go build ./...`, `go vet ./...`,
      `golangci-lint run ./...`, `go test ./... -count=1`,
      `go test -race` on autonomy/runtime-autonomy/ui/architecture — all clean.

## 9. Validation results

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `golangci-lint run ./...` | 0 issues |
| `go test ./... -count=1` | all pass |
| `go test -race` (autonomy, runtime/autonomy, ui, architecture) | all pass |

## 10. Files changed

| File | Change |
|---|---|
| `internal/runtime/autonomy/adapter.go` | new — `ExecutorAdapter`, `Resolved`, `observe`, `Approve`/`Reject` |
| `internal/runtime/autonomy/driver.go` | new — `Driver`, `Run`/`Resume*`, `decideDefault`, `defaultRepair`, `publish` |
| `internal/runtime/autonomy/helpers_test.go` | new — provider mocks, event collector, real-executor harness |
| `internal/runtime/autonomy/adapter_test.go` | new — 8 adapter tests |
| `internal/runtime/autonomy/driver_test.go` | new — 10 driver tests |
| `internal/autonomy/runtime_loop.go` | extended — outcome vocabulary (`OutcomePatchFailed`), `Observation.PatchID`/`ClarificationRequired`, `LoopRequest.RequestID`, `LoopDecision.PatchID`/`Options`, `HumanBoundary.PatchID`, `Attempts()`/`RecoveryCycles()`, bounds-gating fix, `applyDecision` push-order fix |
| `internal/runtime/compose/compose.go` | wired `Application.Autonomous` driver |
| `internal/ui/autonomy_route.go` | removed `publishAutonomyLoop` call + method |
| `internal/architecture/autonomous_runtime_invariants_test.go` | added 3 Phase 5 guards |
| `docs/design/PHASE/PHASE_5_AUTONOMOUS_RUNTIME_INTEGRATION_REPORT.md` | this report |

## 11. Design decisions

- **Boundary package, not a boundary interface.** The adapter+driver live in
  `internal/runtime/autonomy` rather than extending `internal/autonomy`, because
  the loop contract must stay execution-free (Phase 4 invariant #3) and a new
  importable interface adds nothing the composition root already provides. The
  package doc comment pins the single-boundary rule.
- **The driver publishes transitions; the autonomy engine still publishes
  decision-time traces.** `Engine.PublishTransitions` (Phase 4) emits the
  decision-time capability/cross-workspace micro-loop; the Phase 5 driver emits
  the runtime bounded-loop transitions. Both are canonical `loop.transition`
  events with the same payload; neither is UI-fabricated.
- **`OutcomePatchFailed = "patch_failed"` is the canonical mirror**; Phase 4's
  `OutcomePatchGenFailed = "patch_generation_failed"` is retained for matrix
  compatibility. `decideDefault` handles both.
- **Approval resume interprets the same execution** — never re-executes; a
  rejected mutation leaves the filesystem untouched (proven by test). Clarify
  resume re-executes because no mutation happened.
- **Each `Run` is a fresh bounded loop** — re-entry after terminal is always a
  new run (proven by `TestDriver_TerminalLoopRejectsReentry`); the completed
  loop object itself is frozen.
- **Bounds gate execution only** — a `Complete` at the attempt bound is honored;
  recovery exhaustion parks (AwaitingHuman) instead of aborting, so the human
  can start a fresh bounded run.
- **The driver is not auto-run at wiring.** Wiring must not change existing
  production behavior; a run is explicit per objective.

## 12. Invariant status table (Phase 5)

| # | Invariant | Status |
|---|---|---|
| 1 | `internal/runtime/autonomy` is the only composition-boundary package | **PROVEN** — architecture guard |
| 2 | Driver is a consumer of RuntimeExecutor; single execution boundary | **PROVEN** — adapter/executor + negative imports |
| 3 | Adapter/driver never import providers/patch/ai | **PROVEN** — architecture guard |
| 4 | UI is a pure projection; never publishes loop.transition; never imports the driver | **PROVEN** — `publishAutonomyLoop` removed + guard |
| 5 | Outcome vocabulary mirrors canonical executor strings 1:1 | **PROVEN** — `OutcomePatchFailed` + tests |
| 6 | Approval resume interprets same execution; no re-execute | **PROVEN** — driver test |
| 7 | Rejection leaves filesystem untouched | **PROVEN** — driver test |
| 8 | Clarify resume re-executes (no mutation happened) | **PROVEN** — driver test |
| 9 | Cancellation is a clean permanent abort, never auto-retry | **PROVEN** — two driver tests |
| 10 | Recovery exhaustion parks, never aborts | **PROVEN** — driver test |
| 11 | Next execution is blocked by bounds before any provider call | **PROVEN** — driver test |
| 12 | Transitions record true from-state (no self-transitions) | **PROVEN** — push-order fix + `hasTransition` |
| 13 | Completed loop is frozen; re-entry is a fresh bounded run | **PROVEN** — driver test |
| 14 | `internal/autonomy` remains execution-free | **PROVEN** — retained Phase 4 guards |

## 13. Remaining risks / NOT PROVEN

- **The driver is wired but not yet driven by a production command path.** It is
  constructed at the composition root; no existing user command starts a driver
  run yet. Driving the driver from a future `$autonomous`-style objective (or a
  `$hot` build path) is the natural next consumer.
- **Approval UI → driver resumption bridge is not wired.** The canonical
  approval surface renders; wiring the UI's approve/reject to
  `Application.Autonomous.ResumeApprove/Reject` is follow-up work.
- **Cross-publisher `loop.transition` naming** — the autonomy engine
  (decision-time) and the driver (runtime) both publish `loop.transition`. They
  are semantically distinct loops sharing one event type; if the projection
  ever needs to distinguish them, a discriminator field would be required.
- **`internal/agent` `AgentLoop`** remains dead prototype code (zero production
  callers), untouched by Phase 5.
