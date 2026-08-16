# IZEN RUNTIME AUTHORITY FORENSIC AUDIT

Phase 1 — Read-only evidence-based audit of execution ownership.

Date: 2026-08-15
Scope: entire repository (cmd/, internal/, pkg/), 883 Go files, ~94k LOC.
Method: follow actual call paths; documentation and package names were not trusted.

---

## 1. Executive Summary

Izen is not one runtime — it is three overlapping execution authorities plus a
telemetry-only command facade:

1. **The UI monolith** (`internal/ui/commands.go`, `update.go`, `model.go`,
   `stream.go`, `agents.go`, `multihotfix.go`) — ~19k LOC of provider
   invocation, prompt construction, model routing, patch creation, mutation
   lifecycle, verification orchestration, and approval. This is the de-facto
   execution authority for every user path.
2. **The mode engines** (`internal/modes/{plan,build,investigate,review}`) —
   real headless engines (plan synthesizes, investigate forensics, review
   audits, build applies mutations) but invoked directly *from the UI*.
3. **The Runtime command facade** (`internal/runtime` + `handlers`) — 5
   commands, all of which are **telemetry/state-only**: they never invoke a
   provider, a plan engine, or a mutation. Approval handlers fabricate a
   mutation record via `InMemoryApprover`.
4. **The V3 engine tree** (`pkg/app`, `pkg/kernel`, `pkg/engine/*`) — only
   reached by the headless `izen run` CLI, not by the TUI.

Every execution path is owned by the UI. The runtime command layer is a
parallel "APPLICATION-LAYER COMMAND RECORD" (keys.go:946-949) that runs *in
addition to* the real UI execution, never instead of it.

---

## 2. Execution Path Matrix

For each path: who receives input, who decides strategy, who owns provider,
who owns mutation, who owns approval, who creates evidence, who renders.

| Path | Input received by | Strategy | Provider | Mutation | Approval | Evidence | Rendered by |
|---|---|---|---|---|---|---|---|
| bare input (ask) | UI `handleInput` → `handleMessageContent` | UI hybrid router | **UI** `stream.go:288` | none | none | UI token records | UI |
| bare input (build) | UI | UI (fast-track) | **UI** `commands.go:2733` | UI `execEng` + PatchManager | **UI keys.go** | UI `MutationEvidence`/Proof | UI |
| `$prompt` | UI `routePromptDirective` | UI strategy layer `strategy.Select` | **UI** `commands.go:7074` (ask handoff) + hotfix `commands.go:3848/3912` | UI | UI | UI | UI |
| `$hot` | UI `handleHotfixCmd` | UI hotfix target resolution | **UI** `commands.go:3848,3912`; multi `multihotfix.go:206,236` | UI (`PatchManager.ApplyContext` commands.go:905) | **UI keys.go:481-533** | UI `MutationEvidence` | UI |
| `$fix` | UI | UI handoff | **UI** `agents.go:770`+recovery | UI | UI | UI | UI |
| `$test` | UI | UI | none | none (read-only) | UI test-confirm gate | UI | UI |
| `/ask` | UI | UI Context Planner | **UI** `stream.go:288` | none | none | UI | UI |
| `/investigate` | UI `agents.go:108-137` | mode engine `DispatchStrategy` | mode engine `dispatcher.go:169` + `toolrunner.go:202` | none (diagnostic shell) | none | mode engine ledger | UI |
| `/plan` | UI | mode engine (signal/intent compiler) | **UI** `commands.go:1276-1278` via plan engine provider funcs | none (plan store) | UI plan gate | plan engine `PlanStore` | UI |
| `/build` | UI | UI | **UI** `commands.go:4509,4735,5062` + `2733` | UI | **UI keys.go:659-704** | UI | UI |
| `/review` | UI `agents.go:426` | mode engine (deterministic) | none | none (sandbox tests) | none | mode engine `ReviewLedger` | UI |
| `izen run` CLI | `cmd/izen/runtime.go` | V3 pipeline | `cmd/izen` adapters | `pkg/kernel` | none | pipeline result | CLI |

