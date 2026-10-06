# EXECUTION FORENSICS — STATE

**Read `EXECUTION_FORENSICS.md` first.** This file is the handoff.

- **Branch:** `fix/runtime` (base `f40f369`)
- **Suite:** `go test ./...` green; `-race` green on `internal/execution/...`,
  `internal/events/...`, `internal/architecture`, `internal/runtime/...`,
  `internal/ui/...`, `test/forensics`
- **Status:** **R1 PROVEN** by live execution. **R2 BLOCKED** by live
  execution — recorded, not worked around. The remaining items are open
  questions, not open defects.

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

R2 is closed **BLOCKED**. The next experiment is **not** R3 and **not** token
budgeting (see the R2 result above). When it is started, it must answer this
singular question:

> Can IZEN turn *discovered evidence* into an authorized target through a
> bounded **inspection** pass, without letting a scan choose the target?

The design constraint is already stated by the runtime: discovery is evidence,
never authority (`I13`). So the missing capability is `discovery → inspect →
decide`: a read-only pass that shows the model the discovered candidate file(s)
and lets a **decision** (grounded in those bytes, admitted through the existing
authorization boundary) name the target. The R2 diagnostic proves everything
after that point already works end to end with a real model.

Concretely, the R3 acceptance is the R2 benchmark passing:
`inspect this project, find the incorrect greeting, fix it to "Hello", and verify
the result.` must reach `PROVEN` with `index.html` discovered, inspected, mutated
on disk, and verified — with the model's text never treated as evidence.

### Also open

The behavioural repair path (§2, item 6) remains open and distinct: give the live
workspace a genuinely broken document and assert on `execution.behavior.observed`
that a real defect, a real repair and a `repairs > 0` re-observation occur. That
is a separate capability from R2's target discovery.

1. **§14's per-model budget table is still scripted-only.** Every live call in
   the R1 experiment reported `finish=stop` with no truncation, so
   `effective` budget remains `unobserved`. A provider that caps silently is
   still undetectable.
2. **`ProviderExecutionPayload.OutputChars` is 0 on the mutation lane** (the
   manifest path populates it). Cosmetic.
3. **Contract recovery (§1, Benchmark C)** was never exercised: the scripted
   prose answer reached an approval gate, so only one call happened. Whether
   `authorizeContractRecovery` produces a *useful* second prompt is untested.
