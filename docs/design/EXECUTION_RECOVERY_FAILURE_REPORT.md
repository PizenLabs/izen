# Execution-Recovery Failure: Root-Cause Trace

Traced from source, not from names. Each step names the file:line that performs
the conversion.

## 1. The traced path (CapabilityResult -> next action)

```
ai.RunReadOnlyToolLoop                      internal/ai/toolloop.go:45
  -> runReadOnlyTool                        internal/ai/toolloop.go:88
     -> CapabilityToolRunner.Run            internal/execution/capability_tools.go:174
        -> r.execute                        internal/execution/capability_tools.go:232
           -> ReadOnlyToolRunner.readFile   internal/execution/readonly_tools.go:87
              -> os.ReadFile                internal/execution/readonly_tools.go:99
                 -> "read_file style.css: open .../style.css: no such file..."
  -> returns that string as a TOOL RESULT    internal/ai/toolloop.go:78
     (NOT an error, NOT evidence, NOT a loop decision)
```

At this point the failure is a **string in a message list**. Nothing above this
line knows it happened.

```
CapabilityToolRunner.Run classifies it       internal/execution/capability_tools.go:208
  ev.Class = capability.FailureCapabilityFailed     <- hard-coded, every error
  return "error: <msg> [CAPABILITY_FAILED]", nil    <- nil error, line 225
```

`FailureTargetUncertain` exists (internal/execution/capability/capability.go:125)
but is never reachable from a read: `readFile` classifies a missing file as
`CAPABILITY_FAILED` because `os.ReadFile`'s `*PathError` is not inspected
(internal/execution/readonly_tools.go:100-102).

Then, because the tool result is a message and not a typed failure:

```
ExecutorAdapter.diagnosticEvidence          internal/runtime/autonomy/adapter.go:814
  -> Observation.Diagnostic = the string
Observation.Outcome                         -> whatever the outer artifact path said
RecoverySubtype                             internal/runtime/autonomy/recovery.go:88
  -> no branch matches "file does not exist"
  -> SubtypeTransportError                  internal/runtime/autonomy/recovery.go:111
DecideRecovery -> LoopRepair                 internal/runtime/autonomy/recovery.go:339
  "bounded transport re-execution under the SAME contract identity"
typedRepair -> RecoveryAttempt++            internal/runtime/autonomy/recovery.go:538
  (nothing material changed -> identical request)
```

## 2. Root cause 1 — target identity is not a first-class fact

`style.css` is never compared against the resolved scope `[index.html,
script.js, styles.css]` by any code. `internal/execution/resolver.go` already
owns a six-value `TargetState` classification (RESOLVED_FILE / RESOLVED_SET /
NOT_FOUND / AMBIGUOUS / UNBOUND_PATH / UNBOUND_DIRECTORY) but it is applied only
at MUTATION dispatch (internal/execution/executor.go:1640), never to a
capability read request.

Consequence: a read for a nonexistent path is indistinguishable from a transport
failure, so it is retried under the same contract identity.

## 3. Root cause 2 — recovery has no memory of prior failures

`autonomy.Observation` (internal/autonomy/runtime_loop.go:216) carries
`AttemptNum` and `RecoveryCycle` but NO ledger of what failed. The only dedup is
`RuntimeLoop.recordUsage` (internal/autonomy/runtime_loop.go:1049), which counts
identical *actions*, not identical *failures*. Two `read_file(style.css)` calls
with different surrounding actions look like progress.

`typedRepair`'s `SubtypeTransportError` branch
(internal/runtime/autonomy/recovery.go:535-540) is explicitly a "PURE RETRY:
nothing material changes". That is correct for a transport blip and wrong for a
target that does not exist.

## 4. Root cause 3 — anchor and budget failures are string-matched

`isHallucinatedAnchor` (internal/runtime/autonomy/recovery.go:194) matches
`"zero match"` / `"hallucinated anchor"` by substring. `DecideRecovery` grants
exactly one re-prompt (internal/runtime/autonomy/recovery.go:246-253), then the
driver aborts unconditionally (internal/runtime/autonomy/driver.go:1836) with a
reason string that mislabels an anchor failure as
`"Physical Output Budget Breach"`. Two unrelated failures, one string test, one
misleading label.

## 5. Root cause 4 — OUTPUT_EXHAUSTED is inferred, not preserved

