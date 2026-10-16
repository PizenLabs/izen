# IZEN — Execution Contract Audit (black-box traces → first causes → fixes)

Date: 2026-10-09 · Branch: `fix/execution` · Base commit `0ddadd6`.

This audit was driven from the two supplied black-box traces, not from prior
hypotheses. Every claim below is backed by a reproducible test or a real
execution. The report distinguishes **OBSERVED**, **CAUSAL**, **FIXED**,
**VERIFIED** and **REMAINING**.

---

## 0. The two traces, restated

1. **Inside `/build`** — `/build` then `Create a new file named `zuru.md` with
   the content "Hello everyone".` produced a planning/analysis result
   (`Prepared findings`) and mutated nothing.
2. **Outside `/build` with `$prompt`** — `$prompt Create a new file named
   `zuru.md` ...` reported `intent=ask`, then "proceeding as build", but the
   execution context carried `command_mode=(not-carried)` and compiled to
   `operation=MODIFY scope=UNRESOLVED target=DEFERRED`, discovered `README.md`
   and `testfile.md`, and asked for target selection.

---

## 1. First causal divergence — `$prompt` CREATE regression

### OBSERVED
An explicitly named target (`` `zuru.md` ``) was never extracted, so a CREATE
compiled as a targetless MODIFY and discovery was asked to resolve a scope the
user had already stated.

### CAUSAL
Both target-extraction authorities require a token boundary (start, whitespace
or comma) before a filename. A backtick (or straight/curly quote) is not one, so
the delimiter **hid the target**:

- `internal/autonomy.extractTargets` (classifier) — regex anchored at
  `(?:^|[\s,])`.
