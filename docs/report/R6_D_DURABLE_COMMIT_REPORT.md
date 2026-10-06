# R6-D — Durable Mutation Commit Marker Forensics & Wiring

**Branch:** `fix/runtime`
**Scope:** connect the existing `ExecutionCursor` / `DispatchCursor` /
`CommitExecution` / `Reconcile` machinery to the autonomous `autonomy.Driver`
mutation seam, plus the smallest durable correction required to make the existing
contract reachable. Not a WAL, not resume, not a recovery subsystem.
**Suite:** `go test -count=1 ./...` green; `go test -race -count=1 ./...` green;
`go vet ./...` clean. R1–R6-C remain green.

---

## 1. Executive conclusion

R6-C established the autonomous crash boundary:

> IZEN is truthful in-process, but after process death the autonomous path
> cannot durably prove whether a mutation committed.

R6-D closes that boundary using the machinery that already existed, and nothing
else. The autonomous path now records a durable **pre-digest cursor** before a
side-effecting dispatch and a durable **commit marker with the observed
post-digest** immediately after a mutation actually lands (`changed` / `created`),
at the single execution seam and the approval seam. The commit is independent of
objective `PROVEN`, because *mutation commitment* and *objective completion* are
different facts.

A **fresh runtime** can now classify the interrupted mutation from surviving
evidence alone:

```text
mutation -> process death -> fresh runtime -> Reconcile
    ALREADY_COMMITTED   durable commit marker + recorded post-digest matches the workspace
    SAFE_RETRY          dispatched cursor + live digest == durable pre-digest
    CONFLICT            live digest matches neither; never a blind retry
    UNKNOWN             no durable cursor exists
```

The verification is a **read-only** inspection. `ALREADY_COMMITTED` does **not**
resume, `SAFE_RETRY` does **not** retry, `CONFLICT` is **not** a failure of the
world, and `UNKNOWN` is **not** success. No duplicate mutation is possible after
`ALREADY_COMMITTED` because reconciliation mutates nothing; a decision is a fact
a human boundary may act on.

**Crash recovery is still not an automatic contract** — it is now a *truthful
reconciliation contract*. The state transition became more truthful, not more
optimistic.

---

## 2. Existing cursor architecture (traced, not inferred from names)

| Component | Owner | Durable? | Authority | Used by the autonomous path (before R6-D)? |
|---|---|---|---|---|
| `ExecutionCursor` | `durable` (`types.go:55`) | via `ledger.ndjson` | the cursor record itself | **no** |
| `DispatchCursor` | `durable.TaskStore` (`store.go:193`) | **yes** (fsync) | append `CURSOR_DISPATCHED`; folds cursor into `TaskState` | **no** |
| `CommitExecution` | `durable.TaskStore` (`store.go:218`) | **yes** (fsync) | append `EXECUTION_COMMITTED`; status `COMMITTED` | **only on PROVEN** (`objective_completion.go:208`) |
| pre-digest | caller of `DispatchCursor` | yes (in the dispatch event) | workspace state before the side effect | **no** |
| post-digest | caller of `DispatchCursor` (dispatch-time) | yes (when supplied) | workspace state after the side effect | **no** (the commit path could not record it) |
| `Reconcile` | `durable` (`cursor.go:16`) | pure function | `current == post -> ALREADY_COMMITTED`; `== pre -> SAFE_RETRY`; else `CONFLICT` | **no** |
| `ReconcileAll` | `durable.TaskStore` (`store.go:728`) | **yes** | transition only *pending* cursors | **no** (only scopeguard `RuntimeEngine`) |

Answers to the ten audit questions:

1. **Who creates the cursor?** The caller of `DispatchCursor`. In production that
   was the headless `runLedger` (`cmd/izen/runtime_ledger.go:134`) and the
   scopeguard `RuntimeExecutor` (`scopeguard/gateway.go:299`) — never the
   autonomous driver.
2. **Who owns it?** The `durable.TaskStore`: `DispatchCursor` and the
   `EventExecutionCommitted` fold own the materialized `TaskState.Cursor`.
