# R6-A — CANCELLATION LIFECYCLE FORENSICS

**Status: PROVEN, with one lifecycle-semantics defect found and fixed.**

R1–R5.1 are treated as PROVEN and are not reopened. This report answers only one
question:

> When the human cancels an active execution, can the runtime guarantee that the
> execution cannot continue mutating or complete successfully after cancellation?

The answer is **yes**, with one correction (F1 below). Cancellation is a real
runtime-owned boundary: the human request becomes a context withdrawal, the
driver owns two linearization points, the terminal loop state is
`RuntimeAborted` and can never become `PROVEN`, a late provider/capability result
is consumed into an already-terminal (or about-to-terminate) loop, and a
committed mutation is never rolled back merely because a cancellation happened.

**Branch:** `fix/runtime` (continues from the recorded R5.1 state).
**Read `EXECUTION_FORENSICS.md` / `EXECUTION_FORENSICS_STATE.md` first.**

```
# deterministic (no model)
go test ./internal/execution/        -run TestR6_ -v
go test ./internal/runtime/autonomy/ -run TestR6_ -v

# live, real local model, opt-in
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r6/ -v -timeout 500s
```

---

## 0. Answer in one line

```text
RUNNING
  → cancel.requested   (human: Esc×2 / Ctrl+C / OS SIGINT; or driver.Abort when parked)
  → cancel.propagated  (context.WithCancel through the driver into the executor, provider,
                        shell and the mutation boundary)
  → CANCELLED          (loop terminal = RuntimeAborted, reason "context cancelled";
                        attempt terminal = OutcomeCancelled)
```

Never:

```text
CANCELLED → late callback → capability execution → mutation → PROVEN
```

A late provider result **is** allowed to be consumed into the loop for one
iteration, but the loop is linearized to abort at the top-of-loop gate or at
`RuntimeLoop.Step`, and the completion authority never runs after termination.
The deterministic negative test `TestR6_LateProviderSuccessAfterCancelCannotComplete`
proves it; the live test observes a real cancelled provider call and a
zero-delta workspace.

---

## 1. The core question and the invariant

```text
activity       ≠ continuation
continuation   ≠ progress
completion     requires PROVEN evidence
stall          ≠ success
cancel         ≠ rollback
```

R6-A adds:

```text
cancel.requested   ≠ cancel.propagated
cancel.propagated  ≠ execution finalized as cancelled
cancelled          ≠ PROVEN, ever
committed mutation ≠ rolled back because a cancel happened
```

---

## 2. The real call graph (human → provider → mutation)

Traced through production calls, not names.

```text
internal/ui/update.go  Update()
  ├─ tea.KeyCtrlC ──────────────► model.handleCtrlC()                 (operation.go:321)
  │                                 ├─ parked autonomous → abortAutonomousRun() (autonomous.go:432)
  │                                 │       └─ autonomousDriver.Abort(reason)   (driver.go:831)
  │                                 └─ active op/workflow → handleEmergencyInterrupt() (model.go:3789)
  ├─ tea.KeyEsc (executing, no modal) ─► handleInterruptEsc()          (interrupt.go:111)
  │       first Esc arms 1.5s; 2nd Esc → MsgCancelStream → handleEmergencyInterrupt()
  ├─ tea.KeyCtrlD (StateProcessing only) ─► handleEmergencyInterrupt()
  ├─ tea.InterruptMsg / interruptSignalMsg (OS SIGINT/SIGTERM) ─► handleCtrlC()
  └─ modal/boundary owns Esc ─► modal reject/cancel (NOT a run cancel)

handleEmergencyInterrupt()  (model.go:3789)
  ├─ m.activeOp.Cancel()                     ← operation.Cancel (operation.go:99)
  ├─ m.cancelAllBackgroundContexts()
  ├─ m.streamCancel() / m.shellCancel()
  └─ execution.KillAllOrphans()

runAutonomousDriver()  (autonomous.go:127)
  ctx := m.operationContext()                ← m.activeOp.Ctx
  m.autonomousDriver.Run(ctx, objective)
        │
        ▼
Driver.Run(ctx, objective)                    (internal/runtime/autonomy/driver.go:575)
  d.runCtx, d.runCancel = context.WithCancel(ctx)          (driver.go:595)
        │
        ▼
Driver.observeAndRun(d.runCtx, runID)         (driver.go:2120)
  ├─ inter-step gate:  select { case <-ctx.Done(): terminateAbort(...,"context cancelled") }  (2244)
  └─ execution:        d.adapter.Execute(ctx, d.req)        (2360)
        │
        ▼
ExecutorAdapter.Execute(ctx, req)             (adapter.go:383)
  a.executor.Execute(ctx, execReq)            (adapter.go:588)
        │
        ▼
RuntimeExecutor.Execute(ctx, req)             (executor.go:1586)
  ├─ provider:  invokeMutation / invokeReadOnly / invokeStream (ctx threaded)
  │     └─ providers.OllamaProvider.Execute / ExecuteStream   (ollama.go:243 / 366)
  │           http.NewRequestWithContext(reqCtx, ...)         (ollama.go:246 / 374)
  └─ mutation:  RuntimeExecutor.Approve(ctx, patchID)         (executor.go:2418)
        ├─ applyCtx, cancel := context.WithTimeout(ctx, 90s)  (2587)
        └─ PatchManager.ApplyContext(applyCtx, p)             (2591 → patch.go:1066)
              └─ kernelbridge.Apply(ctx, ...)                 (patch.go:565)
        │
        ▼
RuntimeLoop.Step(ctx, decision)               (internal/autonomy/runtime_loop.go:940)
  ctx.Done() → terminate(LoopAbort, RuntimeAborted, "context cancelled", FailurePermanent)  (947-954)
```

