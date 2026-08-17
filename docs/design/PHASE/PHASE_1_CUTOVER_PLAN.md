# Phase 1 — Cutover Plan: Route the Production TUI onto the RuntimeExecutor

**Status:** PLAN ONLY — no code changed during Phase 0. Every step below is a wiring decision, not a refactor, sequenced so the system stays green after each commit.
**Goal invariant:**

```
Autonomy Decision → RuntimeExecutor → Provider → Artifact → MutationSet → Verification → ExecutionProof → Canonical Events → UI Projection
```

**Design constraint:** the RuntimeExecutor + execution graph + canonical events + verifier are already built, tested, and wired in compose (`a.Executor`, compose.go:437). What is missing is **switching the interactive runtime onto them** and deleting the shadows. Steps are ordered so each commit is independently verifiable (`go build ./...`, `go test ./... -race`, `golangci-lint run ./...`).

---

## Cutover Surface (what Phase 1 touches)

| # | Concern | Current (Phase 0) | Target |
|---|---|---|---|
| S1 | `$prompt` / `$hot` / free-form entry | `runAutonomyRoutedCmd` → `handleMessageContent` → mode engines → direct provider | `runAutonomyRoutedCmd` → `executeAutonomyWorkspace` → **RuntimeExecutor** |
| S2 | Mutation apply | `m.execEng.Patches.ApplyContext` (7 call sites) | `m.executor.Approve` (single site) |
| S3 | Verification | none on real path | `x.verifier.RunAll()` at Approve (already in executor) |
| S4 | Canonical events | dormant only | emitted on every execution (already in executor/graph) |
| S5 | Approval gate | autonomy proposal + patch dock (2 gates) | 1 gate: executor `EventApprovalRequired`/`Approve` |
| S6 | Context/evidence | `fastTrackFileContext` full-file dump | bounded regions only (autonomy/handoff.go contract) |
| S7 | Provider invocation | 7+ direct UI sites | `x.provider` inside executor only |
| S8 | Transaction | `execEng` mutationSet | executor-owned MutationSet |
| S9 | Reclassification | `handleMessageContent` re-routes inside workspace | remove; executor/gateway owns classification |

---

## Step-by-Step

### Step 0 — Preflight (no behavior change)
- Add a runtime instrumentation gate behind env var `IZEN_RUNTIME_EXECUTOR=1` that routes `$prompt`/`$hot`/free-form through `m.executor.Execute` instead of the legacy handlers, while keeping the legacy path intact behind the flag. This makes cutover a config decision, reviewable in isolation.
- Ship `IZEN_RUNTIME_EXECUTOR=0` default (legacy behavior unchanged). Add `docs/design/PHASE/PHASE1_PROGRESS.md` to track flag state.

### Step 1 — Wire the verifier onto the real mutation path (independent, low risk)
- In `execution.NewEngine` (execution.go:79-97) attach the already-created verifier to the PatchManager: `p.SetVerifier(v)`. This activates the micro-fix gate (patch.go:850) on the legacy path immediately.
- Add a content-changed check before `recordMutationEvidence(..., OutcomeChanged, ...)` (patch.go:940-941) so a no-change apply reports no-change, not success.
- **Verify:** `execution_truth_test.go` already asserts the gate behavior with `SetVerifier` — extend it to assert `NewEngine` produces a non-nil `Patches.Verifier()`.

> Rationale: closes P0#2 (verification) without touching routing. Even if cutover stalls, the real path becomes verified.

### Step 2 — Unify target resolution behind one resolver
- Introduce a single `execution.ResolveTarget` used by: `resolveAutonomyBuildTarget` (autonomy_target.go:38), `resolveHotfixTarget` (commands.go:3388), `resolveMultiHotfixTargets` (multihotfix.go:50), and the executor's own strategy collect. Make the autonomy BUILD path and `$hot` call the same function.
- Delete the divergent UI regex resolvers once the shared one is used.
- **Verify:** all resolver tests still pass; add a cross-check test asserting autonomy BUILD and `$hot` resolve the same mention identically.

### Step 3 — Collapse the two authorization gates
- Make the autonomy proposal the **single capability gate** (`DecisionAskUser` → grant → re-Decide → execute, already implemented).
- Replace the patch proposal dock approval (Alt+A → `applyProposalCmd`) with the executor's `EventApprovalRequired` → `m.executor.Approve` (executor.go:599). The proposal still renders the diff; the approval now drives a real executor mutation.
- **Verify:** `proposals.go` apply path is replaced; `applyPatchWithDeadline` call sites (commands.go:931, proposals.go:521/634/658, multihotfix.go:435) migrate to `m.executor.Approve`.

