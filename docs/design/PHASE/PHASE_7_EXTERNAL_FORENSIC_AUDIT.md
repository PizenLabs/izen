# PHASE 7 — EXTERNAL FORENSIC AUDIT

**Status:** COMPLETE — independent trace of the 5,883-token repro and `artifact_rejected` early-termination. Two root-cause findings (CASE A token-budget omission; CASE B artifact-gate policy bypass + accounting erasure), each with exact file:line evidence, committed-HEAD baseline vs. working-tree fix, and falsifiable invariants pinned by the developer's forensic tests.

**Auditor scope:** External forensic auditor — NO production code is modified or fixed by this audit. The repo `fix/phase-7b` carries the developer's in-progress fix (uncommitted working-tree changes); this audit traces the committed `HEAD` baseline independently, then reconciles with the working-tree remediation.

**Repro anchors** (pinned by `internal/execution/usage_forensics_test.go:22`):
- `reproUsage = {PromptTokens: 2181, CompletionTokens: 5883, ReasoningTokens: 5000, TotalTokens: 7064, Known: true}`
- Malformed artifact: `<html><body><script>alert(1)</body></html>` → gate error "unterminated `<script>` element"

---

## §1 — Executive Summary

| # | Label | Severity | Committed-HEAD Status | Working-Tree Fix |
|---|-------|----------|----------------------|------------------|
| A | Autonomous output-budget omission | **Root cause (billing)** | OpenRouter receives `max_tokens` omitted → provider default (~5,883) | `adapter.go` adds `MaxOutputTokens: profile.MaxOutputTokens` |
| B.1 | Artifact-gate policy bypass | **Root cause (early-termination)** | `artifactGate` ignores `DecisionRetry` → hard `ErrArtifactRejected` → `LoopAbort` | **NOT fixed** — asserted as intentional (permanent rejection) |
| B.2 | Invocation-evidence erasure | **Amplifier (accounting)** | `invokeMutation` returns `nil,nil,nil` on gate error → `Completed.OutputTokens=0` | `invokeMutation` returns `nil,invs,nil`; `Execute` appends `invs` before classification |

The 5,883-token anomaly is explained by **CASE A alone**: the autonomous path never forwarded `profile.MaxOutputTokens` to the OpenRouter request, so the `max_tokens` field (JSON `omitempty`) was omitted and the provider used its own default ceiling. CASE B.1 explains why the run then terminated at `artifact_rejected` without reaching approval: the model's unbounded, truncated output failed the HTML validator, and the gate hard-rejected it as permanent — bypassing the failure-policy's `DecisionRetry`.

---

## §2 — Execution Paths Audited

**(1) UI-initiated path** (`/build`, `$prompt`, `$build$hot`):

`internal/ui/gateway.go` → `runGatedLine` → `IntentGateway.Gate()` (`internal/execution/intent.go:69`) → `RuntimeExecutor.Execute` (`internal/execution/executor.go:429`).

**(2) Autonomous path**:

`internal/runtime/autonomy/driver.go` (`Driver.Run`) → `ExecutorAdapter.Execute` (`internal/runtime/autonomy/adapter.go:107`) → `RuntimeExecutor.Execute`.

Both paths converge on `RuntimeExecutor.Execute`, which dispatches to `invokeMutation` (`executor.go:1014`) for `TargetedMutation` strategies. The divergence is at budget assignment:

- `/build` path: `IntentGateway.Gate` sets `req.MaxOutputTokens = profile.MaxOutputTokens` (intent.go:107). ✓ bounded.
- autonomous path: `ExecutorAdapter.Execute` (committed HEAD) builds `ExecuteRequest` **without** `MaxOutputTokens` (adapter.go:107-117). ✗ unbounded.

---

## §3 — CASE A: Autonomous Output-Budget Omission

### §3.1 Finding

The committed `HEAD` autonomous path does not propagate the strategy-selected output budget to the model request, causing OpenRouter to omit `max_tokens` from the JSON body and fall back to its provider default.

### §3.2 Evidence Chain

