# EXECUTION FORENSICS — STATE

**Read `EXECUTION_FORENSICS.md` first.** This file is the handoff.

- **Branch:** `fix/runtime` (base `f40f369`)
- **Suite:** `go test ./...` green; `go test -race ./...` green (full tree);
  `go vet ./...` clean. Verified after R6-A.
- **Status:** **R1 PROVEN** by live execution. **R2 BLOCKED** by live
  execution — recorded, not worked around. **R3 PROVEN** — the target-proposal
  authority boundary is identified and pinned; DISCOVERED/PROPOSED/AUTHORIZED
  are now separate, single-sourced control-plane facts. **R4 PROVEN** — a real
  model call can exhaust its output ceiling, IZEN records it truthfully, the
  runtime-owned continuation stays inside the same execution, and completion
  remains evidence-gated. **R5 PROVEN** — IZEN already owns a runtime-side
  definition of progress (evidence-reduced completion conditions + the failure
  ledger), the five non-progress scenarios are bounded and proven, and one
  precisely-bounded missing transition was recorded (the per-attempt progress
  delta on the successful-partial path). **R5.1 PROVEN** — that boundary is
  closed: the continuation router now compares the objective's authoritative
  progress fingerprint across lifecycle attempts, so a repeated partial state is
  not re-opened, while the `MaxIdenticalDecisions` safety ceiling is unchanged.
  **R6-A PROVEN** — cancellation is a real runtime-owned boundary: Esc×2 /
  Ctrl+C / OS signal withdraw the operation context, the driver owns two
  linearization points (the inter-step gate and `RuntimeLoop.Step`), a cancelled
  execution terminates `RuntimeAborted` and can never become `PROVEN`, late
  provider/stream/capability results cannot resurrect it, and a committed
  mutation is not rolled back. One lifecycle-semantics defect was found and
  fixed (a cancel at the mutation boundary reported `apply_failed` instead of
  `cancelled`). Full report: `R6_CANCELLATION_REPORT.md`.
  **R6-B PROVEN** — interruption is execution-scoped and cannot cross an
  execution boundary. The runtime has **no queue of independent executions**: a
  second unit is refused at the driver, the UI admission and the kernel. Parked
  abort (`Driver.Abort`) is terminal/non-resumable and distinct from a running
  cancel; a continuation keeps its original execution identity; a late decision
  from a terminated run cannot mutate the next run. No lifecycle defect was
  found and **no production code was changed**. Full report:
  `R6_INTERRUPT_QUEUE_REPORT.md`.
  The remaining items are open questions, not open defects.

---

## Where we are

The `$prompt` path is fully observable. A bounded run is reconstructable from
`internal/forensics` — live, or from `.izen/audit/events.ndjson` after the
process exits.

Seven defects found and fixed by R1. The full trace, evidence and reasoning are
in `EXECUTION_FORENSICS.md`. R2 (below) found and fixed an eighth and recorded a
BLOCKED benchmark.

---

## R1 STATUS: PROVEN

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

is demonstrated end to end against a **real local model** (`ollama /
qwen2.5-coder:7b`) over the **real production composition** (`compose.Wire` →
the bounded `autonomy.Driver` → the `RuntimeExecutor`), in an isolated git
workspace.

The decisive evidence, in one line each:

| Fact | Value |
|---|---|
| scope provenance | `$prompt` |
| grant derived | `workspace.discover, file.read, file.search, runtime.serve, runtime.fetch, runtime.inspect, command.run` |
| **capabilities actually executed** | **`runtime.fetch, runtime.serve, workspace.discover`** |
| behavioural verdict | **PROVEN** — served the workspace, fetched it, `HTTP 200` |
| objective state | **PROVEN** (granted=true) |
| mutation on disk | `index.html` greeting `goodbye` → `hello` |
| final state | **`completed`** |
| forensic patterns | none detected |

**The control arm is what makes it a proof.** The identical run — same
workspace, same objective, same model — with the directive withheld derives a
read-only grant, executes only `workspace.discover`, is refused
`AUTHORIZATION_BLOCKED` at `runtime.serve`, never reports `PROVEN`, and ends at
`awaiting_human`. The only variable is whether the authorizing directive reached
the run.

Reproduce:

```
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r1/ -v -timeout 900s
```

---

## R2 STATUS: BLOCKED

The R2 benchmark asks whether IZEN performs a real multi-step
discover→inspect→reason→mutate→observe→verify→PROVEN loop on an objective that
**does not name the target file**:

```
workspace:  index.html with <h1 id="greeting">Helo</h1>
objective:  inspect this project, find the incorrect greeting, fix it to "Hello",
            and verify the result.
```

It does **not**. The chain stops at `discovery → inspection`:

