# IZEN — Execution Topology Convergence

Date: 2026-10-09 · Branch: `fix/execution` · Base for this change: `d5fe1f2`.
Evidence baseline: `docs/report/IZEN_EXECUTION_TOPOLOGY_AUDIT.md` (branch base `0ddadd6`).

This report records an **implementation**, not another audit. Every finding from the
baseline audit was re-verified against the current source (HEAD `d5fe1f2`) before any
edit. The correction is deliberately staged: it establishes one authoritative runtime
owner for the two primary TUI execution surfaces, corrects the three causally-linked
execution-plane defects, and explicitly fences and discloses the remaining owners.

Distinctions used throughout:

* **VERIFIED** — proven by a test that ran on this branch, or by a live provider run.
* **OBSERVED** — read in the current source, with a citation.
* **REMAINING** — a known deviation, disclosed honestly and not claimed converged.

---

## 1. Root cause

**OBSERVED → ROOT CAUSE → OWNERSHIP CHANGE → REGRESSION TEST → EXECUTION EVIDENCE**

The audit's smallest seam was: admission resolves intent, operation, scope, target and
mutation authority, but **not the execution lifecycle owner**. The owner was selected
afterwards by command surface and composition wiring.

Re-verified at HEAD: `ExecuteRequest` (`internal/execution/executor.go:85`) carries
scope, mode, prompt, targets, strategy, intent, evidence and model, but no field that
selects the lifecycle owner. The owner is still chosen by `if m.autonomy != nil` /
`if m.autonomousDriver != nil` in `internal/ui/autonomy_route.go:196` and by the
`/build` branch of `internal/ui/commands.go:1015` → `runRuntimePrompt`.

The concrete defect corrected here:

> `$prompt` and `$hot` entered the bounded autonomy `Driver` (the lifecycle owner),
> while an **ordinary `/build` prompt dispatched `RuntimeExecutor.Execute` directly**
> from the UI (`runtime_cutover.go` → `runRuntimeExecuteCmd`), so the UI composition
> owned a second LLM execution lifecycle.

Ownership change: `/build` ordinary prompts now cross the **same** admission →
autonomy-party seam as `$prompt` when the runtime is wired, and the command surface is
carried explicitly (`"$build"`) so `/build` remains distinct from `$prompt`.

---

## 2. Before / after execution graph (actual call graph)

### Before

```text
$prompt
  handleInput → dispatchDirectives → routePromptDirective            commands.cs / intent_dispatch.go:287
    → admitNewExecutionRun / bindScopeProvenance(ScopeDynamic)
    → runAutonomyRoutedCmdExplicit                                   autonomy_route.go:56
      → autonomy.Engine.Decide → dispatchAutonomyTrace
        → executeAutonomyWorkspace                                   autonomy_route.go:163
          → executeAutonomyViaDriver → Driver.Run                     autonomous.go:43 / driver.go:587
            → ExecutorAdapter.Execute → RuntimeExecutor.Execute       adapter.go:588 / executor.go:1641
              → invokeMutation → invokeStream → provider.ExecuteStream
        ⇒ LIFE-OWNER: Driver

/build ordinary prompt  (SECOND, COMPETING OWNER)
  handleInput → handleMessageContent → runRuntimePrompt              commands.go:1015 / runtime_cutover.go:350
    → runRuntimeExecuteCmd → RuntimeExecutor.Execute (DIRECT)        runtime_cutover.go:57
      → invokeMutation → invokeStream → provider.ExecuteStream
  ⇒ LIFE-OWNER: UI + executor (no Driver; no objective lifecycle)
```

Terminal truth was produced by two different owners on the two paths (`RuntimeLoop`
state vs `ExecutionProof.Outcome`), and `/build` performed no requirement pass and no
run-level budget envelope.

### After

