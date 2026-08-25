# CODEBASE SURVEY REPORT — Izen Structural & Flow Audit

| Field            | Value                                                        |
| ---------------- | ------------------------------------------------------------ |
| Status           | ACTIVE SURVEY                                                |
| Version          | 1.0                                                          |
| Date             | 2026-08-24                                                   |
| Reference        | `docs/constitution/IMPLEMENTATION_AUDIT.md` (v0.4.0)         |
| Mode             | READ-ONLY forensic survey (no source modified)               |
| Method           | Direct source inspection, call-graph tracing, test inventory |
| Codebase Size    | 912 Go files, ~16k symbols indexed                           |

> **Evidence rule inherited from the audit:** this survey proves, falsifies, or leaves
> unverified. It does not invent architecture. Every claim carries a `file:line` anchor.

---

## 0. Executive Summary

1. **D-002 as written in IMPLEMENTATION_AUDIT.md is stale.** The Phase 1 cutover has been
   landed: the production TUI's autonomy decision layer converges on the `RuntimeExecutor`
   through the composition-bound Driver (`compose.go:607-610`), and AST-level regression
   guards exist (`internal/architecture/execution_invariants_test.go`). The original
   "dormant because `m.autonomy` is always non-nil" condition describes the pre-cutover
   tree.
2. **A superseding authority-integrity finding replaces it:** three legacy mutation
   surfaces remain reachable under default runtime construction (§3.3): the mixed-plan
   fallback to `handleBuildRun`, the task-amendment path, and the UI-owned
   `execution.Engine` transaction lifecycle. Gate A stays blocked until these route
   through the executor or are formally rescinded with equivalence evidence.
3. **SM-012 (AMBIGUOUS ∧ DEGRADED_CONTEXT → FAIL_CLOSED) has no implementation
   substrate**: no degraded-context signal exists anywhere in the decision model.
4. **No `PARTIALLY_APPLIED` and no session-taint machinery exists.** Multi-file outcomes
   collapse to changed→created→nochange precedence; atomicity inside one MutationSet is
   real, but partial-application truth retention (Boundary 11) is absent.
5. **OCC is structurally stubbed in production**: the AuthorizationEngine is wired with a
   `noopSourceHashVerifier`; no `WorkspaceStateConflict` type exists.
6. **Zero of the ~30 mandatory audit tests/benchmarks exist by name**, though ~350
   adjacent tests provide partial coverage (§5).

---

## 1. Executable Architecture Map

Status values follow the audit's Evidence Model. `VERIFIED` requires implementation
evidence **and** named executable tests; neither may be manufactured.

