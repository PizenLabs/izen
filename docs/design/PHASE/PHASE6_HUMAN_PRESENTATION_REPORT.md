# PHASE 6 — Human Presentation Architecture

**Status:** Complete · **Scope:** TUI presentation layer over the Phase 5
ExecutionGraph runtime · **Constraint honored:** runtime unchanged.

---

## 1. Problem: the presentation layer leaked runtime internals

Phase 5 delivered the correct runtime architecture (ExecutionGraph → canonical
lifecycle events → `ExecutionProjection`), but the TUI was still rendering
runtime internals directly:

1. **Runtime lifecycle events were rendered directly.** `handleDomainEvent`
   logged `[runtime] strategy selected: …`, `[runtime] provider response: …
   (tok in / tok out)`, `[runtime] artifact produced: …` into the human
   activity surface.
2. **Internal states were visible by default.** Strategy names, provider/model
   names, token counts, and event names all surfaced in the default view.
3. **Narrative steps were hardcoded.** Sentences like *"Preparing a targeted
   edit"*, *"Generated a proposed change"*, *"Drafted a plan"*,
   *"Thinking..."* were authored per strategy / per artifact kind inside the
   narrative reducer — static, predefined UI language instead of a pure
   function of the ExecutionGraph transitions that actually occurred.
4. **Structured artifacts were dumped as raw text.** A JSON plan contract was
   pushed to the viewport as its raw payload instead of being rendered through
   a semantic renderer.

---

## 2. Before / After architecture

### Before (Phase 5)

```
Runtime (execution/graph)                UI (internal/ui)
  events ───────────────────────────────────▶ handleDomainEvent
                                               ├── logActivity("[runtime] strategy selected: …")
                                               ├── logActivity("[runtime] provider response: …")
                                               ├── logActivity("[runtime] artifact produced: …")
                                               └── execView.Project(ev)
                                            renderExecutionNarrative()
                                               └── hardcoded steps from narrative (strategy/kind sentences)
```

- UI had direct knowledge of internal state names (`strategy`, `provider`,
  `tokens`) to render them.
- Narrative authored sentences by strategy name / artifact kind.
- Structured artifacts (JSON plans) pushed raw.

### After (Phase 6)

```
Runtime (execution/graph)  [UNCHANGED]
  events ────────────────────────────▶ presentation.ExecutionProjection
                                         ├── ExecutionViewState (+ Details metadata)
                                         └── ExecutionNarrative (transition-derived sentences)
                                              └── Frame(Visibility) ← interpretation

UI (internal/ui)
  renderExecutionLayered()
    └── renderExecutionFrame(execView.Frame(execVisibility))   ← visual only
         ├── NORMAL  : human narrative milestones + current step
         ├── EXPANDED: + strategy, context policy, model, tokens, duration, artifacts
         └── DEBUG   : + full machine event stream
```

- The UI consumes **only** `ExecutionViewState` / `ExecutionNarrative` /
  `ExecutionFrame` — never runtime internals directly.
- `[runtime] …` detail lines are gated behind the DEBUG layer
  (`logRuntimeDetail`), so internals are invisible by default.
- Narrative is derived from ExecutionGraph transitions; structured artifacts
  render through `ArtifactRenderer`.

---

## 3. Visibility model (three strict layers)

`presentation.Visibility` defines the layers; `ExecutionProjection.Frame(v)`
is the single interpretation point that decides what belongs in each layer.
The renderer formats the frame — it never decides.

| Layer   | Key     | Contains                                                                              | Excludes                                          |
| ------- | ------- | ------------------------------------------------------------------------------------- | ------------------------------------------------- |
| NORMAL  | default | Human narrative milestones + live current step ("Understanding request", "Inspecting index.html", …) | providers, strategies, tokens, event names        |
| EXPANDED| Ctrl+O  | NORMAL + `ExecutionDetails`: strategy, context policy (channels + tokens), model, token usage, duration, artifacts | raw event stream                                  |
| DEBUG   | Ctrl+O×2| EXPANDED + the full ordered machine event stream (`execution.started` … `execution.finished`) | —                                                 |

