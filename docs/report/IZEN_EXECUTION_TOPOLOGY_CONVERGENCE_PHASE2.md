# IZEN — Execution Topology Convergence, Phase 2

Date: 2026-10-09 · Branch: `fix/execution` · HEAD at start: `f9b117c`
Recorded baseline: `d5fe1f2` (the parent of the previous convergence change).

This is the second phase. The previous change (`f9b117c`,
`docs/report/IZEN_EXECUTION_TOPOLOGY_CONVERGENCE.md`) established the bounded
autonomy `Driver` as the authoritative lifecycle for the migrated TUI surfaces
(`$prompt`, ordinary `/build`, `$hot`) and explicitly fenced the remaining
owners in its §14. This phase selects ONE of those remaining owners, migrates it
onto the same authoritative lifecycle, and discloses everything it did not do.

Distinctions used throughout, unchanged from Phase 1:

* **VERIFIED** — proven by a test that ran on this working tree, or by a live
  provider run.
* **OBSERVED** — read in the current source, with a citation.
* **REMAINING** — a known deviation, disclosed and not claimed converged.

---

## 1. Method and baseline

1. `git status` → clean tree; branch `fix/execution`; HEAD `f9b117c`
   (`feat(exec): execution topology convergence`), one commit above the recorded
   baseline `d5fe1f2`. The commit `f9b117c` **is** the previous convergence
   change and the source of `IZEN_EXECUTION_TOPOLOGY_CONVERGENCE.md`.
2. Re-read the previous report and its evidence audit
   (`docs/report/IZEN_EXECUTION_TOPOLOGY_AUDIT.md`).
3. Re-ran the baseline ownership/authorization tests (all `ok`; see §5.1).
4. Rebuilt the call graph for every remaining owner by exact source reads and
   import/call-edge greps. The codebase-memory graph was **not** used for the
   final claims: its snapshot was indexed 2026-10-04, before `f9b117c`
   (2026-10-09), so it is stale for the changed files. All claims below are
   source-verified and each material claim has a test or a live run.

---

## 2. Phase A — current topology

### 2.1 Entry points and reachability (OBSERVED)

`cmd/izen/main.go` dispatches four execution-bearing subcommands in addition to
the TUI:

| Subcommand | Owner | Import proof | Can mutate | Own terminal truth |
|---|---|---|---|---|
| `izen run "<prompt>"` | `internal/app.Pipeline` | `cmd/izen/runtime.go` + `runtime_ledger.go` are the **only** importers of `internal/app` | yes (via `substrate`) | `app.Result` |
| `izen prompt` / `izen orchestrate` | `internal/cli.Stack` → `orchestrator.RunCycle` | `cmd/izen/orchestrate.go` is the **only** importer of `internal/cli` | yes (via `FileExecutor`) | `orchestrator.ExecutionResult` |
| TUI `$prompt`/`/build`/`$hot` | autonomy `Driver` → `RuntimeExecutor` | `compose.Wire` (previous phase) | yes | `autonomy.RuntimeLoop` |

Import scans (`grep -rl`, non-test) established:

* `internal/app` — imported only by `cmd/izen/runtime.go`,
  `cmd/izen/runtime_ledger.go` → **`izen run` only**. Not reachable from the TUI.
* `internal/cli` — imported only by `cmd/izen/orchestrate.go` → **`izen
  prompt` / `izen orchestrate` only**.
* `internal/runtime/orchestrator` — imported by `cmd/izen`, `internal/cli`,
  `internal/ui` (only the deterministic `PhaseManager` + budget helpers),
  `internal/runtime/compose` (only `PhaseManager`), `internal/verification`,
  and `test/realworld`. The **proposal lifecycle** `Orchestrator.RunCycle` is
  reached only from `cli.Stack.Run` and the verification harness.
* `internal/engine` (layered loop) — imported by `internal/ui`, `internal/modes`
  (`plan`, `investigate`, `review`), `internal/runtime/compose`,
  `internal/runtime/orchestrator/manager.go`, `internal/execution`
  (`mutationset.go`, `patch.go`). It is a **facade** consumed by the read-only
  planning surfaces (`ExecutePlan` / `ValidatePatch`), not a TUI mutation loop.
