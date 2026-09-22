# PHASE 7 — CANONICAL RUNTIME CONVERGENCE

> Design phase only. No production code was modified in this phase (beyond read-only
> `rg` reachability probes). All reachability claims below distinguish **production
> callers** (non-`*_test.go` files reachable from `cmd/izen/main.go`) from test-only
> callers. Index generation `2026-09-21T16:04:40Z`, project
> `Users-anvndev-Documents-Project-OpenSource-PizenLabs-izen`; every cited path
> returned `no_recorded_issue` from coverage check (best-effort, not completeness
> proof — key seams were additionally verified with `rg` + `Read`).

---

## 1. Executive Finding

**The claimed bounded-step/admission/continuation runtime is not the production
runtime. The production runtime is `IntentGateway → autonomy.Driver →
ExecutorAdapter → execution.RuntimeExecutor → substrate`, with decomposition
supplied by the pure `execution/planner` and single-cycle headless service from
`runtime/orchestrator.Loop` / `cli.Stack`.**

Concretely, verified by `rg` over `internal/ cmd/` excluding `*_test.go`:

- `understanding.Derive`, `problemsurface.Derive`, `mutationstrategy.Derive`,
  `stepadmission.AdmitStep`, `continuation.DeriveNextStep` /
  `DeriveContinuation` / `ContinuationToTaskSpec`, `runtime/scheduler.StepScheduler.Schedule` /
  `RunNext` / `NewStepScheduler` have **zero non-test callers**. The only
  non-test hits are the definitions themselves (`stepadmission/admission.go:18`,
  `continuation/derive.go:15`, `runtime/scheduler/scheduler.go:98`,
  `runtime/scheduler/continuation_bridge.go:131` re-export) plus one doc comment
  (`scheduler.go:97`). Every other hit is `*_test.go` or `internal/architecture/*_test.go`.
- The single production execution-authority binding is
  `internal/runtime/compose/compose.go:585`
  `a.Executor = execution.NewRuntimeExecutor(...)` with
  `internal/runtime/compose/compose.go:586`
  `a.Gateway = execution.NewIntentGateway(...)`.
- The single production TUI multi-step loop is
  `internal/runtime/autonomy/driver.go:251 Driver.Run`, wired at
  `internal/runtime/compose/compose.go:791-793` (`NewExecutorAdapter` → `NewDriver`)
  and entered from `internal/ui/autonomous.go:43 executeAutonomyViaDriver`.
- Headless `izen run` (`cmd/izen/runtime.go:107`) and `izen orchestrate|prompt`
  (`cmd/izen/orchestrate.go:40`) are **single-cycle** by design: they never enter
  `Driver`, `StepScheduler`, `AdmitStep`, or `DeriveNextStep`.

**Canonical decision (Option B, Driver-centric, §7):** keep
`autonomy.Driver + execution/planner + execution.RuntimeExecutor` as the canonical
production orchestration model. **Do not promote `runtime/scheduler.StepScheduler`
to canonical.** Demote `runtime/scheduler.StepScheduler`, the `stepadmission`
package-as-runtime, and the `continuation`-as-runtime chain to
`architecture_experiment` (or delete), while **selectively reusing their pure
logic** (`AdmitStep` bound-check, `DeriveNextStep` transition function) *inside*
the Driver if Phase 8 wants them — as library calls, never as a second scheduler.
The documentation must then describe the Driver/planner runtime as the actual
architecture.

This is the smallest change satisfying §6: **1 owner kept, 2 duplicate executors
+ 2 duplicate schedulers deleted/demoted, 0 new loops introduced.**

Answers to the implementation gate (§13):

| Question | Answer (single owner) |
|---|---|
| Who owns step selection? | `runtime/autonomy.Driver` (bounded loop; decomposition via `execution/planner`) — `driver.go:251,850,1053` |
| Who owns admission? | `execution.RuntimeExecutor` pipeline (`verifyIntentContext` → `admission.Admit` → strategy/targets/observe) — `executor.go:1039-1121`; intent pre-admission in `execution/intent.go:83 Gate` |
| Who owns continuation? | `Driver` recovery matrix + resume intents (`recovery.go:72`, `ResumeApprove/Reject/Clarify/WithProposal`, `observeAndRun:887`) |
| Who owns execution authority? | `execution.RuntimeExecutor` (sole prod binding `compose.go:585`) → `substrate` side-effect sink |
| Who owns evidence? | `execution.RuntimeExecutor` (`ExecutionProof` + `ExecutionEvidence`, `evidence.go:24`) → `events.Bus` → UI projection; durable ledger via `runtime/durable` |
| How does a recovered step re-enter? | Same Driver loop: `Recovering(repair)` → `adapter.Execute` with same `ContractID`/attempt++ (transport) or rewritten bounded contract (OUTPUT_CEILING/refine) or `StagedSubTasks` DAG re-entry; human resume re-enters via `Resume*` without a second scheduler |

---

## 2. Current Production Runtime

### 2.1 Exact production call graph (with file/function references)

```text
cmd/izen/main.go:82 main()
 ├─ Phase-1 dispatch (:84-147): `run` → runRuntimeCommand (:119)
 │                              `prompt|orchestrate` → runOrchestrateCommand (:135,141)
 │   else → TUI: RunMainDashboardWithApp (:327) / RunRollbackEngine (:325)
 ├─ compose.Wire(...) (:226) — SINGLE TUI composition root
 │   ├─ execution.NewRuntimeExecutor (:585)          ← canonical authority
 │   ├─ execution.NewIntentGateway (:586)            ← intent authority
 │   ├─ runtimeOrchestrator.New(...).WithEventBus.WithPipeline (:754) ← phase shell
 │   ├─ runtimeAutonomy.NewExecutorAdapter (:791) → runtimeAutonomy.NewDriver (:793)
 │   └─ autonomy.NewEngine (:782), policy.NewPolicyEngine (:828)
 └─ workspace lock (:262), audit flush (:338)
```

**TUI BUILD path (production multi-step):**

