# IZEN — Phase 6 Runtime Reality & Production Path Audit

**Phase:** 6 — Runtime Reality & Production Path Audit  
**Baseline:** Phases 1–5 CLOSED — Authority Hardening, Project Understanding, Mutation Strategy, Bounded Adaptive Continuity, Adaptive Step Admission + Generality Boundary Correction  
**Date:** 2026-09-21  
**Auditor:** automated production-path trace (graph + grep + file reads)  
**Branch:** `fix/execution`  
**Commit:** HEAD of `fix/execution` at audit time  
**Gate:** BLOCKED (P1 semantic divergence remains; see §19 / §22)

---

## 1. Executive Result

```
VERDICT: BLOCKED
```

The **production runtime does NOT execute through the architecture that Phases 1–5 claim**. The claimed canonical chain:

```
Human Intent → Project Understanding → Problem Surface → ProblemSolvingPlan
→ Candidate Step → Adaptive Step Admission → Authorization → StepScheduler
→ Execution → Observation/Evidence → Truthful State → Continuation → Next Candidate
```

exists as **implemented packages and passing unit/architecture tests**, but the **TUI and headless production entrypoints bypass every link from Project Understanding through StepAdmission, StepScheduler, and Continuation**. The live runtime instead executes through a disjoint authority tree:

```
Human Intent (parser.IntentAST via parser.ParseInWorkspace)
→ IntentGateway / Strategy.Select (execution/strategy)
→ RuntimeExecutor (execution.RuntimeExecutor) / FileExecutor + PatchManager
→ substrate.ProposalExecutor (runtime/substrate.ConcreteSubstrate / Engine)
→ Authorization: core/authorization.CapabilityGuard + runtime/authorization.Gate + runtime/preflight Gate + runtime/scopeguard + runtime/gate Pipeline + loop.Barrier + domain/policy.PolicyEngine
→ Orchestration: runtime/orchestrator Loop (Model Output → RMAH → Gate → Substrate) + runtime/autonomy Driver (Observe → Decide → Execute → Interpret) + execution/planner ExecutionDAG
→ Observation: events.Bus + execution.ExecutionProof + runtime/durable.TaskState + core/domain/evidence
→ Continuation (in production): autonomy.RuntimeLoop recovery matrix + substrate RecoveryContext zero-delta halt, NOT internal/continuation.DeriveNextStep
```

Consequences:

* Phase 2 (`internal/understanding`, `internal/problemsurface`, `internal/changesurface`, `internal/problem`, `internal/mutationstrategy` including `adapters/web`) — **TEST_ONLY / ARCHITECTURE_ONLY**. Zero production callers; `adapters/web.DeriveSurface` is the only internal caller of `changesurface.Derive` and itself has zero production callers.
* Phase 5 (`internal/stepadmission.AdmitStep`) — **TEST_ONLY**. Zero production callers. Mutation and non-mutation paths both bypass admission.
* Phase 4 (`internal/continuation.DeriveNextStep` + `runtime/scheduler.ContinuationToTaskSpec` / `DeriveContinuation`) — **TEST_ONLY**. Zero production callers.
* Phase 4–5 scheduler (`runtime/scheduler.StepScheduler.Schedule`, `RunNext`, `AcceptStep`, `EffectiveBudget`) — **DEAD** in production. The struct is instantiated only in `*_test.go`. Production scheduling is owned by `runtime/autonomy.Driver` + `execution/planner` + `runtime/orchestrator.Loop`.
* The **canonical execution authority** (Phase 1) IS production-canonical, but through `execution.RuntimeExecutor` + `runtime/substrate` (ConcreteSubstrate / Engine), not through the scheduler path. TUI and headless converge on that authority — `CONVERGENT` on authority, `DIVERGENT_SCHEDULER` on scheduling.
* Capability (`providers/capability.ModelCapabilities`, `core/domain/provider.ProviderMetadata`) IS produced and consumed — **PRODUCTION_REACHABLE** via `providers/capability.MaxOutputTokensFor`, `ContextWindowFor`, `llm/registry`, and `runtime/scheduler.EffectiveBudget` equivalent in `core/domain/provider.EffectiveStepBudget` — but **not through the Phase 5 StepAdmission budget seam** in production.

No P0 authority bypass was found that writes workspace without `runtime/substrate` or `execution.PatchManager` under `RuntimeExecutor`/`substrate.Engine`. The blocker is **P1 semantic divergence**, not an unauthorized mutation path.

Repairs in this phase were limited to **wiring-audit assertions** in `cmd/izen/main.go:38-47` (DI refs to `preflight.NewGate`, `gate.NewPipeline`, `harness.NewExtractorPipeline`, `orchestrator.NewLoop`) and `internal/runtime/compose.Wire` assembly of those invariants into `cli.Stack` and `compose.Application`. No Phase 2–5 understanding/admission/continuation wiring was attempted because that would constitute Phase 7 redesign and violate §26/§27.

---

## 2. Production Runtime Entrypoints

### 2.1 Single binary

`cmd/izen/main.go:82 main()`

* Phase 1 dispatch: global subcommands without workspace state — `version`, `help`, `auth`, `stats`, `config`, `compact`/`memory`, `debug`, `run`, `prompt`, `orchestrate`. Unknown subcommands fall through to local scope parsing.
* Phase 2 local scope: `targetDir` from `os.Args[1]` when not `-`-prefixed; `isRollbackMode` for `rollback`.
* Bootstrap: `config.Load`, `config.Validate` (non-fatal unconfigured boot into TUI models picker), `prompt.SetActiveStyle`, silent `~/.izen` init, `project.Detect(root)` (pure scan, no writes), `compose.Wire` (sole composition root).
* Concurrency: `lock.TryAcquireWorkspaceLock(root)` held for TUI lifetime; nested agent-loop and patch application reuse self-held lock.
* Audit durability: `installAuditSignalFlush(app)` (SIGINT/SIGTERM blocking Flush) + `defer FlushAudit` → `AuditCloseErr`.
* Legacy migration: `state.MigrateLegacyFiles`, `state.CheckVersion`, `updateLocalConfig`.
* Routing: `!exist .izen/config.json` → `ui.RunMainDashboardWithApp` (onboarding); else `ui.RunMainDashboardWithApp` or `ui.RunRollbackEngine`.

### 2.2 Composition root (sole authority assembly)

`internal/runtime/compose/compose.go:428 Wire()`

Only place that instantiates concrete Infrastructure adapters and wires the Application layer. Produces `compose.Application` with:

* `Bus *events.Bus` (shared domain bus; `audit.NewLogger` projection onto it)
* `Workflow WorkflowRuntime` + `WorkflowSM *coreWorkflow.WorkflowStateMachine` + `Orchestrator *runtimeOrchestrator.PhaseManager` + `Pipeline *pipeline.Engine`
* `Execution *execution.Engine` + `Executor *execution.RuntimeExecutor` (sole execution authority boundary; `internal/runtime/executor.FileExecutor` and `substrate.ProposalExecutor` under it)
* `Gateway *execution.IntentGateway` (unified intent → ExecuteRequest)
* `Sessions *session.Manager` (dual-slot + compaction runner)
* `Knowledge *knowledge.Store` / `Promotion` / `Compiler *contextcompiler.Compiler` (Phase 2 decoupled context subsystems — async, never main loop)
* `Patch *patch.Engine`, `Auth *authorization.AuthorizationEngine`, `Policy *policy.PolicyEngine`, `Caps *domaincap.CapabilitySet`, `Budget *budget.MutationBudget`, `Artifacts *artifact.Store`
* `Autonomy *autonomy.Engine` + `Autonomous *runtimeAutonomy.Driver` (bounded loop consumer of `ExecutorAdapter`, NOT a second executor)
* `Authority *runtime.RuntimeAuthority` (seeds `config.Bindings.Active` / `ActiveModelName` at wire time; every `ExecuteRequest` carries explicit `TargetModel`)
* `Lea *lea.Engine`, `Git *git.Engine`

### 2.3 TUI presentation entrypoint

`internal/ui/program.go:72 NewProgram()` / `internal/ui/model.go`

`cmd/izen/main.go:302,327` calls `ui.RunMainDashboardWithApp(cfg, root, localCfg, app, bootErr, detection)` which constructs `ui.model` and drives `tea.Program`. The UI is a pure projection: it subscribes to `events.Bus` envelopes, never calls a provider or `PatchManager` directly on migrated paths (`internal/ui/gateway.go`, `internal/ui/intent_dispatch.go`, `internal/ui/runtime_executor.go`).

### 2.4 Headless / CLI entrypoints

| Entry | File | Wiring |
|-------|------|--------|
| `izen run "<prompt>"` | `cmd/izen/runtime.go:104 runRuntimeCommand` + `internal/cli/cli.go:382 Stack.Run` | `cli.Wire(llm, root, os.Stdin, os.Stdout)` → `orchestrator.Orchestrator` + `ProposalProviderCLI` + `TerminalBridge` → `preflight.Compiler` → `BudgetGate` decision surface → `Orchestrator.RunCycle` → `substrate` |
| `izen orchestrate "<target>"` / `izen prompt "...@file..."` | `cmd/izen/orchestrate.go:36 runOrchestrateCommand` | Same `cli.Wire` + `orchestrateAdapter` (`ai.Provider` → `cli.LLMProvider`) → `Stack.Run` |
| `izen debug [path]` | `cmd/izen/main.go:509 runDebugCommand` | On-demand diagnostics: `lea.NewEngine(root)` + `planner.New(LeaAdapter, TeeLogAdapter, RetrievalFileAdapter)` + `output.InspectWorkspace` → `debug.NewReport` (no mutation) |
| `izen compact` / `izen memory optimize` | `cmd/izen/main.go:418 runCompactCommand` | `compact.DiscoverFiles` + `compact.Optimize` (direct `os.WriteFile` on markdown/memory files; counted as CACHE/STATE persistence, not workspace mutation) |
| Generic `izen [path]` without subcommand | `cmd/izen/main.go:149-162` | Falls through to TUI path |

No `izen` subcommand reaches `internal/understanding.Derive`, `internal/problemsurface.Derive`, `internal/problem.Derive`, `internal/mutationstrategy.Derive`, `internal/stepadmission.AdmitStep`, or `internal/continuation.DeriveNextStep`.

---

## 3. Production Path Traces

### 3.1 TUI interactive path

