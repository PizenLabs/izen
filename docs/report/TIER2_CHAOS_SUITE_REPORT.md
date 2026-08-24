# TIER 2 — CROSS-PHASE CHAOS SUITE & CONTEXT-DIGEST REMEDIATION REPORT

| Field       | Value |
| ----------- | ----- |
| Status      | COMPLETE — ALL ACCEPTANCE CRITERIA GREEN |
| Version     | 1.0 |
| Date        | 2026-08-25 |
| Reference   | `docs/report/PHASE3_OCC_ENGINE_REPORT.md` (OCC engine, stress-tested here); `docs/report/ARCHITECTURE_INVARIANT_LOCK_REPORT.md` (Phase 0–3 locks, preserved) |
| Scope       | `internal/architecture/chaos_tier2_test.go` *(new)* · `internal/execution/executor.go` (`stampContractIdentity`, one-line remediation) |
| Lock suite  | `internal/architecture/chaos_tier2_test.go` (4 tests, external package `architecture_test`) |
| Verification | `go build ./...` · `go test -count=10 -race ./internal/architecture/chaos_tier2_test.go -v` · `go test -count=1 -race ./...` · `golangci-lint run ./...` |

> **Purpose:** prove that the runtime engine's Phase 0–3 invariants hold JOINTLY
> under extreme concurrency, resource contention, out-of-band state tampering
> and continuous failure loops; and remediate the execution-identity defect the
> suite surfaced — approval-path terminal evidence omitted the immutable
> `ContextDigest` established at intent admission.

---

## 0. Executive Summary

- **Four chaos scenarios, all green under `-race` ×10 repeats:** OCC
  high-concurrency race (100 concurrent mutating contracts + hostile
  out-of-band writer), bounded recovery-chain exhaustion, context-derivation
  memory stability (1,000 derivations, GC-reclamation proof via `weak`
  pointers, O(1) seal-verification latency), and evidence-ledger projection
  isolation (500-event randomized stream, 8 concurrent readers).
- **Deterministic by construction, not by luck.** Where a scenario could
  otherwise be scheduling-dependent (the OCC storm), a writer-to-approval
  barrier guarantees every admission baseline is stale BEFORE any approval can
  open — 100% conflict conversion is proven, not sampled. The projection
  stream uses a fixed-seed PRNG; latency comparisons use min-of-batches so
  conclusions rest on compute differences, not jitter.
- **Defect found and fixed.** The suite's initial identity sweep exposed that
  terminal evidence emitted on the approval path carried an EMPTY
  `ContextDigest`: `Approve`/`Reject` synthesize a fresh `ExecutionProof`,
  which `stampContractIdentity` populated without the digest. Remediated with
  a single line at the identity choke point; the workaround assertion was
  removed and replaced by hard fail-closed checks (non-empty, exact match
  against the admitted snapshot digest, sha256-hex form).
- **Zero drift elsewhere.** Full repository `-race` suite passes unchanged;
  lint reports 0 issues; no Phase 0–3 AST guard or behavioral lock was touched.

---

## Part A — The Tier 2 Chaos Suite

## 1. Deliverables

| File | Contents |
| --- | --- |
| `internal/architecture/chaos_tier2_test.go` *(new)* | Four cross-phase chaos tests plus self-contained fixtures (`chaosScriptedProvider`, frozen-intent builder returning the admitted digest, log silencer, lineage/anomaly helpers) |

The suite lives deliberately in the EXTERNAL test package
(`package architecture_test`): it compiles standalone (`go test
./internal/architecture/chaos_tier2_test.go`), shares nothing with the
in-package lock suites, and drives ONLY exported APIs — weakening any exported
runtime contract fails here regardless of in-package edits.

## 2. Scenario 1 — OCC High-Concurrency Race
(`TestChaos_OCC_HighConcurrencyRace`)

Three phases, 100 goroutines:

1. **Admission storm** — each goroutine wires an independent `RuntimeExecutor`
   over ONE shared workspace and submits its own integrity-sealed targeted
   mutation over the SAME overlapping target geometry. Every OCC baseline is
   captured in this window. All 100 intents must HOLD at the approval gate.
2. **Hostile out-of-band writer** — performs its FIRST divergent write before
   releasing an approval barrier (`firstDivergence` channel), then hammers the
   target until approvals resolve. Because write #1 happens-after ALL
   baselines and each subsequent write diverges again, EVERY captured baseline
   is provably stale at verify time — the 100%-conflict outcome is guaranteed,
   not statistical.
3. **Approval storm** — 100 concurrent `Approve` calls race the writer.

Per-execution absolute invariants (all 100):

