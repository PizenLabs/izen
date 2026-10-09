# IZEN — Adaptive Proposal Orchestration & End-to-End Objective Execution

**Status:** implemented and validated, with explicitly documented limitations.
**Branch:** `fix/execution`
**Scope:** first-class adaptive proposal contract; adaptive interaction
selection; revision-safe human decisions; compilation of accepted decisions into
the existing authoritative execution contract; rendered-layout verification;
UI projection; regression tests.

This is the final report. Sections A–G follow the required format. All claims
below are backed by source that exists in this working tree and by commands run
against it.

---

## A. Verified baseline

At the start of this phase the working tree was clean on `fix/execution` at
`56e53a5 feat(exec): truthful project-level execution`. I inspected the tree,
the recent commits/diffs and the tests rather than trusting the prior report.
The preceding phase already provided:

- Evidence-bound, project-level scope derivation
  (`internal/execution/derive.go`, `Driver.deriveEvidenceScope`) that binds the
  workspace's authored implementation artifacts and excludes documentation,
  data, configuration and assets, bounded to 12 files / 5 extension families.
- Truthful completion for broad objectives: `ObjectiveEvidence.BroadObjective`,
  `GATE_UNVERIFIED`, and a `verifierSatisfied` that refuses to treat a skipped
  verifier as a pass for a broad transformation.
- Durable human-decision recording: `ledgerHumanDecision`,
  `durable.EventHumanDecision`, `Driver.ResumeClarify/Approve/Reject/WithProposal`
  all append a decision record.

The single authoritative owner of execution/authorization is the `Driver`
(`internal/runtime/autonomy/driver.go`) over the `ExecutorAdapter`; completion is
owned solely by `execution.ObjectiveCompletionAuthority`. The pre-existing
`DecisionSurface` (`internal/runtime/autonomy/proposal.go`) is a recovery surface
for a CLOSED preflight gate, and `preflight.DecisionSurface` is a budget-overflow
presentation. Neither is a general adaptive proposal. The repository's own
`docs/report/IZEN_OBJECTIVE_ORCHESTRATION_TRUTHFUL_EXECUTION.md` §5 named the
same three remaining gaps this phase closes.

No resolved defect was reopened; no regression in the three prior fixes was
found.

## B. Architectural decisions

| Concern | Owner | Rationale |
|---|---|---|
| Proposal data, revision, decisions, compilation | new pure package `internal/runtime/proposal` | It owns no workspace, provider, executor or authority. It is the typed language and the deterministic rules. |
| Interaction selection | `proposal.SelectInteraction` | Pure function of objective + observed evidence + policy; the runtime does not follow a fixed questionnaire. |
| Proposal transport to the UI | `internal/autonomy.HumanBoundary` (`HumanBoundaryAdaptiveProposal`, `HumanProposalQuestion`, `ProposalDecision`) | Mirrors the existing `HumanProposalOption` pattern; the UI can name it without importing the runtime package. |
| Runtime orchestration | `internal/runtime/autonomy/adaptive_proposal.go` (Driver seam) | The Driver holds the rich `proposal.State`; accepted decisions compile into the SAME `d.req`/resolved scope. No parallel executor, no second durable task owner. |
| Completion | unchanged `execution.ObjectiveCompletionAuthority` | Rendered verification is added as EVIDENCE (`ObjectiveEvidence.RenderedVerification`) consumed by the existing integrity condition; the authority remains the only completion owner. |
| Rendered verification | `internal/runtime/autonomy/rendered_verify.go` | A consumer that renders the real workspace with the local headless browser; it mutates nothing and grants nothing. |
| Authorization | unchanged approval boundary + admission gate | Accepting a strategy is NOT approving a concrete mutation. `applyCompiledProposal` re-binds targets through the canonical gateway and never applies bytes. |

A model recommendation is `ProvenanceModel`; only a `ProvenanceHuman` decision can
satisfy a required question or compile into a contract. `ProvenancePolicy`
answers are accepted only for questions that declare a `Default`. A proposal can
never grant a capability.

## C. Implementation

### New files

