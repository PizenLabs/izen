# AUTHORITY GRAPH — Phase 0 Audit

Format: `[ENTRYPOINT] file:line` → `[DISPATCHER]` → `[COORDINATOR]` → `[AUTHORITY]` → `[SINK]` with concrete file:line references.

---

## Entry Point 1: `cmd/izen/main.go`

```text
[ENTRYPOINT] cmd/izen/main.go:78 (main())
  └── [COMPOSITION ROOT] cmd/izen/main.go:211 (compose.Wire)
      ├── [INFRASTRUCTURE SDK] internal/infrastructure/capabilities/osfile.go (NewOSFile)
      ├── [INFRASTRUCTURE SDK] internal/infrastructure/capabilities/exeshell.go (NewExecShell)
      ├── [INFRASTRUCTURE SDK] internal/infrastructure/capabilities/gitcli.go (NewGitCLI)
      ├── [INFRASTRUCTURE SDK] internal/infrastructure/capabilities/patchadapter.go (NewPatchAdapter)
      └── [COORDINATOR] internal/runtime/compose/compose.go (Wire → creates App)
          ├── [AUTHORITY] internal/execution/executor.go:474 (NewRuntimeExecutor)
          │   ├── [SINK] internal/execution/patch.go:726 (PatchManager.Apply → os.WriteFile)
          │   ├── [SINK] internal/execution/patch.go:332 (os.WriteFile backup creation)
          │   ├── [SINK] internal/execution/mutationset.go:197 (MutationSet.Commit → engine.Transaction.Commit)
          │   ├── [SINK] internal/execution/mutationset.go:230 (MutationSet.Rollback → engine.Transaction.Rollback)
          │   ├── [SINK] internal/execution/executor.go:667 (os.ReadFile pull-through)
          │   └── [SINK] internal/execution/executor.go:506 (PatchManager.SetOnMutation callback)
          └── [COORDINATOR] internal/runtime/autonomy/driver.go:207 (NewDriver → drives RuntimeLoop)
              ├── [COORDINATOR] internal/autonomy/runtime_loop.go:626 (RuntimeLoop — owns state)
              ├── [AUTHORITY] internal/execution/executor.go:942 (RuntimeExecutor.Execute)
              └── [SINK] pkg/runtime/orchestrator/loop.go:286 (Loop.ExecuteCycle → executor.CommitMutation)
                  └── [AUTHORITY] internal/execution/executor.go (CommitMutation → PatchManager.Apply → os.WriteFile)
```

Notes:
- `compose.Wire` creates the single production pipeline (`App`) that connects the runtime, autonomy driver, and execution boundary.
- `RuntimeExecutor` is the sole mutation authority. `PatchManager.Apply` performs the actual `os.WriteFile` (line 726) and shadow backup writes (line 332).
- `MutationSet.Commit()` triggers `engine.Transaction.Commit()` (line 197 in mutationset.go), which commits the snapshot/transaction boundary.
- `pkg/runtime/orchestrator/loop.go` (line 286) delegates mutation to `executor.CommitMutation`, confirming the orchestrator loop delegates authority rather than owning it.

---

## Entry Point 2: `cmd/izen/orchestrate.go`

```text
[ENTRYPOINT] cmd/izen/orchestrate.go:37 (runOrchestrateCommand)
  └── [DISPATCHER] pkg/cli/cli.go (Wire)
      └── [COORDINATOR] pkg/runtime/orchestrator/loop.go:164 (NewLoop)
          ├── [AUTHORITY] pkg/runtime/executor/RuntimeExecutor (wired by adapter)
          │   └── [SINK] pkg/runtime/orchestrator/loop.go:286 (executor.CommitMutation)
          │       └── [AUTHORITY] internal/execution/executor.go (CommitMutation pipeline)
          │           └── [SINK] internal/execution/patch.go:726 (os.WriteFile)
          ├── [COORDINATOR] pkg/engine/pipeline/adaptive.go:57 (RunAdaptive)
          │   └── [COORDINATOR] pkg/engine/control/loop.go:149 (ControlLoopOrchestrator.Run)
          │       ├── [INFRASTRUCTURE SDK] pkg/engine/control/session.go:65 (NewSession → Dynamic IR)
          │       ├── [COORDINATOR] pkg/engine/control/session.go:91 (session.Observe)
          │       ├── [COORDINATOR] pkg/engine/control/session.go:114 (session.MarkRunning)
          │       ├── [COORDINATOR] pkg/engine/control/session.go:146 (session.Apply → variable mutation)
          │       └── [AUTHORITY] pkg/engine/pipeline/adaptive.go:112 (adaptiveExecutor.Execute)
          │           ├── [SEMANTIC_COMPILER] pkg/app/pipeline.go:226 (Pipeline.Run → artifact generation)
          │           │   └── [SINK] pkg/app/pipeline.go:425 (tx.Commit → file write transaction)
          │           └── [AUTHORITY] internal/execution/patch.go (patch apply via pipeline results)
          └── [SINK] pkg/runtime/harness/extractor.go (artifact extraction — no direct mutation)
```

Notes:
- `cli.Wire` creates a `Pipeline` and `Loop` but does not create `RuntimeExecutor` directly; the adapter binds the provider and model to the CLI contract.
- `pkg/app/pipeline.go` (line 425) has a direct mutation sink: `tx.Commit()` (transaction file system commit) after the planning and validation stages. This is an independent mutation path from `PatchManager.Apply` — it commits workspace writes through `txfs.TxFS`.
- `pkg/engine/control/loop.go` applies variable mutations (`session.Apply`) but does not directly write files; file mutations come from the adaptive pipeline (`layer3.Run`) producing patches.

