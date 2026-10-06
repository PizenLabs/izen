# Kernel Integration Map

**Status:** current as of the brownfield mutation slice (the `izen run`
brownfield path).
**Companion:** `docs/architecture/STRANGLER_MIGRATION.md`.

This document is the audit that precedes the integration. Every line was
established by reading the working tree, not from intent. It names the current
execution path, the authorization boundary, the mutation and observation seams,
the verification seam, and the exact point at which the kernel now replaces the
low-level filesystem primitive.

---

## 1. Current execution stacks

The repository still contains more than one execution stack; that is the premise
of the strangler described in `STRANGLER_MIGRATION.md`.

| Stack | Entry | Mutation primitive |
|---|---|---|
| A — headless `izen run` | `cmd/izen/runtime.go` → `app.Pipeline.Run` (`internal/app/pipeline.go`) → `internal/runtime/substrate` | `ConcreteSubstrate.commitWrite`/`commitDelete` → `internal/kernelbridge` (slice 6) |
| B — LEA layered engine | `internal/engine/pipeline` (plan synthesis only) | none live |
| C — TUI / `RuntimeExecutor` | `compose.Wire` → `autonomy.Driver` → `ExecutorAdapter` → `execution.RuntimeExecutor` | `PatchManager.apply` → `internal/kernelbridge` (slice 4) |
| O — `izen orchestrate` / `izen prompt` | `cmd/izen/orchestrate.go` → `cli.Wire` → `cli.Stack.Run` → `orchestrator.RunCycle` → `FileExecutor` | `FileExecutor.Commit`/`Rollback` → `internal/kernelbridge` (FileExecutor slice) |

Stack O is not a fourth stack; it is the CLI control plane, which until this
slice had no row here at all because it was missing from the census rather
than because it had no filesystem effect. It is listed now because the census
that found it is the reason this document's §4 grew an entry for
`internal/app/plan.go`'s brownfield branch.

Stack A's committed filesystem effects are on the kernel as of slice 6. What is
still outside it on stack A is **not** the commit path: it is the substrate's
process-execution surface (`OpExecCmd` → `ShellPort`, unchanged) and
`Substrate.ExecuteUnit`, which belongs to the separate
`internal/runtime/executor.RuntimeExecutor` authority and is not reached from
`izen run`. Those are recorded as remaining work in §4, not claimed here.

## 2. Stack C call graph (as built)

```
RuntimeExecutor.Execute            internal/execution/executor.go
  → admission + authorization      internal/execution/admission*.go
  → bounded provider invocation    invokeMutation
  → Patch / ExecutionSpec held     pendingMutation
RuntimeExecutor.Approve            internal/execution/executor.go:2584
  → admission re-check (FILE_MUTATE)
  → MutationSet open + OCC gate
  → PatchManager.ApplyContext      internal/execution/patch.go
  → PatchManager.apply             patch derivation (diff → SEARCH/REPLACE → full content)
  → PatchManager.commitThroughKernel              ← THE SEAM (slice 4)
  → kernelbridge.Apply
  → runtime/kernel                 Engine.Open → Run → dispatch → verify → adjudicate
  → runtime/capabilities/filesystem file.write
  → MutationSet.Commit / Rollback  recovery
  → ExecutionEvidence sealed       evidence / state transition
```

## 3. Answers to the Phase A questions

**Current authorization boundary.**
Core authorization is `internal/core/authorization` (`MutationAuthorization`),
enforced in `RuntimeExecutor.Approve` through `x.auth.Authorizes(patchID, digest)`
plus a re-admission via `x.admission.Admit`. The kernel does not see this; it
executes the request the bridge hands it. The bridge builds the kernel `Grant`
from fixed seam policy (`kernelbridge.mutate`), exactly as its audited contract
specifies.

**Current ExecutionSpec / Grant representation.**
Kernel authority: `kernel.Spec` + `kernel.Contract` + `kernel.Grant` in
`runtime/kernel/spec.go` and `authorization.go`. The bridge composes these; Core
does not construct a `kernel.Spec` itself.

**Current RuntimeExecutor.** `internal/execution/executor.go`
(`type RuntimeExecutor`, `Execute`, `Approve`, `Reject`).