* `internal/runtime/kernel` + `internal/kernelbridge` — the execution plane both
  CLI paths already cross at mutation time
  (`internal/runtime/executor/kernelcommit.go`,
  `internal/runtime/substrate/kernelcommit.go`). Not a lifecycle owner.

### 2.2 The two genuine CLI owners

Both remaining CLI owners are **not** reachable from the TUI, and both cross the
same kernel at the mutation seam. They differ in lifecycle ownership:

```text
izen run  (internal/app.Pipeline)
  cmd/izen/runtime.go:198  app.NewPipeline
    → app.Pipeline.Run (pipeline.go:290)
      → for {}  compile → generate → extract → align → validate → repair → plan
      → ConcreteSubstrate.Execute → kernelbridge.Apply → runtime/kernel
  ⇒ LIFE-OWNER: app.Pipeline; terminal vocabulary: app.Result

izen prompt / izen orchestrate  (internal/cli.Stack)
  cmd/izen/orchestrate.go:106  cli.Wire
    → Stack.Run (cli.go:397)
      → Orchestrator.RunCycle (engine.go:101)
        → preflight → ProposalProviderCLI.GenerateProposal (provider call)
        → ProposalValidator.Validate → ApprovalGate (own [y/N] session)
        → FileExecutor.Commit → kernelbridge.Apply → runtime/kernel
  ⇒ LIFE-OWNER: orchestrator.RunCycle; terminal vocabulary: ExecutionResult
```

### 2.3 Per-owner responsibilities (OBSERVED)

`internal/cli.Stack` / `orchestrator.RunCycle`:

* reachable entry points: `izen prompt`, `izen orchestrate` (and the
  `internal/verification` harness);
* mutates files: **yes**, through its own `FileExecutor` → kernel;
* continuation/retry/recovery/cancellation: **none/single-cycle**; the
  `Run` doc explicitly states it "never loops on NeedsContinuation and never
  enters autonomy.Driver" (`cli.go:388-396`);
* approval: **its own** `authorization.ApprovalGate` (a separate gate type from
  the production `core/authorization.AuthorizationEngine`);
* verification: the executor's own transaction; no objective proof;
* terminal truth: `orchestrator.ExecutionResult` (`Committed`, `Action`);
* provider seam: `ProposalProviderCLI.GenerateProposal` → `LLMProvider.Complete`
  (one provider call per cycle);
* tests: `internal/cli/cli_test.go` (`TestStackRunEndToEnd`,
  `TestStackRunRejectionEndToEnd`, `TestStackRunIsSingleCycle`),
  `internal/runtime/orchestrator/*_test.go`, `internal/verification/harness.go`;
* disposition: **active but fenced** (CLI only).

`internal/app.Pipeline`:

* reachable entry points: `izen run` only;
* mutates files: **yes**, through `substrate` → kernel;
* continuation/retry/recovery: **yes** — a real `for{}` loop with
  `maxAttempts`/`maxRepairs` and extraction/alignment/validation repair
  (`pipeline.go:394-469`);
* approval: **none** — it has no human approval gate; it writes through the
  substrate after its internal gates;
* verification: capability validation + semantic alignment (not objective proof);
* terminal truth: `app.Result`;
* tests: `internal/app/*_test.go` (extensive).

`internal/engine`, `modes/plan`, `modes/investigate`, `modes/review`,
UI `/ask` + role fallback, secondary provider callers, and the budget bypass
resolvers are unchanged from Phase 1 §14 and remain fenced.

### 2.4 Baseline test run (VERIFIED)

```
go build ./...                                                  exit 0
go test ./internal/core/authorization/ ./internal/runtime/autonomy/ \
        ./internal/architecture/ -run 'TestExecutionOwnership|TestMutationOperation|TestCapabilityInvocation'
                                                                ok
```

---

## 3. Phase B — migration decision

### 3.1 Ranking of remaining deviations

| Rank | Owner | Competing lifecycle? | Reachable from | Duplication | Migration cost | Risk |
|---|---|---|---|---|---|---|
| 1 | `cli.Stack` + `RunCycle` | **yes** (own provider + validator + approval gate + executor + terminal result) | `izen prompt`/`orchestrate` | duplicates proposal/approval/commit + provider seam | **medium**, self-contained | medium |
| 2 | `app.Pipeline` | **yes** (full `for{}` loop) | `izen run` | duplicates generation/extract/align/plan/commit | high (capability/planner semantics) | high |
| 3 | `internal/engine` facade | read-only via modes | `/plan`,`/investigate`,`/review` | validation/context | high | high |
| 4 | kernel/kernelbridge | no (execution plane) | all mutation seams | none | n/a | n/a |
| 5 | `modes/plan`/`investigate` | read-only loops | TUI | provider seam | medium | medium |
| 6 | UI `/ask` + secondary | read-only conversation | TUI | provider seam | low | low |

