# IZEN PHASE 0 — AUTHORITY & SEMANTIC OWNERSHIP CENSUS

> Phase 0 only. Investigation, no refactor, no new abstractions, behavior unchanged.
> Method: source reads + `go list -deps` reachability + existing test suites + graph
> coverage check (project `Users-anvndev-Documents-Project-OpenSource-PizenLabs-izen`,
> generation `2026-09-20T13:16:39Z`, all 21 cited paths `no_recorded_issue`;
> best-effort signal, not completeness proof).
> No code was modified. No tests were added.

## A. Executive Summary

* Actual production path (TUI): `cmd/izen/main.go` → `runtime/compose.Wire`
  → `ui.RunMainDashboardWithApp` → `model.handleInput` → `intentFromInput`
  → `parser.ParseInWorkspace` → `dispatchASTIntent` → `runGatedLine` →
  `execution.IntentGateway.Gate` → `execution.RuntimeExecutor.Execute` →
  `PatchManager.Apply` → `os.WriteFile` (+ `Verifier`, `ExecutionEvidence`).
* Actual execution authority: `execution.RuntimeExecutor` (`internal/execution/executor.go:432`),
  admission-checked via `AdmissionGateway` + `authorization.MutationAuthorization`
  (single-use, expiring) + OCC pre-commit gate + human approval gate.
* Actual mutation authority: `ScopeProvenance` (`internal/core/domain/execution_intent.go:7`;
  zero value `ScopeNone` = read-only) minted ONLY by `parser/parser.go:145-153`
  from `$prompt`/`$hot`; enforced at gateway downgrade, staged-build gates, and executor admission.
* `$prompt`/`$hot` boundary: centralized in parser → `IntentGateway.selectScopedStrategy`
  → autonomy ask-ceiling, BUT NOT airtight: the `!<shell>` path bypasses it entirely (P0-1).
* Largest ownership conflict: TWO `RuntimeExecutor`s (`execution` vs `runtime/executor`)
  both claim to be the sole mutation authority (P0-2).
* Highest-risk bypass: `!` shell executes arbitrary commands in `/build`/`/investigate`
  with no grant, no approval, no executor (P0-1); `$test`/`$run` execute test binaries
  (arbitrary code) under read-only modes (P1-1).

## B. Production Execution Graph

### B.1 TUI (canonical interactive entry)

```text
cmd/izen/main.go:226 compose.Wire (composition root: OSFile, ExecShell, GitCLI, PatchAdapter, bus)
  ↓
ui.RunMainDashboardWithApp
  ↓
model.handleInput — internal/ui/commands.go:246
  ├─ "!" prefix → shellFirewall → execShell → capabilities.NewExecShell(0).Execute  [BYPASS, §K P0-1]
  ├─ parser pipeline: intentFromInput — internal/ui/intent_dispatch.go:31
  │     → parser.ParseInWorkspace (perm check vs workspace; $prompt/$hot mint ScopeProvenance)
  │     → $prompt/$hot permission fallback re-parse in WorkspaceBuild (:50-56, no mode switch)
  ↓
dispatchASTIntent — intent_dispatch.go:139 (workspace transition / composite / commands / directives)
  ├─ $prompt → routePromptDirective (:287) → autonomy.Decide → dispatchAutonomyTrace
  │     → executeAutonomyWorkspace (autonomy_route.go:127) → mode engines / driver / gateway fallback
  ├─ $hot    → routeHotfixThroughAutonomy (:41, binds ScopeDeclared) → autonomy or runHotExecution
  ├─ /review $test → runReviewTestComposite (command/router.go:66)
  ├─ bare /mode → handleMessageContent / runInvestigateCmd / runReviewCmd / setMode
  └─ free text → routeFreeInput (ask-mask) → runGatedLine — commands.go:475
        ↓
runGatedLine — internal/ui/gateway.go:43 (single execution entry)
  → IntentGateway.Gate — internal/execution/intent.go:83
  → strategy.Select (unconditional) + selectScopedStrategy downgrade (:150)
  → RuntimeExecutor.Execute — internal/execution/executor.go:998
      → admission (context-fidelity + risk-scope) → checkAuthorization (single-use token)
      → provider invoke → PatchManager.Apply → OCC pre-commit → approval gate
      → os.WriteFile (patch.go:594) → Verifier → ExecutionEvidence → events bus → UI projection
```

### B.2 Headless entries (non-TUI, same libraries, distinct funnels)

