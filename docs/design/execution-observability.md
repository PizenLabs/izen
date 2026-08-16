# Izen Execution Observability — Investigation Report

**Goal:** Understand why users cannot see truthful execution progress while the agent is working.

**Scope:** Input → command parsing → intent classification → execution planning → context discovery → file/symbol search → model invocation → streaming → artifact generation → approval → mutation → renderer.

**Status:** Investigation only. No code was modified, renamed, or redesigned.

---

## Executive summary

Izen has **two parallel execution engines with two different event vocabularies and two different rendering surfaces that never reconcile**. The path with the **best event stream** (gated `RuntimeExecutor`) has the **worst model-visibility** — it calls the provider **non-streaming** with **no reasoning handler**, runs outside the operation/telemetry/watchdog lifecycle, and is **not interruptible by Ctrl+C**. The path with the **best token visibility** (legacy `/ask` + fast-track `/build`) emits only **coarse mode-vocabulary events** and never drives the canonical execution timeline. Meanwhile the richest truth record (`ExecutionProof.RuntimeGraph`) is produced by the runtime and **never rendered anywhere**.

---

## A. Current execution visibility map

### The real flow — two divergent paths

```
Input (handleInput, commands.go:161)
 ├─ AST parse → directives/global cmds → dispatchASTIntent (commands.go:253)
 ├─ mode shorthand ("/build x") → handleMessageContent (commands.go:266)
 ├─ "/cmd" → handleCommand
 └─ bare text / $prompt / $hot → runGatedLine (gateway.go:42)
```

**Path 1 — Gated runtime (canonical)**

`runGatedLine` → `IntentGateway.Gate` (`internal/execution/intent.go:69`) → `RuntimeExecutor.Execute` (`internal/execution/executor.go:370`) → graph transitions emit the canonical stream (`internal/execution/graph/graph.go`) → bus → UI subscription (`internal/ui/program.go:311–337`) → `domainEventMsg` → `handleDomainEvent` (`internal/ui/model.go:1955`) → `execView.Project` (`internal/presentation/execution_projection.go:201`) → `renderLoadingDock` (`internal/ui/loading.go:166`).

**Path 2 — Legacy mode engines**

`handleMessageContent` → `runBuildCmd` / `runInvestigateCmd` / `runPlanEngineCmd` / `runReviewCmd` / `streamCmd` → mode engines (`internal/modes/{plan,build,investigate,review}/engine.go`) emit **mode vocabulary** events; `/ask` + fast-track stream raw tokens into `m.streamCh`.

### Where execution truth actually lives

| Truth | Location | Populated on which path |
|---|---|---|
| Canonical lifecycle events (`execution.started`…`execution.finished`) | `internal/execution/graph/graph.go:281–470` | **Gated only** (RuntimeExecutor) |
| Mode events (`plan.staged`, `patch.applied`, `stage.completed`, …) | `internal/modes/*/engine.go`, `internal/modes/build/executor.go` | **Legacy only** |
| Workflow states | `WorkflowStateMachine` (`internal/domain/workflow`) | both (UI derives from it, `model.go:2060`) |
| Execution-view state (narrative) | `internal/presentation/execution_projection.go` | **Gated only** |
| Stage record (`setStage`) | `internal/ui/stage.go` + `internal/execution/telemetry.go` | **Legacy only** — never called on gated path |
| Operation telemetry (`$inspect`) | `internal/ui/operation.go:240–259` | **Legacy only** — gated path starts no operation |
| Provider usage (authoritative) | `internal/ai/provider.go` `ProviderUsage` | both, but gated only at terminal (`ExecutionResult.Completed`, `executor.go:1057`) |
| Tool calls | `internal/execution/toolcalls.go` `ToolCallBuffer` | **fast-track only** |
| ExecutionProof + RuntimeGraph | `internal/execution/executor.go:83–109` | gated only — **never rendered** |

### Streaming reality per path

| Path | Provider call | Streams? | Reasoning? | Tool calls? | Interruptible? |
|---|---|---|---|---|---|
| Gated `$prompt`/`$hot`/bare | `executor.go:861` (:961 read-only) `Execute` | **No** | **No** | **No** | **No** (`gateway.go:96` `context.Background()`, no `beginOperation`) |
| `/ask` | `stream.go:288` `ExecuteStream` | Yes | Yes (`ReasoningHandler` `stream.go:235`) | n/a | Yes (`m.streamCancel` = op ctx) |
| `/build` fast-track | `commands.go:2704` `ExecuteStream` | Yes | Yes (sentinel extraction `:2821`) | Yes (`livePreviewChunkMsg` `:2885`) | Yes |
| `/build` per-task patch | `commands.go:3819`/`:4480`/`:4706`/`:5033` `Execute` | **No** | No handler | via buffered `ToolCallBuffer` | Yes (op ctx) |
| `/plan` | `internal/modes/plan/engine.go:398` `ExecuteStream` | Yes | Yes (`:391`) | n/a | Yes (registered cancel `:971`) |
| `/investigate` | `dispatcher.go:169` / `toolrunner.go:202` `Execute` | **No** | **No** | n/a | Yes |
| `/review` | none (deterministic) | — | — | — | Yes |