Two cancellation linearization points own the ordering:

1. the **inter-step gate** at the top of `observeAndRun` (driver.go:2244);
2. **`RuntimeLoop.Step`** (runtime_loop.go:947), which also gates the completion
   and continuation decisions.

There is no third path. A cancellation that is observed at either point produces
the same terminal state.

---

## 3. Ownership map (exact owner per responsibility)

| # | Responsibility | Owner | Location |
|---|---|---|---|
| 1 | **cancellation request** | UI `operation.Cancel` (Esc×2 / Ctrl+C / Ctrl+D / OS signal); `Driver.Abort` for a *parked* run | `internal/ui/operation.go`, `model.go:3789`, `autonomous.go:432`; `driver.go:831` |
| 2 | **cancellation state** | the withdrawn `context.Context` (`m.activeOp.Ctx` → `d.runCtx`). Separate kernel/task path: `TaskRuntime.canceled atomic.Bool` | `operation.go:99,154`; `driver.go:595`; `runtime/task.go:30,61` |
| 3 | **cancellation propagation** | context cancellation: `activeOp.Ctx → d.runCtx → adapter → executor → provider/shell/mutation` | `driver.go:595,2244`; `adapter.go:588`; `executor.go:1586` |
| 4 | **provider cancellation** | HTTP request context (`http.NewRequestWithContext`); streaming loop checks `ctx.Err()` and a teardown goroutine closes the body on `ctx.Done()` | `providers/ollama.go:246,374,4919-4927,4707-4714` |
| 5 | **capability cancellation** | shell: `exec.CommandContext`; command runner: `exec.CommandContext`; capability-tool runner **ignores ctx** (read-only, bounded) | `infrastructure/capabilities/exeshell.go:52`; `execution/runner.go:247`; `execution/capability_tools.go:566` |
| 6 | **mutation boundary** | `RuntimeExecutor.Approve` → `MutationSet` transaction → `PatchManager.ApplyContext` (fast-path `ctx.Err()` check) → `kernelbridge.Apply(ctx)`; any apply error → `MutationSet.RollbackTo` | `executor.go:2418,2587,2591`; `patch.go:1066,1076` |
| 7 | **final execution state** | `RuntimeLoop` terminal state; projected by `Driver.State()` | `runtime_loop.go:940-993,1098-1105`; `driver.go:1351` |
| 8 | **completion authority** | `ObjectiveCompletionAuthority` / `authorizeObjectiveCompletion` + `authorizeBehavioralCompletion` — **downgrade-only** | `runtime/autonomy/objective_completion.go`, `behavior.go`; `driver.go:2265-2301` |

No ownership above is inferred from a function name; each is the function that
performs the transition.

---

## 4. Cancellation mechanisms

For each mechanism: who writes it, who reads it, when it is checked, what work
it actually stops.

