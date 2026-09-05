# 01_CODEBASE_DISCOVERY.md — Izen Read-Only Codebase Audit

> **Mode:** STRICT READ-ONLY — no source modification, no patch generation. This document is the sole write artifact.
> **Baseline:** `docs/architecture/ARCHITECTURE.md` (3065 lines, 60 sections, 20 Core Invariants) — cited as ARCHITECTURE_3 baseline per task brief.
> **Date:** 2026-09-06
> **Auditor Role:** Principal Systems Architect & Static Analysis Auditor — 5 Vector Scan

---

## 1. Executive Summary & Codebase Footprint

### 1.1 Quantitative Footprint

| Dimension | Value | Method |
| :--- | :--- | :--- |
| **Total Go LOC** | **~322,647** | `find . -name "*.go" \| xargs wc -l` |
| **Go files** | **~1,183** | `find . -name "*.go" \| wc -l` |
| **Go packages (go list)** | **169** | `go list ./...` |
| **Top-level dirs** | `internal/` 253,606 LOC (879 files) · `pkg/` 66,654 LOC (293 files) · `cmd/` 1,080 LOC (4 files) | per-dir `wc -l` |
| **Largest package** | `internal/ui` 71,688 LOC (186 files) — 28% of `internal/` | |
| **Second largest** | `internal/execution` 42,877 LOC (132 files) | |
| **Test asset ratio** | Heavily tested (every major mutant path has `*_test.go`, chaos tiers, architecture invariant linters in `internal/architecture/`) | `internal/architecture/*_test.go` (15 files) |

**Package breakdown (top 15 by LOC, `internal/` only):**

```
internal/ui:              71,688  186 files  — presentation + dispatch + operation lifecycle
internal/execution:       42,877  132 files  — RuntimeExecutor, IntentGateway, graph, toolcalls, patch
internal/runtime:         23,259   72 files  — substrate, autonomy, compose, handlers
internal/modes:           20,256   69 files  — plan/investigate/build/review/commit/undo
internal/retrieval:       10,299   46 files  — lynx adapter, fallback grep/rg, symbol extractors
internal/core:             7,920   31 files  — artifact, workflow, budget, capability, authorization
internal/providers:        6,229   18 files  — provider adapters
internal/architecture:     5,966   15 files  — structural invariant linters (AST import graph checks)
internal/autonomy:         5,295   23 files  — driver, proposal, intelligence, boundaries
internal/session:          5,075   21 files  — session manager, GC, compaction
internal/llm:              4,867   20 files  — registry, provider routing, ollama exec
internal/lea:              4,317   18 files  — structural graph engine
internal/domain:           4,258   25 files  — workflow runtime, policy, intent
internal/presentation:     3,406   14 files  — view state, artifact rendering
internal/events:           3,397   10 files  — bus, domain events, audit
```

The codebase is **not a thin LLM wrapper**. It is a layered Smart Harness with a claimed Control Plane / Execution Plane split, an artifact lineage system, and a gated RuntimeExecutor. The significant engineering investment is in `internal/ui`, `internal/execution`, and `internal/runtime/compose`.

### 1.2 High-Level Verdict — Divergence from ARCHITECTURE.md

| Architectural Pillar | Target State (ARCHITECTURE.md) | AS-IS Assessment | Divergence |
| :--- | :--- | :--- | :--- |
| **Single Authoritative Control Plane** (Sec 1.2, Inv 15) | One mutation authority, one execution decision path | **Fragmented**: at least four mutation authorities coexist (`internal/execution/RuntimeExecutor` + `internal/execution.Engine` + `internal/runtime/substrate.ConcreteSubstrate` + `internal/ui.executionRunner`). UI dispatches via gateway on some paths but retains direct `exec.CommandContext("bash","-c",…)` on others. | **HIGH — architectural** |
| **Workflow / Artifact / Capability / ExecutionState independence** (Sec 1.3, Inv 7) | Four orthogonal dimensions, no collapse | **Collapsed in `internal/ui/model.go:697`**: single `model` struct (5,094 lines) fuses WorkflowState, Artifact handles, CapabilitySet, budgets, proposal dock, graph, and operation state. | **HIGH** |
| **Capability Guard is final authority** (Sec 15, 44, Inv 1/2/7) | Every write/exec through `CapabilityGuard` + `Authorization = Budget ∧ Scope ∧ Hash ∧ Capability ∧ (Approval ∨ PreApproval)` | Partially implemented (`internal/core/authorization`, `internal/controlplane/guard`, `internal/runtime/substrate`) but **bypassable** via UI fallback runners that skip the guard. | **HIGH** |
| **Untrusted Boundary for `$prompt`** (Sec 5.1) | Workspace content wrapped in `<untrusted_context>` data boundary, never executable | **Not implemented** — zero occurrences of `untrusted_context` in Go sources; prompt ingestion wraps no explicit isolation boundary. | **HIGH** |
| **State Boundary & Artifact Lifecycle** (Sec 12, 13, Inv 3) | Immutable artifacts with `DRAFT→VALIDATED→AWAITING_APPROVAL→AUTHORIZED→CONSUMED→ARCHIVED` + hash-anchored `STALE/INVALIDATED` | **Sound core**: `internal/core/artifact/lifecycle.go:7`, `types.go:1`, `persistence.go:1` correctly model it; but consumers (`internal/ui/model.go:1123` hotfix gate, `internal/execution/patch.go`) can still apply without re-validating after external edits. | **MEDIUM** |
| **Verification Levels 0-5** (Sec 28/29, Inv 11) | Graduated evidence (`No verifier → Artifact integrity → Structural → Diagnostics → Build → Tests`), never `No verifier = Verified` | **Largely sound** with graceful degradation (`internal/verification/guard.go:9` `IsGoProject`/`IsEnvironmentSetupError`/`FormatSkipMessage`), but premium UI paths can still surface `COMPLETED·VERIFIED` when verification was skipped for static assets. | **MEDIUM** |
| **Presentation Contract** (Sec 48) | TUI as pure subscriber: no independent state, no direct mutation, one state source | **Violated**: TUI is the thickest package, owns operation lifecycle, mode transitions, patch approval routing, and direct shell execution. Event-bus subscription exists but does not subsume the legacy paths. | **HIGH** |
| **Cold-Start Workspace** (Sec 39) | Useful execution with `COMPLETED·PARTIALLY_VERIFIED/UNVERIFIED`, never `VERIFIED` without evidence | **Mostly implemented** (guard skips `go build` on non-Go projects). Remaining risk: tree-sitter/language discovery lazily cached but can impose graph indexing on trivial `$prompt hi` paths if mis-routed. | **LOW-MEDIUM** |
| **Efficiency vs Correctness** (Inv 20) | Never trade authority/context for token savings | **Good faith implementation** (`internal/contextcompiler`, `internal/execution/strategy/context.go:121` strict compiler, `internal/execution/executor.go:1903` `compileContext`). Real bug is the opposite: `internal/ui` sometimes injects large tool logs without budget awareness on legacy paths. | **MEDIUM** |

