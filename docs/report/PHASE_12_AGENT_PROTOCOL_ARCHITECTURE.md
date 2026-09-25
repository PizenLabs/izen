# PHASE 12 — IZEN AGENT PROTOCOL ARCHITECTURE & PROVIDER BOUNDARY

| Field | Value |
| --- | --- |
| Status | COMPLETE — Architecture & Audit (read-only; no production code changed) |
| Version | 1.0 |
| Date | 2026-09-24 |
| Branch | `feature/abstraction` |
| Scope | Agent-protocol boundary, provider adapters, execution-contract selection, context architecture, and the runtime signal/state-truth addendum |
| Method | Graph-indexed reconnaissance (`codebase-memory-mcp` project `izen`, 31 423 nodes / 178 680 edges) + direct source reads; evidence tier **Verify (Tier 2)** |
| Verification | `go build ./...` ✅ · `go test ./internal/httpx ./internal/provider/registry` ✅ · `go test ./internal/providers -run 'Eligib\|Ineligible\|Compatib\|Harness\|403\|Forbidden'` ✅ · `go test ./internal/ui -run 'Interrupt\|Cancel\|Escape\|Lifecycle\|Orphan\|Spinner\|Terminal'` ✅ · `go test ./internal/runtime/autonomy ./internal/execution` ✅ |
| Reference | `docs/design/PHASE/PHASE_7_AUTONOMOUS_WORKFLOW_COMPLETION_REPORT.md`, `docs/architecture/tech/EXECUTION-CONTRACT.md`, `docs/audits/IZEN_RUNTIME_REALITY_AUDIT.md` |
| Non-goal compliance | No dummy tools, no fake harness, no `/ask`→agentic change, no second scheduler/executor, no authorization move, no broad refactor, no production edits |

> **Purpose.** Resolve the architectural boundary that Phase 12 identifies: Izen already behaves as an agent runtime at the control-plane level, but its provider-facing transport still represents execution as ordinary chat completion. This document defines *what an Izen agent is*, separates that canonical semantics from provider wire protocols, audits the OpenRouter agentic-only-model case (Inkling), and verifies the runtime signal/state-truth addendum — **before** any implementation.
>
> **Evidence discipline.** Every claim carries a `file:line`. Items that could not be confirmed against source are marked **[UNVERIFIED]**. Live OpenRouter traffic could not be captured in this environment (no credentials); §11 evidence is code-level plus recorded provider behaviour, and the limitation is stated explicitly in §F.

---

## 0. Executive Answer — The 15 Exit Questions

| # | Question | Answer (evidence) |
| --- | --- | --- |
| 1 | What is an "agent" in Izen? | The **bounded control loop owned by `internal/runtime/autonomy.Driver`** (`driver.go:252`, `:851`, `:888`) that observes→decides→proposes→admits→authorizes→executes→verifies→recovers, over the single canonical executor. "Agentic" is a *runtime property*, never a wire property. |
| 2 | What is an "agent turn"? | One bounded model interaction inside that loop: a single `ExecutorAdapter.Execute` → `RuntimeExecutor.Execute` → one provider invocation, carrying a step contract, a bounded context projection, and a step budget. See §B `AgentTurn`. |
| 3 | What is the canonical Izen Agent Protocol? | The **minimal semantic contract** in §B: `InteractionContract`, `AgentTurn`, `AgentContext`, `AgentCapability`, `AgentProposal`, `AgentResult`, `AgentEvidence`. It is adapter-facing and authority-neutral; it does not exist today (zero matches for `AgentTurn`/`AgentProtocol`/`DirectCompletion`/`AgenticLoop`). |
| 4 | What is an execution contract? | The *kind of model interaction* a step requires — `DirectCompletion`, `StructuredCompletion`, `ToolEnabledCompletion`, `AgenticLoop` — selected from **intent + mode ceiling**, not from provider capability (§C). Today it is implicit in the routing decision (`autonomy/workspace.go:192`, `autonomy/engine.go:185`). |
| 5 | What does a provider-native tool call mean to Izen? | An **untrusted proposal** (`AgentProposal`), never execution. Today native `tool_calls` are parsed into `ai.Response.ToolCalls` (`openrouter.go:261`) that **nothing consumes**; the streaming `ToolCallSentinel` (`openrouter.go:154`) has no downstream parser. See §D and §7. |
| 6 | Who owns authorization? | `execution.AuthorizationEngine` via `AuthorizeBuild` (`autonomy.go:624`) and the executor's admission/authorization gates (`execution/admission.go:368`, `executor.go:1045`/`:1086`). Providers never authorize. |
| 7 | Who owns execution? | One canonical authority: `execution.RuntimeExecutor` (`executor.go:444`), constructed exactly once at the composition root (`compose.go:585`); verified by `TestPhase1_SingleProductionExecutionAuthority`. |
| 8 | How does Izen select context? | Multiple resolvers (planner, runtime context compiler, strategy envelope, retrieval orchestrator, context builder) each do relevance selection under a budget. See §E. There is **no single facade** and one authority (`internal/contextcompiler`) is **dormant**. |
| 9 | How does Izen bound context and output budgets? | Per-stage budgets: planner/contextcompiler 4 000 tok, strategy 0/4 000/16 000 tok, executor 200 KB/target, `llmstep` output 1 536/1 200/256 tok with ≤3 continuations, compactor 200 000 tok. See §E budget table. |
| 10 | How does an OpenRouter agentic-only model fit? | As a **wire-compatibility** refusal, not a semantic one: the model is *discovered* in the catalog but *ineligible* for the ordinary API-key execution path. Izen must represent this as an `InteractionContract` compatibility verdict behind the adapter. See §F. |
| 11 | Why does `/ask` differ from `/build`? | `/ask` is a **direct LLM call** from the TUI (`commands.go:1019`, `stream.go:573`) and never reaches the executor; `/build` is an **executor/driver run** (`runtime_cutover.go:337`, `autonomous.go:43`). See §C. |
| 12 | Where does provider-specific protocol translation live? | In `internal/providers/*` (`ai.Provider` implementations) and the adapter/capability layer (`internal/provider/adapter`, `internal/provider/capabilities.go`). Wire quirks currently leak *only* there — with one exception: the model-eligibility policy currently sits in core `internal/provider/registry` (see §F.6). |
| 13 | Can the architecture support providers with different agent protocols without changing core? | Yes — the protocol boundary is `ai.Provider` + the new `InteractionContract` descriptor. Core dispatches a semantic turn; each adapter encodes it. No core change is required per provider. |
| 14 | Can native tool calling coexist with the prompt/structured path? | Yes, if and only if native `tool_call` is normalized to `AgentProposal` and re-enters admission/authorization. It must **not** be executed by a provider-side loop. See §7 and §14. |
| 15 | Does the design preserve *Dynamic Path / Static Authority / Truthful State Transition*? | Yes. Dynamic path = intent-derived contract + bounded loop; static authority = unchanged admission/authorization/executor; truthful state = verified in §H. |

