# PHASE 12 — EXECUTION INTELLIGENCE AUDIT

| Field | Value |
| --- | --- |
| Status | AUDIT COMPLETE — read-only; no production code changed at time of writing |
| Date | 2026-09-27 |
| Branch | `fix/runtime` |
| Scope | `$prompt` production execution path: intent → policy → planning → context → Driver → planner → admission → RuntimeExecutor → provider → artifact → mutation → evidence → verification → continuation |
| Method | Graph-indexed reconnaissance (`codebase-memory-mcp`, 37 198 nodes / 205 695 edges, indexed 2026-09-27) + direct source reads + **executable probes** against the real classifier/planner. Evidence tier **Verify (Tier 2)** |
| Verification | `go build ./...` ✅ · `go vet ./internal/execution ./internal/llmstep ./internal/continuation ./internal/autonomy` ✅ · 4 probe programs executed and removed |
| Companion report | `docs/report/PHASE_12_EXECUTION_INTELLIGENCE_REPORT.md` |

> **Purpose.** Before changing any production code, establish exactly where the current `$prompt` execution semantics are insufficient, and prove each gap with a `file:line` citation or an executed probe. Every proposed change below is derived from a defect demonstrated in this document — not from a preference.
>
> **Naming note.** `docs/report/PHASE_12_AGENT_PROTOCOL_ARCHITECTURE.md` is a *different* Phase 12 (agent-protocol boundary, read-only, 2026-09-24). This phase is the execution-intelligence convergence and uses a distinct filename to avoid collision.

---

## 0. Executive answer — the observed failure, decomposed

The reported failure is **reproduced exactly** in this repository, by three independent defects that compound. None of them requires a new subsystem to fix; all three are repairs inside canonical owners.

| # | Defect | Owner | Evidence |
| --- | --- | --- | --- |
| **D1** | `"redesign"` contains the substring `"design"`, and the planning-signal loop is **not** gated on a mutation verb. A request that unambiguously requires file creation classifies as read-only `IntentPlanning` and routes to `WorkspacePlan` — the Driver is never entered. | `internal/autonomy/intent.go:305` | Probe: `"redesign @index.html and remove the redundant blocks"` → `intent=planning, mutates=false, ws=plan` |
| **D2** | The greenfield IR path is a **keyword-driven file enumerator**, not a planner. It synthesizes `index.html` / `styles.css` / `script.js` from prompt substrings with **zero** workspace discovery, **zero** evidence, and **zero** model involvement, and marks them `IsHardcoded: true`. It produces the identical 3 tasks in a completely **empty** workspace. | `internal/engine/planner/irplanner.go:33`, `internal/modes/plan/intentcompiler.go:162` | Probe: empty tempdir → 3 × `FILE_MUTATE` tasks |
| **D3** | The full-artifact (`create_file`) invocation is a **single** provider call. `finish_reason=length` discards the bytes, sets the whole task outcome to `truncated`, and the recovery matrix relabels the contract `FULL_REWRITE → BOUNDED_PATCH` — a bounded SEARCH/REPLACE against a file that does not exist. The canonical bounded-step continuation primitive (`internal/llmstep`) is **not** used on this path. | `internal/execution/executor.go:2662`, `internal/runtime/autonomy/recovery.go:351` | `invokeReadOnly` uses `llmstep.StepState` (`executor.go:3289`); `invokeMutation` does not |

And two amplifiers that turn a recoverable condition into a dead end:

| # | Amplifier | Evidence |
| --- | --- | --- |
| **A1** | The production loop bound is `DefaultLoopBounds()` → `MaxTotalTokens: 8000`. The first observed call cost 4 983 tokens (887 in + 4 096 out). One continuation of any useful size crosses the bound and the loop **terminates** with `max total tokens (8000) exhausted`. Every Driver test uses generous bounds instead. | `internal/autonomy/runtime_loop.go:515`, `:965`; `internal/runtime/compose/compose.go:839` (no `WithLoopBounds`) vs. `truncation_recovery_simple_test.go:70` (`100000`) |
| **A2** | `emitSnapshotActivity` reports **filesystem** cache hits through the **ungated** `globalActivityLog`, bypassing the UI's `logRuntimeDetail` debug gate. A "snapshot cache hit" line is a filesystem fact reported as if it were model-context reuse. | `internal/execution/executor.go:3974-3985` vs. `internal/ui/model.go:2934` |

---

## 1. Current `$prompt` execution trace

### 1.1 The path that actually runs today (probed)

```
$prompt "Please review this project and redesign a professional personal portfolio page
          for me using HTML, CSS, and JS; the author's name is Tom Hunter, an AI Engineer."

ui.handleInput
  → ui.intentFromInput                      internal/ui/intent_dispatch.go:31
      → parser.ParseInWorkspace             internal/parser/parser.go:149   → mints ScopeDynamic
  → ui.dispatchASTIntent                    internal/ui/intent_dispatch.go:139
      → ui.bindScopeProvenance(ScopeDynamic)          :288 / :316
  → ui.dispatchDirectives                   :213
  → ui.routePromptDirective                 :287
  → ui.runAutonomyRoutedCmdExplicit         internal/ui/autonomy_route.go:57
  → autonomy.Engine.Decide                 internal/autonomy/engine.go
  → autonomy.Classify                      internal/autonomy/intent.go:337
      → classifyDeterministic              :254
        ⚠ planningPatterns contains "design"        :206-209
        ⚠ NOT gated on hasMutationLike              :305-309
      ⇒ IntentPlanning, conf 0.85                    [PROBE CONFIRMED]
  → autonomy.SelectWorkspace               internal/autonomy/workspace.go:163
      ⇒ WorkspacePlan (read-only: CapMutate forbidden, workspace.go:86-90)
  → ui.dispatchAutonomyTrace               internal/ui/autonomy_route.go:85
  → ui.executeAutonomyWorkspace            :157
      → handoffExecutionContext                        :164
      → m.setMode(modes.ModePlan)                      :177
      → default: m.handleMessageContent(...)            :197   ◀── D1: Driver NEVER entered
```

`/plan` in Plan mode then runs the deterministic prime path:

```
ui.handlePlanCommand
  → plan.IntentCompilerPlanner.TryPlan     internal/modes/plan/intentcompiler.go:60
      → WorkspaceInspector.Inspect()                        :83   ◀── facts collected…
      → InferenceEngine.Infer(facts, slots)                 :86
      → PolicyEngine.Evaluate(…, TypeFramework)             :89
      → lowerer.ResolveFramework(...)                       :90
      → strategy.IsGreenfieldWebPrompt(prompt)             :113/126 ◀── keyword gate
      → planner.IRPlanner.Generate(prompt)   internal/engine/planner/irplanner.go:33
          ⚠ pure string function — no workspace, no evidence, no model
      → lowerer.PlanLowerer.Lower(lp, fw)                  :138
      → artifactsToTasks(artifacts)                        :162
          ⇒ 3 × Task{Type:"FILE_MUTATE", IsHardcoded:true}       ◀── D2
            index.html / styles.css / script.js
  → planResultMsg{IntentCompiler:true}    internal/ui/commands.go:907-909
  → "Intent compiler plan: 3 task(s) staged from the IR lowerer — no model call."
```

`/build` then advances the staged queue through a **second lifecycle**:

```
ui.handleBuildCommand → handleBuildRun(stepNum)   internal/ui/commands.go:2858
  → dispatchStagedTask(task)                                 :2961
  → runRuntimeTaskRequest(task)                              :2972
  → RuntimeExecutor.Execute(ExecuteRequest{…})
  → strategy.Select → artifactForMutation(OperationCreate) → "create_file"
  → withBudgets → outputForArtifact("create_file", level)     internal/execution/strategy/selector.go:661
      ⇒ MaxOutputTokens = 4096                                        ◀── D3 budget
  → BudgetGuardrail.Check(): TargetTokens==0 (file absent) ⇒ permissive
                                                          internal/execution/budget_guardrail.go:114-120
  → x.invokeStream → ai.Provider
      ⇒ one monolithic invocation; input ≈887 tok, output hits 4096, finish_reason=length
  → Boundary 3: gateFor(target, "length") → CanonicalOutputExhausted
                                                          internal/execution/executor.go:2662
      ⇒ res.Content = ""            (partial bytes DISCARDED)          :1821
      ⇒ res.Proof.Outcome = OutcomeTruncated                           :1830
  → ONE diagnostic string appended                                    :1823-1828
  → ui.projectBuildQueueFromProof(...)  internal/ui/gateway.go:736  ⇒ task "failed", queue HALTS
```

