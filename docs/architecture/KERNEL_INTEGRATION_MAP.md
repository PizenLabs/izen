# Kernel Integration Map

**Status:** current as of slice 6 (`izen run` / stack A's filesystem commit).
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
| `internal/runtime/executor/file_executor.go` `atomicWrite`/`Rollback` | REPLACE (future) | transactional executor; needs recovery to move |
| `internal/execution/boundary.go`, `internal/patch/applicator.go`, `internal/infrastructure/capabilities/osfile.go` | REPLACE (future) | other write authorities |
| `internal/runtime/substrate` `ConcreteSubstrate.commitWrite`/`commitDelete` | REPLACED (slice 6) | stack A's commit path now crosses `internal/kernelbridge` |
| `internal/runtime/substrate` rollback (`writeForRecovery`/`removeForRecovery`) | EXEMPT | recovery; Core owns undo and it never migrates |
| `internal/runtime/substrate` `writeEvidenceProof` | KEEP | `.izen` bookkeeping, not a mutation target |
| `internal/runtime/substrate` `Substrate.ExecuteUnit` → `FilePort.Write` | REPLACE (future) | belongs to `internal/runtime/executor.RuntimeExecutor`, a separate authority |
| `internal/runtime/substrate` `osShellPort` / `OpExecCmd` | REPLACE (future) | process execution: a different capability, a different seam |

## 5. The seams, stated once

There are two, and they are the same shape because they are the same decision
taken on two paths.

| Path | Seam | Delegates to |
|---|---|---|
| C — `RuntimeExecutor` | `internal/execution/patch.go : commitThroughKernel` | `kernelbridge.Apply` |
| A — `izen run` | `internal/runtime/substrate/kernelcommit.go : commitWrite` / `commitDelete` | `kernelbridge.Apply` / `kernelbridge.Delete` |

Each is authoritative because it is the only place on its path that places
resolved bytes on disk, and each delegates that placement to `internal/kernelbridge`,
the only sanctioned path from the legacy tree to `runtime/kernel`.

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
