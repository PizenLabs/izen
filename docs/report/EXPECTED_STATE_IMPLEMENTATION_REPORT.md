# IZEN — Expected-State Implementation Report

**Scope:** Move IZEN from its current maturity state toward the expected state
described in the implementation directive — without rewriting the kernel,
weakening mutation authority, or building benchmark-specific paths.  
**Governing principle:** *Dynamic Path, Static Authority, Truthful State
Transition.*  
**Model under test (real-world validation):** `qwen2.5-coder:7b` via a local
Ollama at `http://127.0.0.1:11434/v1`.

---

## 0. Verdict up front

Two coherent architectural changes were made, both derived from repository
evidence and prior forensic reports:

1. **Evidence becomes first-class runtime state for a targetless
   investigation.** A read-only investigation whose target is legitimately
   unknown now performs bounded repository observation, admits that observed
   material as read-only context, records it as authoritative **evidence**, and
   is allowed to reach `PROVEN` on that evidence — while remaining structurally
   incapable of authorizing a mutation.
2. **UI completion is gated on the authoritative objective verdict.** The
   projection consumes `objective.evaluated`, so `UI.Completed ⇔
   ObjectiveState == PROVEN` is structural, not accidental.

A third, adjacent control-plane defect was fixed because the new capability
made it reachable: the dispatch contract guard re-read a **negated** change verb
(`"Do not modify anything"`) as a mutation request and forced an authority
ceiling violation. The read-only constraint now travels with the request as a
lifecycle fact.

A latent forensics truth defect was also fixed: a legitimate completed
read-only investigation was being flagged `PREMATURE_TERMINATION` because the
pattern read a summary scalar that older runtimes leave at zero.

**Real-world result:** with a real local model, `INVESTIGATE` and `PERFORMANCE
INVESTIGATION` (both targetless, both explicitly read-only) now reach
`PROVEN` with **zero mutations** and no false patterns. `CREATE` still reaches
`PROVEN` and writes a real file. `MODIFY` still parks at a resumable
clarification. `FIX` (targetless) remains a recorded limitation.

```
go build ./...                PASS
gofmt -l ./internal ./test    clean
go vet ./...                  PASS
go test -count=1 ./...        PASS (0 failures)
```

---

## 1. Current architecture discovered from the repository

The runtime had already passed substantial correctness work (R1–R7). The
production path was reconstructed from real call sites and the prior reports
(`R7_RUNTIME_CLOSURE_REPORT.md`, `REAL_WORLD_AGENT_VALIDATION_REPORT.md`):

```text
cmd/izen/main.go
 └─ compose.Wire                              internal/runtime/compose/compose.go
     ├─ RuntimeExecutor                       (single execution/mutation authority)
     ├─ ExecutorAdapter                       (sole loop→execution surface)
     ├─ autonomy.Driver (app.Autonomous)      (single loop owner)
     └─ durable.TaskStore                     (ledger)
 └─ ui.RunMainDashboardWithApp                internal/ui/program.go
     └─ "$prompt" → routePromptDirective      ui/intent_dispatch.go
        └─ runAutonomyRoutedCmdExplicit       ui/autonomy_route.go
           └─ executeAutonomyWorkspace
              └─ Driver.Run                    internal/runtime/autonomy/driver.go
                 └─ observeAndRun
```

Authority owners are unchanged and were not touched:

| Authority | Owner |
|---|---|
| Objective/scope resolution | `Driver.bindAuthoritativeTargets`, `ExecutorAdapter.ResolveScoped` |
| Discovery (evidence only) | `execution.DeriveScope`, `execution.WorkspaceDiscovery.Discover` |
| Admission (`I12`/`I13`) | `EvaluatePreflightAdmission` (`preflight_admission.go`) |
| Context compilation | `contextcompiler.Compiler` |
| Mutation authority | `PatchManager.ApplyContext` via `RuntimeExecutor.Approve` |
| Verification | `Verifier.RunAllFor` |
| Completion authority | `ObjectiveCompletionAuthority.Evaluate` (`objective_authority.go`) |
| UI projection | `presentation.ExecutionProjection` (`execution_projection.go`) |

