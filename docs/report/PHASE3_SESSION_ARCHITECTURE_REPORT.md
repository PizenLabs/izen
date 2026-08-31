# PHASE 3 — SESSION ARCHITECTURE SURFACE REPORT

| Field            | Value |
| ---------------- | ----- |
| Status           | COMPLETE — ALL DEFINITION-OF-DONE CRITERIA GREEN |
| Version          | 1.0 |
| Date             | 2026-08-31 |
| Reference        | `docs/architecture/SESSION.md` (§10 CLI surface, §22 audit correlation, §26 delete semantics, §28 state machine, INV-SESSION-01..15); `docs/report/CODEBASE_SURVEY_REPORT.md` (Phase 1/2 seams, preserved) |
| Scope            | `internal/session/{manager,session,context,pointer,flock_*}.go`, `internal/events/{envelope,events}.go`, `internal/events/audit/{writer,store}.go`, `internal/execution/{executor,graph}.go`, `internal/session/compaction/runner.go`, `internal/contextcompiler/compiler.go`, `internal/git/engine.go`, `internal/runtime/compose/compose.go`, `internal/ui/{session_cmds,model,program}.go` |
| Lock suite       | `internal/session/phase3_integration_test.go` (8) · `internal/events/audit/session_correlation_test.go` (3) · `internal/execution/session_correlation_test.go` (3) · `internal/runtime/compose/phase3_integration_test.go` (2) · `internal/ui/session_cmds_test.go` (7) · `internal/architecture/windows_lock_parity_test.go` (3) · `internal/git/git_test.go` (+1 regression) |
| Verification     | `go build ./...` · `go build ./cmd/izen` · `go test ./... -race -count=1` · `golangci-lint run ./...` · `GOOS=windows GOARCH=amd64` cross-build of `internal/session` + `internal/events` |

> **Purpose:** complete the Session Architecture surface introduced in Phase 1
> and Phase 2: enforce cross-session audit correlation (INV-SESSION-10),
> implement workspace-boundary safety (dirty-state injection on session
> switch), deliver Windows process-lock parity with the Unix `flock` tier, and
> wire the complete `/session` control surface (inspect / rename / archive /
> delete / compact) — all while preserving the Session Manager's non-execution
> authority boundary (INV-SESSION-09).

---

## 0. Executive Summary

- **Audit correlation is strict and session-aware.** Every record persisted to
  `.izen/audit/events.ndjson` — typed domain events AND envelopes — now carries
  the originating `session_id`, stamped at bus-crossing time by the AuditLogger
  through a session resolver wired to `SessionManager.ActiveSessionID()`.
  The audit log is no longer a partial stream: typed lifecycle events are
  wrapped (with their canonical type preserved as the source discriminator)
  instead of being dropped.
- **Executions carry their session into evidence.** `ExecuteRequest.SessionID`
  flows through `ExecutionProof`, `ExecutionResult`, `ExecutionCompleted` and
  the terminal `ExecutionEvidencePayload`; the runtime resolves the active
  session at admission via a resolver when the request does not declare one, so
  autonomous/headless submissions are correlated too. Mutation traces, token
  usage and tool invocations map strictly to their originating session.
- **Workspace boundary guard injects dirty state.** On every session switch
  (`/new` and `/session resume`), the target session — and its compact context,
  which is the Context Compiler's view — receives the workspace's uncommitted
  files (git-backed, `.izen/` excluded), so a session can never silently
  overwrite work left by another session. Guard failures are fail-open with
  observability (`LastBoundaryErr`).
- **Windows lock parity is real, not a no-op.** The `!unix` fallback excluded
  Windows; a dedicated `flock_windows.go` (`//go:build windows`) now implements
  `LockFileEx` with `LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY`,
  mapping `ERROR_LOCK_VIOLATION` onto the same `errWouldBlock` retry sentinel
  the Unix tier uses — identical timeout/backoff acquisition semantics.
