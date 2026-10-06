# R6-C — Process / Crash Failure Forensics

**Branch:** `fix/runtime`
**Scope:** audit-only, except one demonstrated lifecycle defect (F1) corrected with a
deterministic regression test.
**Suite:** `go test -count=1 ./...` green (211 packages, 0 failures);
`go test -race -count=1 ./...` green (211 packages, no data race, no panic);
`go vet ./...` clean.

---

## 1. Executive conclusion

IZEN is **truthful in-process and silent-about-commit after process death**.

Every failure boundary that the runtime *observes* is classified honestly and
cannot be rounded up to success:

- a provider error is a typed failure, never a completion;
- a command non-zero exit is a failure, never a completion;
- an authorization refusal applies nothing;
- an apply failure rolls the whole `MutationSet` back;
- a verification failure restores the pre-apply bytes;
- a cancelled verification is **now** a failure (F1 corrected — it was previously
  reported `PASSED` when the command never ran);
- a panic inside `PatchManager.apply` is recovered to an error, never a success;
- a cancelled/failed execution cannot reach `RuntimeCompleted`.

What IZEN **cannot** do is establish *post-boundary truth after its own process
dies*. The mutation boundary has an in-memory commit
(`MutationSet.Commit` + `engine.Transaction.Commit`) and a real, independently
re-verified write through the kernel, but the autonomous `$prompt` path has **no
durable commit marker bound to the mutation**. Its durable ledger
(`.izen/runtime/ledger.ndjson`) records a checkpoint at the *loop boundary
before execution* and only records `EXECUTION_COMMITTED` once the objective is
**PROVEN**.

The exact missing evidence is already modeled elsewhere in the codebase
(`durable.ExecutionCursor` precondition/postcondition digests + `Reconcile` →
`ALREADY_COMMITTED` / `SAFE_RETRY` / `CONFLICT`), but that cursor machinery is
wired to the scoped `RuntimeEngine`/`scopeguard` path, **not** to the
`autonomy.Driver`. Consequently:

> After `mutation commit → process dies`, IZEN knows the task was in flight and
> that the workspace may have changed, but it cannot prove whether the mutation
> committed. The truthful post-crash classification is **UNKNOWN**, and IZEN
> keeps the task non-terminal (`RecoverableTasks`) rather than asserting
> success or failure.

**Crash recovery is not a supported contract for the autonomous execution
path.** A fresh runtime can reconstruct *that a task was in flight and where the
loop was*, but not *whether the in-flight mutation committed*. This is
**Case B** of the Crash Recovery Decision Boundary (§14): durable evidence
exists but is insufficient to distinguish two material states.

---

## 2. Failure-boundary map

Owners verified by reading the production path. `UNKNOWN` means the architecture
cannot currently prove the answer.

