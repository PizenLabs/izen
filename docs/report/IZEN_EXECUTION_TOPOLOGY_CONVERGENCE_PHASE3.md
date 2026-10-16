# IZEN — Execution Topology Convergence, Phase 3

Date: 2026-10-09 · Branch: `fix/execution` · HEAD at start: `09636b0`
Reported baseline: `041a344`, branch `fix/execution`, clean tree.

Distinctions used throughout, unchanged from Phases 1/2:

* **VERIFIED** — proven by a test that ran on this working tree, or by a live
  provider run.
* **OBSERVED** — read in the current source, with a citation.
* **REMAINING** — a known deviation, disclosed and not claimed converged.

---

## 1. Method and baseline

1. `git status` → clean tree; branch `fix/execution`; `git rev-parse HEAD` →
   `09636b0134d515acb2c06fbea30c9dd4eb7bf2fd`. This is `041a344`
   (`feat(exec): phase 2 — CLI proposal lifecycle converges on Driver`) plus
   `09636b0` (`fix(exec): canonical artifact approval`), i.e. the reported HEAD
   plus the Phase 2.1 correction.
2. Re-read `docs/report/IZEN_EXECUTION_TOPOLOGY_CONVERGENCE_PHASE2.md` and
   `docs/report/IZEN_ARTIFACT_APPROVAL_INTEGRITY.md`.
3. Traced the complete `izen run` path by exact source reads (not by shared
   helper names): CLI parse → lifecycle owner → provider seam → mutation
   boundary → terminal result.
4. Ran the baseline affected packages before editing (all `ok`).
5. Implemented the migration, then re-ran focused, affected, architecture,
   full-suite, vet and build checks, plus three live local-provider runs.
6. The codebase-memory graph was **not** used for final claims (its snapshot
   predates the changed files); every material claim below is source-verified
   and, where a lifecycle transition matters, test- or live-verified.

Final change set (uncommitted at report time):

```
 cmd/izen/authoritative.go        (new)  shared authoritative CLI lifecycle
 cmd/izen/runtime.go              rewrite: thin adapter, keeps provider build
 cmd/izen/runtime_ledger.go       DELETED (obsolete headless ledger)
 cmd/izen/runtime_ledger_test.go  DELETED
 cmd/izen/orchestrate.go          delegates to the shared lifecycle
 cmd/izen/runtime_test.go         (new) argument-grammar tests
 internal/runtime/autonomy/driver.go             SetTargets (explicit target seam)
 internal/runtime/autonomy/objective_completion.go  $run scope provenance
 internal/runtime/autonomy/behavior_test.go      $run provenance case
 internal/runtime/autonomy/target_statement_test.go (new)
 internal/execution/executor.go                  canonical envelope + applied-bytes diff
 internal/execution/artifact_approval_integrity_test.go  2 new regression tests
 internal/changeset/compiler.go                  UnifiedDiffBetween
 internal/architecture/execution_ownership_convergence_test.go  Phase 3 locks
```

---

## 2. Phase A — audit of the `izen run` path (OBSERVED)

### 2.1 The path before this change

```text
izen run  (cmd/izen/main.go:119)
  → runRuntimeCommand (cmd/izen/runtime.go:117)
      → newRunLedger (cmd/izen/runtime_ledger.go:68)   durable task under .izen/runtime
      → app.NewPipeline (runtime.go:198)               own generator + intent compiler
      → pipeline.Run (internal/app/pipeline.go:290)    for{} generate→extract→align→validate→repair→plan
          → ConcreteSubstrate.Execute → kernelbridge.Apply → runtime/kernel
      → publishRunLifecycle (runtime.go:436)           hand-built audit lifecycle
      → runLedger.Complete / .Fail (runtime_ledger.go)  committed-EXECUTION record
  ⇒ LIFE-OWNER: internal/app.Pipeline; terminal vocabulary: app.Result
```

`internal/app` was imported **only** by `cmd/izen/runtime.go` and
`cmd/izen/runtime_ledger.go` in production. The command owned its own durable
ledger, audit wiring, pipeline loop and terminal rendering.

### 2.2 The authoritative destination

