# IZEN — Objective Orchestration & Truthful Execution

**Status:** implemented, full suite green
**Branch:** `fix/execution`
**Scope:** evidence-bound project scope derivation, truthful completion for broad objectives, durable human decisions.

This report records the repository-grounded audit and the changes made to close
the objective-orchestration defect. It follows the report convention under
`docs/report/`.

---

## 1. Confirmed root causes

### RC-1 — A project-level objective with no declared artifact kind is forced to
### clarify by filename (and can be reduced to a single file).

`$prompt redesign this project help me` on a real static-website workspace
(`index.html`, `styles.css`, `script.js`, `readme.md`) previously parked at
`HumanBoundaryClarify` with every workspace file as an option, before any
provider call. The exact production trace:

- `strategy.Select` classifies `redesign` via `architecturalSignals` →
  `OperationArchitect`, and with no explicit/inferred target returns
  `RepositoryInvestigation` (read-only) —
  `internal/execution/strategy/selector.go:77-81,248-261`.
- The objective layer still classifies the request as a mutation
  (`internal/runtime/autonomy/objective_completion.go:159`,
  `internal/autonomy/intent.go:237-243`), so `admissionIntent()` elevates the
  read-only strategy back to `IntentMutate` (`driver.go:2068-2077`).
- `execution.DeriveScope` only derives a target when the objective literally
  names an artifact kind (`internal/execution/derive.go:286-297`). “redesign
  this project” names none, so it returns `UNRESOLVED` with the whole candidate
  set as evidence.
- The preflight admission gate turns the resulting unresolved/ambiguous binding
  into `AdmissionDisambiguate`
  (`internal/runtime/autonomy/preflight_admission.go:351-352`) and the driver
  parks (`driver.go:2266-2272`).

Selecting the offered `index.html` then re-resolved as a single-file
`TargetedMutation`, which is the reported “reduced to selecting index.html”.

### RC-2 — A skipped verifier satisfied the integrity obligation for broad objectives.

`ObjectiveEvidence.verifierSatisfied` treated `VerificationSkipped` as
satisfying a required verifier (`objective_authority.go:440-445`), and
`ObligationIntegrityHeld` marked the condition satisfied as
`GATE_NOT_APPLICABLE` (`objective_conditions.go:268-274`). HTML/CSS/JS have no
verification contract (`internal/language/defs.go`), so a broad redesign could
be reported `PROVEN` / CLI `outcome=proven` on the strength of an applied patch
alone.

### RC-3 — Human decisions were not durably recorded.

`EventProposalAuthorized` existed but the autonomy driver never appended a
decision record; only mutation/terminal events were durable
(`internal/runtime/autonomy/ledger.go`). A resumed run could not read *what*
the human authorized.

---

## 2. Changes

| Area | Change |
|---|---|
| `internal/execution/strategy/{strategy,selector}.go` | `ExecutionStrategyProfile.Operation` exposes the gateway's own operation family so a downstream authority reads the scope width instead of re-classifying the prompt. |
| `internal/execution/derive.go` | `DerivationRequest.ProjectLevel`; `deriveProjectScope` binds the workspace's authored implementation artifacts for a project-level no-kind objective, excluding documentation/data/configuration/assets, bounded to 12 files / 5 extension families. |
| `internal/runtime/autonomy/adapter.go` | `DeriveScopeWith(prompt, stated, projectLevel)`; `DeriveScope` delegates with `false` (backward compatible). |
| `internal/runtime/autonomy/driver.go` | `isProjectLevelObjective` (Architect/Refactor, never `$hot`); `deriveEvidenceScope` passes it; resume paths record durable human decisions. |
| `internal/execution/objective_authority.go` | `ObjectiveEvidence.BroadObjective`; `verifierSatisfied`/`VerifierVerdict` refuse to treat a skipped verifier as a pass for a broad objective. |
| `internal/execution/objective_contract.go`, `objective_conditions.go` | New `VerifyGateUnverified` state and the `ObligationIntegrityHeld` branch that produces it. |
| `internal/runtime/durable/types.go` | `EventHumanDecision` (audit lineage). |
| `internal/runtime/autonomy/ledger.go` | `ledgerHumanDecision` appends the action, candidate identity and content digest. |
| `internal/runtime/autonomy/approval_admission.go` | The approval review now carries the evidence-bound scope rationale. |
| `test/architecture/kernel_lock_test.go` | Updated the two line-numbered allowlist entries shifted by the adapter change. |

### Intentional contract change

`PROVEN` is now **narrower** for broad project-level objectives: when no
verification contract exists for the target language, a broad transformation
terminates `UNSUBSTANTIATED` with `GATE_UNVERIFIED` instead of `PROVEN`.
Content-determined objectives (an explicit creation, a declared delta) keep
their existing behaviour: a provably not-applicable gate is still not a failure.

---

## 3. Regression tests

| Mission test | Test | Level |
|---|---|---|
| A explicit create | `TestOrchestration_A_ExplicitCreateExactContent` | driver |
| B project redesign | `TestOrchestration_B_ProjectRedesignDerivesMultiFileScope`, `..._UnverifiedIsNotProven`, `TestCLIBench_ProjectRedesignDerivesMultiFileScope` | driver + subprocess |
| C existing target conflict | `TestOrchestration_C_ExistingTargetNeverSilentlyOverwritten` | driver |
| E no unnecessary clarification | `TestOrchestration_E_ClearLowRiskAndDiscoverableProceed` | driver |
| H skipped/failed verification | `TestOrchestration_H_SkippedVerifierIsDistinctFromPass` | unit |
| J surfaces / static authority | `TestOrchestration_J_HotSurfaceNeverWidensToProjectScope` | driver |
| durable human decision | `TestOrchestration_DurableHumanDecisionRecorded` | driver + durable ledger |
| derivation bounds | `TestProjectDerivation_*` | unit |

`F`, `G`, `I` are already pinned by the existing
`clarification_invalidation_test.go`, `driver_test.go` and `ledger_test.go`
suites and were not duplicated.

---

## 4. Validation

```
go build ./...                                   clean
go test ./... -count=1                           exit 0 (all packages ok)
golangci-lint run (changed packages)             0 issues
```

End-to-end subprocess evidence for the corrected project redesign:

```
exit=5 outcome=unsubstantiated
reason=objective UNPROVEN (UNSUBSTANTIATED): completion condition
       "cond-integrity-held" is unmet: the verification gate reported
       GATE_UNVERIFIED — refused completion claim over outcome "changed"
index.html, styles.css, script.js: changed
readme.md: byte-identical
```

---

## 5. Remaining gaps

- A **general strategy proposal with human questions** (material migration
  trade-offs, multi-select/free-form, recommendation, revision dependencies) is
  not implemented as a first-class contract. The runtime reuses the existing
  `DecisionSurface` and approval-boundary mechanisms; a proposal that names the
  interpreted objective, recommends a strategy and asks adaptive questions
  remains future work.
- Proposal/boundary **revision numbering** is not introduced; candidate binding
  is content-digest based (`CandidateID` + `CandidateDigest`), which is
  revision-safe for content but is not a monotonic review generation.
- Rendered-layout verification for visual redesigns is not performed; the
  runtime reports `GATE_UNVERIFIED` rather than claiming a visual check.
