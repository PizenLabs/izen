# PHASE 13 REPORT — EXECUTION STATE, EVIDENCE & UX CONVERGENCE

| Field | Value |
| --- | --- |
| Status | **COMPLETE** |
| Date | 2026-09-28 |
| Branch | `fix/runtime` |
| Scope | The user-facing execution lifecycle: authorization semantics, evidence-gated completion, artifact-centric observability, diff-as-evidence, single-owner execution state, main-UI/Trace boundary, layer-labelled context telemetry |
| Governing principle | **Dynamic Path, Static Authority, Truthful State Transition.** LLMMs reason. The Engine decides. Capabilities execute. Humans remain in control. |
| Verification | `go build ./...` ✅ · `gofmt -l ./cmd ./internal` ✅ clean · `go vet ./...` ✅ clean · `go test ./...` ✅ **197/197 tested packages** (29 carry no test files) · `go test -race -count=1 ./...` ✅ clean, no data races |
| Production files changed | 14 (13 modified + 1 new) · **New runtime code: 1 file** (`internal/presentation/completion_gate.go`) |
| Tests added | 8 new files · 11 existing tests corrected (each with its intent preserved) |
| Diff | 24 modified, 9 new · **+1 309 / −186** |
| Non-goal compliance | No new executor · no new scheduler · no new state machine · no second diff engine · no second context cache · no second mutation authority · no chain-of-thought exposure · no fabricated progress · no fabricated diff · no context pruning · no unrelated subsystem modified |

> **Naming note.** `PHASE_12_AGENT_PROTOCOL_ARCHITECTURE.md` and `PHASE_12_EXECUTION_INTELLIGENCE_REPORT.md` are both Phase 12 documents (agent-protocol boundary; execution-intelligence convergence). Phase 12 is *preserved and not regressed* by this phase — see §16.

---

## 1. Executive summary

The runtime was largely correct. **The TUI was lying about it.**

Audit first, map ownership second, repair minimally third. Four concrete defects were reproduced before a single line of production code changed:

```
$prompt "…redesign a professional personal portfolio page… using HTML, CSS, and JS"
  │
  ├─ D1  AUTONOMY PROPOSAL / AUTONOMY REQUEST
  │      rendered "[I] Inspect Diff" when NO diff existed
  │      ⇒ the action set was a STATIC template, derived from the request's SHAPE
  │      ⇒ autonomy.Proposal has no diff field at all
  │      ⇒ the label promised a payload the function never opened
  │
  ├─ D2  the SAME current step rendered TWICE in one frame
  │      ⇒ loadingDock  line 1 = "✻ " + execView.HumanStep()
  │      ⇒ narrative    last line = that same Current step
  │      ⇒ the runtime stage added a THIRD wording: "Model ● streaming" / "Model responding"
  │      ⇒ the EXECUTING header titled from a FOURTH source (m.shimmerText)
  │
  ├─ D3  COMPLETION without evidence
  │      ⇒ Graph.CompleteExecution emitted execution.finished(success=true)
  │        BEFORE the evidence was sealed (sealing lives in finalizeResult)
  │      ⇒ 4 of 5 call sites asserted success with res.ArtifactKind = "" and no apply
  │      ⇒ the evidence layer ALREADY contradicted them (REQUIRES_REVIEW ⇒ not authoritative)
  │      ⇒ the bus event was the lying one, and nothing downstream could tell
  │
  └─ D4  diff evidence UNDER-reported
         ⇒ recordMutationEvidence measured strings.Contains(patch.Modified, "@@")
           against the raw ARTIFACT
         ⇒ a bounded SEARCH/REPLACE artifact has no hunk header
         ⇒ a change that demonstrably happened reported DiffPresent: false
         ⇒ the runtime's real compiled unified diff (changeset → compileDiff) was never measured
```

**All four are repaired inside canonical owners, and the repairs are locked by tests.**

The repair in one sentence: **move the evidence, derive the actions, pick one owner per state.**

| Defect | Repair | Owner |
| --- | --- | --- |
| D1 | The action set became a **derived** function of runtime state. One list feeds both the rendered hints and the ↑/↓ cursor, so the two cannot disagree. `Inspect` is bound to the existence of a real candidate/diff object. | `internal/ui/autonomy_proposal.go` |
| D2 | The execution narrative panel became the **single** main-UI owner of the current step. The dock keeps its glyph and its tip and renders no status text while the panel is mounted; the stage returns only when nothing else is mounted; the header echoes the panel. | `internal/ui/loading.go`, `topbar.go` |
| D3 | `execution.finished` can no longer be observed without the sealed evidence. One choke point seals → publishes evidence → publishes completion. Completion is then a **verdict on evidence**, with a new explicit non-complete terminal state. | `internal/execution/executor.go`, `internal/presentation/` |
| D4 | Diff evidence is measured from the runtime's **own** compiled unified diff. One diff engine; the UI consumes the same object the human was shown. | `internal/execution/patch.go` |

---

## 2. Pre-change audit

### 2.1 Reproduced failures