The kernel was **not** modified.

### The three relevant seams discovered

1. **Objective authority** (`internal/execution/objective_authority.go`). A
   read/review contract required `WorkspaceObservations >= 1`, and that counter
   only counted **declared target** reads (`countWorkspaceObservations`). A
   targetless investigation has no declared target, so it could never satisfy
   the observation obligation (recorded defect **D5** in the prior report).
2. **Read-only execution** (`RuntimeExecutor.Execute`, read-only branch). For a
   repository-policy strategy with no declared target, the compiled prompt
   carried the user request only — the model answered from priors, and the
   runtime recorded no observation at all.
3. **UI projection** (`presentation.ExecutionProjection`). The projection did
   **not** subscribe to or consume `objective.evaluated`; its terminal state was
   derived from `execution.finished` and the execution-evidence gate alone
   (recorded defects **E1/E2**). A read-only run that terminated cleanly was
   rendered `Completed` even when the objective was never proven.

---

## 2. Gaps identified against the directive

| Directive expectation | Repository reality | Gap |
|---|---|---|
| §5 Investigation must not require a mutation target | Read-only intents were admitted without a target, but a targetless read-only objective could never reach a truthful proven terminal state | **Targetless investigation could not complete** |
| §6 Evidence is first-class runtime state | Observation evidence existed only for declared targets | **Repository observation was not representable** |
| §7 Actions must increase authoritative information | Targetless read-only invocation passed **no repository material** to the model | **Investigation answered from priors** |
| §8 Investigation as a real capability | `RepositoryInvestigation` strategy existed but produced no evidence | **Discovery did not become evidence** |
| §11 `UI.Completed ⇔ ObjectiveState == PROVEN` | `objective.evaluated` was routed to Trace only; completion was computed from a weaker gate | **UI could show Completed without PROVEN** |
| §9 Bug fixing builds on investigation | A targetless “find and fix” dead-ends in a non-resumable `inform` park (recorded **D3**) | **Remains a limitation (see §7)** |

---

## 3. Architectural changes made

### Change 1 — Repository observation as first-class evidence

- `ExecutionProof.RepositoryObservations int` (`internal/execution/executor.go`).
- `RuntimeExecutor.investigationContextTargets` performs bounded, deterministic
  discovery for a **repository-policy** strategy with no declared target,
  reads each candidate through the existing snapshot cache, and returns up to
  `maxInvestigationContextFiles` (24) observed paths. The paths are then passed
  as read-only context to the existing `invokeReadOnly` compile path.
- `ObjectiveEvidence.RepositoryObservations int`
  (`internal/execution/objective_authority.go`), populated from the proof in
  `ObjectiveEvidenceFromResult`.
- A single predicate, `observedWorkspace`, now decides the READ/REVIEW
  observation obligation from **either** a declared-target read **or** repository
  observation. `evaluateRead`, `evaluateReview`, `unsatisfiedClause` and the
  condition reducer (`objective_conditions.go`) all read the same predicate.

```text
bounded discovery (authoritative read)
        ↓
observed repository files admitted as READ-ONLY context
        ↓
model reasons from real workspace material
        ↓
RepositoryObservations recorded as EVIDENCE
        ↓
READ/REVIEW observation obligation satisfied
        ↓
PROVEN — with zero mutation authority created
```

### Change 2 — UI completion gated on the authoritative verdict

- `events.EventObjectiveEvaluated` added to `projectedEventTypes()`
  (`internal/ui/program.go`), so the projection actually receives it.
- `ExecutionProjection` records an `objectiveVerdict` from
  `ObjectiveEvaluatedPayload` and re-derives a provisional terminal state when
  the verdict arrives (it is published **after** `execution.finished`).
