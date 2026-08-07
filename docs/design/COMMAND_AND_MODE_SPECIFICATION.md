# IZEN — Command & Mode Operational Specification

> **Static architecture audit of Izen's CLI & TUI command surface.**
> Primary sources: `cmd/izen/`, `internal/ui/`, `pkg/app/`, `pkg/planner/`, `pkg/capability/`,
> `internal/modes/`, `internal/core/workflow/`, `internal/presentation/`.
> Every command, mode transition, and hotkey in this document is traced to its exact
> implementation site. Line references point to the current source.

---

## 0. System Topology at a Glance

Izen is a human-centered coding agent with two surfaces:

| Surface | Entry point | Runtime |
|---|---|---|
| **Interactive TUI** (primary) | `cmd/izen/main.go` → `ui.RunMainDashboardWithApp` / `ui.RunRollbackEngine` | Bubble Tea event loop, modal UI states, 5 workflow modes |
| **Headless CLI / V3 Agent Runtime** | `cmd/izen/main.go` → `runRuntimeCommand` (`izen run`) | `pkg/app.Pipeline` (IntentCompiler → StrategyRegistry → Planner → TxFS) |

The TUI and the headless pipeline are architecturally separated: the TUI drives the
`internal/` engines (ask/investigate/plan/build/review), while `izen run` drives the
`pkg/` V3 pipeline. The two share the `internal/command`, `internal/modes` and provider
layers only.