```text
compose.Wire (internal/runtime/compose/compose.go:500)
  → autonomy.Engine + RuntimeExecutor + IntentGateway + AuthorizationEngine
  → runtimeAutonomy.NewDriver (compose.go:937)
       → Driver.Run (internal/runtime/autonomy/driver.go:587)
           → driver loop (driver.go:2146)
           → ExecutorAdapter.Execute (internal/runtime/autonomy/adapter.go:383)
               → RuntimeExecutor.Execute (internal/execution)
       → ObjectiveCompletionAuthority (objective_completion.go:199)
  ⇒ LIFE-OWNER: autonomy.Driver; terminal vocabulary: autonomy.LoopTermination
```

`izen prompt` / `izen orchestrate` already crossed this seam through
`internal/cli.RunAuthoritative` (Phase 2). This phase routes `izen run` through
the same seam.

### 2.3 Behavior matrix — `app.Pipeline` vs authoritative `Driver`

Not inferred from shared helper names; each row was read in the cited owner.

| # | Dimension | `app.Pipeline` (retired `izen run`) | authoritative `Driver` |
|---|---|---|---|
| 1 | Objective derivation / target resolution | `Request{Intent, Targets}`; `Targets` passed verbatim; capability resolution by prompt keywords (`pipeline.go:781`) | `IntentGateway`/`Strategy.Select` resolves `@` targets; evidence-bound `DeriveScope`; ambiguity parks at a clarify boundary (`driver.go:699`, `adapter.go:257`) |
| 2 | CREATE vs PATCH | no approval-bound classification; `Mode` greenfield/brownfield; substrate `OpFileWrite`/`OpFileDelete` (`proposalFromTx`) | executor classifies operation from target pre-state (CREATE/UPDATE/DELETE); artifact contract kinds (create_file/replace_block/search_replace/full_file) (`executor.go` staging) |
| 3 | Scope / mutation authorization | substrate confinement + kernel; **no human approval**; scope only for ledger provenance (`runtime_ledger.go:113`) | session capability ledger (read/analyze/propose/mutate/verify), preflight admission gate, per-candidate authorization bound to `CandidateID`+`CandidateDigest`, checkpoint-gated (`orchestrate.go`/`authoritative.go` authorize fn) |
| 4 | Context discovery / provider sequencing | `IntentCompiler` semantic call, prompt builder, generation loop (`pipeline.go:310,394`) | read-only requirement pass, then preflight admission, then main generation; grant-gated context re-compilation (`driver.go:833`, `compose.go:985`) |
| 5 | Artifact extraction / repair | MarkdownFence+JSON extractors; `maxAttempts`/`maxRepairs`; Semantic Alignment Gate; capability validation (`pipeline.go:394-469`) | executor ingestion/parse/artifact binder/validator; recovery matrix retry/repair; contract-recovery circuit breaker (`runtime_loop.go:948`) |
| 6 | Human approval / rejection | none; only the ambiguity `Clarifier` (`runtime.go:206`) | `HumanBoundary` approval with the runtime's own candidate review; `y`/`i`/reject (`cli/authoritative.go`) |
| 7 | Checkpoint / rollback / cancellation | TxFS + substrate `MutationSet`; ctx timeout; rollback on failure | durable `TaskStore`; loop checkpoints; DAG rollback `RollbackAndVerify`; ctx cancellation gate (`driver.go:2270`); `Abort` |
| 8 | Verification / objective completion | capability validation gate only; no objective proof | `ObjectiveCompletionAuthority` + behavioral gate; terminal PROVEN / UNSUBSTANTIATED / ABORTED (`objective_completion.go`) |
| 9 | Token budgets / usage | no run budget; `maxTokens=0` (provider default) | per-invocation `max_output` from strategy; `RunTokenBudget`; aggregate usage (`runtime_loop.go:647`) |
| 10 | Error class / terminal result | `app.Result` + typed errors; ledger records a committed EXECUTION, explicitly *not* a proven objective (`runtime_ledger.go:164`) | `LoopTermination{State,Reason,Class}`; ledger terminal `VERIFICATION_RESULT` (`ledger.go:336`) |

The matrix is the basis of the migration contract. It is **not** a claim of
equivalence: rows 6, 8 and 9 are genuine behavioral differences and are treated
as contract changes below.

