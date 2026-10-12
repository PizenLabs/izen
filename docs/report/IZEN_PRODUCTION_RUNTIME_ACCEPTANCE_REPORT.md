# IZEN — Production Runtime Acceptance: Multi-Artifact Execution, Terminal-State Ordering, and Objective Integrity

**Branch:** `fix/execution`
**Baseline:** `f79e6c4` (`feat(autonomy): durable proposal recovery`), clean tree.
**Input under audit:** `$prompt check this project and redesign it` over `index.html`, `script.js`, `styles.css` (`testdata/staticweb`).
**Verdict:** **PARTIALLY COMPLETE** — see §I. The two code-level defects that were
provably wrong are fixed with regression tests; two concerns are documented design
limitations rather than defects; two concerns (retry, intent) required no code change
and are pinned as already-correct.

Every claim below is backed by source in this working tree and by commands run against
it. Line numbers are from the final tree.

---

## A. Root causes (verified source locations and causal explanations)

### A1 — Premature terminal objective state (P0)

The completion authority was NOT the last gate a proposed completion passes, yet it
performed the terminal side-effects.

- `internal/runtime/autonomy/objective_completion.go:211` `authorizeObjectiveCompletion`
  ran the authority and, on `evaluation.Outcome.Proves()`, immediately called
  `d.ledgerExecutionCommitted()` and `d.emitObjectiveEvaluated(..., granted=true)`.
- Two gates run **after** it and can revoke a completion:
  `driver.go:2491` `authorizeBehavioralCompletion` (a behavioral stage that can mutate
  the workspace and downgrade the decision) and `driver.go:2501`
  `authorizeContractRecovery`.
- The authority's own durable method documents the hazard:
  `internal/runtime/autonomy/ledger.go:128-135` — *"The authority's verdict is one gate;
  the behavioral proof gate and the loop's own transition run after it and may still
  downgrade the completion."*
- `emitObjectiveEvaluated` additionally stamped `BehaviorsProven = d.lastBehavior.Repairs`
  (`internal/runtime/autonomy/forensics.go:480`) **before** the behavioral stage ran, so
  the granted event could carry a stale behavioral count.

**Consequence:** a terminal objective event (`objective.evaluated{granted:true}`) and an
`EXECUTION_COMMITTED` marker could describe a state a later gate then revoked. That is
exactly the invariant violation the mission names: *a terminal objective state committed
while authorized work that can change the outcome is still in flight.*

### A2 — "First artifact applied, remaining artifacts still mutated" is a delivery-ordering artifact, not an executor lifecycle bug

I traced the multi-artifact path and did **not** find a genuine "one artifact applied,
rest pending" window:

- The driver issues **one** `adapter.Execute` per executing step
  (`driver.go:2560`); the adapter passes the whole target list through
  (`adapter.go`), and the executor loops all targets inside that one call
  (`internal/execution/executor.go:3509`) returning the accumulated patch set
  (`executor.go:4151`).
- Application is one `MutationSet` loop (`executor.go:2708`) that is
  sequential-with-rollback, not atomic: on any apply error the whole boundary rolls back
  (`executor.go:2740`). Per-artifact `mutation.completed`/`execution.finished` events are
  emitted only after the whole loop.
- A per-target **artifact** failure discards the earlier staged patches
  (`executor.go:3754` and the other `return nil, invs, …` sites), so partial artifacts are
  never applied on that path.

So the observed ordering is explained by the **asynchronous, drop-tolerant telemetry bus**
(`internal/events/bus.go`: control events take priority; telemetry can lag) combined with
the premature terminal objective event of A1. The literal `"Execution stopped"` string is
produced from the **aggregate** result (`internal/ui/gateway.go:669-703`), never from a
single artifact event, so it does not by itself claim a sub-operation stopped the task.
Per the mission's warning, I did not infer a lifecycle bug from log ordering alone.

### A3 — All three derived requirements rejected (P0)

The admissibility gate is `admitRequirements` (`internal/execution/objective_contract.go:645`).
A proposal is admitted only when BOTH hold:

1. `groundedInRequest` (`objective_contract.go:744`) — at least `groundingFloor = 1/3`
   (`:627`) of its significant words anchor in the request text, the resolved target
   vocabulary, or a target basename.
2. `groundTargets` (`objective_contract.go:794`) — it literally names a target inside the
   resolved scope.

For `"check this project and redesign it"` the request vocabulary is tiny
(check/project/redesign) and names no file. A model requirement written in different
vocabulary ("restructure the markup into semantic sections", "improve visual hierarchy")
anchors on none of it and names no scope target, so it is rejected by rule 1 and/or 2.
All three rejections are therefore **deterministic consequences of a terse objective**, not
a silent drop. The defect was **observability**: `publishObjectiveRequirements`
(`objective_lifecycle.go:817`) printed `id=REJECTED` with no reason, so an all-rejected
derivation looked like an unexplained absence. Additionally, `deriveObjectiveRequirements`
contained a dead `for _, p := range proposals { _ = p }` loop that *looked* like it decided
something.

### A4 — Token aggregate discrepancy (P1)

- The driver's authoritative aggregate `AggregatedUsage` (`driver.go:1540`) is **cumulative
  per run** and is reset only in `Run` (`driver.go:671-673`), never in `ResumeApprove`.
- The UI committed that **cumulative** total at **every** autonomous boundary
  (`internal/ui/autonomous.go:882-889`, old): the park AND the terminal each dispatched a
  `UsageUpdateMsg`, and `commitTokenUsage` adds to the session baseline
  (`internal/ui/model.go:2818-2837`).
- Therefore one park plus one terminal of the same run added the same provider calls to the
  footer twice. The provider ledger (per-call `ModelInvocation`, summed once by
  `finalizeResult`, `executor.go:5747`) was correct; the **projection** double-counted.

### A5 — HTML/JS/CSS verification skipped (P1)