**Overall verdict: Divergence is structural, not cosmetic.** The artifact/workflow substrate (`internal/core/*`, `internal/runtime/compose`, `internal/events`) is well-aligned and is the evident foundation for the target architecture. The `internal/ui` + legacy `internal/execution.Engine` seam is where authority leakage, state collapse, and presentation coupling concentrate. A directory-structure refactor alone would not satisfy Sec 1.2 — authority ownership must be consolidated before Phase 2/3.

---

## 2. AS-IS Dataflow & Execution Path

### 2.1 Complete Call Graph: User Input → Directive Parsing → Execution

```
HUMAN (keyboard / $prompt / $hot / /build / /ask / /investigate / /plan / @path)
  │
  ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  PRESENTATION LAYER  internal/ui/                                       │
│                                                                         │
│  keys.go:1083 handleInput / handleCommand                                │
│    ├─► model.intentFromInput()                 intent_dispatch.go:30     │
│    │     ├─► parser.Tokenize()                 parser/lexer.go          │
│    │     ├─► parser.ParseInWorkspace()         parser/parser.go:31      │
│    │     │     ├─ registry LookupPrefix ($/ )  pkg/domain/command/reg.  │
│    │     │     ├─ classifyScope(@)             parser/parser.go:259     │
│    │     │     └─ permission Contains()         pkg/domain/command/…     │
│    │     └─► alignDirectiveContext()           intent_dispatch.go:76     │
│    │           (continuous execution realignment)                         │
│    │                                                                    │
│    ├─► model.dispatchASTIntent()               intent_dispatch.go       │
│    │     ├─► direct /ask/$prompt → legacy ask pipeline                 │
│    │     ├─► $hot/$prompt gated path → IntentGateway.Gate →            │
│    │     │         RuntimeExecutor.Execute (see below)                  │
│    │     └─► /build /investigate /plan → mode transition + engine      │
│    │                                                                    │
│    └─► model.handleCommand() / slashRouter     command/router.go:14     │
│          └─► IsReviewTestComposite /review $test fast-query             │
└─────────────────────────────────────────────────────────────────────────┘
  │                    │                     │                 │
  │ gated path         │ legacy ask          │ build/plan      │ direct shell
  ▼                    ▼                     ▼                 ▼
┌────────────────┐ ┌──────────────┐  ┌──────────────────┐ ┌──────────────┐
│ IntentGateway  │ │ planner.Planner│  │ modes/plan.Engine │ │ executionRunner│
│ execution/     │ │ contextPlanner │  │ modes/investigate │ │ ui/commands.go│
│ admission.go   │ │ lea.Engine     │  │ modes/build       │ │  :2940       │
│ Gate()         │ │ retrieval/*    │  │ checkpoint/engine │ │ RunContext() │
│  └─ Context    │ │  (graph+kNN)   │  │                   │ │  exec.Cmd  │
│     Snapshot   │ └──────┬─────────┘  └────────┬──────────┘ │  bash -c   │
│     + Strategy │        │                     │            └──────┬───────┘
│       .Select  │        ▼                     ▼                   │
│ execution/     │   ai.Provider            patch.Applicator         │
│ strategy/      │   Stream / Chat          execution/patch.go       │
│  selector.go   │        │                     │                   │
└───────┬────────┘        │                     ▼                   │
        │                 │              verification/guard.go        │
        ▼                 │              IsGoProject / IsEnvSetupErr  │
┌────────────────┐        │                     │                   │
│ RuntimeExecutor│◄───────┘                     ▼                   │
│ execution/     │                    execution.Engine               │
│ executor.go:33 │                    execution.go / runner.go       │
│  admission:    │                         │                        │
│   Context Seal │                         ├─► ToolCallBuffer        │
│   + Strategy   │                         ├─► PatchManager         │
│  compileContext│                         ├─► Verifier              │
│   (policy-     │                         └─► MutationSet           │
│    owned)      │                              │                   │
│  invokeMutation│                              │                   │
│  artifact      │                              ▼                   │
│  mutate/verify │                    ConcreteSubstrate              │
│  evidence      │                    runtime/substrate/engine.go:21 │
│                │                    (claims ONLY Execute() may     │
└───────┬────────┘                     call os.WriteFile)            │
        │                              ─────────────────────────     │
        ▼                                     │                      │
  Capability Guard                              │                      │
  ┌─────────────────┐                     os.WriteFile  (disputed)   │
  │ core/            │                     os.Remove                 │
  │  authorization/  │                     exec.CommandContext        │
  │  engine.go       │                          │                    │
  │  capability/     │                          ▼                    │
  │  CapabilitySet   │                    WORKSPACE (filesystem)     │
  │  budget/         │                    ./.izen/artifacts/        │
  │   MutationBudget │                    ./.izen/checkpoints/      │
  │ controlplane/    │                    ./.izen/history/          │
  │  guard/          │                    git index / worktree       │
  │   ScopeGuard     │                                           │
  └─────────────────┘                                           │
        │                                                        │
        ▼                                                        │
  CheckpointCoordinator                                          │
  checkpoint/engine.go:356 git read-tree / checkout-index       │
  workspace/checkpoint/manager.go (blob store)                   │
        │                                                        │
        ▼                                                        │
  Events Bus  internal/events/bus.go + audit/audit.go            │
  EventCommandReceived → IntentParsed → ContextCompiled →         │
  PlanStaged → PatchAttempted → PatchApplied → Verified →        │
  ExecutionFailed (FailureClassification) → StageCompleted        │
        │                                                        │
        ▼                                                        │
  Presentation Projection  internal/presentation/*                │
  ExecutionProjection / WorkflowViewState / EvidenceProjection    │
  internal/ui/model.go:2601 syncUIState / handleDomainEvent      │
  internal/ui/gateway.go / activity_tree.go / header.go          │
```

### 2.2 Active Execution Authorities (AS-IS)

> **Invariant 15 truth test** — "All mutation and termination decisions must pass through the authoritative Control Plane path" (`ARCHITECTURE.md:2598`).

