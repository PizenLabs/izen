# R7 — Agent Runtime Closure & Termination Audit

**Branch:** `fix/runtime` (HEAD `9686c1e`, base `f40f369`)
**Date:** 2026-10-06
**Question answered:** *Is IZEN now a truthful, bounded, general-purpose Agent
Runtime & Smart Harness whose execution contract is strong enough to freeze?*

R7 is a **closure audit**. It added **no production code** and proposes **no new
phase**. It reconstructed the production runtime, re-ran the deterministic
acceptance matrix, verified that the R1–R6 guarantees compose, classified every
discovered limitation, and issued one termination decision.

---

## 1. Executive verdict

**IZEN's runtime contract is proven, bounded, observable and truthful enough to
freeze.** Every authority boundary named in the mission is explicit and enforced
in the real production path; every invariant either holds with deterministic
evidence or is recorded as a non-blocking limitation that cannot produce
unauthorized mutation, false `PROVEN`, false progress, false verification,
duplicate mutation, cross-execution contamination, unbounded continuation,
incorrect cancellation, incorrect durable mutation truth, or loss of human
authority.

The model proposes. The runtime decides. Capabilities are gated. Evidence is
truth. The human authorizes what requires authorization. IZEN is not optimized
around web, a provider, a model, or a tool protocol: the runtime primitives are
domain-independent and the removed web-specific mappings are documented in
`CAPABILITY_REALITY_AUDIT.md` §H.4.

**Termination decision: `FREEZE`** (§11).

---

## 2. Actual production execution graph

Every arrow below is a real call site on the `$prompt` path, or a real
construction site in the composition root. Nothing is inferred from a package
name.

```text
USER INTENT
  ui.handleInput
  → parser.Parse (user text only)
  → ui.dispatchDirectives
  → ui.routePromptDirective                          internal/ui/intent_dispatch.go:287
  → ui.runAutonomyRoutedCmdExplicit                  internal/ui/autonomy_route.go:56
  → autonomy.Engine.Decide(objective)                internal/runtime/autonomy/engine.go
  → ui.executeAutonomyWorkspace                      internal/ui/autonomy_route.go:163
  → ui.executeAutonomyViaDriver                      internal/ui/autonomous.go:43
  → ui.runAutonomousDriver                           internal/ui/autonomous.go:127
  → autonomy.Driver.Run                              internal/runtime/autonomy/driver.go:587
      ├─ bindAuthoritativeTargets                    driver.go:699 / :1636
      ├─ deriveEvidenceScope                         driver.go:1805
      ├─ selectInteractionContract                   driver.go:1395
      └─ observeAndRun                               driver.go:2140
           ├─ syncGrantedWorkspaceContext            driver.go:2153   (fail-closed)
           ├─ preflightBarrier.Wait                  driver.go:2160
           ├─ loop.Observe → deciding                driver.go:2185
           ├─ EvaluatePreflightAdmission             driver.go:2200   (I12 / I13)
           └─ for !loop.State().IsTerminal()         driver.go:2250
                ├─ cancellation gate                 driver.go:2264
                ├─ RuntimeDeciding/Interpreting
                │    ├─ authorizeObjectiveCompletion  driver.go:2293
                │    ├─ routeObjectiveContinuation    driver.go:2305
                │    ├─ authorizeBehavioralCompletion driver.go:2320
                │    ├─ authorizeContractRecovery     driver.go:2329
                │    └─ step(decision)                driver.go:2365
                ├─ RuntimeExecuting
                │    ├─ ValidateDispatchContract      driver.go:2376
                │    ├─ ledgerMutationPrepared        driver.go:2387
                │    ├─ adapter.Execute → RuntimeExecutor.Execute  executor.go:1586
                │    │     ├─ admission / scope / grant / contract
                │    │     ├─ context compile (frozen, SHA-256 sealed)
                │    │     ├─ provider.Execute / ExecuteStream
                │    │     ├─ artifact parse → pending patch
                │    │     ├─ Approve → MutationBoundary → OCC     executor.go:2418
                │    │     ├─ verifier.AuditObjective / RunAllFor  verify.go:423
                │    │     └─ seal ExecutionEvidence → ExecutionProof
                │    └─ ledgerMutationCommitted       driver.go:2405
                ├─ RuntimeRecovering
                │    └─ typedRepair → LoopContinue     driver.go:2522
                └─ RuntimeAwaitingHuman               driver.go:2517 / :2235
CONTINUATION / COMPLETION
  objective.evaluated (PROVEN|UNSUBSTANTIATED|FAILED|REQUIRES_AUTHORIZATION)
```