**Current mutation path.** `RuntimeExecutor.Approve` → `PatchManager.ApplyContext`
→ `PatchManager.apply` → (formerly `os.WriteFile`, now) `commitThroughKernel` →
`kernelbridge.Apply` → `file.write`.

**Current observation / read path.** Patch derivation re-reads the live target
with `os.ReadFile` inside `apply` (derivation, not an effect). Context
compilation reads through `CapabilityAuthority` (`internal/execution/capability`).
Native tool calls read through `kernelbridge.ReadFiles` (slice 3).

**Current verification path.** The apply gate is
`internal/execution/verifier` (`Verifier.RunAllFor`), attached to the
`PatchManager` and recorded on the `MutationSet`; it runs inside the apply
boundary. It is a domain verifier (language/syntax), independent of the kernel's
content verifier.

**Current evidence / state transition path.** `MutationSet` outcomes →
`ExecutionResult.Mutations` → `ExecutionProof` / sealed `ExecutionEvidence`
(`internal/execution/evidence.go`).

**Current rollback / checkpoint path.** `MutationSet.RollbackTo` plus
`PatchManager.restoreFromShadowBackup` / `createShadowBackup`
(`.izen/checkpoints`). This is recovery and is deliberately NOT migrated.

**Callers that must remain compatible.** `RuntimeExecutor.Approve`,
`internal/execution/engine_admission.go` (`e.Patches.Apply`), and every test that
drives `PatchManager.Apply` / `ApplyContext`. Their signatures are unchanged;
`apply` is now context-threaded internally and `Apply` remains the contextless
entry point.

**Legacy path removed / replaced.** The two `os.WriteFile` calls in
`PatchManager.apply` (the execution writes) and the two `os.MkdirAll` calls that
created the destination directory before the grant was checked.

## 4. Classification of remaining filesystem sites

| Site | Class | Reason |
|---|---|---|
| `createShadowBackup`, `appendMutationLog`, `patch.Store` | KEEP | `.izen` runtime bookkeeping, not a mutation target |
| `restoreFromShadowBackup`, `MutationSet.RollbackTo` | EXEMPT | recovery/rollback, out of scope |
| `os.ReadFile` in `apply`, fresh-context retry | KEEP | patch derivation against live bytes |
| `internal/runtime/executor/file_executor.go` `atomicWrite`/`Rollback` | REPLACED (FileExecutor slice) | the transaction stayed in Core; only the final filesystem effect moved. See §7 |
| `internal/app/plan.go` brownfield branch → `internal/resource/file.FileResource.Write` | **REPLACED (brownfield slice)** | the graph/resource layer no longer owns the effect. The planner lowers each artifact to a `coreMutationTarget`, which submits a `substrate.Proposal` to the same Core authority Path A uses. See §8 |
| `internal/app/pipeline.go` `ensureParentDirs` | REMOVED | the eager `os.MkdirAll` is gone; the kernel's `file.write` capability creates parent directories as part of the authorized, evidence-backed mutation |
| `internal/fs/txfs.go` `TxFS.Commit` | EXEMPT | zero production callers; dead. Its rollback siblings are recovery |
| `internal/substrate/patch.go` `ApplyPatch` | EXEMPT | zero production callers; dead |
| `internal/runtime/substrate` `Substrate.ExecuteUnit` | EXEMPT | belongs to `internal/runtime/executor.RuntimeExecutor`, which has no production constructor; dead |
| `internal/execution/boundary.go` `RollbackAndVerify` | EXEMPT | recovery: restores a caller-supplied `originals` map after a DAG abort. Never places a new mutation |
| `internal/patch/applicator.go` `FileApplicator.Apply` | EXEMPT | DI-wired but never invoked; `Engine.Apply` has no production caller. Dead |
| `internal/infrastructure/capabilities/osfile.go` `OSFile.Write`/`Remove` | EXEMPT | every production call site writes a `.izen/` bookkeeping path; `main.go`'s instance is stored in `compose.Capabilities.File` and never read |
| `internal/resource/file/file.go` `Write`/`Delete`/`Restore` | EXEMPT | no longer on the production brownfield path; the brownfield planner binds `coreMutationTarget`, and the adapter refuses `Restore` rather than delegating. Remains a capability primitive for callers that hold it directly |
| `internal/runtime/substrate` `ConcreteSubstrate.commitWrite`/`commitDelete` | REPLACED (slice 6) | stack A's commit path now crosses `internal/kernelbridge` |
| `internal/runtime/substrate` rollback (`writeForRecovery`/`removeForRecovery`) | EXEMPT | recovery; Core owns undo and it never migrates |
| `internal/runtime/substrate` `writeEvidenceProof` | KEEP | `.izen` bookkeeping, not a mutation target |
| `internal/runtime/substrate` `Substrate.ExecuteUnit` → `FilePort.Write` | REPLACE (future) | belongs to `internal/runtime/executor.RuntimeExecutor`, a separate authority |
| `internal/runtime/substrate` `osShellPort` / `OpExecCmd` | REPLACE (future) | process execution: a different capability, a different seam |