---

## A. Current-State Architecture

### A.1 Canonical production path (`/build`, autonomy-wired)

```text
User (TUI)
  → handleInput                      internal/ui/commands.go:184
  → intent AST / directive routing   internal/ui/intent_dispatch.go:31,139
  → autonomy.Engine.Decide           internal/autonomy/engine.go:185
  → dispatchAutonomyTrace            internal/ui/autonomy_route.go:85
  → executeAutonomyWorkspace         internal/ui/autonomy_route.go:157
  → executeAutonomyViaDriver         internal/ui/autonomous.go:43
  → runAutonomousDriver              internal/ui/autonomous.go:101
  → Driver.Run                       internal/runtime/autonomy/driver.go:252
  → observeAndRun                    internal/runtime/autonomy/driver.go:851   (loop :888)
  → ExecutorAdapter.Execute          internal/runtime/autonomy/adapter.go:180
  → RuntimeExecutor.Execute          internal/execution/executor.go:1010
       admission I                   internal/execution/admission.go:368 / executor.go:1045
       strategy                      internal/execution/executor.go:1064
       admission II                  internal/execution/executor.go:1086
       invokeStream                  internal/execution/executor.go:2886
  → provider.ExecuteStream           internal/providers/openrouter.go:305  (or Execute)
  → Model
  → ingest / verify                  internal/execution/{verify.go:336, evidence.go:104}
  → sealTerminalEvidence             internal/execution/executor.go:3446
  → finalizeResult                   internal/execution/executor.go:3531
  → Observation → loop Consume*      internal/runtime/autonomy/driver.go:995
  → terminal/parks                   autonomousRunMsg / executionResultMsg
```

### A.2 Where semantic agent behaviour exists

- **Bounded loop + state machine**: `internal/runtime/autonomy/driver.go:851`; loop states `internal/autonomy/runtime_loop.go`.
- **Intent → required capabilities** (pure function): `internal/autonomy/intent.go:103-124`.
- **Workspace/contract selection from capability** (mutation ⇒ BUILD): `internal/autonomy/workspace.go:200` (`SelectWorkspace`).
- **Admission + authorization + single executor**: `internal/execution/admission.go:368`, `execution/executor.go:444`.
- **Evidence + verification + continuation**: `execution/evidence.go:104`, `execution/verify.go:336`, `internal/llmstep/response_state.go:31`.

### A.3 Where protocol-level agent behaviour is missing

| Gap | Evidence |
| --- | --- |
| No canonical agent-protocol abstraction | Zero matches for `AgentTurn`/`AgentProtocol`/`DirectCompletion`/`StructuredCompletion`/`ToolEnabled`/`AgenticLoop` under `internal/` |
| `ai.Provider` is a two-method text API with no contract/mode switch | `internal/ai/provider.go:140-144` |
| Native tools are effectively dead on the wire | `ai.Request.Tools` (`provider.go:36`) is never populated in production; only production write is `req.Tools = nil` (`internal/ui/stream.go:513`); `ai.FileMutationTools()` (`ai/tools.go:95`) has no production caller |
| Parsed native tool calls are unconsumed | `ai.Response.ToolCalls` read only by the three producing providers; `execution.DispatchToolCalls` (`execution/toolcalls.go:334`) has no production caller |
| Streaming tool sentinel has no parser | `ToolCallSentinel` (`openrouter.go:154`) referenced only by producers; stream classifier knows only `ReasoningSentinel` (`internal/core/stream/splitter.go:110`) |
| `tool_choice` is never sent by any provider | repo-wide: no `tool_choice` symbol |
| `response_format` reaches only Ollama | `internal/providers/ollama.go:211` (`format:"json"`); constructed at `internal/modes/plan/engine.go:970`; OpenRouter/OpenAI/Claude/Gemini/Groq drop it |
| Two disconnected provider stacks | live `ai.Provider` (`providers/*`) vs legacy `llm.LLMProvider` (`internal/llm/provider.go:72`), no shared contract |

### A.4 Competing executors (all explicitly non-authoritative)

| Type | Location | Status |
| --- | --- | --- |
| `execution.RuntimeExecutor` | `execution/executor.go:444` | **THE canonical production authority**; constructed once (`compose.go:585`) |
| `runtime/executor.RuntimeExecutor` | `runtime/executor/executor.go:40` | unreachable Control-Plane coordinator (Case C) |
| `runtime/scopeguard.RuntimeExecutor` | `runtime/scopeguard/gateway.go:199` | subordinate idempotency primitive (Case B); no production wiring |
| `execution.Engine` | `execution/execution.go:15` | legacy mode engine; not the build authority |
| `runtime/orchestrator.Loop` | `runtime/orchestrator/loop.go:152` | control-plane loop primitives |

