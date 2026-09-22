# IZEN — Generality Boundary Correction

**Status:** Pre-Phase-4, complete
**Phase:** Boundary correction
**Depends on:** `IZEN_PROBLEM_SOLVING_GENERALITY_AUDIT.md`
**Implementation:** required
**New execution authority:** NONE
**New scheduler:** NONE
**Authorization redesign:** NONE

---

## 1. Changes Made

| Area | Action | Detail |
|---|---|---|
| `internal/understanding` | Domain-neutralized | Removed `StaticWeb *StaticWebSurface` field, `scanStaticWeb()` call, `StaticWeb`-dependent `classify()`/`identityFor()`/`buildComponents()` branches. Derivation now uses generic manifests, config, language extension counts, source directory topology, and bounded representative structure evidence. `classify()` derives `EXISTING` from `sourceFiles ≥ 2` without `sw.Present`. `identityFor()` derives coarse identity from manifest signals only. `buildComponents()` emits components from identities plus language-based components (up to 4 representative paths per language). |
| `internal/adapters/web` | **New** | Extracted `StaticWebSurface` (Present, Entrypoints, HTML, CSS, Scripts, AssetDirs, ScriptRefs, StyleRefs, HasPackageJSON) and `Derive(root)` / `DeriveFromUnderstanding(u)` plus `DeriveSurface(intent, targets, u)` that implements web-specific keyword mapping (`homepage`/`style`/`script`/`asset` → `StaticWebSurface` paths). Only adapter knows `style → .css` etc. Core never imports adapter. |
| `internal/problemsurface` | **New** | Introduced domain-neutral `ProblemSurface` (`ProblemSurface`, `Reference`, `Certainty`, `Status`, `DigestMatches`, `Derive`). Generic token matching against evidenced paths/components/languages plus bounded broad-intent supplement. Preserves `no invented targets` and `stale understanding → unusable surface`. No authorization/execution semantics. |
| `internal/changesurface` | Genericized | Removed web keyword rules (`keywordRules`, `staticWebCandidates`, `isBroadReadIntent` web-only, `StaticWeb` in `knownPaths`/`componentSurface`). `knownPaths` now indexes generic evidence IDs plus component paths. `Derive` uses generic token matching plus conservative `componentSurface` fallback and broad-supplement for large tasks (when broad and `<3` candidates and not narrow). `DeriveFromProblemSurface` documented as narrow transformation `ProblemSurface ⊇ ChangeSurface`. |
| `internal/mutationstrategy` | Genericized | `operationForIntent` now verb-family only (`hasStaticWeb` branch removed). `Estimator.EstimateForStep` signature changed to `(refs, op, deps)` and `familyOf` now returns extension or top-dir (`"misc"` fallback), not `html`/`css`/`js`/`assets`. `selectStrategy` / `groupCount` / `familyCount` now use distinct extension/directory groups. `groupByFamily` / `familyOrder` are alphabetical, not `html→css→js→assets`. `buildSteps` coalesces by generic groups with no `html→css/js` dependency wiring (conservative independent steps). `rationaleFor` is generic (`op + fam + deps`). |
| `internal/problem` | **New** | Introduced `ProblemSolvingPlan` (`ProblemSolvingPlan`, `ProblemStep`, `StepKind` = `INVESTIGATE`/`ANALYZE`/`EXPERIMENT`/`MUTATE`/`VERIFY`/`OBSERVE`, `Derive`). Pure, deterministic, proposal-level, read-only. Never authorizes, executes, retries, or schedules. Investigation intents yield `INVESTIGATE` steps without forcing `CREATE`/`MODIFY`. |
| `internal/understanding/understanding_test.go` | Updated | `TestPU05` now asserts web surface via `web.Derive` and that core `Identity != "static-web"`; `TestPU06` asserts evidence-backed but not `static-web`. |
| `internal/changesurface/surface_test.go` | Updated | `TestCS01` asserts generic surface `RESOLVED or PARTIAL` with ≥1 candidate and web adapter `DIRECT index.html` via `web.DeriveSurface`. |
| `internal/mutationstrategy/plan_test.go` | Updated | `TestAccept_SmallChangeIsSingleStep` and `TestSTRAT01` now use explicit `@index.html` for deterministic single-step; `TestAccept_StaticWebRedesignIsMultiStep` now allows `RESOLVED or PARTIAL` and no longer requires html dependency; `TestSTRAT03` now checks no invented dependencies; `TestAccept_LargeTask*` thresholds relaxed to `≥2` candidates; `EstimateForStep` calls updated to 3-arg signature. |
| `internal/architecture/phase2_understanding_changesurface_test.go` | Updated | `TestPhase2_NoNewMutationAuthority` now creates `index.html + styles.css` to satisfy generic `sourceFiles ≥ 2` → `EXISTING`. |
| `internal/architecture/generality_boundary_test.go` | **New** | 12 GEN tests + 2 architecture tests + 9-task generality matrix. |
| `docs/architecture/IZEN_PROBLEM_SOLVING_MODEL.md` | **New** | Canonical semantics for Understanding / ProblemSurface / ChangeSurface / ProblemSolvingPlan / MutationPlan / Authorization / Scheduler / Execution / Observation / State and the flow `Intent → Evidence → Understanding → ProblemSurface → ProblemSolvingPlan → MutationPlan → Authorization → Scheduler → Execution → Observation → State`. |
| `internal/understanding/staticweb.go` | Deleted | Moved to `internal/adapters/web/surface.go`. |