Entry point: `cmd/izen/main.go → compose.Wire` (`compose.go:500`), the sole
dependency-graph assembly point. `app.Autonomous` (`compose.go:937`) is injected
into the TUI at `internal/ui/program.go:204`. There is exactly **one** loop
owner, **one** execution/mutation authority, and **one** completion authority.

---

## 3. Authority map

For each transition: who owns the decision, what is authoritative, what is
untrusted, what is prevented.

| Transition | Decision owner | Authoritative input | Untrusted input | What is prevented |
|---|---|---|---|---|
| objective → scope | `IntentGateway` / `DeriveScope` | stated target, declared artifact kind | model text | target invention |
| scope → admission | `EvaluatePreflightAdmission` (`preflight_admission.go:307`) | proven target, boundary, evidence | discovery candidates | I12 no-evidence-no-provider; I13 evidence ≠ authority |
| model invocation | `RuntimeExecutor.Execute` (`executor.go:1586`) admission | admission capability vector, frozen context | model output | unauthorized dispatch |
| proposal → mutation | `MutationBoundary` + `Approve` (`executor.go:2418`) | human authorization token | model artifact | unauthorized write |
| mutation → verification | `Verifier.runStep` (`verify.go:482`) | post-effect process result | — | verification absence read as pass |
| evidence → completion | `ObjectiveCompletionAuthority.Evaluate` (`objective_authority.go:727`) | sealed `ObjectiveEvidence` | model claim | activity/mutation read as completion |
| proposal completion → terminal | `authorizeObjectiveCompletion` (`objective_completion.go:190`) | same evidence bundle | proposed decision | false `PROVEN` |
| continuation | `routeObjectiveContinuation` + R5.1 fingerprint | progress delta since prior evaluation | step count, tokens | stall → unbounded continuation |
| cancellation | `Driver.runCtx` (`driver.go:607`) + inter-step gate (`:2264`) | withdrawn context | late provider/stream/capability results | late mutation / resurrection |
| crash reconciliation | `durable.InspectCursors` (`store.go:811`, read-only) | durable cursor + digest | — | fabricated resume / duplicate mutation |
| human gate | UI boundary → `AuthorizationEngine` | reviewed candidate + digest | model proposal | approval of an unreviewed or changed change |

The core rule holds in code: **LLM output = untrusted proposal; runtime =
authority; capabilities = controlled effects; evidence = truth; human = final
authority where authorization is required.**

---

## 4. Capability reality matrix

The six-state test (`IMPLEMENTED → REACHABLE → AUTHORIZED → EXECUTED →
EVIDENCED → VERIFIED`) applied to the runtime's important capabilities. A
capability is production-only if it is reachable through the composition root,
not merely implemented.

