# Phase 2 — Execution Truth Report

**Status:** IMPLEMENTED — execution truth is authoritative, observable, and
distinct from model claims and UI state. No new autonomous behavior.
**Companion:** `PHASE_0_ARCHITECTURE_BASELINE.md`, `PHASE_0_FINDINGS.md`,
`PHASE_1_CUTOVER_PLAN.md`, `PHASE_1_IMPLEMENTATION_REPORT.md`, `PHASE1_PROGRESS.md`.
**Baseline:** `aa9079f` (Phase 1 cutover).

---

## 1. Current execution-truth model

The enabled path (`IZEN_RUNTIME_EXECUTOR=1`) is one authority chain:

```
Provider output
  ↓ ai.ProviderUsage           (authoritative billing)
Artifact extraction+validation (V3 artifact gate, executor)
  ↓ Patch{Original, Modified}
Approval gate (human)
  ↓ RuntimeExecutor.Approve
PatchManager.Apply (byte compare → evidence) → verifier gate (report captured)
  ↓ MutationSet transaction (commit / rollback)
ExecutionProof (outcome, evidence, verification, usage)
  ↓ canonical events (graph transitions)
UI projection (executionResultUpdate / handleDomainEvent)
```

Source → transformation → consumer map (pre-Phase 2):

| Truth | Source | Transformation (pre-Phase 2) | Consumer |
|---|---|---|---|
| usage | `ai.ProviderUsage` | dropped `Known`/`Cached`/`Reasoning` at `ModelInvocation`; no `Known` on `ExecutionCompleted` | footer infers `known` from `in>0‖out>0`; apply-terminal hardcoded `known=true` |
| artifact | model response | no validation gate on the executor path; apply-time guards only | proposal dock |
| mutation | `PatchManager.Apply` | `recordMutationEvidence` fabricated `VerificationRun/Passed=true` for changed; no-change recorded `ApplyExecuted=false` | proof, log, ActivityTree |
| verification | verifier gate | executor re-ran `RunAll()` **after** commit | proof + UI |
| outcome | executor | pending approval → `no_artifact`; rejection → `cancelled`; rolled-back aggregate could read `changed` | proof + UI |
| events | graph transitions | no `approval.rejected` transition | UI |

Every fabrication above was corrected in Phase 2.

## 2. Artifact boundary

The executor path now runs the established V3 artifact gate
(`execution.NewV3ArtifactPipeline`, the same pipeline the legacy build path
uses) inside `invokeMutation` — `RuntimeExecutor.artifactGate`
(`internal/execution/executor.go`):

```
Provider Output → ResolveModifiedContent → artifactGate (validate+normalize) → valid Patch
```

- Registered-language targets (`.go`/`.html`/`.json`) that fail validation
  (`go/parser`, WHATWG HTML + lexical scan, JSON) are rejected **before any
  approval or mutation surface** as `OutcomeArtifactRejected`
  (`ErrArtifactRejected`).
- Unregistered languages pass normalized (canonical bytes: CRLF→LF, BOM
  strip, trailing newline), so the proposal preview and the eventual disk
  write agree.
- Empty extraction, raw patch markers, ambiguous snippets and truncated
  content remain blocked by the pre-existing empty-artifact check and the
  apply-time guardrail cascade (`IsAmbiguousSnippet`, `IsTruncatedOutput`,
  destructive-wipe guard).
- A patch strategy does **not** silently fall back to full-file replacement
  beyond the established ≤50 KB `forcedFullContentFallback` contract, which
  is itself gated by `IsAmbiguousSnippet` for existing files.

## 3. MutationEvidence contract

`recordMutationEvidence` (`internal/execution/patch.go`) now takes explicit
`applyFacts{executed, changed, verifyRun, verifyPassed}` derived from what the
apply boundary actually did — never from the model, the provider, or the
outcome label:

| Outcome | ApplyExecuted | FilesystemChanged | VerificationRun | VerificationPassed |
|---|---|---|---|---|
| changed | true | true | gate fact | gate fact |
| created (FILE_CREATE) | true | true | **false** (gate does not run) | **false** |
| nochange | **true** (was false) | false | gate fact | gate fact |
| verify_failed | true (write happened) | **post-restore byte compare** | true | false |
| apply_failed | false (no write) | false | false | false |
| skipped | false | false | false | false |

The verification facts come from the gate that actually ran (or did not run).
`applyFacts` for a `changed` outcome can no longer fabricate a pass.

## 4. Verification ordering

Verification is now an actual **gate**, not a post-hoc decoration:

- The gate runs inside `PatchManager.Apply` (the Phase 1 verifier-as-apply-gate),
  before commit. Its report is captured on the owning `MutationSet`
  (`MutationSet.Verification`, set by `PatchManager.recordVerification`).
