# IZEN PHASE 3 — MUTATION STRATEGY & STEP CONSTRUCTION

**Status:** implemented. `go build ./...` passes, `go test ./...` green, `golangci-lint` 0 issues on all touched packages.

## A. Scope

Phase 3 implemented the semantic planning layer between the existing Change Surface and the existing authorization/execution boundaries, without touching Phase 0/1/2 authorities:

```text
Human Intent
  → Repository Evidence
    → Project Understanding (internal/understanding)
      → Change Surface (internal/changesurface)
        → Mutation Strategy / Mutation Plan (internal/mutationstrategy — this phase)
          → Existing Planner/Proposal
            → Existing Authorization (Phase 1 grants)
              → Existing Execution (RuntimeExecutor)
```

New code (all pure, deterministic, read-only derivation — no filesystem mutation, no shell, no model call, no autonomous continuation):

- `internal/mutationstrategy/` — the ONE canonical home for Phase 3:
  - `doc.go` — canonical package declaration and authority invariant
  - `operation.go` — `OperationKind` proposal vocabulary (`CREATE`/`MODIFY`/`DELETE`/`REFACTOR`/`RENAME`/`NOOP`)
  - `strategy.go` — `StrategyKind` (`SINGLE_STEP`/`MULTI_STEP`) and `PlanStatus` (`READY`/`PARTIAL`/`TOO_LARGE`/`BLOCKED`/`UNRESOLVED`)
  - `estimate.go` — `EstimatedMutationSize{Lower, Expected, Upper, Confidence}` + `Estimator` (structural, uncertainty-preserving, not token-based)
  - `budget.go` — `StepBudget`, `ModelConstraints`, `StepBudgetFor`, `Envelope` (planning constraints, never authorization)
  - `plan.go` — `MutationStep`, `MutationPlan`, `PlanOptions`, `Derive()` (pure `intent+understanding+surface → plan`)
- Tests: `internal/mutationstrategy/plan_test.go` (acceptances + EST + STRAT families)
- Architecture locks: `internal/architecture/phase3_mutationstrategy_test.go`
- This report: `docs/audit/IZEN_PHASE_3_MUTATION_STRATEGY_STEP_CONSTRUCTION.md`

## B. Existing Architecture

Audit of the candidate homes requested in §3 and the reuse decision:

| Existing component | Reachability | Verdict |
|---|---|---|
| `planner/scope` (`ScopeDecision`, `StrategySinglePass`/`StrategyDAG`, `StructuralMetrics`) | production (preflight planner hooks) | **excluded** — strategy-adjacent but wrong abstraction: it classifies the structural complexity of ONE file's DOM/AST (depth/density/fan-out) into `SinglePass` vs `DAG` decomposition of that one target. Phase 3 decomposes a *Change Surface* (many paths, many artifact families) into bounded *proposal steps*. Reusing it would force a second filesystem discovery inside the plan and conflate file-local topology with task-level mutation strategy. Kept untouched. |
| `execution/strategy.go` (`FileMutationStrategy`, `StrategyForFile`) | production (prompt selection) | **excluded** — prompt-mode selector (`STRATEGY_NEW_FILE` vs `STRATEGY_EXISTING_FILE`) for `new_file` vs `existing_file` system prompts. Single-file, execution-adjacent, not task-level decomposition. Kept untouched. |
| `engine/strategy` (`Goal`, `PlanningStrategy`, `DetermineGoal`) | production (microkernel goal derivation) | **excluded** — microkernel `Intent+PlanningContext → Goal` for the plan pipeline; it produces outcome/targets/constraints, not bounded mutation steps with budgets/estimates/dependencies. Reuse would pull engine context/planning dependencies into a pure planning package. Kept untouched. |
| `runtime/scheduler` (`StepScheduler`, `ExecutionStep`, `EffectiveBudget`, `Strategy` resolution) | production (Phase 6.2 bounded execution) | **excluded** — this IS an execution scheduler: it partitions durable `TaskSpec` targets into `EffectiveBudget`-fitting steps, tracks `StateFingerprint`, `RecoveryContext`, `StepOutcomePartial` continuations, and owns `AcceptStep` (Invariant 2). Phase 3 must be a *proposal* derivation that never schedules execution, never continues on `length`, never touches durable state. Reusing it would collapse Proposal ≠ Execution. Kept untouched. Contains the reference for `EstimatedMutationSize`, `StepBudget` *names* but with execution semantics (overflow handling via `StepOutcomePartial`) that Phase 3 explicitly must not implement. |
| `planner/planner.go` (`Planner`, `PlanResult`, `ContextPlan`) | production (context planning) | **excluded** — context-source budgeting for LLM prompts, not mutation decomposition. Kept untouched. |
| `internal/understanding` + `internal/changesurface` | production (Phase 2) | **inputs, not homes** — understanding is the lifecycle-bound evidence truth; change surface is the evidenced relevant-area set. Neither may own mutation operations/budgets without collapsing `Understanding ≠ Surface ≠ Strategy`. Used strictly as read-only input. |
| `core/authorization`, `execution`, `runtime/executor`, `patch`, `boundary/scopeguard`, `llm`, `provider` | Phase 1 authority / execution | **never imported** — would mint a second authority. Pinned by architecture sweep. |