---

## 3. Artifact-integrity prerequisite

The brief asked whether the two limitations recorded in
`IZEN_ARTIFACT_APPROVAL_INTEGRITY.md` can violate authorization, overwrite
protection, or the invariant that approved content equals applied content, and
to fix/fence before migrating.

### 3.1 Exact nested-marker boundary (REMAINING, no approval gap)

A `FILE_CREATE` body whose own line is *exactly* `>>>>>>> END_FILE` terminates
the envelope at that line. Because the canonical body is resolved at staging and
`Apply` writes those exact bytes verbatim, the **approved bytes and the applied
bytes are the same (truncated) body**. It is a transport-format limitation, not
an authorization or approval-binding gap. No change; disclosed as before.

### 3.2 Unscoped `FILE_CREATE` against an existing target (VERIFIED — fixed)

This limitation **was reachable and did violate the invariant.** For an existing
target (`original != ""`), Phase 2.1's canonicalization was scoped to
`original == ""`, so a `FILE_CREATE` envelope aimed at an existing file staged
`modified = ResolveModifiedContent(original, raw)` = the **whole envelope** (no
SEARCH/`@@` markers), while `Patch.Canonical` was false, so the apply boundary
extracted the body. Two owners resolved two artifacts: the human reviewed the
envelope; the mutation wrote the body. The anchor check does not catch it
(`classifyAnchors` sees no SEARCH block and returns `(false, nil)`,
`artifact_step.go:357`).

**Fix (at the owner):** the staging canonicalization now applies whenever the
parsed artifact is a recognized transported envelope (`artifact.Structural`),
for a new **or** an existing target. `Patch.Canonical = true` makes `Apply` write
the body verbatim; the diff is derived from the body. The overwrite remains
gated by the normal authorization/approval boundary. Regression:
`TestArtifactIntegrity_FileCreateAgainstExistingTargetIsCanonical`.

### 3.3 PATCH bounded-envelope preview divergence (VERIFIED — found live, fixed)

While exercising `izen run` live, a third divergence surfaced on the **PATCH**
path. Phase 2.1 §2.2 asserted PATCH was clean, but that assertion holds only for
the full-artifact `ResolveModifiedContent` path. The **bounded-patch** path sets
`raw = verbatim` (the envelope) and compiled the preview/evidence diff with
`compileDiff(raw)` (`executor.go:4110`). `ExtractBoundedPatch` and the changeset
pipeline are two parsers of the same model output; on a SEARCH/REPLACE envelope
with stray blank lines around the markers they disagreed: the extractor resolved
the applied body while the changeset pipeline read the whole envelope as a
full-file replacement.

Live evidence before the fix (`izen run -dir <tmp> "change 'world' to 'everyone'
in @greet.txt"`):

```text
proposal ... operation=UPDATE (+2 -2)
--- a/greet.txt
+++ b/greet.txt
@@ -1,2 +1,7 @@
+<<<<<<< SEARCH
 Hello world
+=======
+Hello everyone
+>>>>>>> REPLACE
...
applied greet.txt = "Hello everyone\n"        ← the envelope was reviewed; the body was written
```

**Fix (at the owner):** the preview and the mutation-boundary evidence are
compiled from the **canonical bytes the apply will write** (`Patch.Modified`)
against the real baseline, via `changeset.UnifiedDiffBetween`
(`internal/changeset/compiler.go`). The raw-envelope re-parse remains only as a
fallback. Regression:
`TestArtifactIntegrity_MalformedPatchEnvelopePreviewIsTheAppliedArtifact`; live
re-run in §7.2.

No parsing heuristic was broadened and CREATE/PATCH applied semantics are
unchanged: only the *description* of the mutation was made equal to the bytes
the mutation writes.

---

## 4. Phase B — migration contract

> **Removed owner:** the `izen run` execution lifecycle —
> `internal/app.Pipeline` + `cmd/izen/runtime_ledger.go` + `publishRunLifecycle`.
>
> **Authoritative destination / admission seam:** `compose.Wire` →
> `autonomy.Driver` over `RuntimeExecutor`, reached through the CLI seam
> `internal/cli.RunAuthoritative`, sharing one production path with
> `izen prompt` / `izen orchestrate`.