There is **no** Driver on this path, therefore no recovery matrix, no continuation, no
re-scope surface. `projectBuildQueueFromProof` treats `OutcomeTruncated` as a hard
halt because it is not in the `advance` set (`internal/ui/gateway.go:713-726`).

### 1.2 The Driver path (for contrast — reached by `$prompt` only when the classifier yields a mutation intent)

```
ui.executeAutonomyViaDriver                 internal/ui/autonomous.go:43
  → runAutonomousDriver                     :116
  → Driver.Run                              internal/runtime/autonomy/driver.go:313
      → adapter.Resolve(objective)          adapter.go:91   (gateway.SelectStrategy)
      → loop.Start → observeAndRun          driver.go:1033
      → loop.Observe → RuntimeDeciding      :1063
      → decideDefault(obs, bounds)          :1598
      → case RuntimeExecuting:              :1118
          → adapter.Execute(ctx, d.req)     adapter.go:180
              → Boundary 5 digest re-check  adapter.go:192   ⇒ workspace_drift
              → gateway.SelectStrategy      adapter.go:199
              → executor.Execute(ctx, execReq)              :371
      → loop.ConsumeExecution / ConsumeVerification         :1192-1193
      → case RuntimeRecovering:              :1222
          → d.repair = typedRepair          recovery.go:277
              → SubtypeOutputExhausted ⇒ RecoveryStrategy = bounded_patch  :351-371
```

### 1.3 Answered questions A–AE

| # | Question | Answer |
| --- | --- | --- |
| **A** | Where is `$prompt` parsed? | `internal/ui/intent_dispatch.go:31` (`parser.ParseInWorkspace`); also `internal/runtime/handlers/handlers.go:517-534`, `internal/runtime/executor/fastpath.go:83-98`, and `internal/execution/intent.go:99-119` (`IntentGateway.Gate`). |
| **B** | Where is `ScopeDynamic` minted? | `internal/parser/parser.go:149`. Type: `internal/core/domain/execution_intent.go:11`. Bound to the session at `internal/ui/intent_dispatch.go:288` / `:316`. Locked by `internal/architecture/phase1_authorization_boundary_test.go:188`. |
| **C** | Where does `$prompt` become an execution policy? | **Two owners that disagree.** (i) Capability policy: `internal/autonomy/intent.go:337 Classify` → `internal/autonomy/workspace.go:200 SelectWorkspace`. (ii) Strategy/budget policy: `internal/execution/intent.go:78 SelectStrategy` → `internal/execution/strategy/selector.go:602 withBudgets`. |
| **D** | Where is the static IR/task graph generated? | `internal/engine/planner/irplanner.go:33 IRPlanner.Generate` + `internal/engine/strategy/greenfield.go:70 planFiles`, driven from `internal/modes/plan/intentcompiler.go:131` and `:162`. A sibling microkernel path exists at `internal/modes/plan/microkernel.go:60`. |
| **E** | Does the IR lowerer create concrete file tasks before workspace discovery? | **Yes.** `WorkspaceInspector.Inspect()` (`intentcompiler.go:83`) is consumed only for framework inference; the artifact set is fixed by `IsGreenfieldWebPrompt` + `mentionsCSS`/`mentionsJS`. Probe: an empty workspace still yields 3 `FILE_MUTATE` tasks. |
| **F** | Which component owns task decomposition? | Canonical: `internal/execution/planner` (`DecomposeTarget:136`, `StageSemanticSections:205`, `groupSections:407`, `FallbackLineSlicer:465`), reached via `internal/runtime/autonomy/decomposition.go` + `adapter.go:382`. **Non-canonical duplicate:** `internal/engine/planner/irplanner.go` and `internal/modes/plan/microkernel.go`, which emit `plan.Task` view-models directly and bypass the canonical planner. |
| **G** | Who owns model invocation? | `internal/execution/executor.go:3447 invokeStream` (called from `:2628` mutation and `:3345` read-only) → `ai.Provider`. Budget resolution: `internal/llmstep/step.go:68`. |
| **H** | Who owns `max_tokens`? | `internal/execution/strategy/selector.go:611` `outputForArtifact` — `"create_file"` ⇒ **4096**, hard-coded. Clamped by `llmstep.ResolveMaxTokens` (`executor.go:2443`) and `ModelProfile.IsConstrained` ⇒ `ConstrainedMaxTokens = 980` (`budget_guardrail.go:161`). Per-attempt override: `ExecuteRequest.MaxOutputTokens` → `effectiveMaxOutput` (`executor.go:2335`). |
| **I** | Who interprets `finish_reason=length`? | `internal/execution/boundaries.go:75 NormalizeFinishReason` → `CanonicalOutputExhausted`; raised at `executor.go:2662 gateFor` (`boundaries.go:300`); provider value set at `internal/providers/openrouter.go:692` and `internal/providers/capability/capability.go:213`. |
| **J** | Where does `OUTPUT_EXHAUSTED` originate? | `internal/execution/boundaries.go:53` (`OutputGateError`); mapped to the loop at `executor.go:1829-1833`. Sibling typed conditions: `internal/llmstep/step.go:257`, `internal/modes/plan/synthesis_step.go:301`. |
| **K** | What state survives? | Only `ModelInvocation` telemetry (`executor.go:1855-1856`), one bounded `Diagnostic` string (`:1823`), and `res.Proof.Outcome = OutcomeTruncated`. The partial bytes are set to `""` at `:1821`. **No** per-artifact status, **no** completed-artifact set, **no** candidate. |
| **L** | Can the current state represent partial task completion? | **No.** `ExecutionProof.Outcome` is a single terminal enum; there is no PARTIAL artifact state. `continuation.StatePartial` (`internal/continuation/types.go:82`) and `DerivationInput.IsPartialOutput` (`:132`) exist in the pure library but the executor never feeds them. |
| **M** | Where are stream deltas accumulated? | `internal/execution/executor.go:3472` (`content strings.Builder` in `invokeStream`). Read-only accumulates across steps at `:3308`; the mutation path does not. |
| **N** | Can partial output become a structured artifact candidate? | **No** on the mutation path — `executor.go:2662` returns before extraction/validation/approval. **Yes** on the read-only path — `:3372-3380` returns the accumulated delivered result via `llmstep.StepState`. |
| **O** | Where are artifact candidates admitted? | `internal/execution/executor.go:3029 artifactGate` (V3 `policy` pipeline), then patch extraction/resolution, then the approval gate. Pre-admission at `internal/execution/admission.go:368`. Emission: `events.NewArtifactProduced` (`internal/events/events.go:1309`). |
| **P** | Where is mutation authorized? | `internal/execution/auth.go` `AuthorizationEngine.AuthorizeBuild` — invoked from `internal/ui/autonomous.go:654` and the executor admission boundary. **Unchanged by this phase.** |
| **Q** | Where is evidence emitted? | `internal/execution/evidence.go` (`ExecutionEvidence`) + `res.Proof`; projected onto `internal/events/events.go` and persisted to `.izen/audit/events.ndjson` (`internal/runtime/compose/compose.go:890`). |
| **R** | Where does verification occur? | `internal/execution/verify.go:336 RunAll` inside the executor; post-DAG global audit at `internal/runtime/autonomy/objective_verify.go:55`. Evidence: `events.NewVerificationCompleted` (`events.go:1336`). |
| **S** | How does continuation re-enter execution? | **Two mechanisms.** (i) Driver: `driver.go:1222 RuntimeRecovering` → `d.repair` (`recovery.go:277 typedRepair`) → `d.req = req` → `LoopContinue` → `RuntimeExecuting` → `adapter.Execute` (`:1130`). (ii) Staged `/build` queue: `commands.go:2858` → `:2961` → `runRuntimeTaskRequest`; advanced by `projectBuildQueueFromProof` (`internal/ui/gateway.go:736`). **(ii) never enters the Driver.** |
| **T** | Is continuation using the SAME `RuntimeExecutor`? | **Yes** in both mechanisms — a single `*execution.RuntimeExecutor` is constructed once at the composition root and pinned by `TestPhase1_SingleProductionExecutionAuthority`. The defect is a duplicate *lifecycle*, not a duplicate executor. |
| **U** | Where is context cached? | **Workspace:** `internal/execution/executor.go:559 observeSnapshot` + `getSnapshotContent:752` + `invalidateSnapshot:633` (wired at `:604` to `PatchManager.SetOnMutation`). **Model context:** `internal/contextcompiler.Compiler` via `x.compileRequest` (`internal/execution/context_compiler.go:110`) — recomputed per attempt; the `PromptFingerprint` field already exists (`context_compiler.go:212`) but is never used as a cache key. OCC fingerprints: `internal/execution/occ.go`. Topology: `internal/execution/cache/topology.go`. |
| **V** | What does "snapshot cache hit" mean? | Only that the raw bytes were in the executor's in-memory map (`executor.go:3979`). It is a **filesystem** fact. It does **not** imply model-context reuse. |
| **W** | Filesystem acquisition only, or model-context construction too? | **Filesystem acquisition only.** `compileRequest` re-projects the same bytes into a fresh prompt on every attempt. |
| **X** | Which context reaches the provider? | Full artifact: `executor.go:2509` system + `:2514`/`:2519` user + `contextFiles = workspaceFiles([target], true)` (`:2580`) → `compileRequest` (`:2582`) → `ai.Request{MaxTokens: maxOut}` (`:2620`). Bounded patch: `:2564 buildBoundedPatchUserPrompt` over a `selectBoundedPatchWindowScaled` window (`:2557`); no `contextFiles`. Read-only: `:3294` with `workspaceFiles(targets, false)`. |
| **Y** | Are identical fragments resent? | **Yes, within a recovery sequence.** Each attempt re-runs `workspaceFiles` (cache hit — no re-read) and then re-compiles and re-bills the identical projection. |
| **Z–AB** | UI layers / user-facing vs. telemetry | See §7. |
| **AC** | Why does `$prompt` require `/build`? | **Accidental coupling (D1).** `"redesign"` ⊃ `"design"` in `planningPatterns`, ungated at `internal/autonomy/intent.go:305`, routes to the read-only `WorkspacePlan`, and `executeAutonomyWorkspace` falls through to `handleMessageContent` (`internal/ui/autonomy_route.go:197`). Nothing executable is staged or authorized, so the human types `/build`. |
| **AD** | Intentional architecture or accidental? | **Accidental.** The comment at `internal/autonomy/intent.go:217-219` states the goal ("modificationPatterns … are matched after diagnostic/verification/planning signals so 'why is the build failing' never routes to mutation"). The implementation lets a design word *veto* an explicit mutation verb — contradicting the adjacent comment at `:273-277` ("Mutation verbs dominate read-only investigation phrasing"). The asymmetry is visible in the probe: `"build me a personal portfolio site…"` → modification/build, but `"redesign…"` → planning/plan. |
| **AE** | Are planning and execution conflated? | **Yes, in three places.** (1) Classification priority (`intent.go:305`). (2) `strategy.Select` classifies the same prompt as `repository_investigation` with **zero** targets (probe), so even a BUILD-routed request has no discovered target. (3) `internal/engine/strategy/greenfield.go` and `internal/engine/planner/irplanner.go` are named "planner" but *decide the artifact set* — that is execution scope, decided with no evidence. |

