# Strangler Migration: from three execution stacks to one kernel

**Status:** in progress. Slices 1 (`file.exists`), 2 (`file.write`), 3 (`file.read`),
4 (`file.write` on the canonical `RuntimeExecutor` mutation seam) and 5 (`file.delete`)
complete.
**Date:** 2026-10-04
**Branch:** `refactor/kernel`

---

## 1. Why this document exists

The Runtime Kernel (`runtime/kernel`) is now the substrate Izen intends to execute
everything through. It is not yet what executes anything.

Izen currently runs **three separate execution stacks**, none of which is the
kernel. Each answers its own workspace questions with whatever syscall was nearest,
which means a mutation target can be declared "present" by a bare `os.Stat` with
no event, no state, no evidence and no verification behind it.

Adopting the kernel by rewriting anything is not available: all three stacks are
live and one of them is the interactive product. So the migration is a **strangler
fig** — new route in front, old route deleted behind, one slice at a time, with a
lock that makes the old route impossible to grow.

This document is the map. It states what the three paths are, which order they are
strangled in, and what each slice is required to deliver.

---

## 2. The three execution paths

All references were verified against the working tree on the date above.

### Path A — headless `izen run`

```
izen run "<prompt>"
  → cmd/izen/runtime.go:312     pipeline.Run(ctx, app.Request{Intent, Targets})
  → internal/app/pipeline.go:290 (*Pipeline).Run
       IntentCompiler → changeset generator → substrate writes
       EventBus → auditevents.NewLogger(.izen/audit) → stderr
```

Wiring: `cmd/izen/runtime.go:198` (`app.NewPipeline`), with an LLM intent compiler,
a changeset generator, a `KnowledgeGraph`, a clarification callback and
`substrate.NewConcreteSubstrate(dir)`.

Scope: **headless only.** It never touches the TUI, the autonomy engine, the mode
engines, or the canonical `events` stream. It is reachable from exactly one place.

### Path B — LEA layered engine (`internal/engine/pipeline`)

```
Layer 0 knowledge resolution
Layer 1 capability detection
Layer 2 governed ExecutionContext assembly
Layer 3 routed stateless worker        (internal/engine/layer3)
Layer 4 validation DAG                 (internal/engine/layer4)
Layer 5 telemetry
```

Facade: `internal/engine/pipeline/facade.go:27` (`ExecutePlan`, `ValidatePatch`),
implemented by `*Engine` (`internal/engine/pipeline/engine.go:81`).

Inbound, in production: **plan-mode synthesis only** —
`internal/modes/plan.processFromLedger` → `synthesizeViaFacade` →
`Facade.ExecutePlan`.

Scope: it is not a general executor. Only `ExecutePlan` and the Layer-4
`ValidatePatch` have live callers. The rest of the layered engine is exercised by
its own tests only.

### Path C — TUI runtime stack — **this is what actually runs**

```
compose.Wire                      internal/runtime/compose/compose.go:486
  ├─ runtime/autonomy.Driver      (target resolution, manifest/requirement passes)
  ├─ execution.RuntimeExecutor    internal/execution/executor.go:387 Execute
  └─ presentation projection + legacy UI path
```

`RuntimeExecutor.Execute` inbound callers (from the code graph):
`internal/runtime/autonomy.{executeSubTask, executeSubTaskWithRetry, runProposalDAG,
ExecuteManifestPass, RequirementPassForExecutor, ResumeApproveProposal}`,
`internal/runtime/compose.{Wire, Bootstrap}`, and `cmd/izen.main`.

Two mutation authorities still compete inside this path:

| Concern | Authority today |
|---|---|
| Provider invocation | `m.provider.Execute` / `ExecuteStream` called directly from the UI — `internal/ui/agents.go:781`, `internal/ui/commands.go:4373`, and wired straight into the plan engine at `internal/ui/provider.go:147-148, 340-341` |
| Mutation | `internal/execution/executor.go:2584` → `PatchManager.ApplyContext` → `internal/execution/patch.go` |
| Target resolution | `internal/runtime/handlers/handlers.go:605`, `internal/runtime/autonomy/adapter.go:777,800`, and `internal/ui/autonomy_target.go` |