| Invariant | Assertion |
| --- | --- |
| Conflict conversion | error wraps `ErrWorkspaceStateConflict`; atomic counter totals exactly 100 |
| Terminal outcome | `ABORTED_OCC`, terminal, produced exclusively by the commit gate |
| Taint | `Mutations().Tainted == true`, `FilesMutated == 0`, `!ApplyExecuted` |
| Authority | `Authoritative() == false`; `presentation.ProjectEvidence` blocks |
| Identity | `AttemptID == 1`, non-zero `ContractID`, non-empty `ContextDigest` matching the admitted snapshot (see Part B) |
| Telemetry | exactly 1 snapshot / 1 verification / 1 mismatch / 1 conflict per executor |
| Approval surface | consumed — `PendingPatchIDs()` empty after abort |

Global invariant: final workspace bytes are ALWAYS a complete state (writer
content matching `^external out-of-band edit #[0-9]+\n$`, or the original, or
the full mutation truth) — any mixed bytes are a partial-write leak.

## 3. Scenario 2 — Bounded Recovery-Chain Exhaustion
(`TestChaos_RecoveryChainExhaustion`)

A permanently failing provider drives automatic causal recovery: the failed
root seals FAILED evidence; each recovery step re-fails deterministically and
spawns the next causal child, up to `MaxRecoveryChainDepth`.

| Invariant | Assertion |
| --- | --- |
| Bound pinned | `MaxRecoveryChainDepth == 4` re-asserted (drift fails immediately) |
| Append-only history | each child: `ParentContractID` = parent, ancestry = parent's ancestry + parent, `RecoveryDepth == d`, registry back-pointer matches evidence back-pointer |
| Failed parents frozen | parent attempt counters never move while children admit |
| Exhaustion fail-closed | BOTH a material-drift step AND an exact resubmission of the deepest request refuse with `ErrRecoveryChainExhausted` at depth 5 — no evidence sealed, `OutcomeFailed`, ZERO provider crossings |
| Acyclic lineage | back-pointer walk with visited-set: root reached in exactly 4 hops, 5 unique contracts, no cycles, no dangling links |
| Digest binding | each step's evidence `ContextDigest` non-empty and identical to the registry contract's digest |

## 4. Scenario 3 — Context Derivation Memory Stability
(`TestChaos_ContextDerivationMemoryLeak`)

1,000 `Derive()` steps over a fixed-size (~46.5 KB) frozen payload, with
interleaved state-recovery probes (every 250 steps a throwaway node is
corrupted in place and MUST fail closed with `ErrContextIntegrity`).

- **GC reclamation proof** — released ancestors are tracked with Go 1.26
  `weak.Pointer`s; snapshots link ancestors by VALUE (string ID), never by
  live pointer. After forced collection, ALL 1,000 historical nodes must be
  reclaimed while the strongly-held tail survives and still verifies.
  Derivation runs in its own function so stale stack references die with the
  frame before collection is asserted.
- **O(1) seal verification** — min-of-7 batches × 200 verifications compares a
  depth-0 control against the depth-1,000 tail: identical payloads make costs
  byte-comparable; the deep best-case must stay within 20× the shallow
  best-case (25 ms floor). An O(depth) implementation would measure ~1000×;
  the margin between 20× and 1000× makes the verdict robust under `-race`.

## 5. Scenario 4 — Evidence-Ledger Projection Isolation
(`TestChaos_EvidenceLedgerProjectionIsolation`)

A seeded-PRNG interleaved stream of 500 records across `COMMITTED` /
`FAILED` / `CANCELLED` / `ABORTED_OCC` — half the committed records tainted —
is appended to one `EvidenceLedger` while 8 concurrent readers poll
authoritative projections mid-stream (test-side `sync.RWMutex`; the ledger
itself stays single-writer-append per its own contract).

| Invariant | Assertion |
| --- | --- |
| Gate exclusivity | granted ⇔ outcome COMMITTED ∧ untainted — verified per event against independently computed expectations |
| Fail-closed blocking | every blocked projection carries a non-empty reason classified by outcome (`taint` / `cancelled` / `optimistic-concurrency` / `failed`); ≥ 3 distinct reasons observed |
| Projection-state purity | blocked events NEVER enter the target projection map; map size == grant count |
| Mid-stream safety | no reader ever obtains authority for blocked evidence, nor misses authority for granted evidence, at ANY interleaving point (violation channel drained post-stream) |
| Ledger fidelity | `Latest` returns the identical immutable record for all 500 contracts; `AuthoritativeFor` mirrors the gate verdict exactly |
| Reconstruction hygiene | `SealFromScalars` refuses a non-vocabulary outcome (`DETONATED` → nil); nil evidence cannot enter the ledger |

Coverage guards fail the test if the random stream degenerates (any missing
outcome class, or missing taint split).

---

## Part B — ContextDigest Remediation

## 6. Defect Analysis