### What the UI shows while working

- **Loading dock** (`internal/ui/loading.go:166`): text priority = `execView.HumanStep()` (event-derived, gated) → `stageSnapshot()` (`stage.go`, legacy) → `m.shimmerText` (synthetic template). On the gated path during the provider call this is **"Analyzing"** — the last narrative sentence before `provider.response` (`narrative.go:81` `"provider.invoked" → "Analyzing"`). It stays frozen for the whole non-streaming call (minutes on queued cloud / 7B local).
- **`$inspect`** (`internal/ui/execution_telemetry.go:30–115`) renders `m.lastExecutionSnapshot` / `m.lastStrategyGraph` / `m.lastExecutionGraph` — all **nil on the gated path** (`multihotfix.go:534` is the only writer). A gated execution's `ExecutionProof` (`executor.go:83`) with full `RuntimeGraph` stage evidence is discarded after `executionResultUpdate` consumes `res.Content`/`Diff`/`Mutations`/`Completed`.

---

## B. Missing observability contracts

1. **No live provider stream contract on the runtime path.** `RuntimeExecutor` invokes `Execute` (non-streaming), sets no `ReasoningHandler`, no `Tools`. The canonical graph (`graph.go:319–332`) has only `BeginModel` → `CompleteModel` — no first-token, no streaming, no token-progress transition. The UI has the machinery to render all of it (`EventReasoningStream` → thinking panel, `stageStreaming` → token counter) but the runtime never emits it.

2. **No operation/telemetry/watchdog lifecycle on the gated path.** `runGatedLine` sets `m.executionResolving = true` but never `beginOperation` (only `gateway.go:321`, after the result). Consequences: `operationContext()` falls back to `context.Background()` → **Ctrl+C cannot cancel the provider call**; no `$inspect` record; no watchdog; no worker tracking; the provider call context (`gateway.go:97`) is detached from the interrupt system (`keys.go:788–803`, `cancelAllBackgroundContexts` `commands.go:6514`).

3. **Two event vocabularies, two projections, no reconciliation.** Mode engines never emit canonical events; the runtime path never emits mode/engine activity. The UI needs two subscription sets (`program.go:311` mode types vs `:321` runtime types) and two status sources (`execView` vs `stage.go` vs shimmer). A `/build` fast-track run shows **no execution timeline**; a `$prompt` run shows **no token/tool/reasoning progress**. Both claim to be "the" execution.

4. **Context discovery emits no telemetry on the runtime path.** `executor.go` reads targets directly (`os.ReadFile` `:804`, `:844`, `:932`) — it never routes through `retrieval.SetActivityLogger` / `SetEventLogger` (wired in `program.go:228–242`), so gated executions produce zero `EventEngineTelemetry`/`EventActivity` (no "search X: N results", no file-read records) even though the strategy's `ResolveFuzzy` does a real filesystem walk (`executor.go:1211`).

5. **`ExecutionProof` is produced but never surfaced.** The full stage/evidence/timeline record (`executor.go:83–109`) is the most truthful artifact in the system; the UI consumes only the projection (`res.Content`/`Diff`/`Mutations`/`Verification`/`Completed`) and drops the proof.

6. **Drop-prone bus + projection coupling.** The bus is non-blocking with a 256-event per-subscription buffer (`bus.go:12`, `:229–235`) — events are silently dropped on overflow. The `execView` and narrative advance *only* from events; a dropped `execution.finished` would leave `PhaseRunning` (`projection.go:201`). The terminal UI state is recovered from the result msg (`gateway.go:200`), but the *timeline* projection can be truncated under load.

7. **Local-model token estimates rendered as billed.** `stream.go:351–359` fabricates `len/4` counts for Ollama and marks them `usageKnown` (`update.go:2675–2687`); `status.FormatUsageContext` (`internal/ui/status/status.go:152`) renders them with no estimate marker.

---

## C. Root causes ranked by impact

1. **`RuntimeExecutor` calls the provider non-streaming with no reasoning/tool channel** (`executor.go:861`, `:961`). The single highest-impact cause: the canonical path — the one with the truthful timeline — is a **black box during the longest phase** (model invocation). Users see `Analyzing` frozen for minutes.
2. **Gated executions run outside the operation lifecycle** (`gateway.go:96–104`: `context.Background()`, no `beginOperation`). Causes the second symptom pair: **cannot interrupt** the work and **cannot inspect** it (`$inspect` empty, no watchdog, no telemetry).
3. **Two parallel execution vocabularies + two status surfaces.** Progress truth is fragmented; each mode shows a *different kind* of progress and none shows all of it.
4. **`ExecutionProof` never rendered** (`execution_telemetry.go` reads only legacy snapshots). The most complete record is dropped at the terminal.
5. **No engine-activity telemetry from the runtime path** (executor reads files directly). Context-discovery work is invisible.
6. **Bus drops + local-estimate token rendering** — secondary correctness/accuracy issues.

---

## D. Proposed architecture (minimal)