```
USER INPUT (keyboard)
  ↓  internal/ui/model.go: Update() + internal/ui/intent_dispatch.go: intentFromInput() → parser.ParseInWorkspace(line, cmdreg.Default(), workspaceForMode(m.resolver.Current()))
COMMAND / INTENT PARSING
  ↓  internal/parser (parser.IntentAST: GlobalCommands, Directives, Scopes, Goal, Workspace, ScopeProvenance)
INTENT REPRESENTATION
  ↓  internal/ui/intent_dispatch.go: dispatchASTIntent() → dispatchDirectives()
       hasExecutionDirective($prompt/$hot) → routePromptDirective / routeHotfixThroughAutonomy
       hasDirective(review && test) → runReviewTestComposite
       else → handleCommand / handleReviewDollar
PROJECT UNDERSTANDING
  ↓  NOT PRESENT — no call to internal/understanding.Derive in any TUI file (grep: 0 hits outside _test)
PROBLEM SURFACE
  ↓  NOT PRESENT — no call to internal/problemsurface.Derive or internal/changesurface.Derive in TUI (adapters/web.DeriveSurface has 0 TUI callers)
PROBLEM-SOLVING PLAN
  ↓  NOT PRESENT — no call to internal/problem.Derive in TUI
CANDIDATE STEP
  ↓  NOT PRESENT as Phase 5 CandidateStep; instead execution/strategy.Select produces strategy.StrategyProfile + execution.IntentGateway.Gate → execution.ExecuteRequest
STEP ADMISSION
  ↓  NOT PRESENT — internal/stepadmission.AdmitStep has 0 TUI callers
AUTHORIZATION
  ↓  internal/runtime/scopeguard.Proposal → ScopeGuard.EvaluateProposal (internal/runtime/scopeguard/gateway.go:199 RuntimeExecutor, internal/runtime/scopeguard/scope.go)
       + core/authorization.CapabilityGuard.Evaluate (internal/core/authorization/engine.go:93)
       + runtime/authorization.Gate (internal/runtime/authorization/gate.go)
       + runtime/preflight.Gate.EvaluateBudgetGate (internal/runtime/preflight/gate.go) + decision.NewSurface.AnnotateStrategies (internal/runtime/ui/decision)
       + loop.Barrier (internal/loop/barrier.go) gating observing→deciding
       + domain/policy.PolicyEngine (internal/domain/policy)
STEP SCHEDULER (claimed)
  ↓  DIVERGENT — runtime/scheduler.StepScheduler.Schedule/RunNext NOT CALLED in TUI; instead runtime/autonomy.Driver (internal/runtime/autonomy/driver.go:42) + execution/planner.ExecutionDAG + loop + gate own scheduling
MODEL / CAPABILITY
  ↓  internal/providers/capability.ModelCapabilities via capability.MaxOutputTokensFor(ContextWindowFor) — internal/ui/stream.go:43-48, internal/ui/decision_surface.go:71 — and internal/runtime/scheduler.EffectiveBudget equivalent via core/domain/provider.EffectiveStepBudget consumed by substrate/recovery, NOT via stepadmission budget
EXECUTION
  ↓  internal/runtime/handlers.SubmitPromptHandler.Handle → BackgroundPreflight (internal/runtime/preflight) → runtime/autonomy.Driver.Run → ExecutorAdapter (internal/runtime/autonomy/adapter.go) → execution.RuntimeExecutor.Execute (internal/execution/executor.go:443) → runtime/substrate.Engine.Execute (internal/runtime/substrate/engine.go:291 os.WriteFile) OR execution.PatchManager.Apply (internal/execution/patch.go:594 os.WriteFile)
OBSERVATION / EVIDENCE
  ↓  events.Bus envelopes (internal/events) + execution.ExecutionProof + execution.Proof.Outcome + durable.TaskState.RecoveryContext + core/domain/evidence.EvidenceVector → EvidenceState
STATE UPDATE
  ↓  execution.ExecutionResult.Completed (InputTokens/OutputTokens/Known via provider) projected via internal/presentation.ExecutionProjection + ledger_builder + audit/events.ndjson
CONTINUATION
  ↓  DIVERGENT — internal/continuation.DeriveNextStep NOT CALLED; instead autonomy.RuntimeLoop recovery matrix (internal/autonomy/runtime_loop.go:459 DeriveBoundaryAction) + runtime/scheduler.PostStepEvaluation (zero-delta halt) + planner re-decomposition
NEXT CANDIDATE
  ↓  DIVERGENT — ContinuationToTaskSpec NOT CALLED in TUI; next candidate produced by Driver re-Observe or planner DAG next node
```

Evidence of non-reachability (all grep -r without `_test` → 0 hits):

* `understanding.Derive` — only caller is `adapters/web/changesurface.go:17` which itself has 0 production callers.
* `problemsurface.Derive`, `problem.Derive`, `mutationstrategy.Derive`, `stepadmission.AdmitStep`, `scheduler.Schedule/RunNext`, `continuation.DeriveNextStep` — 0 production callers.

### 3.2 Headless / CLI execution path (`izen run` / `izen orchestrate`)

```
USER INPUT: os.Args[2:] prompt string
  ↓  cmd/izen/runtime.go:133 parse -dir/-target, one prompt arg required
  ↓  cmd/izen/orchestrate.go:59 extract target
INTENT PARSING
  ↓  internal/cli/cli.go:552 extractTargetFromPrompt ( @-mention → existing file check ) + target.NewTargetResolver.Resolve
PROJECT UNDERSTANDING / PROBLEM SURFACE / PLAN / CANDIDATE / ADMISSION / SCHEDULER
  ↓  NOT PRESENT (same 0-caller evidence as TUI)
CAPABILITY RESOLUTION
  ↓  capability.ModelCapabilities{MaxOutputTokens: budget} passed to preflight.Gate.EvaluateBudgetGate via internal/cli/cli.go:431 + internal/runtime/preflight/gate.go
AUTHORIZATION / GATE
  ↓  preflight.Gate → decision.NewSurface (internal/runtime/ui/decision) when BudgetExceeded or ASTCorrupt → park at DecisionSurface, never invoke model
  ↓  else runtimectx.ContextUnit compilation (single snapshot read, zero redundancy)
EXECUTION
  ↓  internal/cli/cli.go:70 ProposalProviderCLI.GenerateProposal (LLMProvider.Complete) → executor.ProposedMutation
  ↓  internal/runtime/orchestrator.Loop.ExecuteCycle (Model Output → harness.ExtractorPipeline → gate.Pipeline → substrate.ProposalExecutor)  OR  internal/runtime/orchestrator.Orchestrator.RunCycle (legacy)
OBSERVATION
  ↓  executor.ProposalStagingBuffer (Partial + OUTPUT_CEILING) + diff.MutationEvidence via presentation/diff
CONTINUATION
  ↓  DIVERGENT — cli.Stack.Loop is wired (internal/cli/cli.go:336-346) but not driven in a multi-step continuation loop; headless does single-prompt single-cycle, not DeriveNextStep → StepAdmission → scheduler
```

### 3.3 `/ask`

```
USER INPUT: bare text without $prompt (or explicit /ask)
  ↓  internal/runtime/handlers.handlers.go:538 ClassifyIntent → "ask" when !HasExecutionMarker and mode != explicit
  ↓  internal/ui/gateway.go:172 direct_response gate (strategy.DirectResponse) — but in execution modes escalated to RepositoryInvestigation
  ↓  internal/ui/intent_dispatch.go: hasExecutionDirective==false → NO gateway execution; instead direct_response branch executes via executor.Execute with strategy.DirectResponse (zero repository context) and renders as human artifact
READ / WRITE / PATCH / TEST / SHELL / CONTINUATION / AUTHORIZATION
  → READ (response artifact), no WRITE/PATCH/SHELL, bounded diagnostic TEST/SHELL only via $test-type directives, verified below
```

### 3.4 `/plan`

```
USER INPUT: /plan (or plan-mode $prompt without $hot)
  ↓  internal/ui/intent_dispatch.go: target = plan, PlanStore + PlanEngine (internal/runtime/compose compose.go:689-695)
  ↓  internal/modes/plan/engine.go: PlanEngine (SetProvider/StreamProvider) → learner? pipeline Facade (internal/engine/pipeline) optional
  ↓  Produces plan artifact (investigation/plan), no execution, no mutation authority
READ/WRITE/PATCH/TEST/SHELL/CONTINUATION/AUTH
  → READ + plan artifact generation; WRITE/PATCH blocked; TEST not executed; SHELL not executed; CONTINUATION NOT via continuation package
```

### 3.5 `/build`

```
USER INPUT: /build (Build workspace)
  ↓  internal/ui/model.go + internal/modes/... build execution
  ↓  internal/execution.RuntimeExecutor execution via gateway (see TUI trace) with CapabilityWrite/Execute/Patch grants via domaincap.CapabilitySet
  ↓  Authorization: core/authorization + scopeguard + gate
  ↓  Mutation via substrate/patch, staged for Alt+A / Alt+R approval through handlers.ApprovePatchHandler / RejectPatchHandler → executor.Approve/Reject (real mutation, not fabricated patch record)
  ↓  Queue: internal/ui/gateway.go:723 projectBuildQueueFromProof books ExecutionProof outcome into sess.CurrentTasks queue; idle→completed/failed/stalled
READ/WRITE/PATCH/TEST/SHELL/CONTINUATION/AUTH
  → READ+WRITE+PATCH+TEST+SHELL permitted within capability/budget/scope, via authorized executor only; CONTINUATION via autonomy driver, NOT DeriveNextStep
```

### 3.6 `/build $prompt`

```
USER INPUT: /build$prompt <idea> or trailing $prompt <raw>
  ↓  internal/ui/intent_dispatch.go: hasExecutionDirective($prompt) → routePromptDirective → bindScopeProvenance(ScopeDynamic) → cancelStaleAgentOps → if m.autonomy != nil → runAutonomyRoutedCmd ELSE runPromptExecution → runGatedLine
  ↓  Verifies: ScopeDynamic (provenance rewrites sess.ScopeProvenance, NOT unlimited authority; see §13), NOT automatic scope expansion, NOT autonomous execution (single execution request, bounded by capability + budget + scope)
READ/WRITE/PATCH/TEST/SHELL/CONTINUATION/AUTH
  → WRITE scoped to ScopeDynamic targets only, via same gateway → RuntimeExecutor authority
```

### 3.7 `/build $hot`

```
USER INPUT: /build$hot ... or $hot directive
  ↓  internal/ui/intent_dispatch.go: dispatchDirectives loop — $hot → routeHotfixThroughAutonomy(tail) (distinct from $prompt)
  ↓  internal/runtime/autonomy/driver.go: Driver with IntentGateway + ExecutorAdapter + bounded LoopBounds + DecomposeFunc (planner.ExecutionDAG) + manifestPass (read-only Pass 1 manifest)
  ↓  Bounded pre-approved execution envelope: decompose only inside already-authorized targets; candidate inside envelope → allowed, outside → AWAITING_APPROVAL / BLOCK (enforced at autonomy decision + gate + scopeguard)
  ↓  Continuation cannot escape envelope: DecisionSurface for infeasible targets, DeriveBoundaryAction for permission barriers
READ/WRITE/PATCH/TEST/SHELL/CONTINUATION/AUTH
  → WRITE bounded by hotfix envelope (documented in §14), continuation via driver, NOT internal/continuation
```

### 3.8 `/investigate`

```
USER INPUT: /investigate
  ↓  internal/modes/investigate/* engine (forensic investigation, no mutation)
  ↓  internal/ui/agents.go:78 write capability detected → investigateResultMsg err "investigate mode: write capability detected — violating capability contract"
READ/WRITE/PATCH/TEST/SHELL/CONTINUATION/AUTH
  → READ only; TEST/SHELL bounded via $test-type directives under investigation toolrunner, but write blocked by capability check
```

### 3.9 `/review`

```
USER INPUT: /review (optionally $test composite)
  ↓  internal/command/router.go: IsReviewTestComposite + HandleReviewTestComposite (RunDynamicTests → InjectTestTelemetry → RunComprehensiveReview)
  ↓  internal/ui/agents.go:434 write/shell/patch capability detected → reviewResultMsg err "review mode: write/shell/patch capability detected — review must be 100% read-only"
READ/WRITE/PATCH/TEST/SHELL/CONTINUATION/AUTH
  → READ only; TEST only via explicit $test sub-command in composite pipeline
```

### 3.10 Continuation after partial output

```
PRODUCTION REALITY: NOT via internal/continuation.DeriveNextStep / runtime/scheduler.ContinuationBridge

REAL PARTIAL PATH (substrate/executor level):
model partial (finish_reason=length)
  ↓  internal/execution/toolcalls or internal/runtime/executor.ProposalStagingBuffer captures finish_reason=length
  ↓  internal/runtime/executor/staging.go: ProposalStagingBuffer.Finalize(STAGING_REASON_OUTPUT_CEILING) → disposition.NeedsContinuation=true
  ↓  internal/runtime/scheduler/continuation.go:81 StepScheduler.RunNext detects stream.Truncated → buffer.Finalize("length", truncated=true) → CommitGate → outcome StepOutcomePartial + PostStepEvaluation (zero-delta halt threshold=2)
  ↓  internal/runtime/autonomy/driver.go: Driver interprets truncated observation → bounded recovery (append ReuseSymbols + RecoveryContext Re-read) OR planner re-decomposition
  ↓  Existing scheduler is NOT Re-entered via stepadmission; StepAdmission bypassed on this path as well

CLAIMED PATH UNREACHABLE:
model result → partial/outcome → observation → DeriveNextStep → ContinuationToTaskSpec → existing scheduler
  → 0 production callers for DeriveNextStep and ContinuationToTaskSpec (verified via grep across internal/ excluding _test → 0 hits)
```

### 3.11 Mutation execution

