# IZEN PHASE 1 — AUTHORIZATION BOUNDARY AUDIT

**Status:** implemented. `go test ./...` green, `golangci-lint` 0 issues on all touched packages.

## A. Executive Summary

Phase 1 hardened the global execution boundary without new runtimes, new
authorization frameworks, new capability vocabularies, or package reorganization.

What changed and why:

1. **P0-2 resolved (dual `RuntimeExecutor` authority).** Production
   reachability proves `execution.RuntimeExecutor` (`internal/execution`) is
   the ONE canonical mutation authority: wired exactly once by the composition
   root (`internal/runtime/compose/compose.go:585`), consumed by the TUI
   (`m.executor.Execute`), the autonomy adapter, and the headless handlers.
   `runtime/executor.RuntimeExecutor.Execute` (guard→substrate coordinator)
   has **zero production callers** — Case C for its authority path (its
   `FileExecutor`/`ProposalValidator`/sanitizer helpers remain live
   *subordinate primitives*). `scopeguard.RuntimeExecutor` is a subordinate
   idempotency cursor (Case B) reachable only via `RuntimeEngine`, which
   itself has **zero production constructors**. Both are documented
   in-place as non-authorities and pinned by
   `TestPhase1_SingleProductionExecutionAuthority`.
2. **P0-1 resolved (`!` shell bypass).** `model.handleInput` → `execShell` →
   `sh -c` executed with only a mode bit + blacklist. The path now crosses
   `authorizeShellExecution` → `execShellGranted`/`streamShellCmd` →
   `recordShellEvidence`. `!command` is defined as an explicit human
   shell-execution intent authorizing ONLY that exact command (grant binds the
   command string; substitution fails closed). The raw `execShell` primitive
   was deleted; every production shell path is grant-gated
   (`TestPhase1_BangCrossesTheBoundary`).
3. **P1-1 resolved (`$test`/`$run` from read-only modes).** `runTestEngine`,
   `runBuildEngine`, `runTraceCmd`, `runLogCmd` (arbitrary `$log <text>`
   executed as shell!), and the composite `reviewTestExecutor` executed
   processes with **zero checks**. Each now mints a per-operation human grant
   (`authorizeTestExecution`/`authorizeShellExecution`), validates targets
   against shell-metachar injection, runs only through `RunGranted`
   (exact-command binding), and records evidence
   (`TestPhase1_TestExecCrossesTheBoundary`). The legacy ungranted runner
   seams (`runner.Run`/`runner.RunContext`) have zero production callers left.
4. **Monotonicity + mode matrix pinned.** Twelve architecture locks
   (AUTH-01…12) prove: no-grant ⇒ no mutation; model/planner/capability
   output never manufactures authorization; `$prompt` is bounded intent
   (still passes admission); `$hot` never auto-promotes; scope never
   accumulates; autonomy cannot escalate; OCC drift aborts clean.
5. **Evidence reused, not reinvented.** Shell/test outcomes publish
   `StageCompleted("shell-exec", …)` on the existing event bus (→
   `audit/events.ndjson` via the wired audit logger) and append to the
   existing activity tree. No new evidence system, no new file writes (Phase 0
   write-inventory locks untouched and green).

## B. Final Authorization Model

```text
ScopeNone
    = no execution grant (read-only).
    = gateway compiles every mutation strategy to read-only; the executor
      holds nothing approvable without a token, and Approve without a token
      fails (AUTH-01).

ScopeDynamic ($prompt)
    = human grants broad task-execution intent.
    = execution remains constrained by admission (risk scope vs admitted
      capabilities), mode policy, budget, OCC, verification, evidence.
    = under read-only admission the SAME intent is denied (AUTH-05).

ScopeDeclared ($hot)
    = human grants a bounded pre-approved execution envelope.
    = never auto-promoted: a follow-up bare intent falls back to ScopeNone;
      a new file/target/capability requires a new $hot (AUTH-06, AUTH-07).

Explicit shell authorization (!, approved SHELL_EXEC, proposed-shell Enter,
$log <text>, $env)
    = human grants ONE exact shell operation (ShellGrant binds the command
      string). Requires mode CapShell + firewall admission. Grants zero file
      mutation authority. `!go test ./...` never authorizes PatchManager.

Test/build execution authorization ($test, $run, $trace, /review composite)
    = human grants ONE bounded "go test"/"go build" invocation. The tool
      prefix is fixed by the caller (never user input); the target is
      validated against shell-metachar injection (`;|&$`'"\<>(){}[]*?#!
      =~`, newline, leading-dash flag smuggling). Grants zero file mutation
      authority and never leaves the test-execution class.
