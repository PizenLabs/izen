# IZEN — Capability Reality Audit

**Status:** audit complete, gaps proven and closed where provable. Sections A–G are the
audit findings; H–J are the changes, tests and remaining limitations.

**Scope:** an independent audit of what the canonical IZEN runtime can *actually* do
today for a model, traced from real callers — not from interfaces, package names, or
the presence of an implementation.

**Non-goals (deliberately excluded):** no HTML/CSS/JS intelligence was added; no
portfolio-specific logic; no benchmark-specific runtime behaviour. The portfolio
benchmark remains a benchmark.

**Baseline at audit time:** `go build ./...` clean; `go test -race -count=1 ./...`
passes — 201 packages `ok`, 0 failures.

---

## Re-audit: the capability matrix after implementation

The matrix in §B is the **audit-time** truth. After §H, the P0 rows move:

| Capability | Status before | Status after | Why |
|---|---|---|---|
| file read | PARTIAL | **AVAILABLE** | contract-driven tool attachment; Control-Plane authorized; evidence published |
| directory listing | PARTIAL | **AVAILABLE** | as above |
| search | PARTIAL | **AVAILABLE** | as above, plus reachable via `CapabilityAuthority.Search` |
| symbol lookup | PARTIAL | **AVAILABLE** | as above |
| post-mutation observation | PARTIAL | **AVAILABLE** | `Driver.observeDeclaredTargets` returns live declared-target state to the repair computation |
| repair | PARTIAL | **AVAILABLE** | a repair is now an informed computation carrying objective-level observation, not only format diagnostics |
| workspace discovery | UNREACHABLE | **PARTIAL** | reachable via `CapabilityAuthority.Discover`; not yet model-requestable |
| project inspection | UNREACHABLE | **PARTIAL** | as above |
| dependency inspection | UNREACHABLE | **PARTIAL** | manifest derivation now reachable via `Discover` |
| command execution | UNREACHABLE | UNREACHABLE | deliberately not exposed (process spawning); see §J.1 |
| process lifecycle | UNREACHABLE | UNREACHABLE | as above; background process control does not exist |
| HTTP/URL observation | UNREACHABLE | UNREACHABLE | deliberately not exposed (network egress); see §J.1 |
| artifact generation / file mutation / verification / replan / objective evaluation / truthful termination | AVAILABLE | AVAILABLE | unchanged; proven sound |

**No capability is `MISSING`.** The remaining `UNREACHABLE` rows are unreachable *by
design decision*, not by accident: they spawn processes or egress the network, and §12
requires those to stay behind their own authority boundary.

---

## Definition used throughout

```
Capability = reachable + authorized + executable + observable + evidenced
```

`code exists` is **not** a capability. A file-read implementation that the canonical
model execution path cannot invoke is classified `UNREACHABLE`, regardless of how
complete it is.

Statuses: `AVAILABLE` · `PARTIAL` · `UNREACHABLE` · `MISSING` · `UNKNOWN`

---

## A. Canonical call graph

The real `$prompt` path, traced from the TUI key handler to terminal state. Every arrow
is an actual call site.

```
user types "$prompt <objective>"
└─ ui.handleInput                        internal/ui/commands.go:188
   └─ parser.Parse (user text only)      internal/parser/parser.go:19
      └─ dispatchDirectives              internal/ui/intent_dispatch.go:213
         └─ routePromptDirective         internal/ui/intent_dispatch.go:287
            ├─ bindScopeProvenance(ScopeDynamic)          :288
            └─ runAutonomyRoutedCmdExplicit               internal/ui/autonomy_route.go:57
               ├─ autonomy.Engine.Decide(objective)       internal/autonomy/engine.go
               └─ dispatchAutonomyTrace                   internal/ui/autonomy_route.go:85
                  ├─ DecisionDirectResponse → conversational unwind
                  ├─ DecisionAskUser  → proposal gate (human)
                  ├─ DecisionBlock    → refuse
                  └─ auto_continue → executeAutonomyWorkspace          :157
                     ├─ handoffExecutionContext                        :164
                     ├─ modeForAutonomyWorkspace → ModeBuild
                     └─ executeAutonomyViaDriver        internal/ui/autonomous.go:43
                        └─ runAutonomousDriver           internal/ui/autonomous.go:116
                           └─ Driver.Run   internal/runtime/autonomy/driver.go:445
                              ├─ adapter.Resolve(objective)                    :509
                              │    └─ IntentGateway.SelectStrategy (deterministic)
                              ├─ deriveEvidenceScope                           :526
                              ├─ selectInteractionContract                     :527
                              │    └─ protocol.AgenticLoop for mutation lanes   :539
                              ├─ ValidateObjectiveContract                     :596
                              ├─ syncCanonicalIntent                           :611
                              │    └─ blocking intent revision + CONTEXT RE-COMPILE
                              ├─ capturePreExecutionTargets                    :584
                              └─ observeAndRun                                 :1485
                                 ├─ syncGrantedWorkspaceContext          :1498  (fail-closed)
                                 ├─ preflightBarrier.Wait                :1501
                                 ├─ loop.Observe(obs) → deciding         :1530
                                 ├─ EvaluatePreflightAdmission          :1545  ("no evidence, no provider")
                                 └─ for !loop.State().IsTerminal()      :1563
                                    ├─ RuntimeDeciding/Interpreting      :1584
                                    │   ├─ decideDefault(obs, bounds)   :1590
                                    │   ├─ authorizeObjectiveCompletion :1598  ← completion authority
                                    │   ├─ authorizeBehavioralCompletion:1611  ← observe→repair→verify
                                    │   ├─ authorizeContractRecovery    :1618
                                    │   └─ step(decision)               :1653
                                    ├─ RuntimeExecuting                  :1656
                                    │   ├─ ValidateDispatchContract      :1664
                                    │   └─ adapter.Execute(ctx, d.req)   :1668
                                    │      └─ execution.RuntimeExecutor.Execute
                                    │         ├─ admission / scope / grant / contract
                                    │         ├─ context compile (frozen, SHA-256 sealed)
                                    │         ├─ provider.Execute / ExecuteStream   ← LLM CALL
                                    │         │   └─ provider tool loop (CONDITIONAL — see §C)
                                    │         ├─ artifact parse (execution/parser.go)
                                    │         ├─ pending patch + approval gate
                                    │         ├─ Approve → MutationBoundary → OCC
                                    │         ├─ verification (verifier.AuditObjective)
                                    │         └─ seal ExecutionEvidence → ExecutionProof
                                    ├─ RuntimeRecovering                 :1764
                                    │   └─ typedRepair(obs, req) → new LoopRequest → LoopContinue
                                    └─ RuntimeAwaitingHuman              :1824  (park, no termination)
```