| Entry | Funnel | Authority |
| ----- | ------ | --------- |
| `izen run "<prompt>"` (runtime.go:108) | `app.Pipeline.Run` (Capability Registry → Extractor → Artifact IR → Planner → ExecutionGraph → Kernel) + `substrate.NewConcreteSubstrate` | Pipeline + `ConcreteSubstrate`; audit-flush failure invalidates success |
| `izen orchestrate/prompt` (orchestrate.go:40) | `cli.Wire` → preflight → LLM proposal → validation → interactive `[y/N]` approval → atomic commit | `runtime/orchestrator` + terminal human approval |
| `izen compact` (main.go:418) | direct `os.WriteFile` of compressed docs | CLI-explicit invocation (user typed the command) |
| `izen debug` | read-only report (Lea + planner + logs inspection) | none (no mutation) |

### B.3 Per-command routing (TUI)

* `/ask` — `handleMessageContent` read-only chat (Context Planner governed); mutation words
  typed in `/ask` never execute: autonomy classifies → capability-escalation proposal
  (commands.go:327-340); with autonomy unwired, free text still crosses `runGatedLine` →
  gateway downgrade to `RepositoryInvestigation`/`TargetedReasoning` explanation contract.
* `/plan` — `modes/plan.Engine` synthesizes JSON tasks → staged into session; execution
  requires `StagedScopeProvenance.AllowsMutation()` at `runStagedBuildViaRuntime` /
  `runRuntimeTaskRequest` (runtime_cutover.go:223,302).
* `/build` — staged-plan executor above; `handleBuildRun` per-task; `SHELL_EXEC` tasks
  → `runStagedShellGate` interactive approval (commands.go:2838); `!/cmd` bypasses (§K).
* `/build $prompt` — `$prompt` tail routed via `routePromptDirective` (no mode alignment,
  intent_dispatch.go:87-91); gateway + executor as B.1.
* `/build $hot` — `routeHotfixThroughAutonomy` binds `ScopeDeclared`, autonomy decides
  BUILD workspace with hotfix semantics; fallback `runHotExecution` → gateway.
* `/investigate` — `runInvestigateCmd` (agents.go:25) forensic engine (read-only evidence
  + hypothesis); mutation intent with target → `runRuntimePrompt` (commands.go:593-598);
  `$test` runs test binaries (P1-1); `!` shell allowlisted only (shellFirewall).
* `/review` — `runReviewCmd` (agents.go:463) hard read-only pre-checks (write/shell/patch
  capability → error); `review.Engine` audit; `$test` composite runs suite then review.

## C. Authority Matrix

| Semantic | Current Owner | Evidence | Reachability | Status |
| -------- | ------------- | -------- | ------------ | ------ |
| Intent (parse) | `parser.ParseInWorkspace` + `domain/command.Registry` | parser.go:88-161; registry.go:1-8 (stdlib-only) | prod (all input) | CANONICAL |
| Intent (semantic fallback) | autonomy classifier + `routeFreeInput` mask | commands.go:475-522; runtime/autonomy/engine.go (ask-ceiling) | prod (TUI) | CANONICAL |
| Authorization (token) | `core/authorization` (`MutationAuthorization`, single-use/expiring) via `execution/auth.go:checkAuthorization` | auth.go:30-56; engine.go:89-98 | prod (executor) | CANONICAL |
| Authorization (policy) | `domain/policy.PolicyEngine` (delegated) + `modes.EffectiveCapabilities` (grant ∩ mode) | core/authorization/engine.go:60-87; modes.go:113-119 | prod | CANONICAL |
| Grant (`$prompt`/`$hot`) | `ScopeProvenance` minted in parser.go:145-153 | execution_intent.go:7-19; parser_test.go:156-162 | prod | CANONICAL |
| Planning | `execution/planner` + `modes/plan.Engine` synthesis + `planner/scope` scoring | modes/plan/engine.go; planner/scope/engine.go | prod-transitive | CANONICAL (dup surface, §J) |
| Capability (authz) | `domain/capability` bitmask | capability.go:26-40 | prod | CANONICAL |
| Capability (scheduling) | `provider/*`, `llm/*`, `core/domain/provider`, `prompt/*` | §G | prod-transitive | PROJECTION (non-authoritative by invariant) |
| Execution | `execution.RuntimeExecutor.Execute` (executor.go:998) | phase0 lock suite pins SOLE authority | prod (TUI gateway) | CANONICAL |
| Execution (2nd claimant) | `runtime/executor.RuntimeExecutor.Execute` (executor.go:124, 6-step + 8-clause) | §K P0-2 | compose-wired | UNKNOWN (conflict) |
| Verification | `execution.Verifier` (+ language steps) pre/post apply | verify.go:226-411; patch.go:236 | prod | CANONICAL |
| Evidence | `execution.ExecutionEvidence` (COMMITTED/FAILED/ABORTED_OCC/CANCELLED) + bus → `.izen/audit/events.ndjson` | evidence.go:17-60; main.go:273-291 | prod | CANONICAL |
| Shell | SPLIT: substrate path (authorized) vs `execShell` direct (unauthorized) | substrate.go; commands.go:4214 | both prod | SPLIT — P0-1 |
| Checkpoint/rollback | `checkpoint.*` via executor step 4 + `boundary/rollback.go` | executor staging.go:436 CommitGate | prod | CANONICAL |
| Recovery decision | `runtime/autonomy/recovery.go` zero-trust matrix (executor path); `execution/repair.go` patch-recovery prompt | §H | prod | CANONICAL (path-local) |
| Recovery (controlplane) | `controlplane/failure.RecoveryManager` | failure/recovery.go | DEAD (§10) | LEGACY |