| Mechanism | Who writes | Who reads | When checked | What it actually stops |
|---|---|---|---|---|
| **`context.Context`** (the authoritative one) | UI `operation.Cancel`; `Driver.Abort` (`d.runCancel`) | driver, adapter, executor, provider, `PatchManager`, `Runner`, `RuntimeLoop` | inter-step gate (driver.go:2244); `RuntimeLoop.Step` (runtime_loop.go:947); stream loop each iteration (executor.go:4920); `ApplyContext` fast-path (patch.go:1076); `exec.CommandContext` | in-flight provider HTTP request; streaming body read; shell/child process; the next execution-bound decision (attempt/continuation); the open mutation apply |
| **atomic flag** | `TaskRuntime.Cancel`; `runtime/kernel.Engine.Cancel` | `TaskRuntime.IsCanceled`; `kernel.Engine.Cancelled` | task classification (`runtime/task.go:216`); kernel recovery | the kernel/task lifecycle. **Not read by the autonomy loop.** |
| **driver state** | `Driver` (`d.runCtx`, `d.loop`) | `Driver.State`, `Boundary`, `Termination` | on every loop iteration | nothing by itself; it *is* the loop position |
| **runtime state** | `RuntimeLoop` | `Driver`, projections | `IsTerminal()` gate on `Step` | further loop transitions |
| **provider abort** | provider transport | HTTP client | transport | the HTTP request/stream |
| **process signal** | OS | UI root signal bridge | `interruptSignalMsg` / `tea.InterruptMsg` | routed through the *same* Ctrl+C protocol; `KillAllOrphans` kills registered subprocesses |
| **TUI command** | `MsgCancelStream`, `cancelStreamCmd`, `abortAutonomousRun` | `Update`, driver | event loop | funnels into `handleEmergencyInterrupt` / `Driver.Abort` |
| **channel** | none for cancellation | — | — | The only channels are event/data channels (`execStreamCh`), which are *discarded* after cancel. No cancellation channel exists. |

**Proven per item, not assumed:**

- **Provider HTTP** — `http.NewRequestWithContext` (ollama.go:246,374). The live
  R6 run shows the second real call erroring `cancelled`, 0 tokens, 1 ms.
- **Streaming** — the executor's stream loop checks `ctx.Err()` before every
  read (executor.go:4920) and a goroutine closes the body on `ctx.Done()`
  (executor.go:4707-4714). A late chunk returned from the reader is accumulated
  into local `content`, then discarded by the cancellation branch. Proven by
  `TestR6_Streaming_LateChunkAfterCancelCannotResurrectExecution`.
- **Shell / child process** — `exec.CommandContext` (exeshell.go:52,
  runner.go:247), process-group aware.
- **Capability execution** — `CapabilityToolRunner.execute` takes `_ context.Context`
  (capability_tools.go:566) and its operations are bounded synchronous read-only
  file reads (`readFile`, `listDirectory`, `searchCodebase`, `symbolLookup`).
  Cancellation does **not** reach them; there is no mutation on that seam.
  Classified **E** below.
- **Mutation** — `ApplyContext` fast-path `ctx.Err()` (patch.go:1076) and
  `kernelbridge.Apply(ctx,...)`. Proven by
  `TestR6_MutationBoundary_CancelBeforeAuthorizationLeavesZeroDelta`.
- **Next attempt / continuation** — `RuntimeLoop.Step` ctx check
  (runtime_loop.go:947). Proven by
  `TestR6_CancelAtContinuationBoundaryDoesNotStartNextAttempt`.

---

## 5. Authoritative lifecycle states (existing types first)

IZEN **already** owns a typed cancellation state at the **execution-attempt**
level. R6-A reuses it and does **not** invent a parallel vocabulary:

| Level | Cancellation state | Where |
|---|---|---|
| execution attempt outcome | `OutcomeCancelled` = `"cancelled"` (canonical mutation outcome) | `internal/execution` |
| execution graph phase | `PhaseCancelled` (terminal) | `internal/execution/graph/graph.go:646` |
| lifecycle event | `execution.finished(success=false, outcome="cancelled")` — this **is** `execution.cancelled`; no duplicate event type is created | `events.NewExecutionFinished` (events.go:2078) |
| autonomous parked-run abort | `autonomous.aborted` | `events.NewAutonomousAborted` (events.go:2482) |
| loop terminal reason | `RuntimeAborted` + `FailurePermanent` + reason `"context cancelled"` / `"aborted by operator: …"` | `runtime_loop.go:947-954`, `driver.go:3104` |