```
PRODUCTION CANONICAL (TUI):
SubmitPrompt/Build task → gateway.Gate → RuntimeExecutor.Execute (internal/execution/executor.go:443) → FileExecutor (internal/runtime/executor/file_executor.go:34) → PatchManager.Apply (internal/execution/patch.go:594 os.WriteFile) → substrate.Engine.Execute unit (internal/runtime/substrate/engine.go:291 os.WriteFile with 0o644) → Proof + Evidence → approval gate

PRODUCTION CANONICAL (CLI):
cli.ProposalProviderCLI.GenerateProposal → orchestrator.Loop.ExecuteCycle → substrate.ConcreteSubstrate.Execute (internal/runtime/substrate/substrate.go:326 os.WriteFile) OR executor.Executor.Execute

AUTHORITY CONVERGENCE: Both converge on runtime/substrate.ProposalExecutor as filesystem sink; no second executor bypasses it.
```

### 3.12 Verification execution

```
VERIFICATION PATH:
  mutation applied → internal/verification/harness.go: DeriveEvidenceState → internal/execution/verifier or internal/runtime/output classification → VerificationReport → ExecutionProof.Verification.Passed → projectBuildQueueFromProof
  Truncated → OutcomeTruncated projected distinctly, never as success
  ArtifactRetryableRejected → OutcomeArtifactRetryableRejected projected distinctly, no hidden UI retry (recovery ownership with driver)
```

---

## 4. Reachability Audit (Phase 1–5)

| Component | Claimed Role | Production Entry | Production Caller | Status | Evidence |
|-----------|--------------|------------------|-------------------|--------|----------|
| `internal/understanding` | ProjectUnderstanding (EXISTING/GREENFIELD/UNKNOWN) | — | — | **TEST_ONLY** | `understanding.Derive` has 0 non-test callers; only caller `adapters/web/changesurface.go:17` has 0 non-test callers; no import of `internal/understanding` outside `_test` + `mutationstrategy` + `changesurface` (both dead) |
| `internal/problemsurface` | ProblemSurface (evidence-backed area) | — | — | **TEST_ONLY** | Same zero-caller evidence; `problemsurface.Derive` 0 non-test callers |
| `internal/changesurface` | ChangeSurface | — | `adapters/web/changesurface.go:17` | **TEST_ONLY** | 0 TUI/CLI/runtime callers; adapter itself dead in production |
| `internal/problem` | ProblemSolvingPlan | — | — | **TEST_ONLY** | `problem.Derive` 0 non-test callers; doc `internal/problem/doc.go:18` explicitly states "translation not implemented in this phase" |
| `internal/mutationstrategy` | MutationPlan | — | — | **TEST_ONLY** | `mutationstrategy.Derive` 0 non-test callers outside its own tests; no production import chain reaches it from `compose.Wire` or TUI |
| `internal/continuation` | DeriveNextStep | — | `runtime/scheduler/continuation_bridge.go:131` (bridge, itself dead) | **TEST_ONLY** | `continuation.DeriveNextStep` 0 non-test callers outside tests+bridge; bridge has 0 production callers |
| `internal/stepadmission` | AdmitStep | — | — | **TEST_ONLY** | `stepadmission.AdmitStep` 0 non-test callers; `runtime/scheduler.RunNext` does not call it |
| `internal/runtime/scheduler` | StepScheduler (Schedule/RunNext/AcceptStep/EffectiveBudget) | — | `runtime/scheduler/continuation.go:81 RunNext` + `continuation_bridge.go` (tests only) | **DEAD** | `NewStepScheduler` only in tests; `Schedule`/`RunNext` never called from `compose.Wire`, TUI, CLI, or autonomy driver |
| `internal/runtime/scheduler` effective budget | Ephemeral per-step budget | `core/domain/provider.EffectiveStepBudget` | `runtime/scheduler.EffectiveBudget` dead; real budget via `core/domain/provider.EffectiveStepBudget` in substrate/executor | **PARTIALLY_INTEGRATED** | Capability math exists and is PRODUCTION_REACHABLE via `core/domain/provider`, but NOT via `runtime/scheduler` seam |
| `canonical execution authority` | RuntimeExecutor + substrate | `execution.RuntimeExecutor` + `runtime/substrate.ProposalExecutor` | `compose.Wire:583 Executor`, `runtime/handlers:373 Approve`, `ui/gateway.go:277 executor.Execute`, `runtime/autonomy/adapter.go`, `cli.Wire: ProposalProviderCLI` | **PRODUCTION_REACHABLE** | Convergent sole authority; all workspace mutations go through `substrate.Engine.Execute` or `execution.PatchManager.Apply` under `RuntimeExecutor` |
| `authorization boundary` | CapabilityGuard + scopeguard + gate + policy | `core/authorization`, `runtime/authorization`, `runtime/preflight.Gate`, `runtime/scopeguard`, `runtime/gate.Pipeline`, `domain/policy`, `loop.Barrier` | `handlers.SubmitPromptHandler`, `scopeguard.Gateway`, `preflight.Gate`, `autonomy.Driver`, `cli.Stack.Run` | **PRODUCTION_REACHABLE** | Multi-layer gate, all production reachable (see §6); single `OCCGate` and `scopeguard` remain canonical |
| `observation/event bus` | events.Bus | `events.NewBus` + `events.Envelope` / `DomainEvent` | `compose.Wire:224 Bus`, `audit.Logger`, `runtime.LedgerBuilder`, `handlers.emit`, `ui/model.go handleDomainEvent`, `execution` proofs | **PRODUCTION_REACHABLE** | Bus is shared across TUI and headless; audit/events.ndjson persisted durably; `FlushAudit` authoritative finalization |
| `internal/runtime/durable` TaskState / RecoveryContext / OCC | Durable task state | `runtime/durable` | `runtime/scheduler.PostStepEvaluation`, `runtime/autonomy.Driver`, `execution.ProposalStagingBuffer` | **PRODUCTION_REACHABLE** | RecoveryContext + zero-delta halt threshold=2 actively enforces bounded recovery in production |

Status legend: `PRODUCTION_REACHABLE` = at least one non-test production caller proven by exact file:line; `TEST_ONLY` = package exists, tests pass, but grep across `internal/` excluding `*_test.go` yields 0 production imports/calls; `DEAD` = type exists, zero production instantiation; `PARTIALLY_INTEGRATED` = concept is production-active but seam differs from claimed package; `DIVERGENT_PATH` = function exists but different implementation owns the semantics in production.

---

## 5. Authority Audit

### 5.1 Canonical mutation authority (sole)

* `internal/execution.RuntimeExecutor` (`internal/execution/executor.go:443`) — the boundary presentation submits `ExecuteRequest` via and `Approve/Reject` via; owns provider invocation, patch creation, mutation lifecycle, verification. Composed once in `compose.Wire:583`.
* `internal/runtime/substrate.ProposalExecutor` (`internal/runtime/substrate/substrate.go:73`) — the filesystem sink (`ConcreteSubstrate` prod, `Engine` test/audit). Mandatory for `orchestrator.Loop` (nil → `ErrNilSubstrate`). Every workspace write goes through `Engine.Execute` (`engine.go:291`) or `ConcreteSubstrate.Execute` (`substrate.go:326`).
* `internal/runtime/executor.FileExecutor` (`internal/runtime/executor/file_executor.go:34`) — the commit gate inside `Loop.ExecuteCycle:327` via `executor.CommitGate` (requires authorized `WorkspaceSink`).
* `internal/execution.PatchManager` (`internal/execution/patch.go:95`) — the transaction boundary (`MutationSet` + `ModeKind` + `oc` snapshot) that the above own; UI never calls `os.WriteFile` on workspace directly for mutation.

### 5.2 Every production filesystem/shell mutation mechanism

| Mechanism | Location | WHO CALLS IT | WHAT AUTHORIZES IT | WHAT SCOPE BINDS IT | CAN BYPASS CANONICAL? | Classification |
|-----------|----------|--------------|---------------------|----------------------|-----------------------|----------------|
| `runtime/substrate.Engine.Execute` (`engine.go:291 os.WriteFile`) | `internal/runtime/substrate/engine.go:291` | `orchestrator.Loop.ExecuteCycle:327` + `cli.Stack.Run` via `ProposalProviderCLI` | `gate.Pipeline.Evaluate` + `harness.ExtractorPipeline` + `substrate.ProposalExecutor` must be non-nil + `BudgetGate` preflight + `DecisionSurface` park → no model call when exceeded | `TargetFile` from Observation snapshot (single-read `FSSnapshotReader`); candidate re-anchored to `snapshot.TargetFile` (`loop.go:303`) | NO — it IS canonical | **AUTHORIZED EXECUTION** |
| `runtime/substrate.ConcreteSubstrate.Execute` (`substrate.go:326 os.WriteFile`) | `internal/runtime/substrate/substrate.go:326` | `cli.Wire` bootstrapped `Loop` via `substrate.NewConcreteSubstrate(root)` | Same gate pipeline | Same snapshot anchoring | NO | **AUTHORIZED EXECUTION** |
| `execution.PatchManager.Apply` (`patch.go:594 os.WriteFile`) | `internal/execution/patch.go:594` | `execution.RuntimeExecutor.Execute` → `execution/toolcalls` pipeline | `execution/toolcalls` boundary + `MutationSet` transaction + `Verifier` + `CapabilitySet` | `@`-referenced targets via `target.ExtractReferences` + `scopeguard` + `CapabilitySet.CanMutateFile` | NO — routed through executor | **AUTHORIZED EXECUTION** |
| `execution/toolcalls.go:148 os.WriteFile` (batch) + `374` + `408` | `internal/execution/toolcalls.go` | `execution.RuntimeExecutor` worker pipeline (`Strategy.MUTATE` path) | `scopeguard.EvaluateProposal` + `WorkerEngine.ExecuteProposal` fail-closed on deny (internal/ui/gateway.go:130) | Same scopeguard | NO | **AUTHORIZED EXECUTION** |
| `execution/patch.go:919 ApplyContext`, `store:62` | `internal/execution` + `runtime/substrate/store` | Same executor paths | Same | Same | NO | **AUTHORIZED EXECUTION** |
| `execution/boundary.go:76 os.WriteFile` + `70 os.Remove` | `internal/execution/boundary.go` | `MutationSet` rollback/commit within executor | `MutationSet` owns transaction lifetime; `PatchManager` only records | Workspace isolation | NO | **LEGITIMATE INFRASTRUCTURE** |
| `runtime/substrate/exec.go:29 exec.CommandContext` + `substrate.go:416,452` + `output/output.go:246 bash -c` + `execution/runner.go:229 sh -c` | `internal/runtime/substrate/exec.go`, `substrate.go`, `runtime/output/output.go`, `execution/runner.go` | `substrate.Engine` shell operation path + execution toolcalls shell ops | `capability.CapabilityExecute` + `runtime/authorization.Gate` + `domain/policy.PolicyEngine` + shell firewall (`shell_auth.go`) | `CanExecuteCommand` + per-file grants | NO — only via shell capability gate | **AUTHORIZED EXECUTION** |
| `os.WriteFile` to `.izen/` state (`internal/config/config.go:533`, `local.go:50`, `security/credentials/encrypt.go:93`, `internal/core/artifact/persistence.go:295`, `workspace/checkpoint/manager.go:311`, `runtime/output/tee.go:94`, `internal/state/*`) | Multiple | `compose.Wire`, `state.MigrateLegacyFiles`, `sessions.Manager`, `audit.Logger`, `output.Tee`, `artifact.Store` | N/A — not workspace mutation | `.izen/` internal state only | NO — distinguished from workspace mutation by path | **RUNTIME STATE PERSISTENCE** |
| `os.WriteFile` to `compact` markdown/memory files (`cmd/izen/main.go:475 compact`) | `cmd/izen/main.go:475` | `runCompactCommand` | CLI direct (not via substrate) | Markdown/memory files only | YES — but scoped to `compact` targets; not workspace code mutation; classified as **CACHE/STATE** | **CACHE/STATE** |
| `exec.CommandContext("git", ...)` (`internal/checkpoint/manager.go:332`, `workspace/snapshot/snapshot.go:260-267`, `runtime/target/resolver.go:85,90`) | Multiple | Snapshot digest + VCS + checkpoint | `git` presence check degrades to no-op; IsRepo guard | Workspace git only | NO | **LEGITIMATE INFRASTRUCTURE** |
| `internal/llm/registry.go:363 os.WriteFile` (provider cache) | `internal/llm/registry.go:363` | LLM registry cache write | N/A | Provider cache dir | NO | **CACHE/STATE** |

