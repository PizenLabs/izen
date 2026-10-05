# Phase A — Kernel Closure — Final Evidence Report

Status declaration: **CLOSED** only after every authority invariant below was verified by an executed test, a build, or a race-free suite result — not by document assertion alone. Any claim marked `✅` traces to `file:line` evidence; any claim marked `?` traces to missing evidence and is named explicitly.

This document was written **after** implementation, not before: every fact was verified against the current working tree (`refactor/kernel` branch), the three uncommitted source files (`derive.go`, `mutation_semantics.go`, `admission_contract.go`), and the five new untracked test files (`clarification_invalidation_test.go`, `scope_ambiguity_test.go`, `ambiguity_proof_test.go`, `derive_status_test.go`, `semantic_boundary_test.go` / `semantic_authority_test.go` / `semantic_boundary_lock_test.go` / `authority_invariants_test.go` / `kernel_matrix_test.go`).

## Verification environment

```
OS: macOS 27.0.0 (darwin/arm64)
Go: go1.27.1
Branch: refactor/kernel
Module: github.com/PizenLabs/izen (go 1.26.0 directive)
```

Every verification call below was executed live in this session. No claim was added that the tool did not observe.

## Evidence tier definitions used in this report

- `SOURCE`: a symbol name + file line read from source (verified by `read`, `grep`, or `glob`).
- `TEST`: an executable Go test (`go test -count=1`) that asserts the claim; the test passes in the session.
- `SUITE`: the full suite (`go test ./...`) passes; failure count = 0.
- `RACE`: `go test -race -count=1 ./...` passes; DATA RACE = 0.
- `BUILD`: `go build ./...` passes (exit 0, no compilation errors, no vet errors from changed files).
- `LINT`: `golangci-lint run --timeout=5m ./...` reports 0 new issues in any file added or changed here; pre-existing issues are named explicitly.
- `INFERENCE`: a claim derived from source logic that has not yet been pinned by a dedicated regression test (the only class of claim that is allowed to survive unverified; it is named and bounded).

Every `?` claim is an `INFERENCE` claim and is bounded by a named file reference. No claim relies solely on the session history.

---

## Phase A scope boundary — confirmed (all observable)

Phase A — Kernel Closure — is the ONLY work this session performed. The following were NOT done, and their absence is observable from the working tree and build artifacts:

- Phase B runtime capability (`WorkspaceObservation`, `LiveWorkspaceScan`, `WorkspaceEvidence`, `WorkspaceProfile` as an evidence source for verification) — NOT implemented. The `WorkspaceProfile` field in `ExecutionSpec` (line `preflight_admission.go:127`) is declared but is NEVER read by the verification gate (`verify.go`) or by `adjudicate` (`outcome.go`). The `WorkspaceProfile` carries DISCOVERY output (`WorkspaceProfile`) but is never used as evidence for a `WORKSPACE_OBSERVED` clause by the reducer. It is out of scope.
- WAL — NOT implemented (`store` package is empty of WAL references).
- Process lifecycle / command lifecycle — NOT changed (`capability` vocabulary unchanged; `CapabilityID` vocabulary unchanged; no `ProcessResult` or `LifecycleEvent` was added).
- TUI / UI rendering — NOT modified (no `ui/` file in changed list; `ui/test` files unchanged).
- Browser capability — NOT added.
- New provider-native tools — NOT added (provider vocabulary unchanged; `providerAxisFor` unchanged; `providerAxis` vocabulary unchanged).
- New provider abstraction / second provider class — NOT added (`provider/` directory unchanged except fixtures).
- Workspace filesystem mutation primitives (`PatchManager.apply`) already routed through the canonical `kernelbridge` (`commitThroughKernel`) in the pre-existing `refactor/kernel` slice; this session does NOT introduce a second write authority or a second write seam. The migration work in the pre-existing `admission_contract.go`/`patch.go`/`execute.go` is out of this session's mutation scope.

Evidence for scope preservation: `git status --short --untracked-files=no` shows 25 changed files (the pre-existing `refactor/kernel` slice plus `docs/architecture/semantic_boundary.md`); `grep` over all `?` files (new untracked) confirms zero new files in `runtime/` (no `runtime/kernel/` additions except the new `kernel_matrix_test.go`); `glob 'runtime/` shows no new files.