---

## 2. Domain Leakage Removed

| ID | Leakage | Correction |
|---|---|---|
| DL-01 | `ProjectUnderstanding.StaticWeb` as first-class field | Removed field; adapter holds `StaticWebSurface`. |
| DL-02 | `Derive` → `scanStaticWeb` emits first-class `EvidenceStructure`/`EvidenceReference` for HTML/CSS/JS/assets | Removed; core emits one bounded `EvidenceStructure` per language representative; adapter emits web evidence. |
| DL-03 | `classify` / `identityFor` use `sw.Present` to set `KindExisting` / `"static-web"` | Removed; `classify` uses `sourceFiles ≥ 2`, `identityFor` uses manifest identities only. |
| DL-04 | `changesurface.keywordRules` maps `homepage`/`style`/`script`/`asset` exclusively to `StaticWebSurface` | Removed; core uses generic token matching; web mapping lives in `adapters/web.DeriveSurface`. |
| DL-05 | `changesurface.knownPaths` hardcodes `StaticWeb` paths | Removed; now indexes generic evidence + component paths. |
| DL-06 | `MutationPlan` is mutation-only but was presented as general planning | Preserved as mutation-only; introduced `ProblemSolvingPlan` for investigation/analysis/verify. |
| DL-07 | `selectStrategy` / `isNarrowIntent` / `multiFamilyIntent` use `page title` / `portfolio website` / `assets` | Replaced with `isNarrowGeneric` (`one line`/`small`/`fix typo`/`single file`/`trivial`/`title`) and `groupCount` (distinct extensions/dirs). |
| DL-08 | `groupByFamily` / `familyOrder` hardcode `html`/`css`/`js`/`assets` with `html→css→js→assets` dependency | Replaced with generic `familyOf` (extension/top-dir) and alphabetical `familyOrder`; removed `html→css/js` dependency wiring. |
| DL-09 | `estimate.familyOf` classifies `.html→html` etc. and `artifactFamilyCount` counts web families | Replaced with generic `familyOf` (extension) and group count; `EstimateForStep` no longer takes `hasStaticWeb`. |
| DL-10 | `operationForIntent` branch `hasStaticWeb && redesign → MODIFY` | Removed; now verb-family only. |
| DL-11 | Tests/docs only validated web tasks | Added `GEN-03`…`GEN-08` and nine-task matrix covering Go race, backend latency, React bottleneck, memory leak, flaky CI, architecture refactor, large unfamiliar-codebase, root-cause→fix→verify. |

