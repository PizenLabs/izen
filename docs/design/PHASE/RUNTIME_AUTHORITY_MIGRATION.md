# IZEN RUNTIME AUTHORITY MIGRATION — REPORT

Phase 2 — migration executed on top of the forensic audit (RUNTIME_FORENSIC_AUDIT.md).

Date: 2026-08-15
Status: `go build ./...`, `go test ./...` (race-clean on changed packages),
`go vet ./...`, `golangci-lint run ./...` → all 0 issues.

---

## 1. Before architecture diagram (as-built)

```
                    User
                     |
                     v
              +----------------------+
              |   UI (TUI monolith)   |  ~19k LOC internal/ui
              |  19k LOC: provider,   |
              |  prompts, models,     |
              |  patches, mutations,  |
              |  verification,        |
              |  approval, rendering  |
              +----------------------+
                |      |       |
        provider.Execute  PatchManager  MutationSet.Commit
        (12 sites)        (5 sites)     (11 sites)
                |      |       |
                v      v       v
        +----------------------------+
        |   execution.Engine (utils)  |   <- UI drives the engine
        +----------------------------+
                |
                v
        +----------------------------+
        | Runtime command handlers    |
        | (SubmitPrompt/Approve/...   |
        |  = telemetry only, fake     |
        |  InMemoryApprover)          |
        +----------------------------+
```

The UI owned everything. The runtime command layer was a parallel
"APPLICATION-LAYER COMMAND RECORD" that fabricated approval records and plan
staging.

## 2. After architecture diagram (as migrated)

```
                    User
                     |
                     v
          Intent Resolver (router.Router / strategy.Select)
                     |
                     v
          Strategy Engine (strategy.Select -> profile)
                     |
                     v
          +------------------------------------------+
          |  Execution Runtime                        |
          |  internal/execution.RuntimeExecutor       |
          |  .Execute(req) -> ExecutionResult+Proof   |
          |  .Approve(id) / .Reject(id)               |
          |                                           |
          |  owns: provider invocation, context       |
          |  compilation, patch creation, MutationSet |
          |  lifecycle, PatchManager.Apply, Verifier  |
          +------------------------------------------+
                     |
                     v
           Evidence Store (ExecutionProof, per execution)
                     |
                     v
          Canonical RuntimeEventBus
          (execution.started/strategy.selected/target.resolved/
           context.prepared/model.invoked/artifact.produced/
           mutation.started/mutation.completed/verification.completed/
           execution.finished/execution.failed)
                     |
                     v
          UI (internal/ui) — pure renderer on migrated path
          submit requests -> executor; render events + results
```

Runtime command handlers (`approve_patch` / `reject_patch`) route through the
RuntimeExecutor and now perform **real** mutations (Rule 3: no fake states).

## 3. Ownership changes

| Concern | Before | After |
|---|---|---|
| Provider invocation (`$prompt` targeted mutation) | UI `m.provider.Execute` (commands.go:3848/3912, multihotfix.go, ...) | **RuntimeExecutor** (`internal/execution/executor.go` `invokeMutation`) |
| Strategy selection | UI `strategy.Select` called in UI (`engine_first.go`) | RuntimeExecutor `selectStrategy` (same deterministic selector, owned by runtime) |
| Context compilation | UI `fastTrackFileContext`, per-call prompt assembly | RuntimeExecutor `compileContext` (bounded, channel-accounted) |
| Patch creation (response → patch) | UI `proposals.go`, `update.go` extractors | RuntimeExecutor `invokeMutation` + `compileDiff` (changeset) |
| Mutation lifecycle (Begin/Apply/Commit/Rollback) | UI `m.execEng.BeginTransaction/CommitTransaction/RollbackTransaction` (11 sites) | RuntimeExecutor `Approve`/`Reject` (PatchManager + MutationSet owned by runtime) |
| Verification | UI shells `go test` (commands.go:5827/5909) | RuntimeExecutor → `Verifier.RunAll` + `VerificationCompleted` event |
| Approval authority | UI keys.go gates + fake `InMemoryApprover` | UI keys route through RuntimeExecutor; handlers route through executor; no fabrication |
| Execution evidence | UI-local `ExecutionProof` (execution_proof.go) | Runtime-owned `execution.ExecutionProof` returned by every Execute |
| Canonical events | Partial, telemetry-heavy | Full lifecycle stream (`internal/events/events.go`) |

