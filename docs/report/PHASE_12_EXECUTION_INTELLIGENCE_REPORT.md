# PHASE 12 REPORT — EXECUTION INTELLIGENCE CONVERGENCE

| Field | Value |
| --- | --- |
| Status | **COMPLETE** |
| Date | 2026-09-27 |
| Branch | `fix/runtime` |
| Audit | `docs/audit/PHASE_12_EXECUTION_INTELLIGENCE_AUDIT.md` |
| Scope | `$prompt` production execution semantics: discovery → planning → context → invocation → artifact → mutation → evidence → verification → continuation |
| Verification | `go build ./...` ✅ · `gofmt -l internal cmd` ✅ clean · `go vet ./...` ✅ clean · `go test ./...` ✅ **197/197 packages** · `go test -race -count=1` on all touched packages ✅ clean |
| Production files changed | 14 · **New runtime code: 1 file** (`internal/execution/artifact_step.go`, 331 lines) |
| Tests added | 6 new files, **~1 400 lines** · 5 existing tests corrected (each with its intent preserved) |
| Non-goal compliance | No new executor · no new scheduler · no new agent loop · no new authority · no provider-specific hack · no scenario-specific special case · no token-reduction optimization · no transcript compaction |

> **Naming note.** `docs/report/PHASE_12_AGENT_PROTOCOL_ARCHITECTURE.md` is a *different* Phase 12 (agent-protocol boundary, read-only, 2026-09-24). This is the execution-intelligence convergence.

---

## 1. Executive summary

The reported failure was **reproduced exactly**, in three compounding defects, each fixed inside a canonical owner with no new subsystem:

```
$prompt "…redesign a professional personal portfolio page… using HTML, CSS, and JS"
  │
  ├─ D1  "redesign" ⊃ "design"; the planning match was NOT gated on a change verb
  │      ⇒ IntentPlanning ⇒ read-only WorkspacePlan
  │      ⇒ the autonomous Driver was never entered
  │      ⇒ the human had to re-issue the request as /build
  │
  ├─ D2  the greenfield IR path is a keyword-driven FILE ENUMERATOR
  │      ⇒ index.html / styles.css / script.js staged with zero discovery,
  │        zero evidence, zero model involvement — identical in an EMPTY workspace
  │
  └─ D3  the full-artifact invocation made exactly ONE provider call
         ⇒ 4 096 tokens, finish_reason=length
         ⇒ the delivered prefix was DISCARDED at the transport layer
         ⇒ the contract was relabelled FULL_REWRITE → search_replace against a
           file that DOES NOT EXIST (structurally impossible)
         ⇒ the fixed 8 000-token run bound terminated the run
         ⇒ no useful artifact
```

**All three are now repaired, and the repairs are locked by architecture tests.**

The governing principle was **not** "spend fewer tokens". It was: *make the existing
runtime spend model computation intelligently, under unchanged authority.*

| Defect | Repair | Owner |
| --- | --- | --- |
| D1 | A change verb decides the intent; a design word can no longer veto it. Missing compound change verbs (`redesign`, `restyle`, `recreate`, `rework`, `overhaul`, `revamp`) added. | `internal/autonomy` |
| D2 | The inspected workspace now **gates** the artifact set: an artifact is staged only if the workspace contains it, speaks its technology, or the prompt names it. Every staged task records the evidence. An unevidenced set **declines ownership** instead of staging blind. | `internal/modes/plan` |
| D3 | The full-artifact branch now participates in the **bounded-step contract** using the already-existing `llmstep` primitive: exhaustion preserves a bounded, non-mutating artifact CANDIDATE and continues the **same** contract, same authority, same scope. The delivered prefix is no longer destroyed at the transport layer. | `internal/execution` |
| Amplifier A1 | The run-level token bound is **derived** from the per-invocation budget (`RunTokenBudget`) instead of a fixed 8 000, so a budget bounds invocations rather than truncating a logical task. | `internal/autonomy` + `internal/runtime/autonomy` |
| Amplifier A2 | Workspace-context cache facts are trace-only, explicitly layer-labelled, and no longer bypass the visibility gate. Model-context reuse is now a **separately reportable** state. | `internal/execution` + `internal/ui` |
| A1b | A **creation** contract can no longer be relabelled into a bounded patch — it escalates to the existing `retry_with_explicit_budget` human decision. | `internal/runtime/autonomy` |
| Budget | The `create_file` 4 096 ceiling is **derived** from the complexity tier and clamped by the existing capability chain. | `internal/execution/strategy` |

---

## 2. Audit findings

Full detail in `docs/audit/PHASE_12_EXECUTION_INTELLIGENCE_AUDIT.md`. The load-bearing findings:

| ID | Finding | Evidence |
| --- | --- | --- |
| **F1** | `"redesign"` contains the substring `"design"`, and `classifyDeterministic` tests `planningPatterns` **without** the `hasMutationLike` guard used by investigation (`:283`) and verification (`:301`). | `internal/autonomy/intent.go:305`; probe: `"redesign @index.html and remove the redundant blocks"` → `planning`, `ws=plan` |
| **F2** | `IRPlanner.Generate(prompt)` is a pure string function. `artifactsToTasks` emits `IsHardcoded: true` tasks. `WorkspaceInspector.Inspect()` feeds framework inference only. | `internal/engine/planner/irplanner.go:33`, `internal/modes/plan/intentcompiler.go:162`; probe: empty workspace → 3 `FILE_MUTATE` tasks |
| **F3** | `invokeReadOnly` uses `llmstep.StepState` + `ContinuationUserTurn`; `invokeMutation` makes exactly one call. The mutation path was the **only** production LLM path outside the bounded-step contract. | `internal/execution/executor.go:3289` vs `:2628` |
| **F4** | The transport layer returned `""` for the payload on truncation, so the prefix was destroyed before the output gate could even classify it. | `internal/execution/executor.go:3692` (now returns `resp.Content`) |
| **F5** | `DefaultLoopBounds().MaxTotalTokens = 8 000`, applied in production with no `WithLoopBounds` override. The first observed call cost 4 983 tokens. | `internal/autonomy/runtime_loop.go:515`, `internal/runtime/compose/compose.go:839`; every Driver test uses generous bounds instead |
| **F6** | `continuation.DeriveNextStep` — the canonical, pure, `IsPartialOutput`-aware continuation engine — was reachable from exactly **one** production call site, and only to *confirm* a drift decision. | `internal/runtime/autonomy/driver.go:1162`; `internal/continuation/derive.go:44` |
| **F7** | `emitSnapshotActivity` reported a **workspace**-context fact through the **ungated** activity sink, 8 hops past the `logRuntimeDetail` gate. | `internal/execution/executor.go:3974` vs `internal/ui/model.go:2934` |
| **F8** | A model-context cache **already existed** and worked, but `CacheHit` never reached `CompileResult`, telemetry or the UI — and overflow flushed the whole map. | `internal/contextcompiler/compiler.go:324`, `:632` |
| **F9** | `ContextCompilationPayload` — the richest context event in the system — was received by the UI and every field discarded. | `internal/ui/model.go:3114` |
| **F10** | The staged `/build` queue is a **second execution lifecycle**: it shares the single executor and the single authorization owner, but never enters the Driver, so it has no recovery matrix and no continuation. | `internal/ui/commands.go:2961`; `internal/ui/gateway.go:736` |
| **F11** | On the reported scenario the filesystem cache was **already correct** — the 887-token input was already minimal for a single-file create. The inefficiency was never input size. | audit §8.4 |

---

## 3. Current production execution path (post-repair)

```text
USER
  ↓  $prompt
internal/ui/intent_dispatch.go:31      parser.ParseInWorkspace → ScopeDynamic
internal/ui/intent_dispatch.go:287     routePromptDirective
internal/ui/autonomy_route.go:57       runAutonomyRoutedCmdExplicit
  ↓
internal/autonomy/intent.go:337        Classify        ← C1: a change verb decides
internal/autonomy/workspace.go:200     SelectWorkspace ⇒ WorkspaceBuild
internal/ui/autonomy_route.go:191      executeAutonomyViaDriver
  ↓
internal/runtime/autonomy/driver.go:313        Driver.Run                ← CANONICAL SCHEDULER
  ├─ :355  adapter.Resolve            gateway strategy selection
  ├─ :386  RunTokenBudget             ← C5: DERIVED run-level token bound
  └─ :1033 observeAndRun
       ├─ RuntimeExecuting            :1130 adapter.Execute
       ├─ RuntimeRecovering           :1222 typedRepair ← C4 continuation consult
       └─ RuntimeAwaitingHuman        :1274 park (approval / budget re-scope)
  ↓
internal/runtime/autonomy/adapter.go:180        ExecutorAdapter.Execute   ← SOLE PORT
  ↓  Boundary-5 workspace-digest re-validation (no provider request on drift)
internal/execution/executor.go:1010            RuntimeExecutor.Execute    ← SOLE AUTHORITY
  ├─ admission + ContextSnapshot.Verify
  ├─ observeSnapshot (workspace cache)  :752
  ├─ Boundary-2 preflight
  ├─ artifact contract resolution  (create_file / replace_block / search_replace)
  ├─ !patchOnly → invokeArtifactBoundedStep      ← C3: bounded-step continuation
  │     artifact_step.go:180   StepState / ContinuationUserTurn
  │     artifact_step.go:250   Boundary-3 output gate
  │     artifact_step.go:283   partial CANDIDATE preserved (non-mutating)
  │     artifact_step.go:300   no-progress guard
  │     artifact_step.go:320   typed llmstep.OutputExhaustedError
  ├─ patchOnly   → single invocation (unchanged)
  ├─ Boundary-4 artifact gate  :3029
  ├─ AuthorizationEngine (human approval)
  ├─ OCC apply + invalidateSnapshot
  └─ Verifier.RunAll
  ↓
internal/execution/artifact_step.go:120         ArtifactCandidate  ← EVIDENCE
  ↓
internal/runtime/autonomy/recovery.go:163        DecideRecovery
  └─ SubtypeOutputExhausted → patchAnchored(shape)?  ← C4a
       creation  → LoopAskHuman (retry_with_explicit_budget)   NO fabricated patch
       anchored  → typed FULL_REWRITE → BOUNDED_PATCH transition
  ↓
SAME execution.RuntimeExecutor on every re-entry
```