**Deterministic answer to the 7 audit questions:**

1. **Who receives user input?** The UI (`internal/ui/keys.go:936` →
   `handleInput`). The Runtime facade also receives a duplicate record.
2. **Who decides strategy?** The UI (hybrid router `routeFreeInput`,
   fast-track, hotfix target resolution, engine-first strategy layer). The
   mode engines decide strategy *only inside* their engines, reached from the UI.
3. **Who owns provider invocation?** The UI (12 direct call sites). The plan
   and investigate engines own provider calls for their modes. The runtime
   handlers own none.
4. **Who owns filesystem mutation?** The UI (`m.execEng.BeginTransaction /
   CommitTransaction / RollbackTransaction` at 11 sites; `PatchManager.Apply`
   via `applyPatchWithDeadline` at 5 sites). The build mode engine owns
   `os.WriteFile` (executor.go:413) but is not wired in the production
   composition root.
5. **Who owns approval?** The UI (keys.go approval gates). The runtime
   handlers' approval is a fabricated `InMemoryApprover` record.
6. **Who creates execution evidence?** The UI (`execution.MutationEvidence`,
   `ExecutionProof`, `lastExecutionStrategy`). The engines produce ledgers the
   UI stores.
7. **Who renders the final state?** The UI — which is correct, but it renders
   states it also authored, so the "projection" is a mirror of its own
   execution rather than a projection of a runtime.

---

## 3. Finding Category A — Execution Authority

Every provider/mutation/verification call, classified GOOD (runtime owns) vs
BAD (UI owns).

### A.1 Provider invocation — BAD: UI owns it (12 sites)

| file:line | Function | Path | Classification |
|---|---|---|---|
| `internal/ui/commands.go:2733` | `runBuildFastTrack` | /build fast-track, `$hot`, bare build | **BAD** — UI owns provider + model + prompt |
| `internal/ui/commands.go:3848` | `proposeHotfixPatch` | `$hot`, `$prompt`-targeted | **BAD** |
| `internal/ui/commands.go:3912` | `proposeHotfixPatch` retry | `$hot` | **BAD** |
| `internal/ui/commands.go:4509` | `proposeBuildPatch` | /build per-task | **BAD** |
| `internal/ui/commands.go:4735` | `proposeBuildPatch` full-rewrite | /build | **BAD** |
| `internal/ui/commands.go:5062` | `proposeHybridTemplatePatch` | /build template | **BAD** |
| `internal/ui/commands.go:6990` | `runDiagnoseCmd` | `$diagnose` | **BAD** (read-only, but UI owns provider) |
| `internal/ui/commands.go:7074` | `runAskPromptHandoffCmd` | `$prompt` | **BAD** (read-only) |
| `internal/ui/stream.go:288` | `streamCmd` | bare input, /ask | **BAD** |
| `internal/ui/agents.go:770` | `runCommitCmdAgent` | /commit | **BAD** (owns git mutation too) |
| `internal/ui/multihotfix.go:206` | `proposeMultiHotfixPatch` | `$hot` multi-file | **BAD** |
| `internal/ui/multihotfix.go:236` | `proposeMultiHotfixPatch` retry | `$hot` multi-file | **BAD** |

Good-context provider invocations (owned by engines, not UI):

| file:line | Owner | Path |
|---|---|---|
| `internal/modes/plan/engine.go:357,398,1940` | plan engine (func adapters) | /plan synthesis |
| `internal/modes/investigate/dispatcher.go:169` | investigate engine | /investigate dispatch |
| `internal/modes/investigate/toolrunner.go:202` | investigate engine | /investigate root-cause |
| `internal/runtime/compose/compose.go:494-495,558` | composition root wiring | plan + intent router |

### A.2 Filesystem mutation — BAD: UI orchestrates it

