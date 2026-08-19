# Phase 6 — Production Autonomous Driver Report

Phase 6 makes the Phase 4/5 autonomous runtime **user-controllable in the TUI**.
It adds the human-decision presentation contract (`HumanBoundary` with
Action/Resumable/Targets), hardens the bounded `Driver` (single-lane guard,
`Abort`, boundary enrichment), and bridges the driver to the UI through a
structural interface so a production run can park at an approval/clarify gate,
resume under the same governance every other mutation uses, or abort. It
removes the last fabricated preview surface (`AutonomousLoop`, `PublishTransitions`,
`NewAutonomyLoopPreview`) — the `Driver` is now the sole loop-transition
producer. It is **not** a new execution authority: `autonomy.Decide` remains the
classifier/workspace/verdict authority, the driver is a consumer of the shared
`RuntimeExecutor`, and no model re-run ever happens to approve.

## 1. HumanBoundary presentation contract

`internal/autonomy/runtime_loop.go` now carries the facts the TUI needs to
render and decide on a parked run:

| Field | Meaning |
|---|---|
| `PatchID` | held executor patch (approval gate) |
| `Options` | clarify candidates (target ambiguity) |
| `Reason` | human-readable park reason |
| `Action` | `approve` / `clarify` / `inform` — the only decision surface kind |
| `Resumable` | whether a resume decision exists (approval/clarify yes, inform no) |
| `Targets` | authoritative resolved target set (filled from `d.req.Targets`) |

`deriveBoundaryAction` is the single derivation rule (PatchID → approve/resumable;
Options → clarify/resumable; else inform/non-resumable). It is applied in
`applyDecision` (LoopAskUser) and `AwaitHuman`, and defensively re-applied by the
driver's `enrichBoundary` at every park so a boundary is never rendered without
an Action.

## 2. Driver hardening (`internal/runtime/autonomy/driver.go`)

- **Single-lane guard**: `Run` rejects a duplicate start while a run is active
  OR parked (`loop != nil && !State().IsTerminal()`). Re-entry after a terminal
  state is legal (satisfies `TestDriver_TerminalLoopRejectsReentry`).
- **`Abort(reason)`**: terminates a parked/active run as `RuntimeAborted /
  FailurePermanent`; no-op if already terminal; requires a started run. It is
  the only UI path to cancel a parked run (in-flight cancellation flows through
  the operation context).
- **`enrichBoundary()`**: fills Action/Resumable defensively and copies
  `d.req.Targets` into the boundary at the Run ambiguity park and the
  `RuntimeAwaitingHuman` observeAndRun case. `d.req` is set before the ambiguity
  check so targets are always authoritative.

## 3. UI driver bridge (`internal/ui/autonomous.go`)

The UI **never imports** `internal/runtime/autonomy` (architecture invariant: UI
is a projection, the driver owns the loop). It drives the driver through the
structural interface `autonomousDriver` (`Run/ResumeApprove/ResumeReject/
ResumeClarify/Abort/State/Boundary/Termination`), which `app.Autonomous` satisfies
(compile-time verified, wired at `compose.go:608` → `program.go`).

- **Initiation**: `runAutonomousDriver` guards against a second start while
  active **or parked** (a parked boundary can never be clobbered by a new run).
  A run starts under a foreground operation (`OpAutonomous`).
- **Parking**: `handleAutonomousRun` distinguishes parked (`term == nil` + a
  Boundary → renders the boundary card, `enterApprovalState`) from terminal
  (releases the operation, resolves the approval state, logs the outcome).
- **Resume decisions** (never auto-approved, never re-classified):
  - `resumeAutonomousApprove` issues a `MutationAuthorization` over the
    boundary's `Targets` through the production `AuthorizationEngine` and
    attaches it to the executor the driver shares **before** `ResumeApprove`,
    so the held patch applies under the same governance owner as every other
    mutation.
  - `resumeAutonomousReject` runs the executor-backed rejection path (terminal,
    no file touched).
  - `resumeAutonomousClarify` resumes with the explicitly selected candidate
    (↑/↓ + Enter); selection is an explicit human act.
  - `abortAutonomousRun` is the only cancel path for a parked run.
- **Keys** (`keys.go`): Alt+A/Enter approve · Alt+R/Esc reject · ↑/↓+Enter
  clarify-select · Ctrl+C aborts. `handleCtrlC` (operation.go) and
  `handleEmergencyInterrupt` (model.go) both abort a parked run; the interrupt
  path reuses `clearAutonomousRun`.
- **Rendering** (`view.go`): a parked boundary renders the interactive card in
  `StateAwaitingApproval` via `renderAutonomousBoundaryBlock`.
- **Fallback**: when the driver is not wired (harness), the ModeBuild branch
  falls back to the single-shot `executeAutonomyViaRuntime` — the pre-Phase-6
  path is preserved.

## 4. Dead-code removal (the driver is the sole loop producer)

| Removed | Where | Why |
|---|---|---|
| `AutonomousLoop` / `NewAutonomousLoop` | `internal/autonomy/loop.go`, `loop_test.go` | zero production callers; preview only |
| `Engine.PublishTransitions` | `internal/autonomy/engine.go:349` + engine_test.go | loop transitions are now published only by the Driver's `publish` |
| `NewAutonomyLoopPreview` | `autonomy_cmd.go`, `autonomy_route.go`, `autonomy_cmd_test.go` | fabricated preview loop; the driver is the real loop |

## 5. Test coverage

- **Driver** (`internal/runtime/autonomy/driver_phase6_test.go`):
  `TestDriver_AbortParkedApproval` (abort leaves the file untouched, no
  re-execution, fresh Run legal after), `TestDriver_AbortParkedClarify` (zero
  model calls before/after), `TestDriver_DuplicateStartBlocked` (second Run
  rejected, parked boundary preserved and still resumable),
  `TestDriver_ApprovalBoundaryEnrichment` (Targets/Action/Resumable),
  `TestDriver_ClarifyBoundaryEnrichment` (Options/Action, no targets),
  `TestDriver_InformBoundaryNotResumable` (Action=inform, Resumable=false, no
  PatchID).
- **UI** (`internal/ui/autonomous_test.go`): park at approval renders the card
  + enters `StateAwaitingApproval`; clarify park renders candidates and Enter
  resumes with the selected target; approve issues authorization over the
  boundary targets then resumes (terminal completed releases the run); abort
  yields `RuntimeAborted` and fully releases the boundary; key bindings (Esc →
  reject, Alt+A → approve); inform boundary is non-resumable (keys inert);
  duplicate-start guard refuses a second run; `executeAutonomyViaDriver`
  falls back to the runtime executor when the driver is unwired.

## 6. Validation

`go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues),
`go test ./... -count=1`, and `go test -race ./internal/ui/... ./internal/autonomy/...
./internal/runtime/autonomy/...` all pass.

## 7. Hard constraints preserved

Exactly one production Autonomous Driver; exactly one approval authority
(RuntimeExecutor gate); exactly one target-resolution authority (IntentGateway);
runtime never calls Provider/PatchManager/fs directly; no auto-approval; no model
re-run to approve; resume never re-classifies/re-resolves/re-generates/resets
budgets; no new `/agent`-style commands (`$prompt`/`$hot` remain the intended
entry); terminal states are Completed/Aborted (AwaitingHuman non-terminal only
when resumable); cancellation is not rejection (Ctrl+C aborts; Alt+R rejects).