**What is missing:** the autonomous **loop** has no distinct `RuntimeCancelled`
position. Cancellation, operator abort, preflight refusal and bound exhaustion
all terminate at `RuntimeAborted`. The terminal **reason string** distinguishes
them, but the typed state does not. This is recorded as **observation gap (B)**,
not a resurrection path: the abort is terminal, `IsTerminal()` is true, and the
completion authority never runs afterward.

R6-A deliberately does **not** add a `RuntimeCancelled` state or a
`cancel.requested` / `cancel.propagated` event pair. That would be a new
lifecycle vocabulary layered on top of one that already carries the truth in
`OutcomeCancelled` + the terminal reason, and the experiment is instructed not to
introduce a lifecycle framework before the existing one is traced. The
recommendation is recorded in §12.

---

## 6. Deterministic cancellation benchmark

The smallest existing seam that allows deterministic cancellation is the
**driver's provider boundary** (`ExecutorAdapter.Execute`). A provider that
signals its start on a channel and blocks on `ctx.Done()` gives exact
coordination with no sleeps. All deterministic tests are in
`internal/runtime/autonomy/r6_cancellation_test.go` and
`internal/execution/r6_cancellation_test.go`.

Representative event trace
(`TestR6_CancelWhileProviderActiveAbortsAndNeverMutates`, real executor, real
graph/events):

```text
execution.started
target.resolved            index.html
context.prepared           channels=[target:index.html]
model.invoked
                                   ← provider is blocked; cancel() fires
execution.finished         success=false outcome=cancelled
loop.transition  executing   → verifying    "execution consumed: cancelled"
loop.transition  verifying   → interpreting "verification consumed"
loop.transition  interpreting→ aborted      "context cancelled"
```

Terminal: `RuntimeAborted / FailurePermanent / "context cancelled"`.
Workspace: `note.txt` byte-identical. Pending candidates: none.

---

## 7. Required negative / boundary / race tests

All deterministic; no arbitrary sleeps (channel/event coordination only).

| # | Requirement | Test | Result |
|---|---|---|---|
| 1 | cancel while provider is active | `TestR6_CancelWhileProviderActiveAbortsAndNeverMutates` | PASS — aborted, zero delta, `execution.finished(success=false,"cancelled")` |
| 2 | cancel before capability execution | `TestR6_CancelBeforeExecutionNeverTouchesExecutor` | PASS — 0 provider calls, aborted |
| 3 | cancel after mutation | `TestR6_MutationBoundary_CommittedMutationIsNotRolledBackByCancellation`, `TestR6_CommittedMutationSurvivesCancellation` | PASS — committed bytes survive |
| 4 | late provider completion after cancel | `TestR6_LateProviderSuccessAfterCancelCannotComplete` | PASS — late success → aborted, never completed |
| 5 | late capability completion after cancel | `TestR6_LateMutationResultCannotBeAppliedAfterCancel` | PASS — late mutating result → aborted; `ResumeApprove` refuses; zero delta |
| 6 | cancel during streaming | `TestR6_Streaming_LateChunkAfterCancelCannotResurrectExecution` | PASS — late chunk discarded, `OutcomeCancelled`, no patch |
| 7 | cancel during continuation boundary | `TestR6_CancelAtContinuationBoundaryDoesNotStartNextAttempt` | PASS — provider calls = 1 |
| 8 | completion/cancel race | `TestR6_CompletionCancelRaceIsDeterministic` | PASS — cancellation wins at `Step` |
| — | terminal immutability (reverse ordering) | `TestR6_TerminalCompletionIsImmutable` | PASS — completed is frozen |
| — | mutation boundary A (cancel before authorization) | `TestR6_MutationBoundary_CancelBeforeAuthorizationLeavesZeroDelta` | PASS — 0 delta, **`OutcomeCancelled`** (was `apply_failed`; see F1) |

### The required negative test, in detail

`TestR6_LateProviderSuccessAfterCancelCannotComplete` uses a provider that
**ignores** `ctx` and returns a successful response after being released:

```text
run starts → provider blocked (observed on a channel)
cancel()          ← RUNNING → CANCEL
release provider  ← late SUCCESS arrives after the cancel
provider success is consumed by the loop (RunID unchanged; d.obs updated)
loop top: select { case <-ctx.Done(): terminateAbort("context cancelled") }
terminal: RuntimeAborted   ← NOT Completed, NOT PROVEN
```

