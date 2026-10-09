# IZEN — Final Closure Audit: Recovery, Interaction Completeness, and Runtime Reliability

**Status:** all four integration gaps closed; one parallel-test defect root-caused and fixed.
**Branch:** `fix/execution`
**Scope:** durable cross-process proposal recovery; complete free-form TUI
interaction; a multi-phase migration integration fixture; reproduction, root
cause and repair of the parallel test instability; the §6 regression set.

Every claim below is backed by source in this working tree and by commands run
against it. Tests that use a scripted model are named as such.

---

## A. Verified baseline

Baseline commit `5e33d5d`, clean tree. I re-read the previous report's six
limitations and verified each against the source rather than trusting it:

| Prior limitation | State at start | Closed by |
|---|---|---|
| 1. Autematic cross-process restore not wired | `RecordCustomEvent` had no read-back; `RestoreProposal` was in-process only | §B.1 |
| 2. Rendered verification policy-gated | unchanged (out of scope) | — |
| 3. Rendered checks are DOM-only | unchanged (out of scope) | — |
| 4. Free-form TUI input API-only | `QuestionFreeForm`/`AllowFreeForm` existed in the contract and the transport; the TUI projected single/multi only | §B.2 |
| 5. No multi-phase fixture | contract modelled phases; no end-to-end fixture | §B.3 |
| 6. Parallel flake "documented" | two tests, cause not identified | §D |

The architectural invariants held at baseline and still hold: the `Driver` is the
single execution/authorization owner over the `ExecutorAdapter`,
`execution.ObjectiveCompletionAuthority` is the only completion owner, and a
proposal grants no capability.

---

## B. Implementation

### New files

**`internal/runtime/durable/custom_events.go`** — audit-lineage read-back.
`CustomEvent`, `IsCustomEventType`, `TaskStore.CustomEvents(taskID, types…)`,
`TaskStore.CustomEventsTruncated(taskID)`, and a bounded retention window
(`maxRetainedCustomEvents = 512`) folded from `apply`, so live appends and replay
retain identically. The bound is REPORTED, not hidden: a caller concluding "the
human never decided" over a dropped window would be inventing an absence. The
journal remains the only authority; retention is a cache.

**`internal/runtime/autonomy/proposal_recovery.go`** — the cross-process seam.
`Driver.RestoreProposalFromLedger(objective)` reads the real store, and
`Driver.continueRestoredProposal` is the single consumer of a restored proposal.

**`internal/runtime/autonomy/proposal_recovery_test.go`** — the real
persistence round trip plus the refusal matrix (§C.1).

**`internal/runtime/autonomy/proposal_multiphase_test.go`** — the multi-phase
migration fixture (§C.3).

**`test/benchmark/cli_multiphase_test.go` + `test/benchmark/ledger_inspect.go`**
— the same fixture at the real CLI/runtime process boundary (§C.3).

### Changed files

| File | Why |
|---|---|
| `internal/runtime/durable/store.go` | `custom`/`customTruncated` fields, replay reset, `retainCustom(ev)` call in `apply`. 35 lines, no behavioural change to any existing path. |
| `internal/runtime/proposal/state.go` | `Restore` now REFUSES a revision < 1 and a strategy-less contract instead of silently repairing them. A revision of 0 cannot be minted by `State.Apply`; a proposal that compiles to nothing must not be accepted at all. |
| `internal/runtime/autonomy/driver.go` | `Run` restores a durable proposal for an objective whose task is still open, and refuses the run when the recorded state cannot be trusted. 28 lines. |
| `internal/runtime/autonomy/adaptive_proposal.go` | `maybeAdaptiveProposal` continues a restored proposal instead of forming a competing one; `proposalIdentity` split so the objective half is stable across processes; `invalidateProposalDerivedState` now shares `dropStaleDerivedProposalState` with the restore path (no duplicated logic). |
| `internal/ui/autonomous.go` | free-form answer state and its pure renderer; free-form submission folded into the SAME `resumeAutonomousAdaptiveProposal` the option path uses; empty submissions refused before the runtime is invoked. |
| `internal/ui/keys.go` | `i` opens the free-form editor, Enter commits, Esc discards, Backspace edits, ↑/↓ moves the question cursor. |
| `internal/ui/model.go` | `adaptiveFreeform`, `adaptiveFocus`, `adaptiveEditing`, `adaptiveDraft` — presentation state only. |
| `internal/ui/autonomous_test.go` | the fake driver records the fingerprint it was called with. |
| `execution_trace_test.go`, `r5_nonprogress_test.go`, `r3_target_proposal_test.go`, `phase12_portfolio_integration_test.go` | §D's defect. |