| file:line | Call | Owner |
|---|---|---|
| `internal/ui/commands.go:905` | `m.execEng.Patches.ApplyContext` (applyPatchWithDeadline) | **BAD** — UI |
| `internal/ui/commands.go:4831` | `applyTrivialTemplate` → disk write | **BAD** — UI (no approval) |
| `internal/ui/model.go:2316` | `m.execEng.CommitTransaction` | **BAD** — UI |
| `internal/ui/update.go:1248,1614,1659,1714,2128,2274` | `Commit/RollbackTransaction` | **BAD** — UI |
| `internal/ui/keys.go:681` | `RollbackTransaction` | **BAD** — UI |
| `internal/ui/lifecycle.go:244` | `RollbackTransaction` (/drop) | **BAD** — UI |
| `internal/ui/program.go:429` | `app.Execution.RollbackTransaction` (rollback CLI) | **BAD** — UI entry |
| `internal/modes/build/executor.go:413` | `os.WriteFile` (ApplyMutation) | GOOD — mode engine, but **not wired in production composition** |
| `internal/execution/patch.go:832` | `os.WriteFile` inside PatchManager.Apply | GOOD — execution runtime owns the actual write |

### A.3 Verification — MIXED

| file:line | Call | Owner |
|---|---|---|
| `internal/execution/patch.go:851` | `Verifier.RunAll()` inside apply | GOOD — runtime |
| `internal/execution/verify.go:335` | `Verifier.RunAll` | GOOD — runtime |
| `internal/modes/build/executor.go:466` | `go build ./...` | GOOD — mode engine |
| `internal/modes/review/engine.go:451` | sandboxed `go test` | GOOD — mode engine |
| `internal/ui/commands.go:5827` | `runTestEngine` → `go test -v` | **BAD** — UI shells out to `go test` directly |
| `internal/ui/commands.go:5909` | `runBuildEngine` → `go build` | **BAD** — UI |

### A.4 Approval — BAD: UI owns it; runtime approval is fake

| file:line | Call | Owner |
|---|---|---|
| `internal/ui/keys.go:481-533` | `$hot` approval gate | **BAD** — UI |
| `internal/ui/keys.go:536+` | multi-file approval | **BAD** — UI |
| `internal/ui/keys.go:659-704` | proposal approval (Alt+A/Alt+R) | **BAD** — UI |
| `internal/runtime/handlers/handlers.go:308-313` | `resolveApproval` → `InMemoryApprover` fabricates `{File: patchID, LinesAdd: 1}` | **FAKE STATE** — no real mutation |
| `internal/runtime/handlers/handlers.go:505` | `InMemoryApprover.Resolve` default fabrication | **FAKE STATE** |

---

## 4. Finding Category B — UI Responsibility Audit

The UI must only: receive keyboard events, display state, display streaming
output, display approval requests, render the execution timeline.

Logic currently inside the UI that belongs to the runtime:

| Responsibility | UI location(s) | Belongs to |
|---|---|---|
| **Model routing** | `model.go:1516 routeModel`, `syncPipelineTiers`, every call site sets `req.Model` | Strategy/Runtime |
| **Context construction** | `commands.go:2625-2671,3788-3833,4411-4495,5033-5055,6989-6997`; `stream.go:150-247`; `agents.go:758-763`; `multihotfix.go:187-199` | Runtime (context compiler) |
| **Prompt building** | every call site via `prompt.*` + `m.effortFromTasks`, `m.tieredModePrompt` | Runtime |
| **Patch creation (response→patch parsing)** | `commands.go:3001-3010,3855-3863,3976-3986,4026-4034,4118-4128,4685-4693,4763-4776,5079-5087`; `proposals.go:45-176`; `update.go:3024-3074` | Runtime (artifact pipeline) |
| **Mutation execution** | `commands.go:905,2395,2552,5146,4831`; `update.go:1248,1614,1659,1714,2128,2274`; `model.go:2316`; `keys.go:681`; `lifecycle.go:244` | Runtime (mutation runtime) |
| **Verification orchestration** | `model.go:2335`; `update.go:2191,2283,2292`; `commands.go:5827,5909` | Runtime |
| **Approval** | `keys.go:481-533,536+,659-704` | Runtime (approval boundary) |
| **Strategy decisions** | `commands.go:375 routeFreeInput` (hybrid router), `intent_dispatch.go:34-59` (permission re-resolution), `engine_first.go` (engine-first routing), `commands.go:415-418` ($hot fast-track), `commands.go:265-308` (compressor fast-track) | Intent Resolver / Strategy Engine |
| **Intent classification (duplicated)** | `handlers.go:349 ClassifyIntent` (runtime, keyword); `modes/investigate/dispatcher.go:510 ClassifyIntent`; `internal/router/classifier.go`; `gateway.CompressPrompt`; `retrieval.CompressPrompt`; `engine_first` strategy | one classifier |