**Convergence proof:** `internal/architecture/phase1_authorization_boundary_test.go:323` (`TestPhase1_SingleProductionExecutionAuthority`) and `internal/architecture/execution_invariants_test.go:150` (`TestRuntimeExecutorSingleCompositionBinding`).

### A.5 Composition root / wiring

`internal/runtime/compose/compose.go:428` `Wire` builds the `Application` (`:106`); executor at `:585`, gateway at `:586`, autonomy engine at `:782`, driver + adapter at `:791-803`. TUI binds them at `internal/ui/program.go:172-201`.

---

## B. Protocol Proposal — The Minimal Izen Agent Protocol

### B.0 Design rules

1. **The protocol describes Izen semantics, not any provider API.** It never contains `tool_calls`, `tool_use`, `response_format` or provider headers.
2. **Authority-neutral by construction.** No protocol object carries authorization. `AgentCapability` is a *ceiling for proposal*, not a grant.
3. **Reuse before inventing.** Izen already has authoritative usage, outcome, continuation and evidence types; the protocol must not duplicate them.
4. **Name collision warning.** `execution.ExecutionContract` already exists as an **OCC/recovery** primitive (`internal/execution/contract.go:76`). Do **not** overload it for provider interaction. The interaction kind is named `InteractionContract` here.

### B.1 Vocabulary (minimal)

| Primitive | Purpose | Owner | Authority | Lifetime | Input | Output | Security | Token |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `InteractionContract` | Declare the *kind* of model interaction a step needs | Intent/strategy selector | none (descriptive) | per step | intent + required caps + mode ceiling | enum `{DirectCompletion, StructuredCompletion, ToolEnabledCompletion, AgenticLoop}` | none | 0 |
| `AgentTurn` | One bounded model interaction | Driver (loop) | none; executor enforces | one step | objective, contract, `AgentContext`, step budget, allowed capability ceiling | `AgentProposal` | carries no secrets; scope is bounded | bounded by step budget |
| `AgentContext` | The selected, bounded projection sent to the model | Context resolver | none | one turn | `ContextSources`, `ContextBudget`, `ContextScope`, `ContextLineage`, `ContextExclusions` | rendered messages/system | exclusions enforced pre-send | ≤ budget |
| `AgentCapability` | The set of capabilities the model may *propose against* | Intent classifier | ceiling only; never a grant | per turn | required caps | cap set | cannot elevate mode | schema cost if rendered |
| `AgentProposal` | Normalized untrusted model output | Adapter | **untrusted** | one turn | text artifact / structured object / native tool call | artifact or typed action | must pass admission | model output |
| `AgentResult` | Post-execution result of the turn | Executor | evidence-backed | one step | verification outcome | outcome + evidence + usage + continuation cursor | — | evidence budget |
| `AgentEvidence` | Verification facts | Executor | authoritative | one step | verification | refs, digests | — | evidence budget |

### B.2 Mapping to existing authoritative types (no duplication)

| Protocol concept | Existing authoritative type | File |
| --- | --- | --- |
| usage | `ai.ProviderUsage` | `internal/ai/provider.go:70` |
| finish/outcome | `provider.StreamOutcome` + `MapFinishReason` | `internal/provider/capabilities.go:58-84` |
| bounded continuation | `llmstep.ResponseState` | `internal/llmstep/response_state.go:31` |
| loop outcome / termination | `autonomy.Observation`, `ExecutionOutcome`, `LoopTermination` | `internal/autonomy/runtime_loop.go:198,85,514` |
| evidence | `execution.ExecutionProof`, `ExecutionEvidence` | `internal/execution/executor.go:223`, `evidence.go:104` |
| proposal (human) | `autonomy.MutationProposal` | `internal/autonomy/proposal.go:144` |

### B.3 Ownership diagram

```text
IZEN CORE (semantic authority)                      PROVIDER BOUNDARY (wire)
┌──────────────────────────────────────────┐
│ Intent → InteractionContract selection   │
│ AgentTurn (bounded)                      │
│ AgentContext (selected, budgeted)        │
│ AgentCapability (ceiling)                │
│ AgentProposal  ← UNTRUSTED               │
│ Admission → Authorization → Execution    │
│ AgentEvidence / AgentResult              │
└───────────────────┬──────────────────────┘
                    │  ai.Provider + InteractionContract descriptor
                    ▼
             Provider Adapter (translator)
          ┌─────────┼──────────┬─────────┐
          ▼         ▼          ▼         ▼
      OpenRouter  Anthropic   Ollama    OpenAI
```

**Invariant:** the adapter may *translate* an `AgentTurn` into any wire format and *normalize* any wire response into an `AgentProposal`, but it may never execute a proposal and may never grant authority.

---

## C. Execution Contract Matrix

Derived from the mode audit (mode capabilities at `internal/modes/modes.go:82-88`; routing at `internal/ui/commands.go`).