### 4.1 The canonicality question, answered from the call graph

> Can any authorized IZEN operation currently mutate a workspace without
> crossing `internal/kernelbridge`?

**Not via `izen run`.** The brownfield branch used to be the counterexample:
`izen run` on any workspace `defaultDetector` classifies as brownfield — which
is any workspace containing a non-hidden directory, source file, or manifest —
took `internal/app/plan.go:316-340`, built raw `*file.FileResource` targets
(`internal/planner/brownfield/brownfield.go:218`), lowered them to
`op.OpWriteFile`, and executed them through `internal/graph/node.go:102` →
`internal/resource/file/file.go:213`, a bare `os.WriteFile`. No transaction
owner, no Core authorization, no kernel, no evidence.

That branch now routes through Core (§8). The mutation the planner describes
becomes a `substrate.Proposal` submitted to the same `ProposalExecutor` Path A
uses; the final effect crosses `internal/kernelbridge` under an explicit grant
and is adjudicated into `MutationEvidence`. The remaining process-execution
surface (`OpExecCmd` → `ShellPort`) is a different capability and is recorded
in §4, not claimed here.

The greenfield branch stages through `TxFS` and only reaches the substrate when
`p.tx.StagedPaths()` is non-empty (`internal/app/pipeline.go:499`). The
brownfield branch no longer needs to stage: each described write is submitted
to the substrate directly, because its verify command must run against the live
workspace.

## 5. The seams, stated once

There are four now, and they share a shape because they are the same decision
taken on four paths. The brownfield seam delegates one level further: it hands
a proposal to Core's existing authority (the Path A seam) rather than reaching
the bridge itself, so the final filesystem effect still crosses exactly one
door.

| Path | Seam | Delegates to |
|---|---|---|
| C — `RuntimeExecutor` | `internal/execution/patch.go : commitThroughKernel` | `kernelbridge.Apply` |
| A — `izen run` | `internal/runtime/substrate/kernelcommit.go : commitWrite` / `commitDelete` | `kernelbridge.Apply` / `kernelbridge.Delete` |
| O — `izen orchestrate` | `internal/runtime/executor/kernelcommit.go : placeThroughKernel` / `removeThroughKernel` | `kernelbridge.Apply` / `kernelbridge.Delete` |
| BF — `izen run` brownfield | `internal/planner/brownfield/coremutation.go : submit` | `substrate.ConcreteSubstrate.Execute` → Path A seam → `kernelbridge.Apply` / `kernelbridge.Delete` |

The brownfield seam is authoritative for the same reason the others are: it is
the only place on its path that hands the graph's described mutation to a Core
authority, and it owns no filesystem primitive of its own. Core's authority is
the only component that places the bytes, and it delegates that final effect to
`internal/kernelbridge`.


## 6. Stack A call graph (as built, slice 6)

```
izen run                              cmd/izen/runtime.go
  → app.NewPipeline(WithSubstrate(ConcreteSubstrate))
  → Pipeline.Run                      internal/app/pipeline.go
  → proposalFromTx                    Core: staging buffer → proposal
  → ConcreteSubstrate.Execute         Core: admission, transaction, rollback, verification
  → commitWrite / commitDelete        ← THE SEAM (slice 6)
  → kernelbridge.Apply / Delete
  → runtime/kernel                    Engine.Open → Run → dispatch → verify → adjudicate
  → runtime/capabilities/filesystem   file.write / file.delete
  → ExecutionProof.Mutations          evidence / state transition
  → .izen/substrate/<id>.proof        durable Core evidence
```