| Capability | Implemented | Reachable (production path) | Authorized | Executed | Evidenced | Verified | Class |
|---|---:|---:|---:|---:|---:|---:|---|
| objective/scope resolution | ✓ | ✓ `Driver.Run` | ✓ gateway | ✓ | ✓ events | ✓ | **PRODUCTION** |
| read/list/search/symbol (model tools) | ✓ | ✓ contract-bearing lane → `CapabilityToolRunner` | ✓ admission vector | ✓ | ✓ `capability.execute` | ✓ | **PRODUCTION** |
| artifact generation | ✓ | ✓ `invokeMutation` | ✓ | ✓ | ✓ `ExecutionProof` | ✓ | **PRODUCTION** |
| file mutation | ✓ | ✓ `Approve → MutationBoundary` | ✓ human + authorization | ✓ | ✓ `mutation.completed` | ✓ | **PRODUCTION** |
| verification | ✓ | ✓ `verifier.*` | ✓ | ✓ | ✓ sealed report | ✓ | **PRODUCTION** |
| objective completion | ✓ | ✓ `authorizeObjectiveCompletion` | ✓ authority | ✓ | ✓ `objective.evaluated` | ✓ | **PRODUCTION** |
| bounded continuation | ✓ | ✓ `artifact_step` + driver recovery | ✓ bounds | ✓ | ✓ `continuation.evaluated/selected` | ✓ | **PRODUCTION** |
| cancellation | ✓ | ✓ `runCtx` + `terminateAbort` | ✓ | ✓ | ✓ `execution.finished(outcome=cancelled)` | ✓ | **PRODUCTION** |
| durable commit / reconciliation | ✓ | ✓ `ledgerMutationPrepared/Committed` + `ReconcileInterrupted` | ✓ (commit) / read-only (inspect) | ✓ | ✓ `EXECUTION_COMMITTED` | ✓ | **PRODUCTION** |
| behavioural observation/repair | ✓ | ~ only when `BehaviorRequired` heuristic triggers | ✓ capability grant | ✓ | ✓ `execution.behavior.observed` | ✓ | **PRODUCTION (gated)** |
| process/network capabilities (`runtime.serve/fetch/inspect`, `command.run`) | ✓ | behind behavioural stage only | ✓ | ✓ | ✓ | ✓ | **PRODUCTION (behavioural only)** |
| `ToolCallBuffer.ApplyApproved` | ✓ | — nothing populates the buffer | n/a | — | — | — | **UNREACHABLE / inert** |
| `patch.Engine.Apply` | ✓ | — zero call sites | — | — | — | — | **UNREACHABLE** |
| second `runtime/executor.RuntimeExecutor` | ✓ | — zero production callers | — | — | — | — | **UNREACHABLE** |
| `internal/domain/capability` portfolio gate | ✓ | `izen run` only, not `$prompt` | n/a | — | — | — | **LEGACY (separate entry)** |
| live-model benchmarks (R1–R6) | ✓ | opt-in (`IZEN_LIVE_FORENSICS=1`) | n/a | ✓ (recorded) | ✓ | ✓ | **TEST/evidence** |

**No capability is `MISSING`.** The `UNREACHABLE` rows are unreachable by
design or genuinely inert and are non-load-bearing on the `$prompt` contract.
The `ToolCallBuffer` path, even if armed, now routes through the Runtime Kernel
and authorization rather than the historical bare `os.WriteFile`.

---

## 5. Runtime invariant results

### Authority
- LLM output cannot directly authorize mutation — **holds**
  (`TestPhase1_ModelOutputCannotAuthorize`,
  `TestCapabilityToolRunnerRefusesUngrantedCapability`).
- Runtime policy determines capabilities — **holds**
  (`SetAdmittedCapabilities`, `CapabilityGrantFor`; `TestPhase1_*`).
- Human approval remains authoritative where required — **holds**
  (`TestPhase0MutationsHeldBehindRuntimeApprovalBoundary`).
- Dynamic paths cannot expand static authority — **holds**
  (`TestInvariant_StrategyProjectionCannotWidenAuthority`,
  `TestPhase1NoImplicitScopeEscalation`).
- A discovered candidate cannot silently become a mutation target — **holds**
  (`TestR3_DiscoveryEvidenceCannotDirectlyAuthorizeMutation`).

### Mutation
- Unauthorized mutation cannot reach the boundary — **holds**
  (`TestR6C_AuthorizationFailureBeforeMutationLeavesZeroDelta`).
- Mutation is transactional / failure-contained — **holds**
  (`TestTruthMatrix_MultiFilePartialFailureRollsBackAll`,
  `TestR6C_MutationSetPartialApplyRollsBackAtomically`).
- Mutation truth is independently observable — **holds** (filesystem re-read +
  `mutation.completed`; `TestTruthMatrix_ValidMutation`).
- Durable mutation evidence ≠ objective completion — **holds** (§5 under
  Crash/Durability; separate ledger methods).
- Cancellation cannot produce a later mutation — **holds**
  (`TestR6_LateMutationResultCannotBeAppliedAfterCancel`).