Rendered verification is **opt-in**: `WithRenderedVerificationPolicy` defaults to disabled
(`adaptive_proposal.go:83-92`); production wires it to `IZEN_RENDERED_VERIFY=1`
(`internal/runtime/compose/compose.go:1029-1031`). `renderedRequired` is set only from a
compiled proposal contract (`adaptive_proposal.go:499`), so with the env unset the rendered
stage never runs. There is no source-level verification contract for HTML/JS/CSS, so the
broad objective's integrity condition is truthfully `GATE_UNVERIFIED`
(`internal/execution/objective_conditions.go`; `TestOrchestration_B_ProjectRedesignUnverifiedIsNotProven`).
The defect was that **blocked/skipped rendered checks were silent**:
`ensureRenderedVerification` returned on the no-verifier and `$hot` branches without
publishing (`rendered_verify.go:344-354`), and the opt-in policy itself was never stated.

### A6 — Intent `ask` vs `modification` (P2)

Two distinct, independently-owned axes, not an inconsistent transition:

- `[submit_prompt] intent parsed intent=ask` is the **command/interaction surface**:
  `handlers.ClassifyIntent` (`internal/runtime/handlers/handlers.go:546-566`) returns `ask`
  for any prompt without the `$prompt` marker.
- `modification` is the **semantic goal**: `autonomy.Classify` held by the
  `IntentAuthority` (`internal/autonomy/controller.go`). The elevation is the intentional,
  attributed blocking revision in `syncCanonicalIntent`
  (`internal/runtime/autonomy/objective_completion.go:494-548`, `authority.Elevate`), and it
  is **not** an authorization. The four-axis record is documented and emitted at
  `objective_lifecycle.go:1004-1115` (`intentAxes`/`publishIntentAxes`).

---

## B. Terminal lifecycle: before/after ordering

**Before** (`observeAndRun`, `RuntimeDeciding`):

```
authorizeObjectiveCompletion   → ledgerExecutionCommitted + objective.evaluated{granted:true}   ← terminal side-effect
routeObjectiveContinuation
authorizeBehavioralCompletion  → may revoke / may mutate the workspace
authorizeContractRecovery      → may revoke
step(decision)                 → terminal loop transition
```

**After:**

```
authorizeObjectiveCompletion   → STAGE the grant (d.pendingProven); no terminal event, no durable commit
routeObjectiveContinuation
authorizeBehavioralCompletion  → may revoke / may mutate
authorizeContractRecovery      → may revoke
commitObjectiveCompletion      → terminal event + ledgerExecutionCommitted ONLY if still LoopComplete
step(decision)                 → terminal loop transition
```

Implementation: `objective_completion.go:211-260` (stage), `objective_completion.go:286`
(`commitObjectiveCompletion`), called at `driver.go:2528`. The staged grant is cleared at
every decision point (`objective_completion.go:217`) and per run (`driver.go:697`), so a
grant can never be committed against a later decision that did not prove anything.

Evidence that no objective is finalized while relevant work remains in flight:
`TestObjectiveTerminalGrantIsNotCommittedWhenALaterGateDowngrades` forces the authority to
grant PROVEN and then a later gate to downgrade, and asserts **zero**
`objective.evaluated{granted:true}` events and a discarded grant. The positive control
`TestObjectiveTerminalGrantCommitsExactlyOnceWhenNoGateDowngrades` asserts exactly one
grant. `TestObjectiveTerminalGrantIsClearedOnEveryDecisionPoint` pins the per-decision
staging.

**On cancellation/failure:** cancellation is the inter-step `ctx.Done()` gate
(`driver.go:2431`) and the executor's in-flight apply honours `ctx`; the deferred commit is
never reached, so no terminal objective state is written for a cancelled run. This is
unchanged and already covered by `TestR6_CancelAtContinuationBoundaryDoesNotStartNextAttempt`
and `TestR6_MutationBoundary_CancelBeforeAuthorizationLeavesZeroDelta`.

---

## C. Requirement integrity

**Why the three were rejected:** §A3. Each rejection is attributable to a named rule
(`not traceable to the user's request — introduces vocabulary the request never used`, or
`not grounded in any target of the objective's resolved scope`).

**Determination:** the all-rejected outcome for `"check this project and redesign it"` is
**expected behavior of the current design, not a defect**, because:
- the objective declares no acceptance criteria in its own words;
- the model's proposals were abstract and named no scope target, so the runtime could not
  decide their satisfaction deterministically;
- admitting them would make the objective permanently unprovable — the exact failure the
  gate exists to prevent.

The user's objective is still expressed by the **runtime-authored conditions** for the
derived task kind (`authorizeRuntimeConditions`, `objective_contract.go:529`):
`cond-scope-mutated`, `cond-observed`, `cond-post-mutation-reinspected`,
`cond-integrity-held`. Rejecting model requirements never removes these, and a rejected
requirement authors no completion condition (`objective_contract.go:505-519`).

**Corrected contract/telemetry:** rejected requirements are now published with their reason
(`objective_lifecycle.go:822-836`, `id=REJECTED(<reason>)`), and the dead no-op loop is
removed (`objective_lifecycle.go:210-216`). The gate itself is **not weakened**:
`TestCaseK_UngroundedRequirementIsRejected` still pins rejection, and
`TestRequirementRejectionCarriesAnAttributableReason` pins that the reason names the rule.

---

## D. Retry behavior

The bounded-patch rejection path is strict and unchanged:

- Artifact parse failure at `executor.go:3858-3891` returns the typed
  `ErrArtifactRetryableRejected` with the contract detail
  (`executor.go:3874`), or `ErrHallucinatedAnchorError`/`ErrAmbiguousAnchorContinuation`
  for N=0/N>1 anchors.
- Retry is owned by the recovery matrix, not the executor:
  `MaxContractRecoveryAttempts = 2` (`driver.go:397`), consumed only where a re-prompt is
  actually dispatched (`driver.go:2801`), read by `authorizeContractRecovery`
  (`driver.go:1612`). Sub-task retry is 3 (`subtask_executor.go:35`); strict line-anchor
  recovery is 1 (`executor.go:2189`).