| Failure | Evidence |
| --- | --- |
| `Inspect Diff` with no diff | `autonomy_proposal.go:379-382` rendered the three-action line unconditionally. `autonomy.Proposal` (`internal/autonomy/proposal.go:29-60`) has **no diff field**. `toggleAutonomyProposalInspect` expanded only `objective/required/missing/scope` — the label was false. |
| Duplicate model state | `model.go:5956` and `:5963` emit `renderLoadingDock()` and `renderExecutionLayered()` back-to-back under the **identical** predicate. The dock's line was `flake + HumanStep()`; the panel re-prints the whole list with the last one `Current`. Two claims about one state, in one frame. |
| A third wording | `model.go:3238` set stage `stageStreaming` ("Model ● streaming") on `ProviderFirstToken`; `narrative.go:94` produced "Model responding" from the same event. |
| Header/narrative disagreement | `topbar.go:84-88` (pre-change) titled from `m.shimmerText`; the dock *preferred* `execView` over `shimmerText`. "WAITING FOR MODEL..." could sit directly above "Applying changes". |
| False completion | `graph.go:570` `CompleteExecution` has no artifact / mutation / verification check. 4 of 5 call sites asserted `success=true` with `res.ArtifactKind = ""` and no apply (`executor.go:1495, 1742, 1757`; plus `:1586` read-only with verification `Skip`ped). |
| Evidence arrived too late | `Approve` called `g.CompleteExecution` at `:2188`; `sealTerminalEvidence` only ran inside `finalizeResult` at `:4368` — **after** the completion event. A consumer could not have gated on evidence even if it tried. |
| Diff under-reported | `patch.go:249` (pre-change, in the old `recordMutationEvidence`) `ev.DiffPresent = patch.Modified != "" && strings.Contains(patch.Modified, "@@")` — measured against the artifact, not `pm.diffs[i]`. |
| Ambiguous token telemetry | `ContextPreparedPayload.Tokens` is `EstimateTokens(assembled) = ceil(runes/4)` (`contextcompiler/budget.go:347-352`), rendered as `~%d model tokens` — indistinguishable from provider prompt tokens. |

### 2.2 Actual root causes

1. **A static action template instead of a state-derived action set.** The UI assumed the *shape* of the request rather than reading its *state*. Two independent hard-coded lists (a `[]autonomy.ProposalAction` for navigation, a string triple for rendering) meant the rendered set and the reachable set were separate inventions.
2. **No single owner per semantic state.** Three surfaces each claimed a piece of "what the model is doing", fed by one projection but with independent text. Two of them could disagree, and neither was authoritative.
3. **The completion verdict was transported as a boolean.** `ExecutionFinishedPayload.Success` means *"terminated without an error"* but was consumed as *"objective satisfied"*. The evidence gate **already existed** (`presentation.ProjectEvidence`, `evidence_projection.go:73`) — it simply had nothing to gate, because the evidence had not arrived yet, and because `finishedSentence` never consulted it.
4. **Two diff measurements, one of them wrong.** The runtime compiles a real unified diff (`executor.compileDiff` → `changeset.Pipeline`); the mutation boundary measured something else.

### 2.3 Affected owners — all pre-existing, none added

| Layer | Owner | Phase-13 disposition |
| --- | --- | --- |
| Decision | `autonomy.Engine` / `AutonomyController` | **untouched** — authority preserved |
| Event emission | `execution/graph.Graph` | +1 evidence-carrying emitter |
| Lifecycle | `execution.RuntimeExecutor` | ordering only, one choke point |
| Mutation authority | `execution.PatchManager` + `MutationSet` | measurement source corrected |
| Diff compiler | `changeset.Pipeline` | **untouched** — reused, not duplicated |
| Evidence | `execution/evidence.go` | **untouched** — the gate that existed is now actually fed |
| Verification | `execution/verify.go` | **untouched** — the gate that ran is now reported |
| Projection | `presentation.ExecutionProjection` | evidence gate + artifact ledger |
| Cache | `contextcompiler.Compiler` | **untouched** — reused, audited, labelled |
| Presentation | `internal/ui` | state-derived actions, one owner, Trace boundary |

---

## 3. Execution state map

**No new state machine was introduced.** The canonical user-facing state remains the existing `presentation.ViewPhase`, extended by exactly one terminal state that the hard invariant requires.

```
IDLE ──execution.started──────────────▶ RUNNING
                                          │
                              approval.required
                                          ▼
                                   WAITING_APPROVAL
                                          │ mutation.started
                                          ▼
                                       RUNNING ◀── provider.* / artifact.produced
                                          │
                    ┌─── mutation.completed (+ real apply evidence)
                    ▼
        sealTerminalEvidence  ──▶  emit execution.evidence        ← EVIDENCE FIRST
                    │
                    ▼
              execution.finished(success)
                    │
        ┌───────────┴────────────┬─────────────────┐
        ▼                        ▼                 ▼
   success=false          gate GRANTS        gate REFUSES
        │                        │                 │
        ▼                        ▼                 ▼
     FAILED                 COMPLETED      UNSUBSTANTIATED
   (or "cancelled",                        ("Not completed — <reason>")
    a terminal non-success)
```

New terminal state — `presentation.PhaseUnsubstantiated` (`execution_projection.go:48`):

> *"the runtime terminated, but the sealed evidence does not substantiate a completion claim (no record was published, the outcome is not COMMITTED, or the mutation set is tainted). It exists so an unsubstantiated attempt can never be rendered as a completed one."*

This is **one more terminal outcome of the same reducer**, not a parallel machine. §6 of the mandate demands an explicit non-complete state; this is it, and it carries a deterministic reason so a refusal is never vaguer than the fact that caused it.

### 3.1 State ownership

| State | Owner | Written by |
| --- | --- | --- |
| `ViewPhase` | `presentation` | `ExecutionProjection.Project` — a pure event reducer |
| **Completion** authority | `presentation.CompletionGate` | reads the sealed evidence; **holds no authority of its own** |
| Mutation authority | `execution.PatchManager` via `MutationSet` | **unchanged** |
| Diff object | `changeset.Pipeline` via `RuntimeExecutor.compileDiff` | **unchanged** — now actually consumed |
| Authorization | `autonomy.Engine` + `AutonomyController` | **unchanged** — the UI only renders the request |
| UI authorization *card* | `internal/ui` | a **projection**, never an authority |

---

## 4. Authorization UX

### 4.1 Before

```
┌─ ⚠ AUTONOMY REQUEST: modification ──────────────────────┐
│ Target: index.html │ Risk: LOW │ Scope: 1 file │ modif… │
│ Plan:   inspect target -> analyze structure -> propo…    │
│ Action: [Enter] Approve & Run   [I] Inspect Diff         │
│         [Esc] Reject                                    │
└─────────────────────────────────────────────────────────┘
```