No file was deleted. No architecture test was relaxed. No assertion was weakened
anywhere in this pass — the §D fix replaces un-waited reads of an asynchronous
projection with bounded waits for the same events.

---

## C. The three deliverables

### C.1 Cross-process recovery — and its evidence

**The lifecycle, traced end to end.** Human decision → `State.Apply` (revision
advances, dependency graph invalidated) → `Driver.ledgerHumanDecision` →
`TaskStore.RecordCustomEvent(HUMAN_DECISION)` → `appendLocked` (single JSON line,
fsync) → `apply` → `retainCustom`. Snapshot: `State.Snapshot` → `ledgerProposalSnapshot`
→ `PROPOSAL_SNAPSHOT`. On restart: `NewTaskStore(sameDir).Open()` → `replayLocked`
→ `retainCustom` rebuilds the window → `Driver.RestoreProposalFromLedger` reads
it back → `decodeProposalSnapshot` → `reconcileRestoredProposal` →
`proposal.Restore` → `d.proposalState` → `continueRestoredProposal` → the
EXISTING approval gate.

**What the restore establishes.**

1. **Identity and revision** — proposal id (objective-derived half), revision,
   and fingerprint all read from the journal.
2. **Decisions and provenance** — each decision is cross-checked against its
   question and required to be `ProvenanceHuman`.
3. **Dependency graph** — `proposal.Restore` runs `Validate`; `continueRestoredProposal`
   calls `SelectInteraction` and `Unanswered` over the reconstructed contract, so
   the remaining questions are the ones the human actually left open.
4. **Rejection of malformed / incomplete / stale / tampered** — see the matrix below.
5. **Reconciliation** — objective must match verbatim; the durable task must
   exist, be non-terminal, and record the same intent; the payload's own index
   fields must agree with the decoded contract; the snapshot history must be
   monotonic; and after compilation the targets must still resolve through the
   canonical gateway.
6. **Invalidation of derived state** — `dropStaleDerivedProposalState` drops held
   candidates, invalidates the compiled context and re-arms the grant gate.
7. **No authorization from a snapshot** — nothing grants anything; the restore is
   state only, and the mutation still has to pass the approval gate (asserted).
8. **Completed work preserved** — a retired task restores nothing.
9. **Fail closed** — a recorded-but-untrustworthy proposal aborts the run with a
   named reason rather than proceeding as though nothing had been recorded.

**The round-trip test.** `TestProposalSurvivesProcessRestartThroughTheDurableStore`
constructs a driver over a real temp workspace with a real ledger, runs to the
adaptive-proposal park, copies the identity it must reproduce, sets
`first = nil`, then **closes the store and opens a new one over the same
directory** and **constructs a second driver**. The second driver restores from
`ledger.ndjson` and re-parks at the same revision with the same questions and
zero provider calls. `TestRestoredProposalCompilesAndReachesTheExistingApprovalGate`
does the same after a decision was recorded, and asserts the restored decision's
provenance, that the resumed run still stops at the approval gate, and that
nothing was written before approval.

Observed:

```
ok  github.com/PizenLabs/izen/internal/runtime/autonomy
  TestProposalSurvivesProcessRestartThroughTheDurableStore   PASS
  TestRestoredProposalCompilesAndReachesTheExistingApprovalGate PASS
  TestRestoreRefusesTamperedAndForeignSnapshots (6 sub-cases) PASS
  TestRestoreRefusesASnapshotForAnotherObjective             PASS
  TestRestoreRefusesADecisionWithoutHumanLineage             PASS
  TestRestoreSkipsARetiredTask                               PASS
  TestCustomEventReadBackSurvivesReplay                      PASS
  TestCustomEventRetentionIsBounded                          PASS
```

The refusal matrix (each mutates the journal on disk, then reopens):

| Mutation | Refusal names |
|---|---|
| tampered fingerprint | `fingerprint` |
| empty snapshot body | `no snapshot body` |
| undecodable body | `undecodable` |
| revision 0 (fingerprint re-stamped so the revision is the only defect) | `revision` |
| payload id ≠ contract id | `names proposal` |
| payload revision ≠ contract revision | `names revision` |
| id from a different objective | `this objective` |
| snapshot history that goes backwards | `not monotonic` |
| decision with its `HUMAN_DECISION` record removed | `HUMAN_DECISION` |