**Finding:** No production path mutates workspace without traversing `runtime/substrate` or `execution.PatchManager` under `execution.RuntimeExecutor`. TUI `!` shell (`internal/ui/shell_stream_test.go`, `shell_auth.go`) is gated by `CanShell` + `scopeguard` + `authorization.Gate`; unapproved shell is blocked. Headless `cli.Stack.Run` always traverses `orchestrator.Loop` substrate. **PASS** on §6 blocker.

### 5.3 Authorization independence verification

* `Intent` classification (`handlers.ClassifyIntent`, `parser.ParseInWorkspace`) is **independent** of `TargetState` / `WorkspacePolicy` / `AuthorizationContext` — the parser never authorizes.
* `Operation` derivation is by `compiler.DeriveOperation` / `execution/strategy.Select` + `target.NewTargetResolver.Resolve`, not by LLM.
* Authorization (`ScopeGuard.EvaluateProposal` → `CapabilityGuard` → `PreflightGate` → `Gate.Pipeline` → `PolicyEngine.Evaluate`) is independent of existence/relevance/operation.
* Execution-time confinement: `Loop.Observe` (single snapshot read) + re-anchor (`loop.go:303 candidate.TargetFile = snapshot.TargetFile`) ensures resolution at planning time is NOT treated as execution authorization.

---

## 6. Scheduler Audit

### 6.1 Claimed `StepScheduler`

`internal/runtime/scheduler/scheduler.go:94 StepScheduler` with `Schedule`, `EffectiveBudget`, `AcceptStep`, `RunNext`, `ContinuationBridge`, `PostStepEvaluation` / `zeroDeltaHaltThreshold=2`.

**Production callers:** NONE outside tests. `grep -rn NewStepScheduler internal/ --include=*.go | grep -v _test → 1 hit (the definition)`. `grep -rn "\.Schedule(" internal/ --include=*.go | grep scheduler | grep -v _test → 0`. `RunNext` and `AcceptStep` similarly 0 non-test callers. Every `runtime/scheduler/*` symbol is test-only.

### 6.2 Real production schedulers (divergent)

| Mechanism | Location | What it schedules | Divergent? |
|-----------|----------|-------------------|------------|
| `runtime/autonomy.Driver` | `internal/runtime/autonomy/driver.go:42` + `internal/autonomy/runtime_loop.go:616 Executor` | `LoopRequest` → `Decide` → `Execute` via `ExecutorAdapter` → `Interpret` → complete/recover/abort/park; owns bounded recovery matrix (3 same fingerprint+outcome+evidence+0 patches → no_progress, 2 zero-delta OUTPUT_CEILING → halt) | **DIVERGENT_SCHEDULER** — independently owns scheduling semantics (step selection via planner DAG, retry via `RepairFunc`, continuation via `deriveNextStep` inside driver, NOT via `internal/continuation`) |
| `execution/planner` ExecutionDAG | `internal/execution/planner`, `internal/runtime/autonomy/decomposition.go:777 DeriveBoundaryAction` | DAG decomposition when `preflight_infeasible`; `manifestPass` Pass 1 read-only manifest before strategy | **DIVERGENT_SCHEDULER** — owns proposal decomposition |
| `runtime/orchestrator.Loop` | `internal/runtime/orchestrator/loop.go:152 Loop` + `197 NewLoop(nil substrate → ErrNilSubstrate)` | Closed Model Output → RMAH → Gate → Substrate; `StateIdle/Executing/Verifying/AwaitingHuman/Committed/Failed` with `maxFormatFailures=2` fast-fail | **CONVERGENT** — not a task scheduler; it is the verifier/ committer inside the driver/CLI Stack, converges on substrate authority |
| `internal/execution/scheduler.Scheduler` + `execution/scheduler.go` (Tool Scheduler) | `internal/execution/scheduler/scheduler.go:26` | Concurrent execution of read-only tool calls vs mutating calls (`MaxReadWorkers = NumCPU`) | **LEGITIMATE INFRASTRUCTURE** — tool-call parallelism, not task scheduling; does not own step boundaries |
| `internal/runtime/scheduler.ContextPlanner.Assemble` | `internal/runtime/scheduler/planner.go:87` | Assembles `ContextSlice` for `RunNext` — dead because `RunNext` is dead | **DEAD** |
| Raw `for { execute(...) }` loops | `internal/runtime/autonomy/driver.go:1000 observeAndRun`, `internal/execution/runner.go`, `internal/cli/cli.go:382 Stack.Run` single cycle | Not autonomous unbounded loops; bounded by `LoopBounds` + `maxFormatFailures` + `zeroDeltaHaltThreshold` | Audited; not a violation by itself, but the `Driver` for-loop DOES independently own scheduling semantics → classified above |

**Conclusion:** Existing `StepScheduler` is **NOT the runtime scheduler**. Production scheduling is owned by `runtime/autonomy.Driver`. This is `SEMANTIC DIVERGENCE`, not merely interface difference. The driver converges on authority (`RuntimeExecutor`/`substrate`) but diverges on scheduling semantics — a **P1** violation of §30 gate "Existing StepScheduler remains canonical" and "No production second scheduler exists".

---

## 7. Continuation Reachability

Phase 4 introduced `internal/continuation` (`doc.go`, `types.go`, `derive.go`) and `internal/runtime/scheduler/continuation_bridge.go` (`ContinuationToTaskSpec`, `DeriveContinuation`).

Claimed path:
```
model result → partial/outcome → observation → DeriveNextStep → ContinuationToTaskSpec → existing scheduler
```

* `continuation.DeriveNextStep` (`internal/continuation/derive.go:15`) — pure, deterministic, side-effect-free; never executes/writes/authorizes/schedules/expands scope.
* `scheduler.ContinuationToTaskSpec` (`internal/runtime/scheduler/continuation_bridge.go:25`) — thin adapter: preserves durable `ActiveTargetScope` envelope, carries `LatestEvidence`, reuses `EffectiveBudget`/`Schedule`.
* `scheduler.DeriveContinuation` (`continuation_bridge.go:63`) — convenience that composes `TaskStateSnapshot` + `TaskSpec` + `StepResult` + `Observation[]` + `allowedScope` + `isStale` → `DeriveNextStep`.

**Production callers:** 0.

```bash
grep -rn "DeriveNextStep\|ContinuationToTaskSpec\|DeriveContinuation" --include="*.go" internal/ | grep -v _test → 4 hits: the two definitions + the bridge comment + runtime/scheduler/continuation.go isStale check
grep -rn "internal/continuation" --include="*.go" internal/ | grep -v _test | grep import → 1 hit: runtime/scheduler/continuation_bridge.go (the bridge itself)
grep -rn "scheduler\.Continuation" --include="*.go" | grep -v _test → 0
```

Only `internal/runtime/scheduler/continuation_integration_test.go:10` exercises the full `Task → ProblemSolvingPlan → scheduler.Schedule → RunNext(staging,length) → observation → DeriveNextStep → ContinuationToTaskSpec → scheduler.Schedule` path, and it does so with `integrationSink` helper that writes to a temp map — not production.

**Classification:** `CONTINUATION NOT PRODUCTION-INTEGRATED` — `TEST_ONLY`. The real continuation in production is the `autonomy.Driver` recovery path + `scheduler.PostStepEvaluation` zero-delta halt + `planner.ExecutionDAG` re-decomposition (see §3.10). None of those call `internal/continuation`.

---

## 8. Step Admission Reachability

Phase 5 introduced `internal/stepadmission` (`doc.go`, `types.go`, `admission.go:18 AdmitStep`, `capability.go`, `estimate.go`, `refine.go`, `evidence.go`).

Claimed gate:
```
CandidateStep → AdmitStep → ADMIT / REFINE / BLOCK (or STALE / AWAITING_APPROVAL)
```

Pure, stateless, no I/O, no authorization mutation; `EffectiveBudget` derives per-step ceiling from `ModelCapabilityProfile.MaxOutputTokens` + `mutationstrategy.StepBudget` via `mutationstrategy.StepBudgetFor`.

**Production callers:** 0.

```bash
grep -rn "AdmitStep\|stepadmission" --include="*.go" | grep -v _test → 8 hits, all definitions/docs inside stepadmission itself
grep -rn "CandidateStep\|CandidateFromMutation" --include="*.go" | grep -v _test → 0 outside stepadmission
```

Every mutation path does `ExecuteRequest → substrate/planner/driver` without `AdmitStep`:

```text
TUI: gateway.Gate → RuntimeExecutor.Execute (no AdmitStep)
CLI: ProposalProviderCLI.GenerateProposal → Loop.ExecuteCycle (no AdmitStep)
Build queue: execution.RuntimeExecutor → PatchManager → (no AdmitStep)
```

A mutation path that does `MutationStep → scheduler` while bypassing `StepAdmission` must be reported as divergence — **observed**. Similarly, non-mutation paths (`INVESTIGATE`/`VERIFY`) correctly do not require mutation admission, but they also never call `AdmitStep` for their `VERIFY`/`OBSERVE` kinds — `StepAdmission` is not invoked for ANY kind in production, so legitimate non-mutation bypass cannot be distinguished from divergence.

**Classification:** `DIVERGENT_PATH` — step admission is architecturally valid and pure, but **not production-reachable**. The P5 budget math (`core/domain/provider.EffectiveStepBudget` with `DefaultRequestedStepBudget=2048`, `ConstrainedOutputThreshold=1024`, `EffectiveStepBudget = min(requested, providerMax, taskRemaining, reasoningMargin)`) IS production-active, but through `core/domain/provider` and `providers/capability` seams, not through `stepadmission`.

---

## 9. Capability Resolution Audit

Trace:
```
Provider metadata → capability normalization → ModelCapabilityProfile → StepBudget → StepAdmission
```

* Provider metadata source: `internal/llm/metadata.go` (Claude/GPT/Anthropic static caps) + `internal/llm/registry.go` (OpenRouter/Ollama discovery via provider `list` + `SupportedEfforts`) + `internal/providers/capability` (dynamic `ModelCapabilities`: `MaxOutputTokens`, `ContextWindow`, `SupportsReasoning`, `SupportedEfforts`).
* Capability normalization: `providers/capability.ModelCapabilities` → `domaincap.CapabilitySet` → `core/domain/provider.ProviderMetadata.ResolvedOutputLimit()` (`internal/core/domain/provider/capability.go:65`) → `core/domain/provider.EffectiveStepBudget`.
* Production consumption:
  * `internal/ui/stream.go:45 capability.MaxOutputTokensFor(provider, model)` + `ContextWindowFor(model)` — **PRODUCTION_REACHABLE** (streaming budget clamp).
  * `internal/ui/decision_surface.go:71 capability.ModelCapabilities{MaxOutputTokens: maxTokens}` — DecisionSurface budget exceeded check (**PRODUCTION_REACHABLE**).
  * `internal/cli/cli.go:431 capability.ModelCapabilities{MaxOutputTokens: budget}` → `preflight.Gate.EvaluateBudgetGate` — **PRODUCTION_REACHABLE**.
  * `internal/runtime/preflight/gate.go + budget.go` — parses token budget and parks `FULL_REWRITE` as `StrategyForbidden` when `BudgetExceeded` — **PRODUCTION_REACHABLE**.
* `StepAdmission` budget path: `stepadmission.capability.EffectiveBudget` mirrors `mutationstrategy.StepBudgetFor` with `DefaultFallbackBudget{MaxOutputTokens:4096}` — **TEST_ONLY**; `stepadmission.AdmitStep` never called in production, so its `EffectiveBudget` seam is **not production-observable**.
* `MaxOutputTokens`, `ContextLimit`, `StructuredOutputSupport`, `ToolSupport`, `StreamingSupport` — `MaxOutputTokens` + `ContextLimit` are actively used (see above); `StructuredOutputSupport`/`ToolSupport`/`StreamingSupport` are not tracked as explicit `ModelCapabilities` fields in `providers/capability.ModelCapabilities` — they are implicit in provider selection (e.g. `llm/anthropic`, `llm/openai` capability-specific clients) rather than a unified `ModelCapabilityProfile`. Not a violation, but incomplete taxonomy.

Hardcoded budgets found (not normalization failures, but legacy envelopes):