3. **What is persisted?** `CURSOR_DISPATCHED` (stepId, operationId, phase,
   preconditionDigest, postconditionDigest, status) and `EXECUTION_COMMITTED`
   (operationId, phase, status). R6-D adds the *observed* postcondition digest to
   the commit event.
4. **When is it persisted?** Dispatch is written **before** the effect by the
   caller; the commit is written **after** the effect. Both are truth boundaries
   (fsync) and both are torn-tail-tolerant on replay.
5. **What digest is authoritative?** `durable.ComputeTreeDigest(workDir, scope...)`
   — a deterministic SHA-256 over sorted `relpath\0size\0content`. Both dispatch
   and reconciliation must use the same function over the same scope.
6. **What pre-state means.** The digest of the workspace scope **before** the
   side effect. Matching it proves the side effect never committed, so retry is
   safe.
7. **What post-state means.** The digest of the workspace scope **after** the
   side effect. Matching it proves the side effect committed and the workspace
   still holds exactly the bytes it produced.
8. **When `CommitExecution` is called.** The headless path calls it after a
   successful cycle; the scopeguard path calls it inside `IdempotentRunner`; the
   autonomous driver called it **only** from `ledgerExecutionCommitted`, which
   runs only when the objective authority returns `PROVEN`.
9. **When it is NOT called.** Whenever the objective is not `PROVEN` — including
   the entire window after a real mutation commit and before the completion
   authority runs, and every crash before that point. This is exactly the R6-C
   gap.
10. **What `Reconcile` reads after restart.** `ReconcileAll` replays the journal,
    recomputes the live digest per task scope, and compares each **non-committed**
    task cursor: `current == post -> ALREADY_COMMITTED` (advance, no
    re-execution), `== pre -> SAFE_RETRY`, else `CONFLICT -> TARGET_CONFLICT ->
    RE_PLAN`. It deliberately skips cursors already `COMMITTED`/`CONFLICT`.
11. **What conditions produce each result.** Precisely the three-way comparison
    above. R6-D adds a fourth, honest state — `UNKNOWN` — exposed only by a
    read-only inspection when no cursor exists at all.
12. **Is the mechanism durable across process death?** **Yes.** Each event is
    written and `fsync`ed in the same line as its assignable sequence; the
    directory is `fsync`ed at truth boundaries; replay tolerates a torn tail and
    audits sequence gaps. The *mechanism* was already durable — it was simply not
    reachable from the autonomous path.

---

## 3. Existing digest contract

`durable.ComputeTreeDigest` (`digest.go`) walks the workspace (or a single file
scope), skips `.izen`/`.git`, and hashes sorted `relpath\0size\0content\0`
records. It is stable across processes and sensitive to any byte change. A
missing scope entry contributes a tombstone (`missing:<path>`) so
create-vs-delete is digest-distinguishable.

## 4. Existing reconciliation contract

`durable.Reconcile` (`cursor.go:16`) is a pure three-way rule:

```text
current == PostconditionDigest -> ALREADY_COMMITTED (advance, DO NOT re-execute)
current == PreconditionDigest  -> SAFE_RETRY        (never committed)
otherwise                      -> CONFLICT          (third party / partial)
```

`ReconcileAll` applies it to pending cursors and persists the transition;
`IdempotentRunner` applies it around a single effect.

**The correction R6-D makes to this contract.** The cursor already carried a
`PostconditionDigest` field, but `CommitExecution` had no way to *record* the
digest observed after the effect, so the only production consumers either
supplied the post-digest at dispatch time (impossible for a real mutation) or
left it empty. R6-D adds `CommitExecutionWithDigest`, which stores the observed
post-state on the commit event; the existing `CommitExecution` delegates with an
empty digest, so the scopeguard path is byte-for-byte unchanged.

## 5. Autonomous mutation seam

The autonomous path crosses into the execution authority at exactly one call:

```text
Driver.observeAndRun
  RuntimeExecuting
    d.ledgerMutationPrepared(...)      <-- R6-D: PREPARED (dispatch pre-digest)
    obs := d.adapter.Execute(ctx, req) <-- the RuntimeExecutor mutates
    d.ledgerMutationCommitted(...)     <-- R6-D: COMMITTED (marker + post-digest)
```

