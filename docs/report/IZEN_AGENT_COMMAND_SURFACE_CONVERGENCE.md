# IZEN — Agent Command Surface and Runtime Integration Convergence

Date: 2026-10-09 · Branch: `fix/execution` · HEAD at start and at report time:
`a13178f` (`feat(exec): unify CLI on authoritative runtime`), working tree dirty
with this change set only (no commit made).

Distinctions used throughout:

* **VERIFIED** — proven by a test that ran on this working tree, or by a live
  subprocess run of the real binary.
* **OBSERVED** — read in the current source, with a citation.
* **REMAINING / NOT VERIFIED** — a known limitation, disclosed and not claimed.

---

## 1. Verified repository state

Commands actually run and their results:

```
git rev-parse HEAD      -> a13178f023be3136c4f2187cf923569864b3db1a
git status --porcelain  -> only the files listed in §11 (no reset/clean/stash)
go build ./...          -> clean
go vet ./...            -> clean
go test -race -count=1 ./...  -> see §10 (full suite)
```

Baseline before editing (preserved, not overwritten): the tree was clean at
`a13178f`; a full `go test -race -count=1 ./...` was run first and passed
(`baseline_exit=0`). No pre-existing user change was reset, cleaned or
overwritten.

---

## 2. Existing and proposed command hierarchy

### 2.1 Existing surface before this change (OBSERVED)

`cmd/izen/main.go` dispatched a flat `switch`:

```
izen / izen [path]         TUI
izen version|help
izen auth login            stub ("not yet implemented")
izen stats                 stub ("not yet implemented")
izen config style <policy>
izen compact | memory optimize
izen debug [path]
izen run [-dir] [-target]... "<prompt>"        interactive [y/N] only
izen prompt "<objective>"                      alias of orchestrate
izen orchestrate [-dir] "<objective>"          interactive [y/N] only
izen rollback
```

There was **no non-interactive execution contract, no structured output, no
documented exit-code semantics, no model/provider selection flag, no durable
task listing and no resumable command**. `izen run` and `izen orchestrate`
always blocked on `[y/N]`; provider/model selection was global config only.
This is the gap the convergence brief targets.

### 2.2 Proposed hierarchy (implemented)

```
izen                                 TUI (unchanged)
izen [path]                          TUI at path (unchanged)

Execution — one authoritative runtime
  izen run [flags] "<objective>"     direct execution
  izen prompt [flags] "<objective>"  alias, surface $prompt
  izen orchestrate [flags] "<objective>"  alias, surface $orchestrate
  izen resume [flags]                continue the recorded non-terminal objective
  izen runs [-dir]                   list durable execution tasks (read-only)

Inspect / configure
  izen models [use <provider> <model>]
  izen auth status
  izen config show | get <key> | style <policy>
  izen stats [-dir] [-json]
  izen debug [path]
  izen compact | izen memory optimize

  izen version | izen help
```

**Retained / aliased:** `prompt`, `orchestrate`, `config style`, `compact`,
`memory optimize`, `debug`, `rollback` are all retained. `prompt` and
`orchestrate` are now thin aliases of the same execution seam rather than
separate lifecycles.

**Deferred (documented, not implemented):**

* **`izen serve` (HTTP/API boundary)** and **ACP / other agent-protocol
  adapters**. Justification: the runtime exposes one in-process lifecycle
  (`compose.Wire` → `autonomy.Driver` → `ExecutorAdapter` → `RuntimeExecutor`).
  A service boundary requires a *new contract* — concurrent run isolation,
  authentication, and a transport-level run/approve/stream protocol — that does
  not exist yet. Building it now would either duplicate the driver or expose an
  unaudited second execution loop, which the brief forbids. This is the precise
  blocker: **the runtime has no external, run-scoped request/approval transport
  contract**; the smallest safe contract in scope is the subprocess JSON
  contract delivered here (`izen.run.v1`), which an external service can already
  consume.