- `RuntimeExecutor.Approve` reads that captured report — it **no longer
  re-runs `RunAll()` after commit**. The verification work always precedes the
  terminal result; nothing is verified after a success is declared.
- A failing gate restores the shadow backup, records `verify_failed`
  evidence, fails the apply, and the whole transaction rolls back to
  `MutationFailed` — `execution.failed` follows `verification.completed
  (failed)`. `verify_failed` never degrades into success.
- With no verifier attached, the verification stage is explicitly skipped
  (`"no verifier gate ran during apply"`), `res.Verification` is empty, and
  the evidence records `VerificationRun=false` — "verification unavailable"
  is representable and rendered as such.

## 5. Execution result semantics

`MutationOutcome` vocabulary extended with three states (`internal/execution/mutation.go`):

- `artifact_rejected` — artifact existed but failed the validation boundary
  (distinct from `patch_failed` = no usable artifact).
- `pending_approval` — a valid artifact is held at the approval gate (was
  mislabeled `no_artifact`).
- `rejected` — the human explicitly rejected the held proposal (was conflated
  with `cancelled`).

The required distinctions map as follows:

| Requirement | State |
|---|---|
| success / verified | `changed`/`created` + `MutationEvidence.Verify()` / `Verification.Passed` |
| success / no-change | `nochange` |
| artifact rejected | `artifact_rejected` |
| target ambiguous | `ClarificationRequired=true` + `OutcomeCancelled` (no model call) |
| approval rejected | `rejected` |
| mutation failed | `apply_failed` |
| mutation changed but verification failed | `verify_failed` (mutation rolled back; never persists) |
| verification unavailable | `changed` + `Verification.Passed=false` + `VerificationRun=false` |
| provider failed | `failed` / `patch_failed` |
| execution cancelled | `cancelled` |

No unnecessarily large enum was created; three states were added to the
existing vocabulary because the existing ones could not express the contract.

## 6. ProviderUsage lifecycle

`ModelInvocation` now carries `Known`, `CachedTokens`, `ReasoningTokens`;
`ExecutionCompleted` carries `Known`, `CachedTokens`, `ReasoningTokens`.

- Streaming path (`invokeStream` via `UsageProvider`) and non-streaming path
  (`Execute` fallback + legacy `resp.TokenInput/Output` transport) both
  converge on `ai.ProviderUsage`.
- `finalizeResult` aggregates provider-reported counts; `Known` is true when
  at least one invocation reported usage and **false when no invocation did**.
- The UI reads `res.Completed.Known` directly (`tokenUsageCmdKnown`), never
  inferring `known` from counts and never hardcoding it. `!Known` renders
  "usage unknown"; `Known` with zero counts renders "0 tok" — the two are
  never conflated.

## 7. Canonical event semantics

- Every event continues to be generated only from a graph transition
  (`internal/execution/graph/graph.go`), which is driven only by real runtime
  boundaries. No UI-progress event was added or kept.
- New transition: `approval.rejected` (`events.EventApprovalRejected`) —
  emitted by `Graph.RejectApproval` on a real human rejection, distinct from
  `execution.finished(success=false)` for a cancellation.
- `verification.completed` is emitted with the gate report that actually ran
  (passed or failed), before `execution.finished`.
- `execution.finished(success=true)` is emitted only after commit + the
  verification gate reached a real terminal result. A rolled-back apply emits
  `execution.failed`, never success.

## 8. UI projection changes

`internal/ui/gateway.go` `executionResultUpdate` (the single shared projection):

- **No-change terminal** — a committed `OutcomeNoChange` renders its own
  "Mutation applied — no content changed." line. Previously it fell through to
  the generic "Completed — nothing produced." fallback.
- **Rejected terminal** — `OutcomeRejected` renders "Rejected — no files were
  modified." as its own branch, distinct from `OutcomeCancelled`.
- **Usage truth** — every terminal branch passes the runtime's authoritative
  `res.Completed.Known` to `tokenUsageCmdKnown`; the apply-terminal branch no
  longer hardcodes `known=true`.
- `handleDomainEvent` (`internal/ui/model.go`) subscribes and projects
  `approval.rejected`, and treats `OutcomeRejected` as a clean terminal
  (OpOutcomeCancelled), never a fabricated failure.
- `recordRuntimeProof` (`internal/ui/execution_proof.go`) marks
  `verify_failed` executions as rolled back.

The UI still projects canonical runtime state; it never independently decides
success/changed/verified/token-count. The visual identity is unchanged.

## 9. Multi-file semantics

`RuntimeExecutor.Approve` now defines explicit transaction semantics:

- **Failure at any file** → `RollbackTo(MutationFailed)`, aggregate outcome is
  `apply_failed` (or `verify_failed` when the gate failed) — **never**
  `changed`, even when a sibling file applied before the failure. Per-file
  evidence is corrected to the actual post-rollback filesystem state
  (`correctEvidenceAfterRollback`), so no evidence overclaims a mutation.
- **Success** → `AggregateMutationOutcome`: any `changed` → `changed`;
  otherwise any `created` → `created`; otherwise any `nochange` → `nochange`.
  A batch never reports `success` merely because one file changed.
- Rollback restores every recorded snapshot (pre-existing `engine.Transaction`
  behavior preserved).

## 10. False-positive paths discovered and fixed

| Site | Path | False positive | Fix |
|---|---|---|---|
| `patch.go recordMutationEvidence` | changed/created | fabricated `VerificationRun/Passed=true` even with no gate (FILE_CREATE never runs the gate) | gate facts threaded via `applyFacts` |
| `patch.go` no-change evidence | nochange | `ApplyExecuted=false` hid that the apply ran | `ApplyExecuted=true`, `FilesystemChanged=false` |
| `executor.go Approve` failure | multi-file rollback | aggregate could read `changed` for a rolled-back transaction | always a failure outcome + post-rollback evidence correction |
| `executor.go Approve` success | multi-file | outcome = last non-`no_artifact` (order-dependent) | `AggregateMutationOutcome` |
| `executor.go Approve` | verification | post-commit `RunAll()` re-run after the mutation was already committed | gate report captured on the MutationSet |
| `executor.go Execute` | pending approval | `Proof.Outcome=no_artifact` for a valid held artifact | `pending_approval` |
| `executor.go Reject` | rejection | `cancelled` | `rejected` + `approval.rejected` event |
| `executor.go invokeMutation` | artifact | no validation before approval | V3 artifact gate |
| `executor.go finalizeResult` + UI | usage | `Known` dropped; UI inferred known / hardcoded `true` | `Known`/`Cached`/`Reasoning` survive; UI reads `Completed.Known` |
| UI `executionResultUpdate` | no-change | fell through to "Completed — nothing produced." | distinct no-change terminal |
| UI `recordRuntimeProof` | proof view | `verify_failed` not marked rolled back | included |

Audited and left as-is (not false positives on the authority path): legacy
flag-off `applyEvidence` (already byte-compares disk), undo/agent loop
success flags (derived from real restore/step results), `recordLedgerAndSummarize`
marking no-change plan tasks completed (the apply executed), and mode-engine
`StageOutput{Success:true}` for dormant non-production paths.

## 11. Tests added / updated

New:
- `internal/execution/execution_truth_matrix_test.go` — 16 cases (below).
- `internal/ui/runtime_truth_projection_test.go` — 4 UI projection cases.

Updated (pinned the corrected contract):
- `internal/ui/engine_first_test.go` — pre-approval outcome is `pending_approval`,
  never a claimed mutation.
- `internal/ui/engine_graph_test.go` — same.
- `internal/ui/mutationset_test.go` — with no verifier attached, evidence must
  NOT claim verification passed (the old fabricated `Verify()` assertion).

## 12. Test results

| Command | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `golangci-lint run ./...` | **0 issues** |
| `go test ./... -count=1` | all packages pass |
| `go test -race ./internal/{execution,ui,events,modes/build}/...` | clean |
| `go test ./internal/execution/ -run TestTruthMatrix -count=1` | 16/16 pass |
| `go test ./internal/ui/ -run 'TestUITruth' -count=1` | 4/4 pass |

Truth matrix coverage: (1) valid mutation changed+verified; (2) no-change;
(3) empty artifact; (4) malformed artifact → `artifact_rejected`; (5) truncated
artifact never written; (6) ambiguous target stops before the model; (7)
approval rejected → `rejected` + `approval.rejected`; (8) apply failure; (9)
verifier failure rolls back → `verify_failed`; (10) provider failure; (11)
usage known; (12) usage unknown ≠ zero; (13) streaming usage; (14) non-streaming
usage (the known-usage case exercises the Execute fallback); (15) multi-file
partial failure rolls back all + evidence corrected; (16) multi-file rollback;
(17) execution cancellation; (18) successful verification gate (covered by
cases 1/9); (19) canonical event ordering `mutation.started →
mutation.completed → verification.completed → execution.finished`; (20) UI
result projection (no-change/rejected/usage-known/usage-unknown).

## 13. Remaining truth gaps

