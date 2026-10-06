# R5 — NON-PROGRESS AND LOOP CONTROL FORENSICS

**Status: PROVEN with one precisely bounded open boundary.**

IZEN already owns a runtime-side definition of progress. It is not "the model
said something", not a token count and not "a capability ran". It is an
**observed, authoritative state transition** evaluated against the objective's
own completion conditions, plus a **failure fingerprint under an evidence
epoch**.

R5 traced the real call graph, pinned the owners, proved the five required
scenarios deterministically, captured the same facts live against
`ollama/qwen2.5-coder:7b`, and found **one missing transition** on the
successful-but-stalling path. That boundary is reported here and deliberately
**not implemented** — the surviving stop is bounded (no infinite loop), and R5
was instructed not to add a generic loop detector.

**Branch:** `fix/runtime` (continues from the recorded R4 PROVEN state).
**Read `EXECUTION_FORENSICS.md` / `EXECUTION_FORENSICS_STATE.md` first.** R1
PROVEN, R2 BLOCKED (recorded), R3 PROVEN, R4 PROVEN; R5 does not reopen any of
them.

Reproduce:

```
# deterministic semantic proof (no model)
go test ./internal/execution/         -run TestR5_ -v
go test ./internal/runtime/autonomy/  -run TestR5_ -v
go test ./internal/progress/          -run TestR5_ -v

# live boundedness benchmark (real local model, opt-in)
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r5/ -v -timeout 1500s
```

---

## The question

> What prevents a continuation-capable execution from repeatedly continuing
> without making meaningful progress?

R4 proved that continuation is runtime-owned and bounded. R5 separates the two
questions R4 deliberately left joined:

```
continuation budget    = a safety ceiling (how many times MAY we continue?)
progress detection     = a semantic contract (DID continuing make progress?)
```

---

## 1. What IZEN currently considers progress

The runtime distinguishes every candidate signal P0–P6. Only three of them are
authoritative; the rest are evidence, or noise.

| Signal | Meaning | Authoritative? | Where it is computed |
|---|---|---|---|
| **P0** | no observable change | **no** | absence of evidence → `ReduceProgress` = `REQUIRES_CONTINUATION`; failure ledger epoch unchanged |
| **P1** | model produced new output (tokens) | **no** | `provider.execution` / `reasoning.step.*`; parsed by the artifact gate, never counted as progress |
| **P2** | new context/evidence available | **yes (evidence)** | `ObjectiveEvidence.WorkspaceObservations`, `PostMutationObserved` → `OBSERVED` condition; `FailureLedger.AdvanceEvidence()` epoch |
| **P3** | a capability executed | **no, by itself** | only the evidence it produces counts; a zero-byte success satisfies nothing |
| **P4** | workspace/artifact state changed | **yes** | `ObjectiveEvidence.Mutated()` / `MutatedFiles` / `ObservedDeltaTargets` → `SCOPE_MUTATED` condition |
| **P5** | verification advanced | **yes** | `VerificationPassed`/`Skipped`/`Ran` → `INTEGRITY_HELD` with `GATE_PASS` / `GATE_NOT_APPLICABLE` |
| **P6** | objective state advanced | **yes (terminal)** | `ReduceProgress` → `PARTIALLY_SATISFIED` (advanceable) … `PROVEN` (complete) |

The decisive implementation is `execution.ReduceProgressWith`, which **recomputes
every completion condition from observed evidence** (`ReduceConditions`) and
never reads a caller-supplied status:

```
conditions satisfied = N of M  →  PARTIALLY_SATISFIED   (advanceable)
all conditions hold            →  PROVEN
truncated (partial artifact)   →  REQUIRES_CONTINUATION  (never PROVEN)
nothing holds, budget remains  →  REQUIRES_CONTINUATION
nothing holds, budget spent    →  UNSUBSTANTIATED
```

A model can emit 4 096 bytes of prose and satisfy zero conditions
(`TestR5_P1_ModelOutputAloneIsNeverProgress`). A capability can exit 0 and
satisfy zero conditions (`TestR5_CaseC_NoOpCapabilitySatisfiesNoCondition`).

---

## 2. Ownership map (traced through the real call graph)

No ownership below is inferred from a name; each row names the function that
actually performs the transition.

