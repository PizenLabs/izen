# PHASE 4 — EXECUTION PROJECTION & CONTEXT CONTRACT REFACTOR

**Status:** Complete
**Branch:** `fix/engine`
**Scope:** Runtime event semantics, strategy-owned context policy, casual-chat routing, presentation reducer, single execution-view state.

The Runtime is the source of truth. The UI is a projection. This phase hardened that
boundary and made the projection a clean, testable reduction of the canonical event stream.

---

## 1. Before / After lifecycle

### Before (suspected + actual gaps)

```
execution.started
strategy.selected
target.resolved
context.prepared      ← generic compiler always emitted 3 channels ("hi" → 3 channels ~0 tokens)
model.invoked         ← emitted AFTER the provider call (with usage) — no explicit "response" boundary
artifact.produced
approval.required
mutation.completed
verification.completed
execution.finished
```

Problems:

- `context.prepared` was emitted unconditionally with the same generic channel set
  (`user_intent`, `explicit_targets`, `target_content`) for **every** strategy — a casual
  greeting was prepared with repository plumbing.
- There was no explicit "provider response received" event; `model.invoked` carried the
  post-call usage, so the stream could not distinguish "invocation began" from "response
  returned".
- On a provider failure the proof still appended an empty `ModelInvocation` record
  (zero model, zero tokens), claiming an invocation that never responded.
- Casual chat (`"hi"`) routed to `targeted_reasoning` — a repository-facing strategy name
  with a non-zero context expectation.

### After (required lifecycle, enforced)

```
execution.started
strategy.selected
target.resolved
context.prepared       ← strategy-owned; "hi" prepares 0 channels, 0 tokens
model.invoked          ← emitted when the invocation BEGINS (before the provider call)
provider.response      ← NEW: emitted ONLY on a successful response (authoritative usage)
artifact.produced      ← can never precede provider.response
approval.required      ← mutation path only
mutation.completed
verification.completed
execution.finished     ← always terminal, always last
```

Rules enforced:

1. **An artifact cannot exist before the provider response** — `artifact.produced` is
   emitted strictly after `provider.response` (see `internal/execution/executor.go`).
2. **No success event before the responsible operation succeeds** — a failed invocation
   emits `execution.failed` + `execution.finished(success=false)`, and nothing else.
3. **Failed execution never emits misleading success artifacts** — no
   `artifact.produced`, no `provider.response`, no `approval.required`, and the proof
   records **zero** invocations on failure.
4. **`execution.finished` is always terminal** — emitted exactly once, always last.

---

## 2. Event ownership

All lifecycle events are emitted **only by the RuntimeExecutor** at real runtime
boundaries. The presentation layer never synthesizes them.

| Event | Owner | Emitted when |
| --- | --- | --- |
| `execution.started` | RuntimeExecutor | `Execute` begins |
| `execution.strategy.selected` | RuntimeExecutor | after `Strategy.Select` (unconditional) |
| `execution.target.resolved` | RuntimeExecutor | per resolved target |
| `execution.context.prepared` | RuntimeExecutor | after strategy-owned context compile |
| `execution.model.invoked` | RuntimeExecutor | before the provider call (invocation start) |
| `execution.provider.response` **NEW** | RuntimeExecutor | after a successful provider response (usage) |
| `execution.artifact.produced` | RuntimeExecutor | after a successful response |
| `approval.required` | RuntimeExecutor | at the approval gate |
| `execution.mutation.started/completed` | RuntimeExecutor | mutation apply (Approve) |
| `execution.verification.completed` | RuntimeExecutor | real verifier result |
| `execution.finished` | RuntimeExecutor | every terminal path (success/failure/cancel/clarify/deterministic) |

New event added in `internal/events/events.go`:

```go
EventProviderResponse = "execution.provider.response"
ProviderResponsePayload{ RequestID, Model, TokenInput, TokenOutput }
```

`ModelInvokedPayload` semantics changed: it now records the invocation **start**; the
authoritative usage travels on `provider.response`. The `ExecutionProof.ModelInvocations`
still records authoritative usage (unchanged contract, verified by tests).

---

## 3. Context policy design (strategy-owned)

Introduced `strategy.ContextPolicy` on `ExecutionStrategyProfile`:

```go
type ContextPolicy string

const (
    ContextPolicyNone           ContextPolicy = "none"            // zero context: no workspace scan, no repo context, no file channels
    ContextPolicyTargetFileOnly ContextPolicy = "target_file_only" // exactly the resolved target file(s) + content
    ContextPolicyRepository     ContextPolicy = "repository"       // symbol graph, relevant files, dependency context
)
```

### Mapping

| Strategy | ContextPolicy | ContextKinds |
| --- | --- | --- |
| `direct_response` (casual chat) | `none` | `[]` (zero) |
| `human_clarification` | `none` | `[]` / `[user_intent]` |
| `direct_deterministic` | `target_file_only` | intent, targets, artifact |
| `targeted_mutation` | `target_file_only` | intent, targets, content, artifact, verify |
| `targeted_reasoning` | `target_file_only` | intent, targets, content |
| `repository_investigation` | `repository` | intent, prior, deps, constraints |
| `multi_file_planning` | `repository` | intent, deps, constraints |

### Where it lives

- `internal/execution/strategy/strategy.go` — type + field + `Policy()` accessor (zero
  value normalizes to `none`).
- `internal/execution/strategy/selector.go` — every branch sets its policy; the generic
  compiler no longer decides.
- `internal/execution/strategy/context.go` — `Compiler.Compile` returns an **empty
  envelope** for `ContextPolicyNone` (no user-intent item is synthesized).
- `internal/execution/executor.go` — `compileContext(profile, targets)` derives channels
  from `profile.Policy()`: `none` → `nil, 0` (no file reads, no workspace scan).

### Example

```text
Input:  hi
Intent: casual_chat
Strategy: direct_response
ContextPolicy: none
Result:  0 channels, 0 tokens, no file read, no workspace scan
```

```text
Input:  fix login handler in auth.go
Strategy: targeted_mutation
ContextPolicy: target_file_only
Result:  exactly auth.go content
```

---

## 4. Casual chat routing

`"hi"` no longer enters `ask` / `targeted_reasoning`.

- New strategy: `direct_response` (`internal/execution/strategy/strategy.go`).
- `selector.go` casual-chat branch → `DirectResponse`, `Intent: casual_chat`,
  `ContextPolicy: ContextPolicyNone`, `ContextKinds: nil`.
- Executor routes it through the read-only branch with the casual system prompt and
  produces a `response` artifact. The target-bound clarification guard exempts it
  (no target is required).
- Greetings never trigger workspace planning, never load repository context, and are
  zero-context end to end.

---

## 5. Projection architecture

```
Runtime Events (bus)
        |
        v
ExecutionProjection  (internal/presentation/execution_projection.go)
  - reduces the canonical stream into ONE ExecutionViewState
  - builds Debug (machine) + Human (narrative) timelines
        |
        v
UI (renderer depends ONLY on ExecutionViewState)
```

### ExecutionViewState (Part 5)

```go
ViewPhase: Idle | Running(step) | WaitingApproval | Completed | Failed
ExecutionViewState{ Phase, Step, Outcome, RequestID }
```

- `Running` always carries a human step (`Thinking...`, `Found target index.html`,
  `Generated change`, `Applying...`).
- A **terminal event always transitions** into `Completed` / `Failed` — success,
  failure, and clean cancellation are covered.
- After a terminal phase no running step can be rendered (stray events for the same
  request are ignored); a fresh `execution.started` resets the projection.

### Human projection (normal mode)

```text
Thinking...
✓ Found target index.html
✓ Generated change
Waiting for approval
✓ Applied
✓ Verified
✓ Completed
```

### Debug projection (developer mode)

```text
execution.started
strategy.selected: targeted_mutation
context.prepared: 2 channel(s), ~40 tokens
model.invoked: mock
provider.response: mock (12 in / 6 out)
artifact.produced: patch (index.html)
execution.finished: success=true (completed)
```

### UI integration (minimal, faithful)

- `model.execView *presentation.ExecutionProjection` — reset at each gated dispatch
  (`runGatedLine`).
- `handleDomainEvent` projects every lifecycle event into it (Part 5: the renderer
  never invents state).
- The loading-dock text for the gated path derives **only** from `m.execView.HumanStep()`
  (gated on the in-flight marker so a later legacy operation never inherits a stale
  execution step).
- The existing shimmer/busy machinery remains for legacy paths; the execution-truth path
  is consolidated onto the single projection state.

---

## 6. Tests proving correctness

### Runtime (internal/execution/execution_phase4_test.go)