- Failure is never rounded up to success — **holds**
  (`TestR6C_PanicAfterWriteIsRecoveredAsFailureNotSuccess`,
  `TestR6C_CommandNonZeroExitIsAFailure`).

### Verification
- Verification truth is separate from mutation truth — **holds**
  (`TestInvariant9_MutationDoesNotImplyVerification`).
- Verification failure cannot produce `PROVEN` — **holds**
  (`TestTruthMatrix_VerifierFailureRollsBack`).
- Missing verification is represented honestly — **holds** (six-state
  vocabulary; `test/forensics/verification_observability_test.go`).
- A verifier error cannot become `Passed=true` — **holds**
  (`verify.go:492-509`; `TestR6C_Verification_CancelledContextNeverPasses`).
- Verification is evaluated against the post-effect state — **holds**
  (`RunAllFor` runs after apply; `TestTruthMatrix_VerifierFailureRollsBack`).

### Progress
- Model activity is not progress — **holds**
  (`TestR5_P1_ModelOutputAloneIsNeverProgress`).
- Capability execution alone is not progress — **holds**
  (`TestR5_P3_CapabilityExecutionWithoutStateChangeIsNotProgress`).
- Progress derives from authoritative evidence — **holds** (`ReduceProgress`;
  `TestR5_P2`, `TestR5_P4`).
- Identical evidence cannot produce infinite continuation — **holds**
  (`TestR5_1_CaseB_IdenticalPartialStateDoesNotReopen`).
- Successful-but-partial execution cannot loop without new evidence — **holds**
  (R5.1 fingerprint + `MaxIdenticalDecisions` ceiling).

### Continuation
- Output exhaustion is distinguishable from task failure — **holds**
  (`TestR4_*`, `TestPhase12_ExhaustionIsNotTaskFailure`).
- Continuation is bounded — **holds** (executor step budget + driver recovery
  matrix + `RuntimeLoop` bounds).
- Continuation requires runtime-owned justification — **holds**
  (`TestR5_1_CaseA_ProgressThenProgressContinues`).
- Continuation is not blind retry / failure fingerprints prevent non-progress —
  **holds** (`failure_ledger.go`; `TestR5_CaseB_*`, `TestR5_CaseD_*`).

### Cancellation
- Cancellation is runtime-owned and propagates — **holds**
  (`TestR6_CancelWhileProviderActiveAbortsAndNeverMutates`).
- Cancelled execution cannot later become `PROVEN` — **holds**
  (`TestR6_LateProviderSuccessAfterCancelCannotComplete`).
- Cancellation is scoped to the correct execution identity — **holds**
  (`TestR6B_CancellationIsExecutionScoped`).
- Cancellation does not imply rollback; a committed mutation stays truthful —
  **holds** (`TestR6_CommittedMutationSurvivesCancellation`).

### Crash / Durability
- In-process failure is represented honestly — **holds** (R6-C suite).
- Process death cannot fabricate `PROVEN` — **holds**
  (`TestR6C_AbruptProcessDeathLeavesMutationStateUnreconstructible`).
- Durable mutation evidence is distinguishable from objective evidence —
  **holds** (`ledgerMutationCommitted` vs `ledgerExecutionCommitted`).
- Reconciliation is read-only — **holds** (`TestR6D_ReconcileIsReadOnly`).
- `ALREADY_COMMITTED` / `SAFE_RETRY` / `CONFLICT` / `UNKNOWN` remain distinct —
  **holds** (`durable/cursor.go:16`, `store.go:866`; `TestR6D_D1–D7`).
- Reconciliation does not automatically execute; no automatic resume — **holds**
  (`ReconcileInterrupted` appends nothing and transitions nothing).

### Identity / Isolation
- One execution cannot mutate another execution's state — **holds**
  (`TestR6B_NoQueue_SecondExecutionIsRefusedWhileActiveOrParked`,
  `TestR6B_KernelAdmitsExactlyOneExecution`).
- Late callbacks cannot cross execution boundaries — **holds**
  (`TestR6B_LateDecisionCannotCrossIntoTheNextExecution`; `d.runID` guard).