### Summary

| | Path | Reachable from | Kernel today |
|---|---|---|---|
| A | `izen run` pipeline | one CLI command | no |
| B | LEA layered engine | plan-mode synthesis | no |
| C | TUI runtime stack | every interactive action | no |

---

## 3. The strangler rule

One slice, four obligations. A slice that does not do all four is not finished:

1. **Route** one real user-facing execution through the kernel.
2. **Prove** the complete chain end to end: event → state → evidence →
   verification → PROVEN. Asserting only the final outcome would let every
   intermediate be deleted and the test would still pass.
3. **Delete** the old implementation path in the same change. Not deprecate it, not
   feature-flag it, not leave it as a fallback.
4. **Lock** it, so the old path cannot grow back and the bridge cannot be walked
   around.

### The seam

`internal/kernelbridge` is the only sanctioned path from the legacy tree to the
kernel. Three locks enforce it:

- `TestKernelLock_SingleKernelEntryPoint` — only `internal/kernelbridge` may import
  `runtime/kernel` or `runtime/capabilities/filesystem`. A caller that constructs a
  capability itself would get an `Observation` with no `Spec`, no `Grant`, no event
  and no verdict, so the import is locked too, not just the kernel package. Because
  that lock is real, the seam re-exports the two mutation contracts as
  `kernelbridge.ContractCreate` / `ContractPatch`; a sanctioned caller has to be
  able to obey the lock.
- `TestKernelLock_NoUnregisteredWorkspaceExistenceDecision` — a ratchet over every
  remaining existence decision in the execution packages. The allowlist is the
  strangler's work list; it may only shrink, and it fails in **both** directions
  (a new site is a new bypass; a stale entry means the list has stopped describing
  reality).
- `TestKernelLock_MigratedToolCallsOwnNoFilesystemOfTheirOwn` — after slices 2 and
  3, `internal/execution/toolcalls.go` may not call **any** workspace syscall. This
  one is deliberately not a specific-call list: that file's only correct future is
  to keep asking the seam, so any direct `os.*` filesystem call appearing there is a
  regression whatever its intent.

All four locks (including the slice-1 pin) were verified to fail when violated: a
stray `os.Stat` branch, a direct kernel import, a restored `os.WriteFile`, a
reintroduced `DispatchToolCalls`, and a restored `os.Stat` in the migrated file each
turn the build red.

### The seam is not a layer

`internal/kernelbridge` holds no execution state between calls, does no planning,
names no provider, implements no recovery, retries nothing, and decides no mutation
policy beyond which grant it builds. Each of its three directions is a pure function
of `(ctx, root, request)`:

| Direction | Entry point | Contract | Verifier |
|---|---|---|---|
| observe existence | `Observe` | `OBSERVE` | re-stats each target; agreement is the check |
| mutate | `Apply` | `CREATE` / `PATCH`, declared by the caller | re-reads each destination and compares content |
| read | `ReadFiles` | `OBSERVE` over `file.read` | re-reads each target; presence and absence are both passes, only disagreement fails |

Two rules make that checkable rather than aspirational:

- **the seam never decides what to do.** It composes a Spec, a Grant and a
  verifier and returns what the kernel adjudicated. An oversized request is
  refused, never truncated; a destination that escapes the workspace is passed to
  the kernel so the refusal is recorded in the kernel's own vocabulary; a write set
  whose calls disagree about the contract is refused rather than resolved.
- **no verdict is a field.** `Proven()`, `Exists()`, `Absent()`, `Landed()`,
  `Created()`, `Written()`, `Found()` and `Content()` are all derived, so a
  hand-built value cannot disagree with the state it claims to describe. There is no
  `Success`, `Verified`, `Applied`, `Completed` or `Proven` boolean anywhere in the
  seam or on its results.

### Failure has to be an answer

An unanswered question is never rendered as a negative answer. `Observation.Exists`
and `Observation.Absent` both return false when the execution did not reach PROVEN,
and `targetResolution.unproven()` lets the presentation layer say "the runtime
could not prove this" instead of asserting an absence nobody established.