```text
User input (Bubble Tea)
 → internal/ui/commands.go:349,491 handleInput
 → internal/ui/intent_dispatch.go:31 intentFromInput
 → internal/parser/parser.go:32 ParseInWorkspace → parser/ast.go:64 IntentAST
 → internal/ui/intent_dispatch.go:213 dispatchDirectives
 → internal/ui/intent_dispatch.go:287 routePromptDirective / :228 routeHotfixThroughAutonomy
 → internal/ui/autonomy_route.go:27 runAutonomyRoutedCmd
 → internal/autonomy Classify→Route→Controller (:55 dispatchAutonomyTrace)
 → internal/ui/autonomy_route.go:140-151 BUILD branch
 → internal/ui/autonomous.go:43 executeAutonomyViaDriver            [Phase-6 PROD]
 → internal/runtime/autonomy/driver.go:251 Driver.Run
 → internal/runtime/autonomy/driver.go:850 observeAndRun loop
      Deciding → Executing → Recovering → AwaitingHuman
 → internal/runtime/autonomy/adapter.go:180 ExecutorAdapter.Execute
      (:92 Resolve via IntentGateway.SelectStrategy; :244-262 ResolveModel;
       :293 StagedSubTasks; :390 driftObservation Boundary-5; :441 observe)
 → internal/execution/planner (pure DAG, only on OutcomePreflightInfeasible)
      internal/runtime/autonomy/decomposition.go:984 stageDecomposition
      via planner/planner.go:42-62, decompose.go, dag.go, ast.go, block.go, semantic.go
 → internal/execution/executor.go:1009 RuntimeExecutor.Execute
      verifyIntentContext (:1039) → admission.Admit (:1079) → selectStrategy (:1923)
      → targets (:1099) → observeTargets (:1117) → provider p.Execute (:2651,2805)
      → PatchManager.Apply (patch.go:594) → Verifier.RunAll → approval hold
      → OCC pre-commit → ExecutionProof + ExecutionEvidence → runtimegraph.Graph (:1033)
 → internal/runtime/substrate/substrate.go:179 Execute / :191 ExecuteUnit
      (verifyUse :111, substrateRel :138, ErrWorkspaceEscape :24)
      → engine.go:151 ConcreteSubstrate.Execute → os.WriteFile (:291 unit path)
 → ExecutionResult → internal/ui/gateway.go:345 handleGatedExecution
      → :360 executionResultUpdate → Bus events/EventApprovalRequired
 → events.Bus → internal/ui/program.go:311-337 → domainEventMsg
      → internal/ui/model.go:1955 handleDomainEvent
      → presentation.ExecutionProjection.Project
 → Driver.observeAndRun:887 recovery matrix (recovery.go:72 RecoverySubtype)
      → ResumeApprove (:374) / ResumeReject (:425) / ResumeClarify (:463)
        / ResumeWithProposal (:504) → SAME loop re-entry (next step)
```

**TUI non-BUILD / read-only path (production, no Driver):**

```text
→ internal/ui/gateway.go:68 runGatedLine (IntentGateway.Gate :83 + SelectStrategy :72)
→ internal/ui/runtime_cutover.go:39 runRuntimeExecuteCmd / :127,316,349 runtime task/prompt
→ execution.RuntimeExecutor.Execute (same authority, single-step, no Driver loop)
```

**Headless paths (production, single-cycle each — no continuation loop):**

```text
izen run (cmd/izen/runtime.go:107 runRuntimeCommand)
 → app.NewPipeline (:163 WithGenerator/WithIntentCompiler/WithSubstrate(NewConcreteSubstrate))
 → pipeline.Run (:269) + publishRunLifecycle (:377)

izen orchestrate|prompt (cmd/izen/orchestrate.go:40 runOrchestrateCommand)
 → cli.Wire (:81,97) → stack.Run (:86,134)
 → orchestrator/engine.go:101 RunCycle (preflight→proposal→validate→snapshot→arm→authorize→commit)
```

**Orchestrator TUI shell (phase lifecycle, NOT per-step mutation authority):**

```text
internal/runtime/orchestrator/manager.go:70 PhaseManager (RuntimeLoop :204, ExecuteCycle :260)
internal/runtime/orchestrator/loop.go:269 Loop.ExecuteCycle (ModelOutput→RMAH→Gate→commit)
wired compose.go:754. TUI mutation does NOT go Driver→Orchestrator→Executor;
it goes Driver→Adapter→execution.RuntimeExecutor directly.
```

### 2.2 Ownership matrix (production responsibilities)

