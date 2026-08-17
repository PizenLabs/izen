# Phase 1 — Implementation Report: RuntimeExecutor Cutover

**Status:** IMPLEMENTED (steps 0-6 shipped behind the `IZEN_RUNTIME_EXECUTOR`
rollback flag; steps 7-8 partially scoped — see below).
**Companion:** `PHASE_1_CUTOVER_PLAN.md`, `PHASE_0_ARCHITECTURE_BASELINE.md`,
`PHASE_0_FINDINGS.md`, `PHASE1_PROGRESS.md`.
**Baseline frozen at:** `ec1d04e`.

---

## 1. What changed

The production TUI now has an explicit, flag-gated execution authority
convergence. With `IZEN_RUNTIME_EXECUTOR=1`, every autonomy-decided BUILD
mutation (`$prompt`, `$hot`, free-form in build mode, `/build` with a staged
FILE_MUTATE plan, investigate→build handoff, and the autonomy target-selector
resume) routes through the **RuntimeExecutor**. The executor owns provider
invocation, context compilation, patch creation, the approval gate, apply,
verification and the canonical lifecycle events. The UI collects input, renders
state, presents the autonomy proposal (capability gate), issues the approval
authorization, and projects runtime events and results.

Two independent safety fixes were made on the way:

1. **Verifier wiring (P0#2).** `execution.NewEngine` constructed a `Verifier`
   but never attached it to its own `PatchManager` (`p.SetVerifier(v)` was
   absent), so the production mutation path applied unverified. It is now
   attached (`internal/execution/execution.go`), activating the micro-fix gate
   (`patch.go` `if pm.verifier != nil`) on every `PatchManager.Apply`.
2. **Mutation truth.** The apply path reports `OutcomeNoChange` (never
   `OutcomeChanged`) when the resolved final content is byte-for-byte identical
   to the on-disk original (`internal/execution/patch.go`).
3. **Executor apply gate.** The RuntimeExecutor ran its verifier only *after*
   the MutationSet commit, so a verifier failure left the mutation on disk and
   still reported `changed`. The verifier is now attached to the executor's own
   PatchManager so the gate runs *inside* `Apply` and restores the shadow backup
   on failure (`internal/execution/executor.go` Approve).
4. **Empty-artifact rejection.** A model response with no usable mutation
   artifact is now an execution failure before any approval surface, never an
   empty proposal (`internal/execution/executor.go` invokeMutation).

## 2. Exact old execution path (pre-cutover, flag disabled)

```
handleInput → intentFromInput → dispatchASTIntent
  ├─ $prompt → routePromptDirective → runAutonomyRoutedCmd → autonomy.Decide
  ├─ $hot    → routeHotfixThroughAutonomy → runAutonomyRoutedCmd → autonomy.Decide
  └─ free-form → routeFreeInput → runAutonomyRoutedCmd → autonomy.Decide
        → dispatchAutonomyTrace → executeAutonomyWorkspace (BUILD)
            ├─ $hot → handleHotfixCmd → proposeHotfixPatch → m.provider.Execute (direct)
            │        → hotfixProposalMsg → applyHotfixPatch → applyPatchWithDeadline
            │        → m.execEng.Patches.ApplyContext → PatchManager.Apply → os.WriteFile
            └─ executeAutonomyBuild → stageAutonomyBuild → planResultMsg{IsFastTrack}
                 → runBuildCmd → runBuildFastTrack → m.provider.ExecuteStream (direct)
                 → proposeBuildPatch → m.provider.Execute (direct) → proposal dock
                 → Alt+A → applyProposalCmd → applyPatchWithDeadline
                 → m.execEng.Patches.ApplyContext → PatchManager.Apply → os.WriteFile
```

Verification: none on the real path (PatchManager.verifier was nil).
Canonical events: none on the real path.

## 3. Exact new execution path (flag enabled)

```
User Input → Parser/Autonomy → Execution Intent
  → executeAutonomyViaRuntime (UI: prompt + autonomy trace)
  → m.gateway.SelectStrategy(prompt)          ← canonical strategy + target resolution
  → (ambiguity → autonomy selector / not-found, explicit, before any model call)
  → execution.ExecuteRequest{Targets, Strategy, Evidence, Intent, Confidence, Scope}
  → m.executor.Execute(...)                   ← RuntimeExecutor (owns provider)
      → selectStrategy → target resolution → context compilation (bounded)
      → invokeMutation → provider → artifact → patch
      → approval gate (PendingPatchID)
  → executionResultUpdate (UI projection) → proposal dock
  → Alt+A → authorizeExecutorApproval (AuthorizationEngine → executor.SetAuthorization)
  → m.executor.Approve(...)                   ← RuntimeExecutor (owns apply)
      → PatchManager.Apply (runtime-owned, verifier as apply gate)
      → Verifier.RunAll → MutationSet commit → ExecutionProof
      → canonical lifecycle events → UI projection
```

The call graph converges on `RuntimeExecutor`. The UI never calls a provider,
a PatchManager, or a MutationSet on the flagged path.

## 4. Files changed

Production:
- `internal/ui/runtime_flag.go` — `IZEN_RUNTIME_EXECUTOR` gate (new).
- `internal/ui/runtime_cutover.go` — `executeAutonomyViaRuntime`,
  `runRuntimeExecuteCmd`, `runStagedBuildViaRuntime`, `runRuntimePrompt`,
  `resolvedTargetsForExecution` (new).
- `internal/ui/autonomy_route.go` — BUILD workspace routes through the executor
  when the flag is enabled.
- `internal/ui/commands.go` — `/build` auto-trigger, investigate→build handoff,
  build-mode free-form route through the executor when enabled.
- `internal/ui/autonomy_target.go` — target-selector resume routes through the
  executor when enabled.
- `internal/ui/runtime_executor.go` — `runExecutorApproveCmd` authorizes via
  the AuthorizationEngine before `Approve`; `authorizeExecutorApproval`.
- `internal/ui/gateway.go` — `executorPendingTargets` captured at proposal
  staging; cleared on terminal outcomes.
- `internal/ui/model.go` — `executorPendingTargets` field.
- `internal/ui/keys.go` — approval/reject paths clear the pending target set.
- `internal/execution/execution.go` — `p.SetVerifier(v)` in `NewEngine`.
- `internal/execution/patch.go` — no-change evidence rule.
- `internal/execution/executor.go` — `ExecuteRequest`/`ExecutionProof` handoff
  fields, bounded evidence injection, verifier-as-apply-gate, empty-artifact
  rejection.
- `internal/autonomy/engine.go` — `Trace.TargetConfidence` preserved.
- `internal/events/events.go`, `internal/ui/program.go` — gofmt (comment
  alignment, pre-existing nits).

Tests:
- `internal/execution/verifier_wiring_test.go` (new)
- `internal/execution/lynx_independence_test.go` (new)
- `internal/retrieval/router_test.go` (new)
- `internal/ui/runtime_cutover_test.go` (new)
- `internal/execution/execution_multifile_test.go` (apply-failure fixture)

Docs:
- `docs/design/PHASE/PHASE1_PROGRESS.md` (new)
- `docs/design/PHASE/PHASE_1_IMPLEMENTATION_REPORT.md` (this file)

## 5. Direct provider invocation sites — removed or retained

| Site | File | Status | Reason |
|---|---|---|---|
| fast-track build stream (`ExecuteStream`) | commands.go | **retained, flag-off only** | rollback path; unreachable on the enabled architecture |
| build propose (`Execute`) | commands.go | retained, flag-off only | rollback path |
| build full-rewrite retry (`Execute`) | commands.go | retained, flag-off only | rollback path |
| hotfix (`Execute`) | commands.go | retained, flag-off only | rollback path |
| hotfix retry (`Execute`) | commands.go | retained, flag-off only | rollback path |
| hybrid template (`Execute`) | commands.go | retained, flag-off only | rollback path |
| multi-hotfix (`Execute`) | multihotfix.go | retained, flag-off only | rollback path |
| chat stream (`ExecuteStream`) | stream.go | **retained (read-only)** | `/ask` chat, no mutation |
| commit agent (`Execute`) | agents.go | retained (read-only) | no mutation |
| diagnose/ask (`Execute`) | commands.go | retained (read-only) | no mutation |
| RuntimeExecutor (`Execute`) | executor.go | **the authority** | all flagged-path mutations |

No direct provider site was deleted in Phase 1: every mutation-capable UI site
remains as the **flag-off rollback path**. Deleting them is the Step 7 one-way
door, deferred until the flag is the default and soaked.

## 6. Verifier wiring proof

- `internal/execution/execution.go`: `NewEngine` calls `p.SetVerifier(v)`; the
  engine's own `PatchManager` now carries the verifier.
- `internal/execution/verifier_wiring_test.go`:
  `TestNewEngineWiresVerifierIntoPatchManager` asserts
  `eng.Patches.Verifier() == eng.Verifier != nil`.
- `internal/execution/executor.go` Approve: `x.patches.SetVerifier(x.verifier)`
  makes the verifier the **apply gate** inside the runtime's PatchManager.
- `internal/execution/patch.go`: no-change applies record `OutcomeNoChange`
  (never `changed`); `TestApplyIdenticalContentReportsNoChange` / `...Changed`.
- `internal/ui/runtime_cutover_test.go`:
  `TestRuntimeCutoverVerificationFailureIsNotSuccess` — a failing verifier
  fails the approve, rolls back the boundary, and restores the file.
- Legacy micro-fix gate (`patch.go` `if pm.verifier != nil`) is now reachable.

Invariant 3 holds: production mutations have a wired Verifier on both the
flag-off legacy path (Step 1) and the flag-on executor path (verifier-as-gate).

## 7. Target resolution authority

The canonical authority is the **IntentGateway → `strategy.Select`**
(`internal/execution/intent.go`, `internal/execution/strategy/selector.go`):
`collectTargets`/`resolveTarget` resolve `@` scopes, bare filenames, and
workspace evidence deterministically with bounded fuzzy lookup, classifying
explicit/resolved/inferred/unresolved/ambiguous.

On the flagged path the UI calls `m.gateway.SelectStrategy(prompt)` and passes
the profile + resolved targets into `ExecuteRequest`. No UI regex resolver
(`resolveHotfixTarget`, `resolveMultiHotfixTargets`, `resolveAutonomyBuildTarget`)
reinterprets the target for execution. `resolveAutonomyBuildTarget` remains
ONLY as the explicit-ambiguity surface (candidate selector / not-found
diagnosis) — it decides a *human pause*, never the execution target.
`TestRuntimeCutoverFlagOnAmbiguousTargetStaysExplicit` pins that no provider
call precedes the ambiguity diagnosis.

## 8. Approval boundary

Two boundaries remain, and they are genuinely different security surfaces:

1. **Autonomy proposal (`DecisionAskUser` → Execute)** — the *capability* gate:
   whether the runtime may act in the mutation domain. It grants capability and
   re-Decides. Unchanged.
2. **Executor approval gate (`EventApprovalRequired` → Alt+A)** — the *mutation*
   gate: whether this specific file mutation is applied. `Alt+A` now issues a
   fresh `MutationAuthorization` through the production `AuthorizationEngine`
   over exactly the held execution's target files and attaches it to the
   executor (`authorizeExecutorApproval`), then `RuntimeExecutor.Approve`
   applies, verifies and commits. The UI performs no second independent
   apply-authorization; it authorizes the boundary.

The old second UI apply gate (`authorizeBuildExecution` → `m.execEng.SetAuthorization`
→ legacy `applyPatchWithDeadline`) is only reachable on the flag-off rollback
path.

## 9. Fast-track / `$hot` path

With the flag on, `$hot` routes through the executor exactly like any targeted
mutation: `gateway.SelectStrategy` → `TargetedMutation` → provider → artifact →
approval gate → `Approve`. No special legacy hotfix authority is created on the
enabled path (`TestRuntimeCutoverFlagOnRoutesHotThroughExecutor`). The
deterministic 0-token local-replacement shortcut remains on the flag-off path
only. A deterministic mutation on the enabled path is still an executor
execution: the executor's `profile.Deterministic` branch and the empty-artifact
guard keep the runtime authoritative for every strategy.

## 10. Evidence / context behavior

- The autonomy-compiled deterministic evidence ledger (Context Evidence Ledger +
  redundancy findings) is the **authoritative evidence contract**: it crosses
  into the mutation prompt as `### EVIDENCE LEDGER`
  (`internal/execution/executor.go` `buildMutationUserPrompt`).
- Full-file target context remains **supporting context only**, bounded by the
  strategy-owned context policy and the 200 KB executor cap
  (`compileContext`). The model is never handed a full-file dump *instead of*
  the evidence.
- `TestRuntimeCutoverFlagOnRoutesPromptMutationThroughExecutor` asserts the
  evidence ledger reaches the provider request.
- The legacy `fastTrackFileContext` full-file injection remains on the flag-off
  rollback path only; the enabled architecture no longer uses it.

## 11. Autonomy handoff behavior

`autonomy.Decide` computes `targetConfidence` but historically dropped it. It is
now stored on `Trace.TargetConfidence` (`internal/autonomy/engine.go`) and
flows into `ExecuteRequest` alongside `Intent`, `IntentConfidence`, and `Scope`
(`internal/execution/executor.go`). The executor preserves them on
`ExecutionProof` so `$inspect` sees the decision facts and the already
classified intent is never re-classified downstream (`executeAutonomyViaRuntime`
forces `TargetedMutation` for a decided mutation intent — a decided mutation
cannot degrade into a read-only plan artifact).
`TestRuntimeCutoverFlagOnRoutesPromptMutationThroughExecutor` asserts the proof
carries intent/confidence/target-confidence/scope.

## 12. Canonical event behavior

The RuntimeExecutor drives the runtime-owned execution graph on every flagged
execution; every transition emits a canonical lifecycle event
(`graph/graph.go` via `x.emit` → shared bus). The UI subscribes to the full
lifecycle set (`program.go`). `TestRuntimeCutoverApproveAppliesThroughExecutor`
asserts the sequence `execution.started → strategy.selected → target.resolved →
model.invoked → artifact.produced → mutation.started → mutation.completed →
verification.completed → execution.finished` on the cutover path. The UI
remains a pure projection — no fabricated progress events are emitted.

`pkg/event` (7 types) serves the `izen run` product lineage and is documented
as a separate product stack (Phase 0 §15); it was not touched. The
`.izen/audit/mutations.log` write (`patch.go appendMutationLog`) is retained:
`guardrail.go` parses it for the infinite-autofix-loop guard, so routing audit
writes exclusively through `internal/events/audit` would break the guardrail —
flagged for Phase 3.

## 13. Lynx optional-capability behavior

- `internal/retrieval/router_test.go`: `lx` absent from PATH → native Go search
  engine (fallback); `lx` present → Lynx hybrid engine; explicit engine override
  honored.
- `internal/execution/lynx_independence_test.go`: the executor's strategy +
  target resolution is **byte-identical** whether the global router runs native
  or Lynx. Lynx never selects the execution path, never resolves mutation
  targets, and is never an execution dependency.
- The execution architecture is identical regardless of Lynx availability — no
  `lx available → executor` / `lx absent → legacy` branch exists.

## 14. Tests executed and results

| Command | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `golangci-lint run ./...` | **0 issues** |
| `go test ./... -count=1` | all packages pass |
| `go test -race ./internal/{execution,ui,autonomy,retrieval,runtime/handlers,architecture}/...` | clean |
| `go test ./internal/ui/ -run TestRuntimeCutover -count=1` | 8/8 pass |
| `go test ./internal/execution/ -run 'TestNewEngineWiresVerifier\|TestApplyIdenticalContent\|TestApplyRealChange'` | pass |

Known flake: `internal/runtime TestAskPlanBuildFlow` intermittently fails
`ledger failures = 0, want 2` under `-count=3` (async LedgerBuilder projection
timing). Reproduced on the pre-cutover baseline; passes in isolation and under
`-count=1`. Not caused by this phase.

## 15. Remaining legacy paths

All retained deliberately as the flag-off rollback boundary (Phase 3 pruning):
- `runBuildCmd` / `runBuildFastTrack` direct-provider streaming
- `proposeHotfixPatch` / `proposeBuildPatch` / `proposeMultiHotfixPatch` /
  `proposeHybridTemplatePatch` direct provider calls
- `applyPatchWithDeadline` legacy apply
- `handleMessageContent` build-mode reclassification (flag-off)
- `fastTrackFileContext` full-file injection (flag-off)
- dormant infrastructure (`router.Router` at compose.go:590, `timeline.Timeline`
  read accessor unused, `runtime.LedgerBuilder` consumer, `modes/build`
  Executor/ApplyMutation, `PipelineRunner.ExecuteBuild`, `PatchQueue`,
  `StreamMonitor`, `strategy.Compile`) — untouched, marked for Phase 3.

## 16. Known risks

1. **Flag default is disabled.** The enabled architecture is fully wired and
   tested but not the process default. Flipping to enabled-by-default and the
   Step 7 deletion are the post-soak one-way door. Until then the two
   architectures coexist behind the flag.
2. **Executor apply contract.** The runtime's PatchManager rejects a plain
   full-content rewrite of an existing file when the new content is < 80% of the
   original length (ambiguous-snippet guard), unless the model emits a
   SEARCH/REPLACE or unified diff. The executor's bounded-mutation system prompt
   requests those forms; small-file edits are best covered by SEARCH/REPLACE.
   The legacy hotfix path's whole-file-overwrite contract is not reproduced on
   the enabled path.
3. **Deterministic hotfix shortcut.** The flag-on `$hot` path is model-assisted
   (the legacy 0-token local-replacement shortcut stays flag-off). Not a
   correctness gap, but a token-cost change.
4. **Verifier cost.** Wiring the verifier into both `NewEngine.Patches` and the
   executor's PatchManager means the configured steps (default `go fmt`, `go vet`,
   `go test`) run per apply. This is the intended micro-fix gate but is heavier
   than the pre-cutover unverified path.