- `terminalState` now requires, when an objective verdict was observed, that it
  be `PROVEN && Granted` for `PhaseCompleted`; `FAILED` → failed; everything
  else → `PhaseUnsubstantiated` carrying the runtime's own reason.
- The raw verdict is additionally written to the Trace overlay
  (`internal/ui/model.go`).

### Change 3 — The read-only constraint is a lifecycle fact

- `LoopRequest.ReadOnly bool` (`internal/autonomy/runtime_loop.go`), set once in
  `Driver.Run` from the single constraint authority
  (`ExecutorAdapter.ReadOnlyConstraintStated` → `IntentGateway`, preserving the
  semantic-boundary lock).
- `ValidateDispatchContract` and `normalizedRequestContract`
  (`internal/runtime/autonomy/contract_guard.go`) no longer force a mutation
  obligation from a negated change verb when the human declared read-only.

### Change 4 — Forensics truth for completed read-only runs

- `PatternPrematureTermination` now reads the reconstructed trace's own model
  invocation list as well as the summary scalar
  (`internal/forensics/trace.go`).

---

## 4. Why each change was necessary

- **Evidence first-class (1).** The directive’s central distinction is KNOWN
  TARGET vs TARGET DISCOVERY. `WorkspaceObservations` was a *declared-target*
  count; reusing it for repository material would collapse two different states.
  A dedicated `RepositoryObservations` field keeps the distinction explicit and
  lets `observedWorkspace` be the one place the observation obligation is
  computed. Because the field is consulted only by the READ/REVIEW clauses, it
  can never satisfy a mutation clause — which is the directive’s “discovery
  produces evidence, not authority” made structural.
- **Real repository context (1).** Recording an observation while still sending
  the model nothing would be theatre. The directive demands that actions
  *increase authoritative information*. Admitting bounded observed files into
  the read-only prompt makes the investigation evidence-grounded, which the
  acceptance test asserts directly.
- **UI truth (2).** `presentation.ExecutionProjection` is the only path the TUI
  has to execution truth. If it never observes `objective.evaluated`, no amount
  of downstream correctness can make `Completed` mean `PROVEN`. Gating the
  terminal state on the verdict is the minimum coherent fix, and the late
  arrival is handled explicitly rather than assumed away.
- **Read-only constraint at dispatch (3).** The strategy gateway already
  downgraded the run to `RepositoryInvestigation`, but the dispatch guard
  independently rescanned the raw text and read `"Do not modify anything"` as a
  mutation. Two authorities disagreeing about the same request is exactly the
  split-brain class the codebase elsewhere calls out. Threading one derived
  boolean fixes it without adding a second caller of the locked predicate.
- **Forensics truth (4).** The new capability surfaced a latent lie: a
  completed, evidenced, read-only investigation was reported as a premature
  termination. The pattern read a summary scalar that is never populated for
  these runs; the reconstructed trace already held the truth.

---

## 5. Tests added / changed

### Added

| Test | Layer | What it pins |
|---|---|---|
| `TestTargetlessInvestigation_ProvesFromRepositoryEvidence` (`internal/runtime/autonomy`) | Integration (real Driver + RuntimeExecutor) | targetless read-only objective observes the repository, is given the observed file, reaches `PROVEN`, zero mutations, `RepositoryObservations ≥ 1` |
| `TestTargetlessInvestigation_NegatedConstraintStillProves` | Integration | a negated change verb (`"Do not modify anything"`) does not force a mutation contract; the run completes read-only |
| `TestRepositoryObservation_ProvesReadObjective` (`internal/execution`) | Unit / invariant | READ/REVIEW observation is satisfied by either declared-target reads or repository observation; a blind answer is `UNSUBSTANTIATED` |
| `TestRepositoryObservation_NeverAuthorizesMutation` | Unit / invariant | a rich repository observation never satisfies a PATCH contract without a durable mutation |
| `TestProjection_TerminalIsProvisionalUntilObjectiveVerdict` (`internal/presentation`) | Unit / invariant | a late `UNSUBSTANTIATED` verdict downgrades a provisional `Completed` |
| `TestProjection_CompletedRequiresProvenVerdict` | Unit / invariant | `PROVEN` keeps `Completed` |
| `TestProjection_VerdictBeforeFinishedAlsoGates` | Unit / invariant | a verdict arriving before `execution.finished` also gates |
| `TestProjection_IdleAcceptsUntaggedVerdict` | Unit / invariant | an untagged verdict is not silently dropped |