### Component questions

| # | Component | Caller | Mode | In | Out | Reachable from `$prompt` | Model-caused? | Authorized? | Observable? | Evidenced? |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | `ui.handleInput` | Bubble Tea | all | raw line | `tea.Cmd` | yes | yes (input) | n/a | yes (frame) | n/a |
| 2 | `parser.Parse` | #1 | all | user text | `IntentAST` | yes | yes | n/a | n/a | n/a |
| 3 | `routePromptDirective` | #2 | `$prompt` | tail text | routes | yes | yes | binds `ScopeDynamic` | yes | n/a |
| 4 | `autonomy.Engine.Decide` | #3 | all | objective | `autonomy.Trace` | yes | yes | reads `GrantLedger` | yes (`renderAutonomyDecision`) | no |
| 5 | `Driver.Run` | #3 via #4→`executeAutonomyViaDriver` | `ModeBuild` | objective | `LoopTermination` | yes | yes | grant-gated context barrier | yes (`loop.transition`) | yes (durable ledger) |
| 6 | `adapter.Resolve` | #5 | all | objective | `Resolved` | yes | yes | n/a (deterministic) | yes | no |
| 7 | `syncCanonicalIntent` | #5 | all | targets, intent | error / provenance | yes | yes | re-compile must validate | yes (activity) | yes |
| 8 | `RuntimeExecutor.Execute` | #5 `adapter.Execute` | `ModeBuild` | `ExecuteRequest` | `ExecutionResult` | yes | yes | **yes — admission, scope, grant, contract** | yes | yes (`ExecutionProof`) |
| 9 | provider (`openrouter.Execute`/`ExecuteStream`) | #8 | all | `ai.Request` | `ai.Response` | yes | yes | contract ceiling only | yes (stream) | yes (`ModelInvocation`) |
| 10 | **provider tool loop** | #9 | all | `ai.Request` | final response | **conditional — 2 model IDs only** | **only if allowlisted** | **NO** | **NO** | **NO** |
| 11 | `capability.Runner` (7 IDs) | **behavioral stage only** | behavioral | `capability.Grant` | `Evidence` | **behind substring heuristic** | **NO** | **yes** (`authorize`) | yes | **yes** |
| 12 | `MutationBoundary` | #8 `Approve` | mutation | `MutationSet` | applied/rollback | yes | no (human approves) | yes | yes | yes |
| 13 | `verifier.AuditObjective` | #8 | mutation | base+mutated bytes | `Verdict` | yes | no | n/a | yes | yes |
| 14 | `ObjectiveCompletionAuthority.Evaluate` | #5 `authorizeObjectiveCompletion` | all | `(TaskContract, ObjectiveEvidence)` | `ObjectiveOutcome` | yes | no | n/a | yes | yes |
| 15 | `typedRepair` | #5 `RuntimeRecovering` | all | `(Observation, LoopRequest)` | new `LoopRequest` | yes | partially (model proposes) | yes (bounded) | yes | yes |

**Rows 10 and 11 are the audit's core result.** Both are real, complete
implementations. Neither is reachable by a general-purpose model.

---

## B. Capability matrix

Legend: `✓` yes · `~` partial · `—` no · `?` unknown

| Capability | Implemented | Canonically reachable | Model-invocable | Authorized | Observable | Evidence | Status |
|---|---:|---:|---:|---:|---:|---:|---|
| workspace discovery | ✓ `capability.Runner.Discover` (`workspace.go:168`) | — | — | ✓ `authorize` | ✓ | ✓ `Evidence` | **UNREACHABLE** |
| directory listing | ✓ `readonly_tools.go:113` | ~ | ~ 2 models | — | — | — | **PARTIAL** |
| file read | ✓ `readonly_tools.go:87` + `capability/io.go:199` | ~ | ~ 2 models | mixed | — | — | **PARTIAL** |
| search | ✓ `readonly_tools.go:144` + `capability/io.go:252` | ~ | ~ 2 models | mixed | — | — | **PARTIAL** |
| symbol lookup | ✓ `readonly_tools.go:166` | ~ | ~ 2 models | — | — | — | **PARTIAL** |
| project inspection | ✓ `workspace.go:168,287` | — | — | ✓ | ✓ | ✓ | **UNREACHABLE** |
| dependency inspection | ✓ `workspace.go:287` (`deriveManifests`) | — | — | ✓ | ✓ | ✓ | **UNREACHABLE** |
| command execution | ✓ `capability/serve.go:974` | — | — | ✓ | ✓ | ✓ | **UNREACHABLE** |
| process lifecycle | ✓ `serve.go:108/196/243/315` | — | — | ✓ | ✓ | ✓ | **UNREACHABLE** |
| HTTP/URL observation | ✓ `serve.go:408` `Fetch`, `:671` `Inspect` | — | — | ✓ | ✓ | ✓ | **UNREACHABLE** |
| artifact generation | ✓ `executor.go:2722` `invokeMutation` | ✓ | n/a (produces) | ✓ | ✓ | ✓ | **AVAILABLE** |
| file mutation | ✓ `MutationBoundary` + `Approve` | ✓ | n/a | ✓ | ✓ | ✓ | **AVAILABLE** |
| post-mutation observation | ~ existence only (`adapter.go:687`) | ~ | — | n/a | ~ counter only | ~ | **PARTIAL** |
| verification | ✓ `verifier.AuditObjective` (`verifier.go:225`) | ✓ | — | ✓ | ✓ | ✓ | **AVAILABLE** |
| repair | ~ `typedRepair` + behavioral loop | ~ heuristic | ~ | ✓ | ✓ | ✓ | **PARTIAL** |
| replan | ✓ `DecideRecovery` / decomposition | ✓ | — | ✓ | ✓ | ✓ | **AVAILABLE** |
| continuation | ~ `llmstep` (own bytes only) | ✓ | n/a | ✓ | n/a | ✓ | **PARTIAL** |
| objective evaluation | ✓ `objective_authority.go:683` | ✓ | — | ✓ | ✓ | ✓ | **AVAILABLE** |
| truthful termination | ✓ fail-closed reducer → `LoopUnsubstantiate` | ✓ | — | ✓ | ✓ | ✓ | **AVAILABLE** |

