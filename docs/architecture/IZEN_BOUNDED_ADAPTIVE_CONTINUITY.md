# IZEN — Bounded Adaptive Continuity (Phase 4)

**Status:** Phase 4 complete — generality boundary + bounded continuity enforced  
**Scope:** `internal/continuation`, `internal/runtime/scheduler` (bridge), `internal/problem`, `internal/problemsurface`, `internal/understanding`, `internal/runtime/durable`, `internal/runtime/executor` (staging), `internal/execution` (single authority)  
**Baseline:** `docs/architecture/IZEN_PROBLEM_SOLVING_MODEL.md` (pre-Phase-4 boundary)

---

## 1. Objective

Extend the single-task single-step pipeline

```text
Task → Understanding → ProblemSurface → ProblemSolvingPlan → MutationPlan → Authorization → Execution
```

into a bounded multi-step task loop

```text
Human Intent
  → Problem Understanding
  → Problem Surface
  → ProblemSolvingPlan
  → Bounded Step Proposal
  → Authorization (existing)
  → Existing StepScheduler
  → Bounded Execution (existing authority)
  → Observation / Evidence (truthful)
  → Truthful State
  → Continuation Decision
  → Next Bounded Step (or termination)
```

Governing principle:

> **Bound the step, not the task.**

`TASK BOUNDARY ≠ STEP BOUNDARY`. A provider's output ceiling bounds one decision, never the whole task.

---

## 2. Architectural Invariants (Hard)

1. **No new execution authority.** `execution.RuntimeExecutor` + `runtime/substrate.Substrate` (composition root `internal/runtime/compose`) remain the sole mutation authority. `internal/runtime/executor.RuntimeExecutor` is the non-production control-plane coordinator (pinned by `TestPhase1_SingleProductionExecutionAuthority`).
2. **No new scheduler.** `runtime/scheduler.StepScheduler` (`Schedule`, `EffectiveBudget`, `AcceptStep`, `StepOutcome`, `RecoveryContext`, `StateFingerprint`) remains the single scheduling substrate. No `AdaptiveScheduler` / `ContinuationScheduler` / `AgentScheduler` exists.
3. **Continuation ≠ authorization.** Continuation may propose `The next useful step appears to be X` but never `Therefore X is authorized`. It cannot mint grants, expand scope, add capabilities, bypass `$hot`, convert `$prompt` into write authority, or authorize writes/execution. Every mutation re-enters the existing authorization boundary.
4. **Model output ≠ durable state.** Transcript, provider KV, hidden model memory are ephemeral. Durable state is execution evidence, observation events, workspace state, fingerprints, recovery context, task/step state, OCC, digests, verified artifacts. The model receives durable facts as context, but transcript is not truth.

All four are machine-enforced by `internal/architecture/continuation_invariants_test.go` and the `internal/continuation` import boundary.

---

## 3. Core Concept

A small domain-neutral continuation layer **above** the scheduler (`internal/continuation`):

```text
Observed Truth + Current Task State + Previous Step Outcome + ProblemSolving Intent + Evidence
  ↓
Continuation Derivation (pure, deterministic, proposal-only)
  ↓
Next Bounded Step Proposal (at most one)
```

It does not execute, authorize, schedule, mutate, create grants, expand scope, invent targets, or switch execution authority.

---

## 4. Bounded Step Lifecycle (§4)

```text
PROPOSED → ADMITTED → EXECUTING → OBSERVED → VERIFIED / PARTIAL / BLOCKED / FAILED → CONTINUE or TERMINATE
```

Implemented as `BoundedStepState` in `internal/continuation` with `Transition()` helper and reuse of existing `scheduler.StepOutcome` (`pending`/`complete`/`partial`/`failed`) and `durable.TaskStatus` / `RecoveryContext` where meanings already match. No parallel taxonomy: `VERIFIED` maps to scheduler `Complete` + verification passed; `PARTIAL` is `StepOutcomePartial` (finish_reason=length isolation).

---

## 5. Continuation Contract (§5)

```go
type ContinuationDecision struct {
    Action      ContinuationAction // COMPLETE | CONTINUE | BLOCKED | FAILED | STALE | AWAITING_APPROVAL | NO_PROGRESS
    Reason      string
    Evidence    []Observation      // authoritative only (VERIFICATION/OBSERVATION/EXECUTION_RESULT/STATE_TRANSITION)
    NextStep    *StepProposal      // present only when Action == CONTINUE
    StateDigest string             // durable fingerprint, not transcript hash
}

type StepProposal struct {
    ID              string
    Kind            string // problem.StepKind value, domain-neutral
    Targets         []string
    Rationale       string
    EvidenceRefs    []string
    EstimatedTokens int // per-step, bounded by provider ceiling
    StateDigest     string
}

type ObservationKind string // MODEL_PROPOSAL | EXECUTION_RESULT | OBSERVATION | VERIFICATION | STATE_TRANSITION
```