### 3.2 Selected first target

> **Owner removed/subordinated:** the `izen prompt` / `izen orchestrate` proposal
> lifecycle — `internal/cli.Stack.Run` → `internal/runtime/orchestrator.Orchestrator.RunCycle`
> and its private `ProposalProviderCLI` + `ProposalValidator` +
> `authorization.ApprovalGate` + `FileExecutor` stack.
>
> **Authoritative destination:** the composition-root autonomy `Driver`
> (`internal/runtime/autonomy.Driver`) over the `RuntimeExecutor` execution
> plane — the same owner the TUI drives, reached through `compose.Wire`.

Rationale (evidence-based):

* It is a complete, self-contained execution owner: own provider call, own
  validation, own approval gate, own executor, own terminal vocabulary. Removing
  it from the command path removes a genuine competitor.
* It is the **smallest** competitor whose contract maps onto the authoritative
  runtime, so it is the lowest-risk first migration — exactly what the brief
  asks for ("the smallest complete migration").
* Its behavior is directly comparable: the same objective, the same fixture, the
  same operator decision can be run through both owners (§5.3).
* It is the only remaining owner whose destination wiring (`compose.Wire` →
  `app.Autonomous`) already exists and is exercised by the real-world harness.

`app.Pipeline` (rank 2) is the next target, not this one: its capability/
alignment/planner semantics do not map onto the runtime without a rewrite, so
migrating it first would violate "smallest complete migration".

### 3.3 Behavioral contract that must remain unchanged

* `izen prompt` / `izen orchestrate` parse `-dir <path>` and one objective.
* A proposal is rendered and the operator decides with the **same vocabulary**:
  `y`/`yes` applies, `i`/`inspect` inspects without applying, anything else
  (including Enter) rejects.
* Approve applies the reviewed bytes atomically through the kernel; reject
  leaves the workspace untouched.
* The mutation authorization is bound to the reviewed candidate identity and
  digest; a refusal refuses the apply.
* Cancellation stays distinct from failure and success.
* Terminal truth is the runtime's, established by evidence, not by model prose
  or UI state.

### 3.4 Parity tests required

1. `internal/cli` behavioral tests: approve issues the authorization before
   resume; reject/inspect never authorize; authorization refusal rejects;
   clarify resumes; inform parks; bridge failure aborts; cancellation preserved.
2. `internal/cli` execution-parity test: the legacy `cli.Stack.Run` path and the
   authoritative `RuntimeExecutor` plane produce **identical file bytes** for the
   same model content and operator decision.
3. `internal/architecture` AST locks: `runOrchestrateCommand` crosses
   `cli.RunAuthoritative` + `compose.Wire` and does **not** call
   `RunCycle`/`NewOrchestrator`/`NewProposalProvider`/`NewTerminalBridge`;
   `cli.RunAuthoritative` never invokes a provider or the filesystem.
4. `internal/runtime/autonomy` scope-provenance test extended for the new
   `$orchestrate` surface.

### 3.5 Rollback boundary

`cmd/izen/orchestrate.go` is the only production file that changes behavior. To
roll back, restore its `cli.Wire` + `stack.Run` body; `internal/cli` and
`internal/runtime/orchestrator` are **not** deleted and their tests still pass.
The library additions (`internal/cli/authoritative.go`) and the tests are
additive.

### 3.6 Files/packages expected to change

`cmd/izen/orchestrate.go`, `internal/cli/authoritative.go` (+tests),
`internal/runtime/autonomy/objective_completion.go` (+ test),
`internal/architecture/execution_ownership_convergence_test.go`.

---

## 4. Phase C — implementation

### 4.1 What changed (exact)