5. **Pre-existing flaky test** `TestAskPlanBuildFlow` (see §14).

## 17. Invariant confirmation

| # | Invariant | Status |
|---|---|---|
| 1 | All production TUI mutations originate from RuntimeExecutor | **HOLDS** on the enabled architecture (all flagged mutation surfaces route through `m.executor.Execute`/`Approve`). |
| 2 | RuntimeExecutor is the single execution authority | **HOLDS** on the enabled path — it owns provider, artifact, mutation, verification; the UI owns input/state/approval/projection. |
| 3 | Production mutations have a wired Verifier | **HOLDS** — `NewEngine` attaches its verifier (Step 1); the executor attaches its verifier as the apply gate. |
| 4 | Target resolution has one canonical authority | **HOLDS** — `IntentGateway → strategy.Select`; ambiguity stays explicit, no UI regex re-interprets targets for execution. |
| 5 | Model output is not execution truth | **HOLDS** — no-change applies report `OutcomeNoChange`; empty artifacts fail before approval; verification gates the apply. |
| 6 | Mutation truth comes from actual mutation evidence | **HOLDS** — `MutationEvidence`/`OutcomeNoChange` only from byte comparison; `ApplyExecutedChanged`; rollback restores disk bytes. |
| 7 | UI is a projection of canonical runtime events | **HOLDS** — E2E test asserts the lifecycle sequence on the bus; the UI renders from events and results. |
| 8 | Lynx is an optional evidence/search capability, never an execution dependency | **HOLDS** — router tests (native fallback / lynx hybrid) + executor independence test. |
| 9 | Lea remains an internal Izen capability | **HOLDS** — no external Lea boundary introduced; `internal/lea` untouched. |
| 10 | No new execution authority is introduced | **HOLDS** — only the pre-existing RuntimeExecutor boundary was activated; no new executor/classifier/resolver/bus was created. |

**Note on Invariants 1, 2, 7:** they hold for the *enabled* architecture. The
flag-off rollback path is the pre-cutover legacy behavior and is retained
explicitly for migration safety (Step 0 requirement: "disabled → old
behavior"). Once the flag becomes the default and is soaked, the legacy path is
deleted (Step 7) and the invariants hold unconditionally.

## Definition of done

```
User → Autonomy → Execution Intent → RuntimeExecutor
     → Artifact/MutationSet → Verifier → ExecutionProof → Canonical Events → UI
```

Achieved for every autonomy-decided and command-driven TUI mutation on the
enabled architecture, with no production TUI mutation path bypassing
RuntimeExecutor. The flag-off rollback boundary is the only legacy remainder,
per the cutover plan's rollback rule.