- `internal/runtime/proposal/contract.go` — typed `Contract`, `Provenance`,
  observations, assumptions, uncertainties, strategies, trade-offs, risks,
  dependencies, phases, acceptance criteria, verification methods, questions
  (single/multi/free-form), options, decisions, admission conditions;
  `Validate` and revision-safe `Fingerprint`.
- `internal/runtime/proposal/interaction.go` — `Interaction`
  (direct/investigate/clarify), `Signal`, `SelectInteraction`,
  `ConsequentialQuestions`, `Unanswered`.
- `internal/runtime/proposal/state.go` — revision-safe `State`; `Apply`
  (human), `ApplyPolicy` (declared default only), dependency-graph
  invalidation, `Complete`, `Compile` → `ExecutionContract`,
  `Snapshot`/`Restore` with fingerprint tamper-refusal.
- `internal/runtime/autonomy/adaptive_proposal.go` — `ProposalAdvisor`,
  `ProposalFacts`, `ProposalPlan`, `DefaultProposalAdvisor`,
  `WithProposalAdvisor`, `WithRenderedVerificationPolicy`,
  `maybeAdaptiveProposal`, `ResumeAdaptiveProposal`, `applyCompiledProposal`,
  durable proposal snapshot recording, `ProposalSnapshot`/`RestoreProposal`.
- `internal/runtime/autonomy/rendered_verify.go` — `RenderedVerification`,
  `RenderedCheck`, `HeadlessBrowserVerifier`, `DetectBrowser`,
  `WithRenderedVerifier`, `WithRenderedChecks`, `ensureRenderedVerification`.
- Tests: `internal/runtime/proposal/proposal_test.go`,
  `internal/runtime/autonomy/adaptive_proposal_test.go`,
  `internal/runtime/autonomy/rendered_verify_test.go`,
  `internal/execution/objective_rendered_test.go`,
  `internal/ui/adaptive_proposal_test.go`.

### Changed files

- `internal/autonomy/runtime_loop.go` — `HumanBoundaryAdaptiveProposal`;
  `HumanProposalQuestion`, `HumanProposalStrategy`/`Tradeoff`/`Risk`/`Phase`,
  `ProposalDecision`; boundary carry fields; `DeriveBoundaryAction` case.
- `internal/runtime/autonomy/driver.go` — proposal/rendered fields and reset;
  `maybeAdaptiveProposal` call; `ensureRenderedVerification` before the
  completion authority.
- `internal/runtime/autonomy/objective_completion.go` — `RenderedVerification`
  folded into the evidence bundle.
- `internal/execution/objective_authority.go` —
  `RenderedVerificationEvidence`, evidence field, `VerifierVerdict`,
  `verifierSatisfied`.
- `internal/execution/objective_conditions.go` — rendered facts and the
  `ObligationIntegrityHeld` broad-objective branch.
- `internal/runtime/durable/types.go` — `EventProposalSnapshot`.
- `internal/runtime/compose/compose.go` — production wiring of
  `DefaultProposalAdvisor` and the env-gated rendered verifier.
- `internal/ui/{autonomous.go,keys.go,model.go}` — render the adaptive proposal
  and submit decisions through `Driver.ResumeAdaptiveProposal`.
- `internal/ui/autonomous_test.go` — fake driver implements the new interface
  method.

## D. Demonstrated workflows

- **Simple task (direct).** `TestAdaptive_DirectTaskKeepsItsPath`: with the
  advisor wired, `create a new file named zuru.md …` selects `direct`, forms no
  proposal, and reaches the existing approval gate unchanged.
- **Project redesign (investigate → proceed).**
  `TestAdaptive_InteractionDiffersByDecisionRequirement` objective 1: a broad
  objective with no consequential choice proceeds with the recommendation over
  the evidence-derived multi-file scope and records the compiled proposal.
  `TestCLIBench_ProjectRedesignDerivesMultiFileScope` still passes end to end.
- **Adaptive proposal (clarify).** Same test objective 2: a consequential
  choice parks at `HumanBoundaryAdaptiveProposal` with exactly the material
  question and zero provider calls before the human decided.
- **Acceptance → execution contract.**
  `TestAdaptive_AcceptedDecisionCompilesIntoExecution`: accepting compiles the
  decision into constraints, re-binds the derived targets through the canonical
  gateway, dispatches one invocation per target, and reaches the existing
  approval gate with nothing written yet.