| Interaction Contract | `/ask` | `/plan` | `/build` | `/build $hot` | `/investigate` | `/review` |
| --- | --- | --- | --- | --- | --- | --- |
| **DirectCompletion** | ✅ primary (`commands.go:1019`, `stream.go:573`) | ✅ streaming fallback (`commands.go:975`) | — (executor owns provider) | — | ✅ internal engine dispatcher (`agents.go:135`) | — (deterministic, `agents.go:515`) |
| **StructuredCompletion** | optional (read-only contract) | ✅ plan synthesis `json_object` (`modes/plan/engine.go:970`; only Ollama renders it) | possible per-step (not wired) | possible (not wired) | — | — |
| **ToolEnabledCompletion** | ❌ (never; read-only) | ❌ | ❌ today (tools not sent) — **candidate** | ❌ today — **candidate** | ❌ | ❌ |
| **AgenticLoop** | ❌ (bounded continuation only, `llmstep`) | ❌ | ✅ `Driver`+`RuntimeExecutor` (`autonomous.go:43`) | ✅ same, `ScopeDeclared` (`autonomy_route.go:71`) | internal bounded hypotheses (own engine) | ❌ |

Notes:
- `/ask` never touches `m.executor` or `m.autonomy` (`commands.go:348-350`, `:1019-1038`).
- `/build` selects strategy/target in the runtime, not the UI (`runtime_cutover.go:124-158`); mode is a presentation label there (`gateway.go:78-81`).
- Contract selection is currently **implicit**: required caps (`autonomy/intent.go:103`) → workspace contract (`autonomy/workspace.go:200`) → controller verdict (`autonomy/controller.go:114`), clamped by mode (`autonomy/mode_authority.go:46`).
- `$hot` differs from `$prompt` by binding `ScopeDeclared` (`autonomy_route.go:72`) and setting the hotfix objective (`:78-79`); both enter the same autonomy boundary (`:57`).

**Proposed rule:** `InteractionContract = f(intent, required caps, mode ceiling)` — *never* `f(provider capabilities)`. A model's support for tools must not silently promote `/ask` to `ToolEnabledCompletion`.

---

## D. Provider Protocol Matrix

Only verified wire behaviour is recorded. Source: `internal/providers/*`, `internal/ai/*`.

| Dimension | Izen canonical | OpenRouter | Anthropic (Claude) | Ollama |
| --- | --- | --- | --- | --- |
| **Tool calling** | semantic `AgentProposal`; **no wire tools sent today** | parses `tool_calls` only on `finish_reason=="tool_calls"` (`openrouter.go:261-277`); **no `tool_choice`**; stream sentinel unparsed (`:154`) | ignores `tool_use` blocks; reads only `type=="text"` (`claude.go:217-222`) | none |
| **Structured output** | `ai.Request.ResponseFormat` (`provider.go:35`) | dropped | dropped | `format:"json"` when `json_object` (`ollama.go:211`) |
| **Streaming** | `ExecuteStream` → `io.ReadCloser` | SSE reader `openrouterSSEReader` (`openrouter.go:1010`) | SSE (`claude.go:247`) | SSE (`ollama.go:302`) |
| **Continuation** | `llmstep.ResponseState`, transcript-free (`response_state.go:121`) | stateless | stateless | stateless |
| **Context** | caller-assembled `Messages`+`System` (`provider.go:27-31`) | passthrough | top-level `system` | passthrough |
| **Capability declaration** | `ModelDescriptor.Capabilities` (`registry/types.go:10-13`) + `provider/adapter/capability.go:41` | `/models` `supported_parameters` (`providers/capability/adapter.go:74`) | static | `/api/tags` (`adapter.go:173`) |
| **Error semantics** | `httpx.ProviderError` + `IsModelCompatibility()` (`provider_error.go`) | 401→`ErrOpenRouterAuth` (`openrouter.go:25`); 403 agentic→compat sentinel (`:53`); 429 backoff (`:731`) | HTTP status | HTTP status |

**Izen (canonical) column is the target state, not the current wire state.** Today Izen sends **no** tools, **no** `tool_choice`, and structured output only to Ollama.

---

## E. Context Architecture

### E.1 Stage pipeline (as it exists)

```text
Intent
  → Context Resolution   (several independent resolvers — see E.2)
  → Context Projection   (Assemble/Render → messages/system)
  → Provider Adapter     (wire encode)
```

### E.2 Resolvers (no single facade)

| Resolver | Selection mechanism | File |
| --- | --- | --- |
| Context Planner | intent → fixed allocation → graph/log/file sources → rank → budget-fit | `internal/planner/orchestrator.go:114,153,191` |
| Intent-aware knapsack | depth from confidence → relevance-desc knapsack ≤ budget | `internal/runtime/context/compiler.go:35-76` |
| Strategy envelope | adds `ContextItem` only when strategy profile demands; `none`→empty | `internal/execution/strategy/context.go:137-235` |
| Context Builder | git + Lea graph + session; `MaxFiles=10`, `MaxSymbols=30` | `internal/context/builder.go:105,125-130` |
| Retrieval orchestrator | graph → vector → fulltext → ripgrep escalation | `internal/retrieval/orchestrator.go:125-194` |
| Context Compiler (**authority**) | priority order + per-source shares | `internal/contextcompiler/compiler.go:150`, `budget.go:78` |
| Autonomy artifact compiler | findings + bounded regions | `internal/autonomy/context.go:185` |
| Compaction | generational summary + windowed history | `internal/session/compaction/engine.go:45` |

> ⚠️ **`internal/contextcompiler` is dormant.** It is constructed (`compose.go:512`) and exposed (`:390`) but **no production caller invokes `Compile`** (grep: only its own tests + a construction assertion at `phase2_context_subsystems_test.go:137`). Its "Context Compilation authority" budgets are therefore not enforced at runtime. **[Verify before relying on it.]**

### E.3 Budget enforcement points