| Responsibility | Owner (package · type/fn · file:line) | Inputs → Outputs | State owned | Authority owned | Production reachability |
|---|---|---|---|---|---|
| Intent parsing | `parser` · `Parse/ParseInWorkspace` · `parser/parser.go:19,32` · `IntentAST` `ast.go:64` | raw line → `IntentAST` + `RequiredPerms` | none (pure + workspace path) | **intent syntax only**; no mutation/scheduling | PRODUCTION via `ui/intent_dispatch.go:31` |
| Intent resolution / strategy path | `execution` · `IntentGateway.Gate/SelectStrategy` · `intent.go:83,72` + `strategy/selector.go:95 Select` | prompt → `ExecuteRequest` + `IntentResolution` + frozen `ContextSnapshot` | frozen context digest | **intent authority** (directive strip, read-only downgrade, scope-auth error); never calls provider | PRODUCTION via `ui/gateway.go:68`, `ui/runtime_cutover.go:127,316,349`, `autonomy/adapter.go:92,192` |
| Capability/risk routing | `autonomy` · `Engine.Decide` + `runtime/autonomy/engine.go:97 Evaluate` | objective → DirectResponse/AskUser/Block/auto_continue | DecisionSurface lifecycle | ask-ceiling only; no mutation | PRODUCTION via `ui/autonomy_route.go:27` |
| **Step selection + ordering + recovery entry (canonical scheduler)** | `runtime/autonomy` · `Driver.Run/step/observeAndRun` · `driver.go:251,1053,850` | objective → bounded step sequence | runID, LoopBounds, DAG, manifestPass, runCtx, DecisionSurface | **scheduling + recovery only**; never reads file / invokes provider / mutates FS (`driver.go:38-41`) | PRODUCTION via `compose.go:793` + `ui/autonomous.go:43` |
| Translation + Boundary-5 | `runtime/autonomy` · `ExecutorAdapter.Execute/observe` · `adapter.go:180,441` | LoopRequest → ExecuteRequest; Result → Observation | ContractID lineage, recovery contract rewrite | none (revalidates digest, rewrites recovery contract) | PRODUCTION (Driver subordinate) |
| Decomposition (pure) | `execution/planner` · `Decomposer` · `planner/planner.go:138` | oversize target → contiguous budget-bounded DAG | none (pure) | none | PRODUCTION via `autonomy/decomposition.go:984` |
| **Admission (executable-step gate)** | `execution` · `RuntimeExecutor.Execute` pipeline · `executor.go:1009,1039-1121` (`verifyIntentContext`, `admission.Admit`, strategy, targets, `observeTargets`) | ExecuteRequest → admitted strategy+targets | ContractID/attempt, baseline snapshot | **execution admission** (risk-scope-contract); authorization itself via guards below | PRODUCTION via `ui/gateway.go:184,277`, `ui/runtime_cutover.go:57`, `adapter.go:360` |
| **Execution authority** | `execution` · `RuntimeExecutor` · `executor.go:443,1009` (+ `Approve:1582`, `Reject:1844`) | admitted request → `ExecutionResult` + `ExecutionProof` + `ExecutionEvidence` | MutationSet txn, pending patch, OCC baseline, runtimegraph | **sole mutation authority** (provider invoke, patch apply, verify, commit) | PRODUCTION single binding `compose.go:585` |
| Side-effect sink | `runtime/substrate` · `Substrate.Execute/ExecuteUnit` · `substrate/substrate.go:179,191` | Proposal/unit → `ExecutionProof` | scopeRoot FD, snapshots for rollback | **execution-time confinement** (`verifyUse`, `ErrWorkspaceEscape`, AST re-anchor) | PRODUCTION via executor + CLI loop |
| Scope authorization | `runtime/scopeguard` · `ScopeGuard.Check/Enforce` + `IntentGateway.Authorize` · `scope/scope.go:66,100`, `gateway.go:109` | Proposal → ScopeResult | authorized scope list | scope veto (fail-closed) | PRODUCTION (pipeline subordinate; `engine.go:82-83,171`) |
| Capability authorization | `core/domain/authorization` · `SimpleCapabilityGuard.Evaluate` · `formula.go:128` + `core/authorization/engine.go:89 Evaluate` | proposal+patch+caps+budget+approval → decision | none (stateless formula + engine wiring) | 8-clause veto incl. `ClauseApproval` | PRODUCTION via `runtime/executor/executor.go:134-136`, `compose.go:828,831` |
| Budget preflight | `runtime/preflight` · `Gate.EvaluateBudgetGate` · `gate.go:131` + `runtime/gate` · `Pipeline.Evaluate` · `pipeline.go:133` | target+caps → BudgetGateResult / GateResult | BudgetAdvisor | pre-model veto; no mutation | PRODUCTION via `cli.go:431`, handlers `BackgroundPreflight`, `orchestrator/loop.go:313` |
| Policy | `domain/policy` · `PolicyEngine.Evaluate` · `engine.go:43` | action+ctx → Verdict | CapabilityGraph | mode-boundary + high-risk `REQUIRE_APPROVAL` | PRODUCTION via `core/authorization/engine.go:44-87`, `compose.go:828` |
| Approval session | `runtime/authorization` · `ApprovalGate.Evaluate` · `gate.go:125` + `core/workflow/machine.go:46 MarkApprovalPending` | ApprovalEvent → Authorized/Rejected | epoch, pendingApproval | human-approval gate (UI read-only while pending) | PRODUCTION (TUI surfaces + headless `[y/N]/i`) |
| OCC / staleness | `execution/occ.go:20-54 OCCVerifier` + `core/domain/occ/gate.go:28 ValidateAndAdvance` + `durable.ComputeTreeDigest` (`durable/digest.go:22`) | baseline+current → advance/abort STALE | StateVersion clock, baselines | abort-on-conflict (pre-commit) | PRODUCTION (executor `Approve` + `Proof.OccAborted`) |
| Checkpoint / rollback | `core/domain/checkpoint` · `DiskCheckpointCoordinator` · `coordinator.go:62,79,161` | build → snapshot id → restore | snapshots, taint flag | **sole rollback authority** (only via WorkflowStateMachine + RuntimeExecutor) | PRODUCTION |
| Evidence → observation | `execution` · `ExecutionOutcome/Evidence` · `evidence.go:24` + `graph/graph.go` → `events.Bus` → UI projection | result → immutable evidence + bus events | ContractID lineage, digests | **truth production** (only runtime produces it) | PRODUCTION |
| Recovery / continuation entry | `runtime/autonomy` · `recovery.go:72` matrix + `runtime_loop.go:459 DeriveBoundaryAction` + `Resume*` intents | observation → retry/re-scope/halt/park | RecoveryAttempt, WorkspaceDigest, StagedPlan | re-entry decision (same loop) | PRODUCTION (Driver loop) |
| Durable task state | `runtime/durable` · `TaskState/RecoveryContext/LedgerEvent` | events → ledger | ledger, consecutiveZeroDeltas | ledger truth (no auth) | PRODUCTION |

---

## 3. Claimed Architecture

```text
ProjectUnderstanding → ProblemSurface → ProblemSolvingPlan [→ MutationPlan]
 → CandidateStep → StepAdmission (ADMIT|REFINE|BLOCK) → StepScheduler
 → Execution → Observation → Continuation → (re-enter StepAdmission)
```

Each boundary is a pure, I/O-free package by design (`stepadmission/doc.go:21`,
`continuation/doc.go:4`, `problem/doc.go:15` "translation not implemented").
The intended invariants (from `docs/architecture/IZEN_BOUNDED_ADAPTIVE_CONTINUITY.md`,
`IZEN_PROBLEM_SOLVING_MODEL.md`, `stepadmission/*`, `continuation/*`) are sound
as *policy-type* design: capability-bounded admission, stale-digest refusal, no
authorization invention, no direct execution/scheduling from continuation
(`continuation_test.go: TestCONT04-08,16,17`).

The problem is not the policy design — it is that **none of it executes in
production** (§4).

---

## 4. Divergence Map