and, for a human-approved mutation, at the approval seam:

```text
Driver.ResumeApprove
  obs := d.adapter.Approve(ctx, pid)
  d.ledgerMutationCommitted(...)       <-- R6-D
```

The mutation itself is performed inside `execution.RuntimeExecutor`
(`PatchManager.apply -> commitThroughKernel -> kernelbridge.Apply`). The durable
cursor is not scattered through that machinery; it brackets the one boundary the
driver owns, using `changed`/`created` (`MutationOutcome.MutationSucceeded`) as
the only positive evidence that bytes changed.

## 6. Exact integration point

| Change | File | Symbol |
|---|---|---|
| durable commit with observed post-state | `internal/runtime/durable/store.go` | `CommitExecutionWithDigest` (231), `apply` fold of `postconditionDigest` |
| read-only fresh-runtime inspection | `internal/runtime/durable/store.go` | `InspectCursors` (811), `inspectDecision` (865) |
| new honest decision | `internal/runtime/durable/types.go` | `DecisionUnknown` (314), `CursorInspection` (332) |
| pre-digest + commit helpers | `internal/runtime/autonomy/ledger.go` | `ledgerMutationPrepared` (167), `ledgerMutationCommitted` (205), `cursorDigest` |
| internal commit-path dedup | `internal/runtime/autonomy/ledger.go` | `ledgerExecutionCommitted` now skips an operation already committed at the mutation boundary |
| autonomous seam | `internal/runtime/autonomy/driver.go` | line 2387 (prepare), 2405-2406 (commit), 925-926 (approval) |
| fresh-runtime surface | `internal/runtime/autonomy/ledger.go`, `internal/runtime/compose/compose.go` | `Driver.ReconcileInterrupted` (268), `Application.ReconcileInterrupted` (489) |

The ordering implemented is the natural one the existing contract supports:

```text
PREPARED (pre-digest durable)
    -> MUTATION (kernel write)
    -> COMMITTED (marker durable; observed post-digest recorded)
```

The alternative ordering in `IdempotentRunner` (both digests durable *before* the
effect) is only possible when the caller can predict the post-state, which a real
mutation cannot. The postcondition is therefore recorded **after** the effect and
is `""` until commit — the D3 window below is the honest consequence.

## 7. Crash-window matrix

| Window | Durable state | Live state | Result | Safe? |
|---|---|---|---|---|
| **D1** before mutation | dispatched, post `""` | `current == pre` | `SAFE_RETRY` | yes |
| **D2** mutation + marker | committed, post recorded | `current == post` | `ALREADY_COMMITTED` | yes |
| **D3** mutation, marker absent | dispatched, post `""` | `current != pre` | `CONFLICT` | yes (no blind retry) |
| **D4** mismatch | dispatched, both digests set | matches neither | `CONFLICT` | yes |
| **D5** already committed | committed, post recorded | `current == post` | `ALREADY_COMMITTED`, zero writes | yes |
| **D6** safe retry | dispatched, post `""` | `current == pre` required | `SAFE_RETRY` iff agreement; else `CONFLICT` | yes |
| no cursor | none | any | `UNKNOWN` | honest |
| crash inside the provider/mutation before the post-digest can be computed | dispatched, post `""` | `current == pre` -> `SAFE_RETRY`; changed -> `CONFLICT` | never `ALREADY_COMMITTED` | yes |

`UNKNOWN` is represented by `durable.DecisionUnknown`, returned only by
`InspectCursors` when a task has no cursor. `Reconcile` itself never returns it.

## 8. D1–D7 test evidence

Added files:

```
NEW  internal/runtime/durable/r6d_commit_marker_test.go   D1–D6 + UNKNOWN + D7 kill -9
NEW  internal/runtime/autonomy/r6d_wiring_test.go          real-driver dispatch/commit/reconcile
```

Every durable test asserts **both** the reconciliation decision **and** the
actual workspace bytes; the read-only test additionally asserts no journal event,
no provider call, and no byte change.

