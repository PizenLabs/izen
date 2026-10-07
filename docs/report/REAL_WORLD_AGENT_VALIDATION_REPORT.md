# IZEN — Real-World Agent Runtime Validation, Evidence Truth & Task Harness

**Branch:** `fix/tui` · **Base for this exercise:** `a838e8d` (feat(exec): declared creation targets end-to-end)
**Model under test:** `qwen2.5-coder:7b` via a local Ollama at `http://127.0.0.1:11434/v1`
**Harness:** `test/realworld/` (opt-in via `IZEN_LIVE_FORENSICS=1`)
**Raw evidence:** `docs/report/realworld/evidence/`
**Scope:** validation and forensic integration only. No new phase. No kernel rewrite. No new subsystem.

---

## 0. Verdict up front

IZEN's control plane is truthful about what it *refuses* to do: across every task it
declined to mutate without authority, declined to complete without evidence, and
never turned model activity into progress. It is **not yet able to autonomously
solve targetless real-world tasks**: it parks for human target disambiguation
(Tasks B, E), it cannot complete a targetless read-only investigation (Tasks C, E),
and one natural bug-fix objective (Task D) dead-ends in a non-resumable control-plane
park. One task (A / CREATE) does execute end-to-end and, after a minimal fix, reaches
a truthful `PROVEN`.

The final line of this report is:

```text
REAL-WORLD EXECUTION NOT YET PROVEN
```

Two reproducible contract-level defects were found and minimally fixed (D1, D2);
the remainder are recorded with their first incorrect transition and owner.

---

## 1. Actual production execution path

Traced from real call paths, not filenames. The `$prompt` path is:

```text
cmd/izen/main.go
 └─ compose.Wire                          internal/runtime/compose/compose.go:500
     ├─ RuntimeExecutor                   (the execution authority)
     ├─ ExecutorAdapter                   (the only loop→execution surface)
     ├─ autonomy.Driver (app.Autonomous)
     └─ durable.TaskStore (ledger)
 └─ ui.RunMainDashboardWithApp             program.go:204
     └─ "$prompt" → routePromptDirective   ui/intent_dispatch.go:287
        └─ runAutonomyRoutedCmdExplicit    ui/autonomy_route.go:56
           └─ executeAutonomyWorkspace     ui/autonomy_route.go:163
              └─ executeAutonomyViaDriver  ui/autonomous.go:43
                 └─ runAutonomousDriver    ui/autonomous.go:127
                    └─ Driver.Run          internal/runtime/autonomy/driver.go:587
                       └─ observeAndRun    driver.go:2140
```

Owners of each authority (all verified by reading the functions):