**Behavior preserved (parity required):**

* `izen run [-dir <path>] [-target <path>]... "<prompt>"` — public syntax and
  one-prompt rule unchanged (tested in `cmd/izen/runtime_test.go`).
* Provider selection, config precedence and provider/model fail-fast binding
  (`buildActiveProvider`, `validateProviderModelBinding`) unchanged.
* An explicit `-target` set is bound through the same authoritative target seam
  as a human-named target; it is resolved, derived and admission-gated, and
  never bypasses resolution. New `Driver.SetTargets` (the push form of the
  clarification statement), consumed once per run.
* Cancellation is preserved and distinct from failure/success (ctx gate,
  `Driver.Abort`; `cli.RunAuthoritative` cancellation test).
* Approval, artifact integrity, authorization and unrelated-file preservation
  are owned by the runtime (see §3).
* Audit (`.izen/audit/events.ndjson`), durable execution ledger
  (`.izen/runtime/ledger.ndjson`) and session-start checkpoint semantics are
  preserved by the runtime; the CLI materializes the derived snapshot at
  finalization (see §5.2).

**Behavior intentionally corrected / rejected:**

* **Human approval is now required for a mutation.** `izen run` previously wrote
  through the substrate with no approval gate. On the authoritative runtime the
  per-candidate `[y/N]`/inspect boundary is mandatory; the command plays the
  human exactly as `izen orchestrate` does. This is the runtime contract, not a
  parallel loop.
* **The bespoke "V3 pipeline audit trail" stdout block is gone.** The audit of
  record is the runtime's own `events.ndjson`; the command renders the runtime's
  proposal and terminal projection. (The old printout was a lossy re-render.)
* **Direct-chat answers are not surfaced (REMAINING capability gap).** See §8.

**Rollback boundary:** revert `cmd/izen/authoritative.go`, `runtime.go`,
`orchestrate.go` and restore the deleted `runtime_ledger.go`; the runtime and
`internal/app` are untouched and their tests still pass. The runtime-side
additions (`SetTargets`, `$run` provenance, the two executor integrity fixes) are
small and independently justified.

**Files expected to change:** `cmd/izen/*`, `internal/runtime/autonomy/driver.go`,
`internal/runtime/autonomy/objective_completion.go`, `internal/execution/*`,
`internal/changeset/compiler.go`, `internal/architecture/*_test.go`.

---

## 5. Phase C — implementation

### 5.1 One authoritative CLI lifecycle (new `cmd/izen/authoritative.go`)

`runAuthoritativeCommand(dir, objective, scope, label, targets)` is the single
production wiring for every migrated headless surface:

1. `config.Load` + `buildActiveProvider` (fail closed without a provider).
2. `compose.Wire(WithRoot, WithConfig, WithProvider, WithAuditDir,
   WithLanguageID(detected))`.
3. `app.Autonomy.GrantDefault(read/analyze/propose/mutate/verify)` (admission
   grant; the per-candidate `[y/N]` remains a separate gate).
4. `app.Autonomous.SetTargets(targets)` when `-target` was given.
5. session-start checkpoint (`app.Execution.ShadowCP`).
6. preflight dispatch `app.Runtime.Execute(SubmitPromptCmd{Prompt, Mode:"build"})`.
7. `cli.RunAuthoritative(ctx, app.Autonomous, terminalBridge, authorize, scope, objective)`.
8. snapshot finalization + `app.FlushAudit`; render the runtime terminal.

`runOrchestrateCommand` and `runRuntimeCommand` now only parse arguments and call
this helper (`$orchestrate` / `$run` respectively). No command owns a proposal
loop, a provider call, a planner or a terminal decision.

### 5.2 Preserved semantics

* **Terminal truth is the Driver's.** The command switches on
  `res.Termination.State` (`RuntimeCompleted` / `RuntimeUnsubstantiated` /
  default abort) and prints the runtime's `reason`; it never infers success.
* **Cancellation** resolves to the runtime's abort/terminal, never a fabricated
  success.
