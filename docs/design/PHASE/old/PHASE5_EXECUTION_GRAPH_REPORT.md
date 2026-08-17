# PHASE 5 — EXECUTION GRAPH CONSOLIDATION & CLAUDE-LIKE HUMAN EXPERIENCE

**Status:** Complete
**Branch:** `fix/engine`
**Scope:** Runtime-owned ExecutionGraph, ExecutionNarrative, ContextBudget, multi-file mutation correctness, TUI projection, architectural enforcement.

The Runtime is the source of truth. The UI is a projection. This phase made the
execution lifecycle an **explicit, runtime-owned graph** whose transitions ARE
the canonical events, added a deterministic human **narrative layer**, gave the
strategy a real **context budget**, made multi-file mutation a single **atomic
transaction**, and added **machine-enforced** regression guards.

---

## 1. Before / After architecture

### Before (implicit lifecycle)

```
RuntimeExecutor.Execute — imperative, linear
  strategy → targets → context → model → artifact → approval
  → emits events inline (x.emit) at hand-written points
  → Proof.Graph built by ad-hoc append("...GraphStep...")
```

Problems:

- The lifecycle was implicit in the control flow — there was no first-class
  runtime graph; progress was only the `ExecutionProof.Graph []GraphStep`
  projection built by hand.
- Events were emitted at hand-written points inside the executor, not derived
  from a lifecycle record — nothing prevented a future path from emitting an
  event without a corresponding stage.
- The UI could only infer progress from those events; there was no
  deterministic human narrative.

### After (explicit runtime-owned ExecutionGraph)

```
Input
 ↓
IntentGateway (unconditional Strategy.Select)
 ↓
RuntimeExecutor.Execute
 ↓
runtimegraph.Graph (internal/execution/graph)
   user_intent → strategy_selection → target_resolution → context_compilation
   → model_invocation → artifact_validation → approval_gate
   → mutation_transaction → verification → completion
   every transition EMITS the canonical event
 ↓
ExecutionProof (folds graph stage evidence + context decisions + transaction)
 ↓
Presentation projection (ExecutionNarrative + ExecutionViewState)
```

Every canonical lifecycle event is generated **by a graph transition** — there
is no other emitter (machine-enforced, see §7). The UI never infers progress
and never authors progress text; it renders the `ExecutionProjection`.

---

## 2. Graph ownership

`internal/execution/graph/graph.go` is the runtime-owned execution graph:

| Concern | Owner |
| --- | --- |
| Stage topology (10 stages, deterministic chain) | `graph.New` |
| Dependency edges (sequential chain, parallel-ready DAG scheduler) | `Graph.Edges` / `Runnable()` / `Ready()` |
| Stage lifecycle (pending/running/complete/skipped/failed/cancelled) | `Graph.Begin/Complete/Skip/Wait/Fail` |
| Whole-graph phase (idle/running/awaiting_approval/completed/failed/cancelled) | `Graph.Phase` |
| Event generation from transitions | `Graph.CompleteStrategy/CompleteTarget/CompleteContext/BeginModel/CompleteModel/CompleteArtifact/WaitApproval/BeginMutation/CompleteMutation/CompleteVerification/CompleteExecution/FailExecution/CancelExecution` |
| Evidence folding into `ExecutionProof` | `Graph.Evidence()` → `Proof.RuntimeGraph` + `Proof.Graph` projection |
| Single execution authority | the RuntimeExecutor drives the graph; the graph is the only event emitter |

Design notes:

- **No fake stages.** A stage that is never reached is `pending` and contributes
  no evidence; a stage the strategy cleanly skips is `skipped` with a reason.
  Neither emits an event.
- **Sequential now, parallel-ready.** Every stage declares its dependencies;
  `Runnable()` returns the topological frontier. Today all topologies are
  chains (frontier ≤ 1); a future parallel topology only adds DAG edges.
- **Events from transitions, terminal-aware.** `CompleteExecution` /
  `FailExecution` / `CancelExecution` are terminal transitions and always emit
  `execution.finished` last.