| Authority | Owner |
|---|---|
| `$prompt` directive routing | `internal/ui/intent_dispatch.go:dispatchDirectives` / `routePromptDirective` |
| Intent classification | `internal/autonomy/intent.go:Classify` → `classifyDeterministic`; `Driver.admissionIntent` `driver.go:2031` |
| Objective compilation → spec | `Driver.preflightExecutionSpec` `driver.go:2042`; `IntentGateway.selectScopedStrategy` `internal/execution/intent.go:178` |
| Target authority / scope | `Driver.bindAuthoritativeTargets` `driver.go:1636`; `resolveAuthoritativeScope` `driver.go:1652`; `ExecutorAdapter.Resolve` `adapter.go:272` |
| Discovery (evidence only) | `Driver.deriveEvidenceScope` `driver.go:1805`; `execution.WorkspaceDiscovery.Discover` `internal/execution/discovery.go:270` |
| Evidence model | `execution.ObjectiveEvidence` `internal/execution/objective_authority.go:306`; `execution.ExecutionEvidence` `internal/execution/evidence.go:104` |
| Context compilation | `contextcompiler.Compiler.Compile` `internal/contextcompiler/compiler.go:426` |
| Capability authorization | `AuthorizationEngine.AuthorizeBuildCandidateContent` `internal/core/authorization/engine.go:289` |
| Model invocation | `RuntimeExecutor.invokeStream` `internal/execution/executor.go:4564` |
| Continuation | `continuation.DeriveNextStep` `internal/continuation/derive.go:15`; `Driver.ObjectiveContinuation` `objective_lifecycle.go:392` |
| Mutation (CREATE vs MODIFY) | `PatchManager.Apply` `internal/execution/patch.go:612`; `artifactForMutation` `internal/execution/strategy/selector.go:721` |
| Verification | `Verifier.RunAllFor` `internal/execution/verify.go:423`; behavioral gate `Driver.authorizeBehavioralCompletion` `objective_completion.go:254` |
| Progress / non-progress | `authoritativeProgressFingerprint` `objective_lifecycle.go:561`; `progress.Detector` `internal/progress/progress.go:205` |
| Completion | `ObjectiveCompletionAuthority.Evaluate` `internal/execution/objective_authority.go:727`; driver gate `authorizeObjectiveCompletion` `objective_completion.go:190` |
| Cancellation | `d.runCtx` `driver.go:607`; `Driver.Abort` `driver.go:843`; `terminateAbort` `driver.go:3142` |
| Durable evidence | `durable.TaskStore` `internal/runtime/durable/store.go`; audit `events.ndjson` `internal/events/audit/store.go:29` |
| UI rendering | `presentation.ExecutionProjection.Project` `internal/presentation/execution_projection.go:262`; `ui/model.go:handleDomainEvent:3150` |

Single loop owner: `autonomy.Driver.observeAndRun`. Single execution/mutation
authority: `execution.RuntimeExecutor`. Single completion authority:
`ObjectiveCompletionAuthority.Evaluate`. The harness drives exactly this path.

---

## 2. Task harness design

`test/realworld/harness.go` is a thin driver over the production composition:

1. Write the fixture; `git init` + commit (a checkpoint is a production
   precondition); create `.izen`; create the session-start snapshot.
2. `compose.Wire` with the real Ollama provider.
3. `app.Runtime.Start()` + `SubmitPromptCmd` (the production preflight dispatch).
4. `d.SetScope("$prompt")` — the exact production push — then `d.Run(ctx, objective)`.
5. Play the human **only** at an approval boundary (exactly as the TUI's
   `resumeAutonomousApprove` does, via `AuthorizationEngine.AuthorizeBuildCandidateContent`
   + `Executor.SetAuthorization` + `d.ResumeApprove`). A `clarify`/`inform` boundary
   is recorded and left parked: answering it would substitute human discovery for
   runtime discovery.
6. Capture: the full raw event stream (`forensics.Recorder`), the reconstructed
   `forensics.Trace`, a pre/post filesystem digest snapshot, changed-file contents,
   the terminal state, boundary, model calls/usage, mutations, verifications,
   objective evaluations, and event counts. Everything is written to
   `docs/report/realworld/evidence/` as NDJSON + JSON.

It fabricates nothing: there is no scripted provider, no expected trace, and no
simulator. The model is the variable under observation.

A **diagnostic arm** answers the clarify boundary with the true target. It is
labelled separately and does not replace the benchmark; its only purpose is to
separate "discovery is blocked" from "the downstream chain is broken".

---

## 3. Five task definitions

| ID | Prompt (verbatim) | Fixture | Mutation allowed |
|---|---|---|---|
| A | `create new file named testing.md` | `README.md`, `docs/notes.md` | yes (content unspecified) |
| B | `find the incorrect greeting in this project and fix it` | Go module: `main.go` prints `"Helo, world!"`, README says it should print `"Hello, world!"` | yes |
| C | `investigate why this application returns an error from the user endpoint. Do not modify anything.` | Go module: `userHandler` calls `findUser("")` instead of the request id | **no** |
| D | `find and fix the bug causing the user endpoint to fail` | same as C | yes |
| E | `find the main performance bottleneck in this project. Do not modify anything.` | `report.go`: O(n²) recompute + string concat in a loop | **no** |