### 2.3 Flags (documented and effective)

`izen run` / `izen prompt` / `izen orchestrate` / `izen resume`:

| Flag | Effect | Reaches |
| --- | --- | --- |
| `-dir <path>` | workspace root | `compose.WithRoot` |
| `-target <path>` (repeatable) | explicit target set | `Driver.SetTargets` |
| `-provider <name>` | provider override | `resolveSelection` → `buildProvider` |
| `-model <id>` | model override | authority binding + executor `resolveModel` |
| `-base-url <url>` | endpoint override | provider constructor |
| `-mode <name>` | ask/investigate/plan/build/review | `SubmitPromptCmd.Mode` (validated via `handlers.ParsePhase`) |
| `-approve <policy>` | ask/always/never | `DriverTerminalBridge.WithPolicy` |
| `-timeout <dur>` | wall-clock bound | `context.WithTimeout` |
| `-json` | machine-readable stdout | report encoder |
| `-quiet` | suppress diagnostics | stderr diagnostics only |
| `-resume` | continue recorded objective | durable `TaskStore.MostRecentRecoverable` |

Every flag was verified to reach the runtime: an unknown flag, an invalid mode,
an invalid approval policy, an invalid timeout, an unknown provider and a
provider/model mismatch are all rejected with exit 2 **before** any provider
call (`TestParseExecArgs`, `TestCLIBench_InvalidModelSelection`, which asserts
`provider.Calls() == 0`).

---

## 3. Runtime access topology

There is exactly one execution lifecycle. Every surface below crosses the same
admission front-end and the same driver:

```
                 ┌─────────────────────── human surfaces ───────────────────────┐
                 │ izen run / prompt / orchestrate / resume   TUI $prompt /build │
                 └───────────────┬───────────────────────────────┬──────────────┘
                                 │                               │
          cmd/izen: parseExecArgs │                     internal/ui: runAutonomyRoutedCmdExplicit
                                 ▼                               ▼
          runExecRequestAs → runAuthoritativeCommand   (same admission + decision)
                                 │
                                 ▼
              compose.Wire  (the single composition root)
                 • Runtime facade (submit_prompt preflight dispatch)
                 • autonomy.Engine (session grant ledger)
                 • RuntimeExecutor (provider seam, mutation, verification)
                 • intent gateway, authorization engine, durable ledger, audit
                                 │
                                 ▼
              runtimeAutonomy.NewDriver  (bounded Observe→Decide→Execute→Interpret)
                                 │
                                 ▼
              cli.RunAuthoritative  (plays the human at parked boundaries only)
```

Evidence:

* `TestExecutionOwnership_AuthoritativeCommandWiresRuntimeAndDriver`
  (VERIFIED) pins `runAuthoritativeCommand` → `compose.Wire` +
  `cli.RunAuthoritative`, and forbids `NewDriver`/`NewPipeline`/`RunCycle`/
  `NewOrchestrator`/`NewProposalProvider`/`NewTerminalBridge` in the seam.
* `assertAuthoritativeExecSeam` (VERIFIED) pins the shared wrapper
  `runExecRequestAs` → `runAuthoritativeCommand` and forbids the same.
* `TestExecutionOwnership_CLIRunDrivesAuthoritativeDriver` and
  `..._CLIOrchestrateDrivesAuthoritativeDriver` (VERIFIED, updated transitively)
  pin that `run`/`orchestrate` cross the shared seam and construct no competing
  lifecycle.
* `TestExecutionOwnership_AllExecutionSurfacesConvergeOnOneRuntime` (VERIFIED,
  unchanged) pins the TUI surfaces onto the single runtime seam.

The CLI owns **no** proposal loop, provider call, planner, approval gate or
terminal decision. Its only human role is rendering the runtime's own candidate
review and issuing the candidate-bound authorization on approve
(`cli.RunAuthoritative`, `authorize` closure in `cmd/izen/authoritative.go`).