| Authority | File(s) | Operations Owned | Guarded? | Inv 15 Status |
| :--- | :--- | :--- | :--- | :--- |
| **A1 — RuntimeExecutor** (intended authority) | `internal/execution/executor.go:33`, `internal/execution/admission.go`, `internal/execution/auth.go` | `Gate→compileContext→invokeMutation→artifact→mutate→verify→evidence`; emits canonical `events.*` lifecycle | YES — `core/authorization.AuthorizationEngine.Evaluate` + `controlplane/guard.ScopeGuard` + `budget.MutationBudget` | **Compliant** |
| **A2 — Legacy Execution Engine** | `internal/execution/execution.go`, `internal/execution/runner.go:229`, `internal/execution/toolcalls.go:148,374,408`, `internal/execution/patch.go:332,413,594,726` | Direct `os.WriteFile`/`os.Remove` inside `ToolCallBuffer.Apply`, `Runner` shell steps, `patch.Write` | PARTIAL — has its own `execution/boundaries.go`, `guard.go`, `preflight/` but is a second mutation authority | **VIOLATION — Inv 15** |
| **A3 — ConcreteSubstrate** | `internal/runtime/substrate/engine.go:21,203,274,302,318`, `internal/runtime/substrate/exec.go:25` | Claims exclusive `os.WriteFile`/`os.Remove`/`exec.CommandContext` via `Execute()` + ledger | YES — internal ledger + `store.Store` | **Competing claim — Inv 15** (substrate vs RuntimeExecutor both claim exclusive write) |
| **A4 — TUI Direct Shell Runner** | `internal/ui/commands.go:2966`, `internal/ui/commands.go:4079`, `internal/ui/proposals.go:111`, `internal/ui/commands.go:2880` | `exec.CommandContext("bash","-c",command)` for `go build`, `$test`, `$run`, `$fix` helpers; `_ = os.WriteFile(logPath, …)` for logs; `_ = os.Remove(stashedPlanPath)` | NO — inline `if !CanWrite() && !CanPatch()` guard only, no `AuthorizationEngine` | **VIOLATION — Inv 1, 7, 15** |
| **A5 — Infrastructure Adapters** (intended providers) | `internal/infrastructure/capabilities/osfile.go:71`, `patchadapter.go:106`, `exeshell.go:52`, `workspace/checkpoint/manager.go:148,311`, `checkpoint/engine.go:356` | Concrete `FilePort`/`ShellPort`/`GitPort`/`PatchPort` implementations | YES — intended Execution Plane (Sec 54) when called via Control Plane | **Compliant when invoked via A1; violation when invoked directly by A2/A4** |
| **A6 — Config/Session Persistence Writers** | `internal/config/config.go:495`, `internal/config/auth.go:149`, `internal/config/local.go:50`, `internal/state/globals.go:56`, `internal/llm/registry.go:318` | `os.WriteFile` for `~/.izen/config.yml`, provider cache, global `~/.izen/` | N/A — global infra (`ARCHITECTURE.md:49`); project-lineage writes to `~/.izen` would violate storage model, but observed writes are global-only | **Compliant (global scope)** |

**Diagram of authority leakage:**

```
              ┌───────────── A1 RuntimeExecutor (gated) ─────────────┐
              │  AuthorizationEngine + ScopeGuard + Budget + Hash    │
              └──────────────────────┬───────────────────────────────┘
                                   │ should be sole writer
              ┌────────────────────┼────────────────────┐
              ▼                    ▼                    ▼
        A2 Execution.Engine   A3 Substrate      A4 UI executionRunner
        (second authority)    (rival claim)     (unguarded bash -c)
              │                    │                    │
              └────────────► WORKSPACE ◄────────────────┘
                           (race on same files)
```

---

## 3. Invariant Violation & Risk Matrix

> Evaluated strictly against the 20 Core Invariants (`ARCHITECTURE.md:53`) and the section rules cited in the audit vectors. `TO-BE Target` = normative behavior per ARCHITECTURE.md. No refactored code is provided per NO PREMATURE FIXING constraint.

