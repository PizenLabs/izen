# R6-B — INTERRUPT AND QUEUE LIFECYCLE FORENSICS

**Status: PROVEN. No lifecycle defect found; no production code changed.**

R1–R6-A are treated as PROVEN and are **not reopened**. R6-B answers exactly one
question:

> Does an interrupt/cancel request apply to exactly the intended execution, or
> can active, queued, and parked work interfere with one another?

The answer is: **cancellation is execution-scoped, and it cannot cross an
execution boundary** — because the runtime does not have a queue of independent
executions at all. It has a single execution lane at every owner, and a second
unit of work is *refused*, never enqueued. The R6-B invariants therefore hold,
mostly by the stronger property that there can only ever be one execution at a
time; and the parked/continuation/late-callback cases are proven directly.

**Branch:** `fix/runtime` (continues from the recorded R6-A state).
**Read `EXECUTION_FORENSICS.md` / `EXECUTION_FORENSICS_STATE.md` first.**

```
# deterministic (no model)
go test ./internal/runtime/autonomy/ -run TestR6B_ -v
go test ./runtime/kernel/             -run TestR6B_ -v
go test ./internal/ui/                -run TestR6B_ -v

# existing single-lane invariant (not new, re-cited)
go test ./internal/ui/ -run TestInvariant6_NewExecutionIsRefusedAtAdmissionWhileARunIsParked -v
```

No live provider test was run. R6-B establishes semantics deterministically; a
live call would not add evidence.

---

## 0. Answer in one line

```text
there is no queue of independent executions
  → a second execution is REFUSED (driver, UI admission, kernel), not enqueued
  → cancellation is a per-execution context withdrawal (or a per-engine flag)
  → parked work is aborted through Driver.Abort, which is terminal + non-resumable
  → a late decision from a terminated run is refused against the next run
  → continuation keeps its original execution identity
```

---

## 1. Forensic method

The investigation did **not** assume the UI vocabulary ("queued", "parked",
"pending") is authoritative. It traced the production lifecycle at every owner
and asked, for each, "can two units coexist, and who owns cancellation?" The
sources are the production call sites, not names:

- `internal/runtime/autonomy/driver.go` — the autonomous execution lane.
- `internal/autonomy/runtime_loop.go` — the per-run loop state machine.
- `internal/runtime/task.go`, `runtime/kernel/engine.go` — the task and kernel
  execution owners.
- `internal/ui/operation.go`, `model.go`, `autonomous.go`, `gateway.go`,
  `execution_admission.go`, `intake`, `keys.go`, `interrupt.go` — submission,
  interrupt and admission.
- `internal/runtime/orchestrator/manager.go`,
  `internal/core/workflow/machine.go` — the workflow/phase lifecycle.
- `internal/context/ledger.go`, `internal/engine/layer4/dag.go`,
  `internal/engine/control/workerpool.go`, `internal/session/compaction/runner.go`
  — the queues that *do* exist.

The pre-existing tests are also evidence: the UI already pins a single-lane
"new execution refused while parked" invariant in
`TestInvariant6_NewExecutionIsRefusedAtAdmissionWhileARunIsParked`
(`internal/ui/execution_lifecycle_invariants_test.go:473`).

---

## 2. Phase 1 — does queueing exist?

**No. There is no queue of independent executions anywhere in the runtime.** A
second unit of work is refused at every owner; queued *work* that does exist is
strictly intra-execution sub-work under one execution identity.

### The execution owners and their admission rule

| Owner | What it admits | Second unit while one is live | Source |
|---|---|---|---|
| `autonomy.Driver` | one **run** | `Run` refuses if the loop is non-terminal (active **or** parked): `"a run is already active or parked at a human boundary — resume or abort it first"` | `driver.go:589-591` |
| `ui.model` (autonomous) | one autonomous start | `runAutonomousDriver` refuses while `autonomousActive \|\| autonomousParked()` | `autonomous.go:131-136` |
| `ui.model` (admission) | one execution run | `admitNewExecutionRun` reads the **driver's own boundary** and refuses; `EXECUTION BLOCKED … No new execution started` | `execution_admission.go:155-162`; callers `autonomy_route.go:75`, `intent_dispatch.go:298` |
| `runtime.kernel.Engine` | exactly one **execution** per engine, forever | `Open` refuses: `"engine already holds execution %s"` | `kernel/engine.go:245-247` |
| `runtime.RuntimeEngine` | one durable **task** | bound to one `taskID`; proposals for any other task are rejected | `internal/runtime/engine.go:38-46` |
| `orchestrator.PhaseManager` + `workflow.WorkflowStateMachine` | one workflow/phase lifecycle | single shared SM; parked is a position, not a second lane | `manager.go`, `machine.go` |