| # | Audit Boundary | Physical Packages / Files | Status | Basis |
|---|----------------|---------------------------|--------|-------|
| 1 | Intent & Dynamic Surface Selection | `internal/execution/intent.go` (IntentGateway :50-111); `internal/parser` (IntentAST pipeline); `internal/ui/intent_dispatch.go`; `internal/ui/gateway.go:42`; `internal/execution/strategy` | **PARTIAL** | Deterministic directive resolution VERIFIED (no model call: `SelectStrategy` → `strategy.Select`, intent.go:62-65). But no Reduced/Full surface-profile object exists; all 7 capabilities granted at boot regardless of intent (compose.go:520-528); mandatory tests absent. |
| 2 | Context Compiler & Graceful Degradation | `internal/planner` (orchestrator.go, sources.go, adapters.go); `pkg/app/compiler`; `internal/execution/executor.go:1038` (compileContext); `internal/retrieval` | **PARTIAL** | Probes are SEQUENTIAL (orchestrator.go:152-183). Token-budget degradation implemented (fitBudget :190-215). Probe-failure degradation semantics unproven; quality ranking implicit in priority order, not a ranked-bundle contract. |
| 3 | Autonomy & Capability Grant | `internal/autonomy` (engine.go, controller.go, grant.go, intent.go); `internal/runtime/compose/compose.go:572-610`; `internal/ui/autonomy_route.go`; `internal/core/capability`; `pkg/capability/policy` | **PARTIAL** | Cutover converged on RuntimeExecutor (see §3). Grant ledger exists (`grant.go`, one grant per capability/scope pair). Residual legacy mutation paths (§3.3) keep this open. Structural guards: `TestRuntimeExecutorSingleCompositionBinding`, `TestUICannotCallProviderOnExecutionPath`. |
| 4 | Invocation Retry vs Contract Recovery | `internal/execution/executor.go` (RecoveryStrategy/RecoveryAttempt :108-116, bounded_patch_contract_test.go); `internal/controlplane/failure` | **PARTIAL** | Bounded-patch repair cycles with window rotation exist (executor request fields + 7 tests). No first-class ExecutionContract identity (ID) object, so "retry preserves Contract ID / recovery changes canonical parameter" is UNPROVABLE as specified. |
| 5 | Mutation Domain, OCC & Snapshot Lifecycle | `internal/execution/executor.go:746-935` (Approve/Rollback/evidence); `mutationset.go`; `patch.go` (PatchManager :95, shadow backup :299-420); `pkg/fs/txfs.go`; `internal/workspace/snapshot`; `internal/workspace/checkpoint`; `internal/core/authorization/impl.go:10-17` (noop verifier) | **PARTIAL** | Staging + atomic two-phase commit + whole-transaction rollback + post-rollback evidence reconciliation VERIFIED in code and tests. OCC fingerprinting STUBBED (noop); no WorkspaceStateConflict; SnapshotCache is a diagnostics view, not an OCC baseline. |
| 6 | Runtime Evidence & UI Projection | `internal/events` (bus.go, events.go); `internal/execution/graph/graph.go` (sole lifecycle emitter); `internal/presentation/execution_projection.go`; `internal/ui/model.go` (handleDomainEvent); `internal/events/audit` | **PARTIAL** | "Lifecycle events generated only from graph transitions" is AST-enforced (`TestLifecycleEventsGeneratedOnlyFromGraph`). Provider usage authoritative (`ModelInvocation.Known`, executor.go:124-130; usage_forensics_test.go). No named test proves UI never renders success before MutationEvidence, though execution_truth_matrix_test.go (15 tests) approximates. |
| 7 | Mutation Domain Isolation | `internal/ui/runtime_cutover.go:177-236` (FILE_MUTATE→executor / SHELL_EXEC split); `internal/ui/commands.go:2614-2641` (shell gate); `runBuildShellExec` | **PARTIAL** | Dispatch-level decomposition EXISTS (runtime does not own OS commands). But a plan mixing SHELL_EXEC with FILE_MUTATE falls back ENTIRELY to the legacy per-task path (runtime_cutover.go:195-200) — mixed-domain operations execute as one sequential UI-driven batch with no independent per-domain evidence contract. |
| 8 | Context Probe Budget Enforcement | `internal/planner/orchestrator.go:190-215` (token caps); `compose.go:530-539` (Budget + MicroBudget) | **PARTIAL** | Aggregate token budget + per-source share VERIFIED. NO wall-clock, CPU-worker, or memory ceilings per probe; no fan-out overrun guard beyond token math. |
| 9 | Risk Scope Pre-Authorization | `internal/execution/risk.go` (RiskClassifier); `internal/autonomy/controller.go:103-227`; `compose.go:587-592` (risk probe binding); `internal/domain/policy/engine.go` | **PARTIAL** | Quantitative thresholds active (IntentConf 0.6, TargetConf 0.7, MaxAutonomousScope=3 files). High-risk target approval gate in PolicyEngine. No first-class inspectable/revocable Risk Scope object with lifespan/provenance/blast-radius fields; nothing named "RiskScope". |
| 10 | Context Fidelity & Mutation Eligibility | — (no implementation located) | **UNKNOWN** | DecisionInput (controller.go:56-65) carries NO context-fidelity input; no eligibility threshold gates mutation-contract issuance; zero occurrences of DEGRADED_CONTEXT repo-wide. SM-012 compound fail-closed NOT IMPLEMENTED. |
| 11 | Partial Application & Session Taint | `internal/session/session.go:24-45` (Session struct); `internal/execution/mutation.go:66-124,159-193` | **VIOLATION-leaning UNKNOWN** | No PARTIALLY_APPLIED outcome; AggregateMutationOutcome collapses multi-file results (any-changed→changed). Session has no taint/state field; a failed shell step after file commit stalls the task queue only and clears with a new prompt — directly contradicting Boundary 11's "cannot silently resume clean" invariant. Recorded as UNKNOWN because the outcome vocabulary is otherwise truthful; the *taint* requirement is unimplemented rather than mis-implemented. |

### Divergence Log cross-check

| Divergence | Audit doc claim | Survey verdict |
|------------|-----------------|----------------|
| D-001 Mutation Atomicity | TO_BE_AUDITED | **PARTIAL-VERIFIED within single MutationSet**: Apply loop breaks on first error → `RollbackTo(MutationFailed)` restores originals; per-file evidence corrected to post-rollback filesystem truth (executor.go:826-868, :910-935). Multi-file atomicity holds on the executor path. Not yet proven under concurrent external writes (no conflict detector exists to trigger it). |
| D-002 Authority Integrity | Migrated path dormant; `m.autonomy` always non-nil | **RESOLVED then SUPERSEDED** — see §3. Original condition eliminated by Phase 1 cutover; new residual-path finding blocks Gate A. |
| D-003 Domain Isolation | TO_BE_AUDITED | **CONFIRMED AS RISK** (§1 row 7): dispatch-level decomposition exists, but mixed plans degrade wholesale to the legacy path; no ordered-contract enforcement ("external only after verified State-bearing commit") is evidenced. |

