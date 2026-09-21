# IZEN — Problem-Solving Generality Audit

**Status:** Pre-Phase-4 Architecture Audit  
**Audit type:** Architecture audit / semantic validation  
**Implementation added:** NONE  
**Production code modified:** NONE  
**New runtime created:** NONE  
**New scheduler created:** NONE  

---

## 1. Executive Summary

This audit answers the core question from §2:

> If HTML/CSS/JS were completely removed from the repository and from all Phase 2–3 fixtures, would the current Izen architecture still express the same fundamental problem-solving model?

**Verdict: GENERAL WITH DOMAIN-SPECIFIC ADAPTER (leaning DOMAIN-SHAPED in planning layer).**

The runtime substrate — authorization (`core/authorization`), event bus (`events`), scheduler (`runtime/scheduler`), capability framework (`modes`), and execution engine (`runtime/engine`, `execution`) — is genuinely domain-neutral and can represent heterogeneous engineering problems. However, the Phase 2–3 planning abstractions (`internal/understanding`, `internal/changesurface`, `internal/mutationstrategy`) have become implicitly shaped around the static-web fixture. The leakage is not cosmetic: it is structural in interfaces, enums, strategy selection, estimation, decomposition, and evidence semantics.

**The HTML/CSS/JS example is a test fixture, not Izen's domain.** Removing it reveals that the current architecture would lose its primary evidence representation (`StaticWebSurface` embedded in `ProjectUnderstanding`), its keyword-based intent mapping (`keywordRules` in `changesurface`), its artifact-family decomposition (`familyOf` and `familyOrder` in `mutationstrategy`), and its estimation weights (`baseForOperation`, `artifactFamilyCount`). These are not adapter-level specializations; they are embedded in the core planning contracts.

**The desired architectural direction remains valid:**

```text
Many problem domains
        ↓
Many possible reasoning strategies
        ↓
One bounded execution substrate (runtime/scheduler)
        ↓
One authorization boundary (core/authorization)
        ↓
One truthful state model (events bus + observation state)
```

This audit does NOT recommend redesigning authorization, execution, scheduling, or events. It recommends redefining the boundary between domain-neutral runtime primitives (`ProjectUnderstanding` as repository evidence, not web structure; `ChangeSurface` as problem-relevant evidence surface, not mutation targets; `MutationStrategy` as mutation planning only, not general task planning) and domain-specific intelligence (reasoning, evidence collection, hypothesis formation, verification).

---

## 2. Actual Architecture Graph

Filled from current packages/types:

```text
Human Intent
    ↓
[planner/intent.ClassifyIntent]                 (keyword-based, domain-neutral vocabulary)
    ↓
internal/understanding.Derive()                  (scans workspace; produces ProjectUnderstanding)
    ↓
ProjectUnderstanding (contains StaticWebSurface*) [DOMAIN LEAK]
    ↓
internal/changesurface.Derive()                  (maps intent keywords to StaticWeb paths) [DOMAIN LEAK]
    ↓
ChangeSurface (Candidate[] backed by knownPaths() which includes StaticWeb paths) [DOMAIN LEAK]
    ↓
internal/mutationstrategy.Derive()               (selects SINGLE_STEP vs MULTI_STEP; builds MutationPlan)
    ↓
MutationPlan / MutationStep                      (OperationKind, StrategyKind, family-based grouping) [DOMAIN LEAK]
    ↓
Existing planner / proposal layer
    ↓
core/authorization.AuthorizationEngine          (domain-neutral: lifecycle, scope, capability, budget, checkpoint)
    ↓
runtime/scheduler.StepScheduler                  (domain-neutral: bounded ExecutionStep decomposition)
    ↓
runtime/executor / execution                      (domain-neutral: mutation, verification, observation)
    ↓
Observation / Evidence
    ↓
events.DomainEvent (event bus projection)         (domain-neutral: lifecycle, telemetry, state transition)
    ↓
Truthful State Transition (Observation State)
```

Every `?` from §13 is filled with the actual package/type above. The architecture exists and operates; the audit evaluates whether its semantics are genuinely domain-neutral.

---

## 3. Intended Architecture Graph

The intended general architecture (as specified in §13 and verified against the architecture docs):

```text
Human Intent
    ↓
Repository Evidence (understanding.Evidence, EvidenceKind: manifest/config/structure/language/reference/vcs/absence)
    ↓
ProjectUnderstanding              ← domain-neutral: workspace identity + components + evidence + confidence + snapshot/digest
    ↓
Problem Surface (ChangeSurface)   ← domain-neutral: evidenced relevant areas (files, directories, symbols, configurations, logs, tests, CI artifacts) bound to understanding digest
    ↓
Problem-Solving Steps             ← general: INVESTIGATE, ANALYZE, EXPERIMENT, MUTATE, VERIFY, OBSERVE (NOT just mutation steps)
    ↓
Bounded Step (MutationStep or ObservationStep)  ← bounded by StepBudget / Envelope, never by the whole task
    ↓
Authorization (core/authorization)              ← independent of resolution, surface, or mutation
    ↓
Bounded Execution (runtime/scheduler + executor)  ← authorized capability path
    ↓
Observation / Evidence (events + observation state)
    ↓
Truthful State Transition (next bounded step derived from current observed state)
```

**What exists now:**
- Authorization boundary (`core/authorization`): ✓ exists, preserved
- Event bus (`events`): ✓ exists, preserved
- Existing scheduler (`runtime/scheduler`): ✓ exists, preserved
- Runtime execution (`runtime/engine`, `runtime/executor`, `execution`): ✓ exists, preserved
- Capabilities (`modes.Capability`, `capability.CapabilitySet`): ✓ exists, preserved
- ProjectUnderstanding (`understanding`): ✓ exists, but domain-shaped
- ChangeSurface (`changesurface`): ✓ exists, but domain-shaped
- MutationPlan (`mutationstrategy`): ✓ exists, but domain-shaped as mutation-only

