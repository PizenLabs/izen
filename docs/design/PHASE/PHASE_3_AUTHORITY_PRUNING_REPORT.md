# Phase 3 — Authority Pruning Report

Phase 1 cut the TUI onto the RuntimeExecutor. Phase 2 established the execution
truth model. Phase 3 completes the pruning: the RuntimeExecutor is the SOLE
production execution authority, every duplicate classifier/resolver/projection
is removed, and the negative space is pinned by architecture tests.

Branch: `fix/authority-pruning` (12 commits, 94 files changed, +1,228 / -21,774)

---

## 1. Executive summary

The `IZEN_RUNTIME_EXECUTOR` feature flag is gone. RuntimeExecutor execution is
not "enabled by default" — it is the only production mutation path. The legacy
UI execution shadows (hotfix provider calls, build fast-track, plan-fallback
classifiers, gateway squeezer/compressor, the build mode engine, the intent
router tree) were removed after proving each had zero live consumers or was
superseded by the runtime path.

The phase also removed the unread in-memory execution timeline projection and
pinned the pruning contract with AST-level negative architecture tests so a
duplicate authority cannot silently resurface.

## 2. Before state (as-audited)

The forensic audit (RUNTIME_FORENSIC_AUDIT.md) found three overlapping
execution authorities: the UI monolith (~19k LOC owning provider/mutation/
approval), the mode engines (headless but UI-invoked), and a telemetry-only
runtime facade. Twelve UI provider sites, five UI apply sites, eleven UI
transaction sites, and six+ target resolvers could disagree on the same
request.

## 3. After state (as-pruned)

```
User Input (bare / $prompt / $hot)
   |
   v
IntentGateway.Gate / SelectStrategy      internal/execution/intent.go
   (strategy.Select — the single classifier + target resolver)
   |
   v
RuntimeExecutor.Execute / Approve / Reject   internal/execution/executor.go
   (owns provider invocation, context, artifact, patch, approval,
    MutationSet, apply, verify, canonical events)
   |
   v
ExecutionProof + canonical events -> UI renders (pure projection)
```

The UI owns only: keyboard input, rendering, and the bounded ambiguity
selector (`resolveAutonomyBuildTarget` — see §6).

## 4. Authority audit table

| Authority | Before | After | Proof |
|---|---|---|---|
| Intent classification | 6+ classifiers (UI router, gateway, command fallback, ai prompts, autonomy, strategy) | `autonomy.Classify` (production) + `strategy.Select` (execution) | §8 |
| Target resolution | 6+ resolvers (UI workspace walk, regex fast-track, fuzzy) | `strategy.Select` (`selector.go:95`) via `IntentGateway` | §6 |
| Provider invocation (mutations) | 12 UI sites | RuntimeExecutor only | architecture test `TestUICannotCallProviderOnExecutionPath` |
| Filesystem mutation | 5 UI apply sites + 11 transaction sites | RuntimeExecutor approval boundary only | architecture test `TestUICannotMutateWorkspaceOnExecutionPath` |
| Approval | UI keys.go gates (3 surfaces) + fake `InMemoryApprover` | single `executor.Approve` (`runtime_executor.go:42`) | `TestRunnerAuthorizationEnforcement` |
| Verification | UI `go build`/`go test` shells | `Verifier.RunAll` inside runtime apply | truth matrix |
| Lifecycle events | UI-authored + runtime graph | runtime graph only | `TestLifecycleEventsGeneratedOnlyFromGraph` |
| Execution timeline | `timeline.Timeline` (write-only) | removed | §10 |
| Execution audit log | n/a | `events/audit` (retained by design) | §11 |

## 5. Removed surfaces inventory

### 5.1 Dead execution pipeline (`internal/execution`)
`diff.go`, `execution.go` pipeline remnants, `pipeline.go`, `policy.go`,
`stream.go` — the legacy multi-stage execution pipeline. RuntimeExecutor
subsumes it. (-1,414 lines)