`ProviderState` has no `EXHAUSTED` member (internal/execution/objective_authority.go:43-52);
exhaustion is only visible as `finish_reason` +
`OutcomeTruncated`. `RecoverySubtype` checks `isZeroArtifacts` FIRST
(internal/runtime/autonomy/recovery.go:94), so a `finish_reason=length` run with
zero parsed artifacts classifies as `SubtypeZeroArtifacts` ("the model wrote
prose") rather than `SubtypeOutputExhausted`. The two are different facts:
prose means the model chose not to answer; exhaustion means it was cut off.

## 6. Root cause 5 — telemetry drops resolved state

`publishIntentAxes` (internal/runtime/autonomy/objective_lifecycle.go:872-881)
reads `d.req.Scope` for scope and `d.obs.Intent` for user intent.
`d.req.Scope` is never assigned in `Run` (internal/runtime/autonomy/driver.go:574-582),
and `d.obs.Intent` comes from `autonomy.ParseIntent(d.prompt)`
(internal/runtime/autonomy/driver.go:2118) — `ParseIntent` maps canonical LABELS,
not prompt text, so it returns `IntentUnknown` and the axis renders `unknown`.
The authoritative `d.scopeResolution` (already RESOLVED at that point) is never
read.

## 7. Root cause 6 — the executor mislabels anchor failures (found during the fix)

`executor.go:2067` and `executor.go:2074` wrapped a hallucinated-anchor terminal
in `ErrPhysicalOutputBudgetBreach` — a name describing a TOKEN-budget failure.
`isPhysicalOutputBudgetBreach` then matched the string and aborted the run before
the recovery matrix could classify it, with a reason naming a failure that never
occurred. The label was not only wrong, it was structurally load-bearing: it is
what converted every anchor failure into an immediate terminal.

Renamed to `ErrStrictAnchorRecoveryExhausted`. The old symbol is retained as a
deprecated compatibility alias and is no longer produced by the anchor path.

---

# Verification

## Deterministic reproduction of the live failure

`internal/runtime/autonomy/live_failure_repro_test.go` replays the observed
sequence through the REAL capability seam (the provider implements
`SetToolRunner` and drives `ai.RunReadOnlyToolLoop`, as production does).

BEFORE the fix the observed run performed:

    read_file(style.css) -> "style.css: no such file or directory"   (attempt 1)
    read_file(style.css) -> "style.css: no such file or directory"   (attempt 2)

and the failure reached the recovery matrix as an untyped transport error,
which the matrix's own words describe as "bounded transport re-execution under
the SAME contract identity" — i.e. an identical re-request.

AFTER the fix the same reproduction records:

    model capability requests : 2 (for ["style.css"])
    refusals                  : 2
    failure                   : class=TARGET_IDENTITY_MISMATCH target="style.css"
                                seen=2 policy=FORBIDDEN
    terminal state            : awaiting_human
    objective id              : run-1
    objective scope           : index.html, script.js, styles.css
    unmet conditions          : cond-scope-mutated,
                                cond-post-mutation-reinspected,
                                cond-integrity-held
    mutations                 : 0
    held candidates           : 0

The third identical request never happens: the second is already classified
NON_PROGRESSING and the class policy is FORBIDDEN, so no third attempt is
admissible.

## Live benchmark (section 14)

    IZEN_BENCH_LIVE=1 IZEN_BENCH_MODEL=qwen/qwen3-coder \
      OPENROUTER_API_KEY=... go test ./internal/runtime/autonomy/ \
        -run TestPortfolioBenchmark_LiveModel -v

Result — PASS, truthful trace:

    provider invocations : 4
    held candidates      : 0
    approval candidate   : ""
    parked boundary      : inform
    boundary reason      : NON_PROGRESSING_EXECUTION: ARTIFACT_INVALID
                          target=index.html: executor: mutation artifact
                          rejected with retry directive: script.js: bounded
                          patch contract requires SEARCH/REPLACE blocks ...
                          (seen 2 time(s) under unchanged evidence)
    terminal outcome     : <parked, no terminal state>
    mutations applied    : 0 (0 byte(s) changed)
    objective id         : run-1
    objective scope      : index.html, script.js, styles.css
    unmet conditions     : cond-scope-mutated,
                          cond-post-mutation-reinspected, cond-integrity-held

Every acceptance criterion holds: no scope substitution, no repeated invalid
target, no repeated invalid anchor, no patch claimed without a parsed artifact,
no completion claimed, no generic "repair" hiding the cause, objective identity
intact, authoritative scope intact, no manufactured progress events.

### Token budget

`max completion = 3072` was NOT raised. Section 11 asked five questions; the
answers are architectural and are recorded here rather than acted on:

1. Why requirement derivation consumes a provider call — it is a separate
   read-only invocation issued before the main lane (`RequirementPassForExecutor`).
   It is necessary: the model proposes, the runtime admits. Removing it would not
   help the exhaustion cases observed here, none of which were requirement-pass
   exhaustion.
2. Why the model repeatedly reaches the output boundary — because it was asked
   for a whole-file rewrite of three targets under a 3072-token ceiling. That is
   a DECOMPOSITION question, not a budget question.
3. Whether it is asked to produce too much in one bounded step — yes, and the
   staged-decomposition path already exists for it; the live run did not reach it
   because the artifact contract failed first.
4. Whether continuation preserves enough state — it now does, and this was a
   real defect: `recoveryBrief` was added because the previous continuation
   carried the objective but not the FAILED APPROACH.
5. Whether repair prompts cause repeated full-context generation — they did, and
   that is now bounded by the failure ledger and per-class breakers rather than by
   a token wall.

A budget adjustment may still be justified for the decomposition path, but it is
not the fix for this failure layer and is deliberately not made here.