`Applied.Landed` is the identical rule on the mutating side: write evidence beside
an unproven outcome describes a workspace the runtime cannot vouch for, and it is
not reported as a completed write.

### Refusals carry no state

A request refused before admission returns a zero `State`, because no execution
exists to describe. Its axes are empty rather than `NONE`, and an empty axis is not
a claim. Consumers decide from `Proven()`, `Landed()` and the evidence — never from
an axis on a refusal. `Verify` is set explicitly even then, because callers read it
directly.

---

## 4. Slice 1 — `file.exists`

### What it was

`internal/ui/autonomy_target.go` decided which file a user-facing mutation would
land on by calling `os.Stat` itself:

```go
if strings.Contains(filepath.ToSlash(target), "/") {
    if _, err := os.Stat(filepath.Join(root, target)); err == nil {
        return filepath.ToSlash(target), []string{filepath.ToSlash(target)}
    }
}
```

That boolean decided whether the build ran against a single unambiguous target,
paused on the human candidate selector, or reported the target as missing. A
mutation was authorized on the strength of it, with nothing recorded anywhere.

### What it is now

Every existence claim in the resolution is adjudicated by the kernel:

- one `OBSERVE` contract per question set, `RequiresVerification: true`
- one `file.exists` step per named target, under an explicit read-only grant that
  covers exactly those targets
- an independent verification seam that re-derives each fact and requires the
  evidence to **agree** with the filesystem — a genuinely absent target is a PASS,
  because the contract was to report truthfully, not to produce a particular answer
- `PROVEN` is the only outcome that yields a candidate

The resolution now returns a `targetResolution` carrying the kernel executions that
produced it, because the caller needs them to be truthful: the target-not-found
diagnosis used to assert "no file matching X exists in the workspace" and now
reports what was actually proven.

The workspace walk that remains is **enumeration, not observation**: it produces a
list of names to ask about. Every name that leaves the function has been proven
present by kernel evidence first, and a name the runtime could not account for is
dropped rather than offered — offering it would hand mutation authority to an
unverified claim.

### The proof

`internal/ui/autonomy_target_kernel_test.go` drives the real entry point
(`resolveAutonomyBuildTarget`, reached from `@path/to/file.go` in build mode) and
asserts each link separately:

| Link | Assertion |
|---|---|
| decision | exactly one candidate, the named file |
| terminal truth | `Outcome == PROVEN` |
| events | the ordered spine `execution.started → step.started → capability.invoked → evidence.produced → verification.started → verification.passed → execution.finished`, contiguous revisions, every event targeting the exact path |
| state | `SETTLED`; provider `DONE`; artifact `NONE`; mutation `NONE` — a settled invocation is not a completion |
| evidence | `FILE_PRESENT` about the exact resolved target, attributed to a step |
| verification | `PASSED`, never a skip |
| contract | `OBSERVE`, which forbids mutation |

Absence is proved too: `TestAutonomyTargetResolution_AbsentTargetIsProvenAbsent`
requires `FILE_ABSENT` evidence and a PROVEN outcome, which is the case a bare
`bool` could never express.

### Files

| File | Change |
|---|---|
| `internal/kernelbridge/kernelbridge.go` | new — the seam |
| `internal/kernelbridge/kernelbridge_test.go` | new — bridge-level proof |
| `internal/ui/autonomy_target.go` | routed through the bridge; `os.Stat` deleted |
| `internal/ui/runtime_cutover.go` | consumes the kernel-carrying result |
| `internal/ui/autonomy_target_kernel_test.go` | new — user-facing proof |
| `test/architecture/kernel_lock_test.go` | new — the locks |

---

## 5. Slice 2 — `file.write`, and slice 3 — `file.read`

Both slices migrate the same user-facing path: a native `write_file` /
`apply_patch` tool call arriving from the model, buffered for review, approved by
the human pressing `a` or `l` in the approval prompt.

```
key "a" / "l"   internal/ui/keys.go:1180,1184
  → internal/ui/model.go            applyToolCallBuffer
  → internal/execution/toolcalls.go ToolCallBuffer.ApplyApproved   (write)
                                     bufferWriteFile / bufferApplyPatch (read)
  → internal/kernelbridge           Apply / ReadFiles
  → runtime/kernel                  engine → dispatch → verify → adjudicate
  → runtime/capabilities/filesystem file.write / file.read
```