All fields are JSON-serializable, digest-bound, and carry no grant/capability fields (enforced by `TestContinuationHasNoAuthorizationFields`).

Possible actions cover the required §5 semantics:

| Action | Meaning |
|---|---|
| `COMPLETE` | All bounded steps verified, no unresolved work |
| `CONTINUE` | Derive next bounded step from evidence |
| `BLOCKED` | No evidence-backed next step determinable without invention |
| `FAILED` | Failed with no recoverable evidence path |
| `STALE` | State fingerprint / OCC drift — requires re-admission/recovery, not silent continuation |
| `AWAITING_APPROVAL` | Next step exceeds `$hot` envelope — needs human approval, not silent expansion |
| `NO_PROGRESS` | Repeated identical fingerprint/outcome/evidence with zero patches — safe termination |

No overlapping outcome system: `ContinuationAction` is task-level terminal/continuation, `StepOutcome` is step-level execution result.

---

## 6. Next-Step Derivation (§6)

Pure function:

```go
func DeriveNextStep(in DerivationInput) ContinuationDecision
type DerivationInput struct {
    Task            TaskStateView      // durable task state (scope, fingerprints, recovery, history, budgets)
    PlanSteps       []PlanStepView     // domain-neutral projection of ProblemSteps
    PreviousOutcome string             // scheduler StepOutcome value
    PreviousReason  string
    Observations    []Observation      // durable, evidence-backed
    Verified        bool
    HasStaleState   bool
    IsPartialOutput bool               // finish_reason=length
    AllowedScope    []string           // $hot envelope, must not be expanded
    // no transcript, no provider handle, no capability grant
}
```

Policy (order is material — freshness and no-progress are checked first):

1. **Stale/OCC drift** → `STALE` (no NextStep).
2. **No-progress** (≥3 identical fingerprint/outcome/evidence with zero patches) → `NO_PROGRESS`.
3. **Partial** (`IsPartialOutput || PreviousOutcome==partial`) → `CONTINUE` with next targets from durable plan/evidence, bounded to provider ceiling; never a blind prompt resend. Exceeding `AllowedScope` → `AWAITING_APPROVAL`. This preserves the task's original intent and the *per-step* provider budget.
4. **Model-only claim** without authoritative verification → `CONTINUE` with `VERIFY` step (model claim is not a state transition).
5. **Task complete** (all `PlanSteps` in `CompletedSteps` + `Verified`, or `complete`+`Verified`+no pending) → `COMPLETE`.
6. **Failed without recoverable evidence** → `FAILED`.
7. **Otherwise** derive next bounded targets from first incomplete `PlanStep`; fallback to evidence-backed observation subjects, then remaining scope. Every reference is evidence-backed — no invented targets. `AllowedScope` violation → `AWAITING_APPROVAL`; no determinable target → `BLOCKED`; otherwise `CONTINUE`.

The function never writes, authorizes, schedules, creates grants, expands scope, invents targets, or switches authority. It is safe to call from tests, the scheduler bridge, or future orchestration without side effects.

---

## 7. Partial Model Output (§7)

`finish_reason=length` is represented as `scheduler.StepOutcomePartial` (already present) and `executor.ProposalStagingBuffer` isolation: incomplete bytes are discarded, never cross the authorization boundary, and are recorded as `StagingDisposition{NeedsContinuation:true, Reason: OUTPUT_CEILING}`. The `State.RecoveryContext` carries `LastCleanByteOffset`/`ObservedTokens` so the scheduler's `PostStepEvaluation` enforces the Zero-Delta Recovery Limit invariant.

```
partial observation → persist truthful state (fingerprint, evidence, recovery) → DeriveNextStep → new bounded proposal → existing authorization → existing scheduler
```

The continuation step does **not** resend the same prompt; it uses durable `EvidenceDigest`/`StateFingerprint`/`RecoveryContext` to determine what remains unresolved.

---

## 8. Provider Constraints (§9)

Provider constraints belong to the current step (`StepBudget`, `ProviderCeiling`, `ObservedTokens`), never the whole task:

```text
Task (RemainingBudget = 8192)
  ├─ Step 1: budget 1024 → partial → CONTINUE (from durable state)
  ├─ Step 2: budget 1024 → skeleton → CONTINUE
  └─ Step N: budget 1024 → complete
```

A 1024-token ceiling therefore never truncates the task; the task progresses through multiple bounded steps (CONT-12). `EffectiveBudget` still owns per-step budget semantics; `continuation.boundedEstimate` caps `EstimatedTokens` at the provider ceiling without forcing a larger `max_tokens`.