- Cancellation cannot leak between executions — **holds**
  (`TestR6B_CancellationIsExecutionScoped`).
- Durable records cannot be selected by ambiguous identity — **holds**
  (`ledgerTaskID` = SHA-256(session ⊕ objective); operation id is latched).
- Parked work has explicit terminal/abort semantics — **holds**
  (`Driver.Abort`; `TestR6B_ParkedAbortIsTerminalNonResumable...`).

---

## 6. End-to-end acceptance matrix

Deterministic, production-seam tests (scripted provider; real Driver, real
RuntimeExecutor, real composition where applicable). **A–J all PASS.** The R7
focused run was **254 PASS / 0 FAIL**.

| # | Scenario | Expected | Representative evidence | Result |
|---|---|---|---|---|
| A | Read-only execution (intent→objective→context→model→read→evidence→truthful result) | observation or response produced; no mutation; `PROVEN` only on evidence | `TestPhase14B_ReadOnlyObjectiveProves`, `TestExecutionTrace_UnprovenObjectiveContinues` | **PASS** |
| B | Authorized mutation (…→target authority→authorization→proposal→mutation→verification→`PROVEN`) | mutation applied once; verification passed; `PROVEN` with bytes | `TestExecutionTrace_CanonicalObjectiveSequence`, `TestDomainNeutralBenchmark_TruthfulTermination`, `TestTruthMatrix_ValidMutation` | **PASS** |
| C | Unauthorized mutation (proposal→authority rejection→zero mutation→no `PROVEN`) | zero delta; refusal recorded | `TestPhase0MutationsHeldBehindRuntimeApprovalBoundary`, `TestR3_UnauthorizedTargetCannotReachRuntimeExecutor`, `TestTruthMatrix_ApprovalRejected` | **PASS** |
| D | Ambiguous target (discovery→candidates→insufficient authority→DISAMBIGUATE/BLOCK→zero mutation) | park/DISAMBIGUATE; no provider billed; no mutation | `TestTruthMatrix_AmbiguousTargetStopsBeforeModel`, `TestR3_AmbiguousCandidatesParkWithCandidateSet`, `TestR3_UniqueCandidateIsProposedNotAuthorized` | **PASS** |
| E | Output exhaustion (model→truncated→bounded continuation→progress or bounded stop) | continuation inside same execution; never false completion | `TestR4_ExecutorBoundedStepContinuation`, `TestR4_DriverContinuationAfterBoundedStepBudgetExhausted`, `TestR4_RepeatedExhaustionNeverCompletes` | **PASS** |
| F | Non-progress (repeated decision→unchanged evidence→bounded stop) | typed non-progress; bounded stop; no reopen without delta | `TestR5_CaseB_*`, `TestR5_CaseD_*`, `TestR5_1_CaseB_IdenticalPartialStateDoesNotReopen` | **PASS** |
| G | Cancellation (running→cancel→propagation→terminal cancellation→no late mutation) | `cancelled`; no late mutation; committed mutation survives | `TestR6_CancelWhileProviderActiveAbortsAndNeverMutates`, `TestR6_LateMutationResultCannotBeAppliedAfterCancel`, `TestR6_CommittedMutationSurvivesCancellation` | **PASS** |
| H | Process death (mutation→death→restart→durable reconciliation→truthful state) | `ALREADY_COMMITTED`/`SAFE_RETRY`/`CONFLICT`/`UNKNOWN`; no auto-resume | `TestR6D_D7_ProcessDeathAndFreshRuntimeReconcile`, `TestR6D_D1–D6`, `TestR6D_NoCursorIsUnknownNotSafeRetry` | **PASS** |
| I | Verification failure (mutation→verification failure→no `PROVEN`) | rollback; no `PROVEN`; absent verification ≠ pass | `TestTruthMatrix_VerifierFailureRollsBack`, `TestR6C_VerificationFailureRestoresTheWorkspace`, `TestR6C_Verification_CancelledContextNeverPasses` | **PASS** |
| J | Provider failure (provider failure→typed failure→no false completion) | typed failure; zero mutation; no `PROVEN` | `TestR6C_ProviderErrorNeverCompletesAndNeverMutates`, `TestTruthMatrix_ProviderFailure` | **PASS** |