| File | Change |
|---|---|
| `internal/cli/authoritative.go` (new) | `RunAuthoritative` — the narrow CLI driver over the authoritative runtime. Defines the structural `AuthoritativeDriver` port (Run/ResumeApprove/ResumeReject/ResumeClarify/Abort/State/Boundary/SetScope/RunID), the `HumanBridge` port, the `AuthorizeFn`, and the terminal `DriverTerminalBridge` (renders the runtime's own `CandidateDiff`/evidence, reads `[y/N]`/inspect, preserves the vocabulary). |
| `cmd/izen/orchestrate.go` (rewritten) | `runOrchestrateCommand` now wires `compose.Wire` (production composition root) + `app.Autonomous` and calls `cli.RunAuthoritative`. It grants the workspace capability vector at admission (read/analyze/propose/mutate/verify), creates the session-start checkpoint, dispatches `SubmitPromptCmd` preflight, issues the mutation authorization on approve, flushes audit, and projects the runtime terminal state. Removed the private `orchestrateAdapter`/`stubNoopProvider` and the `cli.Wire`/`stack.Run` call. |
| `internal/runtime/autonomy/objective_completion.go` | `scopeProvenance()` maps `$orchestrate` (case-insensitive) to `domain.ScopeDynamic`, so the headless CLI build surface carries the same mutation authority as `$prompt`/`$build` while remaining a distinct command surface. No new authority. |
| `internal/cli/authoritative_test.go` (new) | Behavioral ownership tests (10). |
| `internal/cli/authoritative_parity_test.go` (new) | Execution parity legacy-vs-authoritative (2). |
| `internal/runtime/autonomy/behavior_test.go` | Pins `$build`/`$orchestrate` → `ScopeDynamic`. |
| `internal/architecture/execution_ownership_convergence_test.go` | AST locks on the CLI seam and on `RunAuthoritative` purity. |

No new runtime, planner, driver, compatibility executor, or state machine was
introduced. The CLI no longer constructs any of the following on its production
path: `ProposalProviderCLI`, `ProposalValidator`, `ApprovalGate`, `FileExecutor`,
`Orchestrator.RunCycle`.

### 4.2 How the requirements are met

1. **Semantics/scope/target/approval preserved.** Argument parsing, target
   resolution (via the runtime's `IntentGateway`), the `[y/N]`/inspect
   vocabulary, and the approve/reject boundary are preserved; the surface is
   recorded as `$orchestrate` (distinct from `$build`/`$prompt`).
2. **Cancellation preserved and distinct.** The command's context is threaded to
   the driver; a canceled run returns the runtime's abort and is never reported
   as success (`TestAuthoritativeCancellationIsPreserved`).
3. **Proposal payload integrity / authorization semantics.** The approval review
   is rendered from the runtime's own `HumanBoundary.CandidateDiff` (the same
   object the mutation records its evidence from) and the authorization is bound
   to `CandidateID`/`CandidateDigest`; a refusal rejects, never applies
   (`TestAuthoritativeAuthorizationRefusalRejectsNotApplies`).
4. **Objective completion by evidence.** The command reports only the runtime's
   terminal state; the live run's `objective.evaluated` event is
   `state=PROVEN, granted=true, authority=objective_completion_authority`.
5. **No silent fallback.** The command no longer builds `cli.Wire`/`RunCycle`;
   the architecture lock fails if it returns. The previous no-provider stub path
   is removed: an execution command without a provider fails closed.
6. **Read-only paths stay separate.** `/ask`, `modes/plan`, `modes/investigate`,
   `modes/review`, and the secondary provider callers are untouched; their
   lifecycle genuinely differs (read-only conversation/planning) and remains
   fenced as in Phase 1 §14.
7. **No premature removal.** `internal/cli`, `internal/runtime/orchestrator`, and
   the `FileExecutor` remain in the tree (tests + `internal/verification`
   harness); the kernel and legacy engine are untouched.
8. **No weakened tests.** All existing tests still pass (§5.1); only additive
   tests were added.

---

## 5. Phase D — verification

### 5.1 Test commands executed (VERIFIED)

| Command | Result |
|---|---|
| `go build ./...` | **exit 0** |
| `go vet ./internal/cli/ ./cmd/izen/ ./internal/runtime/autonomy/ ./internal/architecture/` | **exit 0** |
| `go test ./internal/cli/ ./internal/runtime/orchestrator/ ./internal/architecture/ ./cmd/izen/ ./internal/app/ -count=1` | **ok** (all five) |
| `go test ./internal/cli/ -run 'Authoritative\|DriverTerminal\|TestParity' -count=1` | **ok** |
| `go test ./internal/runtime/autonomy/ -run TestScopeProvenanceMapping -count=1` | **ok** |
| `go test ./...` | see §5.5 |

New tests (all behavioral, not interface-shape):

* `internal/cli/authoritative_test.go` — approve ordering + authorization
  binding; reject/inspect never authorize; authorization refusal rejects;
  clarify resumes; inform parks without a resume; bridge failure aborts;
  cancellation preserved; terminal vocabulary preserved; clarify selection.
* `internal/cli/authoritative_parity_test.go` — legacy vs authoritative produce
  identical bytes on approve and leave the workspace untouched on reject.
* `internal/architecture/execution_ownership_convergence_test.go` — CLI seam lock
  + `RunAuthoritative` purity lock.
* `internal/runtime/autonomy/behavior_test.go` — `$orchestrate` provenance.

### 5.2 Static observations

* The `internal/app` and `internal/cli` packages remain imported only by their
  CLI commands (no TUI reachability) — unchanged.
* `RunCycle` is now unreachable from any production command; it survives only in
  tests and the `internal/verification` harness. It is **subordinated, not
  deleted** (rollback safety + parity).

### 5.3 Live execution evidence (VERIFIED — real local model)

Provider: local **Ollama `qwen2.5-coder:7b`** (real model; no simulation). No
credentials or paid API were used.

Workspace: isolated `mktemp` git repository containing only `keep.md`
(`# Fixture\n\nUnrelated file that must not change.\n`), committed as baseline.

Command:

```text
printf 'y\n' | ./bin/izen orchestrate -dir <tmp> \
  "Create a new file named hello.md with the content 'Hello everyone'."
```

Captured facts (all from the runtime's own ledger and audit, never from model
prose):

* **Entry point / resolved surface:** `izen orchestrate`, surface `$orchestrate`
  (`prompt.admitted`, `capability.granted` with read/analyze/propose/mutate/verify).
* **Selected runtime owner:** autonomy `Driver` (`loop.transition` ×9;
  `execution.admission.decision`; `execution.spec.frozen`).
* **Provider-call count:** **2** (`execution.provider.execution` ×2) —
  requirement pass + main generation.
* **Requested / effective token limits:** call #1
  `requested_output_tokens=512, effective_output_tokens=512`;
  call #2 usage `prompt_tokens=362, completion_tokens=13,
  finish_reason=stop`; aggregate usage known.
* **Proposal/approval transitions:** `approval.required` →
  `execution.summary status=awaiting_human parked=true boundary_action=approve`
  → operator `y` → `execution.mutation.started` → `execution.mutation.completed`.
* **Mutation evidence:** `EXECUTION_COMMITTED` with a postcondition digest;
  `.izen/patches/run-1-patch-1.json` = `{"file":"hello.md","original":"",
  "modified":"Hello everyone","applied":true}`.
* **Verification:** `execution.verification.completed` =
  `NOT_APPLICABLE` ("no verification configured for language") — an honest
  not-applicable, not a false pass.
* **Final terminal state:** `objective.evaluated state=PROVEN granted=true
  authority=objective_completion_authority`; `execution.finished Success=true
  Outcome=created`; `execution.summary status=completed termination_reason=
  "objective satisfied: created; objective PROVEN by evidence"`.
* **Changed artifacts / preservation:** `hello.md` created with exactly
  `Hello everyone`; `keep.md` byte-for-byte unchanged.

A first live attempt **before** the admission grant correctly parked at an
`inform` boundary with `objective BEHAVIORALLY UNPROVEN: AUTHORIZATION_BLOCKED
[runtime.serve]`, having already applied the bytes — a truthful unproven
terminal, not a fabricated success. This is the evidence that the objective gate
is real: without the workspace grant the runtime refuses to claim the objective,
even though the mutation landed.

### 5.4 What the live run does **not** prove

The live run exercises the migrated **command** end to end (admission → Driver →
RuntimeExecutor → approval → mutation → objective proof). It does not script a
keystroke-level TUI session; the TUI surfaces were proven deterministically in
Phase 1 and are unaffected here.

### 5.5 Full suite (VERIFIED)

```
go test ./...
```

**exit 0** — 212 packages `ok`, 29 packages with no test files, **zero `FAIL`
lines and zero panics**. No test was modified to pass; the only test edits are
the additive `$build`/`$orchestrate` provenance rows and the new ownership/parity
tests.

---

## 6. Remaining deviations (REMAINING)

The selected owner is subordinated, not deleted, and the following remain
explicitly unconverged. None is claimed converged.

1. **`izen run` → `internal/app.Pipeline`** — still an independent full `for{}`
   loop with its own extraction/repair/terminal `app.Result`. CLI-only.
2. **`internal/cli.Stack` + `orchestrator.RunCycle`** — retained for tests and
   the `internal/verification` harness; unreachable from production. This is the
   subordinated owner, kept for rollback/parity.
3. **`internal/engine` layered loop** — still a facade consumed by `/plan`,
   `/investigate`, `/review`.
4. **`runtime/kernel` + `kernelbridge`** — the execution plane; still the
   vocabulary at the mutation-commit seam.
5. **`modes/plan` + `modes/investigate`** — direct provider callers with their
   own loops (read-only lifecycle, deliberately separate).
6. **UI `/ask` stream + role fallback** — direct read-only provider calls.
7. **Secondary provider callers** — session-title, `/commit`, `$diagnose`,
   provider-internal retries.
8. **Budget bypass resolvers** — requirement pass, manifest pass, behavioral
   repair, UI ASK, plan synthesis (Phase 1 §7/§15).
9. **Proposal-preview envelope fidelity (newly observed, pre-existing):** when
   the model emits a `FILE_CREATE`-style artifact envelope, the rendered
   `CandidateDiff` can include the envelope while the applied bytes are the
   parsed inner content. The TUI renders the same `CandidateDiff`
   (`internal/ui/autonomous.go:920`; `approval_admission.go:181`), so this is a
   pre-existing runtime property, not introduced by this migration. It is
   disclosed rather than silently "fixed" because it belongs to the
   artifact-integrity owner, not the execution-topology owner.

---

## 7. Migration debt (explicit)

| Debt | Owner | Reason left |
|---|---|---|
| Delete `cli.Stack`/`RunCycle`/`FileExecutor` after parity is permanently pinned | `internal/cli` | kept as the rollback/parity boundary |
| Migrate `izen run` (`app.Pipeline`) onto the runtime | `internal/app` | needs capability/alignment/planner parity harness; larger change |
| Remove the `internal/engine` facade after parity | `internal/engine` | consumed by read-only modes |
| Promote `runtime/kernel` to the single execution plane | `runtime/kernel` | vocabulary convergence across all owners |
| Route `modes/plan` + `modes/investigate` through the provider seam | `internal/modes` | read-only lifecycle |
| Make `/ask`, title, `/commit`, diagnose explicit read-only lifecycle steps | `internal/ui` | conversational/read-only |
| Unify the budget resolvers behind one envelope | execution/autonomy | touches non-migrated surfaces |
| Proposal-preview envelope fidelity | artifact integrity | see §6.9 |

---

## 8. Conclusion

The migrated invariant now holds on `izen prompt` / `izen orchestrate`:

```text
ONE REQUEST → ONE ADMISSION (compose.Wire + preflight dispatch)
           → ONE AUTHORITATIVE RUNTIME (autonomy Driver, surface $orchestrate)
           → CONTROLLED EXECUTION-PLANE CAPABILITIES (RuntimeExecutor)
           → ONE AUTHORITATIVE TERMINAL RESULT (runtime ledger: PROVEN)
```

Verified, not assumed:

* The CLI command crosses the authoritative Driver seam and no longer constructs
  or runs its private proposal lifecycle (AST lock + source).
* Approve issues the candidate-bound authorization before resume; reject and
  inspect never apply; an authorization refusal rejects
  (behavioral tests).
* The legacy CLI path and the authoritative execution plane produce identical
  bytes for the same decision (execution parity test).
* A live CREATE completed with `objective PROVEN by evidence`, wrote exactly the
  requested bytes, preserved the unrelated file, in **2 provider calls**.
* The full test suite result is recorded in §5.5 / §6.

Not claimed converged: `izen run`, the layered engine, the kernel vocabulary,
`modes/plan`/`investigate`, the UI read-only provider calls, and the budget
resolvers remain, explicitly and testably fenced.
