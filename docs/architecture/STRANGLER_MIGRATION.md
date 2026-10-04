# Strangler Migration: from three execution stacks to one kernel

**Status:** in progress. Slice 1 (`file.exists`) complete.
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
kernel. Two locks enforce it:

- `TestKernelLock_SingleKernelEntryPoint` — only `internal/kernelbridge` may import
  `runtime/kernel` or `runtime/capabilities/filesystem`. A caller that constructs a
  capability itself would get an `Observation` with no `Spec`, no `Grant`, no event
  and no verdict, so the import is locked too, not just the kernel package.
- `TestKernelLock_NoUnregisteredWorkspaceExistenceDecision` — a ratchet over every
  remaining existence decision in the execution packages. The allowlist is the
  strangler's work list; it may only shrink, and it fails in **both** directions
  (a new site is a new bypass; a stale entry means the list has stopped describing
  reality).

Both locks were verified to fail when violated: adding a stray `os.Stat` branch, a
direct kernel import, and restoring the deleted `os.Stat` each turn the build red.

### Failure has to be an answer

An unanswered question is never rendered as a negative answer. `Observation.Exists`
and `Observation.Absent` both return false when the execution did not reach PROVEN,
and `targetResolution.unproven()` lets the presentation layer say "the runtime
could not prove this" instead of asserting an absence nobody established.

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
| `test/architecture/kernel_lock_test.go` | new — the three locks |

---

## 5. Remaining work, in order

Each line is an allowlist entry in `test/architecture/kernel_lock_test.go` marked
`target-existence`. Delete the entry in the same change that strands the site.

| # | Site | What it decides |
|---|---|---|
| 2 | `internal/runtime/autonomy/adapter.go:777` | `TargetExists` / `TargetAbsent` evidence — literally the kernel's `FILE_PRESENT` / `FILE_ABSENT` concepts, implemented with a bare stat |
| 3 | `internal/runtime/autonomy/adapter.go:800` | `TargetExistence` pre-dispatch evidence for the IDEMPOTENT contract |
| 4 | `internal/runtime/handlers/handlers.go:605` | filters `@file` references by existence |
| 5 | `internal/ui/utils.go:63,74` | `@file` expansion in the composer |
| 6 | `internal/runtime/autonomy/preflight.go:526` | local-dependency feasibility |
| 7 | `internal/execution/executor.go:1069` | workspace evidence for context compilation |

Slices 2 and 3 are next for a reason: they are the runtime's own existence
evidence, they feed the IDEMPOTENT contract, and a post-hoc reading of them is
exactly the "already done" vs "done by this run" ambiguity the kernel's evidence
model exists to remove.

### Out of scope until the strangler completes

Per the migration decision, these are untouched and must stay untouched:

- provider integration
- recovery
- contract derivation
- planner architecture

---

## 6. Findings

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