The matrix is finite and is not extended. Additional theoretical edge cases do
not reopen the contract.

---

## 7. Failure classification

No material failure was discovered that crosses the acceptance boundary.
Nothing in categories **A–E** was found. Every recorded limitation is **F**
(non-blocking limitation), **G** (documentation/tooling), or **H** (intentional
design boundary).

| ID | Observation | Class | Crosses acceptance boundary? | Action |
|---|---|---|---|---|
| L1 | `BehaviorRequired` is an English substring heuristic gating the behavioural *proof* stage | F | No — capability authorization no longer depends on it | None |
| L2 | Streaming truncation drops the delivered prefix (`invokeStream`) | F | No — driver recovery covers continuation | None |
| L3 | No startup consumer of `ReconcileInterrupted`; crash recovery is not an automatic contract | H | No — reconciliation is read-only and by-design non-automatic | None |
| L4 | R2 targetless `discovery → inspection` BLOCKED | H | No — evidence is never authority (I13); DISAMBIGUATE is correct | None |
| L5 | Capability evidence not enumerated inside sealed `ExecutionProof` (counter only) | D/F | No — evidence is still published; observability only | None |
| L6 | No distinct loop-level cancellation state/event | D/F | No — terminal state is truthful | None |
| L7 | Dormant `ToolCallBuffer` write path | F | No — never populated in production; routes through kernel/authorization if armed | None |
| L8 | `internal/domain/capability` portfolio gate reachable only from `izen run` | F/G | No — unreachable from `$prompt` | None |
| L9 | `patch.Engine.Apply` and second `RuntimeExecutor` have no production callers | F | No — non-load-bearing | None |
| L10 | Live benchmarks are opt-in; R7 relied on recorded live evidence + deterministic matrix | G | No — evidence-strength note | None |

Applying the criticality test to each: (1) no runtime invariant is violated;
(2) no required production capability is blocked; (3) none can produce the
forbidden outcomes; (4) each is demonstrable but classified non-blocking;
(5) none requires a new architectural subsystem. **Therefore none justifies
work.** The default outcome — `FREEZE` — stands.

---

## 8. Known limitations

The full list is in `RUNTIME_FORENSICS_STATE.md` §"Remaining limitations". None
is a correctness defect. The honest bounds of the current contract are:

- Behavioural proof engagement is heuristic; capability access is not.
- Crash **recovery** is not an automatic contract; truthful **reconciliation**
  is.
- The system remains single-lane: a second unit of work is refused, not queued.
- Live-model validation of the tool loop over a real network is not part of the
  deterministic suite.

---

## 9. Evidence references

**Evidence hierarchy applied.** Live production execution (R1–R6 opt-in runs,
recorded) > deterministic production-seam integration tests > deterministic unit
tests > static code inspection > documentation. Documentation and a TUI status
line were never treated as runtime evidence. Negative claims were only made after
reading the relevant code path, and `UNKNOWN` was preferred over inference where
evidence was absent (all such cases resolved to non-blocking limitations in §7).

### Reports (prior evidence)
- `docs/report/EXECUTION_FORENSICS.md` — R1 full trace + control arm.
- `docs/report/EXECUTION_FORENSICS_STATE.md` — R1–R6-D handoff.
- `docs/report/R3_TARGET_PROPOSAL_REPORT.md`, `R4_BOUNDED_CONTINUATION_REPORT.md`,
  `R5_NON_PROGRESS_REPORT.md`, `R6_CANCELLATION_REPORT.md`,
  `R6_INTERRUPT_QUEUE_REPORT.md`, `R6_CRASH_FAILURE_REPORT.md`,
  `R6_D_DURABLE_COMMIT_REPORT.md`.
- `docs/report/CAPABILITY_REALITY_AUDIT.md` — reachability audit; §H.4 web
  mappings removed; §B.4 dead/unreachable inventory.
- `docs/report/ARCHITECTURE_INVARIANT_LOCK_REPORT.md` — Phase 0/1 authority locks.
- `docs/report/RUNTIME_FORENSICS_STATE.md` — this audit's state handoff.