| Test | Proves |
| --- | --- |
| `TestModelFailureProducesNoArtifact` | provider failure → no `artifact.produced`, no `provider.response`, no `approval.required`, proof records **0** invocations, file untouched |
| `TestArtifactOrderAfterProviderResponse` | event order `model.invoked < provider.response < artifact.produced`; artifact timestamp after response; proof carries authoritative usage |
| `TestExecutionFinishedAlwaysTerminal` | exactly one `execution.finished`, always the last lifecycle event, across approval / read-only / failure / reject paths |
| `TestDirectResponseZeroContext` | `"hi"` → `direct_response`, 0 context channels, no target, no file context leaked to the provider, terminal read-only completion |
| `TestProofGraphReflectsLifecycleOrder` | proof graph stage order (strategy → context → artifact; no mutate before approval) |

### Strategy (internal/execution/strategy/strategy_test.go)

| Test | Proves |
| --- | --- |
| `TestGreetingUsesDirectResponseStrategy` | `hi/hello/thanks/good morning` → `direct_response`, `Intent=casual_chat`, `ContextPolicy=none`, `ContextKinds == 0` |
| `TestSelectGreetingNeverPlans` | greetings never `multi_file_planning`, never demand repo/dependency context |
| `TestSelectDirectQuestionIsReadOnly` | direct questions → `direct_response` (zero-context), never workspace planning |

### Presentation (internal/presentation/execution_projection_test.go)

| Test | Proves |
| --- | --- |
| `TestReducerHumanNarrative` | the exact human timeline (Thinking → Found target → Generated → Waiting → Applied → Verified → Completed) |
| `TestReducerDebugProjection` | the developer diagnostics projection in order |
| `TestReducerTerminalEventAlwaysTransitions` | every terminal event → terminal phase; no stale running step |
| `TestReducerNoImpossibleStates` | no fabricated "Applied" without a mutation; no running step after terminal |
| `TestReducerResetOnNewExecution` / `TestReducerStaleRequestIgnored` | fresh execution resets; stale-request events ignored |

### UI (internal/ui/execution_projection_ui_test.go)

| Test | Proves |
| --- | --- |
| `TestGatedDispatchStartsExecutionProjection` | dispatch initializes the single projection; dock text derives from it |
| `TestGatedProjectionFollowsRuntimeEvents` | the dock follows `target.resolved` / `artifact.produced` / `approval.required` |
| `TestGatedProjectionTerminalClearsLoading` | a terminal event clears the shimmer and ends the projection terminal |
| `TestUIProjectionNeverRendersImpossibleStates` | the renderer can never resurrect a running step after a terminal event |

### Validation (all green)

```text
go build ./...            OK
go test ./... -count=1    OK (full suite)
go vet ./...              OK
golangci-lint run ./...   0 issues
go test -race (execution, strategy, presentation)  OK
```

---

## 7. Remaining risks

1. **Legacy UI paths are not yet fully consolidated.** The gated RuntimeExecutor path
   renders purely from `ExecutionViewState`, but legacy mode engines (`/plan`, `/build`
   fast-track, `/investigate`, `/review`) still drive their own busy flags
   (`agentRunning`, `streaming`, `pipelineRunning`, shimmer). As those paths migrate onto
   the RuntimeExecutor they should be re-rendered through the same single projection
   state. This is an incremental migration, not a boundary violation: the UI still never
   invents execution truth on the migrated paths.

2. **`model.invoked` semantics changed** (now invocation start, usage on
   `provider.response`). Existing consumers only read the model name; the proof's
   `ModelInvocations` still carries authoritative usage. Any new consumer must read
   `provider.response` for usage.

3. **Bus delivery order.** The bus delivers per-subscription via independent goroutines;
   cross-subscription arrival order is nondeterministic. Ordering assertions in tests use
   a single `SubscribeAll` (FIFO) subscription. Consumers that need strict ordering must
   subscribe once (or read `ExecutionProof`, which is strictly ordered).

4. **Repository-policy context is accounted but not yet gathered** in the executor's
   read-only path: `repository_investigation` / `multi_file_planning` name
   `dependency_evidence` / `repository_constraints` channels, but the executor currently
   passes only the request prompt (the forensic engine owns the real evidence gathering).
   The channel accounting is honest about what the strategy *claims*; wiring the actual
   evidence into the executor prompt is follow-up work.

5. **`ContextPolicy` default is `none`** for any strategy that omits it. New strategies
   must explicitly set a policy or they will silently compile zero context.