No existing type could host "bounded mutation proposal over an evidenced surface" without collapsing Proposal/Execution or mixing file-local topology with task-level strategy. One small canonical package was therefore created instead of `MutationStrategyEngine`/`StepPlanner`/`StepScheduler`/`AdaptivePlanner` orchestration layers. Nothing existing was moved, renamed, or re-responsibilitied.

## C. Mutation Strategy

Canonical type: `mutationstrategy.StrategyKind` in `internal/mutationstrategy`.

```go
type StrategyKind string
const (
    StrategySingleStep StrategyKind = "SINGLE_STEP"
    StrategyMultiStep  StrategyKind = "MULTI_STEP"
)
```

Semantics: answers "what high-level approach should be used to satisfy this intent over this Change Surface?" Selected deterministically from evidence:

- candidate count, artifact-family span (`html`/`css`/`js`/`assets`/extension-normalized `misc`), DIRECT-vs-RELATED balance, narrow-vs-broad intent signals (e.g. `"Change the page title"` → narrow single-DIRECT → `SINGLE_STEP`; `"Redesign the portfolio website"` → multi-family → `MULTI_STEP`), and the existing `StaticWeb` surface.
- `StrategySingleStep` is chosen when the surface is single-candidate, or narrow intent with exactly one `DIRECT` candidate, or two candidates within one family — never by inventing steps.
- `StrategyMultiStep` is chosen when the surface spans `≥2` artifact families or carries a broad multi-family intent signal (`redesign`/`overhaul`/`portfolio website`/…) or exceeds the per-step file envelope — proven by static-web multi-step and large-task acceptances.
- The vocabulary is intentionally minimal (`SINGLE_STEP`/`MULTI_STEP` only); `SEQUENTIAL`/`DEPENDENCY_ORDERED` are expressed as `DependsOn` edges inside the multi-step plan rather than as additional strategy constants, avoiding a premature DAG runtime.
- Strategy selection never modifies authorization; it only labels the proposal.

## D. Mutation Plan

Canonical types: `mutationstrategy.MutationPlan` and `mutationstrategy.MutationStep` in `internal/mutationstrategy` (the same package — one semantic home for both).

```go
type MutationStep struct {
    ID          string               // stable "step-01"
    SurfaceRefs []string             // subset of ChangeSurface candidates, never invented
    Operation   OperationKind        // proposal semantic (CREATE/MODIFY/…)
    Estimate    EstimatedMutationSize // structural, uncertainty-preserving
    Budget      StepBudget           // planning envelope, not a grant
    Envelope    Envelope             // model-output envelope for this step
    DependsOn   []string             // predecessor step IDs (HTML → CSS/JS)
    Rationale   string               // deterministic why (e.g. "stylesheet — references HTML structure; depends on step-01")
    Evidence    []string             // provenance signal keys
    Status      PlanStatus           // READY/PARTIAL/TOO_LARGE/…
}

type MutationPlan struct {
    Strategy            StrategyKind
    Status              PlanStatus             // READY/PARTIAL/TOO_LARGE/BLOCKED/UNRESOLVED
    Steps               []MutationStep
    Estimate            EstimatedMutationSize  // aggregate, monotonic vs max step, lowest confidence
    Evidence            []string               // derivation provenance
    UnderstandingDigest string                 // binds to exact understanding
    SurfaceDigest       string                 // binds to exact surface (content-hash of status+digest+paths+evidence)
    IntentSummary       string                 // truncated intent
    UnresolvedReason    string                 // when not READY
    CreatedAt           time.Time
}

type PlanOptions struct {
    ModelConstraints ModelConstraints
    StepBudget       StepBudget
    MaxFilesPerStep  int
}

func Derive(intent string, u understanding.ProjectUnderstanding, surface changesurface.ChangeSurface, opts PlanOptions) MutationPlan
```