* **Durable view materialization.** The driver records the terminal transition
  into the journal but does not materialize the derived `snapshot.json` at that
  transition (it is rebuilt from the journal on the next `Open`). To preserve
  the retired headless path's checkpoint-at-terminal behavior, the CLI
  materializes the snapshot at finalization (`ledger.Checkpoint(CurrentID(),
  "cli-finalize")`). This is a projection of the journal, never a second
  terminal decision. Verified live: snapshot status `COMPLETED` (§7).
* **`$run` authority.** `scopeProvenance` maps `$run` to `domain.ScopeDynamic`
  (same vector as `$prompt`/`$build`/`$orchestrate`), so the migrated surface is
  not silently downgraded to read-only.

### 5.3 What was removed

`cmd/izen/runtime_ledger.go` (the bespoke durable ledger) and its test were
deleted: the authoritative `durable.TaskStore` wired by `compose.Wire` is now the
single execution journal for `izen run`, with the same event vocabulary and task
lifecycle. The pipeline-building code, the `cliGenerator`/
`semanticExtractorAdapter` adapters and `publishRunLifecycle` were removed from
`runtime.go`. `internal/app` is no longer imported by any production `cmd/izen`
file (AST-locked in §6.3).

---

## 6. Phase D — regression, parity and architecture tests

New/updated tests assert observable lifecycle transitions, filesystem effects and
terminal evidence — not merely that the adapter was called.

### 6.1 Artifact / approval / applied-bytes integrity (`internal/execution`)

* `TestArtifactIntegrity_MalformedPatchEnvelopePreviewIsTheAppliedArtifact`
  (new) — the live PATCH bug: preview diff carries no envelope; `+qux`/`-bar`
  present; applied `foo\nqux\nbaz\n`; `sha256(applied) == preview.ArtifactDigest`.
* `TestArtifactIntegrity_FileCreateAgainstExistingTargetIsCanonical` (new) —
  `FILE_CREATE` at an existing target: review is the body; applied == reviewed
  digest; overwrite gated normally.
* The existing 12 integrity tests still pass unchanged (14 total).

### 6.2 Driver target statement (`internal/runtime/autonomy`)

* `TestSetTargetsBindsTheNextRunThroughTheAuthoritativeSeam` (new) — an explicit
  target set is resolved by the run, deduplicated, applied to the held
  candidate, nothing written before approval, and the statement is consumed once.
* `TestScopeProvenanceMapping` extended with `$run`/`$RUN` → `ScopeDynamic`.

### 6.3 CLI parsing and architecture locks

* `cmd/izen/runtime_test.go` (new) — `parseRunArgs` preserves the public grammar
  (`-dir`, repeatable `-target`, one prompt, `-h`) and rejects ambiguous input.
* `internal/architecture/execution_ownership_convergence_test.go`:
  * `TestExecutionOwnership_CLIRunDrivesAuthoritativeDriver` — `runRuntimeCommand`
    crosses `runAuthoritativeCommand` and does **not** call `NewPipeline`,
    `RunCycle`, `NewOrchestrator`, `NewProposalProvider`, `NewTerminalBridge`.
  * `TestExecutionOwnership_AuthoritativeCommandWiresRuntimeAndDriver` — the
    shared helper crosses `compose.Wire` + `cli.RunAuthoritative` and does not
    construct a competing runtime/loop (`NewDriver` forbidden — the Driver is
    built by `compose.Wire`).
  * `TestExecutionOwnership_CLIProductionFilesDoNotImportApp` — no production
    `cmd/izen` file imports `internal/app`; the retired pipeline cannot return.
  * `TestExecutionOwnership_CLIOrchestrateDrivesAuthoritativeDriver` updated to
    the shared seam.

Coverage of the remaining brief items is provided by the existing suite, which
the migration did not weaken: target ambiguity/unresolved targets
(`internal/runtime/autonomy/acceptance_matrix_test.go`), approval/rejection/
cancellation (`internal/cli/authoritative_test.go`), placeholder rejection
(`TestArtifactIntegrity_PlaceholderEchoRejectedBeforeApproval`), verification
observing the file (`TestArtifactIntegrity_PostApplyVerificationObservesTheFile`),
checkpoint/recovery (`internal/runtime/durable/*`), budget exhaustion and
provider failure (`internal/runtime/autonomy/r6c_provider_failure_test.go`,
`r5_nonprogress_test.go`), terminal truth when a mutation succeeds but the
objective is unproven (`objective_verification_test.go`), fail-closed admission/
authorization (`TestAcceptance_UnauthorizedApprovalLeavesZeroDelta`).

