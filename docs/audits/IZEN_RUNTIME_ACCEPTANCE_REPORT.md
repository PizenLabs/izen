# IZEN_RUNTIME_ACCEPTANCE_REPORT.md

## 1. Executive Summary

**Decision: ACCEPTED WITH OBSERVATIONS.**

Eight runtime scenarios (A–H) were executed against the current repository
(`fix/execution`, commit `c096edb`, clean tree) using the canonical path
`IntentGateway.Gate → execution.RuntimeExecutor` with scripted mock providers
in isolated temp workspaces. All authority-boundary assertions passed:

- Bare `/build` with no scope directive is rejected fail-closed at intent
  time (`ScopeAuthorizationError`); no mutation, no model call, filesystem
  byte-identical.
- `$prompt` and `$hot` both authorize mutation scope but with observably
  distinct contracts (scope provenance, output budget, context policy).
- `finish_reason=length` circuit-breaks the step as typed
  `OUTPUT_EXHAUSTED` with zero partial mutation (BOUND THE STEP, NOT THE TASK).
- Sequential continuation reuses the same `RuntimeExecutor` identity; no new
  runtime, no duplicate execution, exactly 1 model call per step.
- An adversarial model artifact naming an out-of-scope file produced no
  mutation anywhere (proposal ≠ execution).
- No TUI-reachable competing runtime path was observed; one reachable
  alternate mutation path exists for headless entry points (observation, not
  drift).

No production code was modified. One temporary harness file was created,
executed, and removed; the tree is clean after the run. Three non-blocking
observations are recorded as backlog.

## 2. Scope

Validation-only acceptance of the runtime converged in Phases 7–10. No
redesign, no new scheduler/executor, no optimization, no Phase 11. The five
mission questions (authority boundary; `$prompt`/`$hot`/absent semantics;
constrained/interrupted/continued execution; bottlenecks/costs; parallel
logic) are answered from runtime evidence below.

Canonical model assumed (and confirmed by evidence):

```text
HUMAN → Intent/Policy → Admission → Authorization
  → execution.RuntimeExecutor → Execution → Evidence
  → Verification → Recovery/Continuation → SAME executor re-entry
```

## 3. Environment

```text
commit:        c096edb "fix(architecture): correct registry file path"
branch:        fix/execution
working tree:  clean before and after (temp harness removed, `git status` empty)
Go version:    go1.27.1 darwin/arm64 (go.mod declares go 1.26.0)
OS:            Darwin 27.0.0 arm64
provider:      scripted mock implementing ai.Provider (no network, no keys)
               — provider latency / real token accounting NOT OBSERVABLE here
config:        config.Default() + trivial `true` verifier + 1h MutationAuthorization
workspace:     t.TempDir() per scenario (isolated, disposable)
```

No secrets exist in this environment; none were printed. No live
(OpenRouter/local-model) configuration was available, so all model behavior
is via the injected `ai.Provider` interface — the exact seam the production
runtime uses, so the authority/strategy/mutation/evidence path exercised is
the production path.

## 4. Baseline

```text
go build ./...                    → clean (exit 0)
go test ./...                     → all packages ok, no failures
go test ./test/architecture/...   → ok
go test ./test/integration/...    → ok (0.639s)
go test ./internal/execution/...  → ok (cached, then re-run with harness)
go test ./internal/runtime/...    → ok
```

Repository starts clean; no pre-existing failure to confound scenario results.

## 5. Test Matrix

| ID | Scenario | Harness |
|----|----------|---------|
| A | `/build` without `$prompt`/`$hot` (negative control) | `TestAcc_A_NoModifierCannotMutate` |
| B | `/build $prompt` broad build | `TestAcc_B_PromptBroadBuild` |
| C | `/build $hot` narrow fix | `TestAcc_C_HotBounded` |
| A/B/C | Gate-level comparison (4 inputs) | `TestAcc_ABC_Comparison` + broad-prompt probe |
| D | Constrained model (`finish_reason=length`) | `TestAcc_D_ConstrainedOutput` |
| E | Continuation, same authority | `TestAcc_E_ContinuationSameAuthority` |
| F | Multi-step measurement | `TestAcc_F_MultiStep` |
| G | Scope-boundary attack | `TestAcc_G_ScopeAttack` |
| H | Parallel/divergent logic trace | static + runtime reachability (no harness test) |

