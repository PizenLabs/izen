# PHASE 3 — STATE VERIFICATION & OCC ENGINE REPORT

| Field            | Value |
| ---------------- | ----- |
| Status           | COMPLETE — ALL ACCEPTANCE CRITERIA GREEN |
| Version          | 1.0 |
| Date             | 2026-08-25 |
| Reference        | `docs/report/CODEBASE_SURVEY_REPORT.md` (P3 — Gate D: real OCC + operational measurement); `docs/report/ARCHITECTURE_INVARIANT_LOCK_REPORT.md` (Phase 0/1 locks, preserved) |
| Scope            | `internal/execution/occ.go`, `internal/execution/{executor,evidence,mutation}.go`, `internal/core/authorization/impl.go` |
| Lock suite       | `internal/architecture/phase3_occ_lock_test.go` (9 tests) · `internal/execution/occ_test.go` (12 tests) · `internal/core/authorization/impl_test.go` (5 tests) |
| Verification     | `go build ./...` · `go test ./... -race -count=1` · `golangci-lint run ./...` |

> **Purpose:** replace the `noopSourceHashVerifier` placeholder with a
> production-grade target-scoped Optimistic Concurrency Control (OCC)
> verification engine: establish a pre-execution workspace baseline over the
> contract's resolved targets, re-validate target compatibility immediately
> before state commit, and cleanly abort mutations with an authoritative
> `ABORTED_OCC` tainted evidence outcome on any baseline conflict.

---

## 0. Executive Summary

- **The placeholder is gone.** `noopSourceHashVerifier` is eradicated from the
  repository and its resurrection is mechanically forbidden (AST sweep of the
  whole authorization package). The production authorization engine now wires
  a real constant-time sha256 freshness gate; the runtime executor now owns a
  full OCC engine gating its commit pipeline.
- **Target-scoped by construction.** Baseline snapshotting fingerprints ONLY
  the resolved targets of the execution contract — never a workspace-wide
  walk. A structural guard bans walk calls inside the OCC module outright.
- **Zero partial writes.** The pre-commit gate lexically precedes the first
  `ApplyContext` call in `Approve` (position locked by AST test): an aborted
  attempt can never reach disk. Behavioral tests prove byte-level outcomes for
  modified, deleted and concurrent out-of-band writers.
- **First-class terminal outcome.** `ABORTED_OCC` is produced exclusively by
  the OCC commit gate through `Proof.OccAborted`, sealed as tainted,
  non-authoritative evidence at the single Phase 2 evidence choke point. The
  coarse outcome mapper stays free of the derivation (Phase 2 guard intact).
- **Operational telemetry** records OCC check durations, fingerprint cache
  hits, verification counts and mismatch frequencies, exposed programmatically
  (`OCC().Metrics()`) and via `[occ] …` structured log lines.
- **26 new tests** across three files; all existing suites pass unchanged
  under `-race`; lint reports 0 issues.

---

## 1. Deliverables

| File | Contents |
| --- | --- |
| `internal/execution/occ.go` *(new)* | The OCC engine: `WorkspaceBaseline` (immutable target-scoped fingerprint set with content-addressed digest), `OCCVerifier` (`SnapshotBaseline` / `VerifyAgainst` / `Metrics`, race-safe), `WorkspaceStateConflict` error taxonomy (`modified` / `deleted` / `created` / `unreadable`) wrapping the `ErrWorkspaceStateConflict` sentinel, `OCCTelemetry` metrics record |
| `internal/execution/executor.go` | Runtime wiring: `occ *OCCVerifier` owned by `RuntimeExecutor` (+ `OCC()` observability accessor); admission-time baseline for targeted mutations held on `pendingMutation.baseline`; the pre-commit gate and the clean-abort path (`abortOnStateConflict`) in `Approve`; `Proof.OccAborted` flag |
| `internal/execution/evidence.go` | `EvidenceAbortedOCC` doc promoted from "reserved" to "produced exclusively by the Phase 3 OCC commit gate"; `sealTerminalEvidence` derives the abort outcome solely from `Proof.OccAborted` and forces `Tainted=true`; the coarse mapper contract documented as never deriving it |
| `internal/execution/mutation.go` | New canonical per-target outcome `OutcomeOCCAborted` (`"occ_aborted"`) with `Display`/`ParseMutationOutcome` parity |
| `internal/core/authorization/impl.go` | `noopSourceHashVerifier` deleted; `sha256SourceHashVerifier` (constant-time compare over `DomainSourceHash` — deterministic length-prefixed sorted domain encoding with absent-file markers) wired into `NewProductionAuthorizationEngine` unchanged signature |