| Transition | Owner | Location |
|---|---|---|
| **state transition** | `autonomy.RuntimeLoop` (`Start`/`Observe`/`Step`/`ConsumeExecution`/`ConsumeVerification`/`applyDecision`/`terminate`) | `internal/autonomy/runtime_loop.go` |
| **artifact transition** | `RuntimeExecutor.invokeArtifactBoundedStep` (`ArtifactCandidate` pending/partial/complete) | `internal/execution/artifact_step.go` |
| **mutation transition** | execution mutation boundary → `ObjectiveEvidence.Mutation` / `MutatedFiles` / `ObservedDeltaTargets` | `internal/execution/{mutation,mutationset,patch}.go`; sealed onto the observation by the adapter |
| **evidence transition** | `Driver.bindStepEvidence` → `objective.discharged`, `objective.postMutation`; `FailureLedger.AdvanceEvidence` | `internal/runtime/autonomy/objective_lifecycle.go`; `failure_ledger.go` |
| **objective transition** | `execution.ReduceProgressWith` + `ObjectiveCompletionAuthority` (via `Driver.authorizeObjectiveCompletion`) | `internal/execution/objective_conditions.go`; `objective_authority.go`; `internal/runtime/autonomy/objective_completion.go` |
| **continuation eligibility** | `Driver.routeObjectiveContinuation` over `execution.DecideContinuation`; failure path `DecideRecoveryWith` + `FailureLedger`; exhaustion path `consultContinuationOnExhaustion` + `continuation.DeriveNextStep` | `objective_lifecycle.go`; `recovery_decision.go`; `recovery.go`; `driver.go:2695`; `internal/continuation/derive.go` |
| **stop / termination** | `autonomy.RuntimeLoop.violation()` bounds + `applyDecision` terminal actions; the completion authority may only *downgrade* a completion | `internal/autonomy/runtime_loop.go`; `driver.go:3124` `decideDefault` |

### The one control plane, end to end

```
observe → decide (decideWithObjective)
            ├─ success shape        → decideDefault → LoopComplete
            │     └─ authorizeObjectiveCompletion → PROVEN | downgrade
            │           └─ routeObjectiveContinuation → CONTINUE | REPAIR | terminal
            ├─ failure             → DecideRecoveryWith (ledger + matrix)
            └─ exhaustion          → consultContinuationOnExhaustion → DeriveNextStep
                                         ↓
                                     RuntimeLoop.Step  ← the ONLY executor of a decision
                                         ↓
                              violation(): attempts / steps / identical / tokens
```

**Loop-control authority.** `continue/stop` is decided exclusively by
`RuntimeLoop` (and the authorities that *rewrite a proposal into a terminal
action*). A model can propose another action; it has no channel to authorize
continuation. There is exactly one loop.

---

## 3. The three stop owners, and what each answers

| Owner | Detects | Semantic? |
|---|---|---|
| `FailureLedger` (+`DecideRecoveryWith`) | same failure fingerprint, same **evidence epoch** → `NON_PROGRESSING_EXECUTION` → `UNSUBSTANTIATED` | **yes** — state-based |
| `execution.ReduceProgress` | objective conditions satisfied / exhausted → `PARTIALLY_SATISFIED` / `UNSUBSTANTIATED` | **yes** — evidence-based |
| `autonomy.RuntimeLoop` bounds | action counters (`MaxAttempts`, `MaxExecutionSteps`, `MaxIdenticalDecisions`, `MaxTotalTokens`) | **no** — safety ceiling |

Two further semantic engines exist but are **not consulted by the autonomy
continuation decision**:

- `internal/progress.Detector` — the stateful snapshot-delta detector
  (`PROGRESS` / `NO_PROGRESS` / `UNKNOWN`). Wired to
  `execution.NewBehaviorLoop` (`internal/execution/behavior_loop.go`), the
  behavioral repair loop.
- `internal/continuation.DeriveNextStep.detectNoProgress` — present, but
  unreachable from the driver because `DriverContinuationInput` never populates
  `TaskStateView.History` (so `len(history) >= 3` is never true on that path).

---

## 4. Required scenarios — deterministic traces

All tests below run with no model. The pure semantic proof is
`internal/execution/r5_progress_test.go`; the runtime proof is
`internal/runtime/autonomy/r5_nonprogress_test.go` and
`internal/progress/r5_nonprogress_test.go`.

### Case A — genuine progress → continuation allowed `PASS`

`TestR5_CaseA_GenuineProgressIsContinuable`: one of two declared targets was
really mutated. Progress = `PARTIALLY_SATISFIED`, `ObjectiveContinuation`
continues, and the **real router** re-opens the loop:

```
progress = PARTIALLY_SATISFIED
continuation = CONTINUE
routeObjectiveContinuation(LoopUnsubstantiate) → LoopContinue
    reason = "objective PARTIALLY_SATISFIED — 1 of 2 completion condition(s) hold; …"
```