```

Grant minting rule (monotonicity): grants are minted ONLY from explicit human
directive provenances (`! typed`, `$test typed`, `$run typed`, `$trace typed`,
`$env typed`, `$log typed`, `proposed-shell`, `approved SHELL_EXEC step N`,
`/review composite`). Empty commands, empty provenances, and
directive-substrings (`$promptly`) mint nothing. `bindScopeProvenance`
REPLACES (never accumulates); each intent's provenance is minted fresh at the
parser/gateway layer.

## C. Final Execution Authority

| Type | File | Verdict |
|---|---|---|
| `execution.RuntimeExecutor` | `internal/execution/executor.go` | **Canonical authority.** Wired once in `internal/runtime/compose/compose.go:585`; consumed by `internal/ui/runtime_cutover.go` (`runRuntimeExecuteCmd`), `internal/ui/gateway.go` (`runGatedLine`), `internal/runtime/autonomy/adapter.go:360`. Owns provider invocation, context, admission, approval gate, apply, verification, lifecycle events. |
| `runtime/executor.RuntimeExecutor` | `internal/runtime/executor/executor.go` | **Case C (unreachable authority).** Zero non-test callers of `Execute`. Package stays for its subordinate primitives (`FileExecutor`, `ProposalValidator`, `SanitizeUntrustedPayload`, `MaterializeCandidateExported`, `ValidateProviderModel`) used by the orchestrator/CLI/verification harness — primitives, not authorities. Documented in-place. |
| `scopeguard.RuntimeExecutor` | `internal/runtime/scopeguard/gateway.go` | **Case B (subordinate component).** Idempotency cursor executing only caller-supplied, already-authorized `Effect` closures; owns no sink. Reachable only via `RuntimeEngine`, which has zero production constructors. Documented in-place. |

Production reachability evidence (all non-test sources swept):

- `NewRuntimeExecutor` ident calls in files importing
  `internal/runtime/executor` or `internal/runtime/scopeguard`: **none**.
- `NewRuntimeEngine` / `NewRuntimeExecutorWithWorkDir` ident calls outside
  `internal/runtime/engine.go` itself: **none**.
- `execution.NewRuntimeExecutor` production site: exactly one
  (`compose.go:585`; additionally locked by
  `TestRuntimeExecutorSingleCompositionBinding`).
- `go list -deps ./cmd/izen` still lists the rival packages (primitives are
  linked) — but no production *authority edge* reaches their coordinators.
  Package presence ≠ authority; the locks assert on construction/call sites,
  not package names.

## D. Global Execution Graph

TUI file mutation (all modes):

```text
human ($prompt/$hot)
→ parser/IntentGateway.Gate (ScopeProvenance + frozen context snapshot)
→ runGatedLine / runRuntimeExecuteCmd / autonomy cutover
→ execution.RuntimeExecutor.Execute
  (context-fidelity admission → strategy → risk-scope admission →
   contract identity → approval gate (human) → OCC commit gate → apply)
