# ARCHITECTURAL INVARIANT LOCK SUITE — Phase 0 & Phase 1 Enforcement Report

| Field            | Value                                                        |
| ---------------- | ------------------------------------------------------------ |
| Status           | COMPLETE — ALL LOCKS GREEN                                   |
| Version          | 1.0                                                          |
| Date             | 2026-08-25                                                   |
| Reference        | `docs/report/CODEBASE_SURVEY_REPORT.md` (Phase 0/1 findings) |
| Scope            | `internal/architecture/phase0_authority_lock_test.go`, `internal/architecture/phase1_context_risk_lock_test.go` |
| Mode             | Read-only mechanical lock suite (no business logic modified) |
| Verification     | `go build ./...` · `go test ./internal/architecture/... -v -race` · `go test ./... -race` · `golangci-lint run ./...` |

> **Purpose:** executable architectural proof that the Phase 0 Execution
> Authority invariants cannot be broken by future code edits, and that the
> Phase 1 Context Fidelity and Risk Scope invariants fail closed upon any
> violation. Every lock is a hard assertion — no skips, no placeholders.

---

## 0. Executive Summary

- **18 top-level lock tests** (46 test results including subtests) added under
  `internal/architecture/`, package `architecture`.
- All locks are **structural** (`go/parser` + `go/ast`, whitespace/comment
  immune) or **behavioral** (black-box against the exported
  `internal/execution` API only), so weakening an exported contract fails even
  if in-package tests are edited.
- Every violation message attributes the offending site as
  `file:line (function)` for actionable failures.
- Whitelists fail on **drift AND staleness**: adding a forbidden surface fails;
  silently removing a recorded exception also fails, so locks can never rot.
- **Mutation-sensitivity validated:** 7 representative invariant violations
  injected into a throwaway tree copy were detected 7/7; the clean tree passes
  after restore (byte-identical).

---

## 1. Deliverables

| File | Contents |
| --- | --- |
| `internal/architecture/phase0_authority_lock_test.go` | Guards 0.1–0.3: UI transaction/mutation ownership bans, intent-factory seam purity, fail-closed dispatch, exclusive RuntimeExecutor mutation authority |
| `internal/architecture/phase1_context_risk_lock_test.go` | Guards 1.1–1.5: snapshot seal unforgeability, tamper invalidation matrix, evaluator purity/determinism, pipeline ordering, scope non-escalation |

Shared fixtures: `lockScriptedProvider` (race-safe counting fake provider),
`lockFrozenMutationIntent` / `lockSealChannels` (canonical sealed intents),
`scanSelectorCallsInFuncs` / `scanIdentCallsInFuncs` / `calleeNamesInNode`
(function-attributed AST call scanners), `findNilGuard` (fail-closed guard
detector).

---

## 2. Phase 0 — Execution Authority Locks

### Guard 0.1 — No UI Transaction/Mutation Ownership

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase0UIPackageOwnsNoTransactionOrMutationAuthority` | AST sweep of the **entire recursive** `internal/ui` tree (incl. subpackages): zero `BeginTransaction/CommitTransaction/RollbackTransaction` calls except exactly one whitelisted composition-root rollback entry (`program.go::RunRollbackEngine::app.Execution.RollbackTransaction`, the `izen --rollback` recovery path wired by compose); zero `NewPatchManager/NewMutationSet/NewTxFS`; zero PatchManager/MutationSet surface calls (`ApplyContext/RollbackTo/SetMutationSet/SetLedger/SetContextID`); zero legacy-engine execution (`m.execEng.Execute/ExecuteStream/Apply`) | ✅ PASS |
| `TestPhase0UIWorkspaceWritesLockedToBookkeeping` | **Frozen inventory** of all 14 raw `os.*` write sites across 9 UI functions (session markers, debug/test logs, prompt history, audit trail, plan-stash cleanup). Any NEW raw write site fails; silently REMOVING a recorded site also fails (anti-stale) | ✅ PASS |

Scoping note (documented in-file): "workspace writes" means raw filesystem
mutation of execution targets. UI-internal `.izen/` bookkeeping persistence is
legitimate and is frozen to its exact current inventory instead of banned.

### Guard 0.2 — Intent Factory Seam

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase0HandleBuildRunIsPureIntentFactorySeam` | AST callee inventory of `handleBuildRun`: routes only via `beginStagedTask → dispatchStagedTask`; provider/mutation/gateway-execution surfaces banned. `dispatchStagedTask` switch arms locked structurally: FILE_MUTATE/GIT_ACTION → `runRuntimeTaskRequest` exactly once each; SHELL_EXEC → `runStagedShellGate` exactly once; default arm FAIL-CLOSED (bookkeeping-only callee allowlist: stall + persist + surface halt) | ✅ PASS |
| `TestPhase0RuntimeDispatchFailsClosedWithoutExecutor` | AST on all four runtime seams (`runRuntimeExecuteCmd`, `runRuntimeTaskRequest`, `runStagedBuildViaRuntime`, `runRuntimePrompt`): nil-guard on `m.executor`/`m.gateway` must precede every runtime reference; the failure branch is inert (no execution, no gateway resolution, no legacy handler); no shadow engine anywhere behind the boundary | ✅ PASS |
| `TestPhase0ExecutorErrorYieldsZeroSideEffects` | Behavioral: provider failure → deterministic surfaced error, ≤1 invocation (no shadow retry/fallback loop), byte-identical workspace, empty approval surface | ✅ PASS |
| `TestPhase0UninitializedProviderFailsClosed` | Behavioral: model-required strategy with nil provider fails deterministically before any mutation surface | ✅ PASS |

