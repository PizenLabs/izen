# EXECUTION FORENSICS

**Status:** R1 **PROVEN** by live execution — instrumented, benchmarked, root-caused, closed
**R2:** **BLOCKED** — a real-agentic-repair benchmark over the real local model. Discovery runs and finds the file; the runtime then refuses to bind a target it was never given and correctly parks. One independent defect on the path (a repair objective routed to zero-context casual chat) was found and fixed. Full evidence in [§R2 REAL AGENTIC REPAIR](#r2-real-agentic-repair).
**Scope:** `$prompt` execution, runtime loop, continuation, output budgets, evidence, verification, completion, multi-step discovery
**Branch:** `fix/runtime` (base `f40f369`)
**Full repository suite:** green (`go test ./...`); `-race` green on execution, events, forensics, architecture, runtime, ui

---

## CURRENT STATUS

The `$prompt` execution path is instrumented end to end, and the one defect that
had real execution consequences is now **proven fixed by a real execution**, not by
reading the fix.

Seven defects were found. Three were observability defects that made execution
undiagnosable; one is a **provenance propagation defect whose effect on real
execution has now been measured**; two more were found *by* that measurement —
a structurally dead pattern detector and a conflated verification state; one is a
pre-existing broken test that disabled an entire test package.

**The headline finding is that the runtime executes, verifies, and refuses
truthfully.** The `$prompt` complaint is not "the runtime cannot execute". It is
that the run cannot be *observed*, and — for one specific class of objective —
that the authorizing directive never reaches the component that needs it.

### R1 STATUS: PROVEN

The chain

```
$prompt
→ ScopeDynamic
→ LoopRequest.Scope = ScopeDynamic
→ Execute capability granted
→ runtime actually executes
→ mutation actually occurs
→ verification actually runs
→ behavior is satisfied
→ ObjectiveState = PROVEN
→ execution finishes normally
```

is now demonstrated end to end against a **real local model** over the **real
production composition**. Full evidence in [§R1 PROOF](#r1-proof--live-evidence).

---

## KNOWN FACTS

### The execution path (audited, §23)

```
submitEnter
→ intent_dispatch.go:287  routePromptDirective      (authoritative admission)
→ intent_dispatch.go:301  bindScopeProvenance       ($prompt → ScopeDynamic)
→ autonomous.go:43        executeAutonomyViaDriver
→ autonomous.go:120       runAutonomousDriver       ← SetScope added here
→ runtime/autonomy/driver.go:523  Driver.Run         ← the loop owner
     → driver.go:626  bindAuthoritativeTargets       (scope derivation)
     → driver.go:662  LoopRequest                   ← Scope added here
     → driver.go:733  observeAndRun                 ← the loop
          → driver.go:2037  frozenSpec → EvaluatePreflightAdmission  (authorization gate)
          → driver.go:2099  decideWithObjective      (decision matrix — PROPOSES)
          → driver.go:2116  authorizeObjectiveCompletion   (rewrites)
          → driver.go:2127  routeObjectiveContinuation      (rewrites)
          → driver.go:2141  authorizeBehavioralCompletion   (rewrites)
          → driver.go:2148  authorizeContractRecovery        (rewrites)
          → driver.go:2196  adapter.Execute         ← the only execution port
     → executor.go:3287  effectiveMaxOutput → llmstep.ResolveMaxTokens  (budget)
     → executor.go:3964  providerExecutionPayload  ← provider record
     → patch.go:897     verifier.RunAllFor        (verification)
     → objective_completion.go:190  ObjectiveCompletionAuthority (disposes)
```

### Ownership (the ten questions of §23)

| # | Concern | Authoritative owner | File |
|---|---|---|---|
| 1 | Loop owner | `Driver.observeAndRun` | `internal/runtime/autonomy/driver.go:1965` |
| 2 | Continuation decision | `decideWithObjective` → `autonomy.RecoverFailure`, then 4 authorities | `driver.go:2099` |
| 3 | Model call | `RuntimeExecutor.invokeMutation` / `invokeReadOnly` | `internal/execution/executor.go` |
| 4 | Token budget | `strategy.outputForArtifact` → `effectiveMaxOutput` → `llmstep.ResolveMaxTokens` | `selector.go:798`, `executor.go:3287`, `llmstep/step.go:68` |
| 5 | Finish reason | provider → `ai.ResponseMetadata` → `providerExecutionPayload` | `executor.go:3992` |
| 6 | Execution step | `RuntimeLoop.ConsumeExecution` | `internal/autonomy/runtime_loop.go:998` |
| 7 | Evidence | `MutationSet` → `ExecutionEvidence` | `internal/execution/evidence.go` |
| 8 | Verification | `PatchManager` → `Verifier.RunAllFor` | `internal/execution/patch.go:897` |
| 9 | Completion | `ObjectiveCompletionAuthority` | `internal/execution/objective_authority.go` |
| 10 | TUI events | `events.Bus` → `internal/ui` subscribers | `internal/events/` |

**There is exactly one loop owner.** No competing loop owners were found. The four
authorities are a sequential gate chain over one proposal, not four loops.

### The output budget is derived, not fixed (§5, §14)

`max_tokens` is **not** a constant. Measured from real runs:

| Artifact shape | Requested budget | Source |
|---|---:|---|
| `replace_block` | **1024** | `outputForArtifact` fixed bound |
| `create_file` (medium) | **4096** | `CreationTokenTiers[ComplexityMedium]` |
| `plan` | 1536 | `outputForArtifact` |
| `investigation` | 2048 | `outputForArtifact` |

Then `llmstep.ResolveMaxTokens(model, requested)` clamps against the model's real
ceiling: free-tier (`:free`) and any model advertising `max_output ≤ 1024` are
forced to `min(requested, 980)`. The configured `ai.max_tokens: 4096` is therefore
a **profile default that is almost never the value sent**.

Before this investigation, `ProviderExecutionPayload` recorded the tokens the
provider *reported* but not the budget *requested*, and recorded no effective
ceiling at all. A `finish_reason=length` was unattributable.

---

## TESTS RUN

`test/forensics/` — 27 tests, all passing. Every test prints its full forensic
trace.

| Test | What it pins |
|---|---|
| `TestBenchmarkA_SingleModelResponse` | clean lifecycle, 1 call, no patterns |
| `TestBenchmarkB_OutputExhaustion` | §5: exhaustion observable; requested vs effective budget |
| `TestBenchmarkC_Continuation` | §6: continuation changes the request |
| `TestBenchmarkD_NoOpCompletion` | negative: a read-only objective invents no mutation |
| `TestBenchmarkE_MutationAndVerification` | real verification, rollback on failure |
| `TestBenchmarkF_FailureRecovery` | bounded failure; never PROVEN; workspace intact |
| `TestForensics_TraceSurvivesTheProcess` | §9: post-mortem reconstruction from disk |
| `TestForensics_ReadNDJSONSkipsATruncatedTail` | a crashed writer's log is still readable |
| `TestForensics_ReadNDJSONReportsAMissingRun` | fails loudly rather than returning empty |
| `TestForensics_ReadNDJSONDrainsAcrossALiveProcess` | live and disk readers agree |
| `TestForensics_AuthorizationRefusalIsPublished` | refusal is as observable as a grant |
| `TestForensics_SummaryIsPublishedOnEveryTerminalPath` | summary on refusals too |
| `TestForensics_ContinuationDecisionPairsProposalWithSelection` | proposed vs selected |
| `TestForensics_ScopeProvenanceReachesTheRuntime` | the mode label, pinned to the canonical vocabulary |
| `TestForensics_TraceRefusesAMultiRunStream` | refuses to fabricate an ordering |
| **`TestR1_ProductionPromptScopeGrantsExecuteAndTheRuntimeExecutes`** | **the R1 regression** — real driver, real behavioral gate, capability actually EXECUTED |
| **`TestR1_WithoutTheDirectiveTheGateCannotObserve`** | the control arm: read-only is refused, so the scoped arm proves something |
| `TestR1_ScopeProvenanceDrivesTheBehavioralCapabilityVector` | the mapping the regression depends on |
| **`TestVerificationStatesAreDistinguishable`** | all four terminal verification transitions are distinct |
| `TestVerificationEntryIsPublishedSeparately` | gate entry is its own event, so ordering survives |
| **`TestForensicReaderRendersEveryVerificationState`** | the reader names all six states |
| **`TestForensicReaderNeverAccusesFromSilence`** | absence of a record never produces an accusation |
| `TestForensicReaderAccusesOnlyOnObservedIncompleteness` | …but an entered gate with no verdict does |
| **`TestCompletionWithoutEvidenceDetectorIsAlive`** | the dead detector can fire again |
| `TestBehavioralRecordSeparatesGrantedFromExecuted` | a permission is never rendered as an observation |
| **`TestIsCasualChat_CodingTasks`** (R2) | **a repair objective that writes the value `"Hello"` is not casual chat** |
| **`TestSelect_RepairObjectiveWithGreetingValueIsNotCasual`** (R2) | **the R2 objective carries mutation semantics, not direct response** |
| **`TestR2_TargetlessRepairObservesButDoesNotDispatch`** (R2) | **deterministic: targetless repair discovers its candidate, binds nothing, dispatches nothing, mutates nothing, parks** |

`internal/execution/verification_publish_test.go` — pins the executor seam that
routes a real `VerificationReport` to its own graph transition, and that an
absent report publishes nothing rather than inventing a verdict.

`test/live_r1/` — the live experiment. Opt-in via `IZEN_LIVE_FORENSICS=1`; it
needs a local model server and is deliberately **not** part of `go test ./...`,
which must stay hermetic. The deterministic proof of R1 lives in
`test/forensics` and runs always; this package is the live confirmation.

`test/live_r2/` — the R2 real-agentic-repair experiment, also opt-in via
`IZEN_LIVE_FORENSICS=1`. It carries the benchmark measurement
(`TestLiveR2_Observation`), the downstream-chain diagnostic
(`TestLiveR2_DiagnosticDeclaredKind`, PROVEN), and the acceptance test
(`TestLiveR2_AcceptanceChain`, currently FAIL by design — R2 is BLOCKED at
`discovery→inspection`).

### Test matrix (§17) — populated from actual runs, not expectations

| Test | Budget requested | Calls | Continue | Mutation | Verify | Final state |
|---|---:|---:|---|---|---|---|
| A exact response | 1024 | 1 | 0 | yes (after approve) | not_applicable | `completed` |
| B exhaustion | 1024, 1024 | 2 | 1 | none | n/a | `awaiting_human` |
| C continuation | 1024 | 1 | 0 | none | n/a | `awaiting_human` |
| D no-op | 1024 | 1 | 0 | **none** | n/a | `completed` |
| E create file | **4096** | 2 | 1 | applied → **rolled back** | **failed (0/1)** | `aborted` |
| F failure | 1024, 1024 | 2 | 1 | **none** | n/a | `awaiting_human` |
| **LIVE R1 (`$prompt`)** | **512, 1024** | **2** | **1** | **applied, `fs_changed=true`** | **started → not_applicable** | **`completed`** |
| **LIVE R1 control (withheld)** | 512, 1024 | 2 | 1 | applied | not_applicable | **`awaiting_human`** (behavioral refusal) |
| **LIVE R2 benchmark (no filename)** | 512 | 1 (requirement pass only) | 0 | **none** | not entered | **`awaiting_human`** (disambiguate) — **BLOCKED** |
| **LIVE R2 diagnostic (declares HTML)** | 512, 1024 | 2 | 1 | **applied, `fs_changed=true`, `+1/-1`** | **started → not_applicable** | **`completed`** |

Benchmark B's continuation line, verbatim:

```
CONTINUATION OBSERVED: call #2 re-issued the request (identical prompt: false, budget 1024)
```

**This is the §6 answer.** On `finish_reason=length` IZEN makes a second call
with a **different prompt** and the **same budget**. It is a real continuation,
not a blind replay — the prompt changed — and it does not inflate the budget,
because the ceiling belongs to the model. `NON_PROGRESSING_CONTINUATION` did not
fire.

---

## OBSERVED TRACE

A real `change bar to qux @note.txt` run, reconstructed:

```text
EXECUTION
────────────────────────────────────────────────────────────────────────
run_id:  run-1
objective:
  change bar to qux @note.txt

AUTHORIZATION
  verdict:   allow
  granted:   true  blocked: false
  authority: preflight_admission_gate
  intent:    MUTATE   scope: RESOLVED   mode: $prompt
  targets:   note.txt
  reason:    admitted: proven target, admissible mutation boundary, authoritative evidence

EXECUTION SPEC (frozen before dispatch)
  intent:        MUTATE
  strategy:      targeted_mutation
  contract:      agentic_loop  ceiling: execute
  targets:       note.txt
  boundary:      BOUND   evidence: PRODUCED
  channels:      target:note.txt
  scope:         RESOLVED (the strategy gateway resolved this request's target set…)
  derivation:    (none) kinds=(none)
  req_budget:    1024
  digest:        465c4ed9e46f732de0dce20777f1068e86f7c252e45df9a6a93cbe87148281ed

MODEL CALLS
  CALL #1
    provider:    scripted / qwen2.5-coder:7b
    requested:   1024 output tokens
    effective:   unobserved (no truncation observed)
    prompt:      1800 tokens / 1547 chars  fp=e906a811967b
    completion:  260 tokens
    finish:      stop   truncated: false

CONTINUATION DECISIONS
  step 1  deciding → executing
    proposed:   continue — objective resolved — execute
    selected:   continue — objective resolved — execute
  step 2  interpreting → awaiting_human
    proposed:   ask_human — mutation awaiting approval
    selected:   ask_human — mutation awaiting approval
  step 3  deciding → completed
    proposed:   complete — objective satisfied: changed
    selected:   complete — objective satisfied: changed; objective PROVEN by evidence
    REWRITTEN by: objective_completion_authority

OBJECTIVE EVALUATIONS
  PROVEN             granted=true mutations=1 verified=false
      clause: (none)
      reason: (none)

EVIDENCE
  mutation: target=note.txt outcome=changed artifact=true diff=false apply=true fs_changed=true
  verification: NOT APPLICABLE — no verification configured for language

LOOP TRANSITIONS
  idle         → observing    continue      user objective: change bar to qux @note.txt
  observing    → deciding     continue      observation consumed
  deciding     → executing    continue      objective resolved — execute
  executing    → verifying    continue      execution consumed: pending_approval
  verifying    → interpreting continue      verification consumed
  interpreting → awaiting_human ask_human     mutation awaiting approval
  awaiting_human → observing    continue      patch approved
  observing    → deciding     continue      observation consumed
  deciding     → completed    complete      objective satisfied: changed; objective PROVEN by evidence

EXECUTION SUMMARY
────────────────────────────────────────────────────────────────────────
status: completed
note:   revision 2 — the run parked earlier and this is its resumed outcome
model_calls:   1 (failures 0, output_exhausted 0)
runtime_steps: 1
continuations: 0
tokens:
  input:                   1800
  output:                  260
  total:                   2060  (authoritative=true)
budgets:
  call #1 requested=1024 effective=unobserved
execution:
  mutations:     1
  verification:  not_applicable
termination:
  state:  completed
  reason: objective satisfied: changed; objective PROVEN by evidence
patterns:
  none detected
```

Note `step 3`: the matrix proposed `complete` with reason *"objective satisfied:
changed"*, and the completion authority appended *"objective PROVEN by
evidence"*. Both render as one `loop.transition`. Only the forensic record
distinguishes them — which is why `Rewritten` and `Authorities` exist.

---

## ROOT CAUSE

Seven defects, in order of consequence. R6 and R7 were found by the live R1
experiment itself, not by reading the code — both had predicates that read
correctly and behaved wrongly only on real runs.

### R1 — The authorizing directive never reached the runtime (execution defect)

**Owner:** the propagation seam between `internal/ui` and the Driver's
`LoopRequest`.

`internal/ui/intent_dispatch.go:301` binds `$prompt` → `ScopeDynamic`, and
`internal/runtime/autonomy/driver.go:662` is the only production construction of
`autonomy.LoopRequest` — and it never set `Scope`. Therefore
`d.scopeProvenance()` returned `ScopeNone` on **every** production `$prompt` run.

`scopeProvenance()` is read by exactly one consumer:
`authorizeBehavioralCompletion` → `BehaviorStage.Stage` →
`execution.GrantFor(provenance, caps)`. And `GrantFor`'s default branch is:

```go
default:
    grant.Read = readable   // Execute: NO   Network: NO
```

`ScopeNone` therefore granted **Read only** — no Execute, no Network — to the
behavioral completion gate on every `$prompt` run. The behavioral runtime's job
is to serve the workspace and probe it; it needs Execute to start a process and
Network to probe. Per `GrantFor`'s own comment: *"a scope that may not start a
process cannot reach one, so granting probe without it would produce a capability
that can only ever report OBSERVATION_FAILED."*

The gate runs whenever `BehaviorRequired(objective)` is true, and that predicate
matches **"fix", "repair", "debug", "correct", "valid", "verify", "work", "run",
"renders", "display", "load", "execute", "test", "break"**.

So for any objective phrased as a fix or a verification, the gate was structurally
incapable of proving the result, and `authorizeBehavioralCompletion` downgraded the
completion to `BEHAVIORALLY UNPROVEN`. **That is the "$prompt plans and stops"
symptom**, and it was not a planning failure at all — the mutation and the
evidence were fine; the authority that certifies the outcome was running with the
wrong grant.

Notably, `WithSubcommand("$prompt")` already existed and was **never wired in
production** — the value was available at the composition root and simply unused.

### R2 — Verification could not be distinguished from verification-never-happened (observability)

**Owner:** `internal/execution/graph/graph.go` and
`internal/execution/executor.go`.

Two independent defects, both about the same ambiguity:

1. `Graph.CompleteVerification` published `execution.verification.completed`;
   `Graph.Skip(StageVerification, …)` published **nothing**. The stream could
   express *passed* and *failed* but not *skipped*.
2. `executor.go` routed BOTH "the gate was consulted and no contract exists for
   this language" (`VerificationReport.Skipped`) AND "the boundary was never
   reached" through the same `g.Skip(StageVerification, …)` call.

So four real states collapsed into two. This is why the first version of the
pattern detector reported a **false `UNVERIFIED_MUTATION` on a perfectly correct
text-file edit**. A forensic tool that manufactures accusations from its own
coverage gaps is worse than no tool.

### R6 — `COMPLETION_WITHOUT_EVIDENCE` could never fire (found by the live run)

**Owner:** `internal/forensics/trace.go`.

The detector compared the objective state against the literal `"proven"`. The
runtime's canonical value is `execution.ObjectiveProven` = **`"PROVEN"`**. The
comparison therefore never matched, and the pattern was **structurally dead**: it
rendered "no patterns detected" on exactly the run it exists to catch.

This was found only because the live R1 experiment produced a genuine `PROVEN`
record and the reader still reported no patterns. Reading the code would not have
found it — the predicate looks correct. It is now compared against
`execution.ObjectiveProven.String()`, and
`TestCompletionWithoutEvidenceDetectorIsAlive` fails if it ever dies again.

**A detector that silently cannot fire is worse than no detector**, because the
absence of a flag reads as an all-clear.

### R7 — The reader accused the runtime from missing evidence (found by the live run)

**Owner:** `internal/forensics/trace.go`.

`UNVERIFIED_MUTATION` fired when a mutation reported `apply_executed +
fs_changed` and the trace held **no verification record at all**. That is an
accusation manufactured from an absence: the reader has no observation either way,
and "the record does not say" is not "verification did not happen".

It now fires only on a **positively observed** hole — the trace shows the gate was
ENTERED and never concluded. A run whose verification is `NOT_APPLICABLE` or
`SKIPPED` has stated what verification was possible; a run with no record has
stated nothing, and the reader says `UNKNOWN` instead of inventing the statement.

`PREMATURE_TERMINATION` had the same shape and lost its verification clause for
the same reason: it now rests on three positive observations (completed, zero
provider calls, zero mutations) rather than four, one of which was an absence.

### R3 — Requested and effective output budgets were unrecorded (§5)

**Owner:** `events.ProviderExecutionPayload`.

The record carried `CompletionTokens` and `FinishReason` but not the budget the
runtime *asked for*, and nothing about the ceiling the provider *allowed*. A
`finish_reason=length` could not be attributed to a budget mismatch because the
budget was not in the record.

### R4 — The control plane was invisible (§1 Q1, Q2, Q9, Q15, Q16)

**Owner:** `internal/events`.

Four of the brief's questions had no event at all. `loop.transition` carries an
action and a reason string, which is why the gap looked absent from the outside:
a decision the matrix proposed and an authority rewrote both render as one
transition. `execution.authorized`, `execution.spec.frozen`,
`continuation.evaluated` / `.selected` and `objective.evaluated` did not exist.

### R5 — A reader that races its own publisher (tooling defect, found by using it)

The first working reader silently dropped the tail of every run: the bus delivers
on its own goroutine, and cancelling the subscription stops it immediately. The
tail is exactly what decides the verdict — the final `continuation.selected`, the
`objective.evaluated` that granted completion, the verification record, the run
summary. The symptom was a clean run flagged `UNVERIFIED_MUTATION` and
`COMPLETION_WITHOUT_EVIDENCE`. Draining is now part of the reader's contract.

---

## FIX APPLIED

All fixes are additive at the identified owner. No refactor, no new authority.

| # | File | Change |
|---|---|---|
| R1 | `internal/runtime/autonomy/driver.go` | `scope` field, `WithScope`, `SetScope`; `LoopRequest.Scope` populated at the single production construction |
| R1 | `internal/ui/autonomous.go` | `SetScope` on the narrow driver interface; pushed from `runAutonomousDriver` |
| R1 | `internal/ui/intent_dispatch.go` | `scopeProvenanceDirective()` renders the bound directive |
| R1 | `internal/ui/autonomous_test.go` | fake records the directive so a test can assert it arrived |
| R1 | `test/forensics/r1_scope_regression_test.go` | **new** — the production-path regression + its control arm |
| R1 | `test/live_r1/` | **new** — the opt-in live experiment, real model, real composition |
| R2 | `internal/execution/graph/graph.go` | `Skip(StageVerification)` publishes SKIPPED; `NotApplicableVerification` and `BeginVerification` are their own transitions |
| R2 | `internal/execution/executor.go` | `publishVerification` routes a real `VerificationReport` to the correct transition |
| R2 | `internal/events/events.go` | `VerificationCompletedPayload.Outcome` + `NewVerificationNotApplicable` + `NewVerificationStarted` on its own event type |
| R2 | `internal/architecture/execution_invariants_test.go` | all four verification constructors pinned to the graph |
| R3 | `internal/events/events.go` | `ProviderExecutionPayload.RequestedOutputTokens` / `.EffectiveOutputTokens` / `.EffectiveOutputKnown` |
| R3 | `internal/execution/executor.go` | populated from `req.MaxTokens` and the provider-reported count at truncation |
| R4 | `internal/events/events.go` | 6 event types + 6 typed payloads + constructors |
| R4 | `internal/events/bus.go` | the 6 are CONTROL events (guaranteed delivery — low frequency, load-bearing) |
| R4 | `internal/runtime/autonomy/forensics.go` | **new** — the emitters; no decision authority |
| R4 | `internal/runtime/autonomy/driver.go` | emitters wired at the 4 real seams |
| R4 | `internal/runtime/autonomy/objective_completion.go` | `objective.evaluated` at the gate |
| R4/R1 | `internal/events/events.go` | `execution.behavior.observed` + `BehaviorObservedPayload` — the capability grant AND the capabilities actually executed |
| R4/R1 | `internal/runtime/autonomy/behavior.go` | emits the pass's own evidence; refusals excluded from `Executed` |
| R6 | `internal/forensics/trace.go` | `COMPLETION_WITHOUT_EVIDENCE` compares `execution.ObjectiveProven`, not a re-spelling |
| R7 | `internal/forensics/trace.go` | `UNVERIFIED_MUTATION` fires only on observed incompleteness; `UNKNOWN` is a real answer |
| R7 | `internal/forensics/trace.go` | `VerificationState()` exported; per-request then per-run reduction |
| — | `internal/forensics/trace.go` | **new** — reconstruction, rendering, 6 pattern detectors |
| — | `internal/forensics/ndjson.go` | **new** — post-mortem reconstruction from disk |
| R5 | `internal/execution/execution_test.go` | pre-existing: `internal/context` shadowed stdlib `context`, breaking the whole package's test binary |

### Design decisions worth recording

- **`settleDecision` is driven by the loop's own transition history**, not by the
  call site that stepped the loop. `publish` is the one place every *applied*
  decision passes through, including parks that bypass `Driver.step`
  (decomposition staging, continuation-library escalation, the admission gate).
- **An authority is recorded only when it CHANGED the decision.** An authority
  that ran, inspected and agreed has influenced nothing. The first version named
  all four on every decision; that was a fabricated causation and was removed.
- **A summary is emitted on every return path, not once per run.** A run that
  parks at approval publishes summary #1 for the park and #2 for the resumed
  outcome. A once-per-run guard would have reported the parked state as the run's
  result. `Revision` disambiguates; the last one is the outcome.
- **`rootRunID`** reduces `run-1-attempt-2` to `run-1`. A recovery attempt is a
  step inside the run's own loop, not a second run.
- **The verification vocabulary is the runtime's, not a new one.** The outcome
  labels project `execution.ObjectiveEvidence.VerifierVerdict()`'s existing
  PASS / NOT_APPLICABLE / FAIL / NOT_RUN set, plus `SKIPPED` and `STARTED` for
  the two boundary facts only the verification stage can observe.
- **`SKIPPED` and `NOT_APPLICABLE` are different transitions, not one flag.**
  `Graph.Skip` means the boundary was never crossed; `Graph.NotApplicableVerification`
  means the gate was consulted and reported no contract. They previously shared one
  call site, which is the defect.
- **The gate-entry record is its OWN event type**, not `verification.completed`.
  The canonical lifecycle orders completion strictly after `mutation.completed`
  (pinned by `TestTruthMatrix_CanonicalEventOrdering`), so an entry marker on the
  completion type breaks an invariant that exists so no stage reports completion
  ahead of the work it completes. Entry and completion are different facts.
- **The reader names all six states and treats `UNKNOWN` as an answer.** Absence
  of a verification record is rendered as `UNKNOWN`, never folded into a verdict
  and never used as grounds for an accusation.
- The reader **reports uninterpreted event types** rather than swallowing them. A
  reader that hides its own blind spots cannot be used to prove absence.
- **Capability executions are structured, not prose.** `Executed` is derived from
  the behavioral pass's own evidence, and a REFUSED capability is excluded — a
  naive derivation would list `runtime.serve` as "executed" for a pass that was
  refused at the authorization boundary and never started anything.

---

## REGRESSION TEST

`TestR1_ProductionPromptScopeGrantsExecuteAndTheRuntimeExecutes` is the R1
regression. It is written against the **production path**, not against a helper:

- the driver is the real `runtimeAutonomy.Driver`, constructed as `compose.Wire`
  constructs it — including `WithBehaviorProposer`, which is what makes the
  behavioral completion gate engage at all;
- the behavioral stage runs over a real `execution.BehavioralRuntime` against a
  real temp workspace with a real, servable HTML entry document;
- the objective is phrased so `BehaviorRequired` matches, so the gate actually
  runs;
- the scope reaches the driver through the **same push the TUI performs**
  (`SetScope`), and nothing else in the test supplies one.

**No test writes a Scope into a `LoopRequest`.** If `Driver.Run` stopped copying
the scope onto the request it builds, every assertion fails, because the gate
would derive a read-only grant and could not serve the workspace.

The assertions read the structured `execution.behavior.observed` record, never a
prose fragment of a decision reason — the reason is bounded and truncates exactly
the clause that proves the runtime served the workspace.

### The regression was verified to actually fail

Removing `Scope: d.scope` from `driver.go`'s single production `LoopRequest`
construction and re-running:

```
--- FAIL: TestR1_ProductionPromptScopeGrantsExecuteAndTheRuntimeExecutes
    authorization mode = "read_only", want $prompt — LoopRequest.Scope did not travel
    final state: awaiting_human
```

Restoring it returns the suite to green. The test is load-bearing.

`TestR1_WithoutTheDirectiveTheGateCannotObserve` is the **control arm**: the same
workspace, the same objective, the same provider, no directive. It asserts the
gate is refused at `AUTHORIZATION_BLOCKED` and never reports `PROVEN` — which is
what makes the scoped arm's success mean something rather than being an artefact
of the workspace or the model.

---

## R1 PROOF — LIVE EVIDENCE

**R1 STATUS: PROVEN**

Two independent experiments, both against the production composition
(`compose.Wire` → the real bounded `autonomy.Driver` → the real
`RuntimeExecutor`), both in `test/live_r1/`, both opt-in via
`IZEN_LIVE_FORENSICS=1`.

### Setup

| | |
|---|---|
| Model | **ollama / `qwen2.5-coder:7b`** — a real local model, not a fixture |
| Workspace | isolated `t.TempDir()`, git-initialised with one commit |
| Workspace content | `index.html` declaring a document element with a **wrong** greeting |
| Objective | `fix the greeting in @index.html so it displays hello and verify the page renders correctly` |
| Composition | `compose.Wire(WithRoot, WithConfig, WithProvider)` — the real driver, real executor, real policy engine, real behavioral stage |
| Approval | answered through the driver's own `ResumeApprove`, with authorization issued by the same `AuthorizationEngine` production uses |

The objective requires **both** a mutation and an observable result: "verify"
and "renders" match `BehaviorRequired`, so the run reaches the behavioral
completion gate — the gate whose grant R1 changed.

> Production preconditions the harness reproduces, because omitting them measures
> the harness rather than the runtime: a git repository (a checkpoint *is* a
> commit — without it the checkpoint-verification clause refuses every mutation),
> the session-start shadow checkpoint the TUI creates at init, and the
> Application-layer `SubmitPromptCmd` that dispatches `BackgroundPreflight`, the
> only writer of the barrier the driver waits on at `observing → deciding`.

### The recorded facts

| Field | Value |
|---|---|
| `execution_id` | `run-1` |
| intent | `MUTATE` |
| scope | `RESOLVED` (target `index.html`) |
| **scope provenance** | **`$prompt`** (`execution.authorized.mode`) |
| grant / capabilities (derived) | `workspace.discover, file.read, file.search, runtime.serve, runtime.fetch, runtime.inspect, command.run` |
| **capabilities ACTUALLY executed** | **`runtime.fetch, runtime.serve, workspace.discover`** |
| model | `ollama / qwen2.5-coder:7b` |
| requested output budget | call #1 `512`, call #2 `1024` |
| effective output budget | `unobserved` — no truncation observed, so no smaller ceiling was demonstrated |
| model calls | **2** (requirement pass + mutation), 0 failures, 0 exhausted |
| continuation decisions | 3 (execute → ask_human → complete); step 3 **REWRITTEN by** `objective_completion_authority, behavioral_completion_gate` |
| capability executions | discover → serve → fetch → stop, all with real evidence lines |
| mutation evidence | `target=index.html outcome=changed artifact=true apply=true fs_changed=true` |
| verification state | `STARTED` (gate entered) → **`NOT_APPLICABLE`** — "no verification configured for language html" |
| behavioural proof | **PROVEN**, 0 repairs, evidence: `workspace.discover: discovered 1 file(s) … entry=index.html \| runtime.serve: served … at http://127.0.0.1:<port> (entry=index.html, readiness confirmed) \| runtime.fetch: GET …/index.html -> HTTP 200 (147 bytes, 0ms) \| runtime.serve: stopped` |
| **objective state** | **`PROVEN`**, granted=true, mutations=1 |
| final termination reason | `completed` — `objective satisfied: changed; objective PROVEN by evidence; behavioral requirements PROVEN by runtime observation` |
| forensic patterns | **none detected** |

### Filesystem after execution

The mutation is read from disk, never inferred from model output:

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Greeting</title>
</head>
<body>
<h1 id="greeting">hello</h1>
</body>
</html>
```

`goodbye` → `hello`. The change is on disk.

### The control arm

The identical run with the directive withheld — the pre-fix condition:

| | `$prompt` (scoped) | withheld (control) |
|---|---|---|
| `authorization.mode` | `$prompt` | `read_only` |
| grant | all 7 capabilities | `workspace.discover, file.read, file.search` |
| **executed** | `runtime.fetch, runtime.serve, workspace.discover` | **`workspace.discover` only** |
| block | none | `AUTHORIZATION_BLOCKED — capability: not authorized: runtime.serve is not granted for this run` |
| behavioural verdict | **PROVEN** | **not proven** |
| final state | **`completed`** | **`awaiting_human`** |

Same workspace, same objective, same model. The **only** difference is whether
the authorizing directive reached the run — and that is the whole of R1.

### What the experiment also found

The live run is what surfaced **R6** (a structurally dead pattern detector) and
**R7** (a reader accusing the runtime from missing evidence). Neither was visible
by reading the code: R6's predicate *looks* correct, and R7's predicate fires
only on rare traces. Both were fixed and pinned.

---

## R2 REAL AGENTIC REPAIR

**R2 STATUS: BLOCKED.** The benchmark objective names no file, so the runtime
must *discover* its target. It does — bounded discovery observes `index.html` —
and then the authorization boundary refuses to bind a target it was never given,
parks for disambiguation, and no inspection, mutation, observation, verification
or objective evaluation occurs. The first incorrect transition on the way in (a
repair objective classified as casual chat) was found, fixed and pinned. The
**downstream** chain is independently PROVEN with the same real model when the
objective declares the artifact kind, which isolates the block to targetless
discovery rather than the loop.

### Setup

| | |
|---|---|
| Model | **ollama / `qwen2.5-coder:7b`** — the same real local model as R1 |
| Workspace | isolated `t.TempDir()`, git-initialised with one commit |
| Workspace content | `index.html` with an obviously wrong greeting: `<h1 id="greeting">Helo</h1>` |
| **Benchmark objective** | `inspect this project, find the incorrect greeting, fix it to "Hello", and verify the result.` — **names no file** |
| Composition | `compose.Wire(WithRoot, WithConfig, WithProvider)` — real driver, real executor, real policy engine, real behavioral stage |
| Directive | `driver.SetScope("$prompt")` — the exact production push |
| Harness | `test/live_r2/` (opt-in via `IZEN_LIVE_FORENSICS=1`) |

The harness deliberately does **not** answer a clarification/disambiguation
boundary. Answering with the filename would substitute human discovery for
runtime discovery, which is the exact confusion this experiment exists to avoid.
It answers only an approval boundary (a review of a produced candidate).

### Arm 1 — the benchmark (names no file): BLOCKED

The reconstructed trace is authoritative-runtime-events only:

```text
AUTHORIZATION
  verdict:   disambiguate
  granted:   false  blocked: true
  authority: preflight_admission_gate
  intent:    MUTATE   scope: UNRESOLVED   mode: $prompt
  targets:   (none)
  reason:    the target is unresolved and the workspace offers 1 candidate(s);
             the human must name the target before any provider call

EXECUTION SPEC (frozen before dispatch)
  intent:        MUTATE
  strategy:      multi_file_planning
  contract:      agentic_loop  ceiling: execute
  targets:       (none)
  boundary:      UNBOUND   evidence: (none)
  channels:      (none)
  scope:         UNRESOLVED (the objective declares no artifact kind …)
  derivation:    UNRESOLVED kinds=(none)
  req_budget:    1536

MODEL CALLS
  CALL #1  (the read-only requirement-derivation pass)
    provider:    ollama / qwen2.5-coder:7b
    requested:   512 output tokens
    effective:   unobserved (no truncation observed)
    prompt:      248 tokens / 1164 chars
    completion:  2 tokens / 2 chars
    finish:      stop   truncated: false

EVIDENCE
  mutations:     (none)
  verifications: UNKNOWN — the runtime published no verification record.

LOOP TRANSITIONS
  idle         → observing    continue   user objective: …
  observing    → deciding     continue   observation consumed
  deciding     → awaiting_human ask_human  the target is unresolved and the workspace offers 1 candidate(s) …

EXECUTION SUMMARY
  status:        awaiting_human
  model_calls:   0 (executor lane); 1 read-only requirement pass
  continuations: 0
  patterns:      none detected
```

The **only** model call is the requirement-derivation pass, and its prompt
explicitly tells the model `RESOLVED TARGETS: (none resolved yet — propose no
requirements)`. It receives no file bytes and returns no requirements
(`[objective] requirement derivation unavailable: … payload carried no
requirement text`). The workspace is byte-identical to the baseline.

### The fourteen questions, answered from the trace

| # | Question | Authoritative answer |
|---|---|---|
| 1 | Did the runtime discover the relevant file? | **YES.** Bounded discovery observed exactly one candidate, `index.html`, and the admission boundary carried it as the disambiguation option (`options=[index.html]`). The activity log records `[discovery] REQUIRED: operation=MODIFY scope=UNRESOLVED target=DEFERRED`. |
| 2 | Did it inspect the file? | **NO.** `channels: (none)` — no context channel was bound, so the model never received the file bytes. The requirement pass is told the resolved target list is empty. |
| 3 | Which model call produced the repair decision? | **NONE.** No repair decision exists. The single call was the read-only requirement pass; it produced 2 output tokens and no requirement. |
| 4 | What context did that call receive? | The requirement-pass system prompt plus `USER OBJECTIVE: <objective>` and `RESOLVED TARGETS: (none resolved yet — propose no requirements)`. 248 prompt tokens / 1164 chars. **No workspace context.** |
| 5 | Requested output budget? | **512** for call #1 (the requirement-pass ceiling). The frozen spec's mutation budget is 1536. |
| 6 | Effective output budget? | **Unobserved** — no truncation occurred, so no smaller ceiling was demonstrated. Provider reported 2 completion tokens. |
| 7 | Normal finish or `length`? | **`stop`**, `truncated=false`. |
| 8 | If `length`, what next? | **N/A** — no `length`. |
| 9 | Did the next step have materially new state/context? | **N/A** — there was no continuation. |
| 10 | Did the next step make progress? | **N/A** — no continuation. |
| 11 | Was the mutation applied? | **NO.** `mutations: 0`; `index.html` on disk still holds `Helo`. |
| 12 | Was the changed state observed? | **NO** — there is no changed state to observe. |
| 13 | Was verification entered? | **NO.** `verifications: UNKNOWN`; the gate was never entered. |
| 14 | Was the final objective PROVEN? | **NO.** No `objective.evaluated` was published. Final state `awaiting_human`. |

### Continuation analysis

There was **no continuation**. `continuations: 0`, one provider call, and the
loop moved `deciding → awaiting_human` and stopped. `NON_PROGRESSING_CONTINUATION`
did **not** fire, and correctly so: there is no repeated call to accuse.

### Token exhaustion

Not reached. Every live call in both arms reported `finish_reason=stop`,
`truncated=false`. No `length`, therefore no continuation, therefore no
`call #1`/`call #2` budget comparison. The effective budget remains
`unobserved` for the same reason as R1: no smaller ceiling was demonstrated.

### The first incorrect transition

The chain was walked transition by transition. The **first incorrect transition
on the benchmark path was the objective→capability classification**:

```
objective → authorization → discovery → inspection → model computation → decision → mutation → …
   OK           OK            OK          ✗
```

A repair objective was routed to **`direct_response`** — the zero-context casual
path — because the *value to write*, `"Hello"`, is also a
`casualGreetingPatterns` entry:

```
strategy = direct_response
reason   = "casual greeting / direct chat; answered directly, zero repository context"
```

The frozen spec recorded `intent: MUTATE` with `strategy: direct_response` — a
read-only strategy under a mutation intent. This is a **CAPABILITY_SELECTION_FAILURE**:
the runtime selected a conversational capability for a workspace repair.

Fix (one rule at the identified owner, `internal/gateway/chat.go`): a message
that contains an explicit workspace action (`fix`, `verify`, `change`, `inspect`,
…) is never casual. A greeting word inside an instruction is the value to write,
not small talk. Matching stays whole-word/whole-phrase, exactly like the existing
`fileRefIndicatorPatterns` and greeting tables, so `address` never matches `add`
and `created` never matches `create`.

After the fix the benchmark's spec is `strategy: multi_file_planning` (a
mutation-semantics proposal contract), not `direct_response`. **Re-running the
same benchmark produces the same block**, one transition later and unchanged in
cause: the runtime discovers `index.html` but the authorization boundary will not
bind a target the objective did not name.

### Why the benchmark stays BLOCKED

The blocking transition is `discovery → inspection`. Its cause is a deliberate,
test-pinned property of the runtime, not an incidental bug:

- discovery candidates are **evidence, never authority** (`I13`); and
- evidence-bound scope derivation binds only files that satisfy an artifact kind
  **the objective itself declared** (`internal/execution/derive.go`), and the
  benchmark declares none.

So the runtime is *structurally* correct to refuse: binding `index.html` because
a scan happened to return it would be exactly the "arbitrary file" hazard the
invariant forbids. Enabling targetless discovery to reach mutation means adding a
discovery→inspection→decision capability at the authorization boundary. That is a
redesign of the authorization architecture, which this experiment is explicitly
forbidden to undertake. Hence: **BLOCKED**, recorded, not worked around.

### Arm 2 — the diagnostic (declares the artifact kind): PROVEN

To separate "targetless discovery cannot resolve" from "the downstream chain is
broken", the same workspace and the same no-filename shape were run with an
objective that *declares the kind* — the one condition under which discovery can
bind without a human:

> `inspect this project, find the incorrect message in the HTML, fix it to "Welcome", and verify the result.`

Every stage then executes against the real model:

| Stage | Authoritative evidence |
|---|---|
| authorization | `allow`, `granted=true`, `intent=MUTATE`, `scope=RESOLVED`, `targets=[index.html]` |
| discovery (derivation) | `derivation: UNIQUE kinds=html` → `index.html` bound from OBSERVED files |
| inspection | spec `channels: [target:index.html]`; context compiled `policy=target_file_only`, `Truncated=false`; the mutation prompt is 419 tokens / 1767 chars (carries file bytes) |
| model computation | call #1 requirement pass (512 → 42 tokens, `stop`); call #2 mutation (1024 → 51 tokens, `stop`) |
| decision | 3 continuation decisions; step 3 `complete` **REWRITTEN by `objective_completion_authority`** |
| mutation | `target=index.html outcome=changed artifact=true apply_executed=true fs_changed=true diff=+1/-1` |
| observation | workspace read from disk: `<h1 id="greeting">Welcome</h1>` |
| verification | `STARTED` → `NOT_APPLICABLE` — "no verification configured for language html" |
| objective evaluation | `objective.evaluated: state=PROVEN granted=true mutations=1` (authority `objective_completion_authority`) |
| completion | summary `completed` — "objective satisfied: changed; objective PROVEN by evidence" |
| forensic patterns | none detected |

The mutation is confirmed on disk, never inferred from model text. The
diagnostic is retained as a live integration test
(`TestLiveR2_DiagnosticDeclaredKind`), and the benchmark is retained as a live
acceptance test (`TestLiveR2_AcceptanceChain`) that currently **fails** at
`discovery→inspection` — the honest red state of a BLOCKED result.

### Reproduce

```
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r2/ -v -timeout 1200s
# TestLiveR2_Observation            PASS   — the benchmark measurement (BLOCKED)
# TestLiveR2_DiagnosticDeclaredKind PASS   — the downstream chain (PROVEN)
# TestLiveR2_AcceptanceChain        FAIL   — R2 acceptance, blocked at discovery→inspection
```

The deterministic regression for the classification defect runs always:

```
go test ./internal/gateway/ ./internal/execution/strategy/
# TestIsCasualChat_CodingTasks                       (the repair-with-"Hello" cases)
# TestSelect_RepairObjectiveWithGreetingValueIsNotCasual
```

Both were verified to fail when the `chat.go` rule is reverted.

The deterministic pin of the BLOCKED boundary also runs always (no model):

```
go test ./internal/runtime/autonomy/ -run TestR2_TargetlessRepairObservesButDoesNotDispatch -v
# discovery observed index.html; 0 provider calls; 0 mutations; parked at clarify
```

---

## REMAINING UNKNOWN

1. **Benchmark C did not exercise the contract-recovery path.** The scripted
   prose answer was accepted by the artifact parser and reached an approval gate,
   so only one call was made. Whether `authorizeContractRecovery` produces a
   *useful* second prompt (rather than merely a bounded second call) is untested.

2. **Live-model measurement is one model, one run.** The live arm used
   `qwen2.5-coder:7b` on Ollama. Ollama's actual `finish_reason` vocabulary,
   free-tier ceilings and silent capping across providers remain unmeasured, and
   §14's per-model table is still only populated from scripted providers. Every
   live call in the evidence above reported `finish=stop` with no truncation, so
   the effective-budget question is still answered only on failure.

3. **No real TUI run.** The experiment drives `compose.Wire`'s real driver and
   pushes the directive through `SetScope` — the exact call `runAutonomousDriver`
   makes — but it does not go through Bubble Tea. `internal/ui` tests cover the
   routing, and `internal/ui/autonomous_test.go`'s fake records the directive so
   the push itself is asserted.

4. **`ProviderExecutionPayload.OutputChars` reads 0** for the mutation path
   (the manifest path populates it; `invokeMutation` does not). Cosmetic, but it
   means output *size* is unavailable for the mutation lane.

5. **`effective` is `unobserved` for every non-truncated call.** That is the
   honest reading — no smaller ceiling was demonstrated — but it means "effective
   budget" is only ever *proved* on a call that failed. A provider that caps
   without truncating is still undetectable.

6. **The behavioural repair path is still unmeasured against a real model.** The
   live run PROVED a clean observation with 0 repairs. The repair loop — a real
   model proposing a fix from a real observed defect — has only been exercised
   with a scripted proposer.

7. **Targetless discovery cannot reach mutation (R2's BLOCKER).** A mutating
   objective that names no file and declares no artifact kind observes its
   candidates and then correctly parks at disambiguation; the authorization
   boundary will not bind evidence as a target (I13), and the evidence-bound
   derivation binds only declared artifact kinds. This is a recorded,
   out-of-scope architectural boundary, not a bug fixed here. The R2 diagnostic
   shows the whole downstream chain is sound when the scope *can* resolve.

8. **The casual-chat classifier was over-eager (R2's fix).** A message that
   contained a greeting word anywhere — including inside a repair instruction's
   replacement value — was routed to the zero-context direct-response path. Fixed
   at `internal/gateway/chat.go` with a workspace-action guard, pinned by
   `TestIsCasualChat_CodingTasks` and
   `TestSelect_RepairObjectiveWithGreetingValueIsNotCasual`.

---

## WHAT WAS NOT TOUCHED

Per §11 and §21, none of these were modified, and the evidence did not
demonstrate a defect in any of them:

`runtime/kernel` · `internal/kernelbridge` · `internal/core/authorization` ·
`ContextSpec` / `ExecutionSpec` separation · `ObjectiveCompletionAuthority` ·
`PatchManager` · `MutationSet` · `internal/providers` transport · dynamic model
discovery · ohgo

R2 is the concrete application of that boundary: the benchmark's blocking
transition (a discovered candidate cannot be bound as a target for an objective
that never named one and declared no artifact kind) lives in the target-binding /
admission path, so it was **recorded as BLOCKED rather than redesigned**. The one
R2 change is outside it: the `IsCasualChat` classification guard.