Lifecycle:

- `Derive` is the single pure entry point. It consumes the three semantic inputs (`intent`, `u`, `surface`) plus optional `PlanOptions` (model ceiling, fallback budget, per-step file cap). It never re-discovers the repo, never invents paths, never mints a grant, never executes.
- Guard 1: `u.Valid()` and `surface.UnderstandingDigest == u.Digest` — otherwise `UNRESOLVED` (stale surface never trusted).
- Guard 2: `surface.Status == UNRESOLVED` or no candidates → `UNRESOLVED` with `UnresolvedReason` (never fabricated targets).
- Operation is derived from lowercased intent verb families (`delete`→`DELETE`, `rename`→`RENAME`, `refactor`→`REFACTOR`, `create`→`CREATE`, `redesign`→`MODIFY`, else conservative `MODIFY`).
- Budget is `StepBudgetFor(ModelConstraints, fallback)` — model ceiling minus a fixed planning margin; `Envelope` mirrors it with `RequiresBoundedPatch` when `OpModify`.
- Steps are built by `buildSteps`: `SINGLE_STEP` → one step (narrow intent with single `DIRECT` uses only `DIRECT` refs so the estimate stays small; otherwise all sorted paths coalesced), `MULTI_STEP` → artifact-family coalescing in deterministic order `html → css → js → assets → misc`, chunked by `MaxFilesPerStep` when set, with dependencies wiring (`css`/`js` depend on `html` steps; assets independent).
- Each step's `Estimate` is recomputed with its `DependsOn` count (dependency weight).
- `TOO_LARGE` is raised when any step's `Expected` exceeds its `Budget.MaxOutputTokens`; if the step still references `>1` file and no explicit per-step cap was requested, one finer retry (`MaxFilesPerStep=1` family-chunked) is attempted — if that now fits, it is adopted; otherwise the offending step is marked `TOO_LARGE` and the plan returns `StatusTooLarge` with `UnresolvedReason` ("decomposition required, not auto-continuation"). No silent continuation, no second model call.
- Otherwise surface status maps to plan status (`RESOLVED`→`READY`, `PARTIAL`→`PARTIAL`), with per-step `Status` set accordingly.
- `SurfaceDigest` is a content hash of `status+understandingDigest+intentSummary+sortedPaths+evidence`; combined with `UnderstandingDigest` it provides semantic invalidation without redesigning OCC — a plan derived from stale surface is never silently considered current.

## E. Estimated Mutation Size

Canonical type: `mutationstrategy.EstimatedMutationSize`.

```go
type EstimatedMutationSize struct {
    Lower      int     // conservative lower bound
    Expected   int     // central expectation
    Upper      int     // conservative upper bound
    Confidence float64 // [0,1], low when evidence is weak
}
func (e EstimatedMutationSize) Valid() bool
func (e EstimatedMutationSize) Fits(b StepBudget) bool
func (e EstimatedMutationSize) Exceeds(b StepBudget) bool
```

Estimator: `Estimator{DefaultStepEnvelope int}` with `DefaultEstimator() → 4096`.