### Guard 0.3 — Execution Seam Authority

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase0MutationDispatchExclusivelyThroughRuntimeExecutor` | Package-wide seam quotas: `m.executor.Execute` allowed ONLY at `{runtime_cutover.go::runRuntimeExecuteCmd ×1, gateway.go::runGatedLine ×2}`; executor `Approve`/`Reject` ONLY inside the approval bridge (`runtime_executor.go`) with anti-vacuous presence checks | ✅ PASS |
| `TestPhase0MutationsHeldBehindRuntimeApprovalBoundary` | Behavioral proof from outside the package: an admitted mutation HOLDS at the approval gate with the workspace pristine pre-approval; `Reject` never writes; `Approve` is the sole writing act that applies the held change | ✅ PASS |

---

## 3. Phase 1 — Context Fidelity & Risk Scope Locks

### Guard 1.1 — Context Snapshot Unexported & Seal Integrity

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase1ContextSnapshotSealFieldsUnexported` | AST parse of `internal/execution/context.go`: exported field set locked to exactly `{ID, CreatedAt, Parent, Channels}`; any exported digest/seal/hash/signature/MAC-named field is fatal; ≥1 unexported seal field mandatory; compiled type cross-checked via `reflect`; digest readable only through the `Digest()` method. Import surface frozen to pure hashing/encoding (`crypto/sha256, encoding/hex, errors, fmt, strconv, strings, time`) | ✅ PASS |
| `TestPhase1ContextModuleImportSurfaceLocked` | Import freeze of context.go — dependency drift fails the lock | ✅ PASS |
| `TestPhase1ForgedSnapshotsFailClosedAtVerifyAndAdmission` | JSON-decoded, hand-assembled, zero-value, and nil snapshots ALL yield `ErrContextIntegrity` from `Verify()`, `AdmissionGateway.Admit` (deny decision, no snapshot propagated), and `RuntimeExecutor.Execute` — with ZERO provider calls, ZERO workspace writes, ZERO pending patches. SHA-256 seal shape (64-hex digest, `ctx-<digest[:16]>` content address) re-derived from crypto primitives | ✅ PASS |

### Guard 1.2 — Context Tamper Invalidation

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase1TamperedIntentPayloadRejectedWithZeroSideEffects` | 12-case tamper matrix over every payload class after `FreezeContext`: prompt content swap, prompt channel rename, evidence-ledger injection, target rename inside snapshot, target channel removal, tool-definition overwrite, environment-state swap, system-determinant swap, request-level prompt/evidence divergence, target append, target drop. Each case requires immediate `ErrContextIntegrity`, OutcomeFailed, **zero provider calls**, byte-identical workspace, zero approval surface. Positive-control subtest proves the untampered twin crosses cleanly (rejections are caused by the tampering, not the fixture) | ✅ PASS (13 subtests) |

### Guard 1.3 — Risk Scope Evaluator Purity & Isolation

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase1RiskScopeEvaluatorPureAndIsolated` | AST of `admission.go`: `EvaluateRiskScope` must be a free function taking exactly one `RiskInput` and returning exactly one `RiskVerdict`; no os/io/ioutil/http/net/exec/rand/time/json/atomic access inside the classifier body; module import surface frozen against external policy stores and dynamic rule engines | ✅ PASS |
| `TestPhase1RiskScopeEvaluatorDeterministicUnderConcurrency` | Behavioral purity: 16 goroutines × 50 iterations evaluate equal inputs concurrently with interleaved distinct inputs — verdicts must be byte-equal every time (race detector active; impossible if hidden state participated) | ✅ PASS |

