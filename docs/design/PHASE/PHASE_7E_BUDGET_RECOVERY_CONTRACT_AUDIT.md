# PHASE 7E — Budget / Recovery Contract Forensic Audit

**Date:** 2026-08-20
**Scope:** Output-token budgeting, reasoning-token budgeting, artifact validation semantics, retry/recovery semantics, and autonomous-loop terminal semantics.
**Tree:** `fix/phase-7b` @ `63db000` (clean)
**Auditor role:** External adversarial architecture auditor (no production code modified; one temporary wire experiment run and removed).

---

## 1. Executive Summary

The audit set out to answer five questions: (1) how output-token budgets are
computed and forwarded to the provider, (2) whether reasoning tokens are budgeted
and billed, (3) what the artifact validation gate actually enforces, (4) what the
retry/recovery machinery actually recovers from, and (5) how the autonomous loop
terminates. The audit found **six hard findings**, one of which (F2) contradicts
the premise of the audit prompt itself, and one of which (F5) is a live,
production-visible accounting defect.

**Findings:**

| ID | Finding | Severity |
|----|---------|----------|
| F1 | The pre-fix autonomous path omitted `max_tokens` from the wire; the fix (`63db000`) forwarded `profile.MaxOutputTokens` only in the **autonomous driver** adapter. | High (fixed, but incomplete — see F2) |
| F2 | The **UI /build paths** (`runStagedBuildViaRuntime`, `runRuntimeTaskRequest`, `runRuntimePrompt`, `executeAutonomyViaRuntime`) still omit `MaxOutputTokens`, so the strategy budget is **never on the wire** for staged builds, per-task builds, and free-form build prompts. | High — live defect |
| F3 | The artifact validation gate runs `ValidateContent(target, data, 0)` and checks **only** `gate.Passed`; the policy's `DecisionRetry` / `Directive` and the retry budget (`maxRetryAttempts=3`) are **dead code** in the mutation path. Invalid artifacts → `ErrArtifactRejected` → `FailurePermanent` → `LoopAbort`. | High |
| F4 | `ValidateContent` is called from **exactly one** call site (`artifactGate`), always with `attempts=0`, so `StandardFailurePolicy` can never emit anything but a first-pass verdict. | High |
| F5 | The executor **never checks `finish_reason`** on the mutation path. A `finish_reason="length"` truncation is invisible; the (truncated) artifact proceeds to the deterministic gate and can produce a **false `ErrArtifactRejected`** with a misleading reason. | High — production-visible |
| F6 | **Reasoning tokens are never requested** (no production call site sets `ai.Request.Reasoning`) and **never charged or tracked as a separate line** on the mutation path; they are silently embedded in `completion_tokens`. The `usage.go` comment claiming reasoning is "internal model deliberation, not billed output content" **contradicts** OpenRouter billing. | Medium — cost/accounting |
| F7 | The autonomous **approval path** classifies `apply_failed`/`verify_failed` after approval as `FailurePermanent` (`terminateAbort`), contradicting both the executor's own event (`FailureRecoverable`) and `ClassifyOutcome`. | Medium |
| F8 | `OutcomeNoArtifact` → `FailureTransient` → `Retry` can re-invoke the model up to 3× for deterministic read-only strategies that legitimately produce no artifact. | Low |

The audit concludes with a **final architectural verdict** (Section 15) and a
strict PROVEN / UNPROVEN / REQUIRES DESIGN DECISION classification.

---

## 2. Evidence Matrix

Every claim below carries a source/wire evidence chain. `req_forensics` =
`internal/execution/request_forensics_test.go`; `usage_forensics` =
`internal/execution/usage_forensics_test.go`; `phase7_fx` =
`internal/runtime/autonomy/forensics_phase7_test.go`.