---

## 2. Current canonical call graph

```text
                    ┌───────────────────────────────────────────────┐
                    │  UI (projection only)                          │
                    │  intent_dispatch.go · autonomy_route.go        │
                    │  autonomous.go · commands.go · model.go        │
                    └───────────────┬───────────────────────────────┘
                                    │
        ════════════════════════════╪══════════════════════════════════════════
          AUTHORITY BOUNDARY (static)  ▼
        ═══════════════════════════════════════════════════════════════════════════
                                    │
             autonomy.Engine.Decide / Classify            internal/autonomy
             execution.IntentGateway.SelectStrategy       internal/execution
                                    │
                                    ▼
        ───────────────────  autonomy.Driver  (canonical scheduler)  ──────────
                                    │  driver.go:313 Run / :1033 observeAndRun
                                    │  :1284 step · :1598 decideDefault
                                    ▼
             ExecutorAdapter.Resolve  adapter.go:91   (gateway → targets)
             ExecutorAdapter.Execute  adapter.go:180  (single port to execution)
                                    │
        ───────────────────  execution.RuntimeExecutor  (SOLE AUTHORITY) ─────
                                    │  executor.go:1010 Execute
                                    │    :1045 admission
                                    │    :600  context freeze + snapshot cache
                                    │    :2410 mutation branch / :3270 read-only branch
                                    │    :2662 Boundary 3 output gate
                                    │    :3029 Boundary 4 artifact gate
                                    │    :336  Verifier.RunAll
                                    ▼
             ai.Provider.Stream   internal/ai · internal/providers/*
                                    │
        ───────────────────  pure policy libraries (one-way)  ─────────────────
             stepadmission.AdmitStep      internal/stepadmission
             continuation.DeriveNextStep  internal/continuation
             execution/planner            internal/execution/planner
                                    │
                                    ▼
                              mutation → evidence → verification
                                    │
                                    ▼
                      SAME RuntimeExecutor (continuation re-entry)
```

**Broken edges identified:**

- `internal/continuation.DeriveNextStep` is reachable from exactly **one** production call site
  — `internal/runtime/autonomy/driver.go:1162` — and that site is the `workspace_drift`
  branch, where the result is used only to *confirm* a decision the matrix already took.
  The canonical continuation library is **dormant on the truncation path**.
- `internal/engine/planner/irplanner.go` and `internal/modes/plan/microkernel.go` reach
  `plan.Task` without passing through `execution/planner` — a decomposition path that
  `TestDeriveChainHasNoCanonicalImporters` does not currently cover because those files
  are not in the forbidden import set.

---

## 3. Current context flow

```text
target(s)
   │
   ├─ getSnapshotContent(target)                     executor.go:752
   │      hit  → in-memory observeSnapshot           executor.go:559
   │      miss → os.ReadFile + repopulate            executor.go:776
   │      invalidated by PatchManager.onMutation      executor.go:604 → :633
   │
   ├─ workspaceFiles(targets, critical)              context_compiler.go:256
   │      non-critical files capped at 200 KB        executor.go:4287
   │
   └─ compileRequest(...)                            context_compiler.go:110
          contextcompiler.Compiler.CompileRequest
          → AgentContext.CompileResult{UsedTokens, ContextTokens, Truncated,
                                      DropCount, PromptFingerprint, Sources, …}
          → ContextPrepared + ContextCompilation   events.go:1232 / :1252
                                    │
                                    ▼
                          ai.Request{MaxTokens: maxOut}   executor.go:2620
```

**Three distinct context kinds are conflated in the current telemetry:**

| Kind | Today |
| --- | --- |
| **Workspace context** (what Izen knows locally) | `observeSnapshot` + `CompileResult.Sources` |
| **Model context** (what is sent to the LLM) | `CompileResult.UsedTokens` / `PromptFingerprint` |
| **Execution context** (state authorizing work) | `ContextSnapshot` (`internal/execution/context.go:67`), sealed + verified at admission |

The UI is told "snapshot cache hit" (a *workspace* fact) as if it were *model-context*
reuse. See §8.

---

## 4. Current model invocation flow

| Path | Invocations | Continuation primitive | Budget source |
| --- | --- | --- | --- |
| Read-only (`executor.go:3270`) | up to `1 + DefaultMaxContinuationSteps` | **`llmstep.StepState` + `ResponseState` + `ContinuationUserTurn`** (`:3289`, `:3318`) | `effectiveMaxOutput` → `llmstep.ResolveMaxTokens` |
| Plan synthesis (`internal/modes/plan/synthesis_step.go`) | bounded | same `llmstep` primitive | strategy budget |
| **Full-artifact mutation (`executor.go:2410`)** | **exactly 1** | **none** | `outputForArtifact("create_file") = 4096` |
| Bounded-patch mutation | 1 per attempt (Driver-driven) | Driver `typedRepair` (contract relabel only) | `req.MaxOutputTokens` / profile |

The mutation branch is the **only** production LLM path that does not participate in the
bounded-step contract. That asymmetry *is* defect D3.

---

## 5. Current artifact flow

```text
raw provider bytes
   │
   ├─ verbatim = trace.RawOutput                     executor.go:2671-2674
   ├─ line-offset materialization (recovery only)    :2675-2681
   ├─ NO_OP sentinel classification (patch only)     :2683+
   ├─ patch extraction / full-file resolution        (mutation resolution)
   ├─ artifactGate → policy V3 ValidateContent       :3029
   ├─ diff compilation (best effort)                 :3051
   ├─ authorization / approval                       auth.go
   ├─ apply (PatchManager, OCC digest chain)         occ.go
   ├─ invalidateSnapshot(target)                     :604
   └─ Verifier.RunAll → res.Verification             verify.go:336
```

On `finish_reason != COMPLETE` the flow stops at the **output gate**
(`executor.go:2662`) and the bytes never reach extraction. `res.Content = ""`
(`:1821`) is the only thing that distinguishes an exhausted generation from an empty one.

---

## 6. Current failure / continuation flow

