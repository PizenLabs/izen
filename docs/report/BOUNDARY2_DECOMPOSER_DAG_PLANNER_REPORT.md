# BOUNDARY 2 EXPANSION — SUB-TASK DECOMPOSER & DAG PLANNER REPORT

| Field            | Value |
| ---------------- | ----- |
| Status           | COMPLETE — ALL ACCEPTANCE CRITERIA GREEN |
| Version          | 1.0 |
| Date             | 2026-08-25 |
| Reference        | `docs/report/PHASE3_OCC_ENGINE_REPORT.md` (B5 digest authority consumed by the DAG runner); `internal/execution/boundaries.go` (the 5-Boundary model, I5); `docs/report/CODEBASE_SURVEY_REPORT.md` |
| Scope            | `internal/execution/planner/` *(new)*, `internal/runtime/autonomy/{driver,adapter,decomposition}.go`, `internal/autonomy/runtime_loop.go` |
| Test suite       | `internal/execution/planner/decomposer_test.go` (18 tests) · `internal/runtime/autonomy/decomposition_test.go` (7 tests) · `internal/runtime/autonomy/conformance_zero_trust_test.go` (Conformance A extended) |
| Verification     | `go build ./...` · `go test ./... -race -count=1` · `golangci-lint run ./...` |

> **Purpose:** expand Boundary 2 (Preflight Guard, invariant I5). When
> `EvaluatePreflight` refuses an objective as `preflight_infeasible`, the
> runtime no longer stops at a generic ask-human gate: a deterministic planner
> partitions the target into individually preflight-feasible sub-tasks staged
> as a validated `ExecutionDAG`, parks the loop at a typed
> `DECOMPOSITION_PROPOSAL` boundary listing every unit, and — on explicit
> human approval — executes them inside an ATOMIC TRANSACTION LOOP with
> Boundary-5 digest verification around every sub-task and rollback to the
> base tree on any failure.

---

## 0. Executive Summary

- **The infeasibility wall became a fork, not a dead end.** A full-file
  rewrite that trips I5 is decomposed into budget-fitted units; a target with
  no structural decomposition (or a plan that cannot fit any budget) falls
  back — unchanged and fail-closed — to the legacy explicit-re-scope park.
  User intent is never silently altered on either path.
- **The 0.7 rule is mechanical.** Every sub-task obeys
  `EstimatedTokens <= max_output × 7/10` (integer math), enforced twice: at
  `AddTask` and again across the whole plan in `Validate()`. Because estimates
  use the SAME accounting as Boundary 2 (`bytes/4 × FullRewriteTokenMultiplier`),
  every staged sub-task passes `EvaluatePreflight` individually BY CONSTRUCTION.
- **Structural splitting, not guessing.** Go/Rust/TS sources split at
  top-level declaration boundaries through a shared bracket/string/comment
  scanner (raw strings and comments can never fake a boundary); HTML splits at
  tag-depth top-level elements (script/style raw text respected, void elements
  never nest); Markdown at fence-aware headings; TOML/INI/YAML/JSON at their
  native section/key boundaries. An indivisible oversized section yields NO
  plan (`ErrNotDecomposable`) — never an oversized unit.
- **One human decision covers the whole plan.** The proposal boundary lists
  all sub-tasks (bounded prose + structured payload). Approval authorizes the
  entire DAG as one atomic transaction; per-unit approval gates are resolved
  under that consent, each still crossing B3 → B4 → B5 through the real
  executor.
- **Atomicity is digest-proven.** The Boundary-5 `WorkspaceTreeDigest` is
  captured before AND after every sub-task; the "after" digest of one unit is
  the expected "before" digest of the next, so any out-of-band writer is caught
  between steps. Any failure at B3/B4/B5 — or drift/cancellation anywhere —
  aborts the DAG, restores byte-exact base content, re-verifies the digest,
  and marks the plan `DAG_EXECUTION_FAILED`. Remaining sub-tasks NEVER execute.
- **Bounds stay honest.** The approved scope legitimately exceeds
  single-objective defaults, so `RuntimeLoop.WidenBounds` reserves headroom for
  exactly N consented executions — bounds are never lowered, and every
  sub-task still flows through Observe → Step → ConsumeExecution so attempts/
  steps/tokens/history remain authoritative.
- **25 new tests** (18 planner + 7 driver integration over the REAL
  `RuntimeExecutor`); Conformance A re-pinned for both I5 outcomes; the full
  repository passes under `-race`; lint reports 0 issues.

---

## 1. Deliverables