```text
$prompt                      /build ordinary prompt              $hot
  |                              |                               |
  routePromptDirective           runRuntimePrompt                routeHotfixThroughAutonomy
  (bind ScopeDynamic,            (bind ScopeDynamic,             (bind ScopeDeclared,
   surface="$prompt")             surface="$build",               surface="$hot")
  |                              admitNewExecutionRun)           |
  +------------------------------+-------------------------------+
                                 |
                    runAutonomyRoutedCmdExplicit                 autonomy_route.go:56
                                 |
                    autonomy.Engine.Decide → dispatchAutonomyTrace
                                 |
                    executeAutonomyWorkspace (handoff context, mode)
                                 |
                    executeAutonomyViaDriver → Driver.Run         autonomous.go:43 / driver.go:587
                                 |   (owns loop, continuation, budget bound, terminal truth)
                    ExecutorAdapter.Execute → RuntimeExecutor.Execute   adapter.go:588 / executor.go:1655
                                 |   (execution plane: context, provider seam, patch, verify)
                    invokeMutation → invokeStream → provider.ExecuteStream
```

`/build` and `$prompt` now share the execution **authority** (both `ScopeDynamic`,
`objective_completion.go:309` maps `$build` → `ScopeDynamic`) while remaining distinct
command **surfaces** (`d.req.Scope` carries the literal surface; the `command_mode`
axis renders it). `$hot` remains `ScopeDeclared`.

The only remaining direct-executor edge from the UI is the explicit **harness
fallback** (`m.autonomy == nil || m.autonomousDriver == nil`), which starts no loop and
is pinned by `TestExecutionOwnership_DirectExecutorDispatchIsFencedToHarnessFallback`.

---

## 3. Authoritative runtime owner

**VERIFIED** — the bounded autonomy `Driver` is the lifecycle owner for the migrated
paths.

* Entry point: `internal/runtime/autonomy/driver.go:587` `func (d *Driver) Run(ctx, objective)`.
* The loop: `driver.go` (`for !d.loop.State().IsTerminal()`), with per-step
  `RuntimeDeciding/Executing/Interpreting/Recovering` transitions.
* It invokes the provider only through its execution-plane capability:
  `internal/runtime/autonomy/adapter.go:588` `a.executor.Execute(ctx, execReq)`.
* The UI reaches it through the structural interface `autonomousDriver`
  (`internal/ui/autonomous.go:67`) — the UI never imports the runtime autonomy package.

The migration is **not** a new runtime abstraction: it is the existing proven Driver,
now also reached by `/build`.

---

## 4. Execution contract: fields, invariants, authoritative writers

The authoritative contract in the current architecture is the pair
(`autonomy.LoopRequest`, `execution.ExecuteRequest`) resolved once by admission and
carried to the runtime. This change adds the missing **owner and surface** identity at
the admission seam rather than inventing a parallel type.

| Field | Meaning | Authoritative writer | Transition rule |
|---|---|---|---|
| `ExecuteRequest.ScopeProvenance` | mutation authority (`ScopeNone/Dynamic/Declared`) | admission (`bindScopeProvenance`) | replaced per input; never inferred downstream |
| `ExecuteRequest.Strategy` | execution path decision | `IntentGateway.SelectStrategy` / autonomy | set once before dispatch |
| `ExecuteRequest.Targets` | resolved target set | admission | never re-derived by the model |
| `ExecuteRequest.MaxOutputTokens` | requested output budget | driver/adapter | clamped by `llmstep.ResolveMaxTokens` |
| `LoopRequest.Scope` (`d.req.Scope`) | command surface that authorized the run | UI `SetScope` (`$prompt`/`$build`/`$hot`) | copied verbatim to the proof/axis |
| `d.executionSurface` (UI) | recorded surface at the entry point | each entry point | consumed by `runAutonomousDriver` |
| `ContractID` (`execution.contract.go`) | immutable intent identity | `ContractRegistry.Resolve` | content-addressed; retries increment `AttemptID` |
| `MutationAuthorization.Scope` (new) | capability family / consumption boundary | `AuthorizeBuildCandidateContent` | consumed once per operation |