Each scenario records INPUT / POLICY / AUTHORITY / CAPABILITIES / MUTATION /
EXECUTION / EVIDENCE / VERIFICATION / RECOVERY / ACTUAL per contract.

## 6. Scenario A — /build WITHOUT $prompt OR $hot

- **INPUT:** `/build redesign the portfolio homepage and modify the relevant files`
- **EXPECTED:** fail-closed; no WRITE; no mutation.
- **ACTUAL:** `Gate` returned `state error: mutation plan requires scope
  authorization via $prompt or $hot` (directive=`""`, scope=`ScopeNone`).
  No `ExecuteRequest` with mutation scope was produced, no provider was
  invoked, file hash unchanged (`b9a6627384e6 == b9a6627384e6`).
- **Classification:** **PASS.** This is `WRITE denied` (fail-closed at the
  intent boundary), not `granted but unused` — the stronger, correct outcome.

## 7. Scenario B — /build $prompt

- **INPUT:** `$prompt check index.html and remove extra contents`
- **Gate:** directive=`prompt`, scope=`ScopeDynamic` (allows mutation),
  strategy=`targeted_mutation`, targets=`[index.html]`,
  ctx=`target_file_only`, artifact=`replace_block`, out=`2048`.
- **EXECUTION:** 1 model call → `approval.required` (PendingPatchID) →
  `Approve` → outcome=`changed`, file mutated (hash `e97c52aa8327`),
  verification passed, 8.35 ms wall.
- **Events:** `started → strategy.selected → target.resolved →
  context.prepared → model.invoked → artifact.produced → approval.required →
  mutation.started → mutation.completed → verification.completed`
  (`execution.finished` arrives asynchronously; observed in scenarios with a
  drain wait — see §19 observation O2).
- **Classification:** **PASS.** `$prompt` authorizes dynamic mutation scope
  and executes the full canonical lifecycle with an approval gate.

## 8. Scenario C — /build $hot

- **INPUT:** `$hot fix the navigation in @index.html`
- **Gate:** directive=`hot`, scope=`ScopeDeclared`, strategy=
  `targeted_mutation`, targets=`[index.html]`, maxFiles=`10`, out=`1024`.
- **EXECUTION:** 1 model call → approve → `<nav>old</nav>` → `<nav>new</nav>`,
  outcome=`changed`, verification passed.
- **Bounded-mode evidence:** output budget `1024` vs `$prompt`'s `2048–3072`
  for the same target; declared-target scoping; `human_clarification` (zero
  model calls, zero reads) when the `@target` does not resolve — verified
  with `/build $hot fix … @index.html` in an empty workspace.
- **Classification:** **PASS.** `$hot` is a bounded deliberate mode with its
  own budget/scope contract, not a renamed `$prompt`.

## 9. Scenario D — Constrained Model Execution

- **INPUT:** `$hot fix the navigation in @index.html` with first response
  `finish_reason=length` (truncated SEARCH/REPLACE prefix).
- **ACTUAL:** `Execute` returned typed
  `output gate OUTPUT_EXHAUSTED … finish_reason="length"` wrapping
  `ErrPayloadTruncated`; exactly 1 provider call consumed; events stop after
  `model.invoked` (no artifact, no approval, no mutation events);
  filesystem byte-identical (no partial apply).
- **Classification:** **PASS.** BOUND THE STEP, NOT THE TASK: the step halts
  with a typed recoverable signal; nothing was retried wastefully, nothing
  duplicated, no state lost, no partial write. (The bounded-patch recovery
  continuation is owned by the strategy/repair layer; the halt itself is the
  contracted behavior at this boundary.)

## 10. Scenario E — Continuation / Recovery

- **INPUT:** two sequential `$hot` single-file mutations through one executor.
- **ACTUAL:** both steps approved; `a.txt` and `b.txt` each exactly as
  intended; same `*RuntimeExecutor` pointer (`0x45407dda8c0`) served both
  steps; 2 model calls total; full canonical event sequence per step;
  authorization object reused, no new runtime constructed.
- **Classification:** **PASS.** Continuation changed the next step, not the
  authority owner. The forbidden pattern (failure → new runtime → new
  authority → duplicate execution) did not occur.

## 11. Scenario F — Multi-Step Execution