| Claimed Boundary | Current Production Owner | Existing Seam | Reachable? | Required Action (§8) |
|---|---|---|---|---|
| ProjectUnderstanding | **none** (no prod owner) | `understanding.Derive` · `internal/understanding/understanding.go:204` | NO — 0 non-test callers (all hits in `*_test.go`: `understanding_test.go:28+`, `architecture/*_test.go`, `mutationstrategy/plan_test.go:41+`) | Demote to experiment/derive-helper; do NOT wire as prod gate unless Phase 8 proves a concrete ownership gap |
| ProblemSurface | **none** | `problemsurface.Derive` · `internal/problemsurface/surface.go:85` | NO — callers only `continuation_integration_test.go:34`, `generality_boundary_test.go:151,287,372,394,600` | Same as above |
| ProblemSolvingPlan | **Driver + execution/planner** (live) | `problem.Derive` · `internal/problem/plan.go:75` | NO for `problem.Derive` (callers only `continuation_integration_test.go:38`, `generality_boundary_test.go:291,441,607`; `doc.go:15` self-admits "translation not implemented") | Keep Driver/planner; demote `problem.Derive` |
| MutationPlan | **execution/planner DAG** (live) | `mutationstrategy.Derive` · `internal/mutationstrategy/plan.go:93` | NO — 85 hits all `*_test.go` (`plan_test.go:52+`, `generality_boundary_test.go:235,260,302,384,433,619`) | Keep `execution/planner`; demote `mutationstrategy.Derive` (bridge `CandidateFromMutationStep` `admission.go:176` itself only called in `admission_test.go:386`) |
| CandidateStep | **ExecuteRequest + Target list** (live: `executor.go:77`, `strategy/collectTargets`) | `stepadmission.CandidateStep` · `types.go:13` | NO — 61 hits all `admission_test.go:39,62,86...`; zero prod imports of `internal/stepadmission` | Keep `ExecuteRequest`; optionally reuse `CandidateStep` as a *view* type, not a runtime |
| StepAdmission | **`execution.RuntimeExecutor` pipeline** (live: `verifyIntentContext:1039`, `admission.Admit:1079`, strategy/targets/observe) | `stepadmission.AdmitStep` · `admission.go:18` | NO — called only in `admission_test.go:49,73,96...`; self-guard `admission_test.go:259` asserts isolation from scheduler | **Do not replace** prod admission; optionally call `AdmitStep` as a pure bound-check *inside* executor/Driver (library, not boundary) |
| StepScheduler | **`autonomy.Driver`** (live TUI) + **`orchestrator.Loop`** (live headless single-cycle) | `runtime/scheduler.StepScheduler` · `scheduler.go:95,98,118` + `continuation.go:81 RunNext` | NO — callers only `recovery_test.go:90,125,161...`, `symbols_test.go:35,56,72`, `continuation_integration_test.go:47,95`; rivals `execution/scheduler.go:27`, `execution/scheduler/scheduler.go:32` (DUPLICATE trio) | Demote `runtime/scheduler.StepScheduler` to experiment/delete; unify duplicate schedulers |
| Execution | **`execution.RuntimeExecutor`** (canonical) | `executor.go:443,1009` (binding `compose.go:585`) | **YES** | Preserve; delete/demote rivals `runtime/executor/executor.go:40` (self-declared non-prod `:16-29`), `scopeguard/gateway.go:199,208,214` cursor |
| Observation | **`ExecutionProof/Evidence` + `events.Bus`** (live) | `continuation.Observation` (`types.go:37`) / `substrate.SaveEvidence` (`substrate/evidence.go:23`) | NO — `SaveEvidence` only in `substrate_test.go:96,98`; `Observation` only in `continuation_test.go` + test-only `continuation_bridge.go:50,124` | Keep bus/evidence pipeline; demote `SaveEvidence`/`Observation`-as-runtime |
| Continuation | **`Driver` recovery matrix + resume** (live) | `continuation.DeriveNextStep` · `derive.go:15` + `scheduler/continuation_bridge.go:25,66,131` | NO — chain terminates in `continuation_test.go:47,67,90...` + `continuation_integration_test.go:119,127`; `continuation/doc.go:4` "does not schedule" | Keep Driver matrix; optionally reuse `DeriveNextStep` as pure transition fn *inside* Driver |

**Duplicate inventory (must converge to one each):**

- Schedulers (3): `runtime/scheduler.StepScheduler` (dead) vs `execution.Scheduler`
  (`execution/scheduler.go:27`) vs `execution/scheduler.Scheduler`
  (`execution/scheduler/scheduler.go:32`). Production scheduling is a fourth thing:
  `Driver` + `orchestrator.Loop`. → Keep Driver/Loop; collapse the trio.
- Executors (3): `execution.RuntimeExecutor` (canonical, `compose.go:585`) vs
  `runtime/executor.RuntimeExecutor` (`executor.go:40`, header `:16-29` declares
  non-prod) vs `scopeguard.RuntimeExecutor` (`gateway.go:199,208,214`, pipeline
  subordinate via `runtime/engine.go:94,210` where `RuntimeEngine` itself has zero
  prod constructors). Architecture tests already pin this
  (`phase1_authorization_boundary_test.go:328,343`,
  `execution_invariants_test.go:150`).
- ScopeGuards (4): `runtime/scopeguard` (envelope) vs `controlplane/guard`
  (patch, legacy) vs `core/authorization` (drift tracker) vs
  `boundary/scopeguard` (format helper). Not all must merge, but names must stop
  implying interchangeable authority — see migration step M4.

---

## 5. Option A Analysis — Scheduler-Centric Convergence

Target:

```text
Intent → Understanding → ProblemSurface → ProblemSolvingPlan → CandidateStep
 → StepAdmission → StepScheduler → RuntimeExecution → Observation
 → Continuation → StepAdmission → StepScheduler
Driver = thin lifecycle/session adapter.
```

**What it would require (facts, not preference):**

- Promote a type with **zero production callers** (`NewStepScheduler`, `Schedule`,
  `RunNext`) to own `what step executes next` for TUI *and* headless, replacing a
  loop (`Driver.Run:251`, `observeAndRun:887`, `step:1053`) that already owns
  runID/single-lane guard (`:260`), LoopBounds, preflight barrier (10s →
  `PREFLIGHT_TIMEOUT:855-878`), manifestPass/globalVerify, DecisionSurface
  lifecycle, approval/clarify/proposal human boundaries, DAG re-entry, and a typed
  recovery matrix (`recovery.go:72`, `ErrRecoveryHalted:64`).
- Reproduce inside `StepScheduler` (or its callers) everything the Driver already
  integrates and that `StepScheduler` today lacks: provider invocation path,
  approval session epochs (`authorization/gate.go:65,125`), OCC pre-commit
  (`execution/occ.go`), checkpoint/rollback authority
  (`coordinator.go:79,161`), `OUTPUT_CEILING` zero-delta halt
  (`continuation.go:159` exists in the dead scheduler but is only exercised by a
  test sink; the live halt lives in `executor/staging.go:436`,
  `ProposalStagingBuffer.Finalize` + Driver matrix), stale-digest enforcement
  across real disk state, and `events.Bus` lifecycle emission.