- **Revision and invalidation.**
  `TestAdaptive_IncompleteDecisionReparksWithRevision`: a partial decision set
  re-parks at revision 2 with an explicit incompleteness, executes nothing, then
  compiles at revision 3 when the remaining decision arrives. Unit-level
  dependency invalidation is `TestStateRevisionAndInvalidation`.
- **Authorization boundaries.** `TestAdaptive_ProposalDoesNotWidenAuthority`
  (compiled scope ⊆ derived scope; mutation review preserved; no pre-approval
  write) and `TestAdaptive_HotNeverGainsRenderedCapability` (`$hot` refuses the
  browser capability).
- **Truthful verification.** `TestAdaptive_RenderedEvidenceFlowsToCompletion`
  and the `internal/execution` `TestRendered_*` tests: a passing real rendered
  check satisfies the broad objective's integrity condition; a failed check is
  `GATE_FAIL`; a blocked check is `GATE_UNVERIFIED`; a non-broad skipped gate is
  unchanged.
- **Durable decisions / resume.** `TestAdaptive_DecisionsAreDurable` asserts
  `HUMAN_DECISION` and `PROPOSAL_SNAPSHOT` in the on-disk ledger and that the
  snapshot restores the human decision and revision.
- **UI projection.** `TestAutonomousAdaptiveProposalRendersAndSubmits`,
  `…EscapeAborts`, `…DigitSelects`: the TUI renders the proposal and routes the
  selected option ids to the runtime.

## E. Evidence

Commands run (repository root):

```
go build ./...                                   clean
go vet ./...                                     clean
golangci-lint run <changed packages>             0 issues
go test -p 1 ./... -count=1                      all packages ok
go test -race ./internal/runtime/proposal/       ok
go test -race ./internal/runtime/autonomy/ -run 'TestAdaptive_|TestRendered'   ok
go test -race ./internal/ui/ -run TestAutonomousAdaptiveProposal               ok
go test ./test/benchmark/ -run TestCLIBench_ProjectRedesignDerivesMultiFileScope  ok
```

Real external integration (actual local browser, not a fake): the
`TestRenderedVerifier_RealBrowser` test renders a fixture whose visible text is
produced by JavaScript and checks the RENDERED DOM. Representative log:

```
rendered verification: browser="Google Chrome 155.0.8059.39"
  revision=4916abf74fd1a9d4
  observations=[rendered index.html artifact_revision=4916abf74fd1a9d4
                check script-ran: satisfied check initial-gone: satisfied]
  screenshot="/var/folders/…/TestRenderedVerifier_RealBrowser…/rendered.png"
```

Fake-provider tests are explicitly the driver tests that use `mockProvider` /
`targetProvider` (`adaptive_proposal_test.go`, `objective_orchestration_test.go`)
and the `fakeRenderedVerifier` in
`TestAdaptive_RenderedEvidenceFlowsToCompletion`; they exercise the production
`Driver → ExecutorAdapter → RuntimeExecutor` path with a scripted model, and are
labelled as such. The CLI benchmark is a real-binary subprocess test. The
rendered verifier test is a real external integration.

### Note on parallel-test flakiness

Running `go test ./...` with default package parallelism twice produced one
failure each time in a DIFFERENT, pre-existing timing-sensitive autonomy test
(`TestExecutionTrace_CanonicalObjectiveSequence`, then
`TestR5_CaseA_EndToEndMutationReachesProven`). Both pass in isolation and the
entire suite is green under `-p 1`. Neither test touches the proposal or
rendered paths (no advisor is wired in them). This is cross-package resource
contention in the existing suite, not a regression introduced here.

## F. Remaining limitations

1. **Cross-process proposal resume is not fully automatic.** Decisions and the
   proposal snapshot are durably recorded (`EventHumanDecision`,
   `EventProposalSnapshot`) and `Driver.ProposalSnapshot`/`RestoreProposal`
   restore within a process with tamper-refusal. The durable store does not
   currently expose read-back of custom-event payloads, so an automatic restore
   of the proposal snapshot from the ledger on a fresh process is not wired.
   `RestoreProposal` is the sanctioned seam once read-back exists; this is
   deliberate rather than silently reconstructing authorization from
   conversational text.
