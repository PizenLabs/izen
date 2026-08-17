# Phase 0 — Architecture Baseline: Execution Authority (Frozen Snapshot)

**Status:** FROZEN — investigation complete, no production code changed.
**Date:** 2026-08-17
**Companion docs:** `docs/architecture/AUDIT-RUNTIME-AUTHORITY.md` (forensic audit), `docs/design/PHASE/RUNTIME_FORENSIC_AUDIT.md`, `PHASE_0_FINDINGS.md` (new facts), `PHASE_1_CUTOVER_PLAN.md` (wiring plan).
**Method:** Static trace from every entry point (`handleInput`, `$prompt`, `$hot`, `izen run`, runtime facade) to `os.WriteFile`. Every claim below was re-verified against source in this session.

---

## 1. Executive Summary

The interactive TUI (`izen`) runs one real execution stack: **autonomy decides → legacy UI mode engines execute → direct provider calls → `m.execEng.Patches.ApplyContext` → `os.WriteFile`**. Two competing stacks are fully built and wired but never execute in the TUI:

1. **RuntimeExecutor** (`internal/execution/executor.go`) — the declared "single execution authority" (compose.go:132-142) — is reachable only via `runGatedLine`/`runPromptExecution`/`runHotExecution`, all gated on `m.autonomy == nil`. Autonomy is always wired (compose.go:642, program.go:188). **Dormant.**
2. **`izen run`** (`cmd/izen/runtime.go`) — a parallel `pkg/app` lineage. **Separate product stack.**

The production mutation path has **no verification gate** (PatchManager.verifier is nil) and emits **no canonical runtime lifecycle events**. The target invariant for Phase 1:

```
Autonomy Decision → RuntimeExecutor → Provider → Artifact → MutationSet → Verification → ExecutionProof → Canonical Events → UI Projection
```

---

## 2. Entry Routing (verified call chain)

```
handleInput                        internal/ui/commands.go:162
  ├─ $decide → intercepted pre-parser
  └─ intentFromInput → parser.ParseInWorkspace + directive re-parse   intent_dispatch.go:30
       → dispatchASTIntent / mode shorthand / free-form               commands.go:271,274,399
```

| Directive | Autonomy wired (production) | Autonomy nil (dormant/test) |
|---|---|---|
| `$prompt` | `routePromptDirective` → `runAutonomyRoutedCmd` (autonomy_route.go:27) | `runPromptExecution` (gateway.go:376, gated) |
| `$hot` | `routeHotfixThroughAutonomy` (autonomy_route.go:41) | `runHotExecution` (gateway.go:392, gated) |
| free-form | `runAutonomyRoutedCmd` → `handleMessageContent` if no workspace match | `runGatedLine` (gateway.go:42) → `m.gateway.Gate` → `m.executor.Execute` |
| `/build` | `runBuildCmd` → `runBuildFastTrack` (requires staged tasks) | same |
| `/plan` | `runPlanEngineCmd` / intentCompiler / microkernel | same |

`runAutonomyRoutedCmd` (autonomy_route.go:27-33): nil autonomy → falls back to `handleMessageContent` (legacy owner path).

### 2.1 Autonomy decision dispatch

`autonomy.Decide` (engine.go:179) → `Trace` → `dispatchAutonomyTrace` (autonomy_route.go:54):

- `DecisionDirectResponse` → `streamCmd` (direct provider chat)
- `DecisionAskUser` → `requestAutonomyProposal` (autonomy_proposal.go:34) → `executeAutonomyProposal` (grants internally, re-Decides, continues) — the ONLY user-facing authorization gate
- `DecisionBlock` → error, no execution
- `DecisionAutoContinue` → `executeAutonomyWorkspace` (autonomy_route.go:89) → mode switch + dispatch:
  - INVESTIGATE → `runInvestigateCmd`
  - BUILD → `executeAutonomyBuild` → `stageAutonomyBuild` → `planResultMsg{IsFastTrack:true}` → `runBuildCmd`
  - BUILD + `$hot` → `handleHotfixCmd`
  - REVIEW → `runReviewCmd`
  - PLAN / ASK → `handleMessageContent`