New field added by this change: `MutationAuthorization.Scope`
(`internal/core/authorization/types.go:70`) and `model.executionSurface`
(`internal/ui/model.go`). Both have a single writer and a named consumption/transition
rule; neither duplicates an existing owner field.

Invariant enforced: a downstream phase may not re-select intent, operation, target,
authority or budget. If it discovers the contract incomplete, it returns a typed
failure (e.g. `ErrArtifactPlaceholderEcho`, `ErrEmptyProposalPayload`,
`ErrArtifactRejected`) rather than continuing under a competing interpretation.

---

## 5. Removed or subordinated owners

| Component | Disposition in this change | Reason |
|---|---|---|
| UI direct-executor `/build` dispatch | **subordinated**: now the Driver path when wired | it owned a second lifecycle; no loop/continuation/objective policy |
| Driver | **confirmed** as lifecycle owner | owns bounded loop, budget bound, recovery, terminal truth |
| `RuntimeExecutor` | **confirmed** as execution plane | provider seam, artifact boundary, patch/apply/verify capability |
| `MutationAuthorization` per-shell consumption | **corrected** (scoped) | one grant no longer starves a multi-step verifier |
| `runAutonomyRoutedCmdExplicit` | **confirmed** as the single admission→runtime seam | all three surfaces cross it |
| `app.Pipeline`, `cli.Stack`/`orchestrator.RunCycle`, `internal/engine` layered loop, `runtime/kernel` competing substrate | **not touched; explicitly disclosed as remaining** | out of the migrated TUI surface; see §14 |

**Not** done: no new planner, driver, retry loop, or compatibility executor was added.
The only new types are two value fields with explicit consumption rules.

---

## 6. Provider-call inventory (as it stands)

Live provider seam (the only one the migrated paths use): `invokeStream`
(`internal/execution/executor.go:4737`) → `provider.ExecuteStream` (`:`, with a
non-stream `provider.Execute` fallback). It is reached by exactly one logical route on
the migrated paths: `Driver → ExecutorAdapter.Execute → RuntimeExecutor.Execute →
invokeMutation/invokeReadOnly → invokeStream`.

Every provider call is now an explicit lifecycle step under the Driver:

| Call | Trigger | Budget policy |
|---|---|---|
| requirement pass | `Driver.Run` objective derivation | fixed 512 (unchanged; see §7 debt) |
| main generation / bounded continuation | `invokeMutation` / `invokeArtifactBoundedStep` | `effectiveMaxOutput` → `llmstep.ResolveMaxTokens` |
| bounded full-artifact continuation (≤3) | `llmstep.DefaultMaxContinuationSteps` | same authoritative clamp |
| read-only continuation (≤3) | `invokeReadOnly` | same authoritative clamp |
| recovery re-prompt | `Driver` recovery, bounded by `MaxRecoveryChainDepth` | `Driver` explicit budget override |

The live run below reports **2 provider calls** for the CREATE scenario
(requirement pass + main generation), with the summary budgets `call #1 requested=512`,
`call #2 requested=4096` and `effective=unobserved` — the latter is an honest gap, not a
claim.

Remaining direct provider sites outside the migrated TUI surface are disclosed in §14.

---

## 7. Budget authority

Status: **partially corrected; ownership documented, not fully unified.**

* The Driver derives and owns the run-level bound:
  `driver.go` `d.loop.WidenBounds(…, autonomy.RunTokenBudget(d.resolved.Profile.MaxOutputTokens, …), …)`.
* The execution plane resolves and enforces the per-invocation effective budget:
  `effectiveMaxOutput` → `llmstep.ResolveMaxTokens(model, requested)` (`executor.go`
  in `invokeMutation`; same in `invokeReadOnly`). `llmstep` remains the documented
  single clamp.
* Requested/effective/actual accounting exists: `ModelInvocation` carries
  `TokenInput/TokenOutput/Known`; the live forensics summary renders
  `budgets: call #N requested=… effective=…` and aggregate tokens.