- **INPUT:** two `$hot` mutations, measured.
- **MEASURED:** modelCalls=`2`, wall=`12.6 ms`, `21` events (two full
  canonical sequences), filesMutated=`2`. Per step: 1 model call, 1 patch,
  1 verification. No duplicate planning, inspection, model calls,
  re-verification, or recovery observed.
- **Classification:** **PASS** (no significant unnecessary execution).

## 12. Scenario G — Scope / Authority Boundary Attack

- **INPUT:** `$hot fix the navigation in @index.html` with `secret.txt`
  present and discoverable; model artifact adversarially targets
  `DO NOT TOUCH` (content of `secret.txt`).
- **Gate scope:** targets=`[index.html]` only; `secret.txt` never entered
  the target set.
- **ACTUAL:** `secret.txt` byte-identical; `index.html` unchanged (adversarial
  SEARCH block matches no authorized target, apply fails closed). The LLM
  *proposed* it; the runtime neither authorized nor executed it.
- **Classification:** **PASS.** Proposal ≠ authorization ≠ execution holds
  under adversarial model output.

## 13. Scenario H — Parallel / Divergent Logic Detection

Runtime-traced (all scenarios A–G executed exclusively
`IntentGateway.Gate → execution.RuntimeExecutor.Execute → Approve`) and
statically cross-checked:

```text
Canonical runtime path (observed in every scenario):
  UI → IntentGateway.Gate → execution.RuntimeExecutor
       (wired via internal/runtime/compose/compose.go:585)
  TUI /build: handleBuildRun → dispatchStagedTask → runRuntimeTaskRequest
       → RuntimeExecutor.Execute (internal/ui/commands.go, gateway.go)
  Autonomous TUI surface drives the same boundary (autonomousDriver interface).
```

| Candidate | Reachability | Classification |
|-----------|--------------|----------------|
| `execution.RuntimeExecutor` | reached in every scenario | canonical authority |
| `runtime/orchestrator.Orchestrator.RunCycle` + `runtime/executor.FileExecutor.Commit` | reachable via headless CLI (`internal/cli/cli.go:553`), verification harness (`internal/verification/harness.go:330`), `cmd/izen/orchestrate.go` | REACHABLE AND SUPPORTING (alternate entry points with own preflight + FastPathGate + sanitize + snapshot gate; never observed intercepting TUI `/build` traffic) |
| `MutationSet` / `PatchManager` construction | only inside `internal/execution` (executor/execution/patch/mutationset.go); no UI-direct construction found | UNREACHABLE except through canonical authority |
| Direct `os.WriteFile` mutation paths | present in checkpoint/config/substrate/rollback persistence layers (state stores, not task execution) | REACHABLE BUT NON-AUTHORITATIVE (no task-mutation path bypassing the executor found) |
| Legacy schedulers / mode-specific runtimes | none reached; UI comments assert no legacy path; architecture convergence tests pass | UNREACHABLE (dead/residue if present) |

**Conclusion:** no semantically competing path was reached. Zero findings in
the `REACHABLE AND SEMANTICALLY COMPETING` category. The headless
`RunCycle/FileExecutor` path is recorded as observation O1 (backlog),
not drift.

## 14. Authority Boundary Results

- Bare `/build` and bare conversational input compile to read-only
  (`ScopeNone`; unauthorized mutation goals are rewritten to
  `repository_investigation`/`targeted_reasoning` with explanation artifacts).
- `WRITE denied` proven by rejection error + zero model calls + identical
  hashes — not by UI labels.
- Authorization (`MutationAuthorization`) is set on the executor independently
  of scope; both are required before `Approve` can mutate.
- Adversarial artifact (G) caused no mutation anywhere.

## 15. $prompt / $hot Behavioral Comparison

```text
                 NO MODIFIER        $prompt              $hot
Policy           read-only          dynamic scope        declared scope
Scope            ScopeNone          ScopeDynamic(1)      ScopeDeclared(2)
Capabilities     no mutation        mutation (resolved)  mutation (declared)
Authorization    denied at Gate     granted+used         granted+used
Budget(out)      0 / N/A            2048–3072            1024
Mutation env.    none               target-resolved      target-declared, bounded
Files inspected  none (rejected)    target_file_only     target_file_only (maxFiles=10)
Files modified   0                  1                    1
Model calls      0                  1                    1
Exec steps       0                  full canonical seq   full canonical seq
Verification     N/A                passed               passed
Recovery         N/A                OUTPUT_EXHAUSTED halt OUTPUT_EXHAUSTED halt
Unresolved tgt   N/A                investigation         human_clarification, 0 calls
```