| # | Claim | Evidence chain | Status |
|---|-------|----------------|--------|
| E1 | `MaxTokens` is `int` with `json:"max_tokens,omitempty"`; `0` omits the field | `internal/providers/openrouter.go` (`openrouterRequest.MaxTokens`); `req_forensics` PRE-FIX body shows no `max_tokens` key | PROVEN |
| E2 | `Reasoning` field is omitted unless `ai.Request.Reasoning != nil` | `internal/providers/openrouter.go` `reasoningFor(req)` returns nil for nil config; `req_forensics` shows `Reasoning: <nil>` and no `reasoning` key; all 9 production `ai.Request{}` construction sites leave `Reasoning` nil | PROVEN |
| E3 | Autonomous driver path forwards `MaxOutputTokens: profile.MaxOutputTokens` | `internal/runtime/autonomy/adapter.go:118` (introduced by `63db000`) | PROVEN |
| E4 | IntentGateway (/build `Gate`) forwards `MaxOutputTokens: profile.MaxOutputTokens` | `internal/execution/intent.go:107` | PROVEN |
| E5 | UI cutover /build sites omit `MaxOutputTokens` | `internal/ui/runtime_cutover.go:146,229,260,287` (ExecuteRequest construction); callers `internal/ui/commands.go:313,453,781,2668,4302`, `update.go:797`, `autonomy_target.go:169` | PROVEN |
| E6 | Executor sends `req.MaxOutputTokens` on the wire, not the merged profile value | `internal/execution/executor.go:1043-1047` (`invokeMutation`), `:1188-1192` (`invokeReadOnly`); `selectStrategy` merges req→profile one-way (`if req.MaxOutputTokens > 0`) | PROVEN |
| E7 | `artifactGate` calls `ValidateContent(target, data, 0)`; checks only `gate.Passed`; ignores `Decision`/`Directive` | `internal/execution/executor.go:1124-1130` | PROVEN |
| E8 | `ValidateContent` has exactly one caller (`artifactGate`), always `attempts=0` | repo-wide grep (single match outside policy tests) | PROVEN |
| E9 | Syntax errors in HTML produce `DecisionRetry` in `StandardFailurePolicy` | `pkg/capability/policy/standard.go` (`isSyntaxError`, `maxRetryAttempts=3`); `pkg/capability/policy/policy.go` (`DecisionRetry`) | PROVEN |
| E10 | `ErrArtifactRejected` → `OutcomeArtifactRejected` → `FailurePermanent` → `LoopAbort` | `internal/execution/mutation.go` (`OutcomeArtifactRejected`); `internal/execution/executor.go:614-625` (`FailExecution(events.FailurePermanent)`); `internal/autonomy/runtime_loop.go` (`ClassifyOutcome`); `internal/runtime/autonomy/driver.go:494-496` (`decideDefault` hardcodes LoopAbort) | PROVEN |
| E11 | Original design contract re-prompts on failed validation | `pkg/app/pipeline.go:316-347` (validation gate re-prompts with rejection reasons; `p.maxAttempts` cap); `pkg/engine/decision/engine.go` (`DirectiveRetry`); `internal/modes/plan/engine.go:970` (`maxSilentRetries := 2`) — pattern still live in plan mode | PROVEN |
| E12 | Executor never inspects `finish_reason` on the mutation path | `internal/execution/executor.go` (no `FinishReason` read); only `ui/stream.go:366` and `internal/modes/plan/engine.go:525` check `=="length"` | PROVEN |
| E13 | Repro usage ledger | `usage_forensics`: prompt 2181, completion 5883, reasoning 5000, total 7064, Known=true | PROVEN |
| E14 | `completion_tokens` includes `completion_tokens_details.reasoning_tokens` on the wire | `internal/providers/openrouter.go` `openrouterUsage`; OpenRouter docs | PROVEN |
| E15 | Reasoning tokens are billed as output tokens | OpenRouter docs (`reasoning.max_tokens` reference; billing section) | PROVEN (vendor doc) |
| E16 | Approval adds **zero** new provider invocations | `phase7_fx` `TestProviderInvocationCountAfterSingleApproval` | PROVEN |
| E17 | 429 retried ≤3× inside the provider (same logical invocation); 400-with-reasoning retried once | `internal/providers/openrouter.go` (`openRouterMaxRateLimitRetries=3`, backoff; 400-reasoning strip+retry) | PROVEN |
| E18 | The deterministic micro-fix verification gate does **not** re-invoke the model | `internal/execution/patch.go:707-779` (local verifier, restore-from-shadow-backup, no provider call) | PROVEN |
| E19 | Approval-path `apply_failed`/`verify_failed` are classified permanent by the driver | `internal/runtime/autonomy/driver.go` (`approvalFailureOutcome`, `terminateAbort(FailurePermanent)`); conflicts with `executor.go:801` (`g.FailExecution(events.FailureRecoverable,...)`) | PROVEN |
| E20 | `OutcomeNoArtifact` → transient → Retry | `internal/autonomy/runtime_loop.go` (`ClassifyOutcome` transient list) | PROVEN |
| E21 | Provider-side behavior under `max_tokens=1024` vs explicit reasoning budget (exact `finish_reason`, token split, visible length) | Requires live API key; **not** deterministically provable in this environment | UNPROVEN |
| E22 | The three wire variants A/B/C serialize differently | Temporary isolated experiment using the real `buildRequest` (run and removed): A omits reasoning, B emits `"reasoning":{"max_tokens":2000}`, C emits `"reasoning":{"effort":"none"}` | PROVEN |

