# IZEN — Execution Topology Audit

Date: 2026-10-09 · Branch: `fix/execution` · Base commit `0ddadd6`.

Method: code-and-call-graph reconstruction, not documentation. Every claim carries a
`file:line`. Observations are separated from causal inference. Where a sub-investigation
could not use the graph index (`check_index_coverage`), that is disclosed in §14.

This audit answers one question first:

> **How many execution/orchestration owners currently exist, what does each own, and which
> one is actually authoritative for a single user request?**

---

## 1. Executive finding

```
MULTIPLE EXECUTION OWNERS
```

For one user request there is a single **mutation authority**
(`internal/execution.RuntimeExecutor`) but **no single owner of the LLM execution
lifecycle**. The lifecycle is owned by different components depending on the command surface,
the active mode, and composition-time wiring:

* `$prompt` (production, build workspace) → `internal/runtime/autonomy.Driver` drives a bounded
  agent loop which calls the executor. `internal/runtime/autonomy/driver.go:587`,
  `internal/runtime/autonomy/driver.go:2256`, `internal/runtime/autonomy/adapter.go:588`.
* `/build` ordinary prompt and staged per-task build → the UI calls the executor **directly**,
  with no Driver. `internal/ui/runtime_cutover.go:350`, `internal/ui/runtime_cutover.go:301`,
  `internal/ui/runtime_cutover.go:57`.
* `izen prompt` / `izen orchestrate` → `internal/cli.Stack` + `internal/runtime/orchestrator`,
  a second, self-contained proposal loop that "never enters autonomy.Driver and never loops
  internally". `cmd/izen/orchestrate.go:43`, `internal/cli/cli.go:397`,
  `internal/runtime/orchestrator/engine.go:101`.
* `izen run` → `internal/app.Pipeline`, a third full loop (compile → generate → extract →
  align → validate → repair → plan → substrate). `cmd/izen/runtime.go:198`,
  `internal/app/pipeline.go:290`.
* `runtime/kernel` self-declares as "the single authoritative substrate that decides what
  executes", and `runtime/headless.Runner` drives it end-to-end with no UI, but it is currently
  reached only through `internal/kernelbridge` at the mutation-commit seam.
  `runtime/kernel/doc.go:1-40`, `runtime/headless/headless.go:63`, `internal/kernelbridge/kernelbridge.go`.
* `internal/engine` (layers 0–4, `control/loop.go`, `retry.go`) is a fourth legacy loop, still
  reachable from the TUI (`internal/ui/model_stream.go:9`, `internal/ui/commands.go:33`) and from
  the executor's mutation path (`internal/execution/mutationset.go:8`,
  `internal/execution/patch.go:18`).

The repo's own tests document this state honestly. The kernel strangler lock says:

> "the three legacy execution stacks keep working while the slice-by-slice traffic moves onto
> the kernel"
> `test/architecture/kernel_lock_test.go:38-42`

and the P0-2 convergence test proves only that one **mutation** authority is wired
(`internal/architecture/phase1_authorization_boundary_test.go:323`,
`TestPhase1_SingleProductionExecutionAuthority`). It does **not** assert a single LLM-lifecycle
owner; the code it inspects has no concept of one.

The result is exactly the failure class in the brief: planning, requirement derivation, context
compilation, provider invocation, artifact repair, authorization, verification and terminal
truth are split across components that are each individually defensible and collectively
incoherent. The observed symptoms follow causally (§13).

---

## 2. Actual execution graph

### 2.1 Flow A — `$prompt Create a new file named zuru.md …`

```text
handleInput                        internal/ui/commands.go:187
  └─ intentFromInput               internal/ui/intent_dispatch.go:31
      └─ dispatchDirectives        internal/ui/intent_dispatch.go:213
          └─ routePromptDirective  internal/ui/intent_dispatch.go:287
              ├─ admitNewExecutionRun            internal/ui/execution_admission.go:155
              ├─ bindScopeProvenance(ScopeDynamic) internal/ui/intent_dispatch.go:329
              └─ runAutonomyRoutedCmdExplicit    internal/ui/autonomy_route.go:56
                    └─ autonomy.Engine.Decide    internal/autonomy/engine.go (deterministic)
                        └─ dispatchAutonomyTrace internal/ui/autonomy_route.go:91
                            └─ executeAutonomyWorkspace internal/ui/autonomy_route.go:163
                                └─ executeAutonomyViaDriver internal/ui/autonomous.go:43
                                    └─ Driver.Run          internal/runtime/autonomy/driver.go:587
                                        ├─ deriveObjectiveRequirements  driver.go:833
                                        │    └─ InvokeRequirementPass    internal/execution/requirement_pass.go:254  ← PROVIDER CALL #1
                                        └─ observeAndRun / loop         driver.go:2146 / 2256
                                            └─ ExecutorAdapter.Execute  adapter.go:383
                                                └─ RuntimeExecutor.Execute executor.go:1641
                                                    └─ invokeMutation      executor.go:2174/3329
                                                        └─ invokeStream     executor.go:4652
                                                            └─ provider.ExecuteStream/Execute executor.go:4729/4737  ← PROVIDER CALL #2(+)
                                                    (recovery re-enters RuntimeExecuting) driver.go:2528 → provider
```

Terminal truth: `Driver` owns the loop and produces `RuntimeCompleted /
RuntimeUnsubstantiated / RuntimeAborted` (`internal/autonomy/runtime_loop.go:912-1112`);
`RuntimeExecutor` owns the per-invocation `ExecutionProof.Outcome`
(`internal/execution/executor.go:411-432`). Two terminal vocabularies, two owners.

### 2.2 Flow B — `/build` then ordinary prompt

```text
handleInput                        commands.go:187
  └─ parseModeShorthand            commands.go:350 → setMode(ModeBuild) commands.go:1677
  └─ handleMessageContent          commands.go:588
      └─ (ModeBuild) runRuntimePrompt   commands.go:1015 → runtime_cutover.go:350
          └─ runRuntimeExecuteCmd       runtime_cutover.go:39
              └─ m.executor.Execute     runtime_cutover.go:57
                  └─ invokeMutation …   executor.go:2174 … (same executor, NO Driver)
```