Broad `$prompt` without a target resolves to `repository_investigation`
(ctx=`repository`, out=`2048`) — broad does not mean unlimited. The three
modes are observably distinct where the contract requires it. No dead or
redundant policy logic found.

## 16. Continuation / Recovery Results

- Exhausted step halts typed, state-preserving, no partial write (D).
- Next-step continuation reuses executor/authority identity (E).
- No new runtime, no duplicate execution, 1:1 calls-to-steps (F).
- Recovery does not create authority; it queues the next step under the same
  owner.

## 17. Execution Correctness

For every successful scenario: authorized scope matched targets; capability
set matched strategy; mutation bytes matched the artifact; checkpoint/OCC
verify passed (`occ verify targets=1 conflicts=0` in logs); proof outcome
(`changed`) matched filesystem diffs; verification (`true` step) reflected
real command results. Observed filesystem state (hashes, content reads) was
used as ground truth over log claims throughout.

## 18. Execution Efficiency

```text
wall-clock (mock provider):  B 8.35ms / F 12.6ms for 2 steps (no I/O bottleneck)
model calls:                 exactly 1 per mutation step, 0 for rejected/clarified
input/output tokens:         from provider-reported usage only (mock values
                             20–40 in / 6–12 out); real token accounting NOT
                             OBSERVABLE without a live provider
planning latency:            NOT OBSERVABLE separately (folded into Execute)
execution/verification/
  recovery latency:          NOT OBSERVABLE separately (ms-scale total)
files inspected/mutated:     1 target in / 1 file out per bounded step
continuation/recovery count: 0 in F; 1 typed halt in D (no retry loop)
```

## 19. Telemetry Consistency

Cross-checked TUI-event model vs `occ`/execution logs vs filesystem state:
canonical event sequences matched the observed mutations in all scenarios;
token counts in proofs came only from provider usage. Two notes:

- **O2:** `execution.finished` is emitted asynchronously; a consumer reading
  events immediately after `Approve` returns may not yet see it (scenario B
  showed 10/11 events without a drain wait; C/E/F with continued execution
  showed it). Consumers must drain/wait, not assume synchrony.
- No stale counters, duplicate events, execution-without-evidence, or
  verification-without-mutation were observed.

## 20. Bottleneck Analysis

```text
Primary bottleneck:       none demonstrated at runtime (mock provider;
                          8–13 ms end-to-end per step). In production the
                          provider round-trip is expected to dominate, but
                          that is provider-induced and was NOT OBSERVABLE here.
Secondary bottleneck:     none demonstrated.
Unnecessary execution:    none found (1 call/step, no duplicate planning or
                          re-verification).
Provider-induced limit:   real latency/token accounting unmeasurable without
                          live credentials (environment limitation, not defect).
Runtime-induced limit:    none demonstrated.
```

Per the stopping rule: **no material runtime bottleneck was demonstrated by
the tested scenarios.** None is invented.

## 21. Parallel Logic Analysis

```text
Canonical runtime path:  UI → IntentGateway.Gate → execution.RuntimeExecutor
                         (Execute → Approve) — sole path in all scenarios.
Observed reachable paths: canonical path only (A–G traces).
Non-canonical reachable:  Orchestrator.RunCycle + FileExecutor.Commit via
                          headless CLI / orchestrate / verification harness
                          (own preflight + FastPathGate + sanitize gates).
Semantically competing:   none observed.
Dead/unreachable legacy:  no live traffic; convergence tests green.
Conclusion:               no architectural drift demonstrated at runtime.
```

## 22. Defects

No `RUNTIME DEFECT`, `AUTHORITY-BOUNDARY DEFECT`,
`EXECUTION-CORRECTNESS DEFECT`, or `ARCHITECTURAL DRIFT` findings. All
anomalies classify as `PASS` or `PASS WITH OBSERVATION` (O1–O3 below); the
absence of live-provider measurement is an `ENVIRONMENT LIMITATION`.

## 23. Observations (backlog, non-blocking)

- **O1 — Headless `RunCycle/FileExecutor` mutation path.** Reachable via CLI
  entry points with its own authorization gate. Never observed competing with
  the TUI canonical path. Follow-up: add a convergence test pinning that TUI
  `/build` traffic cannot reach `FileExecutor.Commit`, or document the
  headless path as the supported alternate entry. Severity: low.