## D. Mutation Sink Matrix

| Sink | Path | Authorization | Guard | Status |
| ---- | ---- | ------------- | ----- | ------ |
| `PatchManager.Apply` → `os.WriteFile` (patch.go:479/594) + shadow backup (332) / restore (413) | `RuntimeExecutor.Execute` only | admission + `checkAuthorization` + OCC + approval gate + `SetAuthorization/SetBudget` (267-279) | ScopeGuard file/symbol + budget (controlplane/guard analog in-path) + `verifyTargetsAtUse` analog (OCC baseline) | AUTHORIZED |
| `substrate` file ops + `os/exec` (substrate.go:77-80 claims ONLY package) | `runtime/executor` (guard.Evaluate → ckpt → ExecuteUnit) + app pipeline `ConcreteSubstrate` | 8-clause guard / pipeline admission | scope FD confinement (`ErrWorkspaceEscape`), AST re-anchor (`ErrVerificationFailed`) | AUTHORIZED (within its path) |
| `infrastructure/capabilities.ExecShell.Execute` (exeshell.go:35, `sh -c`, timeout+process-group) | substrate (authorized) AND `ui.execShell` direct (commands.go:4214) | authorized-branch only | shellFirewall blacklist + mode CanShell (UI branch only) | GUARDED-then-BYPASSABLE — P0-1 |
| `resource/file` Write/Remove (file.go:192-223), `atomicio` Rename (atomic.go:68) | substrate/ports adapters | via substrate callers | root containment | AUTHORIZED (by caller) |
| Git mutation (`GitCLI` port; `git/engine.go:126` commit-msg tmp; modes/commit semantic-only, delegates to substrate) | executor/substrate or explicit `/commit`,`/undo`,`/checkpoint` commands | command permission (parser denies `/commit`/`/undo` in read-only ws, parser_test.go:190-199) | mode matrix (only build has CapCheckpoint) | AUTHORIZED |
| Session/audit/state writes (`.izen/`: session manager/ledger/pointer, events/audit/store, audit.go) | always-on bookkeeping | n/a (non-target writes) | frozen inventory `TestPhase0UIWorkspaceWritesLockedToBookkeeping` | AUTHORIZED (bookkeeping) |
| `izen compact` `os.WriteFile` (main.go:475) | explicit CLI invocation | invocation = intent | dry-run flag | AUTHORIZED |
| Review/investigate `$test`/`$run` (`go test`/`go build` via sandbox.go:111,175; runTestCmd) | read-only modes, no grant required | mode CanTest only | investigator allowlist (shellFirewall) — does NOT confine test-code side effects | BYPASSABLE — P1-1 |
| `controlplane/*`, `engine/executor`, `execution/scheduler`, `runtime/engine`, `runtime/scheduler`, `modes/undo` sinks | none found | — | — | DEAD/LEGACY (absent from `go list -deps ./cmd/izen`) |

Convergence answer: NO — production mutation does NOT converge through one boundary.
Two executor authorities (§K P0-2) plus the `!`-shell side door (§K P0-1) exist alongside
the canonical gateway→executor→PatchManager path.

## E. `$prompt`/`$hot` Boundary Matrix

Intended invariant: no grant → READ ONLY; `$prompt` → broad-task grant (`ScopeDynamic`);
`$hot` → bounded pre-approved grant (`ScopeDeclared`). Parser is the minter
(parser.go:145-153); zero value `ScopeNone` is read-only by construction.