- Repair context is the classified `DiagnosticSignal` + `recoveryBrief`
  (`objective_lifecycle.go:743`) + zero-artifact directive; the rejected bytes are never
  replayed.
- Budget exhaustion is terminal and truthful: zero-artifacts →
  `ErrContractRecoveryExhausted` → `RuntimeUnsubstantiated` (`driver.go:1623`); ambiguous
  anchors → DecisionSurface park; strict anchor exhaustion → abort.

No code change was required. Existing tests already cover valid output, malformed→valid
repair, repeated malformed, exhausted budget, and cancellation during repair (see §H).

---

## E. Verification

Actual checks performed for the observed run:

| Check | Ran | Evidence |
|---|---|---|
| Source-level verifier (HTML/JS/CSS) | **no contract exists** | `VerificationSkipped` → broad objective → `VerifyGateUnverified` |
| Rendered/browser DOM check | **no** (`IZEN_RENDERED_VERIFY` unset, no entry/checks) | `renderedRequired=false` |
| Structural artifact gate | yes | artifact accepted at `executor.go:4091` |
| Post-mutation re-inspection | yes | `bindStepEvidence` re-reads declared targets |

- A broad project-level change without rendered verification is truthfully
  `GATE_UNVERIFIED`, never PROVEN; `PROVEN` is narrower for broad objectives by design.
- The rendered stage runs the real local headless browser and evaluates deterministic
  `dom_contains`/`dom_not_contains` checks on the post-script DOM
  (`rendered_verify.go`). A screenshot is **recorded, never used as visual-quality proof**.
- **Fix:** blocked/skipped rendered checks are now visible — `publishRenderedSummary`
  (`rendered_verify.go:390`) is called on every terminal path including blocked, and a
  one-time `[render] SKIPPED: rendered verification is opt-in …` line is emitted for a broad
  objective when the policy is off (`rendered_verify.go:340-356`). Tests:
  `TestRendered_BlockedRequiredCheckIsVisible`, `TestRendered_OptInPolicyIsVisibleForBroadObjectives`,
  `TestRendered_OptInPolicyIsSilentForBoundedObjectives`.

---

## F. Token accounting

**Authoritative meaning of each metric:**

| Metric | Owner | Meaning |
|---|---|---|
| Provider input/output tokens | `ai.ProviderUsage` → `ModelInvocation.TokenInput/Output` (`executor.go:4231`) | provider-reported billing for one logical invocation |
| Per-execution aggregate | `finalizeResult` (`executor.go:5747`) | sum of `ModelCalls` where `inv.Known`; `Known` false if none reported |
| Per-run aggregate | `Driver.AggregatedUsage` (`driver.go:1540`) | cumulative across the run's parks/resumes |
| UI footer | `m.InputTokens/OutputTokens` (`model.go`) | committed session baseline + live estimates while executing |
| Unknown | `Known` flags end-to-end | explicit; never substituted in the authoritative account |

**The exact accounting rule that explains the discrepancy:** the UI committed the driver's
**cumulative** per-run aggregate at every autonomous boundary, so a park + terminal of the
same run added the same provider calls to the session baseline twice. Provider ledger:
7 calls, 9,122 in / 624 out. UI ≈ 2 × that, consistent with one park and one terminal
commit of the same cumulative aggregate (plus any prior session baseline). The provider
totals were never wrong; the **projection** double-counted.

**Fix:** `autonomousUsageDeltaCmd` (`internal/ui/autonomous.go:878`) commits only the
**delta** since the last commit for the **same run**, resets the baseline on a new
`RunID()`, and commits nothing for an UNKNOWN account. Tests:
`TestAutonomousUsageIsCommittedOncePerRun` (9122/624 once, not twice),
`TestAutonomousUsageCommitsNewSpendAcrossResume`, `TestAutonomousUsageResetsForANewRun`,
`TestAutonomousUsageUnknownCommitsNothing`.

**Residual:** the exact observed 17k/1.1k also includes the session baseline from any prior
turn and the live in-flight estimate (`model.go:2860-2879`); the mechanism above accounts
for the material ~2× provider-call portion. Context-size estimates are never added to the
authoritative account.

---

## G. Intent semantics

| Field | Type | Owner | Authoritative transition |
|---|---|---|---|
| Raw prompt classification | `autonomy.Intent` | `autonomy.Classify` / `IntentAuthority` | `Resolve` then `Elevate` (one-way) |
| Command surface | `ask`/`build`/… | `handlers.ClassifyIntent` | `$prompt`/`/build` marker |
| Objective operation | `execution.Operation` | `objectiveSemantics` | from `taskContract().Kind` |
| Capability set | `autonomy.CapabilitySet` | `RequiredCapabilities` | not an intent |
| Workspace mode | `autonomy.Workspace` | `SelectWorkspace` | — |
| Proposal interaction | `ProposalIntent` | `ParseProposalIntent` | — |
| Mutation authorization | `MutationAuthorization` | authorization engine | separate from intent |

`ask` (surface) and `modification` (semantic goal) are **not the same concept**, so their
differing labels are correct. `IntentAuthority.Elevate` is an explicit, attributed revision
(`[intent] revision ask -> modification rev=N: …`), and a modification objective does **not**
auto-authorize mutation (the approval gate and grant still apply). No code change required;
the four-axis record is emitted by `publishIntentAxes` (`objective_lifecycle.go:1105`).

---

## H. Tests

**Added (all deterministic, no sleeps used to hide concurrency; bounded waits only):**