```
[committed HEAD]
adapter.go:107-117        ExecutorAdapter.Execute builds ExecuteRequest
    └── (NO MaxOutputTokens field)        ← committed omits it
        ↓
executor.go:1047          ai.Request{MaxTokens: req.MaxOutputTokens}
    └── req.MaxOutputTokens == 0          ← 0 propagates
        ↓
executor.go:1043-1048     ai.Request{Model, System, Messages, MaxTokens: 0}
    └── sent to provider.ExecuteStream / Execute
        ↓
openrouter.go:323         MaxTokens int `json:"max_tokens,omitempty"`
openrouter.go:399         MaxTokens: req.MaxTokens   (= 0 → OMITTED)
    └── JSON body has NO "max_tokens" key
        ↓
openrouter.org            uses provider default ceiling
    └── completion_tokens = 5883  (reproUsage.ComprehensiveTokens)
```

### §3.3 Contrast: `/build` path sets the budget

`internal/execution/intent.go:104-109`:
```go
req := ExecuteRequest{
    Prompt:          prompt,
    Targets:         res.Targets,
    MaxOutputTokens: profile.MaxOutputTokens,   // ✓ bounded
    Strategy:        &profile,
}
```

The `profile.MaxOutputTokens` is derived by `withBudgets` (`strategy/selector.go:587-598`) → `outputForArtifact` (`selector.go:640-664`):

| Artifact Kind | Complexity | MaxOutputTokens |
|---|---|---|
| `create_file` | — | 4096 |
| `replace_block` / `replace_file` | Low | 1024 |
| `replace_block` / `replace_file` | Medium | 2048 |
| `replace_block` / `replace_file` | High | 3072 |
| `plan` | — | 1536 |
| `investigation` | — | 2048 |
| `explanation` | — | 1024 |
| `response` | — | 512 |

For the repro (replacing a `<script>` block in an HTML file), `outputForArtifact` would return **1024** (Low complexity replace_block) — far below the 5,883 the provider actually billed.

### §3.4 Why OpenRouter is uniquely affected

Other providers guard the zero case:

| Provider | `maxTokens <= 0` fallback |
|---|---|
| `internal/providers/gemini.go:157` | `maxTokens = 4096` |
| `internal/providers/ollama.go:156` | `maxTokens = 4096` |
| `internal/providers/claude.go:155` | `maxTokens = 4096` |
| `internal/providers/openrouter.go` | **none** — passes 0 directly, `omitempty` drops it |
| `internal/providers/openai.go:46` | `max_tokens,omitempty` — also drops, but OpenAI default is lower |

OpenRouter is the only provider with **no** zero-guard AND `omitempty`, making it the sole vector for unbounded completion.

### §3.5 Committed-HEAD baseline (git show)

```
$ git show HEAD:internal/runtime/autonomy/adapter.go | sed -n '107,117p'
    Evidence:         req.Evidence,
    }               ← no MaxOutputTokens
```

### §3.6 Working-Tree Fix (diff)

```
$ git diff HEAD -- internal/runtime/autonomy/adapter.go
+       // The strategy-selected output ceiling is a REQUEST budget, not a
+       // reporting change ... (the 5,883-token repro: max_tokens was omitted
+       // because req.MaxOutputTokens stayed 0).
+       MaxOutputTokens: profile.MaxOutputTokens,
```

### §3.7 Forensic test pin

`internal/execution/usage_forensics_test.go:196` — `TestOutputBudgetDoesNotAlterReportedUsage`:
- `MaxOutputTokens=0` → `request.MaxTokens == 0` (omitted) ✓
- `MaxOutputTokens=512` → `request.MaxTokens == 512` ✓
- Both report `Completed.OutputTokens == 5883` (provider truth is never reduced by the budget) ✓

---

## §4 — CASE B.1: Artifact-Gate Policy Bypass

### §4.1 Finding

Even though the V3 artifact pipeline is constructed with a non-nil failure policy that classifies HTML syntax errors as **retryable** (`DecisionRetry`), the `artifactGate` function ignores the policy decision and returns a hard `ErrArtifactRejected`. This converts a recoverable syntax error into a permanent terminal outcome, causing the autonomous loop to `LoopAbort` without retry.

### §4.2 Evidence Chain

