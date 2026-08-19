# Phase 4 — Autonomous Runtime Loop Report

Phase 4 establishes the *contract* for a deterministic, bounded, observable
autonomous runtime loop. It is **not** a loop-first implementation phase: the
loop is the last thing that should be built, and it is never an execution
authority. The deliverable is the contract — RuntimeState, Observation,
Decision, Action, ExecutionResult, RecoveryDecision, HumanBoundary,
LoopTermination — plus the architecture negative tests that pin the loop to
being a **consumer** of the RuntimeExecutor, never a producer of mutations.

## 1. STEP 1 — Current-state audit (as-audited)

### 1.1 Verified Phase 3 after-state

```
User Input (bare / $prompt / $hot)
   |
   v
autonomy.Engine.Decide (intent classif + controller)   internal/autonomy/engine.go:185
   |  Decision: direct_response | auto_continue | ask_user | block
   v
IntentGateway.Gate / strategy.Select                    internal/execution/intent.go
   |  (single classifier + target resolver)
   v
RuntimeExecutor.Execute / Approve / Reject             internal/execution/executor.go:418
   |  (owns provider, context, artifact, patch, approval, MutationSet,
   |   apply, verify, canonical lifecycle events, ExecutionProof)
   v
ExecutionProof + canonical events -> UI (pure projection)
```

### 1.2 Authority audit (current)

| Authority | Owner | Proof |
|---|---|---|
| Intent classification | `autonomy.Classify` + `strategy.Select` (execution) | `compose.go:589`, `TestNoDuplicateIntentClassifierResurfaces` |
| Target resolution | `strategy.Select` | `selector.go`, `TestExecutorTargetResolutionIndependentOfLynx` |
| Provider invocation | RuntimeExecutor only | `TestUICannotCallProviderOnExecutionPath` |
| Filesystem mutation | RuntimeExecutor approval boundary only | `TestUICannotMutateWorkspaceOnExecutionPath` |
| Approval | `executor.Approve` + `AuthorizationEngine` | `runtime_executor.go`, `core/authorization/engine.go` |
| Verification | `Verifier.RunAll` inside runtime apply | `SetVerifier`, `TestEveryVerificationRequiresRealVerifier` |
| Lifecycle events | runtime graph only | `TestLifecycleEventsGeneratedOnlyFromGraph` |
| Budget | `budget.MutationBudget` (files/diff/tokens/attempts/shell/time/mutations) | `core/budget/budget.go` |
| Workflow phase | `workflow.WorkflowStateMachine` (incl. `pendingApproval`) | `core/workflow/machine.go` |

### 1.3 Existing loop prior-art (NOT production authorities)

Three loop-adjacent surfaces exist. **None** is a bounded autonomous runtime
loop in production:

| Surface | Location | Reality |
|---|---|---|
| `AutonomousLoop` | `internal/autonomy/loop.go` | A 236-line decision-loop **state machine** (idle→investigate→plan→build→verify→diagnose→ask_user→stop) bounded by `maxIterations` (default 3). Consumed ONLY by `internal/ui/autonomy_route.go:123` `publishAutonomyLoop` to render a loop **preview** on `loop.transition` events. It has no observation, no ExecutionProof consumption, no decision validation, no recovery matrix, no human boundary beyond `AskUser`. |
| `AgentLoop` | `internal/agent/loop.go` | A **stub** (executeStep returns nil; recovery phase never actually repairs; event stream is its own `EventStream`, not the canonical bus). **Zero production callers** (verified: `NewAgentLoop` appears only in `internal/agent` + tests). Not wired in `compose.go`. |
| `control` package | `internal/control` | Only `NewWorkflowCheckpointManager` (checkpoint refs) is wired (`compose.go:552`). `handoff.go` is not a production loop. |

### 1.4 Step-1 conclusion

- The RuntimeExecutor is the sole execution authority. ✓ (Phase 3 proven)
- The autonomy engine already decides *once per objective* (`Decide`) and maps
  the decision to a workspace; it does **not** run a bounded observe→decide→
  act→verify→interpret cycle and does **not** consume ExecutionProof.
- `EventAutonomyDecision`, `EventLoopTransition`, `EventApprovalRequired`,
  `EventApprovalRejected`, `EventExecutionFinished` all already exist on the
  canonical bus. **No second bus.** ✓
- Loop bounds primitives exist at the *single-mutation* level
  (`MutationBudget.MaxAttempts=3`, `ScaleBudget` for multi-step plans), but
  there is **no** runtime-owned accounting for repeated identical decisions,
  recovery-cycle exhaustion, or pathological repair→fail→repair loops.