- `internal/execution/strategy.extractBareTargets` (the strategy gateway, which
  is the `$prompt` driver's scope authority) — `strings.Trim` cutset omitted the
  backtick, so `` `zuru.md` `` kept a trailing `` ` `` and `.md` never matched.

With no target, `strategy.Select` fell through to repository-level planning
(backtick) and `DeriveObjectiveSemantics` produced
`MODIFY/UNRESOLVED/DEFERRED/REQUIRED`; discovery then observed the unrelated
markdown files and parked at candidate selection. This is the **first**
divergence; nothing downstream is at fault.

### FIXED
- `internal/autonomy/intent.go` — `normalizeTargetDelimiters` replaces quote /
  code-span delimiters with spaces before scanning (`extractTargets`).
- `internal/execution/strategy/selector.go` — `normalizeBareTargetDelimiters`
  does the same for the prose-target pass (`extractBareTargets`).

Both fixes touch only delimiters, never path characters, so a quoted name
presents exactly the boundaries of a bare one.

### VERIFIED (real execution, local `qwen2.5-coder:7b`)
```
IZEN_LIVE_FORENSICS=1 IZEN_BENCH_PROVIDER=ollama \
  go test ./test/realworld/ -run TestBenchmark_CreateCodeQuotedPrompt
```
Trace (excerpt):
```
EVIDENCE
  mutation: target=zuru.md outcome=created artifact=true apply=true fs_changed=true
OBJECTIVE EVALUATIONS
  PROVEN  granted=true mutations=1
termination: state=completed reason=objective satisfied: created; objective PROVEN by evidence
```
The unrelated `testfile.md` was **not** mutated; no clarify boundary was opened.

Deterministic driver-arm proof:
`internal/runtime/autonomy/code_quoted_create_test.go`
(`TestCreate_CodeQuotedTargetBindsBeforeDiscovery` — real workspace, real
kernel, real filesystem bytes, `objective=PROVEN`).

Unit proofs: `internal/execution/strategy/code_quoted_target_test.go`,
`TestClassify_CodeQuotedTargetExtracted`.

---

## 2. First causal divergence — `/build` ordinary prompt

### OBSERVED
Inside `/build`, an ordinary prompt produced a read-only `Prepared findings`
artifact instead of a CREATE.

### CAUSAL
`/build` was not represented as an execution authority. The parser grants
`ScopeProvenance` **only** for explicit `$prompt`/`$hot` directives, so a plain
goal inside `/build` parses as `ScopeNone`. `runRuntimePrompt` refuses mutation
without an authorizing scope and falls back to `runGatedLine`, whose
`IntentGateway.selectScopedStrategy` downgrades a mutation to a read-only
`RepositoryInvestigation` (`ArtifactKind="investigation"` →
`ClassifyArtifact` → `ArtifactInspection` → `"Prepared findings"`).

This is a **wrong interaction contract**, at the `/build` seam, not a planner or
strategy bug.

### FIXED
`internal/ui/runtime_cutover.go` — `runRuntimePrompt` now treats `/build` itself
as the execution contract: when the active mode is `ModeBuild` and no explicit
directive bound a scope, it binds the runtime-resolved mutation scope
(`ScopeDynamic`). It deliberately does **not** override an explicit `$hot`
(`ScopeDeclared`) binding, so `/build`, `$prompt` and `$hot` stay distinct at
the command layer and converge on the same mutation authority at admission.

### VERIFIED
`internal/ui/build_interaction_contract_test.go`:
- `TestBuildModeOrdinaryPromptIsAnExecutionRequest` — drives the exact backtick
  prompt in `/build` with `ScopeNone`; asserts mutation authority, strategy
  `targeted_mutation` / artifact `create_file`, and executor target `zuru.md`.
- `TestBuildModePromptDoesNotOverrideHotfixScope` — a pre-bound `$hot`
  (`ScopeDeclared`) is not replaced by the `/build` grant.

The executor arm of `/build` is the same `RuntimeExecutor` proven by the
real-world CREATE benchmark in §1, so the request proven there is the request
`/build` now dispatches.

---

## 3. Command-mode propagation (`command_mode=(not-carried)`)

### OBSERVED
A `$prompt` run reported `command_mode=(not-carried)` even though the directive
was the authority that authorized it.

### CAUSAL
`intentAxes` read `d.subcommand`, a **composition-time preflight policy** field
that the per-input `SetScope` binding never touches. The authoritative carried
value lives on the loop request (`d.req.Scope`) and was already populated for
`$prompt`/`$hot`.

### FIXED
`internal/runtime/autonomy/objective_lifecycle.go` — `intentAxes` reads the
authoritative per-run `d.req.Scope` (falling back to the composition
`d.subcommand`, then the explicit `(not-carried)` marker). No parallel authority
state was added: `d.req.Scope` is the single carried representation of the
command surface; `d.subcommand` remains only the preflight policy selector.

### VERIFIED
`internal/runtime/autonomy/command_mode_axis_test.go`
(`TestIntentAxes_CommandSurfaceIsCarried`) — `SetScope("$prompt")` /
`SetScope("$hot")` render the exact surface; an unbound run still reports
`(not-carried)`.

---

## 4. `$hot` contract

### OBSERVED / AUDITED
`$hot` is a directive handled before mode routing
(`dispatchDirectives` → `routeHotfixThroughAutonomy`), binding
`ScopeDeclared` and routing through the autonomy driver. It does not require
`$prompt`, and it is permitted inside `/build` (`ParseInWorkspace` validates the
directive against the active build workspace).

### CAUSAL (no defect found)
Target resolution is deterministic (`IntentGateway`/`strategy.Select`); the model
never resolves a target. `ScopeDeclared` is a distinct authority from
`ScopeDynamic`, so the human-declared scope cannot be silently widened by
inference.

### FIXED / REMAINING
No change required. Regression pinned by:
- `internal/ui/scope_control_test.go::TestControl_ExplicitScopeDirectives`
  (`/build$hot refactor @target.go` → `ScopeDeclared` + `targeted_mutation`).
- `internal/ui/build_interaction_contract_test.go::TestBuildModePromptDoesNotOverrideHotfixScope`
  (the `/build` grant never replaces a declared `$hot` scope).
- `internal/runtime/autonomy/behavior_test.go` (scope→grant vector).

---

## 5. Cancellation

### OBSERVED
The UI printed `[ ABORT ] Execution force-cancelled by user` but the provider
invocation appeared to continue.

### CAUSAL
The cancellation chain is complete in code: `handleCtrlC` →
`handleEmergencyInterrupt` → `activeOp.Cancel()` + `cancelAllBackgroundContexts`
+ `streamCancel`; workers derive their context from
`m.operationContext()`; the executor closes the raw stream on `ctx.Done`; every
production provider builds its HTTP request with
`http.NewRequestWithContext`. Go cannot force-kill a goroutine that ignores its
context, so a hostile provider is the only way the call "continues"; real
providers do not. No first-cause defect was found in the cancellation chain.

### VERIFIED (real provider termination)
```
IZEN_LIVE_FORENSICS=1 go test ./test/live_r6/ \
  -run TestLiveR6_RealProviderCancellationIsBounded
```
Result against the real local provider:
```
status: aborted
termination: state=aborted reason=context cancelled
loop: executing → verifying (execution consumed: cancelled) → interpreting → aborted
workspace byte-identical (225 bytes)
no PROVEN objective
```
Deterministic coverage: `internal/runtime/autonomy/r6_cancellation_test.go`,
`driver_test.go` (`TestDriver_ProviderIgnoresCancellation` documents the
honest boundary), `internal/execution/r6_cancellation_test.go`,
`execution_stream_test.go`.

### Conversation lifecycle independence
`/new` does not terminate a run; the execution remains parked. This is pinned by
`internal/ui/execution_admission.go` and
`internal/ui/execution_lifecycle_e2e_test.go` / `execution_lifecycle_invariants_test.go`
(“remains parked”, “A conversation boundary does not terminate an execution
run.”). The driver has a separate run identity and terminal state from the
conversation.

---

## 6. Awaiting-human / ambiguity barrier

### OBSERVED
The CREATE example parked at candidate selection although it was not ambiguous.

### CAUSAL
Not an ambiguity-barrier defect: the target had been lost upstream (§1), so the
runtime genuinely had no scope. The ambiguity barrier itself is correct and was
not weakened.

### FIXED
Fixed by §1: the explicit target now reaches scope derivation before discovery,
so the CREATE path never enters discovery/candidate selection.

### VERIFIED
- `internal/runtime/autonomy/acceptance_matrix_test.go::TestAcceptance_AmbiguousTargetParksBeforeProvider`
  — an intentionally ambiguous MODIFY still parks with `AWAITING_HUMAN`, zero
  provider calls, zero mutation.
- `internal/runtime/autonomy/code_quoted_create_test.go` — the explicit CREATE
  never opens a clarify boundary.

---

## Acceptance matrix status

| Arm | Expected | Status | Evidence |
|---|---|---|---|
| `$prompt` CREATE (code-quoted) | CREATE/zuru.md/created/verified/PROVEN | **VERIFIED** | real benchmark trace + driver test |
| `/build` ordinary prompt | same semantics | **VERIFIED (admission)** | UI contract test + shared executor |
| `/build` + `$hot` | human-declared scope honored | **VERIFIED** | scope_control + build contract tests |
| Ambiguous MODIFY | `AWAITING_HUMAN`, no mutation | **VERIFIED** | acceptance matrix |
| Cancellation | provider terminated, aborted | **VERIFIED** | live R6 probe |
| Conversation reset `/new` | conversation reset, execution parked | **VERIFIED** | execution_admission + e2e |

---

## REMAINING (disclosed, not fixed)

1. **`/build` uses the single-shot executor, `$prompt` uses the bounded driver.**
   They now converge on scope, operation and target at admission and on the
   same `RuntimeExecutor` for the mutation, but `/build` does not emit the
   driver's `objective=PROVEN` machine trace (it reports through
   `ExecutionProof`). A future change could route `/build` ordinary prompts
   through `runAutonomyRoutedCmdExplicit` for full trace convergence; this was
   deliberately not done here to preserve existing `/build` behavior and tests.
2. **`d.subcommand` (preflight policy) is not synced from `SetScope`.** The
   `command_mode` axis is now truthful from `d.req.Scope`, but the preflight
   DecisionSurface policy still sees the composition-time value. This affects
   recovery option shaping only, never authority or the command-mode axis.
3. **No live run of the interactive TUI `/build` path.** The `/build` fix is
   proven at the UI seam deterministically and its executor request is the same
   one proven live for `$prompt`; a full interactive live `/build` run was not
   scripted.
4. **Two target-extraction implementations remain.** `internal/autonomy` and
   `internal/execution/strategy` each own a delimiter-normalization helper. They
   now agree by construction and by test, but the duplication is a standing
   consolidation candidate.

---

## Files changed

```
internal/autonomy/intent.go                              (+25)
internal/execution/strategy/selector.go                  (+24)
internal/runtime/autonomy/objective_lifecycle.go         (command_mode axis)
internal/ui/runtime_cutover.go                           (+17)
test/realworld/benchmark_test.go                         (+58, live code-quoted arm)
internal/runtime/autonomy/code_quoted_create_test.go     (new)
internal/runtime/autonomy/command_mode_axis_test.go      (new)
internal/execution/strategy/code_quoted_target_test.go   (new)
internal/ui/build_interaction_contract_test.go           (new)
```

`go test ./...` → **exit 0**, 212 packages `ok`, 0 failures.