---

## 5. Finding Category C — Event Contract Audit

### Canonical runtime event stream (target) vs current events

| Target event | Current equivalent | Status |
|---|---|---|
| ExecutionStarted | `EventCommandReceived` (`command.received`) | authoritative but telemetry-thin |
| StrategySelected | none (UI-internal `lastExecutionStrategy`) | **missing** |
| TargetResolved | none | **missing** |
| ContextPrepared | none (UI-internal `lastContextEnvelope`) | **missing** |
| ModelInvoked | none | **missing** |
| ArtifactProduced | `EventPatchParsed` / `EventPatchValidated` | partial |
| ApprovalRequired | `EventApprovalRequested` | authoritative |
| MutationStarted | `EventPatchAttempted` | partial |
| MutationCompleted | `EventPatchApplied` | authoritative |
| VerificationCompleted | none (UI-internal `go test` orchestration) | **missing** |
| ExecutionFinished | `EventStageCompleted` / `EventExecutionFailed` | partial |
| ExecutionFailed | `EventExecutionFailed` | authoritative (has classification) |

### Classification of current events

- **Authoritative:** `EventPatchApplied`, `EventPatchAttempted`,
  `EventExecutionFailed`, `EventApprovalRequested`, `EventPhaseChanged`,
  `EventPlanStaged` (from plan engine), `EventPatchParsed/Validated/Rejected`
  (from patch engine).
- **Telemetry only:** `EventCommandReceived`, `EventStageCompleted`,
  `EventActivity`, `EventEngineTelemetry`, `EventSelfHealing*`, `EventPlanFallback`.
- **Duplicated:** `EventIntentClassified` + `EventIntentParsed` (two classifiers
  on the same signal), `EventPatchApplied` emitted by BOTH the real patch engine
  (executor.go:419) and the fake runtime handler (handlers.go:261).
- **Misleading / fake:** `EventPatchApplied` emitted by `ApprovePatchHandler`
  via `InMemoryApprover` fabricated record — renders "applied patch +1/-0" with
  no file mutation; `EventPlanStaged` emitted by `SubmitPromptHandler` from pure
  newline splitting — renders a staged plan with no plan.
- **Double-rendered:** `EventIntentClassified`, `EventPhaseChanged`,
  `EventApprovalRequested` are subscribed by the UI both as raw `domainEventMsg`
  (program.go:310-321) AND as translated `presentationEventMsg`
  (event_translator.go:75,77,86), so the viewport logs each twice.

---

## 6. Duplication Inventory (Rule 4)

| Concern | Duplicated implementations |
|---|---|
| Intent classifiers | `handlers.ClassifyIntent` (keyword) · `investigate.ClassifyIntent` (keyword) · `router` semantic classifier · `gateway.CompressPrompt` · `command.GenerateFallbackPlan` · strategy `Select` |
| Execution entry | UI `handleInput` rich path + Runtime `SubmitPromptCmd` record path (keys.go:936,950) |
| Approval | UI keys.go gates + runtime `InMemoryApprover` (fake) |
| Patch applied event | real engine + fake handler |
| Provider access | UI `m.provider` (direct) + `app.Provider()` (composed) + mode engines |
| Evidence | UI `ExecutionProof`/`MutationEvidence` + engine ledgers (not unified) |