---

## 7. Phase E — verification and live evidence

### 7.1 Deterministic test results (VERIFIED)

| Command | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./internal/execution/ -run TestArtifactIntegrity -count=1` | ok (14/14) |
| `go test ./internal/execution/ ./internal/changeset/ ./internal/architecture/ ./internal/cli/ ./internal/app/ ./internal/runtime/compose/ ./test/architecture/ -count=1` | ok (all seven) |
| `go test ./... -count=1` | **exit 0 — 212 packages `ok`, 29 with no test files, 0 `FAIL`, 0 panics** |

**Flake investigation (not a regression).** The first full-suite run after the
artifact fix reported one failure: `TestR5_CaseA_EndToEndMutationReachesProven`
(`internal/runtime/autonomy`), with the message "no continuation record reported
PROVEN progress" and a record whose `SelectedAction` was still empty. This is
the continuation-record snapshot-timing flake already documented in the Phase 2.1
report. It **passes in isolation**, with `-count=5`, and on every full-suite
re-run (`izen_fulltest2` failed once, `izen_fulltest3` and `izen_fulltest4` both
exit 0). No test was modified to pass.

### 7.2 Live execution evidence (VERIFIED — real local model)

Provider: local **Ollama `qwen2.5-coder:7b`** (real model; zero cost, no
credentials, no paid API). Workspaces: isolated `mktemp` git repos containing
only `keep.md` (CREATE) or `keep.md` + `greet.txt` (PATCH), committed as
baseline. Entry point: `./bin/izen run -dir <tmp> "<prompt>"` with `printf 'y\n'`
on stdin.

**CREATE** — `"Create a new file named hello.md with the content 'Hello everyone'."`

* surface: `$run` (audit `command_mode=$run`, `scope=$run`).
* owner: `autonomy.Driver` over `RuntimeExecutor` (loop.transition,
  execution.admission.decision, execution.spec.frozen).
* provider calls: **2** — requirement pass `prompt_tokens=236, completion=56,
  requested_output=512, finish=stop`; main generation `prompt_tokens=362,
  completion=13, requested_output=4096, finish=stop`.
* transitions: `approval.required` → `execution.summary status=awaiting_human
  parked=true boundary_action=approve` → operator `y` →
  `execution.mutation.completed Outcome=created DiffAdds=1 DiffRemoves=0
  ApplyExecuted=true FilesystemChanged=true`.
* verification: `execution.verification.completed Outcome=NOT_APPLICABLE`
  ("no verification configured for language") — an honest not-applicable.
* terminal: `objective.evaluated state=PROVEN granted=true
  authority=objective_completion_authority`; `execution.finished Success=true
  Outcome=created`; `run: run=run-1 completed approved=true reason="objective
  satisfied: created; objective PROVEN by evidence"`.
* artifacts: `hello.md` = `Hello everyone\n`; `.izen/patches/run-1-patch-1.json`
  canonical; `keep.md` byte-for-byte unchanged; `snapshot.json` = `COMPLETED`.

**PATCH** — `"change 'world' to 'everyone' in @greet.txt"`

* surface `$run`; provider calls **2** (requirement pass + main).
* proposal: `operation=UPDATE`, preview `-Hello world / +Hello everyone`
  (**no envelope**) — the §3.3 fix.
* mutation: `execution.mutation.completed Outcome=changed DiffAdds=1
  DiffRemoves=1 ApplyExecuted=true FilesystemChanged=true`.
* terminal: `objective.evaluated state=PROVEN`; `execution.finished Success=true
  Outcome=changed`; reason `"objective satisfied: changed; objective PROVEN by
  evidence"`.
* artifacts: `greet.txt` = `Hello everyone\n`; `keep.md` unchanged;
  `snapshot.json` = `COMPLETED`.

**Conversational** — `"say hello in one short sentence"` (no mutation expected)