| File | Contents |
| --- | --- |
| `internal/execution/planner/planner.go` *(new)* | Package contract: `Decomposer` interface, `Section`/`Region` (inclusive 1-indexed line windows, `SliceLines` as the single region→bytes authority), `SplitKind` (`ast_structural` / `block`), fail-closed sentinels (`ErrNoDecomposer`, `ErrNotDecomposable`, `ErrInvalidDAG`, `ErrEmptySource`) |
| `internal/execution/planner/dag.go` *(new)* | `SubTask` (id/index/kind/target/bounded description/region/estimate/backwards deps) and `ExecutionDAG` (objective, target, `BaseTreeDigest`, `MaxOutputTokens`, topological task list, `PlanStatus` lifecycle incl. `PLAN_STAGED`, `DAG_EXECUTING`, `DAG_EXECUTION_COMPLETED`, `DAG_EXECUTION_FAILED`). Strict budget rule (`SubTaskBudget`, 7/10 integer factor), `AddTask` ceiling enforcement, `Validate()` invariants V1–V5, `ProposalSummary()` rendering with elision cap |
| `internal/execution/planner/ast.go` *(new)* | `ASTDecomposer` for `.go/.rs/.ts/.tsx/.mts/.cts`: column-zero declaration-prefix tables per language; shared streaming scanner (`scanState`) tracking bracket depth, string literals (incl. backtick raw strings), line/block comments; backward extension glues doc-comment/attribute runs forward while labels come from the RAW declaration line |
| `internal/execution/planner/block.go` *(new)* | `BlockDecomposer` for `.html/.htm/.xhtml`, `.md/.markdown/.mdx`, `.json/.yaml/.yml/.toml/.ini/.cfg/.conf/.properties/.env`: HTML tag-depth walk (wrapper tags don't nest, void/self-closing ignored, script/style raw text opaque, comments/doctype skipped); MD ATX headings outside ``` / ~~~ fences; TOML/INI `[section]` headers; YAML top-level keys + `---` separators; JSON root-object members via bracket-depth tracking |
| `internal/execution/planner/decompose.go` *(new)* | The engine: ordered registry lookup (`ForTarget`/`Decomposable`), token accounting identical to Boundary 2 (`EstimateTokens`, `EstimateRegionTokens`, `PreflightFeasible`), `Decompose()` orchestration, greedy grouping measured by MERGED-region estimate (floor-division superadditivity handled — summing per-section estimates would undershoot), `MaxSubTasks` cap, deterministic `st-N` identities and chained dependencies |
| `internal/execution/planner/decomposer_test.go` *(new)* | 18 tests incl. the mandated 20KB Go and 10KB HTML coverage |
| `internal/runtime/autonomy/decomposition.go` *(new)* | `DecomposeFunc` seam (+ injectable default wrapping `planner.Decompose`), `stageDecomposition()` parking logic, `ResumeApproveProposal` / `ResumeRejectProposal`, `runProposalDAG()` atomic transaction loop, `failDAG()` rollback convergence, bounded `subTaskPrompt` scoping, success classification |
| `internal/runtime/autonomy/driver.go` | `decompose`/`dag` fields, `WithDecompose` option (nil disables → legacy park), preflight interception hook in `observeAndRun` BEFORE the generic ask-human step, `Proposal()`/`Plan()` accessors |
| `internal/runtime/autonomy/adapter.go` | `ReadTargetFile` (planner input + rollback snapshots), `RestoreTargets` (deterministic-order rollback write authority), and hardened `bounded_patch` recovery: when the gateway cannot re-select a strategy, the adapter now synthesizes a minimal TargetedMutation profile whose artifact contract IS the search_replace patch — recovery can never silently fall back to the unbounded protocol and re-trip the very guard it escapes |
| `internal/autonomy/runtime_loop.go` | `HumanBoundaryDecomposition` action (`"decomposition_proposal"`), `HumanBoundary.Proposal *planner.ExecutionDAG` field, `DeriveBoundaryAction` precedence (patch > proposal > options > inform), `WidenBounds` (raise-only floors for human-approved scopes) |
| `internal/runtime/autonomy/conformance_zero_trust_test.go` | Conformance A re-pinned: decomposable target ⇒ typed proposal boundary with validated plan and zero requests; non-decomposable target ⇒ legacy `"preflight infeasible … re-scope"` inform boundary, zero requests |

Test files:

| File | Tests |
| --- | --- |
| `internal/execution/planner/decomposer_test.go` | 20KB Go decomposition (coverage, chaining, preamble, individual preflight pass); indivisible giant-function fail-closed; 10KB HTML decomposition; script raw-text opacity; Rust/TS declaration tables; brace-in-string/comment immunity; MD fences; YAML/TOML/JSON dialects; `SubTaskBudget` floor semantics; `AddTask` ceiling refusal (boundary estimate accepted); `Validate` rejection matrix; registry coverage; sentinel `errors.Is` matrix; `SliceLines` round-trip |
| `internal/runtime/autonomy/decomposition_test.go` | Full-loop atomic happy path (zero calls before consent, one call per unit, completion status, chained digests, `DAG_EXECUTING` history); B4 artifact failure mid-plan (abort after exactly 2 calls, byte-exact rollback, digest == base, failing unit named); B5 out-of-band writer mid-plan (OCC refuses the apply, rollback erases BOTH the landed patch and the foreign tail); rejection without execution; resume guards; disabled-decomposition legacy park; success/failure outcome classification; prompt boundedness/scoping |
| `conformance_zero_trust_test.go` | Conformance A both branches (above) |

---

## 2. Design

### 2.1 Decomposition Pipeline

```
EvaluatePreflight ──infeasible──▶ stageDecomposition
                                    │ target readable? decomposable? max_output known?
                                    ▼
                              planner.Decompose(objective, target, source, baseDigest, maxOut)
                                    │ ForTarget → AST | Block splitter
                                    │ Split → contiguous labeled sections
                                    │ groupSections: merged-region estimate ≤ ⌊0.7·max⌋
                                    ▼
                              ExecutionDAG{BaseTreeDigest, tasks st-1..st-n}   ──invalid──▶ nil (legacy park)
                                    │ Validate ✓
                                    ▼
                       loop.AwaitHuman(HumanBoundary{Proposal, Reason=summary})
                                    │ human approves
                                    ▼
                       ResumeApproveProposal → runProposalDAG (atomic)
```

Grouping greedily absorbs the next adjacent section only while the WHOLE
merged window stays within the ceiling. Estimates are recomputed over the
candidate merged region rather than summed: `floor(x/4)` is superadditive, so
merged regions can cost strictly more than their parts — measuring directly is
what keeps the guarantee honest (regression-tested by the 20KB fixture).

### 2.2 The Atomic Transaction Loop

Per sub-task, in topological order:

1. **B5 BEFORE** — `WorkspaceVersion(targets)` must equal the rolling
   expectation (initially `BaseTreeDigest`, afterwards the previous apply's
   post-digest). Divergence = out-of-band writer between steps ⇒ abort.
2. **Execute** — scoped request (`runRequest-st-N`, windowed prompt, forced
   `bounded_patch` protocol, digest carried for the adapter's own pre-submit
   drift check) driven through the loop state machine so bounds/history stay
   truthful.
3. **Resolve the gate** — a `pending_approval` observation is auto-approved:
   the human authorized THIS plan; the executor still runs its OCC commit gate
   against the baseline captured moments earlier.
4. **Classify** — only `changed/created/nochange/completed` count as applied;
   ANY other terminal outcome (truncated/refused/schema-violated/occ-aborted/
   apply-failed/cancelled/skipped) trips `failDAG`.
5. **B5 AFTER** — capture the post-apply digest; it becomes the next unit's
   expectation. A no-change apply keeps the same digest — still consistent.

On failure: restore every plan target to its snapshot bytes, RE-VERIFY the
live digest equals `BaseTreeDigest` (mismatch appended to evidence), set
`Status=DagExecutionFailed` + bounded reason naming the unit, converge the
loop to a permanent abort whose termination reason carries
`DAG_EXECUTION_FAILED`. Rejection parks resolve symmetrically without any
execution.

### 2.3 Budget & Feasibility Guarantees

| Invariant | Mechanism |
| --- | --- |
| `SubTask.EstimatedTokens <= 0.7 × max_output` | `AddTask` refuses violations; `Validate` re-checks every task; tests pin the exact floor (e.g. 3072 → 2150) |
| Every sub-task passes B2 individually | Identical accounting (`bytes/4 × multiplier`) + strict-inside-the-ceiling margin; asserted per unit in `requireValidDAG` |
| Whole-file infeasibility precondition | Each fixture proves `EvaluatePreflight(whole)` is infeasible BEFORE decomposing |
| No unbounded plans | Indivisible oversized section ⇒ `ErrNotDecomposable`; >64 units ⇒ refused; zero/unknown budget ⇒ refused |

### 2.4 Bounds Integrity

`WidenBounds(minAttempts, minSteps, minTokens, minIdentical)` raises ONLY the
floors needed by the consented scope (`n+1` attempts/steps, estimated total
plus slack tokens, `n+2` identical-decision tolerance — sequential `continue`
decisions are legitimate here). Single-objective defaults are untouched for
every other path; nothing can lower a bound.

### 2.5 UI Degradation

Unknown boundary actions render through the existing inform path: the
`DECOMPOSITION_PROPOSAL` summary (all sub-tasks, budgets, base digest) is
visible verbatim, generic Escape/Ctrl+C abort paths work, and no key handler
panics on the new action value. A dedicated proposal card remains follow-up
surface polish, not a correctness dependency.

---

## 3. Test Suite

### Planner units (18)

| Area | Proves |
| --- | --- |
| `TestDecompose_GoSource20KB` | Mandated 20KB Go fixture: whole file infeasible at B2; ≥2 structural units; preamble present; last unit names its declaration; dependency chain strictly backwards; full line coverage; EVERY unit ≤ ceiling AND individually preflight-feasible |
| `TestDecompose_HTML10KB` | Mandated 10KB HTML fixture: block strategy; wrapper lines attach forward; tag identities label every group; coverage + per-unit preflight pass |
| `TestDecompose_GoIndivisibleFunctionFailsClosed` | One giant function ⇒ `ErrNotDecomposable`, nil DAG |
| Scanners | Backticks/comments never fake boundaries; closing tags parse correctly (regression: leading-slash tagName bug); script/style raw text opaque; TS decorators/Rust attributes glue forward |
| Formats | MD fence-aware headings; YAML documents/separators; TOML sections; JSON root members |
| Validation | Duplicate id / forward dependency / over-ceiling / coverage gap / empty / nil all rejected; boundary estimate (= ceiling) accepted |
| Registry | AST vs Block routing for 17 extensions; plain-text/python/ruby not decomposable |
| Sentinels | `errors.Is` identity for all three fail-closed paths |

### Driver integration (7, real RuntimeExecutor + scripted provider)

| Test | Proves |
| --- | --- |
| `…ApprovalExecutesAllSubTasksAtomically` | 60-handler fixture stages 6 units; zero provider calls before consent; approve ⇒ completed with EXACTLY n calls; workspace mutated; final digest consistent; n `DAG_EXECUTING` transitions recorded |
| `…ArtifactFailureRollsBackToBaseDigest` | Poisoned unit 2 (B4) ⇒ aborted with `DAG_EXECUTION_FAILED` naming `st-2`; exactly 2 calls ever; file byte-identical to original; live digest == `BaseTreeDigest` |
| `…WorkspaceDriftAbortsAndRollsBack` | Foreign writer fires during unit 2 ⇒ executor OCC gate refuses the apply; rollback removes BOTH the landed unit-1 patch and the foreign bytes; digest restored |
| `…RejectTerminatesWithoutExecution` | Reject ⇒ permanent abort, zero calls, untouched workspace |
| `…ResumeRequiresParkedProposal` / `…DisabledKeepsLegacyReScopePark` | Guard rails: resume needs the parked gate; `WithDecompose(nil)` reproduces the pre-expansion behavior exactly (zero calls) |
| Classification/prompt units | Closed success vocabulary vs everything-aborts; prompt carries id/position/window/SEARCH instruction within a hard size bound |

### Conformance A (re-pinned)

Zero provider requests on preflight refusal — now asserted on BOTH branches:
typed `decomposition_proposal` boundary carrying a `PLAN_STAGED` validated DAG
for decomposable targets, and the unchanged explicit re-scope demand for
non-decomposable ones.

---

## 4. Verification Results

```
1. go build ./...                                ✅ OK
2. go build ./cmd/izen                           ✅ OK
3. go vet ./...                                  ✅ clean
4. go test ./...                                 ✅ exit=0 (127 packages)
5. go test ./... -race                           ✅ exit=0
6. golangci-lint run ./...                       ✅ 0 issues
```

Hygiene notes:

- The planner package is PURE: no filesystem, provider or clock access —
  fully deterministic and trivially testable.
- Integration fixtures anchor patches on the LIVE first line of each requested
  window, so applies succeed regardless of how the planner groups sections.
- One regression was caught and locked during development: closing HTML tags
  previously produced empty tag names (leading `/` treated as a terminator),
  leaking depth monotonically — now unit-covered via the raw-text opacity test.

## 5. Maintenance Contract

1. Adding a language/format means adding ONE prefix table or boundary
   predicate plus registry entries — never special cases in the engine.
2. The 0.7 factor is a load-bearing constant (`SubTaskBudgetFactor*`): changing
   it changes the safety envelope against B2 truncation and requires updating
   `TestSubTaskBudget_IsSeventyPercent` and the conformance fixtures together.
3. Any new terminal execution outcome must be classified explicitly in
   `dagOutcomeSuccess` — the default is ABORT (atomicity fails closed).
4. Rollback correctness depends on `ReadTargetFile` snapshots taken before the
   FIRST mutation; do not "optimize" them per-sub-task.
5. If the executor grows a native multi-patch transaction, the runner's
   auto-approve of consented units should migrate to it — the digest protocol
   stays mandatory either way.