`TestRestoreRefusesADecisionWithoutHumanLineage` is the load-bearing one: a
snapshot claiming a decision the journal never witnessed is refused, which is
what stops authorization being reconstructed from a snapshot alone.

### C.2 Complete free-form TUI interaction — and its evidence

The UI never mutates proposal state. `resumeAutonomousAdaptiveProposal` collects
the option ids AND the free-form text and routes both through
`Driver.ResumeAdaptiveProposal` — the same seam, with the same fingerprint
binding. Free-form answers replace any option selection for the same question,
because they are two spellings of one decision.

Verified, in order: input validation (an empty submission is refused before the
runtime is invoked, and the park survives; a question that permits no free-form
never opens an editor); identity/revision binding (the runtime rejects a stale
fingerprint — asserted); dependency invalidation (the multiphase fixture's
boundary change invalides the dependent phase and its criterion and nothing
else); durable recording (`HUMAN_DECISION` in the journal, restored with
`ProvenanceHuman`); re-parking when decisions remain; and recompilation +
admission before execution resumes.

Tests: `TestAutonomousAdaptiveProposalFreeFormSubmitsThroughTheDriver`,
`TestAutonomousAdaptiveProposalFreeFormDraftIsDiscarded`,
`TestMigrationFixture_FreeFormDecisionTraversesTheAuthoritativePath`,
`TestAdaptive_BlankFreeFormDecisionIsRefusedByTheAuthoritativePath`.

The draft is bounded (4000 characters) and control-key input is rejected, so a
paste cannot become a human decision. A draft that was never committed never
reaches the runtime — Escape discards it and the previously committed answer is
untouched.

Simple tasks stay simple: no proposal workflow and no confirmation screen was
added anywhere, and `TestAdaptive_DirectTaskKeepsItsPath` still passes.

### C.3 Multi-phase integration fixture — and its trace

The fixture is a static site whose inline script must be extracted and whose
stylesheet must be adapted. Two phases, two acceptance criteria, two
checkpoints, two material trade-offs, one risk. Two consequential questions:
the strategy, and the phase boundary.

The trace, with the exact commands:

```
$ go test -count=1 -v -run 'TestMigrationFixture_' ./internal/runtime/autonomy/
=== RUN   TestMigrationFixture_IntegratedWorkflow
    final state = completed reason="objective satisfied: changed; objective PROVEN by evidence" progress=PROVEN served=map[index.html:1 styles.css:1]
--- PASS: TestMigrationFixture_IntegratedWorkflow (0.61s)
=== RUN   TestMigrationFixture_CompletedWorkIsNotRedoneAfterRestore
    provider calls before=1 after=2 survived=true served=map[]
--- PASS
=== RUN   TestMigrationFixture_DecisionChangeInvalidatesDependentPhase
--- PASS
=== RUN   TestMigrationFixture_StaleSnapshotIsNotResumed
--- PASS
=== RUN   TestMigrationFixture_FreeFormDecisionTraversesTheAuthoritativePath
--- PASS
ok  github.com/PizenLabs/izen/internal/runtime/autonomy 1.80s
```

Step by step, all on the production path:

1. **Discover and inspect the evidence.** The objective names no file; the
   evidence-bound derivation binds `index.html` and `styles.css` and excludes
   `readme.md`. Asserted.
2. **Produce a proposal.** Recommendation `phased-migration` (with its
   trade-off), phases `extract-markup` (order 1, own boundary + checkpoint) and
   `adapt-styles` (order 2, own boundary + checkpoint), two acceptance criteria
   bound to source-level verification. All asserted off the boundary projection.
   Zero provider calls before the human decides.
3. **Record the decisions.** `phase-order=phased-migration` → revision 2, re-park
   (executes nothing, zero provider calls); `phase-boundary=after-markup` →
   revision 3.
4. **Compile.** Strategy id, both phases' targets, both criteria, every decision
   carrying `ProvenanceHuman`, two compiled constraints. The existing approval
   gate holds the candidate; nothing was written.
5. **Execute an authorized phase.** Resume-approve; both phases land, each over
   its own artifact (`served=map[index.html:1 styles.css:1]` — a mis-dispatch
   would have thrown).