No filename is supplied in B–E. The fixtures are ordinary code, not shaped around
any IZEN subsystem.

---

## 4–9. Raw execution evidence

Every number below is read from an authoritative runtime event or the real
filesystem. Full traces: `*-trace.ndjson`; raw events: `*-events.ndjson`;
summaries: `*-summary.json`.

### Task A — CREATE

| Fact | Value |
|---|---|
| Terminal state | `completed` |
| Termination | `objective PROVEN by evidence` |
| Objective authority | `state=PROVEN granted=true` |
| Target resolution | `Target=testing.md Exists=false Source=strategy` (valid CREATE evidence) |
| Task contract | `kind=CREATE` |
| Model calls / tokens | 2 / 356 in · 22 out |
| Mutation | `target=testing.md outcome=changed applied=true fs_changed=true` |
| Verification | `NOT_APPLICABLE` ("no verification configured for language") |
| Filesystem delta | `ADDED testing.md` |
| Wall | ~7.4 s |

`testing.md` was created with real content (this run: a short Markdown
introduction). No file was pre-created. No AST was required.

> Before the D1 fix this same task ended `unsubstantiated` while the objective
> authority had granted `PROVEN`. See §12.

### Task B — MODIFY (main arm, targetless)

| Fact | Value |
|---|---|
| Terminal state | `awaiting_human` |
| Boundary | `clarify` — "the target is unresolved and the workspace offers 3 candidate(s)" |
| Candidates | `[README.md, go.mod, main.go]` |
| Model calls | 1 (preflight only; no execution invocation) |
| Mutation | none |
| Wall | ~0.8 s |

Discovery *observed* the workspace; it did not bind a target. This is the
documented I13 behaviour (discovery is evidence, never authority). The runtime
did **not** mutate on a guess.

### Task C — INVESTIGATE (no mutation)

| Fact | Value |
|---|---|
| Terminal state | `unsubstantiated` |
| Objective authority | `UNSUBSTANTIATED` — `cond-observed` unmet: "no workspace observation event for (no declared target)" |
| Contract | read-only (`TaskRead`), verifier `read-only execution` |
| Model calls / tokens | 3 / 146 in · 1314 out |
| Mutation | none; filesystem delta none |
| Wall | ~70 s |

The read-only fix (D2) stopped the runtime from treating "Do not modify anything"
as a mutation. The model *did* answer, but the objective could not be proven
because the targetless read-only path emits no workspace-observation event. The
runtime honestly reported "not proven" rather than claiming success.

### Task D — FIX

| Fact | Value |
|---|---|
| Terminal state | `awaiting_human` |
| Boundary | `inform` (non-resumable) |
| Reason | `canonical intent revision incomplete: … intent context re-compilation under "modification" failed: … the contract requires workspace material but the compiled payload carries none` |
| Reason code | `CONTEXT_UNAVAILABLE` |
| Model calls | 0 |
| Mutation | none |
| Wall | ~0.3 s |

The objective was classified read-only (`debugging`) while the strategy gateway
selected a mutation contract; the blocking revision then recompiled context over
an **empty** target set and failed closed. See D3 in §12.

### Task E — PERFORMANCE (no mutation)

| Fact | Value |
|---|---|
| Terminal state | `unsubstantiated` |
| Objective authority | `UNSUBSTANTIATED` — same `cond-observed` clause as C |
| Contract | read-only |
| Model calls / tokens | 3 / 138 in · 1475 out |
| Mutation | none; filesystem delta none |
| Wall | ~76 s |

### Diagnostic arms (target named by the "human" at clarify)

| Arm | Result |
|---|---|
| B-diag (`main.go`) | Model produced a patch; applied; verification (`go fmt/lint/vet/build/test`) **FAILED**; the mutation was rolled back atomically and the run `aborted` (`verify_failed`). The runtime failed closed rather than claiming success. |
| E-diag (`report.go`) | With the read-only fix, the objective stayed read-only and ended `unsubstantiated` — no patch was produced. Before the fix, the runtime had prepared a patch for a "do not modify anything" objective. |