---

## 3. Provider Wire Contract

### 3.1 Schema (OpenRouter)

`internal/providers/openrouter.go`:

- `openrouterRequest.MaxTokens` is `int` tagged `json:"max_tokens,omitempty"`.
  A zero value **omits** the field, which tells the provider to use its own
  default output limit — **not** a bound of 0.
- `Reasoning *ReasoningConfig` tagged `json:"reasoning,omitempty"`.
  `reasoningFor(req)` returns `nil` when `req.Reasoning == nil`, so the key is
  never emitted unless explicitly configured.
- Normalized `finish_reason` follows the OpenAI-style enum; a truncation at the
  token ceiling is reported as `finish_reason == "length"`.

### 3.2 Budgets computed by the strategy layer

`internal/execution/strategy/selector.go:587,643` (`withBudgets`, `outputForArtifact`):

| Strategy | MaxOutput (tokens) |
|----------|--------------------|
| `replace_block` low | 1024 |
| `replace_block` med | 2048 |
| `replace_block` high | 3072 |
| `create_file` | 4096 |
| `plan` | 1536 |
| `investigation` | 2048 |
| `explanation` | 1024 |
| `response` | 512 |

These land in `InvocationContract.MaxOutput` (`strategy/invocation.go`) and are
mirrored into the runtime `profile.MaxOutputTokens`.

### 3.3 How the budget reaches the wire — the seam

- `ExecutorAdapter.Execute` (autonomy, post-fix) → `ExecuteRequest{MaxOutputTokens: profile.MaxOutputTokens}` → `executor.invokeMutation` → `ai.Request.MaxTokens = req.MaxOutputTokens` → wire. **Correct.**
- `IntentGateway.Gate` (/build `Gate`) → `ExecuteRequest{MaxOutputTokens: profile.MaxOutputTokens}` → same path. **Correct.**
- UI cutover sites → `ExecuteRequest{}` with **no** `MaxOutputTokens` → `req.MaxOutputTokens == 0` → `ai.Request.MaxTokens = 0` → field omitted → provider default applies. **Incorrect.**

---

## 4. Reasoning Budget Analysis

### 4.1 Proven facts

- **No production call site sets `ai.Request.Reasoning`.** All 9 construction
  sites (`executor.go:1043,1188`; `toolrunner.go:202`; `dispatcher.go:169`;
  `compose.go:715`; `ui/agents.go:764`; `modes/plan/engine.go:829,885,1920`;
  `ui/commands.go:3952`; `cmd/izen/runtime.go:57,82`; `ui/stream.go:229`) leave
  `Reasoning` nil. Therefore `reasoningFor` always returns nil and the
  `reasoning` key is **never** on the wire. (`req_forensics` shows `Reasoning: <nil>`.)
- The strategy layer **computes** a reasoning budget (`InvocationContract.ReasoningBudget`,
  `selector.reasoningForComplexity`) but nothing forwards it to `ai.Request.Reasoning`.
- When the model does reason (vendor default), `openrouterUsage` reports it
  inside `completion_tokens` via `completion_tokens_details.reasoning_tokens`.
  Repro ledger (E13): completion 5883 of which reasoning 5000 — **85% of billed
  completion tokens are reasoning**, on a request that carried no reasoning
  directive at all.