```text
CanonicalOutputExhausted
  → OutputGateError                                   boundaries.go:53
  → res.Proof.Outcome = OutcomeTruncated             executor.go:1830
  → Observation{Outcome: truncated, FinishReason,
                MaxOutputTokens, ContractID}          adapter.go:462
  → ClassifyOutcome(truncated) = FailureRecoverable  runtime_loop.go:174
  → DecideRecovery → RecoverySubtype = SubtypeOutputExhausted
                                                      recovery.go:72
  → transitionAvailable?  (latch: RecoveryStrategy != bounded_patch)
  → LoopRepair  "typed transition FULL_REWRITE -> BOUNDED_PATCH"
                                                      recovery.go:210
  → typedRepair: RecoveryStrategy = bounded_patch     recovery.go:356
      adapter.go:311 forces Artifact.Kind = "search_replace"
                                                      adapter.go:317-319
  → next attempt emits a SEARCH/REPLACE block         executor.go:2529
```

**Why this is wrong for the reported scenario:** the target is `index.html`, which does
not exist. A `search_replace` contract against empty content cannot produce a file. The
correct continuation of a *full-artifact* generation is **another bounded step of the same
artifact contract**, not a relabel into a patch contract. This is exactly the
`Task ≠ Invocation` distinction the specification demands, and it is already implemented
for the read-only path but not here.

**And when the run is on the Driver**, `LoopRepair` consumes a recovery cycle and the
subsequent attempt re-enters `RuntimeExecuting` — where
`violation()` (`runtime_loop.go:965`) terminates the run the moment
`l.tokens > MaxTotalTokens` (8 000 in production). The observed 4 983-token first attempt
leaves ~3 000 tokens of headroom for a continuation that needs more.

---

## 7. Current UI execution-state flow

*Dedicated read-only surface audit. Every claim carries `file:line`; three independent
assertions were executed against the source and are marked [VERIFIED].*

### 7.1 The two activity sinks and their gates

| Sink | Location | Gating |
| --- | --- | --- |
| `m.logActivity` | `internal/ui/model.go:2907` | **only** `m.activitySurfaceSealed` (`:2912`) |
| `m.logRuntimeDetail` | `internal/ui/model.go:2934` | `m.execVisibility != presentation.VisibilityDebug` → return (`:2935`) |
| `m.push` | `internal/ui/model.go:4080` | `activitySurfaceSealed` (`:4087`); **the only** path that feeds the Trace demuxer (`:4104`) |

`presentation.Visibility` (`internal/presentation/layers.go:11-24`) is a `uint8` enum whose
zero value is `VisibilityNormal` — so **Normal is the default** and is what a real user
sees. It is cycled by `Ctrl+O` (`internal/ui/keys.go:135-152`, dispatch `:434-437`) and
reset to Normal on each gated dispatch (`internal/ui/gateway.go:174`, `:264`;
`internal/ui/runtime_cutover.go:52`; `internal/ui/model.go:3765`, `:3778`). It is **not**
configurable.

### 7.2 The leak: filesystem telemetry bypasses the visibility gate entirely

Traced end-to-end [VERIFIED across 8 hops]:

```
execution.emitSnapshotActivity            internal/execution/executor.go:3974
  → globalActivityLog(...)                                    :3977 / :3979
  → ui program.go:259  execution.SetActivityLogger(activityFn)
  → activityFn = eventBus.Publish(events.NewActivity(...))     program.go:255-257   ← NO visibility check
  → events.EventActivity = "engine.activity"                   events/events.go:47
  → NOT in the UI subscription list                           program.go:349-418
  → IS in Application.TranslatedEventTypes()                  internal/runtime/event_translator.go:87
  → case ActivityPayload → PresentationActivity               :189-192
  → ui presentationEventMsg                                  program.go:325-327
  → m.handlePresentationEvent                                update.go:673-678
  → m.logActivity("%s", ...)                                 model.go:3893          ← UNGATED
```

**Result:** `[runtime] reading disk index.html (2048 bytes)` and
`[runtime] snapshot cache hit …` reach the chat viewport at `VisibilityNormal`, through
`logActivity`, never touching the `logRuntimeDetail` gate at `model.go:2935`.
The same applies to every `events.ActivityPayload` line (`model.go:3278`) — the whole
retrieval/execution/investigate free-form log.

### 7.3 A second, unrelated visibility system

`TraceVerbose` (`internal/ui/layout_builder.go:340`, default `false`, toggled `Alt+E` at
`internal/ui/keys.go:308-323`) does the *real* suppression, by string-prefix matching:
`isEngineTraceLine` (`layout_builder.go:383-446`, matches `[runtime`, `[phase`, `[stage`,
`[intent`, `[preflight`, `[autonomy]`…) → collapse to one `▸ Trace: …` row per turn
(`:839-865`). **It never consults `execVisibility`.** Two independent visibility systems
exist; neither is authoritative over the other.

### 7.4 Simultaneous duplicate rendering

| Semantic state | Rendered by | Citations |
| --- | --- | --- |
| **Stage line** | 2 surfaces, **same formatter** `renderStageStatus` | `internal/ui/view.go:526` (processing dock) **and** `internal/ui/loading.go:266` (shimmer dock) — both on `m.stageSnapshot()` (`internal/ui/stage.go:318`) |
| **Phase / workflow** | **4** surfaces | header `internal/ui/header.go:125`, `[phase]` log line `model.go:3232`, execution panel `model.go:5875`, dock text `loading.go:260-264` |
| **Token usage** | 2 surfaces + 2 mirrors | footer `internal/ui/footer.go:513-514` / `:312-313`; details `loading.go:343-347`; mirrors `streamBaseInputTokens` `model.go:3159`, `stage.Tokens` `:3157` |
| **File read / mutate** | 2 surfaces | activity record `model.go:3278` **and** ActivityTree row `model.go:2959-2968` / `:2987-2997`, rendered `internal/ui/activity_tree.go:321` — both in the same frame (`model.go:5832` … `:5940`) |
| **Shell exec** | 3 surfaces | ActivityTree `model.go:3003`; tool/batch cards `internal/ui/plan_tool_dock.go:278-298`; `roleSystem` summary `plan_tool_dock.go:239` |
| **Preflight line** | 2 gates on one line | written `model.go:3366`, rewritten by the layout `internal/ui/layout_builder.go:890-898`, with a carve-out at `:391-393` preventing re-collapse |

### 7.5 Dead / unreachable duplicate surfaces

| Surface | Why it is dead |
| --- | --- |
| Plan / TODO checklist `renderPlanDock` (`internal/ui/plan_tool_dock.go:45`) | `m.execPlan` is **never populated in production** — `PlanUpdateMsg`/`PlanStepMsg` are only declared (`internal/ui/messages.go:44-50`) and handled (`plan_tool_dock.go:20`, `:29`); nothing constructs them. `renderPlanDock` returns `""` (`plan_tool_dock.go:46-48`). [VERIFIED] |
| Trace overlay buffer (`Alt+T`) | Fed only by `m.push` (`model.go:4104`); `logActivity` appends directly (`model.go:2917`) and never calls `push` — so **no activity or runtime line ever reaches Trace**. |
| `renderExecutionNarrative` (`internal/ui/loading.go:292`) | Superseded by `renderExecutionLayered` (`:303`); only a test calls it. |
| `stageDisplayLabel` case `"read"` (`internal/ui/stage.go:296`) | No `setStage("read", …)` exists. [VERIFIED] |
| `m.handoffCtx.PendingTodos` | Prompt-synthesis input only; no renderer. |

### 7.6 Answering Z / AA / AB

- **Z — which layer receives these events?** Two: (i) the Bubble Tea UI model
  (`internal/ui/model.go:3010-3400` `handleDomainEvent`, plus `handleEngineEvent:2946` and
  `handlePresentationEvent:3871`); (ii) the Context Ledger projection
  (`internal/runtime/ledger_builder.go:246`).
- **AA — user-facing execution state:** `renderExecutionFrame` / `renderExecutionLayered`
  (`internal/ui/loading.go:303-350`), `renderStageStatus` (`internal/ui/stage.go:318`),
  `renderStageLine` (`:372`), the ActivityTree (`internal/ui/activity_tree.go:321`), the
  skeleton row (`internal/ui/skeleton.go:231`), the footer (`internal/ui/footer.go:129`),
  the boundary cards (`internal/ui/autonomous.go:822`).
- **AB — raw telemetry / debug trace:** everything routed through
  `m.logRuntimeDetail` (`model.go:2934`, Debug-only), the `VisibilityDebug` event stream
  (`loading.go:348-350`), and the `TraceVerbose` collapse (`layout_builder.go:839-865`).
  **Misclassified today:** `events.ActivityPayload` (all `execution`/`retrieval` lines,
  including `emitSnapshotActivity`) is *raw telemetry* but lands in the **user-facing**
  channel via `handlePresentationEvent` → `logActivity` (`model.go:3893`).