* Actual usage is aggregated by the Driver (`AggregatedUsage`), with a `Known` flag so
  "unavailable" is never conflated with zero.

**REMAINING (not corrected here):** the audit's five bypass resolvers still exist —
requirement pass (`512`), manifest pass (`200`), behavioral repair (`4096`), UI ASK
(`resolveASKMaxTokens`), and plan synthesis. Unifying them behind one owned envelope is
a larger change that touches non-migrated surfaces (`/plan`, `/ask`, behavioral stage);
it is disclosed rather than half-done. The run-level bound already prevents an
unbounded sequence: the loop terminates on the token bound.

---

## 8. Proposal lifecycle

**OBSERVED → ROOT CAUSE → OWNERSHIP CHANGE → REGRESSION TEST → EXECUTION EVIDENCE**

* Producer: `RuntimeExecutor` `compileDiff` (`executor.go:4341`) could legitimately
  return `""` for a CREATE (no baseline to anchor), yet the pending candidate was still
  staged with `res.Diff == ""`. The UI consumer then waited forever for a payload no
  producer filled.
* Correction (`executor.go` in `Execute`): when the compiled diff is empty, derive the
  whole-file-addition diff from the **same artifact bytes** the apply writes
  (`wholeFileAdditionDiff`), exactly as the candidate preview already does; if still no
  payload, fail **explicitly** with `ErrEmptyProposalPayload` before entering
  `OutcomePendingApproval`.
* Producer, consumer, completion, failure and cancellation now agree: the proposal
  payload is non-empty by construction, or the execution terminates with a typed
  failure. The UI renders `res.Diff` — it no longer maintains a competing producer.

Regression tests: `TestCreateProposalCarriesWholeFileDiff` (asserts a whole-file-addition
payload carrying `+Hello everyone`), plus the existing approval/verification tests.

---

## 9. Authorization lifecycle

**OBSERVED → ROOT CAUSE → OWNERSHIP CHANGE → REGRESSION TEST → EXECUTION EVIDENCE**

* Observed: `Runner.run` consumed the single-use token on every guarded invocation
  (`runner.go:330`); `Verifier.runStep` attached the **same** mutation token to every
  verification step. The first `sh`-command consumed it and the second was refused
  ("single-use and has already been consumed"), turning a successful mutation into a
  failed verification.
* Root cause: one grant shared by two capability families (file write + shell
  verification) and by a multi-step verifier — a consumption-boundary mismatch, not a
  need to make tokens reusable.
* Correction: `MutationAuthorization.Scope` (`types.go`). The engine mints
  `ScopeMutationOperation` for a build candidate (`engine.go:343`). The runner consumes
  only `ConsumesPerInvocation()` grants (`runner.go`); a mutation-operation grant is
  consumed **once**, at the operation's terminal state, via a `defer` in `Approve`
  (`executor.go:2523`). Single-use semantics are preserved and now scoped.
* Human approval and the candidate identity/digest binding (`Authorizes`) are unchanged;
  verification still cannot start without a valid, unexpired grant, and a rejected
  candidate never applies.

Regression tests: `TestMutationOperationGrantNotConsumedByRunner`,
`TestCapabilityInvocationGrantRemainsSingleUse`,
`TestMutationOperationGrantSurvivesMultiStepVerification` (two-step verifier passes under
one grant).

---

## 10. Artifact integrity

**OBSERVED → ROOT CAUSE → OWNERSHIP CHANGE → REGRESSION TEST → EXECUTION EVIDENCE**

* Observed: a `$prompt` CREATE of a Markdown target was corrupted by
  `rule_html_tag_balance` appending synthetic closing tags.
* Root cause (causal chain): the CREATE system instruction wrapped its content slot in
  angle brackets — `<the COMPLETE new file content, every line of it>` — making it
  indistinguishable from an HTML element. A model that copied the placeholder produced a
  payload the **content-only** ingestion classifier judged as an unbalanced HTML tag and
  "repaired".