---

## 2. Methodology Notes

- Composition root inspected directly: `internal/runtime/compose/compose.go` (Wire).
- Call-graph traced from `cmd/izen/main.go` → `ui.RunMainDashboardWithApp` →
  `model.handleInput` → decision layer → `RuntimeExecutor.Execute/Approve`.
- Prior phase reports in `docs/design/PHASE/` were used as leads only; every claim was
  re-verified against current source (several are stale relative to code).
- Repo-wide searches executed: `PARTIALLY_APPLIED` (0 hits), `taint` (0 relevant hits),
  `DEGRADED_CONTEXT` (0 hits), `WorkspaceStateConflict` (0 hits),
  `func Benchmark` (5 hits, none audit-mandated).

---

## 3. Deep Dive D-002 Forensic Analysis

### 3.1 Historical root cause (as documented, pre-cutover)

`handleInput` reached `runGatedLine` (the only UI caller of `m.executor.Execute`) only
when `m.autonomy == nil`; the autonomy engine was always wired, so the declared
"single execution authority" never ran in production. Corroborated by
`docs/architecture/AUDIT-RUNTIME-AUTHORITY.md` and
`docs/design/PHASE/PHASE_0_ARCHITECTURE_BASELINE.md`.

### 3.2 Current state: cutover landed (original condition ELIMINATED)

Production wiring — all unconditional, no feature flag remains
(the `IZEN_RUNTIME_EXECUTOR=1` comment at `runtime_cutover.go:20` is stale; no env check
exists outside tests):

```
cmd/izen/main.go:177        compose.Wire(...)
├─ compose.go:428           a.Executor   = execution.NewRuntimeExecutor(root, cfg, provider, bus, langID)
├─ compose.go:429           a.Gateway    = execution.NewIntentGateway(root)
├─ compose.go:598           a.Autonomy   = autonomy.NewEngine(...)          // decision layer, ALWAYS non-nil
├─ compose.go:607-610       a.Autonomous = runtimeAutonomy.NewDriver(
│                             runtimeAutonomy.NewExecutorAdapter(root, a.Gateway, a.Executor),  // ← driver CONSUMES executor
│                             a.Bus)
└─ ui/program.go:188-189    m.autonomy = app.Autonomy ; m.autonomousDriver = app.Autonomous
```

Execution entry trace (default TUI construction):

```
model.handleInput                     commands.go:383-387
  └─ m.autonomy != nil → routeFreeInput          commands.go:396-401
       └─ runAutonomyRoutedCmd                   autonomy_route.go:25-31
            └─ m.autonomy.Decide(objective)      autonomy/engine.go (Classify→Route→Controller.Decide)
                 └─ dispatchAutonomyTrace        autonomy_route.go:52-82
                      ├─ direct_response → chat stream (read-only)
                      ├─ ask_user        → proposal surface → executeAutonomyProposal
                      ├─ block           → stop
                      └─ auto_continue   → executeAutonomyWorkspace   autonomy_route.go:87-117
                           └─ BUILD → m.autonomousDriver != nil (production: always)
                                ├─ YES → executeAutonomyViaDriver      autonomous.go:38+
                                │          └─ Driver.Run → ExecutorAdapter.Execute
                                │             └─ RuntimeExecutor.Execute   executor.go:473
                                └─ NO  → executeAutonomyViaRuntime     runtime_cutover.go:80-158
                                           └─ gateway.SelectStrategy → runRuntimeExecuteCmd
                                              └─ m.executor.Execute     runtime_cutover.go:56
```

Directives converge identically:

- `$prompt` → `routePromptDirective` (intent_dispatch.go:270-294): autonomy!=nil →
  `runAutonomyRoutedCmd`; nil → `runPromptExecution` (gateway path, harness-only).
- `$hot` → `routeHotfixThroughAutonomy` (autonomy_route.go:39-48) → same boundary.
- Investigate-mode mutation override → `runRuntimePrompt` (commands.go:453-459 →
  runtime_cutover.go:273-294 → executor).
- Staged `/build` FILE_MUTATE/GIT_ACTION plans → `runStagedBuildViaRuntime`
  (commands.go:318, update.go:797/800, commands.go:4307 → runtime_cutover.go:186-236 →
  executor).
- Approval handlers route through the executor (`handlers.HandlerDeps.Executor`,
  compose.go:432-437; handlers.go:52-57, 239+, 299+).

Regression guards (structural, AST-level):
`internal/architecture/execution_invariants_test.go`

