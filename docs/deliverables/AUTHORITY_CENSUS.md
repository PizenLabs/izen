# AUTHORITY CENSUS — Phase 0 Audit

## Methodology
- Traced all `cmd/` entry points down to mutation sinks via concrete import/call chains.
- Inspected 5 candidate loops and 9 target packages.
- Classification tags assigned based on actual execution reachability, not folder names.

---

## Master Matrix: Target Packages

| Package / File | Primary Responsibility | Owns Loop State? | Has Mutation Authority? | Reachable from `cmd/`? | Classification Tag |
|---|---|---|---|---|---|
| `internal/execution` | Runtime execution boundary: `RuntimeExecutor`, `PatchManager`, `MutationSet`, `ArtifactValidator`, verification gate, mutation evidence recording. | No (delegates to adapter/loop) | **YES** — `PatchManager.Apply` (os.WriteFile), `MutationSet.Commit()` (tx commit), `executor.CommitMutation()` | YES — via `compose.Wire` → runtime/autonomy adapter → execution boundary | **PRODUCTION_AUTHORITY** |
| `internal/autonomy` | Autonomous loop vocabulary (`RuntimeLoop`, `LoopBounds`, `Observation`, `LoopDecision`, `RecoveryStrategy`, failure classification). Defines state vocabulary consumed by the runtime driver. | **YES** (`RuntimeLoop` owns state, attempts, cycles, termination) | **NO** — never calls filesystem, provider, or mutation directly; delegates to `Executor` interface (`adapter.Execute`, `adapter.Approve`) | YES — `internal/runtime/autonomy/driver.go` uses it; reachable from `cmd/izen/main.go` through runtime path | **PRODUCTION_COORDINATOR** |
| `internal/runtime` | Runtime dispatch, composition root (`compose`), dispatcher, output pipeline, event translator, handlers, engine evaluator. | Partial (`compose` creates runtime context; dispatcher routes events) | Indirect — `compose.Wire` creates capabilities; `output` writes `.logs/`; no direct mutation of workspace files except through execution adapter | YES — `cmd/izen/main.go` imports `compose` directly; `cmd/izen/runtime.go` uses `pkg/app` which routes through runtime layers | **PRODUCTION_COORDINATOR** |
| `internal/agent` | Agent loop (`AgentLoop`) with state machine (`AgentState`), event streams, approval channels. `executeStep` is a stub (`return nil`). No concrete mutation execution. | **YES** (`AgentLoop` state machine) | **NO** — `executeStep` is empty stub; `runExecutionPhase` calls stub; no filesystem or process mutation | **NO** — not imported by `cmd/izen/main.go`, `cmd/izen/orchestrate.go`, `cmd/izen/runtime.go`, or `cmd/diagnostic/main.go`. Only referenced by `lea/query.go` (ignored paths) and `ui/stream.go` (`internal/agents` plural, different package) | **DEAD_LINEAGE** |
| `internal/core/runtime` | `RuntimeContext`: capability guard, mutation budget, artifact registry, workspace snapshot cache, archetype registry aggregation. | No | **NO** — provides `CanWrite()`, `CanMutateFile()`, `ConsumeBudget()`; delegates actual mutation to callers (execution engine) | YES — injected by `compose.Wire` and used by runtime/autonomy/driver through adapter | **INFRASTRUCTURE_SDK** |
| `internal/orchestrator` | `Orchestrator`: workflow state machine (`Phase` transitions), persistent `RuntimeContext`, pipeline engine wiring, runtime loop injection (`WithRuntimeLoop`), execution cycle delegation (`ExecuteCycle`). | **YES** (`Orchestrator` owns phase state, SM, history) | **NO** — delegates execution cycles to `runtimeLoop.ExecuteCycle()` and mutations to the loop's `executor` (RuntimeExecutor) | YES — reachable via `internal/runtime/compose/compose.go` and through app layer; `cmd/izen/main.go` creates app that uses orchestrator concepts | **PRODUCTION_COORDINATOR** |
| `pkg/engine` | Layered engine pipeline (`layer0`-`layer5`), adaptive control (`control/loop.go`), decision engine, worker pool, inference, planner, validator. Compiles static plans and runs adaptive reconciliation. | **YES** (`ControlLoopOrchestrator` owns loop iterations, session observations, termination) | **Indirect** — `ControlLoopOrchestrator.dispatch` uses `WorkerPool.Submit` and `session.Apply` (variable/state mutations through observations); actual file patches produced by `layer3` pipeline, not directly committed by control loop | YES — `pkg/app/pipeline.go` uses `pkg/engine/pipeline/adaptive.go` which imports `pkg/engine/control`; `cmd/izen/runtime.go` uses `app.NewPipeline` | **PRODUCTION_COORDINATOR** |
| `pkg/runtime` | `orchestrator.Loop`, harness (`ExtractorPipeline`, `CandidateArtifact`), gate (`GatePipeline`, `GateResult`), preflight, target resolver. Defines the `Loop` that connects model output → RMAH → Gate → RuntimeExecutor commit. | **YES** (`Loop` owns `LoopState`: Idle → Executing → Verifying → AwaitingHuman → Committed) | **Indirect** — `Loop.ExecuteCycle` calls `executor.CommitMutation()` (delegates mutation authority to `RuntimeExecutor`) | YES — `pkg/cli` (`cli.Wire`) imports `pkg/runtime/orchestrator`; `cmd/izen/orchestrate.go` uses it | **PRODUCTION_COORDINATOR** |
| `pkg/kernel` | `Engine`: executes `Executable` tasks, enforces lifecycle contract (`StatusCompleted`/`StatusFailed`), wraps event bus (`event.EventBus`), creates `Runtime` per task. Task execution abstraction, not mutation authority. | No (per-task runtime context, not persistent loop) | **NO** — executes `task.Execute(ctx, rt)`; mutation authority lives in the concrete `Executable` implementation (e.g., pipeline, build agent), not in kernel | Partial — imported by some engine/test paths; not directly on `cmd/izen` production path unless through pipeline tasks | **SEMANTIC_COMPILER** (compiles task lifecycle into events; no direct mutation) — but actually **INFRASTRUCTURE_SDK** since it's a task execution framework |