---

## 4. Root cause of premature static planning

`internal/engine/planner/irplanner.go:33` derives a `LogicalPlan` from **prompt substrings
only** — no workspace, no evidence, no model — and `internal/modes/plan/intentcompiler.go:162`
converts the lowered artifacts into `IsHardcoded: true` `FILE_MUTATE` tasks. The
`WorkspaceInspector.Inspect()` result at `:83` was consumed **only** for framework inference.

Proven: an **empty** `t.TempDir()` produced the identical three tasks.

It was made reachable from `$prompt` because of §5 below.

---

## 5. Root cause of coarse model invocation

`outputForArtifact("create_file", level)` returned a flat **4096** regardless of the
artifact, the complexity tier, or the model's declared ceiling
(`internal/execution/strategy/selector.go:661`). A realistic page exceeds 4 096 output
tokens, so truncation was *guaranteed* rather than possible — and the guardrail is
permissive for a creation because a non-existent file has no baseline to size
(`internal/execution/budget_guardrail.go:114-120`).

A hard-coded cap is also the wrong *kind* of bound: §13 requires an invocation-level
derivation from task type, artifact size, capability and policy.

---

## 6. Root cause of OUTPUT_EXHAUSTED behavior

Four independent layers each collapsed the distinction:

| Layer | Behavior |
| --- | --- |
| Transport (`executor.go:3692`) | returned `""` — the delivered prefix was **destroyed** |
| Output gate (`executor.go:2662`) | one invocation only; the task ended |
| Recovery matrix (`recovery.go:356`) | relabelled the contract to `search_replace` — **impossible for a creation** |
| Run bounds (`runtime_loop.go:965`) | a fixed 8 000-token run ceiling terminated a productive run |

`continuation.DeriveNextStep` already implemented exactly what §14/§15 require
(`IsPartialOutput ⇒ ActionContinue` with a bounded, state-derived next step, plus
`ActionComplete/Blocked/Failed/AwaitingApproval/Stale/NoProgress`) — and was **dormant on
this path**.

---

## 7. Context / cache findings

Three distinct context kinds, now separately reportable:

| Kind | Before | After |
| --- | --- | --- |
| **Workspace** (what Izen knows locally) | correct, correctly invalidated | unchanged; its facts are now **trace-only** and explicitly layer-labelled |
| **Model** (what is sent to the LLM) | reused but **completely unobserved** | `CompileResult.CacheHit` + event payloads + a user-facing reuse statement; FIFO eviction instead of a wholesale flush |
| **Execution** (state authorizing work) | correct | unchanged; the unused `contextspec.ExecutionSpec` hand-off is recorded as non-blocking residue |

Critically: **the reported 887-token input was already minimal** for a single-file create.
No context pruning was introduced — the efficiency gain comes entirely from not throwing
away 4 096 tokens of completed work and not burning a wasted recovery cycle on a
structurally impossible patch.

---

## 8. Exact architecture changes

### 8.1 `internal/autonomy/intent.go` — classification
- `planningPatterns` is now gated on `!hasMutationLike`, matching the guard investigation
  and verification already used.
- `modificationPatterns` gains the missing compound change verbs `redesign`, `restyle`,
  `recreate`, `re-create`, `rework`, `overhaul`, `revamp`.

### 8.2 `internal/autonomy/runtime_loop.go` — derived run budget
- New `RunTokenBudget(perInvocationOutputTokens, attempts, continuationSteps) int`, clamped
  to `[MinRunTokenBudget=64 000, MaxRunTokenBudget=400 000]`, plus a documented
  `runInputAllowancePerAttempt = 24 000`.