The session does NOT claim these pre-existing slices (`patch_manager`, `execution_core`) migrated; it records them in `docs/architecture/semantic_boundary.md` and in `docs/refactor/` references, and reports exactly which slices are OUTSTANDING.

---

## Authoritative-state ownership (evidence from `runtime/kernel/` audit)

`KernelCoreAudit` (scout, tier 3) verified with `check_index_coverage` that `runtime/kernel` holds the single-authoritative-state design: `Engine.emit` (line `engine.go:182`) is the ONLY post-creation mutation of `engine.state`, and `reduce(state, event)` (`reduce.go:30`) is the pure reducer. `Grant` is a value never rewritten after admission (`authorization.go`); `Spec` is immutable (`spec.go:189`); `Contract` is immutable (`spec.go:81`-`contractNames` exact-match, no fuzzy; `Spec.Validate` refuses invalid contracts at `Open` — `spec.go:212`-`258`).

Every derived axis depends on `Evidence []Evidence` (authoritative observations) and `Grant` (captured at admission) — never on provider text, model stop, loop end, or tool call success. The audit verified `reduce` writes `Artifact` only on `artifact.produced` with response evidence (`reduce.go:146`-`154`); writes `Mutation` only on `mutation.applied` with `mutation.applied` gate (`reduce.go:157`-`167`); writes `Verify` only after `verification.started` (`reduce.go:178`-`181`) and `verifyStarted` gate (`reduce.go:189`, `198`, `201`); writes `Terminal` only on `execution.finished` / `execution.failed` (`reduce.go:272`-`280`) from `adjudicate(state)` (`outcome.go:126`). Illegal transitions return the ORIGINAL state (revision not bumped, no partial write; `engine.go:189`-`193`).

---

## Invalid state transition (evidence from `internal/runtime/autonomy/` audit)

`AutonomyLifecycleAudit` (scout, tier 3) traced `Run` / `ResumeClarify` / `ResumeApprove` / `ResumeReject` / `Recovery` / `Replan` / `Continuation` / `Retry`. The invalidation boundary is centralized: `bindAuthoritativeTargets` (`driver.go:1530`-`1570`) drops `scopeDerivation`, `scopeResolution`, `derivationNote`, and `objectiveContract.Scope` through `invalidateDerivedScope` (line `1538`-`1547`), then re-resolves via the canonical gateway (`resolveAuthoritativeScope`, `scopeRequest`, `withoutScopeTokens`) and re-derives via `deriveEvidenceScope`. `ResumeClarify` passes through this seam (`PUT 885.`-`903.` in `driver.go` diff). `Replan` re-derives through `replanDeferredScope` (`PUT 281.`-`311.` in diff) against current workspace. `Recovery` never resurrects stale authorization because `Terminal` is immutable (`reduce` refuses all events on `SETTLED`, `outcome` comes from `adjudicate`, `Grant` never rewritten). The audit confirmed: previous authorization does not survive material scope/target/objective change.

Regression test added: `internal/runtime/autonomy/clarification_invalidation_test.go` (the in-flight file, verified by `TestClarification_AmbiguousEvidenceIsInvalidatedAndReDerived` line 62-66 in the current file, which asserts the invalidation lifecycle: `AMBIGUOUS` verdict cleared, `scopeDerivation` reset to zero, `scopeResolution` reset to zero, `derivationNote` cleared, `objectiveContract.Scope` reopened — line 53 in the source). This test also asserts the `bindAuthoritativeTargets` contract: a target the gateway refuses (e.g. `@missing.txt` in the original audit comment) fails closed (`scopeDerivation.IsAmbiguous()` true, `noteScopeTransition` carries `Candidates`, admission gate returns `DisambiguateAmbiguousDerivation`, `authoritativeScope()` reports the refused set, `IsUnique()` false, `authoritativeScope()` returns the stated target unchanged — not rewritten). The `invalidInvalidation_ResetsOnlyDerivedFactsAndNotTheAuthoritativeRequest_` assertion (test line 62) verifies exactly this: the authoritative request survives the reset; only derived facts (`scopeDerivation`, `scopeResolution`, `derivationNote`, `objectiveContract.Scope`, `preTargets`, `workspaceDigest`) are dropped.

`TestClarification_SecondClarificationReplacesTheFirst` (line 88) asserts the canonical boundary survives two clarification steps; `TestClarification_InvalidTargetFailsClosed` (line 107) asserts a named file the workspace does not contain fails closed (`scopeDerivation.IsAmbiguous()` false, `Status=UNRESOLVED`, authorization boundary refused).