> **Correction on `pkg/kernel`:** After inspection, `pkg/kernel` is purely an infrastructure SDK (task execution framework with event bus). It does not compile user intent or hold execution authority. Updated tag: **INFRASTRUCTURE_SDK**.

---

## Master Matrix: Candidate Control Loops

| Loop File | Owns State? | Mutation Authority? | Reachable from `cmd/`? | Competing? | Classification Tag |
|---|---|---|---|---|---|
| `internal/agent/loop.go` (`AgentLoop`) | **YES** (`AgentState`, `turnCount`, `stream`, `approvalCh`) | **NO** — `executeStep` is empty stub; no mutation calls | **NO** — unreachable from any `cmd/` entry point; `internal/agent` not imported by production entry points | No (isolated stub) | **DEAD_LINEAGE** |
| `internal/autonomy/runtime_loop.go` (`RuntimeLoop`) | **YES** (`RuntimeState`, `LoopBounds`, `history`, `attempts`, `recoveryCycles`, `termination`) | **NO** — validates decisions, applies state transitions, consumes observations; mutation delegated to `Executor` interface (`adapter.Execute`) | **YES** — `internal/runtime/autonomy/driver.go` creates and drives it; reachable through runtime/autonomy adapter wired by `compose` or `cli` | **YES** — runs concurrently with `pkg/runtime/orchestrator/loop` when both are active (dual-authority risk: runtime loop + orchestrator loop both consume execution results) | **PRODUCTION_COORDINATOR** |
| `internal/runtime/autonomy/driver.go` (`Driver`) | **YES** (`Driver` holds `loop` (*autonomy.RuntimeLoop), `runCtx`, `runCancel`, `runID`) | **NO** — drives loop; delegates execution to `adapter` (`adapter.Execute`, `adapter.Approve`) which maps to `RuntimeExecutor` | **YES** — used by autonomy runtime; reachable from production runtime path through adapter wiring | **YES** — competes with `pkg/runtime/orchestrator/loop` for control during runtime execution (both observe/execute/verify cycles) | **PRODUCTION_COORDINATOR** |
| `pkg/engine/control/loop.go` (`ControlLoopOrchestrator`) | **YES** (`iteration` count, `runID`, session observations, `maxIterations`) | **Indirect** — `dispatch` submits to `WorkerPool`; `session.Apply` updates variables; patches come from `adaptiveExecutor` pipeline results, not direct commit | **YES** — `pkg/app/pipeline.go` reaches it through adaptive pipeline (`pkg/engine/pipeline/adaptive.go`) when `RunAdaptive` is invoked | **YES** — competes with `pkg/runtime/orchestrator/loop` during adaptive execution; both manage execution cycles over different state representations (`Dynamic IR` vs `MemorySnapshot`) | **PRODUCTION_COORDINATOR** |
| `pkg/runtime/orchestrator/loop.go` (`Loop`) | **YES** (`LoopState`, `formatFailures`, `snapshot`) | **Indirect** — calls `l.executor.CommitMutation()`; mutation authority is delegated to the `executor` (`RuntimeExecutor`) | **YES** — `pkg/cli` (`cli.Wire`) creates it; `cmd/izen/orchestrate.go` uses `cli.Wire` and `stack.Run` which routes through orchestrator loop | **YES** — active during `izen orchestrate`; competes with `internal/autonomy` loop when both are wired (e.g., if `WithRuntimeLoop` is set alongside `Driver`) | **PRODUCTION_COORDINATOR** |

---

## Key Classification Justifications

- **PRODUCTION_AUTHORITY** (`internal/execution`): The only package that actually writes to disk (`os.WriteFile` in `PatchManager.Apply`), commits transactions (`MutationSet.Commit()` calling `engine.Transaction.Commit()`), and runs verification gates (`verifier.RunAll()`). Every mutation path in production ends here.

- **PRODUCTION_COORDINATOR** (`internal/autonomy`, `internal/runtime/autonomy/driver.go`, `internal/runtime`, `internal/orchestrator`, `pkg/engine/control/loop.go`, `pkg/runtime/orchestrator/loop.go`): All actively invoked in production, own loop/state machine state, drive transitions, but delegate mutation execution to `RuntimeExecutor` or `PatchManager`. None directly call `os.WriteFile` for mutation.

- **INFRASTRUCTURE_SDK** (`pkg/kernel`, `internal/core/runtime`): Helper frameworks, task execution, runtime context aggregation. No loop state ownership or mutation authority.

- **SEMANTIC_COMPILER** (`pkg/app` pipeline components like `compiler`, `extractor`): Compile user intent or model output into artifacts/IR; no persistent execution state or mutation authority.

- **DEAD_LINEAGE** (`internal/agent/loop.go`, `AgentLoop`): Unreachable from any `cmd/` entry point. The `executeStep` stub confirms no mutation path exists. Must be frozen in Phase 1 and eliminated in Phase 4.

- **LEGACY_REACHABLE**: None definitively identified as reachable-but-deprecated with uncleaned references. `internal/agent` is unreachable rather than legacy-reachable.