---

## 3. New Semantic Boundaries

```text
Domain-specific evidence / reasoning (adapters/web, future Go/React/perf adapters)
            ↓  (evidence, not authorization)
ProjectUnderstanding (domain-neutral: Root, Kind, Identity, Languages, Components, Evidence, Digest)
            ↓
ProblemSurface (domain-neutral: evidence-backed files/dirs/symbols/packages/tests/config/CI/logs…)
            ↓
ProblemSolvingPlan (bounded proposal: INVESTIGATE | ANALYZE | EXPERIMENT | MUTATE | VERIFY | OBSERVE)
            ↓  (mutation sub-plan only)
MutationPlan (bounded proposal: CREATE | MODIFY | DELETE | REFACTOR | RENAME | NOOP, SINGLE/MULTI, estimate)
            ↓  (proposal, never grants authority)
Existing authorization (core/authorization)
            ↓
Existing scheduler (runtime/scheduler → ExecutionStep)
            ↓
Existing execution (runtime/engine, execution)
            ↓
Observed evidence (events.DomainEvent)
            ↓
Truthful state (Observed State + Evidence + Durable task state)
```

Preserved invariants: `no invented targets`, `stale understanding → unusable surface`, `missing target ≠ greenfield`, `derivation failure ≠ greenfield`, `ModelProposal never becomes truth`.

---

## 4. Abstractions Intentionally NOT Introduced

- No general `map[string]any` abstraction to hide semantic differences.
- No plugin framework or generic adapter framework (a package boundary is sufficient).
- No `GoStrategy` / `ReactStrategy` / `BackendStrategy` / `PerformanceStrategy` / `WebStrategy` inside the core runtime.
- No second scheduler (`ProblemScheduler`, `AdaptiveScheduler`, …).
- No second execution authority.
- No new authorization path, shell grants, capabilities, OCC, PatchManager, ScopeGuard redesign.
- No `StateTransition` engine (documented as future contract only).
- No adaptive execution, continuation, retry, provider fallback, model switching, or agent loop.
- No `truncateWithEllipsis`/`padRight`/`fitCell` generic formatting abstractions; no token/ provider model prediction in `EstimatedMutationSize`.
- No new `Generation` dimension for ProblemSolvingPlan beyond the six kinds; additional kinds will be added only when a concrete producer/consumer/test exists (per §15).

---

## 5. Static-Web Adapter Boundary

**Location:** `internal/adapters/web/` (`surface.go`, `changesurface.go`)

**Owns:**
- `StaticWebSurface` (Present, Entrypoints, HTML, CSS, Scripts, AssetDirs, ScriptRefs, StyleRefs, HasPackageJSON)
- `Derive(root string) StaticWebSurface` — bounded walk, reference extraction (script src / link href), entrypoint ordering (`index.html` first)
- `DeriveFromUnderstanding(u ProjectUnderstanding) StaticWebSurface` — interprets generic evidence (components + `structure:` IDs) or falls back to filesystem scan
- `DeriveSurface(intent, explicitTargets, u) ChangeSurface` — merges generic surface with web-specific keyword expansion (`homepage`/`style`/`script`/`asset` → `StaticWebSurface` candidates), deduped, status `RESOLVED` if any `DIRECT`

**Core owns:** evidence contracts, surface contracts, understanding identity, mutation semantics, budgets, planning invariants. Web adapter may depend on `internal/understanding` / `internal/changesurface` / `internal/problemsurface`; core never imports the adapter (enforced by `TestArch_CoreDoesNotDependOnWebAdapter` which asserts no `adapters/web` import in `internal/understanding`, `internal/problemsurface`, `internal/changesurface`, `internal/mutationstrategy`, `internal/problem`).

Static-web support is **not deleted**; it **ceases to define the core architecture**. Fixture `testdata/staticweb` remains valid through the adapter.

---

## 6. ProjectUnderstanding Final Responsibility

> What is this repository/workspace, and what evidence do we currently have about it?