| Test | Line | Pins |
|------|------|------|
| `TestLifecycleEventsGeneratedOnlyFromGraph` | :111 | Canonical lifecycle constructors callable ONLY from `internal/execution/graph/graph.go` |
| `TestRuntimeExecutorSingleCompositionBinding` | :150 | `NewRuntimeExecutor` constructed exactly once, only in compose.go |
| `TestUICannotCallProviderOnExecutionPath` | :176 | gateway.go must not invoke provider; MUST call `m.gateway.Gate` + `m.executor.Execute` |
| `TestUICannotMutateWorkspaceOnExecutionPath` | :208 | gateway.go must not own PatchManager/MutationSet/TxFS |
| `TestEveryUserActionCrossesIntentGateway` | :235 | Gate precedes Execute |
| `TestNoDuplicateHotfixExecutionPath` | :255 | `$hot` must not fast-track through legacy builder |

### 3.3 SUPERSEDING FINDING: residual legacy mutation paths under default construction

These are reachable **with autonomy AND executor wired** (production default):

**L1 — Mixed-plan wholesale fallback (highest risk).**
`runStagedBuildViaRuntime` returns `m.handleBuildRun(0)` for the ENTIRE plan if ANY task
is not FILE_MUTATE/GIT_ACTION (runtime_cutover.go:195-200). A plan containing one
SHELL_EXEC task executes its FILE_MUTATE siblings through the legacy path:

```
handleBuildRun                       commands.go:2516
├─ m.execEng.Patches.SetLedger/.SetContextID    :2611-2612   // UI-owned execution.Engine
├─ SHELL_EXEC → interactive approval → runBuildShellExec    :2622-2641
└─ FILE_MUTATE → direct LLM patch generation → m.execEng.Patches (legacy apply)
```

This path invokes the provider and applies patches OUTSIDE the RuntimeExecutor — the
exact condition D-002's release rule forbids ("no legacy or secondary production path
can mutate outside RuntimeHost").

**L2 — Task amendment.** `amendBuildTask` → `handleBuildRun(stepNum)`
(commands.go:2370-2382) routes retried tasks straight to the legacy path.

**L3 — UI-owned transaction lifecycle.** `execution.Engine` (m.execEng) transactions
remain UI-driven: Begin on every mode switch into build/investigate/plan/review
(commands.go:1549-1551); Commit on proposal acceptance (update.go:1604-1608) and batch
apply (update.go:1750-1754; model.go:2478-2480 completeFastTrackBuild); Rollback on
reject/undo/abort (update.go:1197, keys.go:756-758, lifecycle.go:235-237,
program.go:467). Even where applies now flow through the executor, commit/rollback
bookkeeping authority is split across the UI.

**Structural root cause:** the migration made the *decision* layer converge on the
executor but left `handleBuildRun`'s provider+apply machinery intact as a live fallback
rather than deleting it, and the architecture tests scope their assertions to
`gateway.go`/`commands.go($hot)` only — they do not cover `runtime_cutover.go` fallbacks
or `amendBuildTask`.

**Corrected D-002 statement for the audit log:**

> The migrated RuntimeExecutor path is exercised under default construction (Phase 1
> cutover complete; regression-guarded). REMAINING BLOCKER: `handleBuildRun` remains a
> reachable secondary production mutation authority via (a) mixed staged plans containing
> SHELL_EXEC tasks, (b) amended-task retries, and (c) UI-owned Engine commit/rollback
> lifecycle. Equivalence argument unavailable: the legacy path lacks the executor's
> artifact gate, verifier-as-apply-gate, MutationSet evidence, and canonical event stream.

### 3.4 Non-mutating requests & boundary instantiation

- `/ask` content routes through the SAME autonomy decision authority
  (commands.go:262-275): read-only classification answers in-place; a mutation phrased
  inside `/ask` yields a capability-escalation proposal instead of silent execution. ✓
- BUT `compose.Wire` grants ALL capabilities to the session CapabilitySet at boot —
  Read, Write, Execute, Test, Patch, Checkpoint, Rollback (compose.go:520-528) — before
  any intent exists. Surface selection therefore does NOT gate boundary instantiation;
  only the autonomy grant ledger (`internal/autonomy/grant.go`) and PolicyEngine mode
  gates interpose at action time. This contradicts Boundary 1's letter
  ("Non-mutating requests do not instantiate mutation boundaries") while preserving its
  spirit (no authority is *exercised* without decision-layer clearance).
- The autonomous loop's read-only capabilities are documented as inherent to workspace
  contracts (controller.go:140-157) — consistent with SM-001/SM-002 but unbudgeted.

---

## 4. Gate-by-Gate Findings

### Gate B — Safety, Fidelity & Risk Scope