### 7.7 UI gaps this phase must close

| Gap | Repair |
| --- | --- |
| Filesystem cache lines reach the default surface | C7 |
| ActivityTree + activity record both render file read/mutate | C7 — the ActivityTree row is the *structured* projection and should be the canonical one; the duplicate free-form record is the raw telemetry and belongs in Trace |
| Trace overlay never receives runtime lines | C7 — route gated detail through `m.push` (or feed the demuxer from `logActivity`) so Trace is actually populated |
| Stage line rendered twice from one formatter | C7 — one surface owns the stage line |
| `execVisibility` vs `TraceVerbose` never consult each other | C7 — do not merge them (they are orthogonal), but make the classification decision **once**, at the write site, instead of by string matching at render time |
| Dead `m.execPlan` / `renderPlanDock` | C7 — remove the unreachable renderer rather than leave a second "TODO" surface that could one day render duplicates |

---

## 8. Existing cache semantics

*(Corrected after a dedicated cache audit. The first draft wrongly claimed the context
compiler had no memoization — it does. The correction is recorded in
`PHASE_12_EXECUTION_INTELLIGENCE_REPORT.md` §20.)*

| Cache | Key | Value | Invalidation | Reached from the production path? |
| --- | --- | --- | --- | --- |
| `Compiler.cache` (`internal/contextcompiler/compiler.go:324`) | SHA-256 fingerprint of the **full normalized `Input`** — incl. every file's path, size, critical/truncated/priority flags **and content** (`compiler.go:1235-1311`) | `*CompiledContext` (the rendered sections) | wholesale map reset at `cacheLimit`=64 (`compiler.go:632-634`) — **not LRU** | **Yes** — `compileRequest` → `CompileRequest` → `Compile` (`context_compiler.go:143` → `request.go:177` → `compiler.go:276` → `:415`); wired instance `compose.go:533,622` |
| `observeSnapshot` (`executor.go:559`) | target, basename, absolute path (3 aliases) | **raw file bytes only** | `PatchManager.SetOnMutation` → `invalidateSnapshot` (`:604`, `:633-643`, deletes all 3) | **Yes** — populated `executor.go:1431`; consumed `:592`, `:799`, `:2387`, `:2461`, `context_compiler.go:262` |
| `OCCVerifier.cache` (`occ.go:194`) | workspace-relative target path | `occFingerprint{size, mtimeNano, sha256}` | implicit — `os.Stat` on every read; a hit needs size **and** mtime unchanged (`occ.go:412-438`); never pruned | **Yes** — `executor.go:1583`, `:2035`, `adapter.go:121` |
| `ContractRegistry.contracts` (`contract.go:253`) | ContractID | `*ExecutionContract` | append-only chains bounded by `MaxRecoveryChainDepth = 4` | **Yes** — `executor.go:1442` (identity ledger, not a content cache) |
| `cache.TopologyCache` (`cache/topology.go:110`) | `SHA256(content)` — **path-agnostic** | `*StructuralSnapshot` | LRU @128; no path/turn invalidation (deliberate — content-addressed) | **No** — reached only from the **async** preflight worker (`handlers.go:262-277`, detached context), never on the `Execute` stack |
| `preflight.ObservationState.snapshots` (`preflight/snapshot.go:45`) | `snap.Target` | `*StructuralSnapshot` | overwrite-only | **No — and its output is never read by any production consumer** |
| `retrieval.NativeGoEngine` (`internal/retrieval/native.go:29`) | file path | parsed AST | none observed | **No** — only `internal/ui/agents.go:108` (investigate mode) |

### 8.1 The three context kinds, correctly separated

| Kind | Today | Gap |
| --- | --- | --- |
| **Workspace context** (what Izen knows locally) | `observeSnapshot` + `CompileResult.Sources` | correct; `invalidateSnapshot` is right |
| **Model context** (what is sent to the LLM) | `CompileResult.UsedTokens` / `PromptChars` / `PromptFingerprint` | **A cache exists and is completely unobserved** — `CompiledContext.CacheHit` (`compiler.go:134,420`) is never projected into `CompileResult` (`internal/observability/compile.go:15-45`), never reaches telemetry, and never reaches the UI |
| **Execution context** (state authorizing work) | `ContextSnapshot` (`internal/execution/context.go:67`), sealed + verified at admission | correct; the frozen `contextspec.ExecutionSpec` however **never reaches the executor** — `ExecutionPayload` (`contextspec/pipeline.go:229`) has zero production callers, so its constraints/decisions/scope/budget are discarded at the UI boundary (`internal/ui/context_boundary.go:41-56`) |

### 8.2 §10 compliance

- **cache hit / cache miss** — both exist and are *correct*, but they are **not
  distinguishable from the model-context layer**, and the model-context layer has no
  observable hit at all.
- **invalidation** — `observeSnapshot`: correct and precise (delete-not-overwrite, all 3
  aliases). `Compiler.cache`: correct by fingerprint identity but wholesale-flushed.
- **changed file / unchanged file** — `observeSnapshot` re-reads on mutation; the compiler
  cache forks because file content is inside the fingerprint. Correct, and currently
  invisible.
- **reused context / refreshed context** — **unreportable today.**

### 8.3 Dormant infrastructure (verified by import closure of `internal/runtime/autonomy` and `internal/execution`)

`internal/understanding`, `internal/discovery`, `internal/project`, `internal/fs`,
`internal/git`, `knowledge.KnowledgeGraph`, `retrieval.NativeGoEngine`, and the
`preflight.ObservationState` / `EventStructuralSnapshot` outputs are all **outside** the
production `$prompt` context path. The only genuine "index" on the path is
`observeSnapshot` + the OCC fingerprint map. **No new repository-indexing subsystem is
needed** — §8's requirement is met by making the existing caches observable, not by
adding one.

### 8.4 On the reported scenario, the filesystem cache is already correct

`index.html` does not exist, so `getSnapshotContent` returns `(nil, false)` and no re-read
occurs. The reported `input: 887` tokens are **already** the minimal model context for a
single-file create. **Reducing them further would make reasoning worse, not better.** The
inefficiency is not input size — it is that a 4 096-token output ceiling is spent on one
attempt and then thrown away.

### 8.5 Additional dead weight found

`system_instructions`, `schema_overlay` and `tool_descriptors` are **reserved and charged**
against the context budget (`compiler.go:431-441`, `:616`) but **excluded from the
transmitted text** by `ContextOnly()` (`compiler.go:202-222`, reservation predicate
`compiler.go:782-784`). The executor never sets `IncludeSchema`
(`context_compiler.go:154-167`), so the schema overlay is charged for bytes that are never
sent. Non-blocking, but it means the reported context-token numbers overstate the real
model context.

---

## 9. Existing budget semantics

| Bound | Value | Owner | Notes |
| --- | --- | --- | --- |
| `outputForArtifact("create_file")` | 4 096 | `strategy/selector.go:661` | **The observed ceiling.** Not derived from target size or capability. |
| `outputForArtifact("replace_block")` | 1 024 / 2 048 / 3 072 by complexity | `selector.go:670-678` | Complexity-tiered, inspectable |
| `ConstrainedMaxTokens` | 980 | `budget_guardrail.go:161` | Capability-derived via `llmstep.ResolveMaxTokens` |
| `ContextPolicyTargetFileOnly` | 4 000 tok / 10 files | `selector.go:626` | |
| `ContextPolicyRepository` | 16 000 tok / 50 files | `selector.go:635` | |
| `SubTaskBudget(maxOutput)` | `0.7 × max_output` | `planner/dag.go` | |
| `DefaultLoopBounds.MaxTotalTokens` | **8 000** | `autonomy/runtime_loop.go:515` | **Production. Applied at `compose.go:839` with no `WithLoopBounds`.** |
| `llmstep.DefaultMaxContinuationSteps` | 3 | `llmstep/step.go:56` | Read-only path only |

**Gap:** §13 requires `max_tokens` to be an *invocation-level* bound derived
deterministically from task type / artifact size / capability / expected output. Today the
`create_file` budget is a single hard-coded constant that is smaller than the actual
artifact for any non-trivial page. The budget is correct in *kind* (it is a request, not a
capability claim) but wrong in *derivation*: it does not consider the artifact's expected
size at all.

---

## 10. Architecture violations / gaps

