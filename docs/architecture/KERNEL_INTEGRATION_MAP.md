# Kernel Integration Map

**Status:** current as of slice 5 (`file.delete`).
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
| A — headless `izen run` | `cmd/izen/runtime.go` → `app.Pipeline.Run` (`internal/app/pipeline.go`) → `internal/runtime/substrate` | `osFilePort.Write` / `Remove`, `substrate/engine.go` `OpFileWrite` / `OpFileDelete` |
| B — LEA layered engine | `internal/engine/pipeline` (plan synthesis only) | none live |
| C — TUI / `RuntimeExecutor` | `compose.Wire` → `autonomy.Driver` → `ExecutorAdapter` → `execution.RuntimeExecutor` | `PatchManager.apply` → `internal/kernelbridge` (slice 4) |

The target architecture names `RuntimeExecutor`; the integration is at stack C.
Stack A is a distinct authority with its own `FilePort`/`ShellPort` pipe and is
recorded as remaining strangler work, not silently claimed.

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

## 4. Classification of remaining filesystem sites (stack C)

| Site | Class | Reason |
|---|---|---|
| `createShadowBackup`, `appendMutationLog`, `patch.Store` | KEEP | `.izen` runtime bookkeeping, not a mutation target |
| `restoreFromShadowBackup`, `MutationSet.RollbackTo` | EXEMPT | recovery/rollback, out of scope |
| `os.ReadFile` in `apply`, fresh-context retry | KEEP | patch derivation against live bytes |
| `internal/runtime/executor/file_executor.go` `atomicWrite`/`Rollback` | REPLACE (future) | transactional executor; needs recovery to move |
| `internal/execution/boundary.go`, `internal/patch/applicator.go`, `internal/infrastructure/capabilities/osfile.go` | REPLACE (future) | other write authorities |
| `internal/runtime/substrate` (`FilePort.Write`/`Remove`) | REPLACE (future) | stack A authority, separate strangler target |

## 5. The seam, stated once

`internal/execution/patch.go : commitThroughKernel` is the canonical mutation
seam. It is authoritative because it is the only method on the canonical
execution path that places resolved patch bytes on disk; it now delegates that
placement to `internal/kernelbridge.Apply`, which is the only sanctioned path
from the legacy tree to `runtime/kernel`.