### 5.2 Build mode engine (`internal/modes/build`)
`engine.go`, `executor.go`, `recovery.go`, `stage.go`, `summary.go`,
`changeset.go`, `checkpoint.go` + 10 test files. Zero production composition
callers; the executor owns mutation. (-2,500+ lines)

### 5.3 Intent router tree (`internal/router`)
`router.go`, `classifier.go`, `fastpath.go`, `policy.go`, `semantic.go` + tests.
The UI hybrid intent router was a duplicated classifier. (-800 lines)

### 5.4 UI execution shadows (`internal/ui`)
- `alignment.go`, `effort.go`, `multihotfix.go`, `proposals.go` (legacy),
  `execution_proof.go` (UI-owned proof), `engine_first.go` routing remnants,
  `hotfix_*.go`, `fasttrack_*.go`, `mutationset_test.go` et al.
- `engine_graph.go` — the dead strategy-graph telemetry projection.
- `commands.go` shrunk by ~3,500 lines of legacy fast-track/hotfix provider
  paths.
(-11,987 lines in one commit alone)

### 5.5 Duplicate intent classifiers (`73ec789`)
| Removed | File | Live replacement |
|---|---|---|
| `ClassifyExecutionMode`, `ClassifyDirectMutation`, `ClassifyIntentMode` | `internal/core/intent.go` (deleted) | `autonomy.Classify` |
| `CompressPrompt`, `ClassifyComplexity`, `Squeeze`, `IntentClassifyRequest` | `internal/gateway/compressor.go`, `squeezer.go` (deleted) | strategy `Select` + autonomy |
| `SimpleMutationPrompt`, `IntentClassifyPrompt` | `internal/ai/prompts.go` (deleted) | executor prompt assembly |
| `FallbackPlanTarget`, `GenerateFallbackPlan`, `ParseTargetFromSanitizedLedger` | `internal/command/plan_fallback.go` (deleted) | strategy `Select` |

### 5.6 Timeline projection (`46719a5`)
`internal/events/timeline/{timeline.go,span.go,timeline_test.go}` — the
write-only in-memory timeline, its `Application.Timeline()` accessor, Wire
wiring, Close teardown, and 3 compose tests. Proven zero production readers.

## 6. Canonical target resolution (STEP 4 — documented)

`strategy.Select` is the single target-resolution authority. The ONLY UI-side
resolution surface remaining is `resolveAutonomyBuildTarget`
(`internal/ui/autonomy_target.go:38`), and its scope is strictly bounded:

- called on one path only — `executeAutonomyViaRuntime`
  (`internal/ui/runtime_cutover.go:107`), and only when `SelectStrategy` has
  already returned `HumanClarification`;
- it never re-resolves a resolved target — it expands an ambiguity into a
  candidate list for the human selector, or emits the terminal not-found
  diagnosis;
- selection is an explicit human act; no candidate is auto-picked, and the
  chosen candidate is staged directly, never re-resolved.

`resolveHotfixTarget` / `resolveMultiHotfixTargets` (legacy regex resolvers)
were removed with the legacy hotfix path. Documented in
RUNTIME_AUTHORITY_MIGRATION.md §"Canonical target resolution".

## 7. Flaky test fix (`ed78def`)

`TestAskPlanBuildFlow` asserted the raw `app.Ledger.Snapshot().Failures` the
moment a failure message arrived, racing the async bus-dispatch goroutine. It
now polls `waitFor(2*time.Second, ...)` until `Failures == 2`. Deterministic
(8/8 passes, verified across runs).

## 8. Intent-classification invariant — PROVEN

The canonical production classifier is `autonomy.Classify`
(`internal/autonomy/intent.go:337`, wired `compose.go:589`). The execution
classifier is `strategy.Select`. Every removed duplicate symbol is verified
absent (`grep` zero non-test hits), and the negative guard
`TestNoDuplicateIntentClassifierResurfaces` (internal/architecture/
pruning_invariants_test.go) fails the build if any of them returns.

## 9. Execution-authority invariant — PROVEN

