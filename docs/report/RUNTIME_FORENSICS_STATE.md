# RUNTIME FORENSICS — STATE (R7)

**Read `R7_RUNTIME_CLOSURE_REPORT.md` for the decision; read
`EXECUTION_FORENSICS.md` for R1's full trace.** This file is the handoff.

- **Branch:** `fix/runtime` (HEAD `9686c1e`, base `f40f369`)
- **Suite (R7):** `go build ./...` clean; `go test -count=1 ./...` **PASS**
  (211 packages `ok`, 0 failures, exit 0); `go vet ./...` clean. Targeted
  `-race` run over the runtime/exec plane is recorded in the closure report.
- **Status:** **R1 PROVEN · R2 BLOCKED (recorded, by design) · R3 PROVEN ·
  R4 PROVEN · R5 PROVEN · R5.1 PROVEN · R6-A PROVEN · R6-B PROVEN ·
  R6-C PROVEN (audit) · R6-D PROVEN · R7 CLOSED → FREEZE.**

R7 is a **closure audit, not a feature phase**. It reconstructed the production
runtime, re-ran the deterministic acceptance matrix, verified that the R1–R6
guarantees compose, and issued a termination decision. It added **no production
code** and **no new phase**. See `R7_RUNTIME_CLOSURE_REPORT.md`.

---

## The production path (as it actually is)

```text
izen (cmd/izen/main.go)
 └─ compose.Wire                                   internal/runtime/compose/compose.go:500
     ├─ RuntimeExecutor                            compose.go:672   (the execution authority)
     ├─ ExecutorAdapter                            compose.go:915
     ├─ autonomy.Driver (app.Autonomous)           compose.go:937
     └─ durable.TaskStore (ledger)                 compose.go:752
 └─ ui.RunMainDashboardWithApp                     program.go:204  autonomousDriver: app.Autonomous
     └─ "$prompt" → routePromptDirective           intent_dispatch.go:287
        └─ runAutonomyRoutedCmdExplicit            autonomy_route.go:56
           └─ executeAutonomyWorkspace             autonomy_route.go:163
              └─ executeAutonomyViaDriver          autonomous.go:43
                 └─ runAutonomousDriver            autonomous.go:127
                    └─ Driver.Run                  driver.go:587
```

The single loop owner is `autonomy.Driver.observeAndRun` (`driver.go:2140`).
The single execution/mutation authority is `execution.RuntimeExecutor`
(`executor.go:1586 Execute`, `:2418 Approve`). The single completion authority
is `execution.ObjectiveCompletionAuthority.Evaluate`
(`objective_authority.go:727`). There is no second loop, no second executor on
the `$prompt` path, and no queue of independent executions.

---

## What R7 verified (and the exact evidence)

| Runtime invariant | Verdict | Primary deterministic evidence (all PASS) |
|---|---|---|
| Model output cannot authorize mutation | holds | `TestPhase1_ModelOutputCannotAuthorize`, `TestCapabilityToolRunnerRefusesUngrantedCapability` |
| Unauthorized mutation cannot reach the boundary | holds | `TestR3_UnauthorizedTargetCannotReachRuntimeExecutor`, `TestR6C_AuthorizationFailureBeforeMutationLeavesZeroDelta`, `TestTruthMatrix_ApprovalRejected` |
| Discovery cannot silently become authority | holds | `TestR3_DiscoveryEvidenceCannotDirectlyAuthorizeMutation`, `TestPhase16_1_EvidenceDoesNotGrantAuthority`, `TestInvariant_AmbiguousCandidatesCannotBecomeScope` |
| Mutation is failure-contained / transactional | holds | `TestTruthMatrix_MultiFilePartialFailureRollsBackAll`, `TestR6C_MutationSetPartialApplyRollsBackAtomically` |
| Verification failure cannot produce PROVEN | holds | `TestTruthMatrix_VerifierFailureRollsBack`, `TestR6C_VerificationFailureRestoresTheWorkspace`, `TestR6C_Verification_CancelledContextNeverPasses` |
| Activity ≠ progress | holds | `TestR5_P1_ModelOutputAloneIsNeverProgress`, `TestR5_P3_CapabilityExecutionWithoutStateChangeIsNotProgress` |
| Progress ≠ completion | holds | `TestR5_P6_ObjectiveAdvancementIsTheOnlyCompletionSignal`, `TestPhase14E_AuthorityIsSoleCompletionAuthority` |
| Continuation is bounded and evidence-driven | holds | `TestR5_1_CaseB_IdenticalPartialStateDoesNotReopen`, `TestR4_RepeatedExhaustionNeverCompletes`, `TestPhase14C_TruncatedStreamNeverCompletes` |
| Cancellation is execution-scoped and cannot be followed by a late mutation | holds | `TestR6_LateMutationResultCannotBeAppliedAfterCancel`, `TestR6B_CancellationIsExecutionScoped`, `TestR6_CompletionCancelRaceIsDeterministic` |
| Process death cannot fabricate PROVEN | holds | `TestR6D_D7_ProcessDeathAndFreshRuntimeReconcile`, `TestR6C_AbruptProcessDeathLeavesMutationStateUnreconstructible` |
| Durable mutation truth ≠ objective truth | holds | `TestR6D_AutonomousMutationDispatchesAndCommitsDurably`, `TestR6D_D2_CommitThenRestartAlreadyCommitted` |
| Provider/model failure cannot fabricate success | holds | `TestR6C_ProviderErrorNeverCompletesAndNeverMutates`, `TestTruthMatrix_ProviderFailure` |
| One execution cannot affect another | holds | `TestR6B_NoQueue_SecondExecutionIsRefusedWhileActiveOrParked`, `TestR6B_LateDecisionCannotCrossIntoTheNextExecution` |
| Ambiguous targets cannot silently authorize mutation | holds | `TestR3_AmbiguousCandidatesParkWithCandidateSet`, `TestTruthMatrix_AmbiguousTargetStopsBeforeModel` |