| Test | Scenario | Journal/cursor assertion | Workspace assertion |
|---|---|---|---|
| `TestR6D_D1_PreMutationSafeRetry` | death before mutation | `SAFE_RETRY`, not committed, no marker | `a.txt == "v1"` |
| `TestR6D_D2_CommitThenRestartAlreadyCommitted` | mutation + marker | `ALREADY_COMMITTED`, committed, post recorded | `a.txt == "v2"` |
| `TestR6D_D3_MutationCommitMarkerGap` | mutation, no marker | `CONFLICT` (never `SAFE_RETRY`/`ALREADY_COMMITTED`) | `a.txt == "v2"` |
| `TestR6D_D4_DigestConflict` | third-party state | `CONFLICT` | `a.txt == "third-party"` |
| `TestR6D_D5_AlreadyCommittedDoesNotMutateAgain` | restart at post-state | `ALREADY_COMMITTED`, 0 further writes | bytes unchanged, still `"v2"` |
| `TestR6D_D6_SafeRetryRequiresPreStateAgreement` | agreement vs drift | `SAFE_RETRY` then `CONFLICT` | — |
| `TestR6D_NoCursorIsUnknownNotSafeRetry` | no evidence | `UNKNOWN` | — |
| `TestR6D_D7_ProcessDeathAndFreshRuntimeReconcile` | real `kill -9` | fresh store -> `ALREADY_COMMITTED`, committed | `a.txt == "patched"`, no second write |
| `TestR6D_AutonomousMutationDispatchesAndCommitsDurably` | real driver + approval | `CURSOR_DISPATCHED` pre == live pre; `EXECUTION_COMMITTED` post == computed post | workspace mutated |
| `TestR6D_AutonomousCrashAfterCommitReconciles` | post-commit / pre-terminal | fresh driver -> `ALREADY_COMMITTED` | committed bytes preserved, no duplicate |
| `TestR6D_AutonomousPreMutationParkIsSafeRetry` | parked at approval | fresh driver -> `SAFE_RETRY` | workspace untouched |
| `TestR6D_ReconcileIsReadOnly` | reconciliation never acts | no new journal event | no byte change, no provider call |

Reproduce:

```
go test -count=1 ./internal/runtime/durable/  -run TestR6D -v
go test -count=1 ./internal/runtime/autonomy/ -run TestR6D -v
```

## 9. Production changes

```
MOD  internal/runtime/durable/types.go   + DecisionUnknown, + CursorInspection (read-only)
MOD  internal/runtime/durable/store.go   + CommitExecutionWithDigest; commit event folds the
                                         observed post-digest; + InspectCursors + inspectDecision;
                                         CommitExecution delegates with ""
MOD  internal/runtime/autonomy/ledger.go + ledgerMutationPrepared / ledgerMutationCommitted /
                                         cursorDigest / ReconcileInterrupted; ledgerExecutionCommitted
                                         dedups an already-recorded mutation commit
MOD  internal/runtime/autonomy/driver.go + ledgerPendingOp/ledgerCommittedOp; prepare at the
                                         execution seam and the approval seam
MOD  internal/runtime/compose/compose.go + Application.ReconcileInterrupted (read-only bridge)
```

`Reconcile` and `ReconcileAll` are untouched. No new event type, no new journal,
no WAL, no queue, no resume, no retry. The commit reuses `EXECUTION_COMMITTED`
(payload gains an optional `postconditionDigest`), so replay of an older journal
is unaffected.

## 10. Remaining UNKNOWN states

| UNKNOWN | Why | Classification |
|---|---|---|
| The commit marker itself was never dispatched (death before `ledgerMutationPrepared`) | no cursor survives | honest `UNKNOWN`; the pre-digest was never taken |
| Verification ran / passed after the commit | verification is the apply gate before commit; its report is not the mutation marker | unchanged from R6-C; separate lifecycle |
| Whether the objective was satisfied | complete-independent of mutation commitment | preserved by design |
| Earlier-attempt commit evidence at the *cursor* level | the materialized cursor holds the latest operation, not a history; the `EXECUTION_COMMITTED` events remain in the journal | observability, not a correctness gap |
| D3's specific post-state | the postcondition is unknowable before the effect; `CONFLICT` is the conservative direction | accepted, not forced green |

## 11. Verification-marker assessment