### 4.2 Wire variants (deterministic experiment)

Using the real production builder (`buildRequest`) with `max_tokens=1024`:

- **A (production):** `{"model":...,"max_tokens":1024,"stream":true,"stream_options":{"include_usage":true}}` — no reasoning key.
- **B (explicit budget):** `...,"max_tokens":1024,"stream":true,"stream_options":{"include_usage":true},"reasoning":{"max_tokens":2000}}`
- **C (disabled):** `...,"reasoning":{"effort":"none"}}`

Note B is **invalid for Anthropic-family models** on OpenRouter: the vendor
requires `max_tokens` to be strictly greater than the reasoning budget
(`budget_tokens = max(min(max_tokens * effort_ratio, 128000), 1024)`). A `reasoning.max_tokens=2000`
with `max_tokens=1024` would be rejected or coerced — a configuration hazard
that does not exist today only because B is never emitted.

### 4.3 Billing truth

OpenRouter bills reasoning tokens as output tokens. The comment in
`internal/providers/usage.go` ("Reasoning is internal model deliberation, not
billed output content") is **factually wrong** against the vendor contract and
leads operators to under-account the true output cost. Izen's own accounting
records them inside `completion_tokens`, which is *correct*; the comment is the
defect.

---

## 5. Autonomous vs /build Request Diff

Request-construction diff at the **wire** level (verified via `req_forensics`
and the temporary experiment):

| Path | Entry | Sets `MaxOutputTokens`? | `max_tokens` on wire? |
|------|-------|------------------------|----------------------|
| Autonomous driver | `internal/runtime/autonomy/adapter.go:118` (`Execute`) | Yes (post-fix) | Yes |
| /build `Gate` | `internal/execution/intent.go:107` | Yes | Yes |
| UI staged-build | `internal/ui/runtime_cutover.go:146` (`runStagedBuildViaRuntime`) | **No** | **No** |
| UI per-task build | `internal/ui/runtime_cutover.go:229` (`runRuntimeTaskRequest`) | **No** | **No** |
| UI free-form build prompt | `internal/ui/runtime_cutover.go:260` (`runRuntimePrompt`) | **No** | **No** |
| UI target-selection fallback | `internal/ui/runtime_cutover.go:287` (`executeAutonomyViaRuntime`) | **No** | **No** |

All four UI paths are live: `commands.go:313,453,781,2668,4302`,
`update.go:797-800`, `autonomy_target.go:169`.

**Root cause of the diff:** `selectStrategy` (executor.go) merges the request
into the profile one-directionally (`if req.MaxOutputTokens > 0 { profile.MaxOutputTokens = req.MaxOutputTokens }`),
and both `invokeMutation`/`invokeReadOnly` read `req.MaxOutputTokens`, **not** the
merged profile. The executor's own strategy contract is therefore dead on the
wire for any caller that does not set `req.MaxOutputTokens` — precisely the UI
cutover paths. The fix must be applied at the executor seam, not at every caller.

---

## 6. Artifact Validation Contract

### 6.1 What the gate runs

`artifactGate` (`internal/execution/executor.go:1124-1130`):

```go
gate := p.gate.ValidateContent(target, []byte(modified), 0)  // attempts hardcoded to 0
if !gate.Passed {
    return fmt.Errorf("%w: %v", ErrArtifactRejected, gate.Error)
}
```

`ValidateContent` (only caller: `artifactGate`) resolves a deterministic
capability validator (e.g. `pkg/capability/validator/html.go` — real HTML5 parse +
well-formedness scan; `json.go` — `json.Valid`) and feeds the error into
`StandardFailurePolicy`. For syntax degradation the policy computes
`DecisionRetry` (`standard.go`, `maxRetryAttempts=3`).

### 6.2 What the gate enforces — and what it discards

`ArtifactGateResult` carries `Passed`, `Decision`, `Error`, `Directive`
(`internal/execution/artifact.go`). The mutation path reads **only `Passed`**.
`DecisionRetry` and `Directive` are **dead code**; `attempts` is always `0`, so
the retry budget is never consulted.

### 6.3 Terminal chain

`ErrArtifactRejected` → `OutcomeArtifactRejected` (`mutation.go`) →
`FailExecution(events.FailurePermanent, ...)` (`executor.go:614-625`) →
`EventExecutionFailed(FailurePermanent)` (`graph.go:487-504`) →
`ClassifyOutcome` → `FailurePermanent` → `RecoverFailure` → `Abort`
(`runtime_loop.go`) → `decideDefault` hardcodes `OutcomeArtifactRejected → LoopAbort`
(`driver.go:494-496`).

**The loop aborts on the first invalid artifact.** There is no re-prompt, no
repair, no narrower re-attempt.

### 6.4 The design contract it diverged from

The original V3 pipeline re-prompted the model on validation failure: `pkg/app/pipeline.go:316-347`
("validation gate (failed artifacts re-prompt with rejection reasons)"), capped
by `WithMaxAttempts`/`WithMaxRepairs`; `pkg/engine/decision/engine.go` exposes
`DirectiveRetry`; and plan mode still runs `maxSilentRetries := 2` with
prompt-augmentation retries (`modes/plan/engine.go:970-984`). The runtime
executor's permanent-abort is therefore a **policy regression** relative to the
codebase's own recovery philosophy — likely an accidental consequence of wiring
the policy only as a verdict function and never as a loop.

---

## 7. Failure Classification Matrix

`ClassifyOutcome` (`internal/autonomy/runtime_loop.go`):

| Outcome | Class | Recovery action |
|---------|-------|-----------------|
| `NoArtifact`, `ArtifactProduced`, `Changed`, `Created`, `NoChange`, `Completed`, `Skipped`, `PendingApproval` | **Transient** | Retry (≤ `MaxAttempts`, default 3) |
| `Failed`, `PatchGenerationFailed`, `PatchFailed`, `ApplyFailed`, `VerifyFailed` | **Recoverable** | Repair (≤ `MaxRecoveryCycles`, default 2) |
| `ArtifactRejected`, `Cancelled`, `Rejected` | **Permanent** | Abort |

`RecoverFailure`: permanent→Abort; transient→Retry; recoverable→Repair;
bounds exhausted→AskHuman.

**Matrix defects:**

1. **`OutcomeArtifactRejected` is permanent** even though the failure is a
   deterministic, often-trivial syntax defect (`unterminated <script>`). This is
   the highest-leverage recovery defect (Section 6).
2. **Approval-path contradiction:** `ResumeApprove` → apply/verify failure →
   `approvalFailureOutcome` → `terminateAbort(FailurePermanent)` — yet the
   executor emits `FailureRecoverable` for the identical failure at `executor.go:801`,
   and `ClassifyOutcome` says recoverable. Two adjacent layers disagree on the
   same event.
3. **`OutcomeNoArtifact` → transient → Retry:** a deterministic read-only
   strategy (e.g. `investigation` with no extractable artifact) is classified
   transient and re-invokes the model up to 3× — the retry loop is not
   conditioned on whether the strategy is deterministic.

---

## 8. Recovery / Retry Ownership

| Concern | Owner | Bound | Evidence |
|---------|-------|-------|----------|
| HTTP 429 rate limit | Provider (`openrouter.go`) | 3 retries, 1s/2s/4s backoff, same invocation | E17 |
| 400 with reasoning directive | Provider | 1 retry with reasoning stripped | E17 |
| JSON/command synthesis failure (plan mode) | Plan engine | 2 silent retries, augmented prompt | `modes/plan/engine.go:970` |
| Deterministic micro-fix verification gate | Patch apply | 0 model calls (restore-from-backup only) | E18 |
| Execution failure (`recoverable`) | Autonomous loop | Repair ≤ 2 cycles | `runtime_loop.go` |
| Execution failure (`transient`) | Autonomous loop | Retry ≤ 3 | `runtime_loop.go` |
| Artifact validation failure | **Nobody** | 0 — permanent abort | F3/F4 |
| Truncated output (`finish_reason=length`) | **Nobody** | 0 — invisible | F5 |

**Ownership gap:** artifact-validation and truncation — the two failures most
likely to be *model-caused* — have no recovery owner at all, while transient
HTTP conditions (429) have three. Recovery ownership is inverted relative to
failure cause.

---

## 9. Execution Truth Ledger

Authoritative repro ledger (`internal/execution/usage_forensics_test.go`):

| Metric | Value |
|--------|-------|
| Prompt tokens | 2181 |
| Completion tokens | 5883 |
| Reasoning tokens (inside completion) | 5000 |
| Total tokens | 7064 |
| Usage known | true |

Truth rules verified:

- `invokeStream` falls back to `Execute` **only** when the stream returns nil/
  error before any bytes; never double-bills (E-tested).
- `streamUsageTracker` counts `httpAttempts`/`rateLimitedRetries` separately from
  logical invocations — a stream with 3 rate-limit retries is **one** logical
  invocation in the ledger (correct), but the billed cost may exceed the ledger
  if the gateway charges rejected attempts (UNPROVEN, Section 12).
- **Truncation is invisible:** `finish_reason` is never read on the mutation
  path. A `"length"` finish is recorded as a successful completion with a
  truncated artifact; the truncated artifact then fails the deterministic gate
  and is reported as `artifact_rejected` with a syntax-error reason — misdirecting
  the operator away from the true cause (output ceiling).
- **Estimated usage can overwrite authoritative:** `if u := usageUp.Usage(); u.Known { usage = u }`
  — the estimated tracker reports `Known=true`, so a stream-interruption estimate
  can replace a provider-authoritative figure with no `Estimated` flag on the
  recorded `ModelInvocation`.

---

## 10. Hidden Invocation Audit

Every model-relevant re-entry point, counted against the ledger:

| Event | Model invocations | Ledger visibility |
|-------|-------------------|-------------------|
| 429 retry | 0 (same invocation) | `httpAttempts` counter only |
| 400-reasoning retry | 0 | `httpAttempts` counter only |
| Repair cycle (recoverable) | +1 per cycle (≤2) | New logical invocation (recorded) |
| Retry cycle (transient) | +1 per retry (≤3) | New logical invocation (recorded) |
| Plan synthesis retries | +1 per retry (≤2) | New invocation (plan ledger) |
| Micro-fix verification gate | 0 | Deterministic; no cost |
| Approval resume | **0** (`phase7_fx` proven) | — |
| `adapter.Execute` `SelectStrategy` calls | 0 (deterministic) | — |

**Verdict:** no *logical* invocation is hidden. The only genuinely invisible
costs are (a) gateway-side billing for rate-limited/rejected HTTP attempts and
(b) reasoning tokens, which are visible in `completion_tokens` but never
separated on the mutation-path ledger.

---

## 11. Root-Cause Graph

```
                            +-------------------------------------+
                            | F1: pre-fix autonomous path omitted  |
                            |     max_tokens on wire               |
                            +------------------+------------------+
                                               |
                          fix 63db000 only patches the AUTONOMY ADAPTER
                                               |
                    +--------------------------+--------------------------+
                    v                                                     v
       +---------------------------+                +--------------------------------------+
       | F2: UI cutover /build     |                | Executor seam: invokeMutation/ReadOnly |
       |     paths omit MaxOutput  |   (same root)  | read req.MaxOutputTokens, NOT the      |
       |     (4 live call sites)   | --------------- | merged profile; selectStrategy merge   |
       +---------------------------+                | is one-way and dead on the wire       |
                                                    +--------------------------------------+
                                                    |
                     +------------------------------+------------------------------+
                     v                                                             v
       +----------------------------------+                 +---------------------------------------------+
       | F5: finish_reason never checked  |                 | F3/F4: artifactGate ignores DecisionRetry    |
       |     on mutation path; truncation |                 |        & attempts; only caller of            |
       |     invisibly fails the gate     | --------------->|        ValidateContent, attempts always 0     |
       +----------------------------------+                 +----------------------+----------------------+
                                                                                   |
                                                    +------------------------------+--------------------------+
                                                    v                                                     v
                                            OutcomeArtifactRejected                                    DecisionRetry (computed,
                                            -> FailurePermanent                                          never consumed) = dead code
                                            -> LoopAbort                                                  in the mutation path
                                                    |
                       +----------------------------+----------------------------+
                       v                                                         v
        +--------------------------------+                     +------------------------------------------+
        | F7: approval path classifies   |                     | Original design (pkg/app:316-347,         |
        |     apply/verify failure as    |                     | plan engine:970) re-prompts on failure;   |
        |     permanent; contradicts     |                     | runtime divergence = policy regression     |
        |     executor FailureRecoverable|                     +------------------------------------------+
        +--------------------------------+

Cost layer: F6 (reasoning never requested, never separated; usage.go comment
contradicts billing) and F8 (NoArtifact->transient->retry) attach to the same
executor seam.
```

The single architectural root is the **executor seam**: budget merge direction,
`finish_reason` handling, and the validation-gate decision all hang off the same
three functions (`invokeMutation`, `invokeReadOnly`, `artifactGate`).

---

## 12. Remaining Risks

| Risk | Classification | Impact |
|------|----------------|--------|
| Live provider behavior of A vs B vs C under `max_tokens=1024` on a specific model — exact `finish_reason`, token split, visible-length floor, whether a `"length"` finish follows a valid partial artifact | UNPROVEN (no API key in environment) | If the model truncates before a syntactically valid artifact, the gate may pass a truncated file silently; if it truncates mid-token, the gate fails and the loop aborts with a misleading reason |
| Whether the gateway bills rejected/rate-limited HTTP attempts (400-reasoning retry, 429s) | UNPROVEN (gateway-side) | Ledger understates true spend |
| `reasoning.max_tokens` interaction with Anthropic-family models (max_tokens must exceed reasoning budget) | UNPROVEN live; PROVEN as a config hazard from vendor formula | Emitting B naively would produce 400s or silent coercion |
| Estimated-usage overwrite of authoritative usage (no `Estimated` flag on recorded invocation) | PROVEN code path; live frequency UNPROVEN | Misleading ledger after stream interruption |
| Deterministic read-only strategies classified transient (≤3 retries) | PROVEN | Wasted billable invocations on no-op investigations |

---

## 13. Recommended Fixes

Ordered by leverage (audit-scope recommendations only — **no code was changed**):

1. **Executor seam fix (kills F2, F6 budget half, F8):** in `invokeMutation`/`invokeReadOnly`, when `req.MaxOutputTokens == 0`, use `profile.MaxOutputTokens`; when `req.Reasoning` is nil and `profile` carries a reasoning budget, forward it. This makes the strategy contract authoritative regardless of caller, fixing all UI cutover paths at one point.
2. **`finish_reason="length"` handling (kills F5):** read the normalized finish reason on the mutation path; treat `"length"` as a distinct outcome (`OutcomeTruncated`) with its own classification and a narrow re-attempt (higher budget or `create_file` fallback), never as a syntax failure.
3. **Artifact-gate retry (kills F3/F4):** honor `gate.Decision` in `artifactGate`; on `DecisionRetry` with remaining budget, re-run the *same* deterministic extraction/validation with the gate error appended to the prompt (the `pkg/app:316-347` contract) before any permanent classification.
4. **Unify approval-path classification (kills F7):** make the driver's `approvalFailureOutcome` emit/respect the executor's `FailureRecoverable` classification instead of `terminateAbort(FailurePermanent)`.
5. **Correct the usage.go comment (kills the misleading half of F6):** document that reasoning tokens are billed output tokens and are inside `completion_tokens`.
6. **Add an `Estimated` flag** to the recorded `ModelInvocation` when the usage estimate replaces authoritative usage.

---

## 14. Tests Required

All are isolated additions; none modify production code.

1. **Wire-omission regression:** assert every `ExecuteRequest` constructor in `runtime_cutover.go` yields a wire body containing `max_tokens` once the executor seam forwards the profile (mirror of `req_forensics`).
2. **Gate-decision consumption:** stub `ValidateContent` to return `{Passed:false, Decision:DecisionRetry}` and assert `artifactGate` consumes the directive (re-prompt or recovery) instead of `ErrArtifactRejected` once F3 lands.
3. **Truncation path:** assert a stream terminating with `finish_reason="length"` produces `OutcomeTruncated` (not gate-failure) and that the ledger records the finish reason.
4. **Approval-path classification parity:** assert `approvalFailureOutcome` and `executor.go:801` agree on classification for `apply_failed`/`verify_failed`.
5. **Estimated-usage flag:** assert a stream-interruption estimate is recorded with `Estimated=true` and cannot overwrite a provider-authoritative figure.
6. **Live A/B/C harness (needs API key):** execute A/B/C against a reasoning-capable model; capture `finish_reason`, `completion_tokens`, `completion_tokens_details.reasoning_tokens`, visible output length, and total cost for each. Label the harness `//go:build audit_live` so it never runs in CI.

---

## 15. Final Architectural Verdict

The autonomous runtime's **budget/recovery contract is structurally sound in
intent but broken at the executor seam**. The audit found no double-billing, no
hidden logical invocations, and a working post-fix autonomous wire; but it found
a live /build budget omission (F2), a validation gate whose retry decision is
dead code (F3/F4), a truncation signal that is never read (F5), a recovery
classification hierarchy that is inverted for the two most common *model-caused*
failures (Section 8), and an approval-path classification contradiction (F7).

The architectural picture: the codebase knows how to recover (plan-mode retries,
the original V3 re-prompt pipeline, the deterministic micro-fix gate), but the
runtime executor was wired to the policy layer as a *verdict*, not a *loop*, and
the budget layer was wired to `req` instead of the strategy profile. The
contracts are all present; the executor seam silently drops the parts that make
them live.

---

## Final Classification

**PROVEN**
1. Pre-fix autonomous wire omitted `max_tokens`; fix `63db000` forwards it only via the autonomy adapter (`E1, E3`).
2. UI cutover /build paths (4 live sites) omit `MaxOutputTokens`; `req.MaxOutputTokens` (not the merged profile) drives the wire — the strategy budget is dead for those callers (`E5, E6`).
3. `artifactGate` checks only `gate.Passed`; `DecisionRetry`/`Directive`/`attempts` are dead in the mutation path; `ValidateContent` has exactly one caller with `attempts=0` (`E7, E8, E9`).
4. Invalid artifact → `OutcomeArtifactRejected` → `FailurePermanent` → `LoopAbort` (`E10`).
5. Executor never reads `finish_reason` on the mutation path; only legacy/plan paths check `"length"` (`E12`).
6. No production call site sets `ai.Request.Reasoning`; reasoning field never on the wire; reasoning tokens appear inside `completion_tokens` (repro: 5000/5883) (`E2, E13, E14`).
7. Approval adds zero provider invocations; recovery loops re-invoke only via Repair/Retry cycles (`E16, E17`).
8. Approval-path apply/verify failure is classified permanent by the driver but recoverable by the executor (`E19`).
9. `OutcomeNoArtifact` → transient → Retry can re-invoke ≤3× (`E20`).
10. Wire variants A/B/C serialize exactly as captured (real builder, isolated experiment) (`E22`).

**UNPROVEN**
1. Live provider behavior of A vs B vs C under `max_tokens=1024` on a specific model — exact `finish_reason`, token split, visible-length floor, partial-artifact truncation behavior (`E21`).
2. Whether the gateway bills rejected/rate-limited HTTP attempts (400-reasoning retry, 429 retries).
3. Whether the estimated-usage overwrite occurs in practice (code path PROVEN; frequency UNPROVEN).
4. Vendor enforcement of the `max_tokens > reasoning budget` constraint for Anthropic-family models on the live gateway.

**REQUIRES DESIGN DECISION**
1. Should artifact rejection re-prompt/repair (per the original `pkg/app:316-347` contract and plan-mode precedent) instead of permanent abort?
2. Should `finish_reason="length"` be detected and recovered as a truncation outcome distinct from syntax failure?
3. Should the executor forward `profile.MaxOutputTokens` and `profile` reasoning budget when `req` omits them (making the strategy layer authoritative)?
4. Should the approval-path apply/verify failure respect the executor's `FailureRecoverable` classification?
5. Should a reasoning budget be explicitly emitted via `reasoning.max_tokens` for reasoning-capable models — and if so, under what constraint (requires the live experiment to answer)?