| Stage | Result |
|---|---|
| objective → authorization | reached; verdict `disambiguate` |
| discovery | **RAN** — bounded scan observed 1 candidate, `index.html` |
| inspection | **NOT REACHED** — `channels: (none)`, no file bytes sent to the model |
| model computation | only the read-only requirement pass ran (512 → 2 tokens, `stop`) |
| decision | `ask_human` |
| mutation / observation / verification / objective evaluation | **none** — `mutations: 0`, workspace unchanged |

**Why it is correct to stop there, and why it is a block:** discovery
candidates are evidence, never authority (`I13`), and evidence-bound scope
derivation binds only files satisfying an artifact kind the objective itself
declared. The benchmark declares none. Reaching mutation from targetless
discovery requires a discovery→inspection→decision capability at the
authorization boundary — a redesign this experiment is forbidden to make. So the
result is **BLOCKED and recorded**, not hacked green.

**The downstream chain is independently PROVEN.** The same workspace, same
no-filename shape, same model, with an objective that declares the kind
(`…the incorrect message in the HTML…`) runs the whole loop:

```
derivation UNIQUE kinds=html → index.html bound
→ channels [target:index.html] → mutation call (419-token prompt carrying file bytes)
→ mutation applied fs_changed=true (+1/-1) → verification STARTED→NOT_APPLICABLE
→ objective.evaluated PROVEN granted=true → completed
→ disk holds <h1 id="greeting">Welcome</h1>
```

So the loop is real; the missing piece is *targetless discovery authority*.

**One defect on the path was found, classified and fixed.** The benchmark
objective was classified `direct_response` (zero-context casual chat) because the
replacement value `"Hello"` is also a greeting pattern — a frozen spec with
`intent=MUTATE` and a read-only strategy. Classification:
**CAPABILITY_SELECTION_FAILURE**. Fixed in `internal/gateway/chat.go` (a
workspace-action guard) with two deterministic regressions; re-running the
benchmark then reaches the same block with the corrected
`multi_file_planning` mutation contract.

Reproduce:

```
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r2/ -v -timeout 1200s
# Observation            PASS (benchmark measurement)
# DiagnosticDeclaredKind PASS (downstream chain PROVEN)
# AcceptanceChain        FAIL (R2 blocked at discovery→inspection — expected)

# Deterministic, no model — the same boundary, pinned in the always-run suite:
go test ./internal/runtime/autonomy/ -run TestR2_TargetlessRepairObservesButDoesNotDispatch -v
```

---

## R3 STATUS: PROVEN

R3 asks whether IZEN can turn discovery evidence into a **target proposal** —
and, where policy permits, authority — without conflating DISCOVERED with
AUTHORIZED. Full report: **`R3_TARGET_PROPOSAL_REPORT.md`**.

The finding: the one authoritative decision boundary is
`EvaluatePreflightAdmission`; the proposal is produced by `execution.DeriveScope`
and offered through the strategy gateway; a unique candidate can justify a
**proposal** but not authority. For an objective that declares no artifact kind,
the correct authoritative outcome is an explicit clarification — and R3 proves
it is explicit and grounded, not an accidental zero-scope fallback.

| Scenario | DISCOVERED | PROPOSED | authorizes | AUTHORITY | calls | fs | final |
|---|---|---|---|---|---|---|---|
| R3-A one file, no kind | `index.html` | `[index.html]` | **false** | DISAMBIGUATE | **0** | none | clarify |
| R3-B three files, no kind | 3 candidates | none | **false** | DISAMBIGUATE | **0** | none | clarify |
| R3-C explicit `index.html` | — | `[index.html]` | **true** | ADMIT | 1 | applied | PROVEN |

R3 grants **no new authority**. It makes DISCOVERED and PROPOSED first-class and
single-sourced: `Derivation.Candidates`, a non-authorizing `ScopeProposed`
scope position, and `derivation_candidates` / `candidates` / `proposed_targets`
on the canonical events and the forensic trace.

Reproduce (no model):

```
go test ./internal/runtime/autonomy/ -run TestR3_ -v
# live acceptance (real local model):
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r2/ -run TestLiveR3_ -v
```

---

## R4 STATUS: PROVEN

R4 asks whether a real model that reaches its output limit before completing an
authorized objective is continued correctly — or stopped, restarted, duplicated,
context-lost, or falsely completed. Full report:
**`R4_BOUNDED_CONTINUATION_REPORT.md`**.

The finding: continuation is **runtime-owned** and, in fact, has **two layers**,
both of which were verified live:

1. **Executor full-artifact bounded-step continuation**
   (`internal/execution/artifact_step.go`): a `length` is an INVOCATION outcome,
   not a task failure. The delivered prefix is preserved as a *candidate* and the
   SAME artifact contract is advanced across bounded invocations, up to
   `llmstep.DefaultMaxContinuationSteps`.
2. **Driver recovery matrix** (`internal/runtime/autonomy`): when the step budget
   is consumed (or the exhausted step delivered no bytes) the typed
   `OUTPUT_EXHAUSTED` reaches the driver, which materialises a materially
   different contract (`FULL_REWRITE → BOUNDED_PATCH`) and makes a second call in
   the SAME execution.