- Rewire every production entry (`ui/autonomous.go:43`, `compose.go:791-793`,
  `cli.Stack`, `pipeline.Run`) to the new path, plus rewrite the Driver's
  extensive production-parity test suite (`driver_test.go`, `forensics_phase7_test.go`,
  `conformance_zero_trust_test.go`, `truncation_recovery_*`, `autonomy_test.go` —
  all driving REAL `Driver+Adapter+RuntimeExecutor` with only the LLM mocked) to
  drive `StepScheduler` instead.
- Resolve the headless gap anyway: headless is single-cycle (`orchestrate.go:134`,
  `runtime.go:269`); pointing it at `StepScheduler` does not by itself add a
  bounded multi-step loop, approval UX, or durable ledger looping on
  `NeedsContinuation` (today the caller must re-invoke — `RUNTIME_REALITY_AUDIT.md:706`).

**Against §5 criteria:**

- (A) Authority locality — possible in principle (`StepScheduler` is pure today),
  but the *integration* that keeps scheduling from becoming authorization (adapter
  translation, Boundary-5 digest, gateway pre-admission) already exists around the
  Driver, not around `StepScheduler`.
- (B) Recovery locality — `StepScheduler` re-entry exists **only** in
  `continuation_integration_test.go:72,119,127` with a stub worker + temp-map sink;
  Driver re-entry is production (`Recovering(repair:1007)`, `Resume*`, DAG re-entry).
- (C) Admission locality — `AdmitStep` admits zero production steps; prod admission
  is the executor pipeline. Promoting `AdmitStep` to *the* boundary means rewiring
  all three prod entries to it and re-proving ordering locks
  (`phase1_execution_pipeline_ordering`, `phase0_mutation_dispatch`).
- (D–G) — same pattern: capability/evidence/failure/parity integrations are live
  around Driver+executor (mock-provider parity tests, `forensics_phase7_test.go`
  convergence tests, orchestrator `integration_test.go` zero-redundancy proofs) and
  absent around `StepScheduler`.

**Verdict on A:** architecturally coherent as an aspiration, but it is the
**larger rewrite**: it demotes the tested production loop to an adapter and
promotes untested wiring to canonical, violating hard constraints §1.1 (no new
runtime — this effectively installs one) and §1.3 (prefer promoting existing
canonical boundaries over adding abstractions). No scores or rankings are offered
per spec; the factual cost is stated above.

---

## 6. Option B Analysis — Driver-Centric Convergence

Target: keep `autonomy.Driver + execution/planner` as canonical orchestration;
demote/remove `runtime/scheduler.StepScheduler`, `stepadmission`-as-runtime, and
`continuation`-as-runtime from the claimed production architecture (to
`architecture_experiment` or deletion); documentation describes Driver/planner as
the actual architecture.

**What it requires (facts):**

- No production call-path changes: `Driver→Adapter→RuntimeExecutor→substrate`
  stays; `execution/planner` stays the pure decomposer; `orchestrator.Loop` stays
  the headless single-cycle engine (with an explicit decision on whether headless
  gains a bounded Driver loop — see M5).
- Deletions/demotions are deletions of **dead code only**: `StepScheduler`
  struct/bridge, duplicate executors/schedulers, `substrate.SaveEvidence`-as-runtime —
  none have prod callers, so removal cannot break production (only
  `*_test.go` + `internal/architecture/*_test.go` need rewriting — §11).
- The pure policy logic (`AdmitStep` bound-check, `DeriveNextStep` transition,
  `EffectiveBudget`, `detectOCCDrift`) can be **reused as library calls inside**
  the Driver/executor without promoting their packages to runtime owners. This
  preserves the design value of Phase 4 work without creating a second loop.
- Authority boundaries (§1.2) are untouched: ScopeGuard, CapabilityGuard,
  `preflight.Gate`, `Gate.Pipeline`, PolicyEngine, RuntimeExecutor, substrate
  confinement, approval, OCC/digest, checkpoint/recovery all stay exactly where
  they are; the change only removes *rival claimants* to scheduling/admission/
  execution ownership.

**Against §5 criteria:**

- (A) One component owns next-step: **Driver** (`Run/step/observeAndRun`), and it
  is structurally barred from authorization (no FS reads, no provider calls, no
  mutation — `driver.go:38-41`; authorization lives in gateway/guards/executor).
- (B) Recovery re-enters the same path: `observeAndRun` → `repair` →
  `adapter.Execute` (same `ContractID`/attempt++ or rewritten bounded contract or
  `StagedSubTasks` DAG), human resume via `Resume*` intents (`599-756`).
- (C) One admission boundary per executable step: intent `Gate` + executor
  `verifyIntentContext → admission.Admit → strategy/targets/observe` — every TUI
  step (Driver or direct) and every adapter step passes it; headless passes the
  CLI/gate equivalents (convergent auth, divergent loop — explicitly owned in M5).
- (D) Capability constraints influence admission without becoming authority:
  `PolicyEngine.CanExecuteCommand`, `ModelCapabilities.MaxOutputTokens`,
  `OUTPUT_CEILING` handling in executor staging + Driver matrix; model/provider
  remains an untrusted proposal source (fail-closed on empty `ModelID`,
  `compose.Wire:563`, `gateway.go:85-86`).
- (E) Durable observation before continuation: `ExecutionProof/Evidence` sealed by
  executor (`sealTerminalEvidence`), projected via `runtimegraph.Graph` → `Bus` →
  UI; `adapter.observe:441` maps 1:1; Driver decides only after observation.
- (F) Parity: same `Driver+Adapter+RuntimeExecutor` exercised by TUI tests, Driver
  unit tests, and forensics tests with injected (`mockProvider`) vs real-provider
  boundary (fail-closed binding `runtime.go:442,474`); headless single-cycle
  semantics explicitly documented rather than papered over.
- (G) All six failure signals return through one state-transition model: the
  Driver loop states (`Deciding→Executing→Recovering→AwaitingHuman`) fed by typed
  executor/substrate/guard sentinels (table in §11 test strategy).

**Verdict on B:** the **smaller convergence**: it deletes duplicate authority,
promotes the existing canonical boundary (Driver + RuntimeExecutor), connects
verified seams (planner DAG, bus evidence, durable ledger), and removes dead
architecture — exactly the preference order in §1.3.

---

## 7. Canonical Decision

**Select Option B (Driver-centric), with selective library reuse of the pure
admission/continuation functions.**

