# EXECUTION FORENSICS — STATE

**Read `EXECUTION_FORENSICS.md` first.** This file is the handoff.

- **Branch:** `fix/runtime` (base `f40f369`)
- **Suite:** `go test ./...` green; `-race` green on `internal/execution/...`,
  `internal/events/...`, `internal/architecture`, `internal/runtime/...`,
  `internal/ui/...`, `test/forensics`
- **Status:** **R1 PROVEN by live execution.** The remaining items are open
  questions, not open defects.

---

## Where we are

The `$prompt` path is fully observable. A bounded run is reconstructable from
`internal/forensics` — live, or from `.izen/audit/events.ndjson` after the
process exits.

Seven defects found and fixed. The full trace, evidence and reasoning are in
`EXECUTION_FORENSICS.md`.

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

## What is broken RIGHT NOW

Nothing in the repository. Seven defects are fixed:

| ID | Defect | Fixed in |
|---|---|---|
| R1 | `$prompt`/`$hot` directive never reached the Driver → `ScopeNone` → behavioral gate granted **no Execute, no Network** → every "fix/verify/renders" objective downgraded to `BEHAVIORALLY UNPROVEN` | `driver.go` `WithScope`/`SetScope`, `ui/autonomous.go`, `ui/intent_dispatch.go` |
| R2 | `Skip(StageVerification)` published nothing, **and** the executor routed both "no contract exists" and "never reached" through that one call → "not applicable" indistinguishable from "never ran" → false `UNVERIFIED_MUTATION` | `graph.go` (`Skip` / `NotApplicableVerification` / `BeginVerification`), `executor.go` `publishVerification`, `events.go` |
| R3 | `ProviderExecutionPayload` recorded neither the requested budget nor the effective ceiling | `executor.go` |
| R4 | `execution.authorized`, `.spec.frozen`, `continuation.evaluated/.selected`, `objective.evaluated`, `execution.behavior.observed` did not exist | `internal/events`, `runtime/autonomy/forensics.go`, `runtime/autonomy/behavior.go` |
| R5 | Forensic reader raced its own bus subscription, dropping the verdict tail | `internal/forensics` `Recorder.WaitFor`/`WaitQuiet` |
| R6 | `COMPLETION_WITHOUT_EVIDENCE` compared against `"proven"` while the canonical value is `PROVEN` — **the detector could never fire** | `forensics/trace.go` |
| R7 | `UNVERIFIED_MUTATION` fired on the **absence** of any verification record — an accusation manufactured from missing evidence | `forensics/trace.go` |

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
```

---

## What must NOT be touched

`runtime/kernel` · `internal/kernelbridge` · `internal/core/authorization` ·
the ContextSpec/ExecutionSpec separation · `ObjectiveCompletionAuthority` ·
`PatchManager` · `MutationSet` · provider transport · dynamic model discovery ·
ohgo.

The evidence did not show a defect in any of them. This work is
**observability + one propagation fix**, not a redesign.

---

## The next EXACT experiment

R1 is closed. The highest-value remaining question is **§2, item 6**:

> The behavioural **repair** path has only been exercised with a scripted
> proposer. The live run PROVED a clean observation with 0 repairs.

Concretely: give the live workspace a **genuinely broken** document — an
unclosed element, or a `<link href>` naming a stylesheet that does not exist —
and assert on the structured `execution.behavior.observed` record that:

1. the observation reports a real `Defects` entry with real evidence;
2. the model proposes a repair;
3. the runtime selects the target **from the evidence**, not from the proposal;
4. `repairs > 0` and the re-observation holds;
5. the mutation is visible **on disk**, and the run still ends `completed`.

The control that makes it meaningful: the same broken workspace with the
directive withheld must be refused, exactly as the R1 control arm was.

Do **not** guess if it fails. Read `execution.behavior.observed` — its
`Executed`, `Defects` and `BlockClass` fields name which capability or boundary
diverged, which is the whole point of R4.

### Also open

1. **§14's per-model budget table is still scripted-only.** Every live call in
   the R1 experiment reported `finish=stop` with no truncation, so
   `effective` budget remains `unobserved`. A provider that caps silently is
   still undetectable.
2. **`ProviderExecutionPayload.OutputChars` is 0 on the mutation lane** (the
   manifest path populates it). Cosmetic.
3. **Contract recovery (§1, Benchmark C)** was never exercised: the scripted
   prose answer reached an approval gate, so only one call happened. Whether
   `authorizeContractRecovery` produces a *useful* second prompt is untested.