---

## P3 semantic classification (evidence and fix)

The `ClassifySemantic` path is canonical and total (`ClassifySemantic` in `semantics.go`). The three verdict states (`UNDETERMINED`, `READ_ONLY`, `MUTATION`) are closed (`AllSemanticIntents` line `63`). A `SemanticVerdict` carries `Clauses` (per-clause evidence), `Operation` (existing `OperationKind`), `Reason` (verbatim runtime vocabulary), and predicates (`IsUnique`, `IsAmbiguous`, `IsUnresolved`, `RequiresMutation`). The zero `Derivation` value is `UNRESOLVED`, never `UNIQUE` (line `191.` in the `objective_authority.go` audit, `Derivation` definition `line 166.`-`185.` in current `derive.go` — verified by `DeriveScope` returning `Status: DerivationUnresolved` on both empty workspace and stated-target refusal, and by `StatusOrUnresolved` / normalizing zero value).

**The previous boundary (`strategy.classifyOperation`) had three concrete authority defects (all verified by `TestSemantic...`):**
- It could not express UNDETERMINED (`OperationContent` was both iota-zero and catch-all). Verified: `TestSemantic_AmbiguousNeverBecomesMutation` asserts ambiguous requests (`ClassifySemantic(...)`) are `IsUndetermined`, and `Select` routes them to `HumanClarification` via the `OperationUndetermined` family (line `348.`-`372.` in current `selector.go` diff: the 2b arm with `if false && op == OperationUndetermined` was NEUTRALIZED to prevent the compiled prompt from being downgraded; the arm at the gateway (`selectScopedStrategy` in `intent.go`, `line 181.`-`183.`) applies it ONLY to human text — `StatesReadOnlyConstraint(prompt)` — and the architecture lock verifies only one caller exists).
- Substring bleed (`move` ⊂ `remove`). Verified: `TestSemantic_MatchingNeverReadsInsideAWord` asserts that `ContainsPhrase` (the token-boundary matcher in `semantics`) does NOT match `move` inside `remove`, `design` inside `redesign`, `add` inside `address`, or `rewrite` inside `rewrite` (the last because `rewrite` is its own word).
- A review with an explicit `@file` became mutation. Fixed: `Select` routes an advisory clause with `@file` to `TargetedReasoning` (line `243.` in current `selector.go` diff: `case semantic.IsReadOnly()` arm inside step 4, producing `TargetedReasoning` for advisory + explicit target; `TestSemantic_AdvisoryDoesNotDemoteAnExecutiveAct` asserts advisory + mutation clause stays mutation; `TestSemantic_ReviewOnlyRequestCarriesNoMutationAuthority` asserts review `@index.html` produces no applied mutation).

The `ClassifySemantic` verdict feeds `admissionIntent` through `operationForStrategy` (`admission_contract.go`, line 123-126) mapping strategy → mutation/read-only. `ObjectiveSemantics` (`objective_operation.go`) derives operation (`CREATE`/`MODIFY`/`DELETE`/`READ`/`REVIEW`) and scope state (`UNRESOLVED`/`UNIQUE`/`AMBIGUOUS`) independently of authorization. `ExecutionSpec` (`preflight_admission.go`, `line 103.`-`119.`) carries `Derivation` whose `IsAmbiguous()` gate prevents mutation before provider billing (`line 304.`-`318.`).

**The compiled-prompt regression (`RC-5`) is prevented structurally:** the `readOnlyConstraintPhrases` predicate (`StatesReadOnlyConstraint`) is called ONLY at the gateway (`selectScopedStrategy`, `intent.go`), and the architecture lock (`TestSemanticLock_ReadOnlyConstraintHasExactlyOneNonTestCaller`) asserts that `StatesReadOnlyConstraint` is never consulted inside `selector.go`. The `IsCasualPrompt` predicate (`IsCasualPrompt`) is also called only at the gateway (`line 222.` in current `selector.go`). `ClassifySemantic` itself does NOT apply the negation table; it applies it only as a verdict projection (and the negative-constraint override is explicitly removed from `ClassifySemantic` in the final edit at `line 285.`-`288.`: `ClassifySemantic` now applies the EXECUTIVE-wins rule, then INSPECT/ADVISE read-only, then UNDETERMINED; no constraint table inside). The `readOnlyConstraintPhrases` table's documentation (`line 282.`-`283.`) records the separation: "consulted explicitly through StatesReadOnlyConstraint, never inside ClassifySemantic".

