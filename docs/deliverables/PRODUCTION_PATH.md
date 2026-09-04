# PRODUCTION PATH — Phase 0 Audit

## Active Production Pipeline

The **definitive active production pipeline** is the path through `cmd/izen/main.go` → `compose.Wire` → runtime/autonomy adapter → `internal/execution` mutation boundary (`RuntimeExecutor` / `PatchManager`).

Why this is the definitive path:
- `cmd/izen/main.go` is the primary binary entry point (line 78 `main()`).
- It creates the concrete `App` through `compose.Wire` (line 217), which is the only composition root that wires `RuntimeExecutor`, `PatchManager`, `MutationSet`, `Driver`, and the event bus.
- `cmd/izen/orchestrate.go` and `cmd/izen/runtime.go` are secondary subcommands (`orchestrate` and `run`). They use different pipelines (`pkg/cli` and `pkg/app`) and do not share the same mutation authority boundary as the main TUI/app path.
- The `cmd/diagnostic` entry point performs zero mutations.

Exact pipeline path:
```
cmd/izen/main.go (main)
  → compose.Wire (composition root)
    → creates App (internal/runtime/compose)
      → wires RuntimeContext (internal/core/runtime)
      → wires RuntimeExecutor (internal/execution/executor.go)
        → PatchManager (internal/execution/patch.go)
        → MutationSet (internal/execution/mutationset.go)
      → wires Driver (internal/runtime/autonomy/driver.go)
        → RuntimeLoop (internal/autonomy/runtime_loop.go)
        → Executor adapter (delegates to RuntimeExecutor)
  → ui.RunMainDashboardWithApp / ui.RunRollbackEngine
```

---

## How Many Distinct Runtime Authorities Can Commit Side-Effects?

**Two (2) distinct runtime mutation authorities** currently have the ability to commit side-effects:

### Authority 1: `internal/execution` (`RuntimeExecutor` + `PatchManager` + `MutationSet`)
- **Path**: `cmd/izen/main.go` → `compose.Wire` → `RuntimeExecutor`
- **Mutation mechanisms**:
  - `PatchManager.Apply` (`os.WriteFile` at `patch.go:726`)
  - `MutationSet.Commit()` (`engine.Transaction.Commit()` at `mutationset.go:197`)
  - `FILE_CREATE` protocol (`os.WriteFile` at `patch.go:594`)
- **Audit trail**: `ExecutionProof` (contract identity, mutation evidence, verification report, diff summary, transaction ID).

### Authority 2: `pkg/app` (`Pipeline` + `txfs.TxFS`)
- **Path**: `cmd/izen/runtime.go` (`runRuntimeCommand`) → `app.NewPipeline` → `Pipeline.Run`
- **Mutation mechanisms**:
  - `txfs.TxFS.Commit()` (`p.tx.Commit()` at `pipeline.go:425`)
  - `txfs.TxFS.Rollback()` (`p.tx.Rollback()` at `pipeline.go:332`)
  - `os.MkdirAll()` (`ensureParentDirs` at `pipeline.go:868`)
- **Audit trail**: `Result` (artifact list, validation outcomes, event snapshot) — **no contract identity, no mutation evidence vocabulary, no verification gate integration with `PatchManager`.**

This is a **critical dual-authority risk**: the same workspace file can be modified by either authority, but their rollback mechanisms (`MutationSet.Rollback` vs `txfs.TxFS.Rollback`) and audit vocabularies (`MutationEvidence` vs `Result`) are incompatible.

---

## Competing Control Loops During Runtime Execution

During runtime execution, **three loops can compete for control**:

### 1. `internal/autonomy` `RuntimeLoop` (`runtime_loop.go`)
- Owns bounded loop state (`RuntimeState`: Observing → Deciding → Executing → Verifying → Interpreting → Completed/Aborted).
- Enforces termination bounds (`LoopBounds`): max attempts, recovery cycles, execution steps, identical decisions, total tokens.
- Delegates mutation to `Executor` interface (`adapter.Execute`).

### 2. `pkg/runtime/orchestrator` `Loop` (`pkg/runtime/orchestrator/loop.go`)
- Owns execution cycle state (`LoopState`: Idle → Executing → Verifying → AwaitingHuman → Committed/Failed).
- Manages `MemorySnapshot` (single observation-phase disk read), `formatFailures`, and fast-fail budget (`maxFormatFailures = 2`).
- Delegates commit to `executor.CommitMutation()` (`RuntimeExecutor`).
- If both `RuntimeLoop` and `Loop` are active (e.g., autonomy driver wired with `WithRuntimeLoop` while orchestrator loop runs independently), the same model output can trigger conflicting state transitions.

### 3. `pkg/engine/control` `ControlLoopOrchestrator` (`pkg/engine/control/loop.go`)
- Owns adaptive reconciliation loop (`Observe` → `Decide` → `Execute`) over `Dynamic IR` (`ExecutionSnapshot`).
- Applies mechanical state changes (`session.Apply`) and dispatches through `WorkerPool`.
- If active alongside `pkg/runtime/orchestrator/loop`, the adaptive loop manages its own `ExecutionSnapshot` (variable/state mutations) independently of the orchestrator's `MemorySnapshot`.

### Competition Scenario
When `cmd/izen/main.go` creates an `App` via `compose.Wire`, it does not explicitly disable `pkg/engine/control` or `pkg/runtime/orchestrator` loops. The `App` uses `pipeline.Run` (`pkg/app`) which can trigger `RunAdaptive` (`pkg/engine/pipeline/adaptive.go`), which creates a `ControlLoopOrchestrator`. At the same time, `compose.Wire` may wire a `Driver` (`internal/runtime/autonomy`) with a `RuntimeLoop`, and the CLI (`cmd/izen/orchestrate.go`) explicitly creates an `orchestrator.Loop` (`pkg/runtime/orchestrator`).

If any of these loops are activated simultaneously on the same workspace target, the following conflicts occur:
- **State divergence**: `RuntimeLoop` records `RuntimeState`; `Loop` records `LoopState`; `ControlLoopOrchestrator` records `ExecutionSnapshot`. There is no shared state synchronization.
- **Mutation divergence**: A mutation applied by `RuntimeExecutor` (through `PatchManager`) updates the workspace but does not update the `MemorySnapshot` of `Loop` or the `Dynamic IR` of `ControlLoopOrchestrator` unless explicit invalidation callbacks (`onMutation`) are triggered. The `RuntimeExecutor` does wire `SetOnMutation` (`executor.go:506`), but `Loop` and `ControlLoopOrchestrator` have no equivalent cross-loop invalidation.
- **Termination divergence**: Each loop has its own termination criteria. A permanent abort in `RuntimeLoop` (`LoopAbort` with `FailurePermanent`) does not propagate to `Loop` or `ControlLoopOrchestrator`.

---

## Definitive Answer

**Active production pipeline**: `cmd/izen/main.go` → `compose.Wire` → `App` (runtime/autonomy adapter + execution boundary).

**Distinct mutation authorities**: **2** (`internal/execution` and `pkg/app/txfs`).

**Loops competing for control**: **3** (`RuntimeLoop`, `orchestrator.Loop`, `ControlLoopOrchestrator`). The `AgentLoop` (`internal/agent`) is dead lineage and does not compete.