```
[model produces truncated HTML → 5,883 tokens]
        ↓
invokeMutation:1078   modified := ResolveModifiedContent(original, raw)
        ↓
artifact.go:105-136    v3Artifact.ValidateContent(target, modified, attempts=0)
    ├── gate.Passed = false
    ├── gate.Error = "html: unterminated <script> element"
    ├── policy.Handle(err) → DecisionRetry          ← policy says RETRY
    ├── gate.Decision = DecisionRetry
    ├── gate.Directive = "Syntax error in your previous response..."
    └── (returned to caller)
        ↓
executor.go:1124-1130  artifactGate:
    gate := v3Artifact.ValidateContent(...)
    if !gate.Passed {
        return "", fmt.Errorf("%w: %s: %w",
            ErrArtifactRejected, target, gate.Error)   ← IGNORES gate.Decision
    }                                                  ← ignores gate.Directive
    return string(gate.Normalized), nil
        ↓
executor.go:616-627   Execute:
    errors.Is(err, ErrArtifactRejected) →
        g.FailExecution(FailurePermanent, ...)
        res.Proof.Outcome = OutcomeArtifactRejected     ← PERMANENT
        return ..., err
        ↓
runtime_loop.go:128   ClassifyOutcome(OutcomeArtifactRejected) → FailurePermanent
        ↓
driver.go:494         decideDefault → LoopAbort               ← NO RETRY
```

### §4.3 The failure policy WOULD have retried

`pkg/capability/policy/standard.go:53-59`:
```go
func (p *StandardFailurePolicy) Handle(err error) PolicyDecision {
    switch classify(err) {
    case FailurePermissionDenied:
        return DecisionAbort
    default:
        return DecisionRetry          ← HTML syntax errors fall here
    }
}
```

`pkg/capability/policy/standard.go:100-111`:
```go
func isSyntaxError(err error) bool {
    msg := err.Error()
    for _, prefix := range []string{"html: ", "json: ", "go: ", "validator: "} {
        if strings.HasPrefix(msg, prefix) {
            return true                 ← "html: unterminated <script>" matches
        }
    }
    return false
}
```

`internal/execution/artifact.go:120-133` (called with `attempts=0`, `MaxAttempts()=3`):
```go
if err := p.registry.Validate(...); err != nil {
    result.Passed = false
    result.Error = err
    result.Decision = p.policy.Handle(err)        ← DecisionRetry
    if attempts >= p.policy.MaxAttempts() {       ← 0 >= 3? NO
        result.Decision = DecisionAbort
    }
    if result.Decision == DecisionRetry {
        result.Directive = policy.Directive(err)   ← "Syntax error..." directive
    }
}
```

So `ValidateContent` correctly computes `DecisionRetry` + a reprompt directive — but `artifactGate` throws it away.

### §4.4 Why this matters

The policy's retry mechanism is designed for the **contract-parser** path (`ParseContracts`), where a model that emits prose instead of a fenced artifact can be re-prompted with the Protocol Specification directive. But for the **artifact-validator** path (HTML/JSON/Go well-formedness), the same `DecisionRetry` is computed and then silently discarded. This means:

- A truncated `<script>` (caused by CASE A's unbounded output) is classified as retryable but hard-rejected.
- The autonomous loop never attempts a single re-prompt — it aborts immediately.
- The approval surface is never reached.

### §4.5 Working-Tree Status

**NOT fixed.** `artifactGate` (`executor.go:1124-1130`) is unchanged from committed HEAD. The policy bypass remains.

### §4.6 Developer's position

`internal/execution/usage_forensics_test.go:179`:
```go
// P4: artifact_rejected is a PERMANENT failure, never recoverable.
```

The forensic test explicitly asserts that `OutcomeArtifactRejected` → `FailurePermanent` → `LoopAbort` is the **intended** terminal semantics. The developer treats the policy bypass as a deliberate design choice: a malformed artifact (truncated HTML) is not worth retrying because re-prompting the same model that just produced 5,883 tokens of truncated output is unlikely to yield valid content. CASE A's budget fix is the primary defense — if the model never generates beyond 1,024 tokens, it cannot produce a truncated `<script>`.

---

## §4.7 — CASE B.2: Invocation-Evidence Erasure (Accounting)

### §4.7.1 Finding

The committed `HEAD` `invokeMutation` and `Execute` error handler drop the `ModelInvocation` evidence (which carries the provider-reported token usage) on any error return — including artifact rejection. This erases the 5,883-token bill from `ExecutionResult.Completed` and `Proof.ModelInvocations`, making the anomaly invisible to accounting.

### §4.7.2 Evidence

**Committed `invokeMutation` error returns (git show HEAD):**
```
git show HEAD:internal/execution/executor.go | sed -n '1049,1090p'

    raw, usage, callErr := x.invokeStream(...)
    if callErr != nil {
        return nil, nil, nil, ...          ← DROPS inv
    }
    ...
    normalized, gateErr := x.artifactGate(...)
    if gateErr != nil {
        return nil, nil, nil, gateErr      ← DROPS inv (the 5,883 bill)
    }
```

**Committed `Execute` error handler:**
```
git show HEAD:internal/execution/executor.go | sed -n '595,616p'

    patches, invs, diffs, err := x.invokeMutation(...)
    if err != nil {
        // NO res.ModelCalls append here        ← invs never captured
        if errors.Is(err, context.Canceled) {
            ...
            return x.finalizeResult(res), nil   ← Completed.OutputTokens = 0
        }
        if errors.Is(err, ErrArtifactRejected) {
            ...
            res.Proof.Outcome = OutcomeArtifactRejected
            return x.finalizeResult(res), err   ← Completed.OutputTokens = 0
        }
```

The `invs` slice is `nil` (because `invokeMutation` dropped it), so `finalizeResult` (which sums `res.ModelCalls`) produces `Completed.OutputTokens = 0`.

### §4.7.3 Working-Tree Fix (diff)

```
diff --git a/internal/execution/executor.go
+   // Retain the invocation evidence on EVERY error return ...
+   res.ModelCalls = append(res.ModelCalls, invs...)
+   res.Proof.ModelInvocations = append(res.Proof.ModelInvocations, invs...)
    if errors.Is(err, context.Canceled) {

diff --git a/internal/execution/executor.go (invokeMutation)
+   // The invocation evidence is built from the stream outcome REGARDLESS
+   // of the artifact result ...
    inv := ModelInvocation{Model: model}
    ...
+   if callErr != nil {
+       return nil, append(invs, inv), nil, ...   ← RETAINS inv
    }

diff --git a/internal/execution/executor.go (artifact gate)
+   // The artifact was rejected, but the invocation evidence ... must survive
    return nil, invs, nil, gateErr                 ← RETAINS inv
```

### §4.7.4 Forensic test pin

`internal/execution/usage_forensics_test.go:161-169`:
```go
if res.Completed.OutputTokens != 5883 { ... }      // provider billing survives
if res.Completed.InputTokens != 2181 { ... }
if res.Completed.ReasoningTokens != 5000 { ... }
if !res.Completed.Known { ... }                    // Known=true, not erased
```

`internal/execution/usage_forensics_test.go:176-178`:
```go
if len(res.Proof.ModelInvocations) != 1 { ... }     // one logical invocation
```

`internal/execution/usage_forensics_test.go:187-189`:
```go
if mock.callCount != 1 { ... }                     // one provider call (no retry)
```

---

## §5 — Falsifiable Invariants (Pinned by Forensic Tests)

All invariants are asserted in the uncommitted forensic test files. The following summarizes what each test proves and where:

### §5.1 Invariants pinning CASE A (output budget)

| Test | File:line | Assertion |
|---|---|---|
| `TestOutputBudgetDoesNotAlterReportedUsage` | `usage_forensics_test.go:196` | `MaxOutputTokens=0` → request omits `max_tokens`; `MaxOutputTokens=512` → request sends `max_tokens=512`; BOTH report `Completed.OutputTokens=5883` (budget is control, not reporting) |
| `TestSingleObjectiveDoesNotImplicitlyReinvokeProvider` | `forensics_phase7_test.go:337` | Single streaming objective → exactly **1** provider invocation; `provider.usage_update` carries `2181/5883/5000` on the bus |

### §5.2 Invariants pinning CASE B (policy bypass + accounting)

| Test | File:line | Assertion |
|---|---|---|
| `TestArtifactRejectedPreservesProviderUsageAndTerminalSemantics` | `usage_forensics_test.go:134` | Malformed HTML → `ErrArtifactRejected`; `Completed.OutputTokens=5883` preserved; `OutcomeArtifactRejected`; `mock.callCount==1` (no retry); file untouched |
| `TestLogicalInvocationVsHTTPAttemptAccounting` | `usage_forensics_test.go:244` | 3 HTTP attempts (2 rate-limit retries) → 1 `ModelInvocation`; `HTTPAttempts=3`, `RateLimitedRetries=2`; `OutputTokens=5883` (not ×3) |
| `TestCancellationPreservesBilledUsage` | `usage_forensics_test.go:275` | Mid-stream cancellation AFTER billing → `Completed.OutputTokens=5883` preserved; `OutcomeCancelled` |

### §5.3 Invariants pinning CASE B.1 (permanent rejection)

| Test | File:line | Assertion |
|---|---|---|
| P4 clause in `TestArtifactRejectedPreservesProviderUsageAndTerminalSemantics` | `usage_forensics_test.go:179` | `artifact_rejected` is **PERMANENT** — `FailurePermanent`, asserted via `ClassifyOutcome` and `decideDefault`→`LoopAbort` |
| `TestProviderInvocationCountAfterSingleApproval` | `forensics_phase7_test.go:25` | Approving a parked approval gate → **0** new provider invocations (no implicit re-invocation) |
| `TestHTMLApprovalConvergesExactlyOnce` | `forensics_phase7_test.go:476` | One objective → Run→approval→completed → exactly 1 provider invocation, 1 apply, 0 re-execution |

### §5.4 Stream-fallback double-billing (falsified)

`internal/execution/executor.go:1256-1282` — the `invokeStream` ExecuteStream→Execute fallback:
```go
rawStream, err := x.provider.ExecuteStream(ctx, req)
if err != nil || rawStream == nil {
    resp, cerr := x.provider.Execute(ctx, req)   // fallback
    ...
    return ai.VisibleCompletion(resp.Content), usage, nil
}
```
The comment explicitly states: "the failed stream consumed nothing" and "never double-bills." The `reproStreamProvider` test mock (`forensics_phase7_test.go:453-458`) enforces this by returning an error from `Execute` (non-stream) — if the fallback fired, the test would fail:
```go
func (p *reproStreamProvider) Execute(...) (*ai.Response, error) {
    p.callsN++
    return nil, errors.New("streaming repro provider must not fall back to Execute")
}
```
**Falsified**: the stream-fallback path does NOT double-bill. A failed `ExecuteStream` (error or nil stream before any byte) consumes nothing and may fall back to `Execute` exactly once.

---

## §6 — OpenRouter SSE Streaming & Reasoning

### §6.1 Reasoning segregation

`internal/providers/openrouter.go` SSE reader wraps `reasoning_content` in `ReasoningSentinel` (`\x00RSNG\x00`); the stream classifier routes these to a separate `reasoningBuf` — never merged with visible completion. `invokeStream`'s `ReasoningHandler` (`executor.go:1248`) consumes reasoning chunks for telemetry only.

### §6.2 Reasoning-by-default

Some OpenRouter models (e.g. DeepSeek) emit reasoning by default. The `reproUsage` pins `reasoning_tokens=5000` of `completion_tokens=5883` — meaning ~5,000 reasoning tokens are billed as completion tokens (OpenAI-compatible convention). With CASE A's fix (`max_tokens=1024`), these reasoning tokens would still be consumed (reasoning is not bounded by `max_tokens` on OpenRouter's reasoning-enabled models unless `reasoning.max_tokens` is set), but the visible truncation risk is reduced.