- **Complete `/session` surface.** `inspect`, `rename`, `archive`, `delete`,
  `compact` (plus `list`/`resume`) dispatch through the SessionManager. Delete
  is session-owned-state only (INV-SESSION-12); archiving guards `/new` from
  silently overwriting archived sessions; manual compaction is a synchronous
  seam into the Generational Compactor.
- **28 new tests** across seven files; all existing suites pass unchanged under
  `-race`; lint reports 0 issues.

---

## 1. Deliverables

| File | Contents |
| --- | --- |
| `internal/events/envelope.go` | `Envelope.SessionID` field (JSON `session_id`); exported `NewEnvelopeID()` for typed-event wrapping |
| `internal/events/audit/writer.go` | AuditLogger now persists EVERY event (typed events wrapped with `Type()` preserved in `Source`); `SetSessionResolver(func() string)` stamps the active session on every record at handling time; `Flush()` observability seam |
| `internal/events/events.go` | `ExecutionStartedPayload.SessionID` (4-arg `NewExecutionStarted`), `ExecutionEvidencePayload.SessionID` |
| `internal/execution/executor.go` | `ExecuteRequest.SessionID`; `ExecutionProof/Result/Completed.SessionID`; `RuntimeExecutor.sessionID` resolver + `SetSessionResolver`/`resolveSessionID`; `pendingMutation.sessionID` carried through `Approve`; evidence event emission includes session |
| `internal/execution/graph/graph.go` | `Graph.SessionID` + `SetSessionID`; `execution.started` carries it |
| `internal/session/manager.go` | `WorkspaceGuard` interface + `WithWorkspaceGuard`; `ActiveSessionID()`; dirty-file injection in `NewSession`/`ResumeSession` (persisted into the target record + compact context); `Lifecycle` transitions; archived-slot overwrite guard in `/new`; `Inspect` / `Rename` / `Archive` / `Delete` (INV-SESSION-12) / `CompactContext` / `bootstrapSlot` / `liveSessionFor` / `slotLifecycle` / `sessionData`; `SlotInfo` gains `Lifecycle` + `DirtyCount` |
| `internal/session/session.go` | `Session.Title` (mutable) + `EffectiveTitle()`; `Session.Lifecycle` (active/dormant/archived) with typed constants; `Session.WorkspaceDirtyFiles` |
| `internal/session/context.go` | `CompactContext.DirtyFiles` — the Context Compiler view carrier; derived from / hydrated into the session |
| `internal/session/compaction/runner.go` | `Runner.Compact(ctx, Job)` — synchronous manual-compaction seam (`/session compact`) |
| `internal/contextcompiler/compiler.go` | `renderCompact` surfaces uncommitted dirty files in the SESSION COMPACT section; fingerprint includes them (cache invalidation) |
| `internal/git/engine.go` | **Bug fix:** `parseStatus` no longer trims the leading porcelain index column — full paths (`tracked.txt`, not `racked.txt`) |
| `internal/runtime/compose/compose.go` | Wires the git-backed `WorkspaceGuard` (lazy git resolve, `.izen/` filtered), the audit logger's session resolver, and the executor's session resolver |
| `internal/ui/session_cmds.go` | Full `/session` surface: list/inspect/rename/archive/delete/compact/resume with usage errors, execution-in-flight guards, dirty-file resume banner |
| `internal/ui/model.go`, `internal/ui/program.go` | `compactionRunner` wired into the model from `app.CompactionRunner()` |

---

## 2. Audit Correlation Protocol (INV-SESSION-10)

### 2.1 Envelope-level correlation at the persistence choke point

Every line of `events.ndjson` must carry the originating `session_id`. Because
events are produced across the whole engine tree, correlation is enforced at the
single point every record crosses — the `AuditLogger`:

```
Bus ──▶ AuditLogger.handle (bus goroutine)
          ├─ typed event ⇒ wrap into Envelope{Source: ev.Type(), Kind: system}
          └─ envelope    ⇒ kept verbatim
          both: env.SessionID = resolver()     ← active session at emission time
          └─▶ buffered channel ──▶ NDJSON worker ──▶ events.ndjson
```

- The resolver is consulted per event at **handling time** (the moment the event
  crossed the bus), so a session switch between events correlates each record to
  the session active at emission — proven by
  `TestAuditLoggerSessionResolverTracksActiveSession`.
- Typed lifecycle events (`execution.started`, `execution.evidence`, …) are now
  persisted (previously dropped), making the audit log a complete stream with
  the canonical type preserved in `Source`.
- A nil resolver degrades gracefully to an empty `session_id` (harness/headless)
  — never a failure.

### 2.2 Executor-level correlation into evidence

The envelope stamp is the durable audit record; the executor additionally
carries the session into the execution's own truth artifacts so the correlation
survives independent of the audit projection:

- `ExecuteRequest.SessionID` (explicit) wins; otherwise the wired resolver is
  consulted at admission (explicit-over-resolver unit-proven).
- Stamped onto `ExecutionProof.SessionID`, `ExecutionResult.SessionID`,
  `ExecutionCompleted.SessionID`, the graph's `execution.started` event, and the
  terminal `execution.evidence` payload — so `session → executions → mutations →
  artifacts` reconstructs from either the bus stream or the sealed evidence.
- `pendingMutation.sessionID` is carried through the approval gate so an
  `Approve`-resolved execution keeps its originating session.

### 2.3 Composition wiring

`compose.Wire` connects the resolver to the single session authority:

```go
logger.SetSessionResolver(func() string { return a.Sessions.ActiveSessionID() })
a.Executor.SetSessionResolver(func() string { return a.Sessions.ActiveSessionID() })
```

End-to-end proven by `TestComposeWiresWorkspaceGuardAndAuditSessionCorrelation`
and `TestComposeAuditStampsActiveSessionOnSwitch` (two real NDJSON lines after a
pointer switch carry the old and new active ids respectively).

---

## 3. Workspace Boundary Guard & Dirty State Handling

### 3.1 The seam

`session.WorkspaceGuard` is a read-only seam the Session Manager consumes — it
never executes git itself (INV-SESSION-09, structurally enforced by the existing
forbidden-import sweep). The composition root wires a git-backed adapter that
reports `git status --porcelain` paths, excluding `.izen/` internal state.

### 3.2 Injection on switch

Both `/new` and `/session resume` resolve the guard under the two-tier lock and
inject the dirty set into the **target** session:

- `targetSess.WorkspaceDirtyFiles` — persisted into `session.json` BEFORE the
  pointer commits (so the injection is durable and crash-safe);
- `deriveCompactContext` copies them into `CompactContext.DirtyFiles` — the
  Context Compiler's view — which `contextcompiler.renderCompact` surfaces as
  `uncommitted workspace changes (from a previous session): …` under the SESSION
  COMPACT section, and the compiler fingerprint includes them so cache entries
  invalidate on change.

Guard errors are **fail-open with observability**: the switch commits and the
error is recorded as `LastBoundaryErr` (unit-proven). The UI resume banner
reports the carried-over count.

### 3.3 The git parse bug this exposed

The guard's first integration run surfaced a latent defect in
`git.Engine.parseStatus`: `strings.TrimSpace` on each porcelain line stripped the
leading index column (` M tracked.txt` → `M tracked.txt`), shifting the path one
character left (`racked.txt`). Fixed to trim only trailing whitespace per line
and locked with `TestStatusPreservesFullPorcelainPaths`.

---

## 4. Windows Process Lock Parity

The two-tier lock (`lock.go`) already retries a non-blocking exclusive flock
with timeout/backoff; the OS tier was the missing piece on Windows.