This proves Invariant 2 and Invariant 5 deterministically. It also corrects the
imprecise claim in the pre-existing `TestDriver_ProviderIgnoresCancellation`
comment: the guard is **not** the `d.runID` check (which only changes on a new
run/resume); it is the driver's context gate plus `RuntimeLoop.Step`.

### Mutation boundary

- **A — cancel before authorization:** approve a held candidate under a
  withdrawn context. `ApplyContext` fast-path returns before any write;
  `MutationSet.RollbackTo` closes the transaction; delta = 0.
- **B — mutation already committed:** approve normally (committed), then cancel.
  The bytes remain; evidence is not erased. `cancel != rollback`.

### Completion race

The only authoritative ordering is: whichever of the two ctx checks observes the
cancellation first wins. If it is observed at `RuntimeLoop.Step`, the proposed
`LoopComplete` is never applied, and the terminal state is `Aborted`. The
converse (completion already committed before the cancel is observed) leaves the
run `Completed`, and `Step` refuses further transitions.

### Continuation race

R4 continuation is runtime-owned. At the driver boundary a selected
execution-bound decision is dispatched through `RuntimeLoop.Step`, which checks
`ctx` first — `TestR6_CancelAtContinuationBoundaryDoesNotStartNextAttempt` shows
the second attempt never starts. Inside the executor,
`invokeArtifactBoundedStep` (artifact_step.go:195) does not itself check `ctx`
between steps; it relies on the next provider invocation honoring it. That is
safe because the provider request is built with the context (D) and because the
driver's `Step` gate is the authority for whether a *new* attempt is admitted.
Classified **A/D**, not a defect.

### Streaming race

`TestR6_Streaming_LateChunkAfterCancelCannotResurrectExecution`: the stream
emits a late chunk after `cancel()`; the executor returns `OutcomeCancelled`,
never reaches the approval gate, and leaves the workspace unchanged. UI data
(`streamCb` → `execStreamCh`) is dropped when the channel is cleared; it never
carries authority.

---

## 8. Queued execution

**There is no queued-execution lifecycle contract in the autonomous runtime.**
A second start is *refused*, not queued:

- `Driver.Run` refuses when a run is active or parked
  (`driver.go:589-591`);
- `runAutonomousDriver` refuses when a run is active or parked
  (`autonomous.go:131-136`) and when the standard executor is busy
  (`autonomous.go:138-143`).

Therefore the current semantics are:

```text
cancel affects exactly the ONE active-or-parked execution
there is no queued execution A / B
a second start while active/parked is rejected
"interrupt" (Esc×2 / Ctrl+C) and "process exit" (2nd Ctrl+C → 130) are distinct
```

Recorded at the boundary and stopped, per the R6-A instruction. No queue
architecture was invented.

---

## 9. UI semantics — what each input actually requests

Traced from `internal/ui/update.go` and its handlers.

| Input | Production transition requested |
|---|---|
| **Esc** (executing, no modal) | first press *arms* a 1.5 s window; second press emits `MsgCancelStream` → `handleEmergencyInterrupt("escape")` → cancels `activeOp.Ctx` + background contexts. It is **not** an immediate cancel. |
| **Esc** (modal/boundary active) | consumed by the modal: approval = reject, proposal = `ProposalCancel`, permission = deny. It never reaches the run-cancel path (`isInterruptModalActive`). |
| **Ctrl+C** | `handleCtrlC`: if a parked autonomous run → `abortAutonomousRun` → `Driver.Abort`; else if an active op/workflow → `handleEmergencyInterrupt("ctrl-c")` → cancel context. It arms a grace window. |
| **Ctrl+C** (second, within grace) | `hardExit130()` — process exit with status 130. |
| **Ctrl+D** | only while `StateProcessing` → `handleEmergencyInterrupt("ctrl-d")`. |
| **OS SIGINT / SIGTERM** | `tea.InterruptMsg` / `interruptSignalMsg` → the same `handleCtrlC` protocol. |

So the assumption in the brief is **not** exactly the production behavior:

```text
Esc      = two-press cancel (or a modal reject), not an immediate cancel
Ctrl+C   = cancel first; a SECOND Ctrl+C exits (status 130)
```

The UI, the operation lifecycle and the driver loop all finalize through one
terminal path; the driver's `autonomousRunMsg` is the canonical cleanup for an
autonomous run (`model.go:3837-3846`).

---

## 10. Required forensic events