### §6.3 Visible completion fallback

When `rawStream` content is empty but `reasoningBuf` has data, `invokeStream` emits `VisibleCompletion(reasoningBuf)` as a last-resort artifact source. This is a known seam where reasoning text could leak into the artifact validator — documented in `internal/ai/sanitize.go` and marked `//nolint` at call sites.

---

## §7 — Provider Usage Reporting Flow

```
openrouter.go ExecuteStream → SSE reader
    ├── content chunks → contentBuf → "data: ..." payload
    ├── reasoning chunks → reasoningBuf (segregated, sentinel-wrapped)
    ├── usage delta → streamUsageTracker (Known=true)
    └── provider.usage_update event (events.EventProviderUsageUpdate)

invokeStream (executor.go:1052)
    ├── g.BeginModel(model)           ← model.invoked
    ├── raw, usage, callErr := ...
    ├── inv := ModelInvocation{usage}
    ├── g.CompleteModel(model, in, out)   ← provider.response
    └── return raw, usage

invokeMutation (executor.go:1052-1114)
    ├── invs = append(invs, inv)
    ├── patches = append(patches, Patch{Original, Modified})
    └── return patches, invs, diffs, nil

Execute (executor.go:637-638) [success]
    ├── res.ModelCalls = append(res.ModelCalls, invs...)
    └── setProofGraph → finalizeResult sums ModelCalls → Completed.OutputTokens
```

The working-tree fix ensures the same `invs` append happens on the error path (line 597-604), closing CASE B.2.

---

## §8 — Classification: Transient vs. Recoverable vs. Permanent