### 2.2 Fast-track → build (verified)

`planResultMsg` (update.go:594): stages tasks (update.go:687), bridges to ContextLedger (690), ScopeGuard final validation against `planEngine.AllowedFiles` via `control.ValidateStagedPlan` (657-674). `IsFastTrack` auto-approves (699-706) and continues to `runBuildCmd` in the same turn (779-791).

---

## 3. Mutation Path Map (the REAL authority)

### 3.1 Single-file build (fast-track)

```
runBuildCmd (2423) → zero-task halt (2433) → execEng.BeginTransaction (2449)
  → runBuildFastTrack (2591): sess.CurrentTasks required (2593), transitionToBuilding (2599),
      BeginTransaction (2606), sess.ClearHistory (2611), SetStreamContextFiles (2619),
      Patches.SetLedger + SetContextID (2648-2649), fastTrackFileContext (2679, FULL-FILE injection)
  → m.provider.ExecuteStream (2787, buildGenerationTimeout ctx)
  → stream loop → proposeBuildPatch (commands.go:4506)
      → m.provider.Execute (4659) with retry (4885 full-rewrite fallback)
  → buildProposalReadyMsg (streamCh at 3086 / return at 4663...)
  → update.go:1030 case buildProposalReadyMsg → extractBuildProposals → proposal dock
  → user Alt+A → applyProposalCmd (proposals.go:474)
      → transitionToBuilding (512), checkpoint fallback (516), authorizeBuildExecution (518)
      → applyPatchWithDeadline (521)
      → m.execEng.Patches.ApplyContext (commands.go:937)
      → PatchManager.Apply → shadow backup → os.WriteFile (patch.go:832)
      → CommitTransaction (model.go:2567 / update.go:2143,2289)
```

### 3.2 Hotfix (`$hot`)

```
handleHotfixCmd (3151): requires ModeBuild (3151+), hotfixBrandingLabel, resolveMultiHotfixTargets, PROMPT guard
  → handleHotfixProposal → proposeHotfixPatch (3828)
      → local deterministic short-circuit: ApplyContextAwareFuzzyReplace (3861) — 0 tokens
      → contract := hotfixContractFor(orig) (3892): ReplaceFile / ReplaceBlock (bounded snippet) / ReplaceBlock+HTML
      → ambiguity gate classifyHotfixAmbiguity (3912)
      → m.provider.Execute (3998) + non-streaming retry (4062)
      → tool calls buffered for approval (4085)
  → hotfixProposalMsg → applyHotfixPatch → applyPatchWithDeadline (commands.go:4981,5351)
```

### 3.3 Multi-hotfix

```
runMultiHotfix (multihotfix.go:103) → proposeMultiHotfixPatch
  → m.provider.Execute (206, 236)
  → applyMultiHotfixGraph → per-node applyPatchWithDeadline (multihotfix.go:435)
```

### 3.4 Hybrid template / misc provider sites

- `hybrid template` → `m.provider.Execute` (commands.go:5212) → buildProposalReadyMsg
- `diagnose/ask` → `m.provider.Execute` (commands.go:7141) — read-only
- chat `streamCmd` → `m.provider.ExecuteStream` (stream.go:288) — read-only
- commit agent → `m.provider.Execute` (agents.go:770) — read-only
- plan engine → `m.planEngine` provider (compose.go:528-529 `SetProvider`/`SetStreamProvider`)

### 3.5 Provider invocation inventory (verified)