1. **Legacy flag-off rollback path** — all Phase 2 fixes apply to the
   RuntimeExecutor authority path. The flag-off path (mode engines →
   `applyPatchWithDeadline`) is preserved as the rollback boundary and retains
   its pre-Phase-1 truth discipline; it is un-wired once the flag becomes the
   default (Phase 3).
2. **Prose → new-file fallback** — `invokeMutation` still treats a prose
   response as full content when extraction yields nothing and the trimmed
   response is non-empty. Registered-language new files are protected by the
   artifact gate; unregistered-language new files rely on the apply-time
   truncation guard (`<3` lines). A generic prose filter was deliberately not
   built (mandate: no generic filter-everything layer).
3. **Mixed known/unknown usage** — a multi-invocation execution with one
   unknown-usage invocation reports `Known=true` with the partial aggregate of
   the known invocations. The counts are real billing; the total may be
   partial. Acceptable, documented.
4. **FILE_CREATE path bypasses the verification gate** (pre-existing) — created
   files are written without a workspace verification run; evidence honestly
   reports `VerificationRun=false`.
5. **`recordLedgerAndSummarize`** marks a no-change apply's plan task
   Completed (legacy path). The apply executed; acceptable.

## 14. Phase 2 invariants

| # | Invariant | Status |
|---|---|---|
| 1 | Model responses are not artifacts until the extraction/validation boundary | **PROVEN** — `artifactGate` runs on every executor mutation artifact; malformed artifacts are rejected before approval (`TestTruthMatrix_MalformedArtifactRejected`). |
| 2 | Empty/malformed/truncated artifacts cannot reach approval or mutation | **PROVEN** — empty-artifact failure (`TestTruthMatrix_EmptyArtifactFails`), artifact gate, apply-time truncation/ambiguity guards (`TestTruthMatrix_TruncatedArtifactRejected`). |
| 3 | A mutation is reported changed only on actual post-apply evidence | **PROVEN** — byte compare at apply; post-rollback evidence correction (`TestTruthMatrix_ValidMutation`, `..._MultiFilePartialFailureRollsBackAll`). |
| 4 | `old == new` ⇒ `OutcomeNoChange`, never changed/success | **PROVEN** — `final == patch.Original` check + `TestTruthMatrix_NoChangeMutation`; aggregate derives `nochange`. |
| 5 | Apply truth: the runtime knows whether apply executed | **PROVEN** — `applyFacts.executed`; no-change records `ApplyExecuted=true`; failed applies record false (`TestTruthMatrix_NoChangeMutation`, `..._ApplyFailure`). |
| 6 | Verification is associated with the mutation; never fabricated | **PROVEN** — gate report captured on the MutationSet; evidence `verifyRun/verifyPassed` are gate facts; `TestTruthMatrix_VerifierFailureRollsBack`, updated `mutationset_test.go`. |
| 7 | Failure at any authoritative stage propagates as failure | **PROVEN** — apply/verify/artifact/provider failures all yield non-success outcomes (`TestTruthMatrix_*` failure cases). |
| 8 | Successful mutations carry reconstructable proof | **PROVEN** — `ExecutionProof` carries targets, evidence (pre/post, apply, changed, outcome), verification report, invocation usage, transaction ID, graph timeline. |
| 9 | Events describe real runtime transitions | **PROVEN** — graph-driven events; `verification.completed` from the real gate; `approval.rejected` on rejection; ordering test (`TestTruthMatrix_CanonicalEventOrdering`). |
| 10 | UI projects runtime truth, never independently infers | **PROVEN** — no-change/rejected/usage-known branches key off `ExecutionResult`; `TestUITruth_*`. |

## 15. Changes intentionally deferred to Phase 3/4

- Flipping `IZEN_RUNTIME_EXECUTOR` to enabled-by-default (mandate: do not flip).
- Deleting the flag-off rollback path (`runBuildFastTrack` direct provider
  sites, `proposeHotfixPatch`/`proposeBuildPatch`/`proposeMultiHotfixPatch`,
  `applyPatchWithDeadline`, `handleMessageContent` reclassification,
  `fastTrackFileContext` full-file injection) — one-way door after soak.
- Removing dormant infrastructure (`router.Router`, `timeline.Timeline` read
  accessor, `runtime.LedgerBuilder`, `modes/build` Executor/ApplyMutation,
  `PipelineRunner.ExecuteBuild`, `PatchQueue`/`StreamMonitor`, `strategy.Compile`).
- Routing the `.izen/audit/mutations.log` write exclusively through
  `internal/events/audit` (guardrail currently parses the log).
- A generic prose-vs-content filter for unregistered-language new files
  (mandate: no generic filter layer; needs a strategy-owned contract).
- Retiring `pkg/event` (7-type `izen run` product bus) or documenting it as a
  separate product stack.