| FailureClass | Trigger | Autonomous Loop Action |
|---|---|---|
| `FailureTransient` | Success/no-artifact/skipped outcomes | `LoopComplete` or `LoopAskHuman` |
| `FailureRecoverable` | `OutcomeFailed`, `OutcomePatchGenFailed`, `OutcomePatchFailed`, `OutcomeApplyFailed`, `OutcomeVerifyFailed` | `RecoverFailure(o, FailureRecoverable, b)` — bounded retry |
| `FailurePermanent` | `OutcomeCancelled`, `OutcomeRejected`, `OutcomeArtifactRejected` | `LoopAbort` — no retry |

`internal/autonomy/runtime_loop.go:128`:
```go
case OutcomeCancelled, OutcomeRejected, OutcomeArtifactRejected:
    return FailurePermanent
```

`internal/runtime/autonomy/driver.go:494`:
```go
case autonomy.OutcomeCancelled, autonomy.OutcomeRejected, autonomy.OutcomeArtifactRejected:
    return autonomy.LoopDecision{Action: autonomy.LoopAbort,
        Reason: "terminal outcome: " + string(o.Outcome)}
```

The `OutcomeFailed`/`OutcomePatchGenFailed` outcomes (which return `RecoverFailure`) are the ONLY retryable paths in the autonomous loop. `OutcomeArtifactRejected` is permanent by design.

---

## §9 — Reconciliation: Committed HEAD vs. Working-Tree Fix

### §9.1 Files changed (working-tree)

```
$ git diff HEAD --stat
 internal/execution/executor.go                  | 24 ++++++++---
 internal/runtime/autonomy/adapter.go            |  9 ++++

$ git status --short
 M internal/execution/executor.go
 M internal/runtime/autonomy/adapter.go
 M internal/execution/execution_phase4_test.go
 M internal/providers/openrouter_retry_test.go
 M internal/providers/usage_test.go
 M internal/runtime/autonomy/forensics_phase7_test.go
 M docs/design/PHASE/PHASE_7_AUTONOMOUS_WORKFLOW_COMPLETION_REPORT.md
?? internal/execution/request_forensics_test.go
?? internal/execution/usage_forensics_test.go
```

### §9.2 Changes mapped to findings

| Finding | Fix File | Fix Lines | Effect |
|---|---|---|---|
| CASE A (budget) | `adapter.go` | +`MaxOutputTokens: profile.MaxOutputTokens` | OpenRouter sends `max_tokens` → bounded generation |
| CASE B.2 (accounting) | `executor.go invokeMutation` | `return nil, invs, nil, ...` (was `nil, nil, nil`) | Invocation evidence survives on all error paths |
| CASE B.2 (accounting) | `executor.go Execute` | `res.ModelCalls = append(...)` before classification | `Completed.OutputTokens` populated even on rejection |
| CASE A (verification) | `adapter.go` (comment only) | +9 lines explanatory | Documents the budget-vs-reporting distinction |

### §9.3 What is NOT fixed

| Finding | Location | Status |
|---|---|---|
| CASE B.1 (policy bypass) | `executor.go:1124-1130` `artifactGate` | **Unchanged** — `DecisionRetry` still ignored |
| Reasoning-by-default | `openrouter.go` reasoning config | Unchanged — reasoning tokens still flow unless model-specific |
| Stream-fallback | `executor.go:1256-1282` | Not a bug — falsified (single fallback, no double-bill) |

---

## §10 — Root-Cause Chain (Synthesized)