| Budget | Value | File |
| --- | --- | --- |
| Planner / contextcompiler total | 4 000 tok | `planner/types.go:33`, `contextcompiler/budget.go:58` |
| Strategy context budget | 0 / 4 000 / 16 000 tok (none / target / repository) | `strategy/selector.go:602-641` |
| Executor per-target bytes | 200 KB | `executor.go:3626` |
| `llmstep` output | Ask 1 536 / Mutation 1 200 / Classify 256, ≤3 continuations | `llmstep/step.go:42-57` |
| Conversation compactor | 200 000 tok, 0.80 soft / 0.95 hard | `context/compactor/types.go:24-35` |
| UI history window | agentic 20 / casual 6 messages | `ui/stream.go:384-394` |
| Session sliding window | `maxTurns*2` | `session/session.go:344-358` |

**Semantic vs blind:** `selectKnowledge` sorts by confidence (`contextcompiler/compiler.go:230`); `fitBudget` ranks by priority+score (`planner/orchestrator.go:191`); continuation never replays the transcript (`llmstep/step.go:192`). Blind truncation still exists (`truncateToTokens = budget*4` runes at `compiler.go:352`; head/tail crop at `session/compaction/engine.go:270`).

### E.4 Whole-repo / whole-conversation / tool-registry inclusion

- **Whole repository: not sent.** Relevance-matched, `MaxFiles=10`/`MaxSymbols=30` (`context/builder.go:125`); `IncludeAll` has no production caller; target bytes capped at 200 KB.
- **Whole conversation: not sent.** Windowed (`ui/stream.go:384`); continuations are transcript-free (`llmstep/response_state.go:117`).
- **Whole tool registry: does not exist.** Only two tool schemas exist (`ai/tools.go`), and no production request populates them.

### E.5 Target shape (proposal, not implementation)

Introduce one **Context Resolver facade** returning an `AgentContext` (sources, scope, budget, lineage, exclusions). The facade delegates to the existing resolvers; it does **not** replace them. Make `internal/contextcompiler` live *or* delete it — a dormant "authority" is an invariant hazard.

---

## F. OpenRouter Compatibility Design (Inkling Small Free)

### F.1 The distinction that must survive

```text
semantic compatibility   = can the model reason about this step's objective?
wire compatibility       = can the model be invoked through THIS execution path?
```

Catalog presence ≠ execution compatibility. OpenRouter's `GET /models` is a **discoverability** catalog; it exposes no harness-eligibility flag (id, description, context_length, architecture, pricing, top_provider, supported_parameters, reasoning — none). The refusal appears only at inference time:

```text
POST /chat/completions → HTTP 403
"thinkingmachines/inkling-small:free is only available on agentic harnesses."
```

### F.2 Model Compatibility model (proposed)

Compatibility is a **matrix**, not a boolean:

| Dimension | Meaning |
| --- | --- |
| Provider | who serves it |
| Model | which model id |
| InteractionContract | which contract it can serve (`DirectCompletion` vs `AgenticLoop`) |
| ProtocolCapabilities | tools / structured output / streaming / reasoning |
| ContextLimit / OutputLimit | budgets |
| AgenticRequirements | provider-side harness restrictions |

Inkling is: `DirectCompletion → incompatible`, `AgenticLoop → possibly compatible (unverified)` — it must **not** be marked globally "incompatible".

### F.3 Audit of the "Shadow Schema" proposal (dummy `execute_agent_step` tool)

| Question | Verdict |
| --- | --- |
| Violates the Izen protocol boundary? | **Yes.** It injects a wire artifact whose semantics do not exist in Izen, to satisfy a gateway detector. |
| Creates undocumented provider coupling? | **Yes.** It binds Izen to an undocumented OpenRouter detection heuristic. |
| Can it become a stable adapter? | **No.** The trigger is unspecified and may change without notice. |
| Does the tool have semantic meaning? | **No.** It would be a decoy. |
| If OpenRouter changes detection? | Silent breakage; the 403 returns and the hack is inert. |
| Is a real tool-call/result loop required? | **Not proven.** No evidence establishes that `tools != nil` is the authorization condition. |
| Would it misrepresent Izen's capabilities? | **Yes.** It advertises an agent harness Izen does not expose over that path. |

**Recommendation: REJECT.** The mission's §8 instruction is upheld: do not approve merely because 403 disappears.

### F.4 Assessment of the in-progress eligibility work (this branch)

The uncommitted changes introduce an evidence-seeded **eligibility policy**:

- `internal/provider/registry/eligibility.go` — `CheckExecutable`, `ModelDescriptor.Executable()`, `ExecutableModels`, with one seeded entry (`openrouter/thinkingmachines/inkling:free`, `ReasonAgenticHarnessOnly`) and explicit `Evidence`.
- `internal/provider/registry/{types,snapshot,cache}.go` — `IneligibleReason` marks discovered-but-not-executable; `LoadExecutable()` is the selectable view; the raw catalog and on-disk cache preserve every model.
- `internal/providers/openrouter.go` — pre-flight `guardModelExecutable` (`:45`) and `asCompatibilityError` (`:53`) classify a harness 403 without retry or silent model switch.
- `internal/httpx/provider_error.go` — `IsModelCompatibility()` (`403` + "agentic harness").
- Selection guards: model picker uses `LoadExecutable()` (`model_picker_wiring.go:145`), direct/picker/role paths call `rejectIneligibleModel`.
- Atomic binding transaction: `persistAndActivateBinding` (`model_picker_wiring.go`) unifies disk store, session config and `RuntimeAuthority`.

**Verdict: principled, not a dummy-tool hack.** It correctly separates *discovered* from *executable* and keeps the raw catalog intact. It is compatible with §F.2 — **with two corrections**:

1. **Boundary placement.** The policy table lives in **core** `internal/provider/registry`. Per §13, provider-compatibility quirks belong at the **adapter boundary**. Move the seeded policy behind `internal/provider/adapter` (or a `provider/compat` package) so core types stay provider-neutral.
2. **Model it as contract compatibility.** A global `IneligibleReason` risks over-blocking: it should be keyed by `(provider, model, InteractionContract)`, so a model ineligible for `DirectCompletion` is not implicitly ineligible for an `AgenticLoop` contract. Today's single boolean is correct for the observed case but must not ossify.