### Guard 1.4 — Runtime Execution Pipeline Ordering

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase1ExecutionPipelineOrderingLocked` | AST landmarks in `RuntimeExecutor.Execute`: strict order `verifyIntentContext` < `selectStrategy` < `x.admission.Admit` < min(`invokeReadOnly`, `invokeMutation`); zero direct filesystem-mutation calls inside Execute; `ApplyContext`/`Commit` invocations confined to `Approve` only across the whole file | ✅ PASS |
| `TestPhase1PipelineOrderingObservableFromProof` | Behavioral mirror via `ExecutionProof`: a fidelity rejection leaves `Proof.Strategy == "" && Proof.RiskScope == ""` (rejected BEFORE selection); a risk-scope rejection records both the selected strategy and the evaluated tier (`workspace_mutate`) — proving selection preceded gating. Both cases: zero provider crossings, untouched workspace | ✅ PASS |

### Guard 1.5 — No Implicit Scope Escalation

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase1NoImplicitScopeEscalation` | Behavioral: read-only-admitted runtime rejects the SAME mutation intent identically across three re-submissions (`ErrRiskScopeExceeded`, tier `workspace_mutate`, zero provider calls, untouched workspace, no leaked approval surface). Escalation occurs ONLY through explicit `SetAdmittedCapabilities` re-grant; even then the granted intent still holds behind the approval gate | ✅ PASS |
| `TestPhase1ScopeTiersNeverInheritPrivileges` | Exhaustive capability truth table (5 grant sets × 4 scopes): no tier implies another; nil capability set denies everything fail-closed; 13-row classification table covering FILE_MUTATE/GIT_ACTION/FILE_EDIT/SHELL_EXEC/VERIFY, destructive commands, credential paths, traversal/system-path targets, and conservative classification of unknown strategies (never ride a read-only grant); `ClassifyTaskScope` parity checked for GIT_ACTION and SHELL_EXEC | ✅ PASS |

---

## 4. Mutation-Sensitivity Validation

To prove the locks are not vacuous, seven representative invariant violations
were injected into a throwaway copy of the repository (the real workspace was
never modified) and the suite was executed there:

| # | Injected violation | Detected by |
| --- | --- | --- |
| 1 | New `os.WriteFile` site added under `internal/ui` | Guard 0.1 write inventory ("NEW raw filesystem write site") |
| 2 | `PatchManager.ApplyContext` invoked from a rogue UI file | Guard 0.1 mutation-surface ban |
| 3 | `streamCmd` legacy fallback inside the FILE_MUTATE arm of `dispatchStagedTask` | Guard 0.2 factory-seam arm lock |
| 4 | Second `m.executor.Execute` submission site in a new UI file | Guard 0.3 seam quota |
| 5 | Exported `SealDigest` field added to `ContextSnapshot` | Guard 1.1 seal-field lock ("seal MUST be unexported") |
| 6 | `os.Getenv("POLICY_OVERRIDE")` inside `EvaluateRiskScope` | Guard 1.3 purity scan + import freeze |
| 7 | `net/http` import added to admission.go | Guard 1.3 policy-store import ban |

**Result: 7/7 detected. Clean tree passes after restore (byte-identical,
verified by diff).**

---

## 5. Verification Results

```
1. go build ./...                               ✅ OK
2. go test ./internal/architecture/... -v -race  ✅ ok  — 46/46 pass, 0 FAIL, 0 SKIP (~10.7s)
3. go test ./... -race                           ✅ exit=0 (entire repository)
4. golangci-lint run ./...                       ✅ 0 issues
```

Additional hygiene:

- Zero `t.Skip()` / placeholder assertions in either file.
- Behavioral fixtures are race-safe (mutex-guarded counters); the determinism
  lock runs concurrent evaluation explicitly under `-race`.
- Standard library only (`go/parser`, `go/ast`, `go/token`, `crypto/sha256`,
  `encoding/hex`, `encoding/json`, `reflect`, `sync`) plus already-vendored
  module packages (`internal/execution`, `internal/ai`, `internal/config`,
  `internal/core/authorization`, `internal/execution/strategy`). No external
  dependencies introduced.

## 6. Lock Maintenance Contract

The suites intentionally freeze current structure. Legitimate refactors will
trip them — by design. When a lock fires:

1. Review whether the change violates the Phase 0/Phase 1 authority model.
2. If it is a legitimate structural evolution, update the corresponding frozen
   inventory (`phase0UIWriteInventory`, seam quotas, import allowlists,
   ContextSnapshot field set) in the same commit, with justification.
3. Never weaken an assertion to make it pass; add a narrowly scoped, documented
   whitelist entry instead.