Ctrl+O cycles `NORMAL → EXPANDED → DEBUG → NORMAL` while a gated execution is
active; with no active execution it falls through to the existing thought-block
toggle. A fresh dispatch always resets to NORMAL.

Key invariant: **a terminal phase has no live step.** `Frame` flags the current
step only while `PhaseRunning` / `PhaseWaitingApproval`, so a completed or
failed execution can never render a spinner/current marker.

---

## 4. Narrative derived from ExecutionGraph transitions

`internal/presentation/narrative.go` now derives every human sentence from the
canonical transition that produced it (`transitionForEvent` +
`transitionNarrative`). There is no per-strategy or per-kind authoring left.

| ExecutionGraph transition | Human sentence |
| ------------------------- | -------------- |
| `execution.started`       | Understanding request |
| `strategy.selected`       | Understanding request |
| `target.resolved`         | Inspecting target (enriched with the real target: *Inspecting index.html*) |
| `context.prepared`        | Gathering context |
| `provider.invoked`        | Generating response |
| `artifact.produced`       | Preparing result |
| `approval.required`       | Waiting for approval |
| `mutation.started`        | Applying changes |
| `mutation.completed`      | Applied change to … |
| `verification.completed`  | Verified changes |
| `execution.finished`      | Completed / Cancelled / Failed |

Two properties fall out of the derivation:

- **No fake/static steps.** A sentence exists only for a transition that
  actually occurred — a partial graph yields a partial narrative. The reducer
  collapses consecutive identical sentences (e.g. two targets → one
  "Inspecting …" step) and records only machine records for non-human
  transitions.
- **Narrative changes with the graph.** Feeding more transitions lengthens the
  narrative; skipping a transition (e.g. no verification) never fabricates
  "Verified changes".

`ExecutionNarrative.Steps()` exposes each step with its `Transition` key, so
tests (and the renderer) can prove a step is graph-derived.

---

## 5. Artifact rendering model

`internal/presentation/artifacts.go` introduces the `ArtifactRenderer`
abstraction. Artifacts are classified into a semantic type and rendered by
that type — **never printed as raw JSON.**

| Semantic type (`ArtifactType`) | Runtime kinds | Renderer behavior |
| ------------------------------ | ------------- | ----------------- |
| `response`  | explanation, response | content as human text |
| `plan`      | plan            | parses the JSON plan into an overview + task list ("impact: …", "2 steps", "  file [strategy] — description") |
| `diff`      | patch, diff     | truthful header + diff body |
| `inspection`| investigation   | truthful header + findings |
| `verification` | verification | truthful header + verifier output |
| `error`     | error           | truthful header + error text |

Ownership split (requirement 5):

| Layer       | Responsibility                                        |
| ----------- | ----------------------------------------------------- |
| **Runtime** | execution truth (events) — unchanged                  |
| **Presentation** | interpretation: classify artifact kind → semantic type, parse structured payloads, decide what belongs in each visibility layer (`Frame`) |
| **Renderer**| visual output only — formats an already-typed `ArtifactView`/`ExecutionFrame`, zero business logic |

`RenderArtifact(kind, target, content)` is the classify-and-render convenience
the UI terminal path uses, so a plan artifact reaching the read-only terminal
is rendered as a semantic task list, never as raw JSON.

---

## 6. Tests proving the separation

### `internal/presentation/narrative_test.go`
- `TestNarrativeTransitionDerivation` — every canonical transition derives its
  expected sentence.
- `TestNarrativeNoFakeStaticSteps` — a partial graph never produces steps for
  transitions that did not occur.
- `TestNarrativeChangesWithGraphState` — narrative length grows with the actual
  graph.