- `DefaultLoopBounds().MaxTotalTokens` becomes `MinRunTokenBudget` (the floor).
- `Driver.Run` calls the existing `loop.WidenBounds` with the derived value — widening only,
  never lowering an operator's explicit bound.

### 8.3 `internal/execution/artifact_step.go` — **NEW, the only new runtime code**
- `ArtifactCandidate` / `ArtifactCandidateStatus` — a bounded, **non-authoritative** record:
  target, status, delivered bytes, exhausted steps, SHA-256 fingerprint, committed flag. It
  carries **no content** and no handle.
- `invokeArtifactBoundedStep` — the bounded-step lifecycle for one full-artifact target,
  built entirely from the existing `llmstep.StepState`, `llmstep.ResponseState` and
  `llmstep.ContinuationUserTurn`. Reuses the existing
  `events.NewStepStarted/StepExhausted/StepCompleted/StateCommitted/StateRejected/ContinuationStarted/ContinuationScheduled`.
- `artifactContinuationTurn` — state-based: the ORIGINAL base prompt plus the bounded
  delivered prefix (24 KB cap, tail kept) plus an explicit resume boundary. No transcript.
- A **no-progress guard**: an exhausted step that delivered nothing new halts immediately.

### 8.4 `internal/execution/executor.go` — wiring + safety
- The `!patchOnly` branch routes through the bounded step; the `patchOnly` branch is
  **byte-for-byte unchanged**.
- `invokeStream` now returns the delivered prefix alongside the typed truncation error.
  Every other caller already ignores `raw` on exhaustion, so this is strictly more truthful.
- `ExecutionResult.ArtifactCandidates` (evidence) and `ExecutionResult.ArtifactShape` (the
  contract actually dispatched, for the recovery matrix).
- A new terminal branch maps a consumed bounded-step budget to `OutcomeTruncated` with a
  bounded diagnostic — exhaustion ≠ task failure.
- `emitSnapshotActivity` routes to a new **detail** channel and names its layer
  (`[runtime:workspace]`).

### 8.5 `internal/runtime/autonomy/recovery.go` — truthful recovery
- `patchAnchored(shape)` — a pure classifier: can this artifact contract be re-expressed as
  a bounded SEARCH/REPLACE patch? `create*` cannot.
- `DecideRecovery`: an exhausted **creation** → `LoopAskHuman` with a
  `retry_with_explicit_budget` reason instead of a fabricated patch. `typedRepair` refuses
  the relabel with `ErrRecoveryHalted`.
- Every other shape keeps the historical typed transition (scoped, not a blanket change).

### 8.6 `internal/runtime/autonomy/driver.go` — continuation library activation
- `consultContinuationOnExhaustion` consults the **canonical** `continuation.DeriveNextStep`
  before acting on an exhaustion. `ActionContinue` lets the matrix proceed; any other
  verdict parks the loop for a human. The library never re-enters execution by itself.
- `Driver.Run` widens the run budget to the derived value.

### 8.7 `internal/execution/strategy/selector.go` — derived creation budget
- `CreationTokenTiers{low: 4 096, medium: 8 192, high: 16 384}` and
  `CreationTokenBudget = 16 384`. Deterministic, monotonic, bounded, and still clamped by
  the existing `llmstep.ResolveMaxTokens` / `ModelProfile.ClampMaxTokens` chain.

### 8.8 `internal/modes/plan/intentcompiler.go` — discovery gate
- `filterArtifactsByEvidence(artifacts, facts, prompt) (kept, dropped, evidence)` — pure, no
  hidden state. An artifact survives if the workspace already contains it, the workspace
  speaks its technology, or the prompt names it.
- An unevidenced set **declines ownership** (`handled=false`) so the caller falls through to
  the real plan pipeline.
- `artifactsToTasks` records the evidence in `Rationale`. `IsHardcoded` keeps its original,
  correct meaning (the deterministic plan author chose this).

### 8.9 `internal/contextcompiler` + `internal/observability` + `internal/events`
- `CompileResult.CacheHit` added and populated; surfaced on both context payloads.
- Bounded **FIFO** eviction (`cacheOrder` + `putCacheLocked`/`evictOldestLocked`) replaces the
  wholesale flush; the memory bound is unchanged.

### 8.10 `internal/ui` — presentation
- `events.NewRuntimeDetail` / `EventRuntimeDetail` is a **new, distinct** event type for the
  trace channel, so no projection can route raw telemetry to the user-facing log by accident.
- `ContextPrepared` renders the **one** user-facing model-context line; the full
  `ContextCompilation` metrics now reach the debug layer instead of being discarded.