* Correction, at the actual owners:
  1. `StrictArtifactContractInstruction` (`parser.go:140`) no longer angle-brackets any
     slot; placeholders are square-bracketed and cannot be read as markup.
  2. A placeholder echo is an **explicit** rejection: `isArtifactPlaceholderEcho`
     (`parser.go:115`), enforced in `invokeMutation` → `ErrArtifactPlaceholderEcho`
     (wrapping `ErrArtifactRejected`) before validation or any approval surface.
  3. Repair is contract-aware: `ingestion.ProcessWithPolicy(raw, allowMarkupRepair)`
     (`ingestion.go:39`). `invokeStream` receives `allowMarkupRepair` derived from the
     resolved target type (`isMarkupTarget`, `executor.go`), so a Markdown/plain target
     never proposes HTML tag balancing. Repair acceptance additionally refuses any
     payload that literal-copied the placeholder.

Raw output is preserved by the `IngestionTrace`, so corruption origin remains
attributable. Validation, verification and human approval were **not** weakened.

Regression tests: `TestArtifactContractPlaceholdersAreNotMarkup`,
`TestPlaceholderEchoDetectionIsPrecise`, `TestCreatePlaceholderEchoIsRejectedNotRepaired`,
`TestProcessWithPolicyGatesMarkupRepair`.

Live evidence: `zuru.md` content is exactly `Hello everyone` in both live scenarios
(§13).

---

## 11. Terminal truth

The runtime owns the terminal result on the migrated paths. The invariant is unchanged
from the audit's target: only the Driver's loop state is authoritative, and the executor's
`ExecutionProof.Outcome` is its per-invocation evidence, projected into the loop.

* Non-success states are not success: a parked proposal is `awaiting_human`, a launched
  verification is not verification success, a mutation is not objective completion.
* The live run shows the authority rewriting the loop's proposal from `complete` to
  `ask_human` until the mutation is approved, then `complete` only after
  `PROVEN by evidence` (`objective.evaluated` with `granted=true`).
* `CANCELLED` and `ABORTED` remain distinct and never report `PROVEN` (pinned by existing
  `r6_*` suites, which still pass).

**REMAINING:** the CLI/`izen run` (`app.Result`) and `izen prompt/orchestrate`
(`orchestrator.ExecutionResult`) still own their own terminal vocabularies; they are
fenced in §14.

---

## 12. Tests: commands executed and outcomes

All commands run on this branch, current working tree.

| Command | Result |
|---|---|
| `go build ./...` | **exit 0** |
| `go test ./internal/core/authorization/ ./internal/execution/...` | **ok** |
| `go test ./internal/ui/` | **ok** |
| `go test ./internal/runtime/autonomy/` | **ok** |
| `go test ./internal/architecture/ -run TestExecutionOwnership` | **PASS** |
| `go test ./...` | **no FAIL lines** (212 packages `ok`/no-test) |

New tests added:

* `internal/execution/verification_authorization_test.go` — authorization consumption boundary.
* `internal/execution/artifact_integrity_convergence_test.go` — placeholder + contract-aware repair.
* `internal/execution/ingestion/policy_test.go` — `ProcessWithPolicy` gate.
* `internal/execution/constrained_create_test.go` — `TestCreateProposalCarriesWholeFileDiff`.
* `internal/ui/execution_ownership_convergence_test.go` — `$prompt`/`/build` enter one runtime with distinct surfaces.
* `internal/architecture/execution_ownership_convergence_test.go` — AST lock on the single seam.
* `test/realworld/benchmark_test.go` — live Scenario B (`$build` surface) arm.

These assert ownership/lifecycle invariants (behavioral call edges, consumption
boundary, payload producer/consumer, artifact bytes), not merely that types compile.

---

## 13. Live execution evidence

Provider: local **Ollama `qwen2.5-coder:7b`** (real model; no simulation).