### Step 4 — Route `$prompt` / `$hot` mutations through the executor (core cutover)
- In `executeAutonomyWorkspace` (autonomy_route.go:89), BUILD workspace: call `m.executor.Execute` with the autonomy trace (intent, target, required capabilities) instead of `executeAutonomyBuild` → `stageAutonomyBuild` → `runBuildCmd`.
- `$hot`: replace `handleHotfixCmd` → `proposeHotfixPatch` with `m.executor.Execute` (the executor already owns `strategy.Select`, patch creation, and the approval gate).
- The executor's `invokeMutation` already performs: context prepare → model invoke → artifact produce → proposal → approval → apply → verify → proof. This satisfies S2-S5, S7-S8 in one move.
- **Verify:** executor integration tests (executor_integration_test.go) + new UI tests asserting `runAutonomyRoutedCmd` with autonomy wired reaches `m.executor.Execute`.

### Step 5 — Restore bounded evidence context (S6)
- Stop injecting the entire file in `fastTrackFileContext` (alignment.go:103-128) for non-rewrite targets; emit only the bounded evidence regions already computed by `compileAutonomyBuildEvidence` (autonomy_route.go:233-255) and the strategy-owned context policy (executor.go:815 `compileContext`).
- **Verify:** `context_economy_test.go` assertions still hold with bounded injection.

### Step 6 — Propagate the autonomy trace into execution (handoff completeness)
- Extend the executor entry to accept the autonomy `Trace` (intent, confidence, target confidence, scope, rollback) so `targetConfidence` (engine.go:208-214) stops being dropped and flows into the strategy selection and proposal rendering.
- **Verify:** a new test asserts Trace fields reach `ExecutionResult`.

### Step 7 — Retire the shadows (after Steps 1-6 are green)
- Remove / un-wire: `runBuildFastTrack` direct-provider path, `proposeHotfixPatch`/`proposeBuildPatch`/`proposeMultiHotfixPatch` direct provider calls, `applyPatchWithDeadline` legacy apply, `handleMessageContent` reclassification inside autonomy-decided workspaces (S9), the dead projections (`timeline.Timeline` if unread, `runtime.LedgerBuilder` if unread, `events/audit` write-only), `router.Router` (compose.go:590), `modes/build` Executor/ApplyMutation, `PipelineRunner.ExecuteBuild`, `PatchQueue`/`StreamMonitor`, `strategy.Compile` if unused.
- Keep `execution.Engine` as the shared verification/gate library; delete only its role as the UI apply authority.
- **Verify:** `go vet`, full `-race` suite, and a manual `$prompt`/`$hot` smoke test confirming canonical events appear in the UI projection.

### Step 8 — Reconcile buses and observability (final cleanup)
- Decide `pkg/event` (`izen run`) fate: retire or explicitly document as separate product.
- Bridge telemetry bus is already one-way (compose.go:509); keep. Remove write-only audit collision (patch.go:584 vs audit.go JSON) by routing audit writes through `internal/events/audit` only.
- **Verify:** `golangci-lint` clean; audit replay still functional.

---

## Sequencing / Risk

| Step | Risk | Reversible | Depends on |
|---|---|---|---|
| 0 (flag) | none | yes | — |
| 1 (verifier) | low (may surface latent verification failures) | yes (flag) | 0 |
| 2 (resolver) | low | yes | 0 |
| 3 (gates) | medium (UX change) | yes (flag) | 0 |
| 4 (executor routing) | **high** (behavior change) | yes (flag off = legacy) | 1,2,3 |
| 5 (context) | medium | yes | 4 |
| 6 (handoff) | low | yes | 4 |
| 7 (delete shadows) | medium (dead-code removal) | no (restore from git) | 1-6 |
| 8 (buses) | low | yes | 7 |

**Rollback rule:** Steps 0-6 ship behind `IZEN_RUNTIME_EXECUTOR`; any regression flips the flag back. Step 7 is a one-way door — do it last, only after the flag is the default and soak-tested.

---

## Definition of Done (Phase 1)

1. `IZEN_RUNTIME_EXECUTOR` default is `1` for all TUI executions.
2. Every mutation on the real path runs `Verifier.RunAll` and produces `ExecutionProof`.
3. Canonical lifecycle events are emitted for every `$prompt`/`$hot`/build mutation and visible in the UI projection.
4. One authorization surface (autonomy proposal) + one mutation approval (executor Approve).
5. Evidence injection is bounded; no full-file dumps alongside evidence ledgers.
6. `applyPatchWithDeadline` and the direct UI provider mutation sites are gone.
7. `go build ./...`, `go test ./... -race`, `golangci-lint run ./...` all clean.