---

## 4. CLI/TUI parity evidence

Both surfaces are wired by the same composition root and drive the same driver;
the CLI only differs in its input/output adapter.

* **Same driver, same admission.** TUI: `internal/ui/runtime_cutover.go` →
  `runAutonomyRoutedCmdExplicit` → autonomy decision → `compose` driver. CLI:
  `runAuthoritativeCommand` → `compose.Wire` → `Driver`. Both cross `compose.Wire`
  and `cli.RunAuthoritative`-equivalent lifecycle.
* **Subprocess parity (VERIFIED).** `TestCLIBench_SurfaceParity` runs the
  identical creation objective through `izen run`, `izen prompt` and
  `izen orchestrate` and asserts, for each: exit 0, `outcome=proven`, exact
  artifact bytes, and the truthful `surface` (`$run`/`$prompt`/`$orchestrate`).
* **Interactive parity (VERIFIED).** `TestCLIBench_InteractiveApproval` answers
  `y` on stdin and asserts the candidate was reviewed, authorized and applied
  through the default `-approve ask` policy — the same boundary the TUI renders.
* **Direct-chat parity gap (REMAINING, pre-existing).** A conversational prompt
  is not answered directly; the runtime is workspace-evidence-bound and
  terminates with an honest non-proof rather than fabricating an answer (Phase 3
  report §8.1, unchanged by this work).

---

## 5. Session and resume semantics

Durable state is the runtime's `durable.TaskStore` at `.izen/runtime/`
(`ledger.ndjson` + derived `snapshot.json`). Audit is the append-only
`.izen/audit/events.ndjson` written by `compose`'s audit logger.

* `izen runs` (read-only) lists every non-terminal durable task
  (`TaskStore.RecoverableTasks`). It resumes or mutates nothing.
* `izen resume` reads `TaskStore.MostRecentRecoverable`, then re-drives the
  **recorded objective and scope** through the same authoritative runtime as
  `izen run`. It is a *continuation*, not a replay of a frozen in-memory loop:
  the runtime re-observes the live workspace and re-derives authority, so it can
  never reuse a stale authorization. The report records `resumed_from`.
* **VERIFIED:** `TestCLIBench_ResumeContinuation` parks a run on a
  non-progressing artifact (empty content), confirms `izen runs` lists the
  recoverable task, then `izen resume -approve always` against a fixed provider
  completes the creation with the exact bytes and `outcome=proven`.
* **Clarification invalidation** is preserved: a clarify boundary left
  unanswered aborts the run (`RunAuthoritative` → `Driver.Abort`), and a resumed
  objective re-derives scope from scratch (`bindAuthoritativeTargets`).
* **REMAINING:** the loop does not persist a resumable mid-loop program state.
  Resume is "recorded objective + live reconciliation", which is exactly what
  the durable journal supports. A resume of a task whose status is already
  terminal is correctly refused (`Run` duplicate-start guard / no recoverable
  task → exit 1). The benchmark does not exercise a hard `SIGKILL` mid-mutation
  resume; the durable reconcile surfaces (`ReconcileInterrupted`,
  `InspectCursors`) exist but are not exercised by a subprocess test here.

---

## 6. Non-interactive approval and failure semantics

The approval policy selects behaviour only at the human surface; it changes
nothing about admission, authorization, mutation or verification.

| Policy | Approval boundary | Clarify boundary |
| --- | --- | --- |
| `ask` (default) | renders review, blocks on `[y/N]` | renders options, reads a target |
| `always` | renders review, auto-approves | cannot answer → abort with `clarify_required` |
| `never` | renders review, auto-rejects | cannot answer → abort with `clarify_required` |

* **Authorization still binds to the exact candidate.** For `always`, the same
  `authorize(b.CandidateTargets, b.CandidateID, b.CandidateDigest)` closure runs
  before `ResumeApprove`, so approval binds to the reviewed identity and digest
  (`TestRunAuthoritativeAlwaysPolicyApproves`, VERIFIED).