* terminal: `objective.evaluated state=UNSUBSTANTIATED granted=false`;
  command printed `run: run=run-1 unsubstantiated approved=false reason="objective
  UNPROVEN (UNSUBSTANTIATED) … no workspace observation event for (no declared
  target) …"`. The execution itself reported `Outcome=completed`, but the
  runtime's completion authority refused to prove a workspace objective with no
  observation. This is the honest terminal and is disclosed as a deviation in
  §8.

No credentials were assumed, no evidence fabricated, and no paid API was used.

---

## 8. Remaining deviations and migration debt (REMAINING)

1. **Conversational prompts no longer answer directly.** The retired
   `app.Pipeline` short-circuited greetings to a direct chat pass and returned
   `app.Result.Answer`. The authoritative runtime has no workspace-independent
   completion contract, so a conversational `izen run` terminates
   `UNSUBSTANTIATED` (the executor still produces `direct_response`, but the
   objective authority cannot PROVE it). Per the brief ("if the authoritative
   runtime cannot preserve an existing required behavior, stop and report the
   missing capability rather than adding a parallel loop"), this is reported, not
   patched with a chat fallback. A future phase could add an explicit read-only
   direct-response terminal to the Driver, or route pure conversation to the
   fenced `/ask` surface.
2. **`izen run` is now interactive at the approval boundary.** Documented
   contract change required by the runtime (§4).
3. **The V3 pipeline audit-trail stdout block is gone**; the audit of record is
   `.izen/audit/events.ndjson`. Documented contract change.
4. **`internal/app.Pipeline` remains in the tree**, unreachable from production
   CLI (its brownfield/kernel locks still pass). It is retained as the rollback
   boundary and for its own tests; it is no longer a lifecycle owner on any
   command path.
5. **Derived-view materialization timing.** The CLI materializes `snapshot.json`
   at finalization; the driver itself does not checkpoint at the terminal
   transition. Recovery is journal-driven and correct regardless (replay
   reconstructs the terminal state). Noted for a future convergence of the
   driver's own finalization.
6. **`preview.AddedLines/RemovedLines` counts diff headers** (`+++`/`---`) via
   `countDiffLines` (`executor.go:3252`), so the preview metrics can read one
   higher than the mutation-boundary metrics for a minimal patch. Pre-existing,
   cosmetic, and independent of integrity (the digest binds the applied bytes);
   disclosed rather than silently "fixed".
7. **Unchanged from Phases 1/2:** `internal/cli.Stack`+`RunCycle`,
   `internal/engine`, `runtime/kernel`+`kernelbridge` vocabulary, `modes/plan`/
   `investigate`, UI `/ask`, secondary provider callers and the budget bypass
   resolvers remain fenced.

---

## 9. Conclusion

The migration is complete for its stated scope:

```text
ONE REQUEST → ONE ADMISSION (compose.Wire + preflight dispatch)
           → ONE AUTHORITATIVE RUNTIME (autonomy Driver, surface $run)
           → CONTROLLED EXECUTION-PLANE CAPABILITIES (RuntimeExecutor)
           → ONE HUMAN APPROVAL BOUNDARY (candidate-bound authorization)
           → ONE AUTHORITATIVE TERMINAL RESULT (runtime ledger)
```

Verified, not assumed:

* `izen run` no longer owns an execution loop: it parses arguments and enters the
  shared authoritative lifecycle (AST locks + source).
* Explicit `-target` targets are bound through the runtime's canonical target
  seam, not a private pipeline.
* Two reachable artifact-integrity violations were found (one by reading, one
  live) and fixed at the owner: an envelope reviewed but a body applied, on both
  the `FILE_CREATE`-against-existing-target path and the malformed-envelope PATCH
  path. Approved bytes now equal applied bytes, bound by digest.
* Approval, rejection, cancellation, authorization and unrelated-file
  preservation remain intact (tests + live).
* The full suite passes (212 `ok`, 0 `FAIL`), `go vet ./...` and `go build ./...`
  are clean, and a live local model exercised CREATE and PATCH end to end with
  `objective PROVEN by evidence` and a `COMPLETED` durable snapshot.
* Remaining deviations — direct chat, interactivity, the removed stdout audit
  block, and the fenced owners — are explicitly documented, not claimed
  converged.