R6-C recorded an optional durable "verification started" marker. R6-D **did not**
add one.

- The mutation commit marker already makes the decisive crash question —
  *did the mutation commit?* — answerable.
- Verification is the apply gate **before** commit; a death after a mutation that
  passed the gate but before any terminal record does not change the mutation's
  committed state. A verification marker would add a *second* lifecycle state
  without changing any R6-D decision.
- It is not required to make an existing truthful contract reachable, it has no
  unambiguous owner smaller than a new lifecycle mechanism, and the existing
  `VERIFICATION_RESULT` event already carries a result when the process survives.

**Classification: future work.** Add it only if a deterministic experiment shows
a specific decision that cannot be reached without it.

## 12. Panic-boundary assessment

R6-D **did not** add a process-level panic boundary.

- A panic in `PatchManager.apply` is already recovered to an error by
  `PatchManager.ApplyContext`; the executor rolls the `MutationSet` back.
- A panic **after** the mutation and before the driver's commit marker leaves the
  dispatched cursor with an empty post-digest, which a fresh runtime classifies
  as `SAFE_RETRY` (if unchanged) or `CONFLICT` (if changed) — never
  `ALREADY_COMMITTED` and never `PROVEN`. That is the truthful answer and it
  requires no new boundary.
- A panic cannot silently erase the durable marker: the marker is `fsync`ed
  before the driver can proceed.
- No deterministic test demonstrates that the durable contract becomes
  *incorrect* without a narrow panic-safe finalizer. The R6-C finding F7 (no
  panic boundary outside `PatchManager.apply`) stands as an observability/
  robustness gap, not a correctness one for the mutation state.

**Classification: unchanged future work.**

## 13. Objective-completion interaction

R6-D preserves the separation the objective contract exists for:

```text
ALREADY_COMMITTED  !=  PROVEN
```

`ledgerMutationCommitted` is called only on `changed`/`created` and records the
mutation commit marker. `ledgerExecutionCommitted` (the PROVEN-gated commit) now
**skips** an operation already committed at the mutation boundary, so a single
mutation never claims two commits. The objective still requires verification,
artifact observation, requirement discharge and provider evidence before
`ObjectiveCompletionAuthority` can return `PROVEN`. A fresh runtime that reads
`ALREADY_COMMITTED` learns a mutation committed; it learns nothing about whether
the objective was satisfied.

## 14. Automatic resume

R6-D introduces **no automatic resume and no automatic retry**.

`ReconcileInterrupted` is read-only: it appends no event, transitions no task,
invokes no provider, and mutates no byte (asserted by
`TestR6D_ReconcileIsReadOnly`). `SAFE_RETRY` means "the state is proven safe for
retry under the existing digest contract"; it does not mean "retry now".
`ALREADY_COMMITTED` does not continue the old objective. `CONFLICT` and
`UNKNOWN` require a human decision. `Driver.Run` does not consult cursors, so a
fresh run cannot silently act on an interrupted one.

## 15. Final crash-recovery capability assessment

| Question | Before R6-D | After R6-D |
|---|---|---|
| Can the autonomous path reach `CommitExecution` for a mutation? | only via `PROVEN` | yes, at the mutation boundary, independent of `PROVEN` |
| Does a durable pre-digest exist before the mutation? | no | yes |
| Does a durable commit marker exist after the mutation? | only after `PROVEN` | yes, immediately after `changed`/`created` |
| Can a fresh runtime prove `ALREADY_COMMITTED`? | no | yes (marker + post-digest) |
| Can it prove `SAFE_RETRY`? | no | yes (pre-digest agreement only) |
| Can it detect `CONFLICT`? | no | yes |
| Can it keep `UNKNOWN`? | yes | yes (no cursor) |
| Duplicate mutation after `ALREADY_COMMITTED`? | n/a | impossible (read-only reconcile) |
| Blind retry on `CONFLICT`? | n/a | impossible |
| Is `PROVEN` affected? | no | no |

Reduction achieved: the R6-C `mutation commit -> death -> UNKNOWN` window is now
`ALREADY_COMMITTED` (D2/D5/D7), `SAFE_RETRY` (D1/D6) or `CONFLICT` (D3/D4); only
the genuinely-evidence-less case (no cursor at all) remains `UNKNOWN`.