* **Denial is never a silent apply.** `TestCLIBench_ApprovalDenied` proves
  `-approve never` yields exit 7 / `outcome=denied` and creates **no file**
  (VERIFIED).
* **No unauthorized mutation through non-interactive execution.** Auto-approve
  is explicit (`-approve always`) and still requires the runtime's checkpoint-
  gated, candidate-bound authorization. Auto-deny applies nothing.
* **Clarification fails closed.** Under a non-interactive policy the bridge
  returns an empty decision; the lifecycle aborts and reports
  `clarify_required` (exit 4). `TestCLIBench_ReadOnlyAndAmbiguity/ambiguity`
  proves an ambiguous objective under `-approve always` yields exit 4 and
  mutates no candidate file (VERIFIED).
* **Provider failure.** A provider/transport failure is retried by the runtime's
  bounded recovery and, if it parks, is surfaced as `provider_failed` (exit 3),
  never success (`TestCLIBench_ProviderFailure`, VERIFIED).
* **False success is structurally detected.** An empty artifact that completes
  the stream does not create a file and does not report `proven`
  (`TestCLIBench_FalseSuccessDetection`, VERIFIED).

---

## 7. Structured output and exit-code contract

`-json` emits **one** JSON object on stdout (`schema: izen.run.v1`); all human
diagnostics and the candidate review go to stderr. The report is derived only
from runtime state:

```json
{
  "schema": "izen.run.v1",
  "surface": "$run",
  "provider": "opencode", "model": "scripted/model-large",
  "workspace": ".", "objective": "...", "mode": "build",
  "outcome": "proven", "reason": "objective satisfied ...",
  "approved": true, "inspected": false, "rejected": false,
  "clarify_required": false,
  "objective_contract": { "progress": "PROVEN", "outcome": "PROVEN",
                          "continuation": "COMPLETE", "conditions": [ ... ] },
  "artifacts": { "applied": ["note.txt"], "candidate_targets": ["note.txt"],
                 "candidate_digest": "...", "operation": "CREATE" },
  "usage": { "input_tokens": 12, "output_tokens": 7, "known": true },
  "elapsed_ms": 98, "exit_code": 0
}
```

* **Completion vs proof.** `outcome` is the process classification; the nested
  `objective_contract.progress` (`PROVEN` / `UNSUBSTANTIATED` / `FAILED` / …) is
  the runtime's evidence-backed proof state. They are deliberately distinct.
* **No fabricated evidence.** Absent facts are omitted (`omitempty`), not
  invented; token usage carries `known:false` when the provider reported none.
* **`TestCLIBench_JSONContract` (VERIFIED)** asserts stdout is a single JSON
  object with the required keys, `schema=izen.run.v1`, `exit_code=0`, and that
  diagnostics appear on stderr.

Exit codes (stable, documented in `izen help` and `izen run -h`):

| Code | Meaning |
| --- | --- |
| 0 | `proven` / `completed` |
| 1 | failed / aborted |
| 2 | usage or configuration error (unknown flag, invalid mode/model/binding) |
| 3 | provider failure (`provider_failed` / propagated provider error) |
| 4 | human required (parked non-resumable boundary, or `clarify_required`) |
| 5 | `unsubstantiated` (terminal, objective not proven) |
| 6 | `timeout` (wall-clock bound) |
| 7 | `denied` (operator or policy declined the mutation) |

`TestCLIBench_Timeout` (VERIFIED) proves the wall-clock bound yields exit 6 and
no artifact. `TestCLIBench_HelpAndExitCodes` (VERIFIED) pins help and exit 2.

---

## 8. Benchmark harness — commands and actual results

The harness is `test/benchmark/cli_harness.go` + `cli_bench_test.go`. It:

1. builds the **real `izen` binary** (`go build ./cmd/izen`);
2. starts a **scripted OpenAI-compatible provider** on 127.0.0.1 (no network,
   no credential) that answers requirement/manifest passes with contract-shaped
   empty payloads and streams the scripted artifact;
3. runs the binary in an **isolated `HOME` and a fresh git workspace**,
   capturing stdout, stderr and exit status;
4. asserts **exact artifact bytes**, the JSON report, exit code and **durable
   evidence** (ledger + audit present).

Exact commands (no TUI, no credentials):

```sh
# Full CLI benchmark suite (builds the binary, uses a local scripted provider)
make bench-cli

# Equivalent direct invocation
go test -run 'TestCLIBench' -v -count=1 -timeout 15m ./test/benchmark

# One scenario
go test -run 'TestCLIBench_FalseSuccessDetection' -v ./test/benchmark

# Investigative dump (skipped by default): subprocess output, JSON report,
# every provider request, durable ledger/snapshot/audit tails
IZEN_CLI_DEBUG=1 go test -run TestCLIDebug -v ./test/benchmark
```

Actual results on this working tree (all executed):

```
--- PASS: TestCLIBench_CreateFile
--- PASS: TestCLIBench_ModifyFile
--- PASS: TestCLIBench_ApprovalDenied
--- PASS: TestCLIBench_InvalidModelSelection
--- PASS: TestCLIBench_ProviderFailure
--- PASS: TestCLIBench_Timeout
--- PASS: TestCLIBench_FalseSuccessDetection
--- PASS: TestCLIBench_ReadOnlyAndAmbiguity (read-only, ambiguity)
--- PASS: TestCLIBench_JSONContract
--- PASS: TestCLIBench_ResumeContinuation
--- PASS: TestCLIBench_InteractiveApproval
--- PASS: TestCLIBench_SurfaceParity (run, prompt, orchestrate)
--- PASS: TestCLIBench_HelpAndExitCodes
ok  github.com/PizenLabs/izen/test/benchmark
```

Coverage mapping to the brief:

| Requirement | Scenario | Assertion |
| --- | --- | --- |
| read-only question | `ReadOnlyAndAmbiguity/read-only` | no mutation; exit 0, PROVEN |
| investigation | covered by read-only (`-mode ask`) | no mutation |
| file creation | `CreateFile` | exact bytes, `proven`, ledger |
| file modification | `ModifyFile` | exact bytes, `proven` |
| ambiguity | `ReadOnlyAndAmbiguity/ambiguity` | exit 4, no mutation |
| approval denial | `ApprovalDenied` | exit 7, no file |
| invalid model selection | `InvalidModelSelection` | exit 2, 0 provider calls |
| provider failure | `ProviderFailure` | exit 3, no file |
| verification failure | `FalseSuccessDetection` (empty artifact) | not `proven`, no file |
| non-interactive execution | all `-json -approve always` scenarios | report + exit contract |
| resume / continuation | `ResumeContinuation` | resumed artifact, `resumed_from` |
| interface parity | `SurfaceParity` | identical artifact across surfaces |

**False-success detection.** No scenario relies on exit code 0 alone: every
successful scenario asserts the exact on-disk bytes, and the anti-false-success
scenario asserts that a stream that reports success but carries no artifact
produces **neither** a file **nor** `outcome=proven`.

---

## 9. Artifact-integrity and durable-state evidence

* **Applied bytes equal reviewed bytes.** The CLI never synthesizes a proposal;
  the review is `autonomy.HumanBoundary` facts (`CandidateDiff`, digest,
  targets) rendered from the runtime's own candidate record. The authorization
  binds `CandidateID` + `CandidateDigest`. `TestCLIBench_CreateFile` and
  `_ModifyFile` assert the final bytes equal the scripted artifact, and
  `TestCLIBench_SurfaceParity` asserts the same bytes across all three surfaces
  (VERIFIED).