| Command | No Grant | `$prompt` | `$hot` | Actual Enforcement |
| ------- | -------- | --------- | ------ | ------------------ |
| `/ask` | read-only chat; gateway downgrades mutation profiles to investigation/explanation (intent.go:150-171); autonomy ask-ceiling (no CapMutate/CapPropose) | routes via autonomy/gateway; mutation stages proposal (`DecisionAskUser`), executes only after human authorizes | parse-DENIED in /ask workspace (`/ask $hot` → ErrPermissionDenied, parser_test.go:157); retried in Build ws with NO mode switch (intent_dispatch.go:50-56) | CENTRALIZED (parser+gateway+mask) |
| `/plan` | synthesis + staging only; `runStagedBuildViaRuntime` refuses without staged mutation scope (runtime_cutover.go:223) | same as /ask row | same fallback as /ask row | CENTRALIZED |
| `/build` | staged/legacy paths fail closed (`ScopeAuthorizationError`, unwired executor → error, runtime_cutover.go:302-311); BUT `!shell` executes (P0-1) | `routePromptDirective`, no mode alignment | `routeHotfixThroughAutonomy` binds `ScopeDeclared` | CENTRALIZED except `!` |
| `/investigate` | forensics read-only; mutation re-routes via `runRuntimePrompt` (grant-checked, 338-340); `$test` runs binaries (P1-1); `!` allowlisted (P0-1) | via autonomy/gateway | same fallback | CENTRALIZED except `!`/`$test` |
| `/review` | hard read-only pre-checks deny write/shell/patch capability (agents.go:494-503); review engine `ErrWriteForbidden/ErrShellForbidden`; `$fix` blocked (commands.go:3724-3741) | n/a (review decides ask/build workspaces via autonomy) | n/a | CENTRALIZED except `$test` composite (P1-1) |

Bypass question — can model/planner/capability cause execution without `$prompt`/`$hot`?
* Model output: NO on the canonical path — artifacts pass `artifactValidator`
  (NormalizingValidator, executor.go:489/507-512), `artifactGate`, and the approval/OCC
  gates; LLM authority-override bytes are denied at the fast-path gate
  (`runtime/executor` Step 0). Capability availability ≠ authorization (modes.go:65-73,
  provider invariant in provider/capabilities.go:19-23).
* Planner output: NO — staged plans re-cross admission per task; `/build` with empty
  grant yields empty effective set → `ErrCapabilityDenied` (modes.go:113-119).
* Command semantics: NO — registry permission check denies write directives in read-only
  workspaces; slash fallthrough guard fails fast on unknown commands (commands.go:389-397).
* YES paths (documented, not fixed): P0-1 `!` shell; P1-1 test-binary execution.
  Both are human-typed (not model-initiated), so the LLM→mutation invariant holds;
  the human-grant invariant does not fully hold for shell.

## F. Scope Ownership Matrix

| Semantic | Implementation | Role | Classification |
| -------- | -------------- | ---- | -------------- |
| Target Resolution (canonical identity) | `runtime/target.Resolver` (VCS > filesystem > raw; rejects absolute) | resolves spelling → canonical path | CANONICAL |
| Target Resolution (LLM-drift repair) | `retrieval.TargetPathResolver` (reserved-keyword reject → exists → glob → rg) + `hotfix.Resolve*` (pre-model block extraction) | repairs model-emitted paths/blocks | ADAPTER |
| Change Surface (decomposition) | `planner/scope` elastic scoring (SinglePass vs DAG from structural complexity) | plans how, not whether | PROJECTION (scheduling) |
| Change Surface (blast radius) | `execution` RiskScope tiers (`ScopeReadOnly/Mutate/ShellSideEffect/Destructive`, admission.go) + `strategy.Select` profiles | admission tiering | CANONICAL (executor path) |
| Authorization Scope | `ScopeProvenance` (None/Dynamic/Declared) + `domain/command` workspace permissions | the grant | CANONICAL |
| Mutation Guard (proposal) | `runtime/scopeguard.IntentGateway.Authorize` (ScopeGuard → Structural → policy/budget; Proposal≠Execution) | pre-dispatch decision | CANONICAL (its path) |
| Mutation Guard (staged plan) | `boundary/scopeguard.ValidateStagedPlan` (allowed-tree check; go.mod/sum implicit) | plan filter | ADAPTER |
| Mutation Guard (patch) | `controlplane/guard.ScopeGuard` (file+symbol+b budget) | patch validation | LEGACY (dead per §10) |
| Mutation Guard (execution-time) | `runtime/scope.Root.Verify` FD-anchored TOCTOU confinement + substrate `ErrWorkspaceEscape` + OCC re-validation | use-time confinement | CANONICAL |
| Target extraction (effect) | `runtime/target/extract.go`, `execution/targets.go`, `directiveTail` (intent_dispatch.go:254) | payload plumbing | ADAPTER |

`Target Resolution ≠ Change Surface ≠ Authorization Scope ≠ Mutation Guard` holds in
code: four distinct mechanisms, four distinct owners. `$hot` scope-expansion rejection:
parser-declared scopes + `ScopeDeclared` provenance flow into admission risk-scope and
`ScopeGuard.Enforce`; cross-tier movement requires NEW submission (admission.go: "never
an automatic promotion"); FD use-time verify + OCC abort on drift (`ABORTED_OCC`, zero
partial writes).