`TestR5_CaseA_EndToEndMutationReachesProven`: a real two-target run over the
real executor reaches `PROVEN` / `completed` only after the durable mutations.

### Case B — same output, same state → eventually stops `PASS`

- **Semantic (failure) path**: `TestR5_CaseB_RepeatedIdenticalFailureStopsSemantically`.
  First deterministic failure → informed replan; **second under the same epoch**
  → `NON_PROGRESSING_EXECUTION` → `UNSUBSTANTIATED`.
- **Ceiling path**: `TestR5_CaseB_NoProgressIsBoundedByTheRuntimeLoop`. Three
  consecutive identical execution-bound decisions abort permanently
  (`"repeated identical decisions (3) — pathological loop detected"`).
- **Real run**: an exhausted provider over `@index.html`
  (`TestRecovery_ExhaustionProducesNoExecutableCandidate`) ends non-`PROVEN`,
  workspace byte-identical, bounded calls.

### Case C — repeated no-op capability `PASS`

A capability that completes with `exit=0` and changes nothing satisfies **zero**
completion conditions (`TestR5_CaseC_NoOpCapabilitySatisfiesNoCondition`,
`TestR5_P3_CapabilityExecutionWithoutStateChangeIsNotProgress`). End-to-end, a
change-request DAG that applies zero bytes parks at `EXECUTION_INERTIA_NO_OP`
and never completes (`TestPhase12_ModificationNoOpDAGNeverClaimsCompletion`).
The snapshot detector classifies repeated identical no-op states as
`NO_PROGRESS` (`TestR5_DetectorDistinguishesProgressFromStall`).

**Conclusion: a successful no-op capability is not progress.**

### Case D — repeated mutation attempt `PASS`

`TestR5_CaseD_RepeatedMutationRequestCannotLoop`: mutation #1 applies; the
identical patch #2 anchors on content that no longer exists → `ANCHOR_NOT_FOUND`.
The repeat is recorded and named (`NON_PROGRESSING_EXECUTION`, count=2), and the
disposition is terminal — never an unending identical re-issue.

```
mutation #1 → applied (durable delta, SCOPE_MUTATED)
mutation #2 → ANCHOR_NOT_FOUND → recorded as NON_PROGRESSING_EXECUTION → terminal
```

### Case E — progress followed by stall `PASS (bounded)` — with the open boundary

`TestR5_CaseE_ProgressThenStallIsBounded` proves the ledger's epoch semantics:
progress (`AdvanceEvidence`) *legitimately* clears one repeat; the very next
unchanged attempt is `NON_PROGRESSING_EXECUTION` → `UNSUBSTANTIATED`.

`TestR5_CaseE_ActionCeilingIsNotProgressAware` records the exact boundary. The
`RuntimeLoop`'s identical-decision ceiling aborts a stalled sequence **and an
equally-shaped progressing sequence** — because it compares *actions*, not
*state*:

```
stall sequence      → aborted ("repeated identical decisions")
progress sequence   → aborted ("repeated identical decisions")
```

So the stall is **bounded**, but not on the successful-partial path
**semantically detected**. See §5.

---

## 5. The first missing transition

> **`(attempt N: PARTIALLY_SATISFIED, no authoritative delta) → (attempt N+1)`**

| | |
|---|---|
| **First diverging transition** | `routeObjectiveContinuation` re-opens on the **static** `ProgressPartiallySatisfied` projection without comparing attempt N to attempt N−1. |
| **Owner that should own it** | the continuation decision — `Driver.routeObjectiveContinuation` over `execution.DecideContinuation`, consuming a per-lifecycle *previous-progress fingerprint*. (The failure path already has it: `FailureLedger`.) |
| **Current behaviour** | A partial objective keeps `LoopContinue` on every refused completion, because `PARTIALLY_SATISFIED` is monotone: once one condition holds it stays held. Termination is deferred to `RuntimeLoop.MaxIdenticalDecisions` (default 2), an **action** ceiling that is not progress-aware. |
| **Required invariant** | Another bounded attempt is admissible only if the authoritative state **advanced since the previous attempt** — a new evidence epoch, a newly satisfied condition, or a durable delta. Otherwise the objective terminates `UNSUBSTANTIATED` (or parks), never silently continues. |
| **Smallest implementation boundary** | One per-lifecycle `previousProgressFingerprint` (condition-set digest + evidence-epoch + mutation fingerprint) computed in `objectiveProgress`, and a delta predicate in `routeObjectiveContinuation`: continue only when `advance || progress == PARTIALLY_SATISFIED-from-a-fresh-delta`. No new loop; no keyword matching; no model self-report. |
| **Second, already-present hook** | `internal/progress.Detector` (snapshot delta) already implements the missing semantic; it is wired only to the behavioral loop. `continuation.DeriveNextStep.detectNoProgress` exists but is unreachable because `History` is unpopulated. |