* **Durable journal (VERIFIED, captured from a real create run).** After
  `izen run -json -approve always …` on an isolated workspace:

  ```
  .izen/runtime/ledger.ndjson
    {"eventType":"TASK_CREATED",      "payload":{"intent":"Create ...","scope":["note.txt"]}}
    {"eventType":"CHECKPOINT_CREATED","payload":{"checkpointId":"run-1-attempt-0-deciding"}}
    {"eventType":"CURSOR_DISPATCHED", "payload":{"operationId":"run-1-attempt-0","status":"DISPATCHED"}}
    {"eventType":"VERIFICATION_RESULT","payload":{"detail":"outcome=pending_approval ..."}}
  .izen/runtime/snapshot.json
    { "status": "COMPLETED", "lastCheckpointId": "cli-finalize",
      "activeTargetScope": ["note.txt"] }
  .izen/audit/events.ndjson  (non-empty)
  ```

  `assertLedgerEvidence` asserts both files exist for every mutating benchmark
  scenario.
* **Objective proof is evidence-bound.** The `objective_contract` in the report
  is `Driver.ObjectiveReport()` — re-decided from runtime conditions with
  `verification_state` and `evidence_refs`; the CLI never writes it.
* **Audit flush is authoritative.** `runAuthoritativeCommand` propagates
  `app.FlushAudit()` failure as a hard error (exit 1) and materializes the
  derived snapshot at the same boundary the retired headless ledger used.

---

## 10. Test results and unverified areas

Commands run on this working tree:

```
go build ./...                                   clean
go vet ./...                                     clean
go test -race -count=1 ./...                     PASS (final5_exit=0, 0 FAIL)
make bench-cli / go test -run TestCLIBench ...   PASS (13 scenarios)
go test ./cmd/izen/... ./internal/cli/...
     ./internal/architecture/... ./test/architecture/...   PASS
go test -race ./internal/runtime/autonomy -run TestR5_CaseA... -count=10  PASS
```

Focused packages:

```
ok  github.com/PizenLabs/izen/cmd/izen            (parser, resume, models/auth/config/stats, resume fail-closed)
ok  github.com/PizenLabs/izen/internal/cli        (approval policies, clarify fail-closed, authorization ordering)
ok  github.com/PizenLabs/izen/internal/architecture (execution-ownership locks)
ok  github.com/PizenLabs/izen/test/architecture   (kernel / negative-architecture locks)
ok  github.com/PizenLabs/izen/test/benchmark      (subprocess CLI suite)
```

Full-suite result: `go test -race -count=1 ./...` completed with `final5_exit=0`
(0 `FAIL`) on the standalone, frozen tree. The pre-existing architecture lock
`test/architecture/negative_rules_test.go` and the kernel locks still pass
unchanged; the only architecture test changed was the CLI ownership lock
(`internal/architecture/execution_ownership_convergence_test.go`), updated
transitively (see §11).

**Load-sensitive flake (disclosed, not hidden).** While two full `-race` suites
were run concurrently (an accidental overlap of verification runs), one
pre-existing test flaked twice: `TestR5_CaseA_EndToEndMutationReachesProven`
(`internal/runtime/autonomy`). Its post-run assertion reads an asynchronous
event-collector snapshot and lost the delivery race under extreme CPU load; the
runtime behavior it checks (terminal COMPLETED, `ObjectiveProgress()==PROVEN`,
non-zero provider calls) passed. This package is **not touched by this change**.
The test passes 10/10 with `-race` in isolation, 3/3 when run together with the
new benchmark package, and the standalone full suite is clean. No production
code was weakened to hide it. A future change could make the collector `Wait`
for the terminal continuation event instead of reading immediately.

**Unverified / disclosed gaps:**

* No `izen serve` / ACP adapter (deferred contract, §2.2).
* Resume is not tested across a hard process kill mid-mutation; only the
  recorded-objective continuation path is exercised.
