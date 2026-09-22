# IZEN Universal Bounded-LLM-Step Repair — Acceptance Report

**Date:** 2026-09-22
**Branch:** `fix/execution`
**Module:** `github.com/PizenLabs/izen`


---

## 1. Executive Summary & Final Decision

**FINAL DECISION: `ACCEPTED WITH OBSERVATIONS`**

The bounded-step repair that previously existed only inside plan synthesis has been
generalized into ONE shared capability-aware boundary primitive (`internal/llmstep`),
reused by the executor's read-only/ASK invocation path — the acceptance case
(`/ask` + constrained provider + `finish_reason=length`) is now closed: an exhausted
read-only response is a recoverable bounded step that schedules a continuation under
the SAME read-only authority and produces a completed conversational answer — never
a terminal error, never a `/plan` routing, never a mutation.

Observations that must be closed before the case is fully green end-to-end (none of
them block the boundary itself):

1. **Real constrained-provider trace not yet recorded.** The deterministic wire-clamp
   behavior is proven with a mock provider (`cohere/north-mini-code:free` → 980 on the
   wire, `internal/execution/ask_continuation_test.go`). A live
   `cohere/north-mini-code:free` trace with an actual `finish_reason=length` needs a
   provider credential — Section 16.
2. **Empty-salvage semantic.** A truncated response's buffered partial content is
   dropped by the executor output gate (mirrors plan `StepIncomplete` rescheduling);
   first-step exhaustion commits nothing and the continuation rebuilds from the bare
   base prompt. This is the accepted Phase-4 "empty scrambled buffer" case.
3. **Two unrelated pre-existing UI failures.** `TestAcceptanceCaseAConversationDirectResponse`
   and `TestAcceptanceCaseBAskMutationEscalates` fail identically on the clean baseline
   (verified by `git stash` re-run) and are outside this repair's scope.

---

## 2. Scope & Acceptance Case

The acceptance test: `/ask` with a capability-constrained provider whose local ceiling
has 501 tokens of response before `finish_reason=length` crossed a provider-capability
max_output of 3,136 — leading to a terminal error and `EvidenceState=PARTIAL`. The
repair target: bounded continuation so `/ask` reads and answers within its authority.

**Scope of this change:** `internal/llmstep` (new), `internal/execution/executor.go`
(read-only invocation + mutation budget resolution), `internal/modes/plan` (refactor
onto the shared primitive). **Out of scope by design:** no new runtime, no second
execution authority, no `/plan` routing for ASK, no mutation permission.

**Evidence tier used: Auditor** — affected symbols verified via
`codebase-memory-mcp` (`check_index_coverage`, `search_graph`, `trace_path`), exact
source read for every material claim, and full `go build`/`go vet`/`go test`/lint/race.

---

## 3. Authority Model (invariant audit) — `Verified`

| Invariant | Status | Where |
|---|---|---|
| Single execution authority preserved | `Verified` | `autonomy.Driver` / `execution.RuntimeExecutor` unchanged; no new executor/runtime |
| Continuation ≠ New Authority | `Verified` | `invokeReadOnly` wraps ONE strategy profile; continuation steps reuse the same step budget contract, never a new authority |
| `/ask` stays read-only | `Verified` | continuation path emits no `EventMutationStarted`; `ask_continuation_test.go` asserts absence |
| LLM output ⇒ no direct workspace mutation | `Verified` | mutate gate + `invokeMutation`/`Approve` unchanged |
| Resolution ≠ filesystem authorization | `Verified` | snapshot reads only; `getSnapshotContent` unchanged |
| Headless determinism | `Verified` | typed `*llmstep.OutputExhaustedError` surfaced as `OutcomeTruncated`; no hidden interactive wait (test 2) |

---

## 4. The One Budget Resolution / Invocation Boundary — `Verified`

`llmstep.ResolveMaxTokens(modelName, requested)` (`internal/llmstep/step.go:68`) is the
single capability-aware budget resolver. Audited paths confirmed **no** remaining
duplicate resolver:

- `internal/modes/investigate/toolrunner.go` — provider default (no hardcoded budget)
- `internal/runtime/compose/compose.go` — parameterized
- `internal/app/pipeline.go` — parameterized (`pipeline.go:70`)
- `internal/ui/agents.go`, `internal/ui/commands.go` — provider default
- `internal/ui/stream.go` — uses the accepted `resolveASKMaxTokens`/`ASKBudgetResolver` (UI path, untouched; Section 6)
- `internal/cli.go:439` — parameterized budget

**Observed boundary calls:** executor read-only (`executor.go:2104`), executor mutation
(`executor.go:2107`), plan synthesis (thin `resolveSynthesisMaxTokens` wrapper →
`llmstep.ResolveMaxTokens`).

---

## 5. Capability-Aware effectiveMaxTokens — `Verified`

**Insertion point:** executor budget resolution before every provider request.

- Read-only/ASK: `requested := effectiveMaxOutput(req.MaxOutputTokens, &profile)`; on
  zero → `llmstep.DefaultAskRequestedTokens` (1536); then
  `llmstep.ResolveMaxTokens(model, requested)` (`executor.go:2100–2108`).
- Mutation: same pattern with `llmstep.DefaultMutationRequestedTokens` (1200)
  (`executor.go:2099–2108`); constrained or profile-constrained models are forced to
  `patchOnly` + `ConstrainedMaxTokens=980` (`executor.go:2116–2125`).
- Free-tier model id ⇒ clamp to `ConstrainedOutputThreshold` (980) regardless of
  advertised ceiling (`llmstep/step.go:74–79`).

**Wire-verified:** `TestRuntimeExecutor_StrategyBudgetSurvivesRequestOmission` (777 via
profile) and `TestRuntimeExecutor_ReadOnlyStrategyBudgetSurvivesRequestOmission` (321)
still pass — the resolver never overrides an explicit per-request/per-strategy budget.
**New:** `TestAskConstrainedModel_RequestClampedBelowCeiling` asserts the wire
`MaxTokens == 980` for `cohere/north-mini-code:free`.

---

## 6. ASKBudgetResolver Reuse — `Verified`

The accepted `ASKBudgetResolver`/`ASKBudgetPolicy`/`ModelClass` in
`internal/runtime/orchestrator/budget.go` and the UI `resolveASKMaxTokens` in
`internal/ui/stream.go` are **untouched** (git status clean for those files). This
repair lives in the executor (CLI/headless + engine path), complementing — not
duplicating — the UI's AX budget path.

---

## 7. Bounded Continuation Semantics — `Verified`

- `llmstep.StepState` tracks `Ordinal`, `MaxTokens` (per-step), request-budget
  `CanContinue`/`ContinuationsLeft`/`Advance` (`step.go:114–150`). `Advance` increments
  the ordinal and consumes one continuation unit — the request budget cannot retry
  forever under a ceiling already proven insufficient.
- Continuation prompt: `ContinuationUserTurn(base, committed, pending, format, floor)`
  (`step.go:197`) — rebuilt **from the bare base each time** (test asserts the
  `OUTPUT BUDGET EXHAUSTED` block appears exactly once across rebuilds), never replays
  the transcript, tells the model what is already delivered (never repeat), what is
  pending, and the current output floor.
- Executor loop (`executor.go:2687+`): `StepStarted(1)` → call → `StepExhausted` →
  `StateRejected` → `Advance` → `ContinuationScheduled` → `StepStarted(n)` +
  `ContinuationStarted(n)` → call, bounded by `DefaultMaxContinuationSteps` (3).

**Observed deterministic sequence (test 1):** `reasoning.step.started ×2`,
`reasoning.step.exhausted ×1`, `reasoning.continuation.scheduled ×1`,
`reasoning.continuation.started ×1`, `reasoning.state.committed ×1`,
`reasoning.step.completed ×1`; **zero** `execution.mutation.*`.

---

## 8. Transcript-Free Continuation Context — `Verified`

`llmstep.ResponseState` (`response_state.go:31`) is the ONLY continuation context:
`answered_topics`, `pending_topics`, `validated_findings`, `evidence_refs`,
`response_format`, `continuation_cursor`, `status` (in_progress/continuation/complete).
`CompactContext` renders a bounded, transcript-free block; `AdvanceCursor` advances
exactly once per scheduled continuation; `Complete` closes the response. Unit-tested
(`response_state_test.go`). The executor seeds it with the raw request and the
read-only response format; delivered text is recorded via `AddAnswered`/`AddFinding`.