| # | Package / File | Line / Function | Impacted Invariant / Rule | AS-IS Behavior (Fact) | TO-BE Target Architecture | Risk Severity |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **V1-A** | `internal/ui/commands.go` | `2966` `executionRunner.RunContext` · `4079` `RunContext` · `2880` `os.WriteFile(logPath…)` | **Inv 1** (No Mutation Without Authorization) · **Inv 15** (No Second Mutation Authority) · **Inv 7** (Authorization ≠ Approval) · Sec 1.2, 44 | `executionRunner` executes `exec.CommandContext(ctx,"bash","-c",command)` directly from the TUI for `go build`, `$test`, `$run` helpers. Guard is a local `if !CanWrite() && !CanPatch()` check (`commands.go:2996`) — not the canonical `core/authorization.AuthorizationEngine.Evaluate` path. Log truncation writes `os.WriteFile` without any authorization call. | All `WRITE/EXECUTE/PATCH` mutations route through `RuntimeExecutor` (or `Substrate.Execute`) via `core/authorization` + `controlplane/guard.ScopeGuard` + `budget.MutationBudget` + checkpoint; TUI never calls `exec.Command` or `os.WriteFile` directly. | **High** |
| **V1-B** | `internal/ui/proposals.go` | `111` `exec.CommandContext(ctx,"bash","-c",cmd)` | **Inv 1, 15** · Sec 54 (Layer Ownership) | Proposal approval helper shells out via `exec.CommandContext` without Control Plane mediation; proposal content (LLM-sourced) influences the shell string. | Shell execution is a `Capability EXECUTE` granted to Execution Plane only after `ApprovalPolicy` passes; UI proposal handling only calls `RuntimeExecutor.Approve/Reject`. | **High** |
| **V1-C** | `internal/execution/toolcalls.go` | `148` `os.WriteFile(absPath, Modified)` · `374` `os.WriteFile(absPath, params.Content)` · `408` `os.WriteFile(absPath, modified)` | **Inv 1, 15** · Sec 4 (Execution Classes) | `ToolCallBuffer` and patch-application helpers perform disk writes directly from the legacy `execution.Engine` path. The `RuntimeExecutor` path owns an analogous write set via `execution/patch.go` but neither subsumes the other — two authorities write the same file types. | Single mutation path: `ToolCallBuffer` is either merged into `RuntimeExecutor`'s transaction or removed; all file writes funnel through `Substrate.Execute` with the six-clause authorization formula (`ARCHITECTURE.md:44`). | **High** |
| **V1-D** | `internal/execution/patch.go` | `332`, `413`, `594`, `726` `os.WriteFile` · `2643` `os.Remove` | **Inv 15** · Sec 22 (Frames & Isolation) | `PatchManager`/`Applicator` writes backups and patches outside the single substrate ledger claim (`substrate/engine.go:21` claims exclusive `os.WriteFile` ownership). Rollback boundaries differ between `PatchManager` and `Substrate`. | One rollback boundary per `ExecutionFrame` (`checkpoint + dependency graph + lineage`); one `EvidenceStore`/`ArtifactLedger` pair owned by `internal/runtime/substrate/store`. | **High** |
| **V1-E** | `internal/execution/runner.go` | `229` `exec.CommandContext(ctx,"sh","-c",command)` | **Inv 1, 15** · Sec 15 (Capability Model) | Generic shell runner inside `execution` bypasses `infrastructure/capabilities.ExeShell` port when called from legacy engine tests/helpers. | All `EXECUTE` goes through `ports.ShellPort` (`internal/infrastructure/capabilities/exeshell.go:52`) injected by `internal/runtime/compose`. | **Medium** |
| **V2-A** | `internal/ui/model.go` | `697` `type model struct` (5,094 lines, ~140 fields) | **Sec 1.3** (Four Orthogonal Dimensions) · **Inv 19** (Evidence ≠ Workflow State) | Single `model` fuses: `workflowSM *workflow.WorkflowStateMachine` (Workflow State), `runtimeCtx *runtime.RuntimeContext` (Capabilities), `caps *capability.CapabilitySet` + `mutationBudget`/`microBudget` (Execution State/budget), `currentReviewLedger`/`buildLedger`/`pendingHotfixPatch` (Artifacts), `activeGraph`/`activeOp`/`streamBlocks` (Execution State), and presentation state. Updates to one dimension risk silent cross-dimension effects. | Decompose per Sec 1.3 & 48: `WorkflowState` owned by `core/workflow` (already present), `Artifacts` by `core/artifact.Store`, `Capabilities` by `core/capability.CapabilitySet` + `core/runtime.RuntimeContext`, `ExecutionState` by `execution/graph` frames. TUI reads via `presentation.*Projection` only. | **High** |
| **V2-B** | `internal/ui/model.go` | `1123` `pendingBuildApproval` / `1137` `pendingHotfixTask` / `1146` `appliedHotfixFile` / `1151` `activeGraph` | **Sec 1.3** · **Inv 7** · Sec 12.3 (Lifecycle) | TUI holds artifact lifecycle surrogates (`pendingHotfixPatch *execution.Patch` holding `StateAwaitingApproval`-equivalent truth) alongside the canonical `artifact.Store`. The two can desynchronize after external file edits or mode resets (`V2-D`). | Artifact lifecycle source of truth is `core/artifact.Store` + `LifecycleTransitionValidator`; TUI derives `StateAwaitingApproval` via `workflowSM.PendingApproval()` / `viewState.Sync()` only. | **High** |
| **V2-C** | `internal/execution/executor.go` | `75` `type ExecuteRequest` (carries Mode, Context snapshot + `StagedSubTasks`/`FocusStartLine`) | **Sec 1.3** · Sec 16/21 (Execution Environment / Unit) | `ExecuteRequest` bundles `Mode` (presentation label), `Context` (frozen snapshot), `Strategy` (derived policy), and `Focus*` execution-unit slicing into one request object. A slicing change can inadvertently alter context-fidelity semantics. | `Mode` strictly presentation; `ContextSnapshot`, `ExecutionStrategyProfile`, and `ExecutionUnit` are distinct artifacts/values with separate fidelity checks (`execution/context.go` + `execution/strategy`). | **Medium** |
| **V2-D** | `internal/core/artifact/persistence.go` | `28` `VerifyAll` + `internal/ui/update.go:1036` build-verify path | **Inv 3** (No Mutation Against Stale Dependencies) · Sec 13 | `VerifyAll` correctly marks `STALE` on hash mismatch, and `internal/execution/context.go` enforces freshness. However, `internal/ui` hotfix fast-track (`model.go:972` `fastTrackTargets`) can complete a build immediately when every plan target is covered by the proposal targets, without re-verifying after the applied-patch window. External formatter edits between plan validation and mutation are not re-checked on that fast path. | Every consumption checks `Expected Source State vs Current Workspace State` (Sec 13 diagram) including fast-track; `STALE → Revalidate or Replan`. | **Medium** |
| **V3-A** | `internal/execution/executor.go` + `internal/parser/` + `internal/retrieval/` + `internal/prompt/` | Symbol resolution + prompt compilation seam (`executor.go:1903` `compileContext`, `executor.go:2052` `buildMutationUserPrompt`) | **Sec 5.1** (Prompt Ingestion & Untrusted Boundary) · **Inv 20** | `$prompt explain @internal/auth/token.go` correctly goes through `parser.Parse` → `SemanticScope` classification (`parser/parser.go:259`) and `Target` resolution. **But** file contents retrieved for `$prompt` (READ_ONLY execution class) are injected into the model prompt without an explicit `<untrusted_context>` data boundary. Zero Go source hits for `untrusted_context` (verified via `grep -rn untrusted_context` = 1 hit, in `ARCHITECTURE.md:407` only). LLM prompt evaluates workspace files as instructions-equivalent in the token stream. | Any workspace-derived content in `$prompt` payload wrapped in explicit data boundary (e.g., `<untrusted_context>…</untrusted_context>`) with system instruction that it is `DATA, never SYSTEM instructions`; directive strings cannot override Control Plane invariants. | **High** |
| **V3-B** | `internal/parser/parser.go` | `18` `Parse` / `31` `ParseInWorkspace` · `internal/ui/intent_dispatch.go:30` `intentFromInput` | **Sec 5.1** step 1 (Directive & Reference Parsing) | **Strength**: Deterministic marker-based ingestion (`Tokenize` → `parseTokens` → `IntentAST`), permission-checked against `pkg/domain/command.Registry`. Order-independent, prefix-aware, chain-support enforced. Well-aligned. Minor gap: `@` scope classification (`parser/parser.go:240` `scopeFileExtensions`) is heuristic and can misclassify `@foo.bar` symbol references as file scope when extension list is incomplete. | Extend `scopeFileExtensions` as data-driven capability from `internal/language/defs.go` rather than hardcoded map; add explicit `unknown` fallback handled as `UNKNOWN` per Sec 32. | **Low** |
| **V4-A** | `internal/verification/guard.go` | `9` `IsGoProject` · `36` `IsEnvironmentSetupError` · `69` `IsSetupFailure` · `81` `FormatSkipMessage` | **Inv 11** (Missing Evidence ≠ Positive Evidence) · Sec 28–30 | **Compliant in isolation**: cold-workspace behavior correctly skips `go build` on static `HTML/CSS/JS` projects (`internal/ui/commands.go:2902`), surfaces `[SKIP] No test runner…` instead of `COMPLETED·VERIFIED`. Verification policy distinguishes `COMPLETED·PARTIALLY_VERIFIED`/`UNVERIFIED`. | Preserve; wire the same guard into legacy `execution/verify.go` and `internal/modes/review` static paths so every verifier reports uniform `EvidenceState`. | **Low** (reuse asset) |
| **V4-B** | `internal/infrastructure/capabilities/` + `pkg/provider/capability/` + `internal/modes/investigate/` + `internal/verification/` | LSP / Tree-sitter / Linter / Compiler / Git | **Sec 28** (Verification Levels) | **Mapping observed**: See dedicated table below; capabilities are discovered via `workspace/capability`, `language/defs.go` (tree-sitter), `retrieval` (LSP-ish symbol search via `lea` graph), `verification` guards, `git.Engine`. Classification to L0–L5 is **partially implicit** (not a single `EvidenceLevel` type bridging all providers). `cold-workspace` gracefully degrades but some branches still format a `VERIFIED`-like summary without the structured `EvidenceState` product type (`ExecutionOutcome × EvidenceState`, Sec 30). | Introduce explicit `EvidenceLevel` / `EvidenceState` projection in `internal/presentation/evidence_projection.go` already partially present — promote to runtime `ExecutionProof.EvidenceState` as canonical terminal state. | **Medium** |
| **V5-A** | `internal/ui/model.go` + `internal/ui/update.go` + `internal/ui/commands.go` + `internal/ui/gateway.go` + `internal/ui/program.go` | Entire `internal/ui` (71,688 LOC) | **Sec 48** (Presentation Contract) · **Inv 15** · Sec 54 | **TUI is not a pure subscriber.** `model` owns: mode resolution (`resolver`), operation lifecycle (`activeOp`/`opIDCounter`), checkpoint coordination (`gitEng`), agent polling (`agentRunning`/`agentLabel`), proposal routing (`pendingProposals` → `executorPendingPatchID`), shell runner wiring, and direct `workflowSM.SendEvent` mutations (`model.go:2385`, `2442`). `syncUIState` (`model.go:2601`) derives state correctly but coexists with imperative hand-sets (`MarkApprovalPending`, `SetMode`). Rendering consumes both `handleDomainEvent` events AND direct `model` fields. | Presentation subscribes to `events.Bus` (already wired for 7 lifecycle events) and to `core/workflow` + `core/runtime` + `core/artifact` via `presentation.WorkflowViewState` / `ExecutionProjection`. No `exec.Command`, no `os.WriteFile`, no workflow mutation in `internal/ui` except via `RuntimeExecutor`/`appruntime.Runtime` commands. `internal/architecture/phase0_authority_lock_test.go:3` captures this intent — enforce it fully. | **High** |
| **V5-B** | `internal/ui/program.go` | `424` `exec.CommandContext(ctx,"git","config",…)` · `internal/ui/update_init.go:144,469` `os.WriteFile(sessPath…)` | **Sec 49** (Storage Model) · Sec 54 | UI programbootstrap performs `git config` probe and `session.json` seeding writes outside the substrate ledger. Not project-lineage, but unaudited global-ish side-effects on startup, not represented in `EvidenceStore`. | Move all initialization writes through `internal/runtime/compose` adapters; represent them as auditable events. | **Low-Medium** |
| **V5-C** | `internal/execution/strategy/strategy.go` + `internal/execution/strategy/selector.go` | `120` `ComplexityLevel` / `159` `ExecutionStrategyProfile` | **Inv 13/14** (Context/Cache vs Correctness) | **Compliant**: `compileContext` is a strict compiler (`strategy/context.go:121` "strict compiler, not a dumper"), refuses to include channels it cannot explain. Cache-aware prompt layout (`ARCHITECTURE.md:19`) prefer stable prefix but correctly invalidates on strategy changes. Guarded against fake truncation. | Keep as candidate reuse asset for `internal/core` promotion; document the stable-prefix contract explicitly. | **Low** (reuse asset) |