| Site | File:Line | Direction | Mutation? |
|---|---|---|---|
| fast-track build stream | commands.go:2787 `ExecuteStream` | build | yes (via proposal) |
| build propose | commands.go:4659 `Execute` | build | yes |
| build full-rewrite retry | commands.go:4885 `Execute` | build | yes |
| hotfix | commands.go:3998 `Execute` | hotfix | yes |
| hotfix retry | commands.go:4062 `Execute` | hotfix | yes |
| hybrid template | commands.go:5212 `Execute` | build | yes |
| chat stream | stream.go:288 `ExecuteStream` | ask | no |
| commit agent | agents.go:770 `Execute` | ask | no |
| diagnose/ask | commands.go:7141 `Execute` | ask | no |
| multi-hotfix | multihotfix.go:206,236 `Execute` | hotfix | yes |
| investigate dispatcher | modes/investigate/dispatcher.go:169 `Execute` | investigate | no |
| investigate toolrunner | modes/investigate/toolrunner.go:202 `Execute` | investigate | no |
| RuntimeExecutor (dormant) | executor.go:373 `Execute` | all | yes |
| plan engine | compose.go:528-529 | plan | no |

---

## 4. Verification Map (verified — the key gap)

- **PatchManager micro-fix gate** (patch.go:839-887): `if pm.verifier != nil { report := pm.verifier.RunAll(); if !report.Passed { restore shadow backup; micro-fix }}`. **`SetVerifier` has NO production callers** — grep confirms only test files (`internal/ui/execution_truth_test.go:45,333`, `mutationset_test.go:211`, `executor_integration_test.go`, etc.) and the two definitions (executor.go:249, patch.go:362).
- **`execution.Engine.Verifier`** (execution.go:31,90) IS created in `NewEngine` (execution.go:68-75) and configured (114-116, `configureVerifier`), but **never attached to its own PatchManager** (`e.Patches.SetVerifier(v)` is absent). So `m.execEng.Patches.verifier` is nil in production.
- `engine.Pipeline.RunAll` (pipeline.go:131) — only via `PipelineRunner.ExecuteBuild`, which has no production callers.
- **RuntimeExecutor owns a verifier** (executor.go:240-244: `NewLanguageVerifier`/`NewVerifier`) and calls `x.verifier.RunAll()` at Approve (executor.go:697-709). This is the dormant path.
- Net: **the mutation the user actually triggers is never verified.**

### Micro-fix loop status
- Exists behind `PatchManager.Apply` gate (patch.go:850-887): extract syntax errors, restore shadow backup, return "verification gate blocked" error. **Dormant** — verifier nil.

---

## 5. Events Map (verified)

### Canonical lifecycle (emitted ONLY by RuntimeExecutor / runtime graph)
`graph/graph.go` emit points: `NewExecutionStarted` (289), `NewStrategySelected` (302), `NewTargetResolved` (316), `NewContextPrepared` (323), `NewModelInvoked` (331), `NewProviderResponse` (346), `NewProviderWaiting` (339), `NewProviderFirstToken` (353), `NewProviderStreamDelta` (366), `NewArtifactProduced` (373), `NewApprovalRequired` (386), `NewMutationStarted` (392), `NewMutationCompleted` (399), `NewVerificationCompleted` (399/406), `NewExecutionFinished` (468/487/504), `NewExecutionFailed` (486). Published via `x.bus.Publish` (executor.go:271).

### Emitted on the REAL (legacy) path
Only `EventActivity` (`engine.activity`) and `EventEngineTelemetry` via global sinks/activity tree, plus `EventReasoningStream` (commands.go:2822). **No canonical runtime lifecycle events.**

### Autonomy events (wired, live)
`autonomy.emitDecision` → `EventAutonomyDecision` (engine.go:300), `EventCapabilityGranted`, `EventLoopTransition` (PublishTransitions via publishAutonomyLoop, autonomy_route.go:151), `EventContextCompiled`.

### UI subscriptions
program.go:312-348 — subscribes to all 7 mode-engine types + the full canonical runtime lifecycle + autonomy events. **Ready, but the production path never publishes the lifecycle half.**

### Event bus (3 parallel)
- `internal/events.Bus` (42+ types) — canonical, TUI + engines
- `pkg/event.MemoryEventBus` (7 types) — `izen run` only
- `pkg/engine/telemetry.EventBus` (8 types) — bridged one-way into domain bus (compose.go:509)

---

## 6. Intent / Reclassification Sites (verified)