* The benchmark's scripted provider is OpenAI-compatible; it does not exercise
  Anthropic/Gemini-specific wire shapes (those paths are covered by their own
  package tests, not by the subprocess harness).
* `test/live_r2` and other live-provider suites are opt-in and were not run
  (no live credential in the benchmark environment); they are unaffected by this
  change and skip/degrade deterministically.

---

## 11. Changed files and remaining limitations

New:

```
cmd/izen/exec.go            shared execution grammar (parseExecArgs, execRequest)
cmd/izen/report.go          izen.run.v1 report + human render
cmd/izen/resume.go          izen resume / izen runs + durable readers
cmd/izen/models.go          izen models [use]
cmd/izen/auth.go            izen auth status (never prints secrets)
cmd/izen/stats.go           izen stats (durable token usage)
cmd/izen/cli_surface_test.go
test/benchmark/cli_harness.go     scripted provider + subprocess runner
test/benchmark/cli_bench_test.go  scenario suite
test/benchmark/cli_debug_test.go  investigative dump (skipped by default)
```

Modified:

```
cmd/izen/main.go            command hierarchy + exit codes + config show/get
cmd/izen/authoritative.go   one execution seam; report; exit classification
cmd/izen/runtime.go         buildProvider with explicit overrides; run handler
cmd/izen/orchestrate.go     run/prompt/orchestrate aliases on the shared seam
cmd/izen/runtime_test.go    parseExecArgs + approval-policy tests
internal/cli/authoritative.go     ApprovalPolicy + bridge policy + result facts
internal/cli/authoritative_test.go policy/clarify tests
internal/architecture/execution_ownership_convergence_test.go  transitive seam lock
Makefile                    bench-cli target
```

**Remaining limitations (truthful):**

1. `izen run` remains interactive by default (`-approve ask`); automation must
   pass `-approve always|never`. This is intentional — the runtime's approval
   boundary is preserved, not bypassed.
2. A provider failure after bounded recovery surfaces as `provider_failed`
   (exit 3) from a parked boundary rather than from a raw error return; the
   report's `outcome`/`reason` state this explicitly.
3. Direct conversational answers are still not surfaced (pre-existing Phase 3
   gap).
4. `izen stats` reports the durable task ledger's token accounting; it does not
   aggregate a separate historical usage store (none exists).
5. External service/API and ACP adapters remain deferred (§2.2).

---

## 12. Truthful final verdict

The command surface and its execution integration converge on **one
authoritative runtime**:

* All execution commands (`run`, `prompt`, `orchestrate`, `resume`) parse
  arguments only and delegate to the single `runAuthoritativeCommand` seam,
  which wires `compose.Wire` and drives the autonomy `Driver` over the
  `RuntimeExecutor`. No command owns an execution loop or a definition of
  success. VERIFIED by AST locks and by subprocess parity tests.
* Automation is real: explicit workspace/model/mode/target/timeout selection,
  bounded wall-clock, machine-readable `izen.run.v1` output, stable exit codes,
  and explicit approval/clarification outcomes — each reaching the runtime and
  each with a documented, tested effect.
* The benchmark harness invokes the real binary as a subprocess against a
  scripted local provider, asserts exact artifact changes and durable evidence,
  and **detects false success**. VERIFIED by 13 passing scenarios.
* Invariants are preserved: model output is untrusted; the runtime owns authority
  and state; approval binds to the exact canonical mutation; objective state is
  evidence-backed; non-interactive execution cannot mutate without an explicit
  policy and the runtime's own candidate-bound authorization; durable state and
  audit remain authoritative; resume re-derives authority rather than reusing
  stale authorization.

**One authoritative runtime. Multiple coherent interfaces. Reliable automation.
Reproducible benchmarks. Evidence-backed outcomes.**

No claim above is made that was not backed by an executed test or a source
citation. Where a capability is deferred or unverified, it is stated as such.