### What slice 2 was

`ToolCallBuffer.ApplyApproved` wrote the workspace itself:

```go
absPath := resolvePath(b.cwd, b.calls[i].Path)
if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil { ... }
if err := os.WriteFile(absPath, []byte(b.calls[i].Modified), 0644); err != nil { ... }
```

Five defects, all of them invisible on a cooperative filesystem:

1. no event, no state, no evidence, no verification — a write whose only witness is
   the syscall that performed it;
2. not atomic: `os.WriteFile` truncates and then writes, so a failure mid-write
   leaves a target no contract described;
3. unconfinement: `resolvePath` joined the working directory with whatever path the
   model asked for, so `../` left the workspace with no grant behind it;
4. the file's existing mode was discarded in favour of a hardcoded `0644`;
5. `ToolCallResult.IsNew` reported "created" from `orig == ""`, computed at
   BUFFERING time — so an existing zero-byte file was reported as newly created,
   and any file that appeared between buffering and approval was reported as a
   modification.

### What slice 2 is now

One `MUTATE` execution per approved batch:

- two steps per destination, in a fixed order: `file.exists` then `file.write`.
  The order is the program's only control flow and it is load-bearing — an
  observation taken *after* the write could not distinguish "created" from
  "overwritten", which is the question the caller has to answer;
- an explicit grant naming `file.exists` + `file.write` over exactly the requested
  destinations;
- a verifier that re-reads every destination and compares it against what was
  requested. It does **not** reuse the capability: replaying `file.write` would
  only re-run the implementation whose report is being judged;
- `Landed(target)` = write evidence **and** a PROVEN outcome. Write evidence beside
  an unproven outcome describes a workspace the runtime cannot vouch for, and is
  not reported as a completed write;
- `Created(target)` = proven ABSENT before the write, write evidence after. Both
  halves come from recorded evidence, so "created" cannot be asserted for a file the
  runtime only overwrote.

The contract is always `PATCH` — "the named destination ends up holding this
content" — because that is the one obligation the caller can state truthfully for
every call whether it creates or replaces. `CREATE`'s obligations are a strict
superset, so declaring `CREATE` for a call that turns out to overwrite would assert
the creation of a file that was already there. Whether a write *created* its
destination is a fact, not an obligation, and it is read back from evidence.

### What slice 3 was

`bufferWriteFile` and `bufferApplyPatch` read the baseline themselves and treated
**any** read error as "the file is empty":

```go
var orig string
if data, err := os.ReadFile(absPath); err == nil {
    orig = string(data)
}
```

A missing file, a permission error, a directory and an empty file were one thing.
The approval prompt then showed an empty baseline for an unreadable target, and the
approved write replaced whatever was there.

### What slice 3 is now

`readThroughKernel` asks the seam, and the three answers stay distinct:

| on disk | `Found` | `Absent` | `Content` | what the caller does |
|---|---|---|---|---|
| file with bytes | true | false | the bytes | diff against real content |
| zero-byte file | true | false | `""` | diff shows the addition |
| absent | false | **true** | `""` | `write_file` proceeds, `apply_patch` refuses |
| unreadable / no verdict | false | false | `""` | the buffer refuses with the cause |

`FILE_ABSENT` is a real observed fact that reaches `PROVEN`, not an error disguised
as success. `apply_patch` against an absent target is **refused** rather than
satisfied: reading some other file's bytes to compute a baseline would be inventing
the thing the mutation is supposed to change.

### Deleted in the same change

| Deleted | Where | Why |
|---|---|---|
| `ToolCallBuffer.ApplyApproved`'s write loop | `internal/execution/toolcalls.go` | replaced by `kernelbridge.Apply` |
| `dispatchWriteFile` | `internal/execution/toolcalls.go` | a **second** `write_file` implementation, with no callers, that wrote straight to disk with no grant, no evidence and no verification |
| `DispatchToolCalls` / `dispatchToolCall` / `dispatchApplyPatch` | `internal/execution/toolcalls.go` | only callers of the above |
| `BufferedToolCall.IsNew` | `internal/execution/toolcalls.go` | obsolete state: decided by `orig == ""` at buffering time, now established by the kernel's pre-write observation |
| `resolvePath`'s write use | `internal/execution/toolcalls.go` | the write no longer resolves a path itself; the read keeps it for naming only |