## G. Capability Ownership Matrix

| Source | Content | Affects execution? | Classification |
| ------ | ------- | ------------------ | -------------- |
| `domain/capability` bitmask (Read/Write/Execute/Test/Patch/Checkpoint/Rollback) | runtime authorization | YES (sole authz vocabulary) | CANONICAL |
| `modes.Capability` matrix + `EffectiveCapabilities` (grant ∩ mode) | policy-surface filter | YES as filter, grants nothing | PROJECTION (filter) |
| `provider/capabilities.go` (`ProviderCapabilities`, 1024/980 constrained rule) + `core/domain/provider` (per-call limit insulation) + `autonomy/adaptive_selector.go` (BOUNDED_PATCH forcing) | output-token scheduling | NO — static invariant: MUST NEVER influence authorization | PROJECTION (scheduling) |
| `llm/capabilities.go` (variants, context/completion bounds) | provider-native view | NO (budgeting only) | PROJECTION |
| `prompt/capabilities.go` + `tier_adapter.go` (workspace-capability header injection) | model-facing text | NO (informs model, enforces nothing) | PROJECTION |
| `autonomy.GrantLedger` (session grants, `Covers`/`ActiveCaps`) | decision-layer grants | feeds autonomy decisions, not executor admission | ADAPTER |
| Provider truth origin | endpoint metadata (OpenRouter `/models`, Ollama `/api/show`) + heuristic fallback | — | (origin, read-only) |
| Production trust | executor admission + `domain/capability` + `ScopeProvenance`; token/output limits affect ONLY `strategy`/retry shape (`$inspect`-visible `lastExecutionStrategy`) | — | no conflicting authority found (invariants tested) |

## H. Completion / Recovery Matrix

| Path | Reachability | Role | Classification |
| ---- | ------------ | ---- | -------------- |
| `runtime/autonomy/recovery.go` zero-trust matrix (OutputExhausted→1 typed FULL_REWRITE→BOUNDED_PATCH transition, 2nd halt; Refusal/PreflightInfeasible halt; Drift abort) | prod (autonomy driver) | retryability decision | CANONICAL (driver path) |
| `execution/repair.go` (`RepairRecoveryPrompt` SEARCH/REPLACE contract) + `verify.go` micro-fix loop (compiler check → rollback → pinpoint re-prompt) | prod (executor) | patch-level recovery | CANONICAL (executor path) |
| `controlplane/failure` taxonomy (`CODE/ENV/TEST/SCOPE/SECURITY/UNKNOWN`) + `RecoveryManager` (2 auto-repairs then escalate) | DEAD (absent from prod deps) | — | LEGACY |
| `engine/retry.go`, `execution/ingestion/repair`, `runtime/autonomy/ast_repair.go`, `providers/watchdog` | transitive/legacy; ast_repair prod-adjacent | varied | ADAPTER/LEGACY (per-file) |
| `session/recover.go`, `checkpoint` manager/pruner | prod (persistence durability, rollback support) | session/mutation recovery substrate | CANONICAL (substrate, not decision) |
| `providers/watchdog` | UNKNOWN (not traced to dispatch) | — | UNKNOWN |

Outcome axes (existing, NOT yet unified — no new taxonomy implemented):
Generation Outcome (artifact validator verdict) vs Execution Outcome (`ExecutionOutcome`
COMMITTED/FAILED/ABORTED_OCC/CANCELLED) vs Control Verdict (guard `Permitted`+clause /
`DecisionAllow/Deny/RequireVerification`) vs Transport Outcome (`StreamOutcome`
stop→COMPLETE/length→PARTIAL/error→FAILED/cancel→CANCELLED) vs Evidence State
(`evidence.EvidenceState`, `VerdictFailed`→rollback). Mapping between axes: UNKNOWN
(no canonical mapper found; noted, not invented).

## I. Existing Adaptive / Autonomy Paths