| ID | Violation | Severity | Owner to repair |
| --- | --- | --- | --- |
| **V1** | A read-only design signal vetoes an explicit mutation verb, forcing manual `/build` (§1.3 AC/AD). | **High** | `internal/autonomy` (classification) |
| **V2** | Task decomposition outside the canonical planner: `internal/engine/planner/irplanner.go` and `internal/modes/plan/microkernel.go` decide the artifact set with no evidence and mark it `IsHardcoded: true`. | **High** | `internal/execution/planner` (canonical) must own the derived set; the enumerators may only *propose* |
| **V3** | `Task ≠ Invocation` is unimplemented on the full-artifact path: one invocation, hard failure on `finish_reason=length`. | **High** | `internal/execution` (reuse `llmstep`) |
| **V4** | `continuation.DeriveNextStep` — the canonical, pure, `IsPartialOutput`-aware continuation engine — is **dormant on the truncation path**. | **High** | `internal/runtime/autonomy` (wire it into `DecideRecovery`) |
| **V5** | Production `MaxTotalTokens = 8 000` bounds the whole logical task, not one invocation — conflating task and invocation budgets (§26 "Budgets should bound model invocations, not arbitrarily truncate logical tasks"). | **High** | `internal/autonomy` (`DefaultLoopBounds`) |
| **V6** | The staged `/build` queue is a second execution lifecycle that shares the executor but not the Driver, so it has no recovery matrix, no continuation, and no re-scope surface. | **Medium** | Pre-existing; **do not expand**. Route its execution through the Driver instead of adding a second scheduler. |
| **V7** | Filesystem cache hits are reported as if they were model-context reuse, through an **ungated** log sink (`executor.go:3974` vs `ui/model.go:2934`). | **Medium** | `internal/execution` (telemetry shape) + `internal/ui` (gate) |
| **V8** | A model-context cache **does exist** (`contextcompiler.Compiler.cache`) and is **entirely unobserved** — `CacheHit` never reaches `CompileResult`, telemetry, or the UI. Additionally the frozen `contextspec.ExecutionSpec` never reaches the executor. | **Medium** | `internal/contextcompiler` (telemetry) + `internal/observability` (`CompileResult`) |
| **V9** | `create_file` budget (4 096) is smaller than realistic artifacts, guaranteeing `finish_reason=length`; §13 requires a size/capability-derived bound. | **Medium** | `internal/execution/strategy` |
| **V10** | The full-artifact recovery relabels to `search_replace` even when the target does not exist, producing a contract that cannot succeed. | **Medium** | `internal/runtime/autonomy` + `internal/runtime/autonomy/adapter.go:311` |

---

## 11. Proposed minimal changes

Every row answers: current owner · current behavior · problem · desired invariant ·
minimal repair · existing component reused · new type/function · tests · why it creates
no competing authority.

### C1 — Classification: mutation verbs must not be vetoed by a design word

| Field | Value |
| --- | --- |
| **Current owner** | `internal/autonomy` — `classifyDeterministic` (`intent.go:254`) |
| **Current behavior** | `planningPatterns` (`:206-209`, contains bare `"design"`) is tested at `:305-309` **without** the `hasMutationLike` guard used at `:283` and `:301`. |
| **Problem** | `"redesign"`, `"redesign and remove"`, `"design and write"` all become `IntentPlanning` → read-only `WorkspacePlan`. V1. |
| **Desired invariant** | A mutation verb decides the intent. Read-only design phrasing is planning **only when no mutation verb is present** — the rule the file already states at `:273-277`. |
| **Minimal repair** | Gate the planning loop on `!hasMutationLike`, exactly as investigation and verification already are. One conditional. |
| **Reused** | The existing `modificationPatterns` table, `hasMutationLike`, `RequiredCapabilities`, `SelectWorkspace`. |
| **New types** | none |
| **Tests** | (a) unit table asserting `redesign`/`design` + a mutation verb ⇒ `IntentModification` ⇒ `WorkspaceBuild`; (b) negative: `"how should I design the architecture"` stays `IntentPlanning`; (c) negative: `"why is the build failing"` stays `IntentDebugging`. |
| **No competing authority** | Classification is an *advisory proposal* consumed by the existing capability→workspace table. It grants nothing; `WorkspaceBuild` is still the only domain whose contract permits `CapMutate` (`workspace.go:92-96`). |

### C2 — Planning: the canonical planner derives the artifact set; enumerators only propose

| Field | Value |
| --- | --- |
| **Current owner** | `internal/engine/planner/irplanner.go`, `internal/engine/strategy/greenfield.go`, `internal/modes/plan/intentcompiler.go` |
| **Current behavior** | `artifactsToTasks` (`intentcompiler.go:162-181`) emits `Type:"FILE_MUTATE"`, `IsHardcoded:true` from a keyword-derived set. Probe: identical output in an empty workspace. |
| **Problem** | V2 — concrete execution scope is fixed before discovery, from prompt substrings, with no evidence. |
| **Desired invariant** | No execution-unit set is staged without a workspace-evidence record naming **why** each target is in scope. `/plan` is read-only either way — nothing mutates — but the *plan* becomes truthful. |
| **Minimal repair** | Replace the `IsHardcoded` marker with a provenance-bearing `Task` derivation: each task carries the **discovered** evidence (file exists / framework detected / prompt token) and the `planner.ExecutionDAG` region/scope it came from. The `GreenfieldWebStrategy` file set becomes the *seed* proposal; a `execution/planner` call over the **inspected** workspace confirms, prunes, or extends it. Absent evidence ⇒ the task is not staged. |
| **Reused** | `inference.WorkspaceInspector.Inspect` (already called at `intentcompiler.go:83` — its facts are simply *used*), `execution/planner.DecomposeTarget`, `SubTaskScope`. |
| **New types** | one pure helper (provenance tuple) — no new subsystem |
| **Tests** | (a) existing behavior preserved for the empty-workspace case only when no evidence exists ⇒ **now 0 staged tasks + an explicit "insufficient project evidence" reason**; (b) a workspace containing `index.html` + `styles.css` stages exactly those and **not** `script.js` unless the prompt/evidence justifies it; (c) architecture test: `internal/engine/planner` and `internal/modes/plan` may not mark a task `IsHardcoded` for execution. |
| **No competing authority** | The plan remains a *proposal*. `artifactsToTasks` output is `plan.Task` view-model; mutation still requires `dispatchStagedTask` → `RuntimeExecutor.Execute` → `AuthorizationEngine`. |

### C3 — RuntimeExecutor: bounded-step continuation for the full-artifact contract

| Field | Value |
| --- | --- |
| **Current owner** | `internal/execution` — `invokeMutation` (`executor.go:2410`) |
| **Current behavior** | Exactly one provider call. Non-`COMPLETE` ⇒ Boundary 3 ⇒ `res.Content = ""` ⇒ `OutcomeTruncated`. |
| **Problem** | V3, V10. A logical task is declared truncated after a single bounded attempt; the partial work is unrecoverable; the successor contract (`search_replace`) cannot apply to a non-existent file. |
| **Desired invariant** | `finish_reason=length` is an **invocation** outcome. The logical task keeps its state, and a bounded continuation of the **same artifact contract** advances it. |
| **Minimal repair** | Wrap the `!patchOnly` (full-artifact) branch in the same `llmstep.StepState` loop that `invokeReadOnly` already uses at `executor.go:3309-3413`. On `gateFor(...) == CanonicalOutputExhausted`: emit `StepExhausted`, `step.Advance()`, rebuild the user turn from `llmstep.ContinuationUserTurn(base, committed, pending, format, maxTokens)`, and re-invoke — **never** relabeling the contract. Preserve the accumulated `artifact candidate` in a bounded, non-mutating buffer. When the step budget is consumed: emit `StepExhausted` + a typed `llmstep.OutputExhaustedError` so the Driver matrix still sees the truthful exhaustion. |
| **Reused** | `llmstep.StepState`, `llmstep.ResponseState`, `llmstep.ContinuationUserTurn`, `llmstep.OutputExhaustedError`, `llmstep.DefaultMaxContinuationSteps`, `events.NewStepStarted/StepExhausted/ContinuationScheduled/ContinuationStarted/StateCommitted/StateRejected`, `x.invokeStream`, `x.compileRequest`. **Nothing new is invented — the primitive already exists and already has tests.** |
| **New types** | one bounded candidate buffer (content + provenance), no new package |
| **Tests** | (a) unit: two truncated responses then a complete one ⇒ task completes, three `ModelInvocation` records, zero mutations before admission; (b) negative: candidate bytes are **never** written to disk without admission + authorization; (c) negative: bounded patch path behavior is unchanged; (d) budget exhaustion with no delivered state ⇒ typed `llmstep.OutputExhaustedError`, not a silent success. |
| **No competing authority** | The loop is **inside** the canonical executor. It re-invokes `x.invokeStream` — the same provider seam — under the same admission, the same `AuthorizationEngine`, and the same OCC digest. It schedules nothing. |

### C4 — Continuation: consult the canonical pure library on exhaustion