Also note the residual asymmetry: `role_bridge.go:63` `ResolveRoleModel` consumes `reg.Snapshot()` (full catalog), not `LoadExecutable()` — a role could still resolve to an ineligible model. **[Flag for the migration plan.]**

### F.5 The critical experiment (§11) — limitation

The mission asks to capture OpenCode vs Izen `/ask` vs Izen `/build` wire traffic for Inkling. **This could not be executed** in this environment (no live OpenRouter credentials / raw OpenCode capture). What *is* established from source:

- Izen `/ask` sends no tools (`ui/stream.go:494-517`), `/build` likewise does not populate `Tools`.
- Izen currently sends **no** `tool_choice` and only Ollama receives structured output.
- Therefore "Izen sends tools" is **not** a valid explanation of any difference — Izen sends none on either path. The OpenRouter 403 is a **provider-side eligibility** refusal, not an Izen payload-shape issue.

The remaining unknown — whether OpenCode's traffic differs in a way that grants eligibility — is explicitly **unresolved** and must be established empirically before any adapter change.

### F.6 How Inkling should work if supported

1. Catalog discovery records it with `IneligibleReason` for `DirectCompletion`.
2. The picker shows it as *"unavailable for this execution path"* (never silently hidden — truthful UI).
3. `/ask` with Inkling → explicit compatibility error, no retry.
4. If Izen later implements a genuine agentic wire contract that satisfies OpenRouter, the same model becomes eligible **for that contract only** — a matrix update, not a core change.

---

## G. Migration Plan (smallest sequence, no rewrite)

Each step is independently revertable and preserves the canonical executor.

| Step | Change | Files (primary) | Risk | Tests |
| --- | --- | --- | --- | --- |
| **G1. Freeze vocabulary** | Land this document; add an ADR naming `InteractionContract` (avoid the `execution.ExecutionContract` collision) | docs only | none | — |
| **G2. Contract descriptor (no behavior)** | Add `InteractionContract` enum + per-step metadata; carry it on the existing `LoopRequest`/`ExecuteRequest` without changing dispatch | `internal/ai`, `internal/execution` | low | new unit tests; existing parity |
| **G3. Explicit selection** | Derive contract from `intent + caps + mode ceiling` at the existing decision point | `autonomy/workspace.go`, `autonomy/engine.go`, `execution/strategy/selector.go` | medium | contract matrix tests (§C) |
| **G4. Adapter capability descriptor** | One per-provider capability record keyed by contract; replace scattered booleans | `provider/adapter/capability.go`, `provider/capabilities.go` | low | provider matrix tests |
| **G5. Move eligibility to adapter boundary** | Relocate the seeded policy; key by `(provider, model, contract)`; fix `role_bridge.go:63` to use `LoadExecutable` | `provider/registry/eligibility.go` → `provider/adapter` | low | existing eligibility tests + role test |
| **G6. Honest native tools** | Either consume `ai.Response.ToolCalls`/`ToolCallSentinel` through `AgentProposal`→admission, or delete the dead paths | `providers/openrouter.go`, `execution/toolcalls.go`, `core/stream` | medium | tool-proposal authority tests |
| **G7. Context facade** | Introduce the `AgentContext` facade; make `contextcompiler` live or delete it | `internal/context*`, `runtime/context` | medium | budget parity tests |
| **G8. Control-plane hardening** | Add a single typed terminal message and a hard cancellation deadline fallback | `internal/ui`, `runtime/autonomy` | medium | §H tests |

**Do not** reorder G5 before G4 (boundary must exist first). **Do not** implement G6 until the admission mapping is proven.

---

## H. Signal & Context Flow Verification (Addendum)

### H.1 The addendum's claims vs. the code

The addendum reports: (i) on a terminal executor error (e.g. HTTP 403) the TUI stays in BUILDING with a live spinner and running timer; (ii) Esc / Ctrl+C fail to interrupt a hanging background execution.

**Finding: as of this commit, both invariants are wired and covered by regression tests.** The addendum's described failure mode appears to have been addressed by the recent state-sync/unwind and double-tap-Esc work (commits `baf5700` #174, `0d42b51` #176). The claim cannot be reproduced live here (no credentials), so this section verifies by code path + tests.

### H.2 Truthful terminal transition (403 → IDLE/ERROR)

Deterministic flow for a provider 403 on the canonical path:

```text
OpenRouter 403
  → asCompatibilityError / ProviderError        internal/providers/openrouter.go:53,246
  → invokeStream returns err                    internal/execution/executor.go:2929-2942
  → ExecutorAdapter.Execute returns err         internal/runtime/autonomy/adapter.go:180
  → Driver.observeAndRun: return nil, err       internal/runtime/autonomy/driver.go:939-941
  → autonomousRunMsg{term:nil, err}             internal/ui/autonomous.go:172-175
  → handleAutonomousRun (msg.err != nil)        internal/ui/autonomous.go:448
      execStreamCh=nil; execStreaming=false;
      stopShimmer(); setStage(stageFailed)      internal/ui/autonomous.go:429-434
      finalizeOperation(OpOutcomeFailure)       internal/ui/autonomous.go:487
  → clearBusyFlags + syncUIState → StateChat    internal/ui/operation.go:247, model.go:3058
  → timer cleared (executionStartedAt=0)        internal/ui/model.go:4048
```

For the single-shot gated path the equivalent terminal is `executionResultMsg` → `executionResultUpdate` (`internal/ui/gateway.go:360`), reached from `gateway.go:184,277`.