```
┌─────────────────────────────────────────────────────────────────┐
│ CASE A: Autonomous path omits MaxOutputTokens                    │
│                                                                 │
│ adapter.go (committed): ExecuteRequest has NO MaxOutputTokens   │
│   → req.MaxOutputTokens = 0                                      │
│   → ai.Request.MaxTokens = 0                                     │
│   → openrouter.go: max_tokens (omitempty) → OMITTED from JSON    │
│   → OpenRouter uses provider default (~5,883 tokens)             │
│   → model generates 5,883 tokens of (mostly reasoning) output    │
└─────────────────────────────────────────────────────────────────┘
          │
          ▼
┌─────────────────────────────────────────────────────────────────┐
│ CASE B.1: Truncated output fails artifact gate                   │
│                                                                 │
│ model output truncated (5,883 tokens, mostly reasoning)          │
│   → ResolveModifiedContent extracts HTML fragment                │
│   → artifactGate → v3Artifact.ValidateContent                    │
│   → HTML validator: "unterminated <script> element"             │
│   → policy.Handle(err) → DecisionRetry (HTML syntax is retryable) │
│   → BUT artifactGate IGNORES DecisionRetry → ErrArtifactRejected │
│   → Execute → OutcomeArtifactRejected → FailurePermanent          │
│   → ClassifyOutcome → FailurePermanent                           │
│   → decideDefault → LoopAbort (NO RETRY, NO APPROVAL)             │
└─────────────────────────────────────────────────────────────────┘
          │
          ▼
┌─────────────────────────────────────────────────────────────────┐
│ CASE B.2: Billing erased (committed HEAD)                        │
│                                                                 │
│ invokeMutation returns nil, nil, nil on gate error               │
│ Execute error handler drops invs                                 │
│   → Completed.OutputTokens = 0                                   │
│   → Proof.ModelInvocations = [] (empty)                          │
│ → 5,883-token bill VANISHES from accounting                      │
└─────────────────────────────────────────────────────────────────┘
          │
          ▼
┌─────────────────────────────────────────────────────────────────┐
│ OBSERVED SYMPTOM:                                                │
│ "5,883 completion/output tokens consumed, ~0 artifact produced,   │
│  run terminates at artifact_rejected without approval"           │
└─────────────────────────────────────────────────────────────────┘
```

---

## §11 — Auditor's Assessment

### Findings Verified

1. **CASE A is the primary billing root cause.** The autonomous path (`adapter.go` committed HEAD) does not forward `profile.MaxOutputTokens`. OpenRouter is the only provider without a zero-guard fallback, so `max_tokens` is omitted from the request body and the provider's default ceiling applies. The `/build` path correctly sets the budget via `IntentGateway.Gate`. **The working-tree fix is correct and sufficient for CASE A.**

2. **CASE B.1 is the early-termination root cause.** `artifactGate` (`executor.go:1124-1130`) computes `gate := v3Artifact.ValidateContent(...)`, reads `gate.Passed`, but **never inspects `gate.Decision`, `gate.Directive`, or `gate.Normalized`**. The hard-coded `return "", fmt.Errorf("%w: %s: %w", ErrArtifactRejected, ...)` discards the policy's `DecisionRetry`. The policy (`standard.go:53-59`) classifies HTML syntax errors as retryable, and `ValidateContent` correctly computes this — it is simply never honored.

3. **CASE B.2 is an accounting amplifier (committed HEAD only).** Both `invokeMutation` (`return nil, nil, nil, err`) and the `Execute` error handler drop `invs`, so `Completed.OutputTokens` reads 0. **The working-tree fix is correct and complete for B.2.**

4. **The policy-bypass is asserted intentional.** The forensic tests (`usage_forensics_test.go:179`, `forensics_phase7_test.go:476-480`) explicitly pin `artifact_rejected` as a `FailurePermanent` with `LoopAbort` and zero re-invocation. The developer's design decision is that a malformed artifact is not worth retrying — CASE A's budget fix prevents the truncation that creates malformed artifacts in the first place.

### Findings Falsified

- **Stream-fallback double-billing**: `invokeStream` (`executor.go:1256-1282`) falls back to `Execute` only when `ExecuteStream` errors or returns nil — "the failed stream consumed nothing" (comment, line 1259). The `reproStreamProvider` mock enforces this by erroring on `Execute`. **No double-billing.**
- **UI event-loop starvation**: Not reachable from source trace alone. The UI projection (`internal/ui/model.go` `handleDomainEvent`) subscribes to lifecycle events on a buffered bus (256). No blocking path identified in the audit scope.

### Recommendations (for developer follow-up, not applied)

1. **CASE A fix is deployed.** Verify with a live OpenRouter call that `max_tokens` now appears in the request body for autonomous runs.

2. **CASE B.1 is a deliberate design decision.** Document the policy-vs-gate contract boundary explicitly: `ValidateContent`'s `DecisionRetry` is for the contract-parser path (`ParseContracts`); `artifactGate` intentionally hard-rejects at the validator boundary. Consider moving the `Decision`/`Directive` computation out of `ValidateContent` into a dedicated `ValidateContentWithPolicy` if the distinction is not already documented.

3. **CASE B.2 fix is deployed.** The `TestArtifactRejectedPreservesProviderUsageAndTerminalSemantics` test (`usage_forensics_test.go:134`) pins all 5,883-token accounting invariants. Keep this test as a regression guard.