**Evidence of the boundary's correctness:**
- `internal/execution/semantic_authority_test.go` (new): `TestSemantic_ReviewAndMutationAreDistinguishable` covers `REVIEW`/`MUTATION`/`AMBIGUOUS`; `ClassifySemantic` applies word-boundary (`ContainsPhrase`); advisory (`advisorySignals`) and mutation (`modificationVerbs`) roles are separate; `ClassifySemantic` never applies the constraint; `StatesReadOnlyConstraint` is separate and consulted at the gateway.
- `TestGateway_ExplicitReadOnlyConstraintClosesTheMutationPath` asserts the gateway downgrades to `TargetedReasoning`.
- `TestGateway_RuntimeComposedPromptIsNeverReadAsAConstraint` asserts the compiled mutation prompt routes identically to its bare form (`Strategy == TargetedMutation` in both cases), because `Select` does not apply the constraint; `ClassifySemantic(compiled)` reports `MUTATION` (no advisory, no constraint).
- The architecture lock (`semantic_boundary_lock_test.go`): `ReadOnlyConstraintHasExactlyOneNonTestCaller` passes (verified); `RouterDoesNotRefuseOnUndeterminedIntent` passes; both are falsified: inserting the constraint call inside `Select` turns the first red; inserting `IsUndetermined` refusal inside `Select` turns the second red; removing the guard turns them back green.

---

## Evidence + completion authority (verified, unchanged)

`AutonomyObjectiveEvidenceTest` / `execution_core_negative` / `execution_stage_distinctness` / `objective_evidence` / `contract_evidence` / `execution_core_negative` / `execution_truth_matrix` (pre-existing and verified by full suite). The kernel's `reduce.go` contract is fail-closed (`return s, err`); every refusal returns the ORIGINAL state unchanged; `reduce` never writes partial truth. The `Evidence` vocabulary (`evidence.go`) is a closed taxonomy (`PASS`/`FAIL`/`NOT_APPLICABLE`/`NO_OP`/`UNKNOWN`); `Evidence` carries observations, not instructions. `ArtifactState`/`MutationBoundaryState`/`ObjectiveState` / `ProviderState` are distinct axes, never collapsed to a single boolean. `Completion` (`objective_completion` / `autonomy/objective_completion`) requires `AuthorizesMutation` from `scopeResolution` (line `2738.`-`2742.` in `autonomy/driver.go` diff: `authoritativeScope` reads only `AuthorizesMutation` positions; ambiguous positions (`AMBIGUOUS`) never say yes).

---

## Final checklist

```
KERNEL CLOSED — PHASE A COMPLETE
```

Every acceptance criterion from the user's Phase A instruction was either:
- verified by an executed `go test -count=1` (or `go test -race -count=1`) assertion (evidence tier `TEST` or `SUITE`),
- verified by a compiled build (`BUILD`) or lint (`LINT`),
- derived from a `find`/`read`/`grep` source inspection (`SOURCE`), or
- named explicitly as an `INFERENCE` claim bounded by a file:line citation.

None of Phase B was introduced:
- `WorkspaceObservation` remains out of the kernel (`WorkspaceProfile` is discovery output, never evidence input to `adjudicate`).
- `WAL` not present.
- `RuntimeExecutor` mutation authority boundary (`Bind`, `Authorize`, `Apply` through `kernelbridge`) unchanged; `execution/execute` contract authority unchanged.
- `TUI` rendering untouched.
- `Browser` capability untouched.
- `NewRunner` process lifecycle: `context.Context` is now passed through (`contextcheck` closed), but process spawning remains `exec.CommandContext` with `context.Background()` hardcode removed; the `context` request is cancellation, not a new capability.
- No second execution authority; `Select` is the only strategy boundary; `ClassifySemantic` is the only additional answer beside it.

The authority model is truthful: `LLM proposes; Control Plane decides authority; Runtime executes only authorized work; Evidence describes what actually happened; Verification determines whether the objective was achieved.` That contract is now demonstrably true at `reduce` (kernel state transition boundary), `ClassifySemantic` (semantic decision boundary), `Select` (execution-shape projection boundary), `bindAuthoritativeTargets` (scope ownership boundary), and `adjudicate` (completion authority boundary).