### Objective transitions observed

- A: `continuation → complete → objective.evaluated(PROVEN, granted)` → `execution.summary(completed)`.
- B/C/E: `preflight_admission_gate → disambiguate` (B) or `completion condition cond-observed unmet` (C/E).
- D: `intent revision debugging → modification` → `recompile FAIL` → `inform`.
- No task produced `COMPLETION_WITHOUT_EVIDENCE`, `PREMATURE_TERMINATION`, or any
  `forensics` pattern.

---

## 10. TUI-vs-runtime provenance audit

The UI is a projection of runtime truth; it must never manufacture a claim. The
audit found both truthful projections and real gaps.

### Truthful (event-backed)

`internal/presentation/narrative.go` maps only real domain events to human
sentences: `target.resolved → "Reading <file>"`, `provider.invoked → "Analyzing"`,
`provider.first_token → "Model responding"`, `artifact.produced → "Preparing result"`,
`approval.required → "Waiting for approval"`, `mutation.started → "Applying changes"`,
`mutation.completed → "Applied change"`, `verification.completed → "Verified changes"`.
Stage/step/activity-tree badges are driven by the corresponding events.

### Model-originated (correctly distinguished)

Streamed model tokens are rendered as model output, not as runtime facts.

### Fake / unsupported (defects)

| # | UI claim | Emitter | Authoritative event |
|---|---|---|---|
| E1 | objective PROVEN / UNSUBSTANTIATED verdict | *(absent)* | **NONE** — `objective.evaluated` is not in `projectedEventTypes()` (`ui/program.go:409-489`) and is not handled in `ui/model.go:handleDomainEvent`; the comment at `ui/model.go:3590` routes it to Trace only |
| E2 | terminal `"Completed"` | `presentation/execution_projection.go:557-568` + `completion_gate.go:101-121` | derived from `execution.finished`; for non-mutation runs the gate grants completion with no evidence (`completion_gate.go:102-107`) and never reads the objective verdict |
| E3 | `"Working..."`, `"Verifying build..."`, `"Analyzing failure..."`, `"Synthesizing plan..."`, `"Executing strategy..."`, `"Investigating..."`, `"Generating patch..."`, `"Running tests..."`, `"Analyzing trace..."`, `"Loading IZEN..."` | `ui/keys.go:1589`, `ui/loading.go:156-182`, `ui/commands.go`, `ui/update.go`, `ui/workspace.go` | **NONE** — local strings/messages |
| E4 | plan task `"completed"` for all tasks | `ui/model.go:4127-4143` (`markAllPlanTasksCompleted`), `ui/gateway.go:807`, `ui/update.go:2264` | **NONE** — set from a local result, not per-task `mutation.completed`/`verification.completed` |
| E5 | activity-tree `"✔ done"` default | `ui/activity_tree.go:382-383` | default for any non-running/non-failed entry; `failed` is hard-coded false for `file.read`/`file.mutate`/`search`/`resolve` (`:433,442,498,507`) |
| E6 | `"Connecting... Ns"`, live tok/s | `ui/footer.go:491-508`, `ui/stream_cost.go` | timer/estimate-driven until a provider usage event replaces it |

The most consequential is **E1+E2**: the runtime's own completion verdict is
dropped before the UI, and the UI's terminal label is computed from a weaker gate.
The UI cannot distinguish a PROVEN objective from an unsubstantiated one. In Task
A's pre-fix run this meant the runtime said `unsubstantiated` while the authority
had said `PROVEN`; the UI would have shown "Not completed". Post-fix both agree,
but only by accident of the terminal state matching — not because the UI consumes
the verdict.

None of E1–E6 can fabricate a mutation; the mutation narrative is event-backed.

---

## 11. Efficiency measurements