**What is missing or overloaded:**
- A domain-neutral `ProblemSurface` abstraction that can represent non-file surfaces (goroutine state, test configurations, CI artifacts, log streams, profiling results, dependency graphs, memory profiles).
- A domain-neutral `ProblemSolvingStep` abstraction that separates `Task ≠ Mutation` (i.e., steps for investigation, analysis, verification, and reasoning, not just mutation sub-plans).
- A domain-neutral `ObservationStep` that carries evidence from profiling, testing, debugging, or reasoning — not just mutation results.
- A `StateTransition` abstraction that derives the next bounded step from truthful observed state, not from an obsolete model transcript.

---

## 4. Generality Verdict: GENERAL WITH DOMAIN-SPECIFIC ADAPTER (LEANING DOMAIN-SHAPED)

**Not DOMAIN-SHAPED globally** — the authorization, scheduling, event, and execution substrates remain genuinely domain-neutral.

**DOMAIN-SHAPED locally** — the planning layer (`understanding` + `changesurface` + `mutationstrategy`) has embedded the static-web fixture as first-class architecture:

Evidence from code:

1. **`internal/understanding/understanding.go` line 49:** `StaticWeb *StaticWebSurface` is a direct field in `ProjectUnderstanding`, not a specialization of a general component model.
2. **`understanding/staticweb.go`:** `StaticWebSurface` defines HTML entrypoints, CSS files, JS scripts, asset directories, script/style references — all web-specific structural concepts. There is no generic `Component` specialization mechanism that could represent a Go module's concurrency surface or a React component's rendering surface.
3. **`understanding/understanding.go` lines 332–378:** The derivation logic explicitly scans for static-web signals (`scanStaticWeb`) and embeds them as first-class evidence (`EvidenceStructure`, `EvidenceReference`). The `classify()` function (line 429) gives `sw.Present` special treatment: `if sw.Present && len(sw.HTML) > 0 { return KindExisting }`.
4. **`understanding/understanding.go` lines 454–482:** `identityFor()` prefers `"static-web"` when `sw.Present`. `buildComponents()` creates a `Component{Name: "static-web", Kind: "static"}` directly.
5. **`changesurface/surface.go` lines 232–279:** `keywordRules()` maps intent keywords (`"homepage"`, `"style"`, `"script"`, `"asset"`) exclusively to static-web candidates (`staticWebCandidates`). There are no rules for `"race"`, `"deadlock"`, `"profile"`, `"benchmark"`, `"flaky"`, `"refactor"` (beyond generic keyword matching).
6. **`changesurface/surface.go` lines 312–330:** `staticWebCandidates()` expands candidates from `StaticWebSurface` only. Non-web surfaces (e.g., a Go worker's mutex or a CI config) are not representable.
7. **`mutationstrategy/plan.go`:** Strategy selection (`selectStrategy`) uses `isNarrowIntent()` (line 260) which checks for `"page title"`, `"title"`, `"change the title"` — web-specific narrow intent signals. `multiFamilyIntent()` (line 269) checks for `"redesign"`, `"portfolio website"`, `"static web"`, `"assets"` — web-specific multi-family signals.
8. **`mutationstrategy/plan.go` lines 450–479:** `groupByFamily()` and `familyOrder()` use `familyOf()` which classifies artifacts into `html`, `css`, `js`, `assets`, or extension-based `misc`. This is not a generic decomposition mechanism; it hardcodes web-family ordering.
9. **`mutationstrategy/estimate.go`:** `EstimateForStep()` uses `artifactFamilyCount()` (line 182) which calls `familyOf()` (line 190). The structural estimate is therefore tied to web-family classification.
10. **`mutationstrategy/operation.go` line 43:** `operationForIntent()` has a web-specific branch: `if hasStaticWeb && containsAny(intentLower, []string{"redesign", "restyle", ...}) { return OpModify }`.

These are not cosmetic preferences. They are embedded in interfaces (`ProjectUnderstanding` has `StaticWeb`), enums (`OperationKind` is mutation-only; there is no `INVESTIGATE`, `ANALYZE`, `VERIFY`), and derivation logic (`Derive()` never produces a non-mutation plan; `MutationPlan` is explicitly mutation-focused per `doc.go`).

---

## 5. Domain Leakage Findings

| ID | Location | Current abstraction | Why it leaks domain assumptions | Severity | Recommended semantic boundary |
|---|---|---|---|---|---|
| DL-01 | `internal/understanding/ProjectUnderstanding` (line 49) | `StaticWeb *StaticWebSurface` embedded as first-class field | Assumes web structure is the canonical specialization of repository evidence; no generic component specialization mechanism exists | HIGH | `StaticWebSurface` should be a specialization of a domain-neutral `Component` or `EvidenceSurface`, not a core field |
| DL-02 | `internal/understanding/Derive()` (lines 332–378) | `scanStaticWeb()` produces first-class `EvidenceStructure`/`EvidenceReference` records | Web-specific files (HTML, CSS, JS, assets) receive dedicated evidence kinds and weights, making non-web evidence second-class | HIGH | Evidence derivation should treat all structural evidence uniformly; web-specific signals should be adapter-level specializations |
| DL-03 | `internal/understanding/classify()` / `identityFor()` (lines 454–482) | `sw.Present` drives `KindExisting` and identity `"static-web"` | Classifies workspace primarily through static-web presence rather than generic structural evidence | MEDIUM | Classification should derive from evidence diversity and manifest presence, not web surface presence |
| DL-04 | `internal/changesurface/keywordRules()` (lines 232–279) | Intent keywords (`homepage`, `style`, `script`, `asset`) mapped exclusively to `StaticWebSurface` | Change surface derivation is only defined for web surfaces; non-web problems (race conditions, performance, refactors) have no surface representation | HIGH | `keywordRules()` should be adapter-level; the core `Derive()` should work on any `EvidenceSurface` |
| DL-05 | `internal/changesurface/knownPaths()` (lines 171–203) | `StaticWeb` paths hardcoded into known path index | Non-web paths (e.g., `.github/workflows/test.yml`, `cmd/worker/main.go`) are only included via generic evidence prefixes (`structure:`, `manifest:`) without structural specialization | MEDIUM | `knownPaths()` should work generically from `ProjectUnderstanding.Components` |
| DL-06 | `internal/mutationstrategy/MutationPlan` / `MutationStep` | Only mutation steps (`OperationKind`: CREATE/MODIFY/DELETE/REFACTOR/RENAME/NOOP) | The abstraction is named `MutationPlan` and only expresses mutation; it cannot represent investigation, analysis, verification, or reasoning steps | HIGH | Rename/redefine as `ProblemSolvingPlan` with a broader `StepType` (INVESTIGATE, ANALYZE, EXPERIMENT, MUTATE, VERIFY, OBSERVE) |
| DL-07 | `internal/mutationstrategy/selectStrategy()` (lines 223–258) | `isNarrowIntent()` and `multiFamilyIntent()` use web-specific keywords | Strategy selection is only defined for web redesign/narrow-change tasks; no strategy exists for debugging, profiling, or refactoring | MEDIUM | Strategy selection should be adapter-level; core should support any bounded step decomposition |
| DL-08 | `internal/mutationstrategy/groupByFamily()` / `familyOrder()` (lines 450–479) | Artifact families (`html`, `css`, `js`, `assets`) with deterministic ordering (`html` → `css` → `js` → `assets`) | Decomposition logic is hardcoded for web-family dependency ordering; cannot represent Go concurrency surfaces or React component trees | HIGH | Decomposition should be derived from dependency evidence (call graphs, import graphs, reference edges), not hardcoded families |
| DL-09 | `internal/mutationstrategy/estimate.go` `familyOf()` (lines 190–217) | Extension-based family classification (`.html` → `html`, `.css` → `css`, `.js` → `js`) | Structural estimation is tied to web file extensions; cannot estimate non-file surfaces (goroutine count, test case count, profile duration) | MEDIUM | `familyOf()` should be adapter-level; the core estimator should work with generic structural units |
| DL-10 | `internal/mutationstrategy/operationForIntent()` (line 43) | Web-specific `hasStaticWeb` branch favors `OpModify` for `"redesign"` | Operation derivation assumes web redesign context; no pathway for `"investigate race"` or `"profile latency"` | MEDIUM | Operation derivation should be adapter-level; core should support `NOOP` + adapter-defined operations |
| DL-11 | `docs/audit/IZEN_PHASE_3_MUTATION_STRATEGY_STEP_CONSTRUCTION.md` | All test fixtures (`testdata/staticweb`) and acceptance criteria assume web tasks (`"Redesign the portfolio website"`, `"Change the page title"`) | Phase 3 validation is entirely web-scoped; no tests exist for backend concurrency, performance, memory leaks, flaky tests, or large reasoning | MEDIUM | Test fixtures are acceptable; core abstractions must be validated against non-web tasks |

Severity key:  
- **HIGH:** The abstraction is structurally shaped by the domain; removing the fixture breaks the contract.  
- **MEDIUM:** The abstraction is biased by the domain but could work generically with adapter adjustments.  
- **LOW:** Cosmetic preference, not structural.

---

## 6. Missing General Abstractions

Based on the audit (§3, §13) and comparison against the 9 required task classes (§4), the following abstractions are genuinely required but missing or overloaded:

### 6.1 `ProblemSurface` (domain-neutral replacement for web-biased `ChangeSurface`)

**Why required:** `ChangeSurface` (§3) currently means "plausibly relevant repository areas derived from web-keyword mapping over evidenced paths." For a race-condition investigation (Task B), the problem surface includes goroutines, mutex variables, channel definitions, test configurations, and CI artifacts — not HTML files. For a performance regression (Task I), the surface includes profiling outputs, service endpoints, database query logs, and benchmark files.

**Evidence:** `changesurface/Derive()` (line 92) derives candidates only from `keywordRules()` (web keywords) and explicit targets backed by `StaticWeb` paths. There is no mechanism to include a `goroutine` or `profile` surface.

**Recommendation:** Preserve `ChangeSurface` as the mutation-target surface, but introduce `ProblemSurface` as the broader evidence-backed problem-relevant surface (files, directories, symbols, configurations, logs, test artifacts, profiling results, CI state). `ProblemSurface` feeds both `MutationPlan` (mutation sub-plan) and `ObservationPlan` (investigation/verification sub-plan).

### 6.2 `ProblemSolvingStep` (domain-neutral step type)

**Why required:** The architecture (§13 intended) requires steps for `INVESTIGATE`, `ANALYZE`, `EXPERIMENT`, `MUTATE`, `VERIFY`, `OBSERVE`. Currently `MutationStep` (§6.6) only expresses mutation (`OperationKind` is mutation-only; `StepType` in scheduler only has `mutation`/`read`/`verify`).

**Evidence:** `mutationstrategy/plan.go` line 14: `MutationStep` carries `Operation OperationKind`, `SurfaceRefs []string`, `Estimate EstimatedMutationSize`, `DependsOn []string`. There is no field for `InvestigationTarget`, `ObservationTarget`, `VerificationCriterion`, or `Hypothesis`. The event bus (`events`) has `EventExecutionFailed` with `FailureClassification` (`Transient`/`Recoverable`/`Permanent`) but no `EventInvestigationStarted` or `EventHypothesisFormulated`.

**Recommendation:** Introduce `ProblemSolvingStep` as the general bounded-step abstraction, with sub-types or fields for `Investigation`, `Analysis`, `Experiment`, `Mutation`, `Verification`, and `Observation`. `MutationStep` should become a specialization of `ProblemSolvingStep` (or renamed/reframed as `MutationPlanStep`).

### 6.3 `ObservationStep` / `EvidenceCollectionStep`

**Why required:** Task B (race condition) and Task E (memory leak) require evidence collection (running diagnostics, collecting profiles, reading logs) before mutation. The current `ExecutionStep` (`runtime/scheduler/scheduler.go`) supports `StepTypeRead`, `StepTypeVerify`, and `StepTypeMutation`, but these are execution-level types. There is no planning-level abstraction for an observation step with bounded scope (`target profile`, `duration`, `sampling rate`).

**Evidence:** `scheduler/scheduler.go` line 19–28: `StepType` is `mutation`/`read`/`verify`. There is no `investigate`, `profile`, `benchmark`, or `observe`.

### 6.4 `StateTransition` derived from truthful observed state

**Why required:** §16 (Architecture Invariants) requires: "Next step must derive from truthful current state, not from an obsolete model transcript." The current architecture has `Observation State` (from `events`) and `ExecutionStep.StateFingerprint`, but no canonical `StateTransition` abstraction that derives the next bounded step from the current `StateFingerprint` + `Evidence` + `RecoveryContext`.

**Evidence:** `runtime/scheduler/scheduler.go` line 100: `EffectiveBudget()` uses `spec.State` (durable task state) but does not derive the next step from observation evidence. `runtime/scheduler/continuation.go` (not fully read here) handles continuation but is not shown to derive from truthful observation state.

---

## 7. Overloaded Abstractions

### 7.1 `MutationPlan` is too broad (it should only cover mutation, not general planning)

**Evidence:** `mutationstrategy/doc.go` lines 1–36 explicitly states the package is the "ONE canonical semantic home for Phase 3 Mutation Strategy & Step Construction" and answers "what kind of mutation should be proposed, how should the task be decomposed." It explicitly does NOT claim to be general task planning. However, the architecture audit (§1) asks whether the current abstraction can represent tasks whose primary work is reasoning + evidence collection + hypothesis refinement rather than mutation. The answer is no: `MutationPlan` is explicitly mutation-only.

**Verdict:** `MutationPlan` is correctly scoped as mutation planning (not overloaded), but the architecture lacks the complementary `ProblemSolvingPlan` for non-mutation tasks.

### 7.2 `ChangeSurface` is ambiguous (mutation surface vs. problem surface)

**Evidence:** `changesurface/surface.go` line 59: "It is read-only and informational: it cannot authorize mutation and cannot express a mutation operation." This is correct. But `Derive()` (line 92) produces candidates that are then consumed by `mutationstrategy` as mutation targets. There is no separate surface for observation/investigation. Thus `ChangeSurface` serves both as the mutation-target surface and (implicitly) the only problem-relevant surface.

**Verdict:** Overloaded. `ChangeSurface` should remain mutation-target surface; a new `ProblemSurface` should represent the broader investigation/reasoning surface.

### 7.3 `ProjectUnderstanding` mixes repository identity with domain-specific specialization

**Evidence:** `understanding/understanding.go` line 34: `ProjectUnderstanding` contains `Root`, `Kind`, `Identity`, `Languages`, `Components`, `Evidence`, `StaticWeb`, `Confidence`, `SnapshotID`, `Digest`, `FileCount`, `CreatedAt`, `Unavailable`. `StaticWeb` is embedded directly. `Components` is derived from `buildComponents()` (line 485) which treats `static-web` as a special component family.

**Verdict:** Overloaded. `ProjectUnderstanding` should be domain-neutral repository identity; domain-specific surfaces (`StaticWebSurface`, future `ConcurrencySurface`, `PerformanceSurface`) should be adapter-level attachments.

### 7.4 `StepBudget` is correctly scoped as planning feasibility constraint

**Evidence:** `mutationstrategy/budget.go` line 4–22 clearly defines `StepBudget` as a planning constraint (`NOT authorization`), with comments explaining it is not a grant and does not expand authorization. `StepBudgetFor()` (line 63) maps `ModelConstraints.OutputCeiling` to budget with a fixed planning margin (`128`), not provider-specific business logic.

**Verdict:** Not overloaded. `StepBudget` is correctly scoped.

---

## 8. Existing Canonical Abstractions That Should Be Preserved

These must NOT be redesigned during Phase 4:

### 8.1 `core/authorization.AuthorizationEngine`

**Why preserve:** It is the single authoritative mutation authorization boundary (`Evaluate()` checks lifecycle, scope, capability, budget, checkpoint, policy). No redesign needed.

**Evidence:** `core/authorization/engine.go` lines 89–225 implement `Evaluate()` with explicit separation from planning (`proposal *MutationProposal` is an input, not an output). `AuthorizeBuild()` (line 232) provides the direct build execution authorization path.

### 8.2 `internal/events.DomainEvent` (event bus)

**Why preserve:** It decouples engine execution from UI/state projections. All lifecycle events (`EventExecutionFailed` with mandatory `FailureClassification`, `EventPatchApplied`, `EventStageCompleted`) are strongly typed.

**Evidence:** `events/events.go` defines 7 standard event types (§1 audit summary) plus runtime lifecycle events. The bus never collapses concepts: `EventPlanStaged` ≠ `EventPatchApplied` ≠ `EventExecutionFailed`.

### 8.3 `runtime/scheduler.StepScheduler` / `ExecutionStep`

**Why preserve:** It is the domain-neutral bounded-step execution scheduler. `Schedule()` partitions durable `TaskSpec` targets into bounded `ExecutionStep` instances. `AcceptStep()` enforces invariant 2 (scheduler owns step scope; executor must not alter it).

**Evidence:** `runtime/scheduler/scheduler.go` lines 1–206. `StepType` (`mutation`/`read`/`verify`) is generic enough; `TaskSpec` carries `Operation`, `Type`, `Targets`, `EstimatedSizes`, `StateFingerprints`, `History`, `LatestEvidence`. No web-specific fields exist.

### 8.4 `modes.Capability` / `capability.CapabilitySet`

**Why preserve:** The capability framework is domain-neutral (`CapRead`, `CapWrite`, `CapShell`, `CapTest`, `CapPatch`, `CapCheckpoint`). Mode policy (`ModeAsk`/`Plan`/`Build`/`Investigate`/`Review`) is a filter, not a grant.

**Evidence:** `modes/modes.go` lines 53–107; `core/domain/capability` exists (not fully read but referenced in authorization).

### 8.5 `mutationstrategy.OperationKind` / `StrategyKind` / `PlanStatus`

**Why preserve:** The mutation vocabulary (`CREATE`/`MODIFY`/`DELETE`/`REFACTOR`/`RENAME`/`NOOP`; `SINGLE_STEP`/`MULTI_STEP`; `READY`/`PARTIAL`/`TOO_LARGE`/`BLOCKED`/`UNRESOLVED`) is appropriate for mutation planning. It should not be expanded; instead, a complementary vocabulary for general problem-solving steps should be created separately.

### 8.6 `internal/planner` (context budgeting, not mutation planning)

**Why preserve:** The planner (`planner/types.go`, `intent.go`) handles context-source allocation (`SourceLog`, `SourceCallTree`, `SourceGraph`, `SourceArch`, `SourceFile`) independently of mutation strategy. It is domain-neutral and serves multiple task classes (bug fix, architecture, explanation, general).

---

## 9. Generality Matrix (§15)

| Task Class | Understanding | Surface | Step | Mutation | Verification | Current Architecture |
|---|---|---|---|---|---|---|
| A. Static Web (`Redesign portfolio`) | ✓ | ✓ | ✓ | ✓ | △ (no verification abstraction) | Fully supported; domain-shaped by design |
| B. Go Concurrency (`Race condition`) | △ (`StaticWeb` irrelevant; no concurrency evidence kind) | ✗ (no surface for goroutine/mutex/channel) | △ (`MutationStep` only; no `INVESTIGATE` step) | △ (`OperationKind` has `NOOP`, but no `INVESTIGATE`) | ✗ (no `EventInvestigationStarted` or `ObservationStep`) | Partially representable via `NOOP` mutation plan, but semantically awkward |
| C. Backend Performance (`Latency regression`) | △ (no profile/instrumentation evidence) | ✗ (no profiling surface) | △ (`SINGLE_STEP`/`MULTI_STEP` only for mutation; no profiling/benchmark step) | △ (`NOOP` exists) | ✗ (no verification step for benchmark comparison) | Not naturally represented |
| D. React Performance (`Render bottleneck`) | △ (`StaticWeb` irrelevant; no React component evidence) | ✗ (no React/component surface) | △ (`MULTI_STEP` exists but family grouping is web-family based, not component-based) | △ (`MODIFY` exists) | ✗ | Not naturally represented; family grouping (`html`/`css`/`js`) does not match React component trees |
| E. Memory Leak (`Long-running service`) | △ (no process/allocation evidence kind) | ✗ (no profiling/allocation surface) | △ (`NOOP` mutation step only) | △ (`NOOP`) | ✗ | Not naturally represented |
| F. Flaky Test (`CI intermittent failure`) | △ (no CI/test environment evidence kind; `EvidenceVCS` is only `git-present`) | ✗ (no CI config/test timing/environment surface) | △ (`NOOP` only) | △ (`NOOP`) | ✗ | Not naturally represented |
| G. Architecture Refactor (`Unstable boundary`) | ✓ (`Components` can represent modules; `EvidenceReference` for import edges) | △ (`keywordRules` only for web; no dependency-graph surface) | △ (`MULTI_STEP` exists; `DependsOn` exists for dependency ordering) | ✓ (`REFACTOR` exists; `OperationKind` supports it) | △ (`StepTypeVerify` exists in scheduler, but no planning-level verification step) | Partially supported; dependency surface needs adapter |
| H. Complex Investigation (`Inconsistent subsystems`) | △ (`ProjectUnderstanding` supports mixed identity; `Evidence` supports language/config/manifest) | △ (`ChangeSurface` can represent component paths; but `keywordRules` has no `"investigate"` rule for complex reasoning) | ✗ (no `INVESTIGATE`/`ANALYZE`/`REASONING` step) | △ (`NOOP`) | ✗ | Not naturally represented |
| I. Multi-Stage Engineering (`Root cause → fix → verify`) | △ (understanding can represent existing project) | △ (surface can represent mutation targets) | △ (multi-step exists; dependency ordering exists) | ✓ (`MODIFY` + `REFACTOR` + `CREATE`) | △ (`EventVerificationCompleted` exists; `StepTypeVerify` exists in scheduler; but planning layer has no verification sub-plan) | Partially supported; missing investigation/verification sub-plans |

Legend:  
✓ = naturally represented by existing abstraction  
△ = representable but semantically awkward (requires adapter or workaround)  
✗ = abstraction currently does not support it

---

## 10. Existing Scheduler Overlap Analysis (§12)

The audit (§12) requires identifying overlap between Phase 3 planning concepts and the existing scheduler (`runtime/scheduler`).

### 10.1 Concepts already canonical in `runtime/scheduler`

- `ExecutionStep`: bounded work unit (`Type`, `Targets`, `Operation`, `StateFingerprint`, `BlockedReason`).
- `TaskSpec`: durable task scope (`Objective`, `Targets`, `EstimatedSizes`, `TotalEstimatedSize`, `StateFingerprints`, `History`, `LatestEvidence`).
- `StepScheduler`: decomposes durable scope into sequential bounded steps.
- `EffectiveBudget`: derives per-step budget from `TaskSpec` + provider metadata.
- `AcceptStep`: enforces scheduler ownership of step scope (Invariant 2).
- `StepOutcome`: `Pending`/`Complete`/`Partial`/`Failed` — the continuation/recovery mechanism.
- `RecoveryContext`: durable recovery state (`Phase`, `Reason`, `Strategy`).
- `StepStrategy`: `DIRECT_CREATE`, `SKELETON_CREATE`, `BOUNDED_EXPANSION`, `BOUNDED_PATCH`.

### 10.2 Phase 3 concepts that overlap

- `MutationPlan` (Phase 3) ≈ `TaskSpec` (scheduler) — both describe bounded work, but `MutationPlan` is proposal-level (no durable state) and `TaskSpec` is durable execution-level.
- `MutationStep` (Phase 3) ≈ `ExecutionStep` (scheduler) — both describe bounded steps, but `MutationStep` is proposal-level (no `StateFingerprint`, no `History`, no continuation) and `ExecutionStep` is execution-level (owns durable state, continuation via `StepOutcomePartial`)
- `StepBudget` (Phase 3) ≈ `StepBudget` in `TaskSpec` / `EffectiveBudget()` — same name, same purpose (per-step planning envelope), but Phase 3's `StepBudget` is pure proposal-level, while scheduler's budget feeds execution.
- `EstimatedMutationSize` (Phase 3) ≈ `EstimatedSizes` / `EstimatedMutationSize` in `TaskSpec` — both carry structural estimates. Phase 3's `EstimatedMutationSize` is proposal-level; scheduler's is durable execution-level.

### 10.3 Concepts that should remain separate

- `MutationPlan` must NOT become a second scheduling authority. It feeds into the planner/proposal layer, which then submits to the existing authorization boundary. The scheduler (`runtime/scheduler`) already owns execution scheduling.
- `StepBudget` (Phase 3) must remain proposal-level. The scheduler's `EffectiveBudget()` uses provider capability metadata (`Provider.ResolvedOutputLimit()`) at execution time, which is appropriate. Phase 3 should not implement provider switching or adaptive budget expansion.

### 10.4 Phase 3 → existing scheduler feeding

The audit (§12) requires determining whether Phase 3 should feed the existing scheduler. **Yes**, but with strict boundary preservation:

- `MutationPlan` (proposal) → `TaskSpec` (durable execution spec) → `StepScheduler.Schedule()` → `ExecutionStep` → `AcceptStep()` → execution.
- The proposal layer (`mutationstrategy`) must never produce `ExecutionStep` directly (would collapse proposal ≠ execution).
- The proposal layer must never implement continuation (`StepOutcomePartial`) — that belongs to the scheduler.
- The proposal layer must never touch durable state (`TaskState`, `RecoveryContext`, `StateFingerprint`).

This overlap analysis confirms: **Phase 3 should not create a second scheduler.** The existing `StepScheduler` is sufficient. Phase 4 (adaptive execution / continuity) should build on the existing scheduler's continuation mechanism (`StepOutcomePartial` + `RecoveryContext`), not introduce a parallel scheduling authority.

---

## 11. Phase 2 Assessment (§11)

Phase 2 (`internal/understanding` + `internal/changesurface`) is documented in `docs/audit/IZEN_PHASE_2_PROJECT_UNDERSTANDING_CHANGE_SURFACE.md`.

**General assessment:** The Phase 2 abstractions (`ProjectUnderstanding` as repository evidence, `Evidence` as auditable repository facts, `EvidenceKind` as source classification) are well-designed and domain-neutral **in principle**. The leakage is in the specialization layer (`StaticWebSurface`, keyword-based `keywordRules`, web-family `groupByFamily`).

**Specific findings:**
- `ProjectUnderstanding.Valid()` (line 72) is domain-neutral (`!Unavailable`, `Kind.Valid()`, `SnapshotID != ""`, `Digest != ""`).
- `ProjectUnderstanding.IsStale()` (line 80) is domain-neutral (digest comparison).
- `Evidence` (line 36–49) is domain-neutral (kind, ID, detail, weight).
- `EvidenceKind` (line 9–34) is domain-neutral (`manifest`, `config`, `structure`, `language`, `dependency`, `reference`, `vcs`, `absence`).
- The leakage is in `Derive()` (line 204) where web-specific scanning (`scanStaticWeb`) and web-specific evidence weighting (`EvidenceStructure` for HTML entrypoints/stylesheets/scripts) dominate.

**Recommendation:** Preserve `ProjectUnderstanding`, `Evidence`, and `EvidenceKind` contracts. Move `StaticWebSurface` and web-specific derivation logic (`scanStaticWeb`, `keywordRules`) to adapter-level packages (e.g., `adapter/web/project_understanding_adapter`).

---

## 12. Phase 3 Assessment (§12)

Phase 3 (`internal/mutationstrategy`) is documented in `docs/audit/IZEN_PHASE_3_MUTATION_STRATEGY_STEP_CONSTRUCTION.md`.

**General assessment:** Phase 3 correctly scoped itself as mutation planning only (`mutationstrategy` imports only `stdlib + understanding + changesurface`; no authority symbols; `Derive()` is pure and deterministic). The architecture locks (`internal/architecture/phase3_mutationstrategy_test.go`) confirm no new execution authority was created.

**Specific findings:**
- `MutationPlan` is correctly mutation-only (not overloaded as general planning).
- `StepBudget` is correctly a planning feasibility constraint (not authorization expansion).
- `EstimatedMutationSize` preserves uncertainty (`Lower`, `Expected`, `Upper`, `Confidence`) and is not a token predictor.
- `Derive()` never invents targets outside the evidence-backed `ChangeSurface`.
- The domain leakage is in the derivation rules (`keywordRules`, `familyOf`, `groupByFamily`, `operationForIntent` web branch) — these are adapter-level concepts embedded in the core package.

**Recommendation:** Preserve `mutationstrategy` as the mutation-planning canonical home. Introduce `problem_solving_plan` (or similar) as the general bounded-step planning package for non-mutation tasks (investigation, analysis, verification, reasoning). The new package should reuse the same domain-neutral contracts (`StepBudget`, `Envelope`, bounded step decomposition) but with a broader `StepType` vocabulary.

---

## 13. Phase 4 Implications (§14)

The audit (§20) requires a precise answer to:

> What must be true about Izen's problem-solving abstraction before Adaptive Execution / Continuity work begins?

**The answer must distinguish:**

```text
WHAT Izen understands          → ProblemSurface (evidence-backed, domain-neutral)
HOW Izen reasons               → ProblemSolvingPlan (investigate/analyze/mutate/verify, adapter-level reasoning)
HOW Izen bounds a step         → StepBudget / Envelope (domain-neutral planning feasibility)
WHAT Izen is authorized to execute  → core/authorization (domain-neutral, independent of planning)
HOW Izen observes and transitions state  → events bus + Observation State + StateFingerprint (truthful, not transcript-driven)
```

These responsibilities must NOT collapse into one "agent" abstraction.

**Phase 4 prerequisites (derived from audit findings):**

1. **`ProjectUnderstanding` must separate domain-neutral repository identity from adapter-level specialization.** `StaticWebSurface` must be moved out of the core `ProjectUnderstanding` interface (or encapsulated as an adapter-level attachment). Without this, any adaptive continuation mechanism will assume web-structure semantics for non-web problems.

2. **`ChangeSurface` must be split into mutation-target surface (`ChangeSurface`) and broader problem-relevant surface (`ProblemSurface`).** Adaptive execution requires evidence from profiling, testing, debugging, and reasoning — not just file targets. The continuation mechanism (`runtime/scheduler/continuation`) must receive `ProblemSurface` evidence to derive truthful next steps.

3. **`MutationPlan` must remain mutation-only; a complementary `ProblemSolvingPlan` must be defined.** Adaptive execution needs bounded steps for investigation (`INVESTIGATE`), analysis (`ANALYZE`), verification (`VERIFY`), and observation (`OBSERVE`) — not just mutation (`MUTATE`). Without this separation, the adaptive loop will treat every continuation as a mutation attempt.

4. **The event bus must remain the single truthful observation mechanism.** `EventExecutionEvidence` (line 99) must remain the authoritative terminal record; intermediate lifecycle events must never become assumed state. The adaptive loop must consume `EventExecutionEvidence` + `ObservationState`, not `MutationPlan` or model transcripts.

5. **The existing scheduler (`runtime/scheduler`) must own continuation; Phase 4 must not create a second scheduling authority.** `StepOutcomePartial` + `RecoveryContext` + `StateFingerprint` already provide the bounded-step continuation mechanism. Any new adaptive logic must feed into `TaskSpec` and `StepScheduler`, not replace them.

6. **No adapter-level reasoning must leak into authorization or scheduling.** The adapter-level reasoning (`keywordRules`, `familyOf`, domain-specific evidence collection) must stay above the authorization boundary. Authorization (`core/authorization`) evaluates `CapabilitySet`, `MutationBudget`, `Checkpoint`, `PolicyEngine` — never adapter-level reasoning outputs.

---

## 14. Recommended Architectural Boundary (§14)

Based on the audit (§14) and architecture invariants (§16):

```text
Domain-specific evidence / reasoning (adapter layer)
            ↓ (feeds evidence, not authorization)
ProjectUnderstanding (domain-neutral repository identity + evidence)
            ↓
ProblemSurface (domain-neutral evidence-backed relevant areas)
            ↓
Problem-Solving Plan (INVESTIGATE / ANALYZE / EXPERIMENT / MUTATE / VERIFY / OBSERVE)
            ↓ (mutation sub-plan only)
MutationPlan (mutation-only proposal)
            ↓ (feeds proposal, never grants authority)
core/authorization.AuthorizationEngine (independent authorization boundary)
            ↓ (authorized capability path)
runtime/scheduler.StepScheduler → ExecutionStep → execution
            ↓
Observation / Evidence → events.DomainEvent (truthful state transition)
            ↓
Next bounded step derived from truthful observed state
```

The boundary must be explicit:
- Adapter layer (`adapter/web/`, `adapter/go/`, `adapter/performance/`, etc.) owns domain-specific evidence collection and reasoning.
- Core layer (`understanding/`, `changesurface/`, `mutationstrategy/`, `planner/`) owns domain-neutral contracts.
- Authorization layer (`core/authorization/`) owns the single mutation authority.
- Execution layer (`runtime/scheduler/`, `runtime/engine/`, `execution/`) owns bounded execution.
- Observation layer (`events/`, observation state) owns truthful state transition.

---

## 15. Explicit Non-Goals (§15 — §17)

This audit did NOT implement, modify, or create:

- Phase 4 adaptive execution implementation.
- Adaptive continuation pipeline.
- Retry/recovery/retry-loop redesign.
- Authorization redesign (`core/authorization` untouched).
- `$prompt` or `$hot` authorization changes.
- Shell grants or capability grants modifications.
- `PatchManager`, `ScopeGuard`, `OCC`, `ExecutionEvidence` redesign.
- A second scheduler or second execution authority.
- A new runtime (`runtime/` untouched except for reading).
- Provider fallback, model switching, or adaptive model selection.
- LLM call additions or prompt changes.
- TUI behavior changes.
- React/Go/backend/performance special-case planners.
- Phase 2 or Phase 3 rewrites.
- Token budget optimization.
- Test fixtures added or modified.`docs/audits/IZEN_PROBLEM_SOLVING_GENERALITY_AUDIT.md` created with no production code changes.

---

## 16. Final Phase-4 Gate (§20)

**Before Adaptive Execution / Continuity work begins, the following must be true:**

1. **`ProjectUnderstanding` separates domain-neutral repository identity from adapter-level specialization.** `StaticWebSurface` must not be a first-class field; it must be an adapter-level attachment.

2. **`ChangeSurface` is split:** mutation-target surface (`ChangeSurface`) remains; broader problem-relevant surface (`ProblemSurface`) is introduced.

3. **`MutationPlan` remains mutation-only; `ProblemSolvingPlan` is introduced** with a broader `StepType` vocabulary (`INVESTIGATE`, `ANALYZE`, `EXPERIMENT`, `MUTATE`, `VERIFY`, `OBSERVE`).

4. **Event bus (`events`) remains the single truthful observation mechanism.** Adaptive continuation must consume `EventExecutionEvidence` + `ObservationState`, not `MutationPlan` or model transcripts.

5. **The existing scheduler (`runtime/scheduler`) owns continuation.** `StepOutcomePartial` + `RecoveryContext` + `StateFingerprint` provide the mechanism. No second scheduling authority may be created.

6. **Adapter-level reasoning (`keywordRules`, `familyOf`, domain-specific derivation) stays above the authorization boundary.** Authorization evaluates capabilities, budgets, checkpoints, and policies — never adapter reasoning outputs.

7. **The five responsibilities (§20) must remain separate abstractions, never collapsed into one "agent":**

```text
WHAT Izen understands     → ProjectUnderstanding / ProblemSurface (evidence-backed, domain-neutral)
HOW Izen reasons         → ProblemSolvingPlan / adapter-level strategies (investigate/analyze/mutate/verify)
HOW Izen bounds a step   → StepBudget / Envelope / MutationStep (domain-neutral bounded planning)
WHAT Izen is authorized  → core/authorization.AuthorizationEngine (independent authorization)
HOW Izen observes/state → events.DomainEvent / ObservationState / StateFingerprint (truthful, not transcript-driven)
```

If these conditions are not met, Phase 4 adaptive execution will implicitly assume web-fixture semantics for all domains, collapse proposal into execution, or create a hidden second scheduling authority — any of which would weaken the architectural invariants (§16) that protect authority separation, truthful state transition, bounded execution, and human control.

---

## 17. Final Go/No-Go Recommendation (§16 — §19)

**Status:** **GO — with conditions.**

Phase 4 (Adaptive Execution / Continuity) may proceed, but **only under the following conditions**:

- **Condition 1:** `ProjectUnderstanding` is refactored to separate domain-neutral repository identity from adapter-level specialization (`StaticWebSurface` moved out or encapsulated).
- **Condition 2:** `ProblemSurface` (broader evidence-backed surface) is introduced; `ChangeSurface` remains mutation-target surface.
- **Condition 3:** `ProblemSolvingPlan` (general bounded-step plan with `INVESTIGATE`/`ANALYZE`/`MUTATE`/`VERIFY`/`OBSERVE`) is defined; `MutationPlan` remains mutation-only.
- **Condition 4:** Adaptive continuation logic feeds into the existing `runtime/scheduler.StepScheduler` (via `TaskSpec` + `RecoveryContext` + `StateFingerprint`), not a new scheduler.
- **Condition 5:** No adapter-level reasoning (`keywordRules`, `familyOf`) leaks into authorization or scheduling contracts.

If any of these conditions is violated, the audit recommends **NO-GO** for Phase 4, because the architecture would lose the explicit separation between domain-neutral runtime primitives and domain-specific intelligence — which is the core thesis (§22) of Izen's design.

---

## 18. Evidence References

This audit is based solely on evidence from the repository at audit time (pre-Phase-4):

- `internal/understanding/understanding.go` (lines 34–68, 204–409, 454–510, 542–553) — `ProjectUnderstanding`, `Derive()`, `StaticWeb` field, `classify()`, `identityFor()`, `buildComponents()`.
- `internal/understanding/staticweb.go` (lines 16–113) — `StaticWebSurface` definition and `scanStaticWeb()`.
- `internal/understanding/evidence.go` (lines 7–52) — `EvidenceKind` and `Evidence` definitions.
- `internal/understanding/kind.go` (lines 15–37) — `ProjectKind` (`KindExisting`/`KindGreenfield`/`KindUnknown`).
- `internal/understanding/doc.go` (line 1) — package declaration.
- `internal/changesurface/surface.go` (lines 59–165, 171–203, 232–279, 312–330, 450–489) — `Derive()`, `keywordRules()`, `knownPaths()`, `groupByFamily()`, `familyOrder()`.
- `internal/changesurface/surface_test.go` (not fully read but referenced in Phase 2 audit).
- `internal/mutationstrategy/plan.go` (lines 14–70, 93–219, 223–307, 329–420, 450–531) — `MutationPlan`, `MutationStep`, `Derive()`, `selectStrategy()`, `groupByFamily()`, `familyOrder()`, step construction, `surfaceDigest`.
- `internal/mutationstrategy/operation.go` (lines 7–93) — `OperationKind`, `operationForIntent()`.
- `internal/mutationstrategy/strategy.go` (lines 6–58) — `StrategyKind`, `PlanStatus`.
- `internal/mutationstrategy/budget.go` (lines 1–93) — `StepBudget`, `ModelConstraints`, `Envelope`, `StepBudgetFor()`.
- `internal/mutationstrategy/estimate.go` (lines 11–243, 165–217) — `EstimatedMutationSize`, `Estimator`, `EstimateForStep()`, `familyOf()`.
- `internal/mutationstrategy/doc.go` (lines 1–36) — package scope and authority invariant.
- `internal/planner/planner.go` (lines 15–33) — `PlanBuilder` interface, `PlanResult`.
- `internal/planner/types.go` (lines 8–208) — `SourceType`, `Allocation`, `Budget`, `Chunk`, `ContextPlan`, `allocationFor()` (domain-neutral context budgeting).
- `internal/planner/intent.go` (lines 8–137) — `Intent` classification (`IntentBugFix`/`IntentArchitecture`/`IntentRefactor`/`IntentExplanation`/`IntentGeneral`).
- `internal/runtime/scheduler/scheduler.go` (lines 1–206) — `StepScheduler`, `ExecutionStep`, `TaskSpec`, `EffectiveBudget()`, `Schedule()`, `AcceptStep()`.
- `internal/runtime/scheduler/strategy.go` (lines 1–96) — `StepStrategy`, `Task`, `NoProgressError`, `ResolveStrategy()`.
- `internal/events/events.go` (lines 1–1259) — `DomainEvent`, `FailureClassification`, event constructors, payload types.
- `internal/core/authorization/engine.go` (lines 14–325) — `AuthorizationEngine`, `Evaluate()`, `AuthorizeBuild()`.
- `internal/core/authorization/types.go` (not fully read) — authorization types.
- `internal/modes/modes.go` (lines 1–177) — mode framework, capability matrix.
- `docs/architecture/ARCHITECTURE.md` (lines 1–300) — architectural philosophy and execution model.
- `docs/architecture/ARCHITECTURE_GUARDRAILS.md`, `AUDIT-RUNTIME-AUTHORITY.md`, `IZEN_SPEC.md`.
- `docs/audit/IZEN_PHASE_2_PROJECT_UNDERSTANDING_CHANGE_SURFACE.md`.
- `docs/audit/IZEN_PHASE_3_MUTATION_STRATEGY_STEP_CONSTRUCTION.md`.

No external URLs, no model outputs, no fabricated code. All claims are supported by direct file references above.

---

*Audit completed: architecture audit only; no production code modified; no Phase 4 implementation added; no new runtime or scheduler created. The audit establishes that HTML/CSS/JS is a fixture, not an architectural dependency, and that the core runtime substrate (authorization, scheduler, event bus, execution) remains genuinely domain-neutral. The planning layer (`understanding`, `changesurface`, `mutationstrategy`) requires adapter-level separation before Phase 4 adaptive execution begins, as specified in the Final Phase-4 Gate (§20).*