| Build tag | File | Implementation |
| --- | --- | --- |
| `unix` | `flock_unix.go` | `unix.Flock(LOCK_EX|LOCK_NB)` → `errWouldBlock` on `EWOULDBLOCK` |
| `windows` | `flock_windows.go` *(new)* | `windows.LockFileEx(LOCKFILE_EXCLUSIVE_LOCK\|LOCKFILE_FAIL_IMMEDIATELY)` over byte offset 0 → `errWouldBlock` on `ERROR_LOCK_VIOLATION`; `UnlockFileEx` for release |
| `!unix && !windows` | `flock_other.go` | no-op degraded tier (report `ErrLockUnsupported` once) |

Windows now retries contention with the **identical** sentinel and the same
acquire loop, so `ErrLockTimeout` semantics match Unix. Verified by:
- `GOOS=windows GOARCH=amd64` cross-build of `internal/session` + `internal/events`;
- `TestWindowsLockFileExImplementation` (build tag + `LockFileEx`/flags/`ERROR_LOCK_VIOLATION`/`UnlockFileEx` present);
- `TestWindowsDoesNotUseNoopFlockFallback` (the `!unix` fallback is narrowed so
  Windows compiles the real implementation, never the no-op).

---

## 5. Complete `/session` CLI Surface

| Command | Manager authority | Semantics |
| --- | --- | --- |
| `/session`, `/session list` | `List()` | Both slots with lifecycle (`ACTIVE`/`dormant`/`ARCHIVED`), id, objective, uncommitted-file count, recovery flag |
| `/session resume <A\|B>` | `ResumeSession()` | Validate → persist active (dormant) → prepare target (active) → inject dirty files → persist → atomic pointer commit (INV-SESSION-11: pre-commit failures leave the current session active) |
| `/session inspect <A\|B>` | `Inspect()` | Detached JSON view: id, title, goal, lifecycle, mode, timestamps, decisions, artifact references, uncommitted files, compact generation |
| `/session rename <A\|B> <title>` | `Rename()` | Atomic title update in `session.json`; ID immutable (INV-SESSION-01); operates on the live session when active (no unpersisted state loss) |
| `/session archive <A\|B>` | `Archive()` | `ACTIVE/DORMANT → ARCHIVED`, idempotent; archived sessions remain inspectable and resumable (SESSION.md §25); `/new` refuses to overwrite an archived dormant slot |
| `/session delete <A\|B>` | `Delete()` | Purges ONLY `.izen/sessions/<slot>/` (INV-SESSION-12); active-slot delete atomically moves the pointer to the sibling BEFORE removal (crash-safe, never a dangling pointer); project config / knowledge / audit untouched (byte-for-byte asserted) |
| `/session compact <A\|B>` | `CompactContext()` + `Runner.Compact()` + `SetCompactContext()` | Synchronous Generational Compactor run over the session history; generation sealed atomically; raw history untouched (INV-SESSION-05/06) |

All destructive/state-changing commands carry the execution-in-flight guard
(`/new`, `resume`, `delete`), and invalid slot arguments fail with usage errors
instead of panicking.

---

## 6. Architecture Lock Suite

Structural guards parse production sources with `go/parser` (whitespace/comment
immune); behavioral guards drive only exported APIs.

### 6.1 Windows lock parity — `windows_lock_parity_test.go`

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestWindowsLockFileExImplementation` | `flock_windows.go` carries `//go:build windows` and references `LockFileEx`, `LOCKFILE_EXCLUSIVE_LOCK`, `LOCKFILE_FAIL_IMMEDIATELY`, `ERROR_LOCK_VIOLATION`, `UnlockFileEx`, `errWouldBlock` | ✅ PASS |
| `TestWindowsDoesNotUseNoopFlockFallback` | `flock_other.go` build tag is exactly `!unix && !windows` (Windows never degrades to the no-op) | ✅ PASS |
| `TestUnixFlockBuildTagPins` | `flock_unix.go` keeps `//go:build unix` + `unix.Flock(LOCK_EX)` | ✅ PASS |

