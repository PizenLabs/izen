# Evidence-Driven Verification Engine

> Transform `/review` from subjective commentary into a rigorous, evidence-backed
> verification pipeline with traceable provenance and zero workspace pollution.

## Philosophy

The review system in Izen is built on a simple principle:

**Opinions are cheap. Evidence is expensive.**

Instead of generating ungrounded "code review comments," Izen's review engine:

1. Parses changes with full provenance context (git diff + metadata)
2. Formulates risk hypotheses with explicit classification
3. Executes targeted verifications inside an **ephemeral test sandbox**
4. Records structured evidence entries in a C-R-H-V-E context ledger
5. Produces a traceable, evidence-backed conclusion

This aligns with Izen's core philosophy: **AI should strengthen human judgment,
not replace it.** Every risk finding is traced through a verifiable chain of
evidence so the developer can inspect, trust, or challenge each conclusion.

---

## Ledger Model: C-R-H-V-E

All state is captured in a sequential, ID-addressed graph ledger stored in
`ReviewLedger` (`internal/review/ledger.go`). Each record type has a monotonic
ID prefix plus a 3-digit sequence number:

| Prefix | Record | Purpose |
|--------|--------|---------|
| `C-xxx` | Change | File, diff snippet, actor from git metadata |
| `R-xxx` | Risk | Category (Deterministic/Behavioral/Structural/Environmental/Speculative) + code location |
| `H-xxx` | Hypothesis | What might go wrong + the expected invariant |
| `V-xxx` | Verification | How to verify (plan/strategy) |
| `E-xxx` | Evidence | Type, pass/fail status, confidence level, artifact reference |

### Linking

Records are linked through explicit foreign-key references:

```
C-001  →  R-001  →  H-001  →  V-001  →  E-001
                                    ↓
                                  E-002 (ephemeral sandbox)
```

- `HypothesisRecord.RiskID` → links to `RiskRecord.ID`
- `VerificationRecord.HypothesisID` → links to `HypothesisRecord.ID`
- `EvidenceRecord.VerificationID` → links to `VerificationRecord.ID`

### Thread Safety

The ledger is safe for concurrent access via `sync.Mutex`. All mutating methods
(`AddChange`, `AddRisk`, `AddHypothesis`, `AddVerification`, `AddEvidence`,
`SetStatus`) and reading methods (`FormatCompact`, `ActiveEvidenceIDs`) acquire
the lock.

### Review Status

The final status is computed from evidence outcomes:

- **`Verified`** — All evidence records passed; no unresolved items
- **`Conditional`** — One or more risks require manual runtime check (Behavioral/Environmental)
- **`Unresolved`** — Evidence failed, panicked, or no evidence was collected

---

## Risk Classification

The classifier (`internal/review/classifier.go`) maps each `RiskFinding` from
the existing AST/regex auditor into one of five categories:

### Deterministic → Ephemeral Sandbox Test

Patterns with zero false-positive rates that can be verified programmatically.

| Pattern | Rule IDs |
|---------|----------|
| Hardcoded secrets | `SEC-SECRET-001` |
| SQL injection | `SEC-SQL-001` |
| os/exec.Command | `SEC-CMD-001` |
| panic() calls | `GO-PANIC-001`, `GO-PANIC-002` |

`ShouldGenerateTest()` returns `true` — the engine writes ephemeral test files
into `/tmp/izen/review/<review_id>/` and runs `go test`.

### Behavioral → Manual Check Required

Runtime-dependent patterns that require integration or scenario testing.

| Pattern | Rule IDs |
|---------|----------|
| Goroutines without error handling | `GO-GOROUTINE-001` |
| Defer without error check | `GO-DEFER-001` |
| Lock without defer Unlock | `GO-LOCK-001` |
| Unsized reads | `SEC-READ-001` |
| log.Fatal / os.Exit | `GO-FATAL-001`, `GO-EXIT-001` |
| HTTP handlers | `SEC-HTTP-001` |
| Serialization | `SEC-SERIAL-001` |

### Structural → Static Analysis

Signature-level concerns verifiable without runtime.

| Pattern | Rule IDs |
|---------|----------|
| Exported func no return | `GO-FUNC-001` |

### Environmental → Manual Check

Sandbox-dependent side-effect concerns.

| Pattern | Rule IDs |
|---------|----------|
| Side-effect Execute/Run calls | `SEC-EXEC-001` |

### Speculative → Reported Only, No Test

Code quality markers and informational findings. Never generate tests.

| Pattern | Rule IDs |
|---------|----------|
| TODO/FIXME/HACK markers | `CQ-TODO-001` |
| fmt.Print[f|ln] statements | `CQ-PRINT-001` |
| Blank identifier `_ =` | `CQ-BLANK-001` |
| Informational severity | `info` |

---

## Ephemeral Sandbox

The sandbox (`internal/review/sandbox.go`) provides an isolated, disposable
workspace for running verification tests.

### Location

```
/tmp/izen/review/<review_id>/
```

The `review_id` is sanitized (alphanumeric, hyphens, underscores only) to
prevent path traversal. Each review gets its own directory.

### Lifecycle

1. **`NewSandbox(reviewID, projectRoot)`** — creates a sandbox handle
2. **`Create()`** — `os.MkdirAll` on the workspace path
3. **`WriteTestFile(name, content)`** — writes Go test files (supports nested dirs)
4. **`RunGoTestInProject(pkg)`** — runs `go test -v -count=1` in project root with 120s timeout
5. **`RunTest(testFile)`** — runs `go test` inside the sandbox workspace with 60s timeout
6. **`Cleanup()`** — `os.RemoveAll` the workspace; sets `created = false`