| Loop / component | Reachability | Owns continuation? | Owns scheduling? | Unauthed exec? |
| ---------------- | ------------ | ------------------ | ---------------- | -------------- |
| `autonomy.Engine` (intent classify → capability → workspace → controller decision; `GrantLedger`) | prod (TUI wired; fallback gateway when nil) | NO (decides, dispatches once) | NO | NO (ask-ceiling + proposal gate) |
| `runtime/autonomy` Driver (resolve→observe→decide→execute→interpret→approval→complete via `RuntimeExecutor`; `ExecutorAdapter`) | prod (`m.autonomousDriver`; harness fallback single-shot) | YES (bounded loop, `loop.transition` events, parked human boundary) | YES (within run) | NO (approval gate + executor admission) |
| `runtime/adaptive` 5-tier context ladder (L0→L4 on `EvidencePressure`) + compactor/pressure/negative guards | prod-transitive (context budgeting) | NO | context-tier only | NO (no execution) |
| `autonomy/adaptive_selector.go` (bounded-patch forcing) | prod (pre-execution heuristic) | NO | strategy-shape only | NO |
| `engine/pipeline/adaptive.go` (`AdaptivePlan`/`RunAdaptive` Observe→Decide→Execute) | UNKNOWN (imported by modes engines via `engine/pipeline`; `RunAdaptive` callers not confirmed) | UNKNOWN | UNKNOWN | UNKNOWN — do not assume dead or prod |
| `execution/scheduler`, `runtime/scheduler`, `runtime/engine`, `engine/executor` | DEAD (not in prod deps) | — | — | — |
| `execution.Scheduler` (tool-call fan-out), `execution/strategy.Select` | prod | NO (single decision fn) | per-request | NO |

Continuation owner: `runtime/autonomy` Driver (bounded, approval-parked).
Step-scheduling owner: split — Driver (execution steps) vs `strategy.Select` (per-request
profile) vs adaptive ladder (context tiers only).
AEI plug-in seam (proposal, not implementation): the autonomy decision boundary
(`autonomy.Engine.Decide` → `dispatchAutonomyTrace`) and the gateway strategy seam
(`IntentGateway.SelectStrategy` → `RuntimeExecutor.Execute`) already separate
decision from execution; a future planner belongs behind one of those seams, never as
a second runtime beside them.

## J. Duplicate Semantic Inventory

| Concept | Implementations (classification + 1-line justification) |
| ------- | ------------------------------------------------------- |
| Intent | `parser.IntentAST` CANONICAL (all input crosses it) · `app/compiler.IntentCompiler` ADAPTER (headless `izen run` path) · `autonomy` intent classifier CANONICAL (decision input) · `engine/intent` LEGACY (no prod dispatch found) |
| Target | `runtime/target` CANONICAL (VCS-anchored identity) · `retrieval.TargetPathResolver` ADAPTER (drift repair) · `execution/targets.go` ADAPTER (effect plumbing) · `hotfix.Resolve*` ADAPTER (pre-model block) |
| Scope | `ScopeProvenance` CANONICAL (grant) · `runtime/scope` CANONICAL (use-time confinement) · `runtime/scopeguard` CANONICAL (proposal gate) · `planner/scope` PROJECTION (decomposition) · `boundary/scopeguard` ADAPTER (staged-plan filter) · `controlplane/guard` LEGACY (dead) |
| Authorization | `core/authorization`+`execution/auth.go` CANONICAL (token) · `domain/policy` CANONICAL (delegated verdicts) · `runtime/authorization.ApprovalGate` CANONICAL (human gate, driver path) · `executor/permission.go` ADAPTER (whitelist dispatch) |
| Capability | `domain/capability` CANONICAL · `modes` matrix PROJECTION · `provider/llm/core-provider/prompt` PROJECTION (scheduling/text) · `autonomy.GrantLedger` ADAPTER |
| Executor | `execution.RuntimeExecutor` CANONICAL (phase-0-locked) · `runtime/executor.RuntimeExecutor` UNKNOWN-conflict (P0-2) · `app.Pipeline` kernel ADAPTER (headless funnel) · `cli` orchestrator ADAPTER (headless funnel) · `engine/*Executor/control.WorkerPool` LEGACY/ADAPTER |
| Planner | `execution/planner`+`modes/plan` CANONICAL (TUI path) · `planner/` (brownfield/greenfield/context) ADAPTER (headless + debug paths) · `engine/planner|plan|strategy` LEGACY (mode engines import pipeline facade; standalone planning dead?) → UNKNOWN where unproven |
| Context | `runtime.ContextLedger/LedgerBuilder` CANONICAL (projection) · `context/`+`contextcompiler` ADAPTER (assembly/budget) · `core/domain/context` CANONICAL (types) · `autonomy/context.go` ADAPTER |
| Recovery | §H (path-local canonicals; controlplane LEGACY) |
| Verification | `execution.Verifier` CANONICAL · `review.RiskAuditor` ADAPTER (audit, not gate) · language verifiers CANONICAL (per-lang steps) · `controlplane/risk` LEGACY (dead) |
| Budget | `core/budget.MutationBudget/MicroBudget` CANONICAL (admission+guard) · `runtime/executor.BudgetTracker` CANONICAL (domain path) · `runtime/scopeguard.SimpleBudget` ADAPTER (proposal gate) · `contextcompiler/budget.go` ADAPTER |
| Checkpoint | `checkpoint.Manager` CANONICAL (create/restore/prune) · `core/domain/checkpoint` CANONICAL (types/coordinator) · modes `/checkpoint` command ADAPTER (surface) |
| Decision | `autonomy.Engine` CANONICAL (workspace/grant decision) · `engine/decision` LEGACY/ADAPTER (pipeline-internal) · `runtime/ui/decision` surface ADAPTER |
| Completion | `ExecutionEvidence` outcome CANONICAL (terminal truth) · mode `StateMachine`s ADAPTER (phase tracking) · workflow SM ADAPTER (presentation) |
| Evidence | `execution.ExecutionEvidence` + audit ndjson CANONICAL · `modes/*/evidence|ledger` ADAPTER (mode-local) · `diagnostic_ledger` ADAPTER |
| Strategy | `execution/strategy.Select` CANONICAL (unconditional decision) · `modes/*/strategy.go` ADAPTER (mode compilation) · `engine/strategy` LEGACY |
| Pipeline | `engine/pipeline` (layered, used by mode engines) ADAPTER/CANONICAL-per-path · `engine/pipeline/adaptive.go` UNKNOWN (§I) · `app.Pipeline` ADAPTER (headless) |