Test files:

| File | Tests |
| --- | --- |
| `internal/architecture/phase3_occ_lock_test.go` | Guards 3.1–3.3 (structural) + behavioral clean-commit control, deterministic mid-execution conflicts, creation-intent control, hostile concurrent-writer loop |
| `internal/execution/occ_test.go` | Engine units: scoping, dedup/cleanup, all four conflict kinds, multi-conflict aggregation, mtime-touch false-positive resistance, cache-hit/mismatch telemetry, nil-baseline triviality, digest divergence |
| `internal/core/authorization/impl_test.go` | Domain-hash determinism/target-scoping/absent-markers, verifier match/mismatch/empty-skip, production-engine stale-deny + fresh-authorize through exported API, freshness-gate ordering after scope containment |

---

## 2. Engine Design

### 2.1 Target-Scoped Baseline Snapshotting

`SnapshotBaseline(targets)` observes EXACTLY the declared geometry:

- Deduplication + slash-normalization of targets; order preserved.
- Per target: `stat` + content sha256, recorded as
  `occFingerprint{size, modNano, hash}`; any stat failure (or directory) is the
  canonical *absent* fingerprint — capture NEVER fails, so a missing target is
  a legitimate creation intent whose violation surfaces at verify time.
- A size+mtime-keyed content-hash cache short-circuits repeated observation of
  unchanged files (retry cycles) — counted as cache hits in telemetry.
- Output identity is content-addressed: `baselineDigest` hashes the sorted,
  length-prefixed fingerprint set (`izen-occ-baseline-v1`), so identical states
  share a digest and ANY drift forks it.

No recursive traversal exists anywhere in the module (Guard 3.2).

### 2.2 Pre-Commit Verification

`VerifyAgainst(baseline)` re-validates every baseline target against the live
workspace:

| Baseline state | Live state | Verdict |
| --- | --- | --- |
| present | stat error (non-NotExist) | `unreadable` — fail closed |
| present | NotExist | `deleted` |
| present | same size AND mtime | clean — **cache hit**, no read |
| present | otherwise | content hash compare → equal = clean (touched-same-content), differ = `modified` |
| absent | still absent | clean (creation intent protected) |
| absent | exists | `created` |

All conflicts are returned together (never just the first). The mtime fast
path cannot produce false positives: a rewritten-but-identical file settles on
the hash comparison (unit-proven via `Chtimes`).

### 2.3 Runtime Commit-Pipeline Integration

```
Execute:   … → contract admission (P2) → OCC BASELINE(targets)   [mutation path]
                                          │
           approval gate HOLD             ▼
Approve:   BeginMutation → ★ OCC COMMIT GATE → ApplyContext loop → commit
                              │
                              └─ conflict ⇒ abortOnStateConflict
```