**R5 does not implement this.** It was instructed not to add a generic loop
detector, and the existing stop is bounded (no infinite loop) and truthful
(`NOT PROVEN`). The boundary is recorded here so the next experiment can decide,
under contract, whether a progressing *and* a stalling `PARTIALLY_SATISFIED`
attempt must be told apart at the router.

---

## 6. Forensic coverage added (observability only)

`continuation.evaluated` / `continuation.selected` now carry the per-attempt
progress record. Nothing reads these fields to decide anything.

```
ContinuationDecisionPayload +
  progress, previous_progress
  new_evidence, new_artifact, mutation_applied,
  verification_advanced, objective_advanced
```

Emitted from `Driver.beginDecision` via `snapshotProgress` →
`captureProgress`, using the same evidence the completion authority and the
continuation router consume. Per-run reset in `Driver.Run`. Rendered by
`internal/forensics/trace.go`.

A real deterministic run (`TestR5_ForensicsRecordProgressAndTransitions`)
produces:

```
decision #1: progress=READY                 prev=                       mutation_applied=false objective_advanced=false
decision #2: progress=REQUIRES_AUTHORIZATION prev=READY                  mutation_applied=false objective_advanced=true  new_evidence=true   (approval held)
decision #3: progress=PROVEN               prev=REQUIRES_AUTHORIZATION mutation_applied=true  objective_advanced=true  new_evidence=true verification_advanced=true new_artifact=true
```

This is the forensic answer to **"what changed between attempt N and N+1?"**
and **"why was another attempt authorized?"** — the selected action plus its
progress classification, without replaying the contract.

---

## 7. Live benchmark

`test/live_r5/` runs the production composition (`compose.Wire` → the bounded
`autonomy.Driver` → `RuntimeExecutor`) against a real local model
(`ollama/qwen2.5-coder:7b`) in an isolated git workspace. The objective is a
legitimate large rewrite (120 literal list entries) that a 7B model reliably
under-delivers on — no prompt injection, no provider bug.

The assertion is **boundedness**, not convergence:

1. model calls never exceed the runtime-owned ceiling (`r5MaxModelCalls = 12`);
2. the run ends in a typed terminal or parked state;
3. every continuation decision carries an authoritative progress
   classification;
4. completion remains gated on `PROVEN` + an applied filesystem mutation.

<!-- LIVE_RESULT_START -->
Two real runs against `ollama/qwen2.5-coder:7b`, isolated git workspace, 2000-entry
literal-list objective, production composition. Both are bounded; one converged
and one did not. Neither is a false completion.

**Run A — bounded non-convergence (the low-progress case).**

```
provider:      ollama / qwen2.5-coder:7b
model_calls:   2   (requirement pass 149 tok stop; mutation 276 tok stop)
mutation:      target=index.html outcome=nochange artifact=true diff=false
               apply=true fs_changed=false (+0/-0)
objective:     UNSUBSTANTIATED  clause=objective_scope_unmutated
               "no durable delta observed on index.html"
decisions:     1 continue → 2 ask_human (approval) → 3 unsubstantiate
terminal:      unsubstantiated
patterns:      none detected
```

The model produced a *successful* mutation candidate that changed zero bytes.
The completion authority refused it (`nochange` is not `SCOPE_MUTATED`), the
loop did **not** re-authorize another identical attempt, and the run ended
`unsubstantiated` (a typed non-success) after exactly 2 calls. This is Case C
(successful no-op) and Case B (no progress → stop) demonstrated live.

**Run B — bounded continuation to PROVEN.** The same objective on another
draw produced a durable change; the continuation decisions carry the real
progress transitions:

```
DECISION #1  step=1  progress=READY                  prev=(none)                    [ ]
DECISION #2  step=2  progress=REQUIRES_AUTHORIZATION prev=READY   [new_evidence, objective_advanced]
DECISION #3  step=3  progress=PROVEN                  prev=REQUIRES_AUTHORIZATION
                                                       [new_evidence, new_artifact, mutation_applied, objective_advanced]
terminal:    completed (objective PROVEN by evidence)
model_calls: 2   patterns: none detected
```

Run B is the answer to *"what changed between attempt N and N+1?"* exactly as
the forensic record emits it: `READY → REQUIRES_AUTHORIZATION → PROVEN`, with
the mutation/artifact/verification flags. Run A is the bounded refusal.

