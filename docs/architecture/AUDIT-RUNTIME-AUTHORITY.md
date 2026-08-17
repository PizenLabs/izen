# Izen Architecture Forensic Audit

**Scope:** Runtime ownership, duplicate logic, dead paths & incomplete systems.
**Date:** 2026-08-17
**Method:** Static trace of actual function calls from every entry point. No files modified.
**Result:** This is a forensic audit report only — no corrections were implemented.

---

## 1. Executive Finding

Izen is in a **transitional state where three parallel execution stacks coexist, and the stack the architecture documentation declares authoritative is NOT the stack the interactive runtime actually uses.**

There are **three fully separate execution stacks** in the repository:

1. **V3 pipeline (`pkg/app` + `pkg/kernel` + `pkg/planner` + `pkg/fs`)** — reachable **only** through `izen run` (`cmd/izen/runtime.go:156`). Not part of the TUI at all.
2. **LEA layered engine (`pkg/engine/pipeline`, layers 0–5)** — wired into the TUI by `compose.Wire` (`internal/runtime/compose/compose.go:508`) but **dormant as an executor**: its `Run`/`RunAdaptive`/`ExecutePlan` have no live callers. Only its model router, Layer-1 stack detection, and Layer-4 validation (via `/review`) are exercised.
3. **The TUI runtime stack (`compose.Wire`)** — this is what actually runs. Inside it, **two mutation authorities compete**:
   - **Legacy UI path (the REAL runtime authority)**: autonomy engine → mode engines → **direct provider calls** (`m.provider.Execute`/`ExecuteStream`) → proposals → `m.execEng.Patches.ApplyContext` → `os.WriteFile`. **No canonical lifecycle events, no verification.**
   - **RuntimeExecutor path (the DECLARED authority, DORMANT in the TUI)**: `IntentGateway` → `RuntimeExecutor.Execute` → canonical `events.*` stream → `Approve` → `MutationSet` → `Verifier.RunAll`. **Reachable only when the autonomy engine is nil** (headless/test harnesses).

The single most important finding: **`compose.go:437-438` wires `RuntimeExecutor` + `IntentGateway` and their comments declare them the "single execution authority" for `$prompt`/`$hot`, but `runGatedLine` (`internal/ui/gateway.go:42`) — the only UI caller of `m.executor.Execute` — is reached only when `m.autonomy == nil` (`internal/ui/commands.go:402,416`). In the production TUI the autonomy engine is always wired (`compose.go:642`, `internal/ui/program.go:188`), so the RuntimeExecutor is never invoked.** The authority migration (Steps 1–5 described in `internal/execution/executor.go:27-44`) was built and tested but never cut over.

---

## 2. Actual Runtime Graph

### 2a. Production TUI (autonomy wired — the real path)

```
User input (free-form / $prompt / $hot / /build / /investigate / /plan / /review)
  ↓
m.handleInput                     internal/ui/commands.go:162
  ↓  $decide intercepted first; then deterministic parser:
m.intentFromInput → parser.ParseInWorkspace + directive re-parse
                                    internal/ui/intent_dispatch.go:30
  ↓
m.dispatchASTIntent / mode shorthand / free-form        commands.go:271,274,399
  ↓
m.routeFreeInput / m.runAutonomyRoutedCmd               commands.go:412, autonomy_route.go:27
  ↓
autonomy.Engine.Decide()  (deterministic keyword intent + controller verdict)
                                    internal/autonomy/engine.go:179
  ↓
dispatchAutonomyTrace                                   autonomy_route.go:54
  ├─ DirectResponse → streamCmd (direct provider chat)
  ├─ AskUser → requestAutonomyProposal → grant + re-Decide → continue
  ├─ Block → error
  └─ AutoContinue → executeAutonomyWorkspace            autonomy_route.go:89
        └─ mode switch + dispatch to the mode engine:
             INVESTIGATE → runInvestigateCmd   (investigate.Engine, providers via agents.go:110)
             PLAN        → handleMessageContent → runPlanEngineCmd / intentCompiler / microkernel
             BUILD       → executeAutonomyBuild → stageAutonomyBuild (GenerateFallbackPlan + evidence)
                           → planResultMsg → runBuildCmd
                           └─ ($hot) → handleHotfixCmd
             REVIEW      → runReviewCmd       (review.Engine)
             ASK         → prepareAskStreamCmd → streamCmd (provider chat, planner-gated)
  ↓  BUILD / $hot / $prompt mutation:
runBuildFastTrack / proposeHotfixPatch / proposeBuildPatch / runMultiHotfix
  ↓
m.provider.Execute / ExecuteStream        DIRECT  (commands.go:2787,3998,4659; multihotfix.go:206)
  ↓
buildProposalReadyMsg / hotfixProposalMsg → proposal dock (SemanticProposal + Diff)
  ↓  user Alt+A / approve
applyProposalCmd / applyHotfixPatch / applyMultiHotfixGraph
  ↓
applyPatchWithDeadline → m.execEng.Patches.ApplyContext     commands.go:937
  ↓
PatchManager.Apply → shadow backup → os.WriteFile           internal/execution/patch.go:832
  ↓
(NO Verifier.RunAll — PatchManager.verifier is nil in production)
  ↓
buildResultMsg → "success" report; mutation ledger + activity, NO canonical lifecycle events
```