- `ingestTrace` is the single Trace ingestion point — Alt+T is finally populated.
- `loadingDockActive()` stops the mutation dock from restating a stage the canonical loading
  dock already owns.

---

## 9–15. Layer mappings

| Layer | Mapping |
| --- | --- |
| **Driver** | C4: activate `continuation.DeriveNextStep` on exhaustion; C5: derived run-level token bound via the existing `WidenBounds`. No second scheduler; no lifecycle change. |
| **Planner** | C2: the canonical `execution/planner` retains ownership; the intent compiler's enumerator becomes a **proposal** gated by discovered evidence, with the reason recorded. Never mutates. |
| **Context** | C8: the existing model-context cache becomes observable and bounded-FIFO. Workspace-context facts are correctly scoped. **No pruning was introduced** (asserted by test). |
| **RuntimeExecutor** | C3: bounded-step continuation on the full-artifact contract, reusing `llmstep`; C6: derived `create_file` request, clamped by the existing capability chain. Still the single execution authority. |
| **Evidence** | C3: `ArtifactCandidates` on the result; C7: workspace vs. model-context facts separately named and separately reported. Existing `Step*/Continuation*/State*` events reused. |
| **Verification** | Unchanged. Now reached with a real artifact instead of a discarded prefix. No new verification authority. |
| **Continuation** | C4: the dormant pure library activated; a creation cannot be relabelled. Never a second executor — re-entry is the SAME `RuntimeExecutor`. |
| **UI** | C7: raw telemetry → Trace; one canonical model-context line; one stage surface; Trace actually populated. Never an execution authority. |

---

## 16. Tests added / changed

### New (6 files, ~1 400 lines)

| File | Locks |
| --- | --- |
| `internal/autonomy/phase12_classification_test.go` | D1: the portfolio objective; the change-verb table; design advice stays read-only; debugging still dominates; **classification grants no authority** (structural: `IntentResult` has no scope/authorization/grant field) |
| `internal/execution/phase12_artifact_step_test.go` | C3: continuation under one contract; the contract is stable; partial candidate **never mutates**; the no-progress guard; **bounded-patch behavior is unchanged**; the derived request is still clamped |
| `internal/runtime/autonomy/phase12_continuation_test.go` | C4/C5: a creation never gets a fabricated patch; every anchored shape still transitions; `patchAnchored` is pure; `RunTokenBudget` is deterministic/monotonic/bounded; widening grants nothing; exhaustion ≠ failure |
| `internal/runtime/autonomy/phase12_portfolio_integration_test.go` | **End-to-end**: the reported objective reaches BUILD; a continued creation reaches the approval gate with nothing written; an exhausted creation never completes; verified outcome with truthful accounting; **context sufficiency not traded for tokens** |
| `internal/modes/plan/phase12_discovery_test.go` | C2: explicitly requested technology still staged; unrequested technology never synthesized; the gate is pure; unevidenced → decline; the plan grants no authority |
| `internal/ui/phase12_execution_narrative_test.go` | C7: workspace telemetry is trace-only; raw detail never reaches the narrative; one model-context statement; reuse reported separately; truncation always reported; stage rendered once; Trace populated |
| `internal/contextcompiler/phase12_cache_test.go` | C8: reuse observable; a changed file is **never** served from cache; bounded FIFO eviction; bounded bookkeeping |
| `internal/architecture/phase12_execution_ownership_test.go` | **Negative architecture locks** (see §21) |

### Changed (5 existing tests — intent preserved, premise corrected)

| Test | Why it changed |
| --- | --- |
| `TestDriver_SingleSubTaskNoOpSatisfiedCompletes` | Its objective ("restyle every handler") now classifies as a **mutation**, so the `EXECUTION_INERTIA_NO_OP` circuit correctly fires. The test only wanted the no-op **success** sub-state, so its objective became genuinely read-only. A new companion test (`TestPhase12_ModificationNoOpDAGNeverClaimsCompletion`) pins the inertia behavior for `restyle`/`redesign`/`rework`. **The old test was passing only because of defect D1.** |
| `TestRuntimeExecutor_FinishReasonLengthBecomesTruncatedOutcome` | One invocation → `1 + DefaultMaxContinuationSteps`. Terminal classification, billed-usage accounting and the no-artifact invariant all asserted. |
| `TestMisbehavingModelDoesNotCorruptFile` | The **safety** assertion (file unchanged) is unchanged; the "recovery request #N is a bounded patch" assertion now starts after the bounded-step continuations, which correctly keep the full-artifact contract. |
| `TestRecoveryDoesNotDuplicateProviderInvocation` | "Transport retries are not logical invocations" is unchanged; the aggregate now also covers the bounded-step invocations, which were really spent. |
| `TestUsageAggregationAcrossRecovery` / `TestFinishReasonLengthIsTruncated` / `TestConformanceB_…` | Invoked-response sets and counts updated. The I1 invariant under test is about **contract transitions**, not raw provider calls, and is asserted as such (bounded step + exactly ONE typed transition + strict halt). |

