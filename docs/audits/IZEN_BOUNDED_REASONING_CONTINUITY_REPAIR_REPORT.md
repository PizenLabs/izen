# IZEN — Bounded Reasoning Continuity Repair Report

**Status:** Final
**Date:** 2026-09-22
**Scope:** Plan-synthesis output-ceiling handling (`finish_reason=length` → bounded continuation)

---

## 1. Executive Summary

Plan synthesis issued a **single full-size output budget (1,536 tokens) regardless of
provider capability**. Free-tier / constrained provider models enforce a much lower
output ceiling (~980 tokens) and cut the response at `finish_reason=length` **mid-JSON**.
The engine's recovery was a **blind same-scope retry loop** (`maxSilentRetries = 2`) that
re-issued the identical prompt with the identical 1,536-token budget — guaranteed to fail
the same way, up to two extra provider round-trips, before surfacing a confusing fallback.

This report documents the repair: the synthesis budget is now **derived from the model's
actual capability**, constrained models receive a **bounded-output contract**, and a
truncated step enters a **smaller-step bounded continuation** that commits only validated
atomic results and advances them step-by-step, governed by typed
`SynthesisError` outcomes. No second runtime, executor, or authority was introduced.

---

## 2. Baseline: Observed Failure

- Command: `$hot redesign @index.html` (constrained/free model)
- Failure: plan-JSON synthesis returned `finish_reason=length`; the parse fell to the
  silent retry loop which re-ran the same prompt + same budget and failed identically.
- Contributing hazard found during the trace: `ParseJSONPlan`'s tolerant
  `autoCloseJSON` repair can turn truncated JSON into *valid-looking but incomplete*
  plans — a silent partial-plan commit risk.
- Additional hazard: `complete()` accumulated the streaming buffer but **discarded
  `finish_reason` on the success path**, so callers could not distinguish a complete
  response from an exhausted one.

## 3. Root Cause

| # | Root cause | Location (pre-fix) |
|---|-----------|--------------------|
| 1 | Budget hardcoded to 1536 for every model | `internal/modes/plan/engine.go` (`MaxTokens: 1536`, fast-track + JSON branches) |
| 2 | Capability ceilings ignored for constrained/free-tier models | no budget resolution call; `capability.*` helpers existed but were not used here |
| 3 | `finish_reason` dropped on the success path, hiding exhaustion | `internal/modes/plan/engine.go` `complete()` |
| 4 | Recovery was a blind same-scope retry | retry loop `maxSilentRetries := 2` + `retryReinforcement` |
| 5 | Tolerant JSON repair masked truncation | `internal/modes/plan/schema.go` (`autoCloseJSON`) |

## 4. Design Decision

The repair lives in the **existing plan synthesis path** — the smallest coherent insertion
point. Driver-side continuation machinery (`continuation`, `stepadmission`) already existed
and was left untouched. A second "bounded" runtime was considered and **rejected** (it would
have split the authority surface and re-opened the invariants that the runtime already pays
down).

## 5. Governance / Authority (Invariants Preserved)

- No model output can directly mutate workspace state (invariant 10) — every staged task
  still passes the existing validation gates before commit.
- Execution-time confinement (invariant 11): the staged tasks remain **plan proposals**; the
  runtime authority still authorizes execution independently.
- Conflict preservation (invariant 12): salvage and merge never reinterpret an operation;
  tasks are committed exactly as validated, deduplicated by identity.
- Headless determinism (invariant 14): an unrecoverable continuation returns a **typed
  `SynthesisError`**, never a hidden interactive wait.

## 6. The Bounded-Step Model

A bounded reasoning step = a single LLM invocation whose **output budget and scope are
derived from the provider's actual capability**, not a global constant:

- `resolveSynthesisMaxTokens(model, 1536)` clamps to the constrained ceiling (980) or keeps
  the requested budget for capable models; returns `constrained` flag.
- Constrained models get a `[SYSTEM: BOUNDED OUTPUT CONTRACT]` instruction: at most
  `taskBudget` (initial 4, floor 1, shrink 2) `atomic_task` entries per response, terse
  strings — so a full batch *fits* the ceiling instead of being cut mid-JSON.