RuntimeExecutor is constructed exactly once in production, in the composition
root (`compose.go:419`), enforced by `TestRuntimeExecutorSingleCompositionBinding`.
The UI gated path must cross `m.gateway.Gate` before `m.executor.Execute`
(`TestEveryUserActionCrossesIntentGateway`) and must never call the provider
(`TestUICannotCallProviderOnExecutionPath`) or mutate the workspace
(`TestUICannotMutateWorkspaceOnExecutionPath`). The strategy compilation graph
is a test oracle only — the production executor consumes `Select` profiles
directly, never `Compile` (`TestStrategyCompilationGraphTestOracleOnly`).

## 10. Projection invariant — PROVEN

`strategy.Compile` / `ExecutionGraph` / `CheckInvariants` / `EscalationsFor`
have zero production callers (verified with precise `strategy.` selector
scan); they are retained exclusively as the test invariant oracle for
`strategy.Select` and covered by `compile_test.go`/`escalation_test.go`/
`graph_test.go`/`matrix_test.go`. The unread timeline is removed
(`TestNoLegacyTimelineProjectionResurfaces`). The UI strategy-graph telemetry
projection (`engine_graph.go`) is removed.

## 11. Retained components — by design

| Component | Reason | Proof |
|---|---|---|
| `events/audit` AuditLogger | persists to `.izen/audit/events.ndjson`, a user-facing artifact | `cmd/izen/main.go:179` (`WithAuditDir`) |
| `runtime.LedgerBuilder` / `a.Ledger` | live projection read by tests + UI evidence contract | `compose.go:433-436` |
| `execution.Engine` (`execEng`) | checkpoints/shadow-CP + virtual-snapshot transaction rollback guard on mode switch; write-only, conservative cut | `compose.go:606`, `commands.go:1545` |
| `checkAuthorization` | single authorization gate on the runtime patch path | `patch.go:630`, `runner.go:208,317,328` |

## 12. Architecture negative tests added (STEP 10-11)

`internal/architecture/pruning_invariants_test.go` (commit `36f3d0c`):

1. `TestNoDuplicateIntentClassifierResurfaces` — 16 banned duplicate symbols
   must never reappear; `autonomy.Classify` must exist (anti-vacuous).
2. `TestNoLegacyTimelineProjectionResurfaces` — no import of
   `internal/events/timeline`; no `Timeline()` accessor/field in compose.go.
3. `TestStrategyCompilationGraphTestOracleOnly` — no production `strategy.`
   selector calls to the graph oracle; oracle tests must still exist.

Each guard was validated non-vacuous by temporarily re-injecting a banned
symbol/call and confirming the test fails, then restoring.

Existing Phase 5 architecture tests retained: `TestLifecycleEventsGeneratedOnlyFromGraph`,
`TestRuntimeExecutorSingleCompositionBinding`, `TestUICannotCallProviderOnExecutionPath`,
`TestUICannotMutateWorkspaceOnExecutionPath`, `TestEveryUserActionCrossesIntentGateway`,
`TestNoDuplicateHotfixExecutionPath`. Lynx optionality pinned by
`TestExecutorTargetResolutionIndependentOfLynx` (identical strategy + target
under native vs Lynx router).

