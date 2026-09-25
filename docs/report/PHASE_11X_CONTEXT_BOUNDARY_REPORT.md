# PHASE REPORT — SPEC-DRIVEN CONTEXT PIPELINE (Phase 11.x)

Status: COMPLETE · Branch `feature/session` · Verification: `go build ./...` ✅ · `go test -count=1 ./...` ✅ (192 packages)

## 1. Architecture changes

Three domains are now explicit and separated at one boundary:

```text
Conversation Domain   internal/session (unbounded, human-oriented)
        ↓  (explicit hand-off only)
Context Domain        internal/contextspec (compiled, bounded, revision-bound, UNTRUSTED)
        ↓
Execution Domain      execution.RuntimeExecutor (frozen contract, independent authorization)
```

New packages:

- `internal/contextspec` — the Context Domain: `ConversationState`, `ContextSpec`,
  `ContextCompiler`/`CompiledCandidate`, `Store` (CAS), `ExecutionSpec`, `Pipeline`,
  typed errors and an `AuditSink` port. It imports **no** execution, patch, autonomy,
  orchestrator or `os/exec` package.
- `internal/runtime/contextpipeline` — the composition-boundary adapter. It is the only
  package that reaches both the Context Domain and the execution authority: it maps the
  existing `execution.OCCVerifier` into the `SnapshotPort` and projects context audit
  events onto the shared `events.Bus`.

The canonical execution path is unchanged:

```text
UI ($prompt/$hot)
 → executeAutonomyWorkspace           (crosses the context boundary FIRST)
 → autonomy.Driver
 → ExecutorAdapter → RuntimeExecutor
 → admission / authorization / OCC
 → evidence / verification
```

## 2. ConversationState ownership

`internal/session.Session` remains the sole conversation owner. A new monotonic
`Revision uint64` clock is advanced exactly once per accepted user turn
(`AddMessage("user", …)` / `AcceptUserTurn`), never for assistant/system turns.
It is persisted in `session.json`, carried in the compact `context.json` generation and
bootstrapped deterministically (count of user turns) for legacy/recovered records. A
full `Purge` resets it. No semantic detector exists.

## 3. ContextSpec ownership

`internal/contextspec.Store` is the single Control-Plane owner of the committed
`ContextSpec`. It is a single-slot CAS store; the compiler never mutates it. A committed
spec records `ConversationRevision` (freshness clock) and a monotonic `SpecRevision`
(accepted-compilation counter). Items carry per-turn `Provenance`; superseded decisions
and constraints remain in the spec as lineage (`Active=false`).

## 4. Compiler boundary

```go
type ContextCompiler interface {
    Compile(ctx context.Context, input CompileInput) (CompiledCandidate, error)
}
type CompiledCandidate struct {
    BaseConversationRevision uint64
    Spec                     ContextSpec
}
```

`CompileInput` carries only the bounded conversation window, the objective and the
previous committed spec. The default `RuleCompiler` is a deterministic
semantic-state reducer (last-write-wins per decision topic, explicit reset phrases),
not a summarizer and not a dirty detector. A model-backed compiler can be injected
behind the same interface without changing the Control Plane. The compiler produces a
**candidate**; it cannot commit, mutate the session, or authorize.

## 5. Revision / CAS behavior

```text
same ConversationRevision     → CLEAN / fresh
different ConversationRevision → DIRTY / stale
```

`ContextSpec.IsFresh(rev)` is the only freshness test. At compilation the pipeline reads
`baseRevision`, compiles, then re-reads the live revision via `CurrentRevision`;
`Store.Commit` rejects the candidate with `ErrStaleContextCandidate` when
`baseRevision != liveRevision`. A stale candidate is discarded and can never overwrite
newer human state. Compilation is lazy: `EnsureFresh` returns the committed spec without
recompiling when it is fresh.

## 6. ExecutionSpec hand-off

`Pipeline.FreezeExecution` ensures freshness, resolves the target geometry, captures the
CURRENT workspace snapshot and returns a frozen `ExecutionSpec` (intent, active-only
semantic state, scope, revisions, target hashes, budget). It contains no `AuthorizedBy`
field and no grant: execution intent is not authorization. The existing Policy /
Admission / AuthorizationEngine path remains authoritative.