- On `finish_reason=length`:
  - `salvageValidTasks` commits **only validated atomic results** (atomic commit).
  - Nothing validated → **NO STATE COMMIT** (StepIncomplete) → next step is rescheduled
    **smaller** (adaptive granularity), never the same full scope.
  - Each continuation rebuilds the prompt from `baseUserContent` + a compact summary of
    committed state (`boundedContinuationAppend`) — it **advances work instead of replaying
    the transcript**.
  - The whole sequence is bounded by `defaultMaxContinuationSteps = 3`.
  - Natural `stop` merges the response with committed state (dedupe by task identity).

## 7. Capability-Aware Budget Resolution

`resolveSynthesisMaxTokens`:
1. Splits `vendor/model` off the model id (`splitModelVendor`).
2. Asks `capability.MaxOutputTokensFor(vendor, model)` for the family-heuristic ceiling.
3. Free-tier ids (`IsFreeTierModelID`, `:free` suffix) and ceilings at/below the
   constrained threshold (`ConstrainedOutputThreshold = 1024`) are classified **constrained**.
4. Constrained → `ClampMaxTokensForBudget(requested, ceiling)` ⇒ **980**.
5. Unconstrained → keeps the requested 1536.

Pinned by `TestResolveSynthesisMaxTokensConstrainedFreeTier`
(`dots-studio/dots-3-note-preview:free` → 980, constrained) and
`TestResolveSynthesisMaxTokensUnconstrained` (`test-model` → 1536, not constrained).

## 8. Bounded Continuation Semantics

`(e *Engine) synthesizeBoundedContinuation(...)`:
1. **Atomic commit** from the exhausted first buffer (`salvageValidTasks`).
2. Loop while request budget remains (`canContinue`):
   - StepIncomplete (`salvage==∅ && staged==∅`): shrink task budget or fail with typed
     `OUTPUT_EXHAUSTED` at the floor; emit `BudgetRecalculated`.
   - Rebuild continuation prompt from `baseUserContent`; run the bounded step.
   - Exhausted again → salvage validated results, or reject state and continue.
   - Natural stop → validate + ground, merge with staged, return final plan.
3. Loop exhausted → `commitStepState`: commits the staged validated plan, or a typed
   `OUTPUT_EXHAUSTED` (with `Step`, `Hint`) when nothing validated was staged.

## 9. Code Changes

| File | Change |
|------|--------|
| `internal/ai/provider.go` | Added `FinishReason` to `ai.Response`. |
| `internal/events/events.go` | 8 typed events + payloads + constructors: `reasoning.step.started/completed/exhausted`, `reasoning.continuation.scheduled/started`, `reasoning.state.committed/rejected`, `reasoning.budget.recalculated`. |
| `internal/modes/plan/synthesis_step.go` | **New** — bounded-step core (budget resolution, step state, salvage, merge, `SynthesisError`, continuation driver). |
| `internal/modes/plan/engine.go` | `complete()` propagates `FinishReason`; `processFromLedger` uses `resolveSynthesisMaxTokens`, injects the bounded contract, records `baseUserContent`, and routes `finish_reason=length` to `synthesizeBoundedContinuation`; `MaxTokens` no longer hardcoded. |
| `internal/ui/model.go` | `handleDomainEvent` projects the 8 new events to activity lines. |
| `internal/modes/plan/bounded_continuation_test.go` | **New** — capability, no-salvage shrink-floor, salvage-then-stop merge, telemetry, system-prompt contract tests. |

## 10. Telemetry

Every step transition is a typed domain event, so projections distinguish
**OUTPUT_EXHAUSTED from a failed task** without parsing free-form logs. The UI renders:

- `[plan:step] N exhausted at T tokens (salvaged K task(s))`
- `[plan:continuation] step N started (budget B tokens)`
- `[plan:state] committed N task(s) at step M (kind)` / `[plan:state] step N committed nothing: reason`
- `[plan:budget] step N <reason>` (or `A → B tokens (reason)` when the token budget actually changes)

## 11. Failure Classification

`SynthesisError{ Kind, Step, Hint }` with kinds:

- `SynthesisOutputExhausted` — provider repeatedly cut the response; shrink floor reached with no validated state.
- `SynthesisStepIncomplete` — a bounded step produced no validated atomic result (NO STATE COMMIT).
- `SynthesisNoProgress` — a continuation advanced no new validated state.
- `SynthesisContinuationBudgetExhausted` — request budget consumed before completion.

`IsOutputExhausted(err)` distinguishes recoverable exhaustion from task failure. The
recoverable set is surfaced as a typed error (headless, invariant 14) — never a hidden wait.