### Code (authority boundaries)
- `internal/runtime/compose/compose.go:500` (`Wire`), `:672` (`RuntimeExecutor`),
  `:915` (`ExecutorAdapter`), `:937` (`NewDriver`), `:752` (durable ledger).
- `internal/runtime/autonomy/driver.go:587` (`Run`), `:2140` (`observeAndRun`),
  `:2200` (`EvaluatePreflightAdmission`), `:2293`/`:2305`/`:2320`/`:2329`
  (authorities), `:2387`/`:2405` (durable prepared/committed).
- `internal/runtime/autonomy/preflight_admission.go:307`.
- `internal/runtime/autonomy/objective_completion.go:190`.
- `internal/runtime/autonomy/ledger.go:135/167/205/268`.
- `internal/execution/executor.go:1586` (`Execute`), `:2418` (`Approve`).
- `internal/execution/verify.go:482` (`runStep`).
- `internal/execution/objective_authority.go:727` (`Evaluate`), `:976` (`Authorize`).
- `internal/runtime/durable/cursor.go:16` (`Reconcile`),
  `store.go:811` (`InspectCursors`), `:866` (`inspectDecision`),
  `types.go:297-315` (the four decisions).
- `internal/ui/intent_dispatch.go:287`, `internal/ui/autonomy_route.go:56/163`,
  `internal/ui/autonomous.go:43/127`, `internal/ui/program.go:204`.

### Tests (deterministic acceptance)
- `internal/execution/`: `execution_truth_matrix_test.go`,
  `authority_invariants_test.go`, `execution_stage_distinctness_test.go`,
  `r5_progress_test.go`, `r6_cancellation_test.go`, `r6c_crash_failure_test.go`.
- `internal/runtime/autonomy/`: `r2_boundary_test.go`, `r3_target_proposal_test.go`,
  `r5_nonprogress_test.go`, `r5_1_progress_delta_test.go`, `r6_cancellation_test.go`,
  `r6b_lifecycle_isolation_test.go`, `r6c_provider_failure_test.go`,
  `r6d_wiring_test.go`, `objective_completion_test.go`,
  `domain_neutral_benchmark_test.go`, `execution_trace_test.go`.
- `internal/runtime/durable/`: `r6c_process_death_test.go`,
  `r6d_commit_marker_test.go`.
- `internal/architecture/`: `phase0_authority_lock_test.go`,
  `phase1_context_risk_lock_test.go`, `phase1_authorization_boundary_test.go`.
- `test/architecture/`: `kernel_lock_test.go` (deleted second write path).
- `test/forensics/`: `r1_scope_regression_test.go`, `r4_continuation_test.go`,
  `verification_observability_test.go`.

---

## 10. Regression results

R7 changed no production file, so regressions are measured against the frozen
HEAD.

```text
go build ./...                                   PASS
go test -count=1 ./...                           PASS  (exit 0; 211 packages ok; 0 fail)
go vet ./...                                     PASS  (no findings)
go test -race -count=1 \
  ./internal/runtime/autonomy/... ./internal/execution/... \
  ./internal/runtime/durable/... ./runtime/kernel/... ./internal/ui/... \
                                                 PASS  (exit 0; 28 packages ok;
                                                        0 DATA RACE; 0 fail)

R7 focused acceptance run (deterministic, production seam):
  254 PASS / 0 FAIL
```

The five opt-in live benchmarks (`test/live_r1`, `live_r2`, `live_r4`, `live_r5`,
`live_r6`) were not re-run (they require a local model); they compile and their
offline controls are green. The last recorded live runs are in the R1–R6
reports.

---

## 11. Termination decision

### Required final proof