| Classifier | Location | Production status |
|---|---|---|
| `autonomy.Engine.Classify` | autonomy/engine.go:180, intent.go:337 | **A — authoritative** for $prompt/$hot/free-form |
| `hasMutationIntent` | commands.go:7618, checked at 471,525 | E — legacy re-classify inside autonomy-decided workspace |
| `investigate.ClassifyIntent` | commands.go:471 / modes/investigate/dispatcher.go:510 | E — re-routes MODE inside investigate |
| `handlers.ClassifyIntent` | runtime/handlers/handlers.go:153 | F — parallel observer on Runtime facade |
| `autonomy.Classify` semantic fallback | never wired (`WithSemanticClassifier` absent in compose) | dormant |
| `router.Router`/`PromptIntentClassifier` | compose.go:590 constructed, never invoked | D — dead-wired |
| `execution.IntentGateway` → `strategy.Select` | gateway.go:58 | B — dormant (autonomy nil only) |

---

## 7. Target Resolvers (verified)

| Resolver | Location | Used by | Method |
|---|---|---|---|
| `resolveAutonomyBuildTarget` | autonomy_target.go:38 | autonomy BUILD | workspace walk |
| `resolveHotfixTarget` | commands.go:3388 | `$hot` | regex |
| `resolveMultiHotfixTargets` | multihotfix.go:50 | multi-hotfix | per-target |
| `strategy.Select` collectTargets | strategy/selector.go | gated path (dormant) | structural |
| `execution.ResolveTargetSet` | targets.go:90 | (unused in UI) | — |
| `workspace.NewTargetFileResolver` | matcher.go:25 | resolvePendingTodoTarget (commands.go:3372) | matcher |

`resolveAutonomyBuildTarget` candidates > 1 → `stageAutonomyTargetSelector` (autonomy_target.go:126); candidates == 0 → not-found diagnosis unless creation request.

---

## 8. Context / Evidence Injection (verified)

- `fastTrackFileContext` (alignment.go:103-128): reads and injects the **entire** target file content into the prompt (`## Current Content of:` block, line 122). Under full-rewrite intent it emits only a directive (107-110). **This dilutes the bounded-evidence contract** (autonomy/handoff.go:11).
- `fastTrackGoals` (alignment.go:82-95): per-task goal + `task.Evidence` ledger (86-88).
- `compileAutonomyBuildEvidence` (autonomy_route.go:233-255): `autonomy.CompileContext(target, content).FormatEvidenceLedger()` + `hotfix.ResolveRedundantTargets` → `formatRedundancyLedger`.
- `publishAutonomyLoop` (autonomy_route.go:129-153) is a **descriptive projection** — no engine loop actually iterates.

---

## 9. Autonomy Handoff Contract (verified)

`autonomy.Decide` (engine.go:179-244):
- `IntentResult{Intent, Confidence, Targets, Required, Explanation}` → stored on Trace (engine.go:181)
- **`targetConfidence` computed (engine.go:208-214) but NOT stored on Trace** → dropped; only passed into `DecisionInput` (225)
- `Risk`, `Route`, `ScopeSize`, `Rollback`, `Grant` → stored on Trace
- Handoff to build path (`runBuildCmd`/`runBuildFastTrack`) carries **none** of these — confidence never reaches the mutation path.
- `compose.go` wires `WithEventBus`/`WithScope`/`WithRiskFunc` (`Execution.Risk.ClassifyFileOp`)/`WithRollbackFunc` (`Execution.Checkpoints`) — **no `WithSemanticClassifier`** (all-deterministic).
- `a.Approver = nil` (compose.go:369) — the `handlers.PatchApprover` seam is never used in production; executor `SetAuthorization` only from tests.

---

## 10. Authorization Gates (verified — two independent gates)

1. **Autonomy proposal** (autonomy_proposal.go): capability grant (`GrantDefault`, line 80), internal, re-Decide, continue. The ONLY user-facing gate for `ask_user`.
2. **Patch proposal dock** (Alt+A): `authorizeBuildExecution` (model.go:4147-4169) → `m.execEng.SetAuthorization(auth)` via `AuthorizationEngine` (`m.authEngine`, compose.go:657) → then `applyPatchWithDeadline`.
- `/grant` deprecated; `handleAutonomyGrant` dead.