The existing event model already represents the four required concepts; no
duplicate event type was created.

| Required concept | Existing representation |
|---|---|
| `cancel.requested` | **observability gap** — no event marks the instant the human requested cancellation (see §12, B) |
| `cancel.propagated` | `loop.transition` `executing → verifying` with `execution consumed: cancelled`, plus `execution.finished(success=false, outcome="cancelled")` |
| `execution.cancelled` | `execution.finished` payload `Success=false, Outcome="cancelled"` (attempt terminal); `autonomous.aborted` for a parked-run `Abort` |
| `execution.finished` | `EventExecutionFinished` (events.go:123, `ExecutionFinishedPayload`) |

Fields actually available to capture on cancellation using existing payloads:
`execution_id`/`request_id`, run id, attempt/contract id, `reason`, current loop
state (via `loop.transition`), active capability (via `capability.execute`
`stage.completed`), provider state (via `provider.*` events),
mutation state (via `execution.mutation.*`), objective state (via
`objective.evaluated`). Fields that do not exist are reported `UNKNOWN`, never
invented.

---

## 11. Required invariants

| # | Invariant | Holds? | Evidence |
|---|---|---|---|
| 1 | once authoritative state is cancelled, **no new capability execution** | **yes** | `RuntimeLoop.Step` ctx gate; `TestR6_CancelAtContinuationBoundaryDoesNotStartNextAttempt`, `TestR6_CancelBeforeExecutionNeverTouchesExecutor` |
| 2 | cancelled execution cannot become **PROVEN** via a late callback | **yes** | loop top-of-loop gate; `TestR6_LateProviderSuccessAfterCancelCannotComplete` |
| 3 | cancellation does not erase committed mutation evidence | **yes** | `TestR6_MutationBoundary_CommittedMutationIsNotRolledBackByCancellation`, `TestR6_CommittedMutationSurvivesCancellation` |
| 4 | cancellation is not rollback | **yes** | same; `cancel != rollback`. (The DAG *transaction* rolls back on abort — a separate, deliberate atomicity contract; see F5.) |
| 5 | late provider/stream/capability callbacks cannot resurrect execution | **yes** | tests #4, #5, #6 |
| 6 | a selected continuation cannot silently start after cancellation | **yes** | `TestR6_CancelAtContinuationBoundaryDoesNotStartNextAttempt` |
| 7 | final lifecycle state is deterministic under completion/cancel races | **yes** | `TestR6_CompletionCancelRaceIsDeterministic`, `TestR6_TerminalCompletionIsImmutable`; the linearization point is the ctx check at `RuntimeLoop.Step` / the inter-step gate |

---

## 12. Findings and classification

| ID | Finding | Classification |
|---|---|---|
| **F1** | A cancellation at the **mutation boundary** was reported as `apply_failed` (and the graph emitted `execution.failed`), making a cancelled apply indistinguishable from a genuine write failure. | **C — lifecycle semantics bug** (found, fixed) |
| F2 | The autonomous loop has no typed cancellation state; cancel/abort/timeout share `RuntimeAborted`/`FailurePermanent`, distinguished only by the reason string. | **B — observability gap** |
| F3 | `CapabilityToolRunner.execute` ignores `context.Context` (`_ context.Context`); capability cancellation does not reach that seam. | **E — capability cancellation limitation** (bounded, read-only) |
| F4 | The executor's intra-invocation artifact continuation does not check `ctx` between steps; it relies on the next provider call honoring the context. | **A/D — correct at the authority; provider-dependent in the loop** |
| F5 | Cancellation mid-DAG rolls back already-committed sub-task mutations. | **A — existing (deliberate) contract: the DAG is one atomic transaction** |
| F6 | The pre-existing cancellation test comment attributes late-result suppression to the `runID` guard; the real guard is the ctx gate + `RuntimeLoop.Step`. | **B — documentation/observability** |

### Why F2 is a gap, not a bug

Cancellation still terminates truthfully and terminally. `RuntimeAborted` +
reason `"context cancelled"` is observable, durable and not a false success. A
distinct typed state would improve projection ergonomics but would not change
any safety property. It is reported so a later lifecycle phase can decide
whether the loop should expose cancellation as a first-class terminal (it already
does at the attempt level via `OutcomeCancelled`).

### Why F5 is not a violation of `cancel != rollback`

Two different contracts exist and they must not be conflated:

- **Single execution attempt** (`RuntimeExecutor.Approve`): the `MutationSet`
  transaction is open only during the apply. Once it commits, a later
  cancellation does **not** roll it back. The file keeps the bytes.
- **DAG plan** (`Driver.runProposalDAG`): sub-tasks are deliberately grouped into
  one plan-level atomic transaction; aborting before completion restores the
  base tree digest. That is transaction atomicity for a multi-file plan, not
  "cancel erases a committed mutation". `failDAG` reports `DAG_EXECUTION_FAILED`
  with the reason including the cancellation.

---

## 13. The first incorrect transition and the minimal correction

**First incorrect transition (F1).**

```text
ctx withdrawn at the mutation boundary
  → PatchManager.ApplyContext returns ErrPatchApplyTimeout(context.Canceled)
  → MutationSet.RollbackTo (correct: the transaction is open)
  → res.Proof.Outcome = OutcomeApplyFailed        ← INCORRECT
  → g.FailExecution(...)                          ← INCORRECT (cancel is not a failure)
```

**Authoritative owner:** `RuntimeExecutor.Approve` (`internal/execution/executor.go`).

**Smallest correction** (applied): in the apply-error branch, a `context.Canceled`
apply error is classified as the clean `OutcomeCancelled` and the graph is
cancelled rather than failed; the error is nil, and per-file mutation outcomes
are published as `cancelled` (nothing was applied). A `context.DeadlineExceeded`
(the 90 s apply timeout) is deliberately left as a failure — a timeout is not a
user cancellation.

```go
cancelled := errors.Is(applyErr, context.Canceled)
outcome := OutcomeApplyFailed
switch {
case cancelled:
    outcome = OutcomeCancelled
case ms.Verification != nil && !ms.Verification.Passed && !ms.Verification.Skipped:
    outcome = OutcomeVerifyFailed
}
...
if cancelled {
    g.CancelExecution(string(OutcomeCancelled))
    res.Err = nil
    return x.finalizeResult(res), nil
}
```

**Regression:** `TestR6_MutationBoundary_CancelBeforeAuthorizationLeavesZeroDelta`
failed before the fix (`outcome "apply_failed", want "cancelled"`) and passes
after. `TestR6_MutationBoundary_CommittedMutationIsNotRolledBackByCancellation`
and the whole prior suite remain green. No adjacent lifecycle code was
refactored; no WAL, recovery, resume or queue change was made.

---

## 14. Live benchmark (real provider)

`test/live_r6/probe_test.go`, opt-in via `IZEN_LIVE_FORENSICS=1`, real
composition (`compose.Wire` → `autonomy.Driver` → `RuntimeExecutor`), real
`ollama/qwen2.5-coder:7b`, isolated git workspace. Cancellation is triggered by
an **observed** provider lifecycle event (`model.invoked`), never a sleep.

Result (7.05 s):

```text
CALL #1  ollama/qwen2.5-coder:7b  req=512  completion=31 tok  finish=stop     ← requirement pass
CALL #2  ollama/qwen2.5-coder:7b  req=1024 completion=0  tok  error=cancelled  ← real in-flight call cancelled

LOOP TRANSITIONS
  executing    → verifying    "execution consumed: cancelled"
  verifying    → interpreting "verification consumed"
  interpreting → aborted      "context cancelled"

TERMINATION  aborted / "context cancelled"
MUTATIONS     0
WORKSPACE     index.html byte-identical (225 bytes)
PROVEN        none
```

The live run confirms: a **real** provider call was interrupted by a **real**
context cancellation; the run reached a typed terminal `aborted`; the workspace
had a zero delta; no objective was declared PROVEN. Live cancellation was made
deterministic by keying off the `model.invoked` event rather than a timer; no
production semantics were weakened to make the test pass.

Reproduce:

```
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r6/ -v -timeout 500s
```

---

## 15. Verification

```
go test ./...       # PASS — all packages green (incl. test/forensics, test/live_r1..r6)
go test -race ./... # PASS — all packages green under the race detector
go vet ./...        # PASS — no findings
```

- Deterministic R6 tests: `go test ./internal/execution/ -run TestR6_` and
  `go test ./internal/runtime/autonomy/ -run TestR6_`.
- R1–R5.1 tests unchanged and green (full-suite run below).
- `git diff` scope: only `internal/execution/executor.go` (the F1 correction),
  the two new deterministic test files, the new live test, the report and the
  state handoff. Nothing in `runtime/kernel`, `internal/kernelbridge`,
  `internal/core/authorization`, provider transport, target resolution,
  progress semantics, continuation semantics or `ohgo`.