**Context probe execution (Boundary 2).** `Planner.Plan`
(internal/planner/orchestrator.go:113-147) classifies intent → allocates budget → calls
`gather` (:152-183), which queries GraphSource (Lea), LogSource (.logs tee), FileSource
(retrieval) **sequentially in priority order** — parallel execution permitted by the
constitution is not implemented (no errgroup/goroutine in planner). Probe failures:
adapters degrade individually (empty chunk lists), but no explicit failed-probe record
or quality-ranked degradation bundle is produced.

**Executor-side context.** `RuntimeExecutor.compileContext`
(executor.go:1038-1090) is strategy-owned: zero-context for direct_response, target
content for targeted mutation, repository evidence for investigation. The autonomy
runtime additionally compiles a deterministic Context Evidence Ledger
(`autonomy.CompileContext`, wired at runtime_cutover.go:141-144) which is injected as
authoritative evidence.

**SM-012 compound invariant.** NOT IMPLEMENTED.
- `DecisionInput` fields: Intent, confidences, MutationRisk, AffectedScope,
  RollbackAvailable, Granted (controller.go:56-65) — no context-quality field.
- Repo-wide search for degraded-context signals: only `file.parser_degraded`-style
  findings inside the autonomy evidence ledger (context.go:202, :274) — informational,
  never consulted by Decide().
- Consequence: an AMBIGUOUS-but-granted mutation with structurally degraded context can
  reach ask_user/auto_continue purely on confidence+risk; no fail-closed ceiling exists.

**Active quantitative bounds inventory:**

| Bound | Value | Location |
|-------|-------|----------|
| MaxAutonomousScope | 3 files | controller.go:104 |
| TargetConfidenceThreshold | 0.7 | controller.go:107 |
| IntentConfidenceThreshold | 0.6 | controller.go:110 |
| MutationBudget: max files | 100 | compose.go:531 |
| MutationBudget: max diff lines | 5000 | compose.go:532 |
| MutationBudget: max tokens | 1,000,000 | compose.go:533 |
| MutationBudget: max attempts | 10 | compose.go:534 |
| MutationBudget: max duration/step | 30 s | compose.go:535 |
| MutationBudget: max concurrent commands | 5 | compose.go:536 |
| MicroBudget | `budget.DefaultMicroBudget()` | compose.go:538-539 |
| Planner aggregate token budget | intent-allocated, per-source shares | orchestrator.go:118, types.go:41-50 |
| Apply timeout (per approval) | 90 s | executor.go:801 |
| Operation timeout (TUI submission) | 5 min | runtime_cutover.go:52; gateway.go:90 |
| Shell timeout | 30 s | main.go:172 |
| ALLOWED_FILE_TREE scope guard | plan targets ⊆ allowed set (+go.mod/go.sum implicit) | pkg/control/scope_guard.go:28-58 |
| High-risk targets require human approval | secrets/lockfiles/VCS-internals regex set | domain/policy/engine.go:71-73 |

**PolicyEngine precedence** (domain/policy/engine.go:33-80): mode boundary → physical
capability presence → high-risk approval → token budget. Wired to the production
AuthorizationEngine at compose.go:624-627.

### Gate C — Runtime Truth & State Taint

**Outcome vocabulary** (internal/execution/mutation.go:66-124): changed, created,
nochange, no_artifact, patch_failed, artifact_rejected, artifact_retryable_rejected,
truncated, apply_failed, verify_failed, skipped, cancelled, pending_approval, rejected,
failed, completed. Truthful and granular — **but no PARTIALLY_APPLIED**.

**Aggregate semantics** (mutation.go:171-193): any changed → changed; else any created →
created; else nochange; else no_artifact. A 1-of-5-files-applied batch that did NOT roll
back would aggregate to plain "changed". In practice the executor makes this moot by
rolling back the entire transaction on first apply error (executor.go:800-809 break +
:833 `RollbackTo(MutationFailed)`), then reconciling each file's `FilesystemChanged`
against post-rollback disk bytes (:910-935). Atomicity holds; *partial-application truth
retention across contracts* does not exist.

**External side-effect after commit:** SHELL_EXEC tasks run through
`runBuildShellExec` with interactive approval (Allow-Once / Allow-Always bypass /
Reject, commands.go:2614-2641). A shell failure marks the task failed/stalled and halts
remaining steps (:2543-2546, :2556-2563) — a workflow-level stall, NOT a session-state
change. Nothing attaches taint to `Session` (session.go:24-45 has no such field);
starting a new prompt proceeds normally. Boundary 11's taint lifecycle
(NORMAL→TAINTED→verified-recovery→NORMAL) has no implementation substrate.

**Provider usage truth:** `ModelInvocation.TokenInput/TokenOutput` carry
provider-reported counts with a `Known` flag separating "genuinely zero" from "unknown"
(executor.go:124-130); invocation evidence is retained on EVERY error return including
cancellations (:640-648); `finalizeResult` (:1662) sums into Completed.OutputTokens.
usage_forensics_test.go (5 tests) covers truncation/billing-truth cases. ✓