- `EstimateForStep(surfaceRefs, op, dependencies, hasStaticWeb)` is deterministic and structural: `expected = nFiles*baseForOperation(op) + families*40 + dependencies*20` (where `baseForOperation` is `CREATE=180`, `REFACTOR=140`, `MODIFY=100`, `RENAME=80`, `DELETE=40`, `NOOP=0`; `families = artifactFamilyCount(paths)` via `familyOf` (`html`/`css`/`js`/`assets`/`ext`); `dependencies` is each `DependsOn` edge at ~20 units). This is intentionally not `chars/4` — it is a bounded-planning signal whose magnitude stays comparable across small/large tasks. Spread is `expected/3` for `CREATE`/`REFACTOR`, `expected/4` for `nFiles≥3`, else `expected/6`, clamped to `≥4`, giving `lower = expected - spread`, `upper = expected + spread` — so every estimate explicitly preserves uncertainty.
- `Confidence` is `0.85` for single-file single-family, downgraded by operation (`REFACTOR`/`NOOP` `−0.15`), multi-family (`≥3` `−0.15`), and high fan-out (`deps>2` `−0.10`), rounded to two decimals — small single-file modify is high confidence, multi-family create/refactor is low.
- `EstimateForPlan(steps)` aggregates as `sum(lower/expected/upper)` with `lowest confidence` — monotonic (`aggregate ≥ max step`) and uncertainty-carrying, with no claim of exact additivity (decomposition may overlap).
- `Valid()` requires `lower ≤ expected ≤ upper` and `confidence∈[0,1]`; `Fits` checks `Expected ≤ Budget.MaxOutputTokens` (zero budget = unbounded for planning; `TOO_LARGE` is about `Expected`, not `Upper`, so planning is not spuriously pessimistic).

## F. Step Construction

`Derive` → `buildSteps`. Decomposition rule: **bound the step, not the task.**

- Bad design it avoids: `User Task → one giant model call → OUTPUT_EXHAUSTED`.
- Desired design it implements:

```text
User Task
  → Change Surface
    → Mutation Plan
      → Step 1 (bounded: HTML structure)
      → Step 2 (bounded: CSS styling, depends on Step 1)
      → Step 3 (bounded: client-side behavior, depends on Step 1)
      → Step 4 (bounded: assets, independent)
        → each step independently proposable → existing authorization → existing execution
```

- A step is small enough that `expected mutation + required context + required reasoning + required output` fits the step envelope; if it does not, the result is `TOO_LARGE` (decomposition required), not auto-continuation.
- Steps are constructed by evidence, not by byte count: `1000 output tokens` does not imply a step is small; a step touching `index.html`+`styles.css`+`script.js` is structurally larger than a 500-line single-file step. The estimator considers `mutation surface + artifact count + dependency structure + operation type + content magnitude`, while token limits remain a downstream provider concern.
- Each step carries bounded surface refs (always `⊆ change surface`), operation, structural estimate, planning budget, envelope, dependencies, and rationale/evidence — sufficient to describe a bounded proposed unit without re-discovering the repo.
- Decomposition is evidence-based: `"Change the page title"` → one bounded `MODIFY index.html` (proven not to over-decompose); `"Redesign the portfolio website"` → family-coalesced multi-step with explicit `DependsOn` — never artificial inflation.

## G. Step Budget

Canonical types: `mutationstrategy.StepBudget`, `mutationstrategy.ModelConstraints`, `mutationstrategy.Envelope`.

```go
type StepBudget struct {
    MaxOutputTokens  int `json:"max_output_tokens"`
    MaxFiles         int `json:"max_files"`
    MaxMutationUnits int `json:"max_mutation_units,omitempty"`
}
type ModelConstraints struct {
    OutputCeiling     int    `json:"output_ceiling"`
    ContextWindow     int    `json:"context_window,omitempty"`
    CapabilityProfile string `json:"capability_profile,omitempty"`
}
type Envelope struct {
    MaxOutputTokens      int  `json:"max_output_tokens"`
    RequiresBoundedPatch bool `json:"requires_bounded_patch"`
    MaxFiles             int `json:"max_files"`
}
func StepBudgetFor(mc ModelConstraints, fallback StepBudget) StepBudget
```