Nothing is `MISSING`. Every capability in the requested list has a real
implementation. The failures are **reachability and authorization**, not existence.

### B.1 The canonical capability layer exists and is sound

`internal/execution/capability/capability.go:40-75` defines a closed, total vocabulary:

```
workspace.discover   file.read   file.search   runtime.serve
runtime.fetch        runtime.inspect           command.run
```

Each has a real implementation, each is gated by `Runner.authorize` (`io.go:165`)
against a `capability.Grant`, and each returns `capability.Evidence`. The failure
taxonomy is closed (`FailureClass`, 13 members) and `Block` names the class, the
capability, and the evidence IDs. **This layer already satisfies
authorized + executable + observable + evidenced.** It is not the problem.

### B.2 Its only production consumer is a behavioral stage behind an English heuristic

```
internal/runtime/autonomy/behavior.go:177-179   ← only production construction
   └─ execution.GrantFor(provenance, caps)
      execution.NewBehavioralRuntime{Root: ...}
internal/execution/behavior.go:177-184          ← only capability.Runner construction
```

`BehaviorStage.Stage` is invoked from exactly one place —
`authorizeObjectiveCompletion`'s sibling gate `authorizeBehavioralCompletion`
(`internal/runtime/autonomy/objective_completion.go:250`):

```go
if d.behavior == nil || !BehaviorRequired(d.prompt) { return }
```

`BehaviorRequired` (`internal/runtime/autonomy/behavior.go:99-125`) is a **substring
heuristic over the objective text**. It triggers on any of ~23 English fragments
(`run`, `fix`, `test`, `work`, `verify`, `load`, `renders`, `execute`, …) and opts out
on ~12 (`explain`, `describe`, `summar`, `find `, …).

Consequence: whether the only real observe→diagnose→repair→verify loop engages is
decided by **English word choice in the prompt**, not by the objective, the workspace,
or the evidence. An objective phrased without those substrings gets no capability
access at all. This is a heuristic gate on a control-plane decision — exactly the class
of coupling the mission forbids.

---

## C. Model ↔ runtime boundary

### C.1 The mechanism is real and complete

`internal/ai/toolloop.go:45-83` implements precisely:

```
LLM → tool_calls → capability execution → tool result → LLM → …
```

bounded by `ReadOnlyToolLoopMaxIterations = 4` (`toolloop.go:9`). Tools are declared
at `internal/ai/tools.go:8-20` and implemented at `internal/execution/readonly_tools.go`
(`read_file` bounded 256 KB, `list_directory` bounded 500 entries, `search_codebase`
bounded 50 results / 5000 files, `symbol_lookup`), all traversal-guarded by
`resolveWithinRoot` (`readonly_tools.go:69-85`). Non-read-only requests receive a
bounded error string (`toolloop.go:89-91`) and never execute.

It is wired into the canonical path: `executor.go:679` and `executor.go:973` call
`SetToolRunner(NewReadOnlyToolRunner(root))`.

### C.2 The runtime's own contract already declares this path tool-bearing

`internal/protocol/contract.go:388`:

```go
tools := contract == ToolEnabledCompletion || contract == AgenticLoop
```

and the mutation lane selects `AgenticLoop` (`executor.go:1332`, `driver.go:539`).
`AllowsCapability(CapabilityTool)` therefore returns true for `$prompt`
(`contract.go:662`). The canonical interaction contract for `$prompt` **already
declares tool-bearing capability access.**

### C.3 The provider seam refuses to honour it

`internal/providers/openrouter.go:275-277`:

```go
if p.toolRunner != nil && oregistry.RequiresAgenticHarnessWire("openrouter", model) {
    return ai.RunReadOnlyToolLoop(ctx, p.executeOnce, p.toolRunner, req, ai.ToolLoopOptions{})
}
return p.executeOnce(ctx, req)          // ← single-shot, no tools
```

`internal/provider/registry/wire_policy.go:37-43` is a hardcoded allowlist of **exactly
two model IDs**:

```go
{Provider: "openrouter", ID: "thinkingmachines/inkling:free"},
{Provider: "openrouter", ID: "thinkingmachines/inkling-small:free"},
```

`promoteAgenticWireContract` (`openrouter.go:54-73`) is the only place
`ai.ReadOnlyTools()` is ever attached (`openrouter.go:71`), and it returns early for
any other model.

**Therefore:** for a capable general-purpose LLM, `$prompt` is

```
LLM → large prompt → one artifact response → patch
```

not

```
LLM → capability request → runtime authorization → execution → observation → LLM
```

The second shape exists in code and is exercised by tests
(`internal/ai/toolloop_test.go`) but is unreachable for any model outside the
allowlist. The gate is a **provider wire-policy workaround**, not a capability surface.

### C.4 When it does run, it is unauthorized and unevidenced

`ReadOnlyToolRunner.Run` (`readonly_tools.go:38-54`) dispatches straight to
`os.ReadFile` / `os.ReadDir` / `filepath.WalkDir`. It consults no `GrantLedger`, no
`PolicyEngine`, no `ScopeProvenance`, and no `capability.Grant`. Compare the mutation
lane, which passes through `MutationBoundary`, `AuthorizationEngine` and OCC.

It also emits **no event** and writes **nothing** into `ExecutionProof`. Capability
execution leaves no evidence. `ExecutionProof.WorkspaceObservations` is an integer
counter consumed only by the authority (`objective_authority.go:1169`) — never
content, never surfaced to the model.

`internal/mcp/` defines no tools (verified: zero matches for `Tool`), and
`internal/runtime/ephemeral`'s `AllowedTools` is a crash-resume capsule field, not a
tool surface. There is no second tool registry.

### C.5 What the model may choose

In the mutation lane the model has exactly four outputs and selects no capability, no
target, and no operation:

| Output | Meaning |
|---|---|
| structured artifact for one **pre-named** target | produce a change |
| `NO_CHANGES_REQUIRED` | claim no change needed |
| `NO_PROPOSAL` | decline (behavioral repair only) |
| anything else | `ErrZeroArtifactsParsed` — typed, re-promptable, never written |

A typed operation enum exists only in `/plan` `StructuredCompletion`
(`internal/modes/plan/schema.go:233-244`): `FILE_MUTATE`, `SHELL_EXEC`, `GIT_ACTION`,
with an unknown-strategy fallback to `FILE_MUTATE`.

---

## D. Context boundary

Context reaching the model is **real workspace data, not fabricated, and fail-closed.**

- Built by `internal/execution/context_compiler.go` via `x.compileRequest` /
  `x.workspaceFiles` (`executor.go:2945-2957`), covering channels `user_prompt`,
  `environment_state`, `system_prompt`, `referenced_file`, `evidence`
  (`execution/context.go:195-207`).
- Frozen and SHA-256 sealed (`execution/context.go:89-100`); identity travels on
  `ExecutionProof.ContextID` / `ContextDigest` / `ContextParentID`.
- Provenance is validated, not assumed: `ContextProvenance.ScopeMatched`,
  `WorkspaceMaterialPresent`, `Valid` (`internal/contextcompiler/provenance.go:40-47`).
- **Empty context cannot dispatch.** `syncCanonicalIntent`
  (`objective_completion.go:473-485`) re-compiles under the mutation contract and
  parks the run if the re-compilation is invalid; `syncGrantedWorkspaceContext`
  (`driver.go:1498`) does the same for grant-gated context. Invariant I12
  ("no evidence, no provider") is enforced at `driver.go:1545`.
- Staleness is bound: `ContextDigest`/`ParentID` plus OCC `workspace_drift` detection
  (`driver.go:1691-1718`).

**No defect found here.** The prompt-only-fake-context and empty-context failure modes
are already structurally prevented.

One unrelated observation: `internal/engine/context.go:20-46` `BuildObjectiveContext`
returns `{Scope, Budget, Telemetry}` — a token-accounting verdict, no prompt text. It
has one production caller (`ui/commands.go:4450`) and is **not** on the `Driver` path.
It is not the context boundary. It is, however, dead weight on the canonical path and
its empty-scope case reports `RequiresApproval=false` (weight 0 is below the ≥800
floor) — recorded as a minor truthfulness wart, not a capability gap.

---

## E. Observation boundary

### E.1 What the runtime can observe

`ExecutionProof` (`executor.go:262-349`) is a genuinely thorough evidence record:
`ModelInvocations` (with `FinishReason`, `Truncated`, `SchemaMode`, `PromptFingerprint`,
`ContractID`), `Mutations` (with `Tainted`), `Verification`, `AffectedFiles`,
`WorkspaceObservations`, `DiffSummary`, `ContextDecisions`, `ContractID`/`AttemptID`/
`ParentContractID`/`CausalAncestry`, `Outcome`. It is sealed
(`sealTerminalEvidence`, `executor.go:4703`) and a failure to persist structurally
invalidates success.

Post-mutation target existence is re-read live from disk at the composition boundary
(`adapter.go:696-707`) rather than trusting the executor's admission-time view.

### E.2 What reaches the model

Four mechanisms exist. Three carry real state; one deliberately withholds it.

| # | Mechanism | Location | Carries |
|---|---|---|---|
| 1 | read-only tool loop | `ai/toolloop.go:56-81` | real workspace output — **but 2-model gated** |
| 2 | full-artifact continuation | `execution/artifact_step.go:119-136` | the model's own delivered bytes |
| 3 | read-only ASK continuation | `executor.go:3871` | committed summaries + pending topics |
| 4 | **behavioral repair** | `execution/behavior_loop.go:297-393` | **real observation**: base URL, per-resource HTTP status, byte counts, defect codes |
| — | mutation/contract recovery | `recovery.go:394,406,451` | *runtime-authored directives + validation strings only — never response bytes, never workspace state* |

Mechanism 4 is the strongest form that exists in the codebase
(`evidence_reasoner.go:78-115` builds `## OBSERVATION EVIDENCE` from a real
`Observation`). It is also the one behind the substring heuristic (§B.2).

### E.3 The missing boundary

For an objective that is **not** behaviourally triggered, the model performs exactly one
computation per authorized attempt. On failure it receives, at most, 512 bytes of
artifact-contract diagnostic (`adapter.go:762` `maxDiagnosticEvidence`), appended as a
corrective directive (`recovery.go:394`).

It never receives: what the workspace looks like now, what verification found, what a
command returned, or the post-mutation bytes — except indirectly, because a bounded
patch window may be re-sliced from the mutated file on the next attempt
(`executor.go:2924`).

**Exact missing boundary named:** post-execution workspace observation is produced
(`ExecutionProof`) but has no return path to the model outside the behavioural stage
and the 2-model tool loop. `ProviderState`, `ArtifactState`, `MutationBoundaryState`,
`VerificationState` are all recorded; only `VerificationState` and
`MutationBoundaryState` influence the next computation, and only via a bounded
diagnostic string.

---

## F. Objective boundary

Completion is established **only** by the reducer. This part of the system is correct
and was deliberately left untouched.

`internal/execution/objective_authority.go:683` `Evaluate` is total, pure, and
fail-closed. Precedence (`:670-682`, implemented in order):

```
0  ApprovalPending                       → REQUIRES_AUTHORIZATION
1  sealed record FAILED/abortedOCC       → FAILED
1b cancelled / requiresReview / tainted  → UNSUBSTANTIATED
2  truncation / partial / length finish  → UNSUBSTANTIATED (continuing)
3  provider refused / ProviderError       → FAILED
4  per-kind clauses                      → Create/Patch/Delete/Read/Review/Idempotent
5  nothing satisfied                     → UNSUBSTANTIATED   (fail-closed)
```

`ProviderState`, `ArtifactState`, `MutationBoundaryState`, `VerificationState`,
`ObjectiveEvidence` and `ObjectiveState` are separate types (`:41-169`) and are not
collapsed.