## 12. Test Coverage

- Budget resolution: free-tier clamp (980) + unconstrained passthrough + low-ceiling bound.
- **No-salvage shrink-floor**: permanently-exhausted provider → typed `OUTPUT_EXHAUSTED`
  after initial + bounded shrink steps (no blind full-scope retries).
- **Salvage-then-stop merge**: 1 salvaged + 2 natural-stop tasks merge to 3, deduplicated,
  with exactly initial + 1 continuation provider calls.
- **Telemetry**: `StepStarted` / `StepExhausted` / `ContinuationScheduled` / `StateRejected`
  all emitted on the rejection trail.
- **System-prompt contract**: constrained model receives `BOUNDED OUTPUT CONTRACT`;
  unconstrained never does.
- Regression: `TestProcessFromLedgerTruncatedStream` (valid JSON + `length` → 2 tasks)
  stays green through the new continuation path; `TestProcessFromLedgerNilResponseOnRetryNoPanic`
  unaffected (no `length` finish reason).

## 13. Regression Evidence

- `go build ./...` — clean
- `go vet ./internal/modes/plan/ ./internal/events/ ./internal/ai/ ./internal/ui/` — clean
- `golangci-lint run ./internal/modes/plan/... ./internal/events/... ./internal/ai/... ./internal/ui/` — **0 issues**
- `go test ./internal/modes/plan/ ./internal/events/ ./internal/ai/` — pass
- Full suite only fails the **two pre-existing** UI acceptance tests
  (`TestAcceptanceCaseAConversationDirectResponse`, `TestAcceptanceCaseBAskMutationEscalates`)
  — verified **reproduced on clean HEAD** (stash) and therefore unrelated to this change.
  `internal/architecture` flaked once under full-suite parallelism but passes isolated and on rerun.

## 14. Risks and Limitations

- **Idempotent salvage on repeated exhaustion**: if every continuation returns the same
  exhausted content (a pathological provider), staged state stays constant and the bounded
  loop consumes the 3-step budget before committing. Bounded (3) by design; acceptable.
- **Heuristic ceilings**: `MaxOutputTokensFor` is a family heuristic; a provider that
  advertises a different ceiling than the heuristic could still truncate. The bounded-step
  path degrades gracefully in that case (typed exhaustion, smaller steps).
- **Fast-track markdown checklists**: exempt from the length branch (local 7B models truncate
  them routinely and the existing markdown salvage already tolerates that).
- `autoCloseJSON` tolerable repair still exists; it is now funneled through
  `salvageValidTasks` validation so a repaired-but-incomplete JSON only commits if it passes
  every existing gate.

## 15. Alternatives Rejected

1. **Raise max_tokens globally** — over-spends on budget, doesn't fix a provider-imposed
   ceiling, violates the constrained-budget guardrails.
2. **Full transcript replay per continuation** — re-runs the same scope; the exact defect.
3. **Second "bounded" runtime** — splits the authority surface; rejected in §4.
4. **Pre-parse JSON on the first response with more pruning** — treats the symptom; the
   budget mismatch is the root cause.

## 16. Operational Notes

- No config/flag changes; behavior is derived per model id automatically.
- The 8 new event types flow through the existing bus + UI projection (`handleDomainEvent`);
  event-sink and audit integrations remain unchanged.
- Monitor the `[plan:step] N exhausted` activity lines: sustained exhaustion on a model id is
  the signal to prefer a non-free tier.

## 17. Future Work

- Surface the typed `SynthesisError` and its `Kind` in the TUI error panel (today it is a
  plain error string with the typed kind embedded).
- Consider persisting the per-model observed ceiling (learned from `usage.completion_tokens`
  on real exhaustion) to refine the heuristic beyond family inference.
- Add a UI affordance to "continue in a larger-ceiling model" when
  `SynthesisOutputExhausted` is surfaced.

---

## FINAL DECISION: **ACCEPTED**

The bounded reasoning continuity repair is accepted. It eliminates the blind same-scope
retry for output-ceiling truncation, derives the synthesis budget from actual provider
capability, commits only validated atomic state, advances work via smaller bounded
continuation steps, publishes typed telemetry, and preserves every IZEN authority
invariant. Verified: build clean, vet clean, lint 0 issues, and the full test suite shows
no new failures (the only two failures are pre-existing on HEAD).