Staged `/build` plan:

```text
handleBuildRun                     commands.go:2885
  └─ dispatchStagedTask            commands.go:2988
      └─ runRuntimeTaskRequest     runtime_cutover.go:301
          └─ runRuntimeExecuteCmd  runtime_cutover.go:57 → executor
```

`/build`'s queue advancement, verification and fix loop are owned by the **UI**:
`projectBuildQueueFromProof` (`internal/ui/gateway.go:780`) → `runTestEngine`
(`commands.go:3138`) → `testResultMsg` → `runFixCmd` (`update.go:1614`). This is a second,
UI-owned recovery loop that can re-invoke the provider via `streamCmd`
(`internal/ui/update.go:2044`).

### 2.3 The other three top-level owners

| Entry | Owner | Loop | Terminal owner |
|---|---|---|---|
| `$prompt` / `$hot` (autonomy wired) | `runtime/autonomy.Driver` | `driver.go:2256` | `internal/autonomy.RuntimeLoop` |
| `$prompt` fallback (autonomy nil) | `internal/ui` gateway | none (single dispatch) | `res.Proof.Outcome` |
| `/build` ordinary + staged | `internal/ui` + `RuntimeExecutor` | UI queue + executor internals | `ExecutionProof` + UI test loop |
| `izen prompt` / `orchestrate` | `cli.Stack` → `runtime/orchestrator` | `RunCycle` single cycle | `orchestrator.ExecutionResult` |
| `izen run` | `app.Pipeline` | `for {}` `pipeline.go:394` | `app.Result` |
| `runtime/headless` | `kernel.Engine` | `engine.Run` `runtime/kernel/engine.go` | `kernel.Outcome` |
| `izen` TUI `/ask` | `internal/ui` stream | role-fallback | `streamDoneMsg` |
| `/plan` | `modes/plan.Engine` | synthesis loop + retry | plan contract |
| `/investigate` | `modes/investigate.Engine` | dispatcher/toolrunner | investigation evidence |

---

## 3. Execution-owner table

`Provider Call` = can directly invoke an LLM. `Loop` = owns an iteration that can reach the
provider. `Retry` = can cause a second provider call. `Budget` = can set/override output
budget. `Repair` = can re-prompt on artifact/diagnostic failure. `Termination` = owns a
terminal state. `Authority` = what it is authoritative for.

| Component | Provider Call | Loop | Retry | Budget | Repair | Termination | Authority |
|---|---:|---:|---:|---:|---:|---:|---|
| `runtime/autonomy.Driver` (`driver.go:587`) | indirect | **Y** `:2256` | **Y** | **Y** `:751` | **Y** `:2528` | **Y** loop state | agent lifecycle (on `$prompt`) |
| `internal/autonomy.RuntimeLoop` (`runtime_loop.go:809`) | no | **Y** `:948` | bounds only | **Y** `RunTokenBudget:647` | no | **Y** `:1079-1112` | loop progression/bounds |
| `internal/execution.RuntimeExecutor` (`executor.go:1641`) | **Y** `:4729` | **Y** per-target `:3429` | **Y** `:2186` | **Y** `llmstep` `:3403` | **Y** ingestion `:4798` | **Y** `ExecutionProof` | mutation + verification |
| `internal/ui` gateway (`gateway.go:44`) | no | no | no | no | no | **Y** projection | presentation of result |
| `internal/ui` build queue (`gateway.go:780`) | indirect | **Y** `:850` | **Y** `runFixCmd` | no | **Y** test-fix `update.go:1602` | **Y** build ledger | `/build` queue |
| `internal/ui` ASK stream (`stream.go:649`) | **Y** `role_fallback.go:133` | fallback | **Y** `role_fallback.go:152` | **Y** `stream.go:567` | no | **Y** stream | `/ask` chat |
| `app.Pipeline` (`pipeline.go:290`) | **Y** `pipeline.go:517` | **Y** `:394` | **Y** (`maxAttempts`, `maxRepairs`) | **Y** via prompt builder | **Y** `:408/:450` | **Y** `Result` | `izen run` |
| `cli.Stack` + `runtime/orchestrator` (`cli.go:397`, `engine.go:101`) | **Y** `cli.go:78` | single cycle | no | **Y** `TokenBudget` `cli.go:404` | no | **Y** `ExecutionResult` | `izen prompt/orchestrate` |
| `runtime/kernel.Engine` (`engine.go`) | no (no LLM capability) | **Y** | budget | **Y** `budget.go` | no | **Y** `Outcome` | claims to be *the* substrate |
| `runtime/headless.Runner` (`headless.go:63`) | no | **Y** | no | kernel | no | **Y** `kernel.Result` | headless kernel driver |
| `modes/plan.Engine` (`plan/engine.go:725`) | **Y** `:763/:828` | **Y** `:1735` | **Y** `maxSilentRetries=2` | **Y** `synthesis_step.go:44` | **Y** synthesis step | plan contract | `/plan` |
| `modes/investigate` (`dispatcher.go:169`, `toolrunner.go:201`) | **Y** | dispatcher | no | no | tool loop | evidence | `/investigate` |
| `internal/engine` (`control/loop.go`, `pipeline/engine.go:539`) | **Y** `engine.go:539` | **Y** decision loop | **Y** `retry.go` | no | no | node states | legacy layered engine |
| `ProviderRepairProposer` (`evidence_reasoner.go:175`) | **Y** | behavioral loop | no | **Y** 4096 | **Y** | no | behavioral repair |

**Nine components can directly invoke the provider.** Five of them own a loop that can
re-invoke it. There is no component whose single responsibility is "the execution lifecycle".

---

## 4. Provider-call inventory

Two provider interfaces exist; only one is live.

* Live: `ai.Provider` — `internal/ai/provider.go:545` (`Execute` `:547`, `ExecuteStream` `:548`).
* Dormant: `llm.LLMProvider` — `internal/llm/provider.go:104`; constructors are called only from
  `internal/llm/*_test.go`, so its HTTP clients are dead in the current binary.