### Changed

| Test | Change |
|---|---|
| `TestProjectedEventTypesCoverEveryEventTheReducerRequires` (`internal/ui`) | now also requires `EventObjectiveEvaluated`, so the subscription can never silently regress |
| `TestKernelLock_NoUnregisteredWorkspaceExistenceDecision` (`test/architecture`) | line-number allowlist updated for the two shifted `os.Stat` sites (no new existence decision was introduced) |

No test’s intent was weakened; no test was deleted.

---

## 6. Real-world validation results

Harness: `test/realworld/` (production composition, real Ollama
`qwen2.5-coder:7b`), opt-in via `IZEN_LIVE_FORENSICS=1`. Raw evidence:
`/…/izen_rw` (this run) and the existing `docs/report/realworld/evidence/`.

| Task | Prompt (verbatim) | Target known? | Mutation allowed? | Result | Mutations |
|---|---|---|---|---|---|
| A CREATE | `create new file named testing.md` | no | yes | **PROVEN** — `testing.md` created | 1 applied |
| B MODIFY | `find the incorrect greeting in this project and fix it` | no | yes | **awaiting_human** (clarify, resumable, 3 candidates) | 0 |
| C INVESTIGATE | `investigate why this application returns an error from the user endpoint. Do not modify anything.` | **no** | **no** | **PROVEN** (read-only) | **0** |
| D FIX | `find and fix the bug causing the user endpoint to fail` | no | yes | **awaiting_human** (inform, non-resumable) | 0 |
| E PERFORMANCE | `find the main performance bottleneck in this project. Do not modify anything.` | **no** | **no** | **PROVEN** (read-only) | **0** |

Raw excerpts:

```text
═══ C-investigate ═══
state:      completed
termination:completed reason="objective satisfied: completed; objective PROVEN by evidence"
model:      calls=2 failures=0 output_exhausted=0 continuations=1 tokens=360/841 known=true
mutations:  []
verify:     [applicable=false passed=false reason="read-only execution"]
objective:  [state=PROVEN granted=true reason=""]
patterns:   []
delta:      []
```

```text
═══ E-performance ═══
state:      completed
termination:completed reason="objective satisfied: completed; objective PROVEN by evidence"
model:      calls=2 failures=0 output_exhausted=0 continuations=1 tokens=286/679 known=true
mutations:  []
objective:  [state=PROVEN granted=true reason=""]
patterns:   []
delta:      []
```

```text
═══ A-create ═══
state:      completed
mutations:  [target=testing.md outcome=changed applied=true fs_changed=true]
verify:     [applicable=false passed=false reason="no verification configured for language "]
objective:  [state=PROVEN granted=true reason=""]
delta:      [ADDED   testing.md]
```

The C and E arms prove the directive’s Level‑3 requirement: a targetless,
read-only, real-world task completes truthfully on real evidence with a real
local model, and **no file is touched**.

---

## 7. Remaining limitations

1. **Targetless FIX remains a non-resumable `inform` park (D3).**  
   *First incorrect transition:* `Driver.syncCanonicalIntent`
   (`internal/runtime/autonomy/objective_completion.go`) elevates a
   read-only-classified objective (`debugging`) to `modification` because the
   gateway selected `MultiFilePlanning` (a mutation-semantics proposal), then
   re-compiles workspace context over an **empty** target set;
   `RecompileIntentContext` fails closed and the run parks non-resumably.  
   *Why not fixed here:* the correct remedy (route the targetless mutation to a
   resumable clarification and re-derive the canonical contract on resume) is a
   lifecycle change across `syncCanonicalIntent`, `ResumeClarify` and
   `taskContract`. A half-fix would make a resumed mutation objective derive a
   **read** contract (a false-proof hazard), which is worse than the current
   truthful park. Recorded honestly rather than rushed.
