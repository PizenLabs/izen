# IZEN INTEGRATION PROOF REPORT

**Phase:** Post-R7 — IZEN Integration Proof & Real-World Execution Acceptance
**Branch:** `fix/tui`
**HEAD:** `11dbcfa` (`feat(exec): forensic traces & closure freeze (#188)`)
**Suite:** `go test ./...` → **211 packages `ok`, 0 failures**; `go vet` clean.
**Date:** 2026-10-07

---

## 1. Executive verdict

> **TARGETED INTEGRATION DEFECT — identified, fixed at its owning seams, and the
> finite acceptance matrix re-run to green. The frozen kernel is unchanged.**

R1–R7 proved the runtime contract in isolation. Driving the **exact** reproduction
from the brief (`$prompt add file named addtest.md`) through the production
runtime composition exposed a real integration defect that R1–R7 could not have
seen: the runtime **understood CREATE as a concept but could not carry a CREATE
target end-to-end**. Two independent seams assumed every target already exists
and can be read:

| Seam | Owner | Defect |
|---|---|---|
| **A — target binding** | `execution/strategy.Select` | A declared creation whose phrasing was not in the literal phrase table (`"add a"`) was read as an incomplete resolution → endless clarification. |
| **B — context provenance** | `contextcompiler.ValidateContextProvenance` + the grant-gated re-compilation | The workspace contract requires existing bytes for every requested target, so an absent CREATE target could never compile → the granted path parked. |