**Authorization boundary.** Core's own use-time confinement: `substrateRel`
lexical containment plus the FD-anchored `scope.Root.Verify`, both inside
`confinedTarget`, which runs before the kernel is asked. The kernel re-checks
confinement independently from its own root handle, so the guarantee does not
rest on that call alone. The bridge builds the grant from fixed seam policy
(`kernelbridge.mutate` / `kernelbridge.delete`) naming exactly the destination
requested — never wider than what Path A could already do.

**Request / grant representation.** `substrate.Proposal` → `Operation` on the
Core side; `kernel.Spec` + `kernel.Contract` + `kernel.Grant` are composed by the
bridge. Core does not construct a `kernel.Spec`. `FILE_WRITE` is declared
`ContractPatch`: a whole-content replace does not distinguish creation from
overwrite, and `PATCH` under-claims rather than inventing a creation.

**Snapshot, transaction, rollback, recovery.** All Core, all in
`internal/runtime/substrate/snapshot.go`. The kernel owns no transaction state
and none was moved into it. The one thing slice 6 changed here is recovery's
ownership rule: a rollback only reverses a destination whose current bytes still
match what this transaction placed. Without it, the first time a concurrent
writer lost a race its rollback would restore over the winner's committed work.

**Evidence.** `primitive result → kernel evidence → Core evidence → verification
→ execution state` is written down in one artifact. Each `MutationEvidence`
carries the kernel execution id, the contract, the outcome, the verification
axis and the landing verdict; `ExecutionProof.Status` is set by the transaction,
never copied from an outcome. A `PROVEN` outcome for one file is not a `PROVEN`
objective, and the proof keeps the two apart.

**Architecture lock.** `TestKernelLock_PathAMutationRoutesThroughKernel` in
`test/architecture/kernel_lock_test.go`. It constrains the three functions that
make up Path A's execution surface (`Execute`, `commitWrite`, `commitDelete`) to
contain no direct workspace mutation and no `FilePort` write or remove, and keeps
an exact two-way register of the recovery and bookkeeping sites that are allowed
to. It targets the architectural execution path, not the repository: the substrate
package still legitimately contains `os.WriteFile`.

## 7. The orchestrate path (as built, FileExecutor slice)

```
izen orchestrate / izen prompt         cmd/izen/orchestrate.go
  → cli.Wire                          internal/cli/cli.go (binds the workspace root)
  → cli.Stack.Run
  → Orchestrator.RunCycle             internal/runtime/orchestrator/engine.go
  → approval gate                     ui.WaitForApproval → gate.Evaluate → ActionExecute
  → FileExecutor.PrepareSnapshot      Core: snapshot + use-time confinement
  → FileExecutor.Commit               Core: materialization, symbol baseline, rollback policy
  → placeThroughKernel                ← THE SEAM
  → kernelbridge.Apply / Delete
  → runtime/kernel                    Engine.Open → Run → dispatch → verify → adjudicate
  → runtime/capabilities/filesystem   file.write / file.delete
  → FileExecutor.Rollback             Core: failWithRollback, snapshot restore
```

**Authorization boundary.** Unchanged and still Core's: `RunCycle` evaluates an
`authorization.ApprovalEvent` against an armed gate session and commits only on
`authorization.ActionExecute` (`internal/runtime/orchestrator/engine.go:252`).
The kernel does not see that decision and does not need to; it executes the
request the bridge hands it under a grant naming exactly one destination.

**Request / grant representation.** `ProposedMutation` → a workspace-relative
target plus resolved content. `kernel.Spec` / `kernel.Grant` are composed by the
bridge from fixed seam policy; Core constructs neither. `ContractPatch` is
declared for the same reason as Path A — a whole-content replace does not
distinguish creation from overwrite, and under-claiming is the safe direction.

**Snapshot, transaction, rollback, recovery.** All Core, all still in
`internal/runtime/executor/file_executor.go`. `FileBackup` remains the single
source of truth for rollback; `failWithRollback` remains the policy that a
commit failure undoes the snapshot; the kernel holds no transaction state and
none was moved into it.