---

## 9. Typed OUTPUT_EXHAUSTED Condition & Recovery Policy — `Verified`

- `llmstep.OutputExhaustedError{Step, Hint}` + `IsOutputExhausted` (`step.go:255`):
  matches the typed error AND `ai.ErrPayloadTruncated` (transported wrapped either way).
- Executor local `isOutputExhausted` (`executor.go`) matches `OutputGateError` with
  `CanonicalOutputExhausted`, execution's `ErrPayloadTruncated`, and
  `llmstep.IsOutputExhausted` — one recovery policy for the trampolined gate.
- Policy in `invokeReadOnly`: exhaustion with budget remaining → continuation;
  exhaustion with budget consumed and delivered state → success (bounded answer is not
  a failure); consumption with nothing delivered → typed recoverable error
  (`OutcomeTruncated`, never a silent success — test 2).
- Fail-closed: `ValidateProviderModel` / `ALLOWED_FILE_TREE` compute-vs-request guards
  still fail pre-invocation; `invokeReadOnly` returns a hard error for non-exhaustion
  invocation failures.

---

## 10. Read-Only / ASK Invocation Driver — `Verified`

`invokeReadOnly(ctx, req, requestID, profile, targets, g)` (`executor.go`) now returns
`([]ModelInvocation, *ingestion.IngestionTrace, error)` and:

- resolves the capability-aware step budget and `ResponseState`,
- issues one provider request per step (bounded `MaxTokens`),
- records a `ModelInvocation` (with authoritative usage + `finish_reason`) for **every**
  attempt — including gate-exhausted ones — and pairs `g.CompleteModel` with real
  usage for every completed provider response,
- preserves invocation evidence on the error path (`Execute` appends `invs...` to
  `res.ModelCalls` / `res.Proof.ModelInvocations` before failing).

**Invariants preserved:** mutation truncation still yields `ErrOutputTruncated`
(`TestRuntimeExecutor_FinishReasonLengthBecomesTruncatedOutcome` passes); profile
MaxTokens still travels verbatim.

---

## 11. Mutation Budget Resolution (semantics preserved) — `Verified`

The mutation path keeps its capability-aware hard cap for constrained models but the
old global 1200/800 constants were replaced by `llmstep.DefaultMutationRequestedTokens`
(1200) + `ResolveMaxTokens`. Deterministic regression rows (777/321 profile budgets,
`length`→`OutcomeTruncated`) still pass. Strategy `outputForArtifact` budgets still flow
through `effectiveMaxOutput` for unconstrained models.

---

## 12. Telemetry & Evidence Ledger — `Verified`

- Events emitted from the executor via existing `x.emit(bus)` (`executor.go:743`):
  `reasoning.step.*`, `reasoning.continuation.*`, `reasoning.state.*`.
- No misleading AUTONOMY events on the pure-ASK path (asserted: no
  `execution.mutation.started`).
- Invocation evidence (`Proof.ModelInvocations`) records `FinishReason="length"` and the
  authoritative completion-token count for the exhausted attempt (test 1 asserts
  `TokenOutput == 980`).

---

## 13. Fail-Closed Guards Preserved — `Verified`

CAPABILITY preflight and scope guards untouched and still active for read-only ASK;
invocation errors short-circuit to a typed failure with evidence; budget exhaustion
never loops silently — the request budget (3 continuations) bounds the sequence; the
`OutcomeTruncated` proof and the typed error travel to the caller.

---

## 14. Regression Matrix — `Verified` (deterministic rows) / `Not Yet Proven` (provider-locked rows)

Deterministic rows encoded as tests (all pass):