Both seams contradicted a contract the runtime **already states** in
`autonomy.preflightExecutionSpec` ("an explicitly named creation target binds it
by statement") and `EvaluatePreflightAdmission` ("an explicitly stated file that
does not exist yet is a legitimate CREATION target"). The fix makes binding,
compilation and admission agree on CREATE. It does **not** widen authority, does
**not** touch `runtime/kernel`, and is proven against the real
`Driver → ExecutorAdapter → RuntimeExecutor → patch → kernelbridge.Apply → kernel`
path.

After the fix, the exact prompt creates the real file through the frozen kernel,
independently observed and `PROVEN`:

```
Run("add file named addtest.md")   → gateway binds [addtest.md]
                                   → admission ADMIT (creation boundary)
                                   → 1 provider staging call
                                   → parks at MUTATION REVIEW
ResumeApprove(...)                 → kernel write proven (Landed)
                                   → addtest.md = "# addtest\n\ncreated through the frozen kernel\n"
                                   → kernelbridge.Observe PROVEN
                                   → objective PROVEN
```

---

## 2. Actual TUI → runtime → kernel execution graph

The production path (verified against source; production order is
**authorize-then-dispatch**, so the capability grant exists before the loop):

```text
cmd/izen/main.go
 └─ compose.Wire                                    internal/runtime/compose/compose.go:500
     ├─ RuntimeExecutor                             compose.go  (single execution authority)
     ├─ ExecutorAdapter                             compose.go
     ├─ autonomy.Driver (app.Autonomous)            compose.go:937
     └─ grant ledger (shared with the autonomy ctrl)
 └─ ui.RunMainDashboardWithApp                      (Bubble Tea program)
     └─ "$prompt <objective>"                        internal/ui/intent_dispatch.go:287
        └─ routePromptDirective → runAutonomyRoutedCmdExplicit
           └─ m.autonomy.Decide(objective)           intent → capability → workspace
              ├─ DecisionAskUser → requestAutonomyProposal   internal/ui/autonomy_proposal.go:185
              │     └─ Execute → m.autonomy.GrantDefault(missing...)   ← AUTHORIZATION
              │        → re-Decide → auto_continue
              └─ executeAutonomyWorkspace             internal/ui/autonomy_route.go
                 └─ handoffExecutionContext           internal/ui/context_boundary.go
                 └─ executeAutonomyViaDriver          internal/ui/autonomous.go:43
                    └─ Driver.Run                     internal/runtime/autonomy/driver.go:587
                       ├─ adapter.Resolve             strategy gateway  ← SEAM A
                       ├─ deriveEvidenceScope
                       ├─ preflightExecutionSpec       driver.go:2042
                       ├─ EvaluatePreflightAdmission   preflight_admission.go:307
                       ├─ syncGrantedWorkspaceContext  grant_barrier.go:257  ← SEAM B
                       └─ observeAndRun → RuntimeExecutor.Execute → invokeMutation
                          → parks at HumanBoundaryApproval
        └─ Alt+A → resumeAutonomousApprove            autonomous.go:241
           └─ authorizeAutonomousApproval             issues MutationAuthorization
              └─ Driver.ResumeApprove                 driver.go
                 └─ RuntimeExecutor.Approve
                    └─ PatchManager.commitThroughKernel   internal/execution/patch.go:565
                       └─ kernelbridge.Apply              internal/kernelbridge/apply.go:265
                          └─ runtime/kernel (Spec + Verifier + evidence)
                    └─ independent re-read + Landed() proof
              └─ observation → verification → objective authority → PROVEN
        └─ handleAutonomousRun → renders runtime state   autonomous.go:472
```

### 2.1 Control-plane boundaries owned by the UI (and by the runtime)

For each boundary the brief asks for: OWNER / INPUT / OUTPUT / AUTHORITATIVE
STATE / DERIVED STATE / EXECUTION IDENTITY / EVIDENCE.

**B1 — prompt → objective compilation** (`intent_dispatch.go:287`,
`autonomy_route.go:287`)

- Owner: UI (routing only). Input: raw human `$prompt …`. Output: an autonomy
  `Trace` (intent/capability/workspace) **or** a direct host message.
- Authoritative: the human objective text. Derived: `Trace.Intent`,
  `Trace.Route`. Identity: none (no execution yet). Evidence: autonomy decision
  record.

**B2 — authorization / capability grant** (`autonomy_proposal.go:185`,
`executeAutonomyProposal`)

- Owner: **runtime** (`internal/autonomy` engine + grant ledger). Input: the
  proposal's missing capability set. Output: a grant bound to a scope.
- Authoritative: the grant. Derived: re-run decision (`auto_continue`).
- Identity: none. Evidence: grant ledger + `capability.granted` event.
- The UI **cannot mint** a grant; it can only request one.

**B3 — context boundary / spec freeze** (`context_boundary.go`)

- Owner: UI boundary → `contextspec` pipeline. Input: conversation + objective.
  Output: a frozen untrusted `ExecutionSpec`. Authoritative: nothing (untrusted).
  Derived: `lastExecutionSpec`. Identity: spec id. Evidence: frozen snapshot
  validation.

**B4 — target clarification** (`autonomous.go` `resumeAutonomousClarify`,
`driver.go:1005 ResumeClarify`)

- Owner: **runtime** (`bindAuthoritativeTargets`, `driver.go:1636`). Input: the
  human-selected option. Output: an updated authoritative target set **and** a
  re-derived scope. Authoritative: `d.req.Targets`. Derived: `d.resolved`,
  `d.scopeDerivation`, `d.scopeResolution`, objective contract, held candidates
  — all invalidated then re-derived (`invalidateDerivedScope`).
- Identity: the run id is preserved across clarification (`runID++` per attempt,
  no second execution). Evidence: canonical re-resolution + derivation record.

**B5 — mutation review / approval** (`autonomous.go:241`, `autonomous.go:668`)

- Owner: **runtime** (`RuntimeExecutor`). Input: Alt+A. Output: a
  `MutationAuthorization` bound to candidate identity + content digest, then
  `ResumeApprove`. Authoritative: the held candidate. Derived: the rendered
  review. Identity: candidate id + patch id. Evidence: candidate freshness and
  digest re-check at the release seam.

**B6 — execution / kernel mutation** (`RuntimeExecutor.Approve` →
`patch.go:565` → `kernelbridge.Apply`)

- Owner: **frozen kernel bridge**. Input: resolved bytes for one target.
  Output: a kernel `Applied` with an execution id and evidence. Authoritative:
  the kernel outcome. Derived: patch transaction state. Identity: kernel
  `ExecutionID`. Evidence: `Landed(target)` requires the kernel's own re-read.

**B7 — completion / TUI result** (`handleAutonomousRun`, `autonomous.go:472`)

- Owner: **runtime** (`execution.ObjectiveCompletionAuthority`). Input: terminal
  loop state + sealed evidence. Output: `completed` / `unsubstantiated` /
  `aborted`; the UI only projects it. The UI never infers success.

> **Determination:** the UI owns *presentation and routing* (B1, B3) and the
> *human-decision plumbing*; the runtime owns *authority, target derivation,
> context, execution and completion* (B2, B4, B5, B6, B7). Where the failure
> occurred, the UI was **not** the owner.

---

## 3. Integration seams

| Seam | Location | Question it answers | Owner |
|---|---|---|---|
| S1 | `execution/strategy.Select` | Is this a CREATE, and where may it act? | strategy gateway |
| S2 | `autonomy.deriveEvidenceScope` | What does workspace evidence add? | autonomy |
| S3 | `autonomy.EvaluatePreflightAdmission` | May the objective act at all? | autonomy |
| S4 | `execution.RuntimeExecutor.RecompileGrantedIntentContext` + `contextcompiler.ValidateContextProvenance` | Does the compiled context satisfy the active contract? | execution / context compiler |
| S5 | `PatchManager.commitThroughKernel → kernelbridge.Apply` | Did the bytes provably land? | kernel bridge |
| S6 | `execution.ObjectiveCompletionAuthority` | Was the objective proven? | execution |

The defect lived in **S1 and S4**; S3 already had the correct CREATE contract and
S5/S6 were never reached before the fix.

---

## 4. First incorrect transition for every failure

Reproduction is deterministic (`internal/runtime/autonomy`, scripted provider,
real `Driver → ExecutorAdapter → RuntimeExecutor`). Workspace: `readme.md` only.
Grant in force (production authorize-then-dispatch order).

### 4.1 Mode 1 — the exact prompt (`add file named addtest.md`), before the fix

```
Run:
  state            = awaiting_human
  resolved.Strategy= human_clarification      ← SEAM A
  resolved.Targets = []                        ← target never bound
  scopeDerivation  = UNIQUE [readme.md]        (evidence, not authority)
  scopeResolution  = REFUSED
  admission        = DISAMBIGUATE [readme.md]
  boundary         = clarify
  providerCalls    = 0

ResumeClarify("addtest.md"):
  req.Targets      = [addtest.md]              ← authoritative request updated
  resolved.Strategy= human_clarification       ← re-resolution refused the absent target
  resolved.Targets = []                        ← derived scope empty again
  admission        = DISAMBIGUATE
  boundary         = inform
  reason           = "workspace context could not be compiled under the granted
                      capabilities: … compiled context does not satisfy the active
                      intent's provenance contract: the compiled context does not
                      carry the requested target(s): addtest.md"   ← SEAM B
```

### 4.2 Mode 2 — `create addtest.md` / `add a file named addtest.md`, before the fix

The gateway bound the target (creation was recognised), but the grant-gated
context gate refused:

```
RecompileGrantedIntentContext([addtest.md], modification, workspace)
  → valid=false, scopeMatched=false, missing=[addtest.md]
  → execution: compiled context does not satisfy the active intent's provenance contract:
     the compiled context does not carry the requested target(s): addtest.md
```

### 4.3 Answers to the twelve brief questions

| # | Question | Answer | Evidence |
|---|---|---|---|
| 1 | `addtest.md` authoritative after clarification? | **Partial** — `d.req.Targets=[addtest.md]`; the *derived* scope stayed empty/REFUSED | Mode 1 log |
| 2 | Operation CREATE or MODIFY? | Before clarification: **MODIFY/UNRESOLVED**; after: **CREATE/RESOLVED** (`objectiveSemantics`) | `DeriveObjectiveSemantics` |
| 3 | Is target existence required during context compilation? | **YES** (the defect) | `ValidateContextProvenance` SCOPE |
| 4 | Is the clarification result written to the authoritative request? | **YES** (`req.Target`/`req.Targets`) | `ResumeClarify` |
| 5 | Is derived state invalidated correctly? | **Invalidated, but re-derivation could not succeed** (gateway refused the absent target) | `invalidateDerivedScope` + Mode 1 |
| 6 | Does the new execution scope contain the clarified target? | Authoritative request **yes**; derived scope **no** | Mode 1 |
| 7 | Does the compiled context represent absent targets? | **NO** (treated as `MissingTargets`) | Mode 2 log |
| 8 | Does `ExecutionSpec` contain the target? | Mode 2: **yes** (`ExplicitTargets=[addtest.md]`, `NOT_FOUND`); Mode 1 after clarify: **no** | `preflightExecutionSpec` |
| 9 | Does the frozen execution context preserve it? | **NO** (compilation refused) | Mode 2 log |
| 10 | Why does context compilation reject a valid CREATE target? | The workspace contract requires admitted bytes; an absent target has none | `provenance.go` |
| 11 | TUI bug, control-plane bug, context contract bug, or wiring bug? | **Control-plane / context-contract bug** (an internal inconsistency), not a TUI bug | see §3 |
| 12 | Does the kernel ever receive the request? | **NO** (0 provider calls in Mode 1; gate park in Mode 2) | logs |

> **First incorrect transition (owner: strategy gateway).** `Select` read
> `"add file named addtest.md"` as an unresolved modification because its
> creation table is a literal phrase list (`"add a"`). The owned correction is
> in `Select`.
>
> **Second incorrect transition (owner: context provenance).** Even when the
> target *was* bound, the workspace contract refused an absent target. The owned
> correction is `IntentContextCreation`.

The kernel was never reached before the fix, so the kernel was not modified.

---

## 5. CREATE proof

**Regression/acceptance:**
`internal/runtime/autonomy/create_target_integration_test.go::TestCreate_DeclaredAbsentTargetCreatesFileThroughKernel`
(+ `TestCreate_GrantedContextAcceptsAnAbsentCreationTarget`,
`TestCreate_WorkspaceContractStillRefusesAnAbsentTarget`,
`internal/contextcompiler/provenance_test.go::TestProvenance_DeclaredCreationAcceptsAnAbsentTarget`,
`TestProvenance_CreationDeclarationDoesNotSatisfyTheWorkspaceContract`,
`internal/execution/strategy/strategy_test.go::TestSelectDeclaredCreationBindsAnAbsentTarget`,
`TestSelectCreationVerbOverExistingTargetStaysModify`).

Recorded chain (after the fix):

```
gateway targets   = [addtest.md]
operation         = CREATE
admission         = ADMIT
provider calls    = 1  (staging only)
before approval   = addtest.md absent
boundary          = approval, targets=[addtest.md]
after approve     = COMPLETED
created bytes     = "# addtest\n\ncreated through the frozen kernel\n"   (os.ReadFile)
kernel observe    = PROVEN, exists=true
objective         = PROVEN
```

---

## 6. MODIFY proof

`internal/runtime/autonomy/acceptance_matrix_test.go::TestAcceptance_ModifyReachesKernelAndProves`

```
Run("change bar to qux @note.txt") → parks at approval; 1 provider call;
                                     bytes unchanged before approval
ResumeApprove(...)                 → note.txt changed; kernelbridge.Observe PROVEN;
                                     objective PROVEN
```

The existing R3-C / truth-matrix / R6 suites remain green and cover the same
lane under failure injection.

---

## 7. READ-ONLY proof

`TestAcceptance_ReadOnlyMutatesNothing`

```
Run("explain the file @note.txt") → COMPLETED; no human boundary; 1 read-only call;
                                    note.txt byte-identical
```

---

## 8. AUTHORIZATION proof

- **Unauthorized approval:** `TestAcceptance_UnauthorizedApprovalLeavesZeroDelta`
  — a real `RuntimeExecutor` with **no** mutation authorization; approval is
  refused, the file is byte-identical, the objective is **not** `PROVEN`, and the
  kernel observation confirms presence/unchanged.
- **Unrelated pre-existing proofs retained:** `TestR3_UnauthorizedTargetCannotReachRuntimeExecutor`,
  `TestR6C_AuthorizationFailureBeforeMutationLeavesZeroDelta`,
  `TestTruthMatrix_ApprovalRejected`.

---

## 9. AMBIGUITY proof

`TestAcceptance_AmbiguousTargetParksBeforeProvider` (and the existing
`TestClarification_*` suite)

```
Run("check this project and rewrite the HTML and CSS") over 6 candidates
  → AWAITING_HUMAN clarify; 0 provider calls; 0 bytes changed
```

Discovery evidence never becomes authority; a human must name the target.

---

## 10. MULTI-STEP proof

- Multi-file real work: `TestAcceptance_PortfolioRedesign_ExecutesRealWork`
  (discover → read → mutate `index.html`/`styles.css`/`script.js` → verify).
- Bounded continuation across steps: R4 (`TestR4_*`) and R5 (`TestR5_*`,
  `TestR5_1_*`) — repeated exhaustion never completes; identical partial state
  does not reopen; objective advancement is the only completion signal.
- All above are within the 211-package green run.

---

## 11. Independent filesystem / process evidence

- **Filesystem:** `os.ReadFile`/`os.Stat` on the real workspace (create/modify
  bytes asserted exactly).
- **Kernel:** `kernelbridge.Observe(...).Proven()` / `.Exists()` — a second,
  independent kernel adjudication over the same root.
- **Kernel write proof:** the mutation only succeeds because
  `PatchManager.commitThroughKernel` requires `applied.Landed(target)`; a write
  the kernel cannot prove is returned as an error and the transaction rolls back.
- **Provider accounting:** exact call counts recorded by the scripted providers.

---

## 12. Kernel reachability evidence

The single write primitive is:

```
internal/execution/patch.go:565  commitThroughKernel
    → kernelbridge.Apply(ctx, root, []Write{...})   internal/kernelbridge/apply.go:265
        → kernel.Spec + capability + verifier
    → if !applied.Landed(target) { error }
```

`kernelbridge` is invoked only through the production mutation path; no test
bypasses it. The CREATE acceptance test confirms the kernel's own observation of
the created file (`Observe → Proven`). The kernel (`runtime/kernel`) is
**unchanged**.

---

## 13. Verification evidence

- `ResumeApprove` → verification ran and the objective authority returned
  `execution.ObjectiveProven` in both CREATE and MODIFY tests.
- Verification failure cannot fabricate `PROVEN` (R6-C truth matrix, retained
  green).

---

## 14. Completion evidence

- Terminal state is `autonomy.RuntimeCompleted` **with reason**
  `"objective satisfied: changed; objective PROVEN by evidence"`.
- `ObjectiveUnprovenMessage` is rendered instead for `RuntimeUnsubstantiated`;
  the UI projects the runtime state and never infers success
  (`autonomous.go:606-638`).

---

## 15. Remaining limitations

1. **Live TUI end-to-end not automated.** The proof drives the production
   `Driver → ExecutorAdapter → RuntimeExecutor → kernelbridge` composition and
   the TUI control-plane routing (`internal/ui` tests), but does not script the
   Bubble Tea event loop with a live model. The live benchmark remains opt-in
   (`IZEN_LIVE_FORENSICS=1`), consistent with R7. Recommendation: run one manual
   `$prompt add file named addtest.md` session with a local model to capture a
   screen-cast trace; no code change is implied.
2. **UI creation phrase-table duplication.** `internal/ui/autonomy_target.go`
   keeps its own `creationRequestKeywords` (also matching `"add a "` but not
   `"add file named"`). It is only used on the **harness** compatibility path
   (`executeAutonomyViaRuntime`, when `autonomousDriver == nil`); production uses
   the driver. Left as-is to avoid a second owner for the same decision; the
   canonical authority is now `strategy.Select`.
3. **CREATE of an arbitrary file is model-content-driven.** The runtime stages
   the creation and requires human approval; it does not synthesize templates
   for arbitrary targets (template creates remain the zero-model
   `DirectDeterministic` path). This is correct, not missing.
4. Recorded R7 limitations 1–10 remain unchanged and non-blocking.

---

## 16. Exact production changes

All changes are minimal, at the identified owners, and no kernel code changed.

| File | Change |
|---|---|
| `internal/execution/strategy/selector.go` | **Seam A.** Evidence-based CREATE: a canonical creation verb over targets that do not exist binds the declared destination (`isDeclaredCreation`). Existing targets are untouched. |
| `internal/contextcompiler/provenance.go` | Added `IntentContextCreation` vocabulary; creation contracts do not require pre-existing material; `hasWorkspaceMaterial` excludes creation-only declarations so a creation declaration can never satisfy a MODIFY contract. |
| `internal/contextcompiler/compiler.go` | `ArtifactRef.Creation`, `CompiledContext.CreationPaths`, truthful creation render (`(new file — declared for creation; no existing content)`), fingerprint + clone coverage. |
| `internal/execution/context_compiler.go` | `recompileIntentContext`/`workspaceFiles` carry absent targets as declared creation destinations when `required == IntentContextCreation`. |
| `internal/execution/executor.go` | Updated the two existing `workspaceFiles` call sites (creation=false). |
| `internal/runtime/autonomy/grant_barrier.go` | The grant-gated re-compilation selects the creation contract when the run's operation is CREATE; every other mutation keeps the workspace contract. |

**Authority impact:** none widened. A create still requires (1) an explicitly
named destination, (2) admission `ADMIT`, (3) a capability grant, (4) human
approval of the staged artifact, (5) kernel proof. The MODIFY contract is
unchanged and a creation declaration cannot satisfy it.

---

## 17. Regression tests

| Test | Pins |
|---|---|
| `TestCreate_DeclaredAbsentTargetCreatesFileThroughKernel` | Full CREATE chain + kernel proof + `PROVEN` |
| `TestCreate_GrantedContextAcceptsAnAbsentCreationTarget` | Creation provenance accepts absent target |
| `TestCreate_WorkspaceContractStillRefusesAnAbsentTarget` | MODIFY contract not weakened |
| `TestProvenance_DeclaredCreationAcceptsAnAbsentTarget` | Compiler creation scope + truthfulness |
| `TestProvenance_CreationDeclarationDoesNotSatisfyTheWorkspaceContract` | Contract separation |
| `TestSelectDeclaredCreationBindsAnAbsentTarget` | Seam A across four phrasings |
| `TestSelectCreationVerbOverExistingTargetStaysModify` | Verb alone is not creation |
| `TestAutonomyCreateObjectiveStagesMutationProposal` (UI) | TUI compiles CREATE → BUILD mutation proposal; nothing written pre-authorization |
| `TestAcceptance_ModifyReachesKernelAndProves` / `_ReadOnlyMutatesNothing` / `_AmbiguousTargetParksBeforeProvider` / `_UnauthorizedApprovalLeavesZeroDelta` | Acceptance matrix |

Run:

```
go test ./...                                   # 211 ok, 0 fail
go test -race ./internal/runtime/autonomy/ -run 'TestCreate_|TestAcceptance_'
go test -race ./internal/execution/strategy/ -run 'TestSelectDeclaredCreation|TestSelectCreationVerb'
go test -race ./internal/contextcompiler/ -run 'TestProvenance_DeclaredCreation|TestProvenance_CreationDeclaration'
```

---

## 18. Final termination decision

```text
TARGETED INTEGRATION DEFECT
  → Seam A (strategy gateway CREATE binding)    FIXED
  → Seam B (context creation provenance)        FIXED
  → finite acceptance matrix re-run            GREEN (211 packages)
  → CREATE + MODIFY reach the frozen kernel    PROVEN
  → kernel modified                            NO
STOP.
```

**INTEGRATION PROVEN for the tested matrix.** The runtime contract was correct;
its connection to CREATE-shaped real work was not. That connection is now proven
end-to-end through the production composition, the kernel is untouched, and the
phase terminates without opening another phase.

---

## 19. Final proof questions

| # | Question | Answer | Evidence |
|---|---|---|---|
| 1 | Can a real human request CREATE through the IZEN TUI? | **YES** | `TestAutonomyCreateObjectiveStagesMutationProposal` (TUI) + driver CREATE test |
| 2 | Does CREATE represent a target that does not yet exist? | **YES** | `CreationPaths` + `IntentContextCreation`; absence is explicit, not a drop |
| 3 | Can that request reach the frozen kernel through production composition? | **YES** | `commitThroughKernel → kernelbridge.Apply → Landed`; file bytes verified |
| 4 | Can a real MODIFY request reach the frozen kernel? | **YES** | `TestAcceptance_ModifyReachesKernelAndProves` |
| 5 | Does unauthorized mutation remain blocked? | **YES** | `TestAcceptance_UnauthorizedApprovalLeavesZeroDelta` |
| 6 | Does ambiguous discovery remain non-authoritative? | **YES** | `TestAcceptance_AmbiguousTargetParksBeforeProvider` (0 calls, 0 bytes) |
| 7 | Does human clarification correctly update authoritative intent? | **YES** | `TestClarification_*` + `ResumeClarify` |
| 8 | Does context recompilation preserve the clarified target? | **YES** (CREATE now included) | creation recompile test |
| 9 | Can a clarified CREATE target survive context compilation? | **YES** | `TestCreate_GrantedContextAcceptsAnAbsentCreationTarget` |
| 10 | Does the TUI display runtime truth rather than infer success? | **YES** | `handleAutonomousRun` projects terminal state; no inferred success |
| 11 | Can a real task produce an independently verifiable workspace change? | **YES** | `os.ReadFile` + `kernelbridge.Observe` |
| 12 | Can a real task reach verification? | **YES** | verifier runs on approve; `ObjectiveProven` |
| 13 | Can a real task reach PROVEN only through evidence? | **YES** | `ObjectiveCompletionAuthority`; R6 verification-failure matrix green |
| 14 | Can a real multi-step objective make bounded progress? | **YES** | `TestAcceptance_PortfolioRedesign_ExecutesRealWork`; R4/R5 |
| 15 | Can cancellation remain truthful through the TUI? | **YES** | R6 cancellation suite unchanged/green |
| 16 | Does the system remain domain-independent? | **YES** | fix is extension/verb-agnostic; no web/model special-casing added |
| 17 | Does the frozen kernel remain unchanged unless a contract is violated? | **YES** | no `runtime/kernel` change; contract violation was control-plane |
| 18 | Can IZEN solve a representative real-world coding task through the actual interface? | **YES** | CREATE + MODIFY through production composition; TUI routing proven |
| 19 | Is the system now demonstrably usable rather than merely internally correct? | **YES** (for the tested matrix) | real file created/changed, verified, `PROVEN` |
| 20 | Can this phase terminate without inventing another phase? | **YES** | see §18 |