**What moved, exactly.** The final filesystem effect of both directions. The
hand-rolled temp-file-and-rename protocol (`os.MkdirAll`, `os.CreateTemp`,
`os.Chmod`, `os.Rename`) and the rollback's `os.WriteFile`/`os.Chmod`/
`os.Remove` are gone. The kernel's write capability already performs the same
protocol — stage to a temp file in the destination directory, preserve the
destination's permission bits, rename into place, delete the staging file on
every path that does not — so nothing about the atomicity guarantee was
reimplemented on the Core side.

Two behavioural differences follow from moving, and both are recorded rather
than papered over:

  - **Durability.** The legacy `atomicWrite` called `tmp.Sync()` before the
    rename and best-effort `fsyncDir` after it. The kernel capability does
    neither. This is the same trade Path A and Path C already made; the
    durability property is the kernel's, and it is now stated in one place
    instead of three.
  - **Rollback will not delete a directory.** `os.Remove` removed an empty
    directory; the kernel's `file.delete` refuses one outright, because
    removing a tree is a different operation with a different blast radius.
    This is strictly safer: rollback exists to undo what the transaction
    produced, and a directory the transaction never created is not that.

**Evidence.** `commitThroughKernel` returns the kernel's `Applied` alongside
its error, and `failWithKernelWrite` annotates the rollback with whether the
log records the destination as written. That is the distinction Core needs: a
refused write changed nothing, and a write that reached disk but failed its
independent re-read did. Both are rolled back; only the second is reported as
an executed mutation.

**Construction requirement.** An executor must be bound to a workspace
(`WithWorkspace`, or `WithScopeRoot`, which declares both from one handle).
The grant is formed over one root, so an executor without one has nothing to
authorize against, and it refuses with `ErrUnboundWorkspace` rather than
falling back to the process working directory — which would place bytes outside
the grant the caller believes it holds. `cli.Wire` binds `root` at the one
place that already knows it. The same call also means `izen orchestrate` is now
confined to its `-dir`, which it was not before.

**Architecture lock.** `TestKernelLock_OrchestrateMutationRoutesThroughKernel`
in `test/architecture/kernel_lock_test.go`. It pins the four functions that
place or remove bytes (`Commit`, `Rollback`, `placeThroughKernel`,
`removeThroughKernel`) to contain no direct mutation primitive, asserts each
still calls its recorded seam, and asserts each still exists — so the lock
cannot be satisfied by deleting the migration instead of preserving it. It
targets the architectural execution path, not the repository:
`PrepareSnapshot` still reads the filesystem, and that is legitimate.

**Behavioural proof.**
`internal/runtime/executor/file_executor_kernel_seam_test.go` asserts the
observable consequences rather than the wiring: a commit whose target resolves
outside the workspace through a symlink is refused and the file beyond the
boundary is byte-for-byte unchanged; a confined target is still written with
the resolved bytes; both rollback directions still work; and an unbound
executor mutates nothing.

## 8. The brownfield path (as built, brownfield slice)

```
izen run                              cmd/izen/runtime.go
  → app.Pipeline.Run                  internal/app/pipeline.go
  → app.Pipeline.plan                 internal/app/plan.go (brownfield branch)
  → brownfield.BrownfieldPlanner.Plan internal/planner/brownfield/brownfield.go
  → p.fileTarget                      GRAPH/RESOURCE: target + desired content + intent
  → coreMutationTarget                internal/planner/brownfield/coremutation.go
  → substrate.ProposalExecutor.Execute ← THE CORE BOUNDARY (existing authority)
  → ConcreteSubstrate.Execute         Core: admission, transaction, rollback, verification
  → commitWrite / commitDelete        ← THE SEAM
  → kernelbridge.Apply / Delete
  → runtime/kernel                    Engine.Open → Run → dispatch → verify → adjudicate
  → runtime/capabilities/filesystem   file.write / file.delete
  → MutationEvidence + ExecutionProof Core evidence (.izen/substrate/<id>.proof)
  → bf-verify command node            Core-side command verification
```

### 8.1 What the graph layer owns, and what it does not

