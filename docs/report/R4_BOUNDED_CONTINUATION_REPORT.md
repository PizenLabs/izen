# R4 — BOUNDED CONTINUATION FORENSICS

**Status: PROVEN — a real model call can exhaust its output ceiling, IZEN
records the exhaustion truthfully, the runtime — not the model — decides to
continue inside the SAME execution, and completion remains evidence-gated.**

**Branch:** `fix/runtime` (continues from the recorded R3 PROVEN state).

**Read `EXECUTION_FORENSICS.md` / `EXECUTION_FORENSICS_STATE.md` first.** This
report is the R4 continuation. R1 remains PROVEN, R2 remains BLOCKED as recorded,
R3 remains PROVEN; R4 does not reopen any of them.

Reproduce:

```
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r4/ -v -timeout 1200s          # live
go test ./test/forensics/ -run TestR4_ -v                                # deterministic
```

---

## The question

> When a real model reaches its output limit before completing an authorized
> objective, does IZEN correctly continue the same execution state, or does it
> stop, restart, duplicate work, lose context, or falsely complete?

The invariant under test is the separation:

```
finish_reason = length   ≠   failure   ≠   completion
```

A `length` finish means *computation ended because the bounded output budget was
exhausted*. The runtime must decide what that means. The model never does.

**Answer: IZEN continues the same execution state.** It emits a genuine
`finish_reason=length`, records the exhaustion against the observed ceiling,
evaluates continuation explicitly, issues a second call **inside the same run**
(same root run id, same frozen `ExecutionSpec`, same target, same workspace
lineage), and grants completion only through the evidence-gated authority. When
the continued artifact is not sufficient it **does not complete** — it ends
`awaiting_human` or `unsubstantiated`, never `PROVEN`.

---

## 1. Instrumentation — where each fact actually lives

Traced through the real call graph, not from comments.

| # | Fact | Owner | Location |
|---|---|---|---|
| 1 | **Requested** output tokens | `strategy.outputForArtifact` derives `profile.MaxOutputTokens`; `llmstep.ResolveMaxTokens` clamps capability; the adapter publishes the effective request | `internal/execution/strategy/selector.go:798`; `internal/llmstep/step.go:68`; `internal/runtime/autonomy/adapter.go:425-432`; `internal/execution/executor.go:3213` (`effectiveMaxOutput`) |
| 2 | **Effective** output tokens | provider telemetry: the completion count observed at truncation *is* the enforced ceiling | `internal/execution/executor.go:4006-4022` (`requested`/`effective`/`effectiveKnown`) |
| 3 | **Actual** output usage | provider-reported `CompletionTokens`/`PromptTokens`; aggregated once per logical invocation | `internal/execution/executor.go:4038-4042`; `internal/runtime/autonomy/driver.go:2416-2424` (`d.aggOutput`) |
| 4 | `finish_reason` capture | normalized at the transport boundary; carried on the invocation and the provider record | `internal/providers/contract_serialization.go:288` (`NormalizeFinishReason`); `internal/execution/executor.go:4043` |
| 5 | `length` → runtime state | the output gate maps the terminal reason to `CanonicalOutputExhausted`; the recovery classifier maps the observation to `SubtypeOutputExhausted` | `internal/execution/executor.go:250,3575` (`gateFor`); `internal/runtime/autonomy/recovery.go:98` |
| 6 | Continuation **evaluation** | two layers, below | `internal/execution/artifact_step.go:172`; `internal/runtime/autonomy/driver.go:2695` (`consultContinuationOnExhaustion`); `internal/runtime/autonomy/forensics.go:118/143` |
| 7 | **Who owns** the decision | the Driver recovery matrix (`DecideRecovery`); the executor’s bounded-step lifecycle; the pure `continuation.DeriveNextStep` only *proposes* | `internal/runtime/autonomy/recovery.go:231`; `internal/continuation/derive.go:15` |
| 8 | Next call’s prior state | layer 1 rebuilds from the original base turn + the delivered prefix; layer 2 builds a materially different request (bounded patch) with the exhaustion diagnostic | `internal/execution/artifact_step.go:119` (`artifactContinuationTurn`); `internal/runtime/autonomy/recovery.go:396` (`typedRepair`) |
| 9 | Same `ExecutionSpec` | the driver’s `d.req` is the run; the spec is emitted frozen once and the target is unchanged | `internal/runtime/autonomy/forensics.go:290` (`emitSpecFrozen`) |
| 10 | Task/step identity | root run id preserved; a recovery attempt appends `-attempt-N`; step ordinals on `reasoning.step.*` | `internal/forensics/trace.go:286-309` (`rootRunID`); `internal/execution/artifact_step.go:216` |
| 11 | Context: reconstructed vs extended | layer 1 **incrementally extends** the same artifact (delivered prefix quoted back); layer 2 **reconstructs** a bounded disk window | `internal/execution/artifact_step.go:119-145`; `internal/execution/executor.go:3398-3451` |
| 12 | Prior evidence visible to next step | layer 2 injects `[DIAGNOSTIC subtype=OUTPUT_EXHAUSTED …]`; the provider truncation is on the invocation record | `internal/runtime/autonomy/recovery.go:505-508` |
| 13 | Continuation repeating a mutation | a truncation is a *candidate*, never a mutation; the executor’s no-progress guard refuses an exhausted step that delivered nothing | `internal/execution/artifact_step.go:20-25,300-313` |
| 14 | Completion after an exhausted call | the objective completion authority may only *remove* a completion; a `length` is never itself a completion | `internal/runtime/autonomy/driver.go:2261-2270`; `internal/execution/objective_authority.go` |