---

## 11. Transaction Lifecycle (verified)

`execution.Engine` owns `mutationSet` (execution.go:44): `BeginTransaction` (219-222), `CommitTransaction` (227-236), `RollbackTransaction` (242-252), `relinkMutationSet` (257-261). UI callers: `runBuildCmd` (2449), `runBuildFastTrack` (2606), commit at model.go:2567, update.go:2143,2289; rollback at keys.go:764, lifecycle.go:245, update.go:1245,1629,1729. RuntimeExecutor owns a **separate** MutationSet per execution (executor.go:640-714).

---

## 12. Session / Ledger / Budget

- `sess.StageTaskList` + `sess.ContextLedger` (bridgePlanToLedger, update.go:690) — SSOT /build consumes.
- `m.mutationBudget` (`app.Budget`, program.go:148) — compose defaults 100 files/5000 lines/1M tokens/10 attempts/30s/5 concurrent (compose.go:549-556). `m.microBudget` (557-558). `m.execEng.SetBudget` not called by UI; budget enforced via `authorizeBuildExecution` only.
- `m.caps` (`app.Caps`) — all 7 capabilities granted by default (compose.go:540-547).

---

## 13. Dormant / Half-Wired Inventory (verified beyond audit)

| Item | Evidence | Status |
|---|---|---|
| RuntimeExecutor + IntentGateway | executor.go:230; gateway.go:42 | **D in TUI** |
| `execution.Engine.Pipeline` (PipelineRunner.ExecuteBuild) | execution.go:100, no callers | **D** |
| `PatchQueue`/`StreamMonitor` | execution.go:98-99 | **D** |
| PatchManager micro-fix gate | patch.go:850 (SetVerifier no prod callers) | **D** |
| `router.Router` / `PromptIntentClassifier` | compose.go:590, never read | **D** |
| `pipeline.Engine.Run`/`RunAdaptive`/`ExecutePlan` | layers 0-5 | **D in TUI** |
| timeline.Timeline | compose.go:461, `a.Timeline()` never called | **D** |
| runtime.LedgerBuilder/ContextLedger | compose.go:451-454, never read | **D** |
| events/audit ndjson | write-only | **D** |
| modes/build Executor/ApplyMutation | no production callers | **D** |
| handlers.PatchApprover | compose.go:369 Approver=nil | **D** |
| `strategy.Compile`/`ExecutionGraph`/`CheckInvariants` | no callers | **D** |
| `pipeline.Engine.ExecutePlan` facade | guarded by `provider==nil`; provider always set | **E** |
| `execution.ResolveTargetSet` | targets.go:90 | **D** |

---

## 14. Frozen Invariants (Phase 0 baseline — do not regress)

1. `m.autonomy != nil` in production → RuntimeExecutor/gated path unreachable.
2. `m.execEng` (execution.Engine) is the apply authority; its PatchManager has **no verifier**.
3. Provider is invoked **directly** by the UI on 7+ sites (not via executor).
4. Canonical lifecycle events are emitted **only** on the dormant path.
5. Autonomy decision fields (confidence, target confidence) are **not** propagated to build.
6. `fastTrackFileContext` injects the **entire** file (bounded-region contract violated).
7. Two authorization gates (autonomy proposal + patch dock) are independent.
8. `a.Approver == nil`; AuthorizationEngine gates only via `m.execEng.SetAuthorization`.

---

## 15. Stacks Summary

| Stack | Entry | Emits canonical events | Verifies | Mutates via | Production TUI |
|---|---|---|---|---|---|
| Legacy UI + execution.Engine | handleInput | no | no | PatchManager.Apply | **YES** |
| RuntimeExecutor | gateway gated path | yes | yes | own PatchManager + MutationSet | no (autonomy always wired) |
| `izen run` pkg/app | cmd/izen/runtime.go | pkg/event only | capability validator | TxFS | separate product |
| LEA layered engine | compose.go:508 | bridged telemetry | layer4 (review) | none | dormant executor |