### The proof

`internal/ui/toolcall_write_kernel_test.go` drives the real approval command and
asserts each link separately — a removed intermediate cannot pass:

| Link | Assertion |
|---|---|
| entry point | `m.applyToolCallBuffer()`, the exact command the `a`/`l` key handler runs |
| execution | `execution.started` exactly once, `step.started` present, `SETTLED` |
| authorization | a named grant exists, permits `file.write`, and covers **only** the requested destination |
| invocation | `capability.invoked` for `file.exists` **and** `file.write`, each naming the exact target |
| mutation | the bytes are re-read from disk by the test's own `os.ReadFile` |
| evidence | `FILE_WRITTEN` attributed to a declared step, from `file.write`, verdict `PASS`, correct byte count; `FILE_PRESENT` from a second capability; presence observed **before** `mutation.applied` |
| state | mutation axis `APPLIED`, `mutation.applied` exactly once |
| verification | axis `PASSED` (never a skip), and `mutation.applied → verification.started → verification.passed` in that order |
| outcome | `PROVEN`, `Landed`, `Created`, contract `PATCH` and non-forbidding |
| replay | `kernel.Fold` rebuilds the reported state from the log, with the same outcome |

The creation semantics are pinned separately, because they are what the deleted
heuristic got wrong: `TestToolCallWrite_CreatesATargetAndSaysSo` creates the target
*after* buffering (a buffer-time answer would now be stale),
`TestToolCallWrite_AnEmptyExistingFileIsNotACreation` covers the zero-byte file, and
`TestToolCallRead_AnUnreadableTargetIsRefusedNotSilentlyEmpty` covers the data-loss
case.

### Files

| File | Change |
|---|---|
| `internal/kernelbridge/apply.go` | new — the mutating direction |
| `internal/kernelbridge/read.go` | new — the reading direction |
| `internal/kernelbridge/kernelbridge.go` | presence projection widened to `file.read` / `file.write`; package doc states what the seam must never become |
| `internal/kernelbridge/apply_test.go`, `read_test.go` | new — seam-level proof |
| `internal/execution/toolcalls.go` | read and write routed through the seam; old paths deleted |
| `internal/ui/model.go` | the kernel execution rides along on `applyAllResultMsg` |
| `internal/ui/view.go` | the approval icon derives from the diff, not a stale flag |
| `internal/ui/toolcall_write_kernel_test.go` | new — user-facing proof |
| `runtime/capabilities/filesystem/filesystem.go` | confinement completed (see finding below) |
| `test/architecture/kernel_lock_test.go` | fourth lock added |

---

## 5a. Slice 4 — the canonical `RuntimeExecutor` mutation seam, and slice 5 — `file.delete`

Slices 2 and 3 migrated the native tool-call path. Slice 4 migrates the
**canonical** execution authority — the one named in
`docs/architecture/AUDIT-RUNTIME-AUTHORITY.md` as the declared authority — and
slice 5 completes the kernel's mutation vocabulary.

### Where the seam is

```
RuntimeExecutor.Approve            internal/execution/executor.go:2584
  → PatchManager.ApplyContext      internal/execution/patch.go
  → PatchManager.apply             (patch derivation: diff → SEARCH/REPLACE → full content)
  → commitThroughKernel            internal/execution/patch.go  ← the seam
  → kernelbridge.Apply
  → runtime/kernel                 engine → dispatch → verify → adjudicate
  → runtime/capabilities/filesystem file.write
```

The write happens in exactly one method, `PatchManager.apply`, at two former
`os.WriteFile` sites (the `FILE_CREATE` branch and the main branch). Both now
call `commitThroughKernel`, which invokes `kernelbridge.Apply` and fails closed
when the kernel does not report the destination landed. Patch **derivation**
does not move: deciding what a target should hold is the Control Plane's job,
and the kernel owns only the deterministic placement of the resolved bytes.