The live benchmark (`test/live_r4/`, real `ollama/qwen2.5-coder:7b`, isolated git
workspace, explicit `@index.html` target, bounded 1024-token mutation request)
produced a genuine `finish_reason=length` (`requested=1024 effective=1024
known=true completion=1024`), an explicit `continuation.evaluated/selected`
repair, and a second call `run-1-attempt-2` in the **same run**, converging to
`PROVEN` through the evidence-gated authority with exactly one applied mutation.

| Critical invariant | Result |
|---|---|
| real `finish_reason=length` observed | **yes** |
| exhaustion recorded truthfully (`output_exhausted=1`, `effective=1024`) | **yes** |
| incomplete state never becomes `PROVEN` | **yes** (live negatives ended `unsubstantiated`/`awaiting_human`) |
| continuation explicitly evaluated, not a blind retry | **yes** |
| continuation stays in the same execution (`run-1` → `run-1-attempt-2`) | **yes** |
| mutation not blindly duplicated | **yes** (exactly 1 applied) |
| completion remains evidence-gated | **yes** |

**No production file was changed.** R4 is observability + proof. One concrete
finding is reported, not fixed: the executor’s streaming truncation path
(`invokeStream`) discards the delivered prefix, whereas the non-streaming path
returns it so the same-contract continuation can use it. Continuation correctness
is unaffected (the driver layer covers it); it is a salvage symmetry, and the
minimal correction boundary is recorded in the report.

Reproduce:

```
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r4/ -v -timeout 1200s
# deterministic, no model:
go test ./test/forensics/ -run TestR4_ -v
```

---

## R5 STATUS: PROVEN

R5 asks what prevents a continuation-capable execution from repeatedly
continuing without making meaningful progress. Full report:
**`R5_NON_PROGRESS_REPORT.md`**.

The finding: IZEN **already owns a runtime-side definition of progress**. It is
not model output and not a step count. Three independent owners exist:

1. **Objective progress** (`internal/execution/objective_conditions.go`,
   `ReduceProgress`/`ReduceProgressWith` + `DecideContinuation`): a function of
   **observed completion conditions**, recomputed from evidence. P2 (new
   evidence), P4 (durable workspace/artifact delta), P5 (verification) and P6
   (objective advancement) are authoritative; P0 (no change), P1 (model tokens)
   and P3 (a capability ran) are not.
2. **Failure ledger** (`internal/runtime/autonomy/failure_ledger.go` +
   `DecideRecoveryWith`): a failure fingerprint repeated under an **unchanged
   evidence epoch** is `NON_PROGRESSING_EXECUTION` and terminates.
3. **RuntimeLoop bounds** (`internal/autonomy/runtime_loop.go`):
   `MaxAttempts`, `MaxExecutionSteps`, `MaxIdenticalDecisions`, `MaxTotalTokens`
   — the safety ceiling, deliberately separate from progress detection.

The five required scenarios are pinned deterministically:

| Case | Result |
|---|---|
| A genuine progress → continuation allowed | **PASS** — `PartiallySatisfied` re-opens via `routeObjectiveContinuation`; a real two-target run reaches `PROVEN` |
| B same output/state → stops | **PASS** — ledger `NON_PROGRESSING_EXECUTION`; loop ceiling aborts identical decisions |
| C repeated no-op capability | **PASS** — a zero-delta success satisfies no condition; `EXECUTION_INERTIA_NO_OP` never completes |
| D repeated mutation attempt | **PASS** — the repeat is named non-progressing and terminates |
| E progress then stall | **PASS (bounded)** — progress clears one epoch; the stall is bounded |

**The one missing transition — closed by R5.1.** On the
**successful-but-partial** path, `routeObjectiveContinuation` used to re-open on
the *static* `PARTIALLY_SATISFIED` projection without comparing attempt N to
attempt N−1, so stall-after-progress was bounded only by the action-based
`MaxIdenticalDecisions` ceiling. R5.1 adds the per-lifecycle authoritative
progress fingerprint and a delta predicate at the router: a continuation is
admissible only when the objective's authoritative state advanced (a newly
satisfied condition, a new verification state, a new evidence epoch, or a new
durable-ledger entry). A repeated partial state keeps the existing typed
non-success (`UNSUBSTANTIATED`); it is never converted into a completion. The
safety ceiling is untouched. Proof: `TestR5_1_CaseA_ProgressThenProgressContinues`,
`TestR5_1_CaseB_IdenticalPartialStateDoesNotReopen`,
`TestR5_1_CaseB_RepeatedStallIsBoundedByTheExistingCeiling`,
`TestR5_1_CaseC_ProvenObjectiveIsUntouched`. See `R5_NON_PROGRESS_REPORT.md` §5.1.