### 2b. Headless `izen run`

```
$ izen run "<prompt>"  →  cmd/izen/runtime.go
  → app.NewPipeline → pkg/app/pipeline.go:226 Run
  → compiler.IntentCompiler (LLM semantic) → extractor → planner → kernel → TxFS writes
  → pkg/event.EventBus → StatusLine (stderr)
```

This stack never touches the TUI, the autonomy engine, the mode engines, or `internal/events`.

### 2c. Dormant path (built, documented, not wired into TUI runtime)

```
runGatedLine  (gateway.go:42)   [only reached when m.autonomy == nil]
  → IntentGateway.Gate  (internal/execution/intent.go:69)
  → strategy.Select  (internal/execution/strategy/selector.go)
  → RuntimeExecutor.Execute  (executor.go:373)
  → execution graph → canonical events → approval → Approve (executor.go:599)
  → MutationSet → PatchManager.Apply → Verifier.RunAll
```

---

## 3. Authority Matrix

| Responsibility | Intended Authority (per comments) | Actual Authority (runtime) | Other Implementations | Conflict |
|---|---|---|---|---|
| Intent classification | autonomy.Engine.Classify (deterministic; semantic fallback never wired) | Same — autonomy is authoritative for $prompt/$hot/free-form | 12 classifiers exist (see §4) | **Yes** — legacy UI re-classifies via `hasMutationIntent`/`investigate.ClassifyIntent` (commands.go:471,511); SubmitPromptHandler.ClassifyIntent re-classifies for the Runtime facade (handlers.go:402) |
| Target resolution | strategy.Select (`internal/execution/strategy`) | UI resolvers: `resolveAutonomyBuildTarget` (autonomy_target.go:38), `resolveHotfixTarget` (commands.go:3388), `resolveMultiHotfixTargets` (multihotfix.go:50) | 6+ resolvers (§4) | **Yes** — $prompt vs $hot resolve the same mention differently (workspace walk vs regex vs fuzzy) |
| Workspace selection | autonomy.WorkspaceFor / SelectWorkspace | Same — but the UI bypasses it via `modeForAutonomyWorkspace` re-map (autonomy_route.go:289) and legacy mode-first routing in handleMessageContent | pkg/engine/decision, router.Router, handlers.ClassifyIntent | Partial — autonomy decides, UI re-maps workspace→mode |
| Capability resolution | autonomy capability vectors (intent.go:105-124) | autonomy + `internal/modes` capability matrix + `core/capability.CapabilitySet` + `pkg/capability` alignment | 4 sets of capability vocabularies | **Yes** — autonomy caps (read/analyze/propose/mutate/verify) ≠ mode caps (CapRead/Write/…) ≠ core CapabilitySet (CapabilityRead/Write/…) ≠ layer1 caps |
| Authorization | autonomy proposal (grant internally, no /grant) | autonomy proposal gate; **second** approval = patch proposal dock (Alt+A) | `/grant` deprecated handler; `handlers.PatchApprover` seam; `AuthorizationEngine` (`a.Auth`, compose.go:657) | **Yes** — two sequential human gates on $prompt mutation: autonomy proposal THEN patch approval |
| Mutation risk | autonomy controller via `Execution.Risk.ClassifyFileOp` | Same (compose.go:631-635) | `pkg/engine/decision`, `guard` in controlplane | None — risk is single-sourced |
| Context compilation | RuntimeExecutor.compileContext (strategy-owned policy) | UI paths: `fastTrackFileContext` (alignment.go:103), `buildHotfix*Handoff`, planner context for /ask | autonomy.CompileContext (evidence ledger) | **Yes** — executor context contract is bypassed; UI compiles context itself |
| File intelligence | autonomy.intelligence.AnalyzeFile | Same (only invoked from `compileAutonomyBuildEvidence`, autonomy_route.go:233) | internal/planner, retrieval polyglot, pkg/engine/inference | Partial — intelligence is advisory; never gates execution |
| Verification | execution.Verifier.RunAll (executor.Approve) | **None on the legacy UI path** (PatchManager.verifier nil); Verifier only on the dormant executor path | PatchManager micro-fix gate (dormant), layer4 (review only), capability/validator (artifact gate), sandboxed go test (review) | **Yes** — the mutation the user actually triggers is not verified |
| Token accounting | executor.finalizeResult (provider-reported) | UI: `m.provider` usage + `tokenUsageCmd`; execution.Telemetry (UI-owned); stream usage events | execution.Telemetry, events.StreamUsage, session counters | **Yes** — parallel accounting paths; executor.Completed is dead in TUI |
| Patch extraction | changeset.NewPipeline | Same (changeset used on both paths) | `execution.Extract*` helpers, `patch.ParseFileCreateBlocks` | None — single pipeline |
| Execution observability | runtimegraph → canonical events | Legacy path emits **no** canonical events (only `EventActivity`/`EventEngineTelemetry` via global sinks, `EventReasoningStream`) | 5+ event vocabularies (§4) | **Yes** — the real runtime path is invisible to the canonical stream |
| Autonomy loop | autonomy.AutonomousLoop | **PublishTransitions is a UI-side projection** (`publishAutonomyLoop`, autonomy_route.go:129); no engine loop actually iterates | NewAutonomyLoopPreview | Partial — loop is descriptive, not operational |
| Provider invocation | RuntimeExecutor.invokeMutation / invokeReadOnly | UI calls `m.provider.Execute`/`ExecuteStream` directly (fast-track, hotfix, build, commit, plan engine) | plan engine provider, investigate provider | **Yes** |
| Cancellation | operation context + streamCancel | UI-side `operationContext`/`streamCancel` (works on both paths) | runtime CancelCmd | None — single mechanism |
| UI projection | presentation.ExecutionProjection + EventSink | Same, but **only fed on the dormant executor path**; legacy path renders from UI-local state | semantic_renderer, activity_tree, execution_telemetry | **Yes** — the projection contract (gateway.go:120-127) never fires in production |