*(Exact command output is recorded in `EXECUTION_FORENSICS_STATE.md` §R6-A after
the final run.)*

---

## 16. The fourteen questions

1. **Who owns cancellation request?** The UI operation (`operation.Cancel`,
   driven by Esc×2 / Ctrl+C / Ctrl+D / OS signal), or `Driver.Abort` for a parked
   run.
2. **Who owns cancellation state?** The withdrawn `context.Context`
   (`m.activeOp.Ctx → d.runCtx`). A separate kernel/task path uses
   `TaskRuntime.canceled`.
3. **How does cancellation propagate?** Context cancellation through the
   operation context into `d.runCtx`, the adapter, the executor, the provider
   transport, the shell and the mutation boundary.
4. **Does provider cancellation actually occur?** Yes — HTTP request context
   (`http.NewRequestWithContext`); observed live with `error=cancelled` on the
   in-flight call.
5. **Does capability cancellation actually occur?** For shell/command
   capabilities, yes (`exec.CommandContext`). For the read-only
   `CapabilityToolRunner` seam, no (ctx is ignored) — bounded and non-mutating
   (F3, E).
6. **Can a late callback mutate authoritative state?** No. The loop is
   linearized to abort at the ctx gate; a late mutating result is never applied
   (`ResumeApprove` refuses once the loop is terminal), and evidence from a
   cancelled attempt is either absent or truthfully attributed.
7. **Can cancellation race with completion?** Yes, and the ordering is
   deterministic: whichever ctx check observes the cancel first wins; if it is
   `RuntimeLoop.Step`, the completion is never applied and the terminal is
   `Aborted`.
8. **Can cancellation race with continuation?** Yes; a selected continuation is
   gated by the same `Step` ctx check and never starts after cancellation.
9. **What happens if a mutation already committed?** It stays committed.
   Cancellation does not roll it back; the run reports the truth (mutation
   happened, run cancelled, objective not necessarily PROVEN).
10. **Is cancellation distinct from rollback?** Yes for a committed
    single-attempt mutation. A still-open `MutationSet` transaction is rolled
    back for atomicity, and an in-flight DAG plan rolls back to its base digest
    by design — both are transaction boundaries, not `cancel == rollback`.
11. **What are the actual Esc/Ctrl+C semantics?** Esc = two-press cancel (or
    modal reject); Ctrl+C = cancel, a second Ctrl+C exits 130; Ctrl+D = interrupt
    while processing; SIGINT/SIGTERM = Ctrl+C protocol.
12. **What is the first incorrect transition?** A withdrawn context at the
    mutation boundary produced `OutcomeApplyFailed` + `execution.failed`.
13. **What is the smallest correction?** Classify a `context.Canceled` apply
    error as the clean `OutcomeCancelled` and cancel the graph (F1, §13).
14. **What evidence proves cancellation correctness?** The nine deterministic
    R6 tests, the full prior suite, and the live real-model run in §14.

---

## 17. Files

```
NEW  internal/execution/r6_cancellation_test.go              mutation boundary A/B + streaming late chunk
NEW  internal/runtime/autonomy/r6_cancellation_test.go       cancel active/before/late/continuation/race/terminal
NEW  test/live_r6/probe_test.go                              opt-in real-provider cancellation benchmark
MOD  internal/execution/executor.go                          F1: cancel at the mutation boundary is
                                                             OutcomeCancelled, not apply_failed
NEW  docs/report/R6_CANCELLATION_REPORT.md                   this report
MOD  docs/report/EXECUTION_FORENSICS_STATE.md                R6-A handoff
```

---

## 18. Explicitly not done (scope)

No redesign of `RuntimeExecutor` or `autonomy.Driver`; no WAL; no crash
recovery; no resume; no queue redesign; no disconnect recovery; no provider
transport change; no `ohgo` change; no authorization change; no target-resolution
change; no progress-semantics change; no generic retry; no change to
continuation semantics; no new lifecycle framework. `RuntimeLoop` bounds,
`FailureLedger`, the completion authority and the approval gate are untouched.
`RuntimeCancelled` and `cancel.requested`/`cancel.propagated` were deliberately
**not** added; they are recorded as the next observability decision (§12, F2).