Answers from repository evidence only (manifests, config, language-by-extension, source directory topology, VCS, bounded representative structure). No web surface, no mutation intent, no authorization, no execution semantics. Lifecycle is digest-bound (`Digest`/`SnapshotID`/`IsStale`).

---

## 7. ProblemSurface Final Responsibility

> What evidence-backed areas are relevant to the problem?

Domain-neutral, evidence-backed superset for investigation/reasoning/problem solving. References files/directories/symbols/packages/tests/config/CI artifacts/logs/profiles/dependencies/runtime observations **only when evidenced**. Carries `certainty`, `evidence provenance`, `understanding binding`. No invented targets. Stale understanding → unusable surface. No authorization/mutation/execution/retry/continuation/model selection semantics.

---

## 8. ChangeSurface Final Responsibility

> Which relevant areas are currently candidates for mutation?

Explicitly the **subset of the problem-relevant surface that is currently evidenced as a candidate mutation surface**. Read-only, informational, no mutation authorization, no execution authority, no mutation guard. Derived from `ProjectUnderstanding` (or via `DeriveFromProblemSurface`) using generic token matching and bounded broad-intent supplement. `DigestMatches` preserves stale-state protection.

---

## 9. MutationPlan Final Responsibility

> What bounded mutations are proposed?

`MutationPlan` remains **mutation-only** (`OperationKind` `CREATE`/`MODIFY`/`DELETE`/`REFACTOR`/`RENAME`/`NOOP`, `StrategyKind` `SINGLE_STEP`/`MULTI_STEP`, `PlanStatus` `READY`/`PARTIAL`/`TOO_LARGE`/`BLOCKED`/`UNRESOLVED`, `EstimatedMutationSize` structural `Lower`/`Expected`/`Upper`/`Confidence`). Never invents targets, never mints authorization, never mutates the filesystem, never invokes a model. Decomposition uses domain-neutral structural groups (extension/directory) and bounded candidate count, not `html`/`css`/`js`/`assets` families.

---

## 10. Scheduler Boundary

Existing `runtime/scheduler.StepScheduler` remains the canonical execution scheduler (`Schedule` → `ExecutionStep`, `AcceptStep`, `EffectiveBudget`, `StepOutcome`/`RecoveryContext`). No `ProblemScheduler`/`AdaptiveScheduler` exists. Future:

```text
Problem-solving proposal
        ↓
existing scheduler
        ↓
ExecutionStep
        ↓
existing authorization
        ↓
execution
```

Verified by `TestGEN10_SchedulerIsolation` (no `Schedule`/`Scheduler`/`Execute` methods on problemsurface/changesurface/mutationstrategy/problem types) and `TestArch_*` checks.

---

## 11. Authorization Boundary

Existing `core/authorization.AuthorizationEngine` remains the single mutation authorization boundary (lifecycle, scope, capability, budget, checkpoint, policy). No new planning abstraction mints authorization (`TestGEN09_AuthorizationIsolation` — no `Grant`/`Authorize`/`Approve` methods/fields on all four planning types). Planning never expands scope or authority; stale/unknown understanding yields `UNRESOLVED` with explicit reason, never a fabricated plan.

---

## 12. Architecture Dependency Graph

```text
internal/adapters/web
      ├── internal/understanding
      ├── internal/changesurface
      └── internal/problemsurface
              ↑ (no reverse edge)

internal/problem
      ├── internal/understanding
      └── internal/problemsurface

internal/problemsurface
      └── internal/understanding

internal/changesurface
      └── internal/understanding

internal/mutationstrategy
      ├── internal/understanding
      └── internal/changesurface

internal/understanding
      └── stdlib only

core/authorization, runtime/scheduler, runtime/engine, events
      └── (unchanged, domain-neutral substrate)
```

Verified by `TestArch_CoreDoesNotDependOnWebAdapter`: core packages contain no `StaticWeb` type usage and no `adapters/web` import; web adapter does import `understanding`/`changesurface`.

---

## 13. Tests