---

## 3. Migration status (Part 2)

| Path | Status |
| --- | --- |
| `$prompt` | ✅ migrated (IntentGateway → RuntimeExecutor → ExecutionGraph) |
| `$hot` | ✅ migrated (directives route through `runHotExecution` → `runGatedLine`; legacy `$hot` fast-track in `handleMessageContent` **removed** — no duplicate path) |
| multi-file `$hot` | ✅ runtime capability migrated (per-target invocation, single transaction apply, rollback) |
| bare text | ✅ migrated (routes through `runGatedLine` → gateway) |
| `/ask` streamCmd | ⏳ **backlog** — legacy direct-provider streaming path in `internal/ui/stream.go` remains; the gated RuntimeExecutor path covers read-only/direct-response through the graph |
| `/build` | ⏳ **backlog** — legacy mode-engine execution in `internal/ui/commands.go` remains |
| `/commit` | git-boundary command (operates on the git engine, not the provider); unchanged — it is not a provider execution path |

The architectural enforcement tests are scoped to the **migrated** (gated) path,
so the boundary is guarded even while legacy UI internals remain.

---

## 4. ExecutionNarrative (Part 3)

`internal/presentation/narrative.go` — `ExecutionNarrative` separates MACHINE
events from the HUMAN narrative:

- **Deterministic**: the same event always yields the same sentence.
- **No LLM**: narration is a pure string mapping, never a provider call.
- **UI reads, never authors**: the TUI renders the projection; it does not
  type progress text.

Human sentences (Claude-style):

```text
Understanding request
Preparing a targeted edit
Inspecting index.html
Gathering context (2 channels)
Thinking...
Generated a proposed change
Waiting for approval
Applying changes
Applied change to index.html
Verified the change
Completed / Cancelled / Failed
```

Machine records stay raw (`execution.context.prepared: 2 channel(s), 40 tokens`).
`ExecutionProjection` embeds the narrative and exposes
`HumanTimeline() / DebugTimeline() / HumanStep()`.

---

## 5. ContextBudget (Part 4)

`strategy.ContextBudget` extends the strategy-owned context contract with a real
allowance:

```go
type ContextBudget struct {
    Policy     ContextPolicy
    Tokens     int           // accounting budget (provider usage stays authoritative)
    MaxFiles   int
    Evidence   []ContextKind // evidence requirements before reasoning
    Escalation bool
}
```

| Strategy | Policy | Tokens |
| --- | --- | --- |
| casual_chat (`direct_response`) | `none` | 0 |
| single-file edit (`targeted_mutation` / `targeted_reasoning`) | `target_file_only` | 4000 |
| repository investigation (`repository_investigation` / `multi_file_planning`) | `repository` | 16000 |

- The **compiler explains every inclusion** — `contextInclusionReason(kind)` is
  the single explanation source, used both by the compiler and the proof.
- **ExecutionProof stores context decisions** — `Proof.ContextDecisions`
  records the policy, the budget, and the per-item inclusion reasons.
- **Token usage must match provider usage** — the budget is an *accounting*
  budget; authoritative tokens always come from `provider.response` usage
  (enforced by existing Phase 4 tests + `TestExecutorNeverExecutesWithoutStrategy`).

---

## 6. Multi-file mutation correctness (Part 5)

`RuntimeExecutor` now performs **one bounded invocation per resolved target**,
then the approval gate holds ALL patches:

```
Resolve files → generate per-target changeset → approval → ONE MutationSet
transaction → apply ALL changes → verify all affected files → commit / rollback
```

- `Approve` applies every held patch inside a **single MutationSet transaction**;
  any apply failure rolls the WHOLE transaction back (no partial change survives).
- `ExecutionProof` now carries: `AffectedFiles`, `DiffSummary`, `TransactionID`
  (the MutationSet ID), and the real `Verification` results.
- No synthetic verification: `verification.completed` only fires from the real
  verifier run.