| Field | Value |
| --- | --- |
| **Current owner** | `internal/runtime/autonomy` — `DecideRecovery` / `typedRepair` (`recovery.go:163`, `:277`) |
| **Current behavior** | `SubtypeOutputExhausted` → unconditional `LoopRepair` → contract relabel. `continuation.DeriveNextStep` is consulted only for `workspace_drift` (`driver.go:1162`). |
| **Problem** | V4. The canonical continuation engine already implements §14/§15 exactly (`IsPartialOutput` ⇒ `ActionContinue` with a bounded next step; plus `ActionComplete/Blocked/Failed/AwaitingApproval/Stale/NoProgress`) and is bypassed. |
| **Desired invariant** | The recovery matrix reads the pure continuation verdict for exhaustion and maps it — it does not re-derive. A full-artifact contract that has already been continued *within* the executor must not be relabeled again. |
| **Minimal repair** | In `DecideRecovery`, for `SubtypeOutputExhausted`, call `DeriveDriverContinuation` with `IsPartialOutput: true` and map the verdict: `Continue` ⇒ `LoopRepair` **keeping the current artifact contract**; `Blocked`/`NoProgress` ⇒ `LoopAskHuman`; `Stale` ⇒ `LoopAbort`; `Failed` ⇒ `LoopAskHuman`. Extend `DriverContinuationInput` with the fields the library already reads (`CompletedSteps`, `PendingSteps`, `PreviousReason`) so the derivation has real durable state. |
| **Reused** | `internal/continuation.DeriveNextStep`, `DeriveDriverContinuation`, `driverOutcomeToStepOutcome` (already maps `OutcomeTruncated → "partial"`, `admission.go:153-156`). |
| **New types** | none — additive input fields only |
| **Tests** | (a) exhaustion with durable remaining scope ⇒ `LoopRepair`; (b) exhaustion with no remaining scope ⇒ `LoopAskHuman`, never `LoopAbort`; (c) negative: the matrix never returns `LoopComplete` for an unverified observation; (d) architecture test unchanged (`continuation` imports nothing from the runtime). |
| **No competing authority** | `continuation` is a pure proposal function with an architecture-locked import set (`internal/architecture/continuation_invariants_test.go`). The Driver remains the decision owner. |

### C5 — Budget: bound invocations, not the logical task

| Field | Value |
| --- | --- |
| **Current owner** | `internal/autonomy.DefaultLoopBounds` (`runtime_loop.go:509`) |
| **Current behavior** | `MaxTotalTokens: 8 000` is applied to the *whole run* at `compose.go:839` (no override) and terminates the loop at `violation()` (`:965`). |
| **Problem** | V5. A single 4 096-token truncated attempt consumes half the entire task budget, so no continuation is affordable — directly contradicting §26. |
| **Desired invariant** | Per-invocation output is bounded (C3). The run-level bound governs the *number* of invocations and the overall spend ceiling, sized so a legitimate multi-step task can complete. |
| **Minimal repair** | Separate the two budgets: keep a per-attempt `MaxAttemptTokens` concept implicit in the `llmstep` step budget, and raise the run-level `MaxTotalTokens` to a value derived from the model capability + the approved decomposition size (a deterministic function, inspectable), rather than a fixed 8 000. Add the same derivation the tests use explicitly so it is not a magic number. |
| **Reused** | `LoopBounds`, `WidenBounds` (`:682`) — the *existing* mechanism for human-approved plans already widens bounds; extend it to the capability-derived floor rather than inventing a new bound. |
| **New types** | none |
| **Tests** | (a) a 3-step continuation sequence under a constrained model completes within bounds; (b) a pathological loop still terminates on `MaxAttempts` / `MaxIdenticalDecisions`; (c) the bound is derived, not literal — assert the derivation function's output for known capability inputs. |
| **No competing authority** | Budgets bound; they do not authorize. WidenBounds is already human/plan-approved. |

### C6 — Strategy: derive the `create_file` budget instead of hard-coding 4 096

| Field | Value |
| --- | --- |
| **Current owner** | `internal/execution/strategy/selector.go:658 outputForArtifact` |
| **Current behavior** | `"create_file"` ⇒ 4 096, independent of the artifact, the complexity tier, or the model's advertised ceiling. |
| **Problem** | V9. Any page larger than ~16 KB of source is *guaranteed* to truncate; the observed failure is this. |
| **Desired invariant** | §13 — the bound is deterministic, inspectable, bounded, and derived from artifact size + capability + policy, never a single universal limit. |
| **Minimal repair** | Derive the `create_file` request from (a) the strategy's complexity tier, (b) the artifact's expected size where it is known, and (c) the model's declared ceiling, then clamp through the existing `llmstep.ResolveMaxTokens` + `ModelProfile.ClampMaxTokens` chain. Record the derivation inputs on the profile so evidence shows *why*. |
| **Reused** | `withBudgets`, `reasoningForComplexity`, `llmstep.ResolveMaxTokens`, `ModelProfile`, `contextcompiler.ModelLimitsFor`. |
| **New types** | one pure derivation helper |
| **Tests** | table test over {complexity × capability} asserting monotonicity and the constrained-model clamp; negative: a *constrained* model must still be clamped to 980 regardless of the derived request. |
| **No competing authority** | A budget request is not a capability claim and not an authorization; the existing clamp chain already enforces that. |

### C7 — Evidence / UI: separate workspace cache from model-context reuse, and gate it

| Field | Value |
| --- | --- |
| **Current owner** | `internal/execution/executor.go:3974 emitSnapshotActivity`; `internal/ui/model.go:2907/2934` |
| **Current behavior** | `globalActivityLog("[runtime] snapshot cache hit %s")` / `"[runtime] reading disk %s"` — ungated, reaches the normal viewport, and is a *filesystem* fact presented next to model-context reporting. |
| **Problem** | V7, and §10's explicit prohibition on conflating the two. |
| **Desired invariant** | Workspace-context facts and model-context facts are separately named, separately typed, and the filesystem ones are trace-only by default. |
| **Minimal repair** | (a) Rename/re-scope the executor emission to an unambiguous workspace-context label and route it through the **gated** detail channel (the same gate `logRuntimeDetail` uses), never the default activity channel. (b) Add the model-context facts the specification asks for — `ContextPrepared`/`ContextCompilation` already carry `UsedTokens`, `ContextTokens`, `Truncated`, `DropCount`, `PromptFingerprint`; surface *those* as the user-facing context line. |
| **Reused** | `events.NewContextPrepared`, `events.NewContextCompilation`, `m.logRuntimeDetail`, `m.setStage("context", …)`, the `presentation.Visibility` gate, the Alt+T trace overlay. |
| **New types** | none |
| **Tests** | (a) in `VisibilityNormal`, no `[runtime] snapshot cache hit` line appears; (b) in `VisibilityDebug` / trace overlay it does; (c) the user-facing context line reports model-context tokens, not filesystem hits. |
| **No competing authority** | Presentation only. No event is deleted — telemetry moves from the default surface to Trace, as §21/§23 require. |

### C8 — Model context: reuse the compiled prompt when the fingerprint is unchanged