### The queues that DO exist (and why they are not execution queues)

| Queue | Owner | What it holds | Cancellation scope |
|---|---|---|---|
| `TaskLedger` sliding window | `internal/context` | plan **tasks** within one `/build` operation | the owning operation's context |
| DAG ready set + worker pool | `internal/engine/layer4`, `internal/engine/control` | DAG **nodes** within one execution | the execution's `ctx` (`gctx`, `errgroup`) |
| compaction `Runner.queue` (bounded 256) | `internal/session/compaction` | background **compaction jobs** | `Close`/drain; jobs are best-effort and not mounted as cancellable work |
| event bus control/telemetry queues | `internal/events` | events | n/a (delivery, not work) |

None of these is an independent execution with its own cancellation identity.
They are all sub-units of one execution and are stopped by that execution's
context. **Per the R6-B instruction, the queue portion stops here: no queue
architecture was invented.**

Classification: **F — missing queue/lifecycle contract**, and deliberately NOT a
bug, because the architecture *promises refusal*, not queue semantics, and the
refusal is already pinned by an existing UI invariant test.

---

## 3. Phase 2 — lifecycle identity

The authoritative identities, and which one each mechanism uses:

| Concept | Authoritative identity | Owner / source |
|---|---|---|
| **execution (lifecycle unit)** | the `autonomy.Driver` run: stable string `runRequestID` = `"run-N"` | `driver.go:659`, `RunID()` `driver.go:1406-1411` |
| **run counter (internal)** | `Driver.runID uint64`; +1 on `Run`/`Resume*` | `driver.go:596-597`, `931`, `973`, `1035`, `1325` |
| **attempt** | `"run-N-attempt-M"` request id | `driver.go:2577` |
| **step** | `RuntimeLoop` `steps`/`attempts` counters | `runtime_loop.go:801-814` |
| **kernel execution** | `kernel.State.ExecutionID` | `kernel/state.go:158-169` |
| **task** | `domaintask.Executable.ID()` + `TaskRuntime` | `internal/runtime/task.go:26-40` |
| **foreground operation** | `operation.ID` = `"op-N"` | `operation.go:90-120`, `145-148` |
| **queued request** | **does not exist** (no execution queue) | — |
| **parked request** | the `RuntimeLoop` boundary + `runRequestID` | `runtime_loop.go:894-901`, `HumanBoundary.RequestID` `runtime_loop.go:455-457` |

Answers to the six Phase-2 questions:

1. **Which identity receives the cancellation request?** The active
   *foreground operation* (`operation.Cancel`, `operation.go:96-99`) for running
   work; the *run* (driver's `runCtx`) receives the withdrawal; the
   *parked run* receives `Driver.Abort`.
2. **Which identity owns the cancellation context?** The per-run
   `context.Context`: `m.activeOp.Ctx → Driver.runCtx` (`driver.go:595`). A
   separate kernel/task path owns `Engine.cancelled` (`kernel/engine.go:45`) and
   `TaskRuntime.canceled` (`task.go:30`).
3. **Which identity is used by RuntimeLoop?** None directly — the loop is
   per-run and receives `ctx` at `Step` (`runtime_loop.go:940-954`).
4. **Which identity is used by Driver?** `d.runID` (internal guard) and
   `d.runRequestID` (stable published identity).
5. **Which identity guards late callbacks?** `d.runID` — the
   `if d.runID != runID` guards at `driver.go:2235` and `2363`, plus the ctx gate
   at `2244` and `RuntimeLoop.Step`.
6. **Which identity survives continuation attempts?** `runRequestID`
   (`RunID()`), which is set once per `Run` and is NOT changed by `Resume*`
   (those bump the internal `runID` only). Proven by
   `TestR6B_ContinuationKeepsItsOriginalExecutionIdentity`.

No new identity was introduced; every one above already existed.

---

## 4. Phase 3 — active + queued scenario

**Not constructible, because B cannot be queued.** The smallest deterministic
scenario is therefore the truthful one:

```text
Execution A → RUNNING
Execution B → submission REFUSED (not queued)

cancel A
  → A → CANCELLED (RuntimeAborted / "context cancelled")
  → B was never admitted; nothing inherited A's identity, context or terminal
```

Proven by `TestR6B_NoQueue_SecondExecutionIsRefusedWhileActiveOrParked`
(both the `active` and `parked` sub-cases) and by the existing UI
`TestInvariant6_…`.

The converse — "while A remains active" — is the same refusal: there is no
second unit to cancel.

---

## 5. Phase 4 — parked execution

A parked run is a live, resumable execution holding a `HumanBoundary`; it is
**not** terminal. The only way to cancel it is `Driver.Abort`
(`driver.go:831-855`), and the UI routes it there explicitly
(`operation.go:328-334`).

```text
RUNNING  → cancel  : withdraw runCtx   → RuntimeAborted / "context cancelled"
                     (attempt: execution.finished outcome=cancelled)
PARKED   → abort   : Driver.Abort      → RuntimeAborted / "aborted by operator: …"
                     (event: autonomous.aborted; no provider call)
```

These are **different transitions**, and R6-B preserves the distinction. The
parked abort is terminal (`IsTerminal()` true), `Parked()` becomes false,
`ResumeApprove` is refused, and a fresh `Run` is then legal — proven by
`TestR6B_ParkedAbortIsTerminalNonResumableAndDistinctFromRunningCancel`. The
workspace is byte-identical after the abort (abort is not a mutation), and the
`autonomous.aborted` event is observed.

Truthfulness requirement — "a parked execution must not silently resume after
abort" — holds: the Run's context was already returned to the caller, the loop
is terminal, and the resume entry points refuse.

---

## 6. Phase 5 — cancellation isolation (tests A–E, in their truthful form)

Because independent queues do not exist, the required tests are realised with
two genuinely independent lifecycle owners (two drivers / two engines) and with
the parked/continuation cases. All deterministic; no sleeps except a bounded
deadline check that a context did **not** fire.

| # | Requirement | Test | Result |
|---|---|---|---|
| A | A active, B (would-be queued) — cancel A leaves B unaffected | `TestR6B_NoQueue_SecondExecutionIsRefusedWhileActiveOrParked` + `TestR6B_CancellationIsExecutionScoped` | PASS — B refused; and when B is a genuine independent execution, cancelling A leaves B `completed`, its mutation committed, its workspace intact |
| B | cancel B while A active | `TestR6B_CancellationIsExecutionScoped` (symmetric) | PASS — B's cancellation does not touch A |
| C | A parked, abort A; A terminal/non-resumable, B unaffected | `TestR6B_ParkedAbortIsTerminalNonResumableAndDistinctFromRunningCancel` + `TestR6B_UI_ParkedCtrlCTargetsTheParkedRunOnly` | PASS — A terminal, resume refused; an unrelated context stays live |
| D | A cancelled, late callback from A must not mutate B | `TestR6B_LateDecisionCannotCrossIntoTheNextExecution` | PASS — late `ResumeApprove` refused; B's terminal + workspace unchanged |
| E | A continuation; cancel A does not alter B | `TestR6B_ContinuationKeepsItsOriginalExecutionIdentity` | PASS — continuation keeps `run-N`; unrelated execution unaffected |

The UI half of A–C is additionally pinned by the pre-existing
`TestInvariant6_…` (a new execution is refused while a run is parked) and by the
autonomous runner's own guard.

---

## 7. Phase 6 — cross-execution callback safety

At the runtime layer this is structurally safe, and provable:

- The driver executes **synchronously on one goroutine per run**. The provider
  and capability callbacks the executor invokes (`StreamCallback`, mutation
  results) run inline on that goroutine and cannot outlive the `Run` call.
- Two independent guards would reject a stale result anyway:
  1. the **inter-step cancellation gate** (`driver.go:2244`);
  2. the **run-identity guard** `if d.runID != runID` (`driver.go:2235` and
     `2363`);
  3. `RuntimeLoop.Step`'s ctx check (`runtime_loop.go:947`).
- A late *human decision* (a resume for a terminated run) is refused: it is not
  applied to the next execution. Proven by
  `TestR6B_LateDecisionCannotCrossIntoTheNextExecution` — after A is aborted and
  B has completed on the same driver, `ResumeApprove` is refused and B's
  terminal state and workspace are unchanged.

The R6-A negative tests remain the responsible evidence for a hostile provider
that ignores `ctx`: a late provider/stream/mutation success cannot become
`PROVEN`. R6-B adds the *cross-execution* half.

---

## 8. Phase 7 — Ctrl+C / Esc / Ctrl+D / SIGINT / Driver.Abort semantics

Traced from `internal/ui/update.go`, `keys.go`, `interrupt.go`, `operation.go`,
`model.go`. With multiple lifecycle units the scope is:

| Input | Production transition | Which execution? | Queued? | Parked? | Process exit? |
|---|---|---|---|---|---|
| **Esc** | two-press window → `MsgCancelStream` → `handleEmergencyInterrupt("escape")`. Consumed by a modal/boundary first (`isInterruptModalActive`). | the active foreground operation | n/a (none) | never aborts a parked run by itself | no |
| **Ctrl+C** | `handleCtrlC`: parked autonomous run → `abortAutonomousRun` (driver `Abort`); else active op/workflow → `handleEmergencyInterrupt` | *parked run first*, otherwise the active operation | n/a | yes — aborts the parked run | no (first press) |
| **Ctrl+C (2nd, within grace)** | `hardExit130` | the whole process | — | — | **yes, status 130** |
| **Ctrl+D** | only in `StateProcessing` → `handleEmergencyInterrupt("ctrl-d")` | the active operation | n/a | incidental | no |
| **OS SIGINT/SIGTERM** | `tea.InterruptMsg` / `interruptSignalMsg` → same `handleCtrlC` protocol | same as Ctrl+C | n/a | same | no (first) |
| **`Driver.Abort(reason)`** | parked (or active) run → `RuntimeAborted` + `autonomous.aborted` | exactly the driver's current run | n/a | the only parked-cancel route | no |

**The distinction is preserved**, and it is a precedence, not a queue policy:

```text
a parked run is the first thing Ctrl+C/SIGINT targets
  → aborting it does NOT withdraw an unrelated execution's context
    (pinned by TestR6B_UI_ParkedCtrlCTargetsTheParkedRunOnly)
Esc never fires through a boundary/modal
Ctrl+C twice = process exit 130
```

---

## 9. Required invariants

| # | Invariant | Holds? | Evidence |
|---|---|---|---|
| 1 | Cancellation is execution-scoped | **yes** | per-run `runCtx` (`driver.go:595`); per-engine `cancelled` (`kernel/engine.go:45`); `TestR6B_CancellationIsExecutionScoped`, `TestR6B_KernelCancelIsExecutionScoped` |
| 2 | One execution's context cannot cancel another | **yes** | same tests; two independent owners |
| 3 | Late callbacks cannot cross execution boundaries | **yes** | synchronous driver + run-identity guard (`driver.go:2235,2363`) + `Step` ctx gate; `TestR6B_LateDecisionCannotCrossIntoTheNextExecution` |
| 4 | Cancelling active work does not silently cancel queued work | **yes** | no queue exists; a second unit is refused (`TestR6B_NoQueue_…`); the UI admission refusal is pinned by `TestInvariant6_…` |
| 5 | Cancelling queued/parked work does not interrupt active work | **yes** | aborting a parked run leaves an unrelated live context untouched (`TestR6B_UI_ParkedCtrlCTargetsTheParkedRunOnly`) |
| 6 | Parked abort cannot silently resume the parked execution | **yes** | `TestR6B_ParkedAbortIsTerminalNonResumableAndDistinctFromRunningCancel` |
| 7 | Continuation attempts remain associated with their original execution | **yes** | `RunID()` is stable across `Resume*` (`TestR6B_ContinuationKeepsItsOriginalExecutionIdentity`) |
| 8 | Terminal execution state is immutable wrt unrelated executions | **yes** | `TestR6B_LateDecisionCannotCrossIntoTheNextExecution`, `TestR6B_KernelTerminalStateIsImmutableAcrossExecutions` |

---

## 10. Findings and classification

| ID | Finding | Classification |
|---|---|---|
| **F1** | The runtime has **no queue of independent executions**. A second unit is refused at the driver (`driver.go:589`), the UI admission (`execution_admission.go:155`), and the kernel (`kernel/engine.go:245`). Queued work that exists is intra-execution sub-work. | **F — missing queue/lifecycle contract** (not a bug: refusal, not queueing, is the promise) |
| **F2** | Parked abort is correct and *distinct* from running cancel: `Driver.Abort` → `autonomous.aborted` / `"aborted by operator: …"` / terminal / non-resumable; running cancel → `"context cancelled"` / `OutcomeCancelled`. | **A — existing behavior correct** |
| **F3** | Cancellation is scoped by per-run `context.Context` (autonomy), a per-engine atomic flag (kernel), and a per-task runtime (task). No global mutable cancellation state participates in execution cancellation. | **A — existing behavior correct** |
| **F4** | Late provider/capability/human-decision callbacks cannot cross execution boundaries: the driver is synchronous and carries two run-identity guards plus the loop's ctx gate. | **A — existing behavior correct** |
| **F5** | `handleEmergencyInterrupt` performs **model/process-scoped** teardown: `cancelAllBackgroundContexts()` (`model.go:3818`), `KillAllOrphans()` (`model.go:3827`, `runner.go:374`), and a shared workflow-SM reset to Idle (`model.go:3887-3895`). These are not execution-scoped side effects. Today they cannot cross an execution boundary because admission refuses a second run — but they are the "global cancellation side effect" shape and would be incorrect under any future multi-lane runtime. | **B — observability/generality limitation** (no proven harm today) |
| **F6** | `Driver.Resume*` (approve/reject/clarify/proposal) carry **no execution-identity parameter**; they act on the driver's current run. Safe under single-lane; a queue would require identity-parameterised decisions. | **B — limitation, linked to F1** |
| **F7** | The UI's autonomous/Ctrl+C precedence targets a parked run before an active operation (`operation.go:328-334`). Under single-lane admission the two cannot coexist, so this is a *precedence*, not a defect. | **A — existing behavior correct (single-lane)** |
| **F8** | `KillOrphanedByContext` (scoped) exists (`runner.go:76`) but the interrupt path uses `KillAllOrphans` (global). | **B — folded into F5** |

No finding is classified **C (lifecycle semantics bug)**.

### Why F1 is F, not C

R6-B's classification rule: *"Do not classify missing queue functionality as a
bug unless the architecture already promises queue semantics."* The architecture
promises the opposite — every owner explicitly refuses a concurrent unit, and the
UI has a dedicated, tested `EXECUTION BLOCKED` admission. Adding a queue would be
a redesign the brief forbids.

---

## 11. The first incorrect transition and the smallest correction

**No incorrect transition was found.** Therefore:

- no production code was changed;
- no regression was needed;
- the R6-B additions are deterministic, black-box tests only.

If a future lifecycle phase decides cancellation must remain execution-scoped
even after a second lane is admitted, the smallest boundary is already visible:

```text
owner: internal/ui handleEmergencyInterrupt
first scoped step: replace process-wide teardown with the active operation's
  own context tree (cancel the op's derived contexts) and Runner.KillOrphans()
  (the existing context-scoped kill, runner.go:76) instead of KillAllOrphans().
```

This is a **recommendation, not an applied fix**: it changes no current safety
property and would risk leaving orphans if applied without a multi-lane reason.

---

## 12. Evidence index

Deterministic tests added by R6-B (all PASS; no sleeps, no model):

```
internal/runtime/autonomy/r6b_lifecycle_isolation_test.go
  TestR6B_NoQueue_SecondExecutionIsRefusedWhileActiveOrParked   (Inv 4,5)
  TestR6B_CancellationIsExecutionScoped                         (Inv 1,2,8)
  TestR6B_ParkedAbortIsTerminalNonResumableAndDistinctFromRunningCancel (Inv 6)
  TestR6B_LateDecisionCannotCrossIntoTheNextExecution           (Inv 3,8)
  TestR6B_ContinuationKeepsItsOriginalExecutionIdentity         (Inv 7)

runtime/kernel/r6b_engine_isolation_test.go
  TestR6B_KernelCancelIsExecutionScoped                         (Inv 1,2)
  TestR6B_KernelAdmitsExactlyOneExecution                       (F1)
  TestR6B_KernelTerminalStateIsImmutableAcrossExecutions        (Inv 8)

internal/ui/r6b_lifecycle_isolation_test.go
  TestR6B_UI_SecondAutonomousStartRefusedWhileParked            (Inv 4)
  TestR6B_UI_ParkedCtrlCTargetsTheParkedRunOnly                 (Inv 5)
```

Pre-existing evidence re-cited (not modified):

```
internal/ui/execution_lifecycle_invariants_test.go:473
  TestInvariant6_NewExecutionIsRefusedAtAdmissionWhileARunIsParked
internal/runtime/autonomy/r6_cancellation_test.go   (R6-A, hostile late callbacks)
internal/execution/r6_cancellation_test.go          (R6-A, mutation/stream boundaries)
```

---

## 13. The ten required questions

1. **Does a real queue exist?** No. There is no queue of independent
   executions; a second unit is refused at the driver, the UI admission and the
   kernel. Queues that exist (plan tasks, DAG nodes, compaction jobs, bus
   queues) are intra-execution sub-work.
2. **What is the authoritative execution identity?** The driver run:
   `runRequestID` = `"run-N"` (`Driver.RunID()`), with an internal `runID`
   counter, `"run-N-attempt-M"` attempt ids, and a per-run
   `RuntimeLoop`/`context.Context`. Kernel uses `State.ExecutionID`; tasks use
   `TaskRuntime`.
3. **How is cancellation scoped?** By the per-run `context.Context`
   (`activeOp.Ctx → d.runCtx`); the kernel by a per-engine `cancelled` flag; a
   task by `TaskRuntime.canceled`. No global cancellation state participates.
4. **Can active and queued work interfere?** No queue exists to interfere; a
   second execution is refused.
5. **Can parked work be aborted safely?** Yes — `Driver.Abort` terminates it
   (`autonomous.aborted`, `"aborted by operator: …"`), non-resumable, workspace
   untouched, fresh run allowed.
6. **Can late callbacks cross execution boundaries?** No. The driver is
   synchronous and guards with `d.runID != runID` and the ctx gate; a late human
   decision is refused against the next run.
7. **Esc/Ctrl+C/Ctrl+D/SIGINT semantics?** Esc = two-press cancel of the active
   operation (consumed by a modal first); Ctrl+C = abort a parked run first,
   else cancel the active operation, a second Ctrl+C exits 130; Ctrl+D =
   interrupt while processing; SIGINT/SIGTERM = the Ctrl+C protocol;
   `Driver.Abort` = the parked-run cancel route.
8. **First incorrect transition?** None found.
9. **Smallest correction?** None required. If execution-scoped teardown is ever
   needed for a second lane, the boundary is `handleEmergencyInterrupt`
   (use the active operation's context tree and `Runner.KillOrphans()` instead of
   `KillAllOrphans()`); recorded as a recommendation.
10. **Evidence proving isolation?** The ten R6-B deterministic tests in §12,
    the pre-existing UI single-lane invariant, and the untouched R6-A hostile
    late-callback tests.

---

## 14. Verification

```
go test -count=1 ./...        # PASS — exit 0; all packages
go test -race -count=1 ./...  # PASS — exit 0; 211 packages ok; no data race, no panic
go vet ./...                  # PASS — no findings
```

R1–R6-A tests remain green (full-suite run below). `git diff` scope: **three new
deterministic test files, this report, and the state handoff.** No production
file was changed.

Exact command output is recorded in `EXECUTION_FORENSICS_STATE.md` §R6-B.

---

## 15. Explicitly not done (scope)

No queue abstraction; no crash recovery; no WAL; no resume; no disconnect
recovery; no provider-transport change; no cancellation redesign; no
`RuntimeLoop` redesign; no progress/continuation/target-authority change; no
global mutable cancellation state. R6-A's contract is unchanged.
`RuntimeCancelled` and `cancel.requested`/`cancel.propagated` were still not
added (R6-A F2 remains an open observability decision, untouched here).