Command (both scenarios):

```text
IZEN_LIVE_FORENSICS=1 IZEN_BENCH_PROVIDER=ollama \
  IZEN_REALWORLD_ARTIFACTS=<temp> \
  go test ./test/realworld/ -run '<arm>' -v -count=1
```

The harness drives the **production composition** (`compose.Wire → autonomy.Driver →
RuntimeExecutor`) with the real provider and plays the human at the approval boundary
(`test/realworld/harness.go`).

### Scenario A — `$prompt Create a new file named \`zuru.md\` with the content "Hello everyone".`

Arm: `TestBenchmark_CreateCodeQuotedPrompt`. **PASS** (7.9 s).

* Authoritative runtime owner: autonomy `Driver` (`Driver.Run`).
* Execution contract: `ScopeDynamic`, surface `$prompt`, target `zuru.md`, CREATE.
* Provider-call count: **2** (requirement pass + main generation).
* Per-call: `call #1 requested=512 effective=unobserved`, `call #2 requested=4096 effective=unobserved`; aggregate provider-reported **input 368 / output 8** (`usage known`).
* Proposal/mutation transitions: `executing → verifying → awaiting_human (ask_human, mutation awaiting approval)` → `patch approved` → `observing → deciding → completed`.
* Verification: `applicable=false — no verification configured for language` (NOT_APPLICABLE, not a false pass).
* Final terminal state: `completed — objective satisfied: created; objective PROVEN by evidence`.
* `changed_contents["zuru.md"] == "Hello everyone\n"` — **exactly `Hello everyone`** (trimmed); `testfile.md` untouched.

### Scenario B — enter `/build`, then submit the same prompt

Arm: `TestBenchmark_CreateCodeQuotedBuildSurface` (`Surface: "$build"`). **PASS** (2.3 s).

* Authoritative runtime owner: the **same** autonomy `Driver`.
* Execution contract: `ScopeDynamic`, surface `$build`, target `zuru.md`, CREATE.
* Provider-call count: **2**; aggregate input **368** / output **14** (`usage known`).
* Proposal/mutation transitions: identical park→approve→complete sequence.
* Verification: NOT_APPLICABLE (no language), same as A.
* Final terminal state: `completed — objective PROVEN by evidence`.
* `changed_contents["zuru.md"] == "Hello everyone"` — **exactly `Hello everyone`**; unrelated file untouched.

### Honest limitation on Scenario B

Scenario B's **TUI routing** (`/build` ordinary prompt → `runRuntimePrompt` →
`runAutonomyRoutedCmdExplicit`) is proven **deterministically** by
`internal/ui::TestBuildOrdinaryPromptEntersAuthoritativeRuntime` and the AST lock
`internal/architecture::TestExecutionOwnership_AllExecutionSurfacesConvergeOnOneRuntime`.
The live arm exercises the **same runtime owner under the same `$build` surface**; it does
not script a keystroke-level interactive TUI session. This is stated plainly: the live
artifact proves the runtime lifecycle, approval, budget and terminal truth for the
`/build` surface; the interactive TUI wiring is proven by deterministic tests, not by a
live keystroke run.

Evidence artifacts persisted under the `IZEN_REALWORLD_ARTIFACTS` directory
(`*-events.ndjson`, `*-trace.ndjson`, `*-summary.json`).

---

## 14. Remaining deviations

Every known entry point/component that still violates the target invariant is listed.
None is claimed converged.

1. **`izen run` → `internal/app.Pipeline`** (`cmd/izen/runtime.go`, `internal/app/pipeline.go`):
   an independent `for{}` loop with its own extraction/repair/terminal `app.Result`. Not
   migrated; reachable only via the CLI subcommand, not the TUI.
2. **`izen prompt` / `izen orchestrate` → `internal/cli.Stack` + `internal/runtime/orchestrator.RunCycle`**:
   a second self-contained proposal loop (`cmd/izen/orchestrate.go`,
   `internal/cli/cli.go`, `internal/runtime/orchestrator/engine.go`). Not migrated.