R7's focused acceptance run: **254 PASS / 0 FAIL** across
`internal/execution/...`, `internal/runtime/autonomy/...`,
`internal/runtime/durable/...`, `internal/architecture/...`,
`test/forensics/...`.

---

## What R7 did NOT do

- Did **not** reopen R1–R6 or re-implement their work.
- Did **not** add a planner, provider architecture, tool protocol, queue, WAL,
  resume, retry subsystem, TUI subsystem, multi-agent orchestration, or
  domain-specific behaviour.
- Did **not** fix anything, because no discovered limitation crossed the
  acceptance boundary (see the closure report's failure classification).
- Did **not** create R8.

---

## Remaining limitations (all non-blocking; no fix proposed)

These are recorded so the next reader does not rediscover them as "defects".
They are classified in `R7_RUNTIME_CLOSURE_REPORT.md` §7–§8.

1. `BehaviorRequired` is still an English substring heuristic
   (`internal/runtime/autonomy/behavior.go`). It gates whether the behavioural
   *proof* stage engages, not whether capabilities are authorized. **F**.
2. Executor streaming truncation drops the delivered prefix
   (`invokeStream`), unlike the non-streaming path. Continuation is unaffected
   (the driver's recovery layer covers it). **F** (salvage symmetry).
3. No startup consumer of `Application.ReconcileInterrupted`. Reconciliation is
   read-only and reachable; crash recovery is **not** an automatic contract. **H**.
4. R2's targetless `discovery → inspection` remains BLOCKED by design:
   discovery candidates are evidence, never authority (I13); R3's DISAMBIGUATE is
   the correct, explicit outcome. **H**.
5. Capability-evidence records are published on the bus but not enumerated
   inside the sealed `ExecutionProof` (which carries a counter). **D/F**.
6. No distinct loop-level cancellation state/event; cancellation surfaces as an
   attempt `OutcomeCancelled` + loop `RuntimeAborted`. **D**.
7. Dormant `ToolCallBuffer` write path (`ui/keys.go:1176`). Nothing in
   production calls `ToolCallBuffer.Buffer` (only tests do), so
   `HasPending()` is never true; and `ApplyApproved` now routes through the
   Runtime Kernel + authorization rather than a bare `os.WriteFile`. Inert,
   latent. **F**.
8. `internal/domain/capability`'s portfolio/to-do gate and several markup
   detectors remain in code reachable only from `izen run` / planning, never
   from the `$prompt` authority path. **F/G** (out of the runtime contract).
9. Two recorded reachability facts from `CAPABILITY_REALITY_AUDIT.md` remain:
   `patch.Engine.Apply` has no production call site; the second
   `internal/runtime/executor.RuntimeExecutor` type has no production caller.
   **F**.
10. The R1–R6 live benchmarks are opt-in (real local model, `IZEN_LIVE_FORENSICS=1`).
    R7 did not re-run them; it relied on the recorded live evidence plus the
    deterministic matrix. **G** (evidence-strength note, not a defect).

None of the above can produce unauthorized mutation, false `PROVEN`, false
progress, false verification, duplicate mutation, cross-execution contamination,
unbounded continuation, incorrect cancellation, incorrect durable mutation
truth, or loss of human authority.