- `StepBudget` is the minimal per-step planning constraint (model work allocation), not a grant. A planner cannot enlarge a grant by increasing it; a model cannot enlarge a grant by requesting more; a budget exceeding the authorized envelope is rejected downstream at the existing Phase 1 gateway — tested by `TestPhase3_StepBudgetDoesNotExpandAuthorization`.
- `Estimate ≠ Budget`: the estimate describes the expected structural mutation; the budget describes the allowed work envelope for one bounded step. `EstimatedMutationSize → Step Budget` is the planning check; provider-specific token accounting remains out of scope.
- No hardcoded `MaxTokens - 150` or OpenRouter magic numbers: when `ModelConstraints.OutputCeiling` is known, `StepBudgetFor` maps it minus a fixed documented planning margin (`128`) into `MaxOutputTokens`; otherwise the fallback `DefaultStepEnvelope` (4096) is used. Capability-aware adaptive budgeting belongs to a later adaptive-execution phase, not Phase 3.
- `Envelope` is the stable model-output contract for one step (`RequiresBoundedPatch` when `OpModify` so downstream `B2`/`B4` can gate correctly).

## H. Dependency Semantics

- Dependencies are explicit `MutationStep.DependsOn []string` (step IDs). The plan does NOT implement a scheduler; it only describes constraints.
- For static-web surfaces, the deterministic wiring is: `html` steps have no deps; `css`/`js` steps depend on all `html` step IDs (`"stylesheet — references HTML structure; depends on step-01"`); `assets` are independent (`"asset directory — independent of markup structure"`). This is rationale-visible and evidence-backed.
- A step with unresolved dependency information is never silently treated as independent — `DependsOn` is explicitly empty vs populated, and the re-estimated step carries the dependency weight. `TestSTRAT03_DependencyPreserved` pins that `css`/`js` depend on `html`.

## I. Model Constraints

Phase 3 may consume already-known model capability metadata as input to planning, never as adaptive selection.

- `PlanOptions.ModelConstraints` carries `OutputCeiling` (the known `max_output` ceiling mapped into planning units), optional `ContextWindow`, and an opaque `CapabilityProfile` label — all read-only.
- The only allowed use is the `TOO_LARGE` determination: `StepBudgetFor` → per-step envelope → `Estimate.Exceeds(Budget)` → `TOO_LARGE` with `"decomposition required, not auto-continuation"`. The plan never silently switches models, increases limits, or splits-and-executes — it reports a bounded planning failure.
- `TestPhase3_ModelConstraintsDoNotExpandAuthorization` proves capability metadata cannot mint authority.

## J. Boundary Separation

```text
Mutation Strategy
≠ Mutation Plan
≠ Step Budget
≠ Authorization
≠ Execution
```

- `StrategyKind` answers "what approach should be proposed?" — not what is permitted.
- `MutationPlan` describes what should be changed and how it should be decomposed, with per-step operations/estimates/budgets/dependencies — it never grants permission. There is no path `MutationPlan → Grant` without the existing human authorization boundary: `Intent → Understanding → Surface → Plan → Authorization → Execution` remains one-way, tested structurally (`internal/mutationstrategy` imports only `stdlib + understanding + changesurface`; no `execution`/`authorization`/`patch`/`scopeguard`/`provider`/`llm`; no `Authorize`/`Grant`/`Approve`/`Apply`/`Mutate`/`Commit` symbols) and behaviorally (`Derive` leaves the workspace byte-identical, produces no approval surface, and plan-level large budgets still fail the `IntentGateway.Gate` `ScopeProvenance` check).
- `EstimatedMutationSize` and `StepBudget` are planning signals/constraints, each incapable of expanding `ScopeProvenance` — pinned by `TestPhase3_EstimateDoesNotExpandAuthorization` / `TestPhase3_StepBudgetDoesNotExpandAuthorization` / `TestPhase3_ModelConstraintsDoNotExpandAuthorization` and by the structural sweep that mutationstrategy never touches authority surfaces.
- `Change Surface` remains the sole planning input boundary: `Derive` never re-discovers the repo (only consumes `understanding`+`surface`); phony targets (`dashboard.tsx`) are dropped, never invented (`TestPhase3_PlanCannotInventTargetsOutsideSurface`).
- Phase 1 and Phase 2 invariants remain pinned (`TestPhase3_Phase1AuthorizationBoundaryIntact`, `TestPhase3_Phase2UnderstandingStillValid`, plus the untouched `internal/understanding`/`internal/changesurface` suites).

