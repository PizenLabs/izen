# LINEAGE STATUS — Phase 0 Audit

Itemized inventory of dead, legacy, or overlapping lineages to be frozen in Phase 1 and eliminated in Phase 4.

---

## DEAD_LINEAGE (Completely unreachable from `cmd/`)

### `internal/agent/loop.go` (`AgentLoop`, `AgentState`, `EventStream`)
- **Evidence of unreachable**: Not imported by `cmd/izen/main.go`, `cmd/izen/orchestrate.go`, `cmd/izen/runtime.go`, or `cmd/diagnostic/main.go`. Only references in codebase: `lea/query.go` (ignores `internal/agent` paths) and `internal/agent/loop_test.go` (test file). `internal/agents/` (plural) is a different package (`bridge.go`, `context_reducer.go`) used by `internal/ui/stream.go`, not by `internal/agent`.
- **Evidence of stub mutation**: `AgentLoop.executeStep()` (`loop.go:572`) returns `nil` unconditionally — no filesystem mutation, no process execution, no provider call.
- **Evidence of isolated loop**: `AgentLoop.runExecutionPhase()` (`loop.go:517`) calls the stub `executeStep` in a loop but never reaches `PatchManager`, `RuntimeExecutor`, or `MutationSet`.
- **Freeze action (Phase 1)**: Freeze all `internal/agent/` files; do not wire `AgentLoopConfig` or `NewAgentLoop` into `compose.Wire` or any production adapter.
- **Elimination action (Phase 4)**: Delete `internal/agent/loop.go`, `internal/agent/loop_test.go`, `internal/agent/checkpoint/manager.go`, `internal/agent/checkpoint/manager_test.go`, `internal/agent/bridge.go`, and remove any `lea/query.go` exclusions referencing `internal/agent`.

---

## LEGACY_REACHABLE (Reachable but logically deprecated — none definitively identified)

No package meets the strict definition of "old code still reachable from production paths due to uncleaned references, but logically deprecated." `internal/agent` is unreachable (`DEAD_LINEAGE`), not legacy-reachable. The `pkg/engine/control/loop.go` (`ControlLoopOrchestrator`) is actively used (`RunAdaptive`) and is not deprecated.

---

## OVERLAPPING / COMPETING LINEAGES (Must be frozen in Phase 1)

### 1. Dual Mutation Authority (`internal/execution` vs `pkg/app/txfs`)
- **Overlap**: Both `internal/execution/patch.go` (`PatchManager.Apply`) and `pkg/app/pipeline.go` (`txfs.TxFS.Commit`) can write workspace files.
- **Risk**: No shared rollback mechanism. `MutationSet.Rollback()` (`internal/execution`) and `txfs.Rollback()` (`pkg/app`) are independent. A mutation committed via `cmd/izen/main.go` (execution boundary) is invisible to `cmd/izen/runtime.go` (app pipeline), and vice versa.
- **Freeze action**: Freeze `pkg/app` mutation pipeline (`Pipeline.Run` stages 6-7) from being invoked in the same process as `compose.Wire` without an explicit authorization gate that selects one authority.
- **Elimination action (Phase 4)**: Unify mutation authority by making `pkg/app` pipeline delegate all file writes to `PatchManager` (through `RuntimeExecutor`), or eliminate `txfs.TxFS` in favor of `MutationSet`.

### 2. Triple Loop Competition (`RuntimeLoop` + `orchestrator.Loop` + `ControlLoopOrchestrator`)
- **Overlap**: `internal/autonomy/runtime_loop.go`, `pkg/runtime/orchestrator/loop.go`, and `pkg/engine/control/loop.go` all manage bounded execution cycles with different state vocabularies and termination criteria.
- **Risk**: Concurrent activation produces conflicting observations (`RuntimeLoop` consumes `Observation` from adapter; `Loop` consumes `MemorySnapshot`; `ControlLoopOrchestrator` consumes `ExecutionSnapshot`). A single user objective could trigger divergent recovery decisions (e.g., `LoopRepair` in runtime loop vs `DirectiveRetry` in control loop vs `Retry` in orchestrator loop).
- **Freeze action**: Freeze additional loop wiring in `compose.Wire` so that only one loop authority is active per process. If `Driver` is wired, do not also wire `orchestrator.Loop` independently unless explicitly coordinated through a shared `Executor` adapter.
- **Elimination action (Phase 4)**: Consolidate loop state vocabularies: either extend `RuntimeLoop` to absorb `MemorySnapshot` behavior (replacing `orchestrator.Loop`), or eliminate `RuntimeLoop` and make `Driver` delegate directly to `Loop`.

### 3. Stub Execution Path (`AgentLoop`) in `internal/agent`
- **Overlap**: If any future code attempts to wire `AgentLoop` into `App` (e.g., through a legacy adapter or test harness that leaks into production), the stub `executeStep` would create a silent no-op mutation path (no mutation executed, but loop reports `StateFinished`).
- **Freeze action**: Prevent any import of `internal/agent/loop.go` in production build targets (add build tag or lint rule).
- **Elimination action (Phase 4)**: Remove package entirely.

---

## Package-Specific Status

| Package | Status | Phase 1 Freeze | Phase 4 Elimination |
|---|---|---|---|
| `internal/agent` (loop + checkpoint + bridge) | DEAD_LINEAGE (unreachable, stub mutation) | Freeze — do not wire into compose/app | Delete package |
| `internal/autonomy` (`RuntimeLoop`) | PRODUCTION_COORDINATOR (active) | Keep active; freeze additional loop wiring conflicts | Consolidate state vocabulary with `pkg/runtime/orchestrator` |
| `internal/runtime/autonomy/driver.go` (`Driver`) | PRODUCTION_COORDINATOR (active) | Keep active; coordinate with single loop authority | Consolidate loop delegation |
| `internal/execution` (`RuntimeExecutor`) | PRODUCTION_AUTHORITY (active) | Keep active; freeze second mutation authority (`pkg/app`) from concurrent activation | Unify mutation audit trail |
| `pkg/app` (`Pipeline` + `txfs.TxFS`) | PRODUCTION_COORDINATOR / SECONDARY AUTHORITY | Freeze concurrent mutation activation without explicit gate | Unify with `internal/execution` mutation boundary |
| `pkg/engine/control/loop.go` (`ControlLoopOrchestrator`) | PRODUCTION_COORDINATOR (active via adaptive pipeline) | Freeze concurrent activation with `RuntimeLoop` or `Loop` | Consolidate adaptive control into single loop |
| `pkg/runtime/orchestrator/loop.go` (`Loop`) | PRODUCTION_COORDINATOR (active) | Freeze concurrent activation with `RuntimeLoop` or `ControlLoopOrchestrator` | Consolidate loop state vocabulary |
| `pkg/kernel` | INFRASTRUCTURE_SDK | No freeze needed | No elimination needed |
| `internal/core/runtime` | INFRASTRUCTURE_SDK | No freeze needed | No elimination needed |