**Forensic coverage added (observability only).** `continuation.evaluated` /
`continuation.selected` now carry `progress`, `previous_progress`,
`progress_delta`, `new_evidence`, `new_artifact`, `mutation_applied`,
`verification_advanced` and `objective_advanced`, computed from the same evidence
the authority judges. `progress_delta` (R5.1) is the composite the router
consumes; the individual flags describe the same transition in more detail.
Nothing reads these fields to decide anything — the router computes its own
delta from the same pure function.

| Critical invariant | Result |
|---|---|
| model output alone counts as progress | **no** (`TestR5_P1_ModelOutputAloneIsNeverProgress`) |
| successful no-op counts as progress | **no** (`TestR5_CaseC_NoOpCapabilitySatisfiesNoCondition`) |
| repeated identical failure stops semantically | **yes** (failure ledger) |
| repeated partial success without a delta stops semantically | **yes** (R5.1 router fingerprint) |
| repeated identical decisions are bounded | **yes** (`RuntimeLoop`, unchanged) |
| completion remains evidence-gated | **yes** (authority is downgrade-only) |
| live low-progress run stops boundedly | **yes** (see report §7) |

Reproduce:

```
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r5/ -v -timeout 1500s
# deterministic, no model:
go test ./internal/execution/        -run TestR5_ -v
go test ./internal/runtime/autonomy/ -run TestR5_ -v
go test ./internal/progress/         -run TestR5_ -v
```

---

## R5.1 STATUS: PROVEN

R5.1 closes the one boundary R5 recorded. The continuation router now compares
the objective's **authoritative progress fingerprint** across lifecycle attempts
before re-opening a `PARTIALLY_SATISFIED` objective.

**Corrected invariant.**

```text
activity       ≠ continuation
continuation   ≠ progress
continuation requires an authoritative progress delta since the previous
               continuation evaluation
completion     requires PROVEN evidence
stall          ≠ success, and cannot earn an unbounded continuation
```

The fingerprint is built only from runtime-owned cumulative facts — the
recomputed condition set (status + verification state), the `FailureLedger`
evidence epoch, and the durable post-mutation / requirement-discharge ledgers.
No model text, token count, timestamp, provider call id or attempt id
participates. A repeated partial state keeps the existing typed non-success
(`UNSUBSTANTIATED`); it is never converted into a completion. The
`RuntimeLoop.MaxIdenticalDecisions` ceiling is unchanged and remains an
independent safety bound.

Proof: `go test ./internal/runtime/autonomy/ -run TestR5_1_ -v` (plus the full
suite). Rationale: `R5_NON_PROGRESS_REPORT.md` §5.1.

---

## R6-A STATUS: PROVEN

R6-A asks whether IZEN has a real runtime-owned **cancellation** boundary. Full
report: **`R6_CANCELLATION_REPORT.md`**.

The finding: cancellation is a real boundary with **two linearization points**,
and it already reuses an existing typed cancellation state at the attempt level.

- **Request owner:** the UI operation (`operation.Cancel`, driven by Esc×2 /
  Ctrl+C / Ctrl+D / OS SIGINT-SIGTERM) or `Driver.Abort` for a parked run.
- **State:** the withdrawn `context.Context`
  (`m.activeOp.Ctx → Driver.runCtx`). A separate kernel/task path uses
  `TaskRuntime.canceled`.
- **Propagation:** `activeOp.Ctx → runCtx → adapter → RuntimeExecutor →
  provider HTTP / shell / mutation boundary`.
- **Linearization:** the inter-step gate (`observeAndRun`, driver.go:2244) and
  `RuntimeLoop.Step` (`runtime_loop.go:947`). Whichever observes the withdrawal
  first wins.
- **Final state:** loop `RuntimeAborted` + `FailurePermanent` + reason
  `"context cancelled"`; attempt `OutcomeCancelled` (`"cancelled"`) emitted as
  `execution.finished(success=false, outcome="cancelled")`. There is **no**
  distinct loop-level cancellation state — recorded as observation gap (B), not
  a defect.
- **Provider cancellation:** real — `http.NewRequestWithContext`; observed live
  with `error=cancelled` on a real in-flight call.
- **Capability cancellation:** shell yes (`exec.CommandContext`); the read-only
  `CapabilityToolRunner` seam ignores ctx (bounded, non-mutating) — limitation
  (E).
- **Late callbacks:** a late provider/stream/capability result is consumed into
  an already-terminal (or about-to-terminate) loop; it can never become
  `PROVEN`.
- **Mutation already committed:** never rolled back by cancellation.
  `cancel != rollback`. (An open `MutationSet` and an in-flight DAG plan roll
  back by transaction semantics; that is atomicity, not `cancel == rollback`.)