`RuntimeExecutor.Approve` and `.Reject` synthesize a FRESH `ExecutionResult` /
`ExecutionProof` from the held `pendingMutation`. The shared identity-stamping
helper populated four of five identity facts but not the digest:

```
stampContractIdentity:  ContractID ✓  AttemptID ✓  ParentContractID ✓  Ancestry ✓  ContextDigest ✗
```

Consequence: `sealTerminalEvidence` (which copies `Proof.ContextDigest` into
the sealed record) emitted approval-path terminal evidence — commits, OCC
aborts, apply failures AND rejections — with an empty `ContextDigest`.
Downstream projectors consuming only the evidence/event stream lost the
binding between terminal truth and the admitted context snapshot.

Root cause: the digest lived on the admitted `ExecutionContract` all along —
`contracts.Resolve(req, snapshot.Digest(), targets)` stores exactly the
verified snapshot digest — but was never transferred onto proofs rebuilt past
the gate.

## 7. Fix

Single line at the choke point (`internal/execution/executor.go`,
`stampContractIdentity`):

```go
res.Proof.ContextDigest = c.ContextDigest()
```

- **One source of truth:** the active `ExecutionContract`'s digest IS the
  admitted snapshot's sealed digest — no new plumbing field, no parallel
  bookkeeping.
- **Total coverage:** every terminal path crossing the helper is repaired at
  once — Execute (idempotent; same value already stamped), Approve commit,
  OCC abort, apply failure, and Reject (whose evidence now also binds to the
  admitted context).
- **No API surface change:** unexported function, unexported struct semantics;
  `internal/execution/context.go` (Phase 1 digest computation) untouched.

## 8. Enforcement (workaround removed)

The temporary suite accommodation ("assert identity without digest") was
deleted and replaced with hard fail-closed assertions:

- `chaosFrozenMutationIntent` now returns the admitted snapshot's sealed
  digest alongside the request; each race attempt stores its expected value.
- Scenario 1 requires, for all 100 approval-path evidence records:
  `ContextDigest != ""`, exact equality with the admitted snapshot digest, and
  64-char sha256-hex form.
- Scenario 2 additionally requires each recovery step's evidence digest to be
  non-empty and identical to the registry contract's `ContextDigest()`.

Any regression — dropping the stamp, rebuilding proofs without it, or
diverging digest sources — fails the suite loudly.

---

## 9. Forbidden-Change Compliance

| Constraint | Compliance |
| --- | --- |
| Phase 1 digest computation untouched | `internal/execution/context.go` unmodified; `FreezeContext`/`sealContext` semantics intact |
| Phase 0–3 static guards & behavioral locks not weakened | Zero edits to prior test files; full architecture lock suite green |
| No public struct/API changes | Unexported helper only; `ExecutionProof` fields unchanged |
| No dynamic dependencies outside execution package | Diff confined to `executor.go` (+ the new chaos file); stdlib-only test deps (`weak`, `math/rand`, …) |
| No skips / ignored errors in the chaos suite | Absolute fail-closed assertions throughout; provider/write failures fatal |

## 10. Verification Results

```
1. go build ./...                                              ✅ OK
2. go test -count=10 -race
       ./internal/architecture/chaos_tier2_test.go -v          ✅ 40/40 PASS (single-file mode)
3. go test -count=1 -race ./...                                ✅ exit=0 (entire repository)
4. golangci-lint run ./...                                     ✅ 0 issues
5. gofmt -l internal/architecture internal/execution           ✅ clean
6. go test -shuffle=on -race -run TestChaos_ -count=3          ✅ stable
```

Hygiene notes:

- All cross-goroutine traffic uses race-safe primitives only: wait groups,
  atomics (`atomic.Int64` abort counter, failed-write counter), channels
  (`firstDivergence` barrier, violation reporting), RWMutex around ledger
  access. Goroutine workers never call `t.Fatal`; failures funnel through
  collected errors and post-barrier classification.
- Runtime operational logging (`[occ] …` sweeps ×200 per storm) is silenced
  for the duration of each noisy test and restored via `t.Cleanup`.
- The mandated single-file invocation works because the file is fully
  self-contained in the external test package.

## 11. Maintenance Contract

When a chaos test fires:

1. Classify first: invariant violation (fix production) vs fixture drift (e.g.
   canonical label, telemetry shape, vocabulary growth) vs environment noise
   (reclaim deadline, latency floor — tune constants, never assertions).
2. Legitimate evolution (a fifth outcome class, a second approval resolution
   path, deeper admissible chains) requires updating the corresponding chaos
   expectations in the same commit, with justification — never loosening a
   check to make it pass.
3. Standing rule reinforced by Part B: **every piece of terminal evidence must
   remain bound to its admitted context — identity facts flow exclusively
   through `stampContractIdentity`, and the chaos suite holds that line.**