**UI projection truth:** lifecycle events generated only by the runtime graph
(AST-enforced); `execView.Project` renders from the canonical stream; audit logger
persists every envelope to `.izen/audit/events.ndjson` (compose.go:401-416).

### Gate D — OCC, Staging & Infrastructure

**Layered staging infrastructure:**

| Mechanism | Location | Semantics |
|-----------|----------|-----------|
| TxFS (transactional FS) | pkg/fs/txfs.go | In-memory staged map; two-phase commit (fsynced temp files → rename); Rollback restores captured origins incl. created-dir pruning (:1-15, 74-92) |
| TxResource adapter | pkg/fs/txresource.go | Delegates validation/snapshotting while transaction active |
| PatchManager shadow backups | internal/execution/patch.go:95, 299-301, 382-420 | Copies current file to `.izen/checkpoints/cp-<ctx>-backup/` before apply; QuickSave/QuickLoad bulk restore |
| MutationSet | internal/execution/mutationset.go:93-237 | State machine (Recording→Committed/RolledBack…), per-file MutationEvidence, OutcomeFor(path) |
| Verifier-as-apply-gate | executor.go:785-793 | Verification runs INSIDE Apply; failure restores shadow backup and fails the apply |
| WorkspaceSnapshot/SnapshotCache | internal/workspace/snapshot/snapshot.go:44-79 | sha256 ContentHash per file + git dirty state; diagnostics cache (primed at compose.go:540-544), NOT an OCC commit baseline |
| Checkpoint managers | internal/workspace/checkpoint/manager.go; internal/checkpoint/engine.go; internal/agent/checkpoint/manager.go | Named checkpoints; rollback availability probe feeds autonomy (compose.go:593-597) |
| ScopeGuard drift tracking | core/authorization/engine_test.go:TestScopeGuard_* | BeginTracking/CheckDrift/Revoke over tracked paths — closest existing OCC-like drift detector |

**OCC fingerprinting: ABSENT IN PRODUCTION.**
`NewProductionAuthorizationEngine` is wired with `newNoopSourceHashVerifier()`
(core/authorization/impl.go:10-17, :74; compose.go:625). The `VerifySourceHash(paths,
snapshotHash)` seam exists and is tested against the interface, but the production
implementation accepts everything. No `WorkspaceStateConflict` type exists; concurrent
external modification between snapshot and commit is undetected. Fingerprint granularity
(Boundary 5 OCC Precision Audit) is therefore **not auditable — no fingerprinting runs**.

**Benchmarks inventory (complete list):**

| Benchmark | Location |
|-----------|----------|
| BenchmarkGraphBuild20kLOC | internal/lea/bench_test.go:48 |
| BenchmarkGraphUpsertFile | internal/lea/bench_test.go:59 |
| BenchmarkStoreLoad20k | internal/lea/bench_test.go:73 |
| BenchmarkEvaluate_Pass | internal/core/authorization/engine_test.go:1047 |
| BenchmarkEvaluate_Step1Denied | internal/core/authorization/engine_test.go:1066 |

`BenchmarkExplicitIntentResolution` and `BenchmarkOCCAndStagingCost`: DO NOT EXIST.

---

## 5. Test Gap Matrix

Legend: ✅ exists · 🟨 partial equivalent exists · ❌ missing entirely.

### Gate A — Authority & Intent

| Mandatory (audit doc) | Status | Nearest existing evidence |
|-----------------------|--------|---------------------------|
| TestAskDoesNotActivateMutationBoundary | ❌ | commands.go:262-275 `/ask` autonomy routing (code only); TestAutonomyValidationCase1ConversationDirectResponse (autonomy_route_test.go:30) |
| TestModificationActivatesRequiredBoundaries | ❌ | TestAutonomyGrantNoRepeatedApproval (autonomy_route_test.go:185); handlers/executor_integration_test.go:47 drives REAL mutation via executor |
| TestAmbiguousIntentCannotAcquireMutationCapability | 🟨 | autonomy_acceptance_test.go:61-189 (grants remain empty on non-authorized flows); controller.go:165-176 (missing-cap → ask_user) |
| TestAmbiguousIntentCannotCreateRiskScope | ❌ | No RiskScope object exists to test |
| BenchmarkExplicitIntentResolution | ❌ | Deterministic path verified structurally (intent.go:62-65); latency unmeasured |
| Regression: migrated path exercised under default construction | 🟨 | TestRuntimeExecutorSingleCompositionBinding + TestEveryUserActionCrossesIntentGateway (scoped to gateway.go/compose.go only; do NOT cover §3.3 residuals) |
| No secondary path mutates outside RuntimeHost | ❌ | **Fails today** — see §3.3 L1/L2/L3 |