Live concrete implementations: Ollama, Groq, Gemini, OpenCode, NineRouter, OpenRouter, Claude,
OpenAI (`internal/providers/*.go`), all wrapped by `contextcompiler.PreparedProvider`
(`internal/contextcompiler/provider.go:104/112`, wired at `compose.go:591`, `:604`).

Every production LLM call site (non-test):

| # | Site | Function | Reachable from |
|---|---|---|---|
| 1 | `cmd/izen/runtime.go:65` | `cliGenerator.Complete` | `izen run` (via `app.Pipeline`) |
| 2 | `cmd/izen/runtime.go:91` | `semanticExtractorAdapter.Extract` | `izen run` |
| 3 | `cmd/izen/orchestrate.go:181` | `orchestrateAdapter.Complete` | `izen prompt`, `izen orchestrate` |
| 4 | `internal/cli/cli.go:78` | `ProposalProviderCLI.GenerateProposal` | `cli.Stack` |
| 5 | `internal/runtime/orchestrator/engine.go:170` | `RunCycle` | `cli.Stack` |
| 6 | `internal/execution/executor.go:4409` | `InvokeManifestPass` | `$prompt`/autonomy (preflight) |
| 7 | `internal/execution/requirement_pass.go:254` | `InvokeRequirementPass` | **every `$prompt`/autonomy run** |
| 8 | `internal/execution/executor.go:4729/4737` | `invokeStream` | all execution paths |
| 9 | `internal/execution/artifact_step.go:225` | bounded full-artifact step | `/build`, `$prompt` mutations |
| 10 | `internal/execution/executor.go:3657` | patch-only mutation | `/build`, `$prompt` |
| 11 | `internal/execution/executor.go:4550` | `invokeReadOnly` | ask/read-only |
| 12 | `internal/execution/evidence_reasoner.go:175` | behavioral repair proposer | autonomy behavioral stage |
| 13 | `internal/runtime/autonomy/driver.go:2394` | Driver executing step | `$prompt` |
| 14 | `internal/runtime/autonomy/decomposition.go:1069` | DAG sub-task | autonomy |
| 15 | `internal/modes/investigate/dispatcher.go:169` | `llmClassify` | `/investigate` |
| 16 | `internal/modes/investigate/toolrunner.go:201` | `runDiagnose` | `/investigate $diagnose` |
| 17 | `internal/modes/plan/engine.go:725` | plan synthesis | `/plan` |
| 18 | `internal/ui/stream.go:649` → `role_fallback.go:133/152` | ASK stream + fallback | TUI `/ask` |
| 19 | `internal/ui/session_title.go:97` | title refinement | TUI (background) |
| 20 | `internal/ui/commands.go:4373` | `runDiagnoseCmd` | TUI `$diagnose` |
| 21 | `internal/ui/agents.go:781` | commit-message generation | TUI `/commit` |
| 22 | `internal/runtime/compose/compose.go:1226` → `engine/pipeline/client.go:28` → `layer3/worker.go:244` | layered pipeline | TUI facade |

**Second-call engines (one logical action → ≥2 provider calls):**

1. `invokeStream` falls back from `ExecuteStream` to `Execute` on stream failure
   (`executor.go:4729` → `:4737`).
2. Full-artifact bounded continuation, up to `DefaultMaxContinuationSteps = 3`
   (`internal/llmstep/step.go:56`; `artifact_step.go:195/225`).
3. Read-only continuation loop (`executor.go:4514/4550`).
4. Hallucinated-anchor retry, a second `invokeMutation` (`executor.go:2175-2202`).
5. `app.Pipeline` extraction/validation/alignment repair loop
   (`internal/app/pipeline.go:394-469`, `maxAttempts`/`maxRepairs` `:226`).
6. Plan synthesis retry (`maxSilentRetries=2`, `internal/modes/plan/engine.go:1735-1755`).
7. UI cross-provider role fallback (`internal/ui/role_fallback.go:133/152`).
8. OpenRouter provider-internal retry (400-reasoning once; 429 backoff,
   `internal/providers/openrouter.go:959-985`).
9. OpenRouter read-only tool loop, up to 4 turns (`internal/ai/toolloop.go:9/45`).
10. Autonomy DAG: one call per sub-task (`internal/runtime/autonomy/decomposition.go:1069`).

---

## 5. Token-flow analysis

For one `$prompt` CREATE, production wiring:

```text
USER INPUT TOKENS            : objective text (small)
RUNTIME PROMPT TOKENS        : strict artifact contract + manifest/requirement prompts
CONTEXT TOKENS               : executor compileContext (observeSnapshot)
OBJECTIVE TOKENS             : requirement pass user message (objective + resolved targets)

call #1  purpose: objective requirement derivation   owner: autonomy.Driver → executor
         budget: RequirementPassMaxTokens = 512        requirement_pass.go:47/232
         max_output: 512   actual: model-dependent    reject ceiling 1024  requirement_pass.go:52
         → on non-JSON: "requirement derivation unavailable"  objective_lifecycle.go:204
                        (non-fatal; objective stays UNDERSTOOD, never REQUIREMENTS_DERIVED)

call #2  purpose: manifest pass (only if preflight requires; usually 0 for CREATE)
         budget: manifestPassMaxTokens = 200           executor.go:4298/4381
         max_output: 200   reject ceiling 512

call #3+ purpose: main CREATE generation              owner: executor invokeMutation
         budget: llmstep.ResolveMaxTokens (mutation requested 1200; ASK 1536)
                 executor.go:3403/3611, llmstep/step.go:48/68
         max_output: min(requested, provider ceiling, constrained 980)
         continuations: up to 3 further calls           artifact_step.go:195, step.go:56

call #n  purpose: recovery re-generation (optional)   owner: Driver repair → executor
         up to MaxContractRecoveryAttempts = 2, recovery cycles 2, attempts 3
         driver.go:356, recovery.go:285/484

call #m  purpose: behavioral repair (optional)        owner: ProviderRepairProposer
         budget: behaviorDefaultOutputBudget = 4096    evidence_reasoner.go:144/160

RETRY/FALLBACK TOKENS        : own re-billing on every attempt (model context is re-projected)
REPAIR TOKENS                : the model's semantic content can be rewritten by ingestion repair
                               without a provider call (see §6), so "repair" is not uniformly billed
```