```
┌─────────────────────────── TUI (internal/ui) ───────────────────────────┐
│  handleInput → parseModeShorthand → / $ ! @ routing → setMode → engine  │
│  handleKey / Update → hotkeys, approval gates, vi-mode, pickers          │
│  WorkflowStateMachine (idle→investigating→planning→building→reviewing)   │
└──────────────────────────────────────────────────────────────────────────┘
┌───────────────────────── V3 Pipeline (pkg/app) ─────────────────────────┐
│  Request → IntentCompiler → ir.IntentIR → ClarificationGate →            │
│  Capability Registry → StrategyRegistry → ContextPolicy → PromptBuilder  │
│  → Extractors → Semantic Alignment Gate → Capability Validation Gate     │
│  → Planner (greenfield/brownfield) → ExecutionGraph → Kernel Engine      │
│  → TxFS.Begin … TxFS.Commit/Rollback                                      │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## SECTION 1 — EXECUTION MODES & TRANSITION STATE MACHINE

### 1.1 The Five Workflow Modes (`internal/modes/modes.go`)

The TUI exposes **five** logical modes. They are an ordered enum
(`ModeAsk iota … ModeReview`) whose `String()` names are the canonical slash-commands.
Every mode carries a fixed **capability matrix** (the immutable permission boundary):

| Mode | `String()` | Capabilities (`capabilityMatrix`) | ReadOnly | Intent scope |
|---|---|---|---|---|
| `ModeAsk` | `ask` | `CapRead` | ✅ | Explain, inspect, understand. Direct read-only chat boundary. |
| `ModePlan` | `plan` | `CapRead` | ✅ | Architecture, migrations, refactors. No execution. |
| `ModeBuild` | `build` | `CapRead│CapWrite│CapShell│CapTest│CapPatch│CapCheckpoint` | ❌ | Implement, refactor, write tests. Controlled execution. |
| `ModeInvestigate` | `investigate` | `CapRead│CapShell│CapTest` | ❌¹ | Debug bugs, failures, regressions. Bounded forensic loops. |
| `ModeReview` | `review` | `CapRead` | ✅ | Audit changes, detect risks, inspect regressions. 100% read-only. |

¹ `investigate` is *write-locked* but *not* `ReadOnly()` because it legitimately runs
shell/test binaries (`CapShell│CapTest`) — see `runInvestigateAsyncCmd` which hard-fails on
`CanWrite()` and gates on `CanShell()`.

**Capability helpers** (`modes.go:89-98`): `CanRead/CanWrite/CanShell/CanTest/CanPatch/CanCheckpoint`
derive from the matrix. The shell guard in `handleInput` blocks `!cmd` when
`!currentMode.CanShell()`, and `/review`'s engines assert `CanWrite/CanShell/CanPatch` are all false.

### 1.2 Canonical Workflow State Machine (`internal/core/workflow/`)

The **shared WorkflowStateMachine** (`machine.go`) is the logical spine. States
(`state.go:7-16`) and their transitions are a strict lookup table:

**States:** `idle`, `investigating`, `planning`, `building`, `reviewing`, `repairing`, `verified`, `failed`.

**Events** (`state.go:51-59`): `EventInvestigate`, `EventPlan`, `EventBuild`, `EventReview`,
`EventFailureIdentified`, `EventVerificationPassed`, `EventReset`.

**Transition table** (`machine.go:101-172`):

| From | Event | Guard | To |
|---|---|---|---|
| `idle` | `EventInvestigate` | — | `investigating` |
| `idle` | `EventPlan` | — | `planning` |
| `idle` | `EventReset` | — | `idle` |
| `investigating` | `EventPlan` | — | `planning` |
| `investigating` | `EventReset` | — | `idle` |
| `planning` | `EventBuild` | `HasPlan` **and** `HasCapabilities` | `building` |
| `planning` | `EventReset` | — | `idle` |
| `building` | `EventReview` | — | `reviewing` |
| `building` | `EventFailureIdentified` | class-aware | `repairing`/`investigating`/`planning`/`failed` |
| `building` | `EventReset` | — | `idle` |
| `reviewing` | `EventVerificationPassed` | — | `verified` |
| `reviewing` | `EventFailureIdentified` | class-aware | `repairing`/`investigating`/`planning`/`failed` |
| `reviewing` | `EventReset` | — | `idle` |
| `repairing` | `EventBuild` | `HasCapabilities` | `building` |
| `repairing` | `EventFailureIdentified` | class-aware | loop or down-class |
| `repairing` | `EventReset` | — | `idle` |
| `verified` / `failed` | `EventReset` | — | `idle` |

Failure-class routing (`machine.go:174-191`, via `classifier.FailureClass`):
`FailureCodeClass → repairing`, `FailureEnvironmentClass → investigating`,
`FailureTestClass → planning`, `FailureScopeClass → planning`, `FailureUnknownClass → failed`.
A `FailureScopeClass` with an attached checkpoint coordinator triggers an automatic
checkpoint rollback (`machine.go:92-96`); entering `building`/`repairing` from another state
triggers `coordinator.CreateBeforeBuild()` (auto git checkpoint, `machine.go:85-91`).

**Presentation projection** (`internal/presentation/state.go:66-83`): the TUI never stores
modal state itself. `DeriveUIState(phase, approvalPending, isProcessing)` maps the workflow
phase onto `StateChat` / `StateProcessing` / `StateAwaitingApproval`. A pending approval
**always** wins over processing signals.

### 1.3 Per-Mode Contract

#### `/ask` — ModeAsk
- **Intent scope:** Direct read-only chat. Explanation, inspection, understanding.
- **Transition triggers:**
  - In: `parseModeShorthand("/ask…")`, `$prompt` mode-guard router (auto-switch from any mode),
    action chip `reject-plan` (`Command: "/ask"`), route-confirm `Esc` fallback,
    initial resolver state (`NewResolver` starts at `ModeAsk`).
  - Out: `/plan /build /investigate /review` mode commands; `Alt+F` handoff → `/investigate`
    (`keys.go:134-207`); hybrid intent router auto-switch (`router_wiring.go`).
- **Display surface:** No handoff capabilities (empty `askView`, `workspace.go:308-312`);
  prompt indicator `ask)`. Context assembled by the **Context Planner** via
  `prepareAskStreamCmd` (async `askStreamPreparedMsg`), local intent interception
  (`interceptLocalIntent`) answers greetings with zero LLM.
- **Mode-lock rule (`commands.go:435`):** free-form input in `/ask` **bypasses the intent
  router** — `$prompt` is the only structured sub-command. This is the "/ask Direct Chat boundary".
- **Context handling:** governed by the planner's `Plan()` (token budget, chunk rank/dedupe).
  The V3 `PolicyEdit` (default) governs pipeline context when `/ask`-like intent runs headless.

#### `/plan` — ModePlan
- **Intent scope:** Architecture, migrations, refactors. Synthesis of atomic execution tasks.
  No execution.
- **Transition triggers:**
  - In: `/plan…`, `$prompt`→(handoff), `Alt+F` frontend-UI bypass, investigate `IsFrontendUI`
    bypass, action chip `plan-solution` (`Command: "/plan"`), `formulate-plan` chip (`/plan`).
  - Out: `/build` (only through **explicit plan approval** — chip `approve-plan` sets
    `m.planApproved = true`, or `action.ID == "approve-plan"`), `/ask` (reject chip).
- **Display surface:** TODO checklist (`PendingTodos`) + approval chips
  (`planView`, `workspace.go:314-367`): `Approve & Run /build` (`alt+p`), `Reject & Back` (`alt+r`),
  `Execute & Verify Patch` (`alt+c`).
- **Mode boundary law (`commands.go:810-818`):** `/plan` performs **no** semantic scanning —
  no `lx search`, no retrieval. It is a deterministic translator of structured ledger data.
- **Execution paths (priority order):** structural engine synthesis from forensic ledger →
  **Intent Compiler prime path** (`TryPlan`, zero model calls) → **microkernel prime path**
  → legacy conversational assembly (`ctxpkg.Builder.BuildPlanAssembly`) → `streamCmd`.
  Guards: `planHasNothingToSynthesize`, `plan.CheckTokenBudget`.

#### `/build` — ModeBuild
- **Intent scope:** Implement, refactor, write tests. The only mode with full write/shell/test/
  patch/checkpoint capability.
- **Transition triggers:**
  - In: `/build…`, plan approval (`approve-plan` → `Command: "/build"`), fast-track
    `execute-build` chip (`alt+b`), `$prompt` compressor fast-track, `$hot`, `$fix`,
    investigate mutation-intent bypass (`hasMutationIntent`+`hasExecutableBuildTarget`),
    `/review $test` does **not** enter build.
  - Out: `/plan`, `/ask`, `/investigate`, `/review` — but **Rule A** below guards entry.
- **Gatekeeper Rule A (`commands.go:1479-1484`):** auto-transition to `/build` from a non-build
  mode is **blocked** unless `modeChangeAuthorized` (explicit mode command / chip) or
  `planApproved` (explicit plan sign-off) is true:
  `"State Transition Blocked: File modifications are only allowed inside /build mode after /plan approval."`
- **Display surface:** Diff Viewer / proposal dock (`buildProposalReadyMsg` → `pendingProposals`),
  SHELL_EXEC permission box, hotfix approval gate, `Ctrl+O`-expandable exec entries in the
  activity tree, effort selector.
- **Execution:** deterministic executor on **structured tasks only** (`runBuildCmd`,
  `commands.go:2371-2481`) — zero-task guard halts; legacy per-task loop
  (`handleBuildRun`) vs unified fast-track (`runBuildFastTrack`, native `write_file`/`apply_patch`
  tool loop through `ToolCallBuffer`) chosen by `isFastTrackEligible` (≥2 idle
  `FILE_MUTATE`/`GIT_ACTION` tasks).
- **Context handling:** rewrite sanitization — `isFullRewriteIntent` strips obsolete file bytes
  (`fastTrackFileContext`), `mutationBudget.ScaleBudget(n)` for multi-step; strict handoff payload
  (`buildStrictHandoffPayload`) excludes conversational history. V3 `PolicyRewrite` forces
  greenfield full-file overwrite + `readBlocked`.

#### `/investigate` — ModeInvestigate
- **Intent scope:** Debug bugs, failures, regressions. Bounded forensic loops.
- **Transition triggers:**
  - In: `/investigate…`, `Alt+F` ask-handoff, action chip `investigate-root-cause`
    (`Command: "/investigate"`), `re-investigate` chip (`alt+i`), hybrid router.
  - Out: `/plan` (frontend-UI bypass), `/build` (mutation bypass), `/ask`.
- **Display surface:** Forensic Context Ledger, `[PKT-N]` analytical packets,
  stack-trace slicer (`runAutoTraceCmd`), SLM diagnosis line.
- **Capability contract enforcement (`agents.go:29-33`):** `!currentMode.CanShell()` → hard fail;
  `currentMode.CanWrite()` → hard fail ("violating capability contract").
- **Intent-based bypass (`commands.go:544-570`):** `investigate.ClassifyIntent` routes
  `IsFrontendUI()` → `/plan`, and `hasMutationIntent`+`hasExecutableBuildTarget` → `/build`
  (deadlock guard). Invocation budget capped by `maxInvestigateInvocations`.

#### `/review` — ModeReview
- **Intent scope:** Audit changes, detect risks, inspect regressions. 100% read-only.
- **Transition triggers:**
  - In: `/review…`, `/review $test` composite, `runReviewCmd` auto-trigger.
  - Out: `/plan /build /investigate /ask` (explicit only).
- **Display surface:** risk findings, review score (`/100`), recommendations, evidence ledger.
- **Read-only enforcement (`agents.go:420-429`):** `runReviewCmd` hard-fails on
  `CanWrite/CanShell/CanPatch`. `$fix` is blocked in `/review` and `/investigate`
  (`commands.go:5696-5712`) with "Write access required. Switch to /build.".

### 1.4 V3 Planning Modes (`pkg/planner/mode.go`, `pkg/app/plan.go`)

The headless runtime has a second, orthogonal mode axis:

| Mode | Meaning | Selection |
|---|---|---|
| `ModeAuto` | detect greenfield vs brownfield from workspace | `defaultDetector` (any manifest/source → brownfield) |
| `ModeGreenfield` | one-shot batch full-file overwrite; Search/Replace disabled | forced when `PolicyRewrite` **or** `PreserveWorkspace == false` |
| `ModeBrownfield` | interactive edit-and-verify repair graph | existing workspace; verify command toolchain-aware (`go build ./… && go test ./…` etc.) |

`ExecutionModeForPolicy(rewritePolicy, preserveWorkspace)` is the single decision seam
(`pkg/planner/mode.go:36-44`).

### 1.5 Context Policies (`pkg/op/policy.go`) — "Context Handling Rules"

The `StrategyRegistry` (Open/Closed — `Register` extensible resolvers) maps
`OperationSemantics` → `ContextPolicy`. This is the governance that Section 1's "Context
Handling Rules" refer to:

| OperationSemantics | Resolver | ContextPolicy | Prompt behavior |
|---|---|---|---|
| `create_project` | `GenerateStrategyResolver` | `PolicyGenerate` | No baseline injected; pure User Intent |
| `rewrite_project` | `RewriteStrategyResolver` | `PolicyRewrite` | **Strips obsolete file contents**, injects target paths ONLY + `RewriteDirective`; `readGuard → readBlocked` |
| `add_feature` / `refactor` | `EditStrategyResolver` | `PolicyEdit` | Bounded baseline excerpts (`<<<FILE / </FILE>>>`, cap 8 KiB × 8 files) |
| `fix_bug` | `PatchStrategyResolver` | `PolicyPatch` | Baseline + error-trace/diff corrective context |

Default (no resolver matches) = `PolicyEdit` — the conservative choice that never strips content.

---

## SECTION 2 — COMMAND & SUB-COMMAND REGISTRY

### 2.1 CLI (non-interactive) Commands (`cmd/izen/main.go`, `runtime.go`)

| Command | Target Mode | Execution Scope | Description & Trigger Behavior |
|---|---|---|---|
| `izen` | TUI boot | — | Start interactive TUI at `.` (onboarding if `.izen/config.json` missing) |
| `izen [path]` | TUI boot | workspace | Start TUI rooted at `path` |
| `izen version` / `-v` / `--version` | — | — | Print `izen version vX.Y.Z (PizenLabs)`; `os.Exit(0)` |
| `izen help` / `-h` / `--help` | — | — | Print minimalist help (usage + interactive command list) |
| `izen auth login` | — | — | **Stub** — prints "not yet implemented"; `os.Exit(0)` |
| `izen stats` | — | — | **Stub** — prints "not yet implemented"; `os.Exit(0)` |
| `izen config style <verbose\|balanced\|terse\|ultra>` | — | global `~/.izen/config.yml` | Parse+persist output style policy; injects OUTPUT STYLE directive into system prompts (`runConfigCommand`, main.go:262) |
| `izen compact [-n\|--dry-run] [path…]` | — | `AGENTS.md/RULES.md/CLAUDE.md/GEMINI.md/README.md/docs/*.md` | In-place compress prompt-overhead prose; reports byte/token savings; `compact.Optimize` |
| `izen memory optimize` | — | alias | Alias for `izen compact` (`runMemoryCommand`) |
| `izen debug [path]` | — | on-demand engine report | Materialize Lea index, Context Governance plan, Output pipeline `.logs/` report (`runDebugCommand`, main.go:389) |
| `izen run [-dir <path>] [-target <path>] "<prompt>"` | V3 pipeline | `-dir` workspace root (default `.`) | Full audit-trail execution through `app.Pipeline`; interactive Clarifier (`pkg/tui/components/ask`); conversational prompts short-circuit to chat; on failure returns exit 1 (`runtime.go:100`) |
| `izen rollback` | Rollback engine | workspace | Boots `ui.RunRollbackEngine` (recent file-mutation review; `isRollbackMode`) |

### 2.2 Interactive Slash Commands (`internal/ui/commands.go`)

**Gate:** `validSystemCommands` (`commands.go:51-66`). Any `/…` token not in this set (and not a
mode shorthand or composite) prints `unknown command: <cmd>`.

| Command | Target Mode | Execution Scope | Description & Trigger Behavior |
|---|---|---|---|
| `/help`, `/?` | any | inline record block | Render mode + command reference (incl. `$` sub-commands and `@file`). `handleCommand` case at commands.go:1878 |
| `/quit` | — | process | `cleanShutdownCmd()` — goodbye + clean exit |
| `/usage` | any | inline inspector | Provider/model/max-tokens, last-request token & cost breakdown, per-provider env status (`runUsageCmd`, provider.go:22) |
| `/provider <name>` | any | provider switch | Back-compat switch + deprecation tip ("Use /model…"). Bare `/provider` redirects to `/usage`. Valid: ollama, anthropic, openai, gemini, openrouter, groq |
| `/model` | any | modal picker | Open interactive fuzzy model picker (`showModelPicker`, `model_picker.go`) |
| `/model <name>` | any | session model override | Direct switch; tier-config resolution + `inferProviderFromModel` (slash→openrouter, `:`→ollama, known cloud names→openai) |
| `/objective <desc>` | any | session objective | Create+analyze a budget-guarded objective (`analyzeObjectiveCmd`, engine `BuildObjectiveContext`) |
| `/objective approve` | any | session objective | Set `HumanConfirmed=true`, promote status to `Planned` (unblocks outbound pipelines) |
| `/clear` | any | full workspace reset | Purge records, ContextLedger, tasks, handoff context, token counters, build gates; `tea.ClearScreen` |
| `/drop` | any | file attachments | Detach all context files |
| `/drop <@file\|path>` | any | file attachment | Detach one context file |
| `/undo` | build only | git/checkpoint | Single-step undo: restore last session checkpoint (`runUndoCmd`, agents.go:550) |
| `/undo --all` / `all` | build only | git | `git checkout .` — revert all working-dir changes |
| `/undo --session` / `session` | build only | checkpoint | Restore session-start snapshot (`ShadowCP.RestoreSessionStart`) |
| `/commit [msg]` | build only | git | **Blocked outside `/build`** ("/commit is only available in /build mode"). Stage-all, squash consecutive `izen build:` checkpoints, generate commit message (`runCommitCmdAgent`, agents.go:608) |
| `/checkpoint` | build only | git | **Stub** — "not yet implemented" (commands.go:2094) |
| `/arch [layer\|pkg]` | any | Lea graph | Render codebase structure via Lea graph (`renderArch`); indexes if not ready (`spinnerTickCmd`) |
| `/explain-decision` | any | read-only | Evidence inspector — why a tech stack was chosen (`inference.AnalyzeWorkspace` → ranked hypotheses, explain.go:20). Zero LLM |
| `?` | any (empty input) | help overlay | Toggle help overlay (`update.go:2885`) |

### 2.3 Mode Shorthand Commands (`parseModeShorthand`, commands.go:1422-1440)

`/ask`, `/plan`, `/build`, `/investigate`, `/review` and their `<content>` forms
(`/plan build a login page`). **`/mode <name>` is NOT a direct-input command** — it exists only
in the **action-chip** path (`handleChipActivation`, commands.go:6453-6470). Typed literally,
`/mode build` fails the `validSystemCommands` gate with `unknown command`. (Discrepancy vs.
`main.go` help text, which advertises `/mode <name>`.)

Behavior notes:
- `/review` alone auto-runs `runReviewCmd("")` (commands.go:359-365).
- `/build` while already in `/build` with staged tasks/todos/ledger auto-triggers
  `runBuildCmd("")` (commands.go:371-378).
- `/[mode] <content>` applies `handleMessageContent(content)` immediately.

### 2.4 `$` Sub-Commands (`handleReviewDollar`, commands.go:5675-5843)

| Command | Available In | Execution Scope | Behavior |
|---|---|---|---|
| `$prompt <idea>` | **global** (any mode) | → `/ask` handoff | Mode-guard router: transitions to `/ask`, runs `runAskPromptHandoffCmd` with the raw idea; emits FollowUp chip `alt+f` (Forward to /investigate). Also compressor fast-track + direct-mutation fast-track (see §4) |
| `$hot <prompt>` | `/build` | hotfix | Stash plan → clear queue → synthesize single `FILE_MUTATE` task → generate patch (local fuzzy replace or LLM) → **approval gate** (`Alt+A/Enter` apply, `Alt+R/Esc` abort, `hotfixActive` restore) |
| `$fix [target]` | `/build` | build engine | Auto-fix from last test/run failure (`runFixCmd`). **Blocked in `/review`** (write access) and `/investigate` |
| `$test [path]` | `/review`, `/investigate` | test suite | Safety-gated test execution (`pendingTestConfirm` for large repos — `countGoFiles`), `runTestCmd` |
| `$run [path]` | `/review` | go build | `go build` (safety-gated) — `runRunCmd` |
| `$log [--all]` | `/review`, `/investigate` | implicit pipeline | Evaluate shell failure trace; `--all` = full unfiltered history; `#N`-scoped telemetry (`runLogViewCmd` / `runLogCmd`) |
| `$env` | `/investigate` | env diagnostics | Go version, git branch/hash/dirt, relevant env vars (`runEnvCmd`) |
| `$trace [fn]` | `/investigate` | race trace | `go test -run=<fn> -v -race` live; bare `$trace` = auto-trace from saved context log (`runAutoTraceCmd`, stack-frame slicer) |
| `$diagnose` | `/investigate` | SLM | One-sentence root-cause via local model (`providers.DiagnoseSystemPrompt`); stores in session + handoff |

**Composite fast-query** `/review $test` (`command.IsReviewTestComposite`, router.go:27-40):
runs dynamic tests → injects telemetry into forensic ledger → comprehensive risk review
(test + git diff). Evaluated at the very top of the input tree in both `handleInput` and
`handleCommand`.

### 2.5 `!<command>` Shell Escape (handleInput, commands.go:157-196)

- Requires `currentMode.CanShell()` — otherwise `shell execution blocked in /<mode> mode (no CapShell)`.
- Passes the **Shell Firewall** (`shellFirewall`, commands.go:6211): mode allowlist for
  `/investigate` (only `go test`, `go version`, `git status`, `git diff`, `dlv`) + **global
  blacklist** (`rm `, `sudo`, `chmod`, `chown`, `mkfs`, `dd `, `mv /*`, `> /dev/gpi`,
  `apt-get`, `apt `, `dpkg`, `yum `, `dnf `). Blocked → `[SECURITY ALERT]`.
- Output streams as `roleSystem` records; errors sanitized via `providers.SanitizeAPIError`.
- **Proposed-shell checkpoint:** when the agent proposes a command it is injected into the input
  bar (`proposedShellCmd`) and only executes on `Enter` (deterministic, visible).

### 2.6 `@file` References & `@Agent` Scope Targeting

- `@<path>` tokens in a message are collected as file references (`handleMessageContent`,
  commands.go:458-470 → `m.pendingFileRefs`, `m.attachedFiles`) and resolved by the Context
  Planner's governed `FileSource` (no raw disk reads). `@` triggers recursive file autocomplete
  (limit 20; skips `.git/.izen/vendor/node_modules/dist/build/…`).
- `expandFileRefs` substitutes the `@ref` token with governed file context for /ask.
- `resolveHotfixTarget` strips `@` for `$hot` target resolution; `@basename` also feeds
  `isHotfixLocalCandidate` (zero-token local replace eligibility).

---

## SECTION 3 — KEYBOARD SHORTCUTS & INTERACTIVE TUI CONTROLS

### 3.1 Global & Emergency (`update.go:94-110`, `keys.go`)

| Key | Scope | Handler Action |
|---|---|---|
| `Ctrl+C` | **always** (top of Update, unblockable) | If processing/approval/stream/agent/review/pipeline/planPending → `handleEmergencyInterrupt("ctrl-c")`: cancel all background ctx, `execution.KillAllOrphans()`, resolve approval gate, restore `StateChat`. Else clear input. In `handleKey` it also cancels an active stream via `runtimeCancelCmd` |
| `Esc` | always | Emergency abort while `StateProcessing`/`planPending`; otherwise contextual: dismiss help overlay / suggestions / autocomplete / model picker; cancel active stream; clear proposed shell cmd; clear input buffer. **3 consecutive Esc in `StateChat` → enter Vi-mode** (triple-escape detection, update.go:141-160) |
| `Ctrl+D` | chat + processing | Emergency abort in `StateProcessing`; else **clean shutdown** when input is empty and nothing is running (`cleanShutdownCmd`) |
| `?` | chat (empty input) | Toggle help overlay |
| `Alt+O` | global | `toggleThoughtBlock()` — expand/collapse live ThinkingBuffer or legacy ThinkingPanel (or `$`-exec entry via Ctrl+O path); priority: running command exec → ThinkingBuffer → ThinkingPanel → trace buffer |
| `Ctrl+O` | global | Same as `Alt+O`; falls back to cycling foldable build-log entries (`logStore.ToggleCycle`) |
| `Alt+F` | `/ask` | **Handoff to `/investigate`**: requires a valid ask Context Ledger (`ask_handoff` packet or Diagnostics or `handoffLedgerContent`); intent bypass routes frontend-UI → `/plan`, mutation → `/build`; otherwise `setMode(ModeInvestigate)` |

### 3.2 Modal Approval States (`handleKey`, keys.go)

`StateAwaitingApproval` key routing depends on which gate is active:

| Gate | Keys | Action |
|---|---|---|
| **Intent disambiguation** (`pendingRouteConfirm`) | `1`–`5`, `←`/`→`, `Enter`, `Esc` | Digit selects mode; arrows cycle highlight; Enter confirms highlighted; Esc → `/ask` (`cancelRouteSelection`) |
| **Effort selector** | `←`/`→` | Cycle `EffortAuto/Low/Medium/High` |
| **Proposal diff inner scroll** | `↑`/`↓`, `PgUp`/`PgDn` | Scroll within expanded diff dock (`proposalDiffOffset`) |
| **Main viewport behind modal** | `j`/`k`, `Ctrl+U`/`Ctrl+D`, `Space` | Scroll history (tracks `userIsScrollingUp`) |
| **`$hot` hotfix approval** (`pendingHotfixTask`) | `Alt+A` / `Enter` | Approve — apply patch to disk, restore stashed plan |
| | `Alt+R` / `Esc` | Reject — abort hotfix cleanly, zero disk mutation, restore stashed plan |
| **SHELL_EXEC permission box** (`pendingBuildApproval`) | `Alt+A` / `Enter` | Allow once — `runBuildShellExec(task)` |
| | `Alt+L` | **Allow Always** — `pendingBuildAllowAlways = true`, then execute |
| | `Alt+R` / `Esc` | Reject — task marked `stalled` |
| **Native tool-call buffer** (`toolCallBuffer.HasPending()`) | `a` | Accept single call, apply to disk |
| | `l` | Allow all (`ApproveAll`) |
| | `r` / `Esc` | Reject, discard buffer |
| | `e` | Cycle effort level |
| **File-mutation proposals** (`pendingProposals`) | `Alt+A` / `Enter` | Accept first proposal (`applySingleProposal`) + runtime approve |
| | `Alt+L` | Accept all (`applyAllProposals`, `acceptAll=true`) |
| | `Alt+P` | Toggle expanded diff on first proposal |
| | `Alt+R` / `Esc` | **Reject + rollback** (`execEng.RollbackTransaction()`, `sess.ClearHistory`, task → `stalled`) |

### 3.3 Chat / Input Navigation

| Key | Action |
|---|---|
| `Enter` | Submit input (`handleInput`); executes `proposedShellCmd` if set; resolves pending test confirm; batch-dispatch shimmer + smooth ticks |
| `↑` / `↓` | History navigation (skipped while suggestions active) |
| `Tab` / `Enter` (autocomplete active) | Complete highlighted `/…` or `@file` or `$…` suggestion |
| `↑`/`↓` (autocomplete active) | Move dropdown highlight |
| `Esc` / `Space` (autocomplete active) | Dismiss dropdown |
| `j`/`k`, `Ctrl+U`/`Ctrl+D`, `PgUp`/`PgDn`/`Home`/`End`, `Space` | Viewport scrolling (with scroll-lock tracking) |
| `Alt+P` (StateAwaitingApproval + proposals) | Toggle proposal diff expansion (update.go:122-130) |

### 3.4 Action-Chip Hotkeys (state: `StateChat`, update.go:2863-2869)

Chips are pure data produced by the workflow layer (`workspace.go`/`action.go`); the renderer and
the update loop only project them. A hotkey activates the matching `Action.Command` via
`handleChipActivation`. Shortcuts are **alt+ only** (single-char hotkeys banned to protect input).

| Shortcut | Chip / Source | Command |
|---|---|---|
| `Alt+A` | `investigate-root-cause` (`failureResult`) | `/investigate` + query |
| `Alt+D` | `commit-safe-baseline` (`buildVerifyResult` passed) | `/commit` |
| `Alt+R` | `rollback-workspace` (`buildVerifyResult` failed) | `/undo` |
| `Alt+P` | `plan-solution` (`investigateResultActions`) | `/plan` |
| `Alt+I` | `re-investigate` (`investigateResultActions`) | `/investigate` |
| `Alt+P` | `approve-plan` (`planApprovalActions`) | `/build` (sets `planApproved=true`) |
| `Alt+R` | `reject-plan` (`planApprovalActions`) | `/ask` (clears staged tasks) |
| `Alt+B` | `execute-build` / `execute-build` (fast-track/planView) | `/build` |
| `Alt+B` | `formulate-plan` (investigateView) | `/plan` + proposed-fix query |
| `Alt+C` | `execute-patch` (planView) | `/build` |
| `Alt+R` | `reject-plan` (fastTrackPlanActions) | `/ask` (reset & clear) |
| `Alt+F` | `ask-prompt-handoff-investigate` (from `$prompt`) | `/mode investigate` + refined query |

### 3.5 Vi-Mode (`handleViModeKey`, update.go:3169+)

Entered via triple-`Esc`. `i` exits. Motions: `h/j/k/l`, `0`, `$`, `gg`, `G`, `Ctrl+U`/`Ctrl+D`
(half-page), `↑`/`↓`. Search: `/query` (`n`/`N` next/prev). Visual selection: `v` + `y` (yank to
clipboard). Command line: `:` (`q` to quit), `/` search prompt. `Esc` in cmd mode returns to normal.

### 3.6 Model Picker (`update.go:167-190`)

`Esc` closes; all other keys forwarded to the picker model. `Enter` on a highlighted model →
`modelSelectedMsg`.

### 3.7 Clarification AskModel (`pkg/tui/components/ask`)

`izen run`'s clarification gate runs the standalone Ask component. `Esc` resolves to the default
answers so a headless/CI run never deadlocks; `Enter` confirms selections.

---

## SECTION 4 — PROMPT SYNTAX & INTENT ROUTING

### 4.1 Input Grammar (evaluation order in `handleInput`, commands.go:135-440)

```
1. (empty)                                  → nil
2. streaming || agentRunning                → "Input blocked: task active."
3. pendingTestConfirm                       → handleReviewTestConfirm (y/n gate)
4. "!<cmd>"                                 → shell (firewall-gated)
5. "/review $test" composite                → runReviewTestComposite
6. "$prompt[ <idea>]"                       → $prompt router (§4.4)
7. "$<sub>"                                 → handleReviewDollar (§2.4)
8. "/[ask|plan|build|investigate|review]…"  → parseModeShorthand (§2.3)
9. "/<other>"                               → handleCommand (§2.2, validSystemCommands gate)
10. (in /build) "run [N]"                    → handleBuildRun(N)
11. (in /build) failed task present          → amendBuildTask(failedStep, line)
12. free-form                               → Hybrid Intent Gateway (§4.3) [NOT in /ask] → handleMessageContent
```

### 4.2 `@Agent` Scope Targeting & `@file`

- **`@file`** — file reference token, resolved through the governed planner `FileSource`
  (never raw disk) and expanded into prompt context. `@` = autocomplete trigger.
- **`@Agent`** is the same token family: `@<path>` cleans and attaches the referenced file.
  `$hot`'s `@LICENSE`-style references additionally enable the zero-token local-replace path.
- `/drop @file|all` detaches references; `pendingFileRefs` accumulate during autocomplete.

### 4.3 `$prompt` Expansion & Submission

`$prompt` is a **global mode-guard router to `/ask`** — not an execution mode (commands.go:210-335):

1. **Compressor fast-track** (`gateway.CompressPrompt`): if `BypassInvest && Target != ""`,
   skip architect analysis entirely → stage `FILE_MUTATE` tasks (multi-file decomposition or
   `command.GenerateFallbackPlan`) → `planResultMsg{IsFastTrack: true}` (auto-approved,
   `fastTrackPlanActions` chips surface in any mode).
2. **Direct-mutation pre-guard** (`gateway.ClassifyDirectMutation`): single-file non-code
   mutation → same fast-track to `/build`, zero LLM.
3. **Mode-guard**: from a non-`/ask` mode → `setMode(ModeAsk)` + `runAskPromptHandoffCmd(raw)`.
4. In `/ask` → direct `runAskPromptHandoffCmd` (non-streaming, isolated system prompt
   `AskPromptHandoffSystemPrompt`; never touches session history — isolation contract).
5. Result carries the `ask-prompt-handoff-investigate` chip (`alt+f`) for `/investigate` deep-dive.

### 4.4 Slash-Override vs Natural-Language Routing

- **Slash override wins** — `/…`, `$…`, `!…` are parsed deterministically before any LLM or
  intent classifier runs (order above).
- **Free-form (NL)** routes through the **Hybrid Intent Gateway** (`m.intentRouter`,
  `routeFreeInput`, commands.go:447-455; `router_wiring.go:26-83`):
  1. Deterministic fast path first.
  2. Semantic `IntentClassifier` fallback (async, 30s ctx).
  3. **Confident** (`res.Intent` mapped via `modeForIntent`) → auto mode-switch +
     `handleMessageContent(line)`.
  4. **Ambiguous** (`ConfirmationRequirement`) → freeze `StateAwaitingApproval`, render
     interactive mode-selection dock (1-5 / ←→ / Enter / Esc).
  5. Error / no router → plain `handleMessageContent` (current mode).
- **`/ask` mode-lock:** free-form in `/ask` bypasses the router entirely (§1.3).

### 4.5 Intent-Based Overrides (inside `handleMessageContent`)

- `/investigate` + `hasMutationIntent` + `hasExecutableBuildTarget` → `/build` (bypass).
- `/investigate` + `investigate.ClassifyIntent(content).IsFrontendUI()` → `/plan` (bypass).
- `$hot` prefix in message → `runBuildCmd` (fast-track build).
- `/build` message → `retrieval` context compressor (graph-aware) applied to content.

### 4.6 V3 Headless Execution Flow (`izen run` → `pkg/app/pipeline.go`)

```
Request{Intent, Targets}
  │  no IntentIR && !IsConversationalIntent
  ▼
IntentCompiler.Compile → Normalizer → EntityResolver(semantic extractor)
  → ConflictDetector → AmbiguityDetector  → ir.IntentIR{Category,TargetType,PreserveWorkspace,…}
  ▼ DecisionAmbiguity?
  ├─ yes → ClarificationGate: publish TypeClarificationRequired, block on Clarifier
  │        (ask.Run interactive; no Clarifier → ir.DefaultAnswers auto-resolve)
  │        applyClarification folds PreserveWorkspace back
  ▼
IsConversationalIntent? → direct chat pass (chatSystemPrompt) → res.Answer [exit]
  ▼
ResolveCapabilitiesForIntent(registry, intent) → []capability.Capability
  ▼
semantics = SemanticsFromCategory(IntentIR.Category)            // op.SemanticsFromCategory
policy    = PromptBuilder.CompilePolicy(semantics)              // StrategyRegistry.Resolve
readGuard.setMode(fullOverwriteActive(policy, IntentIR))        // rewrite → readBlocked
SystemPrompt = BuildSystem(policy, caps, targets)               // policy-specific block
UserPrompt   = BuildUser(policy, intent, targets)
  ▼
tx.Begin()   ── generation / extraction / validation / alignment loop ──
  │  generate → extract(MarkdownFence→JSON) → DecisionAccept?
  │    Semantic Alignment Gate (capability.CheckAlignment)  → mismatch → rollback+reprompt
  │    Capability Validation Gate (capability.Validate)      → fail     → rollback+reprompt
  ▼
plan(artifacts, intentIR, policy) → ModeAuto detect / ExecutionModeForPolicy
  → brownfield.NewBrownfieldPlanner (verify + 2m timeout, ExecuteAndRepair ≤ maxRepairs)
  → greenfield.NewGreenfieldPlanner (TxFS-backed)
  ▼
validateArtifactPaths → planExecutionOrder (DAG, cycle rejection)
  ▼
kernel.NewEngine(bus).Execute(graph)  ── side-effects via event bus ──
  ▼
tx.Commit()   (any failure → tx.Rollback(); greenfield writes hit disk only at Commit)
  ▼
Result{Mode, IntentIR, Capabilities, Artifacts, Validations, Plan, Events, BlockedReads}
```

Retry budgets: `maxAttempts=3` extraction, `maxRepairs=2` repair rounds. Every rejection
rolls the transaction (`restartTx`) before re-entering the loop — **rejected output can never
reach disk**.

---

## SECTION 5 — SAFETY GATES & FALLBACK BEHAVIORS

### 5.1 Clarification Gate (`AskModel`)

- **Where:** `pkg/app/pipeline.go` `clarifyGate` (V3); TUI equivalent = route-confirm dock.
- **Behavior:** ambiguous intent (`IntentIR.DecisionAmbiguity`) → publish
  `TypeClarificationRequired` with `[]ir.ClarificationQuestion` → block on a buffered response
  channel → `Clarifier` (interactive `pkg/tui/components/ask` in `izen run`) resolves.
- **Fallbacks:** no Clarifier → `ir.DefaultAnswers(questions)` auto-select (headless never
  hangs); failing Clarifier degrades to defaults + `TypeTaskFailed` event; `Esc` in AskModel =
  defaults; cancelled context surfaces `ctx.Err()`.
- **Reconciliation:** answers fold back via `applyClarification` — `OptionReplaceWorkspace`
  → `PreserveWorkspace=false`; `OptionBuildAlongside/MergeSelective/TypeYourOwn` → `true`.

### 5.2 ReadGuard (`pkg/app/readguard.go`)

- **Where:** pipeline read boundary + `PromptBuilder.readBaseline` (`prompt.go:262-290`).
- **Behavior:** two modes — `readAllowed` (PolicyEdit/Patch baseline injection) and
  `readBlocked` (PolicyRewrite or non-preserving intent): every workspace read is **sanitized
  to empty output** and counted on `Result.BlockedReads`, so obsolete code can never anchor a
  small model.
- **Path safety:** `ReadWorkspaceFile` refuses paths that escape the workspace root.

### 5.3 Semantic Alignment Gate (`pkg/capability/alignment.go`, pipeline.go:368-383)

- **Where:** before capability validation, before any write.
- **Trigger:** `IntentIR.TargetType` set + `CheckAlignment` fails → `ErrSemanticMismatch`.
- **Rule:** for `targetType == "portfolio"`, any artifact carrying to-do/task-list scaffolding
  (concrete signals: `todolist`, `addtask`, `let todos`, checkbox ids, `<title>/<h1>` tokens)
  is a hard mismatch ("To-Do App" detected).
- **Behavior:** transaction rolled back; `appendSemanticAlignmentRejection` re-prompts with an
  explicit REGENERATE directive; after `maxRepairs` → `SemanticMismatchError` (wraps
  `ErrSemanticMismatch`), nothing written.

### 5.4 TxFS Rollback Boundaries (`pkg/fs/txfs.go`, pipeline.go)

- **Boundary open:** `p.tx.Begin()` after prompts are built, before the first generation.
- **Boundary close:** `p.tx.Commit()` only after the executed plan succeeds — greenfield writes
  reach disk only here (`pkg/planner/greenfield` is TxFS-backed).
- **Rollback triggers:** generation error, extraction rejection (after `maxAttempts`),
  alignment gate, validation gate, context cancellation, plan/execute failure, commit failure.
- **Repair loop:** each rejection calls `restartTx()` (rollback + fresh Begin) so the next round
  starts clean.
- **Brownfield exception:** parent dirs are created eagerly and writes run against the live
  workspace inside `ExecuteAndRepair` (repair budget = `maxRepairs`); path-escaping artifacts
  are rejected by `validateArtifactPaths` before any directory creation.

### 5.5 TUI Execution Guards

| Guard | Enforcement site | Behavior |
|---|---|---|
| **Mode-capability matrix** | `internal/modes/modes.go` + `handleInput` | `!CanShell()` blocks `!cmd`; `/review` engine asserts read-only; `/investigate` asserts no-write + shell |
| **Rule A mode gate** | `setMode`, commands.go:1479 | Blocked `/build` auto-entry without explicit authorization or plan approval |
| **Zero-task build halt** | `runBuildCmd`, commands.go:2381 | `[BUILD HALTED] No active tasks found` — never enters an empty execution loop |
| **Shell Firewall** | `shellFirewall`, commands.go:6211 | `/investigate` read-only allowlist + global blacklist; hard block message |
| **Max investigate invocations** | `commands.go:518` | caps forensic loops; suggests `/objective` or restart |
| **Invoke-time guards** | `keys.go:135-140` | `Alt+F` rejected while processing/approval/streaming |
| **First-token / completion guards** | `commands.go:1211-1216` | plan synthesis: 120s cloud / 150s local first-token, 180s hard ctx; build fast-track 5m (`buildGenerationTimeout`); investigate 60s |
| **Emergency escape hatches** | `update.go:94-110`, `handleEmergencyInterrupt` | Ctrl+C / Esc / Ctrl+D unblockable in locked states; full deterministic reset to `StateChat` |
| **`unwindBuildFailure`** | `model.go:1769-1790` | on stream/engine failure: release approval gate → `EventReset` → `workflowRT.Reset()` → re-derive `StateChat` |
| **Scope Guard (plan tasks)** | `commands.go:1304-1347` | `control.ValidateStagedPlan` vs `AllowedFiles`; one retry with `FormatRepromptInstruction`, else annotated error |
| **ReadGuard + budget enforcement** | `pkg/app` + `plan.CheckTokenBudget` | context governance on both surfaces |

---

## 6. Known Stubs & Discrepancies (static findings)

These match the implementation 1:1 and are recorded for completeness:

- `izen auth login` and `izen stats` are **not implemented** (CLI stubs, `os.Exit(0)`).
- `/checkpoint` is a **stub** ("not yet implemented").
- `/mode <name>` is **only reachable via action chips**, not as typed input (see §2.3) —
  the `main.go` help text advertises it but the direct-input path reports `unknown command`.
- `internal/command.Router` (bare `run/apply/build/clear/undo` without `/`) is a legacy native
  router with TODO-stub handlers; the live TUI routes through `handleInput`/`handleCommand`,
  not this router.
- `renderApprovalPrompt` / `renderBuildApprovalPrompt` are wired as presentation helpers but
  marked `nolint:unused` (the interactive approval flow lives in `keys.go`/`proposals.go`).

---

*Generated by static audit of `cmd/izen`, `internal/ui`, `pkg/app`, `pkg/planner`,
`pkg/capability` (plus `internal/modes`, `internal/core/workflow`, `internal/presentation`,
`internal/command`, `pkg/op`, `pkg/fs`). Cross-checked against `go build ./...` and
`go test ./... -race`.*