| Critical invariant | Result |
|---|---|
| cancel while provider active → typed abort, zero delta | **yes** |
| cancelled execution cannot become `PROVEN` via late callback | **yes** |
| committed mutation survives cancellation | **yes** |
| late stream chunk cannot resurrect execution | **yes** |
| selected continuation does not start after cancel | **yes** |
| completion/cancel race deterministic | **yes** (cancel observed at `Step` wins) |

**One defect found and fixed (F1, classification C — lifecycle semantics).** A
cancellation at the mutation boundary was reported as `apply_failed` (graph
`execution.failed`), indistinguishable from a genuine write failure. The
smallest correction is in `RuntimeExecutor.Approve`: a `context.Canceled` apply
error is classified `OutcomeCancelled` and the graph is cancelled (nil error),
while a `context.DeadlineExceeded` apply timeout keeps its failure
classification. Regression:
`TestR6_MutationBoundary_CancelBeforeAuthorizationLeavesZeroDelta`.

Deterministic proof: `go test ./internal/execution/ -run TestR6_ -v` and
`go test ./internal/runtime/autonomy/ -run TestR6_ -v`. Live proof:
`IZEN_LIVE_FORENSICS=1 go test ./test/live_r6/ -v -timeout 500s` (real
`ollama/qwen2.5-coder:7b`; observed a real in-flight call cancelled, terminal
`aborted`, workspace byte-identical, no `PROVEN`).

---

## R6-B STATUS: PROVEN

R6-B asks whether an interrupt/cancel applies to exactly the intended execution,
or whether active, queued and parked work can interfere. Full report:
**`R6_INTERRUPT_QUEUE_REPORT.md`**.

The finding: **there is no queue of independent executions.** IZEN is
single-lane at every owner, and a second unit of work is *refused*, never
enqueued:

- `autonomy.Driver.Run` refuses while the loop is active **or** parked
  (`driver.go:589-591`);
- the UI `runAutonomousDriver` refuses while active or parked
  (`autonomous.go:131-136`), and `admitNewExecutionRun` refuses a new execution
  run from the driver's own boundary (`execution_admission.go:155-162`;
  already pinned by `TestInvariant6_NewExecutionIsRefusedAtAdmissionWhileARunIsParked`);
- `runtime.kernel.Engine.Open` refuses a second execution per engine
  (`kernel/engine.go:245-247`); `RuntimeEngine` is bound to one task.

Queues that do exist — the plan `TaskLedger` sliding window, the DAG ready set +
worker pool, the bounded compaction runner, the event-bus queues — are all
**intra-execution sub-work** governed by one execution's context, not
independent executions.

| Critical invariant | Result |
|---|---|
| cancellation is execution-scoped (per-run ctx / per-engine flag) | **yes** |
| one execution's context cannot cancel another | **yes** |
| late callbacks/decisions cannot cross execution boundaries | **yes** |
| cancelling active work does not cancel queued work | **yes** (no queue; a second unit is refused) |
| cancelling parked work does not interrupt active work | **yes** |
| parked abort cannot silently resume | **yes** |
| continuation keeps its original execution identity | **yes** (`RunID()` stable across `Resume*`) |
| terminal state immutable wrt unrelated executions | **yes** |

**No lifecycle defect found (no classification C); no production code changed.**
One observability/generality limitation is recorded (B): the UI
`handleEmergencyInterrupt` teardown is model/process-scoped
(`cancelAllBackgroundContexts`, `KillAllOrphans`, shared workflow-SM reset). It
cannot cross an execution boundary today because admission refuses a second run,
but it is the "global cancellation side effect" shape and would need scoping if a
second lane were ever admitted. The smallest boundary is recorded as a
recommendation only.

Deterministic proof:
`go test ./internal/runtime/autonomy/ -run TestR6B_ -v`,
`go test ./runtime/kernel/ -run TestR6B_ -v`,
`go test ./internal/ui/ -run TestR6B_ -v`.

Verification (after R6-B):

```
go test -count=1 ./...        PASS  (exit 0; all packages)
go test -race -count=1 ./...  PASS  (exit 0; 211 packages ok; no data race, no panic)
go vet ./...                  PASS  (no findings)
```

Scope: three new test files, `R6_INTERRUPT_QUEUE_REPORT.md`, and this handoff.
No production file changed.

---

## What is broken RIGHT NOW

Nothing in the repository. Eight defects are fixed:

| ID | Defect | Fixed in |
|---|---|---|
| R1 | `$prompt`/`$hot` directive never reached the Driver → `ScopeNone` → behavioral gate granted **no Execute, no Network** → every "fix/verify/renders" objective downgraded to `BEHAVIORALLY UNPROVEN` | `driver.go` `WithScope`/`SetScope`, `ui/autonomous.go`, `ui/intent_dispatch.go` |
| R2 | `Skip(StageVerification)` published nothing, **and** the executor routed both "no contract exists" and "never reached" through that one call → "not applicable" indistinguishable from "never ran" → false `UNVERIFIED_MUTATION` | `graph.go` (`Skip` / `NotApplicableVerification` / `BeginVerification`), `executor.go` `publishVerification`, `events.go` |
| R3 | `ProviderExecutionPayload` recorded neither the requested budget nor the effective ceiling | `executor.go` |
| R4 | `execution.authorized`, `.spec.frozen`, `continuation.evaluated/.selected`, `objective.evaluated`, `execution.behavior.observed` did not exist | `internal/events`, `runtime/autonomy/forensics.go`, `runtime/autonomy/behavior.go` |
| R5 | Forensic reader raced its own bus subscription, dropping the verdict tail | `internal/forensics` `Recorder.WaitFor`/`WaitQuiet` |
| R6 | `COMPLETION_WITHOUT_EVIDENCE` compared against `"proven"` while the canonical value is `PROVEN` — **the detector could never fire** | `forensics/trace.go` |
| R7 | `UNVERIFIED_MUTATION` fired on the **absence** of any verification record — an accusation manufactured from missing evidence | `forensics/trace.go` |
| R8 (found by R2) | `IsCasualChat` matched a greeting word **inside a repair instruction** (`fix it to "Hello"`), routing a mutating objective to the zero-context `direct_response` path — the frozen spec showed `intent=MUTATE` with a read-only strategy | `gateway/chat.go` Rule 2b |

Also fixed: a **pre-existing** break where `internal/context` shadowed stdlib
`context` in `execution_test.go`, which took down the whole `internal/execution`
test binary. Present since `f40f369`. That binary now compiles and runs; it is
executed explicitly above rather than assumed.

---

## What is PROVEN (by measurement, not by reading code)

- **R1: wiring `$prompt → ScopeDynamic → Execute grant` changes actual execution
  behaviour.** See above. The behavioural gate now serves and fetches the
  workspace on a `$prompt` run and is refused at the authorization boundary
  without one.
- The runtime **executes, verifies, refuses and rolls back truthfully.**
  Benchmark E: created `main.go`, ran real `go fmt`/`vet`/`build`/`test`,
  verification **failed**, mutation **rolled back**, `main.go` **does not exist**,
  run ended `aborted`. No false success.
- The runtime **invents no work**. Benchmark D: a read-only objective produced
  **zero** mutations.
- The runtime **bounds its failures**. Benchmark F: a permanently-failing
  provider cost **2** calls, then parked. Never `completed`.
- The approval gate is **real**. Benchmark A: the file was byte-identical before
  approval.
- **Continuation is real, not a replay.** Benchmark B: on `finish_reason=length`,
  call #2 used a **different prompt** (`identical prompt: false`) at the **same**
  budget. `NON_PROGRESSING_CONTINUATION` did not fire.
- **Budgets are derived, not fixed.** `replace_block` → 1024,
  `create_file` medium → 4096, then clamped to `min(requested, 980)` for free-tier
  / `max_output ≤ 1024` models. The configured `max_tokens: 4096` is a profile
  default that is almost never what is sent.
- **The forensic record separates a permission from an observation.** The live
  arm shows `granted = [7 capabilities]` and `executed = [3]` side by side, and
  the control arm shows `granted = [3]`, `executed = [1]`, refused.
- **There is exactly one loop owner.** No competing loops.

---

## Verification observability (R2 closure)

The forensic trace now distinguishes all six states, and absence is never read as
a verdict:

| State | Published by | Means |
|---|---|---|
| `PASSED` | `Graph.CompleteVerification(true, …)` | the gate ran and every step held |
| `FAILED` | `Graph.CompleteVerification(false, …)` | the gate ran and a step did not hold |
| `NOT_APPLICABLE` | `Graph.NotApplicableVerification(reason)` | the gate **was consulted** and no contract exists |
| `SKIPPED` | `Graph.Skip(StageVerification, reason)` | the boundary was **never crossed** |
| `STARTED` | `Graph.BeginVerification()` (own event type) | the gate was entered; verdict not yet published |
| `UNKNOWN` | — no record | absence of evidence. **Not** proof that verification did not happen |

Rules now enforced by tests:

- `SKIPPED` and `NOT_APPLICABLE` are different transitions with different call
  sites. They previously shared one, which was the defect.
- The gate-entry record is its **own event type**, because
  `verification.completed` is ordered strictly after `mutation.completed` by
  `TestTruthMatrix_CanonicalEventOrdering`. Entry and completion are different
  facts.
- `UNVERIFIED_MUTATION` fires **only** on a positively observed hole: the trace
  shows the gate was entered and never concluded.
- Absence of a verification record yields `UNKNOWN`, which the trace *states*.
  A reader that manufactures an accusation from missing evidence is worse than
  no reader.

---

## What changed