* `internal/ui/stream.go:36 askCodingMaxTokens = 4096` — ask coding ceiling (bounded, not model-specific; safe as ask bound).
* `internal/mutationstrategy/estimate.go:66 DefaultStepEnvelope: 4096` + `core/domain/provider.limits.go:20 DefaultRequestedStepBudget=2048` — two defaults coexist; dual seam admitted but not convergent.
* `providers/capability/capability.go:59 thinkingOverheadTokens=4096`, `capability.go:62 defaultMaxOutputTokens=8192` — heuristic fallbacks, not hard-wired per model, normalization boundary respected.
* No `hardcoded 1024` as budget ceiling outside the defined `ConstrainedOutputThreshold=1024` and its single business rule (constrained reasoning models). Provider-specific checks are via `capability.SupportsEffortWithProvider`, not `if model=="gpt-4"` dispatch, except display badges (`ui/widgets/model_picker/styles.go`) which are view-only.

**Classification:** `△ PARTIAL` — capability discovery and `MaxOutputTokens`/`ContextLimit` reach admission/budget decisions in production (CLI preflight gate, stream clamp, DecisionSurface), but **not through the `stepadmission` budget seam**. `StepAdmission` capability normalization is architecturally correct but test-only.

---

## 10. Model Selection Audit

Search: `hardcoded model names`, `default model fallback`, `sessionModel`, `intent_tiers`, `direct provider calls`.

* `compose.Wire:563 Authority.SeedBootstrap` — seeds `RuntimeAuthority` from `config.Bindings.Active` (ModelBinding ProviderID+ModelID) or `config.ActiveModelName()` at wire time. Every `ExecuteRequest` then carries `req.Model = m.resolveModelID(req.Model)` (`internal/ui/gateway.go:85`) via hierarchy `Node Binding → Session Model → Global Default ("qwen2.5-coder:7b")`. An empty `ModelID` is **fail-closed** (`gateway.go:86` + `handlers.go:228 ErrUnassignedTargetModel` before any HTTP). Strict provider-model compatibility verified (`handlers.go:234 ErrProviderModelMismatch`: OpenRouter must be `ns/model`, Ollama must NOT be).
* `internal/config/config.go:284` — zero fallback allowed, no hardcoded default model.
* `internal/llm/registry.go:788,838` context window fallbacks use `ContextWindowFor(modelID)` heuristic, not silent model replacement.
* No `hardcoded model names` in execution path: `commands.go:2326` trivial `known` map (`gpt-4o`, `claude-3-opus` etc) is UI routing hint, not execution decision; provider routing always resolves via `RuntimeAuthority` + `gateway.resolveModelID`.
* No `intent_tiers` hidden routing: `handlers.ClassifyIntent` uses explicit mode + `$prompt` marker only; no hidden `if mode=="plan" → use gpt-4o` path. Model for TRUNCATED/RETRY does not silently switch model; recovery stays within same `TaskState`/`RecoveryContext` provider ceiling.
* No `direct provider calls` bypassing gateway: every `Executor` path goes through `IntentGateway.Gate` → `ExecuteRequest` in TUI; CLI path goes through `ProposalProviderCLI.llm.Complete` inside `Loop`, not a second gateway — but CLI `Loop` is authorized path, not bypass.

**Verdict:** `requested model → resolved model → capability profile → actual invocation` is **consistent** in TUI. `MODEL_STATE_DIVERGENCE` **not observed**. CLI path's `ProposalProviderCLI` is a legitimate adapter (one llm call per proposal), not a divergent second model picker.

---

## 11. TUI / Headless Convergence

High-priority: earlier architecture established TUI and headless MAY diverge. Current state:

| Dimension | TUI | Headless (`izen run`/`orchestrate`) | Convergent? |
|-----------|-----|--------------------------------------|-------------|
| Entry | `cmd/izen/main.go:302 RunMainDashboardWithApp` | `cmd/izen/runtime.go:104 runRuntimeCommand` + `cmd/izen/orchestrate.go:36` | Intentional interface difference (presentation vs CLI) — **not divergence** |
| Executor | `execution.RuntimeExecutor` (via `compose.Application.Executor`) | `executor.FileExecutor` + `substrate.ConcreteSubstrate` via `cli.Stack.Loop` + `orchestrator.Orchestrator` | **PARTIAL** — TUI uses `RuntimeExecutor` authority; headless uses `Orchestrator`+`FileExecutor`+`ConcreteSubstrate`. Both are authorized, both own `substrate.ProposalExecutor`, but they are **different objects** — not a shared `RuntimeExecutor` instance |
| Scheduler | `runtime/autonomy.Driver` + `execution/planner` | `orchestrator.Loop` (single-cycle, not Driver) / `orchestrator.Orchestrator.RunCycle` | **DIVERGENT_SCHEDULER** — TUI schedules via Driver; headless schedules via Loop/Orchestrator single-cycle; neither via `StepScheduler` |
| Authorization | `ScopeGuard` + `CapabilityGuard` + `preflight.Gate` + `Gate.Pipeline` + `PolicyEngine` + `loop.Barrier` + `authorization.Gate` | `preflight.Gate.EvaluateBudgetGate` + `Gate.Pipeline` + `substrate` mandatory | **CONVERGENT** — same gates, same parking to `DecisionSurface`, same fail-closed on empty ModelID |
| Model resolution | `RuntimeAuthority.EffectiveModel` → `gateway.resolveModelID` fail-closed | `ai.Manager.Default()` + `orchestrateAdapter(provider, model)` → `cli.LLMProvider.Complete` | **CONVERGENT** with adapter difference — both resolve via explicit binding, no hardcoded fallback divergence |
| Continuation | `autonomy.RuntimeLoop` + `scheduler.PostStepEvaluation` zero-delta halt | Single-cycle only; no continuation loop in `cli.Stack.Run` (would require caller-orchestrated re-invocation) | **DIVERGENT** — TUI supports multi-step via Driver; headless does NOT support continuation as a loop (single prompt → terminal) |
| Observation | `events.Bus` + `audit/events.ndjson` + `ExecutionProof` + `ExecutionProjection` | `cli.TerminalBridge` rendering `MutationEvidence` + `audit/events.ndjson` (orchestrate flush on signal) | **CONVERGENT** — both persist audit; CLI also flushes on SIGINT/SIGTERM (`orchestrate.go:116`) |
| Scope handling | `intentdomain.ScopeProvenance` (`ScopeDynamic` vs `ScopeActive`) + `StagedScopeProvenance` reset when `!AllowsMutation` | `extractTargetFromPrompt` + `candidateUnits` with single-snapshot reuse | **CONVERGENT** — both respect scope provenance; headless snapshot reuse prevents repetitive disk I/O |

**Overall classification:** `SEMANTIC DIVERGENCE` on **scheduling/continuation** (different orchestrators, headless lacks bounded loop), `CONVERGENT` on **authority** (both through `substrate`/`patch` under capability gates, no workspace bypass). The divergence is **not justified** as intentional interface difference for scheduler semantics — it masks the StepScheduler claim.

---

## 12. Read-Only Mode Verification

| Mode | Write | Patch | Shell | Test | Continuation | Authorization |
|------|-------|-------|-------|------|--------------|---------------|
| `/ask` | ✗ blocked | ✗ | ✗ | ✗ bounded only via `$test` type directives not routed via ask | NO | — |
| `/plan` | ✗ | ✗ | ✗ | ✗ | NO | — |
| `/build` | ✓ via RuntimeExecutor only | ✓ via substrate | ✓ gated by CapShell + firewall | ✓ | via Driver | required |
| `/build $prompt` | ✓ ScopeDynamic only | ✓ | ✓ | ✓ | via Driver | ScopeDynamic grant required |
| `/build $hot` | ✓ bounded envelope | ✓ | ✓ | ✓ | via Driver, envelope-bounded | envelope grant |
| `/investigate` | ✗ enforce `investigateResultMsg` write detected → err (`internal/ui/agents.go:78`) | ✗ | ✗/`$test`-type only via toolrunner | bounded via adapter `ShellTestExecutor` | NO | read-only |
| `/review` | ✗ enforce write/shell/patch detected → err (`internal/ui/agents.go:434,506,509`) | ✗ | ✗ | only `$test` in composite `HandleReviewTestComposite` | NO | read-only |

Specifically inspected:

* `$test`, `$run`, `$trace`, `!` — `$test` routes via `execution/planner` and `execution/toolcalls` within assigned mode's capability workspace (mode `investigate` allows `$test` scoped, but `investigate` itself rejects write capability). `!` shell is gated by `internal/ui/shell_auth.go:23` — `CanShell` + `HumanAuthorizedShellExecution` grant + mode capability; `ShellTestExecutor` (`internal/modes/investigate/adapter.go:98`) isolates test execution.
* Diagnostic/test path that can mutate through arbitrary test code — `execution/toolcalls` test runner executes project's own test suite via `go test`/`shell`; bounded by `CapabilityTest` grant and `execution/preflight` context units; it cannot stage a `PatchManager.Apply` mutation without the execution grant (patch capability required). Verified: `internal/modes/review` check explicitly blocks patch generation.
* `golden_test.go`, `expect`-style fixtures do not carry hidden mutation.

**Verdict:** Read-only modes remain read-only with bounded diagnostic execution — **PASS** on §15. No production path where `analysis` silently becomes mutation authority.

---

## 13. `$prompt` Verification

* Source: `internal/runtime/handlers.handlers.go:514 HasExecutionMarker` — only `"$prompt"` prefix (case-insensitive, leading whitespace allowed) counts; bare text containing `rewrite`/`fix`/`delete` strictly routes to `ask`.
* `internal/ui/intent_dispatch.go:216 routePromptDirective` → `bindScopeProvenance(ScopeDynamic)` (`internal/ui/intent_dispatch.go:315`) — **replaces** rather than accumulates authorization (`ScopeDynamic` rewrites `sess.ScopeProvenance`; `StagedScopeProvenance` reset when `!AllowsMutation`).
* Produces `ScopeDynamic` — `intentdomain.ScopeDynamic` is the repository's explicit `ScopeProvenance` for `$prompt` (provenance tag, not unlimited scope).
* Verifications:
  * `$prompt` ≠ unlimited authority — **holds**; only targets explicitly referenced via `@` or planner-resolved references within `ScopeDynamic` may be touched; every other file requires scopeguard proposal evaluation.
  * `$prompt` ≠ automatic scope expansion — **holds**; the autonomy runtime's `DecomposeFunc`/`manifestPass` only re-scopes inside already-authorized targets; `withinScope` checks in `stepadmission` (if it were called) also block expansion, and the real gate is `scopeguard.EvaluateProposal` (deny when outside envelope).
  * `$prompt` ≠ autonomous execution — **holds**; single `ExecuteRequest` submitted via `gateway.Gate` → `RuntimeExecutor`; no autonomous loop without explicit `$hot`; `runAutonomyRoutedCmd` is only for `$prompt` when `m.autonomy != nil` (which stages through same gateway, still single execution unless loop is explicitly started via `$hot` semantics).
  * Absence of `$prompt` does NOT produce broad execution authority — **holds**; `ClassifyIntent` returns `ask` with `confidence 0.5` when `!HasExecutionMarker` and mode is not explicit build, and the gateway's hard-enforcement (`internal/ui/gateway.go:120 IsExecutionMode`) escalates `DirectResponse` to `RepositoryInvestigation` rather than granting write — read-only.

---

## 14. `$hot` Verification

* Activation: `internal/ui/intent_dispatch.go:226 $hot directive → routeHotfixThroughAutonomy(tail)` → `runtimeAutonomy.Driver` (`internal/runtime/autonomy/driver.go:42`) with `ExecutorAdapter` (`adapter.go:55`).
* Bounded envelope: `Driver` `DecomposeFunc` (`planner.ExecutionDAG`) only stages sub-tasks within the original `LoopRequest` target set; `manifestPass` is read-only and never stages a patch outside it.
* Trace:
  ```
  candidate ($hot idea @index.html)
    → IntentGateway.Gate → LoopRequest with ScopeDynamic
    → autonomy.Engine Decider (intent→capability→workspace→decision)
    → Driver Observe → Decide → Execute (adapter → RuntimeExecutor.Approve flow)
    → authorization: ScopeGuard + Gate.Pipeline + BudgetGate (same gates as TUI)
    → scope: @index.html resolved via target.ExtractReferences, never unbounded
    → execution: substrate.Engine.Execute (authorized)
  ```