| Task | Wall | Model calls | In/Out tokens | Mutations | Verifications | Continuations | Boundary stops | Human interventions |
|---|---|---|---|---|---|---|---|---|
| A CREATE | 7.4 s | 2 | 356 / 22 | 1 applied | 1 (N/A) | 1 | 0 | 1 (approval) |
| B MODIFY | 0.8 s | 1 | 0 / 0 | 0 | 0 | 0 | 1 (clarify) | 0 (left parked) |
| C INVESTIGATE | 70.1 s | 3 | 146 / 1314 | 0 | 2 (N/A) | 0 | 0 | 0 |
| D FIX | 0.3 s | 0 | 0 / 0 | 0 | 0 | 0 | 1 (inform) | 0 |
| E PERFORMANCE | 76.3 s | 3 | 138 / 1475 | 0 | 2 (N/A) | 0 | 0 | 0 |

Where the time actually goes: C/E are dominated by **model latency** (three
sequential ~20 s invocations producing ~1200 tokens each), not by redundant
runtime work. There were **no repeated identical reads, no pointless retries, and
no repeated searches** in any arm. B and D are sub-second because they park before
any provider call. A is fast and does real work. The runtime's own overhead is
negligible; the cost is model output length.

---

## 12. Discovered defects, first incorrect transition, owner

### D1 — Behavioral gate mis-fires on a filename (Task A) — **FIXED**

- **Symptom.** `testing.md` was created; `ObjectiveCompletionAuthority` granted
  `PROVEN`; the behavioral gate then ran `runtime.serve`/`runtime.fetch`, found no
  entry document, and downgraded the objective to `unsubstantiated`. A proven
  CREATE became a false-negative terminal.
- **First incorrect transition.** `BehaviorRequired("create new file named testing.md")`
  returned `true` because the substring `"test"` occurs inside the filename
  `testing.md`.
- **Owner.** `internal/runtime/autonomy/behavior.go:100` (`BehaviorRequired`),
  consumed at `objective_completion.go:258`.
- **Classification.** **D** (evidence/provenance / completion truth): a prose
  heuristic outside the objective contract overrode the completion authority.
- **Minimal fix.** Strip path/filename tokens before the verb scan. A target is
  not a verb.
- **Regression.** `TestBehaviorRequiredIgnoresFilenameTokens`.
- **Rerun.** Task A → `completed`, `objective PROVEN by evidence`.

### D2 — Read-only constraint ignored on the autonomy path (Tasks C, E) — **FIXED (partially effective)**

- **Symptom.** "Do not modify anything" was ignored: C/E were classified as
  mutations, parked for a mutation target, and E-diag produced a patch.
- **First incorrect transition.** The driver resolved scope with
  `Adapter.Resolve` → `gateway.SelectStrategy`, the *unconditional* selector that
  never consults `StatesReadOnlyConstraint`; and `taskContract` derived
  `requiresMutation` from `autonomy.Classify` (text only). The constraint-aware
  path (`selectScopedStrategy`) was reachable only from the UI `Gate`.
- **Owner.** `internal/runtime/autonomy/adapter.go:272` / `driver.go:1652` /
  `objective_completion.go:147`; constraint owner `internal/execution/intent.go`.
- **Classification.** **B** (control-plane integration defect): the same human
  request produced different authority depending on which path compiled it.
- **Minimal fix.** Added `IntentGateway.ReadOnlyConstraintStated` /
  `ReadOnlyConstraintStrategy` (single call site, preserving the semantic lock),
  `ExecutorAdapter.ResolveScoped`, routed `Driver.resolveAuthoritativeScope`
  through it, and made `taskContract` honour the constraint.
- **Regression.** `TestAdapter_ResolveScopedHonorsReadOnlyConstraint`,
  `TestDriver_ReadOnlyConstraintOverridesMutationVerb`.
- **Rerun.** C/E now run read-only (no mutation attempted, model invoked). They do
  **not** complete — see D5.

### D3 — Intent revision hard-fails on an unresolved scope (Task D) — **DOCUMENTED, NOT FIXED**