### Gate B — Context, Fidelity & Safety

| Mandatory | Status | Nearest existing evidence |
|-----------|--------|---------------------------|
| Parallel-probe audit | n/a (survey) | Sequential today (orchestrator.go:152-183) |
| Per-probe wall-clock/CPU/memory enforcement | ❌ | None |
| Aggregate context budget | 🟨 | fitBudget token caps (orchestrator.go:190-215); no fan-out/resource dimension |
| TestGraphASTFailureDegradesToSymbolIndex | ❌ | Adapter-level graceful empties only |
| TestAllIndexerFailureDoesNotAbortReasoningPath | ❌ | — |
| TestDegradedContextCannotIssueMutationContract | ❌ | No degraded-context concept |
| TestHighFidelityMutationRequiresStructuralContext | 🟨 | compileAutonomyBuildEvidence injects ledger as authoritative evidence (runtime_cutover.go:135-144) but nothing DENIES mutation without it |
| TestTargetedMutationMayUseLowerContextTierWhenContractAllows | ❌ | — |
| TestAmbiguousIntentCannotUseRiskScopeUnderDegradedContext (SM-012) | ❌ | Both operands partially modeled; conjunction unevaluated anywhere |
| Risk-scope blast-radius / lifespan / revocation tests | ❌ | ScopeGuard Revoke tested (core/authorization) but no RiskScope object |

### Gate C — Runtime Truth & Recovery

| Mandatory | Status | Nearest existing evidence |
|-----------|--------|---------------------------|
| Retries preserve Contract ID / scope / capability (×3) | ❌ | No Contract-ID object; RecoveryStrategy/Attempt fields + bounded_patch_contract_test.go (7) approximate recovery-cycle semantics |
| RecoveryChangesContractIdentity / RequiredOperationalParameter | ❌ | Window rotation logic only |
| Fresh snapshot after terminal contract / re-baseline conflict tests | ❌ | No OCC baseline |
| PARTIALLY_APPLIED produces explicit domain outcomes | ❌ | Vocabulary lacks it; per-file MutationEvidence exists (mutationset.go:139+) |
| TestPartialApplicationTaintsSession | ❌ | No taint |
| TestTaintedSessionCannotSilentlyIssueNormalMutationContract | ❌ | No taint |
| CompensationClearsTaintOnlyAfterVerifiedRecovery | ❌ | No compensation contract concept |
| UI cannot render success before MutationEvidence | 🟨 | execution_truth_matrix_test.go (15); domain_events_test.go (12); no named negative test |
| ProviderUsageProjectionUsesAuthoritativeUsage | 🟨 | usage_forensics_test.go (5); ModelInvocation.Known |

### Gate D — Mutation, OCC & Operational Cost

| Mandatory | Status | Nearest existing evidence |
|-----------|--------|---------------------------|
| TestConcurrentExternalWriteTriggersWorkspaceStateConflict | ❌ | Conflict type does not exist |
| TestStagingAbortLeavesNoPartialWrites | 🟨 | patch_test.go (54 tests) incl. rollback/shadow-restore; txfs_test.go (20) incl. rollback pruning; no cross-mechanism abort test |
| TestRecoveryRequiresFreshSnapshotAfterTerminalContract | ❌ | — |
| TestRebaseliningDoesNotEraseConcurrencyConflict | ❌ | — |
| Fingerprint granularity vs strategy | ❌ | No fingerprinting in production |
| TestExecutionContractRejectsMixedDomains | ❌ | Decomposition at dispatch level only (runtime_cutover.go:177-236) |
| TestExternalSideEffectOnlyRunsAfterVerifiedStateCommit | 🟨 | Sequential task ordering in handleBuildRun; Allow-Always bypass weakens it; no evidence-linked ordering guarantee |
| BenchmarkOCCAndStagingCost | ❌ | Only lea/authz benchmarks exist |
| Conflict frequency under LSP/formatter/watcher/concurrent tooling | ❌ | Unmeasured (Operational: UNMEASURED) |

**Aggregate: 0/30 mandatory items exist by name; 9 partial equivalents identified;
~350 adjacent tests in `internal/execution` (≈200), `internal/core/authorization` (28),
`pkg/fs` (25), `internal/workspace/*` (34), `pkg/control` (11), `internal/ui` (large
suites incl. autonomy_*, domain_events), `pkg/app/compiler` (35).**

---

## 6. Proposed Refactoring Entry Points (Ranked, Gates A→B→C→D)

### P0 — Gate A: close residual mutation-authority paths *(release-blocking)*