→ ExecutionEvidence + bus lifecycle events → audit/events.ndjson + projections
```

TUI shell execution (`!`, proposed-shell Enter, staged SHELL_EXEC, `$log <text>`, `$env`):

```text
human (typed directive / approval-box decision)
→ authorizeShellExecution (mode CanShell ∩ firewall ∩ exact command)
→ ShellGrant (binds command, class, mode, provenance, workspace root)
→ execShellGranted / streamShellCmd / RunGranted (grant equality verified)
→ ExecShell (workspace-root confined, 60s timeout)
→ recordShellEvidence → StageCompleted("shell-exec") on bus + activity tree
```

TUI test/build execution (`$test`, `$run`, `$trace`, `/review` composite):

```text
human (typed directive / /review invocation)
→ authorizeTestExecution (fixed tool prefix + target metachar validation + firewall)
→ ShellGrant (test-execution class)
→ RunGranted (exact-command binding) → confined execution
→ recordShellEvidence → StageCompleted("shell-exec") on bus + activity tree
```

Headless paths (unchanged architecture, documented semantics):

- `izen run "<prompt>"` — explicit CLI invocation IS the human
  authorization event; routes through the V3 app pipeline
  (`substrate.NewConcreteSubstrate`) with audit envelope lifecycle into
  `.izen/audit/events.ndjson`. Does NOT cross `execution.RuntimeExecutor`
  (pre-existing divergence, see §J).
- `izen orchestrate "<target>"` — explicit CLI invocation + interactive
  terminal approval gate (`y/i/n` per proposal) before `FileExecutor.Commit`;
  audit flush invalidates success on failure.
- `izen prompt` — prompt dispatch (no mutation path).
- `izen compact` — explicit local context-file rewrite (narrow, user-invoked).

## E. Mutation Sink Matrix

| Sink | Authorization | Guard | Executor | Scope | Evidence | Status |
|---|---|---|---|---|---|---|
| `PatchManager.Apply` / `MutationSet` (via `execution.RuntimeExecutor.Approve`) | human approval gate + `MutationAuthorization` token + `ScopeProvenance` | admission (fidelity + risk scope), ScopeGuard, OCC commit gate, verifier | `execution.RuntimeExecutor` (canonical) | resolved targets, workspace root, budget | `ExecutionEvidence` + bus lifecycle + `mutations.log` | hardened (pre-existing; pinned AUTH-01/12) |
| `Runner.Run` (legacy `sh -c`, `internal/execution/runner.go`) | `MutationAuthorization` token (`checkAuthorization`) | capability/budget checks | legacy engine (behind canonical executor) | targets | execution proof | unchanged; token-gated |
| `ExecShell` via `!` | per-operation `ShellGrant` (`! typed`) | mode `CanShell` + firewall + exact-command binding | granted seam | workspace root, 60s timeout | `StageCompleted("shell-exec")` + activity tree | **fixed in Phase 1** |
| `ExecShell` via staged `SHELL_EXEC` | interactive approval (`y/a/n`) + per-step `ShellGrant` + `authorizeBuildExecution` | staged-scope gate + firewall + sudo/OS-fence | granted seam | workspace root, op context (cancellable) | same + build result msgs | **fixed in Phase 1** |
| `ExecShell` via proposed-shell Enter | human Enter + `ShellGrant` (`proposed-shell`, sync + async gates) | injection-time sanitize + firewall + `CanShell` | `streamShellCmd` gate | workspace root | same | **fixed in Phase 1** |
| `go test`/`go build` via `$test`/`$run`/`$trace`/composite | per-operation test grant (typed directive / `/review` invocation) | target metachar validation + firewall + exact-command binding | `RunGranted` | workspace root, 60s timeout | same + result msgs | **fixed in Phase 1** |
| `$log <text>` as shell | `ShellGrant` (`$log typed`, requires `CanShell`) | firewall + binding | `RunGranted` | workspace root | same | **fixed in Phase 1** (now fail-closed in /review) |
| `$env` diagnostics | inspect-class `ShellGrant`s | `CanShell` + firewall | `execShellGranted` | workspace root | same | **fixed in Phase 1** |
| `review.Sandbox` (`go test` in `/tmp/izen/review`) | review-engine invocation | 60s timeout, isolated dir | sandbox | sandbox dir | `TestResult` + output pipeline | unchanged (review-internal verification) |
| git/checkpoint I/O (`git rev-parse/status`, checkpoint manager) | enclosing operation's grant | read-only plumbing | respective engines | repo root | audit/events | unchanged (no new authority) |

## F. `$prompt` / `$hot` Matrix

| Mode | no grant | `$prompt` | `$hot` |
|---|---|---|---|
| `/ask` | read-only chat; mutation goals compile to reasoning; no execution | task-execution intent; autonomy-routed; still admitted/verified | transitions toward `/build` envelope; bounded by declared scope |
| `/plan` | read-only planning artifact | bounded planning execution intent | n/a (plan-scoped) |
| `/build` | `ScopeAuthorizationError`; staged plans fail closed (`TestControl_ScopeNoneBuildRejection`) | `ScopeDynamic` targeted mutation via canonical executor | `ScopeDeclared` bounded mutation via canonical executor |
| `/investigate` | read-only diagnostics (+ granted test/shell classes only) | bounded diagnostic execution intent | n/a |
| `/review` | read-only audit (`$fix` blocked); `$test`/`$run` execute ONLY as bounded test grants | bounded validation intent | n/a |

`$test`/`$run`/`!` never mint `ScopeProvenance` and never satisfy the
`AllowsMutation` gate: shell/test grants and mutation authority are disjoint
classes.

## G. Shell Boundary

Final semantics: **`!command` = explicit human shell-execution intent.**

- It IS the human authorization event for that specific shell operation
  (preferred direction adopted and encoded).
- It is capability-specific (`CanShell` mode filter) and operation-specific
  (grant binds the exact string; `!go test ./...` authorizes no patch apply,
  no commit, no second command).
- It MUST NOT grant unrelated mutation authority: the grant type carries no
  `ScopeProvenance`, satisfies no `AllowsMutation` check, and is unreadable
  to the file-mutation pipeline.
- Proof: `TestPhase1_BangCrossesTheBoundary` (structural: no raw primitive on
  the input path; raw port locked to boundary + streaming gate),
  `TestShellAuth_BangDeniedWithoutCapability`,
  `TestShellAuth_BangGrantBindsExactCommand`,
  `TestShellAuth_FirewallIsNotAuthority` (behavioral).
- `CanShell != HumanAuthorizedShellExecution != SpecificShellOperationAllowed`:
  the mode bit filters, the grant authorizes, the exact-command binding
  scopes. The blacklist remains as the last-line defense, never the authority.

## H. Test/Build Execution Boundary

- `$test <target>` / `$run <target>` = explicit human test-execution intents;
  each mints a one-operation grant after target validation + firewall; the
  fixed tool prefix (`go test -v`, `go build`, `go test -run=… -v -race`)
  never comes from user input.
- `$trace <name>` validates the `-run` value identically.
- Read-only modes stay read-only for *mutation* (`$fix` still blocked in
  `/review` and `/investigate`); test execution is its own bounded class with
  its own boundary — the smallest safe contract consistent with the existing
  architecture (directive contract table unchanged: `test`/`run` already
  declare `WaitsForUser: true`).
- **Explicit remaining limitation (no fake guarantee):** test execution is
  NOT sandboxed. It runs in the workspace root with user privileges under a
  60s timeout. A malicious or buggy test binary CAN mutate the workspace;
  confinement is directory + timeout + evidence, not isolation. Full
  sandboxing (containers/seccomp/snapshot-restore around tests) is out of
  Phase 1 scope. What Phase 1 guarantees instead: no *silent* execution
  (every run is grant-bound and evidenced), and no *injected* execution
  (metachar/flag validation + firewall + exact-command binding).

## I. Authority Invariants

| ID | Invariant | Test |
|---|---|---|
| AUTH-01 | No grant ⇒ no mutation | `TestPhase1_NoGrantNoMutation` |
| AUTH-02 | Model output cannot authorize | `TestPhase1_ModelOutputCannotAuthorize` |
| AUTH-03 | Planner output cannot authorize | `TestPhase1_PlannerOutputCannotAuthorize` |
| AUTH-04 | Capability availability cannot authorize | `TestPhase1_CapabilityCannotAuthorize` |
| AUTH-05 | `$prompt` = bounded intent, still admitted | `TestPhase1_PromptGrantsBoundedIntent` |
| AUTH-06 | `$hot` cannot exceed declared envelope | `TestPhase1_HotStaysDeclared` |
| AUTH-07 | Scope expansion needs a new human grant | `TestPhase1_ScopeExpansionRequiresNewGrant` (+ `TestShellAuth_ScopeReplacement` UI half) |
| AUTH-08 | One production execution authority | `TestPhase1_SingleProductionExecutionAuthority` (+ pre-existing `TestRuntimeExecutorSingleCompositionBinding`) |
| AUTH-09 | `!` cannot bypass authorization | `TestPhase1_BangCrossesTheBoundary` + `TestShellAuth_Bang*` + `TestShellAuth_FirewallIsNotAuthority` |
| AUTH-10 | Read-only modes cannot silently run test/build code | `TestPhase1_TestExecCrossesTheBoundary` + `TestShellAuth_TestTargetInjectionRejected` + `TestShellAuth_TraceTargetValidated` |
| AUTH-11 | Autonomous continuation cannot escalate | `TestPhase1_AutonomyCannotEscalate` |
| AUTH-12 | OCC drift ⇒ abort, never partial mutation | `TestPhase1_OccDriftAbortsClean` (reuses the Phase 3 abort assertions) |

Files: `internal/architecture/phase1_authorization_boundary_test.go`,
`internal/ui/shell_auth_test.go`. All assert reachable behavior/authority,
never bare package names.

## J. Remaining Risks

1. **No test sandbox (§H).** `go test`/`go build` run unisolated in the
   workspace. Documented limitation; needs a later-phase sandbox or
   snapshot-restore wrapper.
2. **Headless divergence.** `izen run` (V3 app pipeline + concrete
   substrate) and `izen orchestrate` (orchestrator + `FileExecutor` +
   terminal approval) do not cross `execution.RuntimeExecutor`. Both have
   explicit human authorization (CLI invocation; orchestrate additionally has
   per-proposal interactive approval + audit-gated success), so there is no
   *unauthorized* second authority — but there are two *implementation*
   authorities. Converging headless onto the canonical executor is later-phase
   work (explicitly not attempted: would be a new runtime).
3. **`$hot` envelope binding is provenance-scoped, not target-list locked.**
   The declared target boundary is enforced by admission/OCC at execution
   time; a `$hot` that discovers genuinely new targets fails closed
   (`SCOPE_VIOLATION`/clarification), but the "declared list" itself is the
   resolved target set, not a user-typed manifest. A stricter declared-manifest
   check belongs to a later phase.
4. **Shell `CanShell` is coarse.** Once a mode has `CapShell`, any
   firewall-passing command the human types is grantable. Command-class
   policies (e.g. network vs filesystem) do not exist; the firewall is a
   blacklist, not a policy engine.
5. **Evidence for shell/test is telemetry-grade.** `StageCompleted` records
   who/what/command/result on the bus and activity tree, but shell runs do
   not produce `ExecutionEvidence` contracts (that taxonomy stays
   file-mutation-scoped per §19). Shell forensics therefore cannot use the
   contract/attempt lineage.
6. **Outcome taxonomy untouched.** Authorization/scope/capability/OCC/human-cancel
   failures surface as errors/denials and never as successful mutations, but
   no unified completion taxonomy was built (per §19).

## Definition-of-Done checklist

- [x] One proven production execution authority (C.3 + AUTH-08).
- [x] `!` no longer bypasses the boundary (G + AUTH-09).
- [x] `$test`/`$run` cannot silently violate read-only/execution semantics (H + AUTH-10).
- [x] No-grant operation cannot produce side effects (AUTH-01).
- [x] `$prompt` = broad intent, not unrestricted authority (AUTH-05).
- [x] `$hot` stays inside its declared envelope (AUTH-06).
- [x] Scope expansion needs a new human grant (AUTH-07).
- [x] Model/planner/capability output cannot escalate (AUTH-02/03/04).
- [x] Autonomous continuation cannot escalate (AUTH-11).
- [x] OCC/scope protections intact (AUTH-12; Phase 0–6 locks green).
- [x] Existing evidence is the terminal record (bus + activity tree; §E).
- [x] TUI + headless authorization semantics documented (D).
- [x] Architecture tests pin the invariants (I).
- [x] No new runtime/executor/authorization architecture (one small boundary
      file + grants; no new framework, vocabulary, or package moves).
- [x] No broad refactor (13 files touched, all on execution paths).
- [x] This document reflects the implemented architecture.
- [x] `go test ./...` fully green; `golangci-lint` 0 issues on touched
      packages; no pre-existing failures encountered.