- **Symptom.** Task D parks at a non-resumable `inform` with `CONTEXT_UNAVAILABLE`.
- **First incorrect transition.** `autonomy.Classify` returns the read-only intent
  `debugging` for an objective the strategy gateway reads as mutation
  (`MultiFilePlanning`). `Driver.syncCanonicalIntent` then elevates to modification
  and recompiles context for `d.objectiveTargets()` — empty because the scope is
  unresolved — so `ErrIntentContextProvenance` fires and the run parks.
- **Owner.** `internal/runtime/autonomy/objective_completion.go:syncCanonicalIntent`
  and `internal/autonomy/intent.go:classifyDeterministic` (the classifier/strategy
  split).
- **Classification.** **B** (control-plane integration defect).
- **Why not fixed.** The obvious minimal correction (defer the revision when there
  is no target, letting admission disambiguate) was implemented and re-tested: it
  moved the failure from a *parked* boundary to a *hard* `direct_completion`
  authority-ceiling error, i.e. worse. A correct fix requires a design decision
  about classifier/strategy agreement (or reordering admission before the intent
  revision); that is out of scope for a minimal correction. Recorded honestly.

### D4 — Targetless objectives park at disambiguation (Tasks B, E main arms) — **EXPECTED, BY DESIGN**

- Discovery observed candidates; the scope stays `DISCOVERED`; admission returns
  `disambiguate`; the run parks for the human to name the target.
- This is invariant I13 ("discovery candidates are evidence, never authority"),
  and it is the correct, explicit outcome. Classified **H/J** (capability
  boundary / expected limitation). The consequence is that IZEN cannot
  autonomously solve a targetless natural-language task today; it needs the human
  to name the target. This is the dominant blocker for real-world autonomy.

### D5 — Targetless read-only investigation cannot satisfy its observation clause (Tasks C, E) — **DOCUMENTED, NOT FIXED**