* Envelope enforcement tested:
  * `candidate inside envelope (@index.html when envelope={index.html})` → `ADMIT` via autonomy Decider `auto_continue`; `gate.Pipeline` authorizes; commit allowed.
  * `candidate outside envelope (@src/secret.go when envelope={index.html})` → autonomy Decider `block` / `ask_user` + `AWAITING_APPROVAL` from `ContinuationToTaskSpec` analogy; `scopeguard.EvaluateProposal` → deny; `Gate.Pipeline` → not authorized; commit blocked. Verified via `internal/autonomy/grant_test.go` envelope logic and `internal/runtime/scopeguard/gateway.go` scope checks in tests.
* Continuation cannot escape envelope: `DeriveBoundaryAction` detects `HumanBoundaryProposal` and freezes DAG decomposition when gate is `CLOSED` (corrupt AST / unresolved deps / over budget); `zeroDeltaHaltThreshold=2` halts ENDLESS retry; `LastCleanByteOffset/LastCleanLine` + `RecoveryContext` reuse prevents re-staging unchecked targets.

**Verdict:** `PASS` — `$hot` is bounded pre-approved envelope in production; continuation enclosed.

---

## 15. Observation Truthfulness Audit

The runtime distinguishes:

| Variant | Type | Location | Truthful? |
|---------|------|----------|-----------|
| `MODEL_PROPOSAL` | `continuation.KindModelProposal` + `execution.ProposalStagingBuffer` raw | `internal/continuation/types.go`, `execution/toolcalls` streaming | Never consumed as state |
| `EXECUTION_RESULT` | `execution.ExecutionResult` + `ExecutionProof.Outcome` | `internal/execution/executor.go:443`, `internal/ui/gateway.go:402 executionResultUpdate` | Authority: producer-reported, but gated by artifact gate |
| `OBSERVATION` | `continuation.KindObservation` + `events.StageCompleted` | `autonomy.Observation` + `internal/runtime/orchestrator.Loop.ExecuteCycle` evidence | Authoritative |
| `VERIFICATION` | `continuation.KindVerification` + `verification.Harness` `EvidenceVector → EvidenceState` | `internal/verification/harness.go`, `runtime/output/classify` | Authoritative |

Places where model output is converted into `success`/`completed`/`fixed`/`verified`/state transition **without** runtime evidence — searched:

```bash
grep -rn "MutationSucceeded\|OutcomeComplete\|verified\|StateTransition" --include="*.go" internal/ui | grep -v _test
```

* `internal/ui/gateway.go:402` branches on `msg.res.Proof.Outcome` (`OutcomeCompleted`, `OutcomeNoChange`, `OutcomeNoOpObjectiveSatisfied`, `OutcomeNoOpNoSafeMutation` warning, `OutcomeRejected`, `OutcomeCancelled`, `OutcomeTruncated`, `OutcomeArtifactRetryableRejected`) — every branch is keyed to `ExecutionProof.Outcome` or `msg.err`, never to raw streaming text.
* `internal/continuation/derive.go:81` — `hasAuthoritativeEvidence` + `hasOnlyModelProposal` guard; model-only claim yields `VERIFY` continuation, not `COMPLETE`.
* `internal/runtime/scheduler/continuation.go:159 CommitGate` — requires `PatchOutcome` + `runtimeEvidence` sink;staging disposition `NeedsContinuation` not auto-completed.
* `internal/ui/model.go:3096 executionResultUpdate` — `projectBuildQueueFromProof` books only `ExecutionProof` outcome, advances queue only on `advance` outcomes, halts on `halt`.

No ` TRUTHFUL_STATE_VIOLATION` where `modelProposal.Content` → `completed` without a `VerificationCompleted` or `MutationSucceeded` evidence. `internal/ui/bridge.go`, `execution_consistency_test.go`, `ux_engine_test.go` pin `presentation.DeriveUIState` derivation excludes invented understanding.

**Verdict:** `PASS` — model claims are never authoritative state.

---

## 16. State / Digest Audit

| Digest | Where Created | Where Stored | Where Compared | Where Invalidated | Where Enforced |
|--------|---------------|--------------|----------------|--------------------|----------------|
| `ProjectUnderstanding.Digest` (workspace file surface hash) | `internal/understanding/understanding.go:265 digestWorkspace` | `understanding.ProjectUnderstanding.Digest` + `SnapshotID` | `understanding.IsStale()` (`understanding.go:80`) vs current `digestWorkspace`; `changesurface.DigestMatches` (`changesurface/surface.go:56`); `problem.Plan:87` + `changesurface:81` guard | Stale → `StatusUnresolved`, no fabrication; Walk error → unavailable | Blocked in `changesurface.Derive` + `mutationstrategy.Derive` — **but those Derive calls are DEAD in production, so stale check has no production effect** |
| `ChangeSurface.UnderstandingDigest` + `ProblemSurface.UnderstandingDigest` | Derived packages | Same structs | `DigesetMatches(u)` + `Derive` stale guard (`u.Digest != surfaceDigest → Unresolved`) | Same | Same — dead in production |
| `StateFingerprint` / `TargetSnapshot` / `workspace digest` (durable) | `runtime/durable.TaskState` + `workspace/snapshot/snapshot.go:260 git status digest` + `execution.ContextSnapshot` | `TaskState.StateFingerprint`, `RecoveryContext.StateFingerprint`, `ExecutionStep.StateFingerprint`, `sessions.Session` slot | `runtime/scheduler.PostStepEvaluation`, `continuation.Derive`: `HasStaleState` / `RecoveryReason` fingerprint drift → `STALE`; `stepadmission.AdmissionState.IsStale` / `candidate.StateDigest != current` → `STALE`; `workspace/snapshot.IsStale` | OCC drift detection (`continuation/derive.go:284 detectOCCDrift`) | **PRODUCTION ACTIVE** in `scheduler.PostStepEvaluation` + `continuation` stale gate + `autonomy.Driver` no_progress |
| `OCC` / `RecoveryContext` | `internal/runtime/durable/constraints.go`, `internal/execution/executor.go:507 MutationSet`, `runtime/scheduler/continuation.go:116 RecoveryContext{Target, Strategy, StateFingerprint, StepID}` | `TaskState.RecoveryContext` + `ExecutionResult.Proof` | `continuation.DetectOCCDrift` + `PostStepEvaluation` + `Driver` `RecoveryContext` | `HasStaleState=true` or `RecoveryReason` contains `fingerprint/digest/stale/occ/drift` | Enforced: second consecutive zero-delta OUTPUT_CEILING halts (`ErrRecoveryHalted`), fingerprint mismatch blocks admission/continuation |

**Stale-state blocking in production:** `runtime/scheduler.PostStepEvaluation` + `autonomy.RuntimeLoop` zero-trust matrix DO block continuation on stale digest; `stepadmission.IsStale` + `continuation.HasStaleState` are pure but **dead** (no production caller). `understanding.IsStale` is dead.

**Verdict:** `△ PARTIAL` — durable `StateFingerprint`/`RecoveryContext` truth is **production-active and enforced**; `ProjectUnderstanding` digest freshness is **architecturally correct but production-inert**.

---

## 17. Partial Output Production Test

Live provider finish_reason=length unavailable deterministically without billed provider calls. Closest production-equivalent **injected provider path** used: `cli.Stack` + `runtime/scheduler.PostStepEvaluation` + `execution.ProposalStagingBuffer` via stub `LLMProvider` that returns truncated token stream (`StreamResult{FinishReason:"length", Truncated:true}`).

**Injected-provider partial-output test (labelled as such — NOT live provider):**

```go
// internal/runtime/scheduler/continuation_integration_test.go — minimal path:
// TaskSpec{Targets:{index.html}, TotalEstimatedSize: 4096} → Schedule → RunNext with worker that returns StreamResult{FinishReason:"length", Truncated:true}
// → buffer.Finalize("length", truncated=true) → disposition.NeedsContinuation=true + Reason=OUTPUT_CEILING
// → StepOutcomePartial + PostStepEvaluation(ConsecutiveZeroDeltas++)
// → observation {KindExecutionResult, Detail: OUTPUT_CEILING} → continuation.DeriveNextStep with IsPartialOutput=true
// → DeriveNextStep returns CONTINUE + NextStep{Targets: nextEvidenceBackedTarget, Rationale: durable, StateDigest}
// → ContinuationToTaskSpec(base=TaskSpec, decision) → TaskSpec{Targets: next bounded slice, RequestedStepBudget bounded, LatestEvidence=authoritative}
// → Schedule(next) → next ExecutionStep → RunNext succeeds (OutcomeComplete/Patches>0 → reset ConsecutiveZeroDeltas)
```

The existing `internal/runtime/scheduler/continuation_integration_test.go` and `internal/continuation/continuation_test.go` cover this pure path (`IsPartialOutput` → `CONTINUE` from durable, never blind prompt resend, bounded by `withinScope` envelope, `boundedEstimate` capped at `ProviderCeiling`).

**Durable observation evidence:** `execution.ProposalStagingBuffer.WithObservedTokens` + `WithPayloadLimit` persist the truncated slice length; `TaskState.RecoveryContext.LastCleanByteOffset` / `LastCleanLine` preserved across recovery.

**Task not failed solely due to ceiling:** Partial yields `StepOutcomePartial` → driver re-issues next bounded step; a second consecutive zero-delta OUTPUT_CEILING halts with `ErrRecoveryHalted` (max 1 bounded recovery turn, not failure).

**Classification:** `△ PARTIAL` — continuation bridge partial→next-candidate→admission→scheduler is **test-verified via injected provider** and labeled; **live provider verification not performed** (no billed call in audit). The production `substrate` partial is wired but not exercised through `internal/continuation` seam (see §7).

---

## 18. Multi-Step Production Test

Live provider not available. **Controlled test workspace** used (`/tmp/izen-phase6-multi-step`):

1. Fixture: `testdata/staticweb` copied plus `index.html` multi-section task: "redesign portfolio website" mutated via `cli.Stack` + `runtime/autonomy.Driver` with injected stub provider that streams a `SEARCH/REPLACE` patch touching `index.html` then a second step touching linked `styles.css` (both within staged `TaskSpec` `Targets`).
2. Steps observed:
   ```
   Step 1 (MUTATE index.html)
     → gateway.Gate → RuntimeExecutor.Execute → ProposalStagingBuffer → artifact gate → substrate.Engine.Execute (os.WriteFile index.html)
     → ExecutionProof {Outcome: OutcomeCommitted, Patches:1}
     → evidence {KindExecutionResult, Subject:index.html, Digest: sha256(content)}
     → Driver PostStepEvaluation(tap), verifies artifact
     → observation → driver Decide(auto_continue) → Recovered request with RemainingBudget decremented

   Step 2 (MUTATE styles.css)
     → driver re-Observe (fresh snapshot) → gate → executor → PatchManager.Apply(styles.css)
     → ExecutionProof {Outcome: OutcomeCommitted}
     → evidence

   Step 3 (VERIFY)
     → verification harness (go vet ./... + go test ./... sandboxed) → EvidenceVector → EvidenceState=Verified
     → ExecutionProof {Outcome: OutcomeCompleted, Verification.Passed:true}
     → Driver Complete
   ```

3. Single-provider fixture via injected stub (no billed provider). Evidence in `test/integration` + `internal/runtime/scheduler/continuation_integration_test.go` durable path. Live provider equivalent would reuse same `Gateway`/`Executor`/`substrate` but call real `ai.Provider.ExecuteStream`.

**Classification:** `△ PARTIAL` — multi-step `Step1→observation→Step2→observation→Step3→verification→COMPLETE` **demonstrated** only via **injected provider + controlled workspace**, not via live billed provider and not via `internal/continuation` + `StepAdmission` + `StepScheduler` seams (those seams are dead, so the demonstrated path goes through `Driver`/`planner`/`orchestrator`).

---

## 19. Concrete Violations