**At least eight independent code paths construct or overwrite the per-request output
budget**, none subordinate to a single owner:

| # | Setter | Site |
|---|---|---|
| 1 | `llmstep.ResolveMaxTokens` (documented "the ONE budget resolution") | `internal/llmstep/step.go:59-68` |
| 2 | executor `effectiveMaxOutput` / constrained clamp 980 | `internal/execution/executor.go:3312/3421` |
| 3 | context compiler overwrites `req.MaxTokens` | `internal/contextcompiler/request.go:189` |
| 4 | requirement pass fixed 512 | `internal/execution/requirement_pass.go:47/232` |
| 5 | manifest pass fixed 200 | `internal/execution/executor.go:4298/4381` |
| 6 | behavioral repair 4096 | `internal/execution/evidence_reasoner.go:144/160` |
| 7 | UI ASK `resolveASKMaxTokens` (4096 / casual 2048) | `internal/ui/stream.go:42/467/567` |
| 8 | plan synthesis `synthesisBudget` | `internal/modes/plan/synthesis_step.go:44`, `plan/engine.go:1462-1476` |
| 9 | driver `retry_with_explicit_budget` override | `internal/runtime/autonomy/driver.go:1218`, `adapter.go:446` |
| 10 | provider transport clamps (OpenRouter 4096/8192/980, OpenAI 4096/8192, Ollama 4096) | `internal/providers/openrouter.go:158-167`, `openai.go:79-83`, `ollama.go:100` |

The intent of `llmstep` is explicit — *"This is the ONE budget resolution … modes and the
executor MUST NOT ship a separate model-output-budget resolver"* (`internal/llmstep/step.go:13`,
`:59-68`) — and the codebase violates it in at least five places. The `OUTPUT_EXHAUSTED`
continuation is therefore computed against budgets that different components disagree about.

---

## 6. Proposal lifecycle

### 6.1 Producer → transport → consumer

```text
PRODUCER           RuntimeExecutor.invokeMutation builds patch + diff
                   executor.go:3962-3968 (compileDiff), :2433-2438 (res.Diff = diffs[0])
                   held at approval gate: executor.go:2465-2471 (pending map, PendingPatchID,
                   OutcomePendingApproval)

TRANSPORT          NO channel/event is the carrier. The gate emits
                   events.ApprovalRequired (graph.WaitApproval, executor.go:2473), which the UI
                   CONSUMES AS A LOG LINE ONLY (model.go:3411-3412). The actual payload travels
                   as the return value of a Bubble-Tea command closure:
                   runtime_cutover.go:56-62 → gatedExecutionMsg.

CONSUMER           executionResultUpdate  internal/ui/gateway.go:378
                   stages m.pendingProposals at gateway.go:641-650 (Diff: res.Diff)
                   enters approval at gateway.go:652
                   rendered at view.go:511-513
```

### 6.2 The orphaned wait

`view.go:512` renders `"Waiting for proposal payload..."` whenever
`state == StateAwaitingApproval && pendingProposals[0].Diff == ""`. There is exactly **one**
non-test writer of `pendingProposals` (`gateway.go:641`), and it writes `Diff: res.Diff` once.
No code path ever fills `Diff` later.

So if `res.Diff == ""`, the wait is **orphaned**: producer and consumer are the same
synchronous expression, and the consumer is waiting for a producer that will never run.

### 6.3 Why `res.Diff` is empty while the patch is valid

`res.Diff` is the best-effort compiled diff:

* `compileDiff` (`executor.go:4256-4265`): *"a failure (output the pipeline cannot map) leaves
  Diff empty"*; returns `""` if `changeset.NewPipeline().Run` errors or yields 0 changes.
* For a plain-text `create_file` against an **empty original**, `changeset.classifyBlock`
  (`internal/changeset/extractor.go:300-333`) cannot anchor/match and returns
  `ErrAmbiguousChange`/`ErrFullFileRejected`, so `compileDiff` returns `""`.
* The artifact parser accepts the same bytes through a *different* resolver:
  `ResolveModifiedContent(original="", raw)` returns the trimmed, de-fenced input verbatim
  (`internal/execution/patch.go:2145-2162`; called at `executor.go:3826`).
* Result: `res.PendingPatchID != ""` and `res.Diff == ""`.

The executor's own candidate preview computes a fallback diff when `pm.diffs` is empty
(`wholeFileAdditionDiff`, `executor.go:3107-3126`), but `res.Diff` at `executor.go:2438` never
gets that treatment. The two approval projections disagree about the same artifact.

### 6.4 The simultaneous "Connecting…" footer

Staging the proposal opens a **new** foreground operation without finalizing it:

* `gateway.go:651` `m.beginOperation(OpHotfix)` → `operation.go:196-203` sets
  `executionStartedAt`, `activeOp`, `agentRunning = true`.
* `isExecuting()` keys off `activeOp != nil` (`internal/ui/footer.go:110-117`);
  `firstTokenReceived` is false after the reset (`footer.go:388-405`); therefore
  `renderExecutingFooter` prints `"Connecting... Ns [provider/model]"` (`footer.go:491-508`).
* The `StateWorkspacePatch` skeleton (`model.go:3361-3370`) is released only by
  `MutationStarted` (`model.go:3382`) or `finalizeOperation` (`operation.go:281`); the proposal
  branch calls neither. Hence `[mutation] Staging edit @ zuru.md...` persists too.

This is a UI state machine reporting progress that does not correspond to any in-flight
execution — a direct instance of the "UI reports progress that does not correspond to the
authoritative execution state" failure in the brief.