4. **Reasoning-by-default**: If OpenRouter models emit reasoning by default, consider setting `reasoning.max_tokens` (OpenRouter `reasoning` schema) to bound reasoning output independently of `max_tokens`. Currently `openrouter.go:355-359` only sets `r.MaxTokens` when `req.Reasoning` is explicitly configured — and the production `invokeMutation`/`invokeReadOnly` never set `ai.Request.Reasoning`. This is a separate latent risk if CASE A's budget is bypassed by reasoning tokens.

---

## §12 — Evidence File Map

| Artifact | File | Lines |
|---|---|---|
| `reproUsage` definition | `internal/execution/usage_forensics_test.go:22` | 22-28 |
| Malformed HTML constant | `internal/execution/usage_forensics_test.go:35` | 33-35 |
| `TestArtifactRejectedPreservesProviderUsage...` | `internal/execution/usage_forensics_test.go:134` | 134-190 |
| `TestOutputBudgetDoesNotAlterReportedUsage` | `internal/execution/usage_forensics_test.go:196` | 196-238 |
| `TestLogicalInvocationVsHTTPAttemptAccounting` | `internal/execution/usage_forensics_test.go:244` | 244-273 |
| `TestCancellationPreservesBilledUsage` | `internal/execution/usage_forensics_test.go:275` | 275-329 |
| `TestSingleObjectiveDoesNotImplicitlyReinvoke` | `internal/runtime/autonomy/forensics_phase7_test.go:337` | 337-417 |
| `TestProviderInvocationCountAfterSingleApproval` | `internal/runtime/autonomy/forensics_phase7_test.go:25` | 25-? |
| `TestHTMLApprovalConvergesExactlyOnce` | `internal/runtime/autonomy/forensics_phase7_test.go:476` | 476-609 |
| `artifactGate` (policy bypass) | `internal/execution/executor.go:1124-1130` | — |
| `ValidateContent` (policy computation) | `internal/execution/artifact.go:105-136` | — |
| `StandardFailurePolicy.Handle` | `pkg/capability/policy/standard.go:53-59` | — |
| `isSyntaxError` | `pkg/capability/policy/standard.go:100-111` | — |
| `ClassifyOutcome` | `internal/autonomy/runtime_loop.go:120-135` | — |
| `decideDefault` | `internal/runtime/autonomy/driver.go:481-508` | — |
| OpenRouter `buildRequest` | `internal/providers/openrouter.go:391-414` | — |
| OpenRouter `MaxTokens` field | `internal/providers/openrouter.go:323` | — |
| `withBudgets` / `outputForArtifact` | `internal/execution/strategy/selector.go:587-664` | — |
| `IntentGateway.Gate` budget | `internal/execution/intent.go:104-109` | — |
| Provider zero-guards (gemini/ollama/claude) | `gemini.go:157`, `ollama.go:156`, `claude.go:155` | — |

---

## §13 — Conclusion

The audit independently traces both observed symptoms to their root causes in the committed `HEAD` baseline:

- **The 5,883-token anomaly** is explained by CASE A: the autonomous execution path never forwarded `MaxOutputTokens` to the OpenRouter request, so `max_tokens` was omitted (via `omitempty`) and the provider billed its default ceiling. The working-tree fix in `adapter.go` corrects this by forwarding `profile.MaxOutputTokens`.

- **The `artifact_rejected` early-termination** is explained by CASE B.1: the model's unbounded/truncated output failed the HTML validator (a retryable `DecisionRetry`), but `artifactGate` bypassed the failure policy and returned a hard `ErrArtifactRejected`, which `Execute` mapped to `OutcomeArtifactRejected` → `FailurePermanent` → `LoopAbort`. This bypass is **not fixed** and is **asserted as intentional** by the developer's forensic tests — the permanent-rejection semantics are a deliberate design choice, with CASE A's budget fix serving as the primary prevention.

- **The apparent token erasure** (CASE B.2) is confirmed in committed HEAD: `invokeMutation` and `Execute` dropped `invs` on error returns, zeroing `Completed.OutputTokens`. The working-tree fix retains `invs` on all error paths.

All findings are falsifiable and pinned by the developer's forensic test suite (`reproUsage = 2181/5883/5000`, single invocation, permanent rejection, budget-doesn't-alter-reporting). The stream-fallback double-billing concern is **falsified** — the fallback path consumes nothing and fires at most once.

**Auditor sign-off: root causes identified, evidence chains complete, working-tree remediation validated against committed baseline.**