## 4. Removed responsibilities from the UI

On the migrated `$prompt` targeted-mutation path the UI no longer:

1. Calls `m.provider.Execute` / `ExecuteStream` (runtime owns it).
2. Builds the model request (system prompt, messages, model) — runtime owns it.
3. Selects the strategy or compiles the context envelope — runtime owns it.
4. Parses the model response into a patch — runtime owns it.
5. Applies patches / commits / rolls back MutationSets — runtime owns it.
6. Runs verification — runtime owns it.
7. Fabricates approval outcomes — approval now requires a real pending mutation.

Legacy paths (`streamCmd` bare /ask, `/commit`, multi-file `$hot`, legacy
`$hot`/`proposeBuildPatch`) still contain UI-owned provider/mutation calls.
They are the documented remaining risk (section 6).

## 5. New runtime contracts

### 5.1 `internal/execution.RuntimeExecutor` (`internal/execution/executor.go`)

```
type RuntimeExecutor struct{ ... }   // self-contained execution authority

func NewRuntimeExecutor(root, cfg, provider, bus, langID) *RuntimeExecutor
func (x *RuntimeExecutor) Execute(ctx, req ExecuteRequest) (*ExecutionResult, error)
func (x *RuntimeExecutor) Approve(ctx, patchID) (*ExecutionResult, error)
func (x *RuntimeExecutor) Reject(ctx, patchID, reason) (*ExecutionResult, error)
func (x *RuntimeExecutor) SetVerifier / SetAuthorization / SetProvider
func (x *RuntimeExecutor) PendingPatchIDs() []string
```

- `Execute` runs: strategy → target resolution → context → model → artifact →
  approval gate. It emits `ExecutionStarted`, `StrategySelected`,
  `TargetResolved`, `ContextPrepared`, `ModelInvoked`, `ArtifactProduced` and
  returns `PendingPatchID` (or `ClarificationRequired`).
- `Approve` runs: fresh `MutationSet` → `PatchManager.ApplyContext` (real
  write + verification gate) → `Verifier.RunAll` → commit. Emits
  `MutationStarted`, `MutationCompleted`, `VerificationCompleted`,
  `ExecutionFinished`.
- `Reject` rolls back the held boundary and emits `ExecutionFinished(cancelled)`.
- Every result carries an `execution.ExecutionProof` (strategy, targets, graph
  steps, model invocations with provider usage, mutation evidence, real
  verification report, terminal outcome).

### 5.2 Canonical runtime lifecycle events (`internal/events/events.go`)

`execution.started`, `execution.strategy.selected`, `execution.target.resolved`,
`execution.context.prepared`, `execution.model.invoked`,
`execution.artifact.produced`, `execution.mutation.started`,
`execution.mutation.completed`, `execution.verification.completed`,
`execution.finished` (plus the existing `execution.failed`).

Rule 3 compliance: no "verified" is rendered without a `VerificationCompleted`
event carrying the real `Verifier` result; no token count is rendered without
the provider's `Usage` record.

### 5.3 Runtime command handlers (real execution)

- `approve_patch` / `reject_patch` route through `RuntimeExecutor` when wired.
  The fake `InMemoryApprover` default fabrication was removed (a nil approver /
  unknown patch now fails deterministically).
- `submit_prompt` no longer fabricates a staged plan (fake `EventPlanStaged`
  from newline splitting removed).

## 6. Remaining risks

