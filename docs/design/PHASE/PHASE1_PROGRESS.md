# Phase 1 — Cutover Progress

**Status:** IN PROGRESS — steps 0-6 implemented behind the rollback flag; steps 7-8 partially scoped (see below).

## Flag state

| Variable | Default | Behavior |
|---|---|---|
| `IZEN_RUNTIME_EXECUTOR` | unset (0) | **disabled** → legacy mode-engine execution (old behavior, rollback path) |
| `IZEN_RUNTIME_EXECUTOR` | `1` | **enabled** → RuntimeExecutor execution authority for TUI mutations |

The flag is a **migration mechanism only**, not a permanent execution mode.
Per the cutover plan it ships **disabled by default** so any regression flips
the whole cutover back to the legacy path. It must be flipped to the default
after soak-testing and then removed entirely when the legacy path is deleted
(Phase 3 pruning).

## What the flag routes when enabled

```
User Input → Parser/Autonomy → Execution Intent → RuntimeExecutor
  → Strategy/Target resolution (IntentGateway, single authority)
  → Provider (runtime-owned) → Artifact → Approval gate
  → PatchManager.Apply (runtime-owned, verifier as apply gate)
  → Verification → ExecutionProof → Canonical events → UI projection
```

Covered execution surfaces:
- `$prompt` targeted mutation (autonomy BUILD workspace)
- `$hot` execution request (autonomy BUILD workspace)
- free-form mutation in build mode
- `/build` with a staged all-`FILE_MUTATE` plan
- investigate → build mutation handoff
- autonomy target-selector resume

Disabled (`IZEN_RUNTIME_EXECUTOR` unset) → the pre-cutover legacy routing is
preserved verbatim (mode engines → direct provider → PatchManager).

## Step status

| Step | Status | Notes |
|---|---|---|
| 0 — feature flag | done | `internal/ui/runtime_flag.go`; wired at every mutation routing boundary |
| 1 — verifier wiring | done | `execution.NewEngine` attaches its verifier; no-change evidence rule; regression tests |
| 2 — target resolution | done | executor path resolves through `IntentGateway` (strategy `collectTargets`); ambiguity stays explicit |
| 3 — approval collapse | done | executor approval gate is the single mutation approval; Alt+A authorizes via the AuthorizationEngine and the runtime applies |
| 4 — main cutover | done | autonomy BUILD + `/build` + investigate handoff route through `RuntimeExecutor.Execute` |
| 5 — bounded evidence | done | autonomy evidence ledger is the authoritative model contract; full-file context is supporting/bounded |
| 6 — autonomy handoff | done | `targetConfidence` preserved on `Trace`; intent/confidence/scope flow into `ExecutionProof` |
| 7 — retire shadows | **deferred to Phase 3** | legacy paths are the flag-off rollback boundary; one-way deletion happens only after the flag is the default and soaked |
| 8 — canonical events | done (executor path) | executor emits the lifecycle stream; UI subscribes; E2E test asserts the full event sequence. `pkg/event` documented as a separate product; audit collision left as Phase 3 (guardrail reads the log) |

## Definition-of-Done gaps (deliberate)

- `IZEN_RUNTIME_EXECUTOR` defaults to **disabled** during migration (rollback
  boundary per Step 0). Flipping to enabled-by-default and deleting the legacy
  paths is the post-soak Step 7 one-way door.
- The legacy flag-off paths remain (direct provider sites, `applyPatchWithDeadline`,
  `fastTrackFileContext` full-file dump) — they are the rollback path and are
  unreachable on the enabled architecture.

## Validation

`go build ./...`, `go test ./... -race`, `golangci-lint run ./...` are clean
(one pre-existing flaky runtime ledger-timing test surfaces intermittently
under `-count=3`; passes in isolation and under `-count=1`).