3. **`internal/engine` layered loop** (`control/loop.go`, `retry.go`, `pipeline`):
   imported by the composition root (`internal/runtime/compose/compose.go` imports
   `engine/layer1`, `layer3`, `pipeline`) as a legacy facade. Not removed.
4. **`runtime/kernel` + `kernelbridge`**: self-declared substrate with its own `Outcome`;
   reached at the mutation-commit seam. Remains a parallel owner at the vocabulary level.
5. **`modes/plan.Engine` and `modes/investigate`**: direct provider callers with their own
   retry/tool loops (`/plan`, `/investigate`). Not routed through the runtime seam.
6. **UI `/ask` stream + role fallback** (`internal/ui/stream.go`, `role_fallback.go`):
   direct provider calls for conversation. Outside the mutation lifecycle by design, but
   still a direct provider caller.
7. **Secondary provider callers**: session-title refinement, `/commit` message generation,
   `runDiagnoseCmd`, and provider-internal retries (OpenRouter) — unchanged.
8. **Budget bypass resolvers**: requirement pass, manifest pass, behavioral repair, UI ASK,
   plan synthesis (see §7) — unchanged.

These are **fenced** (they are not reachable from the migrated TUI execution surfaces) and
the positive convergence lock prevents a future silent re-introduction of a second loop on
the `$prompt`/`/build`/`$hot` surfaces.

---

## 15. Migration debt (explicit)

| Debt | Owner | Reason left |
|---|---|---|
| Unify all budget resolvers behind one owned envelope | execution / autonomy | touches non-migrated `/plan`, `/ask`, behavioral stage; needs comparative token measurements |
| Migrate `izen run` (`app.Pipeline`) onto the runtime | `internal/app` | independent CLI path; large surface; requires parity harness |
| Migrate/remove `cli.Stack` + `orchestrator.RunCycle` | `internal/cli` | second proposal loop; needs proposal-lifecycle parity |
| Remove the `internal/engine` layered loop after parity | `internal/engine` | imported by composition root; legacy facade |
| Promote `runtime/kernel` to execution plane (single `Outcome`) | `runtime/kernel` | vocabulary convergence across all owners |
| Route `modes/plan` + `modes/investigate` through the provider seam | `internal/modes` | capability stages with their own loops |
| Make `/ask`, title, `/commit`, diagnose read-only provider calls explicit lifecycle steps | `internal/ui` | conversational/read-only, not mutation lifecycle |
| Interactive keystroke-level live `/build` run | `internal/ui` | deterministic UI test + live runtime arm used instead |

Each item names its owner and the reason it was not done in this change; none is hidden
behind a compatibility wrapper that pretends to be the authoritative runtime.

---

## 16. Conclusion

The corrected invariant holds on the migrated surfaces:

```text
ONE REQUEST → ONE ADMISSION → ONE AUTHORITATIVE RUNTIME (autonomy Driver)
           → CONTROLLED PROVIDER CALLS (execution plane seam)
           → ONE EXECUTION STATE MACHINE → ONE TRUTHFUL TERMINAL RESULT
```

Verified, not assumed:

* `$prompt` and ordinary `/build` enter the same runtime owner with distinct surfaces
  (`internal/ui` + `internal/architecture` tests).
* The proposal payload producer and consumer agree; an empty payload fails explicitly.
* The authorization grant's consumption boundary matches its documented meaning; a
  multi-step verifier no longer starves itself.
* A CREATE produces exactly the requested bytes; a placeholder echo is rejected, and
  markup repair is gated by the artifact contract.
* Both live scenarios completed with `PROVEN`, created `zuru.md` containing exactly
  `Hello everyone`, in 2 provider calls each.

Not claimed converged: the CLI, plan/investigate, kernel and legacy-engine owners listed
in §14 remain, explicitly and testably fenced.