The two `os.MkdirAll` calls that used to create the destination's parent
directory were also removed. Creating a destination's directory is itself a
workspace mutation, and doing it before the grant is checked is a mutation the
grant does not cover. The kernel's `file.write` performs the `MkdirAll` inside
the same authorized step that writes the file.

### Concurrent writers: an unproven write is still an executed mutation

The kernel's content verifier re-reads the destination after writing it. A
concurrent writer that lands between the capability's write and that re-read
produces `FILE_WRITTEN` evidence beside an unproven outcome. `Applied.Landed`
deliberately returns false there — the runtime cannot vouch for the final bytes —
but the write did happen, and the enclosing `MutationSet` must roll it back and
report the attempt as tainted rather than as a no-op.

`Applied.Wrote` draws exactly that distinction from the log, and
`PatchManager.failKernelWrite` projects it into the existing `MutationEvidence`
vocabulary (`ApplyExecuted`, `FilesystemChanged`). This is what keeps
`TestPhase3ConcurrentOutBandWriterNeverPersistsPartialState` truthful: a
rolled-back apply whose bytes reached disk is `Tainted`, and a refused apply that
never wrote is not.

### The atomic replace preserves the destination's mode

The kernel write commits by renaming a staged file into place, which installs a
new inode. The capability now reads the destination's existing permission bits
before staging and applies them to the staged file (defaulting to 0644 for a new
target). Without that, every write through the canonical path would have reset an
existing file's mode — the exact defect slice 2's report attributed to the old
`os.WriteFile` path.

### What slice 4 is not

It is not a rewrite of `PatchManager`. Shadow backups, the transaction record,
the OCC gate, the micro-fix / syntax verifier, and the rollback path remain in
Core and are deliberately untouched:

| Site | Classification | Reason |
|---|---|---|
| `createShadowBackup` (`patch.go`) | KEEP | writes the runtime's OWN `.izen/checkpoints` bookkeeping, not a mutation target |
| `appendMutationLog` (`patch.go`) | KEEP | `.izen/audit` bookkeeping |
| `restoreFromShadowBackup` (`patch.go`) | EXEMPT | recovery/rollback, out of scope until the kernel owns transactions |
| `patch.Store` (`patch.go`) | KEEP | `.izen` patch bookkeeping |
| `os.ReadFile` in `apply` | KEEP | patch derivation reads against the live file; not a mutation |

### Slice 5 — `file.delete`

The kernel's contract vocabulary has always named `DELETE` (`ContractDelete`,
and the `target_absent` / `mutation_applied` clauses that adjudicate it), but no
capability implemented a removal, so the contract was unsatisfiable. Slice 5
fills that gap:

- `runtime/kernel`: `CapabilityID("file.delete")` and the mutating evidence kind
  `FILE_DELETED`. A write and a delete are both workspace changes
  (`EvidenceKind.Mutating`), so either can satisfy the mutation boundary, but
  they stay distinct kinds so a contract that names a write is never satisfied by
  a delete.
- `runtime/capabilities/filesystem`: `deleteCap` removes exactly one confined
  file, refuses a directory, reports `PASS` with `FILE_DELETED` + a fresh
  `FILE_ABSENT` when it removed one, and `NO_OP` with `FILE_ABSENT` when the
  target was already gone. An already-absent target therefore does **not**
  satisfy a DELETE contract, which demands a durable change.
- `internal/kernelbridge/delete.go`: `Delete` runs one DELETE execution per
  target under a grant naming exactly those targets, with an independent
  verifier that re-derives absence.

### Changed files

| File | Change |
|---|---|
| `internal/execution/patch.go` | both execution writes routed through `commitThroughKernel`; `os.MkdirAll` removed; `apply` takes a context |
| `runtime/kernel/capability.go` | `file.delete` capability |
| `runtime/kernel/evidence.go` | `FILE_DELETED`; `Mutating` and `mutatedTargets` cover it |
| `runtime/kernel/dispatch.go` | mutation axis projected from any mutating evidence kind |
| `runtime/capabilities/filesystem/filesystem.go` | `deleteCap` |
| `internal/kernelbridge/delete.go` | new — the deleting direction |
| `internal/kernelbridge/apply.go` | `Removed` / `Deleted` result predicates |
| `internal/kernelbridge/kernelbridge.go` | presence projection includes `file.delete` |
| `test/architecture/kernel_lock_test.go` | `TestKernelLock_CanonicalMutationRoutesThroughKernel` |