<!-- LIVE_RESULT_END -->

---

## 8. The twelve questions

1. **What does IZEN consider progress?** An observed, authoritative state
   transition evaluated against the objective's own completion conditions
   (`ReduceConditions`/`ReduceProgress`), plus a failure fingerprint under an
   unchanged evidence epoch (`FailureLedger`).
2. **Which state transitions are authoritative?** P2 (new evidence), P4
   (durable workspace/artifact delta), P5 (verification), P6 (objective
   advancement). Not P0, not P1, not P3.
3. **Who owns continuation?** `Driver.routeObjectiveContinuation` /
   `DecideContinuation` for the success path; `DecideRecoveryWith` +
   `FailureLedger` for the failure path; `consultContinuationOnExhaustion` for
   exhaustion.
4. **Who owns stop/termination?** `autonomy.RuntimeLoop` (bounds + terminal
   actions). The completion authority can only *downgrade* a completion.
5. **Can model output alone count as progress?** No.
6. **Can a successful no-op capability count as progress?** No.
7. **What happens when the same state repeats?** Failure path: semantic
   `NON_PROGRESSING_EXECUTION` → terminal. Successful-partial path: bounded by
   `MaxIdenticalDecisions`.
8. **What happens after progress followed by repeated stalls?** Progress earns
   exactly one legitimate re-attempt (new epoch); the stall is then bounded by
   the ceiling, and the failure path stops semantically. Recorded in §5.
9. **Can repeated mutation attempts loop?** No — the identical request becomes
   an anchor failure, is recorded as a repeat, and terminates.
10. **What safety ceiling exists?** `LoopBounds{MaxAttempts:3,
    MaxRecoveryCycles:2, MaxExecutionSteps:10, MaxIdenticalDecisions:2,
    MaxTotalTokens: derived 64k–400k}`.
11. **Is there a semantic progress boundary already?** Yes — the objective
    reducer and the failure ledger. Missing only the *per-attempt delta* on the
    successful-partial path.
12. **If missing, what is the smallest missing contract?** A previous-progress
    fingerprint + delta predicate at `routeObjectiveContinuation` (§5).

---

## 9. What was deliberately NOT changed

- No redesign of the autonomy driver, `RuntimeExecutor`, or token allocation.
- No provider/`ohgo` change; no kernel/`kernelbridge`/authorization change.
- No generic retry loop; no keyword-based loop detection.
- Model self-report is still never authoritative.
- Completion evidence, authorization and preflight are untouched.
- R1/R2/R3/R4 semantics unchanged; their tests remain green.
- The §5 boundary is **reported, not implemented**.

---

## 10. Success criteria

| # | Criterion | Result |
|---|---|---|
| 1 | real progress → continuation allowed | **PASS** — Case A |
| 2 | no progress → continuation eventually stops | **PASS** — Case B (ledger + ceiling) |
| 3 | repeated no-op → cannot loop indefinitely | **PASS** — Case C |
| 4 | repeated mutation attempt → no infinite continuation | **PASS** — Case D |
| 5 | progress followed by stall → detected/bounded | **PASS (bounded)** — Case E, §5 records the missing semantic delta |
| 6 | completion still requires PROVEN evidence | **PASS** — authority is downgrade-only |
| 7 | `go test ./...` green | **PASS** |
| 8 | `-race` green (`go test -race ./...`) | **PASS** |
| 9 | R1/R2/R3/R4 remain green | **PASS** |
| 10 | live benchmark bounded on a real model | **PASS** — §7 |

---

## 11. Files

```
NEW  internal/execution/r5_progress_test.go             P0–P6 semantic proof (9 tests)
NEW  internal/runtime/autonomy/r5_nonprogress_test.go   Cases A–E + forensics (9 tests)
NEW  internal/progress/r5_nonprogress_test.go           state-delta detector (3 tests)
NEW  test/live_r5/probe_test.go                          live boundedness benchmark (opt-in)
NEW  docs/report/R5_NON_PROGRESS_REPORT.md               this report
MOD  internal/events/events.go                           progress + transition fields on
                                                         ContinuationDecisionPayload
MOD  internal/runtime/autonomy/forensics.go              snapshot/capture progress; emit it
MOD  internal/runtime/autonomy/driver.go                 per-run progress reset
MOD  internal/forensics/trace.go                         render progress transitions
MOD  docs/report/EXECUTION_FORENSICS_STATE.md            R5 handoff
```

No authority-bearing production path changed. R5 is **observability + proof +
one recorded boundary**.