| Failure boundary | Owner (file:line) | Current state transition | Durable evidence | Truthful terminal state |
|---|---|---|---|---|
| provider HTTP error / non-2xx | `executor.invokeStream` `executor.go:4641`; adapters `providers/*.go` | attempt outcome `OutcomeFailed` / `OutcomePatchGenerationFailed` → loop recovery (`DecideRecovery`) | `execution.finished(success=false)` + `provider.execution` telemetry event (coarse `error_code`); audit NDJSON (lossy) | non-success: retry/park/abort — never `PROVEN` |
| provider returned explicit error body | `httpx.ParseProviderError` (display only) | not typed into the outcome | none beyond the generic failure text | non-success |
| provider connection dropped mid-stream | `providers/*` SSE reader → `invokeStream` | `OutcomeFailed` (generic) | none distinct | non-success |
| provider truncated (`finish_reason=length`) | `executor.invokeStream` `executor.go:5084`; `OutputGateError` | `OutcomeTruncated` → same-execution bounded continuation | `provider.execution` (requested/effective tokens, truncated), continuation events | non-success / continued |
| caller context cancelled | executor read-only/mutation/stream paths `executor.go:2007,2228,4953` | `OutcomeCancelled`, `res.Err=nil`, graph `CancelExecution` | `execution.finished(success=false, outcome="cancelled")` | cancelled (not failed) |
| caller context deadline | same paths | **failure** (`context.DeadlineExceeded` is not matched as cancel) | generic `execution.failed` | non-success |
| command non-zero exit | `capability.Runner.Command` `capability/serve.go:993` ← `shellCommandRunner` `behavior.go:206` ← `ExecShell.run` `exeshell.go:48` | currently `FailureCapabilityFailed` (`"command could not be started"`); contract says `FailureExecutionFailed` | capability `Evidence` (in-memory) → observation; no durable record | non-success, but exit code/stdout/stderr **lost** (E) |
| command signalled (SIGKILL/timeout) | `ExecShell.run` `exeshell.go:67-78` | error; exit code `-1` | none distinct from start failure | non-success, signal indistinguishable from start failure (E) |
| command output not started / fork failure | `ExecShell.run` `exeshell.go:73-77` | `-1` | none | non-success |
| executor error (contract/preflight) | `execution.RuntimeExecutor.Execute` `executor.go`; `adapter.observe(nil)` `adapter.go:680` | `OutcomeFailed` (nil result), graph `FailExecution` | `execution.failed` + `execution.finished(false)` | non-success |
| mutation failure | `PatchManager.apply` `patch.go:818/868`; `MutationSet.RollbackTo` `mutationset.go:212`; executor `executor.go:2622` | `RollbackTo(MutationFailed)`; graph `FailExecution` | `execution.failed`; in-memory mutation evidence; prior `.izen/audit/mutations.log` only if an earlier write succeeded | non-success, workspace restored |
| mutation committed | `PatchManager.commitThroughKernel` `patch.go:565`; `MutationSet.Commit` `executor.go:2690` | `OutcomeChanged/Created`; loop may complete | workspace bytes; `mutations.log` line (not fsync'd); durable `EXECUTION_COMMITTED` **only if objective PROVEN** | truthful while the process lives; **UNKNOWN** post-crash |
| verification failure | apply gate in `PatchManager.apply` `patch.go:897-957`; `Verifier.runSteps` `verify.go:450` | `OutcomeVerifyFailed`; shadow restore; graph `FailExecution` | `execution.failed` + `verification.*` events | non-success, workspace restored |
| verification interrupted (cancelled ctx / start failure) | `Verifier.runStep` `verify.go:482` | **F1 FIXED:** now failure; previously `PASSED` | verification report → events (was a fabricated pass) | truthful after fix |
| runtime panic (in apply) | `PatchManager.ApplyContext` `patch.go:1081-1091` | recovered → error; **no rollback, no terminal event** | none | no false success; post-panic state caller-dependent → UNKNOWN if unhandled |
| runtime panic (outside apply) | none | propagates; bubbletea `recoverFromGoPanic` → `os.Exit(1)` | none for the execution (`driver.emitRunSummary` only) | no completion; state UNKNOWN |
| process death (SIGKILL / host loss) | OS | no transition executes | `.izen/runtime/ledger.ndjson` (fsync'd), audit NDJSON (buffered, lossy), workspace bytes | **UNKNOWN** unless durable evidence independently proves otherwise |

No row can produce `PROVEN` except the verification-backed objective authority
(`objective_completion.go`), and every terminal graph transition is
terminal-guarded (`graph.go:610-658`).

---

## 3. Mutation-boundary analysis

IZEN's mutation machinery, in order:

1. **Preparation / authority** — `RuntimeExecutor.Execute` stages a `PatchManager`
   patch, attaches the authoritative `MutationSet` and compiled diffs, and runs
   the OCC baseline gate **before any byte is written** (`executor.go:2561-2577`).
   `PatchManager.apply` re-checks authorization (`patch.go:626`).
2. **Record** — `MutationSet.Record` snapshots the pre-mutation bytes into the
   owned `engine.Transaction` (`mutationset.go:149`, `transaction.go:66`). The
   snapshot lives **only in memory**.
3. **Write** — `commitThroughKernel` → `kernelbridge.Apply` → kernel `file.write`
   under a fixed grant. The kernel then **re-reads the bytes through code that
   did not write them** and adjudicates (`kernelbridge/apply.go:471-514`). A
   destination whose on-disk content differs is a FAILED execution.
4. **Commit** — `ms.Commit()` (`executor.go:2690`) flips the in-memory state and
   clears the snapshots (`transaction.go:133`). There is no durable write here.
5. **Post-commit observation / events** — `publishMutationOutcome`,
   `publishVerification`, then `completeExecution` (`executor.go:2716`) seals
   evidence (`sealTerminalEvidence`) and emits `execution.finished`.
6. **Filesystem durability** — the kernel write is the durability point for the
   bytes. `appendMutationLog` (`patch.go:517`) appends a text line to
   `.izen/audit/mutations.log` *after* a successful write; it is opened per call
   and **not fsync'd**, so it is a forensic hint, not a commit marker.
7. **Rollback** — `MutationSet.RollbackTo` restores every recorded snapshot
   (`mutationset.go:205-233`). A verifier failure restores the shadow backup
   inline (`patch.go:930`). A committed set can never roll back.

### The five cases

| Case | Reachable? | State | Evidence |
|---|---|---|---|
| **A** no mutation started | yes | `MutationSet` pending; nothing recorded; workspace unchanged | known (no write ran, no record) |
| **B** started but did not commit | yes | on failure the executor rolls the whole set back → workspace restored | known while the process lives; **UNKNOWN** after death |
| **C** committed and returned success | yes | kernel write proved; `OutcomeChanged/Created`; `execution.finished(true)` | known while the process lives |
| **D** may have committed, caller lost control before recording success | yes | window between kernel write (`executor.go:2591` / `patch.go:868`) and terminal (`executor.go:2716`) / driver `ledgerObserved` (`driver.go:2439`) / `ledgerExecutionCommitted` (`objective_completion.go:208`) | **UNKNOWN after death**: no durable commit marker on the autonomy path |
| **E** genuinely unknowable | yes | post-crash D | **UNKNOWN** |

### The decisive seam

```
kernel write lands (bytes durable)
        ↓
MutationSet.Commit()            ← in-memory only
        ↓
... process dies here ...
        ↓
execution.finished / ledger terminal never written
```

After restart, `.izen/runtime/ledger.ndjson` contains the task
(`TASK_CREATED`), the pre-execution loop checkpoint (`CHECKPOINT_CREATED`), and
nothing that says the mutation committed. The workspace bytes are visible, but
there is **no digest / operation marker** binding those bytes to the authorized
operation. Therefore:

> mutation may have committed, but no durable commit marker exists for the
> autonomous execution path; post-crash mutation state = **UNKNOWN**.

**A commit marker exists in the codebase and is deliberately not used here.**
`durable.ExecutionCursor` records precondition/postcondition tree digests and
`Reconcile` maps the observed digest to `ALREADY_COMMITTED` / `SAFE_RETRY` /
`CONFLICT` (`durable/cursor.go:8-27`, `durable/types.go:55-63`). It is driven by
`IdempotentRunner` (`durable/cursor.go:51`) and the `scopeguard`/`RuntimeEngine`
path, not by `autonomy.Driver`. The driver's ledger helpers
(`internal/runtime/autonomy/ledger.go`) call `CommitExecution` only when the
objective is **PROVEN** (`ledger.go:133`, called at `objective_completion.go:208`)
and never call `DispatchCursor`. This is the exact missing evidence.

---

## 4. Provider failure analysis

Typed vocabularies exist:

- `CanonicalOutcome` (`execution/boundaries.go:45-61`):
  `COMPLETE`, `OUTPUT_EXHAUSTED`, `PROVIDER_REFUSAL`, `TRANSPORT_ERROR`,
  `UNKNOWN`.
- `ProviderState` (`execution/objective_authority.go:43-54`):
  `PENDING`, `STREAMING`, `DONE`, `REFUSED` — **no `FAILED`/`TIMEOUT` state**.
- `FailureClass` (`execution/execution_failure.go:59-143`): includes
  `TRANSPORT_ERROR`, `OUTPUT_EXHAUSTED`, `PROVIDER_REFUSAL`, etc.

Findings:

- **Only truncation is distinctly typed.** `finish_reason=length` produces
  `OutputGateError` → `OutcomeTruncated` → `FailureOutputExhausted`
  (`executor.go:5084`, `boundaries.go`).
- **`CanonicalTransportError` is dead vocabulary** — it is defined but never
  assigned anywhere; a transport failure normalizes to `CanonicalUnknown`
  (`boundaries.go:75-88`). There is no producer.
- **HTTP non-2xx, an explicit provider error body, a mid-stream connection
  drop, and connection-refused all collapse** into the generic
  `OutcomeFailed`/`OutcomePatchGenerationFailed` and the single
  `FailureClass=TRANSPORT_ERROR` (`failure_ledger.go:354-362`). Only a
  display/telemetry string distinguishes them (`executor.go:4094-4108`).
- **A transport failure is recorded as `ProviderState=STREAMING`** (non-terminal)
  with `ProviderError=nil`, because `ObjectiveEvidenceFromResult` calls the
  observer with a hard-coded `nil` error after the failed invocation was
  appended (`objective_authority.go:1283-1291`, `executor.go:3594`).
- **Cancellation is clean** (`context.Canceled` → `OutcomeCancelled`);
  `context.DeadlineExceeded` is **not** treated as a cancellation and falls into
  the generic failure branch.
- **Timeouts:** no per-invocation deadline inside the executor; the provider call
  receives the caller's context directly (`executor.go:4641`). Bounds come from
  HTTP phase timeouts (`httpx/transport.go`), a 15 s TTFT watchdog and a 30 s
  inter-token idle guard (`core/stream/idle.go:27-37`), and caller operation
  deadlines (e.g. UI gateway 5 min).
- **Durability of provider facts:** the sealed/durable execution record carries
  only outcome/targets/mutations/timestamps (`events.go:1169-1185`); provider
  detail is recomputed in memory and the per-invocation
  `ProviderExecutionPayload` is the closest durable provider record.

**Distinguishing provider failure from runtime loss of control:** a provider
that *returns* an error is representable (as a coarse failure). A runtime that
*loses control before the response* produces no observation at all, and absence
is not classified — it defaults to `ProviderPending`/`Streaming` (non-terminal)
and cannot be turned into success. So IZEN can distinguish a returned provider
error from a successful response, but it **cannot** distinguish "the provider
failed" from "the runtime died before observing the provider" once the process
is gone.

---

## 5. Command / process failure analysis

Owner chain for the `command.run` capability:

```
capability.Runner.Command          internal/execution/capability/serve.go:974
  → shellCommandRunner.RunCommand  internal/execution/behavior.go:206
  → ports.ShellPort.ExecuteIn      internal/domain/ports
  → ExecShell.run                  internal/infrastructure/capabilities/exeshell.go:48
      exec.CommandContext(ctx, "sh", "-c", command)   exeshell.go:52
```

- **Exit code semantics (defect, classification E).** The capability contract
  and its unit test say a non-zero exit is a *failed observation* with
  `FailureExecutionFailed` and the real `exit_code` (`serve.go:1000-1005`,
  `capability_test.go:662-681`). But `ExecShell.run` returns a non-nil error for
  every non-zero exit (`exeshell.go:67-78`), and `shellCommandRunner.RunCommand`
  discards the whole `ShellResult` when the port errors
  (`behavior.go:222-225`). The capability therefore reports
  `FailureCapabilityFailed` with the false reason **"command could not be
  started"**, and **exit code / stdout / stderr are lost**. The unit test only
  exercises a stub, so the real adapter is unverified.
- **Signal termination is indistinguishable from start failure.** Every exit
  code is read via `exec.ExitError.ExitCode()`, which returns `-1` for a
  signalled process; the same `-1` is used for fork/start failure. There is no
  `Signaled()`/`WaitStatus` check anywhere.
- **Context cancellation / timeout** at the command boundary is misclassified as
  a capability failure, not a cancellation.
- **Timeout:** `command.run` is double-bounded — `shellCommandRunner` wraps the
  caller context with `s.timeout` (`behavior.go:210-215`) and `ExecShell` wraps
  it again (`exeshell.go:49`); the production port timeout is 60 s
  (`compose.go:1042,667`). `execution.Runner.run` (verify/test/engine) has **no**
  per-command timeout.
- **Start failure vs non-zero exit:** because everything routes through
  `sh -c`, a missing inner binary exits 127; a missing shell itself is a Go
  start error. The two are not typed apart.
- **Ownership / orphans:** `command.run`'s process is **not** registered in the
  `execution.Runner` orphan registry (`registerProcess`), so
  `KillAllOrphans`/`KillOrphanedByContext` cannot reach it. It does set
  `Setpgid` and a group-kill `Cancel` (`exeshell.go:54-61`), so cancellation
  reaps the group, but a hard process death can orphan the child. The tracked
  registry kills only the shell PID, not the group.

**Conclusion:** command failure never becomes success, but the current contract
does not preserve the exit status; signal, timeout, cancellation and start
failure are not distinguishable at the `command.run` boundary. This is a
**capability-evidence limitation (E)**, not a false-state path.

---

## 6. Panic analysis

There is **no panicking-recovery boundary around the autonomous execution**:

- The driver runs inside a Bubble Tea `tea.Cmd` goroutine
  (`ui/autonomous.go:207`); `driveAutonomy` has no `recover`
  (`ui/autonomous.go:115-121`).
- `internal/ui/update.go:114-123` (`recoverUpdate`) guards only the `Update`
  call; it prints a stack to stderr and discards the recovered value — **no
  terminal execution state is recorded**.
- Bubble Tea's own `recoverFromGoPanic` catches a command-goroutine panic,
  cancels the program, and `program.go:578` calls `os.Exit(1)`, bypassing
  `cmd/izen/main.go`'s deferred audit flush and lock release.
- `grep recover()` finds **no** recovery in `cmd/izen`, `executor.go`, the
  autonomy package, or the execution package. The only domain recovery is
  `PatchManager.ApplyContext` (`patch.go:1081-1091`), which converts a panic from
  `pm.apply` into an error and performs **no rollback and no terminal
  publication** (its own doc says the caller must roll back). `runtime/kernel/event.go:240`
  silently discards a panic in a notification callback.

Consequences:

1. A panic **cannot** produce a false `PROVEN`: the loop never reaches
   `RuntimeCompleted` and every graph terminal transition is guarded.
2. A panic in `PatchManager.apply` is safe-ish: it becomes a failed apply, and
   the executor rolls the `MutationSet` back.
3. A panic **after** the apply/commit and **before** the terminal event
   (`executor.go:2591` … `2716`, or in the driver/adapter) is unguarded: bytes
   may be on disk, no `execution.finished` is emitted, and no durable terminal is
   written. The post-panic state is **UNKNOWN**. `driver.emitRunSummary` is
   deferred and will run during unwinding, but the durable terminal
   (`ledgerTerminal`) is *not* deferred (`driver.go:583`, `term()` at `:3093`).

No production panic boundary was added; the absence is recorded as an
architectural fact.

---

## 7. Abrupt process-death analysis

Controlled subprocess test: a helper process opens the durable store, records an
in-flight task, writes the pre-execution loop checkpoint, performs a real
workspace mutation, and blocks; the parent delivers `SIGKILL`; a **fresh**
runtime opens the same `.izen` state.

```
Before death:
    in-memory  : loop state, MutationSet, Transaction snapshots
    durable    : .izen/runtime/ledger.ndjson (TASK_CREATED + CHECKPOINT_CREATED,
                 fsync'd), .izen/runtime/snapshot.json
    workspace  : the mutation's bytes (kernel write already landed)

After death:
    workspace  : the mutation survives
    durable    : ledger survives to the last valid line (torn tail tolerated);
                 audit NDJSON loses its buffered tail (bufio + async queue, no
                 per-event fsync); in-memory snapshots/sets are gone

After fresh start:
    IZEN reconstructs: there is a non-terminal task `obj-crash`, and the last
        checkpoint is the pre-execution loop boundary
    IZEN cannot reconstruct: whether the mutation committed, whether it was the
        authorized one, or whether verification ran
```

The test asserts: `RecoverableTasks()` returns exactly the interrupted task;
`State().Status` is non-terminal; `State().Cursor == nil`; the ledger contains
no `EXECUTION_COMMITTED`; and the workspace holds the mutation. That is the
truthful post-crash picture: **task = in flight; mutation = UNKNOWN**.

Evidence: `internal/runtime/durable/r6c_process_death_test.go`.

---

## 8. Truthfulness matrix

Terms use the IZEN state model (`autonomy.RuntimeState`: `executing`,
`verifying`, `interpreting`, `awaiting_human`, `completed`, `unsubstantiated`,
`aborted`) and the canonical mutation outcomes.

| Point of failure | Workspace | Runtime knowledge | Objective | Safe classification |
|---|---|---|---|---|
| before mutation (auth/OCC/preflight) | unchanged | known (no write ran) | incomplete | `aborted` / `awaiting_human` — never `completed` |
| during mutation (apply error) | restored (rollback) | known (rollback ran) | incomplete | `aborted` (failure) — workspace unchanged |
| during mutation then **process dies** | changed / unknown | lost | incomplete | **UNKNOWN** |
| after commit, before terminal event | changed | uncertain (no durable commit marker) | incomplete | **UNKNOWN** |
| after mutation, before verification | changed | known (in-process) | incomplete | `verifying`; if process dies → **UNKNOWN** |
| during verification (gate fails) | restored | known | incomplete | `aborted` (verify failure) |
| during verification (process dies) | changed | partial | incomplete | **UNKNOWN / UNSUBSTANTIATED** |
| verification interrupted (cancelled ctx) | depends | known | incomplete | non-success (F1: previously a false PASS) |
| after verified proof (authority PROVEN) | proven evidence | known | **PROVEN** | `completed` |
| process death (any point) | survived bytes | **lost** | unknowable | **UNKNOWN** unless durable evidence proves otherwise |

---

## 9. Checkpoint analysis

Three distinct checkpoint schemes coexist; none is a mutation commit marker.

| Scheme | Persists | Written | Represents | Authoritative? |
|---|---|---|---|---|
| `durable.TaskStore` `CHECKPOINT_CREATED` + `snapshot.json` | loop-boundary bookmark, task status, last checkpoint id, cursor | at the top of each driver loop iteration, **before execution** (`driver.go:2233`; `ledger.go:166`) | *where the loop was*, not mutation state | authoritative for in-flight **task** surfacing; not for mutation state |
| `durable.ExecutionCursor` precondition/postcondition digests | `CURSOR_DISPATCHED` / `EXECUTION_COMMITTED` + digests | around a side effect via `IdempotentRunner`/`scopeguard` | mutation **commit** | authoritative — **but not wired to `autonomy.Driver`** |
| `checkpoint` package (shadow git tree / Phase-3 file copy) + `execution.CheckpointManager` | shadow workspace snapshot / git commits | `CreatePreExecSnapshot` is effectively unused in production; `CreateSessionStartSnapshot` at session start; Phase-3 copy in `substrate.ApplyPatch` | pre-mutation **state** | restoration support only |

Answers to the audit questions:

- **What is persisted?** Task lifecycle events, loop checkpoints,
  `snapshot.json`, session state, patches, OCC clocks, audit NDJSON,
  `mutations.log`.
- **Sufficient to reconstruct execution truth?** It is sufficient to reconstruct
  *which task was in flight and roughly where the loop was*. It is **not**
  sufficient to reconstruct whether a mutation committed.
- **Before or after mutation?** The driver checkpoint is **before** execution.
  `mutations.log` is **after** a successful write. There is no durable "commit"
  between them.
- **Can a checkpoint be stale?** Yes — it describes a pre-execution loop
  boundary and is not invalidated by a later workspace mutation.
- **Intent / authorization / mutation / verified?** The driver checkpoint
  represents loop position (intent-adjacent). Authorization is an event only.
  Mutation state is in-memory. Verified state is the objective authority's
  verdict plus `EXECUTION_COMMITTED` (only on PROVEN).
- **Authoritative or forensic?** `ledger.ndjson` is authoritative for task
  lifecycle; `events.ndjson` and `mutations.log` are forensic projections.
- **Survives abrupt death?** The ledger does (fsync per event + directory fsync
  on truth boundaries, torn-tail replay, stale-lock reclaim). The audit NDJSON
  and `mutations.log` are lossy.
- **Can a fresh runtime consume it safely?** It can safely *surface* the task;
  it cannot safely *resume* it or classify the mutation, because the commit
  marker is absent.

---

## 10. Classification

| # | Finding | Class |
|---|---|---|
| **F1** | `Verifier.runStep` reported `PASSED` for a step whose command never ran (`err != nil` with `ExitCode == 0`, the shape a cancelled/unstarted `exec.Exec` produces). This is the mutation apply-gate. | **C — Lifecycle Bug (FIXED)** |
| F2 | HTTP non-2xx / dropped connection / connection-refused / explicit provider error all collapse to one generic failure; `CanonicalTransportError` is dead vocabulary; transport failure is recorded as `ProviderState=STREAMING`. | **B — Observability Gap** + **D — Provider Limitation** |
| F3 | `command.run`'s real adapter discards a non-zero exit's result and reports `"command could not be started"`, losing exit code/stdout/stderr. | **E — Capability Limitation** (+ B) |
| F4 | Signal termination and start failure both encode as exit code `-1`; no `Signaled()`/`WaitStatus`. | **E — Capability Limitation** |
| F5 | `command.run`'s process is not in the orphan registry; a hard death can orphan it. | **E — Capability Limitation** |
| F6 | The autonomous path writes no durable mutation commit marker (no `DispatchCursor`); post-crash mutation state is UNKNOWN. | **F — Missing Crash Contract** (+ B) |
| F7 | No panic boundary around execution outside `PatchManager.apply`; a post-commit panic leaves no terminal record. | **F — Missing Crash Contract** |
| F8 | `.izen/audit/events.ndjson` and `mutations.log` are buffered/not fsync'd; the audit tail is lost on `SIGKILL`. | **B — Observability Gap** |
| F9 | `context.DeadlineExceeded` is classified as a generic failure, not a timeout/cancellation. | **B — Observability Gap** |

Not classified as bugs: the absence of WAL/resume, the absence of a queue, the
absence of a top-level panic boundary — these are "not implemented" and are
recorded as facts, per the fix policy.

---

## 11. Production changes

**Exactly one production change**, the smallest local correction for the one
demonstrated lifecycle bug:

```diff
--- a/internal/execution/verify.go
+++ b/internal/execution/verify.go
@@
 	if err != nil {
 		result.Error = err.Error()
 		if rawResult != nil {
 			result.Output = rawResult.Stderr
 			if result.Output == "" {
 				result.Output = rawResult.Stdout
 			}
 		}
-		if rawResult != nil && rawResult.ExitCode == 0 {
-			result.Passed = true
-		}
-		if !result.Passed {
-			result.SyntaxErrors = ParseSyntaxErrors(result.Output)
-		}
+		// A step that returned an error is NEVER a pass. The only shape where
+		// Runner.Run yields a non-nil error with ExitCode == 0 is a command
+		// that never ran at all...
+		result.Passed = false
+		result.SyntaxErrors = ParseSyntaxErrors(result.Output)
 		return result
 	}
```

Why it satisfies the fix policy: (1) a real lifecycle correctness bug is
demonstrated by a failing deterministic test; (2) the owning seam
(`Verifier.runStep`) is unambiguous; (3) the correction is one local branch;
(4) it introduces **no** WAL/resume/replay/persistence architecture; (5) a
deterministic regression proves it; (6) all R1–R6-A/B invariants stay green
(full suite, race, vet).

No `DispatchCursor` wiring, no recovery, no resume, no queue, no provider
redesign was added. The remaining findings are recorded, not fixed.

---

## 12. Deterministic test evidence

Added (minimum necessary):

```
NEW  internal/execution/r6c_crash_failure_test.go        C2/C3/C4/C6/C7 + F1 regression
NEW  internal/runtime/autonomy/r6c_provider_failure_test.go   C1
NEW  internal/runtime/durable/r6c_process_death_test.go  C5/C8 subprocess kill -9
```

| Test | Scenario | Proves |
|---|---|---|
| `TestR6C_Verification_CancelledContextNeverPasses` | C6 / F1 | a command that never ran is not `PASSED` |
| `TestR6C_Verification_FailingCommandFailsTheGate` | C6 | a real non-zero verification fails the gate |
| `TestR6C_VerificationFailureRestoresTheWorkspace` | C6 | failed gate restores pre-apply bytes; no commit |
| `TestR6C_AuthorizationFailureBeforeMutationLeavesZeroDelta` | C3 | authorized ≠ applied; zero delta |
| `TestR6C_MutationSetPartialApplyRollsBackAtomically` | C4 | partial apply rolls the whole set back |
| `TestR6C_PanicAfterWriteIsRecoveredAsFailureNotSuccess` | C7 | panic → error, no commit; post-write window pinned |
| `TestR6C_CommandNonZeroExitIsAFailure` | C2 | a non-zero command exit is never success |
| `TestR6C_ProviderErrorNeverCompletesAndNeverMutates` | C1 | provider error → non-success, zero mutation |
| `TestR6C_AbruptProcessDeathLeavesMutationStateUnreconstructible` | C5/C8 | real kill -9; fresh runtime sees in-flight task + no commit marker → UNKNOWN |

Reproduce:

```
go test -count=1 ./internal/execution/            -run TestR6C_ -v
go test -count=1 ./internal/runtime/autonomy/      -run TestR6C_ -v
go test -count=1 ./internal/runtime/durable/       -run TestR6C_ -v
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

Live provider tests were not required for R6-C; every boundary above is
exercised deterministically with controlled injection and explicit
synchronization (no timing races, no production-only hooks).

---

## 13. Remaining UNKNOWN states

These are the states IZEN genuinely cannot resolve from surviving evidence. They
are listed as facts, not as work items.

| UNKNOWN | Why it is unknowable today |
|---|---|
| Did the in-flight mutation commit? | No durable per-operation commit marker on the autonomous path; the journal's `EXECUTION_COMMITTED` is only written on PROVEN. |
| Was the mutation the *authorized* operation? | The authorized candidate exists only as an in-memory `PatchManager` record and the `execution.authorized` event; no digest binds it to the surviving bytes. |
| Did verification run, and did it pass? | Verification is the apply gate before commit; its report is in-memory and only reaches the journal as part of a terminal or `ledgerObserved` record. A death between write and that record loses it. |
| Had the provider returned? | A returned error is representable; a runtime that died before observing the response leaves no observation, and absence is not classified (defaults non-terminal). |
| Was a `mutations.log` line written before death? | That file is buffered and not fsync'd, so its presence/absence after `SIGKILL` is not reliable. |
| Was a command signalled, or did it fail to start? | Both encode as exit code `-1`; no signal information is preserved. |

The one state IZEN **can** resolve is the *task lifecycle*: `RecoverableTasks`
reports that work was in flight, and the last checkpoint locates the loop
boundary. That is deliberately preferred to asserting either success or failure.

---

## 14. Crash Recovery Decision Boundary

### Case B — durable evidence exists but cannot distinguish some states

```text
mutation may have committed
but no durable commit marker exists for the autonomous execution path
therefore post-crash mutation state = UNKNOWN
```

A fresh runtime **can** answer: "there was an in-flight task, and its last loop
checkpoint was X". It **cannot** answer: "the mutation committed" or "the
mutation did not commit". The existing `ExecutionCursor`/`Reconcile` machinery
would make that answer safe, but it is not wired to `autonomy.Driver`.

No recovery work is implemented. This is recorded as the next experiment's
target.

---

## 15. Does IZEN currently support crash recovery?

**No — not as a supported contract for the autonomous execution path.**

- What exists: a durable, fsync'd, torn-tail-tolerant ledger that surfaces
  in-flight tasks (`RecoverableTasks`, `MostRecentRecoverable`) and stale-lock
  reclaim; a digest-based commit protocol (`ExecutionCursor` + `Reconcile`)
  wired to the scoped `RuntimeEngine`/`scopeguard` path.
- What is missing: a durable mutation commit marker on the `autonomy.Driver`
  path, and a panic boundary outside `PatchManager.apply`.
- Net: crash recovery is **partial** — task surfacing works; in-flight execution
  truth (whether a mutation committed) is **UNKNOWN** and must not be guessed.

---

## 16. Required final answers

1. **Can IZEN distinguish provider failure from runtime loss of control?**
   Partially. A provider that returns an error is representable as a coarse
   failure (`OutcomeFailed` → `FailureTransportError`). A runtime that loses
   control before the response leaves no observation, and absence is not
   classified (it defaults to `PENDING`/`STREAMING`, non-terminal). After process
   death the two are indistinguishable.
2. **Can IZEN distinguish mutation failure from mutation commit followed by
   caller/process death?** In-process: yes (rollback vs. committed evidence).
   Post-crash: **no** — there is no durable commit marker on the autonomy path.
3. **Can IZEN prove workspace state after abrupt process death?** It can observe
   that workspace bytes changed, but it cannot prove *which* mutation / that it
   was authorized / that it was verified. The state transition is not provable.
4. **What survives in `.izen`?** `.izen/runtime/ledger.ndjson` (fsync'd events,
   last checkpoint, task status), `.izen/runtime/snapshot.json`, session state,
   `.izen/patches`, `.izen/checkpoints`, `.izen/occ`, and (lossily)
   `.izen/audit/{events.ndjson,mutations.log}`.
5. **What survives only on disk?** The workspace file bytes (the mutation
   itself) and any external side effects (command-created files, git state).
6. **What becomes UNKNOWN?** Whether an in-flight mutation committed; whether
   verification ran/passed; whether the mutation was the authorized one; whether
   the provider had returned.
7. **Can a fresh runtime reconstruct in-flight execution truth?** Partially: it
   can reconstruct the non-terminal task and the last loop checkpoint, but not
   the mutation commit.
8. **Does any existing checkpoint make that reconstruction safe?** No for the
   autonomy path. `CHECKPOINT_CREATED` is a pre-execution loop bookmark; the
   cursor/digest mechanism that would make it safe is not wired to the driver.
9. **Is there any path to false PROVEN after failure?** No path was found.
   Completion requires `ObjectiveCompletionAuthority` to PROVE over
   `ObjectiveEvidence`; cancellation, panic and process death cannot reach
   `RuntimeCompleted`, and all graph terminals are guarded. F1 could have gated a
   mutation as *verified* (now fixed), but did not by itself set the objective
   PROVEN.
10. **Is crash recovery currently a supported contract?** No — see §15. Task
    surfacing works; mutation-state reconstruction does not.
11. **What exact missing evidence would be required?** A durable commit marker
    written **after** the kernel write and **before** any success event, bound to
    the operation identity and to precondition/postcondition workspace digests —
    i.e. wiring the existing `ExecutionCursor` / `EXECUTION_COMMITTED` /
    `Reconcile` machinery into `autonomy.Driver`; plus a durable
    "verification started" record and a panic boundary outside
    `PatchManager.apply`.
12. **Is any production correction actually necessary?** Yes — exactly one
    demonstrated lifecycle bug (F1, `verify.go` false PASS), corrected locally
    with a regression test. Everything else is recorded as B/D/E/F and left
    unchanged.

---

## 17. Recommended next experiment

**R6-D — Durable Mutation Commit Marker (design/implementation).** Wire the
existing `durable.ExecutionCursor` (precondition/postcondition digests) and
`Reconcile` into the `autonomy.Driver`'s mutation boundary so that:

```
dispatch(pre-digest) → kernel write → commit(post-digest)
```

is durable, and a fresh runtime can resolve the post-crash state as
`ALREADY_COMMITTED` / `SAFE_RETRY` / `CONFLICT` from the workspace itself —
turning the C5/C8 `UNKNOWN` into a provable classification. Add a durable
"verification started" marker and a panic boundary at the executor/driver seam
so a post-commit panic is recorded truthfully. This is explicitly **out of
R6-C scope** and must not be started here.

---

# Final principle

IZEN remains truthful where it can observe, and it says **UNKNOWN** where it
cannot. The R6-C result is not that IZEN recovers from crashes — it does not —
but that it does not fabricate `PROVEN` or `FAILED` from incomplete evidence.
One place where it *did* round absence up to success (F1) is now closed.