The change is a **single contract: make the runtime path stream the same provider truth the legacy path already streams, through the events the UI already renders.**

1. **Add a live provider-stream event to the canonical vocabulary** (extend `internal/events/events.go` alongside `EventReasoningStream`/`EventStreamUsage`): a content-delta event carrying the same `ProviderUsage` fields the legacy `streamUsageMsg` carries, plus reuse the existing `EventReasoningStream` (`events.go:39`, constructor `:514`) for reasoning. **No new renderer needed** — `handleDomainEvent` already projects `ReasoningPayload` (`model.go:2106`) and `StreamUsagePayload` (`:2093`) into the thinking panel and footer.

2. **Make `RuntimeExecutor` invoke via `ExecuteStream`** (`executor.go:861`, `:961`), set `req.ReasoningHandler` to publish `EventReasoningStream`, and emit the new stream event from the read loop (the `core/stream.Classifier` + `RuneBuffer` already exist and are used by `ingestLLMStream`, `stream.go:416`). Add a graph transition pair `BeginStreaming`/`StreamToken` between `BeginModel` and `CompleteModel` (`graph.go:321`/`:329`) so `execView`/`$inspect` show first-token and live token counts on the canonical timeline.

3. **Bind gated executions to the operation lifecycle**: in `runGatedLine` (`gateway.go:42`) call `beginOperation` (the appropriate kind), register the cancel via `registerBackgroundCancel`, and derive the provider ctx from `operationContext()` (`operation.go:219`). This makes Ctrl+C propagate (`keys.go:788` → `cancelAllBackgroundContexts`), enables the watchdog, and populates `$inspect` via `finalizeOperation` (`operation.go:240`).

4. **Render the proof**: in `handleReviewDollar` `$inspect` (`commands.go:6530`), also map `res.Proof.RuntimeGraph` → `renderExecutionGraph` (`execution_telemetry.go:149`) so gated executions get the same timeline the legacy paths get.

5. **Emit engine activity from the runtime path**: route `compileContext`/`invokeMutation` file reads (`executor.go:804`/`:844`/`:932`) through the existing activity/event loggers wired at `program.go:228` so context discovery shows in the ActivityTree.

6. **Optionally** gate the local estimate behind a visible `~` marker (`status/status.go:152`) — a small accuracy fix, independent of the above.

This is minimal because it reuses the existing bus, event vocabulary, projection, classifier, thinking panel, and `$inspect` renderer; nothing is renamed or redesigned. Steps 1–2 restore **token/reasoning/tool truth**, step 3 restores **interruptibility + inspectability**, steps 4–5 restore **evidence**.

---

## E. Example user experience after fix

User types `$hot fix the leak in @server.go` on a queued cloud model.

**Before (current):**

```
✻ Analyzing          ← frozen for ~2 min, no further truth
   Tip: …
── [runtime] model invoked: qwen2.5-coder:7b   ← activity line, then silence
```

Ctrl+C prints `Interrupted.` but the provider call keeps running in the background; its result pops in minutes later. `$inspect` says "no execution record".

**After:**

```
✻ Model ● waiting · 0 tok                      ← truthful provider state
✻ Model ● streaming · 84 tok                   ← first token arrived
  [runtime] context prepared: 3 channels
  Reading server.go                             ← event-derived step, live
  [reasoning]  ─  reviewing the leak site ...   ← reasoning via Ctrl+O / inline dim
  [tool] write_file @server.go (12KB) streaming ← tool arg deltas live
✻ Applying changes → Approved diff shown        ← approval gate
  Mutation applied and verified · 1.4k tok in / 3.1k tok out
```

Ctrl+C during the wait cancels the provider call synchronously (operation context), and `$inspect` after completion shows the full runtime graph (strategy → targets → context → model request → first-token → streaming → artifact → approval → mutation → verify) with per-stage timestamps from `ExecutionProof.RuntimeGraph`.

---

## Appendix — Key files

| Concern | File |
|---|---|
| Gated execution entry | `internal/ui/gateway.go`, `internal/execution/intent.go` |
| Runtime execution authority | `internal/execution/executor.go` |
| Canonical event graph | `internal/execution/graph/graph.go` |
| Domain event vocabulary + bus | `internal/events/events.go`, `internal/events/bus.go` |
| Event → UI projection | `internal/ui/model.go` (`handleDomainEvent`), `internal/ui/program.go` |
| Execution narrative / view state | `internal/presentation/narrative.go`, `internal/presentation/execution_projection.go` |
| Legacy streaming paths | `internal/ui/stream.go`, `internal/ui/commands.go` (fast-track) |
| Mode engines | `internal/modes/{plan,build,investigate,review}/engine.go` |
| Provider contract | `internal/ai/provider.go`, `internal/ai/reasoning.go` |
| Stage / telemetry (legacy) | `internal/ui/stage.go`, `internal/execution/telemetry.go` |
| Tool calls | `internal/execution/toolcalls.go` |
| Event translator (mode events only) | `internal/runtime/event_translator.go` |
| Audit / timeline (all events) | `internal/events/audit/writer.go`, `internal/events/timeline/timeline.go` |