1. **UI still owns provider invocation on non-migrated paths**: `stream.go:288`
   (bare /ask chat), `agents.go:770` (/commit), `multihotfix.go:206,236`
   (multi-file `$hot`), `commands.go:4509,4735,5062` (`/build` per-task
   proposal), `commands.go:2733` (fast-track), `commands.go:6990,7074`
   (diagnose / ask-handoff). These still violate Rule 1 and are the next
   migration batch.
2. **UI still owns mutation execution on legacy paths**: `applyHotfixPatch`,
   `applyProposalCmd`, `applyMultiHotfixGraph` still drive
   `m.execEng.Patches.ApplyContext` and the transaction helpers. The migrated
   `$prompt`-targeted path and runtime approval path are clean; legacy `$hot`
   and `/build` are not.
3. **Bare input does not yet reach the strategy layer**: bare free-form input
   in non-ask modes goes through the hybrid intent router, not
   `strategy.Select`. Requirement "bare input reaches strategy layer" is
   partially met (the engine-first layer is reached by `$prompt`; the strategy
   selector is the runtime's own entry).
4. **Duplicate classifiers remain**: `handlers.ClassifyIntent`,
   `investigate.ClassifyIntent`, the `router` semantic classifier, and
   `gateway.CompressPrompt` still coexist. Consolidation into one intent
   resolver is not yet done.
5. **`$inspect` renders the UI-side strategy graph**, not yet the runtime
   `ExecutionProof.Graph`. The runtime proof is produced; wiring `$inspect` to
   it is future work.
6. **Multi-file `$hot` and `/build` fast-track still run UI-side execution
   graphs.** The RuntimeExecutor currently covers single-target mutations.

## 7. Tests proving authority migration

| Test | Proves |
|---|---|
| `internal/execution/executor_test.go` — `TestRuntimeExecutor_TargetedMutationFlow` | Runtime owns provider + mutation + verification; execution stops at approval gate; approval mutates the file, commits the MutationSet, and fires the canonical lifecycle events; verification evidence comes from the real verifier |
| `.../RejectDoesNotMutate` | Reject rolls back with zero disk mutation |
| `.../StrategyResolvesTargetFromPrompt` | Strategy (target resolution) runs inside the runtime |
| `.../NoProviderFailsDeterministically` | No fabricated model invocation; `ExecutionFailed` fires; file untouched |
| `.../ApprovalWithoutPendingFails` | Rule 3: approval of a non-pending patch fails — no fake mutation |
| `internal/runtime/handlers/executor_integration_test.go` — `TestApprovePatchHandler_RoutesThroughExecutor` | The canonical `approve_patch` command drives a REAL mutation through the runtime (file changed, lifecycle events fired, double-approve fails) |
| `.../RejectPatchHandler_RoutesThroughExecutor` | Rejection terminates with zero disk mutation |
| `internal/runtime/integration_test.go` — `TestAskPlanBuildFlow` | The runtime never fabricates approvals or plans; non-pending approvals fail; ledger records only real failures |
| `internal/ui/engine_first_test.go` — `TestEngineFirstPromptRoutesThroughExecutor` | `$prompt` targeted mutation routes through the executor: the UI never calls the provider (callCount == 1 inside the executor), a pending patch id is returned, and no file mutation occurs before approval |
| `internal/ui/hotfix_contract_test.go` (existing, still passing) | Legacy `$hot` behavior unchanged after the keys.go approval-gate split |

## 8. Migration rules compliance (as migrated)

- **Rule 1** (UI must not call provider/patch/mutation): MET on the migrated
  `$prompt`-targeted path and runtime approval path; NOT met on legacy
  chat/hotfix/build paths (remaining risk).
- **Rule 2** (ExecutionProof per execution): MET — the runtime returns an
  `execution.ExecutionProof` for every execution it owns.
- **Rule 3** (no fake states): MET — fabricated approval records and fabricated
  plan staging removed; verification/token evidence only from real runtime
  results.
- **Rule 4** (one execution path): PARTIAL — the migrated path is single-runtime;
  duplicate classifiers and the dual dispatch on other paths remain.

## 9. Files changed

```
internal/events/events.go            canonical runtime lifecycle events
internal/execution/executor.go       RuntimeExecutor (NEW)
internal/execution/executor_test.go  executor tests (NEW)
internal/execution/mutation.go       OutcomeFailed vocabulary
internal/execution/patch.go          (none — apply already verifier-gated)
internal/runtime/command.go          (none)
internal/runtime/handlers/handlers.go       real approval via executor, removed fake plan staging
internal/runtime/handlers/handlers_test.go  contract updates
internal/runtime/handlers/executor_integration_test.go  runtime-approval proof (NEW)
internal/runtime/compose/compose.go  RuntimeExecutor wired into Application
internal/runtime/integration_test.go no-fake-state contract update
internal/presentation/bridge_test.go no-fake-state contract update
internal/ui/model.go                 executor field, lifecycle-event projection, dedup
internal/ui/program.go               executor injection, lifecycle subscriptions
internal/ui/engine_first.go          $prompt targeted -> RuntimeExecutor
internal/ui/engine_first_test.go     authority-migration proof (NEW)
internal/ui/runtime_executor.go      UI executor bridge: cmds + messages (NEW)
internal/ui/update.go                executionResultMsg handler
internal/ui/keys.go                  approval gate routes through executor
internal/ui/presentation_events_test.go dedup contract update
RUNTIME_FORENSIC_AUDIT.md            Phase 1 evidence report (NEW)
```

---

## Phase 3 — Execution-driven runtime (unified IntentGateway)

Extends the migration from "mode-driven CLI" to "execution-driven agent runtime".

### The unified flow (now the single execution path)

```
User Input (bare text / $prompt / $hot)
    |
    v
IntentGateway.Gate()          internal/execution/intent.go
  directive stripping + Strategy.Select (UNCONDITIONAL)
    |
    v
ExecuteRequest { Strategy profile, Targets, Prompt, Mode(label) }
    |
    v
RuntimeExecutor.Execute/Approve/Reject     internal/execution/executor.go
  (owns provider invocation, context, patch creation,
   MutationSet lifecycle, PatchManager.Apply, Verifier)
    |
    v
ExecutionProof + canonical RuntimeEvents -> UI renders (pure projection)
```

### Violations fixed in Phase 3

1. **User intent no longer routes through UI modes before execution.** Bare
   text, `$prompt` and `$hot` all cross the IntentGateway; the mode never
   selects the execution path.
2. **`$prompt` is an execution request, not a message command.** It no longer
   transitions to /ask, no longer triggers the compressor fast-track, and no
   longer invokes a hidden /build. It produces an ExecuteRequest.
3. **Mode transitions are no longer orchestration.** Execution directives
   (`$prompt`/`$hot`) never switch modes. `dispatchASTIntent` only transitions
   the presentation mode on an explicit `/workspace` marker or legacy
   directive alignment — never for execution directives.
4. **The UI no longer decides when execution begins.** `handleInput`'s bare
   text tail routes through `runGatedLine`; the runtime decides the path.
5. **Execution lifecycle events are the single source of progress.** The
   executor emits `execution.started → strategy.selected → target.resolved →
   context.prepared → model.invoked → artifact.produced → approval.required →
   mutation.completed → verification.completed → execution.finished` and the
   UI renders them.

### New runtime contracts

- `internal/execution.IntentGateway` (`intent.go`): `Gate(ctx, line)` →
  `(ExecuteRequest, IntentResolution, error)`; `SelectStrategy` runs
  `Strategy.Select` unconditionally.
- `ExecuteRequest.Strategy *strategy.ExecutionStrategyProfile`: the gateway
  always attaches the profile; the executor consumes it (falling back to its
  own unconditional `Strategy.Select`).
- Read-only strategies (`targeted_reasoning`, `multi_file_planning`,
  `repository_investigation`) now execute in the runtime: one bounded
  invocation returns an artifact (`explanation` / `plan` / `investigation`),
  no mutation path, no approval surface.
- `approval.required` (`EventApprovalRequired`): the canonical runtime
  approval-gate event, distinct from the patch-engine `approval.requested`.
- `ProviderResponse → Artifact → ExecutionResult → UI`: the provider response
  is owned by the executor (`invokeMutation`/`invokeReadOnly`), parsed into an
  artifact inside the runtime, returned as `ExecutionResult`, rendered by the
  UI. The raw response never reaches the UI.

### Canonical target resolution (single resolver, bounded UI surface)

`strategy.Select` — reached via `IntentGateway.SelectStrategy`
(`internal/execution/intent.go:62`) and `IntentGateway.Gate` — is the ONE
canonical target-resolution authority. Every execution request (`bare text`,
`$prompt`, `$hot`) has its target set determined by `Strategy.Select`
(`internal/execution/strategy/selector.go:95`), not by a UI-side resolver.

The only UI-side resolution surface left after the pruning is
`resolveAutonomyBuildTarget` (`internal/ui/autonomy_target.go:38`), and its
scope is strictly bounded:

- It is called on ONE path: `executeAutonomyViaRuntime`
  (`internal/ui/runtime_cutover.go:107`), and only when the gateway's
  `SelectStrategy` already returned `HumanClarification` (the strategy
  selector declared the raw target ambiguous/unresolvable).
- It never re-resolves a target the strategy selector resolved. It only
  expands the ambiguity into a concrete candidate list for the human selector
  (`stageAutonomyTargetSelector`) or produces the terminal not-found diagnosis
  (`reportAutonomyTargetNotFound`). Selection is an explicit human act; no
  candidate is auto-picked, and the selected candidate is staged directly
  (`activateAutonomyTarget` → `executeAutonomyViaRuntime`) — never re-resolved.
- It must never grow into a second resolver: any new UI-side path that maps a
  raw target to a file is an architectural regression. Target resolution
  belongs to `strategy.Select`; ambiguity resolution is a human decision.

`resolveHotfixTarget`/`resolveMultiHotfixTargets` (the legacy regex fast-track
resolvers) were removed with the legacy hotfix path.

### Removed from the UI (dead architecture deleted)

- UI-side engine-first strategy router (`routeEngineFirstPrompt` /
  `routeEngineFirstTargeted` / `routeEngineFirstDeterministic` /
  `routeEngineFirstReasoning` / `clarifyEngineFirst`) — duplicated strategy
  selection.
- UI-side `recordStrategyGraph` — the runtime proof graph is authoritative.
- `routeFreeInput` / `routerResultMsg` / `handleRouterResult` /
  `router_wiring.go` — the UI-side hybrid intent classifier (duplicated intent
  classification) and its route-confirmation UI.
- `runAskPromptHandoffCmd` — the UI-owned `$prompt` provider call.
- `$prompt` compressor fast-track + direct-mutation classifier (hidden /build
  fallbacks).

### Validation (acceptance test)

`internal/execution/validation_test.go` — `TestValidationPromptTargetedMutation`
proves the exact contract for `$prompt check index.html and remove extra
contents`:

- emits `execution.started`, `strategy.selected`, `target.resolved`,
  `context.prepared`, `model.invoked`, `artifact.produced`,
  `approval.required`, `mutation.completed`, `verification.completed`,
  `execution.finished`;
- emits NO `phase.changed`, NO `stage.completed`;
- the runtime invokes the provider exactly once;
- token accounting comes only from the provider's Usage record;
- verification evidence comes from the real verifier result.

### Remaining risks (unchanged scope)

- Legacy `/build` per-task proposals, multi-file `$hot`, `/commit`, and the
  streaming `streamCmd` still hold UI-owned provider calls (documented in the
  Phase-2 report). The unified gateway is the single path for bare text,
  `$prompt`, and single-target `$hot`.
- `$inspect` renders the runtime `ExecutionProof.Graph` concept via the proof;
  wiring the full proof into `$inspect` rendering is future work.
- `EventApprovalRequested` (patch-engine Tier-4) and `EventApprovalRequired`
  (runtime) coexist; the runtime path uses `EventApprovalRequired`.