Three false claims:
- **`[I] Inspect Diff`** — for a diff that does not exist at authorization time.
- **`Plan: …`** — for a plan that has not been made.
- **`Approve & Run`** — a request is not a proposal.

### 4.2 After

```
┌─ ⚠ EXECUTION AUTHORIZATION ─────────────────────────────┐
│ Target: index.html │ Risk: LOW │ Scope: 1 file │ modif… │
│ Capabilities: read, analyze, propose, mutate            │
│ No mutation has occurred.                               │
│ Action: [Enter] Execute   [Esc] Cancel                  │
└─────────────────────────────────────────────────────────┘
```

Every line is a **fact observed at authorization-request time**: the classified objective, its boundary, the capability vector being requested, and the explicit absence of any change.

### 4.3 The three concepts, now distinct

| Concept | Owner | Where it lives |
| --- | --- | --- |
| **Autonomy decision** | `autonomy.Engine.Decide` | `auto_continue` / `ask_user` / `block` / direct answer |
| **Authorization request** | `presentation` of that verdict | this card — nothing proposed, planned, or written |
| **Authorization grant** | the runtime, on the human's decision | `m.autonomy.GrantDefault(...)` inside `executeAutonomyProposal` |

The UI never mints a grant. Selecting `Execute` releases a human decision into the runtime, which re-runs the decision on the **same input** (no command parser, no re-submitted prompt) and continues.

No new terminology was invented. The card keeps the existing house style; only the noun changed, from *"proposal"* — which it never was — to **"EXECUTION AUTHORIZATION"**, which it is.

### 4.4 State-dependent actions

`authorizationActions()` (`autonomy_proposal.go:102`) is the **single source** for both the rendered hints and the ↑/↓ cursor:

| Runtime state | Derived action set | Rendered |
| --- | --- | --- |
| No candidate (authorization request) | `[Execute, Cancel]` | **no `Inspect`** |
| `pendingHotfixPatch.Modified != ""` | `[Execute, Inspect, Cancel]` | `[I] Inspect Diff` |
| `pendingProposals[i].Diff != ""` | `[Execute, Inspect, Cancel]` | `[I] Inspect Diff` |
| Staged proposal with **blank** diff | `[Execute, Cancel]` | **no `Inspect`** |

The invariant is now **structural**, not conventional:

- The cursor wraps over the derived list and **re-anchors** when the set shrinks, so a stale index cannot activate the wrong action.
- The `I` binding is gated on the action existing (`keys.go:499`) — otherwise the key **falls through to the input composer** rather than silently doing something the UI never advertised.
- Rendering and navigation read the same function, so the rendered set and the reachable set cannot drift apart.

`Inspect` reveals the **real** candidate text plus the decision facts. A blank diff is not a candidate.

---

## 5. Evidence pipeline

```
MODEL OUTPUT ─▶ ARTIFACT CANDIDATE ─▶ DIFF AVAILABLE ─▶ MUTATION ─▶ VERIFICATION ─▶ SEALED EVIDENCE ─▶ COMPLETED
  provider       ArtifactCandidate     changeset          PatchManager  Verifier.RunAll   sealTerminalEvidence   gate
                 + artifact.produced   compileDiff        ApplyContext  (inside apply)                       grants
                                                          mutation.completed (+ real evidence)
```

| Stage | Owner | Object |
| --- | --- | --- |
| Candidate | `RuntimeExecutor` | `ArtifactCandidate` (`executor.go:1683`) + `artifact.produced` |
| Diff | `RuntimeExecutor.compileDiff` → `changeset.Pipeline` | the one unified diff, index-aligned with targets |
| Mutation | `PatchManager.ApplyContext` (`patch.go:986`) | per-target `ApplyExecuted && FilesystemChanged` |
| Verification | `Verifier.RunAll` inside the apply gate | the report the gate actually produced |
| Evidence | `sealTerminalEvidence` | the immutable `ExecutionEvidence` |
| Completion | `presentation.CompletionGate` | the verdict |

### 5.1 The evidence ordering contract

Every terminal success now routes through **one** choke point, `RuntimeExecutor.completeExecution` (`executor.go:4428`):

```
sealTerminalEvidence  →  emit execution.evidence  →  emit execution.finished(success=true)
```

A completion claim is never broadcast ahead of the record that substantiates it. A projector that observes `execution.finished` therefore **always** holds the evidence and can gate on it.

The previous architecture lock ("evidence is born ONLY at `finalizeResult`") is **strengthened, not relaxed**:

- exactly two in-runtime seal sites: `completeExecution` (ordered choke point) + `finalizeResult` (idempotent backstop — `sealTerminalEvidence` refuses to re-seal);
- `Graph.CompleteExecution` must be **unreachable from any other function** in the executor;
- within `completeExecution`, `sealTerminalEvidence` must appear **before** `CompleteExecution` (AST position assertion).

### 5.2 The hard completion invariant, mechanically

`CompletionGate.Verdict()` (`completion_gate.go:101`) — rules in precedence order:

| # | Condition | Verdict |
| --- | --- | --- |
| 1 | No sealed record at all | **refused** — *"no sealed execution evidence was published for this attempt"* |
| 2 | Tainted mutations | **refused** — *"…tainted mutations; partial state is not truth"* |
| 3 | Outcome not `COMMITTED` | **refused**, naming the outcome |
| 4 | `COMMITTED` + untainted, but **zero durable file changes** | **refused** — *"…committed no durable file change; the objective was not met by mutation"* |
| 5 | Committed + untainted + ≥1 proven mutation | **granted** |
| 6 | No mutation boundary entered, no evidence obligation | granted — deliberate scope |

**Rule 4** is what closes `executor.go:1742` / `:1757`, where `COMMITTED` was sealed over an empty mutation set — the exact shape of a false completion.