---

## 4. Duplicate / Competing Systems

### Intent classifiers (12 distinct implementations)
| Implementation | Classification | Production caller | Verdict |
|---|---|---|---|
| `autonomy.Classify` (intent.go:337) | deterministic + optional semantic | `autonomy.Decide` (engine.go:180) | **A — authoritative** for autonomy path |
| `router.Router` + `PromptIntentClassifier` (router/classifier.go, semantic.go) | LLM semantic | `compose.go:590` constructs, **never invoked** | **D — dead-wired** (a.IntentRouter never read) |
| `pkg/engine/intent.Classify` (classify.go:57) | deterministic keyword | MicrokernelPlanner (microkernel.go:93) | A — microkernel path |
| `pkg/app/compiler.IntentCompiler` | LLM semantic | `izen run` only (runtime.go:154) | **C — CLI only** |
| `execution.IntentGateway` → `strategy.Select` | deterministic | gateway.go:58 (dormant in TUI) | **B — dormant fallback** |
| `investigate.ClassifyIntent` (dispatcher.go:510) + UI `hasMutationIntent`/`isStructuralHotfixIntent`/`isRedundantContentIntent` (commands.go:7618,3543,3605) | keyword/regex | UI build/investigate bypass paths (commands.go:471,511,525) | **E — partially migrated / conflicting** (re-classifies inside autonomy-decided workspace) |
| `pkg/grounding.Sanitize` (intent.go:29) | fuzzy keyword | plan engine DiscoverAllowedFiles (engine.go:220) | A — grounding only |
| `internal/parser` IntentAST | structural | UI intentFromInput (intent_dispatch.go:32) | A — parser layer |
| `internal/planner.ClassifyIntent` | keyword | /ask + investigate context planner | A — context planning |
| `handlers.ClassifyIntent` (handlers.go:402) | keyword | SubmitPromptHandler (Runtime facade mirror) | **F — conflicting observer** (re-classifies every input in parallel, handlers.go:153) |
| `internal/core.ClassifyExecutionMode` (core/intent.go:75) | keyword | **no callers** | **D — dead** |
| `gateway.ClassifyIntentMode`/`ClassifyDirectMutation`/`IsFrontendUI`/… (internal/gateway/router.go) | keyword | **no callers** | **D — dead** |

### Target resolvers (6 distinct)
`strategy.Select` collectTargets (B1, dormant), `execution.ResolveTargetSet` (targets.go:90), `resolveAutonomyBuildTarget` (autonomy_target.go:38, workspace walk), `resolveHotfixTarget` (commands.go:3388, regex), `resolveMultiHotfixTargets` (multihotfix.go:50), `workspace.TargetFileResolver` (matcher.go:25). All can resolve the same mention differently.

### Language detectors (7 distinct)
`project.Detect`→`language.Registry` (authoritative, main.go:157), `layer1.Detect` (stack), `inference.detectLanguage/Framework` (framework), `symbol.ExtractorRegistry.DetectLanguage`, `langFromPath` (presentation), `autonomy.intelligence.detectLanguageID` (intelligence.go:112), `router.detectLanguage` (dead).

### Event buses (3 structurally identical)
`internal/events.Bus` (42 types), `pkg/event.MemoryEventBus` (7 types, `izen run`), `pkg/engine/telemetry.EventBus` (8 types). One-way bridge only (telemetry→domain). **No bridge between `izen run` events and the TUI.**

### Mutation systems (3)
`execution.Engine` (m.execEng, PatchManager + MutationSet, UI-authoritative), `RuntimeExecutor` (own PatchManager + MutationSet, dormant), `modes/build.Executor.ApplyMutation` (no production callers — **D**).

### Verification systems (5, none gating the real path)
`execution.Verifier` (executor.Approve only), `PatchManager` micro-fix gate (dormant, patch.go:850), `layer4` DAG (review only), `pkg/capability/validator` (artifact gate), `internal/verification` (skip decisions only).