- **Why it is canonical (repository evidence):** only the Driver path has
  production callers, production state (runID, bounds, DAG, DecisionSurface,
  ledger), production recovery (matrix + resume + preflight barrier), and
  production-parity tests with the real executor. The `StepScheduler` path has
  none of these — only synthetic-sink tests. Canonical status must follow
  reachability, not package names or line counts (§5 preamble).
- **What becomes legacy:** `internal/runtime/scheduler` (`StepScheduler`,
  `Schedule/RunNext/AcceptStep`, `continuation_bridge.go`, `continuation.go`
  scheduler-side loop), `internal/stepadmission` *as a runtime boundary*,
  `internal/continuation` *as a runtime loop*, `internal/problem`,
  `internal/problemsurface` (Derive), `internal/understanding` (Derive),
  `internal/mutationstrategy` (Derive) as production stages; duplicate executors
  (`runtime/executor.RuntimeExecutor`, `scopeguard.RuntimeExecutor`) and
  duplicate schedulers (`execution.Scheduler`, `execution/scheduler.Scheduler`
  trio — collapse to Driver/Loop + one planner type if needed).
- **What gets deleted:** dead runtime wiring (scheduler struct + bridge +
  `RunNext` loop), rival executor constructors from production docs/imports,
  `substrate.SaveEvidence`-as-canonical-path claims; see M1–M4.
- **What gets promoted:** `Driver` (scheduler + recovery owner, explicitly
  documented as such), `execution/planner` (sole decomposer),
  `execution.RuntimeExecutor` (sole execution authority — already true in code,
  now true in docs), `events.Bus + ExecutionEvidence` (sole evidence path),
  `runtime/durable` ledger (sole durable task state).
- **What gets reused (not promoted):** pure functions `AdmitStep`,
  `EffectiveBudget`, `EstimateForCandidate`, `DeriveNextStep`, `HasStaleState`,
  `detectOCCDrift` — callable from Driver/executor as helpers in Phase 8 if they
  add bound-check value beyond the existing executor admission. Reuse must never
  reintroduce a second `Schedule`/`RunNext` loop.

---

## 8. Target Runtime

```text
User input
 → parser.IntentAST (syntax only)
 → execution.IntentGateway.Gate + strategy.Select (intent authority + path)
 → autonomy.Decide (capability/risk routing; read-only vs BUILD)
 → runtime/autonomy.Driver.Run  ★ CANONICAL SCHEDULER ★
 │    ├─ preflight barrier (BackgroundPreflight, 10s → PREFLIGHT_TIMEOUT)
 │    ├─ decomposition via execution/planner (pure DAG; Boundary-2)
 │    │     [optional Phase-8: AdmitStep/EffectiveBudget as pure bound-check here]
 │    ├─ ExecutorAdapter.Execute (translation + Boundary-5 digest revalidation)
 │    ├─ execution.RuntimeExecutor.Execute  ★ SOLE EXECUTION AUTHORITY ★
 │    │     verifyIntentContext → admission.Admit → selectStrategy
 │    │     → targets → observeTargets → provider → PatchManager.Apply
 │    │     → Verifier → approval hold → OCC pre-commit → Proof + Evidence
 │    │     [guards wrap every step: ScopeGuard → CapabilityGuard →
 │    │      preflight.Gate/Gate.Pipeline → PolicyEngine → ApprovalGate]
 │    ├─ substrate.Execute/ExecuteUnit  ★ SOLE SIDE-EFFECT SINK ★
 │    ├─ ExecutionProof + ExecutionEvidence → events.Bus → UI projection
 │    │     [optional Phase-8: DeriveNextStep as pure transition fn on Observation]
 │    └─ Driver.observeAndRun recovery matrix → Resume*/retry/re-scope/halt/park
 │         → RE-ENTER SAME Driver loop (same ContractID/attempt++ or DAG re-entry)
 │         → next admitted step (ONE continuation entry point)
 │
 ├─ Headless (explicit, documented): `izen run` pipeline / `izen orchestrate`
 │  cli.Stack → orchestrator.RunCycle/Loop (single-cycle; caller re-invokes
 │  for continuation; Phase-8 M5 decides whether headless adopts Driver loop)
 │
 └─ Durable: runtime/durable TaskStore/Ledger (ONE durable task state)
      guards: ScopeGuard + CapabilityGuard + preflight.Gate + Gate.Pipeline
              + PolicyEngine + ApprovalGate + OCC/digest + CheckpointCoordinator
              (all preserved unchanged per §1.2)
```

Properties satisfied (§7 target state): ONE scheduler (`Driver`; headless
single-cycle explicitly scoped, not a rival scheduler), ONE execution authority
(`execution.RuntimeExecutor`), ONE admission boundary (executor pipeline +
intent `Gate`), ONE continuation entry (`Driver.observeAndRun` re-entry), ONE
evidence path (`Proof/Evidence` → `Bus`), ONE durable task state
(`runtime/durable`). No scheduler A+B, no planner A+B, no continuation A+B;
test architecture = production architecture (real Driver+Executor, mock LLM
only); TUI vs headless difference is loop cardinality, explicitly documented.

---

## 9. Migration Plan (smallest sequence; Phase 8 work, not this phase)