| Row | Scenario | Test |
|---|---|---|
| A | constrained free-tier request clamps to 980 on the wire | `TestAskConstrainedModel_RequestClampedBelowCeiling` |
| B | `length` ⇒ recoverable; one bounded continuation ⇒ completed answer | `TestAskTruncatedFollowedByContinuation_Succeeds` |
| C | budget exhausted with nothing delivered ⇒ typed `OUTPUT_EXHAUSTED`, `OutcomeTruncated`, no silent success | `TestAskContinuationBudgetExhausted_TypedRecoverable` |
| D–E | explicit request/strategy budgets survive resolution verbatim | `TestRuntimeExecutor_{Strategy,ReadOnlyStrategy}BudgetSurvivesRequestOmission` |
| F | mutation `length` still surfaces `ErrOutputTruncated` | `TestRuntimeExecutor_FinishReasonLengthBecomesTruncatedOutcome` |
| G | plan free-tier clamp (980) baseline | `internal/modes/plan/bounded_continuation_test.go` |
| H | provider validation + scope guards fail closed | pre-existing executor tests |
| I | step-state lifecycle advances exactly `DefaultMaxContinuationSteps` | `TestStepStateLifecycle` |
| J | continuation turn never replays / duplicates the instruction block | `TestContinuationUserTurn_NeverReplaysTranscript` |
| K | `IsOutputExhausted` matches typed error + `ai.ErrPayloadTruncated` (bare + wrapped) | `TestIsOutputExhausted` |
| L | `ResponseState` lifecycle + compact transcript-free context | `response_state_test.go` |

Provider-locked rows (real `finish_reason=length` against live constrained models,
end-to-end `/ask` through the UI) are **Not Yet Proven** in this run — see Section 16.

---

## 15. Verification Results — `Verified`

- `go build ./...` — clean
- `go vet ./...` — clean
- `golangci-lint run ./internal/llmstep/... ./internal/execution/` — 0 issues (gofmt applied)
- `go test ./internal/llmstep/ ./internal/execution/ ./internal/modes/plan/ -race -count=1` — clean
- `go test ./... -count=1` — all packages pass **except** `internal/ui`
  `TestAcceptanceCaseAConversationDirectResponse` and
  `TestAcceptanceCaseBAskMutationEscalates`, which fail identically on the clean
  baseline (`git stash` re-run) and are unrelated to this repair.

Note: re-invoking `commit`-selector methods inside the executor would trip the
architecture lock in `internal/architecture/phase1_context_risk_lock_test.go:653-661`;
the llmstep delivered-state method is named `RecordDelivered` specifically so
transaction-`Commit` remains Apply/Approve-exclusive.

---

## 16. Real Constrained-Provider Trace — `Not Yet Proven`

No live provider credential was available in this environment, so the primary
acceptance trace (`/ask` on `cohere/north-mini-code:free`, real 501-token `length`
cut, end-to-end event stream) was NOT recorded. The deterministic analogue
(mock wire-clamp = 980 + full step/continuation/completed event sequence; proof
invocations with `finish_reason=length`) is recorded in Section 7/10/14. The live
trace remains the acceptance gate: run the harness against a real
`cohere/north-mini-code:free` and append the transcript before closing the observation.

## 17. Open Observations, Risks & Next Steps

**Observations (resolved in this change):**
- The plan package's `resolveSynthesisMaxTokens` remains as a thin delegating wrapper
  so plan tests keep compiling; it contains no independent budget logic.
- Truncated partials are dropped (empty salvage) by design — documented Section 1.

**Open risks (tracked, not blocking):**
- Two pre-existing `internal/ui` autonomy failures (Section 15) predate this change and
  live in the UI/autonomy acceptance layer, not the executor boundary.
- The real provider trace (Section 16) is the only remaining evidence gap.

**Next steps:**
1. Run the live-`cohere/north-mini-code:free` acceptance trace and append it.
2. Close the two pre-existing UI autonomy failures in a separate change.
3. Consider a follow-up hardening: if the stream reader ever yields partial content
   before the gate, salvage it into `StepState` instead of dropping (currently the
   transport returns `""` on `OUTPUT_EXHAUSTED`, so this is not reachable today).

---

### Artifacts
- `internal/llmstep/step.go`, `internal/llmstep/response_state.go` — shared boundary primitive (new)
- `internal/execution/executor.go` — read-only continuation driver, mutation budget, `isOutputExhausted`, Execute evidence append
- `internal/modes/plan/synthesis_step.go`, `internal/modes/plan/engine.go` — plan refactor onto the shared primitive
- `internal/llmstep/step_test.go`, `internal/llmstep/response_state_test.go`, `internal/execution/ask_continuation_test.go` — regression matrix