**Scope note.** The invariant is scoped to *mutation executions*, and deliberately so: a read-only answer has no filesystem claim to substantiate, and demanding a mutation record from it would be a category error, not rigour. The gate reads the runtime's **own** policy rulings — `Outcome` and `Tainted` already fold in whether a verification gate was required and whether it passed. The gate never re-runs or second-guesses verification policy; that would make the UI an authority.

**Model completion ≠ task completion** (§7). The two are separate records, in separate fields, from separate events:

| Field | Source | Meaning |
| --- | --- | --- |
| `ProviderState` | `provider.*` events | `waiting` / `streaming` / `done` — the *invocation* |
| `FinishReason` | `provider.response` | describes the **generation** |
| `ProviderCalls` | `model.invoked` count | the denominator for efficiency |
| `VerificationRan` / `VerificationPassed` | `verification.completed` | the *gate* |
| `EvidenceOutcome` / `EvidenceTainted` / `FilesMutated` | `execution.evidence` | the *objective* |

`finish_reason=length` is preserved as an invocation fact and never becomes a task verdict. `finish_reason=stop` is likewise not proof of task completion.

### 5.3 Diff is evidence, not decoration

The fix: `PatchManager.SetCompiledDiffs` (`patch.go:181`) receives the runtime's **own** compiled unified diffs, and `recordMutationEvidence` (`patch.go:243`) measures from **that**, not from the raw artifact.

```
before:  ev.DiffPresent = strings.Contains(patch.Modified, "@@")     ← the ARTIFACT
after:   diff := pm.compiledDiffFor(patch.File)                    ← the runtime's DIFF
         ev.DiffPresent = diff != ""
         ev.DiffAdds, ev.DiffRemoves = countUnifiedDiffLines(diff)
```

Consequences:

- **One diff engine.** `changeset.Pipeline` is the only producer. No display-time diff was added.
- **The published evidence and the reviewed evidence are the same object** — the same text the approval surface showed the human.
- **The absent case stays absent.** A target with no compiled diff reports `DiffPresent: false` and `+0 −0` is unreachable. A renderer that has no diff evidence has no diff statistics to show.
- **A target the boundary proved unchanged carries no diff**, even if a diff was compiled for it — the line metrics of an artifact that never landed would be a false claim about a change.
- `compiledDiffsByTarget` (`executor.go:4347`) maps **by target, not by position**, so an empty entry is never silently attributed to a neighbour.

---

## 6. Main UI / Trace boundary

Every event was classified as **user-facing execution evidence** or **diagnostic telemetry**. Only the second belongs in Trace.

### 6.1 Moved to Trace

| Line | Was | Why |
| --- | --- | --- |
| `[stream] … tok prompt + tok completion` | main narrative | The footer already binds the same live `↑/↓` counters; the details panel reports them layer-labelled. A third copy added nothing a user could act on. |
| `[phase] plan → build` | main narrative | The phase is already projected onto the state machine and rendered by the top-bar badge and the EXECUTING header. |