1. **Route mixed staged plans through the executor** — replace the wholesale fallback at
   `internal/ui/runtime_cutover.go:195-200` with per-domain dispatch: FILE_MUTATE tasks →
   `runRuntimeTaskRequest` (already exists, :243-267); SHELL_EXEC → shell capability with
   independent evidence. Entry point is small; `runRuntimeExecuteCmd` (:38) is reusable
   as-is.
2. **Redirect `amendBuildTask`** (commands.go:2381) to `runRuntimeTaskRequest` instead of
   `handleBuildRun`.
3. **Retire or subordinate `handleBuildRun`'s apply machinery** (commands.go:2516+):
   either delete the `m.execEng.Patches` provider+apply branch once 1-2 land, or move
   ownership behind the executor so the UI keeps only task bookkeeping. Then extend
   `TestNoDuplicateHotfixExecutionPath`-style AST guards to forbid
   `m.execEng.Patches.Apply*` calls anywhere under `internal/ui`.
4. **Consolidate transaction lifecycle** (commands.go:1550, update.go:1606/1752,
   model.go:2479): MutationSet already owns begin/commit/rollback inside Approve; the
   UI-side Engine transactions should become projection-only or be deleted.
5. **Land the named regression test**: `TestDefaultConstructionRoutesMutationsThroughExecutor`
   (runtime-behavioral, not AST) exercising autonomy-wired BUILD decisions end-to-end.
6. *(Optional hardening)* Make boot-time CapabilitySet intent-scoped or rename to
   "physical capability inventory" so Boundary 1's wording is satisfiable
   (compose.go:520-528).

### P1 — Gate B: fidelity signal + SM-012 fail-closed

1. **Add context fidelity to the decision model**: extend `DecisionInput`
   (controller.go:56) with `ContextFidelity` (tier enum + provenance), populated from
   the strategy profile's context decisions (executor.go:513) and the planner's
   Truncated/Dropped flags (orchestrator.go:139-146).
2. **Implement SM-012** in `Decide` (:114): if AMBIGUOUS-classified (confidence below
   thresholds) ∧ fidelity < mutation floor → `DecisionBlock` (fail-closed), ignoring
   grants. Land `TestAmbiguousIntentCannotUseRiskScopeUnderDegradedContext`.
3. **Add per-probe ceilings** in `Planner.gather`: wall-clock via per-source
   `context.WithTimeout`, worker/memory caps; record failed probes explicitly in
   `ContextPlan` (new FailedProbes field) so degradation is observable.
4. **Formalize Risk Scope** as an inspectable object (operation class, target scope,
   capability, lifespan, provenance, quantitative blast radius) — natural home:
   `internal/autonomy/grant.go` extension or new `internal/domain/riskscope`.

### P2 — Gate C: partial-application truth + taint

1. **Add `OutcomePartiallyApplied`** to the vocabulary (mutation.go:66) and a rule in
   `AggregateMutationOutcome` (:171): mixed applied/unapplied WITHOUT successful
   rollback → PARTIALLY_APPLIED (today unreachable due to executor atomicity, but the
   type is required for cross-contract batches and the shell-after-commit case).
2. **Add `TaintState` to Session** (session.go:24): NORMAL→TAINTED on unresolved partial
   application; consulted wherever build execution begins (transitionToBuilding,
   commands.go:2271 area); clearable only via verified recovery or explicit human
   baseline command.
3. **Contract identity**: introduce an ExecutionContract ID minted at gateway Gate time;
   assert retry-invariance (sampling params don't rotate ID) and
   recovery-changes-parameter — unlocks Boundary 4's five mandatory tests.

### P3 — Gate D: real OCC + operational measurement

1. **Replace `noopSourceHashVerifier`** (core/authorization/impl.go:10) with a sha256
   implementation over the declared mutation domain (SnapshotCache.ContentHash already
   computes per-file hashes — reuse it). Emit `WorkspaceStateConflict` on mismatch
   between authorize-time and apply-time state.
2. **Strategy-matched fingerprint scope**: full-file rewrite → whole-file hash;
   targeted → byte-region hash; multi-file → domain-wide manifest (Boundary 5 precision
   matrix).
3. **Land `BenchmarkOCCAndStagingCost`** plus intent-resolution benchmark; measure
   conflict rates under LSP/formatter/watcher churn (representative workloads already
   named in the audit).
4. **Mixed-domain contract rejection/decomposition tests** once P0-1 lands — the
   dispatch seam will then be testable as ordered independent contracts.

---

## 7. Survey Closure Statement

Per the audit's closure rules: **Gate A remains BLOCKED** — not by the documented
D-002 dormancy (resolved), but by the residual secondary mutation authority (§3.3) and
the absence of runtime-behavioral regression proof. Gates B–D contain implementable
surfaces whose invariants are currently unenforced or unmeasured; none may be used to
declare Gate A health. All statuses above are falsifiable anchors and should be
re-validated after each remediation landing.