- Event ordering fixed to the required lifecycle:
  `approval.required → mutation.completed → verification.completed →
  execution.finished`.

---

## 7. Architectural enforcement tests (Part 7)

`internal/architecture/execution_invariants_test.go` (AST-level) + runtime
enforcement:

| # | Rule | Test |
| --- | --- | --- |
| 1 | UI cannot call provider | `TestUICannotCallProviderOnExecutionPath` — `gateway.go` has zero `provider.Execute/ExecuteStream`; submits via `gateway.Gate → executor.Execute` |
| 2 | UI cannot mutate workspace | `TestUICannotMutateWorkspaceOnExecutionPath` — `gateway.go` has no PatchManager/MutationSet/`ApplyContext` call sites |
| 3 | Every execution has ExecutionProof | `TestEveryExecutionHasExecutionProof` — all terminal paths (mutation/read-only/direct/failure/deterministic/clarify) return a non-nil proof with runtime-graph evidence |
| 4 | Every verification requires a real verifier | `TestEveryVerificationRequiresRealVerifier` — read-only never fabricates verification; mutation verification matches the real verifier steps |
| 5 | Every lifecycle transition comes from graph | `TestLifecycleEventsGeneratedOnlyFromGraph` — the 13 lifecycle constructors are invoked nowhere except `graph/graph.go` |
| 6 | Every user action crosses IntentGateway | `TestEveryUserActionCrossesIntentGateway` (gate before execute, source order) + `TestExecutorNeverExecutesWithoutStrategy` (unconditional classification) |
| 7 | No duplicate execution paths | `TestRuntimeExecutorSingleCompositionBinding` (single `NewRuntimeExecutor` in compose root) + `TestNoDuplicateHotfixExecutionPath` (no legacy `$hot`→`runBuildCmd`) |

---

## 8. Deliverables

1. **Runtime graph implementation** — `internal/execution/graph/` (+ tests).
2. **Legacy path migration** — `$hot` unified through the gateway; multi-file
   capability migrated; `/build`, `/ask` documented as backlog.
3. **ExecutionNarrative** — `internal/presentation/narrative.go` (+ tests).
4. **ContextBudget** — `strategy.ContextBudget` + proof context decisions.
5. **Updated TUI projection** — `renderExecutionNarrative()` panel renders the
   projection, never raw events (`TestNarrativePanelRendersProjection`).
6. **Enforcement tests** — §7.
7. **This report.**

### Validation

```text
go build ./...            OK
go test ./... -count=1    OK (full suite)
go vet ./...              OK
golangci-lint run ./...   0 issues
go test -race (execution, graph, strategy, presentation, architecture, handlers)  OK
```

---

## 9. Remaining risks

1. **Legacy UI provider paths remain** (`/ask` streamCmd, `/build` mode engine).
   The migrated (gated) path is enforced, but the legacy internals in
   `internal/ui/{stream,commands,multihotfix}.go` still call `m.provider`
   directly. Migrating them onto the RuntimeExecutor (a `/build`/`/ask` runtime
   strategy) is the next phase's work; the enforcement tests are scoped to the
   migrated path so they stay green while that migration proceeds.

2. **Graph topology is fixed** (10-stage chain). The strategy selects which
   stages complete vs skip; it cannot reorder or insert stages. Parallel
   execution is scheduler-ready (`Runnable`/`Ready`) but no parallel topology
   is wired yet.

3. **`ContextBudget.Evidence` is recorded but the executor's read-only path does
   not yet gather repository evidence** for `repository`-policy strategies (the
   forensic engine owns that); the channel accounting and proof decisions are
   honest about what the strategy claims.

4. **Bus delivery order** remains nondeterministic across per-type
   subscriptions; order-sensitive consumers must subscribe once
   (`SubscribeAll`) or read `ExecutionProof` (strictly ordered).

5. **`model.invoked` carries zero tokens** (invocation start); authoritative
   usage travels on `provider.response`. New consumers must read the response
   event for usage.