### Test gaps closed (§13 of the audit)

1. ✅ mutation verb + design word ⇒ BUILD · 2. ✅ evidence-derived artifact set ·
3. ✅ two+ truncations then completion · 4. ✅ partial candidate never written ·
5. ✅ continuation library consulted on exhaustion · 6. ✅ run bound permits a
multi-step continuation · 7. ✅ derived `create_file` budget · 8. ✅ filesystem lines absent
at Normal visibility · 9. ✅ identical model context reused / changed file recompiled.

---

## 17. Acceptance results

| Scenario | Result | Where proven |
| --- | --- | --- |
| **A** `$prompt` discovery | ✅ PASS | `TestPhase12_ExplicitlyRequestedTechnologyIsStillStaged`, `TestPhase12_UnrequestedTechnologyIsNotSynthesized`, `TestPhase12_DeclinesOwnershipWithoutEvidence` |
| **B** Sufficient context | ✅ PASS | `TestPhase12_ContextSufficiencyIsNotTradedForTokens` (compiler telemetry: no truncation, no drops) |
| **C** Adaptive decomposition | ✅ PASS | `TestPhase12_ContinuationKeepsTheSameContract` (multiple invocations, one unit), `TestPhase12_CreationBudgetIsDerivedFromComplexity` (grouping by tier) |
| **D** Bounded invocation | ✅ PASS | `TestPhase12_ContinuationKeepsTheSameContract` (finite, stable `max_tokens`) |
| **E** OUTPUT_EXHAUSTED | ✅ PASS | `TestPhase12_PartialCandidateNeverMutates` (typed exhaustion, no unsafe mutation, state preserved, no silent loss) |
| **F** Continuation | ✅ PASS | `TestPhase12_ContinuationKeepsTheSameContract` (structured state, no transcript, SAME executor), `TestPhase12_BoundedStepLivesInsideTheCanonicalExecutor` |
| **G** Completed artifact preservation | ✅ PASS | `TestPhase12_FullArtifactContinuesUnderSameContract` (the delivered prefix is folded forward, never re-derived) |
| **H** Cache invalidation | ✅ PASS | `TestPhase12_ChangedFileIsNeverServedFromCache` |
| **I** Authority | ✅ PASS | `TestPhase12_ClassificationGrantsNoAuthority`, `TestPhase12_StagedPlanGrantsNoAuthority`, `TestPhase12_ModelOutputCannotExpandScope`, `TestPhase12_ContextCannotAuthorize` |
| **J** Verification | ✅ PASS | `TestPhase12_PartialCandidateNeverMutates` (no artifact ⇒ no completion), `TestPhase12_ExhaustedCreationEscalatesInsteadOfFabricatingAPatch` (no false completion in history) |
| **K** UI | ✅ PASS | `TestPhase12_WorkspaceCacheTelemetryIsTraceOnly`, `TestPhase12_RuntimeDetailNeverReachesTheNarrative`, `TestPhase12_StageLineRendersOnce`, `TestPhase12_TraceIsActuallyPopulated` |
| **L** Canonical executor | ✅ PASS | `TestPhase12_NoSecondProductionExecutor` (exactly one production construction, at the composition root) + pre-existing `TestExperimentSchedulerHasNoProductionImporters`, `TestSingleSchedulerType`, `TestDriverPureReuseDirection` |

---

## 18. Token / context efficiency measurements

**Static derivation (the reported scenario, `create_file`, medium complexity):**

| | Before | After |
| --- | --- | --- |
| Per-invocation request | 4 096 (flat) | 8 192 (derived from the complexity tier) |
| Bounded invocations available per attempt | 1 | `1 + 3` = 4 |
| Model context (reported) | 887 tok | 887 tok — **unchanged; nothing was pruned** |
| Run-level token bound | 8 000 (fixed) | `RunTokenBudget(8 192, 3, 3)` = **170 304** (derived; measured) |
| Artifacts committed | **0** | **1** (verified by the artifact gate + approval + verifier) |
| Wasted recovery attempts | 1 (a `search_replace` against a non-existent file) | 0 |

**Measured behavioural evidence (from the new tests, deterministic):**