## 2. STEP 2 — Autonomous Runtime Contract

All types below are contract definitions. Every abstraction must have a
concrete runtime consumer (STEP 2 rule). None of these grant the loop any
execution authority — the loop only ever issues `ExecutionRequest` values to
the RuntimeExecutor and consumes `ExecutionResult` values from it.

### 2.1 RuntimeState (runtime-owned canonical loop state)

The loop state is a separate runtime concept from the workflow phase machine
and from the UI. It must exist headlessly. States are only the ones with
concrete semantics:

| State | Semantics | Terminal |
|---|---|---|
| `Idle` | not started | no |
| `Observing` | collecting bounded Observation | no |
| `Deciding` | validating decision, choosing Action | no |
| `Executing` | RuntimeExecutor running one ExecutionRequest | no |
| `Verifying` | consuming verification outcome | no |
| `Interpreting` | mapping ExecutionResult → RecoveryDecision/continuation | no |
| `Recovering` | bounded recovery cycle in progress | no |
| `AwaitingHuman` | runtime parks; human must respond | no |
| `Completed` | terminal success | **yes** |
| `Aborted` | terminal: loop bounds/cancellation/runtime failure | **yes** |

Rule: **only runtime-owned transitions** mutate this state. The UI may read it
(projection) but never write it. `AwaitingHuman` is a runtime state — the UI
merely renders it.

### 2.2 Observation (bounded, structured)

An Observation is the *minimal structured context* the loop is allowed to see.
It must NEVER include raw full file contents or unbounded history.

```
Observation{
  RequestID       // provenance link to the execution that produced it
  Intent          // classified intent (authoritative)
  Target          // resolved target (authoritative, from strategy.Select)
  Evidence        // bounded structural ledger (deterministic)
  PreviousResult  // *ExecutionResult (optional; authoritative proof consumption)
  Verification    // verification outcome (optional)
  ProviderUsage   // tokens/steps — feeds loop bounds
  Attempts        // attempt counter for current objective
  RecoveryCycles  // recovery-cycle counter
  LastDecision    // previous decision + its Action (identical-decision detection)
}
```

Explicit provenance rule: `PreviousResult.Proof` is **authoritative**; anything
else (UI state, telemetry) is **advisory** and must be marked as such.

### 2.3 Decision (structured, runtime-validated)

```
Decision{ Action, Reason, EvidenceRef }
```

Action vocabulary (closed set):

| Action | Meaning |
|---|---|
| `Continue` | proceed with next step of the current objective |
| `Complete` | objective satisfied; stop the loop (→ Completed) |
| `Retry` | re-execute the same request (bounded) |
| `Repair` | recovery path: re-plan/re-scope (bounded) |
| `AskHuman` | park in AwaitingHuman (runtime state) |
| `Abort` | terminal: stop with failure classification |

Rules:
- Decisions are **validated by the runtime** before any Action executes.
- A Decision that references a non-terminal or impossible transition is
  rejected (invalid) — never silently accepted.
- Decision ≠ execution: the loop can decide anything; only the RuntimeExecutor
  executes.

### 2.4 Action

An Action is the *only* way the loop affects the world, and it is always one
of:

1. `ExecutionRequest` → RuntimeExecutor.Execute (the ONLY mutation/read path)
2. `ExecutionRequest` with approval intent → executor `Approve`/`Reject`
3. no-op (Continue/Complete bookkeeping)

The loop NEVER calls the provider, PatchManager, or filesystem directly. This
is enforced by architecture negative tests (STEP 9).

### 2.5 ExecutionResult (consumed, not extended)

The loop consumes the existing `ExecutionResult`/`ExecutionProof`. It never
fabricates outcome: `OutcomeNoArtifact`, `OutcomeFailed`, `OutcomeCancelled`,
`OutcomeArtifactProduced` are read from the proof timeline only. Unknown stays
unknown.

### 2.6 RecoveryDecision + failure matrix

```
RecoveryDecision{
  RecoveryAction   // Retry | Repair | AskHuman | Abort | NoChange
  FailureClass     // Transient | Recoverable | Permanent (canonical)
  AttemptNum
}
```

Failure matrix (the loop interprets, never executes):