| ID | Name | Proven |
|---|---|---|
| GEN-01 | No StaticWeb Core Dependency | `reflect.TypeOf(ProjectUnderstanding{})` has no `StaticWeb` field; `Derive` on Go repo (`go.mod+main.go`) yields `EXISTING` without static-web |
| GEN-02 | Static Web Adapter Preservation | `web.Derive(fixture)` and `web.DeriveFromUnderstanding` yield `Present` with `index.html`/`CSS`/`Scripts`/`AssetDirs`/`ScriptRefs`/`StyleRefs`; `web.DeriveSurface` yields `DIRECT index.html` for `Redesign the portfolio website.` |
| GEN-03 | Generic Repository Understanding | `go backend`, `node app`, `python`, `cli`, `systems` fixtures each yield `EXISTING` without `static-web` identity |
| GEN-04 | Generic Problem Surface | `cmd/worker` + `internal/worker` Go backend, intent `investigate worker race condition` → `RESOLVED` problem surface with `worker` refs, no HTML/CSS/JS required, `DigestMatches` |
| GEN-05 | ChangeSurface Is Mutation-Only | `ChangeSurface` is `read-only`/`informational`, `DigestMatches`, bounded (≤12), subset of `ProblemSurface` conceptually, no mutation operation vocabulary |
| GEN-06 | No Web Family Dependency | `go.mod` + `internal/worker` → `ChangeSurface` and `MutationPlan` produce valid bounded plan without `html`/`css`/`js`/`assets` |
| GEN-07 | Non-Web Mutation | `refactor the Go worker module for clarity` → `MutationPlan` with `REFACTOR` operation, `SINGLE or MULTI`, bounded steps, evidence-backed refs |
| GEN-08 | Investigation Does Not Imply Mutation | `investigate a race condition in worker` → `ProblemSolvingPlan` with `INVESTIGATE` (not `MUTATE`), `MutationPlan` is `UNRESOLVED` or `PARTIAL` with no forced web `CREATE`/`MODIFY` |
| GEN-09 | Authorization Isolation | No `Grant`/`Authorize`/`Approve` method or field on `ProblemSurface`/`ChangeSurface`/`MutationPlan`/`ProblemSolvingPlan` (reflection) |
| GEN-10 | Scheduler Isolation | No `Schedule`/`Scheduler`/`Execute`/`Run` method/field on the same four types |
| GEN-11 | No Invented Targets | Unevidenced `dashboard.tsx` never appears in `ProblemSurface`/`ChangeSurface`/`MutationPlan`; unknown understanding → empty `UNRESOLVED` surfaces |
| GEN-12 | Digest / Staleness Preservation | `IsStale`/`DigestMatches` for `ProjectUnderstanding`/`ProblemSurface`/`ChangeSurface`/`MutationPlan`/`ProblemSolvingPlan`; material change invalidates both understanding and surfaces; plan digests bind correctly |
| Arch | Core→Web dependency | Core files contain no `StaticWeb` type usage and no `adapters/web` import; web adapter does import domain-neutral contracts |
| Arch | Planning has no execution authority | `ProblemSolvingPlan`/`ProblemSurface` types have no `Exec`/`Run`/`Apply`/`Mutate` methods |
| Matrix | Nine task classes | `static web redesign`, `Go race-condition`, `backend latency`, `React rendering bottleneck`, `memory leak`, `flaky CI`, `architecture refactor`, `large unfamiliar-codebase`, `root-cause→fix→verify` each remain semantically valid (problem surface + problem-solving plan resolve without forcing web model) |

All `internal/architecture` lock suites remain green (`TestPhase2_NoNewMutationAuthority` updated to use `index.html + styles.css` to satisfy generic `sourceFiles ≥ 2`).

---

## 14. Regression Status

```text
go test ./... -count=1 -short
```

246 packages — all `ok` (0 failed). Selected checks:

- `go build ./...` — clean
- `go vet ./...` — clean (0 issues)
- `go test ./internal/understanding` / `changesurface` / `mutationstrategy` / `architecture` — all `ok`
- `golangci-lint` equivalent — no new issues in modified packages