### 6.2 CLI surface completeness — `session_invariants_test.go`

| Test | Mechanism | Result |
| --- | --- | --- |
| `TestSessionCLISurfaceComplete` | `session_cmds.go` dispatches every subcommand verb (list/inspect/rename/archive/delete/compact/resume) and reaches each SessionManager authority method | ✅ PASS |

### 6.3 Behavioral invariants — `session/phase3_integration_test.go`

| Test | Proves | Result |
| --- | --- | --- |
| `TestWorkspaceGuardInjectsDirtyFilesOnResume` | Dirty files injected into the resumed target AND its compact context | ✅ PASS |
| `TestNewSessionCarriesDirtyFilesIntoFreshSession` | A fresh `/new` session receives the workspace dirt (no silent overwrite) | ✅ PASS |
| `TestWorkspaceGuardErrorIsNonFatal` | Guard failure never blocks the switch; `LastBoundaryErr` records it; no partial dirty set | ✅ PASS |
| `TestSessionRenameAtomicallyUpdatesTitle` | Rename persists trimmed title, keeps ID, mirrors the live session, rejects blank titles | ✅ PASS |
| `TestSessionArchiveLifecycle` | Archive idempotent; archived inspectable; `/new` refuses to overwrite archived; resume re-activates | ✅ PASS |
| `TestDeleteActiveSlotPreservesProjectState` | INV-SESSION-12: deleting the active slot moves the pointer to the sibling and leaves config + audit byte-preserved | ✅ PASS |
| `TestDeleteDormantSlotPurgesOnlySessionState` | Dormant deletion keeps project knowledge + sibling; idempotent re-delete | ✅ PASS |
| `TestInspectReturnsDetachedRecord` | Inspect never hands out the live pointer (mutations cannot leak) | ✅ PASS |
| `TestManualCompactSeam` | `SetCompactContext` accepts a manual generation; raw history preserved | ✅ PASS |

### 6.4 Audit + executor correlation — `events/audit`, `execution`, `compose`

| Test | Proves | Result |
| --- | --- | --- |
| `TestAuditLoggerStampsSessionIDOnEveryRecord` | Every persisted record (typed + envelope) carries the resolved session; typed `Source` preserved | ✅ PASS |
| `TestAuditLoggerWithoutSessionResolverLeavesEmptySessionID` | Harness mode degrades to empty id, never fails | ✅ PASS |
| `TestAuditLoggerSessionResolverTracksActiveSession` | Per-event resolution: a switch between events correlates each record to its own session | ✅ PASS |
| `TestRuntimeExecutor_SessionCorrelationFromResolver` | Request without explicit id resolves from the resolver into result/proof/completed + `execution.started` + `execution.evidence` | ✅ PASS |
| `TestRuntimeExecutor_ExplicitSessionIDWinsOverResolver` | Explicit request id beats a corrupt resolver | ✅ PASS |
| `TestRuntimeExecutor_NoSessionResolverLeavesEmpty` | Harness executes cleanly with empty correlation | ✅ PASS |
| `TestComposeWiresWorkspaceGuardAndAuditSessionCorrelation` | End-to-end: git-backed guard injects the real dirty file (excluding `.izen/`); NDJSON line carries the active session id | ✅ PASS |
| `TestComposeAuditStampsActiveSessionOnSwitch` | Two records after a switch carry old and new active ids | ✅ PASS |

### 6.5 UI CLI end-to-end — `ui/session_cmds_test.go`