- `TestNarrativeStepsCarryTransitions` — steps carry their derivation key.
- `TestNarrativeMachineSeparated`, `TestNarrativeTerminalSentences`,
  `TestNarrativeDeterministic` — machine/human separation, terminals, determinism.

### `internal/presentation/layers_test.go`
- `TestFrameNormalHidesInternals` — NORMAL frame carries no provider names,
  event names, or token counts; Details/Events empty.
- `TestFrameExpandedShowsDetails` — EXPANDED frame carries strategy, context
  policy, model, tokens, duration, artifacts.
- `TestFrameDebugShowsEvents` — DEBUG frame carries the full lifecycle stream.
- `TestFrameTerminal` — terminal frames have no live current step.
- `TestVisibilityString`.

### `internal/presentation/artifacts_test.go`
- `TestJSONPlanArtifactUsesSemanticRenderer` — a JSON plan renders as a task
  list; raw JSON (`{`, `atomic_tasks`, `"task_id"`) never appears.
- `TestUnparseablePlanNeverDumpsRawJSON` — unparseable plans render a notice.
- `TestClassifyArtifact`, `TestDiffArtifactSemanticRender`,
  `TestResponseArtifactSemanticRender`, `TestRenderArtifactConvenience`.

### `internal/presentation/execution_projection_test.go`
- `TestReducerHumanNarrative` — the full canonical transition timeline.
- `TestReducerAccumulatesDetails` / `TestReducerDetailsSurviveTerminal` —
  EXPANDED metadata accumulation, including across terminal reassignment.
- `TestReducerDebugProjection` — machine event diagnostics.

### `internal/ui/human_presentation_test.go` (end-to-end through the model)
- **Human view does not contain provider names** — `TestHumanViewDoesNotContainProviderNames`.
- **Expanded view contains runtime metadata** — `TestExpandedViewContainsRuntimeMetadata`.
- **Debug view contains lifecycle events** — `TestDebugViewContainsLifecycleEvents`.
- **JSON artifact uses semantic renderer** — `TestJSONPlanArtifactUsesSemanticRenderer`.
- **Narrative changes according to actual graph state** — `TestNarrativeChangesAccordingToGraphState`.
- **No fake/static steps** — `TestNoFakeStaticSteps`.
- **Ctrl+O cycles the layers** — `TestCtrlOCyclesVisibility`.

---

## 7. Files changed

| File | Change |
| ---- | ------ |
| `internal/presentation/layers.go` (new) | `Visibility`, `ExecutionFrame`, `ExecutionDetails`, `NarrativeStep` |
| `internal/presentation/artifacts.go` (new) | `ArtifactType`, `ClassifyArtifact`, `ArtifactView`, `ArtifactRenderer`, `DefaultArtifactRenderer`, `RenderArtifact` |
| `internal/presentation/narrative.go` | narrative derived from ExecutionGraph transitions; `Steps()`; removed per-strategy/per-kind authoring |
| `internal/presentation/execution_projection.go` | `ExecutionViewState.Details` accumulation; `Frame(Visibility)`; terminal phases keep details; live step flagging |
| `internal/ui/model.go` | `execVisibility` field; `logRuntimeDetail` gates `[runtime]` lines behind DEBUG; layered render call site |
| `internal/ui/keys.go` | `cycleExecVisibility` (Ctrl+O cycles NORMAL→EXPANDED→DEBUG) |
| `internal/ui/loading.go` | `renderExecutionLayered`, `renderExecutionFrame`, `renderExecutionDetails`, `renderExecutionDebug` |
| `internal/ui/gateway.go` | `pushArtifact` routes terminal artifacts through `ArtifactRenderer`; resets visibility at dispatch |
| tests | narrative/layers/artifacts/projection + UI `human_presentation_test.go`; existing narrative pins updated |

**Verification:** `go build ./...`, `go vet`, `golangci-lint run ./...`
(0 issues), and the full `go test ./...` suite pass, including `-race` for the
presentation and UI packages.