## K. Critical Findings

```text
P0-1  `!`-shell bypasses the human-grant boundary.
      internal/ui/commands.go:246-285 dispatches BEFORE the parser; execShell (:4214)
      news up capabilities.NewExecShell(0) with no ScopeProvenance, no IntentGateway,
      no RuntimeExecutor, no approval. Guards are only mode CanShell + shellFirewall
      blacklist (rm/sudo/chmod/…): `!git clean -fdx`, `!python -c '…write…'`,
      `!go run …` etc. execute in /build or /investigate with zero grant.
      Effect: shell/workspace mutation without $prompt/$hot. Evidence: source above;
      lock suite covers target-file writes, not this direct shell seam.

P0-2  Dual RuntimeExecutor authority (canonical conflict, unresolved in Phase 0).
      execution.RuntimeExecutor (intent.go:83 Gate → executor.go:998 Execute;
      phase-0 lock suite: "SOLE workspace-mutation authority") vs
      runtime/executor.RuntimeExecutor (6-step CapabilityGuard→checkpoint→substrate→
      evidence/rollback, executor.go:26-36,124). Both live, both wired (TUI vs
      compose/autonomy-driver funnels). Phase 1 MUST NOT assume either is dead;
      convergence must be proven, not declared.

P1-1  Test-binary execution under read-only modes. $test/$run (review/investigate)
      execute `go test`/`go build` (review/sandbox.go:111,175) with only CanTest gating.
      Test code is arbitrary code with file/network access; a malicious or careless
      test mutates the workspace with no $prompt/$hot. Read-only claim is about
      Izen's own writes, not the test binary's.

P1-2  controlplane/* (failure/guard/risk) is DEAD but present and authoritative-looking.
      Absent from `go list -deps ./cmd/izen`; only self-imports+tests. Risk: future work
      (incl. AEI) wiring to it by name. Recommend header deprecation notice (doc-only).

P1-3  engine/pipeline/adaptive.go reachability UNKNOWN. RunAdaptive callers unconfirmed;
      modes engines import engine/pipeline facade. AEI MUST re-verify before reusing.

P2-1  Outcome taxonomy axes (Generation/Execution/Control/Transport/Evidence) coexist
      with no canonical mapper (evidence vs StreamOutcome vs FailureClass vs gateway
      Decision). Marked UNKNOWN; unification is Phase ≥1 work.

P2-2  Six capability representations coexist; scheduling views carry explicit
      must-not-influence-authorization invariants (tested). No conflicting authority
      found — hygiene item, not a hole.
```

## L. Recommended Canonical Ownership

(Proposals only — no code changed.)