### Proposal/approval systems (3)
Autonomy proposal (capability authorization), patch proposal dock (mutation approval), `handlers.PatchApprover` seam (never used in production).

### Plan engines (7)
PlanEngine (LLM synthesis), MicrokernelPlanner, IntentCompilerPlanner, `pkg/engine/plan`+`pkg/engine/planner`, `pkg/planner` brownfield/greenfield (CLI only), `internal/planner` (context planning), `pkg/app/plan.go` (CLI only).

### Audit/observability (3 parallel)
`internal/events/audit` (events.ndjson, write-only), `internal/audit` (legacy JSON logs + patch.go plain-text collisions in same file), audit `ReadMutations` etc. (no callers).

---

## 5. Dead / Unused / Half-Wired Logic

| Item | Evidence | Classification |
|---|---|---|
| **RuntimeExecutor + IntentGateway in TUI** | `runGatedLine` only reached when `m.autonomy==nil` (commands.go:402,416); autonomy always wired (compose.go:642) | **D in TUI** — fully built, tested, documented, dormant |
| `router.Router` / `PromptIntentClassifier` | constructed compose.go:590, `a.IntentRouter` never read (only 2 refs) | **D** |
| `pipeline.Engine.Run` / `RunAdaptive` / `AdaptivePlan` | no production callers (tests only) | **D** |
| Layers 0, 2 (knowledge resolution, context governor) | only invoked via dormant `Run` | **D** in TUI |
| `pipeline.Engine.ExecutePlan` (facade) | guarded by `e.provider==nil` (plan/engine.go:818); compose always wires provider (compose.go:527) | **E — half-wired fallback** |
| `internal/events/timeline.Timeline` | started compose.go:461; `a.Timeline()` never called | **D — projection nobody reads** |
| `runtime.LedgerBuilder` / `runtime.ContextLedger` | subscribed + stored `a.Ledger`; never read | **D** |
| `events/audit` `events.ndjson` | written by AuditLogger; zero readers | **D — write-only** |
| `telemetry.Timeline` / `StrategyOptimizer` | zero production users | **D** |
| `modes/build` Engine/Executor (`ApplyMutation`, `ExecuteBuildLoop`) | no production callers; UI only uses `build.StripNonPatchProse`/`IsConversationalOutput` | **D — third unmaintained patch path** |
| `execution.PipelineRunner.ExecuteBuild` | constructed execution.go:100, never invoked | **D** |
| `PatchQueue` apply/stream APIs, `StreamMonitor` | no production callers | **D** |
| `strategy.Compile` / `strategy.ExecutionGraph` / `CheckInvariants` | no production callers | **D** |
| `PatchManager` micro-fix verification gate (patch.go:850) | `SetVerifier` has no production callers | **D — dormant gate** |
| `modes/undo` + `internal/command/router.go` (`NewCommandRouter`) | no production callers; UI undo uses `m.execEng.Checkpoints` | **D** |
| `internal/control/handoff.go` | no production callers | **D** |
| `internal/control/checkpoint.go` CheckpointController | no production callers | **D** |
| `internal/controlplane/risk` | zero importers | **D** |
| `internal/controlplane/failure` | only reachable via dead `internal/agent` | **D** |
| `internal/agent` | zero production callers | **D** |
| `internal/gateway/compressor.go` (whole file) | zero callers | **D** |
| `internal/gateway/squeezer.go` Squeeze/ClassifyComplexity | zero callers | **D** |
| `internal/gateway/router.go` routing heuristics (8 fns) | zero callers | **D** |
| `retrieval.LogDeduplicator` | zero callers, not even tests | **D** |
| `pkg/capability/registry.NewRegistry` | no production callers | **D** |
| `internal/core.ClassifyExecutionMode` | no production callers | **D** |
| `internal/audit` read APIs | no external callers | **D** |
| `pkg/kernel`, `pkg/dag`, `pkg/graph`, `pkg/planner`, `pkg/fs`, `pkg/resource`, `pkg/op`, `pkg/control` scope_guard | reachable only via `izen run` | **C** |
| `internal/agents/context_reducer.go` SanitizeForensicLedger | no callers | **D** |
| `$grant` / `/grant` handler | explicitly deprecated; not reachable from parser | **B — compatibility seam** |
| `handleAutonomyGrant` | same | **B** |

---

## 6. Contract Violations