| ID | File/package | Current responsibility | New responsibility | Dependency direction | Risk | Required tests |
|---|---|---|---|---|---|---|
| M1 | `internal/runtime/scheduler/` (`scheduler.go:95`, `continuation.go:81`, `continuation_bridge.go:25,66`) | Dead second scheduler + bridge | Move to `architecture_experiment/` or delete; keep `PostStepEvaluation` zero-delta-halt logic only as a helper if Driver lacks it | Tests → package only; nothing in prod depends on it | Low (no prod callers) | Delete/relocate `recovery_test.go`, `symbols_test.go`, `scheduler_bounded_test.go`, `continuation_integration_test.go`, `telemetry_test.go`, `planner_ephemeral_test.go` or re-point them at Driver; add a negative test asserting zero prod imports of the experiment path |
| M2 | `internal/runtime/executor/executor.go:40`, `internal/runtime/scopeguard/gateway.go:199,208,214`, `internal/runtime/engine.go:94` | Rival executors / pipeline cursor | Demote to harness/legacy; single canonical import `execution.NewRuntimeExecutor` (`compose.go:585`); `engine.go:94` binding removed or test-scoped | Prod → `execution` only | Low (rivals have zero prod callers; `engine.go` path has zero prod constructors) | Extend `TestRuntimeExecutorSingleCompositionBinding` + `TestPhase1_SingleProductionExecutionAuthority` to fail on any new non-test `NewRuntimeExecutor` rival; keep `phase0_mutation_dispatch` quota test green |
| M3 | `internal/execution/scheduler.go:27` + `internal/execution/scheduler/scheduler.go:32` vs Driver | Duplicate scheduler types | Collapse: keep at most one planner/scheduler *type* for pure decomposition metadata; scheduling decisions stay in Driver | Executor/planner → Driver (never reverse) | Low-Med (check `execution/*_test.go` refs) | `execution/scheduler_test.go` + planner tests re-pointed; no prod path change |
| M4 | ScopeGuard ×4 (`runtime/scopeguard/scope.go:42`, `controlplane/guard/scope_guard.go:42`, `core/authorization/guard.go:11`, `boundary/scopeguard/scope_guard.go:68`) | Overlapping names, distinct roles | Keep roles; rename or alias so only the envelope guard reads as authority-adjacent; mark `controlplane/guard` legacy explicitly | No dependency change (docs + names) | Low (name-only) but wide grep surface | Existing guard/engine tests stay green; add doc-link test if repo uses them |
| M5 | `cmd/izen/runtime.go:107`, `cmd/izen/orchestrate.go:40`, `internal/cli/cli.go:336`, `internal/app/pipeline.go:240` | Headless single-cycle (divergent loop cardinality) | Explicit decision (Phase 8): (i) document single-cycle as intentional + caller-re-invokes contract, OR (ii) adopt Driver bounded loop for headless. Either way ONE documented continuation contract | Headless → same executor/substrate/guards (already true) | Med (UX + audit-flush `orchestrator.ErrAuditPersistenceFailed` semantics) | New headless multi-step test (real executor + stub provider, 2-step + OUTPUT_CEILING + approval-deny); headless approval `[y/N]/i` test |
| M6 | `internal/stepadmission/*`, `internal/continuation/*` (pure) | Test-only boundaries | Optional library reuse: call `AdmitStep`/`DeriveNextStep` from Driver/executor as pure helpers (no `Schedule`/`RunNext` promotion) | Driver/executor → pure pkg (one-way; pure pkgs must never import Driver/executor) | Low (pure, no I/O) | New Driver-level tests: REFINE/BLOCK/OUTPUT_CEILING/stale via helper; keep `admission_test.go`, `continuation_test.go` pure suites green |
| M7 | `internal/understanding/`, `internal/problemsurface/`, `internal/problem/`, `internal/mutationstrategy/` | Dead Derive chain | Demote to experiment/derive-helpers; remove "canonical production stage" claims from docs; do NOT add to prod path in Phase 8 without a proven ownership gap | Docs/tests only | Low | Re-point `architecture/generality_boundary_test.go`, `phase2/phase3_*_test.go`, `mutationstrategy/plan_test.go` at experiment path or mark explicitly as policy-type tests |

Dependency rule for all steps: **pure policy packages must never import the
Driver, executor, substrate, or bus**; the Driver/executor may call pure
functions. No new interfaces, orchestrators, or loops.

---

## 10. Invariants (must remain true throughout migration)

1. `Intent ≠ Authorization ≠ Grant ≠ Execution ≠ Evidence ≠ Verification` — LLM
   output stays an untrusted proposal; no model/provider becomes an authority owner.
2. ScopeGuard, CapabilityGuard, `preflight.Gate`, `Gate.Pipeline`, PolicyEngine,
   RuntimeExecutor, substrate confinement, approval semantics, OCC/tree-digest,
   checkpoint/recovery safety are **not weakened** by any M-step (no bypass, no
   reorder of `verifyIntentContext < selectStrategy < admission.Admit <
   min(invokeReadOnly,invokeMutation)`, no `ApplyContext/Commit` outside `Approve`).
3. Operation is derived by the Engine; LLM cannot select CREATE/MODIFY/DELETE.
4. Conflict preservation: CREATE+Exists → CONFLICT; DELETE+!Exists → CONFLICT; no
   silent downgrade.
5. Execution-time confinement: planning-time resolution is never sufficient
   filesystem authorization at execution time (`verifyUse` per target per use).
6. All discovered paths are untrusted (normalization + containment + symlink policy).
7. Only authorized executable tasks cross the execution boundary; no model output
   directly mutates workspace state.
8. Single production executor binding (`compose.go:585`); single composition root
   per entry (TUI `compose.Wire`, headless `cli.Wire`/`app.Pipeline` — both to the
   same executor type after M2/M5).
9. Approval: UI read-only while `PendingApproval`; only `RuntimeExecutor`/appruntime
   may mark/apply approval; stale-epoch and PTY-bleed window semantics preserved.
10. Evidence immutability: only the executor seals terminal evidence; UI only projects.
11. Headless determinism: conflicts yield structured errors, never hidden
    interactive waits inside the core engine.
12. No second scheduler/loop is introduced by any M-step (M1/M3/M6 explicitly forbid
    promoting `Schedule`/`RunNext`).

---

## 11. Test Strategy

Existing suites to **preserve** (production-parity, mock LLM only):
`runtime/autonomy/driver_test.go`, `forensics_phase7_test.go`,
`conformance_zero_trust_test.go`, `truncation_recovery_*`, `ast_repair_test.go`,
`autonomy_test.go`, `state_machine_test.go`, `execution_truth_matrix_test.go`,
`execution_phase4_test.go`, `executor_test.go`, `orchestrator/integration_test.go`
(`TestCase1/2/3`), `engine_integration_test.go`, `phase0/phase1/phase4 lock tests`.

Existing suites to **rewrite/relocate** (test-only architecture):
`scheduler/recovery_test.go`, `scheduler_bounded_test.go`, `symbols_test.go`,
`telemetry_test.go`, `planner_ephemeral_test.go`,
`scheduler/continuation_integration_test.go` (the only multi-step scheduler test —
test-only worker+sink), `stepadmission/admission_test.go` (keep as pure suite,
re-scope as library), `continuation/continuation_test.go` (same),
`architecture/generality_boundary_test.go`, `phase2_understanding_changesurface_test.go`,
`phase3_mutationstrategy_test.go`, `mutationstrategy/plan_test.go`,
`understanding_test.go`, `substrate_test.go` (SaveEvidence).

New integration tests required (all on REAL `Driver+Adapter+RuntimeExecutor` +
REAL bus/FS, stub/injected provider unless noted):