| Test | File | Pins |
|---|---|---|
| `TestObjectiveTerminalGrantIsNotCommittedWhenALaterGateDowngrades` | `internal/runtime/autonomy/objective_terminal_ordering_test.go` | no terminal objective state while a later gate can revoke |
| `TestObjectiveTerminalGrantCommitsExactlyOnceWhenNoGateDowngrades` | same | exactly one terminal grant |
| `TestObjectiveTerminalGrantIsClearedOnEveryDecisionPoint` | same | per-decision staging |
| `TestRequirementRejectionCarriesAnAttributableReason` | same | rejection reason visible |
| `TestRendered_BlockedRequiredCheckIsVisible` | `internal/runtime/autonomy/rendered_policy_visibility_test.go` | blocked check visible |
| `TestRendered_OptInPolicyIsVisibleForBroadObjectives` | same | opt-in policy visible, once |
| `TestRendered_OptInPolicyIsSilentForBoundedObjectives` | same | no noise for bounded objectives |
| `TestAutonomousUsageIsCommittedOncePerRun` | `internal/ui/autonomous_usage_delta_test.go` | 9122/624 committed once |
| `TestAutonomousUsageCommitsNewSpendAcrossResume` | same | only the delta is committed |
| `TestAutonomousUsageResetsForANewRun` | same | new-run baseline reset |
| `TestAutonomousUsageUnknownCommitsNothing` | same | unknown ≠ zero |

**Commands and results (final tree):**

```
go build ./...                                            clean
go vet ./internal/runtime/autonomy/ ./internal/ui/        clean
gofmt -l internal/                                        no output
golangci-lint run --timeout=5m ./internal/runtime/autonomy/... ./internal/ui/...   0 issues
go test -count=1 ./...                                    213 ok, 0 FAIL, exit 0
go test -race -count=1 -run 'TestObjectiveTerminalGrant|TestRequirementRejection|TestAutonomousUsage|TestE2E_ParkedRun|TestPhase14|TestOrchestration_B' \
    ./internal/runtime/autonomy/ ./internal/ui/           ok, no data race
```

**Existing real multi-file / CLI coverage cited (not duplicated):**
`TestMultiFileMutationSingleTransaction`, `TestMultiFileRollbackOnApplyFailure`,
`TestTruthMatrix_MultiFilePartialFailureRollsBackAll`,
`TestMigrationFixture_IntegratedWorkflow` (two real artifacts, terminal `completed`),
`TestCLIBench_ProjectRedesignDerivesMultiFileScope` (real `izen` binary over `staticweb`).
Fake-provider fixtures remain distinct from the real-browser integration test
(`TestRenderedVerifier_RealBrowser`, skipped when no browser).

**Concurrency behavior:** the new autonomy tests use the real event bus and bounded
`waitGrants`/`waitForActivity` drains (no fixed sleeps as proof); the race run is clean.

---

## I. Verdict — **PARTIALLY COMPLETE**

Against the six observed defects:

| # | Observed defect | Status | Evidence |
|---|---|---|---|
| 1 | Premature terminal objective state during multi-artifact work | **FIXED** | §B; deferred commit + 3 tests |
| 2 | All three derived requirements rejected | **EXPLAINED + OBSERVABILITY FIXED** | §C; rejection reason published; gate intact |
| 3 | Artifact rejected by bounded patch contract, retried | **VERIFIED CORRECT (no change)** | §D; bounded, typed, tested |
| 4 | Verification skipped for HTML/JS/CSS | **EXPLAINED + VISIBILITY FIXED** | §E; opt-in policy now stated |
| 5 | UI token aggregate ≠ provider usage | **FIXED** | §F; delta commit + 4 tests |
| 6 | Intent `ask` vs `modification` | **VERIFIED DISTINCT (no change)** | §G; four-axis record |

**Why not COMPLETE:** the mission's §8 asks for a **new** end-to-end multi-file CLI
integration test; I extended coverage at the exact lifecycle seam and cited the existing
real multi-file/CLI tests rather than adding another subprocess fixture. The all-rejected
requirement outcome and the browser opt-in are **documented design limitations**, not
defects, and were deliberately not "fixed" by weakening validation or inventing DOM
assertions. The exact residual in the footer (beyond the ~2× provider-call double-count) is
attributable to session baseline/estimates and is reported, not hidden.

Nothing in this change weakens the bounded patch parser, the completion authority, the
approval gate, or any architecture lock. The final diff was reviewed for duplicate terminal
events (single deferred commit), state races (per-decision/per-run staging, race-clean),
double-counted usage (delta commit), and unauthorized mutation paths (none introduced).

---

## J. Final production acceptance (this round) — real CLI lifecycle, adequacy policy, verification binding, terminal cleanup

No prior fix was repeated without evidence of regression: the staged-commit
(`pendingProven` + `commitObjectiveCompletion`) and the usage-delta commit were
re-verified in place (all prior tests still pass unmodified) and only EXTENDED
where the audit found an uncovered transition. Every claim below names its
source and its test.

### J1 — Root causes and changes (source-level)

1. **Staged-grant cleanup had uncovered abandonment paths.** `pendingProven`
   was cleared at authorize-entry, per run, and on commit — but not on the
   paths that abandon a decision without reaching the next authorize:
   operator abort, run-resource release, and the inter-step cancellation gate.
   Fix (no behavior change on the happy path):
   - `objective_completion.go`: new `discardPendingProven()` — the single
     cleanup path for every transition that abandons or replaces a staged
     grant.
   - `driver.go:Abort`: discards the grant after cancelling the run context.
   - `grant_barrier.go:releaseRunResources`: discards the grant so one run's
     staged completion can never leak into the next run.
   - `driver.go:observeAndRun` cancellation gate: discards the grant before
     `terminateAbort`, so a cancelled run writes no terminal objective state.
   - Resume paths need no new call: a parked run already passed through
     `commitObjectiveCompletion` before `step()` parked it, so
     `pendingProven == nil` at every park (pinned by
     `TestTerminalCleanup_ParkedRunHoldsNoStagedGrant`); the next
     `observeAndRun` re-authorizes through the clearing entry.
2. **Failed requirement derivation published no tally.** `deriveObjectiveRequirements`
   returned early on pass error without `publishObjectiveRequirements`, so
   "no requirements were derived" was indistinguishable from "the tally was
   never emitted". Fix (`objective_lifecycle.go`): the error path now publishes
   `admitted=0 rejected=0` with the recorded reason. The runtime's own
   obligations still gate completion; the failure loosens nothing and proves
   nothing.