---

## 9. Observation and Truthful State (§10)

Continuation distinguishes (via `ObservationKind`):

```text
MODEL_PROPOSAL      — untrusted claim ("I fixed the leak")
EXECUTION_RESULT    — what the runtime did (staging verdict, patch count)
OBSERVATION         — workspace/state observation (digest, fingerprint)
VERIFICATION        — evidence verdict (PASS/FAIL, required level)
STATE_TRANSITION    — durable ledger event (TASK_CREATED, CURSOR_DISPATCHED, etc.)
```

Only the latter four drive state transitions. Example: `Model: "I fixed the leak."` without `VERIFICATION=PASS` yields a `VERIFY` continuation step, not `COMPLETE`. `model transcript loss` therefore does not destroy durable state — the decision is deterministic from fingerprints/evidence (CONT-13, CONT-14).

---

## 10. State Freshness (§11)

Continuation respects `StateFingerprint` / `TargetSnapshot` / workspace digest / OCC `RecoveryContext` / `StateDigest`. Any `HasStaleState` or `RecoveryReason` containing `fingerprint/digest/stale/occ/drift` yields `STALE` and the `STALE/DRIFT` path requires re-admission/recovery. No continuation silently overrides an `OCC.ValidateAndAdvance` failure — the executor's `OCCGate` remains the enforcer before any substrate write.

---

## 11. No-Progress Protection (§12)

Repeated:

```text
same StateFingerprint + same StepOutcome + same EvidenceDigest + zero patches (3 consecutive)
or same StepID repeated with identical outcome and zero patches
```

→ `NO_PROGRESS` (or `ErrRecoveryHalted` at the scheduler staging layer). Detection uses existing task/step `History` and `RecoveryContext.ConsecutiveZeroDeltas` (already bounded at `maxConsecutiveZeroDeltas=1`), not a hidden autonomous-loop counter.

---

## 12. Authorization Interaction (§13)

| Surface | Continuation may derive | Continuation may NOT do |
|---|---|---|
| `/ask` (READ ONLY) | additional `INVESTIGATE`/`ANALYZE`/`OBSERVE` steps | create mutation authority |
| `/plan` (READ ONLY) | refine planning steps | mutate |
| `/build` | `MUTATE`/`VERIFY` proposals targeting existing scope | bypass authorization |
| `$prompt` | broad reasoning steps | authorize unlimited continuation |
| `$hot` (bounded pre-approval) | steps inside the envelope | expand envelope — exceeding → `AWAITING_APPROVAL` |

All mutation proposals re-enter `authorization.CapabilityGuard.Evaluate` via `runtime/executor` → `substrate.ExecuteUnit`; continuation never bypasses `$hot`.

---

## 13. ProblemSolvingPlan Integration (§14)

Reuse `INVESTIGATE` / `ANALYZE` / `EXPERIMENT` / `MUTATE` / `VERIFY` / `OBSERVE` as **problem-solving semantics**, not execution authority:

```text
INVESTIGATE → ≥1 READ steps
ANALYZE     → bounded reasoning step
MUTATE      → MutationPlan → Authorization → Scheduler → Execution
VERIFY      → bounded test/verification execution
OBSERVE     → evidence/state collection
```

The plan stays domain-neutral; it never maps 1:1 to `scheduler.StepType`. Layers remain separate.

---

## 14. MutationPlan Relationship (§15)

```
ProblemSolvingPlan (what progression is required?)
  ↓ (MUTATE step)
MutationPlan (how should a mutation be decomposed into bounded mutation steps?)
  ↓
Authorization
  ↓
Scheduler
```

`ProblemSolvingPlan ≠ MutationPlan`. The mutation plan is not the universal planner.

---

## 15. Domain-Neutral (§16)

Continuation reasons over `problem state / evidence / step outcome / scope / dependencies / verification / freshness / progress`. No `ReactContinuation` / `BackendContinuation` / `GoContinuation` / `StaticWebContinuation` exists; domain reasoning lives in adapters/evidence providers (`internal/adapters/web`, future perf/Go adapters) that produce evidence, not continuation types.

---

## 16. Architecture Dependency Rules (§17)

Preserved direction (core never imports adapter):

```text
Adapters (adapters/web)
  ↓ (uses)
Understanding / ProblemSurface (domain-neutral evidence)
  ↓
Problem Solving (problem.ProblemSolvingPlan)
  ↓
Mutation Planning (mutationstrategy.MutationPlan)
  ↓
Authorization Boundary (core/authorization)
  ↓
Scheduler (runtime/scheduler)
  ↓
Execution (execution + runtime/substrate)
  ↓
Observation / Evidence (events.DomainEvent)
  ↓
Continuation Proposal (internal/continuation — pure, proposal-only)
```