```text
TUI single-step / multi-step / partial-output / OUTPUT_CEILING /
  REFINE / BLOCK / continuation / stale-state / verification-failure /
  approval / scope-violation — via Driver with mockProvider (mirror
  forensics_phase7_test.go htmlTestHarness pattern)
headless single-step + multi-step (M5 decision) + OUTPUT_CEILING +
  approval-deny ([y/N]/i) — via cli.Stack/pipeline with stubLLM
injected-provider parity: same scenario with mock vs recorded provider —
  assert identical admission/evidence/ledger outcomes
real-provider boundary: fail-closed on empty ModelID / mismatch
  (ErrProviderModelMismatch/ErrProviderDisabled), no FS mutation on mismatch
stale/OCC: concurrent modification → OccAborted + RollbackToBaseline proof
  (WorkspaceVersion()==BaseTreeDigest post-rollback)
approval: approve applies REAL mutation; reject leaves FS unchanged;
  double-approve cannot leave stale AwaitingHuman
scope violation: traversal/absolute/symlink/depth → SCOPE_VIOLATION_REJECTED +
  ledger count (engine_integration_test.go pattern)
verification failure: Verifier fail → converges ABORTED (forensics pattern)
negative: zero non-test imports of architecture_experiment path;
  single-composition-binding quota test extended (M2)
```

---

## 12. Documentation Impact (becomes stale after convergence)

Primary (dead-path census + divergent-scheduler verdicts — must be rewritten to
describe Driver/planner as canonical):

- `docs/audits/IZEN_RUNTIME_REALITY_AUDIT.md` — §§2-4,6-8,11,17-19 (dead-path
  census, DIVERGENT_SCHEDULER, P1-05/P1-07/P1-08, headless-single-cycle).
- `docs/architecture/IZEN_PROBLEM_SOLVING_MODEL.md:184`,
  `docs/architecture/IZEN_BOUNDED_ADAPTIVE_CONTINUITY.md:44-45,75,157,200,213`
  ("StepScheduler remains canonical / no second scheduler" — aspirational today).
- `docs/audits/IZEN_PHASE4_BOUNDED_ADAPTIVE_CONTINUITY.md:31,45,60,68`,
  `docs/audits/IZEN_GENERALITY_BOUNDARY_CORRECTION.md:144,250`,
  `docs/audits/IZEN_PROBLEM_SOLVING_GENERALITY_AUDIT.md`,
  `docs/audit/IZEN_PHASE_3_MUTATION_STRATEGY_STEP_CONSTRUCTION.md:42,276,286`.

Authority-boundary docs (dual-executor / pipeline-ordering / headless-divergence
notes close out as done):

- `docs/audit/IZEN_PHASE_0_AUTHORITY_CENSUS.md` (P0-2 dual executor),
  `docs/audit/IZEN_PHASE_1_AUTHORIZATION_BOUNDARY.md` (Case B/C taxonomy, §J),
  `docs/audit/IZEN_PHASE_2_PROJECT_UNDERSTANDING_CHANGE_SURFACE.md:211`,
  `docs/deliverables/PRODUCTION_PATH.md:25-86`, `LINEAGE_STATUS.md:13-35`,
  `AUTHORITY_CENSUS.md:14-21`, `AUTHORITY_GRAPH.md:11-87`,
  `docs/refactor/01_CODEBASE_DISCOVERY.md:50,168-170,199-201,265` (V1-A/B/C/D),
  `docs/refactor/02_SYSTEM_MODEL.md:1015-1031,1620-1623,1696-1699` (deletion contracts).

System/design docs (composition roots, TUI-vs-pipeline split, firewall, staging):

- `docs/design/IZEN_SYSTEM_ARCHITECTURE.md:23,41,74,107,163,327,375,419,691,716`,
  `docs/design/COMMAND_AND_MODE_SPECIFICATION.md:17-20,199,243-244,309,414,482,583`,
  `docs/design/PHASE/RUNTIME_FORENSIC_AUDIT.md:22,29,324`,
  `PHASE_0_ARCHITECTURE_BASELINE.md:60,131,215`,
  `RUNTIME_AUTHORITY_MIGRATION.md:125,267`, `PHASE_1_CUTOVER_PLAN.md:93`,
  `PHASE_7_EXTERNAL_FORENSIC_AUDIT.md:29-35`.
- `docs/report/CODEBASE_SURVEY_REPORT.md`, `ARCHITECTURE_INVARIANT_LOCK_REPORT.md:42-111`,
  `PHASE3_OCC_ENGINE_REPORT.md:37-54`, `BOUNDARY2_DECOMPOSER_DAG_PLANNER_REPORT.md:49-194`,
  `docs/constitution/IMPLEMENTATION_AUDIT.md:50,83,302,509`,
  `docs/architecture/ARCHITECTURE.md:1176,2120,2681`,
  `ARCHITECTURE_GUARDRAILS.md`, `AUDIT-RUNTIME-AUTHORITY.md`, `IZEN_SPEC.md`.

Package godoc that asserts production status (`internal/problem/doc.go:15`,
`internal/runtime/scheduler/continuation_bridge.go:20,63`,
`internal/runtime/executor/executor.go:16-29` — the last is already correct and
stays as the demotion notice).

---

## 13. Implementation Gate

Phase 7 is complete when the repository (this document + cited code) defensibly
answers — with **single owners**, never "two components / depends on mode /
test-only":

- Step selection: **Driver** (`driver.go:251,850,1053`).
- Admission: **RuntimeExecutor pipeline + IntentGateway.Gate**
  (`executor.go:1039-1121`, `intent.go:83`).
- Continuation: **Driver recovery/resume re-entry** (`recovery.go:72`,
  `observeAndRun:887`, `Resume*:374,425,463,504`).
- Execution authority: **`execution.RuntimeExecutor`** (`compose.go:585`).
- Evidence: **executor-sealed `Proof/Evidence` → `Bus`** (`evidence.go:24`, `graph/graph.go`).
- Recovered-step re-entry: **same Driver loop** (same ContractID/attempt++,
  rewritten bounded contract, or `StagedSubTasks` DAG; M5 extends the contract to
  headless).

Convergence is NOT complete while any answer remains "two schedulers", "depends
on execution mode" (beyond the explicitly scoped headless single-cycle contract
in M5), or "only true in tests". No migration code is implemented in this phase
by design; Phase 8 executes M1–M7 under the invariants in §10.