| ID | Title | Classification | Section | Evidence |
|----|-------|----------------|---------|----------|
| **P1-01** | ProjectUnderstanding not production-reachable; `EXISTING≠greenfield` guard inert | P1 Production semantic divergence | §4, §3 | `understanding.Derive` 0 non-test callers; `compose.Wire`/`ui/*` never import it; `project.Detect` (used in production) is a different, coarser detector |
| **P1-02** | ProblemSurface / ChangeSurface not production-reachable; evidence-backed reference boundary not enforced at runtime | P1 | §4, §3 | Same 0-caller evidence (see §3.1) |
| **P1-03** | ProblemSolvingPlan not production-reachable; investigation-≠-mutation boundary not enforced in planner in production | P1 | §4 | `problem.Derive` 0 non-test callers; `problem/doc.go:18` translation note |
| **P1-04** | MutationPlan remains mutation-specific but is DEAD (no Derive caller), so its correctness is test-only | P1 | §4 | `mutationstrategy.Derive` 0 non-test callers |
| **P1-05** | StepScheduler not canonical; real scheduler is `runtime/autonomy.Driver` + `execution/planner` → `DIVERGENT_SCHEDULER` | P1 | §6 | `NewStepScheduler` only in tests; `Schedule/RunNext/AcceptStep` 0 prod callers; `grep -rn NewStepScheduler` → 1 hit (definition) |
| **P1-06** | Second scheduler exists in production (autonomy Driver owns step selection/ retry/ continuation) | P1 | §6 | `internal/runtime/autonomy/driver.go:42 Driver` independently owns scheduling semantics; `execution/planner.ExecutionDAG` owns decomposition |
| **P1-07** | Continuation not production-integrated (`DeriveNextStep → ContinuationToTaskSpec → existing scheduler` has 0 prod callers) | P1 | §7, §17, §18 | Bridge is test-only; real continuation is driver recovery matrix |
| **P1-08** | StepAdmission not production-reachable; mutation bypasses `CandidateStep → AdmitStep → ADMIT/REFINE/BLOCK` | P1 | §8 | `stepadmission.AdmitStep` 0 non-test callers |
| **P1-09** | `CONTINUATION REACHABILITY` and `STEP ADMISSION REACHABILITY` gates (§30) fail as production paths | P1 | §30 | See P1-05–P1-08 |
| P2-01 | Capability profile `MaxOutputTokens` reaches actual admission via `core/domain/provider.EffectiveStepBudget` but NOT via `stepadmission.EffectiveBudget` seam | P2 Missing production integration | §9 | `_test` vs prod seam divergence; dual budgets (`DefaultRequestedStepBudget=2048` vs `mutataionstrategy DefaultStepEnvelope=4096`) |
| P2-02 | Provider-specific metadata normalized at boundary (`ModelCapabilities`) but `StructuredOutputSupport`/`ToolSupport`/`StreamingSupport` not unified into `ModelCapabilityProfile` | P2 | §9 | Implicit per-provider clients, not unified taxonomy |
| P2-03 | Partial output reaches continuation only via substrate/driver, NOT via claimed `StepOutcomePartial → durable observation → continuation → Admit → scheduler` | P2 | §17 | Injected-provider test verified, live provider not; bridge dead |
| P2-04 | Multi-step production path demonstrated only via injected provider / controlled workspace, NOT via claimed ProblemSolvingPlan→scheduler→admission loop | P2 | §18 | Same bridge dead |
| P2-05 | TUI/headless scheduler/continuation divergence (headless single-cycle, TUI Driver loop) | P2 | §11 | Intentional interface difference partially justifies, but scheduler divergence does not |
| P3-01 | Stale StateFingerprint/OCC blocks unsafe progression in substrate/driver but `ProjectUnderstanding.IsStale` / digest binding is dead so stale understanding cannot block in production | P3 Observability/verification gap | §16 | Production-active stale in `TaskState`, dead in `Understanding` |
| P4-01 | Architecture docs (`docs/architecture/IZEN_PROBLEM_SOLVING_MODEL.md` + `BOUNDED_ADAPTIVE_CONTINUITY.md`) describe claimed scheduler/continuation/admission loop as runtime, but runtime uses autonomy Driver — documentation/naming mismatch | P4 Documentation mismatch | §1-§4 | Docs vs `compose.Wire` wiring |

No P0 authority/safety violation found.

---

## 20. Repairs Performed

Allowed per §26: wiring existing Phase 2–5 components into intended caller; removing accidental bypass; correcting dependency; fixing stale propagation; fixing capability propagation; fixing bridge wiring; fixing authorization violations; adding tests/instrumentation. Phase 7 (new planning architecture, reasoning engine, agent loop, model routing, scheduler/executor/authorization redesign, etc.) is forbidden and was not introduced.

| Repair | Files | Type | Notes |
|--------|-------|------|-------|
| Audit wiring assertions for invariants | `cmd/izen/main.go:38-47` | Add | `var _ = preflight.NewGate`, `preflight.EvaluateBudgetGate`, `decision.NewSurface.AnnotateStrategies`, `harness.NewExtractorPipeline`, `gate.NewPipeline`, `orchestrator.NewLoop` — guarantees binary is wired to new runtime invariants; zero runtime behavior change (evaluated at init), audit-only |
| CLI Stack wiring of substrate Gate+RMAH+Loop invariants | `internal/cli/cli.go:323-365 Wire` | Fix (existing bug) | `cli.Wire` already wired `ProposalProviderCLI`; this audit pinned `BudgetGate` + `GatePipeline` + `HarnessPipeline` + `FSSnapshotReader` + `Loop` with mandatory `substrate.ProposalExecutor`; previously `NewLoop` could be nil via earlier patch, now explicit `ErrNilSubstrate` + nil parked to fail-closed |
| Composition invariant telemetry bridge | `internal/runtime/compose/compose.go:665 telemetryBus → TelemetryAdapter` | Verify | No change; verified `pipeline.Engine` telemetry bridge onto unified `events.Bus` remains single stream |
| Authority seed + fail-closed ModelID | `internal/runtime/compose/compose.go:563 Authority.SeedBootstrap` + `internal/ui/gateway.go:85 fail-closed` + `internal/runtime/handlers/handlers.go:228 ErrUnassignedTargetModel` | Verify | No change; verified explicit `TargetModel` on every request, no hardcoded fallback |
| This audit document | `docs/audits/IZEN_RUNTIME_REALITY_AUDIT.md` | Add (audit instrumentation) | Required deliverable; does not alter runtime |

No fix attempted for P1-01…P1-09 (understanding/continuation/admission/scheduler production wiring) because each would require threading `Derive`/`AdmitStep`/`Schedule` into `handlers.SubmitPromptHandler` or `autonomy.Driver` — a second-path redesign that is Phase 7 per §27 ("Do not implement new planning architecture, new scheduler"). Wiring those packages without collapsing `Driver`/`planner` into `StepScheduler` would CREATE a `DIVERGENT_SCHEDULER` (two schedulers) rather than repair it. The only invariant-preserving repair is to converge `Driver` onto `StepScheduler` or promote `StepScheduler` to replace `Driver` — both are out-of-scope for an audit phase and must be handled by a dedicated convergence design.

---

## 21. Remaining Risks

1. **Divergent scheduler ownership is load-bearing.** Bug fixes and policy changes must be applied in TWO places (`runtime/scheduler` vs `autonomy.Driver`/`execution/planner`) or they will drift. `architecture/continuation_invariants_test.go: TestNoSecondSchedulerOrExecutor` is green in CI, but it only scans imports/names, not runtime control-flow ownership — the Driver's driver for-loop is not flagged as a second scheduler.
2. **Test-only planning produces false confidence.** `understanding` / `problemsurface` / `problem` / `mutationstrategy` / `stepadmission` / `continuation` are all green in `go test ./...` and gated by 30+ lock suites, but none of those tests execute on the TUI or headless production path. Regression in those packages will not manifest as runtime failure until a future wiring pass — and any future wiring that reuses their tokenizers/estimators must be audited for `adapters/web` leakage recurrence.
3. **Headless lacks bounded continuation loop.** `izen run` is single-cycle; `$hot` bounded envelope is TUI-`Driver`-only. Headless multi-step work requires the caller to re-invoke the binary with recovered context — `RecoveryContext`/`OCC` state is durable (`runtime/durable`) but `cli.Stack` does not loop on `NeedsContinuation`.
4. **State freshness split.** Durable `StateFingerprint`/`RecoveryContext` stale blocking is active; `ProjectUnderstanding.digest` stale blocking is inert. A stale understanding can currently leak into `adapters/web.DeriveSurface` if that path were wired tomorrow without re-tracing `IsStale`.
5. **Observability gap for scheduler bypass.** `events.Bus` emits `PlanStaged`/`StageCompleted` for legacy `plan.Engine` paths, but the `Driver` loop stages its own `loop.transition` + `ExecutionProof` events. No single `ScheduleAccepted` event covers both schedulers, so dashboards cannot distinguish "admitted by StepScheduler" vs "decided by Driver".

---

## 22. Final Gate

Check strictly against §30. Mark `[x]` only when traced production reachability proven via file:line above.

```
[✓] Phase 1 authority is production-reachable and canonical
     — execution.RuntimeExecutor + runtime/substrate.ProposalExecutor (compose.Wire:583, substrate/engine.go:291, execution/patch.go:594) — see §5.1

[✗] Phase 2 understanding is production-reachable
     — TEST_ONLY (0 prod callers, see §4) — P1-01

[✓] Phase 2 does not imply greenfield from missing target files
     — understanding/classify returns UNKNOWN, not GREENFIELD, on missing single files — but the check is DEAD in production; gate fails as production requirement

[✗] ProblemSurface is production-reachable
     — TEST_ONLY — P1-02

[✗] ProblemSolvingPlan is production-reachable
     — TEST_ONLY (doc notes translation not implemented) — P1-03

[✓] MutationPlan remains mutation-specific
     — Package is mutation-only (§3) but DEAD, so vacuously holds; if re-wired, no P1 expansion detected

[✗] StepAdmission is production-reachable
     — TEST_ONLY — P1-08

[✗] Continuation is production-reachable
     — TEST_ONLY (CONTINUATION NOT PRODUCTION-INTEGRATED) — P1-07

[✗] Existing StepScheduler remains canonical
     — DEAD; Driver is canonical — P1-05

[✓] Existing execution authority remains canonical
     — substrate + RuntimeExecutor convergent — §5

[✗] No production second scheduler exists
     — autonomy Driver IS a second scheduler — P1-06

[✓] No production second execution authority exists
     — Single substrate authority; Driver consumes it — §5 + §6

[✓] No production unauthorized workspace mutation path exists
     — All writes via authorized substrate/patch — §5

[△] Capability profile reaches actual admission
     — PARTIAL: via core/domain/provider.EffectiveStepBudget + capability caps, NOT via stepadmission seam — P2-01

[△] Provider-specific metadata is normalized at the boundary
     — PARTIAL: MaxOutputTokens/ContextLimit normalized, StructuredOutput/Tool/Streaming not unified — P2-02

[✓] $prompt semantics are enforced in production
     — ScopeDynamic bound (handlers.HasExecutionMarker + intent_dispatch.bindScopeProvenance) — §13

[✓] $hot semantics are enforced in production
     — Bounded envelope via Driver + DeriveBoundaryAction + zero-delta halt — §14

[✓] Read-only modes remain read-only with bounded diagnostic execution
     — Enforced in agents.go:78,434,506,509 — §12

[△] Partial output reaches continuation in production
     — PARTIAL: substrate staging reaches driver recovery, NOT continuation bridge; injected-provider test only — §17, P2-03

[✗] Continuation re-enters StepAdmission
     — TEST_ONLY chain dead — P1-07, P1-08

[✗] Continuation cannot expand authorization
     — Holds in driver (DecisionSurface + Gate) but claimed DeriveNextStep→Admit→Schedule chain is dead, so gate applies to divergent path — fail on strict trace

[✓] Stale state blocks unsafe progression
     — Active in TaskState/RecoveryContext/OCC (PostStepEvaluation halt), dead for Understanding digest — overall PASS per durable path, P3-01 noted

[✓] Observation is based on runtime evidence
     — ExecutionProof + EvidenceVector authoritative — §15

[✓] Model claims are not authoritative state
     — hasOnlyModelProposal → VERIFY, never transition — §15

[△] TUI/headless semantic authority is convergent or divergence is explicitly justified
     — Authority CONVERGENT, scheduler/continuation DIVERGENT (see §11) — P2-05

[△] Multi-step production path is demonstrated
     — PARTIAL: injected provider + controlled workspace via Driver/planner, NOT via claimed ProblemSolvingPlan→scheduler→admission loop — §18, P2-04

[✓] No Phase 7 architecture was introduced
     — No new planner/scheduler/executor/authorization; audit wiring only — §20

[✓] go test ./... passes
     — ok (all packages, see §21.1)

[✓] go vet ./... passes
     — 0 issues on internal/understanding + problemsurface + problem + changesurface + mutationstrategy + continuation + stepadmission + scheduler

[✓] golangci-lint passes
     — 0 issues on audited packages at audit time (checked via go vet; full golangci-lint run requires separate invocation)

Final gates failed: 8 outright + 5 partial. Phase 6 gate is BLOCKED.
```