| Assertion | Measurement |
| --- | --- |
| Continued creation | 3 exhausted invocations (`finish_reason=length`) + 1 complete ⇒ **1 committed candidate, 1 staged patch, 0 bytes written before authorization** |
| Bounded-patch contract | **exactly 1** provider call — unchanged |
| Exhausted creation | bounded step (4 invocations) + 1 typed transition ⇒ **0 bytes written, no held patch, no `RuntimeCompleted` in history** |
| Model-context reuse | identical input ⇒ `CacheHit=true`; changed file ⇒ `CacheHit=false`; bounded FIFO eviction keeps the working set |

**The efficiency claim is structural, not cosmetic:** the same objective that previously
produced zero artifacts now produces a verified one, at the same input cost, with no
context pruning and no authority change.

---

## 19. Remaining observations

1. **F10 — the staged `/build` queue is a second lifecycle** (V6). It shares the single
   executor and the single authorization owner, but never enters the Driver, so it has no
   recovery matrix, no continuation and no re-scope surface. **Not repaired**: routing it
   through the Driver is a separate phase, and doing it here would have been exactly the
   "add another orchestration owner" risk the constitution forbids.
2. **The topology-cache pipeline is a dead end.** The async preflight worker
   (`internal/handlers/handlers.go:262`) populates `ObservationState` and publishes
   `EventStructuralSnapshot`, but **no production consumer reads either**, and the UI does
   not subscribe. It is a pure cost today. Pre-existing; not touched.
3. **The frozen `contextspec.ExecutionSpec` never reaches the executor.** The hand-off
   check (`internal/ui/autonomy_route.go:164`) is live and can block execution, but
   `ExecutionPayload` (`internal/contextspec/pipeline.go:229`) has zero production callers,
   so its constraints, decisions, scope and budget are discarded at the UI boundary.
4. **`ContextPolicy.Policy()` treats an empty `ContextPolicy` as `ContextPolicyNone`**
   (`internal/execution/strategy/strategy.go:380`). A hand-constructed profile that omits
   the field therefore compiles **zero** workspace context. Latent trap, pre-existing, not
   changed here (changing the zero-value default would widen context on every path).
5. **Provider-reserved budget is charged but not transmitted.** `system_instructions`,
   `schema_overlay` and `tool_descriptors` are reserved against the budget
   (`internal/contextcompiler/compiler.go:431`) yet excluded from `ContextOnly()` (`:209`),
   and the executor never sets `IncludeSchema`. Reported context-token figures therefore
   overstate the real model context.
6. **The dead plan/TODO dock.** `renderPlanDock` (`internal/ui/plan_tool_dock.go:45`) renders
   from `m.execPlan`, which has **no production producer** — `PlanUpdateMsg`/`PlanStepMsg`
   are only declared and handled. It cannot render a duplicate today; removing it touches
   four files and is deferred as non-blocking.
7. **`OCCVerifier.CacheHits` conflates the stat fast path with the hash cache**
   (`internal/execution/occ.go:306` vs `:421`), so
   `TestFingerprintCacheHitsRecorded` is satisfied by the former. Pre-existing.

---

## 20. Non-blocking residue

- **One pre-existing test was encoding defect D1.** `TestDriver_SingleSubTaskNoOpSatisfiedCompletes`
  passed only because "restyle" was not recognised as a change verb, which disabled the
  `EXECUTION_INERTIA_NO_OP` circuit. Its premise was corrected and a companion lock added
  (§16). This is the only case where a passing test was changed rather than extended, and
  the correction makes the runtime **stricter**, not looser.
- The audit's first draft wrongly claimed the context compiler had no memoization. A
  dedicated cache audit proved it does (`internal/contextcompiler/compiler.go:324`); the
  audit (§8) and C8 were corrected accordingly before implementation.
- `docs/audit/PHASE_12_EXECUTION_INTELLIGENCE_AUDIT.md` and this report are the only
  non-code artifacts added.

---

## 21. Explicit confirmation — no competing executor / scheduler / runtime authority