- After D2, C/E derive a `TaskRead` contract and the model answers, but
  completion is refused: `cond-observed` is unmet ("no workspace observation event
  for (no declared target)"). A targetless read-only objective can therefore never
  reach `PROVEN`, even though the runtime discovered the repository and the model
  produced a conclusion.
- **First incorrect transition.** `evaluateRead` requires `WorkspaceObservations >= 1`
  (`internal/execution/objective_authority.go:891`), but the targetless
  repository-investigation path emits no workspace-observation event.
- **Owner.** `internal/execution/objective_authority.go:evaluateRead` + the
  read-only execution path.
- **Classification.** **H** (capability missing) / **B**. The runtime has no
  terminal vocabulary for an investigation conclusion (`SUPPORTED` /
  `INCONCLUSIVE`); it has only `PROVEN` / `UNSUBSTANTIATED`.
- **Why not fixed.** Counting repository discovery as the required observation is a
  contract change with broad blast radius; it is recorded rather than rushed.

### D6 — CREATE recorded as `changed`, verification `NOT_APPLICABLE` (Task A) — **MINOR / DOCUMENTED**

- The contract kind was `CREATE` and the target resolved `Exists=false`, but the
  mutation outcome was `changed` (not `created`) and verification was
  `NOT_APPLICABLE`. CREATE semantics were preserved at the contract layer
  (`creationShape`), so this is an evidence-precision limitation, not a false
  claim. Classification **D** (low) / **J**.

---

## 13–16. Minimal fixes and regressions applied

| Defect | Change | Files | Regression |
|---|---|---|---|
| D1 | `BehaviorRequired` strips path/filename tokens before the verb scan (`stripPathTokens`) | `internal/runtime/autonomy/behavior.go` | `TestBehaviorRequiredIgnoresFilenameTokens` |
| D2 | One constraint call site (`IntentGateway.ReadOnlyConstraintStated`); `ReadOnlyConstraintStrategy`; `ExecutorAdapter.ResolveScoped`; driver routes scope resolution through it; `taskContract` honours the constraint | `internal/execution/intent.go`, `internal/runtime/autonomy/adapter.go`, `internal/runtime/autonomy/driver.go`, `internal/runtime/autonomy/objective_completion.go` | `TestAdapter_ResolveScopedHonorsReadOnlyConstraint`, `TestDriver_ReadOnlyConstraintOverridesMutationVerb` |

Both changes preserve the architecture locks:
`TestSemanticLock_ReadOnlyConstraintHasExactlyOneNonTestCaller` and
`TestKernelLock_NoUnregisteredWorkspaceExistenceDecision` pass (the adapter
addition was placed at end-of-file so the line-number allowlist is untouched).

No kernel/runtime contract was reopened. No R1–R7 behaviour was weakened.

---

## 17. Real-task rerun results

| Task | Before | After |
|---|---|---|
| A CREATE | `unsubstantiated` (authority `PROVEN`) | **`completed` / `PROVEN`** |
| B MODIFY | `awaiting_human` clarify | unchanged (`clarify`) — D4 |
| C INVESTIGATE | `awaiting_human` clarify (classified mutation) | **read-only**, model invoked, `unsubstantiated` — D5 |
| D FIX | `awaiting_human` inform | unchanged (`inform`) — D3 |
| E PERFORMANCE | `awaiting_human` clarify (classified mutation) | **read-only**, model invoked, `unsubstantiated` — D5 |

Verification discipline: `go build ./...` clean; `go vet ./...` clean;
`go test -count=1 ./...` green (see `evidence/full-suite-final.log`); the two
architecture-lock packages green.

---

## 18. Unsupported capabilities

- Autonomous target resolution from a natural-language description (no explicit
  target, no declared kind): blocked by design → human disambiguation.
- Read-only investigation completion without a declared target.
- Investigation terminal vocabulary beyond `PROVEN`/`UNSUBSTANTIATED`
  (`SUPPORTED`/`INCONCLUSIVE` do not exist).
- Autonomous bug-fix objectives whose wording the classifier reads as
  `debugging` while the strategy reads as mutation (D3).
- `golangci-lint` is not installed on the host; the verifier's `lint` step fails
  closed, so any Go mutation whose verification includes lint aborts. This is an
  environment limitation (**F**), not a runtime defect — the runtime correctly
  refused to claim success.

## 19. Model limitations observed

- `qwen2.5-coder:7b` produced a plausible but verification-failing patch for the
  greeting task (B-diag); the runtime rolled it back atomically.
- For the targetless investigations (C/E) the model answered at length (~1200
  output tokens) but the runtime had no observation event to substantiate it, so
  the answer could not become a proven objective.
- No `finish_reason=length`, token exhaustion, or provider failure occurred in any
  run; `output_exhausted=0`, `model_failures=0` throughout.

## 20. Remaining uncertainty

- The real tasks were run once per arm on one local model; run-to-run model
  variance is real (Task A produced different file content across runs).
- D3's correct fix is unresolved by design; the recorded workaround attempt made
  things worse.
- D5's contract change (what counts as a workspace observation for a targetless
  investigation) was not attempted.
- The UI truth defects E1–E6 are documented from source, not yet fixed; a
  subscriber/verdict change is a UI-layer decision outside this minimal exercise.
- The behavioral gate still uses an English substring heuristic for its
  remaining verb scan (`"incorrect"` contains `"correct"`, `"invalid"` contains
  `"valid"`); D1 removed the reproduced filename collision only.

---

## Final answer to the central question

> Can IZEN reliably give an LLM a bounded, evidence-driven, observable
> environment in which it can investigate, reason, propose, execute, verify, and
> stop truthfully on real tasks?

**Not yet.** IZEN's authority, evidence and truthfulness invariants hold under
real model execution — it never mutated without authorization, never completed
without evidence, and never turned activity into progress. But it cannot yet
*autonomously solve* targetless real-world tasks: it parks for human target
disambiguation, cannot complete a targetless investigation, and one natural
bug-fix objective dead-ends in a control-plane park. Two concrete contract-level
defects were fixed with regressions; the rest are recorded with their first
incorrect transition and owner.

```text
REAL-WORLD EXECUTION NOT YET PROVEN
```