Phase 0–3 lock suites (`phase0_*`, `phase1_*`, `phase2_*`, `phase3_*`) remain green after the `phase2_understanding_changesurface_test.go` fixture adjustment.

---

## 15. Remaining Phase-4 Prerequisites

The generality cut is complete; the remaining work before Adaptive Execution/Continuity is unchanged from the audit §13/§20 except that prerequisites 1–3 are now **satisfied**:

- **Satisfied:** `ProjectUnderstanding` is domain-neutral; `StaticWebSurface` is adapter-level.
- **Satisfied:** `ChangeSurface` is mutation-target surface; `ProblemSurface` exists as the broader evidence-backed surface.
- **Satisfied:** `MutationPlan` remains mutation-only; `ProblemSolvingPlan` exists with `INVESTIGATE`/`ANALYZE`/`MUTATE`/`VERIFY`/`OBSERVE` kinds (minimal, pure, no execution).
- **Remaining:** Adaptive continuation logic must feed into existing `runtime/scheduler.StepScheduler` via `TaskSpec` + `RecoveryContext` + `StateFingerprint` (no second scheduler) — not implemented in this phase by design.
- **Remaining:** `StateTransition` engine (`Observed State + Evidence + Durable task state → future adaptive decision`) must be implemented on the execution loop, consuming `events.DomainEvent` + `Observation State`, not model transcripts.
- **Remaining:** Adapter-level reasoning (`cmd worker` token matching, investigation hypothesis formation, verification result interpretation) must stay above the authorization boundary; no adapter output may become an authorization grant.
- **Remaining:** Token/attempt/time budget adaptation and provider fallback remain execution-environment concerns, not planning concerns.

---

## 16. Explicit NO-GO/GO Gate for Adaptive Execution

**GO** for Phase 4 **iff** all of the following are structurally enforceable (all now true for this phase):

```text
Domain-specific reasoning
        ↓
domain-neutral problem representation (Understanding + ProblemSurface)
        ↓
bounded proposal (ProblemSolvingPlan → MutationPlan)
        ↓
existing authorization (core/authorization)
        ↓
existing scheduler (runtime/scheduler)
        ↓
existing execution (runtime/engine, execution)
        ↓
Observed evidence (events)
```

**Hard failures (none present):**

- `web semantics in core planning` — **fixed** (no `StaticWeb` in core, no `html`/`css`/`js`/`assets` in `changesurface`/`mutationstrategy` core)
- `new execution authority` — **not created** (verified by `TestGEN09` + `phase*_NoNewMutationAuthority`)
- `new scheduler` — **not created** (`TestGEN10` + no `ProblemScheduler`)
- `planning grants authorization` — **not created** (`TestGEN09` + `phase2/understanding never authorizes`)
- `mutation plan becomes general task executor` — **not created** (`MutationPlan` stays `CREATE`/`MODIFY`/`DELETE`/`REFACTOR`/`RENAME`/`NOOP`; `ProblemSolvingPlan` is separate)
- `model transcript becomes durable truth` — **not created** (no transcript-driven state; `IsStale`/`DigestMatches` remain authoritative)

**Gate:** **GO** for Phase 4. The architecture now supports:

```text
             ┌── Web reasoning
             ├── Go reasoning
             ├── React reasoning
             ├── Systems reasoning
             ├── Performance reasoning
             └── Debugging reasoning
                     ↓
              IZEN COMMON CORE
                     ↓
          bounded authorized execution
```

One runtime. Many reasoning strategies. No domain-specific execution authority. No autonomous authority escalation. Human authorization remains the root of mutation authority.

---

*This boundary correction is not a rewrite. Web semantics were moved, not deleted; mutation semantics were preserved, not abstracted away as `map[string]any`. The core now needs only stable primitives: `evidence`, `understanding`, `surface`, `bounded step`, `authorization`, `execution`, `observation`, `state`.*