- **O2 — Async `execution.finished` delivery.** Consumers must drain/wait for
  terminal events; immediate post-`Approve` reads may miss the tail.
  Follow-up: document the drain contract or expose a synchronous flush for
  tests. Severity: low (test-harness ergonomics).
- **O3 — Live-provider validation outstanding.** Latency, token accounting,
  and recovery-under-real-truncation were proven only against the provider
  interface seam. Follow-up: repeat B/C/D once against a constrained live
  model when credentials are available. Severity: environment limitation.

## 24. Acceptance Matrix

| Acceptance Criterion | Scenario | Expected | Actual | Evidence | Status |
|---|---|---|---|---|---|
| No modifier cannot mutate | A | fail-closed, hashes equal | `ScopeAuthorizationError`, 0 calls, `b9a6627384e6==b9a6627384e6` | gate log + hash compare | PASS |
| `$prompt` has intended semantics | B | dynamic scope, full lifecycle | `ScopeDynamic/targeted_mutation`, 1 call, `changed`, verify pass | gate+exec+event log | PASS |
| `$hot` has bounded semantics | C | declared scope, tighter budget | `ScopeDeclared`, out=1024, `changed` | gate+exec log | PASS |
| Mode distinctions observable | A/B/C | distinct policy/scope/budget | scope 0/1/2, out 0/2048–3072/1024, clarify-vs-investigate | comparison table §15 | PASS |
| Authorization boundary enforced | A/B/C/G | deny-unauthorized, contain-authorized | A denied; G adversarial artifact → 0 mutations | hashes + target-set log | PASS |
| Constrained step handled | D | typed halt, no partial apply | `OUTPUT_EXHAUSTED`, 1 call, file identical | error + events + hash | PASS |
| Continuation preserves authority | E | same executor/authority | same pointer, 2 calls, both files correct | identity + content | PASS |
| Recovery creates no runtime | E | next step, same owner | no new runtime; sequential canonical seqs | event sequences | PASS |
| Execution matches evidence | A–H | proof == filesystem | `changed` ⇔ content diff in all cases | hashes + proofs | PASS |
| Verification reflects state | A–H | real gate results | `true`-step results + OCC `conflicts=0` | logs + flags | PASS |
| No competing path reached | H | canonical only | all traces via `RuntimeExecutor`; headless path untouched | traces + grep | PASS |
| No unnecessary execution | F | 1 call/step | 2 calls / 2 steps / 21 events | counters | PASS |
| Telemetry reflects truth | A–H | truthful transitions | canonical seqs; async-tail note O2 | event logs | PASS WITH OBSERVATION (O2) |

## 25. Final Acceptance Decision

1. **Runtime consistent with Phases 7–10 architecture?** Yes — every trace
   followed `IntentGateway → RuntimeExecutor` with strategy-owned paths.
2. **Authority boundaries enforced in real execution?** Yes — fail-closed
   denial (A) and adversarial containment (G) both proven by filesystem state.
3. **`$prompt`/`$hot`/no-modifier semantically distinct?** Yes — scope
   provenance, budgets, context policy, and unresolved-target behavior all
   differ observably (§15).
4. **Constrained execution preserves continuity without violating authority?**
   Yes — typed halt, no partial write, no wasteful retry (D).
5. **Recovery/continuation preserves authority?** Yes — same executor identity
   across steps (E/F).
6. **Duplicate/competing runtime logic?** None reached; one supporting
   headless path noted (O1).
7. **Largest demonstrated bottleneck?** None demonstrated (mock provider;
   production provider latency expected but unmeasured).
8. **Remaining issues classified?** O1/O2 backlog observations; O3
   environment limitation. No architectural or runtime defects.
9. **Ready for normal product/feature development?** Yes.

```text
ACCEPTED WITH OBSERVATIONS
```

## 26. Recommended Follow-up

1. Pin O1 with a convergence test (TUI `/build` ⇒ never `FileExecutor.Commit`).
2. Document/drain-fix the async terminal-event contract (O2).
3. Re-run B/C/D against a live constrained model when credentials exist (O3).
4. Then: close the repair cycle, establish this report as the runtime
   baseline, and build product features. No further architecture phases.