### Safety Entry Point

```go
func RunWithSandbox(reviewID, projectRoot string, fn SandboxRunFn) (EvidenceRecord, error)
```

This is the canonical way to use the sandbox:
1. Creates the sandbox
2. Executes the callback (your verification logic)
3. **Always** cleans up, even if the callback panics or returns an error
4. Returns a populated `EvidenceRecord`

### Contract: Zero Workspace Pollution

The sandbox **never** writes ephemeral test files into the user's source
repository. All generated artifacts live under `/tmp/izen/review/` and are
deleted immediately after evidence collection. The cleanup is unconditional:

```
RunWithSandbox → Create → Execute → Cleanup (always)
```

If cleanup fails, the error is returned but the sandbox `created` flag is still
reset. Tests verify that the directory is removed after both success and failure
paths.

---

## Provenance Rendering

The `ProvenanceRenderer` (`internal/review/provenance.go`) converts the ledger
into a boxed TUI display for `/review` and `$log` output:

```
┌─ Review Ledger ─────────────────────────────────────────┐
│ C-001  Change: cmd/api/main.go (removed signal stop)     │
│ R-001  Risk [Behavioral]: Repeated interrupt regression   │
│ H-001  Hypothesis: Second interrupt may no longer shutdown│
│ V-001  Plan: Signal lifecycle verification               │
│ E-001  Evidence [Existing Test]: Passed (Confidence: Ver.)│
│ E-002  Evidence [Ephemeral Sandbox]: Passed (Conf: High)  │
│ ───────────────────────────────────────────────────────── │
│ Review Status: Conditional (1 risk requires manual check) │
└───────────────────────────────────────────────────────────┘
```

Two rendering modes:
- **`Render()`** — Boxed display with auto-truncation to fit width (40-120 chars)
- **`RenderCompact()`** — Plain-text format for embedding in structured output

---

## State Machine Integration

The verification loop is wired as a new state in the review state machine
(`internal/modes/review/`):

```
      ┌─────────────────────────────────────────────┐
      │                                             │
      ▼                                             │
Collect → AnalyzeDiff → ImpactRadius → RiskAudit → Verify → Report → Done
                                                    │
                                                    ├─ Deterministic → Sandbox (go test) → E-xxx
                                                    ├─ Behavioral    → E-xxx [Manual Check Unresolved]
                                                    ├─ Structural    → E-xxx [Static Analysis Passed]
                                                    ├─ Environmental → E-xxx [Manual Check Unresolved]
                                                    └─ Speculative   → E-xxx [Skipped]
```

### Engine Changes (`engine.go`)

- **`stateVerify()`** — Creates `ReviewLedger`, iterates every `RiskFinding`,
  classifies via `ClassifyRisk`, links C→R→H→V→E records, runs
  `runDeterministicVerification` for deterministic risks, computes final status
- **`runDeterministicVerification()`** — Wraps `RunWithSandbox`, generates a
  risk-specific Go test file, runs `go test ./...`, returns evidence
- **`generateTestForRisk()`** — Template-based test generation for each
  deterministically-verifiable risk pattern (secrets, panics, SQL, exec)

### State Machine Changes

- `StateVerify` added after `StateRiskAudit` in `types.go`
- Transitions: `RiskAudit → Verify` (forward), `Verify → Report | ImpactRadius` (back/forward)
- All existing tests updated to include `StateVerify` in sequential paths

---

## Provenance in `$log`

The `/review` output now includes the full provenance chain. The `$log` command
can display review results from `.izen/audit/mutations.log` with structured
ledger entries. When a review is saved, the Compact ledger format is included
in the audit log entry so the developer can trace every finding back to its
evidence.

### LLM Context Budget

When the review passes context to an LLM, it sends **only** the relevant
evidence IDs (e.g., `C-001`, `R-001`, `E-002`) — not the full ledger text.
This keeps prompt context lean and prevents noise injection.

---

## File Layout

```
internal/review/
├── ledger.go         # ReviewLedger: C-R-H-V-E records, thread-safe mutations
├── classifier.go     # RiskCategory enum, ClassifyRisk(), ShouldGenerateTest()
├── sandbox.go        # Ephemeral sandbox at /tmp/izen/review/<id>/
├── provenance.go     # ProvenanceRenderer: boxed TUI display
└── review_test.go    # 28 tests covering all modules

internal/modes/review/
├── types.go          # StateVerify + ReviewLedger field on ReviewResult
├── state.go          # Verify transitions
├── engine.go         # stateVerify(), runDeterministicVerification(), generateTestForRisk()
├── diff.go           # (unchanged)
├── risk.go           # (unchanged)
├── impact.go         # (unchanged)
└── review_test.go    # Updated for StateVerify
```

---

## Contract Guarantees

| Guarantee | Enforcement |
|-----------|-------------|
| **No workspace pollution** | All ephemeral files in `/tmp/izen/review/`; `Cleanup()` always called |
| **Thread-safe ledger** | `sync.Mutex` on all reads and writes |
| **Deterministic only → tests** | `ShouldGenerateTest()` gates on `RiskDeterministic` |
| **Speculative never generates tests** | Classifier returns `RiskSpeculative` for code-quality/info findings |
| **Evidence always linked** | Foreign keys C→R→H→V→E enforced at API level |
| **Context-lean LLM injection** | Only evidence IDs passed, not full ledger text |
| **Cleanup on failure** | `RunWithSandbox` defers cleanup; tests verify post-failure |
| **Input sanitization** | `sanitizeID()` prevents path traversal in sandbox path |