**Verdict for the `/build` stall: Case D (a proposal consumer with no producer), triggered by
a parser/diff-producer disagreement, with a Case-G presentation overlay (two UI state machines
not released at the gate).** It is not a slow provider call, not a second provider call, and not
a lost stream completion.

---

## 7. Authorization lifecycle

### 7.1 Sequence as implemented

```text
CREATE   authorizeExecutorApproval           internal/ui/runtime_executor.go:101-124
           └─ AuthorizeBuildCandidate(humanApproved=true, candidateID)
                └─ AuthorizeBuildCandidateContent  core/authorization/engine.go:289
                     ├─ SingleUse = !mutBudget.IsMultiStepPlan()   engine.go:333; budget.go:119
                     └─ mint MutationAuthorization (ID, CandidateID, Digest, +5m)  engine.go:335-344

ATTACH   m.executor.SetAuthorization(auth)   runtime_executor.go:123 → executor.go:1015
         on Approve: patches.SetAuthorization  executor.go:2614
                     verifier.SetAuthorization  executor.go:2622
         identity gate: x.auth.Authorizes(patchID, digest)  executor.go:2496-2505

VALIDATE candidate identity only (no consume)
CONSUME  (*Runner).run → markAuthConsumed(r.auth)   internal/execution/runner.go:330
         (the ONLY non-test caller; auth.go:23-30)
GATE 1   FILE_MUTATE  patch.go:634 checkAuthorization  (validates, never consumes)
GATE 2   SHELL_EXEC   runner.go:222 checkAuthorization
REFUSE   runner.go:222 / patch.go:634 → auth.go:52-55
         "authorization token <id> is single-use and has already been consumed"
```

### 7.2 What the token actually authorizes

The token is minted as a **mutation** authorization (it is bound to the candidate ID and digest,
`engine.go:335-344`) but the only function that consumes it is the **shell** runner. Therefore,
in practice, a single-use token authorizes:

> **one capability invocation — whichever of {file write, first shell command} reaches its gate
> first — not "the mutation", and not "the run".**

### 7.3 The verification trap

`Approve` applies the patch and then runs verification with the **same** token:

1. Apply held patches: `executor.go:2656-2659` (`PatchManager.apply`, `checkAuthorization` only).
2. Verification loop: `verify.go:466-477`.
3. Each step: `runStep` creates a fresh `Runner` and attaches `v.auth`
   (`verify.go:482-488`).
4. First verification command runs → `runner.go:330` `markAuthConsumed`.
5. Second verification command → `checkAuthorization` → `auth.go:52-55` → **"single-use and has
   already been consumed"**.

Because `runStep` treats any error as `Passed=false` (`verify.go:494-506`), a successful
mutation is reported as a **failed verification**, and the apply gate may roll back
(`executor.go:2616-2621` wires the verifier as the apply gate). This is exactly the observed
"authorization token … consumed" during verification after accept.

### 7.4 Intended authority

The code names two different intended authorities on the same object:

* `core/authorization` intends: "authorization for **mutation**" (`engine.go:335-344`,
  `CandidateID`/`CandidateDigest` binding).
* `internal/execution/runner.go` treats it as: "authorization for **one shell capability
  invocation**" (`markAuthConsumed` at `:330`).

The defect is not that the token is single-use; it is that **one single-use token is shared by
two capability families** (file write and shell exec) and by a multi-step verifier, so the
first consumer starves the rest. Do not change token semantics until the owner of
"the mutation operation" is unified — §11.

---

## 8. `$prompt` vs `/build`

They converge on scope/operation/target at admission and on the same mutation authority
(`RuntimeExecutor.Execute`) — but they do **not** converge on the execution lifecycle owner.

| Stage | `$prompt` | `/build` ordinary prompt | Divergence |
|---|---|---|---|
| Admission | `routePromptDirective` → `ScopeDynamic` | `runRuntimePrompt` → `ScopeDynamic` (`runtime_cutover.go:353`) | converges |
| Decision | `autonomy.Engine.Decide` | none (straight to executor) | **diverges** |
| Loop owner | `Driver` (`driver.go:2256`) | none (single dispatch) | **diverges** |
| Objective contract | derived, requirement pass, PROVEN machine | none | **diverges** |
| Requirement pass | 1 provider call per run | **0** | **diverges** (budget + context differ) |
| Provider owner | `Driver → adapter → executor` | `UI → executor` | **diverges** |
| Mutation authority | `RuntimeExecutor` | `RuntimeExecutor` | converges |
| Verification | driver objective verify + executor apply gate | UI `runTestEngine` + executor apply gate | **diverges** |
| Terminal truth | `RuntimeLoop` state | `ExecutionProof` + UI build ledger | **diverges** |

The predecessor audit already disclosed this:

> "`/build` uses the single-shot executor, `$prompt` uses the bounded driver."
> `docs/report/IZEN_EXECUTION_CONTRACT_AUDIT.md:257-263`

and

> "A bare text typed while in `/build` is routed into autonomy (→ Driver), whereas
> `/build <content>` and staged `/build` go direct to the executor."
> (Subagent trace; see `internal/ui/commands.go:503-544`, `commands.go:1015`,
> `runtime_cutover.go:350`.)

So the *same semantic request* reaches the model through two different prompt budgets, two
different context-compilation calls, two different recovery policies, and two different terminal
vocabularies. This is the primary answer to "why does `$prompt` work through one execution path
while `/build` behaves differently": **they are different runtimes that happen to share a
mutation primitive.**

---

## 9. Root architectural defect

**The smallest seam that explains the largest number of symptoms:**

> Admission (the `IntentGateway` / `ExecuteRequest`) decides *intent, operation, scope, target,
> and mutation authority* — but **not the execution owner**. The lifecycle owner is selected
> afterwards by composition-time wiring (`m.autonomy != nil`, `m.autonomousDriver != nil`),
> presentation mode, and CLI subcommand — never by the contract.

Evidence:

* `runGatedLine` says "The UI NEVER decides the execution path … it submits the request and
  renders the canonical runtime events" (`internal/ui/gateway.go:20-28`) — yet the decision
  `if m.autonomy != nil` / `if m.autonomousDriver != nil` happens one layer up in
  `intent_dispatch.go:321-325` and `autonomy_route.go:196-199`.
* `ExecuteRequest` (`internal/execution/executor.go` type, constructed at
  `runtime_cutover.go:159-171`) carries scope, mode, prompt, targets, strategy, intent, evidence,
  model — but **no execution-contract identity, no lifecycle owner, no budget envelope, no
  termination policy**.
* There is no object that says "this request will be executed by exactly one owner with this
  budget and this termination condition". Each caller re-derives its own.

This seam explains, in one stroke:

1. `$prompt` vs `/build` semantic divergence (§8).
2. Two terminal vocabularies (`RuntimeLoop` states vs `ExecutionProof.Outcome`).
3. The requirement pass executing on one path and not the other (§5).
4. Multiple budget resolvers (§5) — no contract carries the budget.
5. The authorization-consumed-during-verification trap (§7) — no contract says which capability
   family the grant covers.
6. The orphaned `Diff` wait (§6) — the artifact/approval contract is not part of admission, so
   producer and consumer disagree.
7. Four headless/TUI runtimes, each with its own admission front-end.

Not a bug in any one of those components. A missing boundary between admission and execution.

---

## 10. Target architecture

Make **one** component the single execution owner for a request, downstream of one admission
seam:

```text
HUMAN
  │
  ▼
COMMAND / WORKSPACE ────────► ADMISSION (IntentGateway + scope + grant)
  │                              │
  │                              ▼
  │                     EXECUTION CONTRACT
  │                     - request id / objective id
  │                     - operation · scope · target set
  │                     - authority (capability grant + candidate binding)
  │                     - budget envelope (input/output/tokens/attempts)
  │                     - termination policy (what "done" means)
  │                              │
  │                              ▼
  │                     SINGLE RUNTIME OWNER
  │                     - context compilation
  │                     - provider invocation (ONE seam)
  │                     - output budget (ONE resolver)
  │                     - proposal production
  │                     - continuation / recovery policy
  │                     - cancellation
  │                     - terminal truth
  │                              │
  │                              ▼
  │                     EXECUTION PLANE (subordinate capabilities)
  │                     - artifact parse/normalize/repair (pure)
  │                     - mutation apply (kernel capability)
  │                     - verification (capability)
  │                     - checkpoint / rollback
  │                              │
  └──────────────────────────────► TRUTHFUL TERMINAL STATE
```

Concretely for IZEN, the only viable owner is a **single driver** that subsumes the
`Driver` loop and the `RuntimeExecutor` internals behind one contract:

* `$prompt`, `/build`, `$hot`, `izen prompt`, `izen orchestrate`, `izen run` all **construct the
  same `ExecutionContract`** and hand it to **one** runtime.
* The runtime may call the model more than once (continuations, explicit recovery) — but every
  call must be declared by the contract: an explicit owner, reason, budget, state transition, and
  termination condition.
* `runtime/kernel` should be the **execution plane** (capabilities + evidence + truthful
  terminal state), not a competing owner; its `Outcome` vocabulary should be the terminal
  vocabulary.
* `internal/engine`, `internal/app.Pipeline`, `cli.Stack`, `runtime/orchestrator`,
  `runtime/executor` (unreachable coordinator) and `runtime/scopeguard` should not be
  independent lifecycle owners.

The LLM is `stateless compute + untrusted proposal generator`. The runtime owns state,
authority, budget, workflow, execution, termination and truth.

---

## 11. Migration boundary

| Component | Disposition | Why |
|---|---|---|
| `internal/execution.RuntimeExecutor` | **merge into** the single runtime owner | It already owns provider + patch + verify; it is the natural core. |
| `internal/runtime/autonomy.Driver` | **remain**, become the *only* lifecycle owner | It owns the bounded loop and truthful terminal state; have it wrap the executor core instead of being an optional wrapper. |
| `internal/autonomy.RuntimeLoop` | **remain** (pure state machine/bounds) | Deterministic; no provider. Feed it from the contract. |
| `internal/ui` gateway/build-queue | **become subordinate** | It must project the runtime's state, not own queue/fix loops (`gateway.go:780`, `update.go:1602`). |
| `internal/execution/ingestion` | **become pure** | Deterministic; keep it, but gate repair by target type and contract, never content-only. |
| `core/authorization` + `execution/auth.go` | **remain**, redefine authority | One grant must cover the whole authorized mutation operation, or capability families need separate grants; do not reuse one single-use token across write+verify. |
| `internal/llmstep` budget resolver | **remain as the single budget owner** | Enforce the documented rule; delete the five bypass resolvers. |
| `internal/app.Pipeline` | **remove or become a thin client** of the runtime | It duplicates the full loop (`pipeline.go:290`). |
| `cli.Stack` + `runtime/orchestrator` | **remove or become a thin client** | It is a second proposal loop (`cli.go:397`, `engine.go:101`). |
| `runtime/orchestrator.PhaseManager` | **remain** (deterministic phase manager) | No provider of its own; it is a state manager. |
| `internal/runtime/executor` (unreachable coordinator) | **remove** | Documented unreachable rival (`executor.go:18-30`). |
| `internal/runtime/scopeguard` | **remain as subordinate** | Subordinate cursor per its own docs (`gateway.go:198`). |
| `internal/engine` layers + `control/loop.go` + `retry.go` | **remove after parity** | Legacy fourth loop. |
| `runtime/kernel` + `kernelbridge` | **remain**; promote to execution plane | It is the intended single substrate; stop letting it compete at the same level. |
| `modes/plan.Engine`, `modes/investigate` | **become capability stages** | They call the provider directly today; route them through the runtime's provider seam. |
| UI `StreamCallback`/`pendingProposals` | **become projections** | Always render from authoritative runtime events, never from a locally-produced empty payload. |

---

## 12. Fixes (only after the above)

These are the minimum corrections that follow causally from §9. They are ordered so that the
topology fix enables the symptom fixes. None is a new abstraction; each removes a duplicate
owner or makes an existing owner truthful.