**Invariant:** `isWorkflowBusy()` (`model.go:3149`) is the sole gate for `StateProcessing`; `finalizeOperation` clears every transient flag (`operation.go:273`) and `clearBusyFlags` zeroes `executionStartedAt` (`model.go:4048`). A terminal error cannot leave the derived state active.

**One intentional deferral:** the `streamErrMsg` handler, when `m.execStreaming` is true (driver path), stops the spinner but **does not** finalize the operation (`update.go:3229-3235`) — it defers to the terminal `autonomousRunMsg`. Truthfulness therefore depends on that message always arriving. It does, because the driver run is a Bubble Tea `tea.Cmd` whose return value is always delivered (`autonomous.go:172`), and the driver returns on provider error (`driver.go:939`). **This is the single point where a future hang would manifest** and is the rationale for H.6.

### H.3 Cancellation plumbing (Esc / Ctrl+C)

```text
Esc (double-tap, 1.5 s window)          internal/ui/interrupt.go:108-133
  → MsgCancelStream                     internal/ui/interrupt.go:35
  → handleEmergencyInterrupt("escape")  internal/ui/model.go:2877
Ctrl+C                                   internal/ui/operation.go:313
  → handleCtrlC → cancelActiveOperation  operation.go:329,297
  → handleEmergencyInterrupt             model.go:2877
     ├─ activeOp.Cancel()                model.go:2884-2886   (root session ctx)
     ├─ cancelAllBackgroundContexts()    model.go:2888, commands.go:3812
     ├─ streamCancel() / shellCancel()   model.go:2889-2896
     ├─ execution.KillAllOrphans()       model.go:2897, execution/runner.go:346
     ├─ clearBusyFlags + syncUIState     model.go:2914,2962
     └─ workflow EventUserInterrupt      model.go:2954
```

Downstream propagation:

```text
operation ctx → Driver.runCtx          driver.go:267
  → adapter.Execute(ctx)               driver.go:933
  → executor.Execute(ctx)              adapter.go:360
  → provider.ExecuteStream(ctx)        executor.go:2929
  → http.NewRequestWithContext(ctx)    openrouter.go:695
```

Every layer honours `ctx`: the only `select` on the autonomy loop path is `RuntimeLoop.Step` (`runtime_loop.go:765`, select at `:773-774`); the preflight barrier has `waitCtx.Done()` (`preflight/barrier.go:83`); the rate-limit wait is interruptible (`waitRateLimitRetry`, `openrouter.go:788`).

### H.4 Goroutine-leak audit

| Concern | Finding |
| --- | --- |
| Unbounded loop after terminal failure | No. `for !State().IsTerminal()` (`driver.go:888`); terminal returns clear the run context (`driver.go:324-327`). |
| Channel read without `ctx.Done()` | No select in the driver loop at all; the only autonomy select is guarded (`runtime_loop.go:773`). Cancellation is cooperative via `ctx.Err()` poll (`driver.go:893`). |
| Provider connection release on cancel | Requests built with `NewRequestWithContext` (`openrouter.go:695`); non-OK bodies drained and closed before returning (`openrouter.go:340-344`). |
| Subprocess orphans | `execution.KillAllOrphans()` on every emergency interrupt (`model.go:2897`). |
| Timer leaks | `streamInterTokenTimer.Stop()` in `clearBusyFlags` (`model.go:4043-4047`); watchdog self-terminates when idle (`operation.go:409-411`). |

### H.5 Empirical verification

All green at this commit:

```
go build ./...                                                          ✅
go test ./internal/httpx ./internal/provider/registry                   ✅
go test ./internal/providers -run '...Eligib|...403|...Forbidden'       ✅
go test ./internal/ui -run 'Interrupt|Cancel|Escape|Lifecycle|Spinner'  ✅
go test ./internal/runtime/autonomy ./internal/execution                ✅
```

Key regression tests proving the addendum invariants:

| Test | Proves | File |
| --- | --- | --- |
| `TestEmergencyEscapeHatchEscUnfreezesStateProcessing` | double-Esc returns to `StateChat`, clears flags | `escape_hatch_test.go:59` |
| `TestEmergencyEscapeHatchCtrlCUnfreezesStateProcessing` | Ctrl+C clears flags, spinner frame 0 | `escape_hatch_test.go:33` |
| `TestRegressionEscDuringAutonomousExecutionReleasesSpinner` | autonomous interrupt does not finalize early; terminal msg finalizes to `StateChat` + focus | `operation_regression_test.go:325` |
| `TestAutonomousSpinnerLifecycle` | shimmer stops only on terminal | `operation_regression_test.go:382` |
| `TestGatedExecutionCtrlCCancelsProviderCall` | provider call cancelled | `gated_cancellation_test.go:52` |
| `TestExecute_RejectsIneligibleModelWithoutNetwork` / `TestExecute_ClassifiesAgenticHarness403` | 403 classified; no network on guard | `openrouter_eligibility_test.go:32,67` |
| `TestTerminalExecutionMsgHaltsTimersAndUnwindsBuildPhase` | fatal terminal event atomically halts timer/spinner, clears BUILDING header phase, restores IDLE, < 50 ms | `terminal_execution_test.go` |
| `TestExecutionResultFailureUnwindsBuildPhase` | executor terminal failure releases the workflow phase (header) | `terminal_execution_test.go` |
| `TestFatalDriverErrorEmitsTerminalExecutionMsg` / `TestRecoverableDriverErrorStaysAutonomousRunMsg` | fatal driver error → `TerminalExecutionMsg`; recoverable selection rejection stays parked | `terminal_execution_test.go` |

### H.6 Residual gaps vs. the addendum spec (actionable)