Enforced: `continuation` ∤→ `runtime/executor`, `runtime/scheduler`, `execution`, `core/domain/authorization`, `ui`/`tui`, `provider`/`providers`/`ai`, `adapters/web`; `core` ∤→ `adapters/web` (§19 architecture tests). No circular dependency; no second runtime/scheduler.

---

## 17. Integration Requirement (§20)

One minimal, observable path is implemented and tested (`internal/runtime/scheduler/continuation_bridge.go` + `continuation_integration_test.go`):

```text
Task (ProblemSolvingPlan via problem.Derive)
  → bounded ExecutionStep (scheduler.Schedule)
  → existing StepScheduler (AcceptStep + RunNext staging)
  → existing execution (ProposalStagingBuffer → CommitGate)
  → outcome/evidence (StepResult, Observation)
  → continuation.DeriveNextStep (pure)
  → next bounded StepProposal
  → ContinuationToTaskSpec → existing scheduler.Schedule (next step)
```

The bridge `ContinuationToTaskSpec` is a thin adapter: it preserves the durable `ActiveTargetScope` as the envelope, carries authoritative evidence into `LatestEvidence`, and never creates a second scheduler, executor, or grant. The model is:

```text
LLM = bounded reasoning worker
ProblemSolvingPlan = task-level structure
Continuation = derives next bounded proposal from truthful state
Authorization = decides permission
StepScheduler = admits bounded progression
Execution = performs authorized actions
Observation = records what happened
State = determines what is true now

LLM proposes → Continuation derives → Authorization permits → Scheduler admits → Execution acts → Observation records → State changes → Next bounded decision
```

Never collapsed.

---

## 18. Explicit Non-Goals (§21)

Not implemented in this phase: autonomous agent loop, unrestricted self-directed execution, new scheduler/executor, new authorization, permission escalation, provider switching/ranking, model picker redesign, capability/sandbox redesign, headless/TUI rewrite, domain-specific runtime, long-term memory, transcript persistence as authority, speculative multi-step planning. Provider/model ranking and model picker remain untouched.

---

## 19. Completion Gate (§23)

All 23 gates are satisfied and machine-verified:

| Gate | How |
|---|---|
| Large task progresses through multiple bounded steps | `CONT-11` multi-step simulation (INVESTIGATE→…→OBSERVE) |
| One model output ≠ whole task | `CONT-02`, `CONT-12` — partial → CONTINUE, task persists |
| Partial can continue from durable state | `CONT-02`, integration test `length → DeriveNextStep → new proposal` |
| Continuation cannot authorize | `CONT-04`, `TestContinuationHasNoAuthorizationFields`, `TestContinuationMustNotImportForbidden` |
| Cannot execute | `CONT-07`, `TestContinuationMustNotContainExecutionPrimitives` |
| Cannot schedule | `CONT-08`, absence of `Schedule`/`StepScheduler` in continuation |
| Cannot expand scope | `CONT-05` — exceeding `$hot` → `AWAITING_APPROVAL` |
| Existing scheduler authoritative | `TestNoSecondSchedulerOrExecutor`, bridge reuses `Schedule` |
| Existing execution authoritative | `TestNoSecondSchedulerOrExecutor`, substrate remains sole `os/exec`/`WriteFile` owner |
| Authorization unchanged | `TestContinuationMustNotImportForbidden` (no auth import), `TestGEN09` still green |
| `$prompt` unchanged | `CONT-05`/`CONT-06` — no capability creation, read-only surfaces still read-only |
| `$hot` unchanged | `CONT-05` — envelope never expanded silently |
| OCC/freshness enforced | `CONT-09`, `Stale` on drift |
| No-progress terminates | `CONT-10` + `PostStepEvaluation` / `ErrRecoveryHalted` |
| Transcript not durable truth | `CONT-13`, `CONT-14` — decision from fingerprints/verification, model claim ignored |
| Observation/evidence drives state | `CONT-14`, `Kind` hierarchy, `filterAuthoritative` |
| ProblemSolvingPlan domain-neutral | `CONT-18`, `GEN-08` still green |
| MutationPlan mutation-specific | §15 boundary preserved; `ProblemSolvingPlan ≠ MutationPlan` |
| No static-web leakage | `TestCoreMustNotImportWebAdapterContinuation`, `GEN-01`…`GEN-12` green |
| No second runtime | `TestNoSecondSchedulerOrExecutor` |
| No second scheduler | same |
| No second executor | same |
| `go test ./...` | `ok` (see audit) |
| `go vet ./...` | 0 issues |
| `golangci-lint` | 0 issues in touched packages |

---

*One runtime. Many reasoning strategies. Bounded steps. No second authority. Human authorization remains the root of mutation authority.*