The graph still sequences the plan and drives the closed-loop repair: write
nodes in dependency order, a verify command node depending on every write, and
`InjectRepairOps` when the verify command fails. What it no longer owns is the
filesystem effect. Each write node targets a `coreMutationTarget`, which
describes exactly one mutation — a workspace-relative target, the desired
content, and the operation intent — and submits it to the Core authority. The
resource layer is a request shaper, not an executor; it never calls a
filesystem primitive and never constructs or widens a kernel Grant.

### 8.2 The operation is truthful

Brownfield planning lowers file artifacts to whole-content writes, so the
intent is `op.OpWriteFile` and the substrate maps it to `substrate.OpFileWrite`.
Core declares that under the same `kernelbridge.ContractPatch` Path A uses: a
whole-content replace does not distinguish creation from overwrite, and PATCH
is the honest under-claim. The kernel still reports creation truthfully through
`Applied.Created`, derived from its own pre-write observation rather than from
anything the planner asserted. A delete intent maps to `substrate.OpFileDelete`
and is refused if it is not a durable removal.

### 8.3 Authorization, transaction, verification, evidence

All four are Core's and none of them moved into the graph or the kernel.

- **Authorization / admission.** `ConcreteSubstrate.Execute` → `confinedTarget`
  applies lexical containment (`substrateRel`) and the use-time
  FD-anchored `scope.Root.Verify`, so a symlink swapped in after planning fails
  closed with `ErrWorkspaceEscape` before the kernel is asked. The kernel then
  re-checks confinement independently from its own root handle and admits the
  execution under a grant naming exactly the requested destination.
- **Transaction.** The substrate snapshots the target before the mutation and
  rolls the whole proposal back on any failure; the kernel owns no transaction
  state. This is the same transaction owner Path A uses, not a second one.
- **Verification.** `verifyProposal` re-anchors the payload by AST symbol
  extraction before commit, and the kernel's independent content verifier
  re-reads the bytes from disk through code that did not write them.
- **Evidence.** Each mutation produces `MutationEvidence` carrying the kernel
  execution id, contract, outcome, verification axis and landing verdict, and
  the transaction writes a durable `.izen/substrate/<id>.proof` plus a stored
  evidence record. A write that reached disk and failed its re-read is reported
  as unproven and rolled back, never as a completed mutation.

### 8.4 Removed byproducts

- `internal/app/plan.go`'s eager `ensureParentDirs` (`os.MkdirAll`) is gone.
  The kernel's `file.write` capability creates parent directories as part of
  the authorized mutation, so the brownfield path no longer opens a directory
  before the grant is checked.
- `internal/resource/file/file.go` is no longer a production workspace
  mutation authority. The brownfield planner binds `coreMutationTarget`; the
  adapter delegates only read/validate/snapshot and refuses `Restore` rather
  than delegating to `FileResource.Restore`.

### 8.5 Architecture lock

`TestKernelLock_BrownfieldMutationRoutesThroughKernel` in
`test/architecture/kernel_lock_test.go`. It pins the execution files, asserts
the planner routes every file target through `p.fileTarget` and that
`fileTarget` wraps it in `coreMutationTarget` (failing closed with
`ErrNoMutationAuthority`), asserts the seam's `WriteContext`/`DeleteContext` →
`submit` → authority `Execute` chain, asserts the pipeline binds the substrate,
and asserts the graph dispatch prefers the context-aware Core contract. It
fails in both directions: a direct primitive reappearing in the execution files
is a bypass, and deleting the seam (or its `Execute` call) fails as a migration
removed rather than preserved. It is scoped to the brownfield execution path;
reads, snapshots and recovery elsewhere remain legitimate.

### 8.6 Behavioural proof

`internal/planner/brownfield/coremutation_test.go` asserts the observable
consequences against a real `*substrate.ConcreteSubstrate`: a truthful proposal
shape, the delete intent, fail-closed without an authority, an authority
refusal that is inert and produces no proof, context cancellation propagation,
an authorized mutation that changes the target and produces PROVEN primitive
and committed Core evidence, a symlink escape that mutates nothing beyond the
boundary, and kernel-created parent directories. The end-to-end `izen run`
smoke test is `TestPipelineBrownfieldMutationCrossesKernelAuthority` in
`internal/app/brownfield_kernel_test.go`: it begins with an existing workspace
and asserts the file changed and a PROVEN proof was written.