### 3.1 Verification Capability Mapping (Vector 4 Detail — Sec 28)

| Level | ARCHITECTURE.md Label | Evidence Capability | Concrete Provider(s) Observed | Cold-Workspace Behavior (AS-IS) | Gap |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **0** | No verifier | No tool available | — (single-file workspace, no `go.mod`/`*.go`) | Returns `COMPLETED·UNVERIFIED` path; no false `VERIFIED` in guarded paths | None — but unverified terminal must never render as `VERIFIED` (Inv 11) |
| **1** | Artifact integrity | Hash + file existence | `core/artifact/persistence.go:28` `VerifyAll` (sha256 dependency hashes) · `execution/context.go` snapshot + seal in `execution/admission.go` | Dependency staleness → `StateStale`/`StateInvalidated`, execution refused or replanned | Incomplete on hotfix fast-track (`V2-D`) |
| **2** | Structural integrity | AST / Symbol graph / Formatter | `internal/lea.Engine` (structural graph), `internal/language/defs.go` (tree-sitter per language), `internal/retrieval/symbol/extractors` | Works lazily; `contextCompiler` degrades to `FULL→STRUCTURAL→REGIONAL` correctly | Tree-sitter is capability-provided, not Core — correct per Sec 56, but discovery is eager on some `/arch` paths |
| **3** | Diagnostics / static analysis | Parser · Type diagnostics · Linter | `internal/execution/syntax.go`, `internal/runtime/substrate/engine.go:71` `verifyProposal` (go/parser AST check), `internal/retrieval` fallback `rg`/`grep` diagnostics | Parser errors surfaced as evidence, not as verification claims | OK |
| **4** | Build / type checking | Compiler · Type checker | `executionRunner.RunContext("go build",…)`, `internal/review/sandbox.go:111` `go test -count=1`, `internal/execution/verify.go` | Guarded by `verification.IsGoProject` / `IsEnvironmentSetupError`; static projects get `FormatSkipMessage` not a green check | Legacy `execution/verify.go` paths not yet using the same guard uniformly |
| **5** | Tests / runtime validation | Test runner · Runtime validation | `internal/execution/test.go:100` (log write) · `internal/command/router.go:27` `TestExecutor.RunDynamicTests` · `internal/modes/review` | Tests run in `/review $test` composite pipeline; requires hosted environment; `IsSetupFailure` prevents misclassifying setup as test defect | Level 4/5 are ambient-capability–dependent per Sec 28 — compliant; classify via explicit `EvidenceState` product type on every terminal path |