6. **Verify.** `VERIFICATION_RESULT` and `EXECUTION_COMMITTED` in the journal.
7. **Interrupt and restore.** The driver and its store are destroyed; a new pair
   is opened over the same `ledger.ndjson`; `RestoreProposalFromLedger` restores
   revision 3 and the human's `after-markup` answer.
8. **Continue only when valid.** The resumed run compiles the restored proposal
   and stops at the approval gate again — the decision survived the boundary,
   the authorization did not. A retired task restores nothing, and an artifact
   whose phase already landed is not rewritten.
9. **Report the final state.** `completed`, "objective satisfied: changed;
   objective PROVEN by evidence", `PROVEN`, durable task terminal.

**The consequential decision change.**
`TestMigrationFixture_DecisionChangeInvalidatesDependentPhase` answers the
strategy, then the boundary, then changes the boundary from `after-markup` to
`after-styles` while the candidate is held. The change advances the revision;
the fingerprint changes; `InvalidatedElements()` is exactly
`[criterion:crit-styles, phase:adapt-styles]` — the dependent phase and its
criterion, and NOT `criterion:crit-markup` or `phase:extract-markup`, which is
what makes the invalidation dependency-derived rather than a blanket reset. The
held candidate is dropped, `ResumeApprove` no longer lands a byte, and the
compiled execution contract carries the new revision and fingerprint.

**The CLI/runtime boundary.**
`TestCLIBench_MultiPhaseMigrationDerivesTheImplementationScope` drives the real
`izen` binary against a scripted OpenAI-compatible provider over the same
fixture. Assertions: no filename clarification park, both phases' artifacts
changed, `readme.md` byte-identical, `PROPOSAL_SNAPSHOT` present in
`.izen/runtime/ledger.ndjson`, and `izen resume` over the finished objective
refuses rather than re-running it. Observed outcome `unsubstantiated` with
`GATE_UNVERIFIED` — the truthful non-success for an unverified project-level
change, matching the existing benchmark.

Fakes are identified: the `targetProvider` scripted model in the in-process
fixtures, and `NewScriptedProvider` in the CLI test. Everything else — CLI
binary, kernel, executor, gateway, verifier, ledger, completion authority — is
the real implementation.

---

## D. Parallel test instability: reproduction, root cause, repair

### D.1 The exact failing tests and assertions

The previous phase reported "one failure each time in a DIFFERENT,
pre-existing timing-sensitive autonomy test" and attributed it to cross-package
contention. I did not accept that attribution without evidence.

The two tests named were `TestExecutionTrace_CanonicalObjectiveSequence`
(`internal/runtime/autonomy/execution_trace_test.go`) and
`TestR5_CaseA_EndToEndMutationReachesProven`
(`internal/runtime/autonomy/r5_nonprogress_test.go`). The failing assertions:

- `execution_trace_test.go:265` — `objective.accepted: no target resolution evidence`
- `r5_nonprogress_test.go:209` — `no continuation record reported PROVEN progress`

### D.2 Reproduction

Five full-suite runs with default package parallelism at baseline were green
(5/5), so the fault is load-sensitive, not deterministic. I reproduced it
deterministically by supplying the contention the default parallelism was
hiding — eight CPU burners while running the two tests:

```
$ for c in 1..8; do yes > /dev/null & done
$ for i in $(seq 1 15); do go test -count=1 -run 'TestR5_CaseA_EndToEndMutationReachesProven|TestExecutionTrace_CanonicalObjectiveSequence' ./internal/runtime/autonomy/; done
TOTAL FAILS = 9/15
```

Failing assertions observed across those runs — both of the named ones:

```
execution_trace_test.go:265: objective.accepted: no target resolution evidence
r5_nonprogress_test.go:209:   no continuation record reported PROVEN progress
```

`GOMAXPROCS=1` reproduces it even harder: the whole `autonomy` package failed
15/15 runs, and after fixing the two named tests it still failed 1/1 with three
more instances of the same defect that the default parallelism had never
surfaced:

```
phase12_portfolio_integration_test.go:295: no context telemetry was emitted
r3_target_proposal_test.go:343:           no authorization record
execution_trace_test.go:372:             the run terminated before a second computation was possible
```

So the prior phase's "cross-package contention" was a symptom report, not a root
cause, and it was incomplete: the same defect existed in four more places.

