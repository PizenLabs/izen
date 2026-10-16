# IZEN — Final Runtime Convergence

Date: 2026-10-09 · Branch: `fix/execution` · HEAD: `09636b0134d515acb2c06fbea30c9dd4eb7bf2fd`

Working tree: **not clean** — the Phase 3 convergence change set plus this pass's
corrections are uncommitted (no commit was created; none was authorized).

Evidence vocabulary, unchanged from Phases 1–3:

* **VERIFIED** — proven by a command that ran on this working tree, or by a live
  local-provider run whose artifacts are quoted.
* **OBSERVED** — read in the current source with a citation.
* **REMAINING** — a known deviation, disclosed and not claimed converged.

---

## 1. Verified initial repository state

| Fact | Value |
|---|---|
| Branch | `fix/execution` |
| HEAD | `09636b0134d515acb2c06fbea30c9dd4eb7bf2fd` (`fix(exec): canonical artifact approval`) |
| Parent | `041a344` (`feat(exec): phase 2 — CLI proposal lifecycle converges on Driver`) |
| Remotes | `main` = `f7fc26a`; no push performed |
| Working tree | modified/deleted/untracked as listed in §11; no reset, clean, or revert was applied |

The starting tree already contained a Phase 3 change set (documented in the
untracked `docs/report/IZEN_EXECUTION_TOPOLOGY_CONVERGENCE_PHASE3.md`). That work
was treated as user work and preserved. This report does **not** treat the Phase
3 report as ground truth; its material claims were re-derived from source and
re-executed where feasible.

Baseline commands run **before** any correction:

| Command | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./internal/execution/ ./internal/changeset/ ./internal/architecture/ ./internal/cli/ ./internal/app/ ./internal/runtime/compose/ ./internal/runtime/autonomy/ ./test/architecture/ -count=1` | all `ok` |
| `go test ./... -count=1` | exit 0 — 212 packages `ok`, 29 with no test files, 0 `FAIL`, 0 panics |

---

## 2. Actual execution topology

One production lifecycle per headless command, shared by every migrated surface
(OBSERVED, `cmd/izen/main.go`, `cmd/izen/authoritative.go`):

```text
cmd/izen
  run        → runRuntimeCommand    ─┐
  prompt     → runOrchestrateCommand ├→ runAuthoritativeCommand (cmd/izen/authoritative.go)
  orchestrate→ runOrchestrateCommand ┘
        → compose.Wire                       (internal/runtime/compose)
        → admission / preflight dispatch     (app.Runtime.Execute SubmitPromptCmd)
        → autonomy.Driver                    (internal/runtime/autonomy)
        → ExecutorAdapter → RuntimeExecutor  (internal/execution)
        → authorized capabilities → mutation boundary → verification
        → durable journal (.izen/runtime/ledger.ndjson) + audit (.izen/audit)
        → cli.RunAuthoritative (human boundary) → terminal projection