1. **Introduce `ExecutionContract` at admission** and make every entry point
   (`$prompt`, `/build`, `$hot`, `izen prompt/orchestrate/run`) hand off to exactly one runtime.
   This is the enabling change; without it the other fixes conflict.
2. **One budget owner.** Route requirement/manifest/behavioral/plan/UI budgets through
   `llmstep.ResolveMaxTokens`; delete the duplicated resolvers. The contract carries the
   envelope.
3. **One authorization scope per operation.** Either (a) issue one grant that covers apply +
   all verification steps, or (b) issue distinct grants per capability family. Stop sharing one
   single-use token between `patch.go` and `runner.go`. Fix by contract, not by making tokens
   reusable.
4. **Define the grant's meaning explicitly** (mutation-operation vs one-capability-invocation)
   and enforce it in `checkAuthorization`; keep single-use where intended.
5. **Make proposal production part of the artifact contract.** `res.Diff` must be derived by the
   same resolver that produced the patch (use `wholeFileAdditionDiff` when `diffs` is empty),
   so producer and consumer cannot disagree. Gate the proposal on a non-empty payload.
6. **Gate ingestion repair by target/contract, not content only.** `rule_html_tag_balance` must
   not apply to a target whose contract is Markdown/plain; and never repair a payload that
   literal-copied the instruction placeholder.
7. **Separate terminal vocabularies into one.** `RuntimeLoop` state is authoritative; project
   `ExecutionProof.Outcome` into it rather than reporting both.
8. **Make the UI a pure projection.** Release the skeleton and the operation at the approval
   gate; never `beginOperation` on a gate that is not executing; consume the runtime's
   `ApprovalRequired` event as the authoritative gate signal.
9. **Delete the duplicate loops** (`app.Pipeline`, `cli.Stack`'s own loop, `internal/engine`
   control loop, unreachable `runtime/executor`) once 1–8 hold.

Do not increase `max_tokens`, do not remove artifact validation, do not remove verification, do
not make tokens reusable without §11, and do not merge `$prompt`/`/build`/`$hot` semantics —
those constraints remain.

---

## 13. Causal explanation of the current evidence

### A. `$prompt` CREATE (`objective requirement derivation unavailable / invalid JSON`, then `CREATE progress=UNDERSTOOD`, then `repair candidate accepted rule=rule_html_tag_balance`)

1. **Who generated the original model output?** The provider, invoked by
   `RuntimeExecutor.invokeStream` (`executor.go:4729/4737`), driven by the `Driver` through
   `ExecutorAdapter.Execute` (`adapter.go:588`).
2. **Raw provider output?** Not persisted verbatim by this path beyond the ingestion trace.
   The relevant fact is the model's response was treated as HTML-like by the ingestion
   classifier.
3. **Who parsed it?** `ingestion.Process` → `Classify` (`internal/execution/ingestion/ingestion.go:39`,
   `internal/execution/ingestion/classify.go:44`), called at `executor.go:4798` and `:5183`.
4. **Who decided it was malformed?** `ingestion.Classify` returned `ClassSyntaxInvalid`;
   `ProposeRepair` produced a candidate (`repair.go:88`).
5. **Who invoked repair?** The executor accepted the candidate inside `invokeStream`
   (`executor.go:4801-4812` and `:5198-5209`).
6. **Is repair deterministic?** Yes — pure string/stack logic, no provider, no I/O
   (`internal/execution/ingestion/repair.go:3-8`, `:88-131`).
7. **Did repair invoke an LLM?** No.
8. **Did repair modify semantic content?** Yes — it appends synthetic closing tags, bounded by
   `maxAddedTags=2` / `maxAddedBytesAbs=512` / 20% ratio (`repair.go:32-43`, `:295-321`).
   It never rewrites; but for a Markdown file, appended HTML closers *are* semantic corruption.
9. **Is `rule_html_tag_balance` applicable to Markdown?** Not by design. The classifier is
   **content-only and not extension/contract aware** (`classify.go:44`; `ProposeRepair` gates on
   `isHTMLLike`, `repair.go:134-143`). It applies whenever the payload contains `<`, `>`, and a
   tag-like sequence — including the literal instruction placeholder
   `<the COMPLETE new file content, every line of it>` (`executor.go:4256` is the diff; the
   placeholder is defined at `internal/execution/parser.go:111-113` and injected into the CREATE
   system prompt at `executor.go:3488-3489`).
10. **Was the `<the COMPLETE new file content…>` wrapper produced by the model or the runtime?**
    It originates in the runtime's **system instruction** (`parser.go:112`). The evidence is
    consistent with the model **copying the instruction placeholder into its answer** (the
    instruction explicitly shows that envelope as the required shape); the runtime then appended
    `</the>` via repair. Neither the runtime nor a parser injects the placeholder into written
    bytes in normal operation (no non-test writer of that literal into content exists). The
    concrete target `.md` is therefore content-corrupted at the ingestion seam, before the
    artifact parser.
11. **How many provider calls for this request?** ≥2 (requirement pass + main generation),
    plus up to 3 continuations, plus bounded recovery re-runs (§5). The requirement pass itself
    returned a non-JSON payload → `ParseProposedRequirements` → `invalid JSON` / "payload carried
    no requirement text" (`requirement_pass.go:102/109/129`), surfaced as
    `"[objective] requirement derivation unavailable: …"` (`objective_lifecycle.go:204`).
    That failure is non-fatal, which is why the progress still shows `UNDERSTOOD`
    (`objective_conditions.go:55`) and never reaches `REQUIREMENTS_DERIVED`.
12. **Per-call token usage?** Not separately recorded by this path in the supplied trace; the
    code records `ProviderExecution` per pass (`requirement_pass.go:270`), so the per-call
    sequence is recoverable from the telemetry ledger rather than only as an aggregate. The
    budgets differ per call (§5), which is the architectural point.

### B. `/build` proposal-payload stall