---

## 6. Remaining work, in order

Each line is an allowlist entry in `test/architecture/kernel_lock_test.go` marked
`target-existence`. Delete the entry in the same change that strands the site.

| # | Site | What it decides |
|---|---|---|
| 4 | `internal/runtime/autonomy/adapter.go:777` | `TargetExists` / `TargetAbsent` evidence — literally the kernel's `FILE_PRESENT` / `FILE_ABSENT` concepts, implemented with a bare stat |
| 5 | `internal/runtime/autonomy/adapter.go:800` | `TargetExistence` pre-dispatch evidence for the IDEMPOTENT contract |
| 6 | `internal/runtime/handlers/handlers.go:605` | filters `@file` references by existence |
| 7 | `internal/ui/utils.go:63,74` | `@file` expansion in the composer |
| 8 | `internal/runtime/autonomy/preflight.go:526` | local-dependency feasibility |
| 9 | `internal/execution/executor.go:1069` | workspace evidence for context compilation |

Entries 2 and 3 — the ones slice 1's author marked as next — were **not** taken,
because slices 2 and 3 went somewhere else. They are the runtime's own existence
evidence and they feed the IDEMPOTENT contract, and both of those need an
IDEMPOTENT contract the kernel does not have yet. Choosing them would have meant
deriving a contract, which is out of scope. They remain the next candidates once
the IDEMPOTENT obligation exists.

Mutation sites NOT taken, and why:

| Site | Why not yet |
|---|---|
| `internal/runtime/executor/file_executor.go` — `PrepareSnapshot` / `Commit` / `atomicWrite` / `Rollback` | transactional: snapshot, staged write, symbol baseline, use-time confinement, automatic rollback. Migrating it means recovery, which is out of scope. |
| `internal/execution/patch.go` — `ApplyContext`, `restoreFromShadowBackup` `os.WriteFile` at :475, `patch.Store` | Slice 4 moved the two EXECUTION writes (`:661`/`:793`) onto the kernel. What remains is rollback (`restoreFromShadowBackup`), which is recovery, and `.izen` bookkeeping (`patch.Store`), which is not a mutation target. |
| `internal/runtime/substrate/substrate.go` (`osFilePort.Write`/`Remove`) and `internal/runtime/substrate/engine.go` | Path A (`izen run`) owns its own `FilePort`/`ShellPort` transaction pipe, separate from the `RuntimeExecutor` path slice 4 migrates. It is a distinct execution authority and remains a strangler target. |
| `internal/runtime/executor/file_executor.go` — `PrepareSnapshot` / `Commit` / `atomicWrite` / `Rollback` | transactional: snapshot, staged write, symbol baseline, use-time confinement, automatic rollback. Migrating it means recovery, which is out of scope. |
| `internal/execution/boundary.go:76`, `internal/patch/applicator.go:44`, `internal/infrastructure/capabilities/osfile.go:179` | other write authorities, each with its own approval and transaction machinery. |

### Out of scope until the strangler completes

Per the migration decision, these are untouched and must stay untouched:

- provider integration
- recovery
- resume
- retry
- contract derivation
- planner architecture
- intent heuristics
- command.run
- UI redesign

---

## 7. Findings

### The filesystem capability's confinement was lexical only

Found while proving slice 2, and fixed in the same change because slice 2 makes
`file.write` reachable from a user-facing path for the first time.

`Capability.resolve` refused absolute targets and targets climbing out with `..`,
and then re-checked that the joined absolute path was textually under the root. That
is not confinement. A symlink **inside** the workspace pointing at a directory
outside it satisfies every one of those checks — the cleaned relative path has no
`..`, and the joined path is textually under the root — while the bytes land
somewhere else.