2. **Targetless MODIFY still parks at a resumable clarification (D4, by
   design/I13).** Discovery cannot silently supply mutation authority, so the
   human must name the target. This is correctness, not a defect.
3. **Hypothesis handling is not yet a first-class runtime state.** The directive
   names `HYPOTHESIZED` as a distinct state; the runtime today produces
   observation evidence + a model response, but does not represent an explicit,
   evidence-linked hypothesis object in the Control Plane. This is the largest
   remaining architectural gap relative to the directive.
4. **Repository context is capped at 24 files** (`maxInvestigationContextFiles`).
   A truncated observation set is still an observation, and it is recorded as a
   count, but very large repositories are only partially observed in one turn.
5. **Dependency/call-graph exploration is not yet an investigation action.**
   Discovery observes paths and manifests; it does not yet traverse symbol
   dependencies or route tables as distinct evidence kinds.
6. **`ExecutionSummaryPayload` model-call counters remain unpopulated**; the
   trace/render compensate from the reconstructed event stream. The false
   `PREMATURE_TERMINATION` was fixed at the pattern, not by populating the
   summary scalar.

None of these can produce an unauthorized mutation, a false `PROVEN`, a false
verification, or a false progress signal.

---

## 8. Evidence that mutation authority was not weakened

The repository’s existing authority locks remain in force and the full suite is
green. The new capability is confined to the READ/REVIEW half of the contract:

- **`TestRepositoryObservation_NeverAuthorizesMutation`** — a PATCH contract
  with `RepositoryObservations = 12`, `WorkspaceObservations = 12`,
  `Artifact = NONE`, `Mutation = NONE` remains **not** `PROVEN`. Repository
  observation cannot stand in for a durable mutation.
- **`TestTargetlessInvestigation_*`** — the read-only investigation parks at no
  mutation boundary, and `note.txt` is byte-identical afterwards.
- **Real-world C/E** — `mutations: []`, `delta: []`, `verify: read-only
  execution`.
- **Real-world A** — the mutation path is unchanged: the CREATE still crosses
  `Approve → MutationBoundary`, is independently observed on disk, and reaches
  `PROVEN` only with `applied=true fs_changed=true`.
- **Architecture locks** — `TestSemanticLock_ReadOnlyConstraintHasExactlyOneNonTestCaller`
  still passes: the new `LoopRequest.ReadOnly` fact is derived from the single
  gateway call, not a second consumer of the locked predicate.
- **Kernel lock** — `TestKernelLock_NoUnregisteredWorkspaceExistenceDecision`
  passes with only the line-number allowlist updated.

No authorization boundary, approval gate, capability grant, or mutation
transaction was modified.

---

## 9. Evidence that targetless investigation works

- `TestTargetlessInvestigation_ProvesFromRepositoryEvidence` drives the real
  `Driver` + `RuntimeExecutor` over a real workspace with an objective that
  names **no target**, and asserts: `RuntimeCompleted`, objective `PROVEN`,
  `TaskRead`/`TaskReview` contract, `RepositoryObservations ≥ 1`, no boundary,
  unchanged bytes, **and** that the observed file appears in the provider
  request actually sent.
- `TestTargetlessInvestigation_NegatedConstraintStillProves` adds the
  `"Do not modify anything"` constraint, exercising the dispatch fix.
- Real-world **C** and **E** are targetless, read-only, and reach `PROVEN` with
  the real model — the exact acceptance probe named in the directive (§8),
  generalized (no `/users` hard-coding; both an endpoint-error and a
  performance-bottleneck objective).

The flow implemented:

```text
objective (target = unknown, mutation = forbidden)
        ↓
bounded repository discovery            (authoritative observation)
        ↓
observed files admitted as READ-ONLY context
        ↓
model reasons from repository evidence
        ↓
RepositoryObservations recorded as EVIDENCE
        ↓
READ/REVIEW observation obligation satisfied
        ↓
PROVEN  (no target bound, no mutation authority created)
```

---

## 10. Evidence that UI completion corresponds to authoritative PROVEN

- `events.EventObjectiveEvaluated` is now in `projectedEventTypes()`, pinned by
  `TestProjectedEventTypesCoverEveryEventTheReducerRequires` — the projection
  cannot lose the verdict through a subscription gap.
- `ExecutionProjection` records the verdict and re-derives terminal state:
  - `TestProjection_TerminalIsProvisionalUntilObjectiveVerdict`: a
    `success=true` execution is provisionally `Completed`, then a late
    `UNSUBSTANTIATED` verdict flips it to `PhaseUnsubstantiated`.
  - `TestProjection_CompletedRequiresProvenVerdict`: `PROVEN && Granted` is the
    only path that keeps `Completed`.
  - `TestProjection_VerdictBeforeFinishedAlsoGates` and
    `TestProjection_IdleAcceptsUntaggedVerdict` cover ordering and untagged
    verdicts.
- The real-world harness enforces the same invariant end-to-end: it flags
  `terminal state COMPLETED with no PROVEN objective event` as a violation. No
  arm produced that note; C/E produced `objective: [state=PROVEN granted=true]`.

```text
UI.Completed  ⇔  presentation.PhaseCompleted
             ⇔  observed objective.evaluated (Granted && State == PROVEN)
             ⇔  runtime ObjectiveState == PROVEN
```

For non-autonomy execution paths that never publish a verdict, the projection
falls back to the pre-existing execution-evidence gate — so no existing surface
regresses.

---

## 11. Files changed

### Production (9)

| File | Change |
|---|---|
| `internal/execution/executor.go` | `RepositoryObservations` proof field; `investigationContextTargets`; read-only context admission |
| `internal/execution/objective_authority.go` | `ObjectiveEvidence.RepositoryObservations`; `observedWorkspace`; READ/REVIEW clauses |
| `internal/execution/objective_conditions.go` | condition reducer counts repository observation |
| `internal/autonomy/runtime_loop.go` | `LoopRequest.ReadOnly` |
| `internal/runtime/autonomy/driver.go` | set `ReadOnly` from the single constraint authority |
| `internal/runtime/autonomy/contract_guard.go` | honour the read-only constraint at dispatch |
| `internal/presentation/execution_projection.go` | consume `objective.evaluated`; gate terminal state |
| `internal/ui/program.go` | subscribe to `objective.evaluated` |
| `internal/ui/model.go` | project the verdict to Trace |
| `internal/forensics/trace.go` | truthful `PREMATURE_TERMINATION` predicate |

### Tests (4 new + 2 changed)

`internal/execution/repository_observation_test.go`,
`internal/presentation/objective_verdict_test.go`,
`internal/runtime/autonomy/targetless_investigation_test.go`,
`internal/ui/runtime_truth_test.go` (extended),
`test/architecture/kernel_lock_test.go` (allowlist shift only).

---

## 12. Final statement

IZEN can now perform a **targetless, read-only investigation** that observes the
real repository, grounds the model in that observation, records it as
first-class evidence, and reaches a truthful `PROVEN` terminal state — while
remaining structurally incapable of granting itself mutation authority. The UI’s
completed state is now a projection of the authoritative objective verdict
rather than of a weaker execution gate.

The directive’s remaining high-value gaps — explicit hypothesis state,
evidence-linked diagnosis, and a targetless FIX flow that does not dead-end —
are recorded honestly in §7 with their owners and first incorrect transitions.
They were **not** papered over, and the kernel, authorization boundary, and
mutation transaction were not touched.