```

* `izen run` parses `-dir`/repeatable `-target`/one prompt and calls
  `runAuthoritativeCommand(dir, prompt, "$run", "run", targets)`
  (`cmd/izen/runtime.go:46`).
* `izen prompt` / `izen orchestrate` call the same helper with `$orchestrate`
  (`cmd/izen/orchestrate.go:31`).
* The retired `internal/app.Pipeline` lifecycle and the bespoke
  `cmd/izen/runtime_ledger.go` are off every command path. No production
  `cmd/izen` file imports `internal/app` (AST lock, and `grep` found none).
* `cmd/izen/main.go:45` references `gate.NewPipeline` / `orchestrator.NewLoop`
  only as compile-time DI references (never called); they are not an execution
  bypass.

Terminal truth is the Driver's. `runAuthoritativeCommand` switches on
`res.Termination.State` (`RuntimeCompleted` → success, `RuntimeUnsubstantiated`
→ truthful non-proof, default → error) and only materializes the derived
`snapshot.json` from the journal at finalization (`ledger.Checkpoint`).

---

## 3. Critical invariants and their enforcing components

| Invariant | Enforcing component | Evidence |
|---|---|---|
| Runtime owns authority/state, not the LLM | `autonomy.Driver`, `ObjectiveCompletionAuthority` | OBSERVED; tests §6 |
| Model output is untrusted input, never authorization | `artifactBinder` (fail-closed path/context binding), `authorize` callback | OBSERVED; `binding_test.go`, `TestPhase1_ModelOutputCannotAuthorize` |
| One authoritative mutation path / journal | `compose.Wire` `TaskStore`; `internal/app` off CLI path | OBSERVED; `TestExecutionOwnership_CLIProductionFilesDoNotImportApp` |
| Approval binds the exact canonical mutation | `AuthorizeBuildCandidateContent` (CandidateID+CandidateDigest) + `Approve` re-check + **`Patch.Resolved`** | VERIFIED: `TestArtifactIntegrity_PatchAppliedBytesAreTheReviewedArtifact`; live patch run §7 |
| Preview bytes == authorized digest == applied bytes | `Patch.Resolved` writes staged bytes verbatim; digest over `Patch.Modified` | VERIFIED (this pass's correction) |
| Terminal state from runtime evidence | `ObjectiveCompletionAuthority.Evaluate`; `ledgerTerminal` maps only PROVEN→COMPLETED | OBSERVED; tests §6; live deny run §7 |
| Capability grants explicit and bounded | `GrantDefault(read/analyze/propose/mutate/verify)` + checkpoint-gated `AuthorizeBuildCandidateContent` | OBSERVED; `internal/cli/authoritative_test.go` |

---

## 4. Findings

### F1 — CRITICAL, confirmed, corrected: approved/reviewed bytes could differ from applied bytes on the ordinary PATCH path

**Where.** `internal/execution/executor.go` staging + `internal/execution/patch.go` `apply`.

**Root cause.** The executor resolves a payload to the final apply-ready content
at STAGING (`Patch.Modified`), and the preview diff and the authorization digest
bind exactly those bytes (`CandidatePreview.ArtifactDigest = sha256Hex(Modified)`,
`CandidatePreview.Digest`). But the staged `Patch` was **not** marked resolved,
so `PatchManager.apply` re-ran its own protocol cascade for every patch that was
not an addressed envelope:

```
SanitizeLLMResponse(Modified) → SplitAndFilterPatches → resolvePatchContent
```

`SanitizeLLMResponse` strips any line matching a hallucinated prefix (`FILE:`,
a lone ```` ``` ````, `>>>>>>> END_FILE`, …). A legitimate reviewed line that
matched one was removed before the write. The Phase 3 correction had fixed the
*description* of the mutation (diff compiled from `Patch.Modified`, and the
envelope case), but not the *applied bytes* for the non-envelope path.

**Evidence (deterministic, reproduced).** A 40-line target, a SEARCH/REPLACE
whose REPLACE side is `FILE: keep me`:

```
preview.ArtifactDigest = a3d842b0…   (content still contains "FILE: keep me")
res.Content            contains "FILE: keep me"
applied contains "FILE: keep me"  = false
applied digest         = d7e2d534…   != reviewed digest a3d842b0…
```

In the small-file variant the same defect surfaced as a hard apply failure of an
already-approved, valid candidate (`ambiguous snippet … for existing file`).

**Correction.** Added `Patch.Resolved` (`internal/execution/patch.go`), set by
the executor for every staged patch (`internal/execution/executor.go:4102`).
`apply` now writes a resolved patch's `Modified` **verbatim**, using the existing
resolver only as a pure safety validator (ambiguous snippet / raw protocol
residue / truncation) — never as a rewriter. Raw payloads staged by legacy
`PatchManager` callers leave `Resolved` unset and keep the old cascade.

**Regression.** `TestArtifactIntegrity_PatchAppliedBytesAreTheReviewedArtifact`
(`internal/execution/artifact_approval_integrity_test.go`).

### F2 — MATERIAL, confirmed, partially corrected: workspace-independent direct answers

**Where.** `DeriveTaskContract` + `authorRuntimeConditions`
(`internal/execution/objective_authority.go`, `objective_contract.go`).

**Root cause.** Every read-only objective required a workspace observation, so a
request that has no workspace (a greeting) could never be PROVEN. The
`conversation` intent is the one intent the classifier marks as
`RequiresWorkspace() == false` (`internal/autonomy/intent.go:56`), yet its
contract still demanded `cond-observed`.