**File mutation does not imply objective proof.** `evaluatePatch`
(`objective_authority.go:766-786`) requires five conjuncts — artifact produced and
parsed, mutation actually applied, **observed delta on a declared target**, declared
target now exists, and verifier satisfied. The doc comment at `:762-765` states it:
*"Applied is not changed: a no-op apply leaves the bytes identical."* The zero-mutation
case (`evaluateIdempotent`, `:850-855`) is stricter still — the structural NO-OP verdict
is the entire clause.

The driver cannot override it: `authorizeObjectiveCompletion`
(`objective_completion.go:190-228`) **mutates** a proposed completion into the terminal
state matching why it was refused, and runs before the behavioural gate, which itself
can only downgrade (`authorizeBehavioralCompletion`, `:246-288`).

### F.1 The Objective contract

The minimal domain-neutral contract the mission describes already exists in substance,
as `TaskContract` + `ObjectiveEvidence` (`objective_authority.go:243-368`):

```
Objective
├── Intent            → TaskClassification.Intent
├── Scope             → TaskContract.Targets
├── Constraints       → TaskContract.Requires{Mutation,Verifier,Observation,Response}
├── Required Evidence → the ObjectiveEvidence clauses per TaskKind
└── Completion Conds  → evaluateCreate / Patch / Delete / Read / Review / Idempotent
```

`TaskKind` (`:190-212`) is closed: create · patch · delete · read · review ·
idempotent. It carries **no domain knowledge** — it does not know what makes a
portfolio beautiful. That requirement is satisfied.

Note: `internal/domain/objective.go:30-38` `domain.Objective` is a *different*,
telemetry-only struct (status, scope, token budget, human-confirmed) with one UI caller.
It is **not** the objective contract and should not be mistaken for one.

---

## G. Repair / replan boundary

Iterative execution **is** possible and is genuinely bounded.

```
observe → insufficient → model computation → authorized proposal → execution → verification
```

The cycle is real: `driver.go:1563` loops while non-terminal, and
`RuntimeRecovering` (`:1764`) calls `typedRepair` then `LoopContinue` back into
`RuntimeExecuting`. Unit-level cross-visibility exists — each DAG sub-task compresses
the **current** target bytes, which mutate between units
(`subtask_executor.go:181-184`), so attempt N+1's input reflects attempt N's applied
effect.

### Bounds are runtime-owned, never model-owned

| Bound | Value | Source |
|---|---:|---|
| `MaxAttempts` | 3 | `runtime_loop.go:611` |
| `MaxRecoveryCycles` | 2 | `runtime_loop.go:612` |
| `MaxExecutionSteps` | 10 | `runtime_loop.go:613` |
| `MaxIdenticalDecisions` | 2 | `runtime_loop.go:614` |
| `MaxContractRecoveryAttempts` | 2 | `driver.go:263` |
| behaviour rounds | 3 | `execution/behavior_loop.go:143` |
| tool-loop turns | 4 | `ai/toolloop.go:9` |
| continuation steps | 3 | `llmstep/step.go:56` |

Enforcement in `violation()` (`runtime_loop.go:1063-1094`) aborts the run. The
run-level token bound is only ever *raised* (`driver.go:564-567`,
`RunTokenBudget` `runtime_loop.go:587-606`) — an operator bound is never reduced.

Repair **is** an explicit authorized computation: `typedRepair` produces a new
`LoopRequest`, which crosses `ValidateDispatchContract` (`driver.go:1664`) and
`adapter.Execute` → `RuntimeExecutor` admission. Contract drift between recovery and
the active contract aborts (`driver.go:1793-1795`). Workspace version is carried
through recovery **unchanged** (`recovery.go:470`) so drift aborts rather than being
silently refreshed.

No uncontrolled autonomous loop exists. There is exactly one driver, one executor, one
completion authority.

### G.1 The gap

Repair is authorized and bounded, but for a non-behaviourally-triggered objective the
model's second computation is driven by **artifact-format diagnostics**, not by
**objective-level observation**. The runtime can tell the model "your SEARCH anchor
matched zero lines"; it cannot tell it "the file now contains X, verification reported
Y, so the objective is still unproven because Z."

That is the single boundary missing from §E.3, and it is the reason a capable model
cannot close a general objective today.

---

### B.3 Benchmark-specific intelligence in runtime production code (§13)

An independent sweep found hardcoded domain intelligence on the canonical path. This is
recorded here because §13 mandates its removal, and because two of the items are
`html → index.html`-class target mapping.

**On the compose/TUI `$prompt` path (reachable):**

| Location | Content |
|---|---|
| `internal/ui/update.go:5565-5570` | prompt containing "static website" ⇒ synthesizes three todos naming `index.html`, `styles.css`, `script.js` |
| `internal/modes/plan/engine.go:2380-2393` | when the VANILLA_WEB archetype guard filters out every LLM task, synthesizes a literal `Task{Target:"index.html", IsHardcoded:true}` |
| `internal/modes/plan/archetype_guard.go:189,200` | `archetypeFallbackTarget` returns literal `"index.html"` |
| `internal/engine/strategy/greenfield.go:60-80` | `WithCriteria("index.html renders…")`, `planFiles` always emits `index.html`, conditionally `styles.css`/`script.js` |
| `internal/engine/adapter/staticweb.go:81-91` | scaffolds `<title>`, `<link href="styles.css">`, `<script src="script.js">` |
| `internal/engine/inference/detect.go:130,168,198` | detectors labelled "Static HTML/CSS/JS" with literal filename lists |
| `internal/modes/investigate/engine.go:1105-1117`, `dispatcher.go:432,451` | web filename keyword lists; `\bportfolio\b` regex in intent routing |
| `internal/ui/autonomy_route.go:218` + `internal/ui/commands.go:2675` | `isHTMLTarget` → `hotfix.ResolveRedundantTargets` feeds model-visible evidence |

**Isolated to `izen run` (not reachable from `$prompt`):** `internal/domain/capability/`
carries the full portfolio/to-do benchmark gate in production code — `CapPortfolioWebsite`
(`defaults.go:20`), `validatePortfolio` (`:190-202`), `todoAppSignal` (`:282`),
`<title>`/`<h1>` extraction (`alignment.go:54`), and the literal constraint text
*"a to-do list app … is FORBIDDEN"* (`defaults.go:84-92`). Its only caller is
`internal/app/pipeline.go:433`, and `internal/app` is imported solely by
`cmd/izen/runtime.go`. So the benchmark gate cannot affect `$prompt`, but it is still
runtime code rather than test infrastructure.