| # | Violation | File / Function | Caller → Callee | Why it violates |
|---|---|---|---|---|
| 1 | Declared execution authority never runs | `compose.go:437` wires Executor; `gateway.go:42` `runGatedLine` → `m.executor.Execute` (gateway.go:93,137) | `handleInput` (commands.go:402,416) → runGatedLine — only when `m.autonomy==nil` | Autonomy is always wired; RuntimeExecutor (the "single execution authority" per compose.go:132-142) is bypassed by the actual runtime |
| 2 | UI invokes providers directly despite "UI MUST NOT call a provider" contract | `commands.go:2787` `m.provider.ExecuteStream`; `commands.go:3998,4659` `m.provider.Execute`; `multihotfix.go:206` | `runBuildFastTrack`/`proposeHotfixPatch`/`proposeBuildPatch`/`proposeMultiHotfixPatch` | Executor's comment (executor.go:36-38) says the UI must not; the actual runtime does |
| 3 | UI applies patches directly through Engine's PatchManager | `commands.go:937` `applyPatchWithDeadline` → `m.execEng.Patches.ApplyContext` (patch.go:972) | `proposals.go:521,634,658`, `commands.go:4981,5351`, `multihotfix.go:435` | Executor owns the mutation boundary by design (executor.go:42-44); the runtime bypasses it |
| 4 | Second approval system appears after autonomy authorization | autonomy grant (autonomy_proposal.go:80) → then patch approval dock (`buildProposalReadyMsg` → Alt+A → applyProposalCmd) | `executeAutonomyProposal` → `dispatchAutonomyTrace` → build flow | Autonomy contract says authorization is internal (proposal.go:13-14); a second human gate re-authorizes the same mutation |
| 5 | Autonomy decides BUILD but legacy logic re-classifies inside the workspace | `commands.go:511-530` `investigate.ClassifyIntent`/`hasMutationIntent` bypass `/build` or `/plan` | `handleMessageContent` (ModeInvestigate case) | Downstream re-classification overrides the runtime's decision, re-deriving the execution path |
| 6 | Two target-resolvers disagree on the same request | `resolveAutonomyBuildTarget` (autonomy_target.go:38) vs `resolveHotfixTarget` (commands.go:3388) vs `strategy.Select` (selector.go:434) | autonomy BUILD vs `$hot` vs gated path | Autonomy resolves the target; a different subsystem re-resolves it differently |
| 7 | Model never reasons "over evidence only" | `fastTrackFileContext` (alignment.go:103) reads and injects the **entire** target file; `compileAutonomyBuildEvidence` evidence ledger also injected (alignment.go:86-88) | `runBuildFastTrack` (commands.go:2679) | Architecture requires bounded evidence regions (autonomy/handoff.go:11); the runtime hands the whole file |
| 8 | Mutation applied with zero verification | `PatchManager.Apply` verification gate (patch.go:850) dormant (`SetVerifier` no prod callers); no `RunAll` in UI apply path | `applyProposalCmd`/`applyHotfixPatch`/`applyMultiHotfixGraph` → `PatchManager.Apply` → `os.WriteFile` (patch.go:832) | Intended model: evidence → mutation → verification → diagnosis. Runtime: evidence → mutation → **success report** |
| 9 | Success reported even on no-change or no verification | `patch.go:940-941` `recordMutationEvidence(patch, OutcomeChanged, "")` unconditionally; buildResultMsg renders success | apply → report | "verification says nochange but the system reports successful mutation" — exactly this class of failure |
| 10 | Whole-file rewrite fallback can corrupt content | `executor.go:905-910` `modified==raw` when extraction fails; `patch.go:764-783` forced full-content fallback for ≤50KB | `invokeMutation` (dormant path) / apply | Evidence → no artifact → mutation path still executes with unvalidated raw text |
| 11 | Orchestrator phase changes do not drive execution | `orchestrator.Transition`/`Force` (commands.go:1489,2378) | UI calls them for presentation only | Orcestrator is meant to map phases (compose.go:608-614); execution actually flows via mode handlers |
| 12 | Facade path shadowed by direct provider | `plan/engine.go:818` guard | compose.go:527 always sets provider | The Layer 0-5 pipeline as plan synthesizer can never run in production |

---

## 7. Information Loss Points