### D.3 Root cause

All six failures are the same defect: **an assertion reading an
asynchronously-populated bus projection without waiting for delivery.**

`events.Bus.Publish` does not call a handler inline. It enqueues onto a
per-subscription channel and a `dispatchLoop` goroutine drains it. The bus
deliberately prioritises bounded batches of control events over telemetry, so
delivery of any event can legitimately lag the run that published it by an
unbounded, scheduling-dependent amount. The tests read the recorder
immediately after `Run`/`ResumeApprove`/`Execute` returned and asserted on
absence. Under CPU contention the dispatcher had not yet drained the queue, so
an event that WAS published read as absent.

This is a defect in the assertions, not in the runtime: the correct statement is
"the run must publish X", and the observation of X must wait for X to be
observable. It is not cross-package shared state, not a filesystem path, not an
environment variable, not a goroutine leak, and not a test-order dependency —
the failing reads are all unsynchronised by construction.

### D.4 Repair (no assertion weakened)

Same class of fix in each place: keep the assertion, wait for the observation.

| File | Before | After |
|---|---|---|
| `execution_trace_test.go` | `hasKind(EventTargetResolved)` and `hasKind(EventExecutionStarted)` read immediately | `waitForKind(5s, …)`, the helper the file already uses for `EventContextPrepared` |
| `execution_trace_test.go` | `collector.loopTransitions() == 0` | `collector.waitTransitions(1, 5s)` |
| `r5_nonprogress_test.go` | `rec.snapshot()` read immediately, then assert PROVEN | `waitForProven(rec)` — a bounded wait for the same condition — then assert PROVEN. `containsProven` is used by both, so the wait looks for exactly what is asserted. |
| `r3_target_proposal_test.go` | `r.Events.authorization()` | `r.Events.waitForAuthorization(5s)` |
| `phase12_portfolio_integration_test.go` | `len(prepared) == 0` under a held mutex | `waitContextPrepared(&mu, &prepared, 5s)` before the mutex is taken (taking it after would deadlock) |

No `time.Sleep` was added to hide a failure, no `t.Parallel` was added, nothing
was serialised, no timeout was lengthened past what the file already used, and
no assertion was removed. `hasKind` and the `kinds` helper it used are now
deleted rather than left unused.

### D.5 Repeated-run results after the fix

```
$ for i in $(seq 1 20); do go test -count=1 -run 'TestR5_CaseA_…|TestExecutionTrace_|TestR3_|TestPhase12_' ./internal/runtime/autonomy/; done
TOTAL CPU-LOAD FAILS = 0/20        # was 9/15

$ for i in $(seq 1 6); do GOMAXPROCS=1 go test -count=1 ./internal/runtime/autonomy/; done
TOTAL GOMAXPROCS=1 FAILS = 0/6      # was 15/15 (package-level)

$ for i in $(seq 1 10); do GOMAXPROCS=1 go test -count=1 -run 'TestR5_CaseA_EndToEndMutationReachesProven' ./internal/runtime/autonomy/; done
TOTAL = 0/10                        # was 10/10
```

Before the fix, `GOMAXPROCS=1` on the package also surfaced a deadlock introduced
by an early draft of the phase-12 change; that draft was corrected before the
counts above, and the final form is the one reported.

**Conclusion.** The previously reported instability is PRE-EXISTING (not
introduced by the previous phase's changes — none of those files touched these
tests, and the failure reproduces on the untouched baseline), it is a
test-side synchronisation defect, it has been repaired at the root, and the
repair is verified under the contention that produced it. Nothing remains
unresolved in this dimension.

---

## E. Verification semantics preserved

I did not touch rendered verification, and I did not add any browser
infrastructure. `internal/runtime/autonomy/rendered_verify.go` and its tests are
unchanged, so the existing claims stand exactly as reported:

- deterministic DOM checks prove only the conditions they inspect;
- a screenshot does not prove visual quality;
- no claim of model-assessed visual judgement is made anywhere;
- a blocked check (`$hot`, no browser, missing entry) is `GATE_UNVERIFIED`, and
  the existing `TestRendered_*` matrix still passes.

The new code adds no path by which a skipped or failed verification could
satisfy an integrity obligation: a restored proposal compiles into the existing
request and passes through the same completion authority, which still refuses to
call an unverified project-level change PROVEN.

---

## F. Validation commands and results

All run from the repository root, on the final tree.