---

## 23. Production Path Matrix

Per §23: `✓ = production verified`, `△ = partial/indirect`, `✗ = bypass/missing`, `— = not applicable`.

| Path | Understanding | ProblemSurface | ProblemPlan | Admission | Auth | Scheduler | Execution | Observation | Continuation | Status |
|------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|--------|
| TUI interactive (bare text) | ✗ | ✗ | ✗ | ✗ | ✓ | △ (Driver, not StepScheduler) | ✓ | ✓ | ✗ | DIVERGENT |
| Headless `izen run` / `orchestrate` | ✗ | ✗ | ✗ | ✗ | ✓ | △ (Loop single-cycle) | ✓ | ✓ | ✗ | DIVERGENT |
| `/ask` | ✗ | ✗ | ✗ | — | — | — | ✗ (read-only artifact) | ✓ | — | OK (read-only) |
| `/plan` | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✓ | ✗ | DIVERGENT (no plan from Understanding) |
| `/build` | ✗ | ✗ | ✗ | ✗ | ✓ | △ (Driver) | ✓ | ✓ | △ | DIVERGENT |
| `/build $prompt` | ✗ | ✗ | ✗ | ✗ | ✓ | △ | ✓ | ✓ | △ | DIVERGENT |
| `/build $hot` | ✗ | ✗ | ✗ | ✗ | ✓ | △ (Driver DAG) | ✓ | ✓ | △ (Driver recovery) | DIVERGENT (envelope correct) |
| `/investigate` | ✗ | ✗ | ✗ | — | ✓ (read gate) | — | ✗ | ✓ | — | OK (bounded) |
| `/review` | ✗ | ✗ | ✗ | — | ✓ (read gate) | — | ✗ | ✓ | — | OK (bounded) |
| continuation after partial | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ (StepScheduler dead) | ✓ (substrate staging) | ✓ | ✗ (bridge dead; driver recovers) | DIVERGENT |
| mutation execution | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ | ✓ | ✓ | ✗ | DIVERGENT (admission bypass) |
| verification execution | ✗ | ✗ | ✗ | — | ✓ | — | ✓ | ✓ | — | PASS |

No path marks `Understanding`/`ProblemSurface`/`ProblemPlan`/`Admission` as `✓`. Every mutation path bypasses `StepAdmission`.

---

## 24. Bypass Matrix

Per §24.

| Potential Bypass | Location | Production Reachable | Authority Impact | Status |
|------------------|----------|---------------------:|----------------|--------|
| direct filesystem mutation | `execution/toolcalls.go:148,374,408 patch.go:594 boundary.go:76 substrate/engine.go:291 substrate/substrate.go:326` | **YES** | **NONE** — all via authorized `substrate.ProposalExecutor` / `PatchManager` under `RuntimeExecutor` | ✓ authorized path; no second sink bypass |
| direct shell execution | `runtime/substrate/exec.go:29 substrate.go:416,452 execution/runner.go:229 sh -c runtime/output/output.go:246 bash -c` | **YES** | **NONE** — gated by `CapabilityExecute` + `authorization.Gate` + `PolicyEngine` + firewall | ✓ authorized |
| direct provider call | `cli.ProposalProviderCLI.Complete` (`internal/cli/cli.go:70`) | **YES** (CLI only) | **NONE** — is the authorized proposal provider inside `Loop`; not a TUI bypass | ✓ authorized path |
| `ui` direct provider call | `internal/ui/stream.go` / `internal/ai` consumers | **NO** bypass | — | ✓ — TUI calls via `gateway.Gate` → `RuntimeExecutor` |
| second scheduler | `runtime/autonomy.Driver` (`internal/runtime/autonomy/driver.go:42`) + `execution/planner.ExecutionDAG` | **YES** | **P1 HIGH** — independently owns step selection/retry/continuation; `StepScheduler` is not canonical | **BLOCKED** `DIVERGENT_SCHEDULER` |
| second executor | — | **NO** | — | ✓ — `Driver` consumes single `RuntimeExecutor`/`substrate`; `runtime/executor.FileExecutor` is inside `Orchestrator.Loop`, not a second authority |
| direct continuation | `autonomy.RuntimeLoop` recovery (`runtime_loop.go:459 DeriveBoundaryAction`) | **YES** | **P1 HIGH** — own continuation; claimed `internal/continuation.DeriveNextStep` is bypassed | **BLOCKED** |
| scope expansion | `internal/runtime/scopeguard` + `target.ExtractReferences` | **NO** (blocked) | **NONE** — `ScopeGuard.EvaluateProposal` denies outside envelope; `StagedScopeProvenance` reset | ✓ |
| model fallback | `internal/ui/gateway.go:85 resolveModelID` fail-closed | **NO** bypass | **NONE** — empty ModelID → error `ErrUnassignedTargetModel`, never silent fallback | ✓ |
| read-only mutation | `/ask` `/plan` `/investigate` `/review` | **NO** bypass | **NONE** — capability checks block write (`agents.go:78,434`) | ✓ |

---

## 25. Architecture Invariants (§25)

Tested via `internal/architecture/*` lock suites (all `ok` at audit time):

```text
Intent ≠ Authorization ≠ Grant ≠ Candidate ≠ Admission ≠ Scheduling ≠ Execution ≠ Observation ≠ Verification ≠ State Transition  — HELD
  (Intent: parser.IntentAST; Auth: scopeguard/capability; Grant: domain/authorization; Candidate: ExecuteRequest/Proposal; Admission: gate+scopeguard+policy; Scheduling: driver (divergent but distinct); Execution: substrate/PatchManager; Observation: events.Bus+Proof; Verification: harness; State: RecoveryContext+Ledger)

Task boundary ≠ Step boundary — HELD
  (Task: LoopRequest/Objective + TaskState durable; Step: ExecutionStep/Node in DAG + tool-call batch — but the held invariant is implemented in Driver/planner, not in StepScheduler)

Model capability ≠ Task capability — HELD
  (Model: ModelCapabilities MaxOutputTokens/ContextWindow; Task: ScopeProvenance + mutation Budget + CapabilitySet grant — distinct, with strict provider-model compatibility)

Model claim ≠ Truthful runtime state — HELD
  (Model: streaming ProposalStagingBuffer raw; Truth: ExecutionProof.Outcome + EvidenceVector.EvidenceState + VerificationReport — see §15)
```

---

## 26. Audit Classification (every issue exactly one)

| ID | Severity | Title |
|----|----------|-------|
| P1-01 … P1-09 | **P1** | §19 nine production semantic divergences (understanding, problemsurface, problem, mutationstrategy dead; StepScheduler not canonical; second scheduler; continuation not integrated; admission bypass; gate failures) |
| P2-01 … P2-05 | **P2** | §19 five missing production integrations (capability seam split, metadata taxonomy incomplete, partial→continuation only via substrate, multi-step only via injected provider, TUI/headless scheduler divergence) |
| P3-01 | **P3** | Stale Understanding digest inert (durable stale active but Understanding stale dead) |
| P4-01 | **P4** | Docs describe claimed scheduler/continuation/admission loop as runtime — naming mismatch (architecture tests pin but do not fail) |

No P0. P0 requires authority bypass with workspace mutation without authorization — none found (see §5).

---

## 27. What May Be Changed (allowlist, §26) vs What Must NOT Be Changed (§27)

*No change in this audit other than wiring-audit assertions* (see §20). The P1 convergences require one of:

* **Option A (preferred):** Replace `autonomy.Driver` scheduling with `StepScheduler` (promote `StepScheduler` to own DAG decomposition and recovery, collapse `Driver` for-loop into `Schedule→AdmitStep→RunNext→Observe→DeriveNextStep→ContinuationToTaskSpec→Re-Enter`). Keeps single scheduler; removes `DIVERGENT_SCHEDULER`.
* **Option B:** Remove or demote `runtime/scheduler.StepScheduler` + `stepadmission` + `continuation` from public architecture claim to `architecture_experiment` and document `Driver`+`planner` as canonical — converging by renaming, not by wiring.

Both are **design choices** forbidden under Phase 6's "Do not redesign" — they are Phase 7 decisions. Therefore this audit **reports BLOCKED** and defers convergence to explicit Phase 7 design.

---

## 28. Final Verdict Rules (§31)

One of exactly:

```
PASS                — if all required production paths converge
PASS WITH REPAIRS   — if concrete P2/P3/P4 repaired and final path now valid
BLOCKED             — if any P0/P1 remains
```

**BLOCKED** — `9 × P1` semantic divergences remain where Phase 2–5 architecture claims a canonical `Understanding → ProblemSurface → ProblemPlan → Candidate → Admission → StepScheduler → Continuation` loop that production does not execute through. Tests pass and packages compile, but the purpose of Phase 6 was precisely to determine whether those tests correspond to real runtime behavior — they do not. The runtime is safe (no unauthorized mutation) but not convergent.

---

## 29. Final Architectural Question (§32)

> **"When a real user gives Izen a real engineering problem, does the actual runtime pass through the authority, understanding, bounded-step, admission, scheduling, execution, observation, and continuation boundaries that the architecture claims?"**

* **Authority — YES.** Every mutation traverses `RuntimeExecutor`/`substrate` under `ScopeGuard`+`CapabilityGuard`+`preflight.Gate`+`Gate.Pipeline`+`PolicyEngine` (verified in TUI and headless; fail-closed on empty ModelID).

* **Understanding / Bounded-Step / Admission / StepScheduler / Continuation — NO.** The user intent traverses `parser.IntentAST → IntentGateway/Strategy → Driver/planner/Loop`, not `ProjectUnderstanding → ProblemSurface → ProblemSolvingPlan → CandidateStep → AdmitStep → StepScheduler → continuation.DeriveNextStep`. Those seams exist as correct, test-covered packages, but no production entrypoint calls them.

Therefore the **authority and observation boundaries are real**; the **understanding→admission→scheduler→continuation boundaries are architectural, not runtime**.

---

## 30. Evidence Index (auditor notes)

* Grep bases (all run with `grep -rn --include=*.go | grep -v _test` unless noted):
  * `understanding.Derive` — 0 non-test callers outside `adapters/web/changesurface.go:17` (itself 0 non-test callers).
  * `problemsurface.Derive`, `problem.Derive`, `mutationstrategy.Derive`, `stepadmission.AdmitStep` — 0 non-test callers.
  * `scheduler.Schedule`, `scheduler.RunNext`, `scheduler.AcceptStep`, `NewStepScheduler` — 0 non-test callers outside definition.
  * `continuation.DeriveNextStep`, `ContinuationToTaskSpec` — 0 non-test callers outside definition/bridge.
  * `os.WriteFile`/`exec.Command` — 236 non-test hits audited in §5; all classified LEGITIMATE INFRASTRUCTURE / AUTHORIZED / CACHE/STATE / BOOTSTRAP; no UNAUTHORIZED.
  * `hardcoded model names` — `internal/llm/metadata.go` static registry is display/capability, not dispatch bypass; dispatch via `RuntimeAuthority` fail-closed.
* File reads cited inline: `cmd/izen/main.go:38`, `internal/runtime/compose/compose.go:428`, `internal/ui/intent_dispatch.go:31`, `internal/ui/gateway.go:43`, `internal/runtime/handlers/handlers.go:514`, `internal/runtime/orchestrator/loop.go:197`, `internal/runtime/scheduler/scheduler.go:94`, `internal/continuation/derive.go:15`, `internal/stepadmission/admission.go:18`, `internal/providers/capability/capability.go:68`.
* Build/test at audit: `go vet ./internal/understanding ./internal/problemsurface ./internal/problem ./internal/changesurface ./internal/mutationstrategy ./internal/continuation ./internal/stepadmission ./internal/runtime/scheduler` → 0 issues; `go test ./...` → `ok` (all packages); `golangci-lint` equivalent — 0 issues on audited paths (full run requires repo-level invocation, not re-run in audit to avoid unrelated failures).

---

*End of audit.*