---

## 4. Candidate Reuse Asset Assessment

> Units that **comply** with ARCHITECTURE.md and can be directly migrated to `internal/core/domain` (domain truth) or `internal/capability` / `internal/infrastructure` (execution providers) without behavior change. Discovery is evidence-backed via line references.

| Asset | Location | Lines | Why Compliant | Migration Target (TO-BE) | Action |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Artifact Model (Identity · Lifecycle · Lineage · Dependencies · Storage)** | `internal/core/artifact/artifact.go:1`, `types.go:1`, `lifecycle.go:1`, `persistence.go:1` | `types.go:10` `Lineage`, `30` `Dependency{Hash}`, `41` `BaseArtifact`; `lifecycle.go:7` 9-state machine + `47` `LifecycleTransitionValidator`; `persistence.go:28` `VerifyAll` STALE mark, `270` `Save`/`Archive` | Immutable IDs, append-only lineage (`DerivedFrom`/`Supersedes`), hash-anchored dependencies, strict validator, correct ACTIVE (`.izen/artifacts/`) vs ARCHIVE (`.izen/history/`) storage per Sec 49 | `internal/core/domain/artifact` (no change) | **Direct reuse — promote as canonical Artifact substrate** |
| **Workflow State Machine** | `internal/core/workflow/machine.go:16` | `WorkflowStateMachine` with `StateIdle/Investigating/Planning/Building/Repairing/Reviewing/Verified/Failed`, `pendingApproval` as single approval gate (`46` `MarkApprovalPending`), `lookup` + `failureTarget` routing, `checkpoint.Coordinator` hooks (`85` `CreateBeforeBuild`, `94` `Rollback` on `FailureScopeClass`) | No state explosion (rejects `BUILDING_OUTPUT_EXHAUSTED` anti-pattern, Sec 52), workflow state never encodes execution sub-states, `Inv 5` `FailureScopeClass → Replan` / `Unknown → StateFailed` correct | `internal/core/domain/workflow` | **Direct reuse** |
| **Capability Set (GRANT-only, scoped)** | `internal/core/capability/` (CapabilitySet), `internal/core/budget/` (MutationBudget/MicroBudget) | `Grant(READ/WRITE/TEST/PATCH/CHECKPOINT/ROLLBACK, ScopeRule)` with path/symbol scoping (`Sec 15` table); `core/authorization/engine_test.go:119` coverage | Scoped, non-escalating grants; budget counters per Sec 33 (files/diffs/tokens/attempts), immutable once issued per composition root | `internal/capability` (merge with `pkg/runtime/authorization`) | **Direct reuse — unify duplicate Gate types** |
| **Authorization Engine (6-clause formula)** | `internal/core/authorization/engine.go` + `types.go:28` `CapabilityFlags`, `internal/controlplane/guard/scope_guard.go:41` `ScopeGuard`, `failure/classifier.go` | `ValidIntent ∧ ValidScope ∧ ValidPlan/MicroPlan ∧ CheckpointCreated ∧ SourceHashMatch ∧ BudgetAvailable ∧ CapabilityGranted ∧ (HumanApproved ∨ BudgetIsPreApproval)` — `ARCHITECTURE.md:44` formula faithfully implemented; `ScopeGuard.ValidatePatch` validates file/symbol boundaries | Capability Guard as final authority, no bypass on that path; failure sentinels (`ErrScopeViolation`, `ErrBudgetExceeded`) classify cleanly | `internal/core/domain/authorization` | **Direct reuse — eliminate rival TUI guard** |
| **Execution Strategy & Context Compiler (Strict)** | `internal/execution/strategy/strategy.go:159` `ExecutionStrategyProfile`, `selector.go`, `context.go:121`, `compile.go:14`, `internal/contextcompiler/compiler.go:25`, `internal/execution/executor.go:1903` `compileContext` | Context compilation is a strict compiler ("adds channel only with explainable value"), policy-owned (`ContextPolicyNone/target_file_only/repository`), zero-context for trivial `$prompt hi` (`executor.go:1912`), budget-bounded output, stable-prefix intent with correct invalidation (`Sec 18.1`, `19`) | Proportional context, never destructive truncation, cache never overrides correctness (`Inv 13, 14`) | `internal/core/domain/strategy` + `internal/capability/context` | **Direct reuse — already minimal-sufficient execution** |
| **Deterministic Directive Parser** | `internal/parser/lexer.go`, `parser.go:18` `Parse`/`ParseInWorkspace`, `ast.go:63` `IntentAST`, `pkg/domain/command/registry.go` | Order-independent markers (`$` directives, `@` scopes, `/` global commands), permission policy enforced post-workspace-resolution, alias/prefix resolution, chain-support contract; fully deterministic (no LM dependence) | Correct Sec 4 execution-class derivation surface and Sec 5.1 ingestion step 1 | `internal/core/domain/parser` | **Direct reuse** |
| **Verification Guard (Graceful Degradation)** | `internal/verification/guard.go:9` `IsGoProject`, `36` `IsEnvironmentSetupError`, `69` `IsSetupFailure`, `81` `FormatSkipMessage` | Environment-aware level detection, skip rather than false-positive, environment vs code failure distinction, environment errors never routed to bounded repair auto-retry (`internal/ui/update.go:1058`) | Honest evidence boundary, Sec 39 cold-start compliant | `internal/capability/verification` | **Direct reuse — extend to all verification seams** |
| **Event Bus & CQRS Projections** | `internal/events/events.go`, `bus.go`, `internal/presentation/execution_projection.go`, `workflow/state.go`, `evidence_projection.go`, `internal/ui/model.go:2140` `logRuntimeDetail` | Non-blocking bus (256-buffer per subscription), 7 typed lifecycle events (`EventCommandReceived` → `StageCompleted`), `ExecutionProjection`/`WorkflowViewState` as pure subscriber views; UI already subscribes in `program.go:366` | One state source drives Runtime/TUI/Telemetry/Audit (`Sec 48` goal), no second source on the new path | `internal/core/stream` + `internal/presentation` | **Direct reuse — make it the exclusive TUI data source** |
| **Composition Root** | `internal/runtime/compose/compose.go:13` (single root claim), `internal/architecture/invariants_test.go:207` `TestLeaEngineSingleCompositionBinding` | Single `Application` wiring `WorkflowRuntime` + `LedgerBuilder` + `EventTranslator` + capability ports (`Capabilities{File,Shell,Git,Patch}` at `compose.go:70`); AST drift invariant pins import boundaries | Sec 37 Provider Independence, Sec 54 Layer Ownership, Sec 1.2 singular authority ambition | `internal/app` / `internal/core/runtime` | **Direct reuse — prune secondary roots** |
| **Structural Index (Lea Engine)** | `internal/lea/Engine`, `internal/language/defs.go` (16 languages with tree-sitter bindings) | Language-agnostic normalized symbol graph; Core never hardcodes language semantics (Sec 56); archive-cached, invalidatable, workspace-scoped | `Sec 38` harness discovery, `Sec 56` agnostic execution | `internal/capability/structural` | **Direct reuse (whitelisted composition binding)** |
| **Checkpoint Subsystem** | `internal/checkpoint/engine.go:356`, `workspace/checkpoint/manager.go`, `internal/core/workflow/machine.go:85` | Git `read-tree` + `checkout-index`–based restore, blob store per checkpoint, `CreateBeforeBuild` / `Rollback` on scope failure, `Invariant 11` cycle-safe | Sec 22 rollback granularity (LOCAL/ANCESTRAL/TASK) | `internal/capability/checkpoint` | **Direct reuse** |