| Question | Answer | Concrete evidence |
|---|---|---|
| Can the model authorize itself? | **NO** | `TestPhase1_ModelOutputCannotAuthorize`, `TestCapabilityToolRunnerRefusesUngrantedCapability`; admission vector grants capabilities, model output is a proposal |
| Can the runtime prevent unauthorized mutation? | **YES** | `EvaluatePreflightAdmission` (`preflight_admission.go:307`) + approval admission + `RuntimeExecutor` admission; `TestPhase0MutationsHeldBehindRuntimeApprovalBoundary`, `TestR6C_AuthorizationFailureBeforeMutationLeavesZeroDelta` |
| Can discovery silently become authority? | **NO** | `TestR3_DiscoveryEvidenceCannotDirectlyAuthorizeMutation`, `TestPhase16_1_EvidenceDoesNotGrantAuthority`, `TestInvariant_AmbiguousCandidatesCannotBecomeScope` |
| Can activity be mistaken for progress? | **NO** | `TestR5_P1_ModelOutputAloneIsNeverProgress`, `TestR5_P3_CapabilityExecutionWithoutStateChangeIsNotProgress` |
| Can progress be mistaken for completion? | **NO** | `TestR5_P6_ObjectiveAdvancementIsTheOnlyCompletionSignal`, `TestPhase14E_AuthorityIsSoleCompletionAuthority` |
| Can continuation loop without new evidence? | **NO** | `TestR5_1_CaseB_IdenticalPartialStateDoesNotReopen`, `TestR4_RepeatedExhaustionNeverCompletes` |
| Can cancellation be followed by a late mutation? | **NO** | `TestR6_LateMutationResultCannotBeAppliedAfterCancel`, `TestR6_CompletionCancelRaceIsDeterministic` |
| Can process death fabricate `PROVEN`? | **NO** | `TestR6C_AbruptProcessDeathLeavesMutationStateUnreconstructible`, `TestR6D_D7_ProcessDeathAndFreshRuntimeReconcile` |
| Can durable mutation truth be distinguished from objective truth? | **YES** | separate `ledgerMutationCommitted` vs `ledgerExecutionCommitted`; `TestR6D_D2_CommitThenRestartAlreadyCommitted` |
| Can verification failure produce `PROVEN`? | **NO** | `TestTruthMatrix_VerifierFailureRollsBack`, `TestR6C_Verification_CancelledContextNeverPasses` |
| Can provider failure produce `PROVEN`? | **NO** | `TestR6C_ProviderErrorNeverCompletesAndNeverMutates`, `TestTruthMatrix_ProviderFailure` |
| Can one execution affect another? | **NO** | `TestR6B_NoQueue_SecondExecutionIsRefusedWhileActiveOrParked`, `TestR6B_KernelAdmitsExactlyOneExecution`, `TestR6B_LateDecisionCannotCrossIntoTheNextExecution` |
| Can a limitation be mistaken for a correctness defect? | **NO** | §7 classification; each limitation is A–H-typed and none crosses the boundary |
| Can the audit terminate? | **YES** | this decision |

### Closure criteria

1. Core authority boundaries are explicit — **yes** (§3).
2. Production execution reaches the intended runtime path — **yes** (§2, §4;
   `program.go:204` wires `app.Autonomous`).
3. Mutation is authorization-gated — **yes**.
4. Completion is evidence-gated — **yes**.
5. Progress is runtime-owned — **yes**.
6. Continuation is bounded and evidence-driven — **yes**.
7. Cancellation is execution-scoped — **yes**.
8. Process failure cannot fabricate completion — **yes**.
9. Durable mutation truth is distinguishable from objective truth — **yes**.
10. Ambiguous targets cannot silently authorize mutation — **yes**.
11. Provider/model failures cannot fabricate success — **yes**.
12. The finite acceptance matrix passes — **yes** (A–J, 254/254).
13. No known critical invariant violation remains — **yes**.
14. Remaining limitations are explicitly classified as non-blocking — **yes** (§7–§8).
15. No unresolved defect requires inventing a new architecture — **yes**.

### Decision

```text
════════════════════════════════════════
                  FREEZE
════════════════════════════════════════
RUNTIME CONTRACT PROVEN
ARCHITECTURE STABLE
```

The runtime satisfies its defined contract. Remaining gaps are non-critical,
observability/convenience improvements, intentional design boundaries, or
research questions — explicitly out of scope. No new implementation phase is
justified. There is no automatic `R8`.

**Hard stop.** Do not polish the UI. Do not add convenience features. Do not
refactor because another abstraction looks cleaner. Do not create another phase.