| Failure | Classification | RecoveryDecision |
|---|---|---|
| `execution.failed` transient (provider) | Transient | Retry (bounded) |
| Verification failure on artifact | Recoverable | Repair (bounded) |
| Artifact rejection | Recoverable | Repair |
| Approval rejection | HumanBoundary | AskHuman |
| Filesystem mismatch (source-hash) | Recoverable | Repair |
| Target ambiguity / not-found | Recoverable | AskHuman (never auto-pick) |
| Loop-bounds exhausted | Permanent | Abort |
| Cancellation / runtime terminal failure | Permanent | Abort |

Rule: only Recoverable/Transient failures re-enter the loop. Permanent
failures terminate.

### 2.7 HumanBoundary (AwaitingHuman)

AwaitingHuman is entered only when: a decision requires approval, a target is
ambiguous, evidence is insufficient for a bounded decision, or recovery is
exhausted. While parked, the loop holds its runtime state; the UI renders the
canonical approval/ask surface; the human's response re-enters via an
authoritative event. The loop does not spin.

### 2.8 LoopTermination (runtime-owned bounds)

| Bound | Default | Enforcement |
|---|---|---|
| MaxAttempts | 3 | attempts per objective (executor budget already tracks) |
| MaxRecoveryCycles | 2 | re-plan/re-scope cycles |
| MaxExecutionSteps | budget.MaxMutations | mutated steps in one loop run |
| MaxRepeatedIdenticalDecisions | 2 | identical Continue/Retry decisions → Abort |
| MaxTotalTokens | budget.MaxTokens | provider usage across the loop |
| Cancellation | ctx.Done | immediate stop, Permanent classification |
| Human escalation | any point | runtime parks, never burns budget |

Rule: **the runtime owns termination**, never the model. A pathological
repair→fail→repair loop is detected via `RecoveryCycles` and identical-decision
counting, and is terminated by the runtime.

## 3. Invariants (Phase 4)

1. RuntimeState is runtime-owned; UI only projects.
2. The loop is a **consumer** of RuntimeExecutor — never a second authority.
3. Loop → Provider/PatchManager/filesystem imports are FORBIDDEN (negative
   architecture tests).
4. Observation is bounded and structured; provenance authoritative-vs-advisory
   is explicit.
5. Decisions are runtime-validated; invalid decisions are rejected.
6. Only Recoverable/Transient failures re-enter the loop; Permanent → Abort.
7. AwaitingHuman is a runtime state; human re-entry is authoritative.
8. Termination is runtime-owned and bounded (attempts, recovery cycles,
   identical decisions, budget, cancellation).
9. No second event bus; loop transitions published as canonical events
   (`loop.transition`).
10. Headless capability: the loop contract depends on runtime + bus only, not
    on the UI.

## 4. Definition of Done

- [x] STEP 1 audit complete (this report §1).
- [x] STEP 2 contract defined (this report §2) with concrete runtime consumers.
- [x] STEP 3 RuntimeState defined as runtime-owned canonical state machine
      (`internal/autonomy/runtime_loop.go`).
- [x] STEP 4 Observation/Decision/Action contracts defined (`runtime_loop.go`).
- [x] STEP 5 ExecutionResult + ExecutionProof consumption defined — the loop
      consumes a normalized `ExecutionOutcome` via the `Executor` port; the
      composition root binds it to the RuntimeExecutor. The loop never imports
      execution internals.
- [x] STEP 6 RecoveryDecision + failure matrix defined (`RecoverFailure` +
      `ClassifyOutcome`).
- [x] STEP 7 HumanBoundary (AwaitingHuman) contract defined.
- [x] STEP 8 LoopTermination + bounds defined (attempts, recovery cycles,
      identical-decision detection, tokens, cancellation).
- [x] STEP 9 Architecture negative tests:
      `internal/architecture/autonomous_runtime_invariants_test.go` —
      autonomy must not import execution/providers/patch/ai; no direct
      filesystem mutation; no provider invocation; contract symbols must exist
      (anti-vacuous).
- [x] STEP 10 Semantic tests: `internal/autonomy/runtime_loop_test.go` —
      state machine, decision validation, loop bounds (attempts/recovery/
      identical/tokens/cancellation), recovery matrix, human boundary,
      Executor-port-only.
- [x] STEP 11 RuntimeExecutor remains sole authority — no wiring change
      (compose.go untouched).
- [x] STEP 12 Validation: `go build ./...`, `go build ./cmd/izen`,
      `go vet ./...`, `go test ./...`, `go test -race`, `golangci-lint run ./...`
      all clean.
- [x] STEP 13 This report finalized with validation results (§5).