---

## 7. Architecture Diagrams

### 7.1 Before (as-built)

```
                    User
                     |
                     v
              +-----------+      parallel record       +----------------------+
              |   UI (TUI)  |<------------------------| Runtime facade (thin)|
              | 19k LOC monolith|   SubmitPromptCmd     | handlers = telemetry |
              +-----------+      (telemetry only)      +----------------------+
                |    |    |    |
        strategy|    |ctx |    |approval keys
        +-------+    |    +    +----------+
        v            v                v
  strategy.Select  planner       keys.go gates
        |            |
        v            v
   +--------------------------------------+
   |  UI owns provider.Execute/Stream (12) |
   |  UI builds prompts, picks models,     |
   |  parses patches, applies mutations,   |
   |  runs go test, commits MutationSets   |
   +--------------------------------------+
        |                    |
        v                    v
   mode engines        execution.Engine
   (plan/investigate)  (PatchManager/Verifier/MutationSet)
        |                    |
        v                    v
   events bus (partial, telemetry-heavy)  ->  UI renders its OWN states
```

### 7.2 After (target)

```
                    User
                     |
                     v
          Intent Resolver (router.Router / strategy.Select)
                     |
                     v
          Strategy Engine (strategy.Select -> profile + graph)
                     |
                     v
            Execution Runtime (RuntimeExecutor.Execute)
                     |
        +------------+------------+
        |                          |
 Provider Runtime           Mutation Runtime
 (owns Execute/Stream,        (owns PatchManager, MutationSet,
  builds context,              apply/verify/commit, approval)
  parses artifacts)
        |                          |
        +------------+------------+
                     |
              Evidence Store (ExecutionProof)
                     |
                     v
              RuntimeEventBus (canonical stream)
                     |
                     v
              UI (pure renderer — events only)
```

---

## 8. Violations vs Migration Rules

- **Rule 1 (UI must not call provider/patch/mutation):** violated — 12 provider
  sites, 5 apply sites, 11 transaction sites in UI.
- **Rule 2 (ExecutionProof per execution):** partially met — the UI builds
  `ExecutionProof` (execution_proof.go) but the runtime command layer produces
  none; proofs are UI-local, not runtime-owned.
- **Rule 3 (no fake states):** violated — `InMemoryApprover` fabricated
  mutation; `SubmitPromptHandler` fabricated plan staging; UI renders
  verification from its own `go test` orchestration, not a verifier result.
- **Rule 4 (one execution path):** violated — dual dispatch (rich + record),
  duplicated classifiers, duplicated approval systems, telemetry-only runtime.

---

## 9. Evidence Index (canonical file:line)

- Composition root: `internal/runtime/compose/compose.go:337` (Wire), `494-495`
  (plan provider), `555-572` (intent router provider).
- Runtime facade: `internal/runtime/runtime.go:91` (Execute).
- Command types: `internal/runtime/command.go:16-22`.
- Telemetry-only handlers: `internal/runtime/handlers/handlers.go:131-169`
  (SubmitPrompt), `248-296` (Approve/Reject), `308-313` (fabricated approval).
- Fake approver: `internal/runtime/handlers/handlers.go:475-506`.
- UI dual dispatch: `internal/ui/keys.go:936,950`.
- UI provider sites: see A.1 table.
- UI mutation sites: see A.2 table.
- UI verification: `internal/ui/commands.go:5827,5909`.
- Duplicate event render: `internal/ui/program.go:310-321` vs
  `internal/runtime/event_translator.go:71-90`.
- Headless CLI path (the only real runtime-owned execution): `cmd/izen/runtime.go:100`.