### Required per-call observability — mapping

| Required field | Source | Notes |
|---|---|---|
| `call_id` | `ProviderExecutionPayload.RequestID` | `""` for the read-only requirement pass |
| `execution_id` | `RunID` (canonical events) / `RequestID` root | attempt suffix `-attempt-N` is a child of the same run |
| `step_id` | `StepStartedPayload.Step` (`reasoning.step.started`) | layer-1 ordinals; `UNKNOWN` for the single bounded-patch recovery call |
| `requested_output_tokens` | `ProviderExecutionPayload.RequestedOutputTokens` | the request’s `max_tokens` |
| `effective_output_tokens` | `…EffectiveOutputTokens` / `EffectiveOutputKnown` | `known=false` ⇒ no smaller ceiling was ever demonstrated |
| `actual_output_tokens` | `…CompletionTokens` / `PromptTokens` / `TotalTokens` | provider-reported; estimated flag when the local model omits usage |
| `finish_reason` | `…FinishReason` (+ `Truncated` + `ErrorCode`) | `length` ⇒ `error_code=output_truncated` |
| `provider_state` | **UNKNOWN (not an explicit field)** — derivable from `UsageKnown`/`Truncated`/`FinishReason` | reported honestly; not invented |
| `continuation_decision` | `ContinuationDecisionPayload.SelectedAction` (`continuation.selected`); layer 1 `reasoning.continuation.*` | |
| `continuation_reason` | `…SelectedReason` / `ProposedReason` | |

Where the provider exposes nothing, the field is left empty / `UNKNOWN`; no value
is fabricated.

The required continuation vocabulary is covered by the decision record:
`continuation.evaluated` publishes the PROPOSAL, and `continuation.selected`
publishes what the loop applied. There is no separate
`continuation.skipped` / `continuation.blocked` event type; those outcomes travel
as the selected action (`BLOCKED`, `NO_PROGRESS`, `STALE`, `AWAITING_APPROVAL`),
which is the single-sourced spelling of the same fact.

---

## 2. The two continuation layers (the actual mechanism)

Continuation is **not** a single feature. Two runtime-owned layers exist, and R4
verified both.

**Layer 1 — executor full-artifact bounded-step continuation**
(`internal/execution/artifact_step.go`, `invokeArtifactBoundedStep`). One
full-artifact generation is treated as a logical task that may need several
bounded invocations. On `length` the delivered prefix is preserved as a
**candidate** (never a mutation), and the next step re-invokes the SAME artifact
contract with the delivered prefix quoted back. Bounded by
`llmstep.DefaultMaxContinuationSteps = 3`. Emits `reasoning.step.*` /
`reasoning.continuation.*` / `reasoning.state.*`.

**Layer 2 — driver recovery matrix** (`internal/runtime/autonomy`). When the
executor’s bounded-step budget is consumed (or the exhausted step delivered no
new bytes) the typed `OUTPUT_EXHAUSTED` reaches the driver. `DecideRecovery`
materialises a **materially different** contract (`FULL_REWRITE → BOUNDED_PATCH`),
`consultContinuationOnExhaustion` confirms it through the pure
`continuation.DeriveNextStep`, and the loop re-enters the SAME executor with a
diagnostic-bearing request. Emits the canonical
`continuation.evaluated` / `continuation.selected`.

Both layers: never authorize, never mutate on a partial, never widen scope, and
never treat `length` as failure or completion.

---

## 3. The live benchmark

`test/live_r4/` runs the production composition (`compose.Wire` → the bounded
`autonomy.Driver` → `RuntimeExecutor`) against a real local Ollama model
(`qwen2.5-coder:7b`) in an isolated git workspace. The objective **names an
explicit target** so the experiment isolates continuation rather than discovery
authority, and requires more output than the bounded `replace_block` request
(1024) can hold:

```
workspace:  @index.html   (≈1.3 kB, sized just under the Boundary-2
                           complete-file preflight ceiling)
objective:  rewrite the complete @index.html file so the <ul id="items"> list
            contains exactly 80 entries … output the entire updated file;
            then verify the page renders
```

The complete-file response is larger than 1024 tokens; the runtime’s continued
invocation is a bounded SEARCH/REPLACE patch, which re-emits only the changed
block.

### Real event trace (one converged run, verbatim from `.izen/audit`)

```text
execution.spec.frozen
execution.authorized
continuation.evaluated   step=1 proposed=continue   selected=-
continuation.selected    step=1 proposed=continue   selected=continue  next=executing
execution.started
reasoning.step.started   step=1 max_output=1024
execution.model.invoked  request=run-1
execution.provider.execution  finish_reason=length truncated=true
                              requested=1024 effective=1024 known=true
                              completion=1024 output_chars=2499
                              error_code=output_truncated
reasoning.step.exhausted step=1 output_tokens=1024
continuation.evaluated   step=2 proposed=repair   selected=-
continuation.selected    step=2 proposed=repair   selected=repair  next=recovering
execution.model.invoked  request=run-1-attempt-2          # SAME run, new attempt
execution.provider.execution  finish_reason=stop requested=1024 completion=798
approval.required
execution.mutation.started / execution.mutation.completed   fs_changed=true
execution.verification.started / execution.verification.completed
objective.evaluated      state=PROVEN granted=true mutations=1
continuation.evaluated   step=4 proposed=complete selected=-
continuation.selected    step=4 proposed=complete selected=complete next=completed
                              REWRITTEN by objective_completion_authority,
                                         behavioral_completion_gate
execution.finished
```

Observed model calls in that run:

| Call | request_id | requested | effective | known | finish | tokens | role |
|---|---|---:|---:|---|---|---:|---|
| #1 | `""` | 512 | 512 | false | `stop` | 53 | read-only requirement pass |
| #2 | `run-1` | **1024** | **1024** | **true** | **`length`** | **1024** | mutation, step 1 |
| #3 | `run-1-attempt-2` | 1024 | 1024 | false | `stop` | 798 | driver continuation (bounded patch) |

The workspace converged to the requested 80-entry list, and the objective was
granted `PROVEN` by `objective_completion_authority` + `behavioral_completion_gate`
(single applied mutation, `fs_changed=true`, behavioural serve/fetch HTTP 200).

The run is non-deterministic in the *model’s artifact quality* — a 7B local model
does not always emit a valid continued patch. The acceptance arm therefore allows
a small number of attempts for convergence while asserting the runtime chain on
every attempt; see §5 for the negative runs.

---

## 4. Critical checks

**A. No false completion — PASS.** The completion authority may only rewrite a
`complete` into something weaker, never grant one. Across the live runs, when the
continued artifact did not satisfy the objective the run ended
`unsubstantiated` (`NO_PROGRESS`) or `awaiting_human` — never `completed`, never
`PROVEN`. Deterministic pin: `TestR4_RepeatedExhaustionNeverCompletes`.

**B. No blind retry — PASS.** Continuation is selected by
`continuation.evaluated/selected` (`proposed=repair`, `selected=repair`) after a
runtime-owned `DecideRecovery`. A `length` alone never re-invokes; the executor’s
bounded-step path additionally refuses to continue an exhausted step that
delivered no new bytes (`artifact_step.go:300-313`).

**C. State continuity — PASS.** `execution_id #1 == execution_id #2` at the run
level: `run-1` → `run-1-attempt-2` (the reader’s `rootRunID` treats the attempt
suffix as one run). The frozen `ExecutionSpec` (`targets=[index.html]`), the
authorization and the workspace lineage are unchanged. The second prompt differs
from the first (`identical prompt: false`), and no new execution is created.

**D. No duplicate mutation — PASS.** A truncated generation is a *candidate*,
never admitted; only one `mutation.completed` with `fs_changed=true` is recorded,
and `REPEATED_IDENTICAL_EXECUTION` / `NON_PROGRESSING_CONTINUATION` do not fire.

**E. Completion remains evidence-gated — PASS.** The terminal `PROVEN` came from
`objective.evaluated` (`granted=true`) preceded by `mutation.completed`
(`fs_changed=true`) and `verification.*`.

**F. Continuation exhaustion — PASS.** With a provider that never stops
truncating, the run is bounded and never `PROVEN`; the workspace is byte-identical
to the baseline. Deterministic pin:
`TestR4_RepeatedExhaustionNeverCompletes`.

---

## 5. Deterministic regression (`test/forensics/r4_continuation_test.go`)

All five always-run tests pass:

| Test | Pins |
|---|---|
| `TestR4_ExecutorBoundedStepContinuation` | layer 1: `length` + delivered prefix → same-contract continuation; 2 calls, different prompt, same run; one mutation; `PROVEN` |
| `TestR4_DriverContinuationAfterBoundedStepBudgetExhausted` | layer 2: `length` + no delivered bytes → explicit `continuation.evaluated/selected` `repair`; 2 calls in one run; one mutation; `PROVEN` |
| `TestR4_RepeatedExhaustionNeverCompletes` | continuation limit reached → **never** `PROVEN`, bounded calls, workspace intact |
| `TestR4_AppliedMutationIsNotReapplied` | a single successful call → exactly one call, one mutation (no blind duplicate) |
| `TestR4_PureContinuationStateMachine` | pure library: partial+target→`CONTINUE`; complete+verified→`COMPLETE`; partial+no-target→`BLOCKED`; out-of-scope→`AWAITING_APPROVAL` |

---

## 6. The first incorrect transition

**There is no incorrect continuation transition.** The chain
`length → exhaustion recorded → continuation evaluated → continuation selected →
second call → same execution → evidence-gated completion` holds, and the negative
outcomes are truthful rather than optimistic.

### One concrete finding (reported, not fixed)

The executor’s full-artifact bounded-step continuation returns the delivered
prefix on truncation in the **non-streaming** path
(`executor.go:4672`, deliberately, so the same contract can continue), but the
**streaming** path discards it and returns `""` with the gate error
(`executor.go:5028, 5040, 5056`).

| | |
|---|---|
| **First diverging transition** | `invokeStream` streaming truncation returns `raw=""` while the non-streaming path returns `resp.Content` |
| **Owner** | `RuntimeExecutor.invokeStream` (`internal/execution/executor.go`) |
| **Observed state** | layer-1 no-progress guard sees no delivered bytes → returns typed `OUTPUT_EXHAUSTED`; continuation falls through to the driver’s contract change (layer 2) |
| **Expected state** | layer 1 could continue the SAME artifact contract using the delivered prefix |
| **Impact** | none on R4 correctness — layer 2 continues correctly, preserves identity and does not falsely complete. It is a salvage/efficiency asymmetry, not a semantic defect. |
| **Minimal correction boundary** | make the streaming truncation returns carry `content.String()` exactly as the non-streaming path does (`4672`) — a one-line change at three sites, **not applied** here because R4 semantics do not require it and the runtime is authoritative |

The delivered bytes remain observable even now (`provider.execution.output_chars`
recorded 2499 for the truncated call), so nothing is hidden; only the in-executor
continuation does not get to use them.

---

## 7. What was deliberately NOT changed

- No `RuntimeExecutor` change (the finding above is reported, not patched).
- No target authority / `I13` change; no scan→target guessing; no model-driven
  authorization.
- No token-allocation redesign; the accounting was inspected, not replaced.
- No provider/`ohgo` architecture change; no kernel/`kernelbridge` change.
- No generic retry; no new unbounded loop.
- R1/R2/R3 semantics unchanged; their tests remain green.

---

## 8. Success criteria

| # | Criterion | Result |
|---|---|---|
| 1 | a real bounded call can produce `finish_reason=length` | **PASS** — `run-1` call, `length`, effective 1024 known=true |
| 2 | IZEN records exhaustion truthfully | **PASS** — `output_exhausted=1`, `error_code=output_truncated`, `effective=1024` |
| 3 | incomplete state does not become `PROVEN` | **PASS** — negative live runs ended `unsubstantiated` / `awaiting_human` |
| 4 | runtime explicitly evaluates continuation | **PASS** — `continuation.evaluated/selected` |
| 5 | continuation stays inside the same execution | **PASS** — `run-1` → `run-1-attempt-2` |
| 6 | second call receives sufficient state | **PASS** — bounded-patch window + `[DIAGNOSTIC subtype=OUTPUT_EXHAUSTED]` |
| 7 | mutations are not blindly duplicated | **PASS** — exactly one applied mutation |
| 8 | completion remains evidence-gated | **PASS** — `objective.evaluated granted=true` after real evidence |
| 9 | successful convergence reaches `PROVEN` | **PASS** — live `TestLiveR4_Acceptance` converged |
| 10 | bounded failure remains non-`PROVEN` | **PASS** — deterministic + live negative |
| 11 | deterministic regression covers the state machine | **PASS** — 5 tests |
| 12 | R1/R2/R3 remain green | **PASS** — full suite |

---

## 9. Files

```
NEW  test/live_r4/probe_test.go                     live R4 benchmark (opt-in)
NEW  test/forensics/r4_continuation_test.go         the deterministic state-machine
                                                    regression (5 tests)
NEW  docs/report/R4_BOUNDED_CONTINUATION_REPORT.md  this report
MOD  docs/report/EXECUTION_FORENSICS_STATE.md       R4 handoff
```

No production file was modified. R4 is **observability + proof**.