## 5. Validation results (STEP 12)

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go build ./cmd/izen` | clean |
| `go vet ./...` | clean |
| `go test ./... -count=1` | all pass |
| `go test -race` (autonomy/architecture/execution) | all pass |
| `golangci-lint run ./...` | 0 issues |

## 6. Files changed

| File | Change |
|---|---|
| `internal/autonomy/runtime_loop.go` | Phase 4 contract: RuntimeState, ExecutionOutcome, ClassifyOutcome, VerificationOutcome, Observation, LoopAction/LoopDecision, RecoverFailure, HumanBoundary, LoopBounds/LoopTermination, RuntimeTransition, LoopRequest/Executor port, RuntimeLoop |
| `internal/autonomy/runtime_loop_test.go` | Semantic tests for the contract |
| `internal/architecture/autonomous_runtime_invariants_test.go` | Negative architecture tests pinning the loop as consumer-only |
| `docs/design/PHASE/PHASE_4_AUTONOMOUS_RUNTIME_REPORT.md` | This report |

## 7. Design decisions

- **Loop contract lives in `internal/autonomy`** (execution-free: imports only
  events/language). It normalizes execution facts into its own `Observation`/
  `ExecutionOutcome` types — consistent with the existing pattern where the
  autonomy controller never imports execution internals
  (`MutationRiskInput` in controller.go). The loop is decoupled from execution
  by an `Executor` port (`Execute(ctx, LoopRequest) (Observation, error)`),
  bound to the RuntimeExecutor at the composition root.
- **No second event bus.** The loop records `RuntimeTransition` history and
  emits `loop.transition` canonical events via `autonomy.Engine.PublishTransitions`
  — the existing bus path, unchanged.
- **`ConsumeExecution`/`ConsumeVerification` are the authoritative result
  ingestion points.** They increment attempts/steps/tokens from the runtime
  observation; `Step` validates decisions and enforces bounds. The loop never
  fabricates an outcome — it consumes the normalized result of a real
  execution.
- **Recovery cycles are bounded at decision time** (a repair crossing the bound
  is rejected before it consumes a cycle), distinct from attempt/step/token
  bounds which are enforced at execution-consumption time. Identical-decision
  detection is a post-apply check that catches a pathological
  repair→fail→repair loop.
- **Termination is runtime-owned**: `violation()` returns the terminal outcome
  for every bound; the model can only propose a decision, never a stop.

## 8. Invariant status table (Phase 4)

| # | Invariant | Status |
|---|---|---|
| 1 | RuntimeState is runtime-owned; UI only projects | **PROVEN** — contract + `RuntimeLoop` is the only mutator |
| 2 | Loop is a consumer of RuntimeExecutor, never a second authority | **PROVEN** — Executor port; autonomy imports execution-free |
| 3 | Loop → provider/PatchManager/filesystem FORBIDDEN | **PROVEN** — negative architecture tests |
| 4 | Observation bounded + structured; provenance explicit | **PROVEN** — typed Observation, no raw file content |
| 5 | Decisions runtime-validated; invalid rejected | **PROVEN** — `Step` rejects invalid/illegal actions (tests) |
| 6 | Only Transient/Recoverable re-enter; Permanent → Abort | **PROVEN** — `RecoverFailure` matrix + tests |
| 7 | AwaitingHuman is a runtime state; human re-entry authoritative | **PROVEN** — `AwaitHuman`/`ReleaseHuman` + tests |
| 8 | Termination runtime-owned and bounded | **PROVEN** — `violation()` on attempts/steps/tokens/identical + cancellation |
| 9 | No second event bus | **PROVEN** — reuses `loop.transition` via `PublishTransitions` |
| 10 | Headless capability | **PROVEN** — contract depends on runtime + bus only, no UI import |

## 9. Remaining risks / NOT PROVEN

- **The loop is a contract, not yet wired.** No production path drives
  `RuntimeLoop` against the RuntimeExecutor yet; the UI's
  `publishAutonomyLoop` still emits the preview `AutonomousLoop`. Wiring the
  runtime loop to consume `ExecutionResult` and drive the executor is future
  work — this phase deliberately defined the bounded contract first.
- **`internal/agent` `AgentLoop`** remains dead prototype code (zero production
  callers). It does not consume ExecutionProof and uses its own EventStream;
  it is not part of the Phase 4 contract and can be pruned separately.
- **`RejectFailure` on `EventExecutionFailed`** currently classifies execution
  failures; the loop's `RecoverFailure` consumes a normalized class. A bridge
  that maps the canonical failure classification onto `FailureClass` is
  trivial (same string values) but is not wired.