---

## Entry Point 3: `cmd/izen/runtime.go`

```text
[ENTRYPOINT] cmd/izen/runtime.go:100 (runRuntimeCommand)
  └── [DISPATCHER] pkg/app/app.go:156 (NewPipeline / Pipeline.Run)
      ├── [SEMANTIC_COMPILER] pkg/app/compiler/compiler.go (IntentCompiler → ir.IntentIR)
      ├── [SEMANTIC_COMPILER] pkg/app/pipeline.go:290 (capability resolution, policy compilation)
      ├── [COORDINATOR] pkg/app/pipeline.go:409 (plan stage)
      └── [AUTHORITY] pkg/app/pipeline.go:420 (execute stage → Kernel engine / pipeline execution)
          ├── [SINK] pkg/app/pipeline.go:425 (tx.Commit — workspace file write transaction)
          ├── [SINK] pkg/app/pipeline.go:332 (tx.Rollback — rollback on failure)
          └── [AUTHORITY] pkg/app/pipeline.go:868 (ensureParentDirs → os.MkdirAll)
              └── [AUTHORITY] pkg/app/pipeline.go:880 (os.MkdirAll for artifact parent dirs)
```

Key mutation sinks for `runRuntimeCommand`:
- `pkg/app/pipeline.go:425` (`tx.Commit`) — atomic workspace mutation through `txfs.TxFS`.
- `pkg/app/pipeline.go:868` (`os.MkdirAll`) — parent directory creation for artifacts.
- `pkg/app/pipeline.go:323` (`tx.Begin`) / `332` (`tx.Rollback`) — transaction lifecycle.

This entry point does NOT route through `internal/execution` directly; it uses the V3 pipeline (`pkg/app`) which manages its own transaction (`txfs.TxFS`) independent of `PatchManager`. This confirms a **dual-authority risk**: `cmd/izen/main.go` uses `internal/execution` mutation boundary; `cmd/izen/runtime.go` uses `pkg/app` transaction boundary.

---

## Entry Point 4: `cmd/diagnostic/main.go`

```text
[ENTRYPOINT] cmd/diagnostic/main.go:16 (main())
  └── [INFRASTRUCTURE SDK] internal/providers/*.go (NewOllamaProvider, etc.)
  └── [AUTHORITY — READ ONLY] cmd/diagnostic/main.go:56 (provider.Execute — non-streaming)
  └── [AUTHORITY — READ ONLY] cmd/diagnostic/main.go:81 (provider.ExecuteStream — streaming)
```
No mutation sinks. Only provider invocation for diagnostic verification.

---

## Cross-Path Mutation Sink Inventory

All concrete mutation sinks reachable from `cmd/`:

1. **`internal/execution/patch.go:726`** (`os.WriteFile`) — `PatchManager.Apply`
2. **`internal/execution/patch.go:332`** (`os.WriteFile`) — shadow backup creation (`createShadowBackup`)
3. **`internal/execution/mutationset.go:197`** (`ms.Transaction.Commit()`) — `MutationSet.Commit()`
4. **`internal/execution/mutationset.go:230`** (`ms.Transaction.Rollback()`) — `MutationSet.Rollback()`
5. **`internal/execution/patch.go:594`** (`os.WriteFile`) — `FILE_CREATE` protocol (new file creation within `Apply`)
6. **`pkg/app/pipeline.go:425`** (`p.tx.Commit()`) — `txfs.TxFS` transaction commit (`Pipeline.Run`)
7. **`pkg/app/pipeline.go:332`** (`_ = p.tx.Rollback()`) — rollback on failure (`Pipeline.Run`)
8. **`pkg/app/pipeline.go:868`** (`os.MkdirAll`) — parent directory creation (`ensureParentDirs`)
9. **`pkg/app/pipeline.go:323`** (`p.tx.Begin()`) — transaction begin (`Pipeline.Run`)
10. **`pkg/engine/control/session.go:146`** (`session.Apply`) — variable/state mutation (`Dynamic IR` state update)
11. **`pkg/runtime/orchestrator/loop.go:286`** (`l.executor.CommitMutation`) — delegated mutation call to `RuntimeExecutor`

---

## Dual-Authority Risk Highlight

| Path A (`main.go`) | Path B (`runtime.go`) | Overlap / Conflict |
|---|---|---|
| `internal/execution` mutation boundary (`PatchManager` + `MutationSet`) | `pkg/app` transaction boundary (`txfs.TxFS`) | Both can write workspace files; `runtime.go` does not use `PatchManager` or `MutationSet`, so rollback evidence from `runtime.go` (txfs) is not visible to `main.go` execution proof, and vice versa. |
| `RuntimeExecutor` owns contract identity (`ContractRegistry`) | `pkg/app` pipeline has no contract identity; uses `txfs.TxFS` transaction IDs only | A mutation committed through `main.go` and one through `runtime.go` have different audit trails; no unified mutation ledger exists across both authorities. |
| `internal/autonomy` loop (`Driver`) drives recovery cycles | `pkg/engine/control` loop (`ControlLoopOrchestrator`) drives adaptive reconciliation | Both manage bounded loops with different state vocabularies (`RuntimeState` vs `ExecutionSnapshot`). If both are active simultaneously (e.g., `compose` wires both), the same user objective could trigger conflicting recovery decisions. |