## 16. Next experiment

The next experiment is **R6-E — Durable Reconciliation Surface & Human Decision
Boundary** (chosen from evidence, and explicitly not started here).

Evidence for the choice:

- R6-D makes reconciliation *computable* and *reachable* (`Application.
  ReconcileInterrupted`), but nothing in production **consumes** it at startup;
  `InterruptedTask` exists and the UI/CLI do not yet present a reconciliation
  decision.
- The honest handling of D3 (`CONFLICT`) and the no-cursor `UNKNOWN` needs a
  human-facing decision surface before any recovery policy could be considered.
- A "predict the postcondition before the effect" design would move D3 toward
  `ALREADY_COMMITTED`, but that is a real design change and must be justified by
  a deterministic reproduction of a harmful D3 outcome.

Alternative candidates (not chosen): the R6-C verification marker and the panic
boundary, both of which R6-D assessed and left as future work because neither is
required to make an existing truthful contract reachable.

---

## Required final answers

1. **Authoritative autonomous mutation boundary?** `ExecutorAdapter.Execute` /
   `ExecutorAdapter.Approve` → `execution.RuntimeExecutor`, bracketed by the
   driver's `ledgerMutationPrepared` / `ledgerMutationCommitted` at
   `driver.go:2387`/`2405` and `driver.go:925`.
2. **Where is the durable mutation cursor owned?** `durable.TaskStore`
   (`DispatchCursor`, `CommitExecutionWithDigest`, the `EventExecutionCommitted`
   fold, `InspectCursors`).
3. **What does the pre-digest prove?** The exact workspace scope before the side
   effect. Matching it after a crash proves no mutation occurred (safe retry).
4. **What does the post-digest prove?** The exact workspace scope after the side
   effect. Matching it proves the operation committed and the workspace still
   holds the bytes it produced.
5. **When is the commit marker durable?** Immediately after the mutation lands
   and before any further observation/decision; `EXECUTION_COMMITTED` is a truth
   boundary (`fsync`).
6. **Can the autonomous path now reach `CommitExecution`?** Yes — at the mutation
   seam and the approval seam, independent of `PROVEN`.
7. **Can a fresh runtime distinguish `ALREADY_COMMITTED`?** Yes (D2/D5/D7).
8. **Can it distinguish `SAFE_RETRY`?** Yes, and only from pre-state agreement
   (D1/D6).
9. **Can it detect `CONFLICT`?** Yes (D3/D4).
10. **Which crash window remains `UNKNOWN`?** Death before any cursor was
    dispatched (no pre-digest) — and the verification/objective half of the
    lifecycle.
11. **Can the runtime avoid duplicate mutation after restart?** Yes:
    reconciliation is read-only, and a decision-driven caller writes only on
    `SAFE_RETRY`.
12. **Does reconciliation ever imply `PROVEN`?** No. `ALREADY_COMMITTED` is a
    mutation fact; `PROVEN` remains the objective authority's.
13. **Does R6-D introduce automatic resume?** No.
14. **Does R6-D introduce a WAL?** No — it reuses `ledger.ndjson`.
15. **Does R6-D introduce queueing?** No.
16. **What production code changed and why?** See §9: record the observed
    post-digest at commit; expose read-only `InspectCursors`/`DecisionUnknown`;
    dispatch/commit at the autonomous mutation seam; expose the read-only
    `ReconcileInterrupted` bridge.
17. **Are remaining gaps architectural requirements or observability/generality
    gaps?** D3's unknowable postcondition is a **contract boundary**, not a bug
    (the conservative `CONFLICT` is the truthful direction). The verification
    marker, the panic boundary and a startup reconciliation consumer are
    **observability/generalization gaps**, recorded as future work.

---

# Final principle

R6-D does not make IZEN recover. It makes the mutation boundary **durably
truthful**:

```text
ALREADY_COMMITTED != PROVEN
SAFE_RETRY        != RETRY NOW
CONFLICT          != FAILURE OF THE WORLD
UNKNOWN           != SUCCESS
```