> **Implementation note (2026-09-24).** Gap 1 below has been implemented in this task:
> `TerminalExecutionMsg` now exists (`internal/ui/terminal.go`), is emitted by
> `driveAutonomy` on every fatal driver error (`internal/ui/autonomous.go`), and
> its handler performs the atomic IDLE transition. The workflow-SM unwind gap
> (the root cause of the lingering BUILDING header) is fixed in
> `executionResultUpdate` (`gateway.go`), `handleAutonomousRun` (`autonomous.go`),
> and the driver's cancellation gate is now an explicit
> `case <-ctx.Done():` (`runtime/autonomy/driver.go`).

1. ~~**No single strongly-typed `TerminalExecutionMsg`.**~~ **Implemented.** The
   autonomous path now emits `TerminalExecutionMsg`; the gated/executor path's
   `executionResultMsg` remains the equivalent terminal event and now unwinds
   the workflow phase. (`streamErrMsg` is still the pre-terminal provider
   notification.)
2. **No hard deadline / goroutine-boundary fallback.** The addendum asks for an emergency fallback if the runtime fails to exit within ~250 ms. Current hard exit is a **second Ctrl+C within a 5 s grace window** (`operation.go:336-347`, `signal.go:83`). There is **no** 250 ms goroutine-boundary termination. **Recommend** adding a bounded post-cancel watchdog that forces the presentation terminal state if no terminal message arrives within a deadline.
3. **Cooperative cancellation is a standing risk.** The driver observes cancellation only *between* loop iterations (`driver.go:893`) and relies on the in-flight executor/provider call honouring `ctx`. This holds today (`openrouter.go:695`) but any future blocking I/O that ignores `ctx` would reintroduce the addendum's hang. **Recommend** a lint/test invariant: every `select`/blocking call on the execution path must carry `ctx`.
4. **`streamErrMsg` deferral (H.2).** Intended, but it means a dropped terminal message leaves `agentRunning=true`. The G8 terminal-envelope + deadline fallback closes this.

### H.7 Deterministic verification procedure (for CI / manual)

```text
1. Inject a 403 via httptest (pattern: openrouter_eligibility_test.go:67).
2. Assert provider returns ErrOpenRouterModelIncompatible; 0 network on guard.
3. Feed the error through adapter → driver; assert Driver returns (nil, err).
4. Feed autonomousRunMsg{err} to model.Update; assert:
     state == StateChat, agentRunning == false, execStreaming == false,
     shimmerActive == false, activeOp == nil, executionStartedAt.IsZero().
5. Cancellation: arm+fire double-Esc (or Ctrl+C) mid-run; assert ctx cancelled
   within < 100 ms (measure), provider call returns context.Canceled,
   terminal message finalizes within one update cycle.
6. Leak: runtime.NumGoroutine() returns to baseline after terminal.
```

Steps 3–4 are already exercised by `operation_regression_test.go:325`; step 5 by `gated_cancellation_test.go:52`; step 6 is the recommended new assertion.

---

## Validation Against Phase 12 Invariants

| Invariant | Status |
| --- | --- |
| LLM output remains untrusted | ✅ §B.3, §7 — native tool call = `AgentProposal` |
| Provider protocol does not own authorization | ✅ §A.2, §D — authorization stays in `AuthorizationEngine` |
| Human approval semantics intact | ✅ `autonomous.go:207,570`; `state == StateAwaitingApproval` |
| Izen remains the execution authority | ✅ one `RuntimeExecutor` (`compose.go:585`) |
| One canonical production executor | ✅ §A.4 + architecture tests |
| Continuation re-enters same authority | ✅ `Driver.ResumeApprove` → same executor (`adapter.go:420`) |
| Context intentionally selected | ✅ §E (relevance + budgets; no whole-repo dump) |
| Token usage bounded by semantic need | ✅ §E.3 |
| Provider quirks behind adapters | ⚠️ **except** eligibility policy currently in core registry (§F.4 correction) |

| Anti-pattern | Status |
| --- | --- |
| semantic duplication | ⚠️ two provider stacks (`ai.Provider` vs `llm.LLMProvider`) — pre-existing; consolidate later |
| state-machine / scheduler / executor duplication | ✅ none authoritative |
| authorization bypass | ✅ none found |
| provider lock-in | ✅ adapter boundary |
| context explosion / token waste | ✅ bounded; but dormant `contextcompiler` is an unenforced budget |
| truthful state transition | ✅ §H (with G8 hardening recommended) |

---

## Final Principle

> Do not optimize Izen to become compatible with somebody else's definition of an agent. Define what an Izen agent is first — then make providers speak to it.

The provider is a communication endpoint. The model is a reasoning engine. The adapter is a translator. **Izen remains the runtime.**

---

## Appendix — Evidence Tier & Limitations

- **Tier:** Verify (Tier 2). Graph index `izen` generation `2026-09-24T07:47:17Z`, `moderate`; `check_index_coverage` returned `no_recorded_issue` for all 20 primary paths (best-effort; absence of a recorded gap is not proof of completeness).
- **Direct reads:** `internal/ui/autonomous.go`, `interrupt.go`, `operation.go`, `model.go`, `update.go`; `internal/runtime/autonomy/driver.go`; `internal/providers/openrouter.go`; `internal/execution/executor.go`; `internal/ai/{provider,tools}.go`; `internal/llmstep/response_state.go`; `internal/provider/registry/eligibility.go`; plus the regression tests cited in §H.5.
- **Subagent-assisted maps** (execution pipeline, provider boundary, modes, context) were cross-checked against the primary paths above.
- **Not verified:** live OpenRouter/OpenCode wire capture for Inkling (§F.5); exact production wiring of the legacy `internal/llm.LLMProvider` stack; whether `internal/contextcompiler` is invoked by a non-grep dynamic path.
- **No production code was modified by this phase.**