```
NEW  internal/forensics/trace.go             reconstruction + render + 6 detectors
NEW  internal/forensics/ndjson.go            post-mortem reconstruction from disk
NEW  internal/runtime/autonomy/forensics.go  emitters only — no decision authority
NEW  test/forensics/                         27 tests, all print their trace
NEW  test/forensics/r1_scope_regression_test.go   the R1 regression + control arm
NEW  test/forensics/verification_observability_test.go  the six states + no-cry-wolf
NEW  test/live_r1/                           opt-in live experiment (real model)
NEW  internal/execution/verification_publish_test.go  the executor's routing seam
NEW  test/live_r2/                           opt-in R2 real-agentic-repair experiment
NEW  internal/runtime/autonomy/r2_boundary_test.go  deterministic pin of the R2
                                     blocked boundary (no model)
NEW  docs/report/EXECUTION_FORENSICS.md
NEW  docs/report/EXECUTION_FORENSICS_STATE.md

MOD  internal/events/events.go       verification Outcome vocabulary; 2 new
                                     constructors; behavior.observed payload;
                                     provider budget fields
MOD  internal/events/bus.go          the forensics records are CONTROL events
MOD  internal/execution/executor.go  publishVerification; BeginVerification;
                                     provider budget fields
MOD  internal/execution/graph/graph.go   Skip/Skip-publishes; NotApplicable;
                                          BeginVerification
MOD  internal/runtime/autonomy/driver.go        Scope propagation + emitters
MOD  internal/runtime/autonomy/behavior.go      emits the pass's own evidence
MOD  internal/runtime/autonomy/objective_completion.go  objective.evaluated
MOD  internal/ui/{autonomous,intent_dispatch}.go         SetScope push
MOD  internal/ui/autonomous_test.go  fake records the directive
MOD  internal/execution/execution_test.go   pre-existing build break
MOD  internal/architecture/execution_invariants_test.go  4 verification
                                     constructors pinned to the graph
MOD  internal/gateway/chat.go        R2: a workspace action in a message is never
                                     casual chat, even when it writes the value
                                     "Hello" (Rule 2b)
MOD  internal/gateway/chat_test.go   R2 regression (repair-with-"Hello" cases)
MOD  internal/execution/strategy/strategy_test.go  R2 regression: the repair
                                     objective carries mutation semantics
```

R4 added **no production change** — observability + proof only:

```
NEW  test/live_r4/probe_test.go              R4 live bounded-continuation benchmark
NEW  test/forensics/r4_continuation_test.go  R4 deterministic state-machine (5 tests)
NEW  docs/report/R4_BOUNDED_CONTINUATION_REPORT.md
```

R5 added **observability only** (no authority-bearing production change):

```
NEW  internal/execution/r5_progress_test.go              P0–P6 semantic proof (9 tests)
NEW  internal/runtime/autonomy/r5_nonprogress_test.go    cases A–E + forensics (9 tests)
NEW  internal/progress/r5_nonprogress_test.go            state-delta detector (3 tests)
NEW  test/live_r5/probe_test.go                          live boundedness benchmark (opt-in)
NEW  docs/report/R5_NON_PROGRESS_REPORT.md               the R5 report
MOD  internal/events/events.go                           progress + transition fields on
                                                         ContinuationDecisionPayload
MOD  internal/runtime/autonomy/forensics.go              snapshot/capture/emit progress
MOD  internal/runtime/autonomy/driver.go                 per-run progress reset
MOD  internal/forensics/trace.go                         render progress transitions
```

R6-A added the cancellation proof plus one production correction:

```
NEW  internal/execution/r6_cancellation_test.go            mutation boundary A/B + streaming late chunk
NEW  internal/runtime/autonomy/r6_cancellation_test.go     cancel active/before/late/continuation/race/terminal
NEW  test/live_r6/probe_test.go                            opt-in real-provider cancellation benchmark
NEW  docs/report/R6_CANCELLATION_REPORT.md                 the R6-A report
MOD  internal/execution/executor.go                        F1: cancel at the mutation boundary is
                                                           OutcomeCancelled, not apply_failed
```

R6-B added **deterministic tests only** (no production change):

```
NEW  internal/runtime/autonomy/r6b_lifecycle_isolation_test.go  single-lane refusal, execution-scoped
                                                                cancel, parked abort, late decision,
                                                                continuation identity
NEW  runtime/kernel/r6b_engine_isolation_test.go                per-engine cancel scoping, one execution
                                                                per engine, terminal immutability
NEW  internal/ui/r6b_lifecycle_isolation_test.go                autonomous start refusal while parked,
                                                                parked Ctrl+C does not touch other work
NEW  docs/report/R6_INTERRUPT_QUEUE_REPORT.md                   the R6-B report
```

---

## What must NOT be touched

`runtime/kernel` · `internal/kernelbridge` · `internal/core/authorization` ·
the ContextSpec/ExecutionSpec separation · `ObjectiveCompletionAuthority` ·
`PatchManager` · `MutationSet` · provider transport · dynamic model discovery ·
ohgo.