Both are re-labelled on arrival (`tok input` → `tok prompt` / `tok completion`, matching the footer's vocabulary) and **still reach Trace** — the boundary moves information, it does not discard it.

### 6.2 Retained in Main UI (user-facing execution evidence)

The authorization card · the artifact ledger · per-file mutation results with real diff metrics · the verification verdict · the sealed-evidence verdict · the layer-labelled compiled-context line.

### 6.3 Retained in Trace (diagnostic telemetry)

Raw compilation metrics · provider/usage/reasoning telemetry · `[context]` / `[plan:*]` / `[loop]` / `[barrier]` diagnostics · `conv_rev` / `spec_rev` / `ctx_rev` · execution IDs · the full machine event stream (DEBUG layer, `Ctrl+O`).

No log line was moved into a new visual widget merely to hide it. Information was reclassified, not relocated cosmetically.

### 6.4 No chain-of-thought (§12)

The reasoning surfaces are **untouched**. Operational reasoning only — `Reading index.html`, `Gathering context`, `Analyzing`, `Applying changes`, `Applied change to index.html`, `Verified changes`. `ReasoningTelemetryPayload` carries duration + token count and never content. No `I think…`, no raw hidden reasoning, no reasoning theater.

### 6.5 No silent execution (§13)

The pre-first-step window previously had no event-derived claim to show. Now:

| Phase | Dock | Panel |
| --- | --- | --- |
| `execution.started` only, no step yet | `✻ Inspecting project` + tip | (empty — nothing to claim) |
| Step + candidates | `✻` + tip | `● Reading index.html` … `── generating ──` `● index.html` |
| After apply | `✻` + tip | `── mutation ──` `● index.html +84 -12` `○ styles.css (nochange)` `● 1 file(s) updated` |
| Verification | `✻` + tip | `◇ Verified changes` |

While execution is active the user always sees the current phase, the current artifact, and the provider invocation state. Raw model output is **not** dumped into the main interface to create activity — the target is *smooth + informative*, not *busy + noisy*.

---

## 7. Context & cache audit

### 7.1 What the number actually was

`ContextPreparedPayload.Tokens` = `EstimateTokens(assembled prompt)` = `ceil(runes / 4)` — a coarse accounting heuristic (`contextcompiler/budget.go:347-352`). **Not** a tokenizer, **not** a workspace size, **not** provider prompt tokens, **not** a budget.

It was printed as `~%d model tokens`, which is exactly the conflation §14 forbids. The four quantities in an execution are now separately labelled:

| Figure | Meaning | Rendered as |
| --- | --- | --- |
| compiled context | `runes/4` of the assembled prompt | `compiled context (est.): ~812 tok` · `Context compiled: 3 channel(s), ~812 tok (estimate)` |
| provider prompt | provider-reported | `provider tokens: 1840 prompt / 612 completion` |
| reasoning | provider-attributed | `reasoning (telemetry): 2.1s (412 tok)` |
| execution budget | `BudgetTokens` | Trace: `[runtime] context compiled: … budget=16000 …` |

### 7.2 No pruning was introduced (§15)

The principle is *sufficient context*, not maximum and not minimum. The canonical `contextcompiler.Compiler` fingerprint cache is **unchanged** and remains the only context cache. No cache was duplicated and no context-compilation logic was reimplemented.

### 7.3 Cache matrix (§20) — now complete

| Case | Test | Result |
| --- | --- | --- |
| hit | `TestPhase12_ModelContextReuseIsObservable` | ✅ |
| miss | same | ✅ |
| relevant invalidation | `TestPhase12_ChangedFileIsNeverServedFromCache`, `TestPhase13_RelevantChangeAlwaysMisses` | ✅ |
| **unrelated change** | `TestPhase13_UnrelatedChangeDoesNotInvalidate` *(new)* | ✅ — keyed on the *compiled context*, not the workspace |
| bounded eviction | `TestPhase12_EvictionIsBoundedFIFO`, `TestPhase13_CacheIsBoundedAndReusableUnderChurn` *(new)* | ✅ |
| sufficient, not minimal | `TestPhase13_ContextIsSufficientNotMinimal` *(new)* | ✅ — a wider context compiles to *more* tokens; 0 dropped, 0 truncated |

The unrelated-change case is what distinguishes a working cache from a paranoid one: keying on the whole workspace would invalidate on every unrelated edit and turn the cache into overhead.

The three distinct context kinds remain separately reportable, inherited from Phase 12: **workspace** (correct, correctly invalidated), **model** (reused, now observable), **execution** (correct).

---

## 8. Token / provider metrics

Recorded internally, surfaced compactly. Nothing is estimated.

| Metric | Source | Surface |
| --- | --- | --- |
| `ProviderCalls` | counted from observed `model.invoked` | `provider calls: 1` |
| `TokenInput` / `TokenOutput` | provider-reported usage | `provider tokens: …` |
| `ReasoningTokens` / `ReasoningDuration` | `reasoning.telemetry` | EXPANDED details |
| `FinishReason` | `provider.response` | EXPANDED details, **separate field** |
| `ProviderState` | provider events only | EXPANDED details |
| `ContextTokens` / `ContextCacheHit` | `context.prepared` | layer-labelled |
| `CandidateCount` / `MutatedFiles` | ledger, **derived** not incremented | artifact ledger |
| `VerificationRan` / `VerificationPassed` / `VerificationSteps` | `verification.completed` | EXPANDED details |
| `EvidenceOutcome` / `EvidenceTainted` / `FilesMutated` | `execution.evidence` | EXPANDED details |
| `Duration` | event timestamps | EXPANDED details |

**Efficiency is measured, not claimed.** §17's caution is respected: "efficiency" is never asserted because a global token limit decreased. The denominator (`ProviderCalls`) and the numerator (verified files mutated) are both observable, so `useful verified outcome / model computation` is computable. The objective is minimum *unnecessary* computation while preserving sufficient context — which is precisely why no pruning was performed.

`TargetEvidence` counters are **recomputed from the ledger** (`recountTargets`, `execution_projection.go:241`) rather than incremented per event, so a duplicated or replayed event cannot inflate a count.

---

## 9. Architectural conformance

### 9.1 No second runtime (§18)

**Proven absent:** no `UIExecutor`, `UIRuntime`, `ExecutionRuntime`, `ProgressScheduler`, `ArtifactScheduler`, or second continuation engine. No parallel state machine.

Dependency direction is strictly one-way: `internal/ui` → `internal/presentation` → `internal/execution`. `execution` imports **neither** `ui` **nor** `presentation`. The UI is a **consumer**; it cannot become an authority. (This is also why the golden test is split across two layers — an in-package test in `execution` cannot import `presentation` without a cycle.)

### 9.2 Canonical ownership

| Component | Owner | Changed? |
| --- | --- | --- |
| **Driver** | `internal/runtime/autonomy.Driver` | **No** |
| **Planner** | `internal/execution/planner` | **No** |
| **Context** | `internal/contextcompiler` | **No** — reused, audited, labelled |
| **RuntimeExecutor** | `internal/execution/executor.go` | Ordering only — one choke point, no new phase |
| **Evidence** | `internal/execution/evidence.go` | **No** — the gate that existed is now fed |
| **Verification** | `internal/execution/verify.go` | **No** — the gate that ran is now reported |
| **Continuation** | `artifact_step.go` / `llmstep.StepState` | **No** — Phase 12 invariants preserved |

### 9.3 Mutation authority

The canonical path is unchanged: `RuntimeExecutor.Approve` → `PatchManager.ApplyContext` (`patch.go:986`), guarded by the OCC pre-commit gate. The competing writers the audit surfaced (`ToolCallBuffer.ApplyApproved`, `Substrate.ExecuteUnit`, `runtime/executor/file_executor.go`, `scope.go`, `output/tee.go`, `execution/boundary.go`) remain unreachable from the `$prompt` path and were **not touched** — modifying them would be an unrelated refactor without evidence.

---

## 10. Test matrix

### 10.1 Production reachability map ($prompt)

```
$prompt
 ↓  internal/ui/intent_dispatch.go            routePromptDirective
 ↓  internal/autonomy/intent.go               Classify            ⇒ IntentModification      [owner: autonomy]
 ↓  internal/autonomy/workspace.go            SelectWorkspace     ⇒ WorkspaceBuild          [owner: autonomy]
 ↓  internal/autonomy/controller.go:114       Decide              ⇒ DecisionAskUser         [owner: autonomy]
 ↓  internal/ui/autonomy_proposal.go:113      requestAutonomyProposal → EXECUTION AUTHORIZATION card
 ↓  internal/ui/autonomy_proposal.go:170      executeAutonomyProposal → GrantDefault           [owner: autonomy]
 ↓  internal/runtime/autonomy/driver.go:315   Driver.Run
 ↓  internal/execution/planner                Decompose / DAG
 ↓  internal/contextcompiler/compiler.go:407  Compile (canonical fingerprint cache)
 ↓  internal/execution/artifact_step.go:172   invokeArtifactBoundedStep (llmstep.StepState)
 ↓  internal/execution/executor.go:1227       RuntimeExecutor.Execute
 ↓  internal/execution/executor.go:3573       invokeStream → provider.ExecuteStream
 ↓  internal/execution/executor.go:1683       ArtifactCandidate  ← EVIDENCE
 ↓  internal/execution/executor.go:2944       compileDiff → changeset.Pipeline   ← THE ONE DIFF
 ↓  approval.required → human gate
 ↓  internal/execution/executor.go:2064       SetCompiledDiffs → mutation.started
 ↓  internal/execution/patch.go:486           PatchManager.ApplyContext         [MUTATION AUTHORITY]
 ↓  internal/execution/patch.go:243           recordMutationEvidence → mutation.completed (+ real diff)
 ↓  internal/execution/patch.go:823           Verifier.RunAll (inside the apply gate)
 ↓                                            → verification.completed
 ↓  internal/execution/executor.go:4428       completeExecution:
 ↓      sealTerminalEvidence → execution.evidence        ← EVIDENCE BEFORE COMPLETION
 ↓      Graph.CompleteExecution → execution.finished
 ↓  internal/presentation/completion_gate.go:101  CompletionGate.Verdict
 ↓  internal/presentation/execution_projection.go:529 terminalState
 ↓  internal/presentation/narrative.go            RewriteHuman(terminalSentence)
 ↓  internal/ui/loading.go:326 / :392             renderExecutionFrame / renderArtifactLedger
 ↓  TUI projection
```

**No competing implementation exists** of: execution state (`ViewPhase` has one writer), completion state (`CompletionGate` has one writer, and `Graph.CompleteExecution` is AST-locked to one call site), diff generation (`changeset.Pipeline`), mutation authority (`PatchManager.ApplyContext` (`patch.go:986`)), or continuation authority (`llmstep.StepState`).

### 10.2 Invariant tests (§20)

| Requirement | Test | Layer |
| --- | --- | --- |
| No candidate → no Inspect | `TestAuthorization_NoCandidateNoInspectDiff` | ui |
| Candidate exists → Inspect available | `TestAuthorization_CandidateEnablesInspectDiff`, `…_DiffBackedCandidateEnablesInspectDiff` | ui |
| Blank diff is not a candidate | `TestAuthorization_EmptyProposalDiffDoesNotEnableInspect` | ui |
| Navigation never lands on a hidden action | `TestAuthorization_NavigationNeverLandsOnAHiddenAction` | ui |
| Cancel → no mutation | `TestAuthorization_CancelIssuesNoGrantAndNoMutation` (byte-compares the file) | ui |
| Execute → grant created | `TestAuthorization_ExecuteIssuesTheGrant` | ui |
| Provider returned ≠ task completed | `TestCompletionGate_ProviderReturnedIsNotTaskComplete` | presentation |
| Candidate exists, mutation not applied | `TestCompletionGate_CandidateWithoutMutation` | presentation |
| Mutation applied, verification pending | `TestCompletionGate_MutationAppliedVerificationPending` | presentation |
| Failed verification → never completed | `TestCompletionGate_FailedVerificationIsNeverCompleted` | presentation |
| Verification passed → completed | `TestCompletionGate_VerifiedMutationIsCompleted` | presentation |
| Non-committed outcomes refused | `TestCompletionGate_NonCommittedOutcomesAreRefused` (4 cases) | presentation |
| Cancellation ≠ completion | `TestCompletionGate_CancellationIsNeverReportedAsCompleted` | presentation |
| Model invocation ≠ task completion | `TestCompletionGate_ModelInvocationIsNotTaskCompletion` | presentation |
| Exhaustion: prefix preserved, contract kept | `TestGolden_PortfolioRedesignEndToEnd` | execution |
| No diff → no diff stats | `TestTargetEvidence_DiffIsAbsentWhenNoDiffWasCompiled` | presentation |
| Candidate → actual diff, verbatim | `TestTargetEvidence_DiffMetricsAreVerbatim` | presentation |
| Mutation → diff/evidence consistent with disk | `TestGolden_PortfolioRedesignEndToEnd` | execution |
| "0 bytes written" truthful | `TestTargetEvidence_CandidateWithoutApplyHasNoDiff`, `TestGolden_HeldCandidateRendersNoDiffStatistics` | presentation, execution |
| Replayed events don't inflate counts | `TestTargetEvidence_RepeatedCompletionDoesNotInflateCounts` | presentation |
| Active vs pending candidates | `TestArtifactLedger_DistinguishesActiveFromPending` | ui |
| No duplicate model state | `TestNoDuplicateModelStateInOneFrame` | ui |
| Dock still owns the step when unowned | `TestDockOwnsTheStepWhenNoPanelIsMounted` | ui |
| Header agrees with the panel | `TestHeaderAgreesWithThePanel` | ui |
| Infrastructure events stay out of main UI | `TestDiagnosticTelemetryStaysOutOfTheMainNarrative` (also asserts they *reach* Trace) | ui |
| Verification reported only when observed | `TestVerificationIsReportedOnlyWhenObserved` | ui |
| Cache hit / miss / relevant / unrelated / bounded | 4 new + 4 existing | contextcompiler |
| Evidence sealed **before** completion | `TestGolden_PortfolioRedesignEndToEnd` (event index assertion) + AST lock | execution, architecture |
| One seal site; no raw `CompleteExecution` | `TestPhase2EvidenceSealedOnlyAtFinalizeResultChokePoint` (strengthened) | architecture |

### 10.3 Corrected tests

11 existing tests were updated. **Every one of them pinned the defect.** Their intent was preserved; the false assertion was replaced with the true invariant.

| Test | Pinned | Now |
| --- | --- | --- |
| `TestReducerHumanNarrative` | `"Completed"` from `success=true` alone | requires the sealed evidence |
| `TestNarrativeTerminalSentences` | `success → "Completed"` | provisional, then evidence-gated |
| `TestAutonomyValidationCase3…` | `"AUTONOMY PROPOSAL"`, Inspect present | `"EXECUTION AUTHORIZATION"`, Inspect absent |
| `TestAutonomyProposalKeyboardNavigation` | Inspect reachable with no candidate | 2-action set; `I` inert; Esc cancels with a byte-check |
| `TestCompactAutonomyGateBannerLayout` | `Plan:`, `Approve & Run`, `Inspect Diff`, `Reject` | `Capabilities:`, `No mutation has occurred.`, `Execute`, `Cancel` |
| `TestGatedProjectionFollowsRuntimeEvents` | dock restates the projected step | dock silent; the panel owns it |
| `TestExpandedViewContainsRuntimeMetadata` | `context policy:`, `tokens: 12 in` | layer-labelled figures |
| `TestPhase12_ModelContextIsTheOneUserFacing…` | `~812 model tokens` | `~812 tok (estimate)`, layer-named |
| `TestHandleDomainEventProjection` | stream/phase lines in main UI | moved to the Trace-boundary test |
| `TestBuildHotAutonomyExecution` | `Inspect` present | `Inspect` absent |
| `TestPhase2EvidenceSealedOnlyAtFinalizeResult…` | 1 seal site | 2 ordered sites + no raw completion call site |

---

## 11. Golden scenario result

```
$prompt Please review this project and redesign a professional personal portfolio
         page for me using HTML, CSS, and JS; the author's name is Tom Hunter,
         an AI Engineer.
```

Two halves, each driving its **real** production path:

- **Runtime** — `TestGolden_PortfolioRedesignEndToEnd` (`internal/execution`): the real `RuntimeExecutor`, the real artifact step, the real `PatchManager`, the real verifier, the real bus.
- **Presentation** — `TestGoldenProjection_*` (`internal/presentation`): the same semantic stream through the real projection — the only path the TUI has to execution truth.

### 11.1 Observed semantic lifecycle

```
MODIFICATION → AUTHORIZATION REQUIRED → USER AUTHORIZES → CONTEXT PREPARED
→ PROJECT UNDERSTOOD → ARTIFACT GENERATION → CANDIDATE READY (×3)
→ DIFF AVAILABLE → MUTATION → VERIFICATION → COMPLETED
```

Human narrative, verbatim from the projection:

```
Reading index.html → Waiting for approval → Gathering context → Analyzing
→ Preparing result → Applying changes → Applied change to index.html
→ Verified changes → Completed
```

A three-file apply names all three changes — not one generic "applied" line.

### 11.2 Metrics

| | |
| --- | --- |
| **provider calls** | **1** |
| **input tokens** | **1 840** (provider-reported prompt) |
| **output tokens** | **612** (provider-reported completion) |
| **reasoning tokens** | **0** |
| **continuations** | **0** |
| **cache hits** | **0** (cold compile) |
| **cache misses** | **1** |
| **compiled context** | **224 tok** (estimate) of a **16 000** budget — **1.4 % used, 0 dropped, 0 truncated** |
| **artifacts generated** | **1** candidate (`index.html`, 244 B, `complete`, `committed`) |
| **artifacts mutated** | **1** (`FilesMutated: 1`) |
| **diff** | `present=true +8 −8` — measured from the runtime's own compiled unified diff, consistent with the on-disk delta |
| **verification** | `passed=true` (gate executed **inside** the apply boundary) |
| **evidence** | `COMMITTED`, untainted, authoritative |
| **final state** | **`PhaseCompleted` / `changed`** |

### 11.3 Acceptance properties asserted

| Property | Assertion |
| --- | --- |
| **No false completion** | Removing only the evidence event flips the state to `unsubstantiated` with the reason named. The golden run's success rests on the evidence, not on the provider having returned. |
| **No duplicate model state** | Dock text is empty while the panel owns the step; the stage never re-words it; the EXECUTING header agrees. |
| **No fake diff** | The ledger prints the boundary's own `+84 −12` / `+156 −31`; blank where no diff exists; `+0 −0` unreachable. |
| **No mutation before authorization** | Workspace byte-identical; **zero** `mutation.started` / `mutation.completed` / `verification.completed`; no stray file written. |
| **No mutation outside scope** | `styles.css` and `script.js` byte-identical after apply. |
| **No lost partial artifact** | Candidate recorded *before* the gate; the held patch carries the full resolved content. |
| **No CREATE → PATCH contract corruption** | The diff is a plain single-file unified diff; no foreign contract marker; the base is the file as it exists. |
| **No unnecessary context pruning** | 224 / 16 000 tokens, 0 dropped, 0 truncated; `PromptFingerprint` present as the cache key. |

---

## 12. Remaining non-blocking residue

| # | Residue | Assessment |
| --- | --- | --- |
| 1 | `TraceAnchorText()` has no production caller — the "▸ Trace (N steps) · Alt+T" anchor is defined and tested but never rendered. | Discoverability gap, not correctness. The overlay is reachable via `Alt+T`. *Not touched: unrelated to this mandate.* |
| 2 | Competing mutation writers remain in the tree (`ToolCallBuffer.ApplyApproved`, `Substrate.ExecuteUnit`, `runtime/executor/file_executor.go`, `scope.go`, `output/tee.go`, `execution/boundary.go`). | Unreachable from `$prompt` today, but a latent hazard. *Deliberately not removed — §"modify unrelated subsystems".* |
| 3 | The DEBUG stream (`Ctrl+O`×2) lists every event, so it is a wall of plumbing. | That is the intended diagnostic layer. Acceptable for a debug surface. |
| 4 | The skeleton and the activity-tree badges animate on predicates independent of `loadingDockActive()`. | They are chrome/skeleton in different regions, not restatements of the execution step — so not duplicate *claims*. A future consolidation of the busy predicates would be a fair cleanup. |
| 5 | `ExecutionFinishedPayload.Success` remains a coarse boolean. The projection gates on evidence, so the UI is correct — but a future consumer reading `Success` directly would repeat D3. | *Mitigation in place:* the architecture lock forbids a raw `CompleteExecution` call site. A follow-up could carry the gate verdict onto the payload. |
| 6 | `ContextPreparedPayload.Channels` is empty for a `targeted_mutation` profile with `Policy: none` — the compiler produced 224 tokens but declared no named channels. | Truthful. The golden test asserts the **truth** (`Policy` reported, channels reported-as-empty) rather than inventing a channel name. |

---

## 13. Final verdict

**Complete. All acceptance criteria satisfied. All verification gates green.**

```
go build ./...                OK
gofmt -l ./cmd ./internal     clean
go vet ./...                  OK
go test ./...                 197/197 tested packages pass (29 have no test files)
go test -race -count=1 ./...  pass, no data races
```

The runtime was already doing the hard part correctly. This phase made the interface **stop lying about it** — and, in one case (`recordMutationEvidence`), made the runtime stop lying to itself.

The three repairs that matter:

1. **An action is rendered only when its object exists.** `authorizationActions()` is one derived list feeding both the labels and the cursor, so the two cannot disagree. The defect class is now structurally impossible, not merely fixed.

2. **Completion is a verdict on evidence, not a boolean on a return.** The evidence already existed and already refused what the bus event claimed. Publishing it *before* `execution.finished` made the refusal reachable, and one new terminal state gives the refusal a truthful name instead of a fabricated success.

3. **One owner per state.** The panel owns the step; the dock owns the glyph; the header echoes the panel; the stage returns only when nothing else is mounted.

What was deliberately **not** done: no new executor, scheduler, state machine, context compiler or cache; no context pruning; no chain-of-thought; no fabricated progress; no fabricated diff; no broad refactor; no unrelated subsystem touched.

The result is quiet, precise, alive and evidence-driven. During a real invocation the user now sees the artifact being worked on, the files that actually changed with their measured line counts, the verification verdict, and — if anything is unproven — an explicit **"Not completed"** with the reason, instead of a spinner and then a green checkmark.

> *The objective is not to make Izen look more like an autonomous agent. The objective is to make the actual Izen runtime execution visible, truthful, efficient, and understandable without exposing unnecessary internal machinery.*

---

## Appendix A — File inventory

### Production code changed (14: 13 modified + 1 new)

| File | Change |
| --- | --- |
| `internal/events/events.go` | `MutationCompletedPayload` += real apply-boundary evidence; `NewMutationCompletedWithEvidence`; `events.MutationEvidence` transport projection |
| `internal/execution/graph/graph.go` | `CompleteMutationWithEvidence` — emits the evidence-backed `mutation.completed` |
| `internal/execution/mutationset.go` | `EvidenceFor` (the only accessor for diff metrics), `Count` |
| `internal/execution/patch.go` | `SetCompiledDiffs` / `compiledDiffFor`; `recordMutationEvidence` measures the runtime's compiled diff, not the artifact |
| `internal/execution/executor.go` | `completeExecution` choke point (seal → evidence → completion); `publishMutationOutcome`; `compiledDiffsByTarget`; 5 terminal-success sites rerouted |
| `internal/presentation/completion_gate.go` | **NEW** — `CompletionGate`, `CompletionVerdict` |
| `internal/presentation/layers.go` | `TargetEvidence`; `ExecutionDetails` += candidate/mutation/verification/evidence/provider fields; `BytesChanged` |
| `internal/presentation/execution_projection.go` | evidence gate accumulation; artifact ledger; `PhaseUnsubstantiated`; `terminalState`; `terminalSentence`; `targetSlot` / `recountTargets` |
| `internal/presentation/narrative.go` | `RewriteHuman` seam; `provisionalFinishedSentence` |
| `internal/ui/autonomy_proposal.go` | `authorizationCandidate` / `authorizationActions` / `hasAuthorizationAction`; EXECUTION AUTHORIZATION card; derived action line |
| `internal/ui/keys.go` | `I` binding gated on the action existing |
| `internal/ui/loading.go` | single-owner dock; artifact ledger renderers; layer-labelled details |
| `internal/ui/topbar.go` | header titles from the canonical step |
| `internal/ui/model.go` | layer-labelled context line; stream/phase telemetry → Trace |

### Tests added (8)

`internal/execution/phase13_golden_portfolio_test.go` · `internal/presentation/completion_gate_test.go` · `internal/presentation/artifact_evidence_test.go` · `internal/presentation/artifact_evidence_events_test.go` · `internal/presentation/phase13_golden_projection_test.go` · `internal/ui/phase13_authorization_ux_test.go` · `internal/ui/phase13_convergence_test.go` · `internal/contextcompiler/phase13_cache_matrix_test.go`

### Tests corrected (11)

`internal/architecture/phase2_identity_evidence_lock_test.go` (strengthened lock) · `internal/execution/execution_phase4_test.go` (`firstPayload` helper) · `internal/presentation/execution_projection_test.go` · `internal/presentation/narrative_test.go` · `internal/ui/autonomy_execution_test.go` · `internal/ui/autonomy_route_test.go` · `internal/ui/domain_events_test.go` · `internal/ui/execution_projection_ui_test.go` · `internal/ui/human_presentation_test.go` · `internal/ui/phase12_execution_narrative_test.go` · `internal/ui/refactor_tui_engine_test.go`