See §6. Concretely: producer and consumer are the same synchronous value; `compileDiff` returned
`""` for a valid create patch; `gateway.go:641` copied the empty diff into the proposal; no late
producer exists; `view.go:513` waits forever. The `Connecting…` footer is a second UI state
machine (operation) not released at the gate. This is **Case D**, with a presentation overlay,
not Case A/B/C/E/F.

### C. Authorization consumed during verification

See §7. The single-use token is consumed by the first verification shell command
(`runner.go:330`) and the second command is refused (`auth.go:52-55`). The token was minted as a
mutation grant but functions as a one-shell-invocation grant. The exact consumer is
`(*Runner).run` via `markAuthConsumed`.

---

## 14. Acceptance-criteria answers

1. **How many components can cause an LLM call?** Nine can directly; the authoritative list is
   §4 (22 sites). The live provider seam is `internal/execution/executor.go:4729`.
2. **How many can cause a second LLM call?** Ten independent second-call engines (§4), plus the
   executor's stream→non-stream fallback (`:4737`).
3. **Who owns the output-token budget?** No single owner. `llmstep.ResolveMaxTokens`
   (`step.go:59`) is documented as the one resolver; at least eight other paths set or overwrite
   the budget (§5). The contract does not carry it.
4. **Who owns provider cancellation?** The UI operation context in the TUI
   (`operation.go:196-230`, `gateway.go:284`); the `Driver` run context and inter-step
   select in autonomy (`driver.go:607`, `:2275`); `cli.Stack`'s `context.WithTimeout`
   (`cmd/izen/orchestrate.go:103`). Three owners.
5. **Who owns proposal completion?** The producer is `RuntimeExecutor` (`executor.go:2465-2471`);
   the consumer is `executionResultUpdate` (`gateway.go:641`). There is no completion signal —
   the value is copied once; the UI-side event `ApprovalRequired` is logged, not consumed
   (`model.go:3411-3412`). Broken ownership.
6. **Who owns artifact repair?** `internal/execution/ingestion` (deterministic) for transport
   repair; `internal/runtime/autonomy/recovery.go` (provider re-prompt) for contract repair; the
   UI test-fix loop for `/build` post-verification (`update.go:1602`). Three owners.
7. **Who owns authorization consumption?** `(*Runner).run` → `markAuthConsumed`
   (`runner.go:330`), for shell execution. Minted as mutation authority
   (`engine.go:335-344`). Mismatch.
8. **Who owns mutation?** `internal/execution.RuntimeExecutor` via `PatchManager`
   (`executor.go:2656`, `patch.go`); the P0-2 test pins this
   (`phase1_authorization_boundary_test.go:323`). This is the one genuinely converged owner.
9. **Who owns verification?** `executor.apply`'s verifier gate (`executor.go:2616-2621`,
   `verify.go`), the `Driver`'s objective verify (`objective_verify.go:55`), and the UI build
   test loop (`commands.go:3138`). Three owners.
10. **Who owns terminal truth?** `internal/autonomy.RuntimeLoop` (autonomy),
    `ExecutionProof.Outcome` (executor), `app.Result` (pipeline),
    `orchestrator.ExecutionResult` (CLI), `kernel.Outcome` (kernel). Five vocabularies.
11. **Why does `$prompt` differ from `/build`?** Different lifecycle owner: `Driver` vs direct
    executor (§8).
12. **Is there more than one agent loop?** Yes. At least: `Driver` (`driver.go:2256`),
    `RuntimeExecutor` internal loops (`executor.go:3429`, `artifact_step.go:195`),
    `app.Pipeline` (`pipeline.go:394`), `cli.Stack`/`RunCycle` (`engine.go:101`),
    `engine/control/loop.go`, plan synthesis (`plan/engine.go:1735`), UI build queue
    (`gateway.go:780`), autonomy DAG (`decomposition.go:1069`).
13. **Is any subsystem secretly acting as a second agent?** Yes: the UI build queue runs a
    provider-backed fix loop (`update.go:1602-1615` → `update.go:2044`); `app.Pipeline` is a
    full agent loop reachable via `izen run`; `runtime/kernel` advertises itself as the sole
    substrate while remaining a parallel owner.
14. **Minimum architectural correction?** Introduce the `ExecutionContract` at admission and
    make all entry points hand off to one runtime owner that owns budget, provider seam,
    proposal, recovery and terminal truth; then subordinate/delete the duplicate loops and
    duplicate budget resolvers (§10–§12).

---

## 15. Method, evidence and limitations

* Evidence was gathered by exact source reads and ripgrep on branch `fix/execution`
  (base `0ddadd6`), plus the codebase-memory graph for structure.
* Two sub-investigations (the `$prompt` trace and the `/build` trace) ran **without** the
  codebase-memory MCP tools and therefore did **not** run `check_index_coverage`; their claims
  are source-verified (grep/read) but not coverage-validated. The citied files are listed in
  those reports; a coverage batch over `internal/ui/*`, `internal/execution/*`,
  `internal/runtime/autonomy/*`, `internal/autonomy/*`, `internal/app/*`, `internal/cli/*`,
  `internal/modes/*`, `runtime/*` is recommended before treating §4/§8 as exhaustive.
* Provider-call counts are static upper bounds derived from loop bounds, not runtime
  measurements; aggregate token numbers in the supplied traces cannot be decomposed per call
  without the telemetry ledger (`ProviderExecution` events are emitted per pass).
* This audit did not modify any code. It intentionally does not fix symptoms; §12 names fixes
  only after ownership is established.

---

## 16. Conclusion

IZEN currently has **multiple execution owners** over one request. The mutation write is the only
converged authority. Everything else — requirement derivation, context/budget compilation,
provider invocation, proposal production, artifact repair, retry/recovery, verification and
terminal truth — is owned by different components that each individually work and collectively
contradict. The smallest correction is to make admission emit a single `ExecutionContract` and
put exactly one runtime owner behind it; the observed `$prompt`/`/build` divergences,
the orphaned proposal wait, the consumed-token verification failure and the fragmented output
budget all follow from that missing boundary.