## 13. Validation results (STEP 12)

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go build ./cmd/izen` | clean |
| `go vet ./...` | clean |
| `golangci-lint run ./...` | 0 issues |
| `go test ./... -count=1` | all pass |
| `go test -race` (execution/runtime/ui/events) | all pass |
| Lynx on/off | covered by independence test (both router states) |

## 14. Invariant status table

| # | Invariant | Status |
|---|---|---|
| 1 | RuntimeExecutor is the sole production mutation authority | **PROVEN** — single composition binding; UI has no apply/PatchManager path |
| 2 | No UI provider invocation on the execution path | **PROVEN** — `TestUICannotCallProviderOnExecutionPath` |
| 3 | No UI workspace mutation | **PROVEN** — `TestUICannotMutateWorkspaceOnExecutionPath` |
| 4 | One approval surface (`executor.Approve`) | **PROVEN** — authorization tests + `runExecutorApproveCmd` |
| 5 | Every execution crosses the IntentGateway | **PROVEN** — `TestEveryUserActionCrossesIntentGateway` |
| 6 | Lifecycle events generated only from the runtime graph | **PROVEN** — `TestLifecycleEventsGeneratedOnlyFromGraph` |
| 7 | One canonical intent classifier | **PROVEN** — dead duplicates verified absent + negative guard |
| 8 | One canonical target resolver | **PROVEN** — `strategy.Select`; bounded UI ambiguity surface documented |
| 9 | No duplicate hotfix execution path | **PROVEN** — `TestNoDuplicateHotfixExecutionPath` |
| 10 | Lynx never selects the execution path / resolves targets | **PROVEN** — `TestExecutorTargetResolutionIndependentOfLynx` |
| 11 | Strategy compilation graph is a test oracle only | **PROVEN** — zero production `strategy.Compile`/graph calls + guard |
| 12 | Unread timeline projection removed | **PROVEN** — package/import/accessor absent + guard |
| 13 | Removed classifiers never resurface | **PROVEN** — negative identifier guard |
| 14 | Every execution has an ExecutionProof | **PROVEN** — `TestEveryExecutionHasExecutionProof` |
| 15 | Verification requires a real verifier | **PROVEN** — `TestEveryVerificationRequiresRealVerifier` |
| 16 | Executor never executes without a strategy | **PROVEN** — `TestExecutorNeverExecutesWithoutStrategy` |
| 17 | Ledger-failure assertion is deterministic | **PROVEN** — polling fix (`ed78def`) |
| 18 | Audit log retained by design (not pruned) | **PROVEN** — wired `cmd/izen/main.go:179` |

## 15. Remaining risks / NOT PROVEN

- **UI read-only provider calls remain** (`/commit` generation
  `agents.go:770`, `/investigate $diagnose` `commands.go:3952`). Both are
  read-only — no mutation, no approval — and are outside the mutation
  authority. Moving them into the runtime is future work, not a violation.
- **Virtual snapshot transactions** (`execEng.BeginTransaction/Commit/Rollback`
  on mode switch) still reference the retained `execution.Engine`. They guard
  rollback on legacy-adjacent flows; the executor mutation path does not depend
  on them. A future cut can retire the transaction surface when all
  proposal-apply paths are executor-owned.
- **`/plan`-mode plan engine** provider funcs remain wired from the UI
  (`commands.go:127` via `m.planEngine.SetProvider`). Read-only synthesis;
  deferred.
- **Fake `InMemoryApprover`** was outside Phase 3 scope; the runtime handler
  approval is not a production TUI path.

## 16. Files changed (branch summary)

94 files, +1,228 / -21,774. Deletions dominate: legacy UI execution shadows
(~12k), build mode engine (~2.5k), gateway duplicate classifiers (~2.7k),
intent router (~800), dead execution pipeline (~1.4k), timeline (~725).
Retained `events/audit`, `LedgerBuilder`, `execution.Engine` (checkpoints/
transaction guard), and the strategy test oracle.

## 17. Documentation updates

- `RUNTIME_AUTHORITY_MIGRATION.md` — added §"Canonical target resolution":
  `strategy.Select` is the single resolver; `resolveAutonomyBuildTarget` is the
  bounded ambiguity-candidate surface; `resolveHotfixTarget`/
  `resolveMultiHotfixTargets` removed.
- This report.

## 18. Definition of Done

- [x] `IZEN_RUNTIME_EXECUTOR` flag removed; executor is the only mutation path.
- [x] One authorization surface (autonomy proposal) + one mutation approval
      (executor `Approve`).
- [x] `applyPatchWithDeadline` and direct UI mutation sites gone (verified
      zero `execEng.Patches.Apply`/`Patches.ApplyContext` in UI).
- [x] Duplicate classifiers/resolvers/projections removed (proven + guarded).
- [x] Architecture negative tests pin the pruning contract.
- [x] `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race`,
      `golangci-lint run ./...` all clean.