3. **Adequacy policy was enforced but unstated.** The gate already implemented
   the correct policy (traceable + scope-grounded or rejected; rejected authors
   no condition; runtime obligations unconditional). Fix
   (`objective_contract.go`): the OBJECTIVE ADEQUACY POLICY block above
   `admitRequirements` now states source/authority (user criteria authoritative
   via runtime conditions; repository facts require attributable evidence;
   model criteria are proposals until validated; rejections retain reasons;
   absence never becomes proof), the best-effort execution rule for broad
   objectives, and the adaptive-proposal routing for consequential choices.

### J2 — Production lifecycle trace (real CLI subprocess)

New: `test/benchmark/cli_terminal_lifecycle_test.go` — three deterministic
subprocess tests over the real `izen` binary (scripted provider is the only
fake; CLI, runtime, verifier, ledger are real; no sleeps):

- `TestCLIBench_TerminalLifecycleMultiArtifact` (`redesign this project help
  me` over index.html/styles.css/script.js + untouched readme.md): derived
  scope contains exactly the implementation surface; `approved=true` (a
  candidate reached the approval boundary); all three files changed in ONE run
  (one report, one ledger task); outcome `unsubstantiated`/exit 5 with
  progress `PARTIALLY_SATISFIED` — the mutations landed but the unverified
  broad objective is NOT proven; ledger holds exactly one task and its last
  TERMINAL `VERIFICATION_RESULT` is non-success, agreeing with the CLI report;
  report usage (36/21) equals exactly the 3 mutation-lane calls × 12/7 (the
  requirement pass is a read-only planning call outside the execution
  aggregate — verified by dumping the provider's system prompts: call 0 is the
  requirement-derivation stage, calls 1–3 are the bounded mutation engine).
- `TestCLIBench_TerminalLifecycleDowngradePreventsProven` (empty artifact):
  non-zero exit, outcome never `proven`, `ghost.txt` absent, zero
  `EXECUTION_COMMITTED` markers, ledger terminal non-success — the downgrade
  prevented terminal PROVEN at the process boundary.