### B.4 Other reachability facts worth recording

- `internal/infrastructure/capabilities`: `OSFile`, `GitCLI` and `PatchAdapter` are
  constructed at `cmd/izen/main.go:221-224` and injected via `compose.WithCapabilities`,
  but `compose.go:145-147` documents the struct as a *"read-only record for the
  composition root; not consumed by handlers"* and there are no readers. Only `ExecShell`
  is live, via a **separate** instance at `compose.go:667`.
- `internal/runtime/executor.RuntimeExecutor` (a second type with the same name) has
  **no production caller** — pinned by `TestPhase1_SingleProductionExecutionAuthority`.
  Only its primitives are used.
- `internal/patch.Engine.Apply` is wired into the composition root (`compose.go:964`,
  `ui/program.go:207`) but has **zero call sites**.
- `RuntimeExecutor.SetMutationBoundary` (`executor.go:849`) has no non-test caller, so
  `RuntimeExecutor.mutationBoundary` is nil in production; live rollback integrity comes
  from `execution.RollbackAndVerify` via `adapter.go:284`.
- `scopeguard.IntentGateway.Authorize` is live (`ui/runtime_bridge.go:192`) but is
  constructed with the structural, budget and ledger tiers all `nil`.
- **Background-process start/stream/kill does not exist.** `ExecShell` and
  `execution.Runner` both buffer output; only foreground exec with process-group SIGKILL
  and HTTP `Serve`/`Stop` exist.
- `internal/mcp/` defines no tools. `internal/runtime/ephemeral.AllowedTools` is a
  crash-resume capsule field. There is no second tool registry.
- `internal/workspace/checkpoint`, `internal/workspace/failure`,
  `internal/verification/harness.go`, `internal/verification/stabilize`,
  `internal/command/router.go`, `execution.Scheduler`, `internal/adapters/web` and
  `internal/boundary` have no production callers.

## H. Changes made

Every change below is justified by a specific finding above. No new autonomous
architecture, driver, executor, planner, mutation path or completion authority was
introduced; no domain-specific runtime capability was added.

### H.1 P0 — canonical capability access (fixes §C.3)

`internal/providers/openrouter.go`

| Change | Before | After |
|---|---|---|
| trigger for attaching capability tools | `RequiresAgenticHarnessWire` — a hardcoded 2-model allowlist | the canonical interaction contract (`protocol.contract.go:388` already declares `AgenticLoop` tool-bearing), with wire policy kept as an *additional* independent trigger |
| trigger for running the tool loop | same allowlist | `capabilityToolsWire` — tool-bearing contract **or** wire policy |
| tool advertisement on a casual prompt | stripped unless allowlisted | stripped only when the contract is **not** tool-bearing |

`promoteAgenticWireContract` and `buildRequest` now share one predicate
(`contractIsToolBearing`), so the wire contract and the loop cannot disagree.
`StructuredCompletion` is explicitly excluded: it owns a JSON schema contract and
cannot also carry a tool loop.

**Effect:** for a capable general-purpose LLM, `$prompt` is no longer
`large prompt → one artifact response`. It is `capability request → authorized
execution → observation → next computation`, because the contract that already
declared that access is finally honored on the wire.

### H.2 P0 — model capability requests are Control-Plane authorized and evidenced (fixes §C.4)

New: `internal/execution/capability_tools.go`

`CapabilityToolRunner` replaces the bare `ReadOnlyToolRunner` at both provider-binding
sites (`executor.go` `NewRuntimeExecutor`, `SetProvider`). Per call it now:

1. maps the tool name to a canonical `capability.ID` — unknown ⇒ refused, never guessed;
2. authorizes against the executor's **own admission capability vector**, projected onto
   `capability.Grant` via `CapabilityGrantFor`;
3. executes the real operation by delegating to the existing traversal-guarded
   `ReadOnlyToolRunner` (no duplicated filesystem logic);
4. records a `capability.Evidence` record and publishes it on the existing event bus via
   the existing `events.NewStageCompleted` / `NewActivity`.

The admission vector is read through a closure, so `SetAdmittedCapabilities` takes effect
on the next request without rewiring the provider. `Tools()` and `CapabilityToolNames()`
advertise only what the grant permits — an ungranted capability is never offered.

### H.3 P0 — post-execution observation reaches the next computation (fixes §E.3, §G.1)

New: `internal/execution/capability_authority.go`
New: `ExecutorAdapter.ObservationAuthority` (`internal/runtime/autonomy/adapter.go`)
New: `Driver.observeDeclaredTargets` (`internal/runtime/autonomy/driver.go`)

`CapabilityAuthority` is the **same** `capability.Runner` the behavioural stage uses,
holding the same `GrantFor` projection. It adds no authority and no new capability
implementation. It exposes `ObserveTarget`, `Search` and `Discover` — so *search* and
*project inspection* are now reachable outside the behavioural stage.

`observeDeclaredTargets` is invoked in the `RuntimeRecovering` branch, appending the
current on-disk facts of every **declared** target to the repair re-prompt's evidence. It
is read-only, grant-bounded, scope-bounded to the declared target set, and carries facts
(bytes/lines/evidence identity) only — never artifact bytes, never a completion claim.

**Scope, deliberately narrow:** only the observational surface crosses this seam.
`runtime.serve`, `runtime.fetch`, `runtime.inspect` and `command.run` remain owned by the
behavioural observation stage. Promoting process spawning or network egress to routine
post-execution observation is a different authority decision, and the audit does not prove
it is required for canonical objective execution.

### H.4 §13 — benchmark-specific runtime intelligence removed