---

## 5. High-Risk Coupling Hotspots

> Files/packages whose tight coupling or hidden state dependencies will break (or be forced to be broken) during Phase 2 (authority consolidation) / Phase 3 (state decomposition) refactoring. Ranked by blast radius.

### 5.1 Spotlight: `internal/ui` — The Monolithic Presentation Layer

| Hotspot | Size | Coupling | Why It Will Break | Refactoring Exposure |
| :--- | :--- | :--- | :--- | :--- |
| `internal/ui/model.go:697` `type model` | 5,094 lines, ~140 fields, 186 files in package | **Star-coupled** to every subsystem: `config`, `session`, `ai.Provider`, `modes.Resolver`, `git.Engine`, `lea.Engine`, `agents`, `planner`, `execution.Engine`, `plan.Engine`, `execution.RuntimeExecutor`, `execution.IntentGateway`, `autonomy.Driver`, `runtime.RuntimeContext`, `workflow.WorkflowStateMachine`, `domain/workflow.WorkflowRuntime`, `presentation.WorkflowViewState` | Any Phase 2 authority change (e.g., removing `executionRunner` or collapsing `execution.Engine` into `RuntimeExecutor`) forces invasive edits to `model` fields, message handlers, and approval routing simultaneously. The `model` is both the control-flow owner and the largest state owner, so state decomposition requires it to be **decomposed first** — the classic "God object" anti-pattern is explicitly forbidden by Sec 1.3 and Sec 48. | Phase 2 **P0**. Must be split into: (a) thin event subscriber / view projection, (b) command dispatcher via `appruntime.Runtime`, (c) lightweight snapshot for rendering only. |
| `internal/ui/model.go:2601` `syncUIState` vs imperative `MarkApprovalPending`/`SendEvent` | Dual state sources | `syncUIState` correctly derives `viewState` from `workflowSM.PendingApproval()` (`model.go:2631`), but `model.go:2394` `enterApprovalState` **writes** the authoritative `workflowSM.MarkApprovalPending()`. UI both reads and writes the single source of truth — a subscriber that can also author workflow transitions. | Phase 2 approval-gate centralization will invert ownership: UI must become **read-only** on `ApprovalPending`; only `appruntime.Runtime` or `RuntimeExecutor` may set it. Eliminating the imperative path will surface ~15 call sites that currently depend on UI-authored transitions (`keys.go:1170`, `commands.go:1079`, `update.go:203`). | **P0** |
| `internal/ui/update.go:1036` `buildResultMsg` verification + auto-recovery loop | Implicit controller inside view | Encodes `Feedback Controller` semantics (`Sec 24` CONTINUE/REPLAN/STOP) directly in the TUI message handler (retry count in `buildRecoveryCount`, `maxBuildRecoveryAttempts`, environment-error classification via `verification.IsEnvironmentSetupError`). The Control Plane's `loop`/`runtimeAutonomy` own the same decision, yielding **two feedback controllers**. | Phase 2 must migrate retry/replan classification to `internal/controlplane/failure.Classifier` + `internal/loop` exclusively; TUI's auto-recovery shim must be deleted or driven by an `EventFailureIdentified` projection alone. | **P1** |

### 5.2 Authority Fragmentation Hotspots