## 7. Workspace snapshot behavior

The snapshot port is the existing `execution.OCCVerifier` (`TreeDigest` plus a new
`TargetHashes` per-target view). No parallel hasher exists. At hand-off the pipeline
freezes hashes and validates them; a divergence returns the typed
`ErrStaleWorkspaceSnapshot`. At mutation time the executor continues to enforce its own
OCC baseline and Boundary-5 workspace digest, so a stale workspace cannot silently
execute.

## 8. Authorization preservation

The Context Domain cannot write, patch, shell, grant scope or reach the RuntimeExecutor.
`ExecutionSpec` does not self-authorize. Architecture tests (`TestG`/`TestG2`/`TestH`)
enforce the absence of authority fields and methods and the absence of execution imports.
`$hot` scope/`ScopeDeclared`/budget pre-approval semantics are untouched — the compiled
spec only supplies metadata (`Declared`), never a widened scope.

## 9. Streaming preservation

No streaming code was modified. `/ask` live response, `DirectResponse`, executor
`StreamCallback` and TUI rendering are unchanged; the context hand-off is a synchronous,
in-memory pre-execution step.

## 10. Tests added

`internal/contextspec/contextspec_test.go`:

- Test A — normal conversation: 3 user turns → revision 3.
- Test B — lazy compilation: 3 `/ask` turns + 1 hand-off → exactly 1 compilation.
- Test C — revision coherence: spec at 9 is stale at 10, recompiles to 10.
- Test D / D2 — semantic correction (contradictions collapse to `existing CSS`).
- Test E — CAS rejection: stale candidate discarded, state not overwritten.
- Test F — workspace divergence → `ErrStaleWorkspaceSnapshot`.
- Test I — raw conversation isolation: execution payload excludes unrelated chatter.
- Unresolved target → `ErrUnresolvedExecutionTarget`.
- Concurrency — 32 parallel commits with mixed base revisions; CAS races clean under `-race`.

`internal/architecture/context_boundary_test.go`:

- Test G / G2 — ContextSpec cannot execute (no execution imports/authority methods/fields).
- Test H — ExecutionSpec cannot self-authorize.
- Test J — `executeAutonomyWorkspace` crosses the context boundary BEFORE the canonical driver.
- Test J2 — composition root wires the pipeline over the OCC snapshot mechanism.

## 11. Full verification results

```text
go build ./...                 ✅
go test -count=1 ./...         ✅ 192 packages
go test -race ./internal/contextspec/ ✅
go vet (touched packages)      ✅
```

## 12. Files/packages changed

New: `internal/contextspec/{types,compiler,store,pipeline,audit,errors}.go`,
`internal/contextspec/contextspec_test.go`, `internal/runtime/contextpipeline/adapter.go`,
`internal/ui/context_boundary.go`, `internal/architecture/context_boundary_test.go`.

Modified: `internal/session/{session,context}.go` (revision clock),
`internal/execution/occ.go` (`TargetHashes`), `internal/runtime/compose/compose.go`
(wiring + accessor), `internal/ui/{model,program,autonomy_route,commands}.go`.

## 13. Remaining observations

- The default compiler is deterministic/offline; an LLM-backed `ContextCompiler` can be
  injected behind the existing interface when a semantic model pass is desired.
- `/spec` is read-only and lazily compiles when stale.
- The `ExecutionPayload` projection is the bounded execution-facing view; the frozen
  contract is currently consumed for hand-off validation, `/spec` and audit. The
  executor's own context strategy remains the authority for prompt construction.
- Provenance currently records the compile-time conversation revision plus source turn;
  finer per-turn revisions can be added without changing the boundary.

## 14. Architectural conflicts discovered

- No second executor/scheduler/mutation authority was introduced or found on this path.
- The pre-existing `internal/contextcompiler` is token-budget prompt compilation and is
  orthogonal to semantic `ContextSpec`; the two were deliberately not merged
  (One Question, One Owner).
- Workspace staleness at mutation time already has a canonical owner
  (`execution.OCCVerifier` + adapter Boundary-5). The pipeline reuses it rather than
  duplicating it.