- `TestCLIBench_TerminalLifecycleParkResumeUsage` (broken → fixed provider,
  `run` then `resume`): first report usage == first-run mutation calls × 12/7;
  resume report usage == NEW mutation calls only (the resume did not
  re-commit the first run's spend); the summed usage reconciles exactly with
  total mutation-lane billing; `resumed_from` is recorded.

Observation boundary (documented, not hidden): a subprocess cannot observe the
intra-iteration instant between staging and deferred commit. That ordering —
zero `objective.evaluated{granted:true}` and no `EXECUTION_COMMITTED` before
the behavioral and contract-recovery gates — is pinned deterministically by
the in-process seam tests with a granting authority and a forced downgrade
(`TestObjectiveTerminalGrantIsNotCommittedWhenALaterGateDowngrades`,
`TestObjectiveTerminalGrantCommitsExactlyOnceWhenNoGateDowngrades`,
`TestTerminalCleanup_*`), with no timing. The subprocess tests prove every
observable consequence of that ordering.

### J3 — All-rejected-requirements policy and observed behavior

New: `internal/runtime/autonomy/objective_adequacy_policy_test.go` — five
tests, all by objective semantics and runtime policy (no prompt-string
special cases; `isProjectLevelObjective` reads the strategy gateway's
`Profile.Operation`, never the prompt text):

1. `TestAdequacy_ClearObjectiveAdmitsAndProves` — `change bar to qux
   @note.txt` with a grounded proposal: 1 admitted requirement, approval,
   terminal COMPLETED, progress PROVEN.
2. `TestAdequacy_BroadObjectiveEvidenceSupportsBoundedProposal` —
   `DefaultProposalAdvisor` over the observed surface yields one evidence-bound
   strategy with repository-attributed observations, acceptance criteria, and
   zero questions.
3. `TestAdequacy_BroadObjectiveConsequentialDecisionParks` — an advisor with a
   consequential required question parks at `awaiting_human` with the question
   on the boundary; nothing executes and no requirement is invented.
4. `TestAdequacy_AllRejectedRetainsReasonsAndProvesNothing` — the exact
   observed vocabulary (`restructure the markup…`, `improve visual
   hierarchy…`, `worldclass kazakhstan-approved…`) over the staticweb scope:
   0 admitted / 3 rejected, every reason retained, zero `SourceModel`
   conditions, runtime obligations intact, and the authority refuses PROVEN
   with zero evidence.
5. `TestAdequacy_SuccessfulMutationWithUnsubstantiatedObjective` — a valid
   mutation on one of two declared targets: the file really changed, the claim
   is rewritten to `LoopUnsubstantiate`, outcome `UNSUBSTANTIATED`.

Observed behavior for `$prompt check this project and redesign it`: repository
evidence IS sufficient to construct the bounded default proposal (the
evidence-derived implementation surface + `DefaultProposalAdvisor`), execution
proceeds under that explicitly recorded best-effort policy, and completion
stays truthfully `GATE_UNVERIFIED`/`UNSUBSTANTIATED` without rendered proof —
never silently PROVEN.

### J4 — CLI versus durable-ledger terminal-state consistency

Per subprocess test above: one CLI run ↔ one ledger task; the report's
`outcome`/`progress` and the journal's last terminal `VERIFICATION_RESULT`
(`terminal=true`, `ok` flag) agree in both directions — proven reports sit
above successful journals, unproven reports sit above non-success journals
(`ledgerLastTerminalSuccess` reads only `terminal=true` records; step-level
results are skipped). A finished objective refuses resume without minting a
second task (pre-existing `TestCLIBench_MultiPhaseMigration…` tail).

### J5 — Provider usage reconciliation across park/resume

Per `TestCLIBench_TerminalLifecycleParkResumeUsage`: each process's JSON
`usage` equals its own mutation-lane calls × (12 in / 7 out); the resume does
not re-commit the parked run's spend; the sum reconciles to the provider's
total mutation-lane billing. (The ~2× TUI footer double-count fixed earlier is
a presentation-baseline issue; the CLI report reads `AggregatedUsage`
directly and never held it. The residual session-baseline/live-estimate
portion reported in §F is unchanged and still reported, not hidden.)

### J6 — Verification evidence and limitations

Rendered verification stays OPT-IN (`IZEN_RENDERED_VERIFY=1`,
`compose.go:1029`); no DOM assertions were added and no screenshot is claimed
as visual proof. New: `rendered_evidence_binding_test.go` — 4 tests:

- a failed configured check projects `RENDERED_FAIL` and the authority refuses
  PROVEN on it;
- `renderedRevision` is content-bound (same targets, different bytes ⇒
  different 16-hex revision), so a check cannot be replayed against a later
  edit;
- missing configuration (no verifier / no entry / no checks) projects
  distinct blocked states, never a pass;
- the authority consumes exactly that evidence: passing rendered verification
  satisfies a broad objective's `cond-integrity-held`, a skipped gate leaves
  `GATE_UNVERIFIED` and unsatisfied.

Limitation: this environment holds a Chrome binary but CI does not guarantee
one; the real-browser path (`TestRenderedVerifier_RealBrowser`) remains
skip-guarded, and the headless-browser E2E is therefore covered by the blocked/
unit-bound evidence above rather than a live rendering in this run.

### J7 — Terminal-state cleanup audit

New: `objective_terminal_cleanup_test.go` — 6 tests pinning every abandonment
path: cancel discards, abort/release discards, retry decision-point clears,
parked run holds no grant (resume re-authorizes), new run never inherits,
later downgrade discards with zero terminal grants. The durable terminal
transition still has ONE authoritative commit path: `grep` confirms
`emitObjectiveEvaluated(…, granted=true)` and the objective's
`ledgerExecutionCommitted()` occur only in `commitObjectiveCompletion`
(the mutation boundary's `ledgerMutationCommitted` is the OPERATION commit, a
different fact, deduped per-op by `ledgerCommittedOp`).

### J8 — Validation and final report

```
go build ./...                                            clean
go vet ./internal/runtime/autonomy/ ./internal/ui/ \
         ./test/benchmark/ ./internal/execution/          clean
gofmt -l internal/ test/ cmd/                             clean
golangci-lint run ./internal/runtime/autonomy/... \
                  ./internal/ui/...                       0 issues
go test -count=1 ./...                                    213 ok, 0 FAIL
go test -race -count=1 -run 'TestObjectiveTerminalGrant|
  TestRequirementRejection|TestAutonomousUsage|TestE2E_ParkedRun|
  TestPhase14|TestOrchestration_B|TestAdequacy_|
  TestTerminalCleanup_|TestRendered_' \
  ./internal/runtime/autonomy/ ./internal/ui/             ok, no data race
go test -count=1 -run 'TestCLIBench_TerminalLifecycle' \
  ./test/benchmark/                                       3 PASS
```

New tests added this round: 5 adequacy + 6 cleanup + 4 rendered-binding +
3 CLI subprocess = 18. All prior tests pass unmodified.

## K. Verdict — **COMPLETE**

Against the mission's five sections:

| § | Requirement | Status | Evidence |
|---|---|---|---|
| 1 | Real CLI subprocess test over the production lifecycle | **DONE** | §J2; 3 subprocess tests, deterministic, no sleeps |
| 2 | Policy for objectives with no admitted requirements + 5 tests | **DONE** | §J1.3, §J3; policy block in `objective_contract.go`, 5 tests |
| 3 | Verification policy without overclaiming | **DONE** | §J6; opt-in kept, 3 prior + 4 new tests |
| 4 | Terminal-state cleanup for all abandonment paths | **DONE** | §J1.1, §J7; `discardPendingProven` + 6 tests, one commit path |
| 5 | Full validation + report | **DONE** | §J8; build/vet/lint/test/race/CLI all green |

The two earlier code-level fixes were re-verified, not repeated: every prior
test passes unmodified, and the only production-code changes are the three
surgical items in §J1 (grant cleanup on abort/release/cancel, failed-pass
tally visibility, adequacy-policy documentation). The remaining non-defects
are stated where they belong: the intra-iteration instant is observable only
in-process (pinned there deterministically); rendered E2E needs a browser CI
does not guarantee; the TUI footer residual beyond the fixed ~2× is session
baseline/estimates. Vague objectives cannot silently become success: an
all-rejected derivation retains its reasons, executes only under the recorded
best-effort policy, and terminates `UNSUBSTANTIATED` — never `PROVEN` —
without evidence.

*Dynamic Path. Static Authority. Truthful State Transition.*

---

## L. Follow-up (2026-10-10) — Constrained-model artifact contract match

**Branch:** `fix/execution`. **Input under audit:** `$prompt check this project and
redesign it` over `index.html`, `script.js`, `styles.css`, provider/model
`openrouter` / `cohere/north-mini-code:free` (the exact observed run). **Verdict:**
**COMPLETE** for the artifact-generation defect named in this round; see §L7.

This section is a fresh audit of the *artifact-generation and bounded-recovery*
path. It reproduces the production failure, identifies its authoritative owner,
fixes it there, and pins it with deterministic regression tests plus a real CLI
benchmark.

### L1 — Reproduction (live, then deterministic)

**Live reproduction of the current tree (before the fix):** the exact objective
with the configured provider/model parked at `PREFLIGHT_INFEASIBLE` (invariant
I5) with **zero provider calls** and zero mutations:

```
outcome : parked
reason  : invariant I5: preflight infeasible — explicit re-scope required
scope   : [index.html, script.js, styles.css]
usage   : input_tokens=0 output_tokens=0 known=false
```

**Deterministic reproduction of the *reported* artifact rejections** (scripted
provider, unfenced output, constrained model) — this is the failure the mission
names, now pinned by `TestCLIBench_ConstrainedModelInvalidArtifactStopsTruthfully`:

```
NON_PROGRESSING_EXECUTION: ARTIFACT_INVALID target=index.html:
  executor: mutation artifact rejected with retry directive: index.html:
  bounded patch contract requires SEARCH/REPLACE blocks or unified diff hunks;
  full-file or unstructured output rejected;
  RMAH: RMAH Tier 3: synthesized patch rejected due to destructive truncation
  (<60% retention)
  (seen 2 time(s) under unchanged evidence) — hard-block: bounded recovery
  exhausted for index.html (attempt 2, recovery cycle 1, subtype
  "schema_violation") — park at DecisionSurface awaiting_human
```

### L2 — Root-cause table

| # | Symptom | Authoritative owner | Consequence | Correction |
|---|---|---|---|---|
| R1 | Multi-target objective parks at `PREFLIGHT_INFEASIBLE`, zero provider calls, before any artifact exists | `RuntimeExecutor.Execute` Boundary-2 guard (`executor.go:2155`) judging `!patchOnlyArtifact(profile)` | A feasible per-target bounded-patch redesign is refused as an infeasible *full rewrite* the executor was never going to attempt; the whole objective dies before generation | Judge the **dispatch** contract: `willDispatchBoundedPatch` (new, `executor.go:5886`) folds the existing constrained-model resolution into the guard condition |
| R2 | `script.js`: "no extractable code content" | `rmah.Pipeline` Tier 2 + `tier3ASTTarget` | A constrained model that ignores the SEARCH/REPLACE contract is rejected (correct) but the *root* cause (a model under a small ceiling) is the contract, not the parser | Same as R1: the constrained model is dispatched under the contract it can satisfy; a genuine contract miss is recovered/parks truthfully |
| R3 | `index.html`: "synthesized patch … destructive truncation (<60% retention)" | `rmah.Pipeline` Tier 3 retention floor (`rmah.go:245`) | A window-scoped response is measured against the whole baseline, so it is correctly refused as a destructive whole-file rewrite | Retention floor **unchanged** (per the mission); the run is recovered with the failed-approach evidence and parks truthfully |
| R4 | Terminal `NON_PROGRESSING_EXECUTION` | `FailureLedger` + `DecideRecoveryWith` | Correct bounded termination; the CLI and ledger agree it is not a success | No change (verified truthful); regression test added |

R1 is the verified production defect: a *contract mismatch* between the driver's
own adaptive decision (`AdaptiveSelectStrategy` already returns
`StrategyBoundedPatch` for large targets / small budgets) and the executor's
Boundary-2 guard, which evaluated the request as a full rewrite.

### L3 — Files and authoritative seams changed

| File | Seam | Change |
|---|---|---|
| `internal/execution/executor.go` | `RuntimeExecutor.willDispatchBoundedPatch` (new) + Boundary-2 condition | The guard now asks the same question `invokeMutation` asks: "will this dispatch be bounded?" A constrained model (free-tier / low-ceiling) on an existing target is bounded by construction, so the guard is skipped and the bounded patch is dispatched. The full-rewrite refusal is preserved for every model that would genuinely attempt one. |

No kernel, parser, approval, mutation, verification or ledger code was touched.
No retention threshold, schema check, scope, or completion gate was weakened.
`willDispatchBoundedPatch` reuses the two existing authorities
(`llmstep.ResolveMaxTokens` and `ModelProfile.IsConstrained`) — it does not
re-derive capability.

### L4 — Why this addresses the root cause

`invokeMutation` already forces `patchOnly = true` for a constrained model on an
existing target (`executor.go:3501`), and that contract fits any output budget by
construction. Boundary 2 ran *before* that classification and refused the same
request as an infeasible full rewrite. The two owners disagreed about the
contract. `willDispatchBoundedPatch` makes Boundary 2 read the same contract the
dispatch will use, so the guard still refuses a full rewrite that cannot fit and
no longer refuses one the runtime will not attempt. The bounded patch itself
still crosses the *unchanged* canonical artifact boundary (SEARCH/REPLACE /
unified-diff extraction, anchor proof, AST gate, retention floor, approval,
mutation, verification, ledger).

### L5 — Regression tests and exact outcomes

| Test | File | Pins |
|---|---|---|
| `TestConstrainedModelSkipsInfeasibleFullRewritePreflight` | `internal/execution/constrained_preflight_test.go` | constrained model on a large target dispatches the bounded patch (1 provider call, staged candidate, bounded-patch system prompt, `max_tokens=980`) |
| `TestUnconstrainedModelStillRefusesInfeasibleFullRewrite` | same | the guard is **not** weakened: the same target/budget still refuses with `ErrPreflightInfeasible` and zero provider calls |
| `TestTier2_RetentionBoundary` | `internal/execution/rmah/rmah_test.go` | retention floor at exactly 60% is admitted; above admitted; below rejected with `destructive truncation` |
| `TestConstrainedBoundedPatchRecoveryCarriesFailedApproach` | `internal/runtime/autonomy/constrained_recovery_evidence_test.go` | a rejected constrained bounded-patch attempt produces a materially-changed continuation carrying `[FAILED APPROACH]` + the SEARCH/REPLACE contract; the corrected second attempt stages through the same canonical validator |
| `TestCLIBench_ConstrainedModelMultiFileRedesignUsesBoundedPatches` | `test/benchmark/cli_constrained_contract_test.go` | real binary: no `PREFLIGHT_INFEASIBLE`; candidate approved; all three files changed through the canonical path; bounded-patch calls ≥ 3; unverified objective not PROVEN; exit non-zero |
| `TestCLIBench_ConstrainedModelInvalidArtifactStopsTruthfully` | same | real binary: unfenced output → workspace byte-identical, zero `EXECUTION_COMMITTED`, non-zero exit, attributable reason, bounded provider calls |

Exact command outcomes (final tree):

```
gofmt -l internal/ test/ cmd/                              no output
go build ./...                                             clean
go vet ./internal/execution/... ./internal/runtime/autonomy/ ./test/benchmark/   clean
golangci-lint run --timeout=5m ./internal/execution/... \
    ./internal/runtime/autonomy/... ./test/benchmark/...   0 issues
go test -count=1 ./...                                     213 ok, 0 FAIL, exit 0
go test -race -count=1 -run 'TestConstrainedModel|TestUnconstrainedModel|TestConstrainedBoundedPatchRecovery|TestTier2_RetentionBoundary|TestCLIBench_ConstrainedModel' \
    ./internal/execution/ ./internal/execution/rmah/ ./internal/runtime/autonomy/ ./test/benchmark/   ok, no data race
go test -count=1 -run 'TestCLIBench' -timeout 15m ./test/benchmark/            ok
```

### L6 — Real CLI benchmark (subprocess, real provider)

Disposable copy of `testdata/staticweb`, the exact objective, the configured
provider. Raw evidence is stored beside this report:
`docs/report/realworld/evidence/constrained-bounded-redesign-{report.json,ledger.txt,diff.patch}`.

```
provider/model : openrouter / cohere/north-mini-code:free
surface        : $prompt
outcome        : unsubstantiated      (truthful: rendered verification is opt-in/off)
reason         : cond-integrity-held unmet: GATE_UNVERIFIED — refused completion
                 claim over outcome "changed"
approved       : true
candidate_id   : run-1-patch-1
candidate_digest: 9852d16a1d37943bf5d27627f8fef0aa50e3c83e26c44133cd68e915ed7514a7
applied        : [index.html, script.js, styles.css]
progress       : PARTIALLY_SATISFIED
usage          : input_tokens=6895 output_tokens=512 known=true
exit_code      : 5
```

Durable ledger agrees (16 records, one task):

```
TASK_CREATED        scope=[index.html, script.js, styles.css]
TASK_PAUSED         "mutation awaiting approval"
HUMAN_DECISION      approve candidate=run-1-patch-1 digest=9852d16a… targets=[…]
EXECUTION_COMMITTED status=COMMITTED
FAILURE_CLASSIFIED  reason=unsubstantiated
VERIFICATION_RESULT(terminal) ok=false failed=true
```

Against the mission's Phase-6 acceptance list:

1. **Discovery evidence-bound to the three implementation files** — `TASK_CREATED.scope`
   is exactly `[index.html, script.js, styles.css]`; the out-of-scope `assets/`
   was untouched.
2. **Every mutation through the canonical validated artifact path** — three
   `search_replace` bounded patches, one approved candidate, one `EXECUTION_COMMITTED`.
3. **Invalid output corrected or stops safely** — deterministic
   `TestCLIBench_ConstrainedModelInvalidArtifactStopsTruthfully` parks with an
   attributable reason and zero mutation.
4. **No malformed/truncated/destructive artifact bypasses validation** — the
   artifact boundary and retention floor are unchanged; invalid output never
   stages.
5. **The approved candidate is exactly the candidate applied** — one
   `candidate_id`/`candidate_digest`, bound in `HUMAN_DECISION`, applied to all
   three targets.
6. **Partial failure is not reported as objective success** — outcome
   `unsubstantiated`, `progress=PARTIALLY_SATISFIED`.
7. **CLI and durable ledger agree** — report exit 5 / `unsubstantiated` sits above
   a terminal `VERIFICATION_RESULT{ok:false}`.
8. **Usage accounting consistent** — the live report's usage is `known=true` and
   carries a single execution-lane aggregate (no double count; see §F for the
   presentation-layer note). The exact per-call reconciliation against provider
   billing is pinned deterministically by the scripted-provider subprocess tests
   (`TestCLIBench_TerminalLifecycleMultiArtifact` / `…ParkResumeUsage`), since a
   live free-tier provider does not expose an inspectable billing ledger here.
9. **Original files preserved before approval; mutations inside the workspace** —
   the diff touches only the three in-workspace files.
10. **Rendered verification unverified** — `IZEN_RENDERED_VERIFY` is unset, so the
    broad objective is truthfully `GATE_UNVERIFIED`; no visual success is claimed.

The applied diff is preserved in `…-diff.patch`; it is a real (small) change to
each file, not a fabricated success. It is **not** claimed to be a visually
excellent redesign — the model is a free-tier model and the objective is
correctly `UNSUBSTANTIATED`.

### L7 — Verdict — **COMPLETE**

| Phase | Status | Evidence |
|---|---|---|
| 1 Audit + reproduction | DONE | §L1, §L2; live + deterministic reproduction |
| 2 Artifact normalization/admission | DONE | §L3; admission now judges the dispatch contract; canonical parser unchanged |
| 3 Recovery converges or stops truthfully | DONE | §L5 recovery test + deterministic park; recovery carries the failed approach and the required contract |
| 4 Multi-file execution strategy | DONE | §L4; per-target bounded patches through the existing path; partial progress explicit |
| 5 Regression tests at the real seams | DONE | §L5; 6 new tests across 4 files |
| 6 Real CLI acceptance benchmark | DONE | §L6; live provider run + ledger + diff |
| 7 Full validation + report | DONE | §L5 commands; this section |

**What is proven:** the verified artifact-generation defect (the driver/executor
contract mismatch that parked a feasible multi-target redesign before any
generation) is fixed at its authoritative owner; a valid bounded patch becomes a
reviewable, applied candidate; invalid output is recovered with actionable
evidence or parks truthfully; the real CLI benchmark produces auditable evidence
consistent with the durable ledger.

**Limitations (stated, not hidden):** the live free-tier model's patches are
small and are not a visually excellent redesign (the objective is truthfully
`UNSUBSTANTIATED`); rendered/browser verification is opt-in and was not enabled,
so visual correctness is **unverified**; CSS has no registered structural
validator, so a syntactically broken CSS patch is only caught by verification,
not by the artifact gate — this is a pre-existing coverage limitation, not a
regression, and adding a CSS validator is out of scope for this defect.

*Dynamic Path. Static Authority. Truthful State Transition.*