## K. Tests

All new Phase 3 tests pass; all existing Phase 1/2 tests remain green.

| ID | Test (file) | What it proves | Result |
|---|---|---|---|
| **Accept** | `TestAccept_StaticWebRedesignIsMultiStep` | `testdata/staticweb` + `"Redesign the portfolio website."` → `MULTI_STEP` `READY` with bounded family steps, provenance, digest binding, preserved dependencies | pass |
| **Accept** | `TestAccept_SmallChangeIsSingleStep` | `"Change the page title."` → `SINGLE_STEP` single-step, bounded, dependency-free, small estimate < redesign aggregate | pass |
| **Accept** | `TestAccept_LargeTaskIsMultiStepWithBoundedSteps` | multi-file, multi-family large task → `MULTI_STEP` with `≥2` bounded steps each carrying estimate/budget/envelope/rationale and no invented paths | pass |
| EST-01 | `TestEST01_SmallModificationSmallEstimate` | single small modify → small structural estimate (`<500` band) | pass |
| EST-02 | `TestEST02_MultipleArtifactsLargerEstimate` | multi-artifact aggregate > small single-file | pass |
| EST-03 | `TestEST03_CreateAndModifyDistinguishable` | `CREATE` vs `MODIFY` estimates distinguishable (`CREATE > MODIFY`) | pass |
| EST-04 | `TestEST04_EstimateNotTokenCount` | estimate ≠ `bytes/4` token count; two-family `css+js` > single `html` due to family weight, not bytes | pass |
| EST-05 | `TestEST05_EstimatePreservesUncertainty` | every step preserves `lower < expected < upper` and `confidence ∈ (0,1)`; `UNRESOLVED` carries reason | pass |
| EST-06 | `TestEST06_StepExceedingEnvelopeIsTooLarge` | tiny `StepBudget{MaxOutputTokens:10}` → `TOO_LARGE` with reason and offending step marked `TOO_LARGE` | pass |
| EST-07 | `TestEST07_LargeTaskDecomposedFitsEnvelope` | default envelope: large-task decomposition keeps every step `Expected ≤ Budget` (bounded decision size, not max decomposition) | pass |
| STRAT-01 | `TestSTRAT01_SingleArtifactChangeIsSingleStep` | small single-artifact change → `SINGLE_STEP` | pass |
| STRAT-02 | `TestSTRAT02_MultiArtifactChangeIsMultiStep` | multi-artifact redesign → `MULTI_STEP` | pass |
| STRAT-03 | `TestSTRAT03_DependencyPreserved` | `css`/`js` steps depend on `html` step ID | pass |
| STRAT-04 | `TestSTRAT04_StrategyDoesNotCreateAuthorization` | strategy selection creates no grant (structural: plan type has no grant method; behavioral: no approval surface) | pass |
| STRAT-05 | `TestSTRAT05_StrategyDoesNotInventTargets` | all `SurfaceRefs` are `⊆ surface candidates`; unevidenced `dashboard.tsx` never appears | pass |
| STRAT-06 | `TestSTRAT06_UnknownSurfaceIsUnresolved` | `UNKNOWN` understanding or empty/stale surface → `UNRESOLVED` with reason, zero steps | pass |
| Digest | `TestDigestBinding` | plan binds `UnderstandingDigest` and `SurfaceDigest` | pass |
| Op | `TestOperationKindValid` | all six `OperationKind` values valid, fake invalid | pass |
| Pure | `TestPureDerivation` | `Derive` leaves the filesystem byte-identical | pass |
| Structural | `TestEstimateStructuralNotByteBased` | `a.js+b.js` > `index.html` structurally, with `confidence<1` | pass |
| ID | `TestStepIDStable` | stable `step-01` IDs, no duplicates, every step carries rationale+evidence | pass |
| ARCH | `TestPhase3_CanonicalHomeExists` | canonical `internal/mutationstrategy` exists, rival engines do not | pass |
| ARCH | `TestPhase3_MutationStrategyNeverAuthorizes` | mutationstrategy imports only `stdlib+understanding+changesurface`; no authority symbols | pass |
| ARCH | `TestPhase3_NoNewExecutionAuthority` | source contains no `RuntimeExecutor`/`PatchManager`/`continuation`/`retry`/`exec.Command`/`WriteFile` etc. | pass |
| AUTH-01/02/03/04/05 | `TestPhase3_*DoesNotExpandAuthorization` | `Strategy`/`Plan`/`Estimate`/`Budget`/`ModelConstraints` never expand `ScopeProvenance` | pass |
| AUTH-06/07 | `TestPhase3_NoNewExecutionAuthority` + `TestPhase3_PlanNeverMintsAuthorityNorMutates` | planner never invokes canonical executor; no new execution authority exists | pass |
| AUTH-08/09 | `TestPhase3_Phase1AuthorizationBoundaryIntact` / `TestPhase3_Phase2UnderstandingStillValid` | Phase 1/2 locks remain green (`go test ./...` fully green) | pass |
| REG | full `go test ./...` | all 30+ packages green | pass |
| REG | `go build ./...` | builds with no errors | pass |
| REG | `golangci-lint run ./internal/mutationstrategy/... ./internal/architecture/...` | 0 issues | pass |