| Location | Removed | Replacement |
|---|---|---|
| `internal/ui/update.go:5556` | a "static website" objective synthesized three tasks naming `index.html`, `styles.css`, `script.js` | one domain-neutral `[FILE_MUTATE] [resolve]` task whose target is resolved by the existing build resolver |
| `internal/ui/autonomy_route.go:207` + `commands.go:2675` | `isHTMLTarget` gating a markup-only redundancy ledger into model-visible build evidence | the generic Context Evidence Ledger for every target; `isHTMLTarget` and `formatRedundancyLedger` deleted |
| `internal/modes/plan/engine.go:2381` | synthesized `Task{Target:"index.html", IsHardcoded:true}` when the archetype guard filtered every candidate | `nil` — an unresolved plan is reported truthfully |
| `internal/modes/plan/archetype_guard.go:181` | fabricated `"index.html"` for VANILLA_WEB / REACT_NEXT with no eligible file | `""` — no target, no fabrication |

`internal/hotfix` and `internal/adapters/web` are untouched: markup analysis and web
detection belong there, and GEN-02 already pins the adapter as the sanctioned home.

### H.5 Explicitly NOT changed

- `ObjectiveCompletionAuthority` — the audit found it sound and fail-closed (§F).
- `internal/domain/capability`'s portfolio/to-do gate — unreachable from `$prompt`
  (only `izen run` reaches it). Removing it is a separate change against a separate
  entry point and is recorded as a remaining gap rather than done as unrelated cleanup.
- `BehaviorRequired`'s substring heuristic — documented as a remaining gap (§J). The
  capability layer is now reachable without it, which is the structural fix; deleting the
  heuristic is a behavioural change to the behavioural gate and is not proven necessary.
- `internal/infrastructure/capabilities` (`OSFile`/`GitCLI`/`PatchAdapter` constructed but
  unconsumed), the second `runtime/executor.RuntimeExecutor`, `patch.Engine.Apply` — all
  recorded, none load-bearing on the canonical path.
- The two constitution documents — unchanged.

## I. Tests

| Test | Purpose |
|---|---|
| `TestCapabilityToolRunnerRefusesUngrantedCapability` | fail-closed: a zero admission vector advertises no tools, returns the canonical authorization refusal, and **never returns file content** |
| `TestCapabilityToolRunnerExecutesAndEvidences` | an authorized read executes against the live workspace and publishes `capability.execute` evidence |
| `TestCapabilityToolRunnerToolNamesMapToCanonicalCapabilities` | pins tool→capability mapping to the canonical vocabulary; mutating tools have **no** capability on this seam |
| `TestCapabilityToolRunnerDeniesWorkspaceEscape` | the traversal guard still holds through the authorized seam |
| `TestObservationAuthorityReadsCurrentState` | observation returns **live** state, not a cached snapshot; evidence reports a real line count |
| `TestObservationAuthorityRefusesWithoutGrant` | the zero grant authorizes nothing; the refusal is reported truthfully |
| `TestObservationAuthoritySearchesAndDiscovers` | search and project inspection are real, reachable capabilities |
| `TestObservationGrantIsProjectionOfExistingAuthority` | the observation grant is a projection of existing provenance × capability-set authority, and can never reach a process or a socket |
| `TestDomainNeutralBenchmark_TruthfulTermination` | Go-module workspace, zero markup: real `$prompt` run reaches the approval boundary, does not mutate before approval, and after the authorized approval reaches **PROVEN** with the proving bytes |
| `TestDomainNeutralBenchmark_UnprovenObjectiveNeverCompletes` | a prose-only response is never written to the workspace and never completes |
| `TestExecutionTrace_CanonicalObjectiveSequence` | the canonical event sequence: target resolved → context prepared → admission → execution → **approval boundary before mutation** → mutation applied → verification → proven |
| `TestExecutionTrace_UnprovenObjectiveContinues` | the continuation requirement: insufficient evidence → **post-execution observation captured** → second informed computation → artifact → authorization → verified → proven |
| `TestExecutionTrace_CapabilityRequestsAreEvidenced` | a model capability request produces canonical evidence naming the capability id |
| `TestSynthesizeBuildTodosFromMutation_NoHardcodedFilenames` | §13 regression: no objective wording — including the exact legacy trigger — may cause the runtime to infer a target filename |

Full suite: `go build ./...` clean · `go test ./...` 201/201 packages ok ·
`go test -race -count=1 ./...` clean · `golangci-lint run --timeout=5m ./...` **0 issues**.

## J. Remaining gaps

Not hidden. Each is a real, unresolved limitation.

1. **Process lifecycle as a model-requestable capability.** `runtime.serve`,
   `runtime.fetch`, `runtime.inspect` and `command.run` are authorized, evidenced and
   implemented, but reachable only through the behavioural stage. Background-process
   start/stream/kill does not exist at all (`ExecShell` and `execution.Runner` both buffer
   output). P2 in the mission; not implemented because the audit did not prove it is
   required for canonical objective execution.

2. **`BehaviorRequired` is still a substring heuristic** (`behavior.go:99-125`) over ~23
   English fragments. The capability layer no longer depends on it, so capability access
   is no longer gated by word choice — but whether the *behavioural proof gate* engages
   still is. An outcome-oriented objective phrased without those words skips behavioural
   proof. Replacing the heuristic with an evidence-derived decision is the correct fix and
   is **not done**.

3. **Capability results are evidenced but not yet returned as `ExecutionProof` content.**
   Each capability call publishes `stage.completed` + `engine.activity` and returns a
   `capability.Evidence` record, but `ExecutionProof` still carries only the
   `WorkspaceObservations` **counter** (`executor.go:288`). A reader of a sealed proof
   cannot enumerate which capabilities ran. `ExecutionProof` is a serialized public
   shape; adding a field is a contract change.

4. **The model can request inspection, not mutation.** A mutation request from the model
   is still an artifact in the response stream, authorized by the Control Plane at
   `RuntimeExecutor` admission and applied only after the approval gate. This is correct
   (§12) and is not a gap, but it does mean the model cannot express "now patch what you
   just read" as a single typed action.

5. **`izeno run` still carries the portfolio/to-do benchmark gate** in
   `internal/domain/capability` (`defaults.go:84-92, 282`, `alignment.go:54-145`), reachable
   only from `internal/app/pipeline.go`. It cannot affect `$prompt`. It is runtime code, not
   test infrastructure, and §13 would move it. **Not done.**