Verified before the fix: `kernelbridge.Apply` on `link/escaped.go`, with `link` a
symlink to a sibling of the workspace root, returned **PROVEN**
(`target_delta_applied, verification_satisfied`) with the file written outside the
root. The verifier confirmed the escape, because it followed the same symlink and
re-read the same bytes.

That defeats the point of a grant. A grant names workspace-relative paths; a
capability that can be redirected out of the workspace through one symlink is a
capability no grant can reason about. The `TestConfinementRefusesEscapingTargets`
test called itself "the security contract" and enumerated "every traversal shape"
while covering four lexical shapes and no symlink.

`resolve` now has a second half: the target's symlink-resolved path must stay under
the root's symlink-resolved path, comparing resolved to resolved so a root reached
through a symlink (`/var` → `/private/var` on macOS) does not false-positive. The
deepest existing ancestor is resolved and the not-yet-created components appended,
because the creation case is exactly where a symlinked parent redirects the write.
Symlinks resolving **inside** the root stay permitted —
`TestConfinementAllowsSymlinksResolvingInsideTheRoot` — because refusing those would
be theatre, and a check that has to be disabled routinely is not a check.

### A read's verification compares lengths, not bytes

The kernel's evidence vocabulary records observations — a kind, a target, a byte
count — and deliberately not payloads. `readMeasurement` therefore re-reads each
target and compares the **length** against the length the capability recorded, so
two independent reads of the same path returning the same length but different bytes
would pass.

This is a real TOCTOU window, not a closed one, and it is stated rather than papered
over: closing it needs content-bearing evidence in the kernel, which is a kernel
change and therefore out of scope for these slices. The bytes the seam returns are
the verifier's re-read, so the caller receives the reading that was independently
re-checked rather than the one the capability reported. The write verifier does not
have this limitation — it compares content, because the request gives it content to
compare against.

### The neighbouring negative-rules audit is vacuous

`test/architecture/negative_rules_test.go` walks `internal/engine`,
`internal/execution`, `internal/runtime`, `internal/providers`, `internal/modes`,
`internal/app/model` and `internal/core` as **relative** paths, from a working
directory of `test/architecture/`. Those paths do not exist there, and the walk
error is discarded (`_ = filepath.Walk(...)`), so the test scans zero files and
passes. Verified: reading `./internal` from that directory returns
`open internal: no such file or directory`.

Re-rooting it (via the same `repoRoot` helper the new lock uses) surfaces five real
violations that have been invisible:

```
internal/providers/capability/heuristic.go : "claude-sonnet-4"
internal/modes/plan/budget.go              : "qwen2.5-coder:7b", "claude-sonnet-4", "gpt-4o-mini", "gpt-4o"
```

Static model defaults are exactly the defect the audit exists to catch. This is not
fixed here — it is a different defect in a different subsystem, and fixing it means
editing provider and plan budget configuration. It needs its own change.

### Only `runtime/` was previously held to any architecture rule

The kernel subtree was added to the negative audit in the change that created it.
This document's locks extend that: they constrain `internal/` against the kernel,
which nothing did before.

### The write verifier's failure branch is not reachable from outside the seam

`contentVerifier` fails when a destination's on-disk bytes differ from the request.
That branch is the check that gives a mutation its meaning, and it is deliberately
not faked in a test: producing it requires the file to change between the write and
the re-read, which from outside the seam needs a hook the seam does not expose and
should not — a caller able to interpose between a capability and its verifier could
also interpose between a mutation and its verification. In production it is
reachable by exactly the thing it exists to catch: a concurrent writer. A runtime
that cannot survive that should fail loudly, and this one does.

### Dead write authorities are easy to miss and expensive to leave

`DispatchToolCalls` / `dispatchWriteFile` / `dispatchApplyPatch` were a complete
second `write_file` implementation in the same file as the live one: no approval
gate, no grant, no confinement, no evidence, no verification — and **no callers at
all**. Nothing fails for an exported function nobody calls, and nothing in the
linter flags a duplicate implementation either. It would have survived indefinitely
and been the obvious thing for the next person to call.

Deleting it was not tidiness; it was the "do not leave two competing
implementations" obligation. The new lock checks for it by name, because a deleted
implementation that can be re-added verbatim is not deleted.