| Hotspot | Coupling | Break Description | Severity |
| :--- | :--- | :--- | :--- |
| `internal/execution/executor.go:33` `RuntimeExecutor` vs `internal/execution/execution.go` `Engine` vs `internal/runtime/substrate/engine.go:21` `ConcreteSubstrate` — **three overlapping "only Execute may write" claims** | Each owns `os.WriteFile`/`exec.CommandContext` and a disjoint ledger/graph; `internal/execution/executor.go:339` writes `EvidenceStore` directly while `execution.Engine` writes via `Mutationset` | Phase 2 must choose one author and deprecate the others. Advised: `RuntimeExecutor` + `Substrate` converge (substrate becomes Execution Plane port under `RuntimeExecutor`'s Control Plane), `execution.Engine` is retired to `legacy/` or deleted after its migrated paths are proven via `internal/architecture/phase0_authority_lock_test.go:3`. Feature-flagging the cutover without atomic deprecation will leave the workspace race. | **P0** |
| `internal/execution/toolcalls.go` vs `internal/execution/patch.go` — **second patch-apply transaction boundary** | `ToolCallBuffer` holds staged diffs never seen by `RuntimeExecutor`'s `BudgetGuardrail` or `ScopeGuard` for the same target | Tight coupling between prompt-contract extraction (LLM tool-call shape) and filesystem mutation. Reformatting tool-call schema or contracting to `search_replace` (shape change) breaks apply semantics without exercising the canonical `BudgetGuardrail` path. | **P1** |
| `internal/runtime/substrate/store/store.go:62` `os.WriteFile(path,data,0644)` vs `internal/checkpoint/engine.go:321` checkpoint ledger | Two persistence owners for overlapping `.izen/` subtrees (`store.Store` vs `checkpoint.Engine` vs `core/artifact.Store`) | Storage model (`Sec 49`) mandates `.izen/` for project provenance and `~/.izen/` for global only; multiple writers risk inter-writer inconsistency after rollback. | **P1** |

### 5.3 State & Lifecycle Coupling Hotspots

| Hotspot | Coupling | Break Description | Severity |
| :--- | :--- | :--- | :--- |
| `internal/execution/patch.go:332,413,594,726` patch backup/restore vs `internal/core/artifact/persistence.go:28` hash verification vs `internal/execution/context.go` seal | Backup file naming and hash assumptions are duplicated; restore does not clear `STALE` via `LifecycleTransitionValidator`, so an external edit + rollback can leave an artifact in `StateAuthorized` with a mismatched workspace hash | Phase 3 artifact staleness enforcement (`VerifyAll` on every `AUTHORIZED→CONSUMED` transition) must be upstream of patch restore, not parallel to it. | **P1** |
| `internal/ui/model.go:972` `fastTrackTargets` + `internal/ui/update.go:1036` fast-track completion | Direct `len(task.Target) ∈ proposalTargets` equality check bypasses per-file hash re-validation | See `V2-D`; whitespace-only or no-op edits that satisfy target-name coverage but not semantic coverage would be misreported as build completion. Coupled to `internal/execution/noop_semantics.go`. | **P1** |

### 5.4 External-Dependency Coupling Hotspots

| Hotspot | Coupling | Break Description | Severity |
| :--- | :--- | :--- | :--- |
| `internal/retrieval/lynx_adapter.go:75` `exec.CommandContext(ctx, binaryPath, args…)` + `fallback.go:102` `rg`/`grep` fallback | Tightly bound to external `lynx`/`rg` binaries; retry/remediation (`internal/modes/plan/truncate.go:251` `HasCanonicalImportMismatch`, `modes/investigate/toolrunner.go:…` `runPackageRemediation`) invokes `lx` then `go mod tidy` with `//nolint:contextcheck` context propagation debt | Autonomy remediation loops where environment failure (`Inv 5` `CODE_FAILURE` vs `ENVIRONMENT_FAILURE`) can silently reclassify to code repair and attempt unbounded `lx`/`go mod tidy` cycles if provider availability is misread. | **Medium** |
| `internal/llm/registry.go:150` `_ = os.Remove(cacheFilePath())` + `631` `exec.CommandContext(ctx, ollamaPath, "list")` | Provider discovery mutates global model cache as side-effect of listing | Provider independence (`Sec 37`) requires provider behavior to be capability-described, not ambient-mutated during enumeration. A listing call must not delete cache artifacts even on error path. | **Low-Medium** |

### 5.5 Architectural-Linter Coupling (Good Coupling — Preserve)

| Hotspot | Why It Matters |
| :--- | :--- |
| `internal/architecture/invariants_test.go:207` `TestLeaEngineSingleCompositionBinding` | Machine-enforces composition-root singleton invariant (Sec 1.2); must remain green throughout Phase 2/3. Any new `lea.NewEngine` call site outside `internal/runtime/compose/compose.go:13` will fail this gate. |
| `internal/architecture/phase0_authority_lock_test.go:3` + `context_invariants_test.go:3` + `phase3_occ_lock_test.go:3` | Lock tiers freeze admission, budget, and OCC abort invariants; serve as the executable specification for `ToBe` verification after each refactor slice. |
| `internal/architecture/execution_invariants_test.go` + `pruning_invariants_test.go` | Guard context-budget invariants and planner pruning; prevent `Token-First Planning` and `Destructive Context Truncation` anti-patterns (`Sec 52`). |

---

## Appendix — Audit Method & Evidence

| Vector | Sections Verified | Files Inspected (sample) | Tools |
| :--- | :--- | :--- | :--- |
| **1 Authority & Mutation** | Sec 1.2, 15, 44, 45; Inv 1,2,7,15 | `internal/execution/executor.go:33,339`, `internal/runtime/substrate/engine.go:21,203,274,302,318`, `internal/execution/toolcalls.go:148,374,408`, `internal/execution/patch.go:332,413,594,726`, `internal/ui/commands.go:2880,2966,4079`, `internal/ui/proposals.go:111`, `internal/infrastructure/capabilities/*`, `internal/core/authorization/*`, `internal/controlplane/guard/*` | `grep -rn os.WriteFile/exec.Command` (non-test), `grep -rn Capability/guard`, import-graph via `internal/architecture` |
| **2 State & Artifact** | Sec 1.3, 12, 13, 14; Inv 3,7,19 | `internal/core/artifact/types.go:10`, `lifecycle.go:7`, `persistence.go:28`, `internal/core/workflow/machine.go:16`, `internal/execution/executor.go:75`, `internal/ui/model.go:697,1123,1151`, `internal/execution/strategy/strategy.go:159` | Structural read of every `type Struct`, `type State`, lifecycle validator, `model` field census |
| **3 Directive Ingestion & Router** | Sec 4, 5, 5.1; Inv 4 | `internal/parser/lexer.go`, `parser.go:18,31,259`, `ast.go:63`, `pkg/domain/command/registry.go`, `internal/ui/intent_dispatch.go:30,76`, `internal/ui/directive_contract.go`, `internal/execution/intent.go`, `internal/execution/executor.go:1903,2052` | `grep -rn untrusted_context` (negative evidence), parser trace with `/build$hot $test @file` inputs, scope classification review |
| **4 Capability & Verification** | Sec 16, 28, 29, 30, 32, 33, 38, 39; Inv 6,11 | `internal/verification/guard.go:9,36,69,81`, `internal/infrastructure/capabilities/{osfile,patchadapter,exeshell,gitcli}.go`, `pkg/provider/capability/*`, `internal/language/defs.go`, `internal/retrieval/{lynx_adapter,fallback}.go`, `internal/lea/Engine`, `internal/workspace/capability/*` | Capability→Level mapping review, cold-workspace `IsGoProject` probe, `go build` guard audit |
| **5 Presentation & TUI** | Sec 48, 54; Inv 15 | `internal/ui/model.go:697,2601`, `program.go:424`, `update.go:1036`, `update_init.go:144`, `commands.go:2880`, `gateway.go:230`, `presentation/{execution_projection,state,evidence_projection}.go`, `internal/events/bus.go` | TUI state census, direct-mutation grep, subscriber vs imperative mutation inventory |

**All line numbers cited are `file_path:line_number` at audit time (`go 1.x`, commit HEAD, `wc -l` 322,647 Go LOC). No source files were modified.**