| Concept | Candidate canonical | Rationale |
| ------- | ------------------- | --------- |
| Intent | `parser.IntentAST` + `domain/command.Registry` | sole production parse; stdlib-only; permission-checked |
| Grant/scope provenance | `core/domain.ScopeProvenance` | zero-value read-only; single minter |
| Authorization | `core/authorization` token + `domain/policy` verdicts, admitted at ONE executor | merge P0-2 first; token shape already single-use/expiring |
| Execution | ONE `RuntimeExecutor` (prove `execution` vs `runtime/executor` convergence; current lean: `execution` for TUI path per lock suite) | P0-2 resolution required |
| Mutation sink | `PatchManager.Apply` (files) + substrate ports (file/shell/git) — both behind the single executor | only sinks reachable with auth |
| Target identity | `runtime/target.Resolver` | VCS-anchored; rejects absolute |
| Use-time confinement | `runtime/scope.Root` + OCC | FD-anchored TOCTOU close |
| Capability (authz) | `domain/capability` bitmask | sole authz vocabulary |
| Capability (scheduling) | keep projections, keep invariants | explicitly non-authoritative |
| Verification | `execution.Verifier` + evidence-gated rollback | pre/post apply |
| Evidence | `execution.ExecutionEvidence` + bus → audit ndjson | sole terminal truth |
| Recovery decision | zero-trust subtype matrix (port `runtime/autonomy/recovery.go` semantics to the single executor) | deterministic, bounded, halt-biased |
| Planning input | `execution/strategy.Select` seam | unconditional single decision point |
| Decision (workspace/grant) | `autonomy.Engine` | already separated from execution |
| Dead code | `controlplane/*`, `execution/scheduler`, `runtime/engine`, `runtime/scheduler`, `engine/executor`, `modes/undo` — deprecate-by-header after reverse-deps audit | unreachable per `go list -deps` |

## M. Phase 0 Exit Assessment

```text
1. Do we know the real production execution path?            YES — §B.1 end-to-end
   (compose → handleInput → parser → dispatch → gateway → execution.RuntimeExecutor
   → PatchManager → os.WriteFile → Verifier → Evidence), plus headless funnels §B.2.
2. Do we know the real execution authority?                  MOSTLY — execution.RuntimeExecutor
   is the TUI-path authority (lock-pinned), BUT P0-2 dual-executor conflict is open.
3. Do all mutation sinks have an identifiable authorization boundary?
   NO — P0-1 (`!` shell) and P1-1 (test binaries) lack a grant boundary. All other
   production sinks converge on executor admission.
4. Is $prompt/$hot enforcement centralized?                  MOSTLY — parser mints,
   gateway downgrades, autonomy masks, staged gates refuse; NOT airtight (§E).
5. Do we know where Adaptive Planner should integrate?       YES as seam, NO as design:
   behind autonomy.Decide or IntentGateway.SelectStrategy; never a second runtime. (§I)
6. Are the canonical semantic owners sufficiently clear to begin Phase 1?
   YES for intent/grant/capability/evidence/verification/scope-guard; CONDITIONAL
   overall — P0-2 must be resolved first, P0-1 dispositioned (accept+document or close).
```

DoD trace (index.html):
* Decide: `parser` (grant?) → `IntentGateway.Gate` (+ `strategy.Select`) → autonomy
  `DecisionAskUser` proposal → human ↑/↓+Enter → `guard.Evaluate`/admission.
* Enforce: `checkAuthorization` (single-use token) + `ScopeGuard`/`StructuralGuard` +
  OCC baseline + `runtime/scope` FD use-time verify (`ErrWorkspaceEscape`).
* Perform: `PatchManager.Apply` → `os.WriteFile` (patch.go:594).
* Prove: `ExecutionEvidence` (COMMITTED/…) + bus → `.izen/audit/events.ndjson` +
  shadow backup + `$inspect` timeline.
* `/ask` without grant: parser mints `ScopeNone` → gateway `selectScopedStrategy`
  downgrades to investigation/explanation → no mutation surface; staged gates refuse
  with `ScopeAuthorizationError`.
* `$hot` expansion: cross-tier requires new admission (never auto-promoted); drift
  aborts `ABORTED_OCC` with zero partial writes.

## Appendix — verification work

* `go test ./internal/architecture/... ./internal/parser/... ./internal/domain/command/...
  ./internal/execution/...` — all `ok` (arch 3.5s incl. 98 pass phase locks).
* `go test ./internal/ui/... ./internal/modes/... ./internal/runtime/...
  ./internal/autonomy/... ./internal/core/...` — all `ok`, zero failures observed.
* `go list ./...` ≈ 180+ packages; `go list -deps ./cmd/izen` reaches all target trees
  EXCEPT `controlplane/{failure,guard,risk}`, `core/domain/{artifact,context,execution,
  workflow}`, `engine/executor`, `execution/scheduler`, `modes/undo`, `runtime/{engine,
  scheduler}` (dead-from-entry candidates; controlplane additionally has no production
  importers). `internal/command` alone imports only `internal/review` + `runtime/output`.
* Pre-existing failures: NONE observed in the suites above. (`go test ./...` full-tree
  not run; suites scoped per Phase 0 instructions.)
* Read-only audit tests added: NONE (existing lock suites — `phase0_authority_lock_test.go`
  9 tests, `authority_invariants_test.go`, `slash_router_lock_test.go` — already pin the
  relevant invariants; no new instrumentation was required to prove findings).