| Test | Proves | Result |
| --- | --- | --- |
| `TestSessionListRendersBothSlots` | List renders both slots | ✅ PASS |
| `TestSessionInspectRendersStructuredMetadata` | Inspect JSON parses with id/slot/goal | ✅ PASS |
| `TestSessionRenameUpdatesTitle` | CLI rename persists the title | ✅ PASS |
| `TestSessionArchiveViaCLI` | CLI archive + `/new` refusal | ✅ PASS |
| `TestSessionCompactViaCLI` | CLI compact seals a generation (event count) | ✅ PASS |
| `TestSessionDeleteViaCLI` | CLI delete purges the slot, preserves project config, mirrors the surviving session | ✅ PASS |
| `TestSessionInvalidSlotRefusesCleanly` | Unknown slot args fail with usage errors, valid resume works | ✅ PASS |

---

## 7. Forbidden-Change Compliance

| Constraint | Compliance |
| --- | --- |
| Session Manager never executes mutations (INV-SESSION-09) | Only a read-only `WorkspaceGuard` seam added; the existing forbidden-import + forbidden-method sweeps still pass; the git guard lives at the composition root |
| Session deletion cannot delete project state (INV-SESSION-12) | `Delete()` purges only the slot directory; config / knowledge / audit byte-preserved (behaviorally asserted) |
| Failed switching leaves the current session active (INV-SESSION-11) | All pre-commit failures (persist, prepare, guard) return before the atomic pointer commit |
| Compaction is non-destructive (INV-SESSION-05/06) | Manual compact reads history, writes a new generation; raw history untouched |
| One interactive session active per workspace (INV-SESSION-02) | Active-slot delete moves the pointer to the sibling before removal — never a dangling/dual pointer |
| Context size controlled by policy, not history (INV-SESSION-13) | Context compiler untouched structurally; dirty files ride the session compact under the existing budget |
| Runtime execution correlated with session (INV-SESSION-10) | Audit envelope stamping + executor proof/evidence correlation |
| Existing assertions not weakened | Zero edits to prior session/execution/audit test assertions; new tests are additive |
| No unrelated packages altered | `git/engine.go` fix is the single out-of-scope-path change, required by and regression-locked for the guard |

---

## 8. Verification Results

```
1. go build ./...                                ✅ OK
2. go build ./cmd/izen                           ✅ OK
3. go vet ./...                                  ✅ clean
4. go test ./... -race -count=1                  ✅ exit=0 (entire repository)
5. golangci-lint run ./...                       ✅ 0 issues
6. GOOS=windows GOARCH=amd64 go build ./internal/session/... ./internal/events/...   ✅ clean
7. GOOS=plan9 GOARCH=amd64 go build ./internal/session/   ✅ clean (no-op fallback tier)
8. Phase 3 integration suites (-race, -count=1)  ✅ 28/28 new tests pass
```

Hygiene notes:

- The `internal/ui` Windows cross-build is blocked by a **pre-existing**
  Unix-only `syscall.Setpgid` in `internal/execution/runner.go` (committed as
  `0254e6a`, unrelated to this work); the session/events scope builds clean.
- The audit write path is `bufio`-buffered by design (never stalls the bus);
  `AuditLogger.Flush()` was added as the observability seam operators and tests
  read through.
- Guard injection persists the target BEFORE the pointer commit, so a crash
  between prepare and commit leaves the injected dirty set durable in the
  dormant slot — the recovery ladder (INV-SESSION-14) re-derives it.

## 9. Maintenance Contract

The Phase 3 locks intentionally freeze the current topology. When one fires:

1. Decide whether the change violates the session model (silent overwrite of
   archived/dirty state, dangling pointer after delete, session-free audit
   records, Windows no-op lock).
2. Legitimate evolution (e.g. adding a third slot, a new audit resolver
   strategy) requires updating the corresponding guard in the same commit, with
   justification — never weakening an assertion to make it pass.
3. Any new audit persistence surface must keep the invariant: **every NDJSON
   record is correlated with the session active when it crossed the bus.**
4. Any new session-switch path must keep the invariant: **uncommitted workspace
   state is injected into the target session's Context Compiler view, and the
   pointer commits atomically or the current session stays active.**