| Information | Created | Transformed | Consumed | Disappears at |
|---|---|---|---|---|
| Intent + confidence | autonomy.Decide (engine.go:179) | Trace → UI render | execution dispatch | **Confidence never reaches the build path** — dropped between `dispatchAutonomyTrace` and `runBuildCmd` |
| Target + target confidence | autonomy intent regex + `resolveAutonomyBuildTarget` | candidates/selection | fast-track targets | **Confidence discarded**; target re-resolved by `fastTrackFileContext`/patch flow |
| Structural evidence (orphan text, redundancy) | `compileAutonomyBuildEvidence` (autonomy_route.go:233) → task.Evidence | `fastTrackGoals` (alignment.go:86-88) | prompt | **Diluted** — the full file is also injected (alignment.go:116-122), so evidence is advisory, not authoritative |
| Strategy decision | strategy.Select (dormant) | — | — | **Never computed on the real path**; `m.lastStrategyGraph` always nil (gateway.go:66, commands.go:3168) |
| Context policy / budget | executor.compileContext (executor.go:815) | — | — | **Never runs** — UI compiles context ad hoc |
| Model output | provider stream/execute | changeset.NewPipeline → patch + diff | proposal dock | Diff is **best-effort** (`compileDiff` returns "" on failure; the applied content is what's written, not the rendered diff) |
| Mutation evidence | PatchManager.recordMutationEvidence | execution proof (dormant) | UI ledger/activity | **Canonical evidence (ExecutionProof) never produced on the real path**; only `[ OK ] patched` activity lines |
| Verification result | — | — | — | **Never produced on the real path** — no verifier runs |
| Token usage | provider-reported usage | UI tokenUsageCmd / session counters | footer | Multiple accounting paths; executor.Completed (authoritative per executor.go:1258) is dead in TUI |
| Execution proof / runtime graph | RuntimeExecutor (dormant) | — | $inspect | **Never created**; `m.recordRuntimeProof` only receives results on the gated path |
| Canonical events | — | — | UI projection | **Real path emits none** — `handleDomainEvent`/execView projections (model.go:2039) only fire on the dormant path |

---

## 8. Execution Artifact Chain

```
intent → evidence → model → artifact → diff → proposal → mutation → verification
```

**Expected (per autonomy_route.go:155-173 comments and executor doc):** evidence compiled → model reasons over evidence → artifact → diff → proposal → approval → transactional mutation → verify → diagnose/loop.

**Actual (production path):**

| Step | Actual | Result |
|---|---|---|
| intent | autonomy.Classify (deterministic) | ✓ present |
| evidence | compileAutonomyBuildEvidence → task.Evidence → prompt | ✓ present, **but diluted by full-file injection** |
| model | `m.provider.ExecuteStream` (direct, commands.go:2787) | ✓ |
| artifact | tool calls buffered → patches (commands.go:3056-3064) OR code-block extraction (`ResolveModifiedContent`) | Partial — no artifact contract validation on the fast-track path (artifact gate `m.execEng.Artifact.ValidateContent` only in proposeBuildPatch) |
| diff | changeset.NewPipeline | Partial — best-effort; empty on failure |
| proposal | SemanticProposal dock | ✓ |
| mutation | `applyPatchWithDeadline` → `m.execEng.Patches.ApplyContext` → `PatchManager.Apply` → `os.WriteFile` | ✓ **but writes raw content without verification gate** (patch.go:850 dormant) |
| verification | **nothing** | ✗ **absent** |
| diagnosis/loop | **nothing** | ✗ **absent** |

**The "evidence → no artifact → no diff → mutation path still executes → nochange" failure class is real:** `executor.go:905-910` (`modified = raw` when extraction fails) and `patch.go:764-783` (forced full-content fallback) both permit unvalidated model text to reach disk; `patch.go:940-941` then records `OutcomeChanged` and the UI reports success regardless of whether the content actually changed.

---

## 9. Workspace Audit

| Workspace | Allowed (contract) | Forbidden | Actual entry | Can autonomy enter | Bypassed by | Performs work outside contract |
|---|---|---|---|---|---|---|
| **ASK** | Read (autonomy/workspace.go:73-104) | Mutate | `/ask`, direct response, autonomy DecisionDirectResponse, `modeForAutonomyWorkspace(ask)` | Yes | `handleMessageContent` ModeAsk → `prepareAskStreamCmd` → provider chat | No mutation ✓; **but `/ask <mutation>` is routed through autonomy and escalates to a proposal that CAN end in build mutation** (commands.go:293-295) — the read-only boundary is renegotiated, not enforced |
| **INVESTIGATE** | Read + Analyze | Mutate | `/investigate`, autonomy, mode switch | Yes | `handleMessageContent` ModeInvestigate **re-classifies intent** and can jump to /plan or /build (commands.go:511-530) — bypasses the workspace | Violates its own contract by silently transitioning |
| **PLAN** | Read + Analyze + Propose | Mutate | `/plan`, autonomy | Yes | intentCompiler/microkernel prime paths replace the plan engine for greenfield | No mutation ✓ |
| **BUILD** | Read+Analyze+Propose+Mutate+Verify | — | `/build`, autonomy BUILD | Yes | The intended executor (RuntimeExecutor) is bypassed for the legacy direct-provider path | **Verify is in the contract but never runs**; mutation happens outside the canonical event stream |
| **REVIEW** | Read + Analyze + Verify | Mutate | `/review`, autonomy | Yes | clean-tree fast path (commands.go:309) | Sandboxed `go test` only; no workspace mutation ✓ |

---

## 10. Current `$prompt` Failure Trace (`$prompt find and remove the redundant content from @index.html`)

| Step | Expected | Actual | Missing/Conflict | Location |
|---|---|---|---|---|
| 1. Detect HTML | language/intelligence detect | `autonomy.intelligence.detectLanguageID` → HTML via extension | ✓ | intelligence.go:112 |
| 2. Inspect size/complexity | AnalyzeFile metrics | `compileAutonomyBuildEvidence` → `m.autonomy.CompileContext` → AnalyzeFile | ✓ (only if evidence compiled) | autonomy_route.go:233-243 |
| 3. Select structural strategy | strategy dispatch (AST/semantic/tree-sitter) | `autonomy.CompileContext` strategy dispatch → semantic/HTML compile | ✓ | autonomy/context.go:185-223 |
| 4. Read file (small) | read + analyze | Multiple reads: `compileAutonomyBuildEvidence` os.ReadFile + `fastTrackFileContext` os.ReadFile + patch apply re-read | ✓ but redundant | autonomy_route.go:234, alignment.go:116, patch.go:652 |
| 5. Detect orphan/redundant content | structural analysis | `hotfix.ResolveRedundantTargets` → `formatRedundancyLedger` | ✓ | autonomy_route.go:245, commands.go:3330 |
| 6. Produce evidence region | bounded ledger | Evidence ledger attached to task.Evidence | ✓ | autonomy_route.go:210-214, alignment.go:86-88 |
| 7. AI reasons over evidence only | bounded evidence regions | Model receives **both** the evidence ledger AND the **entire** file content | **Conflict** — full-file injection dilutes the evidence contract | alignment.go:103-128 |
| 8. Concrete mutation artifact | bounded artifact | Tool calls / code block → patches | ✓ (weak validation on fast-track) | commands.go:3056-3064 |
| 9. Extract + validate artifact | changeset + artifact gate | `changeset.NewPipeline().Run` for diff; artifact gate `ValidateContent` **not called on fast-track path** | **Missing** on this path | commands.go:4240 vs 4817-4818 |
| 10. Generate MutationProposal | proposal with diff | `buildProposalReadyMsg` → SemanticProposal | ✓ | gateway.go:329-365 |
| 11. Show actual diff | real diff | best-effort diff (empty on pipeline failure) | Partial | executor/compileDiff concept not used here; UI renders `p.Diff` |
| 12. User chooses Apply | human approval | Alt+A → applySingleProposal → applyProposalCmd | ✓ | keys.go:749, proposals.go:474 |
| 13. Apply transactionally | MutationSet | `execEng.BeginTransaction` (commands.go:2606) + `PatchManager.Apply` + `CommitTransaction` | ✓ (transaction exists) | patch.go:658-663, model.go:2567 |
| 14. Verify | verifier gate | **No verifier runs** — `PatchManager.verifier` nil; no `RunAll` anywhere in the UI apply path | **Missing — the step is absent** | patch.go:850 (dormant), no UI caller |
| 15. Diagnose / loop on failure | micro-fix loop | Micro-fix loop only exists behind the dormant gate | **Missing** | patch.go:850-887 |
| 16. Stop when satisfied | objective satisfaction | Build result reported as success after apply, regardless of content change or verification | **Missing** — success is reported unconditionally | patch.go:940-941, buildResultMsg |

---

## 11. Architectural Health Scores

| Subsystem | Score | Explanation |
|---|---|---|
| Intent | 2 | 12 classifiers; autonomy is authoritative for the decision path but the UI and Runtime facade re-classify; confidence is discarded downstream |
| Routing | 2 | Autonomy + AST parser + legacy mode routing + dormant gateway coexist; autonomy usually wins, but handleMessageContent re-routes inside workspaces |
| Autonomy | 4 | Coherent, deterministic, well-tested decision engine; but it only *decides* — the execution it hands off to is the legacy stack |
| Workspace | 3 | Contracts exist and are mostly honored, but INVESTIGATE bypasses itself and BUILD's Verify capability is fictional |
| Capability | 2 | Four incompatible vocabularies (autonomy vs modes vs core vs layer1); the autonomy grant is checked once and never re-verified at mutation time |
| Authorization | 2 | Two sequential gates (autonomy proposal + patch approval) with no shared authority; AuthorizationEngine (`a.Auth`, compose.go:657) is wired but not consulted by the real path |
| Context Intelligence | 3 | autonomy.CompileContext evidence ledger works and reaches the prompt, but the full file is also injected, making it advisory |
| Evidence | 3 | Evidence is produced and delivered, but not authoritative — the model and the patch pipeline can ignore/override it |
| AI Handoff | 2 | Evidence+regions exist (autonomy/handoff.go) but the real handoff is a raw full-file dump |
| Planning | 4 | Three deterministic planners + LLM plan engine, coherently chained; no mutation |
| Artifact Extraction | 3 | changeset pipeline is shared and correct; fast-track path skips the artifact validation gate |
| Proposal | 3 | Two proposal systems (autonomy + patch dock) both work, but duplicate the authorization decision |
| Mutation | 2 | Two live patch-apply authorities (Engine, and dormant Executor) + a dead third (modes/build); the live one has no canonical events and no verification |
| Verification | 1 | The verifier exists and works, but only on the dormant path; the real mutation path is never verified |
| Diagnosis | 0 | No runtime diagnosis loop exists on the real path; micro-fix loop is dormant |
| Loop | 1 | The autonomous loop is a descriptive projection (`publishAutonomyLoop`), not an operational driver |
| Provider | 3 | Provider layer is solid (streaming, usage, model-mismatch guard), but invocation is scattered across UI + engines |
| Observability | 2 | Canonical 42-event stream is complete but the real execution path emits only legacy activity lines; three bus implementations; three write-only projections |
| Cancellation | 4 | operation context + streamCancel + CancelCmd; coherent |
| UI Projection | 3 | ExecutionProjection contract works but only receives events on the dormant path; legacy path renders from UI-local state |

---

## 12. Critical Findings

### P0 — architectural correctness / execution truth
1. **The RuntimeExecutor — documented as the single execution authority (compose.go:132-142, executor.go:27-44) — never runs in the production TUI.** Autonomy always handles input (`commands.go:399-402,416`), and the mutation it triggers goes through `m.execEng.Patches.ApplyContext` with direct provider calls. The entire "engine-first" cutover (git #136/#137) is built, tested, and wired, but the interactive runtime was never switched onto it.
2. **The real mutation path applies files with no verification and reports success unconditionally.** `PatchManager.verifier` is nil in production (patch.go:850 gate dormant), and `recordMutationEvidence(..., OutcomeChanged, "")` (patch.go:940-941) records success regardless of actual content change. The intended evidence → mutation → verify → diagnose chain terminates at mutation.
3. **`git log` shows the intended architecture is "V3 → LEA → engine-first → autonomous" (commits #128–#137), but the codebase converges on a hybrid where the newest layer (autonomy) wraps the OLDEST execution layer (UI direct-provider + `execution.Engine`), skipping the two intervening execution stacks entirely.** Each migration added a new decision layer without retiring the previous execution layer.

### P1 — duplicated authority / broken contract
4. **Two concurrent authorization gates** on every `$prompt` mutation: autonomy proposal (capability grant, autonomy_proposal.go:80) + patch approval dock (Alt+A). Neither knows about the other.
5. **UI invokes providers directly** on the hotfix/build/fast-track/commit paths, violating the explicit executor contract (executor.go:36-38).
6. **Intent/target/context are each resolved twice** by different subsystems (autonomy vs UI heuristics vs strategy.Select; three target resolvers; evidence + full-file context).
7. **The canonical event stream does not observe the real execution** — only the dormant path emits it; the real path emits free-form activity strings, so $inspect and the audit log do not reflect actual mutations.

### P2 — incomplete / dead / unnecessary logic
8. **Dead projections wired into the composition root**: `timeline.Timeline`, `runtime.ContextLedger`/`LedgerBuilder`, `events.ndjson` audit (write-only), `telemetry.Timeline`/`StrategyOptimizer`.
9. **Dead execution machinery**: `modes/build` Executor/ApplyMutation (third patch path), `PipelineRunner.ExecuteBuild`, `PatchQueue`/`StreamMonitor`, `strategy.Compile`, `router.Router`, `internal/control/handoff.go`, `controlplane/{risk,failure}`, `internal/agent`, `modes/undo` wiring, `internal/gateway/compressor.go`.
10. **Half-wired**: `pipeline.Engine.ExecutePlan` facade (shadowed by provider), PatchManager micro-fix verification gate (dormant), Layers 0/2 (dormant in TUI), `/grant` compatibility seam.

### P3 — cleanup / maintainability
11. Three structurally identical bus implementations with disjoint vocabularies and no cross-bridge.
12. `internal/audit` vs `internal/events/audit` both write `.izen/audit` with colliding formats (patch.go:584 plain-text vs audit.go JSON).
13. `globalActivityLog`/`globalEventLog` declared in both `internal/execution` and `internal/retrieval`.
14. `izen run`'s entire `pkg/` stack (`pkg/app`, `pkg/kernel`, `pkg/dag`, `pkg/graph`, `pkg/planner`, `pkg/fs`, `pkg/resource`, `pkg/op`) is a parallel product lineage disconnected from the TUI.

---

## 13. Recommended Correction Order

The minimal correction sequence — each step is a wiring decision, not a refactor:

1. **Decide the single execution authority and make `handleInput`/`routeFreeInput`/`$prompt`/`$hot` call it unconditionally** (P0#1). Either (a) route all executions through `RuntimeExecutor` (the built, tested, event-emitting, verifying path), or (b) explicitly rescind that authority and retrofit canonical events + verification onto the legacy path. Leaving the choice unmade is the root defect.
2. **Wire the verifier into the path that actually mutates** (P0#2): attach the language verifier to the `PatchManager` instance used by `m.execEng` (`SetVerifier`) so the micro-fix gate at patch.go:850 runs, and surface its result instead of unconditional success. Add a content-changed check before recording `OutcomeChanged`.
3. **Make the evidence the only context** (P1#6): stop injecting the full file alongside the evidence ledger in `fastTrackFileContext`/handoff builders; honor the bounded-region contract (autonomy/handoff.go).
4. **Collapse the two authorization gates** (P1#4): make the autonomy proposal the single capability gate and let the mutation approval be the single artifact gate, or fold them into one decision surface with one authority.
5. **Unify target resolution** (P1#6): one resolver used by autonomy BUILD, `$hot`, and the executor; remove the divergent UI resolvers.
6. **Wire the canonical event stream onto the real path** (P1#7): have the legacy apply path publish the lifecycle events the projection already consumes (or route through the executor so it emits them).
7. **Delete or un-wire the dead projections and dead execution surfaces** (P2#8, #9) so future work is not built against a graph that includes phantom authorities.
8. **Only after steps 1–7**: reconcile the three bus implementations and the two audit writers (P3), then retire the `izen run` `pkg/` lineage or explicitly document it as a separate product.