| Field | Value |
| --- | --- |
| **Current owner** | `internal/contextcompiler.Compiler` (via `x.compileRequest`, `context_compiler.go:110`) |
| **Current behavior** | A fingerprint-keyed cache of `*CompiledContext` **already exists** (`compiler.go:324`, key at `:1235-1311` including every file's content). It works, but `CompiledContext.CacheHit` is never projected into `CompileResult` (`internal/observability/compile.go:15-45`) so nothing can see it. The cache is also wholesale-flushed at 64 entries rather than LRU (`:632-634`). |
| **Problem** | V8, and §10's requirement that "reused context" be a definable, testable state. |
| **Desired invariant** | Identical model context is not re-derived, and reuse is *reported* as its own state — separate from filesystem reuse. Every attempt still re-projects and re-bills the bytes the provider sees (provider-side caching is never assumed). |
| **Minimal repair** | (a) Add `CacheHit` to `observability.CompileResult` and populate it in `contextcompiler.Metrics()`, so the existing cache becomes observable through the event/telemetry chain that already exists. (b) Replace the wholesale flush with LRU eviction bounded by the same `cacheLimit`. (c) Surface the reuse fact in the `ContextPrepared`/`ContextCompilation` payload the executor already emits. |
| **Reused** | `Compiler.cache`, `Compiler.fingerprint`, `cloneCompiled`, `CompileResult`, `ContextPreparedPayload`, `ContextCompilationPayload`, `CompileResult.PromptFingerprint`. |
| **New types** | two additive fields; no new package |
| **Tests** | (a) unchanged file ⇒ identical fingerprint ⇒ one compile across two attempts and a reported reuse; (b) changed file ⇒ fingerprint changes ⇒ recompile and a reported miss; (c) eviction is bounded and does not drop live entries; (d) negative: a cache hit never bypasses `ContextSnapshot.Verify` or the current-state read. |
| **No competing authority** | Pure memo + telemetry inside the canonical context-compilation path. It authorizes nothing and never skips the admission snapshot. |

---

## 12. Explicitly rejected changes

| Rejected | Why |
| --- | --- |
| A new agent loop / ReAct loop / generic autonomous loop | §33; the Driver already owns the bounded loop and it is adequate once C3/C4 land. |
| A second executor or a second scheduler | §1; `TestSingleSchedulerType` and `TestPhase1_SingleProductionExecutionAuthority` already lock this. |
| Replacing the canonical `execution/planner` with the IR planner (or vice versa) | Both are wrong in isolation. `execution/planner` has the budget-correct, evidence-driven decomposition and the architecture lock; the IR planner has the framework adapters. Merge *responsibility* (C2), keep both components. |
| Removing the staged `/build` queue (V6) | Out of scope and risky. It is a real second lifecycle, but it shares the single executor and the single authorization owner. Record it; do not expand it. The correct long-term fix is to route it through the Driver, which is a separate phase. |
| Lowering `max_tokens` to make the observed run cheaper | Explicitly forbidden (§2, §33). It would make the truncation *more* likely. |
| Raising `max_tokens` to a large universal constant | §13 forbids a single universal limit, and it would spend unbounded model computation per attempt. C6 derives instead. |
| Aggressive context pruning / minimal-token context | §9 and §33. §8 above proves the reported 887-token input is already minimal for a single-file create. |
| Transcript compaction as the continuation mechanism | §33. `llmstep.ContinuationUserTurn` is state-based, not transcript-based. |
| A provider-specific or model-specific fix | §33. Every change is capability-derived through existing provider metadata. |
| A hard-coded special case for the portfolio scenario | §33. C1 fixes the *classification* rule; C3/C6 fix the *invocation* contract. Neither names HTML, CSS, or a portfolio. |
| Promoting `emitSnapshotActivity` to a richer model-context reporter inside `execution` | Would place presentation policy in the execution plane. C7 keeps the labelling in `execution` (evidence truth) and the *gating* in `internal/ui` (presentation). |

---

## 13. Test gaps

Existing coverage that must keep passing (do not regress):

| Concern | Existing test |
| --- | --- |
| `$prompt` mints `ScopeDynamic` | `internal/architecture/phase1_authorization_boundary_test.go:188` |
| Single production execution authority | `TestPhase1_SingleProductionExecutionAuthority` (Phase 7/8) |
| One `Scheduler` type; no `StepScheduler` | `internal/architecture/canonical_runtime_convergence_test.go:58` |
| Pure packages import nothing from the runtime | `:161`; `internal/architecture/continuation_invariants_test.go:16` |
| Driver holds no provider/filesystem authority | `internal/architecture/autonomous_runtime_invariants_test.go` |
| `finish_reason` normalization | `internal/execution/boundaries_conformance_test.go:237` |
| Guardrail semantics | `internal/execution/budget_guardrail.go` tests |
| `llmstep` bounded-step lifecycle | `internal/llmstep/step_test.go`, `response_state_test.go` |
| Truncation → BOUNDED_PATCH transition | `internal/runtime/autonomy/truncation_recovery_*_test.go` |
| Zero-trust conformance | `internal/runtime/autonomy/conformance_zero_trust_test.go` |
| Generic repository (no web coupling) | `internal/architecture/generality_boundary_test.go` |
| Intent compiler stages the canonical file set | `internal/modes/plan/intentcompiler_test.go`, `internal/ui/intentcompiler_cmd_test.go` |

Gaps this phase must close:

1. No test asserts that a **mutation verb + a design word** routes to BUILD.
2. No test asserts that the staged artifact set is **evidence-derived** rather than keyword-derived.
3. No test exercises **two truncated invocations followed by a complete one** on the mutation path.
4. No test asserts that a **partial artifact candidate is never written** without admission + authorization.
5. No test asserts that `continuation.DeriveNextStep` is consulted for `SubtypeOutputExhausted`.
6. No test asserts a **run-level token bound permits a legitimate multi-step continuation**.
7. No test asserts that the `create_file` budget is **derived** (monotonic in complexity/capability) rather than the literal 4 096.
8. No test asserts that filesystem cache lines are **absent** at `VisibilityNormal`.
9. No test asserts that identical model context is **not recompiled** across attempts, and that a changed file *is*.

---

## 14. Production-risk assessment

| Change | Risk | Mitigation |
| --- | --- | --- |
| C1 classification | **Medium** — a previously read-only prompt now routes to BUILD, so it can reach a mutation boundary. | `WorkspaceBuild` is still the only `CapMutate` domain; every mutation still requires `AuthorizationEngine.AuthorizeBuild` and the approval gate. Add negative tests proving read-only design phrasing is unchanged. |
| C2 planning provenance | **Medium** — a previously staged greenfield plan may now stage fewer tasks. | Make the reduction *explicit and explained* ("insufficient project evidence") rather than silent. Preserve `IsGreenfieldWebPrompt` as the applicability gate. |
| C3 bounded-step continuation | **High** — more invocations per task; must never mutate from partial output. | Capped by `llmstep.DefaultMaxContinuationSteps`; the candidate buffer is non-mutating by construction; the existing artifact gate and authorization run unchanged on the final result. Emit `StepExhausted` so exhaustion stays truthful. |
| C4 continuation wiring | **Medium** | `continuation` is pure and architecture-locked; the matrix keeps decision ownership. |
| C5 run-level budget | **Medium** — higher ceiling means more spend on a pathological loop. | Other bounds (`MaxAttempts`, `MaxIdenticalDecisions`, `MaxExecutionSteps`) still bound the loop; the increase is derived from capability, not a free-for-all. |
| C6 budget derivation | **Low** — larger `create_file` requests may cost more per call. | Clamped by the model ceiling; the fix converts *guaranteed* truncation into a *likely* completion, which is the intended efficiency gain. |
| C7 UI/telemetry | **Low** — informational only. | Telemetry is preserved and moves to Trace. |
| C8 model-context memo | **Medium** — a stale memo would be a correctness bug. | Key on `PromptFingerprint` **and** invalidate on any `invalidateSnapshot`; negative test asserts current-state validation is never bypassed. |

**Cross-cutting invariant that must be proven by architecture test, not by review:**

> Continuation must re-enter through the SAME `execution.RuntimeExecutor`, and a model
> invocation must never be able to expand authority, scope, capability, or mutation
> permission.

---

## 15. Change map

| Layer | Change |
| --- | --- |
| **Driver** | C4 (wire `continuation.DeriveNextStep` into `DecideRecovery` for exhaustion; extend `DriverContinuationInput` with durable step state) · C5 (run-level bound derived from capability + approved plan, via the existing `WidenBounds`) |
| **Planner** | C2 (canonical `execution/planner` owns the derived artifact set; enumerators propose, evidence admits) |
| **Context** | C8 (make the *existing* `contextcompiler` cache observable + bounded-LRU instead of wholesale-flush) · C2's discovery reuse (`WorkspaceInspector` facts actually consumed) |
| **RuntimeExecutor** | C3 (bounded-step continuation on the full-artifact contract, reusing `llmstep`) · C6 (derived `create_file` budget, clamped by existing capability chain) |
| **Evidence** | C7 (workspace-context vs. model-context facts named and separated; existing `Step*/Continuation*/State*` events reused for the new lifecycle) |
| **Verification** | unchanged — but now reached with a real artifact instead of a discarded prefix; no new verification authority |
| **Continuation** | C4 (dormant pure library activated); **never** a second scheduler — the verdict is mapped into the existing `LoopDecision` vocabulary |
| **UI/Presentation** | C7 (filesystem telemetry moves to Trace; model-context facts become the user-facing context line) |

---

## 16. Audit limitations

- **No live provider traffic was captured** (no credentials in this environment). The
  4096-token observation is taken from the reported run and corroborated statically by
  `outputForArtifact("create_file", …) = 4096` (`selector.go:661`).
- `contextcompiler.Compiler`'s internals were read for the cache audit; **[UNVERIFIED]**
  whether any provider adapter performs server-side prompt caching (irrelevant to the
  repairs, which never assume it).
- `internal/contextspec` (Phase 11.x Context Domain) is wired at
  `internal/ui/*handoffExecutionContext*` for the **freshness/hand-off** check; whether it
  participates in model-context construction is **[UNVERIFIED]** and is not required by
  any proposed change.
- Sub-process races were not re-run for this audit; the existing `-race` suite is the
  authority.