- The gate runs BEFORE the first apply/write call site (locked structurally).
- On conflict: the open `MutationSet` is terminated cleanly (nothing was ever
  recorded or applied — rollback closes the transaction), per-target
  `OutcomeOCCAborted` evidence is recorded without claiming an apply, the
  graph fails with `events.FailurePermanent` at stage `executor.occ`, the
  verification stage is marked explicitly skipped ("occ abort before any
  apply"), the caller receives an error wrapping
  `ErrWorkspaceStateConflict`, and the approval surface is consumed.
- `Cancel()` is NOT used: an OCC abort is a distinct terminal evidence
  outcome, not a cancellation.

### 2.4 Evidence Sealing (ABORTED_OCC producer topology)

- `sealTerminalEvidence` (the single Phase 2 choke point, invoked only from
  `finalizeResult`) produces `EvidenceAbortedOCC` exclusively from
  `Proof.OccAborted`; `evidenceOutcomeFor` remains free of the derivation.
- OCC-aborted summaries are forced `Tainted=true`: the held proposal is stale
  and every downstream projector must invalidate tentative state even though
  zero bytes reached the workspace. `Authoritative()` is false by construction;
  `presentation.ProjectEvidence` blocks the projection (behaviorally asserted).
- `SealFromScalars` round-trips `ABORTED_OCC` for audit/replay (vocabulary
  member was already terminal-blocking since Phase 2).

### 2.5 Authorization Freshness Gate

`sha256SourceHashVerifier.VerifySourceHash(paths, snapshotHash)`:

- Empty declared snapshot ⇒ not-applicable pass (legacy proposals that declare
  no baseline are unaffected — verified against both production callers).
- Otherwise recomputes `DomainSourceHash(root, paths)` and compares
  constant-time; mismatch yields `SourceHashMismatchError` denied at
  `StepDependencyFreshness`.
- Ordering preserved: scope containment still fires before freshness
  (unit-asserted).

### 2.6 Operational Telemetry

| Metric | Source |
| --- | --- |
| Snapshot count / duration | `OCCTelemetry.Snapshots`, `SnapshotDuration()` |
| Verification count / duration | `OCCTelemetry.Verifications`, `VerifyDuration()` |
| Fingerprint cache hits | `OCCTelemetry.CacheHits` (snapshot reuse + verify fast path) |
| Mismatch frequency | `OCCTelemetry.Mismatches`, `ConflictsFound` |
| Observability | `RuntimeExecutor.OCC().Metrics()` + one-line `[occ] baseline…` / `[occ] verify…` logs with scoped/digest/conflict/duration fields |

---

## 3. Architecture Lock Suite — `phase3_occ_lock_test.go`

Structural guards parse production sources with `go/parser` + `go/ast`
(whitespace/comment immune); behavioral guards drive only EXPORTED package APIs.

### Guard 3.1 — Noop Eradication & Real Production Verifier

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase3NoopVerifierEradicated` | AST sweep of every production file under `internal/core/authorization`: zero `noopSourceHashVerifier` idents; `impl.go` must declare `sha256SourceHashVerifier`. Behavioral half (exported APIs): `NewProductionAuthorizationEngine` denies a stale domain at `StepDependencyFreshness` and authorizes the fresh twin (checkpoint fixture on disk) | ✅ PASS |

### Guard 3.2 — Target Scoping & Gate Ordering

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase3OCCBaselineIsTargetScoped` | occ.go must declare `WorkspaceBaseline` / `OCCVerifier` / `WorkspaceStateConflict`; any `Walk`/`WalkDir` call site inside the module is fatal (no workspace-wide scan may ever exist) | ✅ PASS |
| `TestPhase3OCCGatePrecedesEveryWrite` | Inside `Approve`, the minimum `VerifyAgainst` call position must precede the minimum `ApplyContext` position — the OCC gate lexically precedes every final file write | ✅ PASS |

### Guard 3.3 — ABORTED_OCC Producer Topology

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestPhase3AbortedOccProducerTopology` | `evidenceOutcomeFor` body stays free of `EvidenceAbortedOCC` (Phase 2 continuity); `sealTerminalEvidence` must reference it AND assign `Tainted`; the flag assignment must live on the Approve conflict path (`abortOnStateConflict`, called from `Approve`); canonical label `"ABORTED_OCC"` re-asserted terminal/non-committed | ✅ PASS |

### Behavioral Invariants (exported-API only)

| Test | Proves | Result |
| --- | --- | --- |
| `TestPhase3CleanCommitStillCommits` | Positive control: uncontended flow commits (`COMMITTED`, untainted, `FilesMutated=1`); Execute result carries NO premature evidence while approval-held; telemetry shows ≥1 snapshot/verification, 0 mismatches, ≥1 cache hit, non-negative durations | ✅ PASS |
| `TestPhase3OutOfBandMidExecutionEditAbortsClean` | Out-of-band edit between admission-hold and Approve ⇒ error wraps `ErrWorkspaceStateConflict`; evidence `ABORTED_OCC` + tainted + `FilesMutated=0` + not authoritative; presentation gate blocks; workspace keeps BYTE-FOR-BYTE the external content; approval surface consumed; mismatch telemetry recorded | ✅ PASS |
| `TestPhase3OutOfBandDeletionAbortsClean` | Deleted target ⇒ same abort invariants; workspace membership unchanged (no resurrection write, nothing added) | ✅ PASS |
| `TestPhase3CreationIntentCommitsWhenUncontended` | Absent-at-baseline target remaining absent commits normally (full-content provider) — the gate protects admitted state without forbidding creation | ✅ PASS |
| `TestPhase3ConcurrentOutBandWriterNeverPersistsPartialState` | Hostile writer loops `os.WriteFile` across the whole execute→approve window, 12 iterations: final bytes are ALWAYS a complete state (writer content / executor truth / rolled-back original) — any mixed bytes fail; conflicted iterations satisfy every `ABORTED_OCC` invariant with disk == writer content; committed iterations are untainted `COMMITTED`. Runs under `-race` | ✅ PASS |

Supporting unit coverage: 12 engine tests (`occ_test.go`) and 5 verifier tests
(`impl_test.go`) — 26 new tests total.

Obsolete-verifier-model marker: `TestProductionEngineWiresRealSourceHashVerifier`
carries the `Deprecated: Obsolete Verifier Model` doc note — retained as the
modern lock replacing whatever noop-era expectations existed; no legacy test
assertions were weakened or removed.

---

## 4. Forbidden-Change Compliance

| Constraint | Compliance |
| --- | --- |
| No unbounded/workspace-wide scans per submission | Baseline reads only resolved targets; `Walk/WalkDir` banned in occ.go by Guard 3.2 |
| No partial writes when OCC validation fails | Gate precedes all writes (structural + behavioral + concurrency proof); abort path applies nothing and rolls back the open boundary |
| Phase 0 authority boundaries untouched | Full suite green; UI dispatch/approval seams unchanged |
| Phase 1 fidelity/risk gating untouched | `verifyIntentContext` / `Admit` ordering and locks unmodified (suite green) |
| Phase 2 identity/evidence structures untouched | Contract registry, evidence immutability, seal choke point and mapper purity preserved — re-asserted by Guard 3.3 and the phase2 suite |
| `Cancel()` never substitutes OCC abort | Dedicated `abortOnStateConflict` terminal path with `FailurePermanent` + `ABORTED_OCC` evidence |
| Existing assertions not weakened | Zero edits to prior test files; obsolete model marked on the NEW replacement lock |
| No unrelated packages altered | Diff confined to state-verification, commit validation and evidence-emission paths (`internal/execution`, `internal/core/authorization`, `internal/architecture`) |

---

## 5. Verification Results

```
1. go build ./...                                ✅ OK
2. go build ./cmd/izen                           ✅ OK
3. go vet ./...                                  ✅ clean
4. go test ./... -race -count=1                  ✅ exit=0 (entire repository, 99+ packages)
5. golangci-lint run ./...                       ✅ 0 issues
6. phase3 lock suite (-race, verbose)            ✅ 9/9 pass
7. concurrency test ×5 repeats                   ✅ stable, no flakes
```

Hygiene notes:

- All OCC telemetry counters accumulate under one mutex; the concurrency lock
  exercises them under `-race`.
- Behavioral fixtures reuse the shared architecture-lock provider/intent
  fixtures (`lockScriptedProvider`, `lockFrozenMutationIntent`,
  `lockOriginal`/`lockMutated`) plus a race-safe full-content provider for
  creation flows — no new external dependencies (stdlib sha256 only).
- Failure messages attribute violations to `file:line` for actionable hits.

## 6. Maintenance Contract

The Phase 3 locks intentionally freeze the current topology. When one fires:

1. Decide whether the change violates the OCC model (placeholder resurrection,
   post-write verification, mapper-derived aborts, workspace walks).
2. Legitimate evolution (e.g. adding a second write stage after the gate)
   requires updating the corresponding guard in the same commit, with
   justification — never weakening an assertion to make it pass.
3. Any new mutation-writing surface must keep the invariant: **verification
   precedes every final file write, or the attempt aborts tainted.**