The evidence did not show a defect in any of them. This work is
**observability + one propagation fix**, not a redesign.

R2 confirmed the boundary rather than reopening it. The R2 benchmark's blocking
transition — a discovered candidate cannot be bound as a target for an objective
that named none and declared no artifact kind — lives in the target-binding /
admission path, so it was **recorded as BLOCKED**, not redesigned. The single R2
change is outside it: the `IsCasualChat` classification guard.

---

## The next EXACT experiment

R6-A is closed **PROVEN** (`R6_CANCELLATION_REPORT.md`). It established: cancel
request → propagation → authoritative cancellation → no resurrection → truthful
final state, with one lifetime-semantics defect fixed (F1). R6-B is closed
**PROVEN** (`R6_INTERRUPT_QUEUE_REPORT.md`): there is no execution queue; a
second unit is refused; cancellation is execution-scoped and cross-execution
callbacks are blocked. The remaining lifecycle questions are deliberately **out
of R6-A/R6-B scope** and MUST NOT be started here: crash recovery, WAL, resume,
disconnect recovery, and R6-C/D. The one recorded observation gap from R6-A
(F2: no distinct loop-level cancellation state; `cancel.requested` /
`cancel.propagated` events absent) is the next candidate decision, not an open
defect, and was left untouched by R6-B.

R2 is closed **BLOCKED**; R3 is closed **PROVEN** (`R3_TARGET_PROPOSAL_REPORT.md`);
R4 is closed **PROVEN** (`R4_BOUNDED_CONTINUATION_REPORT.md`); R5 is closed
**PROVEN** (`R5_NON_PROGRESS_REPORT.md`). R3 identified the single authority
boundary (`EvaluatePreflightAdmission`) and made DISCOVERED/PROPOSED/AUTHORIZED
explicit without granting authority. R4 proved that a real `finish_reason=length`
is continued inside the same execution by runtime-owned logic and never falsely
completed. R5 proved that the runtime already owns a definition of progress
(evidence-reduced completion conditions + the failure ledger), that the five
non-progress scenarios are bounded, and recorded one missing transition: the
per-attempt progress delta on the successful-partial path (report §5). That
boundary is **reported, not implemented** — R5 was instructed not to add a
generic loop detector, and the surviving stop is truthful (`NOT PROVEN`).

R4 resolved the two open items that R1/R3 had left in this section:

- **The per-model budget table is no longer scripted-only.** A live call now
  reports `finish_reason=length` with `requested=1024 effective=1024 known=true`.
  A provider that caps silently is exactly what `effective < requested` records.
- **`ProviderExecutionPayload.OutputChars` is populated on the streaming mutation
  lane** in the R4 live trace (`output_chars=2499` on the truncated call), so the
  delivered prefix is observable even on exhaustion.

The remaining open question is a *policy* question, not an execution one, and it
is explicitly out of R3/R4 scope:

> Should IZEN ever accept a *content-grounded* proposal — a bounded, read-only
> inspection pass that establishes a discovered candidate is what the objective
> is about — and if so, under what contract?

Today the only objective-compatible semantic evidence the authority model
accepts is an **artifact kind the objective declares**, matched by extension
against files read from disk. Inventing a content-grounded proposal would be a
new authority contract, and R3 was instructed not to invent it. If it is ever
pursued, it must pass through the same single boundary and keep model text out
of target resolution.

### Also open

The behavioural repair path (§2, item 6) remains open and distinct: give the live
workspace a genuinely broken document and assert on `execution.behavior.observed`
that a real defect, a real repair and a `repairs > 0` re-observation occur. That
is a separate capability from R2's target discovery.

4. **Executor streaming truncation drops the delivered prefix.** The
   non-streaming `invokeStream` path returns the accumulated bytes with the
   output-gate error so the executor’s full-artifact bounded-step continuation
   can advance the same contract; the streaming path returns `""`. R4 semantics
   are unaffected (the driver recovery layer continues correctly), but the
   smallest correction is a symmetry fix at the three streaming return sites.
   Reported in `R4_BOUNDED_CONTINUATION_REPORT.md` §6; not applied.
5. **Contract recovery (§1, Benchmark C)** was never exercised: the scripted
   prose answer reached an approval gate, so only one call happened. Whether
   `authorizeContractRecovery` produces a *useful* second prompt is untested.
6. **The successful-partial progress delta is a router input (R5.1 — CLOSED).**
   `routeObjectiveContinuation` now re-opens only when the objective's
   authoritative progress fingerprint advanced since the previous continuation
   evaluation; a repeated partial state keeps the existing typed non-success
   (`UNSUBSTANTIATED`). The action-based `RuntimeLoop.MaxIdenticalDecisions`
   ceiling remains as an independent safety bound. Proof:
   `internal/runtime/autonomy/r5_1_progress_delta_test.go`; rationale in
   `R5_NON_PROGRESS_REPORT.md` §5.1.