**Correction (implemented and tested).** A `conversation` intent now derives a
`TaskRead` contract with `RequiresResponse=true`,
`RequiresObservation=false`, and authors only the `cond-response-delivered`
obligation. Evidence remains runtime-owned: `ResponseProduced` is set from the
delivered bytes (`ObjectiveEvidence` built from `res.Content != ""`), never from
a model self-report. The clause is scoped to `conversation` only; explanation /
investigation / review objectives still require an observation and still fail
closed without one.

**Live.** `izen run "hello"` → `completed approved=false reason="objective
satisfied: completed; objective PROVEN by evidence"`.

**Regression.** `TestConversationContractNeedsNoWorkspaceObservation`
(execution, contract level) and
`TestConversationTerminatesProvenWithoutWorkspaceObservation` (autonomy, driver
level).

**REMAINING (F2b).** A general workspace-independent *question* is classified
`explanation`, not `conversation` (e.g. the Phase 3 example "say hello in one
short sentence"). It still terminates `UNSUBSTANTIATED`, which is the honest
state given the current contract. See §10 for the precise blocker.

### F3 — LOW, disclosed, not corrected: nested-marker `FILE_CREATE` boundary

A `FILE_CREATE` body whose own line is exactly `>>>>>>> END_FILE` terminates the
envelope there. Canonical bytes and applied bytes agree (both the truncated
body); it is a transport-format limitation, not an authorization gap. Unchanged
from Phase 3.

### F4 — LOW, disclosed, not corrected: preview metric counts diff headers

`preview.AddedLines/RemovedLines` counts `+++`/`---` via `countDiffLines`, so a
preview can read one higher than the mutation-boundary metric. Cosmetic; the
digest binds the applied bytes. Unchanged from Phase 3.

### F5 — INFORMATIONAL: `Patch` serialization includes the new marker

`Patch.Resolved` is `json:"resolved,omitempty"`. It survives the durable patch
record and a resume/restart, so an approved patch still applies verbatim after a
process restart.

---

## 5. Corrections and regression tests

| Correction | File(s) | Regression |
|---|---|---|
| F1 verbatim apply | `internal/execution/patch.go`, `internal/execution/executor.go` | `TestArtifactIntegrity_PatchAppliedBytesAreTheReviewedArtifact` |
| F2 conversation contract | `internal/execution/objective_authority.go`, `internal/execution/objective_contract.go`, `internal/runtime/autonomy/objective_completion.go`, `internal/runtime/autonomy/objective_lifecycle.go` | `TestConversationContractNeedsNoWorkspaceObservation`, `TestConversationTerminatesProvenWithoutWorkspaceObservation` |

Both corrections are the smallest coherent change that restores the invariant at
the existing owner. No second execution loop, authority owner, or mutation path
was introduced. Phase 3's envelope/integrity tests pass unchanged.

---

## 6. Behavioral matrix

Evidence type: **U** = unit/regression, **I** = integration/end-to-end,
**L** = live local provider, **M** = manual source inspection.

| Scenario | Required property | Result | Evidence |
|---|---|---|---|
| Direct conversational question (`conversation` intent) | truthful completion, no mutation | PASS | U `TestConversationTerminatesProven…`; L `izen run "hello"` → PROVEN/COMPLETED |
| Direct question classified `explanation`, no workspace | truthful response or correctly classified limitation | LIMITATION | L `izen run "say hello…"` → UNSUBSTANTIATED (F2b) |
| Read-only investigation | correct evidence, no mutation | PASS | U `evaluateRead/Review`, `TestConversationContract…` guard clause; I autonomy suite |
| Plan-only request | no unintended execution | PASS | U/I `acceptance_matrix_test.go` |
| Create a new file | correct bytes, approval, verification | PASS | L CREATE run §7; U `TestArtifactIntegrity_FileCreate*` |
| Modify an existing file | exact approved artifact applied | PASS | U `TestArtifactIntegrity_PatchAppliedBytesAreTheReviewedArtifact`; L PATCH run §7 |
| Ambiguous target | clarification, no guessed mutation | PASS | U `scope_ambiguity_test.go` |
| Multiple candidate targets | no implicit authorization expansion | PASS | U `TestAcceptance_UnauthorizedApprovalLeavesZeroDelta` |
| User denies approval | no mutation | PASS | I `TestAuthoritativeRejectNeverAuthorizes`; L deny run §7 |
| Provider failure | truthful failure state | PASS | U `r6c_provider_failure_test.go`, `TestModelFailureProducesNoArtifact` |
| Verification failure | never falsely proven | PASS | U `TestTruthMatrix_VerifierFailureRollsBack` |
| Resume after clarification | fresh request-derived decisions | PASS | U `clarification_invalidation_test.go` |
| Resume after interruption | no unauthorized duplicate mutation | PASS | U `internal/runtime/durable/*` |
| Explicit `-target` | correct target, bound through resolution | PASS | U `TestSetTargetsBindsTheNextRun…`, `TestParseRunArgs` |
| Unrelated workspace files | unchanged unless authorized | PASS | U/L `keep.md` byte-identical in CREATE/PATCH/deny runs |

Unsupported in this pass: live `$hot` / `/plan` / `/build` UI surfaces (fenced,
unchanged from Phases 1–2); no live provider run was performed for them.

---

## 7. Build, vet, test, race, and live-execution results

Deterministic (VERIFIED, this working tree):

| Command | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `gofmt -l internal/execution internal/runtime/autonomy cmd/izen` | empty |
| `go test ./... -count=1` (after all corrections) | exit 0 — 212 `ok`, 29 no-test-files, 0 `FAIL`, 0 panics |
| `go test -race -count=1 ./internal/execution/ ./internal/runtime/autonomy/ ./internal/cli/ ./internal/runtime/durable/ ./internal/architecture/` | exit 0, no data race |
| `go test ./internal/execution/ -run TestArtifactIntegrity -count=1` | 15/15 `PASS` |
| `go test ./internal/runtime/autonomy/ -run TestR5_CaseA_EndToEndMutationReachesProven -count=5` | `ok` (previously-reported flake did not reproduce) |

Live local provider (VERIFIED): Ollama `qwen2.5-coder:7b`, isolated `mktemp` git
repos, `printf 'y\n' | ./bin/izen run -dir <tmp> "<prompt>"` with a freshly built
binary from this tree.

* **CREATE** — `"Create a new file named hello.md with the content 'Hello everyone'."`
  * proposal `targets=[hello.md] operation=CREATE (+1 -0)`; diff `+Hello everyone`
  * approval `y` → terminal `run: run-1 completed approved=true reason="objective satisfied: created; objective PROVEN by evidence"`
  * artifacts: `hello.md = "Hello everyone\n"` (`od -c`); `keep.md` byte-identical;
    `.izen/runtime/snapshot.json` task `status: COMPLETED`;
    `.izen/patches/run-1-patch-1.json` `"modified":"Hello everyone\n"`,
    `"canonical":true`, `"resolved":true`; audit `objective.evaluated state=PROVEN granted=true`
* **PATCH** — `"change 'world' to 'everyone' in @greet.txt"`
  * proposal `operation=UPDATE (+2 -2)`; preview `-Hello world / +Hello everyone` (no envelope)
  * terminal `run: run-1 completed approved=true reason="objective satisfied: changed; objective PROVEN by evidence"`
  * artifacts: `greet.txt = "Hello everyone\n"`; `keep.md` unchanged; snapshot `COMPLETED`; `objective.evaluated state=PROVEN`
* **Conversation** — `"hello"` → `run: run-1 completed approved=false reason="objective satisfied: completed; objective PROVEN by evidence"`, audit `state=PROVEN granted=true`
* **Explanation (limitation)** — `"say hello in one short sentence"` → `unsubstantiated … completion condition "cond-observed" is unmet`, audit `state=UNSUBSTANTIATED granted=false`
* **Denial** — PATCH prompt answered `n` → `greet.txt` byte-identical (`Hello world\n`), snapshot task `status: FAILED`, process exit 1

No paid API was used; no credentials were assumed; no evidence was fabricated.

---

## 8. Artifact-integrity evidence

For the corrected ordinary-PATCH scenario the five required artifacts agree
(VERIFIED):

| Artifact | Value |
|---|---|
| Candidate identity | held `Patch` id (preview `CandidateID == PatchID`) |
| Exact preview bytes | preview diff compiled from `Patch.Modified` (`changeset.UnifiedDiffBetween`) |
| Authorization-bound digest | `sha256(Patch.Modified)` carried as `CandidateDigest`, re-checked in `Approve` |
| Applied bytes | `Patch.Modified` written verbatim (`Patch.Resolved`) |
| Post-apply verification | per-target gate runs inside the mutation boundary |
| Unrelated files | unchanged (`keep.md` byte-identical) |
| Durable objective state | `objective.evaluated state=PROVEN`; task `COMPLETED` |

The deterministic `TestArtifactIntegrity_PatchAppliedBytesAreTheReviewedArtifact`
asserts `applied == res.Content`, the reviewed line is present in the applied
file, and `sha256(applied) == preview.ArtifactDigest`. The Phase 3 tests
(`…MalformedPatchEnvelopePreviewIsTheAppliedArtifact`,
`…FileCreateAgainstExistingTargetIsCanonical`) and the other 13 integrity tests
still pass.

---

## 9. Objective-state and recovery semantics

Canonical terminal vocabulary (OBSERVED, `objective_authority.go`):
`PROVEN`, `UNSUBSTANTIATED`, `FAILED`, `REQUIRES_AUTHORIZATION`; projected to
loop states `RuntimeCompleted` / `RuntimeUnsubstantiated` / abort / human
boundary. Only `PROVEN` authorizes completion.

| Request category | Contract | Proof obligation | Terminal |
|---|---|---|---|
| Conversation | READ | delivered response | PROVEN / UNSUBSTANTIATED |
| Direct question (`explanation`, no workspace) | READ | response **and** workspace observation | UNSUBSTANTIATED (F2b) |
| Read / investigation / review | READ/REVIEW | observation + response | PROVEN / UNSUBSTANTIATED |
| Planning (no execution) | READ/REVIEW | observation + response | PROVEN / UNSUBSTANTIATED |
| Create / modify / delete | CREATE/PATCH/DELETE | artifact + durable mutation (+verifier) | PROVEN / UNSUBSTANTIATED / FAILED |
| Already satisfied | IDEMPOTENT | structural NO-OP confirmation | PROVEN / UNSUBSTANTIATED |
| Ambiguous | human clarify | target decision | parked / AWAITING_HUMAN |
| Blocked authorization | REQUIRES_AUTHORIZATION | human decision | AWAITING_HUMAN |
| Provider error / refusal | FAILED | — | FAILED |
| Truncation | UNSUBSTANTIATED | — | UNSUBSTANTIATED |
| Cancellation | UNSUBSTANTIATED | — | UNSUBSTANTIATED |

Durability (`internal/runtime/autonomy/ledger.go`): `ledgerTerminal` records
`RuntimeCompleted` as `RecordTerminalResult(ok=true)` → `TaskCompleted`; every
other terminal state records a failure classification and
`RecordTerminalResult(ok=false)` → `TaskFailed`; a parked run is `PAUSED`, not
terminal. The CLI's `snapshot.json` is materialized from that journal, so a
failed/unsubstantiated run cannot read `COMPLETED` (VERIFIED by the live deny
run: task `FAILED`). Audit flush failure invalidates success without rolling back
a committed mutation (`runAuthoritativeCommand`).

Crash consistency limitation (honest): rollback is the `MutationSet` shadow
backup within one apply transaction; there is no multi-process distributed
transaction. Interrupted tasks replay non-terminal from the journal
(`internal/runtime/durable/*`).

---

## 10. Remaining limitations and explicit non-goals

1. **F2b — general workspace-independent direct answers.** A non-`conversation`
   read-only request with no workspace (e.g. a general knowledge question
   classified `explanation`) still requires an observation and terminates
   `UNSUBSTANTIATED`. **Precise blocker:** the deterministic discriminator that
   exists is the *dispatched strategy* (`strategy.DirectResponse` with
   `ContextPolicyNone`), not the intent label; the intent label `explanation`
   also covers workspace-dependent explanations ("explain @main.go"), so keying
   the workspace-independent contract on the intent would risk proving an
   un-observed workspace explanation. Threading the dispatched strategy into
   both `DeriveTaskContract` and the *cached* `DeriveObjectiveContract`
   (whose conditions author `cond-observed`) is a lifecycle-ordering change:
   the objective contract is derived/cached by the Driver before the strategy is
   guaranteed authoritative. Recommended decision (not implemented): add a
   `WorkspaceIndependent` input sourced from the resolved strategy and refresh
   the objective contract's authored conditions when the strategy is a
   zero-context direct answer — without weakening the observation obligation for
   any strategy that admitted workspace/repository material.
2. `izen run` is interactive at the approval boundary (runtime contract).
3. The bespoke "V3 pipeline audit trail" stdout block is gone; the audit of
   record is `.izen/audit/events.ndjson`.
4. `internal/app.Pipeline` remains in the tree, unreachable from production CLI;
   retained as the rollback boundary and for its own tests.
5. Derived-view materialization happens at CLI finalization; recovery is
   journal-driven.
6. Nested-marker `FILE_CREATE` boundary (F3) and preview metric counts (F4).
7. Unchanged from Phases 1/2: `internal/cli.Stack`+`RunCycle`, the UI `/ask`,
   `/plan`, `/build`, `$hot` surfaces, secondary provider callers and budget
   bypass resolvers remain fenced and were not converged in this pass.
8. **Not verified in this pass:** live `$hot`/UI mutation runs; live runs for
   providers other than local Ollama; multi-process crash interleavings.

Non-goals: kernel rewrite, package reorganisation, UI redesign, new feature
implementation, commits, pushes.

---

## 11. Final repository state and changed-file list

HEAD unchanged: `09636b0134d515acb2c06fbea30c9dd4eb7bf2fd`. No commit created.
Working tree not clean (by design — existing user changes preserved).

Pre-existing (Phase 3, carried in the working tree when this pass began):

```
 M cmd/izen/orchestrate.go
 M cmd/izen/runtime.go
 D cmd/izen/runtime_ledger.go
 D cmd/izen/runtime_ledger_test.go
 M internal/architecture/execution_ownership_convergence_test.go
 M internal/changeset/compiler.go
 M internal/execution/artifact_approval_integrity_test.go
 M internal/execution/executor.go
 M internal/runtime/autonomy/behavior_test.go
 M internal/runtime/autonomy/driver.go
 M internal/runtime/autonomy/objective_completion.go
?? cmd/izen/authoritative.go
?? cmd/izen/runtime_test.go
?? docs/report/IZEN_EXECUTION_TOPOLOGY_CONVERGENCE_PHASE3.md
?? internal/runtime/autonomy/target_statement_test.go
```

Added/modified by this pass:

```
 M internal/execution/patch.go                     F1 verbatim apply
 M internal/execution/executor.go                  F1 mark staged patches Resolved
 M internal/execution/artifact_approval_integrity_test.go   F1 regression
 M internal/execution/objective_authority.go       F2 conversation contract
 M internal/execution/objective_contract.go        F2 Intent input + condition
 M internal/execution/objective_contract_test.go   F2 contract-level regression
 M internal/runtime/autonomy/objective_completion.go  lifecycleIntent()
 M internal/runtime/autonomy/objective_lifecycle.go   pass Intent
?? internal/runtime/autonomy/conversation_contract_test.go  F2 driver-level regression
?? docs/report/IZEN_FINAL_RUNTIME_CONVERGENCE.md   this report
```

---

## 12. Verdict

**CLOSED WITH EXPLICIT LIMITATIONS.**

* Exactly one authoritative production execution lifecycle: **yes** (§2, AST
  locks).
* Any production mutation bypasses the authorization boundary: **no** — apply is
  denied without a token (`checkAuthorization`), and the CLI always issues a
  candidate-bound token.
* Approval bound to the exact canonical mutation applied: **yes** (§8; F1
  corrected).
* Stale state / clarification reusing an obsolete authorization: **no** —
  `Approve` re-checks candidate identity and digest against the held preview.
* Any failure path producing `PROVEN`: **no** — only
  `ObjectiveCompletionAuthority.PROVEN` maps to `COMPLETED`.
* Objective completion meaningful per category: **yes, except** the disclosed
  workspace-independent direct-answer gap (F2b).
* Durable state reflects actual outcomes: **yes** (§9; live evidence).
* Critical invariants enforced by tests/architecture locks: **yes** for the
  corrected invariant; locks exist for the single lifecycle.
* Remaining deviations explicit and bounded: **yes** (§10).
* Repository state reproducible and change set reviewable: **yes** (§11; all
  commands above re-runnable).

The one material gap (F2b) is a product-contract decision with a stated blocker
and recommended design, not an unresolved correctness, authorization, or
durability defect. A negative answer to a critical safety/correctness invariant
does not remain.