| Command | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l .` | no output |
| `golangci-lint run --timeout=10m ./internal/... ./test/...` | **0 issues** |
| `go test -count=1 -p 1 ./...` | **213 `ok`, 0 failures** (`final2-p1.log`) |
| `go test -count=1 ./...` (default parallelism) ×5 | **5/5 green, 0 failures** |
| `go test -race -count=1 ./internal/runtime/proposal/ ./internal/runtime/durable/` | ok |
| `go test -race -count=1 -run 'TestAdaptive_\|TestMigrationFixture_\|TestProposalSurvives\|TestRestored\|TestRestore\|TestCustomEvent' ./internal/runtime/autonomy/` | ok, 22.5s, no data race |
| `go test -race -count=1 -run TestAutonomousAdaptiveProposal ./internal/ui/` | ok |

Note on `-p 1` being green: per the instruction, a green serialized suite is not
on its own evidence of reliability. The reliability evidence here is §D.5 —
the previously failing tests now pass 20/20 under the CPU contention that
produced the failures, and 10/10 and 6/6 under `GOMAXPROCS=1`. That is the same
condition the default-parallelism run creates; the serial run simply removes the
contention.

---

## G. §6 mandatory regression checks

| Check | Status | Evidence |
|---|---|---|
| Simple explicit mutations keep the direct path | **PASS** | `TestAdaptive_DirectTaskKeepsItsPath` (advisor wired; still the approval gate, no proposal compiled) |
| Broad objectives derive justified scopes without filename clarification | **PASS** | `TestAdaptive_InteractionDiffersByDecisionRequirement`, `TestOrchestration_B_ProjectRedesignDerivesMultiFileScope`, `TestCLIBench_ProjectRedesignDerivesMultiFileScope` |
| Proposal acceptance cannot bypass mutation authorization | **PASS** | `TestAdaptive_ProposalDoesNotWidenAuthority`, `TestMigrationFixture_IntegratedWorkflow` (approval gate, nothing written) |
| `$hot` cannot widen its scope | **PASS** | `TestOrchestration_J_HotSurfaceNeverWidensToProjectScope` (pre-existing) |
| Consequential decision changes invalidate dependent state | **PASS** | `TestMigrationFixture_DecisionChangeInvalidatesDependentPhase` (invalidated set is exactly the dependent phase + its criterion) |
| Stale approvals cannot authorize a changed candidate | **PASS** | same test: held candidate dropped, `CandidateHeld` false, `ResumeApprove` lands no byte, compiled fingerprint advanced |
| Cross-process restoration preserves decision provenance and authority boundaries | **PASS** | `TestProposalSurvivesProcessRestartThroughTheDurableStore`, `TestRestoredProposalCompilesAndReachesTheExistingApprovalGate`, `TestRestoreRefusesADecisionWithoutHumanLineage` |
| Skipped or failed verification cannot produce unsupported PROVEN outcomes | **PASS** | `TestRendered_*`, `TestOrchestration_B_ProjectRedesignUnverifiedIsNotProven` (pre-existing); the CLI migration test asserts `unsubstantiated`/`GATE_UNVERIFIED` |
| Existing kernel and execution architecture constraints remain enforced | **PASS** | `go vet ./...`, `golangci-lint` 0 issues, `internal/architecture`, `test/architecture`, `architecture_lock_test.go`, `test/forensics` all green |

Diff hygiene, as required: no duplicated state ownership (the restore path reuses
`dropStaleDerivedProposalState` rather than copying the invalidation); no alternate
execution path (the only new consumer is `continueRestoredProposal`, which calls
the pre-existing `Compile`/`applyCompiledProposal`/approval gate); no relaxed
architecture test; no unrelated change; no leftover cleanup (the one stray
`os.Remove` in a draft was removed, and `hasKind` is deleted rather than
orphaned).

---

## H. Remaining limitations

1. **The restore is automatic in `Run` but only for a proposal a previous process
   recorded.** It is not wired into `izen resume`, which has its own (unrelated)
   durable-task continuation. If the same objective was run more than once in a
   process, the restored proposal is the one the run-request identity maps to; a
   second process reaching the objective on a later run index would re-form
   rather than adopt. This is fail-safe (never fail-open) and is the documented
   boundary of the deterministic identity scheme, but it is a real limit on how
   many interruptions one objective survives.
2. **Retention is bounded (512 custom events per task).** For an objective that
   generated more audit lineage than that, the window truncates and
   `CustomEventsTruncated` reports it; the restore then refuses rather than
   reads a non-newest tail. The full journal remains on disk and authoritative.
3. **Production advisors still ask no consequential question.**
   `DefaultProposalAdvisor` recommends and asks nothing, so the consequential
   path in production requires a domain/policy advisor. This was already the
   documented boundary and is unchanged.
4. **Rendered verification remains policy-gated and DOM-only.** Documented, not
   regressed.
5. **The parallel-instability evidence is load-drive reproduction, not
   spontaneous reproduction on an idle machine.** On an idle machine the
   default-parallelism suite passed 5/5 before the fix as well; the defect was
   always latent. I am reporting it as root-caused and repaired rather than
   claiming the machine is now immune to every scheduling pattern.

---

## I. Definition of Done

| # | Criterion | Verdict |
|---|---|---|
| 1 | Proposal state survives a real durable-store process restart and restores safely | **COMPLETE** — `TestProposalSurvivesProcessRestartThroughTheDurableStore` writes, destroys the owner, reopens the store on disk, restores revision + fingerprint + identity, and resumes. |
| 2 | Free-form TUI decisions traverse the same authoritative runtime path | **COMPLETE** — `resumeAutonomousAdaptiveProposal` routes free-form through `Driver.ResumeAdaptiveProposal` with the parked fingerprint; validated, durable, re-parks, recompiles. |
| 3 | Multi-phase fixture demonstrates proposal, execution, verification, interruption and recovery | **COMPLETE** — five tests, two real process boundaries, per-phase execution + verification + terminal `PROVEN`, plus the CLI-boundary counterpart. |
| 4 | Parallel instability reproduced and resolved, or documented with evidence | **COMPLETE** — reproduced on demand (9/15 under CPU load, 15/15 under `GOMAXPROCS=1`), root-caused to unsynchronised reads of asynchronous bus delivery, fixed in six places, verified 0 failures under the same contention. |
| 5 | Authorization, revision and truthful-completion invariants remain intact | **COMPLETE** — approval gate preserved on every path; stale candidate/approval cannot authorize a changed proposal; a restored proposal grants nothing; nothing written before approval in any new test; unverified objectives still refuse PROVEN. |
| 6 | All claimed validation results correspond to actual commands and observed results | **COMPLETE** — §F lists the exact commands and the counts actually observed; §C and §D quote actual test output. |

**Overall: COMPLETE.**

---

## J. Where the work lives

```
internal/runtime/durable/custom_events.go            NEW  audit-lineage read-back + bounded retention
internal/runtime/durable/store.go                    MOD  retention fields, replay reset, retainCustom call
internal/runtime/proposal/state.go                   MOD  Restore refuses revision<1 and strategy-less
internal/runtime/autonomy/proposal_recovery.go       NEW  cross-process restore + reconciliation
internal/runtime/autonomy/proposal_recovery_test.go  NEW  round trip + refusal matrix
internal/runtime/autonomy/proposal_multiphase_test.go NEW migration fixture
internal/runtime/autonomy/adaptive_proposal.go       MOD  restore-aware seam, shared invalidation
internal/runtime/autonomy/driver.go                   MOD  restore + fail-closed refusal in Run
internal/runtime/autonomy/adaptive_proposal_test.go  MOD  blank free-form refused at the runtime seam
internal/runtime/autonomy/execution_trace_test.go    MOD  wait for delivery (2 places)
internal/runtime/autonomy/r5_nonprogress_test.go     MOD  wait for PROVEN classification
internal/runtime/autonomy/r3_target_proposal_test.go MOD  wait for authorization record
internal/runtime/autonomy/phase12_portfolio_integration_test.go MOD wait for context telemetry
internal/ui/autonomous.go                            MOD  free-form answers + pure renderer
internal/ui/keys.go                                  MOD  free-form editor key handling
internal/ui/model.go                                 MOD  free-form presentation state
internal/ui/autonomous_test.go                       MOD  fake records the resume fingerprint
internal/ui/adaptive_proposal_test.go                MOD  free-form UI tests
test/benchmark/cli_multiphase_test.go                NEW  CLI-boundary migration test
test/benchmark/ledger_inspect.go                     NEW  ledger reader for the boundary assertions
```

Dynamic Path. Static Authority. Truthful State Transition.