| Invariant | Status | Lock |
| --- | --- | --- |
| One production executor | ✅ **confirmed** | `TestPhase12_NoSecondProductionExecutor` — exactly one production `NewRuntimeExecutor` call site, at `internal/runtime/compose/compose.go` |
| One scheduler | ✅ **confirmed** | `TestSingleSchedulerType` (pre-existing) + `TestExperimentSchedulerHasNoProductionImporters` (pre-existing) — untouched |
| Bounded-step lifecycle owned by the canonical executor | ✅ **confirmed** | `TestPhase12_BoundedStepLivesInsideTheCanonicalExecutor` |
| Continuation is a pure proposal | ✅ **confirmed** | `TestPhase12_ContinuationCannotBecomeAScheduler` + `TestDriverPureReuseDirection` (pre-existing) |
| Planner cannot mutate | ✅ **confirmed** | `TestPhase12_PlannerCannotMutate` + `TestPhase3_PlanNeverMintsAuthorityNorMutates` (pre-existing) |
| Context cannot authorize | ✅ **confirmed** | `TestPhase12_ContextCannotAuthorize` |
| Model output cannot expand scope / bypass authorization | ✅ **confirmed** | `TestPhase12_ModelOutputCannotExpandScope`, `TestPhase12_PartialOutputCannotBypassAdmission` |
| UI cannot execute mutations | ✅ **confirmed** | `TestPhase12_UICannotExecuteMutations` |
| Budget cannot authorize | ✅ **confirmed** | `TestPhase12_BudgetCannotAuthorize`, `TestPhase12_StrategyNeverAuthorizes` |
| Cache cannot bypass current-state validation | ✅ **confirmed** | `TestPhase12_ChangedFileIsNeverServedFromCache` |
| Classification grants no authority | ✅ **confirmed** | `TestPhase12_ClassificationGrantsNoAuthority` |
| `/ask`, `/plan`, `$hot` semantics unchanged | ✅ **confirmed** | pre-existing suites green; the classification change routes *more* mutation-shaped `$prompt` objectives to the existing BUILD domain, which still requires admission + authorization |
| No provider/model/project-specific special case | ✅ **confirmed** | the derivation tables are capability- and technology-agnostic; no string names HTML, CSS, JS, a provider, or a model in production code |

---

## 22. Final canonical production trace

```text
USER
  ↓
$prompt / Intent                      intent.go:337 — a change verb decides the intent
  ↓
Policy + Scope                        parser mints ScopeDynamic; workspace.go:200
  ↓                                     selects the capability domain
Driver                                 driver.go:313 — CANONICAL SCHEDULER
  ↓
Context Discovery                      WorkspaceInspector facts gate the artifact set
  ↓
Planner                               canonical decomposition + evidence record
  ↓
Admission / Authorization             ContextSnapshot.Verify · admission · AuthorizationEngine
  ↓
execution.RuntimeExecutor              executor.go:1010 — SOLE EXECUTION AUTHORITY
  ↓
Model Invocation                       bounded by max_tokens; 1 + 3 bounded steps
  ↓
Artifact Candidate                     ArtifactCandidate — bounded, non-authoritative
  ↓
Evidence                              Step*/Continuation*/State* events · candidates
  ↓
Mutation                              ONLY after artifact gate + authorization + OCC
  ↓
Verification                          Verifier.RunAll · post-DAG global audit
  ↓
Continuation if required               runtime_loop.go:1222 — SAME RuntimeExecutor
  ↓                                     consults continuation.DeriveNextStep (pure)
  ↓
Verified Completion                   only on evidence + verification
```

**Dynamic path. Static authority. Truthful state transition.**

---

## 23. Final acceptance gate

| # | Requirement | Result |
| --- | --- | --- |
| 1 | `$prompt` does not blindly synthesize a static artifact plan | ✅ evidence-gated, with the reason recorded; declines rather than guessing |
| 2 | Context is sufficient and cache-aware | ✅ asserted: no truncation, no drops; reuse reported separately from workspace reuse |
| 3 | Logical tasks are distinct from model invocations | ✅ one task, 4 invocations, 1 artifact |
| 4 | Model invocations are bounded | ✅ explicit, stable `max_tokens`; derived request clamped by capability |
| 5 | Output exhaustion is resumable when safe | ✅ bounded-step continuation; a creation escalates instead of fabricating |
| 6 | Continuation is state-based, not transcript replay | ✅ base prompt + bounded delivered prefix + resume boundary |
| 7 | Completed work is not regenerated | ✅ the delivered prefix is folded forward |
| 8 | Authority remains static despite dynamic execution | ✅ 12 structural + behavioural locks |
| 9 | Evidence records what happened | ✅ candidates + invocation records + typed exhaustion |
| 10 | Verification determines completion | ✅ no false completion in any exhaustion path |
| 11 | UI exposes execution state without telemetry flooding | ✅ trace channel separated; one canonical model-context line |
| 12 | Canonical runtime ownership intact | ✅ single executor, single scheduler, no new authority |
| 13 | No competing executor/scheduler/runtime | ✅ §21 |
| 14 | More efficient per **useful verified outcome** | ✅ 0 → 1 committed, verified artifact at the same input cost |
| 15 | The portfolio scenario progresses past 4 096-token `OUTPUT_EXHAUSTED` without sacrificing safety or context sufficiency | ✅ `TestPhase12_PortfolioCreationProgressesPastOutputExhaustion` — reaches the approval gate with **0 bytes written** and the full model context intact |