2. **Rendered verification is policy-gated in production.** It is enabled by
   `IZEN_RENDERED_VERIFY=1` with `IZEN_RENDERED_ENTRY` and
   `IZEN_RENDERED_CONTAINS`. Unconfigured, the objective stays `UNVERIFIED`.
   The TUI does not currently expose a configuration surface for it.
3. **Rendered checks are deterministic DOM substring checks.** Model-assessed
   visual judgement is NOT implemented. This is intentional: the adapter never
   claims an image was visually inspected because a screenshot was generated.
4. **Free-form TUI input.** The runtime API supports and validates free-form
   answers (`QuestionFreeForm`, `AllowFreeForm`), and the proposal package tests
   cover them. The TUI currently exposes single/multi selection only; free-form
   submission is API-only.
5. **Multi-phase migration fixture.** The contract models phases, checkpoints,
   boundaries, dependencies and acceptance criteria, and phase/dependency
   compilation and invalidation are tested (`TestCompilePhasesCarryTargetsAndAcceptance`,
   `TestStateRevisionAndInvalidation`). A full multi-phase migration fixture
   driven end to end was not added.
6. **Production default advisor asks no questions.** The domain-neutral
   `DefaultProposalAdvisor` recommends the evidence-bound strategy and asks
   nothing; consequential questions require a domain/policy advisor. This keeps
   simple tasks efficient and is the deliberate boundary that keeps domain
   vocabulary out of runtime decision sites.

## G. Completion verdict

**PARTIALLY COMPLETE.**

| # | Definition of Done | Verdict |
|---|---|---|
| 1 | Typed/validated proposal contract | **DONE** — `internal/runtime/proposal`; validated, fingerprinted, tested. |
| 2 | Interaction adapts, not a fixed questionnaire | **DONE** — `SelectInteraction` + `TestAdaptive_InteractionDiffersByDecisionRequirement`, `TestSelectInteractionIsNotAFixedQuestionnaire`. |
| 3 | Repository evidence informs recommendations/targets | **DONE** — advisor consumes `ProposalFacts` derived from observed scope; targets are evidence-derived. |
| 4 | Human decisions durably recorded with revision/provenance | **DONE** — `EventHumanDecision` + `EventProposalSnapshot`; `TestAdaptive_DecisionsAreDurable`. |
| 5 | Accepted decisions compile into authoritative execution contract | **DONE** — `applyCompiledProposal` → existing `d.req`/gateway; `TestAdaptive_AcceptedDecisionCompilesIntoExecution`. |
| 6 | Revision changes invalidate correct dependent state / stale approvals | **DONE** — `State.Apply` dependency invalidation + `invalidateProposalDerivedState` (drops held candidates, invalidates context, re-binds scope); tested. |
| 7 | Mutations stay on the existing authorization/execution authority | **DONE** — no new executor; approval gate + admission preserved; `TestAdaptive_ProposalDoesNotWidenAuthority`. |
| 8 | Simple tasks keep the efficient path | **DONE** — `TestAdaptive_DirectTaskKeepsItsPath`; advisor opt-in. |
| 9 | Multi-artifact objectives execute against a justified scope | **DONE** — existing derivation + `TestCLIBench_ProjectRedesignDerivesMultiFileScope` and adaptive tests. |
| 10 | Completion truthful under skipped/failed verification | **DONE** — `TestRendered_*` and `TestOrchestration_B_ProjectRedesignUnverifiedIsNotProven`. |
| 11 | Rendered verification integrated and validated, or blocker documented | **PARTIAL** — real browser adapter integrated and validated (Chrome 155); production use is policy-gated and checks are deterministic DOM checks; model visual judgement not implemented (documented). |
| 12 | Required regression/validation commands pass | **DONE** — build/vet/lint/race/tests green; `-p 1` full suite green; parallel flake documented. |
| 13 | Report distinguishes implementation/tests/real evidence/unresolved | **DONE** — this report. |

The phase is not reported COMPLETE because item 11's rendered verification is
deterministic-DOM-only and policy-gated rather than a default, model-assessed
visual verification, and because automatic cross-process proposal restore is not
wired. Both limitations have concrete, documented integration paths and do not
compromise the truthful-state invariants.