## L. Non-Goals

Explicitly confirmed Phase 3 did NOT implement (no such symbols/paths exist in `internal/mutationstrategy`, pinned by the `phase3_mutationstrategy_test.go` sweep and by `go test ./...` remaining fully green on Phase 1/2):

- Adaptive Intelligence, adaptive model selection, provider fallback/switching
- Continuation pipeline (`if finish_reason == length → call model again`)
- Auto-recovery / retry orchestration / provider-specific retry loops
- Model switching or autonomous continuation
- `$prompt` / `$hot` authorization changes or new authorization framework
- New execution authority (`RuntimeExecutor` is still the sole authority)
- `PatchManager` redesign, `ScopeGuard` redesign, `OCC` redesign
- Shell authorization changes, test/build authorization changes
- New capability vocabulary beyond `ModelConstraints{OutputCeiling, CapabilityProfile}`
- `ExecutionEvidence` redesign, full agent loop, token streaming, TUI redesign, headless authority convergence

If any of these appeared necessary during implementation, the work stopped and the dependency was documented instead — none arose; the bounded planning check (`TOO_LARGE`) naturally exposes the need for a later AEI/continuation phase without implementing it.

## M. Remaining Risks

1. **Keyword-derived strategy is heuristic.** `selectStrategy`'s `isNarrowIntent`/`multiFamilyIntent` phrase lists are conservative and evidence-gated (family count still decides), but non-English or domain-specific intents degrade gracefully: multi-candidate surfaces with few families and no broad keywords fall back to `MULTI_STEP` only when `n≥3`; narrow intents with a single `DIRECT` stay `SINGLE_STEP` with only `DIRECT` refs (so the estimate stays small) — intended, but planners must handle `PARTIAL`/`UNRESOLVED` without expecting exact plan shapes.
2. **No planner/execution consumption wired.** `MutationPlan → ExecutionStep` is currently available but uncalled; wiring it later must preserve the one-way `Plan → Authorization → Execution` edge (no planner-owned `AcceptStep` bypass).
3. **Estimate is structural heuristic, not a predictor.** The family/operation/dependency-weighted estimator preserves uncertainty but is not a token predictor; downstream `B2` preflight (`bytes/4 × multiplier`) remains the authority on feasibility vs the real `max_output` ceiling.
4. **Surface digest is size-derived, not content-hash.** `surfaceDigest` hashes `status+understandingDigest+intentSummary+sortedPaths+evidence` — same-size path changes with identical evidence may not rotate the digest; a later phase may adopt a content hash for `StaticWeb` file contents, but the lifecycle seam (`UnderstandingDigest`+`SurfaceDigest`) already supports swapping the function.
5. **Headless divergence (Phase 1 §J) persists.** Untouched by Phase 3; `Derive` is headless-deterministic but headless convergence remains for a later phase.