6. **Markup-specific detectors remain in the compose-wired planning layer**:
   `internal/engine/inference/detect.go:130,168,198`,
   `internal/engine/strategy/greenfield.go:60-80` (`planFiles` always emits `index.html`),
   `internal/engine/adapter/staticweb.go:81-91`,
   `internal/modes/investigate/{engine.go:1105-1117, dispatcher.go:432,451}`,
   `internal/knowledge/graph.go:188-191, 234-239`. These are *detection and scaffolding*
   rather than objective-fulfilment authority, and some are legitimate domain adapters —
   but `greenfield.go` still infers target filenames from objective wording. **Not done.**

7. **Dormant second mutation path.** `execution.ToolCallBuffer.ApplyApproved`
   (`toolcalls.go:130`) and `DispatchToolCalls` (`:334`) write via `os.WriteFile`,
   bypassing the mutation boundary, reachable from `ui/keys.go:1090`. Inert because
   nothing ever populates the buffer. A latent hazard, not an active bypass. **Not
   removed** — deleting it is a cleanup outside the audit's proven scope.

8. **No live-provider validation.** Every capability finding is proven with a scripted
   provider against the real Driver and RuntimeExecutor. The tool loop's real wire
   behaviour with a live OpenRouter account was not exercised (no credentials), so
   "a capable general-purpose LLM can request capabilities" is proven at the
   contract-and-wiring layer, not end-to-end over the network.

---

## §16 — Portfolio benchmark rerun

The portfolio benchmark was rerun **after** the generic benchmark passed, with no
special runtime code added. `test/benchmark` remains test infrastructure and its
offline suite is green (`go test ./test/benchmark/` → ok). The live sweep is
credential-gated by design (`OPENROUTER_API_KEY`, `test/benchmark/live.go:26-29`), so
it is offline by default and was not executed without credentials.

The runtime trace for a portfolio-shaped objective now follows the same canonical
sequence as every other objective:

```
discover → read → understand → plan → modify → observe → verify → evaluate
        → repair/replan (with post-execution observation) → verify → truthful conclusion
```

The removed `html → index.html` mappings (`update.go`, `archetype_guard.go`,
`plan/engine.go`) and the extension-gated build evidence (`autonomy_route.go`) were
benchmark-shape helpers. None of them were required for the trace; the trace is
produced by authority and evidence alone.

**No visual-quality claim is made.** If the model produces mediocre markup, that is the
model's output quality, not a runtime capability. The benchmark measures the runtime.

---

## §20 — The answer, with evidence

> Can a capable general-purpose LLM now use IZEN as a controlled runtime to inspect a
> workspace, obtain relevant evidence, perform an authorized computation, execute a
> change, observe the resulting state, verify it, and — when the objective is not yet
> satisfied — perform another informed and authorized computation before reaching a
> truthful conclusion?

# PARTIAL

Not YES, because three links in the chain are still incomplete (§J.1, §J.2, §J.3):
process/network capabilities are not model-requestable by design; the behavioural proof
gate is still gated by an English substring heuristic; and capability evidence is not yet
enumerated inside the sealed `ExecutionProof`.

Not NO, because every link that the audit proved missing has been closed and each is
demonstrated by an executable trace.

### Proof, from the recorded canonical trace

`TestExecutionTrace_UnprovenObjectiveContinues` — a Go-module workspace, zero markup,
real `Driver` + real `RuntimeExecutor` + production `ExecutorAdapter`, only the model
scripted. Verbatim event sequence (50 events, 13 loop transitions):

```
objective.accepted    execution.admission.decision
                      execution.strategy.selected
                      execution.target.resolved :: internal/worker/pool.go
context.acquired      execution.context.compilation
                      execution.context.prepared
model.computation     execution.model.invoked / provider.execution / provider.response
observation           execution.failed            ← prose, no artifact: INSUFFICIENT
                      execution.evidence
                      loop.transition ×3
                      [loop] post-execution observation captured for repair re-prompt   ← the closed boundary
next computation      execution.started → admission → target.resolved
                      execution.context.prepared          ← real workspace context, re-derived
                      execution.model.invoked            ← SECOND, INFORMED computation
                      execution.artifact.produced
proposal.authorized   approval.required                   ← authorization gate
                      execution.admission.decision
mutation.applied      execution.mutation.started / mutation.completed :: changed
verification          execution.verification.completed :: passed
                      execution.evidence
objective.evaluated   PROVEN  (verified by asserting the applied bytes)
```

### Each link, mapped to evidence

| Required link | Status | Evidence |
|---|---|---|
| inspect a workspace | **YES** | `read_file`/`list_directory`/`search_codebase`/`symbol_lookup` are attached whenever the contract is tool-bearing (`protocol/contract.go:388`), for **every** model, not a 2-model allowlist (`providers/openrouter.go`) |
| obtain relevant evidence | **YES** | results are `capability.Evidence`, published as `capability.execute` (`TestExecutionTrace_CapabilityRequestsAreEvidenced`) |
| authorized computation | **YES** | every capability call is checked against the executor's admission vector; the zero vector refuses and returns no content (`TestCapabilityToolRunnerRefusesUngrantedCapability`) |
| execute a change | **YES** | `mutation.completed :: changed` in the trace above |
| observe resulting state | **YES** | `[loop] post-execution observation captured for repair re-prompt` — live declared-target state, grant-bounded |
| verify | **YES** | `verification.completed :: passed` |
| another informed computation | **YES** | `execution.model.invoked` appears **twice**; the second carries re-derived context plus the observation |
| truthful conclusion | **YES** | `TestDomainNeutralBenchmark_UnprovenObjectiveNeverCompletes`: prose is never written to disk and never completes |
| **process lifecycle** | **NO** | not exposed; background process control does not exist (§J.1) |
| **behavioural proof generality** | **PARTIAL** | still gated by `BehaviorRequired`'s substring heuristic (§J.2) |
| **evidence inside the sealed proof** | **PARTIAL** | capability evidence is published, but `ExecutionProof` carries only a counter (§J.3) |

### The one thing that must not be misread

`mutation.completed :: changed` appears **before** `objective.evaluated`, and the run
still had to reach PROVEN through `evaluatePatch`, which independently requires an
observed delta on a *declared* target plus verifier PASS
(`objective_authority.go:766-786`). A file change is never the proof; it is one of five
conjuncts. `TestDomainNeutralBenchmark_UnprovenObjectiveNeverCompletes` is the negative
control that would fail if it were.